/**
 * Smoke tests, section metadata, and DB-mode layer editing.
 *
 * One part of the "scion-page-admin-server-config" suite. The suite is split
 * across admin-server-config.*.test.ts so its sections run in parallel
 * workers; shared helpers live in ./__fixtures__/admin-server-config.ts.
 */

import { describe, it, expect, vi, beforeAll, afterEach } from 'vitest';
import { setPreferredTimeZone } from '../../utils/time.js';
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

  // ── Smoke tests ──

  it('renders without errors', async () => {
    element = await createComponent(createFetchHandler(makeBaseConfig()));
    expect(element.shadowRoot).toBeTruthy();
    expect(shadowText(element)).toContain('0.1.0-test');
  });

  it('calls the config API on connect', async () => {
    element = await createComponent(createFetchHandler(makeBaseConfig()));
    expect(vi.mocked(fetch)).toHaveBeenCalledWith(
      expect.stringContaining('/api/v1/admin/server-config'),
      expect.any(Object)
    );
  });

  // ── Criterion 1: Seeded lifecycle (UI aspects) ──

  describe('Criterion 1 — Seeded lifecycle UI', () => {
    it('managed section shows source metadata (source, revision, updated_by)', async () => {
      const config = makeBaseConfig({
        settings_tier: 'db',
        section_metadata: {
          endpoints: {
            source: 'db',
            revision: 5,
            updated_by: 'admin@test.com',
            updated_at: '2026-07-01T12:00:00Z',
          },
        },
      });
      element = await createComponent(createFetchHandler(config));

      const metaEl = query(element, '.section-meta');
      expect(metaEl).not.toBeNull();
      const metaText = metaEl!.textContent ?? '';
      expect(metaText).toContain('Source:');
      expect(metaText).toContain('Database');
      expect(metaText).toContain('rev 5');
      expect(metaText).toContain('admin@test.com');
    });

    it('section metadata time renders in the display zone, 24-hour, with a zone label (tz-refactor task 20)', async () => {
      const config = makeBaseConfig({
        settings_tier: 'db',
        section_metadata: {
          // Midnight in Tokyo (UTC+9); the browser zone is pinned to UTC.
          endpoints: { source: 'db', revision: 5, updated_at: '2026-07-01T15:00:00Z' },
        },
      });
      setPreferredTimeZone('Asia/Tokyo');
      try {
        element = await createComponent(createFetchHandler(config));
        const metaText = () => query(element!, '.section-meta')?.textContent ?? '';
        expect(metaText()).toContain('Jul 2, 2026, 00:00 (Asia/Tokyo)');

        setPreferredTimeZone('UTC');
        await element.updateComplete;
        expect(metaText()).toContain('Jul 1, 2026, 15:00 (UTC)');
      } finally {
        setPreferredTimeZone('');
      }
    });

    it('build time renders in the display zone with the raw value as its title (tz-refactor task 20)', async () => {
      const buildTime = (el: HTMLElement) =>
        Array.from(el.shadowRoot?.querySelectorAll('.version-item') ?? [])
          .find(
            (item) => item.querySelector('.version-label')?.textContent?.trim() === 'Build Time'
          )
          ?.querySelector('.version-value') ?? null;
      setPreferredTimeZone('Asia/Tokyo');
      try {
        // Midnight in Tokyo (UTC+9); the browser zone is pinned to UTC.
        element = await createComponent(
          createFetchHandler(makeBaseConfig({ scion_build_time: '2026-07-01T15:00:00Z' }))
        );
        const value = buildTime(element);
        expect(value?.textContent?.trim()).toBe('Jul 2, 2026, 00:00 (Asia/Tokyo)');
        expect(value?.getAttribute('title')).toBe('2026-07-01T15:00:00Z');

        element.remove();
        // A value that is not an instant is shown unchanged.
        element = await createComponent(
          createFetchHandler(makeBaseConfig({ scion_build_time: 'unknown' }))
        );
        expect(buildTime(element)?.textContent?.trim()).toBe('unknown');
      } finally {
        setPreferredTimeZone('');
      }
    });

    it('section metadata renders source:File for file-sourced sections', async () => {
      const config = makeBaseConfig({
        settings_tier: 'db',
        section_metadata: {
          lifecycle: { source: 'file' },
        },
      });
      element = await createComponent(createFetchHandler(config));

      const metaEl = query(element, '.section-meta');
      expect(metaEl).not.toBeNull();
      expect(metaEl!.textContent).toContain('File');
    });

    it('section metadata renders source:Default for default-sourced sections', async () => {
      const config = makeBaseConfig({
        settings_tier: 'db',
        section_metadata: {
          agent_defaults: { source: 'default' },
        },
      });
      element = await createComponent(createFetchHandler(config));

      const metaEl = query(element, '.section-meta');
      expect(metaEl).not.toBeNull();
      expect(metaEl!.textContent).toContain('Default');
    });

    it('does not render section metadata when absent (file/SQLite mode)', async () => {
      element = await createComponent(createFetchHandler(makeBaseConfig()));

      const metaEl = query(element, '.section-meta');
      expect(metaEl).toBeNull();
    });
  });

  // ── Criterion 3: DB mode, Layer-1 edit ──

  describe('Criterion 3 — DB mode, Layer-1 fields editable', () => {
    it('Layer-1 fields render as editable inputs (not read-only badges)', async () => {
      const config = makeBaseConfig({ settings_tier: 'db' });
      element = await createComponent(createFetchHandler(config));

      const adminEmailsInput = query(element, 'sl-input[value="admin@example.com"]');
      expect(adminEmailsInput).not.toBeNull();
    });

    it('PUT payload in DB mode includes only Layer-1 keys', async () => {
      const config = makeBaseConfig({ settings_tier: 'db' });
      let capturedPayload: Record<string, unknown> | null = null;

      element = await createComponent(
        createFetchHandler(config, {
          putHandler: (body) => {
            capturedPayload = body;
            return { status: 200, body: { reload: { applied: [] } } };
          },
        })
      );

      const buttons = queryAll(element, 'sl-button[variant="primary"]');
      const saveBtn = buttons.find((b) => b.textContent?.trim() === 'Save & Reload');
      expect(saveBtn).not.toBeNull();
      (saveBtn as HTMLElement).click();
      await new Promise((resolve) => setTimeout(resolve, 300));
      await (element as any).updateComplete;

      expect(capturedPayload).not.toBeNull();
      const server = capturedPayload!.server as Record<string, unknown> | undefined;
      const hub = server?.hub as Record<string, unknown> | undefined;

      // Layer-1 keys should be present
      expect(hub).toBeDefined();
      expect(hub!.admin_emails).toBeDefined();

      // Layer-0 keys must be absent from the payload
      expect(server?.mode).toBeUndefined();
      expect(server?.log_level).toBeUndefined();
      expect(server?.database).toBeUndefined();
      expect(server?.storage).toBeUndefined();
      expect(server?.secrets).toBeUndefined();
      expect(server?.broker).toBeUndefined();
      expect(capturedPayload!.active_profile).toBeUndefined();
    });

    it('PUT in DB mode does not trigger a 422', async () => {
      const config = makeBaseConfig({ settings_tier: 'db' });
      let putCalled = false;

      element = await createComponent(
        createFetchHandler(config, {
          putHandler: () => {
            putCalled = true;
            return { status: 200, body: { reload: { applied: [] } } };
          },
        })
      );

      const buttons = queryAll(element, 'sl-button[variant="primary"]');
      const saveBtn = buttons.find((b) => b.textContent?.trim() === 'Save & Reload');
      (saveBtn as HTMLElement).click();
      await new Promise((resolve) => setTimeout(resolve, 300));

      expect(putCalled).toBe(true);
      expect(query(element, '.safety-net-notice')).toBeNull();
    });
  });

  // ── Criterion 4: DB mode, Layer-0 read-only ──

  describe('Criterion 4 — DB mode, Layer-0 read-only', () => {
    it('Layer-0 fields render as read-only with bootstrap badge', async () => {
      const config = makeBaseConfig({ settings_tier: 'db' });
      element = await createComponent(createFetchHandler(config));

      const badges = queryAll(element, '.read-only-badge');
      expect(badges.length).toBeGreaterThan(0);
      const badgeTexts = badges.map((b) => b.textContent ?? '');
      expect(badgeTexts.some((t) => t.includes('deployment configuration'))).toBe(true);
    });

    it('server.mode renders as read-only value (not editable select)', async () => {
      const config = makeBaseConfig({ settings_tier: 'db' });
      element = await createComponent(createFetchHandler(config));

      const readOnlyValues = queryAll(element, '.read-only-value');
      const values = readOnlyValues.map((el) => el.textContent?.trim());
      expect(values).toContain('standalone');
    });

    it('database.driver renders as read-only in DB mode', async () => {
      const config = makeBaseConfig({ settings_tier: 'db' });
      element = await createComponent(createFetchHandler(config));

      const readOnlyValues = queryAll(element, '.read-only-value');
      const values = readOnlyValues.map((el) => el.textContent?.trim());
      expect(values).toContain('postgres');
    });

    it('log_level renders as read-only in DB mode', async () => {
      const config = makeBaseConfig({ settings_tier: 'db' });
      element = await createComponent(createFetchHandler(config));

      const readOnlyValues = queryAll(element, '.read-only-value');
      const values = readOnlyValues.map((el) => el.textContent?.trim());
      expect(values).toContain('info');
    });

    it('hub.port renders as read-only in DB mode', async () => {
      const config = makeBaseConfig({ settings_tier: 'db' });
      element = await createComponent(createFetchHandler(config));

      const readOnlyValues = queryAll(element, '.read-only-value');
      const values = readOnlyValues.map((el) => el.textContent?.trim());
      expect(values).toContain('8080');
    });

    it('Layer-0 keys are absent from PUT payload in DB mode', async () => {
      const config = makeBaseConfig({ settings_tier: 'db' });
      let capturedPayload: Record<string, unknown> | null = null;

      element = await createComponent(
        createFetchHandler(config, {
          putHandler: (body) => {
            capturedPayload = body;
            return { status: 200, body: { reload: { applied: [] } } };
          },
        })
      );

      const buttons = queryAll(element, 'sl-button[variant="primary"]');
      const saveBtn = buttons.find((b) => b.textContent?.trim() === 'Save & Reload');
      (saveBtn as HTMLElement).click();
      await new Promise((resolve) => setTimeout(resolve, 300));

      expect(capturedPayload).not.toBeNull();
      const server = capturedPayload!.server as Record<string, unknown> | undefined;

      expect(server?.mode).toBeUndefined();
      expect(server?.log_level).toBeUndefined();
      expect(server?.log_format).toBeUndefined();
      expect(server?.database).toBeUndefined();
      expect(server?.storage).toBeUndefined();
      expect(server?.secrets).toBeUndefined();
      expect(server?.broker).toBeUndefined();
      expect(server?.message_broker).toBeUndefined();
    });
  });

  // ── Workstation hubs (layer0_editable): Layer-0 editable on the DB tier ──

  describe('DB mode on a workstation hub (layer0_editable)', () => {
    it('Layer-0 fields are editable: no deployment-configuration badges', async () => {
      const config = makeBaseConfig({ settings_tier: 'db', layer0_editable: true });
      element = await createComponent(createFetchHandler(config));

      const badgeTexts = queryAll(element, '.read-only-badge').map((b) => b.textContent ?? '');
      expect(badgeTexts.some((t) => t.includes('deployment configuration'))).toBe(false);
      const values = queryAll(element, '.read-only-value').map((el) => el.textContent?.trim());
      expect(values).not.toContain('postgres');
      expect(values).not.toContain('8080');
    });

    it('env-pinned fields stay read-only', async () => {
      const config = makeBaseConfig({
        settings_tier: 'db',
        layer0_editable: true,
        env_overrides: ['server.hub.port'],
      });
      element = await createComponent(createFetchHandler(config));

      const badgeTexts = queryAll(element, '.read-only-badge').map((b) => b.textContent ?? '');
      expect(badgeTexts.some((t) => t.includes('environment variable'))).toBe(true);
    });

    async function capturePut(
      config: Record<string, unknown>,
      mutate: (el: any) => void
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
      mutate(element as any);
      const buttons = queryAll(element, 'sl-button[variant="primary"]');
      const saveBtn = buttons.find((b) => b.textContent?.trim() === 'Save & Reload');
      (saveBtn as HTMLElement).click();
      await new Promise((resolve) => setTimeout(resolve, 300));
      expect(captured).not.toBeNull();
      return captured!;
    }

    it('changed Layer-0 fields go in the PUT payload for the server to split', async () => {
      const payload = await capturePut(
        makeBaseConfig({ settings_tier: 'db', layer0_editable: true }),
        (el) => {
          el.logLevel = 'debug';
          el.dbDriver = 'sqlite';
        }
      );
      const server = payload.server as Record<string, unknown> | undefined;
      expect(server?.log_level).toBe('debug');
      expect((server?.database as Record<string, unknown> | undefined)?.driver).toBe('sqlite');
      // The masked database URL is left out so the stored value is kept.
      expect((server?.database as Record<string, unknown> | undefined)?.url).toBeUndefined();
    });

    it('a minimal workstation GET with no user change sends no Layer-0 leaves', async () => {
      // The GET a hub returns for a stock workstation settings.yaml.
      const minimal = {
        schema_version: '1',
        settings_tier: 'db',
        layer0_editable: true,
        server: {
          hub: { soft_delete_retain_files: false, auto_suspend_stalled: false },
          broker: { broker_id: 'b-1', broker_token: '********' },
          auth: {},
          github_app: {},
        },
      };
      const payload = await capturePut(minimal, () => {});
      expect(payload).not.toHaveProperty('active_profile');
      expect(payload).not.toHaveProperty('workspace_path');
      const server = (payload.server ?? {}) as Record<string, Record<string, unknown>>;
      for (const block of [
        'broker',
        'database',
        'storage',
        'secrets',
        'message_broker',
        'native_chat',
      ]) {
        expect(server).not.toHaveProperty(block);
      }
      expect(server.hub ?? {}).not.toHaveProperty('port');
      expect(server.auth ?? {}).not.toHaveProperty('dev_token');
      expect(server).not.toHaveProperty('log_format');
    });

    it('changing one Layer-0 field sends exactly that leaf', async () => {
      const minimal = {
        schema_version: '1',
        settings_tier: 'db',
        layer0_editable: true,
        server: { hub: {}, broker: { broker_id: 'b-1', broker_token: '********' }, auth: {} },
      };
      const payload = await capturePut(minimal, (el) => {
        el.storageBucket = 'my-bucket';
      });
      const server = payload.server as Record<string, Record<string, unknown>>;
      expect(server.storage).toEqual({ bucket: 'my-bucket' });
      expect(server).not.toHaveProperty('broker');
      expect(payload).not.toHaveProperty('active_profile');
    });

    it('auto_provide shows as on when GET omits it', async () => {
      element = await createComponent(
        createFetchHandler({
          schema_version: '1',
          settings_tier: 'db',
          layer0_editable: true,
          server: { broker: { broker_id: 'b-1' } },
        })
      );
      expect((element as any).brokerAutoProvide).toBe(true);
    });

    it('clearing authorized_domains and public_url sends [] and ""', async () => {
      const payload = await capturePut(
        makeBaseConfig({ settings_tier: 'db', layer0_editable: true }),
        (el) => {
          el.authAuthorizedDomains = '';
          el.hubPublicUrl = '';
        }
      );
      const server = payload.server as Record<string, Record<string, unknown>>;
      expect(server.auth.authorized_domains).toEqual([]);
      expect(server.hub.public_url).toBe('');
      // The unchanged Layer-0 port is not sent.
      expect(server.hub).not.toHaveProperty('port');
    });

    it('cleared and switched-off Layer-0 fields are sent explicitly', async () => {
      const base = makeBaseConfig({ settings_tier: 'db', layer0_editable: true }) as any;
      base.server.storage.bucket = 'old-bucket';
      base.server.message_broker = { enabled: true, type: 'inprocess' };
      const payload = await capturePut(base, (el) => {
        el.logLevel = '';
        el.storageBucket = '';
        el.messageBrokerEnabled = false;
      });
      const server = payload.server as Record<string, Record<string, unknown> | string>;
      expect(server.log_level).toBe('');
      expect(server.storage).toEqual({ bucket: '' });
      expect(server.message_broker).toEqual({ enabled: false });
    });

    it('defaulted fields GET omitted are not sent from an untouched form', async () => {
      // makeBaseConfig has no gcp_iam_* and no native_chat.
      const payload = await capturePut(
        makeBaseConfig({ settings_tier: 'db', layer0_editable: true }),
        () => {}
      );
      const server = payload.server as Record<string, Record<string, unknown>>;
      expect(server.hub).not.toHaveProperty('gcp_iam_check_mode');
      expect(server.hub).not.toHaveProperty('gcp_iam_deny_unknown_policy');
      expect(server).not.toHaveProperty('native_chat');
    });

    it('defaulted fields are sent only when the user changed them', async () => {
      const base = makeBaseConfig({ settings_tier: 'db', layer0_editable: true }) as any;
      base.server.hub.gcp_iam_check_mode = 'enforce';
      const payload = await capturePut(base, (el) => {
        el.nativeChatEnabled = false;
      });
      const server = payload.server as Record<string, Record<string, unknown>>;
      // Unchanged since GET (even though GET had it): not sent.
      expect(server.hub).not.toHaveProperty('gcp_iam_check_mode');
      expect(server.hub).not.toHaveProperty('gcp_iam_deny_unknown_policy');
      expect(server.native_chat).toEqual({ enabled: false });
    });

    it('a hosted save leaves out unchanged gcp_iam keys', async () => {
      const base = makeBaseConfig({ settings_tier: 'db' }) as any;
      base.server.hub.gcp_iam_check_mode = 'enforce';
      base.server.hub.gcp_iam_deny_unknown_policy = 'fail-closed';
      const payload = await capturePut(base, () => {});
      const hub = (payload.server as Record<string, Record<string, unknown>> | undefined)?.hub;
      expect(hub ?? {}).not.toHaveProperty('gcp_iam_check_mode');
      expect(hub ?? {}).not.toHaveProperty('gcp_iam_deny_unknown_policy');
    });

    it('a hosted save sends a changed gcp_iam key in the Layer-1 payload', async () => {
      const base = makeBaseConfig({ settings_tier: 'db' }) as any;
      base.server.hub.gcp_iam_check_mode = 'enforce';
      const payload = await capturePut(base, (el) => {
        el.hubGcpIamDenyUnknownPolicy = 'fail-closed';
      });
      const hub = (payload.server as Record<string, Record<string, unknown>>).hub;
      expect(hub.gcp_iam_deny_unknown_policy).toBe('fail-closed');
      expect(hub).not.toHaveProperty('gcp_iam_check_mode');
    });

    it('gcp_iam selects show the reported value and an env badge when env-pinned', async () => {
      const base = makeBaseConfig({
        settings_tier: 'db',
        env_overrides: ['server.hub.gcp_iam_check_mode'],
      }) as any;
      base.server.hub.gcp_iam_check_mode = 'enforce';
      element = await createComponent(createFetchHandler(base));
      const label = queryAll(element, 'label').find(
        (l) => l.textContent?.trim() === 'IAM Check Mode'
      );
      const field = label!.closest('.form-field')!;
      expect(field.querySelector('sl-select')?.getAttribute('value')).toBe('enforce');
      expect(field.querySelector('.env-badge')).not.toBeNull();
    });

    it('gcp_iam selects are read-only on a hosted hub without the gcp_iam section', async () => {
      const base = makeBaseConfig({ settings_tier: 'db' }) as any;
      base.server.hub.gcp_iam_check_mode = 'enforce';
      const schema = JSON.parse(JSON.stringify(SCHEMA_RESPONSE));
      delete schema.sections.gcp_iam;
      element = await createComponent(createFetchHandler(base, { schemaResponse: schema }));
      const labels = queryAll(element, 'label').filter((l) =>
        ['IAM Check Mode', 'Deny Policy Fallback'].includes(l.textContent?.trim() ?? '')
      );
      expect(labels).toHaveLength(2);
      for (const label of labels) {
        const field = label.closest('.form-field')!;
        expect(field.querySelector('sl-select')).toBeNull();
        expect(field.querySelector('.read-only-badge')?.textContent).toContain(
          'deployment configuration'
        );
      }
    });

    it('GCP replication locations are read-only on a hosted hub', async () => {
      const base = makeBaseConfig({ settings_tier: 'db' }) as any;
      base.server.secrets = { backend: 'gcpsm', gcp_replication_locations: ['us-east1'] };
      element = await createComponent(createFetchHandler(base));
      const label = queryAll(element, 'label').find(
        (l) => l.textContent?.trim() === 'GCP Replication Locations'
      );
      expect(label).toBeDefined();
      const field = label!.closest('.form-field')!;
      expect(field.querySelector('sl-input')).toBeNull();
      expect(field.querySelector('.read-only-badge')?.textContent).toContain(
        'deployment configuration'
      );
      expect(field.querySelector('.read-only-value')?.textContent).toContain('us-east1');
    });

    it('GCP replication locations stay editable on a workstation hub', async () => {
      const base = makeBaseConfig({ settings_tier: 'db', layer0_editable: true }) as any;
      base.server.secrets = { backend: 'gcpsm', gcp_replication_locations: ['us-east1'] };
      element = await createComponent(createFetchHandler(base));
      const label = queryAll(element, 'label').find(
        (l) => l.textContent?.trim() === 'GCP Replication Locations'
      );
      const field = label!.closest('.form-field')!;
      expect(field.querySelector('sl-input')).not.toBeNull();
    });

    it('flag-managed workstation fields are read-only and not sent', async () => {
      const config = makeBaseConfig({ settings_tier: 'db', layer0_editable: true });
      const payload = await capturePut(config, () => {});
      const badgeTexts = queryAll(element!, '.read-only-badge').map((b) => b.textContent ?? '');
      expect(badgeTexts.some((t) => t.includes('workstation startup defaults'))).toBe(true);
      const server = payload.server as Record<string, Record<string, unknown>>;
      expect(server.broker?.enabled).toBeUndefined();
      expect(server.auth?.dev_mode).toBeUndefined();
      expect(server.storage?.provider).toBeUndefined();
      expect(server.hub?.host).toBeUndefined();
    });
  });
});
