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

package bundle

import (
	"os"
	"path"
	"strings"

	"github.com/GoogleCloudPlatform/scion/pkg/transfer"
)

// Excluder reports whether a slash-separated root-relative path should be
// omitted from a bundle. When isDir is true and Exclude returns true, the
// walker skips the entire directory subtree.
type Excluder interface {
	Exclude(relPath string, isDir bool) bool
}

// ExcluderFunc adapts a function to Excluder.
type ExcluderFunc func(relPath string, isDir bool) bool

// Exclude calls f(relPath, isDir).
func (f ExcluderFunc) Exclude(relPath string, isDir bool) bool {
	return f(relPath, isDir)
}

// rootResolvableExcluder is implemented by path-based excluders so Write and
// Estimate can rebase exclusion entries through any in-root symlinked ancestor
// directories before walking, preventing excluded secrets from being bundled
// under their physical path.
type rootResolvableExcluder interface {
	resolveInRoot(root *os.Root) Excluder
}

func resolveExcluderInRoot(root *os.Root, ex Excluder) Excluder {
	if r, ok := ex.(rootResolvableExcluder); ok {
		return r.resolveInRoot(root)
	}
	return ex
}

// HomeIdentityCredentialExcludes are identity-bound credentials minted for the
// source broker's identity that must never travel in a move bundle (design §3.4
// item 3 / D6).
var HomeIdentityCredentialExcludes = []string{
	".config/gcloud/credentials.db",
	".config/gcloud/access_tokens.db",
	".config/gcloud/application_default_credentials.json",
	".config/gcloud/legacy_credentials/**",
	".docker/config.json",
	".kube/config",
}

// HomeCacheExcludes are regenerable cache directories omitted from a $HOME
// bundle to save bandwidth and space (design §3.4 item 4 / D6).
var HomeCacheExcludes = []string{
	".cache/**",
	".npm/_cacache/**",
	"go/pkg/mod/cache/**",
}

// WorkspaceExcluder returns the excluder for a workspace root: root `.scion`
// and `.scion/**` only (case-insensitive on the root element, matching
// transfer.ExcludeWorkspaceObject). `.git/**` and nested `a/.scion` are kept.
func WorkspaceExcluder() Excluder {
	return ExcluderFunc(func(relPath string, _ bool) bool {
		first, _, _ := strings.Cut(relPath, "/")
		return transfer.IsWorkspaceIdentityName(first)
	})
}

// HomeExcluder returns the excluder for a $HOME root: the union of
// caller-supplied extraLiterals (such as runtime.PortableHomeExcludes(cfg) for
// Scion-managed secrets, hub token, reset marker, and per-agent resolved
// secrets/auth — matched literally as exact path or subtree prefix, never as
// globs), an optional tempNamePred (such as runtime.IsHomeTempName) matched
// against each entry's basename, HomeIdentityCredentialExcludes, and
// HomeCacheExcludes (design §3.4 "Exclusions" items 1–4).
//
// When passed to Write or Estimate, every exclusion entry is also resolved
// through in-root symlinks before the walk so an excluded secret under a
// symlinked directory (e.g. ".config/gcloud -> ../dotfiles/gcloud") is also
// excluded at its physical path.
func HomeExcluder(extraLiterals []string, tempNamePred func(base string) bool) Excluder {
	prefixes := make([]string, 0, len(extraLiterals)+len(HomeIdentityCredentialExcludes)+len(HomeCacheExcludes))
	for _, lit := range extraLiterals {
		lit = strings.TrimSpace(lit)
		if lit == "" {
			continue
		}
		prefixes = append(prefixes, lit)
	}
	for _, pat := range HomeIdentityCredentialExcludes {
		if p := normalizePrefixPattern(pat); p != "" {
			prefixes = append(prefixes, p)
		}
	}
	for _, pat := range HomeCacheExcludes {
		if p := normalizePrefixPattern(pat); p != "" {
			prefixes = append(prefixes, p)
		}
	}
	return &prefixExcluder{
		prefixes:     prefixes,
		tempNamePred: tempNamePred,
	}
}

// PatternExcluder builds an Excluder from slash-separated relative patterns.
//
// Supported forms (no glob metacharacter parsing, so paths containing
// brackets or wildcards such as ".config/app[1]/token" match literally and
// never fail open):
//   - "prefix/**" matches "prefix" itself and any descendant "prefix/...".
//   - Any other pattern is matched literally as an exact path or subtree
//     prefix ("a/b" matches "a/b" and "a/b/...").
func PatternExcluder(patterns []string) Excluder {
	prefixes := make([]string, 0, len(patterns))
	for _, pat := range patterns {
		if p := normalizePrefixPattern(pat); p != "" {
			prefixes = append(prefixes, p)
		}
	}
	return &prefixExcluder{prefixes: prefixes}
}

func normalizePrefixPattern(pat string) string {
	pat = strings.TrimSpace(pat)
	if pat == "" {
		return ""
	}
	if prefix, ok := strings.CutSuffix(pat, "/**"); ok {
		return prefix
	}
	return pat
}

type prefixExcluder struct {
	prefixes     []string
	tempNamePred func(base string) bool
}

func (e *prefixExcluder) Exclude(relPath string, _ bool) bool {
	if e.tempNamePred != nil && e.tempNamePred(path.Base(relPath)) {
		return true
	}
	for _, p := range e.prefixes {
		if relPath == p || strings.HasPrefix(relPath, p+"/") {
			return true
		}
	}
	return false
}

// resolveInRoot expands each exclusion prefix through any in-root symlinks
// (ancestor directories or final symlink targets) so that excluded entries are
// omitted at both their logical and physical paths.
func (e *prefixExcluder) resolveInRoot(root *os.Root) Excluder {
	seen := make(map[string]struct{}, len(e.prefixes))
	resolved := make([]string, 0, len(e.prefixes))
	add := func(p string) {
		if p == "" {
			return
		}
		if _, dup := seen[p]; dup {
			return
		}
		seen[p] = struct{}{}
		resolved = append(resolved, p)
	}
	for _, p := range e.prefixes {
		add(p)
		if phys, err := resolvePathInRoot(root, ".", p); err == nil && phys != "" {
			add(phys)
		}
	}
	return &prefixExcluder{
		prefixes:     resolved,
		tempNamePred: e.tempNamePred,
	}
}
