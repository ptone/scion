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
	"sort"

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

// errBrokerCapabilityUnavailable is returned when the agent's broker
// record cannot be read to tell whether its start can gather env: the start
// fails rather than being sent as a start that may lack the agent's
// as-needed values. Handlers answer 503 with brokerCapabilityUnavailableMessage.
var errBrokerCapabilityUnavailable = errors.New("the agent's runtime broker could not be checked")

// brokerCapabilityUnavailableMessage is the fixed user-facing text for
// errBrokerCapabilityUnavailable.
func brokerCapabilityUnavailableMessage(action string) string {
	done := "started"
	if action == api.AgentActionRestart {
		done = "restarted"
	}
	return errBrokerCapabilityUnavailable.Error() + "; the agent was not " + done
}

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
// echo), 503 for errBrokerCapabilityUnavailable.
func writeStartEnvGatherError(w http.ResponseWriter, err error, action string) bool {
	switch {
	case errors.Is(err, errStartEnvUnmet):
		writeError(w, http.StatusUnprocessableEntity, ErrCodeMissingEnvVars, startEnvUnmetMessage(action), nil)
	case errors.Is(err, errBrokerLacksStartEnvGather):
		writeError(w, http.StatusPreconditionFailed, ErrCodeUnsupportedCapability, errBrokerLacksStartEnvGather.Error(), nil)
	case errors.Is(err, errBrokerCapabilityUnavailable):
		writeError(w, http.StatusServiceUnavailable, ErrCodeUnavailable, brokerCapabilityUnavailableMessage(action), nil)
	default:
		return false
	}
	return true
}

// startEnvGatherCapable reports whether agent's broker reports
// startEnvGather. A broker record that cannot be read, or does not exist,
// is an error (errBrokerCapabilityUnavailable, 503), never an answer: the
// start fails with nothing sent (G-2a). An agent with no assigned broker is
// not capable; requireRuntimeBrokerAssigned refuses its start.
func (d *HTTPAgentDispatcher) startEnvGatherCapable(ctx context.Context, agent *store.Agent) (bool, error) {
	if agent == nil || agent.RuntimeBrokerID == "" || d.store == nil {
		return false, nil
	}
	broker, err := d.store.GetRuntimeBroker(ctx, agent.RuntimeBrokerID)
	if err == nil && broker == nil {
		err = store.ErrNotFound
	}
	if err != nil {
		d.log.WarnContext(ctx, "start: failed to read the agent's runtime broker; agent not started",
			"agent_id", agent.ID, "broker_id", agent.RuntimeBrokerID, "error", err)
		return false, fmt.Errorf("%w: %w", errBrokerCapabilityUnavailable, err)
	}
	return broker.Capabilities != nil && broker.Capabilities.StartEnvGather, nil
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
	resolved, err := d.asNeededPass(ctx, agent, needs, "started")
	if err != nil {
		return nil, err
	}
	userSupplied := userSuppliedKeys(agent)
	if n := countUnresolved(hardNeeds(needs, userSupplied), resolved); n > 0 {
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
			"agent_id", agent.ID, "count", len(hardNeeds(resp.EnvNeeds, userSupplied)))
		return nil, errStartEnvUnmet
	}
	resp, err = d.checkEnvGatheredEcho(ctx, agent, resp, stopRun)
	if err != nil {
		return resp, err
	}
	if n := countUnresolved(optionalNeeds(needs, userSupplied), resolved); n > 0 {
		addDispatchWarnings(ctx, fmt.Sprintf("%d optional environment value(s) were not available on the hub and were omitted", n))
	}
	return resp, nil
}

// asNeededPass resolves the needs of reqs from the agent's as-needed env
// vars and secrets (resolveAsNeededForKeys). verb names the action a
// *secretResolutionError reports ("started", "restarted", ...). The values
// are returned for the dispatch's request only; nothing stores them.
func (d *HTTPAgentDispatcher) asNeededPass(ctx context.Context, agent *store.Agent, reqs *RemoteEnvRequirementsResponse, verb string) (map[string]string, error) {
	if reqs == nil || len(reqs.Needs) == 0 {
		return nil, nil
	}
	resolved, err := d.resolveAsNeededForKeys(ctx, agent, reqs.Needs, reqs.Alternatives)
	if err != nil {
		var resErr *secretResolutionError
		if errors.As(err, &resErr) {
			resErr.Verb = verb
		}
		return nil, err
	}
	return resolved, nil
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

// recordUserSuppliedEnvKeys records, in memory, the names of the env keys
// a successful env finalize submitted (sorted, deduplicated, names only;
// TZ, which the finalize drops, excluded), replacing any earlier list. The
// caller's own write of the agent persists it, as for ConfigDelivered.
func recordUserSuppliedEnvKeys(agent *store.Agent, env map[string]string) {
	if agent == nil {
		return
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		if k != "" && k != agentTZEnvKey {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	if agent.AppliedConfig == nil {
		if len(keys) == 0 {
			return
		}
		agent.AppliedConfig = &store.AgentAppliedConfig{}
	}
	if len(keys) == 0 {
		keys = nil
	}
	agent.AppliedConfig.UserSuppliedEnvKeys = keys
}

// userSuppliedKeys returns the agent's user-supplied env key names as a set.
func userSuppliedKeys(agent *store.Agent) map[string]struct{} {
	if agent == nil || agent.AppliedConfig == nil {
		return nil
	}
	set := make(map[string]struct{}, len(agent.AppliedConfig.UserSuppliedEnvKeys))
	for _, k := range agent.AppliedConfig.UserSuppliedEnvKeys {
		set[k] = struct{}{}
	}
	return set
}

// hardNeeds returns the needs of reqs a start may not leave unmet: the
// needs that are not marker-only, plus the marker-only needs the user
// supplied at create (userSupplied, by the key or one of its alternatives).
func hardNeeds(reqs *RemoteEnvRequirementsResponse, userSupplied map[string]struct{}) []string {
	if reqs == nil {
		return nil
	}
	marker := make(map[string]struct{}, len(reqs.MarkerOnly))
	for _, k := range reqs.MarkerOnly {
		marker[k] = struct{}{}
	}
	var out []string
	for _, k := range reqs.Needs {
		if _, isMarker := marker[k]; !isMarker || suppliedByUser(k, reqs.Alternatives[k], userSupplied) {
			out = append(out, k)
		}
	}
	return out
}

// optionalNeeds returns the marker-only needs of reqs the user did not
// supply at create: a start may leave these unmet.
func optionalNeeds(reqs *RemoteEnvRequirementsResponse, userSupplied map[string]struct{}) []string {
	if reqs == nil {
		return nil
	}
	var out []string
	for _, k := range reqs.MarkerOnly {
		if !suppliedByUser(k, reqs.Alternatives[k], userSupplied) {
			out = append(out, k)
		}
	}
	return out
}

// suppliedByUser reports whether key, or one of its alternatives, is in
// userSupplied.
func suppliedByUser(key string, alternatives []string, userSupplied map[string]struct{}) bool {
	if _, ok := userSupplied[key]; ok {
		return true
	}
	for _, alt := range alternatives {
		if _, ok := userSupplied[alt]; ok {
			return true
		}
	}
	return false
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
