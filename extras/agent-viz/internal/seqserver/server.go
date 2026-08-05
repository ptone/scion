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

// Package seqserver serves the precomputed run digest and the sequence
// visualizer frontend.
//
// Unlike the original agent-viz server there is no WebSocket and no playback
// engine: the digest is computed once, up front, and the client owns the clock.
// That is what makes a shared link land on exactly the same moment for
// everyone who opens it.
package seqserver

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path"
	"strings"
	"time"

	"github.com/GoogleCloudPlatform/scion/extras/agent-viz/internal/digest"
)

//go:embed all:dist
var embeddedAssets embed.FS

// devAssetDirs are searched, in order, for built frontend assets in dev mode.
var devAssetDirs = []string{"web-seq/dist", "internal/seqserver/dist"}

// missingAssetsPage is served when the frontend has not been built yet, which
// is the normal state of a fresh checkout.
const missingAssetsPage = `<!doctype html>
<html><head><meta charset="utf-8"><title>seq-viz</title>
<style>body{font:14px/1.6 ui-monospace,monospace;margin:4rem auto;max-width:44rem;color:#ddd;background:#131417}
code{background:#22242a;padding:.15rem .4rem;border-radius:3px}</style></head>
<body>
<h1>seq-viz backend is running</h1>
<p>The frontend bundle has not been built, so there is nothing to render yet.</p>
<p>Build it with <code>make seq-web</code> (or <code>cd web-seq &amp;&amp; npm install &amp;&amp; npx vite build</code>)
and reload this page.</p>
<p>The digest itself is already being served at <a href="/api/digest">/api/digest</a>.</p>
</body></html>
`

// Server serves one precomputed digest.
type Server struct {
	digest     *digest.Digest
	digestJSON []byte
	digestErr  error
}

// New creates a server for the given digest. The JSON encoding is done once
// here: the digest is immutable, so re-marshalling per request would be pure
// waste on a payload that can reach a few megabytes.
func New(d *digest.Digest) *Server {
	s := &Server{digest: d}
	s.digestJSON, s.digestErr = json.Marshal(d)
	return s
}

// Start begins serving on the given port. In devMode the frontend is read from
// disk so a vite rebuild is picked up without recompiling Go.
func (s *Server) Start(port int, devMode bool) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/digest", s.handleDigest)
	mux.HandleFunc("/api/healthz", s.handleHealthz)

	assets, err := s.assetFS(devMode)
	if err != nil {
		return err
	}
	mux.Handle("/", s.staticHandler(assets))

	addr := fmt.Sprintf(":%d", port)
	log.Printf("Sequence visualizer running at http://localhost:%d", port)
	return http.ListenAndServe(addr, mux)
}

// assetFS resolves the filesystem holding the frontend bundle. A nil result is
// not an error: it means the bundle is missing and the placeholder page should
// be served instead.
func (s *Server) assetFS(devMode bool) (fs.FS, error) {
	if devMode {
		for _, dir := range devAssetDirs {
			if st, err := os.Stat(path.Join(dir, "index.html")); err == nil && !st.IsDir() {
				log.Printf("dev mode: serving web assets from %s", dir)
				return os.DirFS(dir), nil
			}
		}
		log.Printf("dev mode: no built assets found in %v", devAssetDirs)
		return nil, nil
	}
	sub, err := fs.Sub(embeddedAssets, "dist")
	if err != nil {
		return nil, fmt.Errorf("embedded assets: %w", err)
	}
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		// Only the .gitkeep is embedded; the frontend was never built.
		return nil, nil
	}
	return sub, nil
}

func (s *Server) handleDigest(w http.ResponseWriter, r *http.Request) {
	if s.digestErr != nil {
		http.Error(w, "encoding digest: "+s.digestErr.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(s.digestJSON)
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ok"))
}

// staticHandler serves the bundle with SPA fallback: anything that is not a
// real file and not an API route resolves to index.html so client-side deep
// links survive a page load.
func (s *Server) staticHandler(assets fs.FS) http.Handler {
	if assets == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/api/") {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(missingAssetsPage))
		})
	}
	files := http.FileServer(http.FS(assets))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.NotFound(w, r)
			return
		}
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name == "" || name == "." {
			name = "index.html"
		}
		if st, err := fs.Stat(assets, name); err == nil && !st.IsDir() && name != "index.html" {
			files.ServeHTTP(w, r)
			return
		}
		// Fallback (and the bare root) must write index.html itself rather than
		// rewrite the path and re-enter http.FileServer: FileServer redirects
		// any request ending in "index.html" to "./", which for a deep link
		// like /run/abc turns into an endless 301 chain instead of the app.
		s.serveIndex(w, r, assets)
	})
}

// serveIndex writes the SPA entry document. index.html is served no-cache
// because the entry bundle is emitted at a stable, unhashed path, so a stale
// cached document would keep pointing at an old build.
func (s *Server) serveIndex(w http.ResponseWriter, r *http.Request, assets fs.FS) {
	data, err := fs.ReadFile(assets, "index.html")
	if err != nil {
		http.Error(w, "index.html not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(data))
}
