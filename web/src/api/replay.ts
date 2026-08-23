import type { JsonRecord } from "../types";
import { AeroIdTransport } from "./transport";

export type ReplayMode = "forensic" | "dry-run" | "controlled";
export type ProjectionReplaySource =
  "snaplink" | "aero-im" | "aero-vault" | "aero-id";

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
  cursor?: string;
  limit: number;
  idempotencyKey?: string;
  confirmed: boolean;
  commands: ReplayCommand[];
}

export interface ProjectionReplayInput {
  accountID: string;
  source: ProjectionReplaySource;
  cursor?: string;
  limit: number;
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
          cursor: input.cursor,
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

  async replayProjection(input: ProjectionReplayInput): Promise<JsonRecord> {
    return dataOf(
      await this.transport.request("POST", "/audit/replays/projection", {
        body: {
          account_id: input.accountID,
          source: input.source,
          cursor: input.cursor,
          limit: input.limit,
        },
        timeoutMs: 60_000,
      }),
    );
  }
}
