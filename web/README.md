# Aero Platform Console

`aero-id/web` 下基于 Iris UI React 适配器的独立平台控制台。当前端到端切片负责：

- 通过 Snaplink Authorization Code + PKCE（S256）登录；
- 在浏览器内存中持有短期 access token，不写入 localStorage/sessionStorage；
- 退出时调用 discovery 声明的 `end_session_endpoint`，销毁 Snaplink 会话后回跳；
- 调用 aero-id `/v1/me`、Profile、来源、成员关系、脱敏资源摘要、活动、同步/导出任务和 Operation API；
- 支持任务详情、unknown 结果对账，以及带 Bearer 且确认后销毁制品的一次性导出下载；
- 展示 Operation/Saga 步骤、来源 operation ID、补偿状态与追加式脱敏审计时间线；
- 管理员可按关键词、状态和 home region 查询账户，并检查来源与投影的新鲜度；
- 管理员账户同步、导出和擦除均创建幂等任务；导出与擦除要求二次确认；
- 审计台账支持过滤、keyset 分页、脱敏证据详情和来源分区哈希链验证；
- 明确展示 partial、stale、unavailable 和 unknown 状态；
- 使用独立链接进入 Audit Governance、Aero IM 和 Aero Vault 控制台，不跨 audience 转发 token。

## Snaplink client

注册独立 public client，不能复用 `snaplink-console`：

```text
client_id: aero-account-console
token_endpoint_auth_method: none
grant_types: authorization_code
response_types: code
require_pkce: true
redirect_uri: https://accounts.example.com/
tenant_id: <Snaplink tenant ID>
allowed_resources: aero-id
scopes: openid profile account:read account:write operation:read source:sync
```

Snaplink 当前把 `tenant_id` 从 client 绑定写入 access token，而 aero-id 的生产校验要求非空
tenant claim。因此每个租户应注册独立 client（或使用独立域名/运行配置选择对应 client_id），不能
用一个无租户 client 给所有租户签发 token。

Snaplink discovery 的 `authorization_endpoint` 是 `/auth/login` JSON API，不是可导航的
登录页面。`snaplinkHostedLoginUrl` 必须指向单独部署的 Hosted Login UI（例如 Snaplink
Console 的 `/login/`）；Token endpoint 始终来自 OIDC discovery。

## 运行配置

生产部署应在加载应用前提供 `/runtime-config.js`：

```js
window.__AERO_PLATFORM_CONFIG__ = {
  snaplinkIssuer: "https://identity.example.com",
  snaplinkHostedLoginUrl: "https://identity.example.com/login/",
  snaplinkClientId: "aero-account-console",
  snaplinkResource: "aero-id",
  snaplinkScopes: [
    "openid",
    "profile",
    "account:read",
    "account:write",
    "operation:read",
    "source:sync",
  ],
  redirectUri: "https://accounts.example.com/",
  aeroIdApiBase: "https://accounts-api.example.com/v1",
  auditConsoleUrl: "https://audit.example.com",
  aeroImConsoleUrl: "https://messages.example.com",
  aeroVaultConsoleUrl: "https://files.example.com",
};
```

也可以复制 `.env.example` 用于本地构建。不要把 client secret、service credential 或
bearer token 写进任何前端配置。

## 开发与验证

```bash
pnpm install --frozen-lockfile
pnpm dev
pnpm check
```

`pnpm dev:iris` 会在当前工作站直接解析 `~/iris-ui/packages/*/src`，适合组件库与产品 UI
联调；`pnpm build` 使用 lockfile 固定的已发布 Iris 包，因此 CI 和容器不依赖个人目录。
业务源码的 import 始终保持 npm 包名（例如 `@iris-ui-kit/react`），Vite 只在
`IRIS_UI_SOURCE=1` 时把这些包名 alias 到本地源码。`pnpm check:iris-imports` 会禁止业务源码
直接导入本地 Iris 路径以及 `workspace:`、`file:`、`link:` 依赖。

生产镜像：

```bash
docker build -t aero-id-web ./web
docker run --rm -p 8081:8080 aero-id-web
```

部署时用 ConfigMap 或只读卷覆盖 `/usr/share/nginx/html/runtime-config.js`，不要在镜像中写入
client secret、service credential 或 bearer token。
