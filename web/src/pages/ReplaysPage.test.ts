import { describe, expect, it, vi } from "vitest";
import type { ReplayCreateInput } from "../api/replay";
import {
  controlledReplaySignature,
  nextReplayIdempotencyKey,
  replayContinuation,
  replayTimeWindow,
} from "../components/replayHelpers";

describe("controlledReplaySignature", () => {
  it("binds a key to its command while ignoring protocol confirmation fields", () => {
    const input: ReplayCreateInput = {
      mode: "controlled",
      idempotencyKey: "key-1",
      accountID: "account-1",
      limit: 10,
      confirmed: false,
      commands: [{ source: "aero-im", action: "summary", payload: {} }],
    };

    expect(
      controlledReplaySignature({
        ...input,
        idempotencyKey: "key-2",
        confirmed: true,
      }),
    ).toBe(controlledReplaySignature(input));
    expect(
      controlledReplaySignature({ ...input, accountID: "account-2" }),
    ).not.toBe(controlledReplaySignature(input));
  });
});

describe("nextReplayIdempotencyKey", () => {
  it("rotates only after successful controlled intents using the current key", () => {
    const makeKey = vi.fn(() => "next-key");
    const input: ReplayCreateInput = {
      mode: "controlled",
      idempotencyKey: "submitted-key",
      accountID: "account-1",
      limit: 10,
      confirmed: true,
      commands: [],
    };

    expect(nextReplayIdempotencyKey(input, "submitted-key", makeKey)).toBe(
      "next-key",
    );
    expect(nextReplayIdempotencyKey(input, "newer-key", makeKey)).toBe(
      "newer-key",
    );
    expect(
      nextReplayIdempotencyKey(
        { ...input, mode: "dry-run" },
        "submitted-key",
        makeKey,
      ),
    ).toBe("submitted-key");
    expect(makeKey).toHaveBeenCalledTimes(1);
  });
});

describe("replayContinuation", () => {
  it("preserves the bounded dry-run scope and commands while replacing the cursor", () => {
    const input: ReplayCreateInput = {
      mode: "dry-run",
      accountID: "account-1",
      operationID: "operation-1",
      cursor: "window-1",
      limit: 25,
      confirmed: false,
      commands: [{ source: "aero-vault", action: "summary", payload: {} }],
    };

    expect(replayContinuation(input, "window-2")).toEqual({
      ...input,
      cursor: "window-2",
    });
  });

  it("does not create a continuation request for controlled replay", () => {
    expect(() =>
      replayContinuation(
        {
          mode: "controlled",
          accountID: "account-1",
          limit: 25,
          confirmed: true,
          commands: [],
        },
        "window-2",
      ),
    ).toThrow("只有 Forensic 或 Dry-run");
  });
});

describe("replayTimeWindow", () => {
  it("normalizes an optional half-open time window to RFC3339", () => {
    const result = replayTimeWindow(
      "2026-08-20T10:00:00Z",
      "2026-08-21T10:00:00Z",
    );
    expect(result).toEqual({
      from: "2026-08-20T10:00:00.000Z",
      to: "2026-08-21T10:00:00.000Z",
    });
  });

  it("rejects an empty or reversed interval", () => {
    expect(() =>
      replayTimeWindow("2026-08-21T10:00:00Z", "2026-08-21T10:00:00Z"),
    ).toThrow("开始时间必须早于结束时间");
    expect(() =>
      replayTimeWindow("2026-08-22T10:00:00Z", "2026-08-21T10:00:00Z"),
    ).toThrow("开始时间必须早于结束时间");
  });
});
