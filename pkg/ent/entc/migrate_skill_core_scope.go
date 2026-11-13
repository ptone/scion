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

package entc

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/ent"
	enthubsetting "github.com/GoogleCloudPlatform/scion/pkg/ent/hubsetting"
	entskill "github.com/GoogleCloudPlatform/scion/pkg/ent/skill"
	entskillinjection "github.com/GoogleCloudPlatform/scion/pkg/ent/skillinjection"
	"github.com/google/uuid"
)

const (
	retiredSkillScopeCore          = "core"
	migratedSkillScopeGlobal       = "global"
	maxMigratedSkillSlugLen        = 64
	injectedSkillsHubSettingKey    = "injected_skills"
	retiredCoreSkillAliasPrefix    = "skill://core/"
	retiredCoreSkillScionPrefix    = "skill://scion/core/"
	retiredCoreSkillDefaultPrefix  = "skill:///core/"
	replacementGlobalSkillURIPrefx = "skill://global/"
)

type coreCollisionWarning struct {
	skillID uuid.UUID
	oldSlug string
	newSlug string
}

// MigrateSkillCoreScope performs an idempotent data migration retiring the
// legacy "core" skill scope in a single transaction on both SQLite and
// PostgreSQL:
//  1. Each scope="core" skill with no same-slug scope="global" row is rewritten
//     to scope="global", leaving storage_path and storage_uri as stored.
//  2. On slug collision with an existing scope="global" row (or an earlier
//     migrated row in the same batch), the core skill's slug is renamed to
//     <slug>-core (then <slug>-core-2, <slug>-core-3, ...) within the 64-char
//     slug limit, its scope is set to "global", and a warning is logged with
//     the old and new identity.
//  3. Stored skill://core/<slug> (as well as skill://scion/core/<slug> and
//     skill:///core/<slug>) references in hub_settings["injected_skills"].user_defined
//     and in project/user SkillInjection rows are rewritten to
//     skill://global/<new slug> (preserving any @version suffix).
//  4. Template files are not rewritten.
func MigrateSkillCoreScope(ctx context.Context, client *ent.Client) error {
	tx, err := client.Tx(ctx)
	if err != nil {
		return fmt.Errorf("begin core skill scope migration: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	mutated := false
	var warnings []coreCollisionWarning

	coreSkills, err := tx.Skill.Query().
		Where(entskill.ScopeEQ(retiredSkillScopeCore)).
		Order(ent.Asc(entskill.FieldCreated), ent.Asc(entskill.FieldID)).
		All(ctx)
	if err != nil {
		return fmt.Errorf("querying core skills for migration: %w", err)
	}

	slugRemap := make(map[string]string, len(coreSkills))
	if len(coreSkills) > 0 {
		existingGlobalSlugs, err := tx.Skill.Query().
			Where(
				entskill.ScopeEQ(migratedSkillScopeGlobal),
				entskill.Or(
					entskill.ScopeIDEQ(""),
					entskill.ScopeIDIsNil(),
				),
			).
			Select(entskill.FieldSlug).
			Strings(ctx)
		if err != nil {
			return fmt.Errorf("querying existing global skill slugs: %w", err)
		}

		globalSlugs := make(map[string]bool, len(existingGlobalSlugs)+len(coreSkills))
		for _, slug := range existingGlobalSlugs {
			globalSlugs[slug] = true
		}

		for _, sk := range coreSkills {
			oldSlug := sk.Slug
			newSlug := oldSlug
			collided := globalSlugs[oldSlug]
			if collided {
				newSlug = nextCoreCollisionSlug(oldSlug, globalSlugs)
			}
			globalSlugs[newSlug] = true
			if _, recorded := slugRemap[oldSlug]; !recorded {
				slugRemap[oldSlug] = newSlug
			}

			upd := tx.Skill.UpdateOneID(sk.ID).
				SetScope(migratedSkillScopeGlobal).
				SetScopeID("")
			if newSlug != oldSlug {
				upd = upd.SetSlug(newSlug)
			}
			if err := upd.Exec(ctx); err != nil {
				return fmt.Errorf("migrating core skill %s (%q -> %q): %w", sk.ID, oldSlug, newSlug, err)
			}
			if collided {
				warnings = append(warnings, coreCollisionWarning{
					skillID: sk.ID,
					oldSlug: oldSlug,
					newSlug: newSlug,
				})
			}
			mutated = true
		}
	}

	// Rewrite project and user SkillInjection rows that reference core skills.
	candidateInjections, err := tx.SkillInjection.Query().
		Where(entskillinjection.SkillURIContains("/core/")).
		Order(ent.Asc(entskillinjection.FieldSortOrder), ent.Asc(entskillinjection.FieldCreatedAt), ent.Asc(entskillinjection.FieldID)).
		All(ctx)
	if err != nil {
		return fmt.Errorf("querying core skill injections for migration: %w", err)
	}

	type pendingInjectionRewrite struct {
		row    *ent.SkillInjection
		newURI string
	}
	var pendingInjections []pendingInjectionRewrite
	scopeIDSet := make(map[string]bool)
	for _, si := range candidateInjections {
		if newURI, ok := rewriteCoreSkillURI(si.SkillURI, slugRemap); ok && newURI != si.SkillURI {
			pendingInjections = append(pendingInjections, pendingInjectionRewrite{row: si, newURI: newURI})
			scopeIDSet[si.ScopeID] = true
		}
	}

	if len(pendingInjections) > 0 {
		scopeIDs := make([]string, 0, len(scopeIDSet))
		for id := range scopeIDSet {
			scopeIDs = append(scopeIDs, id)
		}
		existingInScopes, err := tx.SkillInjection.Query().
			Where(entskillinjection.ScopeIDIn(scopeIDs...)).
			All(ctx)
		if err != nil {
			return fmt.Errorf("querying existing skill injections in affected scopes: %w", err)
		}

		type injectionKey struct {
			scope   entskillinjection.Scope
			scopeID string
			uri     string
		}
		existingKeys := make(map[injectionKey]bool, len(existingInScopes))
		for _, si := range existingInScopes {
			existingKeys[injectionKey{scope: si.Scope, scopeID: si.ScopeID, uri: si.SkillURI}] = true
		}

		for _, p := range pendingInjections {
			oldKey := injectionKey{scope: p.row.Scope, scopeID: p.row.ScopeID, uri: p.row.SkillURI}
			delete(existingKeys, oldKey)

			targetKey := injectionKey{scope: p.row.Scope, scopeID: p.row.ScopeID, uri: p.newURI}
			if existingKeys[targetKey] {
				// Target URI already exists in this (scope, scope_id); drop the redundant core row.
				if err := tx.SkillInjection.DeleteOneID(p.row.ID).Exec(ctx); err != nil {
					return fmt.Errorf("deduplicating migrated skill injection %s: %w", p.row.ID, err)
				}
			} else {
				if err := tx.SkillInjection.UpdateOneID(p.row.ID).SetSkillURI(p.newURI).Exec(ctx); err != nil {
					return fmt.Errorf("rewriting skill injection %s (%q -> %q): %w", p.row.ID, p.row.SkillURI, p.newURI, err)
				}
				existingKeys[targetKey] = true
			}
			mutated = true
		}
	}

	// Rewrite hub injected_skills.user_defined references in hub_settings.
	hs, err := tx.HubSetting.Query().
		Where(enthubsetting.SectionEQ(injectedSkillsHubSettingKey)).
		Only(ctx)
	if err != nil && !ent.IsNotFound(err) {
		return fmt.Errorf("querying hub injected_skills setting: %w", err)
	}
	if err == nil && len(hs.Value) > 0 {
		var setting api.HubSkillInjectionSetting
		if err := json.Unmarshal(hs.Value, &setting); err != nil {
			return fmt.Errorf("parsing hub injected_skills setting: %w", err)
		}
		settingChanged := false
		for i := range setting.UserDefined {
			if newURI, ok := rewriteCoreSkillURI(setting.UserDefined[i].URI, slugRemap); ok && newURI != setting.UserDefined[i].URI {
				setting.UserDefined[i].URI = newURI
				settingChanged = true
			}
		}
		if settingChanged {
			raw, err := json.Marshal(setting)
			if err != nil {
				return fmt.Errorf("serializing migrated hub injected_skills setting: %w", err)
			}
			if err := tx.HubSetting.UpdateOneID(hs.ID).SetValue(raw).Exec(ctx); err != nil {
				return fmt.Errorf("updating migrated hub injected_skills setting: %w", err)
			}
			mutated = true
		}
	}

	if !mutated {
		return nil
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing core skill scope migration: %w", err)
	}
	committed = true

	for _, w := range warnings {
		slog.WarnContext(ctx, "renamed colliding core skill during migration to global scope",
			"skill_id", w.skillID.String(),
			"old_scope", retiredSkillScopeCore,
			"old_slug", w.oldSlug,
			"old_uri", retiredCoreSkillAliasPrefix+w.oldSlug,
			"new_scope", migratedSkillScopeGlobal,
			"new_slug", w.newSlug,
			"new_uri", replacementGlobalSkillURIPrefx+w.newSlug,
		)
	}

	return nil
}

// nextCoreCollisionSlug computes the first unoccupied collision-renamed slug
// (<slug>-core, <slug>-core-2, <slug>-core-3, ...) within the 64-character
// kebab-case skill name limit.
func nextCoreCollisionSlug(baseSlug string, occupied map[string]bool) string {
	for n := 1; ; n++ {
		suffix := "-core"
		if n >= 2 {
			suffix = fmt.Sprintf("-core-%d", n)
		}
		maxBase := maxMigratedSkillSlugLen - len(suffix)
		prefix := baseSlug
		if len(prefix) > maxBase {
			prefix = strings.TrimRight(prefix[:maxBase], "-")
		}
		if prefix == "" {
			prefix = "skill"
			if len(prefix) > maxBase && maxBase > 0 {
				prefix = prefix[:maxBase]
			}
		}
		candidate := prefix + suffix
		if !occupied[candidate] {
			return candidate
		}
	}
}

// rewriteCoreSkillURI rewrites a Hub-registry core-scope skill URI
// (skill://core/<slug>, skill://scion/core/<slug>, or skill:///core/<slug>,
// with an optional @<version> suffix) to skill://global/<newSlug>[@<version>].
func rewriteCoreSkillURI(raw string, slugRemap map[string]string) (string, bool) {
	trimmed := strings.TrimSpace(raw)
	var rest string
	switch {
	case strings.HasPrefix(trimmed, retiredCoreSkillAliasPrefix):
		rest = trimmed[len(retiredCoreSkillAliasPrefix):]
	case strings.HasPrefix(trimmed, retiredCoreSkillScionPrefix):
		rest = trimmed[len(retiredCoreSkillScionPrefix):]
	case strings.HasPrefix(trimmed, retiredCoreSkillDefaultPrefix):
		rest = trimmed[len(retiredCoreSkillDefaultPrefix):]
	default:
		return raw, false
	}

	if rest == "" || strings.ContainsAny(rest, "/?#") {
		return raw, false
	}

	oldSlug := rest
	verSuffix := ""
	if atIdx := strings.IndexByte(rest, '@'); atIdx >= 0 {
		oldSlug = rest[:atIdx]
		verSuffix = rest[atIdx:]
		if oldSlug == "" || verSuffix == "@" {
			return raw, false
		}
	}

	newSlug := oldSlug
	if mapped, ok := slugRemap[oldSlug]; ok {
		newSlug = mapped
	}
	return replacementGlobalSkillURIPrefx + newSlug + verSuffix, true
}
