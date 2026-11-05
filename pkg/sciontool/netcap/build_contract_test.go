/*
Copyright 2026 The Scion Authors.
*/

package netcap

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// DropNetAdmin uses syscall.AllThreadsSyscall, which returns ENOTSUP in a
// binary that links cgo, so sciontool must be built with CGO_ENABLED=0 on
// every path that produces it. TestShippedSciontoolBuildsDisableCgo pins
// that in the build definitions themselves, so a change that lets either
// path link cgo again fails here, in normal CI, before any image is built:
//
//   - image-build/scion-base/Dockerfile builds the sciontool that ships to
//     agents (the image's PID 1) and runs image-build/lib/assert-no-cgo.sh
//     on it;
//   - the Makefile's container-sciontool target (a development path) builds
//     it the same way and runs the same check.

func repoFile(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "..", filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

// logicalLines joins backslash-continued lines, so a shell command split
// across lines is checked as one command.
func logicalLines(content string) []string {
	var out []string
	var cur strings.Builder
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimRight(line, " \t")
		if strings.HasSuffix(trimmed, "\\") {
			cur.WriteString(strings.TrimSuffix(trimmed, "\\"))
			cur.WriteString(" ")
			continue
		}
		cur.WriteString(trimmed)
		out = append(out, cur.String())
		cur.Reset()
	}
	return out
}

// makeRecipe returns the logical recipe lines of target in a Makefile.
func makeRecipe(t *testing.T, makefile, target string) []string {
	t.Helper()
	lines := logicalLines(makefile)
	for i, l := range lines {
		if strings.HasPrefix(l, target+":") {
			var recipe []string
			for _, r := range lines[i+1:] {
				if !strings.HasPrefix(r, "\t") {
					break
				}
				recipe = append(recipe, r)
			}
			return recipe
		}
	}
	t.Fatalf("Makefile has no %s target", target)
	return nil
}

func TestShippedSciontoolBuildsDisableCgo(t *testing.T) {
	t.Run("scion-base Dockerfile", func(t *testing.T) {
		df := repoFile(t, "image-build/scion-base/Dockerfile")
		var build []string
		for _, l := range logicalLines(df) {
			if strings.Contains(l, "-o /usr/local/bin/sciontool") {
				build = append(build, l)
			}
		}
		if len(build) != 1 {
			t.Fatalf("found %d sciontool build commands, want exactly 1", len(build))
		}
		// The command is one RUN with several && steps; check the step that
		// builds sciontool, then the guard after it.
		var buildStep string
		for _, step := range strings.Split(build[0], "&&") {
			if strings.Contains(step, "-o /usr/local/bin/sciontool") {
				buildStep = strings.TrimSpace(step)
			}
		}
		if !strings.HasPrefix(buildStep, "CGO_ENABLED=0 go build ") {
			t.Errorf("sciontool build step does not start with CGO_ENABLED=0 go build: %q", buildStep)
		}
		if !strings.Contains(build[0], "assert-no-cgo.sh /usr/local/bin/sciontool") {
			t.Error("the sciontool build is not followed by assert-no-cgo.sh on the built binary")
		}
		if !strings.Contains(df, "COPY image-build/lib/assert-no-cgo.sh ") {
			t.Error("the builder stage does not copy in assert-no-cgo.sh")
		}
	})

	t.Run("Makefile container-sciontool", func(t *testing.T) {
		recipe := makeRecipe(t, repoFile(t, "Makefile"), "container-sciontool")
		var built, guarded bool
		for _, r := range recipe {
			if strings.Contains(r, "go build") && strings.Contains(r, "./cmd/sciontool") {
				built = true
				if !strings.Contains(r, "CGO_ENABLED=0") {
					t.Errorf("container-sciontool builds without CGO_ENABLED=0: %q", strings.TrimSpace(r))
				}
			}
			if strings.Contains(r, "assert-no-cgo.sh $(CONTAINER_DIR)/sciontool") {
				guarded = true
			}
		}
		if !built {
			t.Fatal("container-sciontool has no go build of ./cmd/sciontool")
		}
		if !guarded {
			t.Error("container-sciontool does not run assert-no-cgo.sh on its output")
		}
	})
}
