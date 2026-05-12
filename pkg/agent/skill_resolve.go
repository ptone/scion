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

package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/hubclient"
	"github.com/GoogleCloudPlatform/scion/pkg/transfer"
	"github.com/GoogleCloudPlatform/scion/pkg/util"
)

// ---------- context helpers for hub client ----------

type contextKey string

const hubClientCtxKey contextKey = "hubClient"

// ContextWithHubClient returns a new context with the hub client attached.
// The provisioner uses this to resolve skill references from the Hub.
func ContextWithHubClient(ctx context.Context, c hubclient.Client) context.Context {
	return context.WithValue(ctx, hubClientCtxKey, c)
}

// hubClientFromContext extracts the hub client from the context, or nil if not set.
func hubClientFromContext(ctx context.Context) hubclient.Client {
	c, _ := ctx.Value(hubClientCtxKey).(hubclient.Client)
	return c
}

// ---------- skill URI parsing ----------

// SkillURI represents a parsed skill URI with its individual components.
//
// Full form: skill://<registry>/<scope>/<name>@<version>
// Examples:
//
//	skill://scion/core/scion@^1.0
//	skill:///core/scion@^1.0   (registry defaults to "scion")
//	scion                       (bare name, scope-search resolution)
type SkillURI struct {
	Registry string // e.g. "scion", "registry.agentskills.io"
	Scope    string // e.g. "core", "global", "project/<id>", "user/<id>"
	Name     string // skill identifier (kebab-case)
	Version  string // semver constraint or "latest"
}

// ParseSkillURI parses a skill reference string into its components.
//
// Accepted formats:
//   - Full URI:   skill://scion/core/my-skill@^1.0
//   - No registry: skill:///core/my-skill@^1.0
//   - No version: skill://scion/core/my-skill
//   - Bare name:  my-skill  (resolved via scope search order)
//   - Name@ver:   my-skill@1.2.0
func ParseSkillURI(raw string) (*SkillURI, error) {
	if raw == "" {
		return nil, fmt.Errorf("empty skill URI")
	}

	uri := &SkillURI{
		Registry: "scion",
		Version:  "latest",
	}

	// Check for skill:// scheme
	if strings.HasPrefix(raw, "skill://") {
		return parseFullSkillURI(raw, uri)
	}

	// Bare name or name@version
	return parseBareSkillRef(raw, uri)
}

// parseFullSkillURI handles the skill://registry/scope/name@version format.
func parseFullSkillURI(raw string, uri *SkillURI) (*SkillURI, error) {
	// Strip scheme
	rest := strings.TrimPrefix(raw, "skill://")

	// Split off version
	rest, version := splitVersion(rest)
	if version != "" {
		uri.Version = version
	}

	// Split on "/" to get registry/scope/name
	parts := strings.SplitN(rest, "/", 3)

	switch len(parts) {
	case 1:
		// skill://name — bare name with scheme (unusual but handle it)
		if parts[0] == "" {
			return nil, fmt.Errorf("invalid skill URI: missing name in %q", raw)
		}
		uri.Name = parts[0]
	case 2:
		// skill://registry/name or skill:///name (empty registry)
		if parts[0] != "" {
			uri.Registry = parts[0]
		}
		if parts[1] == "" {
			return nil, fmt.Errorf("invalid skill URI: missing name in %q", raw)
		}
		uri.Name = parts[1]
	default:
		// skill://registry/scope/name
		if parts[0] != "" {
			uri.Registry = parts[0]
		}
		uri.Scope = parts[1]
		if parts[2] == "" {
			return nil, fmt.Errorf("invalid skill URI: missing name in %q", raw)
		}
		uri.Name = parts[2]
	}

	return uri, nil
}

// parseBareSkillRef handles bare name or name@version format.
func parseBareSkillRef(raw string, uri *SkillURI) (*SkillURI, error) {
	name, version := splitVersion(raw)
	if version != "" {
		uri.Version = version
	}
	if name == "" {
		return nil, fmt.Errorf("invalid skill reference: empty name in %q", raw)
	}
	uri.Name = name
	return uri, nil
}

// splitVersion splits a string into (rest, version) at the last "@".
func splitVersion(s string) (string, string) {
	idx := strings.LastIndex(s, "@")
	if idx < 0 {
		return s, ""
	}
	return s[:idx], s[idx+1:]
}

// String returns the canonical URI string.
func (u *SkillURI) String() string {
	var sb strings.Builder
	sb.WriteString("skill://")
	sb.WriteString(u.Registry)
	sb.WriteString("/")
	if u.Scope != "" {
		sb.WriteString(u.Scope)
		sb.WriteString("/")
	}
	sb.WriteString(u.Name)
	if u.Version != "" && u.Version != "latest" {
		sb.WriteString("@")
		sb.WriteString(u.Version)
	}
	return sb.String()
}

// ---------- skill reference → URI conversion ----------

// skillRefToURI converts a SkillReference to a URI string suitable for
// the hub Resolve() API. If the reference already has a URI, it is returned
// directly. Otherwise, a URI is constructed from Name and Version.
func skillRefToURI(ref api.SkillReference) string {
	if ref.URI != "" {
		return ref.URI
	}
	// Construct from name+version. Use bare name format so the hub
	// performs scope-search resolution.
	if ref.Version != "" {
		return ref.Name + "@" + ref.Version
	}
	return ref.Name
}

// ---------- skill resolution ----------

// resolvedSkillInfo holds the result of resolving a single skill reference.
type resolvedSkillInfo struct {
	// Reference is the original skill reference.
	Reference api.SkillReference
	// Resolved is the hub resolution result (nil if resolution failed for optional skill).
	Resolved *hubclient.ResolvedSkill
}

// resolveSkillReferences batch-resolves skill references via the hub client,
// downloads the resolved files, verifies content hashes, and places them into
// the agent's skills directory.
//
// Parameters:
//   - ctx: context (may carry hub client)
//   - hubClient: the hub client to use for resolution
//   - refs: skill references from the merged ScionConfig
//   - skillsDestDir: the destination directory for skills (agentHome/<skillsDir>)
//   - projectID: current project ID (for scope resolution, may be empty)
//   - userID: current user ID (for scope resolution, may be empty)
func resolveSkillReferences(
	ctx context.Context,
	hubClient hubclient.Client,
	refs []api.SkillReference,
	skillsDestDir string,
	projectID string,
	userID string,
) error {
	if len(refs) == 0 || hubClient == nil {
		return nil
	}

	// Build the batch resolve request
	skillRefs := make([]hubclient.SkillRef, 0, len(refs))
	for _, ref := range refs {
		uri := skillRefToURI(ref)
		skillRefs = append(skillRefs, hubclient.SkillRef{URI: uri})
	}

	resolveReq := &hubclient.ResolveSkillsRequest{
		Skills:    skillRefs,
		ProjectID: projectID,
		UserID:    userID,
	}

	util.Debugf("resolveSkillReferences: resolving %d skill references", len(refs))

	resp, err := hubClient.Skills().Resolve(ctx, resolveReq)
	if err != nil {
		// Check if all skills are optional — if so, log and skip
		allOptional := true
		for _, ref := range refs {
			if !ref.Optional {
				allOptional = false
				break
			}
		}
		if allOptional {
			util.Debugf("resolveSkillReferences: hub resolve failed but all skills optional, skipping: %v", err)
			return nil
		}
		return fmt.Errorf("hub skill resolve: %w", err)
	}

	// Build a lookup map from URI to SkillReference for optional/as handling
	refByURI := make(map[string]api.SkillReference, len(refs))
	for _, ref := range refs {
		uri := skillRefToURI(ref)
		refByURI[uri] = ref
	}

	// Check for resolution errors
	for _, resolveErr := range resp.Errors {
		ref, ok := refByURI[resolveErr.URI]
		if ok && ref.Optional {
			util.Debugf("resolveSkillReferences: optional skill %s failed to resolve: %s", resolveErr.URI, resolveErr.Error)
			continue
		}
		return fmt.Errorf("skill %s: %s", resolveErr.URI, resolveErr.Error)
	}

	// Log warnings
	for _, w := range resp.Warnings {
		util.Debugf("resolveSkillReferences: warning for %s: %s", w.URI, w.Message)
	}

	// Download and place resolved skills
	for _, resolved := range resp.Resolved {
		ref := refByURI[resolved.URI]

		// Determine destination directory name
		destName := ref.As
		if destName == "" {
			destName = resolved.Name
		}
		if destName == "" {
			// Fallback: parse name from URI
			parsed, parseErr := ParseSkillURI(resolved.URI)
			if parseErr == nil {
				destName = parsed.Name
			} else {
				destName = resolved.URI
			}
		}

		destDir := filepath.Join(skillsDestDir, destName)
		if err := os.MkdirAll(destDir, 0755); err != nil {
			return fmt.Errorf("create skill dir %s: %w", destName, err)
		}

		util.Debugf("resolveSkillReferences: downloading skill %s (version %s) → %s",
			resolved.URI, resolved.ResolvedVersion, destDir)

		if err := downloadSkillFiles(ctx, hubClient, resolved, destDir, ref.Optional); err != nil {
			if ref.Optional {
				util.Debugf("resolveSkillReferences: optional skill %s download failed: %v", resolved.URI, err)
				// Clean up partial directory
				_ = os.RemoveAll(destDir)
				continue
			}
			return fmt.Errorf("download skill %s: %w", resolved.URI, err)
		}
	}

	return nil
}

// downloadSkillFiles downloads all files for a resolved skill, verifies
// their content hashes, and writes them to the destination directory.
func downloadSkillFiles(
	ctx context.Context,
	hubClient hubclient.Client,
	resolved hubclient.ResolvedSkill,
	destDir string,
	optional bool,
) error {
	skillsSvc := hubClient.Skills()

	for _, file := range resolved.Files {
		if file.URL == "" {
			continue
		}

		data, err := skillsSvc.DownloadFile(ctx, file.URL)
		if err != nil {
			return fmt.Errorf("download %s: %w", file.Path, err)
		}

		// Verify content hash if provided
		if file.Hash != "" {
			actualHash := transfer.HashBytes(data)
			if actualHash != file.Hash {
				return fmt.Errorf("hash mismatch for %s: expected %s, got %s",
					file.Path, file.Hash, actualHash)
			}
		}

		// Write file to destination
		destPath := filepath.Join(destDir, file.Path)

		// Ensure parent directory exists (for nested paths like scripts/analyze.sh)
		if dir := filepath.Dir(destPath); dir != destDir {
			if err := os.MkdirAll(dir, 0755); err != nil {
				return fmt.Errorf("create parent dir for %s: %w", file.Path, err)
			}
		}

		// Determine file mode: executable for scripts, default 0644 otherwise
		mode := os.FileMode(0644)
		if strings.HasSuffix(file.Path, ".sh") || strings.HasSuffix(file.Path, ".py") {
			mode = 0755
		}

		if err := os.WriteFile(destPath, data, mode); err != nil {
			return fmt.Errorf("write %s: %w", file.Path, err)
		}

		util.Debugf("resolveSkillReferences: wrote %s (%d bytes, hash=%s)",
			destPath, len(data), file.Hash)
	}

	return nil
}
