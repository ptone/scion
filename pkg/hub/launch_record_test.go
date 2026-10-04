//go:build !no_sqlite

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
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/GoogleCloudPlatform/scion/pkg/store/entadapter"
	"github.com/GoogleCloudPlatform/scion/pkg/store/enttest"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// launchIDClient is a RuntimeBrokerClient for launch id tests. It records the
// launch id each start/restart sends, together with the agent row's launch
// id at that moment, and answers with resp/err. A broker other than
// localBroker is handed off (ErrLifecycleDeferred) when deferRemote is set.
type launchIDClient struct {
	fakeHTTPClient
	store       store.Store
	agentID     string
	localBroker string
	deferRemote bool
	resp        func(sent string) *RemoteAgentResponse
	err         error

	sent      []string
	rowAtSend []string
}

func (c *launchIDClient) answer(ctx context.Context, brokerID string, extras StartExtras) (*RemoteAgentResponse, error) {
	if c.deferRemote && brokerID != c.localBroker {
		return nil, ErrLifecycleDeferred
	}
	c.sent = append(c.sent, extras.LaunchID)
	row := "<unread>"
	if a, err := c.store.GetAgent(ctx, c.agentID); err == nil {
		row = a.LaunchID
	}
	c.rowAtSend = append(c.rowAtSend, row)
	if c.err != nil {
		return nil, c.err
	}
	if c.resp == nil {
		return nil, nil
	}
	return c.resp(extras.LaunchID), nil
}

func (c *launchIDClient) StartAgent(ctx context.Context, brokerID, _, _, _, _, _, _, _, _, _ string, _ map[string]string, _ []ResolvedSecret, _ *api.ScionConfig, _ []api.SharedDir, _, _ bool, extras StartExtras) (*RemoteAgentResponse, error) {
	return c.answer(ctx, brokerID, extras)
}

func (c *launchIDClient) RestartAgent(ctx context.Context, brokerID, _, _, _ string, _ map[string]string, extras StartExtras) (*RemoteAgentResponse, error) {
	return c.answer(ctx, brokerID, extras)
}

// effective returns a broker response reporting id as the running
// container's launch id.
func effective(id string) func(string) *RemoteAgentResponse {
	return func(string) *RemoteAgentResponse {
		return &RemoteAgentResponse{EffectiveLaunchID: &id}
	}
}

// echoEffective answers as a broker that started a new container: the
// effective id is the one sent.
func echoEffective(sent string) *RemoteAgentResponse {
	return &RemoteAgentResponse{EffectiveLaunchID: &sent}
}

func dispatchOp(ctx context.Context, d *HTTPAgentDispatcher, op string, agent *store.Agent) error {
	if op == "restart" {
		return d.DispatchAgentRestart(ctx, agent)
	}
	return d.DispatchAgentStart(ctx, agent, "", false)
}

func launchRow(t *testing.T, cs store.Store, agentID string) *store.Agent {
	t.Helper()
	a, err := cs.GetAgent(context.Background(), agentID)
	require.NoError(t, err)
	return a
}

// TestDispatchLaunchID covers the launch id a direct start or restart sends
// and what the agent row holds after the broker answers.
func TestDispatchLaunchID(t *testing.T) {
	const reusedID = "0b7d4a52-0000-4000-8000-00000000000a"
	tests := []struct {
		name    string
		resp    func(string) *RemoteAgentResponse
		err     error
		wantErr bool
		wantRow string // "proposed" means the id that was sent
	}{
		{name: "new container", resp: echoEffective, wantRow: "proposed"},
		{name: "reused labelled container", resp: effective(reusedID), wantRow: reusedID},
		{name: "reused unlabelled container", resp: effective(""), wantRow: ""},
		{name: "broker without effective id", resp: func(string) *RemoteAgentResponse { return &RemoteAgentResponse{} }, wantRow: "proposed"},
		{name: "no response body", wantRow: "proposed"},
		{name: "dispatch timeout", err: context.DeadlineExceeded, wantErr: true, wantRow: "proposed"},
		{name: "dispatch refused", err: errors.New("broker unavailable"), wantErr: true, wantRow: "proposed"},
	}
	for _, op := range []string{"start", "restart"} {
		for _, tt := range tests {
			t.Run(op+"/"+tt.name, func(t *testing.T) {
				ctx := context.Background()
				cs := entadapter.NewCompositeStore(enttest.NewClient(t))
				agent := seedAgentWithBrokerID(t, cs, uuid.NewString())
				client := &launchIDClient{store: cs, agentID: agent.ID, resp: tt.resp, err: tt.err}
				d := NewHTTPAgentDispatcherWithClient(cs, client, false, slog.Default())

				err := dispatchOp(ctx, d, op, agent)
				if tt.wantErr {
					require.Error(t, err)
				} else {
					require.NoError(t, err)
				}

				require.Len(t, client.sent, 1)
				proposed := client.sent[0]
				_, perr := uuid.Parse(proposed)
				require.NoError(t, perr, "a launch id is sent")
				assert.Equal(t, proposed, client.rowAtSend[0], "the id is recorded before the send")

				want := tt.wantRow
				if want == "proposed" {
					want = proposed
				}
				row := launchRow(t, cs, agent.ID)
				assert.Equal(t, want, row.LaunchID)
				assert.Equal(t, want, agent.LaunchID, "the in-memory agent follows the row")
				wantKind := store.LaunchKindStart
				if op == "restart" {
					wantKind = store.LaunchKindRestart
				}
				assert.Equal(t, wantKind, row.LaunchKind)
				assert.Equal(t, store.LaunchEndReasonRecordOnly, row.LaunchEndReason)
			})
		}
	}
}

// TestDispatchLaunchIDEachStartIsNew: every start records its own id, and a
// hash-mismatch retry within one start resends the same id.
func TestDispatchLaunchIDEachStartIsNew(t *testing.T) {
	ctx := context.Background()
	cs := entadapter.NewCompositeStore(enttest.NewClient(t))
	agent := seedAgentWithBrokerID(t, cs, uuid.NewString())
	client := &launchIDClient{store: cs, agentID: agent.ID, resp: echoEffective}
	d := NewHTTPAgentDispatcherWithClient(cs, client, false, slog.Default())

	require.NoError(t, d.DispatchAgentStart(ctx, agent, "", false))
	require.NoError(t, d.DispatchAgentStart(ctx, agent, "", false))
	require.Len(t, client.sent, 2)
	assert.NotEqual(t, client.sent[0], client.sent[1])
	assert.Equal(t, client.sent[1], launchRow(t, cs, agent.ID).LaunchID)
}

// TestDispatchLaunchIDRecordFailure: when the id cannot be recorded the
// dispatch goes ahead without one.
func TestDispatchLaunchIDRecordFailure(t *testing.T) {
	ctx := context.Background()
	cs := entadapter.NewCompositeStore(enttest.NewClient(t))
	agent := seedAgentWithBrokerID(t, cs, uuid.NewString())
	client := &launchIDClient{store: cs, resp: echoEffective}
	d := NewHTTPAgentDispatcherWithClient(cs, client, false, slog.Default())

	ghost := *agent
	ghost.ID = uuid.NewString() // not in the store: RecordLaunch fails
	require.NoError(t, d.DispatchAgentStart(ctx, &ghost, "", false))
	assert.Equal(t, []string{""}, client.sent)
}

// cancelOnSignalBus ends the originator's wait as soon as it has handed a
// lifecycle op off, standing in for a wait that times out.
type cancelOnSignalBus struct {
	NoopCommandBus
	cancel context.CancelFunc
}

func (b cancelOnSignalBus) SignalBrokerCmd(context.Context, string) error {
	b.cancel()
	return nil
}

// TestDeferredLaunchIDReplay: a start or restart handed to the owning node
// carries the proposed id in its dispatch row. The originator's wait times
// out; the owner's replay then sends that same id, and the effective id from
// the replay's answer completes the adoption.
func TestDeferredLaunchIDReplay(t *testing.T) {
	const reusedID = "0b7d4a52-0000-4000-8000-00000000000b"
	tests := []struct {
		name    string
		resp    func(string) *RemoteAgentResponse
		wantRow string // "proposed" means the originator's id
	}{
		{name: "reused labelled container", resp: effective(reusedID), wantRow: reusedID},
		{name: "reused unlabelled container", resp: effective(""), wantRow: ""},
		{name: "new container", resp: echoEffective, wantRow: "proposed"},
	}
	for _, op := range []string{"start", "restart"} {
		for _, tt := range tests {
			t.Run(op+"/"+tt.name, func(t *testing.T) {
				ctx := context.Background()
				cs := entadapter.NewCompositeStore(enttest.NewClient(t))
				agent := seedAgentWithBrokerID(t, cs, uuid.NewString())
				events := NewChannelEventPublisher()
				t.Cleanup(events.Close)

				// Originator: the broker is owned elsewhere, so the op is
				// handed off and the wait ends without an answer.
				waitCtx, cancel := context.WithCancel(ctx)
				defer cancel()
				originator := &launchIDClient{store: cs, agentID: agent.ID, deferRemote: true, localBroker: "elsewhere"}
				od := NewHTTPAgentDispatcherWithClient(cs, originator, false, slog.Default())
				od.SetCrossNodeDeps(events, cancelOnSignalBus{cancel: cancel})
				require.Error(t, dispatchOp(waitCtx, od, op, agent), "the originator's wait ends unanswered")

				proposed := launchRow(t, cs, agent.ID).LaunchID
				_, err := uuid.Parse(proposed)
				require.NoError(t, err, "the originator recorded an id before handing off")
				pending, err := cs.ListPendingDispatch(ctx, agent.RuntimeBrokerID)
				require.NoError(t, err)
				require.Len(t, pending, 1)
				var args struct {
					LaunchID string `json:"launchId"`
				}
				require.NoError(t, json.Unmarshal([]byte(pending[0].Args), &args))
				assert.Equal(t, proposed, args.LaunchID, "the dispatch row carries the proposed id")

				// Owner: replays the row against a broker that answers.
				owner := &launchIDClient{store: cs, agentID: agent.ID, resp: tt.resp}
				srv := &Server{
					store:             cs,
					instanceID:        "hub-owner-" + uuid.NewString()[:8],
					agentLifecycleLog: slog.Default(),
					events:            events,
				}
				srv.SetDispatcher(NewHTTPAgentDispatcherWithClient(cs, owner, false, slog.Default()))
				srv.execDispatch = srv.executeDispatch
				srv.reconcileBroker(ctx, agent.RuntimeBrokerID)

				assert.Equal(t, []string{proposed}, owner.sent, "the replay sends the same id")
				assert.Equal(t, []string{proposed}, owner.rowAtSend, "the replay records no new id")
				want := tt.wantRow
				if want == "proposed" {
					want = proposed
				}
				assert.Equal(t, want, launchRow(t, cs, agent.ID).LaunchID)
				done, err := cs.GetBrokerDispatch(ctx, pending[0].ID)
				require.NoError(t, err)
				assert.Equal(t, store.DispatchStateDone, done.State)
			})
		}
	}
}

// TestDeferredLaunchIDReplaySuperseded: when a later start has recorded its
// own id by the time the replay answers, the replay's effective id is not
// adopted.
func TestDeferredLaunchIDReplaySuperseded(t *testing.T) {
	ctx := context.Background()
	cs := entadapter.NewCompositeStore(enttest.NewClient(t))
	agent := seedAgentWithBrokerID(t, cs, uuid.NewString())
	stale, err := cs.RecordLaunch(ctx, agent.ID, store.LaunchKindStart)
	require.NoError(t, err)
	current, err := cs.RecordLaunch(ctx, agent.ID, store.LaunchKindStart)
	require.NoError(t, err)

	client := &launchIDClient{store: cs, agentID: agent.ID, resp: effective("0b7d4a52-0000-4000-8000-00000000000c")}
	d := NewHTTPAgentDispatcherWithClient(cs, client, false, slog.Default())
	require.NoError(t, d.DispatchAgentStart(withDispatchLaunchID(ctx, stale), agent, "", false))

	assert.Equal(t, []string{stale}, client.sent)
	assert.Equal(t, current, launchRow(t, cs, agent.ID).LaunchID)
}

func TestApplyStartExtrasLaunchID(t *testing.T) {
	tests := []struct {
		name     string
		launchID string
		want     any
	}{
		{name: "set", launchID: "id-1", want: "id-1"},
		{name: "unset", want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload := map[string]interface{}{}
			applyStartExtras(payload, StartExtras{LaunchID: tt.launchID})
			got, ok := payload["launchId"]
			if tt.want == nil {
				assert.False(t, ok, "no launchId key")
				return
			}
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestRestartResponseDecoding covers both transports: a restart returns the
// broker's effective launch id, and a body that does not parse is not an
// error (the broker accepted the restart).
func TestRestartResponseDecoding(t *testing.T) {
	const id = "0b7d4a52-0000-4000-8000-00000000000d"
	tests := []struct {
		name    string
		body    string
		wantNil bool    // no response at all
		wantID  *string // nil means the field is absent
	}{
		{name: "effective id", body: `{"agent":{"id":"c1"},"created":false,"effectiveLaunchId":"` + id + `"}`, wantID: strPtrHub(id)},
		{name: "empty effective id", body: `{"created":false,"effectiveLaunchId":""}`, wantID: strPtrHub("")},
		{name: "no effective id", body: `{"created":false}`},
		{name: "empty body", wantNil: true},
		{name: "not json", body: "ok", wantNil: true},
	}
	transports := map[string]func(t *testing.T, body string) (*RemoteAgentResponse, error){
		"http": func(t *testing.T, body string) (*RemoteAgentResponse, error) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req map[string]any
				_ = json.NewDecoder(r.Body).Decode(&req)
				assert.Equal(t, "proposed-1", req["launchId"])
				w.WriteHeader(http.StatusAccepted)
				_, _ = w.Write([]byte(body))
			}))
			t.Cleanup(srv.Close)
			return NewHTTPRuntimeBrokerClient().RestartAgent(context.Background(), "b1", srv.URL, "a1", "", nil, StartExtras{LaunchID: "proposed-1"})
		},
		"control channel": func(t *testing.T, body string) (*RemoteAgentResponse, error) {
			tunnel := &mockControlChannelTunnel{connected: true, status: http.StatusAccepted}
			if body != "" {
				tunnel.body = []byte(body)
			}
			c := &ControlChannelBrokerClient{manager: tunnel, signer: &mockBrokerSigner{}}
			resp, err := c.RestartAgent(context.Background(), "b1", "", "a1", "", nil, StartExtras{LaunchID: "proposed-1"})
			var req map[string]any
			require.NoError(t, json.Unmarshal(tunnel.lastRequest.Body, &req))
			assert.Equal(t, "proposed-1", req["launchId"])
			return resp, err
		},
	}
	for tname, call := range transports {
		for _, tt := range tests {
			t.Run(tname+"/"+tt.name, func(t *testing.T) {
				resp, err := call(t, tt.body)
				require.NoError(t, err)
				if tt.wantNil {
					assert.Nil(t, resp)
					return
				}
				require.NotNil(t, resp)
				assert.Equal(t, tt.wantID, resp.EffectiveLaunchID)
			})
		}
	}
}

func strPtrHub(s string) *string { return &s }
