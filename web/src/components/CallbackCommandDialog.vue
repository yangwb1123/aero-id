<script setup lang="ts">
import {
  IrisAlert,
  IrisButton,
  IrisDialog,
  IrisDialogContent,
  IrisDialogDescription,
  IrisDialogTitle,
} from "@iris-ui-kit/vue";
import type { CallbackCommand } from "./dialogTypes";
const props = defineProps<{
  command?: CallbackCommand;
  running: boolean;
  eligible: boolean;
  error?: Error;
}>();
const emit = defineEmits<{ cancel: []; confirm: [] }>();
function closed(open: boolean): void {
  if (!open && !props.running) emit("cancel");
}
</script>
<template>
  <IrisDialog :open="Boolean(command)" @update:open="closed">
    <IrisDialogContent>
      <IrisDialogTitle>确认删除 Callback 订阅</IrisDialogTitle>
      <IrisDialogDescription
        >删除后将停止向
        {{ command?.endpoint ?? "该端点" }}
        创建新投递，但不能撤回已发送字节。</IrisDialogDescription
      >
      <IrisAlert v-if="error" tone="danger">{{ error.message }}</IrisAlert>
      <IrisAlert v-if="!eligible" tone="warning"
        >当前动作能力未确认；保留原意图，不发送确认。</IrisAlert
      >
      <div class="dialog-actions">
        <IrisButton variant="ghost" :disabled="running" @click="emit('cancel')"
          >取消</IrisButton
        >
        <IrisButton
          variant="danger"
          :loading="running"
          :disabled="running || !eligible"
          @click="emit('confirm')"
          >确认删除</IrisButton
        >
      </div>
    </IrisDialogContent>
  </IrisDialog>
</template>
