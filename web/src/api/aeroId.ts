import type {
  AccountView,
  AggregateView,
  CapabilityReport,
  Dataset,
  JsonRecord,
  PageData,
  Profile,
} from "../types";
import { accountReferencePage } from "./accountReferences";
import { CallbackClient } from "./callback";
import { CommandIntentClient } from "./commandIntent";
import { parseCapabilities } from "./capabilities";
import { ComplianceClient } from "./compliance";
import { MigrationClient } from "./migration";
import { RegionClient } from "./region";
import { ReplayClient } from "./replay";
import { isTaskStatus } from "./taskStatus";
import {
  AeroIdError,
  AeroIdTransport,
  requireDataEnvelope as dataOf,
  type RequestOptions,
} from "./transport";
export { AeroIdError } from "./transport";

const maxExportResponseBytes = 64 * 1024 * 1024;

export interface AdminAccountFilters {
  query?: string;
  status?: string;
  homeRegion?: string;
}

export type AdminAccountJobType = "sync" | "export" | "erase";

export interface AuditEventFilters {
  source?: string;
  action?: string;
  operationID?: string;
  accountID?: string;
  requestID?: string;
}

export type ConsistencyMode = "eventual" | "bounded" | "strong";

export interface AggregateQuery {
  datasets?: string[];
  consistency?: ConsistencyMode;
  maxAge?: string;
}

export interface OperationStepInput {
  stepKey: string;
  source: "snaplink" | "aero-im" | "aero-vault";
  action:
    "sync" | "summary" | "source.summary" | "erase" | "delete" | "source.erase";
  requestPayload: JsonRecord;
  compensationAction?: string;
}

export interface OperationCreateInput {
  accountID: string;
  operationType: "sync" | "erase" | "source.command" | "replay";
  homeRegion?: string;
  requestPayload?: JsonRecord;
  idempotencyKey: string;
  steps: OperationStepInput[];
}

function records(value: unknown): JsonRecord[] {
  return Array.isArray(value) ? value.filter(isRecord) : [];
}

function isRecord(value: unknown): value is JsonRecord {
  return Boolean(value) && typeof value === "object" && !Array.isArray(value);
}

function pageOf(data: JsonRecord, field: string): PageData<JsonRecord> {
  return {
    items: records(data[field]),
    nextCursor:
      typeof data.next_cursor === "string" ? data.next_cursor : undefined,
  };
}

function taskPageOf(
  value: unknown,
  field: "jobs" | "operations",
): PageData<JsonRecord> {
  const data = dataOf(value);
  if (
    !Array.isArray(data[field]) ||
    !data[field].every(isRecord) ||
    (data.next_cursor !== undefined && typeof data.next_cursor !== "string")
  )
    throw new AeroIdError(
      0,
      "response.invalid",
      "列表响应无效；请重读原任务列表",
    );
  return pageOf(data, field);
}

function profileOf(value: unknown): Profile {
  const data = dataOf(value);
  if (
    ["display_name", "avatar_url", "locale", "timezone"].some(
      (field) => typeof data[field] !== "string",
    )
  )
    throw new AeroIdError(
      0,
      "response.invalid",
      "资料响应无效；请核实当前资料，不会自动重试保存",
    );
  return data as Profile;
}

function resourceSourceErrorsValid(value: unknown): boolean {
  if (value === undefined) return true;
  return (
    isRecord(value) &&
    Object.keys(value).length <= 16 &&
    Object.values(value).every((code) => typeof code === "string")
  );
}

function resourceAggregateOf(value: unknown): AggregateView {
  const data = dataOf(value);
  const stale = data.stale_datasets;
  if (
    !Array.isArray(data.snapshots) ||
    data.snapshots.length > 64 ||
    !data.snapshots.every(isRecord) ||
    typeof data.partial !== "boolean" ||
    (data.consistency !== undefined &&
      !["eventual", "bounded", "strong"].includes(
        data.consistency as string,
      )) ||
    !resourceSourceErrorsValid(data.source_errors) ||
    (stale !== null &&
      stale !== undefined &&
      (!Array.isArray(stale) ||
        stale.length > 64 ||
        !stale.every((name) => typeof name === "string")))
  )
    throw new AeroIdError(
      0,
      "response.invalid",
      "资源摘要响应无效；请显式重读，不会触发来源同步",
    );
  return data as AggregateView;
}

function taskDetailOf(
  value: unknown,
  field: "job_id" | "operation_id",
  id: string,
): JsonRecord {
  const data = dataOf(value);
  if (
    !id ||
    typeof data[field] !== "string" ||
    data[field] !== id ||
    !isTaskStatus(data.status, field)
  )
    throw new AeroIdError(
      0,
      "response.invalid",
      "任务详情与原请求不一致；请核实原回执，不会重发命令",
    );
  return data;
}

function operationAuditOf(value: unknown, id: string): PageData<JsonRecord> {
  const data = dataOf(value);
  if (
    !id ||
    typeof data.operation_id !== "string" ||
    data.operation_id !== id ||
    !Array.isArray(data.events) ||
    !data.events.every(isRecord) ||
    (data.next_cursor !== undefined && typeof data.next_cursor !== "string")
  )
    throw new AeroIdError(
      0,
      "response.invalid",
      "审计响应无效；请重读原 Operation",
    );
  return pageOf(data, "events");
}

function requiredIdempotencyKey(value: string): string {
  const key = value.trim();
  if (!key) throw new Error("写入命令需要 Idempotency-Key");
  return key;
}

function activityPage(data: JsonRecord): PageData<JsonRecord> {
  if (Array.isArray(data.events)) return pageOf(data, "events");
  const snapshot = records(data.snapshots).find(
    (item) => item.dataset === "aero-im.activity_summary",
  );
  const projected = isRecord(snapshot?.data) ? snapshot.data : {};
  return {
    items: records(projected.events ?? projected.items),
    nextCursor:
      typeof projected.next_cursor === "string"
        ? projected.next_cursor
        : undefined,
  };
}

export class AeroIdClient {
  private readonly transport: AeroIdTransport;
  readonly callbacks: CallbackClient;
  readonly commands: CommandIntentClient;
  readonly compliance: ComplianceClient;
  readonly migration: MigrationClient;
  readonly regions: RegionClient;
  readonly replays: ReplayClient;

  constructor(
    baseUrl: string,
    accessToken: () => string,
    fetcher: typeof fetch = window.fetch.bind(window),
  ) {
    this.transport = new AeroIdTransport(baseUrl, accessToken, fetcher);
    this.callbacks = new CallbackClient(this.transport);
    this.commands = new CommandIntentClient(this.transport);
    this.compliance = new ComplianceClient(this.transport);
    this.migration = new MigrationClient(this.transport);
    this.regions = new RegionClient(this.transport);
    this.replays = new ReplayClient(this.transport);
  }

  cancelPendingReads(): void {
    this.transport.cancelPendingReads();
  }

  async listDatasets(signal?: AbortSignal): Promise<Dataset[]> {
    const data = dataOf(
      await this.request("GET", "/account-datasets", { signal }),
    );
    return records(data.datasets).flatMap((item) => {
      if (typeof item.name !== "string" || typeof item.source !== "string")
        return [];
      return [{ name: item.name, source: item.source, pii: item.pii === true }];
    });
  }

  async getCapabilities(signal?: AbortSignal): Promise<CapabilityReport> {
    return parseCapabilities(
      dataOf(await this.request("GET", "/me/capabilities", { signal })),
    );
  }

  async getMe(signal?: AbortSignal): Promise<AccountView> {
    return dataOf(await this.request("GET", "/me", { signal })) as AccountView;
  }

  async getProfile(signal?: AbortSignal): Promise<Profile> {
    return profileOf(await this.request("GET", "/me/profile", { signal }));
  }

  async updateProfile(profile: Partial<Profile>): Promise<Profile> {
    return profileOf(
      await this.request("PATCH", "/me/profile", { body: profile }),
    );
  }

  async getOverview(
    datasets: string[] = [],
    signal?: AbortSignal,
  ): Promise<AggregateView> {
    return this.getAggregate("/me/overview", { datasets }, signal);
  }

  async getResources(
    datasets: string[] = [],
    signal?: AbortSignal,
  ): Promise<AggregateView> {
    return resourceAggregateOf(
      await this.request("GET", "/me/resources", {
        query: { dataset: datasets, consistency: "eventual" },
        signal,
      }),
    );
  }

  async getMyData(
    query: AggregateQuery = {},
    signal?: AbortSignal,
  ): Promise<AggregateView> {
    return this.getAggregate("/me/data", query, signal);
  }

  async getAdminAccountData(
    id: string,
    query: AggregateQuery = {},
    signal?: AbortSignal,
  ): Promise<AggregateView> {
    return this.getAggregate(
      `/admin/accounts/${encodeURIComponent(id)}/data`,
      query,
      signal,
    );
  }

  async listAdminAccounts(
    filters: AdminAccountFilters = {},
    cursor?: string,
  ): Promise<PageData<JsonRecord>> {
    return pageOf(
      dataOf(
        await this.request("GET", "/admin/accounts", {
          query: {
            q: filters.query,
            status: filters.status,
            home_region: filters.homeRegion,
            cursor,
            limit: 50,
          },
        }),
      ),
      "accounts",
    );
  }

  async getAdminAccount(
    id: string,
    signal?: AbortSignal,
  ): Promise<AccountView> {
    return dataOf(
      await this.request("GET", `/admin/accounts/${encodeURIComponent(id)}`, {
        signal,
      }),
    ) as AccountView;
  }

  async getAdminAccountOverview(
    id: string,
    signal?: AbortSignal,
  ): Promise<AggregateView> {
    return dataOf(
      await this.request(
        "GET",
        `/admin/accounts/${encodeURIComponent(id)}/overview`,
        { query: { consistency: "eventual" }, signal },
      ),
    ) as AggregateView;
  }

  async createAdminAccountJob(
    id: string,
    jobType: AdminAccountJobType,
    idempotencyKey: string,
  ): Promise<JsonRecord> {
    const key = requiredIdempotencyKey(idempotencyKey);
    return dataOf(
      await this.request(
        "POST",
        `/admin/accounts/${encodeURIComponent(id)}/${jobType}`,
        {
          idempotencyKey: key,
          body: { datasets: [], idempotency_key: key },
        },
      ),
    );
  }

  async listSources(
    cursor?: string,
    signal?: AbortSignal,
  ): Promise<PageData<JsonRecord>> {
    return accountReferencePage(
      await this.request("GET", "/me/sources", {
        query: { cursor, limit: 50 },
        signal,
      }),
      "sources",
    );
  }

  async listMemberships(
    cursor?: string,
    signal?: AbortSignal,
  ): Promise<PageData<JsonRecord>> {
    return accountReferencePage(
      await this.request("GET", "/me/memberships", {
        query: { cursor, limit: 50 },
        signal,
      }),
      "memberships",
    );
  }

  async listActivity(cursor?: string): Promise<PageData<JsonRecord>> {
    return activityPage(
      dataOf(
        await this.request("GET", "/me/activity", {
          query: { cursor, limit: 50 },
        }),
      ),
    );
  }

  async listSyncJobs(
    cursor?: string,
    signal?: AbortSignal,
  ): Promise<PageData<JsonRecord>> {
    return taskPageOf(
      await this.request("GET", "/sync/jobs", {
        query: { cursor, limit: 50 },
        signal,
      }),
      "jobs",
    );
  }

  async getSyncJob(id: string, signal?: AbortSignal): Promise<JsonRecord> {
    return taskDetailOf(
      await this.request("GET", `/sync/jobs/${encodeURIComponent(id)}`, {
        signal,
      }),
      "job_id",
      id,
    );
  }

  async reconcileSyncJob(
    id: string,
    idempotencyKey: string,
  ): Promise<JsonRecord> {
    const key = requiredIdempotencyKey(idempotencyKey);
    return dataOf(
      await this.request(
        "POST",
        `/sync/jobs/${encodeURIComponent(id)}/reconcile`,
        { idempotencyKey: key },
      ),
    );
  }

  async downloadExport(id: string, oneTime = true): Promise<Blob> {
    return this.transport.download(
      `/sync/jobs/${encodeURIComponent(id)}/export/download`,
      { query: { one_time: String(oneTime) }, timeoutMs: 60_000 },
      maxExportResponseBytes,
    );
  }

  async createSyncJob(
    datasets: string[],
    idempotencyKey: string,
  ): Promise<JsonRecord> {
    const key = requiredIdempotencyKey(idempotencyKey);
    return dataOf(
      await this.request("POST", "/me/sync", {
        idempotencyKey: key,
        body: { datasets, idempotency_key: key },
      }),
    );
  }

  async createExportJob(
    datasets: string[],
    idempotencyKey: string,
  ): Promise<JsonRecord> {
    const key = requiredIdempotencyKey(idempotencyKey);
    return dataOf(
      await this.request("POST", "/me/export", {
        idempotencyKey: key,
        body: { datasets, idempotency_key: key },
      }),
    );
  }

  async eraseMyAccount(idempotencyKey: string): Promise<JsonRecord> {
    const key = requiredIdempotencyKey(idempotencyKey);
    return dataOf(
      await this.request("POST", "/me/erase", {
        idempotencyKey: key,
        body: { idempotency_key: key },
      }),
    );
  }

  async listOperations(
    cursor?: string,
    signal?: AbortSignal,
  ): Promise<PageData<JsonRecord>> {
    return taskPageOf(
      await this.request("GET", "/operations", {
        signal,
        query: { cursor, limit: 50 },
      }),
      "operations",
    );
  }

  async getOperation(id: string, signal?: AbortSignal): Promise<JsonRecord> {
    return taskDetailOf(
      await this.request("GET", `/operations/${encodeURIComponent(id)}`, {
        signal,
      }),
      "operation_id",
      id,
    );
  }

  async reconcileOperation(
    id: string,
    idempotencyKey: string,
  ): Promise<JsonRecord> {
    const key = requiredIdempotencyKey(idempotencyKey);
    return dataOf(
      await this.request(
        "POST",
        `/operations/${encodeURIComponent(id)}/reconcile`,
        { idempotencyKey: key },
      ),
    );
  }

  async createOperation(input: OperationCreateInput): Promise<JsonRecord> {
    const key = requiredIdempotencyKey(input.idempotencyKey);
    return dataOf(
      await this.request("POST", "/operations", {
        idempotencyKey: key,
        body: {
          account_id: input.accountID,
          home_region: input.homeRegion,
          operation_type: input.operationType,
          idempotency_key: key,
          request_payload: input.requestPayload,
          steps: input.steps.map((step) => ({
            step_key: step.stepKey,
            source: step.source,
            action: step.action,
            request_payload: step.requestPayload,
            compensation_action: step.compensationAction,
          })),
        },
      }),
    );
  }

  async runOperation(id: string, idempotencyKey: string): Promise<JsonRecord> {
    const key = requiredIdempotencyKey(idempotencyKey);
    return dataOf(
      await this.request("POST", `/operations/${encodeURIComponent(id)}/run`, {
        idempotencyKey: key,
      }),
    );
  }

  async listOperationAudit(
    id: string,
    cursor?: string,
    signal?: AbortSignal,
  ): Promise<PageData<JsonRecord>> {
    return operationAuditOf(
      await this.request("GET", `/audit/operations/${encodeURIComponent(id)}`, {
        query: { cursor, limit: 50 },
        signal,
      }),
      id,
    );
  }

  async listAuditEvents(
    filters: AuditEventFilters = {},
    cursor?: string,
    signal?: AbortSignal,
  ): Promise<PageData<JsonRecord>> {
    return pageOf(
      dataOf(
        await this.request("GET", "/audit/events", {
          query: {
            source: filters.source,
            action: filters.action,
            operation_id: filters.operationID,
            account_id: filters.accountID,
            request_id: filters.requestID,
            cursor,
            limit: 50,
          },
          signal,
        }),
      ),
      "events",
    );
  }

  async verifyAuditChain(
    source: string,
    partition: string,
  ): Promise<JsonRecord> {
    return dataOf(
      await this.request("GET", "/audit/verify", {
        query: { source, partition },
      }),
    );
  }

  async listAdminAudit(
    action?: string,
    cursor?: string,
  ): Promise<PageData<JsonRecord>> {
    return pageOf(
      dataOf(
        await this.request("GET", "/admin/audit-log", {
          query: { action, cursor, limit: 50 },
        }),
      ),
      "events",
    );
  }

  async listRoles(): Promise<JsonRecord[]> {
    return records(dataOf(await this.request("GET", "/admin/roles")).roles);
  }

  async listUserRoles(uid: string): Promise<JsonRecord[]> {
    return records(
      dataOf(
        await this.request("GET", `/admin/roles/${encodeURIComponent(uid)}`),
      ).roles,
    );
  }

  async grantRole(
    uid: string,
    roleCode: string,
    idempotencyKey: string,
  ): Promise<JsonRecord> {
    const key = requiredIdempotencyKey(idempotencyKey);
    return dataOf(
      await this.request("POST", "/admin/roles/grant", {
        idempotencyKey: key,
        body: { uid, role_code: roleCode },
      }),
    );
  }

  async revokeRole(
    uid: string,
    roleCode: string,
    idempotencyKey: string,
  ): Promise<JsonRecord> {
    const key = requiredIdempotencyKey(idempotencyKey);
    return dataOf(
      await this.request("POST", "/admin/roles/revoke", {
        idempotencyKey: key,
        body: { uid, role_code: roleCode },
      }),
    );
  }

  async banUser(
    uid: string,
    reason: string,
    duration: string | undefined,
    idempotencyKey: string,
  ): Promise<JsonRecord> {
    const key = requiredIdempotencyKey(idempotencyKey);
    return dataOf(
      await this.request("POST", "/admin/bans", {
        idempotencyKey: key,
        body: { uid, reason, duration },
      }),
    );
  }

  async listBanned(cursor?: string): Promise<PageData<JsonRecord>> {
    return pageOf(
      dataOf(
        await this.request("GET", "/admin/bans", {
          query: { cursor, limit: 50 },
        }),
      ),
      "bans",
    );
  }

  async unbanUser(uid: string): Promise<JsonRecord> {
    return dataOf(
      await this.request("DELETE", `/admin/bans/${encodeURIComponent(uid)}`),
    );
  }

  private async getAggregate(
    path: string,
    query: AggregateQuery,
    signal?: AbortSignal,
  ): Promise<AggregateView> {
    return dataOf(
      await this.request("GET", path, {
        signal,
        query: {
          dataset: query.datasets ?? [],
          consistency: query.consistency ?? "eventual",
          max_age: query.maxAge,
        },
      }),
    ) as AggregateView;
  }

  private async request(
    method: string,
    path: string,
    options: RequestOptions = {},
  ): Promise<unknown> {
    return this.transport.request(method, path, options);
  }
}
