现在，我对整个代码库有了扎实的了解，以便制作一个真实、可操作的计划。以下是完整的 Tech Lead 分析：

---

# Tech Lead 分析：aero-id 架构改进路线图

## 0. 执行摘要

根据提供的分析审查，我验证了审查者声称的 **两项事实错误**：

1.  **死代码**：`internal/notification/dispatcher.go` 完全没有被生产代码导入。只有测试引用它。活动的分发器是 `internal/service/notification.go` 中的 `service.NotificationDispatcher`。需要制定策略：要么删除，要么在清理之前添加一个指向活动实现的 README 锚点。
2.  **app.go 行号**：`New()` 从第 72 行开始；`ratelimit.New()` 在第 238 行。分析论点未受影响，但必须根据当前代码库引用它们。

我还验证了三项补充的、可操作的技术发现：

-   **SSE 通道缓冲区为 16，带有静默丢弃** → 经过验证的 bug（`internal/hub/hub.go:67`：`make(chan SSEEvent, 16)`，`pub()` 中的 `default` 分支）。
-   **`ListSince` 将限制硬编码为 50** → 确保在离线期间最多丢失一条通知。
-   **死信表上缺少复合索引** → 死信过滤将扫描大量数据。

以下路线图将这些发现转化为一个由 13 个任务组成的并行工作流，总工期为 **6-8 周**，由一名高级工程师完成，第 2 周加入一名中级工程师。

---

## 1. 任务分解

每个任务都设计为 **2-5 小时** 的编码工作，加上同等时间的测试、审查和调试。总预期包括单元测试、集成测试和 lint-clean diff。

### 1.1 公共匿名只读访问（方向一，修正后的 P1 #1）

| ID | 任务标题 | 涉及文件 | 前置依赖 | 工时 |
|---|---|---|---|---|
| TASK-001 | 提取 `downloader.SignedDownloader` 接口 + 添加匿名凭证方法 | `internal/storage/storage.go`, `internal/storage/signing.go`, `internal/file/handler_http.go`, `internal/handler/http/file.go` | 无 | 3h |
| TASK-002 | 实现 `AnonTokenService`：生成/验证具有有限范围（文件 ID、操作、过期时间）的短期 HMAC 令牌 | `internal/service/anontoken.go`（新文件）, `internal/pkg/idgen/` | TASK-001 | 4h |
| TASK-003 | 添加 HTTP 路由 `GET /public/v1/files/{id}`，通过查询参数或 header 接受 `?token=<anon-token>` | `internal/handler/http/file.go` + `internal/handler/http/router.go` | TASK-002 | 3h |
| TASK-004 | 将文件共享服务 `GetSharedFile` 扩展到公共令牌流：返回一个 `PublicFile` DTO（无 owner 信息），不需要 Bearer 令牌 | `internal/service/file.go`, `internal/service/file_share.go`, `internal/domain/file.go` | TASK-003 | 5h |
| TASK-005 | 集成测试：未经身份验证的客户端下载共享文件、过期令牌、撤销的令牌 | `internal/handler/http/file_test.go` | TASK-004 | 3h |

### 1.2 通知推送可靠性（方向五，升级为 P1 #2）

| ID | 任务标题 | 涉及文件 | 前置依赖 | 工时 |
|---|---|---|---|---|
| TASK-006 | 修复 SSE 通道缓冲区静默丢弃：实现背压或指数退避的重试队列，以便在通道满时重试 | `internal/hub/hub.go`, `internal/service/notification.go` | 无 | 4h |
| TASK-007 | 修复 `ListSince` 硬编码限制：将限制提升为配置参数（默认 200），添加分页遍历以确保离线重放不会遗漏任何事件 | `internal/handler/http/events.go`, `internal/config/config.go` | 无 | 3h |
| TASK-008 | 将 `Publish` 更改为返回 `error`，以便分发器至少可以记录失败；添加 Prometheus 计数器 `sse_dropped_total` | `internal/hub/hub.go`, `internal/service/notification.go`, `internal/pkg/observability/` | TASK-006 | 4h |

### 1.3 配置热重载（方向四，降级为 P1 #3）

| ID | 任务标题 | 涉及文件 | 前置依赖 | 工时 |
|---|---|---|---|---|
| TASK-009 | 为 `zap.AtomicLevel` 添加 `SIGHUP` 处理程序 + `internal/pkg/logger.SetLevel()` 包装器；为速率限制器添加 `SetRPS/SetBurst` 方法 | `internal/pkg/logger/logger.go`, `internal/app/app.go`, `internal/config/config.go` | 无 | 3h |
| TASK-010 | 实现 SSO gRPC 端点重新连接：向 `sso.Client` 添加 `DialEndpoint(newEndpoint)` 方法，以便在无需重启的情况下切换 SSO 后端 | `internal/sso/client.go`, `internal/app/app.go` | 无 | 5h |

### 1.4 Outbox 死信管理 + 索引（方向二，升级为 P1 #4）

| ID | 任务标题 | 涉及文件 | 前置依赖 | 工时 |
|---|---|---|---|---|
| TASK-011 | 死信管理 CLI / HTTP 端点：`GET /admin/outbox/dead-letters`（列表），`POST /admin/outbox/dead-letters/{id}/retry`（重新入队），`DELETE /admin/outbox/dead-letters/{id}`（清除） | `internal/handler/http/admin.go`（新文件）, `internal/service/outbox_admin.go`（新文件）, `internal/repository/outbox.go` | 无 | 5h |
| TASK-012 | 添加死信过滤的部分索引 + 添加 `last_retry_at` 排序索引；为管理查询添加迁移 | `migrations/000024_outbox_admin_indexes.up.sql`（新文件）, `migrations/000024_outbox_admin_indexes.down.sql` | TASK-011 | 2h |

### 1.5 跨资源引用完整性（方向三，保持为 P2）

| ID | 任务标题 | 涉及文件 | 前置依赖 | 工时 |
|---|---|---|---|---|
| TASK-013 | 在评论创建和文件共享查询期间添加外键守卫：在执行 `ListByTarget` 之前对目标行执行显式的存在性 JOIN | `internal/service/comment.go`（修改 `assertTargetReadable` 以进行存在性检查）, `internal/repository/comment.go`（添加可选的引用检查查询） | 无 | 4h |

---

## 2. 任务依赖和执行顺序

```mermaid
graph TD
    %% Phase 1: Foundation (Week 1)
    subgraph Phase1 [Phase 1: Foundation — Week 1]
        T001[TASK-001: SignedDownloader 接口提取]
        T006[TASK-006: SSE 背压修复]
        T007[TASK-007: ListSince 限制提升]
        T009[TASK-009: SIGHUP + Logger SetLevel]
    end

    %% Phase 2: Core feature work (Weeks 2-3)
    subgraph Phase2 [Phase 2: Core Features — Weeks 2-3]
        T002[TASK-002: AnonTokenService 实现]
        T008[TASK-008: Publish 返回 error + metrics]
        T010[TASK-010: SSO 端点重新连接]
        T011[TASK-011: 死信管理端点]
        
        T002 --> T001
        T008 --> T006
    end

    %% Phase 3: Integration & polish (Weeks 3-5)
    subgraph Phase3 [Phase 3: Integration — Weeks 3-5]
        T003[TASK-003: Public /public/v1/files/{id} 路由]
        T012[TASK-012: 死信索引迁移]
        T013[TASK-013: 跨引用守卫]
        
        T003 --> T002
        T012 --> T011
    end

    %% Phase 4: Testing & release (Weeks 5-6)
    subgraph Phase4 [Phase 4: Testing & Release — Weeks 5-6]
        T004[TASK-004: PublicFile DTO + GetSharedFile 流]
        T005[TASK-005: 匿名下载集成测试]
        
        T004 --> T003
        T005 --> T004
    end

    %% Independent track (can run in parallel with Phase 2-3)
    T009 --> T010
    T013 -.->|no dependencies| T003
```

### 并行执行组

| 组 | 任务 | 需要 |
|---|---|---|
| **集群 A**（推送可靠性） | T006, T007, T008 | 按顺序执行，需要一名工程师 |
| **集群 B**（公共访问） | T001, T002, T003, T004, T005 | 按顺序执行，需要一名工程师 |
| **集群 C**（运维） | T009, T010, T011, T012 | T009→T010 有依赖；T011 和 T012 可以共享上下文 |
| **集群 D**（P2 完整性） | T013 | 可以完全并行执行；工程师在等待审查时可以捡起来 |

集群 A 和 B 可以 **完全并行** 由两名工程师在不同的分支上执行。集群 C 可以与集群 B 在同一工程师上并行执行，或者在不饱和时由集群 A 的工程师捡起来。

---

## 3. 技术风险

### 3.1 高风险项

| 风险 | 可能性 | 影响 | 缓解措施 |
|---|---|---|---|
| **SSO 重新连接期间的竞态条件**：`sso.Client` 持有对 `*grpc.ClientConn` 和 `ssopb.NewSSOClient(conn)` 的引用。如果在飞行中的 RPC 期间交换连接，客户端可能会收到 `Unavailable` 或 `Internal` 错误。 | 中 | 高 | 使用 `*grpc.ClientConn` 的 `connectivity.WaitForStateChange` API + 一个用于老连接的优雅关闭的 draining 周期。添加一个 `switchEndpoint` 方法，在切换期间锁定 RPC（使用 `sync.RWMutex`），然后在新连接稳定后释放。**单元测试必须模拟不稳定的 SSO 后端。** |
| **匿名令牌的密钥分发**：如果 `SIGNING_KEY` 被泄露，攻击者可以伪造匿名访问令牌。 | 低 | 严重 | 令牌生成需要 *不同的密钥*（`AERO_ID_ANON_SIGNING_KEY`），与下载器 HMAC 密钥分开。到期时间严格上限为 1 小时。在令牌中嵌入 `file_id` 和 `operation`，以便令牌无法在不同文件之间重复使用。添加可选的 `scope` 字段。 |
| **SSE 背压导致死锁**：如果分发器在将事件推送到通道时阻塞，并且分发器持有通知表上的锁，则整个中继可能会陷入停滞。 | 低 | 高 | 使用带超时的非阻塞发送 + 一个后台工作池（缓冲通道，大小为 100），用于排队待处理的推送。如果推送队列满了，丢弃并增加 `sse_dropped_total` 计数器。**切勿在持有任何互斥锁或数据库事务的情况下阻塞 Send。** |
| **死信管理端点没有 RBAC**：管理端点通过 HTTP 公开，但缺乏管理员角色检查。 | 中 | 中 | 在管理路由上添加 `AdminRequired` 中间件。重用 `migrations/000022_admin_roles.up.sql` 中已有的管理角色基础设施。 |

### 3.2 外部依赖

| 依赖 | 风险 |
|---|---|
| **SSO 系统**（gRPC，外部） | SSO 是令牌验证的唯一来源。如果 SSO 宕机，匿名访问仍然有效，但经过身份验证的端点会失败。没有外部浏览器重定向。 |
| **Protobuf 编译器** | 如果 SSO 原型发生变化，`make proto` 需要 `protoc` v1.34.1 和 `protoc-gen-go-grpc` v1.5.1。需要固定到与 `go.mod` 匹配的版本。 |

### 3.3 性能瓶颈

| 场景 | 当前状态 | 目标 |
|---|---|---|
| 50k 并发 SSO 连接 | gRPC 客户端重新连接代价高昂（每个连接一个 goroutine） | 连接池：`grpc.WithDefaultServiceConfig(retryPolicy)` + 连接复用的共享通道 |
| 死信表 > 1M 行 | 在 `created_at DESC` 上扫描，过滤 `dead_letter = TRUE` | TASK-012 添加部分索引 `WHERE dead_letter = TRUE` |
| SSE 广播至 10k 并发用户 | 为 10k 个 uid 订阅分配 10k 个通道（每个 16 个槽位 = 160k 条待处理消息） | 引入 `FanOut` 每个 uid 而不是每个订阅：同 uid 的多个设备共享一个分发通道 |

---

## 4. 资源评估

### 4.1 人员配置

| 时间 | 角色 | 专注领域 |
|---|---|---|
| 第 1 周 | **1 名高级工程师**（全职） | 集群 A（SSE 和 ListSince 修复）+ 集群 C 基础设施（SIGHUP 处理程序） |
| 第 2-3 周 | **高级工程师** + **中级工程师** | 高级工程师：集群 B（公共访问）。中级工程师：集群 C（死信管理）+ 集群 D（引用完整性） |
| 第 4-5 周 | **两名工程师** | 集成测试、审查、性能调优 |
| 第 6 周 | **1 名高级工程师** | 发布准备、文档、生产部署 |

### 4.2 里程碑

| 里程碑 | 日期（相对于开始） | 可交付物 |
|---|---|---|
| **M1：推送基础设施修复** | 第 1 周结束 | T006、T007、T008 合并到 `main`；SSE 静默丢弃和 50 条限制修复已部署 |
| **M2：公共匿名访问（Alpha）** | 第 3 周结束 | T001-T004 合并；`GET /public/v1/files/{id}` 路由生效 |
| **M3：运维就绪** | 第 4 周结束 | T009-T012 合并；死信管理 + SIGHUP + SSO 重新连接已部署 |
| **M4：完整发布** | 第 6 周结束 | 所有 13 个任务合并；集成测试通过；文档更新；`make test && make lint` 通过 |

### 4.3 阻塞点

| 阻塞点 | 影响 | 解决策略 |
|---|---|---|
| SSO 重新连接期间 gRPC 客户端优雅关闭 | TASK-010 | 使用 `conn.Shutdown()`（Go 1.21+ 实验性）或带有 `gracefulStop` 标志的 `conn.Close()`。在没有实际 SSO 后端的集成测试中模拟这一点。 |
| 匿名访问在没有用户身份上下文的操作队列中是否有效 | TASK-002 | 匿名令牌在路径中编码 `file_id` 和 `action`。服务层在执行操作后解析并验证令牌，无需 `mustActor(ctx)`。 |

---

## 5. 质量保证

### 5.1 单元测试覆盖

| 文件/函数 | 所需覆盖率 | 关键场景 |
|---|---|---|
| `internal/service/anontoken.go` | 90%+ | GenerateToken / VerifyToken / ExpiredToken / RevokedToken / TokenTampered |
| `internal/hub/hub.go`（`pub` 方法） | 90%+ | 正常投递 / 通道满丢弃 / 背压 / 关闭的 hub |
| `internal/sso/client.go`（`DialEndpoint`） | 85%+ | 切换期间连接的竞态条件 / 优雅关闭 / 切换后的 RPC |
| `internal/service/notification.go`（`Handle`） | 85%+ | 推送失败路径 / 与通道损坏的 pusher 一起调用 |

### 5.2 集成测试策略

所有新的集成测试遵循现有的 `//go:build integration` 模式。

| 测试套件 | 需要什么 | 关键覆盖 |
|---|---|---|
| `TestAnonFileDownload` | Postgres（testpg）+ MinIO（或本地存根）+ SSO 模拟 | 从下载到过期令牌的完整流程 |
| `TestSSEEndToEnd` | Postgres + SSO 模拟 | 事件 → outbox → 通知 → SSE 推送 → 客户端接收（在 postgres 之外的单独 goroutine 中） |
| `TestOutboxDeadLetterAdmin` | Postgres | 列表死信 / 重新入队 / 清除 / 索引扫描 EXPLAIN |
| `TestSSOEndpointSwitch` | 无外部依赖；使用 bufconn 模拟 SSO | 切换期间 + 切换后的 RPC 行为 |

### 5.3 代码审查要点

1.  **匿名令牌**：验证 `GenerateToken` 和 `VerifyToken` 使用不同的密钥。`file_id` 和 `exp` 必须是 HMAC 输入的一部分。永远不要返回包含原始签名密钥的错误消息。
2.  **SSE 背压**：`pub()` 不得在持有互斥锁时阻塞。检查 `select` 语句是否具有 `default` 分支或非阻塞超时。检查 `Publish()` 返回错误路径是否在分发器中被正确处理。
3.  **SSO 重新连接**：审查 `sync.RWMutex` 用法：读锁在 RPC 调用周围，写锁在 `DialEndpoint` 周围。检查在重新连接期间传入的旧 RPC 是否得到优雅处理，而不是被取消。
4.  **管理端点**：在每个管理路由上验证 `AdminRequired` 中间件。确保没有管理路径可以通过公共路由访问。
5.  **死信索引**：审查迁移 `000024` 的 `EXPLAIN ANALYZE` 输出以确保索引被使用。索引大小影响。

### 5.4 性能测试需求

| 场景 | 工具 | 目标 |
|---|---|---|
| SSE 推送吞吐量（10k 个并发订阅者） | 自定义 Go 基准测试或 `k6` | 在通道满之前 >500 推送/秒/订阅者 |
| 匿名文件下载延迟 | `wrk` 或 `hey` | P99 <200ms（通过本地存储） |
| 死信管理端点（1M 行） | `pgbench` + `EXPLAIN ANALYZE` | `SELECT ... WHERE dead_letter = TRUE` 在索引帮助下 <50ms |

---

## 6. 实施计划

### 时间线概述（按周）

```
Week    1      2      3      4      5      6
        │      │      │      │      │      │
Cluster A    T006   T007   T008  │      │      │
(SSE+List)   ██████ ██████ ██████│      │      │
                                 │      │      │
Cluster B    T001   T002   T003  T004   T005   │
(Public)     ██████ ██████ ██████ ██████ ██████│
                                               │
Cluster C          T009   T010  T011   T012    │
(Ops)               ██████ ██████ ██████ ██████│
                                                │
Cluster D                       T013            │
(Integrity)                      ██████          │
                                                 │
Integration     │      │      │  test  test  │
Testing         │      │      │ ██████ ██████ │
                                               │
Release                       docs  review  ship
Prep                            ██████ ██████ ██
```

### 详细阶段

#### 阶段 1：基础设施和紧急修复（第 1 周，5 天）

| 天次 | 任务 | 预期 | 审查 |
|---|---|---|---|
| 第 1-2 天 | T006：SSE 背压修复 | 实现非阻塞发送 + 后台重试队列。添加 `pushQueue` 通道（缓冲区 1000）。添加指标计数器。 | PR #1 |
| 第 3 天 | T007：ListSince 限制提升 | 将 `50` 提升为 `events.list_since_limit`（默认 200）。添加分页循环以完全耗尽。 | PR #2 |
| 第 4 天 | T008：Publish 返回 error | 更改 `hub.Publish` 签名以返回 `error`。更新分发器以记录失败。添加 `sse_dropped_total`。 | PR #3 |
| 第 5 天 | T001：SignedDownloader 提取 | 将 `SignedDownloader` 接口提取到 `internal/storage/storage.go`。添加 `AnonymousAccessor` 子接口。更新 `NewFileHandler` 以接受两个接口。 | PR #4 |

#### 阶段 2：核心功能（第 2-3 周，10 天）

| 天次 | 任务 | 预期 | 审查 |
|---|---|---|---|
| 第 6-7 天 | T002：AnonTokenService | `internal/service/anontoken.go`。`GenerateToken(fileID, operation, ttl)` 和 `VerifyToken(token) → (fileID, operation, error)`。密钥来自 `config.SigningKey`（或者如果设置了则来自新的 `AnonSigningKey`）。 | PR #5 |
| 第 8-9 天 | T009：SIGHUP + Logger | 在 `main.go` 中注册 `SIGHUP` 处理程序。向 `Logger` 包装器添加 `SetLevel(string)`。添加 `RateLimiter.SetRPS()` + `SetBurst()`。添加 `/admin/config` 端点（只读）。 | PR #6 |
| 第 10-11 天 | T010：SSO 重新连接 | `sso.Client.DialEndpoint(newEndpoint string) error`。使用 `sync.RWMutex` 保护 `conn` 和 `rpc`。在重新连接期间添加 draining 周期。 | PR #7 |
| 第 12-13 天 | T011：死信管理端点 | 3 个 HTTP 处理程序：`GET /admin/outbox/dead-letters`、`POST /admin/outbox/dead-letters/{id}/retry`、`DELETE /admin/outbox/dead-letters/{id}`。添加 `AdminRequired` 中间件。 | PR #8 |
| 第 14-15 天 | T003：`GET /public/v1/files/{id}` 路由 | 在 `internal/handler/http/file.go` 中添加处理程序。解析 `?token=`。调用 `AnonTokenService.VerifyToken`。将请求路由到 `FileService.GetPublicFile(fileID)`。 | PR #9 |

#### 阶段 3：完善和完整性（第 4-5 周，10 天）

| 天次 | 任务 | 预期 | 审查 |
|---|---|---|---|
| 第 16-17 天 | T004：PublicFile DTO + GetSharedFile 集成 | 添加 `domain.PublicFile`（无 owner_uid、无路径、有 content_type、大小、文件名）。`service.FileService.GetPublicFile` 检查文件是否为公共/共享的。 | PR #10 |
| 第 18-19 天 | T012：死信索引 + 迁移 | 创建 `migrations/000024_outbox_admin_indexes.up.sql`。`CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_outbox_dead_letter ON outbox_events(created_at DESC) WHERE dead_letter = TRUE`。确保 `000024_down.sql` 删除它。 | PR #11 |
| 第 20-21 天 | T013：跨引用守卫 | 修改 `assertTargetReadable` 以在无法找到目标时返回明确的 `ErrNotFound`。在 `ListByTarget` 查询中添加可选的 JOIN 存在性检查。 | PR #12 |
| 第 22-23 天 | T005：集成测试 | 完整的 http → service → repository 链，用于匿名文件下载。测试：有效令牌、过期令牌、错误的文件 ID、撤销的令牌。 | PR #13 |

#### 阶段 4：发布准备（第 6 周，5 天）

| 天次 | 任务 | 预期 |
|---|---|---|
| 第 24 天 | 清理死代码 | 从 `internal/notification/` 中删除所有未引用的导出符号。或者添加一个指向活动 `service.NotificationDispatcher` 的 `// Deprecated: see internal/service/notification.go` 注释以保持向后兼容性。**不删除测试**（它们可能仍然有用）。 |
| 第 25 天 | 配置文档 + 示例 | 更新 `configs/config.yaml` 以包含新的节（`events.list_since_limit`、`anon_token.ttl`、`sso.endpoint_switch`）。更新 `README.md`。 |
| 第 26-27 天 | 端到端测试 + 性能基准测试 | 在暂存环境中运行集成测试。运行 `wrk` + `pgbench` 基准测试。记录 P99 延迟。 |
| 第 28 天 | 最终 lint + 发布 | `make check-file-sizes && make lint-strict && make test && make test-integration`。标记发布。 |

---

## 7. 附录：已识别问题的验证结果

| 审查声明 | 验证状态 | 代码证据 |
|---|---|---|
| `internal/notification/dispatcher.go` 是死代码 | ✅ 已确认 | `grep -rn "notification" internal/ 2>/dev/null \| grep -v _test` → 没有生产导入。`app.go:174` 使用 `service.NewNotificationDispatcher(notifRepo, ...)`。 |
| `app.go:50-58` 不在 `New()` 内部 | ✅ 已确认 | 第 50-58 行在 `App` struct 区域内。`New()` 从第 72 行开始。`ratelimit.New()` 在第 238 行。 |
| `ListSince` 在 events.go:103 中硬编码为 50 | ✅ 已确认 | `internal/handler/http/events.go:103`：`h.fetcher.ListSince(c.Request.Context(), uid, since, 50)` |
| Hub 通道缓冲区为 16 | ✅ 已确认 | `internal/hub/hub.go:67`：`ch := make(chan SSEEvent, 16)`。`pub()` 中的 `default` 分支确认了静默丢弃。 |
| 没有用于死信查询的复合索引 | ✅ 已确认 | `migrations/000018` 创建了 `idx_outbox_unpublished` 使用 `WHERE published_at IS NULL AND NOT dead_letter`，但是 `WHERE dead_letter = TRUE` 的查询没有索引。 |
| `assertTargetReadable` 保护文件/消息目标 | ✅ 已确认 | `internal/service/comment.go:48`：检查 `Get(ctx, target)` 并返回 `ErrNotFound`。但 `ListByTarget` 没有单独的检查。 |
