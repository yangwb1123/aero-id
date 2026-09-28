<script setup lang="ts">
import { reactive, ref } from "vue";
import {
  IrisButton,
  IrisCard,
  IrisFormField,
  IrisInput,
} from "@iris-ui-kit/vue";
import type { AdminAccountFilters, AeroIdClient } from "../api/aeroId";
import AdminAccountDetails from "../components/AdminAccountDetails.vue";
import { adminAccountID } from "../components/adminHelpers";
import PagedRecordTable from "../components/PagedRecordTable.vue";
import PageError from "../components/PageError.vue";
import PageHeader from "../components/PageHeader.vue";
import PageLoading from "../components/PageLoading.vue";
import { useAsyncResource } from "../hooks/useAsyncResource";

const { client } = defineProps<{ client: AeroIdClient }>();
const draft = reactive<AdminAccountFilters>({});
const filters = ref<AdminAccountFilters>({});
const selectedAccount = ref<string>();
const resource = useAsyncResource(() =>
  client.listAdminAccounts(filters.value),
);
function apply(): void {
  filters.value = { ...draft };
  resource.reload();
}
function reset(): void {
  Object.keys(draft).forEach(
    (key) => delete draft[key as keyof AdminAccountFilters],
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
      title="账户管理"
      description="Snaplink 决定全局账户读取权限；aero-id 仅展示规范账户、来源引用和版本化聚合投影。"
    />
    <IrisCard variant="outline">
      <template #header>查询条件</template>
      <form class="admin-filter-grid" @submit.prevent="apply">
        <IrisFormField label="账户关键词">
          <IrisInput
            v-model="draft.query"
            type="search"
            placeholder="Account ID 或 Canonical UID"
          />
        </IrisFormField>
        <IrisFormField label="状态">
          <IrisInput v-model="draft.status" placeholder="active / suspended" />
        </IrisFormField>
        <IrisFormField label="Home Region">
          <IrisInput v-model="draft.homeRegion" placeholder="例如 us-west" />
        </IrisFormField>
        <div class="admin-filter-actions">
          <IrisButton type="button" variant="ghost" @click="reset"
            >重置</IrisButton
          >
          <IrisButton type="submit">查询</IrisButton>
        </div>
      </form>
    </IrisCard>
    <IrisCard variant="outline">
      <template #header>规范账户</template>
      <PagedRecordTable
        :page="resource.data"
        :load-page="(cursor) => client.listAdminAccounts(filters, cursor)"
        :columns="[
          { key: 'account_id', label: 'Account ID', aliases: ['id'] },
          { key: 'canonical_uid', label: 'Canonical UID' },
          { key: 'status', label: '状态', kind: 'status' },
          { key: 'home_region', label: 'Home Region' },
        ]"
        empty="没有符合条件的账户"
      >
        <template #actions="{ record }">
          <IrisButton
            v-if="adminAccountID(record)"
            size="sm"
            variant="ghost"
            @click="selectedAccount = adminAccountID(record)"
            >查看来源</IrisButton
          >
        </template>
      </PagedRecordTable>
    </IrisCard>
    <AdminAccountDetails
      v-if="selectedAccount"
      :key="selectedAccount"
      :client="client"
      :account-id="selectedAccount"
      @close="selectedAccount = undefined"
    />
  </section>
</template>
