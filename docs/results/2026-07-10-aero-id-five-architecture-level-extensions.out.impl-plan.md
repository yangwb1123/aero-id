现在我已彻底了解相关代码库，可以写出全面的技术负责人分析了。

---

# 技术负责人分析：五项架构扩展

## 1. 任务分解

### 方向 5 — HTTP 逐路由请求体大小限制（P0）

| 任务 ID | 标题 | 文件 | 依赖 | 工时 |
|---|---|---|---|---|
| TASK-501 | 创建 `BodySizeLimit` 中间件 | `internal/middleware/bodylimit.go`（新建） | 无 | 1h |
| TASK-502 | 将中间件插入路由器 + 配置路由级别映射 | `internal/handler/http/router.go` | TASK-501 | 1.5h |
| TASK-503 | 向 `config.yaml` 添加 `request_body_limit` + 每个路由的覆盖 | `configs/config.yaml` | 无 | 0.5h |
| TASK-504 | 向 `config.Config` 结构体添加设置 | `internal/config/`（相关文件） | TASK-503 | 0.5h |
| TASK-505 | gRPC `MaxCallRecvMsgSize` 保护：为 gRPC 服务层添加显式配置 | `internal/handler/grpc/server.go` | 无 | 0.5h |
| TASK-506 | 为路由映射 + 大请求拒绝编写单元测试 | `internal/middleware/bodylimit_test.go`（新建） | TASK-502 | 1h |
| **总计** | | | | **5h** |

### 方向 4 — 死信事件生命周期管理（P1）

| 任务 ID | 标题 | 文件 | 依赖 | 工时 |
|---|---|---|---|---|
| TASK-401 | 在 `Repository` 接口和 `GormRepository` 中添加 `FetchDeadLetters` / `RetryDeadLetter` / `PurgeDeadLetters`（按时间范围） | `internal/outbox/repository.go`、`internal/outbox/outbox.go` | 无 | 3h |
| TASK-402 | 向死信列表/重试/清除添加 `DeadLetterService` | `internal/service/deadletter.go`（新建） | TASK-401 | 2h |
| TASK-403 | 实现 HTTP 管理端点：`GET/POST/DELETE /v1/admin/outbox/dead-letters` | `internal/handler/http/admin.go` | TASK-402 | 3h |
| TASK-404 | 实现 gRPC 管理端点（admin proto 中的 `DeadLetterService` RPC） | `internal/handler/grpc/admin.go`、`api/proto/...` | TASK-402 | 3h |
| TASK-405 | 添加死信指标（`aeroid_outbox_dead_letter_total`、`_retried_total`） | `internal/pkg/metrics/metrics.go` | TASK-401 | 1.5h |
| TASK-406 | 为仓库方法编写单元测试 | `internal/outbox/repository_test.go` | TASK-401 | 2h |
| TASK-407 | 为管理 API 编写集成测试 | `internal/handler/http/admin_test.go`（扩充） | TASK-403 | 2h |
| **总计** | | | | **16.5h** |

### 方向 1 — 异步链路追踪连续性（P1）

| 任务 ID | 标题 | 文件 | 依赖 | 工时 |
|---|---|---|---|---|
| TASK-101 | 向 `Event` 结构体添加 `Traceparent` 字段，使 `NewEvent` 接受 `ctx context.Context` 以提取追踪上下文 | `internal/outbox/outbox.go` | 无 | 1.5h |
| TASK-102 | 更新 `eventRow` GORM 映射以包含 `trace_id` + `span_id` 列 | `internal/outbox/repository.go` | TASK-101 | 0.5h |
| TASK-103 | 创建迁移 `000028` 以添加 `trace_id` + `span_id` 列 + 索引 | `migrations/000028_*.up.sql`、`000028_*.down.sql` | 无 | 0.5h |
| TASK-104 | 更新所有 `newEvent()` 调用者（`message.go`、`comment.go`、`file.go` 等）以传递 `ctx` | `internal/service/message.go`、`internal/service/comment.go`、`internal/service/file.go` | TASK-101 | 2h |
| TASK-105 | 更新 relay 的 `Start()` 以为每个 drain 创建根 span，并从事件的追踪上下文中恢复父级 | `internal/outbox/relay.go` | TASK-101、TASK-102 | 2h |
| TASK-106 | 为异步路径添加集成测试（drain 期间的追踪连续性） | `internal/outbox/relay_test.go` | TASK-105 | 2h |
| **总计** | | | | **8.5h** |

### 方向 3 — 跨实例 SSE 推送（P1 — 两阶段实施）

#### 阶段 1：客户端回退拉取（低成本，无 Redis 依赖）

| 任务 ID | 标题 | 文件 | 依赖 | 工时 |
|---|---|---|---|---|
| TASK-301 | 扩展 SSE 重放以定期拉取：客户端每 30 秒轮询  | （现有 `ListSince` + `last_event_id` 重放已覆盖——记录下游指南） | 无 | 0.5h* |
| TASK-302 | 添加文档：告知运营商默认 SSE 限制（每实例），以及如果 >1 副本部署则建议使用 Redis | `docs/operations/sse-multi-instance.md`（新建） | 无 | 1h |

#### 阶段 2：Redis Pub/Sub 跨实例广播

| 任务 ID | 标题 | 文件 | 依赖 | 工时 |
|---|---|---|---|---|
| TASK-303 | 定义 `Broadcaster` 接口 + `RedisBroadcaster` 实现 | `internal/hub/broadcaster.go`（新建）、`internal/hub/redis.go`（新建） | 无 (Redis 客户端新依赖) | 4h |
| TASK-304 | 修改 `NotificationDispatcher.Publish` 以通过 `Broadcaster` 广播，而不仅仅是本地 hub | `internal/service/notification.go` | TASK-303 | 1.5h |
| TASK-305 | 向 `hub.Hub` 添加订阅逻辑以从广播通道消费（在 goroutine 中） | `internal/hub/hub.go` | TASK-303 | 2h |
| TASK-306 | 更新 `app/new.go` 组合根：条件是 provider 有 Redis 时，用 `RedisBroadcaster` 包装 hub | `internal/app/new.go`、`configs/config.yaml` | TASK-304、TASK-305 | 1.5h |
| TASK-307 | 为 Redis 广播器编写单元 + 集成测试 | `internal/hub/redis_test.go`（新建） | TASK-303 | 2.5h |
| **总计** | | | | **13h** |

*（TASK-301 主要是记录现有行为——现有 `last_event_id` 重放 + 定期轮询已经作为 SSE 架构的一部分被支持）*

### 方向 2 — 存储 GC TOCTOU 竞态（P2）

| 任务 ID | 标题 | 文件 | 依赖 | 工时 |
|---|---|---|---|---|
| TASK-201 | 实施 `verifyStorageConsistency` 定期修复任务以检测并修复损坏引用 | `internal/storage/gc/consistency.go`（新建） | 无 | 4h |
| TASK-202 | 为修复任务添加可选行锁守卫，使用 `SELECT ... FOR UPDATE` 防止竞态（在配置中默认关闭——仅在 GC 启用且需要时使用） | `internal/storage/gc/sweeper.go` | 无 | 3h |
| TASK-203 | 将一致性修复任务接入 GC 的 `Run()` 循环 | `internal/storage/gc/sweeper.go` | TASK-201 | 0.5h |
| TASK-204 | 编写集成测试：模拟具有 TOCTOU 条件的并发文件创建 + GC sweep | `internal/storage/gc/sweeper_test.go` | TASK-202 | 3h |
| TASK-205 | 在启用 GC 时添加操作手册 + 文档 | `docs/operations/storage-gc.md`（新建） | TASK-201 | 1.5h |
| **总计** | | | | **12h** |

---

## 2. 执行顺序与依赖图

```mermaid
graph TD
    %% 方向 5 — P0，无依赖
    T501[方向 5 TASK-501: BodySizeLimit 中间件] --> T502[方向 5 TASK-502: 集成到路由器]
    T503[方向 5 TASK-503: config.yaml] --> T504[方向 5 TASK-504: Config 结构体]
    T502 --> T506[方向 5 TASK-506: 单元测试]
    T504 --> T502
    T505[方向 5 TASK-505: gRPC 大小限制] --- T502

    %% 方向 4 — P1，与方向 1 共享路由/仓库模型
    T401[方向 4 TASK-401: 仓库方法<br/>(FetchDeadLetters / Retry / Purge)] --> T402[方向 4 TASK-402: DeadLetterService]
    T402 --> T403[方向 4 TASK-403: HTTP 管理 API]
    T402 --> T404[方向 4 TASK-404: gRPC 管理 API]
    T402 --> T405[方向 4 TASK-405: 死信指标]
    T401 --> T406[方向 4 TASK-406: 仓库单元测试]
    T403 --> T407[方向 4 TASK-407: API 集成测试]

    %% 方向 1 — P1，与方向 4 共享 outbox 包
    T101[方向 1 TASK-101: Event + Traceparent<br/>NewEvent(ctx)] --> T102[方向 1 TASK-102: eventRow 列]
    T101 --> T104[方向 1 TASK-104: 更新调用者<br/>(message/comment/file service)]
    T102 --> T103[方向 1 TASK-103: 迁移 000028]
    T102 --> T105[方向 1 TASK-105: Relay 根 span<br/>+ 追踪注入]
    T105 --> T106[方向 1 TASK-106: 集成测试]

    %% 方向 1 + 方向 4 共享 outbox 模型（没有严格依赖关系——可以并行处理）
    T101 -.->|共享包| T401

    %% 方向 3 — P1
    T301[方向 3 TASK-301: 回退拉取指南] --- T302[方向 3 TASK-302: 操作文档]
    T303[方向 3 TASK-303: Broadcaster 接口<br/>+ RedisBroadcaster] --> T304[方向 3 TASK-304: 集成到 Dispatcher]
    T303 --> T305[方向 3 TASK-305: Hub 订阅广播]
    T303 --> T307[方向 3 TASK-307: Redis 测试]
    T304 --> T306[方向 3 TASK-306: 组合根布线]

    %% 方向 2 — P2（仅当 GC 启用时）
    T201[方向 2 TASK-201: 一致性修复 Job] --> T203[方向 2 TASK-203: 集成到 Run() 循环]
    T202[方向 2 TASK-202: 可选行锁] --> T204[方向 2 TASK-204: 集成测试]
    T203 --> T205[方向 2 TASK-205: 操作文档]

    %% 并行组
    subgraph Group_A [P0 — 立即：请求体限制]
        T501; T502; T503; T504; T505; T506
    end

    subgraph Group_B [P1 — 并行流 1：死信管理]
        T401; T402; T403; T404; T405; T406; T407
    end

    subgraph Group_C [P1 — 并行流 2：追踪连续性]
        T101; T102; T103; T104; T105; T106
    end

    subgraph Group_D [P1 — 并行流 3：SSE 跨实例]
        T301; T302; T303; T304; T305; T306; T307
    end

    subgraph Group_E [P2 — 后期：GC 竞态修复]
        T201; T202; T203; T204; T205
    end
```

**并行执行组：**

| 组 | 优先 | 可以并行 |
|---|---|---|
| A（P0，方向 5） | **立即** | 独立。可以与所有其他组并行 |
| B（P1，方向 4） | 第 1 周 | 与 C 并行（它们共享 outbox 包，但修改非重叠：B 添加方法，C 修改结构体 + 调用者） |
| C（P1，方向 1） | 第 1 周 | 与 B 并行 |
| D（P1，方向 3） | 第 2 周 | 独立——启动前需要 Redis |
| E（P2，方向 2） | 第 4+ 周 | 仅在 GC 启用时必要；可以延迟 |

---

## 3. 技术风险

### 风险 1：`NewEvent` 签名更改的破坏性影响（方向 1，TASK-101）

**问题**：`outbox.NewEvent()` 目前是 `func(aggregateType, aggregateID, eventType string, payload any) *Event`。添加 `ctx context.Context` 参数将破坏所有调用者。有 **6 个调用站点**分布在服务层（`message.go`、`comment.go`、可能还有 `file.go`）。

**缓解措施**：
- 用 `WithContext(ctx, ...)` 流畅包装器或重载的 `NewEventFromContext(ctx, ...)` 包装器来分阶段处理，该包装器保留旧的 `NewEvent` 用于测试。
- 更好的方法是：添加 `func NewEventWithContext(ctx context.Context, ...) (*Event, error)` 并让旧的 `NewEvent` 委托给它，使用 `trace.SpanFromContext(ctx)` 的可选提取。
- 在所有调用者在一次提交中更新之前，运行 `make test` 以确保测试通过。

**可能性**：低 — 清晰的重构路径。**工时缓冲**：+1h 用于跨调用者的协调。

### 风险 2：Redis 作为 SSE 广播的新依赖（方向 3，TASK-303）

**问题**：引入 Redis 客户端为项目增加了新的外部依赖、连接管理和故障模式。

**缓解措施**：
- 使 Redis 广播成为可选的（通过配置标记 `sse.broadcast.type: redis|local`）。
- 使用具备断路器/超时功能的专用 Redis 客户端（如 `go-redis` + 自定义包装器）。
- 如果 Redis 宕机则优雅降级回到仅本地 hub 推送。
- 为 `Broadcaster` 接口添加测试替身，以便测试时不需 Redis。

**可能性**：中等。额外的运维负担。**工时缓冲**：+3h 用于断路器 + 错误处理。

### 风险 3：死信重试的幂等性问题（方向 4，TASK-403）

**问题**：重试死信事件可能触发具有破坏性副作用的 handler（例如，发送重复的 webhook）。

**缓解措施**：
- 文档要求：在重试死信事件之前，操作员应审查 `event_type`。
- 为手动重试添加 max_retry 计数器（例如，手动重试不超过 3 次，之后锁定）。
- Outbox handler 应该已经是幂等的（通知通过 `created_at` 去重）。

**可能性**：低到中。**缓解成本**：+1h 用于手动重试限制逻辑。

### 风险 4：GC TOCTOU 修复的集成测试（方向 2，TASK-204）

**问题**：在测试中可靠地重现并发的 TOCTOU 竞态条件很困难——这是针对计时器的精度竞赛。

**缓解措施**：
- 使用 `sync.WaitGroup` + 有意的屏障注入来强制精确的计时顺序：在 `countLive()` 之后、`store.Delete()` 之前设置一个可注入的阻塞点。
- 使用受控的假存储后端（`storage.FileStorage` 的模拟实现）来验证竞态窗口。
- 验证一致性 Job 在竞态窗口关闭后检测到损坏引用。

**可能性**：高——竞态测试很脆弱。**工时缓冲**：+3h。

### 风险 5：BodySizeLimit 与流式上传交互（方向 5，TASK-501）

**问题**：预签名上传流（上传直接到 S3，不经过 aero-id）不应受到 HTTP 层请求体大小限制的影响。文件上传已经已有 service 层限制。

**缓解措施**：
- 在路由器中专门为 `/v1/files/upload*` 路由设置例外——不要应用限制，或应用更大的限制。
- 验证 `http.MaxBytesReader` 是否返回正确的 413 状态码（由 Gin 的 `ShouldBindJSON` 测试）。

**可能性**：低。简单的路由属性。**工时缓冲**：0.5h。

---

## 4. 资源评估

### 团队组成

| 角色 | 需要人数 | 职责 |
|---|---|---|
| **高级后端工程师** | 1 （全职，第 0-3 周） | 方向 1 + 方向 4（outbox 核心变更）、方向 5（中间件 + 配置） |
| **后端工程师** | 1 （全职，第 1-4 周） | 方向 3（SSE + Redis 广播）、方向 2（GC 竞态） |
| **SRE / 平台工程师** | 0.5 （兼职，第 2 周） | Redis 基础设施、部署指南、告警规则（Prometheus） |
| **QA 工程师** | 0.5 （兼职，第 1-3 周） | 集成测试设计、竞态测试，跨方向验证 |

**总计**：2-3 名 FTE，为期 4 周。

### 里程碑

| 里程碑 | 时间 | 可交付物 |
|---|---|---|
| **M0**（快速胜利） | 第 1 天 | 方向 5 完成（请求体限制）——所有环境部署 |
| **M1**（核心稳定性） | 第 1 周结束 | 方向 4 完成（死信管理 API + 指标） |
| | | 方向 1 完成（异步追踪连续性） |
| **M2**（实时推送） | 第 2 周结束 | 方向 3 完成——SSE 广播（如果 Redis 可用，则阶段 2；否则仅阶段 1） |
| **M3**（存储完整性） | 第 4 周结束 | 方向 2 完成（GC 修复 Job + 行锁） |
| | | 所有任务：`make test && make lint` 通过 |

### 阻塞点与策略

| 阻塞点 | 影响 | 解决策略 |
|---|---|---|
| **Redis 基础设施不可用** | 方向 3 阶段 2 被阻塞 | 前移阶段 1（客户端轮询）。记录 `sse.broadcast.type: local`。稍后添加 Redis |
| **Prostobuf 版本冲突** | 如果 `protoc-gen-go` 版本不匹配，方向 4 的 gRPC 管理端点被阻塞 | 在 `Makefile` 中固定版本。使用 `make proto` 验证。读 `AGENTS.md` 关于 protoc 版本的内容 |
| **GC 竞态修复需要 `storage_refs` 表** | 方向 2 被阻塞，如果存储引用计数未在生产中启用 | GC 默认已禁用。修复 Job 是纯逻辑的。行锁守卫是可选迁移。验证 `storage_refs` 已部署 |

---

## 5. 质量保证

### 单元测试覆盖要求

| 包 | 最低覆盖率目标 | 关键测试场景 |
|---|---|---|
| `internal/outbox` | 85% | `FetchDeadLetters` 分页 + 过滤、`RetryDeadLetter` 重置、`MarkFailed` 死信阈值、`Append` 追踪上下文持久化、`FetchUnpublished` 死信排除 |
| `internal/middleware` | 90% | `BodySizeLimit` 在限制内/超出限制/限制边界、流请求、JSON 与原始 body、413 状态码 |
| `internal/service` | 85% | `DeadLetterService` 授权（只有 admin）、重试限制（最多 3 次）、空结果 |
| `internal/hub` | 80% | `RedisBroadcaster` 发布/订阅/断线重连、优雅降级（Redis 宕机） |
| `internal/storage/gc` | 80% | TOCTOU 顺序（强制屏障）、修复 Job 检测损坏、行锁阻止并发删除 |
| `internal/pkg/metrics` | 90% | 死信计数器注册/增量/报告、空 event_type 降级为 "unknown" |

### 集成测试策略

| 场景 | 需要基础设施 | 测试文件 |
|---|---|---|
| 死信管理 API：列出/重试/清除 | Postgres（testcontainers） | `internal/handler/http/admin_test.go` |
| 追踪连续性：事务性 outbox 写入 + relay 处理 | Postgres（testcontainers） | `internal/outbox/relay_test.go` |
| GC 竞态：并发文件创建 + GC sweep | Postgres + MinIO（testcontainers） | `internal/storage/gc/sweeper_test.go` |
| SSE Redis 广播：多实例发布 + 订阅 | Redis（testcontainers 或嵌入式 miniredis） | `internal/hub/redis_test.go` |

所有集成测试使用 `//go:build integration` 构建标签，遵循现有的 `internal/repository/testpg` 模式。

### 代码审查要点

| 关注领域 | 需要检查的内容 |
|---|---|
| **方向 1（追踪）** | `NewEvent` 是否从 ctx 正确提取 W3C traceparent？Relay 是否为每个事件创建根 span？数据库迁移是否包含回滚？ |
| **方向 4（死信）** | `RetryDeadLetter` 是否重置 `retry_count` 和 `dead_letter`？手动重试是否有限制？API 是否通过 `middleware.RequirePermission` 要求 admin 权限？ |
| **方向 5（请求体限制）** | 文件上传路径是否有例外或更大的限制？使用 `http.MaxBytesReader` 后，Gin 是否优雅地返回 413（而非 500）？ |
| **方向 3（SSE）** | `RedisBroadcaster.Publish` 是否返回错误？Hub 订阅 goroutine 是否包含 `recover()` 以防止 panic 泄漏到其他连接？ |
| **方向 2（GC）** | 一致性修复 Job 是否记录其发现并在检测到损坏时发出警告？`SELECT ... FOR UPDATE` 是否在短事务内使用以防止锁竞争？ |

### 性能测试需求

| 场景 | 负载 | 监控指标 |
|---|---|---|
| 大 JSON 请求体（1MB+）拒绝 | 并行 100 个请求，每个 10MB | P99 延迟 < 5ms（快速失败），内存增量 < 50MB |
| 死信列表（10K 死信行） | 10 个并发客户端，分页请求 | P99 延迟 < 200ms，无 OOM |
| GC 一致性扫描（100K 文件） | 单次扫描，串行 | 持续时间 < 5 分钟（100K/500 批次），CPU < 50% |
| SSE Redis 广播 | 1 个发布者，100 个订阅者 | P99 端到端延迟（发布→消费）< 100ms |

---

## 6. 实施计划

### 阶段 1：基础设施搭建（第 0 天 → 第 1 天）

**方向 5（P0）** — 零依赖，可以并应该立即推送。

**日 1 时间线：**

| 时间 | 工作 |
|---|---|
| 上午 9:00—10:00 | TASK-503 + TASK-504：`config.yaml` + Config 结构体（0.5h 结对完成） |
| 上午 10:00—11:30 | TASK-501：`bodylimit.go` 中间件 |
| 上午 11:30—12:30 | TASK-502：集成到 `router.go` |
| 中午 12:30—13:30 | 午餐 |
| 下午 13:30—14:30 | TASK-505：gRPC 接收消息大小配置 |
| 下午 14:30—15:30 | TASK-506：单元测试 |
| 下午 15:30—16:30 | 代码审查 + 合并 + 发布到暂存环境 |
| 下午 16:30—17:00 | 性能验证（大请求 413 拒绝） |

**产出**：生产就绪的请求体大小限制中间件，具有路由级控制。

---

### 阶段 2：核心功能实现（第 1 天 → 第 7 天）

**并行流 1：死信管理（方向 4）** → 1 个后端工程师

| 日 | 任务 |
|---|---|
| 1 | TASK-401：仓库方法（`FetchDeadLetters`、`RetryDeadLetter`、按时间范围 `PurgeDeadLetters`） |
| 2 | TASK-402：DeadLetterService 层 + 测试 |
| 3 | TASK-403：HTTP 管理端点（需要 admin 权限） |
| 4 | TASK-404：gRPC 管理端点 + proto 定义，运行 `make proto` |
| 5 | TASK-405：死信指标 + 告警规则 |
| 6 | TASK-406 + TASK-407：仓库 + API 集成测试 |
| 7 | 代码审查 + 修复 + 合并 |

**并行流 2：追踪连续性（方向 1）** → 1 个高级后端工程师

| 日 | 任务 |
|---|---|
| 1 | TASK-101：向 `Event` 添加 `Traceparent`，带 `ctx` 参数的 `NewEvent` 重载 |
| 2 | TASK-102 + TASK-103：eventRow 列 + 迁移 000028 |
| 3 | TASK-104：更新所有调用者（`message.go`、`comment.go`、`file.go` 等） |
| 4 | TASK-105：Relay 根 span 创建 + 事件追踪上下文注入 |
| 5 | TASK-106：集成测试（drain 期间的追踪上下文传播） |
| 6 | 性能测试 + 边界情况（没有追踪上下文的事件） |
| 7 | 代码审查 + 合并 |

---

### 阶段 3：集成测试与优化（第 8 天 → 第 14 天）

**并行流 3：SSE 跨实例（方向 3）**

| 日 | 任务 |
|---|---|
| 8 | TASK-301 + TASK-302：回退拉取文档，操作手册 |
| 9 | TASK-303：`Broadcaster` 接口 + `RedisBroadcaster` 实现 |
| 10 | TASK-304：修改 `NotificationDispatcher.Publish` 以使用广播器 |
| 11 | TASK-305：Hub 订阅广播通道（goroutine 消费） |
| 12 | TASK-306：组合根布线（有条件：配置中指定 Redis 地址） |
| 13 | TASK-307：Redis 集成测试（使用 `miniredis` 或 testcontainers） |
| 14 | 代码审查 + 文档 + 多副本部署指南 |

---

### 阶段 4：发布准备 + 方向 2（第 15 天 → 第 28 天）

**流 4：GC 竞态修复（方向 2）** — P2，仅在 GC 启用时必要

| 日 | 任务 |
|---|---|
| 15-16 | TASK-201：一致性修复 Job 实现 |
| 17 | TASK-203：集成到 `Sweeper.Run()` |
| 18-19 | TASK-202：可选 `SELECT ... FOR UPDATE` 行锁守卫 |
| 20-21 | TASK-204：带屏障变量的 TOCTOU 并发测试 |
| 22 | TASK-205：操作手册（启用 GC、修复 Job、指标解释） |

**发布准备（第 23-28 天）：**

| 活动 | 描述 |
|---|---|
| **端到端测试** | 多副本部署（3 个实例）+ Redis + Postgres 全工作流测试：消息、通知、SSE 推送 |
| **负载测试** | 请求体限制效果验证，死信在压力下的管理延迟 |
| **监控设置** | 添加死信告警规则 + 仪表盘（死信率、重试率、GC 修复率） |
| **文档** | 更新 `README.md` 的新配置选项，操作手册 |
| **安全审查** | 验证管理端点通过 `middleware.RequirePermission` 要求正确的 admin 角色 |
| **上线前检查清单** | 确认前：`make test && make lint` |

---

## 总结实施指标

| 指标 | 值 |
|---|---|
| **总任务** | 25 |
| **总预估工时** | ~55 小时开发 + ~15 小时测试/审查 |
| **日历持续时间（并行团队）** | 28 天（4 周） |
| **关键路径** | 方向 4（16.5h）→ 方向 3（13h）→ 方向 2（12h），但方向 5 + 方向 1 可并行 |
| **快速胜利（方向 5）** | 第 1 天部署 |
| **最大风险** | 方向 2（TOCTOU 测试） |
| **每方向测试覆盖率增长** | 方向 5: ~2%，方向 4: ~8%，方向 1: ~5%，方向 3: ~6%，方向 2: ~5% |

**开始顺序建议**：
1. 方向 5（P0）——同时进行所有开发；在一天内完成。
2. 方向 4（P1）+ 方向 1（P1）——第 1 天开始，并行进行（如果可能由两名工程师，或由同一名工程师按顺序进行）。
3. 方向 3（P1）——第 2 周开始。
4. 方向 2（P2）——最后，且仅当 GC 需要启用时。
