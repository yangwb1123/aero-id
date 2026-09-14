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
import type { AeroIdClient, ConsistencyMode } from "../api/aeroId";
import type { AggregateView, Dataset } from "../types";
import RecordTable from "./RecordTable.vue";

const props = defineProps<{
  client: AeroIdClient;
  datasets: Dataset[];
  adminAccountId?: string;
}>();
const selectedDatasets = ref<string[]>([]);
const consistency = ref<ConsistencyMode>("eventual");
const maxAge = ref("");
const loading = ref(false);
const error = ref<Error>();
const result = ref<AggregateView>();
const consistencyItems = [
  { value: "eventual", label: "Eventual（本地区域投影）" },
  { value: "bounded", label: "Bounded（有界刷新）" },
  { value: "strong", label: "Strong（有界强一致读取）" },
];
const datasetItems = computed(() =>
  props.datasets.map((dataset) => ({
    value: dataset.name,
    label: `${dataset.name} · ${dataset.source}${dataset.pii ? " · PII" : ""}`,
  })),
);
const snapshots = computed(() => result.value?.snapshots ?? []);
const sourceErrors = computed(() =>
  Object.entries(result.value?.source_errors ?? {})
    .map(([source, message]) => `${source}: ${message}`)
    .join("；"),
);
function selectAll(): void {
  selectedDatasets.value = props.datasets.map((dataset) => dataset.name);
}
function clearSelection(): void {
  selectedDatasets.value = [];
}
function setDatasets(value: unknown): void {
  const allowed = new Set(props.datasets.map((dataset) => dataset.name));
  selectedDatasets.value = Array.isArray(value)
    ? value.filter(
        (item): item is string => typeof item === "string" && allowed.has(item),
      )
    : [];
}
function setConsistency(value: unknown): void {
  if (value === "eventual" || value === "bounded" || value === "strong") {
    consistency.value = value;
  }
}
function validateMaxAge(value: string): string | undefined {
  const normalized = value.trim();
  if (!normalized) return undefined;
  if (!/^[0-9]+(?:ns|us|µs|ms|s|m|h)$/.test(normalized)) {
    throw new Error("Max Age 必须是类似 5m、30s 的 duration");
  }
  return normalized;
}
async function load(): Promise<void> {
  if (props.datasets.length === 0) {
    error.value = new Error("服务端没有返回可用的数据集 allow-list");
    return;
  }
  let age: string | undefined;
  try {
    age = validateMaxAge(maxAge.value);
  } catch (reason) {
    error.value = reason instanceof Error ? reason : new Error("Max Age 无效");
    return;
  }
  loading.value = true;
  error.value = undefined;
  try {
    result.value = props.adminAccountId
      ? await props.client.getAdminAccountData(props.adminAccountId, {
          datasets: selectedDatasets.value,
          consistency: consistency.value,
          maxAge: age,
        })
      : await props.client.getMyData({
          datasets: selectedDatasets.value,
          consistency: consistency.value,
          maxAge: age,
        });
  } catch (reason) {
    error.value =
      reason instanceof Error ? reason : new Error("账户数据读取失败");
  } finally {
    loading.value = false;
  }
}
</script>

<template>
  <IrisCard variant="outline">
    <template #header>Allow-list 账户数据</template>
    <IrisAlert tone="info" title="只读取已注册的数据集">
      选择器只来自 aero-id 返回的
      registry。查询结果保留每个数据集的来源、版本、新鲜度和脱敏数据；不会代理任意源路径、SQL、文件字节或消息正文。
    </IrisAlert>
    <div class="data-explorer-grid">
      <IrisFormField label="数据集" hint="留空表示请求服务端允许的全部数据集">
        <IrisSelect
          multiple
          :model-value="selectedDatasets"
          :items="datasetItems"
          placeholder="选择一个或多个数据集"
          @update:model-value="setDatasets"
        />
      </IrisFormField>
      <IrisFormField label="一致性模式">
        <IrisSelect
          :model-value="consistency"
          :items="consistencyItems"
          @update:model-value="setConsistency"
        />
      </IrisFormField>
      <IrisFormField label="最大允许陈旧时间" hint="可选，例如 5m、30s">
        <IrisInput v-model="maxAge" placeholder="5m" />
      </IrisFormField>
      <div class="data-explorer-actions">
        <IrisButton type="button" variant="ghost" @click="clearSelection"
          >清除选择</IrisButton
        >
        <IrisButton type="button" variant="outline" @click="selectAll"
          >全选</IrisButton
        >
        <IrisButton type="button" :loading="loading" @click="void load()"
          >读取账户数据</IrisButton
        >
      </div>
    </div>
    <IrisAlert v-if="error" tone="danger">{{ error.message }}</IrisAlert>
    <template v-if="result">
      <IrisAlert v-if="result.partial" tone="warning" title="聚合结果不完整"
        >部分来源不可用或投影已过期；缺失数据不会被解释为空数据。</IrisAlert
      >
      <IrisAlert
        v-if="result.stale_datasets?.length"
        tone="warning"
        title="存在陈旧数据集"
        >{{ result.stale_datasets.join("、") }}</IrisAlert
      >
      <IrisAlert v-if="sourceErrors" tone="danger" title="来源读取错误">{{
        sourceErrors
      }}</IrisAlert>
      <RecordTable
        :records="snapshots"
        :columns="[
          { key: 'dataset', label: '数据集' },
          { key: 'source', label: '来源' },
          { key: 'source_region', label: '来源区域' },
          { key: 'status', label: '状态', kind: 'status' },
          { key: 'version', label: '版本' },
          { key: 'generated_at', label: '生成时间', kind: 'date' },
          { key: 'last_synced_at', label: '最后同步', kind: 'date' },
          { key: 'data', label: '脱敏数据' },
        ]"
        empty="本次查询没有可用投影"
      />
    </template>
  </IrisCard>
</template>
