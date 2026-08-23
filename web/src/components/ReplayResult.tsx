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
  continueWindow,
  continuing,
}: {
  client: AeroIdClient;
  result: JsonRecord;
  clear(): void;
  continueWindow(cursor: string): void;
  continuing: boolean;
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
            {!controlled && result.next_cursor ? (
              <IrisButton
                size="sm"
                variant="outline"
                loading={continuing}
                onClick={() => continueWindow(String(result.next_cursor))}
              >
                手动读取下一窗口
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
            当前仅展示一个有界窗口。继续时会原样回传游标、重新鉴权并写入新的审计读取事实；页面不会自动全量扫描。
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
