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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/config/opsettings"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
)

// The artifacts settings section on the server-config API (ptone/scion#7107
// phase 2b). The section is DB-only: it has no settings.yaml form, so it
// is written through the DB-mode PUT /api/v1/admin/server-config as an
// "artifacts" object, by an interactive admin session only (the whole
// section is refused for user access tokens, serverConfigTokenSections),
// and a file-mode hub refuses it. Every key a save changes is audited with
// its old and new value before the write.

// artifactsSection is the operational settings section of the artifact
// service.
const artifactsSection = "artifacts"

// The hub settings audit vocabulary, shared with the GCP permission-check
// and public URL audits; named generically for the artifacts records.
const (
	hubSettingAuditMutation      = gcpIAMAuditMutation      // hub_setting_update
	hubSettingNotAppliedMutation = gcpIAMNotAppliedMutation // hub_setting_update_not_applied
	hubSettingAuditTarget        = gcpIAMAuditTarget        // hub_setting
	hubSettingSurfaceKind        = gcpIAMSurfaceKind        // hub_settings
)

// artifactsBodyMember returns the value of the body's top-level artifacts
// member, matched as the JSON decoder matches member names, and whether
// the body has one.
func artifactsBodyMember(rawBody []byte) (json.RawMessage, bool) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(rawBody, &top); err != nil {
		return nil, false
	}
	want := foldJSONMemberName(artifactsSection)
	for name, v := range top {
		if foldJSONMemberName(name) == want {
			return v, true
		}
	}
	return nil, false
}

// rejectArtifactsInFileMode refuses a file-mode server-config write that
// carries the artifacts section, for every caller: the section lives only
// in the settings database. It reports whether it wrote the refusal.
func rejectArtifactsInFileMode(w http.ResponseWriter, rawBody []byte) bool {
	if _, ok := artifactsBodyMember(rawBody); !ok {
		return false
	}
	writeSettingsCodedKeysError(w, "db_only_section_rejected",
		"The artifacts settings section is stored only in the settings database; this hub has no settings database, so it cannot be saved here.",
		[]string{artifactsSection})
	return true
}

// buildArtifactsSectionDoc merges the body's artifacts object onto the
// current row: a key the object sends replaces the stored one, null clears
// it (back to its default), and a key it leaves out keeps its stored value.
// It returns the merged document, the stored document it was merged on
// (for the audit) and that row's revision (the write's compare-and-set
// base; 0 when there is no row). badRequest is set when the member is not
// a JSON object. The generic section merge (mergeSectionOnCurrentWith)
// patches only keys with a koanf path, and this DB-only section has none,
// so it is merged here; the section schema then checks every key.
func buildArtifactsSectionDoc(ctx context.Context, ops *OperationalSettings, raw json.RawMessage) (doc, before json.RawMessage, rev int64, badRequest string, err error) {
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil || obj == nil {
		return nil, nil, 0, "artifacts must be a JSON object of artifacts settings", nil
	}
	base := map[string]json.RawMessage{}
	row, err := ops.store.GetHubSetting(ctx, artifactsSection)
	switch {
	case err == nil:
		if len(bytes.TrimSpace(row.Value)) > 0 {
			if err := json.Unmarshal(row.Value, &base); err != nil {
				return nil, nil, 0, "", fmt.Errorf("decoding current artifacts row: %w", err)
			}
			if base == nil { // stored JSON null
				base = map[string]json.RawMessage{}
			}
		}
		rev = row.Revision
	case errors.Is(err, store.ErrNotFound):
	default:
		return nil, nil, 0, "", fmt.Errorf("reading current artifacts row: %w", err)
	}
	if before, err = json.Marshal(base); err != nil {
		return nil, nil, 0, "", fmt.Errorf("marshalling artifacts base: %w", err)
	}
	for k, v := range obj {
		if bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			delete(base, k)
			continue
		}
		base[k] = v
	}
	if doc, err = json.Marshal(base); err != nil {
		return nil, nil, 0, "", fmt.Errorf("marshalling artifacts doc: %w", err)
	}
	return doc, before, rev, "", nil
}

// checkArtifactsLinkOriginSave applies the save rules for link_origin to a
// merged artifacts document the section schema has accepted (so the value,
// when set, passed opsettings.ValidateLinkOrigin). It stores the value
// normalized (an empty one is removed), refuses one on the hub's own host
// name at any scheme or port, and warns about one under the hub's
// registrable domain (security review condition 5). refusal is the 422
// message, or "".
func (s *Server) checkArtifactsLinkOriginSave(doc json.RawMessage) (out json.RawMessage, warning, refusal string) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(doc, &m); err != nil {
		return doc, "", ""
	}
	raw, ok := m["link_origin"]
	if !ok {
		return doc, "", ""
	}
	var v string
	if json.Unmarshal(raw, &v) != nil {
		return doc, "", ""
	}
	lo, err := opsettings.ValidateLinkOrigin(v)
	if err != nil {
		return doc, "", err.Error()
	}
	if lo == "" {
		delete(m, "link_origin")
	} else {
		public := s.HubEndpoints().Public
		sameHost, sameSite := opsettings.LinkOriginHubConflict(lo, public)
		if sameHost {
			return doc, "", opsettings.ArtifactsLinkOriginKey + ": must not use the hub's own host name (" + public + ") at any scheme or port; use a separate host name that routes to this hub"
		}
		if sameSite {
			warning = opsettings.ArtifactsLinkOriginKey + " shares the hub's registrable domain, which gives little isolation; use a separate registrable domain"
			slog.Warn("artifacts settings: saved link_origin shares the hub's registrable domain",
				"link_origin", lo, "hub_public_url", public)
		}
		b, _ := json.Marshal(lo)
		m["link_origin"] = b
	}
	b, err := json.Marshal(m)
	if err != nil {
		return doc, "", ""
	}
	return b, warning, ""
}

// artifactsKeyChange is one artifacts key a save changes, with its stored
// value before and after ("" when absent).
type artifactsKeyChange struct {
	key, before, after string
}

// artifactsKeyChanges returns, sorted by key, the top-level keys whose
// value differs between two artifacts documents. Values compare as JSON
// (spacing does not count).
func artifactsKeyChanges(before, after json.RawMessage) []artifactsKeyChange {
	var b, a map[string]json.RawMessage
	_ = json.Unmarshal(before, &b)
	_ = json.Unmarshal(after, &a)
	keys := map[string]bool{}
	for k := range b {
		keys[k] = true
	}
	for k := range a {
		keys[k] = true
	}
	var out []artifactsKeyChange
	for k := range keys {
		bv, av := auditValue(b[k]), auditValue(a[k])
		_, inB := b[k]
		_, inA := a[k]
		if bv == av && inB == inA {
			continue
		}
		out = append(out, artifactsKeyChange{key: k, before: bv, after: av})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].key < out[j].key })
	return out
}

// auditValue renders a stored JSON value for an audit summary: a string
// as itself, anything else as compact JSON, an absent value as "".
func auditValue(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var buf bytes.Buffer
	if json.Compact(&buf, raw) == nil {
		return buf.String()
	}
	return string(raw)
}

// auditArtifactsSettingsChanges records each artifacts key a save changes
// before the section is written, through the hub settings audit path of
// auditPublicURLChange: one hub_setting_update mutation audit record per
// key, naming it (artifacts.<key>) with its old and new stored value and
// the actor. If a record cannot be written it returns an error and the
// caller does not make the change. A save that changes no key records
// nothing.
func (s *Server) auditArtifactsSettingsChanges(ctx context.Context, surface string, changes []artifactsKeyChange) error {
	if len(changes) == 0 {
		return nil
	}
	if s.store == nil {
		return errors.New("no store for the audit record")
	}
	for _, c := range changes {
		target := artifactsSection + "." + c.key
		rec := &store.MutationAuditRecord{
			MutationType:  hubSettingAuditMutation,
			TargetType:    hubSettingAuditTarget,
			TargetID:      target,
			BeforeSummary: c.before,
			AfterSummary:  c.after,
			ExecutorKind:  hubSettingSurfaceKind,
			ExecutorID:    surface,
		}
		auditActorFromContext(ctx).ApplyActor(rec)
		if rec.ActorPrincipalKind == "" || rec.ActorPrincipalID == "" {
			return fmt.Errorf("recording %s change: no actor", target)
		}
		rec.Timestamp = time.Now().UTC()
		if err := s.store.CreateMutationAudit(ctx, rec); err != nil {
			return fmt.Errorf("recording %s change: %w", target, err)
		}
		slog.Info("Artifacts setting changed",
			"key", target, "old", c.before, "new", c.after, "surface", surface,
			"principal_kind", rec.ActorPrincipalKind, "principal", rec.ActorPrincipalID,
			"audit_id", rec.ID)
	}
	return nil
}

// auditArtifactsSettingsNotApplied records that changes recorded by
// auditArtifactsSettingsChanges were not written (the section write failed
// or hit a revision conflict), as auditPublicURLNotApplied does. A failed
// record is logged and does not change the response.
func (s *Server) auditArtifactsSettingsNotApplied(ctx context.Context, surface string, changes []artifactsKeyChange, cause error) {
	if len(changes) == 0 {
		return
	}
	slog.Error("Artifacts settings change was recorded but not written", "error", cause)
	if s.store == nil {
		slog.Warn("failed to emit mutation audit record",
			"mutation_type", hubSettingNotAppliedMutation, "error", "no store")
		return
	}
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), auditWriteTimeout)
	defer cancel()
	for _, c := range changes {
		rec := &store.MutationAuditRecord{
			MutationType:  hubSettingNotAppliedMutation,
			TargetType:    hubSettingAuditTarget,
			TargetID:      artifactsSection + "." + c.key,
			BeforeSummary: c.before,
			AfterSummary:  c.after,
			ExecutorKind:  hubSettingSurfaceKind,
			ExecutorID:    surface,
		}
		auditActorFromContext(ctx).ApplyActor(rec)
		rec.Timestamp = time.Now().UTC()
		if err := s.store.CreateMutationAudit(writeCtx, rec); err != nil {
			slog.Warn("failed to emit mutation audit record",
				"mutation_type", rec.MutationType, "error", err)
		}
	}
}
