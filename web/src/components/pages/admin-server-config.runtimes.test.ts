/**
 * Env badges, the Experiments tab, maintenance, and runtime/profile editor fields.
 *
 * One part of the "scion-page-admin-server-config" suite. The suite is split
 * across admin-server-config.*.test.ts so its sections run in parallel
 * workers; shared helpers live in ./__fixtures__/admin-server-config.ts.
 */

import { describe, it, expect, vi, beforeAll, afterEach } from 'vitest';
import {
  makeBaseConfig,
  SCHEMA_RESPONSE,
  createFetchHandler,
  createComponent,
  shadowText,
  queryAll,
  query,
} from './__fixtures__/admin-server-config.js';

// ── Tests ──
describe('scion-page-admin-server-config', () => {
  let element: HTMLElement | null = null;

  beforeAll(async () => {
    vi.stubGlobal('fetch', vi.fn(createFetchHandler(makeBaseConfig())));
    await import('./admin-server-config.js');
  });

  afterEach(() => {
    element?.remove();
    element = null;
    vi.restoreAllMocks();
  });

  // ── Per-field env badges on map sections and the GCP IAM tab (ptone/scion#389) ──

  describe('env badges on runtimes, profiles and GCP IAM fields', () => {
    function envBadgeCount(el: HTMLElement): number {
      return queryAll(el, '.env-badge').length;
    }

    it('renders no env badge when nothing is overridden', async () => {
      element = await createComponent(createFetchHandler(makeBaseConfig({ settings_tier: 'db' })));
      expect(envBadgeCount(element)).toBe(0);
    });

    it('badges the runtimes and profiles sections from leaf env keys under them', async () => {
      element = await createComponent(
        createFetchHandler(
          makeBaseConfig({
            settings_tier: 'db',
            env_overrides: ['runtimes.docker.host', 'profiles.local.runtime'],
          })
        )
      );
      expect(envBadgeCount(element)).toBe(2);
    });

    it('does not badge a section from a key that only shares its name prefix', async () => {
      element = await createComponent(
        createFetchHandler(
          makeBaseConfig({ settings_tier: 'db', env_overrides: ['profilesx.local.runtime'] })
        )
      );
      expect(envBadgeCount(element)).toBe(0);
    });

    const gcpIamEnv = ['server.hub.gcp_iam_check_mode', 'server.hub.gcp_iam_deny_unknown_policy'];

    it('badges the GCP IAM check mode and deny policy fields on a hosted DB hub', async () => {
      // gcp_iam is Layer-1, so the selects stay editable and carry the env badge.
      element = await createComponent(
        createFetchHandler(makeBaseConfig({ settings_tier: 'db', env_overrides: gcpIamEnv }))
      );
      const panel = query(element, 'sl-tab-panel[name="gcp-identity"]');
      expect(panel?.querySelectorAll('.env-badge').length).toBe(2);
      // The selects stay editable: both render, and neither is env-pinned.
      expect(panel?.querySelectorAll('sl-select').length).toBe(2);
      expect(panel?.querySelectorAll('.read-only-badge').length).toBe(0);
    });

    it('shows the GCP IAM fields as env-pinned on a file-tier hub', async () => {
      element = await createComponent(
        createFetchHandler(makeBaseConfig({ env_overrides: gcpIamEnv }))
      );
      const panel = query(element, 'sl-tab-panel[name="gcp-identity"]');
      const pinned = Array.from(panel?.querySelectorAll('.read-only-badge') ?? []).filter((b) =>
        (b.textContent ?? '').includes('environment variable')
      );
      expect(pinned.length).toBe(2);
    });
  });

  // ── Experiments tab (ptone/scion#2217) ──

  describe('Experiments tab', () => {
    function showTab(el: HTMLElement, name: string): void {
      const tabGroup = query(el, 'sl-tab-group');
      tabGroup?.dispatchEvent(new CustomEvent('sl-tab-show', { detail: { name } }));
    }

    // The top-level Save & Reload / Reset bar is a direct child of the
    // shadow root; the GitHub App tab has its own unrelated ".actions" div
    // nested inside its (always-rendered, visibility-toggled) panel, so a
    // plain `.actions` query would match both.
    function topLevelActions(el: HTMLElement): Element | null {
      const candidates = el.shadowRoot?.querySelectorAll('.actions') ?? [];
      return Array.from(candidates).find((c) => c.parentNode === el.shadowRoot) ?? null;
    }

    it('appears last in the tab nav', async () => {
      element = await createComponent(createFetchHandler(makeBaseConfig()));
      // Scope to the outer tab group's direct children — the Runtimes &
      // Profiles panel nests its own sl-tab-group for agent-defaults, whose
      // tabs also carry slot="nav" but belong to a different tab group.
      const outerTabGroup = query(element, 'sl-tab-group');
      const tabs = Array.from(outerTabGroup?.querySelectorAll(':scope > sl-tab[slot="nav"]') ?? []);
      expect(tabs[tabs.length - 1].getAttribute('panel')).toBe('experiments');
    });

    it('renders <scion-admin-experiments> in its panel', async () => {
      element = await createComponent(createFetchHandler(makeBaseConfig()));
      expect(
        query(element, 'sl-tab-panel[name="experiments"] scion-admin-experiments')
      ).not.toBeNull();
    });

    it('hides the actions bar and the harness-config error message while the Experiments tab is active', async () => {
      element = await createComponent(createFetchHandler(makeBaseConfig()));

      // Force the harness-config error message's condition on, as if the
      // Runtimes & Profiles tab had an invalid JSON entry, so we can prove
      // it is specifically hidden on the Experiments tab, not just absent.
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      (element as any).harnessConfigErrors = { profileA: 'invalid json' };
      element.requestUpdate();
      await element.updateComplete;

      expect(topLevelActions(element)).not.toBeNull();
      expect(shadowText(element)).toContain('Cannot save');

      showTab(element, 'experiments');
      await element.updateComplete;

      expect(topLevelActions(element)).toBeNull();
      expect(shadowText(element)).not.toContain('Cannot save');

      showTab(element, 'general');
      await element.updateComplete;

      expect(topLevelActions(element)).not.toBeNull();
      expect(shadowText(element)).toContain('Cannot save');
    });

    it('sets .active on <scion-admin-experiments> only while its tab is shown', async () => {
      element = await createComponent(createFetchHandler(makeBaseConfig()));
      const experimentsEl = query(element, 'scion-admin-experiments') as HTMLElement & {
        active: boolean;
      };
      expect(experimentsEl.active).toBe(false);

      showTab(element, 'experiments');
      await element.updateComplete;
      expect(experimentsEl.active).toBe(true);

      showTab(element, 'general');
      await element.updateComplete;
      expect(experimentsEl.active).toBe(false);
    });
  });

  describe('safe_to_evict on runtimes and profiles', () => {
    function steConfig() {
      return makeBaseConfig({
        runtimes: { k8s: { type: 'kubernetes', safe_to_evict: false } },
        profiles: {
          gke: { runtime: 'k8s' },
          evictable: { runtime: 'k8s', safe_to_evict: true },
        },
      });
    }

    async function saveAndCapture(el: HTMLElement): Promise<void> {
      await (el as any).updateComplete;
      const buttons = queryAll(el, 'sl-button[variant="primary"]');
      const saveBtn = buttons.find((b) => b.textContent?.trim() === 'Save & Reload');
      (saveBtn as HTMLElement).click();
      await new Promise((resolve) => setTimeout(resolve, 300));
    }

    it('shows false, true and unset distinctly', async () => {
      element = await createComponent(createFetchHandler(steConfig()));
      const values = queryAll(element, 'sl-select.safe-to-evict').map((s) =>
        s.getAttribute('value')
      );
      // runtime k8s, then profiles gke and evictable
      expect(values).toEqual(['false', '', 'true']);
    });

    it('labels the select as ignored on non-Kubernetes runtimes', async () => {
      element = await createComponent(
        createFetchHandler(
          makeBaseConfig({
            runtimes: {
              k8s: { type: 'kubernetes' },
              docker: { type: 'docker', safe_to_evict: false },
              remote: {},
            },
            profiles: {
              gke: { runtime: 'k8s' },
              local: { runtime: 'docker' },
              far: { runtime: 'remote' },
            },
          })
        )
      );
      await (element as any).updateComplete;
      // the docker runtime card and the profile that uses it
      expect(queryAll(element, '.safe-to-evict-ignored').length).toBe(2);
    });

    it('sends booleans, and clearing removes the key', async () => {
      let capturedPayload: Record<string, any> | null = null;
      element = await createComponent(
        createFetchHandler(steConfig(), {
          putHandler: (body) => {
            if ('profiles' in body) capturedPayload = body;
            return { status: 200, body: { reload: { applied: [] } } };
          },
        })
      );
      const [runtimeSel, gkeSel, evictableSel] = queryAll(
        element,
        'sl-select.safe-to-evict'
      ) as (HTMLElement & { value: string })[];
      gkeSel.value = 'false';
      gkeSel.dispatchEvent(new Event('sl-change'));
      evictableSel.value = '';
      evictableSel.dispatchEvent(new Event('sl-change'));
      expect(runtimeSel).toBeDefined();
      await saveAndCapture(element);

      expect(capturedPayload).not.toBeNull();
      expect(capturedPayload!.runtimes.k8s.safe_to_evict).toBe(false);
      expect(capturedPayload!.profiles.gke.safe_to_evict).toBe(false);
      expect('safe_to_evict' in capturedPayload!.profiles.evictable).toBe(false);
    });
  });

  // ── Tier-based maintenance dispatch (fork issue: rebuild-server always used) ──

  describe('Tier-based maintenance dispatch', () => {
    it('defaults to source tier and dispatches rebuild-server/run', async () => {
      let ranPath: string | null = null;
      element = await createComponent(
        createFetchHandler(makeBaseConfig(), {
          checkUpdatesResponse: { tier: 'source', update_available: true, commits_behind: 2 },
          onOperationRun: (path) => {
            ranPath = path;
          },
        })
      );

      await (element as any).checkForUpdates();
      await (element as any).updateComplete;
      expect((element as any).deploymentTier).toBe('source');

      await (element as any).triggerUpdate();
      await (element as any).updateComplete;

      expect(ranPath).not.toBeNull();
      expect(ranPath).toContain('/operations/rebuild-server/run');
    });

    it('sets deploymentTier to binary and dispatches update-binary/run', async () => {
      let ranPath: string | null = null;
      element = await createComponent(
        createFetchHandler(makeBaseConfig(), {
          checkUpdatesResponse: {
            tier: 'binary',
            update_available: true,
            current_version: '1.0.0',
            latest_version: '1.1.0',
            channel: 'stable',
          },
          onOperationRun: (path) => {
            ranPath = path;
          },
        })
      );

      await (element as any).checkForUpdates();
      await (element as any).updateComplete;
      expect((element as any).deploymentTier).toBe('binary');

      await (element as any).triggerUpdate();
      await (element as any).updateComplete;

      expect(ranPath).not.toBeNull();
      expect(ranPath).toContain('/operations/update-binary/run');
    });

    it('renders binary-tier version info in the update banner', async () => {
      element = await createComponent(
        createFetchHandler(makeBaseConfig(), {
          checkUpdatesResponse: {
            tier: 'binary',
            update_available: true,
            current_version: '1.0.0',
            latest_version: '1.1.0',
            channel: 'stable',
            release_url: 'https://example.com/releases/1.1.0',
          },
        })
      );

      await (element as any).checkForUpdates();
      await (element as any).updateComplete;

      const text = shadowText(element);
      expect(text).toContain('1.0.0');
      expect(text).toContain('1.1.0');
      expect(text).toContain('stable');
    });

    it('omits the commit count in the source-tier banner when commits_behind is missing', async () => {
      element = await createComponent(
        createFetchHandler(makeBaseConfig(), {
          checkUpdatesResponse: { tier: 'source', update_available: true },
        })
      );

      await (element as any).checkForUpdates();
      await (element as any).updateComplete;

      const text = shadowText(element);
      expect(text).toContain('Update');
      expect(text).not.toContain('undefined');
      expect(text).not.toMatch(/new\s+commit/);
    });

    it('renders the commit count in the source-tier banner when commits_behind is set', async () => {
      element = await createComponent(
        createFetchHandler(makeBaseConfig(), {
          checkUpdatesResponse: { tier: 'source', update_available: true, commits_behind: 3 },
        })
      );

      await (element as any).checkForUpdates();
      await (element as any).updateComplete;

      expect(shadowText(element)).toMatch(/3\s+new\s+commits/);
    });

    it('shows binary-tier confirm dialog text', async () => {
      element = await createComponent(
        createFetchHandler(makeBaseConfig(), {
          checkUpdatesResponse: {
            tier: 'binary',
            update_available: true,
            current_version: '1.0.0',
            latest_version: '1.1.0',
            channel: 'stable',
          },
        })
      );

      await (element as any).checkForUpdates();
      (element as any).showUpdateConfirm = true;
      await (element as any).updateComplete;

      const text = shadowText(element);
      expect(text).toContain('download the latest release binary');
      expect(text).not.toContain('pull the latest code');
    });

    it('shows source-tier confirm dialog text', async () => {
      element = await createComponent(
        createFetchHandler(makeBaseConfig(), {
          checkUpdatesResponse: { tier: 'source', update_available: true, commits_behind: 3 },
        })
      );

      await (element as any).checkForUpdates();
      (element as any).showUpdateConfirm = true;
      await (element as any).updateComplete;

      const text = shadowText(element);
      expect(text).toContain('pull the latest code');
      expect(text).not.toContain('download the latest release binary');
    });
  });

  describe('shared_dir_storage_backend on runtimes and profiles', () => {
    function sdsConfig() {
      // File mode (settings.yaml is authoritative), where every section
      // without an env override is editable.
      return makeBaseConfig({
        runtimes: { k8s: { type: 'kubernetes', shared_dir_storage_backend: 'nfs' } },
        profiles: {
          gke: { runtime: 'k8s', shared_dir_storage_backend: 'nfs', timezone: 'UTC' },
        },
      });
    }

    async function saveAndCapture(el: HTMLElement): Promise<void> {
      await (el as any).updateComplete;
      const buttons = queryAll(el, 'sl-button[variant="primary"]');
      const saveBtn = buttons.find((b) => b.textContent?.trim() === 'Save & Reload');
      (saveBtn as HTMLElement).click();
      await new Promise((resolve) => setTimeout(resolve, 300));
    }

    it('shows the current value on the runtime and profile cards', async () => {
      element = await createComponent(createFetchHandler(sdsConfig()));
      const selects = queryAll(element, 'sl-select.shared-dir-storage-backend');
      expect(selects.length).toBe(2);
      for (const sel of selects) {
        expect(sel.getAttribute('value')).toBe('nfs');
      }
    });

    it('editing another profile field keeps the key in the PUT payload', async () => {
      let capturedPayload: Record<string, any> | null = null;
      element = await createComponent(
        createFetchHandler(sdsConfig(), {
          putHandler: (body) => {
            if ('profiles' in body) capturedPayload = body;
            return { status: 200, body: { reload: { applied: [] } } };
          },
        })
      );

      const registry = query(element, 'sl-input[placeholder="Override image registry"]') as
        | (HTMLElement & { value: string })
        | null;
      expect(registry).not.toBeNull();
      registry!.value = 'registry.example.com/team';
      registry!.dispatchEvent(new Event('sl-input'));
      await saveAndCapture(element);

      expect(capturedPayload).not.toBeNull();
      const gke = capturedPayload!.profiles.gke;
      expect(gke.image_registry).toBe('registry.example.com/team');
      expect(gke.shared_dir_storage_backend).toBe('nfs');
      expect(gke.timezone).toBe('UTC');
      expect(capturedPayload!.runtimes.k8s.shared_dir_storage_backend).toBe('nfs');
    });

    it('changing and clearing the select updates the payload', async () => {
      let capturedPayload: Record<string, any> | null = null;
      element = await createComponent(
        createFetchHandler(sdsConfig(), {
          putHandler: (body) => {
            if ('profiles' in body) capturedPayload = body;
            return { status: 200, body: { reload: { applied: [] } } };
          },
        })
      );

      const [runtimeSel, profileSel] = queryAll(
        element,
        'sl-select.shared-dir-storage-backend'
      ) as (HTMLElement & { value: string })[];
      runtimeSel.value = '';
      runtimeSel.dispatchEvent(new Event('sl-change'));
      profileSel.value = 'local';
      profileSel.dispatchEvent(new Event('sl-change'));
      await saveAndCapture(element);

      expect(capturedPayload).not.toBeNull();
      expect(capturedPayload!.profiles.gke.shared_dir_storage_backend).toBe('local');
      expect('shared_dir_storage_backend' in capturedPayload!.runtimes.k8s).toBe(false);
    });
  });

  describe('Cloud Run runtime editor field names (ptone/scion#3475)', () => {
    function cloudRunConfig(
      tier: Record<string, unknown>,
      runtime: Record<string, unknown> = {
        type: 'cloudrun',
        cloudrun: { project_id: 'proj-a', location: 'us-central1' },
      }
    ) {
      return makeBaseConfig({ ...tier, runtimes: { crun: runtime } });
    }

    function cloudRunInputs(el: HTMLElement): {
      project: HTMLElement & { value: string };
      location: HTMLElement & { value: string };
    } {
      const fields = queryAll(el, '.form-field');
      const byLabel = (label: string) => {
        const field = fields.find((f) => f.querySelector('label')?.textContent?.trim() === label);
        return field?.querySelector('sl-input') as HTMLElement & { value: string };
      };
      return { project: byLabel('GCP Project'), location: byLabel('GCP Region') };
    }

    async function saveAndCapture(el: HTMLElement): Promise<void> {
      await (el as any).updateComplete;
      const buttons = queryAll(el, 'sl-button[variant="primary"]');
      const saveBtn = buttons.find((b) => b.textContent?.trim() === 'Save & Reload');
      (saveBtn as HTMLElement).click();
      await new Promise((resolve) => setTimeout(resolve, 300));
    }

    for (const [mode, tier] of [
      ['file', {}],
      ['db', { settings_tier: 'db' }],
    ] as const) {
      it(`${mode} mode: reads and sends project_id and location`, async () => {
        let capturedPayload: Record<string, any> | null = null;
        element = await createComponent(
          createFetchHandler(cloudRunConfig(tier), {
            schemaResponse: {
              sections: {
                ...SCHEMA_RESPONSE.sections,
                runtimes: { koanf_paths: ['runtimes'] },
              },
            },
            putHandler: (body) => {
              if ('runtimes' in body) capturedPayload = body;
              return { status: 200, body: { reload: { applied: [] } } };
            },
          })
        );

        const { project, location } = cloudRunInputs(element);
        expect(project).toBeDefined();
        expect(location).toBeDefined();
        expect(project.getAttribute('value')).toBe('proj-a');
        expect(location.getAttribute('value')).toBe('us-central1');

        project.value = 'proj-b';
        project.dispatchEvent(new Event('sl-input'));
        location.value = 'europe-west1';
        location.dispatchEvent(new Event('sl-input'));
        await saveAndCapture(element);

        expect(capturedPayload).not.toBeNull();
        expect(capturedPayload!.runtimes.crun.cloudrun).toEqual({
          project_id: 'proj-b',
          location: 'europe-west1',
        });
        expect('cloudrun_instances' in capturedPayload!.runtimes.crun).toBe(false);
      });

      it(`${mode} mode: cloudrun-instances reads and sends the cloudrun_instances block`, async () => {
        let capturedPayload: Record<string, any> | null = null;
        element = await createComponent(
          createFetchHandler(
            cloudRunConfig(tier, {
              type: 'cloudrun-instances',
              cloudrun_instances: { project_id: 'proj-a', region: 'us-central1' },
            }),
            {
              schemaResponse: {
                sections: {
                  ...SCHEMA_RESPONSE.sections,
                  runtimes: { koanf_paths: ['runtimes'] },
                },
              },
              putHandler: (body) => {
                if ('runtimes' in body) capturedPayload = body;
                return { status: 200, body: { reload: { applied: [] } } };
              },
            }
          )
        );

        const { project, location } = cloudRunInputs(element);
        expect(project).toBeDefined();
        expect(location).toBeDefined();
        expect(project.getAttribute('value')).toBe('proj-a');
        expect(location.getAttribute('value')).toBe('us-central1');

        project.value = 'proj-b';
        project.dispatchEvent(new Event('sl-input'));
        location.value = 'europe-west1';
        location.dispatchEvent(new Event('sl-input'));
        await saveAndCapture(element);

        expect(capturedPayload).not.toBeNull();
        expect(capturedPayload!.runtimes.crun.cloudrun_instances).toEqual({
          project_id: 'proj-b',
          region: 'europe-west1',
        });
        expect('cloudrun' in capturedPayload!.runtimes.crun).toBe(false);
      });

      for (const [from, to, fromBlock, toBlock, toFields] of [
        [
          'cloudrun',
          'cloudrun-instances',
          'cloudrun',
          'cloudrun_instances',
          { project_id: 'proj-b', region: 'europe-west1' },
        ],
        [
          'cloudrun-instances',
          'cloudrun',
          'cloudrun_instances',
          'cloudrun',
          { project_id: 'proj-b', location: 'europe-west1' },
        ],
      ] as const) {
        it(`${mode} mode: switching ${from} to ${to} drops the ${fromBlock} block`, async () => {
          let capturedPayload: Record<string, any> | null = null;
          const fromFields =
            from === 'cloudrun'
              ? { project_id: 'proj-a', location: 'us-central1' }
              : { project_id: 'proj-a', region: 'us-central1' };
          element = await createComponent(
            createFetchHandler(
              cloudRunConfig(tier, {
                type: from,
                sync: 'tar',
                env: { FOO: 'bar' },
                [fromBlock]: fromFields,
              }),
              {
                schemaResponse: {
                  sections: {
                    ...SCHEMA_RESPONSE.sections,
                    runtimes: { koanf_paths: ['runtimes'] },
                  },
                },
                putHandler: (body) => {
                  if ('runtimes' in body) capturedPayload = body;
                  return { status: 200, body: { reload: { applied: [] } } };
                },
              }
            )
          );

          // Finds the runtime type select by its cloudrun-instances option;
          // update this if another runtime-type select appears on the page.
          const typeSelect = queryAll(element, 'sl-select').find((s) =>
            s.querySelector('sl-option[value="cloudrun-instances"]')
          ) as HTMLElement & { value: string };
          expect(typeSelect).toBeDefined();
          typeSelect.value = to;
          typeSelect.dispatchEvent(new Event('sl-change'));
          await (element as any).updateComplete;

          const { project, location } = cloudRunInputs(element);
          expect(project.getAttribute('value')).toBe('');
          expect(location.getAttribute('value')).toBe('');
          project.value = 'proj-b';
          project.dispatchEvent(new Event('sl-input'));
          location.value = 'europe-west1';
          location.dispatchEvent(new Event('sl-input'));
          await saveAndCapture(element);

          expect(capturedPayload).not.toBeNull();
          const crun = capturedPayload!.runtimes.crun;
          expect(crun.type).toBe(to);
          expect(fromBlock in crun).toBe(false);
          expect(crun[toBlock]).toEqual(toFields);
          expect(crun.env).toEqual({ FOO: 'bar' });
          expect(crun.sync).toBe('tar');
        });
      }

      for (const [type, block, fields] of [
        ['cloudrun', 'cloudrun', { project_id: 'proj-a', location: 'us-central1' }],
        [
          'cloudrun-instances',
          'cloudrun_instances',
          { project_id: 'proj-a', region: 'us-central1' },
        ],
      ] as const) {
        const loadRuntime = async (onPut: (body: Record<string, any>) => void) =>
          createComponent(
            createFetchHandler(
              cloudRunConfig(tier, { type, env: { FOO: 'bar' }, [block]: fields }),
              {
                schemaResponse: {
                  sections: {
                    ...SCHEMA_RESPONSE.sections,
                    runtimes: { koanf_paths: ['runtimes'] },
                  },
                },
                putHandler: (body) => {
                  if ('runtimes' in body) onPut(body);
                  return { status: 200, body: { reload: { applied: [] } } };
                },
              }
            )
          );

        it(`${mode} mode: clearing both ${type} fields drops the ${block} block`, async () => {
          let capturedPayload: Record<string, any> | null = null;
          element = await loadRuntime((body) => (capturedPayload = body));

          const { project, location } = cloudRunInputs(element);
          project.value = '';
          project.dispatchEvent(new Event('sl-input'));
          location.value = '';
          location.dispatchEvent(new Event('sl-input'));
          await saveAndCapture(element);

          expect(capturedPayload).not.toBeNull();
          const crun = capturedPayload!.runtimes.crun;
          expect(crun.type).toBe(type);
          expect(block in crun).toBe(false);
          expect(crun.env).toEqual({ FOO: 'bar' });
        });

        it(`${mode} mode: switching ${type} to docker drops both Cloud Run blocks`, async () => {
          let capturedPayload: Record<string, any> | null = null;
          element = await loadRuntime((body) => (capturedPayload = body));

          // Finds the runtime type select by its cloudrun-instances option;
          // update this if another runtime-type select appears on the page.
          const typeSelect = queryAll(element, 'sl-select').find((s) =>
            s.querySelector('sl-option[value="cloudrun-instances"]')
          ) as HTMLElement & { value: string };
          expect(typeSelect).toBeDefined();
          typeSelect.value = 'docker';
          typeSelect.dispatchEvent(new Event('sl-change'));
          await saveAndCapture(element);

          expect(capturedPayload).not.toBeNull();
          const crun = capturedPayload!.runtimes.crun;
          expect(crun.type).toBe('docker');
          expect('cloudrun' in crun).toBe(false);
          expect('cloudrun_instances' in crun).toBe(false);
          expect(crun.env).toEqual({ FOO: 'bar' });
        });
      }
    }
  });
});
