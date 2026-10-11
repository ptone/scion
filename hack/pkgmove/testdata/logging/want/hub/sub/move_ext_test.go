package sub_test

import (
	stdlog "log"
	"testing"

	"example.com/fx/hub/sub"
)

func TestWire(t *testing.T) {
	stdlog.Print("wire")
	sub.Wire()
}
