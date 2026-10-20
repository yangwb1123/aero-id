import { describe, expect, it } from "vitest";
import {
  newOperationStep,
  operationIntentSignature,
  operationNeedsDatasets,
  parseOperationSteps,
} from "./operationFormHelpers";

const datasets = [
  { name: "aero-im.profile", source: "aero-im", pii: true },
  { name: "aero-vault.usage", source: "aero-vault", pii: false },
];

describe("operation intent signature", () => {
  it("binds retries to the same operation but excludes its idempotency key", () => {
    const input = {
      accountID: "account-1",
      operationType: "sync" as const,
      idempotencyKey: "key-1",
      requestPayload: { source: "aero-im" },
      steps: [
        {
          stepKey: "summary-1",
          source: "aero-im" as const,
          action: "summary" as const,
          requestPayload: { datasets: ["aero-im.workspaces"] },
        },
      ],
    };

    expect(
      operationIntentSignature({ ...input, idempotencyKey: "key-2" }),
    ).toBe(operationIntentSignature(input));
    expect(
      operationIntentSignature({
        ...input,
        requestPayload: { source: "aero-vault" },
      }),
    ).not.toBe(operationIntentSignature(input));
  });
});

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
    expect(operationNeedsDatasets("aero-vault", "summary")).toBe(true);
    expect(operationNeedsDatasets("snaplink", "summary")).toBe(true);
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

  it("requires explicit scope for all preview sources, including Vault and Snaplink", () => {
    for (const source of ["snaplink", "aero-vault"] as const) {
      expect(() =>
        parseOperationSteps(
          [{ ...newOperationStep(), source, payload: "{}" }],
          "sync",
          datasets,
        ),
      ).toThrow("显式同源");
    }
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
