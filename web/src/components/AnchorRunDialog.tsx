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

export const anchorRunConfirmation = "ANCHOR ALL CHAINS";

export function AnchorRunDialog({
  idempotencyKey,
  confirmation,
  running,
  error,
  setConfirmation,
  cancel,
  confirm,
}: {
  idempotencyKey?: string;
  confirmation: string;
  running: boolean;
  error?: Error;
  setConfirmation(value: string): void;
  cancel(): void;
  confirm(): void;
}): React.ReactElement {
  return (
    <IrisDialog
      open={Boolean(idempotencyKey)}
      onOpenChange={(open) => {
        if (!open && !running) cancel();
      }}
    >
      <IrisDialogContent>
        <IrisDialogTitle>确认执行 WORM Anchor</IrisDialogTitle>
        <IrisDialogDescription>
          本次操作会扫描并验证当前全部审计链，再为每条链写入新的不可变归档对象。固定幂等键会保留到本次确认关闭。
        </IrisDialogDescription>
        <IrisAlert tone="warning" title="全链执行可能留下部分完成结果">
          服务端会在任何归档副作用前先持久化管理意图；审计失败时不会写归档。若后续链校验失败或请求超时，已经写入的不可变锚点不会回滚，应先查询
          Manifest 再决定是否重试。
        </IrisAlert>
        <IrisFormField
          label={`输入 ${anchorRunConfirmation} 以确认`}
          hint="该操作覆盖所有来源与分区，不是单链操作"
        >
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
            disabled={confirmation !== anchorRunConfirmation}
            onClick={confirm}
          >
            验证全部链并写入锚点
          </IrisButton>
        </div>
      </IrisDialogContent>
    </IrisDialog>
  );
}
