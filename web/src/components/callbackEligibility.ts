import { capabilityAvailable } from "../api/capabilities";
import type { CallbackDelivery } from "../api/callbackDelivery";
import type { CapabilityReport } from "../types";
import {
  browserCommandContext,
  sameCapabilityContext,
} from "./commandEligibility";

// Guidance only: service/Snaplink still authorize. Durable replay admission also
// checks exact revision/generation/state and no owner or timestamp at the DB.
export function callbackReadAvailable(
  report: CapabilityReport | undefined,
  scope: string | undefined,
): boolean {
  return callbackFeatureAvailable(report, scope, "callback.read");
}
export function callbackActionAvailable(
  report: CapabilityReport | undefined,
  scope: string | undefined,
): boolean {
  return callbackFeatureAvailable(report, scope, "callback.manage");
}
function callbackFeatureAvailable(
  report: CapabilityReport | undefined,
  scope: string | undefined,
  name: string,
): boolean {
  return (
    sameCapabilityContext(report, scope) &&
    report?.capabilities.find((item) => item.name === name)
      ?.authorization_scope === "global" &&
    capabilityAvailable(report, [name])
  );
}
export function callbackReplayAvailable(
  delivery: CallbackDelivery | undefined,
  report: CapabilityReport | undefined,
  scope: string | undefined,
): boolean {
  const context = browserCommandContext(scope);
  return Boolean(
    delivery &&
    context &&
    callbackActionAvailable(report, scope) &&
    delivery.tenant_id === context.tenantID &&
    delivery.status === "dead_letter" &&
    Number.isSafeInteger(delivery.revision) &&
    delivery.revision > 0 &&
    delivery.lease_until === undefined,
  );
}
