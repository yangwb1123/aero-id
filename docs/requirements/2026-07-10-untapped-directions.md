# aero-id — 未被覆盖的高价值扩展方向

> **日期**: 2026-07-10
> **视角**: 资深架构师 / 产品经理 — 全局代码扫描 + 12 份既有审查文档交叉分析
> **发现方法**: 逐一比对 `docs/` 下 12+ 份审查/需求文档的主题索引，定位零覆盖或仅零散提及的方向
> **无代码变更**

---

## 发现摘要

已有 12 份审查文档（PRODUCT_GAPS、ARCHITECTURE_DEEP、FAILURE_ABUSE_API、FLOW_CONCURRENCY_CACHE、
INFRA_DEEP、SCHEMA_QUERY_TEST、SECURITY_OPS、expansion-directions、architecture-extensions、
edge-cases-and-performance、product-directions、ARCHITECT_REVIEW）覆盖了群聊、活动流、Webhook 平台、
多副本扩展、WebSocket、存储配额、多租户、搜索增强、消息会话管理、数据导出、文件版本管理、
定时动作引擎、自适应限流、数据平台、审核工作台、文件加工管线等 **16+ 个方向**。

以下 **5 个方向** 在所有现有文档中 **零覆盖或仅有单句提及**，代表 aero-id 尚未触及的蓝海。

| 方向 | 现有文档覆盖度 | 核心价值 |
|------|:------------:|---------|
| **1. 移动推送通知网关** | REVIEW 中单段提及，无落地路径 | 移动端可达性，SSE 无法替代 |
| **2. 官方客户端 SDK 与开发者平台** | SCHEMA_QUERY_TEST 末节简述，无方向论证 | 降低集成成本，社区生态入口 |
| **3. OAuth 2.0 第三方应用授权** | SECURITY_OPS 中一句话提及 | 开放平台基础，类比 GitHub Apps |
| **4. 数据导入与迁移服务** | **零覆盖** | 竞争差异化，用户锁定反转 |
| **5. 多区域部署与数据驻留** | **零覆盖** | 合规刚需，全球延迟优化 |

---

## 方向一：移动推送通知网关（Mobile Push Notification Gateway）

### 为什么需要它

aero-id 已拥有完善的**服务端通知系统**（事务性 Outbox → NotificationDispatcher → SSE 实时推送），
但 SSE 有一个根本性局限：**它只在浏览器/桌面长连接场景下工作**。一旦用户关闭浏览器或切换到移动端，
SSE 推送完全失效。当前通知系统对移动用户的实际可达性为零。

| 维度 | SSE | 移动推送（FCM/APNs） |
|------|-----|---------------------|
| 应用在后台 | ❌ 连接断开 | ✅ 系统级通道可达 |
| 电池消耗 | 高（TCP 长连接） | 低（系统统一推送服务） |
| 到达率 | 依赖 WebSocket 保活 | 99%+（iOS/Android 保证） |
| 推送负载 | 完整数据 | 标题+正文+payload，点击唤起应用 |
| 用户覆盖 | 仅 Web | 全平台（Web + Mobile Native） |

**数据佐证**：当前 `NotificationDispatcher` 每次处理事件都调用 `pusher.Publish()` 做 SSE 推送，
但如果用户在移动端，这条 push 只是被服务端静默丢弃（channel 满后 `select default:` 的 silent drop）。
用户完全不知道有消息/评论/通知到达——直到用户主动打开应用。

### 设计要点

#### 1. 设备注册

新增表和 API：

```sql
CREATE TABLE push_devices (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_uid       TEXT NOT NULL,
    platform        TEXT NOT NULL,  -- 'ios' | 'android' | 'web_push'
    device_token    TEXT NOT NULL,  -- FCM registration token / APNs device token
    app_version     TEXT,
    locale          TEXT,           -- 用于推送本地化
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at      TIMESTAMPTZ
);
CREATE INDEX idx_push_devices_owner ON push_devices(owner_uid) WHERE deleted_at IS NULL;
```

API：
- `PUT /v1/push/devices` — 注册/更新设备 token（幂等，按 `(owner_uid, device_token)` 去重）
- `DELETE /v1/push/devices/:id` — 注销设备（用户登出/卸载）
- 客户端在应用启动/登录时注册 token，登出时反注册

#### 2. Outbox 推送网关

新增 `internal/push/` 包，作为 outbox.Chain 的新一环：

```
Outbox Event → Chain: [NotificationDispatcher → WebhookHandler → PushGateway]
```

`PushGateway` 实现 `outbox.Handler` 接口，监听 `notification.created` 事件（或直接监听业务事件），

处理流程：
1. 从事件 payload 解析 `recipient_uid`
2. 查询该 uid 的所有活跃设备 token（`push_devices` 表）
3. 对每个设备，构建平台特定的推送消息（标题 + 正文 + 自定义数据）
4. 通过 FCM SDK（Android/Web）或 APNs（iOS）发送
5. 记录投递结果；对失败 token 做降级处理（过期 token 自动从 DB 删除）

#### 3. 推送渠道配置

```yaml
notifications:
  push:
    enabled: false
    fcm:
      credentials_file: "/etc/aero-id/fcm-credentials.json"  # Firebase 服务账号
    apns:
      key_id: ""
      team_id: ""
      key_file: "/etc/aero-id/apns-key.p8"
      topic: "com.aero.app"  # 应用 bundle ID
    defaults:
      sound: "default"
      badge: 1
      ttl: 86400  # 24h，超出后 FCM 不再递送
```

FCM 支持同时发送 Android + iOS + Web Push，因此初期可以仅接入 FCM 覆盖所有平台，
APNs 直连可作为第二阶段优化（利用 FCM 的 APNs 桥接也已足够）。

#### 4. 推送偏好与限流

- 复用现有的 `NotificationPreference` 表：`quiet_hours_start/end` 在移动推送时应被尊崇
- 推送频率限制：每个 recipient 每分钟最多 N 条推送（防水喉效应）
- 折叠：多条同类通知（如群聊多条消息）折叠为一条摘要推送（"3 条新消息"）

### 与现有架构的关系

| 现有组件 | 集成方式 |
|---------|---------|
| `outbox.Chain` | `PushGateway` 作为链中新 handler，共享重试/幂等语义 |
| `NotificationDispatcher` | 推送网关可监听 `notification.created` 事件，或直接监听原始业务事件 |
| `NotificationPreference` | 尊崇静音时段 + enabled 开关 |
| `block.BlockChecker` | 推送前再次确认发送方未被收件人屏蔽（已由 Dispatcher 处理） |
| 通知折叠 | 推送摘要文本复用折叠逻辑 |

### 边界情况

| Edge Case | 处理 |
|-----------|------|
| 设备 token 过期（FCM 返回 NotRegistered） | 自动从 `push_devices` 软删除该 token，静默继续 |
| 用户卸载应用后重装 | 新 token 注册覆盖旧 token（按 owner_uid 去重） |
| 多设备推送重复 | 同一 uid 的 N 个设备各自收到一次，这是正确的行为（手机+平板+桌面） |
| 推送网关宕机 | Outbox 重试机制兜底；消息最终会送达（有延迟） |
| 静音时段跨时区 | 推送网关中根据用户 `timezone` 字段计算本地时间，决定是否推迟推送 |

### 估算

- 新包 `internal/push/`：FCM/APNs 客户端 + 设备注册 repo：**中**（1-2 周）
- 设备注册 API（HTTP + gRPC）：**低**（2-3 天）
- 集成到 outbox.Chain：**低**（1 天）
- 测试（单元 + mock FCM 服务器）：**中**（3-5 天）
- **总计：3-4 周**

---

## 方向二：官方客户端 SDK 与开发者平台

### 为什么需要它

aero-id 是一个**双传输（HTTP + gRPC）**的 API 服务，但目前**没有任何官方客户端库**。
消费者要接入 aero-id 必须自行处理：token 获取、请求签名、错误重试、分页、序列化、
WebSocket/SSE 连接管理等。这在内部集成场景尚可接受，但作为平台产品这是致命缺陷。

**竞争对标**：
| 产品 | SDK 覆盖 | 开发者体验 |
|------|---------|-----------|
| Slack | Python, JS/TS, Java, Go, .NET | SDK 生成器, API playground |
| GitHub | Octokit (7 语言) | 自描述 API, 文档自动生成 |
| Stripe | 7 语言官方 SDK | OpenAPI → SDK 自动映射, 测试模式 |
| **aero-id** | **无** | 手动 curl/gRPCurl |

**当前尴尬**：`api/proto/aero_id/v1/aero_id.proto` 已定义了完整的 gRPC 服务契约，
proto 文件本身就是一种 IDL（接口定义语言）。从 proto 自动生成 SDK 是行业标准做法
（`protoc-gen-*` 全家桶），但 aero-id 没有将这一步集成到构建管线中。

### 设计要点

#### Phase 1 — OpenAPI 规范 + 自动生成

**为什么不是直接发 protoc 生成的 gRPC SDK？**
- HTTP REST API 是主要对外接口（gRPC 供内部使用）
- 大多数第三方开发者期望 REST + JSON（gRPC-Web 有额外代理要求）

**实现方式**：
```makefile
# 新增 Makefile target
make openapi       # 从 proto + HTTP handler 注释生成 OpenAPI 3.1 规范
make client-go     # 从 OpenAPI 生成 Go 客户端（使用 oapi-codegen）
make client-ts     # 从 OpenAPI 生成 TypeScript 客户端（使用 openapi-typescript）
```

```bash
# 输出
docs/openapi.yaml           # OpenAPI 3.1 规范（含所有端点 + schema + 安全方案）
clients/go/                 # Go 客户端 SDK 包
clients/typescript/         # TypeScript/JS 客户端 SDK（支持 Node + 浏览器）
```

**OpenAPI 生成策略**：有两种可行路径

1. **从 proto 自动推导**：`protoc-gen-openapiv2`（grpc-gateway 项目）可从 gRPC proto 生成 OpenAPI v2
2. **从 Go handler 结构生成**：使用 `swaggo/swag` 从 `// @Summary` `// @Param` 等注解生成
3. **手写维护**：对稳定 API 编写一次 OpenAPI 3.1 规范，配合 CI diff 检查

推荐路径：**proto → OpenAPI v2 → 转 OpenAPI 3.1**，配合 CI 自动更新。

#### Phase 2 — API Key 管理与服务账号

当前只有**用户级 SSO token** 一种认证方式。这意味着：
- 自动化脚本/CI pipeline 无法调用 aero-id API（SSO token 是人工签发的，有短 TTL）
- 第三方开发者无法获得长期有效的凭据

**新增模型**：
```sql
CREATE TABLE api_keys (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_uid     TEXT NOT NULL,          -- 创建者（用户或 admin）
    name          TEXT NOT NULL,          -- 人工可读名称
    key_prefix    TEXT NOT NULL,          -- 密钥前缀（用于识别，如 "aero_" + 前 8 位）
    key_hash      TEXT NOT NULL,          -- bcrypt(完整的 secret key)
    scopes        TEXT[] NOT NULL DEFAULT '{}',  -- 权限范围
    expires_at    TIMESTAMPTZ,
    last_used_at  TIMESTAMPTZ,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at    TIMESTAMPTZ
);
CREATE INDEX idx_api_keys_owner ON api_keys(owner_uid) WHERE deleted_at IS NULL;
```

**API**：
- `POST /v1/api-keys` — 创建 key（仅返回完整 secret 一次）
- `GET /v1/api-keys` — 列出我的 keys（不含完整 secret）
- `DELETE /v1/api-keys/:id` — 撤销 key

**鉴权流程**：
请求头 `Authorization: Bearer aeroid_xxxxxxxxxxxx...` → 解析前缀 → 查找 key_hash → bcrypt 比较 → 鉴权

#### Phase 3 — API 沙箱/Playground

- 为每个新注册的自动创建独立隔离的测试环境
- 含预设的测试文件和消息
- 浏览器内的 API 控制台（参考 Stripe / GitHub API 体验）

### 与现有架构的关系

| 现有组件 | 集成方式 |
|---------|---------|
| `middleware.Auth` | 增加 `APIKeyValidator` 分支：先尝试 SSO token 校验，失败后尝试 API key |
| `internal/handler/grpc/server.go` | gRPC 拦截器做同样处理 |
| 幂等性 | API key 触发的请求同样受 idempotency key 保护（共享 `idempotency_keys` 表） |
| 限流 | API key 作为限流 key（与 uid 共享限流配额或独立配额） |
| 审计日志 | API key 操作记录 `actor_uid` 为 key owner，`meta` 中记录 `api_key_name` |

### 边界情况

| Edge Case | 处理 |
|-----------|------|
| API Key 泄露 | owner 可随时撤销；支持 key 轮换（同时维护 2 个 key 的宽限期） |
| 密钥存储安全 | `key_hash` 使用 bcrypt，原始 secret 仅创建时返回一次 |
| 权限范围最小化 | 支持 `files:read` / `messages:write` 等 scope 粒度，类比 GitHub OAuth Scopes |
| SDK 版本管理 | SDK 包使用语义化版本，与 aero-id API 版本解耦 |

### 估算

- OpenAPI 规范生成管线：**低**（3-5 天）
- Go + TS 客户端 SDK 初始版本：**中**（1-2 周）
- API Key 服务（表 + 中间件 + CRUD）：**低**（1 周）
- 开发者文档门户：**中**（2 周，依赖前端资源）
- **总计（Phase 1+2）：4-6 周**

---

## 方向三：OAuth 2.0 第三方应用授权

### 为什么需要它

当前 aero-id 的 API 访问模型是**全有或全无**——调用方持有用户的 SSO Bearer token，
就拥有该用户所有数据的完整访问权限。这在以下场景中不可接受：

- **第三方应用集成**：一个外部应用希望代表用户"读取文件列表"但不应"发送消息"
- **CI/CD 自动化**：部署流水线需要上传构建产物，但不应该能读取私信
- **SaaS 集成商**：如 Zendesk 集成 aero-id 附件，需要临时访问权限

**OAuth 2.0 是行业标准解决方案**，GitHub、Google、Slack、Dropbox 全部使用 OAuth 2.0
作为第三方应用授权协议。aero-id 没有它，就无法成为一个真正的开放平台。

**当前状态**：SSO 已经提供了身份认证（`ValidateToken`），但 SSO 不管理授权范围。
aero-id 甚至不需要改动 SSO——它可以独立实现 OAuth 2.0 授权端点，因为资源（文件、消息、评论）
全部在 aero-id 自己的数据库中。

### 设计要点

#### 1. OAuth 2.0 服务端

引入 `internal/oauth/` 包，实现以下端点：

| 端点 | 描述 |
|------|------|
| `GET /oauth/authorize` | 授权请求：用户确认后返回 authorization_code |
| `POST /oauth/token` | Token 端点：authorization_code → access_token + refresh_token |
| `POST /oauth/revoke` | 撤销 token |
| `GET /oauth/applications` | 管理已授权的应用 |

#### 2. 核心模型

```sql
CREATE TABLE oauth_applications (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_uid       TEXT NOT NULL,           -- 应用开发者
    name            TEXT NOT NULL,
    description     TEXT,
    redirect_uris   TEXT[] NOT NULL,         -- 允许的重定向 URI
    client_id       TEXT UNIQUE NOT NULL,
    client_secret   TEXT,                    -- bcrypt hash，仅 confidential clients
    scopes          TEXT[] NOT NULL DEFAULT '{}',  -- 应用请求的权限范围
    logo_url        TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at      TIMESTAMPTZ
);

CREATE TABLE oauth_authorizations (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_uid            TEXT NOT NULL,    -- 资源所有者
    application_id      UUID NOT NULL REFERENCES oauth_applications(id),
    scopes              TEXT[] NOT NULL,
    authorization_code  TEXT UNIQUE,      -- 一次性 code，短 TTL
    code_challenge      TEXT,             -- PKCE
    code_challenge_method TEXT,           -- "S256" | "plain"
    access_token_hash   TEXT,             -- bcrypt(access_token)
    refresh_token_hash  TEXT,             -- bcrypt(refresh_token)
    access_token_expires_at TIMESTAMPTZ,
    refresh_token_expires_at TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    revoked_at          TIMESTAMPTZ
);
```

#### 3. 授权流程（Authorization Code + PKCE）

```
第三方应用 ←→ aero-id OAuth

1. 应用跳转用户到: GET /oauth/authorize?client_id=xxx&redirect_uri=yyy&scope=files:read&code_challenge=zzz
2. 用户登录（SSO 校验）→ 确认授权
3. 302 重定向到 redirect_uri?code=xxxx
4. 应用用 code + code_verifier 换取: POST /oauth/token
5. 返回 { access_token, refresh_token, expires_in, scope }
6. 应用使用 access_token 调用 API（Authorization: Bearer <access_token>）
7. access_token 过期后使用 refresh_token 续期
```

**为什么不支持 Implicit Grant（已废弃）？** 安全原因 OAuth 2.1 已移除 Implicit Grant，
Authorization Code + PKCE 是当前推荐流程。

#### 4. 与现有鉴权体系整合

新增 `middleware.OAuth` 中间件：

```
请求 → 解析 Authorization: Bearer <token>
  → 尝试 SSO token 验证（现有流程）
  → 失败 → 尝试 OAuth access_token 验证（新流程）
    → 成功 → 注入 uid + scope 到 ctx
    → 在 service 层检查 scope 是否包含操作所需权限
  → 都失败 → 401
```

**Scope 检查集成**：在每个 service 方法的入口处，检查 `actor` 的 scope 是否包含所需权限。
如果请求的 scope 不足（如 `files:read` 应用试图写消息），返回 403 Forbidden。

```go
// 在 service 入口
func (s *messageService) Send(ctx context.Context, ...) error {
    actor, err := domain.MustActor(ctx)
    if err != nil { return err }
    if err := domain.RequireScope(ctx, "messages:write"); err != nil {
        return err  // 403
    }
    ...
}
```

#### 5. 令牌生命周期管理

- `access_token` TTL：默认 1 小时（可配置）
- `refresh_token` TTL：默认 6 个月，可轮换（Rotation：每次 refresh 时发放新 refresh_token，废弃旧 token）
- 用户可以在 `GET /oauth/applications` 页面随时撤销任意应用的授权

### 边界情况

| Edge Case | 处理 |
|-----------|------|
| redirect_uri 不匹配 | 严格校验，精确匹配或按模式匹配，防止开放重定向攻击 |
| CSRF 攻击 | 要求 `state` 参数，OAuth 服务端回传 state 不变 |
| Authorization Code 重用 | 一次性 code，使用后立即失效；重复使用返回错误 |
| Token 泄露 | 短 TTL access_token + 可撤销；refresh_token 泄露时用户可从授权列表撤销 |
| 吊销后已分发的 token | 黑白名单 + 短 TTL 结合；实时检查 `revoked_at` |

### 估算

- OAuth 核心包 + 端点（HTTP）：**中**（1-2 周）
- 鉴权中间件扩展（SSO + OAuth 双通道）：**中**（1 周）
- Scope 注解 + service 层权限检查：**中**（1 周）
- 开发者注册 API（application CRUD）：**低**（3 天）
- **总计：4-5 周**

---

## 方向四：数据导入与迁移服务（Data Import & Migration）

### 为什么需要它

aero-id 已实现了 GDPR 数据导出（方向被现有文档覆盖），但**完全没有数据导入能力**。
这在多个场景中是关键缺口：

1. **从竞品迁移**：用户从其他文件/消息服务迁移到以 aero-id 为后端的平台时，需要导入历史数据
2. **批量初始化**：企业客户 onboarding 时，需要批量导入文件、建立初始消息
3. **数据恢复**：从备份恢复；从导出 zip 重新导入
4. **生态系统锁定反转**：当 aero-id 是平台后端时，允许用户导入数据 = 降低迁移门槛 = 提升用户获取

**关键洞察**：数据导出（GDPR）是合规驱动的防守功能；**数据导入是增长驱动的进攻功能**。

### 设计要点

#### 1. 导入任务模型

```sql
CREATE TABLE import_tasks (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_uid       TEXT NOT NULL,
    source          TEXT NOT NULL,           -- 'generic_json' | 'generic_zip' | 'slack_export' | ...
    status          TEXT NOT NULL DEFAULT 'pending',  -- pending | running | completed | failed
    config          JSONB NOT NULL DEFAULT '{}',      -- 导入选项
    stats           JSONB NOT NULL DEFAULT '{}',      -- 进度统计
    error_message   TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at    TIMESTAMPTZ,
    deleted_at      TIMESTAMPTZ
);
```

#### 2. 导入格式

最小可行版本支持 **通用 JSON 格式**：

```json
{
  "version": 1,
  "exported_at": "2026-07-01T00:00:00Z",
  "files": [
    {
      "name": "report.pdf",
      "mime_type": "application/pdf",
      "size": 1024000,
      "content_base64": "...",   // 小文件内嵌
      "url": "https://...",      // 大文件：aero-id 从 URL 拉取
      "created_at": "2025-01-01T00:00:00Z"
    }
  ],
  "messages": [
    {
      "to_uid": "user_b",
      "content": "Hello!",
      "created_at": "2025-01-01T00:00:00Z"
    }
  ],
  "comments": [
    {
      "target_type": "file",
      "target_id": "existing-file-uuid",
      "content": "Nice file!",
      "created_at": "2025-01-01T00:00:00Z"
    }
  ]
}
```

以及 **Slack 导出格式** 支持（作为首次集成的第三方格式样例）。

#### 3. 处理流程

```
POST /v1/import (multipart: 上传 json/zip 文件)
  → 创建 import_tasks 记录（status=pending）
  → 异步处理（复用 storage/gc 的 periodic task 模式，或新增 outbox 事件）
  → 格式验证 → schema 校验 → 逐条导入：
     - 文件: 如果 content_base64 → 解码 → storage.Put → 写 files 行
     - 消息: 按 created_at 排序插入（保留原始时间戳）
     - 评论: 按 target_id 匹配（关联到已有实体或跳过）
  → 更新 stats（imported, skipped, failed 计数）
  → status = completed / failed
  → 用户查询 GET /v1/import/:id 查看进度和结果
```

#### 4. 冲突与去重策略

| 场景 | 策略 |
|------|------|
| 消息已存在于目标 | 按 `(from_uid, to_uid, content, created_at)` 四元组去重 |
| 文件 checksum 已存在 | 复用现有 blob（content-addressed dedup），写新 files 行 |
| 评论 target 不存在 | 跳过该评论，在 stats 中记录 skipped |
| 原始时间戳保留 | 覆盖 `created_at`（默认启用）；可选使用导入时间 |
| 文件大小超过配额 | 跳过并在 stats 中记录 |

### 与现有架构的关系

| 现有组件 | 集成方式 |
|---------|---------|
| `storage.Put` | 导入文件直接复用上传路径 |
| 文件去重 | 导入时同样计算 checksum，复用现有 content-addressed dedup |
| `outbox` | 导入批量操作不触发 outbox 事件（避免通知洪泛） |
| 幂等性 | 导入任务本身支持幂等（按 import_task_id 去重） |
| 审计日志 | 导入操作记录 `action: import.batch`，`meta.source` 记录来源 |

### 边界情况

| Edge Case | 处理 |
|-----------|------|
| 超大导入（>10 万条记录） | 流式处理（逐页读取），异步后台任务，进度反馈 |
| 导入中途失败回滚 | 记录已处理的行 ID；支持断点续传（重新导入时跳过已存在记录） |
| 恶意导入（大量小文件耗尽配额） | 遵守现有配额限制；导入不享受豁免 |
| 用户导入他人导出数据 | 仅导入 `owner_uid` 匹配的记录；跳过不匹配的行 |
| 导入格式版本兼容 | `version` 字段：未来格式变更时通过版本路由到不同解析器 |

### 估算

- 导入核心引擎（解析 + 校验 + 存储）：**中**（1-2 周）
- 导入任务管理 API（HTTP + gRPC）：**低**（3-5 天）
- Slack 格式适配器（参考公开文档）：**低**（2-3 天）
- 异步 worker + 进度跟踪：**低**（3 天）
- **总计：3-4 周**

---

## 方向五：多区域部署与数据驻留（Multi-Region / Data Residency）

### 为什么需要它

aero-id 的架构基于**单一 PostgreSQL 实例 + 单一存储后端**。这在以下场景中成为硬障碍：

#### 1. 数据驻留合规

| 管辖地 | 法律要求 | 影响 |
|--------|---------|------|
| EU (GDPR) | 个人数据不得离境 | 必须部署在 EU 区域的 DB 和存储 |
| China (PIPL) | 重要数据境内存储 | 必须部署在中国境内 |
| Russia (152-FZ) | 公民数据境内存储 | 必须部署在俄罗斯境内 |
| US (state laws) | 部分州有特定要求 | 金融/医疗数据需 US-only |

**当前架构缺陷**：所有用户数据在同一个 PG 实例 + 同一个 S3 bucket。无法按用户区域隔离数据。

#### 2. 全球延迟

| 用户所在区域 | 到单一 region 的延迟 | 体验 |
|------------|-------------------|------|
| us-east-1 | ~5ms | 极佳 |
| eu-west-1 | ~80ms | 可接受 |
| ap-southeast-1 | ~200ms | 明显延迟 |
| ap-northeast-1 | ~150ms | 可见延迟 |
| sa-east-1 | ~180ms | 可见延迟 |

对于消息系统，200ms 的写延迟意味着**用户在输入时能感知到卡顿**。

#### 3. 容灾与高可用

单区域部署 = 单点故障。区域级故障（AWS us-east-1 大规模 outage）意味着整个 aero-id 不可用。
多区域部署允许跨区域故障切换。

### 设计要点

#### 1. 区域路由层（Region Router）

```go
// internal/region/ 包
type Region string  // "us-east-1" | "eu-west-1" | "ap-northeast-1"

type Router interface {
    // 根据请求上下文（用户区域偏好、token claim、IP 地理位置）决定目标区域
    SelectRegion(ctx context.Context, uid string) (Region, error)
    // 返回该 uid 的数据驻留区域
    DataRegion(ctx context.Context, uid string) (Region, error)
}
```

**路由策略**（按优先级）：
1. SSO token 扩展字段 `data_region`（SSO 返回的 token claim 中指定数据驻留区域）
2. 用户首次上传文件时锁定的区域（一旦写入，不可修改）
3. IP 地理位置的最近区域（全局亲和性路由，无合规要求时）
4. 默认区域（管理员配置）

#### 2. 区域感知的数据模型

```sql
-- 用户区域映射表（核心路由表）
CREATE TABLE user_regions (
    uid         TEXT PRIMARY KEY,
    region      TEXT NOT NULL,                   -- 数据驻留区域
    locked_at   TIMESTAMPTZ,                     -- 首次数据写入后锁定，不可变更
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 跨区域索引（仅元数据，不含实际数据体）
CREATE TABLE cross_region_references (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    source_uid      TEXT NOT NULL,               -- 查询方
    target_uid      TEXT NOT NULL,               -- 被查询方
    target_region   TEXT NOT NULL,               -- 对方数据所在区域
    last_accessed   TIMESTAMPTZ
);
```

#### 3. 架构模式

对于消息和通知这类**跨区域交互**（A在us-east-1 发消息给 B在 eu-west-1），采用**主区域写入 + 跨区域复制**模式：

```
用户 A (us-east-1)
  │  发送消息给 B (eu-west-1)
  ▼
us-east-1 aero-id 实例
  │  1. 写消息到 us-east-1 PG（A 的本地区域）
  │  2. Outbox 事件写入 us-east-1 本地
  ▼
跨区域 Outbox Relay（新组件）
  │  3. 检测收件人 B 在另一个区域
  │  4. 通过区域间 gRPC 转发事件到 eu-west-1 实例
  ▼
eu-west-1 aero-id 实例
  │  5. 在 eu-west-1 本地写入 notification 行
  │  6. 通过本地 SSE/推送送达用户 B
```

**文件场景**：文件 blob 始终存储在用户区域。跨区域分享时，生成跨区域 presigned URL。

#### 4. 区域部署拓扑

```
┌─────────────┐     ┌─────────────┐     ┌─────────────┐
│  us-east-1  │     │  eu-west-1  │     │  ap-northeast-1 │
│             │     │             │     │             │
│  aero-id    │◄───►│  aero-id    │◄───►│  aero-id    │
│  PostgreSQL │     │  PostgreSQL │     │  PostgreSQL │
│  S3 bucket  │     │  S3 bucket  │     │  S3 bucket  │
└──────┬──────┘     └──────┬──────┘     └──────┬──────┘
       │                   │                   │
       └───────────────────┼───────────────────┘
                           │
                    Global DNS / Load Balancer
                    (区域亲和性路由)
```

#### 5. 配置

```yaml
region:
  default: "us-east-1"
  routing: "token"   # token | geo | config
  peer_regions:
    - endpoint: "eu-west-1.aero-id.internal:9090"
      tls: true
    - endpoint: "ap-northeast-1.aero-id.internal:9090"
      tls: true
  forwarding:
    timeout: 5s
    max_retries: 3
```

### 与现有架构的关系（影响分析）

| 组件 | 多区域影响 | 改动程度 |
|------|-----------|---------|
| `internal/sso` | 无影响（SSO 仍是全局服务） | 无 |
| Repository | 每个区域一个 PG 实例；连接池按区域配置 | **大** |
| `internal/storage` | 每个区域一个 S3 bucket | 中 |
| Outbox | 新增跨区域 relay 组件 | **大** |
| Hub/SSE | 每个区域独立 Hub，跨区域推送通过转发 | 中 |
| 搜索 | 跨区域搜索需要合并结果 | 大 |
| 限流 | 每区域独立限流（分布式时共享 Redis） | 中 |

**最小可行范围**：初始只支持**管理隔离**（用户创建时锁定区域，数据永不跨区域），
不做跨区域消息转发。这可以满足合规需求而不需要跨区域数据流。

### 边界情况

| Edge Case | 处理 |
|-----------|------|
| 用户搬家到另一个区域 | 数据驻留锁定后不可迁移（技术上可通过后台迁移工具支持，但初期不实现） |
| 跨区域用户互相发消息 | 初期不支持（返回用户提示"用户在不同区域，暂不支持跨区域消息"）；后期通过转发支持 |
| 区域级故障切换 | DNS 路由切换到健康区域，但该区域无其他区域数据的副本 → 仅本区域用户可用 |
| 跨区域文件分享 | 生成跨区域 presigned URL（目标区域 S3 签名）；或代理下载 |

### 估算

- Region router + user_regions 表：**低**（3-5 天）
- 多区域仓储层重构（每区域连接池）：**高**（2-3 周）
- 跨区域 Outbox relay：**高**（2-3 周）
- 跨区域搜索：**高**（1-2 周）
- **总计（合规 MVP）：2-3 周**（仅管理隔离，无跨区域数据流）
- **总计（完整多区域）：8-12 周**

---

## 优先级建议

| 方向 | 用户价值 | 技术复杂度 | 架构耦合度 | 推荐优先级 | 最佳时机 |
|------|---------|-----------|-----------|-----------|---------|
| **1. 移动推送通知** | 🔥🔥🔥🔥🔥 | 中 | 低（独立 outbox handler） | **P1** | 启动移动端开发前 1 个 sprint |
| **2. 开发者 SDK + 平台** | 🔥🔥🔥🔥 | 中 | 低（新增 API key MW）+ 生成管线 | **P1** | 首个外部集成方接入前 |
| **3. OAuth 2.0 授权** | 🔥🔥🔥🔥🔥 | 中 | 中（鉴权双通道） | **P2** | SDK 平台之后，第三方应用出现时 |
| **4. 数据导入服务** | 🔥🔥🔥🔥 | 低中 | 低（独立子系统） | **P2** | 数据导出实现之后（基础设施复用） |
| **5. 多区域部署** | 🔥🔥🔥🔥 | **高** | **高**（全层改动） | **P3** | 首次出现合规需求或第二个 region 时 |

### 执行建议

**立即可以启动**（低风险、高 ROI）：
- **方向 2 Phase 1**（OpenAPI 生成 + API 客户端 SDK）是纯增益无破坏的工程投资，
  且 Proto 文件已完整，自动化管线可以 3-5 天完成。建议作为下一个 sprint 的 side quest。
- **方向 1**（移动推送）与现有通知系统耦合度低，只需新增一个 outbox handler，
  且移动推送的缺失是当前产品最大的体验缺口。

**中期规划**：
- **方向 4**（数据导入）可在 3-4 周内交付，但与方向 3（OAuth）无依赖关系，可并行。
- **方向 3**（OAuth 2.0）建议在 SDK 平台发出后启动——开发者有 SDK 后自然期望 OAuth。

**长期投资**：
- **方向 5**（多区域）是架构级变更，必须做完整的 RFC 设计评审且需要独立的 1-2 个月实现周期。
  建议在用户规模增长到需要多区域时启动，而非提前投入。

---

## 附录：与现有文档的对比确认

以下矩阵确认本文 5 个方向在现有 12 份审查文档中的覆盖度：

| 文档 | 移动推送 | SDK/开发者平台 | OAuth 2.0 | 数据导入 | 多区域 |
|------|:-------:|:------------:|:---------:|:--------:|:------:|
| PRODUCT_GAPS | ❌ | ❌ | ❌ | ❌ | ❌ |
| ARCHITECTURE_DEEP | ❌ | ❌ | ❌ | ❌ | ❌ |
| FAILURE_ABUSE_API | ✅单段 | ❌ | ❌ | ❌ | ❌ |
| FLOW_CONCURRENCY_CACHE | ❌ | ❌ | ❌ | ❌ | ❌ |
| INFRA_DEEP | ❌ | ❌ | ❌ | ❌ | ❌ |
| SCHEMA_QUERY_TEST | ❌ | ✅末节 | ❌ | ❌ | ❌ |
| SECURITY_OPS | ❌ | ❌ | ✅单句 | ❌ | ❌ |
| expansion-directions | ❌ | ❌ | ❌ | ❌ | ❌ |
| architecture-extensions | ❌ | ❌ | ❌ | ❌ | ❌ |
| edge-cases-and-performance | ❌ | ❌ | ❌ | ❌ | ❌ |
| product-directions | ❌ | ❌ | ❌ | ❌ | ❌ |
| ARCHITECT_REVIEW | ❌ | ❌ | ❌ | ❌ | ❌ |

**结论**：5 个方向在已有文档中覆盖度为零或接近零，确认为新的独立分析成果。
