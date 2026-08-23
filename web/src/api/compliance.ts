import type { JsonRecord } from "../types";
import { AeroIdTransport } from "./transport";

export type AuditHoldScope = "tenant" | "account" | "actor";

export interface AuditHoldInput {
  scopeType: AuditHoldScope;
  scopeID: string;
  reason: string;
  expiresAt?: string;
}

function isRecord(value: unknown): value is JsonRecord {
  return Boolean(value) && typeof value === "object" && !Array.isArray(value);
}

function dataOf(value: unknown): JsonRecord {
  if (!isRecord(value) || !isRecord(value.data)) return {};
  return value.data;
}

function holds(value: unknown): JsonRecord[] {
  return Array.isArray(value) ? value.filter(isRecord) : [];
}

export class ComplianceClient {
  constructor(private readonly transport: AeroIdTransport) {}

  async listHolds(): Promise<JsonRecord[]> {
    const data = dataOf(
      await this.transport.request("GET", "/admin/audit/holds"),
    );
    return holds(data.holds);
  }

  async putHold(input: AuditHoldInput): Promise<JsonRecord> {
    return dataOf(
      await this.transport.request("POST", "/admin/audit/holds", {
        body: {
          scope_type: input.scopeType,
          scope_id: input.scopeID,
          reason: input.reason,
          expires_at: input.expiresAt,
        },
      }),
    );
  }

  async removeHold(scopeType: AuditHoldScope, scopeID: string): Promise<void> {
    await this.transport.request(
      "DELETE",
      `/admin/audit/holds/${encodeURIComponent(scopeType)}/${encodeURIComponent(scopeID)}`,
    );
  }

  async createAuditExport(): Promise<JsonRecord> {
    const key = crypto.randomUUID();
    return dataOf(
      await this.transport.request("POST", "/audit/export", {
        idempotencyKey: key,
        body: { idempotency_key: key },
      }),
    );
  }

  async runAuditAnchors(idempotencyKey: string): Promise<JsonRecord> {
    return dataOf(
      await this.transport.request("POST", "/admin/audit/anchor", {
        idempotencyKey,
        timeoutMs: 60_000,
      }),
    );
  }

  async getAuditAnchorManifest(
    source: string,
    partition: string,
  ): Promise<JsonRecord> {
    return dataOf(
      await this.transport.request(
        "GET",
        `/admin/audit/anchor/manifest/${encodeURIComponent(source)}/${encodeURIComponent(partition)}`,
        { timeoutMs: 30_000 },
      ),
    );
  }
}
