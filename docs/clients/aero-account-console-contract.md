# aero-account-console 后端契约

这是独立账户客户端的最小后端契约。客户端只访问 aero-id；除登录跳转外，不直接调用 Snaplink、Aero IM 或 Aero Vault。

## OAuth client registration（P7-02）

在 Snaplink 注册**独立** OAuth/OIDC client（禁止复用 snaplink-console 的 client）：

| 项           | 值（示例）                                                                                                                                   |
| ------------ | -------------------------------------------------------------------------------------------------------------------------------------------- |
| client_id    | `aero-account-console`                                                                                                                       |
| redirect_uri | `https://console.<region>.example.com/`                                                                                                      |
| audience     | `aero-id`                                                                                                                                    |
| grant        | Authorization Code + PKCE（S256，强制）                                                                                                      |
| scopes       | `openid profile account:read` 起步；按需加 `account:write account:manage audit:read audit:replay operation:read callback:manage source:sync` |

前端使用 Iris UI 的 Vue 适配器，可运行客户端位于本仓库 `web/`。本契约与 OpenAPI
（`api/openapi/aero-id-account.yaml`，仓库有守卫测试防漂移）仍保持技术栈无关；
客户端不复用 `snaplink-console` 的 OAuth client，也不把 aero-id audience 的 token
转发给 Audit Governance、Aero IM 或 Aero Vault。

## 登录与请求上下文

- 在 Snaplink 注册独立 OAuth/OIDC client，不复用 `snaplink-console` 的 client ID。
- 浏览器使用 Authorization Code + PKCE；令牌的 issuer、audience、subject 必须由 Snaplink 校验。
- 每次请求发送 `Authorization: Bearer <access-token>`。客户端可发送 `X-Request-ID`，服务端会在响应中返回它；服务端还返回 `X-Trace-ID`。
- 需要幂等的 POST 同时发送 `Idempotency-Key`。重复请求返回同一任务/操作结果，不重复触发源系统副作用。
- 浏览器 CORS 预检允许 `Idempotency-Key`、`X-Aero-Write-Epoch` 和 W3C trace headers，并暴露
  `X-Request-ID`、`X-Trace-ID`、降级、写 epoch、陈旧读取、幂等重放与重试响应头。

## 响应和数据集

成功响应使用 `{"data": ...}`。错误使用稳定的 `code` 和 `message`；不要把 endpoint、SQL、堆栈或凭据展示给用户。

```text
GET  /v1/account-datasets
GET  /v1/me
GET  /v1/me/profile
PATCH /v1/me/profile
GET  /v1/me/sources?limit=50&cursor=<cursor>
GET  /v1/me/memberships?limit=50&cursor=<cursor>
GET  /v1/me/data?dataset=aero-im.workspaces&consistency=eventual
GET  /v1/me/overview
GET  /v1/me/activity
```

`/v1/account-datasets` 是 allow-list；客户端只能请求返回的 dataset 名称，不能拼接源系统路径或 SQL。
`aero-im.notifications` 是 Aero IM 通知收件箱的有界、无正文投影；只含通知与关联资源 ID、类型、时间、
聚合数、重要度、未读总数和截断标记。账户控制台不通过 aero-id 修改通知状态，处理动作仍进入 Aero IM，
避免在账户系统中建立第二套通知权威。尚未在 Aero IM 完成首次 Snaplink JIT 关联时，投影保持相同结构并以
`source_account_status=not_found` 明确标记；只读摘要请求不得隐式创建 Aero IM 账户。
`aero-vault.tenants`、`aero-vault.buckets` 和 `aero-vault.usage` 只提供租户状态、桶名称及
容量/对象数配额用量，不包含文件正文、对象内容或下载凭据。账户控制台只读展示这些投影；上传、下载、
删除、分享和保留策略仍由 Aero Vault 执行，aero-id audience 的 access token 不得转发给 Aero Vault。
`snaplink.identity`、`snaplink.tenants`、`snaplink.permissions` 和 `snaplink.security` 仅按 Snaplink
明确响应字段展示身份、账户状态、租户、角色与权限。客户端不得通用展开未知字段，也不得显示密码、
MFA 密钥、会话令牌或其他凭据；身份验证和授权写操作必须回到 Snaplink 管理端执行。
聚合快照包含 `source`、`source_region`、`status`、`version`、`generated_at`、`last_synced_at` 和可选
`expires_at`。`status` 可能为 `fresh`、`stale`、`unavailable` 或 `unknown`。聚合响应中的 `partial`、
`source_errors` 和 `stale_datasets` 必须在界面上明确展示。

`consistency=eventual` 只读本地投影；`bounded` 和 `strong` 会在有界 deadline 内刷新指定源，不能保证跨源
事务一致性。源服务不可用时，其他源仍可返回。

## 账户管理和异步任务

客户端可以修改的 Profile 仅限 aero-id 拥有的展示/偏好字段；邮箱、电话、验证、MFA 和生命周期字段不在
Profile PATCH 中。

```text
POST /v1/me/sync
POST /v1/me/export
POST /v1/me/erase
GET  /v1/sync/jobs?limit=&cursor=
GET  /v1/sync/jobs/:id
POST /v1/sync/jobs/:id/reconcile
GET  /v1/sync/jobs/:id/export/download
POST /v1/operations
GET  /v1/operations/:id
POST /v1/operations/:id/run
POST /v1/operations/:id/reconcile
GET  /v1/audit/operations/:id
```

同步、导出、删除和跨源命令均为异步任务或 Operation。客户端按 `job_id`/`operation_id` 轮询，展示
`completed`、`partially_completed`、`failed`、`unknown` 等终态；`unknown` 不能自动当作失败重试，必须等待
对账或人工确认。导出文件是短时、加密、受权限保护的临时工件，不能缓存到公共 URL。

## 管理端

管理入口由 Snaplink 权限控制，服务端仍会再次检查目标账户和任务权限：

```text
GET  /v1/admin/accounts?q=&status=&home_region=&limit=&cursor=
GET  /v1/admin/accounts/:id
GET  /v1/admin/accounts/:id/data
GET  /v1/admin/accounts/:id/overview
POST /v1/admin/accounts/:id/sync
POST /v1/admin/accounts/:id/export
POST /v1/admin/accounts/:id/erase
POST /v1/admin/accounts/backfill/legacy-identities
POST /v1/admin/migration/legacy/compare
GET  /v1/admin/migration/legacy/cutover/:tenant_id
POST /v1/admin/migration/legacy/cutover
GET  /v1/admin/migration/legacy/cleanup-readiness/:tenant_id
GET  /v1/admin/regions/directory/:account_id
POST /v1/admin/regions/promote
GET  /v1/audit/events
GET  /v1/audit/operations/:id
GET  /v1/audit/verify?source=&partition=
GET  /v1/admin/audit/holds
POST /v1/admin/audit/holds
DELETE /v1/admin/audit/holds/:scope_type/:scope_id
POST /v1/admin/audit/anchor
GET  /v1/admin/audit/anchor/manifest/:source/:partition
POST /v1/audit/export
POST /v1/audit/replays
POST /v1/audit/replays/projection
GET  /v1/audit/replays/:id
GET  /v1/admin/audit-log?action=&limit=&cursor=
GET  /v1/admin/roles
GET  /v1/admin/roles/:uid
POST /v1/admin/roles/grant
POST /v1/admin/roles/revoke
GET  /v1/admin/bans?limit=&cursor=
POST /v1/admin/bans
DELETE /v1/admin/bans/:uid
GET  /v1/admin/callback-subscriptions
POST /v1/admin/callback-subscriptions
DELETE /v1/admin/callback-subscriptions/:id
POST /v1/admin/callback-subscriptions/deliveries/:id/replay
```

Callback 注册和人工重放 Delivery 都必须为该次意图生成 `Idempotency-Key`；超时或响应丢失时，对同一请求复用原键。注册请求的幂等重放只返回订阅元数据，不会再次返回一次性 Secret；客户端应视为已注册并提示 Secret 不可恢复，不得因缺少 Secret 自动用新键重复创建。只有用户明确开始新的注册或新的重放意图时，才生成新键。

审计查询返回脱敏 payload、链序号、hash/signature 版本、`request_id`、`trace_id`、`operation_id` 和因果 ID。
Forensic replay 不产生副作用；dry-run 只模拟；controlled replay 必须重新授权、使用新的幂等键并产生新的
审计事实。Projection replay 只接受 `snaplink | aero-im | aero-vault | aero-id`，服务端再次校验 `audit.replay`
权限和当前认证 Tenant 下的 Account；每个窗口限制为 1–10000 条脱敏事件并独立审计。`completed=false` 时客户端只能
用服务端返回的 opaque `next_cursor` 手动读取下一窗口，不能自动全量扫描，也不能把单窗口 matched 当作全局一致；
该工具永远不写入或修复 live projection。

Legacy backfill 每批限制为 1–1000 个 UID，使用固定 `Idempotency-Key`，并在任何账户创建前记录独立的管理意图；
shadow compare 只接受 `aero-vault | aero-im`，最多读取 5000 条 content-free digest 输入，来源失败只返回稳定错误，
成功结果在离开服务前写入敏感读取审计。Cutover 只允许服务端状态机定义的下一跳，写命令使用固定幂等键和 Tenant ID
二次确认；服务端以读取到的旧状态做事务内 CAS，并与审计事实原子提交，`409 cutover.concurrent_transition` 后必须重新读取，
不能拿旧决策自动重试。cleanup readiness 只报告删除前置条件，不执行 legacy 数据清理。

WORM Anchor POST 需要 `system.config` 权限，扫描并校验全部当前审计链，且必须在一次确认流程中保持同一个
`Idempotency-Key`；服务端在写入任何不可变归档对象前先持久化 `audit.anchor.requested`，审计不可用时不产生归档。
后续单链失败会停止本轮执行，但之前写入的不可变锚点不会回滚；超时也不是远端失败证明，应先查询 Manifest。Manifest GET 只返回
指定 `(source, partition)` 的最新锚点，并在成功返回敏感证据前持久化 `audit.anchor.manifest_viewed`。

`/admin/audit-log`、`/admin/roles*` 和 `/admin/bans*` 是当前 OpenAPI 与 live handler 已发布的 legacy 管理面，
每个请求仍由服务端按权限校验；角色/封禁变更还会写管理审计事实。角色授予、撤销和封禁 POST 使用通用持久化
幂等中间件与 `Idempotency-Key`，控制台仍要求二次确认并在超时后复用同一意图和键；解封当前是 DELETE，未接入
通用幂等中间件，超时后必须先重新读取状态，不自动重试。

## 分页、区域和安全约束

- 高基数列表使用 keyset `cursor`，不要使用 offset；客户端应原样保存并回传 `next_cursor`。
- `home_region` 是账户写入路由依据；读取可来自本地区域的版本化投影，并展示来源区域和新鲜度。
- Region 提升必须使用刚读取的 `fenced_epoch` 做 CAS、携带固定 `Idempotency-Key` 并二次确认
  Account ID；成功响应的 `X-Aero-Write-Epoch` 指向目标 Writer，冲突后必须重新读取而不是自动重试旧
  Epoch。
- Snaplink 决定全局权限，IM/Vault 仍决定其资源 ACL；缓存 miss 或授权不确定不能当作允许。
- 客户端不得显示或记录 bearer token、callback secret、源凭据、对象字节、消息正文或 MFA/恢复材料。

回调请求带有 `X-Aero-Callback-Schema-Version`、`X-Aero-Callback-Timestamp` 和
`X-Aero-Callback-Signature: sha256=<hex>`。签名输入为
`<timestamp> + "\\n" + 原始请求体`；客户端应先校验时间窗口，再使用幂等的 `event_id` 处理事实。
