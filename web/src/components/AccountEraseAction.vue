<script setup lang="ts">
import { ref, watch } from "vue";
import {
  IrisAlert,
  IrisButton,
  IrisCard,
  IrisDialog,
  IrisDialogContent,
  IrisDialogDescription,
  IrisDialogTitle,
  IrisFormField,
  IrisInput,
} from "@iris-ui-kit/vue";
import type { AeroIdClient } from "../api/aeroId";
import JobDetails from "./JobDetails.vue";
import { syncJobID } from "./jobHelpers";
import { loadEraseIntent, saveEraseIntent } from "./eraseIntentStorage";

const confirmationText = "ERASE MY ACCOUNT";
const props = defineProps<{ client: AeroIdClient; accountID?: string }>();
const open = ref(false);
const confirmation = ref("");
const running = ref(false);
const error = ref<Error>();
const jobID = ref<string>();
const showJobDetails = ref(false);
const idempotencyKey = ref<string>();
const attempted = ref(false);
const recoveredIntent = ref(false);

function readIntent(): { idempotencyKey?: string; jobID?: string } {
  try {
    return loadEraseIntent(window.sessionStorage, props.accountID);
  } catch {
    return {};
  }
}
function persistIntent(intent: {
  idempotencyKey?: string;
  jobID?: string;
}): void {
  try {
    saveEraseIntent(window.sessionStorage, props.accountID, intent);
  } catch {
    // Keep the command key in memory if session storage is unavailable.
  }
}
function restoreAccountIntent(): void {
  const saved = readIntent();
  idempotencyKey.value = saved.idempotencyKey;
  attempted.value = Boolean(saved.idempotencyKey);
  recoveredIntent.value = Boolean(saved.idempotencyKey);
  jobID.value = saved.jobID;
  showJobDetails.value = Boolean(saved.jobID);
  open.value = false;
  confirmation.value = "";
  error.value = undefined;
}
watch(() => props.accountID, restoreAccountIntent, { immediate: true });

function openErase(): void {
  if (jobID.value || running.value) return;
  error.value = undefined;
  confirmation.value = "";
  if (!idempotencyKey.value) {
    idempotencyKey.value = crypto.randomUUID();
    attempted.value = false;
    recoveredIntent.value = false;
  } else {
    recoveredIntent.value = attempted.value;
  }
  open.value = true;
}
function close(openValue: boolean): void {
  if (openValue || running.value) return;
  open.value = false;
  confirmation.value = "";
  error.value = undefined;
  if (!attempted.value) {
    persistIntent({});
    idempotencyKey.value = undefined;
    recoveredIntent.value = false;
  }
}

async function erase(): Promise<void> {
  const key = idempotencyKey.value;
  if (running.value || confirmation.value !== confirmationText || !key) return;
  running.value = true;
  attempted.value = true;
  persistIntent({ idempotencyKey: key });
  error.value = undefined;
  try {
    const result = await props.client.eraseMyAccount(key);
    const id = syncJobID(result);
    if (!id)
      throw new Error(
        "任务可能已创建，但响应缺少任务 ID；原幂等键已保留，请勿换键重复创建。",
      );
    jobID.value = id;
    showJobDetails.value = true;
    persistIntent({ jobID: id });
    idempotencyKey.value = undefined;
    attempted.value = false;
    recoveredIntent.value = false;
    open.value = false;
    confirmation.value = "";
  } catch (reason) {
    error.value =
      reason instanceof Error ? reason : new Error("账户擦除任务创建失败");
  } finally {
    running.value = false;
  }
}
</script>

<template>
  <IrisCard variant="outline">
    <template #header>账户生命周期</template>
    <IrisAlert tone="warning" title="不可逆的跨来源擦除">
      这会创建异步擦除 Saga，要求各权威来源分别确认删除；远端超时会保留为
      unknown，不会被前端当作成功或自动盲目重试。请先导出需要保留的合规记录，并确认这是当前登录账户。
    </IrisAlert>
    <IrisAlert v-if="error && !open" tone="danger">{{
      error.message
    }}</IrisAlert>
    <IrisButton
      v-if="jobID && !showJobDetails"
      variant="outline"
      @click="showJobDetails = true"
      >查看已创建的擦除任务</IrisButton
    >
    <IrisButton
      v-else
      variant="danger"
      :disabled="Boolean(jobID)"
      @click="openErase"
      >擦除我的账户</IrisButton
    >
  </IrisCard>
  <JobDetails
    v-if="jobID && showJobDetails"
    :client="client"
    :job-id="jobID"
    @close="showJobDetails = false"
  />
  <IrisDialog :open="open" @update:open="close">
    <IrisDialogContent>
      <IrisDialogTitle>确认擦除我的账户</IrisDialogTitle>
      <IrisDialogDescription>
        这不是退出登录。服务端会创建持久化擦除任务，删除 aero-id
        控制面和各来源授权范围内的数据；完成前不要重复发起新的擦除意图。
      </IrisDialogDescription>
      <IrisAlert v-if="recoveredIntent" tone="warning">
        检测到尚未确认结果的擦除请求；重试会复用原幂等键。请先核实任务状态，不要换键重发。
      </IrisAlert>
      <IrisFormField
        :label="`输入 ${confirmationText} 以确认`"
        :hint="confirmationText"
      >
        <IrisInput v-model="confirmation" autocomplete="off" />
      </IrisFormField>
      <IrisAlert v-if="error" tone="danger">{{ error.message }}</IrisAlert>
      <div class="dialog-actions">
        <IrisButton variant="ghost" :disabled="running" @click="close(false)"
          >取消</IrisButton
        >
        <IrisButton
          variant="danger"
          :loading="running"
          :disabled="confirmation !== confirmationText"
          @click="void erase()"
          >创建擦除任务</IrisButton
        >
      </div>
    </IrisDialogContent>
  </IrisDialog>
</template>
