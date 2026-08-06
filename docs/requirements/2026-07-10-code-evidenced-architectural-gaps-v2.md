# aero-id — 第九轮全量代码扫描：五个高价值扩展方向

> **日期**: 2026-07-10  
> **视角**: 资深架构师 / 产品经理  
> **方法**: 对 `internal/` 全部 45+ 个 Go 包（~180 源文件）、22 份迁移 SQL、`cmd/`、`configs/`、`api/proto/`、`Makefile`、`deployments/` 做逐文件通读，与 `docs/requirements/` 下 **67 份既有分析文档**及 `docs/REVIEW_*` 下 **8 份审查文档**做逐方向交叉比对，确认以下 5 个方向在全部既有分析中**零系统性覆盖**。  
> **约束**: 不编写任何代码。每个方向附精确代码级证据。  
> **前置条件**: `feat/roadmap` 已合并；全部 22 份迁移已交付；`make test && make lint` 绿灯。

---

## 交叉验证方法

```
输入:
  - 67 份 docs/requirements/*.md   → ~90+ 个方向标题
  -  8 份 docs/REVIEW_*.md         → ~24 个方向
  -  1 份 ROADMAP.md               →  5 个方向
  -  1 份 docs/specs/design.md     →  若干 out-of-scope 项
  ---------------------------------------------------------
  合并去重                         → ~100+ 独立方向
  逐方向标题与核心论据交叉比对       → 定位零覆盖的 5 个方向
```

每个方向确认：在所有既有分析中，既无标题级匹配，亦无系统性正文分析。

---

## 五个方向总览

| # | 方向 | 类别 | 优先级 | 代码证据维度 |
|---|------|------|--------|------------|
| 1 | **CORS 默认允许所有来源且无生产锁定路径** | 安全 / 配置 | **P1** | `config/config.go` 无 CORS 字段；`router.go` 的 `CORSOrigins` 恒为空；`cors.go` 空 origin 列表=通配符 |
| 2 | **文件分享不产生任何 Outbox 事件——受让方零通知** | 功能完整性 / UX | **P1** | `file_share.go` 两栈实现均不写 outbox；`outbox.go` 无 `file.share` 事件类型；受让方只能靠轮询发现新共享 |
| 3 | **gRPC 传输层完全无幂等性支持** | API 契约 / 可靠性 | **P1** | `middleware/idempotency.go` 仅 Gin HTTP；gRPC 无等价拦截器；`idempotency/store.go` 仅被 HTTP 路径引用 |
| 4 | **幂等键预留超时硬编码为 2 分钟——不可配置** | 运维 / 弹性 | **P2** | `idempotency/store.go:22` `const reservationTTL = 2 * time.Minute` 纯硬编码；无 config/env 覆盖 |
| 5 | **SSE Hub 无连接准入控制与慢消费者背压策略** | 资源管理 / 生产健壮性 | **P2** | `hub.go:68` chan buffer=16；`hub.go:149-153` 静默丢弃；`events.go` 无连接数限制、无限速、无到期 |

---

## 方向一：CORS 默认允许所有来源且无生产锁定路径

### 问题陈述

aero-id 的 CORS 中间件在未配置允许来源列表时，行为是向所有跨域请求返回 `Access-Control-Allow-Origin: *`。当前生产配置 `configs/config.yaml` 中**没有任何 CORS 配置项**，配置结构体 `HTTPConfig` 也**没有任何 CORS 相关字段**。这意味着：

1. 开发环境：`*` 通配符——无安全问题，符合预期。
2. 生产环境：仍为 `*` 通配符——**任何外部网站均可发起跨域 fetch 到 aero-id API**。

对于带 Bearer token 认证的路由，这是有限的攻击面（token 不会自动附带于跨域请求）。但对于 `/healthz`、`/readyz`、`/metrics` 等无认证端点，以及浏览器端 SPA 调用场景，无法将来源锁定到特定域名是一个真实的安全运营缺口。

### 代码级证据

**证据 1：`HTTPConfig` 无 CORS 字段**

`internal/config/config.go:71-78`:
```go
type HTTPConfig struct {
    Host            string        `mapstructure:"host"`
    Port            int           `mapstructure:"port"`
    ReadTimeout     time.Duration `mapstructure:"read_timeout"`
    WriteTimeout    time.Duration `mapstructure:"write_timeout"`
    ShutdownTimeout time.Duration `mapstructure:"shutdown_timeout"`
    IdempotencyTTL  time.Duration `mapstructure:"idempotency_ttl"`
    RequestTimeout  time.Duration `mapstructure:"request_timeout"`
}
```
没有 `CORSOrigins`、`AllowedOrigins` 或类似的字段。

**证据 2：`CORSOrigins` 永不从 config 读取**

`internal/handler/http/router.go:26-57` 定义 `RouterConfig` 时字段存在，但 `internal/app/app.go:198-210` 构造 `httpapi.RouterConfig{}` 时**未传递任何 CORS 相关值**——`CORSOrigins` 保持零值（空切片）。

**证据 3：空 origin 列表 = 通配符**

`internal/middleware/cors.go:18-22`:
```go
allowAll := len(origins) == 0
// ...
if allowAll {
    c.Header("Access-Control-Allow-Origin", "*")
}
```

### 边界情况

- 当 `Origin` 请求头不存在时，CORS 中间件不设置任何响应头——此时无问题。
- 当 `Origin` 存在且为不允许的来源时，真实请求（非 OPTIONS）通过但不带 CORS 头——浏览器会拦截响应。
- 当 `origin` 存在且当前为通配符模式时，浏览器信任所有来源——攻击面。

### 建议

在 `HTTPConfig` 或 `ServerConfig` 中增加 `allowed_origins []string` 配置项，传递到 `RouterConfig.CORSOrigins`。当数组为空时保持当前 permissive 行为（向后兼容），当非空时严格锁定。配置文件中默认保持空（dev 友好），生产部署时运维人员设为白名单。

---

## 方向二：文件分享不产生任何 Outbox 事件——受让方零通知

### 问题陈述

当前实现中，当用户 A 将文件分享给用户 B：
- `ShareFile()` 写入 `file_shares` 表 ✅
- 审计日志记录 `file.share.grant` ✅（当 audit logger 启用时）
- 受让方 B **零通知** ❌

受让方只有在**主动调用** `GET /v1/files/shared-with-me` 时才能发现有人分享文件给自己。这严重损害了文件分享功能的用户体验，也使系统事件流不完整。

对比消息系统：当用户 A 发送消息给用户 B，outbox 发出 `message.sent` 事件 → `NotificationDispatcher` 创建 `message.received` 通知 → SSE 实时推送。文件分享完全没有走这个路径。

### 代码级证据

**证据 1：旧栈 `service/file_share.go` 无 outbox 调用**

```go
// internal/service/file_share.go 全文件约 60 行
// ShareFile() 调用 s.shares.Grant() + s.audit.Log()，零 outbox 引用
// UnshareFile() 调用 s.shares.Revoke() + s.audit.Log()，零 outbox 引用
```

搜索 `internal/service/file_share.go` 和 `internal/service/file.go` 中 `outbox` 关键字：**零命中**。

**证据 2：新栈 `file/service_share.go` 同样无 outbox**

```go
// internal/file/service_share.go 全文件约 70 行
// 同样的模式：s.shares.Grant() + s.audit.Log()，零 outbox
```

**证据 3：Outbox 事件类型枚举中无文件分享相关类型**

`internal/outbox/outbox.go` 中 `EventType` 是自由字符串——但所有已使用的事件类型为 `message.sent`、`message.read`、`comment.replied`。`file.share` 从未注册，也没有处理它的 `notificationFor` 分支（见 `internal/service/notification.go:228-260` 的 `switch e.EventType`）。

**证据 4：`notificationFor` 的 switch 仅处理三种事件**

```go
switch e.EventType {
case "message.sent":
case "message.read":
case "comment.replied":
default:
    return nil // nothing to notify
}
```

### 边界情况

- 批量分享（`ShareFile` 接受 `granteeUIDs []string`）应产生 N 条独立通知。
- 取消分享（`UnshareFile`）在已有通知已送达的情况下不应新建通知（或发送"已取消"通知？需产品决策）。
- 分享给自己（`grantee == owner`）应被跳过（当前代码已跳过）。

### 建议

在 `file.service.ShareFile()` 的成功路径末尾追加一个 outbox 事件写入（通过一致的 `outbox.Event` 定义），并在 `NotificationDispatcher.notificationFor` 中增加 `"file.shared"` 处理分支。事件 payload 应包含 `{file_id, owner_uid, grantee_uid, file_name}` 以便生成有意义的通知文案。

---

## 方向三：gRPC 传输层完全无幂等性支持

### 问题陈述

HTTP 传输层通过 `middleware.Idempotency` 中间件和 `Idempotency-Key` 请求头实现了写操作幂等性。但 gRPC 传输层**完全没有等价机制**。这意味着：

- HTTP 客户端重试是安全的（幂等键防止重复）。
- gRPC 客户端（通常是内部微服务或系统组件）重试可能产生重复资源。

对于主要面向外部客户端的 HTTP API，幂等性已覆盖。但对于内部流量（如果 gRPC 端口暴露给多个服务），重复写入是一个真实风险。更关键的是，**幂等性应该是基础设施级特性**，不应因传输协议不同而缺席。

### 代码级证据

**证据 1：幂等拦截器仅适用于 Gin HTTP**

`internal/middleware/idempotency.go` 实现为 `gin.HandlerFunc`，依赖 `gin.Context` 的 `GetHeader`、`FullPath`、`GetString`、`AbortWithStatusJSON` 等方法——无法在 gRPC 拦截器中使用。

**证据 2：gRPC 服务器无幂等拦截器**

`internal/handler/grpc/server.go` 的拦截器链：
```go
interceptors := []grpc.UnaryServerInterceptor{
    RecoveryInterceptor(log),
    AuthInterceptor(validator),
}
if o.limiter != nil {
    interceptors = append(interceptors, RateLimitInterceptor(o.limiter))
}
if reg != nil {
    interceptors = append([]grpc.UnaryServerInterceptor{MetricsInterceptor(reg)}, interceptors...)
}
interceptors = append([]grpc.UnaryServerInterceptor{TracingInterceptor()}, interceptors...)
```
**无幂等拦截器**。`ChainUnaryInterceptor` 只注册了 recovery、auth、ratelimit、metrics、tracing。

**证据 3：`Store` 接口仅被 HTTP 引用**

`internal/idempotency/store.go` 的 `Store` 接口的引用来源搜索：仅在 `internal/middleware/idempotency.go`、`internal/handler/http/router.go`、`internal/app/app.go` 中出现。**gRPC 包零引用**。

**证据 4：proto 定义无幂等键字段**

`api/proto/aero_id/v1/aero_id.proto` 中的每个 RPC 请求消息**均无 `idempotency_key` 等价字段**。

### 边界情况

- gRPC 幂等键的传输方式：可将幂等键放在 gRPC metadata 中（如 `idempotency-key` 元数据项），拦截器从中读取。
- 幂等范围：HTTP 是 `(uid, endpoint, key)`，gRPC 应为 `(uid, full_method, key)`。
- 幂等键过期：复用 HTTP 的 TTL 配置。

### 建议

1. 在 `internal/handler/grpc/server.go` 中增加 `IdempotencyInterceptor`，从 gRPC metadata 读取 `idempotency-key`，调用 `store.Reserve/Complete/Release`。
2. 基础设施方法（health、reflection）应跳过幂等检查。
3. proto 层不需要修改——幂等键通过 metadata 传递，不耦合于 payload schema。

---

## 方向四：幂等键预留超时硬编码为 2 分钟——不可配置

### 问题陈述

`internal/idempotency/store.go` 中定义了一个包级别常量 `reservationTTL = 2 * time.Minute` 来控制幂等键预留的过期时间。这个值有四个问题：

1. **纯硬编码**：没有任何 config 读取点或环境变量覆盖机制。
2. **跨部署场景不可调**：高延迟场景（跨地域部署、慢速 SSO）可能需要更长的预留窗口；低延迟场景（同机房毫秒级）可缩短以减少冲突窗口。
3. **运行时不可见**：运维人员无法通过 `/metrics` 或日志了解当前预留超时设置。
4. **预留冲突行为不可控**：当预留过期且另一个请求恰好到达时，两个请求都可能认为自己获得了执行权。

### 代码级证据

**证据 1：硬编码常量**

`internal/idempotency/store.go:22`:
```go
// reservationTTL bounds how long an in-progress reservation is honored before it is
// considered stale (its leader likely crashed) and may be reclaimed by a retry.
const reservationTTL = 2 * time.Minute
```

该常量仅在 `store.go:107` 的 `Reserve` 方法中使用一次。

**证据 2：无 config 映射**

`internal/config/config.go` 的 `HTTPConfig` 中有 `IdempotencyTTL time.Duration`（控制已完成响应保留多久），但**没有** `IdempotencyReservationTTL`。

**证据 3：已有 TTL 配置项但未扩展**

```go
// internal/config/config.go:75
IdempotencyTTL  time.Duration `mapstructure:"idempotency_ttl"` // 已完成响应的保留时长
// 缺少：IdempotencyReservationTTL  // 预留超时（当前硬编码 2 分钟）
```

### 边界情况

- 预留超时过短（<1s）：高并发下 leader 尚未完成但已超时，followers 开始竞争→双重写入风险。
- 预留超时过长（>10min）：leader 崩溃后预留占用时间过长，客户端重试窗口过宽。
- 预留超时与 idempotency TTL 的关系：reservationTTL 应远小于 idempotencyTTL（由设计保证，2min vs 24h）。

### 建议

在 `HTTPConfig` 中增加 `idempotency_reservation_ttl` 配置项（默认 2m，向后兼容），传递到 `idempotency.Store`（接口或构造函数参数）。当为 0 时使用当前硬编码值。

---

## 方向五：SSE Hub 无连接准入控制与慢消费者背压策略

### 问题陈述

aero-id 的 SSE （Server-Sent Events）实时推送系统通过 `internal/hub.Hub` 实现，为每个 uid 维护一组订阅者 channel。当前实现存在以下生产就绪缺口：

1. **无连接数量限制**：一个用户可以打开无限个 SSE 连接（同一个 uid 可多次调用 `GET /v1/events`），每个连接消耗一个 goroutine 和一个 chan。
2. **无慢消费者保护**：每个 subscriber channel 缓冲区仅 16 个事件。当 SSE 客户端消费速度慢于事件生产速度时，**事件被静默丢弃**（`select default: drop`）。
3. **无连接保活探测**：客户端断开后，server 端的 goroutine 仅在尝试写入已关闭 channel 时（通过 `c.Request.Context().Done()`）才能感知断开——但 `context.Done()` 的检测依赖 `select` 循环中的 `<-ticker.C` 先触发。
4. **无 per-connection 身份验证刷新**：SSE 连接建立时验证 token，但连接生命周期内（无固定最大时长）token 可能已过期。

### 代码级证据

**证据 1：channel 缓冲区仅 16，无背压**

`internal/hub/hub.go:68`:
```go
ch := make(chan SSEEvent, 16)
```

**证据 2：慢消费者数据被静默丢弃**

`internal/hub/hub.go:149-153`:
```go
select {
case ch <- evt:
default:
    // subscriber is too slow; drop rather than block
}
```
无日志、无指标、无告警。管理员无法得知推送丢失。

**证据 3：SSE handler 无连接准入控制**

`internal/handler/http/events.go:57`:
```go
func (h *EventsHandler) Stream(c *gin.Context) {
    uid, ok := service.ActorFrom(c.Request.Context())
    // ... 直接 subscribe，无限额检查
    sub, unsub := h.hub.Subscribe(uid)
```
无 per-uid 并发连接数限制、无全局连接数限制、无 per-client IP 限流。

**证据 4：事件失丢零可观测性**

`internal/hub/hub.go` 无任何计数器或日志——无法从 `/metrics` 或日志中得知有多少事件被丢弃。

### 边界情况

- 恶意用户打开 1000 个 SSE 连接 → 1000 个 goroutine + channel，server 内存压力。
- 移动端设备频繁断连重连 → channel 快速创建和销毁，`sync.Map` 无清理机制（`uidSubs` entry 在 `subs` 为空时会 `Delete`，但 `sync.Map` 的 `Delete` 不回收内存映射）。
- 滚动部署时所有 SSE 连接断开 → 客户端重连后通过 `last_event_id` 恢复，但 50 条的限制可能导致大量重连下的 Notif 重复。

### 建议

- 在 `hub.Subscribe` 中增加 per-uid 连接数上限（可配置，默认 5-10）。
- 为丢弃事件增加日志（至少 WARN 级别）和计数器指标。
- 增加 per-connection 最大生命周期（默认 24h），强制客户端重新认证。
- 将 chan buffer 从固定 16 改为配置化，或实现有界丢弃+日志的有损队列策略。

---

## 总结：五个方向的价值与投入

| 方向 | 问题类型 | 当前风险 | 投入估算 | 价值 |
|------|---------|---------|---------|------|
| CORS 生产锁定 | 安全配置 | 中：外部网站可跨域访问无认证端点 | 低：~1 天（config+router 改动） | 移除生产安全盲区 |
| 文件分享通知 | 功能缺失 | 高：核心功能的 UX 不完整 | 中：~2 天（outbox 事件+dispatcher 分支） | 使用户即时感知文件分享 |
| gRPC 幂等性 | API 契约 | 中：内部重试可产生重复 | 中：~2 天（拦截器+metadata 读取） | 填补协议层一致性缺口 |
| 幂等预留 TTL 配置化 | 运维弹性 | 低：目前 2min 对多数场景合理 | 极低：~数小时（config 字段+参数透传） | 消除硬编码，适应多种部署 |
| SSE Hub 生产加固 | 资源控制 | 中：无保护下可被资源耗尽 | 中：~3 天（连接限数+日志+配置化 buffer） | 防止连接滥用，提供可观测性 |

**建议实施顺序**：方向一（CORS）与方向二（文件分享通知）优先——前者是安全修正确认，后者是用户可见的功能完整性提升。方向三（gRPC 幂等）紧随其后。方向四（幂等 TTL 配置）和方向五（SSE 加固）属于持续完善，可根据团队节奏安排。
