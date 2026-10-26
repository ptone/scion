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

// Authorization checks after the request context ended (ptone/scion#6012).
//
// When a client gives up, the request context is cancelled (or its deadline
// passes) while a handler may still be checking permissions or agent
// standing item by item. Each such check denies, as every failed check
// does, but it is not a hub fault: logging it as a WARN per item turns one
// abandoned list into hundreds of lines. Instead each one is logged at
// DEBUG and counted on a request-scoped note, and the request emits one
// INFO summary line when its handler returns.

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
)

// authzStoppedNote counts the authorization checks of one request that
// were refused because the request context had ended. It is installed by
// serveAfterAuth and read once when the handler returns.
type authzStoppedNote struct {
	mu        sync.Mutex
	count     int
	firstSite string
	class     string
	route     string
}

type authzStoppedNoteKey struct{}

// withAuthzStoppedNote installs a fresh note unless one is present. It
// returns the note only when it installed it, so only the outermost
// installer emits; otherwise it returns ctx unchanged and a nil note.
func withAuthzStoppedNote(ctx context.Context) (context.Context, *authzStoppedNote) {
	if _, ok := ctx.Value(authzStoppedNoteKey{}).(*authzStoppedNote); ok {
		return ctx, nil
	}
	n := &authzStoppedNote{}
	return context.WithValue(ctx, authzStoppedNoteKey{}, n), n
}

// requestDoneClass returns "canceled" or "deadline" when ctx has ended,
// and "" while it is live.
func requestDoneClass(ctx context.Context) string {
	switch err := ctx.Err(); {
	case err == nil:
		return ""
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline"
	default:
		return "canceled"
	}
}

// authzStoppedByRequestEnd reports whether err is the request context's
// own end: ctx is done and err is a cancellation or deadline. A
// cancellation or deadline from anywhere else, while ctx is live, is an
// ordinary fault.
func authzStoppedByRequestEnd(ctx context.Context, err error) bool {
	if err == nil || ctx.Err() == nil {
		return false
	}
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// noteAuthzStopped records one check at site that stopped because the
// request context ended, and logs it at DEBUG. The caller still denies.
func noteAuthzStopped(ctx context.Context, logger *slog.Logger, site string, err error, attrs ...any) {
	class := requestDoneClass(ctx)
	if n, ok := ctx.Value(authzStoppedNoteKey{}).(*authzStoppedNote); ok {
		n.mu.Lock()
		if n.count == 0 {
			n.firstSite = site
			n.class = class
			n.route = routeFromContext(ctx)
		}
		n.count++
		n.mu.Unlock()
	}
	if logger == nil {
		logger = slog.Default()
	}
	logger.Debug("authorization check stopped after the request context ended",
		append([]any{"site", site, "class", class, "error", err}, attrs...)...)
}

// emit writes the request's single summary line, only when at least one
// check was stopped.
func (n *authzStoppedNote) emit(r *http.Request) {
	if n == nil {
		return
	}
	n.mu.Lock()
	count, site, class, route := n.count, n.firstSite, n.class, n.route
	n.mu.Unlock()
	if count == 0 {
		return
	}
	attrs := []any{"count", count, "first_site", site, "class", class}
	if r != nil {
		attrs = append(attrs, "method", r.Method)
	}
	if route != "" {
		attrs = append(attrs, "route", route)
	}
	slog.Info("authorization checks stopped after the request context ended", attrs...)
}
