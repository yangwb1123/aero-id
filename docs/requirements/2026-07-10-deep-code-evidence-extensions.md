# aero-id — 代码级证据驱动的架构演进方向（未覆盖盲区）

> **日期**: 2026-07-10
> **视角**: 资深架构师 / 产品经理
> **方法**: 对 `internal/` 全部 22 个包（120+ 源文件）、22 份迁移 SQL、双传输层 handler、composition root 做逐文件检查，与 `docs/requirements/` 下现有 36 份分析文档做主题交叉比对，定位在所有既有分析中**未被系统性分析**或**仅作为单句提及**的方向。
> **无代码变更**。

---

## 发现方法

1. 扫描 `internal/` 的每一个 Go 源文件，记录"代码已暗示但未系统建立为功能"的模式
2. 对每个模式提取关键词，与 36 份既有分析文档的全部方向标题做交叉比对
3. 筛选出在既有文档中**零系统覆盖**或**仅单句提及（非方向级分析）** 的主题
4. 回扫代码确认代码级证据的完整性和准确性

以下 5 个方向的共同特征：不需要引入新的外部依赖，在当前基础设施上即可构建，但需要的工程投入不低——这也是它们至今未被系统性分析的原因。

---

## 方向一：分布式实时推送缺失 — 多实例 SSE Hub 无声降级

### 代码级证据

| 位置 | 发现 |
|------|------|
| `internal/hub/hub.go:31-45` | `Hub.subs` 定义为 `sync.Map`（类型 `map[string]*uidSubs`），**纯进程内内存**结构 |
| `internal/app/app.go:117` | `sseHub := hub.New()` — 每个进程实例创建自己的独立 Hub。无跨实例共享机制 |
| `internal/app/app.go:148` | `relay.Start(pollInterval)` — 每个实例都启动独立的 outbox relay |
| `internal/hub/adapter.go:12-14` | `Hub.Publish()` 调用 `h.pub(uid, ...)`，仅在本地进程的 `sync.Map` 中查找订阅者 |
| `internal/notification/dispatcher.go:88` | `d.pusher.Publish(n.RecipientUID, ...)` — 推送只发送给本地 Hub 的订阅者 |

### 问题分析

这是一个**多实例部署下的无声数据丢失**问题。

**典型部署拓扑：**
```
                 ┌──────────────┐
                 │  Load Balancer │
                 └──┬────┬────┬──┘
                    │    │    │
              ┌─────┼────┼────┼─────┐
              │     │    │    │     │
              ▼     ▼    ▼    ▼     ▼
           ┌────┐ ┌────┐ ┌────┐     ┌────┐
           │Ins1│ │Ins2│ │Ins3│ ... │InsN│
           │Hub1│ │Hub2│ │Hub3│     │HubN│
           └────┘ └────┘ └────┘     └────┘
```

**事件流（故障场景）：**
1. 用户 A 的 `POST /v1/messages` 请求打到 Instance 1
2. Instance 1 的 `MessageService.Send()` 创建消息行 + outbox 事件（`message.sent`）
3. Instance 1 的 relay 轮询到该事件：
   a. `NotificationDispatcher.Handle()` 创建通知行 ✅（数据库写入成功）
   b. `d.pusher.Publish(recipientUID, "message.received", payload)` → Hub1
4. 用户 B（消息接收者）的 SSE 连接在 Instance 2 上 (`/v1/events`)
5. ⚠️ Hub1 上没有用户 B 的订阅者 → **推送被静默丢弃**
6. 用户 B 下一次轮询（或刷新页面）时才能看到新通知

**直接影响：**
- SSE 实时推送在 N 实例部署中的有效送达率 ≈ 1/N（如果粘滞会话未启用）
- 通知**最终**会被用户 B 看到（通过下一次 LIST 请求），但"实时"承诺完全失效
- 无人知道推送被丢弃——无日志、无指标、无告警

### 现有防御的局限

| 机制 | 是否有效 | 原因 |
|------|---------|------|
| 通知行写入 DB | ✅ 有效 | 用户 B 下次查询时看到通知 —— 但"实时"承诺失效 |
| SSE miss 后 JS 定时重连 | 🔶 部分有效 | 浏览器断开后重连可能连到不同实例，只是把问题延迟 |
| Sticky session（LB 粘滞） | 🔶 部分有效 | 需要 LB 配置支持；实例启停/扩缩容时丢失；物理连接断开后无法恢复同一实例 |
| Hub 内的 `select default` 丢弃 | ❌ 无法解决 | 这是慢消费者保护，与跨实例无关 |

### 建议方案

**方案 A — 无状态推送（推荐）**

引入外部 pub/sub channel。当实例创建通知时，将该通知发布到共享 channel；所有实例订阅此 channel，匹配本地的订阅者后推送。

```
Instance 1                            Instance 2
  │                                      │
  ├── NotificationDispatcher              ├── SSE subscriber for User B
  │   ├── DB write ✅                     │
  │   └── Push to Redis channel ────────►  └── Receive from Redis
  │                                      │   → pub(B, "message.received", ...)
  │                                      │   → Hub2 delivers to B's SSE conn
```

Redis Pub/Sub 是最轻量的选择（不需要新的基础设施，大部分部署已有 Redis）。Postgres `LISTEN/NOTIFY` 是零依赖的选择（不需要引入新组件），但可靠性不如 Redis。

**方案 B — 轮询退避 + Sticky Session**

在不引入新组件的前提下，通过 sticky session 和 fallback 轮询来提升推送成功率：
1. Load balancer 配置 sticky session（基于 cookie 或 client IP）
2. SSE 侧增加 fallback 机制：如果推送失败（或一定时间内未收到），回退到定时轮询 `/notifications`

这是"治标不治本"的方案，但成本最低。

### 优先级判断

| 维度 | 评分 |
|------|------|
| 数据正确性影响 | 🔥🔥🔥🔥🔥 — 不是"功能缺失"，是多实例下的正确性 bug |
| 产品体验影响 | 🔥🔥🔥🔥 — 实时通知不可靠，SSE 形同虚设 |
| 触发条件 | 🔥🔥🔥🔥🔥 — 任何多实例部署（包括蓝绿部署）都会触发 |
| 实现成本 | 中-高（Redis pub/sub 或 Pg notify 集成 + 架构重构） |
| **建议** | **P0** — 如果计划多实例部署，这是必须修复的缺陷，不是可选的改进 |

---

## 方向二：后台异步管线的可观测性与 Crash 安全

### 代码级证据

| 位置 | 发现 |
|------|------|
| `internal/outbox/relay.go:86-117` | `drain()` 方法处理一个 batch 的事件。**如果 handler 的 Handle() panic，drain 整个崩溃**——无 recovery 机制 |
| `internal/outbox/relay.go:119-133` | `Start()` 启动的 goroutine 在 `done` 通道关闭后执行一次最终 drain 然后退出。**如果 drain 永久 hang（如 DB 连接池耗尽），goroutine 泄漏** |
| `internal/storage/gc/sweeper.go:135-145` | `Run()` 中的 `purgeSoftDeleted` 和 `sweepOrphans` **如果 panic，整个 sweeper goroutine 崩溃** |
| `internal/storage/gc/sweeper.go:177-186` | `Start()` 启动的 goroutine 在 `done` 关闭后立即退出，**不做最后一次 purge** |
| `internal/hub/hub.go:85-90` | slow consumer 的 `select default` 静默丢弃——**无日志、无 metrics**。运维无法知道通知丢失 |
| `internal/app/app.go:238-250` | `warmup()` 中启动两个 goroutine，**不等待它们完成**，也不记录它们是否崩溃 |
| `internal/handler/http/admin.go:15-60` | 唯一的管理端点是 `GET /admin/audit-log`。**无 dead-letter 事件查询 API**，运维无法看到出栈队列 |
| `internal/outbox/relay.go:36-39` | `MarkFailed` 将超过 `maxRetries` 的事件标记为 `dead_letter=true`——**但无 API/告警**通知运维 |

### 问题分析

aero-id 有三个关键的异步管线：

```
1. Outbox Relay ───→ NotificationDispatcher ───→ Notification row
                 └──→ WebhookHandler ───────────→ External endpoint

2. Storage GC ───→ purgeSoftDeleted ───→ blob deletion
              └──→ sweepOrphans ───────→ orphan detection

3. SSE Hub ─────→ deliver to subscribers
```

这些管线都是：
- **不可观测的**：无法知道管线是否健康、是否落后、是否内存泄漏
- **不可调试的**：dead-letter 事件对运维不可见
- **不安全的**：一个 panic 不会通过 HTTP handler 的 recovery 中间件被捕获，而是直接杀死 goroutine

**具体场景分析：**

**场景 1：Outbox Handler Panic**
```
事件 E 导致 NotificationDispatcher.Handle() panic
→ drain() 没有 recover
→ drain() 返回（或 panic 扩散到 Start() 的 goroutine）
→ 事件 E 未标记为 published, 也未标记为 failed
→ 该 goroutine 死亡
→ relay 永不再 drain 任何事件（因为 goroutine 已经死了）
→ 所有后续的 outbox 事件永久积压
→ 用户不再收到通知
→ ❌ 无人知道（metrics 挂掉后归零也不会触发告警）
```

**场景 2：Storage GC Goroutine 死锁**
```
GC sweeper 调用 store.Delete() 时存储后端 hang
→ sweeper goroutine 阻塞等待
→ 永不再扫描
→ 软删除的行永久不 purge
→ 孤儿 blob 永不被回收
→ 存储持续增长
→ ❌ 无人知道，直到磁盘满
```

**场景 3：SSE Hub 慢消费者**
```
用户 B 的 SSE 连接是慢消费者（网络差）
→ Hub 的 select default 丢弃事件
→ 无日志、无 metrics
→ 用户 B 永远收不到特定通知
→ ❌ 无人知道
```

### 建议方案

#### 1. Pipeline 级恢复与健康状态

为每个异步管线增加：
- 顶级 `recover()` 捕获 panic 并记录 stack trace
- 健康状态暴露（通过 `/readyz` 或 `/metrics`）
- restart 机制（panic 后自动重启 goroutine，而非永久死亡）

```go
// internal/pkg/pipeline/pipeline.go
type Pipeline struct {
    Name    string
    Run     func(ctx context.Context) error
    Restart bool  // 是否在 panic 后自动重启
}

func (p *Pipeline) Start(ctx context.Context, interval time.Duration) (stop func()) {
    for {
        select {
        case <-ctx.Done():
            return
        default:
            func() {
                defer func() {
                    if r := recover(); r != nil {
                        log.Error("pipeline panic", ...)
                        metrics.PipelineCrash(p.Name)
                    }
                }()
                p.Run(ctx) // 执行一次 pass
            }()
            // 等待 interval 或 ctx cancelled
            select {
            case <-time.After(interval):
            case <-ctx.Done():
                return
            }
        }
    }
}
```

#### 2. Dead-letter 事件管理 API

当前 `dead_letter=true` 的事件完全不可见。增加：
- `GET /v1/admin/outbox/dead-letters` — 列出所有死信事件
- `POST /v1/admin/outbox/dead-letters/:id/replay` — 重新投递指定死信
- `POST /v1/admin/outbox/dead-letters/:id/ack` — 确认丢弃

利用已有 `adminRepo` + `outboxRepo`，新增 migration（`outbox_events` 已是通用表，只需查询条件）。

#### 3. Hub 慢消费者可观测性

在 `hub.go` 的 `select default` 处增加：
- 日志记录（logger.Warn + 订阅者标识）
- metrics counter 暴露（`hub_push_dropped_total{uid, event_type}`）
- 可选的订阅者 channel buffer 扩容（从 16 根据历史丢弃率自动调整）

### 优先级判断

| 维度 | 评分 |
|------|------|
| 运维可靠性 | 🔥🔥🔥🔥🔥 — 后台管线坠毁后无告警，是生产级运维的基本缺口 |
| 数据安全 | 🔥🔥🔥🔥 — dead-letter 事件不可见意味着数据可能永久丢失 |
| 实现成本 | 低-中（pipeline 框架 + dead-letter API + hub metrics） |
| **建议** | **P0** — 在上线前完成，建议 pipeline 框架和 dead-letter API 先做 |

---

## 方向三：可恢复大文件上传（Resumable Upload）

### 代码级证据

| 位置 | 发现 |
|------|------|
| `internal/file/service_crud.go:70-120` | `Create()` 方法接受 `io.Reader`，**一次性读取全部内容**。没有分块，没有断点续传 |
| `internal/file/service_upload.go:30-80` | `CompleteUpload()` 通过 `store.Stat()` 验证对象存在后即写入元数据，**不含完整性校验** |
| `internal/storage/storage.go:25-35` | `FileStorage` 接口的 `Put` 签名：`Put(ctx, ownerUID, name, io.Reader) → (key, size, err)`。**没有 offset/chunk 参数** |
| `internal/storage/local.go:60-90` | `local.Put()` 通过 `io.Copy(w, r)` 写入文件。**不记录写入进度**，中断后无法恢复 |
| `internal/storage/s3.go:70-120` | `s3.Put()` 使用 `s3manager.NewUploader` 的 `Upload()`。SDK 支持分块上传，但服务层未暴露此能力 |
| `configs/config.yaml` | `max_upload_bytes: 52428800` — 50MB 上限。超过此大小的文件被直接拒绝，**而非分块传输** |
| `internal/file/handler_http.go:35-70` | `Upload()` handler 通过 `c.FormFile("file")` 读取，**multipart form 不能 stream**，整个文件在 handler 中被缓冲 |

### 问题分析

当前文件上传有两个路径：
1. **代理上传（proxy upload）**：`POST /v1/files` → handler 接收 multipart → service 读完整 reader → store.Put
2. **预签名直传（presigned direct upload）**：`POST /v1/files/upload-url` → 返回 S3 presigned URL → 客户端 PUT → `POST /v1/files/complete`

**两个路径都有致命缺陷：**

#### 路径 1：代理上传

```
客户端 ──POST multipart──→ aero-id ──io.Copy──→ Storage
                             │
                             └── 全文件必须在一次请求中传输
```

- 文件越大，请求超时风险越高（`write_timeout: 15s`，50MB 在低速网络下可能超时）
- 网络中断 → 必须重新上传整个文件
- 服务端内存：即便用 `io.Copy` 流式写入，`c.FormFile()` 在 gin 中也是完整缓冲
- 当前 `max_upload_bytes: 50MB` 是硬上限，超过即拒绝

跨网络验证：上海→新加坡 50MB 文件，平均 15 Mbps → 约 27 秒。服务端 `write_timeout` 默认 **15 秒** → ⚠️ **超时**。即便调整 timeout，网络抖动也会前功尽弃。

#### 路径 2：预签名直传

```
客户端 ──→ presign URL ←── aero-id
  │
  └──PUT object──→ S3/MinIO ──→ POST complete
```

- 客户端直接 PUT 到 S3/MinIO，不经过 aero-id → 不受 `write_timeout` 限制 ✅
- **但 S3 presigned URL 不支持断点续传** → 中断后仍需重新上传整个文件 ❌
- local 后端不支持预签名 → 此路径仅对 S3 有效 ❌

### 建议方案

**引入 TUS 协议兼容的可恢复上传端点。**

TUS（https://tus.io）是开放的可恢复上传协议，核心是对 `PATCH` + `Offset` header 的支持：

```
1. POST /v1/files/upload     → 创建上传，返回 upload_id + Location
2. PATCH /v1/files/upload/:id → 上传/追加数据（带 Upload-Offset header）
3. HEAD /v1/files/upload/:id  → 查询已上传的 offset（断点恢复用）
4. DELETE /v1/files/upload/:id → 取消上传
```

对于 S3 后端，每个 `PATCH` 调用上传一个分块到 S3 的 multipart upload：
```
PATCH /v1/files/upload/uuid
  Upload-Offset: 5242880   ← 前一次中断时已上传 5MB
  Upload-Length: 52428800  ← 总大小 50MB
  Body: [5MB-10MB 的数据]

→ aero-id 调用 S3 UploadPart(PartNumber=2, UploadId=..., data)
```

对于 local 后端，每个 `PATCH` append 到临时文件：
```
→ os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0644)
→ io.Copy(offset=5242880)
```

**为什么不是"已经有 presigned direct upload 就够了"：**

| 场景 | Presigned Upload | TUS Upload |
|------|-----------------|------------|
| 全文件上传（50MB 以下） | ✅ 快（直接到 S3） | 🔶 多一次 request |
| 网络中断后恢复 | ❌ 重新上传全部 | ✅ 从断点继续 |
| 超大文件（500MB+） | ❌ S3 presigned 不支持分块 | ✅ 分块上传 |
| local 后端 | ❌ 不支持 | ✅ 支持 |
| 上传进度反馈 | ❌ 无 | ✅ `Offset` header 即进度 |
| 服务端超时控制 | ✅ 无超时（直连 S3） | 🔶 需要服务端处理超时 |

**实施路径：**
1. 新增 `internal/upload/` 包，定义 TUS 风格的 upload session 模型
2. 新增 `upload_sessions` 表（临时表，上传完成后清理）
3. 在 HTTP handler 中新增 3 个端点：`POST /v1/files/upload`、`PATCH /v1/files/upload/:id`、`HEAD /v1/files/upload/:id`
4. 后端抽象：为 S3 实现 multipart upload，为 local 实现 append-file
5. 上传完成后自动调用现有的 `CompleteUpload` 流程

### 优先级判断

| 维度 | 评分 |
|------|------|
| 用户体验影响 | 🔥🔥🔥🔥🔥 — 大文件上传中断是所有文件服务的核心痛点 |
| 产品差异化 | 🔥🔥🔥🔥 — TUS 是 Dropbox、Google Drive 级别的基础设施 |
| 实现成本 | 高（TUS 协议兼容实现 + multipart upload 集成 + local 后端支持） |
| 当前上限 | 50MB 硬上限，超过则拒绝 —— 限制了文件服务的使用场景 |
| **建议** | **P1-P2** — 如果产品目标是"小文件分享"，可不做；要覆盖大文件场景则必做 |

---

## 方向四：API 生命周期管理与向后兼容性契约

### 代码级证据

| 位置 | 发现 |
|------|------|
| `api/proto/aero_id/v1/aero_id.proto:1-10` | proto 文件在 `v1` 包下，**但 service 名称没有版本**（`FileService`, `MessageService` 等——应为 `FileServiceV1`） |
| `api/proto/aero_id/v1/aero_id.proto:180-210` | 30 个 message 类型使用 proto3 scalar 类型，**所有字段均为 required（proto3 默认）**——新版本无法在不破坏兼容性的情况下添加可选字段 |
| `internal/handler/http/router.go:50-55` | `Routes.Auth` 硬编码为 `/v1` group。**无法在一个进程内同时提供 `/v1/` 和 `/v2/`** |
| `internal/handler/grpc/server.go:40-55` | gRPC server 注册所有 service 在一个 server 上。**没有版本路由**——新老 gRPC 客户端无法同时使用 |
| `internal/handler/grpc/server.go:130-135` | `infraMethod()` 通过 `strings.HasPrefix` 匹配方法名——**如果 proto 包名改变（从 `v1` 到 `v2`），基础设施探测失效** |
| `internal/pkg/errors/errors.go:1-50` | 错误代码（`file.not_found`, `auth.unauthorized` 等）**无版本命名空间**——新版本无法独立演进 |
| `internal/repository/file.go:120-130` | `SearchFiles` 使用原始 SQL `plainto_tsquery('simple', ?)`——**如果未来需要改变搜索行为，新老客户端必须兼容** |
| `internal/handler/grpc/handlers_test.go:1-20` | gRPC handler 测试使用 bufconn **无版本兼容性测试**——无法检测 proto 变更是否破坏老客户端 |
| `.github/workflows/ci.yml` | CI 仅运行 `go test ./...` + `go vet ./...`。**无 proto 兼容性检查**（`buf breaking` 等） |

### 问题分析

当前 API 生命周期管理为零。这是一个**沉默的演进负债**——在服务只有 1 个内部消费者时不明显，但随外部客户增多会变成增长瓶颈。

#### 具体风险

**场景 1：需要废弃一个字段**
```
当前 Message 消息有 `file_id` 字段（字符串类型）。
未来需要支持多个附件：`file_ids`（repeated string）。
→ 不能直接改，因为老客户端解析 proto 会失败（字段类型变了）
→ 不能加 `file_ids` 因为老客户端永远不会发这个字段
→ 只能创建 MessageV2 message，然后所有 consumer 都升级
→ ❌ 当前无版本机制支持这个迁移
```

**场景 2：需要修改一个 gRPC service 的方法签名**
```
当前 Inbox RPC 返回 `(messages, users, next_cursor)`。
未来需要增加 `unread_count` 字段。
→ proto 中增加 `unread_count` 字段是安全的（proto3 的 additive 兼容性）
→ 但 HTTP JSON 响应结构改变后，老客户端解析 `{...}` 时如果遇到新字段不会出错
→ 但如果老客户端**依赖"messages 一定存在"而新版本改了**，就是 break change
→ ❌ 无契约测试来防止这件事
```

**场景 3：HTTP API URL 路径变更**
```
当前 `DELETE /v1/comments/:id` 软删除评论。
未来需要真正的硬删除：`DELETE /v2/comments/:id?hard=true`
→ 不可行：`/v1` 和 `/v2` 共享同一个路由组
→ 当前 `router.go` 只有 `/v1` group，无法在运行中同时提供 v1 和 v2
→ ❌ 所有入口都卡在 `/v1/` 硬前缀后面
```

### 建议方案

#### 1. Proto 包命名版本化

```proto
// api/proto/aero_id/v2/aero_id.proto（而非 v1）
package aero_id.v2;

service FileServiceV2 {  // 替代 FileService
  rpc GetFile(GetFileRequest) returns (GetFileResponse);
}
```

每次非向后兼容的变更：
- 创建新的 `vN` 包
- 老 service 保留在 `v(N-1)` 包中
- gRPC server 同时注册两个版本的 service
- HTTP router 同时挂载 `/v1/` 和 `/v2/`

#### 2. HTTP 版本路由支持

```go
// internal/handler/http/router.go
type Router struct {
    v1 *gin.RouterGroup
    v2 *gin.RouterGroup
}

// 每个 handler 注册时可以指定版本
type RouteRegistrarV1 interface {
    RegisterRoutesV1(r RoutesV1)
}
type RouteRegistrarV2 interface {
    RegisterRoutesV2(r RoutesV2)
}
```

#### 3. Proto 兼容性检查 CI

在 CI 中增加步骤：
```yaml
- name: Check proto breaking changes
  uses: bufbuild/buf-breaking-action@v1
  with:
    against: "https://github.com/aero/aero-id.git#branch=main,ref=HEAD~1,subdir=api/proto"
```

#### 4. error code 版本化

当前错误代码（`file.not_found`）没有版本命名空间。新版本应该使用带版本前缀的错误代码：
```go
// v2 版本
const ErrFileNotFoundV2 = "v2.file.not_found"
```

### 优先级判断

| 维度 | 评分 |
|------|------|
| 架构演进能力 | 🔥🔥🔥🔥🔥 — 没有版本化机制，API 演化是"改了就炸" |
| 外部客户影响 | 🔥🔥🔥🔥 — 如果有外部消费者，break change 是 SLA 违约 |
| 当前阻塞程度 | 🔶 低 — 当前只有一个消费者（自己），但不解决会持续累积债务 |
| 实现成本 | 中（proto 重命名 + router 改造 + CI 集成） |
| **建议** | **P2** — 在上线并有了第一个外部消费者之前解决。注意：改动越晚，需要迁移的工作量越大 |

---

## 方向五：用户级存储配额与用量分析

### 代码级证据

| 位置 | 发现 |
|------|------|
| `internal/service/file.go:72-85` | `Create()` 检查 `s.maxBytes`（单文件上限），但**不检查用户的总已用空间** |
| `internal/repository/file.go:1-40` | `FileRepository` 接口有 15 个方法，但**无 `CountByOwner`、无 `SumSizeByOwner`** |
| `internal/repository/file.go:200-210` | `ListByOwner` 返回分页的文件列表，**但未返回总和/计数/已用空间** |
| `internal/handler/http/file.go:1-60` | `FileHandler` 暴露 12 个端点，**无 `GET /files/usage` 或 `GET /files/stats`** |
| `internal/config/config.go:60-65` | `StorageConfig` 有 `MaxUploadBytes`（单文件上限），**无 `MaxTotalBytes`（总存储配额）** |
| `internal/domain/file.go:1-20` | `File` 结构体**无配额相关字段** |
| `internal/domain/errors.go:1-5` | 领域错误有 `ErrNotFound` 和 `ErrDuplicate`，**无 `ErrQuotaExceeded`** |
| `internal/pkg/errors/errors.go:1-100` | AppError 构造器有 `BadRequest/Unauthorized/Forbidden/NotFound/Conflict/Internal`，**无 `ErrQuotaExceeded` 或类似语义** |

### 问题分析

aero-id 的核心承诺是"data owned by accounts"——数据归用户所有。但这个承诺缺少一个最基本的管理维度：**用户有多少数据？**

#### 缺少的能力

| 能力 | 为什么需要 |
|------|----------|
| **存储配额（hard quota）** | 防止单个用户耗尽全系统的存储资源 |
| **用量警告（soft quota）** | 在达到硬限制前通知用户（"你已经用了 80% 的配额"） |
| **用量分析（usage analytics）** | 管理员需要知道"谁用了多少存储""增长趋势如何" |
| **按用户/按文件的计费数据** | 如果业务需要按用量计费（SaaS 常见模式） |

#### 具体场景

**场景 1：免费层限制**
```
免费用户 100MB 存储上限 → 上传第 3 张照片时拒绝 → "you have exceeded your storage quota"
→ ❌ 当前：连续上传 3 个 50MB 文件，每个检查 `max_upload_bytes`（50MB）→ 通过
→ 但用户的总存储已达到 150MB > 100MB 配额 → 应该拒绝
```

**场景 2：恶意用户存储滥用**
```
攻击者注册多个账户，每个上传 50MB 垃圾文件
→ 每个文件通过单文件大小检查
→ 攻击者在 1 小时内上传 1000 个文件 → 50GB
→ 存储费用爆发
→ ❌ 无配额 → 无防御
```

**场景 3：账单/成本归因**
```
业务需要按客户（uid）维度核算存储成本
→ 需要查询 "用户 A 在 7 月的存储峰值" "用户 B 的总文件大小"
→ ❌ 无聚合统计 → 只能全表扫描
```

### 建议方案

#### 1. 存储用量聚合表

```sql
-- 新增：用户存储用量汇总（读优化，避免实时 SUM 扫描全表）
CREATE TABLE user_storage_usage (
    uid              TEXT NOT NULL PRIMARY KEY,
    total_bytes      BIGINT NOT NULL DEFAULT 0,
    file_count       INTEGER NOT NULL DEFAULT 0,
    soft_quota_bytes BIGINT NOT NULL DEFAULT 0,  -- 0 = unlimited
    hard_quota_bytes BIGINT NOT NULL DEFAULT 0,  -- 0 = unlimited
    updated_at       TIMESTAMPTZ NOT NULL
);
```

每次文件创建/删除时，在同一个事务中更新 `user_storage_usage`：
```go
// 在 fileService.Create() 中：
tx := db.WithContext(ctx).Begin()
defer tx.Rollback()

// 1. 插入文件行
tx.Create(&fileRow)

// 2. 更新用量
tx.Exec(`INSERT INTO user_storage_usage (uid, total_bytes, file_count, updated_at)
         VALUES (?, ?, 1, NOW())
         ON CONFLICT (uid) DO UPDATE SET
           total_bytes = user_storage_usage.total_bytes + ?,
           file_count  = user_storage_usage.file_count + 1,
           updated_at  = NOW()`,
    actor, f.Size, f.Size)

// 3. 在写入前检查 hard quota
var usage struct{ TotalBytes int64 }
tx.First(&usage, "uid = ?", actor)
if usage.TotalBytes > hardQuota {
    return ErrQuotaExceeded  // 回滚事务
}

tx.Commit()
```

#### 2. 配额检查放在 Service 层

```go
func (s *fileService) Create(ctx context.Context, in CreateFileInput) (*domain.File, error) {
    actor := mustActor(ctx)
    
    // 单文件大小检查（已有）
    if s.maxBytes > 0 && in.Size > s.maxBytes {
        return nil, errFileTooLarge
    }
    
    // 配额检查（新增）
    if s.quotaEnforcer != nil {
        if err := s.quotaEnforcer.CheckQuota(ctx, actor, in.Size); err != nil {
            return nil, err
        }
    }
    
    // ... 原有逻辑 ...
}
```

#### 3. Error code 扩展

```go
// internal/pkg/errors/errors.go
func ErrQuotaExceeded(msg string) *AppError {
    return &AppError{
        Code:    "storage.quota_exceeded",
        Message: msg,
        HTTP:    http.StatusInsufficientStorage,  // 507
        GRPC:    codes.ResourceExhausted,
    }
}
```

`507 Insufficient Storage` 是专门为此场景设计的 HTTP 状态码，比 `413 Payload Too Large`（单文件）更语义准确。

#### 4. 用量 API

```go
// HTTP: GET /v1/files/usage
// Response: { "total_bytes": 104857600, "file_count": 42, "quota_bytes": 1048576000 }

// HTTP: GET /v1/admin/users/:uid/storage  (admin only)
// Response: { "total_bytes": ..., "file_count": ..., "quota_bytes": ..., "quota_usage_pct": 42.5 }
```

### 边界情况

| 场景 | 处理 |
|------|------|
| **并发上传** | `user_storage_usage` 在事务中更新，PostgreSQL 的行锁保证序列化。如果两个上传同时到达，后一个等待前一个事务提交后才检查 quota |
| **软删除与硬删除** | 软删除时暂不释放配额（文件可恢复）；硬删除 / 过期 purge 时才从用量中扣除 |
| **分享文件是否计入** | 分享的文件只计入 owner 的配额，不计入被分享者。文件分享不复制 blob |
| **配额变更** | 管理员降低某个用户的配额，该用户当前已超限 → 不再允许上传（现有文件不受影响） |
| **计费级用量追踪** | 使用 `user_storage_usage.historical` 或单独的 `storage_usage_log` 表保留每日快照用于计费 |

### 优先级判断

| 维度 | 评分 |
|------|------|
| 防滥用价值 | 🔥🔥🔥🔥🔥 — 无限存储 = 无限成本，是所有 SaaS 产品的基础门槛 |
| 产品合理性 | 🔥🔥🔥🔥🔥 — "数据归用户所有"但没有"用户有多少数据"的 API——不合理 |
| 实现成本 | 中（新 migration + service 逻辑 + 聚合表 + error code + API） |
| **建议** | **P0** — 上线前必须完成。这是一个运营安全性（operations safety）问题，不是功能选择 |

---

## 总结：优先级排序

| # | 方向 | 优先级 | 成本 | 类型 | 如果不做 |
|---|------|--------|------|------|---------|
| 1 | **分布式实时推送** | **P0** | 中-高 | 正确性缺陷 | 多实例部署下 SSE 实时推送完全不可靠 |
| 2 | **后台管线可观测性** | **P0** | 低-中 | 运维安全 | 后台 goroutine 坠毁后无人知晓 |
| 3 | **存储配额与用量** | **P0** | 中 | 运营安全 | 存储成本无上限，恶意用户可耗尽资源 |
| 4 | **可恢复大文件上传** | **P1-P2** | 高 | 产品能力 | 50MB 上限 + 上传中断重传 |
| 5 | **API 生命周期管理** | **P2** | 中 | 架构演进 | 外部消费者越多，演进成本越高 |

**建议实施顺序：** P0 方向在上线前完成，P1-P2 根据产品路线图的定位决定。如果 aero-id 定位为"轻量文件分享服务"，可恢复上传优先级可降；如果定位为"企业数据平台"，则 P1 方向需提前。
