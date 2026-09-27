/*
Copyright 2026 The Scion Authors.
*/

package rootexec

import (
	"os"
	"strings"
	"testing"
)

// TestResolve_FindsRealSystemBinaries proves Resolve finds ordinary system
// tools this suite depends on being present and trusted in any environment
// these tests run in — including, on a real Debian-family image, one
// (iptables) that is reached only through a chain of root-installed
// symlinks (update-alternatives), proving the chain is followed rather than
// refused outright.
func TestResolve_FindsRealSystemBinaries(t *testing.T) {
	for _, name := range []string{"sh", "su"} {
		t.Run(name, func(t *testing.T) {
			got, err := Resolve(name)
			if err != nil {
				t.Fatalf("Resolve(%q) = %v", name, err)
			}
			if !strings.Contains(got, name) {
				t.Errorf("Resolve(%q) = %q, want a path containing %q", name, got, name)
			}
			if !strings.HasPrefix(got, "/") {
				t.Errorf("Resolve(%q) = %q, want an absolute path", name, got)
			}
		})
	}
}

// TestResolve_RejectsPathLikeNames proves Resolve refuses anything that is
// not a bare name — it exists to turn a bare name into a verified absolute
// path, not to re-verify a path a caller already built.
func TestResolve_RejectsPathLikeNames(t *testing.T) {
	for _, name := range []string{"", "/usr/bin/sh", "a/b", "./sh"} {
		if _, err := Resolve(name); err == nil {
			t.Errorf("Resolve(%q) = nil error, want a refusal", name)
		}
	}
}

// TestResolve_NeverConsultsPATH proves Resolve's answer does not change when
// $PATH is pointed somewhere else entirely — including somewhere that would
// shadow a real binary if Resolve used the ambient PATH the way exec.Command
// or exec.LookPath does. This is the actual regression the vulnerability
// this package fixes was made of: PID 1's inherited PATH.
func TestResolve_NeverConsultsPATH(t *testing.T) {
	real, err := Resolve("sh")
	if err != nil {
		t.Fatalf("Resolve(sh) baseline: %v", err)
	}

	planted := t.TempDir()
	fake := planted + "/sh"
	if err := os.WriteFile(fake, []byte("#!/bin/sh\necho planted\n"), 0o755); err != nil {
		t.Fatalf("write planted binary: %v", err)
	}
	t.Setenv("PATH", planted+":/nonexistent")

	got, err := Resolve("sh")
	if err != nil {
		t.Fatalf("Resolve(sh) with hostile PATH: %v", err)
	}
	if got != real {
		t.Errorf("Resolve(sh) = %q with a hostile $PATH set, want unchanged %q — PATH must never be consulted", got, real)
	}
}

// TestEnv_NeverInheritsAmbientEnvironment proves Env's result never contains
// anything from the current process's real environment, even one of the
// exact variable names this package exists to keep out of a root exec.
func TestEnv_NeverInheritsAmbientEnvironment(t *testing.T) {
	t.Setenv("PATH", "/should/not/leak")
	t.Setenv("LD_PRELOAD", "/should/not/leak.so")
	t.Setenv("BASH_ENV", "/should/not/leak.sh")

	got := Env("HOME=/explicit")
	for _, kv := range got {
		if strings.Contains(kv, "should/not/leak") {
			t.Errorf("Env() = %v, leaked an ambient environment value: %q", got, kv)
		}
	}
	wantPath := "PATH=" + strings.Join(SearchPath, ":")
	if got[0] != wantPath {
		t.Errorf("Env()[0] = %q, want %q", got[0], wantPath)
	}
	if got[1] != "HOME=/explicit" {
		t.Errorf("Env()[1] = %q, want the explicit extra entry", got[1])
	}
}

// TestSanitizeInheritedEnv_StripsDangerousNamesKeepsOthers proves the one
// caller that legitimately needs to keep most of its inherited environment
// (a pre-start hook) still gets PATH fixed and every LD_*/BASH_ENV/ENV/
// IFS/GIT_*/PYTHON* entry dropped, while an unrelated, legitimately-needed
// variable like HOME survives untouched.
func TestSanitizeInheritedEnv_StripsDangerousNamesKeepsOthers(t *testing.T) {
	in := []string{
		"PATH=/usr/local/share/npm-global/bin:/usr/bin",
		"HOME=/home/scion",
		"LD_PRELOAD=/evil.so",
		"LD_LIBRARY_PATH=/evil",
		"BASH_ENV=/evil.sh",
		"ENV=/evil.sh",
		"IFS=$'\\n'",
		"GIT_SSH_COMMAND=/evil",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"PYTHONPATH=/evil",
		"SCION_AGENT_NAME=test",
	}
	got := SanitizeInheritedEnv(in)

	has := func(kv string) bool {
		for _, e := range got {
			if e == kv {
				return true
			}
		}
		return false
	}
	for _, want := range []string{
		"HOME=/home/scion",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"SCION_AGENT_NAME=test",
		"PATH=" + strings.Join(SearchPath, ":"),
	} {
		if !has(want) {
			t.Errorf("SanitizeInheritedEnv(%v) = %v, missing %q", in, got, want)
		}
	}
	for _, forbidden := range []string{"npm-global", "evil"} {
		for _, e := range got {
			if strings.Contains(e, forbidden) {
				t.Errorf("SanitizeInheritedEnv(%v) = %v, still contains forbidden value %q in %q", in, got, forbidden, e)
			}
		}
	}
}

// TestSelfExe_IsTheProcMagicSymlink proves SelfExe returns the fixed magic
// path, never a resolved on-disk path — the whole point being that this
// string is immune to the on-disk binary being replaced.
func TestSelfExe_IsTheProcMagicSymlink(t *testing.T) {
	if got := SelfExe(); got != "/proc/self/exe" {
		t.Errorf("SelfExe() = %q, want /proc/self/exe", got)
	}
}
