import type { OperationStepInput } from "../api/aeroId";
import type { Dataset, JsonRecord } from "../types";
import { jobDatasetLabels, jobRecord } from "./jobSummaryFields";
import {
  newOperationStep,
  type OperationStepDraft,
} from "./operationFormHelpers";

// Editing helpers only. The existing parser, CommandIntent and source checks
// remain authoritative; no implicit selection, payload repair or new command.
export function operationDatasetChoices(
  datasets: Dataset[],
  source: string,
): Dataset[] {
  return datasets
    .filter(
      (item) =>
        item.source === source &&
        item.name.startsWith(`${source}.`) &&
        Object.hasOwn(jobDatasetLabels, item.name),
    )
    .slice(0, 11);
}
export function operationDraftSelection(
  step: OperationStepDraft,
  datasets: Dataset[],
):
  | {
      payload: JsonRecord;
      selected: string[];
    }
  | undefined {
  if (new TextEncoder().encode(step.payload).byteLength > 64 * 1024)
    return undefined;
  try {
    const payload = jobRecord(JSON.parse(step.payload));
    const selected = payload?.datasets;
    const allowed = new Set(
      operationDatasetChoices(datasets, step.source).map((item) => item.name),
    );
    if (
      !payload ||
      !Array.isArray(selected) ||
      selected.length > 11 ||
      !selected.every(
        (name) => typeof name === "string" && allowed.has(name),
      ) ||
      new Set(selected).size !== selected.length
    )
      return undefined;
    return { payload, selected };
  } catch {
    return undefined;
  }
}
export function selectOperationDataset(
  step: OperationStepDraft,
  datasets: Dataset[],
  name: string,
  selected: boolean,
): string | undefined {
  const current = operationDraftSelection(step, datasets);
  if (
    !current ||
    !operationDatasetChoices(datasets, step.source).some(
      (item) => item.name === name,
    )
  )
    return undefined;
  const names = current.selected.filter((item) => item !== name);
  if (selected) names.push(name);
  return JSON.stringify(
    { ...current.payload, datasets: names.sort() },
    null,
    2,
  );
}
export function changeOperationSource(
  step: OperationStepDraft,
  datasets: Dataset[],
  source: OperationStepInput["source"],
): Partial<OperationStepDraft> | undefined {
  if (!["snaplink", "aero-im", "aero-vault"].includes(source)) return undefined;
  const current = operationDraftSelection(step, datasets);
  if (!current) return undefined;
  if (source === step.source) return {};
  return {
    source,
    payload: JSON.stringify({ ...current.payload, datasets: [] }, null, 2),
  };
}
export function newSelectionOperationStep(
  steps: OperationStepDraft[],
): OperationStepDraft {
  const keys = new Set(steps.map((step) => step.stepKey.trim()));
  let number = 1;
  while (keys.has(`source-summary-${number}`)) number += 1;
  return {
    ...newOperationStep(),
    stepKey: `source-summary-${number}`,
    payload: JSON.stringify({ datasets: [] }, null, 2),
  };
}
