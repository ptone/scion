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

package harnesses_test

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/secret"
)

// captureTargetArg matches a "--target" argument in a sciontool command
// list: either a string literal or a Python name.
var captureTargetArg = regexp.MustCompile(`"--target",\s*([A-Za-z_][A-Za-z0-9_]*|"[^"]*")`)

// TestCaptureScriptTargetsAllowedByHubPolicy pins every --target a harness
// capture_auth.py passes to sciontool against the hub's policy for file
// secrets written from an agent. Each target must be a string literal (or a
// name assigned one in the same file) that AgentFileTargetAllowed accepts;
// a target built at run time, such as os.path.join($HOME, ...), fails here
// because the hub refuses the absolute path it produces.
func TestCaptureScriptTargetsAllowedByHubPolicy(t *testing.T) {
	files, err := filepath.Glob("*/capture_auth.py")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no harness capture_auth.py found")
	}
	checked := 0
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range captureTargetArg.FindAllStringSubmatch(string(data), -1) {
			target := m[1]
			if target[0] == '"' {
				target = target[1 : len(target)-1]
			} else {
				assign := regexp.MustCompile(fmt.Sprintf(`(?m)^\s*%s\s*=\s*"([^"]*)"\s*$`, regexp.QuoteMeta(m[1])))
				am := assign.FindStringSubmatch(string(data))
				if am == nil {
					t.Errorf("%s: --target %s is not a name assigned a string literal in the file", f, m[1])
					continue
				}
				target = am[1]
			}
			if err := secret.AgentFileTargetAllowed(target); err != nil {
				t.Errorf("%s: --target %q is refused by the hub policy: %v", f, target, err)
			}
			checked++
		}
	}
	if checked == 0 {
		t.Fatal("no --target found in any harness capture_auth.py")
	}
}
