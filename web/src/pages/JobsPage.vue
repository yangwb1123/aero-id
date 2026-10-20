<script setup lang="ts">
import { computed, ref } from "vue";
import { IrisAlert, IrisButton, IrisCard } from "@iris-ui-kit/vue";
import type { AeroIdClient } from "../api/aeroId";
import JobDetails from "../components/JobDetails.vue";
import CommandPreviewDialog from "../components/CommandPreviewDialog.vue";
import CommandDispositionNotice from "../components/CommandDispositionNotice.vue";
import CommandDatasetSelection from "../components/CommandDatasetSelection.vue";
import PagedRecordTable from "../components/PagedRecordTable.vue";
import PageError from "../components/PageError.vue";
import PageHeader from "../components/PageHeader.vue";
import PageLoading from "../components/PageLoading.vue";
import { useAsyncResource } from "../hooks/useAsyncResource";
import { useCapabilities } from "../hooks/capabilityContext";
import { useAccountScope } from "../hooks/accountContext";
import { browserCommandContext } from "../components/commandEligibility";
import { useCommandPreview } from "../hooks/useCommandPreview";
import { syncJobID } from "../components/jobHelpers";
import { taskHref } from "../router";
import { useTaskNavigation } from "../hooks/useTaskNavigation";
const props = defineProps<{ client: AeroIdClient; jobId?: string }>();
const { client } = props;
const resource = useAsyncResource(async (signal) => {
  const [jobs, datasets] = await Promise.all([
    client.listSyncJobs(undefined, signal),
    client.listDatasets(signal),
  ]);
  return { jobs, datasets };
});
const capabilities = useCapabilities();
const accountScope = useAccountScope();
const {
  selected: selectedJob,
  detailRoot,
  select: openJob,
  close: closeJob,
} = useTaskNavigation("jobs", () => props.jobId);
const selectedDatasets = ref<string[]>([]);
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
  client: () => client,
  channel: "account-jobs",
  accountID: () => browserCommandContext(accountScope?.())?.accountID,
  onReceipt: (intent) => {
    openJob(intent.receipt_id);
    resource.reload();
  },
});
function plan(kind: "sync" | "export") {
  return {
    kind,
    datasets:
      resource.data?.datasets
        .filter((dataset) => selectedDatasets.value.includes(dataset.name))
        .map((dataset) => dataset.name)
        .sort() ?? [],
  };
}
const sourceUnavailable = computed(
  () =>
    resource.data?.datasets
      .filter(
        (dataset) =>
          !capabilities?.()?.datasets.some(
            (item) => item.name === dataset.name && item.enabled,
          ),
      )
      .map((dataset) => dataset.name) ?? [],
);
</script>
<template>
  <nav
    v-if="selectedJob"
    data-task-navigation
    class="detail-actions task-navigation"
    aria-label="任务详情导航"
  >
    <a href="#/jobs" @click.prevent="closeJob">返回任务列表</a>
    <a :href="taskHref('jobs', selectedJob)">原任务详情链接</a>
    <span>链接仅定位原回执；返回或刷新不重新提交任务。</span>
  </nav>
  <section
    v-if="selectedJob"
    ref="detailRoot"
    tabindex="-1"
    aria-label="原任务详情"
  >
    <JobDetails
      :key="selectedJob"
      :client="client"
      :job-id="selectedJob"
      @close="closeJob"
      @changed="resource.reload"
    />
  </section>
  <PageLoading v-if="resource.loading" />
  <PageError
    v-else-if="resource.error"
    :error="resource.error"
    :retry="resource.reload"
  />
  <section v-else-if="resource.data">
    <PageHeader
      title="同步与导出"
      description="先获取服务端预览，再确认创建原任务；unknown 需要对账，不会自动重试写入。"
    >
      <template #actions>
        <IrisButton
          :loading="busy"
          :disabled="
            busy || Boolean(pending?.intentID) || !allowed(plan('sync'))
          "
          @click="void prepare(plan('sync'))"
          >同步所选数据集</IrisButton
        >
        <IrisButton
          variant="outline"
          :loading="busy"
          :disabled="
            busy || Boolean(pending?.intentID) || !allowed(plan('export'))
          "
          @click="void prepare(plan('export'))"
          >创建加密导出</IrisButton
        >
      </template>
    </PageHeader>
    <IrisAlert v-if="error" tone="danger">{{ error.message }}</IrisAlert>
    <CommandDispositionNotice :records="abandoned" />
    <IrisAlert v-if="pending" tone="warning" title="原意图未核实或回执已保留">
      {{ pending.intentID ?? "预览回应未确认" }}：无 ID 时按原键仅 GET 查找；有
      ID 时只查询原意图。 NotFound 或 prepared
      都不证明处置完成，过期不换键，关闭页面不回滚。
      <IrisButton variant="outline" :disabled="busy" @click="void recover()"
        >查询原意图 / 回执</IrisButton
      >
    </IrisAlert>
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
    <IrisCard variant="outline">
      <CommandDatasetSelection
        :datasets="resource.data.datasets"
        :selected="selectedDatasets"
        :disabled="busy || Boolean(pending?.intentID)"
        @change="selectedDatasets = $event"
      />
      <p v-if="sourceUnavailable.length">
        摘要源未启用或尚未确认（只可显式导出已有本地投影）：{{
          sourceUnavailable.join("、")
        }}
      </p>
      <p>
        无能力/账户上下文不放行；旧直接创建的未确认键阻断新预览，旧服务端预览仅能以原键和原范围恢复。创建回执不代表远端完成或投影可见。
      </p>
    </IrisCard>
    <IrisCard variant="outline">
      <div
        class="jobs-list-table"
        role="region"
        aria-label="任务列表（可横向滚动）"
        tabindex="0"
      >
        <PagedRecordTable
          :page="resource.data.jobs"
          :load-page="(cursor, signal) => client.listSyncJobs(cursor, signal)"
          :columns="[
            { key: 'id', label: '任务 ID', aliases: ['job_id'] },
            { key: 'job_type', label: '类型', aliases: ['type'] },
            { key: 'status', label: '状态', kind: 'status' },
            { key: 'created_at', label: '创建时间', kind: 'date' },
            { key: 'completed_at', label: '完成时间', kind: 'date' },
          ]"
          empty="暂无同步或导出任务"
        >
          <template #actions="{ record }"
            ><IrisButton
              v-if="syncJobID(record)"
              size="sm"
              variant="ghost"
              @click="openJob(syncJobID(record))"
              >查看详情</IrisButton
            ></template
          >
        </PagedRecordTable>
      </div>
    </IrisCard>
  </section>
</template>

<style scoped>
.task-navigation {
  justify-content: flex-start;
  align-items: center;
  margin-block: 12px;
}
.jobs-list-table {
  min-width: 0;
  overflow-x: auto;
}
.jobs-list-table :deep(table) {
  min-width: 850px;
}
.jobs-list-table :deep(.table-scroll) {
  overflow: visible;
}
</style>
