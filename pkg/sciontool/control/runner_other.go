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

//go:build !unix

package control

import (
	"context"
	"errors"
	"fmt"
	"io"
)

// LocalRunner runs the tmuxdeliver steps as local processes. It is
// unix-only; on other platforms Run always fails.
type LocalRunner struct {
	UID, GID             int
	Username             string
	RequirePrivilegeDrop bool
}

// Run fails closed on this platform.
func (LocalRunner) Run(context.Context, []string, io.Reader) (string, error) {
	return "", fmt.Errorf("control: local tmux runner: %w on this platform", errors.ErrUnsupported)
}
