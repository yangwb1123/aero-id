现在我有了对代码库的全景理解。让我提供完整的架构分析。

---

# aero-id 架构深度审查与扩展分析报告

> **分析基础**：基于对 `/home/u1/aero-id/` 全量代码扫描（所有 `.go`、`.proto`、`.sql`、`.yaml`、`.md`），包括 `ARCHITECTURE.md`、`ARCHITECT_REVIEW_2026-06-30.md`、`ROADMAP.md` 及 `app.go` 汇聚根，以及新旧两套代码结构的并行存在。

---

## 一、架构评估

### 1.1 当前架构的优势

**分层清晰度 ⭐⭐⭐⭐⭐**
代码库严格遵循 `handler → service → repository → Postgres` 的单向依赖链，这是该项目的最大工程亮点。每一层的职责边界清晰：

- Handler 层仅做传输编解码（JSON ↔ struct / protobuf ↔ struct），**零业务逻辑**
- Service 层是**唯一**的业务逻辑与授权决策点，HTTP 和 gRPC 共享同一套 Service 实例
- Repository 层封装 GORM，返回 raw error，由 Service 层翻译为 `*AppError`
- 依赖方（SSO、Storage）通过接口注入，可测试、可替换

**错误处理一致性 ⭐⭐⭐⭐⭐**
`pkg/errors.AppError` 携带 HTTP 状态码 + gRPC 状态码 + 业务错误码 + Cause，是跨双传输的错误翻译枢纽。源码扫描确认——**没有任何 raw DB error 穿透到传输层**。

**模块化演进正在进行** ✅
代码库正在从**扁平包结构**（`internal/service/`, `internal/repository/`, `internal/handler/{http,grpc}`）向**领域包结构**（`internal/comment/`, `internal/file/`, `internal/message/` 各自包含 Service/Repository/Handler）迁移。这是正确的方向。

### 1.2 关键架构问题

#### 问题 1：代码库处于"新旧并存"的过渡态——这是最大的架构债务

扫描发现**两套平行实现**：

| 领域 | 旧结构（app.go 当前使用） | 新结构（已编写但未接入） |
|------|--------------------------|--------------------------|
| Comment | `internal/service/comment.go` — `commentService` struct | `internal/comment/service.go` — `serviceImpl` struct |
| Comment HTTP | `internal/handler/http/comment.go` — 接受 `service.CommentService` | `internal/comment/handler_http.go` — 接受 `comment.Service` |
| File | `internal/service/file.go` — `fileService` | `internal/file/service_crud.go` + `service_share.go` + `service_upload.go` |
| ... | ... | ... |

两套代码逻辑高度相似（很多函数的 body 几乎逐行对应），但接口定义、包名、依赖关系都不同。

**风险**：
- 维护成本翻倍——修改一个行为需要改两份代码
- 新功能开发时容易搞错基底——可能在新结构上开发而旧结构未同步，或反过来
- 新人理解困难——不知道哪套是"活"的

**建议**：立即决定**完全废弃旧结构**，将 `app.go` 的汇聚根切换到新领域包。这是一个"低代码变更、高认知收益"的清理操作。

#### 问题 2：`app.go`（汇聚根）已达 300+ 行——违反 AGENTS.md 的 500 行上限

`internal/app/app.go`（336 行）虽然未超 500 行限制，但**已在黄色警戒区**。每一轮功能添加都倾向于在这里加新依赖 + 新构造调用。按照当前增速，再有 2-3 个领域模块加入就会触及硬性上限。

**建议**：引入 `app.Module` 注册模式——每个领域包提供一个 `Module` 接口（或 `Register` 函数）返回自己所需的依赖和要注册的路由，让 `app.go` 从"手动接线"变为"模块注册"。参考 Go 标准模式如 `pkg.go.dev/net/http#Handler` 的 `ServeMux.Handle` 或 Wire 框架的依赖注入思路。

#### 问题 3：`like_count` 高并发下的非原子更新

已由 `ARCHITECT_REVIEW_2026-06-30.md` 精准指出（第 1.1 节）。核心问题是 `Like()` 的 INSERT + UPDATE 两步操作没有事务保护或原子 UPDATE 语句。

**影响**：在热点内容上，并发 Like/Unlike 可能导致 `like_count` 漂移。这是一个真实的数据一致性问题。

**修复方案（三个选项）**：

| 选项 | 复杂度 | 效果 | 额外影响 |
|------|--------|------|----------|
| A：原子 UPDATE `like_count = like_count + 1` 配合 DB 约束 | 低（半天） | 消除竞态 | 需要 `SELECT ... FOR UPDATE` 获取真实值或使用 RETURNING |
| B：乐观锁（version 列） | 中 | 失败时重试 | 需要重试逻辑，影响写路径延迟 |
| C：纯计算（每次查 count(\*)） | 低（但性能差） | 完全避免漂移 | 高频列表查询时 where 子句查询代价高 |

**推荐**：选项 A 是收益/成本比最高的选择。

### 1.3 架构债务清单

| 债务 | 级别 | 估算工时 | 说明 |
|------|------|----------|------|
| 新旧结构并行 | **High** | 2-3 天 | 两套代码难以维护，容易不同步 |
| `like_count` 非原子更新 | **High** | 0.5 天 | 高并发下数据漂移 |
| `idempotency_keys` 无 TTL 清理 | **High** | 0.5 天 | 存储无限膨胀 |
| SSE 无 `last_event_id` | **Medium** | 1 天 | 重启丢通知 |
| Outbox 失败无退避 | **Medium** | 1 天 | 固定间隔重试浪费资源 |
| 搜索无 `statement_timeout` | **Medium** | 0.5 天 | 长查询可能锁死连接 |
| 通知无速率门限 | **Medium** | 1 天 | 恶意用户洪泛通知 |
| FTS 中文分词无效 | **Medium** | 2-3 天 | CJK 用户搜不到中文内容 |
| `app.go` 汇聚根膨胀 | **Low-Medium** | 1 天 | 渐近式模块化注册 |

---

## 二、高价值扩展方向

### Direction 1：模块化注册（清理架构债务 + 预防膨胀）

**为什么需要**。当前 `app.go` 手动接线所有依赖，每个新领域都要加 3-5 行构造逻辑 + 导入语句。切换到模块注册模式可以：

- 将 `app.go` 控制在 200 行以内
- 新领域通过 `Module` 接口自描述注册
- 测试时可以轻松替换模块为 fake

**核心挑战**。Go 没有泛型的模块系统，需要定义清晰的 `Module` 接口。依赖的构造顺序（DB → SSO → Repos → Services → Handlers）需要显式管理。

**建议方案**（不做具体代码实现，仅描述接口契约）：

```
Module 接口：
  Name() string                             // 模块名，用于日志
  Deps() []string                           // 前置模块名
  Init(deps DepsProvider) error             // 初始化自己
  RegisterRoutes(r Routes)                  // 注册 HTTP 路由
  RegisterServices(s *grpc.Server)          // 注册 gRPC 服务
```

**对现有系统的影响**。低——这是纯重构，不改业务逻辑。可以先对 1-2 个领域做试点（如 `block`，相对独立），验证模式后再全量切换。

---

### Direction 2：多人群聊 / 频道系统（最高产品价值）

**为什么需要**。当前 1:1 消息模型是最大的产品功能缺口：

- 文件共享天然需要群聊语境——"分享给项目组"比逐人分享更合理
- 从 MVP 到社交/协作平台的跨越，群聊是必经之路
- 现有通知架构（outbox → dispatcher → SSE）天然可扩展支持群聊通知

**核心挑战**：

1. **数据模型变更**：`messages.to_uid` 当前是单字符串，需要迁移为 `conversation_id`。现有 1:1 消息需要脚本镜像创建 conversation 行
2. **通知写入 N 条**：群消息写入 N 条通知（N=成员数，排除发送者和被拦截者），需批量插入优化
3. **成员管理**：角色（owner / admin / member）、邀请/加入/退出/踢出流程
4. **@提及**：需要解析 `@uid` 语法、仅通知被提及者（而非全员）

**预期架构变更**：

| 组件 | 变更 |
|------|------|
| Schema | 新增 `conversations` 表（类型：1:1/group）、`conversation_members` 表（含角色）、改造 `messages` 添加 `conversation_id` |
| Service | 新增 `ConversationService`：创建/成员管理/更新设置 |
| Domain | 新增 `Conversation`、`ConversationMember` 实体和不变约束（如：群必须有至少 1 个 owner） |
| Repository | 新增 conversation + member 的 CRUD 和迁移脚本 |
| Notification | `Dispatcher` 增加群消息的 N 条通知批量写入，可以引入 `batchWriter` 减少事务数 |
| Migration | 迁移脚本必须处理 1:1 → conversation 的双向映射 |

**对现有系统的影响**。**高**——这是架构级变更。建议分 3 个里程碑：

1. **M1**: conversations 表 + members 表 + 数据迁移（现有 1:1 创建镜像 conversation）
2. **M2**: 群聊发送/接收（改造 `SendMessage` 支持 `conversation_id`）+ 成员管理端点
3. **M3**: 群通知 + @提及 + SSE 推送优化

---

### Direction 3：文件预览管道（Thumbnailing / Transcoding）

**为什么需要**。文件服务目前是"原始 blob 存储"，上传什么返回什么：

- 无缩略图意味着消息列表/文件列表每个都要传原始文件——移动端无法使用
- 产品形态需要从"文件存储"演进到"文件管理"
- 这是**建立 Job Queue 基础设施**的天然切入点——后续 GC、数据导出、批量操作都可复用

**核心挑战**：

1. **Job Queue 基础设施**需要从零构建：要么基于 outbox 泛化，要么引入独立的队列（如 RabbitMQ 或 `internal/jobqueue` 的 PostgreSQL-backed 队列）
2. **外部工具依赖**：`ffmpeg`（视频截图）、`libvips` 或 `ImageMagick`（图片缩放）、`LibreOffice`（文档转 PDF 预览）——每个都增加容器镜像大小和运行时依赖
3. **派生文件模型**：缩略图是另一种"文件"，需要关联到原始文件、有自己的过期/TTL、存储在不同的 key prefix 下
4. **预览失败处理**：不是所有文件都能生成预览（加密 PDF、破损图片），需要优雅降级

**预期架构变更**：

| 组件 | 变更 |
|------|------|
| Job Queue | 新增 `internal/jobqueue` 包（泛化 outbox 模式）或集成外部队列 |
| File Service | 上传完成后 emit `file.uploaded` 事件 → queue thumbnail job |
| Storage | 新增派生文件 key 命名约定（如 `thumb/{original_key}_sm.webp`） |
| Handler | `GET /files/:id?variant=thumb_small` 查询参数 |
| Config | `preview.allowed_types`, `preview.thumbnail_sizes`, `preview.tool_paths` |

**对现有系统的影响**。中等——新功能独立于核心 CRUD 路径。缩略图不存在时优雅降级返回原始文件。

**技术选型选项**：

| Job Queue 方案 | 复杂度 | 运维成本 | 优点 |
|----------------|--------|----------|------|
| 基于 outbox 泛化（+ `job_type` + `status`） | 低 | 无额外依赖 | 复用现有轮询机制 |
| RabbitMQ / Redis Streams | 中 | 中 | 延迟低、特性丰富 |
| River（Go 的 Postgres-backed job queue） | 低 | 无额外依赖 | 现代 API、刚好满足需求 |

**推荐**：**基于 outbox 泛化**作为 P0 实现（日内工作量），后续如果需要更丰富功能再考虑 River。不需要引入额外中间件。

---

### Direction 4：平台级审核 + 管理员角色系统（合规门线）

**为什么需要**。这是从"用户自管"到"平台运营"的硬性跨越：

- 无管理员角色意味着无法处理：儿童安全举报、版权投诉、垃圾信息清理
- 当前审计日志只写不读——管理员需要查询能力
- SSO 不提供角色信息（按设计），aero-id 需要内部轻量角色系统

**核心挑战**：

1. **角色权限模型设计**：需要定义清晰的权限点体系，避免"超级管理员"神奇角色
2. **内容搜索 + 批量操作**：管理员需要按内容、用户、时间范围搜索评论/消息/文件
3. **审核工作流**：举报受理 → 审核 → 处理（隐藏/删除）→ 通知举报人→ 记录审计
4. **不要过度设计**：10 个权限点足够（content.moderate、user.ban、audit.read、system.config 等）

**建议的权限点设计**（不制定具体表结构，给出概念模型）：

```
Permissions:
  content.read     - 读取任意内容（留言/文件/评论）
  content.moderate - 隐藏/删除任意内容
  content.feature  - 置顶内容
  user.read        - 查看用户信息（通过 SSO）
  user.ban         - 封禁/解封用户
  audit.read       - 查看审计日志
  audit.export     - 导出审计日志
  system.config    - 修改系统配置
  system.webhook   - 管理 outbox webhook
```

**对现有系统的影响**。中等——新模块独立。现有的 author-scoped 自管审核继续工作，admin 角色只是叠加层。

---

### Direction 5：通知偏好设置 + 免打扰

**为什么需要**。这是**当前投入产出比最高的新功能**：

- 通知疲劳是用户流失的首要原因——没有偏好设置，用户只能选择 all-or-nothing
- 实现成本**极低**（日间 200 行），影响面**极广**（每个用户都受益）
- Dispatcher 已经有 `WithDispatcherBlockChecker` 的回调模式，再加一个 `PreferenceChecker` 是线性扩展

**技术方案已经非常明确**：

1. 新增 `notification_preferences` 表（uid, type, enabled, quiet_hours_start/end, timezone）
2. 新增 `PreferenceChecker` 接口，注入 Dispatcher
3. SSE 推送前也过偏好检查（即使通知行已写入）
4. 系统安全通知应**跳过**偏好设置的免打扰

**风险点**：要考虑批量操作（用户一次性打开/关闭所有类型）与增量更新（只修改某一项）的 API 设计。建议使用全量 PUT（幂等、简单）而非 patch 语义。

---

## 三、接口设计建议

### 3.1 关键接口设计原则

当前代码库展示了很好的接口设计实践，值得提炼为**项目约定**：

**原则 1：小接口优于大接口**
- `UserDirectory`（只含 `GetUsers`）比 `*sso.Client` 更适合做依赖声明
- `BlockChecker`（只含 `IsBlocked`）、`PermissionChecker`（只含 `HasPermission`）都是最小接口单元
- 这种做法应该推广——每个 Service 只声明自己需要的最小依赖集

**原则 2：领域包内部定义自己的 Repository 接口，而不是消费外部定义的接口**
- 新结构（`internal/comment/repository.go`）定义了 `interface Repository { Create, Get, List... }`
- 旧结构（`internal/repository/comment.go`）导出了具体 struct，Service 直接依赖具体类型
- 新结构更灵活——测试时直接 mock 小接口，不需要 import 整个 repo 包

**建议：统一采用"领域包内定义 Repository 接口"的模式**，这已经是领域驱动设计的标准实践。

### 3.2 需要引入的新抽象层

#### 3.2.1 `internal/module` 模块注册层（可选但推荐）

当前 `app.go` 的"手动接线"模式在模块数增长后不可持续。一个 `Module` 注册机制可以让：

- 每个领域包自描述：`comment.Module{}` 声明依赖 `[database, sso, file_service, message_service]`
- `app.go` 只负责：打开依赖 → 注册所有 Module → 启动服务器
- 隔离测试：`app_test.go` 可以替换 Module 为 test doubles

**选择架构选项**：

| 方案 | 复杂度 | 说服力 |
|------|--------|--------|
| 接口式 Module（`interface { Init, Deps, Routes, GRPC }`） | 低 | 清晰，Go 标准风格 |
| Wire（google/wire）依赖注入框架 | 中 | 编译期注入，但引入外部依赖 |
 | Functional Options 链（当前模式 + RegisterModular） | 低 | 最小变更，渐进演进 |

**推荐**：接口式 Module 方案。Wire 的 `wire.Build` 对项目侵入性过高，且 debug 体验不好。

#### 3.2.2 `internal/pkg/cache` 缓存抽象层（远期可选）

当前无缓存。引入 `Cache` 接口可以让 Repository 透明包装：

```go
type Cache interface {
    Get(ctx context.Context, key string, dest interface{}) error
    Set(ctx context.Context, key string, val interface{}, ttl time.Duration) error
    Delete(ctx context.Context, key string) error
}
```

实现可选：`redisCache`、`memoryCache`（`freecache`/`ristretto`）、`noopCache`（方便测试）。

### 3.3 向后兼容性策略

当前系统有 HTTP + gRPC 双传输，兼容性策略需要双层考虑：

1. **HTTP API 兼容**：URL 路径和 JSON 字段名一旦发布就不能改。新增字段用 `omitempty`。路由变更需提供旧路由的 301 重定向。
2. **gRPC API 兼容**：Protobuf 的字段编号和消息类型不能变。新增字段使用 `optional`。新的 RPC 方法无兼容问题。`breaking change` 必须映射为新服务版本（如 `aero_id.v2`）。
3. **数据库 Schema 兼容**：所有 migration 必须是 additive（加列/加表）。rename/delete 走软删除 + deprecation 通知。回滚必须测试。

---

## 四、技术选型

### 4.1 不需要引入新的技术栈

当前技术栈（Go 1.26 + Gin + GORM + PostgreSQL + protobuf + testcontainers）对于这个项目的规模**完全足够**。以下是我**不建议引入**的技术：

| 技术 | 理由 |
|------|------|
| **Kubernetes** | 过度设计。当前服务单二进制部署 + Docker Compose 就够。K8s 只会增加运维复杂度 |
| **消息队列（RabbitMQ/Kafka）** | outbox 轮询模式已经够用。对于当前负载（通知 < 1000/s），PostgreSQL 轮询的延迟（5s）完全可接受 |
| **Redis 缓存** | 当前数据库负载低（`MaxOpenConns: 50`），新加 Redis 引入的运维复杂度 > 收益。但未来分布式限流会需要它 |
| **GraphQL** | 双传输（REST + gRPC）覆盖了所有使用场景。GraphQL 引入的 N+1 问题和缓存复杂性不划算 |
| **gorilla/mux 或 chi** | Gin 已经是事实标准，且项目深度绑定（中间件、`gin.Context`）。切换路由框架的 ROI 为负 |

### 4.2 第三方依赖的评估标准

基于项目的 AGENTS.md 约束（Go 1.26、protoc-gen-go 版本锁定），建议以下评估标准：

| 标准 | 说明 |
|------|------|
| **维护活跃度** | GitHub 最近一次提交在 6 个月内 |
| **Go 版本兼容** | go.mod 要求 ≤ Go 1.24（避免强制升级项目版本） |
| **依赖大小** | 只引入要实现功能的最小包（非全库引入，如 `samber/lo` 不建议） |
| **许可证兼容** | MIT / Apache 2.0 / BSD；避免 AGPL |
| **测试覆盖** | 库本身测试覆盖 > 70%，且不依赖外部服务（数据库/网络） |

### 4.3 自建 vs 采购的决策依据

对于 preview pipeline（缩略图生成），出现三种方案：

| 方案 | 成本 | 延迟 | 维护 | 推荐度 |
|------|------|------|------|--------|
| A：自建 ffmpeg + libvips 子进程 | 低（Docker 镜像稍大） | 中 | 中 | ⭐⭐⭐ 推荐 P0 |
| B：集成 cloud 服务（Cloudinary/Imgix） | 高（按量计费） | 低 | 低 | ⭐⭐ 生产可考虑 |
| C：Go 原生库（`golang.org/x/image`） | 低 | 低（内存中处理） | 低 | ⭐ 功能有限（仅支持图片） |

**决策依据**：P0 阶段用方案 C（仅图片缩略图，`x/image` 无外部依赖）。后续需要视频/文档预览时升级到方案 A 或 B。

---

## 五、实施路线图

### 优先级排序

| 优先级 | 项目 | 估算 | 类型 | 前置依赖 |
|--------|------|------|------|----------|
| **P0** | 新旧结构统一（废弃旧包） | 3 天 | 架构债清理 | 无 |
| **P0** | `like_count` 原子化 | 0.5 天 | Bug 修复 | 无 |
| **P0** | `idempotency_keys` TTL 清理 | 0.5 天 | Bug 修复 | 无 |
| **P0** | Outbox handler exponential backoff | 1 天 | Bug 修复 | 无 |
| **P1** | 模块化注册（`app.go` 重构） | 2 天 | 架构改进 | P0 统一完成 |
| **P1** | 通知偏好设置（Direction 5） | 1 天 | 新功能 | 无 |
| **P1** | SSE `last_event_id` | 1 天 | 功能补全 | 无 |
| **P1** | 搜索 `statement_timeout` + 中文分词 | 3 天 | 功能改进 | 无 |
| **P2** | 管理员角色系统（Direction 4） | 5 天 | 新功能 | P1 模块化注册 |
| **P2** | 文件预览管道 - M1 Job Queue | 3 天 | 基础设施 | 无 |
| **P2** | 通知速率门限 + 折叠 | 2 天 | 功能改进 | 无 |
| **P3** | 文件预览管道 - M2 缩略图 | 5 天 | 新功能 | P2 Job Queue |
| **P3** | 多人群聊 - M1 数据模型 | 5 天 | 架构级变更 | P1 模块化注册 |
| **P3** | 多人群聊 - M2 发送/接收 | 5 天 | 新功能 | P3 M1 |
| **P3** | 分布式限流 + 缓存层 | 5 天 | 性能优化 | 无（但需要 Redis） |

### 阶段划分

```
Phase 0 (Current Sprint) — 架构债清理
  ├── 统一新旧结构 → 废弃 internal/service/ + internal/handler/{http,grpc}/
  ├── like_count 原子更新
  ├── idempotency TTL 清理后台 goroutine
  └── outbox exponential backoff

Phase 1 (Next Sprint) — 基础设施打磨 + 高 ROI 功能
  ├── 模块化注册（app.go 重构）
  ├── 通知偏好设置（日间交付）
  ├── SSE last_event_id 重连续传
  └── 搜索 statement_timeout + 中文分词（zhparser 或应用层分词）

Phase 2 (Medium-term) — 平台能力构建
  ├── 管理员角色系统
  ├── Job Queue 基础设施
  ├── 通知速率门限 + 折叠
  └── 文件预览 M1（图片缩略图）

Phase 3 (Long-term) — 架构级扩展
  ├── 多人群聊（M1 数据模型 → M2 发送 → M3 通知）
  ├── 分布式限流 + Redis 缓存
  └── 文件预览 M2（视频/文档预览）
```

### 关键风险与缓解策略

| 风险 | 可能性 | 影响 | 缓解 |
|------|--------|------|------|
| 新旧结构统一引入回归 | **中** | 高 | 统一前确保 `make test && make test-integration` 全绿；统一后旧代码先保留 1 周过期 |
| 多人聊天的数据迁移损坏现有消息 | **低** | 严重 | 迁移前备份 messages 表；迁移脚本在事务中执行；提供回滚脚本 |
| 缩略图工具（ffmpeg/libvips）在容器中不可用 | **中** | 中 | Dockerfile 中添加 multi-stage build 安装；提前 CI 验证 |
| 中文分词扩展（zhparser）需要 PostgreSQL 超级权限 | **高** | 中 | 改用应用层分词（gojieba）或 tsvector + 字典组合方案 |
| 模块化注册过度设计为"框架" | **中** | 低 | 保持简单——`Module` 接口不超过 5 个方法；先落地 1 个模块试点 |

---

## 总结

**现状**：aero-id 拥有异常清晰的 Go 微服务架构——分层干净、测试覆盖好、错误处理统一、文档充分。代码库的健康度在同类项目中属上乘。

**最紧迫的行动**是"新旧结构统一"——这是当前最大的架构债务，两个月的并行只会让维护成本指数增长。统一后，模块化注册将自然消除 `app.go` 的膨胀趋势。

**最高 ROI 的功能**是通知偏好设置（Direction 5），日间工作、影响全量用户、复用现有 Dispatcher 的回调模式。

**架构级扩展**（群聊、文件预览、缓存层）应该在基础设施债务清理 + 模块化注册完成后，安全、渐进地引入——每一步都不破坏现有功能的向后兼容性。
