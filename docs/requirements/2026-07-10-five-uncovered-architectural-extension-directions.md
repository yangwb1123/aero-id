# aero-id — 全局代码扫描：五个未被已有分析覆盖的架构级扩展方向

> **日期**: 2026-07-10  
> **视角**: 资深架构师 / 产品经理  
> **方法**: 全代码库逐文件通读（`internal/` 全部 22+ 包、150+ 源文件、22 份迁移 SQL、双传输层 handler、`cmd/`、`configs/`、`deployments/`、`Makefile`、`.github/workflows`），再与 `docs/requirements/` 下 **50 份**既有分析文档 + `docs/REVIEW_*` **8 份** + `ROADMAP.md` + `docs/ARCHITECTURE.md` + `docs/DECISIONS.md` **逐条交叉比对**。  
> **约束**: 不编写任何代码。每个方向附代码级证据。确保以下五个方向在全部既有分析中 **零系统性覆盖**。

---

## 交叉验证：已有 100+ 个方向已被覆盖

已有 50+ 份 `docs/requirements/*.md` + 8 份 `docs/REVIEW_*` 累计提出了 ~100 个独立扩展方向，覆盖的领域包括：

| 已覆盖领域 | 涉及文档数 |
|---|---|
| 文件预览/转码/病毒扫描 | 10+ |
| 全文搜索相关性/去重/分片 | 8+ |
| 通知推送（FCM/APNs/邮件） | 15+ |
| 跨实例协调 / 分布式限流 | 10+ |
| API 版本化 / 生命周期 / Deprecation Header | 8+ |
| Dead Letter 队列管理 | 5+ |
| 存储分层/迁移/冷热分离 | 8+ |
| i18n / 多语言 | 5+ |
| Event Sourcing 深化 | 3+ |
| 审计日志扩展 | 8+ |
| SSE 重连/回放/缓存 | 6+ |
| SSO 缓存/断路器/单飞 | 10+ |
| 文件/评论通知 outbox 事件 | 5+ |
| gRPC 反射访问控制 | 3+ |
| 已读回执通知 | 4+ |
| 用户阻塞级联影响 | 3+ |
| 消息 Reactions 通知 | 3+ |
| 搜索跨类型排序 | 3+ |
| 文件分享过期机制 | 2+ |
| 通知用户信息本地缓存 | 2+ |
| 批量资源操作 API | 3+ |
| 公开分享链接 | 1+ |
| 多租户/组织/工作空间 | 5+ |
| 存储配额与计费 | 6+ |
| Bot 框架 | 3+ |
| 数据安全（加密/脱敏/匿名化） | 12+ |
| API 可消费性 / SDK / OpenAPI | 8+ |
| 混沌测试 / Fuzz / 竞态 | 5+ |
| 特征开关 / 灰度发布 | 2+ |
| 分布式追踪 / 指标 / SLO 框架 | 8+ |
| 供应链安全 / SBOM / 依赖审计 | 3+ |
| 配置管理 / 校验 | 5+ |
| 消息召回 / 定时发送 | 2+ |
| 在线状态 / 打字指示器 | 3+ |
| 软删除恢复 | 2+ |
| 维护模式 | 1+ |
| 资源计量与配额 | 17+ |

**本文聚焦于上述 100+ 方向中零覆盖的 5 个方向。**

---

## 方向一：发布工程与制品生命周期（Release Engineering & Artifact Lifecycle）

### 为什么需要它

aero-id 现在是其他团队依赖的平台服务（双传输层、41 个 gRPC RPC、webhook 集成），但 **项目没有任何发布流程**。没有版本策略，没有制品分发管道，没有 Docker 镜像构建和发布，没有回滚机制。这是一个生产级服务的核心运营缺口。

### 当前证据

**无发布流程：**

```makefile
# Makefile（根目录）
.PHONY: test lint build run proto migrate-up migrate-down

# 缺失：
# - release          # 完整的发布流程
# - docker-build     # Docker 镜像构建
# - docker-push      # 镜像推送
# - changelog        # CHANGELOG 自动生成
# - version          # 版本号查看/更新
```

**Docker 镜像无版本标签：**

```yaml
# deployments/docker-compose.yml
services:
  aero-id:
    build:
      context: ..
      dockerfile: Dockerfile
    image: aero-id:latest   # ← 硬编码 latest，无版本标签
```

**无多阶段构建/Dockerfile：**

项目根目录无 `Dockerfile`（只有 `docker-compose.yml` 间接引用的构建上下文）。这意味着：
- 无最小化镜像（未使用 `distroless`、`scratch` 或 `alpine`）
- 无构建缓存策略
- 无镜像层优化

**CI 无发布阶段：**

```yaml
# .github/workflows/ci.yml
jobs:
  test:
    steps: [lint, test, test-integration, build]
    # 缺失：
    # - goreleaser（构建并发布二进制到 GitHub Releases）
    # - docker-build-and-push（构建并推送镜像到 ghcr.io）
    # - sbom-generate（生成并发布 SBOM）
    # - release-drafter（自动生成 Release Notes）
```

**无 SemVer 策略：**

```go
// internal/config/config.go
type App struct {
    Name    string `mapstructure:"name" yaml:"name"`
    Env     string `mapstructure:"env" yaml:"env"`
    Version string `mapstructure:"version" yaml:"version"` // "0.1.0" — 硬编码，无语义化版本策略
}
```

### 痛点

| 场景 | 当前表现 | 后果 |
|------|---------|------|
| 安全补丁 | 需要手动构建、推送 | 响应时间小时级 |
| 回滚 | 无版本化制品 | 需要重新构建旧代码 |
| 多环境部署 | 同一 `latest` 标签 | 无法区分 staging vs prod |
| 版本审计 | 仅代码中的字符串 | 无法追溯"哪个版本部署在哪" |
| 团队协作 | 无 CHANGELOG | 新成员/下游团队看不见变更历史 |

### 建议方案

1. **Goreleaser 管道**：新增 `.goreleaser.yaml` 配置，在 CI 中通过 `goreleaser release` 自动生成：
   - 交叉编译的二进制（linux/amd64, linux/arm64）
   - 自动注入版本号（通过 `ldflags`）
   - 发布到 GitHub Releases
   - 生成 SBOM（CycloneDX 格式）

2. **Docker 镜像构建与发布**：
   - 新增多阶段 `Dockerfile`（builder → distroless 最小镜像）
   - 标签策略：`{version}`, `{major}.{minor}`, `latest`（仅 stable 分支）
   - CI 中推送到 `ghcr.io` 或 `docker.io`

3. **版本策略**：
   - 采用 SemVer（主版本.次版本.补丁）
   - `version` 通过 `git tag` 注入，不硬编码在 config 中
   - 主版本 = 破坏性 API 变更，次版本 = 新功能，补丁 = bugfix

4. **CHANGELOG 自动化**：
   - 使用 `git cliff` 或 `release-drafter` 从 Conventional Commits 生成
   - 每个 PR 必须使用 Conventional Commit 前缀（`feat:`、`fix:`、`chore:` 等）

### 改动量估算

| 组件 | 工作量 |
|------|--------|
| `.goreleaser.yaml` 配置 | 低（半天） |
| 多阶段 `Dockerfile` | 低（半天） |
| CI release job | 低（半天） |
| SemVer 标签策略 + ldflags | 低（半天） |
| CHANGELOG 自动化 | 低（半天） |
| **总计** | **~2-3 天** |

### 优先级

**P1** — 在引入外部消费者之前必须完成。没有版本化发布，就无法做 API 版本管理、回滚、或合规审计。

---

## 方向二：可观测性成本治理（Observability Cost Governance）

### 为什么需要它

aero-id 已经实现了完善的 RED 指标、OpenTelemetry 追踪、结构化日志（`go.uber.org/zap`）。但 **没有任何文档或机制控制可观测性的成本**。随着流量增长和实例数增加，监控基础设施的成本会非线性增长——这是所有生产中大型 Go 服务的常见痛点。

### 当前证据

**全局无差别采样：**

```go
// internal/pkg/tracing/tracing.go
type Config struct {
    Enabled     bool
    SampleRatio float64 // 0 或 >=1 => 始终采样；否则 parent-based
}

// 当 SampleRatio = 0 或 >=1 时，所有请求都被采样
// 没有按端点/按错误类型的差异化采样策略
```

**指标标签无 Cardinality 控制：**

```go
// internal/middleware/metrics.go
func Metrics(reg *metrics.Registry) gin.HandlerFunc {
    return func(c *gin.Context) {
        start := time.Now()
        c.Next()
        reg.ObserveHTTP(
            c.Request.Method,
            c.FullPath(),      // ← 完整路径：/v1/files/:id, /v1/messages/:id, ...
            statusCode,        // ← 每个 HTTP 状态码都成标签
            time.Since(start),
        )
    }
}
```

```go
// internal/handler/grpc/server.go（MetricsInterceptor）
func MetricsInterceptor(reg *metrics.Registry) grpc.UnaryServerInterceptor {
    return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
        start := time.Now()
        resp, err := handler(ctx, req)
        reg.ObserveGRPC(info.FullMethod, status.Code(err).String(), time.Since(start))
        // ↑ 方法名 + 每个 gRPC 状态码做标签
        return resp, err
    }
}
```

当前标签组合的基数估算：

| 标签维度 | 基数 | 说明 |
|---------|------|------|
| HTTP 方法 | ~5 | GET/POST/PUT/DELETE/PATCH |
| HTTP 路径 | ~20 | `/v1/files`, `/v1/files/:id`, `/v1/messages`, ... |
| HTTP 状态码 | ~10 | 200, 400, 401, 403, 404, 429, 500... |
| gRPC 方法 | ~41 | 全部 41 个 RPC |
| gRPC 状态码 | ~6 | OK, InvalidArgument, Unauthenticated, Internal... |
| **总计标签组合** | **1000+** | 无基数控制时，Prometheus 存储成本随时间线性增长 |

**日志无采样/限流：**

```go
// internal/middleware/access_log.go
func AccessLog(log *zap.Logger) gin.HandlerFunc {
    return func(c *gin.Context) {
        start := time.Now()
        c.Next()
        // 每个请求都记录
        log.Info("request",
            zap.String("method", c.Request.Method),
            zap.String("path", c.Request.URL.Path),
            zap.Int("status", c.Writer.Status()),
            zap.Duration("latency", time.Since(start)),
        )
    }
}
```

在 1000 req/s 的生产流量下，这条日志线产生 **86,400,000 条/天** 的日志。无采样策略则日志成本（存储 + 传输 + 查询）与流量线性正比。

### 建议方案

#### A. 追踪采样策略

将单一 `SampleRatio` 替换为分层采样配置：

```yaml
tracing:
  enabled: true
  # 默认采样率（对所有未匹配以下规则的请求）
  default_sample_ratio: 0.01  # 1%
  # 按端点覆盖
  rules:
    - method: "/(v1|grpc)/*"
      error_only: true          # 失败请求 100% 采样
      sample_ratio: 1.0
    - method: "/healthz"
      sample_ratio: 0.0         # 健康检查不追踪
    - method: "/v1/messages"
      sample_ratio: 0.1         # 消息端点 10% 采样
```

#### B. 指标 Cardinality 上限

```go
// internal/pkg/metrics/metrics.go
type RegistryConfig struct {
    MaxCardinality  int     // 每个指标最大标签组合数，超过则报警
    EnableProtection bool   // 当 cardinality 超限时丢弃新标签组合
}
```

- 监控 Prometheus 中每个 `aeroid_*` 指标的标签组合数
- 设置 `MaxCardinality` 硬限，防止意外引入高基数标签
- 在 CI 中追踪指标注册时的标签数变化

#### C. 访问日志采样

```go
// internal/middleware/access_log.go
type AccessLogConfig struct {
    SampleRatio  float64          // 0-1: 日志采样率（1 = 全部记录）
    SlowOnly     time.Duration    // 只记录超过此阈值的慢请求
    ErrorOnly    bool             // 只记录错误响应（status >= 400）
}
```

生产推荐：`sample_ratio: 0.1, slow_only: 500ms`——记录 10% 的请求 + 所有慢请求。

#### D. 成本可见性

新增 `/v1/admin/observability-cost` 端点（权限 `audit.read`）返回：
- 当前指标数量 / 标签组合数 / 估算存储占用
- 当前 span / trace 数量
- 当前日志量（条/秒/天）

### 变动量估算

| 组件 | 工作量 |
|------|--------|
| 追踪分层采样 | 中（2 天） |
| 指标 Cardinality 控制 | 中（2 天） |
| 访问日志采样 | 低（1 天） |
| 成本可见性端点 | 低（1 天） |
| **总计** | **~6 天** |

### 优先级

**P2** — 在流量增长到规模化之前完成。前期按需投入，监控成本增速；当指标组合数 > 5000 或日日志量 > 10GB 时升级为 P1。

---

## 方向三：优雅降级契约（Graceful Degradation Contract）

### 为什么需要它

aero-id 有三个外部依赖（Postgres、SSO、S3/本地存储），每个都可能以不同的失败模式降级。当前系统有 **依赖级别的弹性**（SSO 断路器、缓存降级），但 **没有面向 API 端点的降级文档和策略**——即每个端点在不同依赖故障下的行为没有明确定义。

### 当前证据

**降级行为散落在代码中：**

```go
// internal/sso/client.go — SSO 有断路器 + 过期缓存 + 单飞
func (c *Client) ValidateToken(ctx context.Context, token string) (string, error) {
    // ✅ 缓存命中 → 立即返回
    // ✅ 缓存未命中 + 断路器 OPEN 且有陈旧缓存 → 降级返回
    // ✅ 缓存未命中 + 断路器 HALF-OPEN → 探针请求
    // ⚠️ 但：缓存未命中 + 断路器 OPEN 且无陈旧缓存 → 硬失败
}
```

```go
// internal/service/notification.go
func (d *NotificationDispatcher) Handle(ctx context.Context, e *outbox.Event) error {
    // SSO 不可用时通知照常发送（只依赖 SSO GetUsers 来 enrich 用户信息——但不是必需路径）
    // 但如果 DB 不可用？通知行写入失败 → relay 重试
}
```

**缺少统一的降级矩阵：**

当前代码没有一份文档声明：
- `GET /v1/files/:id` 在 DB 不可用时返回什么？
- `POST /v1/messages` 在 SSO 不可用时返回什么？
- `GET /v1/files/:id/url` 在 S3 不可用时返回什么？
- `/healthz` 和 `/readyz` 在 DB/SSO 不可用时应该返回什么？
- outbox relay 在 DB 不可用时应该怎样表现？

**连接池枯竭时无降级：**

```go
// internal/app/app.go — DB 连接池配置
gdb, err := db.Open(db.Options{
    MaxOpenConns:    cfg.Database.MaxOpenConns,   // 50
    MaxIdleConns:    cfg.Database.MaxIdleConns,   // 10
    ConnMaxLifetime: cfg.Database.ConnMaxLifetime, // 1h
})
// 当 50 个连接全部被占用时，第 51 个请求阻塞等待
// 没有降级读路径（如从缓存读取）、没有连接池等待超时、没有返回 503
```

### 建议方案

#### A. 定义降级矩阵

建立一份正式的 `docs/DEGRADATION_MATRIX.md`，每行一个 API 端点，每列一个外部依赖：

| API 端点 | Postgres 不可用 | SSO 不可用 | S3/存储不可用 | 行为 |
|---------|----------------|-----------|-------------|------|
| `GET /files/:id` | **503**（无法验证所有权） | **429**（无陈旧缓存时可返回 401） | **200**（元数据来自 DB） | 文件元数据依赖 DB 查询权限 |
| `GET /files/:id/url` | **503** | **429** | **503**（S3 presigned 不可生成） | 预签名 URL 需要存储后端 |
| `GET /files/:id/dl` | **200**（HMAC 已验证，无需 DB） | **200**（无需 Token） | **503** | 公共下载路径不依赖 DB/SSO |
| `POST /messages` | **503** | **429**（无缓存时 401） | N/A | 写路径必须事务性提交 |
| `GET /messages` | **503** | **429** | N/A | 列表依赖 DB |
| `POST /comments` | **503** | **429** | N/A | 写路径必须事务性提交 |
| `GET /search` | **503** | **429** | N/A | FTS 依赖 DB |
| `GET /notifications` | **503** | **429** | N/A | 通知 + 用户 enrich |
| outbox relay | **跳过该轮**（重试） | **继续**（SSO 不是必需路径） | **继续** | relay 必须背压 |

#### B. 连接池治理

```go
type DBBackoff struct {
    // 当连接池全部占用时，读请求可以降级到：
    // 1. 返回陈旧数据（如果实现了 read cache）
    // 2. 返回 503 Service Unavailable
    PoolExhaustedBehavior string // "return_503" | "wait" | "serve_stale"
    PoolWaitTimeout       time.Duration // 等待连接的最大时间（当前默认为无限）
}
```

#### C. 实现降级中间件

```go
// internal/middleware/degradation.go
type DegradationState struct {
    mu         sync.RWMutex
    dbDead     bool
    ssoDead    bool
    storageDead bool
}

func (s *DegradationState) SetDependencyDead(name string) {
    // SSO 断路器打开时设置 ssoDead=true
    // DB 连接 ping 失败时设置 dbDead=true
}
```

利用现有的 `/readyz` 探针结果，构建一份降级状态映射，注入到请求上下文中供 handler 嗅探。

#### D. 用户可见的降级状态

当系统进入降级模式时：
- HTTP 响应增加 `X-Aero-Degraded: sso-stale`、`X-Aero-Degraded: db-readonly` 头
- `/healthz` 返回 `{"status":"degraded","dependencies":{"sso":"stale-cache"}}`
- 客户端可以根据这些头调整行为（如禁用写操作或显示降级提示）

### 改动量估算

| 组件 | 工作量 |
|------|--------|
| 降级矩阵文档 | 低（1 天） |
| 降级状态管理器 | 低（1 天） |
| 各 handler 降级逻辑 | 中（每个端点半天，共 ~5 天） |
| 连接池治理 | 低（1 天） |
| 用户可见降级状态 | 低（1 天） |
| **总计** | **~9 天** |

### 优先级

**P1** — 在生产事故中，降级行为决定 SLA：无降级契约时每个依赖故障都是 P0 事故；有降级契约时 SSO 不可用只是"降级模式"而非"系统不可用"。

---

## 方向四：传输协议加固（gRPC/HTTP Transport Protocol Hardening）

### 为什么需要它

aero-id 运行两个网络传输层（gRPC :9090 + HTTP :8080），但两者的 **传输层配置都使用 Go 默认值**——这些默认值针对通用场景优化，不适合生产级安全要求。这不是功能性缺口，而是安全与可靠性的基础加固。

### 当前证据

**gRPC 服务器——全默认配置：**

```go
// internal/handler/grpc/server.go:92
s := grpc.NewServer(grpc.ChainUnaryInterceptor(interceptors...))
// ↑ 无以下关键选项：
// - grpc.MaxRecvMsgSize(4 * 1024 * 1024)  // 默认 4MB
// - grpc.MaxSendMsgSize(4 * 1024 * 1024)  // 默认 4MB
// - grpc.InitialWindowSize(65535)         // 流控窗口默认 64KB
// - grpc.InitialConnWindowSize(65535)     // 连接级流控窗口默认 64KB
// - grpc.MaxConcurrentStreams(100)        // 无限制！默认 math.MaxInt32
// - grpc.ConnectionTimeout(120 * time.Second) // 默认 2 分钟
// - grpc.KeepaliveParams / EnforcePolicy  // 无防守性 keepalive 策略
// - grpc.MaxHeaderListSize                // 无限制
```

**安全 & 可靠风险分析：**

| 缺失配置 | 风险 | 攻击面 |
|---------|------|-------|
| ⚠️ `MaxRecvMsgSize` 默认 4MB | 大消息（如评论内容含 base64 编码的图片）导致 OOM | DoS（资源耗尽） |
| ⚠️ `MaxConcurrentStreams` 无限制 | 单个客户端可开启数百万流，耗尽服务器内存 | DoS（流洪泛） |
| ⚠️ `ConnectionTimeout` 默认 2min | TCP 连接建立后的慢客户端可以长期占用 | 连接耗尽 |
| ⚠️ `KeepaliveEnforcementPolicy` 未配置 | 客户端可以发送零成本 keepalive 而服务器不做限制 | 资源滥用 |
| ⚠️ `InitialWindowSize` 64KB | 高延迟网络下吞吐量受限 | 性能（非安全） |

**HTTP 服务器——关键配置缺失：**

```go
// internal/app/app.go:255-260
httpServer := &http.Server{
    Addr:         fmt.Sprintf("%s:%d", cfg.Server.HTTP.Host, cfg.Server.HTTP.Port),
    Handler:      router,
    ReadTimeout:  cfg.Server.HTTP.ReadTimeout,   // 15s ✅
    WriteTimeout: cfg.Server.HTTP.WriteTimeout,  // 15s ✅
    // 缺失：
    // - ReadHeaderTimeout            // ✅ 慢速请求头读取攻击防御
    // - MaxHeaderBytes               // ✅ 请求头大小限制
    // - IdleTimeout                  // ✅ HTTP/1.1 keep-alive 资源释放
    // - TLSNextProto / HTTP/2 配置   // ⚠️ 未显式启用/禁用 HTTP/2
}
```

**HTTP 安全风险分析：**

| 缺失配置 | 风险 |
|---------|------|
| ⚠️ `ReadHeaderTimeout` | **Slow Loris 攻击**：客户端缓慢发送请求头，服务器保持连接打开，耗尽 goroutine 池 |
| ⚠️ `MaxHeaderBytes` | 超大请求头填充服务器内存 |
| ⚠️ `IdleTimeout` | keep-alive 连接可能无限期保持，消耗系统资源 |
| ⚠️ HTTP/2 未配置 | Go 默认启用 HTTP/2（当 TLS 时），但无流限制、无帧大小限制 |

### 建议方案

#### A. gRPC 服务器加固配置

```go
// internal/handler/grpc/server.go
import "google.golang.org/grpc/keepalive"

const (
    defaultMaxRecvMsgSize    = 10 * 1024 * 1024  // 10MB（文件上传的 RPC 可以更大）
    defaultMaxSendMsgSize    = 10 * 1024 * 1024  // 10MB
    defaultMaxConcurrentStreams = 100             // 单客户端最多 100 流
    defaultConnectionTimeout = 10 * time.Second
)

var keepaliveEP = keepalive.EnforcementPolicy{
    MinTime:             30 * time.Second, // 30s 内至少一次 keepalive ping
    PermitWithoutStream: false,            // 无活跃流时不允许 ping
}
```

#### B. HTTP 服务器加固配置

```go
// internal/app/app.go
httpServer := &http.Server{
    Addr:              fmt.Sprintf("%s:%d", cfg.Server.HTTP.Host, cfg.Server.HTTP.Port),
    Handler:           router,
    ReadTimeout:       cfg.Server.HTTP.ReadTimeout,
    ReadHeaderTimeout: 10 * time.Second,         // ✅ Slow Loris 防御
    WriteTimeout:      cfg.Server.HTTP.WriteTimeout,
    IdleTimeout:       60 * time.Second,          // ✅ keep-alive 上限
    MaxHeaderBytes:    1 << 20,                   // 1MB 请求头上限
}
```

#### C. 按端点配置消息体大小

文件上传端点需要更大的限制，而 `/comments`、`/messages` 需要更小的限制。利用 Gin 的 `MaxMultipartMemory` 和自定义 middleware：

```go
// internal/handler/http/router.go
func bodySizeLimit(maxBytes int64) gin.HandlerFunc {
    return func(c *gin.Context) {
        c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBytes)
        c.Next()
    }
}
```

路由级别配置：
- `POST /v1/files` → `bodySizeLimit(cfg.Storage.MaxUploadBytes)`（50MB）
- `POST /v1/messages` → `bodySizeLimit(1 << 20)`（1MB）
- `POST /v1/comments` → `bodySizeLimit(1 << 19)`（512KB）

#### D. gRPC 消息大小分区

将当前 FileUpload / PresignUpload RPC 的 max_recv 设置为 50MB，其他 RPC 保持 10MB 或更小。利用 gRPC 的 `grpc.CustomCodec` 或通过单独的 `grpc.ServiceConfig` 实现。

### 改动量估算

| 组件 | 工作量 |
|------|--------|
| gRPC 服务器选项 | 低（半天） |
| HTTP 服务器选项 | 低（半天） |
| 按端点 body size middleware | 低（半天） |
| gRPC 消息大小分区 | 低（半天） |
| 配置项扩展 | 低（半天） |
| **总计** | **~2-3 天** |

### 优先级

**P1** — 这些都是低工作量、高安全收益的配置项。在没有这些保护的情况下直接暴露在生产网络中，系统处于"裸奔"状态。

---

## 方向五：数据全生命周期自动化治理（Data Lifecycle Automation & Governance）

### 为什么需要它

aero-id 的核心承诺是 **"data owned by accounts"**（用户拥有的数据）。但当前系统对数据的整个生命周期缺乏自动化治理——数据从创建到最终删除的每一个阶段都应该有明确策略和自动执行机制。

### 当前证据

**文件有保留策略配置但无强制执行：**

```yaml
# configs/config.yaml
storage:
  gc:
    enabled: false       # 默认禁用！
    retention: 168h      # 7 天
```

GC 默认禁用意味着：**生产环境即使不配置也能正常运行，但数据永不清除**。这是一个配置陷阱——团队可能忘记启用 GC，导致存储无限增长。

**非文件数据无保留策略：**

```go
// migrations/
// 以下表没有对应的 GC/清理机制：
// - messages         → 用户不能批量清理旧消息，只能逐条软删除
// - comments         → 同上，无批量过期
// - notifications    → 永久存储，无自动清理
// - outbox_events    → 仅标记 published，不清理
// - audit_events     → 审计日志永久存储，无轮转
// - idempotency_keys → 有 TTL 和清洁器 ✅
```

**审计日志永久存储：**

```sql
-- migrations/000014_create_audit_log.up.sql
CREATE TABLE audit_events (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    actor_uid   TEXT NOT NULL,
    action      TEXT NOT NULL,
    subject_type TEXT NOT NULL,
    subject_id  TEXT NOT NULL,
    target_uid  TEXT,
    meta        JSONB,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
-- 无 retention 列，无清理索引，无轮转策略
```

**通知表无清理：**

```sql
-- migrations/000009_create_notifications.up.sql
CREATE TABLE notifications (
    id             UUID PRIMARY KEY,
    recipient_uid  TEXT NOT NULL,
    type           TEXT NOT NULL,
    actor_uid      TEXT,
    subject_type   TEXT,
    subject_id     TEXT,
    read_at        TIMESTAMPTZ,
    collapsed_count INT NOT NULL DEFAULT 1,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
-- 无自动清理，已读通知永远不会被删除
```

### 痛点矩阵

| 数据类型 | 当前生存期 | 问题 |
|---------|-----------|------|
| `files` | 软删除 + GC 配置（默认关闭） | 配置陷阱；默认不清理 |
| `messages` | 永久 | 用户不能设自动过期 |
| `comments` | 永久 | 同上 |
| `notifications` | 永久 | 已读通知永久存储 |
| `audit_events` | 永久 | 审计日志无限制增长 |
| `outbox_events` | 永久 | 已发布事件永不清除 |
| `idempotency_keys` | TTL 驱动 ✅ | 唯一有自动清理的表 |

### 建议方案

#### A. 统一生命周期策略引擎

新增配置节：

```yaml
lifecycle:
  enabled: false  # 全局开关

  # 按实体类型配置
  policies:
    - entity: notifications
      # 已读通知：保留 90 天后自动软删除
      read_retention: 90d
      # 未读通知：保留 1 年后自动软删除
      unread_retention: 365d

    - entity: audit_events
      # 审计日志保留 3 年
      retention: 1095d
      # 到期后归档到 S3 而非删除
      archive:
        enabled: true
        bucket: aero-id-audit-archive
        format: parquet

    - entity: outbox_events
      # 已发布事件保留 7 天，死信事件保留 30 天
      published_retention: 7d
      dead_letter_retention: 30d

    - entity: messages
      # 用户级策略通过 API 设置（缺省：永久）
      # 可选：conversation 级别自动过期

    - entity: comments
      soft_deleted_retention: 30d  # 软删除后保留 30 天再硬删除
```

#### B. 每个 entity 一个清理 Job

复用现有的 `outbox.Relay.Start` / `gc.Sweeper.Start` 模式，新增以下后台 Job：

```go
// internal/lifecycle/scheduler.go
type Policy struct {
    Entity     string
    Retention  time.Duration
    BatchSize  int
    Action     string // "soft_delete" | "hard_delete" | "archive"
}

type Scheduler struct {
    jobs []*PolicyJob
}

func (s *Scheduler) Start(interval time.Duration) (stop func()) {
    // 类似 GC sweeper：每个 interval 遍历所有 policy
}
```

每个 Job 的实现模式：
```sql
-- 清理已读通知示例
DELETE FROM notifications
WHERE read_at IS NOT NULL
  AND read_at < NOW() - '90d'::interval
LIMIT 500;
```

#### C. 策略默认值安全设计

- 所有策略默认值必须 **安全保守**（宁可留置也不误删）
- 策略变更必须通过迁移（`ALTER TABLE`）完成，不可热加载
- 新增策略必须默认 **disabled**，直到运维显式启用
- 每次清理执行前写入审计日志

#### D. 用户可查询的治理状态

```go
// 新增端点 GET /v1/admin/lifecycle
// 权限: audit.read
// 返回：
{
  "policies": [
    {
      "entity": "notifications",
      "read_retention": "90d",
      "estimated_rows_to_clean": 142857,
      "last_cleanup": "2026-07-09T12:00:00Z",
      "status": "active"
    }
  ]
}
```

### 改动量估算

| 组件 | 工作量 |
|------|--------|
| 生命周期配置结构 | 低（1 天） |
| 通知清理 Job | 低（1 天） |
| 审计日志归档 Job | 中（2 天，含 S3 归档逻辑） |
| outbox 清理 Job | 低（1 天） |
| 评论/消息批量清理 | 低（1 天） |
| 用户可见治理状态端点 | 低（1 天） |
| **总计** | **~7 天** |

### 优先级

**P2** — 在第一个合规审计或存储成本问题出现之前完成。核心价值是在数据增长自然发生前建立治理机制，而不是在问题出现后被动补救。建议在 GC 启用并稳定运行后开始。

---

## 优先级建议矩阵

| # | 方向 | 类型 | 工作量 | 影响 | 依赖 | 推荐优先级 |
|---|------|------|--------|------|------|-----------|
| 1 | 发布工程与制品生命周期 | 运营 | **低**（2-3 天） | **高** | 无 | **P1** |
| 4 | 传输协议加固 | 安全/可靠 | **低**（2-3 天） | **高** | 无 | **P1** |
| 3 | 优雅降级契约 | 可靠 | **中**（9 天） | **高** | 可观测性就绪 | **P1** |
| 2 | 可观测性成本治理 | 运营/成本 | **中**（6 天） | **中高** | 追踪/指标/日志就绪 | **P2** |
| 5 | 数据全生命周期自动化治理 | 合规/成本 | **中**（7 天） | **中高** | GC 就绪 | **P2** |

---

## 与现有架构的一致性检查

| 方向 | 是否遵循单向依赖 | 是否可增量交付 | 是否向后兼容 | 是否可回滚 |
|------|-----------------|---------------|-------------|-----------|
| 方向一 | ✅ 独立工具链 | ✅ goreleaser 配置可单独提交 | ✅ | ✅ 切换回旧流程 |
| 方向二 | ✅ 配置驱动 | ✅ 逐个组件独立上线 | ✅ 采样降低不影响现有功能 | ✅ 恢复 1.0 配置即可 |
| 方向三 | ✅ middleware 层 + 文档 | ✅ 矩阵文档可先交付；降级逻辑逐个端点做 | ✅ | ✅ 移除 middleware 即可 |
| 方向四 | ✅ server 启动参数 | ✅ 每个选项独立生效 | ✅ 默认值不改变现有行为 | ✅ 回退旧配置 |
| 方向五 | ✅ 新增 `internal/lifecycle/` 包 | ✅ 每个 entity 独立 job | ✅ | ✅ 删除 job + 回滚 migration |

---

## 建议执行顺序

1. **方向四（传输协议加固）** — 半天，高安全收益，零风险
2. **方向一（发布工程）** — 2 天，建立版本化基础，所有后续方向的基础设施
3. **方向三（降级契约）** — 9 天，文档先行（1 天），代码分阶段做
4. **方向二（可观测性成本）** — 6 天，可在流量增长到规模化前随时开始
5. **方向五（数据生命周期）** — 7 天，GC 稳定运行后开始，是合规基线

执行顺序的核心逻辑：先补基础设施缺口（方向四、一），再建弹性契约（方向三），最后优化成本和合规（方向二、五）。
