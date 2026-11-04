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

package challenge

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

func randomSecret(t *testing.T) []byte {
	t.Helper()
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return b
}

func TestDeriveUserSigningKeyFormat(t *testing.T) {
	secret := randomSecret(t)
	want := sha256.Sum256([]byte("scion-hub-signing-key:user_signing_key:" + string(secret)))
	if got := DeriveUserSigningKey(secret); !bytes.Equal(got, want[:]) {
		t.Fatal("derived key does not match the documented format")
	}
	if bytes.Equal(DeriveUserSigningKey(secret), DeriveUserSigningKey(randomSecret(t))) {
		t.Fatal("different secrets produced the same key")
	}
}

func TestMintClaims(t *testing.T) {
	key := DeriveUserSigningKey(randomSecret(t))
	now := time.Now()
	tok, err := Mint(key, "subject-x", now)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := jwt.ParseSigned(tok, []jose.SignatureAlgorithm{jose.HS256})
	if err != nil {
		t.Fatal(err)
	}
	var c jwt.Claims
	if err := parsed.Claims(key, &c); err != nil {
		t.Fatalf("verify with the same key: %v", err)
	}
	if err := c.Validate(jwt.Expected{Issuer: Issuer, AnyAudience: jwt.Audience{Audience}, Time: now}); err != nil {
		t.Fatalf("claims: %v", err)
	}
	if c.Subject != "subject-x" || c.ID == "" {
		t.Fatalf("unexpected subject or empty id: %+v", c)
	}
	if got := c.Expiry.Time().Sub(c.IssuedAt.Time()); got != Lifetime {
		t.Fatalf("lifetime = %v, want %v", got, Lifetime)
	}
	if err := parsed.Claims(DeriveUserSigningKey(randomSecret(t)), &c); err == nil {
		t.Fatal("token verified with an unrelated key")
	}
}

func TestMintRejectsEmptyKey(t *testing.T) {
	if _, err := Mint(nil, "s", time.Now()); err == nil {
		t.Fatal("expected an error for an empty key")
	}
}

func TestZero(t *testing.T) {
	b := []byte{1, 2, 3}
	Zero(b)
	if !bytes.Equal(b, []byte{0, 0, 0}) {
		t.Fatal("not zeroed")
	}
}
