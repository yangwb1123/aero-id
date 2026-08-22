import * as React from "react";
import { IrisAlert, IrisButton, IrisCard } from "@iris-ui-kit/react";
import type { AeroIdClient } from "../api/aeroId";
import { PageHeader } from "../components/PageHeader";
import { PageError, PageLoading } from "../components/PageState";
import { PagedRecordTable } from "../components/PagedRecordTable";
import { useAsyncResource } from "../hooks/useAsyncResource";

export function JobsPage({
  client,
}: {
  client: AeroIdClient;
}): React.ReactElement {
  const load = React.useCallback(async () => {
    const [jobs, datasets] = await Promise.all([
      client.listSyncJobs(),
      client.listDatasets(),
    ]);
    return { jobs, datasets };
  }, [client]);
  const resource = useAsyncResource(load);
  const [action, setAction] = React.useState<"sync" | "export">();
  const [actionError, setActionError] = React.useState<Error>();
  if (resource.loading) return <PageLoading />;
  if (resource.error)
    return <PageError error={resource.error} retry={resource.reload} />;

  const create = async (kind: "sync" | "export") => {
    setAction(kind);
    setActionError(undefined);
    try {
      const datasets = resource.data!.datasets.map((dataset) => dataset.name);
      if (kind === "sync") await client.createSyncJob(datasets);
      else await client.createExportJob(datasets);
      resource.reload();
    } catch (reason) {
      setActionError(
        reason instanceof Error ? reason : new Error("任务创建失败"),
      );
    } finally {
      setAction(undefined);
    }
  };

  return (
    <section>
      <PageHeader
        title="同步与导出"
        description="任务使用幂等键创建；unknown 状态需要对账，不能自动视为失败重试。"
        actions={
          <>
            <IrisButton
              loading={action === "sync"}
              onClick={() => void create("sync")}
            >
              同步全部数据集
            </IrisButton>
            <IrisButton
              variant="outline"
              loading={action === "export"}
              onClick={() => void create("export")}
            >
              创建加密导出
            </IrisButton>
          </>
        }
      />
      {actionError ? (
        <IrisAlert tone="danger">{actionError.message}</IrisAlert>
      ) : null}
      <IrisCard variant="outline">
        <PagedRecordTable
          page={resource.data!.jobs}
          loadPage={(cursor) => client.listSyncJobs(cursor)}
          columns={[
            { key: "id", label: "任务 ID", aliases: ["job_id"] },
            { key: "job_type", label: "类型", aliases: ["type"] },
            { key: "status", label: "状态", kind: "status" },
            { key: "created_at", label: "创建时间", kind: "date" },
            { key: "completed_at", label: "完成时间", kind: "date" },
          ]}
          empty="暂无同步或导出任务"
        />
      </IrisCard>
    </section>
  );
}
