import type { CallbackCreateInput } from "../api/callback";
import { callbackReadID } from "../api/callbackDelivery";
import type { JsonRecord } from "../types";
import { browserCommandContext } from "./commandEligibility";

// Verify the fields actually returned by registration/safe response replay.
// This is not durable intent binding or proof the subscription still exists.
export function callbackRegistrationMatches(
  subscription: JsonRecord,
  input: CallbackCreateInput,
  scope: string | undefined,
): boolean {
  if (typeof subscription.id !== "string") return false;
  try {
    callbackReadID(subscription.id);
  } catch {
    return false;
  }
  const context = browserCommandContext(scope);
  return Boolean(
    context &&
    subscription.tenant_id === context.tenantID &&
    subscription.endpoint === input.endpoint &&
    subscription.verified === true &&
    subscription.enabled === true &&
    sameEventTypes(subscription.event_types, input.eventTypes),
  );
}
function sameEventTypes(value: unknown, expected: string[]): boolean {
  if (
    !Array.isArray(value) ||
    value.length > 64 ||
    !value.every((item) => typeof item === "string")
  )
    return false;
  return (
    new Set(value).size === value.length &&
    JSON.stringify([...value].sort()) === JSON.stringify([...expected].sort())
  );
}
