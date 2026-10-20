import { afterEach, describe, expect, it, vi } from "vitest";
import { AeroIdClient } from "./aeroId";
const calls = [
  {
    name: "overview",
    path: "/v1/me/overview",
    read: (client: AeroIdClient, signal: AbortSignal) =>
      client.getOverview(["aero-vault.usage"], signal),
  },
  {
    name: "self data",
    path: "/v1/me/data",
    read: (client: AeroIdClient, signal: AbortSignal) =>
      client.getMyData(
        {
          datasets: ["aero-vault.usage"],
          consistency: "bounded",
          maxAge: "5m",
        },
        signal,
      ),
  },
  {
    name: "admin data",
    path: "/v1/admin/accounts/account%2Fopaque/data",
    read: (client: AeroIdClient, signal: AbortSignal) =>
      client.getAdminAccountData(
        "account/opaque",
        { datasets: ["aero-vault.usage"], consistency: "strong" },
        signal,
      ),
  },
];
afterEach(() => vi.restoreAllMocks());
describe.each(calls)("$name original read cancellation", ({ read, path }) => {
  it("keeps the original path, explicit scope, mode and GET-only semantics", async () => {
    const fetcher = vi.fn<typeof fetch>(
      async () =>
        new Response(
          JSON.stringify({ data: { snapshots: [], partial: false } }),
        ),
    );
    const client = new AeroIdClient(
      "https://id.example/v1",
      () => "fixture-only",
      fetcher,
    );
    await read(client, new AbortController().signal);
    expect(fetcher).toHaveBeenCalledTimes(1);
    const [input, init] = fetcher.mock.calls[0]!;
    const url = new URL(String(input));
    expect(url.pathname).toBe(path);
    expect(url.searchParams.getAll("dataset")).toEqual(["aero-vault.usage"]);
    expect(url.searchParams.get("consistency")).toBe(
      path.endsWith("overview")
        ? "eventual"
        : path.includes("admin")
          ? "strong"
          : "bounded",
    );
    expect(url.searchParams.get("max_age")).toBe(
      path === "/v1/me/data" ? "5m" : null,
    );
    expect(init?.method).toBe("GET");
    expect(init?.body).toBeUndefined();
    expect(new Headers(init?.headers).has("Idempotency-Key")).toBe(false);
  });
  it("does not start an already-canceled read", async () => {
    const fetcher = vi.fn<typeof fetch>();
    const client = new AeroIdClient(
      "https://id.example/v1",
      () => "fixture-only",
      fetcher,
    );
    const controller = new AbortController();
    controller.abort();
    await expect(read(client, controller.signal)).rejects.toMatchObject({
      code: "request.canceled",
    });
    expect(fetcher).not.toHaveBeenCalled();
  });
  it("cancels its in-flight response body without automatically retrying", async () => {
    let signal: AbortSignal | null | undefined;
    let body: ReadableStreamDefaultController<Uint8Array> | undefined;
    const fetcher = vi.fn<typeof fetch>(async (_url, init) => {
      signal = init?.signal;
      return new Response(
        new ReadableStream<Uint8Array>({
          start(controller) {
            body = controller;
            signal?.addEventListener(
              "abort",
              () => controller.error(new DOMException("aborted", "AbortError")),
              { once: true },
            );
          },
        }),
      );
    });
    const client = new AeroIdClient(
      "https://id.example/v1",
      () => "fixture-only",
      fetcher,
    );
    const controller = new AbortController();
    const pending = read(client, controller.signal);
    const rejected = expect(pending).rejects.toMatchObject({
      code: "request.canceled",
    });
    try {
      await vi.waitFor(() => expect(body).toBeDefined());
      controller.abort();
      await rejected;
      expect(signal?.aborted).toBe(true);
      expect(fetcher).toHaveBeenCalledTimes(1);
    } finally {
      controller.abort();
    }
  });
});
