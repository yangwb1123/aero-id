import * as React from "react";
import {
  IrisAlert,
  IrisButton,
  IrisCard,
  IrisFormField,
  IrisInput,
} from "@iris-ui-kit/react";
import type { AeroIdClient } from "../api/aeroId";
import type { JsonRecord } from "../types";
import { AnchorRunDialog } from "./AnchorRunDialog";
import { RecordTable } from "./RecordTable";

function records(value: unknown): JsonRecord[] {
  if (!Array.isArray(value)) return [];
  return value.filter(
    (item): item is JsonRecord =>
      Boolean(item) && typeof item === "object" && !Array.isArray(item),
  );
}

export function AuditAnchorsPanel({
  client,
}: {
  client: AeroIdClient;
}): React.ReactElement {
  const [source, setSource] = React.useState("");
  const [partition, setPartition] = React.useState("");
  const [manifest, setManifest] = React.useState<JsonRecord>();
  const [querying, setQuerying] = React.useState(false);
  const [queryError, setQueryError] = React.useState<Error>();
  const [runKey, setRunKey] = React.useState<string>();
  const [confirmation, setConfirmation] = React.useState("");
  const [running, setRunning] = React.useState(false);
  const [runError, setRunError] = React.useState<Error>();
  const [runResult, setRunResult] = React.useState<JsonRecord>();

  const queryManifest = async () => {
    const requestedSource = source.trim();
    const requestedPartition = partition.trim();
    if (!requestedSource || !requestedPartition) {
      setManifest(undefined);
      setQueryError(new Error("Source 和 Partition 均不能为空"));
      return;
    }
    setQuerying(true);
    setManifest(undefined);
    setQueryError(undefined);
    try {
      setManifest(
        await client.compliance.getAuditAnchorManifest(
          requestedSource,
          requestedPartition,
        ),
      );
    } catch (cause) {
      setQueryError(
        cause instanceof Error ? cause : new Error("Manifest 查询失败"),
      );
    } finally {
      setQuerying(false);
    }
  };

  const openRun = () => {
    setConfirmation("");
    setRunError(undefined);
    setRunResult(undefined);
    setRunKey(crypto.randomUUID());
  };
  const runAnchors = async () => {
    if (!runKey) return;
    setRunning(true);
    setRunError(undefined);
    try {
      setRunResult(await client.compliance.runAuditAnchors(runKey));
      setRunKey(undefined);
      setConfirmation("");
    } catch (cause) {
      setRunError(
        cause instanceof Error ? cause : new Error("WORM Anchor 执行失败"),
      );
    } finally {
      setRunning(false);
    }
  };

  const chains = records(runResult?.chains);
  return (
    <>
      <IrisCard variant="outline" header="WORM Anchor 与 Manifest">
        <IrisAlert tone="info" title="不可变归档证据">
          Anchor 执行会验证每条来源分区链并写入链式 Manifest；Manifest
          查询属于敏感证据读取，成功返回前会持久化审计事实。
        </IrisAlert>
        <div className="detail-actions">
          <IrisButton variant="outline" onClick={openRun}>
            锚定全部当前审计链
          </IrisButton>
        </div>
        {runResult ? (
          <IrisAlert tone="success">
            本次已写入 {String(runResult.anchored ?? chains.length)} 个锚点。
          </IrisAlert>
        ) : null}
        {chains.length > 0 ? (
          <RecordTable
            records={chains}
            columns={[
              { key: "source", label: "Source" },
              { key: "partition", label: "Partition" },
              { key: "sequence", label: "链序号" },
              { key: "record_hash", label: "Record Hash" },
              { key: "anchor_key", label: "归档对象" },
            ]}
          />
        ) : null}
        <h3 className="detail-heading">查询单链最新 Manifest</h3>
        <form
          className="anchor-query-grid"
          onSubmit={(event) => {
            event.preventDefault();
            void queryManifest();
          }}
        >
          <IrisFormField label="Source" required>
            <IrisInput
              required
              value={source}
              placeholder="aero-id"
              onChange={(event) => setSource(event.target.value)}
            />
          </IrisFormField>
          <IrisFormField label="Partition" required>
            <IrisInput
              required
              value={partition}
              placeholder="tenant:<tenant-id>:system"
              onChange={(event) => setPartition(event.target.value)}
            />
          </IrisFormField>
          <IrisButton type="submit" loading={querying}>
            查询并记录访问审计
          </IrisButton>
        </form>
        {queryError ? (
          <IrisAlert tone="danger">{queryError.message}</IrisAlert>
        ) : null}
        {manifest ? (
          <RecordTable
            records={[manifest]}
            columns={[
              { key: "source", label: "Source" },
              { key: "source_region", label: "来源区域" },
              { key: "partition", label: "Partition" },
              { key: "chain_sequence", label: "链序号" },
              { key: "record_hash", label: "Record Hash" },
              { key: "signature_version", label: "签名版本" },
              { key: "previous_anchor_hash", label: "前序 Anchor Hash" },
              { key: "anchor_hash", label: "Anchor Hash" },
              { key: "anchored_at", label: "锚定时间", kind: "date" },
            ]}
          />
        ) : null}
      </IrisCard>
      <AnchorRunDialog
        idempotencyKey={runKey}
        confirmation={confirmation}
        running={running}
        error={runError}
        setConfirmation={setConfirmation}
        cancel={() => setRunKey(undefined)}
        confirm={() => void runAnchors()}
      />
    </>
  );
}
