import * as React from "react";
import {
  IrisAlert,
  IrisButton,
  IrisCard,
  IrisDialog,
  IrisDialogContent,
  IrisDialogDescription,
  IrisDialogTitle,
} from "@iris-ui-kit/react";
import type { AeroIdClient } from "../api/aeroId";
import type { JsonRecord } from "../types";
import { useAsyncResource } from "../hooks/useAsyncResource";
import { PageError, PageLoading } from "./PageState";
import { RecordTable } from "./RecordTable";

function saveExport(blob: Blob, jobID: string): void {
  const url = URL.createObjectURL(blob);
  const anchor = document.createElement("a");
  anchor.href = url;
  anchor.download = `aero-id-export-${jobID}.json`;
  document.body.append(anchor);
  anchor.click();
  anchor.remove();
  window.setTimeout(() => URL.revokeObjectURL(url), 1_000);
}

function jsonPreview(value: unknown): string {
  return JSON.stringify(value ?? {}, null, 2);
}

export function JobDetails({
  client,
  jobID,
  close,
  changed,
}: {
  client: AeroIdClient;
  jobID: string;
  close(): void;
  changed(): void;
}): React.ReactElement {
  const load = React.useCallback(
    () => client.getSyncJob(jobID),
    [client, jobID],
  );
  const resource = useAsyncResource(load);
  const [action, setAction] = React.useState<"reconcile" | "download">();
  const [actionError, setActionError] = React.useState<Error>();
  const [confirmDownload, setConfirmDownload] = React.useState(false);

  if (resource.loading) return <PageLoading />;
  if (resource.error)
    return <PageError error={resource.error} retry={resource.reload} />;

  const job = resource.data!;
  const status = String(job.status ?? "");
  const jobType = String(job.job_type ?? job.type ?? "");
  const isExport = jobType === "export" || jobType === "audit_export";
  const reconcile = async () => {
    setAction("reconcile");
    setActionError(undefined);
    try {
      resource.replace(await client.reconcileSyncJob(jobID));
      changed();
    } catch (reason) {
      setActionError(
        reason instanceof Error ? reason : new Error("任务对账失败"),
      );
    } finally {
      setAction(undefined);
    }
  };
  const download = async () => {
    setAction("download");
    setActionError(undefined);
    try {
      saveExport(await client.downloadExport(jobID, true), jobID);
      setConfirmDownload(false);
    } catch (reason) {
      setActionError(
        reason instanceof Error ? reason : new Error("导出下载失败"),
      );
    } finally {
      setAction(undefined);
    }
  };

  return (
    <>
      <IrisCard
        variant="outline"
        header={`任务详情 · ${jobID}`}
        footer={
          <div className="detail-actions">
            <IrisButton size="sm" variant="ghost" onClick={close}>
              关闭
            </IrisButton>
            {status === "unknown" ? (
              <IrisButton
                size="sm"
                variant="outline"
                loading={action === "reconcile"}
                onClick={() => void reconcile()}
              >
                对账未知结果
              </IrisButton>
            ) : null}
            {isExport && status === "completed" ? (
              <IrisButton size="sm" onClick={() => setConfirmDownload(true)}>
                一次性下载
              </IrisButton>
            ) : null}
          </div>
        }
      >
        {actionError && !confirmDownload ? (
          <IrisAlert tone="danger">{actionError.message}</IrisAlert>
        ) : null}
        {job.error_code ? (
          <IrisAlert tone="danger" title={String(job.error_code)}>
            {String(job.error_message ?? "任务执行失败")}
          </IrisAlert>
        ) : null}
        <RecordTable
          records={[job]}
          columns={[
            { key: "job_type", label: "类型", aliases: ["type"] },
            { key: "status", label: "状态", kind: "status" },
            { key: "attempt", label: "尝试次数" },
            { key: "created_at", label: "创建时间", kind: "date" },
            { key: "updated_at", label: "更新时间", kind: "date" },
            { key: "completed_at", label: "完成时间", kind: "date" },
          ]}
        />
        <div className="detail-json-grid">
          <div>
            <h3>请求</h3>
            <pre className="json-preview">{jsonPreview(job.request)}</pre>
          </div>
          <div>
            <h3>脱敏结果</h3>
            <pre className="json-preview">{jsonPreview(job.result)}</pre>
          </div>
        </div>
      </IrisCard>
      <IrisDialog
        open={confirmDownload}
        onOpenChange={(open) => {
          if (action !== "download") setConfirmDownload(open);
        }}
      >
        <IrisDialogContent>
          <IrisDialogTitle>确认一次性下载</IrisDialogTitle>
          <IrisDialogDescription>
            服务端会在本次成功读取后销毁导出制品。请确认当前设备和保存位置可信。
          </IrisDialogDescription>
          {actionError ? (
            <IrisAlert tone="danger">{actionError.message}</IrisAlert>
          ) : null}
          <div className="dialog-actions">
            <IrisButton
              variant="ghost"
              disabled={action === "download"}
              onClick={() => setConfirmDownload(false)}
            >
              取消
            </IrisButton>
            <IrisButton
              loading={action === "download"}
              onClick={() => void download()}
            >
              确认并下载
            </IrisButton>
          </div>
        </IrisDialogContent>
      </IrisDialog>
    </>
  );
}

export function syncJobID(record: JsonRecord): string | undefined {
  const value = record.job_id ?? record.id;
  return typeof value === "string" && value ? value : undefined;
}
