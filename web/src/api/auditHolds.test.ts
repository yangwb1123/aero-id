import { afterEach, describe, expect, it, vi } from "vitest";
import { ComplianceClient } from "./compliance";
import { AeroIdError, AeroIdTransport } from "./transport";

const marker = "untrusted-hold-row-marker";
const row = {
  scope_type: "account",
  scope_id: "original-account",
  reason: marker,
  created_by: "operator",
  created_at: "2026-10-16T01:00:00Z",
  expires_at: null,
};
function json(body: unknown, init: ResponseInit = {}): Response {
  return new Response(JSON.stringify(body), init);
}
function setup(fetcher: typeof fetch) {
  const transport = new AeroIdTransport(
    "https://accounts.example.test/v1",
    () => "test-session",
    fetcher,
  );
  return { transport, client: new ComplianceClient(transport) };
}
function expectInvalid(error: unknown): void {
  expect(error).toBeInstanceOf(AeroIdError);
  expect(error).toMatchObject({ status: 0, code: "response.invalid" });
  expect(String(error)).not.toContain(marker);
  expect(String(error)).not.toContain("original-account");
}
const badLists = [
  { label: "missing", data: {} },
  { label: "null", data: { holds: null } },
  { label: "object", data: { holds: row } },
  { label: "string", data: { holds: marker } },
  { label: "number", data: { holds: 7 } },
  { label: "boolean", data: { holds: false } },
  { label: "alias", data: { items: [row] } },
  { label: "nested", data: { result: { holds: [row] } } },
  { label: "different list", data: { events: [row] } },
];
const badRows = [
  { label: "null", value: null },
  { label: "array", value: [] },
  { label: "string", value: marker },
  { label: "number", value: 7 },
  { label: "boolean", value: true },
];

afterEach(() => {
  vi.restoreAllMocks();
  vi.useRealTimers();
});

describe("Legal Hold list adopts only a root object array", () => {
  it.each(badLists)(
    "rejects $label holds instead of an empty list",
    async ({ data }) => {
      const fetcher = vi.fn(async () => json({ data }));
      const { client } = setup(fetcher);
      expectInvalid(
        await client.listHolds().catch((reason: unknown) => reason),
      );
      expect(fetcher).toHaveBeenCalledTimes(1);
    },
  );

  it.each(badRows)(
    "rejects a whole list containing a $label rather than filtering it",
    async ({ value }) => {
      const fetcher = vi.fn(async () =>
        json({ data: { holds: [row, value, row] } }),
      );
      const { client } = setup(fetcher);
      expectInvalid(
        await client.listHolds().catch((reason: unknown) => reason),
      );
      expect(fetcher).toHaveBeenCalledTimes(1);
    },
  );

  it.each([
    { label: "null", data: null },
    { label: "array", data: [] },
    { label: "string", data: marker },
    { label: "number", data: 7 },
    { label: "missing", data: undefined },
  ])("retains data-envelope rejection for $label data", async ({ data }) => {
    const fetcher = vi.fn(async () => json({ data }));
    const { client } = setup(fetcher);
    expectInvalid(await client.listHolds().catch((reason: unknown) => reason));
    expect(fetcher).toHaveBeenCalledTimes(1);
  });

  it("accepts a real empty array without inventing records", async () => {
    const fetcher = vi.fn(async () => json({ data: { holds: [] } }));
    const { client } = setup(fetcher);
    await expect(client.listHolds()).resolves.toEqual([]);
    expect(fetcher).toHaveBeenCalledTimes(1);
  });

  it("preserves row order, fields and the original GET-only request", async () => {
    const rows = [
      row,
      { ...row, scope_id: "other-account", expires_at: "2027-01-02T03:04:05Z" },
    ];
    const fetcher = vi.fn(
      async (_input: RequestInfo | URL, _init?: RequestInit) =>
        json({ data: { holds: rows } }),
    );
    const { client } = setup(fetcher);
    await expect(client.listHolds()).resolves.toEqual(rows);
    expect(fetcher).toHaveBeenCalledTimes(1);
    const [input, init] = fetcher.mock.calls[0];
    const url = new URL(String(input));
    expect(url.pathname).toBe("/v1/admin/audit/holds");
    expect(url.search).toBe("");
    expect(init?.method).toBe("GET");
    expect(init?.body).toBeUndefined();
    expect(init?.credentials).toBe("omit");
    expect(new Headers(init?.headers).get("Idempotency-Key")).toBeNull();
    expect(new Headers(init?.headers).get("X-Request-ID")).toBeTruthy();
  });

  it("does not claim full row validation, deduplication or expiry/tenant decisions", async () => {
    const rows = [{}, { scope_type: "future-kind" }, row, row];
    const fetcher = vi.fn(async () => json({ data: { holds: rows } }));
    const { client } = setup(fetcher);
    await expect(client.listHolds()).resolves.toEqual(rows);
  });

  it.each([403, 503])(
    "preserves HTTP %i ahead of local list validation",
    async (status) => {
      const fetcher = vi.fn(async () =>
        json(
          {
            error: {
              code: "audit_hold.list_failed",
              message: "Hold query unavailable",
            },
          },
          { status },
        ),
      );
      const { client } = setup(fetcher);
      await expect(client.listHolds()).rejects.toMatchObject({
        status,
        code: "audit_hold.list_failed",
      });
      expect(fetcher).toHaveBeenCalledTimes(1);
    },
  );

  it("preserves stale-read refusal ahead of local list validation", async () => {
    const fetcher = vi.fn(async () =>
      json({ data: {} }, { headers: { "X-Aero-Stale-Read": "true" } }),
    );
    const { client } = setup(fetcher);
    await expect(client.listHolds()).rejects.toMatchObject({
      code: "region.stale_read",
    });
    expect(fetcher).toHaveBeenCalledTimes(1);
  });

  it.each(["fetch", "body"])(
    "retains client-wide cancellation during %s without another GET",
    async (phase) => {
      let signal: AbortSignal | null | undefined;
      const fetcher = vi.fn(
        async (_input: RequestInfo | URL, init?: RequestInit) => {
          signal = init?.signal;
          if (phase === "fetch")
            return new Promise<Response>((_resolve, reject) =>
              signal?.addEventListener(
                "abort",
                () => reject(new Error("test abort")),
                { once: true },
              ),
            );
          return new Response(
            new ReadableStream<Uint8Array>({
              start(controller) {
                signal?.addEventListener(
                  "abort",
                  () => controller.error(new Error("test abort")),
                  { once: true },
                );
              },
            }),
          );
        },
      );
      const { transport, client } = setup(fetcher);
      const result = client.listHolds().catch((reason: unknown) => reason);
      await Promise.resolve();
      await Promise.resolve();
      transport.cancelPendingReads();
      expect(signal?.aborted).toBe(true);
      expect(await result).toMatchObject({ code: "request.canceled" });
      expect(fetcher).toHaveBeenCalledTimes(1);
    },
  );

  it("retains the original 15-second read deadline without retry", async () => {
    vi.useFakeTimers();
    const fetcher = vi.fn(
      async (_input: RequestInfo | URL, init?: RequestInit) =>
        new Promise<Response>((_resolve, reject) =>
          init?.signal?.addEventListener(
            "abort",
            () => reject(new Error("test deadline")),
            { once: true },
          ),
        ),
    );
    const { client } = setup(fetcher);
    const result = client.listHolds().catch((reason: unknown) => reason);
    await vi.advanceTimersByTimeAsync(15_000);
    expect(await result).toMatchObject({ code: "network.timeout" });
    expect(fetcher).toHaveBeenCalledTimes(1);
  });
});
