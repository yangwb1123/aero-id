<script setup lang="ts">
import { computed } from "vue";
import { IrisAlert, IrisBadge, IrisCard } from "@iris-ui-kit/vue";
import type { AggregateView, CapabilityReport } from "../types";
import { resourceSummaries } from "./resourceSummary";
const props = defineProps<{
  view: AggregateView;
  report?: CapabilityReport;
  datasets?: readonly string[];
}>();
const summaries = computed(() =>
  resourceSummaries(props.view, props.report, Date.now(), props.datasets),
);
</script>

<template>
  <div
    v-if="summaries.length"
    class="resource-summary-grid"
    aria-label="来源资源业务摘要"
  >
    <article
      v-for="summary in summaries"
      :key="summary.dataset"
      :data-resource-dataset="summary.dataset"
    >
      <IrisCard variant="outline">
        <template #header>
          <div class="resource-summary-heading">
            <h2>{{ summary.title }}</h2>
            <IrisBadge :tone="summary.tone">{{ summary.state }}</IrisBadge>
          </div>
        </template>
        <p>{{ summary.detail }}</p>
        <p class="muted">权威来源：{{ summary.source }}</p>
        <p v-if="summary.kind === 'usage'" class="muted">
          租户级用量，不是当前个人用量。B 表示字节；配额 0
          仅为来源原值，不推断无限制或剩余额度。
        </p>
        <IrisAlert
          v-for="warning in summary.warnings"
          :key="warning"
          tone="warning"
          >{{ warning }}</IrisAlert
        >
        <dl v-if="summary.fields.length" class="resource-usage">
          <div v-for="field in summary.fields" :key="field.label">
            <dt>{{ field.label }}</dt>
            <dd>{{ field.value }}</dd>
          </div>
        </dl>
        <p
          v-if="summary.listMessage"
          class="resource-list-summary"
          role="status"
        >
          {{ summary.listMessage }}
        </p>
        <ul
          v-if="summary.kind === 'buckets' && summary.references.length"
          class="resource-buckets"
          aria-label="桶名称引用"
        >
          <li v-for="reference in summary.references" :key="reference.id">
            <code>{{ reference.name }}</code>
          </li>
        </ul>
        <div
          v-if="summary.kind === 'workspaces' && summary.references.length"
          class="table-scroll"
        >
          <table class="platform-table">
            <caption>
              来源报告的工作空间引用
            </caption>
            <thead>
              <tr>
                <th>名称</th>
                <th>工作空间 ID</th>
                <th>短名称（slug）</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="reference in summary.references" :key="reference.id">
                <td>{{ reference.name }}</td>
                <td>
                  <code>{{ reference.id }}</code>
                </td>
                <td>{{ reference.slug }}</td>
              </tr>
            </tbody>
          </table>
        </div>
        <dl v-if="summary.metadata.length" class="resource-metadata">
          <div v-for="field in summary.metadata" :key="field.label">
            <dt>{{ field.label }}</dt>
            <dd>{{ field.value }}</dd>
          </div>
        </dl>
        <template #footer
          ><span class="muted">{{ summary.guidance }}</span></template
        >
      </IrisCard>
    </article>
  </div>
  <p v-if="summaries.length" class="muted resource-boundary">
    仅显示已批准的摘要字段；本接口未提供来源 coverage/complete
    证明，列表可见条数不是全局总数。不同数据集的版本与时间不能合并为同一时点。对象和消息操作仍由
    Vault / IM 执行。
  </p>
</template>

<style scoped>
.resource-summary-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(min(100%, 340px), 1fr));
  gap: 16px;
  margin-block: 16px;
}
.resource-summary-grid article {
  min-width: 0;
}
.resource-summary-heading {
  display: flex;
  align-items: center;
  justify-content: space-between;
  flex-wrap: wrap;
  gap: 8px;
}
.resource-summary-heading h2 {
  margin: 0;
  font-size: 18px;
}
.resource-summary-grid p {
  line-height: 1.6;
}
.resource-usage,
.resource-metadata {
  margin-block: 16px;
}
.resource-usage > div,
.resource-metadata > div {
  padding-block: 8px;
  border-block-end: 1px solid var(--iris-border);
}
.resource-usage dt,
.resource-metadata dt {
  color: var(--iris-muted);
  font-size: 13px;
}
.resource-usage dd,
.resource-metadata dd {
  margin: 4px 0 0;
  overflow-wrap: anywhere;
}
.resource-usage dd {
  font-size: 20px;
  font-weight: 600;
}
.resource-metadata {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 0 12px;
}
.resource-metadata dd {
  font-size: 13px;
}
.resource-buckets {
  padding-inline-start: 22px;
}
.resource-buckets li {
  margin-block: 8px;
  overflow-wrap: anywhere;
}
.resource-list-summary,
.resource-boundary {
  font-size: 13px;
}
.platform-table {
  overflow-wrap: anywhere;
}
caption {
  text-align: start;
  margin-block-end: 8px;
}
</style>
