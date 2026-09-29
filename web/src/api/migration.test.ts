import { describe, expect, it, vi } from "vitest";
import { MigrationClient } from "./migration";
import { AeroIdTransport } from "./transport";

function json(data: unknown): Response {
  return new Response(JSON.stringify({ data }), {
    headers: { "Content-Type": "application/json" },
  });
}

describe("MigrationClient", () => {
  it("uses the published compare and tenant inspection contracts", async () => {
    const requests: Array<{ url: URL; init?: RequestInit }> = [];
    const fetcher = vi.fn(
      async (input: RequestInfo | URL, init?: RequestInit) => {
        requests.push({ url: new URL(String(input)), init });
        return json({ status: "shadow", checks: [] });
      },
    );
    const client = new MigrationClient(
      new AeroIdTransport(
        "https://accounts.example.test/v1",
        () => "token",
        fetcher as typeof fetch,
      ),
    );

    await client.compare({
      ownerUID: "user-1",
      source: "aero-vault",
      limit: 500,
      idempotencyKey: "compare-key",
    });
    await client.getCutover("tenant:a");
    await client.getCleanupReadiness("tenant:a");

    expect(requests[0].url.pathname).toBe("/v1/admin/migration/legacy/compare");
    expect(new Headers(requests[0].init?.headers).get("Idempotency-Key")).toBe(
      "compare-key",
    );
    expect(JSON.parse(String(requests[0].init?.body))).toEqual({
      owner_uid: "user-1",
      source: "aero-vault",
      limit: 500,
    });
    expect(requests[1].url.pathname).toBe(
      "/v1/admin/migration/legacy/cutover/tenant%3Aa",
    );
    expect(requests[2].url.pathname).toBe(
      "/v1/admin/migration/legacy/cleanup-readiness/tenant%3Aa",
    );
  });

  it("rejects a compare request without an idempotency key before fetching", async () => {
    const fetcher = vi.fn(async () => json({}));
    const client = new MigrationClient(
      new AeroIdTransport(
        "https://accounts.example.test/v1",
        () => "token",
        fetcher as typeof fetch,
      ),
    );

    await expect(
      client.compare({
        ownerUID: "user-1",
        source: "aero-vault",
        limit: 500,
        idempotencyKey: " ",
      }),
    ).rejects.toThrow("Idempotency-Key");
    expect(fetcher).not.toHaveBeenCalled();
  });

  it("keeps fixed idempotency keys on cutover and backfill commands", async () => {
    const requests: RequestInit[] = [];
    const fetcher = vi.fn(
      async (_input: RequestInfo | URL, init?: RequestInit) => {
        requests.push(init ?? {});
        return json({ completed: true });
      },
    );
    const client = new MigrationClient(
      new AeroIdTransport(
        "https://accounts.example.test/v1",
        () => "token",
        fetcher as typeof fetch,
      ),
    );

    await client.setCutover({
      tenantID: "tenant-a",
      status: "cut_over",
      note: "approved change",
      idempotencyKey: "cutover-key",
    });
    await client.backfillLegacyIdentities(500, "backfill-key");

    expect(new Headers(requests[0].headers).get("Idempotency-Key")).toBe(
      "cutover-key",
    );
    expect(JSON.parse(String(requests[0].body))).toEqual({
      tenant_id: "tenant-a",
      status: "cut_over",
      note: "approved change",
    });
    expect(new Headers(requests[1].headers).get("Idempotency-Key")).toBe(
      "backfill-key",
    );
    expect(JSON.parse(String(requests[1].body))).toEqual({ limit: 500 });
  });
});
