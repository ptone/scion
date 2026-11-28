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

package hub

import (
	"context"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/agent"
	"github.com/GoogleCloudPlatform/scion/pkg/ent"
	"github.com/GoogleCloudPlatform/scion/pkg/ent/githubresolutioncache"
	"github.com/GoogleCloudPlatform/scion/pkg/ent/schema"
)

// GitHubFileEntry represents a single file in a GitHub skill resolution result.
// This is a value type (not an ent entity) stored as JSON.
type GitHubFileEntry = schema.GitHubFileEntry

// GitHubCacheEntry represents a cached GitHub skill resolution result (either
// a mutable ref row or an immutable commit-pinned tree row).
type GitHubCacheEntry struct {
	CommitSHA   string
	FileEntries []GitHubFileEntry
	BundleHash  string
	TokenScope  string
	ETag        string
	ExpiresAt   time.Time
	OriginalURI string
}

// GitHubResolutionStore wraps the ent client for GitHub skill resolution cache operations.
type GitHubResolutionStore struct {
	client *ent.Client
}

// NewGitHubResolutionStore creates a new GitHubResolutionStore.
func NewGitHubResolutionStore(client *ent.Client) *GitHubResolutionStore {
	return &GitHubResolutionStore{client: client}
}

// load reads the row for cacheKey, returning (nil, nil) when there is none.
func (s *GitHubResolutionStore) load(ctx context.Context, cacheKey string) (*GitHubCacheEntry, error) {
	row, err := s.client.GitHubResolutionCache.
		Query().
		Where(githubresolutioncache.CacheKeyEQ(cacheKey)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return &GitHubCacheEntry{
		CommitSHA:   row.CommitSha,
		FileEntries: row.FileEntries,
		BundleHash:  row.BundleHash,
		TokenScope:  row.TokenScope,
		ETag:        row.Etag,
		ExpiresAt:   row.ExpiresAt,
		OriginalURI: row.OriginalURI,
	}, nil
}

// Get retrieves a cache entry by key, returning (entry, true, nil) on hit,
// (nil, false, nil) on miss or expiration, or (nil, false, error) on DB error.
func (s *GitHubResolutionStore) Get(ctx context.Context, cacheKey string) (*GitHubCacheEntry, bool, error) {
	entry, err := s.load(ctx, cacheKey)
	if err != nil || entry == nil {
		return nil, false, err
	}
	if time.Now().After(entry.ExpiresAt) {
		return nil, false, nil
	}
	return entry, true, nil
}

// GetStale returns a cache entry for cacheKey even though its TTL has
// expired, provided it is within maxStaleAge of its last successful
// resolution. lastTTL is the TTL that was used to compute the row's
// ExpiresAt when it was last written — the schema has no separate
// "last resolved at" column, so the last resolution time is derived as
// ExpiresAt - lastTTL instead. Callers must only use this for branch refs
// (a known, fixed TTL); a commit-SHA entry's TTL differs and, being
// immutable, has no use for staleness in the first place.
//
// Returns (nil, false, nil) when the row does not exist or is older than
// maxStaleAge, and (nil, false, error) on a DB error.
func (s *GitHubResolutionStore) GetStale(ctx context.Context, cacheKey string, lastTTL, maxStaleAge time.Duration) (*GitHubCacheEntry, bool, error) {
	entry, err := s.load(ctx, cacheKey)
	if err != nil || entry == nil {
		return nil, false, err
	}
	if !entry.ExpiresAt.After(staleCutoff(time.Now(), lastTTL, maxStaleAge)) {
		return nil, false, nil
	}
	return entry, true, nil
}

// GetTreeForStaleRef loads a commit-pinned tree row paired with a stale ref
// row. Because tree rows are immutable at their commit SHA and the ref row
// already enforces the stale window, any existing tree row is usable.
func (s *GitHubResolutionStore) GetTreeForStaleRef(ctx context.Context, cacheKey string) (*GitHubCacheEntry, bool, error) {
	entry, err := s.load(ctx, cacheKey)
	if err != nil || entry == nil {
		return nil, false, err
	}
	return entry, true, nil
}

// Put upserts a cache entry. If an entry with the same cache_key exists, it is updated.
//
// OnConflictColumns names cache_key (the table's unique index, see the
// GitHubResolutionCache schema) as the conflict target explicitly. Without
// it, ent emits "INSERT ... ON CONFLICT DO UPDATE SET ..." with no inference
// specification: Postgres rejects that unconditionally ("ON CONFLICT DO
// UPDATE requires inference specification or constraint name"), so every
// write failed there and the cache was never populated. SQLite 3.35+ accepts
// the same statement by inferring the lone eligible unique index, which is
// why this went unnoticed in SQLite-only tests.
func (s *GitHubResolutionStore) Put(ctx context.Context, cacheKey string, entry GitHubCacheEntry) error {
	return s.client.GitHubResolutionCache.
		Create().
		SetCacheKey(cacheKey).
		SetOriginalURI(entry.OriginalURI).
		SetCommitSha(entry.CommitSHA).
		SetFileEntries(entry.FileEntries).
		SetBundleHash(entry.BundleHash).
		SetTokenScope(entry.TokenScope).
		SetEtag(entry.ETag).
		SetExpiresAt(entry.ExpiresAt).
		OnConflictColumns(githubresolutioncache.FieldCacheKey).
		UpdateNewValues().
		Exec(ctx)
}

// DeleteByRepoAndScope deletes every cached ref and tree row for the given
// repository key prefixes (see githubfetch.RepoCacheKeyPrefixes) belonging to
// tokenScope. Called on authoritative denial (not_found, auth_rejected,
// forbidden) so revoked access stops being served immediately for that scope.
func (s *GitHubResolutionStore) DeleteByRepoAndScope(ctx context.Context, refPrefix, treePrefix, tokenScope string) error {
	_, err := s.client.GitHubResolutionCache.
		Delete().
		Where(
			githubresolutioncache.TokenScopeEQ(tokenScope),
			githubresolutioncache.Or(
				githubresolutioncache.CacheKeyHasPrefix(refPrefix),
				githubresolutioncache.CacheKeyHasPrefix(treePrefix),
			),
		).
		Exec(ctx)
	return err
}

// staleCutoff returns the ExpiresAt threshold at or below which a row can no
// longer be served stale by GetStale(ctx, cacheKey, lastTTL, maxStaleAge): a
// row's last successful resolution time is ExpiresAt - lastTTL (see
// GetStale's own comment), and GetStale keeps serving it stale while now is
// before lastResolvedAt + maxStaleAge — equivalently, while ExpiresAt is
// after this cutoff. GetStale and PurgeExpired both call this so the two
// never disagree about where that line is.
//
// Uses agent.MaxJitteredTTL(lastTTL), not lastTTL itself: ExpiresAt was
// written as time.Now().Add(agent.JitteredTTL(ttl, ...)) (see
// fetchAndCacheGitHubSkill), so the true lastTTL any given row was written
// with is no longer recoverable from the stored ExpiresAt alone. Using the
// jitter's upper bound derives a lastResolvedAt that is never later than the
// row's true one, so a row is never treated as fresher — and so never kept
// stale-servable longer — than its actual age; the cost is that, in the
// worst case (a row whose jitter pushed it to the fast/short end), it stops
// being stale-servable up to the jitter amount before the nominal
// maxStaleAge boundary, never after it.
func staleCutoff(now time.Time, lastTTL, maxStaleAge time.Duration) time.Time {
	return now.Add(agent.MaxJitteredTTL(lastTTL) - maxStaleAge)
}

// PurgeExpired deletes cache entries that can no longer be served stale even
// under a branch ref's own (longer) retention — agent.DefaultResolutionCacheTTL
// and agent.MaxResolutionStaleAge, the only values resolveGitHubSkill ever
// passes to GetStale — using the same staleCutoff GetStale itself checks
// against, so a row GetStale could still have served is never purged out from
// under it. The schema has no column recording whether a row is a branch or a
// commit-SHA ref, so this one cutoff is applied to every row: a SHA-ref row
// (whose own TTL is unrelated to staleness, since GetStale is never consulted
// for one) may then survive somewhat longer than its own TTL before purge,
// which is harmless — its content is immutable, so an unnecessarily long wait
// before deletion costs only a little extra storage, never a wrong answer.
func (s *GitHubResolutionStore) PurgeExpired(ctx context.Context) error {
	cutoff := staleCutoff(time.Now(), agent.DefaultResolutionCacheTTL, agent.MaxResolutionStaleAge)
	_, err := s.client.GitHubResolutionCache.
		Delete().
		Where(githubresolutioncache.ExpiresAtLTE(cutoff)).
		Exec(ctx)
	return err
}
