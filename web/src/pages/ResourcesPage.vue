<script setup lang="ts">
import { computed } from "vue";
import { IrisAlert, IrisBadge, IrisButton, IrisCard } from "@iris-ui-kit/vue";
import type { AeroIdClient } from "../api/aeroId";
import PageError from "../components/PageError.vue";
import PageHeader from "../components/PageHeader.vue";
import PageLoading from "../components/PageLoading.vue";
import RecordTable from "../components/RecordTable.vue";
import ResourceSummaryPanel from "../components/ResourceSummaryPanel.vue";
import {
  resourceDescriptors,
  summaryText,
} from "../components/resourceSummary";
import { useAsyncResource } from "../hooks/useAsyncResource";
import { useCapabilities } from "../hooks/capabilityContext";

const { client } = defineProps<{ client: AeroIdClient }>();
const capabilities = useCapabilities();
const report = computed(() => capabilities?.());
const resource = useAsyncResource(async (signal) => {
  const [datasets, resources] = await Promise.all([
    client.listDatasets(signal),
    client.getResources([], signal),
  ]);
  return { datasets, resources };
});
const stale = computed(() =>
  resourceDescriptors
    .filter((item) =>
      resource.data?.resources.stale_datasets?.includes(item.dataset),
    )
    .map((item) => item.dataset)
    .join("、"),
);
const hasSourceErrors = computed(
  () => Object.keys(resource.data?.resources.source_errors ?? {}).length > 0,
);
const registry = computed(
  () =>
    resource.data?.datasets.map((dataset) => ({
      name: summaryText(dataset.name) ?? "字段无效",
      source: summaryText(dataset.source) ?? "字段无效",
      classification: dataset.pii ? "PII" : "standard",
    })) ?? [],
);
function contextValue(field: string): string {
  return summaryText(resource.data?.resources.account?.[field]) ?? "来源未提供";
}
</script>

<template>
  <PageLoading v-if="resource.loading" />
  <PageError
    v-else-if="resource.error"
    :error="resource.error"
    :retry="resource.reload"
  />
  <section v-else-if="resource.data">
    <PageHeader
      title="资源摘要"
      description="查看已保存的 Vault 租户用量、桶引用和 IM 工作空间；刷新只重读本地投影，不执行来源同步。"
    >
      <template #actions
        ><IrisButton type="button" variant="outline" @click="resource.reload"
          >刷新资源摘要</IrisButton
        ><a href="#/jobs">选择范围同步 / 导出</a></template
      >
    </PageHeader>
    <p class="muted resource-account-context">
      账户 {{ contextValue("account_id") }} · 租户
      {{ contextValue("tenant_id") }} · 主区域 {{ contextValue("home_region") }}
    </p>
    <IrisAlert
      v-if="resource.data.resources.partial"
      tone="warning"
      title="资源摘要不完整"
      >部分来源或数据集缺失、陈旧或不可用；下面保留已返回的投影，不把缺失解释为零或空结果。</IrisAlert
    >
    <IrisAlert v-if="stale" tone="warning" title="存在陈旧资源投影">{{
      stale
    }}</IrisAlert>
    <IrisAlert v-if="hasSourceErrors" tone="warning" title="来源状态需要关注"
      >连接未启用、待同步、拒绝或来源故障请查看各摘要；已有投影不等于本次来源读取成功。</IrisAlert
    >
    <ResourceSummaryPanel :view="resource.data.resources" :report="report" />
    <details class="resource-technical-details">
      <summary>数据集目录与读取边界</summary>
      <IrisCard variant="outline">
        <template #header>允许的数据集</template>
        <p>
          这里只是已注册目录，不代表来源已接通、已获授权或已有数据。当前读取模式：{{
            resource.data.resources.consistency ?? "来源未提供"
          }}；版本仅属于对应来源，不是全局因果水位。
        </p>
        <RecordTable
          :records="registry"
          :columns="[
            { key: 'name', label: '数据集' },
            { key: 'source', label: '权威来源' },
            { key: 'classification', label: '数据分类' },
          ]"
          empty="未返回可查询数据集"
        />
        <template #footer
          ><IrisBadge tone="neutral">Allow-list only</IrisBadge></template
        >
      </IrisCard>
    </details>
  </section>
</template>

<style scoped>
.resource-account-context {
  overflow-wrap: anywhere;
  font-size: 13px;
  line-height: 1.7;
}
.resource-technical-details {
  margin-block: 20px;
}
.resource-technical-details summary {
  cursor: pointer;
  margin-block-end: 12px;
}
</style>
