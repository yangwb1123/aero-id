<script setup lang="ts">
import { computed } from "vue";
import {
  IrisAdminLayout,
  IrisAvatar,
  IrisBadge,
  IrisButton,
  IrisIcon,
  useSkin,
  type NavNode,
} from "@iris-ui-kit/vue";
import { AeroIdClient } from "./api/aeroId";
import { useAuth } from "./auth/auth";
import type { PlatformConfig } from "./config";
import { useHashRoute } from "./router";
import ActivityPage from "./pages/ActivityPage.vue";
import AdminAccountsPage from "./pages/AdminAccountsPage.vue";
import AuditPage from "./pages/AuditPage.vue";
import CallbacksPage from "./pages/CallbacksPage.vue";
import CompliancePage from "./pages/CompliancePage.vue";
import ConnectionsPage from "./pages/ConnectionsPage.vue";
import JobsPage from "./pages/JobsPage.vue";
import MigrationPage from "./pages/MigrationPage.vue";
import OperationsPage from "./pages/OperationsPage.vue";
import OverviewPage from "./pages/OverviewPage.vue";
import ProfilePage from "./pages/ProfilePage.vue";
import ReplaysPage from "./pages/ReplaysPage.vue";
import ResourcesPage from "./pages/ResourcesPage.vue";
import ServiceLinksPage from "./pages/ServiceLinksPage.vue";

const props = defineProps<{ config: PlatformConfig }>();
const auth = useAuth();
const { skin, setMode, setSkin } = useSkin();
const { route, navigate } = useHashRoute("overview");

const menus: NavNode[] = [
  { key: "overview", title: "账户概览", icon: "home" },
  { key: "profile", title: "个人资料", icon: "user" },
  { key: "connections", title: "来源与成员关系", icon: "link" },
  { key: "resources", title: "资源摘要", icon: "folder" },
  { key: "activity", title: "近期活动", icon: "clock" },
  { key: "jobs", title: "同步与导出", icon: "download" },
  { key: "operations", title: "跨系统操作", icon: "settings" },
  { key: "admin-accounts", title: "账户管理", icon: "user" },
  { key: "migration", title: "Legacy 迁移与切换", icon: "link" },
  { key: "audit", title: "审计台账", icon: "shield" },
  { key: "compliance", title: "审计合规管理", icon: "shield" },
  { key: "callbacks", title: "Callback 治理", icon: "link" },
  { key: "replays", title: "审计 Replay", icon: "clock" },
  { key: "services", title: "平台服务", icon: "grid" },
];
const routeKeys = new Set(menus.map((item) => item.key));
const activeRoute = computed({
  get: () => (routeKeys.has(route.value) ? route.value : "overview"),
  set: (value: string) => navigate(value),
});
const client = new AeroIdClient(
  props.config.aeroIdApiBase,
  () => auth.session?.accessToken ?? "",
);
const pageMap = {
  overview: OverviewPage,
  profile: ProfilePage,
  connections: ConnectionsPage,
  resources: ResourcesPage,
  activity: ActivityPage,
  jobs: JobsPage,
  operations: OperationsPage,
  "admin-accounts": AdminAccountsPage,
  migration: MigrationPage,
  audit: AuditPage,
  compliance: CompliancePage,
  callbacks: CallbacksPage,
  replays: ReplaysPage,
  services: ServiceLinksPage,
};
const pageComponent = computed(
  () => pageMap[activeRoute.value as keyof typeof pageMap],
);
const pageProps = computed(() =>
  activeRoute.value === "services"
    ? { client, config: props.config }
    : { client },
);
const dark = computed(() => skin.value.type === "dark");

function toggleTheme(): void {
  setMode("fixed");
  setSkin(dark.value ? "light" : "dark");
}
</script>

<template>
  <IrisAdminLayout
    v-model:active-key="activeRoute"
    :menus="menus"
    :show-tabs="false"
    app-title="Aero Platform"
  >
    <template #toolbar>
      <div class="shell-toolbar">
        <IrisBadge tone="success">Snaplink 已认证</IrisBadge>
        <IrisButton
          variant="outline"
          size="sm"
          aria-label="切换主题"
          @click="toggleTheme"
        >
          <IrisIcon :name="dark ? 'sun' : 'moon'" :size="16" />
        </IrisButton>
        <IrisAvatar name="Aero User" :size="32" />
        <IrisButton variant="ghost" size="sm" @click="void auth.logout()">
          退出
        </IrisButton>
      </div>
    </template>
    <template #default>
      <component :is="pageComponent" v-bind="pageProps" />
    </template>
    <template #footer>
      <div class="shell-footer">账户由 aero-id 聚合 · 身份由 Snaplink 验证</div>
    </template>
  </IrisAdminLayout>
</template>
