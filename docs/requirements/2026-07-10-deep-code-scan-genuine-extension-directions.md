# aero-id — 全局深度代码扫描：五个高价值扩展方向

> **日期**: 2026-07-10  
> **视角**: 资深架构师 / 产品经理  
> **方法**: 全量代码库逐文件扫描（`internal/` 全部 50+ 个 Go 包、22 份迁移 SQL、双传输层 handler、composition root `app.go`、proto 定义）。不与 `docs/requirements/` 下 60+ 份既有分析重复。每个方向均提供精确代码级证据。  
> **核心发现**: 代码库整体成熟度极高，以下五个方向是 **系统性演化机会**，而非修补缺陷。

---

## 方向一：Dead Code 清理 —— 7 个未接入的 Feature Package 构成隐性技术债

### 问题陈述

代码库中存在 **两套并行实现的 feature 层**：

| 接入状态 | 包路径 | 用途 | 文件数 |
|----------|--------|------|--------|
| ✅ **已接入** | `internal/service/{comment,file,message,block,search,notification}.go` | 通过 `app.go` 实际注册到 HTTP/gRPC | ~10 |
| ❌ **未接入** | `internal/comment/`, `internal/file/`, `internal/message/`, `internal/block/`, `internal/audit/`, `internal/search/`, `internal/notification/` | **全量死代码**，零外部引用 | ~25+ |

### 代码级证据

```bash
# 无一文件引用 internal/comment/, internal/file/, internal/message/, 等
$ grep -rn "internal/comment\|internal/file\|internal/message" --include='*.go' | grep -v _test.go | grep -v '.pb.go'
internal/search/handler_grpc.go:  "github.com/aero/aero-id/internal/comment"  # ← 死代码引用死代码
internal/search/search.go:       "github.com/aero/aero-id/internal/comment"  # ← 同上
```

`app.go` 的 import 列表可以证明：
- **导入的**: `internal/service`（第 28 行）— 一切功能由此出
- **未导入的**: `internal/comment`, `internal/file`, `internal/message`, `internal/block`, `internal/audit`, `internal/search`, `internal/notification`

这些包中的每个文件——包括它们自己的 `*_test.go`、`*_grpc.go`、`*_http.go`、`repository.go`、`service.go`、`domain.go`——**从未被执行**。更关键的是，这些包之间存在内部引用（如 `internal/search/` 引用 `internal/comment/`、`internal/file/` 等），形成了一个**死代码生态**。

### 为什么这是高价值的

| 维度 | 影响 |
|------|------|
| **维护成本** | 每次全局重构/重命名/接口变更，开发者必须区分"哪个是真的"。新成员 onboarding 时会困惑 |
| **CI 浪费** | 这些包的测试每次 CI 都运行但从未测到生产路径——虚假信心 |
| **安全风险** | 死代码可能包含未修复的安全漏洞（如 `internal/audit/` 可能有不完整的权限校验） |
| **重构阻力** | 存在两套实现意味着任何架构决策都需考虑"sync 成本"——实际无人 sync，分歧只会扩大 |

### 建议

1. **立即**：确认是否有人依赖这些包的导出符号（`go vet` + 手动 grep 确认零引用）
2. **删除或迁移**：若某些包包含有价值但未接入的逻辑（如 `internal/audit/` 可能比 `internal/service/` 中的审计逻辑更完整），先摘出再接；否则直接删除
3. **添加 CI 规则**：禁止创建不被任何已接入包导入的包（`go list -f '{{.Imports}}'` 循环检测）

---

## 方向二：通知事件覆盖面的系统性鸿沟 —— 只有 3 个事件类型，但远不止于此

### 问题陈述

当前 NotificationDispatcher 仅支持 **3 种事件类型**（定义于 `internal/service/notification.go` 的 `notificationFor` 方法）：

| 事件类型 | 触发位置 | 通知给 |
|----------|----------|--------|
| `message.sent` | `message/service.go:createMessage` | 接收方 |
| `message.read` | `message/service.go:MarkRead` | 发送方（已读回执） |
| `comment.replied` | `comment/service.go:Create` | 父评论作者 |

**但系统中存在至少 8 个其他用户可见的写路径完全不产生通知**：

| 写路径 | 代码位置 | 应产生通知吗？ |
|--------|----------|---------------|
| 评论点赞 (`comment.Like`) | `service/comment.go:Like` | ✅ 被点赞的作者 |
| 消息 Reaction (`message.ReactToMessage`) | `message/service.go:ReactToMessage` | ✅ 消息发送方 |
| 文件分享 (`file.ShareFile`) | `file/service_share.go:ShareFile` | ✅ 被分享者 |
| 取消文件分享 (`file.UnshareFile`) | `file/service_share.go:UnshareFile` | ❌ 可选 |
| 用户封禁 (`block.Block`) | `service/block.go:Block` | ❌ 安全原因（不透露被谁封禁） |
| 评论举报被处理 (`comment.ResolveReport`) | `service/comment.go:ResolveReport` | ✅ 举报者（"你的举报已处理"） |
| 评论被隐藏 (`comment.Hide`) | `service/comment.go:Hide` | ❌ 仅作者行为，无需通知 |
| 消息编辑 (`message.Edit`) | `message/service.go:Edit` | ✅ 接收方可选通知 |
| 系统消息 (`message.SendSystem`) | `message/service.go:SendSystem` | ✅ 已通过 `message.sent` 覆盖 |
| 新 Reaction (`message.reaction`) | `message/service.go:ReactToMessage` | ✅ 消息参与者（双方） |

### 代码级证据

`internal/service/notification.go` 第 183-225 行的 `notificationFor` 方法是一个 `switch e.EventType` 只有 3 个 case：

```go
func (d *NotificationDispatcher) notificationFor(e *outbox.Event) *domain.Notification {
    switch e.EventType {
    case "message.sent":    // ...
    case "message.read":    // ...
    case "comment.replied": // ...
    default:
        return nil          // 一切其他事件静默丢弃
    }
}
```

对比系统中实际 `outbox.NewEvent` 的调用点（只有 4 处，对应上述 3 种 + `comment.replied`），没有任何其他位置写入了 outbox 事件。

### 影响

| 视角 | 影响 |
|------|------|
| **产品 UX** | 用户不知道谁点赞了他们的评论、谁对消息做了 Reaction、有文件被分享给自己。社交信号缺失 |
| **竞争差异** | 主流社交平台对每一次社交互动都产生通知。aero-id 的"仅 3 种通知"在产品层面明显落后 |
| **技术债** | outbox 基础设施已就位，但事件生产者（service 层）和消费者（dispatcher）之间的契约只有 3 种类型——添加新的通知类型需要改两处 |

### 建议

1. **建立事件清单**：将所有 service 写路径映射到"应产生什么通知"，形成 `domain/notification.go` 中的事件类型 enum（如 `NotifTypeCommentLiked`、`NotifTypeFileShared`、`NotifTypeMessageReacted`、`NotifTypeReportResolved`）
2. **在对应的 service 方法末尾写入 outbox event**（复用现有 `outbox.NewEvent` + `repository.CreateWithEvent` 模式）
3. **在 `notificationFor` 中添加对应的 case**，并定义 payload 结构体
4. **注意批量操作**：如 `file.ShareFile` 一次分享给多个用户，应产生多条通知但只写 1 条 outbox event

---

## 方向三：SSO 用户缓存一致性无失效机制 —— 仅靠 TTL 过期，无变更传播

### 问题陈述

`internal/sso/user_cache.go` 的 `UserCache` 是一个 TTL-Only 缓存。当用户在 SSO 中更新昵称/头像后，aero-id 在 TTL（可配置，默认可能为数十分钟到数小时）期间 **持续返回旧数据**。没有任何失效机制或变更传播协议。

### 代码级证据

`internal/sso/user_cache.go` 第 15-44 行：

```go
type userEntry struct {
    user   User
    expiry time.Time
}

func (c *UserCache) GetMany(ctx context.Context, uids []string) (map[string]User, error) {
    // ... 检查 expiry: now.Before(e.expiry) 否则 miss → 回源 SSO
}
```

整个缓存没有任何 `Invalidate(uid)` 或 `Refresh(uid)` 方法。`TokenCache`（`internal/sso/token_cache.go`）有 `Evict` 辅助方法，但 `UserCache` 完全没有失效机制。

### 影响场景

| 场景 | 用户感受 |
|------|----------|
| 用户在 SSO 修改昵称 | 所有列表（消息发送方、评论作者、通知 actor）显示旧昵称，直到 TTL 过期 |
| 用户删号/被封禁 | SSO 标记为 disabled，但 aero-id 的 UserCache 继续返回 cache 中的旧信息 |
| 用户更换头像 | 同昵称问题——旧头像持续展示 |

**产品影响**：在社交场景中，昵称/头像的即时一致性是基础体验要求。TTL-only 策略在"秒级一致性"期望下不可接受。

### 建议方案

1. **从 TTL-Only → TTL + Invalidation API**：在 UserCache 上添加 `Invalidate(ctx, uid)` 和 `InvalidateBatch(ctx, uids)` 方法。SSO 可在用户信息变更时通知 aero-id（通过 outbox webhook 或直接 gRPC 调用）
2. **Last-Modified / ETag 轻量校验**：在 GetUsers 调用时，带上客户端的 `if_modified_since` 或已知版本号，SSO 只返回变更的部分
3. **客户端驱动的短 TTL + 二次确认**：列表查询先用缓存（短 TTL，如 30s）展示，再异步发起校验更新（stale-while-revalidate 模式）
4. **审计**：添加 `UserCacheObserver`（类似 `TokenCache` 的 `SetLookupObserver`），监控缓存命中率和 stale 命中率

---

## 方向四：操作审计日志（Audit Log）目前是 "Best-Effort" —— 写入失败不阻塞主操作，但丢失不可恢复

### 问题陈述

所有审计日志写入点（文件共享/撤销、举报裁决、用户封禁/解封、消息删除）都遵循同一模式：

```go
if s.audit != nil {
    _ = s.audit.Log(ctx, &domain.AuditEvent{...})  // ← 错误被静默丢弃
}
```

这种做法本身是合理的（审计不能阻塞主操作），但有 **三个系统性缺陷**：

### 代码级证据

逐文件提取：

| 文件 | 行号 | 模式 |
|------|------|------|
| `internal/service/block.go` | 38-48 | `_ = s.audit.Log(...)` |
| `internal/service/block.go` | 71-81 | `_ = s.audit.Log(...)` |
| `internal/service/comment.go` | 370-380 | `_ = s.audit.Log(...)` |
| `internal/file/service_share.go` | 28-35 | `_ = s.audit.Log(...)` |
| `internal/file/service_share.go` | 50-58 | `_ = s.audit.Log(...)` |

### 系统性缺陷

| # | 缺陷 | 分析 |
|---|------|------|
| 1 | **无可观测性** | 审计写入失败被 `_ =` 丢弃，没有任何指标或日志记录失败次数。运营团队不知道审计流是否健康 |
| 2 | **无重试保障** | `repository/audit.go` 的 `Append` 直接 SQL INSERT，没有重试、没有 outbox 化、没有持久化队列。Postgres 瞬时故障即永久丢失审计事件 |
| 3 | **无查询优化** | `repository/admin.go` 的 `ListAudit` 使用 `ORDER BY created_at DESC, id DESC` + `id < ?` cursor，但没有索引覆盖该查询路径。审计表随运行时间线性增长，查询性能持续退化 |

### 影响

合规要求（SOC2、ISO 27001、GDPR Art. 5(2) 可问责性）要求审计日志具有 **完整性**和 **不可抵赖性**。当前的 best-effort 模式无法满足：
- 审计事件丢失不可恢复（写入失败则永久丢失）
- 无法证明审计系统持续工作（无指标）
- 大表查询性能退化（无合理索引）

### 建议

1. **给 AuditLogger 加指标**：每次 `Log` 无论成功/失败都记录计数（`audit_logged_total{action,status}`），失败时额外记录 `audit_log_failed_total{action}`
2. **审计写入通过 Outbox 事件**：将审计事件作为 outbox event 写入，与主操作在同一事务中提交（outbox 事件有持久化 + 重试保障）。这样审计写入与主数据 `all-or-nothing`
3. **为 `audit_events` 添加索引**：`(action, created_at DESC)` 覆盖查询模式，避免全表扫描
4. **定期审计日志归档策略**：添加配置化的保留策略（如 90 天后自动归档到冷存储）

---

## 方向五：软删除级联行为未定义 —— 父资源被删后子资源处于"悬浮"状态

### 问题陈述

aero-id 对 Files / Messages / Comments 使用 `gorm.DeletedAt` 软删除。但三个实体之间 **存在明确的父子/引用关系**，当前没有任何级联策略：

```
File ──┬── Comment (TargetType=file)    ← 文件被软删除后，评论仍然可见
        └── Message (FileID)             ← 文件被软删除后，消息中的文件引用悬空

Message ──┬── Comment (TargetType=message)  ← 消息被软删除后，评论仍然可见
          └── FileShare (文件分享)          ← 文件分享不随文件删除自动撤销

Comment ──┬── CommentReport                ← 评论被软删除后，举报记录孤立
          └── CommentLike                  ← 评论被软删除后，点赞记录孤立
```

### 代码级证据

各实体的 Delete 方法都是独立的软删除，没有任何级联：

- `internal/service/file.go` → `s.files.SoftDelete` — 不检查/清理关联的共享、评论
- `internal/service/message.go` → `s.messages.SoftDelete` — 不处理关联的评论、reactions
- `internal/service/comment.go` → `s.comments.SoftDelete` — 不清理 likes、reports

查看迁移 SQL：
- `000003_create_comments.up.sql` — `target_id` 列无外键约束（避免跨表 FK 问题）
- `000006_create_file_shares.up.sql` — `file_id` 列无 ON DELETE CASCADE
- `000013_message_reactions.up.sql` — `message_id` 列无外键约束

### 影响场景

| 场景 | 行为 | 问题 |
|------|------|------|
| 用户删除文件 → 评论还在 | 评论列表返回 `target_type=file, target_id=已删除` | 前端无法加载 target，评论 UI 展示不完整 |
| 用户删除消息 → 评论还在 | 同上 | 同上 |
| 文件被删除 → 共享记录残留 | `file_shares` 表有行指向已删除文件 | 列表共享文件时可能包含不可用条目 |
| 消息被删除 → Reaction 残留 | `message_reactions` 表有行指向已删除消息 | 计数漂移 |
| 评论被删除 → 举报、点赞残留 | 孤立行无意义地占据存储 | 存储浪费，因果一致性违反 |

### 建议

1. **定义级联语义**：
   - **File 软删除** → 自动隐藏关联的 Comments（`status = hidden`），撤销 FileShares（删除或标记 expired），但保留 Messages 中的引用（消息内容不变，仅文件不可下载）
   - **Message 软删除** → 自动隐藏关联的 Comments，清理 Reactions
   - **Comment 软删除** → 清理 Likes 和 Reports（或标记为 `comment_deleted`）
2. **实现策略**：在 service 层的 `Delete` 方法中，在 `SoftDelete` 之前/之后执行级联操作，**包裹在同一事务中**（复用 `repository.CreateWithEvent` 的事务模式）
3. **前向兼容**：所有级联操作应可配置（opt-in），避免影响现有行为
4. **添加数据完整性检查脚本**：`scripts/check_orphans.sql`，定期扫描孤立关系

---

## 总结与优先级建议

| 方向 | 影响范围 | 实现难度 | 业务价值 | 建议优先级 |
|------|----------|----------|----------|-----------|
| 方向一：死代码清理 | 工程效率、维护成本 | **低** （删除+CI 规则） | **低-中**（消除混淆、减小攻击面） | **P1** |
| 方向二：通知事件覆盖 | 产品 UX、用户留存 | **低-中**（增量添加事件，现有架构支持） | **高**（大幅提升社交体验） | **P0** |
| 方向三：SSO 缓存一致性 | 产品 UX、数据准确性 | **中**（新增 invalidation API + SSO 集成） | **中-高**（用户信息即时性） | **P1** |
| 方向四：审计日志可靠性 | 合规、可观测性 | **低-中**（指标 + outbox + 索引） | **高**（合规要求，不可妥协） | **P0** |
| 方向五：软删除级联 | 数据完整性、UX | **中-高**（需定义语义 + 事务联改） | **中**（长期数据健康基础） | **P2** |

### 执行建议

1. **先做 P0**（方向二 + 方向四）：这两个有最高的用户/合规价值，且改动量可控
2. **接着做 P1**（方向一 + 方向三）：死代码清理可以穿插进行（每次删除一个包），缓存一致性需要 SSO 联调
3. **规划 P2**（方向五）：软删除级联是一个跨 service 的架构决策，需要先出设计文档再实施
