<script setup lang="ts">
import { provide } from "vue";
import {
  IrisAlert,
  IrisCard,
  IrisProvider,
  SkinProvider,
} from "@iris-ui-kit/vue";
import { localeZhPlugin } from "@iris-ui-kit/plugin-locale-zh/core";
import { loadPlatformConfig, type PlatformConfig } from "./config";
import { createAuthState, authKey } from "./auth/auth";
import LoginPage from "./pages/LoginPage.vue";
import AeroShell from "./AeroShell.vue";
import { platformSkin } from "./skin";

let config: PlatformConfig | undefined;
let configError: string | undefined;
try {
  config = loadPlatformConfig();
} catch (reason) {
  configError = reason instanceof Error ? reason.message : "无法读取运行配置";
}

const auth = config ? createAuthState(config) : undefined;
if (auth) provide(authKey, auth);
</script>

<template>
  <IrisProvider :plugins="[localeZhPlugin]" locale="zh-CN">
    <SkinProvider :engine="platformSkin">
      <main v-if="!config" class="login-shell">
        <IrisCard variant="outline" class="login-card" header="Aero Platform">
          <IrisAlert tone="danger" title="运行配置无效">
            {{ configError ?? "运行配置无效" }}
          </IrisAlert>
          <p>请部署 runtime-config.js，或根据 .env.example 设置构建环境变量。</p>
        </IrisCard>
      </main>
      <AeroShell v-else-if="auth?.status === 'authenticated'" :config="config" />
      <LoginPage v-else />
    </SkinProvider>
  </IrisProvider>
</template>
