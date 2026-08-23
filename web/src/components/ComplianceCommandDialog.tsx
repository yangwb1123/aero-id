import {
  IrisAlert,
  IrisButton,
  IrisDialog,
  IrisDialogContent,
  IrisDialogDescription,
  IrisDialogTitle,
  IrisFormField,
  IrisInput,
} from "@iris-ui-kit/react";
import type { AuditHoldInput, AuditHoldScope } from "../api/compliance";

export type ComplianceCommand =
  | { kind: "put"; input: AuditHoldInput }
  | { kind: "remove"; scopeType: AuditHoldScope; scopeID: string }
  | { kind: "export" };

function title(command?: ComplianceCommand): string {
  if (command?.kind === "put") return "确认设置 Legal Hold";
  if (command?.kind === "remove") return "确认解除 Legal Hold";
  return "确认创建审计导出";
}

function description(command?: ComplianceCommand): string {
  if (command?.kind === "put")
    return `将保全 ${command.input.scopeType}:${command.input.scopeID} 的适用审计证据，并写入不可变审计事实。`;
  if (command?.kind === "remove")
    return `解除后 ${command.scopeType}:${command.scopeID} 将重新受正常保留与清理策略约束。该操作会写入新的审计事实。`;
  return "导出制品可能包含敏感审计证据，受权限和 TTL 保护；任务完成后仍需确认一次性下载。";
}

export function ComplianceCommandDialog({
  command,
  running,
  error,
  confirmation,
  setConfirmation,
  cancel,
  confirm,
}: {
  command?: ComplianceCommand;
  running: boolean;
  error?: Error;
  confirmation: string;
  setConfirmation(value: string): void;
  cancel(): void;
  confirm(): void;
}): React.ReactElement {
  const removing = command?.kind === "remove";
  const confirmed =
    !removing ||
    confirmation === (command?.kind === "remove" && command.scopeID);
  return (
    <IrisDialog
      open={Boolean(command)}
      onOpenChange={(open) => {
        if (!open && !running) cancel();
      }}
    >
      <IrisDialogContent>
        <IrisDialogTitle>{title(command)}</IrisDialogTitle>
        <IrisDialogDescription>{description(command)}</IrisDialogDescription>
        {removing ? (
          <IrisFormField
            label="输入完整 Scope ID 以确认解除"
            hint={command.scopeID}
          >
            <IrisInput
              value={confirmation}
              autoComplete="off"
              onChange={(event) => setConfirmation(event.target.value)}
            />
          </IrisFormField>
        ) : null}
        {error ? <IrisAlert tone="danger">{error.message}</IrisAlert> : null}
        <div className="dialog-actions">
          <IrisButton variant="ghost" disabled={running} onClick={cancel}>
            取消
          </IrisButton>
          <IrisButton
            variant={removing ? "danger" : "solid"}
            loading={running}
            disabled={!confirmed}
            onClick={confirm}
          >
            {removing ? "确认解除保全" : "确认执行"}
          </IrisButton>
        </div>
      </IrisDialogContent>
    </IrisDialog>
  );
}
