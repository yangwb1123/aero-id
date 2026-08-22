import * as React from "react";
import { IrisCard } from "@iris-ui-kit/react";
import type { AeroIdClient } from "../api/aeroId";
import { PageHeader } from "../components/PageHeader";
import { PageError, PageLoading } from "../components/PageState";
import { RecordTable } from "../components/RecordTable";
import { useAsyncResource } from "../hooks/useAsyncResource";

export function ConnectionsPage({
  client,
}: {
  client: AeroIdClient;
}): React.ReactElement {
  const load = React.useCallback(async () => {
    const [sources, memberships] = await Promise.all([
      client.listSources(),
      client.listMemberships(),
    ]);
    return { sources, memberships };
  }, [client]);
  const resource = useAsyncResource(load);
  if (resource.loading) return <PageLoading />;
  if (resource.error)
    return <PageError error={resource.error} retry={resource.reload} />;
  return (
    <section>
      <PageHeader
        title="来源与成员关系"
        description="保留 Aero IM、Aero Vault 和 Snaplink 的原生引用，不把不同作用域折叠成一个 tenant_id。"
      />
      <IrisCard variant="outline" header="来源账户">
        <RecordTable
          records={resource.data!.sources.items}
          columns={[
            { key: "source", label: "来源", aliases: ["source_system"] },
            {
              key: "source_account_id",
              label: "来源账户",
              aliases: ["external_id", "subject"],
            },
            { key: "status", label: "状态", kind: "status" },
            { key: "source_region", label: "区域", aliases: ["region"] },
            { key: "last_synced_at", label: "最后同步", kind: "date" },
          ]}
          empty="尚未关联来源账户"
        />
      </IrisCard>
      <IrisCard variant="outline" header="成员关系">
        <RecordTable
          records={resource.data!.memberships.items}
          columns={[
            { key: "source", label: "来源" },
            { key: "scope_type", label: "作用域类型" },
            { key: "scope_id", label: "作用域 ID" },
            { key: "role", label: "角色", aliases: ["membership_role"] },
            { key: "status", label: "状态", kind: "status" },
          ]}
          empty="暂无成员关系"
        />
      </IrisCard>
    </section>
  );
}
