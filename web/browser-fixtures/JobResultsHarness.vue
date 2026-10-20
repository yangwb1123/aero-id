<script setup lang="ts">
// Isolated test-only entry; never a login, token or production grant substitute.
import { computed } from "vue";
import {
  IrisAlert,
  IrisProvider,
  SkinProvider,
  createSkinEngine,
} from "@iris-ui-kit/vue";
import { localeZhPlugin } from "@iris-ui-kit/plugin-locale-zh/core";
import type { AeroIdClient } from "../src/api/aeroId";
import JobsPage from "../src/pages/JobsPage.vue";
import ResourcesPage from "../src/pages/ResourcesPage.vue";
import AuditPage from "../src/pages/AuditPage.vue";
import { routeDetails, useHashRoute } from "../src/router";
defineProps<{ client: AeroIdClient }>();
const skin = createSkinEngine({ skins: [], default: "light" });
const { route } = useHashRoute("jobs");
const target = computed(() => routeDetails(route.value));
</script>
<template>
  <IrisProvider :plugins="[localeZhPlugin]" locale="zh-CN">
    <SkinProvider :engine="skin">
      <main class="job-results-fixture">
        <IrisAlert tone="info" title="受控任务结果 Fixture · 非真实登录"
          >原任务列表/详情与真实 Iris，仅使用合成 API
          与制品；不执行真实同步、来源动作或登录。</IrisAlert
        >
        <JobsPage
          v-if="target.page === 'jobs'"
          :client="client"
          :job-id="target.jobID"
        />
        <ResourcesPage
          v-else-if="target.page === 'resources'"
          :client="client"
        />
        <AuditPage
          v-else-if="target.page === 'audit'"
          :client="client"
          :navigation="target.auditNavigation"
        />
        <p v-else>该路径不在本受控任务验收范围。</p>
      </main>
    </SkinProvider>
  </IrisProvider>
</template>
<style>
.job-results-fixture {
  padding: 20px;
  max-width: 1440px;
  margin-inline: auto;
}
.job-results-fixture > [data-iris-alert] {
  margin-block-end: 20px;
}
</style>
