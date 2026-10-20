<script setup lang="ts">
import { IrisAlert } from "@iris-ui-kit/vue";
import type { StoredCommandPreview } from "./commandPreviewStorage";
defineProps<{ records: StoredCommandPreview[] }>();
</script>
<template>
  <IrisAlert v-if="records.length" tone="info">
    浏览器保留以下已报告关闭的原意图指针（本地指针不是当前服务端证明）；原键、计划和审计仍在服务端：
    <span v-for="record in records" :key="record.idempotencyKey"
      >{{ record.intentID }} ·
    </span>
    这不是任务取消、回滚、内容清理或 Legal Hold
    解除。新预览须显式选择并点击，仍重新授权； 生成新键前会仅 GET
    复核关闭证明，不自动提交命令。浏览器保留最多 16 条恢复指针，不作为服务端
    retention。
  </IrisAlert>
</template>
