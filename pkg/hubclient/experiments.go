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
	"fmt"
	"net/url"

	"github.com/GoogleCloudPlatform/scion/pkg/apiclient"
)

type experimentsResponse struct {
	Experiments map[string]bool `json:"experiments"`
}

// FetchExperiment queries GET /api/v1/experiments?name=<name> using the
// client's configured HTTP transport, authenticator, and transport auth.
// Only flags on the Hub's narrow probe allowlist (such as hub.mcp_server) are
// accepted.
func FetchExperiment(ctx context.Context, cl Client, name string) (bool, error) {
	c, ok := cl.(*client)
	if !ok {
		return false, fmt.Errorf("hubclient: FetchExperiment requires a default *hubclient.client")
	}
	q := url.Values{}
	q.Set("name", name)
	resp, err := c.get(ctx, "/api/v1/experiments?"+q.Encode(), nil)
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
