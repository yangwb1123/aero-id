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
import type { AdminAccountJobType, AeroIdClient } from "../api/aeroId";
import JobDetails from "./JobDetails.vue";
import {
  nextJobCreateIntent,
  syncJobID,
  type JobCreateIntent,
} from "./jobHelpers";
const props = defineProps<{ client: AeroIdClient; accountID: string }>();
const action = ref<AdminAccountJobType>();
const actionError = ref<Error>();
const confirmAction = ref<Exclude<AdminAccountJobType, "sync">>();
const eraseConfirmation = ref("");
const createdJobID = ref<string>();
const createIntents = ref<
  Partial<Record<AdminAccountJobType, JobCreateIntent>>
>({});
async function createJob(jobType: AdminAccountJobType): Promise<void> {
  if (action.value) return;
  const intent = nextJobCreateIntent(
    { jobType, accountID: props.accountID, datasets: [] },
    createIntents.value[jobType],
  );
  createIntents.value[jobType] = intent;
  action.value = jobType;
  actionError.value = undefined;
  try {
    const created = await props.client.createAdminAccountJob(
      props.accountID,
      jobType,
      intent.idempotencyKey,
    );
    const id = syncJobID(created);
    if (!id)
      throw new Error(
        "任务可能已创建，但服务端未返回任务 ID；请先核实任务状态，同一意图重试会复用当前幂等键。",
      );
    createdJobID.value = id;
    delete createIntents.value[jobType];
    confirmAction.value = undefined;
    eraseConfirmation.value = "";
  } catch (reason) {
    actionError.value =
      reason instanceof Error ? reason : new Error("账户任务创建失败");
  } finally {
    action.value = undefined;
  }
}
function openConfirmation(kind: Exclude<AdminAccountJobType, "sync">): void {
  actionError.value = undefined;
  eraseConfirmation.value = "";
  confirmAction.value = kind;
}
function closeDialog(open: boolean): void {
  if (!open && !action.value) confirmAction.value = undefined;
}
</script>

<template>
  <IrisCard variant="outline">
    <template #header>账户任务</template>
    <IrisAlert v-if="actionError && !confirmAction" tone="danger">{{
      actionError.message
    }}</IrisAlert>
    <IrisAlert v-if="createdJobID" tone="success" title="任务已创建"
      >任务 {{ createdJobID }} 已持久化；可在下方跟踪状态，unknown
      结果必须对账。</IrisAlert
    >
    <div class="detail-actions">
      <IrisButton
        variant="outline"
        :loading="action === 'sync'"
        :disabled="Boolean(action)"
        @click="void createJob('sync')"
        >同步全部投影</IrisButton
      >
      <IrisButton
        variant="outline"
        :disabled="Boolean(action)"
        @click="openConfirmation('export')"
        >创建账户导出</IrisButton
      >
      <IrisButton
        variant="danger"
        :disabled="Boolean(action)"
        @click="openConfirmation('erase')"
        >擦除账户数据</IrisButton
      >
    </div>
  </IrisCard>
  <JobDetails
    v-if="createdJobID"
    :client="client"
    :job-id="createdJobID"
    @close="createdJobID = undefined"
  />
  <IrisDialog :open="Boolean(confirmAction)" @update:open="closeDialog">
    <IrisDialogContent>
      <IrisDialogTitle>{{
        confirmAction === "erase" ? "确认擦除账户" : "确认创建账户导出"
      }}</IrisDialogTitle>
      <IrisDialogDescription>{{
        confirmAction === "erase"
          ? "该任务会命令各来源删除其权威数据，并最终擦除 aero-id 控制面数据；远端超时将保留为 unknown，不会盲目重发。"
          : "导出可能包含账户的敏感投影。制品受权限与 TTL 保护，完成后仍需一次性下载确认。"
      }}</IrisDialogDescription>
      <IrisFormField
        v-if="confirmAction === 'erase'"
        label="输入完整 Account ID 以确认"
        :hint="accountID"
      >
        <IrisInput v-model="eraseConfirmation" autocomplete="off" />
      </IrisFormField>
      <IrisAlert v-if="actionError" tone="danger">{{
        actionError.message
      }}</IrisAlert>
      <div class="dialog-actions">
        <IrisButton
          variant="ghost"
          :disabled="Boolean(action)"
          @click="confirmAction = undefined"
          >取消</IrisButton
        >
        <IrisButton
          :variant="confirmAction === 'erase' ? 'danger' : 'solid'"
          :loading="Boolean(action)"
          :disabled="
            confirmAction === 'erase' && eraseConfirmation !== accountID
          "
          @click="confirmAction && void createJob(confirmAction)"
          >{{
            confirmAction === "erase" ? "确认创建擦除任务" : "确认导出"
          }}</IrisButton
        >
      </div>
    </IrisDialogContent>
  </IrisDialog>
</template>
