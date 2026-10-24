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

package runtimebroker

import (
	"encoding/json"
	"flag"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
)

// updateEnvGatherGolden rewrites testdata/envgather_create_golden.json from
// the current code. The committed file was generated from the create path
// before the shared env-gather evaluator existed (ptone/scion#6101), so the
// test pins that create's env-gather answers are unchanged by it.
var updateEnvGatherGolden = flag.Bool("envgather-golden-update", false, "rewrite the create env-gather golden file")

const envGatherGoldenFile = "testdata/envgather_create_golden.json"

// envGatherGoldenCase is one create request scored by the env-gather
// preflight. hcYAML, when set, is written as the project's harness-configs/
// <hcName>/config.yaml (newTestServerWithHarnessConfig); otherwise the
// project gets the default template and harness-config directories
// (newTestServerWithProjectPath). body names the project as {{PROJECT}}.
type envGatherGoldenCase struct {
	name     string
	hcName   string
	hcYAML   string
	settings string
	body     string
}

// envGatherGoldenResult is the part of a create answer the golden pins:
// the status, and for a needs answer its key lists (sorted: they are built
// from a map), secret info and alternatives.
type envGatherGoldenResult struct {
	Status       int                          `json:"status"`
	Required     []string                     `json:"required,omitempty"`
	HubHas       []string                     `json:"hubHas,omitempty"`
	Needs        []string                     `json:"needs,omitempty"`
	SecretInfo   map[string]api.SecretKeyInfo `json:"secretInfo,omitempty"`
	Alternatives map[string][]string          `json:"alternatives,omitempty"`
}

const goldenSettingsMarkers = `
schema_version: "1"
harness_configs:
  claude:
    harness: claude
    env:
      API_KEY: ""
      SECRET_TOKEN: ""
      TZ: ""
profiles:
  default:
    runtime: mock
`

const goldenSettingsPlain = `
schema_version: "1"
harness_configs:
  claude:
    harness: claude
profiles:
  default:
    runtime: mock
`

func envGatherGoldenCases() []envGatherGoldenCase {
	claudeHC := "harness: claude\nimage: test-image\nuser: scion\n" + claudeAuthBlock
	geminiHC := "harness: gemini\nimage: test-image\nuser: scion\n" + geminiAuthBlock
	return []envGatherGoldenCase{
		{
			name:     "settings markers, one from the hub",
			settings: goldenSettingsMarkers,
			body: `{"name":"g1","id":"g1","gatherEnv":true,"projectPath":"{{PROJECT}}",
				"resolvedEnv":{"API_KEY":"v"},"config":{"template":"claude","profile":"default"}}`,
		},
		{
			name:     "settings markers, all satisfied",
			settings: goldenSettingsMarkers,
			body: `{"name":"g2","id":"g2","gatherEnv":true,"projectPath":"{{PROJECT}}",
				"resolvedEnv":{"API_KEY":"v","SECRET_TOKEN":"v","ANTHROPIC_API_KEY":"v"},"config":{"template":"claude","profile":"default"}}`,
		},
		{
			name:     "settings markers, satisfied by config env and a secret target",
			settings: goldenSettingsMarkers,
			body: `{"name":"g3","id":"g3","gatherEnv":true,"projectPath":"{{PROJECT}}",
				"resolvedSecrets":[{"name":"tok","type":"environment","target":"SECRET_TOKEN","value":"v"}],
				"config":{"template":"claude","profile":"default","env":["API_KEY=v"]}}`,
		},
		{
			name:     "claude auth key needed",
			hcName:   "claude",
			hcYAML:   claudeHC,
			settings: goldenSettingsPlain,
			body: `{"name":"g4","id":"g4","gatherEnv":true,"projectPath":"{{PROJECT}}",
				"config":{"template":"claude","harnessConfig":"claude","profile":"default"}}`,
		},
		{
			name:     "claude vertex-ai needs region and ADC file, with alternatives",
			hcName:   "claude",
			hcYAML:   claudeHC,
			settings: goldenSettingsPlain,
			body: `{"name":"g5","id":"g5","gatherEnv":true,"projectPath":"{{PROJECT}}",
				"resolvedEnv":{"GOOGLE_CLOUD_PROJECT":"p"},
				"config":{"template":"claude","harnessConfig":"claude","profile":"default"}}`,
		},
		{
			name:     "gemini api-key any_of group",
			hcName:   "gemini",
			hcYAML:   geminiHC,
			settings: strings.ReplaceAll(goldenSettingsPlain, "claude", "gemini"),
			body: `{"name":"g6","id":"g6","gatherEnv":true,"projectPath":"{{PROJECT}}",
				"config":{"template":"gemini","harnessConfig":"gemini","profile":"default"}}`,
		},
		{
			name:     "gemini auth-file chosen by harnessAuth",
			hcName:   "gemini",
			hcYAML:   geminiHC,
			settings: strings.ReplaceAll(goldenSettingsPlain, "claude", "gemini"),
			body: `{"name":"g7","id":"g7","gatherEnv":true,"projectPath":"{{PROJECT}}",
				"config":{"template":"gemini","harnessConfig":"gemini","profile":"default","harnessAuth":"auth-file"}}`,
		},
		{
			name:   "harness secrets, profile secrets and profile override marker",
			hcName: "claude",
			hcYAML: claudeHC,
			settings: `
schema_version: "1"
harness_configs:
  claude:
    harness: claude
    secrets:
      - key: HC_SECRET
        description: "harness secret"
        type: file
profiles:
  default:
    runtime: mock
    secrets:
      - key: PROFILE_SECRET
        description: "profile secret"
    harness_overrides:
      claude:
        env:
          OVERRIDE_MARKER: ""
`,
			body: `{"name":"g8","id":"g8","gatherEnv":true,"projectPath":"{{PROJECT}}",
				"resolvedEnv":{"ANTHROPIC_API_KEY":"v"},
				"config":{"template":"claude","harnessConfig":"claude","profile":"default"}}`,
		},
		{
			name:     "request required secrets, one also a settings marker",
			settings: goldenSettingsMarkers,
			body: `{"name":"g9","id":"g9","gatherEnv":true,"projectPath":"{{PROJECT}}",
				"resolvedEnv":{"ANTHROPIC_API_KEY":"v"},
				"requiredSecrets":[{"key":"SECRET_TOKEN","description":"declared"},{"key":"TPL_SECRET","description":"tpl","type":"environment"}],
				"config":{"template":"claude","profile":"default"}}`,
		},
		{
			name:     "available as-needed key drives auth autodetect",
			hcName:   "claude",
			hcYAML:   claudeHC,
			settings: goldenSettingsPlain,
			body: `{"name":"g10","id":"g10","gatherEnv":true,"projectPath":"{{PROJECT}}",
				"availableAsNeededKeys":["GOOGLE_CLOUD_PROJECT"],
				"config":{"template":"claude","harnessConfig":"claude","profile":"default"}}`,
		},
		{
			name:     "noAuth skips the preflight",
			settings: goldenSettingsMarkers,
			body: `{"name":"g11","id":"g11","gatherEnv":true,"noAuth":true,"projectPath":"{{PROJECT}}",
				"config":{"template":"claude","profile":"default"}}`,
		},
		{
			name:     "no gatherEnv skips the preflight",
			settings: goldenSettingsMarkers,
			body: `{"name":"g12","id":"g12","projectPath":"{{PROJECT}}",
				"config":{"template":"claude","profile":"default"}}`,
		},
	}
}

// runEnvGatherGoldenCase posts c as a create and returns the pinned result.
func runEnvGatherGoldenCase(t *testing.T, c envGatherGoldenCase) envGatherGoldenResult {
	t.Helper()
	for _, k := range []string{"ANTHROPIC_API_KEY", "GEMINI_API_KEY", "GOOGLE_API_KEY", "API_KEY", "SECRET_TOKEN"} {
		t.Setenv(k, "")
	}
	unsetHostGCPEnv(t)
	var srv *Server
	var projectDir string
	if c.hcYAML != "" {
		srv, _, projectDir = newTestServerWithHarnessConfig(t, c.hcName, c.hcYAML, c.settings)
	} else {
		srv, _, projectDir = newTestServerWithProjectPath(t, c.settings)
	}
	body := strings.ReplaceAll(c.body, "{{PROJECT}}", projectDir)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agents", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	res := envGatherGoldenResult{Status: w.Code}
	if w.Code != http.StatusAccepted {
		return res
	}
	var envReqs EnvRequirementsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &envReqs); err != nil {
		t.Fatalf("decode 202 body: %v (%s)", err, w.Body.String())
	}
	res.Required = sortedCopy(envReqs.Required)
	res.HubHas = sortedCopy(envReqs.HubHas)
	res.Needs = sortedCopy(envReqs.Needs)
	res.SecretInfo = envReqs.SecretInfo
	res.Alternatives = envReqs.Alternatives
	return res
}

func sortedCopy(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

// TestEnvGather_CreateGolden pins create's env-gather answers (status,
// required, hubHas, needs, secret info, alternatives) on a fixed set of
// requests to the answers the create path gave before the shared evaluator
// (ptone/scion#6101).
func TestEnvGather_CreateGolden(t *testing.T) {
	cases := envGatherGoldenCases()
	got := make(map[string]envGatherGoldenResult, len(cases))
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got[c.name] = runEnvGatherGoldenCase(t, c)
		})
	}
	if *updateEnvGatherGolden {
		data, err := json.MarshalIndent(got, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(envGatherGoldenFile), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(envGatherGoldenFile, append(data, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	data, err := os.ReadFile(envGatherGoldenFile)
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	var want map[string]envGatherGoldenResult
	if err := json.Unmarshal(data, &want); err != nil {
		t.Fatalf("decode golden: %v", err)
	}
	if len(want) != len(cases) {
		t.Fatalf("golden has %d cases, test has %d", len(want), len(cases))
	}
	for name, w := range want {
		if g, ok := got[name]; !ok || !reflect.DeepEqual(g, w) {
			t.Errorf("case %q:\n got  %+v\n want %+v", name, g, w)
		}
	}
}
