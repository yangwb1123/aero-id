# aero-id — Architecture

> Point-in-time snapshot: 2026-06-13. Reflects branch `feat/roadmap`.

## 系统概览

aero-id 是一个 **双传输** 用户数据服务（HTTP REST + gRPC），核心功能：文件管理、消息收发、评论系统、通知推送、用户阻塞。

```
Client
  ├── HTTP (Gin v1.10, :8080)
  └── gRPC (v1.80, :9090)
         │
         ├── AuthInterceptor / RateLimitInterceptor
         │
         ├── Handler Layer (protocol translation only)
         │   ├── handler/http/{file,message,comment,notification,block}.go
         │   └── handler/grpc/{file,message,comment,notification,block}.go
         │
         ├── Service Layer (business logic)
         │   ├── service/{file,message,comment,notification,block}.go
         │   └── service/notification.go (NotificationDispatcher)
         │
         ├── Repository Layer (DB access via GORM)
         │   ├── repository/{file,message,comment,notification,block,file_share}.go
         │   └── outbox/repository.go
         │
         └── Infrastructure
             ├── PostgreSQL 16 (GORM)
             ├── Storage (Local / S3)
             ├── SSO gRPC client (circuit breaker, retry, stale cache)
             └── Outbox Relay → NotificationDispatcher
```

## 传输层

| 项目 | HTTP | gRPC |
|------|------|------|
| 认证 | `AuthMiddleware` (Bearer token → SSO validate) | `AuthInterceptor` (同上) |
| 限速 | `RateLimitMiddleware` (429 + Retry-After) | `RateLimitInterceptor` (ResourceExhausted) |
| 健康检查 | `/healthz`, `/readyz` | `grpc.health.v1` |
| 指标 | `/metrics` (Prometheus) | 同一 registry，gRPC RED |
| 追踪 | OTel HTTP middleware | OTel gRPC interceptor |
| 下载 | `/files/dl` (HMAC-signed URL) | N/A（S3 presigned 直连） |

## 关键设计不变量

1. **secrets 仅通过环境变量**: `AERO_ID_DATABASE_PASSWORD`, `AERO_ID_SIGNING_KEY`, `AERO_ID_S3_*`
2. **aero-id 永不签发 user token**（SSO 专属）；`SIGNING_KEY` 仅用于本地存储 presign HMAC
3. **无 accounts 表**：通过 uid 字符串引用用户，无 FK 约束
4. **全部 `/v1/*` 均需认证**；仅 `/healthz`、`/readyz`、`/metrics`、HMAC 验证的 `/files/dl` 公开
5. **validated-but-empty uid** 在两种传输层均被拒绝

## 数据流：消息发送

```
POST /v1/messages
  → AuthMiddleware (SSO.ValidateToken → uid)
  → RateLimitMiddleware (token bucket by uid)
  → MessageHandler.Send
    → BlockChecker.IsBlocked(recipient, sender) [suppresses if blocked]
    → MessageService.Send
      → db.Transaction {
          MessageRepo.Create(msg)
          OutboxRepo.Append(message.sent event)
        }
  → 202 Accepted

OutboxRelay (ticker 5s)
  → FetchUnpublished events
  → NotificationDispatcher.Handle(message.sent)
    → BlockChecker [skip if recipient blocked sender]
    → NotificationRepo.Create(message.received notification)
    → MarkPublished
```

## 迁移管理

- 文件: `migrations/000001_*.sql` → `000016_*.sql`（up/down 对，手动应用）
- CLI: `cmd/migrate up|down [N]|version`
- 启动自动: `database.auto_migrate: true`（dev/CI）
- 生产: 始终手动 (`cmd/migrate up`)
- **约束**: 迁移在单个事务内执行（`internal/pkg/migrate.applyTx`），故 **禁止 `CREATE INDEX CONCURRENTLY`**（事务内非法，SQLSTATE 25001）。索引一律用普通 `CREATE INDEX`。`-tags=integration` 套件含 `TestMigratorUpAndDown`，会真实应用全部迁移把关。

## SSO 客户端状态机

```
Closed ──[N consecutive availability errors]──→ Open
Open   ──[cooldown elapsed]──────────────────→ Half-Open
Half-Open ──[probe success]──────────────────→ Closed
Half-Open ──[probe failure]──────────────────→ Open

业务错误 (Unauthenticated/PermissionDenied) 不触发断路器
```

## 存储层

| 后端 | 上传 | 下载 |
|------|------|------|
| Local | multipart 流式写入 | aero-id 提供 HMAC-signed URL → handler 读文件返回 |
| S3 | multipart 流式上传 | presigned URL（客户端直连 S3） |

GC Sweeper（opt-in）: 清理超出 retention 的软删除行 + 孤儿 blob

## gRPC 服务目录（41 RPCs）

- **FileService** (10): GetFile, ListFiles, DeleteFile, GetDownloadURL, ShareFile, UnshareFile, ListSharedWithMe, PresignUpload, CompleteUpload, UpdateFileMetadata
- **MessageService** (11): SendMessage, GetMessage, Inbox, Conversation, MarkRead, UnreadCount, EditMessage, DeleteMessage, MarkConversationRead, ReactToMessage, UnreactMessage
- **CommentService** (12): CreateComment, ListComments, Replies, DeleteComment, LikeComment, UnlikeComment, ReportComment, HideComment, UnhideComment, EditComment, ListReports, ResolveReport
- **NotificationService** (4): ListNotifications, UnreadCount, MarkRead, MarkAllRead
- **BlockService** (3): Block, Unblock, ListBlocked
- **SearchService** (1): Search（全文搜索，PostgreSQL tsvector，按调用者 uid 隔离）

仅 HTTP 的端点（无 gRPC 对应）: `GET /v1/events`（SSE 实时通知推送）、multipart 上传、`/files/dl`（HMAC 签名下载）、`/healthz` `/readyz` `/metrics`。审计事件（`audit_events`）为 fire-and-forget 旁路，无对外 RPC。
