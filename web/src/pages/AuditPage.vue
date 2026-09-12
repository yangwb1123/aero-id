<script setup lang="ts">
import { reactive, ref } from "vue";
import {
  IrisButton,
  IrisCard,
  IrisFormField,
  IrisInput,
} from "@iris-ui-kit/vue";
import type { AeroIdClient, AuditEventFilters } from "../api/aeroId";
import AuditEventDetails from "../components/AuditEventDetails.vue";
import { auditEventID } from "../components/auditHelpers";
import PagedRecordTable from "../components/PagedRecordTable.vue";
import PageError from "../components/PageError.vue";
import PageHeader from "../components/PageHeader.vue";
import PageLoading from "../components/PageLoading.vue";
import { useAsyncResource } from "../hooks/useAsyncResource";
import type { JsonRecord } from "../types";

const { client } = defineProps<{ client: AeroIdClient }>();
const draft = reactive<AuditEventFilters>({});
const filters = ref<AuditEventFilters>({});
const selectedEvent = ref<JsonRecord>();
const resource = useAsyncResource(() => client.listAuditEvents(filters.value));
function apply(): void {
  filters.value = { ...draft };
  resource.reload();
}
function reset(): void {
  Object.keys(draft).forEach(
    (key) => delete draft[key as keyof AuditEventFilters],
  );
  filters.value = {};
  resource.reload();
}
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
      title="审计台账"
      description="查询追加式、脱敏审计事实；链验证由 aero-id 按来源分区重新计算，不声明不存在的全局总序。"
    />
    <IrisCard variant="outline">
      <template #header>审计筛选</template>
      <form class="audit-filter-grid" @submit.prevent="apply">
        <IrisFormField label="来源"
          ><IrisInput
            v-model="draft.source"
            placeholder="aero-id / snaplink / aero-im"
        /></IrisFormField>
        <IrisFormField label="动作"
          ><IrisInput
            v-model="draft.action"
            placeholder="例如 operation.reconcile"
        /></IrisFormField>
        <IrisFormField label="Operation ID"
          ><IrisInput v-model="draft.operationID"
        /></IrisFormField>
        <IrisFormField label="Account ID"
          ><IrisInput v-model="draft.accountID"
        /></IrisFormField>
        <IrisFormField label="Request ID"
          ><IrisInput v-model="draft.requestID"
        /></IrisFormField>
        <div class="admin-filter-actions">
          <IrisButton type="button" variant="ghost" @click="reset"
            >重置</IrisButton
          >
          <IrisButton type="submit">查询</IrisButton>
        </div>
      </form>
    </IrisCard>
    <IrisCard variant="outline">
      <template #header>审计事实</template>
      <PagedRecordTable
        :page="resource.data"
        :load-page="(cursor) => client.listAuditEvents(filters, cursor)"
        :columns="[
          { key: 'occurred_at', label: '发生时间', kind: 'date' },
          { key: 'action', label: '动作' },
          { key: 'source', label: '来源' },
          { key: 'actor_uid', label: 'Actor UID' },
          { key: 'resource_type', label: '资源类型' },
          { key: 'resource_id', label: '资源 ID' },
          { key: 'operation_id', label: 'Operation ID' },
        ]"
        empty="没有符合条件的审计事实"
      >
        <template #actions="{ record }">
          <IrisButton
            v-if="auditEventID(record)"
            size="sm"
            variant="ghost"
            @click="selectedEvent = record"
            >查看证据</IrisButton
          >
        </template>
      </PagedRecordTable>
    </IrisCard>
    <AuditEventDetails
      v-if="selectedEvent"
      :client="client"
      :event="selectedEvent"
      @close="selectedEvent = undefined"
    />
  </section>
</template>
