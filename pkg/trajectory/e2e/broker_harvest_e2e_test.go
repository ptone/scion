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

//go:build !no_sqlite && unix

package e2e

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/ent/usageevent"
	"github.com/GoogleCloudPlatform/scion/pkg/experiments"
	"github.com/GoogleCloudPlatform/scion/pkg/hub"
	"github.com/GoogleCloudPlatform/scion/pkg/hubclient"
	"github.com/GoogleCloudPlatform/scion/pkg/runtimebroker/harvest"
	scionhub "github.com/GoogleCloudPlatform/scion/pkg/sciontool/hub"
	"github.com/GoogleCloudPlatform/scion/pkg/sciontool/telemetry"
	"github.com/GoogleCloudPlatform/scion/pkg/storage"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/GoogleCloudPlatform/scion/pkg/store/entadapter"
	"github.com/GoogleCloudPlatform/scion/pkg/store/enttest"
	"github.com/GoogleCloudPlatform/scion/pkg/telemetrycontract"
	"github.com/GoogleCloudPlatform/scion/pkg/trajectory/shipper"
)

func TestBrokerHarvest_EndToEnd(t *testing.T) {
	ctx := context.Background()

	// 1. Stand up a real Hub server on local storage with both
	//    tokenomics.trajectory_capture and tokenomics.usage_ledger enabled.
	entClient := enttest.NewClient(t)
	st := entadapter.NewCompositeStore(entClient)

	cur := experiments.Default()
	var active []experiments.Experiment
	for _, e := range cur.All() {
		if e.Name == experiments.TokenomicsTrajectoryCapture || e.Name == experiments.TokenomicsUsageLedger {
			e.Default = true
		}
		active = append(active, e)
	}
	reg, err := experiments.NewRegistry(active, cur.Retired())
	require.NoError(t, err)

	srv, err := hub.New(hub.ServerConfig{
		HubID:               "hub-e2e-harvest",
		DevAuthToken:        "e2e-dev-token",
		SharedSigningSecret: "test-e2e-broker-harvest-secret-32b",
		BrokerAuthConfig:    hub.DefaultBrokerAuthConfig(),
		Experiments:         reg,
	}, st)
	require.NoError(t, err)

	loc, err := storage.NewLocal(storage.Config{
		Provider:  storage.ProviderLocal,
		Bucket:    "local",
		LocalPath: t.TempDir(),
	})
	require.NoError(t, err)
	srv.SetStorage(loc)

	httpSrv := httptest.NewServer(srv.Handler())
	t.Cleanup(httpSrv.Close)

	// 2. Register a real broker with HMAC secret, project, agent, run attribution,
	//    and an agent token so the in-container shipper can ship initial bytes.
	brokerID := api.NewUUID()
	brokerKey := []byte("test-broker-harvest-hmac-key-0123456")
	require.NoError(t, st.CreateRuntimeBroker(ctx, &store.RuntimeBroker{
		ID:   brokerID,
		Name: "e2e-harvest-broker",
		Slug: "e2e-harvest-broker",
	}))
	require.NoError(t, st.CreateBrokerSecret(ctx, &store.BrokerSecret{
		BrokerID:  brokerID,
		SecretKey: brokerKey,
		Algorithm: store.BrokerSecretAlgorithmHMACSHA256,
		CreatedAt: time.Now().UTC(),
		Status:    store.BrokerSecretStatusActive,
	}))

	proj := &store.Project{
		ID:   api.NewUUID(),
		Name: "e2e-broker-harvest-proj",
		Slug: "e2e-broker-harvest-proj",
	}
	require.NoError(t, st.CreateProject(ctx, proj))

	runID := "run-" + api.NewUUID()
	ag := &store.Agent{
		ID:              api.NewUUID(),
		Slug:            "e2e-harvest-agent",
		Name:            "e2e-harvest-agent",
		ProjectID:       proj.ID,
		RuntimeBrokerID: brokerID,
		RunID:           runID,
		OwnerID:         hub.DevUserID,
		CreatedBy:       hub.DevUserID,
		AppliedConfig: &store.AgentAppliedConfig{
			Harness:       "claude",
			HarnessConfig: "claude-default",
		},
		Template: "developer",
	}
	require.NoError(t, st.CreateAgent(ctx, ag))

	hubID := srv.HubID()
	_, err = st.EnsureRunAttribution(ctx, &store.RunAttribution{
		RunID:         runID,
		HubID:         hubID,
		ProjectID:     proj.ID,
		AgentID:       ag.ID,
		AgentSlug:     ag.Slug,
		Harness:       "claude",
		HarnessConfig: "claude-default",
		Template:      "developer",
	})
	require.NoError(t, err)

	agentTok, cred, err := srv.GetAgentTokenService().SignAgentToken(hub.AgentTokenGrant{
		AgentID:   ag.ID,
		ProjectID: proj.ID,
		Scopes:    []hub.AgentTokenScope{hub.ScopeAgentStatusUpdate},
	}, runID)
	require.NoError(t, err)
	require.NoError(t, st.CreateAgentCredential(ctx, cred))

	// 3. Run the in-container shipper in a temp home: baseline pre-existing
	//    history, ship a first segment, then simulate a mid-run crash leaving
	//    unshipped bytes (including an incomplete tail) and crash-left usage
	//    events in staging.jsonl.
	homeDir := t.TempDir()
	sessRel := ".claude/projects/demo/session-harvest.jsonl"
	sessPath := filepath.Join(homeDir, filepath.FromSlash(sessRel))
	require.NoError(t, os.MkdirAll(filepath.Dir(sessPath), 0o755))

	preBaseline := "{\"role\":\"system\",\"note\":\"pre-baseline-must-not-ship\"}\n"
	require.NoError(t, os.WriteFile(sessPath, []byte(preBaseline), 0o600))

	agentHubClient := scionhub.NewClientWithConfig(httpSrv.URL, agentTok, ag.ID)
	inContainerShipper, err := shipper.New(shipper.Config{
		HomeDir:  homeDir,
		RunID:    runID,
		Harness:  "claude",
		Uploader: scionhub.NewTrajectoryUploader(agentHubClient),
	})
	require.NoError(t, err)
	_, err = inContainerShipper.Baseline(ctx)
	require.NoError(t, err)

	part1 := "{\"turn\":1,\"text\":\"shipped-by-agent\"}\n"
	appendFile(t, sessPath, part1)
	r1, err := inContainerShipper.RunRound(ctx, shipper.RoundOptions{
		Final:  false,
		Reason: shipper.ReasonPeriodic,
	})
	require.NoError(t, err)
	require.Equal(t, 1, r1.SegmentsShipped)

	// Simulate mid-run crash: more bytes written (including an incomplete tail
	// without trailing newline) that the killed shipper never shipped.
	part2 := "{\"turn\":2,\"text\":\"left-at-crash\"}\n{\"turn\":3,\"partial\":true}"
	appendFile(t, sessPath, part2)

	// Simulate crash-left usage outbox events in staging.jsonl (never finalized).
	ob, err := telemetry.OpenAnalyticsOutbox(telemetry.HubAnalyticsOutboxDirFor(homeDir))
	require.NoError(t, err)
	_, err = ob.PinHubID(hubID)
	require.NoError(t, err)

	now := time.Now().UTC()
	inTok := int64(42)
	for _, id := range []string{"ev-harvest-1", "ev-harvest-2"} {
		ev := &telemetrycontract.UsageEvent{
			Envelope:            telemetrycontract.NewEnvelope(telemetrycontract.EventTypeUsage, id, "sciontool/agent/"+ag.ID, now, now),
			UsageSource:         telemetrycontract.UsageSourceNative,
			SourceKind:          "claude.api_request",
			SourceSchemaVersion: "1",
			SourceEventID:       "rid-" + id,
			SourceIdentity:      telemetrycontract.SourceIdentityRequestID,
			RunID:               runID,
			Harness:             "claude",
			Model:               "claude-sonnet-4-6",
			ModelSource:         telemetrycontract.ModelSourceEvent,
			Status:              telemetrycontract.StatusSuccess,
			Calls:               1,
			Input:               &inTok,
			InputDerivation:     telemetrycontract.InputDerivationReported,
			TokenStatus:         telemetrycontract.TokenStatusReported,
			MalformedFields:     []string{},
		}
		ev.AgentID = ag.ID
		_, err = ob.AppendUsage(ev)
		require.NoError(t, err)
	}

	// 4. Run a broker pre_delete harvest against the real Hub test server.
	hc, err := hubclient.New(httpSrv.URL, hubclient.WithHMACAuth(brokerID, brokerKey))
	require.NoError(t, err)
	bhc, ok := hc.(hubclient.BrokerHarvestClient)
	require.True(t, ok, "hubclient.Client must implement BrokerHarvestClient")

	eng := harvest.New(harvest.Config{
		Hub:   harvest.NewHubClientAdapter(bhc, brokerID),
		HubID: hubID,
	})
	target := harvest.Target{
		AgentID:      ag.ID,
		Slug:         ag.Slug,
		ProjectID:    proj.ID,
		RunID:        runID,
		Harness:      "claude",
		HomeDir:      homeDir,
		RuntimeName:  "docker",
		HomeReadable: true,
	}

	rep := eng.Harvest(ctx, target, harvest.TriggerPreDelete)
	require.Equal(t, telemetrycontract.TrajectoryHarvestOutcomeComplete, rep.Outcome, "report=%+v", rep)
	require.Equal(t, telemetrycontract.TrajectoryHarvestTrajectoryShipped, rep.Trajectory)
	require.Equal(t, telemetrycontract.TrajectoryHarvestUsageDrained, rep.Usage)
	require.Equal(t, 1, rep.SegmentsCommitted)
	require.Equal(t, int64(len(part2)), rep.BytesCommitted)
	require.Equal(t, 2, rep.UsageEventsDrained)
	require.Equal(t, 0, rep.UsageEventsPending)

	// 5. Assert reassembled bytes from Hub storage are byte-identical to all
	//    post-baseline bytes (part1 + part2) and include both upload sources.
	segs, err := entClient.TrajectorySegment.Query().All(ctx)
	require.NoError(t, err)
	require.Len(t, segs, 2)
	sort.Slice(segs, func(i, j int) bool { return segs[i].ByteStart < segs[j].ByteStart })
	require.Equal(t, telemetrycontract.TrajectoryUploadSourceAgent, segs[0].UploadSource)
	require.Equal(t, telemetrycontract.TrajectoryUploadSourceBrokerHarvest, segs[1].UploadSource)

	reassembled := reassembleGeneration(t, ctx, loc, segs, 0)
	require.Equal(t, part1+part2, string(reassembled), "reassembled bytes must be byte-identical to post-baseline bytes")

	// 6. Assert usage events were ingested once.
	usageCount, err := entClient.UsageEvent.Query().Where(usageevent.AgentIDEQ(ag.ID)).Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 2, usageCount)

	// 7. Assert trajectory_harvests row has outcome=complete.
	row, err := st.LatestTrajectoryHarvest(ctx, ag.ID, telemetrycontract.TrajectoryHarvestTriggerPreDelete)
	require.NoError(t, err)
	require.Equal(t, rep.HarvestID, row.HarvestID)
	require.Equal(t, telemetrycontract.TrajectoryHarvestOutcomeComplete, row.Outcome)
	require.Equal(t, telemetrycontract.TrajectoryHarvestTrajectoryShipped, row.Trajectory)
	require.Equal(t, telemetrycontract.TrajectoryHarvestUsageDrained, row.Usage)
	require.Equal(t, 1, row.SegmentsCommitted)
	require.Equal(t, 2, row.UsageEventsDrained)

	// 8. Replay harvest: must report nothing_pending with zero duplicates.
	rep2 := eng.Harvest(ctx, target, harvest.TriggerPreDelete)
	require.Equal(t, telemetrycontract.TrajectoryHarvestOutcomeNothingPending, rep2.Outcome, "replay=%+v", rep2)
	require.Equal(t, telemetrycontract.TrajectoryHarvestTrajectoryNothingPending, rep2.Trajectory)
	require.Equal(t, telemetrycontract.TrajectoryHarvestUsageNothingPending, rep2.Usage)

	segsAfter, err := entClient.TrajectorySegment.Query().All(ctx)
	require.NoError(t, err)
	require.Len(t, segsAfter, 2, "replay must not create duplicate trajectory segments")

	usageCountAfter, err := entClient.UsageEvent.Query().Where(usageevent.AgentIDEQ(ag.ID)).Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 2, usageCountAfter, "replay must not create duplicate usage events")
}
