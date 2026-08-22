import { IrisAlert, IrisButton, IrisCard } from "@iris-ui-kit/react";
import type { PlatformConfig } from "../config";
import { PageHeader } from "../components/PageHeader";

const services = [
  [
    "审计治理",
    "全平台审计查询、完整性、导出、保留与受控重放。",
    "auditConsoleUrl",
  ],
  [
    "消息与通知",
    "Aero IM 的会话、通知、工作区和资源权限。",
    "aeroImConsoleUrl",
  ],
  [
    "文件与对象",
    "Aero Vault 的 Bucket、对象、共享和对象 ACL。",
    "aeroVaultConsoleUrl",
  ],
] as const;

export function ServiceLinksPage({
  config,
}: {
  config: PlatformConfig;
}): React.ReactElement {
  return (
    <section>
      <PageHeader
        title="平台服务"
        description="同一 Snaplink 身份下的领域控制台入口。"
      />
      <IrisAlert tone="info" title="使用独立 audience">
        跳转后的服务必须为自己的 audience 获取令牌；本控制台不会把 aero-id
        access token 转发给其他服务。
      </IrisAlert>
      <div className="service-grid">
        {services.map(([title, description, key]) => {
          const url = config[key];
          return (
            <IrisCard
              key={key}
              variant="outline"
              header={title}
              footer={
                url ? (
                  <IrisButton asChild variant="outline">
                    <a href={url}>打开控制台</a>
                  </IrisButton>
                ) : (
                  <span className="muted">尚未配置入口 URL</span>
                )
              }
            >
              {description}
            </IrisCard>
          );
        })}
      </div>
    </section>
  );
}
