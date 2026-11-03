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

package maintenance

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Pure filesystem tests of the plugin staging cleanup (ptone/scion#7177).
// Untagged: they use no unix-only call, and RemoveAppliedPluginStaging is
// compiled into the Windows cmd/scion.

// stagingEntries returns the plugin staging directories in dir.
func stagingEntries(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), pluginStagingPrefix) {
			out = append(out, e.Name())
		}
	}
	return out
}

// TestRemoveStalePluginStaging_AppliedIsQuiet: the next update removes a
// staging directory a successful update left without the "interrupted or
// failed" warning; an unmarked one still gets it.
func TestRemoveStalePluginStaging_AppliedIsQuiet(t *testing.T) {
	dir := t.TempDir()
	applied := filepath.Join(dir, pluginStagingPrefix+"done")
	interrupted := filepath.Join(dir, pluginStagingPrefix+"cut")
	for _, d := range []string{filepath.Join(applied, "old"), filepath.Join(interrupted, "new")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(applied, pluginStagingAppliedMarker), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	var log bytes.Buffer
	removeStalePluginStaging(&log, dir)
	out := log.String()
	if !strings.Contains(out, "Removing "+applied+", the previous plugin builds kept by an earlier successful update.") {
		t.Errorf("no plain note for the applied staging:\n%s", out)
	}
	if strings.Contains(out, "WARNING: removing "+applied) {
		t.Errorf("applied staging got the interrupted-or-failed warning:\n%s", out)
	}
	if !strings.Contains(out, "WARNING: removing "+interrupted+", left by an interrupted or failed plugin update") {
		t.Errorf("unmarked staging lost its warning:\n%s", out)
	}
	if left := stagingEntries(t, dir); len(left) != 0 {
		t.Fatalf("staging left: %v", left)
	}
}

// TestRemoveAppliedPluginStaging: the startup cleanup removes only marked
// staging directories; an unmarked one (an interrupted run, or a failed
// restore holding the only old builds) and a marker that is not a regular
// file are left; a missing directory is not an error.
func TestRemoveAppliedPluginStaging(t *testing.T) {
	dir := t.TempDir()
	applied := filepath.Join(dir, pluginStagingPrefix+"done")
	unmarked := filepath.Join(dir, pluginStagingPrefix+"kept")
	fakeMarker := filepath.Join(dir, pluginStagingPrefix+"odd")
	for _, d := range []string{filepath.Join(applied, "old"), filepath.Join(unmarked, "old"), filepath.Join(fakeMarker, pluginStagingAppliedMarker)} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(applied, pluginStagingAppliedMarker), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "scion-plugin-a"), []byte("a"), 0o755); err != nil {
		t.Fatal(err)
	}
	removed, err := RemoveAppliedPluginStaging(dir)
	if err != nil || len(removed) != 1 || removed[0] != applied {
		t.Fatalf("RemoveAppliedPluginStaging = %v, %v; want [%s]", removed, err, applied)
	}
	for _, keep := range []string{unmarked, fakeMarker, filepath.Join(dir, "scion-plugin-a")} {
		if _, err := os.Stat(keep); err != nil {
			t.Errorf("%s removed: %v", keep, err)
		}
	}
	if _, err := os.Stat(applied); !os.IsNotExist(err) {
		t.Errorf("applied staging kept: %v", err)
	}
	if removed, err := RemoveAppliedPluginStaging(filepath.Join(dir, "missing")); err != nil || removed != nil {
		t.Fatalf("missing dir: %v, %v", removed, err)
	}
}
