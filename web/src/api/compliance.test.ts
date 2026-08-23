import { describe, expect, it, vi } from "vitest";
import { ComplianceClient } from "./compliance";
import { AeroIdTransport } from "./transport";

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

describe("ComplianceClient", () => {
  it("uses legal-hold routes and preserves their published field names", async () => {
    const requests: Array<{
      url: URL;
      method?: string;
      body?: BodyInit | null;
    }> = [];
    const fetcher = vi.fn(
      async (input: RequestInfo | URL, init?: RequestInit) => {
        requests.push({
          url: new URL(String(input)),
          method: init?.method,
          body: init?.body,
        });
        if (requests.length === 1)
          return json({ data: { holds: [{ scope_id: "account/1" }] } });
        if (requests.length === 2)
          return json(
            { data: { scope_type: "account", scope_id: "account/1" } },
            201,
          );
        return json({ data: { removed: true } });
      },
    );
    const client = new ComplianceClient(
      new AeroIdTransport(
        "https://accounts.example.test/v1",
        () => "token",
        fetcher as typeof fetch,
      ),
    );

    await expect(client.listHolds()).resolves.toEqual([
      { scope_id: "account/1" },
    ]);
    await client.putHold({
      scopeType: "account",
      scopeID: "account/1",
      reason: "case-42",
      expiresAt: "2027-01-02T03:04:05.000Z",
    });
    await client.removeHold("account", "account/1");

    expect(requests[0].url.pathname).toBe("/v1/admin/audit/holds");
    expect(JSON.parse(String(requests[1].body))).toEqual({
      scope_type: "account",
      scope_id: "account/1",
      reason: "case-42",
      expires_at: "2027-01-02T03:04:05.000Z",
    });
    expect(requests[2].method).toBe("DELETE");
    expect(requests[2].url.pathname).toBe(
      "/v1/admin/audit/holds/account/account%2F1",
    );
  });

  it("sends one audit-export idempotency key in the header and body", async () => {
    let request: RequestInit | undefined;
    const fetcher = vi.fn(
      async (_input: RequestInfo | URL, init?: RequestInit) => {
        request = init;
        return json({ data: { job_id: "job-1", status: "pending" } }, 201);
      },
    );
    const client = new ComplianceClient(
      new AeroIdTransport(
        "https://accounts.example.test/v1",
        () => "token",
        fetcher as typeof fetch,
      ),
    );

    await expect(client.createAuditExport()).resolves.toEqual({
      job_id: "job-1",
      status: "pending",
    });

    const headerKey = new Headers(request?.headers).get("Idempotency-Key");
    expect(headerKey).toBeTruthy();
    expect(JSON.parse(String(request?.body))).toEqual({
      idempotency_key: headerKey,
    });
  });
});
