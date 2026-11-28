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

package normalize

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/telemetrycontract"
	"github.com/GoogleCloudPlatform/scion/pkg/trajectory/shipper"
)

// Engine executes Track 2 L1 normalization rounds (contract §4–§5).
type Engine struct {
	cfg Config
}

// New validates cfg and constructs an Engine.
func New(cfg Config) (*Engine, error) {
	if cfg.HomeDir == "" {
		return nil, errors.New("normalize: HomeDir is required")
	}
	if cfg.Harness == "" {
		return nil, errors.New("normalize: Harness is required")
	}
	if err := telemetrycontract.ValidateParserVersion(cfg.ParserVersion); err != nil {
		return nil, fmt.Errorf("normalize: %w", err)
	}
	if cfg.RunID == "" {
		return nil, errors.New("normalize: RunID is required")
	}
	if cfg.Executor == nil {
		return nil, errors.New("normalize: Executor is required")
	}
	if cfg.Uploader == nil {
		return nil, errors.New("normalize: Uploader is required")
	}
	if cfg.StateLockTimeout <= 0 {
		cfg.StateLockTimeout = DefaultStateLockTimeout
	}
	if cfg.PoisonThreshold <= 0 {
		cfg.PoisonThreshold = DefaultPoisonThreshold
	}
	if cfg.MaxATIFBytes <= 0 {
		cfg.MaxATIFBytes = DefaultMaxATIFBytes
	}
	if cfg.SoftMaxRSSBytes <= 0 {
		cfg.SoftMaxRSSBytes = DefaultSoftMaxRSSBytes
	}
	if cfg.Now == nil {
		cfg.Now = func() time.Time { return time.Now().UTC() }
	}
	return &Engine{cfg: cfg}, nil
}

type selectedArtifact struct {
	key          string
	absPath      string
	kind         string
	generation   int
	parsedOffset int64
	endOffset    int64
	fileSize     int64
	copyPath     string
	copyBytes    []byte
	resetGen     bool
}

type trackedArtifact struct {
	key          string
	absPath      string
	kind         string
	generation   int
	parsedOffset int64
	rawCommitted int64
	skip         bool
	resetGen     bool
}

// RunRound executes a single L1 normalization round per contract §4–§5.
func (e *Engine) RunRound(ctx context.Context, req RoundRequest) (RoundResult, error) {
	if req.Mode == telemetrycontract.TrajectoryCapturePolicyOff || req.Mode == "" {
		return RoundResult{Skipped: true, SkipReason: "capture_off"}, nil
	}
	if req.Mode != telemetrycontract.TrajectoryCapturePolicyFull && req.Mode != telemetrycontract.TrajectoryCapturePolicyMetadataOnly {
		return RoundResult{}, fmt.Errorf("normalize: unsupported mode %q", req.Mode)
	}
	if req.Reason == shipper.ReasonPreStop {
		return RoundResult{Skipped: true, SkipReason: "pre_stop"}, nil
	}

	// Step 1: Snapshot shipper + normalizer state under the lock (read-only;
	// never hold the lock while running the normalizer subprocess or uploading).
	var (
		normState    *NormalizerState
		tracked      []trackedArtifact
		genResetKeys []string
	)
	if err := shipper.WithLockedState(ctx, e.cfg.HomeDir, e.cfg.StateLockTimeout, func(st *shipper.State) (bool, error) {
		ns, err := loadNormalizerState(st, e.cfg.RunID, e.cfg.ParserVersion)
		if err != nil {
			return false, err
		}
		normState = ns

		switch req.Mode {
		case telemetrycontract.TrajectoryCapturePolicyFull:
			for _, key := range sortedKeys(st.Artifacts) {
				art := st.Artifacts[key]
				if art == nil {
					continue
				}
				ta := trackedArtifact{
					key:          key,
					absPath:      filepath.Join(e.cfg.HomeDir, filepath.FromSlash(key)),
					kind:         string(art.ArtifactKind),
					generation:   art.Generation,
					parsedOffset: art.ParsedOffset,
					rawCommitted: art.RawCommittedOffset,
				}
				if ta.kind == "" {
					ta.kind = telemetrycontract.TrajectoryArtifactKindJSONLAppend
				}
				if art.Rejected || art.NeedsBaseline {
					ta.skip = true
				}
				if normArt := ns.Artifacts[key]; normArt != nil {
					if normArt.Generation != art.Generation {
						ta.resetGen = true
						genResetKeys = append(genResetKeys, key)
					} else if normArt.ParseFailed && normArt.ParseFailedOffset == art.ParsedOffset {
						ta.skip = true
					}
				}
				tracked = append(tracked, ta)
			}

		case telemetrycontract.TrajectoryCapturePolicyMetadataOnly:
			spec, ok := shipper.HarnessSpecFor(e.cfg.Harness)
			if !ok {
				return false, fmt.Errorf("normalize: unsupported harness %q", e.cfg.Harness)
			}
			disc, err := shipper.Discover(e.cfg.HomeDir, spec)
			if err != nil {
				return false, fmt.Errorf("normalize: discover artifacts: %w", err)
			}
			for _, da := range disc.Artifacts {
				ta := trackedArtifact{
					key:     da.NativeRelativePath,
					absPath: da.AbsPath,
					kind:    string(da.Kind),
				}
				if art := st.Artifacts[da.NativeRelativePath]; art != nil {
					ta.generation = art.Generation
					ta.parsedOffset = art.ParsedOffset
				}
				if normArt := ns.Artifacts[da.NativeRelativePath]; normArt != nil {
					if normArt.Generation != ta.generation {
						ta.resetGen = true
						genResetKeys = append(genResetKeys, da.NativeRelativePath)
					} else if normArt.ParseFailed && normArt.ParseFailedOffset == ta.parsedOffset {
						ta.skip = true
					}
				}
				tracked = append(tracked, ta)
			}
		}
		return false, nil
	}); err != nil {
		return RoundResult{}, err
	}

	// Step 2: Inspect native files on disk to compute line-boundary end offsets
	// and track whether any artifact has unparsed bytes beyond end_offset.
	var (
		selected    []selectedArtifact
		fileSizes   = make(map[string]int64, len(tracked))
		prevStepIDs = make(map[string]int)
		genResetSet = make(map[string]bool, len(genResetKeys))
	)
	for _, k := range genResetKeys {
		genResetSet[k] = true
	}
	for key, na := range normState.Artifacts {
		if genResetSet[key] || na == nil {
			continue
		}
		if na.SessionID != "" && na.LastStepID > prevStepIDs[na.SessionID] {
			prevStepIDs[na.SessionID] = na.LastStepID
		}
	}

	for _, ta := range tracked {
		f, err := os.Open(ta.absPath)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return RoundResult{}, fmt.Errorf("normalize: open %s: %w", ta.key, err)
		}
		fi, statErr := f.Stat()
		if statErr != nil {
			_ = f.Close()
			return RoundResult{}, fmt.Errorf("normalize: stat %s: %w", ta.key, statErr)
		}
		fileSize := fi.Size()
		fileSizes[ta.key] = fileSize

		if ta.skip {
			_ = f.Close()
			continue
		}

		limit := fileSize
		if req.Mode == telemetrycontract.TrajectoryCapturePolicyFull && ta.rawCommitted < limit {
			limit = ta.rawCommitted
		}
		if limit <= ta.parsedOffset {
			_ = f.Close()
			continue
		}

		endOffset, err := findLineBoundaryEnd(f, ta.parsedOffset, limit)
		if err != nil {
			_ = f.Close()
			return RoundResult{}, fmt.Errorf("normalize: find line boundary for %s: %w", ta.key, err)
		}
		if endOffset <= ta.parsedOffset {
			_ = f.Close()
			continue
		}

		byteLen := endOffset - ta.parsedOffset
		buf := make([]byte, byteLen)
		if _, err := io.ReadFull(io.NewSectionReader(f, ta.parsedOffset, byteLen), buf); err != nil {
			_ = f.Close()
			return RoundResult{}, fmt.Errorf("normalize: read %s [%d, %d): %w", ta.key, ta.parsedOffset, endOffset, err)
		}
		_ = f.Close()

		selected = append(selected, selectedArtifact{
			key:          ta.key,
			absPath:      ta.absPath,
			kind:         ta.kind,
			generation:   ta.generation,
			parsedOffset: ta.parsedOffset,
			endOffset:    endOffset,
			fileSize:     fileSize,
			copyBytes:    buf,
			resetGen:     ta.resetGen,
		})
	}

	if len(selected) == 0 {
		// If there were generation resets on artifacts with no new bytes yet,
		// still clean up their stale normalizer/ATIF entries.
		if len(genResetKeys) > 0 {
			_ = e.applyGenerationResets(ctx, genResetKeys)
		}
		return RoundResult{Skipped: true, SkipReason: "no_unparsed_lines"}, nil
	}

	// Step 3: Materialize read-only round copies under a private temp dir
	// (0444 files in a 0500 dir, cleaned up after the round).
	roundDir, err := e.materializeRoundCopies(selected)
	if err != nil {
		return RoundResult{}, err
	}
	defer cleanupRoundDir(roundDir)

	// Step 4: Build protocol Request and invoke Executor.
	reqArtifacts := make([]RequestArtifact, len(selected))
	bounds := make(map[string]artifactBound, len(selected))
	for i, sa := range selected {
		reqArtifacts[i] = RequestArtifact{
			Key:            sa.key,
			Path:           sa.copyPath,
			Kind:           sa.kind,
			Generation:     sa.generation,
			ParsedOffset:   sa.parsedOffset,
			EndOffset:      sa.endOffset,
			FileOffsetBase: sa.parsedOffset,
		}
		bounds[sa.key] = artifactBound{
			key:          sa.key,
			generation:   sa.generation,
			parsedOffset: sa.parsedOffset,
			endOffset:    sa.endOffset,
			copyBytes:    sa.copyBytes,
		}
	}

	protoReq := Request{
		InterfaceVersion: telemetrycontract.NormalizerInterfaceVersion,
		Harness:          e.cfg.Harness,
		HarnessVersion:   e.cfg.HarnessVersion,
		ParserVersion:    e.cfg.ParserVersion,
		Mode:             req.Mode,
		Run: RequestRun{
			RunID:   e.cfg.RunID,
			AgentID: e.cfg.AgentID,
		},
		Artifacts: reqArtifacts,
		State:     normState.OpaqueState,
	}

	execRes, execErr := e.cfg.Executor.Execute(ctx, protoReq)
	if execErr != nil || execRes.ExitCode != 0 {
		reason := formatExecFailure(execRes, execErr)
		poisoned, pErr := e.recordParseFailure(ctx, selected, reason)
		res := RoundResult{
			ArtifactsProcessed: len(selected),
			PeakRSSBytes:       execRes.PeakRSSBytes,
			RSSBudgetExceeded:  execRes.PeakRSSBytes > e.cfg.SoftMaxRSSBytes,
			PoisonedArtifacts:  poisoned,
		}
		if pErr != nil {
			return res, fmt.Errorf("%w: %s (also failed to record failure: %v)", ErrParseFailure, reason, pErr)
		}
		return res, fmt.Errorf("%w: %s", ErrParseFailure, reason)
	}

	// Step 5: Parse & validate normalizer stdout framing.
	parsed, parseErr := parseNormalizerOutput(execRes.StdoutLines, e.cfg.Harness, bounds, prevStepIDs)
	if parseErr != nil {
		poisoned, pErr := e.recordParseFailure(ctx, selected, parseErr.Error())
		res := RoundResult{
			ArtifactsProcessed: len(selected),
			PeakRSSBytes:       execRes.PeakRSSBytes,
			RSSBudgetExceeded:  execRes.PeakRSSBytes > e.cfg.SoftMaxRSSBytes,
			PoisonedArtifacts:  poisoned,
		}
		if pErr != nil {
			return res, fmt.Errorf("%w (also failed to record failure: %v)", parseErr, pErr)
		}
		return res, parseErr
	}

	// Step 6: Per-round dedupe merge, version gate, and enrichment.
	deduped, mergedCount := mergeUsageRecords(parsed.usageRecords)
	unverifiedVersion := versionGateUnverified(e.cfg.HarnessVersion, e.cfg.TestedHarnessVersions)
	captureTS := e.cfg.Now()

	enriched := make([]telemetrycontract.UsageRecord, len(deduped))
	for i, rec := range deduped {
		if unverifiedVersion {
			rec.TokenStatus = telemetrycontract.TokenStatusUnverified
		}
		enriched[i] = telemetrycontract.EnrichUsageRecord(rec, e.cfg.Harness, e.cfg.ParserVersion, captureTS)
	}

	// Determine which artifacts actually advanced their parsed_offset.
	var advancedRanges []ArtifactRange
	newOffsets := make(map[string]int64, len(selected))
	for _, sa := range selected {
		newOff := sa.parsedOffset
		if off, ok := parsed.parsedOffsets[sa.key]; ok {
			newOff = off
		}
		newOffsets[sa.key] = newOff
		if newOff > sa.parsedOffset {
			advancedRanges = append(advancedRanges, ArtifactRange{
				Key:        sa.key,
				Generation: sa.generation,
				Start:      sa.parsedOffset,
				End:        newOff,
			})
		}
	}

	res := RoundResult{
		ArtifactsProcessed:      len(selected),
		UsageRecordsEmitted:     len(enriched),
		UsageRecordsMerged:      mergedCount,
		StrippedDimensionFields: parsed.strippedDims,
		ParseErrorsDropped:      parsed.droppedTotal,
		PeakRSSBytes:            execRes.PeakRSSBytes,
		RSSBudgetExceeded:       execRes.PeakRSSBytes > e.cfg.SoftMaxRSSBytes,
	}

	// Contract §5: An empty round with no records and no offsets advanced
	// produces no chunk (and no inventory).
	if len(enriched) == 0 && len(advancedRanges) == 0 {
		// Still persist opaque state & reset failure counters if normalizer exited cleanly.
		if err := e.commitStateAdvance(ctx, req.Mode, selected, newOffsets, parsed, nil, genResetKeys, "", 0, "", 0); err != nil {
			return res, err
		}
		return res, nil
	}

	// Step 7: Build L1 objects in memory (do not mutate on-disk state or ATIF
	// accumulation files until all commits succeed).
	chunkRanges := advancedRanges
	if len(chunkRanges) == 0 {
		// Records emitted without offset advance (defensive fallback).
		for _, sa := range selected {
			chunkRanges = append(chunkRanges, ArtifactRange{
				Key:        sa.key,
				Generation: sa.generation,
				Start:      sa.parsedOffset,
				End:        sa.endOffset,
			})
		}
	}
	chunkID := DeterministicChunkID(e.cfg.RunID, e.cfg.ParserVersion, chunkRanges)
	chunkGZ, chunkSHA, err := buildUsageChunk(enriched)
	if err != nil {
		return res, err
	}

	type pendingATIFUpload struct {
		sessionID          string
		nativeRelativePath string
		parsedOffset       int64
		gzBytes            []byte
		shaHex             string
		meta               telemetrycontract.SessionMeta
		newSteps           []json.RawMessage
	}
	var atifUploads []pendingATIFUpload

	if req.Mode == telemetrycontract.TrajectoryCapturePolicyFull {
		for _, sid := range parsed.sessionOrder {
			meta, hasMeta := parsed.sessionMetas[sid]
			steps := parsed.atifSteps[sid]
			if !hasMeta && len(steps) == 0 {
				continue
			}
			relPath := meta.NativeRelativePath
			if relPath == "" && len(selected) == 1 {
				relPath = selected[0].key
				meta.NativeRelativePath = relPath
			}
			if relPath == "" {
				continue
			}
			meta.SessionID = sid

			// Load previously committed steps (unless generation was reset).
			var existingSteps []json.RawMessage
			if !genResetSet[relPath] {
				existingSteps, _, err = readATIFSteps(e.cfg.HomeDir, relPath, e.cfg.MaxATIFBytes)
				if err != nil {
					return res, err
				}
			}
			newRawSteps := make([]json.RawMessage, len(steps))
			for i, st := range steps {
				newRawSteps[i] = st.Raw
			}
			allSteps := make([]json.RawMessage, 0, len(existingSteps)+len(newRawSteps))
			allSteps = append(allSteps, existingSteps...)
			allSteps = append(allSteps, newRawSteps...)

			// Augment session_meta with parse errors, step count, status, and soft RSS warning.
			if pe := parsed.parseErrors[sid]; pe != nil {
				meta.ParseError = pe
			}
			meta.StepCount = len(allSteps)
			meta.NormalizeStatus = telemetrycontract.NormalizeStatusOK
			if res.RSSBudgetExceeded {
				if meta.Stats == nil {
					meta.Stats = make(map[string]any)
				}
				meta.Stats["peak_rss_bytes"] = execRes.PeakRSSBytes
				meta.Stats["rss_budget_exceeded"] = true
			}

			atifGZ, atifSHA, err := buildATIFObject(sid, meta, e.cfg.ParserVersion, allSteps)
			if err != nil {
				return res, err
			}
			artOff := newOffsets[relPath]
			atifUploads = append(atifUploads, pendingATIFUpload{
				sessionID:          sid,
				nativeRelativePath: relPath,
				parsedOffset:       artOff,
				gzBytes:            atifGZ,
				shaHex:             atifSHA,
				meta:               meta,
				newSteps:           newRawSteps,
			})
		}
	}

	// Build cumulative run_inventory.
	nextSeq := normState.InventorySeq + 1
	chunks := make([]telemetrycontract.RunInventoryChunk, len(normState.Chunks), len(normState.Chunks)+1)
	copy(chunks, normState.Chunks)
	chunks = append(chunks, telemetrycontract.RunInventoryChunk{
		ChunkID:          chunkID,
		ExpectedRowCount: len(enriched),
		SHA256:           chunkSHA,
	})

	isFinal := req.Reason == shipper.ReasonSessionEnd && !hasUnparsedBytes(tracked, fileSizes, newOffsets)
	inv := telemetrycontract.RunInventory{
		SchemaVersion: telemetrycontract.TrajectoryL1InventorySchemaVersion,
		RunID:         e.cfg.RunID,
		ParserVersion: e.cfg.ParserVersion,
		InventorySeq:  nextSeq,
		Chunks:        chunks,
		Final:         isFinal,
	}
	invBytes, invSHA, err := buildRunInventory(inv)
	if err != nil {
		return res, err
	}

	// Step 8: Upload & commit in strict order:
	//   1) usage_records chunk
	//   2) ATIF trajectories (full mode only)
	//   3) run_inventory
	// Any error returns immediately without advancing state.
	if err := e.uploadAndCommit(ctx, ArtifactPrepareRequest{
		ArtifactType:    ArtifactTypeUsageRecords,
		ParserVersion:   e.cfg.ParserVersion,
		Harness:         e.cfg.Harness,
		ChunkID:         chunkID,
		RowCount:        len(enriched),
		InventorySeq:    nextSeq,
		ContentEncoding: ContentEncodingGzip,
		Size:            int64(len(chunkGZ)),
		SHA256:          chunkSHA,
	}, chunkGZ, nil); err != nil {
		return res, fmt.Errorf("normalize: upload usage_records chunk %s: %w", chunkID, err)
	}

	for _, au := range atifUploads {
		metaCopy := au.meta
		if err := e.uploadAndCommit(ctx, ArtifactPrepareRequest{
			ArtifactType:       ArtifactTypeATIF,
			ParserVersion:      e.cfg.ParserVersion,
			Harness:            e.cfg.Harness,
			SessionID:          au.sessionID,
			NativeRelativePath: au.nativeRelativePath,
			ParsedOffset:       au.parsedOffset,
			ATIFSchemaVersion:  telemetrycontract.ATIFSchemaVersionV18,
			FileFormat:         telemetrycontract.ATIFFileFormatJSON,
			ContentEncoding:    ContentEncodingGzip,
			Size:               int64(len(au.gzBytes)),
			SHA256:             au.shaHex,
		}, au.gzBytes, &metaCopy); err != nil {
			return res, fmt.Errorf("normalize: upload atif for session %s: %w", au.sessionID, err)
		}
	}

	if err := e.uploadAndCommit(ctx, ArtifactPrepareRequest{
		ArtifactType:    ArtifactTypeRunInventory,
		ParserVersion:   e.cfg.ParserVersion,
		Harness:         e.cfg.Harness,
		InventorySeq:    nextSeq,
		Final:           isFinal,
		ContentEncoding: ContentEncodingIdentity,
		Size:            int64(len(invBytes)),
		SHA256:          invSHA,
	}, invBytes, nil); err != nil {
		return res, fmt.Errorf("normalize: upload run_inventory seq %d: %w", nextSeq, err)
	}

	// Step 9: All commits returned 200. Advance shipper state + persist opaque
	// state + append ATIF accumulation files in a single WithLockedState call.
	atifAppends := make(map[string][]json.RawMessage, len(atifUploads))
	for _, au := range atifUploads {
		if len(au.newSteps) > 0 {
			atifAppends[au.nativeRelativePath] = append(atifAppends[au.nativeRelativePath], au.newSteps...)
		}
	}
	if err := e.commitStateAdvance(
		ctx,
		req.Mode,
		selected,
		newOffsets,
		parsed,
		atifAppends,
		genResetKeys,
		chunkID,
		len(enriched),
		chunkSHA,
		nextSeq,
	); err != nil {
		return res, err
	}

	res.ChunkID = chunkID
	res.InventorySeq = nextSeq
	res.InventoryFinal = isFinal
	res.ATIFSessionsUploaded = len(atifUploads)
	return res, nil
}

func (e *Engine) uploadAndCommit(
	ctx context.Context,
	prepReq ArtifactPrepareRequest,
	content []byte,
	sessionMeta *telemetrycontract.SessionMeta,
) error {
	prepResp, err := e.cfg.Uploader.Prepare(ctx, prepReq)
	if err != nil {
		return err
	}
	if prepResp.State == ArtifactStateCommitted {
		// Idempotent replay: already committed on the Hub.
		return nil
	}
	if prepResp.State != ArtifactStateUploadRequired {
		return fmt.Errorf("unexpected prepare state %q", prepResp.State)
	}
	if prepResp.Upload == nil {
		return errors.New("prepare returned upload_required without upload target")
	}
	if err := e.cfg.Uploader.Upload(ctx, *prepResp.Upload, content); err != nil {
		return err
	}
	_, err = e.cfg.Uploader.Commit(ctx, ArtifactCommitRequest{
		ArtifactID:  prepResp.ArtifactID,
		SessionMeta: sessionMeta,
	})
	return err
}

func (e *Engine) commitStateAdvance(
	ctx context.Context,
	mode string,
	selected []selectedArtifact,
	newOffsets map[string]int64,
	parsed *parsedOutput,
	atifAppends map[string][]json.RawMessage,
	genResetKeys []string,
	chunkID string,
	chunkRows int,
	chunkSHA string,
	nextSeq int,
) error {
	return shipper.WithLockedState(ctx, e.cfg.HomeDir, e.cfg.StateLockTimeout, func(st *shipper.State) (bool, error) {
		ns, err := loadNormalizerState(st, e.cfg.RunID, e.cfg.ParserVersion)
		if err != nil {
			return false, err
		}

		// Clean up generation-reset entries and old ATIF accumulation files.
		for _, key := range genResetKeys {
			delete(ns.Artifacts, key)
			delete(ns.ConsecutiveFailures, key)
			removeATIFSteps(e.cfg.HomeDir, key)
		}

		// Append new ATIF steps to on-disk accumulation files.
		for _, relPath := range sortedKeys(atifAppends) {
			if err := appendATIFSteps(e.cfg.HomeDir, relPath, atifAppends[relPath], e.cfg.MaxATIFBytes); err != nil {
				return false, err
			}
		}

		// Map native_relative_path -> session_id from sessionMetas.
		pathToSession := make(map[string]string, len(parsed.sessionMetas))
		for sid, sm := range parsed.sessionMetas {
			if sm.NativeRelativePath != "" {
				pathToSession[sm.NativeRelativePath] = sid
			}
		}

		for _, sa := range selected {
			newOff := newOffsets[sa.key]
			art := st.Artifacts[sa.key]
			if art == nil && mode == telemetrycontract.TrajectoryCapturePolicyMetadataOnly {
				art = &shipper.ArtifactState{
					NativeRelativePath: sa.key,
					ArtifactKind:       shipper.ArtifactKind(sa.kind),
					Generation:         sa.generation,
				}
				st.Artifacts[sa.key] = art
			}
			if art != nil && art.Generation == sa.generation && newOff > art.ParsedOffset {
				art.ParsedOffset = newOff
			}

			// Clear consecutive failure counter on success.
			delete(ns.ConsecutiveFailures, sa.key)

			na := ns.Artifacts[sa.key]
			if na == nil {
				na = &NormalizedArtifactState{}
				ns.Artifacts[sa.key] = na
			}
			na.Generation = sa.generation
			na.ParseFailed = false
			na.ParseFailedOffset = 0
			na.ParseFailedReported = false
			na.LastError = ""
			if sid := pathToSession[sa.key]; sid != "" {
				na.SessionID = sid
				if lastID, ok := parsed.finalStepIDs[sid]; ok {
					na.LastStepID = lastID
				}
			}
		}

		ns.OpaqueState = parsed.state
		if chunkID != "" {
			ns.InventorySeq = nextSeq
			ns.Chunks = append(ns.Chunks, telemetrycontract.RunInventoryChunk{
				ChunkID:          chunkID,
				ExpectedRowCount: chunkRows,
				SHA256:           chunkSHA,
			})
		}

		if err := saveNormalizerState(st, ns); err != nil {
			return false, err
		}
		return true, nil
	})
}

func (e *Engine) applyGenerationResets(ctx context.Context, genResetKeys []string) error {
	return shipper.WithLockedState(ctx, e.cfg.HomeDir, e.cfg.StateLockTimeout, func(st *shipper.State) (bool, error) {
		ns, err := loadNormalizerState(st, e.cfg.RunID, e.cfg.ParserVersion)
		if err != nil {
			return false, err
		}
		for _, key := range genResetKeys {
			delete(ns.Artifacts, key)
			delete(ns.ConsecutiveFailures, key)
			removeATIFSteps(e.cfg.HomeDir, key)
		}
		if err := saveNormalizerState(st, ns); err != nil {
			return false, err
		}
		return true, nil
	})
}

func (e *Engine) recordParseFailure(ctx context.Context, selected []selectedArtifact, reason string) ([]string, error) {
	var newlyPoisoned []selectedArtifact

	err := shipper.WithLockedState(ctx, e.cfg.HomeDir, e.cfg.StateLockTimeout, func(st *shipper.State) (bool, error) {
		ns, err := loadNormalizerState(st, e.cfg.RunID, e.cfg.ParserVersion)
		if err != nil {
			return false, err
		}
		for _, sa := range selected {
			ft := ns.ConsecutiveFailures[sa.key]
			if ft != nil &&
				ft.Generation == sa.generation &&
				ft.ParserVersion == e.cfg.ParserVersion &&
				ft.ParsedOffset == sa.parsedOffset {
				ft.Count++
				ft.LastError = reason
			} else {
				ft = &FailureTracker{
					Generation:    sa.generation,
					ParserVersion: e.cfg.ParserVersion,
					ParsedOffset:  sa.parsedOffset,
					Count:         1,
					LastError:     reason,
				}
				ns.ConsecutiveFailures[sa.key] = ft
			}

			if ft.Count >= e.cfg.PoisonThreshold {
				na := ns.Artifacts[sa.key]
				if na == nil {
					na = &NormalizedArtifactState{}
					ns.Artifacts[sa.key] = na
				}
				wasAlreadyPoisoned := na.ParseFailed && na.Generation == sa.generation && na.ParseFailedOffset == sa.parsedOffset
				na.Generation = sa.generation
				na.ParseFailed = true
				na.ParseFailedOffset = sa.parsedOffset
				na.LastError = reason
				if !wasAlreadyPoisoned || !na.ParseFailedReported {
					newlyPoisoned = append(newlyPoisoned, sa)
				}
			}
		}
		if err := saveNormalizerState(st, ns); err != nil {
			return false, err
		}
		return true, nil
	})
	if err != nil {
		return nil, err
	}

	if len(newlyPoisoned) == 0 {
		return nil, nil
	}

	// Report parse_failed to the Hub outside the state lock (contract §4).
	poisonedKeys := make([]string, 0, len(newlyPoisoned))
	reportedKeys := make([]string, 0, len(newlyPoisoned))
	for _, sa := range newlyPoisoned {
		poisonedKeys = append(poisonedKeys, sa.key)
		report := ParseFailedReport{
			Harness:       e.cfg.Harness,
			ParserVersion: e.cfg.ParserVersion,
			SessionMeta: telemetrycontract.SessionMeta{
				NativeRelativePath: sa.key,
				NormalizeStatus:    telemetrycontract.NormalizeStatusParseFailed,
				ParseError: &telemetrycontract.ParseErrorSummary{
					Count:      e.cfg.PoisonThreshold,
					FirstError: reason,
				},
			},
		}
		if repErr := e.cfg.Uploader.ReportParseFailed(ctx, report); repErr == nil {
			reportedKeys = append(reportedKeys, sa.key)
		}
	}

	if len(reportedKeys) > 0 {
		_ = shipper.WithLockedState(ctx, e.cfg.HomeDir, e.cfg.StateLockTimeout, func(st *shipper.State) (bool, error) {
			ns, err := loadNormalizerState(st, e.cfg.RunID, e.cfg.ParserVersion)
			if err != nil {
				return false, err
			}
			for _, k := range reportedKeys {
				if na := ns.Artifacts[k]; na != nil {
					na.ParseFailedReported = true
				}
			}
			if err := saveNormalizerState(st, ns); err != nil {
				return false, err
			}
			return true, nil
		})
	}

	sort.Strings(poisonedKeys)
	return poisonedKeys, nil
}

func (e *Engine) materializeRoundCopies(selected []selectedArtifact) (string, error) {
	baseDir := e.cfg.TempBaseDir
	if baseDir == "" {
		baseDir = os.TempDir()
	}
	if err := os.MkdirAll(baseDir, 0o700); err != nil {
		return "", fmt.Errorf("normalize: mkdir temp base %s: %w", baseDir, err)
	}
	roundDir, err := os.MkdirTemp(baseDir, "scion-norm-*")
	if err != nil {
		return "", fmt.Errorf("normalize: create round temp dir: %w", err)
	}

	for i := range selected {
		ext := filepath.Ext(selected[i].key)
		if ext == "" {
			ext = ".dat"
		}
		dst := filepath.Join(roundDir, fmt.Sprintf("%d%s", i, ext))
		if err := os.WriteFile(dst, selected[i].copyBytes, 0o600); err != nil {
			cleanupRoundDir(roundDir)
			return "", fmt.Errorf("normalize: write round copy %s: %w", dst, err)
		}
		if err := os.Chmod(dst, 0o444); err != nil {
			cleanupRoundDir(roundDir)
			return "", fmt.Errorf("normalize: chmod round copy %s: %w", dst, err)
		}
		selected[i].copyPath = dst
	}

	if err := os.Chmod(roundDir, 0o500); err != nil {
		cleanupRoundDir(roundDir)
		return "", fmt.Errorf("normalize: chmod round dir %s: %w", roundDir, err)
	}
	return roundDir, nil
}

func cleanupRoundDir(roundDir string) {
	if roundDir == "" {
		return
	}
	_ = os.Chmod(roundDir, 0o700)
	_ = os.RemoveAll(roundDir)
}

func hasUnparsedBytes(tracked []trackedArtifact, fileSizes map[string]int64, newOffsets map[string]int64) bool {
	for _, ta := range tracked {
		sz, ok := fileSizes[ta.key]
		if !ok {
			continue
		}
		off := ta.parsedOffset
		if updated, ok := newOffsets[ta.key]; ok {
			off = updated
		}
		if sz > off {
			return true
		}
	}
	return false
}

func formatExecFailure(res ExecResult, err error) string {
	stderr := truncateStderr(res.Stderr)
	if err != nil {
		if stderr != "" {
			return fmt.Sprintf("exec error: %v (stderr: %s)", err, stderr)
		}
		return fmt.Sprintf("exec error: %v", err)
	}
	if stderr != "" {
		return fmt.Sprintf("exit code %d (stderr: %s)", res.ExitCode, stderr)
	}
	return fmt.Sprintf("exit code %d", res.ExitCode)
}
