import * as React from "react";
import { IrisCard } from "@iris-ui-kit/react";
import type { AeroIdClient } from "../api/aeroId";
import { PageHeader } from "../components/PageHeader";
import { PageError, PageLoading } from "../components/PageState";
import { PagedRecordTable } from "../components/PagedRecordTable";
import { useAsyncResource } from "../hooks/useAsyncResource";

export function ActivityPage({
  client,
}: {
  client: AeroIdClient;
}): React.ReactElement {
  const load = React.useCallback(() => client.listActivity(), [client]);
  const resource = useAsyncResource(load);
  if (resource.loading) return <PageLoading />;
  if (resource.error)
    return <PageError error={resource.error} retry={resource.reload} />;
  return (
    <section>
      <PageHeader
        title="近期活动"
        description="来自本地脱敏活动投影，不读取消息正文或文件内容。"
      />
      <IrisCard variant="outline">
        <PagedRecordTable
          page={resource.data!}
          loadPage={(cursor) => client.listActivity(cursor)}
          columns={[
            {
              key: "occurred_at",
              label: "时间",
              aliases: ["created_at"],
              kind: "date",
            },
            { key: "source", label: "来源", aliases: ["source_system"] },
            { key: "event_type", label: "事件", aliases: ["type", "action"] },
            {
              key: "status",
              label: "状态",
              aliases: ["outcome"],
              kind: "status",
            },
            { key: "operation_id", label: "操作 ID" },
          ]}
          empty="暂无近期活动"
        />
      </IrisCard>
    </section>
  );
}
