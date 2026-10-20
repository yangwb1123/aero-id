import { afterEach, describe, expect, it, vi } from "vitest";
import type { JsonRecord } from "../types";
import { ComplianceClient } from "./compliance";
import { AeroIdError, AeroIdTransport } from "./transport";

const source = "aero/id";
const partition = "tenant:a/system";
const marker = "untrusted-manifest-marker";
function manifest(): JsonRecord {
  return {
    schema_version: 1,
    kind: "audit_anchor",
    source,
    partition,
    source_region: "local",
    chain_sequence: 7,
    record_hash: "original-record-hash",
    signature_version: 1,
    signature: "original-signature",
    previous_anchor_hash: "original-previous-hash",
    anchor_hash: marker,
    anchored_at: "2026-10-16T00:00:00Z",
  };
}
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
  for (const text of [marker, "different-source", "different-partition"])
    expect(String(error)).not.toContain(text);
}
const badFields = ["source", "partition"].flatMap((field) => {
  const original = field === "source" ? source : partition;
  return [
    { label: "missing", value: undefined },
    { label: "null", value: null },
    { label: "number", value: 7 },
    { label: "boolean", value: true },
    { label: "array", value: [original] },
    { label: "object", value: { value: original } },
    { label: "empty", value: "" },
    { label: "padded", value: ` ${original} ` },
    { label: "case variant", value: original.toUpperCase() },
    { label: "different target", value: `different-${field}` },
  ].map(({ label, value }) => ({
    field,
    label,
    data: { ...manifest(), [field]: value },
  }));
});

afterEach(() => {
  vi.restoreAllMocks();
  vi.useRealTimers();
});

describe("Manifest GET response belongs to its original source and partition", () => {
  it.each(badFields)(
    "rejects $label $field without coercion, fallback or retry",
    async ({ data }) => {
      const fetcher = vi.fn(async () => json({ data }));
      const { client } = setup(fetcher);
      const error: unknown = await client
        .getAuditAnchorManifest(source, partition)
        .catch((reason: unknown) => reason);
      expectInvalid(error);
      expect(fetcher).toHaveBeenCalledTimes(1);
    },
  );

  it.each([
    {
      label: "aliases only",
      data: { source_name: source, partition_key: partition },
    },
    { label: "nested targets only", data: { target: { source, partition } } },
    {
      label: "matching alias with wrong root source",
      data: { source: "different-source", partition, source_name: source },
    },
    {
      label: "matching nested target with wrong root partition",
      data: {
        source,
        partition: "different-partition",
        target: { source, partition },
      },
    },
  ])("does not borrow $label", async ({ data }) => {
    const fetcher = vi.fn(async () =>
      json({ data: { ...data, anchor_hash: marker } }),
    );
    const { client } = setup(fetcher);
    expectInvalid(
      await client
        .getAuditAnchorManifest(source, partition)
        .catch((reason: unknown) => reason),
    );
    expect(fetcher).toHaveBeenCalledTimes(1);
  });

  it.each([null, [], "manifest", 7, undefined])(
    "retains data-envelope rejection for %j",
    async (data) => {
      const fetcher = vi.fn(async () => json({ data }));
      const { client } = setup(fetcher);
      expectInvalid(
        await client
          .getAuditAnchorManifest(source, partition)
          .catch((reason: unknown) => reason),
      );
      expect(fetcher).toHaveBeenCalledTimes(1);
    },
  );

  it("preserves exact target text, encoded paths, all metadata and GET-only transport", async () => {
    const data = manifest();
    const fetcher = vi.fn(
      async (_input: RequestInfo | URL, _init?: RequestInit) => json({ data }),
    );
    const { client } = setup(fetcher);
    await expect(
      client.getAuditAnchorManifest(source, partition),
    ).resolves.toEqual(data);
    expect(fetcher).toHaveBeenCalledTimes(1);
    const [input, init] = fetcher.mock.calls[0];
    const url = new URL(String(input));
    expect(url.pathname).toBe(
      "/v1/admin/audit/anchor/manifest/aero%2Fid/tenant%3Aa%2Fsystem",
    );
    expect(url.search).toBe("");
    expect(init?.method).toBe("GET");
    expect(init?.body).toBeUndefined();
    expect(init?.credentials).toBe("omit");
    expect(init?.signal?.aborted).toBe(false);
    expect(new Headers(init?.headers).get("Idempotency-Key")).toBeNull();
    expect(new Headers(init?.headers).get("X-Request-ID")).toBeTruthy();
  });

  it.each(["source", "partition"])(
    "rejects concurrent responses swapped by %s",
    async (field) => {
      const pending: Array<(response: Response) => void> = [];
      const fetcher = vi.fn(
        async () => new Promise<Response>((resolve) => pending.push(resolve)),
      );
      const { client } = setup(fetcher);
      const otherSource = field === "source" ? "different-source" : source;
      const otherPartition =
        field === "partition" ? "different-partition" : partition;
      const first = client
        .getAuditAnchorManifest(source, partition)
        .catch((reason: unknown) => reason);
      const second = client
        .getAuditAnchorManifest(otherSource, otherPartition)
        .catch((reason: unknown) => reason);
      pending[1](json({ data: manifest() }));
      pending[0](
        json({
          data: {
            ...manifest(),
            source: otherSource,
            partition: otherPartition,
          },
        }),
      );
      for (const error of await Promise.all([first, second]))
        expectInvalid(error);
      expect(fetcher).toHaveBeenCalledTimes(2);
    },
  );

  it.each([403, 404, 503])(
    "preserves HTTP %i instead of relabeling it a target mismatch",
    async (status) => {
      const fetcher = vi.fn(async () =>
        json(
          {
            error: {
              code: "audit_anchor.read_failed",
              message: "Manifest unavailable",
            },
          },
          { status },
        ),
      );
      const { client } = setup(fetcher);
      await expect(
        client.getAuditAnchorManifest(source, partition),
      ).rejects.toMatchObject({ status, code: "audit_anchor.read_failed" });
      expect(fetcher).toHaveBeenCalledTimes(1);
    },
  );

  it("keeps stale-read refusal ahead of local target adoption", async () => {
    const fetcher = vi.fn(async () =>
      json(
        { data: { ...manifest(), source: "different-source" } },
        { headers: { "X-Aero-Stale-Read": "true" } },
      ),
    );
    const { client } = setup(fetcher);
    await expect(
      client.getAuditAnchorManifest(source, partition),
    ).rejects.toMatchObject({ code: "region.stale_read" });
    expect(fetcher).toHaveBeenCalledTimes(1);
  });

  it.each(["fetch", "body"])(
    "keeps client read cancellation during %s without replacement GET",
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
      const result = client
        .getAuditAnchorManifest(source, partition)
        .catch((reason: unknown) => reason);
      await vi.waitFor(() => expect(fetcher).toHaveBeenCalledTimes(1));
      // Allow the body reader to start before cancellation in the body case.
      await Promise.resolve();
      transport.cancelPendingReads();
      expect(signal?.aborted).toBe(true);
      expect(await result).toMatchObject({ code: "request.canceled" });
      expect(fetcher).toHaveBeenCalledTimes(1);
    },
  );

  it("keeps the original 30-second deadline and never retries after timeout", async () => {
    vi.useFakeTimers();
    let signal: AbortSignal | null | undefined;
    const fetcher = vi.fn(
      async (_input: RequestInfo | URL, init?: RequestInit) => {
        signal = init?.signal;
        return new Promise<Response>((_resolve, reject) =>
          signal?.addEventListener(
            "abort",
            () => reject(new Error("test timeout")),
            { once: true },
          ),
        );
      },
    );
    const { client } = setup(fetcher);
    const result = client
      .getAuditAnchorManifest(source, partition)
      .catch((reason: unknown) => reason);
    await vi.advanceTimersByTimeAsync(29_999);
    expect(signal?.aborted).toBe(false);
    await vi.advanceTimersByTimeAsync(1);
    expect(signal?.aborted).toBe(true);
    expect(await result).toMatchObject({ code: "network.timeout" });
    await vi.advanceTimersByTimeAsync(60_000);
    expect(fetcher).toHaveBeenCalledTimes(1);
  });
});
