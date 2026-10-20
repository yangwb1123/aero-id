<script setup lang="ts">
import { computed, ref, onBeforeUnmount } from "vue";
import {
  IrisAlert,
  IrisButton,
  IrisCard,
  IrisDialog,
  IrisDialogContent,
  IrisDialogDescription,
  IrisDialogTitle,
} from "@iris-ui-kit/vue";
import type { AeroIdClient } from "../api/aeroId";
import { useAsyncResource } from "../hooks/useAsyncResource";
import { useTaskPolling } from "../hooks/useTaskPolling";
import PageError from "./PageError.vue";
import PageLoading from "./PageLoading.vue";
import RecordTable from "./RecordTable.vue";
import TaskExecutionDiagnostic from "./TaskExecutionDiagnostic.vue";
import JobResultSummary from "./JobResultSummary.vue";
import { jobDownloadState } from "./jobExportSummary";
import { safeJsonPreview } from "./safeJsonPreview";
import { taskHref } from "../router";
import { useAccountScope } from "../hooks/accountContext";
import {
  useCapabilities,
  useRefreshCapabilities,
  refreshAfterAuthorizationError,
} from "../hooks/capabilityContext";
import {
  jobReconcileAvailable,
  jobDownloadAvailable,
  matchesOriginalTask,
} from "./taskActionEligibility";
import {
  loadSyncJobReconcileKey,
  saveSyncJobReconcileKey,
} from "./operationRequestKeys";

const props = defineProps<{ client: AeroIdClient; jobId: string }>();
const capabilities = useCapabilities();
const accountScope = useAccountScope();
const refreshCapabilities = useRefreshCapabilities();
let active = true;
const now = ref(Date.now());
const clock = window.setInterval(() => {
  now.value = Date.now();
}, 1000);
onBeforeUnmount(() => {
  active = false;
  window.clearInterval(clock);
});
const emit = defineEmits<{ close: []; changed: [] }>();
function readReconcileKey(): string | undefined {
  try {
    return loadSyncJobReconcileKey(window.sessionStorage, props.jobId);
  } catch {
    return undefined;
  }
}
function persistReconcileKey(key: string | undefined): void {
  try {
    saveSyncJobReconcileKey(window.sessionStorage, props.jobId, key);
  } catch {
    // Keep the retry key in memory if session storage is blocked.
  }
}
const resource = useAsyncResource((signal) =>
  props.client.getSyncJob(props.jobId, signal),
);
const action = ref<"reconcile" | "download">();
const reconcileKey = ref(readReconcileKey());
const actionError = ref<Error>();
const confirmDownload = ref(false);
const downloadReceived = ref(false);
// Observed metadata gates this view, not the durable initiator's authorization.
const canReconcile = computed(
  () =>
    !resource.loading &&
    !resource.error &&
    resource.data?.status === "unknown" &&
    jobReconcileAvailable(
      resource.data,
      props.jobId,
      capabilities?.(),
      accountScope?.(),
    ),
);
const downloadEligible = computed(() =>
  jobDownloadAvailable(
    resource.data,
    props.jobId,
    capabilities?.(),
    accountScope?.(),
  ),
);
const downloadInfo = computed(() =>
  resource.data
    ? jobDownloadState(resource.data, now.value, downloadReceived.value)
    : undefined,
);
const canDownload = computed(
  () =>
    !resource.loading &&
    !resource.error &&
    resource.data?.status === "completed" &&
    downloadEligible.value &&
    !downloadInfo.value?.blocked,
);
const polling = useTaskPolling(() => resource.data?.status, resource.refresh, {
  blocked: () =>
    resource.loading || Boolean(resource.error) || Boolean(action.value),
});
function saveExport(blob: Blob): void {
  const url = URL.createObjectURL(blob);
  const anchor = document.createElement("a");
  anchor.href = url;
  anchor.download = `aero-id-export-${props.jobId}.json`;
  document.body.append(anchor);
  anchor.click();
  anchor.remove();
  window.setTimeout(() => URL.revokeObjectURL(url), 1000);
}
async function reconcile(): Promise<void> {
  if (!active || action.value || !canReconcile.value) return;
  const key = reconcileKey.value ?? crypto.randomUUID();
  reconcileKey.value = key;
  persistReconcileKey(key);
  action.value = "reconcile";
  actionError.value = undefined;
  try {
    const result = await props.client.reconcileSyncJob(props.jobId, key);
    if (!active) return;
    if (!matchesOriginalTask(result, resource.data, props.jobId, "job_id"))
      throw new Error("对账回执与原任务不一致；保留原键，请核实状态。");
    resource.replace(result);
    reconcileKey.value = undefined;
    persistReconcileKey(undefined);
    emit("changed");
  } catch (reason) {
    if (!active) return;
    actionError.value =
      reason instanceof Error ? reason : new Error("任务对账失败");
    refreshAfterAuthorizationError(reason, refreshCapabilities);
  } finally {
    if (active) action.value = undefined;
  }
}
async function download(): Promise<void> {
  if (
    !active ||
    action.value ||
    !canDownload.value ||
    !resource.data ||
    jobDownloadState(resource.data, Date.now(), downloadReceived.value).blocked
  )
    return;
  action.value = "download";
  actionError.value = undefined;
  try {
    const blob = await props.client.downloadExport(props.jobId, true);
    if (!active) return;
    downloadReceived.value = true;
    saveExport(blob);
    confirmDownload.value = false;
  } catch (reason) {
    if (!active) return;
    actionError.value =
      reason instanceof Error
        ? reason
        : new Error(
            "导出下载失败；一次性消费可能已发生，请核实原任务，不会生成新导出",
          );
    refreshAfterAuthorizationError(reason, refreshCapabilities);
  } finally {
    if (active) action.value = undefined;
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
  <template v-else-if="resource.data">
    <IrisCard variant="outline">
      <template #header>任务详情 · {{ jobId }}</template>
      <template #footer>
        <div class="detail-actions">
          <IrisButton size="sm" variant="ghost" @click="emit('close')"
            >关闭</IrisButton
          >
          <a v-if="taskHref('jobs', jobId)" :href="taskHref('jobs', jobId)"
            >详情永久链接</a
          >
          <IrisButton
            size="sm"
            variant="ghost"
            :disabled="Boolean(action)"
            @click="resource.reload"
            >刷新状态</IrisButton
          >
          <IrisButton
            v-if="resource.data.status === 'unknown'"
            size="sm"
            variant="outline"
            :loading="action === 'reconcile'"
            :disabled="Boolean(action) || !canReconcile"
            @click="void reconcile()"
            >对账未知结果</IrisButton
          >
          <IrisButton
            v-if="
              (resource.data.job_type === 'export' ||
                resource.data.type === 'export' ||
                resource.data.job_type === 'audit_export') &&
              resource.data.status === 'completed'
            "
            size="sm"
            :disabled="Boolean(action) || !canDownload"
            @click="confirmDownload = true"
            >一次性下载</IrisButton
          >
        </div>
      </template>
      <IrisAlert
        v-if="polling.exhausted || resource.data.status === 'unknown'"
        tone="warning"
        title="自动刷新已暂停"
      >
        自动状态查询有时间与次数上限，后台标签页暂停查询。unknown
        不是失败，必须对账后才能决定是否重试；自动刷新从不重新提交命令。
      </IrisAlert>
      <IrisAlert
        v-if="
          (resource.data.status === 'unknown' && !canReconcile) ||
          (resource.data.status === 'completed' &&
            ['export', 'audit_export'].includes(
              String(resource.data.job_type),
            ) &&
            !downloadEligible)
        "
        tone="warning"
        >当前动作权限或账户范围未确认时，暂停对账/下载；不以任务 ownership
        或详情链接推断权限。继续执行核对原发起者，下载另核对当前读取者；审计下载同时需要
        audit.export 和 audit.read。</IrisAlert
      >
      <IrisAlert v-if="actionError && !confirmDownload" tone="danger">{{
        actionError.message
      }}</IrisAlert>
      <TaskExecutionDiagnostic kind="sync" :task="resource.data" />
      <JobResultSummary
        :job="resource.data"
        :now="now"
        :download-eligible="downloadEligible"
        :download-received="downloadReceived"
      />
      <details data-job-diagnostics class="job-diagnostics">
        <summary>技术诊断 · 原请求与结果元数据</summary>
        <div
          class="job-diagnostic-table"
          role="region"
          aria-label="任务技术元数据（可横向滚动）"
          tabindex="0"
        >
          <RecordTable
            :records="[resource.data]"
            :columns="[
              { key: 'job_type', label: '类型', aliases: ['type'] },
              { key: 'status', label: '状态', kind: 'status' },
              { key: 'attempt', label: '尝试次数' },
              { key: 'created_at', label: '创建时间', kind: 'date' },
              { key: 'updated_at', label: '更新时间', kind: 'date' },
              { key: 'completed_at', label: '完成时间', kind: 'date' },
            ]"
          />
        </div>
        <div class="detail-json-grid">
          <div>
            <h3>请求</h3>
            <pre class="json-preview">{{
              safeJsonPreview(resource.data.request)
            }}</pre>
          </div>
          <div>
            <h3>结果元数据</h3>
            <pre class="json-preview">{{
              safeJsonPreview(resource.data.result)
            }}</pre>
          </div>
        </div>
      </details>
    </IrisCard>
    <IrisDialog
      :open="confirmDownload"
      @update:open="(open) => action !== 'download' && (confirmDownload = open)"
    >
      <IrisDialogContent>
        <IrisDialogTitle>确认一次性下载</IrisDialogTitle>
        <IrisDialogDescription
          >请确认当前设备和保存位置可信。一次性指针消费与读取审计先于响应提交；丢失回应或删除不确定可能无法重下，不会生成新导出。审计下载同时要求当前
          audit.export 和 audit.read。</IrisDialogDescription
        >
        <IrisAlert
          v-if="downloadInfo"
          :tone="downloadInfo.blocked ? 'warning' : 'info'"
          :title="downloadInfo.title"
          >{{ downloadInfo.detail }}</IrisAlert
        >
        <IrisAlert v-if="!downloadEligible" tone="warning"
          >当前读取者权限或账户范围未确认；请关闭确认并仅刷新原状态/资格，不自动下载。</IrisAlert
        >
        <IrisAlert v-if="actionError" tone="danger">{{
          actionError.message
        }}</IrisAlert>
        <div class="dialog-actions">
          <IrisButton
            variant="ghost"
            :disabled="action === 'download'"
            @click="confirmDownload = false"
            >取消</IrisButton
          >
          <IrisButton
            :loading="action === 'download'"
            :disabled="Boolean(action) || !canDownload"
            @click="void download()"
            >确认并下载</IrisButton
          >
        </div>
      </IrisDialogContent>
    </IrisDialog>
  </template>
</template>

<style scoped>
.job-diagnostics {
  margin-block-start: 20px;
  min-width: 0;
}
.job-diagnostic-table {
  min-width: 0;
  overflow-x: auto;
}
.job-diagnostic-table :deep(table) {
  min-width: 800px;
}
.job-diagnostic-table :deep(.table-scroll) {
  overflow: visible;
}
.job-diagnostics summary {
  cursor: pointer;
  margin-block-end: 14px;
}
</style>
