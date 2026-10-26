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

package transfer

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync/atomic"
	"unicode/utf8"
)

// NormalizeFileContent normalizes line endings from CRLF to LF for text files.
// Binary content (detected by the presence of null bytes or invalid UTF-8 in
// the first 8KB, extended to a rune boundary) is returned unchanged.
func NormalizeFileContent(data []byte) []byte {
	if !isTextContent(data) {
		return data
	}
	if !bytes.Contains(data, []byte("\r\n")) {
		return data
	}
	return bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
}

// isTextContent returns true if data appears to be text (UTF-8, no null bytes).
// It inspects the first 8KB, plus up to three bytes to finish a UTF-8 rune.
func isTextContent(data []byte) bool {
	sample := data
	if len(sample) > 8192 {
		end := 8192
		// Do not reject valid text just because the sample splits a rune.
		for end < len(data) && end < 8192+utf8.UTFMax-1 && !utf8.RuneStart(data[end]) {
			end++
		}
		sample = data[:end]
	}
	if bytes.ContainsRune(sample, 0) {
		return false
	}
	return utf8.Valid(sample)
}

// binaryExtensions are file extensions that should never be normalized.
var binaryExtensions = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true,
	".ico": true, ".bmp": true, ".webp": true, ".svg": true,
	".woff": true, ".woff2": true, ".ttf": true, ".otf": true, ".eot": true,
	".zip": true, ".tar": true, ".gz": true, ".bz2": true, ".xz": true,
	".pdf": true, ".exe": true, ".dll": true, ".so": true, ".dylib": true,
	".bin": true, ".dat": true, ".db": true, ".sqlite": true,
}

// NormalizeDir normalizes line endings (CRLF to LF) in place in the regular
// text files under dir (NormalizeRoot). dir must be a directory and not a
// link; a link or another non-directory is left alone, as the walk by path
// that this replaces did. The directory is opened with a trailing path
// separator, so a FIFO swapped in for it fails instead of blocking, and it
// must be the directory the Lstat saw.
func NormalizeDir(dir string) error {
	before, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !before.IsDir() {
		return nil
	}
	root, err := os.OpenRoot(dir + string(filepath.Separator))
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	at, err := root.Stat(".")
	if err != nil {
		return err
	}
	if !os.SameFile(before, at) {
		return fmt.Errorf("%s: %w", dir, ErrDirReplaced)
	}
	return NormalizeRoot(root)
}

// normalizeBeforeRead, when set (tests only), runs in NormalizeRoot after a
// file's entry was found to be a regular file and before it is opened.
var normalizeBeforeRead atomic.Pointer[func(rel string)]

// NormalizeRoot walks root (walkRoot) and normalizes line endings (CRLF to
// LF) in place in its regular text files. Binary files (by extension or
// content) are left unchanged. Every read and write goes through root:
//   - a file is read with OpenRegularFile, so a link, FIFO or other
//     non-regular file swapped in after the walk saw a regular file is
//     skipped, never followed or blocked on; a file replaced by another
//     regular file between the check and the open fails the walk;
//   - a changed file is written to a new temporary file in the same
//     directory (O_EXCL, a random name, so no existing file or link is
//     followed or overwritten), given the file's mode, and renamed over the
//     file.
func NormalizeRoot(root *os.Root) error {
	return walkRoot(root, func(rel string, d fs.DirEntry) error {
		// Skip symlinks and special files before reading: FIFOs can block,
		// while sockets and devices do not represent normalizable text files.
		if !d.Type().IsRegular() {
			return nil
		}
		if binaryExtensions[strings.ToLower(path.Ext(rel))] {
			return nil
		}
		return normalizeRootFile(root, filepath.FromSlash(rel))
	})
}

// normalizeRootFile normalizes the regular file rel of root in place.
func normalizeRootFile(root *os.Root, rel string) error {
	if hook := normalizeBeforeRead.Load(); hook != nil {
		(*hook)(filepath.ToSlash(rel))
	}
	f, info, err := OpenRegularFile(root, rel)
	if errors.Is(err, ErrNotRegularFile) {
		return nil
	}
	if err != nil {
		return err
	}
	data, err := io.ReadAll(f)
	_ = f.Close()
	if err != nil {
		return err
	}
	normalized := NormalizeFileContent(data)
	if len(normalized) == len(data) && bytes.Equal(normalized, data) {
		return nil
	}
	// A fixed path + ".tmp" could overwrite an existing file or follow a
	// symlink, so the temporary file gets a random name and O_EXCL.
	var tmp *os.File
	var tmpRel string
	for try := 0; ; try++ {
		var rnd [8]byte
		if _, err := rand.Read(rnd[:]); err != nil {
			return err
		}
		tmpRel = filepath.Join(filepath.Dir(rel), ".scion-normalize.tmp-"+hex.EncodeToString(rnd[:]))
		tmp, err = root.OpenFile(tmpRel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			break
		}
		if !errors.Is(err, fs.ErrExist) || try >= 9 {
			return err
		}
	}
	renamed := false
	defer func() {
		_ = tmp.Close()
		if !renamed {
			_ = root.Remove(tmpRel)
		}
	}()
	if _, err := tmp.Write(normalized); err != nil {
		return err
	}
	if err := tmp.Chmod(info.Mode()); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := root.Rename(tmpRel, rel); err != nil {
		return err
	}
	renamed = true
	return nil
}
