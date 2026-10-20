<script setup lang="ts">
import { computed } from "vue";
import { IrisAlert, IrisBadge, IrisButton, IrisCard } from "@iris-ui-kit/vue";
import AccountDataPanel from "../components/AccountDataPanel.vue";
import AccountProjectionContext from "../components/AccountProjectionContext.vue";
import ResourceSummaryPanel from "../components/ResourceSummaryPanel.vue";
import type { AeroIdClient } from "../api/aeroId";
import PageError from "../components/PageError.vue";
import PageHeader from "../components/PageHeader.vue";
import PageLoading from "../components/PageLoading.vue";
import { useAsyncResource } from "../hooks/useAsyncResource";
import { useCapabilities } from "../hooks/capabilityContext";
import SourceFreshnessPanel from "../components/SourceFreshnessPanel.vue";

const { client } = defineProps<{ client: AeroIdClient }>();
const capabilities = useCapabilities();
const capabilityReport = computed(() => capabilities?.());
const resource = useAsyncResource(async (signal) => {
  const [me, datasets, overview] = await Promise.all([
    client.getMe(signal),
    client.listDatasets(signal),
    client.getOverview([], signal),
  ]);
  return { me, datasets, overview };
});
function visibleCount(value: unknown): string {
  return Array.isArray(value) ? String(value.length) : "来源未提供";
}
const metrics = computed(() => {
  const value = resource.data;
  if (!value) return [];
  return [
    ["本次可见来源引用", visibleCount(value.me.sources)],
    ["本次可见成员关系", visibleCount(value.me.memberships)],
    ["已注册目录（不是已接通）", String(value.datasets.length)],
    ["本次返回投影", visibleCount(value.overview.snapshots)],
  ];
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
      description="先看当前账户已保存的用量与资源引用，再按范围浏览或前往同步/导出；本页只读取本地投影。"
    >
      <template #actions>
        <IrisButton type="button" variant="outline" @click="resource.reload"
          >刷新账户概览</IrisButton
        >
        <a href="#/connections">来源与成员关系</a>
        <a href="#/resources">资源摘要</a>
        <a href="#/data">按范围浏览</a>
        <a href="#/jobs">选择范围同步 / 导出</a>
      </template>
    </PageHeader>
    <AccountProjectionContext :account="resource.data.overview.account" />
    <IrisAlert
      v-if="resource.data.overview.partial"
      tone="warning"
      title="聚合结果不完整"
      >部分来源或数据集缺失、陈旧或不可用；已返回内容不是完整来源清单，缺失不解释为零。</IrisAlert
    >
    <IrisAlert
      v-if="resource.data.overview.stale_datasets?.length"
      tone="warning"
      title="存在陈旧数据集"
      >{{ resource.data.overview.stale_datasets.join("、") }}</IrisAlert
    >
    <IrisAlert
      v-if="Object.keys(resource.data.overview.source_errors ?? {}).length"
      tone="warning"
      title="来源状态需要关注"
      >请查看各摘要的未接通、待同步、故障或拒绝提示；已有投影不表示本次来源读取成功。</IrisAlert
    >
    <ResourceSummaryPanel
      :view="resource.data.overview"
      :report="capabilityReport"
    />
    <details class="overview-technical-details">
      <summary>技术统计与来源新鲜度</summary>
      <p class="muted">
        以下是本次回应可见条数，不是全局总数；已注册、连接启用、本地读取资格与来源资源授权互不等同。
      </p>
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
        <template #header>投影读取状态</template>
        <div class="status-row">
          <span>一致性模式（非全局事务）</span>
          <IrisBadge tone="primary">{{
            resource.data.overview.consistency ?? "来源未提供"
          }}</IrisBadge>
        </div>
        <div
          v-for="[source, message] in Object.entries(
            resource.data.overview.source_errors ?? {},
          )"
          :key="source"
          class="status-row"
        >
          <span>{{ source }}</span
          ><IrisBadge tone="danger">{{ message }}</IrisBadge>
        </div>
      </IrisCard>
      <SourceFreshnessPanel
        v-if="capabilityReport"
        :report="capabilityReport"
      />
    </details>
    <AccountDataPanel :client="client" :datasets="resource.data.datasets" />
  </section>
</template>

<style scoped>
.overview-technical-details {
  margin-block: 24px;
}
.overview-technical-details summary {
  cursor: pointer;
  margin-block-end: 12px;
}
</style>
