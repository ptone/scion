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

package projectfs

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/provision"
	"github.com/GoogleCloudPlatform/scion/pkg/shareddirs"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
)

// Provisioner creates the Scion-owned directories of a Layout (ptone/scion#5834):
// the hub and the brokers create leaves through it, so a leaf has the same
// mode and ACL whoever creates it. It wraps pkg/shareddirs (the confined
// O_NOFOLLOW leaf walk: mode 2775 and the default ACL on a shared backend)
// and pkg/provision (the provisioning lock, the sentinel and the clone).
type Provisioner struct {
	Layout Layout
	// ProvisionShared runs the workspace provisioning; nil means
	// provision.ProvisionShared. Tests replace it.
	ProvisionShared func(provision.ProvisionInput) error
}

// ErrProvisionStorage marks a failed storage step of a provisioning (a leaf
// or the provisioning state directory could not be created or checked), as
// opposed to a failed clone. Its wrapped error may carry a host path.
var ErrProvisionStorage = errors.New("projectfs: project storage step failed")

// EnsureSharedLeaf creates (or verifies) rel below the resolved host base of
// a shared backend with the leaf contract (shareddirs.EnsureLeaf: every
// component opened without following symlinks, a new leaf mode 2775 with the
// default ACL) and returns the leaf's open descriptor, which the caller
// closes with shareddirs.CloseFd. It is the one primitive every Scion-owned
// directory on a shared backend is created through.
func EnsureSharedLeaf(hostBase, rel string) (fd int, existed bool, err error) {
	return shareddirs.EnsureLeaf(hostBase, rel)
}

// EnsureLeaf creates the directory r if it is missing and reports whether it
// already existed. On a shared backend it is EnsureSharedLeaf below r's
// resolved host base. On the local backend it is the directory created
// as before (mode 0755, missing parents included), unchanged.
func (p Provisioner) EnsureLeaf(r Resolved) (existed bool, err error) {
	switch {
	case r.Backend.Shared():
		base, err := r.resolvedHostBase()
		if err != nil {
			return false, err
		}
		fd, existed, err := EnsureSharedLeaf(base, r.RelPath)
		if err != nil {
			return false, fmt.Errorf("create %s %q: %w", r.Kind, r.Name, err)
		}
		if err := shareddirs.CloseFd(fd); err != nil {
			return false, err
		}
		return existed, nil
	case r.Backend == BackendLocal:
		if r.HostPath == "" || !filepath.IsAbs(r.HostPath) {
			return false, fmt.Errorf("projectfs: local directory %q is not absolute", r.HostPath)
		}
		if fi, err := os.Stat(r.HostPath); err == nil {
			if !fi.IsDir() {
				return false, fmt.Errorf("projectfs: %s exists and is not a directory", r.HostPath)
			}
			return true, nil
		} else if !errors.Is(err, fs.ErrNotExist) {
			return false, err
		}
		if err := os.MkdirAll(r.HostPath, 0o755); err != nil {
			return false, err
		}
		return false, nil
	default:
		return false, fmt.Errorf("projectfs: cannot create a directory on backend %q", r.Backend)
	}
}

// provisionDirFor resolves a project's provisioning state directory,
// <ProjectRoot>/provision.
func (l Layout) provisionDirFor(projectID string) (Resolved, error) {
	pr, err := l.ProjectRoot(projectID)
	if err != nil {
		return Resolved{}, err
	}
	return l.shared(filepath.Join(pr.RelPath, provisionDir), Mount{}), nil
}

// OpenStateDir opens a project's provisioning state directory on a shared
// backend, <ProjectRoot>/provision: created with the leaf contract when
// missing, checked to be a real directory (provision.PrepareStateDir, its
// ownership left as it is), and swept of aged lock litter
// (provision.GarbageCollectStateDir, ptone/scion#5836: every open sweeps,
// never a live lock). It returns the directory's path below the resolved
// host base.
func (p Provisioner) OpenStateDir(projectID string) (string, error) {
	r, err := p.Layout.provisionDirFor(projectID)
	if err != nil {
		return "", err
	}
	base, err := r.resolvedHostBase()
	if err != nil {
		return "", err
	}
	fd, _, err := EnsureSharedLeaf(base, r.RelPath)
	if err != nil {
		return "", fmt.Errorf("create the provisioning state directory: %w", err)
	}
	if err := shareddirs.CloseFd(fd); err != nil {
		return "", err
	}
	dir := filepath.Join(base, r.RelPath)
	if err := provision.PrepareStateDir(dir, provision.DefaultOwnerID(p.Layout.UID), provision.DefaultOwnerID(p.Layout.GID), false); err != nil {
		return "", err
	}
	provision.GarbageCollectStateDir(dir)
	return dir, nil
}

// SharedWorkspaceProvision is the input of ProvisionSharedWorkspace.
type SharedWorkspaceProvision struct {
	Ctx       context.Context
	ProjectID string
	// AgentID is recorded for logs; empty for the hub's provisioning at
	// project create.
	AgentID  string
	GitClone *api.GitCloneConfig
	// CloneToken and CloneTokenRemote are provision.ProvisionInput's: the
	// token reaches only the clone command, and only for that remote.
	CloneToken       string
	CloneTokenRemote string
}

// ProvisionSharedWorkspace provisions a project's shared-plain workspace on
// a shared Layout the way the Kubernetes provisioning init container lays
// it out, for the hub (at project create) and the brokers (the backstop
// before a local container starts) alike: the workspace leaf
// (Resolve(workspace)) and the provisioning state directory (OpenStateDir)
// are created with the leaf contract, then provision.ProvisionShared runs
// with the sentinel and the lock in the state directory only, ownership
// from the Layout, a failed chown not fatal, and the clone hardened for a
// project-supplied URL. An existing sentinel means no clone.
//
// createdRoot reports whether the project root did not exist before the
// call, so a caller can remove a tree it created when provisioning fails.
// A failed storage step wraps ErrProvisionStorage; a clone failure is
// returned as provision.ProvisionShared returns it.
func (p Provisioner) ProvisionSharedWorkspace(in SharedWorkspaceProvision) (createdRoot bool, err error) {
	l := p.Layout
	if !l.Backend.Shared() {
		return false, fmt.Errorf("projectfs: shared workspace provisioning needs a shared backend, not %q", l.Backend)
	}
	r, err := l.Resolve(Project{ID: in.ProjectID}, "", WorkspaceMount(ModeSharedPlain, ""))
	if err != nil {
		return false, err
	}
	pr, err := l.ProjectRoot(in.ProjectID)
	if err != nil {
		return false, err
	}
	base, err := r.resolvedHostBase()
	if err != nil {
		return false, fmt.Errorf("%w: host base: %w", ErrProvisionStorage, err)
	}
	_, statErr := os.Lstat(filepath.Join(base, pr.RelPath))
	createdRoot = errors.Is(statErr, fs.ErrNotExist)
	if _, err := p.EnsureLeaf(r); err != nil {
		return createdRoot, fmt.Errorf("%w: workspace leaf: %w", ErrProvisionStorage, err)
	}
	stateDir, err := p.OpenStateDir(in.ProjectID)
	if err != nil {
		return createdRoot, fmt.Errorf("%w: provisioning state directory: %w", ErrProvisionStorage, err)
	}
	run := p.ProvisionShared
	if run == nil {
		run = provision.ProvisionShared
	}
	err = run(provision.ProvisionInput{
		Ctx: in.Ctx,
		Resolved: provision.ResolvedWorkspace{
			HostPath:           filepath.Join(base, r.RelPath),
			ServerRelativePath: r.RelPath,
			HostBase:           base,
			Backend:            string(l.Backend),
		},
		ProjectID:        in.ProjectID,
		AgentID:          in.AgentID,
		Mode:             store.SharingModeSharedPlain,
		GitClone:         in.GitClone,
		CloneToken:       in.CloneToken,
		CloneTokenRemote: in.CloneTokenRemote,
		HostGitHardened:  true,
		NFSUID:           provision.DefaultOwnerID(l.UID),
		NFSGID:           provision.DefaultOwnerID(l.GID),
		SentinelDir:      stateDir,
	})
	return createdRoot, err
}

// ProjectTreeIDs lists the project IDs that have a project root on l (one
// listing of the subpath root, never following a symlink; entries that are
// not project IDs are skipped). A subpath root that does not exist is an
// error (the share may not be mounted). Only l itself is listed, not its
// transitional shared-dir split: workspaces and provisioning state live on l.
func (l Layout) ProjectTreeIDs() ([]string, error) {
	if !l.Backend.Shared() {
		return nil, fmt.Errorf("projectfs: project trees are defined for shared backends, not %q", l.Backend)
	}
	root, err := l.subPathRoot()
	if err != nil {
		return nil, err
	}
	if err := l.checkShareHostBase(); err != nil {
		return nil, err
	}
	r := Resolved{Backend: l.Backend, HostBase: l.HostBase, RelPath: root}
	base, err := r.resolvedHostBase()
	if err != nil {
		return nil, fmt.Errorf("%w: host base %s: %w", ErrRefused, l.HostBase, err)
	}
	names, ok, err := shareddirs.ListDirInTree(base, root)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("%w: %w: %s has no %s directory; the share may not be mounted", ErrRefused, ErrNoProjectTree, l.HostBase, root)
	}
	var ids []string
	for _, name := range names {
		if validProjectID(name) == nil {
			ids = append(ids, name)
		}
	}
	return ids, nil
}
