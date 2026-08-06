# aero-id — 全局代码扫描：资深架构师视角的 5 个高价值扩展方向

> **日期**: 2026-07-10  
> **方法**: 全量代码扫描（`internal/` 全部 ~40 个包、22 份迁移文件、`cmd/`、`configs/`、`api/proto/`），
>   逐层阅读 handler → service → repository → domain → 基础设施代码，追溯数据流与错误路径  
> **核心原则**: 每个方向必须有代码级证据（具体文件名 + 行号 + 关键逻辑），拒绝产品 Wishlist  
> **无代码变更**

---

## 代码库健康度摘要（基于实际扫描）

| 维度 | 状态 |
|------|------|
| 分层架构 | 完整 — handler → service → repository → GORM，严格单向依赖 |
| 双传输层 | HTTP + gRPC 完全对等，parity 测试验证通过 |
| 错误处理 | 全部通过 `AppError` 体系包装，DB 原始错误不泄漏 |
| 事务 | 评论创建 + 回复计数更新使用事务性 outbox |
| 幂等性 | 所有写路径支持 idempotency key |
| 限流 | 内存 token-bucket，per-actor/per-IP |
| SSO 韧性 | 断路器 + stale-cache + singleflight + 重试 + 证书固定 |
| 文件管道 | 流式上传 + 内容去重 + MIME 校验 + 大小限制 + 预签名直传 + GC 扫表 |
| 通知 | Outbox 驱动 + 偏好设置 + 静默时段 + 去重折叠 + SSE 实时推送 |
| 搜索 | PostgreSQL FTS + ILIKE 回退，跨三实体 |
| 审计 | 敏感操作全覆盖（分享/举报/封禁/删除） |
| 可观测性 | RED 指标 + OTel 追踪 + 每依赖健康检查 + SSO 指标 |
| 测试 | 单元测试 + 集成测试（testcontainers），无失败 |
| 管理面 | 角色权限 + Grant/Revoke + 审计日志查询 |

**结论**: 这是一个极高质量的代码库，远超市面常见的 MVP 状态。以下扩展方向不是"修复缺陷"，而是**从产品成熟度和运营规模角度识别的高杠杆投资点**。

---

## 方向一：用户级存储配额与成本治理（Storage Quota & Cost Governance）

### 代码级证据

| 位置 | 证据 |
|------|------|
| `configs/config.yaml:67-68` | 仅有 `max_upload_bytes: 52428800`（单文件上限），**无任何用户级聚合配额** |
| `internal/service/file.go:108-112` | `Create()` 检查 `s.maxBytes > 0 && in.Size > s.maxBytes` 拒绝超限文件，但不追踪用户总用量 |
| `internal/service/file.go:137-143` | 内容去重逻辑（`FindByChecksum`）中，即使复用了已有 blob，文件行仍会新建 — 用户的多条记录共享同一存储，但配额系统无法感知 |
| `internal/repository/file.go:15-30` | `FileRepository` 接口无 `CountSizeByOwner` 或 `SumSizeByOwner` 方法 |
| `internal/storage/gc/sweeper.go:130-160` | GC sweeper 清理孤儿 blob 时，不考虑用户配额释放 |
| `migrations/` 全部 22 个 | 无 `user_storage_usage` 表或 `files.owner_size_total` 聚合列 |

### 问题

aero-id 是一个**文件**服务。其核心经济模型是：用户上传文件 → 消耗存储空间 → 产生成本。但**当前系统对此完全无感**：

- 一个用户可上传 50MB × 无限次 = 无限存储
- 共享文件（显式分享给其他用户）不占用接收方配额，也不影响发送方可用空间
- 内容去重后的文件共享同一个 blob，但"谁为这 50MB 付费"没有模型
- 无法回答"哪个用户消耗了最多存储"、"下周存储成本会增长多少"等运营问题

**这不是一个边缘问题** — 在多租户 SaaS 场景下，存储成本通常排在基础设施支出的前三位。没有配额意味着：
1. **成本不可控** — 一个恶意用户就可以填满磁盘/S3 桶
2. **无法变现** — 无法提供"免费 1GB，Pro 100GB"的定价模型
3. **无容量规划** — 不知道何时需要扩容

### 建议方向

**Phase 1 — 配额追踪基础设施**（低工作量，高架构杠杆）

```go
// 新增领域模型
type StorageQuota struct {
    UID           string    // SSO uid
    MaxBytes      int64     // 配额上限（0 = 无限制）
    UsedBytes     int64     // 当前已用
    UpdatedAt     time.Time // 最后计算时间
}

// 新增 Repository 方法
type FileRepository interface {
    // 原有方法...
    SumSizeByOwner(ctx context.Context, uid string) (int64, error) // 活跃文件总大小
    CountActiveByOwner(ctx context.Context, uid string) (int64, error) // 活跃文件总数
}

// service 层增量检查
func (s *fileService) enforceQuota(ctx, actor, newSize int64) error {
    if s.quota.MaxBytes <= 0 { return nil } // 无限制
    used := s.files.SumSizeByOwner(ctx, actor)
    if used + newSize > s.quota.MaxBytes {
        return apperr.Conflict("file.quota_exceeded", "storage quota exceeded")
    }
    return nil
}
```

关键设计决策：
- **配额是软限制**（软拒绝 → 用户可购买更多空间），非硬上限
- **去重 blob 的配额归属**：谁先上传谁"付费"，共享文件不重复计算
- **共享文件配额**：接收方不消耗配额，由 owner 承担
- **配额增量更新**：`Create` 时加、`Delete`(软删)时减，GC 硬删时最终确认

**Phase 2 — 配额预警与运营面板**（中工作量）

- 当用户使用量达到 80%/90%/100% 时触发通知（复用现有 outbox + notification 管道）
- Prometheus gauge `aeroid_storage_used_bytes{uid}` + `aeroid_storage_quota_bytes{uid}`
- Admin API `GET /admin/quota` 查看全平台存储使用分布

### 边界情况

| 场景 | 处理策略 |
|------|----------|
| **内容去重后的配额计算** | Blob 成本由第一个上传者承担；后续覆盖同一 checksum 的文件行不重复计费 |
| **软删除期间配额** | 软删除立即释放配额；GC 硬删是最终确认 |
| **共享文件的配额** | Owner 承担全部存储成本；grantee 不消耗配额 |
| **批量上传导致配额检查竞态** | 可接受的短暂超配（类似信用卡超额），由定时 reconciliation 矫正 |
| **历史数据回填** | 上线前运行一次性 SQL 计算所有用户的 `SumSizeByOwner` |

### 优先级判断

| 维度 | 评分 |
|------|------|
| 商业价值 | 🔥🔥🔥🔥🔥 — 存储成本通常是 SaaS 前三支出，配额是变现的前提 |
| 架构杠杆 | 🔥🔥🔥🔥 — 新增领域模型 + 2 个 repo 方法 + service 检查，不影响现有逻辑 |
| 用户感知价值 | 🔥🔥🔥🔥 — 用户可见的"已用/总空间"指标是信任的基础 |
| 实现成本 | **低-中**（约 2-3 天，无新依赖） |

---

## 方向二：跨域资源生命周期级联（Cross-Domain Resource Lifecycle Cascade）

### 代码级证据

| 位置 | 证据 |
|------|------|
| `internal/service/file.go:228-253` | `Delete()` 只做 `files.SoftDelete` + 存储 ref 递减。**文件分享记录（`file_shares`）、评论（`comments`）没有任何级联清理** |
| `internal/service/message.go` (impl) | `Delete()` 调用 `messages.SoftDelete`，**但 `message_reactions` 行在原消息删除后继续存在**，成为孤儿数据 |
| `internal/service/comment.go` (impl) | `Delete()` 调用 `comments.SoftDelete`，**但 `comment_likes` 行保持原样** |
| `internal/domain/comment.go:15-30` | `CommentReport` 引用了 `CommentID`，无级联删除 |
| `internal/repository/file_share.go:50-65` | `Revoke()` 单行删除，但**文件软删除后分享记录全部成为孤儿** |
| `migrations/000001-000022` | **没有任何 CASCADE 或应用层级联逻辑** — 所有关联表都是独立行 |
| `internal/storage/gc/sweeper.go` | GC 只清理文件 blob，**不清理文件相关的社交数据**（shares, comments, likes）|

### 问题

这是整个系统中**最隐蔽的数据完整性缺口**。来看一个具体的生命周期：

```
用户上传文件
  → 与其他 5 个用户分享（写入 5 条 file_shares）
  → 该文件下产生 10 条评论
  → 每条评论有若干 like
  → 用户删除文件（soft delete）
  → ... 发生了什么？
```

答案是：**什么都没发生**。`file_shares` 的 5 行、`comments` 的 10 行、`comment_likes` 的 N 行全部保留在数据库中，成为孤儿数据。它们：
- **永远不会被 GC 清理**（GC 只懂 `storage_key`）
- **永远不会被业务查询到**（文件已软删，不能被正常 API 访问）
- **永久占用存储空间**（索引 + 行数据）
- **可能违反隐私预期**（用户认为删了文件，点评/分享记录还在库里）

这不是一个理论问题。在用户量增长后，孤儿数据会成为：
- 数据库膨胀的主要来源（行数远超过活跃数据）
- 隐私合规风险（GDPR 删除请求无法完整执行）
- 审计日志的噪音（可以看到针对已删除文件的分享/举报记录）

### 建议方向

#### 阶段 1：定义级联策略矩阵

| 父实体删除 | 子实体 | 策略 | 原因 |
|-----------|--------|------|------|
| 文件软删 | `file_shares` 行 | **级联软删** | 分享在文件删除后无意义 |
| 文件软删 | `comments` (target=该文件) | **孤儿保留 + 标记** | 评论对平台有独立价值；可加 `target_deleted` 标记 |
| 消息软删 | `message_reactions` | **级联软删** | 反应在消息删除后无意义 |
| 评论软删 | `comment_likes` | **级联软删** | 点赞在评论删除后无意义 |
| 用户注销 | 所有关联行 | **匿名化** (owner_uid 置为 "deleted-<uuid>") | 保留社交图谱完整性 |

#### 阶段 2：在 service 层实现级联钩子

```go
// 在 fileService.Delete() 中增加：
func (s *fileService) Delete(ctx context.Context, id domain.ID) error {
    // ...现有逻辑（权限检查、软删、ref递减）...
    
    // 级联清理（异步 + 容错）
    go s.cascadeFileDeletion(ctx, id, f.StorageKey)
    return nil
}

func (s *fileService) cascadeFileDeletion(ctx context.Context, fileID domain.ID, storageKey string) {
    // 1. 级联软删 file_shares
    _ = s.shares.RevokeAllByFile(ctx, fileID) // 新增方法
    
    // 2. 标记相关评论为 target_deleted
    _ = s.commentRepo.MarkTargetDeleted(ctx, "file", fileID) // 新增方法
    
    // 3. 记录审计
    if s.audit != nil {
        _ = s.audit.Log(ctx, &domain.AuditEvent{...})
    }
}
```

**关键设计原则**：
- 级联是**异步的**（不阻塞用户删除请求）
- 级联是**容错的**（单个步骤失败不影响其他步骤）
- 级联是**可观测的**（每次级联写入审计日志）
- 提供**后台 reconciliation** 定期清理已经遗漏的孤儿数据

#### 阶段 3：用户注销（账户删除）的处理

SSO 是账户权威源，aero-id 没有账户表。但 SSO 用户注销后，aero-id 中的数据需要处理：

- 立即：owner 匿名化（`owner_uid` → `deleted-<uuid>`）
- 保留：评论内容（作为平台内容保留）
- 清理：个人设置、通知偏好

### 边界情况

| 场景 | 处理 |
|------|------|
| **级联操作中途崩溃** | 幂等设计：每个级联操作都是可重入的 |
| **被级联删除的评论已有举报** | 举报作为审计记录保留，评论内容清除 |
| **文件被多个用户共享** | Owner 删除后所有分享记录消失；grantee 需要联系 owner 重新分享 |
| **父实体恢复（undelete）** | 暂不支持；级联是不可逆的（undo log 太复杂） |

### 优先级判断

| 维度 | 评分 |
|------|------|
| 数据完整性 | 🔥🔥🔥🔥🔥 — 这是一个无声的数据泄漏和膨胀问题 |
| 隐私合规 | 🔥🔥🔥🔥🔥 — GDPR/CCPA 删除请求无法在当前架构下完整执行 |
| 用户感知价值 | 🔥🔥 — 用户看不见孤儿数据，但能感受到"删了的东西还在"的不信任 |
| 实现成本 | **中**（约 3-5 天，涉及多个 service 的修改） |

---

## 方向三：多实例分布式限流（Distributed Rate Limiting）

### 代码级证据

| 位置 | 证据 |
|------|------|
| `internal/pkg/ratelimit/ratelimit.go:10-16` | 文件头注释明确声明：**"The limiter is PER-INSTANCE: each running aero-id process keeps its own buckets in memory. Distributed/global rate limiting would need shared state (e.g. Redis) and is intentionally out of scope here."** |
| `internal/app/app.go:222-230` | `limiter = ratelimit.New(...)` 创建单机实例，在 app 启动时构造 |
| `configs/config.yaml:54-65` | `rate_limit` 配置段无集群共享存储配置项 |
| `internal/middleware/ratelimit.go` | 限流中间件使用此单机实例，无集群协调 |
| `internal/middleware/auth.go:30-45` | 限流 key 是 `actor_uid` 或 `client_ip`，在多实例下完全无效 |

### 问题

这是**生产化最严重的隐藏缺陷**。配置了 `rate_limit.rps: 20, burst: 40`，看起来是"每个用户每秒 20 请求"。但实际效果是：

**在 5 副本部署中，每个用户每秒可以发送 20 × 5 = 100 请求**才触发限流。

更糟的是，这个缺陷是有意设计的（注释明确说 out of scope），但**没有任何部署文档或告警提示运维人员**。一个团队可能会在 staging 环境中验证限流正常工作（单实例），然后部署到生产（多实例），以为受到保护。

这带来的具体风险：
1. **暴力破解 / 枚举攻击** — 攻击者可以 N 倍于配置 RPS 的速度尝试
2. **DDoS 防护失效** — 限流无法作为多实例场景下的防护层
3. **资源耗尽** — 一个行为不当的客户端可以在被单实例限流前耗尽连接池

### 建议方向

#### Option A: 中心化 Redis 限流（推荐）— 高

```go
// 新增 internal/pkg/ratelimit/redis.go
type RedisLimiter struct {
    client *redis.Client
    rate   float64
    burst  int
    window time.Duration // sliding window
}

func (l *RedisLimiter) Allow(key string) bool {
    // 使用 Redis Sorted Set + 滑动窗口算法
    // 或更简单的: INCR + EXPIRE (固定窗口, 允许边界毛刺)
    // 或使用 Redis 4.0 的 ALGO.SLIDING_WINDOW 或 CL.THROTTLE
}
```

建议的演进路径：
1. 短期内（Phase 0）：**保留内存限流作为第一道防线** + 文档警告多实例无效
2. 中期（Phase 1）：**Redis 限流作为可选后端**，配置开关 `rate_limit.backend: memory | redis`
3. 长期（Phase 2）：**双层限流** — 内存限流（低延迟，捕获本机毛刺）+ Redis 限流（全局协调，精确控制总量）

#### Option B: 基于 PostgreSQL 的限流（低成本替代方案）— 中

复用现有 PostgreSQL 连接实现分布式限流：

```sql
-- 方法：行锁 + 计数器，在 txn 中原子更新
INSERT INTO rate_limit_buckets (key, tokens, last_refill, expires_at)
VALUES ($1, $2, NOW(), NOW() + INTERVAL '1 minute')
ON CONFLICT (key) DO UPDATE SET
    tokens = GREATEST(0, rate_limit_buckets.tokens - 1),
    last_refill = NOW()
WHERE rate_limit_buckets.tokens > 0;
```

缺点：增加 PG 写负载、有性能上限。适合中等规模部署。

### 边界情况

| 场景 | 处理 |
|------|------|
| **Redis 不可用** | 降级到内存限流（单机保护）+ 日志告警 |
| **时钟偏差** | Redis 限流可接受毫秒级偏差；PG 方案取决于事务时间 |
| **跨数据中心部署** | Redis 需要全球部署或接受区域性限流窗口 |
| **限流键膨胀** | TTL 自动清理 + 惰性驱逐 |

### 优先级判断

| 维度 | 评分 |
|------|------|
| 安全影响 | 🔥🔥🔥🔥🔥 — 当前限流在多实例下完全无效，是伪防御 |
| 架构杠杆 | 🔥🔥🔥🔥 — 新增后端抽象，内存实现保持不变 |
| 用户感知价值 | 🔥🔥🔥 — 用户不会直接感知，但暴力攻击减少提升安全感 |
| 实现成本 | **中**（约 3-4 天，含 Redis 依赖引入） |

---

## 方向四：SSE 推送可靠性增强与多通道通知（Notification Delivery Reliability）

### 代码级证据

| 位置 | 证据 |
|------|------|
| `internal/hub/hub.go:60-65` | SSE Hub 的 channel buffer = 16，**满时直接丢弃事件**（`default: // subscriber is too slow; drop rather than block`）|
| `internal/handler/http/events.go:80-95` | SSE 端点依靠 `Last-Event-ID` 回放恢复丢失事件，但**仅在客户端主动重连时生效** |
| `internal/handler/http/events.go:103-108` | `replayMissed` 最大回放 50 条，**客户端离线超过通知保留期后永久丢失** |
| `internal/service/notification.go:155-160` | `NotificationDispatcher` 在 SSE push 失败时**不做重试或持久化** — 完全 best-effort |
| `internal/service/notification.go` | **无 APNs / FCM / 邮件 fallback** — SSE 是唯一的实时推送通道 |
| `internal/notification/repository.go` | `ListByRecipient` 无 `created_at` 过滤之外的保留期策略 |
| `internal/hub/hub.go:83-90` | **Hub 不支持跨多设备** — 同一用户多个浏览器/Mobile 连接各自独立订阅 |

### 问题

aero-id 的通知管道架构如下：

```
请求路径 →  outbox(事务性写入) →  relay(异步轮询) →  NotificationDispatcher
                                                          ↓ 创建数据库行
                                                          ↓ SSE push (best-effort)
                                                          ↓ ... 通知到达浏览器
```

这个管道的可靠性分层非常清楚：
- **持久层**（数据库行）：可靠 — 事务性写入，可查询
- **实时层**（SSE）：不可靠 — best-effort，无重试，无持久化

这对桌面浏览器端是可接受的（SSE 重连 + `Last-Event-ID` 回放兜底）。但对以下场景**完全失效**：

1. **移动端 / 原生 App** — 没有 SSE 连接，永远收不到实时推送
2. **PC 浏览器关闭后** — 用户隔夜回来才能看到通知
3. **网络不稳定** — SSE 频繁断开重连，channel buffer 16 在批量通知时很容易溢出
4. **多设备** — 手机已读通知，PC 上还是未读状态（无跨设备同步）

### 建议方向

#### 阶段 1: SSE 可靠性增强（近期的低成本改进）

```go
// 1. 可配置的 channel buffer（当前硬编码为 16）
type HubConfig struct {
    SubChannelBuffer int // 默认 16，可根据预期并发调大
}

// 2. 增加持久化的离线通知队列
// 当 SSE push 失败时（sub channel full 或 client 不在线），
// 累积到离线队列中，客户端重连时一次性回放
```

#### 阶段 2: 可插拔推送通道（高价值，中等成本）

引入 `PushProvider` 接口：

```go
// internal/notification/push/push.go
type PushProvider interface {
    // Send delivers a push notification to the target device/user.
    Send(ctx context.Context, push *PushNotification) error
    // RegisterDevice saves a device token for a user.
    RegisterDevice(ctx context.Context, uid string, token DeviceToken) error
}

// 实现 1: SSE（现有）
// 实现 2: Firebase Cloud Messaging (FCM) for Android
// 实现 3: Apple Push Notification Service (APNs) for iOS
// 实现 4: Email 摘要（每天/每周发送未读通知汇总）
```

**设计要点**：
- `PushProvider` 运行在 dispatcher 之后（不阻塞 outbox relay）
- 每个 provider 独立配置、独立失败（SSE 失败不影响 FCM）
- 设备注册需要新的 API 端点 `POST /v1/devices` + 设备表
- FCM/APNs 需要新的配置项（API keys, 证书等）

#### 阶段 3: 跨设备已读同步

当前 `MarkRead` 和 `MarkAllRead` 操作后，SSE 不向其他设备推送同步事件。需要：

```
用户A在手机上阅读通知
  → MarkRead API → 数据库更新
  → SSE push 给用户A的其他设备（PC、平板）
  → 其他设备自动标记已读
```

这需要 Hub 支持向**同一用户的所有连接**广播事件，而不仅仅是某个特定连接。

### 边界情况

| 场景 | 处理 |
|------|------|
| **设备 token 过期** | FCM/APNs 返回 Unregistered 错误后自动清理设备记录 |
| **用户卸载 App** | 同上 + 周期性清理僵尸设备 |
| **通知频率过高** | 已有 rate limiter + collapse 机制，但需要通知级别的频率控制 |
| **多设备已读同步延迟** | 可接受秒级延迟，不做强一致性保证 |

### 优先级判断

| 维度 | 评分 |
|------|------|
| 产品价值 | 🔥🔥🔥🔥🔥 — 没有移动推送的通讯应用在今天不可想象 |
| 用户感知价值 | 🔥🔥🔥🔥🔥 — 这是"消息 App vs 后台服务"的分水岭 |
| 架构杠杆 | 🔥🔥🔥 — PushProvider 接口是薄抽象层 |
| 实现成本 | **高**（约 1-2 周，含 FCM/APNs 集成和多设备同步） |

---

## 方向五：可观测性深度 — 业务指标、SLO 仪表盘与异常检测（Business Metrics, SLO Dashboard & Anomaly Detection）

### 代码级证据

| 位置 | 证据 |
|------|------|
| `internal/pkg/metrics/metrics.go` | 当前 RED 指标覆盖 HTTP/gRPC/outbox/SSO，但**完全是基础设施指标**（QPS、延迟、错误率）|
| `internal/pkg/metrics/metrics.go:80-120` | 无任何业务指标：`files_uploaded_total`、`messages_sent_total`、`reports_created_total`、`quota_exceeded_total` 等 |
| `internal/middleware/metrics.go:30-50` | RED 中间件按 HTTP 方法和路径 + 状态码分类，但不按 uid/tenant 维度 |
| `internal/service/notification.go` | 通知 dispatcher 在静默丢弃通知时**不产生任何指标** |
| `internal/service/comment.go` (resolve report) | 举报解决/驳回操作**无业务指标** |
| `internal/outbox/relay.go:45-55` | Outbox relay 记录 published/failed 计数，但**无按事件类型分类的延迟直方图** |
| `internal/app/app.go` | 启动时无 SLO 配置读取，无健康指标阈值 |

### 问题

aero-id 的可观测性当前是**面向运维的，而非面向业务的**。这对于一个已经功能完整的服务来说，是一个运营成熟度的关键缺口：

**当前能回答的问题**（运维视角）：
- 服务 QPS 是多少？
- P99 延迟是多少？
- 错误率是多少？
- SSO 缓存命中率？

**当前不能回答的问题**（业务 / 产品 / 管理视角）：
- 今天有多少个文件被上传？
- 消息发送量的日环比是多少？
- 举报率是否突然上升（需要调查）？
- 哪个用户的存储消耗增长最快？
- 通知的"已读率"是多少？
- 消息 Reactions 的使用趋势？
- 搜索的"零结果率"（用户搜索但找不到任何内容）？

这些问题在当前架构中**很容易回答** — 只需要在现有代码的关键路径上增加几个计数器。但这些信息缺失意味着：

1. **无法定义 SLO** — 没有业务指标就写不出有意义的 SLO（"99.9% 的消息在 5 秒内送达通知"需要消息发送和通知送达的计数）
2. **无法做容量规划** — 不知道增长趋势
3. **无法做产品决策** — 不知道用户使用哪些功能
4. **无法做异常检测** — 业务异常（举报激增、消息量骤降）无法自动发现

### 建议方向

#### 阶段 1: 关键业务指标埋点（低工作量，高回报）

在现有 `metrics.Registry` 上新增计数器：

| 指标名 | 类型 | 标签 | 埋点位置 |
|--------|------|------|----------|
| `aeroid_files_uploaded_total` | Counter | `mime_type` | `service.file.Create()` |
| `aeroid_files_downloaded_total` | Counter | `mode=presign\stream` | `service.file.DownloadURL()` |
| `aeroid_messages_sent_total` | Counter | `kind` | `service.message.Send()` |
| `aeroid_messages_read_total` | Counter | — | `service.message.MarkRead()` |
| `aeroid_comments_created_total` | Counter | `has_parent` | `service.comment.Create()` |
| `aeroid_reactions_total` | Counter | `type=like\message_reaction` | `service.comment.Like()` / `service.message.React()` |
| `aeroid_reports_created_total` | Counter | — | `service.comment.Report()` |
| `aeroid_reports_resolved_total` | Counter | `status=resolved\dismissed` | `service.comment.ResolveReport()` |
| `aeroid_notifications_pushed_total` | Counter | `type, channel=sse\fcm\apns` | dispatcher 推送处 |
| `aeroid_notifications_delivered_total` | Counter | `type` | dispatcher 创建行后 |
| `aeroid_search_total` | Counter | `kind, zero_results` | `service.search.Search()` |

#### 阶段 2: SLO 仪表盘与告警规则

```yaml
# Prometheus 告警规则示例
# 发送延迟 SLO: 95% 的消息在 10 秒内送达通知
- record: aeroid:message_delivery_latency_seconds:p95
  expr: histogram_quantile(0.95, rate(aeroid_outbox_event_delay_seconds_bucket[5m]))
  
- alert: HighMessageDeliveryLatency
  expr: aeroid:message_delivery_latency_seconds:p95 > 10
  for: 5m
  labels: { severity: warning }
  annotations:
    summary: "Message delivery P95 latency exceeds 10s"

# 零搜索结果率异常
- alert: SearchZeroResultRateSpike
  expr: rate(aeroid_search_zero_results_total[5m]) / rate(aeroid_search_total[5m]) > 0.3
  for: 10m
  labels: { severity: warning }
  annotations:
    summary: "Search zero-result rate exceeds 30% — possible indexing issue"
```

#### 阶段 3: 业务健康看板（Grafana）

建议的看板面板布局：
- **行 1（增长）**：日活用户（DAU）、文件上传量、消息发送量、评论创建量
- **行 2（健康）**：通知送达率、搜索零结果率、举报率
- **行 3（容量）**：存储消耗总量（按用户分布）、DB 行数增长趋势
- **行 4（SLO）**：消息端到端延迟直方图、通知送达延迟直方图

### 边界情况

| 场景 | 处理 |
|------|------|
| **业务指标基数过高** | Counter 是 Prometheus 的原生数据类型，无基数问题；高基数标签（如 uid 维度）谨慎使用 |
| **指标与 RED 指标冲突** | 业务指标使用不同的命名空间前缀（`aeroid_*` vs RED 的 `http_*`），两者不冲突 |
| **测试环境指标污染** | 通过 `env` 标签区分生产/预发/测试 |

### 优先级判断

| 维度 | 评分 |
|------|------|
| 运维价值 | 🔥🔥🔥🔥🔥 — 没有业务指标的服务是一个黑箱 |
| 产品价值 | 🔥🔥🔥🔥 — 产品决策需要数据驱动；当前完全无数据 |
| 架构杠杆 | 🔥🔥🔥🔥🔥 — 极低成本（计数器 + histogram），复用已有 metrics.Registry |
| 实现成本 | **低**（约 1-2 天，纯埋点 + Grafana 看板） |

---

## 路线图建议

```
Phase 0 (P0 — 可独立启动):
├── 方向五：业务指标埋点 + Grafana 看板
│   └── 成本最低（1-2天），收益立即可见
│   └── 为所有其他方向提供数据基础
│
├── 方向三：分布式限流
│   └── 多实例部署的安全基石
│   └── 内存实现可保留，增加 Redis 可选后端
│
└── 方向一：存储配额追踪
    └── 基础设施级投入，支撑后续商业变现

Phase 1 (P1 — 上线前完成):
├── 方向二：跨域资源生命周期级联
│   └── 数据完整性和隐私合规的基础
│   └── 建议在数据积累到百万级前完成
│
└── 方向四：多通道通知推送
    └── 最高产品价值，但成本也最高
    └── 依赖方向五（需要通知送达率指标）
```

### 执行顺序逻辑

```
Phase 0a: 方向五（业务指标）════════ 零依赖，独立交付
Phase 0b: 方向三（分布式限流）══════ 零依赖，独立交付
Phase 0c: 方向一（存储配额）════════ 零依赖，独立交付
                         ↓
Phase 1a: 方向二（生命周期级联）═══ 零依赖，独立交付
Phase 1b: 方向四（多通道推送）═════ 依赖方向五的指标验证效果
```
