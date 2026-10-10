/**
 * Save payloads (cleared, unset and omitted fields), messaging, role help and agent secrets.
 *
 * One part of the "scion-page-admin-server-config" suite. The suite is split
 * across admin-server-config.*.test.ts so its sections run in parallel
 * workers; shared helpers live in ./__fixtures__/admin-server-config.ts.
 */

import { describe, it, expect, vi, beforeAll, afterAll, afterEach } from 'vitest';
import {
  makeBaseConfig,
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

  describe('Regression ptone/scion#2535 — clearing string fields in file mode', () => {
    const clearable: Array<[string, string]> = [
      ['active_profile', 'activeProfile'],
      ['default_template', 'defaultTemplate'],
      // default_harness_auth and the agent role defaults are not sent at all
      // until ptone/scion#3067 is fixed (see the #3067 describe block).
      ['image_registry', 'imageRegistry'],
      ['workspace_path', 'workspacePath'],
      ['default_runtime_broker', 'defaultRuntimeBroker'],
    ];

    it('buildFilePayload sends cleared string fields as "" (not omitted)', async () => {
      element = await createComponent(
        createFetchHandler(makeBaseConfig({ settings_tier: 'file' }))
      );
      const el = element as any;
      for (const [, prop] of clearable) el[prop] = '';
      el.harnessConfigSelection = '';
      el.customHarnessConfig = '';

      const payload = el.buildFilePayload() as Record<string, unknown>;
      for (const [key] of clearable) {
        expect(payload, key).toHaveProperty(key, '');
      }
      expect(payload).toHaveProperty('default_harness_config', '');
    });

    it('buildFilePayload still sends non-empty string values', async () => {
      element = await createComponent(
        createFetchHandler(makeBaseConfig({ settings_tier: 'file' }))
      );
      const el = element as any;
      for (const [, prop] of clearable) el[prop] = `v-${prop}`;
      el.harnessConfigSelection = '__other__';
      el.customHarnessConfig = 'x';

      const payload = el.buildFilePayload() as Record<string, unknown>;
      for (const [key, prop] of clearable) {
        expect(payload, key).toHaveProperty(key, `v-${prop}`);
      }
      expect(payload).toHaveProperty('default_harness_config', 'x');
    });

    it('buildFilePayload sends GCP identity defaults when set', async () => {
      element = await createComponent(
        createFetchHandler(makeBaseConfig({ settings_tier: 'file' }))
      );
      const el = element as any;
      el.defaultGCPIdentityMode = 'assign';
      el.defaultGCPIdentitySAID = 'sa-123';

      const payload = el.buildFilePayload() as Record<string, unknown>;
      expect(payload).toHaveProperty('default_gcp_identity_mode', 'assign');
      expect(payload).toHaveProperty('default_gcp_identity_service_account_id', 'sa-123');
    });

    it('buildFilePayload sends cleared GCP identity defaults as ""', async () => {
      element = await createComponent(
        createFetchHandler(makeBaseConfig({ settings_tier: 'file' }))
      );
      const el = element as any;
      el.defaultGCPIdentityMode = '';
      el.defaultGCPIdentitySAID = '';

      const payload = el.buildFilePayload() as Record<string, unknown>;
      expect(payload).toHaveProperty('default_gcp_identity_mode', '');
      expect(payload).toHaveProperty('default_gcp_identity_service_account_id', '');
    });

    it('buildFilePayload clears the GCP service account when mode is not assign', async () => {
      element = await createComponent(
        createFetchHandler(makeBaseConfig({ settings_tier: 'file' }))
      );
      const el = element as any;
      el.defaultGCPIdentityMode = 'block';
      el.defaultGCPIdentitySAID = 'stale-sa';

      const payload = el.buildFilePayload() as Record<string, unknown>;
      expect(payload).toHaveProperty('default_gcp_identity_mode', 'block');
      expect(payload).toHaveProperty('default_gcp_identity_service_account_id', '');
    });

    it('buildFilePayload clears the GCP service account when mode is empty', async () => {
      element = await createComponent(
        createFetchHandler(makeBaseConfig({ settings_tier: 'file' }))
      );
      const el = element as any;
      el.defaultGCPIdentityMode = '';
      el.defaultGCPIdentitySAID = 'stale-sa';

      const payload = el.buildFilePayload() as Record<string, unknown>;
      expect(payload).toHaveProperty('default_gcp_identity_mode', '');
      expect(payload).toHaveProperty('default_gcp_identity_service_account_id', '');
    });

    it('buildFilePayload omits an env-pinned default_gcp_identity_mode', async () => {
      element = await createComponent(
        createFetchHandler(
          makeBaseConfig({
            settings_tier: 'file',
            env_overrides: ['default_gcp_identity_mode'],
          })
        )
      );
      const el = element as any;
      el.defaultGCPIdentityMode = 'assign';
      el.defaultGCPIdentitySAID = 'sa-123';

      const payload = el.buildFilePayload() as Record<string, unknown>;
      expect(payload).not.toHaveProperty('default_gcp_identity_mode');
      expect(payload).toHaveProperty('default_gcp_identity_service_account_id', 'sa-123');
    });

    // ptone/scion#2720: with the mode env-pinned, the form mode holds the
    // settings-file value, not the effective one, so it must not drive
    // clearing of the account. An unchanged account is left out of the
    // payload, so the server neither clears nor re-checks it.
    const pinnedFileConfig = (formMode: string, said: string) =>
      makeBaseConfig({
        settings_tier: 'file',
        env_overrides: ['default_gcp_identity_mode'],
        default_gcp_identity_mode: formMode,
        default_gcp_identity_service_account_id: said,
      });

    it.each(['', 'block', 'passthrough', 'assign'])(
      'buildFilePayload omits an unchanged GCP service account when the mode is env-pinned (form mode=%j)',
      async (formMode) => {
        element = await createComponent(createFetchHandler(pinnedFileConfig(formMode, 'sa-123')));
        const el = element as any;
        expect(el.defaultGCPIdentitySAID).toBe('sa-123');

        const payload = el.buildFilePayload() as Record<string, unknown>;
        expect(payload).not.toHaveProperty('default_gcp_identity_mode');
        expect(payload).not.toHaveProperty('default_gcp_identity_service_account_id');
      }
    );

    it.each([
      ['changed', 'sa-456'],
      ['cleared', ''],
    ])(
      'buildFilePayload sends an edited GCP service account when the mode is env-pinned (%s)',
      async (_label, edited) => {
        element = await createComponent(createFetchHandler(pinnedFileConfig('assign', 'sa-123')));
        const el = element as any;
        el.defaultGCPIdentitySAID = edited;

        const payload = el.buildFilePayload() as Record<string, unknown>;
        expect(payload).not.toHaveProperty('default_gcp_identity_mode');
        expect(payload).toHaveProperty('default_gcp_identity_service_account_id', edited);
      }
    );

    it('buildFilePayload compares the GCP service account with the latest load when the mode is env-pinned', async () => {
      element = await createComponent(createFetchHandler(pinnedFileConfig('assign', 'sa-123')));
      const el = element as any;
      expect(el.defaultGCPIdentitySAID).toBe('sa-123');

      // Reload (as after a save) with a different stored account.
      vi.stubGlobal('fetch', vi.fn(createFetchHandler(pinnedFileConfig('assign', 'sa-789'))));
      await el.loadConfig();
      await el.updateComplete;
      expect(el.defaultGCPIdentitySAID).toBe('sa-789');
      expect(el.readOnlyReason('default_gcp_identity_mode')).not.toBeNull();

      // Unchanged since the reload: omitted.
      let payload = el.buildFilePayload() as Record<string, unknown>;
      expect(payload).not.toHaveProperty('default_gcp_identity_mode');
      expect(payload).not.toHaveProperty('default_gcp_identity_service_account_id');

      // Back to the first-load value: now an edit, so it is sent.
      el.defaultGCPIdentitySAID = 'sa-123';
      payload = el.buildFilePayload() as Record<string, unknown>;
      expect(payload).not.toHaveProperty('default_gcp_identity_mode');
      expect(payload).toHaveProperty('default_gcp_identity_service_account_id', 'sa-123');
    });

    it.each([
      ['assign', 'sa-123'],
      ['block', ''],
    ])(
      'buildFilePayload sends the loaded GCP service account when the mode is editable (mode=%j)',
      async (mode, expected) => {
        element = await createComponent(
          createFetchHandler(
            makeBaseConfig({
              settings_tier: 'file',
              default_gcp_identity_mode: mode,
              default_gcp_identity_service_account_id: 'sa-123',
            })
          )
        );
        const el = element as any;

        const payload = el.buildFilePayload() as Record<string, unknown>;
        expect(payload).toHaveProperty('default_gcp_identity_mode', mode);
        expect(payload).toHaveProperty('default_gcp_identity_service_account_id', expected);
      }
    );

    // The db-tier test schema does not list the GCP identity keys, so the
    // tests set the Layer-1 key set the page uses to decide editability.
    const GCP_KEYS = ['default_gcp_identity_mode', 'default_gcp_identity_service_account_id'];

    it('buildLayer1Payload sends the GCP service account in assign mode', async () => {
      element = await createComponent(createFetchHandler(makeBaseConfig({ settings_tier: 'db' })));
      const el = element as any;
      el.layer1Keys = new Set(GCP_KEYS);
      el.defaultGCPIdentityMode = 'assign';
      el.defaultGCPIdentitySAID = 'sa-123';

      const payload = el.buildLayer1Payload() as Record<string, unknown>;
      expect(payload).toHaveProperty('default_gcp_identity_mode', 'assign');
      expect(payload).toHaveProperty('default_gcp_identity_service_account_id', 'sa-123');
    });

    it('buildLayer1Payload clears the GCP service account when mode is not assign', async () => {
      element = await createComponent(createFetchHandler(makeBaseConfig({ settings_tier: 'db' })));
      const el = element as any;
      el.layer1Keys = new Set(GCP_KEYS);
      el.defaultGCPIdentityMode = 'block';
      el.defaultGCPIdentitySAID = 'stale-sa';

      const payload = el.buildLayer1Payload() as Record<string, unknown>;
      expect(payload).toHaveProperty('default_gcp_identity_mode', 'block');
      expect(payload).toHaveProperty('default_gcp_identity_service_account_id', '');
    });

    // In the db tier env vars do not lock Layer-1 fields. Both GCP keys
    // are in one settings section, so they are Layer-1 together or
    // deployment-managed together; the page never reaches the read-only
    // mode branch of the account helper there. When both are locked,
    // neither key is sent.
    it('buildLayer1Payload omits both GCP keys when they are not Layer-1', async () => {
      element = await createComponent(createFetchHandler(makeBaseConfig({ settings_tier: 'db' })));
      const el = element as any;
      el.layer1Keys = new Set();
      el.defaultGCPIdentityMode = 'assign';
      el.defaultGCPIdentitySAID = 'sa-123';

      const payload = el.buildLayer1Payload() as Record<string, unknown>;
      expect(payload).not.toHaveProperty('default_gcp_identity_mode');
      expect(payload).not.toHaveProperty('default_gcp_identity_service_account_id');
    });
  });

  describe('unset values are sent as null (ptone/scion#3898)', () => {
    // A DB-backed save keeps the stored value of a key the body leaves
    // out, so an unset value must be sent as an explicit null to clear it.
    const KEYS = [
      'default_thinking_level',
      'default_resources',
      'telemetry.enabled',
      'telemetry.cloud.enabled',
    ];

    it('buildLayer1Payload sends null for an unset thinking level, never 0', async () => {
      element = await createComponent(createFetchHandler(makeBaseConfig({ settings_tier: 'db' })));
      const el = element as any;
      el.layer1Keys = new Set(KEYS);
      el.defaultThinkingLevel = null;
      expect(el.buildLayer1Payload()).toHaveProperty('default_thinking_level', null);

      el.defaultThinkingLevel = 30;
      expect(el.buildLayer1Payload()).toHaveProperty('default_thinking_level', 30);
    });

    it('treats a loaded thinking level of 0 as unset', async () => {
      element = await createComponent(
        createFetchHandler(makeBaseConfig({ settings_tier: 'db', default_thinking_level: 0 }))
      );
      const el = element as any;
      el.layer1Keys = new Set(KEYS);
      expect(el.defaultThinkingLevel).toBeNull();
      expect(el.buildLayer1Payload()).toHaveProperty('default_thinking_level', null);
    });

    it('buildFilePayload sends null for an unset thinking level, never 0', async () => {
      element = await createComponent(
        createFetchHandler(makeBaseConfig({ settings_tier: 'file' }))
      );
      const el = element as any;
      el.defaultThinkingLevel = null;
      expect(el.buildFilePayload()).toHaveProperty('default_thinking_level', null);
    });

    it('buildLayer1Payload sends null default_resources when every resource field is empty', async () => {
      element = await createComponent(createFetchHandler(makeBaseConfig({ settings_tier: 'db' })));
      const el = element as any;
      el.layer1Keys = new Set(KEYS);
      el.defaultResCpuReq = '';
      el.defaultResMemReq = '';
      el.defaultResCpuLim = '';
      el.defaultResMemLim = '';
      el.defaultResDisk = '';
      expect(el.buildLayer1Payload()).toHaveProperty('default_resources', null);

      el.defaultResCpuReq = '500m';
      expect(el.buildLayer1Payload().default_resources).toEqual({
        requests: { cpu: '500m', memory: undefined },
      });
    });

    it('buildLayer1Payload sends null for an empty telemetry cloud GCP project ID', async () => {
      element = await createComponent(createFetchHandler(makeBaseConfig({ settings_tier: 'db' })));
      const el = element as any;
      el.layer1Keys = new Set(KEYS);
      el.telemetryCloudGcpProjectId = '';
      const cloud = (el.buildLayer1Payload().telemetry as Record<string, unknown>).cloud as Record<
        string,
        unknown
      >;
      expect(cloud).toHaveProperty('gcp_project_id', null);
    });
  });

  describe('telemetry.local is not edited (ptone/scion#4103)', () => {
    // Nothing reads telemetry.local, so the page shows no controls for it.
    // A DB-backed save merges key by key, so the omitted key keeps its stored
    // value; a file-mode save replaces the whole telemetry object, so the
    // stored value is echoed back unchanged.
    const storedLocal = { enabled: true, file: '/tmp/t.jsonl', console: true };
    const withLocal = (tier: string) =>
      makeBaseConfig({
        settings_tier: tier,
        telemetry: {
          enabled: true,
          cloud: { enabled: false },
          hub: { enabled: true },
          local: storedLocal,
        },
      });

    it('renders no Local Debug Output controls', async () => {
      element = await createComponent(createFetchHandler(withLocal('db')));
      expect(shadowText(element)).toContain('Report Interval');
      expect(shadowText(element)).not.toContain('Local Debug Output');
      expect(shadowText(element)).not.toContain('Enable Local Output');
    });

    it('buildLayer1Payload omits telemetry.local', async () => {
      element = await createComponent(createFetchHandler(withLocal('db')));
      const el = element as any;
      el.layer1Keys = new Set([
        'telemetry.enabled',
        'telemetry.hub.enabled',
        'telemetry.local.enabled',
        'telemetry.local.file',
        'telemetry.local.console',
      ]);
      const telemetry = el.buildLayer1Payload().telemetry as Record<string, unknown>;
      expect(telemetry).toHaveProperty('hub');
      expect(telemetry).not.toHaveProperty('local');
    });

    it('buildFilePayload echoes the stored telemetry.local unchanged', async () => {
      element = await createComponent(createFetchHandler(withLocal('file')));
      const el = element as any;
      const telemetry = el.buildFilePayload().telemetry as Record<string, unknown>;
      expect(telemetry).toHaveProperty('hub');
      expect(telemetry.local).toEqual(storedLocal);
    });

    it('buildFilePayload sends no telemetry.local when none is stored', async () => {
      element = await createComponent(
        createFetchHandler(
          makeBaseConfig({
            settings_tier: 'file',
            telemetry: { enabled: true, cloud: { enabled: false }, hub: { enabled: true } },
          })
        )
      );
      const el = element as any;
      const telemetry = el.buildFilePayload().telemetry as Record<string, unknown>;
      expect(telemetry).toHaveProperty('hub');
      expect(telemetry).not.toHaveProperty('local');
    });
  });

  describe('server.log_format is not edited (ptone/scion#4103)', () => {
    // server.log_format is accepted but ignored; guard against the no-op
    // Log Format control coming back.
    it('renders no Log Format control', async () => {
      element = await createComponent(createFetchHandler(makeBaseConfig({ settings_tier: 'db' })));
      expect(shadowText(element)).toContain('Log Level');
      expect(shadowText(element)).not.toContain('Log Format');
    });

    it('DB-mode builders never send server.log_format', async () => {
      // buildLayer1Payload carries no server leaves; on a workstation hub the
      // server fields go through buildLayer0Candidate instead.
      element = await createComponent(
        createFetchHandler(makeBaseConfig({ settings_tier: 'db', layer0_editable: true }))
      );
      const el = element as any;
      el.logLevel = 'debug';
      const layer1Server = (el.buildLayer1Payload().server ?? {}) as Record<string, unknown>;
      expect(layer1Server).not.toHaveProperty('log_format');
      const server = el.buildLayer0Candidate().server as Record<string, unknown>;
      expect(server).toHaveProperty('log_level', 'debug');
      expect(server).not.toHaveProperty('log_format');
    });

    it('buildFilePayload never sends server.log_format', async () => {
      element = await createComponent(
        createFetchHandler(makeBaseConfig({ settings_tier: 'file' }))
      );
      const el = element as any;
      el.logLevel = 'debug';
      const server = el.buildFilePayload().server as Record<string, unknown>;
      expect(server).toHaveProperty('log_level', 'debug');
      expect(server).not.toHaveProperty('log_format');
    });
  });

  // ── Cross-project messaging (D1) ──

  describe('File mode server sections keep omitted fields (ptone/scion#2938)', () => {
    // The file-mode PUT deep-merges each server section, so an omitted
    // field keeps its stored value; a field the form shows must be sent as
    // an explicit empty value to be cleared.
    it('buildFilePayload sends cleared server fields as explicit empties', async () => {
      element = await createComponent(
        createFetchHandler(makeBaseConfig({ settings_tier: 'file' }))
      );
      const el = element as any;
      el.logLevel = '';
      el.hubPort = 0;
      el.hubHost = '';
      el.hubPublicUrl = '';
      el.brokerHost = '';
      el.brokerPort = 0;
      el.dbDriver = '';
      el.dbUrl = '';
      el.authDevToken = '';
      el.storageBucket = '';
      el.secretsBackend = '';
      el.secretsGCPProjectId = '';

      const server = (el.buildFilePayload() as Record<string, any>).server;
      expect(server.log_level).toBe('');
      expect(server.hub).toMatchObject({ port: 0, host: '', public_url: '' });
      expect(server.broker).toMatchObject({ port: 0, host: '' });
      expect(server.database).toEqual({ driver: '', url: '' });
      expect(server.auth).toHaveProperty('dev_token', '');
      expect(server.storage).toHaveProperty('bucket', '');
      expect(server.secrets).toMatchObject({ backend: '', gcp_project_id: '' });
    });

    it('buildFilePayload leaves out masked credentials so the stored values are kept', async () => {
      element = await createComponent(
        createFetchHandler(makeBaseConfig({ settings_tier: 'file' }))
      );
      const el = element as any;
      el.dbUrl = '********';
      el.authDevToken = '********';

      const server = (el.buildFilePayload() as Record<string, any>).server;
      expect(server.database).not.toHaveProperty('url');
      expect(server.auth).not.toHaveProperty('dev_token');
    });
  });

  describe('Cross-project messaging section', () => {
    it('renders cross-project messaging section in hub server tab', async () => {
      element = await createComponent(
        createFetchHandler(makeBaseConfig(), {
          messagingResponse: {
            cross_project_messaging_enabled: false,
            revision: 1,
          },
        })
      );

      const text = shadowText(element);
      expect(text).toContain('Cross-Project Agent Messaging');
    });

    it('calls the messaging API on connect', async () => {
      element = await createComponent(
        createFetchHandler(makeBaseConfig(), {
          messagingResponse: {
            cross_project_messaging_enabled: true,
            revision: 3,
          },
        })
      );

      expect(vi.mocked(fetch)).toHaveBeenCalledWith(
        expect.stringContaining('/api/v1/admin/messaging'),
        expect.any(Object)
      );
    });

    it('shows the revision number', async () => {
      element = await createComponent(
        createFetchHandler(makeBaseConfig(), {
          messagingResponse: {
            cross_project_messaging_enabled: false,
            revision: 7,
          },
        })
      );

      const text = shadowText(element);
      expect(text).toContain('Revision: 7');
    });

    it('sends CAS revision on save', async () => {
      let capturedPayload: Record<string, unknown> | null = null;
      element = await createComponent(
        createFetchHandler(makeBaseConfig(), {
          messagingResponse: {
            cross_project_messaging_enabled: false,
            revision: 5,
          },
          putHandler: (body) => {
            capturedPayload = body;
            return {
              status: 200,
              body: { cross_project_messaging_enabled: true, revision: 6 },
            };
          },
        })
      );

      // Wait for messaging settings to load
      await new Promise((resolve) => setTimeout(resolve, 300));
      await (element as any).updateComplete;

      // Trigger save by calling the method directly
      await (element as any).saveCrossProjectMessaging(true);
      await (element as any).updateComplete;

      expect(capturedPayload).not.toBeNull();
      expect(capturedPayload!.expected_revision).toBe(5);
      expect(capturedPayload!.cross_project_messaging_enabled).toBe(true);
    });

    it('handles 409 conflict by reloading', async () => {
      let putCallCount = 0;
      element = await createComponent(
        createFetchHandler(makeBaseConfig(), {
          messagingResponse: {
            cross_project_messaging_enabled: false,
            revision: 5,
          },
          putHandler: () => {
            putCallCount++;
            return {
              status: 409,
              body: { error: 'conflict' },
            };
          },
        })
      );

      await new Promise((resolve) => setTimeout(resolve, 300));
      await (element as any).updateComplete;

      await (element as any).saveCrossProjectMessaging(true);
      await new Promise((resolve) => setTimeout(resolve, 300));
      await (element as any).updateComplete;

      expect(putCallCount).toBe(1);
      // Error message should indicate conflict
      expect((element as any).crossProjectMessagingError).toContain('another administrator');
    });
  });

  // ── Default User Role help text (default_user_role, design §5.F) ──

  describe('Default User Role help text', () => {
    // The tests here that only read the default-config page share this one
    // mount instead of mounting the page for each test.
    let defaultMount: HTMLElement;
    beforeAll(async () => {
      defaultMount = await createComponent(createFetchHandler(makeBaseConfig()));
    });
    afterAll(() => {
      defaultMount.remove();
    });

    function helpText(el: HTMLElement): string {
      return (query(el, '.default-user-role-help')?.textContent ?? '').replace(/\s+/g, ' ').trim();
    }

    it('renders the final explanation of the setting', () => {
      const element = defaultMount;

      const text = helpText(element);
      expect(text).toContain('Default role for new users.');
      expect(text).toContain(
        'Applies when a user first signs in, including invited and allow-listed users ' +
          '(their role is assigned at first sign-in, not when the invite is created).'
      );
      expect(text).toContain('Changing it does not affect users who have already signed in.');
      expect(text).not.toContain('invite, or allow-list entry');
      expect(text).toContain('Users listed in Admin Emails are always admins.');
      expect(text).toContain(
        'Member: can create projects, and works in any project they are added to.'
      );
      expect(text).toContain(
        'Viewer: the same as Member, but cannot create projects (including cloning). ' +
          'Viewers can still be added to projects and work there according to their project role.'
      );
      expect(text).toContain(
        'This is also the role given to an admin who is removed from Admin Emails.'
      );
      expect(text).toContain("Change an individual user's role on Admin > Users.");
    });

    it('links to Admin > Users', () => {
      const element = defaultMount;

      expect(query(element, '.default-user-role-help a[href="/admin/users"]')).not.toBeNull();
    });

    it('no longer shows the old one-line hint', () => {
      const element = defaultMount;

      expect(shadowText(element)).not.toContain(
        'Role assigned to new users who are not in the admin emails list.'
      );
    });
  });

  // ── Agent Secrets card (design ptone/scion#2291 §8, §10 test 10) ──

  describe('Agent Secrets card', () => {
    // The tests here that only read the default-config page share this one
    // mount instead of mounting the page for each test.
    let defaultMount: HTMLElement;
    beforeAll(async () => {
      defaultMount = await createComponent(createFetchHandler(makeBaseConfig()));
    });
    afterAll(() => {
      defaultMount.remove();
    });

    function agentSecretsSwitch(el: HTMLElement): HTMLElement | undefined {
      return queryAll(el, 'sl-switch').find((s) =>
        (s.textContent ?? '').includes('Restrict agent-written secrets to profile scope')
      ) as HTMLElement | undefined;
    }

    it('shows the card with its explanatory hint', () => {
      const element = defaultMount;

      expect(shadowText(element)).toContain('Agent Secrets');
      expect(shadowText(element)).toContain(
        'Project-scope writes from agents are rejected. Existing project secrets are'
      );
      expect(shadowText(element)).toContain('not removed. Users can still manage project secrets.');
    });

    it('switch loads unchecked when agent_secrets is absent', () => {
      const element = defaultMount;

      const sw = agentSecretsSwitch(element);
      expect(sw).not.toBeUndefined();
      expect(sw!.hasAttribute('checked')).toBe(false);
    });

    it('switch loads checked when agent_secrets.user_scope_only is true', async () => {
      const config = makeBaseConfig({ agent_secrets: { user_scope_only: true } });
      element = await createComponent(createFetchHandler(config));

      const sw = agentSecretsSwitch(element);
      expect(sw).not.toBeUndefined();
      expect(sw!.hasAttribute('checked')).toBe(true);
    });

    // Round-1 review R2: parameterised over both settings tiers, since
    // 'file' alone only exercises buildFilePayload() — settingsTier === 'db'
    // is what routes save through the separate buildLayer1Payload() builder
    // (admin-server-config.ts's handleSave: `this.settingsTier === 'db' ?
    // this.buildLayer1Payload() : this.buildFilePayload()`).
    it.each(['file', 'db'] as const)(
      'both payload builders send agent_secrets.user_scope_only on save (settings_tier=%s)',
      async (settingsTier) => {
        let capturedPayload: Record<string, unknown> | null = null;
        const config = makeBaseConfig({
          settings_tier: settingsTier,
          agent_secrets: { user_scope_only: true },
        });

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
        const agentSecrets = capturedPayload!.agent_secrets as Record<string, unknown> | undefined;
        expect(agentSecrets?.user_scope_only).toBe(true);
      }
    );

    it('env-overridden agent_secrets.user_scope_only renders read-only with env badge', async () => {
      const config = makeBaseConfig({
        settings_tier: 'file',
        agent_secrets: { user_scope_only: true },
        env_overrides: ['agent_secrets.user_scope_only'],
      });
      element = await createComponent(createFetchHandler(config));

      const badges = queryAll(element, '.read-only-badge');
      const badgeTexts = badges.map((b) => b.textContent ?? '');
      expect(badgeTexts.some((t) => t.includes('environment variable'))).toBe(true);

      // The switch itself must not render while the field is env-pinned.
      expect(agentSecretsSwitch(element)).toBeUndefined();
    });
  });
});
