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

	"github.com/GoogleCloudPlatform/scion/pkg/githubfetch"
)

// isGHAuthoritativeDenial reports whether err is an authoritative GitHub
// denial (404 not_found, 401 auth_rejected, or non-rate-limit 403 forbidden).
// These trigger cache deletion for the same (host, owner, repo, scope) and a
// 1-minute negative memo, and must never fall back to serving stale entries.
func isGHAuthoritativeDenial(err error) bool {
	var ghe *githubfetch.Error
	if errors.As(err, &ghe) {
		switch ghe.Kind {
		case githubfetch.KindNotFound, githubfetch.KindAuthRejected, githubfetch.KindForbidden:
			return true
		}
	}
	return false
}

// isGHTransientFailure reports whether err is a transient GitHub or transport
// failure (rate_limited, timeout, unreachable, upstream) for which a stale
// cached entry may still be served for a credentialed scope.
func isGHTransientFailure(err error) bool {
	var ghe *githubfetch.Error
	if errors.As(err, &ghe) {
		switch ghe.Kind {
		case githubfetch.KindRateLimited, githubfetch.KindTimeout, githubfetch.KindUnreachable, githubfetch.KindUpstream:
			return true
		}
	}
	return false
}

// rememberGHDenial remembers err for cacheKey for agent.FailureMemoTTL
// (see Server.ghFailures) when it is an authoritative denial (not_found,
// auth_rejected, or forbidden). Transient failures (rate limits, 5xx, network
// errors) are never remembered.
func (s *Server) rememberGHDenial(cacheKey string, err error) {
	if isGHAuthoritativeDenial(err) {
		s.ghFailures.Record(cacheKey, err)
	}
}
