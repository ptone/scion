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

package keyring

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"
)

// LegacyID is the generation ID of material imported from (or adapted
// from) the single legacy server secret.
const LegacyID = "legacy"

// ErrIssuanceClosed is returned by SigningRing.Active at or after the
// active entry's sign_until: the instance must stop issuing with it.
var ErrIssuanceClosed = errors.New("keyring: the active signing generation has reached its sign_until; issuance is closed")

// Emit selects the wire formats signing consumers write.
type Emit string

const (
	// EmitLegacy writes every current (ID-less) wire format.
	EmitLegacy Emit = "legacy"
	// EmitVersioned writes the versioned (key-ID-carrying) formats.
	EmitVersioned Emit = "versioned"
)

// WriteFormat selects the ciphertext envelope encryption consumers write.
type WriteFormat string

const (
	// WriteV1 is the current enc:v1: envelope.
	WriteV1 WriteFormat = "v1"
	// WriteV2 is the key-ID-carrying enc:v2: envelope (design §5).
	WriteV2 WriteFormat = "v2"
)

// Source says where a Keyring came from.
type Source string

const (
	// SourceLegacy is the adapter over the single legacy server secret.
	SourceLegacy Source = "legacy"
	// SourceFile is a validated version-1 keyring file.
	SourceFile Source = "file"
)

// Purpose is one named derivation of a purpose key from a generation's
// signing secret. Consumers define their purposes with their existing
// derivation and label, so the derived bytes never change; the ring itself
// knows no labels.
type Purpose struct {
	name   string
	derive func(secret []byte) []byte
}

// NewPurpose returns a purpose. derive must be deterministic and must not
// retain or modify its argument.
func NewPurpose(name string, derive func(secret []byte) []byte) Purpose {
	if name == "" || derive == nil {
		panic("keyring: NewPurpose needs a name and a derivation")
	}
	return Purpose{name: name, derive: derive}
}

// Name is the purpose's name (never key material).
func (p Purpose) Name() string { return p.name }

// SHA256LabelPurpose is the purpose deriving sha256(label + ":" + secret),
// the derivation of the web session cookie keys.
func SHA256LabelPurpose(label string) Purpose {
	return NewPurpose(label, func(secret []byte) []byte {
		sum := sha256.Sum256(append([]byte(label+":"), secret...))
		return sum[:]
	})
}

// redacted is what every key-holding type prints.
const redacted = "[redacted]"

// SigningKey is one generation's derived key for one purpose, with the
// generation's deadlines. Key is a copy the caller owns.
type SigningKey struct {
	ID          string
	Key         []byte
	SignUntil   *time.Time // nil: no issuance bound
	VerifyUntil *time.Time // nil: no verification bound
}

// CanSign reports whether now is before the issuance bound.
func (k SigningKey) CanSign(now time.Time) bool {
	return k.SignUntil == nil || now.Before(*k.SignUntil)
}

// CanVerify reports whether now is before the verification bound.
func (k SigningKey) CanVerify(now time.Time) bool {
	return k.VerifyUntil == nil || now.Before(*k.VerifyUntil)
}

// String prints the ID only.
func (k SigningKey) String() string { return fmt.Sprintf("SigningKey{ID:%s Key:%s}", k.ID, redacted) }

// GoString prints the ID only.
func (k SigningKey) GoString() string { return k.String() }

// LogValue logs the ID only.
func (k SigningKey) LogValue() slog.Value {
	return slog.GroupValue(slog.String("id", k.ID), slog.String("key", redacted))
}

// Format prints the ID only for every verb.
func (k SigningKey) Format(f fmt.State, _ rune) { _, _ = f.Write([]byte(k.String())) }

// EncryptionKey is one raw 32-byte encryption key. Key is a copy the
// caller owns.
type EncryptionKey struct {
	ID  string
	Key []byte
}

// String prints the ID only.
func (k EncryptionKey) String() string {
	return fmt.Sprintf("EncryptionKey{ID:%s Key:%s}", k.ID, redacted)
}

// GoString prints the ID only.
func (k EncryptionKey) GoString() string { return k.String() }

// LogValue logs the ID only.
func (k EncryptionKey) LogValue() slog.Value {
	return slog.GroupValue(slog.String("id", k.ID), slog.String("key", redacted))
}

// Format prints the ID only for every verb.
func (k EncryptionKey) Format(f fmt.State, _ rune) { _, _ = f.Write([]byte(k.String())) }

type signingEntry struct {
	id          string
	secret      []byte
	signUntil   *time.Time
	verifyUntil *time.Time
}

// SigningRing is the immutable set of signing generations (at most three:
// active, previous, staged successor).
type SigningRing struct {
	active      string
	emit        Emit
	entries     []signingEntry // file order
	legacyReads []string       // file order
}

func (r *SigningRing) entry(id string) (signingEntry, bool) {
	for _, e := range r.entries {
		if e.id == id {
			return e, true
		}
	}
	return signingEntry{}, false
}

func (e signingEntry) key(p Purpose) SigningKey {
	return SigningKey{
		ID:          e.id,
		Key:         p.derive(slices.Clone(e.secret)),
		SignUntil:   cloneTime(e.signUntil),
		VerifyUntil: cloneTime(e.verifyUntil),
	}
}

// ActiveID is the active generation's ID.
func (r *SigningRing) ActiveID() string { return r.active }

// Emit is the configured signing wire-format selection.
func (r *SigningRing) Emit() Emit { return r.emit }

// IDs returns every generation ID, active first, then file order.
func (r *SigningRing) IDs() []string {
	out := []string{r.active}
	for _, e := range r.entries {
		if e.id != r.active {
			out = append(out, e.id)
		}
	}
	return out
}

// Active returns the active generation's key for p. At or after the active
// entry's sign_until it returns ErrIssuanceClosed: no issuance path may use
// that key any more (design §2/§3).
func (r *SigningRing) Active(p Purpose, now time.Time) (SigningKey, error) {
	e, _ := r.entry(r.active)
	k := e.key(p)
	if !k.CanSign(now) {
		return SigningKey{}, ErrIssuanceClosed
	}
	return k, nil
}

// Candidates returns the verification keys for a value carrying key ID
// kid. A non-empty kid selects that generation only, while now is before
// its verify_until; an unknown kid selects nothing and never falls back to
// other keys. An empty kid (an ID-less legacy value) selects the
// configured legacy_read_ids, in file order, each while now is before its
// verify_until.
func (r *SigningRing) Candidates(p Purpose, kid string, now time.Time) []SigningKey {
	var ids []string
	if kid == "" {
		ids = r.legacyReads
	} else {
		ids = []string{kid}
	}
	var out []SigningKey
	for _, id := range ids {
		e, ok := r.entry(id)
		if !ok {
			continue
		}
		if k := e.key(p); k.CanVerify(now) {
			out = append(out, k)
		}
	}
	return out
}

// Generations returns every generation's key for p, active first, then
// file order, with deadlines and no time filter. It is for consumers that
// build one verifier per generation at startup and enforce the deadlines
// on every use (the session cookie codecs).
func (r *SigningRing) Generations(p Purpose) []SigningKey {
	out := make([]SigningKey, 0, len(r.entries))
	for _, id := range r.IDs() {
		e, _ := r.entry(id)
		out = append(out, e.key(p))
	}
	return out
}

// LegacySecret returns the raw secret of the "legacy" signing entry, for
// the Phase 1 wiring of consumers that still use the unchanged legacy
// material. Later phases replace its callers; new code must use Active and
// Candidates.
func (r *SigningRing) LegacySecret() (string, bool) {
	e, ok := r.entry(LegacyID)
	if !ok {
		return "", false
	}
	return string(e.secret), true
}

type encryptionEntry struct {
	id  string
	key []byte
}

// EncryptionRing is the immutable set of raw encryption keys (at most
// three).
type EncryptionRing struct {
	active      string
	writeFormat WriteFormat
	entries     []encryptionEntry
	legacyReads []string
}

func (r *EncryptionRing) entry(id string) (encryptionEntry, bool) {
	for _, e := range r.entries {
		if e.id == id {
			return e, true
		}
	}
	return encryptionEntry{}, false
}

// ActiveID is the active (writer) key's ID.
func (r *EncryptionRing) ActiveID() string { return r.active }

// WriteFormat is the configured ciphertext envelope for writes.
func (r *EncryptionRing) WriteFormat() WriteFormat { return r.writeFormat }

// IDs returns every key ID, active first, then file order.
func (r *EncryptionRing) IDs() []string {
	out := []string{r.active}
	for _, e := range r.entries {
		if e.id != r.active {
			out = append(out, e.id)
		}
	}
	return out
}

// Active returns the writer key.
func (r *EncryptionRing) Active() EncryptionKey {
	e, _ := r.entry(r.active)
	return EncryptionKey{ID: e.id, Key: slices.Clone(e.key)}
}

// Candidates returns the decryption keys for a value carrying key ID kid:
// that key only for a non-empty kid (none if unknown, never a fallback),
// or the legacy_v1_read_ids for an ID-less enc:v1: value.
func (r *EncryptionRing) Candidates(kid string) []EncryptionKey {
	var ids []string
	if kid == "" {
		ids = r.legacyReads
	} else {
		ids = []string{kid}
	}
	var out []EncryptionKey
	for _, id := range ids {
		if e, ok := r.entry(id); ok {
			out = append(out, EncryptionKey{ID: e.id, Key: slices.Clone(e.key)})
		}
	}
	return out
}

// Keyring is the validated, immutable server key material.
type Keyring struct {
	source     Source
	revision   string
	signing    *SigningRing
	encryption *EncryptionRing
}

// Source says whether the keyring came from a file or the legacy adapter.
func (k *Keyring) Source() Source { return k.source }

// Revision is the operator's delivery revision ("" for the legacy
// adapter). It is not secret.
func (k *Keyring) Revision() string { return k.revision }

// Signing is the signing ring.
func (k *Keyring) Signing() *SigningRing { return k.signing }

// Encryption is the encryption ring.
func (k *Keyring) Encryption() *EncryptionRing { return k.encryption }

// String prints non-secret identification only.
func (k *Keyring) String() string {
	return fmt.Sprintf("Keyring{source:%s revision:%q signing:%v encryption:%v}",
		k.source, k.revision, k.signing.IDs(), k.encryption.IDs())
}

// DeriveLegacyEncryptionKey is the legacy local secret encryption key
// derivation, sha256("scion-hub-local-secret-encryption:" + secret). It is
// byte-identical to pkg/secret.DeriveLocalEncryptionKey (a cross-package
// test pins the two together); it lives here so this package stays free of
// the store dependencies of pkg/secret.
func DeriveLegacyEncryptionKey(secret string) []byte {
	sum := sha256.Sum256([]byte("scion-hub-local-secret-encryption:" + secret))
	return sum[:]
}

// Legacy is the adapter over the single legacy server secret: signing
// generation "legacy" holding the exact secret bytes (no deadlines, the
// only legacy reader, emit legacy) and encryption key "legacy" holding
// DeriveLegacyEncryptionKey(secret) (write v1, the only v1 reader).
func Legacy(secret string) *Keyring {
	return &Keyring{
		source: SourceLegacy,
		signing: &SigningRing{
			active:      LegacyID,
			emit:        EmitLegacy,
			entries:     []signingEntry{{id: LegacyID, secret: []byte(secret)}},
			legacyReads: []string{LegacyID},
		},
		encryption: &EncryptionRing{
			active:      LegacyID,
			writeFormat: WriteV1,
			entries:     []encryptionEntry{{id: LegacyID, key: DeriveLegacyEncryptionKey(secret)}},
			legacyReads: []string{LegacyID},
		},
	}
}

func cloneTime(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	c := *t
	return &c
}
