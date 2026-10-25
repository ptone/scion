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
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"hash/crc32"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rclone/rclone/fs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setFetchLimitsForTest lowers the fetch limits for one test.
func setFetchLimitsForTest(t *testing.T, maxBytes int64, maxEntries int, scratchBytes int64, scratchEntries int, download int64) {
	t.Helper()
	pb, pe, psb, pse, pd := remoteFetchMaxBytes, remoteFetchMaxEntries, remoteFetchMaxScratchBytes, remoteFetchMaxScratchEntries, remoteFetchMaxDownloadBytes
	t.Cleanup(func() {
		remoteFetchMaxBytes, remoteFetchMaxEntries, remoteFetchMaxScratchBytes, remoteFetchMaxScratchEntries, remoteFetchMaxDownloadBytes = pb, pe, psb, pse, pd
	})
	remoteFetchMaxBytes, remoteFetchMaxEntries = maxBytes, maxEntries
	remoteFetchMaxScratchBytes, remoteFetchMaxScratchEntries = scratchBytes, scratchEntries
	remoteFetchMaxDownloadBytes = download
}

// limitTarEntries returns n regular files of size bytes each under root/.
func limitTarEntries(n, size int) []tarEntry {
	entries := []tarEntry{{typ: tar.TypeDir, name: "root/", mode: 0o755}}
	for i := 0; i < n; i++ {
		entries = append(entries, tarEntry{typ: tar.TypeReg, name: fmt.Sprintf("root/f%03d.txt", i), body: strings.Repeat("x", size)})
	}
	return entries
}

// dirFileCount counts regular files under dir.
func dirFileCount(t *testing.T, dir string) int {
	t.Helper()
	n := 0
	_ = filepath.Walk(dir, func(_ string, info os.FileInfo, err error) error {
		if err == nil && info.Mode().IsRegular() {
			n++
		}
		return nil
	})
	return n
}

// countingReader counts how much of the source was read.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

func TestFetchBudget(t *testing.T) {
	b := &fetchBudget{maxBytes: 10, maxEntries: 2}
	require.NoError(t, b.entry())
	require.NoError(t, b.entry())
	assert.ErrorIs(t, b.entry(), ErrRemoteTemplateTooLarge)

	b = &fetchBudget{maxBytes: 10, maxEntries: 100}
	var out bytes.Buffer
	require.NoError(t, b.copy(&out, strings.NewReader("12345")))
	require.NoError(t, b.copy(&out, strings.NewReader("67890")), "exactly at the limit is accepted")
	src := &countingReader{r: strings.NewReader(strings.Repeat("y", 1<<20))}
	assert.ErrorIs(t, b.copy(&out, src), ErrRemoteTemplateTooLarge)
	assert.LessOrEqual(t, src.n, int64(1), "reading stops right after the limit")
}

func TestCopyDownload_Limit(t *testing.T) {
	setFetchLimitsForTest(t, 1<<20, 100, 1<<20, 100, 10)
	var out bytes.Buffer
	require.NoError(t, copyDownload(&out, strings.NewReader("0123456789")))
	out.Reset()
	src := &countingReader{r: strings.NewReader(strings.Repeat("z", 1<<20))}
	assert.ErrorIs(t, copyDownload(&out, src), ErrRemoteTemplateTooLarge)
	assert.LessOrEqual(t, src.n, int64(11))
}

func TestExtract_Limits(t *testing.T) {
	tgz := writeArchive(t, buildTarGz(t, limitTarEntries(5, 10)), "a.tar.gz")
	var zipEntries []zipEntry
	for i := 0; i < 5; i++ {
		zipEntries = append(zipEntries, zipEntry{name: fmt.Sprintf("root/f%d.txt", i), body: strings.Repeat("x", 10), mode: 0o644})
	}
	zipPath := writeArchive(t, buildZip(t, zipEntries), "a.zip")
	extract := map[string]func(dest string, b *fetchBudget) error{
		"tar": func(dest string, b *fetchBudget) error { return extractTarGz(tgz, dest, b) },
		"zip": func(dest string, b *fetchBudget) error { return extractZip(zipPath, dest, b) },
	}
	for name, fn := range extract {
		t.Run(name, func(t *testing.T) {
			require.NoError(t, fn(t.TempDir(), &fetchBudget{maxBytes: 50, maxEntries: 5}), "exactly at the limits")
			assert.ErrorIs(t, fn(t.TempDir(), &fetchBudget{maxBytes: 49, maxEntries: 100}), ErrRemoteTemplateTooLarge, "bytes")
			assert.ErrorIs(t, fn(t.TempDir(), &fetchBudget{maxBytes: 1 << 20, maxEntries: 4}), ErrRemoteTemplateTooLarge, "entries")
		})
	}
}

// A tar that declares a small size per entry but holds many entries is
// refused while scanning, before anything is written.
func TestExtractTarGz_ScanRefusesTooManyEntries(t *testing.T) {
	tgz := writeArchive(t, buildTarGz(t, limitTarEntries(50, 0)), "many.tar.gz")
	dest := t.TempDir()
	assert.ErrorIs(t, extractTarGz(tgz, dest, &fetchBudget{maxBytes: 1 << 20, maxEntries: 10}), ErrRemoteTemplateTooLarge)
	assert.Zero(t, dirFileCount(t, dest))
}

// The tar scan allows one entry more than the limit (a common root
// directory is stripped, not written), but the write path enforces the
// limit on its own: an archive WITHOUT a common root holding maxEntries+1
// entries is refused.
func TestExtractTarGz_RootlessArchiveAtLimitPlusOne(t *testing.T) {
	var entries []tarEntry
	for i := 0; i < 6; i++ {
		entries = append(entries, tarEntry{typ: tar.TypeReg, name: fmt.Sprintf("f%d.txt", i), body: "x"})
	}
	tgz := writeArchive(t, buildTarGz(t, entries), "rootless.tar.gz")
	require.NoError(t, extractTarGz(tgz, t.TempDir(), &fetchBudget{maxBytes: 1 << 20, maxEntries: 6}))
	dest := t.TempDir()
	err := extractTarGz(tgz, dest, &fetchBudget{maxBytes: 1 << 20, maxEntries: 5})
	assert.ErrorIs(t, err, ErrRemoteTemplateTooLarge)
	assert.LessOrEqual(t, dirFileCount(t, dest), 5, "never more than the limit is written")
}

func TestCopyDirExcludingGit_Limits(t *testing.T) {
	src := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(src, "tpl", "sub"), 0o755))
	for i := 0; i < 4; i++ {
		require.NoError(t, os.WriteFile(filepath.Join(src, "tpl", "sub", fmt.Sprintf("f%d", i)), []byte("0123456789"), 0o644))
	}
	require.NoError(t, copyDirExcludingGit(src, "tpl", t.TempDir(), &fetchBudget{maxBytes: 40, maxEntries: 5}), "exactly at the limits (1 dir + 4 files)")
	assert.ErrorIs(t, copyDirExcludingGit(src, "tpl", t.TempDir(), &fetchBudget{maxBytes: 39, maxEntries: 100}), ErrRemoteTemplateTooLarge)
	assert.ErrorIs(t, copyDirExcludingGit(src, "tpl", t.TempDir(), &fetchBudget{maxBytes: 1 << 20, maxEntries: 4}), ErrRemoteTemplateTooLarge)
}

func TestCheckRegularTree_Limits(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 3; i++ {
		require.NoError(t, os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%d", i)), []byte("0123456789"), 0o644))
	}
	setFetchLimitsForTest(t, 30, 3, 1<<20, 100, 1<<20)
	require.NoError(t, checkRegularTree(dir))
	setFetchLimitsForTest(t, 29, 3, 1<<20, 100, 1<<20)
	assert.ErrorIs(t, checkRegularTree(dir), ErrRemoteTemplateTooLarge)
	setFetchLimitsForTest(t, 1<<20, 2, 1<<20, 100, 1<<20)
	assert.ErrorIs(t, checkRegularTree(dir), ErrRemoteTemplateTooLarge)
}

// GitHub tarball: the download, the whole-repository unpack (scratch
// budget) and the sub-path copy (template budget) are each limited.
func TestFetchGitHubTarball_Limits(t *testing.T) {
	files := map[string]string{"other/big.bin": strings.Repeat("b", 4000)}
	for i := 0; i < 3; i++ {
		files[fmt.Sprintf("templates/foo/f%d", i)] = "0123456789"
	}
	body := githubTarballFixture(t, "repo-main", files)
	parts := &GitHubURLParts{Owner: "acme", Repo: "repo", Branch: "main", Path: "templates/foo"}

	serveTarball(t, body)
	setFetchLimitsForTest(t, 1<<20, 100, 1<<20, 100, 1<<20)
	dest := t.TempDir()
	require.NoError(t, fetchGitHubTarball(context.Background(), parts, dest, ""))
	assert.Equal(t, 3, dirFileCount(t, dest))

	for _, tc := range []struct {
		name           string
		maxBytes       int64
		maxEntries     int
		scratchBytes   int64
		scratchEntries int
		download       int64
	}{
		{"download", 1 << 20, 100, 1 << 20, 100, int64(len(body)) - 1},
		{"scratch bytes", 1 << 20, 100, 3000, 100, 1 << 20},
		{"scratch entries", 1 << 20, 100, 1 << 20, 4, 1 << 20},
		// The template budget only sees the sub-path: 3 files, 30 bytes.
		{"template bytes", 29, 100, 1 << 20, 100, 1 << 20},
		{"template entries", 1 << 20, 2, 1 << 20, 100, 1 << 20},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setFetchLimitsForTest(t, tc.maxBytes, tc.maxEntries, tc.scratchBytes, tc.scratchEntries, tc.download)
			err := fetchGitHubTarball(context.Background(), parts, t.TempDir(), "")
			assert.ErrorIs(t, err, ErrRemoteTemplateTooLarge)
		})
	}
}

// archiveLimitServer serves body for any .tgz path and lets it through the
// archive source checks.
func archiveLimitServer(t *testing.T, body []byte) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(AllowArchiveServerForTest(srv.URL))
	return srv.URL
}

// Exceeding a limit through FetchRemoteTemplate aborts the fetch and leaves
// no cache directory behind (archive download, archive unpack).
func TestFetchRemoteTemplate_LimitRemovesPartialCache(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	body := buildTarGz(t, limitTarEntries(5, 10))
	base := archiveLimitServer(t, body)
	uri := base + "/tpl.tgz"
	cacheDir, err := remoteCacheDir()
	require.NoError(t, err)
	cachePath := filepath.Join(cacheDir, generateCacheKey(uri))

	setFetchLimitsForTest(t, 1<<20, 100, 1<<20, 100, 1<<20)
	got, err := FetchRemoteTemplate(context.Background(), uri, RemoteFetchOptions{})
	require.NoError(t, err)
	assert.Equal(t, 5, dirFileCount(t, got))

	for _, tc := range []struct {
		name       string
		maxBytes   int64
		maxEntries int
		download   int64
	}{
		{"download", 1 << 20, 100, int64(len(body)) - 1},
		{"bytes", 49, 100, 1 << 20},
		{"entries", 1 << 20, 4, 1 << 20},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setFetchLimitsForTest(t, tc.maxBytes, tc.maxEntries, 1<<20, 100, tc.download)
			_, err := FetchRemoteTemplate(context.Background(), uri, RemoteFetchOptions{})
			require.ErrorIs(t, err, ErrRemoteTemplateTooLarge)
			_, statErr := os.Stat(cachePath)
			assert.True(t, os.IsNotExist(statErr), "the partial cache is removed")
		})
	}
}

// localRcloneSource returns a local rclone Fs holding n files of size bytes.
func localRcloneSource(t *testing.T, n, size int) fs.Fs {
	t.Helper()
	dir := t.TempDir()
	for i := 0; i < n; i++ {
		require.NoError(t, os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%d", i)), bytes.Repeat([]byte("r"), size), 0o644))
	}
	f, err := fs.NewFs(context.Background(), dir)
	require.NoError(t, err)
	return f
}

// The rclone copy (rcloneCopyLimited, the step execRcloneFetch runs after
// opening the source) refuses a source over the limits from its listing,
// before copying anything.
func TestRcloneCopyLimited_ListingLimits(t *testing.T) {
	src := localRcloneSource(t, 4, 10)
	setFetchLimitsForTest(t, 40, 4, 1<<20, 100, 1<<20)
	dest := t.TempDir()
	require.NoError(t, rcloneCopyLimited(context.Background(), src, dest))
	assert.Equal(t, 4, dirFileCount(t, dest))

	for _, tc := range []struct {
		name       string
		maxBytes   int64
		maxEntries int
	}{{"bytes", 39, 100}, {"entries", 1 << 20, 3}} {
		t.Run(tc.name, func(t *testing.T) {
			setFetchLimitsForTest(t, tc.maxBytes, tc.maxEntries, 1<<20, 100, 1<<20)
			dest := t.TempDir()
			assert.ErrorIs(t, rcloneCopyLimited(context.Background(), src, dest), ErrRemoteTemplateTooLarge)
			assert.Zero(t, dirFileCount(t, dest), "nothing is copied")
		})
	}
}

// listForTest lists src with the limits raised, for tests of the copy step.
func listForTest(t *testing.T, src fs.Fs) []fs.Object {
	t.Helper()
	pb, pe := remoteFetchMaxBytes, remoteFetchMaxEntries
	remoteFetchMaxBytes, remoteFetchMaxEntries = 1<<30, 1<<20
	defer func() { remoteFetchMaxBytes, remoteFetchMaxEntries = pb, pe }()
	objs, err := listRcloneSource(context.Background(), src)
	require.NoError(t, err)
	return objs
}

// The rclone copy itself runs with a hard cutoff at the byte limit (the
// guard for an object that grows after the listing), counted per fetch: a
// run of fetches each under the limit all succeed.
func TestRcloneCopyObjects_HardCutoffPerFetch(t *testing.T) {
	big := listForTest(t, localRcloneSource(t, 1, 5000))
	small := listForTest(t, localRcloneSource(t, 1, 600))
	setFetchLimitsForTest(t, 1000, 100, 1<<20, 100, 1<<20)
	assert.ErrorIs(t, rcloneCopyObjects(context.Background(), big, t.TempDir()), ErrRemoteTemplateTooLarge)

	for i := 0; i < 3; i++ {
		dest := t.TempDir()
		require.NoError(t, rcloneCopyObjects(context.Background(), small, dest), "fetch %d", i)
		assert.Equal(t, 1, dirFileCount(t, dest))
	}
}

// Only the listed objects are copied: an object added to the source after
// the listing is not fetched, so the entry count cannot grow past the
// checked listing.
func TestRcloneCopyObjects_OnlyListedObjects(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a"), []byte("a"), 0o644))
	src, err := fs.NewFs(context.Background(), dir)
	require.NoError(t, err)
	objs := listForTest(t, src)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "late"), []byte("late"), 0o644))

	dest := t.TempDir()
	require.NoError(t, rcloneCopyObjects(context.Background(), objs, dest))
	assert.FileExists(t, filepath.Join(dest, "a"))
	assert.NoFileExists(t, filepath.Join(dest, "late"))
}

func TestCheckRcloneEntryName(t *testing.T) {
	for _, ok := range []string{"a", "a/b", "dir/file.txt", ".hidden"} {
		assert.NoError(t, checkRcloneEntryName(ok), ok)
	}
	for _, bad := range []string{"", "..", "../x", "a/../../x", "/etc/passwd", `a\b`, `..\x`} {
		assert.ErrorIs(t, checkRcloneEntryName(bad), errRemoteContentRefused, bad)
	}
}

// The tar scan counts every header, whatever its type, and sums the
// declared sizes, before anything is written.
func TestExtractTarGz_ScanCountsEveryHeaderAndDeclaredSize(t *testing.T) {
	var fifos []tarEntry
	for i := 0; i < 50; i++ {
		fifos = append(fifos, tarEntry{typ: tar.TypeFifo, name: fmt.Sprintf("p%02d", i)})
	}
	flood := writeArchive(t, buildTarGz(t, fifos), "fifos.tar.gz")
	dest := t.TempDir()
	assert.ErrorIs(t, extractTarGz(flood, dest, &fetchBudget{maxBytes: 1 << 20, maxEntries: 10}), ErrRemoteTemplateTooLarge)
	assert.Zero(t, dirFileCount(t, dest))

	big := writeArchive(t, buildTarGz(t, []tarEntry{
		{typ: tar.TypeReg, name: "small.txt", body: "x"},
		{typ: tar.TypeReg, name: "big.bin", body: strings.Repeat("b", 5000)},
	}), "big.tar.gz")
	dest = t.TempDir()
	assert.ErrorIs(t, extractTarGz(big, dest, &fetchBudget{maxBytes: 1000, maxEntries: 100}), ErrRemoteTemplateTooLarge)
	assert.Zero(t, dirFileCount(t, dest), "refused in the scan, before any write")
}

// Header records (here a large pax record) count toward the decompressed
// stream limit, so data that is never written is bounded too.
func TestExtractTarGz_StreamLimitBoundsHeaderRecords(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	require.NoError(t, tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeReg, Name: "f.txt", Size: 1, Mode: 0o644, Format: tar.FormatPAX,
		PAXRecords: map[string]string{"comment": strings.Repeat("c", 200<<10)},
	}))
	_, err := tw.Write([]byte("x"))
	require.NoError(t, err)
	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())
	tgz := writeArchive(t, buf.Bytes(), "pax.tar.gz")

	// Limit: 1 MiB + 102 * 16 KiB, well above the 200 KiB record.
	require.NoError(t, extractTarGz(tgz, t.TempDir(), &fetchBudget{maxBytes: 1 << 20, maxEntries: 100}))
	// Limit: 10 + 3 * 16 KiB, below the record.
	assert.ErrorIs(t, extractTarGz(tgz, t.TempDir(), &fetchBudget{maxBytes: 10, maxEntries: 1}), ErrRemoteTemplateTooLarge)
}

func TestLimitedStream(t *testing.T) {
	data, err := io.ReadAll(&limitedStream{r: strings.NewReader("0123456789"), limit: 10})
	require.NoError(t, err, "a stream that ends exactly at the limit reads to EOF")
	assert.Equal(t, "0123456789", string(data))
	_, err = io.ReadAll(&limitedStream{r: strings.NewReader("0123456789A"), limit: 10})
	assert.ErrorIs(t, err, ErrRemoteTemplateTooLarge)
}

// Directories created implicitly as parents are charged as entries, once
// each, for tar and zip; the sub-path copy charges its directories through
// the same helper (TestCopyDirExcludingGit_Limits).
func TestExtract_ImplicitParentDirsCharged(t *testing.T) {
	// 6 directories (a, a/b, a/b/c, x, x/y, x/y/z) + 2 files = 8 entries;
	// two top-level directories, so no common root is stripped.
	names := []string{"a/b/c/f1", "x/y/z/f2"}
	var tes []tarEntry
	var zes []zipEntry
	for _, n := range names {
		tes = append(tes, tarEntry{typ: tar.TypeReg, name: n, body: "1"})
		zes = append(zes, zipEntry{name: n, body: "1", mode: 0o644})
	}
	tgz := writeArchive(t, buildTarGz(t, tes), "deep.tar.gz")
	zipPath := writeArchive(t, buildZip(t, zes), "deep.zip")
	for name, fn := range map[string]func(string, *fetchBudget) error{
		"tar": func(d string, b *fetchBudget) error { return extractTarGz(tgz, d, b) },
		"zip": func(d string, b *fetchBudget) error { return extractZip(zipPath, d, b) },
	} {
		t.Run(name, func(t *testing.T) {
			require.NoError(t, fn(t.TempDir(), &fetchBudget{maxBytes: 1 << 20, maxEntries: 8}))
			assert.ErrorIs(t, fn(t.TempDir(), &fetchBudget{maxBytes: 1 << 20, maxEntries: 7}), ErrRemoteTemplateTooLarge)
		})
	}

	// The same budget instance charges a directory once, explicit or not.
	b := &fetchBudget{maxBytes: 1 << 20, maxEntries: 100}
	root, err := os.OpenRoot(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = root.Close() })
	require.NoError(t, b.mkdirAll(root, filepath.Join("p", "q", "r"), 0o755))
	require.NoError(t, b.mkdirAll(root, "p", 0o755))
	require.NoError(t, b.mkdirAll(root, filepath.Join("p", "q", "s"), 0o755))
	assert.Equal(t, 4, b.entries)
}

// A zip whose central directory declares a huge size after a small entry is
// refused from the central directory: the sum of declared sizes cannot wrap
// (with a plain sum, 1 + MaxUint64 wraps to 0 and "a" would be written).
func TestExtractZip_HugeDeclaredSizesRefused(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range []struct {
		name string
		size uint64
	}{{"a", 1}, {"b", math.MaxUint64}} {
		w, err := zw.CreateRaw(&zip.FileHeader{
			Name: e.name, Method: zip.Store,
			CompressedSize64: 1, UncompressedSize64: e.size, CRC32: crc32.ChecksumIEEE([]byte("x")),
		})
		require.NoError(t, err)
		_, err = w.Write([]byte("x"))
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())
	zipPath := writeArchive(t, buf.Bytes(), "huge.zip")
	dest := t.TempDir()
	assert.ErrorIs(t, extractZip(zipPath, dest, &fetchBudget{maxBytes: 1 << 20, maxEntries: 100}), ErrRemoteTemplateTooLarge)
	assert.Zero(t, dirFileCount(t, dest), "refused before any entry is written")
}

// A tar header declaring a huge size after a small entry is refused by the
// scan as too large: the sum of declared sizes cannot wrap (with a plain
// sum, 1 + MaxInt64 goes negative and the scan would go on reading).
func TestExtractTarGz_HugeDeclaredSizeRefused(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	require.NoError(t, tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: "a", Size: 1, Mode: 0o644}))
	_, err := tw.Write([]byte("x"))
	require.NoError(t, err)
	require.NoError(t, tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: "b", Size: math.MaxInt64, Mode: 0o644, Format: tar.FormatPAX}))
	// The body is never written: WriteHeader has already written the header
	// blocks, and the archive ends there (tw is deliberately not closed).
	require.NoError(t, gz.Close())
	tgz := writeArchive(t, buf.Bytes(), "huge.tar.gz")
	dest := t.TempDir()
	assert.ErrorIs(t, extractTarGz(tgz, dest, &fetchBudget{maxBytes: 1 << 20, maxEntries: 100}), ErrRemoteTemplateTooLarge)
	assert.Zero(t, dirFileCount(t, dest))
}

// The zip central directory is checked against the limits before any
// entry is written.
func TestExtractZip_CentralDirectoryChecked(t *testing.T) {
	var many []zipEntry
	for i := 0; i < 50; i++ {
		many = append(many, zipEntry{name: fmt.Sprintf("f%02d", i), body: "1", mode: 0o644})
	}
	zipPath := writeArchive(t, buildZip(t, many), "many.zip")
	dest := t.TempDir()
	assert.ErrorIs(t, extractZip(zipPath, dest, &fetchBudget{maxBytes: 1 << 20, maxEntries: 10}), ErrRemoteTemplateTooLarge)
	assert.Zero(t, dirFileCount(t, dest))

	bigPath := writeArchive(t, buildZip(t, []zipEntry{
		{name: "a", body: "1", mode: 0o644},
		{name: "b", body: strings.Repeat("b", 5000), mode: 0o644},
	}), "big.zip")
	dest = t.TempDir()
	assert.ErrorIs(t, extractZip(bigPath, dest, &fetchBudget{maxBytes: 1000, maxEntries: 100}), ErrRemoteTemplateTooLarge)
	assert.Zero(t, dirFileCount(t, dest), "refused from the central directory, before any write")
}

// Through FetchRemoteTemplate, an rclone source over the limits aborts the
// fetch and leaves no cache directory behind.
func TestFetchRemoteTemplate_RcloneLimitRemovesPartialCache(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	src := localRcloneSource(t, 4, 10)
	t.Cleanup(SetRcloneFetchForTest(func(ctx context.Context, _ RcloneRemote, _ RemoteFetchCredentials, destPath string) error {
		return rcloneCopyLimited(ctx, src, destPath)
	}))
	const uri = ":gcs:my-bucket/tpl"
	cacheDir, err := remoteCacheDir()
	require.NoError(t, err)
	cachePath := filepath.Join(cacheDir, generateCacheKey(uri))

	setFetchLimitsForTest(t, 39, 100, 1<<20, 100, 1<<20)
	_, err = FetchRemoteTemplate(context.Background(), uri, RemoteFetchOptions{})
	require.ErrorIs(t, err, ErrRemoteTemplateTooLarge)
	_, statErr := os.Stat(cachePath)
	assert.True(t, os.IsNotExist(statErr))
}
