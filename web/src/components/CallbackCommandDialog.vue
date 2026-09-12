<script setup lang="ts">
import { computed } from "vue";
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
  error?: Error;
}>();
const emit = defineEmits<{ cancel: []; confirm: [] }>();
const deleting = computed(() => props.command?.kind === "delete");
function closed(open: boolean): void {
  if (!open && !props.running) emit("cancel");
}
</script>

<template>
  <IrisDialog :open="Boolean(command)" @update:open="closed">
    <IrisDialogContent>
      <IrisDialogTitle>{{
        deleting ? "确认删除 Callback 订阅" : "确认重放 Callback 投递"
      }}</IrisDialogTitle>
      <IrisDialogDescription>{{
        deleting
          ? `删除后将停止向 ${command?.kind === "delete" ? command.endpoint : "该端点"} 创建新投递。`
          : `Delivery ${command?.id ?? ""} 将作为新的人工意图进入重试队列，并写入新的审计事实。`
      }}</IrisDialogDescription>
      <IrisAlert v-if="error" tone="danger">{{ error.message }}</IrisAlert>
      <div class="dialog-actions">
        <IrisButton variant="ghost" :disabled="running" @click="emit('cancel')"
          >取消</IrisButton
        >
        <IrisButton
          :variant="deleting ? 'danger' : 'solid'"
          :loading="running"
          @click="emit('confirm')"
          >{{ deleting ? "确认删除" : "确认进入重试队列" }}</IrisButton
        >
      </div>
    </IrisDialogContent>
  </IrisDialog>
</template>
