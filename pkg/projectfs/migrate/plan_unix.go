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
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/projectfs"
	"github.com/GoogleCloudPlatform/scion/pkg/projectkeys"
	"golang.org/x/sys/unix"
)

// legacyHubProjectsDir is the retired hub-managed project tree on a shared
// workspace backend (design-v4 G7, invariant "hub-projects/<slug> does not
// exist"). This constant is the only place the name survives (ptone/scion#5819,
// A2 exception agreed with fs-cleanup-arch); the migration that reads it is
// deleted in phase 6 (ptone/scion#5839).
const legacyHubProjectsDir = "hub-projects"

// Step names one M2 step (design-v4 §Migration M2). Steps run in the order
// of ProjectSteps, which puts the seed (4) before the workspace (1).
type Step string

const (
	StepSeed          Step = "4-scion-seed"
	StepHubProjects   Step = "1-hub-projects"
	StepLocalProjects Step = "2-local-projects"
	StepSharedDirs    Step = "3-shared-dirs"
	// StepCloudRunHomes is phase 3's slot (ptone/scion#5825): Cloud Run
	// homes agents/<agentUUID>/{home,secrets} ->
	// agents/<slug>/{home-,secrets-}<agentUUID>, only for stopped agents.
	StepCloudRunHomes Step = "5-cloudrun-homes"
)

// ProjectSteps is the execution order of the M2 steps.
var ProjectSteps = []Step{StepSeed, StepHubProjects, StepLocalProjects, StepSharedDirs, StepCloudRunHomes}

// LegacyHubProjectsRoot returns the directory under which a hub on the
// shared workspace Layout wrote hub-managed projects as <root>/<slug>,
// exactly as the retired resolver did: nfs hubs wrote
// <mount_root>/<share id>/hub-projects (no subpath_root); the volume
// backends (cloudrun-volume, gke-shared-volume) wrote
// /mnt/<volume>/<subpath_root>/hub-projects. The Layout's HostBase is that
// mount root on every shared backend. It fails closed for a Layout that is
// not durable or has no absolute HostBase (an empty volume name,
// ptone/scion#1073), rather than falling back to any other root.
func LegacyHubProjectsRoot(l projectfs.Layout) (Tree, error) {
	if !l.Backend.Shared() {
		return Tree{}, fmt.Errorf("backend %q is not a shared backend; nothing to migrate", l.Backend)
	}
	if l.HostBase == "" || !filepath.IsAbs(l.HostBase) {
		return Tree{}, fmt.Errorf("shared backend %q has no absolute mount root (host base %q)", l.Backend, l.HostBase)
	}
	if l.Backend == projectfs.BackendNFS {
		return Tree{Base: l.HostBase, Rel: legacyHubProjectsDir}, nil
	}
	sub := l.SubPathRoot
	if sub == "" {
		sub = projectfs.DefaultSubPathRoot
	}
	if !filepath.IsLocal(sub) {
		return Tree{}, fmt.Errorf("subpath_root %q is not a local path", sub)
	}
	return Tree{Base: l.HostBase, Rel: filepath.Join(sub, legacyHubProjectsDir)}, nil
}

// SharedDirTarget is where a shared dir's content goes, and how its leaf
// is created when absent (the owner's EnsureLeaf, for mode and ACL).
type SharedDirTarget struct {
	Tree    Tree
	Prepare func() error
}

// ProjectInput is everything M2 needs for one project. Targets come from
// the layout authority (pkg/projectfs); legacy sources are derived here.
type ProjectInput struct {
	ID   string
	Slug string
	// Workspace is the project-scoped workspace target
	// (<subpath_root>/<uuid>/workspace). Nil when the project has no
	// project-scoped workspace (empty-per-agent): steps 1 and 2 leave
	// legacy content in place and report it.
	Workspace *Tree
	// PrepareWorkspace creates the workspace leaf through the authority
	// before steps 1 and 2 move anything into it (nil: the mover creates
	// it).
	PrepareWorkspace func() error
	// LockWorkspace runs a rename into the workspace under the project's
	// provisioning lock (nil: no lock).
	LockWorkspace func(section func() error) error
	// ProjectRoot is <subpath_root>/<uuid>; the seed goes to its .scion.
	// Zero skips step 4.
	ProjectRoot Tree
	// HubProjects is LegacyHubProjectsRoot of the workspace Layout.
	HubProjects Tree
	// GlobalDir is the hub's own global Scion dir (~/.scion), which holds
	// projects/<slug> and project-configs/<slug>__<short>/shared-dirs.
	GlobalDir string
	// SharedDir returns the target of shared dir name. Nil when shared dirs
	// do not resolve to a shared backend on this hub: step 3 is skipped.
	SharedDir func(name string) (SharedDirTarget, error)
	// Tracked reports whether path (relative to the checkout at dir) is
	// tracked by git. Nil uses GitTracked.
	Tracked func(ctx context.Context, dir, path string) (bool, error)
	// WorkspaceOwner and SharedDirOwner are the uid:gid migrated entries
	// are chowned to, under the workspace/.scion and under shared dirs
	// (nil: owners are kept).
	WorkspaceOwner *Ownership
	SharedDirOwner *Ownership
	// CloudRunHomes is phase 3's step 5 (ptone/scion#5825); nil in phase 2.
	CloudRunHomes func(opts Options) []Result
}

// ProjectReport is the M2 report of one project.
type ProjectReport struct {
	ID      string
	Slug    string
	Results []Result
	// Notes are informational lines (entries left in place on purpose).
	Notes []string
	// OwnershipRequested: migrated entries were to be chowned (the Layout
	// names a uid/gid).
	OwnershipRequested bool
}

// Moves counts the Specs that moved (or would move) anything.
func (p ProjectReport) Moves() int {
	n := 0
	for _, r := range p.Results {
		if r.Moved() {
			n++
		}
	}
	return n
}

// OwnershipErrors returns the ownership/mode normalisation failures of
// the project's moves (empty: every migrated entry was normalised).
func (p ProjectReport) OwnershipErrors() []error {
	var errs []error
	for _, r := range flatten(p.Results) {
		if r.OwnershipErr != nil {
			errs = append(errs, r.OwnershipErr)
		}
	}
	return errs
}

// Failed reports whether any step failed. A conflict is not a failure: the
// source's version is kept in its .migrated copy.
func (p ProjectReport) Failed() bool {
	for _, r := range p.Results {
		if r.Failed() {
			return true
		}
	}
	return false
}

// seedEntries are the hub-written identity entries of a hub-managed
// project's .scion: what initHubManagedProject seeds (settings.yaml with
// the hub connection) and the project-id marker.
var seedEntries = []string{"settings.yaml", projectkeys.ProjectIDFile}

// infraEntries are the entries a legacy workspace may hold that are not
// workspace content (the retired probe's rule): a source with nothing else
// has nothing to move into workspace/.
var infraEntries = map[string]bool{"shared-dirs": true, ".scion": true}

// MigrateProject runs (or, with opts.DryRun, plans) every M2 step for one
// project, in ProjectSteps order.
func MigrateProject(ctx context.Context, in ProjectInput, opts Options) ProjectReport {
	rep := ProjectReport{ID: in.ID, Slug: in.Slug, OwnershipRequested: in.WorkspaceOwner != nil || in.SharedDirOwner != nil}
	if err := validateInput(in); err != nil {
		rep.Results = append(rep.Results, Result{Step: StepSeed, Outcome: OutcomeFailed, Err: err})
		return rep
	}
	var sources []Tree
	if in.HubProjects.Base != "" {
		sources = append(sources, Tree{Base: in.HubProjects.Base, Rel: filepath.Join(in.HubProjects.Rel, in.Slug)})
	}
	local := Tree{Base: in.GlobalDir, Rel: filepath.Join("projects", in.Slug)}

	wsOpts, sdOpts := opts, opts
	wsOpts.Owner, sdOpts.Owner = in.WorkspaceOwner, in.SharedDirOwner
	// Step 4 first: otherwise the workspace rename carries the hub seed
	// into workspace/, where it would pose as a repository tier.
	if in.ProjectRoot.Base != "" {
		for _, src := range append(append([]Tree{}, sources...), local) {
			rep.seed(ctx, in, src, wsOpts)
		}
	}
	for _, src := range sources {
		rep.workspace(StepHubProjects, src, in, wsOpts)
	}
	rep.workspace(StepLocalProjects, local, in, wsOpts)
	if in.SharedDir != nil {
		rep.sharedDirs(in, sdOpts)
	}
	if in.CloudRunHomes != nil {
		rep.Results = append(rep.Results, in.CloudRunHomes(opts)...)
	}
	return rep
}

func validateInput(in ProjectInput) error {
	if in.ID == "" || in.Slug == "" {
		return errors.New("project ID and slug are required")
	}
	if strings.ContainsAny(in.Slug, "/\\:") || strings.Contains(in.Slug, "..") || in.Slug == "." || api.Slugify(in.Slug) != in.Slug {
		return fmt.Errorf("invalid project slug %q", in.Slug)
	}
	if !filepath.IsAbs(in.GlobalDir) {
		return fmt.Errorf("global dir %q is not absolute", in.GlobalDir)
	}
	if in.Workspace != nil {
		if err := in.Workspace.validate(); err != nil {
			return fmt.Errorf("workspace target: %w", err)
		}
	}
	if in.ProjectRoot.Base != "" {
		if err := in.ProjectRoot.validate(); err != nil {
			return fmt.Errorf("project root: %w", err)
		}
	}
	if in.HubProjects.Base != "" {
		if err := in.HubProjects.validate(); err != nil {
			return fmt.Errorf("legacy hub projects root: %w", err)
		}
	}
	return nil
}

// seed is step 4 for one legacy workspace source.
func (rep *ProjectReport) seed(ctx context.Context, in ProjectInput, src Tree, opts Options) {
	scion := Tree{Base: src.Base, Rel: filepath.Join(src.Rel, ".scion")}
	target := Tree{Base: in.ProjectRoot.Base, Rel: filepath.Join(in.ProjectRoot.Rel, ".scion")}
	spec := Spec{Step: StepSeed, Source: scion, Target: target, Normalize: NormalizeWorkspace}

	gitKind, err := entryKind(Tree{Base: src.Base, Rel: filepath.Join(src.Rel, ".git")})
	if err != nil {
		rep.Results = append(rep.Results, Result{Step: StepSeed, Source: scion.String(), Target: target.String(), Outcome: OutcomeFailed, Err: fmt.Errorf("inspect .git: %w", err)})
		return
	}
	if gitKind == kindAbsent {
		rep.Results = append(rep.Results, Move(spec, opts))
		return
	}
	scionKind, err := entryKind(scion)
	if err != nil || scionKind != kindDir {
		if err != nil {
			rep.Results = append(rep.Results, Result{Step: StepSeed, Source: scion.String(), Target: target.String(), Outcome: OutcomeFailed, Err: err})
		}
		return // no .scion directory: nothing to move
	}
	if gitKind != kindDir {
		// A .git file or symlink points elsewhere; tracked state cannot be
		// read safely here, so nothing is moved.
		rep.Notes = append(rep.Notes, fmt.Sprintf("%s: .git is not a directory; the seed is left in place", scion))
		return
	}
	tracked := in.Tracked
	if tracked == nil {
		tracked = GitTracked
	}
	names, _, err := listDir(scion)
	if err != nil {
		rep.Results = append(rep.Results, Result{Step: StepSeed, Source: scion.String(), Target: target.String(), Outcome: OutcomeFailed, Err: err})
		return
	}
	dir := src.String()
	var move []string
	for _, name := range names {
		isSeed := false
		for _, s := range seedEntries {
			isSeed = isSeed || s == name
		}
		t, err := tracked(ctx, dir, filepath.Join(".scion", name))
		if err != nil {
			rep.Results = append(rep.Results, Result{Step: StepSeed, Source: scion.String(), Target: target.String(), Outcome: OutcomeFailed, Err: fmt.Errorf("git tracked check of %s: %w", name, err)})
			return
		}
		switch {
		case t:
			// Repository tier (design-v4 P1a): stays in the workspace.
		case isSeed:
			move = append(move, name)
		default:
			rep.Notes = append(rep.Notes, fmt.Sprintf("%s/%s: untracked and not a hub seed entry; left in place", scion, name))
		}
	}
	if len(move) == 0 {
		return
	}
	spec.Entries = move
	rep.Results = append(rep.Results, Move(spec, opts))
}

// workspace is steps 1 and 2 for one legacy workspace source.
func (rep *ProjectReport) workspace(step Step, src Tree, in ProjectInput, opts Options) {
	target := in.Workspace
	names, ok, err := listDir(src)
	if err != nil {
		rep.Results = append(rep.Results, Result{Step: step, Source: src.String(), Outcome: OutcomeFailed, Err: err})
		return
	}
	content := false
	for _, n := range names {
		content = content || !infraEntries[n]
	}
	if !ok || !content {
		return
	}
	if target == nil {
		rep.Notes = append(rep.Notes, fmt.Sprintf("%s: the project has no project-scoped workspace; the legacy tree is left in place", src))
		return
	}
	rep.Results = append(rep.Results, Move(Spec{
		Step:          step,
		Source:        src,
		Target:        *target,
		PrepareTarget: in.PrepareWorkspace,
		Lock:          in.LockWorkspace,
		TargetIsLeaf:  true,
		Normalize:     NormalizeWorkspace,
	}, opts))
}

// sharedDirs is step 3: hub-local project-configs/<slug>__<short>/shared-dirs/<name>.
func (rep *ProjectReport) sharedDirs(in ProjectInput, opts Options) {
	root := Tree{Base: in.GlobalDir, Rel: filepath.Join(projectkeys.ProjectConfigsDir, projectfs.LocalConfigDirName(in.Slug, in.ID), "shared-dirs")}
	names, _, err := listDir(root)
	if err != nil {
		rep.Results = append(rep.Results, Result{Step: StepSharedDirs, Source: root.String(), Outcome: OutcomeFailed, Err: err})
		return
	}
	for _, name := range names {
		src := Tree{Base: root.Base, Rel: filepath.Join(root.Rel, name)}
		if strings.Contains(name, MigratedSuffix) {
			continue // an earlier run's copy
		}
		if err := api.ValidateSharedDirs([]api.SharedDir{{Name: name}}); err != nil {
			rep.Results = append(rep.Results, Result{Step: StepSharedDirs, Source: src.String(), Outcome: OutcomeFailed, Err: fmt.Errorf("not a valid shared dir name: %w", err)})
			continue
		}
		t, err := in.SharedDir(name)
		if err != nil {
			rep.Results = append(rep.Results, Result{Step: StepSharedDirs, Source: src.String(), Outcome: OutcomeFailed, Err: fmt.Errorf("resolve target: %w", err)})
			continue
		}
		rep.Results = append(rep.Results, Move(Spec{Step: StepSharedDirs, Source: src, Target: t.Tree, NoRename: true, PrepareTarget: t.Prepare, Normalize: NormalizeSharedDir}, opts))
	}
}

type kind int

const (
	kindAbsent kind = iota
	kindDir
	kindOther
)

// entryKind reports what t is, without following a symlink anywhere in it.
func entryKind(t Tree) (kind, error) {
	if err := t.validate(); err != nil {
		return kindAbsent, err
	}
	parent, name, ok, err := openParent(t, false)
	if err != nil {
		return kindAbsent, err
	}
	if !ok {
		return kindAbsent, nil
	}
	defer closeFd(parent)
	var st unix.Stat_t
	if err := unix.Fstatat(parent, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return kindAbsent, nil
		}
		return kindAbsent, err
	}
	if st.Mode&unix.S_IFMT == unix.S_IFDIR {
		return kindDir, nil
	}
	return kindOther, nil
}

// listDir lists directory t without following a symlink anywhere in it. ok
// is false when t does not exist. A t that exists but is not a directory
// (a symlink included) is an error.
func listDir(t Tree) (names []string, ok bool, err error) {
	if err := t.validate(); err != nil {
		return nil, false, err
	}
	parent, name, ok, err := openParent(t, false)
	if err != nil || !ok {
		return nil, false, err
	}
	defer closeFd(parent)
	fd, err := openDirAt(parent, name)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("open %s: %w (a symlink is never followed)", t, err)
	}
	defer closeFd(fd)
	names, err = readDirNames(fd)
	if err != nil {
		return nil, false, err
	}
	return names, true, nil
}

// gitTimeout bounds one git tracked check.
const gitTimeout = 30 * time.Second

// GitTracked reports whether path, relative to the checkout at dir (whose
// .git must be a directory), is tracked: git ls-files --error-unmatch. The
// repository is untrusted content, so git runs with an explicit GIT_DIR and
// work tree, no system or global config, no fsmonitor or hooks, no
// optional locks, and a minimal environment.
func GitTracked(ctx context.Context, dir, path string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git",
		"-c", "core.fsmonitor=false", "-c", "core.hooksPath=/dev/null",
		"--no-optional-locks", "ls-files", "--error-unmatch", "--", path)
	cmd.Dir = dir
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"GIT_DIR=" + filepath.Join(dir, ".git"),
		"GIT_WORK_TREE=" + dir,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_OPTIONAL_LOCKS=0",
		"HOME=/nonexistent",
	}
	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, err
}
