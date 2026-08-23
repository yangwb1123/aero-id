import * as React from "react";
import {
  IrisAlert,
  IrisButton,
  IrisCard,
  IrisDialog,
  IrisDialogContent,
  IrisDialogDescription,
  IrisDialogTitle,
  IrisFormField,
  IrisInput,
} from "@iris-ui-kit/react";
import type { AdminAccountJobType, AeroIdClient } from "../api/aeroId";
import { JobDetails, syncJobID } from "./JobDetails";

type ConfirmedJobType = Exclude<AdminAccountJobType, "sync">;

function AccountJobConfirmation({
  accountID,
  jobType,
  running,
  error,
  eraseConfirmation,
  setEraseConfirmation,
  cancel,
  confirm,
}: {
  accountID: string;
  jobType?: ConfirmedJobType;
  running: boolean;
  error?: Error;
  eraseConfirmation: string;
  setEraseConfirmation(value: string): void;
  cancel(): void;
  confirm(): void;
}): React.ReactElement {
  const erase = jobType === "erase";
  return (
    <IrisDialog
      open={Boolean(jobType)}
      onOpenChange={(open) => {
        if (!open && !running) cancel();
      }}
    >
      <IrisDialogContent>
        <IrisDialogTitle>
          {erase ? "确认擦除账户" : "确认创建账户导出"}
        </IrisDialogTitle>
        <IrisDialogDescription>
          {erase
            ? "该任务会命令各来源删除其权威数据，并最终擦除 aero-id 控制面数据；远端超时将保留为 unknown，不会盲目重发。"
            : "导出可能包含账户的敏感投影。制品受权限与 TTL 保护，完成后仍需一次性下载确认。"}
        </IrisDialogDescription>
        {erase ? (
          <IrisFormField label="输入完整 Account ID 以确认" hint={accountID}>
            <IrisInput
              value={eraseConfirmation}
              autoComplete="off"
              onChange={(event) => setEraseConfirmation(event.target.value)}
            />
          </IrisFormField>
        ) : null}
        {error ? <IrisAlert tone="danger">{error.message}</IrisAlert> : null}
        <div className="dialog-actions">
          <IrisButton variant="ghost" disabled={running} onClick={cancel}>
            取消
          </IrisButton>
          <IrisButton
            variant={erase ? "danger" : "solid"}
            loading={running}
            disabled={erase && eraseConfirmation !== accountID}
            onClick={confirm}
          >
            {erase ? "确认创建擦除任务" : "确认导出"}
          </IrisButton>
        </div>
      </IrisDialogContent>
    </IrisDialog>
  );
}

export function AdminAccountActions({
  client,
  accountID,
}: {
  client: AeroIdClient;
  accountID: string;
}): React.ReactElement {
  const [action, setAction] = React.useState<AdminAccountJobType>();
  const [actionError, setActionError] = React.useState<Error>();
  const [confirmAction, setConfirmAction] = React.useState<ConfirmedJobType>();
  const [eraseConfirmation, setEraseConfirmation] = React.useState("");
  const [createdJobID, setCreatedJobID] = React.useState<string>();

  const createJob = async (jobType: AdminAccountJobType) => {
    setAction(jobType);
    setActionError(undefined);
    try {
      const created = await client.createAdminAccountJob(accountID, jobType);
      const id = syncJobID(created);
      if (!id) throw new Error("服务端未返回任务 ID");
      setCreatedJobID(id);
      setConfirmAction(undefined);
      setEraseConfirmation("");
    } catch (reason) {
      setActionError(
        reason instanceof Error ? reason : new Error("账户任务创建失败"),
      );
    } finally {
      setAction(undefined);
    }
  };
  const openConfirmation = (jobType: ConfirmedJobType) => {
    setActionError(undefined);
    setEraseConfirmation("");
    setConfirmAction(jobType);
  };
  return (
    <>
      <IrisCard variant="outline" header="账户任务">
        {actionError && !confirmAction ? (
          <IrisAlert tone="danger">{actionError.message}</IrisAlert>
        ) : null}
        {createdJobID ? (
          <IrisAlert tone="success" title="任务已创建">
            任务 {createdJobID} 已持久化；可在下方跟踪状态，unknown
            结果必须对账。
          </IrisAlert>
        ) : null}
        <div className="detail-actions">
          <IrisButton
            variant="outline"
            loading={action === "sync"}
            disabled={Boolean(action)}
            onClick={() => void createJob("sync")}
          >
            同步全部投影
          </IrisButton>
          <IrisButton
            variant="outline"
            disabled={Boolean(action)}
            onClick={() => openConfirmation("export")}
          >
            创建账户导出
          </IrisButton>
          <IrisButton
            variant="danger"
            disabled={Boolean(action)}
            onClick={() => openConfirmation("erase")}
          >
            擦除账户数据
          </IrisButton>
        </div>
      </IrisCard>
      {createdJobID ? (
        <JobDetails
          client={client}
          jobID={createdJobID}
          close={() => setCreatedJobID(undefined)}
          changed={() => undefined}
        />
      ) : null}
      <AccountJobConfirmation
        accountID={accountID}
        jobType={confirmAction}
        running={Boolean(action)}
        error={actionError}
        eraseConfirmation={eraseConfirmation}
        setEraseConfirmation={setEraseConfirmation}
        cancel={() => setConfirmAction(undefined)}
        confirm={() => {
          if (confirmAction) void createJob(confirmAction);
        }}
      />
    </>
  );
}
