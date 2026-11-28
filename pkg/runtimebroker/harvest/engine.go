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

package harvest

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/sciontool/telemetry"
	"github.com/GoogleCloudPlatform/scion/pkg/telemetrycontract"
	"github.com/GoogleCloudPlatform/scion/pkg/trajectory/shipper"
	"github.com/GoogleCloudPlatform/scion/pkg/util/filelock"
	"github.com/GoogleCloudPlatform/scion/pkg/util/pathcanon"
	"github.com/GoogleCloudPlatform/scion/pkg/util/privatefs"
	"github.com/google/uuid"
)

const (
	// DefaultAttemptTimeout bounds each harvest attempt (contract §5).
	DefaultAttemptTimeout = 60 * time.Second

	// defaultReportPostTimeout bounds the best-effort harvest report POST when
	// the main harvest attempt context has already expired.
	defaultReportPostTimeout = 5 * time.Second

	// maxReportErrorBytes is the maximum byte length of Report.Error (contract §3).
	maxReportErrorBytes = 1024
)

// Config configures a broker harvest Engine.
type Config struct {
	// Hub delivers trajectory segments, usage batches, and harvest reports.
	Hub HubClient
	// HubID is the verified Hub identifier for outbox hub_id checks.
	HubID string
	// AttemptTimeout bounds a single Harvest call (defaults to DefaultAttemptTimeout).
	AttemptTimeout time.Duration
	// LockTimeout bounds how long the shipper waits for state.lock (defaults to shipper.DefaultLockTimeout).
	LockTimeout time.Duration
	// Logger receives harvest diagnostics (defaults to slog.Default()).
	Logger *slog.Logger
	// Now overrides time.Now for tests.
	Now func() time.Time
}

// Engine executes bounded, per-agent-serialized, idempotent broker harvests.
type Engine struct {
	hub            HubClient
	hubID          string
	attemptTimeout time.Duration
	lockTimeout    time.Duration
	log            *slog.Logger
	now            func() time.Time

	mu                  sync.Mutex
	agentLocks          map[string]*sync.Mutex
	unavailableReported map[string]struct{}
}

// New constructs a broker harvest Engine.
func New(cfg Config) *Engine {
	timeout := cfg.AttemptTimeout
	if timeout <= 0 {
		timeout = DefaultAttemptTimeout
	}
	lockTimeout := cfg.LockTimeout
	if lockTimeout <= 0 {
		lockTimeout = shipper.DefaultLockTimeout
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	nowFn := cfg.Now
	if nowFn == nil {
		nowFn = time.Now
	}
	return &Engine{
		hub:                 cfg.Hub,
		hubID:               cfg.HubID,
		attemptTimeout:      timeout,
		lockTimeout:         lockTimeout,
		log:                 logger,
		now:                 nowFn,
		agentLocks:          make(map[string]*sync.Mutex),
		unavailableReported: make(map[string]struct{}),
	}
}

func (e *Engine) lockForAgent(key string) *sync.Mutex {
	e.mu.Lock()
	defer e.mu.Unlock()
	m, ok := e.agentLocks[key]
	if !ok {
		m = &sync.Mutex{}
		e.agentLocks[key] = m
	}
	return m
}

func (e *Engine) agentKey(t Target) string {
	if t.AgentID != "" {
		return t.AgentID
	}
	return t.ProjectID + "/" + t.Slug
}

func (e *Engine) markUnavailableReported(agentKey, runID string, trigger Trigger) bool {
	k := agentKey + "|" + runID + "|" + string(trigger)
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, exists := e.unavailableReported[k]; exists {
		return false
	}
	e.unavailableReported[k] = struct{}{}
	return true
}

// Harvest runs a single bounded harvest for target and trigger, POSTs the
// resulting Report to the Hub (best effort), and returns the Report.
//
// Harvest never creates files or directories in a home that lacks them, never
// invents a shipper baseline, never steals a lock, and never executes Python.
func (e *Engine) Harvest(ctx context.Context, target Target, trigger Trigger) Report {
	started := e.now().UTC()
	report := Report{
		HarvestID: uuid.NewString(),
		RunID:     target.RunID,
		Trigger:   string(trigger),
		StartedAt: started.Format(time.RFC3339),
	}

	if e == nil {
		report.Outcome = OutcomeUnavailable
		report.Trajectory = TrajectorySkipped
		report.Usage = UsageSkipped
		report.ErrorCode = ErrCodeHomeUnreadable
		report.FinishedAt = time.Now().UTC().Format(time.RFC3339)
		return report
	}

	agentKey := e.agentKey(target)
	agentMu := e.lockForAgent(agentKey)
	agentMu.Lock()
	defer agentMu.Unlock()

	attemptCtx, cancel := context.WithTimeout(ctx, e.attemptTimeout)
	defer cancel()

	// Check runtime readability and home existence/readability.
	if !target.HomeReadable || strings.TrimSpace(target.HomeDir) == "" {
		report.Outcome = OutcomeUnavailable
		report.Trajectory = TrajectorySkipped
		report.Usage = UsageSkipped
		report.ErrorCode = ErrCodeHomeUnreadable
		if !target.HomeReadable {
			report.Error = fmt.Sprintf("runtime %q home is not readable by broker", target.RuntimeName)
		} else {
			report.Error = "agent home directory could not be resolved"
		}
		report.FinishedAt = e.now().UTC().Format(time.RFC3339)
		if e.markUnavailableReported(agentKey, target.RunID, trigger) {
			e.postReportBestEffort(ctx, target.AgentID, report)
		}
		return report
	}

	homeInfo, statErr := os.Stat(target.HomeDir)
	if statErr != nil || !homeInfo.IsDir() {
		report.Outcome = OutcomeUnavailable
		report.Trajectory = TrajectorySkipped
		report.Usage = UsageSkipped
		report.ErrorCode = ErrCodeHomeUnreadable
		if statErr != nil {
			report.Error = sanitizeReportError(statErr.Error(), target.HomeDir)
		} else {
			report.Error = "agent home path is not a directory"
		}
		report.FinishedAt = e.now().UTC().Format(time.RFC3339)
		if e.markUnavailableReported(agentKey, target.RunID, trigger) {
			e.postReportBestEffort(ctx, target.AgentID, report)
		}
		return report
	}

	var (
		errCodes []string
		errMsgs  []string
		oldest   time.Time
	)
	recordErr := func(code, msg string) {
		if code != "" {
			errCodes = append(errCodes, code)
		}
		if msg != "" {
			errMsgs = append(errMsgs, sanitizeReportError(msg, target.HomeDir))
		}
	}
	updateOldest := func(t time.Time) {
		if t.IsZero() {
			return
		}
		if oldest.IsZero() || t.Before(oldest) {
			oldest = t
		}
	}

	// 1. Trajectory harvest (raw-only, never invents a baseline).
	trajPendingBytes, trajOldest, trajTruncated, trajCode, trajErrMsg := e.harvestTrajectory(attemptCtx, target, trigger, &report)
	report.PendingBytes += trajPendingBytes
	updateOldest(trajOldest)
	recordErr(trajCode, trajErrMsg)

	// 2. Usage outbox drain (always runs even when trajectory=no_state/skipped/refused).
	usagePendingBytes, usageOldest, usageCode, usageErrMsg := e.harvestUsage(attemptCtx, target, &report)
	report.PendingBytes += usagePendingBytes
	updateOldest(usageOldest)
	recordErr(usageCode, usageErrMsg)

	if !oldest.IsZero() {
		ts := oldest.UTC().Format(time.RFC3339)
		report.OldestPendingAt = &ts
	}
	if len(errCodes) > 0 {
		report.ErrorCode = errCodes[0]
	}
	if len(errMsgs) > 0 {
		report.Error = sanitizeReportError(strings.Join(errMsgs, "; "), target.HomeDir)
	}

	// 3. Classify overall outcome per contract §3 & §5.
	shippedSomething := report.SegmentsCommitted > 0 || report.UsageEventsDrained > 0
	hasRemaining := report.PendingBytes > 0 || report.UsageEventsPending > 0 || trajTruncated
	hasError := report.Trajectory == TrajectoryError || report.Usage == UsageError

	switch {
	case trajTruncated:
		report.Outcome = OutcomePartial
	case hasError || hasRemaining:
		if shippedSomething {
			report.Outcome = OutcomePartial
		} else {
			report.Outcome = OutcomeFailed
		}
	case shippedSomething:
		report.Outcome = OutcomeComplete
	default:
		report.Outcome = OutcomeNothingPending
	}

	report.FinishedAt = e.now().UTC().Format(time.RFC3339)
	e.postReportBestEffort(ctx, target.AgentID, report)
	return report
}

func (e *Engine) harvestTrajectory(
	ctx context.Context,
	target Target,
	trigger Trigger,
	report *Report,
) (pendingBytes int64, oldest time.Time, truncated bool, errCode string, errMsg string) {
	harness := strings.TrimSpace(target.Harness)
	if harness != "" {
		if _, ok := shipper.HarnessSpecFor(harness); !ok {
			report.Trajectory = TrajectorySkipped
			return 0, time.Time{}, false, "", ""
		}
	}

	// Never invent a baseline or create .scion/trajectory-shipper/ if absent.
	st, exists, err := shipper.LoadState(target.HomeDir)
	if err != nil {
		report.Trajectory = TrajectoryError
		return 0, time.Time{}, false, classifyShipperError(err), err.Error()
	}
	if !exists || st == nil || !st.Baselined {
		report.Trajectory = TrajectoryNoState
		return 0, time.Time{}, false, "", ""
	}

	if harness == "" {
		harness = strings.TrimSpace(st.Harness)
	}
	if harness == "" {
		report.Trajectory = TrajectorySkipped
		return 0, time.Time{}, false, "", ""
	}
	spec, ok := shipper.HarnessSpecFor(harness)
	if !ok {
		report.Trajectory = TrajectorySkipped
		return 0, time.Time{}, false, "", ""
	}

	if target.RunID == "" {
		pb, ot := estimateTrajectoryPending(target.HomeDir, spec, st)
		report.Trajectory = TrajectoryError
		return pb, ot, false, ErrCodeMissingRunID, "run_id is required for broker trajectory harvest"
	}

	if e.hub == nil {
		pb, ot := estimateTrajectoryPending(target.HomeDir, spec, st)
		report.Trajectory = TrajectoryError
		return pb, ot, false, ErrCodeHubUnavailable, "hub client not configured"
	}

	lockTimeout := e.lockTimeout
	if deadline, ok := ctx.Deadline(); ok {
		if rem := time.Until(deadline); rem > 0 && rem < lockTimeout {
			lockTimeout = rem
		}
	}

	sh, err := shipper.New(shipper.Config{
		HomeDir:     target.HomeDir,
		RunID:       target.RunID,
		Harness:     harness,
		OriginHubID: e.hubID,
		LockTimeout: lockTimeout,
		Uploader: &brokerUploader{
			hub:     e.hub,
			agentID: target.AgentID,
			runID:   target.RunID,
		},
		Now: e.now,
	})
	if err != nil {
		report.Trajectory = TrajectoryError
		return 0, time.Time{}, false, ErrCodeHomeUnreadable, err.Error()
	}

	var (
		totalSegments int
		totalBytes    int64
		policyRefused bool
		captureUnavail bool
		hadError      bool
		firstErr      error
	)

	reason := "broker_harvest_" + string(trigger)
	for {
		if err := ctx.Err(); err != nil {
			hadError = true
			if firstErr == nil {
				firstErr = err
			}
			break
		}
		res, rerr := sh.RunRound(ctx, shipper.RoundOptions{
			Final:  true,
			Reason: reason,
		})
		if rerr != nil {
			hadError = true
			if firstErr == nil {
				firstErr = rerr
			}
			break
		}
		totalSegments += res.SegmentsShipped
		totalBytes += res.BytesShipped
		if res.PolicyRefused {
			policyRefused = true
			break
		}
		if res.CaptureUnavailable {
			captureUnavail = true
			break
		}
		if res.Truncated {
			truncated = true
			break
		}
		if len(res.ArtifactErrors) > 0 {
			hadError = true
			for _, aerr := range res.ArtifactErrors {
				if firstErr == nil && aerr != nil {
					firstErr = aerr
				}
			}
		}
		if !res.BudgetExhausted || res.SegmentsShipped == 0 {
			break
		}
	}

	report.SegmentsCommitted = totalSegments
	report.BytesCommitted = totalBytes

	switch {
	case policyRefused || captureUnavail:
		report.Trajectory = TrajectoryCaptureRefused
		return 0, time.Time{}, false, "", ""
	case truncated:
		if totalSegments > 0 {
			report.Trajectory = TrajectoryShipped
		} else {
			report.Trajectory = TrajectoryNothingPending
		}
		if stAfter, okAfter, _ := shipper.LoadState(target.HomeDir); okAfter && stAfter != nil {
			pb, ot := estimateTrajectoryPending(target.HomeDir, spec, stAfter)
			return pb, ot, true, "", ""
		}
		return 0, time.Time{}, true, "", ""
	case hadError:
		report.Trajectory = TrajectoryError
		if stAfter, okAfter, _ := shipper.LoadState(target.HomeDir); okAfter && stAfter != nil {
			pendingBytes, oldest = estimateTrajectoryPending(target.HomeDir, spec, stAfter)
		}
		code := classifyShipperError(firstErr)
		msg := ""
		if firstErr != nil {
			msg = firstErr.Error()
		}
		return pendingBytes, oldest, false, code, msg
	case totalSegments > 0:
		report.Trajectory = TrajectoryShipped
		return 0, time.Time{}, false, "", ""
	default:
		report.Trajectory = TrajectoryNothingPending
		return 0, time.Time{}, false, "", ""
	}
}

func (e *Engine) harvestUsage(
	ctx context.Context,
	target Target,
	report *Report,
) (pendingBytes int64, oldest time.Time, errCode string, errMsg string) {
	var post telemetry.UsageIngestPostFunc
	if e.hub != nil {
		post = func(pctx context.Context, req telemetrycontract.UsageIngestRequest) (*telemetrycontract.UsageIngestResponse, error) {
			return e.hub.IngestUsageEvents(pctx, target.AgentID, req)
		}
	}

	stats, err := telemetry.DrainOutboxOnce(ctx, target.HomeDir, nil, post, e.hubID)
	report.UsageEventsDrained = stats.EventsDrained
	report.UsageEventsPending = stats.EventsPending

	if stats.NoOutbox {
		report.Usage = UsageNoOutbox
		return 0, time.Time{}, "", ""
	}
	if stats.HubMismatch {
		report.Usage = UsageSkipped
		return 0, time.Time{}, ErrCodeHubMismatch, "usage outbox pinned to a different hub"
	}
	if err != nil {
		report.Usage = UsageError
		return stats.PendingBytes, stats.OldestPending, classifyOutboxError(err), err.Error()
	}
	if stats.EventsPending > 0 {
		report.Usage = UsageError
		return stats.PendingBytes, stats.OldestPending, ErrCodeDeadline, "usage outbox events remain pending"
	}
	if stats.EventsDrained > 0 {
		report.Usage = UsageDrained
		return 0, time.Time{}, "", ""
	}
	report.Usage = UsageNothingPending
	return 0, time.Time{}, "", ""
}

func (e *Engine) postReportBestEffort(parentCtx context.Context, agentID string, report Report) {
	if e.hub == nil || agentID == "" {
		return
	}
	postCtx := parentCtx
	if postCtx == nil || postCtx.Err() != nil {
		postCtx = context.Background()
	}
	postCtx, cancel := context.WithTimeout(postCtx, defaultReportPostTimeout)
	defer cancel()
	if err := e.hub.ReportTrajectoryHarvest(postCtx, agentID, report); err != nil {
		e.log.Warn("Broker harvest: failed to post harvest report to Hub",
			"agent_id", agentID,
			"harvest_id", report.HarvestID,
			"trigger", report.Trigger,
			"outcome", report.Outcome,
			"error", err)
	}
}

func classifyShipperError(err error) string {
	if err == nil {
		return ""
	}
	switch {
	case errors.Is(err, filelock.ErrTimeout):
		return ErrCodeLockTimeout
	case errors.Is(err, shipper.ErrRootSquashChownRefused), errors.Is(err, privatefs.ErrNotPrivate):
		return ErrCodeUIDMismatch
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return ErrCodeDeadline
	default:
		if _, _, _, ok := extractHTTPStatusAndCode(err); ok {
			return ErrCodeHubUnavailable
		}
		return ErrCodeHubUnavailable
	}
}

func classifyOutboxError(err error) string {
	if err == nil {
		return ""
	}
	switch {
	case errors.Is(err, telemetry.ErrAnalyticsOutboxLocked):
		return ErrCodeLockTimeout
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return ErrCodeDeadline
	default:
		return ErrCodeHubUnavailable
	}
}

// estimateTrajectoryPending computes remaining unshipped bytes and oldest mtime
// across discovered artifacts without mutating state or creating files.
func estimateTrajectoryPending(homeDir string, spec shipper.HarnessSpec, st *shipper.State) (int64, time.Time) {
	resolvedHome, err := pathcanon.Resolve(homeDir)
	if err != nil {
		return 0, time.Time{}
	}
	disc, err := shipper.Discover(resolvedHome, spec)
	if err != nil {
		return 0, time.Time{}
	}
	var total int64
	var oldest time.Time
	for _, art := range disc.Artifacts {
		full := filepath.Join(resolvedHome, filepath.FromSlash(art.NativeRelativePath))
		fi, err := os.Lstat(full)
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		var committed int64
		if st != nil {
			if entry := st.Artifacts[art.NativeRelativePath]; entry != nil {
				committed = entry.RawCommittedOffset
			}
		}
		if fi.Size() > committed {
			total += fi.Size() - committed
			mt := fi.ModTime()
			if oldest.IsZero() || mt.Before(oldest) {
				oldest = mt
			}
		}
	}
	return total, oldest
}

// sanitizeReportError removes absolute host paths (especially homeDir) and
// truncates to maxReportErrorBytes (1024 bytes) per contract §3.
func sanitizeReportError(msg, homeDir string) string {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return ""
	}
	if homeDir != "" {
		msg = strings.ReplaceAll(msg, homeDir, "<home>")
		if resolved, err := pathcanon.Resolve(homeDir); err == nil && resolved != "" && resolved != homeDir {
			msg = strings.ReplaceAll(msg, resolved, "<home>")
		}
	}
	// Redact any remaining absolute Unix path tokens so host paths never leak.
	fields := strings.Fields(msg)
	for i, f := range fields {
		trimmed := strings.Trim(f, `"'(),:;`)
		if strings.HasPrefix(trimmed, "/") && len(trimmed) > 1 && strings.Count(trimmed, "/") >= 2 {
			fields[i] = strings.Replace(f, trimmed, "<path>", 1)
		}
	}
	msg = strings.Join(fields, " ")
	if len(msg) > maxReportErrorBytes {
		msg = msg[:maxReportErrorBytes]
	}
	return msg
}
