import {
  IrisAlert,
  IrisButton,
  IrisCard,
  IrisSpinner,
} from "@iris-ui-kit/react";
import { useAuth } from "../auth/AuthProvider";

export function LoginPage(): React.ReactElement {
  const auth = useAuth();
  const loading = auth.status === "loading";
  return (
    <main className="login-shell">
      <IrisCard
        className="login-card"
        variant="elevated"
        header={<div className="login-brand">Aero Platform</div>}
      >
        <h1>统一账户控制台</h1>
        <p>
          使用 Snaplink 完成登录。控制台只在内存中持有短期访问令牌，账户数据由
          aero-id 聚合，文件、消息与审计仍由各自权威服务管理。
        </p>
        {auth.error ? (
          <IrisAlert tone="danger" title="无法登录">
            {auth.error}
          </IrisAlert>
        ) : null}
        {loading ? (
          <div className="login-progress">
            <IrisSpinner /> 正在完成授权码交换…
          </div>
        ) : (
          <IrisButton size="lg" onClick={() => void auth.login()}>
            使用 Snaplink 登录
          </IrisButton>
        )}
      </IrisCard>
    </main>
  );
}
