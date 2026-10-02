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

import "context"

type launchContextKey struct{}

// WithLaunchContext returns a copy of ctx that carries launchCtx as the
// launch context for a Run call made with it.
//
// The launch context is the context a runtime uses for the part of Run that
// happens after the container or pod already exists (for Kubernetes: the
// pod readiness wait, the home and workspace sync, and the startup-gate
// touch). It lets a caller whose ctx is tied to a short-lived request (the
// runtime broker's synchronous create) keep that work going after the
// request ends, while still being able to end it on purpose: launchCtx
// should not be cancelled by the request, but must be cancelled when the
// launch is abandoned (a stop or delete of the agent, or a newer start for
// the same name).
//
// A Run call made without a launch context uses ctx throughout, as before.
func WithLaunchContext(ctx, launchCtx context.Context) context.Context {
	if launchCtx == nil {
		return ctx
	}
	return context.WithValue(ctx, launchContextKey{}, launchCtx)
}

// LaunchContextFrom returns the launch context attached to ctx by
// WithLaunchContext, or nil when there is none.
func LaunchContextFrom(ctx context.Context) context.Context {
	launchCtx, _ := ctx.Value(launchContextKey{}).(context.Context)
	return launchCtx
}
