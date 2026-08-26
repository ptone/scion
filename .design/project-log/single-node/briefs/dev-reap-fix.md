# Brief: Fix reapOrphanedRunsc argv matching + table-driven test

## Task

Fix a build error and write a table-driven unit test for the `reapOrphanedRunsc` function in `pkg/runtime/cloudrun_sandbox_runtime.go`. The function's matching logic was recently rewritten (NUL-split argv, exact match on final argument) but has a stale variable reference causing a build failure. You must fix it, extract the matching logic into a testable helper, and write a thorough test.

## Context

The `reapOrphanedRunsc` function scans `/proc` for orphaned `runsc delete` processes and kills them. It was originally using substring matching on a space-joined cmdline, which the security auditor flagged as unsafe (sandbox "app" would match "my-app"). The architect then caught that the first fix was wrong (it assumed a `/run/sandbox/<name>/` path that doesn't exist in real argv). The current fix uses NUL-split argv and exact equality on the final argument, which is correct -- but there's a stale variable reference breaking the build.

## What to fix

### 1. Build error: stale `args` reference

In `pkg/runtime/cloudrun_sandbox_runtime.go`, line ~996, the slog call references `args`:

```go
slog.Info("reaping orphaned runsc process",
    "sandbox", sandboxName, "pid", pid,
    "cmdline", strings.TrimSpace(args))
```

`args` no longer exists -- the old code joined NULs with spaces into `args`, but the new code splits into `argv`. Change the slog line to:

```go
"cmdline", strings.Join(argv, " "))
```

### 2. Fix import order

In the import block (line ~17), `slices` appears before `regexp`. Go convention is alphabetical order. Swap them so `regexp` comes before `slices`.

### 3. Extract matching logic into a testable helper

The matching logic inside `reapOrphanedRunsc` cannot be unit-tested because it reads `/proc`. Extract the argv-matching logic into an unexported helper:

```go
// isOrphanedRunscProcess checks whether a NUL-separated /proc/<pid>/cmdline
// belongs to an orphaned runsc delete process for the given sandbox.
//
// The match criteria (derived from captured orphan in defect-sandbox-delete-hang.md section 3):
//   - argv[0] basename contains "runsc"
//   - argv contains "delete"
//   - argv's last element is exactly sandboxName
func isOrphanedRunscProcess(cmdline []byte, sandboxName string) bool {
    argv := strings.Split(strings.TrimRight(string(cmdline), "\x00"), "\x00")
    if len(argv) < 3 {
        return false
    }
    return strings.Contains(filepath.Base(argv[0]), "runsc") &&
        slices.Contains(argv, "delete") &&
        argv[len(argv)-1] == sandboxName
}
```

Then update `reapOrphanedRunsc` to call this helper instead of inlining the logic. The slog line should reconstruct the human-readable cmdline from the parsed argv for logging.

### 4. Write a table-driven test

Write `TestIsOrphanedRunscProcess` in `pkg/runtime/cloudrun_sandbox_runtime_test.go`, placed right after the existing `TestReapOrphanedRunsc_NoProc` test (line ~1462). The test table MUST include these cases:

**Case 1: genuine captured orphan (MUST MATCH)**
Real argv captured from a Cloud Run Instance, recorded in `defect-sandbox-delete-hang.md` section 3:
```
/usr/local/gcp/bin/runsc\x00--platform=xemu\x00--platform_device_path=/dev/xemu\x00--root=/tmp/runsc-root\x00--ignore-cgroups\x00--TESTONLY-unsafe-nonroot\x00--overlay2=root:memory\x00--network=none\x00delete\x00--force\x00my-sandbox\x00
```
sandboxName: `"my-sandbox"` -- expect `true`

**Case 2: near-miss -- sandbox name is a substring of the final arg (MUST NOT MATCH)**
Same argv as case 1 but final arg is `"my-sandbox-worker"` and sandboxName is `"my-sandbox"`.
Expect `false`.

**Case 3: near-miss -- sandbox name appears as a flag value, not final arg (MUST NOT MATCH)**
```
/usr/local/gcp/bin/runsc\x00--platform=xemu\x00--root=/tmp/runsc-root\x00--network=none\x00delete\x00--force\x00--some-flag=my-sandbox\x00other-sandbox\x00
```
sandboxName: `"my-sandbox"` -- expect `false`

**Case 4: unrelated runsc process (MUST NOT MATCH)**
```
/usr/local/gcp/bin/runsc\x00--root=/tmp/runsc-root\x00create\x00--bundle=/tmp/bundle\x00my-sandbox\x00
```
sandboxName: `"my-sandbox"` -- expect `false` (no "delete" verb)

**Case 5: short cmdline (MUST NOT MATCH)**
```
/usr/local/gcp/bin/runsc\x00--help\x00
```
sandboxName: `"anything"` -- expect `false` (fewer than 3 args)

**Case 6: empty cmdline (MUST NOT MATCH)**
Empty byte slice. Expect `false`.

**Case 7: non-runsc binary with delete and matching name (MUST NOT MATCH)**
```
/usr/bin/some-tool\x00delete\x00--force\x00my-sandbox\x00
```
sandboxName: `"my-sandbox"` -- expect `false`

Use this test structure:
```go
func TestIsOrphanedRunscProcess(t *testing.T) {
    tests := []struct {
        name        string
        cmdline     []byte
        sandboxName string
        want        bool
    }{
        // ... cases above
    }
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            got := isOrphanedRunscProcess(tt.cmdline, tt.sandboxName)
            if got != tt.want {
                t.Errorf("isOrphanedRunscProcess(%q, %q) = %v, want %v",
                    tt.cmdline, tt.sandboxName, got, tt.want)
            }
        })
    }
}
```

## Verification

After making the changes, run:
1. `go build ./pkg/runtime/...` -- must pass
2. `go vet ./pkg/runtime/...` -- must pass
3. `go test ./pkg/runtime/... -run "TestIsOrphanedRunscProcess|TestReapOrphanedRunsc" -count=1 -v` -- must pass

## Boundaries

- Do NOT change the matching logic itself (NUL-split, exact equality on final arg) -- it is correct as designed.
- Do NOT change any other function or test in the file.
- Do NOT modify any file outside `pkg/runtime/cloudrun_sandbox_runtime.go` and `pkg/runtime/cloudrun_sandbox_runtime_test.go`.

## Deliverable

- Working, building code with the extracted `isOrphanedRunscProcess` helper
- Table-driven test with all 7 cases passing
- All three verification commands passing
- Commit on the current branch (`scion/dev-rebase-1294`) with message:
  `fix(runtime): extract and test reapOrphanedRunsc argv matching`
- Push the branch
- Write a project log entry to `/workspace/.design/project-log/` documenting what you fixed and why

## Reporting

Report completion to `sn-impl-em2`. If blocked, ask `sn-impl-em2`.

You MUST write the code, commit, push, write the log entry, and then mark the task complete.
