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
	"net/url"

	"github.com/GoogleCloudPlatform/scion/pkg/apiclient"
)

// ExperimentService provides read operations for resolved Hub experiment flags.
type ExperimentService interface {
	// Fetch queries GET /api/v1/experiments?name=<name> for a single flag on the
	// Hub's narrow probe allowlist (such as hub.mcp_server).
	Fetch(ctx context.Context, name string) (bool, error)
}

type experimentService struct {
	c *client
}

type experimentsResponse struct {
	Experiments map[string]bool `json:"experiments"`
}

func (s *experimentService) Fetch(ctx context.Context, name string) (bool, error) {
	q := url.Values{}
	q.Set("name", name)
	resp, err := s.c.get(ctx, "/api/v1/experiments?"+q.Encode(), nil)
	if err != nil {
		return false, err
	}
	decoded, err := apiclient.DecodeRequired[experimentsResponse](resp)
	if err != nil {
		return false, err
	}
	if decoded == nil || decoded.Experiments == nil {
		return false, nil
	}
	return decoded.Experiments[name], nil
}
