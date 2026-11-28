//go:build !unix

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

package trajectory

import (
	"context"
	"errors"
	"fmt"
)

// SandboxExecutor is unsupported on non-Unix platforms.
type SandboxExecutor struct{}

// NewSandboxExecutor fails closed on non-Unix platforms.
func NewSandboxExecutor(_ SandboxExecutorConfig) (*SandboxExecutor, error) {
	return nil, fmt.Errorf("trajectory normalize executor: %w on this platform", errors.ErrUnsupported)
}

// Execute fails closed on non-Unix platforms.
func (*SandboxExecutor) Execute(_ context.Context, _ NormalizeExecRequest) (*NormalizeExecResult, error) {
	return nil, fmt.Errorf("trajectory normalize executor: %w on this platform", errors.ErrUnsupported)
}
