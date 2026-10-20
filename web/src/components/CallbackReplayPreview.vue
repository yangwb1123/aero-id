<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from "vue";
import {
  IrisAlert,
  IrisButton,
  IrisCard,
  IrisFormField,
  IrisInput,
} from "@iris-ui-kit/vue";
import type { AeroIdClient } from "../api/aeroId";
import {
  parseCallbackDelivery,
  type CallbackDelivery,
} from "../api/callbackDelivery";
import type { CommandIntent } from "../api/commandIntent";
import { useAccountScope } from "../hooks/accountContext";
import { useCapabilities } from "../hooks/capabilityContext";
import { useCommandPreview } from "../hooks/useCommandPreview";
import { browserCommandContext } from "./commandEligibility";
import { callbackReplayAvailable } from "./callbackEligibility";
import { callbackReplayPlan } from "./callbackReplayPlan";
import CommandPreviewDialog from "./CommandPreviewDialog.vue";
import CommandDispositionNotice from "./CommandDispositionNotice.vue";

const props = defineProps<{ client: AeroIdClient; disabled: boolean }>();
const emit = defineEmits<{
  queued: [intent: CommandIntent];
  busy: [value: boolean];
}>();
const scope = useAccountScope();
const capabilities = useCapabilities();
const target = ref<CallbackDelivery>();
const receipt = ref<CommandIntent>();
const acknowledgement = ref("");
let generation = 0;
let active = true;
onBeforeUnmount(() => {
  active = false;
  generation += 1;
});
const {
  preview,
  busy,
  error,
  pending,
  abandoned,
  acknowledged,
  acknowledgeReceipt,
  allowed,
  prepare,
  recover,
  confirmed,
} = useCommandPreview({
  client: () => props.client,
  channel: "callback-replay",
  accountID: () => browserCommandContext(scope?.())?.accountID,
  // Sync/Erase legacy keys cannot be migrated into a callback replay command.
  legacyAccountCommands: false,
  onReceipt: (intent) => {
    receipt.value = intent;
    emit("queued", intent);
  },
});
watch(
  () => busy.value || Boolean(pending.value),
  (value) => emit("busy", value),
  { immediate: true },
);
watch(
  () => scope?.(),
  () => {
    generation += 1;
    target.value = undefined;
    receipt.value = undefined;
    acknowledgement.value = "";
  },
  { flush: "sync" },
);
const canPrepare = computed(
  () =>
    !props.disabled &&
    !busy.value &&
    !pending.value?.intentID &&
    callbackReplayAvailable(target.value, capabilities?.(), scope?.()) &&
    Boolean(target.value && allowed(callbackReplayPlan(target.value))),
);
const canAcknowledge = computed(
  () =>
    !props.disabled &&
    !busy.value &&
    Boolean(pending.value?.receiptID) &&
    acknowledgement.value === pending.value?.intentID,
);
async function start(delivery: CallbackDelivery): Promise<void> {
  if (
    props.disabled ||
    busy.value ||
    pending.value ||
    !callbackReplayAvailable(delivery, capabilities?.(), scope?.())
  )
    return;
  try {
    target.value = parseCallbackDelivery(
      delivery,
      delivery.subscription_id,
      delivery.id,
    );
  } catch {
    error.value = new Error("重放目标元数据无效；不会发送预览请求。");
    return;
  }
  receipt.value = undefined;
  acknowledgement.value = "";
  await prepare(callbackReplayPlan(target.value));
}
async function retryOriginal(): Promise<void> {
  if (canPrepare.value && target.value)
    await prepare(callbackReplayPlan(target.value));
}
async function acknowledge(): Promise<void> {
  if (!canAcknowledge.value) return;
  const token = generation;
  await acknowledgeReceipt(); // Explicit GET proof only. Never POST or queue.
  if (active && token === generation && !pending.value) {
    target.value = undefined;
    receipt.value = undefined;
    acknowledgement.value = "";
  }
}
defineExpose({ start });
</script>
<template>
  <IrisCard variant="outline">
    <template #header>持久 Callback 重放意图与恢复</template>
    <IrisAlert tone="warning"
      >只对已验证的原 dead-letter 投递获取服务端预览，再显式确认原
      Delivery/Event
      ID。接收端必须按原事件去重；本地排队不是远端完成，关闭页面不取消发送。旧兼容接口的未知结果不可自动升级成此意图，必须先核实原投递/接收端。</IrisAlert
    >
    <IrisAlert v-if="error" tone="danger">{{ error.message }}</IrisAlert>
    <CommandDispositionNotice :records="abandoned" />
    <IrisAlert v-if="acknowledged.length" tone="info"
      >保留
      {{ acknowledged.length }}
      个已显式核实的本地排队回执指针；不证明接收端完成。新人工意图前会仅 GET
      再核原回执，不自动创建；恢复指针总计最多 16 条，不是
      retention。</IrisAlert
    >
    <IrisAlert v-if="pending" tone="warning">
      原意图 {{ pending.intentID ?? "预览回应未确认" }}：按原 ID/原键仅 GET
      恢复，NotFound、prepared、过期或旧 metadata 均不授权换键。
      <IrisButton variant="outline" :disabled="busy" @click="void recover()"
        >仅查询原重放意图 / 回执</IrisButton
      >
      <IrisButton
        v-if="!pending.intentID && target"
        variant="outline"
        :disabled="!canPrepare"
        @click="void retryOriginal()"
        >显式重取原参数预览（原键）</IrisButton
      >
    </IrisAlert>
    <IrisAlert v-if="receipt" tone="success"
      >Delivery {{ receipt.plan.callback?.delivery_id }} 本地 CAS 已排队；原意图
      / 排队回执
      {{ receipt.receipt_id }}。不代表已投递或接收端业务完成。</IrisAlert
    >
    <template v-if="pending?.receiptID">
      <IrisFormField label="输入原意图 ID，核实本地回执并允许另选新的人工意图">
        <IrisInput
          v-model="acknowledgement"
          :disabled="busy"
          autocomplete="off"
        />
      </IrisFormField>
      <p>
        此动作仅 GET 核原 executed
        排队回执；不取消、不重放、不证明接收端无效果。另一个人工意图仍须另选当前投递版本、核实原事件去重并重新授权。当前拒绝写入不会阻止原回执恢复，但不能借核实获得写入权限。
      </p>
      <IrisButton
        variant="outline"
        :disabled="!canAcknowledge"
        @click="void acknowledge()"
        >核实原排队回执（不发送命令）</IrisButton
      >
    </template>
    <CommandPreviewDialog
      v-if="preview"
      :key="preview.intent_id"
      :client="client"
      :intent="preview"
      :eligible="!disabled && allowed(preview.plan, preview.account_id)"
      @close="preview = undefined"
      @executed="confirmed"
      @abandoned="confirmed"
    />
  </IrisCard>
</template>
