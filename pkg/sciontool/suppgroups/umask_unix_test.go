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

//go:build unix

package suppgroups

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// withUmaskRecorder stubs the umask call so tests never change the test
// process's own umask, and records every value it was asked to set.
func withUmaskRecorder(t *testing.T) *[]int {
	t.Helper()
	var calls []int
	prev := umask
	umask = func(mask int) int { calls = append(calls, mask); return 0o022 }
	t.Cleanup(func() { umask = prev })
	return &calls
}

func TestApplySharedDirUmask(t *testing.T) {
	tests := []struct {
		name    string
		env     string
		granted []int
		applied bool
	}{
		{"granted group applies 002", "1500", []int{0, 1500}, true},
		{"env unset leaves umask unchanged", "", []int{0, 1500}, false},
		{"invalid env leaves umask unchanged", "x,-1,0", []int{0, 1500}, false},
		{"ungranted gid leaves umask unchanged", "2000", []int{0, 1500}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(EnvVar, tt.env)
			withGranted(t, tt.granted, nil)
			calls := withUmaskRecorder(t)

			applied, previous := ApplySharedDirUmask()
			assert.Equal(t, tt.applied, applied)
			assert.Equal(t, tt.applied, ShouldApplyUmask())
			if tt.applied {
				assert.Equal(t, []int{0o002}, *calls)
				assert.Equal(t, 0o022, previous)
			} else {
				assert.Empty(t, *calls, "umask must not be touched")
			}
		})
	}
}
