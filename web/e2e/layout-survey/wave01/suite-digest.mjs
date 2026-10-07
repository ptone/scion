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

// Wave01 suite digest (contract FROZEN rev8 §5b E-RUN) and capture-host checks.
//
// Canonical definition (assessor-confirmed 2026-10-07T14:57Z):
//   files  = tracked blobs at the runner commit under SUITE_PATHS ∪ EXTENSION_PATHS
//   line   = "<sha256(blob bytes)>␠␠<repo-root-relative path>\n"
//   order  = LC_ALL=C byte order of the WHOLE line (i.e. effectively by hash)
//   digest = sha256(concatenation of the sorted lines)
// Equivalent shell: git ls-files <paths> | xargs sha256sum | LC_ALL=C sort | sha256sum
// Only regular blobs (100644 / 100755) are defined; any symlink, gitlink or
// other entry under the covered paths is a stop condition (assessor ruling:
// defining it would be a contract revision).
//
//   node suite-digest.mjs [--commit REV] [--manifest]
//      Prints "<digest>  <fileCount> files @ <commit>" (and the manifest).

import { execFileSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import * as path from 'node:path';
import { fileURLToPath } from 'node:url';

/** Covered roots, repo-root relative, with trailing slash. */
export const SUITE_PATHS = Object.freeze(['web/e2e/layout-survey/', 'web/e2e/harness/']);
/** Runner-extension paths outside SUITE_PATHS. None: every Wave01 file lives under layout-survey/. */
export const EXTENSION_PATHS = Object.freeze([]);
export const COVERED_PATHS = Object.freeze([...SUITE_PATHS, ...EXTENSION_PATHS]);
export const SUITE_DIGEST_METHOD =
  'wave01-E-RUN-v1 (sha256 of LC_ALL=C-sorted "<sha256>  <path>\\n" lines, tracked blobs at commit)';

const REGULAR_MODES = new Set(['100644', '100755']);

/** @param {Buffer|string} b */
const sha256 = (b) => createHash('sha256').update(b).digest('hex');

/**
 * @param {string[]} args
 * @param {string} cwd
 * @returns {Buffer}
 */
function git(args, cwd) {
  return execFileSync('git', args, { cwd, maxBuffer: 256 * 1024 * 1024 });
}

/** @param {string} [cwd] */
export function repoRoot(cwd = path.dirname(fileURLToPath(import.meta.url))) {
  return git(['rev-parse', '--show-toplevel'], cwd).toString('utf-8').trim();
}

/** @param {string} root @param {string} [rev] */
export function resolveCommit(root, rev = 'HEAD') {
  return git(['rev-parse', '--verify', `${rev}^{commit}`], root)
    .toString('utf-8')
    .trim();
}

/**
 * Pure: digest of manifest entries. Exported for unit tests.
 * @param {Array<{path: string, bytes: Buffer}>} entries
 */
export function digestEntries(entries) {
  const lines = entries.map((e) => Buffer.from(`${sha256(e.bytes)}  ${e.path}\n`, 'utf-8'));
  lines.sort(Buffer.compare);
  const manifest = Buffer.concat(lines);
  return {
    digest: sha256(manifest),
    manifest: manifest.toString('utf-8'),
    fileCount: lines.length,
  };
}

/**
 * Suite digest at a commit from tracked blobs (never working-tree bytes).
 * @param {{root?: string, commit?: string, paths?: readonly string[]}} [opts]
 */
export function suiteDigest(opts = {}) {
  const root = opts.root ?? repoRoot();
  const commit = resolveCommit(root, opts.commit ?? 'HEAD');
  const paths = opts.paths ?? COVERED_PATHS;
  const out = git(['ls-tree', '-r', '-z', '--full-tree', commit, '--', ...paths], root).toString(
    'utf-8'
  );
  /** @type {Array<{path: string, bytes: Buffer}>} */
  const entries = [];
  for (const rec of out.split('\0')) {
    if (!rec) continue;
    const tab = rec.indexOf('\t');
    const [mode, type, oid] = rec.slice(0, tab).split(' ');
    const p = rec.slice(tab + 1);
    if (type !== 'blob' || !REGULAR_MODES.has(mode ?? '')) {
      throw new Error(
        `suite digest: non-regular entry ${mode} ${type} ${p} under covered paths is undefined by contract rev8 §5b; stop and ask the assessor`
      );
    }
    entries.push({ path: p, bytes: git(['cat-file', 'blob', /** @type {string} */ (oid)], root) });
  }
  if (entries.length === 0)
    throw new Error(`suite digest: no tracked files under ${paths.join(', ')} at ${commit}`);
  return { commit, method: SUITE_DIGEST_METHOD, paths: [...paths], ...digestEntries(entries) };
}

/**
 * Capture-host checks (contract §5b): HEAD == reviewed runner commit;
 * `git status --porcelain web/e2e` empty (untracked included);
 * `git diff <reviewed> -- web/e2e` empty. Returns the raw outputs.
 * @param {{root?: string, reviewedCommit: string}} opts
 */
export function captureHostCheck(opts) {
  const root = opts.root ?? repoRoot();
  const head = resolveCommit(root, 'HEAD');
  const reviewed = resolveCommit(root, opts.reviewedCommit);
  const status = git(
    ['status', '--porcelain', '--untracked-files=all', '--', 'web/e2e'],
    root
  ).toString('utf-8');
  const diff = git(['diff', reviewed, '--', 'web/e2e'], root).toString('utf-8');
  const coveredStatus = git(
    ['status', '--porcelain', '--untracked-files=all', '--', ...COVERED_PATHS],
    root
  ).toString('utf-8');
  /** @type {string[]} */
  const problems = [];
  if (head !== reviewed) problems.push(`runner HEAD ${head} != reviewed runner commit ${reviewed}`);
  if (status.trim() !== '') problems.push('git status --porcelain web/e2e is not empty');
  if (diff.trim() !== '') problems.push(`git diff ${reviewed} -- web/e2e is not empty`);
  if (coveredStatus.trim() !== '')
    problems.push('dirty or untracked files under covered suite paths');
  return {
    ok: problems.length === 0,
    head,
    reviewedCommit: reviewed,
    statusPorcelain: status,
    diffBytes: diff.length,
    problems,
  };
}

function main() {
  const argv = process.argv.slice(2);
  const i = argv.indexOf('--commit');
  const commit = i >= 0 ? argv[i + 1] : 'HEAD';
  const r = suiteDigest({ commit });
  if (argv.includes('--manifest')) process.stdout.write(r.manifest);
  console.log(`${r.digest}  ${r.fileCount} files @ ${r.commit}`);
}

if (import.meta.url === `file://${process.argv[1]}`) {
  try {
    main();
  } catch (e) {
    console.error(String(e instanceof Error ? e.message : e));
    process.exit(2);
  }
}
