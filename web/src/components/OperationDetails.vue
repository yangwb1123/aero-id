<script setup lang="ts">
import { computed, ref, watch } from "vue";
import {
  IrisAlert,
  IrisButton,
  IrisCard,
  IrisDialog,
  IrisDialogContent,
  IrisDialogDescription,
  IrisDialogTitle,
  IrisEmptyState,
  IrisFormField,
  IrisInput,
  IrisTimeline,
  type IrisTimelineItem,
  type IrisTimelineVariant,
} from "@iris-ui-kit/vue";
import type { AeroIdClient } from "../api/aeroId";
import type { JsonRecord, PageData } from "../types";
import { useAsyncResource } from "../hooks/useAsyncResource";
import { mergePage } from "./table";
import PageError from "./PageError.vue";
import PageLoading from "./PageLoading.vue";
import RecordTable from "./RecordTable.vue";
const props = defineProps<{ client: AeroIdClient; operationId: string }>();
const emit = defineEmits<{ close: []; changed: [] }>();
function records(value: unknown): JsonRecord[] {
  return Array.isArray(value)
    ? value.filter(
        (item): item is JsonRecord =>
          Boolean(item) && typeof item === "object" && !Array.isArray(item),
      )
    : [];
}
function jsonPreview(value: unknown): string {
  return JSON.stringify(value ?? {}, null, 2);
}
function displayDate(value: unknown): string {
  const date = new Date(String(value ?? ""));
  return Number.isNaN(date.getTime())
    ? String(value ?? "")
    : date.toLocaleString();
}
function auditVariant(action: string): IrisTimelineVariant {
  if (/(fail|denied|reject|error)/i.test(action)) return "danger";
  if (/(unknown|reconcile|compensat)/i.test(action)) return "warning";
  if (/(complet|succeed|deliver)/i.test(action)) return "success";
  return "info";
}
const resource = useAsyncResource(() =>
  props.client.getOperation(props.operationId),
);
const auditResource = useAsyncResource(() =>
  props.client.listOperationAudit(props.operationId),
);
const auditPage = ref<PageData<JsonRecord>>({ items: [] });
const auditLoading = ref(false);
const auditError = ref<Error>();
const reconciling = ref(false);
const runningOperation = ref(false);
const runPending = ref(false);
const runConfirmation = ref("");
const actionError = ref<Error>();
watch(
  () => auditResource.data,
  (value) => {
    if (value) {
      auditPage.value = value;
      auditError.value = undefined;
    }
  },
  { immediate: true },
);
const auditItems = computed<IrisTimelineItem[]>(() =>
  auditPage.value.items.map((event, index) => {
    const action = String(event.action ?? "operation.event");
    const source = String(event.source ?? "aero-id");
    const resourceText = [event.resource_type, event.resource_id]
      .filter(Boolean)
      .join(" · ");
    return {
      key: String(event.event_id ?? event.id ?? index),
      title: action,
      time: displayDate(event.occurred_at ?? event.created_at),
      description: resourceText ? `${source} · ${resourceText}` : source,
      variant: auditVariant(action),
    };
  }),
);
async function loadMoreAudit(): Promise<void> {
  const cursor = auditPage.value.nextCursor;
  if (!cursor || auditLoading.value) return;
  auditLoading.value = true;
  auditError.value = undefined;
  try {
    auditPage.value = mergePage(
      auditPage.value,
      await props.client.listOperationAudit(props.operationId, cursor),
      cursor,
    );
  } catch (reason) {
    auditError.value =
      reason instanceof Error ? reason : new Error("审计时间线读取失败");
  } finally {
    auditLoading.value = false;
  }
}
const runIsDangerous = computed(() => {
  if (resource.data?.operation_type === "erase") return true;
  return steps.value.some((step) =>
    ["erase", "delete", "source.erase"].includes(String(step.action)),
  );
});
const runConfirmationPhrase = computed(() =>
  resource.data?.operation_type === "erase"
    ? "CONFIRM ERASE"
    : "CONFIRM OPERATION RUN",
);
async function executeRun(): Promise<void> {
  runningOperation.value = true;
  actionError.value = undefined;
  try {
    resource.replace(await props.client.runOperation(props.operationId));
    runPending.value = false;
    runConfirmation.value = "";
    emit("changed");
  } catch (reason) {
    actionError.value =
      reason instanceof Error ? reason : new Error("Operation 运行失败");
  } finally {
    runningOperation.value = false;
  }
}
function prepareRun(): void {
  if (runIsDangerous.value) {
    runConfirmation.value = "";
    runPending.value = true;
    return;
  }
  void executeRun();
}
function cancelRun(): void {
  if (!runningOperation.value) {
    runPending.value = false;
    runConfirmation.value = "";
  }
}
async function reconcile(): Promise<void> {
  reconciling.value = true;
  actionError.value = undefined;
  try {
    resource.replace(await props.client.reconcileOperation(props.operationId));
    emit("changed");
  } catch (reason) {
    actionError.value =
      reason instanceof Error ? reason : new Error("操作对账失败");
  } finally {
    reconciling.value = false;
  }
}
const steps = computed(() => records(resource.data?.steps));
</script>

<template>
  <PageLoading v-if="resource.loading" />
  <PageError
    v-else-if="resource.error"
    :error="resource.error"
    :retry="resource.reload"
  />
  <div v-else-if="resource.data" class="operation-details">
    <IrisCard variant="outline">
      <template #header>Operation 详情 · {{ operationId }}</template>
      <template #footer
        ><div class="detail-actions">
          <IrisButton size="sm" variant="ghost" @click="emit('close')"
            >关闭</IrisButton
          ><IrisButton
            v-if="
              ['requested', 'waiting'].includes(String(resource.data.status))
            "
            size="sm"
            :variant="runIsDangerous ? 'danger' : 'solid'"
            :loading="runningOperation"
            @click="prepareRun"
            >{{
              runIsDangerous ? "准备运行危险 Operation" : "运行 Operation"
            }}</IrisButton
          ><IrisButton
            v-if="resource.data.status === 'unknown'"
            size="sm"
            variant="outline"
            :loading="reconciling"
            @click="void reconcile()"
            >对账未知结果</IrisButton
          >
        </div></template
      >
      <IrisAlert v-if="actionError" tone="danger">{{
        actionError.message
      }}</IrisAlert>
      <IrisAlert
        v-if="resource.data.status === 'unknown'"
        tone="warning"
        title="远端结果尚未确认"
        >对账会查询来源 operation ID，不会盲目重发原始命令。</IrisAlert
      >
      <IrisAlert
        v-if="resource.data.error_code"
        tone="danger"
        :title="String(resource.data.error_code)"
        >{{ String(resource.data.error_message ?? "操作执行失败") }}</IrisAlert
      >
      <RecordTable
        :records="[resource.data]"
        :columns="[
          { key: 'operation_type', label: '类型', aliases: ['type'] },
          { key: 'status', label: '状态', kind: 'status' },
          { key: 'home_region', label: 'Home Region' },
          { key: 'request_id', label: 'Request ID' },
          { key: 'created_at', label: '创建时间', kind: 'date' },
          { key: 'completed_at', label: '完成时间', kind: 'date' },
        ]"
      />
      <h3 class="detail-heading">Saga 步骤</h3>
      <RecordTable
        :records="steps"
        :columns="[
          { key: 'step_key', label: '步骤' },
          { key: 'source', label: '来源' },
          { key: 'action', label: '动作' },
          { key: 'status', label: '状态', kind: 'status' },
          { key: 'attempt', label: '尝试' },
          { key: 'source_operation_id', label: '来源 Operation ID' },
          { key: 'compensation_status', label: '补偿状态', kind: 'status' },
        ]"
        empty="暂无 Saga 步骤"
      />
      <div class="detail-json-grid">
        <div>
          <h3>请求</h3>
          <pre class="json-preview">{{
            jsonPreview(resource.data.request_payload)
          }}</pre>
        </div>
        <div>
          <h3>结果</h3>
          <pre class="json-preview">{{
            jsonPreview(resource.data.result)
          }}</pre>
        </div>
      </div>
    </IrisCard>
    <IrisCard variant="outline">
      <template #header>脱敏审计时间线</template>
      <IrisEmptyState
        v-if="auditPage.items.length === 0"
        title="暂无审计事实"
        description="此操作尚未产生当前调用者可查看的审计事实。"
      />
      <template v-else>
        <IrisTimeline :items="auditItems" />
        <IrisAlert v-if="auditError" tone="danger"
          ><div class="alert-actions">
            <span>{{ auditError.message }}</span
            ><IrisButton
              size="sm"
              variant="outline"
              @click="void loadMoreAudit()"
              >重试</IrisButton
            >
          </div></IrisAlert
        >
        <div
          v-if="auditPage.nextCursor && !auditError"
          class="pagination-actions"
        >
          <IrisButton
            size="sm"
            variant="outline"
            :loading="auditLoading"
            @click="void loadMoreAudit()"
            >加载更早事实</IrisButton
          >
        </div>
      </template>
    </IrisCard>
  </div>
  <IrisDialog :open="runPending" @update:open="(open) => !open && cancelRun()">
    <IrisDialogContent>
      <IrisDialogTitle>确认运行危险 Operation</IrisDialogTitle>
      <IrisDialogDescription>
        Operation {{ operationId }} 包含不可逆来源动作。运行会继续已有持久化
        Saga；不会创建新 Operation，也不会自动重试远端未知结果。
      </IrisDialogDescription>
      <IrisFormField label="确认短语" :hint="runConfirmationPhrase">
        <IrisInput v-model="runConfirmation" autocomplete="off" />
      </IrisFormField>
      <IrisAlert v-if="actionError" tone="danger">{{
        actionError.message
      }}</IrisAlert>
      <div class="dialog-actions">
        <IrisButton
          variant="ghost"
          :disabled="runningOperation"
          @click="cancelRun"
          >取消</IrisButton
        >
        <IrisButton
          variant="danger"
          :loading="runningOperation"
          :disabled="runConfirmation !== runConfirmationPhrase"
          @click="void executeRun()"
          >确认运行</IrisButton
        >
      </div>
    </IrisDialogContent>
  </IrisDialog>
</template>
