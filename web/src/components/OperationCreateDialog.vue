<script setup lang="ts">
import { computed, ref } from "vue";
import {
  IrisAlert,
  IrisButton,
  IrisDialog,
  IrisDialogContent,
  IrisDialogDescription,
  IrisDialogTitle,
  IrisFormField,
  IrisInput,
  IrisSelect,
  IrisTextarea,
} from "@iris-ui-kit/vue";
import type { AeroIdClient, OperationCreateInput } from "../api/aeroId";
import type { Dataset, JsonRecord } from "../types";
import { useCommandPreview } from "../hooks/useCommandPreview";
import OperationStepEditor from "./OperationStepEditor.vue";
import CommandPreviewDialog from "./CommandPreviewDialog.vue";
import CommandDispositionNotice from "./CommandDispositionNotice.vue";
import {
  operationCommandPlan,
  parseOperationSteps,
  type OperationStepDraft,
} from "./operationFormHelpers";
import { newSelectionOperationStep } from "./operationStepSelection";

const props = defineProps<{
  client: AeroIdClient;
  datasets: Dataset[];
  defaultAccountId?: string;
}>();
const emit = defineEmits<{ created: [operation: JsonRecord] }>();
const open = ref(false);
const accountID = ref(props.defaultAccountId ?? "");
const operationType = ref<OperationCreateInput["operationType"]>("sync");
const requestPayload = ref("{}");
const steps = ref<OperationStepDraft[]>([]);
const {
  preview,
  busy,
  error,
  pending,
  unresolved,
  abandoned,
  allowed,
  prepare,
  recover,
  confirmed,
} = useCommandPreview({
  client: () => props.client,
  channel: "operation",
  accountID: () => accountID.value.trim(),
  onReceipt: (intent) => {
    emit("created", { operation_id: intent.receipt_id });
    open.value = false;
  },
});
const operationTypes = [
  { value: "sync", label: "sync · 来源同步/摘要" },
  { value: "erase", label: "erase · 账户擦除" },
  { value: "source.command", label: "source.command · 已授权来源命令" },
  { value: "replay", label: "replay · 审计回放命令" },
];
const formError = ref<Error>();
const canPrepare = computed(() => {
  try {
    return allowed(plan());
  } catch {
    return false;
  }
});
function opened(value: boolean): void {
  if (busy.value) return;
  if (value && !pending.value) {
    accountID.value = props.defaultAccountId ?? "";
    operationType.value = "sync";
    requestPayload.value = "{}";
    steps.value = [newSelectionOperationStep([])];
    formError.value = undefined;
  }
  open.value = value;
}
function plan() {
  if (new TextEncoder().encode(requestPayload.value).byteLength > 64 * 1024)
    throw new Error("Operation Payload 超出预算");
  const payload: unknown = JSON.parse(requestPayload.value || "{}");
  if (!payload || typeof payload !== "object" || Array.isArray(payload))
    throw new Error("Operation Payload 必须是 JSON 对象");
  return operationCommandPlan(
    operationType.value,
    payload as JsonRecord,
    parseOperationSteps(steps.value, operationType.value, props.datasets),
  );
}
async function prepareForm(): Promise<void> {
  if (busy.value) return;
  formError.value = undefined;
  try {
    await prepare(plan());
  } catch (reason) {
    formError.value =
      reason instanceof Error ? reason : new Error("Operation 参数无效");
  }
}
function changeType(value: unknown): void {
  if (["sync", "erase", "source.command", "replay"].includes(String(value)))
    operationType.value = value as OperationCreateInput["operationType"];
}
</script>

<template>
  <IrisButton @click="opened(true)">创建 Operation / 核实原意图</IrisButton>
  <IrisDialog :open="open && !preview" @update:open="opened">
    <IrisDialogContent
      :style="{
        width: 'min(920px, calc(100vw - 48px))',
        padding: 'clamp(12px, 3vw, 20px)',
        overflowWrap: 'anywhere',
      }"
    >
      <IrisDialogTitle>选择范围，创建 Operation</IrisDialogTitle>
      <IrisDialogDescription>
        选择目标 → 逐步选择来源与数据集 → 核对服务端预览 → 确认原回执。
        默认同步来源摘要并更新本地投影，不需要编写
        JSON；不含对象或完整消息。预览不运行来源，确认也不会自动 Run。
        主区域由服务端解析，不以输入区域或 tenant_id 切换目标。
      </IrisDialogDescription>
      <CommandDispositionNotice :records="abandoned" />
      <IrisAlert v-if="unresolved.length" tone="warning">
        存在原意图。无 ID 的丢回应可用原键仅 GET 查找；NotFound 不允许换键。已知
        ID 仅查询原意图；未经服务端关闭证明不能更改范围或创建替代命令。
        <div v-for="record in unresolved" :key="record.idempotencyKey">
          {{ record.accountID }} · {{ record.intentID ?? "预览回应未确认" }}
          <IrisButton
            variant="outline"
            :disabled="busy"
            @click="void recover(record)"
            >查询原意图</IrisButton
          >
        </div>
      </IrisAlert>
      <form class="operation-create-form" @submit.prevent="void prepareForm()">
        <fieldset :disabled="busy || Boolean(pending?.intentID)">
          <div class="operation-form-grid">
            <IrisFormField
              label="Account ID"
              required
              hint="服务端仍校验当前租户内目标与来源资源权限"
            >
              <IrisInput v-model="accountID" required placeholder="UUID" />
            </IrisFormField>
            <IrisFormField
              label="原预览幂等键"
              hint="丢回应保留；不会自动生成第二个命令"
            >
              <IrisInput
                :model-value="
                  pending?.idempotencyKey ?? '首次预览前生成并持久保存'
                "
                readonly
              />
            </IrisFormField>
          </div>
          <p>
            计划类型：{{
              operationTypes.find((item) => item.value === operationType)?.label
            }}
          </p>
          <details data-operation-expert>
            <summary>高级 Operation 类型与参数（专家）</summary>
            <IrisFormField label="Operation 类型" required>
              <IrisSelect
                :model-value="operationType"
                :items="operationTypes"
                :teleport="false"
                @update:model-value="changeType"
              />
            </IrisFormField>
            <IrisFormField
              label="Operation Request Payload JSON"
              hint="不输入凭据或来源正文；服务端会脱敏规范化。切换类型不会重写原步骤或补偿。"
            >
              <IrisTextarea v-model="requestPayload" autosize :max-rows="6" />
            </IrisFormField>
          </details>
          <h3 class="detail-heading">有序执行步骤</h3>
          <p>
            按显示顺序规划；可移位或添加来源步骤，必须显式选择范围。已注册不等于启用、授权或新鲜。
          </p>
          <OperationStepEditor
            :steps="steps"
            :datasets="datasets"
            @change="steps = $event"
          />
        </fieldset>
        <IrisAlert v-if="!canPrepare" tone="warning"
          >当前动作、来源或账户上下文未确认；不发送新预览。来源擦除启用与 ACL
          由服务端核对，不以摘要启用推断。</IrisAlert
        >
        <IrisAlert v-if="formError || error" tone="danger">{{
          (formError ?? error)?.message
        }}</IrisAlert>
        <div class="dialog-actions">
          <IrisButton
            type="button"
            variant="ghost"
            :disabled="busy"
            @click="opened(false)"
            >关闭（不回滚）</IrisButton
          >
          <IrisButton
            v-if="!pending?.intentID"
            type="submit"
            :loading="busy"
            :disabled="!canPrepare"
            >获取服务端预览</IrisButton
          >
        </div>
      </form>
    </IrisDialogContent>
  </IrisDialog>
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
</template>

<style scoped>
.operation-create-form > fieldset {
  min-width: 0;
  margin: 0;
  padding: 0;
  border: 0;
}
</style>
