<script setup lang="ts">
import { ref } from "vue";
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

const confirmationText = "ERASE MY ACCOUNT";
const { client } = defineProps<{ client: AeroIdClient }>();
const open = ref(false);
const confirmation = ref("");
const running = ref(false);
const error = ref<Error>();
const jobID = ref<string>();

function close(openValue: boolean): void {
  if (!openValue && !running.value) {
    open.value = false;
    confirmation.value = "";
    error.value = undefined;
  }
}

async function erase(): Promise<void> {
  if (confirmation.value !== confirmationText) return;
  running.value = true;
  error.value = undefined;
  try {
    const result = await client.eraseMyAccount();
    const id = syncJobID(result);
    if (!id) throw new Error("服务端未返回擦除任务 ID");
    jobID.value = id;
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
      variant="danger"
      @click="
        open = true;
        error = undefined;
        confirmation = '';
      "
      >擦除我的账户</IrisButton
    >
  </IrisCard>
  <JobDetails
    v-if="jobID"
    :client="client"
    :job-id="jobID"
    @close="jobID = undefined"
  />
  <IrisDialog :open="open" @update:open="close">
    <IrisDialogContent>
      <IrisDialogTitle>确认擦除我的账户</IrisDialogTitle>
      <IrisDialogDescription>
        这不是退出登录。服务端会创建持久化擦除任务，删除 aero-id
        控制面和各来源授权范围内的数据；完成前不要重复发起新的擦除意图。
      </IrisDialogDescription>
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
