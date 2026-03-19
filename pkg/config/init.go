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

package config

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/util"
	"github.com/google/uuid"
	"gopkg.in/yaml.v3"
)

//go:embed all:embeds/*
var EmbedsFS embed.FS

// getDefaultSettingsDataForRuntime generates default settings JSON with the
// specified runtime for the local profile. Handles both versioned and legacy formats.
func getDefaultSettingsDataForRuntime(targetRuntime string) ([]byte, error) {
	data, err := EmbedsFS.ReadFile("embeds/default_settings.yaml")
	if err != nil {
		return nil, err
	}

	version, _ := DetectSettingsFormat(data)
	if version != "" {
		var vs VersionedSettings
		if err := yaml.Unmarshal(data, &vs); err != nil {
			return nil, err
		}
		if local, ok := vs.Profiles["local"]; ok {
			local.Runtime = targetRuntime
			vs.Profiles["local"] = local
		}
		legacy := convertVersionedToLegacy(&vs)
		return json.MarshalIndent(legacy, "", "  ")
	}

	var settings Settings
	if err := yaml.Unmarshal(data, &settings); err != nil {
		return nil, err
	}
	if local, ok := settings.Profiles["local"]; ok {
		local.Runtime = targetRuntime
		settings.Profiles["local"] = local
	}
	return json.MarshalIndent(settings, "", "  ")
}

// GetDefaultSettingsData returns the embedded default settings in JSON format.
// This function adjusts the local profile runtime based on the OS. It is used as
// a fallback default for settings loaders; during init, DetectLocalRuntime is used
// instead for actual runtime probing.
func GetDefaultSettingsData() ([]byte, error) {
	targetRuntime := "docker"
	if runtime.GOOS == "darwin" {
		targetRuntime = "container"
	}
	return getDefaultSettingsDataForRuntime(targetRuntime)
}

// SeedCommonFiles seeds the common files for a harness template.
// It creates the base directory structure and writes only common files
// (.tmux.conf, .zshrc) that are shared across all harnesses.
func SeedCommonFiles(templateDir, configDirName string, force bool) error {
	homeDir := filepath.Join(templateDir, "home")
	// Create directories
	dirs := []string{
		templateDir,
		homeDir,
		filepath.Join(homeDir, ".config", "gcloud"),
	}
	if configDirName != "" {
		dirs = append(dirs, filepath.Join(homeDir, configDirName))
	}

	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("failed to create directory %s: %w", dir, err)
		}
	}

	// Helper to read common embedded file
	readCommonEmbed := func(name string) string {
		data, err := EmbedsFS.ReadFile(filepath.Join("embeds", "common", name))
		if err != nil {
			return ""
		}
		return string(data)
	}

	// Seed common template files
	files := []struct {
		path    string
		content string
		mode    os.FileMode
	}{
		{filepath.Join(homeDir, ".tmux.conf"), readCommonEmbed(".tmux.conf"), 0644},
		{filepath.Join(homeDir, ".zshrc"), readCommonEmbed("zshrc"), 0644},
		{filepath.Join(homeDir, ".gitconfig"), readCommonEmbed(".gitconfig"), 0644},
		{filepath.Join(homeDir, ".gemini", ".geminiignore"), readCommonEmbed(".geminiignore"), 0644},
	}

	for _, f := range files {
		if f.content == "" {
			continue
		}
		// Ensure parent directory exists (e.g., for .gemini/.geminiignore)
		if err := os.MkdirAll(filepath.Dir(f.path), 0755); err != nil {
			return fmt.Errorf("failed to create directory for %s: %w", f.path, err)
		}
		if force {
			if err := os.WriteFile(f.path, []byte(f.content), f.mode); err != nil {
				return fmt.Errorf("failed to write file %s: %w", f.path, err)
			}
			continue
		}

		if _, err := os.Stat(f.path); os.IsNotExist(err) {
			if err := os.WriteFile(f.path, []byte(f.content), f.mode); err != nil {
				return fmt.Errorf("failed to write file %s: %w", f.path, err)
			}
		}
	}

	return nil
}

// SeedCommonFilesToHome seeds common files (.tmux.conf, .zshrc) directly into
// a home directory. Unlike SeedCommonFiles which writes into templateDir/home/,
// this writes directly into the provided homeDir for use during agent provisioning.
func SeedCommonFilesToHome(homeDir string, force bool) error {
	readCommonEmbed := func(name string) string {
		data, err := EmbedsFS.ReadFile(filepath.Join("embeds", "common", name))
		if err != nil {
			return ""
		}
		return string(data)
	}

	files := []struct {
		path    string
		content string
		mode    os.FileMode
	}{
		{filepath.Join(homeDir, ".tmux.conf"), readCommonEmbed(".tmux.conf"), 0644},
		{filepath.Join(homeDir, ".zshrc"), readCommonEmbed("zshrc"), 0644},
		{filepath.Join(homeDir, ".gitconfig"), readCommonEmbed(".gitconfig"), 0644},
		{filepath.Join(homeDir, ".gemini", ".geminiignore"), readCommonEmbed(".geminiignore"), 0644},
	}

	for _, f := range files {
		if f.content == "" {
			continue
		}
		// Ensure parent directory exists (e.g., for .gemini/.geminiignore)
		if err := os.MkdirAll(filepath.Dir(f.path), 0755); err != nil {
			return fmt.Errorf("failed to create directory for %s: %w", f.path, err)
		}
		if force {
			if err := os.WriteFile(f.path, []byte(f.content), f.mode); err != nil {
				return fmt.Errorf("failed to write file %s: %w", f.path, err)
			}
			continue
		}
		if _, err := os.Stat(f.path); os.IsNotExist(err) {
			if err := os.WriteFile(f.path, []byte(f.content), f.mode); err != nil {
				return fmt.Errorf("failed to write file %s: %w", f.path, err)
			}
		}
	}

	return nil
}

// SeedFileFromFS writes a file from an embed.FS to a target path.
// If force is true, the file is always overwritten. Otherwise, it is only
// written if it does not already exist. alwaysOverwrite can be set to true
// for critical config files that should always match embedded defaults.
func SeedFileFromFS(fs embed.FS, basePath, fileName, targetPath string, force, alwaysOverwrite bool) error {
	data, err := fs.ReadFile(filepath.Join(basePath, fileName))
	if err != nil {
		return nil // File not in embeds, skip silently
	}

	if force || alwaysOverwrite {
		if err := os.WriteFile(targetPath, data, 0644); err != nil {
			return fmt.Errorf("failed to write file %s: %w", targetPath, err)
		}
		return nil
	}

	if _, err := os.Stat(targetPath); os.IsNotExist(err) {
		if err := os.WriteFile(targetPath, data, 0644); err != nil {
			return fmt.Errorf("failed to write file %s: %w", targetPath, err)
		}
	}
	return nil
}

// GenerateGroveID creates a grove ID based on git context.
// For git repos with remote: deterministic hash of the normalized remote URL
// For git repos without remote: UUID
// For non-git directories: UUID
func GenerateGroveID() string {
	if util.IsGitRepo() {
		remote := util.GetGitRemote()
		if remote != "" {
			return util.HashGroveID(util.NormalizeGitRemote(remote))
		}
	}
	return uuid.New().String()
}

// GenerateGroveIDForDir creates a grove ID based on git context for the specified directory.
func GenerateGroveIDForDir(dir string) string {
	if util.IsGitRepoDir(dir) {
		remote := util.GetGitRemoteDir(dir)
		if remote != "" {
			return util.HashGroveID(util.NormalizeGitRemote(remote))
		}
	}
	return uuid.New().String()
}

// IsInsideGrove returns true if the current working directory or any parent contains a .scion directory.
func IsInsideGrove() bool {
	_, ok := FindProjectRoot()
	return ok
}

// GetEnclosingGrovePath returns the path to the enclosing .scion directory if one exists,
// along with the root directory containing it.
func GetEnclosingGrovePath() (grovePath string, rootDir string, found bool) {
	wd, err := os.Getwd()
	if err != nil {
		return "", "", false
	}

	dir := wd
	for {
		p := filepath.Join(dir, DotScion)
		info, err := os.Stat(p)
		if err == nil {
			if info.IsDir() {
				if abs, err := filepath.EvalSymlinks(p); err == nil {
					return abs, dir, true
				}
				return p, dir, true
			}
			// .scion is a marker file — resolve to external path
			if resolved, err := ResolveGroveMarker(p); err == nil {
				return resolved, dir, true
			}
		}

		parent := filepath.Dir(dir)
		if parent == dir { // Reached filesystem root
			break
		}
		dir = parent
	}
	return "", "", false
}

// SeedAgnosticTemplate seeds the default agnostic template from embedded files.
// It recursively copies all files and directories (including home/.tmux.conf,
// home/.zshrc, etc.) into the target directory.
func SeedAgnosticTemplate(targetDir string, force bool) error {
	templateBase := "embeds/templates/default"

	if err := os.MkdirAll(targetDir, 0755); err != nil {
		return fmt.Errorf("failed to create template directory %s: %w", targetDir, err)
	}

	if err := fs.WalkDir(EmbedsFS, templateBase, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		// Compute relative path from the template base
		relPath, err := filepath.Rel(templateBase, path)
		if err != nil {
			return fmt.Errorf("failed to compute relative path for %s: %w", path, err)
		}

		// Skip the root directory itself
		if relPath == "." {
			return nil
		}

		targetPath := filepath.Join(targetDir, relPath)

		if d.IsDir() {
			if err := os.MkdirAll(targetPath, 0755); err != nil {
				return fmt.Errorf("failed to create directory %s: %w", targetPath, err)
			}
			return nil
		}

		// Read embedded file
		data, err := EmbedsFS.ReadFile(path)
		if err != nil {
			return fmt.Errorf("failed to read embedded file %s: %w", relPath, err)
		}

		// Skip if file exists and force is false
		if !force {
			if _, err := os.Stat(targetPath); err == nil {
				return nil
			}
		}

		if err := os.WriteFile(targetPath, data, 0644); err != nil {
			return fmt.Errorf("failed to write file %s: %w", targetPath, err)
		}

		return nil
	}); err != nil {
		return err
	}

	// Seed common files (.tmux.conf, .zshrc) from embeds/common/
	return SeedCommonFiles(targetDir, "", force)
}

// InitProjectOpts controls optional behavior for InitProject.
type InitProjectOpts struct {
	// SkipRuntimeCheck skips local container runtime detection.
	// Use this when initializing on a hub server where agents run on remote brokers.
	SkipRuntimeCheck bool
}

func InitProject(targetDir string, harnesses []api.Harness, opts ...InitProjectOpts) error {
	var opt InitProjectOpts
	if len(opts) > 0 {
		opt = opts[0]
	}

	isGit := util.IsGitRepo()

	var projectDir string
	var err error

	if targetDir != "" {
		projectDir = targetDir
	} else {
		projectDir, err = GetTargetProjectDir()
		if err != nil {
			return err
		}
	}

	// Enforce .scion in .gitignore for git repos
	if isGit {
		root, err := util.RepoRoot()
		if err == nil {
			_ = EnsureScionGitignore(root)
		}
	}

	// For non-git groves, externalize the grove data.
	// The .scion entry in the project directory becomes a marker file pointing
	// to ~/.scion/grove-configs/<slug>__<short-uuid>/.scion/
	if !isGit {
		return initExternalGrove(projectDir, opt)
	}

	// Git grove: create .scion as a directory (in-repo)
	return initInRepoGrove(projectDir, opt)
}

// initExternalGrove creates a non-git grove with externalized data.
// The project directory gets a .scion marker file, and the actual grove
// data lives under ~/.scion/grove-configs/.
func initExternalGrove(projectDir string, opt InitProjectOpts) error {
	// projectDir is the intended <project>/.scion path.
	projectRoot := filepath.Dir(projectDir)
	markerPath := filepath.Join(projectRoot, DotScion)

	// TODO(grove-migration): Remove this check after a few releases.
	// Detect old-style non-git grove (directory instead of marker file).
	if info, err := os.Stat(markerPath); err == nil && info.IsDir() {
		return fmt.Errorf("this grove at %s uses an outdated directory format.\n"+
			"Non-git groves now use externalized storage. Please:\n"+
			"  1. Back up any custom templates from %s/templates/\n"+
			"  2. Remove the .scion directory: rm -rf %s\n"+
			"  3. Re-initialize: scion init",
			projectRoot, markerPath, markerPath)
	}

	// If a marker file already exists, read it and use the existing external path
	if IsGroveMarkerFile(markerPath) {
		resolved, err := ResolveGroveMarker(markerPath)
		if err != nil {
			return fmt.Errorf("existing grove marker is invalid: %w", err)
		}
		// External grove already set up — just ensure directories exist
		return ensureGroveDirs(resolved, opt)
	}

	// Generate new grove identity
	groveID := GenerateGroveID()
	groveName := filepath.Base(projectRoot)
	groveSlug := api.Slugify(groveName)

	marker := &GroveMarker{
		GroveID:   groveID,
		GroveName: groveName,
		GroveSlug: groveSlug,
	}

	// Create external grove directory
	externalPath, err := marker.ExternalGrovePath()
	if err != nil {
		return fmt.Errorf("failed to compute external grove path: %w", err)
	}

	// Write settings with workspace-path and grove_id before ensureGroveDirs
	// (which would create a settings.yaml without workspace_path if one doesn't exist yet).
	absProjectRoot, _ := filepath.Abs(projectRoot)
	if err := os.MkdirAll(externalPath, 0755); err != nil {
		return fmt.Errorf("failed to create external grove directory: %w", err)
	}
	if GetSettingsPath(externalPath) == "" {
		if err := writeGroveSettings(externalPath, absProjectRoot, groveID, opt); err != nil {
			return err
		}
	}

	if err := ensureGroveDirs(externalPath, opt); err != nil {
		return err
	}

	// Ensure the project root directory exists before writing the marker
	if err := os.MkdirAll(projectRoot, 0755); err != nil {
		return fmt.Errorf("failed to create project directory: %w", err)
	}

	// Write the marker file
	if err := WriteGroveMarker(markerPath, marker); err != nil {
		return fmt.Errorf("failed to write grove marker: %w", err)
	}

	return nil
}

// initInRepoGrove creates a git grove with .scion as a directory in the repo.
// Settings are stored externally at ~/.scion/grove-configs/<slug>__<uuid>/.scion/
// and agent homes are stored externally at ~/.scion/grove-configs/<slug>__<uuid>/.scion/agents/.
// Templates live in the in-repo .scion/templates/ so they can be committed to the repository.
func initInRepoGrove(projectDir string, opt InitProjectOpts) error {
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		return fmt.Errorf("failed to create settings directory: %w", err)
	}

	// Ensure grove-id file exists for split storage
	if _, err := ReadGroveID(projectDir); err != nil {
		if os.IsNotExist(err) {
			groveID := GenerateGroveIDForDir(filepath.Dir(projectDir))
			if err := WriteGroveID(projectDir, groveID); err != nil {
				return fmt.Errorf("failed to write grove-id: %w", err)
			}
		} else {
			return fmt.Errorf("failed to read grove-id: %w", err)
		}
	}

	// Seed settings.yaml in the external config dir (machine-specific, not committed)
	externalConfigDir, err := GetGitGroveExternalConfigDir(projectDir)
	if err != nil {
		return fmt.Errorf("failed to compute external config path: %w", err)
	}
	if err := ensureGroveSettingsFile(externalConfigDir, opt); err != nil {
		return err
	}

	// Create external agents directory for agent homes
	externalAgentsDir, err := GetGitGroveExternalAgentsDir(projectDir)
	if err != nil {
		return fmt.Errorf("failed to compute external agents path: %w", err)
	}
	if externalAgentsDir != "" {
		if err := os.MkdirAll(externalAgentsDir, 0755); err != nil {
			return fmt.Errorf("failed to create external agents directory: %w", err)
		}
	}

	// Create in-repo agents dir for git worktrees only
	if err := os.MkdirAll(filepath.Join(projectDir, "agents"), 0755); err != nil {
		return fmt.Errorf("failed to create agents directory: %w", err)
	}

	// Create in-repo templates dir — lives in-repo so project templates can be committed
	if err := os.MkdirAll(filepath.Join(projectDir, "templates"), 0755); err != nil {
		return fmt.Errorf("failed to create templates directory: %w", err)
	}

	return nil
}

// ensureGroveSettingsFile creates settings.yaml in configDir if it doesn't exist.
// Unlike ensureGroveConfigFiles, it does not create templates/ (used for git groves
// where templates live in-repo).
func ensureGroveSettingsFile(configDir string, opt InitProjectOpts) error {
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return fmt.Errorf("failed to create grove config directory: %w", err)
	}

	settingsPath := GetSettingsPath(configDir)
	if settingsPath == "" {
		if !opt.SkipRuntimeCheck {
			if _, err := DetectLocalRuntime(); err != nil {
				return err
			}
		}

		defaultSettings, err := GetGroveDefaultSettingsYAML()
		if err != nil {
			return fmt.Errorf("failed to read default grove settings: %w", err)
		}
		newSettingsPath := filepath.Join(configDir, "settings.yaml")
		if err := os.WriteFile(newSettingsPath, defaultSettings, 0644); err != nil {
			return fmt.Errorf("failed to seed settings.yaml: %w", err)
		}
	}

	return nil
}

// ensureGroveConfigFiles creates settings.yaml and templates/ in configDir.
// It does not create the agents/ directory — that is handled separately.
func ensureGroveConfigFiles(configDir string, opt InitProjectOpts) error {
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return fmt.Errorf("failed to create grove config directory: %w", err)
	}

	settingsPath := GetSettingsPath(configDir)
	if settingsPath == "" {
		if !opt.SkipRuntimeCheck {
			if _, err := DetectLocalRuntime(); err != nil {
				return err
			}
		}

		defaultSettings, err := GetGroveDefaultSettingsYAML()
		if err != nil {
			return fmt.Errorf("failed to read default grove settings: %w", err)
		}
		newSettingsPath := filepath.Join(configDir, "settings.yaml")
		if err := os.WriteFile(newSettingsPath, defaultSettings, 0644); err != nil {
			return fmt.Errorf("failed to seed settings.yaml: %w", err)
		}
	}

	if err := os.MkdirAll(filepath.Join(configDir, "templates"), 0755); err != nil {
		return fmt.Errorf("failed to create templates directory: %w", err)
	}

	return nil
}

// ensureGroveDirs creates the standard grove subdirectories and seeds settings.
// Used for non-git groves and global grove where config and agents share the same dir.
func ensureGroveDirs(projectDir string, opt InitProjectOpts) error {
	if err := ensureGroveConfigFiles(projectDir, opt); err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Join(projectDir, "agents"), 0755); err != nil {
		return fmt.Errorf("failed to create agents directory: %w", err)
	}

	return nil
}

// writeGroveSettings writes the initial settings.yaml for an external grove,
// including the workspace-path field.
func writeGroveSettings(externalPath, workspacePath, groveID string, opt InitProjectOpts) error {
	if !opt.SkipRuntimeCheck {
		if _, err := DetectLocalRuntime(); err != nil {
			return err
		}
	}

	defaultSettings, err := GetGroveDefaultSettingsYAML()
	if err != nil {
		return fmt.Errorf("failed to read default grove settings: %w", err)
	}

	// Parse default settings, add workspace-path, and re-marshal
	var settingsMap map[string]interface{}
	if err := yaml.Unmarshal(defaultSettings, &settingsMap); err != nil {
		return fmt.Errorf("failed to parse default grove settings: %w", err)
	}
	settingsMap["workspace_path"] = workspacePath
	if groveID != "" {
		// In v1 format (schema_version: "1"), grove_id is stored under
		// hub.grove_id, not at the top level. Writing it at the top level
		// causes it to be silently dropped when UpdateVersionedSetting
		// round-trips through VersionedSettings (which has no top-level
		// grove_id field), leading to the global hub.grove_id bleeding
		// into local groves.
		if v, _ := settingsMap["schema_version"]; v == "1" {
			hub, _ := settingsMap["hub"].(map[string]interface{})
			if hub == nil {
				hub = make(map[string]interface{})
				settingsMap["hub"] = hub
			}
			hub["grove_id"] = groveID
		} else {
			settingsMap["grove_id"] = groveID
		}
	}

	data, err := yaml.Marshal(settingsMap)
	if err != nil {
		return fmt.Errorf("failed to marshal grove settings: %w", err)
	}

	settingsFile := filepath.Join(externalPath, "settings.yaml")
	if err := os.WriteFile(settingsFile, data, 0644); err != nil {
		return fmt.Errorf("failed to write grove settings.yaml: %w", err)
	}

	return nil
}

// InitMachineOpts controls optional behavior for InitMachine.
type InitMachineOpts struct {
	// ImageRegistry is the container image registry to configure.
	// If non-empty, it is written into settings after seeding.
	ImageRegistry string

	// Force overwrites existing template and harness-config files with the
	// versions embedded in the binary. Use this to refresh after a binary upgrade.
	Force bool
}

// InitMachine performs full global/machine-level setup: creates ~/.scion/,
// seeds settings, harness-configs, and the default agnostic template.
func InitMachine(harnesses []api.Harness, opts ...InitMachineOpts) error {
	globalDir, err := GetGlobalDir()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(globalDir, 0755); err != nil {
		return fmt.Errorf("failed to create global directory: %w", err)
	}

	// Create global settings file if it doesn't exist
	settingsPath := GetSettingsPath(globalDir)
	if settingsPath == "" {
		// Detect a functioning container runtime before seeding settings
		detectedRuntime, err := DetectLocalRuntime()
		if err != nil {
			return err
		}

		// Seed default YAML settings with the detected runtime
		defaultSettings, err := getDefaultSettingsYAMLForRuntime(detectedRuntime)
		if err != nil {
			// Fall back to JSON defaults
			defaultSettings, err = getDefaultSettingsDataForRuntime(detectedRuntime)
			if err != nil {
				return fmt.Errorf("failed to read default settings: %w", err)
			}
		}
		newSettingsPath := filepath.Join(globalDir, "settings.yaml")
		if err := os.WriteFile(newSettingsPath, defaultSettings, 0644); err != nil {
			return fmt.Errorf("failed to seed global settings.yaml: %w", err)
		}
	}

	var opt InitMachineOpts
	if len(opts) > 0 {
		opt = opts[0]
	}

	templatesDir := filepath.Join(globalDir, "templates")
	agentsDir := filepath.Join(globalDir, "agents")
	harnessConfigsDir := filepath.Join(globalDir, harnessConfigsDirName)

	if err := os.MkdirAll(agentsDir, 0755); err != nil {
		return fmt.Errorf("failed to create global agents directory: %w", err)
	}

	// Seed default agnostic template
	if err := SeedAgnosticTemplate(filepath.Join(templatesDir, "default"), opt.Force); err != nil {
		return fmt.Errorf("failed to seed global default agnostic template: %w", err)
	}

	for _, h := range harnesses {
		// Seed harness-config directory
		if err := SeedHarnessConfig(filepath.Join(harnessConfigsDir, h.Name()), h, opt.Force); err != nil {
			return fmt.Errorf("failed to seed global %s harness-config: %w", h.Name(), err)
		}
	}

	// Pre-populate a broker ID so this machine has a stable identity.
	// This will be overwritten if the user later registers with a Hub.
	if err := ensureBrokerID(globalDir); err != nil {
		return fmt.Errorf("failed to pre-populate broker ID: %w", err)
	}

	if opt.ImageRegistry != "" {
		if err := UpdateSetting(globalDir, "image_registry", opt.ImageRegistry, true); err != nil {
			return fmt.Errorf("failed to set image_registry: %w", err)
		}
	}

	return nil
}

// ensureBrokerID checks whether a broker ID already exists in the global settings
// and generates one if not. This gives the machine a stable identity before
// Hub registration.
func ensureBrokerID(globalDir string) error {
	settings, err := LoadSettings(globalDir)
	if err != nil {
		// If we can't load settings, skip — not critical
		return nil
	}

	// Check if broker ID is already set (via legacy or versioned path)
	if settings.Hub != nil && settings.Hub.BrokerID != "" {
		return nil
	}

	brokerID := uuid.New().String()
	return UpdateSetting(globalDir, "hub.brokerId", brokerID, true)
}

// InitGlobal is an alias for InitMachine, kept for backward compatibility.
func InitGlobal(harnesses []api.Harness, opts ...InitMachineOpts) error {
	return InitMachine(harnesses, opts...)
}

// EnsureScionGitignore ensures that .scion/ is listed in the .gitignore file
// at the given repo root. If .scion or .scion/ is already ignored by any
// pattern, this is a no-op.
func EnsureScionGitignore(repoRoot string) error {
	gitignorePath := filepath.Join(repoRoot, ".gitignore")

	content, err := os.ReadFile(gitignorePath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	// Check if .scion agents dir is already covered by an existing pattern.
	// A broad .scion/ pattern also covers agents/, so treat it as already handled.
	for _, line := range strings.Split(string(content), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == ".scion" || trimmed == ".scion/" || trimmed == "/.scion" || trimmed == "/.scion/" ||
			trimmed == ".scion/agents" || trimmed == ".scion/agents/" {
			return nil
		}
	}

	// Append .scion/agents/ to .gitignore. Only the agents directory (worktrees and
	// agent homes) is excluded; templates/ and other config files can be committed.
	var newContent string
	if len(content) > 0 && content[len(content)-1] != '\n' {
		newContent = string(content) + "\n.scion/agents/\n"
	} else {
		newContent = string(content) + ".scion/agents/\n"
	}

	return os.WriteFile(gitignorePath, []byte(newContent), 0644)
}
