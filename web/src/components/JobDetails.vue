<script setup lang="ts">
import { ref } from "vue";
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
import PageError from "./PageError.vue";
import PageLoading from "./PageLoading.vue";
import RecordTable from "./RecordTable.vue";

const props = defineProps<{ client: AeroIdClient; jobId: string }>();
const emit = defineEmits<{ close: []; changed: [] }>();
const resource = useAsyncResource(() => props.client.getSyncJob(props.jobId));
const action = ref<"reconcile" | "download">();
const actionError = ref<Error>();
const confirmDownload = ref(false);
function jsonPreview(value: unknown): string {
  return JSON.stringify(value ?? {}, null, 2);
}
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
  action.value = "reconcile";
  actionError.value = undefined;
  try {
    resource.replace(await props.client.reconcileSyncJob(props.jobId));
    emit("changed");
  } catch (reason) {
    actionError.value =
      reason instanceof Error ? reason : new Error("任务对账失败");
  } finally {
    action.value = undefined;
  }
}
async function download(): Promise<void> {
  action.value = "download";
  actionError.value = undefined;
  try {
    saveExport(await props.client.downloadExport(props.jobId, true));
    confirmDownload.value = false;
  } catch (reason) {
    actionError.value =
      reason instanceof Error ? reason : new Error("导出下载失败");
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
  <template v-else-if="resource.data">
    <IrisCard variant="outline">
      <template #header>任务详情 · {{ jobId }}</template>
      <template #footer>
        <div class="detail-actions">
          <IrisButton size="sm" variant="ghost" @click="emit('close')"
            >关闭</IrisButton
          >
          <IrisButton
            v-if="resource.data.status === 'unknown'"
            size="sm"
            variant="outline"
            :loading="action === 'reconcile'"
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
            @click="confirmDownload = true"
            >一次性下载</IrisButton
          >
        </div>
      </template>
      <IrisAlert v-if="actionError && !confirmDownload" tone="danger">{{
        actionError.message
      }}</IrisAlert>
      <IrisAlert
        v-if="resource.data.error_code"
        tone="danger"
        :title="String(resource.data.error_code)"
        >{{ String(resource.data.error_message ?? "任务执行失败") }}</IrisAlert
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
      <div class="detail-json-grid">
        <div>
          <h3>请求</h3>
          <pre class="json-preview">{{
            jsonPreview(resource.data.request)
          }}</pre>
        </div>
        <div>
          <h3>脱敏结果</h3>
          <pre class="json-preview">{{
            jsonPreview(resource.data.result)
          }}</pre>
        </div>
      </div>
    </IrisCard>
    <IrisDialog
      :open="confirmDownload"
      @update:open="(open) => action !== 'download' && (confirmDownload = open)"
    >
      <IrisDialogContent>
        <IrisDialogTitle>确认一次性下载</IrisDialogTitle>
        <IrisDialogDescription
          >服务端会在本次成功读取后销毁导出制品。请确认当前设备和保存位置可信。</IrisDialogDescription
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
          <IrisButton :loading="action === 'download'" @click="void download()"
            >确认并下载</IrisButton
          >
        </div>
      </IrisDialogContent>
    </IrisDialog>
  </template>
</template>
