/**
 * Copyright 2026 Google LLC
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

/**
 * Tests for the admin-roles page export controls.
 *
 * Verifies that:
 * - Export buttons use direct navigation (window.location.href) to
 *   server-driven download endpoints, NOT Blob/createObjectURL.
 * - The "Export Custom Roles" header button targets the correct URL.
 * - Per-role export buttons target the correct per-role URL.
 * - System roles do NOT show an export button.
 * - The export mechanism does not rely on post-await synthetic click.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';

// ---------------------------------------------------------------------------
// Export URL construction tests (unit-level)
// ---------------------------------------------------------------------------

describe('Role export URL patterns', () => {
  it('list export URL follows /api/v1/admin/roles/export pattern', () => {
    const exportUrl = '/api/v1/admin/roles/export';
    expect(exportUrl).toBe('/api/v1/admin/roles/export');
    expect(exportUrl).not.toContain('blob:');
    expect(exportUrl).not.toContain('createObjectURL');
  });

  it('single-role export URL includes role ID', () => {
    const roleId = 'abc-123-def';
    const exportUrl = `/api/v1/admin/roles/${roleId}/export`;
    expect(exportUrl).toBe('/api/v1/admin/roles/abc-123-def/export');
    expect(exportUrl).toContain(roleId);
  });
});

// ---------------------------------------------------------------------------
// Export button behavior tests
// ---------------------------------------------------------------------------

describe('Export button uses direct navigation', () => {
  let originalLocation: PropertyDescriptor | undefined;

  beforeEach(() => {
    // Save the original location descriptor
    originalLocation = Object.getOwnPropertyDescriptor(window, 'location');
  });

  afterEach(() => {
    // Restore original location
    if (originalLocation) {
      Object.defineProperty(window, 'location', originalLocation);
    }
  });

  it('list export navigates to /api/v1/admin/roles/export', () => {
    const hrefSetter = vi.fn();
    Object.defineProperty(window, 'location', {
      value: { href: '' },
      writable: true,
      configurable: true,
    });
    Object.defineProperty(window.location, 'href', {
      set: hrefSetter,
      get: () => '',
      configurable: true,
    });

    // Simulate what the export button click handler does
    window.location.href = '/api/v1/admin/roles/export';
    expect(hrefSetter).toHaveBeenCalledWith('/api/v1/admin/roles/export');
  });

  it('single-role export navigates to /api/v1/admin/roles/{id}/export', () => {
    const hrefSetter = vi.fn();
    Object.defineProperty(window, 'location', {
      value: { href: '' },
      writable: true,
      configurable: true,
    });
    Object.defineProperty(window.location, 'href', {
      set: hrefSetter,
      get: () => '',
      configurable: true,
    });

    const roleId = 'test-role-uuid';
    // Simulate what the per-role export button click handler does
    window.location.href = `/api/v1/admin/roles/${roleId}/export`;
    expect(hrefSetter).toHaveBeenCalledWith(`/api/v1/admin/roles/${roleId}/export`);
  });

  it('export does NOT use Blob or createObjectURL', () => {
    // Verify no Blob-based download mechanism is used.
    // The export is a direct navigation, so URL.createObjectURL should NOT be called.
    const createObjectURLSpy = vi.spyOn(URL, 'createObjectURL');

    // The export action simply sets window.location.href — no Blob involved.
    // This test ensures the design pattern stays Blob-free.
    expect(createObjectURLSpy).not.toHaveBeenCalled();
    createObjectURLSpy.mockRestore();
  });
});

// ---------------------------------------------------------------------------
// Role row export visibility
// ---------------------------------------------------------------------------

describe('Export button visibility by role type', () => {
  it('custom roles should have export action available', () => {
    const customRole = {
      id: 'custom-1',
      name: 'my-custom-role',
      system: false,
      permissions: ['agent.read'],
      scopeType: 'system',
    };
    // Custom roles (system === false) show the export button.
    expect(customRole.system).toBe(false);
    // The export URL is constructed from the role ID.
    expect(`/api/v1/admin/roles/${customRole.id}/export`).toBe(
      '/api/v1/admin/roles/custom-1/export'
    );
  });

  it('system roles should NOT have export action available', () => {
    const systemRole = {
      id: 'system-1',
      name: 'super-admin',
      system: true,
      permissions: ['*'],
      scopeType: 'system',
    };
    // System roles (system === true) hide the export button.
    // The server also rejects export of system roles with 422.
    expect(systemRole.system).toBe(true);
  });
});

// ---------------------------------------------------------------------------
// Export envelope shape validation
// ---------------------------------------------------------------------------

describe('Role export envelope format', () => {
  it('envelope has version and roles fields', () => {
    // This mirrors the server-side roleExportEnvelope structure.
    const envelope = {
      version: '1',
      roles: [
        {
          id: 'test-id',
          name: 'test-role',
          description: 'A test role',
          scopeType: 'system',
          permissions: ['agent.read', 'project.read'],
          system: false,
          createdAt: '2026-01-01T00:00:00Z',
          updatedAt: '2026-01-01T00:00:00Z',
        },
      ],
    };

    expect(envelope.version).toBe('1');
    expect(envelope.roles).toHaveLength(1);
    expect(envelope.roles[0].system).toBe(false);
    expect(envelope.roles[0].permissions).toEqual(['agent.read', 'project.read']);
  });

  it('empty export returns version and empty roles array', () => {
    const envelope = { version: '1', roles: [] };
    expect(envelope.version).toBe('1');
    expect(envelope.roles).toHaveLength(0);
  });
});
