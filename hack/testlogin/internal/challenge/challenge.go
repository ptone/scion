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

// Package challenge derives the hub's user signing key from its shared
// session secret and mints the short-lived challenge token accepted by the
// hub's test-login endpoint.
//
// Both pieces mirror unexported or method-bound code in pkg/hub
// (deriveSharedSigningKey in server.go and UserTokenService.GenerateTestLoginToken
// in usertoken.go). They are duplicated rather than imported so that the
// testlogin tool does not link pkg/hub. The hubpin subpackage pins this copy
// to the hub: a challenge minted here must validate through
// hub.UserTokenService.ValidateTestLoginToken with the hub's derivation.
package challenge

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

const (
	// UserSigningKeyName is the logical key name the hub uses for user tokens.
	UserSigningKeyName = "user_signing_key"
	// Issuer matches hub.UserTokenIssuer.
	Issuer = "scion-hub"
	// Audience matches hub.TestLoginAudience. It differs from the access-token
	// audience, so a challenge cannot be used as an access token.
	Audience = "scion-test-login"
	// Lifetime matches hub.DefaultTestLoginTokenDuration.
	Lifetime = 5 * time.Minute
)

// DeriveUserSigningKey returns the user signing key the hub derives from its
// shared session secret. It must stay byte-for-byte identical to
// deriveSharedSigningKey(secret, "user_signing_key") in pkg/hub/server.go.
// The caller owns the returned slice and should zero it after use.
func DeriveUserSigningKey(secret []byte) []byte {
	h := sha256.New()
	h.Write([]byte("scion-hub-signing-key:" + UserSigningKeyName + ":"))
	h.Write(secret)
	return h.Sum(nil)
}

// Mint returns a test-login challenge JWT signed with key, valid for
// Lifetime from now. The claims match hub.UserTokenService.GenerateTestLoginToken.
func Mint(key []byte, subject string, now time.Time) (string, error) {
	if len(key) == 0 {
		return "", errors.New("empty signing key")
	}
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.HS256, Key: key},
		(&jose.SignerOptions{}).WithType("JWT"),
	)
	if err != nil {
		return "", fmt.Errorf("create signer: %w", err)
	}
	jti := make([]byte, 16)
	if _, err := rand.Read(jti); err != nil {
		return "", fmt.Errorf("generate token id: %w", err)
	}
	claims := jwt.Claims{
		Issuer:    Issuer,
		Subject:   subject,
		Audience:  jwt.Audience{Audience},
		IssuedAt:  jwt.NewNumericDate(now),
		Expiry:    jwt.NewNumericDate(now.Add(Lifetime)),
		NotBefore: jwt.NewNumericDate(now),
		ID:        hex.EncodeToString(jti),
	}
	token, err := jwt.Signed(signer).Claims(claims).Serialize()
	if err != nil {
		return "", fmt.Errorf("sign challenge: %w", err)
	}
	return token, nil
}

// Zero overwrites b with zeros. It is a best-effort measure to shorten the
// time secret material stays in memory.
func Zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
