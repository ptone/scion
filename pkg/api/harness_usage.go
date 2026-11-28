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

package api

import "time"

// Normalizer default limits and timeout per P7.1 contract §1 and D4.
const (
	DefaultUsageNormalizerTimeout            = 60 * time.Second
	DefaultUsageNormalizerTimeoutString      = "60s"
	DefaultUsageNormalizerCPUSeconds         = 30
	DefaultUsageNormalizerMaxAddressSpaceMiB = 1024
	DefaultUsageNormalizerMaxRSSMiB          = 256
)

// UsageLiveSource identifies the Track 1 live usage metering mechanism
// declared by a harness bundle's usage.yaml sidecar.
type UsageLiveSource string

const (
	UsageLiveSourceNative       UsageLiveSource = "native"
	UsageLiveSourceHooks        UsageLiveSource = "hooks"
	UsageLiveSourceSQLiteNative UsageLiveSource = "sqlite_native"
	UsageLiveSourceFileTailer   UsageLiveSource = "file_tailer"
	UsageLiveSourceNone         UsageLiveSource = "none"
)

// ValidUsageLiveSource reports whether s is a known usage.live.source value.
func ValidUsageLiveSource(s UsageLiveSource) bool {
	switch s {
	case UsageLiveSourceNative,
		UsageLiveSourceHooks,
		UsageLiveSourceSQLiteNative,
		UsageLiveSourceFileTailer,
		UsageLiveSourceNone:
		return true
	default:
		return false
	}
}

// HarnessUsageLiveCapabilities describes Track 1 live usage metering
// capabilities declared under usage.live in a harness bundle's usage.yaml.
type HarnessUsageLiveCapabilities struct {
	Source                  UsageLiveSource `json:"source" yaml:"source" koanf:"source"`
	TokenTypes              []string        `json:"token_types,omitempty" yaml:"token_types,omitempty" koanf:"token_types"`
	NotApplicableTokenTypes []string        `json:"not_applicable_token_types,omitempty" yaml:"not_applicable_token_types,omitempty" koanf:"not_applicable_token_types"`
	CountsFailedCalls       bool            `json:"counts_failed_calls" yaml:"counts_failed_calls" koanf:"counts_failed_calls"`
}

// UsageNormalizerOutput constants identify the output record types a harness
// trajectory normalizer may declare in usage.yaml.
const (
	UsageNormalizerOutputUsageRecord = "usage_record"
	UsageNormalizerOutputATIFStep    = "atif_step"
	UsageNormalizerOutputSessionMeta = "session_meta"
)

// ValidUsageNormalizerOutput reports whether s is a known normalizer output type.
func ValidUsageNormalizerOutput(s string) bool {
	switch s {
	case UsageNormalizerOutputUsageRecord,
		UsageNormalizerOutputATIFStep,
		UsageNormalizerOutputSessionMeta:
		return true
	default:
		return false
	}
}

// HarnessUsageNormalizerLimits describes resource limits for a harness
// trajectory normalizer invocation declared under
// usage.trajectory.normalizer.limits in usage.yaml.
type HarnessUsageNormalizerLimits struct {
	CPUSeconds         int `json:"cpu_seconds,omitempty" yaml:"cpu_seconds,omitempty" koanf:"cpu_seconds"`
	MaxAddressSpaceMiB int `json:"max_address_space_mib,omitempty" yaml:"max_address_space_mib,omitempty" koanf:"max_address_space_mib"`
	MaxRSSMiB          int `json:"max_rss_mib,omitempty" yaml:"max_rss_mib,omitempty" koanf:"max_rss_mib"`
}

// HarnessUsageNormalizerConfig describes the Track 2 trajectory normalizer
// declared under usage.trajectory.normalizer in usage.yaml.
type HarnessUsageNormalizerConfig struct {
	Command               []string                     `json:"command,omitempty" yaml:"command,omitempty" koanf:"command"`
	InterfaceVersion      int                          `json:"interface_version" yaml:"interface_version" koanf:"interface_version"`
	ParserVersion         string                       `json:"parser_version" yaml:"parser_version" koanf:"parser_version"`
	TestedHarnessVersions []string                     `json:"tested_harness_versions,omitempty" yaml:"tested_harness_versions,omitempty" koanf:"tested_harness_versions"`
	Timeout               string                       `json:"timeout,omitempty" yaml:"timeout,omitempty" koanf:"timeout"`
	Limits                HarnessUsageNormalizerLimits `json:"limits,omitempty" yaml:"limits,omitempty" koanf:"limits"`
	Outputs               []string                     `json:"outputs,omitempty" yaml:"outputs,omitempty" koanf:"outputs"`
}

// HarnessUsageTrajectoryCapabilities describes Track 2 trajectory capabilities
// declared under usage.trajectory in a harness bundle's usage.yaml.
type HarnessUsageTrajectoryCapabilities struct {
	Normalizer *HarnessUsageNormalizerConfig `json:"normalizer,omitempty" yaml:"normalizer,omitempty" koanf:"normalizer"`
}

// HarnessUsageCapabilities describes usage metering capabilities declared in
// a harness bundle's usage.yaml sidecar (config.UsageSidecarFile).
type HarnessUsageCapabilities struct {
	SchemaVersion int                                 `json:"schema_version" yaml:"schema_version" koanf:"schema_version"`
	Live          HarnessUsageLiveCapabilities        `json:"live" yaml:"live" koanf:"live"`
	Trajectory    *HarnessUsageTrajectoryCapabilities `json:"trajectory,omitempty" yaml:"trajectory,omitempty" koanf:"trajectory"`
}
