<script setup lang="ts">
import { computed, ref } from "vue";
import {
  IrisAlert,
  IrisBadge,
  IrisButton,
  IrisCard,
  IrisFormField,
  IrisInput,
  IrisSelect,
  IrisTextarea,
} from "@iris-ui-kit/vue";
import type { AeroIdClient } from "../api/aeroId";
import type { LegacyCutoverStatus } from "../api/migration";
import type { JsonRecord } from "../types";
import MigrationCommandDialog from "./MigrationCommandDialog.vue";
import type { MigrationCommand } from "./dialogTypes";
import RecordTable from "./RecordTable.vue";
import {
  cutoverStatus,
  cutoverTransitions,
  records,
  statusLabels,
  statusTone,
} from "./legacyHelpers";
interface TenantInspection {
  tenantID: string;
  status: LegacyCutoverStatus;
  readiness: JsonRecord;
}
const { client } = defineProps<{ client: AeroIdClient }>();
const tenantID = ref("");
const inspection = ref<TenantInspection>();
const nextStatus = ref<LegacyCutoverStatus>();
const note = ref("");
const inspecting = ref(false);
const command = ref<MigrationCommand>();
const confirmation = ref("");
const running = ref(false);
const error = ref<Error>();
const message = ref<string>();
async function inspect(requestedTenant = tenantID.value.trim()): Promise<void> {
  if (!requestedTenant) {
    error.value = new Error("Tenant ID 不能为空");
    inspection.value = undefined;
    return;
  }
  inspecting.value = true;
  error.value = undefined;
  try {
    const [cutover, readiness] = await Promise.all([
      client.migration.getCutover(requestedTenant),
      client.migration.getCleanupReadiness(requestedTenant),
    ]);
    const status = cutoverStatus(cutover.status);
    if (!status) throw new Error("服务端返回了未知 Cutover 状态");
    inspection.value = { tenantID: requestedTenant, status, readiness };
    nextStatus.value = cutoverTransitions(status)[0];
  } catch (reason) {
    inspection.value = undefined;
    error.value =
      reason instanceof Error ? reason : new Error("租户迁移状态读取失败");
  } finally {
    inspecting.value = false;
  }
}
function prepare(): void {
  error.value = undefined;
  message.value = undefined;
  const trimmedNote = note.value.trim();
  if (
    !inspection.value ||
    inspection.value.tenantID !== tenantID.value.trim() ||
    !nextStatus.value
  ) {
    error.value = new Error("请先重新读取当前 Tenant Cutover 状态");
    return;
  }
  if (!cutoverTransitions(inspection.value.status).includes(nextStatus.value)) {
    error.value = new Error("所选状态不是当前状态允许的下一跳");
    return;
  }
  if (!trimmedNote) {
    error.value = new Error("Cutover 决策必须填写变更说明");
    return;
  }
  confirmation.value = "";
  command.value = {
    kind: "cutover",
    tenantID: inspection.value.tenantID,
    status: nextStatus.value,
    note: trimmedNote,
    idempotencyKey: crypto.randomUUID(),
  };
}
async function execute(): Promise<void> {
  if (command.value?.kind !== "cutover") return;
  running.value = true;
  error.value = undefined;
  try {
    await client.migration.setCutover(command.value);
    const changedTenant = command.value.tenantID;
    message.value = `${changedTenant} 已切换为 ${command.value.status}，决策与审计事实已原子提交。`;
    command.value = undefined;
    confirmation.value = "";
    note.value = "";
    await inspect(changedTenant);
  } catch (reason) {
    error.value =
      reason instanceof Error ? reason : new Error("Cutover 决策执行失败");
  } finally {
    running.value = false;
  }
}
const checks = computed(() =>
  records(inspection.value?.readiness.checks).map((item) => ({
    ...item,
    status: item.ready === true ? "ready" : "blocked",
  })),
);
const transitionItems = computed(() =>
  inspection.value
    ? cutoverTransitions(inspection.value.status).map((status) => ({
        value: status,
        label: statusLabels[status],
      }))
    : [],
);
</script>

<template>
  <IrisCard variant="outline">
    <template #header>Per-tenant Legacy Cutover</template>
    <form class="migration-inspect-grid" @submit.prevent="void inspect()">
      <IrisFormField label="Tenant ID" required
        ><IrisInput
          v-model="tenantID"
          required
          @update:model-value="inspection = undefined"
      /></IrisFormField>
      <IrisButton type="submit" :loading="inspecting"
        >读取状态与 Cleanup Readiness</IrisButton
      >
    </form>
    <IrisAlert v-if="message" tone="success">{{ message }}</IrisAlert>
    <IrisAlert v-if="error && !command" tone="danger">{{
      error.message
    }}</IrisAlert>
    <template v-if="inspection">
      <div class="status-row">
        <span>当前状态</span
        ><IrisBadge :tone="statusTone(inspection.status)">{{
          statusLabels[inspection.status]
        }}</IrisBadge>
      </div>
      <IrisAlert
        :tone="inspection.readiness.ready === true ? 'success' : 'warning'"
        title="Legacy cleanup 是独立的后置门禁"
        >{{
          inspection.readiness.ready === true
            ? "全部清理前置条件已满足；本页面仍不会删除任何 legacy 数据。"
            : "仍有清理条件未满足。该结果不阻止进入 shadow，但禁止据此删除 legacy 写路径或证据。"
        }}</IrisAlert
      >
      <RecordTable
        :records="checks"
        :columns="[
          { key: 'check', label: '检查项' },
          { key: 'status', label: '状态', kind: 'status' },
          { key: 'details', label: '详情' },
        ]"
        empty="服务端未返回 Cleanup Readiness 检查项"
      />
      <div class="migration-cutover-grid">
        <IrisFormField label="下一状态"
          ><IrisSelect
            :model-value="nextStatus"
            :items="transitionItems"
            @update:model-value="
              nextStatus = String($event) as LegacyCutoverStatus
            "
        /></IrisFormField>
        <IrisFormField label="变更说明" required
          ><IrisTextarea v-model="note" required maxlength="1000"
        /></IrisFormField>
        <IrisButton variant="danger" @click="prepare"
          >检查并确认状态切换</IrisButton
        >
      </div>
    </template>
  </IrisCard>
  <MigrationCommandDialog
    :command="command"
    :confirmation="confirmation"
    :running="running"
    :error="error"
    @update:confirmation="confirmation = $event"
    @cancel="command = undefined"
    @confirm="void execute()"
  />
</template>
