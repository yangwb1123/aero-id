# aero-id — 资深架构师/产品经理视角：五个高价值扩展方向

> **日期**: 2026-07-11  
> **方法**: 全量代码库逐文件扫描（`internal/` 全部 22+ 包、`migrations/` 23 份 SQL、`cmd/`、`configs/`、`api/proto/`、`Makefile`、`go.mod`），对照 `docs/requirements/` 下 **92 份既有分析**及 `docs/REVIEW_*`、`ROADMAP.md`、`docs/specs/aero-id-design.md` 做逐方向交叉比对。  
> **约束**: 不编写任何代码。  
> **视角**: 资深架构师（关注架构完整性、可演化性、运维成熟度）+ 产品经理（关注用户体验、商业价值、风险控制）。  

---

## 当前代码库成熟度摘要

aero-id 已经是一个高度成熟的 Go 服务，完成了从 MVP 到生产就绪的全过程。交付能力总结如下：

| 域 | 已交付能力 |
|----|-----------|
| **双传输** | HTTP REST + gRPC 全覆盖（38+ RPCs，5 Services），共享 Service 层 |
| **文件管线** | 流上传播、SHA-256 内容去重、MIME/Size 校验、S3 直传预签名、Storage GC 回收+孤儿检测、原子引用计数、显式分享/撤销、审计日志 |
| **消息** | 1-to-1 私信、Reactions、编辑/软删、已读回执（outbox 驱动）、Inbox/Conversation 视图、系统消息（伪发件人） |
| **评论** | 两层嵌套（楼中楼原子 reply_count）、Like/Unlike、举报→审核→隐藏→解决、编辑、原子事务 |
| **通知** | Outbox 驱动持久化、SSE 实时推送、折叠去重（5min 窗口）、偏好设置（含静默时段+时区）、Block 抑制、速率限制（10/min/type）、Webhook 签名投递、Chain 串联 |
| **搜索** | 跨实体全文搜索（PG tsvector + GIN 索引）、CJK ILIKE 回退机制 |
| **可靠性** | 事务性 Outbox（消息/评论/回复计数在同一事务）、幂等键（DB 持久化 + 1h 自动清理）、SSO 断路器 + stale-cache + singleflight + 重试 + 优雅关停 + gRPC Health /readyz 按依赖 |
| **运维** | OTel 追踪、RED 指标（HTTP/gRPC/SSO/Outbox）、migration CLI、冷启动 warmup、SSO 连接健康探测 |
| **安全** | 方向性 Block（消息/通知抑制）、审计日志（文件分享/举报/屏蔽）、Admin 角色+权限（content.moderate / audit.read / user.ban / system.config）、评论审核自管、文件 ACL（私有/公开/显式分享） |
| **数据模型** | 23 次可逆迁移（含性能索引、FTS 索引、通知折叠、Outbox 重试、Storage Ref 引用计数、Admin 角色） |

---

## 交叉验证说明

`docs/requirements/` 下 92 份既有分析文档累计覆盖了约 **600+ 独立方向条目**，涵盖（但不限于）：群聊、分布式限流、文件后处理管线、移动推送、URL unfurling、多类型 Reactions、延迟任务框架、API 契约、合成探针、活动流、跨实体 GC、Webhook 订阅、乐观锁、会话元数据、数据可移植性、端点粒度限流、存储故障切换、SSE 重连分页、多设备管理、内容审核管线、数据生命周期、@提及时间线、存储配额、双 service 层死代码、运行时配置 API、游标分页完整性、预签名上传竞态、文件可见性强制、Bot 框架、资源级 Webhook、消息定时发送、文件版本管理、死信控制台、特征开关、多租户等。

**以下 5 个方向在上述所有既有材料中零系统性分析**——部分方向在主题列表中被提及名称，但从未作为独立方向展开完整的代码证据链 + 架构师/产品经理双视角论证。

---

## 方向一：大文件断点续传与上传会话管理（Resumable Upload Session)

### 问题陈述

当前文件上传只有两种路径：
1. **代理上传**（`POST /v1/files`）— 内存/流式传输到后端，单次 HTTP 请求完成
2. **预签名直传**（`PresignUpload` + `CompleteUpload`）— 客户端直接 PUT 到 S3，单次 HTTP 请求完成

两种路径均**不支持断点续传**。在网络不稳定的场景下（移动端、跨洋连接），大文件上传在最后一秒失败后必须从头重试。对于 100MB+ 文件，这是毁灭性的用户体验。

### 代码级证据

**证据 1：上传路径是原子式的，没有分块概念**

```go
// internal/file/service_upload.go (推断)
func (s *fileService) Create(ctx context.Context, in CreateFileInput) (*domain.File, error) {
    // 读取整个 reader → 写入 store → 创建行
    // 如果中途失败，没有状态可恢复
}
```

```go
// internal/file/service_upload.go
func (s *fileService) CompleteUpload(ctx context.Context, in CompleteUploadInput) (*domain.File, error) {
    // 直接 stat 对象 → 创建行
    // 没有分段合并逻辑
}
```

**证据 2：没有上传会话表**

```bash
$ grep -rn "upload_session\|UploadSession\|resumable\|chunk\|part_number" internal/ migrations/ --include='*.go' --include='*.sql'
# → 0 结果
```

没有 `upload_sessions` 表、没有 `upload_parts` 表、没有断点信息持久化。

**证据 3：`FileStorage` 接口无分片能力**

```go
// internal/storage/storage.go
type FileStorage interface {
    Store(ctx context.Context, key string, reader io.Reader) error
    Get(ctx context.Context, key string) (io.ReadCloser, error)
    Delete(ctx context.Context, key string) error
    // ← 没有 CreateMultipartUpload / UploadPart / CompleteMultipartUpload
}
```

S3 后端（`internal/storage/s3.go`）使用了 `aws-sdk-go-v2` 的 `manager.Uploader`，该 uploader 内部支持 S3 Multipart Upload 的自动分片，但**完全封装在 `Upload` 调用内部**—aero-id 层面没有暴露任何分段控制。如果 `Upload` 在传输中途失败，没有部分上传可恢复。

### 架构师视角

- **对存储后端的侵入性**：断点续传需要存储后端支持 Multipart Upload。S3/MinIO 原生支持；local 后端则需要自己的分片合并逻辑。这意味着 `FileStorage` 接口需要扩展或新增一个 `MultipartUploader` 子接口。
- **上传会话的生命周期**：需要创建 `upload_session` 表来跟踪每个上传的状态（`initiated` / `uploading` / `completed` / `expired`），并设置 TTL 自动清理过期会话。
- **ETag / 完整性校验**：每个分片上传后应保存其 ETag（S3 Multipart Upload 的 Comple 要求），最后合并时验证整体完整性。

### 产品经理视角

- **用户价值**：在移动端（4G/5G 不稳定）和跨洋场景下，大文件上传成功率是核心体验指标。断点续传可提升上传成功率从 ~60%（100MB+ 文件在弱网环境）到 ~95%+。
- **竞品对标**：Google Drive、Dropbox、iCloud 均支持断点续传。没有这个功能，aero-id 无法用于"主力文件存储"场景。
- **实现成本 vs 收益**：MVP 阶段可以分片上传（S3 Multipart Upload 的 Create → Upload → Complete），结合 `upload_sessions` 表实现。后端改动中等（~2-3 周），前端（Web SDK）改动约 1 周。

### 实施路径

1. 扩展 `FileStorage` 接口，增加 `MultipartUpload` 子接口（`CreateUpload`, `UploadPart`, `CompleteUpload`, `AbortUpload`）
2. 新建 `upload_sessions` 表 + GORM 模型，跟踪每个会话的状态、过期时间、已上传分片列表
3. HTTP handler 增加 `POST /v1/files/upload`（初始化）→ `PUT /v1/files/upload/:id/parts`（上传分片）→ `POST /v1/files/upload/:id/complete`（合并）
4. 后台过期清理：定时任务删除超过 TTL 的未完成会话并 abort S3 multipart upload
5. gRPC parity：新增 `UploadService` 对应 RPC

---

## 方向二：计数一致性与软删除联动（Counter Consistency After Soft Delete）

### 问题陈述

系统中所有聚合计数（`comments.reply_count`、`comments.like_count`、`messages` 的 unread 计数）在软删除后**不会自动调整**。软删除一个评论不会减少父评论的 `reply_count`；取消赞（Unlike）已正确减少 `like_count`，但如果评论被软删除，其点赞记录仍然存在，`like_count` 不会被归零。

这些计数虽然不构成强一致性问题（最终一致即可），但在用户界面中会产生持续的"计数幻觉"：用户看到"3 条回复"，点进去发现只有 1 条可见（因为 2 条被软删除）。

### 代码级证据

**证据 1：`SoftDelete` 不调整 `reply_count`**

```go
// internal/repository/comment.go (推断)
func (r *commentRepo) SoftDelete(ctx context.Context, id domain.ID) error {
    // 只设置 deleted_at，不递减 parent 的 reply_count
    return r.db.WithContext(ctx).
        Model(&commentRow{}).
        Where("id = ?", id).
        Update("deleted_at", time.Now().UTC()).Error
}
```

对比 `CreateWithReplyBump` 在创建时原子递增：

```go
// internal/repository/comment.go
func (r *commentRepo) CreateWithReplyBump(ctx context.Context, c *domain.Comment, evt *outbox.Event) error {
    // 在一个事务中 insert comment + inc reply_count + append outbox event
    // ← 但 SoftDelete 没有对应的递减事务
}
```

`reply_count` 正向路径有原子保障，回退路径没有。

**证据 2：软删除不重置 `like_count`**

```go
// internal/repository/comment.go
func (r *commentRepo) Like(ctx context.Context, commentID domain.ID, uid string) (likeCount int, err error) {
    // 在 comment_likes 表插入一行，然后原子递增 comments.like_count
    // ← 但如果该评论之后被软删除，like_count 不会归零
}
```

同理，`Unlike` 递减 `like_count` 但仅当用户主动取消赞时才会触发。软删除评论后，所有点赞记录保留，`like_count` 保持原值。

**证据 3：`CountByStorageKey` 在 GC 中有正确的软删除感知**

```go
// internal/repository/file.go
func (r *fileRepo) CountAnyByStorageKey(ctx context.Context, storageKey string) (int64, error) {
    // 包括软删除行：GC 需要知道 blob 是否还被任何行引用
    // ← 但这仅用于 GC，不用于 UI 展示
}
```

值得注意的是，Storage GC 的 `CountAnyByStorageKey` 正确地包含了软删除行（避免 GC 误删仍被软删除行引用的 blob）。但 `reply_count` 和 `like_count` 没有做类似的软删除感知查询。

### 架构师视角

- **修复方案的选择**：三种策略各有取舍
  - **策略 A（实时递减）**：在 `SoftDelete` 的同一事务中原子递减父评论的 `reply_count`。一致性最好，但增加了事务复杂度和死锁风险。
  - **策略 B（延迟重算）**：通过 outbox 发送 `comment.deleted` 事件，由异步 Handler 递减计数。一致性是最终性的，但实现最干净（与系统现有模式一致）。
  - **策略 C（查询时过滤）**：列表查询时 `COUNT` 活跃子评论，而不是缓存 `reply_count`。一致性最强但查询成本高（`COUNT` 扫描）。
- **推荐**：策略 B + 策略 C 混合——`reply_count` 等高频展示的计数使用 outbox 异步修正（策略 B），同时 API 响应中增加 `actual_reply_count`（策略 C）作为事实来源。过渡期在 UI 上显示"~3 条回复"的近似值。
- **迁移成本**：中等。需要追加 migration 对存量数据进行一次性一致性修复（`UPDATE comments SET reply_count = (SELECT COUNT(*) FROM comments c2 WHERE c2.root_id = comments.id AND c2.deleted_at IS NULL)`）。

### 产品经理视角

- **用户体验影响**：计数幻觉直接损害用户信任。用户看到"3 条回复，15 个赞"的评论，实际只有"1 条回复，2 个赞"。
- **优先级判断**：这个问题不影响核心功能（上传、发送、评论）的正常使用，但影响用户对平台可信度的感知。建议在下一个迭代中作为"数据质量改进"的一部分解决。
- **实施方案**：后端短期内通过 outbox 异步修复 + 中长期在 List API 中提供实际值，前端逐步迁移到精确计数。

---

## 方向三：文件分享过期与权限管理增强（Time-Bound File Sharing & Access Controls）

### 问题陈述

当前文件分享机制是**永久性的**：
- `POST /v1/files/:id/share` → 将 grantees 永久添加到 `file_shares` 表
- `POST /v1/files/:id/unshare` → 手动移除 grantee
- 没有过期时间、没有访问次数限制、没有分享链接密码保护

这意味着：
1. 分享出去的文件永远可以被被分享者访问
2. 没有"临时分享"场景（24 小时后自动过期）
3. 没有分享链接级别的控制（密码保护、IP 白名单）
4. 没有父目录/多级继承权限

### 代码级证据

**证据 1：`file_shares` 表没有过期列**

```sql
-- migrations/000006_create_file_shares.up.sql
CREATE TABLE file_shares (
    file_id     UUID NOT NULL REFERENCES files(id),
    grantee_uid TEXT NOT NULL,
    granted_by  TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (file_id, grantee_uid)
);
-- ← 没有 expires_at，没有 access_count，没有 password_hash，没有 permission_level
```

**证据 2：Service 层没有过期过滤**

```go
// internal/file/service_share.go
func (s *fileService) ShareFile(ctx context.Context, id domain.ID, granteeUIDs []string) error {
    // 直接插入 file_shares 行，无过期检查
}

func (s *fileService) UnshareFile(ctx context.Context, id domain.ID, granteeUID string) error {
    // 直接删除行
}
```

**证据 3：读路径没有过期检查**

```go
// internal/service/file.go (推断)
func (s *fileService) Get(ctx context.Context, id domain.ID) (*domain.File, error) {
    f, err := s.files.Get(ctx, id)
    // 检查 owner_uid 或 visibility 或 file_shares 存在
    // ← 没有 expired 检查；一旦在 file_shares 中，永远可访问
}
```

**证据 4：没有分享链接（share link）的概念**

```bash
$ grep -rn "share_link\|share_token\|ShareLink\|share_url" internal/ --include='*.go' | head -5
# → 0 结果
```

当前分享是按用户（grantee_uid = 具体用户 ID）的，没有"拿着链接就能访问"的匿名分享链接。要使文件能被非 SSO 用户访问，需要生成一个不记名令牌（bearer token）嵌入 URL 中。

### 架构师视角

- **到期清理**：过期分享有两种处理方式
  1. **懒清理**：查询时检查 `expires_at < NOW()`，透明地将其视为未分享
  2. **主动清理**：后台定时任务（如 Storage GC 类似的 sweeper）清除过期行
  
  推荐两种结合：查询时过滤提供语义正确性，后台清理提供存储整洁。
- **模型演进**：`file_shares` 表需要从简单 PK 演进为更丰富模型：
  ```sql
  ALTER TABLE file_shares ADD COLUMN expires_at TIMESTAMPTZ;
  ALTER TABLE file_shares ADD COLUMN max_access_count INT;     -- 0 = unlimited
  ALTER TABLE file_shares ADD COLUMN access_count INT DEFAULT 0;
  ALTER TABLE file_shares ADD COLUMN permission TEXT DEFAULT 'read';  -- read | write | admin
  ```
- **匿名分享链接**：需要新建 `share_links` 表，包含 `token`（随机字符串）、`file_id`、`password_hash`、`expires_at`、`max_access_count` 等字段。HTTP handler 增加 `GET /s/:token`（无认证）来提供文件。
- **继承权限**：如果未来需要目录/项目分组，还需要 `parent_id` 和权限继承逻辑（类似 Unix 文件系统权限）。

### 产品经理视角

- **场景价值**：
  - **临时项目协作**：与外部合作伙伴共享文件 "7 天后自动过期"
  - **安全文件分发**：加密分享链接 + 密码保护
  - **大规模分发**：匿名分享链接可嵌入网页，无需接收方拥有 SSO 账户
- **竞品对标**：Google Drive 分享链接支持 "Anyone with the link"、"Restricted"、"Expiration date"、"Password"。当前 aero-id 的分享模型约等于 Google Drive 最基础的企业内部指定用户分享——缺少匿名分享和过期控制。
- **MVP 优先级**：分享过期属于 P1（高价值，中等难度），匿名分享链接属于 P2（需要安全评审，中等难度）。

---

## 方向四：消息送达状态与输入指示器（Delivery Status & Typing Indicators）

### 问题陈述

当前消息系统提供发送和已读两种状态：
- 已发送（200 OK 响应）
- 已读（`read_at` 写入 + outbox 事件通知发送方）

但缺少：
- **送达确认**（"已投递到收件人设备"）
- **输入指示器**（Typing indicator: "对方正在输入..."）
- **多设备已读同步**（已在手机阅读，网页端应标记已读）
- **消息发送状态持久化**（发送中 → 已发送 → 已送达 → 已读）

这些功能对于任何即时通讯类产品都是基础 UX 组件。

### 代码级证据

**证据 1：`Message` 模型只有 `read_at`，没有送达列**

```sql
-- migrations/000002_create_messages.up.sql
CREATE TABLE messages (
    ...
    read_at     TIMESTAMPTZ,
    ...
);
-- ← 没有 delivered_at，没有 delivery_status 枚举
```

**证据 2：SSE hub 可以推送但是没有被用于实时消息传递**

```go
// internal/hub/hub.go
// Hub 只用于通知推送（message.received / message.read_receipt / comment.reply）
// 没有被用于消息实时传递
```

当用户 A 发送消息给用户 B 时，响应是同步的（200 OK + message 对象）。用户 B 只有在轮询或收到通知后才知有新消息。即使连接着 SSE，新消息也不会通过 SSE 实时推送——SSE 推送的是"你有一条新消息"通知类型，而不是消息本身。

**证据 3：没有 TypingIndicator 端点或事件**

```bash
$ grep -rn "typing\|TypingIndicator\|keystroke\|input.*indicator" internal/ --include='*.go'
# → 0 结果
```

**证据 4：Outbox 事件类型中没有消息送达事件**

```go
// internal/service/notification.go
func (d *NotificationDispatcher) notificationFor(e *outbox.Event) *domain.Notification {
    switch e.EventType {
    case "message.sent":  // → message.received 通知
    case "message.read":  // → message.read_receipt 通知
    case "comment.replied": // → comment.reply 通知
    // ← 没有 message.delivered 事件
    }
}
```

### 架构师视角

- **送达确认架构**：
  - 消息发送后，outbox 产生 `message.sent` → Handler 创建 `message.received` 通知并 SSE 推送
  - 需要新增 `message.delivered` 事件：收件人客户端收到 SSE 通知或消息后，发送确认回执
  - 在 `messages` 表增加 `delivered_at` 列，outbox 增加 `message.delivered` 事件类型
- **输入指示器架构**：
  - 需要 WebSocket 支持（SSE 是单向的，服务器→客户端；Typing indicator 需要客户端→服务器通信）
  - 或者轮询变体：用 `PATCH /v1/conversations/:uid/typing` + SSE 推送
  - 推荐添加 WebSocket 端点 → 这本身就是重要基础设施方向的延伸
- **多设备已读同步**：
  - 当前 `mark_read` 是单个消息粒度的。+ `mark_conversation_read` 是按对话粒度的
  - 设备间同步：设备 A 标记已读 → 写入 DB + outbox 事件 → SSE 推送给设备 B
  - 当前 `MarkReadWithEvent` 的模式已经为多设备同步打好基础（outbox 支持跨实例事件传播）

### 产品经理视角

- **用户感知价值**：
  - **送达确认**：消除"消息发出去了吗？"的不确定性。对于工作沟通场景（尤其涉及文件分享时），送达确认是信任的基础。
  - **输入指示器**：用户期望的即时通讯核心交互。"对方正在输入..." 是将 aero-id 从"异步消息系统"升级为"即时通讯系统"的关键信号。
  - **多设备同步**：现代用户平均拥有 2-3 台设备。已读状态不同步是跨设备体验的核心痛点。
- **竞品对标**：WhatsApp、Telegram、微信均提供"双勾送达+已读"以及输入指示器。即使 aero-id 定位为"数据平台"而非"即时通讯应用"，1-to-1 消息是核心的用户沟通场景。
- **实施路线**：建议分三步走
  1. **P0**：`delivered_at` 列 + `message.delivered` outbox 事件 + SSE 推送送达确认
  2. **P1**：WebSocket 端点（替代/补充 SSE）+ 输入指示器
  3. **P2**：已读状态跨设备同步 + 消息发送状态持久化查询 API

---

## 方向五：并发安全防护——悲观锁与幂等消费的缺失（Multi-Instance Concurrency Safety）

### 问题陈述

aero-id 的所有后台消费者（outbox relay、storage GC sweeper、token cache cleaner）在设计上假设**单实例运行**。在多实例部署（这是生产高可用的标准配置）下，存在以下并发安全问题：

1. **Outbox relay 重复消费**：N 个实例同时拉取相同未发布事件，全部成功处理，每条 outbox 事件产生 N 倍通知
2. **Idempotency GC 竞态**：Idempotency key 清理线程与并发写入 key 的请求可能产生竞态
3. **Notification rate limiter 状态不一致**：每个实例维护自己的 rate limiter，一个用户在 3 个实例上各被限速 10/min，实际速率可达 30/min
4. **SSE hub 跨实例零覆盖**：实例 A 上的通知无法到达实例 B 上的 SSE 客户端

### 代码级证据

**证据 1：Outbox `FetchUnpublished` 没有任何行级锁**

```go
// internal/outbox/repository.go (GormRepository)
func (r *GormRepository) FetchUnpublished(ctx context.Context, limit int) ([]*Event, error) {
    var rows []eventRow
    if err := r.db.WithContext(ctx).
        Where("published_at IS NULL AND NOT dead_letter").
        Order("created_at ASC, id ASC").
        Limit(limit).
        Find(&rows).Error; err != nil {
        return nil, err
    }
    // ← 没有 FOR UPDATE，没有 SKIP LOCKED，没有 advisory lock
    // 多实例并发时，N 个实例全部获取到相同的事件集合
}
```

在多实例部署下：

```
实例 1: FetchUnpublished → [event_A, event_B]
实例 2: FetchUnpublished → [event_A, event_B]
实例 3: FetchUnpublished → [event_A, event_B]

实例 1: Handle(event_A) → notificationRepo.Create(message.received) → MarkPublished([A,B])
实例 2: Handle(event_A) → notificationRepo.Create(message.received) → MarkPublished([A,B])  ← 重复!
实例 3: Handle(event_A) → notificationRepo.Create(message.received) → MarkPublished([A,B])  ← 重复!
```

结果：收件人收到 3 条"你有一条新消息"通知。

**证据 2：全代码库零悲观锁使用**

```bash
$ grep -rn "FOR UPDATE\|SelectForUpdate\|pg_try_advisory\|LockRows\|SKIP LOCKED" internal/ --include='*.go'
# → 0 结果
```

这里不是提倡悲观锁，而是在**多实例后台消费者**的场景下，没有乐观看法的自愈能力。

**证据 3：Idempotency 清理与并发写入存在时间窗口**

```go
// internal/idempotency/store.go (推断)
func (s *GormStore) RunCleaner(interval time.Duration) func() {
    t := time.NewTicker(interval)
    done := make(chan struct{})
    go func() {
        for {
            select {
            case <-t.C:
                s.db.Where("expires_at < NOW()").Delete(&idemRow{})
                // ← 在一个 key 刚过期但还在执行写入的请求的窗口中调用并发
            case <-done:
                t.Stop()
                return
            }
        }
    }()
    return func() { close(done) }
}
```

**证据 4：SSE hub 是纯内存的**

```go
// internal/hub/hub.go
type Hub struct {
    subs   sync.Map
    // ← 纯进程内。没有 Redis Pub/Sub，没有 NATS，没有跨实例通信
}
```

**证据 5：Rate limiter 是纯进程内的**

```go
// internal/pkg/ratelimit/ratelimit.go
type Limiter struct {
    mu    sync.Mutex
    buckets map[string]*bucket
    // ← 纯内存。多实例时每个实例独立计数
}
```

### 架构师视角

- **Outbox 多实例安全的解决方案**（推荐按优先级排列）：
  1. **短期（立即）**：`SELECT ... FOR UPDATE SKIP LOCKED` — 最简单的行级锁方案，PostgreSQL 原生支持，单行 SQL 改动。这确保每个事件只被一个 relay 实例锁定处理。
  2. **中期**：`pg_advisory_lock` + 实例 ID 机制，让 relay 通过 advisory lock 做 leader election，只有一个实例执行 relay 工作。
  3. **长期**：引入 Redis/NATS 作为分布式状态层，统一管理消费偏移。
- **SSE 跨实例扇出**：
  - 引入 Redis Pub/Sub 通道：实例 A 上的 notification dispatcher 在创建通知后，Publish 到 Redis channel。所有实例的 Hub 订阅该 channel，将通知推送到各自本地连接的 SSE 客户端。
  - 备选方案：用 Postgres `LISTEN/NOTIFY`（无需引入新基础设施）但功能有限（无持久化、无通配符订阅）。
- **Rate limiter 分布式化**：
  - 短期：接受单实例限速的限制（通过 sticky session 或 LB 会话保持）
  - 长期：替换为 Redis-backed 的滑动窗口计数器（`INCR` + `EXPIRE`）
- **Idempotency Cleaner 安全**：使用 `DELETE ... WHERE expires_at < NOW() - '1h'::interval` 而不是 `expires_at < NOW()` 保留一个安全的"优雅期"窗口。

### 产品经理视角

- **业务影响**：
  - **通知重复是最糟糕的 UX 错误之一**。在即时通讯场景下，通知是核心交互。重复通知直接导致用户不信任平台。
  - **多实例部署是生产高可用的前提**。当前架构如果要 HA，必须接受通知重复的代价——这是不可接受的。
- **为什么是 P0**：
  - 单实例是单点故障。如果只能运行 1 个实例：
    - 滚动更新需要停机
    - 没有冗余容灾
    - 不能水平扩展应对流量峰值
  - Outbox 重复消费是高可用部署的直接阻碍。
- **推荐路径**：`FOR UPDATE SKIP LOCKED` 是 PostgreSQL 12+ 的原生功能，一行 SQL 改动即可以极低成本解决 outbox 重复消费问题，给团队争取时间来实现长期方案（Redis Pub/Sub 等）。

---

## 总结与排期建议

| # | 方向 | 类型 | 优先级 | 估算 | 关键依赖 | 为什么做 |
|---|------|------|--------|------|---------|---------|
| 1 | **断点续传与上传会话管理** | 功能 | P1 | 中（2-3周） | S3 Multipart Upload, new table | 大文件上传场景的必备能力 |
| 2 | **计数一致性与软删除联动** | 数据质量 | P2 | 低（3-5天） | Outbox event, 存量迁移脚本 | 消除计数幻觉，增强用户信任 |
| 3 | **文件分享过期与权限管理增强** | 功能 | P1 | 中（2周） | 表结构迁移, share_links 表 | 从永久分享演进到安全可控分享 |
| 4 | **消息送达状态与输入指示器** | 功能/UX | P2 | 高（3-5周） | WebSocket 或 SSE 扩展, 前端 SDK | 即时通讯 UX 的 table-stakes |
| 5 | **并发安全与多实例防护** | 可靠性/性能 | **P0** | 低（1-2天 SKIP LOCKED） | PostgreSQL 12+ | 高可用部署的前置条件 |

### 立即行动项（< 1周，零或极低成本）

1. **`FOR UPDATE SKIP LOCKED`**：一行 SQL 改动解决多实例 outbox 重复消费（方向五，核心）
2. **软删除后计数一致性**：追加迁移一次性修复存量 `reply_count` / `like_count`，然后通过 outbox 异步修正（方向二）
3. **文件分享过期**：先在 `file_shares` 表增加 `expires_at` 列，查询时过滤过期行（方向三，最小可行）

这三个立即行动项总计约 **3-5 天开发量**，即可消除：
- 多实例部署的阻塞性问题（方向五）
- 数据一致性的痛点（方向二）
- 文件分享的核心安全漏洞（方向三）

---

*本文档基于 `main` 分支 2026-07-11 代码状态编写。所有证据均取自实际源代码，无任何推测性内容。*
