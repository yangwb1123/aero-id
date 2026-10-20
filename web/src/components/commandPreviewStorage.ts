import {
  immutableCommandMetadata,
  type CommandIntent,
  type CommandPlan,
} from "../api/commandIntent";
import { canonicalCallbackReplayPlan } from "./callbackReplayPlan";

// Recovery metadata only: no plan payload, token, confirmation input or source
// content. A known ID is recovered with GET; a missing ID can be looked up with
// its original key. NotFound never proves that an older prepare cannot commit.
// Retained local closure flags need live GET proof before any replacement key.
export interface StoredCommandPreview {
  channel: string;
  accountID: string;
  kind: CommandPlan["kind"];
  signature: string;
  idempotencyKey: string;
  intentID?: string;
  metadataSignature?: string;
  receiptID?: string;
  abandoned?: true;
  // Explicitly acknowledged local callback queue receipt, not receiver success.
  acknowledged?: true;
}
export const maxCommandPreviews = 16;
export const commandUUID = (value: unknown): value is string =>
  typeof value === "string" &&
  /^[\da-f]{8}-(?:[\da-f]{4}-){3}[\da-f]{12}$/i.test(value) &&
  !/^0{8}-0{4}-0{4}-0{4}-0{12}$/.test(value);

export function loadCommandPreviews(
  storage: Storage,
  scope: string | undefined,
): StoredCommandPreview[] {
  if (!scope) return [];
  const raw = storage.getItem(storageKey(scope));
  if (!raw) return [];
  if (raw.length > 16_384)
    throw new Error("未确认意图记录超出预算；请先核实原回执。");
  let values: unknown;
  try {
    values = JSON.parse(raw);
  } catch {
    throw new Error("未确认意图记录无效；请先核实原回执，不会生成新键。");
  }
  if (!Array.isArray(values) || values.length > maxCommandPreviews)
    throw new Error("未确认意图记录无效；不会生成新键。");
  if (!values.every(validRecord) || !uniqueCommandPreviews(values))
    throw new Error("未确认意图记录无效；请先核实原回执，不会生成新键。");
  return values;
}

export function saveCommandPreviews(
  storage: Storage,
  scope: string,
  values: StoredCommandPreview[],
): void {
  if (
    values.length > maxCommandPreviews ||
    !values.every(validRecord) ||
    !uniqueCommandPreviews(values)
  )
    throw new Error("存在过多或无效的未确认意图；未发送新命令。");
  if (values.length) storage.setItem(storageKey(scope), JSON.stringify(values));
  else storage.removeItem(storageKey(scope));
}

export function assertCommandPointerTransition(
  previous: StoredCommandPreview | undefined,
  next: StoredCommandPreview,
): void {
  if (!previous) return;
  if (
    (previous.abandoned && !next.abandoned) ||
    (previous.acknowledged && !next.acknowledged) ||
    (previous.receiptID && previous.receiptID !== next.receiptID) ||
    (previous.intentID &&
      (previous.intentID !== next.intentID ||
        previous.metadataSignature !== next.metadataSignature))
  )
    throw new Error(
      "原意图/回执/关闭证明已绑定；迟到回应不能回退或替换恢复指针。",
    );
}
function validRecord(value: unknown): value is StoredCommandPreview {
  if (!value || typeof value !== "object" || Array.isArray(value)) return false;
  const item = value as Record<string, unknown>;
  return (
    typeof item.channel === "string" &&
    /^[a-z-]{1,40}$/.test(item.channel) &&
    commandUUID(item.accountID) &&
    item.accountID === item.accountID.toLowerCase() &&
    commandUUID(item.idempotencyKey) &&
    ["sync", "export", "erase", "operation", "callback_replay"].includes(
      String(item.kind),
    ) &&
    typeof item.signature === "string" &&
    /^v1:[a-f0-9]{64}$/.test(item.signature) &&
    validRecoveryIDs(item) &&
    Object.keys(item).every((key) =>
      [
        "channel",
        "accountID",
        "kind",
        "signature",
        "idempotencyKey",
        "intentID",
        "metadataSignature",
        "receiptID",
        "abandoned",
        "acknowledged",
      ].includes(key),
    )
  );
}
function validRecoveryIDs(item: Record<string, unknown>): boolean {
  return (
    (item.intentID === undefined
      ? item.metadataSignature === undefined
      : commandUUID(item.intentID) &&
        typeof item.metadataSignature === "string" &&
        /^v1:[a-f0-9]{64}$/.test(item.metadataSignature)) &&
    (item.receiptID === undefined ||
      (commandUUID(item.receiptID) && commandUUID(item.intentID))) &&
    (item.abandoned === undefined ||
      (item.abandoned === true &&
        commandUUID(item.intentID) &&
        item.receiptID === undefined)) &&
    validAcknowledgedReceipt(item)
  );
}
function validAcknowledgedReceipt(item: Record<string, unknown>): boolean {
  return (
    item.acknowledged === undefined ||
    (item.acknowledged === true &&
      item.kind === "callback_replay" &&
      item.receiptID === item.intentID &&
      commandUUID(item.intentID) &&
      item.abandoned === undefined)
  );
}
function uniqueCommandPreviews(values: StoredCommandPreview[]): boolean {
  const active = values.filter((item) => !item.abandoned && !item.acknowledged);
  return (
    new Set(active.map((item) => `${item.channel}:${item.accountID}`)).size ===
      active.length &&
    new Set(values.map((item) => item.idempotencyKey)).size === values.length
  );
}
function storageKey(scope: string): string {
  return `aero-id:command-previews:v2:${encodeURIComponent(scope)}`;
}

export async function commandPlanSignature(
  accountID: string,
  plan: CommandPlan,
): Promise<string> {
  return commandValueSignature(
    JSON.stringify({
      accountID,
      plan:
        plan.kind === "callback_replay"
          ? canonicalCallbackReplayPlan(plan)
          : plan,
    }),
  );
}
export async function commandMetadataSignature(
  intent: CommandIntent,
): Promise<string> {
  return commandValueSignature(immutableCommandMetadata(intent));
}
async function commandValueSignature(metadata: string): Promise<string> {
  const value = new TextEncoder().encode(metadata);
  if (value.byteLength > 256 * 1024)
    throw new Error("命令元数据超出预算；未发送新命令。");
  const digest = await crypto.subtle.digest("SHA-256", value);
  return `v1:${Array.from(new Uint8Array(digest), (byte) => byte.toString(16).padStart(2, "0")).join("")}`;
}
