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
});
