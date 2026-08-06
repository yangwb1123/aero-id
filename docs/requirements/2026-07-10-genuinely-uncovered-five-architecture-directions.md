# aero-id — 第九轮全量扫描：五个未被任何既有分析覆盖的高价值架构方向

> **日期**: 2026-07-10  
> **范围**: 全代码库交叉比对（`internal/` 全部 22+ 包、120+ 源文件、22 份迁移 SQL、双传输层 handler、`configs/`、`cmd/`、`api/proto/`）  
> **方法**: 逐文件通读后，与 `docs/requirements/` 下 **50 份**既有分析文档 + `docs/REVIEW_*` 下 **8 份**审查文档 + `ROADMAP.md` 的所有方向做逐条标题级和核心论据级交叉比对，确保以下五个方向在全部既有分析中**零系统性覆盖**  
> **视角**: 资深架构师 / 产品经理  
> **约束**: 不编写任何代码。每个方向附代码级证据与代码文件精确行号。

---

## 交叉验证方法

```
输入:
  - 50 份 docs/requirements/*.md  → ~80+ 个方向条目
  -  8 份 docs/REVIEW_*.md        → ~20 个方向条目
  -  1 份 ROADMAP.md              →  5 个方向 + 10+ 子项
  --------------------------------------------------------
  合并去重                         → ~90+ 个独立方向
  逐方向与当前代码比对覆盖度         → 确认以下 5 个方向在所有既有分析中零覆盖
```

每份已有文档都覆盖了特定的方向集合。逐一阅读后确认的已有主题包括（不完全列表）：SSE 重放 50 条上限 · 文件零 outbox 事件 · outbox relay 无超时 shutdown · 搜索超时硬编码 · config 跨字段校验缺失 · Block 抽象穿透文件分享 · 机器可读错误分类 · Session 隐式派生 · 遗留重复行类型 · 通知限流键表增长 · SSE Hub 单实例瓶颈 · Admin 角色 API 不完整 · 文件分享永久授权无过期 · 通知外带送达（邮件/推送） · 跨实例状态同步 · 移动端 API 优化（压缩、部分响应） · API 可消费性 · 软删除恢复 · 跨实体引用完整性 · 用户可见限流状态 · 优雅维护模式 · 实体变更历史 · 实时在线状态与打字指示器 · 消息召回/定时发送/自动销毁 · Bot 框架 · 资源计量与配额 · 多设备会话管理 · 文件静态加密 · 文件分享细化权限 · API 生命周期管理 · 异步管线可观测性 · 供应链安全 · 预签名上传内容检查 · 等等（累计 ~90+ 方向）。

本文档聚焦于上述 90+ 方向中**零覆盖**的 5 个方向。

---

## 方向一：评论点赞（like_count）物化计数器漂移——无定期校正机制

### 为什么需要它

aero-id 的评论点赞使用 `like_count` 作为**物化计数器**（materialized counter），在每次点赞/取消点赞时通过原子 SQL 更新做 ±1 操作。这种设计读写性能高，但存在一个经典问题：**计数器与源数据行（`comment_likes` 表）之间的差异会随着时间累积，且没有回填机制来校正。**

在纯正确性的角度，`like_count` 永远可以从 `SELECT COUNT(*) FROM comment_likes WHERE comment_id = ?` 推导。物化计数器只是缓存。但当前实现视其为权威数据源，从不验证它是否匹配实际行数。

### 代码级证据

```go
// internal/comment/repository.go:248-270
func (r *repo) Like(ctx context.Context, commentID domain.ID, uid string) (int, error) {
    var count int
    err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
        res := tx.Clauses(clause.OnConflict{DoNothing: true}).
            Create(&commentLikeRow{CommentID: commentID, UID: uid, CreatedAt: time.Now().UTC()})
        if res.RowsAffected == 1 {
            if err := tx.Model(&commentRow{}).Where("id = ?", commentID).
                UpdateColumn("like_count", gorm.Expr("like_count + 1")).Error; err != nil {
                return err
            }
        }
        // ...读回 like_count 返回
    })
}
```

```go
// internal/comment/repository.go:275-294
func (r *repo) Unlike(ctx context.Context, commentID domain.ID, uid string) (int, error) {
    // ...
    if res.RowsAffected == 1 {
        if err := tx.Model(&commentRow{}).Where("id = ?", commentID).
            UpdateColumn("like_count", gorm.Expr("GREATEST(like_count - 1, 0)")).Error; err != nil {
            return err
        }
    }
    // ...
}
```

### 漂移来源分析

1. **并发边界条件**：两个并发的 `Unlike` 请求各删一行，各做 `GREATEST(like_count - 1, 0)`。如果计数器恰好为 1，第一个请求将它设为 0，第二个请求将其固定在 0。不会负值，但可能的竞争窗口模式是：两个事务分别读取同一行并各自执行 `like_count - 1`。PostgreSQL 的 MVCC 确保 `UPDATE` 是行级的，所以这实际上无法同时成功——第一个 UPDATE 获取行锁后第二个会等待，最终结果正确。**但**如果代码更复杂（分两步：先读后写），竞争窗口就会打开。

2. **手动数据库操作**：运维人员或迁移脚本直接操作 `comment_likes` 表（例如清理某些用户的点赞）时，不会同步更新 `like_count`。这是最常见的漂移来源。

3. **部分事务失败**：如果创建点赞行成功但更新计数器的步骤失败（虽然当前实现在一个事务中，理论上不可能），或者相反的情况，计数器会漂移。

4. **数据回填 & 迁移**：当从旧系统迁移数据时，如果 `like_count` 被设置为一个近似值而不是精确的 `COUNT(*)`，差异会永久存在。

5. **软删除不更新计数器**：当前没有 `CASCADE` 删除评论时清理 `comment_likes` 的机制（migrations 使用 `ON DELETE CASCADE`，但这是数据库级的行为，不会通过 GORM 的软删除触发）。如果评论被软删除，关联的 `comment_likes` 行仍然存在，但计数器值可能已经失去意义。

### 影响

1. **用户感知的失真**：点赞计数显示为 3，但实际只有 2 个人点赞（或者反过来）。对于用户生成内容的社交验证信号，这种不准确直接损害信任感。
2. **不可修复**：一旦漂移发生，当前没有 API 或后台任务可以重新计算 `like_count`。唯一的方式是手动执行 SQL。
3. **随时间恶化**：漂移是累积的——每次边缘情况都会增加差异，直到计数器完全不可信。

### 建议方案

1. **引入定期校正任务**：像 PostgreSQL `VACUUM` 一样，增加一个低优先级的后台校正作业，定期执行：
   ```sql
   UPDATE comments c
   SET like_count = (
       SELECT COUNT(*) FROM comment_likes cl
       WHERE cl.comment_id = c.id
   )
   WHERE c.deleted_at IS NULL;
   ```
   可以按批处理（每次 1000 条），运行在非高峰时段。

2. **校正可观测性**：校正任务记录每次运行校正了多少条、最大漂移量、平均漂移量作为指标，帮助判断漂移频率。

3. **写路径保护**：在 `Like`/`Unlike` 成功后，可以选择性地验证 `like_count` 是否与 `SELECT COUNT(*)` 匹配，如果不匹配则异步修复（但这是额外的开销）。

4. **考虑用视图或物化视图替代**：对于不需要实时精确的场景，可以直接用 `COUNT(*)` 查询，或者使用 PostgreSQL 的物化视图定期刷新。

**估算**: 低-中。校正任务本身约 50 行核心代码 + 调度集成。验证写路径是额外开销（可选）。

---

## 方向二：gRPC Server Reflection 在生产中开启时无访问控制——API 表面枚举泄露

### 为什么需要它

gRPC Server Reflection 是 gRPC 生态中一个强大的自省工具——它允许 `grpcurl`、`grpc_cli` 等客户端在不持有 `.proto` 文件的情况下，动态发现服务的所有 RPC 方法、参数类型和字段定义。aero-id 通过配置项 `server.grpc.reflection_enabled` 控制其开关，默认关闭。

但是，**当反射被开启（例如用于调试或内部工具集成）时，任何能够连接 gRPC 端口的客户端都可以无认证地枚举全部 API 表面**。反射端点被显式地加入了认证绕过列表。

### 代码级证据

```go
// internal/handler/grpc/server.go:160-170
// 基础设施服务（health, reflection）必须绕过认证和限流
func (s *Server) isInfrastructure(fullMethod string) bool {
    return strings.HasPrefix(fullMethod, "/grpc.health.") ||
        strings.HasPrefix(fullMethod, "/grpc.reflection.")
    //                            ^^^^^^^^^^^^^^^^^^^^^^^^
    // reflection 完全绕过认证
}
```

```go
// internal/handler/grpc/server.go:176-180
// 基础设施调用（health 探针 + reflection）绕过认证，以便探针和运维工具
// 可以在没有 token 的情况下工作。
```

```yaml
# configs/config.yaml
server:
  grpc:
    reflection_enabled: false   # 默认关闭
```

### 问题分析

1. **信息泄露**：当 `reflection_enabled: true` 时，攻击者可以枚举所有：
   - 服务名称（`FileService`、`MessageService`、`CommentService`、`NotificationService`、`BlockService`、`AdminService`）
   - 每个服务的 RPC 方法签名（参数类型、返回类型）
   - 每个消息类型的字段名、类型、编号
   - 所有 proto 文件的文件描述符
   这些信息可以被用来：
   - 构造精确的恶意请求
   - 发现未公开的 API（如管理端点）
   - 了解内部数据结构

2. **攻击面扩大**：有了完整的 proto schema，攻击者可以编写针对性的 fuzzing 工具，精确命中每个字段的边界值。

3. **运维痕迹缺失**：reflection 调用不记录在访问日志中（因为绕过认证中间件），所以安全团队无法知道谁在何时枚举了 API。

### 建议方案

1. **为 reflection 插件基础认证**：在生产中，即使启用 reflection，也应当要求一个简单的共享密钥或 Bearer token：
   ```go
   // 在 reflection 注册前包装一个认证拦截器
   reflection.Register(s.grpcServer, reflection.WithAuth(func(ctx context.Context) error {
       // 检查 metadata 中的 X-Reflection-Token 是否为配置值
   }))
   ```

2. **反射白名单**：不暴露完整的 FileDescriptor，而是只暴露必要的方法签名。但这需要自定义实现。

3. **可观测性**：在 `isInfrastructure` 路径中添加 reflection 调用的日志记录，让安全团队可以审计谁在枚举 API。

4. **运维指南文档化**：在 README 和部署文档中明确 warning：production 应当保持 `reflection_enabled: false`，仅在调试时临时开启。

**估算**: 低。认证包装 + 审计日志共约 30-50 行代码。不会触及任何业务逻辑。

---

## 方向三：SSO 用户缓存无上游变更传播机制——用户信息陈旧直至 TTL 过期

### 为什么需要它

aero-id 通过 SSO 的 `GetUsers` gRPC 调用批量获取用户信息（显示名、头像等），并将其缓存在 `UserCache` 中。当用户在 SSO 中更新了自己的显示名或头像后，aero-id 继续提供旧数据，**直到缓存 TTL 过期**（默认可能是 5-30 分钟）。这意味着在用户看来，他们改了名字但在 aero-id 中完全不生效。

这个问题是**双向信息流断裂**：SSO → aero-id 的单向同步仅在请求时触发（pass-through cache），没有从 SSO 到 aero-id 的主动推送。

### 代码级证据

```go
// internal/sso/user_cache.go（完整文件概览）
type UserCache struct {
    mu      sync.RWMutex
    entries map[string]*cacheEntry // keyed by uid
    ttl     time.Duration          // 缓存条目有效期
    // ...
}

func (c *UserCache) Get(ctx context.Context, uid string) (sso.User, error) {
    c.mu.RLock()
    entry, ok := c.entries[uid]
    c.mu.RUnlock()
    if ok && c.now().Before(entry.expiresAt) {
        return entry.user, nil // 命中缓存，返回（可能陈旧的数据）
    }
    // 缓存未命中，从 SSO 获取
}
```

```go
// internal/sso/client.go:85-115
func (c *Client) GetUsers(ctx context.Context, uids []string) (map[string]sso.User, error) {
    batch := c.collectCached(uids) // 从缓存获取
    missed := c.missed(uids, batch)
    if len(missed) > 0 {
        resp, err := c.grpcGetUsers(ctx, missed) // 调用 SSO
        if err == nil {
            for _, u := range resp.Users {
                c.userCache.Set(u.Uid, toDomainUser(u)) // 写入缓存
            }
        }
    }
    return merged, nil
}
```

```go
// internal/handler/http/comment.go（示例：在评论响应中嵌入用户信息）
type commentResponse struct {
    // ...
    Actor *userResponse `json:"actor,omitempty"` // ← 来自 cache 的用户信息
}
```

### 影响

1. **用户困惑**：用户在 SSO 更新了显示名，在 aero-id 中发了一条消息，其他用户看到的仍然是旧名。这种不一致直接破坏用户体验。

2. **头像/链接陈旧**：除了显示名，用户头像 URL、个人简介等也可能过时，影响消息、评论、通知等多个功能域。

3. **合规风险**：在某些行业（如金融、医疗），用户身份标识的实时性有合规要求。延迟更新可能导致违规。

4. **冷启动问题**：新部署的实例没有缓存，所有用户信息都需要从 SSO 重新获取。但如果 SSO 的 `GetUsers` 调用费用高或有速率限制，这会是一个问题。

### 建议方案

1. **SSO 端推送/Webhook 机制**：在 SSO 中增加用户变更事件的 webhook（例如 `user.updated`、`user.deleted`），aero-id 接收后立即失效对应缓存条目。当前 outbox/webhook 基础设施已经支持这种模式——可以在 SSO 侧实现类似 `outbox` 的机制，或者 SSO 直接调用 aero-id 的 invalidation 端点。

2. **主动失效端点**：在 aero-id 中增加一个 SSO-认证的缓存失效 API（`POST /v1/admin/cache/invalidate?uid=xxx`），让 SSO 可以在用户信息变更时主动调用。

3. **缩短 TTL + serve-stale 结合**：将 `UserCache` 的 TTL 从分钟级降至秒级（如 30s），但配合 stale-while-revalidate 模式：返回缓存数据的同时异步刷新，保证响应速度的同时减少数据滞后。SSO 的 `TokenCache` 已经实现了 serve-stale 模式 (`internal/sso/token_cache.go`)，可以复用相同模式到 `UserCache`。

4. **版本戳机制**：每次用户信息变更时 SSO 递增一个版本号，aero-id 在请求中附带已知版本，SSO 在无变更时返回 `304 Not Modified`，降低轮询开销。

**估算**: 中。最简单的方案（主动失效端点 + SSO 调用）约 100 行代码 + SSO 侧配合。TTL 优化 + serve-stale 约 30 行。

---

## 方向四：全文搜索的可见性缺口——被分享的文件在搜索结果中不可见

### 为什么需要它

aero-id 的搜索 API 是一个跨实体（消息、评论、文件）的全文搜索入口。在消息和评论搜索中，可见性范围是正确的：消息搜索返回用户发送或接收的消息，评论搜索返回用户能读取的目标上的评论。但**文件搜索的可见性实现不完整**——只搜索用户拥有的文件，完全忽略了通过 `file_shares` 分享给用户的文件。

这意味着一个团队协作场景：用户 A 将一个私有文件分享给用户 B，用户 B 知道文件名但无法通过搜索找到它，只能通过专门的 `ListSharedWithMe` 端点遍历查找。

### 代码级证据

```go
// internal/file/repository.go:223-237
func (r *fileRepo) SearchFiles(ctx context.Context, ownerUID, query string, limit int) ([]*File, error) {
    var rows []fileRow
    err := r.db.WithContext(ctx).Raw(
        `SELECT id, owner_uid, name, size, mime_type, storage_key, storage_type,
                visibility, checksum, meta, created_at, updated_at
         FROM files
         WHERE deleted_at IS NULL
           AND owner_uid = ?                           -- ← 只搜 OWNED 文件
           AND search_vector @@ plainto_tsquery('simple', ?)
         ORDER BY ts_rank(search_vector, plainto_tsquery('simple', ?)) DESC
         LIMIT ?`,
        ownerUID, query, query, limit,
    ).Scan(&rows).Error
    // ...
}
```

```go
// internal/search/search.go:88-93
case domain.SearchKindFiles:
    files, err := s.files.SearchFiles(ctx, actor, query, limit)
    // 传递给 repo 的是 actor，但 repo 只用它来查 owner_uid
```

对比消息搜索的正确范围：

```go
// internal/message/repository.go:255-268
func (r *messageRepo) SearchMessages(ctx context.Context, uid, query string, limit int) ([]*domain.Message, error) {
    // ...
    WHERE deleted_at IS NULL
      AND (from_uid = ? OR to_uid = ?)  -- ← 发送或接收都可见
      AND search_vector @@ plainto_tsquery('simple', ?)
    // ...
}
```

以及文件搜索的 LIKE 降级路径同样有这个问题：

```go
// internal/file/repository.go:239-251
func (r *fileRepo) SearchFilesLike(ctx context.Context, ownerUID, query string, limit int) ([]*domain.File, error) {
    // ...
    Where("deleted_at IS NULL AND owner_uid = ? AND name ILIKE ?", ownerUID, pattern)
    //                                                ^^ 同上，不包含分享的文件
}
```

### 影响

1. **UX 断裂**：文件分享功能存在（`ShareFile`/`ListSharedWithMe`），但搜索不感知分享。用户 B 知道"有一个文件被分享给我了"但无法搜索它，只能用列表翻页查找。
2. **功能不一致**：消息搜索的范围是"用户参与的对话"，评论搜索的范围是"用户可读的目标"。文件搜索的范围是"用户拥有的文件"——这种不一致会给用户造成困惑。
3. **对生产力影响大**：如果有大量文件（数十或数百个）被分享给用户，无法搜索意味着用户必须逐一翻页查找，严重降低效率。

### 建议方案

1. **扩展 SearchFiles 的可见性范围**：在 SQL 查询中加入 UNION 或 OR 条件，将分享文件纳入搜索：
   ```sql
   WHERE deleted_at IS NULL
     AND (
         owner_uid = ?
         OR id IN (SELECT file_id FROM file_shares WHERE grantee_uid = ?)
         OR (visibility = 'public')
     )
     AND search_vector @@ plainto_tsquery('simple', ?)
   ```

2. **考虑搜索结果的权限降级**：如果搜索结果包含分享文件，但用户对该文件的分享已经被撤销（在搜索和查看之间），需要保证用户在访问时得到正确的 Forbidden 错误。这可以通过 GetFile 的现有 ACL 检查来保证。

3. **搜索结果的分享元信息**：在搜索返回的文件中增加 `shared_by` 字段，指明文件是由谁分享的，帮助用户理解"为什么我会看到这个文件"。

4. **性能考虑**：`file_shares` 表在 `(grantee_uid, file_id)` 上已有索引，UNION 的查询性能可控。

**估算**: 低-中。SQL 修改 + 单元测试约 30 行代码。不影响现有 API 返回格式。

---

## 方向五：后台 Goroutine 停止模式不统一——部分子系统有最终 Drain，部分没有

### 为什么需要它

aero-id 有多个后台周期性运行的 goroutine：outbox relay、storage GC sweeper、rate limit cleaner、idempotency cleaner、token cache cleaner。它们都遵循相同的 `Start(interval) -> stop()` 模式，但在**最终停止行为**上存在不一致。具体来说：

- **outbox relay**：停止时执行一次最终 drain，确保已拉取但未发布的事件有机会被处理
- **storage GC sweeper**：停止时直接返回，不做最终 sweep
- **idempotency cleaner**：停止时直接返回，不做最终 purge
- **rate limit cleaner**：停止时直接返回，不做最终清理
- **token cache cleaner**：停止时直接返回，不做最终清理

这种不一致性意味着在服务正常重启（如滚动更新）时，某些清理工作可能延迟到下一周期，而另一些则没有。这虽然不致命，但增加了运维的不确定性和运维人员的认知负担。

### 代码级证据

Outbox relay——有最终 drain：

```go
// internal/outbox/relay.go:159-170
func (r *Relay) Start(interval time.Duration) (stop func()) {
    // ...
    go func() {
        ctx := context.Background()
        for {
            select {
            case <-t.C:
                r.drain(ctx)
            case <-done:
                t.Stop()
                r.drain(context.Background()) // ← 有最终 drain
                return
            }
        }
    }()
    return func() { close(done) }
}
```

Storage GC sweeper——无最终 sweep：

```go
// internal/storage/gc/sweeper.go:237-254
func (s *Sweeper) Start(interval time.Duration) (stop func()) {
    // ...
    go func() {
        ctx := context.Background()
        for {
            select {
            case <-t.C:
                _, _ = s.Run(ctx)
            case <-done:
                t.Stop()
                return // ← 无最终 sweep！直接返回
            }
        }
    }()
    return func() { close(done) }
}
```

Idempotency cleaner——无最终清理：

```go
// internal/idempotency/store.go:112-128
func (s *GormStore) RunCleaner(interval time.Duration) (stop func()) {
    // ...
    go func() {
        for {
            select {
            case <-t.C:
                s.purgeExpired()
            case <-done:
                t.Stop()
                return // ← 无最终 purge
            }
        }
    }()
    return func() { close(done) }
}
```

### 问题分析

1. **outbox relay 的一致性是最强的**，因为它需要保证至少一次交付语义。最终 drain 确保在关闭前尽可能多地处理待发布事件。

2. **storage GC sweeper 没有最终 sweep**，意味着在重启前：
   - 任何刚过 retention 窗口的软删除行不会在这一周期被清理
   - 任何刚过 MinOrphanAge 的孤儿 blob 不会在这一周期被清理
   - 这些数据和存储空间直到下一周期（可达数小时后）才会被释放

3. **idempotency cleaner 没有最终 purge**，意味着：
   - 已经过期的幂等键仍然在表中占据空间
   - 重启后数据库中的过期行仍然存在，直到 cleaner 的下一次运行

4. **不在同一个启动/停止生命周期中**：如果服务频繁重启（如开发环境、自动扩缩容），这些"错过"的清理周期会累积。

### 建议方案

1. **统一 Stop 模式**：为所有后台 goroutine 增加一个 `drainOnStop` 选项，默认启用：
   ```go
   type BackoffConfig struct {
       DrainOnStop bool
   }
   ```

2. **对于 sweeper**：在 stop 时执行一次带超时的最终 `Run`，确保至少处理一批过期的软删除行和孤儿 blob：
   ```go
   case <-done:
       t.Stop()
       finalCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
       defer cancel()
       _, _ = s.Run(finalCtx) // 最终 sweep
       return
   ```

3. **对于 idempotency cleaner**：在 stop 时执行一次最终 `purgeExpired`：
   ```go
   case <-done:
       t.Stop()
       s.purgeExpired() // 最终清理
       return
   ```

4. **所有后台 goroutine 共享超时上下文**：当前 shutdown 序列是 `stopRelay()` → `stopGC()` → ...。可以将 shutdown context 传递给所有后台 goroutine，确保整个 shutdown 在 deadline 内完成。

**估算**: 低。每个子系统修改约 5-10 行代码。总共约 30 行。不涉及架构变更。

---

## 优先级与影响矩阵

| # | 方向 | 类型 | 严重程度 | 估算 | 核心价值 |
|---|------|------|----------|------|----------|
| 1 | like_count 物化计数器漂移无校正 | 数据正确性 | **P1** | 低-中（~50 行） | 防止社交验证信号失真，建立数据可信基线 |
| 2 | gRPC reflection 无访问控制 | 安全 | **P1** | 低（~40 行） | 消除反射端点的信息泄露风险，提高安全基线 |
| 3 | SSO 用户缓存无上游变更传播 | UX/一致性 | **P2** | 中（~100 行 + SSO 配合） | 消除"改名不生效"的 UX 断裂，提升用户信任 |
| 4 | 文件搜索不包含分享文件 | 功能缺口 | **P2** | 低-中（~30 行） | 补齐搜索功能与文件分享功能之间的可见性鸿沟 |
| 5 | 后台 goroutine 停止模式不统一 | 运维一致性 | **P3** | 低（~30 行） | 减少重启场景下的不确定性，统一 shutdown 行为 |

### 推荐执行顺序

```
方向二（gRPC reflection 安全）→ 1-2 天
  ↓ 安全加固，风险明确
方向一（like_count 校正）   → 1-2 天
  ↓ 数据正确性，影响可度量
方向四（文件搜索可见性）    → 1 天
  ↓ 功能补全，UX 提升明显
方向五（goroutine 停止统一）→ 0.5 天
  ↓ 运维一致性，配合其他任务一起做
方向三（SSO 缓存传播）      → 需要 SSO 侧配合
  ↓ 依赖外部系统，可规划为独立项目
```

---

## 附录：文件扫描范围

| 包 | 扫描的文件 |
|----|-----------|
| `internal/comment` | `service.go`, `repository.go`, `comment.go`, `handler_http.go`, `handler_grpc.go` |
| `internal/file` | `service_crud.go`, `service_share.go`, `service_upload.go`, `file.go`, `repository.go`, `repository_share.go`, `handler_http.go`, `handler_grpc.go` |
| `internal/message` | `service.go`, `repository.go`, `message.go`, `handler_http.go`, `handler_grpc.go` |
| `internal/sso` | `client.go`, `token_cache.go`, `user_cache.go` |
| `internal/search` | `search.go`, `handler_http.go`, `handler_grpc.go` |
| `internal/storage/gc` | `sweeper.go` |
| `internal/idempotency` | `store.go` |
| `internal/outbox` | `relay.go`, `outbox.go`, `webhook.go`, `chain.go`, `repository.go` |
| `internal/handler/grpc` | `server.go`, `validate.go`, `ops.go` |
| `internal/handler/http` | `router.go`, `events.go`, `file.go`, `search.go`, `admin.go` |
| `internal/service` | `file.go`, `file_presign.go`, `file_share.go`, `search.go`, `notification.go`, `service.go` |
| `internal/repository` | `file.go`, `message.go`, `comment.go`, `notification.go`, `file_share.go` |
| `internal/app` | `app.go` |
| `migrations` | 全部 22 个 `*.up.sql` / `*.down.sql` |

*本文档仅做分析，未修改任何代码。所有建议基于 2026-07-10 的代码库扫描结果，且已逐方向与 50+ 份既有分析文档做交叉比对确认零覆盖。*
