现在我已拥有对代码库全景的深度理解，可以撰写完整的架构分析了。

---

# 架构审查：通知生命周期与领域模型缺口

> **日期**: 2026-07-12  
> **视角**: 资深架构师  
> **基准代码**: `internal/` 全量（45+ Go 包）+ 23 份迁移 SQL + api/proto  
> **参考文献**: `docs/specs/aero-id-design.md` · `docs/ARCHITECTURE.md` · `docs/REVIEW_*`  
> **输入文档**: `docs/requirements/2026-07-10-five-genuine-notification-lifecycle-and-domain-gaps.md`

---

## 1. 架构评估

### 1.1 核心架构质量（对照 AGENTS.md 约束）

| 维度 | 评分 | 证据 |
|------|------|------|
| 分层 | ⭐⭐⭐⭐⭐ | `handler → service → repository + storage/sso`，严格单向依赖 |
| 领域模型完整性 | ⭐⭐⭐ | 文件、消息、评论模型完善；通知类型枚举仅 3 个常量，明显不足 |
| 错误处理 | ⭐⭐⭐⭐⭐ | `*AppError` 统一跨传输；仓库层 raw → service 层 Wrap → handler 渲染 |
| 正确性保障 | ⭐⭐⭐⭐ | 事务性 outbox、幂等键、内容去重校验和、参考计数、通知折叠 |
| 可测试性 | ⭐⭐⭐⭐ | 27 个 pkg 具备单元测试 + testcontainers 集成套件 |
| 工程约束 | ⭐⭐⭐⭐⭐ | AGENTS.md 规定的 500 行/文件、50 行/函数、复杂度 ≤10，全部达标 |

### 1.2 通知子系统的架构成熟度

通知子系统经过明显迭代（从设计文档中的基础 outbox → 当前复杂架构）：

```
Outbox Relay (5s ticker)
  → FetchUnpublished events
  → NotificationDispatcher.Handle(each event)
    → preference check (per-type enablement + quiet hours)
    → rate limiting (per-(recipient, type) per-minute sliding window)
    → block suppression (directional)
    → collapse detection (within 5-min window, unread, same actor+type)
    → notification row CREATE or collapsed_count INCREMENT
    → materialized unread counter update
    → SSE push (via Hub → subscriber channels)
  → MarkPublished
```

**已实现的能力**（超出设计文档原 MVP 范围）：

| 能力 | 代码位置 | 状态 |
|------|---------|------|
| 偏好检查 | `internal/service/preference.go:prefChecker` | ✅ 已完成 |
| 速率限制 | `internal/service/notification.go:notifRateLimiter` | ✅ 已完成 |
| 通知折叠（collapsed_count） | `internal/service/notification.go` + migration 000020 | ✅ 已完成 |
| 存储引用计数 | migration 000021 + `CountByStorageKey` | ✅ 已完成 |
| 管理员角色 | migration 000022 (+ `roles` / `user_roles` 表) | ✅ 已完成 |
| 物化未读计数器 | migration 000023 + `UnreadCounter` | ✅ 已完成 |
| SSE last_event_id 回填 | `ListSince` in `repository/notification.go` | ✅ 已完成 |
| gRPC 健康服务 + reflection | `ARCHITECTURE.md` | ✅ 已完成 |

### 1.3 文档分析的准确度——事实核查

输入文档列举了 5 个方向，逐条核对现状：

| # | 文档方向 | 文档结论 | 实际代码状态 | 准确度 |
|---|---------|---------|-------------|--------|
| 1 | 顶级评论不触发通知 | ❌ 缺口存在 | ✅ 确认：`parent == nil` 时 `evt` 为 nil | ✅ 准确 |
| 2 | Reactions/Likes 不触发通知 | ❌ 缺口存在 | ✅ 确认：`ReactToMessage`/`react` 均无 outbox 事件 | ✅ 准确 |
| 3 | 消息软删除不通知对方 | ❌ 缺口存在 | ✅ 确认：`Delete` 只写审计日志，不产生 outbox 事件 | ✅ 准确 |
| 4 | 文件类型无层级分类 | ❌ 缺口存在 | ✅ 确认：`File` 模型有 `MimeType` 但无 `Category`/`Kind` | ✅ 准确 |
| 5 | 偏好粒度太粗 | ❌ 缺口存在 | 部分纠正：偏好模型确实无资源维度，但文档未提及*已有*速率限制和折叠功能 | ⚠️ 部分准确 |

**关键纠正**：文档未注意到通知子系统已具备速率限制、折叠、物化计数器和偏好检查等基础设施。这使得方向 1-3 的实际实现成本比文档估算的 **更低**——新事件类型直接接入现有管道即可，无需新建基础设施。

### 1.4 架构债务识别

**债务 A — 通知事件类型枚举散落（中严重度）**

`domain.NotificationType` 定义了 3 个常量，但 `notificationFor()` 的 switch 处理 3 个 `EventType`，且 `PreferenceService` 也硬编码了 `notification_type` 字符串。没有一个**统一的注册表**定义哪些事件类型是有效的、各事件对应什么通知类型、以及各自需要的 payload 结构。新增一个事件类型需要修改 3 个位置（生产者、dispatcher switch、偏好检查）。

**债务 B — comment.service 使用文件级 `comment` 包而非领域层 outbox 接口（低严重度）**

`internal/comment/service.go` 直接导入 `"github.com/aero/aero-id/internal/outbox"` 并调用 `outbox.NewEvent(...)`。按照架构原则，service 层应依赖接口而非具体包。虽然在单体架构中尚可接受，但它意味着通知事件的生产逻辑散落在各 service 实现中，而非集中在某个事件发布接口后。

**债务 C — `File.Delete()` 的引用计数竞态（已修复但需验证）**

设计文档的 V1 犯了一个经典并发错误：`Delete()` 调用 `store.Delete()` 之前做了 `CountByStorageKey` 检查，但在检查与删除之间**存在竞态窗口**。migration 000021（`storage_refs` 表）旨在修复此问题，但当前 `file/service_crud.go:Delete()` 仍使用 `CountByStorageKey` 而非 `storage_refs` 表。这是一个**半完成的重构**——引用计数表已创建，但文件 service 尚未切换使用它。

**债务 D — Hub channel buffer 容量无保护（低严重度）**

`hub.go:Subscribe()` 的 channel buffer 硬编码为 16。在 burst 场景中（如同时发送 100 条消息触发 100 条通知），慢消费者会静默丢失事件。当前无背压机制或日志记录。

---

## 2. 扩展方向

基于文档识别的 5 个缺口，加上我的代码库分析，我提出 5 个高价值架构扩展方向，**按战略价值排序**而非文档按成本排列：

### 方向 A：通知事件类型注册表（P0 — 使能器）

当前所有方向 1-3 的缺口根因相同：新增事件类型的方式过于手动化。在开始补充缺失事件类型之前，先建立注册表基础设施。

**为什么需要**：
- 消除每次新增事件类型时在 3+ 处修改代码的摩擦
- 为审计/监控/文档生成提供单一来源
- 新开发人员能一目了然地了解系统支持的通知类型

**核心挑战**：
- Go 没有原生枚举——需要使用类型安全的模式（字符串常量 + 注册表映射）
- 与现有代码的向后兼容：现有 3 种类型必须继续工作

**建议方案**（2 选 1）：

| 方案 | 做法 | 复杂度 | 灵活性 |
|------|------|--------|--------|
| **A1 — 声明式注册表** | 在 `domain/notification.go` 中定义 `var EventTypeRegistry = map[string]NotificationTypeDef{...}`，每个定义包含事件类型、通知类型、必需 payload 键、是否自操作免通知 | 低 | 中 |
| **A2 — 注册表 + 代码生成** | 从 YAML/Proto 定义生成注册表和 payload 结构体 | 中 | 高 |

**推荐**：A1 方式 - 一个中心化映射表，为每个 `EventType` 提供 `NotificationTypeDef`（目标通知类型、生成的 payload 类型引用、是否是自操作）。成本 ~1 天，即可将方向 1-3 的实现时间从每项 2 天降至 ~半天。

**对现有系统的影响**：无。现有 `notificationFor()` switch 可以保留为 fallback，新事件逐步迁移到注册表。

### 方向 B：资源级偏好作用域（P1 — 高业务价值）

文档的方向 5，但上下文已改变——偏好框架本身已存在（`PreferenceService` + `PreferenceChecker`），需要扩展而非新建。

**为什么需要**：
- 用户反馈中排名靠前的功能（能静音特定文件/对话）
- 直接降低通知噪音，提升用户留存
- 为高级功能铺路（"关注"文件、对话优先级）

**核心挑战**：
- Schema 变更：`notification_preferences` 的 `(uid, notification_type)` 主键必须改为允许 `(uid, notification_type, scope_type, scope_id)`，其中 `scope_type`/`scope_id` 可以为空表示全局
- `PreferenceChecker` 接口签名需要扩展到 `IsAllowed(ctx, recipientUID, notificationType, scopeType, scopeID string)`
- 查询性能：资源级偏好的每次检查需要从 DB 读取（无法像全局偏好那样全量缓存到内存）
- 清理：当资源被删除时，相关偏好行必须级联清理

**预期架构变更**：

```
// 当前
type PreferenceChecker interface {
    IsAllowed(ctx, recipientUID, notificationType string) (bool, error)
}

// 未来
type PreferenceChecker interface {
    IsAllowed(ctx, recipientUID, notificationType string, scopes ...ScopeRef) (bool, error)
}

type ScopeRef struct {
    Type string // "file" | "conversation" | "" (global)
    ID   string
}
```

**对现有系统的影响**：
- `PreferenceService.Set()` 需要支持带有作用域参数的偏好
- 通知 `Handle()` 需要在构造通知时提供作用域上下文
- API 变更：`POST /v1/notification-preferences` 需要可选 `scope_type` + `scope_id` 字段

**实现策略**：增量式——先扩展 schema（`scope_type TEXT DEFAULT ''` + `scope_id TEXT DEFAULT ''` + 新主键为 `(uid, notification_type, scope_type, scope_id)`），然后扩展 checker 接口，最后扩展 dispatcher 调用点。向后兼容：scope 为空 = global = 现有行为。

### 方向 C：文件类型分类体系（P1 — 数据平面能力）

文档的方向 4。当前文件领域模型没有类别概念，这在规模化后会成为问题。

**为什么需要**：
- 文件列表 API 当前不支持按类型过滤
- 搜索无法限定 MIME 类别
- 存储统计（"视频占了多少空间"）不可回答
- 未来缩略图管线、转码服务需要根据文件类别路由

**核心挑战**：
- MIME → Category 映射的维护——MIME 类型是无限的，但类别是有限的
- 历史数据回填：已有文件没有类别
- 公开/私有文件的 ACID：类别列本身不影响 ACL

**建议方案**：

```
// domain/file.go
type FileCategory string
const (
    CategoryImage    FileCategory = "image"
    CategoryVideo    FileCategory = "video"
    CategoryAudio    FileCategory = "audio"
    CategoryDocument FileCategory = "document"
    CategoryArchive  FileCategory = "archive"
    CategoryOther    FileCategory = "other"
)

// MIME → Category 映射 (硬编码在 domain 层)
var mimeCategoryMap = map[string]FileCategory{
    "image/jpeg": CategoryImage,
    "image/png":  CategoryImage,
    "video/mp4":  CategoryVideo,
    // ...
}

func ClassifyFile(mimeType string) FileCategory { ... }
```

**对现有系统的影响**：
- 新 migration：`ALTER TABLE files ADD COLUMN category TEXT NOT NULL DEFAULT 'other'`
- `FileUpload` / `CreateFile` 的写入路径：在 service 层在持久化之前自动分类
- `GET /v1/files?category=image`：仓库层添加可选的 `category` 过滤参数
- 搜索：`SearchInput` 添加可选的 `mime_type` 参数
- 计数端点：`GET /v1/files/stats` 按类别返回计数和大小

**向后兼容**：category 字段默认为 'other'，现有 API 不提供 category 过滤时行为不变。

### 方向 D：缺失事件类型填充（P2 — 产品完整性）

文档的方向 1、2、3。这是通知系统最直接的产品缺口。

**为什么需要**：
- 顶级评论不通知 → 用户社交反馈断裂（P1 UX 问题）
- Reactions/Likes 不通知 → 互动激励缺失
- 消息删除不通知 → 对话语义不透明

**核心挑战**：
- 每个事件类型需要仔细设计 payload 以避免过度通知
- 自操作排除：不要通知评论者自己的评论、点赞者自己的内容
- 反应折叠：连续点赞应在通知中聚合（"张三、李四赞了你的评论"）
- 删除通知的 UX 策略：是显示占位符（"此消息已被撤回"）还是只发通知

**利用已有基础架构**：
- 通知折叠（`collapsed_count`）已实现，可直接用于 reaction 聚合
- 速率限制已实现，可防止 reaction 洪泛
- 偏好检查已实现，用户可选择退出

**具体架构变更**：

| 新事件 | 生产者位置 | 通知类型 | 聚合策略 |
|--------|-----------|---------|---------|
| `file.commented` | `comment/service.go:Create()` 中当 `parent == nil` 时 | `comment.new` | 按文件聚合：5 分钟内同一文件的新评论 |
| `message.commented` | 同上，当 target_type=message 时 | `comment.new` | 按消息聚合 |
| `message.reacted` | `message/service.go:ReactToMessage()` 后 | `message.reaction` | 按发送方+消息聚合：连续 reaction |
| `comment.liked` | `comment/service.go:react()` 后 | `comment.liked` | 用户级别：先聚合再单独通知 |
| `message.deleted` | `message/service.go:Delete()` 后 | `message.deleted` | 不聚合 |

**对现有系统的影响**：直接在现有 service 的方法中添加 `outbox.NewEvent()` 调用，然后在 `notificationFor()` switch 中添加新 case。完成第 1 步（事件注册表方向 A）后，这一步变成仅注册新类型。

### 方向 E：实时通知历史恢复 + Hub 弹性（P2 — 架构韧性）

文档未直接覆盖，但从代码审查中浮现出来的问题。

**为什么需要**：
- 当前 Hub channel buffer = 16 → 高 burst 时静默丢事件
- 无连接日志 → 运维无法诊断"用户收不到通知"问题
- 虽然 `ListSince` 已实现，但无客户端指示器表明存在待回填事件

**核心挑战**：
- channel buffer 的适当大小——既要防止背压，又要保留合理的 burst 容量
- `last_event_id` 需要与 SSE 流中的通知行关联
- 超出 buffer 时的降级策略：是否阻塞发布者？

**建议方案**：

```
// hub.go — 配置化 buffer 大小 + 丢弃日志
type HubConfig struct {
    ChannelBuffer int // 默认 64
    OnDrop        func(uid, eventType string) // 日志回调
}

// SSE handler — 在握手时发送 last-event-id 回填
func (h *SSEHandler) ServeHTTP(w, r) {
    lastID := r.Header.Get("Last-Event-ID")  // unix millis
    if lastID != "" {
        missed, _ := h.notifs.ListSince(ctx, uid, parseMillis(lastID), 100)
        for _, n := range missed {
            // replay in correct order
        }
    }
}
```

**对现有系统的影响**：
- `Hub` 构造函数需要 `HubConfig` 参数（可选的，向后兼容）
- SSE handler 需要引用 `ListSince` 方法（目前属于 `NotificationRepository` 接口）
- Hub 需要 `OnDrop` 回调，通过 `Pusher` 接口暴露

---

## 3. 接口设计建议

### 3.1 核心原则

1. **仓库层接口是合同**：`NotificationRepository`、`MessageRepository` 等接口已完成定义，扩展时应严格遵守。
2. **通知生产应通过接口，而非直接调用 outbox.NewEvent**：当前 service 层直接调用 `outbox.NewEvent()`。建议引入 `EventPublisher` 接口隔离：

```go
// 新接口 — 位于 outbox 包或 domain 层
type EventPublisher interface {
    Publish(ctx context.Context, aggregateType, aggregateID, eventType string, payload any) error
}

// service 通过依赖注入获取 publisher，而非直接调用 outbox.NewEvent
type SendMessageInput struct {
    // ...
    Publisher EventPublisher  // 可选；不配置则不产生事件
}
```

**为什么需要**：测试时可以在不涉及数据库的情况下 mock 事件生产；未来可将 outbox 替换为其他事件总线时无需修改 service。

3. **PreferenceChecker 应演进为策略模式**：

```go
// 当前
type PreferenceChecker interface {
    IsAllowed(ctx, recipientUID, notificationType string) (bool, error)
}

// 未来 — 支持分层策略
type NotificationPolicy interface {
    // 0 = 允许, 非零 = 拒绝原因 (rate-limited, blocked, quiet-hours, opted-out, etc.)
    Evaluate(ctx context.Context, n *domain.Notification) (Decision, error)
}

type Decision int
const (
    DecisionAllow Decision = 0
    DecisionRateLimited Decision = 1
    DecisionBlocked Decision = 2
    DecisionOptedOut Decision = 3
    DecisionQuietHours Decision = 4
)
```

**为什么需要**：当前 dispatcher 的 `Handle()` 方法包含顺序的 if 链（偏好 → 限速 → 封锁），每个返回 nil。这使得 debug 困难——无法区分"被限速丢弃"和"被偏好静音"。返回 `Decision` 使关系可观察。

### 3.2 向后兼容策略

| 变更类型 | 策略 |
|---------|------|
| 新事件类型 | 新 producer 发送新 `EventType`；dispatcher 的 `default: return nil` 自动忽略未知类型 → 无破坏性 |
| 偏好作用域 | 新列 `scope_type TEXT DEFAULT ''` + `scope_id TEXT DEFAULT ''`；旧行 `scope_type=''` 被视为全局，行为不变 |
| 文件类别 | `ALTER TABLE ... ADD COLUMN category TEXT NOT NULL DEFAULT 'other'`；旧代码不读取类别列 → 不破坏 |
| 事件注册表 | 可选采用——新事件通过注册表注册，旧 switch 保留作为 fallback |

---

## 4. 技术选型

### 4.1 是否需要新技术？

| 技术 | 当前状态 | 评估 |
|------|---------|------|
| Message queue (NATS/RabbitMQ) | 不需要 | 当前 outbox + 5s ticker 在单实例场景下完全够用。outbox 模式本身就保证了"至少一次"交付。仅在多实例+多分区场景下需要消息队列 |
| Redis | 不需要（当前） | 通知折叠和速率限制使用内存实现；偏好检查走 DB。若偏好查询成为瓶颈，可在偏好检查前加 Redis 缓存层，但当前不需要 |
| 全文搜索增强 | 已使用 PG tsvector | 足够。无需 Elasticsearch |
| 事件溯源 | 不需要 | aero-id 的事件模型是"副作用"而非"状态变更历史"。outbox 模式是正确的选择 |

### 4.2 关键第三方依赖的评估标准

当前依赖集（Gin、GORM、Viper、Zap、gRPC、testcontainers）是健康且标准的。任何新依赖应满足：

1. **必要性**：Go 标准库能否实现？(如 `sync.Map` 已用于 token cache)
2. **维护活跃度**：GitHub stars + 最近提交频率
3. **API 稳定性**：v1 stable，非 v0.x
4. **大小**：不影响二进制体积（当前约 30MB distroless 镜像）
5. **许可证**：Apache 2.0 / MIT，非 AGPL

当前不满足引入任何新主要依赖的理由。

### 4.3 自建 vs 采购决策

| 功能 | 决策 | 理由 |
|------|------|------|
| `FileCategory` 枚举 | 自建 | 纯逻辑 + 硬编码映射，无需外部数据 |
| 通知折叠 | 自建 | 已实现（`collapsed_count` + `notificationFor`） |
| 速率限制 | 自建 | 已实现（`notifRateLimiter`） |
| 事件注册表 | 自建 | 纯声明式，无需框架 |

---

## 5. 实施路线图

### 5.1 优先级总览

```
P0 — 使能器（必须先完成）
P1 — 高业务价值 + 低实施成本
P2 — 中业务价值 + 中实施成本
P3 — 可推迟
```

### 5.2 阶段划分

**Phase 0 — 基础设施（~2 天）**
```
[A] 通知事件注册表
    ├── domain.EventTypeRegistry 声明
    ├── payload 类型定义（file.commented, message.reacted 等）
    ├── 验证器：注册表中的每个事件类型在 dispatcher 中有对应处理
    └── EventPublisher 接口
```

里程碑：新增事件类型只需在注册表中加一行 + dispatcher 加一个处理函数，无需修改其他基础设施。

**Phase 1 — 缺失事件类型填充（~3 天）**
```
[B] 方向 1 (P1): 顶级评论通知
    ├── comment/service.go: 当 parent == nil 时产生 file.commented / message.commented
    ├── dispatcher: 新增 notificationFor 分支
    └── 自操作排除 (commenter == owner → skip)

[C] 方向 2 (P1): Reactions/Likes 通知
    ├── message/service.go: ReactToMessage 后产生 message.reacted
    ├── comment/service.go: react() 后产生 comment.liked
    ├── dispatcher: 新分支 + 折叠策略
    └── 自操作排除

[D] 方向 3 (P2): 消息删除通知
    ├── message/service.go: Delete 后产生 message.deleted
    ├── dispatcher: 新分支
    └── 占位符策略产品决策
```

里程碑：所有 5 个文档缺口完成修复。通知系统覆盖所有用户交互场景。

**Phase 2 — 偏好精细化（~5 天）**
```
[E] 方向 5 (P2): 资源粒度偏好
    ├── migration: 扩展 notification_preferences 主键
    ├── domain: NotificationPreference.ScopeType + ScopeID
    ├── PreferenceChecker: 接口扩展
    ├── dispatcher: 通知构造时传递 scope 上下文
    └── API: POST /v1/files/:id/mute-comments 等
```

里程碑：用户可为单个文件/对话设置静音规则。

**Phase 3 — 数据组织 + 韧性（~3 天）**
```
[F] 方向 4 (P2): 文件类型分类
    ├── domain: FileCategory 枚举 + MIME 映射
    ├── migration: files.category 列
    ├── service: 上传时自动分类
    ├── API: GET /v1/files?category=image
    └── API(可选): GET /v1/files/stats

[G] 方向 E (P2): 实时通知历史恢复
    ├── HubConfig: 可配置 buffer 大小 + OnDrop 回调
    ├── SSE handler: Last-Event-ID 握手
    └── 连接生命周期日志
```

里程碑：文件可按类别过滤；SSE 重连无通知丢失。

### 5.3 风险矩阵

| 风险 | 概率 | 影响 | 缓解策略 |
|------|------|------|---------|
| 新的通知事件类型导致接收方通知洪泛 | 中 | 高 | 速率限制（已实现）+ 折叠（已实现）+ 默认开启偏好选择退出 |
| 偏好作用域 schema 变更导致旧行兼容性问题 | 低 | 高 | `scope_type TEXT DEFAULT ''` + 旧查询 `WHERE scope_type = ''` 过滤 |
| 文件类别 migration 导致锁表（大表 ALTER） | 低（依赖数据量） | 中 | 使用 `ALTER TABLE ... ALTER COLUMN ... SET DEFAULT 'other'` 而非 `ADD COLUMN ... NOT NULL DEFAULT`（PG 11+ 的 ADD COLUMN 是 O(1) 元数据操作） |
| 通知事件类型注册表与现有 switch 不同步 | 中 | 中 | 编译期断言 + 单元测试遍历注册表验证每个类型在 dispatcher 中有 handler |
| 资源级偏好查询性能退化 | 低 | 中 | 在 `(uid, notification_type, scope_type, scope_id)` 上加索引；考虑偏好缓存策略 |

### 5.4 估算总工作量

| 阶段 | 方向 | 估算（人天） | 工程师数量 |
|------|------|-------------|-----------|
| Phase 0 | 事件注册表 | 2 | 1 |
| Phase 1 | 3 个缺失事件类型 | 3 | 1-2 |
| Phase 2 | 资源粒度偏好 | 5 | 1-2 |
| Phase 3 | 文件分类 + SSE 韧性 | 3 | 1 |
| **总计** | | **13 人天** | **2-3 周** |

---

## 总结

文档正确识别了 5 个真实的通知生命周期缺口，但其分析**未注意到通知子系统已远超 MVP 阶段**——速率限制、折叠、偏好检查、物化计数器和 SSE `last_event_id` 回填均已实现。这意味着：

1. **方向 1-3 的实施成本实际上比文档估算的低**——新事件类型直接接入已有管道，不需要新建基础设施
2. **方向 5（资源粒度偏好）是最大的架构变更**——涉及 schema 迁移、接口扩展、查询性能优化
3. **方向 4（文件类型分类）在当前阶段最不紧急**——但实施最简单，可用作热身任务
4. **最关键的架构债务不是 5 个缺口本身，而是事件类型注册表的缺失**（方向 A）——它是所有缺失事件类型的使能器，应在方向 1 之前完成

建议执行顺序：**A → [B, C, D] 并行 → E → F → G**，而非文档建议的简单 1→2→3→4→5。
