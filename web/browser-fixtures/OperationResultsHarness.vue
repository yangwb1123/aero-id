<script setup lang="ts">
// Test-only route using real pages and Iris; not an authentication substitute.
import { computed } from "vue";
import {
  IrisAlert,
  IrisProvider,
  SkinProvider,
  createSkinEngine,
} from "@iris-ui-kit/vue";
import { localeZhPlugin } from "@iris-ui-kit/plugin-locale-zh/core";
import type { AeroIdClient } from "../src/api/aeroId";
import OperationsPage from "../src/pages/OperationsPage.vue";
import AuditPage from "../src/pages/AuditPage.vue";
import ResourcesPage from "../src/pages/ResourcesPage.vue";
import { routeDetails, useHashRoute } from "../src/router";
defineProps<{ client: AeroIdClient }>();
const skin = createSkinEngine({ skins: [], default: "light" });
const { route } = useHashRoute("operations");
const target = computed(() => routeDetails(route.value));
</script>
<template>
  <IrisProvider :plugins="[localeZhPlugin]" locale="zh-CN">
    <SkinProvider :engine="skin">
      <main class="operation-results-fixture">
        <IrisAlert tone="info" title="受控 Operation Fixture · 非真实登录"
          >真实 Iris / 原页面 / 合成 HTTP
          形状；不执行来源动作，不测试生产授权。</IrisAlert
        >
        <OperationsPage
          v-if="target.page === 'operations'"
          :client="client"
          :operation-id="target.operationID"
        />
        <AuditPage
          v-else-if="target.page === 'audit'"
          :client="client"
          :navigation="target.auditNavigation"
        />
        <ResourcesPage
          v-else-if="target.page === 'resources'"
          :client="client"
        />
        <p v-else>当前路径不在本受控 Operation 场景范围。</p>
      </main>
    </SkinProvider>
  </IrisProvider>
</template>
<style>
.operation-results-fixture {
  padding: 20px;
  max-width: 1440px;
  margin-inline: auto;
}
.operation-results-fixture > [data-iris-alert] {
  margin-block-end: 20px;
}
</style>
