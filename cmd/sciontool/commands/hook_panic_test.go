/*
Copyright 2026 The Scion Authors.
*/

package commands

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/GoogleCloudPlatform/scion/pkg/sciontool/hooks"
	"github.com/GoogleCloudPlatform/scion/pkg/testutil"
	"github.com/GoogleCloudPlatform/scion/pkg/util/logging/loglevel"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// hookPanicValue is a panic value longer than maxHookPanicValueLen, so the
// tests can check that it is truncated on stderr.
var hookPanicValue = "boom:" + strings.Repeat("x", 500)

// installAntigravityDialect stages the bundled antigravity dialect (which
// declares a PreToolUse response) under home, where sciontool discovers it.
func installAntigravityDialect(t *testing.T, home string) {
	t.Helper()
	spec, err := os.ReadFile(testutil.HarnessDialectPath(t, "antigravity"))
	require.NoError(t, err)
	dir := filepath.Join(home, ".scion", "harness")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "dialect.yaml"), spec, 0o644))
}

// panicInDispatch makes the next hook dispatch panic with value, from inside
// a registered handler.
func panicInDispatch(t *testing.T, value interface{}) {
	t.Helper()
	orig := markHarnessReadyFn
	markHarnessReadyFn = func(*hooks.Event) { panic(value) }
	t.Cleanup(func() { markHarnessReadyFn = orig })
}

// captureHookOutput redirects the hook's stdout and stderr writers.
func captureHookOutput(t *testing.T) (stdout, stderr *bytes.Buffer) {
	t.Helper()
	stdout, stderr = &bytes.Buffer{}, &bytes.Buffer{}
	origOut, origErr := hookStdout, hookStderr
	hookStdout, hookStderr = stdout, stderr
	t.Cleanup(func() { hookStdout, hookStderr = origOut, origErr })
	return stdout, stderr
}

// assertPanicReport checks the stderr report: one line naming the panic,
// with the value truncated to maxHookPanicValueLen.
func assertPanicReport(t *testing.T, stderr string) {
	t.Helper()
	assert.Contains(t, stderr, "sciontool hook: recovered panic (string): boom:")
	assert.Contains(t, stderr, "...(truncated)")
	assert.NotContains(t, stderr, strings.Repeat("x", maxHookPanicValueLen),
		"the panic value must be truncated on stderr")
}

func TestRunHookCommand_PanicInDispatchIsRecovered(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	scrubScionEnv(t)
	setTestLogPath(t, filepath.Join(tmpDir, "agent.log"))
	installAntigravityDialect(t, tmpDir)
	prev := hookDialect
	hookDialect = "antigravity"
	t.Cleanup(func() { hookDialect = prev })
	stdout, stderr := captureHookOutput(t)
	panicInDispatch(t, hookPanicValue)

	err := runHookCommand([]string{"PreToolUse"})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "recovered panic (string)")
	assert.Empty(t, stdout.String(), "no response may be written after a panic")
	assertPanicReport(t, stderr.String())
	assert.Equal(t, 1, strings.Count(stderr.String(), "\n"), "stderr gets a single line, not the stack")

	logData, readErr := os.ReadFile(filepath.Join(tmpDir, "agent.log"))
	require.NoError(t, readErr)
	assert.Contains(t, string(logData), "recovered panic (string)")
	assert.Contains(t, string(logData), "goroutine", "the log file gets the stack")
}

func TestRunHookCommand_NormalPathHasNoError(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	scrubScionEnv(t)
	setTestLogPath(t, filepath.Join(tmpDir, "agent.log"))
	installAntigravityDialect(t, tmpDir)
	prev := hookDialect
	hookDialect = "antigravity"
	t.Cleanup(func() { hookDialect = prev })
	stdout, stderr := captureHookOutput(t)

	require.NoError(t, runHookCommand([]string{"PreToolUse"}))
	assert.JSONEq(t, `{"decision":"allow"}`, stdout.String())
	assert.Empty(t, stderr.String())
}

func TestRecoverHookPanic_NonStringValue(t *testing.T) {
	_, stderr := captureHookOutput(t)
	setTestLogPath(t, filepath.Join(t.TempDir(), "agent.log"))

	err := func() (err error) {
		defer recoverHookPanic(&err)
		panic(errors.New("handler failed"))
	}()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "recovered panic (*errors.errorString)")
	assert.Contains(t, stderr.String(), "recovered panic (*errors.errorString): handler failed")
}

func TestRecoverHookPanic_NoPanicKeepsError(t *testing.T) {
	_, stderr := captureHookOutput(t)
	want := errors.New("plain failure")
	err := func() (err error) {
		defer recoverHookPanic(&err)
		return want
	}()
	assert.Equal(t, want, err)
	assert.Empty(t, stderr.String())
}

// longStringer is a fmt.Stringer with a long String value.
type longStringer struct{}

func (longStringer) String() string { return "stringer:" + strings.Repeat("s", 500) }

func TestRecoverHookPanic_LongErrorAndStringerAreTruncated(t *testing.T) {
	setTestLogPath(t, filepath.Join(t.TempDir(), "agent.log"))
	for name, value := range map[string]interface{}{
		"error":    errors.New("err:" + strings.Repeat("e", 500)),
		"stringer": longStringer{},
	} {
		t.Run(name, func(t *testing.T) {
			_, stderr := captureHookOutput(t)
			err := func() (err error) {
				defer recoverHookPanic(&err)
				panic(value)
			}()
			require.Error(t, err)
			line := strings.TrimSuffix(strings.TrimPrefix(stderr.String(), "[sciontool] "), "\n")
			_, reported, ok := strings.Cut(line, "): ")
			require.True(t, ok, "stderr line: %q", line)
			assert.True(t, strings.HasSuffix(reported, "...(truncated)"), "reported: %q", reported)
			assert.Len(t, strings.TrimSuffix(reported, "...(truncated)"), maxHookPanicValueLen)
		})
	}
}

func TestTruncateHookPanicValue(t *testing.T) {
	short := strings.Repeat("a", maxHookPanicValueLen)
	assert.Equal(t, short, truncateHookPanicValue(short), "a value at the limit is kept whole")

	// "é" is 2 bytes; placed so it straddles the byte limit, it must be
	// dropped whole rather than split.
	straddle := strings.Repeat("a", maxHookPanicValueLen-1) + "é" + "tail"
	got := truncateHookPanicValue(straddle)
	assert.Equal(t, strings.Repeat("a", maxHookPanicValueLen-1)+"...(truncated)", got)
	assert.True(t, utf8.ValidString(got))

	// A 3-byte rune ending exactly at the limit is kept.
	exact := strings.Repeat("a", maxHookPanicValueLen-3) + "€" + "tail"
	assert.Equal(t, strings.Repeat("a", maxHookPanicValueLen-3)+"€...(truncated)", truncateHookPanicValue(exact))

	// All multibyte: the result stays valid UTF-8 and within the limit.
	got = truncateHookPanicValue(strings.Repeat("€", 100))
	assert.True(t, utf8.ValidString(got))
	assert.LessOrEqual(t, len(strings.TrimSuffix(got, "...(truncated)")), maxHookPanicValueLen)
}

func TestExitOnHookError(t *testing.T) {
	setTestLogPath(t, filepath.Join(t.TempDir(), "agent.log"))
	var codes []int
	orig := hookExit
	hookExit = func(code int) { codes = append(codes, code) }
	t.Cleanup(func() { hookExit = orig })

	exitOnHookError(nil)
	assert.Empty(t, codes)
	exitOnHookError(errors.New("failed"))
	assert.Equal(t, []int{1}, codes)
}

// hookPanicHelperEnv makes TestHookPanicHelperProcess run `sciontool hook`
// as a real process: "ok" for a normal event, "panic" to panic in dispatch.
// hookPanicHelperHomeEnv names the HOME holding the staged dialect.
const (
	hookPanicHelperEnv     = "SCION_TEST_HOOK_PANIC_HELPER"
	hookPanicHelperHomeEnv = "SCION_TEST_HOOK_PANIC_HOME"
)

// TestHookPanicHelperProcess is not a test on its own: it is the child
// process for TestHookCommand_ExitCode, and is skipped unless the helper
// environment variable is set.
func TestHookPanicHelperProcess(t *testing.T) {
	mode := os.Getenv(hookPanicHelperEnv)
	if mode == "" {
		t.Skip("helper process for TestHookCommand_ExitCode")
	}
	_ = os.Setenv("HOME", os.Getenv(hookPanicHelperHomeEnv))
	setTestLogPath(t, filepath.Join(os.Getenv(hookPanicHelperHomeEnv), "agent.log"))
	if mode == "panic" {
		markHarnessReadyFn = func(*hooks.Event) { panic(hookPanicValue) }
	}
	rootCmd.SetArgs([]string{"hook", "--dialect=antigravity"})
	Execute()
	// A successful hook returns from Execute; exit here so the test
	// framework adds nothing to stdout.
	os.Exit(0)
}

// hookHelperEnv returns this process's environment without the keys
// scrubScionEnv clears (so the child cannot reach a Hub or export
// telemetry) and without the log level variables (so no level or
// deprecation notice is added to the child's stderr).
func hookHelperEnv() []string {
	drop := map[string]bool{loglevel.EnvLogLevel: true, loglevel.EnvDebug: true}
	for _, key := range scrubbedScionEnvKeys {
		drop[key] = true
	}
	var env []string
	for _, kv := range os.Environ() {
		key, _, _ := strings.Cut(kv, "=")
		if !drop[key] {
			env = append(env, kv)
		}
	}
	return env
}

// TestHookCommand_ExitCode runs `sciontool hook` in a child process and
// checks the real exit status: 1 (not Go's panic status 2) with empty
// stdout after a panic, and 0 with the dialect response otherwise.
func TestHookCommand_ExitCode(t *testing.T) {
	home := t.TempDir()
	installAntigravityDialect(t, home)

	run := func(mode string) (int, string, string) {
		t.Helper()
		cmd := exec.Command(os.Args[0], "-test.run=^TestHookPanicHelperProcess$")
		cmd.Env = append(hookHelperEnv(), hookPanicHelperEnv+"="+mode, hookPanicHelperHomeEnv+"="+home)
		cmd.Stdin = strings.NewReader(`{"hook_event_name":"PreToolUse","toolCall":{"name":"run_command"}}`)
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		code := 0
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			code = exitErr.ExitCode()
		} else {
			require.NoError(t, err)
		}
		return code, stdout.String(), stderr.String()
	}

	t.Run("panic exits 1 with empty stdout", func(t *testing.T) {
		code, stdout, stderr := run("panic")
		assert.Equal(t, 1, code, "stderr: %s", stderr)
		assert.Empty(t, stdout)
		assertPanicReport(t, stderr)
		assert.Equal(t, 1, strings.Count(stderr, "\n"), "stderr gets exactly one line: %q", stderr)
		assert.NotContains(t, stderr, "goroutine ", "the stack must not reach stderr")
	})
	t.Run("normal event exits 0", func(t *testing.T) {
		code, stdout, stderr := run("ok")
		assert.Equal(t, 0, code, "stderr: %s", stderr)
		assert.JSONEq(t, `{"decision":"allow"}`, stdout)
	})
}

func TestHookHelperEnv_DropsScrubbedAndLogLevelKeys(t *testing.T) {
	t.Setenv(loglevel.EnvDebug, "1")
	t.Setenv(loglevel.EnvLogLevel, "debug")
	t.Setenv("SCION_OTEL_ENDPOINT", "localhost:4317")
	t.Setenv("SCION_TEST_HOOK_KEEP", "kept")
	env := strings.Join(hookHelperEnv(), "\n")
	assert.NotContains(t, env, loglevel.EnvDebug+"=")
	assert.NotContains(t, env, loglevel.EnvLogLevel+"=")
	assert.NotContains(t, env, "SCION_OTEL_ENDPOINT=")
	assert.Contains(t, env, "SCION_TEST_HOOK_KEEP=kept")
}
