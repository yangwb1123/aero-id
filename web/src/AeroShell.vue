<script setup lang="ts">
import { computed, onBeforeUnmount, provide } from "vue";
import {
  IrisAdminLayout,
  IrisAlert,
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
import { routeDetails, useHashRoute } from "./router";
import { useAsyncResource } from "./hooks/useAsyncResource";
import { accountScopeKey } from "./hooks/accountContext";
import { capabilityContextKey } from "./hooks/capabilityContext";
import { routeEligibility } from "./capabilityNavigation";
import PageLoading from "./components/PageLoading.vue";
import PageError from "./components/PageError.vue";
import ActivityPage from "./pages/ActivityPage.vue";
import AdminAccountsPage from "./pages/AdminAccountsPage.vue";
import AdminGovernancePage from "./pages/AdminGovernancePage.vue";
import AuditPage from "./pages/AuditPage.vue";
import CallbacksPage from "./pages/CallbacksPage.vue";
import CompliancePage from "./pages/CompliancePage.vue";
import ConnectionsPage from "./pages/ConnectionsPage.vue";
import DataPage from "./pages/DataPage.vue";
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

const menuRegistry: NavNode[] = [
  { key: "overview", title: "账户概览", icon: "home" },
  { key: "profile", title: "个人资料", icon: "user" },
  { key: "connections", title: "来源与成员关系", icon: "link" },
  { key: "resources", title: "资源摘要", icon: "folder" },
  { key: "data", title: "账户数据浏览", icon: "database" },
  { key: "activity", title: "近期活动", icon: "clock" },
  { key: "jobs", title: "同步与导出", icon: "download" },
  { key: "operations", title: "跨系统操作", icon: "settings" },
  { key: "admin-accounts", title: "账户管理", icon: "user" },
  { key: "admin-governance", title: "管理员治理", icon: "shield" },
  { key: "migration", title: "Legacy 迁移与切换", icon: "link" },
  { key: "audit", title: "审计台账", icon: "shield" },
  { key: "compliance", title: "审计合规管理", icon: "shield" },
  { key: "callbacks", title: "Callback 治理", icon: "link" },
  { key: "replays", title: "审计 Replay", icon: "clock" },
  { key: "services", title: "平台服务", icon: "grid" },
];
const routeKeys = new Set(menuRegistry.map((item) => item.key));
const details = computed(() => routeDetails(route.value));
const activeRoute = computed({
  get: () =>
    routeKeys.has(details.value.page) ? details.value.page : "overview",
  set: (value: string) => navigate(value),
});
const client = new AeroIdClient(
  props.config.aeroIdApiBase,
  () => auth.session?.accessToken ?? "",
);
const context = useAsyncResource((signal) => client.getMe(signal));
const capabilities = useAsyncResource(async (signal) => {
  const report = await client.getCapabilities(signal);
  if (
    report.issuer !== props.config.snaplinkIssuer ||
    report.client_id !== props.config.snaplinkClientId
  ) {
    throw new Error("能力报告与当前登录客户端不一致；不会推断权限");
  }
  return report;
});
provide(capabilityContextKey, () =>
  capabilities.error ? undefined : capabilities.data,
);
const currentEligibility = computed(() =>
  routeEligibility(
    activeRoute.value,
    capabilities.error ? undefined : capabilities.data,
  ),
);
const menus = computed<NavNode[]>(() =>
  menuRegistry.map((item) => {
    const eligibility = routeEligibility(
      item.key,
      capabilities.error ? undefined : capabilities.data,
    );
    return {
      ...item,
      disabled: !eligibility.available,
      badge: eligibility.reason || undefined,
    };
  }),
);
provide(accountScopeKey, () => {
  const account = context.data?.account;
  if (
    typeof account?.account_id !== "string" ||
    typeof account?.tenant_id !== "string"
  )
    return undefined;
  return JSON.stringify([
    props.config.aeroIdApiBase,
    props.config.snaplinkIssuer,
    props.config.snaplinkClientId,
    account.tenant_id,
    account.account_id,
  ]);
});
const accountName = computed(
  () =>
    context.data?.profile?.display_name ||
    String(context.data?.account?.canonical_uid ?? "已认证账户"),
);
const tenantLabel = computed(() =>
  String(context.data?.account?.tenant_id ?? "账户上下文加载中"),
);
onBeforeUnmount(() => client.cancelPendingReads());
const pageMap = {
  overview: OverviewPage,
  profile: ProfilePage,
  connections: ConnectionsPage,
  resources: ResourcesPage,
  data: DataPage,
  activity: ActivityPage,
  jobs: JobsPage,
  operations: OperationsPage,
  "admin-accounts": AdminAccountsPage,
  "admin-governance": AdminGovernancePage,
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
const pageProps = computed(() => {
  if (activeRoute.value === "services") return { client, config: props.config };
  if (activeRoute.value === "jobs")
    return { client, jobId: details.value.jobID };
  if (activeRoute.value === "operations")
    return { client, operationId: details.value.operationID };
  return { client };
});
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
        <IrisBadge :tone="context.error ? 'warning' : 'neutral'">{{
          context.error ? "账户上下文不可用" : tenantLabel
        }}</IrisBadge>
        <IrisAvatar :name="accountName" :size="32" />
        <IrisButton variant="ghost" size="sm" @click="void auth.logout()">
          退出
        </IrisButton>
      </div>
    </template>
    <template #default>
      <PageLoading v-if="capabilities.loading" />
      <PageError
        v-else-if="capabilities.error"
        :error="capabilities.error"
        :retry="capabilities.reload"
      />
      <IrisAlert
        v-else-if="!currentEligibility.available"
        tone="warning"
        :title="currentEligibility.reason"
      >
        当前页面不可用。能力状态是提示，不是授权凭据；后端每次命令仍重新校验。不要仅修改
        tenant_id 切换租户。
        <IrisButton variant="outline" @click="capabilities.reload"
          >重新查询能力</IrisButton
        >
      </IrisAlert>
      <template v-else>
        <IrisAlert
          v-if="capabilities.data?.partial"
          tone="warning"
          title="部分能力尚未确认"
        >
          权限或投影依赖暂不可用，未知权限不会视为允许。
          <IrisButton size="sm" variant="ghost" @click="capabilities.reload"
            >刷新能力</IrisButton
          >
        </IrisAlert>
        <component :is="pageComponent" v-bind="pageProps" />
      </template>
    </template>
    <template #footer>
      <div class="shell-footer">账户由 aero-id 聚合 · 身份由 Snaplink 验证</div>
    </template>
  </IrisAdminLayout>
</template>
