<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from "vue";
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
import { validateDatasetSelection } from "./accountDataHelpers";
import { jobDatasetLabels } from "./jobSummaryFields";
import { resourceDescriptors, summaryRecord } from "./resourceSummary";
import { useCapabilities } from "../hooks/capabilityContext";
import { useAccountScope } from "../hooks/accountContext";
import { capabilityAvailable } from "../api/capabilities";
import { sameCapabilityContext } from "./commandEligibility";
import {
  isProjectionSource,
  sourceProjectionDatasets,
} from "./sourceProjectionScope";
import AccountProjectionContext from "./AccountProjectionContext.vue";
import ResourceSummaryPanel from "./ResourceSummaryPanel.vue";
import RecordTable from "./RecordTable.vue";

const props = defineProps<{
  client: AeroIdClient;
  datasets: Dataset[];
  adminAccountId?: string;
  source?: string;
}>();
const selectedDatasets = ref<string[]>([]);
const consistency = ref<ConsistencyMode>("eventual");
const maxAge = ref("");
const loading = ref(false);
const error = ref<Error>();
const result = ref<AggregateView>();
const requestedScope = ref<string[]>([]);
const readAll = ref(false);
const capabilities = useCapabilities();
// Self-dataset guidance cannot grant or deny access to an admin's other target.
const report = computed(() =>
  props.adminAccountId ? undefined : capabilities?.(),
);
const accountScope = useAccountScope();
const sourceMode = computed(() => props.source !== undefined);
const sourceReadAvailable = computed(
  () =>
    !sourceMode.value ||
    (!props.adminAccountId &&
      isProjectionSource(props.source) &&
      sameCapabilityContext(report.value, accountScope?.()) &&
      capabilityAvailable(report.value, ["account.self.read"])),
);
const availableDatasets = computed(() =>
  sourceMode.value
    ? sourceProjectionDatasets(props.datasets, props.source, report.value)
    : props.datasets,
);
const unsupportedScope = computed(() =>
  requestedScope.value.filter(
    (name) => !resourceDescriptors.some((item) => item.dataset === name),
  ),
);
const consistencyItems = [
  { value: "eventual", label: "Eventual（本地区域投影）" },
  { value: "bounded", label: "Bounded（有界刷新）" },
  { value: "strong", label: "Strong（强制有界刷新，非全局快照）" },
];
const datasetItems = computed(() =>
  availableDatasets.value.map((dataset) => ({
    value: dataset.name,
    label: `${dataset.name} · ${sourceMode.value ? jobDatasetLabels[dataset.name] : dataset.source}${dataset.pii ? " · PII" : ""}`,
  })),
);
const snapshots = computed(() => {
  const values = result.value?.snapshots;
  return Array.isArray(values)
    ? values
        .filter(
          (item) =>
            summaryRecord(item) &&
            requestedScope.value.includes(item.dataset as string),
        )
        .slice(0, 64)
    : [];
});
const querySignature = computed(() =>
  JSON.stringify({
    adminAccountId: props.adminAccountId ?? "",
    source: props.source,
    sourceAccountScope: sourceMode.value ? accountScope?.() : undefined,
    sourceReadAvailable: sourceReadAvailable.value,
    datasets: availableDatasets.value
      .map(({ name, source, pii }) => [name, source, pii])
      .sort(([left], [right]) => String(left).localeCompare(String(right))),
    selected: [...selectedDatasets.value].sort(),
    consistency: consistency.value,
    maxAge: maxAge.value.trim(),
  }),
);
let generation = 0;
let active = true;
let controller: AbortController | undefined;
watch(querySignature, () => {
  generation += 1;
  controller?.abort();
  result.value = undefined;
  requestedScope.value = [];
  error.value = undefined;
  loading.value = false;
});
onBeforeUnmount(() => {
  active = false;
  generation += 1;
  controller?.abort();
});
const sourceErrors = computed(() =>
  Object.entries(result.value?.source_errors ?? {})
    .map(([source, message]) => `${source}: ${message}`)
    .join("；"),
);
function selectAll(): void {
  selectedDatasets.value = availableDatasets.value.map(
    (dataset) => dataset.name,
  );
}
function clearSelection(): void {
  selectedDatasets.value = [];
}
function setDatasets(value: unknown): void {
  const allowed = new Set(
    availableDatasets.value.map((dataset) => dataset.name),
  );
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
  if (!active) return;
  const currentGeneration = ++generation;
  controller?.abort();
  loading.value = false;
  result.value = undefined;
  requestedScope.value = [];
  error.value = undefined;
  if (!sourceReadAvailable.value) {
    error.value = new Error("当前来源读取资格未确认；请核对当前账户范围与能力");
    return;
  }
  if (availableDatasets.value.length === 0) {
    error.value = new Error(
      sourceMode.value
        ? "没有已核实可读的同源数据集；不会改读全部范围"
        : "服务端没有返回可用的数据集 allow-list",
    );
    return;
  }
  let age: string | undefined;
  let requestedDatasets: string[];
  try {
    age = sourceMode.value ? undefined : validateMaxAge(maxAge.value);
    requestedDatasets = validateDatasetSelection(
      selectedDatasets.value,
      availableDatasets.value,
    );
    if (sourceMode.value && requestedDatasets.length === 0)
      throw new Error("请显式选择至少一个同源数据集；不会默认读取全部范围");
  } catch (reason) {
    error.value = reason instanceof Error ? reason : new Error("查询条件无效");
    return;
  }
  const scope = requestedDatasets.length
    ? [...requestedDatasets]
    : availableDatasets.value.map((item) => item.name);
  const all = requestedDatasets.length === 0;
  const submittedSignature = querySignature.value;
  controller = new AbortController();
  const signal = controller.signal;
  loading.value = true;
  try {
    const query = {
      datasets: requestedDatasets,
      consistency: sourceMode.value ? ("eventual" as const) : consistency.value,
      maxAge: age,
    };
    const value = props.adminAccountId
      ? await props.client.getAdminAccountData(
          props.adminAccountId,
          query,
          signal,
        )
      : await props.client.getMyData(query, signal);
    if (
      currentGeneration === generation &&
      sourceReadAvailable.value &&
      (!sourceMode.value ||
        (querySignature.value === submittedSignature &&
          scope.every((name) =>
            availableDatasets.value.some((item) => item.name === name),
          )))
    ) {
      requestedScope.value = scope;
      readAll.value = all;
      result.value = value;
    }
  } catch (reason) {
    if (currentGeneration === generation) {
      error.value =
        reason instanceof Error ? reason : new Error("账户数据读取失败");
    }
  } finally {
    if (currentGeneration === generation) loading.value = false;
  }
}
</script>

<template>
  <IrisCard variant="outline">
    <template #header>按范围浏览账户数据</template>
    <IrisAlert v-if="sourceMode" tone="info" title="同源范围必须显式选择">
      仅列服务端目录与当前读取资格同时确认的同源数据集；未选不会请求。
      此入口固定
      Eventual，只读本地投影；启用状态与历史可读性、来源资源授权各自独立。
    </IrisAlert>
    <IrisAlert v-else tone="info" title="显式读取，不自动同步任务">
      选择器只来自 aero-id 返回的 registry，不表示来源已接通或已授权。Eventual
      只读本地投影；Bounded / Strong
      可能在有界期限内刷新来源，但都不是跨来源同一时点。
      不读取文件字节、消息正文或任意源路径。
    </IrisAlert>
    <IrisAlert
      v-if="sourceMode && !availableDatasets.length"
      tone="warning"
      title="没有已核实可读的同源数据集"
    >
      目录未返回此来源、目录无效或读取资格拒绝/未知；不会改读其他来源或默认全部范围。
    </IrisAlert>
    <p v-if="!result && !loading && !error" class="muted">
      {{
        sourceMode
          ? "尚未读取。请先显式选择同源数据集；留空不读取。"
          : "尚未读取。未选择时沿用全部已注册数据集；"
      }}选择或修改条件不会自动请求，仍需点击「读取账户数据」。
    </p>
    <div class="data-explorer-grid">
      <IrisFormField
        label="数据集"
        :hint="
          sourceMode
            ? '至少选择一个同源数据集；留空不读取'
            : '留空表示请求服务端允许的全部数据集'
        "
      >
        <IrisSelect
          multiple
          :model-value="selectedDatasets"
          :items="datasetItems"
          placeholder="选择一个或多个数据集"
          @update:model-value="setDatasets"
        />
      </IrisFormField>
      <IrisFormField v-if="!sourceMode" label="一致性模式">
        <IrisSelect
          :model-value="consistency"
          :items="consistencyItems"
          @update:model-value="setConsistency"
        />
      </IrisFormField>
      <IrisFormField
        v-if="!sourceMode"
        label="最大允许陈旧时间"
        hint="可选，例如 5m、30s"
      >
        <IrisInput v-model="maxAge" placeholder="5m" />
      </IrisFormField>
      <div class="data-explorer-actions">
        <IrisButton type="button" variant="ghost" @click="clearSelection"
          >清除选择</IrisButton
        >
        <IrisButton type="button" variant="outline" @click="selectAll">{{
          sourceMode ? "全选同源可读范围" : "全选"
        }}</IrisButton>
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
      <section aria-label="本次读取范围" class="data-query-scope">
        <h3>本次读取范围</h3>
        <p>
          {{
            readAll ? "全部已注册数据集（本次显式读取）" : "仅本次所选数据集"
          }}
          · 返回模式：{{ result.consistency ?? "来源未提供" }}
        </p>
        <ul>
          <li v-for="name in requestedScope" :key="name">
            <code>{{ name }}</code>
          </li>
        </ul>
        <p class="muted">
          未选择的数据集不显示为待同步或空结果；改变条件会清除本次展示，不会自动重新读取。
        </p>
      </section>
      <AccountProjectionContext :account="result.account" />
      <p v-if="adminAccountId" class="muted">
        此处为管理员目标账户的服务端回应；当前调用者自己的数据集资格不用于推断该目标权限。
      </p>
      <ResourceSummaryPanel
        :view="result"
        :report="report"
        :datasets="requestedScope"
      />
      <IrisAlert
        v-if="unsupportedScope.length"
        tone="info"
        title="部分数据集暂未提供业务字段视图"
      >
        {{
          unsupportedScope.join("、")
        }}：仅展示下方投影元数据，不通用展开未知来源
        JSON；这不表示数据集为空或来源未接通。
      </IrisAlert>
      <details class="data-projection-details">
        <summary>本次投影元数据与读取边界</summary>
        <p class="muted">
          仅列本次范围内返回的前64行；可见行数不是来源总数，未返回投影不表示远端无资源。来源版本不是全局因果水位。
        </p>
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
          ]"
          empty="本次查询没有返回范围内投影行；请查看所选摘要状态，不表示远端无资源"
        />
      </details>
    </template>
  </IrisCard>
</template>

<style scoped>
.data-explorer-actions {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
}
.data-query-scope {
  margin-block: 20px;
}
.data-query-scope li {
  overflow-wrap: anywhere;
  margin-block: 6px;
}
.data-projection-details {
  margin-block: 20px;
  min-width: 0;
  max-width: 100%;
}
.data-projection-details :deep(.platform-table) {
  min-width: 780px;
}
.data-projection-details summary {
  cursor: pointer;
  margin-block-end: 12px;
}
</style>
