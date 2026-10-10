/**
 * Save errors, file mode, the default_timezone card, and structured error handling.
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

  describe('agent-default fields the hub cannot save (ptone/scion#3067)', () => {
    for (const tier of [
      { settings_tier: 'db' },
      { settings_tier: 'db', layer0_editable: true },
      { settings_tier: 'file' },
    ]) {
      it(`are not sent and not editable (${JSON.stringify(tier)})`, async () => {
        const config = makeBaseConfig({ ...tier, default_agent_role: 'full' });
        let captured: Record<string, unknown> | null = null;
        element = await createComponent(
          createFetchHandler(config, {
            putHandler: (body) => {
              captured = body;
              return { status: 200, body: { reload: { applied: [] } } };
            },
          })
        );
        expect(shadowText(element)).toContain('ptone/scion#3067');
        const buttons = queryAll(element, 'sl-button[variant="primary"]');
        (buttons.find((b) => b.textContent?.trim() === 'Save & Reload') as HTMLElement).click();
        await new Promise((resolve) => setTimeout(resolve, 300));
        expect(captured).not.toBeNull();
        expect(captured!).not.toHaveProperty('default_agent_role');
        expect(captured!).not.toHaveProperty('default_max_agent_role');
        expect(captured!).not.toHaveProperty('default_harness_auth');
      });
    }
  });

  describe('save errors that name keys', () => {
    for (const code of ['unpersisted_keys_rejected', 'unclassified_keys_rejected']) {
      it(`${code} shows the message and the keys`, async () => {
        element = await createComponent(
          createFetchHandler(makeBaseConfig({ settings_tier: 'db' }), {
            putHandler: () => ({
              status: 422,
              body: { error: code, message: 'Not saved.', keys: ['server.hub.bogus', 'x.y'] },
            }),
          })
        );
        const buttons = queryAll(element, 'sl-button[variant="primary"]');
        (buttons.find((b) => b.textContent?.trim() === 'Save & Reload') as HTMLElement).click();
        await new Promise((resolve) => setTimeout(resolve, 300));
        await (element as any).updateComplete;
        const text = shadowText(element);
        expect(text).toContain('Not saved.');
        expect(text).toContain('server.hub.bogus');
        expect(text).toContain('x.y');
      });
    }
  });

  // ── Criterion 6: File mode ──

  describe('Criterion 6 — File mode', () => {
    it('all fields editable when no env_overrides', async () => {
      const config = makeBaseConfig({ settings_tier: 'file' });
      element = await createComponent(createFetchHandler(config));

      const badges = queryAll(element, '.read-only-badge');
      expect(badges.length).toBe(0);
    });

    it('env-overridden field renders read-only with env badge', async () => {
      const config = makeBaseConfig({
        settings_tier: 'file',
        env_overrides: ['server.hub.port'],
      });
      element = await createComponent(createFetchHandler(config));

      const badges = queryAll(element, '.read-only-badge');
      const badgeTexts = badges.map((b) => b.textContent ?? '');
      expect(badgeTexts.some((t) => t.includes('environment variable'))).toBe(true);

      const readOnlyValues = queryAll(element, '.read-only-value');
      const values = readOnlyValues.map((el) => el.textContent?.trim());
      expect(values).toContain('8080');
    });

    it('env-pinned field is absent from file-mode PUT payload', async () => {
      const config = makeBaseConfig({
        settings_tier: 'file',
        env_overrides: ['server.hub.port'],
      });
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
      const hub = server?.hub as Record<string, unknown> | undefined;

      expect(hub?.port).toBeUndefined();
      expect(hub?.admin_emails).toBeDefined();
    });

    it('non-env-overridden fields remain editable in file mode', async () => {
      const config = makeBaseConfig({
        settings_tier: 'file',
        env_overrides: ['server.hub.port'],
      });
      element = await createComponent(createFetchHandler(config));

      const adminInput = query(element, 'sl-input[value="admin@example.com"]');
      expect(adminInput).not.toBeNull();
    });

    it('file mode PUT payload includes Layer-0 keys when no env overrides', async () => {
      const config = makeBaseConfig({ settings_tier: 'file' });
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

      expect(server?.mode).toBeDefined();
      expect(server?.database).toBeDefined();
      expect(server?.hub).toBeDefined();
    });
  });

  // ── Agent Defaults card: default_timezone (tz-refactor task 12) ──

  describe('Agent Defaults card — default_timezone', () => {
    function timezonePicker(el: HTMLElement): Element | null {
      return query(el, 'scion-timezone-picker');
    }

    function emitTimezoneChange(el: HTMLElement, timezone: string): void {
      timezonePicker(el)!.dispatchEvent(
        new CustomEvent('timezone-change', { detail: { timezone } })
      );
    }

    it('loads the current default_timezone into the picker', async () => {
      const config = makeBaseConfig({ settings_tier: 'db', default_timezone: 'Asia/Tokyo' });
      element = await createComponent(createFetchHandler(config));

      const picker = timezonePicker(element);
      expect(picker).not.toBeNull();
      expect((picker as HTMLElement & { value: string }).value).toBe('Asia/Tokyo');
    });

    it('DB mode: PUT payload includes the edited default_timezone', async () => {
      const config = makeBaseConfig({ settings_tier: 'db', default_timezone: '' });
      let capturedPayload: Record<string, unknown> | null = null;

      element = await createComponent(
        createFetchHandler(config, {
          putHandler: (body) => {
            capturedPayload = body;
            return { status: 200, body: { reload: { applied: [] } } };
          },
        })
      );

      emitTimezoneChange(element, 'Europe/Berlin');
      await (element as any).updateComplete;

      const buttons = queryAll(element, 'sl-button[variant="primary"]');
      const saveBtn = buttons.find((b) => b.textContent?.trim() === 'Save & Reload');
      (saveBtn as HTMLElement).click();
      await new Promise((resolve) => setTimeout(resolve, 300));

      expect(capturedPayload).not.toBeNull();
      expect(capturedPayload!.default_timezone).toBe('Europe/Berlin');
    });

    it('DB mode: clearing default_timezone sends an explicit empty string', async () => {
      const config = makeBaseConfig({ settings_tier: 'db', default_timezone: 'Asia/Tokyo' });
      let capturedPayload: Record<string, unknown> | null = null;

      element = await createComponent(
        createFetchHandler(config, {
          putHandler: (body) => {
            capturedPayload = body;
            return { status: 200, body: { reload: { applied: [] } } };
          },
        })
      );

      emitTimezoneChange(element, '');
      await (element as any).updateComplete;

      const buttons = queryAll(element, 'sl-button[variant="primary"]');
      const saveBtn = buttons.find((b) => b.textContent?.trim() === 'Save & Reload');
      (saveBtn as HTMLElement).click();
      await new Promise((resolve) => setTimeout(resolve, 300));

      expect(capturedPayload).not.toBeNull();
      expect(capturedPayload!.default_timezone).toBe('');
    });

    it('file mode: clearing default_timezone sends an explicit empty string, not an omitted key', async () => {
      // Regression guard: unlike default_runtime_broker (`|| undefined`),
      // default_timezone must send "" explicitly on clear, because the
      // backend's *string field treats an omitted key as "no change" and
      // only an explicit "" as "clear" (see admin_settings.go).
      const config = makeBaseConfig({ settings_tier: 'file', default_timezone: 'Asia/Tokyo' });
      let capturedPayload: Record<string, unknown> | null = null;

      element = await createComponent(
        createFetchHandler(config, {
          putHandler: (body) => {
            capturedPayload = body;
            return { status: 200, body: { reload: { applied: [] } } };
          },
        })
      );

      emitTimezoneChange(element, '');
      await (element as any).updateComplete;

      const buttons = queryAll(element, 'sl-button[variant="primary"]');
      const saveBtn = buttons.find((b) => b.textContent?.trim() === 'Save & Reload');
      (saveBtn as HTMLElement).click();
      await new Promise((resolve) => setTimeout(resolve, 300));

      expect(capturedPayload).not.toBeNull();
      expect('default_timezone' in capturedPayload!).toBe(true);
      expect(capturedPayload!.default_timezone).toBe('');
    });

    it('file mode: PUT payload includes the edited default_timezone', async () => {
      const config = makeBaseConfig({ settings_tier: 'file', default_timezone: '' });
      let capturedPayload: Record<string, unknown> | null = null;

      element = await createComponent(
        createFetchHandler(config, {
          putHandler: (body) => {
            capturedPayload = body;
            return { status: 200, body: { reload: { applied: [] } } };
          },
        })
      );

      emitTimezoneChange(element, 'Asia/Kathmandu');
      await (element as any).updateComplete;

      const buttons = queryAll(element, 'sl-button[variant="primary"]');
      const saveBtn = buttons.find((b) => b.textContent?.trim() === 'Save & Reload');
      (saveBtn as HTMLElement).click();
      await new Promise((resolve) => setTimeout(resolve, 300));

      expect(capturedPayload).not.toBeNull();
      expect(capturedPayload!.default_timezone).toBe('Asia/Kathmandu');
    });

    it('an env-overridden default_timezone renders read-only with the env badge, in file mode', async () => {
      const config = makeBaseConfig({
        settings_tier: 'file',
        default_timezone: 'Asia/Tokyo',
        env_overrides: ['default_timezone'],
      });
      element = await createComponent(createFetchHandler(config));

      expect(timezonePicker(element)).toBeNull();
      const readOnlyValues = queryAll(element, '.read-only-value');
      const values = readOnlyValues.map((el) => el.textContent?.trim());
      expect(values).toContain('Asia/Tokyo');
    });

    it('shows an inline error for a name isValidTimeZone rejects, without blocking the field', async () => {
      const config = makeBaseConfig({ settings_tier: 'db', default_timezone: '' });
      element = await createComponent(createFetchHandler(config));

      emitTimezoneChange(element, 'Not/A/Timezone');
      await (element as any).updateComplete;

      expect(shadowText(element)).toContain('is not a recognized timezone');
    });

    it('shows no inline error for a valid zone or for the empty (UTC) value', async () => {
      const config = makeBaseConfig({ settings_tier: 'db', default_timezone: '' });
      element = await createComponent(createFetchHandler(config));

      expect(shadowText(element)).not.toContain('is not a recognized timezone');

      emitTimezoneChange(element, 'Asia/Tokyo');
      await (element as any).updateComplete;
      expect(shadowText(element)).not.toContain('is not a recognized timezone');
    });
  });

  // ── Criterion 8: Structured errors ──

  describe('Criterion 8 — Structured error handling', () => {
    it('400 validation_failed renders inline errors', async () => {
      const config = makeBaseConfig({ settings_tier: 'db' });

      element = await createComponent(
        createFetchHandler(config, {
          putHandler: () => ({
            status: 400,
            body: {
              error: 'validation_failed',
              errors: {
                access: [{ field: 'admin_emails', message: 'invalid email format' }],
              },
            },
          }),
        })
      );

      const buttons = queryAll(element, 'sl-button[variant="primary"]');
      const saveBtn = buttons.find((b) => b.textContent?.trim() === 'Save & Reload');
      (saveBtn as HTMLElement).click();
      await new Promise((resolve) => setTimeout(resolve, 300));
      await (element as any).updateComplete;

      const errorsEl = query(element, '.validation-errors');
      expect(errorsEl).not.toBeNull();
      const errText = errorsEl!.textContent ?? '';
      expect(errText).toContain('invalid email format');
      expect(errText).toContain('admin_emails');
    });

    it('409 revision_conflict renders conflict banner with Reload button', async () => {
      const config = makeBaseConfig({ settings_tier: 'db' });

      element = await createComponent(
        createFetchHandler(config, {
          putHandler: () => ({
            status: 409,
            body: {
              error: 'revision_conflict',
              message: 'Settings have been changed since you loaded this page.',
              conflicted: [{ section: 'access', expected_revision: 3, current_revision: 5 }],
            },
          }),
        })
      );

      const buttons = queryAll(element, 'sl-button[variant="primary"]');
      const saveBtn = buttons.find((b) => b.textContent?.trim() === 'Save & Reload');
      (saveBtn as HTMLElement).click();
      await new Promise((resolve) => setTimeout(resolve, 300));
      await (element as any).updateComplete;

      const bannerEl = query(element, '.conflict-banner');
      expect(bannerEl).not.toBeNull();
      const bannerText = bannerEl!.textContent ?? '';
      expect(bannerText).toContain('changed since you loaded');

      const reloadBtn = bannerEl!.querySelector('sl-button');
      expect(reloadBtn).not.toBeNull();
      expect(reloadBtn!.textContent).toContain('Reload');
    });

    it('422 layer0_rejected renders safety net notice and logs keys', async () => {
      const config = makeBaseConfig({ settings_tier: 'db' });
      const consoleSpy = vi.spyOn(console, 'log').mockImplementation(() => {});

      element = await createComponent(
        createFetchHandler(config, {
          putHandler: () => ({
            status: 422,
            body: {
              error: 'layer0_rejected',
              keys: ['server.mode', 'server.database.driver'],
            },
          }),
        })
      );

      const buttons = queryAll(element, 'sl-button[variant="primary"]');
      const saveBtn = buttons.find((b) => b.textContent?.trim() === 'Save & Reload');
      (saveBtn as HTMLElement).click();
      await new Promise((resolve) => setTimeout(resolve, 300));
      await (element as any).updateComplete;

      const noticeEl = query(element, '.safety-net-notice');
      expect(noticeEl).not.toBeNull();
      const noticeText = noticeEl!.textContent ?? '';
      expect(noticeText).toContain('server.mode');
      expect(noticeText).toContain('server.database.driver');
      expect(noticeText).toContain('bootstrap');

      expect(consoleSpy).toHaveBeenCalledWith(
        expect.stringContaining('layer0_rejected'),
        expect.arrayContaining(['server.mode', 'server.database.driver'])
      );

      consoleSpy.mockRestore();
    });

    it('unknown error falls back to generic error message', async () => {
      const config = makeBaseConfig({ settings_tier: 'db' });

      element = await createComponent(
        createFetchHandler(config, {
          putHandler: () => ({
            status: 500,
            body: {
              error: 'some_unknown_error',
              message: 'Something went terribly wrong',
            },
          }),
        })
      );

      const buttons = queryAll(element, 'sl-button[variant="primary"]');
      const saveBtn = buttons.find((b) => b.textContent?.trim() === 'Save & Reload');
      (saveBtn as HTMLElement).click();
      await new Promise((resolve) => setTimeout(resolve, 300));
      await (element as any).updateComplete;

      expect(shadowText(element)).toContain('Something went terribly wrong');
    });

    // handleSaveError's default case must handle more than a flat
    // {error: "<string>", ...} shape. The real Go writeError() helper
    // (pkg/hub/errors.go), used by
    // every plain field-validation 400/422 on this page — including
    // default_timezone's — responds with {error: {code, message, details}}.
    // Before the fix, body.error being an object meant the switch never
    // matched a case, body.message was undefined (nested at
    // body.error.message instead), and the real message was replaced by the
    // generic fallback. This is the shape the handler actually sends, unlike
    // the idealized flat-string shapes the other Criterion 8 tests above use
    // for validation_failed/revision_conflict/layer0_rejected.
    it('a real writeError()-shaped 422 (nested error.message) renders its message, not the generic fallback', async () => {
      const config = makeBaseConfig({ settings_tier: 'db' });

      element = await createComponent(
        createFetchHandler(config, {
          putHandler: () => ({
            status: 422,
            body: {
              error: {
                code: 'validation_error',
                message:
                  'invalid default_timezone "Not/A/Timezone": unknown time zone Not/A/Timezone',
              },
            },
          }),
        })
      );

      const buttons = queryAll(element, 'sl-button[variant="primary"]');
      const saveBtn = buttons.find((b) => b.textContent?.trim() === 'Save & Reload');
      (saveBtn as HTMLElement).click();
      await new Promise((resolve) => setTimeout(resolve, 300));
      await (element as any).updateComplete;

      const text = shadowText(element);
      expect(text).toContain('invalid default_timezone "Not/A/Timezone"');
      expect(text).not.toContain('An unexpected error occurred');
    });

    // A JSON error body that is not an object (null, a bare string, an
    // array) has no error code or message to read; handleSaveError must
    // treat it like a non-JSON body instead of dereferencing it.
    for (const [label, errBody] of [
      ['null', null],
      ['a bare string', 'upstream proxy error'],
      ['an array', ['boom']],
    ] as const) {
      it(`a non-object JSON error body (${label}) shows the save-failed message`, async () => {
        const config = makeBaseConfig({ settings_tier: 'db' });

        element = await createComponent(
          createFetchHandler(config, {
            putHandler: () => ({ status: 502, body: errBody }),
          })
        );

        const buttons = queryAll(element, 'sl-button[variant="primary"]');
        const saveBtn = buttons.find((b) => b.textContent?.trim() === 'Save & Reload');
        (saveBtn as HTMLElement).click();
        await new Promise((resolve) => setTimeout(resolve, 300));
        await (element as any).updateComplete;

        const text = shadowText(element);
        expect(text).toContain('Failed to save settings');
        expect(text).not.toContain('An unexpected error occurred');
      });
    }
  });

  // ── Schema fallback ──

  describe('Schema endpoint fallback', () => {
    it('falls back to STATIC_LAYER1_KEYS when schema endpoint fails', async () => {
      const config = makeBaseConfig({ settings_tier: 'db' });
      element = await createComponent(createFetchHandler(config, { schemaResponse: null }));

      const adminInput = query(element, 'sl-input[value="admin@example.com"]');
      expect(adminInput).not.toBeNull();

      const badges = queryAll(element, '.read-only-badge');
      expect(badges.length).toBeGreaterThan(0);
      const badgeTexts = badges.map((b) => b.textContent ?? '');
      expect(badgeTexts.some((t) => t.includes('deployment configuration'))).toBe(true);
    });
  });

  // ── Regression: clearing fields to blank (ptone/scion#860) ──

  describe('Regression #860 — clearing agent limit fields to blank', () => {
    it('file mode PUT includes zero/empty agent limit fields so backend can clear them', async () => {
      // Start with agent limits set to non-zero values, then verify
      // the PUT payload includes the zeroed values (not undefined).
      const config = makeBaseConfig({
        settings_tier: 'file',
        default_max_turns: 100,
        default_max_model_calls: 200,
        default_max_duration: '2h',
      });
      let capturedPayload: Record<string, unknown> | null = null;

      element = await createComponent(
        createFetchHandler(config, {
          putHandler: (body) => {
            capturedPayload = body;
            return { status: 200, body: { reload: { applied: [] } } };
          },
        })
      );

      // Simulate clearing the fields: set the internal state to zero/empty.
      // In the real UI, the user would clear the input fields.
      (element as any).defaultMaxTurns = 0;
      (element as any).defaultMaxModelCalls = 0;
      (element as any).defaultMaxDuration = '';
      await (element as any).updateComplete;

      // Trigger save.
      const buttons = queryAll(element, 'sl-button[variant="primary"]');
      const saveBtn = buttons.find((b) => b.textContent?.trim() === 'Save & Reload');
      expect(saveBtn).not.toBeNull();
      (saveBtn as HTMLElement).click();
      await new Promise((resolve) => setTimeout(resolve, 300));
      await (element as any).updateComplete;

      expect(capturedPayload).not.toBeNull();

      // The key assertion: zero/empty values MUST be present in the payload
      // (not undefined), so the backend receives them and can delete the keys.
      expect(capturedPayload!.default_max_turns).toBe(0);
      expect(capturedPayload!.default_max_model_calls).toBe(0);
      expect(capturedPayload!.default_max_duration).toBe('');
    });
  });
});
