Now I have a thorough understanding of the codebase. Here is my architectural analysis.

---

# 架构评审报告：aero-id

## 1. 架构评估

### 1.1 当前架构的优势

**分层清晰，单方向依赖。** `handler/{http,grpc} → service → repository` 的层级结构严格遵循依赖倒置原则。每个层次只依赖其下层接口，service 层是唯一的业务逻辑中枢——HTTP 和 gRPC 共享同一套 service 实例，保证了双协议的行为一致性。这在微服务架构中是一个正确的设计决策。

**组合根（composition root）集中管控。** `internal/app/app.go` 统一完成所有组件的装配，包括中间件链的顺序编排（recovery → tracing → metrics → auth → rate-limit → idempotency）。这种"显式编织"模式比依赖注入框架更易审计，也符合 Go 社区的惯用风格。

**错误处理模式统一。** `pkg/errors.AppError` 同时携带 HTTP 状态码和 gRPC codes，service 层产出结构化错误，handler 层直接渲染，不存在泄露原始 DB 错误的路径。这是微服务多协议场景下的最佳实践。

**配置系统完善。** 基于 Viper 的配置支持 YAML + 环境变量覆盖 + 启动时校验，每个字段都有明确的零值语义和 `validate()` 守卫。SSO 连接池、断路器、重试策略等生产级参数全部可调。

**跨切面关注点正交。** 限流、鉴权、幂等、追踪、指标各自独立为 middleware/interceptor，可通过配置组合启用。限流器 OPT-IN 的设计使得本地开发环境零依赖。

### 1.2 关键架构局限性

**缺失的生产级安全防护（P1 级别）。** CORS 默认允许所有来源（`allowAll := len(origins) == 0`），且 `app.go` 中 `RouterConfig{}` 的 `CORSOrigins` 字段保持零值。在生产环境中，这意味着任何第三方网站都可以跨域调用 API。虽然 aero-id 始终要求 Bearer token（公开端点除外），但 CORS 通配符仍然扩大了攻击面——特别是结合 CSRF 潜力的场景。按照防御纵深原则，这应配置化收紧。

**gRPC 传输层的功能不对称。** HTTP 传输层拥有完整的中间件链（幂等性、限流、超时），而 gRPC 传输层缺少幂等性和请求超时控制。对于同一个 service 层，HTTP 客户端获得幂等性保障而 gRPC 客户端没有，这破坏了双协议"行为一致"的设计初衷。考虑到 gRPC 的主要使用场景是内部服务通信——此类通信通常对幂等性要求更高（重试是常态）——这个缺口值得优先补齐。

**文件分享缺少事件驱动。** `ShareFile()` / `UnshareFile()` 仅写入审计日志，不产生 outbox 事件。受让方无法通过 SSE（Server-Sent Events）实时感知自己被分享，只能通过轮询 `ListSharedWithMe()` 发现新文件。这与消息（`message.sent`）、评论（`comment.replied`）的实时推送机制不一致。该缺口削弱了 aero-id 作为"数据归属账户"平台的用户体验。

### 1.3 架构债务与技术债

**死接口 `hub.Pusher`。** `hub.go:134` 定义了一个 `Pusher` 接口（签名 `Publish(uid, eventType, data string)`），但 `Hub` 的真正实现由 `hub/adapter.go` 通过 `service.NotificationPusher` 接口（多一个 `eventID int64` 参数）暴露。`hub.Pusher` 没有任何引用者，是一个遗留的僵尸接口。虽然不影响运行，但属于代码异味，增加新人的认知负担。

**幂等性预留超时硬编码。** `reservationTTL = 2 * time.Minute` 是包级常量，而 `HTTPConfig` 已有 `IdempotencyTTL`（响应保留时长）但缺少 `IdempotencyReservationTTL`（预留超时）。对于跨数据中心部署的网络延迟差异（2 分钟在某些场景过短，在另一些场景过长），缺乏调优能力。

**SSE Hub 缺乏可观测性与准入控制。** 慢消费者的通道满时静默丢弃事件 (`select default: drop`)，无日志、无指标、无计数器。在生产环境出现订阅者消费能力不足的情况时，运维人员无法从监控中发现，只能从用户反馈中间接推断。此外没有连接数限制，单个恶意客户端可以耗尽连接资源。

---

## 2. 扩展方向

### 方向一：文件分享事件化（P1）

**为什么需要：** 当前受让方只能通过轮询发现被分享的文件，与消息/评论的实时推送能力不一致。将 `file.share` / `file.unshare` 纳入 outbox 事件流，可以让受让方通过 SSE 实时收到通知，并解锁后续的 webhook 通知场景。

**核心挑战：**
- 与消息、评论不同，文件分享不自动产生"收件人"——`ShareFile()` 是显式指定 grantee UID 的集合，这恰好适配 notification dispatcher 的 per-recipient 模型。但要注意不要为每个 grantee 创建重复事件。
- 审计日志（`s.audit.Log`）和 outbox 事件可能存在重复——需要决定是让审计成为 outbox handler 的一部分，还是保持二者独立。

**预期的架构变更：**
- 在 `domain` 或 `pkg/outbox` 中新增 `file.share` / `file.unshare` 事件类型
- `ShareFile()` 和 `UnshareFile()` 调用 `s.shares.Grant()` / `Revoke()` 后，构造 outbox 事件并写入 `EventRepository`
- `NotificationDispatcher.Handle` 的 switch 中增加对 `file.share` / `file.unshare` 的处理
- 受让方通过 SSE 收到 `file.shared` 事件后，可选择调用 `ListSharedWithMe()` 获取详情

**对现有系统的影响：**
- 低侵入性——完全沿用已有的 outbox + dispatcher + SSE 链路
- `repository.FileShareRepository` 不需要变更
- 审计日志可继续保持独立，或考虑将审计事件也纳入 outbox（降低耦合）

### 方向二：gRPC 幂等性（P1）

**为什么需要：** 幂等性防止重复执行导致的状态不一致。HTTP 传输层已有完备实现，gRPC 缺失意味着内部服务（如批处理任务、消息队列消费者）不能安全重试。对于涉及创建消息、评论、文件的 RPC（`SendMessage`、`CreateComment`、`UploadFile`），幂等性是可靠性的基础。

**核心挑战：**
- gRPC 没有类似 HTTP `Idempotency-Key` 的标准头。需要在 proto 的请求消息中显式添加 `idempotency_key` 字段，或者使用 gRPC metadata 传递。
- 选择 metadata 方式可以避免修改 proto 文件（只需在客户端 SDK 层注入），但失去 schema 层面的可见性。选择 proto 字段方式更显式透明，但需要升级所有客户端。
- 幂等性需要存储后端——HTTP 层使用 `idempotency.GormStore`，gRPC 层可以复用同一实现。

**预期的架构变更：**
- 选项 A（proto 字段）：在每个 mutating RPC 的请求消息中添加 `string idempotency_key = 1`，然后实现一个 gRPC 拦截器，从请求中提取 key 并执行 reserve/complete 逻辑。
- 选项 B（metadata）：实现 gRPC 拦截器，从 `metadata` 中读取 `idempotency-key`。客户端 SDK 需要在每个请求中添加 metadata。无需修改 proto。
- 两个选项共同的变更：新增 `grpc.UnaryServerInterceptor`，注入到 `server.go` 的拦截器链中，排在 auth 之后、handler 之前。需要向 `ServerOption` 新增 `WithIdempotency` 选项。

**对现有系统的影响：**
- 选项 B 对 proto 零修改，推荐作为首个实现版本
- 选项 A 可作为长期演进（proto v2）
- 幂等键需要确定作用域——按 `(uid, method, key)` 还是 `(uid, key)`？建议按 `(uid, method, key)` 以允许同一 key 调用不同方法

### 方向三：SSE Hub 连接准入与可观测性（P2）

**为什么需要：** 当前实现没有任何准入控制，慢消费者导致的丢包不可见。在用户量增长时，少量异常客户端可以拖慢整个 hub，且运维无法溯源。

**核心挑战：**
- 连接准入策略的选择：按 uid 限数（每个用户最多 N 个连接）、全局限数（整个实例最多 M 个连接）、还是按 IP 限数？
- 慢消费者检测：是主动关闭过慢的连接，还是只提供监控指标让运维决策？

**预期的架构变更：**
- `Hub` 新增可选的最大连接数（`maxConns int`）和每个 uid 的最大连接数（`maxConnsPerUID int`）
- `Subscribe()` 在分配前检查当前连接数，超过限制返回错误
- 慢消费者可观测性：新增 `metrics.HubMetrics` 记录连接数、丢弃事件数、通道积压深度
- 可选的 per-connection 最大生命周期：`connTTL`，通过 `time.After` 触发优雅断开，客户端通过 `Last-Event-ID` 重连
- 事件丢弃添加日志（采样率控制，避免日志洪泛）

**对现有系统的影响：**
- `Hub` 构造函数的 signature 需要扩展——建议使用 `HubOption` 函数式选项模式，向后兼容
- SSE handler (`events.go`) 需要处理 `Subscribe` 返回的错误（当前不返回）
- 客户端侧不受影响，重连行为不变

### 方向四：幂等性参数配置化（P2）

**为什么需要：** `reservationTTL = 2 * time.Minute` 硬编码无法适应不同部署环境的网络延迟差异。跨区域部署可能需要 5 分钟，而同一 AZ 的延迟敏感场景可能只需 30 秒。

**核心挑战：**
- 幂等性有两个 TTL：预留超时（reservation）和响应保留（completed response）。二者在语义上不同，需要单独的配置键。
- 预留超时的最大值不能超过 `IdempotencyTTL`（已完成响应的保留时长），否则会出现预留未过期但响应已被清理的不一致状态。

**预期的架构变更：**
- `HTTPConfig` 新增 `IdempotencyReservationTTL` 字段（`time.Duration`），默认 2 分钟
- 在 `app.go` 中创建 `GormStore` 后，将配置的预留超时传入（或通过 `SetReservationTTL` 方法）
- `config.yaml` 添加对应的键，环境变量 `AERO_ID_SERVER_HTTP_IDEMPOTENCY_RESERVATION_TTL`
- 在 `validate()` 中添加 `IdempotencyReservationTTL <= IdempotencyTTL` 的约束

**对现有系统的影响：**
- 零影响——纯新增配置字段，默认值保持与现有常量一致
- `GormStore` 需要改为可配置的 TTL，而不是读取包级常量

### 方向五：CORS 来源显式配置（P1）

**为什么需要：** 通配符 CORS 在生产环境中是安全风险。即使 aero-id 依赖 SSO token 鉴权，CORS 通配符仍允许任意域名的页面发起带凭证的请求。配合浏览器漏洞或 SSO 的隐性授权流程，可能扩大攻击面。

**核心挑战：**
- 配置启动时要求必须设置 `CORSOrigins` 可能需要协调前端的部署流程
- 开发环境需要宽松策略——可以保留空 origins = wildcard 的行为，但在生产环境建议增加一个配置校验警告或硬性要求

**预期的架构变更：**
- `config.yaml` 的 `server.http` 下新增 `cors_origins` 字段（`[]string`），空列表 = wildcard
- `app.go` 中从 `cfg.Server.HTTP.CORSOrigins` 传递到 `RouterConfig.CORSOrigins`
- 可选：在 `validate()` 中，当 `cfg.App.Env == "production"` 且 `cors_origins` 为空时输出 warning（不强 blocking，避免启动失败）

**对现有系统的影响：**
- 零侵入——CORS middleware 的行为语义不变，只是 origins 来源从 `app.go` 中的零值变为配置值
- 纯新增字段，向后完全兼容

---

## 3. 接口设计建议

### 3.1 关键接口设计原则

当前的设计遵循了几个正确原则，需要在扩展中继续保持：

**接口应小且聚焦。** `service.NotificationPusher`（一个方法）、`idempotency.Store`（三个方法）、`RouteRegistrar`（一个方法）都是好的例子。未来的扩展也应遵循这个模式——新增功能不要往现有接口上堆积方法，而是定义新的、单一的接口。

**接口属于消费者。** `service.NotificationPusher` 定义在 service 包，由 hub 包实现。`TokenValidator` 定义在 `middleware` 包和 `grpc` 包，由 `sso.Client` 实现。这是依赖倒置的正确方向——接口靠近使用方。

**配置优先于硬编码。** 所有新的行为参数（TTL、限制、超时）都应先出现在 config 结构体中，有合理的默认值，并可通过环境变量覆盖。当前 `config.go` 的设计是这个原则的好范例，要延续。

### 3.2 是否需要引入新的抽象层

**不建议。** 当前的 5 层抽象（handler → service → repository/storage/sso）已经足够。新增抽象层只会增加不必要的间接性。以下是针对每个方向的判断：

- **文件分享事件化**：无需新抽象层，完全复用现有的 outbox.Event → NotificationDispatcher → SSE 链路。
- **gRPC 幂等性**：无需新抽象层，复用已有的 `idempotency.Store` 接口。需要一个新的 gRPC 拦截器，但它属于 transport 层，不是新抽象。
- **SSE Hub 准入控制**：在 Hub 内部增加配置选项和指标计数，不需要新接口。
- **CORS 配置化**：纯配置变更。
- **参数配置化**：纯配置变更。

### 3.3 向后兼容性

五个方向都可以做到零破坏性变更：

1. **文件分享事件化**：新增 outbox 事件类型不影响已有消费者。现有客户端继续通过 `ListSharedWithMe()` 轮询即可，SSE 是可选增益。
2. **gRPC 幂等性（选项 B）**：metadata 方式无需修改 proto，现有 gRPC 客户端不带幂等键仍可正常工作（幂等性未启用或 fallback 到无保证状态）。
3. **SSE Hub 准入控制**：`HubOption` 模式确保所有新参数有默认值无限制，现有代码无需修改。
4. **CORS 配置化**：空列表 = wildcard 的语义保持不变。
5. **幂等性参数配置化**：默认值等于现有常量。

---

## 4. 技术选型

### 4.1 是否需要引入新的技术栈

**不需要。** 五个方向的实现都可以在当前技术栈内完成：
- 数据库：Postgres（已有）
- 存储：Local / S3（已有）
- 消息传递：Transactional outbox + SSE（已有）
- 鉴权：SSO gRPC（已有）
- 框架：Gin + gRPC + GORM（已有）

当前技术栈的选型是合理的，没有引入新依赖的必要。

**一个需要评估的潜在扩展（非本次范围）：** 当跨实例的 SSE 连接分发成为瓶颈时（多副本部署时，发布给 uid A 的事件只有连接在实例 1 上的订阅者能收到），可能需要引入 Redis Pub/Sub 或 NATS 作为跨实例事件总线。这是未来的扩展点，当前单实例架构下不需要。

### 4.2 第三方依赖的评估标准

对于未来可能引入的依赖，建议遵循以下标准：

| 标准 | 门槛 | 原因 |
|---|---|---|
| 许可证兼容 | Apache 2.0 / MIT / BSD | 避免商业限制 |
| 活跃维护 | 过去 12 个月有提交 | 安全修复 |
| Go 模块支持 | 是 | 当前构建体系 |
| 无 CGO 依赖 | 优先 | 交叉编译 |
| 代码质量 | golangci-lint 通过 | 一致性 |
| API 稳定性 | v1+ 或 go 1 promise | 避免 breaking changes |

### 4.3 自建 vs 采购

当前五个方向的实现工作都在自建范围内，不需要评估外部服务。

**未来可能的评估场景：** 如果 SSE 跨实例分发成为瓶颈，评估方案包括：
- **自建**：基于 Redis Pub/Sub 或 NATS 的跨实例事件桥接（开源自建，成本低，运维复杂度中等）
- **采购**：Pusher / Ably / Fanout 等 SaaS（零运维，但引入外部依赖和延迟）

建议初期使用 Redis Pub/Sub 自建方案，因为 aero-id 当前没有 Redis 依赖，而 SSO 层面已有 gRPC 连接管理经验。

---

## 5. 实施路线图

### 5.1 优先级排序

```
P1 - CORS 来源配置化          (安全 · 低侵入 · 快速见效)
P1 - gRPC 幂等性              (可靠性 · 双协议对称性)
P1 - 文件分享事件化            (功能完整性 · 用户体验)
P2 - SSE Hub 准入与可观测性    (运维 · 弹性)
P2 - 幂等性参数配置化          (运维 · 调优)
```

### 5.2 阶段划分

**阶段一：安全加固（Week 1）**

| 里程碑 | 交付物 | 风险 |
|---|---|---|
| CORS 配置化 | `HTTPConfig.CORSOrigins` + `config.yaml` 更新 + 生产环境部署配置审计 | 低——纯新增字段，默认行为不变 |
| 死接口清理 | 删除 `hub.Pusher` 僵尸接口 | 低——`grep` 确认无引用后直接删除 |

**阶段二：传输层对称性（Week 2）**

| 里程碑 | 交付物 | 风险 |
|---|---|---|
| gRPC 幂等性拦截器 | metadata 方式实现 + 注入 server interceptor chain | 中——需要决定 key 的作用域和过期行为，与 HTTP 层幂等性实现一致 |
| 集成测试覆盖 | gRPC idempotency test suite | 低——测试套件验证边界情况 |

**阶段三：事件完整性（Week 3）**

| 里程碑 | 交付物 | 风险 |
|---|---|---|
| `file.share` / `file.unshare` 事件类型 | `domain` 事件枚举更新 + `NotificationDispatcher` switch 扩展 | 中——outbox event payload schema 设计需要慎重，避免后续 breaking change |
| SSE 端自动推送 | 受让方在线时收到实时通知 | 低——自行有的事件链路 | 
| 集成测试 | outbox → dispatcher → notification row → SSE 全链路测试 | 低 |

**阶段四：运营韧性（Week 4）**

| 里程碑 | 交付物 | 风险 |
|---|---|---|
| SSE Hub 准入控制 | 最大连接数 + 最大每 uid 连接数 + `HubOption` API | 中——需要确定合理的默认值 |
| SSE Hub 可观测性 | 连接数仪表、丢包计数器、慢消费者指标 | 低——新增 metrics registry |
| 幂等性参数配置化 | `IdempotencyReservationTTL` + config validate | 低——纯配置变更 |

### 5.3 风险点与缓解策略

| 风险 | 概率 | 影响 | 缓解 |
|---|---|---|---|
| gRPC 幂等性与 HTTP 幂等性行为不一致 | 中 | 高——双协议承诺的"行为一致"受损 | 两个拦截器共享同一 `idempotency.Store` 实现，且共用 `(uid, key)` 命名空间 |
| 文件分享事件导致 notification 表膨胀 | 低 | 中——每次 share 多个 grantee 会批量产生通知行 | dispatcher 已经支持去重折叠（collapse），可将短时间内同一文件的 share 事件折叠为一条通知 |
| SSE Hub 准入限制误杀正常用户 | 低 | 高——多设备用户超过每 uid 限制 | 默认不设上限（0 = unlimited），由运维根据实际用户画像配置 |
| 死接口清理 CI 遗漏引用 | 极低 | 低 | `make lint` + `go vet` 会发现未使用的接口 |

### 5.4 建议的迭代策略

每个阶段完成后运行 `make check-file-sizes && make lint && make test` 门禁（已在 AGENTS.md 中约定）。对于超过 400 行但未到 500 行的文件（如 `app.go` 当前 ~210 行、`server.go` ~210 行、`store.go` ~140 行），目前都在安全范围内，不需要拆分。但如果 `app.go` 因扩展大幅增长（比如新增 3 个拦截器选项），建议按职责拆分：

- `app.go` → `app.go`（组合根）+ `app_options.go`（option 配置逻辑）+ `app_warmup.go`（预热逻辑）

---

## 总结

aero-id 的架构基础是扎实的——分层清晰、依赖单向、错误处理统一、可配置性强。五个待修复方向反映了架构从 MVP 快速演进过程中自然产生的"打磨缺口"而非根本性缺陷。P1 级别的三个修复（CORS 配置化、gRPC 幂等性、文件分享事件化）分别对应安全、可靠性、功能完整性，建议优先投入。整体修复工作量估计在 2-3 人周以内，且风险可控。
