/*
Copyright 2025 The Scion Authors.
*/

package hooks

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

// LifecycleManager handles Scion lifecycle hooks.
// These are container-level events managed by sciontool init.
type LifecycleManager struct {
	// HooksDirs is the ordered list of directories containing hook scripts.
	// Discovery walks each directory in order; per-agent staged hooks (e.g.
	// $HOME/.scion/hooks for container-script harnesses) are appended after
	// the system default and run after the system hooks.
	HooksDirs []string

	// Handlers are the registered handlers for lifecycle events.
	Handlers map[string][]Handler

	// AgentHome overrides the HOME environment variable for hook scripts.
	// Init runs as root (HOME=/root) but hook scripts (especially the
	// container-script harness provisioner) need HOME to point at the
	// scion user's home directory where the harness bundle is staged.
	AgentHome string

	// EnforcePrivilegeDrop selects privilege-drop-enforced mode (set from
	// InitRunOptions.RequirePrivilegeDrop — substrate-serve only). false
	// (the zero value) keeps executeScript's behaviour byte-identical to
	// before this field existed: every hook script runs via the calling
	// process's own credentials, exactly as today, on every other runtime.
	//
	// true switches executeScript to the fstat-based root/drop decision
	// (DecideExecAsRoot): a script runs as root only if it and every
	// directory in its chain up to "/" are root-owned and not group- or
	// world-writable; otherwise it runs dropped to WorkloadUID/WorkloadGID.
	// This applies to every event, not just pre-start — see
	// DecideExecAsRoot's doc comment for why there is no per-event
	// exception.
	EnforcePrivilegeDrop bool

	// WorkloadUID and WorkloadGID are the uid/gid a dropped hook script
	// runs as in enforced mode — the same target identity RunInit resolves
	// for the harness child process itself (setupHostUser's targetUID/
	// targetGID). Ignored when EnforcePrivilegeDrop is false.
	WorkloadUID int
	WorkloadGID int

	// WorkloadUsername is the workload account name (always "scion" for
	// every current caller — the same literal harnessSupervisorConfig
	// passes to supervisor.Config.Username), used to set HOME/USER/LOGNAME
	// for a dropped hook exactly like the harness process gets them
	// (supervisor.Supervisor.Run). Ignored when EnforcePrivilegeDrop is
	// false.
	WorkloadUsername string

	// WorkloadWorkingDir is the working directory a dropped hook script
	// runs in, matching the harness child's own resolved cwd
	// (InitRunOptions.WorkingDir / ResolveWorkingDir, threaded into
	// supervisor.Config.WorkingDir). Empty leaves the dropped hook's cwd
	// unset, inheriting init's own — RunInit sets this only after the
	// working directory has actually been resolved, so a pre-start hook
	// (which runs before that resolution) will see it empty even in
	// enforced mode; every later event (post-start, pre-stop, session-end)
	// runs after it has been set. Ignored when EnforcePrivilegeDrop is
	// false.
	WorkloadWorkingDir string
}

// NewLifecycleManager creates a new lifecycle manager.
//
// The default discovery list is built from $SCION_HOOKS_DIR (colon-separated;
// each entry is honored in order) and falls back to /etc/scion/hooks. Callers
// can append per-agent hook directories with AddHooksDir.
func NewLifecycleManager() *LifecycleManager {
	return &LifecycleManager{
		HooksDirs: defaultHooksDirs(),
		Handlers:  make(map[string][]Handler),
	}
}

func defaultHooksDirs() []string {
	if envDir := os.Getenv("SCION_HOOKS_DIR"); envDir != "" {
		var dirs []string
		for _, d := range strings.Split(envDir, ":") {
			d = strings.TrimSpace(d)
			if d != "" {
				dirs = append(dirs, d)
			}
		}
		if len(dirs) > 0 {
			return dirs
		}
	}
	return []string{"/etc/scion/hooks"}
}

// AddHooksDir appends a hooks directory to the discovery list. Per-agent
// hook directories (e.g. $HOME/.scion/hooks for container-script harnesses)
// are appended after the system default so system hooks run first. Duplicate
// paths are ignored.
func (m *LifecycleManager) AddHooksDir(dir string) {
	if dir == "" {
		return
	}
	for _, d := range m.HooksDirs {
		if d == dir {
			return
		}
	}
	m.HooksDirs = append(m.HooksDirs, dir)
}

// RegisterHandler adds a handler for a lifecycle event.
func (m *LifecycleManager) RegisterHandler(eventName string, handler Handler) {
	m.Handlers[eventName] = append(m.Handlers[eventName], handler)
}

// RunPreStart executes pre-start lifecycle hooks.
// Called after container setup but before the child process starts.
func (m *LifecycleManager) RunPreStart() error {
	event := &Event{
		Name: EventPreStart,
	}
	return m.runHooks(event)
}

// RunPostStart executes post-start lifecycle hooks.
// Called after the child process is confirmed running.
func (m *LifecycleManager) RunPostStart() error {
	event := &Event{
		Name: EventPostStart,
	}
	return m.runHooks(event)
}

// RunPreStop executes pre-stop lifecycle hooks.
// Called when a termination signal (SIGTERM/SIGINT) is received,
// before starting the graceful shutdown process.
func (m *LifecycleManager) RunPreStop() error {
	event := &Event{
		Name: EventPreStop,
	}
	return m.runHooks(event)
}

// RunSessionEnd executes session-end lifecycle hooks.
// Called on graceful shutdown before child termination.
func (m *LifecycleManager) RunSessionEnd() error {
	event := &Event{
		Name: EventSessionEnd,
	}
	return m.runHooks(event)
}

// runHooks executes both script-based and registered handlers for an event.
func (m *LifecycleManager) runHooks(event *Event) error {
	var errs []string

	// Run script-based hooks
	if err := m.runScriptHooks(event.Name); err != nil {
		errs = append(errs, fmt.Sprintf("script hooks: %v", err))
	}

	// Run registered handlers
	if handlers, ok := m.Handlers[event.Name]; ok {
		for _, handler := range handlers {
			if err := handler(event); err != nil {
				errs = append(errs, fmt.Sprintf("handler: %v", err))
			}
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("hook errors: %s", strings.Join(errs, "; "))
	}
	return nil
}

// runScriptHooks looks for and executes script files across all configured
// hooks directories in order. Within each directory, single-file hooks
// (eventName, eventName.sh) execute before the corresponding eventName.d/
// directory entries (sorted alphabetically).
func (m *LifecycleManager) runScriptHooks(eventName string) error {
	for _, dir := range m.HooksDirs {
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			continue
		}

		// Single-file hooks (legacy form)
		patterns := []string{
			filepath.Join(dir, eventName),
			filepath.Join(dir, eventName+".sh"),
		}
		for _, pattern := range patterns {
			if info, err := os.Stat(pattern); err == nil && !info.IsDir() {
				if err := m.executeScript(pattern, eventName); err != nil {
					if m.skipRefusedEntry(dir, pattern, err) {
						continue
					}
					return fmt.Errorf("script %s: %w", pattern, err)
				}
			}
		}

		// .d directory with multiple scripts
		dirPath := filepath.Join(dir, eventName+".d")
		if info, err := os.Stat(dirPath); err == nil && info.IsDir() {
			entries, err := os.ReadDir(dirPath)
			if err != nil {
				return fmt.Errorf("reading hooks dir %s: %w", dirPath, err)
			}
			// os.ReadDir already returns entries sorted by name, but be
			// explicit: stable lexical order matters because scripts use the
			// numeric prefix convention (10-foo, 20-harness-provision).
			sort.Slice(entries, func(i, j int) bool {
				return entries[i].Name() < entries[j].Name()
			})
			for _, entry := range entries {
				if entry.IsDir() {
					continue
				}
				scriptPath := filepath.Join(dirPath, entry.Name())
				if err := m.executeScript(scriptPath, eventName); err != nil {
					if m.skipRefusedEntry(dir, scriptPath, err) {
						continue
					}
					return fmt.Errorf("script %s: %w", scriptPath, err)
				}
			}
		}
	}

	return nil
}

// skipRefusedEntry reports whether a script's error should be logged and
// skipped — letting runScriptHooks move on to the next entry — rather than
// aborting every remaining hook for the event.
//
// This applies only in enforced mode, only to ErrScriptRefused (the leaf
// itself is a symlink or not a regular file — never a directory-chain
// problem, a permission error, or anything else, all of which still hard
// fail), and never to hooksDir == EnforcedHooksDir: content there is
// broker-delivered and root-owned, so a symlink or device node appearing
// there is a real anomaly worth aborting over, not workload nuisance. Every
// OTHER hooks dir (most notably $HOME/.scion/hooks) can have a workload-
// planted symlink or non-regular entry at any time — DecideExecAsRoot would
// drop it anyway, so refusing to run it is already correct; the only
// question is whether that refusal should also silently DoS every later
// hook for the same event. It should not: a workload can otherwise trivially
// disable its own remaining post-start/pre-stop/session-end hooks (or any
// hub-scoped hook sharing the same directory) by planting one broken entry
// ahead of them.
func (m *LifecycleManager) skipRefusedEntry(hooksDir, scriptPath string, err error) bool {
	if !m.EnforcePrivilegeDrop || hooksDir == EnforcedHooksDir {
		return false
	}
	if !errors.Is(err, ErrScriptRefused) {
		return false
	}
	fmt.Fprintf(os.Stderr, "[sciontool] Warning: hook script %s refused (symlink or not a regular file); skipping\n", scriptPath)
	return true
}

// executeScript runs a hook script for eventName. When EnforcePrivilegeDrop
// is false (the zero value), this is exactly today's behaviour, unchanged:
// every runtime other than substrate-serve keeps running every hook script
// via the calling process's own credentials, with no ownership check at
// all (eventName is unused on this path). See executeScriptEnforced for the
// privilege-drop-enforced path.
func (m *LifecycleManager) executeScript(path, eventName string) error {
	if m.EnforcePrivilegeDrop {
		return m.executeScriptEnforced(path, eventName)
	}

	// Check if executable
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.Mode()&0111 == 0 {
		// Not executable, skip with warning
		fmt.Fprintf(os.Stderr, "[sciontool] Warning: hook script %s is not executable, skipping\n", path)
		return nil
	}

	cmd := exec.Command(path)
	cmd.Stdout = os.Stderr // Redirect hook output to stderr
	cmd.Stderr = os.Stderr
	cmd.Env = m.hookEnv()

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("execution failed: %w", err)
	}
	return nil
}

// enforcedExecPrep is prepareEnforcedExec's result: everything
// executeScriptEnforced needs to build and run the command, plus enough for
// a test to assert the root/drop decision against real on-disk state
// without running anything or needing any privilege. fd is the script's own
// open file descriptor (caller must close it) if err is nil.
type enforcedExecPrep struct {
	fd         int
	executable bool
	asRoot     bool
}

// prepareEnforcedExec opens the script's directory chain and the script
// itself, both with O_NOFOLLOW at every component (openChainNoFollow,
// openScriptNoFollow) — never re-resolved by path — and decides root-vs-drop
// from the real fstat results via DecideExecAsRoot. Split out from
// executeScriptEnforced's actual exec so a test can assert the DECISION
// (asRoot) against a real, unprivileged filesystem fixture (e.g. a script
// under a world-writable temp dir, which DecideExecAsRoot must always drop)
// without needing to run the script or hold any privilege at all.
func prepareEnforcedExec(path string) (enforcedExecPrep, error) {
	dir := filepath.Dir(path)
	name := filepath.Base(path)

	dirFd, chain, err := openChainNoFollow(dir)
	if err != nil {
		return enforcedExecPrep{}, fmt.Errorf("hooks: %s: %w", path, err)
	}
	scriptFd, scriptOwnership, err := openScriptNoFollow(dirFd, name)
	_ = closeFd(dirFd)
	if err != nil {
		return enforcedExecPrep{}, fmt.Errorf("hooks: %s: %w", path, err)
	}

	executable, err := fdIsExecutable(scriptFd)
	if err != nil {
		_ = closeFd(scriptFd)
		return enforcedExecPrep{}, fmt.Errorf("hooks: %s: %w", path, err)
	}

	return enforcedExecPrep{
		fd:         scriptFd,
		executable: executable,
		asRoot:     DecideExecAsRoot(scriptOwnership, chain),
	}, nil
}

// executeScriptEnforced is executeScript's privilege-drop-enforced path: it
// calls prepareEnforcedExec (see its own doc comment for the no-TOCTOU
// open/decide construction) and then either skips a non-executable script or
// runs it via buildEnforcedCmd, as root or dropped to
// WorkloadUID/WorkloadGID per the prepared decision.
func (m *LifecycleManager) executeScriptEnforced(path, eventName string) error {
	prep, err := prepareEnforcedExec(path)
	if err != nil {
		return err
	}
	defer func() { _ = closeFd(prep.fd) }()

	if !prep.executable {
		fmt.Fprintf(os.Stderr, "[sciontool] Warning: hook script %s is not executable, skipping\n", path)
		return nil
	}
	if !prep.asRoot {
		fmt.Fprintf(os.Stderr,
			"[sciontool] hook script %s is not root-protected (owner/mode); running as the workload uid=%d gid=%d instead of root\n",
			path, m.WorkloadUID, m.WorkloadGID)
	}
	cmd := m.buildEnforcedCmd(prep.fd, path, eventName, prep.asRoot)

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("execution failed: %w", err)
	}
	return nil
}

// buildEnforcedCmd builds the *exec.Cmd executeScriptEnforced runs, without
// running it — split out purely so a test can assert on the constructed
// Credential/Env/Dir directly (none of which requires any privilege to
// inspect) without needing CAP_SETUID/CAP_SETGID to exercise the "dropped"
// branch, or a root-owned fixture to exercise the "as root" branch.
//
// asRoot is DecideExecAsRoot's own result for this script — the only input
// this function trusts to pick a branch; it does not re-derive or
// second-guess it. eventName picks the root branch's environment: see
// hardenedRootHookEnv's doc comment for why every event except pre-start
// gets a hardened env instead of hookEnv's workload-owned HOME.
//
// SCION_HOOK_PATH is set to the script's own path (displayPath) on both
// branches: $0, as the shebang interpreter sees it, is
// "/proc/self/fd/<n>" (execViaFd's own fexecve-equivalent construction), not
// this path — a script relying on `dirname "$0"` would otherwise silently
// break only on substrate. See §8.1 of the substrate runtime design doc.
func (m *LifecycleManager) buildEnforcedCmd(scriptFd int, path, eventName string, asRoot bool) *exec.Cmd {
	cmd := execViaFd(scriptFd, path)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr

	if asRoot {
		if eventName == EventPreStart {
			// The container-script harness's provisioner (and any project/
			// hub pre-start hook) runs once, before the workload exists at
			// all — there is no workload-owned $HOME content yet for it to
			// load — and the provisioner specifically needs
			// HOME=AgentHome to find the harness bundle it staged there.
			cmd.Env = m.hookEnv()
		} else {
			cmd.Env = m.hardenedRootHookEnv()
		}
		cmd.Env = setEnvVar(cmd.Env, "SCION_HOOK_PATH", path)
		return cmd
	}

	cmd.SysProcAttr = &syscall.SysProcAttr{
		Credential: &syscall.Credential{
			Uid: uint32(m.WorkloadUID),
			Gid: uint32(m.WorkloadGID),
		},
	}
	cmd.Env = m.droppedHookEnv()
	cmd.Env = setEnvVar(cmd.Env, "SCION_HOOK_PATH", path)
	if m.WorkloadWorkingDir != "" {
		cmd.Dir = m.WorkloadWorkingDir
	}
	return cmd
}

// hookEnv builds the environment for hook scripts. When AgentHome is set,
// HOME is overridden so that $HOME in hook scripts resolves to the scion
// user's home directory rather than root's. PYTHONDONTWRITEBYTECODE is set
// to prevent root-owned __pycache__ artifacts from being created during
// pre-start hooks (which run before privilege drop).
func (m *LifecycleManager) hookEnv() []string {
	env := os.Environ()
	env = append(env, "PYTHONDONTWRITEBYTECODE=1")
	if m.AgentHome == "" {
		return env
	}
	override := "HOME=" + m.AgentHome
	for i, e := range env {
		if strings.HasPrefix(e, "HOME=") {
			env[i] = override
			return env
		}
	}
	return append(env, override)
}

// droppedHookEnv builds the environment for a hook script that
// executeScriptEnforced has decided to run dropped: the same base env
// hookEnv builds (AgentHome-overridden HOME, PYTHONDONTWRITEBYTECODE), plus
// USER/LOGNAME set to WorkloadUsername — matching how
// supervisor.Supervisor.Run sets HOME/USER/LOGNAME for the harness child
// process itself when it drops privileges, so a dropped hook sees the same
// identity-derived environment the harness does. A no-op for USER/LOGNAME
// when WorkloadUsername is empty.
func (m *LifecycleManager) droppedHookEnv() []string {
	env := m.hookEnv()
	if m.WorkloadUsername == "" {
		return env
	}
	env = setEnvVar(env, "USER", m.WorkloadUsername)
	env = setEnvVar(env, "LOGNAME", m.WorkloadUsername)
	return env
}

// hardenedRootHookEnv builds the environment for a root-eligible hook script
// at any event AFTER pre-start (post-start, pre-stop, session-end) — i.e.
// while or after the workload has had control of $HOME. hookEnv's own
// HOME=AgentHome (the workload's own, workload-owned home directory) would
// let such a root-run hook — if it happens to invoke python, bash, git, pip,
// or anything else that consults its HOME for rc/site/config files — load
// workload-planted content (~/.bashrc, a PYTHONPATH-adjacent site
// customization, ~/.gitconfig, a pip user config) and execute it as root:
// exactly the escalation class DecideExecAsRoot exists to close, reintroduced
// through the environment instead of the exec path. So a root hook at these
// events instead gets HOME=/root (never workload-owned), PYTHONNOUSERSITE=1
// (disables Python's per-user site-packages lookup, which HOME would
// otherwise influence), and a fixed, minimal PATH that never includes
// anything workload-writable.
//
// Pre-start is exempt — see buildEnforcedCmd's own call site — because the
// only root-eligible pre-start hooks are the container-script harness's
// provisioner and any project/hub hook, both of which run once, before the
// workload exists at all, and the provisioner specifically needs
// HOME=AgentHome to find the harness bundle it staged there.
func (m *LifecycleManager) hardenedRootHookEnv() []string {
	env := m.hookEnv()
	env = setEnvVar(env, "HOME", "/root")
	env = setEnvVar(env, "PYTHONNOUSERSITE", "1")
	env = setEnvVar(env, "PATH", "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin")
	return env
}

// setEnvVar sets key=value in a KEY=VALUE environment slice, replacing an
// existing entry for key in place or appending a new one — the same
// behaviour as pkg/sciontool/supervisor's own unexported setEnvVar, kept as
// a separate copy here rather than an import so this leaf package does not
// need to depend on the supervisor package purely for a five-line string
// helper.
func setEnvVar(env []string, key, value string) []string {
	prefix := key + "="
	for i, e := range env {
		if strings.HasPrefix(e, prefix) {
			env[i] = prefix + value
			return env
		}
	}
	return append(env, prefix+value)
}
