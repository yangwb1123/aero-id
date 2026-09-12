import { describe, expect, it } from "vitest";
import { projectionReplayOutcome } from "./replayHelpers";

describe("projectionReplayOutcome", () => {
  it("never treats partial or drifted windows as a global match", () => {
    expect(
      projectionReplayOutcome({
        completed: false,
        replayed_events: 1000,
        matched: [{ dataset: "aero-vault.usage" }],
      }),
    ).toBe("partial");
    expect(
      projectionReplayOutcome({
        completed: true,
        replayed_events: 3,
        divergent: [{ dataset: "aero-vault.usage" }],
      }),
    ).toBe("divergent");
    expect(
      projectionReplayOutcome({ completed: true, replayed_events: 3 }),
    ).toBe("matched");
    expect(
      projectionReplayOutcome({ completed: true, replayed_events: 0 }),
    ).toBe("empty");
  });
});
