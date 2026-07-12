# 架构分析报告：aero-id 项目

## 1. 架构评估

### 1.1 当前架构的优势

**清晰的分层单向依赖**

从分析文档和 AGENTS.md 可以看出，aero-id 严格遵循 `handler → service → repository → Postgres` 的单向依赖链。这是经过深思熟虑的经典分层架构，优势在于：

- **测试性**：每一层都可以通过 mock/stub 独立测试（`internal/service/mocks` 的存在证实了这一点）
- **可替换性**：storage 层抽象为 `FileStorage` 接口，支持 local 和 s3 两种实现，切换只需修改配置
- **关注点分离**：handler 只做 I/O 编解码，service 承载全部业务逻辑，repository 负责持久化

**领域驱动设计的萌芽**

- `internal/domain/` 目录承载了 `ban.go` 等实体定义和内置方法（如 `UserBan.Expired()`），表明团队有领域模型意识
- 软删除（`gorm.DeletedAt`）的统一使用，体现了对数据生命周期的统一治理

**工程规范严格**

- 500 行文件上限、50 行函数上限、圈复杂度 ≤10、if 嵌套 ≤3 层——这些硬约束虽然增加了开发成本，但有效防止了技术债务的恶性累积
- `make check-file-sizes`、`make lint-strict` 的可自动化执行进一步降低了维护成本

### 1.2 架构局限性

**SSE Hub 是当前最薄弱的设计点**

从 Gap 1 的分析来看，Hub 存在以下架构问题：

| 问题 | 影响 | 严重程度 |
|------|------|----------|
| 缓冲区固定 16，满则静默丢弃 | 高负载下消息丢失，无告警 | **高** |
| 无 Prometheus 指标 | 运维无法感知 Hub 健康状况 | **高** |
| 无生命周期管理 | 连接泄漏、goroutine 泄漏风险 | **中** |
| `sync.Map` + 分片互斥锁有改进但仍不完善 | 热点 uid 场景下性能瓶颈 | **中** |

**跨实例能力缺失是扩展性的根本瓶颈**

Gap 5 指出 Hub、Limiter、Cache 全部是进程内实例，这是单体架构向分布式演进的核心障碍。当前架构本质上是一个**有状态单体**，无法直接水平扩展。

**共享体系的持久化设计不完整**

Gap 3 表明文件共享缺少 `expires_at` 和 `max_access_count`，这意味着：

- 分享出去的链接永久有效，无法过期
- 无法限制访问次数，存在滥用风险
- 无权访问审计，无法追溯谁访问了哪个共享文件

**通知通道扩展性未预留**

Gap 4 的缺失（无 email/SMTP 基础设施）表明通知系统从设计上就只考虑了 SSE 实时推送，没有抽象出通知通道接口。未来如果增加邮件、短信、Webhook 等通道，需要从零开始设计接口。

### 1.3 技术债务清单

| 债务项 | 类型 | 影响 | 修复优先级 |
|--------|------|------|-----------|
| Hub 静默丢弃消息 | 设计债 | 数据丢失，运维盲区 | P0 |
| 无跨实例基础设施 | 架构债 | 无法水平扩展 | P1 |
| 共享无过期/次数限制 | 功能债 | 安全隐患 | P1 |
| 通知无通道抽象 | 设计债 | 扩展新通道成本高 | P2 |
| Limiter 无指标暴露 | 运维债 | 无法评估限流效果 | P2 |
| Cache 无命中率/逐出率指标 | 运维债 | 缓存效率不可见 | P2 |

---

## 2. 扩展方向

### 方向一：SSE Hub 可观测性 + 可靠性提升

**为什么需要**

Hub 是用户实时接收消息的关键路径。当前的设计下：
- 高负载时消息静默丢弃，用户侧表现为消息丢失但无任何告警
- 运维无法定位问题根源（无指标、无日志）
- 这是当前债项中**影响面最广、修复成本最低**的一个

**核心挑战**

- 在不大幅重构的前提下，将可靠的背压机制引入 select 模式
- 指标定义要符合 Prometheus 最佳实践，避免指标爆炸
- 缓冲区大小的配置化 vs 自适应策略的取舍

**预期的架构变更**

```
当前:
Hub (固定 16 缓冲, 静默 drop)

建议:
Hub {
  bufferSize: configurable (default: 64)
  metrics: {
    hub_messages_sent_total{uid, channel}
    hub_messages_dropped_total{uid, channel, reason}
    hub_subscribers_active
    hub_buffer_utilization{uid}
  }
  drop策略: 从 silent drop → 可选策略
    - silent (当前行为)
    - error (返回错误给调用方)
    - wait (阻塞直到有空间)
    - evict_oldest (丢弃最旧消息给新消息让路)
}
```

**对现有系统的影响**

- 对 Hub 内部修改，外部接口不变
- Service 层的调用方无需修改
- 新增 ~50 行 Prometheus 指标注册 + ~30 行配置逻辑

### 方向二：跨实例消息总线

**为什么需要**

这是从单实例走向水平扩展的**核心架构升级**。没有跨实例能力：
- 无法部署多个副本实现 HA
- 实例故障导致该实例的 SSE 连接全部中断，用户需要重新连接
- 限流计数器、缓存均为进程级，多实例下效果减半

**核心挑战**

| 挑战 | 说明 | 难点等级 |
|------|------|----------|
| 消息有序性 | 同一用户的消息在不同实例间广播，需要保证顺序 | ⭐⭐⭐ |
| 连接迁移 | 实例故障时如何将用户的 SSE 连接迁移到健康实例 | ⭐⭐⭐⭐ |
| 缓存一致性 | 多个实例共享缓存后，本地缓存的价值何在 | ⭐⭐ |
| 限流准确性 | 跨实例限流依赖 Redis 原子计数，存在网络延迟 | ⭐⭐ |

**预期的架构变更**

```
Phase 1: Leader Election (P1)
  - 基于 Redis SETNX 的轻量选举
  - 仅选出一个 master 负责全局定时任务（过期清理、数据导出）
  - 评估点：确认当前是否真的需要多实例

Phase 2: Redis Pub/Sub (P2)  
  auth service
    ↓ produce
  [Redis Pub/Sub: "events:user:{uid}"]
    ↓ consume (所有实例)
  Hub (instance 1) ──→ SSE to connected users
  Hub (instance 2) ──→ SSE to connected users

Phase 3: 连接迁移 (P2)
  - 用户建立 SSE 时注册到 Redis 的 `uid→instance` 映射
  - 实例故障后，用户重连时发现新实例，从断点继续
```

**对现有系统的影响**

- 需要引入 Redis 依赖（测试用 testcontainers 模拟，类似当前 MinIO 的做法）
- Hub 从纯内存结构变为「本地订阅 + Redis 订阅」的双层结构
- Connect/Disconnect 生命周期事件需要上报到 Redis
- **最大风险**：引入网络依赖后，故障模式复杂化（网络分区、Redis 故障）

### 方向三：文件共享体系的完整化

**为什么需要**

当前的文件共享是一个"开放永不过期"的链接，这在实际业务中是不可接受的：
- 用户分享敏感文件后无法撤销
- 分享链接被爬虫抓取后永久可用
- 无法限制访问次数（如"仅限 3 次下载"）
- 无访问审计，合规场景（GDPR、SOC2）无法满足

**核心挑战**

- `expires_at` 的过期清理策略：应用层定时清理 vs 数据库扫描
- `max_access_count` 的并发安全：高并发下计数器的原子性
- 向后兼容：现有已创建的分享如何处理缺失的字段

**预期的架构变更**

```
数据库变更:
ALTER TABLE file_shares ADD COLUMN expires_at TIMESTAMPTZ;
ALTER TABLE file_shares ADD COLUMN max_access_count INT;
ALTER TABLE file_shares ADD COLUMN access_count INT DEFAULT 0;

新增表:
file_share_access_logs {
  id            UUIDv7 PK
  share_id      UUID   FK → file_shares
  accessor_uid  TEXT   (即使是匿名也要记录 IP 或标识)
  accessed_at   TIMESTAMPTZ
  ip_address    INET
}

Service 层变更:
- ShareService.Create() 接受可选 expires_at, max_access_count
- ShareService.Access() 检查过期 + 计数 + 记录审计日志
- 新增 ExpiredSharesCleanup() 定时任务（需要跨实例协调）

向后兼容策略:
- 已有分享的 expires_at = NULL 表示永不过期
- max_access_count = NULL 表示不限制
- 迁移期间新旧接口并存
```

**对现有系统的影响**

- 主要在 repository 和 service 层修改，handler 层新增可选参数
- 需要新增后台定时任务（依赖于方向二的跨实例协调或单实例简单实现）
- 对已有分享**零影响**（NULL 兼容）

### 方向四：通知通道抽象层

**为什么需要**

当前只支持 SSE 实时推送（web 在线场景）。但实际业务中：
- 用户不在线时，需要邮件通知
- 移动端需要 APNs/FCM 推送
- 第三方集成需要 Webhook 回调

没有抽象层意味着每新增一个通道都要改动 Service 层，违反了开闭原则。

**核心挑战**

- 通道抽象接口的设计：同步 vs 异步、重试策略、优先级
- 邮件通道的发送速率控制、退信处理、模板管理
- 通道间去重：SSE 投递成功是否还要发邮件？

**预期的架构变更**

```go
// 新的抽象层
type NotificationChannel interface {
  Name() string
  Send(ctx context.Context, notif Notification) error
  Priority() int // 决定通道优先级顺序
}

type Notification struct {
  UID      string
  Type     NotificationType // message_received, file_shared, etc.
  Title    string
  Body     string
  Metadata map[string]string
}

// 当前的 Hub 实现这个接口
type sseChannel struct { hub *Hub }

// 新的邮件实现
type emailChannel struct {
  smtpHost   string
  fromAddr   string
  rateLimiter *rate.Limiter
  templates  *templateRegistry
}

// Service 层
type NotificationService struct {
  channels []NotificationChannel
  deduper  *Deduper // 去重逻辑
}
```

**对现有系统的影响**

- Service 层的 SendNotification() 从直接调用 Hub.Publish() 改为遍历 NotificationService.channels
- 现有 SSE 行为不变（sseChannel 封装了 Hub）
- 邮件通道是纯新增，零侵入
- 最大挑战：**模板管理和国际化**（邮件内容通常需要支持多语言、个性化）

### 方向五：审计与合规体系

**为什么需要**

从已实现的 `user.ban`（Gap 2，已关闭）和 `data_export_jobs`（migration 000025）来看，团队已经有合规意识。但缺少一个统一的审计框架：

- 当前审计事件散落在 service 层各处（如 ban 的审计在 `service/ban.go`）
- 没有统一的审计事件格式和存储
- 没有审计事件的查询和导出 API

**核心挑战**

- 审计事件的分类和标准化（事件类型、严重级别、数据分类）
- 审计日志的存储策略（存多久、如何归档、如何防篡改）
- 审计查询的性能（海量审计数据下的全文检索）

**预期的架构变更**

```go
// 统一审计事件
type AuditEvent struct {
  ID          UUIDv7
  EventType   EventType // BAN_USER, DELETE_FILE, SHARE_CREATE, etc.
  ActorUID    string    // 谁做的
  TargetUID   string    // 对谁做的
  ResourceType string   // file, message, comment, etc.
  ResourceID  string
  Changes     map[string]Change  // Before/After
  Outcome     Outcome   // SUCCESS, DENIED, FAILED
  ClientIP    string
  UserAgent   string
  Timestamp   time.Time
}

// 存储策略
// - 最近 90 天在 PostgreSQL 主表
// - 超过 90 天的自动迁移到 S3/冷存储
// - 通过分区表支持按时间裁剪
```

**对现有系统的影响**

- 这是横切关注点，影响所有 service 方法
- 推荐使用 middleware/decorator 模式，而非在每个 service 方法中手写审计代码
- 与方向二（跨实例）相关：审计事件应该通过消息队列异步写入，避免增加请求延迟
- 最大风险：**过度审计导致性能问题**，需要在服务端做采样或异步批处理

---

## 3. 接口设计建议

### 3.1 关键模块接口设计原则

**现状评估**

当前已有不错的抽象基础：
- `FileStorage` 接口（local/S3 双实现）
- `BanRepository` 接口（清晰定义 CRUD）
- `NotificationChannel` 尚未存在

**建议原则**

| 原则 | 说明 |
|------|------|
| **小接口优先** | 接口 2-4 个方法最佳，避免大而全的 `Service` 接口 |
| **接受的输入结构体** | 核心方法接受输入参数结构体而非平铺参数，便于扩展 |
| **错误语义清晰** | Service 接口返回领域错误，不暴露基础设施错误 |
| **上下文优先** | 所有公共方法第一个参数必须是 `context.Context` |
| **接口定义在消费侧** | 定义在 service 包中，reposity 去实现，而非定义在 repository 包中 |

### 3.2 是否需要新的抽象层

**需要引入**：

1. **`NotificationChannel` 接口**（方向四所需）
   - 在 `internal/service/notification/` 包中定义
   - 当前 Hub 作为第一个实现（`sseChannel`）
   - 扩展点明确：只需新增实现即可添加新通道

2. **`AuditRecorder` 接口**（方向五所需）
   - 在 `internal/service/audit/` 包中定义
   - 当前可以有同步 PostgreSQL 实现和异步队列实现
   - 为 service 层提供 `Record(ctx, event)` 方法

**不建议引入**：

- 不要在 repository 层之上再加 "DAO" 抽象层——GORM 的 `*gorm.DB` 已经是 ORM 抽象，再加一层纯属过度设计
- 不要为 Config 引入接口——`config.Load()` 的静态函数签名足够简洁，配置的结构体本身就是契约

### 3.3 向后兼容性策略

```
API 版本化:
  当前: /v1/* (隐式 v1)
  未来: /v2/* (大版本升级)
  
数据库迁移:
  - 新列使用 NULL 默认值
  - 旧代码不感知新列
  - 不允许 destructive 的 migration（如 DROP COLUMN）
  
消息格式:
  - JSON 字段使用 omitempty
  - protobuf 字段使用 optional
  - Service 层错误码不删除，只废弃（用 deprecated 注释标记）
```

---

## 4. 技术选型

### 4.1 是否需要引入新技术

**Redis（高优先级引入）**

| 评估维度 | 结论 |
|---------|------|
| 必要性 | 跨实例同步、分布式限流、分布式缓存都需要 |
| 成熟度 | Go 生态中 `go-redis` v9 非常成熟 |
| 运维成本 | Redis 是标准中间件，团队已有经验的可能性高 |
| 测试支持 | testcontainers 已经集成在项目中，可以复用 |

决策建议：**引入 Redis**。从最简单的用途开始（leader election + 缓存），逐步扩展。初期只需要一个单节点 Redis，不引入 sentinel/cluster。

**SMTP 邮件库（按需引入）**

- 使用标准库 `net/smtp` 即可满足基本需求
- 高级功能（模板、附件、队列）可考虑 `github.com/emersion/go-smtp`
- 建议使用标准库开始，不要过早引入邮件框架

**消息队列（暂缓）**

- 当前流量规模下，Redis Pub/Sub + 进程内消息足以应对
- Kafka/RabbitMQ 的引入会增加运维复杂度，建议只有出现以下信号时再考虑：
  - 单实例 Hub 的吞吐超过 10K msg/s
  - 需要消息持久化和重启后重放
  - 有多个微服务需要消费同一事件流

### 4.2 第三方依赖评估标准

参照当前项目对 protobuf 工具链的精确定位（`protoc-gen-go@v1.34.1`, `protoc-gen-go-grpc@v1.5.1`），建议所有新依赖遵循同样的准入标准：

```
1. 许可证兼容性（必须是 BSD/MIT/Apache 2.0）
2. Go 最小版本匹配（当前 1.26）
3. 测试覆盖率 ≥ 70%（通过 go.dev 查看）
4. 最近 6 个月有提交（活跃维护）
5. 依赖树深度 ≤ 5 层（避免依赖地狱）
6. 与当前项目的 go.mod 中已有的依赖不产生冲突版本
```

### 4.3 自建 vs 采购

| 功能 | 建议 | 理由 |
|------|------|------|
| SSE/实时推送 | **自建**（当前方案） | 核心差异能力，改动频率高 |
| 邮件发送 | **采购**（SendGrid/Mailgun） | 非核心，延迟敏感，运维邮件基础设施成本高 |
| Redis | **自建**（部署标准 Redis） | 基础中间件，团队控制版本和配置 |
| 文件存储 | **自建**（MinIO 或 S3） | 已有 FileStorage 抽象，MinIO 兼容 S3 API |
| 审计日志查询 | **自建**（PostgreSQL 分区表） | 数据量大但查询模式简单，无需 Elasticsearch |

---

## 5. 实施路线图

### 5.1 优先级排序

```
P0 (必须立即修复)
├── Gap 1 Phase 1: Hub 可观测性
│   ├── 添加 Prometheus 指标
│   ├── 缓冲区大小可配置
│   ├── 添加 drop 日志（至少 warn 级别）
│   └── 预估工作量: 0.5-1 人天
│
├── Gap 2: user.ban
│   └── 代码已就绪，需 git add + commit + review
│
└── Gap 3 Phase 1: 共享过期
    ├── 数据库 migration 新增 expires_at
    ├── Service 层校验
    ├── 定时清理任务
    ├── 代码量: ~150 行
    └── 预估工作量: 1-2 人天

P1 (迭代计划中)
├── Gap 5 Phase 1: 引入 Redis
│   ├── go-redis 集成
│   ├── testcontainers 测试配置
│   ├── 替换 Cache 实现为 Redis
│   ├── 代码量: ~200 行
│   └── 预估工作量: 2-3 人天
│
├── Gap 1 Phase 2: Hub 连接生命周期管理
│   ├── 连接超时自动断开
│   ├── 健康检查 ping/pong
│   ├── Graceful shutdown
│   └── 预估工作量: 2 人天
│
├── Gap 3 Phase 2: 访问计数 + 审计
│   ├── max_access_count + access_count
│   ├── file_share_access_logs 表
│   ├── Service 层 AtomicIncrement
│   └── 预估工作量: 1-2 人天
│
└── 审计框架
    ├── AuditEvent 结构体定义
    ├── middleware/decorator 模式
    ├── PostgreSQL 分区表存储
    └── 预估工作量: 2-3 人天

P2 (后续规划)
├── Gap 4: Email 通知通道
│   ├── NotificationChannel 抽象
│   ├── SMTP 实现
│   ├── 模板管理
│   ├── 代码量: ~350 行
│   └── 预估工作量: 3-5 人天
│
├── Gap 5 Phase 2-4: 跨实例消息
│   ├── Redis Pub/Sub 集成到 Hub
│   ├── 连接注册 + 故障检测
│   ├── 连接迁移
│   ├── 代码量: ~500 行
│   └── 预估工作量: 5-8 人天
│
└── Gap 1 Phase 3: 自适应缓冲区
    ├── 基于历史负载的自适应缓冲区大小
    ├── 用户级背压策略
    └── 预估工作量: 3-5 人天
```

### 5.2 阶段划分

```
里程碑 1: 可观测性基线（P0 任务，1 周）
├── Hub 指标 + 可配置缓冲区
├── user.ban 提交
├── 文件共享 expires_at
└── 验证：make test && make lint && 集成测试通过

里程碑 2: 分布式基础 + 运维就绪（P1 任务，2 周）
├── Redis 集成
├── 缓存改为 Redis
├── 限流器改为分布式
├── 连接生命周期管理
└── 验证：2 个实例并行运行通过集成测试

里程碑 3: 共享体系完整化（P1 任务，1 周）
├── 访问计数
├── 访问审计日志
├── 定时清理任务
└── 验证：分享过期后被拒绝访问

里程碑 4: 全通道通知（P2 任务，2 周）
├── NotificationChannel 接口
├── Email 通道
├── 模板系统
└── 验证：用户在线收 SSE，离线收邮件

里程碑 5: 水平可扩展（P2 任务，3 周）
├── Redis Pub/Sub Hub
├── 连接迁移
├── 故障恢复
└── 验证：随意停止一个实例，用户连接自动恢复
```

### 5.3 风险点和缓解策略

| 风险 | 可能性 | 影响 | 缓解策略 |
|------|--------|------|----------|
| **Redis 引入导致测试复杂度大幅增加** | 中 | 中 | 复用已有的 testcontainers 模式；提供 `inmemory` 实现用于单元测试，Redis 实现只在集成测试中使用 |
| **审计日志查询性能不满足** | 中 | 高 | 先评估现有的 P99 查询延迟；使用分区表 + 物化视图；如果仍不满足再考虑引入 Elasticsearch |
| **跨实例连接迁移导致消息丢失** | 高 | 高 | 使用优雅关闭模式（实例停止前等待所有连接处理完毕）；客户端实现指数退避重连；连接迁移期间的消息使用 Redis 消息队列存蓄 |
| **邮件通道被判定为垃圾邮件** | 中 | 中 | 采购专业邮件服务（SendGrid等），利用其递送优化和信誉管理能力 |
| **多版本同时开发导致代码冲突** | 低 | 中 | 严格遵循 AGENTS.md 的文件大小限制和职责分离，减少同一文件被多人修改的概率 |

---

## 总结

aero-id 的架构基础扎实——清晰的分层架构、严格的文件约束、领域模型意识。当前最大短板是**可观测性缺失**和**跨实例能力的空白**。

优先级的核心逻辑是：

1. **先可观测**（P0）：都不知道系统出了什么问题，其他优化都是盲人摸象
2. **再分布式基础**（P1）：在需要水平扩展之前先打好基础，避免后续大规模重构
3. **然后补齐功能**（P1）：共享过期、访问审计等业务核心能力
4. **最后横向扩展**（P2）：SSE 跨实例、通知通道扩展

需要特别强调的是，**user.ban 的完成是一个非常好的信号**——它证明了分析-验证-修复的工作流是有效的。推荐将这种模式制度化：每次架构评审后，gap 跟踪表作为文档提交到 `docs/architecture/gap-tracking.md`，在每次 sprint 回顾中复盘。
