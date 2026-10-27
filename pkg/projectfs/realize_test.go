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

package projectfs

import (
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
)

// TestRealize_TargetUnchanged pins the design constraint
// (P4 builds in-container attachment paths from Mount.Target): Realize keeps
// the Mount's target as the container path for bind and pod mounts alike.
func TestRealize_TargetUnchanged(t *testing.T) {
	nfs := Layout{Backend: BackendNFS, HostBase: "/mnt/nfs/a", SubPathRoot: "projects", Share: Share{ID: "a", PVName: "a-pv"}}
	local := Layout{Backend: BackendLocal, HostBase: "/home/u/.scion"}
	mounts := []Mount{
		SharedDirMount(api.SharedDir{Name: "scratchpad"}, ""),
		SharedDirMount(api.SharedDir{Name: "notes", InWorkspace: true, ReadOnly: true}, "/repo-root/sub"),
		WorkspaceMount(ModeSharedPlain, ""),
	}
	for _, m := range mounts {
		r, err := nfs.Resolve(Project{ID: testPID}, "", m)
		if err != nil {
			t.Fatalf("%s: %v", m.Name, err)
		}
		for _, kind := range []RuntimeKind{RuntimeBind, RuntimePod} {
			got, err := nfs.Realize(r, kind)
			if err != nil {
				t.Fatalf("%s %s: %v", m.Name, kind, err)
			}
			if got.Target != m.Target || got.Target != r.Target {
				t.Errorf("%s %s: realized target %q, want the Mount's target %q", m.Name, kind, got.Target, m.Target)
			}
			if got.ReadOnly != m.ReadOnly {
				t.Errorf("%s %s: read-only %v, want %v", m.Name, kind, got.ReadOnly, m.ReadOnly)
			}
		}
		bind, _ := nfs.Realize(r, RuntimeBind)
		if bind.Source != r.HostPath {
			t.Errorf("%s: bind source %q, want %q", m.Name, bind.Source, r.HostPath)
		}
		pod, _ := nfs.Realize(r, RuntimePod)
		if pod.ClaimName != "a-pv" || pod.SubPath != r.SubPath {
			t.Errorf("%s: pod mount claim %q subPath %q, want a-pv %q", m.Name, pod.ClaimName, pod.SubPath, r.SubPath)
		}
	}
	r, err := local.Resolve(Project{ID: testPID, Slug: "acme"}, "", mounts[0])
	if err != nil {
		t.Fatal(err)
	}
	got, err := local.Realize(r, RuntimeBind)
	if err != nil || got.Target != mounts[0].Target || got.Source != r.HostPath {
		t.Errorf("local bind = %+v, %v", got, err)
	}
	if _, err := local.Realize(r, RuntimePod); err == nil {
		t.Error("a local directory is not on a shared claim")
	}
	if _, err := nfs.Realize(Resolved{Backend: BackendNFS, HostPath: "/x", SubPath: "p"}, RuntimeBind); err == nil {
		t.Error("a mount without a target must not realize")
	}
	if _, err := nfs.Realize(Resolved{Backend: BackendNFS, Target: "/t", SubPath: "/abs"}, RuntimePod); err == nil {
		t.Error("an absolute subPath must not realize")
	}
	if _, err := nfs.Realize(r, "bogus"); err == nil {
		t.Error("unknown runtime kind")
	}
}

// TestProjectConfigEntry: the read-only .scion entry of design §P1a rule
// 3 is the workspace's own entry (host, share and container paths all under
// the workspace), never the project root.
func TestProjectConfigEntry(t *testing.T) {
	nfs := Layout{Backend: BackendNFS, HostBase: "/mnt/nfs/a", SubPathRoot: "projects", Share: Share{ID: "a", Export: "/e", PVName: "a-pv"}}
	ws, err := nfs.Resolve(Project{ID: testPID}, "", WorkspaceMount(ModeSharedPlain, ""))
	if err != nil {
		t.Fatal(err)
	}
	entry, err := ProjectConfigEntry(ws)
	if err != nil {
		t.Fatal(err)
	}
	want := Resolved{
		Backend: BackendNFS, HostBase: "/mnt/nfs/a", SubPathRoot: "projects",
		HostPath: "/mnt/nfs/a/projects/" + testPID + "/workspace/.scion",
		RelPath:  "projects/" + testPID + "/workspace/.scion",
		SubPath:  "projects/" + testPID + "/workspace/.scion", ServerPath: "/e/projects/" + testPID + "/workspace/.scion",
		Target: "/workspace/.scion", Kind: KindProjectConfig, Name: ".scion", ReadOnly: true, ClaimName: "a-pv",
	}
	if entry != want {
		t.Errorf("entry = %+v\nwant    %+v", entry, want)
	}
	pr, _ := nfs.ProjectRoot(testPID)
	if entry.SubPath == pr.SubPath+"/.scion" {
		t.Error("the entry must not be the project root's .scion")
	}
	pod, err := nfs.Realize(entry, RuntimePod)
	if err != nil || pod != (Realized{ClaimName: "a-pv", SubPath: want.SubPath, Target: "/workspace/.scion", ReadOnly: true}) {
		t.Errorf("pod realize = %+v, %v", pod, err)
	}
	if _, err := ProjectConfigEntry(Resolved{Kind: KindSharedDir, HostPath: "/x", Target: "/y"}); err == nil {
		t.Error("a shared dir has no configuration entry")
	}
	if _, err := ProjectConfigEntry(Resolved{Kind: KindWorkspace, HostPath: "/x"}); err == nil {
		t.Error("a workspace without a target has no entry target")
	}
}

// TestRealize_SplitUsesItsOwnClaim: a shared dir resolved through the
// transitional SharedDirs split is realized on that share's claim, not on
// the outer Layout's.
func TestRealize_SplitUsesItsOwnClaim(t *testing.T) {
	sd := Layout{Backend: BackendNFS, HostBase: "/mnt/sd/s", SubPathRoot: "projects", Share: Share{ID: "s", PVName: "sd-pv"}}
	outer := Layout{Backend: BackendLocal, HostBase: "/home/u/.scion", Share: Share{PVName: "outer-pv"}, SharedDirs: &sd}
	r, err := outer.Resolve(Project{ID: testPID}, "", SharedDirMount(api.SharedDir{Name: "scratchpad"}, ""))
	if err != nil {
		t.Fatal(err)
	}
	got, err := outer.Realize(r, RuntimePod)
	if err != nil {
		t.Fatal(err)
	}
	if got.ClaimName != "sd-pv" {
		t.Errorf("claim = %q, want the split share's sd-pv", got.ClaimName)
	}
}

// TestUserVolumeMount pins the Mount of a `volumes:` entry: agent-scoped,
// user-owned (design OQ-R2), named by its target.
func TestUserVolumeMount(t *testing.T) {
	got := UserVolumeMount("/host/data", "/data", true)
	want := Mount{Kind: KindVolume, Name: "/data", Scope: ScopeAgent, Owner: OwnerUser, Provision: ProvisionNone, Target: "/data", ReadOnly: true, UserSource: "/host/data"}
	if got != want {
		t.Errorf("UserVolumeMount = %+v, want %+v", got, want)
	}
}

// TestRealize_UserVolume: a `volumes:` entry resolves to its own source on
// any Layout and is realized exactly as declared, a bind of the source or
// a claimless pod mount at the target (ptone/scion#5837, no behaviour
// change).
func TestRealize_UserVolume(t *testing.T) {
	layouts := map[string]Layout{
		"zero":  {},
		"local": {Backend: BackendLocal, HostBase: "/home/u/.scion"},
		"nfs":   {Backend: BackendNFS, HostBase: "/mnt/nfs/a", SubPathRoot: "projects", Share: Share{ID: "a", PVName: "a-pv"}},
	}
	for name, l := range layouts {
		for _, ro := range []bool{false, true} {
			r, err := l.Resolve(Project{}, "", UserVolumeMount("/host/data", "/data", ro))
			if err != nil {
				t.Fatalf("%s: Resolve: %v", name, err)
			}
			if r.HostPath != "/host/data" || r.Target != "/data" || r.ReadOnly != ro || r.Kind != KindVolume || r.SubPath != "" || r.ClaimName != "" {
				t.Errorf("%s: Resolve = %+v", name, r)
			}
			bind, err := l.Realize(r, RuntimeBind)
			if err != nil {
				t.Fatalf("%s: Realize bind: %v", name, err)
			}
			if want := (Realized{Source: "/host/data", Target: "/data", ReadOnly: ro}); bind != want {
				t.Errorf("%s: bind = %+v, want %+v", name, bind, want)
			}
			pod, err := l.Realize(r, RuntimePod)
			if err != nil {
				t.Fatalf("%s: Realize pod: %v", name, err)
			}
			if want := (Realized{Target: "/data", ReadOnly: ro}); pod != want {
				t.Errorf("%s: pod = %+v, want %+v", name, pod, want)
			}
			if _, err := l.Realize(r, RuntimeKind("other")); err == nil {
				t.Errorf("%s: unknown runtime kind realized", name)
			}
		}
	}
}

// TestRealize_UserVolumeVerbatim: an entry the runtimes accepted before
// (an empty source or target included) is never refused by Resolve or
// Realize; the entry's own checks are api.VolumeMount.Validate's.
func TestRealize_UserVolumeVerbatim(t *testing.T) {
	for _, m := range []Mount{UserVolumeMount("", "/t", false), UserVolumeMount("/s", "", false), UserVolumeMount("rel/dir", "~/x", false)} {
		r, err := Layout{}.Resolve(Project{}, "", m)
		if err != nil {
			t.Fatalf("%+v: Resolve: %v", m, err)
		}
		got, err := Layout{}.Realize(r, RuntimeBind)
		if err != nil {
			t.Fatalf("%+v: Realize: %v", m, err)
		}
		if got.Source != m.UserSource || got.Target != m.Target {
			t.Errorf("%+v: realized %+v", m, got)
		}
	}
}

// TestResolve_VolumeMustBeUserAgentScoped: a volume Mount that is not
// Owner=user, Scope=agent is refused.
func TestResolve_VolumeMustBeUserAgentScoped(t *testing.T) {
	m := UserVolumeMount("/s", "/t", false)
	m.Owner = OwnerScion
	if _, err := (Layout{}).Resolve(Project{}, "", m); err == nil {
		t.Error("scion-owned volume resolved")
	}
	m = UserVolumeMount("/s", "/t", false)
	m.Scope = ScopeProject
	if _, err := (Layout{}).Resolve(Project{}, "", m); err == nil {
		t.Error("project-scoped volume resolved")
	}
}
