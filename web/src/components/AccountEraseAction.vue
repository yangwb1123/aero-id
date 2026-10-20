<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { IrisAlert, IrisButton, IrisCard } from "@iris-ui-kit/vue";
import type { AeroIdClient } from "../api/aeroId";
import { useCapabilities } from "../hooks/capabilityContext";
import { useCommandPreview } from "../hooks/useCommandPreview";
import { commandUUID } from "./commandPreviewStorage";
import { loadEraseIntent } from "./eraseIntentStorage";
import CommandDatasetSelection from "./CommandDatasetSelection.vue";
import CommandPreviewDialog from "./CommandPreviewDialog.vue";
import CommandDispositionNotice from "./CommandDispositionNotice.vue";
import JobDetails from "./JobDetails.vue";
const props = defineProps<{ client: AeroIdClient; accountID?: string }>();
const capabilities = useCapabilities();
const selected = ref<string[]>([]);
const legacyBlocked = ref(false);
const jobID = ref<string>();
const showJobDetails = ref(false);
const {
  preview,
  busy,
  error,
  pending,
  abandoned,
  allowed,
  prepare,
  recover,
  confirmed,
} = useCommandPreview({
  client: () => props.client,
  channel: "account-jobs",
  accountID: () => props.accountID,
  legacyBlocked: () => legacyBlocked.value,
  onReceipt: (intent) => {
    jobID.value = intent.receipt_id;
    showJobDetails.value = true;
  },
});
const datasets = computed(
  () =>
    capabilities?.()?.datasets.filter((dataset) => dataset.registered) ?? [],
);
const plan = computed(() => ({
  kind: "erase" as const,
  datasets: datasets.value
    .filter((dataset) => selected.value.includes(dataset.name))
    .map((dataset) => dataset.name)
    .sort(),
}));
const eligible = computed(
  () => props.accountID === capabilities?.()?.account_id && allowed(plan.value),
);
watch(
  () => props.accountID,
  () => {
    selected.value = [];
    jobID.value = undefined;
    showJobDetails.value = false;
    legacyBlocked.value = false;
    if (!props.accountID) return;
    try {
      const storage = window.sessionStorage;
      // The old account-only key has no issuer/client binding and must never be
      // migrated into a new endpoint. Even corrupt old state needs verification.
      legacyBlocked.value = Boolean(
        storage.getItem(`aero-id:erase-intent:${props.accountID.trim()}`),
      );
      const legacy = loadEraseIntent(storage, props.accountID);
      if (commandUUID(legacy.jobID)) jobID.value = legacy.jobID;
    } catch {
      /* No storage: retain only in-memory state; never replay automatically. */
    }
  },
  { immediate: true },
);
</script>
<template>
  <IrisCard variant="outline">
    <template #header>账户生命周期</template>
    <IrisAlert tone="warning" title="不可逆的跨来源擦除">
      先显式选择范围并获取服务端计划，再输入目标 Account ID
      确认。确认只创建异步任务，不是删除证明；超时保持 unknown。 不能解除 Legal
      Hold 或把本地写屏障宣称为跨区域/来源删除完成。
    </IrisAlert>
    <IrisAlert v-if="legacyBlocked" tone="warning">
      旧接口存在擦除请求或回执；请先核实旧任务，不会复用未绑定的旧键或换接口创建第二个擦除。
    </IrisAlert>
    <CommandDispositionNotice :records="abandoned" />
    <IrisAlert v-if="pending" tone="warning">
      原意图
      {{ pending.intentID ?? "预览回应未确认" }}
      已保留；仅核实/恢复原意图，不自动换键。
      <IrisButton variant="outline" :disabled="busy" @click="void recover()"
        >查询原意图 / 原任务</IrisButton
      >
    </IrisAlert>
    <IrisAlert v-if="error" tone="danger">{{ error.message }}</IrisAlert>
    <CommandDatasetSelection
      :datasets="datasets"
      :selected="selected"
      :disabled="busy || Boolean(pending?.intentID) || legacyBlocked"
      @change="selected = $event"
    />
    <IrisButton
      variant="danger"
      :loading="busy"
      :disabled="
        busy || Boolean(jobID) || Boolean(pending?.intentID) || !eligible
      "
      @click="void prepare(plan)"
      >预览擦除我的账户</IrisButton
    >
    <IrisButton v-if="jobID" variant="outline" @click="showJobDetails = true"
      >查看原任务 / 回执</IrisButton
    >
  </IrisCard>
  <CommandPreviewDialog
    v-if="preview"
    :key="preview.intent_id"
    :client="client"
    :intent="preview"
    :eligible="
      preview.plan.kind === 'erase' &&
      props.accountID === preview.account_id &&
      allowed(preview.plan, preview.account_id)
    "
    @close="preview = undefined"
    @executed="confirmed"
    @abandoned="confirmed"
  />
  <JobDetails
    v-if="jobID && showJobDetails"
    :key="jobID"
    :client="client"
    :job-id="jobID"
    @close="showJobDetails = false"
  />
</template>
