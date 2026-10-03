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

package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
)

// Durable per-agent state for the Substrate runtime.
//
// Substrate actors carry no labels or annotations, and the actor's control
// server accepts its bootstrap (and with it the control token) exactly once.
// Everything the broker needs to keep managing an actor after its own
// process restarts — the agent record List matches on, the control token
// Exec authenticates with, and the secret values exec output is redacted
// against — is therefore persisted here, one Kubernetes Secret per agent in
// a dedicated state namespace (runtimes.<name>.substrate.state_namespace).
// The process-wide maps in substrate_runtime.go are a write-through cache
// over this store.
//
// The stored object's shape is read by every later broker version, so it is
// fixed:
//
//	metadata.name:   scion-sb-<first 40 hex chars of sha256("<atespace>/<actor>")>
//	labels:          app.kubernetes.io/managed-by=scion-substrate-broker
//	                 scion.substrate/state-version="1"
//	                 scion.substrate/atespace=<atespace>
//	annotations:     scion.substrate/phase=pending|committed|deleting
//	                 scion.substrate/actor-uid=<actor UID, empty while pending>
//	data.id:            "<atespace>/<actor>" (authoritative; the name is only its hash)
//	data.record.json:   substrateAgentRecord as JSON
//	data.control_token: the hex control token
//	data.exec_secrets:  JSON map "<source>:<name>" -> value
//
// Nothing in this file logs or wraps object data: errors and log lines carry
// the operation and the object name only.

const (
	substrateStateNamePrefix   = "scion-sb-"
	substrateStateNameHashLen  = 40
	substrateStateVersion      = "1"
	substrateStateManagedByKey = "app.kubernetes.io/managed-by"
	substrateStateManagedByVal = "scion-substrate-broker"
	substrateStateVersionKey   = "scion.substrate/state-version"
	substrateStateAtespaceKey  = "scion.substrate/atespace"
	substrateStatePhaseKey     = "scion.substrate/phase"
	substrateStateActorUIDKey  = "scion.substrate/actor-uid"

	substrateStateDataID           = "id"
	substrateStateDataRecord       = "record.json"
	substrateStateDataControlToken = "control_token"
	substrateStateDataExecSecrets  = "exec_secrets"

	// substrateStateListPageSize bounds each List call's page; List pages
	// internally until the continue token is empty.
	substrateStateListPageSize = 500
	// maxSubstrateStateListPages bounds List's paging loop against a
	// misbehaving server that never returns an empty continue token.
	maxSubstrateStateListPages = 1000
)

// substrateStatePhase is the lifecycle phase of a stored agent state object.
type substrateStatePhase string

const (
	// substrateStatePending: Run has claimed the id and persisted the
	// token, but the actor is not (yet) bootstrapped.
	substrateStatePending substrateStatePhase = "pending"
	// substrateStateCommitted: the actor was bootstrapped with the stored
	// token; the agent is live.
	substrateStateCommitted substrateStatePhase = "committed"
	// substrateStateDeleting: a Delete has started; Run refuses the id
	// until the object is gone.
	substrateStateDeleting substrateStatePhase = "deleting"
)

var (
	errStateExists   = errors.New("substrate: agent state already exists")
	errStateNotFound = errors.New("substrate: agent state not found")
	errStateConflict = errors.New("substrate: agent state was modified concurrently")
)

// substrateAgentState is one agent's durable state.
type substrateAgentState struct {
	// ID is "<atespace>/<actor>", the same id Run returns.
	ID string
	// ActorUID is the Substrate actor UID; empty while pending, before
	// CreateActor has returned.
	ActorUID     string
	Phase        substrateStatePhase
	Record       substrateAgentRecord
	ControlToken string
	ExecSecrets  map[string]string

	// version is the object's resourceVersion, the compare-and-swap token
	// for Update and Delete. Set by Create, Get, Update and List.
	version string
}

// AgentStateStore persists substrateAgentState objects.
type AgentStateStore interface {
	// Create claims s.ID. It fails with errStateExists if the id is
	// already claimed. On success s.version is set.
	Create(ctx context.Context, s *substrateAgentState) error
	// Get returns the state for id, or errStateNotFound.
	Get(ctx context.Context, id string) (*substrateAgentState, error)
	// Update replaces the stored state if its version still equals
	// s.version (compare-and-swap). It fails with errStateConflict on a
	// version mismatch and errStateNotFound if the object is gone. On
	// success s.version is set to the new version.
	Update(ctx context.Context, s *substrateAgentState) error
	// Delete removes the state for id. A non-empty version is a
	// precondition (errStateConflict on mismatch). NotFound is success.
	Delete(ctx context.Context, id string, version string) error
	// List returns every readable state object in atespace ("" = all).
	List(ctx context.Context, atespace string) ([]*substrateAgentState, error)
}

// substrateStateObjectName returns the deterministic Secret name for id. It
// is a hash, so no project or agent name appears in object names, events or
// audit-log resource paths.
func substrateStateObjectName(id string) string {
	sum := sha256.Sum256([]byte(id))
	return substrateStateNamePrefix + hex.EncodeToString(sum[:])[:substrateStateNameHashLen]
}

// k8sSecretStateStore is the AgentStateStore backed by one Kubernetes Secret
// per agent in a dedicated namespace.
type k8sSecretStateStore struct {
	client    kubernetes.Interface
	namespace string
}

var _ AgentStateStore = (*k8sSecretStateStore)(nil)

func newK8sSecretStateStore(client kubernetes.Interface, namespace string) *k8sSecretStateStore {
	return &k8sSecretStateStore{client: client, namespace: namespace}
}

func (s *k8sSecretStateStore) checkConfigured() error {
	if s.client == nil || s.namespace == "" {
		return errors.New("substrate: agent state store is not configured")
	}
	return nil
}

func (s *k8sSecretStateStore) secrets() typedcorev1.SecretInterface {
	return s.client.CoreV1().Secrets(s.namespace)
}

// encode builds the Secret for st. The returned error never carries data.
func (s *k8sSecretStateStore) encode(st *substrateAgentState) (*corev1.Secret, error) {
	atespace, _, err := splitSubstrateID(st.ID)
	if err != nil {
		return nil, errors.New("substrate: agent state has an invalid id")
	}
	name := substrateStateObjectName(st.ID)
	record, err := json.Marshal(st.Record)
	if err != nil {
		return nil, fmt.Errorf("substrate: encode agent state %s: record", name)
	}
	execSecrets := st.ExecSecrets
	if execSecrets == nil {
		execSecrets = map[string]string{}
	}
	secretsJSON, err := json.Marshal(execSecrets)
	if err != nil {
		return nil, fmt.Errorf("substrate: encode agent state %s: exec secrets", name)
	}
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:            name,
			Namespace:       s.namespace,
			ResourceVersion: st.version,
			Labels: map[string]string{
				substrateStateManagedByKey: substrateStateManagedByVal,
				substrateStateVersionKey:   substrateStateVersion,
				substrateStateAtespaceKey:  atespace,
			},
			Annotations: map[string]string{
				substrateStatePhaseKey:    string(st.Phase),
				substrateStateActorUIDKey: st.ActorUID,
			},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			substrateStateDataID:           []byte(st.ID),
			substrateStateDataRecord:       record,
			substrateStateDataControlToken: []byte(st.ControlToken),
			substrateStateDataExecSecrets:  secretsJSON,
		},
	}, nil
}

// errStateUnreadable marks an object this broker version must not use: an
// unknown state-version, or a malformed object. Callers skip it (List) or
// treat it as not found (Get), logging the object name only.
var errStateUnreadable = errors.New("substrate: agent state object is unreadable")

// decode parses sec. It returns errStateUnreadable (never wrapping data) for
// an unknown state-version or malformed content.
func decodeSubstrateState(sec *corev1.Secret) (*substrateAgentState, error) {
	if sec.GetLabels()[substrateStateVersionKey] != substrateStateVersion {
		return nil, errStateUnreadable
	}
	id := string(sec.Data[substrateStateDataID])
	if _, _, err := splitSubstrateID(id); err != nil {
		return nil, errStateUnreadable
	}
	phase := substrateStatePhase(sec.GetAnnotations()[substrateStatePhaseKey])
	switch phase {
	case substrateStatePending, substrateStateCommitted, substrateStateDeleting:
	default:
		return nil, errStateUnreadable
	}
	st := &substrateAgentState{
		ID:           id,
		ActorUID:     sec.GetAnnotations()[substrateStateActorUIDKey],
		Phase:        phase,
		ControlToken: string(sec.Data[substrateStateDataControlToken]),
		version:      sec.GetResourceVersion(),
	}
	if raw := sec.Data[substrateStateDataRecord]; len(raw) > 0 {
		if err := json.Unmarshal(raw, &st.Record); err != nil {
			return nil, errStateUnreadable
		}
	}
	if raw := sec.Data[substrateStateDataExecSecrets]; len(raw) > 0 {
		if err := json.Unmarshal(raw, &st.ExecSecrets); err != nil {
			return nil, errStateUnreadable
		}
	}
	if st.ExecSecrets == nil {
		st.ExecSecrets = map[string]string{}
	}
	return st, nil
}

// apiErrReason returns a short, data-free description of a client-go error
// for wrapping: the API status reason when there is one. client-go status
// errors carry no object data, but the reason alone is all a caller needs.
func apiErrReason(err error) string {
	if reason := apierrors.ReasonForError(err); reason != metav1.StatusReasonUnknown {
		return string(reason)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err.Error()
	}
	return "request failed"
}

func storeOpError(op, name string, err error) error {
	return fmt.Errorf("substrate: %s agent state %s: %s", op, name, apiErrReason(err))
}

// Create implements AgentStateStore.
func (s *k8sSecretStateStore) Create(ctx context.Context, st *substrateAgentState) error {
	if err := s.checkConfigured(); err != nil {
		return err
	}
	sec, err := s.encode(st)
	if err != nil {
		return err
	}
	sec.ResourceVersion = ""
	created, err := s.secrets().Create(ctx, sec, metav1.CreateOptions{})
	if err != nil {
		if apierrors.IsAlreadyExists(err) {
			return errStateExists
		}
		return storeOpError("create", sec.Name, err)
	}
	st.version = created.GetResourceVersion()
	return nil
}

// Get implements AgentStateStore. A hash collision or a tampered object
// (data.id differs from id), and an object of an unknown state-version, are
// reported as errStateNotFound and logged by object name only.
func (s *k8sSecretStateStore) Get(ctx context.Context, id string) (*substrateAgentState, error) {
	if err := s.checkConfigured(); err != nil {
		return nil, err
	}
	name := substrateStateObjectName(id)
	sec, err := s.secrets().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, errStateNotFound
		}
		return nil, storeOpError("get", name, err)
	}
	st, err := decodeSubstrateState(sec)
	if err != nil {
		runtimeLog.Warn("substrate: skipping unreadable agent state object", "object", name)
		return nil, errStateNotFound
	}
	if st.ID != id {
		runtimeLog.Warn("substrate: agent state object id does not match its name; treating as not found", "object", name)
		return nil, errStateNotFound
	}
	return st, nil
}

// Update implements AgentStateStore. The compare-and-swap is the API
// server's own resourceVersion check: s.version always comes from a
// Create, Get, Update or List response, which carries the object's current
// resourceVersion.
func (s *k8sSecretStateStore) Update(ctx context.Context, st *substrateAgentState) error {
	if err := s.checkConfigured(); err != nil {
		return err
	}
	sec, err := s.encode(st)
	if err != nil {
		return err
	}
	updated, err := s.secrets().Update(ctx, sec, metav1.UpdateOptions{})
	if err != nil {
		switch {
		case apierrors.IsConflict(err):
			return errStateConflict
		case apierrors.IsNotFound(err):
			return errStateNotFound
		}
		return storeOpError("update", sec.Name, err)
	}
	st.version = updated.GetResourceVersion()
	return nil
}

// Delete implements AgentStateStore.
func (s *k8sSecretStateStore) Delete(ctx context.Context, id string, version string) error {
	if err := s.checkConfigured(); err != nil {
		return err
	}
	name := substrateStateObjectName(id)
	opts := metav1.DeleteOptions{}
	if version != "" {
		opts.Preconditions = &metav1.Preconditions{ResourceVersion: &version}
	}
	if err := s.secrets().Delete(ctx, name, opts); err != nil {
		switch {
		case apierrors.IsNotFound(err):
			return nil
		case apierrors.IsConflict(err):
			return errStateConflict
		}
		return storeOpError("delete", name, err)
	}
	return nil
}

// List implements AgentStateStore. Objects of an unknown state-version,
// malformed objects, and objects whose name is not the hash of their own
// data.id are skipped and logged by object name only.
func (s *k8sSecretStateStore) List(ctx context.Context, atespace string) ([]*substrateAgentState, error) {
	if err := s.checkConfigured(); err != nil {
		return nil, err
	}
	selector := substrateStateManagedByKey + "=" + substrateStateManagedByVal
	if atespace != "" {
		selector += "," + substrateStateAtespaceKey + "=" + atespace
	}
	var out []*substrateAgentState
	cont := ""
	for page := 0; ; page++ {
		if page >= maxSubstrateStateListPages {
			return nil, fmt.Errorf("substrate: list agent state: exceeded %d pages", maxSubstrateStateListPages)
		}
		list, err := s.secrets().List(ctx, metav1.ListOptions{
			LabelSelector: selector,
			Limit:         substrateStateListPageSize,
			Continue:      cont,
		})
		if err != nil {
			return nil, fmt.Errorf("substrate: list agent state: %s", apiErrReason(err))
		}
		for i := range list.Items {
			sec := &list.Items[i]
			st, err := decodeSubstrateState(sec)
			if err != nil {
				runtimeLog.Warn("substrate: skipping unreadable agent state object", "object", sec.GetName())
				continue
			}
			if substrateStateObjectName(st.ID) != sec.GetName() {
				runtimeLog.Warn("substrate: agent state object id does not match its name; skipping", "object", sec.GetName())
				continue
			}
			if atespace != "" {
				if ns, _, _ := splitSubstrateID(st.ID); ns != atespace {
					runtimeLog.Warn("substrate: agent state object atespace label does not match its id; skipping", "object", sec.GetName())
					continue
				}
			}
			out = append(out, st)
		}
		next := list.GetContinue()
		if next != "" && next == cont {
			return nil, fmt.Errorf("substrate: list agent state: server returned a repeated continue token")
		}
		cont = next
		if cont == "" {
			break
		}
	}
	return out, nil
}
