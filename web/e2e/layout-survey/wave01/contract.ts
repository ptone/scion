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
 * Pinned identities and frozen constants of the Wave01 acceptance contract.
 * Values are copied from the contract text; the runner never learns them
 * from results. Section references (§) are to FROZEN rev 8.
 */

export const CONTRACT = Object.freeze({
  name: 'wave01-contract-FROZEN-rev8.md',
  revision: 8,
  bytes: 54230,
  sha256: 'f2d4f52308582daf4dd7b44d5143aa724f630e1091e99acd608463261d9e4f5e',
  supersedes: [
    '3e3662f5080c084dac2de8445923642592e0db048b7b321c77c5274a64d34008',
    '67218ac2b5a000eb04f0fb855e73243590ec19828d3b5a02d1e95d34fe0f5f42',
    '9df504e038c1f76539063c776b8b919d3c245c4a02e31db9cbff943c070c288d',
    '0fcc579e4a09d4479ec94a8d52ce28ff13e4136645feb77002be3d945dd9c855',
    '69ec8b81af27c0bbb5be53a4cdf0722c98022368d6ebc947213431aeb03d6680',
    '1877b1d40a5e4bf04d87e47d419a4451cac5ccb2d3f27f9a63d8ce32c88a5447',
    'f7143415343058363d29fb0fcf693bdd4183088a4fcf916d62d7feea2747e8ae',
  ],
});

/** Pilot finding rev 2: B-* clauses for S01/S02 (contract §2 "Groups table"). */
export const PILOT_FINDING = Object.freeze({
  name: 'oracle-FROZEN-webuat-rehearsalB-finding-uatlead.md',
  revision: 2,
  sha256: '8a287ba47ef7717099778b36f1e780430b362feed4e98c4fb99f9ed56ec572fc',
});

/** Fixture-helper acceptance excerpt (governs helper-seeded rows, H1–H7). */
export const HELPER_EXCERPT = Object.freeze({
  name: 'wave01-fixture-helper-acceptance-FROZEN-r1.md',
  sha256: 'd03ca22a0bdf73ec8246eb3daf9f56d79b51b08216a3713e64eba45ceae5ea40',
});

/** Source pins (Release identity, owner-confirmed). */
export const SOURCE = Object.freeze({
  frontendBaseline: '1694e51145a0a26bedf754751a130d7b05544232',
  backend: '4a253489ebe3298fcfe4d7271b3642a5578b2b31',
});

/** §1 profiles. DPR 1, light, en-US, UTC. */
export const PROFILES = Object.freeze([
  { id: 'P1', width: 390, height: 844 },
  { id: 'P2', width: 820, height: 1180 },
  { id: 'P3', width: 1440, height: 900 },
] as const);
export type ProfileId = (typeof PROFILES)[number]['id'];
export const ENVIRONMENT = Object.freeze({
  deviceScaleFactor: 1,
  colorScheme: 'light' as const,
  locale: 'en-US',
  timezoneId: 'UTC',
});

/**
 * APP shell element. Contract §1a names it "`scion-app-shell` (app-shell.ts)";
 * the component defined in app-shell.ts:67 at 1694e511 is registered as
 * `scion-app` (main.ts:1267-1269 SHELL_TAGS.app). The policies are pinned by
 * file:line (e.g. POL-V-APP-CONTENT `.content` app-shell.ts:169-185), so the
 * runner keys on the tag actually defined there. Assessor ruling R-1
 * (2026-10-07T15:20Z), folded into rev 3 §1a as an erratum.
 */
export const APP_SHELL_TAG = 'scion-app';

/** §1 tolerance for every geometry comparison. */
export const TOL = 1;

/** §2 A-F1 bounds. */
export const AF1_N = 60;
export const AF1_NO_TARGET_PRESSES = 20;

/** §2 timeouts. */
export const NAV_TIMEOUT_MS = 15_000;
export const DRAWER_TIMEOUT_MS = 5_000;

/** §3: consecutive capture errors for one state before quarantine. */
export const QUARANTINE_AFTER = 3;

/** §1 actionable target definition. */
export const ACTIONABLE = Object.freeze({
  tags: [
    'a',
    'button',
    'sl-button',
    'sl-icon-button',
    'input',
    'select',
    'sl-select',
    'sl-tab',
    'sl-checkbox',
    'sl-switch',
  ],
  roles: ['button', 'link', 'tab', 'menuitem'],
  /** "sl-dropdown trigger": the element slotted as trigger of an sl-dropdown. */
  dropdownTrigger: true,
});

/**
 * §1b named policies. `host` is the custom-element tag whose shadow root
 * contains the matching element ('#document' = light DOM). `selector` is
 * matched with Element.matches inside that root. `part` matches a shadow
 * part name instead of a selector. Kinds:
 *   V  = vertical scroller policy        H  = horizontal scroller policy
 *   E  = ellipsis (FULL rule; A-C1 exempt) CLAMP = clamp (same)
 *   COLLAPSED = collapsed nav label (CLIP + A-C1 exempt; A-N2 grades label)
 */
export type PolicyKind = 'V' | 'H' | 'E' | 'CLAMP' | 'COLLAPSED';
export interface PolicyDef {
  id: string;
  kind: PolicyKind;
  host: string;
  selector?: string;
  part?: string;
  /** Host attribute that must be present (e.g. collapsed). */
  hostAttr?: string;
  /** H-policy that applies only if the element's computed overflow-x is auto|scroll. */
  requiresScrollableX?: boolean;
  /** Restrict the policy to particular states (POL-V-DRAWER). */
  states?: string[];
  cite: string;
}

export const POLICIES: readonly PolicyDef[] = Object.freeze([
  { id: 'POL-V-DOC', kind: 'V', host: '#viewport', cite: 'document vertical scroll' },
  {
    id: 'POL-V-APP-CONTENT',
    kind: 'V',
    host: APP_SHELL_TAG,
    selector: '.content',
    cite: 'app-shell.ts:169-185',
  },
  {
    id: 'POL-V-APP-NAV',
    kind: 'V',
    host: 'scion-nav',
    selector: '.nav-container',
    cite: 'nav.ts:260-267',
  },
  {
    id: 'POL-V-DRAWER',
    kind: 'V',
    host: 'sl-drawer',
    part: 'body',
    states: ['W01-S05', 'A-N1'],
    cite: 'Shoelace drawer body',
  },
  {
    id: 'POL-V-PD-AGENTS-COLLAPSED',
    kind: 'V',
    host: 'scion-page-project-detail',
    selector: '.agent-grid.agents-collapsed, .agent-table-container.agents-collapsed',
    cite: 'project-detail.ts:632-639',
  },
  {
    id: 'POL-H-PD-AGENT-TABLE',
    kind: 'H',
    host: 'scion-page-project-detail',
    selector: '.agent-table-container',
    cite: 'project-detail.ts:796-803',
  },
  {
    id: 'POL-H-SL-TABNAV',
    kind: 'H',
    host: 'sl-tab-group',
    part: 'nav',
    requiresScrollableX: true,
    cite: 'shoelace 2.20.1 sl-tab-group part nav',
  },
  {
    id: 'POL-E-NAV-LABEL',
    kind: 'E',
    host: 'scion-nav',
    selector: '.nav-link-text, .collapse-toggle-text, .nav-section-title',
    cite: 'nav.ts:356-362, 403-406, 284-294',
  },
  {
    id: 'POL-COLLAPSED-NAV-LABEL',
    kind: 'COLLAPSED',
    host: 'scion-nav',
    hostAttr: 'collapsed',
    selector: '.nav-link-text',
    cite: 'nav.ts:364-368',
  },
  {
    id: 'POL-E-HEADER-TITLE',
    kind: 'E',
    host: 'scion-header',
    selector: '.page-title, .logo-text h1',
    cite: 'header.ts:216-225, 253-263',
  },
  {
    id: 'POL-E-RS-VALUE',
    kind: 'E',
    host: '*',
    selector: '.value-cell',
    cite: 'resource-styles.ts:79-86',
  },
  {
    id: 'POL-E-RS-DESC',
    kind: 'E',
    host: '*',
    selector: '.description-cell',
    cite: 'resource-styles.ts:88-94',
  },
  {
    id: 'POL-E-RS-MONO',
    kind: 'E',
    host: '*',
    selector: '.resource-table-container .mono-cell',
    cite: 'resource-styles.ts:655-662',
  },
  {
    id: 'POL-CLAMP-RS-TASK',
    kind: 'CLAMP',
    host: '*',
    selector: '.resource-table-container .task-cell',
    cite: 'resource-styles.ts:689-696',
  },
  {
    id: 'POL-E-PD-AGENT-TASK',
    kind: 'E',
    host: 'scion-page-project-detail',
    selector: '.agent-task',
    cite: 'project-detail.ts:776-784',
  },
  {
    id: 'POL-CLAMP-PD-TASK',
    kind: 'CLAMP',
    host: 'scion-page-project-detail',
    selector: '.agent-table-container .task-cell',
    cite: 'project-detail.ts:864-868',
  },
  {
    id: 'POL-E-PD-TAB-LABEL',
    kind: 'E',
    host: 'scion-page-project-detail',
    selector: '.tab-label-truncated',
    cite: 'project-detail.ts:1051-1054',
  },
  {
    id: 'POL-E-AG-TASK',
    kind: 'E',
    host: 'scion-page-agents',
    selector: '.agent-task',
    cite: 'agents.ts:363-371',
  },
  {
    id: 'POL-CLAMP-AD-NOTIF',
    kind: 'CLAMP',
    host: 'scion-page-agent-detail',
    selector: '.notif-message',
    cite: 'agent-detail.ts:597-605',
  },
  {
    id: 'POL-E-AU-NAME',
    kind: 'E',
    host: 'scion-page-admin-users',
    selector: '.user-name',
    cite: 'admin-users.ts:344-348',
  },
  {
    id: 'POL-E-AU-EMAIL',
    kind: 'E',
    host: 'scion-page-admin-users',
    selector: '.user-email',
    cite: 'admin-users.ts:351-356',
  },
]);

/**
 * Named scrollers for M0-pos-k positioning (§2b). `policy` names the
 * V/H policy that declares the scroller.
 */
export const NAMED_SCROLLERS = Object.freeze({
  document: { policy: 'POL-V-DOC', axis: 'y' },
  '.content': { policy: 'POL-V-APP-CONTENT', axis: 'y' },
  '.nav-container': { policy: 'POL-V-APP-NAV', axis: 'y' },
  '.agent-table-container': { policy: 'POL-H-PD-AGENT-TABLE', axis: 'x' },
  'sl-tab-group::part(nav)': { policy: 'POL-H-SL-TABNAV', axis: 'x' },
} as const);
