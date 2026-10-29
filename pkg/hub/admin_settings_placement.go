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
	"strings"

	"github.com/GoogleCloudPlatform/scion/pkg/config"
	"github.com/GoogleCloudPlatform/scion/pkg/config/opsettings"
	"github.com/GoogleCloudPlatform/scion/pkg/store"
)

// Kubernetes placement on server-config saves of the runtimes and profiles
// sections: raw placement keys are checked before the typed decode can
// drop them, the proposed pair (the submitted sections plus the stored
// value of an omitted one) is validated, and both sections are written in
// one store transaction that also compares the revision of the omitted
// section read for the validation.

// placementSections are the sections a placement save validates and
// writes together.
var placementSections = []string{"profiles", "runtimes"}

// rejectMalformedPlacement writes a 400 validation_failed naming every
// runtime entry or profile in rawBody whose placement keys are malformed or
// misspelt (config.ValidateRawPlacementEntry). It runs on the raw body,
// before the typed decode. The typed decode matches member names without
// regard to case and merges a repeated member, so a top-level runtimes or
// profiles member spelt in another case, or sent twice, is refused too:
// the raw check would not see it. done is true when the response has been
// written.
func rejectMalformedPlacement(w http.ResponseWriter, rawBody []byte) (done bool) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(rawBody, &top); err != nil {
		return false // the typed decode reports a malformed body
	}
	errs := map[string][]config.ValidationError{}
	for section := range misspeltPlacementMembers(rawBody) {
		errs[section] = append(errs[section], config.ValidationError{Path: section,
			Message: section + ": send the section once, spelt in lower case"})
	}
	for _, section := range placementSections {
		raw, ok := top[section]
		if !ok {
			continue
		}
		var entries map[string]interface{}
		if err := json.Unmarshal(raw, &entries); err != nil {
			continue // not a map: the typed decode reports it
		}
		names := make([]string, 0, len(entries))
		for name := range entries {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			entry, ok := entries[name].(map[string]interface{})
			if !ok {
				continue
			}
			if err := config.ValidateRawPlacementEntry(section+"."+name, entry); err != nil {
				errs[section] = append(errs[section], config.ValidationError{Path: section + "." + name, Message: err.Error()})
			}
		}
	}
	if len(errs) == 0 {
		return false
	}
	writeJSON(w, http.StatusBadRequest, map[string]interface{}{
		"error":  "validation_failed",
		"errors": errs,
	})
	return true
}

// misspeltPlacementMembers returns the sections among runtimes and
// profiles that the JSON object body sends more than once or under a name
// that matches only without regard to case.
func misspeltPlacementMembers(body []byte) map[string]bool {
	bad := map[string]bool{}
	seen := map[string]bool{}
	dec := json.NewDecoder(bytes.NewReader(body))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return bad
	}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return bad
		}
		name, _ := tok.(string)
		for _, section := range placementSections {
			if strings.EqualFold(name, section) {
				if name != section || seen[section] {
					bad[section] = true
				}
				seen[section] = true
			}
		}
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			return bad
		}
	}
	return bad
}

// placementPair is the plan of a placement save: whether the save touches
// runtimes or profiles, and the stored revision of an omitted section the
// validation read (0: no stored row), which the write compares.
type placementPair struct {
	active bool
	reads  map[string]int64
}

// placementPairForSave validates the proposed runtimes and profiles pair
// of a save that sends either section: the sent documents, and for a
// section the body leaves out, its stored row (or, without one, the value
// in effect). A finding answers 422 validation_error with every path and
// fixed reason, sorted, and nothing is written. ok is false when the
// response has been written.
func (s *Server) placementPairForSave(ctx context.Context, w http.ResponseWriter, ops *OperationalSettings, sectionDocs map[string]json.RawMessage) (placementPair, bool) {
	var pair placementPair
	for _, section := range placementSections {
		if _, sent := sectionDocs[section]; sent {
			pair.active = true
		}
	}
	if !pair.active {
		return pair, true
	}
	findings, reads, ok := readPlacementPair(ctx, w, ops, sectionDocs)
	if !ok {
		return pair, false
	}
	pair.reads = reads
	if len(findings) == 0 {
		return pair, true
	}
	writePlacementFindings(w, findings,
		fmt.Sprintf("Kubernetes placement in runtimes or profiles is not valid (%d findings); fix the named settings, including those of a section this save does not send", len(findings)), "")
	return pair, false
}

// readPlacementPair validates the runtimes and profiles pair made of the
// documents in docs and, for a section docs leaves out, its stored row
// (or, without one, the value in effect). reads holds the stored revision
// of each section read (0: no row). ok is false when an error response has
// been written.
func readPlacementPair(ctx context.Context, w http.ResponseWriter, ops *OperationalSettings, docs map[string]json.RawMessage) (findings []config.PlacementSettingsError, reads map[string]int64, ok bool) {
	reads = map[string]int64{}
	snap := ops.Snapshot()
	var runtimes opsettings.RuntimesSettings
	var profiles opsettings.ProfilesSettings
	load := func(section string, inEffect, out interface{}) error {
		if doc, given := docs[section]; given {
			return json.Unmarshal(doc, out)
		}
		row, err := ops.store.GetHubSetting(ctx, section)
		switch {
		case errors.Is(err, store.ErrNotFound):
			reads[section] = 0
			b, err := json.Marshal(inEffect)
			if err != nil {
				return err
			}
			return json.Unmarshal(b, out)
		case err != nil:
			return err
		}
		reads[section] = row.Revision
		return json.Unmarshal(row.Value, out)
	}
	if err := load("runtimes", snap.Runtimes, &runtimes); err != nil {
		slog.Error("Failed to read runtimes for placement validation", "error", err)
		writeError(w, http.StatusInternalServerError, ErrCodeInternalError, "Failed to read existing settings", nil)
		return nil, nil, false
	}
	if err := load("profiles", snap.Profiles, &profiles); err != nil {
		slog.Error("Failed to read profiles for placement validation", "error", err)
		writeError(w, http.StatusInternalServerError, ErrCodeInternalError, "Failed to read existing settings", nil)
		return nil, nil, false
	}
	return config.ValidatePlacementSettings(runtimes, profiles), reads, true
}

// writePlacementFindings answers 422 validation_error with every finding's
// path and fixed reason, and the reset section when not empty.
func writePlacementFindings(w http.ResponseWriter, findings []config.PlacementSettingsError, message, section string) {
	errs := make([]config.ValidationError, len(findings))
	for i, f := range findings {
		errs[i] = config.ValidationError{Path: f.Path, Message: f.Message}
	}
	details := map[string]interface{}{"errors": errs}
	if section != "" {
		details["section"] = section
	}
	writeError(w, http.StatusUnprocessableEntity, ErrCodeValidationError, message, details)
}

// placementReadConflicts lists, for a 409, the sections a guarded save
// read but did not write whose stored revision is no longer the one read
// (expected_revision is the revision read; read is true).
func placementReadConflicts(ctx context.Context, ops *OperationalSettings, reads map[string]int64, written map[string]bool) []map[string]interface{} {
	var out []map[string]interface{}
	for _, section := range placementSections {
		expected, ok := reads[section]
		if !ok || written[section] {
			continue
		}
		var current int64
		row, err := ops.store.GetHubSetting(ctx, section)
		switch {
		case err == nil:
			current = row.Revision
		case !errors.Is(err, store.ErrNotFound):
			continue // unknown: the written sections are still listed
		}
		if current != expected {
			out = append(out, map[string]interface{}{
				"section":           section,
				"expected_revision": expected,
				"current_revision":  current,
				"read":              true,
			})
		}
	}
	return out
}

// resetPlacementSection resets runtimes or profiles to the bootstrap value
// (DELETE /api/v1/admin/server-config/sections/{name}). The pair the reset
// leaves (the bootstrap value plus the other section's stored row) is
// validated first, answering 422 naming the section on a finding; the row
// is then deleted in a guarded transaction that also compares the other
// section's revision, answering 409 on a change. ok is false when an error
// response has been written.
func (s *Server) resetPlacementSection(ctx context.Context, w http.ResponseWriter, ops *OperationalSettings, section string) bool {
	row, err := ops.store.GetHubSetting(ctx, section)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, ErrCodeNotFound, "Section not found in database: "+section, nil)
		return false
	}
	if err != nil {
		slog.Error("Failed to read section for reset", "section", section, "error", err)
		writeError(w, http.StatusInternalServerError, ErrCodeInternalError, "Failed to reset section", nil)
		return false
	}
	boot, _ := config.OverlayMapSections(nil, ops.bootstrapKoanf)
	var bootValue interface{} = boot.Runtimes
	if section == "profiles" {
		bootValue = boot.Profiles
	}
	bootDoc, err := json.Marshal(bootValue)
	if err != nil {
		slog.Error("Failed to encode bootstrap value for reset", "section", section, "error", err)
		writeError(w, http.StatusInternalServerError, ErrCodeInternalError, "Failed to reset section", nil)
		return false
	}
	findings, reads, ok := readPlacementPair(ctx, w, ops, map[string]json.RawMessage{section: bootDoc})
	if !ok {
		return false
	}
	if len(findings) > 0 {
		writePlacementFindings(w, findings,
			fmt.Sprintf("Resetting %s would leave Kubernetes placement in runtimes or profiles not valid (%d findings); fix the named settings first", section, len(findings)), section)
		return false
	}
	err = ops.deleteGuarded(ctx, section, row.Revision, reads)
	switch {
	case errors.Is(err, store.ErrRevisionConflict):
		conflicted := []map[string]interface{}{{
			"section":           section,
			"expected_revision": row.Revision,
			"current_revision":  s.getCurrentRevision(ops, section),
		}}
		conflicted = append(conflicted, placementReadConflicts(ctx, ops, reads, map[string]bool{section: true})...)
		writeJSON(w, http.StatusConflict, map[string]interface{}{
			"error":      "revision_conflict",
			"message":    "The section, or the section validated with it, changed during the reset.",
			"conflicted": conflicted,
		})
		return false
	case err != nil:
		slog.Error("Failed to reset section", "section", section, "error", err)
		writeError(w, http.StatusInternalServerError, ErrCodeInternalError, "Failed to reset section", nil)
		return false
	}
	return true
}

// errGuardedBatchUnsupported reports a settings store without
// store.HubSettingGuardedBatchWriter: a runtimes or profiles save then
// fails closed rather than writing the sections one at a time.
var errGuardedBatchUnsupported = errors.New("the settings store cannot save runtimes and profiles in one transaction")

// guardedSectionWrite is one section of OperationalSettings.updateGuarded.
type guardedSectionWrite struct {
	section          string
	doc              json.RawMessage
	expectedRevision int64
}

// updateGuarded is update for several sections in one store transaction
// guarded by the revisions of the sections in reads
// (store.HubSettingGuardedBatchWriter): every section is validated, then
// written together or not at all, cached, published and self-applied once.
// It returns each written section's new revision and the sections the
// self-apply did not include (see update).
func (o *OperationalSettings) updateGuarded(ctx context.Context, writes []guardedSectionWrite, reads map[string]int64, updatedBy, origin string) (map[string]int64, map[string]bool, error) {
	bw, ok := o.store.(store.HubSettingGuardedBatchWriter)
	if !ok {
		return nil, nil, errGuardedBatchUnsupported
	}
	batch := make([]store.HubSettingWrite, 0, len(writes))
	for _, wr := range writes {
		if err := o.checkWriteGuard(ctx, wr.section); err != nil {
			return nil, nil, err
		}
		if errs := opsettings.Validate(wr.section, wr.doc); len(errs) > 0 {
			return nil, nil, fmt.Errorf("%w for section %q: %v", ErrSectionValidation, wr.section, errs)
		}
		batch = append(batch, store.HubSettingWrite{Section: wr.section, Value: wr.doc, ExpectedRevision: wr.expectedRevision})
	}
	rows, err := bw.UpsertHubSettingsGuarded(ctx, batch, reads, updatedBy, origin)
	if err != nil {
		return nil, nil, err
	}
	revs := make(map[string]int64, len(rows))
	notInEffect := map[string]bool{}
	for i := range rows {
		row := &rows[i]
		revs[row.Section] = row.Revision
		if o.cacheWrite(row.Section, row) && !o.refreshThrough(ctx, row.SettingsRevision, row.Section) {
			notInEffect[row.Section] = true
		}
	}
	for i := range rows {
		o.publish(rows[i].Section, rows[i].Revision)
	}
	o.selfApply()
	return revs, notInEffect, nil
}
