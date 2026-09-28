import { describe, expect, it } from "vitest";
import { validateDatasetSelection } from "./accountDataHelpers";

const datasets = [
  { name: "aero-im.workspaces", source: "aero-im", pii: false },
  { name: "aero-vault.usage", source: "aero-vault", pii: false },
];

describe("validateDatasetSelection", () => {
  it("preserves an explicit valid selection and the empty-means-all selection", () => {
    expect(validateDatasetSelection(["aero-vault.usage"], datasets)).toEqual([
      "aero-vault.usage",
    ]);
    expect(validateDatasetSelection([], datasets)).toEqual([]);
  });

  it("rejects selections that became stale against the current allow-list", () => {
    expect(() =>
      validateDatasetSelection(["aero-im.retired"], datasets),
    ).toThrow("allow-list");
  });
});
