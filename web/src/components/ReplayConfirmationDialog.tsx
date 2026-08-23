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
import type { ReplayCreateInput } from "../api/replay";

export function ReplayConfirmationDialog({
  input,
  confirmation,
  setConfirmation,
  running,
  error,
  cancel,
  confirm,
}: {
  input?: ReplayCreateInput;
  confirmation: string;
  setConfirmation(value: string): void;
  running: boolean;
  error?: Error;
  cancel(): void;
  confirm(): void;
}): React.ReactElement {
  return (
    <IrisDialog
      open={Boolean(input)}
      onOpenChange={(open) => {
        if (!open && !running) cancel();
      }}
    >
      <IrisDialogContent>
        <IrisDialogTitle>确认 Controlled Replay</IrisDialogTitle>
        <IrisDialogDescription>
          将为 {input?.commands.length ?? 0} 个命令创建新的持久化
          Operation。服务端会重新授权并使用新的幂等键，不复用历史凭据。
        </IrisDialogDescription>
        <IrisAlert tone="danger" title="此模式会产生真实来源副作用">
          先执行 dry-run 并核对当前来源状态；超时结果会保留为
          unknown，禁止当作失败盲目重发。
        </IrisAlert>
        <IrisFormField
          label="输入完整 Account ID 以确认"
          hint={input?.accountID}
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
            disabled={!input?.accountID || confirmation !== input.accountID}
            onClick={confirm}
          >
            创建真实 Replay Operation
          </IrisButton>
        </div>
      </IrisDialogContent>
    </IrisDialog>
  );
}
