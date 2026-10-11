package hub_test

import (
	stdlog "log"
	"testing"

	"example.com/fx/hub"
)

func TestWire(t *testing.T) {
	stdlog.Print("wire")
	hub.Wire()
}
