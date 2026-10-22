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

package util

import (
	"sort"
	"strings"
	"testing"
)

func TestRedactGitConfigSecrets(t *testing.T) {
	in := strings.Join([]string{
		"[core]",
		"\trepositoryformatversion = 0",
		"\tfilemode = true",
		"[remote \"origin\"]",
		"\turl = https://alice:s3cret@host.example/o/r.git",
		"\tfetch = +refs/heads/*:refs/remotes/origin/*",
		"\tfetch = +refs/tags/*:refs/tags/*",
		"[credential]",
		"\thelper = store",
		"[credential \"https://host.example\"]",
		"\tusername = alice",
		"[http \"https://host.example\"]",
		"\textraheader = Authorization: Basic QUJD",
		"\tsslVerify = true",
		"[url \"https://bob:pw@mirror.example/\"]",
		"\tinsteadOf = https://host.example/",
		"",
	}, "\n")

	out, stripped, err := RedactGitConfigSecrets([]byte(in))
	if err != nil {
		t.Fatalf("RedactGitConfigSecrets: %v", err)
	}
	outStr := string(out)

	// No secret values survive.
	for _, bad := range []string{"s3cret", "store", "Basic QUJD", "bob:pw", "username = alice", "alice:"} {
		if strings.Contains(outStr, bad) {
			t.Errorf("redacted config still contains %q:\n%s", bad, outStr)
		}
	}
	// Retained, non-secret keys and both multi-valued fetch entries survive.
	for _, keep := range []string{
		"repositoryformatversion", "filemode = ", "sslverify = ", "insteadof = ",
		"refs/heads/*:refs/remotes/origin/*", "refs/tags/*:refs/tags/*",
	} {
		if !strings.Contains(strings.ToLower(outStr), strings.ToLower(keep)) {
			t.Errorf("redacted config dropped expected key %q:\n%s", keep, outStr)
		}
	}
	// The origin URL is kept but without userinfo.
	if !strings.Contains(outStr, "https://host.example/o/r.git") {
		t.Errorf("origin url not preserved (userinfo-stripped):\n%s", outStr)
	}
	// The url.<base> subsection has its userinfo scrubbed too.
	if !strings.Contains(outStr, "https://mirror.example/") || strings.Contains(outStr, "bob") {
		t.Errorf("url.<base> subsection userinfo not scrubbed:\n%s", outStr)
	}

	wantStripped := []string{
		"credential.helper",
		"credential.https://host.example.username",
		"http.https://host.example.extraheader",
		"remote.origin.url",
		"url.https://bob:pw@mirror.example/.insteadof",
	}
	got := append([]string(nil), stripped...)
	sort.Strings(got)
	sort.Strings(wantStripped)
	if strings.Join(got, "\n") != strings.Join(wantStripped, "\n") {
		t.Errorf("stripped keys = %v, want %v", got, wantStripped)
	}

	// The result re-parses (it is valid git config).
	if _, err := parseGitConfigFile(out); err != nil {
		t.Errorf("redacted config does not re-parse: %v\n%s", err, outStr)
	}
}

func TestRedactGitConfigSecrets_ParseErrorFailsClosed(t *testing.T) {
	if _, _, err := RedactGitConfigSecrets([]byte("[lfs\n\tbroken")); err == nil {
		t.Error("an unparseable config should return an error (fail closed), got nil")
	}
}

func TestStripURLUserinfo(t *testing.T) {
	cases := map[string]struct {
		want    string
		changed bool
	}{
		"https://u:p@h/x":    {"https://h/x", true},
		"https://u@h/x":      {"https://h/x", true},
		"https://h/x":        {"https://h/x", false},
		"git@h:o/r.git":      {"git@h:o/r.git", false}, // scp-style, not http(s)
		"ssh://u@h/x":        {"ssh://u@h/x", false},   // not http(s)
	}
	for in, want := range cases {
		got, changed := stripURLUserinfo(in)
		if got != want.want || changed != want.changed {
			t.Errorf("stripURLUserinfo(%q) = (%q,%v), want (%q,%v)", in, got, changed, want.want, want.changed)
		}
	}
}
