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
import { anchorRunConfirmation } from "./dialogTypes";
const props = defineProps<{
  idempotencyKey?: string;
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
  <IrisDialog :open="Boolean(idempotencyKey)" @update:open="closed">
    <IrisDialogContent>
      <IrisDialogTitle>确认执行 WORM Anchor</IrisDialogTitle>
      <IrisDialogDescription
        >本次操作会扫描并验证当前全部审计链，再为每条链写入新的不可变归档对象。固定幂等键会保留到本次确认关闭。</IrisDialogDescription
      >
      <IrisAlert tone="warning" title="全链执行可能留下部分完成结果"
        >服务端会在任何归档副作用前先持久化管理意图；审计失败时不会写归档。若后续链校验失败或请求超时，已经写入的不可变锚点不会回滚，应先查询
        Manifest 再决定是否重试。</IrisAlert
      >
      <IrisFormField
        :label="`输入 ${anchorRunConfirmation} 以确认`"
        hint="该操作覆盖所有来源与分区，不是单链操作"
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
          :disabled="confirmation !== anchorRunConfirmation"
          @click="emit('confirm')"
          >验证全部链并写入锚点</IrisButton
        >
      </div>
    </IrisDialogContent>
  </IrisDialog>
</template>
