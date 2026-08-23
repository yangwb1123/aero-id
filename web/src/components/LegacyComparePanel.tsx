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
import type { LegacySource } from "../api/migration";
import type { JsonRecord } from "../types";
import { RecordTable } from "./RecordTable";

const sourceItems = [
  { value: "aero-vault", label: "Aero Vault（legacy files）" },
  { value: "aero-im", label: "Aero IM（legacy messages/comments）" },
];

function record(value: unknown): JsonRecord {
  return value && typeof value === "object" && !Array.isArray(value)
    ? (value as JsonRecord)
    : {};
}

function strings(value: unknown): string[] {
  return Array.isArray(value)
    ? value.filter((item): item is string => typeof item === "string")
    : [];
}

function digestRows(result?: JsonRecord): JsonRecord[] {
  const legacy = record(result?.legacy);
  return ["files", "messages", "comments"].flatMap((dataset) => {
    const digest = record(legacy[dataset]);
    return Object.keys(digest).length > 0 ? [{ dataset, ...digest }] : [];
  });
}

function counterRows(result?: JsonRecord): JsonRecord[] {
  return Object.entries(record(result?.source_counters)).map(
    ([counter, value]) => ({ counter, value }),
  );
}

export function LegacyComparePanel({
  client,
}: {
  client: AeroIdClient;
}): React.ReactElement {
  const [ownerUID, setOwnerUID] = React.useState("");
  const [source, setSource] = React.useState<LegacySource>("aero-vault");
  const [limit, setLimit] = React.useState(1000);
  const [running, setRunning] = React.useState(false);
  const [error, setError] = React.useState<Error>();
  const [result, setResult] = React.useState<JsonRecord>();

  const compare = async () => {
    const uid = ownerUID.trim();
    setError(undefined);
    setResult(undefined);
    if (!uid) {
      setError(new Error("Legacy Owner UID 不能为空"));
      return;
    }
    if (!Number.isInteger(limit) || limit < 1 || limit > 5000) {
      setError(new Error("Compare limit 必须是 1–5000 的整数"));
      return;
    }
    setRunning(true);
    try {
      setResult(
        await client.migration.compare({ ownerUID: uid, source, limit }),
      );
    } catch (cause) {
      setError(
        cause instanceof Error ? cause : new Error("Shadow compare 失败"),
      );
    } finally {
      setRunning(false);
    }
  };

  const drift = strings(result?.drift);
  const digests = digestRows(result);
  const counters = counterRows(result);
  return (
    <IrisCard variant="outline" header="Shadow Migration Compare">
      <form
        className="migration-compare-grid"
        onSubmit={(event) => {
          event.preventDefault();
          void compare();
        }}
      >
        <IrisFormField label="Legacy Owner UID" required>
          <IrisInput
            required
            value={ownerUID}
            onChange={(event) => setOwnerUID(event.target.value)}
          />
        </IrisFormField>
        <IrisFormField label="目标权威来源">
          <IrisSelect
            items={sourceItems}
            value={source}
            onValueChange={(value) => setSource(String(value) as LegacySource)}
          />
        </IrisFormField>
        <IrisFormField label="单次 Digest 上限" hint="默认 1000，硬上限 5000">
          <IrisInput
            type="number"
            min={1}
            max={5000}
            value={limit}
            onChange={(event) => setLimit(Number(event.target.value))}
          />
        </IrisFormField>
        <IrisButton type="submit" loading={running}>
          运行只读 Compare
        </IrisButton>
      </form>
      {error ? <IrisAlert tone="danger">{error.message}</IrisAlert> : null}
      {result?.partial === true ? (
        <IrisAlert tone="warning" title="Legacy Digest 被批量上限截断">
          当前结果只能用于发现差异，不能据此确认迁移完整。提高有界 limit
          或拆分迁移验证范围。
        </IrisAlert>
      ) : null}
      {result && result.source_available !== true ? (
        <IrisAlert tone="warning" title="目标来源摘要不可用">
          已返回 legacy digest，但没有完成跨来源一致性判断；不要把 unavailable
          当作 matched。
        </IrisAlert>
      ) : null}
      {result?.matched === true && result.partial !== true ? (
        <IrisAlert tone="success">当前有界范围内的来源计数匹配。</IrisAlert>
      ) : null}
      {drift.length > 0 ? (
        <IrisAlert tone="warning" title="发现迁移漂移">
          {drift.join("；")}
        </IrisAlert>
      ) : null}
      {digests.length > 0 ? (
        <>
          <h3 className="detail-heading">Content-free Legacy Digests</h3>
          <RecordTable
            records={digests}
            columns={[
              { key: "dataset", label: "Legacy Dataset" },
              { key: "count", label: "计数" },
              { key: "size_bytes", label: "大小（bytes）" },
              { key: "truncated", label: "已截断" },
              { key: "digest", label: "Digest" },
            ]}
          />
        </>
      ) : null}
      {counters.length > 0 ? (
        <>
          <h3 className="detail-heading">Source Summary Counters</h3>
          <RecordTable
            records={counters}
            columns={[
              { key: "counter", label: "Counter" },
              { key: "value", label: "值" },
            ]}
          />
        </>
      ) : null}
    </IrisCard>
  );
}
