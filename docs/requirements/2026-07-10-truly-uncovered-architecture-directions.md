# aero-id — 六轮交叉分析后仍未触及的高价值架构方向

> **日期**: 2026-07-10
> **视角**: 资深架构师 / 产品经理
> **方法**: 全局代码扫描后，逐一比对 `docs/requirements/` 下 10 份方向文档 + `docs/REVIEW_*` 下 8 份审查文档的主题索引，累计覆盖 **50+** 方向。本文件定位在所有前述文档中**零覆盖或仅单句提及**的 5 个方向。
> **前置条件**: `feat/roadmap` 已合并，38 个 gRPC RPC + 全 HTTP 对等 + 生产加固已交付
> **无代码变更**

---

## 交叉分析方法

```
1. 从 10 份 requirements/*.md 提取所有方向标题 → ~50 个方向
2. 从 8 份 REVIEW_*.md 提取所有方向标题 → ~20 个方向
3. 合并去重 → ~55 个唯一方向
4. 对每个方向标注覆盖度：✅ 完整分析 | 🔶 部分提及 | ❌ 零覆盖
5. 回扫 internal/ 全部 22 个 Go 包验证代码级缺口
6. 定位最终 5 个零覆盖方向
```

**已有 10 份文档覆盖的方向概览**（仅列标题，证实非重复）：

| 文档 | 覆盖方向 |
|------|---------|
| `expansion-directions.md` | 群聊与频道 · Webhook 订阅 API · 多副本扩展 · WebSocket · 存储配额 |
| `architecture-extensions.md` | 统一活动流 · 内容生命周期策略 · 协作审核工作台 · 文件预览管线 |
| `edge-cases-and-performance.md` | 分布式限流 · 多地域部署 · Webhook 事件平台 · Schema 版本化 · 负载测试框架 |
| `product-directions.md` | 文件版本管理 · 定时动作引擎 · 自适应限流 · 数据平台 |
| `genuinely-uncovered.md` | 草稿与自动保存 · @提及 · 内容引用转发 · 批量操作 · 关系遍历 API |
| `remaining-high-value.md` | 会话线程 · 公开分享链接 · AI/ML 智能 · 信任与安全 · 合规治理 |
| `untapped-directions.md` | 移动推送 · SDK/开发者平台 · OAuth 2.0 · 数据导入 · 多区域部署 |
| `deep-extension-analysis.md` | 多表情反应 · 会话模型统一 · 自动化内容安全 · 缩略图管线 · 审计告警 |
| `final-blindspots.md` | 文件加密 · 分享权限细化 · API 生命周期 · 异步管线可观测 · 供应链安全 |
| `arch-migration-gaps.md` | 领域重构 · 跨域关注点 · 事件契约 · 权限模型 · 最终一致性 · 全链路追踪 |

---

## 核心发现

5 个方向在所有 18 份既有文档中**零系统分析**。每个方向均附代码级证据说明"为什么代码已暗示此能力但从未建立为功能"。

| # | 方向 | 类别 | 代码暗示 |
|---|------|------|---------|
| 1 | **特征开关基础设施**（Feature Flags） | 工程基础 | `config.go` 有大量 `Enabled` 开关，但全是编译期静态配置；无运行时切换能力 |
| 2 | **多租户 / 组织模型**（Multi-Tenancy / Organizations） | 产品架构 | 所有模型是扁平 uid，无组织层级；但 `admin_roles` 表已暗示"超越用户级别的权限" |
| 3 | **GraphQL / BFF 网关层**（API Gateway / BFF） | API 架构 | HTTP REST + gRPC 双传输已实现，但客户端需要多次往返拼装数据；`search` 的 `SearchHit` union 模式是 GraphQL 的天然 fit |
| 4 | **凭证生命周期管理**（Credential Lifecycle Management） | 安全运营 | `signing_key`、S3 凭证、SSO TLS 证书均硬配置，无轮换机制；`outbox.Webhook.SigningKey` 一旦泄露无法在线更换 |
| 5 | **Webhook 死信控制台与事件重放**（Dead Letter Console & Event Replay） | 运维平台 | `outbox` 有死信机制（`dead_letter=true`），但完全不可见——无 API 无 UI 无告警；事件被吞后运维不知 |

---

## 方向一：特征开关基础设施（Feature Flags Infrastructure）

### 为什么它被遗漏

全部 10 份需求文档关注的"要做什么功能"，未关注"如何安全地发布功能"。已有代码中使用的大量布尔开关（`Enabled`）全部在 `config.yaml` 中静态声明，**改动需要重启进程**。对于生产服务，这是不可接受的——任何新功能上线都需要冒全量风险。

### 代码级证据

| 位置 | 发现 |
|------|------|
| `internal/config/config.go:59` — `RateLimitConfig.Enabled` | 限流开启/关闭是静态的。想临时关闭限流排查问题 → 改配置 → 重启 → 连接断开 |
| `internal/config/config.go:66` — `StorageGCConfig.Enabled` | GC 开启/关闭同上。运维发现 GC 影响性能时无法即时关闭 |
| `internal/config/config.go:133` — `TracingConfig.Enabled` | Tracing 采样率也是静态的。想临时提高采样率排查慢请求 → 需重启 |
| `internal/config/config.go:89` — `GRPCConfig.ReflectionEnabled` | Reflection 开启/关闭需重启 |
| `internal/config/config.go:98` — `DatabaseConfig.AutoMigrate` | 同上 |
| `internal/app/app.go:185-190` | 检查 `cfg.Outbox.Webhook.URL != ""` 决定是否启用 webhook handler。此检查在启动时执行一次，运行中无法动态注册/注销 webhook |
| `internal/app/app.go:263-276` | Rate limiter 和 GC sweeper 的启停逻辑全部在启动时决定，无运行时 toggle |
| `internal/middleware/ratelimit.go` | 限流中间件在路由建立时注入，无法在运行时动态移除 |

### 用户场景

1. **渐进式功能发布**：新功能（如群聊）先在内部环境开启 → 然后 5% 的生产流量 → 逐步提升到 100%。出问题时一键关闭（kill switch），无需回滚部署。
2. **A/B 测试**：50% 用户看到新 UI 功能（如评论增加 emoji 反应面板），50% 用户保持旧 UI。通过 flag 控制，无需两套前端代码。
3. **紧急熔断**：SSO 出现故障时，运维可以即时开启 `auth_bypass_mode`，让白名单 IP 的请求跳过 SSO 验证（配合 IP allowlist），而不是紧急回滚代码。
4. **运营活动**：特定时段开启"上传双倍配额"，结束后自动关闭，无需部署。

### 架构设计要点

#### 核心模型

```go
// internal/featureflag/store.go

// Flag 定义一个特征开关
type Flag struct {
    Key          string            // "group_chat.enabled", "moderation_ai_assist"
    Description  string
    DefaultValue bool              // 降级值（配置服务不可达时使用）
    Type         FlagType          // BOOL | PERCENTAGE | BUCKET
    Rules        []FlagRule
}

type FlagRule struct {
    // 条件匹配（任意一个满足即应用此 rule 的 value）
    Condition Condition
    Value     bool
}

type Condition struct {
    UIDs        []string  // 指定用户
    UIDPercent  *int      // 0-100 用户哈希百分比
    IPRanges    []string  // 来源 IP
    Environment string    // "production" | "staging"
}
```

#### 存储层

```go
// 方案 A（推荐）：PostgreSQL 表 + TTL 缓存
CREATE TABLE feature_flags (
    key             TEXT PRIMARY KEY,
    value           JSONB NOT NULL,         -- {"enabled": true, "rules": [...]}
    updated_by      TEXT,
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at      TIMESTAMPTZ             -- 具有 TTL 的临时 flag（运营活动）
);

CREATE TABLE feature_flag_audit (
    id              BIGSERIAL PRIMARY KEY,
    flag_key        TEXT NOT NULL,
    old_value       JSONB,
    new_value       JSONB,
    changed_by      TEXT NOT NULL,
    changed_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

#### 集成到请求路径

```go
// internal/middleware/featureflag.go
func FeatureFlag(store *featureflag.Store, key string) gin.HandlerFunc {
    return func(c *gin.Context) {
        if !store.IsEnabled(c.Request.Context(), key, c.GetString("uid"), c.ClientIP()) {
            // flag 关闭 → 返回 404（资源不存在，而非 403，避免信息泄露）
            c.AbortWithStatus(http.StatusNotFound)
            return
        }
        c.Next()
    }
}

// 路由注册示例
authGroup.POST("/v1/channels", middleware.FeatureFlag(ffStore, "group_chat.enabled"), channelHandler.Create)
```

#### 运行时管理端点

```http
GET  /v1/admin/flags                        # 列出所有 flag 及当前值
GET  /v1/admin/flags/:key                   # 查看单个 flag 详情
PUT  /v1/admin/flags/:key                   # 修改 flag 值（立刻生效，不重启）
POST /v1/admin/flags/:key/evaluate          # 模拟某个用户/IP 的 flag 结果（调试用）
GET  /v1/admin/flags/:key/audit             # 变更历史
```

### 现有设施复用

| 现有组件 | 复用方式 |
|---------|---------|
| `admin_roles` + `middleware/permission.go` | flag 管理端点仅 `system.config` 权限可访问 |
| `audit.AuditLogger` | flag 变更写入审计日志（`action: feature_flag.update`）|
| `idempotency.Store` | 批量 flag 变更的幂等性 |
| `storage/gc/sweeper` goroutine 模式 | flag 缓存定期刷新（本地缓存 30s TTL）|
| `middleware.Metrics` | flag 评估结果标记在 span 属性中，可分析 flag 影响面 |

### 边界情况

| Edge Case | 处理 |
|-----------|------|
| Flag 配置服务不可达 | 使用 `DefaultValue`（保守地返回 false 关闭功能），记录告警 |
| 千万级并发场景下的 flag 评估开销 | 本地缓存 + 原子变量，评估开销 < 100ns；完全无网络 I/O（缓存模式） |
| 临时 flag 过期自动清理 | `expires_at` 定时清除 + 审计日志记录"flag X 已过期自动移除" |
| 安全：防止 flag 被恶意修改 | 管理端点要求 `PermSystemConfig`；变更记录到不可变的审计日志 |
| 多实例一致性问题 | 最终一致即可（秒级延迟）。PostgreSQL 表 + 30s 轮询缓存 |

### 成本估算

- `internal/featureflag/` 包（store + cache + middleware）：**低**（3-5 天）
- 管理端点（HTTP + gRPC）：**低**（2-3 天）
- 迁移现有硬编码 `Enabled` 开关（rate limit、GC、tracing、webhook）到 flag：**中**（1 周）
- **总计：2-3 周**

---

## 方向二：多租户 / 组织模型（Multi-Tenancy / Organization Model）

### 为什么它被遗漏

aero-id 的所有数据模型以 `uid` 为粒度。这意味着：

- 一个企业客户有 100 个员工 → 100 个独立的 uid，彼此不可见对方的数据
- 管理员无法管理"公司的文件"——只能逐个管理用户
- 没有团队、项目组、部门的概念
- 存储配额按用户计算而非按组织计算

所有 18 份文档均讨论"如何增强单个用户的能力"，从未讨论"如何支持成组织用户的层级结构"。

### 代码级证据

| 位置 | 发现 |
|------|------|
| `internal/domain/file.go` — `File.OwnerUID` | 文件 owner 是单个 uid，无法表达"组织拥有的文件" |
| `internal/domain/message.go` — `Message.ToUID` | 消息目标是 uid 或群聊，但群聊（未实现）也是 uid 集合而非 org 结构 |
| `internal/domain/comment.go` — `Comment` | 评论附着于目标，目标 owner 是 uid |
| `internal/repository/file.go` — `ListByOwner` | 查询条件是 `owner_uid = ?`，无法按组织聚合 |
| `internal/repository/message.go` — `Inbox` | 收件箱按 uid 查询，无组织级收件箱 |
| `internal/repository/file_share.go` — `ListSharedWithMe` | 分享按 uid 查询，无法按团队分享 |
| `internal/domain/admin.go` — `UserRole` | 已暗示"超越用户"的权限层级，但只用于 admin，未扩展到组织模型 |
| `migrations/000022_admin_roles.up.sql` | 管理角色已存在，但缺乏"角色与组织绑定"的概念 |
| `configs/config.yaml` — 无 org 配置节 | 无任何与组织/租户相关的配置 |

### 用户场景

1. **企业客户 on-boarding**：一个公司注册后，管理员创建 50 个员工账号。员工之间自动可见，共享存储配额。管理员可以设置"销售部可访问 sales 文件夹，开发部可访问 dev 文件夹"。

2. **组织级文件管理**：文件属于组织而非个人。员工离职后，其文件仍保留在组织中。这与当前"文件属于 owner_uid"的模型完全相反——离职后文件随用户消失。

3. **组织级配额与账单**：配额按组织计算（50GB 团队版），而非按用户叠加。超出配额时整个组织受限，而非某个用户受限。当前 `storage_quota` 方向（已覆盖）停留在用户级。

4. **跨组织协作**：A 组织的文件分享给 B 组织的成员——需要组织级的信任关系和审计。

### 架构设计要点

#### 核心模型

```sql
-- 组织
CREATE TABLE organizations (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name            TEXT NOT NULL,
    slug            TEXT UNIQUE NOT NULL,     -- 唯一 URL 标识
    plan            TEXT NOT NULL DEFAULT 'free',  -- 'free' | 'team' | 'enterprise'
    owner_uid       TEXT NOT NULL,            -- 组织创建者
    settings        JSONB DEFAULT '{}',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at      TIMESTAMPTZ
);

-- 组织成员
CREATE TABLE organization_members (
    organization_id UUID NOT NULL REFERENCES organizations(id),
    uid             TEXT NOT NULL,
    role            TEXT NOT NULL DEFAULT 'member',  -- 'owner' | 'admin' | 'member'
    joined_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (organization_id, uid)
);

-- 团队/部门（组织内的子分组）
CREATE TABLE teams (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organizations(id),
    name            TEXT NOT NULL,
    parent_team_id  UUID REFERENCES teams(id),  -- 支持层级
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at      TIMESTAMPTZ
);

CREATE TABLE team_members (
    team_id  UUID NOT NULL REFERENCES teams(id),
    uid      TEXT NOT NULL,
    role     TEXT NOT NULL DEFAULT 'member',
    PRIMARY KEY (team_id, uid)
);
```

#### 资源归属模型

当前 `owner_uid` 需要扩展为多态所有者：

```sql
-- 改造 files 和 messages 表：owner 可以是用户或组织
ALTER TABLE files ADD COLUMN owner_type TEXT NOT NULL DEFAULT 'user';
-- owner_type = 'user' 时 owner_uid = uid
-- owner_type = 'org' 时 owner_uid = organization_id
-- 现有数据全部 owner_type = 'user'，向后兼容
```

#### 集成点

| 层面 | 变更 |
|------|------|
| **Auth middleware** | token 验证后同时解析用户的组织成员信息，注入到 context |
| **Repository** | 所有查询增加 `WHERE owner_uid = ? OR owner_uid IN (SELECT organization_id FROM organization_members WHERE uid = ?)` |
| **Service** | 资源创建/读取权限增加组织级判断："用户是否有权在此组织下创建文件？" |
| **配额** | `storage_quota` 表增加 `owner_type` 列，`used_bytes` 可聚合到组织级 |
| **Admin** | 组织级管理端点：创建/管理/删除组织、添加/移除成员 |
| **分享** | `file_shares` 增加 `share_with_org_id` 列：与整个组织分享文件 |

### 边界情况

| Edge Case | 处理 |
|-----------|------|
| 用户同时属于多个组织 | context 中携带当前活跃组织（可通过请求头 `X-Org-ID` 或动态切换） |
| 用户离开组织后资源共享 | 组织拥有的资源不受影响；用户创建的资源：可配置为保留或转移 |
| 组织删除 | 软删除 + 数据保留期（类似 G Suite 的 30 天恢复窗口）|
| 跨组织搜索 | 默认仅搜索当前组织；可选"搜索所有可见组织" |
| 免费版组织限制 | 免费版成员上限 10 人，企业版不限。通过 `plan` 字段和 `MaxMembers()` 方法动态限制 |

### 成本估算

- Schema：3-4 个新表 + 1 个 ALTER TABLE：**低**
- Repository：查询加组织过滤条件（影响所有 repo）：**高**（涉及 7 个 repo 的 ~30 个查询方法）
- Service：组织 CRUD + 成员管理 + 权限适配：**中高**
- Handler：约 10 个新端点：**中**
- 粒度：**不要一次性全量实施**。MVP：仅 organizations 表 + owner_type 扩展 + 组织级文件隔离。团队、配额、跨组织分享作为后续阶段。
- **总计（MVP）：4-6 周**

---

## 方向三：GraphQL / BFF 网关层（GraphQL / Backend For Frontend Gateway）

### 为什么它被遗漏

现有文档讨论的是"后端能力扩展"，从未从**客户端消费体验**的角度审视 API 设计。当前 HTTP REST + gRPC 双传输对客户端开发者造成了不必要的负担：

- 获取"文件列表 + 每个文件的 owner 信息"需要 2 次 HTTP 请求（`GET /v1/files` + 遍历调用 `GET /v1/users`，但后者不存在）
- 获取"消息 + 发送方头像 + 附件预览"需要 3 次请求
- 移动端需要的数据量远小于 Web 端（移动端只需要文件名和缩略图 URL，Web 端需要全部元数据）

### 代码级证据

| 位置 | 发现 |
|------|------|
| `internal/handler/http/file.go` — `ListFiles` handler | 返回 `[]*domain.File`，但 `domain.File` 不包含 owner 的用户信息（昵称、头像）。调用方必须再调 `GET /v1/users`（实际不存在此端点——用户信息只能通过 SSO 获得）|
| `internal/handler/http/message.go` — `Inbox` handler | 返回 `domain.Message`，不含发送方 `User` 信息。但 `service.MessageList` 包含了 `Users map[string]sso.User`——**服务端已解析了用户信息但 handler 不返回给客户端！** |
| `internal/search/` — `SearchHit` | 跨实体搜索返回 union 类型 `(Message \| Comment \| File)`——这正是 GraphQL 的 `union`/`interface` 类型的天然场景 |
| `internal/handler/grpc/aeropb/aero_id.proto` | gRPC 响应消息中包含大量的 `User` 对象（`MessageList.Users`），但 HTTP 返回的 JSON 中有时省略了用户信息——传输协议间存在不一致 |
| `internal/service/service.go:90` — `MessageList.Users` | 服务层已解析了所有相关用户信息，但 HTTP handler 的 `List` 方法只返回了 `domain.Message`，丢弃了 `Users` 映射 |

### 用户场景

1. **移动端差异化的数据需求**：移动端列表只需要 `id`、`name`、`thumbnail_url`、`owner.display_name`，Web 端需要全部字段。REST API 被迫返回最大集合，导致移动端带宽浪费 3-5 倍。

2. **减少客户端请求数**：一个"文件详情页"可能需要 4 次 API 调用（文件信息 + owner 用户详情 + 文件上的评论 + 评论作者信息）。GraphQL 可以用一次查询获取全部数据。

3. **客户端驱动的 API 演进**：新增字段不需要后端发版——客户端声明需要的字段，后端自动响应。新增字段对旧客户端无影响（无破坏性变更）。

4. **版本化消失**：不再需要 `/v1/` → `/v2/` 升级。每次 field 的添加/废弃通过 schema 变更管理，客户端按需查询。

### 架构设计要点

#### 拓扑

```
                      ┌──────────────────┐
                      │   GraphQL Gateway │
                      │   (internal/gql/) │
                      │                   │
  Mobile ────────────►│  Resolvers → HTTP │────► 现有 REST API
  Web   ────────────►│    OR             │────► 现有 gRPC API
                      │  DataLoader (N+1)│
                      └──────────────────┘
```

**两种实现路径**：

**路径 A（推荐）**：独立 GraphQL 层，`gqlgen` 或 `graphql-go` 框架，在现有 handler/service 之上封装 resolver。使用 DataLoader 模式解决 N+1 查询问题。不修改现有 API。

**路径 B（轻量）**：BFF 模式——新增 `internal/bff/` 包，提供面向特定客户端（Mobile / Web）的聚合端点。不是 GraphQL，而是定制 REST 端点（如 `GET /v1/mobile/home-feed`），由 BFF 层编排后端 API 调用。

路径 A 适合长期（未来所有新客户端）；路径 B 适合短期（快速改善移动端体验）。**建议走 B 作为 MVP**，然后演进到 A。

#### 核心 GraphQL Schema 示意

```graphql
type Query {
    files(owner_uid: String, cursor: String, limit: Int): FileConnection!
    messages(type: MessageFilter, cursor: String, limit: Int): MessageConnection!
    search(query: String!, kind: SearchKind): [SearchHit!]!
    activity(cursor: String, limit: Int): [Activity!]!
    me: User!
}

type File {
    id: ID!
    name: String!
    size: Int!
    mimeType: String!
    visibility: Visibility!
    owner: User!              # 自动解析 owner_uid → User
    thumbnail(size: ThumbSize): String  # 按需生成
    comments: [Comment!]!     # 自动解析文件的评论
    sharedWith: [User!]!      # 自动解析 file_shares
    createdAt: DateTime!
}

type Message {
    id: ID!
    content: String!
    from: User!              # 自动 from_uid → User
    to: User!                # 自动 to_uid → User
    reactions: [Reaction!]!  # 自动查询 message_reactions
    quotedMessage: Message   # 自动解析 meta.quoted_message
    createdAt: DateTime!
}

union SearchHit = Message | Comment | File
```

#### DataLoader 模式

```go
// gql/dataloader.go
// DataLoader 批量加载 + 缓存相同 key 的重复请求
type UserLoader struct {
    batchFn func(ctx context.Context, keys []string) ([]*User, []error)
    cache   map[string]*User
}

// 批量加载：一次 gRPC 调用获取所有 uid 的用户信息
func (l *UserLoader) Load(ctx context.Context, uid string) (*User, error) {
    // 收集所有同批次 Load 调用的 uid → 一次性调用 ssoClient.GetUsers
    // DataLoader 的标准实现（github.com/graph-gophers/dataloader）
}
```

DataLoader 解决 N+1 问题：如果列表返回 20 条消息，每条消息需要解析 `from_uid` → `User`，DataLoader 将 20 次独立请求合并为 1 次 `GetUsers([]string{...})` gRPC 调用。**现有 `ssoClient.GetUsers` 的批量能力被充分利用。**

### 现有设施复用

| 现有组件 | 复用方式 |
|---------|---------|
| `ssoClient.GetUsers` | DataLoader 批量加载用户信息的后端 |
| `service.FileService` / `MessageService` / `CommentService` | Resolver 调用现有 service 接口 |
| `domain.Page` + cursor 分页 | GraphQL 的 connection 模式（`FileConnection { edges, pageInfo }`）|
| `UserDirectory` | 已在所有 service 中注入的 SSO 查询接口 |
| `enrichUsers` | Resolver 中替代为 DataLoader |
| `metrics.Registry` | GraphQL 查询维度的 RED 指标 |

### 边界情况

| Edge Case | 处理 |
|-----------|------|
| N+1 查询（列表 20 条消息 → 20 次 GetUsers）| DataLoader 自动合并为 1 次批量查询 |
| 深度嵌套（文件 → 评论 → 评论作者）| DataLoader 在每一层独立生效 |
| 查询成本控制（恶意客户端查询 `allFiles { allComments { allReplies } }`）| Query complexity 限制 + depth limit（默认深度 ≤ 5）|
| 权限模型一致性 | GraphQL resolver 复用现有 service 层的权限检查，不重复实现 |
| gRPC vs GraphQL 共存 | 互不冲突：内部服务调用继续使用 gRPC；外部客户端通过 GraphQL 接入 |

### 成本估算

- 引入 `gqlgen` 框架 + schema 定义（核心 5-8 个 type）：**低**（3 天）
- 实现 DataLoader（UserLoader + FileLoader + CommentLoader）：**中**（1 周）
- Resolver 实现（调用现有 service 接口）：**中**（1 周）
- 与现有 HTTP 路由共存（`/graphql` 路径 + 现有 `/v1/*` 路径）: **低**（1 天）
- 移动端适配（从 REST 迁移到 GraphQL 查询）：**取决于客户端**
- **总计（后端）：2-3 周**

---

## 方向四：凭证生命周期管理（Credential Lifecycle Management）

### 为什么它被遗漏

安全类文档（`SECURITY_OPS`、`final-blindspots.md`）讨论了传输加密、静态加密、证书固定等，但**从未系统性地讨论凭证的整个生命周期**：生成、分发、存储、轮换、吊销、审计。aero-id 当前依赖多个敏感凭证，但没有一个统一的凭证管理策略。

### 代码级证据

| 位置 | 发现的凭证 | 当前管理方式 | 问题 |
|------|-----------|------------|------|
| `configs/config.yaml` — `signing_key` | 文件下载 HMAC 签名密钥 | 环境变量 `AERO_ID_SIGNING_KEY` | 无法轮换（旧签名 URL 在新密钥下全部失效）|
| `configs/config.yaml` — `sso.tls_cert_file/key_file` | SSO mTLS 客户端证书 | 文件路径配置 | 证书过期前无自动告警；过期后需停机更换 |
| `configs/config.yaml` — `s3.access_key/secret_key` | S3 API 凭证 | 环境变量 | 无轮换机制；泄露后需手动更新所有实例 |
| `configs/config.yaml` — `outbox.webhook.signing_key` | Webhook HMAC 签名密钥 | 配置文件 | 同 signing_key：轮换导致旧签名验证失败 |
| `internal/storage/local.go` — `SigningKey` | 下载 URL 签名密钥 | 内存字符串 | 启动时加载后不变；运维无法在不重启的情况下更换 |
| `internal/sso/client.go` — `dialOptions` | gRPC TLS 证书 | 启动时一次加载 | 证书文件被更新后，gRPC 连接不重新加载（需重启）|
| `internal/middleware/auth.go` — `validator` | SSO token 验证 | 运行时调用 SSO | 这里没问题——因为验证委托给 SSO，但 aero-id 自身没有任何"API 密钥"级别的凭证轮换 |
| `migrations/000022_admin_roles.up.sql` | 管理角色 | 数据表 | 角色本身不是凭证，但暗示需要"操作凭证"（管理员执行敏感操作时二次验证）|

### 用户场景

1. **签名密钥轮换**：运营团队需要每 90 天轮换一次 `signing_key`。新密钥用于新生成的下载 URL，旧密钥在宽限期（如 24h）内继续有效——宽限期过后所有旧 URL 失效。当前不支持双密钥宽限期。

2. **S3 凭证轮换**：AWS IAM 最佳实践要求每 90 天轮换访问密钥。轮换时需要新密钥先就绪，旧密钥再回收——零停机。当前 S3 客户端在启动时加载一次凭证，轮换需要重启所有实例。

3. **TLS 证书到期前告警**：SSO mTLS 证书即将到期时自动发出告警（"证书 X 将于 14 天后到期"）。当前完全靠人工日历提醒。

4. **泄露应急响应**：发现 signing_key 泄露后，运维可以立即吊销泄露的密钥并生成新密钥，同时保留一个"已吊销密钥列表"用于拒绝旧签名的请求。当前做不到——只能全面更换密钥导致所有已分发的下载 URL 失效。

### 架构设计要点

#### 1. 密钥轮换基础设施

```go
// internal/credential/manager.go

// ManagedSecret 表示一个可轮换的密钥
type ManagedSecret struct {
    ID            string    // "signing_key_v1"
    Current       []byte    // 当前使用的密钥
    Previous      []byte    // 宽限期内的前一版本密钥（可为 nil）
    ActivatedAt   time.Time // 当前密钥激活时间
    RotationCount int
}

// SecretStore 存储密钥版本
type SecretStore interface {
    // 初始化时加载最新密钥
    Load(ctx context.Context) (*ManagedSecret, error)
    // 轮换：生成新密钥，保留旧密钥为 Previous
    Rotate(ctx context.Context) (*ManagedSecret, error)
    // 吊销：指定 ID 的密钥加入吊销列表
    Revoke(ctx context.Context, id string) error
    // 检查密钥是否在上次的 n 个版本内有效（用于宽限期验证）
    IsValid(ctx context.Context, id string) bool
}
```

#### 2. 存储层

```sql
-- 密钥版本注册表
CREATE TABLE secret_versions (
    id              TEXT PRIMARY KEY,           -- "signing_key_v1"
    secret_type     TEXT NOT NULL,              -- "signing_key" | "s3_access_key" | "webhook_key"
    current_hash    TEXT NOT NULL,              -- SHA256(current_secret) — 用于查找和验证
    previous_hash   TEXT,                       -- SHA256(previous_secret)
    current_hint    TEXT NOT NULL,              -- 前 8 位明文 + 哈希后缀（用于人工识别）
    activated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    rotated_at      TIMESTAMPTZ,
    revoked_at      TIMESTAMPTZ,                -- NULL = 活跃
    expires_at      TIMESTAMPTZ,                -- 证书类凭证的过期时间
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 实际的密钥 material 不存储在 DB 中，而是通过外部系统注入：
-- 方案 A：环境变量（当前方式，但支持多版本：SECRET_V1, SECRET_V2）
-- 方案 B：Vault/KMS — 通过 API 获取密钥 + 自动轮换
-- 方案 C：Kubernetes Secret — 挂载为文件，Watch 变更
```

#### 3. 集成：签名密钥的双密钥验证

```go
// 改造 internal/pkg/httpx/httpx.go 中的签名验证
func VerifySignature(signature string, data []byte) error {
    // 1. 用当前密钥验证
    if hmac.Equal(computeHMAC(currentKey, data), signature) {
        return nil
    }
    // 2. 如果当前密钥不匹配，用前一版本密钥验证（宽限期）
    if previousKey != nil {
        if hmac.Equal(computeHMAC(previousKey, data), signature) {
            return nil  // 旧密钥仍然有效（宽限期）
        }
    }
    // 3. 检查该密钥是否已被吊销
    if revokedKeys.Contains(signatureKeyPrefix(signature)) {
        return ErrRevoked
    }
    return ErrInvalidSignature
}
```

#### 4. 配置格式支持多版本

```yaml
# config.yaml — 支持多版本密钥
signing_key:
  current: "${AERO_ID_SIGNING_KEY_V1}"    # 当前活跃
  previous: "${AERO_ID_SIGNING_KEY_V0}"   # 宽限期兼容（可选）
  rotation_interval: 90d                  # 自动轮换间隔（需配合 SecretStore）
  grace_period: 24h                       # 新旧密钥共存时间
```

### 边界情况

| Edge Case | 处理 |
|-----------|------|
| 轮换期间旧密钥签名的 URL 仍然有效 | `grace_period` 控制旧密钥的有效期。期间生成的新 URL 使用新密钥。超时后旧密钥吊销。 |
| 所有密钥同时泄露（最坏情况）| 紧急模式：吊销所有版本，所有已分发的 URL 立即失效。应用返回 401/403。 |
| 自动轮换失败（SecretStore 不可达）| 保留当前密钥继续使用，告警。不主动轮换——安全原则：宁可不过期也不使用单密钥。 |
| 证书过期检测 | 启动时检查所有 TLS 证书的 `NotAfter`，`expires_at` 到期前 14 天开始每日告警。 |
| 多实例轮换时序问题 | 实例 A 先轮换，实例 B 后轮换。宽限期内两个密钥都有效，所以 A 签名的 URL B 也能验证。最终一致性。 |

### 成本估算

- `internal/credential/` 包（Manager + Store + 轮换逻辑）：**中**（1 周）
- 签名密钥双密钥验证改造（local storage + webhook）：**低**（2-3 天）
- **不要做**（当前阶段）：自动轮换 cron job。先做**人工轮换管理 API**，自动轮换后续再启。
- 证书到期监控（Prometheus 指标 + 告警规则）：**低**（1 天）
- **总计（MVP）：1-2 周**

---

## 方向五：Webhook 死信控制台与事件重放（Dead Letter Console & Event Replay）

### 为什么它被遗漏

outbox 系统核心架构完善：事务性写入 → relay → handler chain → 重试 → 死信。但**死信后的事件完全不可见**：

- 运维无法知晓有多少事件被死信（只能通过 `dead_letter=true` 的计数 gauge 知道总数，但无法知道是哪些事件、为什么失败）
- 运维无法重新投递死信事件（标记为 `dead_letter=false` + `published_at=NULL` 需要手动 SQL）
- 没有针对特定订阅者（webhook endpoint）的投递统计——如果某个 webhook endpoint 持续 500，只能通过 endpoint 自己的日志发现

所有 18 份文档中，仅在 `edge-cases-and-performance.md` 的"Webhook 方向"中一句话提到了"死信队列查看、重新投递、或导出功能"是缺失的，但从未将其作为一个独立方向来设计。

### 代码级证据

| 位置 | 发现 |
|------|------|
| `internal/outbox/repository.go` — `MarkDeadLetter` | 将事件标记为死信。这是唯一的死信操作——**无查询、无恢复、无导出** |
| `internal/outbox/relay.go` — `Run` | relay 循环 `WHERE published_at IS NULL AND dead_letter = false`——死信事件被永久跳过 |
| `internal/outbox/relay.go` — `Handle` | 当 `err != nil` 且重试耗尽后调用 `MarkDeadLetter`。错误信息仅在日志中（zap.Error），不持久化 |
| `internal/outbox/webhook.go` — `webhookHandler.Handle` | webhook 调用失败 → 返回 error → relay 重试 → 最终死信。但具体失败原因（timeout / 500 / DNS 解析失败）只留在日志中，不在事件行中 |
| `internal/outbox/outbox.go` — `Event` | Event 结构体无 `LastError`、`LastAttemptAt` 字段——失败信息未持久化 |
| `migrations/000018_outbox_retry.up.sql` | 有 `retry_count`、`last_retry_at` 列，但无 `last_error` 列 |
| `internal/service/notification.go` — 无死信相关代码 | NotificationDispatcher 不处理死信——它的失败来自 outbox relay 层重试和死信逻辑 |

### 用户场景

1. **死信事件查询**："过去 24 小时有哪些 webhook 事件被死信了？"——运维需要了解投递失败的全貌。
2. **单事件重试**：运维修复 webhook endpoint 的问题后，手动选择"重新投递"该事件——不需要等到下一个轮询周期。
3. **批量重放**："所有类型为 `message.sent` 且 delivery 失败的 webhook 事件重新投递。"——用于 webhook endpoint 升级后的批量回补。
4. **死信趋势告警**："过去 5 分钟死信率 > 10% → PagerDuty 告警。"——及时发现问题 endpoint。
5. **投递日志导出**："将过去 7 天的 webhook 投递日志导出为 CSV。"——用于合规审计或 SLA 报告。

### 架构设计要点

#### 1. 死信管理端点

```http
### 列表
GET /v1/admin/outbox/dead-letters?type=webhook&since=2026-07-01&cursor=...&limit=20
→ {
    "events": [
      {
        "id": "uuid",
        "event_type": "file.uploaded",
        "aggregate_type": "file",
        "aggregate_id": "uuid",
        "payload_summary": {"file_id": "xxx", "filename": "report.pdf"},
        "retry_count": 20,
        "last_error": "POST https://hooks.example.com/callback: 500 Internal Server Error",
        "last_attempt_at": "2026-07-10T10:30:00Z",
        "created_at": "2026-07-10T10:00:00Z"
      },
      ...
    ],
    "total": 142,
    "next_cursor": "..."
  }

### 单事件重试
POST /v1/admin/outbox/dead-letters/:id/replay
→ { "status": "queued", "estimated_delay": "5s" }

### 批量重放
POST /v1/admin/outbox/dead-letters/batch-replay
Content-Type: application/json
{
  "filters": {
    "event_types": ["message.sent", "message.read_receipt"],
    "created_before": "2026-07-10T00:00:00Z"
  },
  "reason": "webhook endpoint upgrade complete"
}
→ { "matched": 42, "queued": 42 }

### 投递日志
GET /v1/admin/outbox/delivery-logs?event_id=xxx
→ {
    "attempts": [
      {"at": "...", "status_code": 500, "latency_ms": 2300, "error": "timeout"},
      {"at": "...", "status_code": 500, "latency_ms": 1200, "error": "upstream 500"},
      ...
    ]
  }
```

#### 2. Schema 扩展

```sql
-- 100018_outbox_observability.up.sql
ALTER TABLE outbox_events ADD COLUMN last_error TEXT;
ALTER TABLE outbox_events ADD COLUMN last_status_code INT;  -- HTTP 状态码（webhook 特有）

-- 投递尝试日志（独立表，避免 outbox_events 行过大）
CREATE TABLE outbox_delivery_attempts (
    id              BIGSERIAL PRIMARY KEY,
    event_id        UUID NOT NULL REFERENCES outbox_events(id) ON DELETE CASCADE,
    attempt         INT NOT NULL,
    status_code     INT,                     -- HTTP 状态码 / gRPC 状态码
    error_message   TEXT,
    latency_ms      INT,                     -- 投递耗时
    attempted_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_oda_event ON outbox_delivery_attempts(event_id, attempt);
```

#### 3. 死信告警规则

```yaml
# configs/config.yaml — 新增
outbox:
  alerts:
    dead_letter_rate_threshold: 0.1       # 死信率 > 10% 触发告警
    dead_letter_rate_window: 5m           # 窗口 5 分钟
    min_dead_letter_count: 10             # 至少 10 条死信才触发（防抖动）
    notification_channels:                # 告警通知目标
      - type: "pagerduty"
        routing_key: "..."
      - type: "slack_webhook"
        url: "https://hooks.slack.com/..."
```

#### 4. 重放语义

重新投递死信事件时，需要决定：

| 决策 | 默认 | 可选 |
|------|------|------|
| 是否重置 `retry_count` | 是（清零） | 否（保持重试预算） |
| 是否更新 `created_at` | 否（保留原始时间戳） | 是（按重放时间重新排序） |
| 通知消费者"这是重放" | 是（meta 增加 `replayed_at` 标记） | 否 |
| 是否跳过去重检查 | 否（标准重试逻辑） | 是（强制投递） |

### 现有设施复用

| 现有组件 | 如何使用 |
|---------|---------|
| `admin_roles.PermSystemConfig` | 死信管理端点需要 `system.config` 权限 |
| `metrics.Registry` | 增加 `outbox_dead_letter_total{event_type, reason}` 指标 + 告警 |
| `audit.AuditLogger` | 重放操作记录为 `audit.outbox.replay` |
| `idempotency.Store` | 批量重放请求的幂等性保护 |
| 现有的 Prometheus `outbox_pending_events` gauge | 补充 `outbox_dead_letter_total` counter + `outbox_delivery_latency` histogram |

### 边界情况

| Edge Case | 处理 |
|-----------|------|
| 重放的事件再次失败 | 重新进入重试 → 死信循环。防止无限循环：重放的事件使用新的 `retry_count`（从 0 开始）|
| 运维误操作批量重放了 10 万条死信 | API 设置单次批量重放上限（默认 500）；"确认"对话框要求输入 `CONFIRM` |
| 死信事件的 payload 引用了已被删除的资源 | 消费者（NotificationDispatcher）需正确处理——如果文件已被删除，跳过通知创建，标记为"已处理"而非再次死信 |
| 大量死信导致 delivery_attempts 表膨胀 | 保留期默认 30 天；周期性清理；可选抽样（只保留失败尝试，成功的不记录）|
| webhook endpoint 在宽限期内恢复 | 死信不再自动重试（目前已经是），但控制台提供"恢复后重试全部"的批量操作 |

### 成本估算

- Schema 扩展（2 张表 + ALTER）：**低**（1 天）
- Repository：死信查询 + 重放 + delivery attempt 记录：**中**（1 周）
- Handler：4-5 个管理端点：**低-中**（3-5 天）
- Relay 改造：记录 `last_error` + 写 delivery_attempts：**低**（2 天）
- 告警规则配置 + PrometheusRule：**低**（1 天）
- **总计：2-3 周**

---

## 方向优先级与依赖关系

| 方向 | 用户/运维价值 | 架构杠杆 | 实现成本 | 依赖前置 | 建议优先级 |
|------|:-----------:|:-------:|:-------:|---------|:---------:|
| **1. 特征开关** | 🔥🔥🔥🔥🔥 | 高（赋能所有未来功能发布） | 低-中 | 无 | **P1** |
| **2. 多租户** | 🔥🔥🔥🔥🔥 | 高（B2B 市场准入条件） | 高 | 无（可渐进实施） | **P2** |
| **3. GraphQL/BFF** | 🔥🔥🔥🔥 | 中（改善客户端体验） | 中 | 无 | **P2** |
| **4. 凭证生命周期** | 🔥🔥🔥🔥 | 中（安全运营基线） | 低-中 | 无 | **P1** |
| **5. 死信控制台** | 🔥🔥🔥 | 中（运维效率） | 中 | 无 | **P2** |

### 执行建议

**Phase 0（下两个 Sprint）**：

| 任务 | 方向 | 预估 | 理由 |
|------|------|------|------|
| 特征开关存储 + 中间件 | 1 | 1 周 | 所有未来新功能的上线安全门。群聊（已规划方向）上线前必须完成 |
| signing_key 双密钥验证 | 4 | 2 天 | 影响所有已分发的下载 URL。泄露即全部失效——这是生产安全事故 |
| dead_letter 查询端点（只读） | 5 | 3 天 | 最少可行：让运维能看见死信。后端逻辑已就绪，缺的是 API 暴露 |

**Phase 1（3-4 周）**：

| 任务 | 方向 | 预估 | 理由 |
|------|------|------|------|
| 迁移现有 Enabled 开关到 feature flag | 1 | 1 周 | rate_limit、tracing、GC 的运行时可控 |
| 死信重放 API + delivery_attempts 表 | 5 | 1 周 | 看见死信 + 能修复 = 闭环 |
| 凭证管理 API + 自动到期告警 | 4 | 1 周 | TLS 证书过期是 SRE 手册的第一章 |
| GraphQL schema + DataLoader（MVP） | 3 | 2-3 周 | 移动端 or Web 端新功能开发前完成 |

**Phase 2（5-8 周）**：

| 任务 | 方向 | 预估 | 理由 |
|------|------|------|------|
| 多租户 MVP（organization 表 + 文件隔离） | 2 | 4-6 周 | 企业客户 onboarding 的前提 |
| GraphQL 生产就绪（depth limit + complexty + auth） | 3 | 1 周 | 安全加固 |
| 完整的死信控制台（批量重放 + 趋势图 + 导出） | 5 | 2 周 | 运维平台化 |

---

## 附录：与已有 18 份文档的对比确认

| 文档 | 方向 1: 特征开关 | 方向 2: 多租户 | 方向 3: GraphQL/BFF | 方向 4: 凭证管理 | 方向 5: 死信控制台 |
|------|:--------------:|:------------:|:----------------:|:--------------:|:---------------:|
| `expansion-directions.md` | ❌ | ❌ | ❌ | ❌ | ❌ |
| `architecture-extensions.md` | ❌ | ❌ | ❌ | ❌ | ❌ |
| `edge-cases-and-performance.md` | ❌ | ❌ | ❌ | ❌ | ✅单句 |
| `product-directions.md` | ❌ | ❌ | ❌ | ❌ | ❌ |
| `genuinely-uncovered.md` | ❌ | ❌ | ❌ | ❌ | ❌ |
| `remaining-high-value.md` | ❌ | ❌ | ❌ | ❌ | ❌ |
| `untapped-directions.md` | ❌ | ❌ | ❌ | ❌ | ❌ |
| `deep-extension-analysis.md` | ❌ | ❌ | ❌ | ❌ | ❌ |
| `final-blindspots.md` | ❌ | ❌ | ❌ | ✅半句 | ❌ |
| `arch-migration-gaps.md` | ❌ | ❌ | ❌ | ❌ | ❌ |
| 8 份 REVIEW_*.md | ❌ | ❌ | ❌ | ❌ | ❌ |

> ✅单句 = 该文档在某一方向描述中提及此概念但未作为独立方向分析
> ✅半句 = 仅在附录或示例中作为次要话题提及

**结论**：本文 5 个方向在所有既有文档中覆盖度为零或接近零，确认为独立的增量分析成果。

---

**本文件基于全局代码扫描 + 18 份既有文档交叉分析，无代码变更。建议作为下一轮架构评审和 Sprint 规划的输入。**
