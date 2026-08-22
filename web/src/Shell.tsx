import * as React from "react";
import {
  IrisAdminLayout,
  IrisAvatar,
  IrisBadge,
  IrisButton,
  IrisIcon,
  useSkin,
  type NavNode,
} from "@iris-ui-kit/react";
import { AeroIdClient } from "./api/aeroId";
import { useAuth } from "./auth/AuthProvider";
import type { PlatformConfig } from "./config";
import { useHashRoute } from "./router";
import { ActivityPage } from "./pages/ActivityPage";
import { ConnectionsPage } from "./pages/ConnectionsPage";
import { JobsPage } from "./pages/JobsPage";
import { OperationsPage } from "./pages/OperationsPage";
import { OverviewPage } from "./pages/OverviewPage";
import { ProfilePage } from "./pages/ProfilePage";
import { ServiceLinksPage } from "./pages/ServiceLinksPage";

const menus: NavNode[] = [
  { key: "overview", title: "账户概览", icon: "home" },
  { key: "profile", title: "个人资料", icon: "user" },
  { key: "connections", title: "来源与成员关系", icon: "link" },
  { key: "activity", title: "近期活动", icon: "clock" },
  { key: "jobs", title: "同步与导出", icon: "download" },
  { key: "operations", title: "跨系统操作", icon: "settings" },
  { key: "services", title: "平台服务", icon: "grid" },
];

const routeKeys = new Set(menus.map((item) => item.key));

function PageHost({
  route,
  client,
  config,
}: {
  route: string;
  client: AeroIdClient;
  config: PlatformConfig;
}): React.ReactElement {
  switch (route) {
    case "profile":
      return <ProfilePage client={client} />;
    case "connections":
      return <ConnectionsPage client={client} />;
    case "activity":
      return <ActivityPage client={client} />;
    case "jobs":
      return <JobsPage client={client} />;
    case "operations":
      return <OperationsPage client={client} />;
    case "services":
      return <ServiceLinksPage config={config} />;
    default:
      return <OverviewPage client={client} />;
  }
}

export function Shell({
  config,
}: {
  config: PlatformConfig;
}): React.ReactElement {
  const auth = useAuth();
  const { skin, setMode, setSkin } = useSkin();
  const [route, navigate] = useHashRoute("overview");
  const activeRoute = routeKeys.has(route) ? route : "overview";
  const client = React.useMemo(
    () =>
      new AeroIdClient(
        config.aeroIdApiBase,
        () => auth.session?.accessToken ?? "",
      ),
    [auth.session?.accessToken, config.aeroIdApiBase],
  );
  const dark = skin.type === "dark";
  const toggleTheme = () => {
    setMode("fixed");
    setSkin(dark ? "light" : "dark");
  };

  return (
    <IrisAdminLayout
      menus={menus}
      activeKey={activeRoute}
      onActiveKeyChange={navigate}
      showTabs={false}
      appTitle="Aero Platform"
      toolbar={
        <div className="shell-toolbar">
          <IrisBadge tone="success">Snaplink 已认证</IrisBadge>
          <IrisButton
            variant="outline"
            size="sm"
            aria-label="切换主题"
            onClick={toggleTheme}
          >
            <IrisIcon name={dark ? "sun" : "moon"} size={16} />
          </IrisButton>
          <IrisAvatar name="Aero User" size={32} />
          <IrisButton
            variant="ghost"
            size="sm"
            onClick={() => void auth.logout()}
          >
            退出
          </IrisButton>
        </div>
      }
      footer={
        <div className="shell-footer">
          账户由 aero-id 聚合 · 身份由 Snaplink 验证
        </div>
      }
    >
      <PageHost route={activeRoute} client={client} config={config} />
    </IrisAdminLayout>
  );
}
