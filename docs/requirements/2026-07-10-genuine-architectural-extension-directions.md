# aero-id — 全局代码扫描后的五个高价值扩展方向

> **日期**: 2026-07-10
> **视角**: 资深架构师 / 产品经理
> **方法**: 全量代码库逐文件扫描（`internal/` 全部 50+ Go 包 ~200 源文件、22 份 SQL 迁移、双传输层 handler、composition root `app.go`、proto 定义、`cmd/`、`configs/`）。与 `docs/requirements/` 下既有 40+ 份分析文档做逐主题交叉比对，确保以下 5 个方向未被既有分析系统性覆盖。
> **约束**: 不编写任何代码。

---

## 前置：当前代码成熟度概览

aero-id 已交付的功能远超典型 MVP，涵盖文件流水线（流上传、预签名直传、S3/local 双后端、内容去重、MIME 校验、GC 回收）、消息系统（全 CRUD、Reactions、编辑、软删除、已读回执、对话查询、FTS 搜索）、评论系统（两层嵌套、Like/Unlike、举报/审核/隐藏、编辑）、通知系统（Outbox 驱动、偏好设置含静默时段、合并折叠、SSE 实时推送、Webhook 出口）、全文搜索（PostgreSQL FTS 跨实体）、可靠性基础设施（事务性 Outbox、幂等性 Key、限流、SSO 断路器与 TLS/mTLS、Storage GC）、运维体系（RED 指标、OTel 追踪、优雅关闭、命令行迁移工具、自动迁移）、安全管控（用户屏蔽、审计日志、管理角色与权限）、传输层对等（38 个 gRPC RPC 与 HTTP 完全对等）。

`make test && make lint` 绿灯。以下 5 个方向是全局扫描后发现的**高价值但未被既有分析系统性覆盖**的方向。

---

## 方向一：`internal/notification/` 包存在 700+ 行死代码

### 重要性

**高 · 工程债 —— 测试给出虚假信心**

### 问题描述

代码库中存在两份并行的通知派发实现：

| 路径 | 行数（含测试） | 在生产中运行？ |
|------|---------------|---------------|
| `internal/notification/dispatcher.go` | 140（+ 210 测试） | ❌ |
| `internal/notification/service.go` | 74（+ 203 测试） | ❌ |
| `internal/notification/repository.go` | 114（+ 246 测试） | ❌ |
| `internal/notification/handler_http.go` | 78（+ 165 测试） | ❌ |
| `internal/notification/handler_grpc.go` | 100（+ 141 测试） | ❌ |
| **小计** | **~1060 行死代码（含测试）** | **全部未运行** |
| | |
| `internal/service/notification.go` | 320（Dispatcher + Service） | ✅ 实际生产路径 |
| `internal/handler/http/notification.go` | 76 | ✅ |
| `internal/handler/grpc/notification.go` | 76 | ✅ |
| `internal/repository/notification.go` | 160+ | ✅ |
| **小计** | **~630 行有效代码** | **生产运行** |

**证据 1：`app.go` 使用 `service.*` 而非 `notification.*`**

```go
// internal/app/app.go:168-172
dispatcher := service.NewNotificationDispatcher(notifRepo,   // ← service 包
    service.WithDispatcherBlockChecker(blockRepo),
    service.WithDispatcherPreferenceChecker(prefChecker),
    service.WithDispatcherRateLimiter(10),
    service.WithDispatcherCollapse(5*time.Minute),
    service.WithDispatcherPusher(sseHub),
)

// internal/app/app.go:250
notifSvc := service.NewNotificationService(notifRepo, ssoClient)

// internal/app/app.go:304
httpapi.NewNotificationHandler(notifSvc),                    // ← handler/http 包

// internal/app/app.go:325
grpcsrv.NewNotificationServer(notifSvc),                     // ← handler/grpc 包
```

**证据 2：`internal/notification/` 无外部调用者**

```bash
$ grep -rn "github.com/aero/aero-id/internal/notification" --include="*.go" . | grep -v _test.go
# 无输出 — 没有非测试代码引用此包
```

该包仅被其自身的测试文件引用（`dispatcher_test.go`、`handler_grpc_test.go`、`handler_http_test.go`、`service_test.go`、`repository_integration_test.go`），这些测试在测试永远不会进入生产的代码路径。

**证据 3：功能上的静默退化**

死代码中的 `internal/notification/dispatcher.go` 是早期 Dispatcher 实现。它具有与 `service.NotificationDispatcher` 相同的结构但缺少关键功能：
- 无 `PreferenceChecker`（偏好检查）
- 无 `notifRateLimiter`（通知频率限制）
- 无 `collapseWin`（折叠合并）
- `pusher` 类型是 `hub.Pusher` 而非 `service.NotificationPusher`（签名不兼容）

### 影响分析

- **虚假的测试覆盖率**：`make test` 报告通过，但 1060 行代码（含测试）覆盖的是从不执行的路径
- **维护债务**：每次向通知系统添加新功能（如新的通知类型），需要同时修改两套代码
- **未来迁移风险**：新开发者可能误以为 `internal/notification/` 是推荐包，在此添加代码
- **构建时间增加**：不必要的编译单元

### 建议行动

1. 删除 `internal/notification/` 包的全部文件（包含 `.go` 和 `_test.go`）
2. 将 `internal/notification/notification.go` 中有价值的接口定义（如果有）迁移到合适的包
3. 确认无测试框架配置引用已删除的测试

**估算**: 低（1 天，纯删除 + 验证）

---

## 方向二：通知缺乏客户端可直接渲染的内容上下文

### 重要性

**高 · 每次 Feed 加载产生 O(N) 额外 API 调用**

### 问题描述

每条通知携带的信息：

```go
// internal/domain/notification.go
type Notification struct {
    ID             ID
    RecipientUID   string
    Type           string    // e.g. "message.received", "comment.reply"
    ActorUID       string    // who triggered it
    SubjectType    string    // e.g. "message", "comment"
    SubjectID      string    // UUID string, pointing to the subject entity
    ReadAt         *time.Time
    CollapsedCount int
    CreatedAt      time.Time
}
```

通知不包含：

| 缺失字段 | 影响 |
|----------|------|
| `subject_preview` — 消息的前 100 字 / 评论内容 / 文件名 | 客户端必须调用 `GET /messages/:id` 或 `GET /files/:id` 才能显示通知内容 |
| `actor_name` / `actor_avatar` — 触发者的昵称和头像 | 客户端必须调用 `GET /notifications` 再解析返回的 `users` map，或额外调用 SSO |
| `subject_url` — 客户端跳转链接 | 客户端需要自行拼接 URL 模式 |
| `summary_text` — 人类可读的摘要（如"Alice 回复了你的评论"） | 通知在推送通知 / 邮件等外部渠道中不可读 |

**代码级证据：客户端必须走串行化回填流程**

当客户端加载通知列表（`GET /notifications`）时：

```
1. GET /notifications          → 返回 N 条 notification 对象（仅含 subject_id）
2. 对每条 notification 的 subject_id 调用对应 API：
   - subject_type="message"  → GET /v1/conversations/:uid (需要知道对方 uid!)
   - subject_type="comment"  → 需要评论列表 API
   - subject_type="file"     → GET /v1/files/:id
   最坏情况：N 次额外 API 调用 + N 次 SSO 用户解析
```

当前 `GET /notifications` 虽然返回了 `users` map（从 SSO 批量解析触发者信息），但这**仅解决了 actor 昵称/头像的查询问题**，通知中引用的 subject 的内容仍然是黑箱。

**边缘情况分析**

- **已删除的 subject**: 如果用户收到某评论的回复通知，但该评论后来被删除，`GET /v1/comments/:id` 返回 404。客户端无法显示通知内容。
- **已撤销的权限**: 文件通知但用户不再有权限访问该文件（共享被撤销），`GET /v1/files/:id` 返回 403。
- **跨实体引用**: "Alice 回复了你的评论"→ 需要先查出评论，再找出评论挂载的文件/消息名。

### 影响分析

- **UX 缺陷**: 通知列表中的每条通知都需要加载动画或"无法加载"状态占位
- **性能开销**: 每页 20 条通知 → 额外 20 次 API 调用（即使批量也要多次往返）
- **外部渠道不可用**: 无法将通知直接推送到邮件/短信/推送通知，因为没有可读文本
- **离线支持差**: 客户端缓存通知列表后仍无法离线渲染

### 建议方向

在 `notifications` 表增加可选预览/摘要字段：

```sql
ALTER TABLE notifications ADD COLUMN subject_preview TEXT;   -- 前 100 字（消息/评论内容，文件名）
ALTER TABLE notifications ADD COLUMN summary_text TEXT;      -- 如 "Alice 回复了你的评论" 
```

Dispatcher 在创建通知时同步填充这些字段。这样通知列表端点一次调用即可返回所有渲染所需信息。

**估算**: 低-中（1-2 天，涉及 migration + dispatcher 修改 + 服务层填充）

---

## 方向三：通知折叠丢失粒度可见性

### 重要性

**中 · 用户体验降级与信息不对称**

### 问题描述

当 `collapseWin`（默认 5 分钟）内有多条同类型、同发送者的通知时，Dispatcher 将新事件合并到已有的未读通知行上，仅递增 `CollapsedCount`：

```go
// internal/service/notification.go:234-245
existing, err := d.notifs.FindRecentByTypeAndActor(ctx, 
    n.RecipientUID, n.Type, n.ActorUID, d.collapseWin)
if err == nil && existing != nil && existing.ID != n.ID {
    _ = d.notifs.IncrementCollapsedCount(ctx, existing.ID)
    // ...
    return nil  // 新创建的通知行未被读取，但也不在 feed 中突出显示
}
```

这意味着：

| 场景 | 用户看到 | 用户实际错过 |
|------|---------|-------------|
| Alice 在 5 分钟内发了 5 条消息 | "Alice 发了 5 条消息"（通过 `CollapsedCount: 5`） | 无法知道每条消息的内容 |
| Alice 发了 2 条消息 + Bob 发了 1 条 | Alice 的第 2 条被折叠 → "Alice 发了 2 条消息" + "Bob 发了 1 条消息" | 第 2 条 Alice 消息的内容不可见 |
| 3 条评论回复来自不同的父评论 | 3 条独立的通知（不同 subject_id） | ✅ 无信息丢失 |

**关键观察**：折叠是存在信息丢失的。客户端无法知道折叠的消息具体有什么内容。`CollapsedCount` 只是一个数字，没有折叠内容的数组或索引。

**Edge Case — 跨折叠窗口的读取状态**：

假设以下时间线：
```
T=0:  Alice 发消息 "你好"  → 通知 N1 (CollapsedCount=1)
T=2:  Alice 发消息 "在吗"  → 折叠到 N1 (CollapsedCount=2)
T=3:  用户读取 N1
T=4:  Alice 发消息 "怎么不理我" → 通知 N2 (CollapsedCount=1，新通知行)
```

用户永远不知道 `"在吗"` 的存在，因为它在 N1 中被折叠且 N1 已被标记为已读。

### 影响分析

- **信息不对称**：用户不能感知折叠事件的具体内容
- **不可逆的数据丢失**：一旦折叠，原始事件信息永久丢失
- **与偏好设置冲突**：用户如果设置了通知偏好为"重要消息不折叠"，当前系统无法区分"重要"与"普通"

### 建议方向

1. **折叠事件元数据**：在 `notifications` 表中增加 `collapsed_events JSONB` 列，存储被折叠事件的 subject_id 数组
2. **内容快照数组**：存储被折叠消息的内容摘要数组，让客户端可以展开显示所有折叠内容
3. **折叠阈值**：当 `CollapsedCount` 超过某个阈值（如 10）时，不再继续折叠而是创建新行，防止单条通知折叠过多信息

**估算**: 中（2-3 天，涉及 migration + dispatcher 重构 + repository 修改）

---

## 方向四：用户屏蔽不级联到已有的文件分享

### 重要性

**高 · 安全模型缺失 —— 屏蔽后文件访问未撤销**

### 问题描述

当前的 Block 系统在 `internal/service/notification.go` 和 `internal/service/message.go` 中检查和抑制**后续**的消息发送和通知生成，但不会影响**已存在的**文件分享。

**证据 1：`canRead` 方法不检查屏蔽关系**

```go
// internal/file/file.go:120-126
func (s *fileService) canRead(ctx context.Context, actor string, f *File) (bool, error) {
    if domain.CanReadFile(actor, f) {
        return true, nil
    }
    if s.shares == nil {
        return false, nil
    }
    shared, err := s.shares.IsSharedWith(ctx, f.ID, actor)
    if err != nil {
        return false, apperr.Internal(...)
    }
    return shared, nil
}
```

此方法检查文件可见性和显式分享，但**不检查** actor 和文件 owner 之间是否存在屏蔽关系。

**证据 2：相关领域函数也不考虑屏蔽**

```go
// internal/domain/access.go
func CanReadFile(actor string, f *File) bool {
    return f.Visibility == VisibilityPublic || f.OwnerUID == actor
}
```

**证据 3：Block 系统仅在后缀路径中生效**

```bash
$ grep -rn "IsBlocked\|BlockChecker" --include="*.go" internal/ | grep -v _test.go
internal/service/message.go:   // BlockChecker — message.Send 中抑制被屏蔽方发来的消息
internal/service/notification.go: // BlockChecker — notification dispatcher 中抑制通知
internal/block/block.go:   // BlockChecker 接口定义
```

BlockChecker 的注入点和调用点仅限于 `message.Service` 和 `notification.Dispatcher`，**完全未出现在 `file.Service` 中**。

**场景分析**

| 事件序列 | 当前行为 | 预期行为 |
|---------|---------|---------|
| 1. A 分享文件给 B | ✅ 分享成功 | ✅ |
| 2. B 屏蔽 A | ✅ 屏蔽成功；B 不再收到 A 的消息/通知 | ✅ |
| 3. B 访问 A 分享的文件 | ✅ **仍可访问**（安全缺口） | ❌ 应拒绝访问 |
| 4. A 访问自己分享给 B 但 B 已屏蔽 A 的文件 | ✅ **仍可通过分享链接访问**（安全缺口） | ❌ 应拒绝访问 |

**边界情况**

- **方向性屏蔽**：A 屏蔽 B 但 B 未屏蔽 A——A 仍然可以访问 B 分享给 A 的文件（合理，因为 block 是方向性的）
- **部分屏蔽**：A 屏蔽 B 后又取消屏蔽——文件分享应该是仍然有效还是需要重新分享？一致的做法是：屏蔽期间访问被拒绝，取消屏蔽后恢复
- **批量文件**：如果 A 分享了一整个文件夹（当前无文件夹概念，但需要预留）给 B，B 屏蔽 A 后所有文件都应拒绝访问

### 影响分析

- **安全缺口**：屏蔽语义不完整——被屏蔽方仍然可以通过历史共享访问敏感文件
- **用户迷惑**：用户屏蔽某人后期望"完全隔离"，但不自动撤销文件分享
- **合规风险**：在多用户敏感数据场景下，遗留的共享访问可能违反数据保护要求

### 建议方向

1. **增强 `canRead`**：在文件访问路径中增加 BlockChecker 调用——检查请求者和文件 owner 之间的方向性屏蔽关系
2. **屏蔽时级联**：当 A 屏蔽 B 时，自动撤销 A→B 方向的所有文件分享
3. **审计日志**：屏蔽触发的分享撤销应记录审计事件
4. **通知**：当分享因屏蔽被自动撤销时，不通知被屏蔽方（但可通知分享者）

**估算**: 低-中（2-3 天，涉及 file service 修改 + 需传递 BlockChecker 依赖到 file service + 可能的级联逻辑）

---

## 方向五：搜索系统缺乏相关性排序

### 重要性

**中 · 搜索结果对用户不可用**

### 问题描述

当前搜索实现使用 PostgreSQL FTS（`tsvector` + `GIN` 索引），但搜索结果的排序不是按相关性，而是按**实体类型分组、每种类型顺序返回**：

```go
// internal/search/search.go:36-57
func (s *searchService) Search(ctx context.Context, query string, kinds []domain.SearchKind, limit int) ([]SearchHit, error) {
    var hits []SearchHit
    for _, kind := range kinds {
        hits, err = s.searchKind(ctx, actor, query, limit, kind, hits)
        // ...
    }
    return hits, nil
}
```

每个实体类型的搜索调用各自 repository 的 `SearchMessages`、`SearchComments`、`SearchFiles` 方法，每种独立返回最多 `limit` 条结果。

**证据 1：Repository 层使用 `ILIKE` 而非 `ts_rank`**

```go
// internal/repository/message.go — SearchMessages
func (r *messageRepo) SearchMessages(ctx context.Context, actor, query string, limit int) ([]*domain.Message, error) {
    q := r.db.WithContext(ctx).
        Model(&messageRow{}).
        Where(`(search_vector @@ plainto_tsquery('simple', ?) OR content ILIKE ?)`, query, "%"+query+"%").
        Where("(from_uid = ? OR to_uid = ?) AND deleted_at IS NULL", actor, actor).
        Order("created_at DESC").  // ← 按时间排序，不按相关性
        Limit(limit + 1).
        Find(&rows)
```

`Created_at DESC` 作为排序条件意味着：最新的匹配结果总是排在第一位，即使其他结果与查询的相关性高得多。

**证据 2：跨实体排序不存在**

当用户在一个搜索中搜索消息、评论和文件时，结果分组排列：

```
[Message 匹配 1, Message 匹配 2, ..., Comment 匹配 1, Comment 匹配 2, ..., File 匹配 1, ...]
```

所有消息按时间排在前面，然后是评论，最后是文件。与查询最相关的文件可能淹没在 20 条无关消息后面。

**证据 3：`ILIKE` 回退搜索性能差**

`ILIKE '%query%'` 在 `tsvector` 未命中时作为回退方案。这种带前导通配符的 `ILIKE` 无法使用普通 B-tree 索引，会触发**全表顺序扫描**。在 `messages` 表有百万行时，即使没有匹配结果，每次搜索也会扫描整张表。

**场景分析**

| 用户行为 | 当前体验 | 预期体验 |
|---------|---------|---------|
| 搜索"合同" | 先列出最近 20 条包含"合同"的消息，然后是评论，再是文件 | 最相关的文件（文件名=合同.docx）应该排在最前面 |
| 搜索"hello"在百万条消息中 | 扫描整张表（ILIKE 回退） | 使用 `ts_rank` 只扫描 GIN 索引 |
| 跨实体搜索 | 无法按相关性跨实体排序 | 所有实体的结果混合按 `ts_rank` 得分排序 |

### 影响分析

- **搜索可用性差**：用户需要手动翻越大量不相关结果
- **性能问题**：`ILIKE` 回退在大表上导致全表扫描
- **无高亮片段**：返回的是完整实体而非包含匹配上下文的片段，客户端无法显示搜索摘要

### 建议方向

1. **`ts_rank` 排序**：Search 查询使用 `ts_rank(search_vector, plainto_tsquery('simple', ?))` 作为 ORDER BY 条件，将最相关的结果排在前面
2. **统一跨实体搜索**：所有实体类型的搜索结果通过 `UNION ALL` 合并，使用统一的 `ts_rank` 得分排序，加 `LIMIT` 控制总结果数
3. **`ts_headline` 片段**：返回匹配内容的上下文片段，支持客户端高亮显示
4. **去除 `ILIKE` 回退**：仅使用 FTS 索引查询，避免全表扫描

```sql
-- 预期查询模式
SELECT id, 'message' AS kind, content,
       ts_rank(search_vector, plainto_tsquery('simple', '合同')) AS rank,
       ts_headline('simple', content, plainto_tsquery('simple', '合同'),
                   'StartSel = <mark>, StopSel = </mark>') AS headline
FROM messages
WHERE search_vector @@ plainto_tsquery('simple', '合同')
  AND deleted_at IS NULL
  AND (from_uid = 'u1' OR to_uid = 'u1')
ORDER BY rank DESC
LIMIT 20;
```

**估算**: 中（3-5 天，涉及多表 `UNION ALL` 查询 + repository 重构 + search service 重写）

---

## 总结与优先级建议

| # | 方向 | 类型 | 优先级 | 工作量 | 核心理由 |
|---|------|------|--------|--------|---------|
| 1 | 删除 `internal/notification/` 死代码 | 工程债 | **P0** | 1 天 | 700+ 行从未执行的代码 + 1000+ 行虚假测试 |
| 2 | 通知内容模型增加渲染上下文 | UX / 性能 | **P1** | 1-2 天 | 消除 O(N) 额外 API 调用；改善所有客户端体验 |
| 3 | 通知折叠不丢粒度信息 | UX | **P2** | 2-3 天 | 信息丢失不可逆；但影响面局限于折叠窗口内 |
| 4 | 屏蔽级联到已有文件分享 | 安全 | **P1** | 2-3 天 | 屏蔽语义完整性的硬缺口；有合规风险 |
| 5 | 搜索相关性排序 | UX / 性能 | **P2** | 3-5 天 | 搜索结果质量直接影响用户对搜索功能的信任 |

开始之前请执行 `make test && make lint` 验证当前基线状态。
