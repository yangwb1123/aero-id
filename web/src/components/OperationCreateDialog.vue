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
import OperationStepEditor from "./OperationStepEditor.vue";
import {
  newOperationStep,
  parseOperationSteps,
  type OperationStepDraft,
} from "./operationFormHelpers";

const props = defineProps<{
  client: AeroIdClient;
  datasets: Dataset[];
  defaultAccountId?: string;
}>();
const emit = defineEmits<{ created: [operation: JsonRecord] }>();
const open = ref(false);
const running = ref(false);
const error = ref<Error>();
const accountID = ref("");
const homeRegion = ref("");
const operationType = ref<OperationCreateInput["operationType"]>("sync");
const idempotencyKey = ref("");
const requestPayload = ref("{}");
const steps = ref<OperationStepDraft[]>([]);
const confirming = ref(false);
const confirmation = ref("");
const pending = ref<OperationCreateInput>();
const operationTypes = [
  { value: "sync", label: "sync · 来源同步/摘要" },
  { value: "erase", label: "erase · 账户擦除" },
  { value: "source.command", label: "source.command · 已授权来源命令" },
  { value: "replay", label: "replay · 审计回放命令" },
];
const dangerous = computed(() => {
  if (operationType.value === "erase") return true;
  return steps.value.some((step) =>
    ["erase", "delete", "source.erase"].includes(step.action),
  );
});
const confirmationPhrase = computed(() =>
  operationType.value === "erase"
    ? "CONFIRM ERASE"
    : "CONFIRM DESTRUCTIVE OPERATION",
);
const canConfirm = computed(
  () => confirmation.value === confirmationPhrase.value,
);
function newID(): string {
  return crypto.randomUUID();
}
function reset(): void {
  accountID.value = props.defaultAccountId ?? "";
  homeRegion.value = "";
  operationType.value = "sync";
  idempotencyKey.value = newID();
  requestPayload.value = "{}";
  steps.value = [newOperationStep(props.datasets)];
  confirming.value = false;
  confirmation.value = "";
  pending.value = undefined;
  error.value = undefined;
}
function opened(value: boolean): void {
  if (value) {
    reset();
    open.value = true;
    return;
  }
  if (!running.value) {
    open.value = false;
    confirming.value = false;
  }
}
function parseObject(value: string, label: string): JsonRecord {
  let parsed: unknown;
  try {
    parsed = JSON.parse(value || "{}");
  } catch {
    throw new Error(`${label} 不是有效 JSON`);
  }
  if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) {
    throw new Error(`${label} 必须是 JSON 对象`);
  }
  return parsed as JsonRecord;
}
function prepare(): void {
  error.value = undefined;
  try {
    if (!accountID.value.trim()) throw new Error("Account ID 不能为空");
    if (!idempotencyKey.value.trim()) throw new Error("幂等键不能为空");
    const input: OperationCreateInput = {
      accountID: accountID.value.trim(),
      homeRegion: homeRegion.value.trim() || undefined,
      operationType: operationType.value,
      idempotencyKey: idempotencyKey.value.trim(),
      requestPayload: parseObject(requestPayload.value, "Operation Payload"),
      steps: parseOperationSteps(
        steps.value,
        operationType.value,
        props.datasets,
      ),
    };
    pending.value = input;
    if (dangerous.value) {
      confirming.value = true;
      confirmation.value = "";
      return;
    }
    void execute(input);
  } catch (reason) {
    error.value =
      reason instanceof Error ? reason : new Error("Operation 参数无效");
  }
}
async function execute(input: OperationCreateInput): Promise<void> {
  running.value = true;
  error.value = undefined;
  try {
    const operation = await props.client.createOperation(input);
    emit("created", operation);
    open.value = false;
    confirming.value = false;
  } catch (reason) {
    error.value =
      reason instanceof Error ? reason : new Error("Operation 创建失败");
  } finally {
    running.value = false;
  }
}
function confirm(): void {
  if (!pending.value || !canConfirm.value) return;
  void execute(pending.value);
}
function cancelConfirmation(): void {
  if (running.value) return;
  confirming.value = false;
  pending.value = undefined;
  confirmation.value = "";
}
function changeType(value: unknown): void {
  if (
    value === "sync" ||
    value === "erase" ||
    value === "source.command" ||
    value === "replay"
  ) {
    operationType.value = value;
  }
}
</script>

<template>
  <IrisButton @click="opened(true)">创建 Operation</IrisButton>
  <IrisDialog :open="open" @update:open="opened">
    <IrisDialogContent>
      <IrisDialogTitle>{{
        confirming ? "确认危险 Operation" : "创建跨系统 Operation"
      }}</IrisDialogTitle>
      <IrisDialogDescription v-if="confirming">
        该请求包含账户擦除或来源删除动作。远端超时可能产生 unknown
        结果，提交后必须通过 Operation ID 对账；不会自动扫描或自动修复。
      </IrisDialogDescription>
      <IrisDialogDescription v-else>
        每个步骤都会持久化为 Saga 状态。幂等键只可用于同一个具体意图的重试。
      </IrisDialogDescription>
      <template v-if="!confirming">
        <form class="operation-create-form" @submit.prevent="prepare">
          <div class="operation-form-grid">
            <IrisFormField
              label="Account ID"
              required
              hint="用户操作默认填入当前账户；管理员可输入已授权账户"
            >
              <IrisInput v-model="accountID" required placeholder="UUID" />
            </IrisFormField>
            <IrisFormField label="Operation 类型" required>
              <IrisSelect
                :model-value="operationType"
                :items="operationTypes"
                @update:model-value="changeType"
              />
            </IrisFormField>
            <IrisFormField
              label="Home Region"
              hint="留空由服务端按账户归属区域解析"
            >
              <IrisInput v-model="homeRegion" placeholder="例如 cn-east-1" />
            </IrisFormField>
            <IrisFormField
              label="幂等键"
              required
              hint="请求超时后保持不变；新意图请生成新键"
            >
              <IrisInput v-model="idempotencyKey" required readonly />
            </IrisFormField>
          </div>
          <div class="operation-key-row">
            <IrisButton
              type="button"
              variant="outline"
              @click="idempotencyKey = newID()"
              >生成新幂等键</IrisButton
            >
          </div>
          <IrisFormField label="Operation Request Payload JSON">
            <IrisTextarea v-model="requestPayload" autosize :max-rows="6" />
          </IrisFormField>
          <h3 class="detail-heading">执行步骤</h3>
          <OperationStepEditor
            :steps="steps"
            :datasets="datasets"
            @change="steps = $event"
          />
          <IrisAlert v-if="error" tone="danger">{{ error.message }}</IrisAlert>
          <div class="dialog-actions">
            <IrisButton type="button" variant="ghost" @click="opened(false)"
              >取消</IrisButton
            >
            <IrisButton type="submit" :loading="running">校验并继续</IrisButton>
          </div>
        </form>
      </template>
      <template v-else>
        <IrisAlert tone="danger" title="不可逆/高风险动作">
          即将提交 {{ operationType }}。请输入
          <strong>{{ confirmationPhrase }}</strong> 完成二次确认。
        </IrisAlert>
        <IrisFormField label="确认短语" :hint="confirmationPhrase">
          <IrisInput v-model="confirmation" autocomplete="off" />
        </IrisFormField>
        <IrisAlert v-if="error" tone="danger">{{ error.message }}</IrisAlert>
        <div class="dialog-actions">
          <IrisButton
            variant="ghost"
            :disabled="running"
            @click="cancelConfirmation"
            >返回修改</IrisButton
          >
          <IrisButton
            variant="danger"
            :loading="running"
            :disabled="!canConfirm"
            @click="confirm"
            >确认提交</IrisButton
          >
        </div>
      </template>
    </IrisDialogContent>
  </IrisDialog>
</template>
