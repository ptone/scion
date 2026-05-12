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

package hubclient

import (
	"context"
	"io"
	"net/url"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/apiclient"
	"github.com/GoogleCloudPlatform/scion/pkg/transfer"
)

// SkillService handles skill bank operations.
type SkillService interface {
	// List returns skills matching the filter criteria.
	List(ctx context.Context, opts *ListSkillsOptions) (*ListSkillsResponse, error)

	// Get returns a skill by ID.
	Get(ctx context.Context, skillID string) (*Skill, error)

	// Create creates a new skill.
	Create(ctx context.Context, req *CreateSkillRequest) (*CreateSkillResponse, error)

	// Delete removes a skill.
	Delete(ctx context.Context, skillID string) error

	// PublishVersion publishes a new skill version.
	PublishVersion(ctx context.Context, skillID string, req *PublishVersionRequest) (*SkillVersion, error)

	// ListVersions lists all versions for a skill.
	ListVersions(ctx context.Context, skillID string) (*ListVersionsResponse, error)

	// RequestUploadURLs requests signed URLs for uploading skill files.
	RequestUploadURLs(ctx context.Context, skillID, version string, files []FileUploadRequest) (*UploadResponse, error)

	// Finalize finalizes a skill version after file upload.
	Finalize(ctx context.Context, skillID, version string, manifest *SkillManifest) (*SkillVersion, error)

	// RequestDownloadURLs requests signed download URLs.
	RequestDownloadURLs(ctx context.Context, skillID, version string) (*DownloadResponse, error)

	// Resolve batch-resolves skill URIs to download URLs.
	Resolve(ctx context.Context, req *ResolveSkillsRequest) (*ResolveSkillsResponse, error)

	// UploadFile uploads a file to a signed URL.
	UploadFile(ctx context.Context, url, method string, headers map[string]string, content io.Reader) error

	// DownloadFile downloads a file from a signed URL.
	DownloadFile(ctx context.Context, url string) ([]byte, error)
}

// skillService is the implementation of SkillService.
type skillService struct {
	c              *client
	transferClient *transfer.Client
}

// Skill represents a skill from the Hub API.
type Skill struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	DisplayName   string    `json:"displayName,omitempty"`
	Description   string    `json:"description,omitempty"`
	Scope         string    `json:"scope"`
	ScopeID       string    `json:"scopeId,omitempty"`
	Status        string    `json:"status"`
	LatestVersion string    `json:"latestVersion,omitempty"`
	OwnerID       string    `json:"ownerId,omitempty"`
	CreatedBy     string    `json:"createdBy,omitempty"`
	Created       time.Time `json:"created"`
	Updated       time.Time `json:"updated"`
}

// SkillVersion represents a version of a skill.
type SkillVersion struct {
	ID          string      `json:"id"`
	SkillID     string      `json:"skillId"`
	Version     string      `json:"version"`
	ContentHash string      `json:"contentHash,omitempty"`
	Files       []SkillFile `json:"files,omitempty"`
	Status      string      `json:"status"`
	Changelog   string      `json:"changelog,omitempty"`
	CreatedBy   string      `json:"createdBy,omitempty"`
	Created     time.Time   `json:"created"`
}

// SkillFile represents a file within a skill version.
type SkillFile struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
	Hash string `json:"hash"`
	Mode string `json:"mode,omitempty"`
}

// ListSkillsOptions configures skill list filtering.
type ListSkillsOptions struct {
	Name      string // Filter by exact skill name
	Search    string // Full-text search on name/description
	Scope     string // Filter by scope (global, project, user)
	ProjectID string // Filter by project (for project scope)
	Status    string // Filter by status (active, archived)
	Page      apiclient.PageOptions
}

// ListSkillsResponse is the response from listing skills.
type ListSkillsResponse struct {
	Skills []Skill
	Page   apiclient.PageResult
}

// CreateSkillRequest is the request for creating a skill.
type CreateSkillRequest struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName,omitempty"`
	Description string `json:"description,omitempty"`
	Scope       string `json:"scope"`
	ProjectID   string `json:"projectId,omitempty"`
}

// CreateSkillResponse is the response from creating a skill.
type CreateSkillResponse struct {
	Skill *Skill `json:"skill"`
}

// PublishVersionRequest is the request for publishing a skill version.
type PublishVersionRequest struct {
	Version   string `json:"version"`
	Changelog string `json:"changelog,omitempty"`
}

// ListVersionsResponse is the response from listing skill versions.
type ListVersionsResponse struct {
	Versions []SkillVersion `json:"versions"`
}

// SkillManifest is the manifest of uploaded skill files.
type SkillManifest struct {
	Version string      `json:"version"`
	Files   []SkillFile `json:"files"`
}

// ResolveSkillsRequest is the batch-resolution request.
type ResolveSkillsRequest struct {
	Skills    []SkillRef `json:"skills"`
	ProjectID string     `json:"projectId,omitempty"`
	UserID    string     `json:"userId,omitempty"`
}

// SkillRef is a single skill URI to resolve.
type SkillRef struct {
	URI string `json:"uri"`
}

// ResolveSkillsResponse is the batch-resolution response.
type ResolveSkillsResponse struct {
	Resolved []ResolvedSkill  `json:"resolved"`
	Errors   []ResolveError   `json:"errors,omitempty"`
	Warnings []ResolveWarning `json:"warnings,omitempty"`
}

// ResolvedSkill is a successfully resolved skill.
type ResolvedSkill struct {
	URI             string            `json:"uri"`
	Name            string            `json:"name"`
	ResolvedVersion string            `json:"resolvedVersion"`
	ContentHash     string            `json:"contentHash"`
	Files           []DownloadURLInfo `json:"files"`
}

// ResolveError is a resolution failure.
type ResolveError struct {
	URI   string `json:"uri"`
	Error string `json:"error"`
}

// ResolveWarning is a resolution warning (e.g. deprecated skill).
type ResolveWarning struct {
	URI     string `json:"uri"`
	Message string `json:"message"`
}

// List returns skills matching the filter criteria.
func (s *skillService) List(ctx context.Context, opts *ListSkillsOptions) (*ListSkillsResponse, error) {
	query := url.Values{}
	if opts != nil {
		if opts.Name != "" {
			query.Set("name", opts.Name)
		}
		if opts.Search != "" {
			query.Set("search", opts.Search)
		}
		if opts.Scope != "" {
			query.Set("scope", opts.Scope)
		}
		if opts.ProjectID != "" {
			query.Set("projectId", opts.ProjectID)
		}
		if opts.Status != "" {
			query.Set("status", opts.Status)
		}
		opts.Page.ToQuery(query)
	}

	resp, err := s.c.getWithQuery(ctx, "/api/v1/skills", query, nil)
	if err != nil {
		return nil, err
	}

	type listResponse struct {
		Skills     []Skill `json:"skills"`
		NextCursor string  `json:"nextCursor,omitempty"`
		TotalCount int     `json:"totalCount,omitempty"`
	}

	result, err := apiclient.DecodeResponse[listResponse](resp)
	if err != nil {
		return nil, err
	}

	return &ListSkillsResponse{
		Skills: result.Skills,
		Page: apiclient.PageResult{
			NextCursor: result.NextCursor,
			TotalCount: result.TotalCount,
		},
	}, nil
}

// Get returns a skill by ID.
func (s *skillService) Get(ctx context.Context, skillID string) (*Skill, error) {
	resp, err := s.c.get(ctx, "/api/v1/skills/"+skillID, nil)
	if err != nil {
		return nil, err
	}
	return apiclient.DecodeResponse[Skill](resp)
}

// Create creates a new skill.
func (s *skillService) Create(ctx context.Context, req *CreateSkillRequest) (*CreateSkillResponse, error) {
	resp, err := s.c.post(ctx, "/api/v1/skills", req, nil)
	if err != nil {
		return nil, err
	}
	return apiclient.DecodeResponse[CreateSkillResponse](resp)
}

// Delete removes a skill.
func (s *skillService) Delete(ctx context.Context, skillID string) error {
	resp, err := s.c.delete(ctx, "/api/v1/skills/"+skillID, nil)
	if err != nil {
		return err
	}
	return apiclient.CheckResponse(resp)
}

// PublishVersion publishes a new skill version.
func (s *skillService) PublishVersion(ctx context.Context, skillID string, req *PublishVersionRequest) (*SkillVersion, error) {
	resp, err := s.c.post(ctx, "/api/v1/skills/"+skillID+"/versions", req, nil)
	if err != nil {
		return nil, err
	}
	return apiclient.DecodeResponse[SkillVersion](resp)
}

// ListVersions lists all versions for a skill.
func (s *skillService) ListVersions(ctx context.Context, skillID string) (*ListVersionsResponse, error) {
	resp, err := s.c.get(ctx, "/api/v1/skills/"+skillID+"/versions", nil)
	if err != nil {
		return nil, err
	}
	return apiclient.DecodeResponse[ListVersionsResponse](resp)
}

// RequestUploadURLs requests signed URLs for uploading skill files.
func (s *skillService) RequestUploadURLs(ctx context.Context, skillID, version string, files []FileUploadRequest) (*UploadResponse, error) {
	req := struct {
		Files []FileUploadRequest `json:"files"`
	}{
		Files: files,
	}
	resp, err := s.c.post(ctx, "/api/v1/skills/"+skillID+"/versions/"+version+"/upload", req, nil)
	if err != nil {
		return nil, err
	}
	return apiclient.DecodeResponse[UploadResponse](resp)
}

// Finalize finalizes a skill version after file upload.
func (s *skillService) Finalize(ctx context.Context, skillID, version string, manifest *SkillManifest) (*SkillVersion, error) {
	req := struct {
		Manifest *SkillManifest `json:"manifest"`
	}{
		Manifest: manifest,
	}
	resp, err := s.c.post(ctx, "/api/v1/skills/"+skillID+"/versions/"+version+"/finalize", req, nil)
	if err != nil {
		return nil, err
	}
	return apiclient.DecodeResponse[SkillVersion](resp)
}

// RequestDownloadURLs requests signed download URLs.
func (s *skillService) RequestDownloadURLs(ctx context.Context, skillID, version string) (*DownloadResponse, error) {
	resp, err := s.c.get(ctx, "/api/v1/skills/"+skillID+"/versions/"+version+"/download", nil)
	if err != nil {
		return nil, err
	}
	return apiclient.DecodeResponse[DownloadResponse](resp)
}

// Resolve batch-resolves skill URIs to download URLs.
func (s *skillService) Resolve(ctx context.Context, req *ResolveSkillsRequest) (*ResolveSkillsResponse, error) {
	resp, err := s.c.post(ctx, "/api/v1/skills/resolve", req, nil)
	if err != nil {
		return nil, err
	}
	return apiclient.DecodeResponse[ResolveSkillsResponse](resp)
}

// UploadFile uploads a file to a signed URL.
func (s *skillService) UploadFile(ctx context.Context, signedURL string, method string, headers map[string]string, content io.Reader) error {
	client := s.getTransferClient()
	return client.UploadFileWithMethod(ctx, signedURL, method, headers, content)
}

// DownloadFile downloads a file from a signed URL.
func (s *skillService) DownloadFile(ctx context.Context, signedURL string) ([]byte, error) {
	client := s.getTransferClient()
	return client.DownloadFile(ctx, signedURL)
}

// getTransferClient returns the transfer client, creating one if necessary.
func (s *skillService) getTransferClient() *transfer.Client {
	if s.transferClient == nil {
		s.transferClient = transfer.NewClient(s.c.transport.AuthenticatedHTTPClient())
	}
	return s.transferClient
}
