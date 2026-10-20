import type { CapabilityReport, JsonRecord } from "../types";
import type { CommandPlan } from "../api/commandIntent";
import { capabilityAvailable } from "../api/capabilities";
import {
  browserCommandContext,
  sameCapabilityContext,
} from "./commandEligibility";
import { operationReceiptPlan } from "./taskActionEligibility";

const destructive = (action: string) =>
  ["erase", "delete", "source.erase"].includes(action);
function requiredFeatures(plan: CommandPlan) {
  const result = [{ name: "operation.manage", label: "操作管理" }];
  const roots: Record<string, string> = {
    sync: "source.sync",
    erase: "account.erase",
    replay: "audit.replay",
  };
  const root = roots[plan.operation_type ?? ""];
  if (root) result.push({ name: root, label: "原操作类型" });
  for (const step of plan.steps ?? []) {
    const actions: Array<[string | undefined, string]> = [
      [step.action, "正向动作"],
      [step.compensation_action, "补偿动作"],
    ];
    for (const [action, label] of actions)
      if (action)
        result.push({
          name: destructive(action) ? "account.erase" : "source.sync",
          label,
        });
  }
  return result;
}
function permissionIssue(
  plan: CommandPlan,
  report: CapabilityReport,
): string | undefined {
  const required = requiredFeatures(plan).find(
    (item) => !capabilityAvailable(report, [item.name]),
  );
  if (!required) return undefined;
  const feature = report.capabilities.find(
    (item) => item.name === required.name,
  );
  const state =
    feature?.authorization === "denied" ? "已拒绝" : "未确认或未启用";
  return `${required.label}所需资格 ${required.name} ${state}；运行 / 对账已暂停。`;
}
function datasetIssue(
  plan: CommandPlan,
  report: CapabilityReport,
): string | undefined {
  if (
    !plan.datasets.every((name) =>
      report.datasets.some((item) => item.name === name && item.registered),
    )
  )
    return "所选数据集未全部注册；不能运行或对账原操作。";
  const summaryDisabled = plan.steps?.some(
    (step) =>
      [step.action, step.compensation_action].some(
        (action) => action && !destructive(action),
      ) &&
      (step.request_payload?.datasets as string[]).some(
        (name) =>
          !report.datasets.some((item) => item.name === name && item.enabled),
      ),
  );
  return summaryDisabled
    ? "本步骤所选来源摘要未启用；运行 / 对账已暂停，注册不等于已接通。"
    : undefined;
}

// An explanation of the same local prerequisites; never used as an extra grant.
export function operationActionReason(
  value: JsonRecord,
  report: CapabilityReport | undefined,
  scope: string | undefined,
  available: boolean,
): string {
  if (!sameCapabilityContext(report, scope) || !report)
    return "当前账户 / 租户 / 客户端动作资格未确认；仅可核对原回执。";
  if (value.tenant_id !== browserCommandContext(scope)?.tenantID)
    return "原回执租户与当前会话不一致；不能运行或对账。";
  const plan = operationReceiptPlan(value);
  if (!plan)
    return "逐步骤明确范围或有序动作 / 补偿计划未确认；不借顶层请求或审计猜测，运行 / 对账已暂停。";
  if (value.home_region !== report.local_region)
    return "原操作主区域不是当前区域；请在正确主区域核对原回执，不能在此运行或对账。";
  const issue = permissionIssue(plan, report) ?? datasetIssue(plan, report);
  if (issue) return issue;
  if (!available) return "当前动作或原目标身份未确认；运行 / 对账已暂停。";
  return availableStateReason(value);
}
function availableStateReason(value: JsonRecord): string {
  if (
    ["requested", "waiting"].includes(value.status as string) &&
    value.completed_at == null
  )
    return "本页范围、动作 / 补偿提示与主区域匹配，可显式运行原 Operation；不会自动运行。";
  if (value.status === "unknown")
    return "原范围与当前动作提示已确认，可显式对账原来源结果；unknown 不授权重发原动作。";
  return "当前记录不是可运行或可对账状态；请核对步骤与审计，不创建替代命令。";
}
