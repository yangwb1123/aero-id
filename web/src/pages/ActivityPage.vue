<script setup lang="ts">
import { IrisCard } from "@iris-ui-kit/vue";
import type { AeroIdClient } from "../api/aeroId";
import PagedRecordTable from "../components/PagedRecordTable.vue";
import PageError from "../components/PageError.vue";
import PageHeader from "../components/PageHeader.vue";
import PageLoading from "../components/PageLoading.vue";
import { useAsyncResource } from "../hooks/useAsyncResource";
const { client } = defineProps<{ client: AeroIdClient }>();
const resource = useAsyncResource(() => client.listActivity());
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
      title="近期活动"
      description="来自本地脱敏活动投影，不读取消息正文或文件内容。"
    />
    <IrisCard variant="outline">
      <PagedRecordTable
        :page="resource.data"
        :load-page="(cursor) => client.listActivity(cursor)"
        :columns="[
          {
            key: 'occurred_at',
            label: '时间',
            aliases: ['created_at'],
            kind: 'date',
          },
          { key: 'source', label: '来源', aliases: ['source_system'] },
          { key: 'event_type', label: '事件', aliases: ['type', 'action'] },
          {
            key: 'status',
            label: '状态',
            aliases: ['outcome'],
            kind: 'status',
          },
          { key: 'operation_id', label: '操作 ID' },
        ]"
        empty="暂无近期活动"
      />
    </IrisCard>
  </section>
</template>
