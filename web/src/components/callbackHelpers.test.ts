import { describe, expect, it, vi } from "vitest";
import {
  callbackCreateIntent,
  normalizeCallbackEndpoint,
  parseCallbackEventTypes,
} from "./callbackHelpers";

describe("normalizeCallbackEndpoint", () => {
  it("normalizes an HTTPS endpoint without credentials", () => {
    expect(
      normalizeCallbackEndpoint("  https://callback.example.test/events  "),
    ).toBe("https://callback.example.test/events");
  });

  it("rejects insecure or credential-bearing callback URLs", () => {
    expect(() =>
      normalizeCallbackEndpoint("http://callback.example.test"),
    ).toThrow("HTTPS");
    expect(() =>
      normalizeCallbackEndpoint("https://user:password@callback.example.test"),
    ).toThrow("用户信息");
    expect(() =>
      normalizeCallbackEndpoint(
        `https://callback.example.test/${"x".repeat(2048)}`,
      ),
    ).toThrow("2048 个 UTF-8 字节");
  });
});

describe("callbackCreateIntent", () => {
  it("reuses a key for the same retried command and rotates for a new command", () => {
    const input = {
      endpoint: "https://callback.example.test/events",
      eventTypes: ["operation.completed"],
    };
    const makeKey = vi
      .fn<() => string>()
      .mockReturnValueOnce("key-1")
      .mockReturnValueOnce("key-2")
      .mockReturnValueOnce("key-3");
    const first = callbackCreateIntent(input, undefined, makeKey);

    expect(callbackCreateIntent(input, first, makeKey)).toBe(first);
    expect(
      callbackCreateIntent(
        { ...input, endpoint: "https://other.example.test/events" },
        first,
        makeKey,
      ).idempotencyKey,
    ).toBe("key-2");
    expect(callbackCreateIntent(input, undefined, makeKey).idempotencyKey).toBe(
      "key-3",
    );
    expect(makeKey).toHaveBeenCalledTimes(3);
  });
});

describe("parseCallbackEventTypes", () => {
  it("trims, removes blanks, and deduplicates filters", () => {
    expect(
      parseCallbackEventTypes(
        " operation.completed,\noperation.failed, operation.completed ",
      ),
    ).toEqual(["operation.completed", "operation.failed"]);
  });

  it("rejects empty and over-limit filter selections", () => {
    expect(() => parseCallbackEventTypes(" , \n ")).toThrow("至少填写");
    expect(() =>
      parseCallbackEventTypes(
        Array.from({ length: 65 }, (_, index) => `event-${index}`).join(","),
      ),
    ).toThrow("最多允许 64");
    expect(() => parseCallbackEventTypes("x".repeat(129))).toThrow(
      "不能超过 128 个 UTF-8 字节",
    );
    expect(parseCallbackEventTypes("é".repeat(64))).toEqual(["é".repeat(64)]);
    expect(() => parseCallbackEventTypes("é".repeat(65))).toThrow("UTF-8 字节");
  });
});
