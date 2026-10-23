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
// workspace backend (design-v4 G7: the invariant that this tree no longer
// exists). This constant is the only place the name survives (ptone/scion#5819,
// A2 exception agreed with fs-cleanup-arch); the migration that reads it is
// deleted in phase 6 (ptone/scion#5839).
const legacyHubProjectsDir = "hub-projects"

// Step names one M2 step (design-v4 §Migration M2). Steps run in the order
// of ProjectSteps, which puts the seed (4) before the workspace (1).
type Step string

const (
	StepSeed          Step = "4-scion-seed"
	StepHubProjects   Step = "1-legacy-hub-workspace"
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
// <mount_root>/<share id>/<legacyHubProjectsDir> (no subpath_root); the volume
// backends (cloudrun-volume, gke-shared-volume) wrote
// /mnt/<volume>/<subpath_root>/<legacyHubProjectsDir>. The Layout's HostBase is that
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
	// LocalCopyOnly: a co-located broker serves the project from the
	// hub-local ~/.scion/projects/<slug> (the per-project provider check).
	// Steps 2 and 4 then copy from it and change nothing in it, leaving
	// .scion/agents and shared-dirs out (fs-cleanup-arch Q7). The same
	// applies, without a provider, when the tree itself carries broker
	// state (localTreeUsedByBroker).
	LocalCopyOnly bool
	// Git: the project is git-backed; a workspace migrated without a .git
	// is reported, because the first agent start will refuse it.
	Git bool
	// SkipWorkspace: the workspace Layout is not shared, so steps 1, 2 and
	// 4 do not apply (only step 3 runs); reported per project.
	SkipWorkspace bool
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
	// WorkspaceIncomplete: the project's workspace content was not (fully)
	// migrated on this run: the workspace steps were skipped, legacy
	// content was left in place, or a seed step stopped a source. M3 then
	// keeps the project's legacy object-storage copy.
	WorkspaceIncomplete bool
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

// localTreeUsedByBroker reports whether the hub-local project tree carries
// state a broker created (fs-cleanup-arch Q7: conservative; a needless
// copy-only leaves a tree behind, a wrong rename breaks a broker). The hub
// itself only ever wrote .scion/settings.yaml there, so a .scion that is a
// marker file, or holds anything else (agents/, project-id, templates,
// markers), counts as broker state.
func localTreeUsedByBroker(local Tree) (bool, error) {
	scion := Tree{Base: local.Base, Rel: filepath.Join(local.Rel, ".scion")}
	k, err := entryKind(scion)
	if err != nil {
		return false, err
	}
	switch k {
	case kindAbsent:
		return false, nil
	case kindOther:
		return true, nil
	}
	names, _, err := listDir(scion)
	if err != nil {
		return false, err
	}
	for _, n := range names {
		if n != "settings.yaml" {
			return true, nil
		}
	}
	return false, nil
}

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
	if in.SkipWorkspace {
		rep.WorkspaceIncomplete = true
		rep.Notes = append(rep.Notes, "the workspace storage is not shared; workspace and seed steps (1, 2, 4) skipped")
	} else {
		localCopyOnly := in.LocalCopyOnly
		if !localCopyOnly {
			used, err := localTreeUsedByBroker(local)
			if err != nil {
				rep.Results = append(rep.Results, Result{Step: StepLocalProjects, Source: local.String(), Outcome: OutcomeFailed, Err: fmt.Errorf("inspect local tree: %w", err)})
				return rep
			}
			localCopyOnly = used
		}
		if k, _ := entryKind(local); localCopyOnly && k == kindDir {
			rep.Notes = append(rep.Notes, fmt.Sprintf("%s: a broker uses this tree; its content and seed are copied, the tree is left in place", local))
		}
		// A source whose seed step failed or could not decide is not moved
		// into workspace/: the hub seed must never become agent-visible
		// workspace content (design P1a).
		seedOK := func(src Tree, copyOnly bool) bool {
			if in.ProjectRoot.Base == "" {
				return true
			}
			return rep.seed(ctx, in, src, copyOnly, wsOpts)
		}
		for _, src := range sources {
			if rep.bound(src, false, in, opts) && seedOK(src, false) {
				rep.workspace(StepHubProjects, src, in, false, wsOpts)
			} else {
				rep.WorkspaceIncomplete = true
			}
		}
		if rep.bound(local, true, in, opts) && seedOK(local, localCopyOnly) {
			rep.workspace(StepLocalProjects, local, in, localCopyOnly, wsOpts)
		} else {
			rep.WorkspaceIncomplete = true
		}
		rep.noteGitlessWorkspace(in, opts)
	}
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
func (rep *ProjectReport) seed(ctx context.Context, in ProjectInput, src Tree, copyOnly bool, opts Options) (ok bool) {
	scion := Tree{Base: src.Base, Rel: filepath.Join(src.Rel, ".scion")}
	target := Tree{Base: in.ProjectRoot.Base, Rel: filepath.Join(in.ProjectRoot.Rel, ".scion")}
	spec := Spec{Step: StepSeed, Source: scion, Target: target, Normalize: NormalizeWorkspace, CopyOnly: copyOnly}
	failed := func(err error) bool {
		rep.Results = append(rep.Results, Result{Step: StepSeed, Source: scion.String(), Target: target.String(), Outcome: OutcomeFailed, Err: err})
		return false
	}
	move := func() bool {
		r := Move(spec, opts)
		rep.Results = append(rep.Results, r)
		return !r.Failed()
	}

	scionKind, err := entryKind(scion)
	if err != nil {
		return failed(fmt.Errorf("inspect .scion: %w", err))
	}
	if scionKind == kindAbsent {
		return true // no seed: nothing to move
	}
	gitKind, err := entryKind(Tree{Base: src.Base, Rel: filepath.Join(src.Rel, ".git")})
	if err != nil {
		return failed(fmt.Errorf("inspect .git: %w", err))
	}
	if scionKind != kindDir {
		// A .scion marker file: the project's configuration lives elsewhere
		// (a broker's external config); there is no hub seed to move here.
		if copyOnly {
			return true
		}
		return failed(errors.New(".scion is not a directory; cannot tell the hub seed apart, so the tree is not moved"))
	}
	names, _, err := listDir(scion)
	if err != nil {
		return failed(err)
	}
	isSeed := func(name string) bool {
		for _, s := range seedEntries {
			if s == name {
				return true
			}
		}
		return false
	}

	if gitKind == kindAbsent {
		if !copyOnly {
			return move() // the whole .scion is the hub's seed
		}
		// A broker's live tree: copy only the identity files, never its
		// private records (agents/, markers).
		for _, name := range names {
			if isSeed(name) {
				spec.Entries = append(spec.Entries, name)
			}
		}
		if len(spec.Entries) == 0 {
			return true
		}
		return move()
	}
	if gitKind != kindDir {
		// A .git file or symlink points elsewhere: tracked state cannot be
		// read safely, so the seed cannot be told apart from repository
		// content. Nothing moves from this source (steps 1 and 2 skip it).
		return failed(errors.New(".git is not a directory; cannot tell the hub seed from repository .scion content, so this tree is not moved"))
	}
	tracked := in.Tracked
	if tracked == nil {
		tracked = GitTracked
	}
	dir := src.String()
	for _, name := range names {
		t, err := tracked(ctx, dir, filepath.Join(".scion", name))
		if err != nil {
			return failed(fmt.Errorf("git tracked check of %s (this tree is not moved): %w", name, err))
		}
		switch {
		case t:
			// Repository tier (design P1a): stays in the workspace.
		case isSeed(name):
			spec.Entries = append(spec.Entries, name)
		default:
			rep.Notes = append(rep.Notes, fmt.Sprintf("%s/%s: untracked and not a hub seed entry; left with the workspace", scion, name))
		}
	}
	if len(spec.Entries) == 0 {
		return true
	}
	return move()
}

// noteGitlessWorkspace reports a git project whose workspace content was
// migrated without a .git (fs-cleanup-arch residual 1): the provisioner
// refuses a non-empty workspace with no provisioning marker and no .git, so
// the first agent start will fail until the operator acts.
func (rep *ProjectReport) noteGitlessWorkspace(in ProjectInput, opts Options) {
	if !in.Git || in.Workspace == nil {
		return
	}
	moved := false
	for _, r := range rep.Results {
		if (r.Step == StepHubProjects || r.Step == StepLocalProjects) && r.Moved() {
			moved = true
		}
	}
	if !moved {
		return
	}
	k, err := entryKind(Tree{Base: in.Workspace.Base, Rel: filepath.Join(in.Workspace.Rel, ".git")})
	if err != nil || k != kindAbsent {
		return
	}
	if opts.DryRun {
		// The target may not exist yet; say what the source lacks.
		rep.Notes = append(rep.Notes, "git project: the migrated workspace would have no .git; its first agent start would refuse until the operator moves the non-git entries out or re-clones")
		return
	}
	rep.Notes = append(rep.Notes, "git project: migrated, but the workspace has no .git; the first agent start will refuse (non-empty workspace without a provisioning marker or .git) until the operator moves the non-git entries out or re-clones")
}

// StepIdentity reports a legacy tree that is not bound to the project.
const StepIdentity Step = "identity"

// bound reports whether the slug-keyed legacy tree src may be migrated into
// this project (fs-cleanup-arch Q8): an absent tree trivially; otherwise
// only when its identity markers name the project's ID, or, with
// opts.AdoptUnverified, when it carries none. A mismatched or unverified
// tree is reported as a failed step and left for an operator.
func (rep *ProjectReport) bound(src Tree, local bool, in ProjectInput, opts Options) bool {
	k, err := entryKind(src)
	if err != nil {
		rep.Results = append(rep.Results, Result{Step: StepIdentity, Source: src.String(), Outcome: OutcomeFailed, Err: err})
		return false
	}
	if k == kindAbsent {
		return true
	}
	if k == kindDir {
		// Nothing to migrate (no content, no seed): nothing to bind.
		names, _, err := listDir(src)
		if err != nil {
			rep.Results = append(rep.Results, Result{Step: StepIdentity, Source: src.String(), Outcome: OutcomeFailed, Err: err})
			return false
		}
		relevant := false
		for _, n := range names {
			relevant = relevant || n != "shared-dirs"
		}
		if !relevant {
			return true
		}
	}
	id, detail, err := treeIdentity(src, local, in.GlobalDir, in.Slug, in.ID)
	if err != nil {
		rep.Results = append(rep.Results, Result{Step: StepIdentity, Source: src.String(), Outcome: OutcomeFailed, Err: err})
		return false
	}
	switch id {
	case identityVerified:
		return true
	case identityUnverified:
		if opts.AdoptUnverified != nil && opts.AdoptUnverified(in.Slug) {
			rep.Notes = append(rep.Notes, fmt.Sprintf("%s: %s; adopted (adoptUnverified)", src, detail))
			return true
		}
		rep.Results = append(rep.Results, Result{Step: StepIdentity, Source: src.String(), Outcome: OutcomeFailed,
			Err: fmt.Errorf("unverified (%s): the tree may belong to a deleted project with the same slug; left in place. To migrate it, run with {\"params\":{\"adoptUnverified\":\"%s\"}}", detail, in.Slug)})
	default:
		rep.Results = append(rep.Results, Result{Step: StepIdentity, Source: src.String(), Outcome: OutcomeFailed,
			Err: fmt.Errorf("identity mismatch (%s): the tree belongs to another project with the same slug; left in place (never adopted)", detail)})
	}
	return false
}

// workspace is steps 1 and 2 for one legacy workspace source.
func (rep *ProjectReport) workspace(step Step, src Tree, in ProjectInput, copyOnly bool, opts Options) {
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
		rep.WorkspaceIncomplete = true
		rep.Notes = append(rep.Notes, fmt.Sprintf("%s: the project has no project-scoped workspace; the legacy tree is left in place", src))
		return
	}
	spec := Spec{
		Step:          step,
		Source:        src,
		Target:        *target,
		PrepareTarget: in.PrepareWorkspace,
		TargetIsLeaf:  true,
		Normalize:     NormalizeWorkspace,
	}
	hasSharedDirs := false
	for _, n := range names {
		hasSharedDirs = hasSharedDirs || n == "shared-dirs"
	}
	switch {
	case copyOnly:
		spec.CopyOnly = true
		spec.Exclude = []string{".scion", "shared-dirs"}
	case hasSharedDirs:
		// Shared dirs never land in workspace/ (step 3 owns them): copy
		// the rest and keep shared-dirs/ in the source's .migrated copy.
		spec.NoRename = true
		spec.Exclude = []string{"shared-dirs"}
		rep.Notes = append(rep.Notes, fmt.Sprintf("%s/shared-dirs: not moved into the workspace; it stays in the source's .migrated copy", src))
	}
	rep.Results = append(rep.Results, Move(spec, opts))
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
// optional locks, and a minimal environment; safe.directory names dir so a
// checkout owned by another uid is still read. Any failure (git missing,
// timeout) is an error, and the caller then moves nothing from that tree.
func GitTracked(ctx context.Context, dir, path string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git",
		"-c", "core.fsmonitor=false", "-c", "core.hooksPath=/dev/null",
		// The checkout may be owned by another uid (agents, a squashed
		// export); hooks and fsmonitor are off, so trusting it is safe.
		"-c", "safe.directory="+dir,
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
