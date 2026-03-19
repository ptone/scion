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
	"fmt"
	"strings"
	"time"

	"google.golang.org/api/iamcredentials/v1"
	"google.golang.org/api/option"

	"cloud.google.com/go/compute/metadata"
)

// IAMTokenGenerator implements GCPTokenGenerator using the IAM Credentials API
// to impersonate target service accounts. The Hub's own GCP identity (from ADC)
// must have roles/iam.serviceAccountTokenCreator on each target SA.
type IAMTokenGenerator struct {
	service                *iamcredentials.Service
	hubServiceAccountEmail string
}

// NewIAMTokenGenerator creates a new IAMTokenGenerator. It uses Application
// Default Credentials to authenticate with the IAM Credentials API. If
// hubEmail is empty and the Hub is running on GCE/Cloud Run, the email is
// auto-detected from the metadata server.
func NewIAMTokenGenerator(ctx context.Context, hubEmail string, opts ...option.ClientOption) (*IAMTokenGenerator, error) {
	svc, err := iamcredentials.NewService(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("creating IAM credentials service: %w", err)
	}

	if hubEmail == "" {
		hubEmail, _ = metadata.EmailWithContext(ctx, "default")
	}

	return &IAMTokenGenerator{
		service:                svc,
		hubServiceAccountEmail: hubEmail,
	}, nil
}

func saResourceName(email string) string {
	return fmt.Sprintf("projects/-/serviceAccounts/%s", email)
}

func (g *IAMTokenGenerator) GenerateAccessToken(ctx context.Context, serviceAccountEmail string, scopes []string) (*GCPAccessToken, error) {
	req := &iamcredentials.GenerateAccessTokenRequest{
		Scope:    scopes,
		Lifetime: "3600s",
	}
	resp, err := g.service.Projects.ServiceAccounts.GenerateAccessToken(saResourceName(serviceAccountEmail), req).Context(ctx).Do()
	if err != nil {
		return nil, fmt.Errorf("IAM generateAccessToken for %s: %w", serviceAccountEmail, err)
	}

	expiresIn := 3600
	if resp.ExpireTime != "" {
		if t, parseErr := time.Parse(time.RFC3339, resp.ExpireTime); parseErr == nil {
			expiresIn = int(time.Until(t).Seconds())
			if expiresIn < 0 {
				expiresIn = 0
			}
		}
	}

	return &GCPAccessToken{
		AccessToken: resp.AccessToken,
		ExpiresIn:   expiresIn,
		TokenType:   "Bearer",
	}, nil
}

func (g *IAMTokenGenerator) GenerateIDToken(ctx context.Context, serviceAccountEmail string, audience string) (*GCPIDToken, error) {
	req := &iamcredentials.GenerateIdTokenRequest{
		Audience:     audience,
		IncludeEmail: true,
	}
	resp, err := g.service.Projects.ServiceAccounts.GenerateIdToken(saResourceName(serviceAccountEmail), req).Context(ctx).Do()
	if err != nil {
		return nil, fmt.Errorf("IAM generateIdToken for %s: %w", serviceAccountEmail, err)
	}

	return &GCPIDToken{Token: resp.Token}, nil
}

func (g *IAMTokenGenerator) VerifyImpersonation(ctx context.Context, serviceAccountEmail string) error {
	// Attempt to generate a short-lived token to verify the Hub can impersonate this SA.
	req := &iamcredentials.GenerateAccessTokenRequest{
		Scope:    []string{"https://www.googleapis.com/auth/cloud-platform"},
		Lifetime: "300s",
	}
	_, err := g.service.Projects.ServiceAccounts.GenerateAccessToken(saResourceName(serviceAccountEmail), req).Context(ctx).Do()
	if err != nil {
		errStr := err.Error()
		if strings.Contains(errStr, "iam.serviceAccounts.getAccessToken") ||
			strings.Contains(errStr, "Permission") ||
			strings.Contains(errStr, "403") {
			return fmt.Errorf("hub service account cannot impersonate %s: ensure roles/iam.serviceAccountTokenCreator is granted: %w", serviceAccountEmail, err)
		}
		return fmt.Errorf("verifying impersonation for %s: %w", serviceAccountEmail, err)
	}
	return nil
}

func (g *IAMTokenGenerator) ServiceAccountEmail() string {
	return g.hubServiceAccountEmail
}
