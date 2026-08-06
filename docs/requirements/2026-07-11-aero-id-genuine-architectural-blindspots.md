# aero-id — 全量代码扫描：五项结构性盲区与高价值扩展方向

> **日期**: 2026-07-11  
> **视角**: 资深架构师 + 产品经理  
> **方法**: 逐文件通读 `internal/`（23 子包 ~180 源文件）、`migrations/`（23 份 SQL）、`api/proto/`、`cmd/`、`configs/`、`deployments/`、`Makefile`，与 `docs/requirements/` 下 **100+ 份既有需求分析**、`ROADMAP.md`、`docs/REVIEW_*`、`docs/CHANGELOG.md`、`docs/DECISIONS.md` 做逐方向交叉比对。  
> **约束**: 不编写任何代码。每个方向提供精确的代码级证据，从架构师与产品经理双视角论证。  
> **核心承诺**: 以下方向在所有既有材料中未被系统性论证。

---

## 代码库成熟度摘要

aero-id 已从一个三层 CRUD MVP 演变为成熟的生产级服务平台：

| 域 | 已交付能力 |
|----|-----------|
| **基础设施** | 双协议 HTTP+gRPC、GORM+PG16、本地/S3 存储、OTel 追踪、RED 指标、per-dependency /readyz、冷启动 warmup、优雅关停 |
| **可靠性** | 事务性 Outbox、幂等键、速率限制、SSO 断路器 + stale-cache + singleflight + 重试退避 + gRPC keepalive/TLS/mTLS/证书锁定 |
| **文件** | 流上传、MIME/size 校验、SHA-256 去重、S3 presigned 直传、显式分享/撤销、引用计数 GC、元数据 PATCH、storage refs |
| **消息** | 1:1 私信、Reactions、编辑/软删、已读回执+计数、Inbox/Conv 视图、系统消息、Block 拦截 |
| **评论** | 两层嵌套、Like/Unlike、原子计数、举报→审核队列、Hide/Unhide、编辑 |
| **通知** | Outbox 驱动、SSE 实时推送、折叠去重、偏好(静默时段)、Block 抑制、Webhook 投递 |
| **搜索** | 跨实体 FTS (PG tsvector+GIN)、CJK ILIKE 回退、多 kind 联合搜索 |
| **安全** | 方向性 Block、审计日志、Admin 角色+权限(content.moderate/audit.read/user.ban/system.config) |
| **数据模型** | 23 次迁移（含 FTS 索引、通知索引、Outbox 重试、通知折叠、Storage Ref、Admin 角色） |

---

## 前置确认：已被充分覆盖的方向

以下方向在 100+ 份既有分析中已被系统性论证，本文不再重复：

- 群组/频道消息（Group / Channel Messaging）
- API 密钥体系（API Key / Developer Portal）
- 移动推送（FCM / APNs）
- 文件版本管理（File Versioning）
- 消息撤回（Recall / Retract）
- 死信运维与 Outbox 可观测性（DLQ / Outbox Observability）
- 全文搜索增强（Search Relevance / Highlighting）
- WebSocket 双向通信（Typing / Presence）
- 实体变更历史（Entity Change History / PITR）
- 多实例 SSE 兼容（SSE in multi-instance via pub/sub）
- 数据导出 / GDPR 可移植性（Data Export / Portability）
- 配置热加载（Config Hot-Reload）

---

## 方向一：代码库结构性分裂——新旧两套架构并行，新架构完全不可达

### 问题陈述

当前代码库存在**两套完全独立且功能重叠的架构**：一套是传统的分层架构（`internal/service/` + `internal/handler/{http,grpc}/` + `internal/repository/`），另一套是新的领域驱动包架构（`internal/comment/`、`internal/message/`、`internal/file/`、`internal/notification/`、`internal/block/`、`internal/search/`、`internal/audit/`）。但 `internal/app/app.go` 只组装了**旧架构**，新架构的代码全部处于**不可达状态（dead code）**——它们被编译但不被任何生产代码路径引用。

更严重的是，这两套架构之间存在**隐式的 actor context 键不兼容**：旧架构使用 `service.WithActor(ctx, uid)` 存储 Actor，新架构使用 `domain.MustActor(ctx)` 读取 Actor。由于两个 `ctxKey` 类型定义位于不同包中（`service.ctxKey` vs `domain.ctxKey`），通过 `service.WithActor` 写入的值在 `domain.MustActor` 中**永远不可见**。这意味着如果有人尝试将新架构的服务 wiring 到生产路径中，所有请求都会返回 `auth.unauthorized`。

### 代码级证据

**证据 1：`app.go` 只引用旧架构包的构造器**

```go
// internal/app/app.go:295-309
httpapi.NewFileHandler(fileSvc, downloader, presignTTL),     // internal/handler/http/  （旧）
httpapi.NewMessageHandler(msgSvc),                            // internal/handler/http/  （旧）
httpapi.NewCommentHandler(commentSvc),                        // internal/handler/http/  （旧）
httpapi.NewNotificationHandler(notifSvc),                     // internal/handler/http/  （旧）
httpapi.NewBlockHandler(blockSvc),                            // internal/handler/http/  （旧）
httpapi.NewSearchHandler(searchSvc),                          // internal/handler/http/  （旧）
```

所有 `*Svc` 变量均通过 `service.New*Service(...)` 构建（`internal/service/` 旧包）。

**证据 2：新领域包被其他生产代码零引用**

```bash
$ grep -rn "aero-id/internal/comment\|aero-id/internal/file\|aero-id/internal/message\
          \|aero-id/internal/notification\|aero-id/internal/block\|aero-id/internal/audit\
          \|aero-id/internal/search" internal/handler/ internal/app/ internal/middleware/ cmd/
# → 无输出（排除测试文件和 .pb.go）
```

唯一使用新领域包的是它们彼此之间（`internal/notification/dispatcher.go` → `internal/block`，`internal/search/` → `internal/comment`,`internal/file`,`internal/message`），但这些调用方本身也是死代码。

**证据 3：Actor context 键分裂——同一协议、不同键名**

`internal/middleware/auth.go` 写入 Actor：
```go
// internal/middleware/auth.go:38
c.Request = c.Request.WithContext(service.WithActor(c.Request.Context(), uid))
service.WithActor 使用 service.ctxKey(0) 写入
```

`internal/grpc/server.go` gRPC 拦截器同样写入：
```go
// internal/handler/grpc/server.go:184
return handler(service.WithActor(ctx, uid), req)
```

但新领域服务使用 `domain.MustActor` 读取：
```go
// internal/comment/service.go:87
actor, err := domain.MustActor(ctx)      // 使用 domain.ctxKey(0) 读取
// internal/file/service_crud.go:19
actor, err := domain.MustActor(ctx)
// internal/block/block.go:52
actor, err := domain.MustActor(ctx)
```

`service.ctxKey` 和 `domain.ctxKey` 是**不同的类型**（不同包的 `type ctxKey int` 定义且均未导出），因此 `service.WithActor` 写入的值永远不会被 `domain.MustActor` 读到——即使键的数值都是 `0`。

**证据 4：新领域包的 Dispatcher 同样是重复实现**

`internal/service/notification.go` 中的 `NotificationDispatcher`（被 `app.go` 使用）和 `internal/notification/dispatcher.go` 中的 `Dispatcher`（未被引用）实现完全相同的功能：接收 outbox 事件 → 解析 payload → 创建通知行 → 发出 SSE 推送。前者支持偏好检查+速率限制+折叠去重，后者只支持 block 检查和 SSE 推送。两套代码各自维护，各自测试。

### 架构师视角

这是代码库当前**最严重**的结构性问题，优先级高于任何新功能。

- **双倍维护成本**：任何业务逻辑变更都需要在两套架构中同步修改。`ROADMAP.md` 第 1→5 轮迭代期间的所有功能在两个架构中都有对应实现，意味着每一轮开发成本翻倍。
- **新人的认知负担**：新成员必须理解两套架构的关系、哪套是活代码、哪套是死代码、为什么两套存在。
- **阻碍重构**：死代码不会腐烂——它们的 import 依赖阻止了旧架构的独立演进。任何包级别的重命名或拆分都可能误伤死代码的编译。
- **隐式 bug**：证据 3 的 actor context 不兼容意味着 **"新架构看似可以接入，但实际上无法工作"**——这是一个在编译期完全不可见、运行时才会暴露的陷阱。

### 产品经理视角

对用户无直接影响，但对**交付速度**有深远影响：

- 每增加一个功能（如群组消息、文件版本），开发团队面临的选择是"在旧架构上加"还是"在新架构上加"——两者都不能令人满意。
- 死代码的存在意味着技术债务利息在持续累积。架构统一是一个零用户价值、但**解锁所有未来交付**的前置条件。
- 参考 Martin Fowler 的 Strangler Fig 模式：当 strangler 包裹了足够多的原系统后，原系统应当被移除。当前状态是 strangler 与被 strangler 的系统并存，但 strangler 没有接入流量——这是最糟糕的状态。

### 价值主张

**消除死代码 + 统一 actor context + 完成新架构的 wiring** 这三件事应当作为一个独立的工作流（非 P0 生产问题、但 P1 内部债务）在**下一轮迭代之前完成**。预估工作量：低—中（约 3-5 人天，主要是测试验证和新架构的补全）。

---

## 方向二：数据保留与生命周期策略——无自动化的数据过期与销毁机制

### 问题陈述

当前代码库支持软删除（`gorm.DeletedAt`）和存储 GC（`storage/gc/sweeper.go`），但**没有任何面向用户的数据生命周期策略**：

- 用户不能设置消息自动过期（阅后即焚、定时销毁）
- 系统不能配置全局保留期限（例如通知保留 90 天、已读消息保留 1 年）
- 没有"归档"概念（将旧数据移到冷存储，但仍可查询）
- 存储 GC 只清理软删除文件行的 blob，不处理活跃数据的生命周期

对于消息系统而言，数据自动过期是用户隐私的核心功能之一；对于平台运营而言，缺乏保留策略意味着存储成本线性增长且可能违反数据保护法规。

### 代码级证据

**证据 1：软删除没有 TTL 驱动的硬删除**

所有实体都使用 GORM 的 `DeletedAt` 实现软删除：
```sql
-- migrations/000001_create_files.up.sql
deleted_at timestamptz,

-- migrations/000002_create_messages.up.sql
deleted_at timestamptz,

-- migrations/000003_create_comments.up.sql
deleted_at timestamptz,
```

但只有存储 GC 扫描 `files` 表的软删除行（`ListSoftDeletedBefore`），**没有任何周期性任务扫描 `messages`、`comments` 或 `notifications` 的软删除行**。这些行会无限期驻留在表中。

**证据 2：`expires_at` 或 `ttl` 字段在所有模型中不存在**

```go
// internal/domain/message.go — Message 结构体
type Message struct {
    ID        ID
    FromUID   string
    ToUID     string
    Kind      MessageKind
    Content   string
    FileID    *ID
    ReadAt    *time.Time
    EditedAt  *time.Time
    Meta      map[string]any
    CreatedAt time.Time
    // 没有 ExpiresAt、TTL、RetentionPolicy 等字段
}
```

文件、评论、通知模型同理——没有任何生命周期相关的字段。

**证据 3：GC sweeper 只处理文件**

```go
// internal/storage/gc/sweeper.go — Start 方法
func (s *Sweeper) Start(interval time.Duration) func() {
    // 只清除软删除的文件行和孤立的 blob
    // 没有处理消息、评论、通知
}
```

关键差异：存储 GC 是为了防止存储空间泄漏（文件 blob 不删除会直接产生云成本），而消息/评论/通知的生命周期管理是一个产品功能。

**证据 4：没有"归档"或"冷存储"概念**

`FileStorage` 接口只有 `Put/Get/Delete/PresignGet/PresignPut/Stat/NewKey` 方法，没有任何 `Archive/Restore/MoveToTier` 之类的操作。

### 架构师视角

生命周期管理是一个**横切关注点**，需要以下层面的支持：

1. **数据模型层**：为每个实体添加可选的 `expires_at` 时间戳，以及一个可扩展的 `retention_policy` 枚举（`keep_forever` / `delete_after_read` / `ttl_seconds`）。
2. **定时任务层**：扩展 GC sweeper 为通用的"数据管家"（Data Custodian），支持多个实体类型的生命周期策略评估和批量操作。
3. **查询层**：活跃查询应当自动排除已过期但尚未硬删除的行（`WHERE (expires_at IS NULL OR expires_at > now())`）。
4. **存储层**：为文件存储添加冷热分层——S3 有现成的生命周期策略（transition to IA/Glacier），但本地存储需要自行实现。

### 产品经理视角

数据生命周期是**多用户消息系统的标准功能**：

| Feature | 用户价值 | 优先级 |
|---------|---------|--------|
| 阅后即焚（消息被阅读后自动删除） | 隐私敏感用户的核心需求 | P1 |
| 定时消息 (Snapchat 风格) | 年轻用户群期望功能 | P2 |
| 全局保留策略（平台级） | GDPR/CCPA 合规、存储成本控制 | P0 |
| 消息归档（保留但不可见/需要搜索） | 高价值客户的功能需求 | P2 |

### 边界情况

- **阅后即焚 vs 已读回执**：消息被读后立即删除，发送方看不到已读回执（或需要在删除前发送）。
- **定时消息的精确性**：软删除 + 懒汉式硬删除（查询时过滤）比定时任务更可靠——定时任务可能错过窗口。
- **引用的完整性**：消息被自动删除后，引用该消息（通过 FileID）的文件不应被级联删除，除非显式配置。

---

## 方向三：消息对话维度功能缺失——Pin / Mute / Archive / Search-in-Conversation

### 问题陈述

当前消息系统的功能完全围绕**单条消息**和**收件箱视图**展开。对话（Conversation）层面没有任何元数据操作：

- 不能置顶（Pin）重要对话
- 不能静音（Mute）对话（阻止通知但收到消息）
- 不能归档（Archive）对话（从收件箱隐藏但可搜索）
- 可以在**全量**范围搜索消息，但不能限定在某个对话内搜索
- 没有对话级 last_read_at / unread_count 的高效查询

用户面对收件箱中的 100+ 个对话时，没有任何组织和管理手段。这从一个"发消息"的工具变成一个"管理消息"的平台之间的关键差距。

### 代码级证据

**证据 1：MessageRepository 只有 Inbox 和 Conversation 视图，没有对话元数据**

```go
// internal/message/message.go — MessageRepository 接口
type MessageRepository interface {
    Create(ctx context.Context, m *domain.Message, evt *outbox.Event) error
    Get(ctx context.Context, id domain.ID) (*domain.Message, error)
    ListInbox(ctx context.Context, toUID string, page domain.Page, unreadOnly bool) ([]*domain.Message, string, error)
    ListConversation(ctx context.Context, uidA, uidB string, page domain.Page) ([]*domain.Message, string, error)
    MarkRead(ctx context.Context, id domain.ID, at time.Time) error
    MarkReadWithEvent(ctx context.Context, id domain.ID, at time.Time, evt *outbox.Event) error
    CountUnread(ctx context.Context, toUID string) (int64, error)
    // 没有 PinConversation / MuteConversation / ArchiveConversation
    // 没有 SearchInConversation
    // 没有 GetConversationMeta / SetConversationMeta
}
```

**证据 2：领域模型中不存在"对话"实体**

```go
// internal/domain/message.go — 只有 Message
// 没有 Conversation 结构体
// 没有 Pin、Mute、Archive 等状态枚举
```

这个"对话"的概念在当前模型中完全由 `(from_uid, to_uid)` 隐式组合表达，没有独立身份。

**证据 3：搜索不支持对话级过滤**

```go
// internal/service/search.go — Search 方法
// 入参只有 query, kinds, limit
// 没有 conversation_id 过滤参数

// internal/repository/message.go — SearchMessages
// 只接受 uid, query, limit
// 无法限定在某个对话内搜索
```

**证据 4：已有基础设施可以支撑——cursor 分页和 SSO 用户解析**

对话列表视图的构建与收件箱非常相似（需要分页、SSO 用户信息 enrich），这些基础设施已就位。缺失的是：
- 对话的持久化状态（置顶/静音/归档元数据）
- 对对话进行操作的服务层方法
- 对话元数据的数据库表或列

### 架构师视角

对话维度功能的分层设计建议：

1. **新增 `conversation_meta` 表**（或扩展 message 模型），以 `uid_pair`（规范化排序后的 `uidA_uidB`）为主键，存储 `pinned BOOLEAN`、`muted BOOLEAN`、`archived BOOLEAN`、`last_read_at TIMESTAMPTZ`。
2. **扩展 `ListInbox`** 为 `ListConversations`，返回带元数据的对话摘要列表（包括最后一条消息摘要、未读数、Pin/Mute/Archive 状态）。
3. **扩展搜索**，增加可选的 `conversation_id` 过滤参数。
4. **利用已有基础设施**：cursor 分页、SSO user resolution、notification 机制（静音对话不产生通知）均有现成实现。

### 产品经理视角

对话管理是**消息产品从 1.0 到 2.0 的分水岭**：

| 功能 | 用户价值 | 替代方案 |
|------|---------|---------|
| 置顶对话 | 高频对话快速访问 | 无（用户只能翻页） |
| 静音对话 | 群发/通知类对话不干扰 | 无（block 太粗暴） |
| 归档对话 | 清理收件箱但不丢历史 | 无（删除即永久丢失） |
| 对话内搜索 | 快速定位历史消息 | 全局搜索太泛 |
| 已读状态 | 知道对方上次阅读到哪条 | 逐条 read_at 太细碎 |

### 边界情况

- **对话的隐式创建**：当两个人第一次互发消息时，对话应自动创建（当前确实存在这种情况，但没有任何初始化代码）。
- **数据迁移**：现有的 `Inbox` 查询返回的是扁平消息列表，迁移到对话摘要视图后需要兼容旧客户端。
- **静音 vs Block**：静音是单向的、可逆的、不通知对方；Block 是双向消息拦截。需要清晰的语义划分。

---

## 方向四：媒体管线缺失——图片缩略图/视频转码/文件预览能力

### 问题陈述

aero-id 管理的文件包括图片、视频、音频、文档等媒体类型，但**整个管线中没有对媒体内容的任何处理**：

- 上传的图片没有自动生成缩略图
- 上传的视频没有转码或封面截图
- 没有文件内容预览能力（PDF 预览、Office 文档预览）
- 纯客户端报告 MIME 类型，服务端不做 magic bytes 验证
- 没有 CDN 集成或边缘缓存的预签名 URL 优化

这意味着：
- 移动端下载原始图片（可能 10MB+）来展示一个缩略图，带宽和加载时间不可接受
- 视频文件无法在浏览器中直接播放（没有转码为 HLS/DASH）
- 恶意用户可以将可执行文件重命名为 `.jpg` 上传——MIME 检测只检查字符串，不验证字节签名

### 代码级证据

**证据 1：CreateFileInput 不要求任何媒体处理参数**

```go
// internal/file/service_upload.go — 文件上传流程
// 1. 校验 size 和 MIME（字符串对比 allow-list）
// 2. 计算 SHA-256 checksum（用于去重）
// 3. 存储原始 blob
// 4. 创建 file row
// → 没有任何缩略图、转码、预览生成
```

**证据 2：MIME 验证只做字符串匹配，不做内容检测**

```go
// internal/file/service_upload.go — mimeAllowed
func (s *fileService) mimeAllowed(mimeType string) bool {
    if s.allowedMime == nil {
        return true // 没有 allow-list 则接受任何 MIME
    }
    // 只是检查客户端声明的 MIME 字符串是否在 allow-list 中
    return s.allowedMime[strings.ToLower(strings.TrimSpace(mimeType))]
}
```

没有 `http.DetectContentType`（基于前 512 bytes 的 magic number 检测），没有第三方库如 `filetype/go` 或 `libmagic` 的集成。

**证据 3：File 结构体没有维度/时长/预览字段**

```go
// internal/domain/file.go
type File struct {
    ID          ID
    OwnerUID    string
    Name        string
    Size        int64
    MimeType    string
    StorageKey  string
    StorageType string
    Checksum    string
    Visibility  Visibility
    Meta        map[string]any
    CreatedAt   time.Time
    UpdatedAt   time.Time
    // 没有 Width/Height（图片）、Duration（音视频）、
    // ThumbnailKey（缩略图存储键）、PreviewURL 等字段
}
```

**证据 4：FileStorage 接口没有媒体处理能力**

```go
// internal/storage/storage.go — FileStorage 接口
// Put/Get/Delete/PresignGet/Stat/NewKey/PresignPut
// 没有 Thumbnail/Transcode/Analyze 等方法
```

### 架构师视角

媒体处理管线的引入需要一个新的抽象层——`MediaProcessor`——位于存储层和服务层之间：

```
Upload → MIME detection (magic bytes) → [MediaProcessor] → Store original + Store derivative
                                                              ├─ thumbnail (images)
                                                              ├─ HLS segments (video)
                                                              └─ preview (PDF/Office)
```

设计考虑：
- **同步 vs 异步**：缩略图生成可以同步（对小图）或异步通过 outbox（对大图）。转码必须是异步的。
- **存储位置**：衍生文件应使用与源文件相同的存储后端，通过命名约定关联（如 `{key}_thumb.jpg`）。
- **外部处理器**：视频转码通常需要外部服务（FFmpeg、AWS Elemental MediaConvert、Mux 等）。当前无外部任务框架。
- **CDN 集成**：衍生文件（缩略图、HLS 片段）更适合通过 CDN 分发。预签名 URL 的 TTL 管理和 CDN 缓存策略需要协调。

### 产品经理视角

| 功能 | 用户价值 | 实现复杂度 |
|------|---------|-----------|
| MIME 内容签名验证 | 安全基线——防止伪装上传 | 低（标准库支持） |
| 图片缩略图自动生成 | 移动端列表展示、带宽节省 | 中（引入 imaging lib） |
| PDF 预览 | 企业用户刚需 | 中（PDF.js 或后端转图片） |
| 视频转码（HLS） | 产品级差异功能 | 高（FFmpeg/外部服务） |

### 边界情况

- **缩略图失败不应阻塞上传**：媒体处理应为旁路（sidecar），缩略图生成失败不影响源文件可用。
- **去重与衍生文件**：checksum 去重需要同时关联衍生文件。
- **Presigned upload 的处理**：客户端直传 S3 时，缩略图需要在服务端异步下载后再生成。这增加了复杂性。
- **存储膨胀**：衍生文件可能比源文件大（如图片生成多分辨率缩略图、HLS 多码率转码）。需要谨慎设计存储策略。

---

## 方向五：Webhook 生态未完成——无端点管理、无签名轮换、无事件过滤

### 问题陈述

当前已经有了一个**出站 webhook 投递机制**（`internal/outbox/webhook.go`），它能将 outbox 事件通过 HMAC 签名发送到配置的 URL。但它有四个关键缺失：

1. **没有 webhook 管理 API**：`configs/config.yaml` 只能配置一个 webhook 端点。用户/租户不能通过 API 注册自己的 webhook。
2. **没有签名密钥轮换**：`signing_key` 在启动时从配置读取，运行时不可变。密钥泄露后无法在线轮换。
3. **没有事件过滤**：webhook 只能通过 `event_types` 配置来过滤，不能按资源类型、特定用户等条件过滤。
4. **没有重试策略暴露**：retry 完全由 outbox relay 控制（全局 `max_retries`）。单个 webhook 端点不可独立配置重试次数、退避策略或死信。

更关键的是，**没有入站 webhook 能力**——没有端点允许外部服务通过 webhook 来创建消息、上传文件或触发动作。aero-id 是一个"出站 webhook 客户端"而不是一个"webhook 平台"。

### 代码级证据

**证据 1：webhook 配置是进程级单例**

```go
// internal/app/app.go:274-285
if cfg.Outbox.Webhook.URL != "" {
    // 只能配置一个 webhook URL
    whCfg := outbox.WebhookConfig{
        URL:        cfg.Outbox.Webhook.URL,
        SigningKey: cfg.Outbox.Webhook.SigningKey,
        // ...
    }
    // 构建唯一的 webhook handler
    webhookHandler := outbox.NewWebhookHandler(whCfg, log)
}
```

没有 `UserWebhook` 或 `WebhookSubscription` 模型，没有数据库表存储多个 webhook。

**证据 2：webhook 端点由 Chain 硬编码串联**

```go
// internal/app/app.go:286
relayHandler = outbox.NewChain(dispatcher, webhookHandler)
```

所有 outbox 事件依次经过 dispatcher → webhook。每个事件都投递到同一个 webhook URL。没有过滤器来决定"这个事件应该投递到哪些 webhook"。

**证据 3：没有 webhook 重试独立退避**

```go
// internal/outbox/webhook.go
// webhook handler 的错误直接返回给 outbox relay
func (h *WebhookHandler) Handle(ctx context.Context, e *Event) error {
    // HTTP POST 到配置的 URL
    // 出错 → 返回 error → relay 重试（使用全局退避策略）
    // 没有 webhook 级别的独立退避策略
}
```

**证据 4：没有任何入站 webhook（Inbound Webhook）API**

```bash
$ grep -rn "webhook\|inbound\|ingress\|hook\|callback" internal/handler/http/*.go
# → 只找到 SSE /events 端点
# → 没有 POST /v1/webhooks/... 端点
```

**证据 5：现有的 WebhookConfig 已经为扩展做好准备但未使用**

```go
// internal/outbox/webhook.go
type WebhookConfig struct {
    URL        string
    SigningKey string
    Timeout    time.Duration
    EventTypes []string   // 已经支持事件类型过滤
    Headers    map[string]string
}
```

`EventTypes` 字段已被设计但只在 webhook handler 内部做过滤。没有 API 让用户配置它。

### 架构师视角

Webhook 生态的平台化需要三个架构新增：

1. **Webhook 订阅模型**：新增 `webhook_subscriptions` 表，持有 `id, owner_uid, url, signing_key, event_filters (JSONB), retry_config (JSONB), created_at, updated_at`。
2. **多端点路由**：`outbox.Handler` 的 Chain 模式需要升级为 Router（类似消息队列的主题->订阅模式）。每个 outbox 事件根据 `event_type` 和一个可选的过滤表达式路由到所有匹配的 webhook 端点。
3. **入站 webhook**：新增 POST `/v1/webhooks/inbound/:provider` 端点，允许外部服务通过 webhook 创建资源。需要一个"Webhook 翻译层"将外部格式转为内部 domain event。

### 产品经理视角

| 能力 | 用户价值 | 优先级 |
|------|---------|--------|
| 多 webhook 端点（API 管理） | 第三方集成基础设施 | P1 |
| 事件过滤 | 按需接收，减少带宽和噪音 | P1 |
| 签名密钥轮换 | 安全合规 | P0 |
| 入站 webhook | 外部系统触发 aero-id 操作（如 Slack 集成） | P2 |
| Webhook 日志/重放 | 调试和可靠性 | P2 |

### 边界情况

- **签名密钥泄漏**：提供 `POST /v1/webhook-subscriptions/:id/rotate-key` 端点，生成新的 HMAC 密钥并保留上一个密钥的宽限期（dual-key support）。
- **事件顺序**：同一个事件需要按顺序投递给同一个 webhook（但不同 webhook 之间可以并行）。
- **背压与重试**：一个慢速 webhook 不应阻塞其他 webhook 的投递。Chain 的线性模型在多端点下需要改为扇出（fan-out）。

---

## 总结与建议顺序

| # | 方向 | 类型 | 复杂度 | 用户可见 | 建议顺序 |
|---|------|------|--------|---------|---------|
| 1 | **架构分裂统一** | 技术债 | 低—中 | 否（内部） | **P0 — 前置条件** |
| 2 | **数据生命周期策略** | 功能 | 中 | 是 | P1 |
| 3 | **对话维度功能** | 功能 | 中 | 是 | P2 |
| 4 | **媒体处理管线** | 功能+平台 | 高 | 是 | P2 |
| 5 | **Webhook 生态** | 平台 | 中—高 | 是（开发者） | P1 |

**架构师建议**：方向 1 是**所有后续工作的技术前置**——在两套架构并存的状态下，任何跨域重构（如添加群组消息）都将面临"改哪套"的决策困境。先统一架构、消除死代码、修复 actor context 不兼容，再用统一的代码基础扩展方向 2-5。

**产品经理建议**：方向 2（阅后即焚等隐私功能）和方向 3（对话管理）对用户感知最强，且方向 3 可以复用方向 1 治理后的代码基础。方向 4 适合作为 Q3/Q4 的重点项目。方向 5 是开发者生态的长期基础设施。
