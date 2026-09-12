<script setup lang="ts">
import { IrisAlert, IrisButton } from "@iris-ui-kit/vue";

defineProps<{
  error: Error;
  retry: () => void;
}>();
</script>

<template>
  <IrisAlert tone="danger" title="读取失败">
    <div class="alert-actions">
      <span>{{ error.message }}</span>
      <IrisButton size="sm" variant="outline" @click="retry">重试</IrisButton>
    </div>
  </IrisAlert>
</template>
