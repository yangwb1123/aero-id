<script setup lang="ts">
import { reactive, ref } from "vue";
import {
  IrisButton,
  IrisCard,
  IrisFormField,
  IrisInput,
} from "@iris-ui-kit/vue";
import type { AeroIdClient, AuditEventFilters } from "../api/aeroId";
import PagedRecordTable from "./PagedRecordTable.vue";
import PageError from "./PageError.vue";
import PageLoading from "./PageLoading.vue";
import { useAsyncResource } from "../hooks/useAsyncResource";

const props = defineProps<{ client: AeroIdClient }>();
const draft = reactive<Pick<AuditEventFilters, "action">>({});
const action = ref<string>();
const resource = useAsyncResource(() =>
  props.client.listAdminAudit(action.value),
);
function reload(): void {
  resource.reload();
}
function apply(): void {
  action.value = draft.action?.trim() || undefined;
  void reload();
}
function reset(): void {
  draft.action = undefined;
  action.value = undefined;
  void reload();
}
</script>

<template>
  <PageLoading v-if="resource.loading && !resource.data" />
  <PageError
    v-else-if="resource.error && !resource.data"
    :error="resource.error"
    :retry="reload"
  />
  <IrisCard v-else variant="outline">
    <template #header>管理员审计日志</template>
    <form class="audit-filter-grid" @submit.prevent="apply">
      <IrisFormField label="动作"
        ><IrisInput v-model="draft.action" placeholder="例如 admin.role.grant"
      /></IrisFormField>
      <div class="admin-filter-actions">
        <IrisButton type="button" variant="ghost" @click="reset"
          >重置</IrisButton
        >
        <IrisButton type="submit">查询</IrisButton>
      </div>
    </form>
    <PageError v-if="resource.error" :error="resource.error" :retry="reload" />
    <PagedRecordTable
      v-if="resource.data"
      :page="resource.data"
      :load-page="(cursor) => client.listAdminAudit(action, cursor)"
      :columns="[
        { key: 'ID', label: '事件 ID', aliases: ['id'] },
        { key: 'Action', label: '动作', aliases: ['action'] },
        { key: 'ActorUID', label: 'Actor UID', aliases: ['actor_uid'] },
        {
          key: 'SubjectType',
          label: '资源类型',
          aliases: ['subject_type', 'resource_type'],
        },
        {
          key: 'SubjectID',
          label: '资源 ID',
          aliases: ['subject_id', 'resource_id'],
        },
        {
          key: 'CreatedAt',
          label: '时间',
          aliases: ['created_at'],
          kind: 'date',
        },
      ]"
      empty="没有管理员审计日志"
    />
  </IrisCard>
</template>
