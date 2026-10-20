<script setup lang="ts">
import { ref } from "vue";
import { IrisAlert, IrisButton, IrisCard } from "@iris-ui-kit/vue";
import type { AdminAccountJobType, AeroIdClient } from "../api/aeroId";
import type { Dataset } from "../types";
import { useCommandPreview } from "../hooks/useCommandPreview";
import CommandDatasetSelection from "./CommandDatasetSelection.vue";
import CommandPreviewDialog from "./CommandPreviewDialog.vue";
import CommandDispositionNotice from "./CommandDispositionNotice.vue";
import JobDetails from "./JobDetails.vue";
const props = defineProps<{
  client: AeroIdClient;
  accountID: string;
  datasets: Dataset[];
}>();
const selected = ref<string[]>([]);
const createdJobID = ref<string>();
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
  onReceipt: (intent) => {
    createdJobID.value = intent.receipt_id;
  },
});
function plan(kind: AdminAccountJobType) {
  return {
    kind,
    datasets: props.datasets
      .filter((dataset) => selected.value.includes(dataset.name))
      .map((dataset) => dataset.name)
      .sort(),
  };
}
</script>
<template>
  <IrisCard variant="outline">
    <template #header>账户任务 · {{ accountID }}</template>
    <IrisAlert v-if="error" tone="danger">{{ error.message }}</IrisAlert>
    <CommandDispositionNotice :records="abandoned" />
    <IrisAlert v-if="pending" tone="warning">
      {{
        pending.intentID ?? "预览回应尚未确认"
      }}：保留原键和范围，过期不自动换键；先核实原意图/回执。
      <IrisButton variant="outline" :disabled="busy" @click="void recover()"
        >查询原意图</IrisButton
      >
    </IrisAlert>
    <IrisAlert v-if="createdJobID" tone="success"
      >原任务回执 {{ createdJobID }} 已确认；创建不代表来源完成，unknown
      必须对账。</IrisAlert
    >
    <CommandDatasetSelection
      :datasets="datasets"
      :selected="selected"
      :disabled="busy || Boolean(pending?.intentID)"
      @change="selected = $event"
    />
    <div class="detail-actions">
      <IrisButton
        v-for="kind in ['sync', 'export', 'erase'] as const"
        :key="kind"
        :variant="kind === 'erase' ? 'danger' : 'outline'"
        :loading="busy"
        :disabled="busy || Boolean(pending?.intentID) || !allowed(plan(kind))"
        @click="void prepare(plan(kind))"
      >
        {{
          {
            sync: "预览所选同步",
            export: "预览账户导出",
            erase: "预览账户擦除",
          }[kind]
        }}
      </IrisButton>
    </div>
    <p>
      逐动作使用当前能力状态；目标租户/主区域、来源擦除启用与 ACL
      由服务端核对。不能借当前菜单、目标 ownership 或摘要启用授予来源命令权限。
    </p>
  </IrisCard>
  <CommandPreviewDialog
    v-if="preview"
    :key="preview.intent_id"
    :client="client"
    :intent="preview"
    :eligible="allowed(preview.plan, preview.account_id)"
    @close="preview = undefined"
    @executed="confirmed"
    @abandoned="confirmed"
  />
  <JobDetails
    v-if="createdJobID"
    :key="createdJobID"
    :client="client"
    :job-id="createdJobID"
    @close="createdJobID = undefined"
  />
</template>
