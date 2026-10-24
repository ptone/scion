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

package config

import (
	"errors"
	"fmt"
	"io"
)

// Limits on one remote template fetch. Package variables so tests can lower
// them.
var (
	// What a fetch may write into the template cache: file bytes, and
	// entries (files and directories).
	remoteFetchMaxBytes   int64 = 64 << 20
	remoteFetchMaxEntries       = 5000

	// What a fetch may unpack into a temporary directory before copying a
	// sub-path into the cache (a whole GitHub repository tarball).
	remoteFetchMaxScratchBytes   int64 = 1 << 30
	remoteFetchMaxScratchEntries       = 100000

	// What a fetch may download (a compressed archive or tarball).
	remoteFetchMaxDownloadBytes int64 = 256 << 20
)

// ErrRemoteTemplateTooLarge is returned when a fetch exceeds a limit. The
// partial result is removed.
var ErrRemoteTemplateTooLarge = errors.New("remote template exceeds the fetch limits")

// fetchBudget counts what one fetch step writes against its limits.
type fetchBudget struct {
	maxBytes   int64
	maxEntries int
	bytes      int64
	entries    int
}

// templateBudget is the budget for what a fetch writes into the cache.
func templateBudget() *fetchBudget {
	return &fetchBudget{maxBytes: remoteFetchMaxBytes, maxEntries: remoteFetchMaxEntries}
}

// scratchBudget is the budget for a temporary unpack before a sub-path copy.
func scratchBudget() *fetchBudget {
	return &fetchBudget{maxBytes: remoteFetchMaxScratchBytes, maxEntries: remoteFetchMaxScratchEntries}
}

// entry counts one file or directory.
func (b *fetchBudget) entry() error {
	b.entries++
	if b.entries > b.maxEntries {
		return fmt.Errorf("%w: more than %d files and directories", ErrRemoteTemplateTooLarge, b.maxEntries)
	}
	return nil
}

// copy copies r to w, counting the bytes. It stops with an error once the
// byte limit is passed, without reading the rest of r.
func (b *fetchBudget) copy(w io.Writer, r io.Reader) error {
	n, err := io.Copy(w, io.LimitReader(r, b.maxBytes-b.bytes+1))
	b.bytes += n
	if err != nil {
		return err
	}
	if b.bytes > b.maxBytes {
		return fmt.Errorf("%w: more than %d bytes", ErrRemoteTemplateTooLarge, b.maxBytes)
	}
	return nil
}

// copyDownload copies a download body r to w, refusing more than
// remoteFetchMaxDownloadBytes.
func copyDownload(w io.Writer, r io.Reader) error {
	n, err := io.Copy(w, io.LimitReader(r, remoteFetchMaxDownloadBytes+1))
	if err != nil {
		return err
	}
	if n > remoteFetchMaxDownloadBytes {
		return fmt.Errorf("%w: download larger than %d bytes", ErrRemoteTemplateTooLarge, remoteFetchMaxDownloadBytes)
	}
	return nil
}
