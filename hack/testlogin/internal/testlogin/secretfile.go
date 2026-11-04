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

package testlogin

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/GoogleCloudPlatform/scion/hack/testlogin/internal/challenge"
)

// defaultSecretVars lists the variables the hub reads its session secret
// from, in the order resolveSessionSecret (cmd/server_foreground.go) checks
// them after the --session-secret flag.
var defaultSecretVars = []string{"SCION_SERVER_SESSION_SECRET", "SESSION_SECRET"}

const maxSecretFileSize = 1 << 20

// readSecretFile returns the session secret from a systemd EnvironmentFile
// (KEY=VALUE lines, optional "export " prefix, optional quotes) or a unit
// drop-in (Environment= lines). If varName is set, only that variable is
// used; otherwise the hub's default variables are tried in order.
//
// The returned slice is a private copy; the caller should zero it after use.
// Errors never contain any value read from the file.
func readSecretFile(path, varName string, warn io.Writer) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open secret file: %w", err)
	}
	defer func() { _ = f.Close() }()

	st, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat secret file: %w", err)
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("secret file %s is not a regular file", path)
	}
	if st.Size() > maxSecretFileSize {
		return nil, fmt.Errorf("secret file %s is larger than %d bytes", path, maxSecretFileSize)
	}
	if st.Mode().Perm()&0o077 != 0 && warn != nil {
		_, _ = fmt.Fprintf(warn, "testlogin: warning: secret file %s is accessible to group or other (mode %04o)\n", path, st.Mode().Perm())
	}

	raw, err := io.ReadAll(io.LimitReader(f, maxSecretFileSize+1))
	if err != nil {
		return nil, fmt.Errorf("read secret file: %w", err)
	}
	defer challenge.Zero(raw)

	names := defaultSecretVars
	if varName != "" {
		names = []string{varName}
	}
	found := make(map[string][]byte, len(names))
	defer func() {
		for _, v := range found {
			challenge.Zero(v)
		}
	}()
	record := func(key, value []byte) {
		for _, n := range names {
			if string(key) == n {
				if old, ok := found[n]; ok {
					challenge.Zero(old) // later assignments win, as in systemd
				}
				found[n] = append([]byte(nil), value...)
			}
		}
	}

	for _, line := range bytes.Split(raw, []byte("\n")) {
		line = bytes.TrimSpace(bytes.TrimSuffix(line, []byte("\r")))
		if len(line) == 0 || line[0] == '#' || line[0] == ';' {
			continue
		}
		if rest, ok := bytes.CutPrefix(line, []byte("Environment=")); ok {
			// Drop-in form: one or more (optionally quoted) assignments.
			words := splitQuotedWords(rest)
			for _, word := range words {
				if k, v, ok := bytes.Cut(word, []byte("=")); ok {
					record(bytes.TrimSpace(k), v)
				}
				challenge.Zero(word)
			}
			continue
		}
		if line[0] == '[' {
			continue // unit section header in a drop-in
		}
		line = bytes.TrimPrefix(line, []byte("export "))
		k, v, ok := bytes.Cut(line, []byte("="))
		if !ok {
			continue
		}
		record(bytes.TrimSpace(k), unquote(bytes.TrimSpace(v)))
	}

	for _, n := range names {
		if v, ok := found[n]; ok && len(v) > 0 {
			out := append([]byte(nil), v...)
			return out, nil
		}
	}
	return nil, fmt.Errorf("secret file %s does not set %s", path, strings.Join(names, " or "))
}

// unquote strips one pair of matching surrounding quotes.
func unquote(v []byte) []byte {
	if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
		return v[1 : len(v)-1]
	}
	return v
}

// splitQuotedWords splits a systemd Environment= value into words, honouring
// double and single quotes. Backslash escapes are not interpreted.
func splitQuotedWords(s []byte) [][]byte {
	var words [][]byte
	var cur []byte
	var quote byte
	inWord := false
	for _, c := range s {
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			} else {
				cur = append(cur, c)
			}
		case c == '"' || c == '\'':
			quote = c
			inWord = true
		case c == ' ' || c == '\t':
			if inWord {
				words = append(words, cur)
				cur, inWord = nil, false
			}
		default:
			cur = append(cur, c)
			inWord = true
		}
	}
	if inWord {
		words = append(words, cur)
	}
	return words
}
