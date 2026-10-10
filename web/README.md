# Aero Platform Console

`aero-id/web` 下基于 Iris UI Vue 适配器的独立平台控制台。以下功能已接入代码；真实登录、源部署和浏览器 E2E 验收另行记录，不以页面存在或单元测试通过代替：

- 通过 Snaplink Authorization Code + PKCE（S256）登录；
- 在浏览器内存中持有短期 access token，不写入 localStorage/sessionStorage；
- 退出时调用 discovery 声明的 `end_session_endpoint`，销毁 Snaplink 会话后回跳；
- 调用 aero-id `/v1/me`、Profile、来源、成员关系、allow-list 数据浏览、脱敏资源摘要、活动、同步/导出任务和 Operation API；
- 支持任务详情、unknown 结果对账，以及带 Bearer 且确认后销毁制品的一次性导出下载；
- Operation 页面可创建有界 Saga 步骤、使用持久化幂等键、二次确认危险动作，并按 Operation ID 运行或对账；管理员账户支持 `/data` 数据查看；
- 展示 Operation/Saga 步骤、来源 operation ID、补偿状态与追加式脱敏审计时间线；
- 管理员可按关键词、状态和 home region 查询账户，并检查来源与投影的新鲜度；
- 普通账户擦除、管理员账户同步/导出/擦除均创建幂等任务；账户擦除要求输入确认短语，导出与管理员擦除要求二次确认；
- 账户详情展示 durable Region Directory，并通过观察到的 Fenced Epoch、Account ID 二次确认执行 Home Region Writer CAS 提升；
- Legacy 迁移控制面支持有界 shadow identity backfill、content-free digest 对比、cleanup readiness 和经 Tenant ID 二次确认的状态机 Cutover；
- 审计台账支持过滤、keyset 分页、脱敏证据详情和来源分区哈希链验证；
- 管理员治理覆盖 `/admin/audit-log`、`/admin/roles*`、`/admin/bans*`，角色授予/撤销与封禁使用幂等键并二次确认；解封 DELETE 超时后先刷新状态，不自动重试；
- 审计合规管理支持 Legal Hold 查询、设置、经 Scope ID 二次确认的解除，以及带一次性下载保护的审计导出；
- WORM Anchor 操作会使用固定幂等键、全链二次确认和 fail-closed 管理意图审计；单链 Manifest 敏感读取也会留存审计事实；
- Callback 治理支持 challenge 注册、一次性 Secret、订阅删除和经确认的失败投递重放；
- Replay 管理严格区分 forensic、dry-run、controlled，并为真实命令强制新幂等键和 Account ID 二次确认；支持按 Replay ID 恢复查看持久化详情；另提供按 Tenant/Account 鉴权、逐窗口审计且绝不自动修复的 Shadow Projection Replay；
- 明确展示 partial、stale、unavailable 和 unknown 状态；
- 使用独立链接进入 Audit Governance、Aero IM 和 Aero Vault 控制台，不跨 audience 转发 token。

## 范围、重试与任务跟踪

- 同步/导出必须显式勾选数据集；已注册不代表源已启用、获授权或新鲜。导出包含核心账户/资料和所选来源引用，不包含 Vault 文件或完整 IM 消息；任务结果仅显示元数据，内容留在加密 TTL 制品。
- 同步/导出先调用 `/command-intents` 获取五分钟服务端预览，显示目标、范围、步骤、本地版本和到期时间；输入确认后才调用原意图的 execute 创建既有任务回执，不自动运行。执行回应不确定时保留原预览/确认摘要；关闭页面不回滚。
- 预览创建结果不确定时保留原幂等键，相同范围只允许用户主动重试。待确认键按部署、issuer、client、tenant、account 隔离保存到 sessionStorage（最多 16 项；存储被禁用则仅保留内存），不会存储 token、导出内容或自动重新提交命令。
- `#/jobs/<UUID>`、`#/operations/<UUID>` 可分享/刷新恢复详情；原有 `#jobs`、`#/jobs` 等页面链接继续有效。链接不是权限凭据，后端仍检查当前授权。
- 详情自动刷新只调用 metadata GET，等待上一请求完成，逐步退避，最多 60 次/5 分钟；隐藏标签页、操作进行中或读取错误时暂停，终态/unknown 停止。unknown 不是失败，对账不会盲目重发原命令。
- 身份/租户会话变更会重新挂载页面、清除旧上下文/writer pin 并取消未完成读取；取消读取或关闭页面不是取消/回滚已接受的命令。租户不能仅靠修改请求 `tenant_id` 切换。
- Header 使用已验证 `/me` 的账户/租户，不使用占位身份。`/me/capabilities` 报告验证客户端上下文；菜单和深链接区分未配置、无权限与未知，未知不作为允许。源新鲜度面板分别统计注册、启用、本地读取资格及新鲜度；不探测源健康或推断源 ACL。
- 同步/导出按钮依据能力状态提示；未启用来源不能新建同步，但仍可显式导出已有本地投影。报告和预览不是授权凭据；执行重新检查当前权限、计划与本地前置条件。授权版本未受支持，源 ACL 未知，Writer Epoch 不是因果水位。
- 预览键使用独立作用域；检测到旧直接创建接口的未确认键时暂停新预览，不能换接口重复创建。过期不自动换键。Operation、管理员及擦除页面仍待逐动作接入预览；延迟 worker 的权限复查与真实浏览器验收也尚未完成。菜单、链接和前端状态都不是安全边界。

完整进度及未通过门禁见 [`../docs/plans/control-plane-production-readiness.md`](../docs/plans/control-plane-production-readiness.md)。

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

也可以复制 `.env.example` 用于本地构建。`redirectUri` 必须与前端同源，并与 Snaplink 注册的
回调地址完全一致；`aeroIdApiBase` 必须指向 HTTPS 的 `/v1` API（仅 localhost 回环开发地址允许
HTTP），且 URL 不得包含 query、fragment 或 userinfo。`snaplinkScopes` 必须包含 `openid`。
不要把 client secret、service credential 或 bearer token 写进任何前端配置。

## 开发与验证

```bash
pnpm install --frozen-lockfile
pnpm dev
pnpm check
```

`pnpm dev:iris` 会在当前工作站直接解析 `~/iris-ui/packages/*/src`，适合组件库与产品 UI
联调；`pnpm build` 使用 lockfile 固定的已发布 Iris 包，因此 CI 和容器不依赖个人目录。
业务源码的 import 始终保持 npm 包名（例如 `@iris-ui-kit/vue`），Vite 只在
`IRIS_UI_SOURCE=1` 时把这些包名 alias 到本地源码。`pnpm check:iris-imports` 会禁止业务源码
直接导入本地 Iris 路径以及 `workspace:`、`file:`、`link:` 依赖。

生产镜像：

```bash
docker build -t aero-id-web ./web
docker run --rm -p 8081:8080 aero-id-web
```

部署时用 ConfigMap 或只读卷覆盖 `/usr/share/nginx/html/runtime-config.js`，不要在镜像中写入
client secret、service credential 或 bearer token。
