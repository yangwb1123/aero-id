import { describe, expect, it, vi } from "vitest";
import type { JsonRecord } from "../types";
import { AeroIdClient, AeroIdError } from "./aeroId";

const originalID = "01800000-0000-7000-8000-000000000003";
const otherID = "01800000-0000-7000-8000-000000000099";
const targets = [
  {
    method: "getSyncJob",
    field: "job_id",
    path: "/sync/jobs",
    statuses: [
      "pending",
      "running",
      "completed",
      "partially_completed",
      "failed",
      "unknown",
    ],
    foreignStatuses: ["requested", "waiting", "compensating"],
  },
  {
    method: "getOperation",
    field: "operation_id",
    path: "/operations",
    statuses: [
      "requested",
      "running",
      "waiting",
      "unknown",
      "partially_completed",
      "completed",
      "failed",
      "compensating",
    ],
    foreignStatuses: ["pending"],
  },
] as const;

function json(body: unknown, init: ResponseInit = {}): Response {
  return new Response(JSON.stringify(body), {
    ...init,
    headers: { "Content-Type": "application/json", ...init.headers },
  });
}
function clientWith(fetcher: typeof fetch): AeroIdClient {
  return new AeroIdClient(
    "https://accounts.example.test/v1",
    () => "test-token",
    fetcher,
  );
}

describe.each(targets)("$method original GET identity", (target) => {
  const invalid = [
    { name: "another task", fields: { [target.field]: otherID } },
    { name: "missing ID", fields: {} },
    { name: "null ID", fields: { [target.field]: null } },
    { name: "numeric ID", fields: { [target.field]: 3 } },
    { name: "boolean ID", fields: { [target.field]: true } },
    { name: "array ID", fields: { [target.field]: [originalID] } },
    { name: "object ID", fields: { [target.field]: { id: originalID } } },
    { name: "empty ID", fields: { [target.field]: "" } },
    { name: "padded ID", fields: { [target.field]: ` ${originalID} ` } },
    { name: "generic alias only", fields: { id: originalID } },
    {
      name: "other task family only",
      fields: {
        [target.field === "job_id" ? "operation_id" : "job_id"]: originalID,
      },
    },
    {
      name: "correct alias with wrong root ID",
      fields: { id: originalID, [target.field]: otherID },
    },
    {
      name: "nested identity only",
      fields: { result: { [target.field]: originalID } },
    },
  ];

  it.each(invalid)(
    "rejects $name without retrying or exposing data",
    async ({ fields }) => {
      const fetcher = vi.fn(async (_input: RequestInfo | URL) =>
        json({
          data: {
            ...fields,
            status: "unknown",
            error_message: "untrusted-detail-marker",
            idempotency_key: "original-command-key-marker",
          },
        }),
      );
      const client = clientWith(fetcher as typeof fetch);
      const error: unknown = await client[target.method](originalID).catch(
        (reason: unknown) => reason,
      );

      expect(error).toBeInstanceOf(AeroIdError);
      expect(error).toMatchObject({ status: 0, code: "response.invalid" });
      expect(String(error)).not.toContain(otherID);
      expect(String(error)).not.toContain("untrusted-detail-marker");
      expect(String(error)).not.toContain("original-command-key-marker");
      expect(fetcher).toHaveBeenCalledTimes(1);
      expect(new URL(String(fetcher.mock.calls[0]?.[0])).pathname).toBe(
        `/v1${target.path}/${originalID}`,
      );
    },
  );

  it.each(target.statuses)(
    "keeps the exact family status %s and original metadata",
    async (status) => {
      const data = {
        [target.field]: originalID,
        status,
        result: {
          source_operation_id: "original-source-operation",
          status: "not-a-root-status",
        },
        steps: [{ status: "pending", compensation_status: "unknown" }],
        completed_at: null,
      };
      const fetcher = vi.fn(async () => json({ data }));
      await expect(
        clientWith(fetcher as typeof fetch)[target.method](originalID),
      ).resolves.toEqual(data);
      expect(fetcher).toHaveBeenCalledTimes(1);
    },
  );

  it.each([
    { name: "missing", status: undefined },
    { name: "null", status: null },
    { name: "boolean", status: true },
    { name: "number", status: 1 },
    { name: "object", status: { status: "unknown" } },
    { name: "empty array", status: [] },
    { name: "nested array", status: [["unknown"]] },
    { name: "empty string", status: "" },
    { name: "canceled", status: "canceled" },
    { name: "unrecognized string", status: "untrusted-status-marker" },
    { name: "padded string", status: " unknown " },
    { name: "uppercase", status: "UNKNOWN" },
    ...target.statuses.map((status) => ({
      name: `array ${status}`,
      status: [status],
    })),
    ...target.foreignStatuses.map((status) => ({
      name: `other family ${status}`,
      status,
    })),
  ])(
    "refuses $name root status without coercion, nested fallback or retry",
    async ({ status }) => {
      const fetcher = vi.fn(async (_input: RequestInfo | URL) =>
        json({
          data: {
            [target.field]: originalID,
            status,
            result: {
              status: "unknown",
              source_operation_id: "untrusted-result-marker",
            },
            steps: [{ status: "running" }],
            idempotency_key: "original-command-key-marker",
          },
        }),
      );
      const client = clientWith(fetcher as typeof fetch);
      const error: unknown = await client[target.method](originalID).catch(
        (reason: unknown) => reason,
      );
      expect(error).toBeInstanceOf(AeroIdError);
      expect(error).toMatchObject({ code: "response.invalid", status: 0 });
      for (const marker of [
        "untrusted-status-marker",
        "untrusted-result-marker",
        "original-command-key-marker",
      ])
        expect(String(error)).not.toContain(marker);
      expect(fetcher).toHaveBeenCalledTimes(1);
      expect(new URL(String(fetcher.mock.calls[0]?.[0])).pathname).toBe(
        `/v1${target.path}/${originalID}`,
      );
    },
  );

  it("keeps matching metadata, original encoded path and read cancellation signal", async () => {
    const id = "opaque/task:1";
    const data = {
      [target.field]: id,
      account_id: originalID,
      tenant_id: "tenant-a",
      actor_uid: "original-initiator",
      status: "unknown",
      result: { source_operation_id: "original-source-operation" },
      request: { datasets: ["aero-vault.usage"] },
    };
    const fetcher = vi.fn(
      async (_input: RequestInfo | URL, _init?: RequestInit) => json({ data }),
    );
    const signal = new AbortController().signal;
    await expect(
      clientWith(fetcher as typeof fetch)[target.method](id, signal),
    ).resolves.toEqual(data);

    const [url, init] = fetcher.mock.calls[0];
    expect(new URL(String(url)).pathname).toBe(
      `/v1${target.path}/opaque%2Ftask%3A1`,
    );
    expect(init?.method).toBe("GET");
    expect(init?.body).toBeUndefined();
    expect(init?.signal).toBeInstanceOf(AbortSignal);
    expect(init?.signal?.aborted).toBe(false);
    expect(new Headers(init?.headers).get("Idempotency-Key")).toBeNull();
    expect(fetcher).toHaveBeenCalledTimes(1);
  });

  it.each([null, [], "detail", 7])(
    "still rejects a malformed data envelope: %j",
    async (data) => {
      const fetcher = vi.fn(async () => json({ data }));
      await expect(
        clientWith(fetcher as typeof fetch)[target.method](originalID),
      ).rejects.toMatchObject({ status: 0, code: "response.invalid" });
      expect(fetcher).toHaveBeenCalledTimes(1);
    },
  );

  it("does not adopt swapped responses from concurrent original-ID reads", async () => {
    const pending: Array<(response: Response) => void> = [];
    const fetcher = vi.fn(
      async () => new Promise<Response>((resolve) => pending.push(resolve)),
    );
    const client = clientWith(fetcher as typeof fetch);
    const first = client[target.method](originalID).catch(
      (reason: unknown) => reason,
    );
    const second = client[target.method](otherID).catch(
      (reason: unknown) => reason,
    );
    pending[1](
      json({ data: { [target.field]: originalID, status: "unknown" } }),
    );
    pending[0](
      json({ data: { [target.field]: otherID, status: "completed" } }),
    );

    for (const result of await Promise.all([first, second])) {
      expect(result).toBeInstanceOf(AeroIdError);
      expect(result).toMatchObject({ code: "response.invalid", status: 0 });
    }
    expect(fetcher).toHaveBeenCalledTimes(2);
  });

  it.each(["signal", "client"])(
    "preserves %s cancellation rather than making a replacement request",
    async (cancelKind) => {
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
      const result = client[target.method](originalID, controller.signal);
      if (cancelKind === "signal") controller.abort();
      else client.cancelPendingReads();

      await expect(result).rejects.toMatchObject({ code: "request.canceled" });
      expect(readSignal?.aborted).toBe(true);
      expect(fetcher).toHaveBeenCalledTimes(1);
    },
  );

  it("does not reinterpret a server denial or retry it", async () => {
    const fetcher = vi.fn(async () =>
      json(
        { error: { code: "task.forbidden", message: "not permitted" } },
        { status: 403 },
      ),
    );
    await expect(
      clientWith(fetcher as typeof fetch)[target.method](originalID),
    ).rejects.toMatchObject({ status: 403, code: "task.forbidden" });
    expect(fetcher).toHaveBeenCalledTimes(1);
  });

  it("still refuses marked stale reads even with the matching original ID", async () => {
    const data: JsonRecord = { [target.field]: originalID, status: "unknown" };
    const fetcher = vi.fn(async () =>
      json({ data }, { headers: { "X-Aero-Stale-Read": "true" } }),
    );
    await expect(
      clientWith(fetcher as typeof fetch)[target.method](originalID),
    ).rejects.toMatchObject({ code: "region.stale_read" });
    expect(fetcher).toHaveBeenCalledTimes(1);
  });
});
