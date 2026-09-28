import { describe, expect, it, vi } from "vitest";
import { AeroIdTransport } from "./transport";

describe("AeroIdTransport", () => {
  it("bounds a streamed download when content length is absent", async () => {
    const fetcher = vi.fn(async () => new Response("12345"));
    const transport = new AeroIdTransport(
      "https://accounts.example.test/v1",
      () => "token",
      fetcher as typeof fetch,
    );

    await expect(transport.download("/export", {}, 4)).rejects.toMatchObject({
      code: "response.too_large",
    });
  });

  it("keeps the deadline active while reading the response body", async () => {
    const fetcher = vi.fn(
      async (_input: RequestInfo | URL, init?: RequestInit) => {
        const stream = new ReadableStream<Uint8Array>({
          start(controller) {
            init?.signal?.addEventListener("abort", () => {
              controller.error(new DOMException("aborted", "AbortError"));
            });
          },
        });
        return new Response(stream);
      },
    );
    const transport = new AeroIdTransport(
      "https://accounts.example.test/v1",
      () => "token",
      fetcher as typeof fetch,
    );

    await expect(
      transport.download("/export", { timeoutMs: 10 }, 16),
    ).rejects.toMatchObject({ code: "network.timeout" });
  });

  it("does not let an older write response regress a newer writer epoch", async () => {
    const resolvers: Array<(response: Response) => void> = [];
    const sentHeaders: Headers[] = [];
    const fetcher = vi.fn((_input: RequestInfo | URL, init?: RequestInit) => {
      sentHeaders.push(new Headers(init?.headers));
      return new Promise<Response>((resolve) => resolvers.push(resolve));
    });
    const transport = new AeroIdTransport(
      "https://accounts.example.test/v1",
      () => "token",
      fetcher as typeof fetch,
    );

    const olderWrite = transport.request("POST", "/older", { body: {} });
    const newerWrite = transport.request("POST", "/newer", { body: {} });
    resolvers[1](
      new Response('{"data":{}}', {
        headers: { "X-Aero-Write-Epoch": "local:8" },
      }),
    );
    await newerWrite;
    resolvers[0](
      new Response('{"data":{}}', {
        headers: { "X-Aero-Write-Epoch": "local:7" },
      }),
    );
    await olderWrite;

    const nextRead = transport.request("GET", "/me");
    resolvers[2](new Response('{"data":{}}'));
    await nextRead;
    expect(sentHeaders[2].get("X-Aero-Write-Epoch")).toBe("local:8");
  });

  it("does not overwrite an explicit writer pin with an in-flight old response", async () => {
    let resolveOld!: (response: Response) => void;
    const sentHeaders: Headers[] = [];
    const fetcher = vi.fn((_input: RequestInfo | URL, init?: RequestInit) => {
      sentHeaders.push(new Headers(init?.headers));
      if (sentHeaders.length === 1) {
        return new Promise<Response>((resolve) => {
          resolveOld = resolve;
        });
      }
      return Promise.resolve(new Response('{"data":{}}'));
    });
    const transport = new AeroIdTransport(
      "https://accounts.example.test/v1",
      () => "token",
      fetcher as typeof fetch,
    );

    const oldWrite = transport.request("POST", "/old", { body: {} });
    transport.pinWriteEpoch("eu-west", 9);
    resolveOld(
      new Response('{"data":{}}', {
        headers: { "X-Aero-Write-Epoch": "local:7" },
      }),
    );
    await oldWrite;

    await transport.request("GET", "/me");
    expect(sentHeaders[1].get("X-Aero-Write-Epoch")).toBe("eu-west:9");
  });

  it("surfaces a successful stale GET as a recoverable region error", async () => {
    const fetcher = vi.fn(
      async () =>
        new Response(JSON.stringify({ data: { value: "stale" } }), {
          status: 200,
          headers: {
            "Content-Type": "application/json",
            "X-Aero-Stale-Read": "true",
            "X-Request-ID": "request-stale-1",
          },
        }),
    );
    const transport = new AeroIdTransport(
      "https://accounts.example.test/v1",
      () => "token",
      fetcher as typeof fetch,
    );

    await expect(
      transport.request("GET", "/me/overview"),
    ).rejects.toMatchObject({
      status: 200,
      code: "region.stale_read",
      requestId: "request-stale-1",
      message: expect.stringContaining("切换到账户主区域"),
    });
  });

  it("does not treat stale-read metadata as a failed command response", async () => {
    const fetcher = vi.fn(
      async () =>
        new Response(JSON.stringify({ data: { status: "accepted" } }), {
          status: 202,
          headers: {
            "Content-Type": "application/json",
            "X-Aero-Stale-Read": "true",
          },
        }),
    );
    const transport = new AeroIdTransport(
      "https://accounts.example.test/v1",
      () => "token",
      fetcher as typeof fetch,
    );

    await expect(transport.request("POST", "/me/erase")).resolves.toEqual({
      data: { status: "accepted" },
    });
  });

  it("rejects stale one-time export downloads instead of handing them to the user", async () => {
    const fetcher = vi.fn(
      async () =>
        new Response("stale-export", {
          status: 200,
          headers: { "X-Aero-Stale-Read": "true" },
        }),
    );
    const transport = new AeroIdTransport(
      "https://accounts.example.test/v1",
      () => "token",
      fetcher as typeof fetch,
    );

    await expect(transport.download("/export", {}, 64)).rejects.toMatchObject({
      code: "region.stale_read",
    });
  });
});
