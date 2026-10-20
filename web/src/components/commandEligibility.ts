import type { CommandIntent, CommandPlan } from "../api/commandIntent";
import { capabilityAvailable } from "../api/capabilities";
import type { CapabilityReport } from "../types";
import { commandUUID } from "./commandPreviewStorage";

export function browserCommandContext(
  scope: string | undefined,
):
  | { issuer: string; clientID: string; tenantID: string; accountID: string }
  | undefined {
  if (!scope) return undefined;
  try {
    const parts: unknown = JSON.parse(scope);
    if (
      !Array.isArray(parts) ||
      parts.length !== 5 ||
      !parts.every((part) => typeof part === "string" && part.length > 0) ||
      !commandUUID(parts[4])
    )
      return undefined;
    return {
      issuer: parts[1],
      clientID: parts[2],
      tenantID: parts[3],
      accountID: parts[4],
    };
  } catch {
    return undefined;
  }
}
export function sameCommandCaller(
  intent: CommandIntent,
  scope: string | undefined,
): boolean {
  const context = browserCommandContext(scope);
  return Boolean(
    context &&
    intent.issuer === context.issuer &&
    intent.client_id === context.clientID &&
    intent.tenant_id === context.tenantID,
  );
}
export function sameCapabilityContext(
  report: CapabilityReport | undefined,
  scope: string | undefined,
): boolean {
  const context = browserCommandContext(scope);
  return Boolean(
    context &&
    report &&
    report.issuer === context.issuer &&
    report.client_id === context.clientID &&
    report.tenant_id === context.tenantID &&
    report.account_id === context.accountID,
  );
}
const destructive = (action?: string): boolean =>
  ["erase", "delete", "source.erase"].includes(action ?? "");

// Guidance only. Snaplink and the command service still reauthorize; dataset
// enabled is summary enablement, not an erase flag or source resource ACL.
export function commandAvailable(
  report: CapabilityReport | undefined,
  plan: CommandPlan,
  accountID: string,
): boolean {
  if (!report || !commandUUID(accountID)) return false;
  if (plan.kind === "callback_replay")
    return callbackCommandAvailable(report, plan, accountID);
  if (!plan.datasets.length) return false;
  if (
    !plan.datasets.every((name) =>
      report.datasets.some((item) => item.name === name && item.registered),
    )
  )
    return false;
  if (plan.kind !== "operation") {
    const feature = {
      sync: "source.sync",
      export: "account.export",
      erase: "account.erase",
    }[plan.kind];
    if (
      !capabilityAvailable(report, [feature]) ||
      (accountID === report.account_id &&
        !capabilityAvailable(report, [`account.self.${plan.kind}`]))
    )
      return false;
    return (
      plan.kind !== "sync" || summaryDatasetsEnabled(report, plan.datasets)
    );
  }
  return operationAvailable(report, plan);
}
function callbackCommandAvailable(
  report: CapabilityReport,
  plan: CommandPlan,
  accountID: string,
): boolean {
  const feature = report.capabilities.find(
    (item) => item.name === "callback.manage",
  );
  return (
    accountID === report.account_id &&
    plan.datasets.length === 0 &&
    Boolean(plan.callback) &&
    capabilityAvailable(report, ["callback.manage"]) &&
    feature?.authorization_scope === "global"
  );
}
function operationAvailable(
  report: CapabilityReport,
  plan: CommandPlan,
): boolean {
  if (!capabilityAvailable(report, ["operation.manage"]) || !plan.steps?.length)
    return false;
  if (
    plan.operation_type === "replay" &&
    !capabilityAvailable(report, ["audit.replay"])
  )
    return false;
  if (
    plan.operation_type === "sync" &&
    !capabilityAvailable(report, ["source.sync"])
  )
    return false;
  if (
    plan.operation_type === "erase" &&
    !capabilityAvailable(report, ["account.erase"])
  )
    return false;
  return plan.steps.every((step) =>
    [step.action, step.compensation_action].every((action) => {
      if (!action) return true;
      if (destructive(action))
        return capabilityAvailable(report, ["account.erase"]);
      const datasets = step.request_payload?.datasets;
      return (
        capabilityAvailable(report, ["source.sync"]) &&
        Array.isArray(datasets) &&
        datasets.every((name) => typeof name === "string") &&
        summaryDatasetsEnabled(report, datasets)
      );
    }),
  );
}
function summaryDatasetsEnabled(
  report: CapabilityReport,
  datasets: string[],
): boolean {
  return (
    datasets.length > 0 &&
    datasets.every((name) =>
      report.datasets.some(
        (item) => item.name === name && item.registered && item.enabled,
      ),
    )
  );
}
