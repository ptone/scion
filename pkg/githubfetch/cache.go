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

package githubfetch

import "strings"

// CacheKeyKind identifies whether a cache key addresses a mutable ref
// resolution or an immutable commit-pinned tree listing.
type CacheKeyKind string

const (
	// CacheKeyRef keys a mutable ref -> ResolvedRef mapping (with ETag).
	CacheKeyRef CacheKeyKind = "ref"
	// CacheKeyTree keys an immutable (commitSHA, path) -> Tree listing,
	// still scoped by principal because repository visibility depends on it.
	CacheKeyTree CacheKeyKind = "tree"
)

// CacheKey builds a canonical cache key for kind (CacheKeyRef or CacheKeyTree).
// For CacheKeyRef, refOrSHA is the ref name and path is ignored.
// For CacheKeyTree, refOrSHA is the commit SHA and path is the directory path.
// apiHost, owner, and repo are lowercased; refOrSHA and path preserve case.
// An empty scope defaults to AnonScope.
func CacheKey(kind CacheKeyKind, apiHost, owner, repo, refOrSHA, path, scope string) string {
	switch kind {
	case CacheKeyTree:
		return TreeCacheKey(apiHost, owner, repo, refOrSHA, path, scope)
	default:
		return RefCacheKey(apiHost, owner, repo, refOrSHA, scope)
	}
}

// RepoCacheKeyPrefixes returns the key prefixes matching every ref and tree
// cache entry for (apiHost, owner, repo):
//
//	refPrefix  = "ref:{host}/{owner}/{repo}@"
//	treePrefix = "tree:{host}/{owner}/{repo}@"
//
// Callers pair these prefixes with a scope equality filter when purging cached
// entries for a principal after an authoritative denial.
func RepoCacheKeyPrefixes(apiHost, owner, repo string) (refPrefix, treePrefix string) {
	root := strings.ToLower(apiHost) + "/" + strings.ToLower(owner) + "/" + strings.ToLower(repo)
	return "ref:" + root + "@", "tree:" + root + "@"
}
