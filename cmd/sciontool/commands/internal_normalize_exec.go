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

package commands

import (
	"errors"
	"fmt"
	"path/filepath"

	"github.com/GoogleCloudPlatform/scion/pkg/sciontool/trajectory"
	"github.com/spf13/cobra"
)

func newInternalNormalizeExecCmd() *cobra.Command {
	var (
		asMiB      int
		cpuSeconds int
	)

	cmd := &cobra.Command{
		Use:    "normalize-exec -- <cmd> [args...]",
		Short:  "Apply resource limits and exec a trajectory normalizer",
		Hidden: true,
		Args:   cobra.MinimumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if asMiB <= 0 {
				return fmt.Errorf("--as-mib must be positive, got %d", asMiB)
			}
			if cpuSeconds <= 0 {
				return fmt.Errorf("--cpu-seconds must be positive, got %d", cpuSeconds)
			}
			if len(args) == 0 || args[0] == "" {
				return errors.New("command is required after --")
			}
			if !filepath.IsAbs(args[0]) {
				return fmt.Errorf("command must be an absolute path, got %q", args[0])
			}
			return execWithNormalizeLimits(args, asMiB, cpuSeconds)
		},
	}

	cmd.Flags().IntVar(&asMiB, "as-mib", trajectory.DefaultNormalizeMaxAddressSpaceMiB, "Virtual address space limit in MiB (RLIMIT_AS)")
	cmd.Flags().IntVar(&cpuSeconds, "cpu-seconds", trajectory.DefaultNormalizeCPUSeconds, "CPU time limit in seconds (RLIMIT_CPU)")

	return cmd
}

func init() {
	internalCmd.AddCommand(newInternalNormalizeExecCmd())
}
