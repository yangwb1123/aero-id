<script setup lang="ts">
import { computed, reactive, ref, watch } from "vue";
import {
  IrisAlert,
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
import type { AuditNavigation } from "../auditNavigation";
import { capabilityAvailable } from "../api/capabilities";
import { useCapabilities } from "../hooks/capabilityContext";
import { taskHref } from "../router";

const props = defineProps<{
  client: AeroIdClient;
  navigation?: AuditNavigation;
}>();
const capabilities = useCapabilities();
const canRead = computed(() =>
  capabilityAvailable(capabilities?.(), ["audit.read"]),
);
const draft = reactive<AuditEventFilters>({});
const filters = ref<AuditEventFilters>({});
const submitted = ref(!props.navigation);
const selectedEvent = ref<JsonRecord>();
const resource = useAsyncResource((signal) => {
  if (!submitted.value) return Promise.resolve(undefined);
  if (!canRead.value)
    return Promise.reject(
      new Error("当前审计读取资格未确认，请刷新能力后显式查询。"),
    );
  return props.client.listAuditEvents(filters.value, undefined, signal);
});
const returnHref = computed(() =>
  props.navigation
    ? taskHref(props.navigation.taskPage, props.navigation.taskID)
    : undefined,
);
watch(
  () => props.navigation,
  (navigation) => {
    Object.keys(draft).forEach(
      (key) => delete draft[key as keyof AuditEventFilters],
    );
    if (navigation)
      draft[navigation.filter === "operation" ? "operationID" : "requestID"] =
        navigation.value;
    filters.value = {};
    selectedEvent.value = undefined;
    submitted.value = false;
    resource.replace(undefined);
  },
  { immediate: Boolean(props.navigation) },
);
function loadPage(cursor: string, signal?: AbortSignal) {
  if (!canRead.value)
    return Promise.reject(new Error("当前审计读取资格未确认"));
  return props.client.listAuditEvents(filters.value, cursor, signal);
}
function apply(): void {
  if (!canRead.value) return;
  filters.value = { ...draft };
  selectedEvent.value = undefined;
  submitted.value = true;
  resource.reload();
}
function reset(): void {
  Object.keys(draft).forEach(
    (key) => delete draft[key as keyof AuditEventFilters],
  );
  filters.value = {};
  selectedEvent.value = undefined;
  submitted.value = false;
  resource.replace(undefined);
}
</script>

<template>
  <section class="audit-page">
    <PageHeader
      title="审计台账"
      description="查询追加式、脱敏审计事实；链验证由 aero-id 按来源分区重新计算，不声明不存在的全局总序。"
    />
    <IrisAlert
      v-if="navigation"
      data-task-audit-context
      tone="info"
      title="从原任务核对审计"
    >
      <p>
        原
        {{
          navigation.filter === "operation" ? "Operation ID" : "Request ID"
        }}：<code>{{ navigation.value }}</code
        >。初始筛选已预填，可自行修改；点击「查询」才读取，两种标识不互相替代。
      </p>
      <a v-if="returnHref" :href="returnHref"
        >返回原{{ navigation.taskPage === "jobs" ? "任务" : "操作" }}结果</a
      >
      <p>
        Request ID 仅关联该请求，不是所有后续尝试；任务 ID、来源操作 ID
        与审计事件 ID 不混用。链接不授予权限，也不执行回放。
      </p>
    </IrisAlert>
    <IrisAlert v-if="!canRead" tone="warning"
      >当前审计读取资格未确认；请刷新当前能力后显式查询，不沿用旧允许。</IrisAlert
    >
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
          <IrisButton type="submit" :disabled="!canRead">查询</IrisButton>
        </div>
      </form>
    </IrisCard>
    <PageLoading v-if="resource.loading && submitted" />
    <PageError
      v-else-if="resource.error"
      :error="resource.error"
      :retry="resource.reload"
    />
    <p v-else-if="!submitted">
      尚未查询。请确认筛选后点击「查询」；清空条件不会自动读取全部台账。
    </p>
    <IrisCard v-else-if="resource.data" variant="outline">
      <template #header>审计事实</template>
      <p data-applied-audit-filter>
        本次已提交的筛选：Operation ID
        {{ filters.operationID || "未限定" }}；Request ID
        {{ filters.requestID || "未限定" }}。
      </p>
      <p>
        本次无可见事实不证明远端未执行或完整历史为空；可见事实也不替代来源效果核验。
      </p>
      <PagedRecordTable
        :page="resource.data"
        :load-page="loadPage"
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
      :key="auditEventID(selectedEvent)"
      :client="client"
      :event="selectedEvent"
      @close="selectedEvent = undefined"
    />
  </section>
</template>

<style scoped>
.audit-page {
  display: grid;
  gap: 18px;
  min-width: 0;
  overflow-wrap: anywhere;
}
p {
  margin: 0;
}
</style>
