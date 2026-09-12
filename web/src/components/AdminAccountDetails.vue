<script setup lang="ts">
import { ref } from "vue";
import { IrisAlert, IrisButton, IrisCard } from "@iris-ui-kit/vue";
import type { AeroIdClient } from "../api/aeroId";
import { useAsyncResource } from "../hooks/useAsyncResource";
import AdminAccountActions from "./AdminAccountActions.vue";
import { records } from "./adminHelpers";
import PageError from "./PageError.vue";
import PageLoading from "./PageLoading.vue";
import RecordTable from "./RecordTable.vue";
import RegionDirectoryPanel from "./RegionDirectoryPanel.vue";
const props = defineProps<{ client: AeroIdClient; accountId: string }>();
const emit = defineEmits<{ close: [] }>();
const resource = useAsyncResource(async () => {
  const [account, overview] = await Promise.all([
    props.client.getAdminAccount(props.accountId),
    props.client.getAdminAccountOverview(props.accountId),
  ]);
  return { account, overview };
});
const promotionMessage = ref<string>();
</script>

<template>
  <PageLoading v-if="resource.loading" />
  <PageError
    v-else-if="resource.error"
    :error="resource.error"
    :retry="resource.reload"
  />
  <template v-else-if="resource.data">
    <IrisCard variant="outline">
      <template #header>账户详情 · {{ accountId }}</template>
      <template #footer
        ><div class="detail-actions">
          <IrisButton size="sm" variant="ghost" @click="emit('close')"
            >关闭</IrisButton
          >
        </div></template
      >
      <IrisAlert
        v-if="resource.data.overview.partial"
        tone="warning"
        title="聚合结果不完整"
        >部分来源不可用或投影过期；下方状态保留各来源的新鲜度，不会伪装为完整结果。</IrisAlert
      >
      <RecordTable
        :records="[
          resource.data.account.account ?? resource.data.overview.account ?? {},
        ]"
        :columns="[
          { key: 'canonical_uid', label: 'Canonical UID' },
          { key: 'status', label: '状态', kind: 'status' },
          { key: 'home_region', label: 'Home Region' },
        ]"
      />
      <h3 class="detail-heading">来源账户</h3>
      <RecordTable
        :records="records(resource.data.account.sources)"
        :columns="[
          { key: 'source', label: '来源' },
          { key: 'source_account_id', label: '来源账户' },
          { key: 'status', label: '状态', kind: 'status' },
          { key: 'source_region', label: '来源区域' },
          { key: 'version', label: '版本' },
          { key: 'generated_at', label: '生成时间', kind: 'date' },
          { key: 'last_synced_at', label: '最后同步', kind: 'date' },
        ]"
        empty="该账户没有来源关联"
      />
      <h3 class="detail-heading">聚合投影新鲜度</h3>
      <RecordTable
        :records="records(resource.data.overview.snapshots)"
        :columns="[
          { key: 'dataset', label: '数据集' },
          { key: 'source', label: '来源' },
          { key: 'status', label: '状态', kind: 'status' },
          { key: 'source_region', label: '来源区域' },
          { key: 'version', label: '版本' },
          { key: 'generated_at', label: '生成时间', kind: 'date' },
          { key: 'last_synced_at', label: '最后同步', kind: 'date' },
        ]"
        empty="该账户尚无聚合投影"
      />
      <h3 class="detail-heading">跨来源成员关系</h3>
      <RecordTable
        :records="records(resource.data.account.memberships)"
        :columns="[
          { key: 'source', label: '来源' },
          { key: 'scope_type', label: '作用域类型' },
          { key: 'scope_id', label: '作用域 ID' },
          { key: 'role', label: '角色' },
          { key: 'status', label: '状态', kind: 'status' },
          { key: 'last_synced_at', label: '最后同步', kind: 'date' },
        ]"
        empty="该账户没有成员关系"
      />
    </IrisCard>
    <IrisAlert v-if="promotionMessage" tone="success">{{
      promotionMessage
    }}</IrisAlert>
    <RegionDirectoryPanel
      :client="client"
      :account-id="accountId"
      @promoted="
        (result) => {
          promotionMessage = `Writer 已提升到 ${String(result.target_region)}，新 Fenced Epoch 为 ${String(result.fenced_epoch)}。`;
          resource.reload();
        }
      "
    />
    <AdminAccountActions :client="client" :account-i-d="accountId" />
  </template>
</template>
