<script setup lang="ts">
import { computed, ref, watch } from "vue";
import {
  IrisAlert,
  IrisButton,
  IrisCard,
  IrisFormField,
  IrisSelect,
} from "@iris-ui-kit/vue";
import type { AeroIdClient } from "../api/aeroId";
import type {
  CallbackDelivery,
  CallbackDeliveryStatusFilter,
} from "../api/callbackDelivery";
import { useAsyncResource } from "../hooks/useAsyncResource";
import { useAccountScope } from "../hooks/accountContext";
import {
  useCapabilities,
  useRefreshCapabilities,
  refreshAfterAuthorizationError,
} from "../hooks/capabilityContext";
import { browserCommandContext } from "./commandEligibility";
import { callbackReadAvailable } from "./callbackEligibility";
import CallbackDeliveryDetails from "./CallbackDeliveryDetails.vue";
import PagedRecordTable from "./PagedRecordTable.vue";
import PageError from "./PageError.vue";
import PageLoading from "./PageLoading.vue";
const props = defineProps<{
  client: AeroIdClient;
  subscriptionID: string;
  busy: boolean;
}>();
const emit = defineEmits<{ replay: [delivery: CallbackDelivery] }>();
const scope = useAccountScope();
const capabilities = useCapabilities();
const refreshCapabilities = useRefreshCapabilities();
const eligible = computed(() =>
  callbackReadAvailable(capabilities?.(), scope?.()),
);
const status = ref<CallbackDeliveryStatusFilter | "">("");
const selectedID = ref<string>();
const resource = useAsyncResource((signal) => loadPage(undefined, signal));
const statuses = [
  { value: "", label: "全部本地状态" },
  { value: "dead_letter", label: "dead_letter" },
  { value: "pending", label: "pending" },
  { value: "delivering", label: "delivering" },
  { value: "success", label: "success" },
];
async function loadPage(cursor?: string, signal?: AbortSignal) {
  if (!eligible.value)
    throw new Error("当前 Callback 能力尚未确认；不会读取新投递页。");
  const originalScope = scope?.();
  try {
    const page = await props.client.callbacks.listDeliveries(
      props.subscriptionID,
      { cursor, status: status.value || undefined, signal },
    );
    if (
      scope?.() !== originalScope ||
      page.items.some(
        (item) =>
          item.tenant_id !== browserCommandContext(originalScope)?.tenantID ||
          item.subscription_id !== props.subscriptionID,
      )
    )
      throw new Error("投递页与当前租户不一致；不会采用回应或重放。");
    return page;
  } catch (reason) {
    refreshAfterAuthorizationError(reason, refreshCapabilities);
    throw reason;
  }
}
watch(status, () => {
  selectedID.value = undefined;
  resource.reload();
});
function select(id: unknown): void {
  if (eligible.value && !props.busy && typeof id === "string")
    selectedID.value = id;
}
</script>
<template>
  <IrisCard variant="outline">
    <template #header>订阅 {{ subscriptionID }} 的投递</template>
    <IrisAlert tone="info"
      >仅取当前元数据，按 Delivery UUID
      降序分页；不读取事件载荷、远端回应、Secret 或 worker 身份。分页可能与
      worker 更新并发，不是全局时点。</IrisAlert
    >
    <IrisFormField label="本地状态"
      ><IrisSelect
        v-model="status"
        :items="statuses"
        :disabled="!eligible || busy"
    /></IrisFormField>
    <IrisButton
      variant="outline"
      :disabled="!eligible || resource.loading || busy"
      @click="resource.reload"
      >仅刷新投递列表</IrisButton
    >
    <PageLoading v-if="resource.loading" />
    <PageError
      v-else-if="resource.error"
      :error="resource.error"
      :retry="eligible ? resource.reload : undefined"
    />
    <PagedRecordTable
      v-else-if="resource.data"
      :key="status"
      :page="resource.data"
      :load-page="loadPage"
      :columns="[
        { key: 'id', label: 'Delivery ID' },
        { key: 'event_id', label: '原事件 ID' },
        { key: 'action', label: '动作' },
        { key: 'status', label: '本地状态', kind: 'status' },
        { key: 'attempt', label: '已记录尝试' },
        { key: 'error_code', label: '脱敏原因' },
        { key: 'next_attempt_at', label: '下次资格时间', kind: 'date' },
      ]"
      empty="此筛选下没有投递"
    >
      <template #actions="{ record }"
        ><IrisButton
          size="sm"
          variant="outline"
          :disabled="!eligible || busy"
          @click="select(record.id)"
          >查看投递详情</IrisButton
        ></template
      >
    </PagedRecordTable>
    <IrisAlert v-if="!eligible" tone="warning"
      >当前能力未确认；保留已读元数据，不读取新页或准入写入。</IrisAlert
    >
  </IrisCard>
  <CallbackDeliveryDetails
    v-if="selectedID"
    :key="`${subscriptionID}:${selectedID}`"
    :client="client"
    :subscription-i-d="subscriptionID"
    :delivery-i-d="selectedID"
    :busy="busy"
    @replay="emit('replay', $event)"
  />
</template>
