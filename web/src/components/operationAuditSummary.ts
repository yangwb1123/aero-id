import type { IrisTimelineItem, IrisTimelineVariant } from "@iris-ui-kit/vue";
import type { JsonRecord } from "../types";
import {
  jobCount,
  jobDatasetLabels,
  jobDatasets,
  jobRecord,
  jobText,
  jobTime,
} from "./jobSummaryFields";
import {
  operationAction,
  operationCode,
  operationSource,
  operationState,
  operationStepState,
  operationSteps,
} from "./operationResultSummary";
const labels: Record<string, string> = {
  "operation.requested": "原操作已创建",
  "operation.started": "原操作开始处理",
  "operation.step.finished": "步骤结果已记录",
  "operation.step.expired": "正向尝试租约到期（结果未知）",
  "operation.compensating": "原操作进入补偿阶段",
  "operation.compensation.finished": "补偿结果已记录",
  "operation.compensation.reconciled": "补偿对账结果已记录",
  "operation.compensation.expired": "补偿尝试租约到期（结果未知）",
  "operation.compensation.deferred": "补偿已延后，原结果保留",
  "operation.execution.blocked": "继续执行曾被阻断（不是当前授权结论）",
  "operation.reconciled": "原结果对账已记录",
  "operation.finished": "操作状态已记录",
};
function datasetText(value: unknown): string {
  const datasets = jobDatasets(value);
  return datasets?.length
    ? datasets.map((name) => jobDatasetLabels[name]).join("、")
    : "未提供可确认范围";
}
function requestedSteps(value: unknown): string {
  const steps = operationSteps(value);
  if (steps.issue) return "创建事实未提供可确认步骤范围";
  return steps.items
    .map(
      (step) =>
        `${jobText(step.step_key)} / ${operationSource(step.source)} / ${operationAction(step.action)} / ${datasetText(step.datasets)}`,
    )
    .join("；");
}
function detail(action: string, payload: JsonRecord): string[] {
  const fields: string[] = [];
  if (action === "operation.requested")
    fields.push(requestedSteps(payload.steps));
  if (payload.step_key !== undefined)
    fields.push(`步骤：${jobText(payload.step_key)}`);
  else if (payload.step_id !== undefined)
    fields.push(`步骤 ID：${jobText(payload.step_id)}`);
  if (payload.status !== undefined) {
    const state =
      action === "operation.finished" || action === "operation.reconciled"
        ? operationState(payload.status)
        : operationStepState(payload.status);
    fields.push(`记录状态：${state.title}`);
  }
  if (payload.attempt !== undefined)
    fields.push(
      `${action.startsWith("operation.compensation.") ? "补偿" : "正向"}尝试：${jobCount(payload.attempt)}`,
    );
  if (payload.compensation_attempt !== undefined)
    fields.push(`补偿尝试：${jobCount(payload.compensation_attempt)}`);
  if (payload.datasets !== undefined)
    fields.push(`记录范围：${datasetText(payload.datasets)}`);
  if (payload.source_operation_id !== undefined)
    fields.push(`来源操作 ID：${jobText(payload.source_operation_id)}`);
  const code = operationCode(payload.error_code);
  if (code) fields.push(`结果码：${code}`);
  return fields;
}
function variant(action: string, payload: JsonRecord): IrisTimelineVariant {
  if (action === "operation.execution.blocked") return "warning";
  if (payload.status === "failed") return "danger";
  if (
    payload.status === "unknown" ||
    action.includes("compensation") ||
    action.includes("expired")
  )
    return "warning";
  if (payload.status === "succeeded" || payload.status === "completed")
    return "success";
  return "info";
}
export function operationAuditItem(
  event: JsonRecord,
  index: number,
): IrisTimelineItem {
  const action = jobText(event.action);
  const known = Object.hasOwn(labels, action);
  const payload = jobRecord(event.payload) ?? {};
  return {
    key: typeof event.event_id === "string" ? event.event_id : `fact-${index}`,
    title: action,
    time: jobTime(event.occurred_at ?? event.created_at),
    description: [
      known ? labels[action] : "本地审计事实（未映射业务语义）",
      `事实来源：${jobText(event.source)}`,
      ...(event.resource_id === undefined
        ? []
        : [
            `资源：${jobText(event.resource_type)} / ${jobText(event.resource_id)}`,
          ]),
      ...(known ? detail(action, payload) : []),
    ].join(" · "),
    variant: known ? variant(action, payload) : "info",
  };
}
