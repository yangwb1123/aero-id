import type {
  AccountView,
  AggregateView,
  Dataset,
  JsonRecord,
  PageData,
  Profile,
} from "../types";
import { CallbackClient } from "./callback";
import { ComplianceClient } from "./compliance";
import { MigrationClient } from "./migration";
import { RegionClient } from "./region";
import { ReplayClient } from "./replay";
import { AeroIdTransport, type RequestOptions } from "./transport";
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

function dataOf(value: unknown): JsonRecord {
  if (!isRecord(value) || !isRecord(value.data)) return {};
  return value.data;
}

function pageOf(data: JsonRecord, field: string): PageData<JsonRecord> {
  return {
    items: records(data[field]),
    nextCursor:
      typeof data.next_cursor === "string" ? data.next_cursor : undefined,
  };
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
    this.compliance = new ComplianceClient(this.transport);
    this.migration = new MigrationClient(this.transport);
    this.regions = new RegionClient(this.transport);
    this.replays = new ReplayClient(this.transport);
  }

  async listDatasets(): Promise<Dataset[]> {
    const data = dataOf(await this.request("GET", "/account-datasets"));
    return records(data.datasets).flatMap((item) => {
      if (typeof item.name !== "string" || typeof item.source !== "string")
        return [];
      return [{ name: item.name, source: item.source, pii: item.pii === true }];
    });
  }

  async getMe(): Promise<AccountView> {
    return dataOf(await this.request("GET", "/me")) as AccountView;
  }

  async getProfile(): Promise<Profile> {
    return dataOf(await this.request("GET", "/me/profile")) as Profile;
  }

  async updateProfile(profile: Partial<Profile>): Promise<Profile> {
    return dataOf(
      await this.request("PATCH", "/me/profile", { body: profile }),
    ) as Profile;
  }

  async getOverview(datasets: string[] = []): Promise<AggregateView> {
    return this.getAggregate("/me/overview", { datasets });
  }

  async getResources(datasets: string[] = []): Promise<AggregateView> {
    return this.getAggregate("/me/resources", { datasets });
  }

  async getMyData(query: AggregateQuery = {}): Promise<AggregateView> {
    return this.getAggregate("/me/data", query);
  }

  async getAdminAccountData(
    id: string,
    query: AggregateQuery = {},
  ): Promise<AggregateView> {
    return this.getAggregate(
      `/admin/accounts/${encodeURIComponent(id)}/data`,
      query,
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

  async getAdminAccount(id: string): Promise<AccountView> {
    return dataOf(
      await this.request("GET", `/admin/accounts/${encodeURIComponent(id)}`),
    ) as AccountView;
  }

  async getAdminAccountOverview(id: string): Promise<AggregateView> {
    return dataOf(
      await this.request(
        "GET",
        `/admin/accounts/${encodeURIComponent(id)}/overview`,
        { query: { consistency: "eventual" } },
      ),
    ) as AggregateView;
  }

  async createAdminAccountJob(
    id: string,
    jobType: AdminAccountJobType,
  ): Promise<JsonRecord> {
    const key = crypto.randomUUID();
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

  async listSources(cursor?: string): Promise<PageData<JsonRecord>> {
    return pageOf(
      dataOf(
        await this.request("GET", "/me/sources", {
          query: { cursor, limit: 50 },
        }),
      ),
      "sources",
    );
  }

  async listMemberships(cursor?: string): Promise<PageData<JsonRecord>> {
    return pageOf(
      dataOf(
        await this.request("GET", "/me/memberships", {
          query: { cursor, limit: 50 },
        }),
      ),
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

  async listSyncJobs(cursor?: string): Promise<PageData<JsonRecord>> {
    return pageOf(
      dataOf(
        await this.request("GET", "/sync/jobs", {
          query: { cursor, limit: 50 },
        }),
      ),
      "jobs",
    );
  }

  async getSyncJob(id: string): Promise<JsonRecord> {
    return dataOf(
      await this.request("GET", `/sync/jobs/${encodeURIComponent(id)}`),
    );
  }

  async reconcileSyncJob(id: string): Promise<JsonRecord> {
    return dataOf(
      await this.request(
        "POST",
        `/sync/jobs/${encodeURIComponent(id)}/reconcile`,
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

  async createSyncJob(datasets: string[]): Promise<JsonRecord> {
    const key = crypto.randomUUID();
    return dataOf(
      await this.request("POST", "/me/sync", {
        idempotencyKey: key,
        body: { datasets, idempotency_key: key },
      }),
    );
  }

  async createExportJob(datasets: string[]): Promise<JsonRecord> {
    const key = crypto.randomUUID();
    return dataOf(
      await this.request("POST", "/me/export", {
        idempotencyKey: key,
        body: { datasets, idempotency_key: key },
      }),
    );
  }

  async eraseMyAccount(): Promise<JsonRecord> {
    const key = crypto.randomUUID();
    return dataOf(
      await this.request("POST", "/me/erase", {
        idempotencyKey: key,
        body: { idempotency_key: key },
      }),
    );
  }

  async listOperations(cursor?: string): Promise<PageData<JsonRecord>> {
    return pageOf(
      dataOf(
        await this.request("GET", "/operations", {
          query: { cursor, limit: 50 },
        }),
      ),
      "operations",
    );
  }

  async getOperation(id: string): Promise<JsonRecord> {
    return dataOf(
      await this.request("GET", `/operations/${encodeURIComponent(id)}`),
    );
  }

  async reconcileOperation(id: string): Promise<JsonRecord> {
    return dataOf(
      await this.request(
        "POST",
        `/operations/${encodeURIComponent(id)}/reconcile`,
      ),
    );
  }

  async createOperation(input: OperationCreateInput): Promise<JsonRecord> {
    const key = input.idempotencyKey.trim() || crypto.randomUUID();
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

  async runOperation(id: string): Promise<JsonRecord> {
    return dataOf(
      await this.request("POST", `/operations/${encodeURIComponent(id)}/run`),
    );
  }

  async listOperationAudit(
    id: string,
    cursor?: string,
  ): Promise<PageData<JsonRecord>> {
    return pageOf(
      dataOf(
        await this.request(
          "GET",
          `/audit/operations/${encodeURIComponent(id)}`,
          { query: { cursor, limit: 50 } },
        ),
      ),
      "events",
    );
  }

  async listAuditEvents(
    filters: AuditEventFilters = {},
    cursor?: string,
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
    idempotencyKey: string = crypto.randomUUID(),
  ): Promise<JsonRecord> {
    return dataOf(
      await this.request("POST", "/admin/roles/grant", {
        idempotencyKey,
        body: { uid, role_code: roleCode },
      }),
    );
  }

  async revokeRole(
    uid: string,
    roleCode: string,
    idempotencyKey: string = crypto.randomUUID(),
  ): Promise<JsonRecord> {
    return dataOf(
      await this.request("POST", "/admin/roles/revoke", {
        idempotencyKey,
        body: { uid, role_code: roleCode },
      }),
    );
  }

  async banUser(
    uid: string,
    reason: string,
    duration?: string,
    idempotencyKey: string = crypto.randomUUID(),
  ): Promise<JsonRecord> {
    return dataOf(
      await this.request("POST", "/admin/bans", {
        idempotencyKey,
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
  ): Promise<AggregateView> {
    return dataOf(
      await this.request("GET", path, {
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
