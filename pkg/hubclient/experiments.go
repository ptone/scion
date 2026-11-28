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

	"github.com/GoogleCloudPlatform/scion/pkg/apiclient"
)

type experimentsResponse struct {
	Experiments map[string]bool `json:"experiments"`
}

// FetchServerExperiments queries GET /api/v1/experiments?include=server using
// the client's configured HTTP transport, authenticator, and transport auth.
// It returns the resolved map of web-layer and server-layer experiments.
func FetchServerExperiments(ctx context.Context, cl Client) (map[string]bool, error) {
	c, ok := cl.(*client)
	if !ok {
		return nil, fmt.Errorf("hubclient: FetchServerExperiments requires a default *hubclient.client")
	}
	resp, err := c.get(ctx, "/api/v1/experiments?include=server", nil)
	if err != nil {
		return nil, err
	}
	decoded, err := apiclient.DecodeRequired[experimentsResponse](resp)
	if err != nil {
		return nil, err
	}
	if decoded == nil || decoded.Experiments == nil {
		return map[string]bool{}, nil
	}
	return decoded.Experiments, nil
}
