<script setup lang="ts">
import { computed, ref, watch, onBeforeUnmount } from "vue";
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
} from "@iris-ui-kit/vue";
import type { AeroIdClient } from "../api/aeroId";
import type { JsonRecord, PageData } from "../types";
import { useAsyncResource } from "../hooks/useAsyncResource";
import { useTaskPolling } from "../hooks/useTaskPolling";
import { mergePage } from "./table";
import PageError from "./PageError.vue";
import PageLoading from "./PageLoading.vue";
import RecordTable from "./RecordTable.vue";
import TaskExecutionDiagnostic from "./TaskExecutionDiagnostic.vue";
import OperationResultSummary from "./OperationResultSummary.vue";
import { operationActionReason } from "./operationActionGuidance";
import { operationAuditItem } from "./operationAuditSummary";
import { safeJsonPreview } from "./safeJsonPreview";
import { taskHref } from "../router";
import {
  useCapabilities,
  useRefreshCapabilities,
  refreshAfterAuthorizationError,
} from "../hooks/capabilityContext";
import { useAccountScope } from "../hooks/accountContext";
import {
  operationActionAvailable,
  matchesOriginalTask,
} from "./taskActionEligibility";
import {
  loadOperationReconcileKey,
  operationRunIdempotencyKey,
  saveOperationReconcileKey,
} from "./operationRequestKeys";
const props = defineProps<{ client: AeroIdClient; operationId: string }>();
const capabilities = useCapabilities();
const accountScope = useAccountScope();
const refreshCapabilities = useRefreshCapabilities();
let active = true;
let auditController: AbortController | undefined;
onBeforeUnmount(() => {
  active = false;
  auditController?.abort();
});
function readReconcileKey(): string | undefined {
  try {
    return loadOperationReconcileKey(window.sessionStorage, props.operationId);
  } catch {
    return undefined;
  }
}
function persistReconcileKey(key: string | undefined): void {
  try {
    saveOperationReconcileKey(window.sessionStorage, props.operationId, key);
  } catch {
    // Keep the retry key in memory if session storage is blocked.
  }
}
const emit = defineEmits<{ close: []; changed: [] }>();
function records(value: unknown): JsonRecord[] {
  return Array.isArray(value)
    ? value.filter(
        (item): item is JsonRecord =>
          Boolean(item) && typeof item === "object" && !Array.isArray(item),
      )
    : [];
}
const resource = useAsyncResource((signal) =>
  props.client.getOperation(props.operationId, signal),
);
const auditResource = useAsyncResource((signal) =>
  props.client.listOperationAudit(props.operationId, undefined, signal),
);
const auditPage = ref<PageData<JsonRecord>>({ items: [] });
const consumedAuditCursors = new Set<string>();
const auditLoading = ref(false);
const auditError = ref<Error>();
const reconciling = ref(false);
const reconcileKey = ref(readReconcileKey());
const runningOperation = ref(false);
const runPending = ref(false);
const runConfirmation = ref("");
const actionError = ref<Error>();
const actionAvailable = computed(() =>
  operationActionAvailable(
    resource.data,
    props.operationId,
    capabilities?.(),
    accountScope?.(),
  ),
);
const actionReason = computed(() =>
  resource.data
    ? operationActionReason(
        resource.data,
        capabilities?.(),
        accountScope?.(),
        actionAvailable.value,
      )
    : "原回执尚未读取。",
);
// Observed metadata is a local action gate, not an execution grant or lease.
const canRun = computed(() => {
  const task = resource.data;
  return (
    !resource.loading &&
    !resource.error &&
    actionAvailable.value &&
    (task?.status === "requested" || task?.status === "waiting") &&
    (task?.completed_at === undefined || task?.completed_at === null)
  );
});
const canReconcile = computed(
  () =>
    !resource.loading &&
    !resource.error &&
    actionAvailable.value &&
    resource.data?.status === "unknown",
);
const polling = useTaskPolling(() => resource.data?.status, resource.refresh, {
  blocked: () =>
    resource.loading ||
    Boolean(resource.error) ||
    reconciling.value ||
    runningOperation.value,
});
watch(
  () => auditResource.data,
  (value) => {
    if (value) {
      auditPage.value = value;
      consumedAuditCursors.clear();
      auditError.value = undefined;
    }
  },
  { immediate: true },
);
const auditItems = computed(() =>
  auditPage.value.items.map(operationAuditItem),
);
async function loadMoreAudit(): Promise<void> {
  const cursor = auditPage.value.nextCursor;
  if (!cursor || auditLoading.value) return;
  auditLoading.value = true;
  auditError.value = undefined;
  auditController?.abort();
  const controller = new AbortController();
  auditController = controller;
  try {
    const next = await props.client.listOperationAudit(
      props.operationId,
      cursor,
      controller.signal,
    );
    if (!active) return;
    const merged = mergePage(
      auditPage.value,
      next,
      cursor,
      consumedAuditCursors,
    );
    consumedAuditCursors.add(cursor);
    auditPage.value = merged;
  } catch (reason) {
    if (active)
      auditError.value =
        reason instanceof Error ? reason : new Error("审计时间线读取失败");
  } finally {
    if (active) auditLoading.value = false;
  }
}
const runIsDangerous = computed(() => {
  if (resource.data?.operation_type === "erase") return true;
  return steps.value.some((step) =>
    [step.action, step.compensation_action].some((action) =>
      ["erase", "delete", "source.erase"].includes(String(action)),
    ),
  );
});
const runConfirmationPhrase = computed(() =>
  resource.data?.operation_type === "erase"
    ? "CONFIRM ERASE"
    : "CONFIRM OPERATION RUN",
);
async function executeRun(): Promise<void> {
  if (
    !active ||
    runningOperation.value ||
    reconciling.value ||
    !canRun.value ||
    (runIsDangerous.value &&
      runConfirmation.value !== runConfirmationPhrase.value)
  )
    return;
  runningOperation.value = true;
  actionError.value = undefined;
  try {
    const result = await props.client.runOperation(
      props.operationId,
      operationRunIdempotencyKey(props.operationId),
    );
    if (!active) return;
    if (
      !matchesOriginalTask(
        result,
        resource.data,
        props.operationId,
        "operation_id",
      )
    )
      throw new Error(
        "运行回执与原 Operation 不一致；请核实原回执，不会重发。",
      );
    resource.replace(result);
    runPending.value = false;
    runConfirmation.value = "";
    emit("changed");
  } catch (reason) {
    if (!active) return;
    actionError.value =
      reason instanceof Error
        ? reason
        : new Error("Operation 运行失败；查询原回执，不会创建第二个命令");
    refreshAfterAuthorizationError(reason, refreshCapabilities);
  } finally {
    if (active) runningOperation.value = false;
  }
}
function prepareRun(): void {
  if (!active || runningOperation.value || reconciling.value || !canRun.value)
    return;
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
  if (
    !active ||
    reconciling.value ||
    runningOperation.value ||
    !canReconcile.value
  )
    return;
  const key = reconcileKey.value ?? crypto.randomUUID();
  reconcileKey.value = key;
  persistReconcileKey(key);
  reconciling.value = true;
  actionError.value = undefined;
  try {
    const result = await props.client.reconcileOperation(
      props.operationId,
      key,
    );
    if (!active) return;
    if (
      !matchesOriginalTask(
        result,
        resource.data,
        props.operationId,
        "operation_id",
      )
    )
      throw new Error("对账回执与原 Operation 不一致；保留原键，请核实状态。");
    resource.replace(result);
    reconcileKey.value = undefined;
    persistReconcileKey(undefined);
    emit("changed");
  } catch (reason) {
    if (!active) return;
    actionError.value =
      reason instanceof Error ? reason : new Error("操作对账失败");
    refreshAfterAuthorizationError(reason, refreshCapabilities);
  } finally {
    if (active) reconciling.value = false;
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
      <IrisAlert
        data-operation-admission
        :tone="actionAvailable ? 'info' : 'warning'"
        :title="actionAvailable ? '原回执的下一步' : '运行 / 对账已暂停'"
      >
        {{ actionReason }}
        仍需服务端核原发起者、租户 / 区域 /
        租约及来源权限；本页提示不是执行许可。
      </IrisAlert>
      <IrisAlert v-if="actionError" tone="danger">{{
        actionError.message
      }}</IrisAlert>
      <IrisAlert
        v-if="polling.exhausted"
        tone="warning"
        title="自动刷新已达预算"
      >
        请手动刷新。自动状态查询不运行、不重试、不对账命令；投影可见性与
        Operation 完成是独立状态。
      </IrisAlert>
      <IrisAlert
        v-if="resource.data.status === 'unknown'"
        tone="warning"
        title="远端结果尚未确认"
        >对账会查询来源 operation ID，不会盲目重发原始命令。</IrisAlert
      >
      <TaskExecutionDiagnostic kind="operation" :task="resource.data" />
      <OperationResultSummary :operation="resource.data">
        <template #actions>
          <nav
            data-operation-actions
            class="detail-actions"
            aria-label="原操作动作"
          >
            <IrisButton size="sm" variant="ghost" @click="emit('close')"
              >返回操作列表</IrisButton
            >
            <a
              v-if="taskHref('operations', operationId)"
              :href="taskHref('operations', operationId)"
              >详情永久链接</a
            >
            <IrisButton
              size="sm"
              variant="ghost"
              :disabled="reconciling || runningOperation"
              @click="resource.reload"
              >刷新状态</IrisButton
            >
            <IrisButton
              v-if="
                ['requested', 'waiting'].includes(String(resource.data.status))
              "
              size="sm"
              :variant="runIsDangerous ? 'danger' : 'solid'"
              :loading="runningOperation"
              :disabled="reconciling || runningOperation || !canRun"
              @click="prepareRun"
              >{{
                runIsDangerous ? "准备运行危险 Operation" : "运行 Operation"
              }}</IrisButton
            >
            <IrisButton
              v-if="resource.data.status === 'unknown'"
              size="sm"
              variant="outline"
              :loading="reconciling"
              :disabled="reconciling || runningOperation || !canReconcile"
              @click="void reconcile()"
              >对账未知结果</IrisButton
            >
          </nav>
        </template>
      </OperationResultSummary>
      <details data-operation-diagnostics class="operation-diagnostics">
        <summary>技术元数据与脱敏诊断</summary>
        <div
          class="operation-table-region"
          role="region"
          aria-label="操作技术元数据（可横向滚动）"
          tabindex="0"
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
        </div>
        <h3 class="detail-heading">Saga 步骤元数据</h3>
        <p v-if="steps.length > 32">
          诊断仅展示前32条步骤元数据，不代表完整记录；请核对原回执。
        </p>
        <div
          class="operation-table-region"
          role="region"
          aria-label="步骤技术元数据（可横向滚动）"
          tabindex="0"
        >
          <RecordTable
            :records="steps.slice(0, 32)"
            :columns="[
              { key: 'step_key', label: '步骤' },
              { key: 'source', label: '来源' },
              { key: 'action', label: '动作' },
              { key: 'status', label: '状态', kind: 'status' },
              { key: 'attempt', label: '尝试' },
              { key: 'source_operation_id', label: '来源 Operation ID' },
              { key: 'compensation_status', label: '补偿状态', kind: 'status' },
            ]"
            empty="本次未列出 Saga 步骤元数据"
          />
        </div>
        <div class="detail-json-grid">
          <div>
            <h3>请求</h3>
            <pre class="json-preview">{{
              safeJsonPreview(resource.data.request_payload)
            }}</pre>
          </div>
          <div>
            <h3>结果</h3>
            <pre class="json-preview">{{
              safeJsonPreview(resource.data.result)
            }}</pre>
          </div>
        </div>
      </details>
    </IrisCard>
    <IrisCard variant="outline">
      <template #header>脱敏审计时间线</template>
      <p class="operation-audit-note">
        以下是本次可见的本地追加事实，按接口顺序加载更早记录；不是完整尝试历史、来源回执或全局时间线。事实描述只使用已确认字段，不用旧事实授予当前动作资格。
      </p>
      <PageLoading v-if="auditResource.loading" />
      <PageError
        v-else-if="auditResource.error"
        :error="auditResource.error"
        :retry="auditResource.reload"
      />
      <IrisEmptyState
        v-else-if="auditPage.items.length === 0"
        title="本次查询无可见审计事实"
        description="不证明远端未执行或完整历史为空。"
      />
      <template v-else>
        <IrisTimeline :items="auditItems" />
        <PageError
          v-if="auditError"
          :error="auditError"
          :retry="loadMoreAudit"
        />
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
      <IrisAlert v-if="!canRun" tone="warning" title="运行已暂停">
        当前动作资格或可运行详情未确认；请关闭后刷新原回执。
      </IrisAlert>
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
          :disabled="
            runningOperation ||
            reconciling ||
            !canRun ||
            runConfirmation !== runConfirmationPhrase
          "
          @click="void executeRun()"
          >确认运行</IrisButton
        >
      </div>
    </IrisDialogContent>
  </IrisDialog>
</template>

<style scoped>
.operation-details {
  min-width: 0;
}
.operation-diagnostics {
  margin-block-start: 20px;
  min-width: 0;
}
.operation-diagnostics > summary {
  cursor: pointer;
  margin-block-end: 14px;
}
.operation-table-region {
  overflow-x: auto;
  min-width: 0;
}
.operation-table-region :deep(.table-scroll) {
  overflow: visible;
}
.operation-table-region :deep(table) {
  min-width: 850px;
}
.operation-audit-note {
  margin-block: 0 16px;
  overflow-wrap: anywhere;
}
</style>
