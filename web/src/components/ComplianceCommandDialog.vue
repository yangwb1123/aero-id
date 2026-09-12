<script setup lang="ts">
import { computed } from "vue";
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
import type { ComplianceCommand } from "./dialogTypes";
const props = defineProps<{
  command?: ComplianceCommand;
  running: boolean;
  error?: Error;
  confirmation: string;
}>();
const emit = defineEmits<{
  "update:confirmation": [value: string];
  cancel: [];
  confirm: [];
}>();
const removing = computed(() => props.command?.kind === "remove");
const title = computed(() =>
  props.command?.kind === "put"
    ? "确认设置 Legal Hold"
    : props.command?.kind === "remove"
      ? "确认解除 Legal Hold"
      : "确认创建审计导出",
);
const description = computed(() =>
  props.command?.kind === "put"
    ? `将保全 ${props.command.input.scopeType}:${props.command.input.scopeID} 的适用审计证据，并写入不可变审计事实。`
    : props.command?.kind === "remove"
      ? `解除后 ${props.command.scopeType}:${props.command.scopeID} 将重新受正常保留与清理策略约束。该操作会写入新的审计事实。`
      : "导出制品可能包含敏感审计证据，受权限和 TTL 保护；任务完成后仍需确认一次性下载。",
);
const confirmed = computed(
  () =>
    !removing.value ||
    (props.command?.kind === "remove" &&
      props.confirmation === props.command.scopeID),
);
function closed(open: boolean): void {
  if (!open && !props.running) emit("cancel");
}
</script>

<template>
  <IrisDialog :open="Boolean(command)" @update:open="closed">
    <IrisDialogContent>
      <IrisDialogTitle>{{ title }}</IrisDialogTitle>
      <IrisDialogDescription>{{ description }}</IrisDialogDescription>
      <IrisFormField
        v-if="removing && command?.kind === 'remove'"
        label="输入完整 Scope ID 以确认"
        :hint="command.scopeID"
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
          :variant="removing ? 'danger' : 'solid'"
          :loading="running"
          :disabled="!confirmed"
          @click="emit('confirm')"
          >{{ removing ? "确认解除保全" : "确认执行" }}</IrisButton
        >
      </div>
    </IrisDialogContent>
  </IrisDialog>
</template>
