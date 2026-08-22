import * as React from "react";
import { IrisAlert, IrisBadge, IrisCard } from "@iris-ui-kit/react";
import type { AeroIdClient } from "../api/aeroId";
import { PageError, PageLoading } from "../components/PageState";
import { PageHeader } from "../components/PageHeader";
import { useAsyncResource } from "../hooks/useAsyncResource";

export function OverviewPage({
  client,
}: {
  client: AeroIdClient;
}): React.ReactElement {
  const load = React.useCallback(async () => {
    const [me, datasets, overview] = await Promise.all([
      client.getMe(),
      client.listDatasets(),
      client.getOverview(),
    ]);
    return { me, datasets, overview };
  }, [client]);
  const resource = useAsyncResource(load);
  if (resource.loading) return <PageLoading />;
  if (resource.error)
    return <PageError error={resource.error} retry={resource.reload} />;
  const { me, datasets, overview } = resource.data!;
  const metrics = [
    ["来源账户", me.sources?.length ?? 0],
    ["成员关系", me.memberships?.length ?? 0],
    ["可用数据集", datasets.length],
    ["聚合快照", overview.snapshots?.length ?? 0],
  ] as const;
  return (
    <section>
      <PageHeader
        title="账户概览"
        description="本地区域聚合投影及其来源新鲜度。"
      />
      {overview.partial ? (
        <IrisAlert tone="warning" title="聚合结果不完整">
          部分来源暂时不可用；页面保留了其余来源结果，不会把缺失数据解释为空数据。
        </IrisAlert>
      ) : null}
      <div className="metric-grid">
        {metrics.map(([label, value]) => (
          <IrisCard key={label} variant="outline">
            <div className="metric-label">{label}</div>
            <div className="metric-value">{value}</div>
          </IrisCard>
        ))}
      </div>
      <IrisCard variant="outline" header="投影状态">
        <div className="status-row">
          <span>一致性</span>
          <IrisBadge tone="primary">
            {overview.consistency ?? "eventual"}
          </IrisBadge>
        </div>
        <div className="status-row">
          <span>过期数据集</span>
          <span>{overview.stale_datasets?.join("、") || "无"}</span>
        </div>
        {Object.entries(overview.source_errors ?? {}).map(
          ([source, message]) => (
            <div className="status-row" key={source}>
              <span>{source}</span>
              <IrisBadge tone="danger">{message}</IrisBadge>
            </div>
          ),
        )}
      </IrisCard>
    </section>
  );
}
