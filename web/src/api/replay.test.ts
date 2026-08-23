import { describe, expect, it, vi } from "vitest";
import { ReplayClient } from "./replay";
import { AeroIdTransport } from "./transport";

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

describe("ReplayClient", () => {
  it("sends controlled replay confirmation, commands and one explicit idempotency key", async () => {
    const requests: Array<{
      url: URL;
      headers: Headers;
      body?: BodyInit | null;
    }> = [];
    const fetcher = vi.fn(
      async (input: RequestInfo | URL, init?: RequestInit) => {
        requests.push({
          url: new URL(String(input)),
          headers: new Headers(init?.headers),
          body: init?.body,
        });
        return json(
          {
            data: {
              replay_id: "replay/1",
              mode: "controlled",
              status: "requested",
            },
          },
          requests.length === 1 ? 201 : 200,
        );
      },
    );
    const client = new ReplayClient(
      new AeroIdTransport(
        "https://accounts.example.test/v1",
        () => "token",
        fetcher as typeof fetch,
      ),
    );

    await client.create({
      mode: "controlled",
      accountID: "account-1",
      operationID: "operation-1",
      limit: 100,
      idempotencyKey: "replay-intent-1",
      confirmed: true,
      commands: [
        { source: "aero-vault", action: "summary", payload: { limit: 10 } },
      ],
    });
    await client.get("replay/1");

    expect(requests[0].url.pathname).toBe("/v1/audit/replays");
    expect(requests[0].headers.get("Idempotency-Key")).toBe("replay-intent-1");
    expect(JSON.parse(String(requests[0].body))).toMatchObject({
      mode: "controlled",
      account_id: "account-1",
      operation_id: "operation-1",
      idempotency_key: "replay-intent-1",
      confirmed: true,
      commands: [
        { source: "aero-vault", action: "summary", payload: { limit: 10 } },
      ],
    });
    expect(requests[1].url.pathname).toBe("/v1/audit/replays/replay%2F1");
  });

  it("continues forensic reconstruction with the opaque cursor and no command idempotency key", async () => {
    let headers = new Headers();
    let requestBody: unknown;
    const fetcher = vi.fn(
      async (_input: RequestInfo | URL, init?: RequestInit) => {
        headers = new Headers(init?.headers);
        requestBody = JSON.parse(String(init?.body));
        return json({
          data: { replay_id: "replay-1", mode: "forensic", events: [] },
        });
      },
    );
    const client = new ReplayClient(
      new AeroIdTransport(
        "https://accounts.example.test/v1",
        () => "token",
        fetcher as typeof fetch,
      ),
    );

    await client.create({
      mode: "forensic",
      operationID: "operation-1",
      from: "2026-08-20T10:00:00.000Z",
      to: "2026-08-21T10:00:00.000Z",
      cursor: "opaque-window-2",
      limit: 100,
      confirmed: false,
      commands: [],
    });

    expect(headers.has("Idempotency-Key")).toBe(false);
    expect(requestBody).toMatchObject({
      mode: "forensic",
      operation_id: "operation-1",
      from: "2026-08-20T10:00:00.000Z",
      to: "2026-08-21T10:00:00.000Z",
      cursor: "opaque-window-2",
      limit: 100,
    });
  });

  it("continues projection replay with the opaque server cursor", async () => {
    let requestURL = new URL("https://invalid.test");
    let requestBody: unknown;
    const fetcher = vi.fn(
      async (input: RequestInfo | URL, init?: RequestInit) => {
        requestURL = new URL(String(input));
        requestBody = JSON.parse(String(init?.body));
        return json({ data: { completed: true, replayed_events: 12 } });
      },
    );
    const client = new ReplayClient(
      new AeroIdTransport(
        "https://accounts.example.test/v1",
        () => "token",
        fetcher as typeof fetch,
      ),
    );

    await client.replayProjection({
      accountID: "01991c87-8dc8-7b45-b868-8b741fc984ef",
      source: "aero-id",
      cursor: "01991c87-8dc8-7b45-b868-8b741fc98400",
      limit: 250,
    });

    expect(requestURL.pathname).toBe("/v1/audit/replays/projection");
    expect(requestBody).toEqual({
      account_id: "01991c87-8dc8-7b45-b868-8b741fc984ef",
      source: "aero-id",
      cursor: "01991c87-8dc8-7b45-b868-8b741fc98400",
      limit: 250,
    });
  });
});
