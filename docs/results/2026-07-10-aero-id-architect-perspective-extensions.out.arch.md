现在我已经对整个代码库有了深入理解。以下是架构分析报告。

---

# aero-id 架构分析报告

## 1. 架构评估

### 1.1 当前架构的优势

**分层结构清晰** — 代码库严格遵循 `handler → service → repository/storage/sso` 的单向依赖，符合设计文档中"横向分层而非纵向模块化"的原则。`internal/app/app.go` 作为组合根将所有依赖连线到一处，启动和关闭流程可追踪。

**领域模型轻量** — `internal/domain` 包不含业务逻辑（仅 `CanReadFile`/`CanReadMessage` 两个纯函数），领域对象是贫血数据容器。对于当前规模（~50 个实体方法）这是一个合理选择——DDD 的充血模型会增加不必要的复杂度。

**双协议共享 service 层** — HTTP（Gin）和 gRPC handler 确实调用同一组 `service/FileService`、`service/MessageService` 等接口，行为一致。

**接口定义收敛于 `internal/service/service.go`** — 所有 service 接口、输入/输出 DTO 集中定义，避免了 handler 层对 repository 的直接依赖。

**自定义 metrics 包** — 零外部依赖实现 Prometheus 文本暴露，包含请求 RED、SSO 健康、outbox 观测三组指标。这是设计文档中 "out of scope" 后添加的务实决策。

### 1.2 关键的架构债务

#### 债务 1：双套 service 层 — `internal/file/` vs `internal/service/`

这是最严重的架构问题。代码库中存在两套并行服务实现：

| 旧（`internal/file/`、`internal/notification/` 等） | 新（`internal/service/`） |
|---|---|
| `file.FileService`（含 `fileService`、`ShareRepository` 等） | `service.FileService`（含 `fileService` 等） |
| `notification.Dispatcher`（简单版，无语义去重） | `service.NotificationDispatcher`（完整版，含 collapsing/rate-limit/preference） |
| `message.MessageService` | `service.MessageService` |
| `comment.CommentService` | `service.CommentService` |
| — | `service.BlockService`、`service.AdminService` |

**耦合路径**：HTTP handler 使用 `service.*`，但 `internal/search/search.go` 和 `internal/message/message.go` 仍引用旧 `file.Repository` 接口。这意味着两个接口定义必须保持兼容，否则编译失败。这是迁移中间状态——旧接口仍是公共 API 表面。

**风险**：任何对 `file.Repository` 接口的修改（如增加 `IsSharedWithBatch`）需要同时更新两个包中的定义。任何对新 service 层接口的修改如果与旧包接口不一致，可能导致搜索/消息功能被静默破坏。

**建议**：这是一个必须解决的架构债务。旧包要么完成对 `internal/service` 的完全迁移，要么将旧包的接口标记为 deprecated 并冻结改动。

#### 债务 2：审计日志的"伪接口"契约

`domain.AuditLogger` 接口的文档注释写着 "a failed log must not fail the primary operation"，但 Go 的接口类型系统无法强制执行此契约。调用方用 `_ = s.audit.Log(...)` 丢弃错误，这层包装让任何底层故障（DB 连接断开、磁盘满）都被静默吞噬。这是**假可观测性**——运维面板显示请求成功率 100%，但审计线索可能已中断数小时。

#### 债务 3：Download handler 绕过 Gin 中间件

使用 `c.Status(http.StatusOK)` + `io.Copy(c.Writer, rc)` 而非 `c.Data`/`c.Blob` 意味着：
- 响应 body 不经过 Gin 的 response 写入管道
- 全局中间件对 security headers 的注入对此路径无效
- `X-Content-Type-Options`、`Content-Disposition` 等浏览器安全头缺失

#### 债务 4：idempotency 清理和异步后台任务的错误透明性

`purgeExpired()` 的 `_ = ...Delete(...).Error` 是 best-effort 的合理选择（DB 清理不应影响主路径），但无指标暴露意味着运维无法感知。同样的问题适用于 storage GC、rate-limit cleaner 等所有后台 goroutine——它们要么无声失败，要么只有启动日志。

#### 债务 5：`internal/file/` 包尚未完成迁移

`internal/file/file.go` 定义的 `Repository`、`ShareRepository`、`Service` 接口仍在被 `internal/search/` 和 `internal/message/` 使用。`internal/file/` 中的 `fileService` 实现与 `internal/service/` 中的 `fileService` 实现存在大量代码重复（文件创建、ACL 检查、audit 写入等）。

### 1.3 设计决策评估

| 决策 | 评估 | 理由 |
|---|---|---|
| 贫血领域模型 | ✅ 合适当前规模 | ~50 个实体方法，充血模型收益不抵复杂度开销 |
| 自定义 metrics 无 client_golang | ✅ 正确 | 避免大型依赖；~300 行纯 Go 实现完全够用 |
| 双套 service 层（未完成迁移） | ⚠️ 临时债务，需限期冻结 | 增加维护成本；接口分化风险；新功能开发在旧包上加还是新包上？ |
| AuditLogger fire-and-forget | ❌ 需改进 | 无降级策略（队列化、异步写入、最终一致性），不可观测 |
| Idempotency 清理 1h 间隔 vs 24h TTL | ⚠️ 可接受但需指标 | 1h 内可能累积大量过期行（最坏情况=每端点每用户 60 个过期行 × N 用户） |
| S3 PresignGetObject 无 ResponseContentType/Disposition | ❌ 安全缺陷 | 浏览器直接打开时可能导致 MIME 类型嗅探漏洞 |
| UUIDv7 主键 + 基于游标的分页 | ✅ 正确设计 | 解决大偏移量性能问题，UUIDv7 的时序特性减少 B-tree 碎片 |

---

## 2. 扩展方向

### 方向 A：审计子系统异步化与可观测化（P0）

**为什么需要**：当前 14 处 `_ = s.audit.Log(...)` 将审计错误完全隐蔽。在合规审计场景下，审计记录丢失不是运维问题而是法律问题。同时，当前的同步写入路径增加了主要请求链路的尾部延迟（audit 表写入在业务事务之外，但仍是同一 DB 连接上的 sync I/O）。

**核心挑战**：
- 审计日志需要在**不阻塞主业务路径**的前提下实现**最终持久化**
- 需要在业务层保证审计事件至少有一次写入（尽管严格的一次写入需要分布式事务，这种场景下不现实）
- 需要在审计后端过载时提供降级策略

**预期的架构变更**：

```
当前：
  service → auditRepo.Append() (同步 DB, err 丢弃)

建议：
  service → auditBuffer (channel/ring buffer) → async writer → auditRepo (DB)
                        ↓ (同步快速路径, 不可用时静默抛弃)
                  metrics: pending, dropped, write_errors
```

具体选项：

**选项 A1：带 backpressure 的无锁环形缓冲区** — 在 `internal/audit` 包内引入一个容量可配置的 ring buffer（默认 ~4096 项），service 层调用 `buffer.Append()`（O(1), 无阻塞），后台单个 goroutine 批量写入 DB。buffer 满时最新事件被丢弃（记录 dropped 计数）。复杂度低，无新依赖。

**选项 A2：通过 outbox 表实现最终一致性** — 审计事件作为 outbox 事件写入，与业务事务共享 DB 连接（不回滚）。独立 consumer 写入 audit_events 表。优点是复用已有 outbox 基础设施和重试逻辑；缺点是每个审计事件多一次 outbox 表写入和 relay 延迟（~5s）。

**建议**：选项 A1（非阻塞 buffer）更适合审计场景——低延迟、不增加主路径的 DB 事务大小。选项 A2 适合需要严格一次语义的场景，但会增加主路径的写放大。

**对现有系统的影响**：
- `domain.AuditLogger` 接口保留，但实现从同步 DB 改为 buffer 生产者
- 14 处 `_ = s.audit.Log(...)` 改为 `s.audit.Log(...)`（括号内错误检查变为 `metrics` 计数而非返回）
- 新增 `aeroid_audit_buffer_pending`、`aeroid_audit_dropped_total`、`aeroid_audit_write_errors_total` 指标
- 数据丢失窗口从"无限期静默"缩小到"buffer 满时的短暂窗口 + 异常崩溃时的 in-flight 丢失"

---

### 方向 B：文件下载安全加固 + 响应规范化（P0）

**为什么需要**：当前 Download handler 是代码库中唯一不受安全头保护、无需认证、且 Content-Type/Disposition 可控的入口。对 S3 后端，PresignGetObject URL 可直接在浏览器中打开，MIME 嗅探风险真实存在。

**核心挑战**：
- 需要在两套存储后端（local HMAC + S3 presigned）间统一安全策略
- S3 presigned URL 是 AWS 签发的，aeroid 无法在其上注入安全头——需要使用 `ResponseContentType`/`ResponseContentDisposition` 参数
- 需要保持与现有客户端的向后兼容（已有存量的 presigned URL 不含这些参数）

**预期的架构变更**：

```
Local backend:
  signing.go: presignHMAC(key|expires) 
  → presignHMAC(key|expires|content_type|disposition)
  → 更新 verifyPresignedURL 解析 query params

S3 backend:
  s3.go: PresignGetObject(ctx, key, expires)
  → PresignGetObject(ctx, &s3.GetObjectInput{..., ResponseContentType, ResponseContentDisposition}, expires)
  → 需要 s3 SDK 的 presign 参数支持

HTTP handler:
  c.Status(http.StatusOK) + io.Copy
  → c.Header("X-Content-Type-Options", "nosniff")
  → c.Header("Content-Disposition", ...)
  → c.DataFromReader(http.StatusOK, ...)  // 通过 Gin 中间件链
```

**对现有系统的影响**：
- `storage.FileStorage.PresignGet` 签名不变（附加参数走可选的 functional options 或 struct 扩展）
- `storage.Local.VerifyPresignedURL` 需要向后兼容两种签名格式（新旧并存 2 个 TTL 窗口）
- S3 的 ResponseContentType/Disposition 参数需要确保 CDN/代理行为一致

---

### 方向 C：文件共享 ACL 批量化 + 进程内缓存（P1）

**为什么需要**：当前 `IsSharedWith` 每次调用发起一次 DB `SELECT COUNT(*)`。在以下场景 100 次这类查询对应 100 次 DB 往返：
- 渲染包含 100 条文件的消息列表，每条文件需要检查共享权限
- 评论列表 + 文件附件权限检查
- gRPC 批量查询接口

**核心挑战**：
- 共享关系变更不频繁（Grant/Revoke 是用户显式操作），一致性延迟可接受（~分钟级）
- 不同调用点有不同的批量需求：列表渲染需要一次查询获取所有文件的共享状态；单个文件检查需要快速 O(1) 判断
- 缓存失效策略：需要在 Grant/Revoke 时使相关缓存失效，或依赖 TTL

**预期的架构变更**：

最低侵入方案（扩展文档中已验证）：
```
canRead:
  1. check domain.CanReadFile (fast path, 不依赖任何外部调用)
  2. 查进程内缓存 (sync.Map + 1s TTL)
  3. 缓存未命中: DB IsSharedWith → 写入缓存
  4. 返回结果

Grant/Revoke:
  5. 执行 DB 操作后清除相关缓存项
```

长期方案：
```
FileShareRepository 增加 BatchIsSharedWith(ctx, fileIDs []ID, uid string) (map[ID]bool, error)
service.canRead 内部:
  - 列表场景: 收集所有文件 ID, 一次 BatchIsSharedWith
  - 单文件场景: 进程内缓存
```

**对现有系统的影响**：
- 批次方案需要更新 `internal/repository/file_share.go` 和 `internal/service/service.go` 中的 `FileShareRepository` 接口
- 需要同步更新 `internal/file/repository_share.go` 中的旧版接口（保持兼容）
- 测试 mock 需要更新

---

### 方向 D：可观测性统一骨架 — "指标即文档"（P1）

**为什么需要**：当前指标分散在三个层级：

| 层面 | 有指标 | 无指标 |
|---|---|---|
| HTTP/gRPC 请求 | ✅ RED（requests_total, duration, errors） | — |
| SSO 依赖 | ✅ cache hit/miss, error, latency | — |
| Outbox relay | ✅ published/failed/pending/drain | — |
| 审计写入 | ❌ | ✅ 无 |
| Idempotency 清理 | ❌ | ✅ 无 |
| Storage GC | ❌ | ✅ 无 |
| 文件下载 | ❌ | ✅ 无 |
| 通知去重/限流 | ❌ | ✅ 无 |

**核心挑战**：
- 需要在不引入 Prometheus client_golang 的情况下保持一致性（当前自定义实现很轻量）
- 后台 goroutine 的指标暴露方式需要与请求路径的指标区分（counter vs gauge）
- 需要避免"为加指标而加指标"——每个指标必须有明确的告警动作

**预期的架构变更**：

在 `internal/pkg/metrics/metrics.go` 中定义一个 `DatabaseSink` 接口（参考现有 `MetricsSink` 的模式）：

```go
type DatabaseSink interface {
    ObserveQueryDuration(op, table string, dur time.Duration, ok bool)
    ObserveBackgroundError(component string, err error)
    ObserveBackgroundLag(component string, lag time.Duration)
}
```

实现者为 `*metrics.Registry`，暴露在 `/metrics` 端点。各后台组件（idempotency cleaner、storage GC、audit async writer）在构造时注入此 sink。

**新增指标**（示例）：
```
aeroid_db_background_clean_errors_total{component="idempotency_purge"}
aeroid_db_background_clean_errors_total{component="storage_gc"}
aeroid_audit_write_errors_total
aeroid_download_requests_total{backend="local|s3",presigned="true|false"}
aeroid_notification_dispatcher_dropped_total{reason="rate_limited|blocked|preference|duplicate"}
```

**对现有系统的影响**：
- `metrics.Registry` 扩展 ~50 行
- 每个后台组件的构造函数可选接受 `MetricsSink` / `DatabaseSink`
- `app.go` 的 wiring 增加 5-8 行 metric sink 注入
- 零外部依赖

---

### 方向 E：通知去重的"恰好一次"语义（P2）

**为什么需要**：当前 `NotificationDispatcher` 在 `d.notifs.Create(ctx, n)` 和 `d.notifs.MarkPublished(ctx, eventIDs)` 之间存在脆弱窗口——事件已处理但尚未标记已发布。若 relay 在此窗口后崩溃重启，同一个 outbox 事件可能被再次发送，产生重复通知。虽然当前 dispatcher 有 collapse 机制（合并同一 recipient/type/actor 在 5 分钟内的重复），但这仍是"最终去重"而非"避免重复"。

**核心挑战**：
- 需要在不使用分布式锁/事务协商的情况下实现去重
- 通知表的 `(recipient_uid, type, actor_uid, subject_id)` 唯一索引需要在 outbox handler 的 `Create` 调用前检查存在性
- GORM 的 `ON CONFLICT DO NOTHING` 在 `INSERT ... RETURNING` 模式下不返回 id——需要检查 `repository.NotificationRepository.Create` 的实现细节

**预期的架构变更**：

**选项 E1：唯一索引 + ON CONFLICT DO NOTHING**
在 `notifications` 表上添加唯一约束 `(recipient_uid, type, actor_uid, subject_id) WHERE read_at IS NULL`（部分索引，仅针对未读通知去重）。
- 如果 `Create` 使用 GORM 的 `Clauses(clause.OnConflict{DoNothing: true})`，冲突时 `RowsAffected == 0`
- 如果 `Create` 使用裸 `INSERT ... RETURNING id`，需要改为两步：`SELECT ... WHERE ...` + `INSERT ... ON CONFLICT DO NOTHING`

**选项 E2：Dispatcher 内预检（推荐）**
在调用 `d.notifs.Create` 前，先 `d.notifs.FindByRecipientTypeActorSubject(ctx, recipient, type, actor, subject, recentWindow)`。如果存在且未读，直接返回，不写新行。这不需要 schema 变更，但增加一次 DB 查询。

**选项 E3：Outbox 事件去重**
将 outbox 事件表的 handler 处理标记改为幂等的：`UPDATE outbox SET published_at = NOW() WHERE id = ? AND published_at IS NULL`。即使 relay 在 `Handle` 成功后崩溃，重启后的双重回放也会因为 `UPDATE ... WHERE published_at IS NULL` 返回 `RowsAffected == 0` 而跳过。

**建议**：选项 E2 + E3 的组合。E3 修复 relay 级别的重复发送（小的 schema 变更），E2 作为 Dispatcher 层的内防御。E1 需要更复杂的 repository 改动。

**对现有系统的影响**：
- E2 对 `NotificationRepository` 接口增加一个新方法（或扩展 `Create` 返回已存在的通知 id）
- E3 影响 `outbox.Repository.MarkPublished`——需要确认它已在使用 `WHERE published_at IS NULL`
- 测试需要覆盖重复场景

---

## 3. 接口设计建议

### 3.1 接口设计原则

当前代码库的接口设计整体良好，但有以下改进空间：

**原则 1：接口属于消费者，而非生产者**

当前，`internal/service/service.go` 定义了 `FileService` 等接口，而 `internal/file/file.go` 也定义了同样的 `Service` 接口。这是典型的"接口定义重复"反模式。建议：

- **短期**：在 `internal/file/file.go` 接口上添加 `// Deprecated: use service.FileService` 注释，冻结修改
- **长期**：将 repository 接口从 `internal/file/` 移到 `internal/repository/`（已存在），完全消除与旧包的绑定

**原则 2：fire-and-forget 操作应有显式接口契约**

当前 `AuditLogger.Log` 返回 `error` 但调用方用 `_ =` 丢弃。这是契约撒谎。两种修复方式：

```go
// 选项 1：不返回 error（诚实表达 fire-and-forget）
type AuditLogger interface {
    Log(ctx context.Context, e *AuditEvent)
}

// 选项 2：保留 error，引入 AuditSink 封装异步策略
type AuditLogger interface {
    Log(ctx context.Context, e *AuditEvent) error
}
type AuditSink struct {
    inner  AuditLogger
    buffer *ringBuffer  // 异步 buffer, 非阻塞
    metrics MetricsSink
}
func (s *AuditSink) Log(ctx context.Context, e *AuditEvent) error {
    // 从不阻塞主路径
    if !s.buffer.TryPush(e) {
        s.metrics.ObserveAuditDropped(e.Action)
    }
    return nil
}
```

建议选择选项 2——保留被调用方的 `error` 返回签名（因为底层实现确实是同步 I/O），但在上层通过 `AuditSink` 包装为异步。这样 service 调用 `s.audit.Log()` 返回 nil（不阻塞），同时保留底层错误可观测性。

**原则 3：后台 goroutine 的生命周期应统一管理**

当前 `app.go` 中每个后台任务独立管理生命周期（`stopRelay`、`stopGC`、`stopRateLimit`、`stopIdemClean`）。建议引入一个 `BackgroundTask` 抽象：

```go
type BackgroundTask interface {
    Name() string
    Start(context.Context) (stop func())
    Metrics() []MetricDef
}
```

让 `app.app` 以统一方式启动/关闭/注册指标，减少关闭遗漏风险。

### 3.2 是否需要新的抽象层

**不需要**新的纵向分层（如 application 层或事件总线层）。当前 4 层（handler-service-repository-storage）对于单进程服务架构是合适的。

但需要以下**横向抽象**：

1. **`BackgroundRunner`** — 统一后台 goroutine 生命周期 + 指标注册
2. **`AuditSink`** — 异步化审计写入（非新增层，而是现有 `AuditLogger` 实现的包装）
3. **`Cache[K, V]`** — 当前 TokenCache、UserCache、文件共享检查都有类似的 sync.Map + TTL + 后台清理模式，提取为泛型工具类型

### 3.3 向后兼容性策略

对于本次分析中识别的修复：

1. **Download 安全头** — 新增 header 不会破坏现有客户端（浏览器不依赖 header 缺失）
2. **HMAC 签名扩展** — 旧格式 URL 在 TTL 窗口内继续有效；新签名格式与旧格式共享前缀检查逻辑
3. **NotificationRepository 新增方法** — Go 接口新增方法需要更新所有 mock 实现，但生产实现只有一个
4. **FileShareRepository 批量化** — 新增 `BatchIsSharedWith`，保留 `IsSharedWith` 为单文件场景——调用方可按需选择

---

## 4. 技术选型

### 4.1 当前技术栈评估

| 组件 | 当前选型 | 评估 |
|---|---|---|
| Web 框架 | Gin | ✅ 合适。Go 生态标准选择，aero-id 的使用方式（无全局中间件依赖）合理 |
| ORM | GORM v2 | ⚠️ 合格但有隐患。GORM 的 `Clauses(clause.OnConflict{})` 行为在某些边界场景不可预测；`Count()` 的实际 SQL 执行不可控 |
| 数据库连接 | `database/sql` + GORM | ✅ 合适 |
| ID 生成 | `google/uuid` + UUIDv7 | ✅ 优秀选择。无需依赖 snowflake/雪花算法 |
| 配置 | Viper | ✅ 合适。`AERO_ID_*` 环境变量覆盖模式标准 |
| 日志 | Zap | ✅ 标准选择 |
| 指标 | 自定义实现 | ✅ 零依赖，~300 行，够用 |
| 缓存 | sync.Map + 手动 TTL | ✅ 单实例够用。分布式场景不需要 |
| 测试 | testcontainers | ✅ 合理，避免 mock 与真实行为不符 |
| gRPC | google.golang.org/grpc | ✅ 标准选择 |
| 对象存储 | 内置 local + SDK v2 | ✅ 合理 |

### 4.2 是否需要引入新技术

**不需要引入**的新基础设施：
- **消息队列**（RabbitMQ/Kafka）— outbox 模式已经很适合当前规模；NW 的异步处理不需要独立 MQ
- **Redis** — 当前所有缓存场景（token/user/file share）的 TTL 和容量需求可通过进程内内存满足
- **服务网格/API 网关** — 过度抽象

**建议考虑**的新依赖：
- **`golang.org/x/sync` 的 `errgroup`** — 替代 `app.go` 中的手写 goroutine 错误收集。零新依赖（已是间接依赖）
- **`github.com/cespare/xxhash/v2`** — 如果启用通知 collapsing 的哈希去重，比 `crypto/sha256` 快 10x。极小的依赖

**不建议引入**的依赖：
- Prometheus `client_golang` — 当前自实现 metrics 包已够用，引入 client_golang 会增加约 2MB 的间接依赖和启动开销
- OpenTelemetry SDK — `internal/pkg/tracing` 已基于 OTel SDK，不需要替换
- 任何分布式锁实现

### 4.3 自建 vs 采购的决策

当前不涉及任何"采购"决策——所有依赖都是开源 Go 库。重要的是"自建 vs 使用标准库"的决策：

| 组件 | 自建理由 |
|---|---|
| metrics | 无依赖、~300 行、功能精确匹配需求。Prometheus client_golang 的 label 开销和 goroutine 成本对 aero-id 这样的小服务过大 |
| idgen | 基于 `google/uuid` 的 UUIDv7 包装仅 ~50 行。购买语义不适用 |
| 审计 buffer | 基于 `chan` 或 `ring` 的自实现不到 100 行。不需要引入 Kafka |
| 进程内缓存 | sync.Map + TTL cleaner ~30 行。不需要引入 Redis 客户端和 Redis 运维 |

---

## 5. 实施路线图

### 5.1 优先级排序

| 优先级 | 方向 | 理由 |
|---|---|---|
| **P0** | A — 审计异步化 | 当前审计丢失不可观测，是合规风险 |
| **P0** | B — 下载安全加固 | 浏览器端 MIME 嗅探是具体的安全攻击面 |
| **P1** | C — 文件共享批量化 + 缓存 | 性能问题已在评审文档中量化 |
| **P1** | D — 可观测性统一 | 运维可调试性，与 P0 的两个方向（审计错误可观测、idempotency 清理可观测）联动 |
| **P2** | E — 通知去重 | 概率性缺陷，严重程度低于安全和合规问题 |

### 5.2 阶段划分

#### 阶段 1：安全与合规加固（2-3 天）

| 里程碑 | 交付物 | 涉及文件 |
|---|---|---|
| M1.1 | Download handler 增加安全头 + 改为 `c.Data` | `internal/handler/http/file.go` |
| M1.2 | S3 PresignGetObject 附加 ResponseContentType/Disposition | `internal/storage/s3.go` |
| M1.3 | HMAC 签名扩展到覆盖 header 参数（向后兼容旧格式） | `internal/storage/signing.go` |
| M1.4 | 审计 buffer 实现 + 指标 | `internal/audit/buffer.go`（新文件）、`internal/pkg/metrics/metrics.go` |
| 验证 | `make test && make lint` | — |

**风险点**：S3 presigned URL 的额外参数可能与 CDN 行为冲突。需在现有测试环境中验证。

**缓解策略**：分两步部署——先 enable 安全头（无破坏性），再变更 S3 presigned（需验证 CDN 兼容性）。

#### 阶段 2：迁移清理 + 接口收敛（3-4 天）

| 里程碑 | 交付物 |
|---|---|
| M2.1 | 审计 `internal/file/` 中哪个接口被外部消费者使用，标记旧接口 `Deprecated` |
| M2.2 | 迁移 `internal/search/` 和 `internal/message/` 从旧接口到 `internal/repository/` |
| M2.3 | 删除/冻结旧 service 实现（`internal/file/service_*.go`）或用编译器检查防止新代码依赖它们 |
| M2.4 | 统一 `BackgroundTask` 生命周期管理（可选，可后移） |

**风险点**：`internal/search/` 和 `internal/message/` 使用旧接口的具体位置需逐行审计。删除旧接口会暴露未预料到的依赖。

**缓解策略**：不做删除，仅冻结（`go vet` 自定义检查器或 code review 规则）。清理可以分阶段进行。

#### 阶段 3：性能与可观测性（3-4 天）

| 里程碑 | 交付物 | 涉及文件 |
|---|---|---|
| M3.1 | 文件共享缓存 + `BatchIsSharedWith` | `internal/repository/file_share.go`、`internal/service/file.go` |
| M3.2 | 所有后台 goroutine 指标注入 | `internal/idempotency/store.go`、`internal/storage/gc/`、`internal/audit/` |
| M3.3 | `metrics.Registry` 扩展 + 统一暴露 | `internal/pkg/metrics/metrics.go` |
| M3.4 | Grafana 面板模板（可选） | `deployments/grafana/`（新目录） |
| 验证 | `make test && make test-integration` | — |

#### 阶段 4：通知去重 + 后续改进（2-3 天）

| 里程碑 | 交付物 |
|---|---|
| M4.1 | Dispatcher 内预检去重（E2） |
| M4.2 | Outbox MarkPublished 幂等化（E3） |
| M4.3 | 通知去重集成测试（验证崩溃重启场景） |

### 5.3 风险矩阵

| 风险 | 概率 | 影响 | 缓解 |
|---|---|---|---|
| S3 presigned URL 的 ResponseContentType 参数与新版本 AWS SDK 不兼容 | 低 | 高 | 先在 staging 验证；备选方案：在 Download handler 中增加 URL 重写拦截器 |
| 双套 service 层导致新功能在两个地方各实现一次 | 中 | 中 | Code review 检查新功能是否需要同时更新旧接口；明确新旧边界 |
| 审计 buffer 在服务 OOM 时丢失 in-flight 事件 | 低 | 中 | Buffer 容量设为固定上限；崩溃重启后的丢失窗口≤buffer flush interval |
| 通知去重预检增加 DB 查询导致写路径延迟 | 低 | 低 | 预检查询走仅索引扫描；开启 connection pooling 对连接的影响可忽略 |
| 文件共享缓存引入过时数据导致误拒合法请求 | 低 | 中 | TTL 设为 1s（最坏延迟=1s）；用户重新请求即可纠正 |

---

## 总结

aero-id 的代码库整体架构质量优秀——分层清晰、依赖方向正确、接口设计一致。本次分析识别的五个方向中，**审计异步化**和**下载安全加固**是 P0 级别的合规/安全问题，应在下一个迭代中优先解决。**双套 service 层**是最大的架构债务，但在实际操作层面可以采取"冻结旧接口、渐进迁移"而非"一次性重写"的策略来管理风险。

核心建议的优先级框架：

| 紧急 | 重要 | 行动 |
|---|---|---|
| ✅ | ✅ | Download 安全修复、审计异步化 | P0, 立即行动 |
| ❌ | ✅ | 指标统一、缓存批量化 | P1, 下一迭代 |
| ❌ | ❌ | 通知去重 | P2, 按需实施 |

整个改进计划的代码改动量估计在 **500-800 行**（不包括测试），可以在 2 周内完成全部分析发现的问题，核心安全与合规修复在 2-3 天内即可交付。
