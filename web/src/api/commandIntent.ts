import { AeroIdTransport, AeroIdError, requireDataEnvelope } from "./transport";

export interface CommandPlanStep {
  step_key: string;
  source: "snaplink" | "aero-im" | "aero-vault";
  action: string;
  request_payload?: Record<string, unknown>;
  compensation_action?: string;
}
export interface CommandPlan {
  kind: "sync" | "export" | "erase" | "operation";
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
  status: "prepared" | "executed";
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
  receipt_kind?: "sync_job" | "operation";
  receipt_id?: string;
  created_at: string;
  expires_at: string;
}

export class CommandIntentClient {
  constructor(private readonly transport: AeroIdTransport) {}

  async prepare(
    plan: CommandPlan,
    key: string,
    accountID?: string,
  ): Promise<CommandIntent> {
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
      plan.datasets.length > 0 &&
      !sameDatasets(plan.datasets, value.plan.datasets)
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
  async execute(intent: CommandIntent): Promise<CommandIntent> {
    requireIntentID(intent.intent_id);
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
      value.intent_id !== intent.intent_id ||
      value.confirmation_digest !== intent.confirmation_digest ||
      value.account_id !== intent.account_id ||
      value.tenant_id !== intent.tenant_id ||
      value.client_id !== intent.client_id ||
      value.issuer !== intent.issuer ||
      value.status !== "executed"
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
    validPreconditions(value.preconditions)
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
  if (value.status === "prepared")
    return value.version === 1 && !value.receipt_kind && !value.receipt_id;
  return (
    value.status === "executed" &&
    Number(value.version) >= 2 &&
    ["sync_job", "operation"].includes(String(value.receipt_kind)) &&
    uuid(value.receipt_id)
  );
}
function validPlan(value: unknown): boolean {
  if (
    !record(value) ||
    !["sync", "export", "erase", "operation"].includes(String(value.kind)) ||
    !datasets(value.datasets)
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
    new Set(value.steps.map((item) => item.step_key)).size ===
      value.steps.length
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
    (value.request_payload === undefined || record(value.request_payload))
  );
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
  return value.snapshots.every(validSnapshot);
}
function validSnapshot(value: unknown): boolean {
  if (
    !record(value) ||
    !uuid(value.id) ||
    ![value.dataset, value.source, value.status].every(text) ||
    !integer(value.version)
  )
    return false;
  if (typeof value.source_region !== "string") return false;
  return [value.generated_at, value.last_synced_at, value.expires_at].every(
    (item) => item === undefined || time(item),
  );
}
