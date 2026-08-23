import * as React from "react";
import { IrisAlert, IrisButton, IrisCard } from "@iris-ui-kit/react";
import type { AeroIdClient } from "../api/aeroId";
import type { JsonRecord } from "../types";
import { RecordTable } from "./RecordTable";

function jsonPreview(value: unknown): string {
  return JSON.stringify(value ?? {}, null, 2);
}

export function AuditEventDetails({
  client,
  event,
  close,
}: {
  client: AeroIdClient;
  event: JsonRecord;
  close(): void;
}): React.ReactElement {
  const source = typeof event.source === "string" ? event.source : "";
  const partition =
    typeof event.partition_key === "string" ? event.partition_key : "";
  const [verifying, setVerifying] = React.useState(false);
  const [verifyError, setVerifyError] = React.useState<Error>();
  const [verified, setVerified] = React.useState(false);
  const verify = async () => {
    setVerifying(true);
    setVerifyError(undefined);
    setVerified(false);
    try {
      const result = await client.verifyAuditChain(source, partition);
      setVerified(result.verified === true);
      if (result.verified !== true) throw new Error("服务端未确认审计链完整");
    } catch (reason) {
      setVerifyError(
        reason instanceof Error ? reason : new Error("审计链验证失败"),
      );
    } finally {
      setVerifying(false);
    }
  };
  return (
    <IrisCard
      variant="outline"
      header={`审计事实 · ${String(event.event_id ?? event.id ?? "")}`}
      footer={
        <div className="detail-actions">
          <IrisButton size="sm" variant="ghost" onClick={close}>
            关闭
          </IrisButton>
          <IrisButton
            size="sm"
            variant="outline"
            loading={verifying}
            disabled={!source || !partition}
            onClick={() => void verify()}
          >
            验证所属分区链
          </IrisButton>
        </div>
      }
    >
      {verified ? (
        <IrisAlert tone="success" title="审计链验证通过">
          aero-id 已重新计算并验证 {source} / {partition} 分区的版本化哈希链。
        </IrisAlert>
      ) : null}
      {verifyError ? (
        <IrisAlert tone="danger" title="审计链验证失败">
          {verifyError.message}
        </IrisAlert>
      ) : null}
      <RecordTable
        records={[event]}
        columns={[
          { key: "action", label: "动作" },
          { key: "source", label: "来源" },
          { key: "source_region", label: "来源区域" },
          { key: "partition_key", label: "分区" },
          { key: "chain_sequence", label: "链序号" },
          { key: "occurred_at", label: "发生时间", kind: "date" },
        ]}
      />
      <h3 className="detail-heading">关联标识</h3>
      <RecordTable
        records={[event]}
        columns={[
          { key: "operation_id", label: "Operation ID" },
          { key: "event_id", label: "Event ID" },
          { key: "causation_id", label: "Causation ID" },
          { key: "request_id", label: "Request ID" },
          { key: "trace_id", label: "Trace ID" },
          { key: "account_id", label: "Account ID" },
        ]}
      />
      <h3 className="detail-heading">防篡改元数据</h3>
      <RecordTable
        records={[event]}
        columns={[
          { key: "hash_version", label: "Hash 版本" },
          { key: "payload_hash", label: "Payload Hash" },
          { key: "previous_hash", label: "Previous Hash" },
          { key: "record_hash", label: "Record Hash" },
          { key: "signature_version", label: "签名版本" },
          { key: "signature", label: "签名" },
        ]}
      />
      <h3 className="detail-heading">脱敏 Payload</h3>
      <pre className="json-preview">{jsonPreview(event.payload)}</pre>
    </IrisCard>
  );
}

export function auditEventID(record: JsonRecord): string | undefined {
  const value = record.event_id ?? record.id;
  return typeof value === "string" && value ? value : undefined;
}
