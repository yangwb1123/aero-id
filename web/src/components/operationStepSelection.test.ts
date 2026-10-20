import { describe, expect, it } from "vitest";
import { testDatasets } from "./commandTestFixtures";
import {
  changeOperationSource,
  newSelectionOperationStep,
  operationDatasetChoices,
  operationDraftSelection,
  selectOperationDataset,
} from "./operationStepSelection";

describe("explicit Operation step selection", () => {
  it("starts without scope and generates unique normalized keys without changing survivors", () => {
    const first = newSelectionOperationStep([]);
    expect(operationDraftSelection(first, testDatasets)?.selected).toEqual([]);
    expect(first.compensationAction).toBe("");
    expect(
      newSelectionOperationStep([{ ...first, stepKey: " source-summary-1 " }])
        .stepKey,
    ).toBe("source-summary-2");
    expect(first.stepKey).toBe("source-summary-1");
  });
  it("only offers registered, explicitly approved names owned by that source", () => {
    const values = [
      ...testDatasets,
      { name: "aero-vault.unapproved", source: "aero-vault", pii: false },
      { name: "aero-im.profile", source: "aero-vault", pii: false },
    ];
    expect(
      operationDatasetChoices(values, "aero-vault").map((item) => item.name),
    ).toEqual(["aero-vault.usage"]);
    expect(operationDatasetChoices([], "aero-im")).toEqual([]);
  });
  it("checkboxes update only explicit scope, preserve expert values and support removing the last choice", () => {
    const step = {
      ...newSelectionOperationStep([]),
      payload: '{"datasets":[],"note":"reviewed"}',
    };
    const selected = selectOperationDataset(
      step,
      testDatasets,
      "aero-im.workspaces",
      true,
    )!;
    expect(JSON.parse(selected)).toEqual({
      datasets: ["aero-im.workspaces"],
      note: "reviewed",
    });
    expect(
      JSON.parse(
        selectOperationDataset(
          { ...step, payload: selected },
          testDatasets,
          "aero-im.workspaces",
          false,
        )!,
      ),
    ).toEqual({ datasets: [], note: "reviewed" });
    expect(
      selectOperationDataset(step, testDatasets, "aero-vault.usage", true),
    ).toBeUndefined();
    expect(step.payload).toBe('{"datasets":[],"note":"reviewed"}');
  });
  it("changing source is explicit, preserves expert data and does not choose a new dataset", () => {
    const step = {
      ...newSelectionOperationStep([]),
      compensationAction: "erase",
      payload: '{"datasets":["aero-im.workspaces"],"note":"reviewed"}',
    };
    const patch = changeOperationSource(step, testDatasets, "aero-vault")!;
    expect(patch.source).toBe("aero-vault");
    expect(JSON.parse(patch.payload!)).toEqual({
      datasets: [],
      note: "reviewed",
    });
    expect(patch.stepKey).toBeUndefined();
    expect(patch.compensationAction).toBeUndefined();
    expect(changeOperationSource(step, testDatasets, "aero-im")).toEqual({});
  });
  it("missing, non-object, oversized or invalid scopes cannot be silently edited into a new plan", () => {
    const payloads = [
      "{}",
      "null",
      "[]",
      '{"datasets":null}',
      '{"datasets":[false]}',
      '{"datasets":["aero-im.unknown"]}',
      '{"datasets":["aero-vault.usage"]}',
      JSON.stringify({ datasets: Array(12).fill("aero-im.workspaces") }),
      JSON.stringify({ datasets: [], note: "x".repeat(65536) }),
    ];
    for (const payload of payloads) {
      const step = { ...newSelectionOperationStep([]), payload };
      expect(operationDraftSelection(step, testDatasets)).toBeUndefined();
      expect(
        selectOperationDataset(step, testDatasets, "aero-im.workspaces", true),
      ).toBeUndefined();
      expect(
        changeOperationSource(step, testDatasets, "aero-vault"),
      ).toBeUndefined();
      expect(step.payload).toBe(payload);
    }
  });
});
