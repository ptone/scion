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

package runtime

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWipeSubstrateAgentStateForTest_NotCalledOutsideTests guards
// WipeSubstrateAgentStateForTest (substrate_runtime.go): it is exported,
// and therefore compiled into the production binary, only because
// pkg/runtimebroker's tests need it and cannot reach an unexported symbol
// in this package. Nothing in production may call it — doing so would
// silently turn every live agent record-less. Since the symbol can't be
// moved to an _test.go file (a cross-package test importing this package
// in its normal, non-test build would then fail to link), this test
// substitutes a repo-wide grep: it fails if any non-test .go file
// references the identifier anywhere but its own declaration line.
func TestWipeSubstrateAgentStateForTest_NotCalledOutsideTests(t *testing.T) {
	const symbol = "WipeSubstrateAgentStateForTest"

	root := findModuleRootForTest(t)
	var badHits []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			switch info.Name() {
			case "vendor", "testdata", "node_modules", ".git":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(data), "\n") {
			if !strings.Contains(line, symbol) {
				continue
			}
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "func "+symbol) {
				continue // the declaration itself
			}
			if strings.HasPrefix(trimmed, "//") {
				continue // its own doc comment
			}
			badHits = append(badHits, fmt.Sprintf("%s:%d: %s", path, i+1, trimmed))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking module root %q: %v", root, err)
	}
	if len(badHits) > 0 {
		t.Errorf("%s referenced outside tests and its own declaration:\n%s", symbol, strings.Join(badHits, "\n"))
	}
}

// findModuleRootForTest walks up from the current working directory (a Go
// test's package directory) until it finds go.mod.
func findModuleRootForTest(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find go.mod by walking up from the test's working directory")
		}
		dir = parent
	}
}
