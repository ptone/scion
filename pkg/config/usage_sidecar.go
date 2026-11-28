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
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
	"github.com/GoogleCloudPlatform/scion/pkg/telemetrycontract"
	"gopkg.in/yaml.v3"
)

// UsageSidecarFile is the harness-config bundle file that declares usage
// metering capabilities (Track 1 live metering and, in later phases, Track 2
// trajectory normalization metadata).
//
// It lives next to config.yaml instead of inside config.yaml for the same
// reason as AgentPluginsSidecarFile and NoAuthEnvSidecarFile: deployed
// binaries validate config.yaml against a strict schema that rejects unknown
// keys (ptone/scion#5196). Older binaries ignore unknown bundle files, and the
// file travels with the bundle on seeding and harness-config upgrade without
// requiring image rebuilds.
const UsageSidecarFile = "usage.yaml"

// UsageSidecarSchemaVersion is the supported schema_version of usage.yaml.
const UsageSidecarSchemaVersion = 1

// Normalizer defaults and validation constants per P7.1 contract §1 and D4.
const (
	UsageNormalizerInterfaceVersion     = 1
	UsageNormalizerBundleDirPlaceholder = "{bundle_dir}"

	DefaultUsageNormalizerTimeout            = api.DefaultUsageNormalizerTimeout
	DefaultUsageNormalizerTimeoutString      = api.DefaultUsageNormalizerTimeoutString
	DefaultUsageNormalizerCPUSeconds         = api.DefaultUsageNormalizerCPUSeconds
	DefaultUsageNormalizerMaxAddressSpaceMiB = api.DefaultUsageNormalizerMaxAddressSpaceMiB
	DefaultUsageNormalizerMaxRSSMiB          = api.DefaultUsageNormalizerMaxRSSMiB
)

var usageNormalizerParserVersionRe = regexp.MustCompile(`^[a-z0-9-]+-[0-9]+$`)

// usageSidecarFileDoc is the on-disk shape of UsageSidecarFile. Unknown keys
// are ignored by yaml.Unmarshal so newer bundle additions do not break older
// binaries.
type usageSidecarFileDoc struct {
	SchemaVersion int              `yaml:"schema_version"`
	Usage         usageSidecarBody `yaml:"usage"`
}

type usageSidecarBody struct {
	Live       api.HarnessUsageLiveCapabilities `yaml:"live"`
	Trajectory *usageSidecarTrajectoryBody      `yaml:"trajectory"`
}

type usageSidecarTrajectoryBody struct {
	Normalizer *api.HarnessUsageNormalizerConfig `yaml:"normalizer"`
}

// ParseUsageSidecar strictly parses and validates the bytes of a
// UsageSidecarFile (schema_version: 1), including usage.trajectory.normalizer
// when present. Unknown keys at any level are ignored for forward
// compatibility. Runtime loaders do not fail on its error (they warn and leave
// the capability or normalizer nil/disabled); tests of the bundled files call
// it directly so a bad bundled file fails CI.
func ParseUsageSidecar(data []byte) (*api.HarnessUsageCapabilities, error) {
	out, liveErr, normalizerErr := parseUsageSidecar(data)
	if liveErr != nil {
		return nil, liveErr
	}
	if normalizerErr != nil {
		return nil, normalizerErr
	}
	return out, nil
}

// parseUsageSidecar parses data and separates live-section errors from
// trajectory.normalizer errors so runtime loaders can degrade an invalid
// normalizer block to disabled without losing Track 1 live capabilities.
func parseUsageSidecar(data []byte) (*api.HarnessUsageCapabilities, error, error) {
	var doc usageSidecarFileDoc
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("invalid %s: %w", UsageSidecarFile, err), nil
	}
	if doc.SchemaVersion != UsageSidecarSchemaVersion {
		return nil, fmt.Errorf("invalid %s: schema_version must be %d, got %d", UsageSidecarFile, UsageSidecarSchemaVersion, doc.SchemaVersion), nil
	}
	live := doc.Usage.Live
	if !api.ValidUsageLiveSource(live.Source) {
		return nil, fmt.Errorf("invalid %s: usage.live.source must be one of native, hooks, sqlite_native, file_tailer, none; got %q", UsageSidecarFile, live.Source), nil
	}

	seenTokens, err := validateUsageTokenTypeList("usage.live.token_types", live.TokenTypes)
	if err != nil {
		return nil, fmt.Errorf("invalid %s: %w", UsageSidecarFile, err), nil
	}
	seenNA, err := validateUsageTokenTypeList("usage.live.not_applicable_token_types", live.NotApplicableTokenTypes)
	if err != nil {
		return nil, fmt.Errorf("invalid %s: %w", UsageSidecarFile, err), nil
	}
	for tt := range seenNA {
		if seenTokens[tt] {
			return nil, fmt.Errorf("invalid %s: token type %q cannot appear in both token_types and not_applicable_token_types", UsageSidecarFile, tt), nil
		}
	}

	if live.Source == api.UsageLiveSourceNone {
		if len(live.TokenTypes) > 0 {
			return nil, fmt.Errorf("invalid %s: usage.live.token_types must be empty when source is none", UsageSidecarFile), nil
		}
		if live.CountsFailedCalls {
			return nil, fmt.Errorf("invalid %s: usage.live.counts_failed_calls must be false when source is none", UsageSidecarFile), nil
		}
	}

	out := &api.HarnessUsageCapabilities{
		SchemaVersion: doc.SchemaVersion,
		Live: api.HarnessUsageLiveCapabilities{
			Source:            live.Source,
			CountsFailedCalls: live.CountsFailedCalls,
		},
	}
	if len(live.TokenTypes) > 0 {
		out.Live.TokenTypes = append([]string(nil), live.TokenTypes...)
	}
	if len(live.NotApplicableTokenTypes) > 0 {
		out.Live.NotApplicableTokenTypes = append([]string(nil), live.NotApplicableTokenTypes...)
	}

	if doc.Usage.Trajectory != nil && doc.Usage.Trajectory.Normalizer != nil {
		norm, normErr := validateUsageNormalizer(doc.Usage.Trajectory.Normalizer)
		if normErr != nil {
			return out, nil, fmt.Errorf("invalid %s: %w", UsageSidecarFile, normErr)
		}
		out.Trajectory = &api.HarnessUsageTrajectoryCapabilities{
			Normalizer: norm,
		}
	}

	return out, nil, nil
}

func validateUsageNormalizer(raw *api.HarnessUsageNormalizerConfig) (*api.HarnessUsageNormalizerConfig, error) {
	if len(raw.Command) > 0 {
		for i, arg := range raw.Command {
			if arg == "" {
				return nil, fmt.Errorf("usage.trajectory.normalizer.command[%d] must be non-empty", i)
			}
		}
		cmd0 := raw.Command[0]
		if !strings.HasPrefix(cmd0, "/") && !strings.HasPrefix(cmd0, UsageNormalizerBundleDirPlaceholder+"/") {
			return nil, fmt.Errorf("usage.trajectory.normalizer.command[0] must be an absolute path (or start with %s/), got %q", UsageNormalizerBundleDirPlaceholder, cmd0)
		}
	}

	if raw.InterfaceVersion != UsageNormalizerInterfaceVersion {
		return nil, fmt.Errorf("usage.trajectory.normalizer.interface_version must be %d, got %d", UsageNormalizerInterfaceVersion, raw.InterfaceVersion)
	}

	if raw.ParserVersion == "" {
		return nil, errors.New("usage.trajectory.normalizer.parser_version must be non-empty")
	}
	if !usageNormalizerParserVersionRe.MatchString(raw.ParserVersion) {
		return nil, fmt.Errorf("usage.trajectory.normalizer.parser_version %q must match %s", raw.ParserVersion, usageNormalizerParserVersionRe.String())
	}

	timeoutStr := strings.TrimSpace(raw.Timeout)
	if timeoutStr == "" {
		timeoutStr = DefaultUsageNormalizerTimeoutString
	} else {
		d, err := time.ParseDuration(timeoutStr)
		if err != nil {
			return nil, fmt.Errorf("usage.trajectory.normalizer.timeout %q: %w", timeoutStr, err)
		}
		if d <= 0 {
			return nil, fmt.Errorf("usage.trajectory.normalizer.timeout must be positive, got %q", timeoutStr)
		}
	}

	limits := raw.Limits
	if limits.CPUSeconds < 0 {
		return nil, fmt.Errorf("usage.trajectory.normalizer.limits.cpu_seconds must be non-negative, got %d", limits.CPUSeconds)
	}
	if limits.CPUSeconds == 0 {
		limits.CPUSeconds = DefaultUsageNormalizerCPUSeconds
	}
	if limits.MaxAddressSpaceMiB < 0 {
		return nil, fmt.Errorf("usage.trajectory.normalizer.limits.max_address_space_mib must be non-negative, got %d", limits.MaxAddressSpaceMiB)
	}
	if limits.MaxAddressSpaceMiB == 0 {
		limits.MaxAddressSpaceMiB = DefaultUsageNormalizerMaxAddressSpaceMiB
	}
	if limits.MaxRSSMiB < 0 {
		return nil, fmt.Errorf("usage.trajectory.normalizer.limits.max_rss_mib must be non-negative, got %d", limits.MaxRSSMiB)
	}
	if limits.MaxRSSMiB == 0 {
		limits.MaxRSSMiB = DefaultUsageNormalizerMaxRSSMiB
	}

	for i, pat := range raw.TestedHarnessVersions {
		if strings.TrimSpace(pat) == "" {
			return nil, fmt.Errorf("usage.trajectory.normalizer.tested_harness_versions[%d] must be non-empty", i)
		}
		if _, err := path.Match(pat, ""); err != nil {
			return nil, fmt.Errorf("usage.trajectory.normalizer.tested_harness_versions[%d] %q: %w", i, pat, err)
		}
	}

	seenOutputs := make(map[string]bool, len(raw.Outputs))
	for _, outType := range raw.Outputs {
		if !api.ValidUsageNormalizerOutput(outType) {
			return nil, fmt.Errorf("usage.trajectory.normalizer.outputs contains unknown output %q", outType)
		}
		if seenOutputs[outType] {
			return nil, fmt.Errorf("usage.trajectory.normalizer.outputs contains duplicate output %q", outType)
		}
		seenOutputs[outType] = true
	}

	norm := &api.HarnessUsageNormalizerConfig{
		InterfaceVersion: raw.InterfaceVersion,
		ParserVersion:    raw.ParserVersion,
		Timeout:          timeoutStr,
		Limits:           limits,
	}
	if len(raw.Command) > 0 {
		norm.Command = append([]string(nil), raw.Command...)
	}
	if len(raw.TestedHarnessVersions) > 0 {
		norm.TestedHarnessVersions = append([]string(nil), raw.TestedHarnessVersions...)
	}
	if len(raw.Outputs) > 0 {
		norm.Outputs = append([]string(nil), raw.Outputs...)
	}
	return norm, nil
}

// ResolveNormalizerCommand substitutes {bundle_dir} in cfg.Command with
// bundleDir (which must be an absolute path) and verifies that the resulting
// command[0] is an absolute path.
func ResolveNormalizerCommand(cfg *api.HarnessUsageNormalizerConfig, bundleDir string) ([]string, error) {
	if cfg == nil || len(cfg.Command) == 0 {
		return nil, errors.New("normalizer command is empty")
	}
	if bundleDir == "" || !filepath.IsAbs(bundleDir) {
		return nil, fmt.Errorf("bundle_dir must be an absolute path, got %q", bundleDir)
	}
	cleanBundleDir := filepath.Clean(bundleDir)
	resolved := make([]string, len(cfg.Command))
	for i, arg := range cfg.Command {
		resolved[i] = strings.ReplaceAll(arg, UsageNormalizerBundleDirPlaceholder, cleanBundleDir)
	}
	if !filepath.IsAbs(resolved[0]) {
		return nil, fmt.Errorf("resolved normalizer command[0] must be an absolute path, got %q", resolved[0])
	}
	return resolved, nil
}

// ParseNormalizerTimeout parses cfg.Timeout, defaulting to
// DefaultUsageNormalizerTimeout when empty.
func ParseNormalizerTimeout(cfg *api.HarnessUsageNormalizerConfig) (time.Duration, error) {
	if cfg == nil || strings.TrimSpace(cfg.Timeout) == "" {
		return DefaultUsageNormalizerTimeout, nil
	}
	d, err := time.ParseDuration(strings.TrimSpace(cfg.Timeout))
	if err != nil {
		return 0, fmt.Errorf("invalid normalizer timeout %q: %w", cfg.Timeout, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("normalizer timeout must be positive, got %q", cfg.Timeout)
	}
	return d, nil
}

func validateUsageTokenTypeList(field string, list []string) (map[string]bool, error) {
	seen := make(map[string]bool, len(list))
	for _, tt := range list {
		if !telemetrycontract.ValidTokenType(tt) {
			return nil, fmt.Errorf("%s contains unknown token type %q", field, tt)
		}
		if seen[tt] {
			return nil, fmt.Errorf("%s contains duplicate token type %q", field, tt)
		}
		seen[tt] = true
	}
	return seen, nil
}

// ApplyUsageSidecarBytes parses raw usage.yaml bytes and records the
// capability on entry. Invalid or unsupported-version content logs a
// deduplicated warning and leaves entry.UsageSidecar nil (unknown capability)
// when the live section is invalid, or disables the trajectory normalizer
// (Trajectory == nil) while preserving Live when only the normalizer block is
// invalid. It never fails the harness-config load.
func ApplyUsageSidecarBytes(entry *HarnessConfigEntry, data []byte, where string) {
	caps, liveErr, normalizerErr := parseUsageSidecar(data)
	if liveErr != nil {
		warnUsageSidecar(where, liveErr)
		entry.UsageSidecar = nil
		return
	}
	if normalizerErr != nil {
		warnUsageNormalizer(where, normalizerErr)
	}
	entry.UsageSidecar = caps
}

// ApplyUsageSidecarError records an unreadable/oversize usage.yaml error on
// entry by logging a deduplicated warning and leaving entry.UsageSidecar nil.
func ApplyUsageSidecarError(entry *HarnessConfigEntry, err error, where string) {
	warnUsageSidecar(where, fmt.Errorf("invalid %s: %w", UsageSidecarFile, err))
	entry.UsageSidecar = nil
}

// ApplyUsageSidecarDir records dir's UsageSidecarFile on entry. A missing file
// leaves entry unchanged; an unreadable or invalid one warns and leaves
// UsageSidecar nil.
func ApplyUsageSidecarDir(entry *HarnessConfigEntry, dir string) {
	p := filepath.Join(dir, UsageSidecarFile)
	data, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	if err != nil {
		ApplyUsageSidecarError(entry, err, p)
		return
	}
	ApplyUsageSidecarBytes(entry, data, p)
}

// ApplyUsageSidecarFS is ApplyUsageSidecarDir for a bundle directory dir
// inside fsys (for example the embedded harnesses).
func ApplyUsageSidecarFS(entry *HarnessConfigEntry, fsys fs.FS, dir string) {
	p := path.Join(dir, UsageSidecarFile)
	data, err := fs.ReadFile(fsys, p)
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	if err != nil {
		ApplyUsageSidecarError(entry, err, p)
		return
	}
	ApplyUsageSidecarBytes(entry, data, p)
}

func applyUsageSidecarFromReader(entry *HarnessConfigEntry, read func(name string) ([]byte, error), where string) {
	data, err := read(UsageSidecarFile)
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	if err != nil {
		ApplyUsageSidecarError(entry, err, where)
		return
	}
	ApplyUsageSidecarBytes(entry, data, where)
}

func warnUsageSidecar(where string, err error) {
	if _, seen := invalidSidecarWarned.LoadOrStore(where+"\x00"+err.Error(), true); !seen {
		slog.Warn("ignoring invalid harness-config usage.yaml; usage capability treated as unknown",
			"path", where, "error", err)
	}
}

func warnUsageNormalizer(where string, err error) {
	if _, seen := invalidSidecarWarned.LoadOrStore(where+"\x00normalizer\x00"+err.Error(), true); !seen {
		slog.Warn("ignoring invalid usage.trajectory.normalizer in usage.yaml; trajectory normalizer disabled",
			"path", where, "error", err)
	}
}
