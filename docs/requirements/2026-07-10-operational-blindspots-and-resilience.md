# aero-id — 被既有分析遗漏的五个运维与韧性方向

> **日期**: 2026-07-10  
> **视角**: 资深架构师 + 产品经理  
> **方法**: 全局代码扫描后，逐一比对 `docs/requirements/` 下 16 份及 `docs/REVIEW_*` 下 7 份既有分析文档的主题索引，确认零系统覆盖后提出  
> **无代码变更**

---

## 交叉验证方法

1. 提取 `docs/requirements/`（16 份）全部方向标题 → **~65 个方向**
2. 提取 `docs/REVIEW_*`（7 份）全部方向标题 → **~25 个方向**
3. 合并去重 → **~70 个唯一方向**
4. 回扫 `internal/` 全部 ~25 个 Go 包 + 22 份迁移 SQL → 验证代码级缺口
5. 确认以下 5 个方向在**所有既有文档中均无系统性分析**

### 已有文档已覆盖的方向（举例，证明交叉验证的有效性）

群聊/频道 · 统一活动流 · 文件版本管理 · 公开分享链接 · AI/ML 智能 · 多租户/组织 · GraphQL/BFF · 特征开关 · 死信控制台 · 文件加密 · 移动推送 · 存储配额 · 批量操作 · 实时在线状态 · 消息召回/定时发送 · Bot 框架 · 资源计量 · 多设备会话 · 回收站/软删除恢复 · 审核工作台 · 文件预览管线 · 分布式限流 · 多地域部署 · Webhook 事件平台 · Schema 版本化 · 定时动作引擎 · 自适应限流 · 数据平台/嵌入式分析 · 读扩展性 · ……（共 ~70 个）

---

## 五项全新方向概览

| # | 方向 | 类别 | 代码缺口 |
|---|------|------|---------|
| 1 | **后台 Goroutine 生命周期管理** | 运维/可靠性 | outbox relay/GC sweeper/缓存 cleaner 等 5+ 后台 goroutine 无健康检查、无崩溃检测、无自动重启、无法从 `/metrics` 或 `/readyz` 感知存活状态 |
| 2 | **内容编辑历史与版本管理** | 产品/合规 | 评论和消息编辑后旧内容被覆盖。无版本保留、无 diff、无审核回滚能力、无编辑频率滥用防护 |
| 3 | **SSO 用户生命周期与孤儿数据治理** | 数据治理/合规 | SSO 删除用户后 aero-id 无感知、无数据清理、无资源转移、无合规性标记。孤儿数据永久累积 |
| 4 | **数据库断路器与连接池韧性** | 韧性/故障隔离 | SSO 有完整断路器，但数据库连接池无任何保护——DB 故障请求全阻塞，级联失败波及整个服务 |
| 5 | **端点级差异化限流与流量优先级** | 运维/公平性 | 全局唯一限流器；搜索、通知列表、文件列表等重型端点与轻量端点共享配额。管理操作无条件绕过 |

---

## 方向一：后台 Goroutine 生命周期管理

### 问题陈述

aero-id 当前有 5 个独立的后台 goroutine，均使用 "Start → stop func" 模式启动：

| 组件 | 文件 | 启动方式 | 周期 |
|------|------|---------|------|
| Outbox Relay | `internal/outbox/relay.go` | `Start(interval)` | 5s tick |
| Storage GC Sweeper | `internal/storage/gc/sweeper.go` | `Start(interval)` | 可配置 |
| 限流器 Idle Bucket Cleaner | `internal/pkg/ratelimit/ratelimit.go` | `RunCleaner(interval)` | 1min |
| Idempotency Key Cleaner | `internal/idempotency/store.go` | `RunCleaner(interval)` | 1h |
| SSO Token Cache Cleaner | `internal/sso/token_cache.go` | `RunCleaner(interval)` | 可配置 |
| SSO User Cache Cleaner | `internal/sso/user_cache.go` | `RunCleaner(interval)` | 可配置 |

**核心问题：** 这些 goroutine 没有统一的生命周期管理。

1. **无健康检查。** 如果 outbox relay 的 goroutine 因 panic 退出（defer recover 未覆盖的情形），没有任何机制能检测到它已死亡。事件会永远堆积在 outbox 表中，通知不会投递，但服务器仍显示 `200 OK`。

2. **无重启机制。** 一旦 goroutine 退出，它就永久退出了。没有 supervisor/watchdog 尝试重新启动它。

3. **无可见性。** `/readyz` 只检查 DB + SSO，不检查后台组件是否存活。运维人员无法知晓 relay、sweeper、cleaner 是否在正常运行。

4. **无指标。** 只有 outbox relay 上报部分指标（published/failed/lag）。GC sweeper 的 `Run()` 返回 `Stats` 但仅日志记录，不暴露给 Prometheus。cleaner 完全没有指标。

### 为什么既有分析未覆盖

- `REVIEW_2026-06-30_FLOW_CONCURRENCY_CACHE.md` 分析了 outbox relay 的并发模型和锁竞争，但**从未讨论 goroutine 退出后的运维检测**。它的视角是"正常运行时的并发正确性"，而非"异常运行时的检测与恢复"。
- `REVIEW_2026-06-30_INFRA_DEEP.md` 详细分析了监测基础设施，但聚焦于 HTTP/gRPC RED 指标和 SSO 健康，未涉足后台 goroutine 的存活监测。
- 所有 16 份 `requirements/` 文档的方向均围绕"扩展功能"或"修复边界情况"，未将"goroutine 生命周期管理"识别为运维方向。

### 代码级证据

| 位置 | 发现 |
|------|------|
| `internal/app/app.go:113-115` | `stopRelay`、`stopGC`、`stopRateLimit`、`stopIdemClean` 是各自独立的 `func()`，没有包装成集合或 registry |
| `internal/app/app.go:235-253` | `Run()` 中 goroutine 启动后即无管理——没有 `sync.WaitGroup` 跟踪它们是否仍在运行 |
| `internal/app/app.go:279-298` | `shutdown()` 依次调用 stop 函数，但如果某个 goroutine 已经先退出了（panic/error），stop 只是关闭 channel 而已——恐慌已无法恢复 |
| `internal/outbox/relay.go:161-176` | `Start()` 中的 goroutine 被 `done` channel 控制，但没有捕获 panic、没有退出报告、无重试逻辑 |
| `internal/storage/gc/sweeper.go:184-198` | 同上模式——goroutine 内的 `Run()` 错误仅日志记录，不传播给调用方 |
| `internal/pkg/ratelimit/ratelimit.go:104-120` | `RunCleaner` 的 goroutine 内 panic 会导致整个 cleaner 永久沉默 |
| `internal/sso/token_cache.go` | `RunCleaner` 的 goroutine 在 `for/select` 循环中，goroutine 内捕获 panic 但只 recover 不重启 |
| 全代码库 | 无 `BackgroundJobRegistry`、无 `JobHealth` 接口、无 `[]JobStatus` 暴露端点 |

### 建议方案要点

1. **BackgroundJob 接口：**
   ```go
   type BackgroundJob interface {
       Name() string
       Start() (stop func(), ready <-chan struct{})
       Health() JobHealth  // 返回当前状态：running/stopped/crashed + 上次运行时间 + 处理总数
   }
   ```

2. **Registry + Health 端点：**
   - 在 `app.go` 中注册所有后台 job
   - 暴露 `GET /debug/jobs`（限管理角色）查看每个 job 的 `Health`
   - 在 `/readyz` 中可选地 report job 状态（默认不 critical，避免因非关键 job 死亡导致实例被摘除）

3. **Goroutine Watchdog：**
   - 每个 job 的 goroutine 在 panic 时通过 `defer` 调用注册的重启回调
   - Watchdog 记录连续崩溃次数，超过阈值（如 3 次/5 分钟）则停止重启并标记 dead

4. **指标暴露：**
   - `aeroid_job_up{name="outbox_relay"} 1/0`
   - `aeroid_job_restarts_total{name="gc_sweeper"}`
   - `aeroid_job_last_run_timestamp_seconds{name="idempotency_cleaner"}`
   - `aeroid_job_processed_total{name="outbox_relay"}`（已存在但仅 outbox 有）

### 优先级判断

| 维度 | 评分 |
|------|------|
| 运维价值 | 🔥🔥🔥🔥（没有它，后台组件静默死亡是线上盲区） |
| 实现成本 | 低（纯基础设施代码，不涉及业务逻辑变更） |
| 用户感知 | 间接——运维发现能力提升 |
| 架构杠杆 | 中——统一 job 管理为未来更多后台任务奠定基础 |

**建议**: P0 — 在大规模部署前必须完成。没有任何后台 job 的存活保证。

---

## 方向二：内容编辑历史与版本管理

### 问题陈述

当前系统支持评论和消息的**编辑**（会写 `edited_at`），但旧内容被**永久覆盖**：

- `internal/service/message.go` 的 `Edit()` 直接调用 `messages.UpdateContent(ctx, id, content, now)` —— 旧内容无处可查
- `internal/service/comment.go` 的 `Edit()` 同样直接覆盖
- `domain.Message` 和 `domain.Comment` 均无 `EditHistory` 结构
- 审计日志未记录编辑前后的内容变化
- 没有任何编辑频率控制：用户每秒可编辑同一消息 10 次

**为什么这是问题：**

1. **信任与安全。** 用户 B 收到消息 "明天我转给你 5000 元"，一天后 A 编辑为 "明天我借 5000"。没有编辑历史，B 无法举证。这在沟通产品中是一个严重的安全缺陷。

2. **审核需求。** 模审员需要查看被举报评论的编辑历史——"被举报的版本是原始内容还是编辑后的内容？"如果编辑历史不存在，用户可以通过先发违规内容再迅速编辑来逃避审核。

3. **合规与监管。** 金融、医疗等受监管场景要求保留消息的完整变更历史。aero-id 当前无法满足。

4. **滥用风险。** 没有编辑频率限制，用户可滥用编辑功能实现"反向篡改"——发送无害内容，等对方已读后，编辑为包含钓鱼链接的版本。

### 为什么既有分析未覆盖

- `docs/REVIEW_2026-06-30_PRODUCT_GAPS.md` 分析了评论编辑和消息编辑的功能性正确性，但关注点在于"编辑是否正常写入 edited_at"，而非"编辑历史是否被保留"。
- `docs/requirements/2026-07-10-remaining-architectural-gaps.md` 有一段提到"消息没有 EditHistory 结构"，但仅是技术观察，没有作为一个完整的产品方向进行分析（无用户场景、无实施策略、无隐私权衡）。
- 其他 22 份文档均未涉及编辑历史的版本管理。

### 代码级证据

| 位置 | 发现 |
|------|------|
| `internal/service/message.go` — `Edit()` | `repo.Update(ctx, msg.ID, map[string]any{"content": req.Content, "edited_at": now})` — 直接覆盖内容 |
| `internal/service/comment.go` — `Edit()` | 同上模式：`repo.UpdateContent(ctx, id, req.Content)` |
| `internal/domain/message.go` | `Message` 结构有 `EditedAt *time.Time`，无 `EditHistory` |
| `internal/domain/comment.go` | 同上 |
| `internal/repository/message.go` | `Update()` 使用 GORM 的 `Updates()` 方法覆盖全部字段 |
| `internal/repository/comment.go` | `UpdateContent()` 直接 `UPDATE content = ?` |
| `migrations/` 全部 22 份 | 无 `message_edit_history` 或 `comment_edit_history` 表 |
| `audit.Log()` 调用处 | 编辑时调用 `audit.Log()` 时 `Meta` 为空——不传递新旧内容对比 |
| 全代码库 | 无编辑频率检测逻辑（`table: rate_limit_edits_by_user`） |

### 建议方案要点

#### 数据模型

```sql
CREATE TABLE message_edit_history (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    message_id  UUID NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    content     TEXT NOT NULL,          -- 编辑前的旧内容
    edited_at   TIMESTAMPTZ NOT NULL,   -- 什么时候被编辑为这个版本的
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_message_edit_history_message_id ON message_edit_history(message_id, edited_at DESC);

-- comment 同样
```

#### 关键设计决策

| 决策点 | 选项 | 建议 |
|--------|------|------|
| 保留多少个版本 | 有限（最后 10 版）vs 无限 | 有限——默认保留最后 10 个版本，受配置控制。避免无限增长的存储 |
| 谁可以查看历史 | 仅参与者 vs 参与者+审核员 | 参与者（消息的 from/to，评论的 target 参与者）+ 拥有 `content.moderate` 权限的审核员 |
| 编辑频率限制 | 每秒/每分钟最大编辑次数 | 必须实现。建议：同一消息/评论每分钟最多编辑 3 次 |
| 编辑后是否通知 | 静默 vs 推送 "message.edited" 事件 | 必须通知接收方。通过 outbox 发出 `message.edited` 事件 |
| 历史过期清理 | 保留 90 天后自动清理 | 可配置。受合规需求影响 |

#### 附加约束

- 如果评论已被隐藏（`status = hidden`），用户仍可编辑内容但只能看到隐藏版本——编辑历史对审核员可见
- 编辑频率限制应独立于全局 rate limit，按用户+资源维度计数
- 通过 outbox 透传 `message.edited` 事件 → NotificationDispatcher 给接收方推送 "消息已编辑" 提醒

### 优先级判断

| 维度 | 评分 |
|------|------|
| 用户感知价值 | 🔥🔥🔥（信任增强，减少"篡改"风险） |
| 合规/监管价值 | 🔥🔥🔥🔥（关键行业必备） |
| 实现成本 | 中（新表 + 服务层改写 + 历史查询 API） |
| 依赖前置 | 无 |

**建议**: P1 — 在面向外部客户的生产部署前完成。如果 aero-id 用于任何与金钱/法律相关的通信，这是 P0。

---

## 方向三：SSO 用户生命周期与孤儿数据治理

### 问题陈述

aero-id 不存储用户账号——每个资源通过 `owner_uid` / `from_uid` / `to_uid` 引用 SSO 的 uid。但当前系统**完全感知不到 SSO 侧的用户状态变化**：

- 用户被 SSO 删除或停用时，aero-id 无任何通知机制
- 该用户的文件、消息、评论继续存在，但 uid 不再可解析（SSO 的 `GetUsers` 返回空）
- 共享给他人的文件留下孤儿引用——受赠者无法知道文件已经被删除用户的 uid 不可解析
- 发送给已删除用户的消息永远不可读（`to_uid` 不再存在），但发件人不知道
- 存储空间被已删除用户的数据永远占用
- 无法满足 GDPR "被遗忘权"（Right to erasure）要求——没有用户的明确删除请求机制来清理其数据

### 为什么既有分析未覆盖

- `2026-07-10-high-value-extensions.md` 在第五方向（多设备会话管理）中有一句话提到了 SSO `user.deleted` 事件，但仅是附注——**没有将"用户生命周期与孤儿数据治理"作为独立方向分析**。
- 所有其他文档都假设"用户永远存在"——即使 `expansion-directions.md` 的 "file sharing" 方向也假设受赠者永远是可解析的 uid。
- 无任何文档讨论 GDPR 合规、数据最小化、或用户删除的数据生命周期。

### 代码级证据

| 位置 | 发现 |
|------|------|
| `internal/sso/client.go` | `ValidateToken` 和 `GetUsers` 只返回验证结果——不返回"用户是否被停用"的语义信息 |
| `internal/sso/user_cache.go` | 缓存的结构只存 `UID`、`Nickname`、`Avatar`——无 `Active` 或 `DeletedAt` 状态 |
| `internal/sso/ssopb/sso.proto` | `ValidateTokenResponse` 无 `user_status` 字段；`GetUsersResponse` 无 `deleted_users` 列表 |
| `internal/domain/shared.go` | `SystemSenderUID = "system"` 是唯一特殊 uid；无 `DeletedUserPlaceholder` |
| `internal/service/message.go` — `Send()` | 发送消息时检查 `to_uid` 是否为目标用户（通过 SSO 解析），但 SSO 解析不到用户时仅返回 `InvalidArgument`——"用户不存在"和"用户已停用"被同等对待 |
| `internal/service/file.go` — `ListFiles()` | 不加过滤地返回 owner 所有文件——即使 owner 已从 SSO 删除 |
| `internal/repository/` 全部 | 无 `WHERE owner_uid NOT IN (deleted_uids)` 查询模式；无 `orphan_cleanup` 函数 |
| `migrations/` 全部 22 份 | 无 `deleted_uid` 或 `data_retention_policy` 表 |
| 全代码库 | 无定时任务扫描"无效 uid"；无 `user_deletion_request` 表或 API |

### 建议方案要点

#### 检测机制

1. **SSO 侧事件驱动（最佳方案）：** SSO 在用户删除/停用时推送事件到 aero-id（通过现有 outbox webhook 通道或新的事件端点）。事件包含 `event: user.deleted`, `uid: "u_abc123"`, `timestamp: ...`。

2. **主动扫描（回退方案）：** 如果 SSO 不支持事件推送，aero-id 定期调用 `GetUsers(uid_list)`，标记不再返回的 uid。需要批量 upsert 一个 `deactivated_uids` 表。

3. **运行时检测：** 当 SSO 的 `ValidateToken` 返回 `NotFound` 时（之前是有效的 token，现在无效），记录 uid 为 "可能已停用"。

#### 数据治理策略

| 策略 | 描述 | 建议应用 |
|------|------|---------|
| **匿名化** | 将 `owner_uid`/`from_uid` 替换为 `<deleted_user>` 占位符，保留数据内容 | 评论、消息内容保持可用性 |
| **硬删除** | 删除所有关联数据 | 明确符合 GDPR 删除请求的数据 |
| **保留+冻结** | 保留数据但不允许任何操作（不可编辑、不可分享、不可删除） | 监管保留期内的数据 |
| **标记透明** | 在 UI 中标记 "该用户已注销" | 所有显示 uid 的场景 |

#### API 扩展
- `POST /v1/admin/users/:uid/gdpr-delete` — 触发完整的用户数据删除工作流
- `GET /v1/admin/orphan-report` — 报告当前孤儿数据统计

### 优先级判断

| 维度 | 评分 |
|------|------|
| 合规价值 | 🔥🔥🔥🔥🔥（GDPR/CCPA/个人信息保护法硬性要求） |
| 存储成本 | 中高——累积效应明显 |
| 实现成本 | 中（取决于 SSO 侧配合程度） |
| 依赖前置 | SSO 侧需提供用户状态变化事件 |

**建议**: P1 — 有明确合规要求时 P0。至少实现主动扫描机制 + 匿名化策略。

---

## 方向四：数据库断路器与连接池韧性

### 问题陈述

aero-id 的 SSO 依赖有完整的韧性栈：断路器、重试、退避、过期缓存回退（serve-stale）。但数据库依赖**完全没有类似的保护**：

```
SSO 韧性栈（完整）：                        Postgres 韧性栈（缺失）：
├── Circuit breaker (open/closed/half)      ├── (无)                    
├── Bounded retries + backoff               ├── (无)                    
├── Stale-cache fallback                    ├── (无)                    
├── Singleflight stampede protection        ├── (无)                    
├── Health check (ping)                     ├── Health check (ping) — 仅此而已
└── Timeout (per-call + keepalive)          └── Timeout (conn max lifetime)
```

**数据库故障的连锁效应：**

1. Postgres 宕机或连接池耗尽（max_connections = 100-500）
2. 每个新请求调用 `db.Query` → 阻塞等待池中的连接
3. Gin 默认的 goroutine 池很快被阻塞的请求占满
4. HTTP 和 gRPC 服务器的 goroutine 被耗尽
5. SSO 验证（不需要 DB 的步骤）也无法进行——因为 middleware  goroutine 在等 DB
6. **即使 SSO 完全正常，整个服务也因为 DB 不可用而瘫痪**

这与 SSO 故障的隔离性形成鲜明对比——SSO 故障时，serve-stale token cache 允许认证仍正常工作，只是部分功能退化。

### 为什么既有分析未覆盖

- `docs/REVIEW_2026-06-30_FAILURE_ABUSE_API.md` 的故障注入测试提到 `FaultDBConnectionPoolExhaustion` 和 `PoolSize: 1` 作为测试场景，但仅作为"如何验证"的示范，**没有作为架构改进方向提出**。
- `docs/requirements/2026-07-10-edge-cases-and-performance.md` 的数据库部分只讨论了连接池大小和读扩展性（read replicas）——没有讨论故障隔离（circuit breaker）。
- `docs/REVIEW_2026-06-30_SECURITY_OPS.md` 虽然涵盖 SSO 韧性，但分析范围明确限定在 SSO 依赖上，未延伸至其他基础设施依赖。
- 所有其他文档均假设"数据库永远可用"。

### 代码级证据

| 位置 | 发现 |
|------|------|
| `internal/pkg/db/db.go` | `Open()` 仅配置连接池参数（MaxOpenConns/MaxIdleConns/ConnMaxLifttime）——无断路器、无健康检查回调、无连接池耗尽检测 |
| `internal/middleware/auth.go` | Auth 中间件先调用 SSO 验证（这一步不需要 DB），通过后将 uid 注入 context，handler 再访问 DB——但 middleware 之上没有"DB 是否可用"的提前检查 |
| `internal/repository/db.go` | `Connect()` 使用 GORM 的 `AutoMigrate`/`NowFunc`——无连接池监控 |
| `internal/app/app.go` — `New()` | 创建 repository 时发现 DB 连接不可用会返回错误——但运行时 DB 断开无恢复路径 |
| `internal/handler/http/health.go` | `/readyz` 对 DB 的检查就是一次 `PingContext`——ping 通过不代表连接池健康 |
| 全代码库 | 无 `HasDegradedDatabase()` 判断逻辑；无 "DB 不可用时只读操作走 DB、写操作返回 503" 的降级策略；无连接池指标（当前等待数、池大小、超时计数） |

### 建议方案要点

#### 1. 数据库断路器

```go
// DB circuit breaker 的工作方式：
// - 正常：所有请求正常访问 DB
// - 出现 x 个连接失败（timeout/connection refused/EOF）→ 断路器打开
// - 打开状态：所有 DB 请求立即返回错误，不做实际连接尝试
// - 半开：定时（如 30s）尝试一个查询，成功→关闭断路器，失败→保持打开
// - 恢复到正常后，请求逐渐放回 DB
```

#### 2. 降级策略

| 场景 | 行为 |
|------|------|
| DB 正常 | 全部功能正常 |
| DB 连接数 > 80% 池上限 | 返回 `Retry-After` header 或 503，拒绝新增写入请求 |
| 断路器打开（DB 不可用） | 所有写操作返回 503 + 降级提示；读操作尝试最近缓存（如果实现）或返回 503 |
| DB 恢复 | 断路器感知后逐步恢复全部功能 |

#### 3. 关键指标

- `aeroid_db_connections_in_use`（GORM 连接池使用中连接数）
- `aeroid_db_connections_idle`（空闲连接数）
- `aeroid_db_connection_wait_duration_seconds`（等待连接的时间）
- `aeroid_db_circuit_breaker_state`（0=closed, 1=open, 2=half-open)
- `aeroid_db_errors_total{error_type="timeout|conn_refused|query_failed"}`

#### 4. 实现注意事项

- 断路器应在 `repository` 层或 GORM 的 `plugin` 层实现，对所有 repository 透明
- 不要自己实现完整的断路器——Go 生态已有 `gobreaker`、`sony/gobreaker` 等成熟库
- 断路器参数（失败阈值、冷却期、半开尝试间隔）应为可配置，类似于 SSO breaker
- 连接池指标可以通过 GORM 的 `DB.Stats()` 暴露（已存在于 `database/sql`）

### 优先级判断

| 维度 | 评分 |
|------|------|
| 故障隔离价值 | 🔥🔥🔥🔥🔥（DB 是最关键的依赖，它的故障不应让整个服务瘫痪） |
| 用户感知 | 🔥🔥🔥（降级比完全不可用好） |
| 实现成本 | 中（使用现有库可降低成本） |
| 架构杠杆 | 高——为未来所有数据依赖统一韧性模式 |

**建议**: P0 — 与方向一（后台 goroutine 管理）同等优先级。在无降级的单 DB 架构下，DB 故障 = 服务完全不可用。

---

## 方向五：端点级差异化限流与流量优先级

### 问题陈述

当前 rate limiter 是**全局的**——所有经过认证的请求共享同一个基于 uid 的 token bucket：

```yaml
rate_limit:
  enabled: true
  rps: 50         # 每 uid 全局每秒 50 个请求
  burst: 100
```

**问题：**

1. **成本不匹配。** 一个 `GET /v1/messages` 的轻量列表和 `POST /v1/search?q=...` 的全文搜索消耗截然不同的资源（搜索涉及多个 ILIKE 扫描，计算成本可能是列表的 100 倍），但它们消耗的是同一个 token bucket。

2. **无优先级。** 管理员 `/v1/admin/audit-log` 与普通用户共享限流配额。在流量高峰时，管理操作可能被普通流量阻塞——阻止了运营人员恢复服务。

3. **无隔离。** 一个客户端的 burst（发送 100 个请求）可能耗尽同一 uid 的全局配额并导致该 uid 对其他所有端点的请求也被限流——一个端点污染了全部端点。

4. **无可见性。** 用户收到 429 但完全不知道**哪个操作**被限流了、**限流窗口多长**、**是否有更宽松的备选端点**。

### 为什么既有分析未覆盖

- `2026-07-10-product-directions.md` 提出了"自适应限流"（adaptive rate limiting——根据系统负载动态调整限流阈值），但这与差异化限流是正交的方向。自适应限流解决的是"阈值多少合适"的问题；差异化限流解决的是"不同操作应有不同阈值"的问题。
- 所有 16 份 `requirements/` 文档均将 rate limiter 视为**全局横切关注点**，从未讨论端点级别的差异化或优先级。
- `edge-cases-and-performance.md` 提到"block API 无专属限额"，但缺乏系统性分析。

### 代码级证据

| 位置 | 发现 |
|------|------|
| `internal/middleware/ratelimit.go` | `RateLimit(l *ratelimit.Limiter)` — 所有请求走同一个 limiter，按 `uid:` + `ip:` 维度 key |
| `internal/middleware/ratelimit.go:55-57` | `rateLimitKey()` 只区分 uid 和 IP，不区分端点 |
| `internal/app/app.go:200-210` | 只有一个 `limiter` 实例传递给路由器 `RouterConfig{... RateLimiter: limiter}` |
| `internal/handler/grpc/server.go` | gRPC 端的 `RateLimitInterceptor` 同样使用单一 limiter |
| `internal/pkg/ratelimit/ratelimit.go` | `Allow(key)` 方法的 key 是字符串，没有命名空间或优先级概念 |
| `internal/handler/http/admin.go` | `AdminHandler.ListAudit()` 无限流豁免——管理员在流量高峰无法访问审核队列 |
| `internal/handler/http/search.go` | 搜索端点与普通操作共享配额——计算密集型操作未获得专属限额 |
| `internal/handler/http/notification.go` | 通知列表/已读计数共享同一配额——频繁拉取的客户端可能耗尽配额并影响消息发送 |

### 建议方案要点

#### 1. 层级化的 limiter 架构

```
每个 uid（或 IP）可以有多个 limiter：
├── 全局 limiter（兜底）
│   ├── 端点组 limiter: read（列表/详情）—— 100 rpm
│   │   └── 具体端点 limiter: GET /v1/search —— 20 rpm（因为计算重）
│   ├── 端点组 limiter: write（创建/更新/删除）—— 30 rpm
│   └── 管理组 limiter: GET /v1/admin/* —— 无限（仅对 admin 角色生效）
```

#### 2. 配置模型

```yaml
rate_limit:
  enabled: true
  global:
    rps: 50     # 每 uid 全局兜底
    burst: 100
  admin:
    bypass: true  # 有 admin 角色的用户不受限流
  policies:
    - route: "GET /v1/search"
      rps: 5
      burst: 10
      description: "全文搜索是计算密集型操作"
    - route: "GET /v1/notifications"
      rps: 30
      burst: 60
      description: "通知轮询客户端需要更高频率"
    - route: "POST /v1/files"
      rps: 3
      burst: 5
      description: "大文件上传较少但可能重试"
    - group: "admin"
      routes:
        - "/v1/admin/*"
        - "/*/ops*"
      bypass: true
      description: "管理操作不受限流"
```

#### 3. 可见性增强

- 429 响应体增加 `X-RateLimit-{Policy,Remaining,Reset}` 头域
- 新增 `GET /v1/rate-limit/status` 端点（认证后可查询自己的限流状态）——用户可见的限流信息
- 超出限流时，错误码应区分是全局限流还是端点级限流

#### 4. 实现要点

- 引入 `RateLimitPolicy` 接口，支持全局 + 端点级 + 组级策略链
- 端点级策略通过 route pattern 匹配（Gin 路由树或前缀匹配）
- 管理绕过通过现有的 `PermissionChecker` 配合 `middleware.RequirePermission` 
- gRPC 和 HTTP 共享同一套策略定义（gRPC 通过 full method name 匹配）

### 优先级判断

| 维度 | 评分 |
|------|------|
| 公平性价值 | 🔥🔥🔥🔥（保护计算密集型端点不被打满，确保轻量操作始终可用） |
| 运维价值 | 🔥🔥🔥🔥（管理员在故障时能绕开限流恢复服务） |
| 实现成本 | 中（现有 limiter 架构可扩展，重点在于配置解析和路由匹配） |
| 用户感知 | 🔥🔥🔥（清晰的限流信息让用户知道如何调整行为） |

**建议**: P1 — 在生产环境出现限流投诉或管理操作被限流时升至 P0。

---

## 综合路线图建议

```
Phase 1 (P0 — 立即):
├── 方向一：后台 Goroutine 生命周期管理（2-3 天）
│   └── BackgroundJob 接口 + Registry + 基本健康端点
├── 方向四：数据库断路器与连接池韧性（3-5 天）
│   └── 断路器集成 + 连接池指标 + 降级策略
│
Phase 2 (P1 — 下一个迭代):
├── 方向二：内容编辑历史与版本管理（5-8 天）
│   └── edit_history 表 + API + 编辑频率限制 + 编辑通知
├── 方向五：端点级差异化限流（4-6 天）
│   └── 策略配置 + 层级 limiter + 可见性头域
│
Phase 3 (P1 — 有合规需求时):
├── 方向三：SSO 用户生命周期与孤儿数据治理（5-10 天）
│   ├── 事件监听/主动扫描（取决于 SSO 能力）
│   ├── 匿名化/删除工作流
│   └── 管理 API
```

### 依赖关系

```
方向一（后台 goroutine 管理）
  └── 方向二（编辑历史）：需要方向一的 GC sweeper 管理历史数据的保留/过期
  └── 方向三（用户生命周期）：需要方向一的定时扫描任务基础设施

方向四（DB 断路器）
  └── 方向五（差异化限流）：断路器状态可作为限流策略的输入（DB 降级时收紧限流）
```

### 不做的理由

| 方向 | 不做的情况 |
|------|-----------|
| 方向一 | 单实例开发/演示环境，不需要运维保障（但即使如此，也建议实现——代码量很小） |
| 方向二 | 内部工具类使用，用户信任风险可接受；或无审核要求 |
| 方向三 | 无合规要求（GDPR/CCPA 不适用），且用户数据量小（<100GB），手动清理可接受 |
| 方向四 | 使用托管的 PostgreSQL 服务（RDS/CloudSQL）且允许完全不可用期间 |
| 方向五 | 用户量 < 100，且已有充分的端点级速率感知（或使用 API Gateway 做限流） |

---

## 总结

| # | 方向 | 既有分析覆盖度 | 核心价值 | 建议优先级 |
|---|------|--------------|---------|-----------|
| 1 | 后台 Goroutine 生命周期管理 | ❌ 零覆盖 | 运维可见性、防止服务静默降级 | P0 |
| 2 | 内容编辑历史与版本管理 | 🔶 一段观察性文字 | 信任、合规、审核取证 | P1 |
| 3 | SSO 用户生命周期与孤儿数据 | 🔶 一句话附注 | 数据治理、合规、存储成本 | P1（有合规要求时 P0） |
| 4 | 数据库断路器与连接池韧性 | ❌ 零覆盖 | 故障隔离、防止级联崩溃 | P0 |
| 5 | 端点级差异化限流 | ❌ 零覆盖 | 公平性、操作保护、运维控制 | P1 |

这五个方向覆盖了 aero-id 当前在**运维可见性**（方向一）、**韧性隔离**（方向四）、**数据可靠性**（方向二、三）、**资源公平性**（方向五）四个层面的系统性缺口。合在一起，它们将 aero-id 从一个"功能正确"的服务提升为"可运维、可隔离、可信赖"的生产级系统。
