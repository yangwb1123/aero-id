# aero-id — 全局代码扫描：五个高价值但未被任何既有分析覆盖的扩展方向

> **日期**: 2026-07-10  
> **视角**: 资深架构师 / 产品经理  
> **方法**: 全代码库逐文件扫描（`internal/` 全部 ~25 个包、150+ 源文件、22 份迁移 SQL、双传输层 handler、gRPC proto）后，与 `docs/requirements/` 下 50+ 份已有分析文档的每一条章节标题和核心论据做逐条交叉比对，确保以下五个方向在全部既有分析中 **零系统性覆盖**。不编写任何代码。

---

## 前置：交叉验证结论

aero-id 在以下领域已被充分分析（累计 ~90+ 独立方向），故本文不再涉及：

| 已覆盖领域（不完全列表） | 对应文档数量 |
|---|---|
| 文件预览、转码、病毒扫描 | 10+ |
| 全文搜索深度（相关性、可见性、分片） | 8+ |
| 通知系统（推送、FCM/APNs、静默时段） | 15+ |
| 跨实例协调 / 分布式限流 | 10+ |
| API 版本化 / 生命周期 | 5+ |
| Dead Letter 队列管理 | 5+ |
| 存储后端迁移 / 分层 | 8+ |
| i18n / 多语言 | 5+ |
| Event Sourcing 深化 | 3+ |
| 审计日志扩展 | 8+ |
| SSE 重连 / 回放 | 6+ |
| SSO 缓存 / 断路器 | 10+ |
| 文件 Outbox 事件 | 3+ |
| gRPC 反射访问控制 | 3+ |
| 消息已读回执 | 4+ |
| 用户阻塞级联影响 | 3+ |

---

## 方向一：消息 Reactions 和评论 Likes 不产生通知——社交信号缺失

### 为什么需要它

当前系统中有两种"反应"机制——评论点赞（`comment_likes`）和消息回应（`message_reactions`）——但它们都是 **静默操作**：执行 Like/Unlike 或 React/Unreact 后，被赞消息的作者和评论的作者**不会收到任何通知**。

这是一个产品层面的社交信号缺失。在即时通讯场景中，"某人给我的消息点了赞"是最基础的社交确认之一；在评论系统中，"有人赞了我的评论"是驱动用户参与的核心反馈环。

### 代码级证据

**消息 Reactions 无 outbox 事件：**

```go
// internal/message/service.go:253-281
func (s *service) ReactToMessage(ctx context.Context, messageID domain.ID, actorUID string) error {
    // ... 权限验证 ...
    if err := s.messages.ReactToMessage(ctx, messageID, actorUID); err != nil {
        // ...
    }
    return nil  // <-- 没有 outbox.NewEvent，没有通知
}
```

对比同文件中的 Send（有事件）和 MarkRead（有事件）：

```go
// internal/message/service.go:153
evt := outbox.NewEvent("message", m.ID.String(), "message.sent", ...)  // ✅ 发送有事件
// internal/message/service.go:205
evt := outbox.NewEvent("message", m.ID.String(), "message.read", ...)   // ✅ 已读有事件
```

**评论 Likes 无 outbox 事件：**

```go
// internal/comment/service.go:198-220
func (s *serviceImpl) react(ctx context.Context, id domain.ID, like bool) (int, error) {
    // ... 权限验证 ...
    if like {
        count, err = s.comments.Like(ctx, id, actor)
    } else {
        count, err = s.comments.Unlike(ctx, id, actor)
    }
    return count, nil  // <-- 没有 outbox.NewEvent
}
```

对比同文件中的 Create（有事件）：

```go
// internal/comment/service.go:122
evt = outbox.NewEvent("comment", c.ID.String(), "comment.replied", ...)  // ✅ 回复有事件
```

**NotificationDispatcher 只处理 3 种事件类型：**

```go
// internal/notification/dispatcher.go:82-88
func (d *Dispatcher) notificationFor(e *outbox.Event) *domain.Notification {
    switch e.EventType {
    case "message.sent":     // 消息发送
    case "message.read":     // 消息已读
    case "comment.replied":  // 评论回复
    // 缺: "message.reacted", "comment.liked"
    }
}
```

### 影响规模

- 所有 `message_reactions` 操作（有独立表 + 迁移 000013）对消息作者完全透明
- 所有 `comment_likes` 操作（有独立表 + like_count 列）对评论作者完全透明
- 用户只能通过轮询发现有人互动了自己的内容
- 这是 ROI 极高的产品功能——表结构已就绪，outbox pipeline 已就绪，只需加两个 event type

### 建议方案

1. **在 `ReactToMessage` 中增加 outbox 事件**：
   ```go
   evt := outbox.NewEvent("message", messageID.String(), "message.reacted", map[string]any{
       "message_id": messageID.String(),
       "actor_uid":  actorUID,
       "owner_uid":  m.FromUID, // 消息作者
   })
   // Append event in the same transaction as the reaction
   ```

2. **在 `react`（Like/Unlike）中增加 outbox 事件**：
   ```go
   evt := outbox.NewEvent("comment", id.String(), "comment.liked", map[string]any{
       "comment_id": id.String(),
       "actor_uid":  actor,
       "owner_uid":  c.OwnerUID, // 评论作者
   })
   ```

3. **NotificationDispatcher 新增两个 handler**：`notificationMessageReacted` 和 `notificationCommentLiked`

4. **注意去重**：Unlike/Unreact 不应产生通知（仅在 `like=true` 时触发）

**影响**: 低（~2 天）。表和架构完全就绪。对现有用户的唯一影响是新增一些通知行，不会破坏现有行为。

---

## 方向二：搜索缺乏跨实体类型相关性排序与结果去重

### 为什么需要它

当前的搜索实现（`internal/search/search.go`）按实体类型分别查询，然后将结果**简单拼接**：

```
用户搜 "hello"
  → 搜索 messages（3 条结果）
  → 搜索 comments（2 条结果）
  → 搜索 files（1 条结果）
  → 返回 [msg1, msg2, msg3, cmt1, cmt2, file1]
```

这导致三个产品问题：

1. **相关性错乱**：`msg1` 可能是 90% 匹配，但排在 `cmt2`（20% 匹配）之前只是因为它是 message 类型。用户看到的是"按类型分组"而非"按相关度排序"的结果。
2. **界限模糊**：用户不关心"这个消息是评论还是文件"，只关心"有没有找到我要的"。结果应该按相关性统一排序，而非按类型分区。
3. **无去重**：如果同一内容既出现在消息中又出现在文件的元数据中，搜索结果中会出现重复条目。

### 代码级证据

```go
// internal/search/search.go:47-83
func (s *searchService) Search(ctx context.Context, query string, kinds []domain.SearchKind, limit int) ([]SearchHit, error) {
    // ...
    var hits []SearchHit
    for _, kind := range kinds {
        var err error
        hits, err = s.searchKind(ctx, actor, query, limit, kind, hits)  // 顺序追加
        // ...
    }
    return hits, nil  // 简单的 append，无排序，无去重
}
```

```go
// internal/search/search.go:70-90
func (s *searchService) searchKind(ctx context.Context, actor, query string, limit int, kind domain.SearchKind, hits []SearchHit) ([]SearchHit, error) {
    switch kind {
    case domain.SearchKindMessages:
        msgs, _ := s.messages.SearchMessages(ctx, actor, query, limit)
        for _, m := range msgs {
            hits = append(hits, SearchHit{Kind: domain.SearchKindMessages, Message: m})
        }
    // ... comments, files 同理
    }
    return hits, nil
}
```

各 repository 使用 `ts_rank` 进行排序，但在 `search.go` 中被丢弃：

```go
// internal/repository/comment.go (search)
ORDER BY ts_rank(search_vector, plainto_tsquery('simple', ?)) DESC
// 这个 rank 值在 repository 层被计算，但在 service 层被丢弃
```

### 影响

- 搜索用户体验差：关键结果可能淹没在低相关结果中
- 搜索结果的极限值为 `limit` 个，但由于每个 kind 都查 `limit` 条，实际返回结果可能多达 `3 × limit` 条
- 无分页 cursor：超过 limit+1 的结果被静默截断

### 建议方案

1. **传递 rank 值**：修改 repository 层返回类型，在每个 hit 上携带 `ts_rank` 分值（或归一化的相关性分数）
2. **跨类型归并排序**：在 `search.go` 中将所有 hits 按 rank 降序排列，取前 `limit` 个
3. **去重**：按内容 hash（或 `subject_type + subject_id`）去重
4. **可考虑合并 SQL**：使用 PostgreSQL 的 UNION ALL + ORDER BY + LIMIT 在一个查询中完成跨表搜索和排序

**估算**: 中（3-5 天）。关键在于不破坏现有 FTS 基础设施的前提下，将 rank 值从 repository 层传递到 search service 层。

---

## 方向三：文件分享授权无过期机制——分享是永久的

### 为什么需要它

当前的文件分享系统（`internal/file/service_share.go` + `internal/repository/file_share.go`）支持授予和撤销读权限，但**一旦授予，访问权限是永久的**，直到手动撤销。

这在以下场景中成为安全缺口：

- **临时协作文档**：用户想分享一个文件"给对方看 24 小时"，当前只能手动记着去撤销
- **时间敏感资料**：用户想分享一份合同或报价单，"7 天后自动失效"
- **合规要求**：某些法规要求对共享数据的访问必须有时间限制（如 GDPR 的数据最小化原则）
- **用户疏忽**：用户可能忘记撤销分享，导致敏感文件长期暴露

### 代码级证据

```go
// internal/file/service_share.go:12-40
func (s *fileService) ShareFile(ctx context.Context, id domain.ID, granteeUIDs []string) error {
    // ...
    if err := s.shares.Grant(ctx, id, f.OwnerUID, grantees); err != nil {
        // ...
    }
    return nil  // 永久授权，无过期时间参数
}
```

```go
// internal/repository/file_share.go — Grant 方法
func (r *fileShareRepo) Grant(ctx context.Context, fileID domain.ID, ownerUID string, granteeUIDs []string) error {
    // INSERT ... ON CONFLICT DO NOTHING
    // 不涉及过期时间列
}
```

```sql
-- migrations/000006_create_file_shares.up.sql
CREATE TABLE IF NOT EXISTS file_shares (
    file_id    UUID NOT NULL REFERENCES files(id) ON DELETE CASCADE,
    uid        TEXT NOT NULL,       -- grantee
    granted_by TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (file_id, uid)
);
-- 缺少 expires_at 列
```

```go
// internal/file/file.go:30-38
type ShareRepository interface {
    Grant(ctx context.Context, fileID domain.ID, ownerUID string, granteeUIDs []string) error
    Revoke(ctx context.Context, fileID domain.ID, granteeUID string) error
    IsSharedWith(ctx context.Context, fileID domain.ID, uid string) (bool, error)
    ListFilesSharedWith(ctx context.Context, uid string, page domain.Page) ([]*File, string, error)
    // 缺少：GrantWithTTL、ListExpiredShares、CleanupExpiredShares
}
```

### 建议方案

1. **扩展 `file_shares` 表**，增加 `expires_at TIMESTAMPTZ`（NULL = 永不过期，向后兼容）
2. **扩展 `Grant` 方法**（或新增 `GrantWithTTL`）接受可选的 TTL 参数
3. **后台过期清理**：在 GC sweeper 或独立 ticker 中定期删除 `expires_at < NOW()` 的行（或使用 SQL 过滤 + 软过期）
4. **读取时过滤**：在 `IsSharedWith` 中增加 `AND (expires_at IS NULL OR expires_at > NOW())` 条件
5. **HTTP/gRPC 端点**：在分享 API 中增加可选的 `ttl` 参数（如 `?ttl=24h`）

**改动量**:
- 迁移：ALTER TABLE + 索引
- Repository：约 20 行 SQL 变更
- Service：增加 TTL 参数传递
- Handler：解析可选的 `ttl` 查询参数

**估算**: 低-中（2-3 天）。完全向后兼容。

---

## 方向四：通知列表每次查询都调用 SSO 批量解析用户——无本地缓存层

### 为什么需要它

通知列表（`GET /v1/notifications`）在返回前会解析所有 `ActorUID` 为用户信息。当前的实现每次都调用 SSO 的 `GetUsers` RPC，即使同一组 actor 在短时间内已被解析过。

### 代码级证据

```go
// internal/notification/service.go:24-35
func (s *notificationService) List(ctx context.Context, page domain.Page) (*NotificationList, error) {
    // ...
    ns, next, err := s.notifs.ListByRecipient(ctx, actor, page)
    // ...
    users, err := enrichUsers(ctx, s.dir, actorUIDs(ns))  // 每次都调用 SSO GetUsers
    // ...
}
```

```go
// internal/comment/service.go （enrichUsers 的通用定义）
func enrichUsers(ctx context.Context, dir UserDirectory, uids []string) (map[string]sso.User, error) {
    if len(uids) == 0 {
        return map[string]sso.User{}, nil
    }
    return dir.GetUsers(ctx, uids)  // 每次都走 gRPC
}
```

对比消息系统（`internal/message/service.go` 的 `Inbox`）和评论系统（`internal/comment/service.go` 的 `List`）——它们使用完全相同的模式，全部直接调用 SSO GetUsers。

**问题放大因素**：

- SSO 的 `GetUsers` 本身是有缓存的（`internal/sso/user_cache.go`），但缓存只覆盖同一个 `uid` 集合的重复查询。如果通知列表中包含 `[uid1, uid2]`，而评论列表中包含 `[uid2, uid3]`，则 `uid2` 被查询两次
- 通知分页滚动：用户滚动加载更多通知 → 每页都调用一次 GetUsers
- 当前无用户信息预加载机制（warmup 只做连接探测，不预热用户数据）

### 影响

- 不必要的 SSO 往返延迟（即使 SSO 有用户缓存，每次 List 请求都有 gRPC 调用延迟 ~1-5ms）
- SSO 负载：每个列表刷新都产生批量用户查询请求
- 高并发下放大：100 个并发通知列表请求 → 100 个 SSO GetUsers RPC

### 建议方案

1. **引入本地 `UserCache` 装饰器**：在 `UserDirectory` 接口之上包一层 TTL 缓存（复用 `sso.UserCache` 的结构或独立包装）
   ```go
   type cachedUserDir struct {
       inner UserDirectory
       cache *sso.UserCache  // 复用已有的 TTL 缓存
   }
   
   func (c *cachedUserDir) GetUsers(ctx context.Context, uids []string) (map[string]sso.User, error) {
       // 1. 从本地缓存查找命中的 uid
       // 2. 只将未命中的 uid 委托给 inner.GetUsers
       // 3. 将结果回填缓存
   }
   ```
2. **在 `app.go` 的 wiring 中应用**：将 `ssoClient` 包装后注入到所有服务
3. **缓存配置**：复用现有的 `UserCacheTTL` 配置项，或新增独立的 `local_user_cache_ttl`

**影响**: 低（1-2 天）。架构完全兼容——`UserDirectory` 接口本身就是抽象的，装饰器模式无需改动现有代码。

---

## 方向五：批量资源操作 API 缺失——用户管理成本高

### 为什么需要它

当前所有资源操作都是单实体粒度的。对于有 N 个文件、N 条消息的用户来说，以下操作需要 N 次 API 调用：

1. **批量删除文件**：删除 100 个文件需要循环调用 `DELETE /v1/files/:id` × 100
2. **批量标记通知已读**：目前只有 `MarkAllRead`，但缺少批量标记指定通知已读（如列表中的前 20 条）
3. **批量更新消息已读**：只有 `MarkConversationRead`，缺少批量标记特定消息 ID 列表已读
4. **分页链式操作**：列表 → 逐个处理 → 分页 → 再逐个处理 → 循环

### 代码级证据

```go
// internal/file/service_crud.go:43-58
func (s *fileService) Delete(ctx context.Context, id domain.ID) error {
    actor, err := domain.MustActor(ctx)
    // ... 验证单个文件的所有权和引用计数 ...
    return nil  // 一次只能删除一个文件
}
```

```go
// internal/message/service.go:179-190
func (s *service) MarkRead(ctx context.Context, id domain.ID) error {
    // ... 只能标记单条消息已读
}
```

```go
// internal/notification/service.go:30-56
func (s *notificationService) MarkRead(ctx context.Context, id domain.ID) error {
    // ... 单条通知
}
func (s *notificationService) MarkAllRead(ctx context.Context) error {
    // ... 全部标记已读
    // 缺少：批量标记 N 条已读（指定 ID 列表）
}
```

```go
// internal/middleware/idempotency.go
// 幂等键仅支持单请求 -> 单响应。没有批量幂等语义。
```

### 影响

- **移动端场景**：手机用户经常需要批量清理通知（"标记所有为已读"或"标记这 20 条为已读"），当前只能全部标记或逐条标记
- **高延迟网络**：每个单独 API 调用的网络往返在移动网络下可能 ~100-200ms，100 条消息就是 10-20s
- **冲突窗口**：N 次独立 DELETE 操作之间，其他并发操作可能改变状态（例如文件被其他进程删除）

### 建议方案

1. **批量 DELETE /v1/files**：接受 `ids` 查询参数（逗号分隔的 UUID 列表），在单事务中批量软删除
   ```http
   DELETE /v1/files?ids=id1,id2,id3
   ```
2. **批量 MarkRead /v1/notifications**：新增端点接受通知 ID 列表，单事务批量标记
   ```http
   POST /v1/notifications/read
   { "ids": ["id1", "id2", "id3"] }
   ```
3. **批量 MarkRead /v1/messages**：扩展 MarkConversationRead 支持指定 ID 列表
4. **实现方案**：所有批量操作在 repository 层使用 `WHERE id IN (?)` + 单事务，确保原子性

**注意**：批量操作不应绕过原有的权限检查和块检查。在 service 层验证第一个元素的访问权限后，可以信任同列表的其他元素属于同一用户（由 repository 层的 `IN` + `owner_uid` 条件双重保证）。

**影响**: 中（3-5 天）。严格向后兼容——所有现有单实体 API 不变。

---

## 优先级建议矩阵

| # | 方向 | 影响 | 工作量 | 类型 | 推荐优先级 |
|---|------|------|--------|------|-----------|
| 1 | Reactions 通知（社交信号） | **高**（产品体验） | 低（2 天） | 功能 | **P1**（快速赢） |
| 2 | 搜索跨类型排序+去重 | **高**（搜索 UX） | 中（3-5 天） | 功能 | **P1** |
| 3 | 文件分享过期机制 | **中高**（安全） | 低（2-3 天） | 安全/功能 | **P2** |
| 4 | 通知用户信息本地缓存 | **中**（性能） | 低（1-2 天） | 性能 | **P2** |
| 5 | 批量资源操作 API | **中**（移动端 UX） | 中（3-5 天） | 功能/移动 | **P2** |

---

## 附录：与现有架构的一致性检查

| 方向 | 是否遵循单向依赖 | 是否复用 outbox | 是否复用现有 schema | 是否向后兼容 |
|------|-----------------|-----------------|-------------------|-------------|
| 方向一 | ✅ service 层新增 outbox event | ✅ 复用 NotificationDispatcher | ✅ `message_reactions` + `comment_likes` 已存在 | ✅ |
| 方向二 | ✅ search service 内部改动 | N/A | ✅ `ts_rank` 已在 SQL 中 | ✅ |
| 方向三 | ✅ repository 层 + migration | N/A | ✅ `file_shares` 表加列 | ✅（NULL = 永久） |
| 方向四 | ✅ service 层装饰器 | N/A | N/A | ✅ |
| 方向五 | ✅ handler → service → repository | N/A | N/A | ✅ |

---
