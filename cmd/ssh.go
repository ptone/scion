// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/GoogleCloudPlatform/scion/pkg/config"
	"github.com/GoogleCloudPlatform/scion/pkg/tunnelclient"
)

// sshExitFailure is the ssh convention for a local or connection failure.
const sshExitFailure = 255

// sshRemoteUser is the user name sent to the agent. The agent's SSH server
// forces the container user regardless.
const sshRemoteUser = "scion"

// sshUsageLine is printed, on one line, for a usage or flag error.
const sshUsageLine = "usage: scion ssh [flags] <agent> [<command>...]"

var sshCmd = &cobra.Command{
	Use:   "ssh <agent> [<command>...]",
	Short: "Open an SSH session to an agent through scion tunnel",
	Long: `Open an SSH session to an agent with the system ssh client.

The connection runs through scion tunnel as the ProxyCommand, and the agent's
host key is taken from the hub, so no keys or known_hosts entries need to be
set up first. The agent is <agent> in the current project, <project>/<agent>,
or the only agent with that name across all projects.

Flags for scion ssh go before the agent. Everything after the agent is the
remote command and is passed to ssh unchanged, flags included.

scion ssh never prompts for authentication, registration or sync. It returns
the remote command's exit status, or 255 for a local or connection failure,
including a usage or flag error, with the reason on one stderr line.`,
	Args: cobra.ArbitraryArgs,
	RunE: runSSHCommand,
}

func init() {
	// Stop flag parsing at the agent, so the remote command's flags reach ssh.
	sshCmd.Flags().SetInterspersed(false)
	sshCmd.SetFlagErrorFunc(sshFlagError)
	rootCmd.AddCommand(sshCmd)
}

// runSSHCommand is the RunE of scion ssh.
func runSSHCommand(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "scion ssh: %s\n", sshUsageLine)
		sshExit(sshExitFailure)
		return nil
	}
	env, err := defaultSSHCommandEnv()
	if err != nil {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "scion ssh: %s\n", sshOneLine(err.Error()))
		sshExit(sshExitFailure)
		return nil
	}
	if code := runSSH(cmd.Context(), env, args); code != tunnelclient.ExitOK {
		sshExit(code)
	}
	return nil
}

// reportSSHError writes a failed scion ssh invocation's error as one plain
// line and returns 255.
func reportSSHError(w io.Writer, err error) int {
	_, _ = fmt.Fprintf(w, "scion ssh: %s\n", plainErrorLine(config.RedactURLsIn(err.Error())))
	return sshExitFailure
}

// sshFlagError reports a flag error on scion ssh as one line and exits 255,
// following the ssh convention instead of the CLI's usual error output.
func sshFlagError(cmd *cobra.Command, err error) error {
	_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "scion ssh: %s; %s\n", sshOneLine(err.Error()), sshUsageLine)
	sshExit(sshExitFailure)
	return err
}

// runSSH runs scion ssh and returns the process exit code: the remote status
// when a session ran, otherwise 255 with one line on env.stderr.
func runSSH(ctx context.Context, env *sshCommandEnv, args []string) int {
	remote := args[1:]
	// Flag parsing stops at the agent, so a "--" after it is still present;
	// it separates the remote command and is not part of it.
	if len(remote) > 0 && remote[0] == "--" {
		remote = remote[1:]
	}
	sshArgs, cleanup, err := prepareSSH(ctx, env, args[0], remote)
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		_, _ = fmt.Fprintf(env.stderr, "scion ssh: %s\n", sshOneLine(err.Error()))
		return sshExitFailure
	}
	sshBin, err := exec.LookPath("ssh")
	if err != nil {
		_, _ = fmt.Fprintln(env.stderr, "scion ssh: ssh client not found on PATH")
		return sshExitFailure
	}
	c := exec.CommandContext(ctx, sshBin, sshArgs...)
	c.Stdin = env.stdin
	c.Stdout = env.stdout
	c.Stderr = env.stderr

	// The terminal's interrupt goes to ssh, which forwards it to the remote
	// session; scion stays alive to return ssh's status.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGQUIT)
	defer signal.Stop(sigs)

	if err := c.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() >= 0 {
			return exitErr.ExitCode()
		}
		_, _ = fmt.Fprintf(env.stderr, "scion ssh: running ssh: %s\n", sshOneLine(err.Error()))
		return sshExitFailure
	}
	return tunnelclient.ExitOK
}

// prepareSSH resolves the agent and its host key and returns the ssh
// arguments. The returned cleanup removes the temporary known-hosts file.
func prepareSSH(ctx context.Context, env *sshCommandEnv, ref string, remote []string) ([]string, func(), error) {
	hub, err := resolveSSHHub(env)
	if err != nil {
		return nil, nil, err
	}
	rctx, cancel := context.WithTimeout(ctx, sshHubRequestTimeout)
	defer cancel()
	entry, err := hub.conn.resolveSSHTarget(rctx, ref, env.project)
	if err != nil {
		return nil, nil, err
	}
	key, err := hub.conn.agentSSHHostKey(rctx, "", entry.agentID)
	if err != nil {
		return nil, nil, err
	}
	if key == "" {
		return nil, nil, tunnelclient.Errorf(tunnelclient.ExitError, "agent %s/%s has no SSH host key yet", entry.projectSlug, entry.agentSlug)
	}
	norm, err := normalizeSSHHostKey(key)
	if err != nil {
		return nil, nil, tunnelclient.Errorf(tunnelclient.ExitError, "agent %s/%s has an invalid SSH host key: %v", entry.projectSlug, entry.agentSlug, err)
	}
	if err := checkSSHNameParts(hub.label, entry.projectSlug, entry.agentSlug); err != nil {
		return nil, nil, tunnelclient.Errorf(tunnelclient.ExitError, "%v", err)
	}
	if !filepath.IsAbs(env.exe) || hasControl(env.exe) {
		return nil, nil, tunnelclient.Errorf(tunnelclient.ExitError, "scion executable path %q is not a clean absolute path", env.exe)
	}
	host := sshHostName(hub.label, entry.projectSlug, entry.agentSlug)

	dir, err := os.MkdirTemp("", "scion-ssh-")
	if err != nil {
		return nil, nil, tunnelclient.Errorf(tunnelclient.ExitError, "temporary directory: %v", err)
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	knownHosts := filepath.Join(dir, "known_hosts")
	knownHostsArg, err := sshKnownHostsOptionValue(knownHosts)
	if err != nil {
		return nil, cleanup, tunnelclient.Errorf(tunnelclient.ExitError, "temporary directory %s %v", dir, err)
	}
	// The shared atomic writer (design §3.7 delta 3): a new private file in
	// the fresh temporary directory.
	if err := writeFileAtomic(knownHosts, []byte(host+" "+norm+"\n"), sshFileMode); err != nil {
		return nil, cleanup, tunnelclient.Errorf(tunnelclient.ExitError, "writing known hosts: %v", err)
	}

	proxy := strings.Join([]string{
		sshShellQuote(env.exe), "tunnel", "--stdio", "--ssh",
		"--hub", sshShellQuote(hub.hubArg),
		"--project", sshShellQuote(entry.projectSlug),
		sshShellQuote(entry.agentSlug),
	}, " ")
	sshArgs := []string{
		"-o", "ProxyCommand=" + proxy,
		"-o", "HostKeyAlias=" + host,
		"-o", "UserKnownHostsFile=" + knownHostsArg,
		// /dev/null on every OS: Win32-OpenSSH maps it to NUL when it opens
		// a file (contrib/win32/win32compat/misc.c w32_fopen_utf8 and
		// fileio.c fileio_open, NULL_DEVICE in misc_internal.h,
		// PowerShell/openssh-portable latestw_all 274893f8), and Git for
		// Windows' ssh (MSYS) has /dev/null natively.
		"-o", "GlobalKnownHostsFile=/dev/null",
		"-o", "StrictHostKeyChecking=yes",
		"-o", "BatchMode=yes",
		"-l", sshRemoteUser,
		host,
	}
	// ssh stops option parsing at the host, so the rest is the remote command.
	sshArgs = append(sshArgs, remote...)
	return sshArgs, cleanup, nil
}

// sshKnownHostsOptionValue returns path as the value of ssh's
// UserKnownHostsFile option. ssh splits that value into words (it may list
// several files) and expands % tokens in it.
//
// On unix a path with whitespace, a control character or '%' is refused,
// as before, and the hint names TMPDIR (os.TempDir).
//
// On Windows (sshQuoteForWindows) the temp directory is under %TMP% or
// %TEMP%, typically C:\Users\<account>\AppData\Local\Temp, and account
// names with spaces are common. So the path is written with forward
// slashes (ssh's word splitter treats a backslash as an escape; both
// Win32-OpenSSH and Git for Windows' ssh accept C:/ paths) and wrapped in
// double quotes when it has a space, which ssh's option parser reads as
// one word. Only a control character, '%' or '"' is refused there, and the
// hint names TMP and TEMP. (Review r1 finding 2.)
func sshKnownHostsOptionValue(path string) (string, error) {
	if !sshQuoteForWindows {
		if hasSpaceOrControl(path) || strings.Contains(path, "%") {
			return "", errors.New("contains whitespace or %; set TMPDIR to a plain path")
		}
		return path, nil
	}
	if hasControl(path) || strings.ContainsAny(path, `%"`) {
		return "", errors.New(`contains a control character, % or "; set TMP and TEMP to a plain path`)
	}
	v := strings.ReplaceAll(path, `\`, "/")
	if strings.ContainsAny(v, " \t") {
		v = `"` + v + `"`
	}
	return v, nil
}
