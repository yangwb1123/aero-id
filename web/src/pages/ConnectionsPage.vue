<script setup lang="ts">
import { IrisCard } from "@iris-ui-kit/vue";
import type { AeroIdClient } from "../api/aeroId";
import PagedRecordTable from "../components/PagedRecordTable.vue";
import PageError from "../components/PageError.vue";
import PageHeader from "../components/PageHeader.vue";
import PageLoading from "../components/PageLoading.vue";
import { useAsyncResource } from "../hooks/useAsyncResource";
const { client } = defineProps<{ client: AeroIdClient }>();
const resource = useAsyncResource(async () => {
  const [sources, memberships] = await Promise.all([
    client.listSources(),
    client.listMemberships(),
  ]);
  return { sources, memberships };
});
</script>

<template>
  <PageLoading v-if="resource.loading" />
  <PageError
    v-else-if="resource.error"
    :error="resource.error"
    :retry="resource.reload"
  />
  <section v-else-if="resource.data">
    <PageHeader
      title="来源与成员关系"
      description="保留 Aero IM、Aero Vault 和 Snaplink 的原生引用，不把不同作用域折叠成一个 tenant_id。"
    />
    <IrisCard variant="outline">
      <template #header>来源账户</template>
      <PagedRecordTable
        :page="resource.data.sources"
        :load-page="(cursor) => client.listSources(cursor)"
        :columns="[
          { key: 'source', label: '来源', aliases: ['source_system'] },
          {
            key: 'source_account_id',
            label: '来源账户',
            aliases: ['external_id', 'subject'],
          },
          { key: 'status', label: '状态', kind: 'status' },
          { key: 'source_region', label: '区域', aliases: ['region'] },
          { key: 'last_synced_at', label: '最后同步', kind: 'date' },
        ]"
        empty="尚未关联来源账户"
      />
    </IrisCard>
    <IrisCard variant="outline">
      <template #header>成员关系</template>
      <PagedRecordTable
        :page="resource.data.memberships"
        :load-page="(cursor) => client.listMemberships(cursor)"
        :columns="[
          { key: 'source', label: '来源' },
          { key: 'scope_type', label: '作用域类型' },
          { key: 'scope_id', label: '作用域 ID' },
          { key: 'role', label: '角色', aliases: ['membership_role'] },
          { key: 'status', label: '状态', kind: 'status' },
        ]"
        empty="暂无成员关系"
      />
    </IrisCard>
  </section>
</template>
