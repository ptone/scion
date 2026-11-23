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
	"path"
	"regexp"
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

// HomeBaselineExcludes are the static Scion-managed secret and runtime-state
// paths always excluded from a portable $HOME bundle (design §3.4 items 1–2),
// even when no per-agent RunConfig extras are supplied.
var HomeBaselineExcludes = []string{
	".scion/scion-token",
	".scion/scion-token.tmp",
	".scion/secrets.json",
	".scion/harness/secrets",
	".scion/harness/outputs",
	".scion-home-reset-id",
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

// homeTempNameRE matches temporary files left behind by Scion's guarded
// writers (agenthome's ".<name>.tmp-<16 hex>" and dirfd's
// ".<name>.tmp-<pid>-<nanoseconds>"), mirroring runtime.IsHomeTempName.
var homeTempNameRE = regexp.MustCompile(`^\..+\.tmp-([0-9a-f]{16}|[0-9]+-[0-9]+)$`)

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
// HomeBaselineExcludes, any caller-supplied extraPatterns (such as
// runtime.PortableHomeExcludes(cfg) for per-agent resolved secrets and auth
// files), HomeIdentityCredentialExcludes, HomeCacheExcludes, and Scion guarded
// writer temp filenames (design §3.4 "Exclusions" items 1–4).
func HomeExcluder(extraPatterns []string) Excluder {
	patterns := make([]string, 0, len(HomeBaselineExcludes)+len(extraPatterns)+len(HomeIdentityCredentialExcludes)+len(HomeCacheExcludes))
	patterns = append(patterns, HomeBaselineExcludes...)
	patterns = append(patterns, extraPatterns...)
	patterns = append(patterns, HomeIdentityCredentialExcludes...)
	patterns = append(patterns, HomeCacheExcludes...)
	patEx := PatternExcluder(patterns)
	return ExcluderFunc(func(relPath string, isDir bool) bool {
		if homeTempNameRE.MatchString(path.Base(relPath)) {
			return true
		}
		return patEx.Exclude(relPath, isDir)
	})
}

// PatternExcluder builds an Excluder from slash-separated relative patterns.
//
// Supported forms:
//   - "prefix/**" matches "prefix" itself and any descendant "prefix/...".
//   - "**/suffix" matches any path whose basename or trailing path equals suffix.
//   - Plain paths without glob metacharacters ("a/b") match "a/b" and any
//     descendant "a/b/..." (matching runtime.homeRelExcluded semantics).
//   - Simple path.Match globs (containing *, ?, or [) match the full relative
//     path, or the basename if the pattern contains no "/".
func PatternExcluder(patterns []string) Excluder {
	cleaned := make([]string, 0, len(patterns))
	for _, p := range patterns {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		cleaned = append(cleaned, p)
	}
	return ExcluderFunc(func(relPath string, _ bool) bool {
		for _, pat := range cleaned {
			if matchExcludePattern(pat, relPath) {
				return true
			}
		}
		return false
	})
}

func matchExcludePattern(pat, relPath string) bool {
	if prefix, ok := strings.CutSuffix(pat, "/**"); ok {
		return relPath == prefix || strings.HasPrefix(relPath, prefix+"/")
	}
	if suffix, ok := strings.CutPrefix(pat, "**/"); ok {
		return relPath == suffix || strings.HasSuffix(relPath, "/"+suffix)
	}
	if strings.ContainsAny(pat, "*?[") {
		if !strings.Contains(pat, "/") {
			if matched, _ := path.Match(pat, path.Base(relPath)); matched {
				return true
			}
		}
		if matched, _ := path.Match(pat, relPath); matched {
			return true
		}
		return false
	}
	// Plain relative path: matches exact path or any descendant.
	return relPath == pat || strings.HasPrefix(relPath, pat+"/")
}
