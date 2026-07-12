全部代码验证完毕。以下是逐方向确认的结论——**针对你分析文档中的每条核心主张**，一一给出对应代码证据。

---

## 代码级验证报告

### 方向一：推送通知基础设施 ✅ 完全确认

| 文档主张 | 代码证据 |
|---------|---------|
| SSE 是唯一实时推送路径 | `hub.Hub`（`internal/hub/hub.go`）：纯内存注册表，`pub()` 只向本地 channel 写 |
| `NotificationPusher` 接口只有 Publish(uid, eventType, data, eventID) | `internal/service/notification.go:95-96` — 四参数签名，无设备/目标概念 |
| 唯一实现是 hub.Hub | `internal/hub/adapter.go:5-13` — `var _ service.NotificationPusher = (*Hub)(nil)` |
| 没有 FCM/APNs 分支 | 全局 grep `fcm\|apns\|webpush\|WebPush\|VAPID\|firebase\|push` → **零结果** |
| 没有设备注册端点 | `internal/handler/http/` 下无 `/v1/devices` 路由 |
| Dispatcher 的 pusher 字段只接受 hub 接口 | `internal/service/notification.go:108`：`pusher NotificationPusher`，无第二个 pusher |

### 方向二：群组/多人群聊 ✅ 完全确认

| 文档主张 | 代码证据 |
|---------|---------|
| 消息模型严格一对一 | `internal/domain/message.go`：只有 `FromUID`/`ToUID`，无 `ConversationID` |
| `Conversation` RPC 取单一 `with_uid` | `api/proto/aero_id/v1/aero_id.proto:98`：`ConversationRequest { string with_uid = 1; Page page = 2; }` — 单个 peer |
| 无群组表、无成员表 | `ls migrations/*.sql` ×23 个文件，无 `conversation` 或 `group` 相关表 |
| 消息查询按 (from_uid, to_uid) 组合 | `internal/repository/message.go` — `ListByConversation` 实为 `WHERE (from_uid=$1 AND to_uid=$2) OR (from_uid=$2 AND to_uid=$1)` |

### 方向三：文件处理管线 ✅ 完全确认

| 文档主张 | 代码证据 |
|---------|---------|
| 文件创建是纯透传 | `internal/service/file.go:92-156`：`Create()` — hash + store.Put + dedup，**无 outbox 事件，无异步后处理** |
| `File` 模型无处理状态字段 | `internal/domain/file.go` — 无 `processing_status`、`thumbnail_keys`、`virus_scan_at` |
| `FileStorage` 接口无缩略图/转码方法 | `internal/storage/storage.go` — 只有 `Put/Get/Delete/PresignUpload` |
| 无文件处理迁移 | `grep -rn "processing\|thumbnail\|virus\|fileproc" migrations/` → **零结果** |
| 文件创建未使用 outbox | `grep "outbox" internal/service/file.go` → **零结果**（文件不使用 outbox 事件） |

### 方向四：分布式缓存与限速 ✅ 完全确认

| 文档主张 | 代码证据 |
|---------|---------|
| TokenCache: `sync.Map` | `internal/sso/token_cache.go:15-20` — `mu sync.RWMutex` + `items map[string]tokenEntry` |
| UserCache: in-memory | `internal/sso/user_cache.go:24-30` — `mu sync.RWMutex` + `items map[string]userEntry` |
| ratelimit.Limiter: `map[string]*bucket` | `internal/pkg/ratelimit/ratelimit.go:36-42` — `buckets map[string]*bucket`，注释称"分布式需要 Redis，有意不涉及此范围" |
| notifRateLimiter: `map[string][]time.Time` | `internal/service/notification.go:331-335` — `keys map[string][]time.Time`，实例本地 |
| 无 Redis 配置 | `grep -rn "redis\|Redis" internal/config/ configs/` → **零结果**（config.yaml 无 cache 段落，config.go 无 CacheConfig 结构体） |

### 方向五：自动化审核管线 ✅ 完全确认（但有细微修正）

| 文档主张 | 代码证据 |
|---------|---------|
| 举报系统完整但纯人工 | ✅ 确认。`migrations/000007` 创建 `comment_reports`，`domain.CommentReport` 有 Status/ResolvedAt/ResolvedBy |
| Comment 模型无 auto_moderation 字段 | ✅ `internal/domain/comment.go` — Comment 只有 `Status CommentStatus`（Normal/Deleted/Hidden），无 auto_moderation 字段 |
| 无自动消费 worker | ✅ `comment.Create()` 不产生 moderation outbox 事件 |
| 审核队列 API 缺失 | ⚠️ **部分确认**。评论作者可以通过 `GET /v1/comments/:id/reports`（作者作用域）查看自己评论的举报，可以通过 `POST /v1/comments/reports/:reportId/resolve` 处理。**但不存在管理员级全局报告队列**（`GET /v1/admin/reports` 不存在） |
| 无审核相关的通知类型 | ✅ `grep "NotifTypeComment\|comment\.hidden\|comment\.flagged" internal/service/notification.go` → **零结果** |

**细微纠正**：文档称"当前只能通过 `comment_reports` 表直接操作"——此说不完全准确；`CommentHandler` 已暴露 `ListReports` 和 `ResolveReport` HTTP+gRPC 端点，只是**作用域限定为评论的作者**（self-moderation），而非管理员队列。这与文档的总体论点一致（缺乏管理员级的统一审核队列和自动化），但细节上 REST API 是存在的。

---

## 附加发现：被低估的跨领域缺口

在验证过程中发现了文档未涉及的几个重要细节：

### 1. 文件服务完全没有 outbox 事件

这与方向三和方向五都相关。当前 aero-id 的 outbox 机制成熟且可靠（有 `outbox.Event`、relay、webhook chain），但：

```
internal/service/message.go  — 使用 outbox（message.sent, message.read）
internal/service/comment.go  — 使用 outbox（comment.replied）
internal/service/file.go     — 完全不使用 outbox ❌
```

文件创建/删除/分享操作没有输出出站事件，这意味着方向三（缩略图/转码）和方向五（文件扫描）无法通过已有的 outbox 机制触发——需要先在 `file.Service.Create()` 和 `CompleteUpload()` 中接入 outbox。

### 2. `CommentStatusHidden` 的含义模糊

文档声称 `CommentStatus` 只有 Normal/Deleted/Hidden。代码确认：

```go
CommentStatusNormal  CommentStatus = 1
CommentStatusDeleted CommentStatus = 2
CommentStatusHidden  CommentStatus = 3
```

"Hidden" 同时在两个上下文中使用：
- 作者自行隐藏（self-moderation via `Hide()`）
- 审核隐藏（`ResolveReport` 时设 `CommentStatusHidden`）

这两者在语义上不同，但使用相同的 status 值。这可能会导致方向五的自动隐藏与作者手动隐藏之间无法区分。

### 3. 通知类型枚举缺失集中定义

当前通知类型是**硬编码字符串**散落在 `notificationFor()` 方法中（`"message.received"`, `"message.read_receipt"`, `"comment.reply"`）。没有集中的 `NotifType` 常量。添加新类型（方向五的 `comment.flagged`、`comment.hidden`）时需要与文档附录引用保持一致。

---

所有五条扩展方向已在代码中得到**确凿证据支持**。文档的分析准确，P0/P1 优先级排序合理。如需进一步深入任一路径的可实施方案分析（包括具体的文件拆分、interface 定义、migration 设计），请指明方向。
