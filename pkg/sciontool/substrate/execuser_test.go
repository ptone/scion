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
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/substrateenv"
)

// withExecCandidateEnv swaps execAsUserCmd's env source for the duration of
// the test, restoring it on cleanup -- never touching the real process
// environment via os.Setenv.
func withExecCandidateEnv(t *testing.T, env []string) {
	t.Helper()
	orig := execCandidateEnv
	execCandidateEnv = func() []string { return env }
	t.Cleanup(func() { execCandidateEnv = orig })
}

// TestExecAsUserCmd_PlainEnvIsByteIdentical: with none of the CA-bundle
// vars set, execAsUserCmd must produce the exact script and argv it always
// has, pinned as a literal, so an accidental unconditional "-w" is caught
// immediately on the plain-install path.
func TestExecAsUserCmd_PlainEnvIsByteIdentical(t *testing.T) {
	withExecCandidateEnv(t, []string{"PATH=/usr/bin", "HOME=/home/scion"})

	got := execAsUserCmd("scion", "true")
	want := []string{
		"sh", "-c",
		`if [ "$(/usr/bin/whoami)" = "$1" ]; then exec sh -c "$2"; else exec su - "$1" -c "$2"; fi`,
		"exec-as-user", "scion", "true",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("execAsUserCmd() = %#v, want %#v (byte-identical to the pre-fix wrapper)", got, want)
	}
}

// TestExecAsUserCmd_AllCAVarsSet is test (b): with all 5 candidate vars set,
// -w carries exactly those 5, in substrateenv.TrustBundleVarNames's order.
func TestExecAsUserCmd_AllCAVarsSet(t *testing.T) {
	withExecCandidateEnv(t, []string{
		"NODE_EXTRA_CA_CERTS=/run/ate/trust-bundle.pem",
		"GIT_SSL_CAINFO=/run/ate/trust-bundle.pem",
		"SSL_CERT_FILE=/run/ate/trust-bundle.pem",
		"CURL_CA_BUNDLE=/run/ate/trust-bundle.pem",
		"SSL_CERT_DIR=/run/ate",
	})

	got := execAsUserCmd("scion", "true")
	wantScript := `if [ "$(/usr/bin/whoami)" = "$1" ]; then exec sh -c "$2"; else exec su -w NODE_EXTRA_CA_CERTS,GIT_SSL_CAINFO,SSL_CERT_FILE,CURL_CA_BUNDLE,SSL_CERT_DIR - "$1" -c "$2"; fi`
	if got[2] != wantScript {
		t.Errorf("script = %q, want %q", got[2], wantScript)
	}
}

// TestExecAsUserCmd_SubsetOfCAVarsSet is test (c): with only 2 of the 5
// candidate vars set, -w carries exactly those 2, in the fixed order (not
// the order they appear in env).
func TestExecAsUserCmd_SubsetOfCAVarsSet(t *testing.T) {
	withExecCandidateEnv(t, []string{
		"SSL_CERT_FILE=/run/ate/trust-bundle.pem",
		"NODE_EXTRA_CA_CERTS=/run/ate/trust-bundle.pem",
	})

	got := execAsUserCmd("scion", "true")
	wantScript := `if [ "$(/usr/bin/whoami)" = "$1" ]; then exec sh -c "$2"; else exec su -w NODE_EXTRA_CA_CERTS,SSL_CERT_FILE - "$1" -c "$2"; fi`
	if got[2] != wantScript {
		t.Errorf("script = %q, want %q", got[2], wantScript)
	}
}

// TestExecAsUserCmd_EmptyValueCountsAsUnset is test (d): a candidate name
// present in the environment with an empty value must be treated the same
// as absent.
func TestExecAsUserCmd_EmptyValueCountsAsUnset(t *testing.T) {
	withExecCandidateEnv(t, []string{
		"SSL_CERT_FILE=",
		"NODE_EXTRA_CA_CERTS=/run/ate/trust-bundle.pem",
	})

	got := execAsUserCmd("scion", "true")
	wantScript := `if [ "$(/usr/bin/whoami)" = "$1" ]; then exec sh -c "$2"; else exec su -w NODE_EXTRA_CA_CERTS - "$1" -c "$2"; fi`
	if got[2] != wantScript {
		t.Errorf("script = %q, want %q (SSL_CERT_FILE= must count as unset)", got[2], wantScript)
	}
}

// TestExecAsUserCmd_CandidateNamesMatchTemplateEnvNames is test (e): the tie
// between execAsUserCmd's -w candidate list and buildActorTemplate's Env
// names. Both derive from substrateenv.TrustBundleVarNames directly; this
// confirms trustBundleWhitelist actually walks that shared slice, in order,
// rather than some independent, potentially-drifted copy of the names.
func TestExecAsUserCmd_CandidateNamesMatchTemplateEnvNames(t *testing.T) {
	env := make([]string, 0, len(substrateenv.TrustBundleVarNames))
	for _, name := range substrateenv.TrustBundleVarNames {
		env = append(env, name+"=x")
	}

	got := trustBundleWhitelist(env)
	want := strings.Join(substrateenv.TrustBundleVarNames, ",")
	if got != want {
		t.Errorf("trustBundleWhitelist() = %q, want %q (substrateenv.TrustBundleVarNames)", got, want)
	}
}

// TestExecAsUserCmd_RealShellInvokesSuWithExpectedArgv is a confidence test
// beyond the minimum required: the candidate list is built in Go (see
// execAsUserCmd's doc comment for why), and this repository has no existing
// convention that requires exercising a real shell for a wrapper script
// whose logic lives entirely in the Go string that generates it. It's
// cheap and closes the remaining gap between "the Go string looks right"
// and "a real shell parses it the way we expect": it runs the actual
// generated script through /bin/sh with a PATH-shimmed `su` recorder and
// confirms the recorder receives exactly the argv `su` would.
func TestExecAsUserCmd_RealShellInvokesSuWithExpectedArgv(t *testing.T) {
	dir := t.TempDir()
	recorderPath := filepath.Join(dir, "argv.txt")
	suScript := "#!/bin/sh\necho \"$@\" > " + recorderPath + "\n"
	if err := os.WriteFile(filepath.Join(dir, "su"), []byte(suScript), 0o755); err != nil {
		t.Fatal(err)
	}

	withExecCandidateEnv(t, []string{
		"SSL_CERT_FILE=/run/ate/trust-bundle.pem",
		"NODE_EXTRA_CA_CERTS=/run/ate/trust-bundle.pem",
	})

	// A user guaranteed to differ from whatever this test process's real
	// UID resolves to, so the wrapper's whoami check takes the `su` branch
	// (the `sh -c` branch already inherits the environment unmodified and
	// isn't the one this fix touches).
	argv := execAsUserCmd("nonexistent-user-for-test", "true")
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("running wrapper: %v, output=%s", err, out)
	}

	got, err := os.ReadFile(recorderPath)
	if err != nil {
		t.Fatalf("su recorder was never invoked: %v", err)
	}
	want := "-w NODE_EXTRA_CA_CERTS,SSL_CERT_FILE - nonexistent-user-for-test -c true\n"
	if string(got) != want {
		t.Errorf("su recorder saw argv %q, want %q", string(got), want)
	}
}
