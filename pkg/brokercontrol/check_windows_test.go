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

//go:build windows

package brokercontrol

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestCheck_WindowsVolumeRoot(t *testing.T) {
	f := newFixture(t)
	_, err := Check(filepath.VolumeName(f.home)+`\`, VolumeAllowed)
	wantRefused(t, err, RuleAncestor)
}

func TestCheck_WindowsCaseVariant(t *testing.T) {
	f := newFixture(t)
	// The real platform default (case-insensitive) applies here.
	_, err := Check(strings.ToUpper(filepath.Join(f.global, "settings.yaml")), VolumeAllowed)
	wantRefused(t, err, "")
}

func TestCheck_WindowsRelativeDriveForm(t *testing.T) {
	newFixture(t)
	// "C:foo" is drive-relative, not absolute.
	_, err := Check("C:foo", VolumeAllowed)
	wantRefused(t, err, RuleShape)
}
