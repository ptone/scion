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

package config

import (
	"errors"
	"fmt"
	"io"
	iofs "io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// TemplateRefContext says who a template reference comes from, which decides
// what forms it may take.
//
// The zero value is TemplateRefRemote, so a caller that does not choose gets
// the confined behaviour.
type TemplateRefContext int

const (
	// TemplateRefRemote is a reference that arrived through a remote request
	// (hub agent create or dispatch, runtime broker create, or a name read
	// back from an agent's own records). It is either a remote URI, fetched
	// through FetchRemoteTemplate in anonymous mode, or a local relative name
	// (filepath.IsLocal) resolved inside the project or global template
	// directory. Absolute paths are refused.
	TemplateRefRemote TemplateRefContext = iota
	// templateRefLocal is a reference the CLI resolves for its own use on
	// its own host. An absolute directory is accepted as well.
	templateRefLocal
)

// ErrTemplateRefNotAllowed marks a template reference that is refused
// because of its form: an absolute path from a remote caller, or a relative
// path that leaves the template directory. It is distinct from
// ErrTemplateNotFound so that callers can report it as a client error that
// names the rule.
var ErrTemplateRefNotAllowed = errors.New("template reference not allowed")

// ErrTemplateContentRefused marks a file in a template directory that exists
// but is not read: a symbolic link, a non-regular file, or a path that leaves
// the template directory.
var ErrTemplateContentRefused = errors.New("template file refused")

// templateRefNotAllowedError returns the error for a refused reference. The
// message names the rule and the reference's display name only, never a host
// path.
func templateRefNotAllowedError(ref string) error {
	return fmt.Errorf("%w: %q: a template must be a template name, a relative path inside the template directory, or an accepted remote source", ErrTemplateRefNotAllowed, FriendlyTemplateName(ref))
}

// isLocalTemplateName reports whether name is a relative path that stays
// inside the directory it is looked up in and names something below it:
// filepath.IsLocal, and not "." (or a path that cleans to it), which would
// be the directory itself.
func isLocalTemplateName(name string) bool {
	return filepath.IsLocal(name) && filepath.Clean(name) != "."
}

// ValidateRemoteTemplateRef checks the form of a template reference that
// arrives through a remote request, before anything is resolved or stored.
// An empty reference is accepted (no template). A remote URI must pass
// ValidateRemoteFetchSource; the fetch runs the full check again, including
// the dial-time address check. Anything else must be a local relative path
// (filepath.IsLocal), so absolute paths and paths that leave the template
// directory are refused.
func ValidateRemoteTemplateRef(ref string) error {
	if ref == "" {
		return nil
	}
	if IsRemoteURI(ref) {
		if err := ValidateRemoteFetchSource(ref); err != nil {
			return fmt.Errorf("%w: %w", ErrTemplateRefNotAllowed, err)
		}
		return nil
	}
	if !isLocalTemplateName(ref) {
		return templateRefNotAllowedError(ref)
	}
	return nil
}

// findDirInRoot reports the directory name inside root, where name is a
// local relative path. Every component of name must be a real directory
// (not a symbolic link), and the lookup goes through an os.Root, so it can
// never leave root. A missing root or component, a symbolic link, or a
// non-directory reports false.
func findDirInRoot(root, name string) (string, bool) {
	if root == "" || !isLocalTemplateName(name) {
		return "", false
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return "", false
	}
	defer func() { _ = r.Close() }()
	cur := ""
	for _, part := range strings.Split(filepath.Clean(name), string(filepath.Separator)) {
		cur = filepath.Join(cur, part)
		info, err := r.Lstat(cur)
		if err != nil || info.Mode()&iofs.ModeSymlink != 0 || !info.IsDir() {
			return "", false
		}
	}
	return filepath.Join(root, name), true
}

// absentLookupError reports whether a lookup error means the file is simply
// not there: it does not exist, a path component is not a directory, or the
// name cannot be a file name on this system (too long, or invalid). Such a
// field is inline content, as it was before files were read through os.Root.
func absentLookupError(err error) bool {
	return errors.Is(err, iofs.ErrNotExist) ||
		errors.Is(err, syscall.ENOTDIR) ||
		errors.Is(err, syscall.ENAMETOOLONG) ||
		errors.Is(err, syscall.EINVAL)
}

// templateFileState is the result of looking up one file in a template
// directory.
type templateFileState int

const (
	// templateFileAbsent: the file (or a directory leading to it) does not
	// exist.
	templateFileAbsent templateFileState = iota
	// templateFileRegular: the file is a regular file and was read.
	templateFileRegular
	// templateFileRefused: an entry exists but is not read (a symbolic link
	// anywhere on the path, a non-regular file, or a path that leaves dir).
	templateFileRefused
)

// readTemplateFile reads the file name (a relative path) inside the template
// directory dir without following symbolic links. The lookup goes through an
// os.Root opened at dir; every directory component is checked with Lstat,
// the file must be a regular file when inspected, and the opened file must
// be that same file (os.SameFile). A refused file returns
// templateFileRefused with an error wrapping ErrTemplateContentRefused.
func readTemplateFile(dir, name string) ([]byte, templateFileState, error) {
	if !isLocalTemplateName(name) {
		return nil, templateFileRefused, fmt.Errorf("%w: %q leaves the template directory", ErrTemplateContentRefused, filepath.ToSlash(name))
	}
	r, err := os.OpenRoot(dir)
	if err != nil {
		if absentLookupError(err) {
			return nil, templateFileAbsent, nil
		}
		return nil, templateFileAbsent, err
	}
	defer func() { _ = r.Close() }()

	clean := filepath.Clean(name)
	parts := strings.Split(clean, string(filepath.Separator))
	cur := ""
	for i, part := range parts {
		cur = filepath.Join(cur, part)
		info, err := r.Lstat(cur)
		if absentLookupError(err) {
			return nil, templateFileAbsent, nil
		}
		if err != nil {
			return nil, templateFileRefused, fmt.Errorf("%w: %q: %v", ErrTemplateContentRefused, filepath.ToSlash(clean), err)
		}
		if info.Mode()&iofs.ModeSymlink != 0 {
			return nil, templateFileRefused, fmt.Errorf("%w: %q is a symbolic link; replace it with the file itself", ErrTemplateContentRefused, filepath.ToSlash(cur))
		}
		last := i == len(parts)-1
		if !last && !info.IsDir() {
			return nil, templateFileAbsent, nil
		}
		if last && !info.Mode().IsRegular() {
			return nil, templateFileRefused, fmt.Errorf("%w: %q is not a regular file", ErrTemplateContentRefused, filepath.ToSlash(clean))
		}
	}

	linfo, err := r.Lstat(clean)
	if err != nil {
		return nil, templateFileRefused, fmt.Errorf("%w: %q: %v", ErrTemplateContentRefused, filepath.ToSlash(clean), err)
	}
	f, err := r.Open(clean)
	if err != nil {
		return nil, templateFileRefused, fmt.Errorf("%w: %q: %v", ErrTemplateContentRefused, filepath.ToSlash(clean), err)
	}
	defer func() { _ = f.Close() }()
	oinfo, err := f.Stat()
	if err != nil {
		return nil, templateFileRefused, fmt.Errorf("%w: %q: %v", ErrTemplateContentRefused, filepath.ToSlash(clean), err)
	}
	if !oinfo.Mode().IsRegular() || !os.SameFile(linfo, oinfo) {
		return nil, templateFileRefused, fmt.Errorf("%w: %q changed while it was read", ErrTemplateContentRefused, filepath.ToSlash(clean))
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, templateFileRefused, fmt.Errorf("failed to read template file %q: %w", filepath.ToSlash(clean), err)
	}
	return data, templateFileRegular, nil
}

// TemplateFileExists reports whether name (a relative path) is present in
// the template directory dir. It returns an error wrapping
// ErrTemplateContentRefused when an entry exists but would be refused by
// the template file reads (a symbolic link, a non-regular file, or a path
// that leaves dir), so callers that detect files by convention never treat a
// refused file as absent or as present.
func TemplateFileExists(dir, name string) (bool, error) {
	_, state, err := readTemplateFile(dir, name)
	switch state {
	case templateFileRegular:
		return true, nil
	case templateFileRefused:
		return false, err
	default:
		return false, err
	}
}

// scionAgentConfigNames are the template config file names, in lookup order.
var scionAgentConfigNames = []string{"scion-agent.yaml", "scion-agent.yml", "scion-agent.json"}

// readTemplateConfigFile reads the template config file in dir without
// following symbolic links. The first of scionAgentConfigNames that is
// present decides: a regular file is returned with its name, a refused one
// returns an error. found is false when none is present.
func readTemplateConfigFile(dir string) (data []byte, name string, found bool, err error) {
	for _, n := range scionAgentConfigNames {
		b, state, rerr := readTemplateFile(dir, n)
		switch state {
		case templateFileRegular:
			return b, n, true, nil
		case templateFileRefused:
			return nil, n, true, rerr
		}
		if rerr != nil {
			return nil, n, false, rerr
		}
	}
	return nil, "", false, nil
}
