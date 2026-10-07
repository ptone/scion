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

/**
 * LOCAL NON-EVIDENCE loopback integration test of the REAL Wave01 runner
 * (review2 N-i). A tiny node HTTP server on 127.0.0.1 (NOT a Hub) serves a
 * synthetic shadow-DOM app shell + groups page, a fake test-login and a
 * groups list API; the actual `wave01.pw.ts` is run as a child process in
 * LAYOUT_SURVEY_DEBUG_NON_EVIDENCE mode against it. Asserts the runner's
 * error/safety/provisional/aborted paths and record completeness — never
 * any product grade. Nothing here is campaign evidence.
 */

import { test, expect } from '@playwright/test';
import { spawn } from 'node:child_process';
import * as fs from 'node:fs';
import * as http from 'node:http';
import type { AddressInfo } from 'node:net';
import * as os from 'node:os';
import * as path from 'node:path';
import { fileURLToPath } from 'node:url';

const HERE = path.dirname(fileURLToPath(import.meta.url));
const WEB = path.resolve(HERE, '..', '..');
const PW_CLI = path.join(WEB, 'node_modules', '@playwright', 'test', 'cli.js');

type Mode = 'clean' | 'safety' | 'bad-readback' | 'login-fail' | 'dev-auth-on';

const GROUPS = [
  {
    key: 'short',
    id: 'g-short-0001',
    slug: 'ls-lb-short',
    name: 'Platform',
    description: 'Platform rota.',
    labels: { team: 'platform' },
  },
  {
    key: 'long',
    id: 'g-long-0002',
    slug: 'ls-lb-long-wrapping-name',
    name: 'Infrastructure Reliability and Developer Productivity Working Group',
    description: 'Long one.',
    labels: { team: 'sre' },
  },
  {
    key: 'unicode',
    id: 'g-uni-0003',
    slug: 'ls-lb-unicode-multiline',
    name: 'Équipe Données — 数据团队',
    description: 'Uni.',
    labels: { team: 'data' },
  },
];

const esc = (s: string) => s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/"/g, '&quot;');

function mainJs(mode: Mode): string {
  const rows = (gs: typeof GROUPS) =>
    gs
      .map(
        (g) =>
          `<tr><td><a class="group-name-link" href="/admin/groups/${encodeURIComponent(g.id)}">${esc(g.name)}</a></td><td><span class="type-badge">explicit</span></td></tr>`
      )
      .join('');
  return `
const SHELL = \`<style>:host{display:flex;height:100vh;font:14px sans-serif}
.sidebar{width:220px;flex:none}@media(max-width:768px){.sidebar{display:none}}
.main{flex:1;display:flex;flex-direction:column;min-width:0}.content{flex:1;overflow:auto;padding:16px}</style>
<aside class="sidebar"><scion-nav></scion-nav></aside><div class="main"><scion-header></scion-header><div class="content"><slot></slot></div></div>\`;
customElements.define('scion-nav', class extends HTMLElement { constructor(){ super(); this.attachShadow({mode:'open'}).innerHTML =
  '<nav class="nav-container" style="overflow-y:auto;overflow-x:hidden"><a class="nav-link" href="/projects"><span class="nav-link-text">Projects</span></a></nav>'; } });
customElements.define('scion-header', class extends HTMLElement { constructor(){ super(); this.attachShadow({mode:'open'}).innerHTML =
  '<header style="display:flex;gap:8px;padding:8px"><button class="mobile-menu-btn">Menu</button><span class="page-title">Groups</span></header>'; } });
customElements.define('scion-app', class extends HTMLElement { constructor(){ super(); this.attachShadow({mode:'open'}).innerHTML = SHELL; } });
customElements.define('scion-page-admin-groups', class extends HTMLElement {
  async connectedCallback(){
    const r = this.attachShadow({mode:'open'});
    r.innerHTML = '<sl-spinner></sl-spinner>';
    const q = new URLSearchParams(location.search).get('q');
    const res = await fetch('/api/v1/groups?limit=25' + (q ? '&search=' + encodeURIComponent(q) : ''), { credentials: 'include' });
    const d = await res.json();
    const want = new Set(d.groups.map((g) => g.id));
    const all = ${JSON.stringify(rows(GROUPS))};
    const tmp = document.createElement('tbody'); tmp.innerHTML = all;
    for (const tr of Array.from(tmp.querySelectorAll('tr'))) {
      const id = decodeURIComponent(tr.querySelector('a').getAttribute('href').split('/').pop());
      if (!want.has(id)) tr.remove();
    }
    r.innerHTML = '<style>.table-container{overflow:hidden}table{width:100%;table-layout:fixed}a.group-name-link{display:block;overflow-wrap:anywhere}'
      + '.sr-only{position:absolute;width:1px;height:1px;overflow:hidden;clip:rect(0,0,0,0);white-space:nowrap}</style>'
      + '<h1>Groups</h1><button id="create-group-btn">Create group</button>'
      + '<div class="table-container"><table aria-label="Groups"><caption class="sr-only">List of groups</caption><tbody>' + tmp.innerHTML + '</tbody></table></div>';
    ${mode === 'safety' ? "setTimeout(() => fetch('/api/v1/groups', { method: 'POST', credentials: 'include', body: '{}' }), 50);" : ''}
  }
});
const p = location.pathname;
document.body.innerHTML = p.startsWith('/admin/groups/')
  ? '<scion-app><div data-scion-page><h1>Group detail</h1></div></scion-app>'
  : '<scion-app><scion-page-admin-groups data-scion-page></scion-page-admin-groups></scion-app>';
`;
}

function startServer(mode: Mode): Promise<{ server: http.Server; baseURL: string }> {
  const server = http.createServer((req, res) => {
    const u = new URL(req.url ?? '/', 'http://x');
    const authed =
      /Bearer tok-/.test(req.headers.authorization ?? '') ||
      /sess=lb/.test(req.headers.cookie ?? '');
    // rev 4 E-ENV-1a probes, mirroring pkg/hub/auth.go:467-477 and web.go:2773-2800.
    if (
      u.pathname === '/api/v1/groups' &&
      /^Bearer scion_dev_/.test(req.headers.authorization ?? '')
    ) {
      res.writeHead(401, { 'content-type': 'application/json' });
      return res.end(
        JSON.stringify({
          error: {
            code: 'unauthorized',
            message:
              mode === 'dev-auth-on'
                ? 'invalid development token'
                : 'development authentication is not enabled',
          },
        })
      );
    }
    if (u.pathname === '/auth/me') {
      if (mode === 'dev-auth-on') {
        res.writeHead(200, { 'content-type': 'application/json' });
        return res.end(
          JSON.stringify({ userId: 'dev-user', email: 'dev@localhost', role: 'admin' })
        );
      }
      res.writeHead(401, { 'content-type': 'application/json' });
      return res.end('{"error":"authentication required"}');
    }
    if (u.pathname === '/assets/main.js') {
      res.writeHead(200, { 'content-type': 'application/javascript' });
      return res.end(mainJs(mode));
    }
    if (u.pathname === '/api/v1/auth/test-login' && req.method === 'POST') {
      if (mode === 'login-fail') {
        res.writeHead(500, { 'content-type': 'text/plain' });
        return res.end('test-login disabled (loopback)');
      }
      res.writeHead(200, {
        'content-type': 'application/json',
        'set-cookie': 'sess=lb; Path=/; HttpOnly',
      });
      return res.end(
        JSON.stringify({
          user: { id: 'u-lb-admin', email: 'lb-admin@example.test', role: 'admin' },
          accessToken: 'tok-a',
          refreshToken: 'tok-r',
        })
      );
    }
    if (u.pathname === '/api/v1/groups') {
      if (req.method !== 'GET') {
        res.writeHead(405);
        return res.end();
      }
      if (!authed) {
        res.writeHead(401, { 'content-type': 'application/json' });
        return res.end('{"error":"unauthorized"}');
      }
      const search = u.searchParams.get('search');
      if (mode === 'bad-readback' && search) {
        res.writeHead(200, { 'content-type': 'application/json' });
        return res.end('<<not json>>');
      }
      const gs = GROUPS.filter((g) => !search || g.slug.startsWith(search)).map((g) => ({
        id: g.id,
        name: g.name,
        slug: g.slug,
        description: g.description,
        labels: g.labels,
        groupType: 'explicit',
      }));
      res.writeHead(200, { 'content-type': 'application/json' });
      return res.end(JSON.stringify({ groups: gs, totalCount: gs.length }));
    }
    res.writeHead(200, { 'content-type': 'text/html; charset=utf-8' });
    return void res.end(
      '<!doctype html><html><head><meta charset="utf-8"></head><body><script type="module" src="/assets/main.js"></script></body></html>'
    );
  });
  return new Promise((resolve) => {
    server.listen(0, '127.0.0.1', () => {
      const port = (server.address() as AddressInfo).port;
      resolve({ server, baseURL: `http://127.0.0.1:${port}` });
    });
  });
}

async function runRunner(mode: Mode) {
  const { server, baseURL } = await startServer(mode);
  const root = fs.mkdtempSync(path.join(os.tmpdir(), `wave01-loopback-${mode}-`));
  const priv = path.join(root, 'private');
  fs.mkdirSync(priv, { mode: 0o700 });
  const secret = path.join(priv, 'secret');
  fs.writeFileSync(secret, 'loopback-not-a-real-secret-0123456789', { mode: 0o600 });
  const map = path.join(root, 'fixture-map.json');
  fs.writeFileSync(
    map,
    JSON.stringify({
      id: 'fixture-map-lb',
      searchTerm: 'ls-lb-',
      resources: GROUPS.map((g) => ({ ...g, profile: 'normal' })),
    })
  );
  const evidence = path.join(root, 'evidence');
  const env = {
    ...process.env,
    LAYOUT_SURVEY_DEBUG_NON_EVIDENCE: '1',
    LAYOUT_SURVEY_BASE_URL: baseURL,
    LAYOUT_SURVEY_SESSION_SECRET_FILE: secret,
    LAYOUT_SURVEY_PRIVATE_DIR: priv,
    LAYOUT_SURVEY_OPERATOR: 'loopback-selftest',
    LAYOUT_SURVEY_ADMIN_EMAIL: 'lb-admin@example.test',
    LAYOUT_SURVEY_FIXTURE_MAP: map,
    LAYOUT_SURVEY_EVIDENCE_DIR: evidence,
    LAYOUT_SURVEY_STATE_DIR: path.join(root, 'state'),
    LAYOUT_SURVEY_STATES: 'W01-S01,W01-S02',
    LAYOUT_SURVEY_PW_OUTPUT_DIR: path.join(root, 'pw-out'),
  };
  for (const k of [
    'LAYOUT_SURVEY_BASE_RELEASE_FILE',
    'LAYOUT_SURVEY_COMPANION_FILE',
    'LAYOUT_SURVEY_ENV_DECLARATION_FILE',
    'LAYOUT_SURVEY_REVIEWED_RUNNER_COMMIT',
    'LAYOUT_SURVEY_REVIEWED_SUITE_DIGEST',
  ])
    delete (env as Record<string, string | undefined>)[k];
  const code = await new Promise<number>((resolve) => {
    const child = spawn(
      process.execPath,
      [PW_CLI, 'test', '-c', path.join(HERE, 'playwright.wave01.config.ts')],
      {
        cwd: WEB,
        env,
        stdio: ['ignore', 'pipe', 'pipe'],
      }
    );
    child.stdout.resume();
    child.stderr.resume();
    child.on('exit', (c) => resolve(c ?? 1));
  });
  server.close();
  const runs = fs.existsSync(evidence) ? fs.readdirSync(evidence) : [];
  expect(runs, 'exactly one run directory').toHaveLength(1);
  const runDir = path.join(evidence, runs[0]!);
  const run = JSON.parse(fs.readFileSync(path.join(runDir, 'run.json'), 'utf-8'));
  const read = (f: string) => JSON.parse(fs.readFileSync(path.join(runDir, f), 'utf-8'));
  return { code, runDir, run, read, files: fs.readdirSync(runDir) };
}

test.describe.configure({ mode: 'serial' });
test.setTimeout(10 * 60 * 1000);

test('clean loopback: every expected record written; S01/S02 substeps complete; provisional copies; debug stamp', async () => {
  const r = await runRunner('clean');
  expect(r.run.evidenceMode).toBe('debug-non-evidence');
  expect(r.runDir).toContain('debug-run-');
  for (const name of r.run.expectedRecords as string[]) expect(r.files, name).toContain(name);
  const slice = (r.run.expectedRecords as string[]).filter((n) => /^W01-S0[12]\./.test(n));
  expect(slice).toHaveLength(15);
  for (const n of slice) {
    const c = r.read(n);
    expect(c.status, `${n}: ${c.errorReason}`).toBe('complete');
    expect(c.evidenceMode).toBe('debug-non-evidence');
    expect(r.files).toContain(n.replace('.capture.json', '.provisional.json'));
  }
  const others = (r.run.expectedRecords as string[]).filter((n) => !/^W01-S0[12]\./.test(n));
  for (const n of others) expect(r.read(n).status).toBe('blocked');
  const m0 = r.read('W01-S02.P1.M0.capture.json');
  expect(
    m0.files.filter((f: { kind: string }) => f.kind === 'M0-primary-screenshot-fullpage')
  ).toHaveLength(1);
  expect(m0.loadedMainEntry?.url).toContain('/assets/main.js');
  expect(m0.outcomes.map((o: { clause: string }) => o.clause)).toEqual(
    expect.arrayContaining([
      'A-D1',
      'A-C1',
      'A-S1',
      'A-S2',
      'A-N2',
      'B-C1',
      'B-A1',
      'B-A2',
      'B-A3',
      'B-OVR',
    ])
  );
  expect(r.read('W01-S02.P1.M3.capture.json').outcomes[0].clause).toBe('B-I1');
  expect(r.read('W01-S01.P1.M1.capture.json').outcomes[0].clause).toBe('A-F1');
});

test('SAFETY: a page POST during measurement makes every affected substep a capture error with SAFETY first', async () => {
  const r = await runRunner('safety');
  const s01 = (r.run.expectedRecords as string[]).filter((n) => n.startsWith('W01-S01.'));
  for (const n of s01) {
    const c = r.read(n);
    expect(c.status).toBe('capture-error');
    expect(c.errorReason).toMatch(/^SAFETY: /);
    expect(c.outcomes).toEqual([]);
    expect(c.safetyReport.mutatingRequests[0]).toMatchObject({
      method: 'POST',
      path: '/api/v1/groups',
    });
  }
  expect(r.code).not.toBe(0);
});

test('bad readback: per-substep capture errors (both slice states bind fixtures via readback), batch continues, run.json written', async () => {
  const r = await runRunner('bad-readback');
  const slice = (r.run.expectedRecords as string[]).filter((n) => /^W01-S0[12]\./.test(n));
  expect(slice).toHaveLength(15);
  for (const n of slice) {
    const c = r.read(n);
    expect(c.status, n).toBe('capture-error');
    expect(c.errorReason).toContain('in-batch readback failed');
    expect(c.outcomes).toEqual([]);
  }
  expect(r.run.aborted).toBe(false);
  expect(r.run.counts.captureError).toBe(15);
  expect(r.code, 'runner exits non-zero when capture errors exist').not.toBe(0);
});

test('test-login failure: honest aborted run.json, batchValid false, test fails', async () => {
  const r = await runRunner('login-fail');
  expect(r.run).toMatchObject({ aborted: true, batchValid: false });
  expect(String(r.run.abortReason)).toContain('test-login failed');
  expect(r.code).not.toBe(0);
});

test('dev-auth ON (rev 4 probes): E-ENV-1 FAIL stops the batch before capture with an honest run.json', async () => {
  const r = await runRunner('dev-auth-on');
  expect(r.run.batchValid).toBe(false);
  expect((r.run.stopped as string[]).join(' ')).toContain('E-ENV-1');
  const e1 = (r.run.env as Array<{ gate: string; outcome: string }>).find(
    (g) => g.gate === 'E-ENV-1'
  )!;
  expect(e1.outcome).toBe('fail');
  expect(r.files.filter((f) => f.endsWith('.capture.json'))).toEqual([]);
  expect(JSON.stringify(r.run)).not.toMatch(/scion_dev_[0-9a-f]{64}/);
  expect(r.code).not.toBe(0);
});
