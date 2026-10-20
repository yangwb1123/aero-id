import { describe, expect, it, vi } from "vitest";
import type { JsonRecord } from "../types";
import { AeroIdClient, AeroIdError } from "./aeroId";

const originalID = "01800000-0000-7000-8000-000000000003";
const otherID = "01800000-0000-7000-8000-000000000099";
const rejectedMarker = "rejected-audit-content-marker";
const fact = { id: "fact-original", action: "operation.requested" };
function json(data: unknown, init: ResponseInit = {}): Response {
  return new Response(JSON.stringify({ data }), init);
}
function clientWith(fetcher: typeof fetch): AeroIdClient {
  return new AeroIdClient(
    "https://accounts.example.test/v1",
    () => "test-token",
    fetcher,
  );
}
const invalidIdentity = [
  { name: "another Operation", identity: { operation_id: otherID } },
  { name: "missing root ID", identity: {} },
  { name: "null ID", identity: { operation_id: null } },
  { name: "number ID", identity: { operation_id: 3 } },
  { name: "boolean ID", identity: { operation_id: true } },
  { name: "array ID", identity: { operation_id: [originalID] } },
  { name: "object ID", identity: { operation_id: { id: originalID } } },
  { name: "empty ID", identity: { operation_id: "" } },
  { name: "padded ID", identity: { operation_id: ` ${originalID} ` } },
  { name: "generic alias", identity: { id: originalID } },
  { name: "job ID", identity: { job_id: originalID } },
  { name: "nested ID", identity: { result: { operation_id: originalID } } },
  {
    name: "correct alias but wrong root",
    identity: { id: originalID, operation_id: otherID },
  },
];
const invalidPages = [
  { name: "missing events", fields: {} },
  ...[null, false, 0, "events", {}].map((events) => ({
    name: `non-array events ${JSON.stringify(events)}`,
    fields: { events },
  })),
  ...[null, false, 3, "fact", []].map((entry) => ({
    name: `invalid event ${JSON.stringify(entry)} among valid facts`,
    fields: { events: [fact, entry] },
  })),
  ...[null, false, 3, [], {}].map((next_cursor) => ({
    name: `non-string cursor ${JSON.stringify(next_cursor)}`,
    fields: { events: [fact], next_cursor },
  })),
];
async function expectRejected(data: JsonRecord): Promise<void> {
  const fetcher = vi.fn(async (_input: RequestInfo | URL) => json(data));
  const reason: unknown = await clientWith(fetcher as typeof fetch)
    .listOperationAudit(originalID, "original-cursor")
    .catch((error: unknown) => error);
  expect(reason).toBeInstanceOf(AeroIdError);
  expect(reason).toMatchObject({ status: 0, code: "response.invalid" });
  expect(String(reason)).not.toContain(otherID);
  expect(String(reason)).not.toContain(rejectedMarker);
  expect(fetcher).toHaveBeenCalledTimes(1);
  const url = new URL(String(fetcher.mock.calls[0][0]));
  expect(url.pathname).toBe(`/v1/audit/operations/${originalID}`);
  expect(url.searchParams.get("cursor")).toBe("original-cursor");
  expect(url.searchParams.get("limit")).toBe("50");
}

describe("Operation audit original-target and page-shape binding", () => {
  it.each(invalidIdentity)("rejects $name", async ({ identity }) => {
    await expectRejected({
      ...identity,
      events: [{ ...fact, action: rejectedMarker }],
      next_cursor: "must-not-adopt",
    });
  });

  it.each(invalidPages)(
    "rejects $name without silently filtering",
    async ({ fields }) => {
      await expectRejected({
        operation_id: originalID,
        diagnostic: rejectedMarker,
        ...fields,
      });
    },
  );

  it.each([null, [], "page", 3])(
    "rejects invalid data envelope %j",
    async (data) => {
      const fetcher = vi.fn(async () => json(data));
      await expect(
        clientWith(fetcher as typeof fetch).listOperationAudit(originalID),
      ).rejects.toMatchObject({ status: 0, code: "response.invalid" });
      expect(fetcher).toHaveBeenCalledTimes(1);
    },
  );

  it("retains matching facts, order, opaque cursors, encoded path and read signal", async () => {
    const id = "opaque/operation:1";
    const events = [
      { ...fact, operation_id: id, payload: { redacted: true } },
      { id: "fact-older", action: "operation.created" },
    ];
    const fetcher = vi.fn(
      async (_input: RequestInfo | URL, _init?: RequestInit) =>
        json({ operation_id: id, events, next_cursor: "opaque:next/1" }),
    );
    const controller = new AbortController();
    await expect(
      clientWith(fetcher as typeof fetch).listOperationAudit(
        id,
        "opaque:original/1",
        controller.signal,
      ),
    ).resolves.toEqual({ items: events, nextCursor: "opaque:next/1" });
    const [input, init] = fetcher.mock.calls[0];
    const url = new URL(String(input));
    expect(url.pathname).toBe("/v1/audit/operations/opaque%2Foperation%3A1");
    expect(url.searchParams.get("cursor")).toBe("opaque:original/1");
    expect(url.searchParams.get("limit")).toBe("50");
    expect(init?.method).toBe("GET");
    expect(init?.body).toBeUndefined();
    expect(new Headers(init?.headers).get("Idempotency-Key")).toBeNull();
    expect(init?.signal).toBeInstanceOf(AbortSignal);
    expect(init?.signal?.aborted).toBe(false);
    expect(fetcher).toHaveBeenCalledTimes(1);
  });

  it.each([undefined, ""])(
    "accepts a genuine empty page with cursor %j",
    async (next_cursor) => {
      const fetcher = vi.fn(async () =>
        json({ operation_id: originalID, events: [], next_cursor }),
      );
      await expect(
        clientWith(fetcher as typeof fetch).listOperationAudit(originalID),
      ).resolves.toEqual({ items: [], nextCursor: next_cursor });
      expect(fetcher).toHaveBeenCalledTimes(1);
    },
  );

  it("does not adopt swapped concurrent original-ID audit responses", async () => {
    const pending: Array<(response: Response) => void> = [];
    const fetcher = vi.fn(
      async () => new Promise<Response>((resolve) => pending.push(resolve)),
    );
    const client = clientWith(fetcher as typeof fetch);
    const first = client
      .listOperationAudit(originalID)
      .catch((reason: unknown) => reason);
    const second = client
      .listOperationAudit(otherID)
      .catch((reason: unknown) => reason);
    pending[1](json({ operation_id: originalID, events: [fact] }));
    pending[0](json({ operation_id: otherID, events: [fact] }));
    for (const reason of await Promise.all([first, second]))
      expect(reason).toMatchObject({ status: 0, code: "response.invalid" });
    expect(fetcher).toHaveBeenCalledTimes(2);
  });

  it.each(["signal", "client"])(
    "preserves %s cancellation without replacement GET",
    async (kind) => {
      const controller = new AbortController();
      let readSignal: AbortSignal | null | undefined;
      const fetcher = vi.fn(
        async (_input: RequestInfo | URL, init?: RequestInit) => {
          readSignal = init?.signal;
          return new Promise<Response>((_resolve, reject) => {
            readSignal?.addEventListener(
              "abort",
              () => reject(new DOMException("Aborted", "AbortError")),
              { once: true },
            );
          });
        },
      );
      const client = clientWith(fetcher as typeof fetch);
      const result = client.listOperationAudit(
        originalID,
        undefined,
        controller.signal,
      );
      if (kind === "signal") controller.abort();
      else client.cancelPendingReads();
      await expect(result).rejects.toMatchObject({ code: "request.canceled" });
      expect(readSignal?.aborted).toBe(true);
      expect(fetcher).toHaveBeenCalledTimes(1);
    },
  );

  it("preserves server denial without falling back to another audit endpoint", async () => {
    const fetcher = vi.fn(
      async () =>
        new Response(
          JSON.stringify({
            error: { code: "permission.denied", message: "not permitted" },
          }),
          { status: 403 },
        ),
    );
    await expect(
      clientWith(fetcher as typeof fetch).listOperationAudit(originalID),
    ).rejects.toMatchObject({ status: 403, code: "permission.denied" });
    expect(fetcher).toHaveBeenCalledTimes(1);
  });

  it("still rejects a marked stale page with matching identity and valid events", async () => {
    const fetcher = vi.fn(async () =>
      json(
        { operation_id: originalID, events: [fact] },
        { headers: { "X-Aero-Stale-Read": "true" } },
      ),
    );
    await expect(
      clientWith(fetcher as typeof fetch).listOperationAudit(originalID),
    ).rejects.toMatchObject({ code: "region.stale_read" });
    expect(fetcher).toHaveBeenCalledTimes(1);
  });
});
