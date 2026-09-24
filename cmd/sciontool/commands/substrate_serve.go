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

func runSubstrateServe(addr string) int {
	// substrate-serve is PID 1 inside the actor: reap reparented zombies the
	// same way `sciontool init` does. RunInit (invoked after bootstrap)
	// starts its own reaper too; a second StartReaper call is harmless
	// (each independently drains SIGCHLD via WNOHANG) and this one covers
	// the awaiting-bootstrap window before RunInit ever runs.
	supervisor.StartReaper()

	srv := substrate.NewServer(
		substrate.WithInitRunner(func(argv []string, forwardTermSignal bool) int {
			// RequirePrivilegeDrop: true — substrate always starts the actor
			// as UID 0, so a failed/skipped privilege drop can only mean
			// "still root," never a legitimate rootless outcome (see
			// InitRunOptions.RequirePrivilegeDrop). This is a flag passed
			// here at the substrate-serve entry path, not an env var a
			// workload could set itself.
			return RunInit(argv, InitRunOptions{ForwardTermSignal: forwardTermSignal, RequirePrivilegeDrop: true})
		}),
	)

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
