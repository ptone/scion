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

package substrate

import (
	"context"
	"testing"
	"time"
)

func TestCappedWriter_UnderLimitNotTruncated(t *testing.T) {
	w := newCappedWriter(10)
	n, err := w.Write([]byte("hello"))
	if err != nil || n != 5 {
		t.Fatalf("Write = (%d, %v), want (5, nil)", n, err)
	}
	if w.truncated {
		t.Error("truncated = true, want false")
	}
	if w.String() != "hello" {
		t.Errorf("String() = %q, want %q", w.String(), "hello")
	}
}

func TestCappedWriter_ExactLimitNotTruncated(t *testing.T) {
	w := newCappedWriter(5)
	_, _ = w.Write([]byte("hello"))
	if w.truncated {
		t.Error("truncated = true at exactly the limit, want false")
	}
}

func TestCappedWriter_OverLimitTruncatesAndCaps(t *testing.T) {
	w := newCappedWriter(5)
	_, _ = w.Write([]byte("hello world"))
	if !w.truncated {
		t.Error("truncated = false, want true")
	}
	if len(w.String()) != 5 {
		t.Errorf("buffered length = %d, want capped at 5", len(w.String()))
	}
	if w.String() != "hello" {
		t.Errorf("String() = %q, want %q", w.String(), "hello")
	}
}

func TestCappedWriter_SplitAcrossWrites(t *testing.T) {
	w := newCappedWriter(5)
	_, _ = w.Write([]byte("he"))
	_, _ = w.Write([]byte("llo world"))
	if !w.truncated {
		t.Error("truncated = false, want true once combined writes exceed the cap")
	}
	if w.String() != "hello" {
		t.Errorf("String() = %q, want %q", w.String(), "hello")
	}
}

func TestShellQuote_EscapesSingleQuotes(t *testing.T) {
	got := shellQuote(`it's a "test"`)
	want := `'it'"'"'s a "test"'`
	if got != want {
		t.Errorf("shellQuote = %q, want %q", got, want)
	}
}

// TestRunExec_OutputCapsAndFlags is an end-to-end test (real subprocess) of
// the 4 MiB per-stream cap required by substrate-runtime.md §5.1. It generates
// more than maxOutputBytes on stdout and confirms the response is capped at
// exactly maxOutputBytes with truncated=true, and does the same for stderr
// independently.
func TestRunExec_OutputCapsAndFlags(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns real subprocesses producing several MB of output")
	}

	// head -c is fast and available on any Linux test runner; /dev/zero
	// bytes decode fine as a string for length-only assertions.
	over := maxOutputBytes + 1024
	resp := runExec(context.Background(), "scion",
		[]string{"sh", "-c", "head -c " + itoa(over) + " /dev/zero"}, 10*time.Second)

	if resp.ExitCode != 0 {
		t.Fatalf("exit_code = %d, want 0 (stderr=%q)", resp.ExitCode, resp.Stderr)
	}
	if !resp.Truncated {
		t.Error("truncated = false, want true for output exceeding the cap")
	}
	if len(resp.Stdout) != maxOutputBytes {
		t.Errorf("stdout length = %d, want exactly %d", len(resp.Stdout), maxOutputBytes)
	}
}

func TestRunExec_StderrCappedIndependently(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns real subprocesses producing several MB of output")
	}

	over := maxOutputBytes + 1024
	resp := runExec(context.Background(), "scion",
		[]string{"sh", "-c", "head -c " + itoa(over) + " /dev/zero 1>&2"}, 10*time.Second)

	if !resp.Truncated {
		t.Error("truncated = false, want true when stderr alone exceeds the cap")
	}
	if len(resp.Stderr) != maxOutputBytes {
		t.Errorf("stderr length = %d, want exactly %d", len(resp.Stderr), maxOutputBytes)
	}
	if len(resp.Stdout) != 0 {
		t.Errorf("stdout length = %d, want 0", len(resp.Stdout))
	}
}

func TestRunExec_TimeoutKillsProcess(t *testing.T) {
	if testing.Short() {
		t.Skip("waits on a real subprocess timeout")
	}
	start := time.Now()
	resp := runExec(context.Background(), "scion", []string{"sleep", "30"}, 300*time.Millisecond)
	elapsed := time.Since(start)

	if elapsed > 5*time.Second {
		t.Fatalf("runExec took %v, want it to be killed near the 300ms timeout", elapsed)
	}
	if resp.ExitCode == 0 {
		t.Errorf("exit_code = 0, want non-zero for a timed-out command")
	}
}

// itoa avoids pulling in strconv just for a couple of formatted numbers in
// shell commands built above.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
