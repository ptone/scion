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

package transportauth

import (
	"fmt"
	"os"
)

// ADCSourceConstructor is a function that creates a TokenSource from ADC.
// It is injected by the caller (e.g. from the adcsource subpackage) to avoid
// linking the Google API client libraries into the lean sciontool binary.
type ADCSourceConstructor func(audience string) (TokenSource, error)

// ResolveBrokerTransport resolves a transport TokenSource for broker use.
// It tries MetadataSource first (on-GCE), then falls back to ADC if an
// ADCSourceConstructor is provided.
//
// The audience is read from SCION_TRANSPORT_AUDIENCE or SCION_HUB_OIDC_AUDIENCE.
// Returns (nil, nil) when transport auth is not configured.
func ResolveBrokerTransport(adcNew ADCSourceConstructor) (TokenSource, error) {
	audience := os.Getenv(EnvTransportAudience)
	if audience == "" {
		audience = os.Getenv(EnvHubOIDCAudience)
	}
	if audience == "" {
		return nil, nil
	}

	return ResolveBrokerTransportWithAudience(audience, adcNew)
}

// ResolveBrokerTransportWithAudience resolves a transport TokenSource for
// broker use with an explicit audience. It tries MetadataSource first
// (on-GCE), then falls back to ADC.
func ResolveBrokerTransportWithAudience(audience string, adcNew ADCSourceConstructor) (TokenSource, error) {
	if audience == "" {
		return nil, nil
	}

	// On GCE with no metadata-mode override, prefer the metadata server.
	if IsOnGCEFunc() {
		if mode := os.Getenv(EnvMetadataMode); mode == "" {
			return NewMetadataSource(audience), nil
		}
	}

	// Fall back to ADC if available.
	if adcNew != nil {
		src, err := adcNew(audience)
		if err != nil {
			return nil, fmt.Errorf("broker transport: %w", err)
		}
		return src, nil
	}

	return nil, nil
}
