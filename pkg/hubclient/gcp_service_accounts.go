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
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/apiclient"
)

// GCPServiceAccountService handles GCP service account operations for a grove.
type GCPServiceAccountService interface {
	// List returns all GCP service accounts for the grove.
	List(ctx context.Context) ([]GCPServiceAccount, error)

	// Get returns a specific GCP service account by ID.
	Get(ctx context.Context, id string) (*GCPServiceAccount, error)

	// Create registers a new GCP service account.
	Create(ctx context.Context, req *CreateGCPServiceAccountRequest) (*GCPServiceAccount, error)

	// Delete removes a GCP service account registration.
	Delete(ctx context.Context, id string) error

	// Verify triggers verification that the Hub can impersonate the SA.
	Verify(ctx context.Context, id string) (*GCPServiceAccount, error)
}

// GCPServiceAccount represents a registered GCP service account.
type GCPServiceAccount struct {
	ID                 string    `json:"id"`
	Scope              string    `json:"scope"`
	ScopeID            string    `json:"scope_id"`
	Email              string    `json:"email"`
	ProjectID          string    `json:"project_id"`
	DisplayName        string    `json:"display_name"`
	DefaultScopes      []string  `json:"default_scopes,omitempty"`
	Verified           bool      `json:"verified"`
	VerifiedAt         time.Time `json:"verified_at,omitempty"`
	VerificationStatus string    `json:"verificationStatus,omitempty"`
	VerificationError  string    `json:"verificationError,omitempty"`
	CreatedBy          string    `json:"created_by"`
	CreatedAt          time.Time `json:"created_at"`
}

// CreateGCPServiceAccountRequest is the request for registering a GCP SA.
type CreateGCPServiceAccountRequest struct {
	Email       string   `json:"email"`
	ProjectID   string   `json:"project_id"`
	DisplayName string   `json:"display_name,omitempty"`
	Scopes      []string `json:"default_scopes,omitempty"`
}

// gcpServiceAccountService is the implementation of GCPServiceAccountService.
type gcpServiceAccountService struct {
	c       *client
	groveID string
}

func (s *gcpServiceAccountService) basePath() string {
	return fmt.Sprintf("/api/v1/groves/%s/gcp-service-accounts", s.groveID)
}

func (s *gcpServiceAccountService) List(ctx context.Context) ([]GCPServiceAccount, error) {
	resp, err := s.c.transport.Get(ctx, s.basePath(), nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= http.StatusBadRequest {
		return nil, apiclient.ParseErrorResponse(resp)
	}
	var result []GCPServiceAccount
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return result, nil
}

func (s *gcpServiceAccountService) Get(ctx context.Context, id string) (*GCPServiceAccount, error) {
	path := fmt.Sprintf("%s/%s", s.basePath(), id)
	resp, err := s.c.transport.Get(ctx, path, nil)
	if err != nil {
		return nil, err
	}
	return apiclient.DecodeResponse[GCPServiceAccount](resp)
}

func (s *gcpServiceAccountService) Create(ctx context.Context, req *CreateGCPServiceAccountRequest) (*GCPServiceAccount, error) {
	resp, err := s.c.transport.Post(ctx, s.basePath(), req, nil)
	if err != nil {
		return nil, err
	}
	return apiclient.DecodeResponse[GCPServiceAccount](resp)
}

func (s *gcpServiceAccountService) Delete(ctx context.Context, id string) error {
	path := fmt.Sprintf("%s/%s", s.basePath(), id)
	_, err := s.c.transport.Delete(ctx, path, nil)
	return err
}

func (s *gcpServiceAccountService) Verify(ctx context.Context, id string) (*GCPServiceAccount, error) {
	path := fmt.Sprintf("%s/%s/verify", s.basePath(), id)
	resp, err := s.c.transport.Post(ctx, path, nil, nil)
	if err != nil {
		return nil, err
	}
	return apiclient.DecodeResponse[GCPServiceAccount](resp)
}
