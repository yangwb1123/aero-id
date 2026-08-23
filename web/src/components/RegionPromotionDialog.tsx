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

export interface PendingRegionPromotion {
  targetRegion: string;
  expectedEpoch: number;
  idempotencyKey: string;
}

export function RegionPromotionDialog({
  accountID,
  pending,
  confirmation,
  running,
  error,
  setConfirmation,
  cancel,
  confirm,
}: {
  accountID: string;
  pending?: PendingRegionPromotion;
  confirmation: string;
  running: boolean;
  error?: Error;
  setConfirmation(value: string): void;
  cancel(): void;
  confirm(): void;
}): React.ReactElement {
  return (
    <IrisDialog
      open={Boolean(pending)}
      onOpenChange={(open) => {
        if (!open && !running) cancel();
      }}
    >
      <IrisDialogContent>
        <IrisDialogTitle>确认提升 Home Region Writer</IrisDialogTitle>
        <IrisDialogDescription>
          将以已观察到的 Epoch {pending?.expectedEpoch ?? "—"} 执行 CAS，把账户
          Writer 提升到 {pending?.targetRegion ?? "—"}。成功后旧 Writer
          会被新栅栏代际拒绝；冲突不会自动重试。
        </IrisDialogDescription>
        <IrisAlert tone="warning" title="先完成区域故障处置检查">
          确认目标区域数据库、来源连接和审计链可用，并确认流量入口会跟随新的
          Home Region。不要通过本操作建立双写。
        </IrisAlert>
        <IrisFormField label="输入完整 Account ID 以确认" hint={accountID}>
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
            variant="danger"
            loading={running}
            disabled={confirmation !== accountID}
            onClick={confirm}
          >
            确认提升并栅栏旧 Writer
          </IrisButton>
        </div>
      </IrisDialogContent>
    </IrisDialog>
  );
}
