现在我已完整理解输入文档及底层代码库。以下是全面的架构分析。

---

# aero-id 架构分析与扩展建议

> 基于代码库全量扫描和现有分析文档的架构视角评估

---

## 一、架构评估

### 1.1 当前架构的优势

aero-id 的架构质量整体很高，尤其在以下几个方面表现出色：

| 维度 | 现状 | 评估 |
|------|------|------|
| **分层清晰度** | handler → service → repository → DB 的单向依赖严格执行 | ✅ 优秀 |
| **跨传输对等** | HTTP (Gin) 与 gRPC 的 41+ RPC 完全对应，行为一致 | ✅ 已超前实现 |
| **可靠模式采用** | 断路器、回退/重试、stale-cache、singleflight、thread-safe outbox | ✅ 达到成熟度级别 |
| **可观测性** | OTel 追踪、RED 指标、per-dependency readiness 探针 | ✅ 生产就绪 |
| **限流与治理** | 双传输限流、Block 方向性屏蔽、RBAC 角色体系 | ✅ |
| **存储抽象** | `FileStorage` 接口统一 local/S3，可互换 | ✅ |

### 1.2 核心架构局限性

当前架构并非有根本性缺陷，而是在**从单体成熟服务向分布式平台演进**的过程中，存在若干结构性断层：

#### 局限性一：per-instance 隐式假设渗透关键路径

架构中有 5 个组件假设只有单实例运行，这在水平扩容时构成薄弱点：

```
Rate Limiter (内存 token bucket) → 多实例 = 限流阈值 × N
Idempotency Key (GORM 表) → 跨实例无去重保障
SSO 缓存 (内存) → 每个实例冷启动
Notification Collapse (内存窗口) → 多实例各自折叠，用户收重复通知
SSE Hub (内存 subscriber map) → 多实例 = 用户连接分散，推送收不到
```

**本质问题**：没有明确的"共享状态层"抽象。当前设计将"无状态"等同于"所有状态在 DB 中"，但限流/缓存/去重/实时推送这四类状态**不应也不适合全在 DB 中**操作。

#### 局限性二：实时推送层缺乏交付契约

SSE Hub 与 NotificationDispatcher 之间存在**不透明的可靠性缝隙**：

```
  NotificationDispatcher → Hub.Publish() → channel (buffer=16) → SSE client
                                  ↓
                          静默丢弃（无指标）
```

Dispatcher 认为事件已成功推送，Hub 可能在丢事件，但两边都不知情。`Handle()` 返回 `nil` 后，outbox 标记为已发布——事件**永远不会被重新投递**。这是一个**可靠性黑洞**。

#### 局限性三：New Service Layer 存在回归风险

代码中并存两套服务实现：
- `internal/service/`（旧，生产 active wired）
- `internal/file/`, `internal/message/` 等（新，部分逻辑更优，但 Delete 路径有 TOCTOU 回归）

`internal/file/service_crud.go` 的 `Delete()` 跳过了 `StorageRefRepository.Decrement` 原子引用计数，直接回退到裸 `CountByStorageKey`。这暴露了一个**并发安全的回归**——旧层正确处理了，新层没跟上。

**架构债务**：当两套实现同时存在且行为有差异时，维护者需要两套心智模型。重构应该是一次性切换，而非长期共存。

#### 局限性四：数据生命周期管理整体缺失

查询了全部实体模型，只有 `storage GC` 有明确的回收机制。其他实体的数据行都是"永生"的：

| 实体 | 有无 TTL | 有无归档 | 有无级联删除 | 表规模增长 |
|------|----------|----------|-------------|-----------|
| notifications | ❌ | ❌ | ❌ | 永久增长 |
| outbox_events | ❌ | ❌ | ✅ (MarkPublished) | 受消费速度限制 |
| audit_log | ❌ | ❌ | ❌ | 永久增长 |
| file storage refs | ❌ | ✅ (GC sweep) | ✅ | 受 GC 控制 |
| messages | ❌ | ❌ | ❌ | 软删除不释放空间 |

### 1.3 关键技术债务

1. **Dual service layer**：`internal/service/` 与 `internal/file/` 等新包共存，业务逻辑在两处实现，测试覆盖率不同
2. **Admin API 未完成**：RBAC 模型 100% 实现，API 暴露 0%（仅 `ListAudit`）
3. **per-instance 限制明确标注但不解决**：`config.go:119` 注释标注了分布式限流缺失，但未建立修复计划
4. **测试覆盖不完整**：SSE Hub 的丢事件路径零测试覆盖

---

## 二、扩展方向分析（5 个高价值方向）

### 方向一：引入共享状态层（Redis 抽象层）

**为什么需要**

这是**横向扩容的基础设施前提**。没有共享状态层，以下四个功能无法在多实例上正确工作：

| 功能 | 单实例问题 | 多实例后果 |
|------|-----------|-----------|
| 速率限制 | 每个实例独立计数 | 限流阈值 × N，形同虚设 |
| 幂等键 | 跨实例不可见 | 蓝绿部署期间重复写入 |
| 通知折叠 | 每个实例独立窗口 | 用户收到 N 份折叠通知 |
| SSO 缓存 | 各自冷启动 | 部署后前 N 秒高延迟 |

**核心挑战**

- **渐进式引入**：不能要求 Redis 成为硬依赖——在开发/测试环境不引入额外基础设施
- **降级路径**：Redis 宕机时系统不应崩溃，应降级到 in-memory 模式并记录告警
- **一致性模型**：缓存不需要强一致性，但幂等键需要——两者在统一抽象上需要不同保证级别

**架构变更**

```
当前：
  in-memory Limiter / Cache / Idempotency Store

变更后：
  ┌──────────────────────────────────────────────┐
  │              aero-id instance                 │
  │  ┌─────────┐  ┌──────────┐  ┌─────────────┐ │
  │  │ Limiter │  │ Idempot  │  │ SSO Cache   │ │
  │  │ (无状态) │  │ (无状态)  │  │ (L1 小缓存)  │ │
  │  └────┬────┘  └────┬─────┘  └──────┬──────┘ │
  ├───────┴────────────┴────────────────┴────────┤
  │          Redis (共享状态层 / source of truth)  │
  └──────────────────────────────────────────────┘
```

**关键接口设计原则**：将 `Limiter`、`IdempotencyStore`、`TokenCache`、`UserCache` 提取为可切换实现的接口，已有的 in-memory 实现作为 `default`，新的 Redis 实现作为 `withRedis` 选项。不改变现有 API 签名。

**对现有系统的影响**

- **无影响**：这是一个底层基础设施替换，不涉及 handler/service 代码变更
- 只需在 `internal/app/app.go` 的 composition root 中做条件 wiring

---

### 方向二：实时推送可靠性工程（Reliable SSE 分层）

**为什么需要**

当前 SSE Hub 的 channel buffer 硬编码为 16、非阻塞 send 静默丢事件、无 per-uid 上限。这对用户感知的终端体验有直接影响：

- 用户在线期间丢失通知，不刷新页面不会发现
- 大量通知涌入时（如群聊 @all），旧通知被新通知冲掉
- 恶意客户端可创建数百 SSE 连接消耗服务端内存

**核心挑战**

- **背压 vs 实时性**：完全可靠会使系统退化为轮询，失去 SSE 的价值。需要在丢事件指标化 + 重连回放之间取得平衡
- **慢消费者处理**：不能因单个慢客户端阻塞其他客户端的推送
- **跨实例推送**：如果采用 Redis Pub/Sub，用户连接在实例 A，事件发到实例 B，需要跨实例转发（或者固定用户到实例，但这破坏无状态）

**架构变更**

```
当前：
  Hub (内存) ─→ chan SSEEvent (buffer=16) ─→ HTTP Response

变更后：
  Hub ─→ chan SSEEvent (可配置 buffer=128)
   ├──→ metrics: dropped_events_total / subscriber_count
   ├──→ per-uid上限检查 (max_subscribers=10)
   └──→ 慢消费者检测 (5s未消费→主动disconnect)
         ↓
  Dispatcher 接收 Publish 返回值: success/fail
    fail时 → 记录到 outbox 不标记 published
            → 客户端重连时通过 replay 补充
```

**对现有系统的影响**

- 仅影响 `internal/hub/hub.go` 和 `internal/service/notification.go`
- 需要新增 metric 注册和配置读取
- 需要修改 `Publish()` 签名返回是否成功

**方案选择**

| 方案 | 复杂度 | 可靠性提升 | 跨实例支持 | 推荐 |
|------|--------|-----------|-----------|------|
| A: 仅增加 buffer + 指标 + 上限 | 低 | 中 | ❌ | 立即做 |
| B: A + Dispatcher 感知丢事件 | 中 | 高 | ❌ | P1 |
| C: B + Redis Pub/Sub 跨实例转发 | 高 | 高 | ✅ | P2 |

---

### 方向三：通知生命周期管理（Notification TTL + 归档 + 孤儿清理）

**为什么需要**

通知是典型的时间序列数据——7 天后的已读通知几乎无访问价值。当前无 TTL、无清理、无批量归档：

- 表行数随时间线性增长 → `ListByRecipient` 查询性能衰减（即使有 `created_at DESC` 索引）
- 被引用资源删除后通知成为孤儿 → 用户点击得到 404
- 无批量删除 API → 用户无法管理自己的通知存储
- 不符合数据最小化原则（GDPR 合规风险）

**核心挑战**

- **软删除 vs 硬删除**：通知本身可能涉及举报/审核的法律证据保留要求，硬删除不可取
- **级联删除的性能**：删除消息时查询所有关联通知并标记的成本
- **用户预期**：用户可能希望保留某些通知（收藏、待办），全量 TTL 可能不可接受

**架构变更**

```sql
-- 新增列
ALTER TABLE notifications ADD COLUMN subject_deleted bool NOT NULL DEFAULT false;
ALTER TABLE notifications ADD COLUMN archived_at timestamptz;

-- 新增 TTL 策略（应用层实现，非 DB trigger）
-- 通知行在 created_at + 90 天后可被后台任务标记为 archived
-- archived 行在 +30 天后可被硬删除
```

```
新增后台任务：
  NotificationGC (类似 storage/gc/sweeper.go)
    ├── ArchiveReadOlderThan(ctx, 90 days)  → 设置 archived_at
    ├── PurgeArchivedOlderThan(ctx, 30 days) → 硬删除
    └── MarkOrphanSubjects(ctx)              → 查询已删除资源的通知，设置 subject_deleted=true
```

**对现有系统的影响**

- 需要新增 migration（列 + index）
- 需要在 `internal/repository/notification.go` 新增 GC 方法
- 需要在 `internal/app/app.go` 注册后台 GC 任务
- 需要在 HTTP 层新增 `DELETE /v1/notifications` 端点（批量删除/归档）

---

### 方向四：数据完整性加固 —— 文件 TOCTOU + Presigned Checksum + 附件生命周期

**为什么需要**

这是三个数据完整性问题的合并方向：

1. **TOCTOU**（P0）：新服务层的 `file.Delete` 跳过原子引用计数，存在竞态条件下误删 blob
2. **Presigned Checksum**（P0）：S3 presigned upload 路径完全不做内容完整性验证
3. **附件悬挂引用**（P1）：消息的 `file_id` 可被独立删除，接收方得到 404

**核心挑战**

- TOCTOU 的修复本质是**使用数据库锁或原子操作**——`StorageRefRepository.Decrement` 已经实现，只需将新层对齐旧层
- Checksum 验证的关键问题是**大文件的 S3 ETag 不等于 MD5**（multipart upload 时 ETag 是每个分片 MD5 拼接后再 MD5，加 `-N` 后缀），需要客户端额外提供 SHA-256
- 附件生命周期涉及**快照语义**——文件删除不应影响已发送消息的附件展示

**架构变更**

```
TOCTOU 修复（最小变更）：
  file/service_crud.go Delete() 
    当前: CountByStorageKey → store.Delete()
    修复: refs.Decrement() → 仅标记 → GC 延迟清理

Checksum 验证（API 变更）：
  CompleteUploadRequest 新增 optional 字段 expected_sha256
    服务端: HEAD S3 object → 获取 ETag → 与 expected_sha256 交叉验证
    或: 异步 worker 下载 bytes → 计算 SHA-256 → 对比 → 不一致则删除 blob + 标记错误

附件生命周期（Schema 变更）：
  新增 message_attachments 表
    message_id, file_id, original_name, mime_type, size, storage_key (快照)
  当文件被删除时：message_attachments 保留，展示 "附件已不可用" 占位
```

**对现有系统的影响**

- TOCTOU：`internal/file/service_crud.go` 对齐 `internal/service/file.go` 的 `refs.Decrement` 逻辑
- Checksum：`CompleteUpload` handler + S3 HEAD 操作（需 S3 `GetObject` 权限）
- Attachments：新表 + migration + `SendMessage` 逻辑中填充 `message_attachments`

---

### 方向五：API 表面完整性 —— Admin RBAC + Webhook 管理面

**为什么需要**

两个明显的"半成品"API：

```
Admin RBAC:
  代码完成度:   domain 层 ✅  repository 层 ✅  service 层 ❌  handler 层 ❌
  唯一暴露:     ListAudit (HTTP only)
  未暴露:      GrantRole / RevokeRole / ListRoles / UserRoles

Webhook 管理面:
  代码完成度:   outbox 事件 ✅  WebhookHandler ✅  订阅管理 ❌  投递记录查询 ❌
  唯一暴露:     配置化的目标 URL
  未暴露:      订阅 CRUD / 密钥轮换 / 投递历史 / 仪表板
```

**核心挑战**

- Admin RBAC 本质是**补全已有代码的 API 层**——domain 和 repository 已经完备，service 层需要增加 `AdminService`，handler 层新增路由
- Webhook 管理面要求将**配置驱动**转变为**数据库驱动**的订阅模型——需要对 outbox relay 做较大改造

**架构变更**

```
Admin RBAC (低风险，可独立完成):
  internal/service/ 新增 AdminService
    GrantRole(ctx, granterUID, targetUID, roleCode) → 调用 adminRepo.GrantRole
    RevokeRole(...) → adminRepo.RevokeRole
    ListRoles() → domain.Role 枚举（代码已有）
    ListUserRoles(ctx, uid) → adminRepo.ListUserRoles

  internal/handler/http/admin.go
    新增 POST /v1/admin/roles/grant
    新增 POST /v1/admin/roles/revoke
    新增 GET  /v1/admin/roles
    新增 GET  /v1/admin/users/:uid/roles
    迁移 ListAudit 到 RequirePermission 中间件

  api/proto/ 新增 AdminService

Webhook 管理面 (中等风险):
  新增 webhook_subscriptions 表
  新增 WebhookManager service
  新增 Subscription CRUD API
  新增投递状态查询 API
  新增 webhook 级别断路器 (复用 sso/breaker.go 模式)
```

---

## 三、接口设计建议

### 3.1 关键抽象层引入

**建议一的接口模式：可切换的共享状态后端**

```go
// 当前隐式依赖 in-memory 实现
type Limiter interface {
    Allow(ctx context.Context, key string) (bool, time.Duration, error)
}

// 需要两个实现 + 一个可选的中间件抽象
type LimiterBackend interface {
    Allow(ctx context.Context, key string, limit RateLimit) (bool, time.Duration, error)
    Close() error
}

// memoryLimiter - 当前实现，无外部依赖
// redisLimiter - 新实现，sliding window / GCRA
// compositeLimiter - memory + redis 双写，降级用
```

**原则**：不要改动 `Limiter` 接口签名，在 composition root 做注入切换。

### 3.2 SSE 可靠性接口设计

当前 `Hub.Publish()` 返回 `void`，这是问题的根源。建议改为：

```go
// 当前
func (h *Hub) Publish(uid string, eventType string, data string, ts int64)

// 建议
type PublishResult struct {
    Delivered bool      // 至少一个 subscriber 接收到（任意 channel 非阻塞 send 成功）
    Dropped   int64     // 本次推送中被丢弃的事件数
    SubCount  int       // 当前的 subscriber 数量
}

func (h *Hub) Publish(uid string, evt SSEEvent) PublishResult
```

这样 Dispatcher 可以：
- 如果 `Delivered == false && SubCount > 0` → 认为有异常，记录告警
- 如果 `SubCount == 0` → 用户离线，事件已写入通知行，下次 replay 补充

### 3.3 向后兼容性策略

| 变更类型 | 策略 |
|----------|------|
| 新增 API 端点 | 前缀 `/v1/`，与现有端点平级 |
| 现有端点签名字段 | 新增字段用 `optional` + `omitempty`，旧客户端不传则维持旧行为 |
| proto schema 变更 | 新增字段用 `optional`，不重编号既有字段 |
| 数据库迁移 | 只 `ADD COLUMN` 或 `CREATE TABLE`，不改既有列类型 |
| 配置变更 | 新增配置项设默认值，不修改已存在的配置字段名 |

---

## 四、技术选型建议

### 4.1 Redis 引入评估

| 维度 | 评估 |
|------|------|
| **必要性** | ✅ 共享状态层几乎没有替代方案。Etcd 太重，MySQL 不适合高速率限制 |
| **稳定性** | ✅ Redis 6+ 在生产环境已被广泛验证 |
| **操作复杂度** | ✅ 运维成本极低（无状态、可主从、可哨兵） |
| **开发复杂度** | ✅ Go 生态有 `go-redis` 成熟客户端 |
| **硬依赖风险** | ✅ 通过降级到 in-memory 消除 |

**决策**：**引入 Redis，但不作为硬依赖**。所有使用 Redis 的功能必须提供 in-memory fallback。

### 4.2 是否需要消息队列？

分析 outbox 当前的做法：relay 轮询 outbox_events 表，处理成功后 `MarkPublished`。当前 QPS 下这是合理的，但以下三个信号表明未来可能需要消息队列：

1. **文件后处理管线**：缩略图生成、转码等异步任务需要独立的 worker pool
2. **Webhook 管理面**：多订阅者的 fan-out 场景
3. **跨实例 outbox 协调**：多实例轮询同一张表需要 `FOR UPDATE SKIP LOCKED`

**建议**：现阶段保留 outbox relay 的 DB 轮询模式，但在**文件后处理管线**实现时引入 **in-process worker pool**（`internal/pipeline/`），不急于引入独立的消息队列。当 worker 数量 > 3 个且需要独立扩展时，再评估 NATS 或 RabbitMQ。

### 4.3 缩略图/预览方案评估

| 方案 | 优点 | 缺点 | 适用场景 |
|------|------|------|---------|
| **libvips** (Go binding: `bimg`) | 极快、内存效率高、支持格式多 | C 依赖，构建复杂 | 图片缩略图 |
| **ffmpeg** | 视频帧提取 | 二进制依赖，需要 exec | 视频预览 |
| **External API** (Cloudinary/Imgix) | 零运维、功能全 | 外部依赖、成本、延迟 | 产品初期不推荐 |
| **Async Worker + libvips** | 不阻塞上传路径 | 架构复杂度增加 | ✅ 推荐 |

**决策**：使用 `bimg` (libvips) 生成缩略图，通过 outbox relay 驱动异步管线。不上传独立图片处理服务。

### 4.4 自建 vs 使用外部服务

| 功能 | 方案 | 决策 |
|------|------|------|
| 共享状态层 (Redis) | 自建：部署 Redis 主从 | ✅ 自建，运维成本低 |
| 图片处理 | 自建：libvips + outbox pipeline | ✅ 自建，依赖简单 |
| Push Notification | 自建：FCM/APNs 集成 | ✅ 无第三方依赖 |
| 垃圾内容检测 | 初期：关键词黑名单 | ✅ 渐进式，不引入 ML |
| 图片鉴黄/NSFW 检测 | 外部 API（如 AWS Rekognition） | ❌ P3，产品上线后再评估 |

---

## 五、实施路线图

### 阶段划分

```
Phase 0: 数据完整性修复 (P0)  → 1-2 天
Phase 1: API 表面补全 (P0-P1) → 2-3 天
Phase 2: 实时推送可靠性 (P1)  → 3-5 天
Phase 3: 共享状态层 (P1)      → 5-7 天
Phase 4: 通知生命周期 (P2)     → 2-3 天
Phase 5: 高阶扩展 (P2)        → 2-4 周
```

### Phase 0 — 数据完整性修复（P0，高优先级）

| 任务 | 影响范围 | 风险 |
|------|---------|------|
| 0a. 修复新服务层 TOCTOU：将 `file/service_crud.go` 的 Delete 路径对齐旧层，使用 `refs.Decrement` 原子操作 | `internal/file/service_crud.go` | 低 — 旧层已有正确实现可参考 |
| 0b. Presigned upload checksum 验证：`CompleteUpload` 接收 `expected_sha256`，HEAD S3 object 验证 | `internal/file/handler.go`, `internal/file/service.go` | 中 — S3 HEAD 操作需 IAM 权限 |
| 0c. 增加 SSE Hub 丢事件计数器 | `internal/hub/hub.go` | 低 |

**里程碑标志**：`make test && make lint` 通过，`make test-integration` 通过。

### Phase 1 — API 表面补全（P0-P1）

| 任务 | 影响范围 | 风险 |
|------|---------|------|
| 1a. Admin RBAC API：新增 `AdminService` service + HTTP/gRPC 端点 | `internal/service/admin.go`, `internal/handler/{http,grpc}/admin.go` | 低 — domain + repository 已就绪 |
| 1b. 将 AdminHandler 接入 `RequirePermission` 中间件 | `internal/handler/http/admin.go`, `internal/middleware/permission.go` | 低 |
| 1c. Webhook 管理面：`webhook_subscriptions` 表 + CRUD API | `internal/outbox/` + `internal/service/`, handler | 中 — 需要将 webhook handler 改为数据驱动 |
| 1d. Webhook 断路器：复用 `sso/breaker.go` 实现 | `internal/outbox/webhook.go` | 中 |

**里程碑标志**：管理员可通过 API 授予/撤销角色；Webhook 投递有断路器保护。

### Phase 2 — 实时推送可靠性工程（P1）

| 任务 | 影响范围 | 风险 |
|------|---------|------|
| 2a. Hub 配置化：`max_subscribers_per_uid`, `channel_buffer` | `internal/hub/hub.go` + `configs/` | 低 |
| 2b. 丢事件指标 + Prometheus 暴露 | `internal/hub/hub.go` | 低 |
| 2c. Dispatcher 感知丢事件：`Publish()` 返回 `PublishResult` | `internal/hub/hub.go`, `internal/service/notification.go` | 中 — 接口变更需协调 |
| 2d. 慢消费者主动断开 | `internal/hub/hub.go` | 低 |

**里程碑标志**：SSE 丢事件有完整指标暴露；Dispatcher 知道推送是否成功。

### Phase 3 — 共享状态层（P1）

| 任务 | 影响范围 | 风险 |
|------|---------|------|
| 3a. 引入 `go-redis` 依赖 + Redis 连接池配置 | `go.mod`, `configs/` | 低 |
| 3b. IdempotencyStore Redis 实现（`SET NX EX TTL`）+ in-memory fallback | `internal/idempotency/` | 低 |
| 3c. Rate Limiter Redis 后端（sliding window / GCRA） | `internal/middleware/ratelimit/` | 中 — 算法选择需 benchmark |
| 3d. SSO 缓存多级重构：L1=local, L2=Redis | `internal/sso/token_cache.go`, `user_cache.go` | 中 |
| 3e. Outbox relay 跨实例协调：`FOR UPDATE SKIP LOCKED` 或 leader election | `internal/outbox/relay.go` | 高 — 需要确保不丢事件 |

**里程碑标志**：双实例部署时，限流/幂等/缓存/outbox 正确工作。

### Phase 4 — 通知生命周期 + Webhook 断路器（P2）

| 任务 | 影响范围 | 风险 |
|------|---------|------|
| 4a. Migration：`notifications` 表新增 `subject_deleted`, `archived_at` | `migrations/` | 低 |
| 4b. NotificationGC 后台任务 | `internal/service/gc/` | 低 |
| 4c. `DELETE /v1/notifications` 端点和批量操作 | handler + service | 低 |
| 4d. 消息/评论删除时级联标记通知 | `internal/message/service.go`, `internal/comment/service.go` | 低 |

**里程碑标志**：通知表不再永久增长；孤儿通知有标记可查。

### Phase 5 — 高阶产品扩展（P2）

| 任务 | 影响范围 | 风险 |
|------|---------|------|
| 5a. 对话抽象（Conversation 实体）+ 群组消息 Schema | 跨 service/schema | 高 — 架构影响最大 |
| 5b. 文件后处理管线（缩略图 + 预览） | `internal/pipeline/` + `internal/file/` | 中 |
| 5c. 多设备同步 + 设备注册 | `internal/service/device.go` + push worker | 中 |
| 5d. 附件生命周期管理（快照语义） | `internal/message/service.go` | 中 |

**里程碑标志**：可创建群组、可发送群消息、消息附件不被文件删除破坏。

### 风险矩阵

| 风险 | 概率 | 影响 | 缓解策略 |
|------|------|------|---------|
| Phase 3e (outbox 跨实例协调) 引入事件丢失 | 中 | 高 | 先实现 `FOR UPDATE SKIP LOCKED` + 集成测试验证后，再部署到生产 |
| Phase 5a (群组消息) 架构变更过大导致回归 | 中 | 高 | 群组消息作为独立新实体切入，不改动既有 1:1 消息表；通过 `conversation_id nullable` 做向后兼容 |
| Redis 宕机导致服务不可用 | 低 | 低 | 所有 Redis 功能有 in-memory fallback，Redis 宕机=降级而非宕机 |
| 新服务层（`internal/file/`）被 wired 后 TOCTOU 复现 | 中 | 高 | Phase 0a 修复后，在 file service 的 Delete 路径加集成测试覆盖并发场景 |
| 通知 TTL 误删法律相关的通知 | 低 | 中 | TTL 默认 90 天可配置；被举报/审核中的通知跳过 TTL |

---

## 总结

aero-id 的代码库成熟度非常高，已交付的功能在生产级质量之上。核心架构问题是**从单实例向分布式演进的过程中的未完成过渡**，而非根本性设计缺陷。

五个扩展方向按优先级排序：

| # | 方向 | 性质 | 风险 | 时间估计 |
|---|------|------|------|---------|
| P0 | **数据完整性修复**（TOCTOU + Checksum） | Bug 修复 | 低 | 1-2 天 |
| P0 | **API 表面补全**（Admin RBAC + Webhook 管理面） | 功能缺失 | 低-中 | 2-3 天 |
| P1 | **SSE 可靠性工程** | 可靠性 | 中 | 3-5 天 |
| P1 | **共享状态层（Redis）** | 横向扩容 | 中-高 | 5-7 天 |
| P2 | **通知生命周期** + **高阶扩展** | 平台化 | 中-高 | 2-4 周 |

**最紧迫的行动项**：修复新服务层 `file/service_crud.go` 的 TOCTOU 竞态条件——数据完整性不应等待。
