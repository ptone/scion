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

package conduit

import (
	"bufio"
	"bytes"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/conduit/transport"
)

// TestTransportParity runs this package's whole session-level suite again
// over the ws and grpc transports (design §3.6: frames, sessions, router
// and relay are transport-agnostic). Each run is a child process of this
// test binary with envTestTransport set, so the suite is not copied:
// every test that builds its connection through testPipe runs over the
// real transport. It logs which tests passed, and which were skipped
// because they script an in-memory-only peer (testPipe).
func TestTransportParity(t *testing.T) {
	if testTransport() != transport.Memory {
		t.Skip("parity runner runs only in the parent process")
	}
	if testing.Short() {
		t.Skip("parity runs the suite twice more")
	}
	for _, tr := range []string{transport.WS, transport.GRPC} {
		t.Run(tr, func(t *testing.T) {
			args := []string{"-test.run=.", "-test.skip=^TestTransportParity$", "-test.count=1", "-test.v"}
			cmd := exec.Command(os.Args[0], args...)
			cmd.Env = append(os.Environ(), envTestTransport+"="+tr)
			var out bytes.Buffer
			cmd.Stdout = &out
			cmd.Stderr = &out
			err := cmd.Run()
			passed, skipped, failed := parityResults(out.Bytes())
			t.Logf("suite over %s: %d passed, %d skipped (in-memory-only peers), %d failed", tr, len(passed), len(skipped), len(failed))
			for _, name := range skipped {
				t.Logf("  skipped over %s: %s", tr, name)
			}
			if err != nil || len(failed) > 0 || len(passed) == 0 {
				t.Fatalf("suite over %s failed (%v); failed tests: %v\n%s", tr, err, failed, out.String())
			}
		})
	}
}

var parityLine = regexp.MustCompile(`^\s*--- (PASS|SKIP|FAIL): (\S+)`)

// parityResults extracts top-level test results from -test.v output.
func parityResults(out []byte) (passed, skipped, failed []string) {
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		m := parityLine.FindStringSubmatch(sc.Text())
		if m == nil || strings.Contains(m[2], "/") {
			continue
		}
		switch m[1] {
		case "PASS":
			passed = append(passed, m[2])
		case "SKIP":
			skipped = append(skipped, m[2])
		case "FAIL":
			failed = append(failed, m[2])
		}
	}
	return passed, skipped, failed
}
