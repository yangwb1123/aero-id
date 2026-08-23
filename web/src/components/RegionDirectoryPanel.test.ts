import { describe, expect, it } from "vitest";
import { regionEpoch } from "./RegionDirectoryPanel";

describe("regionEpoch", () => {
  it("accepts only a non-negative safe integer", () => {
    expect(regionEpoch({ fenced_epoch: 7 })).toBe(7);
    expect(() => regionEpoch({ fenced_epoch: -1 })).toThrow("Fenced Epoch");
    expect(() => regionEpoch({ fenced_epoch: 1.5 })).toThrow("Fenced Epoch");
    expect(() => regionEpoch({ fenced_epoch: "7" })).toThrow("Fenced Epoch");
  });
});
