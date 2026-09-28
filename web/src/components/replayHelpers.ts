import type {
  ReplayCommand,
  ReplayCreateInput,
  ProjectionReplaySource,
} from "../api/replay";
import type { JsonRecord } from "../types";

export interface ReplayCommandDraft {
  id: string;
  source: ReplayCommand["source"];
  action: ReplayCommand["action"];
  payload: string;
}

export const replaySources: Array<{ value: string; label: string }> = [
  { value: "snaplink", label: "snaplink" },
  { value: "aero-im", label: "aero-im" },
  { value: "aero-vault", label: "aero-vault" },
];
export const replayActions: Array<{ value: string; label: string }> = [
  "sync",
  "summary",
  "source.summary",
  "erase",
  "delete",
  "source.erase",
].map((value) => ({ value, label: value }));

export function newReplayCommand(): ReplayCommandDraft {
  return {
    id: crypto.randomUUID(),
    source: "aero-im",
    action: "summary",
    payload: "{}",
  };
}

export function parseReplayCommands(
  drafts: ReplayCommandDraft[],
): ReplayCommand[] {
  return drafts.map((draft, index) => {
    let payload: unknown;
    try {
      payload = JSON.parse(draft.payload || "{}");
    } catch {
      throw new Error(`第 ${index + 1} 个命令的 Payload 不是有效 JSON`);
    }
    if (!payload || typeof payload !== "object" || Array.isArray(payload))
      throw new Error(`第 ${index + 1} 个命令的 Payload 必须是 JSON 对象`);
    return {
      source: draft.source,
      action: draft.action,
      payload: payload as JsonRecord,
    };
  });
}

export function controlledReplaySignature(input: ReplayCreateInput): string {
  return JSON.stringify({
    mode: input.mode,
    accountID: input.accountID,
    operationID: input.operationID,
    from: input.from,
    to: input.to,
    cursor: input.cursor,
    limit: input.limit,
    commands: input.commands,
  });
}

export function nextReplayIdempotencyKey(
  input: ReplayCreateInput,
  currentKey: string,
  makeKey: () => string = () => crypto.randomUUID(),
): string {
  return input.mode === "controlled" && input.idempotencyKey === currentKey
    ? makeKey()
    : currentKey;
}

export function replayContinuation(
  input: ReplayCreateInput | undefined,
  cursor: string,
): ReplayCreateInput {
  if (!input || input.mode === "controlled")
    throw new Error("只有 Forensic 或 Dry-run 结果可以继续读取下一窗口");
  return { ...input, cursor };
}

function replayTimestamp(value: string, label: string): string | undefined {
  const normalized = value.trim();
  if (!normalized) return undefined;
  const timestamp = new Date(normalized);
  if (Number.isNaN(timestamp.getTime())) throw new Error(`${label} 时间无效`);
  return timestamp.toISOString();
}

export function replayTimeWindow(
  fromValue: string,
  toValue: string,
): { from?: string; to?: string } {
  const from = replayTimestamp(fromValue, "开始");
  const to = replayTimestamp(toValue, "结束");
  if (from && to && Date.parse(from) >= Date.parse(to))
    throw new Error("Replay 开始时间必须早于结束时间");
  return { from, to };
}

export type ProjectionReplayOutcome =
  "partial" | "divergent" | "matched" | "empty";
export function records(value: unknown): JsonRecord[] {
  return Array.isArray(value)
    ? value.filter(
        (item): item is JsonRecord =>
          Boolean(item) && typeof item === "object" && !Array.isArray(item),
      )
    : [];
}
export function projectionReplayOutcome(
  result: JsonRecord,
): ProjectionReplayOutcome {
  if (result.completed !== true) return "partial";
  if (
    records(result.divergent).length ||
    records(result.shadow_only).length ||
    records(result.live_only).length
  )
    return "divergent";
  return Number(result.replayed_events) > 0 ? "matched" : "empty";
}

export const projectionSources: Array<{
  value: ProjectionReplaySource;
  label: string;
}> = [
  { value: "snaplink", label: "Snaplink" },
  { value: "aero-im", label: "Aero IM" },
  { value: "aero-vault", label: "Aero Vault" },
  { value: "aero-id", label: "Aero ID regional replication" },
];
