import type { JsonRecord, PageData } from "../types";
import { AeroIdError } from "./transport";

export type CallbackDeliveryStatus =
  "pending" | "delivering" | "success" | "dead_letter" | "unknown";
export type CallbackDeliveryStatusFilter = Exclude<
  CallbackDeliveryStatus,
  "unknown"
>;
export interface CallbackDelivery extends JsonRecord {
  id: string;
  subscription_id: string;
  event_id: string;
  tenant_id: string;
  action: string;
  status: CallbackDeliveryStatus;
  attempt: number;
  revision: number;
  lease_generation: number;
  lease_until?: string;
  next_attempt_at: string;
  status_code: number;
  error_code: string;
  created_at: string;
  updated_at: string;
}

const fields = new Set([
  "id",
  "subscription_id",
  "event_id",
  "tenant_id",
  "action",
  "status",
  "attempt",
  "revision",
  "lease_generation",
  "lease_until",
  "next_attempt_at",
  "status_code",
  "error_code",
  "created_at",
  "updated_at",
]);
const statuses = ["pending", "delivering", "success", "dead_letter", "unknown"];
const errors = [
  "",
  "callback.delivery_failed",
  "callback.secret_unavailable",
  "manual_replay",
  "callback.error_unknown",
];
const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
function validID(value: unknown): value is string {
  return (
    typeof value === "string" &&
    uuid.test(value) &&
    !/^0{8}-0{4}-0{4}-0{4}-0{12}$/.test(value)
  );
}
export function callbackReadID(value: string): string {
  if (!validID(value)) throw invalidDelivery();
  return value.toLowerCase();
}
function validDate(value: unknown): value is string {
  return (
    typeof value === "string" &&
    value.length <= 40 &&
    value.includes("T") &&
    Number.isFinite(Date.parse(value))
  );
}
function integer(value: unknown): value is number {
  return typeof value === "number" && Number.isSafeInteger(value) && value >= 0;
}
function record(value: unknown): value is JsonRecord {
  return Boolean(value) && typeof value === "object" && !Array.isArray(value);
}
function invalidDelivery(): AeroIdError {
  return new AeroIdError(
    0,
    "response.invalid",
    "Callback 投递响应无效；不会推断重放或投递成功",
  );
}
function validDeliveryIdentity(value: JsonRecord): boolean {
  return (
    [value.id, value.subscription_id, value.event_id].every(
      (id) => validID(id) && id === id.toLowerCase(),
    ) &&
    typeof value.tenant_id === "string" &&
    value.tenant_id.trim().length > 0 &&
    value.tenant_id.length <= 512 &&
    typeof value.action === "string" &&
    /^[a-z0-9._:-]{1,128}$/.test(value.action)
  );
}
function validDeliveryOutcome(value: JsonRecord): boolean {
  return (
    typeof value.status === "string" &&
    statuses.includes(value.status) &&
    typeof value.error_code === "string" &&
    errors.includes(value.error_code) &&
    integer(value.revision) &&
    value.revision > 0 &&
    [value.attempt, value.lease_generation].every(integer) &&
    integer(value.status_code) &&
    (value.status_code === 0 ||
      (value.status_code >= 100 && value.status_code <= 599))
  );
}
function validDeliveryFields(value: JsonRecord): boolean {
  return (
    Object.keys(value).every((key) => fields.has(key)) &&
    validDeliveryIdentity(value) &&
    validDeliveryOutcome(value) &&
    [value.next_attempt_at, value.created_at, value.updated_at].every(
      validDate,
    ) &&
    (value.lease_until === undefined || validDate(value.lease_until))
  );
}
export function parseCallbackDelivery(
  value: unknown,
  subscriptionID: string,
  deliveryID?: string,
): CallbackDelivery {
  if (!record(value) || !validDeliveryFields(value)) throw invalidDelivery();
  if (
    value.subscription_id !== callbackReadID(subscriptionID) ||
    (deliveryID && value.id !== callbackReadID(deliveryID))
  )
    throw invalidDelivery();
  return value as CallbackDelivery;
}
export function parseCallbackDeliveryPage(
  data: JsonRecord,
  subscriptionID: string,
  status?: CallbackDeliveryStatusFilter,
  cursor?: string,
): PageData<CallbackDelivery> {
  if (
    !Array.isArray(data.deliveries) ||
    data.deliveries.length > 100 ||
    typeof data.next_cursor !== "string"
  )
    throw invalidDelivery();
  const items = data.deliveries.map((item) =>
    parseCallbackDelivery(item, subscriptionID),
  );
  let previous = cursor ? callbackReadID(cursor) : undefined;
  for (const item of items) {
    if (
      (previous && item.id >= previous) ||
      (status && item.status !== status) ||
      item.tenant_id !== items[0].tenant_id
    )
      throw invalidDelivery();
    previous = item.id;
  }
  if (!validNextCursor(data.next_cursor, previous, items.length))
    throw invalidDelivery();
  return { items, nextCursor: data.next_cursor || undefined };
}
function validNextCursor(
  next: string,
  previous: string | undefined,
  size: number,
): boolean {
  return next === "" || (validID(next) && next === previous && size > 0);
}
