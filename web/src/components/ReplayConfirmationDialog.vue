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
import type { ReplayCreateInput } from "../api/replay";
const props = defineProps<{
  input?: ReplayCreateInput;
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
  <IrisDialog :open="Boolean(input)" @update:open="closed">
    <IrisDialogContent>
      <IrisDialogTitle>确认 Controlled Replay</IrisDialogTitle>
      <IrisDialogDescription
        >将为 {{ input?.commands.length ?? 0 }} 个命令创建新的持久化
        Operation。服务端会重新授权并使用新的幂等键，不复用历史凭据。</IrisDialogDescription
      >
      <IrisAlert tone="danger" title="此模式会产生真实来源副作用"
        >先执行 dry-run 并核对当前来源状态；超时结果会保留为
        unknown，禁止当作失败盲目重发。</IrisAlert
      >
      <IrisAlert
        v-if="input?.operationID"
        tone="info"
        title="历史 Operation 来源"
        >{{ input.operationID }}。服务端会重新读取并校验它属于当前 Tenant
        下的目标 Account，只将 ID 作为新 Operation 的脱敏
        provenance。</IrisAlert
      >
      <IrisFormField
        label="输入完整 Account ID 以确认"
        :hint="input?.accountID"
      >
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
          :disabled="!input?.accountID || confirmation !== input.accountID"
          @click="emit('confirm')"
          >创建真实 Replay Operation</IrisButton
        >
      </div>
    </IrisDialogContent>
  </IrisDialog>
</template>
