# P2.2 (#1655) integration test runner
# Adapted from the proven P1 runner pattern
# Dynamically removes ALL SCION_* env vars, captures raw stdout/stderr and return codes
import os, json, subprocess
from pathlib import Path

root = Path('/workspace')
r = root / 'reports/p2-1655-logs'
r.mkdir(exist_ok=True)

# Strip all SCION_* environment variables
env = {k: v for k, v in os.environ.items() if not k.startswith('SCION_')}

# Verify HEAD
head = subprocess.run(
    ['git', 'rev-parse', 'HEAD'], cwd=str(root), capture_output=True, text=True
).stdout.strip()
print(f'HEAD: {head}', flush=True)

checks = [
    ('browser-root', [
        'npx', 'playwright', 'test',
        '--config', 'e2e/terminal-workspace/playwright.config.ts',
        '--retries=0',
    ]),
    ('browser-base', [
        'npx', 'playwright', 'test',
        '--config', 'e2e/terminal-workspace/playwright.base.config.ts',
        '--retries=0',
    ]),
    ('focused-vitest', [
        'npx', 'vitest', 'run', '--maxWorkers=2',
        'src/client/terminal-layout.test.ts',
        'src/client/terminal-sessions.test.ts',
        'src/components/terminal/terminal-pane.test.ts',
    ]),
    ('fixture-types', [
        './node_modules/.bin/tsc', '--noEmit',
        '--project', 'e2e/terminal-workspace/tsconfig.json',
    ]),
    ('production-types', [
        './node_modules/.bin/tsc', '--noEmit',
    ]),
    ('production-build', [
        'npm', 'run', 'build',
    ]),
    ('fixture-lint', [
        './node_modules/.bin/eslint',
        'e2e/terminal-workspace/workspace.pw.ts',
        'src/client/terminal-workspace-root.ts',
    ]),
    ('full-vitest', [
        'npx', 'vitest', 'run', '--maxWorkers=2',
    ]),
]

results = []
for name, cmd in checks:
    with (r / (name + '.log')).open('w') as f:
        result = subprocess.run(
            cmd, cwd='/workspace/web', env=env,
            stdout=f, stderr=subprocess.STDOUT
        )
    results.append({
        'name': name,
        'command': cmd,
        'exit': result.returncode,
    })
    (r / 'checks.json').write_text(json.dumps(results, indent=2) + '\n')
    status = 'PASS' if result.returncode == 0 else 'FAIL'
    print(f'{status}: {name} (exit {result.returncode})', flush=True)
    if result.returncode:
        # Print the log for failed checks
        print(f'--- {name} log ---')
        print((r / (name + '.log')).read_text()[-2000:])
        print(f'--- end {name} log ---')
        raise SystemExit(result.returncode)

print('\nAll checks passed.', flush=True)
