<script setup lang="ts">
import { computed, ref } from "vue";
import { IrisAlert, IrisButton, IrisCard } from "@iris-ui-kit/vue";
import type { AeroIdClient } from "../api/aeroId";
import OperationDetails from "../components/OperationDetails.vue";
import PagedRecordTable from "../components/PagedRecordTable.vue";
import PageError from "../components/PageError.vue";
import PageHeader from "../components/PageHeader.vue";
import PageLoading from "../components/PageLoading.vue";
import { useAsyncResource } from "../hooks/useAsyncResource";
import { operationID } from "../components/operationHelpers";
const { client } = defineProps<{ client: AeroIdClient }>();
const resource = useAsyncResource(() => client.listOperations());
const selectedOperation = ref<string>();
const hasUnknown = computed(
  () => resource.data?.items.some((item) => item.status === "unknown") ?? false,
);
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
      title="跨系统操作"
      description="aero-id 持久化的 Saga 状态及来源操作标识。"
    />
    <IrisAlert v-if="hasUnknown" tone="warning" title="存在结果未知的操作">
      远端超时不代表失败；请等待以幂等键或来源 operation ID 完成对账。
    </IrisAlert>
    <IrisCard variant="outline">
      <PagedRecordTable
        :page="resource.data"
        :load-page="(cursor) => client.listOperations(cursor)"
        :columns="[
          { key: 'id', label: '操作 ID', aliases: ['operation_id'] },
          { key: 'operation_type', label: '类型', aliases: ['type'] },
          { key: 'status', label: '状态', kind: 'status' },
          { key: 'created_at', label: '创建时间', kind: 'date' },
          { key: 'updated_at', label: '更新时间', kind: 'date' },
        ]"
        empty="暂无跨系统操作"
      >
        <template #actions="{ record }">
          <IrisButton
            v-if="operationID(record)"
            size="sm"
            variant="ghost"
            @click="selectedOperation = operationID(record)"
            >查看时间线</IrisButton
          >
        </template>
      </PagedRecordTable>
    </IrisCard>
    <OperationDetails
      v-if="selectedOperation"
      :client="client"
      :operation-id="selectedOperation"
      @close="selectedOperation = undefined"
      @changed="resource.reload"
    />
  </section>
</template>
