<script setup lang="ts">
// Test-only Vite entry, never imported by index.html or the production application.
import {
  IrisAlert,
  IrisProvider,
  SkinProvider,
  createSkinEngine,
} from "@iris-ui-kit/vue";
import { localeZhPlugin } from "@iris-ui-kit/plugin-locale-zh/core";
import type { AeroIdClient } from "../src/api/aeroId";
import ResourcesPage from "../src/pages/ResourcesPage.vue";
defineProps<{ client: AeroIdClient }>();
const skin = createSkinEngine({ skins: [], default: "light" });
</script>

<template>
  <IrisProvider :plugins="[localeZhPlugin]" locale="zh-CN">
    <SkinProvider :engine="skin">
      <main class="fixture-container">
        <IrisAlert tone="info" title="受控浏览器 Fixture · 非真实登录"
          >真实 Iris 组件与资源页面；API 为隔离合成响应，不验证 Snaplink
          或远端来源。</IrisAlert
        >
        <ResourcesPage :client="client" />
      </main>
    </SkinProvider>
  </IrisProvider>
</template>

<style>
.fixture-container {
  padding: 20px;
  max-width: 1440px;
  margin-inline: auto;
}
.fixture-container > [data-iris-alert] {
  margin-block-end: 20px;
}
</style>
