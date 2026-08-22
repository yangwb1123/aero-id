import { IrisAlert, IrisCard, IrisProvider } from "@iris-ui-kit/react";
import { localeZhPlugin } from "@iris-ui-kit/plugin-locale-zh/core";
import { AuthProvider, useAuth } from "./auth/AuthProvider";
import { loadPlatformConfig, type PlatformConfig } from "./config";
import { LoginPage } from "./pages/LoginPage";
import { Shell } from "./Shell";
import { platformSkin } from "./skin";

function AuthGate({ config }: { config: PlatformConfig }): React.ReactElement {
  const auth = useAuth();
  return auth.status === "authenticated" ? (
    <Shell config={config} />
  ) : (
    <LoginPage />
  );
}

function ConfigurationError({
  message,
}: {
  message: string;
}): React.ReactElement {
  return (
    <main className="login-shell">
      <IrisCard variant="outline" className="login-card" header="Aero Platform">
        <IrisAlert tone="danger" title="运行配置无效">
          {message}
        </IrisAlert>
        <p>请部署 runtime-config.js，或根据 .env.example 设置构建环境变量。</p>
      </IrisCard>
    </main>
  );
}

export function App(): React.ReactElement {
  let config: PlatformConfig | undefined;
  let configError: string | undefined;
  try {
    config = loadPlatformConfig();
  } catch (reason) {
    configError = reason instanceof Error ? reason.message : "无法读取运行配置";
  }
  return (
    <IrisProvider skin={platformSkin} plugins={[localeZhPlugin]} locale="zh-CN">
      {config ? (
        <AuthProvider config={config}>
          <AuthGate config={config} />
        </AuthProvider>
      ) : (
        <ConfigurationError message={configError ?? "运行配置无效"} />
      )}
    </IrisProvider>
  );
}
