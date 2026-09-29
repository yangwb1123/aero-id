import type { OperationCreateInput, OperationStepInput } from "../api/aeroId";
import type { Dataset, JsonRecord } from "../types";

export interface OperationStepDraft {
  id: string;
  stepKey: string;
  source: OperationStepInput["source"];
  action: OperationStepInput["action"];
  compensationAction: string;
  payload: string;
}

export const operationSources = [
  { value: "snaplink", label: "Snaplink" },
  { value: "aero-im", label: "Aero IM" },
  { value: "aero-vault", label: "Aero Vault" },
];
export function operationIntentSignature(input: OperationCreateInput): string {
  return JSON.stringify({
    accountID: input.accountID,
    homeRegion: input.homeRegion,
    operationType: input.operationType,
    requestPayload: input.requestPayload,
    steps: input.steps,
  });
}

export const operationActions = [
  "sync",
  "summary",
  "source.summary",
  "erase",
  "delete",
  "source.erase",
].map((value) => ({ value, label: value }));

export function newOperationStep(datasets: Dataset[] = []): OperationStepDraft {
  const defaultDataset = datasets.find(
    (dataset) => dataset.source === "aero-im",
  )?.name;
  return {
    id: crypto.randomUUID(),
    stepKey: "source-summary-1",
    source: "aero-im",
    action: "summary",
    compensationAction: "",
    payload: JSON.stringify(
      defaultDataset ? { datasets: [defaultDataset] } : {},
      null,
      2,
    ),
  };
}

export function operationNeedsDatasets(
  source: OperationStepInput["source"],
  action: OperationStepInput["action"],
): boolean {
  if (["erase", "delete", "source.erase"].includes(action)) return true;
  return (
    source === "aero-im" &&
    ["sync", "summary", "source.summary"].includes(action)
  );
}

function isRecord(value: unknown): value is JsonRecord {
  return Boolean(value) && typeof value === "object" && !Array.isArray(value);
}

function parsePayload(value: string, index: number): JsonRecord {
  let parsed: unknown;
  try {
    parsed = JSON.parse(value || "{}");
  } catch {
    throw new Error(`第 ${index + 1} 个步骤的 Payload 不是有效 JSON`);
  }
  if (!isRecord(parsed)) {
    throw new Error(`第 ${index + 1} 个步骤的 Payload 必须是 JSON 对象`);
  }
  return parsed;
}

function operationActionMatchesType(
  operationType: OperationCreateInput["operationType"],
  action: OperationStepInput["action"],
): boolean {
  if (operationType === "sync")
    return ["sync", "summary", "source.summary"].includes(action);
  if (operationType === "erase")
    return ["erase", "delete", "source.erase"].includes(action);
  return true;
}

function validateDatasets(
  payload: JsonRecord,
  source: OperationStepInput["source"],
  action: OperationStepInput["action"],
  allowed: Set<string>,
  index: number,
): void {
  const raw = payload.datasets;
  if (raw === undefined && !operationNeedsDatasets(source, action)) return;
  if (raw === undefined || (Array.isArray(raw) && raw.length === 0)) {
    if (!operationNeedsDatasets(source, action)) return;
    throw new Error(`第 ${index + 1} 个步骤必须包含非空 datasets 数组`);
  }
  if (!Array.isArray(raw) || raw.some((item) => typeof item !== "string")) {
    throw new Error(`第 ${index + 1} 个步骤的 datasets 必须是字符串数组`);
  }
  const values = raw as string[];
  if (
    new Set(values).size !== values.length ||
    values.some((item) => !allowed.has(item))
  ) {
    throw new Error(
      `第 ${index + 1} 个步骤包含不在当前 allow-list 中的 dataset`,
    );
  }
}

export function parseOperationSteps(
  drafts: OperationStepDraft[],
  operationType: OperationCreateInput["operationType"],
  datasets: Dataset[],
): OperationStepInput[] {
  if (drafts.length === 0 || drafts.length > 32) {
    throw new Error("Operation 必须包含 1–32 个步骤");
  }
  const keys = new Set<string>();
  return drafts.map((draft, index) => {
    const stepKey = draft.stepKey.trim();
    if (!stepKey || stepKey.length > 128 || keys.has(stepKey)) {
      throw new Error(`第 ${index + 1} 个步骤的 Step Key 为空、过长或重复`);
    }
    keys.add(stepKey);
    if (!operationActionMatchesType(operationType, draft.action)) {
      throw new Error(`${operationType} Operation 不允许动作 ${draft.action}`);
    }
    const payload = parsePayload(draft.payload, index);
    validateDatasets(
      payload,
      draft.source,
      draft.action,
      new Set(
        datasets
          .filter((dataset) => dataset.source === draft.source)
          .map((dataset) => dataset.name),
      ),
      index,
    );
    const compensationAction = draft.compensationAction.trim();
    if (
      compensationAction &&
      !operationActions.some((item) => item.value === compensationAction)
    ) {
      throw new Error(`第 ${index + 1} 个步骤的补偿动作不在 allow-list 中`);
    }
    return {
      stepKey,
      source: draft.source,
      action: draft.action,
      requestPayload: payload,
      compensationAction: compensationAction || undefined,
    };
  });
}
