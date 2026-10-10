<script setup lang="ts">
import { computed } from "vue";
import { IrisAlert, IrisBadge, IrisCard } from "@iris-ui-kit/vue";
import AccountDataPanel from "../components/AccountDataPanel.vue";
import type { AeroIdClient } from "../api/aeroId";
import PageError from "../components/PageError.vue";
import PageHeader from "../components/PageHeader.vue";
import PageLoading from "../components/PageLoading.vue";
import { useAsyncResource } from "../hooks/useAsyncResource";
import { useCapabilities } from "../hooks/capabilityContext";
import SourceFreshnessPanel from "../components/SourceFreshnessPanel.vue";

type Props = { client: AeroIdClient };
const { client } = defineProps<Props>();
const capabilities = useCapabilities();
const capabilityReport = computed(() => capabilities?.());
const resource = useAsyncResource(async () => {
  const [me, datasets, overview] = await Promise.all([
    client.getMe(),
    client.listDatasets(),
    client.getOverview(),
  ]);
  return { me, datasets, overview };
});
const metrics = computed(() => {
  const value = resource.data;
  if (!value) return [] as Array<readonly [string, number]>;
  return [
    ["来源账户", value.me.sources?.length ?? 0],
    ["成员关系", value.me.memberships?.length ?? 0],
    ["已注册数据集", value.datasets.length],
    ["聚合快照", value.overview.snapshots?.length ?? 0],
  ] as const;
});
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
      title="账户概览"
      description="本地区域聚合投影及其来源新鲜度。"
    />
    <IrisAlert
      v-if="resource.data.overview.partial"
      tone="warning"
      title="聚合结果不完整"
    >
      部分来源暂时不可用；页面保留了其余来源结果，不会把缺失数据解释为空数据。
    </IrisAlert>
    <div class="metric-grid">
      <IrisCard
        v-for="[label, value] in metrics"
        :key="label"
        variant="outline"
      >
        <div class="metric-label">{{ label }}</div>
        <div class="metric-value">{{ value }}</div>
      </IrisCard>
    </div>
    <IrisCard variant="outline">
      <template #header>投影状态</template>
      <div class="status-row">
        <span>一致性</span>
        <IrisBadge tone="primary">{{
          resource.data.overview.consistency ?? "eventual"
        }}</IrisBadge>
      </div>
      <div class="status-row">
        <span>过期数据集</span>
        <span>{{
          resource.data.overview.stale_datasets?.join("、") || "无"
        }}</span>
      </div>
      <div
        v-for="[source, message] in Object.entries(
          resource.data.overview.source_errors ?? {},
        )"
        :key="source"
        class="status-row"
      >
        <span>{{ source }}</span>
        <IrisBadge tone="danger">{{ message }}</IrisBadge>
      </div>
    </IrisCard>
    <SourceFreshnessPanel v-if="capabilityReport" :report="capabilityReport" />
    <AccountDataPanel :client="client" :datasets="resource.data.datasets" />
  </section>
</template>
