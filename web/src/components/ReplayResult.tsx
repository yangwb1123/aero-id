import * as React from "react";
import { IrisAlert, IrisButton, IrisCard } from "@iris-ui-kit/react";
import type { AeroIdClient } from "../api/aeroId";
import type { JsonRecord } from "../types";
import { OperationDetails } from "./OperationDetails";
import { RecordTable } from "./RecordTable";

function records(value: unknown): JsonRecord[] {
  return Array.isArray(value)
    ? value.filter(
        (item): item is JsonRecord =>
          Boolean(item) && typeof item === "object" && !Array.isArray(item),
      )
    : [];
}

function jsonPreview(value: unknown): string {
  return JSON.stringify(value ?? {}, null, 2);
}

export function ReplayResult({
  client,
  result,
  clear,
}: {
  client: AeroIdClient;
  result: JsonRecord;
  clear(): void;
}): React.ReactElement {
  const replayID = String(result.replay_id ?? "");
  const controlled = result.mode === "controlled";
  const [showOperation, setShowOperation] = React.useState(controlled);
  const events = records(result.events);
  return (
    <>
      <IrisCard
        variant="outline"
        header={`Replay 结果 · ${replayID}`}
        footer={
          <div className="detail-actions">
            <IrisButton size="sm" variant="ghost" onClick={clear}>
              清除结果
            </IrisButton>
            {controlled && replayID ? (
              <IrisButton
                size="sm"
                variant="outline"
                onClick={() => setShowOperation(true)}
              >
                查看 Operation 时间线
              </IrisButton>
            ) : null}
          </div>
        }
      >
        <RecordTable
          records={[result]}
          columns={[
            { key: "mode", label: "模式" },
            { key: "status", label: "状态", kind: "status" },
            { key: "replay_id", label: "Replay ID" },
            { key: "next_cursor", label: "下一游标" },
          ]}
        />
        {result.next_cursor ? (
          <IrisAlert tone="warning" title="重建结果仍有后续事实">
            当前 Replay API 返回了下一游标，但没有继续 Replay
            的路由；请使用审计台账按相同作用域继续 keyset 查询。
          </IrisAlert>
        ) : null}
        {result.simulation ? (
          <>
            <h3 className="detail-heading">Dry-run 模拟结果</h3>
            <pre className="json-preview">{jsonPreview(result.simulation)}</pre>
          </>
        ) : null}
        {events.length ? (
          <>
            <h3 className="detail-heading">重建的脱敏事实</h3>
            <RecordTable
              records={events}
              columns={[
                { key: "occurred_at", label: "发生时间", kind: "date" },
                { key: "action", label: "动作" },
                { key: "source", label: "来源" },
                { key: "resource_type", label: "资源类型" },
                { key: "resource_id", label: "资源 ID" },
                { key: "event_id", label: "Event ID" },
              ]}
            />
          </>
        ) : null}
      </IrisCard>
      {controlled && replayID && showOperation ? (
        <OperationDetails
          client={client}
          operationID={replayID}
          close={() => setShowOperation(false)}
          changed={() => undefined}
        />
      ) : null}
    </>
  );
}
