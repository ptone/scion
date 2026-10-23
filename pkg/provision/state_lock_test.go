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


package provision

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestWithProjectStateLock(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "provision")
	ran := 0
	if err := WithProjectStateLock(context.Background(), dir, func(ctx context.Context) error {
		ran++
		if ctx.Err() != nil {
			t.Fatalf("lock context already done: %v", ctx.Err())
		}
		return nil
	}); err != nil || ran != 1 {
		t.Fatalf("err=%v ran=%d", err, ran)
	}
	// Released: a second section can take it, and fn's error is returned.
	sentinel := errors.New("boom")
	if err := WithProjectStateLock(context.Background(), dir, func(context.Context) error { return sentinel }); !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want the section's error", err)
	}
	if err := WithProjectStateLock(context.Background(), "", func(context.Context) error { return nil }); err == nil {
		t.Fatal("want an error for an empty state dir")
	}
}
