import { AeroIdTransport, AeroIdError, requireDataEnvelope } from "./transport";

export interface CommandPlanStep {
  step_key: string;
  source: "snaplink" | "aero-im" | "aero-vault";
  action: string;
  request_payload?: Record<string, unknown>;
  compensation_action?: string;
}
export interface CommandPlan {
  kind: "sync" | "export" | "erase" | "operation" | "callback_replay";
  callback?: {
    subscription_id: string;
    delivery_id: string;
    event_id: string;
    revision: number;
    lease_generation: number;
  };
  datasets: string[];
  operation_type?: "sync" | "erase" | "source.command" | "replay";
  request_payload?: Record<string, unknown>;
  steps?: CommandPlanStep[];
}
export interface CommandSnapshotVersion {
  id: string;
  dataset: string;
  source: string;
  source_region: string;
  status: string;
  version: number;
  generated_at?: string;
  last_synced_at?: string;
  expires_at?: string;
}
export interface CommandIntent {
  intent_id: string;
  schema_version: 1;
  account_id: string;
  tenant_id: string;
  client_id: string;
  issuer: string;
  version: number;
  status: "prepared" | "executed" | "abandoned";
  plan: CommandPlan;
  preconditions: {
    account_status: string;
    home_region: string;
    writer_epoch: number;
    resolver_version: string;
    snapshots: CommandSnapshotVersion[];
  };
  confirmation_digest: string;
  destructive: boolean;
  authorization_version: "unsupported";
  resource_authorization: "unknown";
  receipt_kind?: "sync_job" | "operation" | "callback_replay";
  receipt_id?: string;
  created_at: string;
  expires_at: string;
  abandoned_at?: string;
}

export class CommandIntentClient {
  constructor(private readonly transport: AeroIdTransport) {}

  async prepare(
    plan: CommandPlan,
    key: string,
    accountID?: string,
  ): Promise<CommandIntent> {
    if (
      plan.kind === "callback_replay" &&
      !validCallbackPlan(plan as unknown as Record<string, unknown>)
    )
      invalidIntent();
    if (accountID !== undefined) {
      if (!uuid(accountID)) invalidIntent();
      accountID = accountID.toLowerCase();
    }
    const value = parseCommandIntent(
      requireDataEnvelope(
        await this.transport.request("POST", "/command-intents", {
          body: { plan, ...(accountID ? { account_id: accountID } : {}) },
          idempotencyKey: key,
        }),
      ),
    );
    if (
      value.plan.kind !== plan.kind ||
      (accountID && value.account_id !== accountID)
    )
      invalidIntent();
    if (
      !sameDatasets(plan.datasets, value.plan.datasets) ||
      (plan.kind === "operation" && !sameOperationOutline(plan, value.plan)) ||
      (plan.kind === "callback_replay" &&
        !sameCallbackTarget(plan.callback, value.plan.callback))
    )
      invalidIntent();
    return value;
  }
  async get(id: string, signal?: AbortSignal): Promise<CommandIntent> {
    requireIntentID(id);
    const value = parseCommandIntent(
      requireDataEnvelope(
        await this.transport.request("GET", `/command-intents/${id}`, {
          signal,
        }),
      ),
    );
    if (value.intent_id !== id) invalidIntent();
    return value;
  }
  async findByKey(key: string, signal?: AbortSignal): Promise<CommandIntent> {
    if (!key.trim() || key.length > 200) invalidIntent();
    key = key.trim();
    const value = requireDataEnvelope(
      await this.transport.request("GET", "/command-intents/by-key", {
        idempotencyKey: key,
        signal,
      }),
    );
    if (
      !record(value) ||
      value.key_digest !== (await commandLookupKeyDigest(key))
    )
      invalidIntent();
    return parseCommandIntent(value.intent);
  }
  async abandon(intent: CommandIntent): Promise<CommandIntent> {
    requireIntentID(intent.intent_id);
    const value = parseCommandIntent(
      requireDataEnvelope(
        await this.transport.request(
          "POST",
          `/command-intents/${intent.intent_id}/abandon`,
          {
            body: {
              version: 1,
              confirmation_digest: intent.confirmation_digest,
            },
          },
        ),
      ),
    );
    if (
      !sameOriginalCommandIntent(intent, value) ||
      !["abandoned", "executed"].includes(value.status)
    )
      invalidIntent();
    return value;
  }
  async execute(intent: CommandIntent): Promise<CommandIntent> {
    requireIntentID(intent.intent_id);
    if (intent.status === "abandoned") invalidIntent();
    const value = parseCommandIntent(
      requireDataEnvelope(
        await this.transport.request(
          "POST",
          `/command-intents/${intent.intent_id}/execute`,
          {
            body: {
              version: intent.version,
              confirmation_digest: intent.confirmation_digest,
            },
          },
        ),
      ),
    );
    if (
      !sameOriginalCommandIntent(intent, value) ||
      value.status !== "executed" ||
      value.version !==
        (intent.status === "executed" ? intent.version : intent.version + 1)
    )
      invalidIntent();
    return value;
  }
}

const record = (value: unknown): value is Record<string, unknown> =>
  Boolean(value) && typeof value === "object" && !Array.isArray(value);
const text = (value: unknown): value is string =>
  typeof value === "string" && value.length > 0 && value.length <= 512;
const uuid = (value: unknown): value is string =>
  typeof value === "string" &&
  /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(
    value,
  ) &&
  !/^0{8}-0{4}-0{4}-0{4}-0{12}$/.test(value);
const integer = (value: unknown): value is number =>
  Number.isSafeInteger(value) && Number(value) >= 0;
const time = (value: unknown): value is string =>
  text(value) && Number.isFinite(Date.parse(value));
const datasets = (value: unknown): value is string[] =>
  Array.isArray(value) &&
  value.length > 0 &&
  value.length <= 11 &&
  value.every(
    (item) => text(item) && /^[a-z][a-z0-9-]*\.[a-z_]+$/.test(item),
  ) &&
  new Set(value).size === value.length;

function invalidIntent(): never {
  throw new AeroIdError(
    0,
    "response.invalid",
    "命令预览/回执格式无效；不会推断执行成功或生成新命令",
  );
}
function requireIntentID(id: string): void {
  if (!uuid(id)) invalidIntent();
}

export function parseCommandIntent(value: unknown): CommandIntent {
  if (!validIntent(value)) invalidIntent();
  return value as unknown as CommandIntent;
}
function validIntent(value: unknown): boolean {
  if (
    !record(value) ||
    value.schema_version !== 1 ||
    value.authorization_version !== "unsupported" ||
    value.resource_authorization !== "unknown"
  )
    return false;
  if (
    ![value.intent_id, value.account_id].every(uuid) ||
    ![value.tenant_id, value.client_id, value.issuer].every(text)
  )
    return false;
  return (
    validConfirmation(value) &&
    validLifetime(value) &&
    validReceipt(value) &&
    validPlan(value.plan) &&
    validDestructive(value) &&
    validPreconditions(value.preconditions) &&
    validSnapshotScope(value)
  );
}
function validConfirmation(value: Record<string, unknown>): boolean {
  return (
    integer(value.version) &&
    value.version >= 1 &&
    typeof value.destructive === "boolean" &&
    text(value.confirmation_digest) &&
    /^v1:[a-f0-9]{64}$/.test(value.confirmation_digest)
  );
}
function validLifetime(value: Record<string, unknown>): boolean {
  return (
    time(value.created_at) &&
    time(value.expires_at) &&
    Date.parse(value.expires_at) > Date.parse(value.created_at)
  );
}
function validDestructive(value: Record<string, unknown>): boolean {
  const plan = value.plan as unknown as CommandPlan;
  const destructive = (action: string | undefined) =>
    ["erase", "delete", "source.erase"].includes(action ?? "");
  return (
    value.destructive ===
    (plan.kind === "erase" ||
      plan.kind === "callback_replay" ||
      Boolean(
        plan.steps?.some(
          (step) =>
            destructive(step.action) || destructive(step.compensation_action),
        ),
      ))
  );
}
function sameDatasets(a: string[], b: string[]): boolean {
  return (
    a.length === b.length &&
    new Set(a).size === a.length &&
    a.every((name) => b.includes(name))
  );
}
function validReceipt(value: Record<string, unknown>): boolean {
  if (value.status === "abandoned")
    return (
      value.version === 2 &&
      !value.receipt_kind &&
      !value.receipt_id &&
      time(value.abandoned_at) &&
      Date.parse(value.abandoned_at) >= Date.parse(String(value.created_at))
    );
  if (value.abandoned_at !== undefined) return false;
  if (value.status === "prepared")
    return value.version === 1 && !value.receipt_kind && !value.receipt_id;
  const plan = value.plan;
  return (
    value.status === "executed" &&
    value.version === 2 &&
    record(plan) &&
    value.receipt_kind === expectedReceiptKind(plan.kind) &&
    uuid(value.receipt_id) &&
    sameReceiptTarget(value, plan)
  );
}
function expectedReceiptKind(kind: unknown): string {
  return kind === "callback_replay"
    ? "callback_replay"
    : kind === "operation"
      ? "operation"
      : "sync_job";
}
function sameReceiptTarget(
  value: Record<string, unknown>,
  plan: Record<string, unknown>,
): boolean {
  return (
    plan.kind !== "callback_replay" || value.receipt_id === value.intent_id
  );
}
function validPlan(value: unknown): boolean {
  if (record(value) && value.kind === "callback_replay")
    return validCallbackPlan(value);
  if (
    !record(value) ||
    !["sync", "export", "erase", "operation"].includes(String(value.kind)) ||
    !datasets(value.datasets) ||
    value.callback !== undefined
  )
    return false;
  if (value.kind !== "operation")
    return !value.operation_type && !value.steps && !value.request_payload;
  if (
    !["sync", "erase", "source.command", "replay"].includes(
      String(value.operation_type),
    ) ||
    !Array.isArray(value.steps)
  )
    return false;
  return (
    value.steps.length > 0 &&
    value.steps.length <= 32 &&
    value.steps.every(validStep) &&
    validOperationScope(value as unknown as CommandPlan)
  );
}
function sameCallbackTarget(
  a: CommandPlan["callback"],
  b: CommandPlan["callback"],
): boolean {
  return Boolean(
    a &&
    b &&
    a.subscription_id.toLowerCase() === b.subscription_id &&
    a.delivery_id.toLowerCase() === b.delivery_id &&
    a.event_id.toLowerCase() === b.event_id &&
    a.revision === b.revision &&
    a.lease_generation === b.lease_generation,
  );
}

function validCallbackPlan(value: Record<string, unknown>): boolean {
  const target = value.callback;
  return (
    Object.keys(value).every((key) =>
      ["kind", "datasets", "callback"].includes(key),
    ) &&
    Array.isArray(value.datasets) &&
    value.datasets.length === 0 &&
    !value.operation_type &&
    !value.steps &&
    !value.request_payload &&
    record(target) &&
    [target.subscription_id, target.delivery_id, target.event_id].every(uuid) &&
    integer(target.revision) &&
    target.revision > 0 &&
    integer(target.lease_generation) &&
    Object.keys(target).every((key) =>
      [
        "subscription_id",
        "delivery_id",
        "event_id",
        "revision",
        "lease_generation",
      ].includes(key),
    )
  );
}

function validOperationScope(plan: CommandPlan): boolean {
  const steps = plan.steps!;
  return (
    (plan.request_payload === undefined || record(plan.request_payload)) &&
    new Set(steps.map((step) => step.step_key)).size === steps.length &&
    sameDatasets(plan.datasets, [
      ...new Set(
        steps.flatMap((step) => step.request_payload!.datasets as string[]),
      ),
    ]) &&
    steps.every((step) => operationTypeAllows(plan.operation_type, step.action))
  );
}
const actions = new Set([
  "sync",
  "summary",
  "source.summary",
  "erase",
  "delete",
  "source.erase",
]);
function validStep(value: unknown): boolean {
  if (
    !record(value) ||
    !text(value.step_key) ||
    !["snaplink", "aero-im", "aero-vault"].includes(String(value.source))
  )
    return false;
  return (
    actions.has(String(value.action)) &&
    (value.compensation_action === undefined ||
      actions.has(String(value.compensation_action))) &&
    record(value.request_payload) &&
    datasets(value.request_payload.datasets) &&
    value.request_payload.datasets.every((name) =>
      name.startsWith(`${String(value.source)}.`),
    )
  );
}
function operationTypeAllows(type: unknown, action: string): boolean {
  const destructive = ["erase", "delete", "source.erase"].includes(action);
  return (type !== "sync" || !destructive) && (type !== "erase" || destructive);
}
function validPreconditions(value: unknown): boolean {
  if (
    !record(value) ||
    !["active", "suspended"].includes(String(value.account_status)) ||
    !text(value.home_region) ||
    !text(value.resolver_version)
  )
    return false;
  if (
    !integer(value.writer_epoch) ||
    !Array.isArray(value.snapshots) ||
    value.snapshots.length > 11
  )
    return false;
  return (
    value.snapshots.every(validSnapshot) &&
    new Set(value.snapshots.map((item) => item.id)).size ===
      value.snapshots.length &&
    new Set(value.snapshots.map((item) => item.dataset)).size ===
      value.snapshots.length
  );
}
function validSnapshot(value: unknown): boolean {
  if (
    !record(value) ||
    !uuid(value.id) ||
    ![value.dataset, value.source, value.status].every(text) ||
    !integer(value.version)
  )
    return false;
  if (
    typeof value.source_region !== "string" ||
    value.source_region.length > 128
  )
    return false;
  return [value.generated_at, value.last_synced_at, value.expires_at].every(
    (item) => item === undefined || time(item),
  );
}
function validSnapshotScope(value: Record<string, unknown>): boolean {
  const intent = value as unknown as CommandIntent;
  return intent.preconditions.snapshots.every(
    (snapshot) =>
      intent.plan.datasets.includes(snapshot.dataset) &&
      snapshot.dataset.startsWith(`${snapshot.source}.`) &&
      ["fresh", "stale", "unavailable", "unknown"].includes(snapshot.status),
  );
}
function sameOperationOutline(
  requested: CommandPlan,
  prepared: CommandPlan,
): boolean {
  return (
    requested.operation_type === prepared.operation_type &&
    requested.steps?.length === prepared.steps?.length &&
    Boolean(
      requested.steps?.every((step, index) => {
        const other = prepared.steps?.[index];
        return (
          other &&
          step.step_key === other.step_key &&
          step.source === other.source &&
          step.action === other.action &&
          (step.compensation_action ?? "") ===
            (other.compensation_action ?? "") &&
          sameDatasets(
            (step.request_payload?.datasets as string[]) ?? [],
            (other.request_payload?.datasets as string[]) ?? [],
          )
        );
      }),
    )
  );
}

// GET/execute may add a receipt and advance version, never change the original
// immutable plan, lifetime, principal or admission metadata under one digest.
export function sameOriginalCommandIntent(
  original: CommandIntent,
  result: CommandIntent,
): boolean {
  return (
    immutableCommandMetadata(original) === immutableCommandMetadata(result)
  );
}
export function immutableCommandMetadata(intent: CommandIntent): string {
  return canonical({
    ...intent,
    version: undefined,
    status: undefined,
    receipt_kind: undefined,
    receipt_id: undefined,
    abandoned_at: undefined,
    created_at: canonicalDatabaseTime(intent.created_at),
    expires_at: canonicalDatabaseTime(intent.expires_at),
  });
}
// Create returns Go's nanosecond time; Get/execute use the authoritative
// PostgreSQL/pgx microsecond columns. Normalize ONLY these two DB timestamps,
// preserving microseconds (Date.parse alone would lose another three digits).
// The original confirmation digest and all plan/snapshot metadata stay exact.
function canonicalDatabaseTime(value: string): string {
  const parts =
    /^(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2})(?:\.(\d{1,9}))?(Z|[+-]\d{2}:\d{2})$/.exec(
      value,
    );
  if (!parts) invalidIntent();
  const second = new Date(`${parts[1]}${parts[3]}`).toISOString().slice(0, 19);
  const fraction = (parts[2] ?? "").padEnd(6, "0").slice(0, 6);
  return `${second}.${fraction}Z`;
}
export async function commandLookupKeyDigest(key: string): Promise<string> {
  const digest = await crypto.subtle.digest(
    "SHA-256",
    new TextEncoder().encode(key),
  );
  return `v1:${Array.from(new Uint8Array(digest), (byte) => byte.toString(16).padStart(2, "0")).join("")}`;
}
function canonical(value: unknown, depth = 0): string {
  if (depth > 16) invalidIntent();
  if (Array.isArray(value))
    return `[${value.map((item) => canonical(item, depth + 1)).join(",")}]`;
  if (record(value))
    return `{${Object.keys(value)
      .filter((key) => value[key] !== undefined)
      .sort()
      .map(
        (key) => `${JSON.stringify(key)}:${canonical(value[key], depth + 1)}`,
      )
      .join(",")}}`;
  return JSON.stringify(value);
}
