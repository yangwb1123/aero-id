<script setup lang="ts">
import { IrisCard, IrisSpinner } from "@iris-ui-kit/vue";
</script>

<template>
  <IrisCard variant="outline">
    <div class="page-loading">
      <IrisSpinner label="正在加载" />
      <span>正在读取平台数据…</span>
    </div>
  </IrisCard>
</template>
