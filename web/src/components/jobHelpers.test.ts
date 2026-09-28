import { describe, expect, it, vi } from "vitest";
import { nextJobCreateIntent } from "./jobHelpers";

describe("nextJobCreateIntent", () => {
  it("reuses keys for retries and rotates them for distinct job intents", () => {
    const command = {
      jobType: "export",
      accountID: "account-1",
      datasets: ["aero-vault.usage", "aero-im.workspaces"],
    };
    const makeKey = vi
      .fn<() => string>()
      .mockReturnValueOnce("key-1")
      .mockReturnValueOnce("key-2")
      .mockReturnValueOnce("key-3");
    const first = nextJobCreateIntent(command, undefined, makeKey);

    expect(nextJobCreateIntent(command, first, makeKey)).toBe(first);
    expect(
      nextJobCreateIntent(
        { ...command, datasets: [...command.datasets].reverse() },
        first,
        makeKey,
      ),
    ).toBe(first);
    expect(
      nextJobCreateIntent({ ...command, jobType: "erase" }, first, makeKey)
        .idempotencyKey,
    ).toBe("key-2");
    expect(
      nextJobCreateIntent(command, undefined, makeKey).idempotencyKey,
    ).toBe("key-3");
    expect(makeKey).toHaveBeenCalledTimes(3);
  });
});
