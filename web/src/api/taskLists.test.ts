import { describe, expect, it, vi } from "vitest";
import type { JsonRecord } from "../types";
import { AeroIdClient, AeroIdError } from "./aeroId";

const families = [
  {
    method: "listSyncJobs",
    path: "/sync/jobs",
    field: "jobs",
    other: "operations",
  },
  {
    method: "listOperations",
    path: "/operations",
    field: "operations",
    other: "jobs",
  },
] as const;
type Family = (typeof families)[number];
const originalCursor = "opaque:original/1";
const rejectedMarker = "rejected-list-content-marker";
const row = {
  job_id: "original-job",
  operation_id: "original-operation",
  status: "unknown",
};

function clientWith(fetcher: typeof fetch): AeroIdClient {
  return new AeroIdClient(
    "https://accounts.example.test/v1",
    () => "test-token",
    fetcher,
  );
}
function json(data: unknown, init: ResponseInit = {}): Response {
  return new Response(JSON.stringify({ data }), init);
}
function invalidPages(
  family: Family,
): Array<{ name: string; data: JsonRecord }> {
  return [
    { name: "missing list", data: {} },
    { name: "generic items alias", data: { items: [row] } },
    { name: "other family list", data: { [family.other]: [row] } },
    { name: "nested list", data: { result: { [family.field]: [row] } } },
    ...[null, false, 0, "rows", {}].map((value) => ({
      name: `non-array list ${JSON.stringify(value)}`,
      data: { [family.field]: value },
    })),
    ...[null, false, 0, "row", []].map((entry) => ({
      name: `invalid member ${JSON.stringify(entry)} among valid rows`,
      data: { [family.field]: [row, entry, { marker: rejectedMarker }] },
    })),
    ...[null, false, 0, [], {}].map((next_cursor) => ({
      name: `non-string cursor ${JSON.stringify(next_cursor)}`,
      data: { [family.field]: [row], next_cursor },
    })),
  ];
}
function expectOriginalRead(
  fetcher: ReturnType<typeof vi.fn>,
  family: Family,
  cursor?: string,
): void {
  expect(fetcher).toHaveBeenCalledTimes(1);
  const [input, init] = fetcher.mock.calls[0] as [
    RequestInfo | URL,
    RequestInit,
  ];
  const url = new URL(String(input));
  expect(url.pathname).toBe(`/v1${family.path}`);
  expect([...url.searchParams.entries()].sort()).toEqual(
    cursor
      ? [
          ["cursor", cursor],
          ["limit", "50"],
        ]
      : [["limit", "50"]],
  );
  expect(init.method).toBe("GET");
  expect(init.body).toBeUndefined();
  expect(new Headers(init.headers).get("Idempotency-Key")).toBeNull();
}

describe.each(families)("$method page-shape contract", (family) => {
  it.each(invalidPages(family))(
    "rejects $name as a whole page",
    async ({ data }) => {
      const fetcher = vi.fn(async () =>
        json({ ...data, diagnostic: rejectedMarker }),
      );
      const client = clientWith(fetcher as typeof fetch);
      const reason: unknown = await client[family.method](originalCursor).catch(
        (error: unknown) => error,
      );
      expect(reason).toBeInstanceOf(AeroIdError);
      expect(reason).toMatchObject({ status: 0, code: "response.invalid" });
      expect(String(reason)).not.toContain(rejectedMarker);
      expectOriginalRead(fetcher, family, originalCursor);
    },
  );

  it.each([null, [], "page", 3])(
    "rejects a non-object data envelope %j",
    async (data) => {
      const fetcher = vi.fn(async () => json(data));
      const client = clientWith(fetcher as typeof fetch);
      await expect(client[family.method]()).rejects.toMatchObject({
        status: 0,
        code: "response.invalid",
      });
      expectOriginalRead(fetcher, family);
    },
  );

  it.each([undefined, ""])(
    "accepts a genuine empty page with cursor %j",
    async (next_cursor) => {
      const fetcher = vi.fn(async () =>
        json({ [family.field]: [], next_cursor }),
      );
      const client = clientWith(fetcher as typeof fetch);
      await expect(client[family.method]()).resolves.toEqual({
        items: [],
        nextCursor: next_cursor,
      });
      expectOriginalRead(fetcher, family);
    },
  );

  it("retains rows, order, original fields and opaque cursor without adding detail-field validation", async () => {
    const rows = [
      { ...row, result: { original: true } },
      { marker: "older-row" },
    ];
    const fetcher = vi.fn(async () =>
      json({ [family.field]: rows, next_cursor: "opaque:next/2" }),
    );
    const client = clientWith(fetcher as typeof fetch);
    await expect(client[family.method](originalCursor)).resolves.toEqual({
      items: rows,
      nextCursor: "opaque:next/2",
    });
    expectOriginalRead(fetcher, family, originalCursor);
  });

  it.each(["signal", "client"])(
    "preserves %s cancellation without a replacement request",
    async (kind) => {
      const controller = new AbortController();
      let signal: AbortSignal | null | undefined;
      const fetcher = vi.fn(
        async (_input: RequestInfo | URL, init?: RequestInit) => {
          signal = init?.signal;
          return new Promise<Response>((_resolve, reject) => {
            signal?.addEventListener(
              "abort",
              () => reject(new DOMException("Aborted", "AbortError")),
              { once: true },
            );
          });
        },
      );
      const client = clientWith(fetcher as typeof fetch);
      const pending = client[family.method](originalCursor, controller.signal);
      if (kind === "signal") controller.abort();
      else client.cancelPendingReads();
      await expect(pending).rejects.toMatchObject({ code: "request.canceled" });
      expect(signal?.aborted).toBe(true);
      expectOriginalRead(fetcher, family, originalCursor);
    },
  );

  it.each([403, 503])(
    "preserves HTTP %s before inspecting page shape",
    async (status) => {
      const fetcher = vi.fn(
        async () =>
          new Response(
            JSON.stringify({
              error: { code: "list.unavailable", message: "读取不可用" },
            }),
            { status },
          ),
      );
      const client = clientWith(fetcher as typeof fetch);
      await expect(client[family.method]()).rejects.toMatchObject({
        status,
        code: "list.unavailable",
      });
      expectOriginalRead(fetcher, family);
    },
  );

  it("preserves marked stale-read rejection ahead of page adoption", async () => {
    const fetcher = vi.fn(async () =>
      json(
        { [family.field]: [row], next_cursor: false },
        { headers: { "X-Aero-Stale-Read": "true" } },
      ),
    );
    const client = clientWith(fetcher as typeof fetch);
    await expect(client[family.method]()).rejects.toMatchObject({
      code: "region.stale_read",
    });
    expectOriginalRead(fetcher, family);
  });
});
