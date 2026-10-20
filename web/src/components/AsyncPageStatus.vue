<script setup lang="ts">
import { IrisAlert, IrisCard, IrisSpinner } from "@iris-ui-kit/vue";

// Import diagnostics may contain deployment URLs. Never render the raw error
// or forward page/client props into the placeholder DOM.
defineOptions({ inheritAttrs: false });
defineProps<{ error?: unknown }>();
</script>

<template>
  <IrisAlert v-if="error" tone="danger" title="页面代码加载失败">
    不会自动重试或执行命令。请显式刷新页面后核对原意图/回执，不要因加载失败换键重发。
  </IrisAlert>
  <IrisCard v-else variant="outline">
    <div class="page-loading">
      <IrisSpinner label="正在加载页面代码" />
      <span>正在加载页面代码…</span>
    </div>
  </IrisCard>
</template>
