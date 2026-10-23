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

package hub

import (
	"errors"
	"log/slog"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/keyring"
)

// The /readyz reasons of a keyring file whose signing ring cannot issue.
// They name no key material, ID or deadline.
const (
	// readyReasonSigningIssuanceClosed: the active generation is at or
	// past its sign_until.
	readyReasonSigningIssuanceClosed = "signing_issuance_closed"
	// readyReasonSigningNoActiveGeneration: the ring has no usable active
	// generation (unreachable through the loader; fail closed anyway).
	readyReasonSigningNoActiveGeneration = "signing_no_active_generation"
)

// SigningKeySourceKeyring is the signing-key source label of the web
// session cookie keys when a keyring file (server.keyring_file) supplies
// them (ServerConfig.WebSessionKeySource). It is deployment-wide, so not
// hub-local (signingKeySourceIsHubLocal): the test admin tier refuses it.
const SigningKeySourceKeyring = "keyring"

// keyringReleaseCheck enforces this release's limits on a keyring file
// (no versioned emit, no v2 writes, the imported legacy entries). A
// package variable only so tests can run versioned rings through New;
// production never changes it.
var keyringReleaseCheck = (*keyring.Keyring).CheckSessionPilot

// managedSigning reports whether cfg is in managed ring mode: a keyring
// file is configured, or any keyring other than the legacy adapter is set
// (the same rule /readyz applies). In managed mode the signing ring is
// authoritative for the agent and user JWT keys: missing or incomplete
// ring material refuses startup and never selects the legacy signing-key
// resolution (design §4).
func managedSigning(cfg ServerConfig) bool {
	return cfg.KeyringConfigured || (cfg.Keyring != nil && cfg.Keyring.Source() != keyring.SourceLegacy)
}

// validateKeyringConfig checks a managed-mode ServerConfig
// (ptone/scion#1530 Phases 1 and 3) before New reads any signing key: a
// configured keyring file must have produced a file keyring (a missing or
// unreadable file, or a legacy adapter in its place, never selects legacy
// mode); its signing ring must have an active generation; it must satisfy
// this release's limits (keyringReleaseCheck); SharedSigningSecret must be
// its "legacy" signing entry (every purpose but the web session cookies and
// the agent/user JWTs still uses it); and explicit agent/user JWT signing
// keys are refused, so no purpose is partly rotated. Legacy mode (no
// keyring file) is unchanged: explicit signing keys keep their precedence.
// Errors name no key material, key ID or value.
func validateKeyringConfig(cfg ServerConfig) error {
	if !managedSigning(cfg) {
		return nil
	}
	k := cfg.Keyring
	if k == nil {
		return errors.New("keyring mode: server.keyring_file is configured but no keyring file was loaded; refusing to fall back to legacy signing keys")
	}
	// The zero time never reaches a sign_until, so only a ring without a
	// usable active generation errors here.
	if _, err := k.Signing().Active(agentJWTPurpose, time.Time{}); err != nil {
		return errors.New("keyring mode: the signing ring has no active generation")
	}
	if k.Source() != keyring.SourceFile {
		return errors.New("keyring mode: server.keyring_file is configured but the keyring did not come from it; refusing to fall back to legacy signing keys")
	}
	// The purposes that do not use the ring yet still need the "legacy"
	// entry. Checked here, not only by keyringReleaseCheck, so the test
	// seam can never admit a ring without it.
	legacy, ok := k.Signing().LegacySecret()
	if !ok {
		return errors.New("keyring mode: the signing ring has no \"legacy\" entry; every purpose that does not use the ring yet still needs it")
	}
	if err := keyringReleaseCheck(k); err != nil {
		return err
	}
	if cfg.SharedSigningSecret != legacy {
		return errors.New("keyring mode: SharedSigningSecret must be the keyring's \"legacy\" signing entry")
	}
	if len(cfg.AgentTokenConfig.SigningKey) > 0 {
		return errors.New("keyring mode cannot be combined with an explicit agent token signing key (AgentTokenConfig.SigningKey)")
	}
	if len(cfg.UserTokenConfig.SigningKey) > 0 {
		return errors.New("keyring mode cannot be combined with an explicit user token signing key (UserTokenConfig.SigningKey)")
	}
	return nil
}

// keyringNow is the keyring deadline clock.
func (s *Server) keyringNow() time.Time {
	if s.config.KeyringNow != nil {
		return s.config.KeyringNow()
	}
	return time.Now()
}

// signingNotReadyReason returns the /readyz reason when a keyring file's
// signing ring cannot issue: signing_issuance_closed at or after the active
// generation's sign_until, signing_no_active_generation for a ring without
// a usable active generation (any other Active error is treated the same
// way). It returns "" when issuance works, and always without a keyring
// file or with the legacy adapter. The first not-ready result is logged once at WARN, without key
// material.
func (s *Server) signingNotReadyReason() string {
	k := s.config.Keyring
	if k == nil || k.Source() == keyring.SourceLegacy {
		// No keyring file (or the legacy adapter): nothing to check. Any
		// other keyring, including a zero one, must be able to issue.
		return ""
	}
	_, err := k.Signing().Active(webSessionHashPurpose, s.keyringNow())
	if err == nil {
		return ""
	}
	reason := readyReasonSigningNoActiveGeneration
	if errors.Is(err, keyring.ErrIssuanceClosed) {
		reason = readyReasonSigningIssuanceClosed
	}
	if s.signingNotReadyLogged.CompareAndSwap(false, true) {
		slog.Warn("Not ready: the keyring's signing ring cannot issue",
			"reason", reason, "active_id", k.Signing().ActiveID())
	}
	return reason
}

// ExperimentEnabled reports whether the named experiment is enabled now.
// It is for startup checks outside this package (the server.keyring_file
// gate in cmd); hub code uses experimentEnabled.
func (s *Server) ExperimentEnabled(name string) bool {
	return s.experimentEnabled(name)
}
