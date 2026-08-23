import * as React from "react";
import {
  IrisAlert,
  IrisButton,
  IrisCard,
  IrisFormField,
  IrisInput,
  IrisSelect,
} from "@iris-ui-kit/react";
import type { AeroIdClient } from "../api/aeroId";
import type { ProjectionReplaySource } from "../api/replay";
import type { JsonRecord } from "../types";
import { RecordTable } from "./RecordTable";

const sourceItems = [
  { value: "snaplink", label: "Snaplink" },
  { value: "aero-im", label: "Aero IM" },
  { value: "aero-vault", label: "Aero Vault" },
  { value: "aero-id", label: "Aero ID regional replication" },
];

function records(value: unknown): JsonRecord[] {
  return Array.isArray(value)
    ? value.filter(
        (item): item is JsonRecord =>
          Boolean(item) && typeof item === "object" && !Array.isArray(item),
      )
    : [];
}

export type ProjectionReplayOutcome =
  "partial" | "divergent" | "matched" | "empty";

export function projectionReplayOutcome(
  result: JsonRecord,
): ProjectionReplayOutcome {
  if (result.completed !== true) return "partial";
  if (
    records(result.divergent).length > 0 ||
    records(result.shadow_only).length > 0 ||
    records(result.live_only).length > 0
  )
    return "divergent";
  return Number(result.replayed_events) > 0 ? "matched" : "empty";
}

function ProjectionDatasetTable({
  title,
  rows,
}: {
  title: string;
  rows: JsonRecord[];
}): React.ReactElement | null {
  if (rows.length === 0) return null;
  return (
    <>
      <h3 className="detail-heading">{title}</h3>
      <RecordTable
        records={rows}
        columns={[
          { key: "dataset", label: "Dataset" },
          { key: "status", label: "Shadow 状态", kind: "status" },
          { key: "version", label: "Shadow Version" },
          { key: "live_version", label: "Live Version" },
          { key: "digest", label: "Shadow Digest" },
          { key: "live_digest", label: "Live Digest" },
          { key: "generated_at", label: "生成时间", kind: "date" },
        ]}
      />
    </>
  );
}

function ProjectionReplayWindow({
  result,
  inputCursor,
  running,
  clear,
  continueWith,
}: {
  result: JsonRecord;
  inputCursor?: string;
  running: boolean;
  clear(): void;
  continueWith(cursor: string): void;
}): React.ReactElement {
  const matched = records(result.matched);
  const divergent = records(result.divergent);
  const shadowOnly = records(result.shadow_only);
  const liveOnly = records(result.live_only);
  const nextCursor =
    typeof result.next_cursor === "string" ? result.next_cursor : undefined;
  const outcome = projectionReplayOutcome(result);
  const windowHasDifferences =
    divergent.length > 0 || shadowOnly.length > 0 || liveOnly.length > 0;
  return (
    <>
      <RecordTable
        records={[{ ...result, input_cursor: inputCursor ?? "first" }]}
        columns={[
          { key: "account_id", label: "Account ID" },
          { key: "source", label: "Source" },
          { key: "input_cursor", label: "本窗口起点" },
          { key: "replayed_events", label: "重放事件数" },
          { key: "completed", label: "已到历史末尾" },
          { key: "next_cursor", label: "下一游标" },
        ]}
      />
      {outcome === "partial" ? (
        <IrisAlert tone="warning" title="结果只覆盖当前事件窗口">
          当前分类不是完整账户历史结论。可手动读取下一窗口；禁止自动全量扫描或把窗口内
          matched 当作全局一致。
        </IrisAlert>
      ) : null}
      {windowHasDifferences ? (
        <IrisAlert tone="danger" title="当前窗口发现 Projection 差异">
          未完成窗口中的差异不是全局结论。只读 Replay 不会自动修复；请结合
          source version、后续窗口、事件缺口和审计事实启动受控 reconciliation。
        </IrisAlert>
      ) : null}
      {outcome === "matched" ? (
        <IrisAlert tone="success">
          本次有界重放已到历史末尾，未发现 shadow/live 差异。
        </IrisAlert>
      ) : null}
      {outcome === "empty" ? (
        <IrisAlert tone="warning">
          当前来源没有可重放事件；这不是 live projection 正确性的证明。
        </IrisAlert>
      ) : null}
      <ProjectionDatasetTable title="Matched" rows={matched} />
      <ProjectionDatasetTable title="Divergent" rows={divergent} />
      <ProjectionDatasetTable title="Shadow only" rows={shadowOnly} />
      <ProjectionDatasetTable title="Live only" rows={liveOnly} />
      <div className="detail-actions">
        <IrisButton variant="ghost" onClick={clear}>
          清除窗口结果
        </IrisButton>
        {nextCursor ? (
          <IrisButton
            variant="outline"
            loading={running}
            onClick={() => continueWith(nextCursor)}
          >
            手动读取下一窗口
          </IrisButton>
        ) : null}
      </div>
    </>
  );
}

export function ProjectionReplayPanel({
  client,
}: {
  client: AeroIdClient;
}): React.ReactElement {
  const [accountID, setAccountID] = React.useState("");
  const [source, setSource] =
    React.useState<ProjectionReplaySource>("aero-vault");
  const [limit, setLimit] = React.useState(1000);
  const [running, setRunning] = React.useState(false);
  const [error, setError] = React.useState<Error>();
  const [result, setResult] = React.useState<JsonRecord>();
  const [inputCursor, setInputCursor] = React.useState<string>();

  const resetWindow = () => {
    setResult(undefined);
    setInputCursor(undefined);
    setError(undefined);
  };
  const run = async (cursor?: string) => {
    const id = accountID.trim();
    setError(undefined);
    if (!id) {
      setError(new Error("Account ID 不能为空"));
      return;
    }
    if (!Number.isInteger(limit) || limit < 1 || limit > 10000) {
      setError(new Error("Projection Replay limit 必须是 1–10000 的整数"));
      return;
    }
    setRunning(true);
    try {
      setResult(
        await client.replays.replayProjection({
          accountID: id,
          source,
          cursor,
          limit,
        }),
      );
      setInputCursor(cursor);
    } catch (reason) {
      setError(
        reason instanceof Error
          ? reason
          : new Error("Shadow Projection Replay 失败"),
      );
    } finally {
      setRunning(false);
    }
  };

  return (
    <IrisCard variant="outline" header="Shadow Projection Replay">
      <IrisAlert tone="info" title="只读、有界、逐窗口校验">
        从当前租户的脱敏事件存储重建 shadow projection，并与 live projection
        比较；不会写入或修复 live
        数据。每次读取均独立审计，页面不会自动追赶全部历史。
      </IrisAlert>
      <form
        className="projection-replay-grid"
        onSubmit={(event) => {
          event.preventDefault();
          void run();
        }}
      >
        <IrisFormField label="Account ID" required>
          <IrisInput
            required
            value={accountID}
            onChange={(event) => {
              setAccountID(event.target.value);
              resetWindow();
            }}
          />
        </IrisFormField>
        <IrisFormField label="Event Source">
          <IrisSelect
            items={sourceItems}
            value={source}
            onValueChange={(value) => {
              setSource(String(value) as ProjectionReplaySource);
              resetWindow();
            }}
          />
        </IrisFormField>
        <IrisFormField label="单窗口事件上限" hint="默认 1000，硬上限 10000">
          <IrisInput
            type="number"
            min={1}
            max={10000}
            value={limit}
            onChange={(event) => {
              setLimit(Number(event.target.value));
              resetWindow();
            }}
          />
        </IrisFormField>
        <IrisButton type="submit" loading={running}>
          重放首个有界窗口
        </IrisButton>
      </form>
      {error ? <IrisAlert tone="danger">{error.message}</IrisAlert> : null}
      {result ? (
        <ProjectionReplayWindow
          result={result}
          inputCursor={inputCursor}
          running={running}
          clear={resetWindow}
          continueWith={(cursor) => void run(cursor)}
        />
      ) : null}
    </IrisCard>
  );
}
