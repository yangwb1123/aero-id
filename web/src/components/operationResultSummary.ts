import type { JsonRecord } from "../types";
import {
  jobCount,
  jobDatasetLabels,
  jobDatasets,
  jobFlag,
  jobRecord,
  jobSourceLabels,
  jobText,
} from "./jobSummaryFields";

type Tone = "info" | "warning" | "success" | "danger";
export interface OperationState {
  title: string;
  detail: string;
  tone: Tone;
}
const states: Record<string, OperationState> = {
  requested: {
    title: "等待运行",
    tone: "info",
    detail:
      "已创建原回执。可用时显式运行原 Operation；本页不会自动运行，也不从等待状态推断远端未执行。",
  },
  waiting: {
    title: "等待继续",
    tone: "info",
    detail:
      "仍有步骤待处理。先刷新原状态并核对审计；允许时才显式继续原 Operation，不新建替代命令。",
  },
  running: {
    title: "正在处理",
    tone: "info",
    detail:
      "查看已记录的逐步骤结果，等待或刷新原状态；尝试次数不是进度百分比。",
  },
  compensating: {
    title: "正在补偿",
    tone: "warning",
    detail:
      "请核对各步骤的补偿记录；补偿是独立来源动作，不代表整体回滚。不要另建反向命令。",
  },
  unknown: {
    title: "结果未知",
    tone: "warning",
    detail:
      "保留原 Operation、来源操作 ID 与原键。先刷新并核对审计；资格可确认时才显式对账，不重发原动作，不推断失败或回滚。",
  },
  partially_completed: {
    title: "部分完成",
    tone: "warning",
    detail:
      "请逐项核对已记录的来源效果、缺失范围及补偿；不自动补发失败或未知动作。",
  },
  completed: {
    title: "已完成",
    tone: "success",
    detail:
      "这是原 Operation 的服务端状态，不证明当前投影新鲜、来源实时完整或跨源同一时点。请继续核对逐步骤结果与审计。",
  },
  failed: {
    title: "操作失败",
    tone: "danger",
    detail:
      "请核对失败步骤及补偿；失败不代表此前来源效果已撤回，不自动重试或创建替代命令。",
  },
};
const stepStates: Record<string, OperationState> = {
  pending: {
    title: "等待处理",
    tone: "info",
    detail: "等待是记录状态，不证明此前没有来源效果。",
  },
  running: {
    title: "处理中",
    tone: "info",
    detail: "结果尚未确认，请查看原回执。",
  },
  unknown: {
    title: "步骤结果未知",
    tone: "warning",
    detail: "只能核对原来源操作，不当作失败重发。",
  },
  succeeded: {
    title: "步骤成功",
    tone: "success",
    detail: "服务端已记录成功；摘要完整性和投影应用结果仍需单独核对。",
  },
  failed: {
    title: "步骤失败",
    tone: "danger",
    detail: "核对结果码与审计；不推断此前效果已撤回。",
  },
  skipped: {
    title: "已跳过",
    tone: "warning",
    detail: "服务端记录为跳过，不据此推断此前来源效果。",
  },
};
export function operationState(value: unknown): OperationState {
  return typeof value === "string" && Object.hasOwn(states, value)
    ? states[value]
    : {
        title: "状态未确认",
        tone: "warning",
        detail: "请读取原回执核实，不推断完成或失败。",
      };
}
export function operationStepState(value: unknown): OperationState {
  return typeof value === "string" && Object.hasOwn(stepStates, value)
    ? stepStates[value]
    : {
        title: "步骤状态未报告或无效",
        tone: "warning",
        detail: "不能据此判断未执行。",
      };
}
export function operationType(value: unknown): string {
  const labels: Record<string, string> = {
    sync: "来源同步",
    erase: "来源擦除",
    "source.command": "来源操作",
    replay: "重放操作",
  };
  return typeof value === "string" && Object.hasOwn(labels, value)
    ? labels[value]
    : "Operation";
}
export function operationAction(value: unknown): string {
  if (["sync", "summary", "source.summary"].includes(value as string))
    return "同步来源摘要";
  if (["erase", "delete", "source.erase"].includes(value as string))
    return "擦除所选来源数据（危险）";
  return value == null || value === ""
    ? "来源未提供"
    : "动作字段无效（未展示）";
}
export function operationSource(value: unknown): string {
  return typeof value === "string" && Object.hasOwn(jobSourceLabels, value)
    ? jobSourceLabels[value]
    : "来源未提供或无效";
}
export function operationCode(value: unknown): string | undefined {
  return typeof value === "string" && /^[a-z][a-z0-9_.-]{0,127}$/.test(value)
    ? value
    : undefined;
}
export function operationSteps(value: unknown) {
  if (
    !Array.isArray(value) ||
    value.length > 32 ||
    !value.every((step) => jobRecord(step))
  )
    return {
      items: [] as JsonRecord[],
      issue:
        "步骤记录无法安全展示；不能据此判断未执行。请刷新原回执并核对审计。",
    };
  return { items: value as JsonRecord[], issue: undefined };
}
export function compensationState(step: JsonRecord): OperationState {
  const value = step.compensation_status;
  if (value == null || value === "")
    return {
      title: "未报告补偿状态",
      tone: "info",
      detail: "计划不代表已执行；缺少状态不能判断补偿是否发生或是否需要。",
    };
  if (value === "unknown")
    return {
      title: "补偿结果未知",
      tone: "warning",
      detail: "保留原补偿来源操作 ID，等待原结果核实；不重复补偿。",
    };
  const state = operationStepState(value);
  return {
    ...state,
    title: `补偿 · ${state.title}`,
    detail: "补偿记录与正向结果分开核对；单步补偿成功也不代表整体回滚。",
  };
}
const resultStatuses: Record<string, string> = {
  synced: "已报告来源摘要",
  partially_completed: "来源报告部分完成",
  partial: "来源报告部分完成",
  succeeded: "来源报告成功",
  success: "来源报告成功",
  completed: "来源报告完成",
  complete: "来源报告完成",
  done: "来源报告完成",
  failed: "来源报告失败",
  failure: "来源报告失败",
  rejected: "来源报告拒绝",
  cancelled: "来源报告取消",
  canceled: "来源报告取消",
  pending: "来源报告等待",
  running: "来源报告处理中",
  accepted: "来源已受理（不等于完成）",
  unknown: "来源结果未知",
};
const applicationOutcomes: Record<string, string> = {
  applied: "已记录摘要应用（不保证每条投影更新）",
  stale_ignored: "旧版本摘要已忽略（未据此覆盖新投影）",
  reconciled: "已记录投影对账",
  incomplete: "摘要不完整，未完整应用",
};
function mappedField(value: unknown, labels: Record<string, string>): string {
  if (value == null) return "来源未提供";
  return typeof value === "string" && Object.hasOwn(labels, value)
    ? labels[value]
    : "字段无效（未展示）";
}
function skippedScope(result: JsonRecord, source: unknown): string | undefined {
  if (result.skipped_datasets == null) return undefined;
  const datasets = jobDatasets(result.skipped_datasets);
  if (
    !datasets ||
    typeof source !== "string" ||
    !datasets.every((name) => name.startsWith(`${source}.`))
  )
    return "跳过范围字段无效（未展示）";
  return datasets.length
    ? datasets.map((name) => jobDatasetLabels[name]).join("、")
    : "本次未报告跳过数据集";
}
export function operationResult(step: JsonRecord, compensation = false) {
  const raw = step[compensation ? "compensation_result" : "result"];
  const result = jobRecord(raw);
  const action = compensation ? step.compensation_action : step.action;
  const fields: Array<{ label: string; value: string }> = [];
  if (!result)
    return {
      fields,
      note:
        raw == null
          ? "未报告结果摘要；不能据此判断未执行。"
          : "结果摘要字段无效（未展示）。",
      incomplete: false,
    };
  if (Object.hasOwn(result, "status"))
    fields.push({
      label: "来源结果标记",
      value: mappedField(result.status, resultStatuses),
    });
  const summary = ["sync", "summary", "source.summary"].includes(
    action as string,
  );
  if (summary) {
    fields.push({ label: "摘要投影条数", value: jobCount(result.count) });
    fields.push({
      label: "摘要完整性",
      value: jobFlag(
        result.complete,
        "所请求摘要已报告完整",
        "所请求摘要不完整",
      ),
    });
    fields.push({
      label: "本地应用结果",
      value: mappedField(result.application_outcome, applicationOutcomes),
    });
    const skipped = skippedScope(result, step.source);
    if (skipped) fields.push({ label: "来源报告跳过范围", value: skipped });
  }
  const error = operationCode(result.error_code);
  if (error) fields.push({ label: "结果码", value: error });
  return {
    fields,
    note: summary
      ? "条数是本次摘要投影数，不是工作空间、桶或对象总数；不证明当前投影新鲜。"
      : "只展示已确认结果字段，不展开来源载荷。",
    incomplete:
      summary &&
      (result.complete === false ||
        result.status === "partially_completed" ||
        result.status === "partial"),
  };
}
export function operationStepKey(step: JsonRecord, index: number): string {
  const key = jobText(step.step_key);
  return key === "来源未提供" ? `未提供步骤标识 · ${index + 1}` : key;
}
