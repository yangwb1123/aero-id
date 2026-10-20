<script setup lang="ts">
import type { JsonRecord } from "../types";
import { summaryRecord, summaryText } from "./resourceSummary";
const props = defineProps<{ account?: JsonRecord }>();
function value(field: string): string {
  return summaryRecord(props.account)
    ? (summaryText(props.account[field]) ?? "来源未提供")
    : "来源未提供";
}
</script>

<template>
  <dl class="projection-account-context" aria-label="本次投影账户范围">
    <div>
      <dt>账户 ID</dt>
      <dd>{{ value("account_id") }}</dd>
    </div>
    <div>
      <dt>租户</dt>
      <dd>{{ value("tenant_id") }}</dd>
    </div>
    <div>
      <dt>账户主区域</dt>
      <dd>{{ value("home_region") }}</dd>
    </div>
  </dl>
</template>

<style scoped>
.projection-account-context {
  display: flex;
  flex-wrap: wrap;
  gap: 12px 32px;
  margin-block: 16px;
}
.projection-account-context > div {
  min-width: 0;
}
dt {
  font-size: 13px;
  color: var(--iris-muted);
}
dd {
  margin: 4px 0 0;
  overflow-wrap: anywhere;
}
</style>
