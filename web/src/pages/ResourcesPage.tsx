import * as React from "react";
import { IrisAlert, IrisBadge, IrisCard } from "@iris-ui-kit/react";
import type { AeroIdClient } from "../api/aeroId";
import { PageHeader } from "../components/PageHeader";
import { PageError, PageLoading } from "../components/PageState";
import { RecordTable } from "../components/RecordTable";
import { useAsyncResource } from "../hooks/useAsyncResource";
import type { AggregateView, Dataset, JsonRecord } from "../types";

function datasetRecords(datasets: Dataset[]): JsonRecord[] {
  return datasets.map((dataset) => ({
    name: dataset.name,
    source: dataset.source,
    classification: dataset.pii ? "PII" : "standard",
  }));
}

function ProjectionWarnings({
  view,
}: {
  view: AggregateView;
}): React.ReactElement {
  const sourceErrors = Object.entries(view.source_errors ?? {});
  return (
    <>
      {view.partial ? (
        <IrisAlert tone="warning" title="资源摘要不完整">
          部分来源暂不可用；已返回的来源投影仍然有效，缺失内容不会被解释为空资源。
        </IrisAlert>
      ) : null}
      {view.stale_datasets?.length ? (
        <IrisAlert tone="warning" title="存在过期投影">
          {view.stale_datasets.join("、")}
        </IrisAlert>
      ) : null}
      {sourceErrors.length ? (
        <IrisAlert tone="danger" title="来源错误">
          {sourceErrors
            .map(([source, message]) => `${source}: ${message}`)
            .join("；")}
        </IrisAlert>
      ) : null}
    </>
  );
}

export function ResourcesPage({
  client,
}: {
  client: AeroIdClient;
}): React.ReactElement {
  const load = React.useCallback(async () => {
    const [datasets, resources] = await Promise.all([
      client.listDatasets(),
      client.getResources(),
    ]);
    return { datasets, resources };
  }, [client]);
  const resource = useAsyncResource(load);
  if (resource.loading) return <PageLoading />;
  if (resource.error)
    return <PageError error={resource.error} retry={resource.reload} />;

  const { datasets, resources } = resource.data!;
  return (
    <section>
      <PageHeader
        title="资源摘要"
        description="读取 aero-id 本地脱敏投影；不会直接访问 Aero Vault、Aero IM 数据库，也不展示对象字节或消息正文。"
      />
      <ProjectionWarnings view={resources} />
      <IrisCard
        variant="outline"
        header="投影新鲜度"
        footer={
          <span className="muted">
            一致性：{resources.consistency ?? "eventual"}
          </span>
        }
      >
        <RecordTable
          records={resources.snapshots ?? []}
          columns={[
            { key: "dataset", label: "数据集" },
            { key: "source", label: "权威来源" },
            { key: "source_region", label: "来源区域" },
            { key: "status", label: "状态", kind: "status" },
            { key: "version", label: "版本" },
            { key: "generated_at", label: "生成时间", kind: "date" },
            { key: "last_synced_at", label: "最后同步", kind: "date" },
          ]}
          empty="尚无资源投影"
        />
      </IrisCard>
      <IrisCard
        variant="outline"
        header="允许的数据集"
        footer={<IrisBadge tone="neutral">Allow-list only</IrisBadge>}
      >
        <RecordTable
          records={datasetRecords(datasets)}
          columns={[
            { key: "name", label: "数据集" },
            { key: "source", label: "权威来源" },
            { key: "classification", label: "数据分类" },
          ]}
          empty="未配置可查询数据集"
        />
      </IrisCard>
    </section>
  );
}
