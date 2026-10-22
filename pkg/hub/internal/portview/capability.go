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

// Package portview holds the request-independent parts of the port view
// route (/api/v1/portview/<capability>/<path>, experiment hub.port-view):
// the capability format, the navigation predicate and the preflight
// header rules. The hub's handler (pkg/hub/port_view.go) uses them.
//
// A capability names one user's access to one exposed port of one agent
// until an expiry:
//
//	v1.<agentID>.<port>.<userID>.<exp>.<sig>
//
// sig is base64url (no padding) of HMAC-SHA256 over "port-view\n" +
// agentID + "\n" + port + "\n" + userID + "\n" + exp, keyed with the
// hub's port view key.
package portview

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Route is the path prefix of the port view route.
const Route = "/api/v1/portview/"

const (
	// version is the first field of every capability.
	version = "v1"

	// sigDomain starts every signed message. The port view key is a key of
	// its own (not the artifact view key or any other hub key), and the
	// message carries this domain as well, so that a capability can never
	// be accepted by another route, nor another route's signature here.
	sigDomain = "port-view"

	// MaxSkew bounds how far beyond the configured lifetime an expiry may
	// lie. The hub never mints one; anything further out is refused.
	MaxSkew = time.Minute

	// fields is the number of dot-separated fields of a capability.
	fields = 6
)

// ErrInvalid is the only error Parse returns. Every way a capability can
// fail (shape, encoding, signature, expiry) is reported the same way.
var ErrInvalid = errors.New("invalid port view capability")

// sigEncoding is base64url without padding, refusing non-canonical
// trailing bits, so each signature has exactly one accepted spelling.
var sigEncoding = base64.RawURLEncoding.Strict()

// Grant is what a valid capability names.
type Grant struct {
	AgentID string
	Port    int
	UserID  string
	Expires time.Time
}

func mac(key []byte, agentID, port, userID, exp string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(sigDomain + "\n" + agentID + "\n" + port + "\n" + userID + "\n" + exp))
	return m.Sum(nil)
}

// Mint returns the capability for userID's access to port of agentID
// until exp (whole seconds). The IDs must be canonical (see Parse).
func Mint(key []byte, agentID string, port int, userID string, exp time.Time) string {
	p := strconv.Itoa(port)
	e := strconv.FormatInt(exp.Unix(), 10)
	return version + "." + agentID + "." + p + "." + userID + "." + e + "." +
		sigEncoding.EncodeToString(mac(key, agentID, p, userID, e))
}

// Parse checks capability against key at now and returns what it names.
// It refuses, with ErrInvalid: an empty key; anything but six fields
// starting with "v1"; an agent or user ID that is not a canonical
// (lower-case, hyphenated) UUID; a port or expiry that is not a decimal
// without sign or leading zero, or a port outside 1..65535; a signature
// that is not the canonical encoding or does not match; an expiry that is
// not after now, or that lies more than ttl+MaxSkew after now.
func Parse(key []byte, capability string, now time.Time, ttl time.Duration) (Grant, error) {
	if len(key) == 0 {
		return Grant{}, ErrInvalid
	}
	parts := strings.Split(capability, ".")
	if len(parts) != fields || parts[0] != version {
		return Grant{}, ErrInvalid
	}
	agentID, portStr, userID, expStr, sigStr := parts[1], parts[2], parts[3], parts[4], parts[5]
	if !canonicalUUID(agentID) || !canonicalUUID(userID) {
		return Grant{}, ErrInvalid
	}
	port, ok := canonicalDecimal(portStr)
	if !ok || port < 1 || port > 65535 {
		return Grant{}, ErrInvalid
	}
	exp, ok := canonicalDecimal(expStr)
	if !ok || exp < 1 {
		return Grant{}, ErrInvalid
	}
	sig, err := sigEncoding.DecodeString(sigStr)
	if err != nil || !hmac.Equal(sig, mac(key, agentID, portStr, userID, expStr)) {
		return Grant{}, ErrInvalid
	}
	t := time.Unix(exp, 0)
	if !now.Before(t) || t.After(now.Add(ttl+MaxSkew)) {
		return Grant{}, ErrInvalid
	}
	return Grant{AgentID: agentID, Port: int(port), UserID: userID, Expires: t}, nil
}

// Target returns the agent ID and port fields of capability without
// checking its signature or expiry. The values are not trusted: they are
// only used to send a page navigation back to the agent port's stable
// link, which checks the caller itself.
func Target(capability string) (agentID string, port int, ok bool) {
	parts := strings.Split(capability, ".")
	if len(parts) != fields || parts[0] != version || !canonicalUUID(parts[1]) {
		return "", 0, false
	}
	p, ok := canonicalDecimal(parts[2])
	if !ok || p < 1 || p > 65535 {
		return "", 0, false
	}
	return parts[1], int(p), true
}

// CanonicalID reports whether s may be used as an agent or user ID in a
// capability: a UUID in its canonical (lower-case, hyphenated) spelling.
func CanonicalID(s string) bool { return canonicalUUID(s) }

// canonicalUUID reports whether s is a UUID in its canonical spelling.
func canonicalUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	u, err := uuid.Parse(s)
	return err == nil && u.String() == s
}

// canonicalDecimal parses s as a non-negative decimal of at most 18
// digits with no sign and no leading zero ("0" itself is accepted).
func canonicalDecimal(s string) (int64, bool) {
	if s == "" || len(s) > 18 || (len(s) > 1 && s[0] == '0') {
		return 0, false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	return n, err == nil
}

// IsNavigation reports whether r looks like a page navigation:
// Sec-Fetch-Mode "navigate", or, when the browser sent no Sec-Fetch-Mode,
// an Accept header that includes text/html. The caller chooses these
// headers, so the answer only selects a product behaviour (which link the
// browser is sent to); it is never an access decision.
func IsNavigation(r *http.Request) bool {
	if mode := r.Header.Get("Sec-Fetch-Mode"); mode != "" {
		return mode == "navigate"
	}
	return strings.Contains(r.Header.Get("Accept"), "text/html")
}

// preflightMethods are the methods a preflight may be answered for.
var preflightMethods = map[string]bool{
	http.MethodGet: true, http.MethodHead: true, http.MethodPost: true,
	http.MethodPut: true, http.MethodPatch: true, http.MethodDelete: true,
}

// PreflightMethod returns the value of Access-Control-Allow-Methods for a
// preflight that asks for method: method itself when it is GET, HEAD,
// POST, PUT, PATCH or DELETE, otherwise "" (the header is not sent).
func PreflightMethod(method string) string {
	if preflightMethods[method] {
		return method
	}
	return ""
}

// PreflightHeaders returns the value of Access-Control-Allow-Headers for a
// preflight whose Access-Control-Request-Headers is requested: the
// requested names, lower-cased, without authorization and cookie and
// without any name that is not an HTTP token. "" means the header is not
// sent.
func PreflightHeaders(requested string) string {
	var out []string
	for _, name := range strings.Split(requested, ",") {
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "" || name == "authorization" || name == "cookie" || !isToken(name) {
			continue
		}
		out = append(out, name)
	}
	return strings.Join(out, ", ")
}

// isToken reports whether s is an HTTP token (RFC 9110 section 5.6.2).
func isToken(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0:
		default:
			return false
		}
	}
	return s != ""
}
