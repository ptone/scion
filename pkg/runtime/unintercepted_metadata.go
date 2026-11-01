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
	"errors"
	"fmt"
	"log/slog"
)

// ErrUninterceptedMetadataRefused marks a start refused because the runtime
// cannot install the in-container metadata interception the agent's GCP
// metadata mode requests, and the operator has not opted out
// (checkUninterceptedMetadata).
var ErrUninterceptedMetadataRefused = errors.New("metadata interception cannot be installed")

// UninterceptedMetadataSetting is the operator-only settings block that
// allows such starts.
const UninterceptedMetadataSetting = "runtime.allow_unintercepted_metadata"

// uninterceptedMetadataCloudHost is the cloud-host detector used when such a
// start is decided. Tests replace it.
var uninterceptedMetadataCloudHost = func() CloudHost { return DetectCloudHost("/") }

// checkUninterceptedMetadata decides a start that requests the metadata
// interception (config.MetadataInterception) on a runtime that cannot
// install it. It is fail-closed: the start is refused unless the operator set
// both config.AllowUninterceptedMetadata and
// config.UninterceptedMetadataCloudAck, which come only from the broker's
// global settings.
//
// A host that is not detected as a cloud host is treated as one, because
// detection cannot confirm that a host has no instance metadata server, so
// the cloud acknowledgement is always required. Detection only refines the
// refusal message and the acknowledgement log.
func checkUninterceptedMetadata(runtimeLabel string, config RunConfig) error {
	cloud := uninterceptedMetadataCloudHost()
	if config.AllowUninterceptedMetadata && config.UninterceptedMetadataCloudAck {
		slog.Warn("Start: metadata interception cannot be installed by this runtime; the start is allowed by the operator setting",
			"agent", config.Name, "runtime", runtimeLabel, "setting", UninterceptedMetadataSetting,
			"cloud_host_detected", cloud.Detected, "detection", cloud.Source)
		return nil
	}

	host := "this broker's host is not confirmed to be outside a cloud, so it is treated as a cloud host"
	if cloud.Detected {
		host = fmt.Sprintf("this broker runs on a detected cloud host (%s)", cloud.Provider)
	}
	missing := UninterceptedMetadataSetting + ".enabled and " + UninterceptedMetadataSetting + ".acknowledge_cloud_host"
	if config.AllowUninterceptedMetadata {
		missing = UninterceptedMetadataSetting + ".acknowledge_cloud_host"
	}
	return fmt.Errorf("cannot start agent %q: %w: %s cannot install the in-container metadata interception that GCP metadata mode \"block\" or \"assign\" requires, and %s; use rootful Podman or Docker, or set %s to true in the broker's global settings to accept this",
		config.Name, ErrUninterceptedMetadataRefused, runtimeLabel, host, missing)
}
