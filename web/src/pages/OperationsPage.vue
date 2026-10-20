<script setup lang="ts">
import { computed } from "vue";
import { IrisAlert, IrisButton, IrisCard } from "@iris-ui-kit/vue";
import type { AeroIdClient } from "../api/aeroId";
import type { JsonRecord } from "../types";
import OperationDetails from "../components/OperationDetails.vue";
import OperationCreateDialog from "../components/OperationCreateDialog.vue";
import PagedRecordTable from "../components/PagedRecordTable.vue";
import PageError from "../components/PageError.vue";
import PageHeader from "../components/PageHeader.vue";
import PageLoading from "../components/PageLoading.vue";
import { useAsyncResource } from "../hooks/useAsyncResource";
import { operationID } from "../components/operationHelpers";
import { taskHref } from "../router";
import { useTaskNavigation } from "../hooks/useTaskNavigation";
const props = defineProps<{ client: AeroIdClient; operationId?: string }>();
const { client } = props;
const resource = useAsyncResource((signal) =>
  client.listOperations(undefined, signal),
);
const datasetsResource = useAsyncResource((signal) =>
  client.listDatasets(signal),
);
const meResource = useAsyncResource((signal) => client.getMe(signal));
const {
  selected: selectedOperation,
  detailRoot,
  select: openOperation,
  close: closeOperation,
} = useTaskNavigation("operations", () => props.operationId);
const defaultAccountID = computed(() => {
  const value = meResource.data?.account?.account_id;
  return typeof value === "string" ? value : undefined;
});
function reloadOperations(operation?: JsonRecord): void {
  const id = operation ? operationID(operation) : undefined;
  if (id) openOperation(id);
  void resource.reload();
}
const hasUnknown = computed(
  () => resource.data?.items.some((item) => item.status === "unknown") ?? false,
);
</script>

<template>
  <nav
    v-if="selectedOperation"
    data-task-navigation
    class="detail-actions task-navigation"
    aria-label="Operation 详情导航"
  >
    <a href="#/operations" @click.prevent="closeOperation">返回操作列表</a>
    <a :href="taskHref('operations', selectedOperation)"
      >原 Operation 详情链接</a
    >
    <span>链接仅定位原回执；返回或刷新不运行或重发操作。</span>
  </nav>
  <section
    v-if="selectedOperation"
    ref="detailRoot"
    tabindex="-1"
    aria-label="原 Operation 详情"
  >
    <OperationDetails
      :key="selectedOperation"
      :client="client"
      :operation-id="selectedOperation"
      @close="closeOperation"
      @changed="resource.reload"
    />
  </section>
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
    <div class="page-actions">
      <OperationCreateDialog
        :client="client"
        :datasets="datasetsResource.data ?? []"
        :default-account-id="defaultAccountID"
        @created="reloadOperations"
      />
    </div>
    <IrisAlert
      v-if="datasetsResource.error"
      tone="warning"
      title="Operation 数据集目录不可用"
    >
      无法校验来源 dataset allow-list；请稍后重试后再创建 Operation。
    </IrisAlert>
    <IrisCard variant="outline">
      <div
        class="operations-list-table"
        role="region"
        aria-label="操作列表（可横向滚动）"
        tabindex="0"
      >
        <PagedRecordTable
          :page="resource.data"
          :load-page="(cursor, signal) => client.listOperations(cursor, signal)"
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
              @click="openOperation(operationID(record))"
              >查看时间线</IrisButton
            >
          </template>
        </PagedRecordTable>
      </div>
    </IrisCard>
  </section>
</template>

<style scoped>
.task-navigation {
  justify-content: flex-start;
  align-items: center;
  margin-block: 12px;
}
.operations-list-table {
  min-width: 0;
  overflow-x: auto;
}
.operations-list-table :deep(table) {
  min-width: 850px;
}
.operations-list-table :deep(.table-scroll) {
  overflow: visible;
}
</style>
