import { describe, expect, it } from "vitest";
import type { ReplayCreateInput } from "../api/replay";
import { replayContinuation } from "./ReplaysPage";

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
