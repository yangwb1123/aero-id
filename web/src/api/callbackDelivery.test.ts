import { describe, expect, it, vi } from "vitest";
import { CallbackClient } from "./callback";
import { AeroIdTransport } from "./transport";
import {
  callbackTestDelivery,
  callbackSubscriptionID,
  callbackDeliveryID,
  callbackOtherSubscriptionID,
} from "../components/callbackTestFixtures";
import {
  parseCallbackDelivery,
  parseCallbackDeliveryPage,
} from "./callbackDelivery";

function json(data: unknown) {
  return new Response(JSON.stringify({ data }), {
    headers: { "Content-Type": "application/json" },
  });
}
describe("metadata-only callback delivery API", () => {
  it("requires a positive exact local CAS revision, never defaults missing legacy proof", () => {
    for (const revision of [
      undefined,
      0,
      -1,
      1.5,
      Number.MAX_SAFE_INTEGER + 1,
    ]) {
      expect(() =>
        parseCallbackDelivery(
          { ...callbackTestDelivery(), revision },
          callbackSubscriptionID,
        ),
      ).toThrow();
    }
    expect(
      parseCallbackDelivery(callbackTestDelivery(), callbackSubscriptionID)
        .revision,
    ).toBe(7);
  });
  it("uses bounded tenant-free GET routes, canonical UUIDs and filters", async () => {
    const urls: URL[] = [];
    const expected = callbackTestDelivery();
    const fetcher = vi.fn(
      async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = new URL(String(input));
        urls.push(url);
        expect(init?.method).toBe("GET");
        expect(new Headers(init?.headers).get("Authorization")).toBe(
          "Bearer memory-token",
        );
        if (urls.length === 1)
          return json({
            deliveries: [expected],
            next_cursor: callbackDeliveryID,
          });
        return json({ delivery: expected });
      },
    );
    const client = new CallbackClient(
      new AeroIdTransport(
        "https://id.example/v1",
        () => "memory-token",
        fetcher as typeof fetch,
      ),
    );
    const page = await client.listDeliveries(
      callbackSubscriptionID.toUpperCase(),
      { status: "dead_letter" },
    );
    expect(page.items[0].id).toBe(callbackDeliveryID);
    expect(page.nextCursor).toBe(callbackDeliveryID);
    expect(
      await client.getDelivery(callbackSubscriptionID, callbackDeliveryID),
    ).toEqual(expected);
    expect(urls[0].pathname).toBe(
      `/v1/admin/callback-subscriptions/${callbackSubscriptionID}/deliveries`,
    );
    expect(urls[0].searchParams.get("limit")).toBe("50");
    expect(urls[0].searchParams.get("status")).toBe("dead_letter");
    expect(urls[0].searchParams.has("tenant_id")).toBe(false);
    expect(urls[1].pathname).toBe(
      `/v1/admin/callback-subscriptions/${callbackSubscriptionID}/deliveries/${callbackDeliveryID}`,
    );
  });
  it("rejects unknown fields, unsafe history and wrong targets rather than filtering or inferring success", () => {
    for (const mutation of [
      { payload: { token: "must-not-display" } },
      { response_body: "must-not-display" },
      { lease_owner: "private worker" },
      { secret_ciphertext: "must-not-display" },
      { endpoint: "https://source.secret" },
      { error_code: "password=must-not-display" },
      { status: "invented" },
      { status: ["pending"] },
      { status_code: 99 },
      { attempt: -1 },
      { lease_generation: Number.MAX_SAFE_INTEGER + 1 },
      { created_at: "bad-date" },
      { lease_until: null },
      { action: "https://source.secret/token" },
      { tenant_id: "" },
      { subscription_id: callbackOtherSubscriptionID },
    ])
      expect(() =>
        parseCallbackDelivery(
          { ...callbackTestDelivery(), ...mutation },
          callbackSubscriptionID,
        ),
      ).toThrow("不会推断");
    expect(() =>
      parseCallbackDelivery(
        { ...callbackTestDelivery(), id: callbackOtherSubscriptionID },
        callbackSubscriptionID,
        callbackDeliveryID,
      ),
    ).toThrow("不会推断");
  });
  it("rejects duplicate, unordered, foreign-tenant, filtered or stalled pages", () => {
    const first = callbackTestDelivery();
    const second = { ...first, id: "01800000-0000-7000-8000-000000000008" };
    for (const data of [
      { deliveries: [first, first], next_cursor: "" },
      { deliveries: [second, first], next_cursor: "" },
      {
        deliveries: [first, { ...second, tenant_id: "tenant-b" }],
        next_cursor: "",
      },
      { deliveries: [first], next_cursor: second.id },
      { deliveries: [], next_cursor: first.id },
      { deliveries: Array(101).fill(first), next_cursor: "" },
      { deliveries: "not-a-page", next_cursor: "" },
    ])
      expect(() =>
        parseCallbackDeliveryPage(data, callbackSubscriptionID),
      ).toThrow("不会推断");
    expect(() =>
      parseCallbackDeliveryPage(
        { deliveries: [first], next_cursor: "" },
        callbackSubscriptionID,
        "pending",
      ),
    ).toThrow("不会推断");
    expect(() =>
      parseCallbackDeliveryPage(
        { deliveries: [first], next_cursor: "" },
        callbackSubscriptionID,
        undefined,
        first.id,
      ),
    ).toThrow("不会推断");
    expect(
      parseCallbackDeliveryPage(
        { deliveries: [first, second], next_cursor: second.id },
        callbackSubscriptionID,
      ).items,
    ).toHaveLength(2);
  });
  it("rejects invalid IDs before transport and preserves cancellation", async () => {
    const fetcher = vi.fn(
      async (_input: RequestInfo | URL, init?: RequestInit) =>
        new Promise<Response>((_resolve, reject) =>
          init?.signal?.addEventListener("abort", () =>
            reject(new DOMException("aborted", "AbortError")),
          ),
        ),
    );
    const client = new CallbackClient(
      new AeroIdTransport(
        "https://id.example/v1",
        () => "memory-token",
        fetcher as typeof fetch,
      ),
    );
    await expect(client.listDeliveries("not-a-uuid")).rejects.toThrow(
      "不会推断",
    );
    await expect(
      client.getDelivery(callbackSubscriptionID, "not-a-uuid"),
    ).rejects.toThrow("不会推断");
    await expect(
      client.listDeliveries(callbackSubscriptionID, { cursor: "not-a-uuid" }),
    ).rejects.toThrow("不会推断");
    expect(fetcher).not.toHaveBeenCalled();
    const controller = new AbortController();
    const request = client.getDelivery(
      callbackSubscriptionID,
      callbackDeliveryID,
      controller.signal,
    );
    controller.abort();
    await expect(request).rejects.toMatchObject({ code: "request.canceled" });
    expect(fetcher).toHaveBeenCalledTimes(1);
  });
  it("does not accept a non-queued legacy replay response as success", async () => {
    const fetcher = vi.fn(async () =>
      json({ status: "completed", secret: "must-not-display" }),
    );
    const client = new CallbackClient(
      new AeroIdTransport(
        "https://id.example/v1",
        () => "memory-token",
        fetcher as typeof fetch,
      ),
    );
    await expect(
      client.replayDelivery(callbackDeliveryID, "original-key"),
    ).rejects.toThrow("保留原键");
    expect(fetcher).toHaveBeenCalledTimes(1);
  });
});
