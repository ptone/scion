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
	"fmt"
	"log/slog"
	"net/http"
	"sort"

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
// before the typed decode. done is true when the response has been
// written.
func rejectMalformedPlacement(w http.ResponseWriter, rawBody []byte) (done bool) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(rawBody, &top); err != nil {
		return false // the typed decode reports a malformed body
	}
	errs := map[string][]config.ValidationError{}
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
	pair.reads = map[string]int64{}
	snap := ops.Snapshot()
	var runtimes opsettings.RuntimesSettings
	var profiles opsettings.ProfilesSettings
	load := func(section string, sent json.RawMessage, inEffect, out interface{}) error {
		if sent != nil {
			return json.Unmarshal(sent, out)
		}
		row, err := ops.store.GetHubSetting(ctx, section)
		switch {
		case errors.Is(err, store.ErrNotFound):
			pair.reads[section] = 0
			b, err := json.Marshal(inEffect)
			if err != nil {
				return err
			}
			return json.Unmarshal(b, out)
		case err != nil:
			return err
		}
		pair.reads[section] = row.Revision
		return json.Unmarshal(row.Value, out)
	}
	if err := load("runtimes", sectionDocs["runtimes"], snap.Runtimes, &runtimes); err != nil {
		slog.Error("PUT server-config: failed to read runtimes for placement validation", "error", err)
		writeError(w, http.StatusInternalServerError, ErrCodeInternalError, "Failed to read existing settings", nil)
		return pair, false
	}
	if err := load("profiles", sectionDocs["profiles"], snap.Profiles, &profiles); err != nil {
		slog.Error("PUT server-config: failed to read profiles for placement validation", "error", err)
		writeError(w, http.StatusInternalServerError, ErrCodeInternalError, "Failed to read existing settings", nil)
		return pair, false
	}
	findings := config.ValidatePlacementSettings(runtimes, profiles)
	if len(findings) == 0 {
		return pair, true
	}
	errs := make([]config.ValidationError, len(findings))
	for i, f := range findings {
		errs[i] = config.ValidationError{Path: f.Path, Message: f.Message}
	}
	writeError(w, http.StatusUnprocessableEntity, ErrCodeValidationError,
		fmt.Sprintf("Kubernetes placement in runtimes or profiles is not valid (%d findings); fix the named settings, including those of a section this save does not send", len(errs)),
		map[string]interface{}{"errors": errs})
	return pair, false
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
