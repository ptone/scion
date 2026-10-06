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

package artifacts

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/artifacts/remotefetch"
	"github.com/GoogleCloudPlatform/scion/pkg/storage"
)

// RemoteImageLimits bound the remote images fetched at publish time.
type RemoteImageLimits struct {
	Enabled bool
	// MaxCount caps the images fetched for one version.
	MaxCount int
	// MaxBytes caps one image.
	MaxBytes int64
	// FetchTimeout bounds one fetch; TotalBudget bounds all of them.
	FetchTimeout time.Duration
	TotalBudget  time.Duration
}

// DefaultRemoteImageLimits are used when the host supplies none.
func DefaultRemoteImageLimits() RemoteImageLimits {
	return RemoteImageLimits{
		Enabled: true, MaxCount: 32, MaxBytes: 5 << 20,
		FetchTimeout: 10 * time.Second, TotalBudget: 30 * time.Second,
	}
}

// ImageFetcher fetches one remote image. remotefetch.Fetcher implements it.
type ImageFetcher interface {
	Fetch(ctx context.Context, rawURL string) (*remotefetch.Result, error)
}

// remoteExtractLimit is how many distinct image URLs are read from one
// document: a few times the fetch cap, so the warning can say roughly how
// many were left out, while a large document is never scanned beyond it.
func remoteExtractLimit(l RemoteImageLimits) int { return 4 * l.MaxCount }

// remoteFetchConcurrency is how many images of one version are fetched at
// once.
const remoteFetchConcurrency = 4

// remoteFetchFailed is the only failure text stored or shown for a remote
// image. The specific reason is logged on the server and nowhere else, so
// a publisher cannot tell a refused address from a missing image.
const remoteFetchFailed = "image could not be fetched"

func defaultFetcherFactory(l RemoteImageLimits) ImageFetcher {
	return remotefetch.New(remotefetch.Config{MaxBytes: l.MaxBytes, Timeout: l.FetchTimeout})
}

// setImageFetcherFactory replaces the function that builds the fetcher for
// a publish from the current limits. It is unexported so only this
// package's tests can replace the guarded fetcher.
func (s *Service) setImageFetcherFactory(fn func(RemoteImageLimits) ImageFetcher) {
	s.mu.Lock()
	s.fetcherFactory = fn
	s.mu.Unlock()
}

// remoteImageLimits returns the host's remote image limits. Without a
// limits getter the defaults apply; limits the host does supply but that
// are incomplete or invalid turn remote images off (fail closed).
func (b backend) remoteImageLimits(ctx context.Context) RemoteImageLimits {
	if b.limits == nil {
		return DefaultRemoteImageLimits()
	}
	l := b.limits(ctx).RemoteImages
	if l.MaxCount <= 0 || l.MaxBytes <= 0 || l.FetchTimeout <= 0 || l.TotalBudget <= 0 {
		return RemoteImageLimits{}
	}
	return l
}

// fetchRemoteImages fetches the images at urls into the blob store and
// returns one manifest row per URL it processed, in order, plus the
// warnings for the publisher. A failure never fails the publish: the row is
// marked failed with a generic error and the warning is generic too.
func (s *Service) fetchRemoteImages(ctx context.Context, b backend, lim RemoteImageLimits, versionID string, urls []string) ([]File, []string) {
	if !lim.Enabled || len(urls) == 0 {
		return nil, nil
	}
	var warnings []string
	if len(urls) > lim.MaxCount {
		more := fmt.Sprintf("%d", len(urls)-lim.MaxCount)
		if len(urls) >= remoteExtractLimit(lim) {
			more += " or more"
		}
		warnings = append(warnings, fmt.Sprintf("%s more remote images were not fetched (at most %d per version)", more, lim.MaxCount))
		urls = urls[:lim.MaxCount]
	}
	s.mu.RLock()
	factory := s.fetcherFactory
	s.mu.RUnlock()
	if factory == nil {
		factory = defaultFetcherFactory
	}
	fetcher := factory(lim)

	budget, cancel := context.WithTimeout(ctx, lim.TotalBudget)
	defer cancel()
	files := make([]File, len(urls))
	sem := make(chan struct{}, remoteFetchConcurrency)
	var wg sync.WaitGroup
	for i, u := range urls {
		wg.Add(1)
		go func(i int, u string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			files[i] = s.fetchOne(ctx, budget, b, fetcher, versionID, u)
		}(i, u)
	}
	wg.Wait()
	for _, f := range files {
		if f.FetchStatus != FetchStatusOK {
			warnings = append(warnings, remoteFetchFailed+": "+f.SourceURL)
		}
	}
	return files, warnings
}

// fetchOne fetches one image under the budget and stores it. Uploads use
// ctx, not the budget, so a fetched image is not lost to the deadline.
func (s *Service) fetchOne(ctx, budget context.Context, b backend, fetcher ImageFetcher, versionID, u string) File {
	f := File{VersionID: versionID, Path: RemotePath(u), Origin: FileOriginRemote, SourceURL: u}
	res, err := fetcher.Fetch(budget, u)
	if err != nil {
		// The fetcher logs the specific reason.
		f.FetchStatus, f.FetchError = FetchStatusFailed, remoteFetchFailed
		return f
	}
	if err := putBlobBytes(ctx, b, res.SHA256, res.Body, res.ContentType); err != nil {
		slog.ErrorContext(ctx, "artifacts: remote image blob write failed", "error", err)
		f.FetchStatus, f.FetchError = FetchStatusFailed, remoteFetchFailed
		return f
	}
	f.Size, f.SHA256, f.MediaType, f.FetchStatus = int64(len(res.Body)), res.SHA256, res.ContentType, FetchStatusOK
	return f
}

// putBlobBytes stores body at its content address unless that blob exists.
func putBlobBytes(ctx context.Context, b backend, digest string, body []byte, mediaType string) error {
	p := BlobPath(b.hubID, digest)
	exists, err := b.blobs.Exists(ctx, p)
	if err != nil {
		return fmt.Errorf("check blob: %w", err)
	}
	if exists {
		return nil
	}
	if _, err := b.blobs.Upload(ctx, p, bytes.NewReader(body), storage.UploadOptions{ContentType: mediaType}); err != nil {
		return fmt.Errorf("upload blob: %w", err)
	}
	return nil
}
