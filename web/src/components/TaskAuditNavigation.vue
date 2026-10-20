<script setup lang="ts">
import { computed } from "vue";
import { taskAuditHref } from "../auditNavigation";
const props = defineProps<{
  page: "jobs" | "operations";
  taskId: unknown;
  requestId: unknown;
}>();
const operationLink = computed(() =>
  props.page === "operations"
    ? taskAuditHref(props.page, props.taskId, "operation", props.taskId)
    : undefined,
);
const requestLink = computed(() =>
  taskAuditHref(props.page, props.taskId, "request", props.requestId),
);
</script>
<template>
  <div class="task-audit-navigation">
    <nav class="detail-actions" aria-label="按原标识核对审计">
      <a v-if="operationLink" data-audit-link="operation" :href="operationLink"
        >按原 Operation ID 核对审计</a
      >
      <a v-if="requestLink" data-audit-link="request" :href="requestLink"
        >按原 Request ID 核对审计</a
      >
    </nav>
    <p>
      链接只预填筛选，进入台账后仍需点击「查询」，可返回原回执；不自动回放或运行。
    </p>
    <p v-if="!requestLink">
      原 Request ID 未提供或无法安全预填；不以任务 ID 或来源操作 ID 替代。
    </p>
  </div>
</template>
<style scoped>
.task-audit-navigation {
  display: grid;
  gap: 10px;
  overflow-wrap: anywhere;
}
p {
  margin: 0;
}
</style>
