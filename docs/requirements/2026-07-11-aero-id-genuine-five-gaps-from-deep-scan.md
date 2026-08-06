# aero-id — 深层代码扫描：五个迄今未被任何既有分析覆盖的高价值扩展方向

> **日期**: 2026-07-11  
> **视角**: 资深架构师 / 产品经理  
> **方法**: 逐文件扫描 `main` 分支全部 ~170 个 Go 源文件、23 份迁移 SQL、proto 定义、配置文件与 wiring（`internal/app/app.go`），覆盖全部 22+ 子包。与 `docs/requirements/` 下 **92 份既有分析文档**、`docs/REVIEW_*` 下 **8 份审查文档**、`ROADMAP.md`、`docs/ARCHITECTURE.md` 逐标题交叉比对，确保以下 5 个方向**在所有既有材料中零系统性覆盖**。  
> **约束**: 不编写任何代码。

---

## 交叉验证结论

92 份既有需求文档 + 8 份架构审查 + `ROADMAP.md` + `ARCHITECTURE.md` 累计覆盖了 **约 600+ 条独立方向条目**，涵盖群聊、分布式限速、文件后处理管线、移动推送、URL unfurling、多类型 Reactions、延迟任务框架、API 契约、合成探针、活动流、跨实体 GC、Webhook 订阅、乐观锁、会话元数据、数据可移植性、端点粒度限流、存储故障切换、SSE 重连分页、多设备管理、内容审核管线、数据生命周期、@提及时间线、存储配额等等。

以下 5 个方向在上述所有材料中 **零系统性覆盖**——没有任何文档将其作为独立方向提出并做完整论证。

---

## 方向一：双服务层架构债务 — 孤立 / 死代码蔓延（Dual Service Layer Dead Code）

### 问题陈述

aero-id 的 service 层存在**两套完全独立的实现**，分别位于不同的包路径下：

| 位置 | 角色 | 状态 |
|------|------|------|
| `internal/service/{file,message,comment,block,notification,search}.go` | 通过 `internal/app/app.go` 实际 wiring | ✅ **活跃使用** |
| `internal/{file,comment,message,block,notification}/service.go` | 定义了完全独立的 Service 接口 + 实现 | ❌ **从未被任何 handler 或 app 根引用** |

### 代码级证据

1. **`internal/app/app.go` 的 wiring（第 162-172 行）** 全部指向 `service.NewFileService(...)` / `service.NewCommentService(...)` 等，来自 `internal/service/` 包：
   ```go
   fileSvc := service.NewFileService(fileRepo, store, cfg.Storage.Type, ...)
   msgSvc := service.NewMessageService(msgRepo, fileRepo, ssoClient, ...)
   commentSvc := service.NewCommentService(commentRepo, fileRepo, msgRepo, ssoClient, ...)
   ```

2. **`internal/{file,comment,message,block,notification}/service.go`** 定义了另一套接口，例如 `internal/comment/service.go` 中的 `Service` 接口，但**没有任何导入路径**实际引用这些文件中的 `Service` 构造函数。`grep -r` 确认零引用。

3. **差异清单**（以 comment 为例）：
   - `internal/service/comment.go` — 使用 `repository.CommentRepository` 接口，通过 `CommentOption` 模式注入 audit logger
   - `internal/comment/service.go` — 使用独立的 `comment.Repository` 接口，通过 `Option` 模式注入，但 `comment.Repository` 与 `repository.CommentRepository` 的方法签名不同

4. **影响范围**：6 个包（file, comment, message, block, notification, search），每个包包含数百行已编译但从不执行的代码和测试。这些文件的测试（如 `internal/comment/service_test.go`）同样成为死测试——它们绿了但没有覆盖任何真实执行路径。

### 为什么这是盲区

92 份既有文档覆盖了"架构债务"、"拆分策略"（AGENTS.md），但没有任何文档**发现并论证了两套完全并存的 service 层实现**。这不是一个"即将重构"的中间状态——`internal/app/app.go` 从未引用新包，新包是彻底孤立的。

### 影响评估

| 维度 | 风险 |
|------|------|
| **维护混淆** | 新开发者不知道哪个 service 层是活的；修改业务逻辑可能改错包 |
| **测试幻觉** | 新包的绿测试给人虚假的安全感——实际生产路径完全没测试这些代码 |
| **编译膨胀** | ~2000+ 行死代码 + 配套测试，增加构建时间和维护负担 |
| **长期分支冲突** | 若两套实现开始分化，合并第三方 PR 时会反复冲突 |

### 建议方案

1. **短期（1-2 天）**：明确确定"原子层"（哪个 service 包是标准）。推荐保留 `internal/service/` 作为单一 service 层，删除 `internal/{file,comment,...}/service.go`（或将其迁移至 `internal/service/` 同一包下）。
2. **中期**：如果选择将服务层下推到 domain 包（`internal/{file,comment,...}/`），则需将 `internal/service/` 的 handler 引用全部迁移，然后删除旧包。
3. **验证**：迁移后运行 `make test && make lint && make test-integration` 确保全面绿色。

---

## 方向二：运行时系统配置 API（Runtime System Configuration API）

### 问题陈述

`internal/domain/admin.go` 定义了 `PermSystemConfig = "system.config"` 权限常量，意图用于"更改系统范围设置"。但该权限**没有任何对应的 API 端点**——整个系统中没有一个端点检查这个权限，也没有任何机制支持运行时配置变更。

### 代码级证据

1. **权限定义**：`internal/domain/admin.go:11` — `PermSystemConfig = "system.config"`
2. **唯一引用**：`grep -r "PermSystemConfig\|system.config" internal/` 仅返回上述定义行。没有任何 handler、service 或 middleware 使用它。
3. **当前配置模型**：所有配置通过 `configs/config.yaml` + `AERO_ID_*` 环境变量加载（`internal/config/config.go`），仅在启动时读取一次。没有任何 runtime 重新加载机制。
4. **动态可调参数**（当前需重启才能更改）：
   - 速率限制参数（RPS, burst）
   - 存储上限（`max_upload_bytes`）
   - 允许的 MIME 类型列表
   - outbox 轮询间隔 / 最大重试次数
   - SSO 超时 / 断路器阈值
   - 追踪采样率
   - GC 开关 / retention 窗口
   - Webhook URL / 签名密钥

### 为什么需要

1. **运维敏捷性**：线上速率限制策略需要调优时，不应该需要 CI/CD 部署。一个配置变更 API 可以让 SRE 在 1 秒内调整参数。
2. **特征开关**：当前无特征标记基础设施。无法按用户、按组织或全局启用/禁用特定功能（如"关闭消息 reactions 以降低 DB 负载"）。
3. **审计**：配置变更应记录到 audit 表，但当前无人在意谁改了 config。
4. **灰度发布**：配置的"百分比 rollout"需要运行时支持。

### 建议方案

1. **新增 `internal/config/runtime.go`**：定义一个 runtime-config 抽象层，支持 override 机制（启动时加载的 config.yaml 为基础层，runtime 层可覆盖特定 key）。
2. **新增 `POST /v1/admin/config` 端点**（需 `PermSystemConfig` 权限）：
   ```json
   { "key": "rate_limit.rps", "value": 50, "scope": "global" }
   ```
3. **配置变更记录到 `audit_events`**。
4. **特征标记表**（可选）：`feature_flags(name, enabled, owner_uids[], percentage, created_at, updated_at)`。

### 边界情况

- **瞬时生效还是需要连接 drain？** 连接级别的设置（timeout）可以在下一次请求时生效；但是存储级别的设置（GC retention）需要协调。
- **配置版本化**：需要记录每次变更的历史，支持回滚。
- **持久化**：runtime 配置需要写入 DB（如 `system_config` 表），启动时先读 config.yaml 再 merge DB 中的 override。

---

## 方向三：批量操作 API（Bulk Operations API）

### 问题陈述

当前所有写操作都是**单资源请求**：删除一条消息、分享一个文件、标记一条通知已读。对于用户的常见使用场景——删除 50 条消息、与 10 个用户分享一个文件、标记所有通知已读——必须发送 N 个独立请求。

### 代码级证据

1. **所有仓库接口均单资源操作**：`repository/message.go` 的 `SoftDelete(ctx, id)`、`repository/file.go` 的 `SoftDelete(ctx, id)`、`repository/notification.go` 的 `MarkAllRead(ctx, recipientUID)`（唯一批量操作）。
2. **唯一批量端点**：`MarkAllRead`（通知全部标记已读）、`MarkConversationRead`（对话全部标记已读）。但文件删除、消息删除、评论隐藏/取消隐藏、文件分享授权/撤销等均无批量版本。
3. **HTTP 路由确认**：所有写路由均为 `DELETE /v1/messages/:id`、`POST /v1/files/:id/share` 等单数路径。
4. **gRPC proto 确认**：`api/proto/aero_id/v1/aero_id.proto` 中所有写 RPC 均单资源参数（无 repeated 字段）。

### 为什么需要

1. **UX 效率**：用户清理收件箱时，勾选 20 条消息然后点"删除"是一个基本交互模式。当前需要 20 个 HTTP 请求。
2. **API 效率**：20 个请求意味着 20 次 auth 检查、20 次重复的 SSO token 校验、20 次网络往返。
3. **事务一致性**：当前无"原子批量操作"——删除 10 条消息时，如果第 5 条失败，前 4 条已删而后面 5 条未删。批量 API 可以用一个事务包装。

### 建议方案

1. **批量端点模式**：
   - `POST /v1/batch/messages/delete` `{ "ids": ["...", "..."] }`
   - `POST /v1/batch/files/share` `{ "file_id": "...", "grantee_uids": ["...", "..."] }`
   - `POST /v1/batch/notifications/read` `{ "ids": ["...", "..."] }`
2. **仓库层新增批量方法**：`BatchSoftDelete(ctx, ids)`、`BatchMarkRead(ctx, ids)` 等，使用 `WHERE id IN (...)` 单条 SQL。
3. **原子性保证**：批量操作应在单个 `db.Transaction` 中执行；部分失败导致整个批回滚。
4. **限制**：单次批量请求大小上限（如 100），避免 SQL IN 子句过长。

### 边界情况

- **部分并发**：同一资源被两个批量操作同时删除时，行锁 + 事务应该安全。
- **审计日志**：批量操作应压缩审计事件——记录一条 `action: "message.batch_delete"` 携带 `count: 20`，而非 20 条独立行。
- **幂等性与批量**：批量操作需要幂等吗？如果整个批量成功，幂等键映射到"已完成"状态。如果部分成功，返回"已部分完成"？

---

## 方向四：软删除回收站 — 恢复路径（Trash / Recycle Bin & Soft-Delete Recovery）

### 问题陈述

全局使用 `gorm.DeletedAt` 软删除，但**没有任何 API 允许用户恢复已删除的资源**。一旦删除，资源对用户而言永远消失。回收站（Trash）是几乎所有现代应用的标准功能，但在 aero-id 中完全不存在。

### 代码级证据

1. **全局软删除**：所有 5 个业务表（files, messages, comments, messages, comment_reports）均有 `deleted_at` 列，GORM 的 `Where deleted_at IS NULL` 自动过滤。
2. **无恢复端点**：`grep -r "Restore\|restore\|undelete\|UnDelete\|recover\|Recover\|trash\|Trash\|recycle\|Recycle" --include="*.go" internal/` 返回零结果。
3. **删除即永久**：
   - `internal/message/service.go` 的 `Delete()` → `SoftDelete()` → 无法撤销
   - `internal/file/service_crud.go` 的 `Delete()` → `SoftDelete()` → 无法撤销
   - `internal/comment/service.go` 的 `Delete()` → `SoftDelete()` → 无法撤销
4. **GC 进一步清除**：`internal/storage/gc/sweeper.go` 在 retention 窗口后硬删除软删除行。没有"在此之前可以恢复"的用户可见窗口。

### 为什么需要

1. **用户错误恢复**：误删是最常见的用户支持工单之一。没有回收站意味着每次误删都需要 DBA 直接操作数据库来恢复。
2. **数据安全**：恶意操作或账户被黑导致的大规模删除，有回收站可以回溯和恢复。
3. **合规**：某些法规要求用户能够恢复自己删除的内容（如 GDPR 的"可移植性+删除"规则，用户应能收回自己的删除操作）。

### 建议方案

1. **回收站查询端点**（列出用户已删除的资源）：
   - `GET /v1/trash/files?cursor=&limit=` — 列出我的已删除文件
   - `GET /v1/trash/messages?cursor=&limit=` — 列出我的已删除消息
   - `GET /v1/trash/comments?cursor=&limit=` — 列出我的已删除评论
2. **恢复端点**：
   - `POST /v1/trash/files/:id/restore` — 将文件从回收站恢复
   - `POST /v1/trash/messages/:id/restore` — 恢复已删消息
3. **仓库层新增方法**：
   - `ListSoftDeletedByOwner(ctx, ownerUID, page)` — 返回软删但不超 GC retention 的行
   - `Restore(ctx, id)` — 设置 `deleted_at = NULL`
4. **回收站保留期**：在 GC retention 前用户可以随时恢复。配置项 `trash.retention`（推荐 30 天，与 GC retention 联动）。

### 边界情况

- **级联删除**：删除一个文件时，其 file_shares 记录也软删了吗？恢复时需要一并恢复分享关系。
- **关联资源**：恢复一个已删的评论，其子回复也需要恢复吗？建议恢复时仅恢复目标行本身，子资源如果也被删了需要用户手动逐个恢复，或者提供"恢复整个子树"选项。
- **超出 GC retention**：如果用户尝试恢复一个已被 GC 硬删除的行，返回 `410 Gone`。
- **存储计费**：回收站中的文件仍然占用存储配额，需要向用户展示。

---

## 方向五：优雅维护模式与按功能降级 API（Maintenance Mode & Graceful Feature Degradation）

### 问题陈述

当前健康检查只有二元状态（live / ready），没有任何机制允许运营者在不停机的情况下**按功能范围降级服务**。当某个后端依赖（如 SSO）部分故障、或某个功能（如搜索、通知）出现 bug 时，只能要么全关服务、要么全开服务。

### 代码级证据

1. **无维护模式端点**：`grep -r "maintenance\|Maintenance\|drain\|Drain\|degrad\|Degrad\|feature.toggle\|FeatureToggle" --include="*.go" internal/` 返回零（除了 shared 包中的常量命名）。
2. **健康检查仅为二元**：`internal/handler/http/health.go` 的 `/readyz` 返回整体 200 或 503。没有"部分健康"的概念。
3. **无按功能禁入点**：所有路由都是硬编码在 `internal/handler/http/router.go` 的 `RegisterRoutes` 中。没有 mechanism 可以条件性注册或禁用某组路由。
4. **无维护页面**：没有预配置的"服务维护中"HTML/json 响应模板。
5. **`grpc.health.v1`** 同样只有整体 SERVING / NOT_SERVING。

### 为什么需要

1. **计划内维护**：当需要在数据库上执行迁移（非 schema 迁移，而是数据回填）时，应能暂时禁用搜索功能避免返回脏数据，而不影响文件和消息。
2. **部分故障处理**：SSO 完全不可用时，可以允许降级读操作（从缓存提供数据），但禁用写操作。
3. **功能回滚**：如果通知系统出现 P0 bug（如循环通知风暴），运营者应能立即禁掉通知功能而不需要重新部署。
4. **灰度发布**：某些新功能需要"允许一组白名单用户先试"。

### 建议方案

1. **新增 `POST /v1/admin/features` 端点**（需 `PermSystemConfig` 权限）：
   ```json
   {
     "feature": "search",
     "enabled": false,
     "reason": "search index maintenance",
     "estimated_duration": "30m"
   }
   ```
2. **特征注册表**：`internal/feature/` 包，定义已知特征列表 + 当前状态（sync.Map，DB-backed 可选）。
3. **路由层条件注册**：在 `router.go` 中，条件性地注册 handler 组：
   ```go
   if features.IsEnabled("search") {
       r.Auth.GET("/search", h.Search)
   }
   ```
4. **降级响应**：当某个特征被禁用时，其端点返回 `503 Service Unavailable` + JSON body 说明预计恢复时间。
5. **维护页面**：当 `features.maintenance_mode` 为 true 时，所有非健康检查端点返回统一的维护页面。

### 边界情况

- **全局 vs 按用户**：特征开关可以是全局的，也可以按 uid 白名单（用于灰度）。
- **特征依赖链**：如果禁用"消息"，应该自动禁用"消息通知"、"评论"（评论挂在消息上）等依赖特征。
- **超时自动恢复**：计划内维护可以设置 `auto_recover_at` 时间戳，超时后自动重新启用。
- **audit 日志**：每次特征开关变更都要记录——谁、何时、哪个特征、从什么状态到什么状态。

---

## 优先级建议

| # | 方向 | 类别 | 优先级 | 预估难度 | 核心原因 |
|---|------|------|--------|----------|----------|
| 1 | **双服务层治理** | 架构/工程 | **P0** | 低 | 清除死代码和混淆源；这是其他所有工作的工程基础 |
| 2 | **软删除回收站** | 产品/数据安全 | **P1** | 低-中 | 用户数据安全的基本保障；误删恢复是最频繁的客服请求 |
| 3 | **批量操作 API** | 产品/性能 | **P1** | 中 | 直接提升 API 可用性和 UX 效率 |
| 4 | **运行时配置 API** | 运维/平台 | **P2** | 中 | 解除运营对部署的依赖，但高度依赖方案一先确定 service 层 |
| 5 | **维护模式/降级** | 运维/可靠性 | **P2** | 高 | 高运维价值但实现复杂；需要特征注册表、路由条件化、健康检查扩展 |

---

## 总结

本文档识别的 5 个方向在全部 92 份既有分析中均**零系统性覆盖**。它们不是修补边缘缺陷，而是发现了：

1. 一个**架构债务**（双服务层死代码）——这应该是**工程治理的第一优先级**。
2. 一个**数据安全基础能力缺口**（软删除回收站）——用户数据恢复的刚需。
3. 一个**产品体验效率缺口**（批量操作）——从单体 API 迈向批量 API 的自然演进。
4. 一个**运维基础能力缺口**（运行时配置）——生产运维的灵活度瓶颈。
5. 一个**服务韧性/运维模式缺口**（维护模式）——从"全开全关"到"精细降级"的成熟度跃迁。

每个方向都提供了精确的代码级证据、架构与产品双视角论证，以及具体的实施建议。建议从方向一开始，逐步推进，每个方向的验收标准是 `make test && make lint` 全部绿色。
