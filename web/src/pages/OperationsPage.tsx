import * as React from "react";
import { IrisAlert, IrisCard } from "@iris-ui-kit/react";
import type { AeroIdClient } from "../api/aeroId";
import { PageHeader } from "../components/PageHeader";
import { PageError, PageLoading } from "../components/PageState";
import { PagedRecordTable } from "../components/PagedRecordTable";
import { useAsyncResource } from "../hooks/useAsyncResource";

export function OperationsPage({
  client,
}: {
  client: AeroIdClient;
}): React.ReactElement {
  const load = React.useCallback(() => client.listOperations(), [client]);
  const resource = useAsyncResource(load);
  if (resource.loading) return <PageLoading />;
  if (resource.error)
    return <PageError error={resource.error} retry={resource.reload} />;
  const hasUnknown = resource.data!.items.some(
    (item) => item.status === "unknown",
  );
  return (
    <section>
      <PageHeader
        title="跨系统操作"
        description="aero-id 持久化的 Saga 状态及来源操作标识。"
      />
      {hasUnknown ? (
        <IrisAlert tone="warning" title="存在结果未知的操作">
          远端超时不代表失败；请等待以幂等键或来源 operation ID 完成对账。
        </IrisAlert>
      ) : null}
      <IrisCard variant="outline">
        <PagedRecordTable
          page={resource.data!}
          loadPage={(cursor) => client.listOperations(cursor)}
          columns={[
            { key: "id", label: "操作 ID", aliases: ["operation_id"] },
            { key: "operation_type", label: "类型", aliases: ["type"] },
            { key: "status", label: "状态", kind: "status" },
            { key: "created_at", label: "创建时间", kind: "date" },
            { key: "updated_at", label: "更新时间", kind: "date" },
          ]}
          empty="暂无跨系统操作"
        />
      </IrisCard>
    </section>
  );
}
