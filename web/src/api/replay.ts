import type { JsonRecord } from "../types";
import { AeroIdTransport } from "./transport";

export type ReplayMode = "forensic" | "dry-run" | "controlled";

export interface ReplayCommand {
  source: "snaplink" | "aero-im" | "aero-vault";
  action:
    "sync" | "summary" | "source.summary" | "erase" | "delete" | "source.erase";
  payload: JsonRecord;
}

export interface ReplayCreateInput {
  mode: ReplayMode;
  accountID?: string;
  operationID?: string;
  limit: number;
  idempotencyKey?: string;
  confirmed: boolean;
  commands: ReplayCommand[];
}

function isRecord(value: unknown): value is JsonRecord {
  return Boolean(value) && typeof value === "object" && !Array.isArray(value);
}

function dataOf(value: unknown): JsonRecord {
  if (!isRecord(value) || !isRecord(value.data)) return {};
  return value.data;
}

export class ReplayClient {
  constructor(private readonly transport: AeroIdTransport) {}

  async create(input: ReplayCreateInput): Promise<JsonRecord> {
    return dataOf(
      await this.transport.request("POST", "/audit/replays", {
        idempotencyKey:
          input.mode === "controlled" ? input.idempotencyKey : undefined,
        body: {
          mode: input.mode,
          account_id: input.accountID,
          operation_id: input.operationID,
          limit: input.limit,
          idempotency_key: input.idempotencyKey,
          confirmed: input.confirmed,
          commands: input.commands,
        },
        timeoutMs: 30_000,
      }),
    );
  }

  async get(id: string): Promise<JsonRecord> {
    return dataOf(
      await this.transport.request(
        "GET",
        `/audit/replays/${encodeURIComponent(id)}`,
      ),
    );
  }
}
