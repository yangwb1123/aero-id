import {
  IrisAlert,
  IrisButton,
  IrisCopyButton,
  IrisDialog,
  IrisDialogContent,
  IrisDialogDescription,
  IrisDialogTitle,
} from "@iris-ui-kit/react";

export interface CallbackSecret {
  subscriptionID: string;
  value: string;
}

export type CallbackCommand =
  | { kind: "delete"; id: string; endpoint: string }
  | { kind: "replay"; id: string };

export function CallbackSecretDialog({
  secret,
  discard,
}: {
  secret?: CallbackSecret;
  discard(): void;
}): React.ReactElement {
  return (
    <IrisDialog
      open={Boolean(secret)}
      onOpenChange={(open) => {
        if (!open) discard();
      }}
    >
      <IrisDialogContent>
        <IrisDialogTitle>保存 Callback 签名 Secret</IrisDialogTitle>
        <IrisDialogDescription>
          这是订阅 {secret?.subscriptionID}{" "}
          的一次性敏感结果。关闭后控制台不会保留或再次展示。
        </IrisDialogDescription>
        <IrisAlert tone="warning" title="仅保存到受控 Secret Provider">
          不要写入源码、日志、工单或浏览器存储；接收端应使用它验证时间戳和请求体签名。
        </IrisAlert>
        <code className="secret-value">{secret?.value ?? ""}</code>
        <div className="dialog-actions">
          <IrisCopyButton text={secret?.value ?? ""} disabled={!secret}>
            复制一次
          </IrisCopyButton>
          <IrisButton onClick={discard}>我已安全保存并关闭</IrisButton>
        </div>
      </IrisDialogContent>
    </IrisDialog>
  );
}

export function CallbackCommandDialog({
  command,
  running,
  error,
  cancel,
  confirm,
}: {
  command?: CallbackCommand;
  running: boolean;
  error?: Error;
  cancel(): void;
  confirm(): void;
}): React.ReactElement {
  const deleting = command?.kind === "delete";
  return (
    <IrisDialog
      open={Boolean(command)}
      onOpenChange={(open) => {
        if (!open && !running) cancel();
      }}
    >
      <IrisDialogContent>
        <IrisDialogTitle>
          {deleting ? "确认删除 Callback 订阅" : "确认重放 Callback 投递"}
        </IrisDialogTitle>
        <IrisDialogDescription>
          {deleting
            ? `删除后将停止向 ${command?.endpoint ?? "该端点"} 创建新投递。`
            : `Delivery ${command?.id ?? ""} 将作为新的人工意图进入重试队列，并写入新的审计事实。`}
        </IrisDialogDescription>
        {error ? <IrisAlert tone="danger">{error.message}</IrisAlert> : null}
        <div className="dialog-actions">
          <IrisButton variant="ghost" disabled={running} onClick={cancel}>
            取消
          </IrisButton>
          <IrisButton
            variant={deleting ? "danger" : "solid"}
            loading={running}
            onClick={confirm}
          >
            {deleting ? "确认删除" : "确认进入重试队列"}
          </IrisButton>
        </div>
      </IrisDialogContent>
    </IrisDialog>
  );
}
