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

package substrate

import (
	"os"
	"strings"

	"github.com/GoogleCloudPlatform/scion/pkg/substrateenv"
)

// execCandidateEnv is execAsUserCmd's source of the pre-su environment, as
// a package var so a test can supply a synthetic environment without
// mutating the real process environment via os.Setenv.
var execCandidateEnv = os.Environ

// execAsUserCmd is a deliberate, near-verbatim copy of
// pkg/runtime.ExecAsUserCmd's wrapper script (see that file's doc comment
// for the full PAM/su rationale — the reasoning is identical here), with
// one intentional divergence: it conditionally passes `su -w <list>`.
//
// It is duplicated rather than imported: pkg/runtime transitively imports
// pkg/config, and sciontool must never import pkg/config (project path
// resolution has no business inside an agent container — see
// TestInitProjectDataIsolation in cmd/sciontool/commands/init_test.go,
// which fails the build if that boundary is crossed). pkg/runtime and
// pkg/sciontool/substrate are both owned by parallel work streams for this
// codebase, so extracting a shared leaf package for the whole function is
// left as a follow-up rather than done here — only the CA-bundle var names
// (substrateenv.TrustBundleVarNames) are actually shared, via a small
// dependency-free package both sides can import (see that package's doc
// comment).
//
// The divergence: whenever the target user differs from the caller,
// execAsUserCmd runs `su - <user> -c <cmd>`, a login shell — and `su -`
// discards the entire inherited environment, including the CA-bundle env
// vars buildActorTemplate (pkg/runtime/substrate_template.go) sets on the
// container when egress_trust_bundle is configured. Left unfixed, any
// exec-invoked command (the broker exec endpoint, `scion look`, or
// `/scion/v1/exec` directly) that makes a TLS request loses the gateway CA
// even though the harness itself trusts it fine.
//
// The fix is util-linux `su`'s `-w`/`--whitelist-environment` flag: a
// comma-separated list of variable names to copy from the pre-su
// environment into the post-su one, on top of `su -`'s own minimal login
// set. `-w <list>` is passed ONLY when at least one of the candidate names
// is actually set (to a non-empty value) in the pre-su environment — never
// unconditionally — so a plain (non-sdsmint) install, where none of them
// is ever set, gets the exact `su - "$1" -c "$2"` this wrapper has always
// run: byte-identical script, byte-identical argv. `<list>` names only the
// candidates that ARE set, in substrateenv.TrustBundleVarNames's fixed
// order — the same slice pkg/runtime's buildActorTemplate builds the
// container's Env from — so the two lists cannot drift apart (see
// TestExecAsUserCmd_CandidateNamesMatchTemplateEnvNames).
//
// pkg/runtime.ExecAsUserCmd (used by every other runtime) is deliberately
// left untouched: only Substrate actors run under sdsmint, `-w` is a
// util-linux-specific flag (>= 2.35; scion's images are Debian trixie,
// which satisfies this) not guaranteed present on every other runtime's
// image, and this whole mechanism only exists to counter Substrate's own
// env-propagation shape.
func execAsUserCmd(user, cmd string) []string {
	suFlag := "-"
	if list := trustBundleWhitelist(execCandidateEnv()); list != "" {
		suFlag = "-w " + list + " -"
	}
	script := `if [ "$(/usr/bin/whoami)" = "$1" ]; then exec sh -c "$2"; else exec su ` + suFlag + ` "$1" -c "$2"; fi`
	return []string{"sh", "-c", script, "exec-as-user", user, cmd}
}

// trustBundleWhitelist returns the comma-separated names, in
// substrateenv.TrustBundleVarNames's fixed order, of every candidate
// CA-bundle var with a non-empty value in env (KEY=VALUE strings, as from
// os.Environ()). Returns "" when none are set (the plain-install case, and
// the signal execAsUserCmd uses to omit `-w` entirely). An empty-string
// value counts as unset, matching `su -w`'s own behavior: there is nothing
// useful to copy for a key present but empty.
func trustBundleWhitelist(env []string) string {
	set := make(map[string]string, len(env))
	for _, kv := range env {
		if i := strings.IndexByte(kv, '='); i > 0 {
			set[kv[:i]] = kv[i+1:]
		}
	}
	var names []string
	for _, name := range substrateenv.TrustBundleVarNames {
		if set[name] != "" {
			names = append(names, name)
		}
	}
	return strings.Join(names, ",")
}
