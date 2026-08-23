import * as React from "react";
import {
  IrisAlert,
  IrisBadge,
  IrisButton,
  IrisCard,
  IrisFormField,
  IrisInput,
  IrisSelect,
  IrisTextarea,
} from "@iris-ui-kit/react";
import type { AeroIdClient } from "../api/aeroId";
import type { LegacyCutoverStatus } from "../api/migration";
import type { JsonRecord } from "../types";
import {
  MigrationCommandDialog,
  type MigrationCommand,
} from "./MigrationCommandDialog";
import { RecordTable } from "./RecordTable";

const statusLabels: Record<LegacyCutoverStatus, string> = {
  legacy: "Legacy serving",
  shadow: "Shadow comparison",
  cut_over: "Cut over",
  rolled_back: "Rolled back",
};

export function cutoverTransitions(
  status: LegacyCutoverStatus,
): LegacyCutoverStatus[] {
  if (status === "legacy") return ["shadow"];
  if (status === "shadow") return ["cut_over"];
  if (status === "cut_over") return ["rolled_back"];
  return ["legacy", "shadow"];
}

function cutoverStatus(value: unknown): LegacyCutoverStatus | undefined {
  return value === "legacy" ||
    value === "shadow" ||
    value === "cut_over" ||
    value === "rolled_back"
    ? value
    : undefined;
}

function records(value: unknown): JsonRecord[] {
  if (!Array.isArray(value)) return [];
  return value.filter(
    (item): item is JsonRecord =>
      Boolean(item) && typeof item === "object" && !Array.isArray(item),
  );
}

function statusTone(
  status: LegacyCutoverStatus,
): "neutral" | "primary" | "warning" | "danger" {
  if (status === "shadow") return "primary";
  if (status === "cut_over") return "warning";
  if (status === "rolled_back") return "danger";
  return "neutral";
}

interface TenantInspection {
  tenantID: string;
  status: LegacyCutoverStatus;
  readiness: JsonRecord;
}

export function LegacyCutoverPanel({
  client,
}: {
  client: AeroIdClient;
}): React.ReactElement {
  const [tenantID, setTenantID] = React.useState("");
  const [inspection, setInspection] = React.useState<TenantInspection>();
  const [nextStatus, setNextStatus] = React.useState<LegacyCutoverStatus>();
  const [note, setNote] = React.useState("");
  const [inspecting, setInspecting] = React.useState(false);
  const [command, setCommand] = React.useState<MigrationCommand>();
  const [confirmation, setConfirmation] = React.useState("");
  const [running, setRunning] = React.useState(false);
  const [error, setError] = React.useState<Error>();
  const [message, setMessage] = React.useState<string>();

  const inspect = async (requestedTenant = tenantID.trim()) => {
    if (!requestedTenant) {
      setError(new Error("Tenant ID 不能为空"));
      setInspection(undefined);
      return;
    }
    setInspecting(true);
    setError(undefined);
    try {
      const [cutover, readiness] = await Promise.all([
        client.migration.getCutover(requestedTenant),
        client.migration.getCleanupReadiness(requestedTenant),
      ]);
      const status = cutoverStatus(cutover.status);
      if (!status) throw new Error("服务端返回了未知 Cutover 状态");
      setInspection({ tenantID: requestedTenant, status, readiness });
      setNextStatus(cutoverTransitions(status)[0]);
    } catch (cause) {
      setInspection(undefined);
      setError(
        cause instanceof Error ? cause : new Error("租户迁移状态读取失败"),
      );
    } finally {
      setInspecting(false);
    }
  };

  const prepare = () => {
    setError(undefined);
    setMessage(undefined);
    const trimmedNote = note.trim();
    if (!inspection || inspection.tenantID !== tenantID.trim() || !nextStatus) {
      setError(new Error("请先重新读取当前 Tenant Cutover 状态"));
      return;
    }
    if (!cutoverTransitions(inspection.status).includes(nextStatus)) {
      setError(new Error("所选状态不是当前状态允许的下一跳"));
      return;
    }
    if (!trimmedNote) {
      setError(new Error("Cutover 决策必须填写变更说明"));
      return;
    }
    setConfirmation("");
    setCommand({
      kind: "cutover",
      tenantID: inspection.tenantID,
      status: nextStatus,
      note: trimmedNote,
      idempotencyKey: crypto.randomUUID(),
    });
  };

  const execute = async () => {
    if (command?.kind !== "cutover") return;
    setRunning(true);
    setError(undefined);
    try {
      await client.migration.setCutover(command);
      const changedTenant = command.tenantID;
      setMessage(
        `${changedTenant} 已切换为 ${command.status}，决策与审计事实已原子提交。`,
      );
      setCommand(undefined);
      setConfirmation("");
      setNote("");
      await inspect(changedTenant);
    } catch (cause) {
      setError(
        cause instanceof Error ? cause : new Error("Cutover 决策执行失败"),
      );
    } finally {
      setRunning(false);
    }
  };

  const checks = records(inspection?.readiness.checks).map((item) => ({
    ...item,
    status: item.ready === true ? "ready" : "blocked",
  }));
  const transitionItems = inspection
    ? cutoverTransitions(inspection.status).map((status) => ({
        value: status,
        label: statusLabels[status],
      }))
    : [];

  return (
    <>
      <IrisCard variant="outline" header="Per-tenant Legacy Cutover">
        <form
          className="migration-inspect-grid"
          onSubmit={(event) => {
            event.preventDefault();
            void inspect();
          }}
        >
          <IrisFormField label="Tenant ID" required>
            <IrisInput
              required
              value={tenantID}
              onChange={(event) => {
                setTenantID(event.target.value);
                setInspection(undefined);
              }}
            />
          </IrisFormField>
          <IrisButton type="submit" loading={inspecting}>
            读取状态与 Cleanup Readiness
          </IrisButton>
        </form>
        {message ? <IrisAlert tone="success">{message}</IrisAlert> : null}
        {error && !command ? (
          <IrisAlert tone="danger">{error.message}</IrisAlert>
        ) : null}
        {inspection ? (
          <>
            <div className="status-row">
              <span>当前状态</span>
              <IrisBadge tone={statusTone(inspection.status)}>
                {statusLabels[inspection.status]}
              </IrisBadge>
            </div>
            <IrisAlert
              tone={inspection.readiness.ready === true ? "success" : "warning"}
              title="Legacy cleanup 是独立的后置门禁"
            >
              {inspection.readiness.ready === true
                ? "全部清理前置条件已满足；本页面仍不会删除任何 legacy 数据。"
                : "仍有清理条件未满足。该结果不阻止进入 shadow，但禁止据此删除 legacy 写路径或证据。"}
            </IrisAlert>
            <RecordTable
              records={checks}
              columns={[
                { key: "check", label: "检查项" },
                { key: "status", label: "状态", kind: "status" },
                { key: "details", label: "详情" },
              ]}
              empty="服务端未返回 Cleanup Readiness 检查项"
            />
            <div className="migration-cutover-grid">
              <IrisFormField label="下一状态">
                <IrisSelect
                  items={transitionItems}
                  value={nextStatus}
                  onValueChange={(value) =>
                    setNextStatus(String(value) as LegacyCutoverStatus)
                  }
                />
              </IrisFormField>
              <IrisFormField label="变更说明" required>
                <IrisTextarea
                  required
                  maxLength={1000}
                  value={note}
                  onChange={(event) => setNote(event.target.value)}
                />
              </IrisFormField>
              <IrisButton variant="danger" onClick={prepare}>
                检查并确认状态切换
              </IrisButton>
            </div>
          </>
        ) : null}
      </IrisCard>
      <MigrationCommandDialog
        command={command}
        confirmation={confirmation}
        running={running}
        error={error}
        setConfirmation={setConfirmation}
        cancel={() => setCommand(undefined)}
        confirm={() => void execute()}
      />
    </>
  );
}
