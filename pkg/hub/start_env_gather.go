// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package hub

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
)

// A start of an agent on a broker that reports startEnvGather gathers the
// agent's as-needed values the way create does (ptone/scion#6101): the start
// is sent with gatherEnv, a broker whose preflight finds required keys
// unmet answers with a needs report and starts nothing, the hub resolves
// those keys from as-needed entries, and replays the start with the values
// (envGatherFinal) under the same run. The values live only in the
// dispatch's request; nothing stores them.

// errStartEnvUnmet is returned by a start whose broker still reports
// required environment keys that the hub's as-needed entries cannot supply.
// Nothing was started. Its text names no key and no value. Handlers answer
// 422 missing_env_vars with startEnvUnmetMessage.
var errStartEnvUnmet = errors.New("required environment values are unavailable at start")

// errBrokerLacksStartEnvGather is returned by a start of an agent that has
// as-needed entries when its broker does not report startEnvGather, so its
// start cannot gather them. Handlers answer 412.
var errBrokerLacksStartEnvGather = errors.New("the agent's runtime broker must be upgraded before it can start agents that use as-needed values")

// errStartEnvGatherCapabilityUnknown is returned when the agent's broker
// record cannot be read to tell whether its start can gather env: the start
// fails rather than being sent as a start that may lack the agent's
// as-needed values. Handlers answer 503.
var errStartEnvGatherCapabilityUnknown = errors.New("the agent's runtime broker capabilities could not be read; the agent was not started")

// startEnvGatherEchoMissingError is a start sent with gatherEnv that the
// broker answered as started without the envGathered echo: the broker does
// not have the env-gather start path although its recorded capabilities
// said so. The dispatch stopped the run it started. It is
// errBrokerLacksStartEnvGather to handlers, but unlike the pre-send refusal
// the broker acted on the start.
type startEnvGatherEchoMissingError struct{}

func (startEnvGatherEchoMissingError) Error() string { return errBrokerLacksStartEnvGather.Error() }
func (startEnvGatherEchoMissingError) Unwrap() error { return errBrokerLacksStartEnvGather }

// isStartEnvGatherEchoMissing reports whether err is or wraps a
// startEnvGatherEchoMissingError.
func isStartEnvGatherEchoMissing(err error) bool {
	var e startEnvGatherEchoMissingError
	return errors.As(err, &e)
}

// startEnvUnmetMessage is the fixed user-facing text for errStartEnvUnmet.
// It names no key and no value.
func startEnvUnmetMessage(action string) string {
	done, again := "started", "start"
	if action == api.AgentActionRestart {
		done, again = "restarted", "restart"
	}
	return "Required environment values for this agent are not available on the hub, so the agent was not " + done +
		". Store each required value on the hub as an env var or secret (scion hub env set / scion hub secret set," +
		" injection mode always or as_needed), then " + again + " the agent again."
}

// writeStartEnvGatherError writes the answer for a start or restart that
// failed with one of the start env-gather errors and reports whether it
// did: 422 missing_env_vars for errStartEnvUnmet, 412 for
// errBrokerLacksStartEnvGather (also when the broker started without the
// echo), 503 for errStartEnvGatherCapabilityUnknown.
func writeStartEnvGatherError(w http.ResponseWriter, err error, action string) bool {
	switch {
	case errors.Is(err, errStartEnvUnmet):
		writeError(w, http.StatusUnprocessableEntity, ErrCodeMissingEnvVars, startEnvUnmetMessage(action), nil)
	case errors.Is(err, errBrokerLacksStartEnvGather):
		writeError(w, http.StatusPreconditionFailed, ErrCodeUnsupportedCapability, errBrokerLacksStartEnvGather.Error(), nil)
	case errors.Is(err, errStartEnvGatherCapabilityUnknown):
		writeError(w, http.StatusServiceUnavailable, ErrCodeUnavailable, errStartEnvGatherCapabilityUnknown.Error(), nil)
	default:
		return false
	}
	return true
}

// startEnvGatherCapable reports whether agent's broker reports
// startEnvGather. A broker record that cannot be read is an error
// (errStartEnvGatherCapabilityUnknown), never an answer. An agent with no
// assigned broker, or whose broker record does not exist, is not capable:
// its start then fails at the broker lookup the dispatch does anyway.
func (d *HTTPAgentDispatcher) startEnvGatherCapable(ctx context.Context, agent *store.Agent) (bool, error) {
	if agent == nil || agent.RuntimeBrokerID == "" || d.store == nil {
		return false, nil
	}
	broker, err := d.store.GetRuntimeBroker(ctx, agent.RuntimeBrokerID)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return false, nil
	case err != nil:
		d.log.WarnContext(ctx, "start: failed to read the agent's runtime broker; agent not started",
			"agent_id", agent.ID, "broker_id", agent.RuntimeBrokerID, "error", err)
		return false, fmt.Errorf("%w: %w", errStartEnvGatherCapabilityUnknown, err)
	}
	return broker != nil && broker.Capabilities != nil && broker.Capabilities.StartEnvGather, nil
}

// startEnvGatherRefusal is the check a start or restart of agent on a
// broker without startEnvGather makes before anything is minted or sent:
// errBrokerLacksStartEnvGather when the agent has as-needed entries, the
// lookup's *secretResolutionError when they cannot be read, else nil (the
// start goes on as a plain start). capable brokers return nil.
func (d *HTTPAgentDispatcher) startEnvGatherRefusal(ctx context.Context, agent *store.Agent, verb string) error {
	capable, err := d.startEnvGatherCapable(ctx, agent)
	if err != nil || capable {
		return err
	}
	has, _, err := d.hasAsNeededEntries(ctx, agent, verb)
	if err != nil {
		return err
	}
	if has {
		d.log.WarnContext(ctx, "start: the agent's broker cannot gather as-needed values; agent not "+verb,
			"agent_id", agent.ID, "broker_id", agent.RuntimeBrokerID)
		return errBrokerLacksStartEnvGather
	}
	return nil
}

// restartEnvGatherPrechecker is a dispatcher that checks, before a
// lifecycle restart's stop leg, that its start leg will not be refused for
// env-gather reasons. HTTPAgentDispatcher implements it; other dispatchers
// (test fakes) skip the check.
type restartEnvGatherPrechecker interface {
	RestartEnvGatherPrecheck(ctx context.Context, agent *store.Agent) error
}

// RestartEnvGatherPrecheck is run by the lifecycle restart before its stop
// leg, so a restart its start leg would refuse leaves the agent untouched:
// the capability read error, or an old broker with as-needed entries.
func (d *HTTPAgentDispatcher) RestartEnvGatherPrecheck(ctx context.Context, agent *store.Agent) error {
	return d.startEnvGatherRefusal(ctx, agent, "restarted")
}

// startGatherSend is one start send of a gathering start; final marks the
// replay (envGatherFinal).
type startGatherSend func(ctx context.Context, final bool) (*RemoteAgentResponse, error)

// startGather completes a start sent with gatherEnv, given its first
// answer (resp, err from send(ctx, false)). A needs report is resolved from
// the agent's as-needed entries and the start replayed once with the values
// added to resolvedEnv (classified secret, never plain, so nothing persists
// them); a required (not marker-only) key that cannot be resolved fails
// with errStartEnvUnmet before any replay, a resolution error with the
// *secretResolutionError (verb "started"). A replay that still reports
// needs fails with errStartEnvUnmet. A started answer without the
// envGathered echo is stopped (stopRun) and fails with
// startEnvGatherEchoMissingError. Errors of the sends are returned as they
// are (a cross-node hand-off included).
func (d *HTTPAgentDispatcher) startGather(ctx context.Context, agent *store.Agent, resp *RemoteAgentResponse, err error,
	resolvedEnv map[string]string, classifications *map[string]api.EnvKind,
	send startGatherSend, stopRun func(ctx context.Context)) (*RemoteAgentResponse, error) {
	if err != nil {
		return resp, err
	}
	if resp == nil || resp.EnvNeeds == nil {
		return d.checkEnvGatheredEcho(ctx, agent, resp, stopRun)
	}
	needs := resp.EnvNeeds
	takeTZGatherNeed(needs)
	var resolved map[string]string
	if len(needs.Needs) > 0 {
		resolved, err = d.resolveAsNeededForKeys(ctx, agent, needs.Needs, needs.Alternatives)
		if err != nil {
			var resErr *secretResolutionError
			if errors.As(err, &resErr) {
				resErr.Verb = "started"
			}
			return nil, err
		}
	}
	if n := countUnresolved(hardNeeds(needs), resolved); n > 0 {
		d.log.WarnContext(ctx, "start: required environment values unavailable; agent not started",
			"agent_id", agent.ID, "count", n)
		return nil, errStartEnvUnmet
	}
	for k, v := range resolved {
		resolvedEnv[k] = v
		classifyEnv(classifications, k, api.EnvKindSecretFetchable)
	}
	resp, err = send(ctx, true)
	if err != nil {
		return resp, err
	}
	if resp != nil && resp.EnvNeeds != nil {
		d.log.WarnContext(ctx, "start: required environment values still unavailable after the as-needed pass; agent not started",
			"agent_id", agent.ID, "count", len(hardNeeds(resp.EnvNeeds)))
		return nil, errStartEnvUnmet
	}
	resp, err = d.checkEnvGatheredEcho(ctx, agent, resp, stopRun)
	if err != nil {
		return resp, err
	}
	if n := countUnresolved(needs.MarkerOnly, resolved); n > 0 {
		addDispatchWarnings(ctx, fmt.Sprintf("%d optional environment value(s) were not available on the hub and were omitted", n))
	}
	return resp, nil
}

// checkEnvGatheredEcho returns resp when it carries the envGathered echo.
// Otherwise the broker started without the env-gather start path: the run
// is stopped and the start fails with startEnvGatherEchoMissingError.
func (d *HTTPAgentDispatcher) checkEnvGatheredEcho(ctx context.Context, agent *store.Agent, resp *RemoteAgentResponse, stopRun func(ctx context.Context)) (*RemoteAgentResponse, error) {
	if resp != nil && resp.EnvGathered {
		return resp, nil
	}
	d.log.WarnContext(ctx, "start: the broker started the agent without gathering env; stopping the run",
		"agent_id", agent.ID, "broker_id", agent.RuntimeBrokerID)
	stopRun(ctx)
	return nil, startEnvGatherEchoMissingError{}
}

// hardNeeds returns the needs of reqs that are not marker-only.
func hardNeeds(reqs *RemoteEnvRequirementsResponse) []string {
	if reqs == nil {
		return nil
	}
	marker := make(map[string]struct{}, len(reqs.MarkerOnly))
	for _, k := range reqs.MarkerOnly {
		marker[k] = struct{}{}
	}
	var out []string
	for _, k := range reqs.Needs {
		if _, ok := marker[k]; !ok {
			out = append(out, k)
		}
	}
	return out
}

// countUnresolved counts the keys with no non-empty value in resolved.
func countUnresolved(keys []string, resolved map[string]string) int {
	n := 0
	for _, k := range keys {
		if resolved[k] == "" {
			n++
		}
	}
	return n
}
