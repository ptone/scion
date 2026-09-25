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
// (substrate-runtime.md §5). It is compiled into the same sciontool binary as
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
the signal — is Phase 2 (see .design docs: substrate-runtime.md §11).`,
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
	// One line per start, quoting only the path: a later chdir failure
	// (e.g. a TOCTOU race) surfaces as a bare "permission denied" that
	// doesn't name the directory, so this is what makes that diagnosable.
	log.Info("substrate-serve: harness working directory %q", workingDir)
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
	getenv func(string) string
	stat   func(string) (os.FileInfo, error)
	// evalSymlinks resolves a candidate to its target, the same way
	// dirUsableForScion needs to in order to check the target's own
	// ancestors (stat alone follows the final symlink but says nothing
	// about what's above it) — see dirUsableForScion's doc comment.
	evalSymlinks func(string) (string, error)
	lookupUser   func(string) (*user.User, error)
}

// defaultSubstrateHarnessCwdDeps wires resolveSubstrateHarnessCwd to the
// real process environment, filesystem, and "scion" user.
var defaultSubstrateHarnessCwdDeps = substrateHarnessCwdDeps{
	getenv:       os.Getenv,
	stat:         os.Stat,
	evalSymlinks: filepath.EvalSymlinks,
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
//     normally equal to the HOME supervisor.Run sets, though supervisor
//     derives that value independently as "/home/"+Username rather than
//     from this same lookup), under the same check. This package never
//     reads substrate-serve's own $HOME.
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

	// chosen holds tryCandidate's own filepath.Clean of whichever candidate
	// passed, so the canonical (but still logical — see dirUsableForScion's
	// doc comment on symlinks) spelling is what gets returned and, later,
	// what PWD carries — never the raw, possibly non-canonical input.
	var tried []string
	var chosen string
	tryCandidate := func(path string) bool {
		clean := filepath.Clean(path)
		ok, reason := dirUsableForScion(d, clean, uid, gid)
		if ok {
			chosen = clean
			return true
		}
		log.Info("substrate-serve: harness working directory candidate %q is not usable (%s)", clean, reason)
		tried = append(tried, fmt.Sprintf("%q (%s)", clean, reason))
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
		return chosen, nil
	}

	if home := scionUser.HomeDir; tryCandidate(home) {
		return chosen, nil
	}

	return "", fmt.Errorf("substrate: no usable harness working directory for uid %d: tried %s", uid, strings.Join(tried, ", "))
}

// dirUsableForScion reports whether candidate and every ancestor directory
// up to "/" exist, are directories, and are searchable (execute bit) by
// uid/gid — the exact traversal a chdir(candidate) needs to succeed as that
// uid. candidate is filepath.Clean'd first, so a non-canonical spelling
// (e.g. "/.", "//", or "/tmp/..") can't slip past the "never '/'" guard —
// parentDirs already cleans its own output, so an uncleaned candidate could
// reach that guard already reduced to "/" and pass it. The cleaned value is
// what dirsSearchable is walked against; candidate itself is never "/"
// after cleaning.
//
// candidate must also be absolute. resolveSubstrateHarnessCwd's own switch
// already rejects a non-absolute SCION_WORKSPACE_PATH before ever calling
// here, but scionUser.HomeDir has no such upstream check, and
// filepath.Clean("") == "." (a stdlib quirk, not a filesystem fact) — so an
// /etc/passwd entry with an empty or otherwise relative home directory would
// otherwise reach here as a relative candidate that "candidate == '/'"
// doesn't catch. A relative cmd.Dir is resolved by the kernel against
// substrate-serve's OWN process cwd at chdir time (typically "/" for a
// container's PID 1 before any WORKDIR is applied), so this is a second
// "never '/'" vector, distinct from a literal "/" or a symlink resolving to
// it, and closed here at the same choke point.
//
// stat(dir) follows the final symlink in dir, but says nothing about a
// symlink's target's own ancestors — a candidate that is itself a symlink
// (e.g. "/workspace" -> "/data/ws") can pass the lexical walk above while
// still being unreachable if "/data" isn't searchable, since chdir has to
// traverse the resolved path too. So, separately, the candidate is resolved
// with EvalSymlinks and — only when that changes anything — the resolved
// path's own ancestor chain is walked the same way. An EvalSymlinks error
// (a broken symlink, a cycle, ...) makes the candidate unusable outright,
// with that error as the reason. A resolved target of exactly "/" is
// rejected outright too, for the same "never '/'" reason as the lexical
// guard above — "/" is always searchable, so it would otherwise sail
// through the resolved-chain walk below. Either way, the *candidate* (never
// the resolved path) is what the caller returns, so PWD/cmd.Dir stay
// logical.
func dirUsableForScion(d substrateHarnessCwdDeps, candidate string, uid, gid uint32) (ok bool, reason string) {
	candidate = filepath.Clean(candidate)
	if !filepath.IsAbs(candidate) {
		return false, "not absolute"
	}
	if candidate == "/" {
		return false, "refusing to use the root directory"
	}
	if ok, reason := dirsSearchable(d, append(parentDirs(candidate), candidate), uid, gid); !ok {
		return false, reason
	}

	real, err := d.evalSymlinks(candidate)
	if err != nil {
		return false, fmt.Sprintf("cannot resolve symlinks: %q", err.Error())
	}
	// A candidate that is itself fine lexically (never "/", per the guard
	// above) can still be a symlink chain that resolves to "/" — e.g.
	// SCION_WORKSPACE_PATH or the scion HomeDir pointing at a bind mount
	// that itself symlinks to "/". "/" is always searchable by everyone, so
	// without this check dirsSearchable below would happily approve it, and
	// the caller would return the logical candidate while its EFFECTIVE cwd
	// (what chdir/PWD would actually resolve through) is "/" — exactly the
	// "never /" constraint this whole resolver exists to uphold.
	if real == "/" {
		return false, "resolves to /"
	}
	if real != candidate {
		if ok, reason := dirsSearchable(d, append(parentDirs(real), real), uid, gid); !ok {
			return false, reason
		}
	}
	return true, ""
}

// dirsSearchable reports whether every directory in dirs exists, is a
// directory, and is searchable (execute bit) by uid/gid — the shared walk
// dirUsableForScion runs once for candidate's own lexical ancestor chain
// and, when it differs, again for its resolved (symlink target) chain.
func dirsSearchable(d substrateHarnessCwdDeps, dirs []string, uid, gid uint32) (ok bool, reason string) {
	for _, dir := range dirs {
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

// substrateServeReportCwdFailure reports the no-usable-harness-cwd (exit-18)
// failure to the Hub the same way requirePrivilegeDropOrFail's failure does
// in RunInit (init.go): a best-effort direct Hub call plus local agent-info
// state, via the shared reportInitFailure helper. Without this, the Hub is
// never told the agent failed on this path, since it returns before
// RunInit — and hence before RunInit's own reportInitFailure calls — ever
// runs; only the actor log and healthz's StateInitFailed would show it. See
// reportInitFailure's doc comment for why the direct Hub call is the
// primary signal on substrate specifically. cause is always
// resolveSubstrateHarnessCwd's own paths+uid-only error, never one built
// from raw input, so it's safe to surface verbatim per reportInitFailure's
// contract.
//
// agentHome (where the local agent-info.json write lands) mirrors
// resolveAgentHome's own rootless fallback: the scion user's home when it
// can be looked up, else $HOME. Substrate always runs this path as root
// before any privilege drop, so — unlike the harness child itself — this
// process can typically still write there even when resolveSubstrateHarnessCwd
// judged the same directory unusable for the dropped-privilege child.
func substrateServeReportCwdFailure(d substrateHarnessCwdDeps, cause error) {
	agentHome := os.Getenv("HOME")
	if scionUser, err := d.lookupUser("scion"); err == nil {
		agentHome = scionUser.HomeDir
	}
	reportInitFailure(agentHome, cause)
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

// substrateServeInitRunner builds the substrate.InitRunner that resolves the
// harness working directory and then delegates to runInit. Extracted from
// newSubstrateServeServer so a test can drive it directly — including the
// no-usable-cwd path, which must return exitCodeNoUsableHarnessCwd rather
// than delegate to runInit at all — without standing up a Server.
//
// The init runner's own exit code is deliberately not acted on here beyond
// what WithInitRunner's caller (handleBootstrap) already does (log it,
// flip healthz to StateInitFailed) — substrate-serve does not exit the
// process on a non-zero init. See StateInitFailed's doc comment for why:
// Substrate does not observe a PID 1 exit as a failure signal at all, so
// exiting would only lose the control server (and exec-based diagnosis)
// for no compensating benefit.
func substrateServeInitRunner(runInit func(argv []string, opts InitRunOptions) int) substrate.InitRunner {
	return func(argv []string, forwardTermSignal bool) int {
		opts, err := substrateServeInitOptions(forwardTermSignal)
		if err != nil {
			// Fail the harness start (see resolveSubstrateHarnessCwd's
			// doc comment): never invoke runInit with no usable
			// WorkingDir. Logged in full (paths + uid only, no
			// secrets); the exit code alone flips healthz to
			// StateInitFailed the same way any other init failure does,
			// and substrateServeReportCwdFailure gives the Hub the same
			// direct report RunInit's own failure paths would.
			log.Error("substrate-serve: %v", err)
			substrateServeReportCwdFailure(defaultSubstrateHarnessCwdDeps, err)
			return exitCodeNoUsableHarnessCwd
		}
		return runInit(argv, opts)
	}
}

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
func newSubstrateServeServer(runInit func(argv []string, opts InitRunOptions) int) *substrate.Server {
	return substrate.NewServer(
		substrate.WithPrivilegeDropChecker(substrateServePrivilegeDropChecker),
		substrate.WithRootfsFixup(substrateServeRootfsFixup),
		substrate.WithInitRunner(substrateServeInitRunner(runInit)),
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

	// Phase 1 (substrate-runtime.md §5.6): log SIGTERM and keep running. Do not forward
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
