import { describe, expect, it, vi } from "vitest";
import { rememberIdempotencyKey } from "./idempotencyHelpers";

describe("rememberIdempotencyKey", () => {
  it("reuses a key for a retry and creates a new one after settlement", () => {
    const keys = new Map<string, string>();
    const makeKey = vi
      .fn<() => string>()
      .mockReturnValueOnce("key-1")
      .mockReturnValueOnce("key-2");
    const intent = { action: "grant", uid: "uid-1", role: "operator" };
    const first = rememberIdempotencyKey(intent, keys, makeKey);

    expect(rememberIdempotencyKey(intent, keys, makeKey)).toEqual(first);
    keys.delete(first.signature);
    expect(rememberIdempotencyKey(intent, keys, makeKey).idempotencyKey).toBe(
      "key-2",
    );
    expect(makeKey).toHaveBeenCalledTimes(2);
  });

  it("bounds remembered intents and evicts the least-recently-used key", () => {
    const keys = new Map<string, string>();
    const makeKey = vi.fn(() => `key-${makeKey.mock.calls.length + 1}`);
    const first = rememberIdempotencyKey({ id: 1 }, keys, makeKey);
    for (let id = 2; id <= 32; id++) {
      rememberIdempotencyKey({ id }, keys, makeKey);
    }
    rememberIdempotencyKey({ id: 32 }, keys, makeKey);
    rememberIdempotencyKey({ id: 33 }, keys, makeKey);

    expect(keys.size).toBe(32);
    expect(keys.has(first.signature)).toBe(false);
  });
});
