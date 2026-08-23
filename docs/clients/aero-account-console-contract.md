# aero-account-console 后端契约

这是独立账户客户端的最小后端契约。客户端只访问 aero-id；除登录跳转外，不直接调用 Snaplink、Aero IM 或 Aero Vault。

## OAuth client registration（P7-02）

在 Snaplink 注册**独立** OAuth/OIDC client（禁止复用 snaplink-console 的 client）：

| 项 | 值（示例） |
|---|---|
| client_id | `aero-account-console` |
| redirect_uri | `https://console.<region>.example.com/` |
| audience | `aero-id` |
| grant | Authorization Code + PKCE（S256，强制） |
| scopes | `openid profile account:read` 起步；按需加 `account:write account:manage audit:read audit:replay operation:read callback:manage source:sync` |

前端已选用 Iris UI 的 React 适配器，首个可运行客户端位于
`/home/u1/iris-ui/apps/aero-platform-console`。本契约与 OpenAPI
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
GET  /v1/admin/regions/directory/:account_id
POST /v1/admin/regions/promote
GET  /v1/audit/events
GET  /v1/audit/operations/:id
GET  /v1/audit/verify?source=&partition=
POST /v1/audit/replays
GET  /v1/audit/replays/:id
GET  /v1/admin/callback-subscriptions
POST /v1/admin/callback-subscriptions
DELETE /v1/admin/callback-subscriptions/:id
POST /v1/admin/callback-subscriptions/deliveries/:id/replay
```

审计查询返回脱敏 payload、链序号、hash/signature 版本、`request_id`、`trace_id`、`operation_id` 和因果 ID。
Forensic replay 不产生副作用；dry-run 只模拟；controlled replay 必须重新授权、使用新的幂等键并产生新的
审计事实。

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
