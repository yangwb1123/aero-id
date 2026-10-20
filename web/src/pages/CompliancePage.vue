<script setup lang="ts">
import { onBeforeUnmount, ref } from "vue";
import {
  IrisAlert,
  IrisButton,
  IrisCard,
  IrisFormField,
  IrisInput,
  IrisSelect,
  IrisTextarea,
} from "@iris-ui-kit/vue";
import type { AeroIdClient } from "../api/aeroId";
import type { AuditHoldInput, AuditHoldScope } from "../api/compliance";
import AuditAnchorsPanel from "../components/AuditAnchorsPanel.vue";
import ComplianceCommandDialog from "../components/ComplianceCommandDialog.vue";
import type { ComplianceCommand } from "../components/dialogTypes";
import JobDetails from "../components/JobDetails.vue";
import { commandUUID } from "../components/commandPreviewStorage";
import PageError from "../components/PageError.vue";
import PageHeader from "../components/PageHeader.vue";
import PageLoading from "../components/PageLoading.vue";
import RecordTable from "../components/RecordTable.vue";
import { useAsyncResource } from "../hooks/useAsyncResource";
import { normalizeHoldExpiry } from "../components/complianceHelpers";
import { rememberIdempotencyKey } from "../components/idempotencyHelpers";
import type { JsonRecord } from "../types";

const scopeItems = [
  { value: "tenant", label: "Tenant" },
  { value: "account", label: "Account" },
  { value: "actor", label: "Actor" },
];
const { client } = defineProps<{ client: AeroIdClient }>();
const resource = useAsyncResource((signal) =>
  client.compliance.listHolds(signal),
);
const scopeType = ref<AuditHoldScope>("account");
const scopeID = ref("");
const reason = ref("");
const expiresAt = ref("");
const command = ref<ComplianceCommand>();
const confirmation = ref("");
const running = ref(false);
const commandError = ref<Error>();
const message = ref<string>();
const jobID = ref<string>();
const intentKeys = new Map<string, string>();
let active = true;
onBeforeUnmount(() => {
  active = false;
});
function holdRemoval(record: JsonRecord): ComplianceCommand | undefined {
  const scopeType = record.scope_type;
  const id = record.scope_id;
  if (
    (scopeType === "tenant" ||
      scopeType === "account" ||
      scopeType === "actor") &&
    typeof id === "string" &&
    id.length > 0
  )
    return { kind: "remove", scopeType, scopeID: id };
  return undefined;
}
function prepareHold(): void {
  if (!active || running.value) return;
  commandError.value = undefined;
  try {
    const id = scopeID.value.trim();
    const holdReason = reason.value.trim();
    if (!id) throw new Error("Scope ID 不能为空");
    if (!holdReason) throw new Error("必须填写保全原因");
    const input: AuditHoldInput = {
      scopeType: scopeType.value,
      scopeID: id,
      reason: holdReason,
      expiresAt: normalizeHoldExpiry(expiresAt.value),
    };
    const intent = rememberIdempotencyKey(
      { kind: "put", ...input },
      intentKeys,
    );
    command.value = {
      kind: "put",
      input,
      signature: intent.signature,
      idempotencyKey: intent.idempotencyKey,
    };
    confirmation.value = "";
  } catch (reasonValue) {
    commandError.value =
      reasonValue instanceof Error
        ? reasonValue
        : new Error("Legal Hold 输入无效");
  }
}
function openCommand(next: ComplianceCommand | undefined): void {
  if (!active || running.value || !next) return;
  confirmation.value = "";
  commandError.value = undefined;
  command.value = next;
}
function prepareAuditExport(): void {
  if (!active || running.value) return;
  const intent = rememberIdempotencyKey({ kind: "audit-export" }, intentKeys);
  openCommand({
    kind: "export",
    signature: intent.signature,
    idempotencyKey: intent.idempotencyKey,
  });
}
async function runCommand(): Promise<void> {
  const current = command.value;
  if (!active || !current || running.value) return;
  if (
    current.kind === "remove" &&
    (resource.loading ||
      resource.error ||
      confirmation.value !== current.scopeID)
  )
    return;
  running.value = true;
  commandError.value = undefined;
  message.value = undefined;
  try {
    if (current.kind === "put") {
      await client.compliance.putHold(current.input, current.idempotencyKey);
      intentKeys.delete(current.signature);
      message.value = "Legal Hold 已持久化，并已原子写入审计事实。";
      scopeID.value = "";
      reason.value = "";
      expiresAt.value = "";
      resource.reload();
    } else if (current.kind === "remove") {
      await client.compliance.removeHold(current.scopeType, current.scopeID);
      message.value = "Legal Hold 已解除，并已写入新的审计事实。";
      resource.reload();
    } else {
      const id = (
        await client.compliance.createAuditExport(current.idempotencyKey)
      ).job_id;
      if (!commandUUID(id))
        throw new Error(
          "任务可能已创建；响应任务 ID 无效，请先核实原任务列表与审计，保留当前幂等键。",
        );
      intentKeys.delete(current.signature);
      jobID.value = id;
      message.value = `审计导出任务 ${id} 已持久化。`;
    }
    command.value = undefined;
    confirmation.value = "";
  } catch (reasonValue) {
    commandError.value =
      reasonValue instanceof Error
        ? reasonValue
        : new Error("合规命令执行失败");
    if (current.kind === "remove") {
      command.value = undefined;
      confirmation.value = "";
      resource.reload();
    }
  } finally {
    running.value = false;
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
      title="审计合规管理"
      description="Legal Hold 阻止适用证据进入常规清理；审计导出由异步任务生成，并通过受保护的一次性下载交付。"
    >
      <template #actions
        ><IrisButton variant="outline" @click="prepareAuditExport"
          >创建审计导出</IrisButton
        ></template
      >
    </PageHeader>
    <IrisAlert v-if="message" tone="success">{{ message }}</IrisAlert>
    <IrisAlert v-if="commandError && !command" tone="danger">{{
      commandError.message
    }}</IrisAlert>
    <IrisCard variant="outline">
      <template #header>设置 Legal Hold</template>
      <form class="compliance-create-grid" @submit.prevent="prepareHold">
        <IrisFormField label="Scope 类型"
          ><IrisSelect v-model="scopeType" :items="scopeItems"
        /></IrisFormField>
        <IrisFormField label="Scope ID" required
          ><IrisInput v-model="scopeID" required
        /></IrisFormField>
        <IrisFormField label="保全原因" required
          ><IrisTextarea v-model="reason" required maxlength="1000"
        /></IrisFormField>
        <IrisFormField
          label="到期时间（可选）"
          hint="RFC3339，例如 2027-01-02T03:04:05Z；留空表示长期保全"
          ><IrisInput v-model="expiresAt" placeholder="2027-01-02T03:04:05Z"
        /></IrisFormField>
        <div class="admin-filter-actions">
          <IrisButton type="submit">检查并确认</IrisButton>
        </div>
      </form>
    </IrisCard>
    <IrisCard variant="outline">
      <template #header>本次查询可见的生效 Legal Hold</template>
      <p class="muted">
        无可见记录不证明不存在适用的 Hold，也不授权解除保全或清理。
      </p>
      <RecordTable
        :records="resource.data"
        :columns="[
          { key: 'scope_type', label: 'Scope 类型' },
          { key: 'scope_id', label: 'Scope ID' },
          { key: 'reason', label: '原因' },
          { key: 'created_by', label: '创建者' },
          { key: 'created_at', label: '创建时间', kind: 'date' },
          { key: 'expires_at', label: '到期时间', kind: 'date' },
        ]"
        empty="本次查询无可见的生效 Legal Hold"
      >
        <template #actions="{ record }">
          <IrisButton
            v-if="holdRemoval(record)"
            size="sm"
            variant="danger"
            :disabled="running || resource.loading || Boolean(resource.error)"
            @click="openCommand(holdRemoval(record))"
            >解除</IrisButton
          >
        </template>
      </RecordTable>
    </IrisCard>
    <AuditAnchorsPanel :client="client" />
    <JobDetails
      v-if="jobID"
      :key="jobID"
      :client="client"
      :job-id="jobID"
      @close="jobID = undefined"
    />
    <ComplianceCommandDialog
      :command="command"
      :running="running"
      :error="commandError"
      :confirmation="confirmation"
      @update:confirmation="confirmation = $event"
      @cancel="command = undefined"
      @confirm="void runCommand()"
    />
  </section>
</template>
