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

package runtimebroker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/agent"
	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/apiclient"
	"github.com/GoogleCloudPlatform/scion/pkg/config"
	"github.com/GoogleCloudPlatform/scion/pkg/hubclient"
	scionrt "github.com/GoogleCloudPlatform/scion/pkg/runtime"
	"github.com/GoogleCloudPlatform/scion/pkg/transfer/bundle"
	"github.com/GoogleCloudPlatform/scion/pkg/util"
)

// moveBundleSettings holds test-overridable dependencies and timings for the
// move-bundle broker endpoints. Zero values take production defaults.
type moveBundleSettings struct {
	client            *http.Client
	fetchUploadURL    func(ctx context.Context, hubConn, agentID, rid string, req *api.MoveBundleUploadURLRequest) (*api.MoveBundleUploadURLResponse, error)
	fetchDownloadURLs func(ctx context.Context, hubConn, agentID, rid string, req *api.MoveBundleDownloadURLsRequest) (*api.MoveBundleDownloadURLsResponse, error)
	sleep             func(ctx context.Context, d time.Duration) error
	now               func() time.Time
	urlBackoff        []time.Duration
	partBackoff       []time.Duration
	scratchDir        string
	jobTTL            time.Duration
	// durabilityRuntime, when non-empty (tests only), overrides the runtime
	// name checked by precheck durability.
	durabilityRuntime string
}

func (st moveBundleSettings) resolved(s *Server) moveBundleSettings {
	if st.client == nil {
		st.client = defaultSignedDownloadClient
	}
	if st.fetchUploadURL == nil {
		st.fetchUploadURL = s.fetchMoveBundleUploadURL
	}
	if st.fetchDownloadURLs == nil {
		st.fetchDownloadURLs = s.fetchMoveBundleDownloadURLs
	}
	if st.sleep == nil {
		st.sleep = sleepCtx
	}
	if st.now == nil {
		st.now = time.Now
	}
	if st.urlBackoff == nil {
		st.urlBackoff = defaultSignedPageBackoff
	}
	if st.partBackoff == nil {
		st.partBackoff = defaultSignedFileBackoff
	}
	if st.jobTTL <= 0 {
		st.jobTTL = defaultMoveBundleJobTTL
	}
	return st
}

func (s *Server) moveBundleRegistry() *moveBundleJobRegistry {
	s.moveBundleOnce.Do(func() {
		st := s.moveBundle.resolved(s)
		s.moveBundleJobs = newMoveBundleJobRegistry(st.now, st.jobTTL)
	})
	return s.moveBundleJobs
}

func (s *Server) moveBundleScratchBase() string {
	if s.moveBundle.scratchDir != "" {
		return s.moveBundle.scratchDir
	}
	if s.stateDir != "" {
		dir := filepath.Join(s.stateDir, "move-bundle-scratch")
		if err := os.MkdirAll(dir, 0o700); err == nil {
			return dir
		}
	}
	return ""
}

// validMoveBundleRID checks that rid is a single clean path segment suitable
// for use in URLs and the ".scion-move-bundle-<rid>" marker filename.
func validMoveBundleRID(rid string) bool {
	if rid == "" || len(rid) > 128 || !isSingleCleanPathElement(rid) {
		return false
	}
	if strings.HasPrefix(rid, ".") {
		return false
	}
	for _, c := range rid {
		ok := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.'
		if !ok {
			return false
		}
	}
	return true
}

// handleMoveBundle routes /api/v1/agents/{id}/move-bundle/{subaction}:
//   - POST .../move-bundle/precheck
//   - POST .../move-bundle/export
//   - GET  .../move-bundle/export/{rid}
//   - POST .../move-bundle/import
//   - GET  .../move-bundle/import/{rid}
func (s *Server) handleMoveBundle(w http.ResponseWriter, r *http.Request, id, projectID, subaction string) {
	switch {
	case subaction == "precheck":
		if r.Method != http.MethodPost {
			MethodNotAllowed(w, http.MethodPost)
			return
		}
		s.handleMoveBundlePrecheck(w, r, id, projectID)
	case subaction == "export":
		if r.Method != http.MethodPost {
			MethodNotAllowed(w, http.MethodPost)
			return
		}
		s.handleMoveBundleExportStart(w, r, id, projectID)
	case strings.HasPrefix(subaction, "export/"):
		if r.Method != http.MethodGet {
			MethodNotAllowed(w, http.MethodGet)
			return
		}
		rid := strings.TrimPrefix(subaction, "export/")
		s.handleMoveBundleJobStatus(w, r, id, projectID, api.MoveBundleJobKindExport, rid)
	case subaction == "import":
		if r.Method != http.MethodPost {
			MethodNotAllowed(w, http.MethodPost)
			return
		}
		s.handleMoveBundleImportStart(w, r, id, projectID)
	case strings.HasPrefix(subaction, "import/"):
		if r.Method != http.MethodGet {
			MethodNotAllowed(w, http.MethodGet)
			return
		}
		rid := strings.TrimPrefix(subaction, "import/")
		s.handleMoveBundleJobStatus(w, r, id, projectID, api.MoveBundleJobKindImport, rid)
	default:
		NotFound(w, "MoveBundleAction")
	}
}

// moveBundleDirs holds the resolved host paths for an agent's bundle roots.
type moveBundleDirs struct {
	agentDir     string
	workspaceDir string
	homeDir      string
}

func (s *Server) resolveMoveBundleDirs(agentSlug, projectID, projectPath, projectSlug string) (moveBundleDirs, error) {
	if !isSingleCleanPathElement(agentSlug) {
		return moveBundleDirs{}, fmt.Errorf("invalid agent id")
	}
	if projectSlug != "" && !isSingleCleanPathElement(projectSlug) {
		return moveBundleDirs{}, fmt.Errorf("invalid projectSlug")
	}

	var rawProject string
	switch {
	case projectPath != "":
		rawProject = projectPath
	case projectSlug != "":
		globalDir, err := config.GetGlobalDir()
		if err != nil {
			return moveBundleDirs{}, fmt.Errorf("resolve global dir: %w", err)
		}
		rawProject = filepath.Join(globalDir, "projects", projectSlug)
	default:
		globalDir, err := config.GetGlobalDir()
		if err != nil {
			return moveBundleDirs{}, fmt.Errorf("resolve global dir: %w", err)
		}
		rawProject = globalDir
	}

	projectDir, err := config.GetResolvedProjectDir(rawProject)
	if err != nil {
		return moveBundleDirs{}, fmt.Errorf("resolve project dir: %w", err)
	}
	if _, err := agent.CheckAgentDirContained(projectDir, agentSlug, false); err != nil {
		return moveBundleDirs{}, fmt.Errorf("agent dir containment: %w", err)
	}

	agentDir := config.GetAgentDir(projectDir, agentSlug, false)
	homeDir := config.GetAgentHomePath(projectDir, agentSlug)
	workspaceDir := filepath.Join(agentDir, "workspace")
	if config.IsGlobalProjectDir(projectDir) {
		workspaceDir = filepath.Join(projectDir, "workspace", agentSlug)
	}
	return moveBundleDirs{
		agentDir:     agentDir,
		workspaceDir: workspaceDir,
		homeDir:      homeDir,
	}, nil
}

// resolveMoveBundleRuntimeName determines the runtime type name for durability
// checking on this broker.
func (s *Server) resolveMoveBundleRuntimeName(r *http.Request, id, projectID string) string {
	if s.moveBundle.durabilityRuntime != "" {
		return s.moveBundle.durabilityRuntime
	}
	if recorded := strings.TrimSpace(r.URL.Query().Get(api.RecordedRuntimeQueryParam)); recorded != "" {
		return recorded
	}
	if rt := s.resolveRuntimeForAgent(r.Context(), id, projectID); rt != nil {
		return rt.Name()
	}
	if _, rt, _ := s.defaultPair(); rt != nil {
		return rt.Name()
	}
	return ""
}

// moveBundleHomeExcluder returns the $HOME excluder combining the shared
// runtime.PortableHomeExcludes list with the D6 identity-credential and cache
// exclusion lists in pkg/transfer/bundle.
func moveBundleHomeExcluder() bundle.Excluder {
	return bundle.HomeExcluder(bundle.HomeExcluderOptions{
		ExtraLiterals: scionrt.PortableHomeExcludes(scionrt.RunConfig{}),
		TempNamePred:  scionrt.IsHomeTempName,
		AgentHomeAbs:  util.GetHomeDir(""), // TODO(part-b): pass agent's UnixUsername when available
	})
}

func decodeOptionalJSON(r *http.Request, dst any) error {
	if r.Body == nil {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		return err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil
	}
	return json.Unmarshal(data, dst)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// handleMoveBundlePrecheck implements POST /api/v1/agents/{id}/move-bundle/precheck.
func (s *Server) handleMoveBundlePrecheck(w http.ResponseWriter, r *http.Request, id, projectID string) {
	var req api.MoveBundlePrecheckRequest
	if err := decodeOptionalJSON(r, &req); err != nil {
		BadRequest(w, "invalid request body: "+err.Error())
		return
	}
	if err := req.Limits.Validate(); err != nil {
		ValidationError(w, err.Error(), nil)
		return
	}

	rtName := s.resolveMoveBundleRuntimeName(r, id, firstNonEmpty(req.ProjectID, projectID))
	if !api.IsLocalDiskRuntime(rtName) {
		writeJSON(w, http.StatusOK, api.MoveBundlePrecheckResponse{
			Durable: false,
			Reason:  "not yet supported",
		})
		return
	}

	dirs, err := s.resolveMoveBundleDirs(
		id,
		firstNonEmpty(req.ProjectID, projectID),
		firstNonEmpty(req.ProjectPath, r.URL.Query().Get("projectPath")),
		firstNonEmpty(req.ProjectSlug, r.URL.Query().Get("projectSlug")),
	)
	if err != nil {
		ValidationError(w, err.Error(), nil)
		return
	}

	wsEst, wsErr := estimateMoveBundleRoot(r.Context(), dirs.workspaceDir, bundle.WorkspaceExcluder(), req.Limits.WorkspaceLimits())
	if wsErr != nil {
		if errors.Is(wsErr, fs.ErrNotExist) {
			NotFound(w, "Agent workspace")
			return
		}
		if errors.Is(wsErr, context.Canceled) || errors.Is(wsErr, context.DeadlineExceeded) {
			InternalError(w)
			return
		}
		writeJSON(w, http.StatusOK, api.MoveBundlePrecheckResponse{
			Durable: false,
			Reason:  fmt.Sprintf("source workspace cannot be bundled: %v", wsErr),
		})
		return
	}

	resp := api.MoveBundlePrecheckResponse{
		Durable:   true,
		Workspace: wsEst,
	}
	if wsEst.Over != "" {
		resp.Over = bundle.RootWorkspace + "." + wsEst.Over
		writeJSON(w, http.StatusOK, resp)
		return
	}

	homeEst, homeErr := estimateMoveBundleRoot(r.Context(), dirs.homeDir, moveBundleHomeExcluder(), req.Limits.HomeLimits())
	if homeErr != nil {
		if errors.Is(homeErr, fs.ErrNotExist) {
			NotFound(w, "Agent home")
			return
		}
		if errors.Is(homeErr, context.Canceled) || errors.Is(homeErr, context.DeadlineExceeded) {
			InternalError(w)
			return
		}
		writeJSON(w, http.StatusOK, api.MoveBundlePrecheckResponse{
			Durable: false,
			Reason:  fmt.Sprintf("source home cannot be bundled: %v", homeErr),
		})
		return
	}
	resp.Home = homeEst
	if homeEst.Over != "" {
		resp.Over = bundle.RootHome + "." + homeEst.Over
	}

	writeJSON(w, http.StatusOK, resp)
}

func estimateMoveBundleRoot(ctx context.Context, dir string, ex bundle.Excluder, lim bundle.Limits) (api.MoveBundleRootEstimate, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return api.MoveBundleRootEstimate{}, err
	}
	defer func() { _ = root.Close() }()

	res, err := bundle.Estimate(ctx, root, ex, lim)
	est := api.MoveBundleRootEstimate{
		Bytes:   res.Bytes,
		Entries: res.Entries,
	}
	if err != nil {
		var limErr *bundle.ErrLimitExceeded
		if errors.As(err, &limErr) {
			est.Over = limErr.Field
			return est, nil
		}
		return est, err
	}
	return est, nil
}

// handleMoveBundleJobStatus implements GET /api/v1/agents/{id}/move-bundle/{export|import}/{rid}.
func (s *Server) handleMoveBundleJobStatus(w http.ResponseWriter, r *http.Request, id, projectID, kind, rid string) {
	if !validMoveBundleRID(rid) {
		ValidationError(w, "invalid reincarnationId", nil)
		return
	}
	agentKey := moveBundleAgentKey{ProjectID: projectID, AgentSlug: id, Kind: kind}
	job, ok := s.moveBundleRegistry().get(agentKey, rid)
	if !ok {
		NotFound(w, "MoveBundleJob")
		return
	}
	writeJSON(w, http.StatusOK, job.snapshot())
}

// handleMoveBundleExportStart implements POST /api/v1/agents/{id}/move-bundle/export.
func (s *Server) handleMoveBundleExportStart(w http.ResponseWriter, r *http.Request, id, projectID string) {
	var req api.MoveBundleExportRequest
	if err := decodeOptionalJSON(r, &req); err != nil {
		BadRequest(w, "invalid request body: "+err.Error())
		return
	}
	if !validMoveBundleRID(req.ReincarnationID) {
		ValidationError(w, "invalid or missing reincarnationId", nil)
		return
	}
	if err := req.Limits.Validate(); err != nil {
		ValidationError(w, err.Error(), nil)
		return
	}

	effProjectID := firstNonEmpty(req.ProjectID, projectID)
	dirs, err := s.resolveMoveBundleDirs(
		id,
		effProjectID,
		firstNonEmpty(req.ProjectPath, r.URL.Query().Get("projectPath")),
		firstNonEmpty(req.ProjectSlug, r.URL.Query().Get("projectSlug")),
	)
	if err != nil {
		ValidationError(w, err.Error(), nil)
		return
	}

	hubConn := s.resolveHubNameForLaunch(r)
	agentUUID := firstNonEmpty(req.AgentID, r.URL.Query().Get(api.AgentIDQueryParam), id)

	jobCtx, cancel := context.WithTimeout(context.Background(), req.Limits.Timeout())
	agentKey := moveBundleAgentKey{ProjectID: effProjectID, AgentSlug: id, Kind: api.MoveBundleJobKindExport}
	job, conflict, created := s.moveBundleRegistry().start(agentKey, req.ReincarnationID, cancel)
	if conflict {
		cancel()
		writeError(w, http.StatusConflict, api.BrokerErrCodeMoveBundleJobConflict, "another move-bundle export job is already running for this agent", nil)
		return
	}
	if !created {
		cancel()
		writeJSON(w, http.StatusAccepted, job.snapshot())
		return
	}

	go s.runMoveBundleExport(jobCtx, cancel, job, hubConn, agentUUID, dirs, req)
	writeJSON(w, http.StatusAccepted, job.snapshot())
}

func (s *Server) runMoveBundleExport(
	ctx context.Context,
	cancel context.CancelFunc,
	job *moveBundleJob,
	hubConn string,
	agentID string,
	dirs moveBundleDirs,
	req api.MoveBundleExportRequest,
) {
	defer cancel()
	st := s.moveBundle.resolved(s)

	scratchDir, err := os.MkdirTemp(s.moveBundleScratchBase(), "mb-export-*")
	if err != nil {
		job.markFailed("bundle export failed: create scratch dir", "", st.now())
		return
	}
	defer func() { _ = os.RemoveAll(scratchDir) }()

	stallTimeout := req.Limits.PartStallTimeout()
	type rootResult struct {
		name string
		rm   bundle.RootManifest
		err  error
	}

	runCtx, runCancel := context.WithCancel(ctx)
	defer runCancel()

	roots := []struct {
		name string
		dir  string
		ex   bundle.Excluder
		lim  bundle.Limits
	}{
		{bundle.RootWorkspace, dirs.workspaceDir, bundle.WorkspaceExcluder(), req.Limits.WorkspaceLimits()},
		{bundle.RootHome, dirs.homeDir, moveBundleHomeExcluder(), req.Limits.HomeLimits()},
	}

	results := make(chan rootResult, len(roots))
	var wg sync.WaitGroup
	for _, rt := range roots {
		wg.Add(1)
		go func(name, dir string, ex bundle.Excluder, lim bundle.Limits) {
			defer wg.Done()
			root, openErr := os.OpenRoot(dir)
			if openErr != nil {
				results <- rootResult{name: name, err: fmt.Errorf("open %s root: %w", name, openErr)}
				runCancel()
				return
			}
			defer func() { _ = root.Close() }()

			sink := &hubSignedPartSink{
				st:           st,
				hubConn:      hubConn,
				agentID:      agentID,
				rid:          req.ReincarnationID,
				rootName:     name,
				stallTimeout: stallTimeout,
				job:          job,
			}
			rm, writeErr := bundle.Write(runCtx, root, scratchDir, ex, lim, sink)
			if writeErr != nil {
				results <- rootResult{name: name, err: writeErr}
				runCancel()
				return
			}
			results <- rootResult{name: name, rm: rm}
		}(rt.name, rt.dir, rt.ex, rt.lim)
	}
	wg.Wait()
	close(results)

	manifest := bundle.Manifest{
		Version:         bundle.ManifestVersion,
		ReincarnationID: req.ReincarnationID,
		Roots:           make(map[string]bundle.RootManifest, 2),
	}
	var firstErr error
	for r := range results {
		if r.err != nil && firstErr == nil {
			firstErr = r.err
			continue
		}
		if r.err == nil {
			manifest.Roots[r.name] = r.rm
		}
	}
	if firstErr != nil {
		job.markFailed(formatMoveBundleError("bundle export failed", firstErr), "", st.now())
		return
	}

	job.markSucceeded(&manifest, nil, st.now())
}

// hubSignedPartSink implements bundle.PartSink by asking the Hub for a
// per-part signed PUT URL and uploading the compressed part with retries and
// URL redaction.
type hubSignedPartSink struct {
	st           moveBundleSettings
	hubConn      string
	agentID      string
	rid          string
	rootName     string
	stallTimeout time.Duration
	job          *moveBundleJob
}

func (p *hubSignedPartSink) Put(ctx context.Context, index int, part io.ReadSeeker, size int64, hash string) error {
	urlResp, err := fetchUploadURLWithRetry(ctx, p.st, p.hubConn, p.agentID, p.rid, p.rootName, index, size, hash)
	if err != nil {
		return err
	}
	if err := signedPutPart(
		ctx, p.st.client, urlResp.URL, urlResp.Headers, part, size,
		p.rootName, index, p.stallTimeout, p.st.partBackoff, p.st.sleep,
	); err != nil {
		return err
	}
	p.job.addProgress(size, 1)
	return nil
}

func fetchUploadURLWithRetry(
	ctx context.Context,
	st moveBundleSettings,
	hubConn, agentID, rid, rootName string,
	partIndex int,
	size int64,
	hash string,
) (*api.MoveBundleUploadURLResponse, error) {
	req := &api.MoveBundleUploadURLRequest{
		Root:      rootName,
		PartIndex: partIndex,
		Size:      size,
		Hash:      hash,
	}
	for attempt := 0; ; attempt++ {
		resp, err := st.fetchUploadURL(ctx, hubConn, agentID, rid, req)
		if err == nil {
			if resp == nil || resp.URL == "" {
				return nil, &signedBundleTransferError{Stage: "upload_url", Root: rootName, PartIndex: partIndex, Class: signedClassDecode}
			}
			return resp, nil
		}
		mapped, retryable := mapBundleURLError("upload_url", rootName, partIndex, err)
		if !retryable || ctx.Err() != nil || attempt >= len(st.urlBackoff) {
			if cerr := ctx.Err(); cerr != nil {
				return nil, &signedBundleTransferError{Stage: "upload_url", Root: rootName, PartIndex: partIndex, Class: contextClass(cerr)}
			}
			return nil, mapped
		}
		if serr := st.sleep(ctx, st.urlBackoff[attempt]); serr != nil {
			return nil, &signedBundleTransferError{Stage: "upload_url", Root: rootName, PartIndex: partIndex, Class: contextClass(serr)}
		}
	}
}

func (s *Server) fetchMoveBundleUploadURL(ctx context.Context, hubConn, agentID, rid string, req *api.MoveBundleUploadURLRequest) (*api.MoveBundleUploadURLResponse, error) {
	s.hubMu.RLock()
	conn := s.hubConnections[hubConn]
	s.hubMu.RUnlock()
	if hubConn == "" || conn == nil || conn.HubClient == nil {
		return nil, errNoSignedDownloadHub
	}
	svc, ok := conn.HubClient.RuntimeBrokers().(hubclient.RuntimeBrokerMoveBundleService)
	if !ok {
		return nil, errNoSignedDownloadHub
	}
	return svc.MoveBundleUploadURL(ctx, conn.BrokerID, agentID, rid, req)
}

func (s *Server) fetchMoveBundleDownloadURLs(ctx context.Context, hubConn, agentID, rid string, req *api.MoveBundleDownloadURLsRequest) (*api.MoveBundleDownloadURLsResponse, error) {
	s.hubMu.RLock()
	conn := s.hubConnections[hubConn]
	s.hubMu.RUnlock()
	if hubConn == "" || conn == nil || conn.HubClient == nil {
		return nil, errNoSignedDownloadHub
	}
	svc, ok := conn.HubClient.RuntimeBrokers().(hubclient.RuntimeBrokerMoveBundleService)
	if !ok {
		return nil, errNoSignedDownloadHub
	}
	return svc.MoveBundleDownloadURLs(ctx, conn.BrokerID, agentID, rid, req)
}

func mapBundleURLError(stage, rootName string, partIndex int, err error) (*signedBundleTransferError, bool) {
	out := &signedBundleTransferError{Stage: stage, Root: rootName, PartIndex: partIndex}
	var apiErr *apiclient.APIError
	switch {
	case errors.Is(err, errNoSignedDownloadHub):
		out.Class = signedClassNoHub
		return out, false
	case errors.As(err, &apiErr):
		out.Status = apiErr.StatusCode
		out.Class = "endpoint_" + strconv.Itoa(apiErr.StatusCode)
		return out, apiErr.StatusCode >= 500
	}
	out.Class = transportClass(err)
	if out.Class == signedClassDecode {
		return out, false
	}
	return out, out.Class != signedClassCanceled
}

// handleMoveBundleImportStart implements POST /api/v1/agents/{id}/move-bundle/import.
func (s *Server) handleMoveBundleImportStart(w http.ResponseWriter, r *http.Request, id, projectID string) {
	var req api.MoveBundleImportRequest
	if err := decodeOptionalJSON(r, &req); err != nil {
		BadRequest(w, "invalid request body: "+err.Error())
		return
	}
	if !validMoveBundleRID(req.ReincarnationID) {
		ValidationError(w, "invalid or missing reincarnationId", nil)
		return
	}
	if err := req.Limits.Validate(); err != nil {
		ValidationError(w, err.Error(), nil)
		return
	}
	if err := req.Manifest.Validate(); err != nil {
		ValidationError(w, "invalid manifest: "+err.Error(), nil)
		return
	}
	if req.Manifest.ReincarnationID != req.ReincarnationID {
		ValidationError(w, "manifest reincarnationId does not match request reincarnationId", nil)
		return
	}
	_, computedHash, err := bundle.CanonicalManifest(req.Manifest)
	if err != nil {
		ValidationError(w, "canonicalize manifest: "+err.Error(), nil)
		return
	}
	if req.ManifestHash == "" || req.ManifestHash != computedHash {
		ValidationError(w, "manifestHash does not match manifest", nil)
		return
	}

	effProjectID := firstNonEmpty(req.ProjectID, projectID)
	dirs, err := s.resolveMoveBundleDirs(
		id,
		effProjectID,
		firstNonEmpty(req.ProjectPath, r.URL.Query().Get("projectPath")),
		firstNonEmpty(req.ProjectSlug, r.URL.Query().Get("projectSlug")),
	)
	if err != nil {
		ValidationError(w, err.Error(), nil)
		return
	}

	imported, markerErr := hasMoveBundleImportedMarker(dirs.agentDir, req.ReincarnationID)
	if markerErr != nil {
		if errors.Is(markerErr, fs.ErrNotExist) {
			NotFound(w, "Agent directory")
			return
		}
		InternalError(w)
		return
	}
	if imported {
		writeError(w, http.StatusConflict, api.BrokerErrCodeMoveBundleAlreadyImported, "move-bundle already imported for this reincarnationId", nil)
		return
	}

	hubConn := s.resolveHubNameForLaunch(r)
	agentUUID := firstNonEmpty(req.AgentID, r.URL.Query().Get(api.AgentIDQueryParam), id)

	jobCtx, cancel := context.WithTimeout(context.Background(), req.Limits.Timeout())
	agentKey := moveBundleAgentKey{ProjectID: effProjectID, AgentSlug: id, Kind: api.MoveBundleJobKindImport}
	job, conflict, created := s.moveBundleRegistry().start(agentKey, req.ReincarnationID, cancel)
	if conflict {
		cancel()
		writeError(w, http.StatusConflict, api.BrokerErrCodeMoveBundleJobConflict, "another move-bundle import job is already running for this agent", nil)
		return
	}
	if !created {
		cancel()
		writeJSON(w, http.StatusAccepted, job.snapshot())
		return
	}

	var totalParts int
	var totalBytes int64
	for _, rm := range req.Manifest.Roots {
		totalParts += len(rm.Parts)
		for _, p := range rm.Parts {
			totalBytes += p.Size
		}
	}
	job.setTotals(totalBytes, totalParts)

	go s.runMoveBundleImport(jobCtx, cancel, job, hubConn, agentUUID, dirs, req)
	writeJSON(w, http.StatusAccepted, job.snapshot())
}

func (s *Server) runMoveBundleImport(
	ctx context.Context,
	cancel context.CancelFunc,
	job *moveBundleJob,
	hubConn string,
	agentID string,
	dirs moveBundleDirs,
	req api.MoveBundleImportRequest,
) {
	defer cancel()
	st := s.moveBundle.resolved(s)

	scratchDir, err := os.MkdirTemp(s.moveBundleScratchBase(), "mb-import-*")
	if err != nil {
		job.markFailed("bundle import failed: create scratch dir", "", st.now())
		return
	}
	defer func() { _ = os.RemoveAll(scratchDir) }()

	// Create sibling staging directories ".<base>.bundle-<32hex>" on the same
	// filesystem as workspaceDir and homeDir.
	wsStageName, err := newMoveBundleStageName(filepath.Base(dirs.workspaceDir))
	if err != nil {
		job.markFailed("bundle import failed: generate workspace staging name", "", st.now())
		return
	}
	homeStageName, err := newMoveBundleStageName(filepath.Base(dirs.homeDir))
	if err != nil {
		job.markFailed("bundle import failed: generate home staging name", "", st.now())
		return
	}
	wsStagePath := filepath.Join(filepath.Dir(dirs.workspaceDir), wsStageName)
	homeStagePath := filepath.Join(filepath.Dir(dirs.homeDir), homeStageName)

	if err := os.Mkdir(wsStagePath, 0o755); err != nil {
		job.markFailed("bundle import failed: create workspace staging dir", "", st.now())
		return
	}
	if err := os.Mkdir(homeStagePath, 0o755); err != nil {
		_ = os.RemoveAll(wsStagePath)
		job.markFailed("bundle import failed: create home staging dir", "", st.now())
		return
	}

	keepStagingOnCrash := false
	defer func() {
		if !keepStagingOnCrash {
			_ = os.RemoveAll(wsStagePath)
			_ = os.RemoveAll(homeStagePath)
		}
	}()

	pool := &moveBundleURLPool{
		st:      st,
		hubConn: hubConn,
		agentID: agentID,
		rid:     req.ReincarnationID,
	}

	stallTimeout := req.Limits.PartStallTimeout()
	runCtx, runCancel := context.WithCancel(ctx)
	defer runCancel()

	roots := []struct {
		name      string
		stagePath string
		lim       bundle.Limits
	}{
		{bundle.RootWorkspace, wsStagePath, req.Limits.WorkspaceLimits()},
		{bundle.RootHome, homeStagePath, req.Limits.HomeLimits()},
	}

	errCh := make(chan error, len(roots))
	var wg sync.WaitGroup
	for _, rt := range roots {
		wg.Add(1)
		go func(name, stagePath string, lim bundle.Limits) {
			defer wg.Done()
			stageRoot, openErr := os.OpenRoot(stagePath)
			if openErr != nil {
				errCh <- fmt.Errorf("open %s staging root: %w", name, openErr)
				runCancel()
				return
			}
			defer func() { _ = stageRoot.Close() }()

			src := &hubSignedPartSource{
				pool:         pool,
				st:           st,
				rootName:     name,
				scratchDir:   scratchDir,
				stallTimeout: stallTimeout,
				job:          job,
			}
			if exErr := bundle.ExtractFromManifest(runCtx, stageRoot, scratchDir, req.Manifest, name, lim, src); exErr != nil {
				errCh <- exErr
				runCancel()
				return
			}
		}(rt.name, rt.stagePath, rt.lim)
	}
	wg.Wait()
	close(errCh)

	for exErr := range errCh {
		if exErr != nil {
			job.markFailed(formatMoveBundleError("bundle import failed", exErr), "", st.now())
			return
		}
	}

	// Commit workspace (empty dir; fail on any conflicting top-level name).
	if err := commitMoveBundleWorkspace(dirs.workspaceDir, wsStageName); err != nil {
		if errors.Is(err, errMoveBundleCommitCrash) {
			keepStagingOnCrash = true
		}
		job.markFailed(formatMoveBundleError("bundle import failed", err), "", st.now())
		return
	}

	// Commit home (provision-rendered files win; skip and count).
	skipped, err := commitMoveBundleHome(dirs.homeDir, homeStageName)
	if err != nil {
		if errors.Is(err, errMoveBundleCommitCrash) {
			keepStagingOnCrash = true
		}
		job.markFailed(formatMoveBundleError("bundle import failed", err), "", st.now())
		return
	}

	if err := moveBundleCommitHook("before_marker"); err != nil {
		if errors.Is(err, errMoveBundleCommitCrash) {
			keepStagingOnCrash = true
		}
		job.markFailed(formatMoveBundleError("bundle import failed", err), "", st.now())
		return
	}

	// Write imported marker only after both roots commit.
	if err := writeMoveBundleImportedMarker(dirs.agentDir, req.ReincarnationID); err != nil {
		job.markFailed(formatMoveBundleError("bundle import failed", err), "", st.now())
		return
	}

	job.markSucceeded(nil, &api.MoveBundleImportSkipCounts{HomeProvisionWins: skipped}, st.now())
}

type moveBundlePartKey struct {
	root  string
	index int
}

// moveBundleURLPool caches signed GET URLs for all parts of a bundle import
// and refreshes them from the Hub when deadline-60s passes or when the object
// store answers 400/401/403.
type moveBundleURLPool struct {
	st      moveBundleSettings
	hubConn string
	agentID string
	rid     string

	mu                    sync.Mutex
	urls                  map[moveBundlePartKey]string
	deadline              time.Time
	idleRefreshes         int
	completedSinceRefresh int
}

func (p *moveBundleURLPool) getURL(ctx context.Context, rootName string, partIndex int) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.urls == nil || p.deadline.Sub(p.st.now()) < signedDownloadRefreshMargin {
		if err := p.refreshLocked(ctx, rootName, partIndex); err != nil {
			return "", err
		}
	}
	u, ok := p.urls[moveBundlePartKey{root: rootName, index: partIndex}]
	if !ok || u == "" {
		return "", &signedBundleTransferError{Stage: "download_urls", Root: rootName, PartIndex: partIndex, Class: signedClassInvalidEntry}
	}
	return u, nil
}

func (p *moveBundleURLPool) refreshOnAuthFailure(ctx context.Context, rootName string, partIndex int, failedURL string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	// If another goroutine already refreshed this URL while we were failing,
	// reuse the fresh URL directly.
	if cur, ok := p.urls[moveBundlePartKey{root: rootName, index: partIndex}]; ok && cur != "" && cur != failedURL && p.deadline.Sub(p.st.now()) >= signedDownloadRefreshMargin {
		return cur, nil
	}
	if err := p.refreshLocked(ctx, rootName, partIndex); err != nil {
		return "", err
	}
	u, ok := p.urls[moveBundlePartKey{root: rootName, index: partIndex}]
	if !ok || u == "" {
		return "", &signedBundleTransferError{Stage: "download_urls", Root: rootName, PartIndex: partIndex, Class: signedClassInvalidEntry}
	}
	return u, nil
}

func (p *moveBundleURLPool) markCompleted() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.completedSinceRefresh++
	p.idleRefreshes = 0
}

func (p *moveBundleURLPool) refreshLocked(ctx context.Context, rootName string, partIndex int) error {
	if p.urls != nil {
		if p.completedSinceRefresh == 0 {
			p.idleRefreshes++
		} else {
			p.idleRefreshes = 0
		}
		if p.idleRefreshes > signedDownloadMaxIdleRefreshes {
			return &signedBundleTransferError{Stage: "download_urls", Root: rootName, PartIndex: partIndex, Class: signedClassExpired}
		}
		p.completedSinceRefresh = 0
	}

	var resp *api.MoveBundleDownloadURLsResponse
	for attempt := 0; ; attempt++ {
		r, err := p.st.fetchDownloadURLs(ctx, p.hubConn, p.agentID, p.rid, &api.MoveBundleDownloadURLsRequest{})
		if err == nil {
			if r == nil {
				return &signedBundleTransferError{Stage: "download_urls", Root: rootName, PartIndex: partIndex, Class: signedClassDecode}
			}
			resp = r
			break
		}
		mapped, retryable := mapBundleURLError("download_urls", rootName, partIndex, err)
		if !retryable || ctx.Err() != nil || attempt >= len(p.st.urlBackoff) {
			if cerr := ctx.Err(); cerr != nil {
				return &signedBundleTransferError{Stage: "download_urls", Root: rootName, PartIndex: partIndex, Class: contextClass(cerr)}
			}
			return mapped
		}
		if serr := p.st.sleep(ctx, p.st.urlBackoff[attempt]); serr != nil {
			return &signedBundleTransferError{Stage: "download_urls", Root: rootName, PartIndex: partIndex, Class: contextClass(serr)}
		}
	}

	now := p.st.now()
	urls := make(map[moveBundlePartKey]string, len(resp.Parts))
	var earliest time.Time
	for _, part := range resp.Parts {
		if part.URL == "" {
			return &signedBundleTransferError{Stage: "download_urls", Root: part.Root, PartIndex: part.Index, Class: signedClassInvalidEntry}
		}
		urls[moveBundlePartKey{root: part.Root, index: part.Index}] = part.URL
		if !part.Expires.IsZero() && (earliest.IsZero() || part.Expires.Before(earliest)) {
			earliest = part.Expires
		}
	}
	var deadline time.Time
	switch {
	case resp.ExpiresInSeconds > 0:
		deadline = now.Add(time.Duration(resp.ExpiresInSeconds) * time.Second)
	case !resp.Expires.IsZero():
		deadline = resp.Expires
	default:
		deadline = earliest
	}
	p.urls = urls
	p.deadline = deadline
	return nil
}

// hubSignedPartSource implements bundle.PartSource using moveBundleURLPool
// and signedGetOnce with retries, stall timeout, URL refresh, and strict URL
// redaction.
type hubSignedPartSource struct {
	pool         *moveBundleURLPool
	st           moveBundleSettings
	rootName     string
	scratchDir   string
	stallTimeout time.Duration
	job          *moveBundleJob
}

func (s *hubSignedPartSource) Get(ctx context.Context, p bundle.Part) (io.ReadCloser, error) {
	targetURL, err := s.pool.getURL(ctx, s.rootName, p.Index)
	if err != nil {
		return nil, err
	}
	for attempt := 0; ; attempt++ {
		rc, status, class, retryable := signedGetOnce(ctx, s.st.client, targetURL, s.scratchDir, p.Size, s.stallTimeout)
		if class == "" {
			s.pool.markCompleted()
			s.job.addProgress(p.Size, 1)
			return rc, nil
		}
		if status == http.StatusBadRequest || status == http.StatusUnauthorized || status == http.StatusForbidden {
			freshURL, refErr := s.pool.refreshOnAuthFailure(ctx, s.rootName, p.Index, targetURL)
			if refErr != nil {
				return nil, refErr
			}
			targetURL = freshURL
			attempt = -1 // fresh URL gets its own retry budget
			continue
		}
		if !retryable || ctx.Err() != nil || attempt >= len(s.st.partBackoff) {
			if cerr := ctx.Err(); cerr != nil {
				return nil, &signedBundleTransferError{Stage: "get_part", Root: s.rootName, PartIndex: p.Index, Class: contextClass(cerr)}
			}
			return nil, &signedBundleTransferError{Stage: "get_part", Root: s.rootName, PartIndex: p.Index, Status: status, Class: class}
		}
		if serr := s.st.sleep(ctx, s.st.partBackoff[attempt]); serr != nil {
			return nil, &signedBundleTransferError{Stage: "get_part", Root: s.rootName, PartIndex: p.Index, Class: contextClass(serr)}
		}
	}
}

// formatMoveBundleError formats a job failure message while guaranteeing that
// no signed URL or raw *url.Error ever reaches the status error string.
func formatMoveBundleError(prefix string, err error) string {
	var transferErr *signedBundleTransferError
	if errors.As(err, &transferErr) {
		return prefix + ": " + transferErr.Error()
	}
	var limErr *bundle.ErrLimitExceeded
	if errors.As(err, &limErr) {
		return limErr.Error()
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return prefix + ": timed out"
	}
	if errors.Is(err, context.Canceled) {
		return prefix + ": canceled"
	}
	return prefix + ": " + err.Error()
}
