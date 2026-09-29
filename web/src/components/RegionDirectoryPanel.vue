<script setup lang="ts">
import { ref } from "vue";
import {
  IrisAlert,
  IrisButton,
  IrisCard,
  IrisFormField,
  IrisInput,
} from "@iris-ui-kit/vue";
import type { AeroIdClient } from "../api/aeroId";
import { useAsyncResource } from "../hooks/useAsyncResource";
import PageError from "./PageError.vue";
import PageLoading from "./PageLoading.vue";
import RecordTable from "./RecordTable.vue";
import RegionPromotionDialog from "./RegionPromotionDialog.vue";
import {
  regionEpoch,
  verifyDirectory,
  verifyPromotion,
  type PendingRegionPromotion,
} from "./regionHelpers";
import type { JsonRecord } from "../types";
import { rememberIdempotencyKey } from "./idempotencyHelpers";
const props = defineProps<{ client: AeroIdClient; accountId: string }>();
const emit = defineEmits<{ promoted: [result: JsonRecord] }>();
const resource = useAsyncResource(async () =>
  verifyDirectory(
    await props.client.regions.getDirectory(props.accountId),
    props.accountId,
  ),
);
const targetRegion = ref("");
const pending = ref<PendingRegionPromotion>();
const confirmation = ref("");
const running = ref(false);
const error = ref<Error>();
const intentKeys = new Map<string, string>();
function prepare(): void {
  if (running.value || resource.loading || resource.error) return;
  error.value = undefined;
  const target = targetRegion.value.trim();
  if (!target) {
    error.value = new Error("目标 Region 不能为空");
    return;
  }
  if (target === resource.data?.home_region) {
    error.value = new Error("目标 Region 必须不同于当前 Home Region");
    return;
  }
  confirmation.value = "";
  const expectedEpoch = regionEpoch(resource.data!);
  const intent = rememberIdempotencyKey(
    { accountID: props.accountId, targetRegion: target, expectedEpoch },
    intentKeys,
  );
  pending.value = {
    targetRegion: target,
    expectedEpoch,
    signature: intent.signature,
    idempotencyKey: intent.idempotencyKey,
  };
}
async function promote(): Promise<void> {
  const command = pending.value;
  if (!command || running.value) return;
  running.value = true;
  error.value = undefined;
  try {
    const result = await props.client.regions.promote({
      accountID: props.accountId,
      ...command,
    });
    verifyPromotion(result, props.accountId, command);
    intentKeys.delete(command.signature);
    pending.value = undefined;
    confirmation.value = "";
    targetRegion.value = "";
    emit("promoted", result);
  } catch (reason) {
    error.value =
      reason instanceof Error ? reason : new Error("Home Region 提升失败");
  } finally {
    running.value = false;
  }
}
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
      <template #header>Durable Region Directory</template>
      <RecordTable
        :records="[resource.data]"
        :columns="[
          { key: 'home_region', label: '当前 Writer Region' },
          { key: 'fenced_epoch', label: 'Fenced Epoch' },
          { key: 'resolver_version', label: 'Resolver 版本' },
          { key: 'promoted_at', label: '最近提升时间', kind: 'date' },
        ]"
      />
      <IrisAlert tone="warning" title="提升是故障处置命令"
        >服务端只接受配置允许的 Region，并以当前 Epoch 做一次 CAS；出现
        region.epoch_conflict
        时应重新读取目录和现场状态，不要盲目重试旧请求。</IrisAlert
      >
      <IrisAlert v-if="error && !pending" tone="danger">{{
        error.message
      }}</IrisAlert>
      <form class="region-promotion-row" @submit.prevent="prepare">
        <IrisFormField
          label="目标 Region"
          hint="请输入服务端 account.regions 中已配置的精确标识"
        >
          <IrisInput
            v-model="targetRegion"
            required
            maxlength="64"
            placeholder="例如 us-east"
          />
        </IrisFormField>
        <IrisButton type="submit" variant="danger" :disabled="running"
          >检查提升</IrisButton
        >
      </form>
    </IrisCard>
    <RegionPromotionDialog
      :account-i-d="accountId"
      :pending="pending"
      :confirmation="confirmation"
      :running="running"
      :error="error"
      @update:confirmation="confirmation = $event"
      @cancel="pending = undefined"
      @confirm="void promote()"
    />
  </template>
</template>
