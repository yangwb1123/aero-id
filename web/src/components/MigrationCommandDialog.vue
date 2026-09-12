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
import { backfillConfirmation, type MigrationCommand } from "./dialogTypes";
const props = defineProps<{
  command?: MigrationCommand;
  confirmation: string;
  running: boolean;
  error?: Error;
}>();
const emit = defineEmits<{
  "update:confirmation": [value: string];
  cancel: [];
  confirm: [];
}>();
const cutover = computed(() => props.command?.kind === "cutover");
const expected = computed(() =>
  cutover.value && props.command?.kind === "cutover"
    ? props.command.tenantID
    : backfillConfirmation,
);
function closed(open: boolean): void {
  if (!open && !props.running) emit("cancel");
}
</script>

<template>
  <IrisDialog :open="Boolean(command)" @update:open="closed">
    <IrisDialogContent>
      <IrisDialogTitle>{{
        cutover ? "确认租户 Legacy Cutover" : "确认 Legacy Identity Backfill"
      }}</IrisDialogTitle>
      <IrisDialogDescription>{{
        cutover && command?.kind === "cutover"
          ? `将租户 ${command.tenantID} 切换为 ${command.status}，本次确认期间保持同一个幂等键。`
          : `本轮最多扫描 ${command?.kind === "backfill" ? command.limit : "—"} 个 legacy UID，只创建 shadow account link，不修改 legacy 内容。`
      }}</IrisDialogDescription>
      <IrisAlert
        :tone="cutover ? 'danger' : 'warning'"
        :title="
          cutover ? '切换会改变 Legacy Facade 行为' : '批处理可能部分完成'
        "
        >{{
          cutover
            ? "cut_over 会让该租户的 legacy surface 返回 moved；超时或并发冲突后必须重新读取状态，禁止拿旧决策或新幂等键盲目重试。"
            : "每个账户创建和审计事实原子提交；单条失败会计入 skipped。请求超时后应保留本次幂等键，身份唯一约束仍是最终防线。"
        }}</IrisAlert
      >
      <IrisFormField :label="`输入 ${expected} 以确认`" :hint="expected">
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
          :variant="cutover ? 'danger' : 'solid'"
          :loading="running"
          :disabled="confirmation !== expected"
          @click="emit('confirm')"
          >{{ cutover ? "记录 Cutover 决策" : "执行有界 Backfill" }}</IrisButton
        >
      </div>
    </IrisDialogContent>
  </IrisDialog>
</template>
