<script setup lang="ts">
import { computed, ref } from "vue";
import {
  IrisAlert,
  IrisButton,
  IrisCard,
  IrisFormField,
  IrisInput,
  IrisSelect,
} from "@iris-ui-kit/vue";
import type { AeroIdClient } from "../api/aeroId";
import type { LegacySource } from "../api/migration";
import type { JsonRecord } from "../types";
import RecordTable from "./RecordTable.vue";
const { client } = defineProps<{ client: AeroIdClient }>();
const sourceItems = [
  { value: "aero-vault", label: "Aero Vault（legacy files）" },
  { value: "aero-im", label: "Aero IM（legacy messages/comments）" },
];
const ownerUID = ref("");
const source = ref<LegacySource>("aero-vault");
const limit = ref("1000");
const running = ref(false);
const error = ref<Error>();
const result = ref<JsonRecord>();
function record(value: unknown): JsonRecord {
  return value && typeof value === "object" && !Array.isArray(value)
    ? (value as JsonRecord)
    : {};
}
function strings(value: unknown): string[] {
  return Array.isArray(value)
    ? value.filter((item): item is string => typeof item === "string")
    : [];
}
const digests = computed(() =>
  ["files", "messages", "comments"].flatMap((dataset) => {
    const digest = record(record(result.value?.legacy)[dataset]);
    return Object.keys(digest).length ? [{ dataset, ...digest }] : [];
  }),
);
const counters = computed(() =>
  Object.entries(record(result.value?.source_counters)).map(
    ([counter, value]) => ({ counter, value }),
  ),
);
async function compare(): Promise<void> {
  const uid = ownerUID.value.trim();
  error.value = undefined;
  result.value = undefined;
  if (!uid) {
    error.value = new Error("Legacy Owner UID 不能为空");
    return;
  }
  const compareLimit = Number(limit.value);
  if (
    !Number.isInteger(compareLimit) ||
    compareLimit < 1 ||
    compareLimit > 5000
  ) {
    error.value = new Error("Compare limit 必须是 1–5000 的整数");
    return;
  }
  running.value = true;
  try {
    result.value = await client.migration.compare({
      ownerUID: uid,
      source: source.value,
      limit: compareLimit,
    });
  } catch (reason) {
    error.value =
      reason instanceof Error ? reason : new Error("Shadow compare 失败");
  } finally {
    running.value = false;
  }
}
</script>

<template>
  <IrisCard variant="outline">
    <template #header>Shadow Migration Compare</template>
    <form class="migration-compare-grid" @submit.prevent="void compare()">
      <IrisFormField label="Legacy Owner UID" required
        ><IrisInput v-model="ownerUID" required
      /></IrisFormField>
      <IrisFormField label="目标权威来源"
        ><IrisSelect v-model="source" :items="sourceItems"
      /></IrisFormField>
      <IrisFormField label="单次 Digest 上限" hint="默认 1000，硬上限 5000"
        ><IrisInput v-model="limit" type="number" min="1" max="5000"
      /></IrisFormField>
      <IrisButton type="submit" :loading="running">运行只读 Compare</IrisButton>
    </form>
    <IrisAlert v-if="error" tone="danger">{{ error.message }}</IrisAlert>
    <IrisAlert
      v-if="result?.partial === true"
      tone="warning"
      title="Legacy Digest 被批量上限截断"
      >当前结果只能用于发现差异，不能据此确认迁移完整。提高有界 limit
      或拆分迁移验证范围。</IrisAlert
    >
    <IrisAlert
      v-if="result && result.source_available !== true"
      tone="warning"
      title="目标来源摘要不可用"
      >已返回 legacy digest，但没有完成跨来源一致性判断；不要把 unavailable 当作
      matched。</IrisAlert
    >
    <IrisAlert
      v-if="result?.matched === true && result.partial !== true"
      tone="success"
      >当前有界范围内的来源计数匹配。</IrisAlert
    >
    <IrisAlert
      v-if="strings(result?.drift).length"
      tone="warning"
      title="发现迁移漂移"
      >{{ strings(result?.drift).join("；") }}</IrisAlert
    >
    <template v-if="digests.length">
      <h3 class="detail-heading">Content-free Legacy Digests</h3>
      <RecordTable
        :records="digests"
        :columns="[
          { key: 'dataset', label: 'Legacy Dataset' },
          { key: 'count', label: '计数' },
          { key: 'size_bytes', label: '大小（bytes）' },
          { key: 'truncated', label: '已截断' },
          { key: 'digest', label: 'Digest' },
        ]"
      />
    </template>
    <template v-if="counters.length">
      <h3 class="detail-heading">Source Summary Counters</h3>
      <RecordTable
        :records="counters"
        :columns="[
          { key: 'counter', label: 'Counter' },
          { key: 'value', label: '值' },
        ]"
      />
    </template>
  </IrisCard>
</template>
