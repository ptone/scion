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
	"context"
	"crypto/sha1"
	"encoding/xml"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/webdav"
)

func TestChecksumFile_DeadProps(t *testing.T) {
	dir := t.TempDir()
	content := []byte("hello, world\n")
	relPath := "test.txt"
	require.NoError(t, os.WriteFile(filepath.Join(dir, relPath), content, 0644))

	h := sha1.New()
	h.Write(content)
	expectedHash := fmt.Sprintf("%x", h.Sum(nil))

	davFS := webdav.Dir(dir)
	f, err := davFS.OpenFile(context.Background(), relPath, os.O_RDONLY, 0)
	require.NoError(t, err)
	defer f.Close()

	cf := &checksumFile{File: f, rootPath: dir, relPath: relPath}

	props, err := cf.DeadProps()
	require.NoError(t, err)

	name := xml.Name{Space: "http://owncloud.org/ns", Local: "checksums"}
	prop, ok := props[name]
	require.True(t, ok, "expected oc:checksums property in DeadProps result")
	assert.Equal(t, name, prop.XMLName)
	assert.Contains(t, string(prop.InnerXML), "SHA1:"+expectedHash)
}

func TestChecksumFile_DeadProps_LeadingSlash(t *testing.T) {
	// Verify that relPath with a leading slash (as webdav provides) is handled correctly
	dir := t.TempDir()
	content := []byte("data")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "file.go"), content, 0644))

	davFS := webdav.Dir(dir)
	f, err := davFS.OpenFile(context.Background(), "/file.go", os.O_RDONLY, 0)
	require.NoError(t, err)
	defer f.Close()

	cf := &checksumFile{File: f, rootPath: dir, relPath: "/file.go"}

	props, err := cf.DeadProps()
	require.NoError(t, err)

	name := xml.Name{Space: "http://owncloud.org/ns", Local: "checksums"}
	_, ok := props[name]
	assert.True(t, ok, "expected oc:checksums property even with leading slash in relPath")
}

func TestChecksumFile_Patch(t *testing.T) {
	cf := &checksumFile{}
	result, err := cf.Patch([]webdav.Proppatch{})
	require.NoError(t, err)
	require.Len(t, result, 1)
	assert.Equal(t, http.StatusForbidden, result[0].Status)
	require.Len(t, result[0].Props, 1)
	assert.Equal(t, xml.Name{Space: "http://owncloud.org/ns", Local: "checksums"}, result[0].Props[0].XMLName)
}

func TestIsExcluded(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		excluded bool
	}{
		// Directory exclusions
		{"git dir", ".git", true},
		{"git subpath", ".git/objects/pack", true},
		{"scion dir", ".scion", true},
		{"scion subpath", ".scion/config.yaml", true},
		{"node_modules", "node_modules", true},
		{"node_modules subpath", "node_modules/lodash/index.js", true},

		// Extension exclusions
		{"env file", ".env", true},
		{"env file in subdir", "config/.env", true},
		{"dotenv production", "config/production.env", true},

		// Should NOT be excluded
		{"regular file", "main.go", false},
		{"nested file", "src/app/main.go", false},
		{"gitignore", ".gitignore", false},
		{"env-like name", "environment.go", false},
		{"root", "/", false},
		{"empty", "", false},
		{"dot", ".", false},

		// Leading slash normalization
		{"leading slash git", "/.git", true},
		{"leading slash file", "/main.go", false},
		{"leading slash scion", "/.scion/config", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isExcluded(tt.path)
			assert.Equal(t, tt.excluded, got, "isExcluded(%q)", tt.path)
		})
	}
}
