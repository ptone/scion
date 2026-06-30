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

import "fmt"

// generateBootstrapScript produces an inline bash script that downloads the
// scion and sciontool binaries from a GCS bucket, writes the auth token from
// the SCION_TOKEN env var, and starts the heartbeat daemon in the background.
//
// The script is intended to be delivered as an inline source in a managed agent
// environment config, so it runs inside the sandbox before the agent's main
// process starts.
func generateBootstrapScript(gcsBucket, binaryVersion string) string {
	return fmt.Sprintf(`#!/bin/bash
set -e
BASE_URL="https://storage.googleapis.com/%s/bin/%s/linux-amd64"
mkdir -p /usr/local/bin
curl -sSL "${BASE_URL}/scion" -o /usr/local/bin/scion && chmod +x /usr/local/bin/scion
curl -sSL "${BASE_URL}/sciontool" -o /usr/local/bin/sciontool && chmod +x /usr/local/bin/sciontool
mkdir -p ~/.scion
echo "${SCION_TOKEN}" > ~/.scion/scion-token
sciontool heartbeat --daemon &
`, gcsBucket, binaryVersion)
}
