<script setup lang="ts">
import { computed, ref } from "vue";
import {
  IrisAlert,
  IrisButton,
  IrisCard,
  IrisFormField,
  IrisInput,
} from "@iris-ui-kit/vue";
import type { AeroIdClient } from "../api/aeroId";
import AnchorRunDialog from "./AnchorRunDialog.vue";
import RecordTable from "./RecordTable.vue";
import type { JsonRecord } from "../types";
const { client } = defineProps<{ client: AeroIdClient }>();
const source = ref("");
const partition = ref("");
const manifest = ref<JsonRecord>();
const querying = ref(false);
const queryError = ref<Error>();
const runKey = ref<string>();
const confirmation = ref("");
const running = ref(false);
const runError = ref<Error>();
const runResult = ref<JsonRecord>();
function records(value: unknown): JsonRecord[] {
  return Array.isArray(value)
    ? value.filter(
        (item): item is JsonRecord =>
          Boolean(item) && typeof item === "object" && !Array.isArray(item),
      )
    : [];
}
async function queryManifest(): Promise<void> {
  const requestedSource = source.value.trim();
  const requestedPartition = partition.value.trim();
  if (!requestedSource || !requestedPartition) {
    manifest.value = undefined;
    queryError.value = new Error("Source 和 Partition 均不能为空");
    return;
  }
  querying.value = true;
  manifest.value = undefined;
  queryError.value = undefined;
  try {
    manifest.value = await client.compliance.getAuditAnchorManifest(
      requestedSource,
      requestedPartition,
    );
  } catch (reason) {
    queryError.value =
      reason instanceof Error ? reason : new Error("Manifest 查询失败");
  } finally {
    querying.value = false;
  }
}
function openRun(): void {
  confirmation.value = "";
  runError.value = undefined;
  runResult.value = undefined;
  runKey.value = crypto.randomUUID();
}
async function runAnchors(): Promise<void> {
  if (!runKey.value) return;
  running.value = true;
  runError.value = undefined;
  try {
    runResult.value = await client.compliance.runAuditAnchors(runKey.value);
    runKey.value = undefined;
    confirmation.value = "";
  } catch (reason) {
    runError.value =
      reason instanceof Error ? reason : new Error("WORM Anchor 执行失败");
  } finally {
    running.value = false;
  }
}
const chains = computed(() => records(runResult.value?.chains));
</script>

<template>
  <IrisCard variant="outline">
    <template #header>WORM Anchor 与 Manifest</template>
    <IrisAlert tone="info" title="不可变归档证据"
      >Anchor 执行会验证每条来源分区链并写入链式 Manifest；Manifest
      查询属于敏感证据读取，成功返回前会持久化审计事实。</IrisAlert
    >
    <div class="detail-actions">
      <IrisButton variant="outline" @click="openRun"
        >锚定全部当前审计链</IrisButton
      >
    </div>
    <IrisAlert v-if="runResult" tone="success"
      >本次已写入
      {{ String(runResult.anchored ?? chains.length) }} 个锚点。</IrisAlert
    >
    <RecordTable
      v-if="chains.length"
      :records="chains"
      :columns="[
        { key: 'source', label: 'Source' },
        { key: 'partition', label: 'Partition' },
        { key: 'sequence', label: '链序号' },
        { key: 'record_hash', label: 'Record Hash' },
        { key: 'anchor_key', label: '归档对象' },
      ]"
    />
    <h3 class="detail-heading">查询单链最新 Manifest</h3>
    <form class="anchor-query-grid" @submit.prevent="void queryManifest()">
      <IrisFormField label="Source" required
        ><IrisInput v-model="source" required placeholder="aero-id"
      /></IrisFormField>
      <IrisFormField label="Partition" required
        ><IrisInput
          v-model="partition"
          required
          placeholder="tenant:&lt;tenant-id&gt;:system"
      /></IrisFormField>
      <IrisButton type="submit" :loading="querying"
        >查询并记录访问审计</IrisButton
      >
    </form>
    <IrisAlert v-if="queryError" tone="danger">{{
      queryError.message
    }}</IrisAlert>
    <RecordTable
      v-if="manifest"
      :records="[manifest]"
      :columns="[
        { key: 'source', label: 'Source' },
        { key: 'source_region', label: '来源区域' },
        { key: 'partition', label: 'Partition' },
        { key: 'chain_sequence', label: '链序号' },
        { key: 'record_hash', label: 'Record Hash' },
        { key: 'signature_version', label: '签名版本' },
        { key: 'previous_anchor_hash', label: '前序 Anchor Hash' },
        { key: 'anchor_hash', label: 'Anchor Hash' },
        { key: 'anchored_at', label: '锚定时间', kind: 'date' },
      ]"
    />
  </IrisCard>
  <AnchorRunDialog
    :idempotency-key="runKey"
    :confirmation="confirmation"
    :running="running"
    :error="runError"
    @update:confirmation="confirmation = $event"
    @cancel="runKey = undefined"
    @confirm="void runAnchors()"
  />
</template>
