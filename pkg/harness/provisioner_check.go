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

package harness

import (
	"errors"
	"fmt"
	"strings"

	"github.com/GoogleCloudPlatform/scion/pkg/config"
)

// LegacyBuiltinProvisionerType is the provisioner.type of harness-configs
// written before container-script provisioning. Its compiled-in Go
// implementation has been removed, so such a harness-config cannot
// provision a harness (see harnesses/README.md).
const LegacyBuiltinProvisionerType = "builtin"

// ErrUnusableProvisioner is the sentinel every UnusableProvisionerError
// wraps, so callers can classify the failure with errors.Is.
var ErrUnusableProvisioner = errors.New("harness-config provisioner is not usable")

// UnusableProvisionerError reports a harness-config whose provisioner block
// cannot provision the harness: provisioner.type "builtin", or a
// container-script provisioner without a command. An agent launched from it
// would start without the provisioner's output (auth, harness-native
// config), so launch fails before any container is created
// (ptone/scion#611).
type UnusableProvisionerError struct {
	Name   string                     // harness-config name
	Path   string                     // harness-config directory, if any
	Source config.HarnessConfigSource // resolution branch, if known
	Type   string                     // provisioner.type as written
	Reason string                     // what is wrong with the provisioner
}

func (e *UnusableProvisionerError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "harness-config %q", e.Name)
	if e.Path != "" {
		fmt.Fprintf(&b, " (%s", e.Path)
		if e.Source != "" {
			fmt.Fprintf(&b, ", source %s", e.Source)
		}
		b.WriteString(")")
	}
	fmt.Fprintf(&b, " cannot be used: %s. ", e.Reason)
	b.WriteString(e.Fix())
	return b.String()
}

// Fix returns the action that repairs the harness-config.
func (e *UnusableProvisionerError) Fix() string {
	fix := fmt.Sprintf("Run `scion harness-config upgrade %s --activate-script`, or reinstall it with `scion harness-config install harnesses/<harness>`", e.Name)
	if e.Source == config.HarnessConfigSourceHubHydrated {
		fix += fmt.Sprintf(", then update the hub copy with `scion harness-config sync %s`", e.Name)
	}
	return fix
}

func (e *UnusableProvisionerError) Unwrap() error { return ErrUnusableProvisioner }

// checkProvisionerUsable returns an *UnusableProvisionerError when entry's
// provisioner block cannot provision the harness, nil otherwise (including
// when there is no provisioner block).
func checkProvisionerUsable(name string, hcDir *config.HarnessConfigDir, entry config.HarnessConfigEntry) error {
	prov := entry.Provisioner
	if prov == nil {
		return nil
	}
	var reason string
	switch {
	case prov.Type == LegacyBuiltinProvisionerType:
		reason = `provisioner.type "builtin" is no longer supported (the built-in provisioner was removed)`
	case len(prov.Command) == 0:
		reason = "provisioner.command is empty, so the container-script provisioner has nothing to run"
	default:
		return nil
	}
	e := &UnusableProvisionerError{Name: name, Type: prov.Type, Reason: reason}
	if hcDir != nil {
		e.Path = hcDir.Path
		e.Source = hcDir.Source
	}
	return e
}
