import { describe, expect, it } from "vitest";
import { cutoverTransitions } from "./legacyHelpers";

describe("cutoverTransitions", () => {
  it("exposes only server-valid next hops", () => {
    expect(cutoverTransitions("legacy")).toEqual(["shadow"]);
    expect(cutoverTransitions("shadow")).toEqual(["cut_over"]);
    expect(cutoverTransitions("cut_over")).toEqual(["rolled_back"]);
    expect(cutoverTransitions("rolled_back")).toEqual(["legacy", "shadow"]);
  });
});
