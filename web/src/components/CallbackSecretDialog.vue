<script setup lang="ts">
import {
  IrisAlert,
  IrisButton,
  IrisCopyButton,
  IrisDialog,
  IrisDialogContent,
  IrisDialogDescription,
  IrisDialogTitle,
} from "@iris-ui-kit/vue";
import type { CallbackSecret } from "./dialogTypes";
defineProps<{ secret?: CallbackSecret }>();
const emit = defineEmits<{ discard: [] }>();
function closed(open: boolean): void {
  if (!open) emit("discard");
}
</script>

<template>
  <IrisDialog :open="Boolean(secret)" @update:open="closed">
    <IrisDialogContent>
      <IrisDialogTitle>保存 Callback 签名 Secret</IrisDialogTitle>
      <IrisDialogDescription
        >这是订阅
        {{ secret?.subscriptionID }}
        的一次性敏感结果。关闭后控制台不会保留或再次展示。</IrisDialogDescription
      >
      <IrisAlert tone="warning" title="仅保存到受控 Secret Provider"
        >不要写入源码、日志、工单或浏览器存储；接收端应使用它验证时间戳和请求体签名。</IrisAlert
      >
      <code class="secret-value">{{ secret?.value ?? "" }}</code>
      <div class="dialog-actions">
        <IrisCopyButton :text="secret?.value ?? ''" :disabled="!secret"
          >复制一次</IrisCopyButton
        >
        <IrisButton @click="emit('discard')">我已安全保存并关闭</IrisButton>
      </div>
    </IrisDialogContent>
  </IrisDialog>
</template>
