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
import { ScionClient } from "../src/client.js";
import { AgentsResource } from "../src/resources/agents.js";
import {
  ApiError,
  NotFoundError,
  AuthenticationError,
  ValidationError,
} from "../src/errors.js";
import type {
  Agent,
  CreateAgentParams,
  StructuredMessage,
} from "../src/types/index.js";

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

/** Minimal agent fixture for response mocking. */
function makeAgent(overrides: Partial<Agent> = {}): Agent {
  return {
    id: "agent-001",
    slug: "code-reviewer",
    containerId: "ctr-abc",
    name: "code-reviewer",
    status: "running",
    phase: "running",
    created: "2026-05-12T00:00:00Z",
    updated: "2026-05-12T00:00:00Z",
    ...overrides,
  };
}

interface CapturedRequest {
  url: string;
  method: string;
  headers: Record<string, string>;
  body?: unknown;
}

/**
 * Create a mock fetch that captures the request and returns a canned response.
 * Returns [fetchMock, getCaptured] where getCaptured() returns the last request.
 */
function mockFetch(
  responseBody: unknown,
  status = 200,
): [typeof globalThis.fetch, () => CapturedRequest] {
  let captured: CapturedRequest | undefined;

  const fetchFn = vi.fn(async (url: string | URL | Request, init?: RequestInit) => {
    const urlStr = typeof url === "string" ? url : url instanceof URL ? url.toString() : url.url;
    const method = init?.method ?? "GET";
    const headers: Record<string, string> = {};
    if (init?.headers) {
      const h = init.headers as Record<string, string>;
      for (const [k, v] of Object.entries(h)) {
        headers[k] = v;
      }
    }
    let body: unknown;
    if (init?.body) {
      body = JSON.parse(init.body as string);
    }
    captured = { url: urlStr, method, headers, body };

    return new Response(
      status === 204 ? null : JSON.stringify(responseBody),
      {
        status,
        statusText: status === 204 ? "No Content" : "OK",
        headers: { "Content-Type": "application/json" },
      },
    );
  }) as unknown as typeof globalThis.fetch;

  return [fetchFn, () => captured!];
}

/**
 * Create a mock fetch that returns an error response.
 */
function mockFetchError(
  status: number,
  errorBody: { error?: string; message?: string } = {},
): typeof globalThis.fetch {
  return vi.fn(async () => {
    return new Response(JSON.stringify(errorBody), {
      status,
      statusText: "Error",
      headers: { "Content-Type": "application/json" },
    });
  }) as unknown as typeof globalThis.fetch;
}

const BASE_URL = "https://hub.example.com";

// ---------------------------------------------------------------------------
// AgentsResource — CRUD & lifecycle
// ---------------------------------------------------------------------------

describe("AgentsResource", () => {
  // -----------------------------------------------------------------------
  // create
  // -----------------------------------------------------------------------
  describe("create", () => {
    it("posts to /api/v1/agents with the correct body", async () => {
      const agent = makeAgent();
      const [fetchFn, req] = mockFetch({ agent, warnings: [] });
      const client = new ScionClient({ baseUrl: BASE_URL, fetch: fetchFn });

      const params: CreateAgentParams = {
        name: "code-reviewer",
        projectId: "proj-123",
        template: "claude",
        task: "Review PR #42",
      };
      const result = await client.agents.create(params);

      expect(req().method).toBe("POST");
      expect(req().url).toContain("/api/v1/agents");
      expect(req().body).toEqual(params);
      expect(result.agent.id).toBe("agent-001");
    });

    it("posts to project-scoped path when projectId is set on client", async () => {
      const agent = makeAgent();
      const [fetchFn, req] = mockFetch({ agent });
      const client = new ScionClient({
        baseUrl: BASE_URL,
        fetch: fetchFn,
        projectId: "proj-xyz",
      });

      await client.agents.create({
        name: "test",
        projectId: "proj-xyz",
      });

      expect(req().url).toContain("/api/v1/projects/proj-xyz/agents");
    });
  });

  // -----------------------------------------------------------------------
  // get
  // -----------------------------------------------------------------------
  describe("get", () => {
    it("fetches a single agent by ID", async () => {
      const agent = makeAgent({ id: "agent-42", name: "my-agent" });
      const [fetchFn, req] = mockFetch(agent);
      const client = new ScionClient({ baseUrl: BASE_URL, fetch: fetchFn });

      const result = await client.agents.get("agent-42");

      expect(req().method).toBe("GET");
      expect(req().url).toContain("/api/v1/agents/agent-42");
      expect(result.id).toBe("agent-42");
      expect(result.name).toBe("my-agent");
    });

    it("throws NotFoundError for 404", async () => {
      const fetchFn = mockFetchError(404, { error: "agent not found" });
      const client = new ScionClient({ baseUrl: BASE_URL, fetch: fetchFn });

      await expect(client.agents.get("missing")).rejects.toThrow(NotFoundError);
    });

    it("throws AuthenticationError for 401", async () => {
      const fetchFn = mockFetchError(401, { error: "unauthorized" });
      const client = new ScionClient({ baseUrl: BASE_URL, fetch: fetchFn });

      await expect(client.agents.get("any")).rejects.toThrow(
        AuthenticationError,
      );
    });
  });

  // -----------------------------------------------------------------------
  // list
  // -----------------------------------------------------------------------
  describe("list", () => {
    it("fetches agents with default params", async () => {
      const agents = [makeAgent({ id: "a1" }), makeAgent({ id: "a2" })];
      const [fetchFn, req] = mockFetch({
        agents,
        totalCount: 2,
      });
      const client = new ScionClient({ baseUrl: BASE_URL, fetch: fetchFn });

      const page = await client.agents.list();

      expect(req().method).toBe("GET");
      expect(req().url).toContain("/api/v1/agents");
      expect(page.data).toHaveLength(2);
      expect(page.totalCount).toBe(2);
      expect(page.hasMore).toBe(false);
    });

    it("passes filter and pagination query params", async () => {
      const [fetchFn, req] = mockFetch({ agents: [], totalCount: 0 });
      const client = new ScionClient({ baseUrl: BASE_URL, fetch: fetchFn });

      await client.agents.list({
        phase: "running",
        limit: 10,
        cursor: "abc",
        includeDeleted: true,
        labels: { env: "prod" },
      });

      const url = req().url;
      expect(url).toContain("phase=running");
      expect(url).toContain("limit=10");
      expect(url).toContain("cursor=abc");
      expect(url).toContain("includeDeleted=true");
      expect(url).toContain("label=env%3Dprod");
    });

    it("uses project-scoped path", async () => {
      const [fetchFn, req] = mockFetch({ agents: [] });
      const client = new ScionClient({
        baseUrl: BASE_URL,
        fetch: fetchFn,
        projectId: "proj-1",
      });

      await client.agents.list();
      expect(req().url).toContain("/api/v1/projects/proj-1/agents");
    });

    it("supports async iteration across pages", async () => {
      let callCount = 0;
      const fetchFn = vi.fn(async (url: string | URL | Request) => {
        callCount++;
        const urlStr = typeof url === "string" ? url : url instanceof URL ? url.toString() : url.url;
        if (urlStr.includes("cursor=page2")) {
          return new Response(
            JSON.stringify({
              agents: [makeAgent({ id: "a3" })],
              totalCount: 3,
            }),
            { status: 200, headers: { "Content-Type": "application/json" } },
          );
        }
        return new Response(
          JSON.stringify({
            agents: [makeAgent({ id: "a1" }), makeAgent({ id: "a2" })],
            nextCursor: "page2",
            totalCount: 3,
          }),
          { status: 200, headers: { "Content-Type": "application/json" } },
        );
      }) as unknown as typeof globalThis.fetch;

      const client = new ScionClient({ baseUrl: BASE_URL, fetch: fetchFn });
      const ids: string[] = [];
      const page = await client.agents.list();

      for await (const agent of page) {
        ids.push(agent.id);
      }

      expect(ids).toEqual(["a1", "a2", "a3"]);
      expect(callCount).toBe(2);
    });
  });

  // -----------------------------------------------------------------------
  // lifecycle actions (start, stop, suspend, restart)
  // -----------------------------------------------------------------------
  describe("start", () => {
    it("posts to /agents/:id/start", async () => {
      const [fetchFn, req] = mockFetch(undefined, 204);
      const client = new ScionClient({ baseUrl: BASE_URL, fetch: fetchFn });

      await client.agents.start("agent-1");

      expect(req().method).toBe("POST");
      expect(req().url).toContain("/api/v1/agents/agent-1/start");
    });
  });

  describe("stop", () => {
    it("posts to /agents/:id/stop", async () => {
      const [fetchFn, req] = mockFetch(undefined, 204);
      const client = new ScionClient({ baseUrl: BASE_URL, fetch: fetchFn });

      await client.agents.stop("agent-1");

      expect(req().method).toBe("POST");
      expect(req().url).toContain("/api/v1/agents/agent-1/stop");
    });
  });

  describe("suspend", () => {
    it("posts to /agents/:id/suspend", async () => {
      const [fetchFn, req] = mockFetch(undefined, 204);
      const client = new ScionClient({ baseUrl: BASE_URL, fetch: fetchFn });

      await client.agents.suspend("agent-1");

      expect(req().method).toBe("POST");
      expect(req().url).toContain("/api/v1/agents/agent-1/suspend");
    });
  });

  describe("restart", () => {
    it("posts to /agents/:id/restart", async () => {
      const [fetchFn, req] = mockFetch(undefined, 204);
      const client = new ScionClient({ baseUrl: BASE_URL, fetch: fetchFn });

      await client.agents.restart("agent-1");

      expect(req().method).toBe("POST");
      expect(req().url).toContain("/api/v1/agents/agent-1/restart");
    });
  });

  // -----------------------------------------------------------------------
  // delete
  // -----------------------------------------------------------------------
  describe("delete", () => {
    it("sends DELETE to /agents/:id", async () => {
      const [fetchFn, req] = mockFetch(undefined, 204);
      const client = new ScionClient({ baseUrl: BASE_URL, fetch: fetchFn });

      await client.agents.delete("agent-1");

      expect(req().method).toBe("DELETE");
      expect(req().url).toContain("/api/v1/agents/agent-1");
    });
  });

  // -----------------------------------------------------------------------
  // restore
  // -----------------------------------------------------------------------
  describe("restore", () => {
    it("posts to /agents/:id/restore and returns the agent", async () => {
      const agent = makeAgent({ id: "agent-1", phase: "running" });
      const [fetchFn, req] = mockFetch(agent);
      const client = new ScionClient({ baseUrl: BASE_URL, fetch: fetchFn });

      const result = await client.agents.restore("agent-1");

      expect(req().method).toBe("POST");
      expect(req().url).toContain("/api/v1/agents/agent-1/restore");
      expect(result.id).toBe("agent-1");
    });
  });

  // -----------------------------------------------------------------------
  // sendMessage
  // -----------------------------------------------------------------------
  describe("sendMessage", () => {
    it("posts a plain text message", async () => {
      const [fetchFn, req] = mockFetch(undefined, 204);
      const client = new ScionClient({ baseUrl: BASE_URL, fetch: fetchFn });

      await client.agents.sendMessage("agent-1", "hello");

      expect(req().method).toBe("POST");
      expect(req().url).toContain("/api/v1/agents/agent-1/message");
      expect(req().body).toEqual({ message: "hello", interrupt: false });
    });

    it("passes interrupt=true when specified", async () => {
      const [fetchFn, req] = mockFetch(undefined, 204);
      const client = new ScionClient({ baseUrl: BASE_URL, fetch: fetchFn });

      await client.agents.sendMessage("agent-1", "urgent!", true);

      expect(req().body).toEqual({ message: "urgent!", interrupt: true });
    });
  });

  // -----------------------------------------------------------------------
  // sendStructuredMessage
  // -----------------------------------------------------------------------
  describe("sendStructuredMessage", () => {
    const structuredMsg: StructuredMessage = {
      version: 1,
      timestamp: "2026-05-12T00:00:00Z",
      sender: "user:alice",
      recipient: "agent:code-reviewer",
      msg: "Please review PR #42",
      type: "instruction",
    };

    it("posts a structured message", async () => {
      const [fetchFn, req] = mockFetch(undefined, 204);
      const client = new ScionClient({ baseUrl: BASE_URL, fetch: fetchFn });

      await client.agents.sendStructuredMessage("agent-1", structuredMsg);

      expect(req().method).toBe("POST");
      expect(req().url).toContain("/api/v1/agents/agent-1/message");
      expect(req().body).toEqual({
        structured_message: structuredMsg,
        interrupt: false,
        notify: false,
      });
    });

    it("passes interrupt and notify options", async () => {
      const [fetchFn, req] = mockFetch(undefined, 204);
      const client = new ScionClient({ baseUrl: BASE_URL, fetch: fetchFn });

      await client.agents.sendStructuredMessage("agent-1", structuredMsg, {
        interrupt: true,
        notify: true,
      });

      expect(req().body).toEqual({
        structured_message: structuredMsg,
        interrupt: true,
        notify: true,
      });
    });
  });

  // -----------------------------------------------------------------------
  // broadcastMessage
  // -----------------------------------------------------------------------
  describe("broadcastMessage", () => {
    const broadcastMsg: StructuredMessage = {
      version: 1,
      timestamp: "2026-05-12T00:00:00Z",
      sender: "user:alice",
      recipient: "all",
      msg: "Wrap up",
      type: "instruction",
    };

    it("posts to the project broadcast endpoint", async () => {
      const [fetchFn, req] = mockFetch(undefined, 204);
      const client = new ScionClient({
        baseUrl: BASE_URL,
        fetch: fetchFn,
        projectId: "proj-1",
      });

      await client.agents.broadcastMessage(broadcastMsg);

      expect(req().method).toBe("POST");
      expect(req().url).toContain("/api/v1/projects/proj-1/broadcast");
      expect(req().body).toEqual({
        structured_message: broadcastMsg,
        interrupt: false,
      });
    });

    it("passes interrupt=true", async () => {
      const [fetchFn, req] = mockFetch(undefined, 204);
      const client = new ScionClient({
        baseUrl: BASE_URL,
        fetch: fetchFn,
        projectId: "proj-1",
      });

      await client.agents.broadcastMessage(broadcastMsg, true);

      expect(req().body).toEqual({
        structured_message: broadcastMsg,
        interrupt: true,
      });
    });

    it("throws when client is not project-scoped", async () => {
      const [fetchFn] = mockFetch(undefined, 204);
      const client = new ScionClient({ baseUrl: BASE_URL, fetch: fetchFn });

      await expect(
        client.agents.broadcastMessage(broadcastMsg),
      ).rejects.toThrow("broadcastMessage requires a project-scoped client");
    });
  });

  // -----------------------------------------------------------------------
  // Authentication
  // -----------------------------------------------------------------------
  describe("authentication", () => {
    it("sends bearer token in Authorization header", async () => {
      const [fetchFn, req] = mockFetch(makeAgent());
      const client = new ScionClient({
        baseUrl: BASE_URL,
        fetch: fetchFn,
        token: "my-secret-token",
      });

      await client.agents.get("agent-1");

      expect(req().headers.Authorization).toBe("Bearer my-secret-token");
    });

    it("uses custom auth provider", async () => {
      const [fetchFn, req] = mockFetch(makeAgent());
      const client = new ScionClient({
        baseUrl: BASE_URL,
        fetch: fetchFn,
        auth: () => ({ "X-Custom-Auth": "custom-value" }),
      });

      await client.agents.get("agent-1");

      expect(req().headers["X-Custom-Auth"]).toBe("custom-value");
    });
  });

  // -----------------------------------------------------------------------
  // Error handling
  // -----------------------------------------------------------------------
  describe("error handling", () => {
    it("throws ValidationError for 400", async () => {
      const fetchFn = mockFetchError(400, { error: "invalid name" });
      const client = new ScionClient({ baseUrl: BASE_URL, fetch: fetchFn });

      await expect(
        client.agents.create({ name: "", projectId: "p" }),
      ).rejects.toThrow(ValidationError);
    });

    it("includes error detail in the message", async () => {
      const fetchFn = mockFetchError(404, { error: "agent xyz not found" });
      const client = new ScionClient({ baseUrl: BASE_URL, fetch: fetchFn });

      try {
        await client.agents.get("xyz");
        expect.fail("should have thrown");
      } catch (err) {
        expect(err).toBeInstanceOf(NotFoundError);
        const apiErr = err as ApiError;
        expect(apiErr.status).toBe(404);
        expect(apiErr.detail).toBe("agent xyz not found");
        expect(apiErr.message).toContain("agent xyz not found");
      }
    });

    it("handles non-JSON error bodies gracefully", async () => {
      const fetchFn = vi.fn(async () => {
        return new Response("Internal Server Error", {
          status: 500,
          statusText: "Internal Server Error",
        });
      }) as unknown as typeof globalThis.fetch;

      const client = new ScionClient({ baseUrl: BASE_URL, fetch: fetchFn });

      try {
        await client.agents.get("x");
        expect.fail("should have thrown");
      } catch (err) {
        expect(err).toBeInstanceOf(ApiError);
        expect((err as ApiError).status).toBe(500);
        expect((err as ApiError).body).toBe("Internal Server Error");
      }
    });
  });

  // -----------------------------------------------------------------------
  // Project-scoped paths
  // -----------------------------------------------------------------------
  describe("project-scoped paths", () => {
    it("scopes all paths when projectId is set on the client", async () => {
      const [fetchFn, req] = mockFetch(undefined, 204);
      const client = new ScionClient({
        baseUrl: BASE_URL,
        fetch: fetchFn,
        projectId: "my-project",
      });

      await client.agents.start("a1");
      expect(req().url).toContain(
        "/api/v1/projects/my-project/agents/a1/start",
      );
    });

    it("does not scope when projectId is not set", async () => {
      const [fetchFn, req] = mockFetch(undefined, 204);
      const client = new ScionClient({ baseUrl: BASE_URL, fetch: fetchFn });

      await client.agents.stop("a1");
      expect(req().url).toContain("/api/v1/agents/a1/stop");
      expect(req().url).not.toContain("/projects/");
    });
  });
});
