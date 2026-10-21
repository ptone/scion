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

// Package keyring holds the Hub server's versioned key material: an
// immutable SigningRing (session cookies, and in later phases JWTs, signed
// URLs, cursors, admission tokens and the relay-peer fallback) and an
// immutable EncryptionRing (locally stored ciphertext), loaded once at
// startup from a version-1 keyring file (server.keyring_file) or built from
// the single legacy server secret (Legacy).
//
// Key IDs name generations; they never change derived key bytes. Each
// consumer defines a Purpose with its existing derivation, so a ring built
// from the legacy secret derives exactly the bytes the pre-ring code did.
//
// The package has no dependency on the hub, config or cmd packages, and no
// value it returns formats or logs key material.
//
// Design: ptone/scion#1530 ("Safe rotation of the Hub server secret"),
// §1-§3. This release uses the signing ring for web session cookies only
// (Phase 1); every other purpose still uses the ring's "legacy" entry.
package keyring
