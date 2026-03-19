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

package api

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValidateSharedDirs(t *testing.T) {
	tests := []struct {
		name    string
		dirs    []SharedDir
		wantErr string
	}{
		{
			name: "valid dirs",
			dirs: []SharedDir{
				{Name: "build-cache"},
				{Name: "artifacts", ReadOnly: true},
				{Name: "a"},
			},
		},
		{
			name:    "empty slice is valid",
			dirs:    nil,
			wantErr: "",
		},
		{
			name:    "missing name",
			dirs:    []SharedDir{{Name: ""}},
			wantErr: "missing required field: name",
		},
		{
			name:    "invalid name - uppercase",
			dirs:    []SharedDir{{Name: "BuildCache"}},
			wantErr: "invalid name",
		},
		{
			name:    "invalid name - spaces",
			dirs:    []SharedDir{{Name: "build cache"}},
			wantErr: "invalid name",
		},
		{
			name:    "invalid name - starts with hyphen",
			dirs:    []SharedDir{{Name: "-cache"}},
			wantErr: "invalid name",
		},
		{
			name:    "invalid name - ends with hyphen",
			dirs:    []SharedDir{{Name: "cache-"}},
			wantErr: "invalid name",
		},
		{
			name:    "invalid name - special characters",
			dirs:    []SharedDir{{Name: "build_cache"}},
			wantErr: "invalid name",
		},
		{
			name: "duplicate names",
			dirs: []SharedDir{
				{Name: "cache"},
				{Name: "cache"},
			},
			wantErr: "duplicate name",
		},
		{
			name: "single character valid",
			dirs: []SharedDir{{Name: "a"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateSharedDirs(tt.dirs)
			if tt.wantErr != "" {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestIsValidSlug(t *testing.T) {
	valid := []string{"a", "abc", "build-cache", "a1", "1a", "abc123"}
	invalid := []string{"", "-", "a-", "-a", "A", "abc_def", "abc def", "abc.def"}

	for _, s := range valid {
		assert.True(t, isValidSlug(s), "expected %q to be a valid slug", s)
	}
	for _, s := range invalid {
		assert.False(t, isValidSlug(s), "expected %q to be an invalid slug", s)
	}
}
