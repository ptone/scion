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
	"fmt"
	"sort"
	"strings"
)

// generateBootstrapScript produces an inline bash script that exports
// environment variables, downloads the scion and sciontool binaries from a
// GCS bucket, writes the auth token, and starts the heartbeat daemon.
//
// Environment variables are embedded as export statements because the Google
// managed agent API does not support an env_vars field on environments.
//
// The script is intended to be delivered as an inline source in a managed agent
// environment config, so it runs inside the sandbox before the agent's main
// process starts.
func generateBootstrapScript(gcsBucket, binaryVersion string, envVars map[string]string) string {
	var b strings.Builder
	b.WriteString("#!/bin/bash\nset -e\n")

	keys := make([]string, 0, len(envVars))
	for k := range envVars {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, "export %s='%s'\n", k, shellEscape(envVars[k]))
	}

	fmt.Fprintf(&b, `BASE_URL='https://storage.googleapis.com/%s/bin/%s/linux-amd64'
mkdir -p /usr/local/bin
[ -x /usr/local/bin/sciontool ] || curl --fail -sSL --max-time 60 "${BASE_URL}/sciontool" -o /usr/local/bin/sciontool
chmod +x /usr/local/bin/sciontool
[ -x /usr/local/bin/scion ] || curl --fail -sSL --max-time 300 "${BASE_URL}/scion" -o /usr/local/bin/scion
chmod +x /usr/local/bin/scion
mkdir -p ~/.scion
echo "${SCION_TOKEN}" > ~/.scion/scion-token
sciontool heartbeat &
`, gcsBucket, binaryVersion)

	return b.String()
}

// shellEscape escapes single quotes for safe embedding in single-quoted strings.
func shellEscape(s string) string {
	return strings.ReplaceAll(s, "'", "'\"'\"'")
}
