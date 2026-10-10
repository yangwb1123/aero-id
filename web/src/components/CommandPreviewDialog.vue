<script setup lang="ts">
import { computed, ref, onMounted, onUnmounted } from "vue";
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
import type { CommandIntent } from "../api/commandIntent";
const props = defineProps<{ client: AeroIdClient; intent: CommandIntent }>();
const emit = defineEmits<{ close: []; executed: [intent: CommandIntent] }>();
const busy = ref(false);
const error = ref<Error>();
const confirmation = ref("");
const expired = ref(Date.now() >= Date.parse(props.intent.expires_at));
let expiryTimer: ReturnType<typeof setTimeout> | undefined;
onMounted(() => {
  expiryTimer = setTimeout(
    () => {
      expired.value = true;
    },
    Math.max(0, Date.parse(props.intent.expires_at) - Date.now()),
  );
});
onUnmounted(() => clearTimeout(expiryTimer));
const canConfirm = computed(
  () =>
    !busy.value &&
    !expired.value &&
    confirmation.value ===
      (props.intent.destructive ? props.intent.account_id : "确认"),
);
async function execute(): Promise<void> {
  if (Date.now() >= Date.parse(props.intent.expires_at)) expired.value = true;
  if (!canConfirm.value) return;
  busy.value = true;
  error.value = undefined;
  try {
    emit("executed", await props.client.commands.execute(props.intent));
  } catch (reason) {
    error.value =
      reason instanceof Error
        ? reason
        : new Error("执行回应未确认；请使用原预览查询和手动重试");
  } finally {
    busy.value = false;
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
    <IrisDialogContent>
      <IrisDialogTitle>服务端命令预览</IrisDialogTitle>
      <IrisDialogDescription
        >确认只创建既有任务回执，不代表源执行完成；关闭页面不会回滚已提交命令。</IrisDialogDescription
      >
      <IrisAlert v-if="error" tone="danger"
        >{{ error.message }} 不会自动重试或创建第二个命令。</IrisAlert
      >
      <IrisAlert v-if="expired" tone="warning"
        >预览已过期；请先核实原意图与回执，不会自动换键。</IrisAlert
      >
      <IrisAlert v-if="intent.destructive" tone="danger"
        >此计划包含破坏性正向或补偿动作。</IrisAlert
      >
      <dl>
        <dt>目标账户 / 租户</dt>
        <dd>{{ intent.account_id }} / {{ intent.tenant_id }}</dd>
        <dt>类型 / 版本</dt>
        <dd>{{ intent.plan.kind }} / {{ intent.version }}</dd>
        <dt>数据集</dt>
        <dd>{{ intent.plan.datasets.join("、") }}</dd>
        <dt>主区域 / Writer Epoch</dt>
        <dd>
          {{ intent.preconditions.home_region }} /
          {{ intent.preconditions.writer_epoch }}（非因果水位）
        </dd>
        <dt>有效期</dt>
        <dd>{{ intent.expires_at }}</dd>
      </dl>
      <ul v-if="intent.plan.steps?.length">
        <li v-for="step in intent.plan.steps" :key="step.step_key">
          {{ step.source }} · {{ step.action
          }}<span v-if="step.compensation_action">
            → 补偿 {{ step.compensation_action }}</span
          >
        </li>
      </ul>
      <p>
        投影版本仅表示本地元数据；授权版本未受支持，源资源 ACL
        未知。执行时会重新检查权限、计划和本地前置条件。
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
        :label="intent.destructive ? '输入目标账户 ID 确认' : '输入“确认”继续'"
      >
        <IrisInput v-model="confirmation" :disabled="busy" autocomplete="off" />
      </IrisFormField>
      <IrisButton
        :loading="busy"
        :disabled="!canConfirm"
        @click="void execute()"
        >确认创建任务</IrisButton
      >
      <IrisButton variant="outline" :disabled="busy" @click="emit('close')"
        >关闭（不回滚）</IrisButton
      >
    </IrisDialogContent>
  </IrisDialog>
</template>
