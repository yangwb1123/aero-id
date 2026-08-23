import { describe, expect, it, vi } from "vitest";
import { RegionClient } from "./region";
import { AeroIdTransport } from "./transport";

function json(body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });
}

describe("RegionClient", () => {
  it("reads the durable directory and promotes with the observed epoch", async () => {
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
        if (requests.length === 1)
          return json({
            data: {
              account_id: "account/1",
              home_region: "local",
              fenced_epoch: 4,
            },
          });
        if (requests.length === 2)
          return json({
            data: {
              account_id: "account/1",
              target_region: "us-east",
              fenced_epoch: 5,
            },
          });
        return json({
          data: {
            account_id: "account/1",
            home_region: "us-east",
            fenced_epoch: 5,
          },
        });
      },
    );
    const client = new RegionClient(
      new AeroIdTransport(
        "https://accounts.example.test/v1",
        () => "token",
        fetcher as typeof fetch,
      ),
    );

    await expect(client.getDirectory("account/1")).resolves.toMatchObject({
      home_region: "local",
      fenced_epoch: 4,
    });
    await client.promote({
      accountID: "account/1",
      targetRegion: "us-east",
      expectedEpoch: 4,
      idempotencyKey: "promotion-1",
    });
    await client.getDirectory("account/1");

    expect(requests[0].url.pathname).toBe(
      "/v1/admin/regions/directory/account%2F1",
    );
    expect(requests[1].method).toBe("POST");
    expect(requests[1].url.pathname).toBe("/v1/admin/regions/promote");
    expect(
      new Headers(fetcher.mock.calls[1]?.[1]?.headers).get("Idempotency-Key"),
    ).toBe("promotion-1");
    expect(JSON.parse(String(requests[1].body))).toEqual({
      account_id: "account/1",
      target_region: "us-east",
      expected_epoch: 4,
    });
    expect(requests[2].headers.get("X-Aero-Write-Epoch")).toBe("us-east:5");
  });
});
