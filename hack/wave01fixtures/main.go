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

// Command wave01fixtures is a steward-only, offline fixture helper (Wave01
// "G1"). It inserts stopped/error/created agent rows and offline (B1) runtime
// broker rows into an ISOLATED, STOPPED, CHECKPOINTED clone of a hub SQLite
// database, using only existing store interfaces. It is tool provenance, not
// product behaviour: it never starts a server, dispatches an agent, issues a
// credential or fabricates a heartbeat.
//
// See README.md in this directory for the runbook and recipe schema.
package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"slices"
	"strings"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/util"
)

// ToolName identifies this helper in manifests.
const ToolName = "hack/wave01fixtures"

// markerSuffix names the run marker created beside the DB before any write.
// It is never removed by the tool, so any second run against the same clone
// (including after a partial failure) is refused.
const markerSuffix = ".wave01-fixtures.run"

// Divergences are the H6 disclosures every manifest carries verbatim.
var Divergences = []string{
	"No DelegationEdge and no mutation audit row is written for fixture agents (an edge needs a credential-derived EffectCeiling and cannot be honest offline); fixture agents must never be started.",
	"No quota reservation: broker agentCount and project agent limits under-count fixture agents (project card agent counts are computed live and stay correct).",
	"No events are published for fixture rows.",
	"Created/Updated are the helper's insertion time: ages are honest only relative to the helper run, and updated-time order between helper rows is a TIE; scenario oracles must not require any order among tied rows.",
	"Fixture agents have no runtime broker (RuntimeBrokerID empty); fixture brokers are offline/disconnected with no heartbeat, registration, join token or secret.",
	"Agents with exit fields carry StateVersion 2 (one UpdateAgent after create); others carry StateVersion 1.",
}

// MigrateOrder discloses, in every manifest, that the store's Migrate runs
// before preflight and before the run marker (review1 L1).
const MigrateOrder = "entadapter Migrate runs when the store is opened, before preflight and before the run marker. " +
	"On a clone of a slot already booted at the same backend commit it is expected to be a byte no-op. " +
	"Preflight never writes. After-digests are recorded for every outcome after open; a refusal whose " +
	"digests differ from before is reported as refused-clone-modified-discard."

// cloneChanged reports whether the DB file changed or a non-empty WAL was
// left behind between two digests.
func cloneChanged(before, after *DBState) bool {
	if before == nil || after == nil {
		return true
	}
	return before.DB.SHA256 != after.DB.SHA256 || (after.WAL.Exists && after.WAL.Size > 0)
}

// FileDigest records one database file's state.
type FileDigest struct {
	Path   string `json:"path"`
	Exists bool   `json:"exists"`
	Size   int64  `json:"size,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
}

// DBState records the DB and its WAL/SHM side files.
type DBState struct {
	DB  FileDigest `json:"db"`
	WAL FileDigest `json:"wal"`
	SHM FileDigest `json:"shm"`
}

// Manifest is the non-secret run record written for every run that got past
// argument parsing (including refusals and partial failures).
type Manifest struct {
	Tool            string           `json:"tool"`
	Outcome         string           `json:"outcome"` // ok | refused | partial-discard-clone | failed
	Error           string           `json:"error,omitempty"`
	StartedAt       time.Time        `json:"startedAt"`
	FinishedAt      time.Time        `json:"finishedAt"`
	Attestation     string           `json:"attestation"`
	AttestationKind string           `json:"attestationKind"`
	Helper          HelperProvenance `json:"helper"`
	RecipePath      string           `json:"recipePath"`
	RecipeSHA256    string           `json:"recipeSha256"`
	RecipeSchema    string           `json:"recipeSchema,omitempty"`
	StateMix        map[string]int   `json:"stateMix,omitempty"`
	Before          *DBState         `json:"before,omitempty"`
	After           *DBState         `json:"after,omitempty"`
	Checkpoint      string           `json:"checkpoint,omitempty"`
	Written         *WriteResult     `json:"written,omitempty"`
	Verified        bool             `json:"verified"`
	CloneChanged    bool             `json:"cloneChanged"`
	MigrateOrder    string           `json:"migrateOrder"`
	Divergences     []string         `json:"divergences"`
}

// HelperProvenance is what the running binary can say about itself. Source
// commit and pkg/ent/schema tree hash are recorded by the steward from git
// (see README); the binary's own digest is computed here.
type HelperProvenance struct {
	GoVersion    string            `json:"goVersion"`
	MainPath     string            `json:"mainPath,omitempty"`
	ModulePath   string            `json:"modulePath,omitempty"`
	BuildSetting map[string]string `json:"buildSettings,omitempty"`
	Executable   string            `json:"executable,omitempty"`
	BinarySHA256 string            `json:"binarySha256,omitempty"`
}

func helperProvenance() HelperProvenance {
	h := HelperProvenance{GoVersion: runtime.Version()}
	if bi, ok := debug.ReadBuildInfo(); ok {
		h.MainPath = bi.Path
		h.ModulePath = bi.Main.Path
		h.BuildSetting = map[string]string{}
		for _, s := range bi.Settings {
			if strings.HasPrefix(s.Key, "vcs.") || s.Key == "CGO_ENABLED" || s.Key == "GOOS" || s.Key == "GOARCH" || s.Key == "-trimpath" {
				h.BuildSetting[s.Key] = s.Value
			}
		}
	}
	if exe, err := os.Executable(); err == nil {
		h.Executable = exe
		if d, err := digestFile(exe); err == nil {
			h.BinarySHA256 = d.SHA256
		}
	}
	return h
}

func digestFile(path string) (FileDigest, error) {
	d := FileDigest{Path: path}
	fh, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return d, nil
	}
	if err != nil {
		return d, err
	}
	defer func() { _ = fh.Close() }()
	h := sha256.New()
	n, err := io.Copy(h, fh)
	if err != nil {
		return d, err
	}
	d.Exists, d.Size, d.SHA256 = true, n, hex.EncodeToString(h.Sum(nil))
	return d, nil
}

func dbState(dbPath string) (*DBState, error) {
	var s DBState
	var err error
	if s.DB, err = digestFile(dbPath); err != nil {
		return nil, err
	}
	if s.WAL, err = digestFile(dbPath + "-wal"); err != nil {
		return nil, err
	}
	if s.SHM, err = digestFile(dbPath + "-shm"); err != nil {
		return nil, err
	}
	return &s, nil
}

// checkTarget enforces the file-level preconditions for a stopped,
// checkpointed clone. It cannot prove no process has the file open; that is
// the steward's process/checkpoint evidence (H2).
func checkTarget(dbPath string) error {
	if err := safeDBPath(dbPath); err != nil {
		return err
	}
	fi, err := os.Lstat(dbPath)
	if err != nil {
		return fmt.Errorf("--db %s: %w (the helper never creates a database)", dbPath, err)
	}
	if fi.Mode()&fs.ModeSymlink != 0 || !fi.Mode().IsRegular() {
		return fmt.Errorf("--db %s must be a regular file, not a symlink or special file", dbPath)
	}
	if fi.Size() == 0 {
		return fmt.Errorf("--db %s is empty; expected a cloned hub database", dbPath)
	}
	if wal, err := os.Lstat(dbPath + "-wal"); err == nil && wal.Size() > 0 {
		return fmt.Errorf("%s-wal is non-empty (%d bytes): the clone is not checkpointed; "+
			"checkpoint the stopped slot DB (PRAGMA wal_checkpoint(TRUNCATE)) and re-clone", dbPath, wal.Size())
	}
	if _, err := os.Lstat(dbPath + markerSuffix); err == nil {
		return fmt.Errorf("run marker %s%s exists: this clone was already used by a helper run; discard it and re-clone", dbPath, markerSuffix)
	}
	return nil
}

// safeDBPath requires dbPath to be a path SQLite will open verbatim and that
// names exactly the file the target checks inspect. The DSN is built as
// "file:"+path, and both entc (which cuts the DSN at the first '?') and SQLite
// URI parsing treat '?', '#' and '%' specially, so a path containing them
// would validate one file and open another (review1 M1). The path must also
// be clean and free of symlinks in any component, so the digests, marker and
// manifest all refer to the file actually written.
func safeDBPath(dbPath string) error {
	if !filepath.IsAbs(dbPath) {
		return fmt.Errorf("--db must be an absolute path")
	}
	if strings.ContainsAny(dbPath, "?#%\x00") {
		return fmt.Errorf("--db %q contains '?', '#', '%%' or NUL, which SQLite URI parsing would reinterpret; rename the clone", dbPath)
	}
	if filepath.Clean(dbPath) != dbPath {
		return fmt.Errorf("--db %q is not a clean path (use %q)", dbPath, filepath.Clean(dbPath))
	}
	resolved, err := filepath.EvalSymlinks(dbPath)
	if err != nil {
		return fmt.Errorf("--db %s: %w (the helper never creates a database)", dbPath, err)
	}
	if resolved != dbPath {
		return fmt.Errorf("--db %s resolves through a symlink to %s; pass the real path of the clone", dbPath, resolved)
	}
	return nil
}

// createMarker atomically claims the clone for this run (O_EXCL).
func createMarker(dbPath string, m *Manifest) error {
	fh, err := os.OpenFile(dbPath+markerSuffix, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("claim run marker: %w", err)
	}
	defer func() { _ = fh.Close() }()
	_, err = fmt.Fprintf(fh, "%s run started %s recipe sha256 %s attestation %q\n",
		ToolName, m.StartedAt.Format(time.RFC3339Nano), m.RecipeSHA256, m.Attestation)
	return err
}

// checkpoint folds the WAL into the main DB file after the store is closed,
// so the after-digest describes the complete database.
func checkpoint(dbPath string) (string, error) {
	if err := safeDBPath(dbPath); err != nil {
		return "", err
	}
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		return "", err
	}
	defer func() { _ = db.Close() }()
	var busy, logFrames, ckpt int
	if err := db.QueryRow("PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &logFrames, &ckpt); err != nil {
		return "", err
	}
	res := fmt.Sprintf("wal_checkpoint(TRUNCATE) busy=%d log=%d checkpointed=%d", busy, logFrames, ckpt)
	if busy != 0 {
		return res, fmt.Errorf("checkpoint reported busy: another connection holds the database")
	}
	return res, nil
}

// Options are the parsed command-line inputs.
type Options struct {
	DBPath       string
	RecipePath   string
	ManifestPath string
	Attestation  string
	ValidateOnly bool
}

// Run executes one helper run and always returns a manifest describing it.
func Run(ctx context.Context, o Options) (*Manifest, error) {
	m := &Manifest{
		Tool:            ToolName,
		StartedAt:       time.Now().UTC(),
		Attestation:     o.Attestation,
		AttestationKind: "operator statement (not proof): the steward attests the target is a slot-owned, stopped, checkpointed, isolated clone",
		Helper:          helperProvenance(),
		RecipePath:      o.RecipePath,
		Divergences:     Divergences,
		MigrateOrder:    MigrateOrder,
		Outcome:         "refused",
	}
	fail := func(outcome string, err error) (*Manifest, error) {
		m.Outcome, m.Error, m.FinishedAt = outcome, err.Error(), time.Now().UTC()
		if m.After != nil {
			m.CloneChanged = cloneChanged(m.Before, m.After)
		}
		return m, err
	}

	data, err := os.ReadFile(o.RecipePath)
	if err != nil {
		return fail("refused", fmt.Errorf("read recipe: %w", err))
	}
	sum := sha256.Sum256(data)
	m.RecipeSHA256 = hex.EncodeToString(sum[:])
	r, err := DecodeRecipe(data)
	if err != nil {
		return fail("refused", err)
	}
	m.RecipeSchema = r.Schema
	if err := r.Validate(); err != nil {
		return fail("refused", err)
	}
	m.StateMix = r.StateMix()
	if o.ValidateOnly {
		m.Outcome, m.FinishedAt = "validated-only", time.Now().UTC()
		return m, nil
	}

	if strings.TrimSpace(o.Attestation) == "" {
		return fail("refused", errors.New("--attest-stopped-clone is required: name the slot and state that the target is its stopped, checkpointed, isolated clone"))
	}
	if err := checkTarget(o.DBPath); err != nil {
		return fail("refused", err)
	}
	if m.Before, err = dbState(o.DBPath); err != nil {
		return fail("refused", fmt.Errorf("digest before: %w", err))
	}

	// Opening runs the store's Migrate (the offline-writer pattern) BEFORE
	// preflight and before the marker. On a clone of a slot already booted
	// at the same backend commit this is expected to be a byte no-op (schema
	// current, backfills marker-gated); on any other DB it may write.
	// Preflight itself never writes. From here on every outcome records
	// After digests, and a refusal whose digests differ from Before is
	// classified as a modified clone that must be discarded.
	refusedAfterOpen := func(cause error) (*Manifest, error) {
		m.After, _ = dbState(o.DBPath)
		if cloneChanged(m.Before, m.After) {
			return fail("refused-clone-modified-discard",
				fmt.Errorf("%w; the clone changed while opening (Migrate wrote): DISCARD it", cause))
		}
		return fail("refused", cause)
	}
	fs1, err := openStore(ctx, o.DBPath)
	if err != nil {
		m.After, _ = dbState(o.DBPath)
		return fail("failed", fmt.Errorf("%w; DISCARD this clone", err))
	}
	perr := fs1.Preflight(ctx, r)
	if perr != nil {
		_ = fs1.Close()
		return refusedAfterOpen(perr)
	}
	if err := createMarker(o.DBPath, m); err != nil {
		_ = fs1.Close()
		return refusedAfterOpen(err)
	}
	res, werr := fs1.Write(ctx, r)
	m.Written = &res
	if cerr := fs1.Close(); cerr != nil && werr == nil {
		werr = fmt.Errorf("close store: %w", cerr)
	}
	if werr != nil {
		m.After, _ = dbState(o.DBPath)
		return fail("partial-discard-clone", fmt.Errorf("%w; the clone is now partial: DISCARD it, do not rerun over it", werr))
	}

	// Reopen and verify what was persisted.
	fs2, err := openStore(ctx, o.DBPath)
	if err != nil {
		m.After, _ = dbState(o.DBPath)
		return fail("failed", fmt.Errorf("reopen for verification: %w; DISCARD this clone", err))
	}
	verr := fs2.Verify(ctx, r)
	_ = fs2.Close()
	if verr != nil {
		m.After, _ = dbState(o.DBPath)
		return fail("failed", fmt.Errorf("%w; DISCARD this clone", verr))
	}
	m.Verified = true

	if m.Checkpoint, err = checkpoint(o.DBPath); err != nil {
		m.After, _ = dbState(o.DBPath)
		return fail("failed", fmt.Errorf("final checkpoint: %w", err))
	}
	if m.After, err = dbState(o.DBPath); err != nil {
		return fail("failed", fmt.Errorf("digest after: %w", err))
	}
	m.CloneChanged = cloneChanged(m.Before, m.After)
	m.Outcome, m.FinishedAt = "ok", time.Now().UTC()
	return m, nil
}

func writeManifest(path string, m *Manifest) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if path == "" || path == "-" {
		_, err = os.Stdout.Write(data)
		return err
	}
	fh, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("write manifest (must not already exist): %w", err)
	}
	defer func() { _ = fh.Close() }()
	_, err = fh.Write(data)
	return err
}

func main() {
	// Offline hub-store writer: pin like the other offline store writers.
	util.PinProcessUTC()

	var o Options
	flag.StringVar(&o.DBPath, "db", "", "absolute path of the slot-owned, stopped, checkpointed hub.db CLONE to write")
	flag.StringVar(&o.RecipePath, "recipe", "", "fixture recipe JSON (schema "+RecipeSchema+")")
	flag.StringVar(&o.ManifestPath, "manifest", "-", "manifest output path (must not exist), or - for stdout")
	flag.StringVar(&o.Attestation, "attest-stopped-clone", "", "steward attestation, e.g. 'slot=wl-a; hub stopped 14:50Z; checkpointed; isolated clone'")
	flag.BoolVar(&o.ValidateOnly, "validate-only", false, "validate the recipe only; open no database")
	flag.Parse()

	if o.RecipePath == "" || (!o.ValidateOnly && o.DBPath == "") || flag.NArg() != 0 {
		flag.Usage()
		os.Exit(2)
	}
	if o.ManifestPath != "-" && slices.Contains([]string{o.DBPath, o.RecipePath}, o.ManifestPath) {
		fmt.Fprintln(os.Stderr, "--manifest must not name the DB or the recipe")
		os.Exit(2)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	m, err := Run(ctx, o)
	if werr := writeManifest(o.ManifestPath, m); werr != nil {
		fmt.Fprintln(os.Stderr, "error:", werr)
		if err == nil {
			os.Exit(1)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "%s: outcome %s\n", ToolName, m.Outcome)
}
