现在我已经对代码库有了全面的了解。以下是我的架构分析。

---

# aero-id 架构分析报告

## 1. 架构评估

### 1.1 核心优势

**分层架构扎实**。代码库严格遵循 `handler → service → repository/storage` 的单向依赖，这是可维护性与测试性的正确选择。具体来说：

- **handler 层纯属适配器**：HTTP 和 gRPC 处理程序仅做 I/O 转换——解码、调用服务、渲染响应/错误。零业务逻辑逸入传输层。
- **service 层是单一决策中心**：授权（`mustActor`、`canRead`、`ownedFile`）和业务规则（注释嵌套限制、dedup 逻辑）仅存在于此处。HTTP 和 gRPC 共享同一服务实现，保证行为一致。
- **存储抽象经过深思熟虑**：`FileStorage` 接口有两个生产实现（`Local`、`S3`），`Lister` 作为可选能力，通过类型断言实现——这是 Go 式组合的正确用法。

**Transactional outbox 是架构亮点**。代码库在 `internal/outbox` 中拥有完整的 outbox 模式实现，具有：
- 通过 GORM 事务对 domain 副作用进行 ACID 保证
- 指数退避 + 死信队列
- `MetricsSink` 用于可插拔观测（接口**已定义**，但尚未连接实现）
- 通知调度器（`NotificationDispatcher`）可作为 outbox handler 使用
- 可选的 Webhook 链式传输，通过 HMAC 签名处理外部交付

`MetricsSink` 接口的存在表明架构**预见到了**可观测性，只是尚未实现——这是架构债务，而非设计缺陷。

**SSE Hub 的锁分片设计正确**。`Hub` 采用每 uid 的 `sync.Mutex` 分片，而非全局锁——这意味着向 Alice 发布永远不会阻塞向 Bob 发布或 Bob 的新订阅。这是一种成熟的多租户实时设计。

**功能开关设计**。多个组件（文件分享、存储 GC、限流器、webhook 交付）通过可选配置/依赖注入开启/关闭，而非通过条件编译。这允许单一构建工件，且功能通过部署配置控制。

### 1.2 关键限制

| 维度 | 限制 | 影响 |
|---------|------------|--------|
| **分享模型** | 二元（有/无），无过期、无权限级别、无群组 | 每个分享单元只支持 1:1 授予；群聊或分级访问需要重大重构 |
| **可见性** | 全局公有/私有二元切换 | 不支持链接分享（"拥有链接的任何人都可以查看"），不支持域白名单 |
| **存储加密** | 零 | 本地存储以明文写入；S3 不设置 `ServerSideEncryption` |
| **API 演进** | 单一路由 `/v1`，单一 proto 包 | `Message` 等结构体的任何破坏性变更都会终止所有客户端 |
| **搜索** | 仅全文搜索（TSVector） | 不支持过滤器、构面、评分或跨实体联合搜索 |
| **通知** | 拉取（轮询）+ 推送（SSE） | 无移动推送网关（FCM/APNs）；无电子邮件/SMS 交付 |
| **速率限制** | 内存、单实例 | 水平扩展时分布式速率限制需要共享状态（Redis） |
| **幂等性** | 内存 GORM 存储 | 实例重启会丢失幂等性密钥状态 |

### 1.3 架构债务 / 技术债

1. **P1 — 缺少指标注册表实现**：`MetricsSink` 接口已定义（`relay.go:38-52`），`metrics.Registry` 存在，但调度器、GC 清理器、幂等性清理器没有任何计数器/直方图。这意味着无法通过仪表盘回答"队列深度是多少？"或"通知延迟是多少？"——只能通过日志。这是**结构良好但未完成**的债务。

2. **P2 — 调度器中过度的 JSON 序列化**：`NotificationDispatcher.Handle` 在两种 code path 中（push collapsed，push non-collapsed）都调用 `json.Marshal(existing)`。更糟糕的是，`notificationFor()` switch 语句为每个事件类型使用 `json.Unmarshal`。如果三条 case 都匹配，单次出站处理会产生三次 JSON 往返。这不是当前的问题（轮询间隔 5s，队列积压极少），但在高消息服务下会成为热点。

3. **P2 — 每 uid 无界内存的速率限制器**：`notifRateLimiter.keys` 是一个 `map[string][]time.Time`，会无限增长——每个不同的 `(recipient, type)` 对在活动窗口中最多存储 `rate` 个条目。虽然入口有限（不是每个用户每秒创建类型），但缺乏主动驱逐，忙碌用户可能会在速率限制器数据结构中积累数千个时间戳。

4. **P3 — `idempotency.NewGormStore` 清理器**：`RunCleaner(time.Hour)` 定期执行 `DELETE FROM idempotency_keys WHERE expires_at < now()`。如果主服务每分钟处理 1000 个写请求，此表可能包含数百万行——清理器需要对 `expires_at` 建立索引并使用批处理删除，以避免阻塞表膨胀。当前代码使用原始 GORM `Delete`，这可能逐行发出昂贵的 `WHERE expires_at < now()` 扫描。

5. **P3 — 每个处理的 reader 封装使用 'TeeReader' 实现两次**：`file.service.go` 在单个读取过程中将 `cappedReader` 与 `sha256.New()` 组合，使用 `io.TeeReader`。但 `storage.Put()` 的后端（尤其是 S3）使用 `manager.NewUploader` 在并发部分中传输——TeeReader 通过 HTTP 保持读取管道打开，这是正确的，但不容易调试。这不是 bug，而是一种非显而易见的交互，测试没有覆盖（集成测试没有检查校验和正确性）。

---

## 2. 架构扩展方向

### 方向 A：精细化分享与访问控制模型（P1）

**为什么需要**：当前的 `file_shares` 表只有 `(file_id, grantee_uid)`，没有过期、权限（读/写/管理），也没有群组。这直接限制了消息系统：群聊不能共享文件到通道、不能设置只读历史、不能授予临时访问权限。

**核心挑战**：
- **迁移策略**：现有 `file_shares` 表以 `(file_id, grantee_uid)` 为主键。添加 `expires_at`、`permissions` 位掩码和 `granted_by` 列需要 `DROP ... CASCADE` 迁移或带有版本化视图的交替模式演化。
- **群组与推导**：群组分享（"邀请 `channel-xyz` 中的所有成员"）将访问控制从简单的 `(file, grantee)` 元组变为解析问题——当用户加入群组时，他们是否继承该组过去分享的所有文件？答案决定了是仅存储直接授予，还是存储组 -> 文件映射并通过查询时解析。
- **与通知的集成**：当文件被分享时，当前系统不发送"文件已与你分享"的通知。需要一个新的 outbox 事件类型（`file.shared`）和调度器 case。

**架构变更**：
```
领域：将 Share 从 tuple 提升为 first-class aggregate
      Share {
          id          ID
          file_id     ID
          grantee     string | group_id    // polymorphic: "user:uid" or "group:id"
          permissions bitmask               // read=1, write=2, admin=4
          expires_at  *time.Time
          created_by  string
          created_at  time.Time
      }

仓库：Add  group membership resolver interface
      GroupResolver { IsMember(ctx, groupID, uid) bool }
      或推迟：初始阶段仅做 user-type grantee，添加 expires_at/permissions

迁移：
      ALTER TABLE file_shares
          ADD COLUMN id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
          ADD COLUMN permissions  INT2 NOT NULL DEFAULT 1,
          ADD COLUMN expires_at   TIMESTAMPTZ,
          ADD COLUMN granted_by   TEXT NOT NULL DEFAULT '';
      -- 然后去掉旧的复合 PK，在 (file_id, grantee_uid) 上建唯一索引
```

**对现有系统的影响**：
- `IsSharedWith` 返回 `bool` → 需要返回 `*Share` 或权限位掩码，以通过更丰富的访问决策（例如，"允许写入？"）
- 当前 `ShareFile` 接口接受 `[]string` → 需要接受 `granteeUIDs` + 可选过期/权限
- 后端兼容：旧有授予缺少 `id`、`permissions`、`expires_at` → 迁移默认值 + 现有客户端的兼容性 shim

---

### 方向 B：存储加密（P1 — 取决于部署模式）

**为什么需要**：数据驻留和合规要求（SOC2、GDPR 第 32 条）。本地模式以明文在磁盘上存储用户文件。S3 路径不请求服务器端加密。

**核心挑战**：
- **本地后端（信封加密）**：需要密钥管理——密钥存储在哪里？如何轮换？`SIGNING_KEY` 已经存在但用于 HMAC 签名 URL，不应重复用于加密。Levy 一个专用的 `ENCRYPTION_KEY`（每个实例或每个部署者？）。信封加密（`crypto/aes` + `crypto/rand` 每个文件密钥）是正确的，但增加了约 16 字节的每个对象开销（nonce）和每次读取的解密延迟。
- **S3 后端（SSE-S3 或 SSE-C）**：SSE-S3 是零代码变更——在存储桶上设置 `x-amz-server-side-encryption: AES256` 策略，新对象会自动加密。SSE-C 或 SSE-KMS 在每个 `PutObjectInput` 上需要额外的字段。由于 `S3.Put` 没有设置 `ServerSideEncryption`，添加它是改动最小的路径。
- **Presigned URL 路径**：对于客户端直接上传到 S3 的 pre-signed `PresignPut`，无法强制服务器端加密——必须由上传者发出 `x-amz-server-side-encryption` 标头。存储桶策略（`s3:PutObject` 上的 `aws:SourceArn`）可以要求它，从而无需应用层更改。

**架构变更**：
```
存储接口变化：无 — 加密是后端实现细节
仅在 S3.Put 中添加：
    ServerSideEncryption: types.ServerSideEncryptionAes256

本地后备：添加可选的 Encryptor 接口
    type Encryptor interface {
        Encrypt(ctx, plaintext io.Reader) (ciphertext io.Reader, keyID string, err error)
        Decrypt(ctx, ciphertext io.Reader, keyID string) (plaintext io.Reader, err error)
    }
    通过 NewLocal 的可选参数传递
```

**对现有系统的影响**：
- S3：零影响，除非选择 SSE-C（需要管道的密钥管理）
- Local：信封加密会增加上传/下载延迟；对于生产部署，建议将 S3 作为主要后端，其中 SSE-S3 可免费获得且无影响
- PresignUpload：仅存储桶策略变更；无需代码更改

---

### 方向 C：API 版本化与模式演化（P1）

**为什么需要**：当前的 proto 文件 `api/proto/aero_id/v1/aero_id.proto` 将所有消息类型抛入单一的 `package aero_id.v1`。虽然 gRPC 通过字段存在和未知字段保留提供了比 REST 更好的向后兼容性，但破坏性变更（类型更改、必需字段、正在退役的 RPC）会立即切断所有客户端。需要一种正式的策略。

**核心挑战**：
- **gRPC 版本化约定**：与 REST（URL 路径）不同，gRPC 版本化是通过 protobuf `package` 声明进行的：`aero_id.v2`。客户端需要更新导入路径。没有一个优雅的"在同一端点上暴露 v1 和 v2"的方式。
- **退化路径**：如果 `/v1` 路由被硬编码（目前是这样），切换到基于包的路由（`aero_id.v2.MyService`）需要在 `server.go` 中注册两个服务实现。
- **HTTP API**：当前的 Echo 路由 `e.Group("/v1", authMW...)` 需要迁移到 `/v2` 组，由 `router.go` 可选注册。

**架构变更**：
```
Proto 约定：
    package aero_id.v1;          // 冻结现有内容
    // 新建 aero_id/v2/… 目录
    package aero_id.v2;

    service AeroIDService {      // v2 仅包含 v1 的超集或重构后的 RPC
        rpc GetFile(GetFileRequest) returns (GetFileResponse);
    }

HTTP 路由：
    e.Group("/v1", mw...)  // 保持原样
    e.Group("/v2", mw...)  // 新版本，通过配置可选启用

gRPC 服务器：
    pbv1.RegisterAeroIDServiceServer(grpcSrv, v1Impl)
    pbv2.RegisterAeroIDServiceServer(grpcSrv, v2Impl)
```

**对现有系统的影响**：
- 中等迁移工作。由于代码库**尚未**改变任何 proto 消息类型，立即的风险很低。策略性方法是**在首次破坏性变更之前先有策略，再盲目前进**。
- 对于 HTTP：旧的 `/v1` 端点可以保持注册，允许客户端逐个迁移
- 对于 proto 包命名：采用 `aero_id.v2` 是最低反模式的方式

---

### 方向 D：可观测性与监控基础设施（P2）

**为什么需要**：当前状态只有日志和可选的 tracing。没有跨度/纬度指标意味着运营团队无法对以下内容创建仪表盘：
- 队列深度（outbox 延迟）
- 通知总吞吐量
- SSO 延迟百分位数
- GC 清理器效率
- ID 清理器清理延迟

`MetricsSink` 接口存在但为空——这是一个**未完成**的实现债务，不是设计缺陷。

**核心挑战**：
- **定界**：哪些指标是"必须拥有"？提议的最小集合：outbox 事件延迟（`ObserveOutboxDrain`）、gRPC/HTTP RED（速率、错误、持续时间）按方法、SSO 健康/缓存命中率、通知写入率按类型。
- **导出路径**：Prometheus 是事实上的标准。`metrics.NewRegistry()` 已经存在，并传递给了 SSO 客户端和路由 RED 指标。缺少的是开箱即用的 `/metrics` 端点（Prometheus `http.Handler`）。
- **GC 与清理器指标**：`storagegc.Sweeper.Stats` 仅通过 `log.Info()` 报告。需要为每个传递添加计数器（`objects_purged`、`orphans_deleted`、`pass_duration_seconds`）。

**架构变更**：
```
metrics.Registry 扩展：
    type Registry struct {
        counters   map[string]*prometheus.CounterVec   // 或自定义计数器
        histograms map[string]*prometheus.HistogramVec
        gauges     map[string]prometheus.Gauge
    }

添加：
    http.Handler metrics.HTTPHandler() → /metrics 端点

实现 MetricsSink：
    func (r *Registry) ObserveOutboxPublished(eventType string) {
        r.counter("outbox_published_total", "event_type").WithLabelValues(eventType).Inc()
    }

GC sweeper：添加 metric 钩子
    类似 outbox.MetricsSink 的 GC 指标接收器接口
```

**对现有系统的影响**：
- 新增依赖：`prometheus/client_golang`（设计上已经预计需要）
- HTTP handler 需要注册 `/metrics` GET 路由——使用 `router.go` 中的健康中间件
- 无 API 破坏性变更；完全是附加内容

---

### 方向 E：多租户通知交付（P2 — 路线图依赖）

**为什么需要**：当前通知系统只有 SSE（Web 实时）+ 轮询拉取。缺少移动推送（FCM/APNs）、电子邮件或 webhook 交付。对于任何想要在移动端拥有"类似原生"体验的产品，这都是一个阻碍。

**核心挑战**：
- **FCM/APNs 集成**：需要一个带有设备令牌注册（`device_tokens` 表）和专用中继（连接 FCM/APNs 的 outbox handler）的单独服务。这不是 aero-id 本身的职责——更好的位置是将通知交付卸载到**专用通知路由服务**，该服务推送 FCM/APNs/WebSocket。
- **投递语义**：目前通知被**一次性**创建（每个事件一行）。移动推送需要至少一次投递，带有投递确认回执。当前模型在创建时不跟踪投递。
- **速率限制与抑制**：`notifRateLimiter` 已就位，但仅处理每类型每分钟的计数。移动推送有其自身的速率限制约束（FCM 每个设备限制）。

**架构变更**：
```
当前：  outbox → NotificationDispatcher → notifs table → SSE push + poll

提议：  outbox → Dispatcher → notifs table + push to external relay
                                            ↓
                                    通知路由服务
                                      ↓        ↓
                                    FCM      APNs

外部接口：NotificationDispatcher 需要一个 outbox handler，该 handler 转发了通知事件
         到消息队列（NATS、Kafka）或直接到通知路由服务的 gRPC 端点
```

**对现有系统的影响**：
- 无服务内部变更。`NotificationDispatcher` 已经设计为通过 `outbox.NewChain` 支持多个 handler。
- 外部服务（通知路由器）需要作为单独项目构建——这超出了 aero-id 的范围。

---

## 3. 接口设计建议

### 3.1 当前接口评估

当前接口设计是可靠的，但有一些演进机会：

**`FileStorage` 接口**（`storage.go`）：
- ✅ 正确的抽象水平——不泄露后端细节
- ✅ `Lister` 作为可选接口（类型断言）——Go 式的正确做法
- ❌ 缺少加密参数——`Put` 没有 `encryptionKeyID` 或 `encryptionContext` 参数
- ❌ `Stat` 不返回 `StorageClass`——限制了 S3 分层存储能力

建议在 `ObjectInfo` 中添加可选的 `StorageClass` 字段，并为加密添加 `PutOption` 函数式选项模式：

```
type PutOption func(*putOptions)
func WithEncryption(keyID string) PutOption { ... }
```

**`FileShareRepository` 接口**：
- ✅ `Grant` idempotent（`ON CONFLICT DO NOTHING`）
- ❌ `Grant` 不接受到期时间或权限——需要扩展
- ❌ `IsSharedWith` 返回 `bool`——对精细权限不够丰富

建议：
```
type ShareGrant struct {
    GranteeUID  string
    Permissions SharePermission  // bitmask
    ExpiresAt   *time.Time
}

Grant(ctx, fileID, ownerUID, grantees []ShareGrant) error
// 而不是 []string
```

**`NotificationDispatcher`**：
- ✅ 通过选项模式（`WithDispatcherBlockChecker` 等）实现的可配置行为
- ❌ 当前的 `NotificationPusher` 接口将 `eventID int64` 作为参数——这是 SSE 特定细节的泄漏。应该只接受 `(uid, eventType, data string)`。

### 3.2 新的抽象层

**Sink 抽象**：当前 `MetricsSink` 存在于 `outbox` 包内。建议将其提升到更显眼的位置：

```
// internal/pkg/metrics/sink.go
type Sink interface {
    Observe(key string, value float64, labels ...string)
    Count(key string, labels ...string)
    // ...
}
```

然后 `outbox.Relay`、`storagegc.Sweeper`、`idempotency.Cleaner`、`ratelimit.Limiter` 使用同一 `Sink` 接口，避免从这些包直接导入 prometheus。

**Quota Enforcer 接口**：当前速率限制器是单实例的，按 actor 使用内存映射。对于多实例部署，需要分布式配额：

```
type QuotaEnforcer interface {
    Allow(ctx, key string, cost int) (bool, error)
}
```

初始实现：`memoryQuotaEnforcer`（当前行为）。后来的实现：`redisQuotaEnforcer`（使用滑动窗口）。服务层在不了解底层实现的情况下使用此接口。

### 3.3 向后兼容性策略

对于精细分享等破坏性仓库接口变更：

1. **双写入期**：向仓库添加新方法（`GrantV2`、`IsSharedWithV2`），同时保留旧签名。服务层在新代码路径使用 `v2`，并提供检测器以记录调用的中间状态。
2. **默认值 shim**：当缺少过期时间时，`Get` 返回 `expires_at = NULL`（意味着"永不过期"）——与当前无过期行为向后兼容。
3. **Proto 字段编号**：`api/proto/aero_id/v1/aero_id.proto` 应该**从不**重复使用或更改已发布的字段编号。新字段应按照 `protobuf` 约定使用 reserved 字段。

---

## 4. 技术选型

### 4.1 依赖评估标准

| 标准 | 权重 | 说明 |
|----------|--------|-------------|
| **许可** | 必须 | 仅限 MIT / Apache 2.0 / BSD；无 AGPL/SSPL |
| **Go 生态兼容性** | 高 | 从 `go.mod` 已经可见——在 `go 1.26` 构建；模块路径匹配 |
| **API 稳定性** | 高 | 无 v0 或实验性语义版本（除非显式 opt-in） |
| **传递依赖预算** | 中 | 避免添加大量 C 或 Rust FFI 的模块 |
| **内存/延迟影响** | 中 | 服务是针对 50-100ms 延迟设计的；后台组件（GC、outbox）较少受影响 |

### 4.2 推荐的第三方模块

| 目的 | 模块 | 理由 |
|---------|----------|---------|
| **Prometheus 指标** | `prometheus/client_golang v1.x` | 事实标准；`metrics.Registry` 已经假定它存在 |
| **分布式速率限制** | `redis/go-redis/v9` | 免死锁的滑动窗口实现；`go.mod` 过去有它（被移除），表明已考虑 |
| **密钥管理** | `hashicorp/vault/api` | 信封加密的密钥轮换；L1 依赖项——仅在配置了 vault 时加载 |

**Redis 需要仔细考虑。** aero-id 当前是**无外部运行时依赖**的——唯一的存储后端是 Postgres。增加 Redis：
- 设置复杂度（集群、哨兵、网络）
- 对于"单实例开发"路径，`go redis` 客户端优雅地退回到 `nil`/`ErrNil`（通过检查错误可处理）
- 选型规则：**默认依赖 Postgres**；Redis 始终是可选的，通过配置启用

### 4.3 自建 vs 采购决策

| 能力 | 路径 | 理由 |
|----------|------|---------|
| **移动推送通知** | 采购外部（Firebase/APNs）的单独通知路由服务 | 负责 OAuth 握手和设备管理等；不在 aero-id 关注范围内 |
| **加密密钥轮换** | 自建（Postgres 中的本地信封加密）+ 可选的 Vault 集成 | 对于 single-tenant 部署，信封加密很简单；对于 multi-tenant 中心化密钥管理，Vault 更好 |
| **分布式速率限制** | 采购（Redis + Sliding Window） | Redis SETEX + sorted set 是已知的模式；不需要自定义算法 |
| **全文搜索** | 维持 Postgres TSVector（自建） | 对于当前规模的部署来说已经足够；如果 >1M 实体需要搜索，再到 Elasticsearch |

---

## 5. 实施路线图

### 5.1 阶段划分

```
P0 ── 必须为生产环境解决
  ├─ 加密（S3 SSE-S3 → 存储桶策略；Local 信封加密）
  ├─ 分享过期 + 权限扩展
  └─ MetricsSink 实现（可观测性的 80/20 收益）

P1 ── 短期提升（首个主力版本前）
  ├─ API 版本化策略（冻结 v1 proto，定义 v2 迁移路径）
  ├─ GC 清理器可观测性（计数器 + 直方图）
  └─ 幂等性清理器批处理删除 + 指标

P2 ── 中期发布后
  ├─ Notif 速率限制器内存驱逐守卫
  ├─ 通知调度器 JSON 序列化热点优化
  └─ 按域拆分 Proto（每个实体包一个文件，统一版本化）

P3 ── 基于需求
  ├─ 分布式速率限制（Redis 后端）
  ├─ 群组分享（GroupResolver 接口）
  └─ 移动推送网关集成（外部服务）
```

### 5.2 里程碑时间表

| 里程碑 | 范围 | 依赖 | 可交付成果 |
|-----------|-------|--------------|----------------|
| **M1 — 基础加固** | 加密 + 指标实现 | 无 | `s3.go` 中的 AES256；Local 信封加密键槽；`/metrics` HTTP 端点；带有仪表盘的 Grafana json |
| **M2 — 分享演进** | 精细分享 | M1 | `file_shares` 迁移；带过期/权限的新仓库接口；`ListSharedWithMe` 更新；审计事件 |
| **M3 — API 未来准备** | 版本化策略 | 无 | v1 proto 冻结；`aero_id.v2` 包定义；`/v2` HTTP 路由可选启用；部署指南 |
| **M4 — 运营成熟度** | 清理器 + 调度器优化 | M1 | 幂等性清理器批处理删除；通知调度器 JSON 速度优化；速率限制器最大内存边界 |
| **M5 — 路由图** | 群组分享 | M2 | `GroupResolver` 接口；仓库实现；通过 `WithShareGroupResolver` 功能开关 |

### 5.3 风险与缓解

| 风险 | 可能性 | 影响 | 缓解 |
|------|------|--------|----------|
| **本地加密密钥泄露** | 低 | 高 | 将 `ENCRYPTION_KEY` 从 `SIGNING_KEY` 分离；最少权限 IAM；无密钥的 HMAC + AES |
| **分享迁移意外删除现有授权** | 中 | 高 | 迁移更改默认使用 `ALTER TABLE … ADD COLUMN IF NOT EXISTS`；暂存环境验证；按 100k 行分批迁移 |
| **`/metrics` 端点 DoS** | 低 | 中 | 限制到 127.0.0.1 或带上请求量不大的 `Cache-Control`；通过 Prometheus 拉取，而非推送 |
| **Redi** 哨兵运营复杂度 | 中 | 中 | 默认维持内存 + Postgres；Redis 通过配置 opt-in；带有 `ErrNil` 守护的 `go-redis` 优雅降级 |
| **v2 proto 包与现有客户端冲突** | 高（如变更） | 中 | 在 v2 准备好之前不部署 v2；在 proto 目录中 retain `aero_id/v1/`；迁移文档 |

### 5.4 决策权衡

**选项 1：先加固，后功能**（推荐）
```
M1 → M2 → M3 → M4 → M5
```
优点：在添加功能之前先解决合规和可观测性差距；每个里程碑交付生产级质量。
缺点：对于需要群组分享能力的应用程序团队来说，速度较慢。

**选项 2：并行赛道**
```
Track A：加密 + 指标（M1）
Track B：分享演进（M2 + M5）
```
在不同开发者之间并行。要求轨 A 的开发者熟悉 S3 和 Prometheus 客户端，轨 B 的开发者熟悉 GORM 迁移和 ACL 设计。
优点：更快交付关键功能。
缺点：如果 `FileShareRepository` 接口在 M1 之前发生变化（可能性不大但可能发生），则存在集成风险。

**选项 3：最小可行安全（MVP 生产就绪）**
```
仅加密（S3 SSE-S3）+ `/metrics` 端点
→ 部署到生产
→ 在指标告知队列深度成为问题时处理分享
```
优点：第 0 天生产就绪；零应用代码变更（仅存储桶策略）。
缺点：忽略在分享和可观测性方面已经存在的设计差距。

---

## 总结

aero-id 是一个高质量、组织良好的代码库，具有扎实的架构基础。`outbox` 实现、`hub` 锁分片和 `MetricsSink` 接口证明该团队之前就考虑了可扩展性和可观测性——尽管后者仍未实现。审查识别出的五个方向中，**存储加密**（尤其是通过存储桶策略的 SSE-S3）提供了最高的影响/成本比，其次是 **`MetricsSink` 连接**，它将在不需要理解 Go 源代码的情况下实现运营可见性。

该代码库的架构债务主要在于**未连接的接口**（`MetricsSink`）和**数据模型限制**（二元分享、无过期），而非结构性反模式。路线图从 M1（加固）推进到 M5（群组分享），确保每个层级的运营弹性和合规性在通往新功能的道路上得到确立。
