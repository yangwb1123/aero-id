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

    await client.createSyncJob(["aero-im.workspaces"]);
    await client.listDatasets();

    expect(requests[0].headers.get("Authorization")).toBe(
      "Bearer access-token",
    );
    expect(requests[0].headers.get("Idempotency-Key")).toBeTruthy();
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
