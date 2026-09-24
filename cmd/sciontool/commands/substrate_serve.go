/*
Copyright 2026 The Scion Authors.
*/

package commands

import (
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/GoogleCloudPlatform/scion/pkg/sciontool/log"
	"github.com/GoogleCloudPlatform/scion/pkg/sciontool/substrate"
	"github.com/GoogleCloudPlatform/scion/pkg/sciontool/supervisor"
)

var substrateServeAddr string

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

// substrateServeInitOptions returns the InitRunOptions substrate-serve's
// InitRunner passes to RunInit for one child invocation. Extracted so a test
// can assert RequirePrivilegeDrop: true is actually wired here, rather than
// only testing RunInit's own requirePrivilegeDropOrFail predicate in
// isolation — flipping this to false would silently let the harness start
// as root, and nothing about testing runSubstrateServe's full HTTP server
// would otherwise catch it.
func substrateServeInitOptions(forwardTermSignal bool) InitRunOptions {
	// RequirePrivilegeDrop: true — substrate always starts the actor as UID
	// 0, so a failed/skipped privilege drop can only mean "still root,"
	// never a legitimate rootless outcome (see
	// InitRunOptions.RequirePrivilegeDrop). This is a flag passed here at
	// the substrate-serve entry path, not an env var a workload could set
	// itself.
	return InitRunOptions{ForwardTermSignal: forwardTermSignal, RequirePrivilegeDrop: true}
}

// exitOnNonZeroInit is substrate-serve's InitRunner-wrapping fail-loud
// mechanism: any non-zero RunInit exit code calls exit(code) so PID 1
// itself dies, rather than leaving substrate-serve's control server up
// reporting "running" over an actor that has nothing left happening
// inside it. This was originally scoped to only the privilege-drop
// sentinel (exitCodePrivilegeDropRequired); it was widened after a live
// git-clone failure ("in-process init exited with code 1") left an actor
// showing "running" the same way, proving the gap wasn't unique to the
// privilege-drop path.
//
// Exit code 0 is the one case deliberately left alone: it is RunInit's
// literal definition of nothing having gone wrong, whether that's the
// supervised harness process exiting cleanly on its own or (today, since
// nothing else ever asks it to stop — substrate-serve does not forward
// SIGTERM to the harness, see runSubstrateServe) any other 0 exit RunInit
// produces. Every RunInit failure that returns a specific non-zero
// path — before the harness ever launches (staged secrets, git clone,
// harness manifest, the privilege-drop gate, ...) or after it launched and
// then crashed or hit its limits — already calls reportInitFailure (or,
// for limits/crash classification, RunInit's own end-of-run reporting)
// before returning, so this never needs to guess at a message: it only
// adds the PID 1 exit, which is the one signal Substrate's own process
// supervision observes regardless of whether any of those Hub calls
// actually got through.
//
// exit is a parameter so a test can drive this without an actual os.Exit
// call terminating the test binary.
func exitOnNonZeroInit(exitCode int, exit func(int)) {
	if exitCode != 0 {
		exit(exitCode)
	}
}

// substrateServePrivilegeDropChecker is the substrate.PrivilegeDropChecker
// substrate-serve wires into its Server (see checkPrivilegeDropFeasible's
// doc comment for what it actually checks).
func substrateServePrivilegeDropChecker() error {
	return checkPrivilegeDropFeasible(defaultPrivilegeDropPreconditionDeps)
}

// newSubstrateServeServer builds the *substrate.Server substrate-serve
// mounts, wiring both the init runner and the privilege-drop precondition
// (see PrivilegeDropChecker's doc comment). Extracted so a test can drive
// the exact same wiring runSubstrateServe uses — including a missing or
// disabled precondition regressing back to Phase 1's silent behaviour —
// without starting an HTTP listener.
func newSubstrateServeServer() *substrate.Server {
	return substrate.NewServer(
		substrate.WithPrivilegeDropChecker(substrateServePrivilegeDropChecker),
		substrate.WithInitRunner(func(argv []string, forwardTermSignal bool) int {
			exitCode := RunInit(argv, substrateServeInitOptions(forwardTermSignal))
			exitOnNonZeroInit(exitCode, os.Exit)
			return exitCode
		}),
	)
}

func runSubstrateServe(addr string) int {
	// substrate-serve is PID 1 inside the actor: reap reparented zombies the
	// same way `sciontool init` does. RunInit (invoked after bootstrap)
	// starts its own reaper too; a second StartReaper call is harmless
	// (each independently drains SIGCHLD via WNOHANG) and this one covers
	// the awaiting-bootstrap window before RunInit ever runs.
	supervisor.StartReaper()

	srv := newSubstrateServeServer()

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
