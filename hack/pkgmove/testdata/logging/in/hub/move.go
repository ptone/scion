package hub

import (
	"fmt"
	"log"
	"log/slog"
	"os"
)

type service struct{ logger *slog.Logger }

// Start logs through a package-level slog function.
func Start() { slog.Info("start") }

func (s *service) stop() { s.logger.Error("stop", "err", fmt.Errorf("x")) }

func legacy() { log.Printf("legacy %d", 1) }

func viaDefault() { slog.Default().Warn("w") }

func stdLogger(l *log.Logger) { l.Println("x") }

func quiet() string { return fmt.Sprint("no logging") }

func closure() func() { return func() { slog.Debug("in closure") } }

var onError = func(err error) { slog.Error("failed", "err", err) }

var plain = os.Getpid

// Wire uses every function so the fixture compiles.
func Wire() {
	s := &service{logger: slog.Default()}
	s.stop()
	legacy()
	viaDefault()
	stdLogger(log.Default())
	_ = quiet()
	closure()()
	onError(nil)
	_ = plain
}
