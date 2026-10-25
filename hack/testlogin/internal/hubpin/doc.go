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

// Package hubpin holds the tests that pin the testlogin tool to the real hub
// code: the signing-key derivation and challenge format must be accepted by
// pkg/hub, and the whole mint and cleanup flow must pass against an
// in-process hub. It is kept separate so that only these tests link
// pkg/hub; the tool itself and its other tests do not.
package hubpin
