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
	"errors"
	"strings"

	"github.com/spf13/cobra"
)

// windowsAllowed lists the commands the Windows build runs. It uses the
// same dot-separated command paths as agentAllowed, and every entry,
// parents included, is listed explicitly.
//
// It is an allowlist so that it fails closed: a command added later is
// unsupported on Windows until someone adds it here deliberately, after
// checking that it touches no local file path that is not protected on
// Windows. The server, the Runtime Broker and agent-mode commands are
// deliberately absent (whoami, expose and unexpose read in-container token
// files). Widening the set is a product decision.
var windowsAllowed = map[string]bool{
	"list":          true,
	"ssh":           true,
	"ssh-config":    true,
	"tunnel":        true,
	"tunnel.status": true,
	"vscode":        true,

	"message":          true,
	"message.channels": true,
	"messages":         true,
	"messages.read":    true,

	"conversation":              true,
	"conversation.list":         true,
	"conversation.messages":     true,
	"conversation.create":       true,
	"conversation.get":          true,
	"conversation.get-message":  true,
	"conversation.set-default":  true,
	"conversation.participants": true,
	"conversation.join":         true,
	"conversation.leave":        true,
	"conversation.catch-up":     true,
	"conversation.group":        true,
	"conversation.group.list":   true,
	"conversation.group.get":    true,
	"conversation.group.create": true,
	"conversation.group.rename": true,
	"conversation.group.delete": true,
	"conversation.group.add":    true,
	"conversation.group.remove": true,

	"config":            true,
	"config.list":       true,
	"config.set":        true,
	"config.get":        true,
	"config.unset":      true,
	"config.validate":   true,
	"config.migrate":    true,
	"config.dir":        true,
	"config.cd-config":  true,
	"config.cd-project": true,
	"config.schema":     true,

	"hub":                       true,
	"hub.auth":                  true,
	"hub.auth.login":            true,
	"hub.auth.logout":           true,
	"hub.auth.federation-token": true,
	"hub.secret":                true,
	"hub.secret.set":            true,
	"hub.secret.get":            true,
	"hub.secret.list":           true,
	"hub.secret.clear":          true,
	"hub.secret.update":         true,
	// hub.secret.migrate and hub.secret.migrate-names are deliberately
	// absent: they are hub-server-host admin tools that open the local hub
	// database read-write (schema migration, WAL) and can write a foreign-key
	// snapshot next to it (pkg/store/entadapter/timestamp_normalize.go). Those
	// local files are not findings §4a sites, so §3.1 keeps them gated.
	"hub.test-identity":                 true,
	"hub.test-identity.issue":           true,
	"hub.test-identity.token":           true,
	"hub.test-identity.list":            true,
	"hub.test-identity.delete":          true,
	"hub.test-identity.web-code":        true,
	"hub.test-identity.teardown":        true,
	"hub.test-identity.delegate":        true,
	"hub.test-identity.delegate.create": true,
	"hub.test-identity.delegate.list":   true,
	"hub.test-identity.delegate.revoke": true,
	"hub.test-identity.issue-admin":     true,
	"hub.test-identity.cleanup":         true,
	"hub.test-identity.provenance":      true,
	"hub.test-identity.revoke-all":      true,
	"hub.test-identity.residue":         true,
	"hub.token":                         true,
	"hub.token.create":                  true,
	"hub.token.list":                    true,
	"hub.token.revoke":                  true,
	"hub.token.delete":                  true,
	"hub.token.scopes":                  true,
	"hub.profile":                       true,
	"hub.profile.add":                   true,
	"hub.profile.list":                  true,
	"hub.profile.show":                  true,
	"hub.profile.remove":                true,
	"hub.env":                           true,
	"hub.env.set":                       true,
	"hub.env.get":                       true,
	"hub.env.list":                      true,
	"hub.env.clear":                     true,
	"hub.notifications":                 true,
	"hub.notifications.ack":             true,
	"hub.messaging":                     true,
	"hub.messaging.get":                 true,
	"hub.messaging.set":                 true,
	"hub.status":                        true,
	"hub.projects":                      true,
	"hub.projects.info":                 true,
	"hub.projects.delete":               true,
	"hub.projects.create":               true,
	// hub.brokers mutations are hub API calls only; they run nothing
	// broker-side on this machine.
	"hub.brokers":                   true,
	"hub.brokers.join-token":        true,
	"hub.brokers.join-token.create": true,
	"hub.brokers.info":              true,
	"hub.brokers.delete":            true,
	"hub.enable":                    true,
	"hub.disable":                   true,
	"hub.link":                      true,
	"hub.unlink":                    true,
	"hub.hook":                      true,
	"hub.hook.list":                 true,
	"hub.hook.show":                 true,
	"hub.hook.create":               true,
	"hub.hook.update":               true,
	"hub.hook.activate":             true,
	"hub.hook.delete":               true,
	"hub.allow-list":                true,
	"hub.allow-list.add":            true,
	"hub.allow-list.remove":         true,
	"hub.allow-list.list":           true,
	"hub.allow-list.import":         true,
	"hub.shadow":                    true,
	"hub.add":                       true,
	"hub.list":                      true,
	"hub.rename":                    true,
	"hub.remove":                    true,
	"hub.users":                     true,
	"hub.users.provision":           true,
	"hub.invite":                    true,
	"hub.invite.create":             true,
	"hub.invite.list":               true,
	"hub.invite.revoke":             true,
	"hub.invite.delete":             true,

	"version":    true,
	"help":       true,
	"completion": true,
	// cobra's own completion subcommands; the root adds them when it
	// executes, after the gate, so they are listed to keep the gate's
	// result independent of that order.
	"completion.bash":       true,
	"completion.zsh":        true,
	"completion.fish":       true,
	"completion.powershell": true,
	"global-flags":          true,
}

// errUnsupportedOnWindows is what every command outside windowsAllowed
// returns on Windows. Match it with errors.Is; the error itself names the
// command.
var errUnsupportedOnWindows = errors.New("command is not supported on Windows")

// unsupportedOnWindowsError names the refused command. It matches
// errUnsupportedOnWindows.
type unsupportedOnWindowsError struct {
	path string // space-separated, without the leading "scion"
}

func (e *unsupportedOnWindowsError) Error() string {
	return "scion " + e.path + " is not supported on Windows; the Windows build supports hub client commands only (see docs: Windows)"
}

func (e *unsupportedOnWindowsError) Is(target error) bool {
	return target == errUnsupportedOnWindows
}

// applyPlatformRestrictions gates the command tree for goos. On Windows,
// every command whose path is not in windowsAllowed is kept in the tree
// but hidden, and running it returns errUnsupportedOnWindows: removing it
// would turn "scion server start" into an unknown command, which does not
// say why. On every other platform it does nothing.
//
// A gated command's own run hooks are cleared, and it accepts any
// arguments and unknown flags, so nothing of the command runs and the
// refusal is what the user sees. PersistentPreRunE is left alone, so
// global flag handling still runs. It is called with runtime.GOOS right
// after applyModeRestrictions; tests pass goos explicitly.
func applyPlatformRestrictions(root *cobra.Command, goos string) {
	if goos != "windows" {
		return
	}
	gateCommands(root, "", func(path string) bool { return !windowsAllowed[path] })
}

// gateCommands walks the tree under parent and replaces the run function
// of every command for which gate returns true with one that refuses.
func gateCommands(parent *cobra.Command, prefix string, gate func(path string) bool) {
	for _, child := range parent.Commands() {
		path := child.Name()
		if prefix != "" {
			path = prefix + "." + path
		}
		gateCommands(child, path, gate)
		if !gate(path) {
			continue
		}
		refusal := &unsupportedOnWindowsError{path: strings.ReplaceAll(path, ".", " ")}
		child.RunE = func(*cobra.Command, []string) error { return refusal }
		child.Run = nil
		child.PreRun, child.PreRunE = nil, nil
		child.PostRun, child.PostRunE = nil, nil
		child.ValidArgsFunction = nil
		child.Args = cobra.ArbitraryArgs
		child.FParseErrWhitelist.UnknownFlags = true
		child.Hidden = true
	}
}
