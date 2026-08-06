# aero-id — 生产级规模下的架构断层与扩展方向

> **日期**: 2026-07-10
> **视角**: 资深架构师 / SRE / 产品经理
> **方法**: 对 `internal/` 全部 ~40 个 Go 包、`migrations/` 22 份迁移、`api/proto/` 与 `cmd/` 进行全局扫描，分析调用链路与数据流，定位当系统从单实例/小规模走向多副本/大规模时必然暴露的架构断层。
> **前提条件**: `feat/roadmap` 已合并；所有功能（41 个 gRPC RPC + 全 HTTP 对等 + 22 个迁移）已验证通过。
> **约束**: 不编写任何代码，仅分析。

---

## 文档定位

已有 13+ 份产品/功能方向文档（群聊、文件版本、通知平台、Webhook 平台、消息线程、AI 平台、活动流等），覆盖了大量**功能增长型**方向。

本文不重复那些内容。本文聚焦一个特定切口：**当 aero-id 从开发环境/单实例运行走向生产级多副本部署（≥3 副本，日均百万级请求）时，当前架构中无法扩展或会断裂的点**。这些不是"锦上添花"的功能，而是生产规模下的"必修课"。

---

## 当前架构的诊断摘要

基于代码扫描，提取以下跟生产扩展性直接相关的特征：

| 维度 | 当前状态 | 多副本下的风险 |
|------|---------|--------------|
| **速率限制** | 进程内内存令牌桶，key 按 uid/IP | ❌ 每个副本独立计数，总吞吐无法控制；用户轮询不同副本可绕过限额 |
| **SSO 缓存** | `sync.Map` 进程内缓存（token + user） | ❌ 新副本冷启动时全部穿透到 SSO；滚动部署期间 SSO 负载突增 N 倍 |
| **SSE Hub** | 进程内 `sync.Map` 连接注册表 | ❌ 用户连到副本 A 后，副本 B 产生的通知推不到该用户；滚动重启断连 |
| **限流范围** | 全局同速率（RPS/Burst 对所有 endpoint 一致） | ❌ 搜索/上传等重操作与列表/读取等轻操作竞争同一桶，无法差异化保护 |
| **数据库连接** | 单连接池，无熔断，无读副本感知 | ❌ DB 慢查询（搜索、inbox）会占满连接池，影响其他轻查询；所有查询走主库 |
| **凭据管理** | 启动时从环境变量/配置文件读一次，永不轮换 | ❌ 长期运行的实例无法轮换 `SIGNING_KEY`、Webhook key、TLS 证书 |
| **文件下载** | Local 走 HMAC-signed 代理；S3 走 presigned URL（无 CDN） | ❌ 大文件下载直接打到 aero-id 或 S3，无法利用 CDN 缓存；无 Range 请求优化 |
| **错误协议** | 返回 `code` 字符串 + `message` | ❌ SDK 客户端需解析字符串判错，无结构化元数据、无重试提示、无 i18n |

---

## 方向一：多副本缓存一致性与粘滞路由（Cache Coherence & Sticky Routing）

### 问题

当前所有缓存——SSO `TokenCache`、`UserCache`、速率限制令牌桶、SSE `Hub` 连接——都是**进程内内存状态**。在多副本部署中：

1. **滚动部署**：新副本启动时所有缓存为空，第一批请求全部穿透到 SSO。若 5 个副本同时部署，SSO 瞬时负载暴增 5 倍。
2. **速率限制**：假设 `rps=20`，用户在两副本间轮询，每个副本独立计数，实际吞吐可达 40 rps。
3. **SSE 推送**：用户通过负载均衡连到副本 A。副本 B 的一条通知（例如消息已读回执）产生后，通过 `NotificationDispatcher` 的 `Pusher.Publish` 推送到本副本的 Hub——用户永远收不到。

### 建议

**粘滞路由（Sticky Session）** 是最小侵入方案：

| 组件 | 方案 | 代码影响 | 新增基础设施 |
|------|------|---------|------------|
| SSE Hub | 负载均衡层 cookie 粘滞（基于 `uid`），保证同一用户的 SSE 请求始终到达同一副本 | 无（纯运维配置） | 负载均衡层（ALB/Envoy） |
| Token Cache | 冷启动预热：`App.warmup()` 在就绪前主动调用 `HealthCheck` 预填连接 + 触发 `singleflight` 预热趋势 token | 修改 `app.go` `warmup()` 增加 `ssoClient.WarmupCache(ctx)` | 无 |
| User Cache | 同上 | 同上 | 无 |
| Rate Limiter | 接受"尽力近似"：每个副本独立计数，但降低单个副本容量（`burst=expected_burst/N`）。长期规划分布式限流 | 配置调整 | 无 |

**分布式缓存（Redis）** 是长期方向：

1. `TokenCache` 改为 Redis-backed：token → uid 映射，TTL 同当前。
2. `UserCache` 同理：uid → user 映射，批量查询不改。
3. 速率限制改为 Redis `Lua` 滑动窗口：精确全局计数。
4. SSE Hub 改为 Redis Pub/Sub + 每个副本订阅：任一副本发布事件，所有副本实时收到并推送到各自的本地连接。

### 用户价值

- 用户：速率限制按预期工作，SSE 通知不丢失
- 运维：滚动部署不造成 SSO 负载尖峰，缓存命中率从 ~0% 回升到 ~100%
- 架构：为多区域部署打下基础

### 估算

| 阶段 | 内容 | 工作量 |
|------|------|-------|
| P0 | 粘滞路由 + warmup 扩展 | 1-2 天 |
| P1 | Redis-backed TokenCache + UserCache | 1 周 |
| P2 | 分布式速率限制（Redis）+ SSE Hub（Redis Pub/Sub） | 1-2 周 |

---

## 方向二：数据库连接治理——熔断、读副本、查询隔离（Database Circuit Breaker, Read Replicas & Query Isolation）

### 问题

当前所有数据库查询共享同一个 GORM 连接池，无读副本感知、无查询分类、无熔断：

```
sqlDB = gorm.Open(postgres.Open(dsn), &gorm.Config{...})
// 所有查询——file list, message inbox, search, notification list, audit log——
// 都从同一个 pool 获取连接
```

**规模风险**：

1. **搜索查询锁死连接池**：`SearchMessages` 执行 PostgreSQL `tsquery` 全文搜索，可能耗时 2-10 秒。若 10 个并发搜索占用 10 个连接，剩余的 40 个连接要处理其他所有请求——文件上传、消息发送、通知列表。
2. **无熔断**：若 DB 因维护、备份、或慢查询积压变慢，所有依赖 DB 的 RPC 都排队等待，直至 timeout。SSO 有熔断保护，DB 没有。
3. **无读副本利用**：PostgreSQL 16 的 `hot_standby` 只读副本可为搜索、通知列表、审计日志等查询型负载提供水平扩展，但当前架构无法路由。

### 建议

**短期——查询超时 + 粗粒度熔断**：

```go
// repository/db.go — 为 DB 包级别查询设置 StatementTimeout
db.Exec("SET statement_timeout = '5s'")

// repository — 每个 Repository 在连续 N 次失败后跳过查询直接返回
// 已有 SSO 的 breaker 模式可复用
type DBCircuitBreaker struct { /* 复用 sso/breaker.go 的模式 */ }
```

**中期——读写分离连接池**：

| 查询类别 | 目标 | 路由 |
|---------|------|------|
| 写操作（Create/Update/Delete） | 主库 (RW) | 所有 Repository 的 Create/Update/Delete 方法 |
| 读操作（Get/List/Search） | 从库 (RO) | 所有 Repository 的 Get/List/Search 方法，加上 `staleness_ok` 标志 |

**实现路径**：`gorm.io/plugin/dbresolver` 已内建支持，仅需在 `db.go` 中配置多连接池和读写规则。

### 用户价值

- P99 延迟：搜索等重查询不阻塞其他请求
- 可用性：DB 故障时表现为降级而非熔断（搜索失败但消息发送正常）
- 扩展性：读副本水平扩展，支撑更多查询型 RPC（搜索、inbox、通知）

### 估算

| 阶段 | 内容 | 工作量 |
|------|------|-------|
| P0 | `statement_timeout` + 慢查询日志 | 0.5 天 |
| P1 | dbresolver 读写分离 + 配置 | 2-3 天 |
| P2 | DB circuit breaker（复用 breaker 模式） | 2 天 |

---

## 方向三：凭据轮换基础设施（Credential Rotation Infrastructure）

### 问题

当前系统依赖 5 类敏感凭据，全部在启动时一次性加载且无法轮换：

| 凭据 | 用途 | 配置来源 | 当前有无轮换 |
|------|------|---------|------------|
| `SIGNING_KEY` | Local storage HMAC 签名 | `config.yaml` / 环境变量 | ❌ |
| Webhook `signing_key` | 出站事件 HMAC 签名 | `config.yaml` / 环境变量 | ❌ |
| S3 `access_key` / `secret_key` | S3 存储访问 | `config.yaml` / 环境变量 | ❌ |
| SSO TLS 证书 | mTLS 客户端证书 | 文件路径配置 | ❌ |
| Database `password` | 数据库连接 | 环境变量 | ❌ |

**生产风险**：

1. `SIGNING_KEY` 泄露后，攻击者可伪造任意文件的下载 URL。当前无紧急轮换路径——需重启所有实例。
2. S3 key 泄露同样需重启。若 S3 有跨账户访问权限，泄露窗口期就是 aws 上告警到手动重启的时间。
3. SSO TLS 证书过期是**灾难性故障**——aero-id 会彻底失去认证能力，靠 `stale_ttl` 撑到过期达到后全部拒绝。

### 建议

**最小可行方案——文件系统重载（SIGHUP reload）**：

```go
// internal/config 增加动态字段
type SecretStore interface {
    Get(ctx context.Context, name string) (string, error)
    Watch(ctx context.Context, name string, onChange func(newValue string))
}
```

1. `SIGNING_KEY`：`signing.go` 从 `SecretStore` 读取而非启动时复制字符串。支持 `Watch`，key 变更后新签名立刻用新 key。
2. 双 key 过渡期（`oldKey` 仍能验证 + `newKey` 用于签名）：避免轮换瞬间所有已签发的下载 URL 失效。
3. TLS 证书：gRPC `credentials` 构建时使用 `tls.GetConfigForClient` 回调，每次握手从 `SecretStore` 拉取最新证书——实现证书热加载。

**实现选择**：

| 方案 | 新增依赖 | 复杂度 | 推荐 |
|------|---------|--------|------|
| 文件 SIGHUP（重新读硬盘文件） | 无 | 低 | P0 |
| HashiCorp Vault Agent Sidecar | Vault | 中 | P1 |
| Kubernetes Secret + Volume Mount | K8s | 低（文件系统层） | P1 |

### 用户价值

- 安全事件响应时间：凭据泄露后分钟级轮换，非小时级重启
- 避免证书过期导致的停机
- 满足 SOC2 / ISO 27001 密钥管理要求

### 估算

| 阶段 | 内容 | 工作量 |
|------|------|-------|
| P0 | `SIGNING_KEY` SIGHUP reload + 双 key 过渡 | 3 天 |
| P1 | TLS 证书热加载 | 2 天 |
| P2 | `SecretStore` 接口 + Vault 实现 | 1 周 |

---

## 方向四：可组合速率限制策略——按端点、按层级、按优先级（Composable Rate Limiting Strategy）

### 问题

当前速率限制是**全局扁平模型**：

```yaml
rate_limit:
  enabled: true
  rps: 20        # 所有端点统一
  burst: 40      # 所有端点统一
```

这意味着：
1. `POST /v1/files`（大文件上传，消耗大量 I/O）与 `GET /v1/search`（重 CPU 查询）与 `GET /v1/notifications`（轻量列表）**使用同一个桶**。
2. 管理员用户与普通用户无区别。
3. 无法针对特定昂贵的端点设置更严格的阈值（如搜索 5 req/s、上传 2 req/min）。

**代码证据**：`middleware/ratelimit.go` 中 `rateLimitKey` 返回 `uid:xxx` 或 `ip:xxx`，然后调用 `l.Allow(key)` ——密钥完全扁平，无层次。`internal/pkg/ratelimit/ratelimit.go` 中的 `Limiter` 是单桶 `sync.Map`。

### 建议

**模型：三层级联桶结构**

```
请求到达
  │
  ├─ 第一层：全局桶（保护整个服务）—— 500 req/s，所有用户共享
  │
  ├─ 第二层：端点桶（保护特定资源）—— 例如 search 10 req/s
  │
  └─ 第三层：用户桶（保护公平性）—— 例如 20 req/s 每个 uid
```

**实现变更**：

```go
// internal/pkg/ratelimit 扩展
type MultiLimiter struct {
    global  *Limiter     // 服务级
    byRoute map[string]*Limiter  // 端点级
    perUser *Limiter     // 用户级（保持现有速率）
}
```

配置变为：

```yaml
rate_limit:
  enabled: true
  global:
    rps: 500
    burst: 1000
  per_route:
    "POST /v1/files":          { rps: 2, burst: 4 }
    "GET /v1/search":          { rps: 5, burst: 10 }
    "GET /v1/notifications":   { rps: 50, burst: 100 }
  per_user:
    rps: 20
    burst: 40
```

### 用户价值

- 公平性：一个用户的疯狂搜索不会导致另一个用户无法上传文件
- 可预测性：昂贵操作有独立保护，不会在流量突增时打垮数据库
- 运维友好：配置驱动，无需改代码即可调整保护策略

### 估算

| 阶段 | 内容 | 工作量 |
|------|------|-------|
| P0 | `MultiLimiter` 实现 + 配置解析 | 3-4 天 |
| P1 | 集成到 HTTP 中间件 + gRPC 拦截器（需传递路由信息） | 2 天 |
| P2 | 分布式版本（Redis 后端）+ 管理 API（`GET /rate-limits/status`） | 1 周 |

---

## 方向五：文件下载管线优化——Range 请求、ETag、CDN 与条件性下载（File Download Pipeline Optimization）

### 问题

文件下载是 aero-id 的核心 I/O 路径，但当前两条路径都有明显的性能盲区：

**Local 模式**：
- `GET /files/dl?token=<hmac>` → handler 打开本地文件 → `io.Copy` 到响应
- 不支持 `Range` 请求 → 大文件断点续传、视频拖拽播放（seek）不可用
- 无 `ETag`/`Last-Modified`/`304 Not Modified` → 客户端重复下载同一文件
- 整个文件通过 Go 进程传输，占用 app 进程的 memory + goroutine，限制了并发下载数

**S3 模式**：
- 直接返回 S3 presigned URL，客户端直连 S3
- 可利用 S3 的 `Range` 支持和 `ETag`，因为 S3 对象存储原生支持
- 缺点：无 CDN 集成 → 每个下载都打到 S3 源站；无下载审计（aero-id 不参与请求）

### 建议

**短期——弥补 Local 模式的 HTTP 协议缺陷**：

```go
// internal/storage/local.go 或 internal/handler/http/file.go
// 在 download handler 中添加：
// 1. Range 请求解析（http.ServeContent 自动支持）
// 2. ETag 计算（基于文件的 checksum + modified_at）
// 3. Last-Modified 头
// 4. If-None-Match / If-Modified-Since → 304

// 注意：signing.go 生成的 token 需要包含 ETag/version 信息？
// 不需要——ETag 由文件元数据决定，token 只验证权限。
```

```go
// 修改方案：将 stream 下载 handler 改为使用 http.ServeContent
func (h *FileHandler) serveContent(c *gin.Context, f *domain.File, reader io.ReadSeeker) {
    c.Header("ETag", fmt.Sprintf(`"%s-%d"`, f.Checksum, f.UpdatedAt.Unix()))
    c.Header("Cache-Control", "private, max-age=31536000, immutable")
    http.ServeContent(c.Writer, c.Request, f.Name, f.CreatedAt, reader)
}
```

**中期——S3 + CDN 集成**：

| 组件 | 作用 | 实施 |
|------|------|-----|
| CloudFront / Cloudflare | 边缘缓存 + Range 请求 + 条件 GET | S3 作为 origin，presigned URL 增加 `?cf-cache-key=...` 参数 |
| 签名 URL 策略 | 控制缓存行为 | 对 public file 签发长期缓存签名；对 private file 签发短期 ephemeral URL |
| 下载审计 | 追踪谁下载了什么 | S3 访问日志（S3 Server Access Logs）→ 分析管道，或 aero-id 添加可选的代理下载模式 |

**长期——可选代理下载模式**：

```go
// 当前: mode=stream (Local) / mode=presign (S3)
// 新增: mode=proxy (S3)
// 让 aero-id 作为 S3 的代理，实现：
//   - 下载审计（记录 actor_uid, file_id, timestamp）
//   - 下载限速（保护后端存储）
//   - 动态水印 / 转码（未来扩展）
// 代价：增加 aero-id 的出口流量和 CPU 负载
```

### 用户价值

- 用户体验：大文件断点续传、快速重载（ETag 304）、视频拖拽播放（Range）
- 成本：CDN 缓存命中率 ≥80% 可节省 S3 egress 费用 80%+
- 安全：代理模式下可获得完整的下载审计

### 估算

| 阶段 | 内容 | 工作量 |
|------|------|-------|
| P0 | Local: `http.ServeContent` + ETag + Range + 304 | 2 天 |
| P1 | S3: CDN 集成 + 缓存策略配置 | 3 天（运维配置为主） |
| P2 | 可选代理下载模式（`mode=proxy`）+ 下载审计日志 | 1 周 |

---

## 优先级排序与演进路径

### 核心原则

1. **先保可用性，再谈功能**——方向一、二解决的是多副本下现有功能不失效的问题
2. **先做小重构，再引新依赖**——方向三、四、五中 P0 阶段均无需新增外部依赖
3. **循序渐进**——每个方向分 P0→P1→P2，最紧急的 P0 总工作≤7 天

### 建议执行顺序

| 优先级 | 方向 | 阶段 | 总工作 | 理由 |
|--------|------|------|--------|------|
| **P0** | 方向一：缓存一致性与粘滞路由 | P0（warmup + 粘滞） | 1-2 天 | 多副本部署的前提条件；SSE 断连为功能级阻断 |
| **P0** | 方向二：数据库连接治理 | P0（statement_timeout） | 0.5 天 | 防止搜索查询锁死连接池 |
| **P1** | 方向四：可组合速率限制 | P0（MultiLimiter） | 3-4 天 | 扁平限流在生产规模下是不安全的 |
| **P1** | 方向五：下载管线优化 | P0（Local 端 Range/ETag） | 2 天 | 低投入高 UX 回报 |
| **P1** | 方向三：凭据轮换 | P0（SIGNING_KEY reload） | 3 天 | 安全合规基线 |
| **P2** | 方向一 + 方向二 | Redis 缓存 + dbresolver | 2 周 | 需要外部依赖和测试 |
| **P2** | 方向三 + 方向四 + 方向五 | Vault / 分布式限流 / CDN / 代理下载 | 3 周+ | 长期工程投入 |

### 不做的情况

| 方向 | 何时可以不做 |
|------|------------|
| 粘滞路由 + 缓存预热 | 单实例部署，或使用可以保证同用户→同副本的外部负载均衡（如 K8s Service sessionAffinity=ClientIP） |
| DB 连接治理 | 数据库专享实例 + 查询量 < 100 req/s，搜索功能关闭 |
| 凭据轮换 | 手动运维可接受的内部系统；凭据泄露风险可忽略 |
| 可组合限流 | 用户量 < 100 且端点级差异可忽略 |
| 下载管线优化 | 文件平均 < 1MB 且用户无断点续传/CDN 需求 |

---

## 总结

本文列出的 5 个方向有一个共同特征：**它们不是新功能，而是现有功能在多副本/大规模下的"必修支架"**。当前代码库在单实例场景下是完全可工作的——缓存、限流、SSE、下载等一切正常。但当生产环境扩展到 ≥3 副本、日均百万请求时，以下断层会依次暴露：

1. **缓存与状态不一致**（方向一）——最先暴露，因为多副本滚动部署是标准生产实践
2. **数据库连接池争抢**（方向二）——第二个暴露，搜索/inbox 等重查询随用户量增长变慢
3. **全局扁平限流失效**（方向四）——第三个暴露，一个用户的 burst 操作影响所有用户
4. **凭据生命周期管理缺失**（方向三）——长期运行中暴露，证书过期是定时炸弹
5. **文件下载性能瓶颈**（方向五）——文件数量和大小增长后暴露，Range 和 ETag 缺失影响 UX

这 5 个方向的设计原则是：**最小侵入，渐进扩展**——每个方向的 P0 阶段都不需要新增外部依赖，都是在现有代码上的改造。
