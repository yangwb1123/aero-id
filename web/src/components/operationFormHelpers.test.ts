import { describe, expect, it } from "vitest";
import {
  newOperationStep,
  operationNeedsDatasets,
  parseOperationSteps,
} from "./operationFormHelpers";

const datasets = [
  { name: "aero-im.profile", source: "aero-im", pii: true },
  { name: "aero-vault.usage", source: "aero-vault", pii: false },
];

describe("operation form validation", () => {
  it("starts an IM summary with a registry dataset when available", () => {
    const step = newOperationStep(datasets);

    expect(step.source).toBe("aero-im");
    expect(JSON.parse(step.payload)).toEqual({
      datasets: ["aero-im.profile"],
    });
  });

  it("requires exact datasets for destructive and IM actions", () => {
    expect(operationNeedsDatasets("aero-im", "summary")).toBe(true);
    expect(operationNeedsDatasets("aero-vault", "summary")).toBe(false);
    expect(operationNeedsDatasets("snaplink", "erase")).toBe(true);

    expect(() =>
      parseOperationSteps(
        [
          {
            ...newOperationStep(),
            payload: "{}",
          },
        ],
        "sync",
        datasets,
      ),
    ).toThrow("datasets");
  });

  it("rejects unknown dataset scope before submit", () => {
    const base = newOperationStep(datasets);
    expect(() =>
      parseOperationSteps(
        [
          {
            ...base,
            payload: JSON.stringify({ datasets: ["aero-im.unknown"] }),
          },
        ],
        "sync",
        datasets,
      ),
    ).toThrow("allow-list");
    expect(() =>
      parseOperationSteps(
        [base, { ...base, id: "second", stepKey: "second" }],
        "sync",
        datasets,
      ),
    ).not.toThrow();
  });
});
