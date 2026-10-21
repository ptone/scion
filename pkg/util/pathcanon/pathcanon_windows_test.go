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

//go:build windows

package pathcanon

// One negative test per alias form of security checklist §2, each with a
// positive control: the same file under its legitimate name is Within the
// root, or the legitimate name is accepted.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/scion/internal/testutil/wintest"
)

// fixture creates root/<inside>/file and a sibling directory outside root.
func fixture(t *testing.T) (root, inside, outside string) {
	t.Helper()
	base := t.TempDir()
	root = filepath.Join(base, "root")
	inside = filepath.Join(root, "longdirectoryname-inside")
	outside = filepath.Join(base, "longdirectoryname-outside")
	for _, d := range []string{inside, outside} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(inside, "file.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	return root, inside, outside
}

func mustWithin(t *testing.T, root, p string, want bool) {
	t.Helper()
	got, err := Within(root, p)
	if err != nil {
		t.Fatalf("Within(%q, %q): %v", root, p, err)
	}
	if got != want {
		t.Fatalf("Within(%q, %q) = %v, want %v", root, p, got, want)
	}
}

func TestWithin_ShortName8dot3(t *testing.T) {
	root, inside, outside := fixture(t)
	shortIn := wintest.ShortPath(t, inside)
	shortOut := wintest.ShortPath(t, outside)
	if !strings.Contains(filepath.Base(shortIn), "~") || !strings.Contains(filepath.Base(shortOut), "~") {
		t.Fatalf("no 8.3 short names were generated (%q, %q): the Windows job must enable 8dot3 name creation on the test volume (fsutil 8dot3name set); this test fails rather than passing vacuously", shortIn, shortOut)
	}
	// The short name of a directory inside the root resolves inside...
	mustWithin(t, root, filepath.Join(shortIn, "file.txt"), true)
	// ...and the short name of one outside resolves outside, even with a
	// short-name root.
	mustWithin(t, root, shortOut, false)
	mustWithin(t, wintest.ShortPath(t, root), filepath.Join(inside, "file.txt"), true)
	// Control: the long name.
	mustWithin(t, root, filepath.Join(inside, "file.txt"), true)
	r1, _ := Resolve(shortIn)
	r2, _ := Resolve(inside)
	if r1 != r2 {
		t.Fatalf("Resolve(short) = %q, Resolve(long) = %q; want one canonical form", r1, r2)
	}
}

func TestValidName_ADS(t *testing.T) {
	for _, bad := range []string{"a:stream", "a::$DATA", "file.txt:secret:$DATA", ":x"} {
		if err := ValidName(bad); !errors.Is(err, ErrInvalidName) {
			t.Errorf("ValidName(%q) = %v, want ErrInvalidName", bad, err)
		}
	}
	root, inside, _ := fixture(t)
	if _, err := Within(root, filepath.Join(inside, "file.txt:secret")); !errors.Is(err, ErrInvalidName) {
		t.Fatalf("Within of an ADS path: %v, want ErrInvalidName", err)
	}
	// Control.
	if err := ValidName("a.stream"); err != nil {
		t.Fatalf("control: ValidName(a.stream) = %v", err)
	}
	mustWithin(t, root, filepath.Join(inside, "file.txt"), true)
}

func TestValidName_DeviceNames(t *testing.T) {
	for _, bad := range []string{"CON", "con", "nul.txt", "NUL.tar.gz", "COM1", "com9.log", "LPT1", "LPT¹", "COM²", "lpt³.txt", "AUX", "PRN", "CONIN$", "CONOUT$", "CON .txt"} {
		if err := ValidName(bad); !errors.Is(err, ErrInvalidName) {
			t.Errorf("ValidName(%q) = %v, want ErrInvalidName", bad, err)
		}
	}
	root, inside, _ := fixture(t)
	if _, err := Resolve(filepath.Join(inside, "NUL")); err == nil {
		t.Fatal("Resolve of a device name succeeded")
	}
	if _, err := Within(root, filepath.Join(inside, "con.txt")); err == nil {
		t.Fatal("Within of a device-name path succeeded")
	}
	// Control: names that only start like a device name.
	for _, ok := range []string{"CONSOLE", "nullable.txt", "COM10", "LPT", "auxiliary", "conf.d"} {
		if err := ValidName(ok); err != nil {
			t.Errorf("control: ValidName(%q) = %v", ok, err)
		}
	}
	mustWithin(t, root, filepath.Join(inside, "console.txt"), true)
}

func TestValidInputPath_ExtendedAndUNC(t *testing.T) {
	for _, bad := range []string{`\\?\C:\Windows`, `\\.\pipe\x`, `\\server\share\x`, `//server/share/x`, `\/server\share`, `\??\C:\x`, `//?/C:/x`, `C:\dir\a:b`, `C:\dir\NUL`} {
		if err := ValidInputPath(bad); !errors.Is(err, ErrInvalidName) {
			t.Errorf("ValidInputPath(%q) = %v, want ErrInvalidName", bad, err)
		}
	}
	// Control: ordinary absolute, drive-relative and relative paths.
	for _, ok := range []string{`C:\Users\u\out.json`, `C:out.json`, `rel\out.json`, `..\x\y`, `.\x`, `\rooted\x`, `D:/mixed/sep`} {
		if err := ValidInputPath(ok); err != nil {
			t.Errorf("control: ValidInputPath(%q) = %v", ok, err)
		}
	}
}

func TestWithin_CaseInsensitive(t *testing.T) {
	root, inside, _ := fixture(t)
	mustWithin(t, strings.ToUpper(root), strings.ToLower(filepath.Join(inside, "file.txt")), true)
	mustWithin(t, root, strings.ToUpper(filepath.Join(inside, "missing", "tail")), true)
	// Control: a sibling whose name only extends the root's is outside,
	// whatever the case.
	sib := root + "SIBLING"
	if err := os.Mkdir(sib, 0o700); err != nil {
		t.Fatal(err)
	}
	mustWithin(t, strings.ToLower(root), strings.ToUpper(sib), false)
}

func TestValidName_TrailingDotSpace(t *testing.T) {
	for _, bad := range []string{"a.", "a ", "a. ", "file.txt.", "dir ", "..."} {
		if err := ValidName(bad); !errors.Is(err, ErrInvalidName) {
			t.Errorf("ValidName(%q) = %v, want ErrInvalidName", bad, err)
		}
	}
	root, inside, _ := fixture(t)
	// The file system strips trailing dots and spaces, so these name the
	// same file and resolve to the same canonical form.
	r1, err := Resolve(filepath.Join(inside, "file.txt"))
	if err != nil {
		t.Fatal(err)
	}
	for _, alias := range []string{inside + `\file.txt.`, inside + `\file.txt  `, inside + `.\file.txt`} {
		r2, err := Resolve(alias)
		if err != nil || r2 != r1 {
			t.Fatalf("Resolve(%q) = %q, %v; want %q", alias, r2, err, r1)
		}
	}
	if err := ValidName("a.b"); err != nil {
		t.Fatalf("control: ValidName(a.b) = %v", err)
	}
	mustWithin(t, root, inside+`\file.txt.`, true)
}

func TestWithin_MixedSeparators(t *testing.T) {
	root, inside, outside := fixture(t)
	slashed := strings.ReplaceAll(filepath.Join(inside, "file.txt"), `\`, "/")
	mustWithin(t, root, slashed, true)
	mustWithin(t, strings.ReplaceAll(root, `\`, "/"), filepath.Join(inside, "file.txt"), true)
	mustWithin(t, root, strings.ReplaceAll(outside, `\`, "/"), false)
	mustWithin(t, root, strings.ReplaceAll(root, `\`, "/")+`/..\`+filepath.Base(outside), false)
	// Control.
	mustWithin(t, root, filepath.Join(inside, "file.txt"), true)
}

func TestWithin_JunctionEscapes(t *testing.T) {
	root, inside, outside := fixture(t)
	wintest.MustJunction(t, outside, filepath.Join(root, "j"))
	mustWithin(t, root, filepath.Join(root, "j", "x"), false)
	mustWithin(t, root, filepath.Join(root, "j"), false)
	mustWithin(t, root, filepath.Join(inside, "x"), true)
}

func TestWithin_SymlinkEscapes(t *testing.T) {
	root, inside, outside := fixture(t)
	wintest.MustSymlink(t, outside, filepath.Join(root, "s"))
	mustWithin(t, root, filepath.Join(root, "s", "x"), false)
	mustWithin(t, root, filepath.Join(inside, "x"), true)
}

func TestResolve_OutputPrefixConsistent(t *testing.T) {
	_, inside, _ := fixture(t)
	existing, err := Resolve(filepath.Join(inside, "file.txt"))
	if err != nil {
		t.Fatal(err)
	}
	missing, err := Resolve(filepath.Join(inside, "no", "such"))
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range []string{existing, missing} {
		if !strings.HasPrefix(r, `\\?\`) {
			t.Fatalf("Resolve output %q lacks the \\\\?\\ prefix", r)
		}
		again, err := Resolve(r)
		if err != nil || again != r {
			t.Fatalf("Resolve(Resolve(p)) = %q, %v; want %q (idempotent)", again, err, r)
		}
	}
	if !strings.HasSuffix(missing, `\no\such`) {
		t.Fatalf("missing tail not appended: %q", missing)
	}
	// Resolve output may be passed straight back (it is not caller input),
	// but the same \\?\ string as caller input is refused by ValidInputPath.
	if err := ValidInputPath(existing); !errors.Is(err, ErrInvalidName) {
		t.Fatalf("ValidInputPath(%q) = %v, want ErrInvalidName", existing, err)
	}
}
