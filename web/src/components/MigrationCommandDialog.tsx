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
import type { LegacyCutoverStatus } from "../api/migration";

export const backfillConfirmation = "BACKFILL LEGACY IDENTITIES";

export type MigrationCommand =
  | { kind: "backfill"; limit: number; idempotencyKey: string }
  | {
      kind: "cutover";
      tenantID: string;
      status: LegacyCutoverStatus;
      note: string;
      idempotencyKey: string;
    };

export function MigrationCommandDialog({
  command,
  confirmation,
  running,
  error,
  setConfirmation,
  cancel,
  confirm,
}: {
  command?: MigrationCommand;
  confirmation: string;
  running: boolean;
  error?: Error;
  setConfirmation(value: string): void;
  cancel(): void;
  confirm(): void;
}): React.ReactElement {
  const cutover = command?.kind === "cutover";
  const expected = cutover ? command.tenantID : backfillConfirmation;
  return (
    <IrisDialog
      open={Boolean(command)}
      onOpenChange={(open) => {
        if (!open && !running) cancel();
      }}
    >
      <IrisDialogContent>
        <IrisDialogTitle>
          {cutover
            ? "确认租户 Legacy Cutover"
            : "确认 Legacy Identity Backfill"}
        </IrisDialogTitle>
        <IrisDialogDescription>
          {cutover
            ? `将租户 ${command.tenantID} 切换为 ${command.status}，本次确认期间保持同一个幂等键。`
            : `本轮最多扫描 ${command?.kind === "backfill" ? command.limit : "—"} 个 legacy UID，只创建 shadow account link，不修改 legacy 内容。`}
        </IrisDialogDescription>
        <IrisAlert
          tone={cutover ? "danger" : "warning"}
          title={
            cutover ? "切换会改变 Legacy Facade 行为" : "批处理可能部分完成"
          }
        >
          {cutover
            ? "cut_over 会让该租户的 legacy surface 返回 moved；超时或并发冲突后必须重新读取状态，禁止拿旧决策或新幂等键盲目重试。"
            : "每个账户创建和审计事实原子提交；单条失败会计入 skipped。请求超时后应保留本次幂等键，身份唯一约束仍是最终防线。"}
        </IrisAlert>
        <IrisFormField label={`输入 ${expected} 以确认`} hint={expected}>
          <IrisInput
            value={confirmation}
            autoComplete="off"
            onChange={(event) => setConfirmation(event.target.value)}
          />
        </IrisFormField>
        {error ? <IrisAlert tone="danger">{error.message}</IrisAlert> : null}
        <div className="dialog-actions">
          <IrisButton variant="ghost" disabled={running} onClick={cancel}>
            取消
          </IrisButton>
          <IrisButton
            variant={cutover ? "danger" : "solid"}
            loading={running}
            disabled={confirmation !== expected}
            onClick={confirm}
          >
            {cutover ? "记录 Cutover 决策" : "执行有界 Backfill"}
          </IrisButton>
        </div>
      </IrisDialogContent>
    </IrisDialog>
  );
}
