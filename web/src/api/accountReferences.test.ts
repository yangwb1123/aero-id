import { afterEach, describe, expect, it, vi } from "vitest";
import { AeroIdClient } from "./aeroId";
const clients: AeroIdClient[] = [];
afterEach(() => {
  clients.splice(0).forEach((client) => client.cancelPendingReads());
  vi.restoreAllMocks();
});
const families = [
  { field: "sources", method: "listSources" },
  { field: "memberships", method: "listMemberships" },
] as const;
for (const { field, method } of families)
  describe(field, () => {
    it("reads only the original self endpoint with original cursor, cancellation and page budget", async () => {
      const fetcher = vi.fn(
        async () =>
          new Response(
            JSON.stringify({
              data: {
                [field]: [{ id: "native-row", data: { ignored: true } }],
                next_cursor: "original/opaque+cursor",
              },
            }),
          ),
      );
      const client = new AeroIdClient(
        "https://id.example/v1",
        () => "fixture-marker",
        fetcher,
      );
      clients.push(client);
      const controller = new AbortController();
      const page = await client[method](
        "original/opaque+cursor",
        controller.signal,
      );
      expect(page.nextCursor).toBe("original/opaque+cursor");
      expect(page.items).toHaveLength(1);
      const [input, init] = fetcher.mock.calls[0] as unknown as [
        RequestInfo | URL,
        RequestInit,
      ];
      const url = new URL(String(input));
      expect(url.pathname).toBe(`/v1/me/${field}`);
      expect([...url.searchParams]).toEqual([
        ["cursor", "original/opaque+cursor"],
        ["limit", "50"],
      ]);
      expect(init.method).toBe("GET");
      expect(init.body).toBeUndefined();
      expect(new Headers(init.headers).has("Idempotency-Key")).toBe(false);
      expect(controller.signal.aborted).toBe(false);
    });
    it.each([
      { bad: undefined },
      { bad: null },
      { bad: {} },
      { bad: [null] },
      { bad: [{}, []] },
      { bad: Array.from({ length: 51 }, () => ({})) },
    ])(
      "rejects the whole bad list rather than claiming empty: $bad",
      async ({ bad }) => {
        const client = new AeroIdClient(
          "https://id.example/v1",
          () => "fixture-marker",
          async () => new Response(JSON.stringify({ data: { [field]: bad } })),
        );
        clients.push(client);
        await expect(client[method]()).rejects.toMatchObject({
          code: "response.invalid",
        });
      },
    );
    it("accepts an explicit empty page but never silently consumes a malformed continuation", async () => {
      let cursor: unknown = "";
      const client = new AeroIdClient(
        "https://id.example/v1",
        () => "fixture-marker",
        async () =>
          new Response(
            JSON.stringify({ data: { [field]: [], next_cursor: cursor } }),
          ),
      );
      clients.push(client);
      expect(await client[method]()).toEqual({ items: [], nextCursor: "" });
      cursor = 7;
      await expect(client[method]()).rejects.toMatchObject({
        code: "response.invalid",
      });
    });
    it("propagates scoped abort while fetch is pending, without a second request", async () => {
      const fetcher = vi.fn(
        (_input: RequestInfo | URL, init?: RequestInit) =>
          new Promise<Response>((_resolve, reject) => {
            init?.signal?.addEventListener(
              "abort",
              () => reject(init.signal?.reason),
              { once: true },
            );
          }),
      );
      const client = new AeroIdClient(
        "https://id.example/v1",
        () => "fixture-marker",
        fetcher,
      );
      clients.push(client);
      const controller = new AbortController();
      const pending = client[method](undefined, controller.signal);
      const rejected = expect(pending).rejects.toBeDefined();
      controller.abort();
      await rejected;
      expect(fetcher).toHaveBeenCalledTimes(1);
      expect(fetcher.mock.calls[0]?.[1]?.signal?.aborted).toBe(true);
    });
  });
