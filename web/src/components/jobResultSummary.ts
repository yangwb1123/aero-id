import type { JsonRecord } from "../types";
import {
  jobCount,
  jobDatasetLabels,
  jobDatasets,
  jobFlag,
  jobRecord,
  jobScope,
  jobSourceLabels,
  jobText,
} from "./jobSummaryFields";

type Tone = "info" | "success" | "warning" | "danger";
const statuses: Record<string, { title: string; tone: Tone; detail: string }> =
  {
    pending: {
      title: "等待处理",
      tone: "info",
      detail: "任务回执已创建，尚未报告完成。排队不代表来源已完成。",
    },
    running: {
      title: "处理中",
      tone: "info",
      detail: "后台正在处理原任务；接口未提供进度百分比。",
    },
    completed: {
      title: "已完成",
      tone: "success",
      detail:
        "任务已报告完成；数据完整性、来源结果和制品是否可下载仍需分别查看。",
    },
    partially_completed: {
      title: "部分完成",
      tone: "warning",
      detail: "部分范围未完成或未完整提供；已有结果不代表整个所选范围完成。",
    },
    failed: {
      title: "任务失败",
      tone: "danger",
      detail: "任务报告失败不代表此前来源效果已回滚；先核对原结果与审计。",
    },
    unknown: {
      title: "结果未知",
      tone: "warning",
      detail:
        "结果未知不是失败，也不证明未执行。保留原回执，先刷新原状态，必要时显式对账。",
    },
  };
const types: Record<string, { title: string; detail: string }> = {
  sync: {
    title: "账户同步",
    detail: "读取所选来源摘要并应用到账户的本地投影，不复制对象或完整消息。",
  },
  reconcile: {
    title: "投影对账",
    detail:
      "按原范围核对摘要与本地投影；漂移和修复数是投影条数，不是对象或消息总数。",
  },
  export: {
    title: "账户投影导出",
    detail:
      "导出核心账户/资料及所选本地投影与引用，不是 Vault 文件或完整 IM 消息导出。",
  },
  erase: {
    title: "账户擦除",
    detail:
      "查看原范围各来源的擦除结果；不能据本地完成状态宣称跨区、Legal Hold 或所有来源删除已验收。",
  },
  audit_export: {
    title: "审计导出",
    detail:
      "导出当前租户的有界审计窗口；任务成功不等于完整审计历史或归档验证通过。",
  },
};
export function jobOutcome(job: JsonRecord) {
  const status =
    typeof job.status === "string" && Object.hasOwn(statuses, job.status)
      ? statuses[job.status]
      : undefined;
  const kind =
    typeof job.job_type === "string" && Object.hasOwn(types, job.job_type)
      ? types[job.job_type]
      : undefined;
  return {
    status: status ?? {
      title: "未识别状态",
      tone: "warning" as Tone,
      detail: "请核实原任务，不能据此判断完成或重试。",
    },
    kind: kind ?? {
      title: "未识别任务类型",
      detail: "仅显示已确认的元数据，不推断来源动作。",
    },
  };
}
const sourceStatuses: Record<string, string> = {
  synced: "已应用摘要",
  partial: "已应用部分摘要",
  reconciled: "已核实原摘要回执",
  not_configured: "来源未接通",
  unavailable: "来源不可用",
  projection_read_failed: "投影读取失败",
  projection_error: "投影写入失败",
  invalid_request: "来源请求未通过校验",
  invalid_response: "来源回应无效",
  not_started: "本次来源调用未开始",
  erased: "账户已擦除，摘要未应用",
  succeeded: "来源报告擦除成功",
  failed: "来源报告失败",
  unknown: "来源结果未知",
  pending: "来源结果待处理",
};
const applyOutcomes: Record<string, string> = {
  applied: "本地摘要已应用",
  stale_ignored: "旧版本已忽略",
  reconciled: "本地摘要已对账",
  incomplete: "本地摘要应用不完整",
};
function code(value: unknown): string | undefined {
  return typeof value === "string" && /^[a-z][a-z0-9_.]{0,127}$/.test(value)
    ? value
    : undefined;
}
function sourceTitle(entry: JsonRecord | undefined, kind: unknown): string {
  if (!entry) return "未报告来源结果";
  const status = entry.status;
  if (typeof status !== "string" || !Object.hasOwn(sourceStatuses, status))
    return "未识别的来源状态";
  if (
    kind === "erase" &&
    !["succeeded", "failed", "unknown", "pending"].includes(status)
  )
    return "未识别的来源状态";
  if (kind !== "erase" && status === "succeeded") return "未识别的来源状态";
  return sourceStatuses[status];
}
function summaryFields(entry: JsonRecord | undefined, kind: unknown) {
  if (!entry || (kind !== "sync" && kind !== "reconcile")) return {};
  const outcome =
    typeof entry.outcome === "string" &&
    Object.hasOwn(applyOutcomes, entry.outcome)
      ? applyOutcomes[entry.outcome]
      : undefined;
  return {
    count: jobCount(entry.count),
    complete:
      entry.complete === undefined
        ? undefined
        : jobFlag(entry.complete, "来源报告摘要完整", "来源报告摘要不完整"),
    drift: kind === "reconcile" ? jobCount(entry.drift_count) : undefined,
    repaired: kind === "reconcile" ? jobCount(entry.repaired_count) : undefined,
    outcome,
  };
}
function skippedFields(entry: JsonRecord | undefined, source: string) {
  if (entry?.skipped_datasets === undefined) return {};
  const skipped = jobDatasets(entry.skipped_datasets);
  if (!skipped || skipped.some((name) => !name.startsWith(`${source}.`)))
    return { skippedInvalid: true };
  return { skipped: skipped.map((name) => jobDatasetLabels[name]).join("、") };
}
function sourceEntry(
  source: string,
  datasets: string[],
  raw: unknown,
  kind: unknown,
) {
  const entry = jobRecord(raw);
  return {
    source,
    label: jobSourceLabels[source],
    datasets: datasets.map((name) => jobDatasetLabels[name]),
    title: sourceTitle(entry, kind),
    missing: !entry,
    ...summaryFields(entry, kind),
    ...skippedFields(entry, source),
    operationID:
      kind === "erase" && entry?.source_operation_id !== undefined
        ? jobText(entry.source_operation_id)
        : undefined,
    errorCode: code(entry?.error_code),
  };
}
export function jobSourceResults(job: JsonRecord) {
  const { datasets, issue } = jobScope(job);
  if (issue) return [];
  const sources = jobRecord(jobRecord(job.result)?.sources);
  return Object.keys(jobSourceLabels).flatMap((source) => {
    const requested = datasets.filter((name) => name.startsWith(`${source}.`));
    return requested.length
      ? [sourceEntry(source, requested, sources?.[source], job.job_type)]
      : [];
  });
}
