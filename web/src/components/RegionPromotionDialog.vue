<script setup lang="ts">
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
import type { PendingRegionPromotion } from "./regionHelpers";
const props = defineProps<{
  accountID: string;
  pending?: PendingRegionPromotion;
  confirmation: string;
  running: boolean;
  error?: Error;
}>();
const emit = defineEmits<{
  "update:confirmation": [value: string];
  cancel: [];
  confirm: [];
}>();
function closed(open: boolean): void {
  if (!open && !props.running) emit("cancel");
}
</script>

<template>
  <IrisDialog :open="Boolean(pending)" @update:open="closed">
    <IrisDialogContent>
      <IrisDialogTitle>确认提升 Home Region Writer</IrisDialogTitle>
      <IrisDialogDescription
        >将以已观察到的 Epoch {{ pending?.expectedEpoch ?? "—" }} 执行
        CAS，把账户 Writer 提升到 {{ pending?.targetRegion ?? "—" }}。成功后旧
        Writer 会被新栅栏代际拒绝；冲突不会自动重试。</IrisDialogDescription
      >
      <IrisAlert tone="warning" title="先完成区域故障处置检查"
        >确认目标区域数据库、来源连接和审计链可用，并确认流量入口会跟随新的 Home
        Region。不要通过本操作建立双写。</IrisAlert
      >
      <IrisFormField label="输入完整 Account ID 以确认" :hint="accountID">
        <IrisInput
          :model-value="confirmation"
          autocomplete="off"
          @update:model-value="emit('update:confirmation', $event)"
        />
      </IrisFormField>
      <IrisAlert v-if="error" tone="danger">{{ error.message }}</IrisAlert>
      <div class="dialog-actions">
        <IrisButton variant="ghost" :disabled="running" @click="emit('cancel')"
          >取消</IrisButton
        >
        <IrisButton
          variant="danger"
          :loading="running"
          :disabled="confirmation !== accountID"
          @click="emit('confirm')"
          >确认提升并栅栏旧 Writer</IrisButton
        >
      </div>
    </IrisDialogContent>
  </IrisDialog>
</template>
