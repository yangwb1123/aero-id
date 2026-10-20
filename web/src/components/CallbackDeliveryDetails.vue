<script setup lang="ts">
import { computed } from "vue";
import { IrisAlert, IrisButton, IrisCard } from "@iris-ui-kit/vue";
import type { AeroIdClient } from "../api/aeroId";
import type { CallbackDelivery } from "../api/callbackDelivery";
import { useAsyncResource } from "../hooks/useAsyncResource";
import { useAccountScope } from "../hooks/accountContext";
import {
  useCapabilities,
  useRefreshCapabilities,
  refreshAfterAuthorizationError,
} from "../hooks/capabilityContext";
import { browserCommandContext } from "./commandEligibility";
import {
  callbackReadAvailable,
  callbackReplayAvailable,
} from "./callbackEligibility";
import PageError from "./PageError.vue";
import PageLoading from "./PageLoading.vue";
import RecordTable from "./RecordTable.vue";
const props = defineProps<{
  client: AeroIdClient;
  subscriptionID: string;
  deliveryID: string;
  busy: boolean;
}>();
const emit = defineEmits<{ replay: [delivery: CallbackDelivery] }>();
const scope = useAccountScope();
const capabilities = useCapabilities();
const refreshCapabilities = useRefreshCapabilities();
const eligible = computed(() =>
  callbackReadAvailable(capabilities?.(), scope?.()),
);
const resource = useAsyncResource(async (signal) => {
  if (!eligible.value)
    throw new Error("当前 Callback 能力尚未确认；不会读取新投递详情。");
  const originalScope = scope?.();
  try {
    const item = await props.client.callbacks.getDelivery(
      props.subscriptionID,
      props.deliveryID,
      signal,
    );
    if (
      scope?.() !== originalScope ||
      item.tenant_id !== browserCommandContext(originalScope)?.tenantID ||
      item.subscription_id !== props.subscriptionID ||
      item.id !== props.deliveryID
    )
      throw new Error("投递详情与原目标/当前租户不一致；不会采用回应或重放。");
    return item;
  } catch (reason) {
    refreshAfterAuthorizationError(reason, refreshCapabilities);
    throw reason;
  }
});
const canReplay = computed(
  () =>
    !resource.loading &&
    !resource.error &&
    !props.busy &&
    callbackReplayAvailable(resource.data, capabilities?.(), scope?.()),
);
function replay(): void {
  if (canReplay.value && resource.data) emit("replay", resource.data);
}
</script>
<template>
  <IrisCard variant="outline">
    <template #header>Delivery {{ deliveryID }}</template>
    <IrisAlert tone="warning"
      >这里只显示当前本地投递行，不是完整尝试时间线或接收端业务回执。delivering
      / 过期租约可能已有远端效果；不能据此重放。HTTP 2xx
      也不证明接收端已完成业务操作。</IrisAlert
    >
    <PageLoading v-if="resource.loading" />
    <PageError
      v-else-if="resource.error"
      :error="resource.error"
      :retry="eligible ? resource.reload : undefined"
    />
    <RecordTable
      v-else-if="resource.data"
      :records="[resource.data]"
      :columns="[
        { key: 'event_id', label: '原事件 ID' },
        { key: 'action', label: '动作' },
        { key: 'status', label: '本地状态', kind: 'status' },
        { key: 'attempt', label: '已记录尝试' },
        { key: 'revision', label: '本地 CAS 版本（非因果水位）' },
        { key: 'lease_generation', label: '本地 Claim 代数' },
        { key: 'lease_until', label: '租约至', kind: 'date' },
        { key: 'status_code', label: '已记录 HTTP 状态' },
        { key: 'error_code', label: '脱敏原因' },
        { key: 'next_attempt_at', label: '下次资格时间', kind: 'date' },
        { key: 'updated_at', label: '更新于', kind: 'date' },
      ]"
    />
    <IrisAlert v-if="!eligible" tone="warning"
      >当前能力查询中、拒绝或不可用；保留原详情，但不借用旧资格发送动作。</IrisAlert
    >
    <div class="admin-filter-actions">
      <IrisButton
        variant="outline"
        :disabled="!eligible || resource.loading || busy"
        @click="resource.reload"
        >仅刷新投递状态</IrisButton
      >
      <IrisButton :disabled="!canReplay" @click="replay"
        >确认此失败投递的重放意图（服务端 CAS 预览）</IrisButton
      >
    </div>
  </IrisCard>
</template>
