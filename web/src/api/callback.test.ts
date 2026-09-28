import { describe, expect, it, vi } from "vitest";
import { CallbackClient } from "./callback";
import { AeroIdTransport } from "./transport";

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

describe("CallbackClient", () => {
  it("uses the published callback management routes without exposing secrets in URLs", async () => {
    const requests: Array<{
      url: URL;
      method?: string;
      headers: Headers;
      body?: BodyInit | null;
    }> = [];
    const fetcher = vi.fn(
      async (input: RequestInfo | URL, init?: RequestInit) => {
        requests.push({
          url: new URL(String(input)),
          method: init?.method,
          headers: new Headers(init?.headers),
          body: init?.body,
        });
        switch (requests.length) {
          case 1:
            return json({
              data: {
                subscriptions: [{ id: "subscription/1", verified: true }],
                next_cursor: "subscription-1",
              },
            });
          case 2:
            return json(
              {
                data: {
                  subscription: { id: "subscription/1", verified: true },
                  secret: "one-time-secret",
                },
              },
              201,
            );
          case 3:
            return new Response(null, { status: 204 });
          default:
            return json({ data: { status: "queued" } });
        }
      },
    );
    const client = new CallbackClient(
      new AeroIdTransport(
        "https://accounts.example.test/v1",
        () => "token",
        fetcher as typeof fetch,
      ),
    );

    await expect(client.list("opaque:cursor")).resolves.toEqual({
      items: [{ id: "subscription/1", verified: true }],
      nextCursor: "subscription-1",
    });
    await expect(
      client.create(
        {
          endpoint: "https://callback.example.test/events",
          eventTypes: ["operation.completed"],
        },
        "callback-create-1",
      ),
    ).resolves.toMatchObject({ secret: "one-time-secret" });
    await client.deleteSubscription("subscription/1");
    await client.replayDelivery("delivery/1", "delivery-replay-1");
    await client.replayDelivery("delivery/2", "delivery-replay-2");
    await client.replayDelivery("delivery/2", "delivery-replay-2");

    expect(requests[0].url.pathname).toBe("/v1/admin/callback-subscriptions");
    expect(requests[0].url.searchParams.get("cursor")).toBe("opaque:cursor");
    expect(JSON.parse(String(requests[1].body))).toEqual({
      endpoint: "https://callback.example.test/events",
      event_types: ["operation.completed"],
    });
    expect(requests[1].url.toString()).not.toContain("one-time-secret");
    expect(requests[1].headers.get("Idempotency-Key")).toBe(
      "callback-create-1",
    );
    expect(requests[2].url.pathname).toBe(
      "/v1/admin/callback-subscriptions/subscription%2F1",
    );
    expect(requests[2].method).toBe("DELETE");
    expect(requests[3].url.pathname).toBe(
      "/v1/admin/callback-subscriptions/deliveries/delivery%2F1/replay",
    );
    expect(requests[3].headers.get("Idempotency-Key")).toBe(
      "delivery-replay-1",
    );
    expect(requests[4].headers.get("Idempotency-Key")).toBe(
      "delivery-replay-2",
    );
    expect(requests[5].headers.get("Idempotency-Key")).toBe(
      "delivery-replay-2",
    );
    for (const request of requests)
      expect(request.headers.get("Authorization")).toBe("Bearer token");
  });

  it("accepts the safe metadata-only body returned for an idempotent retry", async () => {
    const keys: string[] = [];
    const fetcher = vi.fn(
      async (_input: RequestInfo | URL, init?: RequestInit) => {
        keys.push(new Headers(init?.headers).get("Idempotency-Key") ?? "");
        return json({ data: { subscription: { id: "subscription-1" } } });
      },
    );
    const client = new CallbackClient(
      new AeroIdTransport(
        "https://accounts.example.test/v1",
        () => "token",
        fetcher as typeof fetch,
      ),
    );

    const input = {
      endpoint: "https://callback.example.test/events",
      eventTypes: ["event"],
    };
    await expect(
      client.create(input, "callback-create-retry"),
    ).resolves.toEqual({ subscription: { id: "subscription-1" } });
    await expect(
      client.create(input, "callback-create-retry"),
    ).resolves.toEqual({ subscription: { id: "subscription-1" } });
    expect(keys).toEqual(["callback-create-retry", "callback-create-retry"]);
  });

  it("rejects missing idempotency keys before sending callback commands", async () => {
    const fetcher = vi.fn(async () => json({ data: { status: "queued" } }));
    const client = new CallbackClient(
      new AeroIdTransport(
        "https://accounts.example.test/v1",
        () => "token",
        fetcher as typeof fetch,
      ),
    );

    await expect(
      client.create(
        {
          endpoint: "https://callback.example.test/events",
          eventTypes: ["event"],
        },
        "  ",
      ),
    ).rejects.toThrow("Callback 注册需要幂等键");
    await expect(client.replayDelivery("delivery-1", "  ")).rejects.toThrow(
      "Callback 重放需要幂等键",
    );
    expect(fetcher).not.toHaveBeenCalled();
  });
});
