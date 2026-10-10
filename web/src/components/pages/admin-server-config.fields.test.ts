/**
 * Home storage, masked secrets, and the monitoring dashboard URL.
 *
 * One part of the "scion-page-admin-server-config" suite. The suite is split
 * across admin-server-config.*.test.ts so its sections run in parallel
 * workers; shared helpers live in ./__fixtures__/admin-server-config.ts.
 */

import { describe, it, expect, vi, beforeAll, afterEach } from 'vitest';
import {
  makeBaseConfig,
  createFetchHandler,
  createComponent,
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

  describe('home storage on runtimes and profiles', () => {
    function homeConfig() {
      return makeBaseConfig({
        runtimes: {
          k8s: { type: 'kubernetes', home_storage_backend: 'nfs', home_storage_leaf: 'broker' },
        },
        profiles: {
          gke: { runtime: 'k8s', home_storage_backend: 'nfs', home_storage_leaf: 'pod' },
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

    it('shows the current values on the runtime and profile cards', async () => {
      element = await createComponent(createFetchHandler(homeConfig()));
      const backends = queryAll(element, 'sl-select.home-storage-backend');
      expect(backends.map((s) => s.getAttribute('value'))).toEqual(['nfs', 'nfs']);
      const leaves = queryAll(element, 'sl-select.home-storage-leaf');
      expect(leaves.map((s) => s.getAttribute('value'))).toEqual(['broker', 'pod']);
    });

    it('editing another profile field keeps both keys in the PUT payload', async () => {
      let capturedPayload: Record<string, any> | null = null;
      element = await createComponent(
        createFetchHandler(homeConfig(), {
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
      expect(capturedPayload!.profiles.gke.home_storage_backend).toBe('nfs');
      expect(capturedPayload!.profiles.gke.home_storage_leaf).toBe('pod');
      expect(capturedPayload!.runtimes.k8s.home_storage_backend).toBe('nfs');
      expect(capturedPayload!.runtimes.k8s.home_storage_leaf).toBe('broker');
    });

    it('changing and clearing the selects updates the payload', async () => {
      let capturedPayload: Record<string, any> | null = null;
      element = await createComponent(
        createFetchHandler(homeConfig(), {
          putHandler: (body) => {
            if ('profiles' in body) capturedPayload = body;
            return { status: 200, body: { reload: { applied: [] } } };
          },
        })
      );
      const [runtimeLeaf, profileLeaf] = queryAll(
        element,
        'sl-select.home-storage-leaf'
      ) as (HTMLElement & {
        value: string;
      })[];
      runtimeLeaf.value = '';
      runtimeLeaf.dispatchEvent(new Event('sl-change'));
      profileLeaf.value = 'broker';
      profileLeaf.dispatchEvent(new Event('sl-change'));
      await saveAndCapture(element);

      expect(capturedPayload).not.toBeNull();
      expect(capturedPayload!.profiles.gke.home_storage_leaf).toBe('broker');
      expect('home_storage_leaf' in capturedPayload!.runtimes.k8s).toBe(false);
      expect(capturedPayload!.runtimes.k8s.home_storage_backend).toBe('nfs');
    });
  });
  describe('Regression ptone/scion#1871 — masked secrets are not sent back', () => {
    const maskedServer = {
      notification_channels: [{ type: 'slack', params: { webhook_url: '********' } }],
      oauth: { web: { github: { client_id: 'cid', client_secret: '********' } } },
      github_app: { app_id: 42, private_key: '********', webhook_secret: '********' },
    };
    const withServer = (tier: string, extra: Record<string, unknown>) => {
      const base = makeBaseConfig({ settings_tier: tier });
      return { ...base, server: { ...base.server, ...extra } };
    };

    it('DB mode payload omits unedited masked notification_channels and github_app', async () => {
      element = await createComponent(createFetchHandler(withServer('db', maskedServer)));
      const el = element as any;
      // loadGitHubAppConfig may replace github_app with an unmasked copy; pin
      // the masked GET value (what the page holds when that load fails).
      el.rawConfig.server.github_app = maskedServer.github_app;
      const server = el.buildLayer1Payload().server as Record<string, unknown>;
      expect(server).toBeDefined();
      expect(server.notification_channels).toBeUndefined();
      expect(server.github_app).toBeUndefined();
      expect(JSON.stringify(el.buildLayer1Payload())).not.toContain('********');
    });

    it('file mode payload omits unedited masked notification_channels, oauth and github_app', async () => {
      element = await createComponent(createFetchHandler(withServer('file', maskedServer)));
      const el = element as any;
      // loadGitHubAppConfig may replace github_app with an unmasked copy; pin
      // the masked GET value (what the page holds when that load fails).
      el.rawConfig.server.github_app = maskedServer.github_app;
      const server = el.buildFilePayload().server as Record<string, unknown>;
      expect(server.notification_channels).toBeUndefined();
      expect(server.oauth).toBeUndefined();
      expect(server.github_app).toBeUndefined();
      expect(JSON.stringify(el.buildFilePayload())).not.toContain('********');
    });

    it('DB mode payload still sends blocks without masked values', async () => {
      const plain = {
        notification_channels: [{ type: 'slack', filter_urgent_only: true }],
        github_app: { app_id: 42, webhooks_enabled: true },
      };
      element = await createComponent(createFetchHandler(withServer('db', plain)));
      const el = element as any;
      // Pin github_app: loadGitHubAppConfig may replace it after load.
      el.rawConfig.server.github_app = plain.github_app;
      const server = el.buildLayer1Payload().server as Record<string, unknown>;
      expect(server.notification_channels).toEqual(plain.notification_channels);
      expect(server.github_app).toEqual(plain.github_app);
    });

    it('file mode payload still sends blocks without masked values', async () => {
      const plain = {
        notification_channels: [{ type: 'slack', filter_urgent_only: true }],
        oauth: { web: { github: { client_id: 'cid' } } },
      };
      element = await createComponent(createFetchHandler(withServer('file', plain)));
      const server = (element as any).buildFilePayload().server as Record<string, unknown>;
      expect(server.notification_channels).toEqual(plain.notification_channels);
      expect(server.oauth).toEqual(plain.oauth);
    });
  });
  describe('Monitoring Dashboard URL field (ptone/scion#3597)', () => {
    const URL_A = 'https://console.cloud.google.com/monitoring/dashboards/builder/hub?project=p';

    function field(el: HTMLElement): Element {
      const label = queryAll(el, 'label').find(
        (l) => l.textContent?.trim() === 'Monitoring Dashboard URL'
      );
      expect(label).toBeTruthy();
      return label!.closest('.form-field')!;
    }

    function type(el: HTMLElement, value: string): void {
      const input = field(el).querySelector('sl-input') as HTMLInputElement;
      input.value = value;
      input.dispatchEvent(new Event('sl-input'));
    }

    async function savePayload(
      config: Record<string, unknown>,
      edit: (el: HTMLElement) => void | Promise<void>
    ): Promise<Record<string, unknown>> {
      let captured: Record<string, unknown> | null = null;
      element = await createComponent(
        createFetchHandler(config, {
          putHandler: (body) => {
            captured = body;
            return { status: 200, body: { reload: { applied: [] } } };
          },
        })
      );
      await edit(element);
      await (element as any).updateComplete;
      const saveBtn = queryAll(element, 'sl-button[variant="primary"]').find(
        (b) => b.textContent?.trim() === 'Save & Reload'
      );
      (saveBtn as HTMLElement).click();
      await new Promise((resolve) => setTimeout(resolve, 300));
      expect(captured).not.toBeNull();
      return captured!;
    }

    function hubOf(payload: Record<string, unknown>): Record<string, unknown> {
      return ((payload.server as Record<string, unknown> | undefined)?.hub ?? {}) as Record<
        string,
        unknown
      >;
    }

    function withUrl(tier: string, url?: string, extra: Record<string, unknown> = {}) {
      const base = makeBaseConfig({ settings_tier: tier, ...extra }) as any;
      if (url !== undefined) base.server.hub.monitoring_dashboard_url = url;
      return base;
    }

    it('shows the loaded value in an editable field', async () => {
      element = await createComponent(createFetchHandler(withUrl('db', URL_A)));
      expect(field(element).querySelector('sl-input')?.getAttribute('value')).toBe(URL_A);
      expect(field(element).querySelector('.env-badge')).toBeNull();
    });

    it('shows the env badge when the key is overridden by environment', async () => {
      element = await createComponent(
        createFetchHandler(
          withUrl('db', URL_A, { env_overrides: ['server.hub.monitoring_dashboard_url'] })
        )
      );
      expect(field(element).querySelector('.env-badge')).not.toBeNull();
    });

    it.each(['db', 'file'])('an untouched form does not send the key (%s mode)', async (tier) => {
      const payload = await savePayload(withUrl(tier, URL_A), () => {});
      expect(hubOf(payload)).not.toHaveProperty('monitoring_dashboard_url');
    });

    it.each(['db', 'file'])('an unset key is not materialised on save (%s mode)', async (tier) => {
      const payload = await savePayload(withUrl(tier), (el) => {
        (el as any).hubPublicUrl = 'https://other.example.com';
      });
      expect(hubOf(payload)).not.toHaveProperty('monitoring_dashboard_url');
    });

    it.each(['db', 'file'])('an edited value is sent, trimmed (%s mode)', async (tier) => {
      const payload = await savePayload(withUrl(tier), (el) => type(el, `  ${URL_A}  `));
      expect(hubOf(payload).monitoring_dashboard_url).toBe(URL_A);
    });

    it('a cleared field is sent as "" so the setting is cleared', async () => {
      const payload = await savePayload(withUrl('db', URL_A), (el) => type(el, ''));
      expect(hubOf(payload)).toHaveProperty('monitoring_dashboard_url', '');
    });

    it('an edit back to the loaded value is not sent', async () => {
      const payload = await savePayload(withUrl('db', URL_A), (el) => {
        type(el, 'https://x.example.com');
        type(el, URL_A);
      });
      expect(hubOf(payload)).not.toHaveProperty('monitoring_dashboard_url');
    });

    it('hints when the value is not an http(s) URL', async () => {
      element = await createComponent(createFetchHandler(withUrl('db')));
      type(element, 'javascript:alert(1)');
      await (element as any).updateComplete;
      expect(field(element).querySelector('.monitoring-url-invalid')).not.toBeNull();
      type(element, URL_A);
      await (element as any).updateComplete;
      expect(field(element).querySelector('.monitoring-url-invalid')).toBeNull();
    });
  });
});
