/**
 * Shared mock data builders and helpers for the admin-server-config.*.test.ts files.
 */

import { vi } from 'vitest';
import type { ScionPageAdminServerConfig } from '../admin-server-config.js';

// ── Shared mock data builders ──

export function makeBaseConfig(overrides: Record<string, unknown> = {}) {
  return {
    schema_version: '1',
    scion_version: '0.1.0-test',
    scion_commit: 'abc123',
    scion_build_time: '2026-01-01T00:00:00Z',
    active_profile: 'default',
    default_template: 'gemini',
    image_registry: 'ghcr.io/test',
    server: {
      mode: 'standalone',
      log_level: 'info',
      log_format: 'text',
      hub: {
        port: 8080,
        host: '0.0.0.0',
        admin_emails: ['admin@example.com'],
        public_url: 'https://hub.example.com',
        soft_delete_retention: '72h',
        soft_delete_retain_files: true,
        auto_suspend_stalled: false,
      },
      broker: { enabled: false },
      database: { driver: 'postgres', url: '********' },
      auth: { dev_mode: false, user_access_mode: 'open', authorized_domains: '' },
      storage: { provider: 'local', local_path: '/data' },
      secrets: { provider: 'env' },
    },
    telemetry: {
      enabled: false,
      cloud: { enabled: false },
      hub: { enabled: false },
      local: { enabled: false },
    },
    ...overrides,
  };
}

export const SCHEMA_RESPONSE = {
  sections: {
    access: {
      koanf_paths: [
        'server.hub.admin_emails',
        'server.auth.user_access_mode',
        'server.auth.default_user_role',
        'server.auth.authorized_domains',
      ],
    },
    lifecycle: {
      koanf_paths: [
        'server.hub.auto_suspend_stalled',
        'server.hub.soft_delete_retention',
        'server.hub.soft_delete_retain_files',
      ],
    },
    endpoints: {
      koanf_paths: [
        'server.hub.public_url',
        'image_registry',
        'server.hub.monitoring_dashboard_url',
      ],
    },
    agent_defaults: {
      koanf_paths: [
        'default_template',
        'default_harness_config',
        'default_max_turns',
        'default_max_model_calls',
        'default_max_duration',
        'default_resources',
        'default_timezone',
      ],
    },
    telemetry: {
      koanf_paths: [
        'telemetry.enabled',
        'telemetry.cloud.enabled',
        'telemetry.cloud.endpoint',
        'telemetry.cloud.protocol',
        'telemetry.cloud.provider',
        'telemetry.hub.enabled',
        'telemetry.hub.report_interval',
        'telemetry.local.enabled',
        'telemetry.local.file',
        'telemetry.local.console',
      ],
    },
    notifications: { koanf_paths: ['server.notification_channels'] },
    github_app: {
      koanf_paths: [
        'server.github_app',
        'server.github_app.app_id',
        'server.github_app.api_base_url',
        'server.github_app.webhooks_enabled',
        'server.github_app.installation_url',
        'server.github_app.private_key_path',
      ],
    },
    agent_secrets: {
      koanf_paths: ['agent_secrets.user_scope_only'],
    },
    gcp_iam: {
      koanf_paths: ['server.hub.gcp_iam_check_mode', 'server.hub.gcp_iam_deny_unknown_policy'],
    },
  },
};

export function createFetchHandler(
  configResponse: Record<string, unknown>,
  opts?: {
    schemaResponse?: Record<string, unknown> | null;
    putHandler?: (body: Record<string, unknown>) => {
      status: number;
      body: unknown;
    };
    messagingResponse?: Record<string, unknown>;
    checkUpdatesResponse?: Record<string, unknown>;
    onOperationRun?: (path: string) => void;
  }
) {
  return (url: string | URL | Request, init?: RequestInit): Promise<Response> => {
    const path = typeof url === 'string' ? url : url instanceof URL ? url.pathname : url.url;

    if (
      init?.method === 'PUT' &&
      opts?.putHandler &&
      path.includes('/api/v1/admin/server-config') &&
      !path.includes('/schema')
    ) {
      const reqBody = JSON.parse(init.body as string);
      const result = opts.putHandler(reqBody);
      return Promise.resolve(
        new Response(JSON.stringify(result.body), {
          status: result.status,
          headers: { 'Content-Type': 'application/json' },
        })
      );
    }

    if (path.includes('/api/v1/admin/server-config/schema')) {
      if (opts?.schemaResponse === null) {
        return Promise.resolve(new Response('', { status: 500 }));
      }
      return Promise.resolve(
        new Response(JSON.stringify(opts?.schemaResponse ?? SCHEMA_RESPONSE), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        })
      );
    }

    if (path.includes('/api/v1/admin/server-config')) {
      return Promise.resolve(
        new Response(JSON.stringify(configResponse), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        })
      );
    }

    if (path.includes('/api/v1/admin/messaging')) {
      if (init?.method === 'PUT' && opts?.putHandler) {
        const reqBody = JSON.parse(init.body as string);
        const result = opts.putHandler(reqBody);
        return Promise.resolve(
          new Response(JSON.stringify(result.body), {
            status: result.status,
            headers: { 'Content-Type': 'application/json' },
          })
        );
      }
      return Promise.resolve(
        new Response(
          JSON.stringify(
            opts?.messagingResponse ?? {
              cross_project_messaging_enabled: false,
              revision: 1,
            }
          ),
          { status: 200, headers: { 'Content-Type': 'application/json' } }
        )
      );
    }

    if (path.includes('/api/v1/admin/maintenance/check-updates')) {
      return Promise.resolve(
        new Response(
          JSON.stringify(opts?.checkUpdatesResponse ?? { tier: 'source', update_available: false }),
          { status: 200, headers: { 'Content-Type': 'application/json' } }
        )
      );
    }

    if (path.includes('/api/v1/admin/maintenance/operations/') && path.endsWith('/run')) {
      opts?.onOperationRun?.(path);
      return Promise.resolve(
        new Response(JSON.stringify({ runId: 'run-1', status: 'running' }), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        })
      );
    }

    if (path.includes('/api/v1/github-app/installations')) {
      return Promise.resolve(
        new Response(JSON.stringify([]), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        })
      );
    }

    if (path.includes('/api/v1/github-app')) {
      return Promise.resolve(
        new Response(JSON.stringify({}), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        })
      );
    }

    return Promise.resolve(
      new Response(JSON.stringify([]), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      })
    );
  };
}

// Import the component module once so the custom element is only registered once.

export async function createComponent(
  fetchHandler: (url: string | URL | Request, init?: RequestInit) => Promise<Response>
) {
  vi.stubGlobal('fetch', vi.fn(fetchHandler));
  const el = document.createElement('scion-page-admin-server-config') as InstanceType<
    typeof ScionPageAdminServerConfig
  >;
  document.body.appendChild(el);
  await el.updateComplete;
  await new Promise((resolve) => setTimeout(resolve, 200));
  await el.updateComplete;
  return el;
}

export function shadowText(el: HTMLElement): string {
  return el.shadowRoot?.textContent ?? '';
}

export function queryAll(el: HTMLElement, selector: string): Element[] {
  return Array.from(el.shadowRoot?.querySelectorAll(selector) ?? []);
}

export function query(el: HTMLElement, selector: string): Element | null {
  return el.shadowRoot?.querySelector(selector) ?? null;
}
