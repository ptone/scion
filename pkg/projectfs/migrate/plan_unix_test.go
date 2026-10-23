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

package migrate

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/projectfs"
	"github.com/GoogleCloudPlatform/scion/pkg/projectkeys"
)

const (
	pid1 = "0f8fad5b-d9cb-469f-a165-70867728950e"
	pid2 = "7c9e6679-7425-40de-944b-e07fc1f90ae7"
)

// fakeObjects is an in-memory ObjectStore.
type fakeObjects struct {
	mu      sync.Mutex
	objects map[string]bool
	deletes []string
}

func (f *fakeObjects) HasObjects(_ context.Context, prefix string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for k := range f.objects {
		if strings.HasPrefix(k, prefix) {
			return true, nil
		}
	}
	return false, nil
}

func (f *fakeObjects) DeletePrefix(_ context.Context, prefix string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletes = append(f.deletes, prefix)
	for k := range f.objects {
		if strings.HasPrefix(k, prefix) {
			delete(f.objects, k)
		}
	}
	return nil
}

func objectPrefix(id string) string { return LegacyObjectPrefix("h1", id) }

type hubFixture struct {
	share, global string
	layout        projectfs.Layout
	objects       *fakeObjects
}

func newHubFixture(t *testing.T) hubFixture {
	t.Helper()
	f := hubFixture{share: t.TempDir(), global: t.TempDir(), objects: &fakeObjects{objects: map[string]bool{}}}
	f.layout = projectfs.Layout{Backend: projectfs.BackendNFS, HostBase: f.share, SubPathRoot: "projects"}
	return f
}

func (f hubFixture) input(projects ...ProjectRef) HubInput {
	return HubInput{
		Workspace:    f.layout,
		SharedDirs:   f.layout,
		GlobalDir:    f.global,
		Projects:     projects,
		Objects:      f.objects,
		ObjectPrefix: objectPrefix,
		Tracked:      func(context.Context, string, string) (bool, error) { return false, nil },
	}
}

func TestLegacyHubProjectsRoot(t *testing.T) {
	nfs := projectfs.Layout{Backend: projectfs.BackendNFS, HostBase: "/mnt/nfs/hub1", SubPathRoot: "tier/projects"}
	got, err := LegacyHubProjectsRoot(nfs)
	if err != nil {
		t.Fatal(err)
	}
	// nfs hubs wrote <mount_root>/<share>/hub-projects, without subpath_root.
	if want := (Tree{Base: "/mnt/nfs/hub1", Rel: legacyHubProjectsDir}); got != want {
		t.Fatalf("nfs: got %+v, want %+v", got, want)
	}
	// The volume backends wrote <mnt>/<volume>/<subpath_root>/hub-projects.
	for _, b := range []projectfs.Backend{projectfs.BackendCloudRunVolume, projectfs.BackendGKESharedVolume} {
		got, err := LegacyHubProjectsRoot(projectfs.Layout{Backend: b, HostBase: "/mnt/vol", SubPathRoot: "tier/projects"})
		if err != nil {
			t.Fatal(err)
		}
		if want := (Tree{Base: "/mnt/vol", Rel: "tier/projects/" + legacyHubProjectsDir}); got != want {
			t.Fatalf("%s: got %+v, want %+v", b, got, want)
		}
		got, err = LegacyHubProjectsRoot(projectfs.Layout{Backend: b, HostBase: "/mnt/vol"})
		if err != nil || got.Rel != "projects/"+legacyHubProjectsDir {
			t.Fatalf("%s default subpath_root: got %+v err=%v", b, got, err)
		}
	}
	for name, l := range map[string]projectfs.Layout{
		"local":        {Backend: projectfs.BackendLocal, HostBase: "/home/u/.scion"},
		"nfs-no-base":  {Backend: projectfs.BackendNFS},
		"nfs-relative": {Backend: projectfs.BackendNFS, HostBase: "mnt/x"},
		"unknown":      {Backend: "cloudrun-volume", HostBase: ""},
	} {
		if _, err := LegacyHubProjectsRoot(l); err == nil {
			t.Fatalf("%s: want an error (fail closed)", name)
		}
	}
}

// T1: on a local workspace backend the operation touches nothing.
func TestRun_LocalBackendIsNoOp(t *testing.T) {
	global := t.TempDir()
	writeTree(t, global, map[string]string{"projects/p1/a.txt": "A", "project-configs/p1__0f8fad5b/shared-dirs/s/x": "x"})
	snap := snapshot(t, global)
	objects := &fakeObjects{objects: map[string]bool{objectPrefix(pid1) + "a": true}}
	for _, dry := range []bool{true, false} {
		rep, err := Run(context.Background(), HubInput{
			Workspace:    projectfs.Layout{Backend: projectfs.BackendLocal, HostBase: global},
			SharedDirs:   projectfs.Layout{Backend: projectfs.BackendLocal, HostBase: global},
			GlobalDir:    global,
			Projects:     []ProjectRef{{ID: pid1, Slug: "p1"}},
			Objects:      objects,
			ObjectPrefix: objectPrefix,
		}, opts(dry))
		if err != nil || rep.Skipped == "" || rep.Moves() != 0 || len(rep.Projects) != 0 {
			t.Fatalf("dry=%t: err=%v skipped=%q moves=%d", dry, err, rep.Skipped, rep.Moves())
		}
	}
	if got := snapshot(t, global); !reflect.DeepEqual(got, snap) {
		t.Fatal("local backend run changed the hub's files")
	}
	if len(objects.deletes) != 0 {
		t.Fatal("local backend run deleted objects")
	}
}

func seedHubProject(t *testing.T, f hubFixture) {
	t.Helper()
	writeTree(t, f.share, map[string]string{
		"hub-projects/p1/.scion/settings.yaml": "hub:\n  project_id: " + pid1,
		"hub-projects/p1/.scion/project-id":    pid1,
		"hub-projects/p1/README.md":            "readme",
		"hub-projects/p1/src/main.go":          "package main",
	})
	writeTree(t, f.global, map[string]string{
		"projects/p1/.scion/settings.yaml":                          "older local seed",
		"projects/p1/notes.txt":                                     "local notes",
		"projects/p1/README.md":                                     "a different readme",
		"project-configs/p1__0f8fad5b/shared-dirs/scratchpad/f.txt": "scratch",
		"project-configs/p1__0f8fad5b/shared-dirs/Bad Name/g.txt":   "bad",
	})
	f.objects.objects[objectPrefix(pid1)+"README.md"] = true
	f.objects.objects["hubs/h1/workspaces/"+pid1+"/agent-1/file"] = true
}

func TestRun_SharedBackendMigratesEveryStep(t *testing.T) {
	f := newHubFixture(t)
	seedHubProject(t, f)
	in := f.input(ProjectRef{ID: pid1, Slug: "p1"})

	// A dry run first: reports moves, changes nothing.
	before := map[string]map[string]string{"share": snapshot(t, f.share), "global": snapshot(t, f.global)}
	dry, err := Run(context.Background(), in, opts(true))
	if err != nil {
		t.Fatal(err)
	}
	if dry.Moves() == 0 || len(dry.PendingProjects()) != 1 {
		t.Fatalf("dry run planned %d moves, pending %v", dry.Moves(), dry.PendingProjects())
	}
	// The invalid shared-dir name fails in the dry run too, so the plan
	// already says the objects would be kept.
	if dry.Objects[0].Outcome != ObjectsKept {
		t.Fatalf("dry-run objects = %s", dry.Objects[0].Outcome)
	}
	after := map[string]map[string]string{"share": snapshot(t, f.share), "global": snapshot(t, f.global)}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("dry run changed files")
	}
	if len(f.objects.deletes) != 0 {
		t.Fatal("dry run deleted objects")
	}

	rep, err := Run(context.Background(), in, opts(false))
	if err != nil {
		t.Fatal(err)
	}
	var log bytes.Buffer
	rep.WriteLog(&log)
	root := filepath.Join(f.share, "projects", pid1)
	ws := filepath.Join(root, "workspace")
	checks := map[string]string{
		filepath.Join(root, ".scion/settings.yaml"):                      "hub:\n  project_id: " + pid1, // step 4 before 1
		filepath.Join(root, ".scion/project-id"):                         pid1,
		filepath.Join(ws, "README.md"):                                   "readme", // step 1 rename wins
		filepath.Join(ws, "src/main.go"):                                 "package main",
		filepath.Join(ws, "notes.txt"):                                   "local notes", // step 2 copy
		filepath.Join(root, "shared-dirs/scratchpad/f.txt"):              "scratch",     // step 3
		filepath.Join(f.global, "projects/p1"+backupSuffix, "README.md"): "a different readme",
	}
	for p, want := range checks {
		if got := readFile(t, p); got != want {
			t.Fatalf("%s = %q, want %q\n%s", p, got, want, log.String())
		}
	}
	assertAbsent(t, filepath.Join(ws, ".scion"))
	assertAbsent(t, filepath.Join(f.share, "hub-projects/p1"))
	if !strings.Contains(log.String(), "CONFLICT README.md") {
		t.Fatalf("conflict not reported:\n%s", log.String())
	}
	// The invalid shared-dir name fails its step, so M2 did not succeed
	// for this project and its objects are kept.
	if !rep.Failed() || rep.Objects[0].Outcome != ObjectsKept || len(f.objects.deletes) != 0 {
		t.Fatalf("failed=%t objects=%s deletes=%v\n%s", rep.Failed(), rep.Objects[0].Outcome, f.objects.deletes, log.String())
	}

	// After the operator removes the bad entry, a re-run completes M2 for
	// the project and M3 deletes only its project-level copy.
	if err := os.RemoveAll(filepath.Join(f.global, "project-configs/p1__0f8fad5b/shared-dirs/Bad Name")); err != nil {
		t.Fatal(err)
	}
	again, err := Run(context.Background(), in, opts(false))
	if err != nil {
		t.Fatal(err)
	}
	log.Reset()
	again.WriteLog(&log)
	if again.Moves() != 0 || again.Failed() {
		t.Fatalf("re-run: moves=%d failed=%t\n%s", again.Moves(), again.Failed(), log.String())
	}
	if !strings.Contains(log.String(), "moves=0") {
		t.Fatalf("summary does not report 0 moves:\n%s", log.String())
	}
	if want := []string{objectPrefix(pid1)}; !reflect.DeepEqual(f.objects.deletes, want) || again.Objects[0].Outcome != ObjectsDeleted {
		t.Fatalf("deletes = %v (%s), want %v", f.objects.deletes, again.Objects[0].Outcome, want)
	}
	if !f.objects.objects["hubs/h1/workspaces/"+pid1+"/agent-1/file"] {
		t.Fatal("M3 deleted a per-agent sync object")
	}
	// A third run is a no-op end to end.
	third, _ := Run(context.Background(), in, opts(false))
	if third.Moves() != 0 || third.Failed() || third.Objects[0].Outcome != ObjectsNone {
		t.Fatalf("third run: moves=%d failed=%t objects=%s", third.Moves(), third.Failed(), third.Objects[0].Outcome)
	}
}

// M3 never deletes for a project whose M2 failed (a symlinked legacy tree
// is refused).
func TestRun_ObjectsKeptWhenM2Fails(t *testing.T) {
	f := newHubFixture(t)
	outside := t.TempDir()
	writeTree(t, outside, map[string]string{"a.txt": "A"})
	writeTree(t, f.share, map[string]string{"hub-projects/p1": "->" + outside})
	f.objects.objects[objectPrefix(pid1)+"a.txt"] = true
	rep, err := Run(context.Background(), f.input(ProjectRef{ID: pid1, Slug: "p1"}), opts(false))
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Failed() || rep.Objects[0].Outcome != ObjectsKept || len(f.objects.deletes) != 0 {
		t.Fatalf("failed=%t objects=%s deletes=%v", rep.Failed(), rep.Objects[0].Outcome, f.objects.deletes)
	}
	assertAbsent(t, filepath.Join(f.share, "projects", pid1, "workspace"))
}

func TestRun_EmptyPerAgentLeavesContentMovesSeed(t *testing.T) {
	f := newHubFixture(t)
	writeTree(t, f.share, map[string]string{"hub-projects/p1/.scion/settings.yaml": "s", "hub-projects/p1/data.txt": "d"})
	rep, err := Run(context.Background(), f.input(ProjectRef{ID: pid1, Slug: "p1", NoWorkspace: true}), opts(false))
	if err != nil || rep.Failed() {
		t.Fatalf("err=%v failed=%t", err, rep.Failed())
	}
	if readFile(t, filepath.Join(f.share, "projects", pid1, ".scion/settings.yaml")) != "s" {
		t.Fatal("seed not moved")
	}
	if readFile(t, filepath.Join(f.share, "hub-projects/p1/data.txt")) != "d" {
		t.Fatal("content of a project without a project-scoped workspace must stay")
	}
	if len(rep.Projects[0].Notes) != 1 {
		t.Fatalf("notes = %v", rep.Projects[0].Notes)
	}
}

func TestRun_InfraOnlyLocalTreeIsNotMoved(t *testing.T) {
	f := newHubFixture(t)
	writeTree(t, f.global, map[string]string{"projects/p1/shared-dirs/": ""})
	rep, err := Run(context.Background(), f.input(ProjectRef{ID: pid1, Slug: "p1"}), opts(false))
	if err != nil || rep.Moves() != 0 || rep.Failed() {
		t.Fatalf("err=%v moves=%d failed=%t", err, rep.Moves(), rep.Failed())
	}
	assertAbsent(t, filepath.Join(f.share, "projects", pid1, "workspace"))
}

// Git source: only untracked hub identity entries move; tracked repository
// .scion content and other untracked entries stay in the workspace.
func TestRun_GitSeedMovesOnlyUntrackedIdentity(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	f := newHubFixture(t)
	repo := filepath.Join(f.share, "hub-projects/p1")
	writeTree(t, repo, map[string]string{
		".scion/templates/t.yaml": "repo template",
		".scion/settings.yaml":    "hub seed",
		".scion/project-id":       pid1,
		".scion/other.txt":        "untracked other",
		"main.go":                 "package main",
	})
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "init.defaultBranch=main", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Dir = repo
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "GIT_CONFIG_NOSYSTEM=1"}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	git("add", ".scion/templates/t.yaml", "main.go")
	git("commit", "-q", "-m", "init")

	in := f.input(ProjectRef{ID: pid1, Slug: "p1"})
	in.Tracked = nil // the real GitTracked
	rep, err := Run(context.Background(), in, opts(false))
	if err != nil || rep.Failed() {
		var b bytes.Buffer
		rep.WriteLog(&b)
		t.Fatalf("err=%v\n%s", err, b.String())
	}
	root := filepath.Join(f.share, "projects", pid1)
	if readFile(t, filepath.Join(root, ".scion/settings.yaml")) != "hub seed" || readFile(t, filepath.Join(root, ".scion/project-id")) != pid1 {
		t.Fatal("identity entries not moved to the project root")
	}
	ws := filepath.Join(root, "workspace")
	if readFile(t, filepath.Join(ws, ".scion/templates/t.yaml")) != "repo template" {
		t.Fatal("tracked repository .scion content must stay in the workspace")
	}
	if readFile(t, filepath.Join(ws, ".scion/other.txt")) != "untracked other" {
		t.Fatal("other untracked entry must stay in the workspace")
	}
	assertAbsent(t, filepath.Join(ws, ".scion/settings.yaml"))
	if len(rep.Projects[0].Notes) != 1 || !strings.Contains(rep.Projects[0].Notes[0], "other.txt") {
		t.Fatalf("notes = %v", rep.Projects[0].Notes)
	}
}

func TestRun_GitFileGitdirLeavesSeed(t *testing.T) {
	f := newHubFixture(t)
	writeTree(t, f.share, map[string]string{"hub-projects/p1/.git": "gitdir: /elsewhere", "hub-projects/p1/.scion/settings.yaml": "s", "hub-projects/p1/a": "a"})
	in := f.input(ProjectRef{ID: pid1, Slug: "p1"})
	in.Tracked = func(context.Context, string, string) (bool, error) {
		t.Fatal("tracked check must not run for a .git file")
		return false, nil
	}
	rep, err := Run(context.Background(), in, opts(false))
	if err != nil || rep.Failed() {
		t.Fatalf("err=%v failed=%t", err, rep.Failed())
	}
	assertAbsent(t, filepath.Join(f.share, "projects", pid1, ".scion"))
	if readFile(t, filepath.Join(f.share, "projects", pid1, "workspace/.scion/settings.yaml")) != "s" {
		t.Fatal("workspace not moved")
	}
}

func TestRun_TwoProjectsIndependent(t *testing.T) {
	f := newHubFixture(t)
	writeTree(t, f.share, map[string]string{"hub-projects/p1/a": "1", "hub-projects/p2": "->/etc"})
	f.objects.objects[objectPrefix(pid1)+"a"] = true
	f.objects.objects[objectPrefix(pid2)+"a"] = true
	rep, err := Run(context.Background(), f.input(ProjectRef{ID: pid1, Slug: "p1"}, ProjectRef{ID: pid2, Slug: "p2"}), opts(false))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Projects[0].Failed() || !rep.Projects[1].Failed() {
		t.Fatalf("p1 failed=%t p2 failed=%t", rep.Projects[0].Failed(), rep.Projects[1].Failed())
	}
	if want := []string{objectPrefix(pid1)}; !reflect.DeepEqual(f.objects.deletes, want) {
		t.Fatalf("deletes = %v, want %v", f.objects.deletes, want)
	}
}

func TestCleanObjects_RefusesBadPrefix(t *testing.T) {
	store := &fakeObjects{objects: map[string]bool{"a/b": true}}
	for _, p := range []string{"", "/", "a", "/a/", "a//b/"} {
		if r := CleanObjects(context.Background(), store, p, ProjectReport{}, false); r.Outcome != ObjectsFailed {
			t.Fatalf("%q: outcome %s, want failed", p, r.Outcome)
		}
	}
	if len(store.deletes) != 0 {
		t.Fatal("deleted with a bad prefix")
	}
}

func TestMigrateProject_RejectsBadInput(t *testing.T) {
	for _, in := range []ProjectInput{
		{ID: pid1, Slug: "../x", GlobalDir: "/g"},
		{ID: pid1, Slug: "Up", GlobalDir: "/g"},
		{ID: "", Slug: "p1", GlobalDir: "/g"},
		{ID: pid1, Slug: "p1", GlobalDir: "rel"},
	} {
		if rep := MigrateProject(context.Background(), in, opts(true)); !rep.Failed() {
			t.Fatalf("%+v: want failure", in)
		}
	}
}

func TestRun_DryRunWouldDeleteObjectsButDoesNot(t *testing.T) {
	f := newHubFixture(t)
	writeTree(t, f.share, map[string]string{"hub-projects/p1/a": "1"})
	f.objects.objects[objectPrefix(pid1)+"a"] = true
	rep, err := Run(context.Background(), f.input(ProjectRef{ID: pid1, Slug: "p1"}), opts(true))
	if err != nil || rep.Failed() {
		t.Fatalf("err=%v failed=%t", err, rep.Failed())
	}
	if rep.Objects[0].Outcome != ObjectsWouldDelete || len(f.objects.deletes) != 0 || !f.objects.objects[objectPrefix(pid1)+"a"] {
		t.Fatalf("objects=%s deletes=%v", rep.Objects[0].Outcome, f.objects.deletes)
	}
}

func TestRun_VolumeBackendLegacyRoot(t *testing.T) {
	vol := t.TempDir()
	global := t.TempDir()
	writeTree(t, vol, map[string]string{"projects/hub-projects/p1/a.txt": "A"})
	layout := projectfs.Layout{Backend: projectfs.BackendCloudRunVolume, HostBase: vol, SubPathRoot: "projects"}
	rep, err := Run(context.Background(), HubInput{Workspace: layout, SharedDirs: projectfs.Layout{Backend: projectfs.BackendLocal}, GlobalDir: global, Projects: []ProjectRef{{ID: pid1, Slug: "p1"}}}, opts(false))
	if err != nil || rep.Failed() || rep.Moves() != 1 {
		t.Fatalf("err=%v failed=%t moves=%d", err, rep.Failed(), rep.Moves())
	}
	if readFile(t, filepath.Join(vol, "projects", pid1, "workspace/a.txt")) != "A" {
		t.Fatal("not moved to the uuid tree on the volume")
	}
	// Fail closed: a volume backend without a mount root is an error.
	if _, err := Run(context.Background(), HubInput{Workspace: projectfs.Layout{Backend: projectfs.BackendGKESharedVolume}, GlobalDir: global}, opts(true)); err == nil {
		t.Fatal("want an error for a volume backend without a mount root")
	}
}

func TestLegacyObjectPrefix(t *testing.T) {
	if got, want := LegacyObjectPrefix("h1", pid1), "hubs/h1/workspaces/"+pid1+"/"+projectkeys.LegacyProjectWorkspaceObjectDir+"/"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if got, want := LegacyObjectPrefix("", pid1), "workspaces/"+pid1+"/"+projectkeys.LegacyProjectWorkspaceObjectDir+"/"; got != want {
		t.Fatalf("no hub ID: got %q, want %q", got, want)
	}
	for _, c := range [][2]string{{"h1", ""}, {"h1", ".."}, {"h1", "a/b"}, {"a/b", pid1}, {"..", pid1}} {
		if got := LegacyObjectPrefix(c[0], c[1]); got != "" {
			t.Fatalf("%q: got %q, want refusal", c, got)
		}
	}
}

// M3 deletes only the project-level legacy copy: the live per-agent
// prefixes beside it (CLI bootstrap, scion sync), a sibling project whose
// ID shares a prefix, and other hubs' objects survive.
func TestRun_M3LeavesPerAgentAndSiblingObjects(t *testing.T) {
	f := newHubFixture(t)
	writeTree(t, f.share, map[string]string{"hub-projects/p1/a": "1"})
	keep := []string{
		"hubs/h1/workspaces/" + pid1 + "/agent-1/file",
		"hubs/h1/workspaces/" + pid1 + "/" + projectkeys.LegacyProjectWorkspaceObjectDir + "-extra/file",
		"hubs/h1/workspaces/" + pid1 + "x/" + projectkeys.LegacyProjectWorkspaceObjectDir + "/file",
		"hubs/h2/workspaces/" + pid1 + "/" + projectkeys.LegacyProjectWorkspaceObjectDir + "/file",
	}
	for _, k := range keep {
		f.objects.objects[k] = true
	}
	f.objects.objects[objectPrefix(pid1)+"file"] = true
	rep, err := Run(context.Background(), f.input(ProjectRef{ID: pid1, Slug: "p1"}), opts(false))
	if err != nil || rep.Failed() || rep.Objects[0].Outcome != ObjectsDeleted {
		t.Fatalf("err=%v failed=%t objects=%s", err, rep.Failed(), rep.Objects[0].Outcome)
	}
	for _, k := range keep {
		if !f.objects.objects[k] {
			t.Fatalf("M3 deleted %s", k)
		}
	}
	if f.objects.objects[objectPrefix(pid1)+"file"] {
		t.Fatal("legacy copy not deleted")
	}
}

// Q7: a hub-local tree carrying broker state is copied from, never changed;
// .scion/agents and shared-dirs are not copied; a re-run moves nothing.
func TestRun_LocalBrokerTreeIsCopyOnly(t *testing.T) {
	for _, tc := range []struct {
		name     string
		files    map[string]string
		provider bool
	}{
		{"evidence", map[string]string{
			"projects/p1/.scion/settings.yaml":    "broker settings",
			"projects/p1/.scion/project-id":       pid1,
			"projects/p1/.scion/agents/a1/home/x": "agent state",
			"projects/p1/notes.txt":               "notes",
			"projects/p1/shared-dirs/s/f":         "old shared",
		}, false},
		{"provider", map[string]string{
			"projects/p1/.scion/settings.yaml": "broker settings",
			"projects/p1/notes.txt":            "notes",
		}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newHubFixture(t)
			writeTree(t, f.global, tc.files)
			local := filepath.Join(f.global, "projects/p1")
			before := snapshot(t, local)
			in := f.input(ProjectRef{ID: pid1, Slug: "p1", LocalBrokerTree: tc.provider})

			dry, err := Run(context.Background(), in, opts(true))
			if err != nil || dry.Moves() == 0 {
				t.Fatalf("dry run: err=%v moves=%d", err, dry.Moves())
			}
			rep, err := Run(context.Background(), in, opts(false))
			if err != nil || rep.Failed() {
				var b bytes.Buffer
				rep.WriteLog(&b)
				t.Fatalf("err=%v\n%s", err, b.String())
			}
			if got := snapshot(t, local); !reflect.DeepEqual(got, before) {
				t.Fatalf("the broker's tree changed:\nbefore %v\nafter  %v", before, got)
			}
			root := filepath.Join(f.share, "projects", pid1)
			if readFile(t, filepath.Join(root, "workspace/notes.txt")) != "notes" {
				t.Fatal("content not copied")
			}
			if readFile(t, filepath.Join(root, ".scion/settings.yaml")) != "broker settings" {
				t.Fatal("seed not copied")
			}
			assertAbsent(t, filepath.Join(root, ".scion/agents"))
			assertAbsent(t, filepath.Join(root, "workspace/shared-dirs"))
			assertAbsent(t, filepath.Join(root, "workspace/.scion"))
			again, err := Run(context.Background(), in, opts(false))
			if err != nil || again.Moves() != 0 || again.Failed() {
				t.Fatalf("re-run: err=%v moves=%d failed=%t", err, again.Moves(), again.Failed())
			}
		})
	}
}

// Precondition: with a local workspace Layout the workspace steps do not
// run; a shared shared-dir Layout still gets step 3.
func TestRun_LocalWorkspaceSharedDirsOnly(t *testing.T) {
	f := newHubFixture(t)
	writeTree(t, f.global, map[string]string{
		"projects/p1/a.txt": "A",
		"project-configs/p1__0f8fad5b/shared-dirs/scratchpad/f.txt": "scratch",
	})
	in := f.input(ProjectRef{ID: pid1, Slug: "p1"})
	in.Workspace = projectfs.Layout{Backend: projectfs.BackendLocal, HostBase: f.global}
	rep, err := Run(context.Background(), in, opts(false))
	if err != nil || rep.Failed() || rep.Skipped != "" {
		t.Fatalf("err=%v failed=%t skipped=%q", err, rep.Failed(), rep.Skipped)
	}
	if readFile(t, filepath.Join(f.share, "projects", pid1, "shared-dirs/scratchpad/f.txt")) != "scratch" {
		t.Fatal("step 3 did not run")
	}
	if readFile(t, filepath.Join(f.global, "projects/p1/a.txt")) != "A" {
		t.Fatal("a workspace step ran on a local workspace Layout")
	}
	assertAbsent(t, filepath.Join(f.share, "projects", pid1, "workspace"))
	if len(rep.Projects[0].Notes) == 0 || !strings.Contains(rep.Projects[0].Notes[0], "not shared") {
		t.Fatalf("notes = %v", rep.Projects[0].Notes)
	}
}
