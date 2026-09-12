<script setup lang="ts">
import { ref } from "vue";
import { IrisAlert, IrisButton, IrisCard } from "@iris-ui-kit/vue";
import type { AeroIdClient } from "../api/aeroId";
import JobDetails from "../components/JobDetails.vue";
import PagedRecordTable from "../components/PagedRecordTable.vue";
import PageError from "../components/PageError.vue";
import PageHeader from "../components/PageHeader.vue";
import PageLoading from "../components/PageLoading.vue";
import { useAsyncResource } from "../hooks/useAsyncResource";
import { syncJobID } from "../components/jobHelpers";
const { client } = defineProps<{ client: AeroIdClient }>();
const resource = useAsyncResource(async () => {
  const [jobs, datasets] = await Promise.all([
    client.listSyncJobs(),
    client.listDatasets(),
  ]);
  return { jobs, datasets };
});
const action = ref<"sync" | "export">();
const actionError = ref<Error>();
const selectedJob = ref<string>();
async function create(kind: "sync" | "export"): Promise<void> {
  action.value = kind;
  actionError.value = undefined;
  try {
    const datasets =
      resource.data?.datasets.map((dataset) => dataset.name) ?? [];
    if (kind === "sync") await client.createSyncJob(datasets);
    else await client.createExportJob(datasets);
    resource.reload();
  } catch (reason) {
    actionError.value =
      reason instanceof Error ? reason : new Error("任务创建失败");
  } finally {
    action.value = undefined;
  }
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
      title="同步与导出"
      description="任务使用幂等键创建；unknown 状态需要对账，不能自动视为失败重试。"
    >
      <template #actions>
        <IrisButton :loading="action === 'sync'" @click="void create('sync')"
          >同步全部数据集</IrisButton
        >
        <IrisButton
          variant="outline"
          :loading="action === 'export'"
          @click="void create('export')"
          >创建加密导出</IrisButton
        >
      </template>
    </PageHeader>
    <IrisAlert v-if="actionError" tone="danger">{{
      actionError.message
    }}</IrisAlert>
    <IrisCard variant="outline">
      <PagedRecordTable
        :page="resource.data.jobs"
        :load-page="(cursor) => client.listSyncJobs(cursor)"
        :columns="[
          { key: 'id', label: '任务 ID', aliases: ['job_id'] },
          { key: 'job_type', label: '类型', aliases: ['type'] },
          { key: 'status', label: '状态', kind: 'status' },
          { key: 'created_at', label: '创建时间', kind: 'date' },
          { key: 'completed_at', label: '完成时间', kind: 'date' },
        ]"
        empty="暂无同步或导出任务"
      >
        <template #actions="{ record }">
          <IrisButton
            v-if="syncJobID(record)"
            size="sm"
            variant="ghost"
            @click="selectedJob = syncJobID(record)"
            >查看详情</IrisButton
          >
        </template>
      </PagedRecordTable>
    </IrisCard>
    <JobDetails
      v-if="selectedJob"
      :client="client"
      :job-id="selectedJob"
      @close="selectedJob = undefined"
      @changed="resource.reload"
    />
  </section>
</template>
