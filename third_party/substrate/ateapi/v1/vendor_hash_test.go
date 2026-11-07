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

package ateapi_test

import (
	"crypto/sha256"
	"encoding/hex"
	"maps"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// vendoredHashes are the sha256 values of the upstream files at
// agent-substrate/substrate v1.0.0 (b34fbee742906494b5eb4f1f36987176bf8770a4),
// hard-coded so that editing README.md alone cannot make a changed file pass.
// README.md must record exactly these values.
var vendoredHashes = map[string]string{
	"LICENSE":           "cfc7749b96f63bd31c3c42b5c471bf756814053e847c10f3eb003417bc523d30",
	"ateapi.pb.go":      "8f5f95d75568cfc485cafd6e1813a897233e3ca8b01258a1b55fa5d30e4d7a91",
	"ateapi.proto":      "b5bb348f04b8f8d9f24c4c880e1f4176771e1556cfd0cff95a97ca8bee079722",
	"ateapi_grpc.pb.go": "ac1d3804745755ddd1cd39a8093a43a5b125de1a21d73cc825387519e682b484",
	"source.go":         "f63529efec392b64a1adb99bf08aa0cd5d431c815db2d420f2329cd2e1f2e536",
}

// readmeHashRow matches one row of the README "Vendored files" table:
// | `file` | `sha256` |
var readmeHashRow = regexp.MustCompile("^\\| `([^`]+)` \\| `([0-9a-f]{64})` \\|$")

func readmeHashes(t *testing.T) map[string]string {
	t.Helper()
	raw, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatalf("read README.md: %v", err)
	}
	hashes := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		m := readmeHashRow.FindStringSubmatch(strings.TrimSuffix(line, "\r"))
		if m == nil {
			continue
		}
		if _, dup := hashes[m[1]]; dup {
			t.Fatalf("README.md records %s more than once", m[1])
		}
		hashes[m[1]] = m[2]
	}
	return hashes
}

// TestVendoredFilesMatchREADMEHashes guards the vendored copy against hand
// edits and stale refreshes: every vendored file must hash to the upstream
// value hard-coded in vendoredHashes, and README.md must record exactly the
// same values.
func TestVendoredFilesMatchREADMEHashes(t *testing.T) {
	readme := readmeHashes(t)
	if !maps.Equal(readme, vendoredHashes) {
		t.Errorf("README.md records %v, want exactly %v", readme, vendoredHashes)
	}

	names := slices.Sorted(maps.Keys(vendoredHashes))
	for _, name := range names {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Errorf("read %s: %v", name, err)
			continue
		}
		sum := sha256.Sum256(data)
		if got := hex.EncodeToString(sum[:]); got != vendoredHashes[name] {
			t.Errorf("%s sha256 = %s, want upstream %s; vendored files must be byte-identical to upstream (see README.md \"Refresh procedure\")", name, got, vendoredHashes[name])
		}
	}
}
