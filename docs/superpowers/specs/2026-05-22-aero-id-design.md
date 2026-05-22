# aero-id 设计文档

- **状态**：草案，待 user review
- **日期**：2026-05-22
- **作者**：Claude（brainstorming session）
- **关联项目目录**：`/home/u1/aero-id`

## 1. 概述与目标

aero-id 是一个 Go 后端服务，承载"账户名下的"业务数据——**文件、消息、评论**。
账户身份的权威数据由独立的 SSO 服务持有，aero-id 通过 gRPC 调用 SSO 完成 token 校验和用户详情查询。

### 1.1 In scope

| 模块 | 能力 |
|---|---|
| 文件 | 账户上传/下载/列出/删除自己文件；可见性（私有/公开）；提供代理流式下载和预签名 URL 两种方式 |
| 消息 | 账户间私信（含文件附件）；标记已读；系统通知（伪发件人）|
| 评论 | 挂在文件/消息（未来其他实体）上的两层评论树（楼+楼中楼）|
| 鉴权 | 通过 SSO gRPC 校验 Bearer token，本地缓存 60s |
| 双协议 | HTTP REST（对外）+ gRPC（对内）共享同一套 service 层 |

### 1.2 Out of scope（YAGNI 掉）

- 账户主数据 CRUD、注册/登录、用户资料管理（属于 SSO 职责）
- 点赞 / 收藏（仅在 schema 留 `like_count` 占位，无 API）
- 后台 worker / 消息队列 / outbox
- Redis / 分布式缓存（短 TTL 内存缓存够用）
- Prometheus metrics / OpenTelemetry tracing
- 多租户、i18n、全文搜索
- 配置热更新

### 1.3 Non-goals

- 不追求 90%+ 测试覆盖率，但 ACL 与错误分支必须覆盖
- 不为未来"拆微服务"做过度抽象（横向分层而非纵向模块）

## 2. 架构总览

### 2.1 进程内逻辑分层

```
[HTTP Handler] ─┐
                ├─→ [Service](业务编排) ─→ [Repository](GORM/PG) ─→ PostgreSQL
[gRPC Handler] ─┘                       ─→ [FileStorage](接口) ─→ local | s3
                                        ─→ [SSOClient](gRPC) ────→ 外部 SSO 服务
                                        ─→ [UserCache](内存) ────→ 缓存 SSO 用户查询
```

HTTP 和 gRPC handler 是两个独立入口，但调用同一套 service。Repository 用 GORM，FileStorage 是接口，SSO 客户端封装 gRPC + 缓存。

### 2.2 目录骨架

```
aero-id/
├── cmd/server/main.go             # 进程入口，装配 wiring
├── internal/
│   ├── app/app.go                 # 应用启动、优雅关停
│   ├── config/                    # viper 配置
│   ├── domain/                    # 领域模型 + 共享类型
│   ├── repository/                # GORM repo（file.go / message.go / comment.go）
│   ├── service/                   # 业务编排（file.go / message.go / comment.go）
│   ├── handler/
│   │   ├── http/                  # Gin handler + router
│   │   └── grpc/                  # gRPC server 实现
│   ├── middleware/                # auth / logger / recovery / cors / request_id
│   ├── sso/                       # SSO gRPC 客户端 + UserCache
│   ├── storage/                   # FileStorage 接口 + local/s3 实现
│   └── pkg/
│       ├── logger/                # zap 封装
│       ├── db/                    # gorm 初始化
│       ├── errors/                # AppError + HTTP/gRPC 映射
│       ├── httpx/                 # 响应工具
│       └── idgen/                 # UUID v7
├── api/proto/
│   ├── aero_id/v1/                # aero-id 暴露的 gRPC
│   └── sso/v1/                    # SSO proto（aero-id 作为客户端引用）
├── migrations/                    # golang-migrate sql 文件
├── configs/config.yaml
├── deployments/
│   ├── Dockerfile
│   └── docker-compose.yml
├── Makefile
└── go.mod
```

### 2.3 关键依赖

| 用途 | 库 |
|---|---|
| HTTP | `gin-gonic/gin` |
| gRPC | `google.golang.org/grpc` |
| ORM | `gorm.io/gorm` + `gorm.io/driver/postgres` |
| 配置 | `spf13/viper` |
| 日志 | `uber-go/zap` |
| 迁移 | `golang-migrate/migrate`（外部 CLI） |
| ID | `google/uuid`（v7 时序友好） |
| HMAC 签名 | `crypto/hmac` + `crypto/sha256`（用于 local storage 的 PresignGet） |
| 测试容器 | `testcontainers-go`（自动起 pg） |

> aero-id 自身**不签发用户 token**（token 由 SSO 签发），所以不需要 JWT 库。配置里的 `AERO_ID_SIGNING_KEY` 仅用于本地文件存储的预签名 URL HMAC。

### 2.4 启动流程

`main()` → 加载 config → 装配 logger → 连 DB → 拉起 SSO gRPC connection → 装配 storage → 装配 repo → service → handler → 起 HTTP（:8080）和 gRPC（:9090）两个 server（独立 goroutine） → 监听 SIGTERM 优雅关停。

## 3. 数据模型与 Schema

### 3.1 共享约定

- 主键：UUID v7
- 时间戳：`created_at` / `updated_at` / `deleted_at`（软删）均为 `TIMESTAMPTZ`
- 账户列：统一叫 `owner_uid` 或 `from_uid`/`to_uid`，存 SSO 的 uid 字符串（不做 FK 约束，因为账户不在本库）
- 软删：`deleted_at IS NOT NULL` 表示已删，所有查询索引带 `WHERE deleted_at IS NULL`
- **当前操作者**：auth 中间件把鉴权后的 uid 写进 `ctx`（key 统一叫 `actor_uid`）。service 层从 ctx 读它做 ACL 判断和写 access log。`actor_uid` 不入业务表（owner_uid 已经覆盖了归属审计）

### 3.2 `files`

```sql
CREATE TABLE files (
  id            UUID PRIMARY KEY,
  owner_uid     TEXT NOT NULL,
  name          TEXT NOT NULL,
  size          BIGINT NOT NULL,
  mime_type     TEXT NOT NULL,
  storage_key   TEXT NOT NULL,
  storage_type  TEXT NOT NULL,                  -- 'local' | 's3'
  checksum      TEXT,                           -- sha256
  visibility    SMALLINT NOT NULL DEFAULT 0,    -- 0 私有，1 公开
  meta          JSONB NOT NULL DEFAULT '{}',
  created_at    TIMESTAMPTZ NOT NULL,
  updated_at    TIMESTAMPTZ NOT NULL,
  deleted_at    TIMESTAMPTZ
);
CREATE INDEX idx_files_owner    ON files(owner_uid, created_at DESC) WHERE deleted_at IS NULL;
CREATE INDEX idx_files_checksum ON files(checksum);
```

### 3.3 `messages`

```sql
CREATE TABLE messages (
  id          UUID PRIMARY KEY,
  from_uid    TEXT NOT NULL,                   -- 'system' 表示系统通知
  to_uid      TEXT NOT NULL,
  kind        SMALLINT NOT NULL DEFAULT 1,     -- 1 文本 2 图片 3 文件附件 4 系统通知
  content     TEXT NOT NULL,
  file_id     UUID REFERENCES files(id),       -- 附件类消息
  read_at     TIMESTAMPTZ,                     -- NULL = 未读
  meta        JSONB NOT NULL DEFAULT '{}',
  created_at  TIMESTAMPTZ NOT NULL,
  deleted_at  TIMESTAMPTZ
);
CREATE INDEX idx_messages_inbox  ON messages(to_uid,   created_at DESC) WHERE deleted_at IS NULL;
CREATE INDEX idx_messages_outbox ON messages(from_uid, created_at DESC) WHERE deleted_at IS NULL;
CREATE INDEX idx_messages_unread ON messages(to_uid) WHERE read_at IS NULL AND deleted_at IS NULL;
```

**附件删除策略**：`messages.file_id` 不级联——文件被 owner 删除后，消息保留（`meta` 内会冗余存原文件名/size 快照）。

### 3.4 `comments`

```sql
CREATE TABLE comments (
  id           UUID PRIMARY KEY,
  owner_uid    TEXT NOT NULL,
  target_type  TEXT NOT NULL,                  -- 'file' | 'message' | ...
  target_id    UUID NOT NULL,
  parent_id    UUID REFERENCES comments(id),
  root_id      UUID,                           -- 顶层评论 id
  content      TEXT NOT NULL,
  like_count   INT NOT NULL DEFAULT 0,         -- 占位字段，MVP 无 API
  reply_count  INT NOT NULL DEFAULT 0,
  status       SMALLINT NOT NULL DEFAULT 1,    -- 1 正常 2 已删 3 屏蔽
  created_at   TIMESTAMPTZ NOT NULL,
  updated_at   TIMESTAMPTZ NOT NULL,
  deleted_at   TIMESTAMPTZ
);
CREATE INDEX idx_comments_target ON comments(target_type, target_id, created_at DESC) WHERE deleted_at IS NULL;
CREATE INDEX idx_comments_owner  ON comments(owner_uid, created_at DESC) WHERE deleted_at IS NULL;
CREATE INDEX idx_comments_root   ON comments(root_id, created_at) WHERE deleted_at IS NULL;
```

**嵌套约束（service 层校验）**：
- 顶层评论：`parent_id IS NULL`，`root_id = id`
- 楼中楼：`parent_id` 必须是某条顶层评论或其直接子；`root_id` = 顶层评论 id
- 拒绝深度 ≥ 3 的层级

**多态外键**：`comments.target_*` 不在 DB 层做外键，由 service 校验 target 存在且操作者有读权限。

## 4. SSO 集成

### 4.1 Proto 接口（消费方）

```proto
service SSO {
  rpc ValidateToken(ValidateTokenRequest) returns (ValidateTokenResponse);
  rpc GetUsers(GetUsersRequest) returns (GetUsersResponse);     // 批量
}
```

如对接的真实 SSO 接口名不同，调整客户端 wrapper 即可，service 层只依赖 wrapper 接口。

### 4.2 鉴权流程

```
HTTP/gRPC 请求 → 取 Authorization: Bearer <token>
  ↓
查 tokenCache[token] → 命中且未过期 → 注入 uid 进 ctx → 放行
  ↓ 未命中
SSO.ValidateToken(token)
  ↓ 成功
缓存 token → uid（TTL = min(token原过期, 60s）→ 注入 ctx → 放行
  ↓ 失败
401 / Unauthenticated
```

- **缓存实现**：`sync.Map` + 后台 goroutine 每 30s 清扫过期项
- **TTL**：60s。SSO 撤销最坏 60s 后生效，可接受
- **gRPC 拦截器**：`UnaryServerInterceptor` 复用同一份鉴权逻辑

### 4.3 UserCache（GetUsers）

- 评论 / 消息列表回写时需要昵称头像
- key = uid，value = `{nickname, avatar, expire}`，TTL 300s
- service 调用 `userCache.GetMany(uids)`：先批量查缓存，缺失的 uid 攒一批一次调 `sso.GetUsers`
- 不落库（避免与 SSO 双写不一致）

## 5. 文件存储抽象

### 5.1 FileStorage 接口

```go
package storage

type FileStorage interface {
    Put(ctx context.Context, ownerUID, name string, r io.Reader) (key string, size int64, err error)
    Get(ctx context.Context, key string) (io.ReadCloser, error)
    Delete(ctx context.Context, key string) error
    PresignGet(ctx context.Context, key string, ttl time.Duration) (string, error)
    Stat(ctx context.Context, key string) (*ObjectInfo, error)
}
```

### 5.2 两种实现

| 实现 | 存储路径/对象 | PresignGet |
|---|---|---|
| `local` | `<root>/<owner_uid>/<yyyy>/<mm>/<uuid>` | 返回自家路由 `/files/dl?token=<hmac>` 临时下载 URL |
| `s3` | `<owner_uid>/<yyyy>/<mm>/<uuid>` 对象 key | 原生 S3 预签名 URL |

配置项 `storage.type = local | s3` 切换。

### 5.3 上传链路

`POST /api/v1/files`（multipart/form-data）
1. handler 接 `io.Reader`
2. service.UploadFile：边读边算 sha256
3. `FileStorage.Put`
4. 写 `files` 表
5. 返回文件元信息

### 5.4 下载链路（两种都给）

通过 query 参数 `mode` 选择：

- `GET /api/v1/files/:id?mode=stream`（默认）：service 校验 ACL → `FileStorage.Get` 流式 → handler `io.Copy` 到响应
- `GET /api/v1/files/:id?mode=presign`：service 校验 ACL → `FileStorage.PresignGet` → 返 `{"url": "..."}` 或 302 跳转（再加 query `redirect=1`）

ACL：owner 全权 + `visibility=public` 时所有人可读。

## 6. 错误处理 · 配置 · 日志

### 6.1 AppError

```go
type AppError struct {
    Code    string       // "auth.unauthorized", "file.not_found"
    Message string       // 给用户
    HTTP    int          // 映射 HTTP 状态
    GRPC    codes.Code   // 映射 gRPC code
    Cause   error        // 内部，不暴露
}
```

HTTP 响应统一结构：
```json
{ "error": { "code": "file.not_found", "message": "file not found", "request_id": "01HX..." } }
```

gRPC 通过 `status.Error(code, message)`。

### 6.2 中间件链（HTTP）

`request_id → recovery → access_log → cors → auth → handler`

- `request_id`：透传 `X-Request-Id`，缺则生成；注入 ctx 和 zap logger 字段
- `recovery`：panic → 500 + stack 入日志
- `access_log`：method/path/status/duration/uid
- `auth`：除 `/healthz` `/readyz` 外强制

### 6.3 配置

加载顺序：默认值 → `configs/config.yaml` → `AERO_ID_*` 环境变量。

敏感项必走环境变量：`AERO_ID_DATABASE_PASSWORD`、`AERO_ID_SIGNING_KEY`（local storage HMAC）、`AERO_ID_S3_*`。

启动时 `cfg.validate()`，关键字段缺失立即 panic。

## 7. 测试策略

| 层 | 方式 | 覆盖目标 |
|---|---|---|
| domain | 纯单测 | 业务规则（评论嵌套校验、ACL 判断） |
| repository | testcontainers-go 真实 pg | SQL 语义、索引使用、约束 |
| service | mock repo + mock sso/storage | 业务编排、错误分支 |
| handler/http | httptest | 路由、绑定、鉴权 |
| handler/grpc | bufconn | 协议层 |
| sso 客户端 | 起本地 grpc.Server + bufconn | 缓存、Token 校验、批量合并 |

**硬性要求**：
- repository 测试**必须用真实 PostgreSQL**，不接受 sqlmock
- ACL 和错误分支 100% 覆盖

## 8. 数据库迁移

- `migrations/000N_<name>.up.sql` / `.down.sql` 成对
- `make migrate-up` / `make migrate-down` / `make migrate-create name=xxx`
- **不在应用启动时自动 migrate**——避免多副本竞争。CI/CD 独立步骤

## 9. 部署与运维

### 9.1 Makefile target

```
make tidy proto build run test test-integration lint
make migrate-up migrate-down migrate-create name=xxx
make docker-build docker-up docker-down
```

### 9.2 docker-compose（本地集成）

- `db`：postgres:16
- `aero-id`：本地构建镜像，`depends_on: db`
- **不内置 SSO**，由 `AERO_ID_SSO_ENDPOINT` 指向真实/mock SSO

### 9.3 Dockerfile

多阶段：golang builder → distroless runtime，最终镜像约 30MB。

### 9.4 端口与健康检查

- HTTP `:8080`
- gRPC `:9090`
- `GET /healthz`：进程存活
- `GET /readyz`：探测 DB + SSO gRPC 连接

### 9.5 优雅停机

SIGTERM → 停接新连接 → 等现有请求完成（≤ `shutdown_timeout`，默认 10s）→ 关 DB / gRPC conn → 退出。

## 10. 关键决策记录

| # | 决策 | 理由 |
|---|---|---|
| D1 | 账户不存本地表，只用 sso_uid 引用 | SSO 是 source of truth；避免双写一致性问题 |
| D2 | 横向分层（方案 A），不按模块纵切 | 三个模块强耦合（评论挂在文件/消息上），同层互调比跨模块接口顺 |
| D3 | gorm 而非 sqlx | 业务表 schema 稳定、CRUD 多于复杂查询，gorm 提速明显；性能瓶颈未到 |
| D4 | token 缓存 60s | 平衡 SSO 压力与撤销时延 |
| D5 | 评论限两层 | 楼中楼足够，避免深嵌套树查询/UI 复杂 |
| D6 | 点赞 v2 | MVP 不做，schema 留 `like_count` 占位 |
| D7 | 文件下载两种模式 | 灵活：私有走代理、公开/大文件走预签 + CDN |
| D8 | metrics / tracing 不内置 | YAGNI，后续接入观测体系再加 |
| D9 | 测试用 testcontainers-go | 真实 PG 测 repo；开发者本地零配置 |
| D10 | 启动不自动 migrate | 多副本部署时避免迁移竞争 |

## 11. 未来扩展点（仅记录，不实现）

- 点赞表 `comment_likes(comment_id, owner_uid)` + 计数维护
- pg 全文检索（已在表上留 jsonb，可加 GIN 索引）
- 消息推送（WebSocket / SSE 长连接）
- 多租户隔离（`org_id` 列）
- 观测（otel + prometheus）

---

**文档状态**：等待 user review。批准后进入实现计划（writing-plans skill）。
