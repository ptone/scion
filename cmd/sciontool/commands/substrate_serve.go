/*
Copyright 2026 The Scion Authors.
*/

package commands

import (
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/GoogleCloudPlatform/scion/pkg/sciontool/log"
	"github.com/GoogleCloudPlatform/scion/pkg/sciontool/substrate"
)

var substrateServeAddr string

// skipRootfsFixupEnv, when set to any non-empty value, skips the rootfs
// fixup at both of its call sites (runSubstrateServe's startup call and
// substrateServeRootfsFixup's /bootstrap fallback) — never the
// privilege-drop precondition (substrateServePrivilegeDropChecker /
// checkPrivilegeDropFeasible), which is wired into the Server independently
// of this env var and always re-checks the same traversability/ownership
// conditions the fixup would have corrected. Skipping the fixup can
// therefore only make bootstrap fail closed sooner, never bypass the check.
// A real actor never has a reason to set it — see each call site for why it
// exists.
const skipRootfsFixupEnv = "SCION_SUBSTRATE_TEST_SKIP_ROOTFS_FIXUP"

// rootfsFixupSkipped reports whether skipRootfsFixupEnv is set, for both
// rootfs fixup call sites to check.
func rootfsFixupSkipped() bool {
	return os.Getenv(skipRootfsFixupEnv) != ""
}

// substrateServeCmd is the template entrypoint for the `substrate` runtime
// (phase1-spec.md §2.1). It is compiled into the same sciontool binary as
// every other subcommand, so no image-build change is needed beyond what
// already builds `./cmd/sciontool/`.
var substrateServeCmd = &cobra.Command{
	Use:   "substrate-serve",
	Short: "Run the Substrate actor control server (Phase 1)",
	Long: `substrate-serve runs sciontool as PID 1 inside a Substrate actor. It
listens on the port the inbound router targets by default (:80) and serves:

  GET  /scion/v1/healthz    liveness + bootstrap state ("awaiting-bootstrap"
                            or "running"); no auth
  POST /scion/v1/bootstrap  one-shot per-agent config (env, files, start
                            command); runs the existing "sciontool init"
                            path in-process once accepted
  POST /scion/v1/exec       authenticated exec, used for message delivery
                            and other broker-side operations

/pty, /rehydrate and /tunnel/open are out of scope for Phase 1.

Known Phase 1 limitation: substrate-serve logs SIGTERM but does not forward
it to the harness and does not exit. Full eviction handling — suspending the
actor within the worker's 30-minute grace period instead of just surviving
the signal — is Phase 2 (see .design docs: findings.md D3).`,
	Run: func(cmd *cobra.Command, args []string) {
		os.Exit(runSubstrateServe(substrateServeAddr))
	},
}

func init() {
	rootCmd.AddCommand(substrateServeCmd)
	substrateServeCmd.Flags().StringVar(&substrateServeAddr, "addr", ":80",
		"Address to listen on (the router targets :80 by default; overridable for tests)")
}

// exitCodeNoUsableHarnessCwd is returned by substrate-serve's InitRunner
// wiring (never by RunInit itself) when resolveSubstrateHarnessCwd could not
// find any directory usable by the scion uid — see its doc comment. Kept
// distinct from a plain 1 for the same reason as
// exitCodePrivilegeDropRequired: so an operator reading the logged exit code
// can tell which failure this was.
const exitCodeNoUsableHarnessCwd = 18

// substrateServeInitOptions returns the InitRunOptions substrate-serve's
// InitRunner passes to RunInit for one child invocation, or an error when
// resolveSubstrateHarnessCwd found no directory usable by the scion uid at
// all — the caller must fail the harness start rather than invoke RunInit
// with an empty/unusable WorkingDir (Go chdirs after the privilege drop, so
// an unset cmd.Dir inherits this process's own cwd, "/").
func substrateServeInitOptions(forwardTermSignal bool) (InitRunOptions, error) {
	workingDir, err := resolveSubstrateHarnessCwd(defaultSubstrateHarnessCwdDeps)
	if err != nil {
		return InitRunOptions{}, err
	}
	return InitRunOptions{
		ForwardTermSignal: forwardTermSignal,
		// RequirePrivilegeDrop: true — substrate always starts the actor as
		// UID 0, so a failed/skipped privilege drop can only mean "still
		// root," never a legitimate rootless outcome (see
		// InitRunOptions.RequirePrivilegeDrop).
		RequirePrivilegeDrop: true,
		WorkingDir:           workingDir,
	}, nil
}

// substrateHarnessCwdDeps groups resolveSubstrateHarnessCwd's external
// dependencies so tests can substitute them — the same reasoning as
// privilegeDropPreconditionDeps: no test should depend on this machine's
// real SCION_WORKSPACE_PATH, filesystem, or "scion" user.
type substrateHarnessCwdDeps struct {
	getenv     func(string) string
	stat       func(string) (os.FileInfo, error)
	lookupUser func(string) (*user.User, error)
}

// defaultSubstrateHarnessCwdDeps wires resolveSubstrateHarnessCwd to the
// real process environment, filesystem, and "scion" user.
var defaultSubstrateHarnessCwdDeps = substrateHarnessCwdDeps{
	getenv: os.Getenv,
	stat:   os.Stat,
	// Wraps the scionUserLookup var in a closure, not its current value, for
	// the same reason as defaultPrivilegeDropPreconditionDeps.lookupUser.
	lookupUser: func(username string) (*user.User, error) { return scionUserLookup(username) },
}

// resolveSubstrateHarnessCwd picks the working directory the substrate
// harness child (and, via tmux's own cwd inheritance, its tmux session too —
// see the "agent"/"shell" window reasoning in the project log) should start
// in, mirroring the image's WORKDIR that ateom does not apply under
// Substrate (see InitRunOptions.WorkingDir).
//
// supervisor.Run's chdir happens via SysProcAttr.Credential AFTER the
// privilege drop to the scion uid/gid, not before, so a candidate that a
// root-only stat approves can still make the child fail to start (EACCES)
// or silently inherit substrate-serve's own cwd. Every candidate below is
// therefore verified searchable by the scion uid/gid specifically,
// including its full ancestor chain, via canSearchDir — never by trusting a
// stat this (root) process could make on its own.
//
// Resolution order:
//  1. SCION_WORKSPACE_PATH (default "/workspace"; rejected if set but not
//     absolute) if it and every ancestor directory are searchable by the
//     scion uid/gid.
//  2. The scion user's own home directory (lookupUser("scion").HomeDir —
//     the same value supervisor.Run sets as the child's HOME), under the
//     same check. This package never reads substrate-serve's own $HOME.
//  3. Neither usable: an error naming every candidate tried (quoted path
//     and reason) and the uid they were checked for — nothing else. This
//     never returns "/" and never leaves cmd.Dir to inherit this process's
//     own cwd.
//
// One log line is emitted whenever a candidate is rejected, quoting only
// the path.
func resolveSubstrateHarnessCwd(d substrateHarnessCwdDeps) (string, error) {
	scionUser, err := d.lookupUser("scion")
	if err != nil {
		return "", fmt.Errorf("substrate: cannot resolve the scion user for the harness working directory: %w", err)
	}
	uid64, uidErr := strconv.ParseUint(scionUser.Uid, 10, 32)
	gid64, gidErr := strconv.ParseUint(scionUser.Gid, 10, 32)
	if uidErr != nil || gidErr != nil {
		return "", fmt.Errorf("substrate: scion user has an unparseable uid/gid")
	}
	uid, gid := uint32(uid64), uint32(gid64)

	var tried []string
	tryCandidate := func(path string) bool {
		ok, reason := dirUsableForScion(d, path, uid, gid)
		if ok {
			return true
		}
		log.Info("substrate-serve: harness working directory candidate %q is not usable (%s)", path, reason)
		tried = append(tried, fmt.Sprintf("%q (%s)", path, reason))
		return false
	}

	workspace := d.getenv("SCION_WORKSPACE_PATH")
	switch {
	case workspace == "":
		workspace = "/workspace"
	case !filepath.IsAbs(workspace):
		log.Info("substrate-serve: SCION_WORKSPACE_PATH %q is not an absolute path", workspace)
		tried = append(tried, fmt.Sprintf("%q (not absolute)", workspace))
		workspace = ""
	}
	if workspace != "" && tryCandidate(workspace) {
		return workspace, nil
	}

	if home := scionUser.HomeDir; tryCandidate(home) {
		return home, nil
	}

	return "", fmt.Errorf("substrate: no usable harness working directory for uid %d: tried %s", uid, strings.Join(tried, ", "))
}

// dirUsableForScion reports whether candidate and every ancestor directory
// up to "/" exist, are directories, and are searchable (execute bit) by
// uid/gid — the exact traversal a chdir(candidate) needs to succeed as that
// uid. candidate itself is never "/": falling back to the root directory is
// the bug this whole resolution exists to avoid.
func dirUsableForScion(d substrateHarnessCwdDeps, candidate string, uid, gid uint32) (ok bool, reason string) {
	if candidate == "/" {
		return false, "refusing to use the root directory"
	}
	for _, dir := range append(parentDirs(candidate), candidate) {
		info, err := d.stat(dir)
		if err != nil {
			return false, "missing"
		}
		if !info.IsDir() {
			return false, "not a directory"
		}
		if !canSearchDir(info, uid, gid) {
			return false, "not searchable"
		}
	}
	return true, ""
}

// substrateServePrivilegeDropChecker is the substrate.PrivilegeDropChecker
// substrate-serve wires into its Server (see checkPrivilegeDropFeasible's
// doc comment for what it actually checks).
func substrateServePrivilegeDropChecker() error {
	return checkPrivilegeDropFeasible(defaultPrivilegeDropPreconditionDeps)
}

// substrateServeRootfsFixup is the substrate.RootfsFixup substrate-serve
// wires into its Server as call site 2 (the /bootstrap fallback — see
// fixupRootfsForScion's doc comment for call site 1, substrate-serve's own
// startup, which is the primary one). Gated on skipRootfsFixupEnv the same
// way call site 1 is; see that const's doc comment for why this never
// weakens the separately-wired privilege-drop precondition.
func substrateServeRootfsFixup() {
	if rootfsFixupSkipped() {
		return
	}
	bootstrapRootfsFixup("/")
}

// startupRootfsFixup is call site 1's own call, as a package var — the same
// reason as startReaper: a test driving runSubstrateServe needs to observe
// (and assert the ordering of) this call without it resolving the real
// "scion" user or touching a real rootfs.
var startupRootfsFixup = fixupRootfsForScionUser

// bootstrapRootfsFixup is call site 2's own call, as a package var for the
// same reason as startupRootfsFixup: a test driving substrateServeRootfsFixup
// needs to observe whether it ran without resolving the real "scion" user or
// touching a real rootfs.
var bootstrapRootfsFixup = fixupRootfsForScionUser

// newSubstrateServeServer builds the *substrate.Server substrate-serve
// mounts, wiring both the init runner and the privilege-drop precondition
// (see PrivilegeDropChecker's doc comment). Extracted so a test can drive
// the exact same wiring runSubstrateServe uses — including a missing or
// disabled precondition regressing back to Phase 1's silent behaviour —
// without starting an HTTP listener.
//
// runInit is a parameter, not the real RunInit called directly, precisely
// so a test exercising this wiring can never reach the real RunInit. A
// test that stubs it and then removes WithPrivilegeDropChecker (the
// regression this wiring exists to catch) must see its stub called and
// fail on that assertion — not have the real RunInit write this machine's
// real agent-info.json, which is exactly what happened before this was
// parameterized: bootstrap wrongly returning 200 under that mutation drove
// the real RunInit for real, in-process.
//
// The init runner's own exit code is deliberately not acted on here beyond
// what WithInitRunner's caller (handleBootstrap) already does (log it,
// flip healthz to StateInitFailed) — substrate-serve does not exit the
// process on a non-zero init. See StateInitFailed's doc comment for why:
// Substrate does not observe a PID 1 exit as a failure signal at all, so
// exiting would only lose the control server (and exec-based diagnosis)
// for no compensating benefit.
func newSubstrateServeServer(runInit func(argv []string, opts InitRunOptions) int) *substrate.Server {
	return substrate.NewServer(
		substrate.WithPrivilegeDropChecker(substrateServePrivilegeDropChecker),
		substrate.WithRootfsFixup(substrateServeRootfsFixup),
		substrate.WithInitRunner(func(argv []string, forwardTermSignal bool) int {
			opts, err := substrateServeInitOptions(forwardTermSignal)
			if err != nil {
				// Fail the harness start (see resolveSubstrateHarnessCwd's
				// doc comment): never invoke runInit with no usable
				// WorkingDir. Logged in full (paths + uid only, no
				// secrets); the exit code alone flips healthz to
				// StateInitFailed the same way any other init failure does.
				log.Error("substrate-serve: %v", err)
				return exitCodeNoUsableHarnessCwd
			}
			return runInit(argv, opts)
		}),
	)
}

func runSubstrateServe(addr string) int {
	// substrate-serve is PID 1 inside the actor: reap reparented zombies the
	// same way `sciontool init` does. RunInit (invoked after bootstrap)
	// starts its own reaper too; a second startReaper call is harmless
	// (each independently drains SIGCHLD via WNOHANG) and this one covers
	// the awaiting-bootstrap window before RunInit ever runs. See
	// startReaper's own doc comment (init.go) for why this is a package var
	// rather than calling supervisor.StartReaper directly.
	startReaper()

	// Call site 1 (primary): fix up the rootfs before /healthz can ever
	// report ready. This runs during the golden boot, so the corrected
	// rootfs is captured in the snapshot and a restored actor never redoes
	// the copy-up of the whole home tree — doing this only in /bootstrap
	// would copy up the entire home (including harness installs) on every
	// actor start and hurt warm start. See fixupRootfsForScion's doc
	// comment for what it actually fixes and why.
	//
	// rootfsFixupSkipped is checked here, not left implicit: this call site
	// runs against a real, unscrubbed "/" and real "scion" home whenever
	// this binary is actually exec'd rather than driven in-process by a
	// test (e.g. TestSubstrateServeCommand_Integration_SIGTERMNotForwarded's
	// real subprocess) — no test-binary TestMain sandboxing reaches a real
	// exec'd child's own process. There is no legitimate reason for a real
	// actor to ever set skipRootfsFixupEnv: see its own doc comment for why
	// this is safe to skip (the precondition below is unaffected). Not read
	// through the injectable startupRootfsFixup var: this check is about
	// whether to call it at all, which a test replacing that var already
	// controls directly.
	if !rootfsFixupSkipped() {
		startupRootfsFixup("/")
	}

	srv := newSubstrateServeServer(RunInit)

	httpServer := &http.Server{
		Addr:    addr,
		Handler: srv.Handler(),
	}

	// Phase 1 (findings.md D3): log SIGTERM and keep running. Do not forward
	// it to the harness and do not exit — an evicting worker sends SIGTERM
	// with a 30-minute grace period before SIGKILL, and killing the harness
	// immediately would turn a recoverable eviction into a lost agent. The
	// broker is expected to react to the eviction notice (reported to the
	// Hub by RunInit's own state reporting) and drive a real suspend/resume
	// in Phase 2; Phase 1 only guarantees survival of the signal itself.
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGTERM)
	// signal.Stop followed by close lets the goroutine below exit via its
	// range loop on any return path, instead of leaking a goroutine parked
	// on sigChan for the life of the process on every call. See the
	// project log for why this matters even though production only ever
	// calls this once.
	defer func() {
		signal.Stop(sigChan)
		close(sigChan)
	}()
	go func() {
		for sig := range sigChan {
			log.Info("substrate-serve: received %s; not forwarding to harness (Phase 1 limitation, full eviction handling is Phase 2)", sig)
		}
	}()

	log.Info("substrate-serve listening on %s", addr)
	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Error("substrate-serve: ListenAndServe failed: %v", err)
		return 1
	}
	return 0
}
