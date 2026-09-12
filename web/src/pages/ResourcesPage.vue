<script setup lang="ts">
import { IrisAlert, IrisBadge, IrisCard } from "@iris-ui-kit/vue";
import type { AeroIdClient } from "../api/aeroId";
import PageError from "../components/PageError.vue";
import PageHeader from "../components/PageHeader.vue";
import PageLoading from "../components/PageLoading.vue";
import RecordTable from "../components/RecordTable.vue";
import { useAsyncResource } from "../hooks/useAsyncResource";
import type { AggregateView, Dataset, JsonRecord } from "../types";

function datasetRecords(datasets: Dataset[]): JsonRecord[] {
  return datasets.map((dataset) => ({
    name: dataset.name,
    source: dataset.source,
    classification: dataset.pii ? "PII" : "standard",
  }));
}
const { client } = defineProps<{ client: AeroIdClient }>();
const resource = useAsyncResource(async () => {
  const [datasets, resources] = await Promise.all([
    client.listDatasets(),
    client.getResources(),
  ]);
  return { datasets, resources };
});
function sourceErrors(view: AggregateView): string {
  return Object.entries(view.source_errors ?? {})
    .map(([source, message]) => `${source}: ${message}`)
    .join("；");
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
      description="读取 aero-id 本地脱敏投影；不会直接访问 Aero Vault、Aero IM 数据库，也不展示对象字节或消息正文。"
    />
    <IrisAlert
      v-if="resource.data.resources.partial"
      tone="warning"
      title="资源摘要不完整"
    >
      部分来源暂不可用；已返回的来源投影仍然有效，缺失内容不会被解释为空资源。
    </IrisAlert>
    <IrisAlert
      v-if="resource.data.resources.stale_datasets?.length"
      tone="warning"
      title="存在过期投影"
    >
      {{ resource.data.resources.stale_datasets.join("、") }}
    </IrisAlert>
    <IrisAlert
      v-if="sourceErrors(resource.data.resources)"
      tone="danger"
      title="来源错误"
    >
      {{ sourceErrors(resource.data.resources) }}
    </IrisAlert>
    <IrisCard variant="outline">
      <template #header>投影新鲜度</template>
      <template #footer
        ><span class="muted"
          >一致性：{{ resource.data.resources.consistency ?? "eventual" }}</span
        ></template
      >
      <RecordTable
        :records="resource.data.resources.snapshots ?? []"
        :columns="[
          { key: 'dataset', label: '数据集' },
          { key: 'source', label: '权威来源' },
          { key: 'source_region', label: '来源区域' },
          { key: 'status', label: '状态', kind: 'status' },
          { key: 'version', label: '版本' },
          { key: 'generated_at', label: '生成时间', kind: 'date' },
          { key: 'last_synced_at', label: '最后同步', kind: 'date' },
        ]"
        empty="尚无资源投影"
      />
    </IrisCard>
    <IrisCard variant="outline">
      <template #header>允许的数据集</template>
      <template #footer
        ><IrisBadge tone="neutral">Allow-list only</IrisBadge></template
      >
      <RecordTable
        :records="datasetRecords(resource.data.datasets)"
        :columns="[
          { key: 'name', label: '数据集' },
          { key: 'source', label: '权威来源' },
          { key: 'classification', label: '数据分类' },
        ]"
        empty="未配置可查询数据集"
      />
    </IrisCard>
  </section>
</template>
