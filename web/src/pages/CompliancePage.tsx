import * as React from "react";
import {
  IrisAlert,
  IrisButton,
  IrisCard,
  IrisFormField,
  IrisInput,
  IrisSelect,
  IrisTextarea,
} from "@iris-ui-kit/react";
import type { AeroIdClient } from "../api/aeroId";
import type { AuditHoldInput, AuditHoldScope } from "../api/compliance";
import {
  ComplianceCommandDialog,
  type ComplianceCommand,
} from "../components/ComplianceCommandDialog";
import { JobDetails, syncJobID } from "../components/JobDetails";
import { PageHeader } from "../components/PageHeader";
import { PageError, PageLoading } from "../components/PageState";
import { RecordTable } from "../components/RecordTable";
import { useAsyncResource } from "../hooks/useAsyncResource";
import type { JsonRecord } from "../types";

const scopeItems = [
  { value: "tenant", label: "Tenant" },
  { value: "account", label: "Account" },
  { value: "actor", label: "Actor" },
];
const rfc3339 =
  /^(\d{4})-(0[1-9]|1[0-2])-(0[1-9]|[12]\d|3[01])T([01]\d|2[0-3]):([0-5]\d):([0-5]\d)(?:\.\d+)?(?:Z|[+-](?:[01]\d|2[0-3]):[0-5]\d)$/;

function validCalendarDate(parts: RegExpMatchArray): boolean {
  const [year, month, day, hour, minute, second] = parts
    .slice(1, 7)
    .map(Number);
  const calendar = new Date(0);
  calendar.setUTCFullYear(year, month - 1, day);
  calendar.setUTCHours(hour, minute, second, 0);
  return (
    calendar.getUTCFullYear() === year &&
    calendar.getUTCMonth() === month - 1 &&
    calendar.getUTCDate() === day
  );
}

export function normalizeHoldExpiry(
  value: string,
  now = Date.now(),
): string | undefined {
  const trimmed = value.trim();
  if (!trimmed) return undefined;
  const parts = trimmed.match(rfc3339);
  if (!parts) throw new Error("到期时间必须是带时区的 RFC3339 时间");
  if (!validCalendarDate(parts)) throw new Error("到期时间无效");
  const timestamp = Date.parse(trimmed);
  if (!Number.isFinite(timestamp)) throw new Error("到期时间无效");
  if (timestamp <= now) throw new Error("到期时间必须晚于当前时间");
  return new Date(timestamp).toISOString();
}

function holdScope(record: JsonRecord): AuditHoldScope | undefined {
  const value = record.scope_type;
  return value === "tenant" || value === "account" || value === "actor"
    ? value
    : undefined;
}

export function CompliancePage({
  client,
}: {
  client: AeroIdClient;
}): React.ReactElement {
  const load = React.useCallback(() => client.compliance.listHolds(), [client]);
  const resource = useAsyncResource(load);
  const [scopeType, setScopeType] = React.useState<AuditHoldScope>("account");
  const [scopeID, setScopeID] = React.useState("");
  const [reason, setReason] = React.useState("");
  const [expiresAt, setExpiresAt] = React.useState("");
  const [command, setCommand] = React.useState<ComplianceCommand>();
  const [confirmation, setConfirmation] = React.useState("");
  const [running, setRunning] = React.useState(false);
  const [commandError, setCommandError] = React.useState<Error>();
  const [message, setMessage] = React.useState<string>();
  const [jobID, setJobID] = React.useState<string>();

  if (resource.loading) return <PageLoading />;
  if (resource.error)
    return <PageError error={resource.error} retry={resource.reload} />;

  const prepareHold = () => {
    setCommandError(undefined);
    try {
      const id = scopeID.trim();
      const holdReason = reason.trim();
      if (!id) throw new Error("Scope ID 不能为空");
      if (!holdReason) throw new Error("必须填写保全原因");
      const input: AuditHoldInput = {
        scopeType,
        scopeID: id,
        reason: holdReason,
        expiresAt: normalizeHoldExpiry(expiresAt),
      };
      setCommand({ kind: "put", input });
    } catch (cause) {
      setCommandError(
        cause instanceof Error ? cause : new Error("Legal Hold 输入无效"),
      );
    }
  };
  const openCommand = (next: ComplianceCommand) => {
    setConfirmation("");
    setCommandError(undefined);
    setCommand(next);
  };
  const runCommand = async () => {
    if (!command) return;
    setRunning(true);
    setCommandError(undefined);
    setMessage(undefined);
    try {
      if (command.kind === "put") {
        await client.compliance.putHold(command.input);
        setMessage("Legal Hold 已持久化，并已原子写入审计事实。");
        setScopeID("");
        setReason("");
        setExpiresAt("");
        resource.reload();
      } else if (command.kind === "remove") {
        await client.compliance.removeHold(command.scopeType, command.scopeID);
        setMessage("Legal Hold 已解除，并已写入新的审计事实。");
        resource.reload();
      } else {
        const id = syncJobID(await client.compliance.createAuditExport());
        if (!id) throw new Error("服务端未返回审计导出任务 ID");
        setJobID(id);
        setMessage(`审计导出任务 ${id} 已持久化。`);
      }
      setCommand(undefined);
      setConfirmation("");
    } catch (cause) {
      setCommandError(
        cause instanceof Error ? cause : new Error("合规命令执行失败"),
      );
    } finally {
      setRunning(false);
    }
  };
  return (
    <section>
      <PageHeader
        title="审计合规管理"
        description="Legal Hold 阻止适用证据进入常规清理；审计导出由异步任务生成，并通过受保护的一次性下载交付。"
        actions={
          <IrisButton
            variant="outline"
            onClick={() => openCommand({ kind: "export" })}
          >
            创建审计导出
          </IrisButton>
        }
      />
      {message ? <IrisAlert tone="success">{message}</IrisAlert> : null}
      {commandError && !command ? (
        <IrisAlert tone="danger">{commandError.message}</IrisAlert>
      ) : null}
      <IrisCard variant="outline" header="设置 Legal Hold">
        <form
          className="compliance-create-grid"
          onSubmit={(event) => {
            event.preventDefault();
            prepareHold();
          }}
        >
          <IrisFormField label="Scope 类型">
            <IrisSelect
              items={scopeItems}
              value={scopeType}
              onValueChange={(value) =>
                setScopeType(String(value) as AuditHoldScope)
              }
            />
          </IrisFormField>
          <IrisFormField label="Scope ID" required>
            <IrisInput
              required
              value={scopeID}
              onChange={(event) => setScopeID(event.target.value)}
            />
          </IrisFormField>
          <IrisFormField label="保全原因" required>
            <IrisTextarea
              required
              value={reason}
              maxLength={1000}
              onChange={(event) => setReason(event.target.value)}
            />
          </IrisFormField>
          <IrisFormField
            label="到期时间（可选）"
            hint="RFC3339，例如 2027-01-02T03:04:05Z；留空表示长期保全"
          >
            <IrisInput
              value={expiresAt}
              placeholder="2027-01-02T03:04:05Z"
              onChange={(event) => setExpiresAt(event.target.value)}
            />
          </IrisFormField>
          <div className="admin-filter-actions">
            <IrisButton type="submit">检查并确认</IrisButton>
          </div>
        </form>
      </IrisCard>
      <IrisCard variant="outline" header="当前生效的 Legal Hold">
        <RecordTable
          records={resource.data!}
          columns={[
            { key: "scope_type", label: "Scope 类型" },
            { key: "scope_id", label: "Scope ID" },
            { key: "reason", label: "原因" },
            { key: "created_by", label: "创建者" },
            { key: "created_at", label: "创建时间", kind: "date" },
            { key: "expires_at", label: "到期时间", kind: "date" },
          ]}
          empty="暂无生效的 Legal Hold"
          renderActions={(hold) => {
            const scope = holdScope(hold);
            const id = String(hold.scope_id ?? "");
            return scope && id ? (
              <IrisButton
                size="sm"
                variant="danger"
                onClick={() =>
                  openCommand({ kind: "remove", scopeType: scope, scopeID: id })
                }
              >
                解除
              </IrisButton>
            ) : null;
          }}
        />
      </IrisCard>
      {jobID ? (
        <JobDetails
          client={client}
          jobID={jobID}
          close={() => setJobID(undefined)}
          changed={() => undefined}
        />
      ) : null}
      <ComplianceCommandDialog
        command={command}
        running={running}
        error={commandError}
        confirmation={confirmation}
        setConfirmation={setConfirmation}
        cancel={() => setCommand(undefined)}
        confirm={() => void runCommand()}
      />
    </section>
  );
}
