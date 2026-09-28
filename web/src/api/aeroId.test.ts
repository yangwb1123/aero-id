import { describe, expect, it, vi } from "vitest";
import { AeroIdClient, AeroIdError } from "./aeroId";

function json(body: unknown, init: ResponseInit = {}): Response {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { "Content-Type": "application/json", ...(init.headers ?? {}) },
    ...init,
  });
}

describe("AeroIdClient", () => {
  it("rejects successful responses that violate the data envelope contract", async () => {
    const client = new AeroIdClient(
      "https://accounts.example.test/v1",
      () => "token",
      vi.fn(async () => json({ status: "ok" })) as typeof fetch,
    );

    await expect(client.getProfile()).rejects.toMatchObject({
      code: "response.invalid",
      status: 0,
    });
  });

  it("sends bearer, request, idempotency and read-your-writes headers", async () => {
    const requests: Array<{
      url: string;
      headers: Headers;
      body?: BodyInit | null;
    }> = [];
    const fetcher = vi.fn(
      async (input: RequestInfo | URL, init?: RequestInit) => {
        requests.push({
          url: String(input),
          headers: new Headers(init?.headers),
          body: init?.body,
        });
        if (requests.length === 1) {
          return json(
            { data: { id: "job-1", status: "pending" } },
            { status: 201, headers: { "X-Aero-Write-Epoch": "local:7" } },
          );
        }
        return json({
          data: {
            datasets: [
              { name: "aero-im.workspaces", source: "aero-im", pii: false },
            ],
          },
        });
      },
    );
    const client = new AeroIdClient(
      "https://accounts.example.test/v1",
      () => "access-token",
      fetcher as typeof fetch,
    );

    await client.createSyncJob(["aero-im.workspaces"], "sync-job-key");
    await client.listDatasets();

    expect(requests[0].headers.get("Authorization")).toBe(
      "Bearer access-token",
    );
    expect(requests[0].headers.get("Idempotency-Key")).toBe("sync-job-key");
    expect(requests[0].headers.get("X-Request-ID")).toBeTruthy();
    expect(requests[1].headers.get("X-Aero-Write-Epoch")).toBe("local:7");
    const body = JSON.parse(String(requests[0].body));
    expect(body.idempotency_key).toBe(
      requests[0].headers.get("Idempotency-Key"),
    );
  });

  it("encodes datasets as repeated allow-listed query parameters", async () => {
    let requested = new URL("https://invalid.test");
    const fetcher = vi.fn(async (input: RequestInfo | URL) => {
      requested = new URL(String(input));
      return json({ data: { snapshots: [], partial: false } });
    });
    const client = new AeroIdClient(
      "https://accounts.example.test/v1",
      () => "token",
      fetcher as typeof fetch,
    );

    await client.getResources(["aero-im.workspaces", "aero-vault.usage"]);

    expect(requested.pathname).toBe("/v1/me/resources");
    expect(requested.searchParams.getAll("dataset")).toEqual([
      "aero-im.workspaces",
      "aero-vault.usage",
    ]);
    expect(requested.searchParams.get("consistency")).toBe("eventual");
  });

  it("adapts the legacy activity snapshot envelope during rolling upgrades", async () => {
    const fetcher = vi.fn(async () =>
      json({
        data: {
          snapshots: [
            {
              dataset: "aero-im.activity_summary",
              data: {
                events: [{ event_id: "event-1", event_type: "message.sent" }],
                next_cursor: "event-1",
              },
            },
          ],
        },
      }),
    );
    const client = new AeroIdClient(
      "https://accounts.example.test/v1",
      () => "token",
      fetcher as typeof fetch,
    );

    await expect(client.listActivity()).resolves.toEqual({
      items: [{ event_id: "event-1", event_type: "message.sent" }],
      nextCursor: "event-1",
    });
  });

  it("reads the paged activity envelope and passes its opaque cursor", async () => {
    let requested = new URL("https://invalid.test");
    const fetcher = vi.fn(async (input: RequestInfo | URL) => {
      requested = new URL(String(input));
      return json({
        data: {
          events: [{ event_id: "event-1", event_type: "message.sent" }],
          next_cursor: "event-1",
        },
      });
    });
    const client = new AeroIdClient(
      "https://accounts.example.test/v1",
      () => "token",
      fetcher as typeof fetch,
    );

    await expect(client.listActivity("event-2")).resolves.toEqual({
      items: [{ event_id: "event-1", event_type: "message.sent" }],
      nextCursor: "event-1",
    });
    expect(requested.pathname).toBe("/v1/me/activity");
    expect(requested.searchParams.get("cursor")).toBe("event-2");
    expect(requested.searchParams.get("limit")).toBe("50");
  });

  it("passes opaque keyset cursors without interpreting them", async () => {
    let requested = new URL("https://invalid.test");
    const fetcher = vi.fn(async (input: RequestInfo | URL) => {
      requested = new URL(String(input));
      return json({ data: { sources: [], next_cursor: "" } });
    });
    const client = new AeroIdClient(
      "https://accounts.example.test/v1",
      () => "token",
      fetcher as typeof fetch,
    );

    await client.listSources("opaque:source:cursor");

    expect(requested.pathname).toBe("/v1/me/sources");
    expect(requested.searchParams.get("cursor")).toBe("opaque:source:cursor");
    expect(requested.searchParams.get("limit")).toBe("50");
  });

  it("filters admin accounts and loads an encoded account freshness view", async () => {
    const requests: URL[] = [];
    const fetcher = vi.fn(async (input: RequestInfo | URL) => {
      requests.push(new URL(String(input)));
      if (requests.length === 1)
        return json({ data: { accounts: [], next_cursor: "account-1" } });
      if (requests.length === 2)
        return json({ data: { account: { account_id: "account/1" } } });
      return json({
        data: { account: { account_id: "account/1" }, snapshots: [] },
      });
    });
    const client = new AeroIdClient(
      "https://accounts.example.test/v1",
      () => "token",
      fetcher as typeof fetch,
    );

    await expect(
      client.listAdminAccounts(
        { query: "uid@example.test", status: "active", homeRegion: "west" },
        "account:cursor",
      ),
    ).resolves.toEqual({ items: [], nextCursor: "account-1" });
    await client.getAdminAccount("account/1");
    await client.getAdminAccountOverview("account/1");

    expect(requests[0].pathname).toBe("/v1/admin/accounts");
    expect(requests[0].searchParams.get("q")).toBe("uid@example.test");
    expect(requests[0].searchParams.get("status")).toBe("active");
    expect(requests[0].searchParams.get("home_region")).toBe("west");
    expect(requests[0].searchParams.get("cursor")).toBe("account:cursor");
    expect(requests[1].pathname).toBe("/v1/admin/accounts/account%2F1");
    expect(requests[2].pathname).toBe(
      "/v1/admin/accounts/account%2F1/overview",
    );
    expect(requests[2].searchParams.get("consistency")).toBe("eventual");
  });

  it("creates idempotent admin sync, export and erase jobs", async () => {
    const requests: Array<{ url: URL; headers: Headers; body: string }> = [];
    const fetcher = vi.fn(
      async (input: RequestInfo | URL, init?: RequestInit) => {
        requests.push({
          url: new URL(String(input)),
          headers: new Headers(init?.headers),
          body: String(init?.body),
        });
        return json({
          data: { job_id: `job-${requests.length}`, status: "pending" },
        });
      },
    );
    const client = new AeroIdClient(
      "https://accounts.example.test/v1",
      () => "token",
      fetcher as typeof fetch,
    );

    await client.createAdminAccountJob("account/1", "sync", "sync-key");
    await client.createAdminAccountJob("account/1", "export", "export-key");
    await client.createAdminAccountJob("account/1", "erase", "erase-key");

    expect(requests.map((request) => request.url.pathname)).toEqual([
      "/v1/admin/accounts/account%2F1/sync",
      "/v1/admin/accounts/account%2F1/export",
      "/v1/admin/accounts/account%2F1/erase",
    ]);
    for (const request of requests) {
      const body = JSON.parse(request.body);
      expect(body.datasets).toEqual([]);
      expect(body.idempotency_key).toBe(request.headers.get("Idempotency-Key"));
    }
    expect(
      requests.map((request) => request.headers.get("Idempotency-Key")),
    ).toEqual(["sync-key", "export-key", "erase-key"]);
  });

  it("rejects a missing sync-job key before making a request", async () => {
    const fetcher = vi.fn(async () => json({ data: {} }));
    const client = new AeroIdClient(
      "https://accounts.example.test/v1",
      () => "token",
      fetcher as typeof fetch,
    );

    await expect(client.createSyncJob([], " ")).rejects.toThrow(
      "Idempotency-Key",
    );
    expect(fetcher).not.toHaveBeenCalled();
  });

  it("creates a self-erase job with a fresh idempotency key", async () => {
    let requested: { url: URL; headers: Headers; body: string } | undefined;
    const fetcher = vi.fn(
      async (input: RequestInfo | URL, init?: RequestInit) => {
        requested = {
          url: new URL(String(input)),
          headers: new Headers(init?.headers),
          body: String(init?.body),
        };
        return json(
          { data: { job_id: "erase-job-1", status: "pending" } },
          { status: 202 },
        );
      },
    );
    const client = new AeroIdClient(
      "https://accounts.example.test/v1",
      () => "token",
      fetcher as typeof fetch,
    );

    await expect(client.eraseMyAccount()).resolves.toEqual({
      job_id: "erase-job-1",
      status: "pending",
    });
    expect(requested?.url.pathname).toBe("/v1/me/erase");
    expect(requested?.headers.get("Idempotency-Key")).toBeTruthy();
    expect(JSON.parse(requested?.body ?? "{}").idempotency_key).toBe(
      requested?.headers.get("Idempotency-Key"),
    );
  });

  it("loads and reconciles a sync job through its encoded resource path", async () => {
    const requests: Array<{ url: URL; method?: string }> = [];
    const fetcher = vi.fn(
      async (input: RequestInfo | URL, init?: RequestInit) => {
        requests.push({ url: new URL(String(input)), method: init?.method });
        return json({ data: { job_id: "job/1", status: "unknown" } });
      },
    );
    const client = new AeroIdClient(
      "https://accounts.example.test/v1",
      () => "token",
      fetcher as typeof fetch,
    );

    await client.getSyncJob("job/1");
    await client.reconcileSyncJob("job/1");

    expect(requests[0].url.pathname).toBe("/v1/sync/jobs/job%2F1");
    expect(requests[0].method).toBe("GET");
    expect(requests[1].url.pathname).toBe("/v1/sync/jobs/job%2F1/reconcile");
    expect(requests[1].method).toBe("POST");
  });

  it("loads an operation, reconciles unknown state and pages its audit timeline", async () => {
    const requests: Array<{ url: URL; method?: string }> = [];
    const fetcher = vi.fn(
      async (input: RequestInfo | URL, init?: RequestInit) => {
        requests.push({ url: new URL(String(input)), method: init?.method });
        if (requests.length === 3) {
          return json({
            data: {
              events: [{ event_id: "event-1", action: "operation.created" }],
              next_cursor: "event-1",
            },
          });
        }
        return json({
          data: { operation_id: "operation/1", status: "unknown" },
        });
      },
    );
    const client = new AeroIdClient(
      "https://accounts.example.test/v1",
      () => "token",
      fetcher as typeof fetch,
    );

    await client.getOperation("operation/1");
    await client.reconcileOperation("operation/1");
    await expect(
      client.listOperationAudit("operation/1", "audit:cursor"),
    ).resolves.toEqual({
      items: [{ event_id: "event-1", action: "operation.created" }],
      nextCursor: "event-1",
    });

    expect(requests[0].url.pathname).toBe("/v1/operations/operation%2F1");
    expect(requests[0].method).toBe("GET");
    expect(requests[1].url.pathname).toBe(
      "/v1/operations/operation%2F1/reconcile",
    );
    expect(requests[1].method).toBe("POST");
    expect(requests[2].url.pathname).toBe("/v1/audit/operations/operation%2F1");
    expect(requests[2].url.searchParams.get("cursor")).toBe("audit:cursor");
    expect(requests[2].url.searchParams.get("limit")).toBe("50");
  });

  it("filters audit facts and verifies a source partition chain", async () => {
    const requests: URL[] = [];
    const fetcher = vi.fn(async (input: RequestInfo | URL) => {
      requests.push(new URL(String(input)));
      if (requests.length === 1)
        return json({
          data: { events: [{ event_id: "event-1" }], next_cursor: "event-1" },
        });
      return json({
        data: { source: "aero-id", partition: "tenant-1", verified: true },
      });
    });
    const client = new AeroIdClient(
      "https://accounts.example.test/v1",
      () => "token",
      fetcher as typeof fetch,
    );

    await client.listAuditEvents(
      {
        source: "aero-id",
        action: "operation.completed",
        operationID: "operation-1",
        accountID: "account-1",
        requestID: "request-1",
      },
      "event:cursor",
    );
    await expect(
      client.verifyAuditChain("aero-id", "tenant/partition"),
    ).resolves.toMatchObject({ verified: true });

    expect(requests[0].pathname).toBe("/v1/audit/events");
    expect(requests[0].searchParams.get("source")).toBe("aero-id");
    expect(requests[0].searchParams.get("action")).toBe("operation.completed");
    expect(requests[0].searchParams.get("operation_id")).toBe("operation-1");
    expect(requests[0].searchParams.get("account_id")).toBe("account-1");
    expect(requests[0].searchParams.get("request_id")).toBe("request-1");
    expect(requests[0].searchParams.get("cursor")).toBe("event:cursor");
    expect(requests[1].pathname).toBe("/v1/audit/verify");
    expect(requests[1].searchParams.get("source")).toBe("aero-id");
    expect(requests[1].searchParams.get("partition")).toBe("tenant/partition");
  });

  it("reads bounded account data and creates/runs a durable operation", async () => {
    const requests: Array<{
      url: URL;
      method?: string;
      headers: Headers;
      body?: string;
    }> = [];
    const fetcher = vi.fn(
      async (input: RequestInfo | URL, init?: RequestInit) => {
        requests.push({
          url: new URL(String(input)),
          method: init?.method,
          headers: new Headers(init?.headers),
          body: init?.body === undefined ? undefined : String(init.body),
        });
        return json({
          data: {
            operation_id: "operation-1",
            status: "requested",
            snapshots: [],
          },
        });
      },
    );
    const client = new AeroIdClient(
      "https://accounts.example.test/v1",
      () => "token",
      fetcher as typeof fetch,
    );

    await client.getMyData({
      datasets: ["aero-im.profile"],
      consistency: "bounded",
      maxAge: "5m",
    });
    await client.getAdminAccountData("account/1", { consistency: "strong" });
    await client.createOperation({
      accountID: "account-1",
      operationType: "sync",
      idempotencyKey: "operation-key-1",
      steps: [
        {
          stepKey: "profile-summary",
          source: "aero-im",
          action: "summary",
          requestPayload: { datasets: ["aero-im.profile"] },
        },
      ],
    });
    await client.runOperation("operation/1");

    expect(requests[0].url.pathname).toBe("/v1/me/data");
    expect(requests[0].url.searchParams.getAll("dataset")).toEqual([
      "aero-im.profile",
    ]);
    expect(requests[0].url.searchParams.get("consistency")).toBe("bounded");
    expect(requests[0].url.searchParams.get("max_age")).toBe("5m");
    expect(requests[1].url.pathname).toBe(
      "/v1/admin/accounts/account%2F1/data",
    );
    expect(requests[1].url.searchParams.get("consistency")).toBe("strong");
    expect(requests[2].url.pathname).toBe("/v1/operations");
    expect(requests[2].headers.get("Idempotency-Key")).toBe("operation-key-1");
    expect(JSON.parse(requests[2].body ?? "{}").steps[0]).toEqual({
      step_key: "profile-summary",
      source: "aero-im",
      action: "summary",
      request_payload: { datasets: ["aero-im.profile"] },
    });
    expect(requests[3].url.pathname).toBe("/v1/operations/operation%2F1/run");
    expect(requests[3].method).toBe("POST");
  });

  it("covers the published admin audit, role and ban routes", async () => {
    const requests: Array<{
      url: URL;
      method?: string;
      body?: string;
      headers: Headers;
    }> = [];
    const fetcher = vi.fn(
      async (input: RequestInfo | URL, init?: RequestInit) => {
        requests.push({
          url: new URL(String(input)),
          method: init?.method,
          body: init?.body === undefined ? undefined : String(init.body),
          headers: new Headers(init?.headers),
        });
        if (requests.length === 1)
          return json({ data: { events: [], next_cursor: "audit-1" } });
        if (requests.length === 2) return json({ data: { roles: [] } });
        if (requests.length === 3) return json({ data: { roles: [] } });
        if (requests.length === 6)
          return json({ data: { bans: [], next_cursor: "ban-1" } });
        return json({ data: { status: "ok" } });
      },
    );
    const client = new AeroIdClient(
      "https://accounts.example.test/v1",
      () => "token",
      fetcher as typeof fetch,
    );

    await client.listAdminAudit("admin.role.grant", "audit-0");
    await client.listRoles();
    await client.listUserRoles("uid/1");
    await client.grantRole("uid/1", "operator", "role-grant-key");
    await client.revokeRole("uid/1", "operator", "role-revoke-key");
    await client.listBanned("ban-0");
    await client.banUser("uid/1", "manual review", "24h", "ban-key");
    await client.unbanUser("uid/1");

    expect(requests.map((request) => request.url.pathname)).toEqual([
      "/v1/admin/audit-log",
      "/v1/admin/roles",
      "/v1/admin/roles/uid%2F1",
      "/v1/admin/roles/grant",
      "/v1/admin/roles/revoke",
      "/v1/admin/bans",
      "/v1/admin/bans",
      "/v1/admin/bans/uid%2F1",
    ]);
    expect(requests[0].url.searchParams.get("action")).toBe("admin.role.grant");
    expect(requests[3].body).toBe(
      JSON.stringify({ uid: "uid/1", role_code: "operator" }),
    );
    expect(requests[3].headers.get("Idempotency-Key")).toBe("role-grant-key");
    expect(requests[4].headers.get("Idempotency-Key")).toBe("role-revoke-key");
    expect(requests[6].body).toBe(
      JSON.stringify({
        uid: "uid/1",
        reason: "manual review",
        duration: "24h",
      }),
    );
    expect(requests[6].headers.get("Idempotency-Key")).toBe("ban-key");
  });

  it("downloads an export with authorization and one-time semantics", async () => {
    let requested = new URL("https://invalid.test");
    let headers = new Headers();
    const fetcher = vi.fn(
      async (input: RequestInfo | URL, init?: RequestInit) => {
        requested = new URL(String(input));
        headers = new Headers(init?.headers);
        return new Response('{"export":true}', {
          headers: {
            "Content-Type": "application/json",
            "Content-Length": "15",
          },
        });
      },
    );
    const client = new AeroIdClient(
      "https://accounts.example.test/v1",
      () => "download-token",
      fetcher as typeof fetch,
    );

    const blob = await client.downloadExport("job-1", true);

    expect(requested.pathname).toBe("/v1/sync/jobs/job-1/export/download");
    expect(requested.searchParams.get("one_time")).toBe("true");
    expect(headers.get("Authorization")).toBe("Bearer download-token");
    expect(blob.size).toBe(15);
  });

  it("rejects an export declared above the browser size limit", async () => {
    const fetcher = vi.fn(
      async () =>
        new Response("", {
          headers: { "Content-Length": String(64 * 1024 * 1024 + 1) },
        }),
    );
    const client = new AeroIdClient(
      "https://accounts.example.test/v1",
      () => "token",
      fetcher as typeof fetch,
    );

    await expect(client.downloadExport("job-1")).rejects.toMatchObject({
      code: "response.too_large",
    });
  });

  it("preserves stable API error metadata without exposing the bearer token", async () => {
    const fetcher = vi.fn(async () =>
      json(
        {
          error: {
            code: "account.projection_stale",
            message: "Projection is stale",
            request_id: "req-7",
          },
        },
        { status: 503 },
      ),
    );
    const client = new AeroIdClient(
      "https://accounts.example.test/v1",
      () => "secret-token",
      fetcher as typeof fetch,
    );

    const error = await client.getMe().catch((reason: unknown) => reason);

    expect(error).toBeInstanceOf(AeroIdError);
    expect(error).toMatchObject({
      status: 503,
      code: "account.projection_stale",
      requestId: "req-7",
    });
    expect(String(error)).not.toContain("secret-token");
  });
});
