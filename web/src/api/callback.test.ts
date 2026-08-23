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
      client.create({
        endpoint: "https://callback.example.test/events",
        eventTypes: ["operation.completed"],
      }),
    ).resolves.toMatchObject({ secret: "one-time-secret" });
    await client.deleteSubscription("subscription/1");
    await client.replayDelivery("delivery/1");

    expect(requests[0].url.pathname).toBe("/v1/admin/callback-subscriptions");
    expect(requests[0].url.searchParams.get("cursor")).toBe("opaque:cursor");
    expect(JSON.parse(String(requests[1].body))).toEqual({
      endpoint: "https://callback.example.test/events",
      event_types: ["operation.completed"],
    });
    expect(requests[1].url.toString()).not.toContain("one-time-secret");
    expect(requests[2].url.pathname).toBe(
      "/v1/admin/callback-subscriptions/subscription%2F1",
    );
    expect(requests[2].method).toBe("DELETE");
    expect(requests[3].url.pathname).toBe(
      "/v1/admin/callback-subscriptions/deliveries/delivery%2F1/replay",
    );
    for (const request of requests)
      expect(request.headers.get("Authorization")).toBe("Bearer token");
  });
});
