# aero-id — 产品级扩展方向（基于全量代码扫描）

> **日期**: 2026-07-11  
> **视角**: 资深架构师 / 产品经理  
> **方法**: 全量代码扫描 `internal/` 全部包、`migrations/` 全部 SQL、`api/proto/`、`configs/`、`cmd/`、`deployments/`。与 `docs/requirements/` 下 123 份既有分析文档做交叉比对，确保方向在既有材料中 **未被作为独立、完整论证的方向**。  
> **约束**: 不编写任何代码。每个方向提供精确的代码级证据。

---

## 代码库现状摘要

经过此前多轮迭代，aero-id 在基础设施层面已高度成熟：

| 领域 | 状态 |
|------|------|
| 双传输层（HTTP + gRPC，41 RPCs） | ✅ 完全实现 |
| 事务性 Outbox + 通知分发 + Webhook | ✅ 完全实现 |
| SSO 弹性（断路器、重试、陈旧缓存、TLS/mTLS） | ✅ 完全实现 |
| 速率限制（per-actor / per-IP 令牌桶） | ✅ 完全实现 |
| 可观测性（RED 指标、OTel 追踪、按依赖 readiness） | ✅ 完全实现 |
| 幂等键 + 迁移工具 + 存储 GC | ✅ 完全实现 |
| SSE 实时推送 + 通知偏好（免打扰时段） | ✅ 完全实现 |
| 全文搜索（PostgreSQL tsvector + GIN） | ✅ 完全实现 |
| 用户阻塞 + 评论举报/审核 + 管理员角色/权限 | ✅ 完全实现 |

**当前核心短板**不在基础设施层面，而在 **产品能力层面**。以下 5 个方向均瞄准：对于自称"data owned by accounts"的平台，当前缺少哪些**直接影响用户体验和平台适用场景**的能力。

---

## 方向一：资源配额与用量计量（Resource Quotas & Usage Metering）

### 问题陈述

aero-id 管理的三类核心资源（文件、消息、评论）**没有任何用户级别的配额限制或用量跟踪**。在一个生产平台中，这会导致以下问题：

1. **无存储上限**：用户可以上传任意数量的文件，直到磁盘/账单告警触发——没有软性预警，没有渐进式限制
2. **无消息频率限制**：虽然速率限制控制请求频率（RPS），但用户一天发 10000 条消息和发 10 条消息在系统层面没有区分
3. **无法分层定价**：如果未来有免费/付费套餐，当前架构完全无法支持"免费用户 100MB 存储、付费用户 10GB"的模型
4. **无用量可见性**：用户无法看到"我已使用多少存储"、"本月发了多少消息"

### 代码级证据

**证据 1：File 和 Message 领域模型完全无配额字段**

```go
// internal/domain/file.go
type File struct {
    ID          ID
    OwnerUID    string
    Name        string
    Size        int64       // 有这个字段但仅用作元数据，不作为配额累加器
    MimeType    string
    StorageKey  string
    Checksum    string
    // … 无 QuotaExceeded 检查
}
```

`Size` 字段存在，但从未在任何 service CRUD 操作中被累加到用户的存储用量计数器。整个代码库没有任何 `sum(size) where owner_uid = ?` 的查询或在 Create 前的配额检查。

```go
// internal/service/file.go — Create
func (s *fileService) Create(ctx context.Context, name, mimeType string, size int64, reader io.Reader, visibility domain.Visibility) (*domain.File, error) {
    // … 没有检查 user_storage_used + size > quota
    // 没有读取任何配额配置
}
```

**证据 2：没有配额表或计数器**

```bash
# 所有 22 份迁移 SQL 中：
# migrations/000001_create_files.up.sql — 文件表，无用户存储累加器
# migrations/000002_create_messages.up.sql — 消息表，无用户消息计数器
# 没有任何 resource_quotas、usage_meters、或者类似的表
```

**证据 3：HTTP handler 的 413 仅检查单文件尺寸，而非用户累计用量**

```go
// internal/service/file.go
// WithMaxUploadBytes 限制的是单个文件大小，不是用户的总存储
// 配置项 max_upload_bytes: 52428800 仅限制单次上传
```

这是"带宽控制"，不是"总量控制"。

**证据 4：没有任何用量统计的 API 端点**

```go
// internal/handler/http/ — 所有 handler 文件
// 没有 GET /v1/usage 或类似的端点
// 没有 GET /v1/storage/usage
```

### 产品经理视角

**用户价值**：用量仪表盘是用户对自己数据的"拥有感"的基础。知道"我有 500MB 文件，其中 200MB 是照片"让用户感觉在管理数据，而不是在填一个无底洞。对于消息，"已发送 1,247 条消息"有社交价值（Snapchat 的 streaks 就是基于此）。

**业务价值**：没有配额，就没有分层的商业模型。这是从"免费工具"到"付费平台"的门槛功能。

**风险**：严格配额可能导致用户流失（"因为超额导致我无法上传重要文件"）。所以需要软性配额（告警）和硬性配额（拒绝）的双层设计。

### 架构师视角

**建议实现路径**：
1. 增加 `user_usage` 表（uid, storage_bytes, message_count, file_count, updated_at），在 service 层的 Create/Delete 操作中原子更新计数器
2. 在 `configs/config.yaml` 增加 `quotas` 段（默认值适用所有用户，支持按 uid 覆盖）
3. 增加 `GET /v1/usage` 端点（返回当前用量 + 配额上限 + 百分比）
4. 在 Create 操作前检查配额，超额返回 `429 QuotaExceeded`（与 rate limit 共享状态码语义但区分 error code）
5. 增加 `OverageThreshold` 配置——达到 80% 时在响应头加 `X-Usage-Warning`

**边界情况**：
- **存储去重**（content-dedup）：去重文件共享同一 storage_key，但用户视角应该算作各自占有空间？建议物理去重但逻辑按文件大小独立计费（用户看到的是"我的 100MB 文件"，不是"存储后端只存了一份所以我不占空间"）
- **硬删除 vs 软删除**：配额应在软删除时释放还是硬删除时？建议软删除时释放（用户期望"删除后就腾出空间了"）
- **并发一致**：使用 `UPDATE user_usage SET storage_bytes = storage_bytes + ? WHERE uid = ? AND storage_bytes + ? <= quota_bytes` 原子条件更新

---

## 方向二：群组会话与频道系统（Group Conversations & Topic Channels）

### 问题陈述

当前消息系统严格限定为 **1:1 私信**模型（`from_uid` ↔ `to_uid`）。这是消息系统最基本的层级，但完全不能满足以下场景：

- **群聊**：3 个以上用户讨论一个主题
- **频道/房间**：持续的、主题驱动的多人讨论（类似 Slack 频道、Discord 房间）
- **广播**：一个用户向多个关注者发送消息
- **客户支持会话**：多个客服 vs 一个用户

### 代码级证据

**证据 1：Message 领域模型只有两个参与者字段**

```go
// internal/domain/message.go
type Message struct {
    ID        ID
    FromUID   string    // 发送者——只有一个！
    ToUID     string    // 接收者——只有一个！
    Kind      MessageKind
    Content   string
    // … 没有 ConversationID, 没有 ParticipantCount, 没有 GroupID
}
```

`FromUID` + `ToUID` 的二元组模型天然不支持 N 方会话。每条消息只能有一对参与者。

**证据 2：Inbox 和 Conversation 端点基于 1:1 模型**

```go
// internal/service/message.go (推断的签名)
func (s *messageService) Conversation(ctx context.Context, peerUID string) ([]*domain.Message, string, error)
// Conversation 的语义是"当前登录用户和 peerUID 之间的所有消息"
// 不存在 GroupConversation(ctx, conversationID string)
```

**证据 3：没有会话元数据表**

```bash
# 所有消息相关迁移：
# 000002_create_messages — 只有 messages 表
# 000011_message_edited_at — 加了 edited_at 列
# 000013_message_reactions — 加了 reactions 表
# 没有任何 conversations、group_members、channel_subscriptions 表
```

**证据 4：通知架构也只考虑了 1:1**

```go
// internal/notification/dispatcher.go
type messageSentPayload struct {
    MessageID string `json:"message_id"`
    FromUID   string `json:"from_uid"`
    ToUID     string `json:"to_uid"`     // 只有一个接收者
}
```

当 `ToUID` 是一个群组时，需要多个 notification 行（每个成员一个），但当前 dispatcher 只产生一个。

### 产品经理视角

**用户价值**：群聊是消息系统的核心功能——几乎所有主流消息平台（WhatsApp、Telegram、微信）的用户活跃度统计中，群聊消息占比超过 60%。没有群聊，aero-id 在"消息"功能上只是一个站内信系统。

**业务价值**：群聊解锁的用例包括：团队协作、兴趣社区、客户群发、直播聊天室。这些都是有独立商业价值的场景。

**风险**：群聊增加了内容审核的复杂度（群成员管理、垃圾消息防控、敏感内容过滤）、存储成本（群消息的 fan-out 策略）和运营成本（群举报与仲裁）。这些都需要配套投入。

### 架构师视角

**建议实现路径**：

**Phase 1 —— 群会话核心（最小可行）**：
1. 增加 `conversations` 表（id, type[1:1|group], title, created_by, created_at）
2. 增加 `conversation_members` 表（conversation_id, uid, role[member|admin], joined_at）
3. message 表增加 `conversation_id` 可选外键（向后兼容 1:1）
4. 群聊消息的 outbox fan-out：创建一条群消息 → 为每个成员生成 notification 行

**Phase 2 —— 会话管理 API**：
- `POST /v1/conversations` 创建群聊
- `POST /v1/conversations/:id/members` 添加/移除成员
- `GET /v1/conversations` 列出用户的会话列表（含最后一条消息摘要）
- `GET /v1/conversations/:id/messages` 群聊消息流

**Phase 3 —— 增强功能**：
- 消息 @mention（`@user` → 针对特定成员的通知）
- 成员离开/加入的系统消息
- 会话置顶、静音、退出

**关键设计决策**：通知 fan-out 采用**写时扩散**（每条群消息为每个成员写一条 notification）还是**读时扩散**（统一通知，成员各自拉取）？
- 小群（<50 人）：写时扩散，简单直接
- 大群（>50 人）：读时扩散或混合策略

建议 Phase 1 使用写时扩散（上限 100 人/群），Phase 2 引入读时扩散优化。

---

## 方向三：自动化内容审核管线（Automated Content Moderation Pipeline）

### 问题陈述

aero-id 有基本的评论举报/审核机制，但完全依赖**人工处理**——没有一个自动化管道来处理常见的滥用场景：

1. **无垃圾消息检测**：用户可以批量发送相同内容的消息/评论，系统无感知
2. **无图片审核**：文件上传不做 NSFW/暴力内容检测  
3. **无文本毒性分析**：辱骂、仇恨言论、PII（个人身份信息）没有自动标记
4. **无用户信誉系统**：没有基于行为的用户评分来优先处理高风险的举报

### 代码级证据

**证据 1：CommentReport 只存举报，不做分析**

```go
// internal/domain/comment.go
type CommentReport struct {
    ID          ID
    CommentID   ID
    ReporterUID string
    Reason      string        // 用户填写的举报原因
    Status      ReportStatus  // open / resolved / dismissed
    ResolvedAt  *time.Time
    ResolvedBy  string
    // 没有 AutoAnalysisResult, 没有 SpamScore, 没有 ToxicityScore
}
```

举报被存储后，唯一的处理路径是管理员手动调用 `ResolveReport`：

```go
// internal/comment/service.go (推断)
func (s *commentService) ResolveReport(ctx context.Context, reportID domain.ID, action string) error {
    // 验证管理员权限 → 更新 report status → 记录 audit log
    // 没有自动触发任何 content analysis
}
```

**证据 2：文件上传不做内容检查**

```go
// internal/domain/file.go
type File struct {
    // Name, Size, MimeType, Checksum — 这些是文件元数据
    // 没有 ContentFlags, 没有 ModerationStatus, 没有 ScanResult
}
```

虽然文件上传做了 MIME type allow-list，但这只防止了**文件类型**层面的风险，不检查文件**内容**。一个 `.jpg` 文件可以是合法图片，也可以是 NSFW 内容。

**证据 3：消息和评论创建没有滥用检测钩子**

```go
// internal/service/message.go — Send
func (s *messageService) Send(ctx context.Context, toUID, content string, kind domain.MessageKind) (*domain.Message, error) {
    // 检查 block → 创建消息 → 写入 outbox
    // 没有垃圾检测，没有重复内容检查，没有频率分析
}
```

```go
// internal/service/comment.go — Create
func (s *commentService) Create(ctx context.Context, targetType domain.TargetType, targetID domain.ID, content string, parentID *domain.ID) (*domain.Comment, error) {
    // 验证嵌套 → 创建评论 → 更新 reply_count → 写入 outbox
    // 没有内容审核
}
```

**证据 4：没有用户信誉或滥用计数器**

```go
// 整个 internal/ 目录 —— grep "reputation\|spam\|abuse\|trust\|flag\|suspension\|shadow.ban"
// 没有任何用户级别的滥用跟踪系统
```

### 产品经理视角

**用户价值**：间接但巨大——用户可能不会直接"看到"审核系统，但他们会因为**没有**审核系统而离开（被骚扰、看到垃圾/仇恨内容）。在 2026 年，内容安全已经是用户选择平台的基本门槛，而非加分项。

**业务价值**：
- **法律合规**：越来越多司法管辖区（DSA 欧盟、IT 法案印度、网络信息内容生态治理规定中国）要求平台有主动内容审核机制
- **品牌安全**：应用商店和广告主会评估平台的内容安全成熟度
- **社区健康**：经过 moderation 的社区留存率显著高于无审核社区

**风险**：过度自动化审核可能导致误伤（false positive），引起用户不满。建议采用"自动标记 + 人工复核"的混合模式。

### 架构师视角

**建议架构**：审核系统应该作为**插件式管道（pluggable pipeline）**集成到写路径中：

```
写请求 → [Service 层] → [审核管道] → [仓库持久化]
                         ├── 垃圾检测（文本相似度 + 频率分析）
                         ├── 毒性检测（可选外部 API：Perspective API / 自建模型）
                         ├── 图片审核（可选外部 API：AWS Rekognition / 自建 NSFW 模型）
                         └── 用户信誉查询 → 加权评分
                               → 通过：正常持久化
                               → 标记：写入 + flag 状态 + 通知管理员
                               → 拒绝：返回 403 ContentRejected
```

**Phase 1（低投入）**：纯文本规则
- 正则匹配常见垃圾模式（URL 白名单、重复字符检测、ALL CAPS 检测）
- 重复内容检测（`content` 的 Simhash/MinHash，跨用户去重）
- 速率触发（一个用户 1 分钟内创建 N 条评论 → flag）

**Phase 2（中等投入）**：引入信誉系统
- `user_reputation` 表（uid, report_count, flagged_count, trust_score, updated_at）
- 基于历史行为的动态阈值

**Phase 3（高投入）**：外部 API 集成
- 文本毒性评分（Tencent / Google / 自部署模型）
- 图片 NSFW 检测
- 可配置：`moderation.provider: "none|local|external"`
- 审核结果作为 outbox 事件分发（供管理员 dashboard 消费）

---

## 方向四：数据可移植性与批量操作（Data Portability & Bulk Operations）

### 问题陈述

aero-id 当前所有端点都是**单实体、游标分页**的。用户没有任何机制来：

1. **导出自己的全部数据**（GDPR 第 20 条数据可移植权要求）
2. **批量删除/归档**符合某条件的文件或消息
3. **跨实体数据聚合**（"下载我所有文件 + 消息 + 评论的 ZIP"）
4. **移转到另一个平台**（把自己从 SSO 导出的身份和数据关联起来）

### 代码级证据

**证据 1：所有 List 端点都是分页的，没有"导出所有"模式**

```go
// internal/handler/http/file.go — List
func (h *FileHandler) List(c *gin.Context) {
    page := httpx.PageFromQuery(c)
    // limit 被钳制在 [1, 100]，默认 20
    // 不可能传 limit=0 表示"全部"
}
```

```go
// internal/pkg/httpx/httpx.go
func PageFromQuery(c *gin.Context) domain.Page {
    limit := 20              // 硬编码默认
    // limit 通过 parseQueryLimit 钳制在 MaxPageLimit (100)
    // 没有 "export_mode" 或 "unlimited" 参数
}
```

**证据 2：没有数据导出/归档的域概念**

```go
// internal/domain/ — 搜索所有文件
// 没有 Export, Archive, DataPortability 相关的结构体、枚举或接口
```

**证据 3：没有后台异步任务系统来支持耗时操作**

大量数据导出（比如：导出 10000 条消息）不可能在一个 HTTP 请求内完成。但 aero-id 没有异步任务队列——outbox 系统用于事件分发，不是任务执行。

```go
// internal/outbox/outbox.go
// outbox.Event 的语义是"领域事件"，不是"后台任务"
// 没有 Task/TaskQueue 类型，没有进度（progress%）字段
```

**证据 4：SSO 用户缓存不支持跨用户批量查询用于导出**

```go
// internal/sso/user_cache.go
// UserCache.GetUsers 用于在列表响应中解析 uid → 显示名
// 但不是为"导出用户的所有关联数据"设计的
```

### 产品经理视角

**用户价值**：数据可移植性是用户"拥有"数据的最强表达。当用户问"我怎样才能把我所有的照片从 aero-id 搬到其他平台"，如果答案是"逐张下载"，用户体验就是不合格的。

**合规价值**：GDPR、CCPA、LGPD 等隐私法规都规定了数据可移植权。没有导出功能，在处理合规请求时需要手动操作——在规模化后会成为运营噩梦。

**产品价值**："导出我的数据"按钮是用户信任的象征。Dropbox、Google Takeout、Facebook 都有这个功能。它的存在本身就是一种质量信号。

### 架构师视角

**建议实现路径**：

**Phase 1 —— 准实时导出（小数据量）**：
- `GET /v1/export?type=messages` 直接返回 JSON stream（适用于 <1000 条记录）
- 支持 `Accept: application/json` 和 `Accept: application/x-ndjson`
- 包含所有元数据（时间戳、参与者 ID、引用关系）

**Phase 2 —— 异步导出（大数据量）**：
- 增加 `export_jobs` 表（id, uid, status[pending|running|completed|failed], entity_types[], filters, created_at, completed_at, download_url）
- `POST /v1/export-jobs` 创建导出任务
- `GET /v1/export-jobs/:id` 查询进度 + 下载链接
- 后台 worker（复用 outbox 机制或新的 `TaskRunner`）执行导出
- 导出文件存到 storage 后端，带 HMAC 签名 URL

**Phase 3 —— 批量操作**：
- `POST /v1/files/batch-delete` 批量删除（ids[]）
- `POST /v1/files/batch-update` 批量修改 visibility / meta
- `POST /v1/messages/batch-delete` 批量删除消息（by date range）

**关键技术挑战**：
1. **数据关联**：export 需要维护实体间的引用关系（消息→文件附件、评论→父评论）
2. **大文件处理**：硬盘/内存管理，超时控制
3. **并发限制**：防止大量导出任务压垮数据库
4. **存储管理**：完成的导出文件有 TTL，过期自动清理

---

## 方向五：开发者生态与平台集成（Developer Ecosystem & Platform Integration）

### 问题陈述

aero-id 作为"数据平台"，当前完全缺乏开发者生态基础设施：

1. **无 Client SDK**：41 个 gRPC RPC + HTTP API 对等，但开发者需要自己写 proto client、手动处理认证、自己解析错误
2. **无 API 密钥系统**：除了 SSO Bearer token（用户级），没有机器对机器的 service token
3. **无 API 文档/沙箱**：没有 OpenAPI/Swagger 文档，没有 gRPC reflection 外的人类可读文档
4. **无用户可配置的 Webhook**：虽然 outbox 有服务端 webhook 配置，但用户不能订阅自己的 webhook
5. **无速率限制层级**：所有用户共享同一限流配置，无法按应用/API 密钥区分优先级

### 代码级证据

**证据 1：认证只支持 Bearer token（用户委托），不支持 API 密钥**

```go
// internal/middleware/auth.go
func Auth(v TokenValidator) gin.HandlerFunc {
    return func(c *gin.Context) {
        token := bearerToken(c.GetHeader("Authorization"))
        // 只支持 bearer token → SSO.ValidateToken
        // 不支持 X-Api-Key header → ApiKeyStore.Lookup
    }
}
```

```go
// internal/middleware/ratelimit.go
func rateLimitKey(c *gin.Context) string {
    if uid, ok := service.ActorFrom(c.Request.Context()); ok {
        return "uid:" + uid  // 按 uid 限速，无法按 API key 分配独立配额
    }
    return "ip:" + c.ClientIP()
}
```

**证据 2：没有 proto 文件的版本管理策略**

```bash
# api/proto/aero_id/v1/aero_id.proto
# package aero_id.v1;
# 版本号嵌在 proto package 中，但没有版本协商机制
# 客户端和服务端必须同时更新 proto。没有任何向后兼容性保证
```

**证据 3：outbox webhook 是服务端配置的，用户不可配置**

```go
// internal/app/app.go
if cfg.Outbox.Webhook.URL != "" {
    // webhook 在 app 启动时静态配置
    // 没有 REST API 让用户管理自己的 webhook 订阅
}
```

```go
// internal/outbox/webhook.go
type WebhookConfig struct {
    URL        string            // 单一 URL，来自配置
    SigningKey string            // 单一 key
    EventTypes []string          // 全局过滤
    // 没有 per-user webhook 的概念
}
```

**证据 4：没有 CORS 的细粒度控制（用于第三方客户端应用）**

```go
// internal/middleware/cors.go
// CORS 中间件对所有来源开放或单一来源，没有 per-client 粒度
// 没有支持 OAuth2-like 的授权码流程
```

### 产品经理视角

**用户价值**：开发者生态是平台从"工具"进化为"平台"的标志。Stripe、Twilio、Slack 的爆发式增长都始于公开 API + SDK + Webhook。

**业务价值**：
- **第三方集成**：其他服务可以通过 API/webhook 与 aero-id 集成（例如：一个 CMS 系统自动在 aero-id 中存储用户上传的文件）
- **自定义客户端**：用户可以用 SDK 构建自己的客户端体验
- **自动化工作流**：通过 webhook 触发其他服务（如：收到文件 → 自动触发转码流水线）

**风险**：开放的 API 表面增加攻击面和维护成本。版本管理、废弃策略、迁移文档都需要投入。

### 架构师视角

**建议实现路径**：

**Phase 1 —— API 密钥 + 限流分层**：
- 增加 `api_keys` 表（id, uid, name, key_hash, rate_limit_rps, rate_limit_burst, last_used_at, created_at, expires_at）
- 认证中间件支持 `X-Api-Key` header（通过 `bcrypt` 验证 key_hash）
- 限流 key 改为 `api_key:{key_id}` 或 `uid:{uid}`，支持 API 密钥级别独立配额

**Phase 2 —— 用户可配置的 Webhook**：
- 增加 `webhook_subscriptions` 表（id, uid, url, signing_key, event_types[], filters, paused, last_success_at, last_failure_at）
- `POST /v1/webhooks` 创建订阅
- `POST /v1/webhooks/:id/test` 发送测试事件
- Dispatcher 在服务级 webhook 之外，查询用户级 webhook 订阅并分发
- 失败重试 + 自动暂停（连续 10 次失败 → paused = true）

**Phase 3 —— SDK 生成与 API 治理**：
- 使用 `protoc-gen-openapi` 从 proto 生成 OpenAPI 规范
- 自动生成 TypeScript/Go/Python client SDK（作为 CI 步骤）
- 引入 API 版本协商（Accept: application/vnd.aero-id.v2+json）
- 发布 API changelog + 弃用策略

**Phase 4 —— OAuth2 授权流程**：
- 允许第三方应用代表用户请求特定权限（https://oauth.net/2/）
- 支持 `scope` 参数（files:read, messages:write, notifications:read）
- `/oauth/authorize` + `/oauth/token` 端点

---

## 优先级建议

| # | 方向 | 类型 | 优先级 | 估算投入 | 为什么是这个优先级 |
|---|------|------|--------|----------|-------------------|
| 1 | **资源配额与用量计量** | 基础平台 | P1 | 低-中 | 安全基线——没有配额，平台无法防止恶意使用，也无法支持商业模型 |
| 2 | **自动化内容审核管线** | 安全/合规 | P1 | 中-高 | 法律合规风险最高，且直接影响社区质量。Phase 1（纯文本规则）可以快速落地 |
| 3 | **群组会话与频道系统** | 产品功能 | P2 | 高 | 最大用户价值提升，但重构量也最大。需要仔细设计向前兼容 |
| 4 | **开发者生态与平台集成** | 平台能力 | P2 | 中-高 | 高战略价值但当前需求优先级低于安全和产品功能 |
| 5 | **数据可移植性与批量操作** | 合规/产品 | P3 | 中 | 合规必需但可以先用准实时 JSON stream 快速交差，异步导出后置 |

### 依赖关系

```
配额系统 (P1)      — 独立，无阻塞依赖
内容审核 (P1)       — 需要配额系统提供用户信誉基础？否，可以独立建设
群组会话 (P2)       — 依赖消息系统的重构，需要配额系统来限制群数量/成员数
开发者生态 (P2)     — 依赖配额系统（API key 限流）
数据可移植性 (P3)   — 最好在群组会话之后（导出需要支持群消息）
```
