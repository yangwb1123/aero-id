<script setup lang="ts">
import { computed } from "vue";
import { IrisAlert } from "@iris-ui-kit/vue";
import type { JsonRecord } from "../types";

const props = defineProps<{
  kind: "sync" | "operation";
  task: JsonRecord;
}>();
const nonterminalStatuses = {
  sync: ["pending", "running", "unknown"],
  operation: ["requested", "running", "waiting", "compensating", "unknown"],
};
// This is historical diagnostic presentation only, never action eligibility.
// A worker may already have resumed while the previous code is still stored.
const reason = computed(() => {
  if (
    typeof props.task.status !== "string" ||
    !nonterminalStatuses[props.kind].includes(props.task.status) ||
    props.task.completed_at != null
  )
    return undefined;
  if (props.task.error_code === `${props.kind}.authorization_denied`)
    return "denied";
  if (props.task.error_code === `${props.kind}.authorization_unavailable`)
    return "unavailable";
  return undefined;
});
</script>

<template>
  <IrisAlert v-if="reason" tone="warning" title="继续执行授权阻断记录">
    <p>
      <code>{{ task.error_code }}</code> ·
      <span v-if="reason === 'denied'"
        >记录的继续执行检查中，原发起者所需的动作权限被拒绝。</span
      >
      <span v-else
        >记录的继续执行检查未能核实授权或目标，不能视为允许，也不是明确拒绝。</span
      >
    </p>
    <p>
      这是持久化诊断，不代表当前授权或全局时点。阻断本身不等于任务执行失败，也不能证明远端未执行；已有结果和
      unknown 不因此改变，不回滚已准入的远端效果。
    </p>
    <p>
      保留原状态、原回执和原键，先刷新原任务并核对审计。继续执行仍检查原发起者的当前权限及租户、账户、主区域/fence
      和活租约，不能借用当前操作者的权限或能力提示。
    </p>
    <p>
      此提示不自动运行、对账、换键或创建第二个命令。unknown
      仅核实原结果；导出仅核原制品 key/digest/size/TTL，不重写或延长有效期。
    </p>
  </IrisAlert>
  <IrisAlert
    v-else-if="task.error_code"
    tone="danger"
    :title="String(task.error_code)"
    >{{
      String(
        task.error_message ??
          (kind === "sync" ? "任务执行失败" : "操作执行失败"),
      )
    }}</IrisAlert
  >
</template>
