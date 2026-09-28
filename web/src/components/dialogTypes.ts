import type { AuditHoldInput, AuditHoldScope } from "../api/compliance";
import type { LegacyCutoverStatus } from "../api/migration";

export const anchorRunConfirmation = "ANCHOR ALL CHAINS";
export const backfillConfirmation = "BACKFILL LEGACY IDENTITIES";

export type ComplianceCommand =
  | { kind: "put"; input: AuditHoldInput }
  | { kind: "remove"; scopeType: AuditHoldScope; scopeID: string }
  | { kind: "export" };

export type MigrationCommand =
  | { kind: "backfill"; limit: number; idempotencyKey: string }
  | {
      kind: "cutover";
      tenantID: string;
      status: LegacyCutoverStatus;
      note: string;
      idempotencyKey: string;
    };

export type CallbackCommand =
  | { kind: "delete"; id: string; endpoint: string }
  | { kind: "replay"; id: string; idempotencyKey: string };
export interface CallbackSecret {
  subscriptionID: string;
  value: string;
}
