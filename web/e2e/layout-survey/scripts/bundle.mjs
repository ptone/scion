#!/usr/bin/env node
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

// Evidence credential scan + immutable bundle publication.
//
//   node bundle.mjs scan DIR --private PRIVATE_DIR
//      Scans every file under DIR for (a) exact credential values found in
//      PRIVATE_DIR/credentials-*.json and the session-secret file named by
//      --secret-file (optional), (b) JWT / bearer / cookie-header patterns.
//      Prints paths + counts only, never values. Exit 1 on any match or on
//      any unreadable file (an incomplete scan is a failure, not zero).
//
//   node bundle.mjs publish RUN_DIR --dest DEST_ROOT --private PRIVATE_DIR [--secret-file F]
//      Scan first (must be clean), then write SHA256SUMS + bundle.json,
//      create DEST_ROOT/<runId>/ (refuses if it exists), copy, chmod files
//      0444 / dirs 0555, re-read every file from DEST and verify its digest,
//      and write DEST_ROOT/<runId>.tar.gz plus its .sha256 for explicit
//      transfer (e.g. GCS). Prints the bundle digest (sha256 of SHA256SUMS).

import { execFileSync } from 'node:child_process';
import * as fs from 'node:fs';
import * as path from 'node:path';
import { listTree, sha256, sha256File, treeManifest } from '../lib/digest.mjs';

const PATTERNS = [
  { id: 'jwt', re: /eyJ[A-Za-z0-9_-]{8,}\.eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}/g },
  { id: 'bearer-header', re: /Bearer\s+[A-Za-z0-9._~+/=-]{20,}/g },
  { id: 'cookie-header', re: /(?:^|[\s"'])(?:Set-)?Cookie:\s*\S+=/gi },
  { id: 'scion-dev-token', re: /scion_dev_[0-9a-f]{16,}/g },
  { id: 'private-key', re: /-----BEGIN [A-Z ]*PRIVATE KEY-----/g },
];

/** Collect exact secret values from the private dir (never printed). */
export function collectSecretValues(privateDir, secretFile) {
  const values = new Set();
  if (privateDir) {
    for (const rel of listTree(privateDir)) {
      const abs = path.join(privateDir, rel);
      if (/credentials-.*\.json$/.test(rel)) {
        const c = JSON.parse(fs.readFileSync(abs, 'utf-8'));
        for (const k of ['accessToken', 'refreshToken']) if (c[k]) values.add(String(c[k]));
      }
      if (rel.endsWith('.json') && rel.includes('storage-')) {
        const s = JSON.parse(fs.readFileSync(abs, 'utf-8'));
        for (const ck of s.cookies ?? [])
          if (ck.value && String(ck.value).length >= 8) values.add(String(ck.value));
      }
    }
  }
  if (secretFile) {
    const v = fs.readFileSync(secretFile, 'utf-8').trim();
    if (v) values.add(v);
  }
  return [...values].filter((v) => v.length >= 8);
}

/**
 * Scan dir. Returns { files, matches: [{path, kind, count}], unreadable: [] }.
 * PNG files are scanned as bytes too (latin1) so embedded text is caught.
 */
export function scanDir(dir, secretValues) {
  const result = { files: 0, matches: [], unreadable: [] };
  for (const rel of listTree(dir)) {
    let text;
    try {
      text = fs.readFileSync(path.join(dir, rel)).toString('latin1');
    } catch {
      result.unreadable.push(rel);
      continue;
    }
    result.files++;
    let exact = 0;
    for (const v of secretValues)
      if (text.includes(Buffer.from(v, 'utf-8').toString('latin1'))) exact++;
    if (exact) result.matches.push({ path: rel, kind: 'exact-issued-value', count: exact });
    for (const p of PATTERNS) {
      const n = (text.match(p.re) ?? []).length;
      if (n) result.matches.push({ path: rel, kind: `pattern:${p.id}`, count: n });
    }
  }
  return result;
}

function parse(argv) {
  const pos = [];
  const flags = {};
  for (let i = 0; i < argv.length; i++) {
    if (argv[i].startsWith('--')) {
      const v = argv[i + 1];
      if (v === undefined || v.startsWith('--') || v.trim() === '')
        throw new Error(`${argv[i]} needs a non-empty value`);
      flags[argv[i].slice(2)] = v;
      i++;
    } else pos.push(argv[i]);
  }
  return { pos, flags };
}

function report(scope, r) {
  console.log(
    `credential-scan scope=${scope} files=${r.files} matches=${r.matches.length} unreadable=${r.unreadable.length}`
  );
  for (const m of r.matches) console.log(`  MATCH ${m.kind} count=${m.count} path=${m.path}`);
  for (const u of r.unreadable) console.log(`  UNREADABLE path=${u}`);
  return r.matches.length === 0 && r.unreadable.length === 0;
}

function chmodTree(root, fileMode, dirMode) {
  for (const rel of listTree(root)) fs.chmodSync(path.join(root, rel), fileMode);
  const dirs = [root];
  for (let i = 0; i < dirs.length; i++) {
    for (const e of fs.readdirSync(dirs[i], { withFileTypes: true }))
      if (e.isDirectory()) dirs.push(path.join(dirs[i], e.name));
  }
  for (const d of dirs.reverse()) fs.chmodSync(d, dirMode);
}

function main() {
  const [cmd, ...rest] = process.argv.slice(2);
  const { pos, flags } = parse(rest);
  if (!flags.private)
    throw new Error('--private PRIVATE_DIR is required (source of exact issued values)');
  const secrets = collectSecretValues(flags.private, flags['secret-file']);
  if (secrets.length === 0)
    throw new Error('no issued credential values found under --private; refusing a vacuous scan');

  if (cmd === 'scan') {
    if (pos.length !== 1) throw new Error('scan needs exactly one DIR');
    process.exit(report(pos[0], scanDir(pos[0], secrets)) ? 0 : 1);
  }
  if (cmd === 'publish') {
    if (pos.length !== 1 || !flags.dest)
      throw new Error('publish needs RUN_DIR and --dest DEST_ROOT');
    const runDir = path.resolve(pos[0]);
    const runId = path.basename(runDir);
    if (!/^capture-run-[0-9A-Za-z-]+$/.test(runId))
      throw new Error(`unexpected run dir name ${runId}`);
    for (const f of ['run.json'])
      if (!fs.existsSync(path.join(runDir, f))) throw new Error(`${f} missing: incomplete run`);
    if (fs.existsSync(path.join(runDir, 'SHA256SUMS')))
      throw new Error('run already sealed (SHA256SUMS exists)');
    if (!report(runDir, scanDir(runDir, secrets)))
      throw new Error('credential scan not clean: refusing to publish');

    // Every file referenced by capture records must exist with the recorded digest.
    for (const rel of listTree(runDir).filter((r) => r.endsWith('.capture.json'))) {
      const rec = JSON.parse(fs.readFileSync(path.join(runDir, rel), 'utf-8'));
      for (const f of rec.files ?? []) {
        if (sha256File(path.join(runDir, f.path)) !== f.sha256)
          throw new Error(`digest mismatch for ${f.path}`);
      }
    }
    const sums = treeManifest(runDir);
    fs.writeFileSync(path.join(runDir, 'SHA256SUMS'), sums, { flag: 'wx' });
    const bundleDigest = sha256(sums);
    fs.writeFileSync(
      path.join(runDir, 'bundle.json'),
      JSON.stringify(
        {
          schemaVersion: 1,
          runId,
          bundleDigest,
          digestOf: 'SHA256SUMS',
          fileCount: sums.split('\n').filter(Boolean).length,
          sealedAt: new Date().toISOString(),
        },
        null,
        2
      ) + '\n',
      { flag: 'wx' }
    );

    const destRoot = path.resolve(flags.dest);
    fs.mkdirSync(destRoot, { recursive: true });
    const dest = path.join(destRoot, runId);
    if (fs.existsSync(dest))
      throw new Error(`destination ${dest} already exists: refusing to overwrite evidence`);
    fs.cpSync(runDir, dest, { recursive: true, errorOnExist: true, force: false });
    // Verify from destination bytes, not from the source.
    const destSums = treeManifest(dest, (r) => r !== 'SHA256SUMS' && r !== 'bundle.json');
    if (destSums !== sums) throw new Error('destination content does not match SHA256SUMS');
    chmodTree(dest, 0o444, 0o555);

    const tar = path.join(destRoot, `${runId}.tar.gz`);
    execFileSync('tar', [
      '--sort=name',
      '--owner=0',
      '--group=0',
      '--numeric-owner',
      '--mtime=@0',
      '-czf',
      tar,
      '-C',
      destRoot,
      runId,
    ]);
    const tarSha = sha256File(tar);
    fs.writeFileSync(`${tar}.sha256`, `${tarSha}  ${path.basename(tar)}\n`, { flag: 'wx' });
    fs.chmodSync(tar, 0o444);
    console.log(
      `bundle ${runId}\n  bundleDigest(SHA256SUMS)=${bundleDigest}\n  tarball=${tar}\n  tarSha256=${tarSha}`
    );
    return;
  }
  throw new Error(
    'usage: bundle.mjs scan DIR --private P | bundle.mjs publish RUN_DIR --dest ROOT --private P'
  );
}

if (import.meta.url === `file://${process.argv[1]}`) {
  try {
    main();
  } catch (e) {
    console.error(String(e instanceof Error ? e.message : e));
    process.exit(2);
  }
}
