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
	"path/filepath"
	"regexp"

	"github.com/GoogleCloudPlatform/scion/pkg/brokercontrol"
)

// namedVolumePattern matches a container runtime's named volume (Docker and
// Podman: a name with no path separator). A named volume lives in the
// runtime's own storage, not at a host path, so it cannot reach a Runtime
// Broker control file.
var namedVolumePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]+$`)

// isNamedVolume reports whether a bind source is a named volume rather than
// a host path.
func isNamedVolume(src string) bool {
	return !filepath.IsAbs(src) && namedVolumePattern.MatchString(src)
}

// logControlPathRefusal records the operator-facing detail of a refused
// mount source; the error returned to the requester carries no resolved
// host path.
func logControlPathRefusal(err error) {
	var re *brokercontrol.RefusalError
	if errors.As(err, &re) {
		runtimeLog().Warn("Refused a mount source that reaches Runtime Broker control files",
			"code", brokercontrol.ErrorCode, "detail", re.Detail())
	}
}

// checkBindSource is the last check on every host-path bind before it is
// emitted: the source may not reach a named Runtime Broker control path or
// contain the Scion global directory. Where a source inside the global
// directory may come from is decided earlier, by the code that knows what
// kind of mount it is (configured volumes: checkConfiguredVolumeSource;
// workspace and home: their own guards; auth files and development binaries:
// checkHostFileSource). It returns the resolved source, which is what must
// be mounted. Named volumes are returned unchanged.
func checkBindSource(src string) (string, error) {
	if isNamedVolume(src) {
		return src, nil
	}
	resolved, err := brokercontrol.CheckNamed(src)
	if err != nil {
		logControlPathRefusal(err)
		return "", err
	}
	return resolved, nil
}

// checkConfiguredVolumeSource applies the control-path check to a configured
// volume (settings, profiles, templates, harness configs, shared dirs, the
// Hub's inline config): inside the Scion global directory only the
// agent-data subtrees of brokercontrol.VolumeAllowed are allowed. Read-only
// volumes are checked the same way.
func checkConfiguredVolumeSource(src string) error {
	if isNamedVolume(src) {
		return nil
	}
	if _, err := brokercontrol.Check(src, brokercontrol.VolumeAllowed); err != nil {
		logControlPathRefusal(err)
		return err
	}
	return nil
}

// checkHostFileSource applies the control-path check to a host path that is
// copied or bound into an agent and never legitimately comes from the Scion
// global directory (harness auth files, development binaries). It returns
// the resolved source.
func checkHostFileSource(src string) (string, error) {
	resolved, err := brokercontrol.Check(src, nil)
	if err != nil {
		logControlPathRefusal(err)
		return "", err
	}
	return resolved, nil
}
