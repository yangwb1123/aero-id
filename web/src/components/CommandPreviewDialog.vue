<script setup lang="ts">
import { computed, ref, onMounted, onBeforeUnmount } from "vue";
import {
  IrisAlert,
  IrisButton,
  IrisDialog,
  IrisDialogContent,
  IrisDialogDescription,
  IrisDialogTitle,
  IrisFormField,
  IrisInput,
} from "@iris-ui-kit/vue";
import type { AeroIdClient } from "../api/aeroId";
import {
  sameOriginalCommandIntent,
  type CommandIntent,
} from "../api/commandIntent";
import {
  useRefreshCapabilities,
  refreshAfterAuthorizationError,
} from "../hooks/capabilityContext";
import { safeJsonPreview } from "./safeJsonPreview";
import OperationPlanSummary from "./OperationPlanSummary.vue";
const props = defineProps<{
  client: AeroIdClient;
  intent: CommandIntent;
  eligible: boolean;
}>();
const emit = defineEmits<{
  close: [];
  executed: [intent: CommandIntent];
  abandoned: [intent: CommandIntent];
}>();
const refreshCapabilities = useRefreshCapabilities();
const busy = ref(false);
const error = ref<Error>();
const blocked = ref(false);
const confirmation = ref("");
const callbackEventConfirmation = ref("");
const callbackTarget = computed(() =>
  props.intent.plan.kind === "callback_replay"
    ? props.intent.plan.callback
    : undefined,
);
const confirmationValue = computed(
  () =>
    callbackTarget.value?.delivery_id ??
    (props.intent.destructive ? props.intent.account_id : "确认"),
);
const dispositionConfirmation = ref("");
const expired = ref(Date.now() >= Date.parse(props.intent.expires_at));
let active = true;
let readController: AbortController | undefined;
let expiryTimer: ReturnType<typeof setTimeout> | undefined;
onMounted(() => {
  expiryTimer = setTimeout(
    () => {
      expired.value = true;
    },
    Math.max(0, Date.parse(props.intent.expires_at) - Date.now()),
  );
});
onBeforeUnmount(() => {
  active = false;
  clearTimeout(expiryTimer);
  readController?.abort();
});
const canConfirm = computed(
  () =>
    !busy.value &&
    props.eligible &&
    !expired.value &&
    !blocked.value &&
    props.intent.status === "prepared" &&
    confirmation.value === confirmationValue.value &&
    (!callbackTarget.value ||
      callbackEventConfirmation.value === callbackTarget.value.event_id),
);
const canAbandon = computed(
  () =>
    !busy.value &&
    props.intent.status === "prepared" &&
    dispositionConfirmation.value === props.intent.intent_id,
);
function resolved(result: CommandIntent): void {
  if (result.status === "abandoned") emit("abandoned", result);
  else if (result.status === "executed") emit("executed", result);
}
async function abandon(): Promise<void> {
  if (!canAbandon.value) return;
  busy.value = true;
  error.value = undefined;
  try {
    const result = await props.client.commands.abandon(props.intent);
    if (active) resolved(result);
  } catch (reason) {
    if (active)
      error.value =
        reason instanceof Error
          ? reason
          : new Error("处置回应未确认；保留原键，仅查询原意图，不准备新命令。");
  } finally {
    if (active) busy.value = false;
  }
}
async function execute(): Promise<void> {
  if (Date.now() >= Date.parse(props.intent.expires_at)) expired.value = true;
  if (!canConfirm.value) return;
  busy.value = true;
  error.value = undefined;
  try {
    const result = await props.client.commands.execute(props.intent);
    if (active) emit("executed", result);
  } catch (reason) {
    if (!active) return;
    error.value =
      reason instanceof Error
        ? reason
        : new Error("执行回应未确认；请查询原意图和手动重试");
    if (reason && typeof reason === "object" && "code" in reason)
      blocked.value = [
        "command.expired",
        "command.stale",
        "command.confirmation_conflict",
        "command.plan_changed",
        "command.abandoned",
      ].includes(String(reason.code));
    refreshAfterAuthorizationError(reason, refreshCapabilities);
  } finally {
    if (active) busy.value = false;
  }
}
async function queryOriginal(): Promise<void> {
  if (busy.value) return;
  busy.value = true;
  error.value = undefined;
  const controller = new AbortController();
  readController = controller;
  try {
    const result = await props.client.commands.get(
      props.intent.intent_id,
      controller.signal,
    );
    if (!active) return;
    if (!sameOriginalCommandIntent(props.intent, result))
      throw new Error("原意图计划/身份/有效期不一致；不会执行或换键。");
    if (result.status !== "prepared") resolved(result);
    else
      error.value = new Error(
        "原意图仍为 prepared；未返回执行回执。过期/冲突不授权换键，未确认结果须继续核实。",
      );
  } catch (reason) {
    if (active)
      error.value =
        reason instanceof Error
          ? reason
          : new Error("原意图查询未确认；保留原确认和键。");
  } finally {
    if (active) busy.value = false;
  }
}
</script>
<template>
  <IrisDialog
    :open="true"
    @update:open="
      (open) => {
        if (!open && !busy) emit('close');
      }
    "
  >
    <IrisDialogContent
      :style="
        intent.plan.kind === 'operation'
          ? {
              width: 'min(920px, calc(100vw - 48px))',
              padding: 'clamp(12px, 3vw, 20px)',
              overflowWrap: 'anywhere',
            }
          : undefined
      "
    >
      <IrisDialogTitle>服务端命令预览</IrisDialogTitle>
      <IrisDialogDescription v-if="callbackTarget"
        >确认只对原投递进行本地版本/状态/lease CAS 排队，回执 ID 为原意图
        ID；不代表已投递或接收端业务完成。关闭页面不取消发送或撤回字节。</IrisDialogDescription
      >
      <IrisDialogDescription v-else
        >确认只创建既有任务/Operation
        原回执，不代表来源完成；关闭页面不会回滚已提交命令。</IrisDialogDescription
      >
      <IrisAlert v-if="error" tone="danger"
        >{{ error.message }} 不会自动重试或创建第二个命令。</IrisAlert
      >
      <IrisAlert v-if="!eligible" tone="warning"
        >当前能力未确认/已撤销；暂停确认，保留原意图与输入，可仅查询原回执。菜单和预览不是权限凭据。</IrisAlert
      >
      <IrisAlert v-if="expired || blocked" tone="warning"
        >预览已过期或本地前置条件冲突；请先查询原意图/回执。查询不续期、不处置或删除持久意图，不会自动换键。</IrisAlert
      >
      <IrisAlert v-if="callbackTarget" tone="danger"
        >先核实接收端按原 Callback Event ID 去重。dead-letter
        不证明远端无效果；原 revision/claim generation
        必须仍匹配且没有任何本地租约。已准入的旧发送仍可能有远端效果；排队不改变原事件
        ID、payload、尝试次数或 signing secret。</IrisAlert
      >
      <IrisAlert v-else-if="intent.destructive" tone="danger"
        >此计划包含破坏性正向或补偿动作，不能作为 Legal Hold
        解除或跨区域删除证明。</IrisAlert
      >
      <dl>
        <dt>原意图 ID</dt>
        <dd>{{ intent.intent_id }}</dd>
        <dt>目标账户 / 租户</dt>
        <dd>{{ intent.account_id }} / {{ intent.tenant_id }}</dd>
        <dt>类型 / 版本</dt>
        <dd>
          {{ intent.plan.operation_type ?? intent.plan.kind }} /
          {{ intent.version }}
        </dd>
        <template v-if="callbackTarget">
          <dt>原订阅 / Delivery ID</dt>
          <dd>
            {{ callbackTarget.subscription_id }} /
            {{ callbackTarget.delivery_id }}
          </dd>
          <dt>原 Callback Event ID</dt>
          <dd>{{ callbackTarget.event_id }}</dd>
          <dt>本地 CAS 版本 / Claim 代数</dt>
          <dd>
            {{ callbackTarget.revision }} /
            {{ callbackTarget.lease_generation }}（非策略版本或因果水位）
          </dd>
        </template>
        <template v-else>
          <dt>数据集</dt>
          <dd>{{ intent.plan.datasets.join("、") }}</dd>
        </template>
        <dt>主区域 / Writer Epoch</dt>
        <dd>
          {{ intent.preconditions.home_region }} /
          {{ intent.preconditions.writer_epoch }}（非因果水位）
        </dd>
        <dt>有效期</dt>
        <dd>{{ intent.expires_at }}</dd>
      </dl>
      <template v-if="intent.plan.kind === 'operation'">
        <OperationPlanSummary :plan="intent.plan" />
        <details data-operation-plan-diagnostics>
          <summary>规范化步骤与高级参数（诊断）</summary>
          <pre class="json-preview">{{ safeJsonPreview(intent.plan) }}</pre>
        </details>
      </template>
      <template v-else>
        <ol v-if="intent.plan.steps?.length">
          <li v-for="step in intent.plan.steps" :key="step.step_key">
            {{ step.step_key }} · {{ step.source }} · {{ step.action }}
            <span v-if="step.compensation_action">
              → 补偿 {{ step.compensation_action }}</span
            >
            <pre class="json-preview">{{
              safeJsonPreview(step.request_payload)
            }}</pre>
          </li>
        </ol>
        <pre v-if="intent.plan.request_payload" class="json-preview">{{
          safeJsonPreview(intent.plan.request_payload)
        }}</pre>
      </template>
      <p>
        投影版本仅表示本地元数据；授权版本未受支持，源资源 ACL 未知。执行和延迟
        worker 重新检查实际动作权限/租约/fence，不能撤回已准入调用或字节。
      </p>
      <ul>
        <li
          v-for="snapshot in intent.preconditions.snapshots"
          :key="snapshot.id"
        >
          {{ snapshot.dataset }} · {{ snapshot.status }} · 本地源版本
          {{ snapshot.version }}
        </li>
      </ul>
      <IrisFormField
        :label="
          callbackTarget
            ? '输入原 Delivery ID 确认'
            : intent.destructive
              ? '输入目标账户 ID 确认'
              : '输入“确认”继续'
        "
      >
        <IrisInput v-model="confirmation" :disabled="busy" autocomplete="off" />
      </IrisFormField>
      <IrisFormField
        v-if="callbackTarget"
        label="输入原 Callback Event ID，确认已核实接收端去重"
      >
        <IrisInput
          v-model="callbackEventConfirmation"
          :disabled="busy"
          autocomplete="off"
        />
      </IrisFormField>
      <IrisButton
        :loading="busy"
        :disabled="!canConfirm"
        @click="void execute()"
        >{{
          callbackTarget
            ? "确认原投递 CAS 排队"
            : "确认创建任务 / Operation 回执"
        }}</IrisButton
      >
      <IrisButton
        variant="outline"
        :disabled="busy"
        @click="void queryOriginal()"
        >仅查询原意图 / 回执</IrisButton
      >
      <IrisFormField label="输入原意图 ID，确认放弃未执行预览">
        <IrisInput
          v-model="dispositionConfirmation"
          :disabled="busy"
          autocomplete="off"
        />
      </IrisFormField>
      <p>
        处置与执行原子竞争。已创建回执则返回原任务，不取消或回滚；只有服务端确认
        abandoned 才能显式准备新预览。过期、prepared GET 或 NotFound
        本身都不是处置证明。不清理计划、审计或 Legal Hold。
      </p>
      <IrisButton
        variant="outline"
        :disabled="!canAbandon"
        :loading="busy"
        @click="void abandon()"
        >放弃未执行预览（不回滚）</IrisButton
      >
      <IrisButton variant="outline" :disabled="busy" @click="emit('close')"
        >关闭（不回滚）</IrisButton
      >
    </IrisDialogContent>
  </IrisDialog>
</template>
