import type { JsonRecord } from "../types";
import { AeroIdTransport, requireDataEnvelope as dataOf } from "./transport";

export type LegacySource = "aero-vault" | "aero-im";
export type LegacyCutoverStatus =
  "legacy" | "shadow" | "cut_over" | "rolled_back";

export interface LegacyCompareInput {
  ownerUID: string;
  source: LegacySource;
  limit: number;
  idempotencyKey: string;
}

export interface LegacyCutoverInput {
  tenantID: string;
  status: LegacyCutoverStatus;
  note: string;
  idempotencyKey: string;
}

export class MigrationClient {
  constructor(private readonly transport: AeroIdTransport) {}

  async compare(input: LegacyCompareInput): Promise<JsonRecord> {
    const idempotencyKey = input.idempotencyKey.trim();
    if (!idempotencyKey) throw new Error("请求需要 Idempotency-Key");
    return dataOf(
      await this.transport.request("POST", "/admin/migration/legacy/compare", {
        idempotencyKey,
        body: {
          owner_uid: input.ownerUID,
          source: input.source,
          limit: input.limit,
        },
        timeoutMs: 30_000,
      }),
    );
  }

  async getCutover(tenantID: string): Promise<JsonRecord> {
    return dataOf(
      await this.transport.request(
        "GET",
        `/admin/migration/legacy/cutover/${encodeURIComponent(tenantID)}`,
      ),
    );
  }

  async getCleanupReadiness(tenantID: string): Promise<JsonRecord> {
    return dataOf(
      await this.transport.request(
        "GET",
        `/admin/migration/legacy/cleanup-readiness/${encodeURIComponent(tenantID)}`,
        { timeoutMs: 30_000 },
      ),
    );
  }

  async setCutover(input: LegacyCutoverInput): Promise<JsonRecord> {
    return dataOf(
      await this.transport.request("POST", "/admin/migration/legacy/cutover", {
        idempotencyKey: input.idempotencyKey,
        body: {
          tenant_id: input.tenantID,
          status: input.status,
          note: input.note,
        },
      }),
    );
  }

  async backfillLegacyIdentities(
    limit: number,
    idempotencyKey: string,
  ): Promise<JsonRecord> {
    return dataOf(
      await this.transport.request(
        "POST",
        "/admin/accounts/backfill/legacy-identities",
        {
          idempotencyKey,
          body: { limit },
          timeoutMs: 60_000,
        },
      ),
    );
  }
}
