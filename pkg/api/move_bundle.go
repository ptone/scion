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

package api

import (
	"context"
	"fmt"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/transfer/bundle"
)

// WorkspaceSourceMoveBundle is the CreateAgentRequest.WorkspaceSource /
// StartOptions.WorkspaceSource value for the target provision of a bundle
// move (`scion reincarnate --broker`, transfer mode "bundle"). Provision
// creates the marker-guarded fresh agent directory and renders $HOME normally
// while leaving the workspace directory empty (no clone, no worktree, no
// empty-repo init) so the broker's move-bundle/import endpoint can populate it.
const WorkspaceSourceMoveBundle = "move-bundle"

type workspaceSourceContextKey struct{}

// ContextWithWorkspaceSource attaches a provision WorkspaceSource override
// (such as WorkspaceSourceMoveBundle) to ctx.
func ContextWithWorkspaceSource(ctx context.Context, source string) context.Context {
	return context.WithValue(ctx, workspaceSourceContextKey{}, source)
}

// WorkspaceSourceFromContext retrieves the provision WorkspaceSource override
// from ctx, or "" if none was set.
func WorkspaceSourceFromContext(ctx context.Context) string {
	v, _ := ctx.Value(workspaceSourceContextKey{}).(string)
	return v
}

// Default move-bundle timeout values from design §3.7.
const (
	DefaultMoveBundleTimeout   = 20 * time.Minute
	DefaultMoveBundlePartStall = 2 * time.Minute
)

// Move-bundle job kinds and states (design §3.5).
const (
	MoveBundleJobKindExport = "export"
	MoveBundleJobKindImport = "import"

	MoveBundleJobStateRunning   = "running"
	MoveBundleJobStateSucceeded = "succeeded"
	MoveBundleJobStateFailed    = "failed"
)

// MoveBundleLimits carries the hub-configured bounds and timeouts for a
// cross-broker bundle move (design §3.7). Brokers have no limits of their own
// and enforce the values sent by the hub. Zero fields take the design §3.7
// defaults; negative fields are rejected.
type MoveBundleLimits struct {
	WorkspaceMaxBytes   int64 `json:"workspaceMaxBytes,omitempty"`
	WorkspaceMaxEntries int64 `json:"workspaceMaxEntries,omitempty"`
	HomeMaxBytes        int64 `json:"homeMaxBytes,omitempty"`
	HomeMaxEntries      int64 `json:"homeMaxEntries,omitempty"`
	MaxDepth            int   `json:"maxDepth,omitempty"`
	MaxPathLen          int   `json:"maxPathLen,omitempty"`
	PartSize            int64 `json:"partSize,omitempty"`
	TimeoutSeconds      int   `json:"timeoutSeconds,omitempty"`
	PartStallSeconds    int   `json:"partStallSeconds,omitempty"`
}

// Validate rejects negative limit fields.
func (l MoveBundleLimits) Validate() error {
	switch {
	case l.WorkspaceMaxBytes < 0:
		return fmt.Errorf("move bundle limits: workspaceMaxBytes must be >= 0")
	case l.WorkspaceMaxEntries < 0:
		return fmt.Errorf("move bundle limits: workspaceMaxEntries must be >= 0")
	case l.HomeMaxBytes < 0:
		return fmt.Errorf("move bundle limits: homeMaxBytes must be >= 0")
	case l.HomeMaxEntries < 0:
		return fmt.Errorf("move bundle limits: homeMaxEntries must be >= 0")
	case l.MaxDepth < 0:
		return fmt.Errorf("move bundle limits: maxDepth must be >= 0")
	case l.MaxPathLen < 0:
		return fmt.Errorf("move bundle limits: maxPathLen must be >= 0")
	case l.PartSize < 0:
		return fmt.Errorf("move bundle limits: partSize must be >= 0")
	case l.TimeoutSeconds < 0:
		return fmt.Errorf("move bundle limits: timeoutSeconds must be >= 0")
	case l.PartStallSeconds < 0:
		return fmt.Errorf("move bundle limits: partStallSeconds must be >= 0")
	}
	return nil
}

// WorkspaceLimits converts the workspace portion of l to bundle.Limits,
// filling zero fields with design §3.7 defaults.
func (l MoveBundleLimits) WorkspaceLimits() bundle.Limits {
	lim := bundle.DefaultWorkspaceLimits()
	if l.WorkspaceMaxBytes > 0 {
		lim.MaxBytes = l.WorkspaceMaxBytes
	}
	if l.WorkspaceMaxEntries > 0 {
		lim.MaxEntries = l.WorkspaceMaxEntries
	}
	if l.MaxDepth > 0 {
		lim.MaxDepth = l.MaxDepth
	}
	if l.MaxPathLen > 0 {
		lim.MaxPathLen = l.MaxPathLen
	}
	if l.PartSize > 0 {
		lim.PartSize = l.PartSize
	}
	return lim
}

// HomeLimits converts the home portion of l to bundle.Limits, filling zero
// fields with design §3.7 defaults.
func (l MoveBundleLimits) HomeLimits() bundle.Limits {
	lim := bundle.DefaultHomeLimits()
	if l.HomeMaxBytes > 0 {
		lim.MaxBytes = l.HomeMaxBytes
	}
	if l.HomeMaxEntries > 0 {
		lim.MaxEntries = l.HomeMaxEntries
	}
	if l.MaxDepth > 0 {
		lim.MaxDepth = l.MaxDepth
	}
	if l.MaxPathLen > 0 {
		lim.MaxPathLen = l.MaxPathLen
	}
	if l.PartSize > 0 {
		lim.PartSize = l.PartSize
	}
	return lim
}

// Timeout returns the whole-job timeout, defaulting to DefaultMoveBundleTimeout.
func (l MoveBundleLimits) Timeout() time.Duration {
	if l.TimeoutSeconds > 0 {
		return time.Duration(l.TimeoutSeconds) * time.Second
	}
	return DefaultMoveBundleTimeout
}

// PartStallTimeout returns the per-part no-progress timeout, defaulting to
// DefaultMoveBundlePartStall.
func (l MoveBundleLimits) PartStallTimeout() time.Duration {
	if l.PartStallSeconds > 0 {
		return time.Duration(l.PartStallSeconds) * time.Second
	}
	return DefaultMoveBundlePartStall
}

// MoveBundlePrecheckRequest is the request body for
// POST /api/v1/agents/{id}/move-bundle/precheck (hub → source broker).
type MoveBundlePrecheckRequest struct {
	ProjectID         string           `json:"projectId,omitempty"`
	ProjectPath       string           `json:"projectPath,omitempty"`
	ProjectSlug       string           `json:"projectSlug,omitempty"`
	SecretFileTargets []string         `json:"secretFileTargets,omitempty"`
	Limits            MoveBundleLimits `json:"limits,omitempty"`
}

// MoveBundleRootEstimate is the stat-only size/entry estimate for one root.
type MoveBundleRootEstimate struct {
	Bytes   int64 `json:"bytes"`
	Entries int64 `json:"entries"`
	// Over names the limit field that caused the walk to stop early
	// (e.g. "bytes", "entries", "depth", "path_len"), or "" if within limits.
	Over string `json:"over,omitempty"`
}

// MoveBundlePrecheckResponse is the response from
// POST /api/v1/agents/{id}/move-bundle/precheck (design §3.4.1).
type MoveBundlePrecheckResponse struct {
	Durable bool   `json:"durable"`
	Reason  string `json:"reason,omitempty"`
	// Over is non-empty when either root exceeded hub-supplied limits
	// (formatted as "<root>.<field>", e.g. "workspace.bytes").
	Over      string                 `json:"over,omitempty"`
	Workspace MoveBundleRootEstimate `json:"workspace"`
	Home      MoveBundleRootEstimate `json:"home"`
}

// MoveBundleExportRequest is the request body for
// POST /api/v1/agents/{id}/move-bundle/export (hub → source broker).
type MoveBundleExportRequest struct {
	ReincarnationID string `json:"reincarnationId"`
	// AgentID is the hub's agent UUID, used by the broker when requesting
	// signed upload URLs from the hub.
	AgentID           string           `json:"agentId,omitempty"`
	ProjectID         string           `json:"projectId,omitempty"`
	ProjectPath       string           `json:"projectPath,omitempty"`
	ProjectSlug       string           `json:"projectSlug,omitempty"`
	SecretFileTargets []string         `json:"secretFileTargets,omitempty"`
	Limits            MoveBundleLimits `json:"limits,omitempty"`
}

// MoveBundleImportRequest is the request body for
// POST /api/v1/agents/{id}/move-bundle/import (hub → target broker).
type MoveBundleImportRequest struct {
	ReincarnationID string `json:"reincarnationId"`
	// AgentID is the hub's agent UUID, used by the broker when requesting
	// signed download URLs from the hub.
	AgentID      string           `json:"agentId,omitempty"`
	ProjectID    string           `json:"projectId,omitempty"`
	ProjectPath  string           `json:"projectPath,omitempty"`
	ProjectSlug  string           `json:"projectSlug,omitempty"`
	Manifest     bundle.Manifest  `json:"manifest"`
	ManifestHash string           `json:"manifestHash"` // sha256:<hex> of canonical manifest JSON
	Limits       MoveBundleLimits `json:"limits,omitempty"`
}

// MoveBundleProgress reports in-flight transfer progress on a broker job.
type MoveBundleProgress struct {
	BytesDone   int64 `json:"bytesDone"`
	BytesTotal  int64 `json:"bytesTotal,omitempty"`
	EntriesDone int64 `json:"entriesDone,omitempty"`
	PartsDone   int   `json:"partsDone"`
	PartsTotal  int   `json:"partsTotal,omitempty"`
}

// MoveBundleImportSkipCounts reports merge skip counts on the target broker.
type MoveBundleImportSkipCounts struct {
	// HomeProvisionWins is the number of bundled $HOME entries skipped because
	// a provision-rendered file already existed at that path on the target.
	HomeProvisionWins int64 `json:"homeProvisionWins,omitempty"`
}

// MoveBundleJobStatus is the response from POST/GET export and import
// endpoints on the broker (design §3.5). Signed URLs must never appear in
// Error.
type MoveBundleJobStatus struct {
	ReincarnationID string                      `json:"reincarnationId"`
	Kind            string                      `json:"kind"`  // "export" | "import"
	State           string                      `json:"state"` // "running" | "succeeded" | "failed"
	Progress        MoveBundleProgress          `json:"progress"`
	Manifest        *bundle.Manifest            `json:"manifest,omitempty"`
	SkipCounts      *MoveBundleImportSkipCounts `json:"skipCounts,omitempty"`
	Error           string                      `json:"error,omitempty"`
	ErrorCode       string                      `json:"errorCode,omitempty"`
}

// MoveBundleUploadURLRequest is the request body for
// POST /api/v1/runtime-brokers/{brokerId}/agents/{agentId}/move-bundle/{rid}/upload-url
// (source broker → hub, design §3.5).
type MoveBundleUploadURLRequest struct {
	Root      string `json:"root"`      // "workspace" | "home"
	PartIndex int    `json:"partIndex"` // 0-based part index
	Size      int64  `json:"size"`      // compressed part byte size
	Hash      string `json:"hash"`      // sha256:<hex> of compressed part bytes
}

// MoveBundleUploadURLResponse is the response from the hub's upload-url
// endpoint (design §3.5). Signed URLs are bearer capabilities and must never
// be logged or included in error strings.
type MoveBundleUploadURLResponse struct {
	URL              string            `json:"url"`
	Method           string            `json:"method"` // "PUT"
	Headers          map[string]string `json:"headers,omitempty"`
	Expires          time.Time         `json:"expires"`
	ExpiresInSeconds int64             `json:"expiresInSeconds,omitempty"`
}

// MoveBundleDownloadURLsRequest is the request body for
// POST /api/v1/runtime-brokers/{brokerId}/agents/{agentId}/move-bundle/{rid}/download-urls
// (target broker → hub, design §3.5).
type MoveBundleDownloadURLsRequest struct{}

// MoveBundleDownloadPartURL is one signed GET URL for a manifest part.
type MoveBundleDownloadPartURL struct {
	Root    string    `json:"root"`
	Index   int       `json:"index"`
	URL     string    `json:"url"`
	Expires time.Time `json:"expires"`
}

// MoveBundleDownloadURLsResponse is the response from the hub's download-urls
// endpoint (design §3.5).
type MoveBundleDownloadURLsResponse struct {
	Parts            []MoveBundleDownloadPartURL `json:"parts"`
	Expires          time.Time                   `json:"expires"`
	ExpiresInSeconds int64                       `json:"expiresInSeconds,omitempty"`
}
