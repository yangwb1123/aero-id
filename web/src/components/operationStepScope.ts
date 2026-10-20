import type { JsonRecord } from "../types";
import { jobDatasets, jobRecord, jobSourceLabels } from "./jobSummaryFields";

// HTTP read metadata only. Never borrow scope from a raw request, root payload,
// historical audit, result or Protobuf, and never expand an omitted selection.
export function operationStepScope(step: JsonRecord): {
  datasets?: string[];
  issue?: string;
} {
  const raw = step.dataset_scope;
  const scope = jobRecord(raw);
  if (
    raw == null ||
    (scope?.status === "missing" &&
      Array.isArray(scope.datasets) &&
      scope.datasets.length === 0)
  )
    return { issue: "本步骤未记录明确范围；不推断为全部数据集。" };
  const datasets = jobDatasets(scope?.datasets);
  if (
    scope?.status !== "explicit" ||
    !datasets?.length ||
    typeof step.source !== "string" ||
    !Object.hasOwn(jobSourceLabels, step.source) ||
    !datasets.every((name) => name.startsWith(`${step.source}.`))
  )
    return {
      issue: "本步骤范围无效或无法确认；不展示未批准名称，不运行或对账。",
    };
  return { datasets: [...datasets] };
}
