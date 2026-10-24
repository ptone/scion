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

package bridge

import (
	"errors"
	"io"
	"net/http"
	"net/url"

	"github.com/GoogleCloudPlatform/scion/pkg/util/urlorigin"
)

// hubOrigin is the origin of a hub endpoint. Origins are compared with
// urlorigin, shared with pkg/apiclient: a host with a non-ASCII byte has no
// origin, and non-canonical spellings compare as different origins.
type hubOrigin struct {
	origin urlorigin.Origin
}

// parseHubOrigin returns the origin of hubEndpoint. The errors never include
// the endpoint text, which may carry user information: a url.Parse error
// quotes its input, and an endpoint without a host may hold user information
// outside u.User. A hub with a non-ASCII host name is configured by its
// punycode form, which is what net/http dials.
func parseHubOrigin(hubEndpoint string) (hubOrigin, error) {
	u, err := url.Parse(hubEndpoint)
	if err != nil {
		return hubOrigin{}, errors.New("hub endpoint is not a valid URL")
	}
	if u.Host == "" {
		return hubOrigin{}, errors.New("hub endpoint has no host")
	}
	o, ok := urlorigin.Of(u)
	if !ok {
		return hubOrigin{}, errors.New("hub endpoint host is empty or not ASCII")
	}
	return hubOrigin{origin: o}, nil
}

// contains reports whether u has the origin's scheme, host and port. The zero
// hubOrigin contains no URL, and no origin contains a URL whose host has a
// non-ASCII byte.
func (o hubOrigin) contains(u *url.URL) bool {
	return urlorigin.Same(u, o.origin)
}

// errHubOtherOrigin is returned by hubOriginTransport for a request whose
// origin is not the configured hub's. Its text never includes the request
// URL.
var errHubOtherOrigin = errors.New("hub client: request outside the configured hub origin refused")

// hubOriginTransport is the outermost RoundTripper of an HTTP client that
// talks only to the configured hub. It passes a request to base only when
// the request's origin is the hub's. Any other request, such as the target
// of a redirect the hub answers with, is refused with errHubOtherOrigin
// before base (and any credential base adds) sees it, and its body is
// closed. Redirects within the hub origin are followed as usual. The
// response from base goes through checkHubResponse: a transport error and a
// response body read error are returned with fixed text, and a redirect with
// an unparsable Location is refused with errHubRedirectLocation.
type hubOriginTransport struct {
	base   http.RoundTripper // nil: http.DefaultTransport at request time
	origin hubOrigin
}

// newHubOriginTransport returns a hubOriginTransport for hubEndpoint. When
// hubEndpoint has no parsable origin, the transport refuses every request.
func newHubOriginTransport(base http.RoundTripper, hubEndpoint string) *hubOriginTransport {
	origin, err := parseHubOrigin(hubEndpoint)
	if err != nil {
		origin = hubOrigin{}
	}
	return &hubOriginTransport{base: base, origin: origin}
}

func (t *hubOriginTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if !t.origin.contains(req.URL) {
		if req.Body != nil {
			_ = req.Body.Close()
		}
		return nil, errHubOtherOrigin
	}
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	resp, err := base.RoundTrip(req)
	return checkHubResponse(req, resp, err)
}

// CloseIdleConnections closes the idle connections of the base transport.
func (t *hubOriginTransport) CloseIdleConnections() {
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	if c, ok := base.(interface{ CloseIdleConnections() }); ok {
		c.CloseIdleConnections()
	}
}

// errHubRedirectLocation is returned for a hub redirect whose Location
// header does not parse. Its text never includes the Location value.
var errHubRedirectLocation = errors.New("hub redirect Location could not be parsed")

// hubTransportError wraps an error the base transport returned for a hub
// request, or a hub response body read returned. Its text is fixed: the
// inner text can quote what the hub or the network sent (a malformed
// response header or trailer line, a Location with user information) or
// name the hub host and port (a dial or TLS failure). The
// inner error stays reachable through errors.Is and errors.As, and Timeout
// reports whether it is a timeout, so url.Error.Timeout keeps working.
type hubTransportError struct {
	err error
}

func (e *hubTransportError) Error() string { return "hub request failed in transport" }

func (e *hubTransportError) Unwrap() error { return e.err }

// Timeout reports whether the inner error is a timeout.
func (e *hubTransportError) Timeout() bool {
	var t interface{ Timeout() bool }
	return errors.As(e.err, &t) && t.Timeout()
}

// checkHubResponse passes on resp and err from the base transport for hub
// request req, with these changes:
//   - A non-nil err that matches a refusal sentinel (errHubOtherOrigin,
//     errFederationOtherOrigin, errHubRedirectLocation) is returned as that
//     sentinel itself, so no text around it passes through. Any other
//     non-nil err is returned as a hubTransportError.
//   - For a redirect that http.Client would follow whose Location is not
//     empty and does not parse against the request URL, the body is closed
//     and errHubRedirectLocation is returned, so http.Client never builds
//     an error quoting the Location value.
//   - Any other response has its body wrapped in hubBody, so a body read
//     error other than io.EOF is returned as a hubTransportError.
func checkHubResponse(req *http.Request, resp *http.Response, err error) (*http.Response, error) {
	if err != nil {
		for _, sentinel := range []error{errHubOtherOrigin, errFederationOtherOrigin, errHubRedirectLocation} {
			if errors.Is(err, sentinel) {
				return nil, sentinel
			}
		}
		return resp, &hubTransportError{err: err}
	}
	if resp == nil {
		return resp, nil
	}
	switch resp.StatusCode {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		if loc := resp.Header.Get("Location"); loc != "" {
			if _, perr := req.URL.Parse(loc); perr != nil {
				_ = resp.Body.Close()
				return nil, errHubRedirectLocation
			}
		}
	}
	if resp.Body != nil {
		resp.Body = &hubBody{rc: resp.Body}
	}
	return resp, nil
}

// hubBody is the body of a hub response. Read returns io.EOF as it is, so
// io.ReadAll and json.Decoder end cleanly, and returns any other error as a
// hubTransportError: a body read error can quote what the hub sent, such as
// a malformed chunked trailer line. Close passes through.
type hubBody struct {
	rc io.ReadCloser
}

func (b *hubBody) Read(p []byte) (int, error) {
	n, err := b.rc.Read(p)
	if err != nil && err != io.EOF {
		err = &hubTransportError{err: err}
	}
	return n, err
}

func (b *hubBody) Close() error { return b.rc.Close() }

// errWithoutURL returns the error a *url.Error wraps, whose own text quotes
// the request URL, or err when it is not one. errors.Is still matches the
// wrapped error, such as errHubOtherOrigin, errHubRedirectLocation or the
// error inside a hubTransportError.
func errWithoutURL(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return urlErr.Err
	}
	return err
}
