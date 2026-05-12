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

import { describe, it, expect, vi, beforeEach } from "vitest";
import { ScionClient, ScionError } from "../src/index";
import type { Secret, SetSecretResponse, ListSecretsResponse } from "../src/index";

/** Creates a mock fetch function that returns a JSON response. */
function mockFetch(body: unknown, status = 200): typeof fetch {
  return vi.fn().mockResolvedValue({
    ok: status >= 200 && status < 300,
    status,
    statusText: status === 200 ? "OK" : status === 204 ? "No Content" : "Error",
    json: () => Promise.resolve(body),
    text: () => Promise.resolve(JSON.stringify(body)),
  }) as unknown as typeof fetch;
}

/** Creates a mock fetch that returns 204 No Content. */
function mockFetchNoContent(): typeof fetch {
  return vi.fn().mockResolvedValue({
    ok: true,
    status: 204,
    statusText: "No Content",
    json: () => Promise.reject(new Error("no body")),
    text: () => Promise.resolve(""),
  }) as unknown as typeof fetch;
}

/** Creates a mock fetch that returns an error. */
function mockFetchError(
  status: number,
  body: unknown = { error: "not found" },
): typeof fetch {
  return vi.fn().mockResolvedValue({
    ok: false,
    status,
    statusText: "Error",
    json: () => Promise.resolve(body),
    text: () => Promise.resolve(JSON.stringify(body)),
  }) as unknown as typeof fetch;
}

const baseURL = "https://hub.example.com";

/** A sample secret for testing. */
const sampleSecret: Secret = {
  id: "sec-001",
  key: "MY_API_KEY",
  type: "environment",
  scope: "user",
  scopeId: "user-123",
  description: "An API key",
  injectionMode: "as_needed",
  version: 1,
  created: "2026-01-01T00:00:00Z",
  updated: "2026-01-01T00:00:00Z",
  createdBy: "user-123",
};

describe("SecretsResource", () => {
  describe("list", () => {
    it("lists secrets with no parameters", async () => {
      const responseBody: ListSecretsResponse = {
        secrets: [sampleSecret],
        scope: "user",
        scopeId: "user-123",
      };
      const fetchFn = mockFetch(responseBody);
      const client = new ScionClient(baseURL, {
        token: "test-token",
        fetch: fetchFn,
      });

      const page = await client.secrets.list();

      expect(page.data).toEqual([sampleSecret]);
      expect(page.scope).toBe("user");
      expect(page.scopeId).toBe("user-123");

      expect(fetchFn).toHaveBeenCalledWith(
        `${baseURL}/api/v1/secrets`,
        expect.objectContaining({ method: "GET" }),
      );
    });

    it("lists secrets with scope parameters", async () => {
      const responseBody: ListSecretsResponse = {
        secrets: [sampleSecret],
        scope: "project",
        scopeId: "proj-456",
      };
      const fetchFn = mockFetch(responseBody);
      const client = new ScionClient(baseURL, {
        token: "test-token",
        fetch: fetchFn,
      });

      const page = await client.secrets.list({
        scope: "project",
        scopeId: "proj-456",
      });

      expect(page.data).toEqual([sampleSecret]);

      const calledURL = (fetchFn as ReturnType<typeof vi.fn>).mock.calls[0][0] as string;
      const url = new URL(calledURL);
      expect(url.searchParams.get("scope")).toBe("project");
      expect(url.searchParams.get("scopeId")).toBe("proj-456");
    });

    it("lists secrets with type filter", async () => {
      const responseBody: ListSecretsResponse = {
        secrets: [],
        scope: "user",
        scopeId: "user-123",
      };
      const fetchFn = mockFetch(responseBody);
      const client = new ScionClient(baseURL, {
        token: "test-token",
        fetch: fetchFn,
      });

      const page = await client.secrets.list({ type: "file" });

      expect(page.data).toEqual([]);

      const calledURL = (fetchFn as ReturnType<typeof vi.fn>).mock.calls[0][0] as string;
      const url = new URL(calledURL);
      expect(url.searchParams.get("type")).toBe("file");
    });

    it("returns empty data when no secrets exist", async () => {
      const responseBody: ListSecretsResponse = {
        secrets: [],
        scope: "user",
        scopeId: "user-123",
      };
      const fetchFn = mockFetch(responseBody);
      const client = new ScionClient(baseURL, {
        token: "test-token",
        fetch: fetchFn,
      });

      const page = await client.secrets.list();

      expect(page.data).toEqual([]);
      expect(page.data).toHaveLength(0);
    });
  });

  describe("get", () => {
    it("gets a secret by key", async () => {
      const fetchFn = mockFetch(sampleSecret);
      const client = new ScionClient(baseURL, {
        token: "test-token",
        fetch: fetchFn,
      });

      const secret = await client.secrets.get("MY_API_KEY");

      expect(secret).toEqual(sampleSecret);

      const calledURL = (fetchFn as ReturnType<typeof vi.fn>).mock.calls[0][0] as string;
      expect(calledURL).toBe(`${baseURL}/api/v1/secrets/MY_API_KEY`);
    });

    it("gets a secret with scope parameters", async () => {
      const fetchFn = mockFetch(sampleSecret);
      const client = new ScionClient(baseURL, {
        token: "test-token",
        fetch: fetchFn,
      });

      const secret = await client.secrets.get("MY_API_KEY", {
        scope: "project",
        scopeId: "proj-456",
      });

      expect(secret).toEqual(sampleSecret);

      const calledURL = (fetchFn as ReturnType<typeof vi.fn>).mock.calls[0][0] as string;
      const url = new URL(calledURL);
      expect(url.pathname).toBe("/api/v1/secrets/MY_API_KEY");
      expect(url.searchParams.get("scope")).toBe("project");
      expect(url.searchParams.get("scopeId")).toBe("proj-456");
    });

    it("URL-encodes special characters in key", async () => {
      const fetchFn = mockFetch(sampleSecret);
      const client = new ScionClient(baseURL, {
        token: "test-token",
        fetch: fetchFn,
      });

      await client.secrets.get("my/special key");

      const calledURL = (fetchFn as ReturnType<typeof vi.fn>).mock.calls[0][0] as string;
      expect(calledURL).toContain(encodeURIComponent("my/special key"));
    });

    it("throws ScionError on 404", async () => {
      const fetchFn = mockFetchError(404, { error: "secret not found" });
      const client = new ScionClient(baseURL, {
        token: "test-token",
        fetch: fetchFn,
      });

      await expect(client.secrets.get("NONEXISTENT")).rejects.toThrow(ScionError);
      await expect(client.secrets.get("NONEXISTENT")).rejects.toMatchObject({
        status: 404,
      });
    });
  });

  describe("set", () => {
    it("creates a new secret", async () => {
      const responseBody: SetSecretResponse = {
        secret: sampleSecret,
        created: true,
      };
      const fetchFn = mockFetch(responseBody);
      const client = new ScionClient(baseURL, {
        token: "test-token",
        fetch: fetchFn,
      });

      const result = await client.secrets.set("MY_API_KEY", {
        value: "sk-secret-value",
        description: "An API key",
      });

      expect(result.secret).toEqual(sampleSecret);
      expect(result.created).toBe(true);

      const calledURL = (fetchFn as ReturnType<typeof vi.fn>).mock.calls[0][0] as string;
      expect(calledURL).toBe(`${baseURL}/api/v1/secrets/MY_API_KEY`);

      const calledOptions = (fetchFn as ReturnType<typeof vi.fn>).mock.calls[0][1];
      expect(calledOptions.method).toBe("PUT");

      const body = JSON.parse(calledOptions.body);
      expect(body.value).toBe("sk-secret-value");
      expect(body.description).toBe("An API key");
    });

    it("updates an existing secret", async () => {
      const responseBody: SetSecretResponse = {
        secret: { ...sampleSecret, version: 2 },
        created: false,
      };
      const fetchFn = mockFetch(responseBody);
      const client = new ScionClient(baseURL, {
        token: "test-token",
        fetch: fetchFn,
      });

      const result = await client.secrets.set("MY_API_KEY", {
        value: "new-value",
      });

      expect(result.created).toBe(false);
      expect(result.secret.version).toBe(2);
    });

    it("sets a project-scoped secret with all options", async () => {
      const responseBody: SetSecretResponse = {
        secret: sampleSecret,
        created: true,
      };
      const fetchFn = mockFetch(responseBody);
      const client = new ScionClient(baseURL, {
        token: "test-token",
        fetch: fetchFn,
      });

      await client.secrets.set("DB_PASSWORD", {
        value: "s3cret",
        scope: "project",
        scopeId: "proj-456",
        description: "Database password",
        injectionMode: "always",
        type: "environment",
        target: "DATABASE_PASSWORD",
        allowProgeny: true,
      });

      const calledOptions = (fetchFn as ReturnType<typeof vi.fn>).mock.calls[0][1];
      const body = JSON.parse(calledOptions.body);
      expect(body).toEqual({
        value: "s3cret",
        scope: "project",
        scopeId: "proj-456",
        description: "Database password",
        injectionMode: "always",
        type: "environment",
        target: "DATABASE_PASSWORD",
        allowProgeny: true,
      });
    });

    it("URL-encodes special characters in key", async () => {
      const responseBody: SetSecretResponse = {
        secret: sampleSecret,
        created: true,
      };
      const fetchFn = mockFetch(responseBody);
      const client = new ScionClient(baseURL, {
        token: "test-token",
        fetch: fetchFn,
      });

      await client.secrets.set("my/special key", { value: "val" });

      const calledURL = (fetchFn as ReturnType<typeof vi.fn>).mock.calls[0][0] as string;
      expect(calledURL).toContain(encodeURIComponent("my/special key"));
    });
  });

  describe("delete", () => {
    it("deletes a secret by key", async () => {
      const fetchFn = mockFetchNoContent();
      const client = new ScionClient(baseURL, {
        token: "test-token",
        fetch: fetchFn,
      });

      await client.secrets.delete("MY_API_KEY");

      const calledURL = (fetchFn as ReturnType<typeof vi.fn>).mock.calls[0][0] as string;
      expect(calledURL).toBe(`${baseURL}/api/v1/secrets/MY_API_KEY`);

      const calledOptions = (fetchFn as ReturnType<typeof vi.fn>).mock.calls[0][1];
      expect(calledOptions.method).toBe("DELETE");
    });

    it("deletes a secret with scope parameters", async () => {
      const fetchFn = mockFetchNoContent();
      const client = new ScionClient(baseURL, {
        token: "test-token",
        fetch: fetchFn,
      });

      await client.secrets.delete("DB_PASSWORD", {
        scope: "project",
        scopeId: "proj-456",
      });

      const calledURL = (fetchFn as ReturnType<typeof vi.fn>).mock.calls[0][0] as string;
      const url = new URL(calledURL);
      expect(url.pathname).toBe("/api/v1/secrets/DB_PASSWORD");
      expect(url.searchParams.get("scope")).toBe("project");
      expect(url.searchParams.get("scopeId")).toBe("proj-456");
    });

    it("throws ScionError on 404", async () => {
      const fetchFn = mockFetchError(404, { error: "secret not found" });
      const client = new ScionClient(baseURL, {
        token: "test-token",
        fetch: fetchFn,
      });

      await expect(
        client.secrets.delete("NONEXISTENT"),
      ).rejects.toThrow(ScionError);
    });

    it("URL-encodes special characters in key", async () => {
      const fetchFn = mockFetchNoContent();
      const client = new ScionClient(baseURL, {
        token: "test-token",
        fetch: fetchFn,
      });

      await client.secrets.delete("my/special key");

      const calledURL = (fetchFn as ReturnType<typeof vi.fn>).mock.calls[0][0] as string;
      expect(calledURL).toContain(encodeURIComponent("my/special key"));
    });
  });

  describe("authentication", () => {
    it("includes Bearer token in requests", async () => {
      const responseBody: ListSecretsResponse = {
        secrets: [],
        scope: "user",
        scopeId: "user-123",
      };
      const fetchFn = mockFetch(responseBody);
      const client = new ScionClient(baseURL, {
        token: "my-token",
        fetch: fetchFn,
      });

      await client.secrets.list();

      const calledOptions = (fetchFn as ReturnType<typeof vi.fn>).mock.calls[0][1];
      expect(calledOptions.headers["Authorization"]).toBe("Bearer my-token");
    });

    it("includes agent token in requests", async () => {
      const responseBody: ListSecretsResponse = {
        secrets: [],
        scope: "user",
        scopeId: "user-123",
      };
      const fetchFn = mockFetch(responseBody);
      const client = new ScionClient(baseURL, {
        agentToken: "agent-tok",
        fetch: fetchFn,
      });

      await client.secrets.list();

      const calledOptions = (fetchFn as ReturnType<typeof vi.fn>).mock.calls[0][1];
      expect(calledOptions.headers["X-Scion-Agent-Token"]).toBe("agent-tok");
    });
  });

  describe("ScionClient.secrets property", () => {
    it("exposes secrets as a property", () => {
      const client = new ScionClient(baseURL, {
        token: "test-token",
        fetch: mockFetch({}),
      });

      expect(client.secrets).toBeDefined();
      expect(typeof client.secrets.list).toBe("function");
      expect(typeof client.secrets.get).toBe("function");
      expect(typeof client.secrets.set).toBe("function");
      expect(typeof client.secrets.delete).toBe("function");
    });
  });
});
