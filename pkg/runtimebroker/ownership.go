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
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/agent"
	"github.com/GoogleCloudPlatform/scion/pkg/api"
)

// Durable ownership records of a flat Runtime Broker instance
// (ptone/scion#3274, P2.3 note section 2a). Each instance keeps them in its
// own broker state root, never in an agent home. They are the authority for
// which agents (including file-only agents with no runtime object) the
// instance may operate on; the reserved runtime label
// (api.LabelRuntimeBrokerID) is the authority for runtime objects. A record
// is the durable mirror of the launch journal (launchRecord handles), not a
// second state machine.
//
// Key: (projectID, agentID). The agent slug is a locator, indexed with a
// uniqueness check so a different agent reusing a slug never inherits the
// holder's record. Writes are serialized per key, carry a monotonically
// increasing revision, and never move a state backwards. Deleted records and
// runs stay as tombstones (no garbage collection in P2); a handle append
// needs an existing live run, so a late callback can never recreate or
// revive a record or run. Releasing a deleted agent's slug (ReleaseSlug)
// lets a new agent ID use it while the tombstone stays.

// Record and run states, in their only allowed order.
const (
	OwnershipStateProvisioning = "provisioning" // run only
	OwnershipStateActive       = "active"       // record only
	OwnershipStateCreated      = "created"      // run only
	OwnershipStateDeleting     = "deleting"
	OwnershipStateDeleted      = "deleted"
)

// Resource states.
const (
	OwnedResourceRecorded = "recorded"
	OwnedResourceAbsent   = "absent"
)

var runStateOrder = map[string]int{OwnershipStateProvisioning: 0, OwnershipStateCreated: 1, OwnershipStateDeleting: 2, OwnershipStateDeleted: 3}
var recordStateOrder = map[string]int{OwnershipStateActive: 0, OwnershipStateDeleting: 1, OwnershipStateDeleted: 2}

const ownershipSchemaVersion = 1

// OwnedResource is one runtime object a run created (a launch resource
// handle). UID is the identity a delete is fenced on.
type OwnedResource struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
	UID       string `json:"uid"`
	State     string `json:"state"`
}

// OwnedRun is one run of an owned agent.
type OwnedRun struct {
	RunID     string          `json:"runId"`
	State     string          `json:"state"`
	Resources []OwnedResource `json:"resources,omitempty"`
}

// OwnershipRecord is the durable ownership record of one agent.
type OwnershipRecord struct {
	SchemaVersion   int        `json:"schemaVersion"`
	Revision        int64      `json:"revision"`
	RuntimeBrokerID string     `json:"runtimeBrokerId"`
	ProjectID       string     `json:"projectId"`
	AgentID         string     `json:"agentId"`
	AgentSlug       string     `json:"agentSlug"`
	State           string     `json:"state"`
	Runs            []OwnedRun `json:"runs"`
	UpdatedAt       time.Time  `json:"updatedAt"`
}

// Run returns the run with this ID, or nil.
func (r *OwnershipRecord) Run(runID string) *OwnedRun {
	for i := range r.Runs {
		if r.Runs[i].RunID == runID {
			return &r.Runs[i]
		}
	}
	return nil
}

// OwnsUID reports whether a run of an active record recorded the object with
// this UID (and its absence has not been established).
func (r *OwnershipRecord) OwnsUID(uid string) bool {
	if uid == "" || r.State != OwnershipStateActive {
		return false
	}
	for _, run := range r.Runs {
		for _, res := range run.Resources {
			if res.UID == uid && res.State == OwnedResourceRecorded {
				return true
			}
		}
	}
	return false
}

// Errors returned by the store.
var (
	ErrOwnershipKeyInvalid  = errors.New("invalid ownership key")
	ErrOwnershipNotRecorded = errors.New("not recorded by this Runtime Broker instance")
	ErrOwnershipSlugHeld    = errors.New("agent slug is held by another agent of this Runtime Broker instance")
	ErrOwnershipStateOrder  = errors.New("ownership state cannot move backwards")
	ErrOwnershipUnreadable  = errors.New("ownership record unreadable")
	ErrOwnershipConflict    = errors.New("ownership is claimed by more than one configured Runtime Broker instance")
)

// OwnershipAgentKey and OwnershipSlugKey are the ownership keys compared
// across a host's instances (LiveKeys, SetConflicting).
func OwnershipAgentKey(projectID, agentID string) string { return "agent:" + projectID + "/" + agentID }

// OwnershipSlugKey is the ownership key of a slug in a project.
func OwnershipSlugKey(projectID, slug string) string { return "slug:" + projectID + "/" + slug }

// OwnershipStore reads and writes one instance's ownership records under
// <stateDir>/ownership/<projectId>/<agentId>.json, with the slug index under
// <stateDir>/ownership/<projectId>/by-slug/<agentSlug>.
type OwnershipStore struct {
	dir      string
	brokerID string
	now      func() time.Time

	locksMu sync.Mutex
	locks   map[string]*sync.Mutex
	// slugMu serializes slug index changes within a project.
	slugMu sync.Mutex
	// conflicting are keys another configured instance claims too; every
	// operation on them is refused (set once, before the store is used).
	conflicting map[string]bool
}

// SetConflicting marks ownership keys (OwnershipAgentKey,
// OwnershipSlugKey) that another configured instance also claims. Reads and
// writes of those keys fail with ErrOwnershipConflict. Call it once, before
// the store is used.
func (s *OwnershipStore) SetConflicting(keys map[string]bool) {
	s.conflicting = make(map[string]bool, len(keys))
	for k, v := range keys {
		if v {
			s.conflicting[k] = true
		}
	}
}

// ConflictingLabels reports whether a runtime object's labels name an agent
// or slug whose ownership is conflicting.
func (s *OwnershipStore) ConflictingLabels(labels map[string]string) bool {
	if len(s.conflicting) == 0 {
		return false
	}
	p := labels["scion.project_id"]
	return s.conflicting[OwnershipAgentKey(p, labels["agent_id"])] || s.conflicting[OwnershipSlugKey(p, labels["scion.name"])]
}

func (s *OwnershipStore) agentConflict(projectID, agentID string) error {
	if s.conflicting[OwnershipAgentKey(projectID, agentID)] {
		return fmt.Errorf("%w: agent %q in project %q", ErrOwnershipConflict, agentID, projectID)
	}
	return nil
}

// LiveKeys returns the agent and slug keys of every live (not deleted)
// record, for comparison with the host's other instances. An unreadable
// record is an error.
func (s *OwnershipStore) LiveKeys() ([]string, error) {
	recs, err := s.List()
	if err != nil {
		return nil, err
	}
	var keys []string
	for _, r := range recs {
		if r.State == OwnershipStateDeleted {
			continue
		}
		keys = append(keys, OwnershipAgentKey(r.ProjectID, r.AgentID), OwnershipSlugKey(r.ProjectID, r.AgentSlug))
	}
	return keys, nil
}

// NewOwnershipStore returns the store of the instance with this Runtime
// Broker ID whose broker state root is stateDir.
func NewOwnershipStore(stateDir, runtimeBrokerID string) *OwnershipStore {
	return &OwnershipStore{dir: filepath.Join(stateDir, "ownership"), brokerID: runtimeBrokerID, now: time.Now, locks: map[string]*sync.Mutex{}}
}

// RuntimeBrokerID is the owner this store records.
func (s *OwnershipStore) RuntimeBrokerID() string { return s.brokerID }

func (s *OwnershipStore) keyLock(projectID, agentID string) *sync.Mutex {
	s.locksMu.Lock()
	defer s.locksMu.Unlock()
	k := projectID + "\x00" + agentID
	if s.locks[k] == nil {
		s.locks[k] = &sync.Mutex{}
	}
	return s.locks[k]
}

func validOwnershipElement(v string) bool {
	return v != "" && v != "." && v != ".." && v != "by-slug" && !strings.HasPrefix(v, ".") &&
		!strings.ContainsAny(v, "/\\\x00") && filepath.Clean(v) == v
}

func (s *OwnershipStore) recordPath(projectID, agentID string) (string, error) {
	if !validOwnershipElement(projectID) || !validOwnershipElement(agentID) {
		return "", fmt.Errorf("%w: project %q, agent %q", ErrOwnershipKeyInvalid, projectID, agentID)
	}
	return filepath.Join(s.dir, projectID, agentID+".json"), nil
}

func (s *OwnershipStore) slugPath(projectID, slug string) (string, error) {
	if !validOwnershipElement(projectID) || !validOwnershipElement(slug) {
		return "", fmt.Errorf("%w: project %q, slug %q", ErrOwnershipKeyInvalid, projectID, slug)
	}
	return filepath.Join(s.dir, projectID, "by-slug", slug), nil
}

// Get returns the record for the key; ok is false when there is none. A
// record that cannot be read or does not belong to this instance and key is
// an error (unresolved), never "no record".
func (s *OwnershipStore) Get(projectID, agentID string) (*OwnershipRecord, bool, error) {
	l := s.keyLock(projectID, agentID)
	l.Lock()
	defer l.Unlock()
	return s.read(projectID, agentID)
}

func (s *OwnershipStore) read(projectID, agentID string) (*OwnershipRecord, bool, error) {
	if err := s.agentConflict(projectID, agentID); err != nil {
		return nil, false, err
	}
	return s.readFile(projectID, agentID)
}

// readFile reads the record without the conflict check (List, LiveKeys).
func (s *OwnershipStore) readFile(projectID, agentID string) (*OwnershipRecord, bool, error) {
	p, err := s.recordPath(projectID, agentID)
	if err != nil {
		return nil, false, err
	}
	data, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("%w: %s: %v", ErrOwnershipUnreadable, p, err)
	}
	var rec OwnershipRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, false, fmt.Errorf("%w: %s: %v", ErrOwnershipUnreadable, p, err)
	}
	if rec.SchemaVersion != ownershipSchemaVersion || rec.RuntimeBrokerID != s.brokerID || rec.ProjectID != projectID || rec.AgentID != agentID {
		return nil, false, fmt.Errorf("%w: %s does not match this instance or key", ErrOwnershipUnreadable, p)
	}
	return &rec, true, nil
}

// SlugHolder returns the agent ID holding slug in the project, or "". A
// conflicting slug is an error.
func (s *OwnershipStore) SlugHolder(projectID, slug string) (string, error) {
	if s.conflicting[OwnershipSlugKey(projectID, slug)] {
		return "", fmt.Errorf("%w: slug %q in project %q", ErrOwnershipConflict, slug, projectID)
	}
	return s.slugHolder(projectID, slug)
}

func (s *OwnershipStore) slugHolder(projectID, slug string) (string, error) {
	p, err := s.slugPath(projectID, slug)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("%w: %s: %v", ErrOwnershipUnreadable, p, err)
	}
	return strings.TrimSpace(string(data)), nil
}

// mutate applies fn to the record under the key lock and writes it with the
// next revision.
func (s *OwnershipStore) mutate(projectID, agentID string, fn func(*OwnershipRecord) error) (*OwnershipRecord, error) {
	l := s.keyLock(projectID, agentID)
	l.Lock()
	defer l.Unlock()
	rec, ok, err := s.read(projectID, agentID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("%w: agent %q in project %q", ErrOwnershipNotRecorded, agentID, projectID)
	}
	if err := fn(rec); err != nil {
		return nil, err
	}
	rec.Revision++
	rec.UpdatedAt = s.now().UTC()
	p, _ := s.recordPath(projectID, agentID)
	if err := writeOwnershipFile(p, mustJSON(rec)); err != nil {
		return nil, err
	}
	return rec, nil
}

// BeginRun records a run in the provisioning state, creating the record
// (and claiming the slug) when the agent has none. It is called before any
// agent file becomes discoverable and before the runtime creates anything.
// A slug held by a different agent ID is refused; a deleting or deleted
// record cannot take a new run.
func (s *OwnershipStore) BeginRun(projectID, agentID, slug, runID string) error {
	if runID == "" {
		return errors.New("ownership: a run ID is required")
	}
	if _, err := s.slugPath(projectID, slug); err != nil {
		return err
	}
	l := s.keyLock(projectID, agentID)
	l.Lock()
	defer l.Unlock()
	rec, ok, err := s.read(projectID, agentID)
	if err != nil {
		return err
	}
	s.slugMu.Lock()
	defer s.slugMu.Unlock()
	holder, err := s.SlugHolder(projectID, slug)
	if err != nil {
		return err
	}
	if holder != "" && holder != agentID {
		return fmt.Errorf("%w: slug %q in project %q is held by agent %s", ErrOwnershipSlugHeld, slug, projectID, holder)
	}
	if !ok {
		rec = &OwnershipRecord{SchemaVersion: ownershipSchemaVersion, RuntimeBrokerID: s.brokerID,
			ProjectID: projectID, AgentID: agentID, AgentSlug: slug, State: OwnershipStateActive}
	}
	if rec.State != OwnershipStateActive {
		return fmt.Errorf("%w: agent %q is %s", ErrOwnershipStateOrder, agentID, rec.State)
	}
	if rec.AgentSlug != slug {
		return fmt.Errorf("%w: agent %q is recorded with slug %q, not %q", ErrOwnershipKeyInvalid, agentID, rec.AgentSlug, slug)
	}
	if existing := rec.Run(runID); existing == nil {
		rec.Runs = append(rec.Runs, OwnedRun{RunID: runID, State: OwnershipStateProvisioning})
	} else if runStateOrder[existing.State] >= runStateOrder[OwnershipStateDeleting] {
		return fmt.Errorf("%w: run %q of agent %q is %s and cannot be revived", ErrOwnershipStateOrder, runID, agentID, existing.State)
	}
	rec.Revision++
	rec.UpdatedAt = s.now().UTC()
	p, _ := s.recordPath(projectID, agentID)
	if err := writeOwnershipFile(p, mustJSON(rec)); err != nil {
		return err
	}
	if holder == "" {
		sp, _ := s.slugPath(projectID, slug)
		if err := writeOwnershipFile(sp, []byte(agentID+"\n")); err != nil {
			return err
		}
	}
	return nil
}

// AddResource records an object a run created. It is refused once the run
// or record is deleting or deleted, so a late callback never adds to (or
// recreates) a record being removed.
func (s *OwnershipStore) AddResource(projectID, agentID, runID string, h api.ResourceHandle) error {
	if h.UID == "" {
		return errors.New("ownership: a resource UID is required")
	}
	_, err := s.mutate(projectID, agentID, func(r *OwnershipRecord) error {
		run := r.Run(runID)
		if run == nil {
			return fmt.Errorf("%w: run %q of agent %q", ErrOwnershipNotRecorded, runID, agentID)
		}
		if r.State != OwnershipStateActive || runStateOrder[run.State] >= runStateOrder[OwnershipStateDeleting] {
			return fmt.Errorf("%w: run %q of agent %q is %s", ErrOwnershipStateOrder, runID, agentID, run.State)
		}
		for _, res := range run.Resources {
			if res.UID == h.UID {
				return nil
			}
		}
		run.Resources = append(run.Resources, OwnedResource{Kind: h.Kind, Namespace: h.Namespace, Name: h.Name, UID: h.UID, State: OwnedResourceRecorded})
		return nil
	})
	return err
}

// SetRunState moves a run forward; a backward move is refused.
func (s *OwnershipStore) SetRunState(projectID, agentID, runID, state string) error {
	if _, ok := runStateOrder[state]; !ok {
		return fmt.Errorf("ownership: unknown run state %q", state)
	}
	_, err := s.mutate(projectID, agentID, func(r *OwnershipRecord) error {
		run := r.Run(runID)
		if run == nil {
			return fmt.Errorf("%w: run %q of agent %q", ErrOwnershipNotRecorded, runID, agentID)
		}
		if runStateOrder[state] < runStateOrder[run.State] {
			return fmt.Errorf("%w: run %q from %s to %s", ErrOwnershipStateOrder, runID, run.State, state)
		}
		run.State = state
		return nil
	})
	return err
}

// SetRecordState moves the record forward (active → deleting → deleted); a
// backward move is refused. Deleted records stay as tombstones until Purge.
func (s *OwnershipStore) SetRecordState(projectID, agentID, state string) error {
	if _, ok := recordStateOrder[state]; !ok {
		return fmt.Errorf("ownership: unknown record state %q", state)
	}
	_, err := s.mutate(projectID, agentID, func(r *OwnershipRecord) error {
		if recordStateOrder[state] < recordStateOrder[r.State] {
			return fmt.Errorf("%w: agent %q from %s to %s", ErrOwnershipStateOrder, agentID, r.State, state)
		}
		r.State = state
		if state == OwnershipStateDeleted {
			for i := range r.Runs {
				r.Runs[i].State = OwnershipStateDeleted
			}
		}
		return nil
	})
	return err
}

// MarkAbsent records that the object with this UID is confirmed gone. The
// caller establishes confirmation (a successful complete scoped read or an
// exact NotFound); an inspection failure must not call this.
func (s *OwnershipStore) MarkAbsent(projectID, agentID, uid string) error {
	_, err := s.mutate(projectID, agentID, func(r *OwnershipRecord) error {
		for i := range r.Runs {
			for j := range r.Runs[i].Resources {
				if r.Runs[i].Resources[j].UID == uid {
					r.Runs[i].Resources[j].State = OwnedResourceAbsent
				}
			}
		}
		return nil
	})
	return err
}

// ReleaseSlug releases a deleted agent's slug reservation once every
// recorded object is confirmed absent; the caller holds the shared path
// lock (S2) and has confirmed the agent's files are gone. The record stays
// as a tombstone. Anything else is refused.
func (s *OwnershipStore) ReleaseSlug(projectID, agentID string) error {
	l := s.keyLock(projectID, agentID)
	l.Lock()
	defer l.Unlock()
	rec, ok, err := s.read(projectID, agentID)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: agent %q in project %q", ErrOwnershipNotRecorded, agentID, projectID)
	}
	if rec.State != OwnershipStateDeleted {
		return fmt.Errorf("%w: agent %q is %s, not deleted", ErrOwnershipStateOrder, agentID, rec.State)
	}
	for _, run := range rec.Runs {
		for _, res := range run.Resources {
			if res.State != OwnedResourceAbsent {
				return fmt.Errorf("ownership: agent %q still has object %s (%s) not confirmed absent", agentID, res.UID, res.Name)
			}
		}
	}
	s.slugMu.Lock()
	defer s.slugMu.Unlock()
	holder, err := s.SlugHolder(projectID, rec.AgentSlug)
	if err != nil {
		return err
	}
	if holder != agentID {
		return nil
	}
	sp, _ := s.slugPath(projectID, rec.AgentSlug)
	if err := os.Remove(sp); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	syncDir(filepath.Dir(sp))
	return nil
}

// RepairSlugIndex restores record/index consistency before the instance
// serves operations: every live (not deleted) record holds its slug; an
// index entry naming a missing or deleted record is reported as a
// conflict, never silently reassigned. It returns the inconsistencies it
// could not repair (two live records claiming one slug, unreadable index or
// records), which the caller treats as unresolved ownership.
func (s *OwnershipStore) RepairSlugIndex() ([]string, error) {
	recs, err := s.List()
	if err != nil {
		return nil, err
	}
	s.slugMu.Lock()
	defer s.slugMu.Unlock()
	var problems []string
	live := map[string]string{} // project + slug -> agentID
	for _, r := range recs {
		if r.State == OwnershipStateDeleted {
			continue
		}
		k := r.ProjectID + "\x00" + r.AgentSlug
		if other, dup := live[k]; dup {
			problems = append(problems, fmt.Sprintf("slug %q in project %q is claimed by live agents %s and %s", r.AgentSlug, r.ProjectID, other, r.AgentID))
			continue
		}
		live[k] = r.AgentID
		holder, err := s.slugHolder(r.ProjectID, r.AgentSlug)
		if err != nil {
			problems = append(problems, err.Error())
			continue
		}
		switch holder {
		case r.AgentID:
		case "":
			sp, _ := s.slugPath(r.ProjectID, r.AgentSlug)
			if err := writeOwnershipFile(sp, []byte(r.AgentID+"\n")); err != nil {
				return nil, err
			}
		default:
			problems = append(problems, fmt.Sprintf("slug %q in project %q is indexed to agent %s but live agent %s records it", r.AgentSlug, r.ProjectID, holder, r.AgentID))
		}
	}
	return problems, nil
}

// List returns every record (tombstones included), sorted by key. An
// unreadable record is returned as an error, never skipped.
func (s *OwnershipStore) List() ([]OwnershipRecord, error) {
	projects, err := os.ReadDir(s.dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []OwnershipRecord
	for _, p := range projects {
		if !p.IsDir() {
			continue
		}
		files, err := os.ReadDir(filepath.Join(s.dir, p.Name()))
		if err != nil {
			return nil, err
		}
		for _, f := range files {
			agentID, ok := strings.CutSuffix(f.Name(), ".json")
			if f.IsDir() || !ok || strings.HasPrefix(f.Name(), ".") {
				continue
			}
			l := s.keyLock(p.Name(), agentID)
			l.Lock()
			rec, found, err := s.readFile(p.Name(), agentID)
			l.Unlock()
			if err != nil {
				return nil, err
			}
			if found {
				out = append(out, *rec)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ProjectID != out[j].ProjectID {
			return out[i].ProjectID < out[j].ProjectID
		}
		return out[i].AgentID < out[j].AgentID
	})
	return out, nil
}

func mustJSON(v any) []byte {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		panic(err) // the record types always marshal
	}
	return data
}

// writeOwnershipFile writes data atomically: temp file, fsync, rename, then
// fsync of the directory (and of a directory it had to create), so a new
// file's directory entry is durable too.
func writeOwnershipFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	created, err := mkdirAllSynced(dir)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	syncDir(dir)
	for _, d := range created {
		syncDir(filepath.Dir(d))
	}
	return nil
}

// mkdirAllSynced creates dir (0700) and returns the directories it created.
func mkdirAllSynced(dir string) ([]string, error) {
	var created []string
	for d := dir; ; d = filepath.Dir(d) {
		if _, err := os.Stat(d); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		created = append(created, d)
		if filepath.Dir(d) == d {
			break
		}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return created, nil
}

func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
}

// fileAgentOwned reports whether this flat instance's durable record claims
// the agent holding slug in the project (the authority for file-only
// agents). Missing, unreadable or deleted records are not owned.
func (s *Server) fileAgentOwned(projectID, slug string) bool {
	if s.ownership == nil || projectID == "" {
		return false
	}
	agentID, err := s.ownership.SlugHolder(projectID, slug)
	if err != nil || agentID == "" {
		return false
	}
	rec, ok, err := s.ownership.Get(projectID, agentID)
	return err == nil && ok && rec.State == OwnershipStateActive
}

// beginOwnedRun records a flat instance's run before the operation creates
// any agent file or runtime object. A legacy Runtime Broker records nothing.
// A create may start a new record; any other operation (start, restart of
// an existing agent) requires the agent's existing live record, so an agent
// whose ownership is not established is never adopted by starting it. A
// missing project or agent ID, a slug held by another agent, or a write
// failure refuses the operation before any side effect.
func (s *Server) beginOwnedRun(projectID, agentID, slug, runID string, create bool) error {
	if s.ownership == nil {
		return nil
	}
	if projectID == "" || agentID == "" {
		return fmt.Errorf("flat Runtime Broker %s: a project ID and an agent ID are required to record ownership", s.ownership.RuntimeBrokerID())
	}
	if !create {
		rec, ok, err := s.ownership.Get(projectID, agentID)
		if err != nil {
			return err
		}
		if !ok || rec.State != OwnershipStateActive {
			return fmt.Errorf("%w: agent %s in project %s has no live ownership record of this Runtime Broker instance", ErrOwnershipNotRecorded, agentID, projectID)
		}
	}
	return s.ownership.BeginRun(projectID, agentID, slug, runID)
}

// ownedStart is one flat start's ownership journal mirror: it records every
// created runtime object (the runtime's ObserveResourceCreated) into the
// durable record and latches the first failure. The in-memory handles are
// the trusted current launch journal for cleanup when mirroring failed.
type ownedStart struct {
	store                     *OwnershipStore
	projectID, agentID, runID string

	mu      sync.Mutex
	err     error
	handles []api.ResourceHandle
}

func (o *ownedStart) observe(h api.ResourceHandle) {
	o.mu.Lock()
	o.handles = append(o.handles, h)
	failed := o.err != nil
	o.mu.Unlock()
	if failed {
		return
	}
	if err := o.store.AddResource(o.projectID, o.agentID, o.runID, h); err != nil {
		o.mu.Lock()
		if o.err == nil {
			o.err = fmt.Errorf("recording runtime object %s (%s) of run %s: %w", h.Name, h.UID, o.runID, err)
		}
		o.mu.Unlock()
	}
}

func (o *ownedStart) latched() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.err
}

func (o *ownedStart) snapshot() []api.ResourceHandle {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]api.ResourceHandle(nil), o.handles...)
}

// checkpoint refuses the next resource-creating call once mirroring failed.
func (o *ownedStart) checkpoint(context.Context, string) error {
	if err := o.latched(); err != nil {
		return fmt.Errorf("flat Runtime Broker: not creating more runtime objects: %w", err)
	}
	return nil
}

// chainCheckpoint runs first, then second (either may be nil).
func chainCheckpoint(first, second func(context.Context, string) error) func(context.Context, string) error {
	switch {
	case first == nil:
		return second
	case second == nil:
		return first
	}
	return func(ctx context.Context, step string) error {
		if err := first(ctx, step); err != nil {
			return err
		}
		return second(ctx, step)
	}
}

// installOwnedStart wires a flat start's ownership mirror into opts: the
// observer and a checkpoint that refuses further creates after a mirroring
// failure (chained before any checkpoint already set).
func (s *Server) installOwnedStart(opts *api.StartOptions, projectID, agentID string) {
	if s.ownership == nil || opts.RunID == "" {
		return
	}
	o := &ownedStart{store: s.ownership, projectID: projectID, agentID: agentID, runID: opts.RunID}
	s.ownedStarts.Store(opts.RunID, o)
	opts.ObserveResourceCreated = o.observe
	opts.Checkpoint = chainCheckpoint(o.checkpoint, opts.Checkpoint)
}

// completeOwnedStart finishes a flat start's ownership mirror after
// Manager.Start returned. A failed start is returned as is (its run stays
// provisioning; the existing failure cleanup applies). A successful start
// whose mirroring failed (including on its last object) is undone: when
// cleanup is set, exactly the journaled objects are removed with their UID
// preconditions (the synchronous path); the async path's own failure
// cleanup removes them otherwise. Only then is the run marked created.
func (s *Server) completeOwnedStart(ctx context.Context, mgr agent.Manager, runID string, startErr error, cleanup bool) error {
	v, ok := s.ownedStarts.LoadAndDelete(runID)
	if !ok {
		return startErr
	}
	o := v.(*ownedStart)
	if startErr != nil {
		return startErr
	}
	err := o.latched()
	if err == nil {
		err = o.store.SetRunState(o.projectID, o.agentID, o.runID, OwnershipStateCreated)
	}
	if err == nil {
		return nil
	}
	if cleanup && mgr != nil {
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
		defer cancel()
		if cerr := mgr.CleanupLaunch(cctx, o.snapshot()); cerr != nil {
			s.agentLifecycleLog.Error("Undoing a start whose ownership could not be recorded failed; its objects stay labelled for reconciliation",
				"run_id", runID, "error", cerr)
		}
	}
	return fmt.Errorf("flat Runtime Broker: the agent's runtime objects could not be recorded, so the start was undone: %w", err)
}

// Reconstruct ensures the record of a runtime object that carries this
// instance's owner label: an object whose labels give the complete project
// ID, agent ID, slug and run ID, and which has an object identity (its
// container or pod ID), is recorded (record and run created as needed, the
// object added) without reviving a terminal run. Missing metadata, or a
// record that contradicts the labels, is an error (unresolved).
func (s *OwnershipStore) Reconstruct(o api.AgentInfo) error {
	if o.Labels[api.LabelRuntimeBrokerID] != s.brokerID {
		return fmt.Errorf("object %s is not labelled for this instance", o.Name)
	}
	projectID := o.Labels["scion.project_id"]
	agentID := o.Labels["agent_id"]
	slug := o.Labels["scion.name"]
	runID := o.Labels[api.LabelRunID]
	uid := o.ContainerID
	if uid == "" {
		uid = o.ID
	}
	if projectID == "" || agentID == "" || slug == "" || runID == "" || uid == "" {
		return fmt.Errorf("labels are incomplete (project, agent, name, run and object ID are all required)")
	}
	rec, ok, err := s.Get(projectID, agentID)
	if err != nil {
		return err
	}
	if ok {
		if rec.AgentSlug != slug {
			return fmt.Errorf("record names slug %q, the object %q", rec.AgentSlug, slug)
		}
		if run := rec.Run(runID); run != nil {
			if runStateOrder[run.State] >= runStateOrder[OwnershipStateDeleting] {
				return fmt.Errorf("run %s is %s and cannot be revived", runID, run.State)
			}
			if rec.OwnsUID(uid) {
				return nil
			}
			return s.AddResource(projectID, agentID, runID, api.ResourceHandle{Kind: reconstructedKind(o), Name: o.Name, UID: uid})
		}
	}
	if err := s.BeginRun(projectID, agentID, slug, runID); err != nil {
		return err
	}
	if err := s.AddResource(projectID, agentID, runID, api.ResourceHandle{Kind: reconstructedKind(o), Name: o.Name, UID: uid}); err != nil {
		return err
	}
	return s.SetRunState(projectID, agentID, runID, OwnershipStateCreated)
}

// reconstructedKind is the resource kind of a listed agent object.
func reconstructedKind(o api.AgentInfo) string {
	if o.Runtime == "kubernetes" {
		return api.ResourceKindPod
	}
	return api.ResourceKindContainer
}

// ownedDelete is a flat instance's whole-agent delete of an owned agent.
type ownedDelete struct {
	projectID, agentID string
}

// beginOwnedDelete decides whether a flat instance may delete the agent
// holding slug in the project and, for a whole-agent delete, moves its
// record to deleting before anything is removed. A legacy Runtime Broker
// (no store) is always allowed. A file-only agent (no runtime object) is
// deleted only when this instance's record owns it; an agent with a runtime
// object was found through this instance's owner-filtered list, so its
// label already establishes ownership even when its record is missing (the
// next start-up reconstructs it).
func (s *Server) beginOwnedDelete(projectID, slug string, hasObject, wholeAgent bool) (*ownedDelete, error) {
	if s.ownership == nil {
		return nil, nil
	}
	agentID := ""
	if projectID != "" {
		holder, err := s.ownership.SlugHolder(projectID, slug)
		if err != nil {
			return nil, err
		}
		agentID = holder
	}
	var rec *OwnershipRecord
	if agentID != "" {
		r, ok, err := s.ownership.Get(projectID, agentID)
		if err != nil {
			return nil, err
		}
		if ok {
			rec = r
		}
	}
	if rec == nil || rec.State == OwnershipStateDeleted {
		if !hasObject {
			return nil, fmt.Errorf("%w: file-only agent %q in project %q", ErrOwnershipNotRecorded, slug, projectID)
		}
		return nil, nil
	}
	if !wholeAgent {
		return nil, nil
	}
	if err := s.ownership.SetRecordState(projectID, agentID, OwnershipStateDeleting); err != nil {
		return nil, err
	}
	return &ownedDelete{projectID: projectID, agentID: agentID}, nil
}

// finishOwnedDelete completes a whole-agent delete's record after the
// runtime delete succeeded: every recorded object is removed or confirmed
// gone through the UID-precondition cleanup (an object already gone, or a
// name now held by another UID, is confirmed absent), then the record is
// marked deleted (its tombstone stays) and the slug is released. If absence
// cannot be established the record stays deleting and the slug stays
// reserved, so nothing reuses it early.
func (s *Server) finishOwnedDelete(ctx context.Context, mgr agent.Manager, od *ownedDelete) {
	if od == nil || s.ownership == nil {
		return
	}
	rec, ok, err := s.ownership.Get(od.projectID, od.agentID)
	if err != nil || !ok {
		return
	}
	var handles []api.ResourceHandle
	for _, run := range rec.Runs {
		for _, res := range run.Resources {
			if res.State == OwnedResourceRecorded {
				handles = append(handles, api.ResourceHandle{Kind: res.Kind, Namespace: res.Namespace, Name: res.Name, UID: res.UID})
			}
		}
	}
	if len(handles) > 0 && mgr != nil {
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
		defer cancel()
		if err := mgr.CleanupLaunch(cctx, handles); err != nil {
			s.agentLifecycleLog.Warn("Agent delete: could not confirm every recorded object is gone; ownership record kept deleting",
				"agent_id", od.agentID, "project_id", od.projectID, "error", err)
			return
		}
	}
	for _, h := range handles {
		if err := s.ownership.MarkAbsent(od.projectID, od.agentID, h.UID); err != nil {
			return
		}
	}
	if err := s.ownership.SetRecordState(od.projectID, od.agentID, OwnershipStateDeleted); err != nil {
		return
	}
	if err := s.ownership.ReleaseSlug(od.projectID, od.agentID); err != nil {
		s.agentLifecycleLog.Warn("Agent delete: slug not released", "agent_id", od.agentID, "project_id", od.projectID, "error", err)
	}
}
