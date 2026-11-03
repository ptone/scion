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

//go:build unix

package maintenance

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Leftover update staging after a successful update (ptone/scion#7177).

// scionUpdateTemps returns the scion-update-* entries in dir.
func scionUpdateTemps(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "scion-update-") {
			out = append(out, e.Name())
		}
	}
	return out
}

// appliedStagingDirs returns the plugin staging directories in dir, failing
// unless each is marked applied, has no new/ and keeps old/.
func appliedStagingDirs(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), pluginStagingPrefix) {
			continue
		}
		p := filepath.Join(dir, e.Name())
		if !isAppliedPluginStaging(p) {
			t.Errorf("%s is not marked applied", p)
		}
		if _, err := os.Stat(filepath.Join(p, "new")); !os.IsNotExist(err) {
			t.Errorf("%s still has new/ (%v)", p, err)
		}
		if _, err := os.Stat(filepath.Join(p, "old")); err != nil {
			t.Errorf("%s lost old/ before the restart: %v", p, err)
		}
		out = append(out, p)
	}
	return out
}

// TestBinaryUpdate_DownloadsAndStagingAtRestart: when the restart is
// requested (the hook runs just before it), the download temp directory is
// already gone and the plugin staging holds only old/ and the applied
// marker, because the restart may end the process before any later
// cleanup runs.
func TestBinaryUpdate_DownloadsAndStagingAtRestart(t *testing.T) {
	requireLinux(t)
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	gh, _, dir, signals, e := symlinkLockstepSetup(t, map[string]string{"telegram": "telegram 22"})
	gh.addLockstepRelease("nightly-20261023", "2026-10-23T04:00:00Z",
		lockstepPlugin{name: "telegram", body: "telegram 23"})

	hooked := false
	var log bytes.Buffer
	err := e.RunWithBeforeRestart(context.Background(), &log, map[string]string{"target_version": "nightly-20261023"},
		func(context.Context, PendingRestart) error {
			hooked = true
			if left := scionUpdateTemps(t, tmp); len(left) != 0 {
				t.Errorf("download temp left at the restart: %v", left)
			}
			staging := appliedStagingDirs(t, dir)
			if len(staging) != 1 {
				t.Fatalf("staging dirs at the restart = %v, want 1", staging)
			}
			wantFile(t, filepath.Join(staging[0], "old", "scion-plugin-telegram"), "telegram 22")
			wantFile(t, filepath.Join(dir, "scion-plugin-telegram"), "telegram 23")
			return nil
		})
	if err != nil || !hooked || *signals != 1 {
		t.Fatalf("Run = %v, hooked %v, signals %d\n%s", err, hooked, *signals, log.String())
	}
	if left := scionUpdateTemps(t, tmp); len(left) != 0 {
		t.Fatalf("download temp left after the run: %v", left)
	}
}

// TestBinaryUpdate_HookFailureStillRestoresAfterMark: the applied marker
// does not stop a rollback: a failing hook restores the old plugin builds
// from old/ and removes the staging.
func TestBinaryUpdate_HookFailureStillRestoresAfterMark(t *testing.T) {
	requireLinux(t)
	gh, _, dir, signals, e := symlinkLockstepSetup(t, map[string]string{"telegram": "telegram 22"})
	gh.addLockstepRelease("nightly-20261023", "2026-10-23T04:00:00Z",
		lockstepPlugin{name: "telegram", body: "telegram 23"})
	err := e.RunWithBeforeRestart(context.Background(), &bytes.Buffer{}, map[string]string{"target_version": "nightly-20261023"},
		func(context.Context, PendingRestart) error {
			if n := len(appliedStagingDirs(t, dir)); n != 1 {
				t.Fatalf("staging dirs = %d, want 1 marked", n)
			}
			return os.ErrPermission
		})
	if err == nil || !strings.Contains(err.Error(), "rolled back") {
		t.Fatalf("Run = %v, want a rollback", err)
	}
	if *signals != 0 {
		t.Fatal("restart requested")
	}
	wantFile(t, filepath.Join(dir, "scion-plugin-telegram"), "telegram 22")
	noStaging(t, dir)
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
	noStaging(t, dir)
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
