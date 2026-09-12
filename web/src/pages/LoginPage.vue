<script setup lang="ts">
import { IrisAlert, IrisButton, IrisCard, IrisSpinner } from "@iris-ui-kit/vue";
import { useAuth } from "../auth/auth";

const auth = useAuth();
</script>

<template>
  <main class="login-shell">
    <IrisCard class="login-card" variant="elevated" :header="'Aero Platform'">
      <h1>统一账户控制台</h1>
      <p>
        使用 Snaplink 完成登录。控制台只在内存中持有短期访问令牌，账户数据由
        aero-id 聚合，文件、消息与审计仍由各自权威服务管理。
      </p>
      <IrisAlert v-if="auth.error" tone="danger" title="无法登录">
        {{ auth.error }}
      </IrisAlert>
      <div v-if="auth.status === 'loading'" class="login-progress">
        <IrisSpinner /> 正在完成授权码交换…
      </div>
      <IrisButton v-else size="lg" @click="void auth.login()">
        使用 Snaplink 登录
      </IrisButton>
    </IrisCard>
  </main>
</template>
