import type { CapabilityReport, JsonRecord } from "../types";
import type { CommandPlan, CommandPlanStep } from "../api/commandIntent";
import { capabilityAvailable } from "../api/capabilities";
import { isTaskStatus } from "../api/taskStatus";
import {
  browserCommandContext,
  commandAvailable,
  sameCapabilityContext,
} from "./commandEligibility";
import { commandUUID } from "./commandPreviewStorage";
import { operationStepScope } from "./operationStepScope";
const record = (value: unknown): value is JsonRecord =>
  Boolean(value) && typeof value === "object" && !Array.isArray(value);
const actions = [
  "sync",
  "summary",
  "source.summary",
  "erase",
  "delete",
  "source.erase",
];

export function operationReceiptPlan(
  value: JsonRecord | undefined,
): CommandPlan | undefined {
  if (
    !value ||
    typeof value.operation_type !== "string" ||
    !["sync", "erase", "source.command", "replay"].includes(
      value.operation_type,
    ) ||
    !Array.isArray(value.steps) ||
    !value.steps.length ||
    value.steps.length > 32 ||
    !value.steps.every(validStep)
  )
    return undefined;
  const steps = (value.steps as JsonRecord[]).map((step): CommandPlanStep => ({
    step_key: step.step_key as string,
    source: step.source as CommandPlanStep["source"],
    action: step.action as string,
    ...(step.compensation_action
      ? { compensation_action: step.compensation_action as string }
      : {}),
    request_payload: { datasets: operationStepScope(step).datasets! },
  }));
  if (
    new Set(steps.map((step) => step.step_key)).size !== steps.length ||
    steps.some(
      (step) =>
        (value.operation_type === "sync" &&
          ["erase", "delete", "source.erase"].includes(step.action)) ||
        (value.operation_type === "erase" &&
          !["erase", "delete", "source.erase"].includes(step.action)),
    )
  )
    return undefined;
  const datasets = [
    ...new Set(
      steps.flatMap((step) => step.request_payload!.datasets as string[]),
    ),
  ].sort();
  return {
    kind: "operation",
    operation_type: value.operation_type as CommandPlan["operation_type"],
    datasets,
    steps,
  };
}
function validStep(value: unknown): boolean {
  if (
    !record(value) ||
    typeof value.step_key !== "string" ||
    !value.step_key ||
    value.step_key.length > 128 ||
    !["snaplink", "aero-im", "aero-vault"].includes(String(value.source)) ||
    typeof value.action !== "string" ||
    !actions.includes(value.action)
  )
    return false;
  if (
    value.compensation_action != null &&
    value.compensation_action !== "" &&
    (typeof value.compensation_action !== "string" ||
      !actions.includes(value.compensation_action))
  )
    return false;
  return Boolean(operationStepScope(value).datasets);
}
function validDatasets(value: unknown): value is string[] {
  return (
    Array.isArray(value) &&
    value.length > 0 &&
    value.length <= 11 &&
    value.every(
      (name) => typeof name === "string" && /^[a-z-]+\.[a-z_]+$/.test(name),
    ) &&
    new Set(value).size === value.length
  );
}
function sameTaskScope(
  value: JsonRecord | undefined,
  id: string,
  report: CapabilityReport | undefined,
  scope: string | undefined,
): boolean {
  return Boolean(
    value &&
    commandUUID(id) &&
    (value.operation_id ?? value.job_id ?? value.id) === id &&
    commandUUID(value.account_id) &&
    typeof value.actor_uid === "string" &&
    value.actor_uid.length > 0 &&
    value.actor_uid.length <= 512 &&
    value.tenant_id === browserCommandContext(scope)?.tenantID &&
    sameCapabilityContext(report, scope),
  );
}
export function operationActionAvailable(
  value: JsonRecord | undefined,
  id: string,
  report: CapabilityReport | undefined,
  scope: string | undefined,
): boolean {
  const plan = operationReceiptPlan(value);
  return Boolean(
    value &&
    plan &&
    report &&
    sameTaskScope(value, id, report, scope) &&
    value.home_region === report.local_region &&
    commandAvailable(report, plan, String(value.account_id)),
  );
}
export function jobReconcileAvailable(
  value: JsonRecord | undefined,
  id: string,
  report: CapabilityReport | undefined,
  scope: string | undefined,
): boolean {
  if (
    !value ||
    !report ||
    !sameTaskScope(value, id, report, scope) ||
    !record(value.request) ||
    !validDatasets(value.request.datasets)
  )
    return false;
  const features: Record<string, string> = {
    sync: "source.sync",
    reconcile: "source.sync",
    erase: "account.erase",
    export: "account.export",
    audit_export: "audit.export",
  };
  const feature = features[String(value.job_type)];
  return Boolean(
    feature &&
    capabilityAvailable(report, [feature]) &&
    (value.job_type !== "audit_export" ||
      capabilityAvailable(report, ["audit.read"])) &&
    (value.account_id !== report?.account_id ||
      report.home_region === report.local_region),
  );
}
export function jobDownloadAvailable(
  value: JsonRecord | undefined,
  id: string,
  report: CapabilityReport | undefined,
  scope: string | undefined,
): boolean {
  if (!value || !sameTaskScope(value, id, report, scope)) return false;
  // Downloads require the global export grant even for the receipt owner.
  return (
    (value.job_type === "export" &&
      capabilityAvailable(report, ["account.export"])) ||
    (value.job_type === "audit_export" &&
      capabilityAvailable(report, ["audit.export"]) &&
      capabilityAvailable(report, ["audit.read"]))
  );
}
export function matchesOriginalTask(
  result: JsonRecord,
  original: JsonRecord | undefined,
  id: string,
  field: "job_id" | "operation_id",
): boolean {
  return Boolean(
    original &&
    id &&
    original[field] === id &&
    isTaskStatus(result.status, field) &&
    result[field] === id &&
    result.account_id === original.account_id &&
    result.tenant_id === original.tenant_id &&
    result.actor_uid === original.actor_uid &&
    result.operation_type === original.operation_type &&
    result.job_type === original.job_type,
  );
}
