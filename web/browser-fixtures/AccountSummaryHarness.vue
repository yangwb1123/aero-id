<script setup lang="ts">
// Isolated test-only entry. No OAuth/login or production authorization bypass.
import {
  IrisAlert,
  IrisProvider,
  SkinProvider,
  createSkinEngine,
} from "@iris-ui-kit/vue";
import { localeZhPlugin } from "@iris-ui-kit/plugin-locale-zh/core";
import type { AeroIdClient } from "../src/api/aeroId";
import OverviewPage from "../src/pages/OverviewPage.vue";
import DataPage from "../src/pages/DataPage.vue";
import ConnectionsPage from "../src/pages/ConnectionsPage.vue";
import ResourcesPage from "../src/pages/ResourcesPage.vue";
import { useHashRoute } from "../src/router";
defineProps<{ client: AeroIdClient }>();
const skin = createSkinEngine({ skins: [], default: "light" });
const { route } = useHashRoute("overview");
</script>
<template>
  <IrisProvider :plugins="[localeZhPlugin]" locale="zh-CN">
    <SkinProvider :engine="skin">
      <main class="account-summary-fixture">
        <IrisAlert tone="info" title="受控浏览器 Fixture · 非真实登录"
          >真实 Iris 与原账户概览/数据浏览页面，只有隔离合成
          API；不验证身份、来源连接或远端业务。</IrisAlert
        >
        <OverviewPage v-if="route === 'overview'" :client="client" />
        <DataPage v-else-if="route === 'data'" :client="client" />
        <ConnectionsPage v-else-if="route === 'connections'" :client="client" />
        <ResourcesPage v-else-if="route === 'resources'" :client="client" />
        <p v-else>
          本 fixture 不运行任务、导出或审计；请使用已部署环境另行验收。
        </p>
      </main>
    </SkinProvider>
  </IrisProvider>
</template>
<style>
.account-summary-fixture {
  padding: 20px;
  max-width: 1440px;
  margin-inline: auto;
}
.account-summary-fixture > [data-iris-alert] {
  margin-block-end: 20px;
}
</style>
