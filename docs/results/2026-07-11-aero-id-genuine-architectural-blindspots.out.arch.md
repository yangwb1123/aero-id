现在我已对代码库完成全面分析。以下是架构评估。

---

# 架构分析：aero-id —— 并行架构分裂与迁移评估

## 1. 架构评估

### 当前状态（基于逐文件验证）

代码库包含两套 **结构相同、功能重复的架构栈**，它们映射到相同的数据表和领域概念：

| 层次 | 旧架构（存活，已接线） | 新架构（主要部分已死） |
|---|---|---|
| 服务器/组合根 | `internal/app/app.go` | — |
| HTTP 处理器 | `internal/handler/http/{file,message,comment,...}.go` | `internal/{comment,file,message,...}/handler_http.go` |
| gRPC 处理器 | `internal/handler/grpc/{file,message,comment,...}.go` | `internal/{comment,file,message,...}/handler_grpc.go` |
| 业务逻辑 | `internal/service/{file,message,comment,...}.go` | `internal/{comment,file,message}/service.go / service_crud.go / ...` |
| GORM 持久层 | `internal/repository/{file,message,comment,...}.go` | `internal/{comment,file,message}/repository.go` |
| 领域模型 | `internal/domain/{file,message,comment,...}.go` | 重新导出 `internal/{comment,file,message}/comment.go` 等（别名/包装器） |
| 通知分发 | `internal/service/notification.go` | `internal/notification/dispatcher.go` |

**唯一被新架构消费的例外：** `internal/search/` 包导入新领域包的*接口*（`comment.Repository`、`file.Repository`、`message.MessageRepository`）以构建搜索服务——但该新搜索服务本身从未实例化。存在仲裁服务仅在旧架构上构建的 `searchSvc`。

### 架构优势

旧架构拥有一个 **清晰、严格的单向依赖链**（`handler → service → repository → GORM`），并在 `AGENTS.md` 中有明确的约定。组合根（`app.go`）集中管理所有依赖项，没有隐式初始化。每种传输协议（HTTP/gRPC）由同一服务层提供服务——这是一个合理的做法。

### 架构债务

**债务 #1：并行实现。** 旧架构和新架构包含**两组针对相同数据库表的 GORM 查询**，两者均编写了完整的 CRUD 操作。这使每条 SQL 语句都有两个实现版本，埋下了分歧/错误的风险。服务接口也接近重复——具有略微不同的方法集和 DTO 结构体。

**债务 #2：未完成的领域驱动迁移。** 有人开始将聚合逻辑迁移到按领域划分的包（`internal/{comment,file,message,...}/`），但从未将接线从旧架构切换到新架构。所有处理器、大多数服务以及新的通知分发都是**死代码**——它们被编译但从未被实例化。

**债务 #3：上下文键分裂 —— 运行时安全漏洞。** 这是最关键的问题。两个不同的包定义了私有的上下文键类型：

```go
// internal/service/service.go
type ctxKey int; const actorKey ctxKey = iota  // 旧

// internal/domain/actor.go
type ctxKey int; const actorKey ctxKey = iota  // 新
```

HTTP 中间件（`internal/middleware/auth.go`）写入 `service.WithActor()`（旧键）。新的领域服务调用 `domain.MustActor()`（新键）。如果任何路径使用新架构的 `MustActor` 而未修复中间件，**每次调用都会以 401 失败**，因为上下文值通过不同的键无法访问。这是一个静默、零测试覆盖的缺陷——代码通过编译但运行时会损坏。

**债务 #4：`internal/block/` 包活在中间地带。** `internal/notification/dispatcher.go` 导入 `internal/block` 以使用 `block.BlockChecker` 接口。但新分发器从未被实例化——旧分发器（`service.NotificationDispatcher`）直接导入 `repository.BlockRepository`。因此，`internal/block/` 包中的接口是必需的（由新分发器导入），但实现却是死代码。

**债务 #5：搜索服务指针断裂。** `internal/search/search.go` 定义了使用新领域包存储库接口的新 `Service`。但 `app.go` 构造的是旧搜索服务 `service.NewSearchService(msgRepo, commentRepo, fileRepo)`（旧存储库）。新搜索包中的处理程序和实现编译通过但无法使用。如果切换，将出现上下文键不匹配导致运行时失败。

---

## 2. 扩展方向

### 方向 1（P0）：架构统一 —— 删除并线

> **为什么需要：** 两个并行的架构栈使得每条 SQL 语句、每个服务方法、每个 HTTP 端点都有两个实现。这导致维护成本翻倍、逻辑可能产生分歧，并增加了新的安全隐患（上下文键）。在扩展新功能之前必须解决。

#### 核心挑战与技术难点

1. **上下文键统一。** 必须选择其中一个键类型。推荐：**消灭 `service.ctxKey`**，因为 `domain` 包已经是共享实体所在之处，且没有传输层依赖。修复中间件以调用 `domain.WithActor()`。
2. **接口对齐。** 旧服务方法集比新服务更广泛（例如，旧 `FileService` 包含 `PresignUpload`/`CompleteUpload`；旧 `CommentService` 有 `Report`/`Hide`/`Unhide`）。新的领域包接口较窄。必须在保留旧接口完整方法集的同时，将旧实现迁移到新包。
3. **存储库去重。** 两组存储库操作相同的表。必须决定保留哪一组（推荐：新的领域包存储库，因其已按领域划分），并删除旧存储库。
4. **搜索包修复。** 搜索包已经是混合体——它导入新领域接口，但接线使用旧存储库 + 旧服务。统一自然就能解决此问题。

#### 预期的架构变更

- 标准化中间件/拦截器使用 `domain.WithActor()` / `domain.MustActor()`
- 将 `internal/service/{file,message,comment,...}.go` 中的业务逻辑迁移到 `internal/{file,message,comment,...}/service.go`
- 删除 `internal/service/` 目录（或保留仅用于审计/偏好等横切关注点的辅助工具）
- 删除 `internal/repository/` 目录（旧存储库）
- 从 `internal/handler/{http,grpc}/` 重新接线到新领域包处理程序
- 删除 `internal/notification/dispatcher.go` 的死分发器（保留并修复 `internal/service/notification.go` 中的分发器）

#### 对现有系统的影响

**风险极高**——这是一次内爆式的重构，需要零停机切换。两个处理程序集都通过 protobuf 暴露相同的 API，因此客户端协议不变。但接线（`app.go`）必须一次性全部更改。建议通过特性标志或重复路由（新旧端点并存）来分阶段切换。

---

### 方向 2（P1）：数据生命周期 TTL 清理

> **为什么需要：** 已存在存储 GC 扫描器（`internal/storage/gc/sweeper.go`），这是一个领域无关的扫描-清理循环抽象。目前仅用于已软删除的 blob。同样的模式可以扩展到按 TTL 自动过期资源：消息（阅后即焚）、临时文件、过期的通知。

#### 核心挑战

- 数据库模式变更：需要为相关表（messages、files、notifications）添加 `expires_at` 列
- 通用 TTL 清理器可以复用 `storagegc` 的 `Config` + `Run`/`Start` 模式，但需要提取为可在任意表上工作的通用 `TTLCleaner`
- 策略决定：是硬删除还是软删除 + 标记为已过期？

#### 架构变更

- 提取 `internal/pkg/ttlcleaner/`（通用扫描-删除循环）
- 向 `domain.Message`、`domain.File`、`domain.Notification` 添加 `ExpiresAt *time.Time`
- 每个领域包注册自己的清理器
- 支持软删除 + 延迟实际清理，以便在一定窗口内可恢复

---

### 方向 3（P1）：Webhook 订阅（自方向五）

> **为什么需要：** 出站事件基础已存在（`internal/outbox/`），`WebhookConfig` 包含 `EventTypes`、`SigningKey`、`URL`。目前这是一个单个的全局 webhook。多租户 webhook 订阅让外部服务能够接收用户级事件的回调。

#### 核心挑战

- 需要多租户：一张 `webhook_subscriptions` 表（`owner_uid`、`url`、`event_types`、`signing_key`、`is_active`）
- CRUD API（仅限拥有者管理订阅）
- 出站中继需要从仅单端点变为多端点。当前 `relayHandler` 模式（链式）很好，但需要按订阅分叉
- 幂等性、重试、回退

#### 架构变更

- 新增 `internal/webhook/` 包：存储库 + 服务 + 处理程序
- 修改 `internal/outbox/relay.go`：遍历活跃订阅而非单个端点
- 添加 proto/HTTP API 用于订阅管理

---

### 方向 4（P2）：对话管理

> **为什么需要：** 目前，消息按单个最近交互（`internal/message/repository.go` 中的 `ListConversation`）分组在收件箱中。没有显式的 `conversations` 表或 `conversation_id`，这使得以下操作难以实现：按固定线程排序、对话元数据（标题、头像）、群聊、离开/静音。

#### 核心挑战

- 最复杂的数据库迁移：需要 `conversations` 表、向 `messages` 添加 `conversation_id` 外键、回填现有数据
- 对现有 API 的影响：`Inbox`、`Conversation`、`MarkConversationRead`——都需要修改或延长
- 群聊引入完整的参与方模型（与块和通知交互）
- 现有 ACL（双人之间的消息）与多参与方不匹配

#### 架构变更

- 新增 `internal/conversation/` 包
- 将 `conversation_id` 添加到 `domain.Message`
- 迁移：将现有的双人线程回填到隐式对话
- 对旧 API 保持向后兼容——将对话视为现有端点的上层视图

---

### 方向 5（P2）：媒体管线

> **为什么需要：** 文件上传目前只存储原始字节。功能需求是 MIME 类型验证、病毒扫描、缩略图生成、图像/视频转码、CDN 分发。

#### 核心挑战

- 最复杂的方向，需要外部依赖（ImageMagick、FFmpeg、ClamAV）和异步 pipeline
- 验证可以零成本：`http.DetectContentType`（MIME magic bytes），在内存中读取前 512 字节
- 缩略图生成需要后台队列（或纯同步，有超时）
- 缩略图存储：新 blob 前缀（`{storage_key}/thumb_*`）或单独的 bucket
- CDN 集成改变文件服务模型（下载 URL 来自 CDN 而非应用）

#### 架构变更

- 添加 `internal/media/` 管道包
- 管道步骤：验证 → 扫描 → 转码 → 缩略图 → CDN 回源
- 背景：复用 `outbox` 模式或用 `internal/media/pipeline.go` 中的简单 worker goroutine
- 缩略图存储：扩展 `storage.FileStorage`，增加 `Thumbnail()` 方法或单独的存储库 `thumbnail_refs`

---

## 3. 接口设计建议

### 统一接口原则

1. **传输层不可知。** 服务接口不应引用 `gin.Context` 或 `grpc.Server`。目前新旧架构都遵循此原则——应保持不变。
2. **服务返回领域类型，而非传输类型。** 这已实现（旧服务返回 `*domain.File`，新服务返回 `*Comment`，即 `domain.Comment` 的别名）。
3. **选择域名包作为唯一接口宿主。** 每个聚合领域应定义自己的存储库和服务接口（新架构已做对的部分）。旧服务接口在与新接口合并后应被删除。

### 需要新增的抽象层

- **领域事件总线：** 目前 outbox 和通知分发写死在一起。应提取一个 `EventBus` 接口，其注册处理器并异步分派——这样一来，webhook、通知、媒体管线都能订阅而不增加中继的复杂度。
- **通用 TTL 清理器。** 一个重复的定时器模式（`internal/storage/gc/sweeper.go`）。应提取 `internal/pkg/gc/cleaner.go`，提供 `Cleaner` 结构体，参数化为扫描查询和删除策略。
- **权限检查器。** 目前 `adminSvc` 作为可选的 `PermissionChecker` 传递给 gRPC 服务器。应将此字段升级为独立接口，以便跨领域使用一致的角色检查。

### 向后兼容性

- 由于采用了 protobuf 线格式，gRPC API 兼容性得以保持。HTTP API 只要保留路由名称和查询参数就能保持兼容。
- 新领域存储库必须实现旧存储库接口的完整方法集（反之亦然），以便切换步骤可逆。
- **强烈建议：** 创建一个 `internal/adapter/` 包，将旧接口转换为新接口（或反过来），以便分阶段迁移，无需一次性全部切换。

---

## 4. 技术选型

### 新依赖项评估

| 候选 | 用途 | 建议 | 原因 |
|---|---|---|---|
| `http.DetectContentType` | MIME magic bytes 验证 | **采纳** | 零外部依赖，Go 标准库，单行代码 |
| `nfnt/resize` 或 `github.com/disintegration/imaging` | 缩略图生成 | **谨慎采纳** | 纯 Go，无 CGo，许可证宽松。通过 go.mod 固定版本 |
| `FFmpeg` 二进制（exec） | 视频转码 | **暂缓** | 需要运行时依赖，CGo/二进制绑定复杂。等业务用例确认再考虑 |
| `ClamAV`（REST 或 UNIX socket） | 病毒扫描 | **暂缓** | 需要运行中的 ClamAV 守护进程。仅在高安全性部署中需要 |
| `RabbitMQ` / `NATS` | 异步媒体 pipeline 队列 | **暂缓** | 出站机制已足够用于初始场景。用 goroutine + 有界通道即可 |

### 自建 vs 集成

| 功能 | 决策 | 理由 |
|---|---|---|
| MIME 验证 | 自建 | 标准库即可 |
| 缩略图生成 | 自建 | 纯 Go 库足够处理 JPEG/PNG/GIF；视频是后续工作 |
| 病毒扫描 | 集成 | 启动外部 ClamAV 进程；集成点简单 |
| 媒体转码 | 自建 | 封装 FFmpeg CLI——谨慎管理资源约束（超时、协程池、OOM 保护） |
| 对话/群聊 | 自建 | 属于应用层逻辑，不是第三方的职责 |
| Webhook 重试 | 自建 | 幂等存储 + 退避算法已在 outbox 中实现；已有基础设施 |

---

## 5. 实施路线图

### 优先级排序总结

| 优先级 | 方向 | 工作量估计 | 风险 |
|---|---|---|---|
| **P0** | 架构统一 | 2-3 周 | **极高** —— 内爆式重构，零停机切换 |
| **P1** | Webhook 订阅 | 1 周 | 低 —— 基础设施已就位 |
| **P1** | 数据生命周期 TTL | 1 周 | 中 —— 数据库迁移 + 通用化 |
| **P2** | 对话管理 | 3-4 周 | 高 —— 数据模型变更大 |
| **P2** | 媒体管线 | 4-6 周 | 高 —— 外部依赖多，异步 pipeline 复杂 |

### 阶段划分

**阶段 0（第 1 周）：审计与安全修复**
- 统一上下文键：`service.ctxKey` → `domain.ctxKey`；更新 HTTP 中间件和 gRPC 拦截器
- 验证所有处理程序在统一后仍能正常工作（运行 `make test && make test-integration`）
- **里程碑：上下文键漏洞已修复，两套架构仍并存但可通过适配层互通**

**阶段 1（第 2-3 周）：存储库统一**
- 将旧存储库接口的方法添加到新领域包接口（或者相反）
- 创建适配器（`internal/adapter/`）使旧服务使用新存储库而不改变旧服务
- 逐个切换接线：先切换存储库，然后切换服务实现
- 删除旧 `internal/repository/` 和 `internal/service/`
- **里程碑：单一架构，死代码已移除，`make test && make lint` 通过**

**阶段 2（第 4 周）：Webhook + TTL**
- 新建 `webhook_subscriptions` 表和 CRUD API
- 扩展 outbox 中继以支持多订阅
- 提取通用 TTL 清理器；为消息和文件添加 `expires_at` 支持
- **里程碑：功能性 webhook 生态 + 数据过期机制**

**阶段 3（第 5-8 周）：对话管理**
- 设计 `conversations` 数据模型
- 实现隐式对话（为现有双人线程回填）
- 迁移 `Inbox`/`Conversation` API 使其使用显式对话
- **里程碑：对话线程可管理，群聊基础已就位**

**阶段 4（第 9-12 周）：媒体管线**
- 添加上传时 MIME magic bytes 验证
- 实现同步缩略图生成（纯 Go）
- 设计异步 pipeline 用于转码 + 扫描（预留位置，初始为空操作）
- CDN 分发：用 CDN 源地址替换下载 URL
- **里程碑：文件上传经过验证并自动生成缩略图**

### 风险与缓解策略

| 风险 | 可能性 | 影响 | 缓解 |
|---|---|---|---|
| P0 统一期间停机 | 中 | **极其严重** | 分阶段切换：中间件适配器 → 存储库层 → 服务层 → 处理程序层。每个步骤有测试 |
| 统一后出现回归（上下文键修复后处理程序路径返回 401） | 中 | 高 | 为关键路径添加完整的集成测试，覆盖所有处理程序方法与认证状态 |
| 接口不匹配（新服务缺少 `PresignUpload` 等方法） | 高 | 中 | 审计接口差异并补充缺失的方法。适配器层可隐藏差异 |
| 对话迁移导致数据丢失 | 低 | 严重 | 在不可逆迁移前进行回填试运行 + 备份 |
| 媒体 pipeline 产生不可用缩略图 | 中 | 低 | 回退到已上传的原始文件。通过特性标志启用缩略图 |

### 验证门禁

**每阶段完成后：**
- `make test && make lint`（无条件）
- `make test-integration`（需要 Docker）
- `make build`（构建通过）
- 上下文键问题通过 grep 验证：`service\.WithActor` 在中间件之外零出现

**P0 阶段 0 的额外要求：**
- 添加一个集成测试，验证经过身份验证的请求写入一个上下文键，而两个传输层都能读取它
- 每次迁移提交后手动验证所有 HTTP/gRPC 端点的冒烟测试

---

## 结论

该文档的分析在事实上是准确的——代码库确实维护着两套并行架构，其中一套已死但未移除。上下文键安全问题确实是一颗等待引爆的运行时炸弹。文档中的五个扩展方向与现有基础设施（出站框架、存储 GC 扫描器、SSO 客户端）完全吻合。

**最关键的见解**不在文档本身，而在于：P0（架构统一）中的风险**被严重低估了**。这不仅仅是一次代码清理——它是一次内核交换，需要在 aero-id 运行的同时用新引擎替换旧引擎。阶段 0（上下文键修复）是最小安全前奏，但阶段 1（存储库/服务重新接线）才是内核交换本身。两阶段之间的任何点出现错误都可能导致部分功能失效。

**关于文档优先级的编辑建议：** P0 应进一步细分为 P0a（上下文键安全修复——可在＜1 天内完成）和 P0b（架构统一——2-3 周）。P0a 是**所有其他工作的前置条件**，与架构分裂的存在无关。
