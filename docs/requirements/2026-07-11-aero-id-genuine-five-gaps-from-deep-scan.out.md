现在我已具备了所有验证所需的证据。让我提供一份完整的审查报告。

---

## 审查结论：五个方向 — 逐项验证

我对代码库（全部 170+ Go 源文件、proto、迁移文件、wiring）和既有 `docs/requirements/` 文档集进行了逐项交叉验证。

> **目录计数**: 目前 `docs/requirements/` 下共有 **163 份源文档**（不含 `.out.md`），而非文档声明的 92 份。

---

### 方向一：双服务层架构债务 — ✅ 确认

该方向是此文档中最有力的一项发现。

**代码级证据 (已确认):**

1. **`internal/app/app.go:238-248`** 使用 `service.NewFileService(...)`、`service.NewCommentService(...)` 等来自 `internal/service/` 包的构造函数。它**从不**导入 `internal/{file,comment,message,block,notification}`。

2. **`internal/handler/http/*.go` 和 `internal/handler/grpc/*.go`** 全部注入来自 `internal/service` 的服务接口（如 `service.CommentService`）。**零**个 handler 导入 `internal/comment`、`internal/file` 等包。

3. **`internal/{file,comment,message,block,notification}/service.go`** 定义并实现了另一套完全独立的 `Service` 接口（例：`comment.NewService()` 在 `internal/comment/service.go`），但这些构造函数在任何 handler、app.go 或其他生产代码路径中均**从未被调用**。

4. **更深层的发现**：`internal/{file,comment,message,block,notification}/repository.go` 定义了**第二组完整的 GORM 仓库实现**，与 `internal/repository/` 中的实现平行。这使问题从"双服务层"升级为"双完整堆栈层"（仓库+服务）。

5. **唯一的跨包使用**: `internal/search/search.go` 导入 `comment.Repository`、`file.Repository`、`message.MessageRepository` 接口定义（而非服务），用于搜索服务。`internal/search/handler_grpc.go` 导入转换辅助函数 `comment.CommentToPB` 等。但这些都不涉及死掉的服务实现。

6. **测试也是死的**: `internal/{file,comment,message,block,notification}/service_test.go` 中的测试很全面，但它们测试的是从不执行的代码——虚假的安全感。

**修正建议**: 该文档估计 ~2000 行死代码。这是准确的。加上仓库实现，真实数字更接近 ~3000-4000 行死代码。

---

### 方向二：运行时系统配置 API — ⚠️ 部分正确（需重大修正）

该方向在根本性见解上是正确的（缺少运行时配置 API），但其代码级证据**有事实错误**。

**已发现的错误:**

1. 文档称 `PermSystemConfig` "没有任何对应的 API 端点——整个系统中没有一个端点检查这个权限" — **这是错误的**。

2. 实际使用情况:
   - **`internal/handler/http/admin.go:38-40`**: 一个 `syscfg` 路由组使用 `middleware.RequirePermission(domain.PermSystemConfig, h.permChecker)`，包含 `POST /v1/admin/roles/grant` 和 `POST /v1/admin/roles/revoke` 端点。
   - **`internal/handler/grpc/server.go:253-254`**: gRPC RBAC 映射将 `PermSystemConfig` 映射到 `AdminService/GrantRole` 和 `AdminService/RevokeRole`。

3. 然而，该权限用于**角色管理**，而非系统级配置变更（如其名称 `system.config` 所暗示）。Grant/Revoke 端点不是配置 API。

**结论**: 核心洞见有效（无运行时配置变更 API），但"零使用"断言是错误的。该文档应在发布前发现实际的使用位置。

---

### 方向三：批量操作 API — ✅ 确认

**代码级证据 (已确认):**

1. 所有仓库接口均为单资源：
   - `repository/message.go`: `SoftDelete(ctx, id)` — 单一 ID
   - `repository/file.go`: `Delete(ctx, id)` — 单一 ID
   - `repository/comment.go`: `SoftDelete(ctx, id)` — 单一 ID

2. Proto 确认: `api/proto/aero_id/v1/aero_id.proto` 中所有写 RPC 均使用单资源请求消息 (`DeleteFileRequest { string id = 1 }`、`DeleteMessageRequest { string id = 1 }` 等)。

3. 仅有类批量的端点: `MarkAllNotificationsRead`（无 ID）和 `MarkConversationRead`（按对话方，而非按 ID 列表）。无批量删除、批量取消分享、批量标记已读（多 ID）。

**细微差别**: `ShareFileRequest` 确实包含 `repeated string grantee_uids`，但这是用于单文件的多授权方——唯一真正的批量参数。

---

### 方向四：软删除回收站 — ✅ 确认

**代码级证据 (已确认):**

1. 全局使用 `gorm.DeletedAt` — 所有业务表均有 `deleted_at` 列。

2. 无恢复端点 — `grep` 返回零匹配（正确，但需注意 `recover()` panic 恢复会干扰结果——文档已正确过滤）。

3. 文档指出了 GC sweeper: `internal/storage/gc/sweeper.go:138-145` 在 retention 窗口后执行 `HardDelete`。用户无法在此之前的窗口中恢复。

4. **额外证据**: `internal/repository/file.go:50-52` 定义了 `ListSoftDeletedBefore`——GC 使用该方法完成其工作，但没有对应的 `ListSoftDeletedByOwner` 供用户恢复使用。

**产品影响**: 该文档低估了级联恢复的复杂性。恢复一个文件需要同时恢复其 `file_shares` 记录。恢复一个评论可能意味着恢复其子资源。这些边缘情况值得在实施前加以思考。

---

### 方向五：优雅维护模式 — ⚠️ 部分正确（需修正 grep 声明）

**已发现的错误:**

1. 文档称 `grep` "返回零（除了 shared 包中的常量命名）" — **这是错误的**。`internal/handler/http/health.go` **已**拥有一个"降级"概念。

2. `health.go:48-71`:
   ```go
   degraded := false
   // ...非关键检查失败设为 degraded = true
   case degraded:
       status = "degraded"
   ```
   健康端点已返回三个状态：`"ready"`、`"degraded"`（非关键依赖失败）、`"unavailable"`（关键依赖失败）。

3. 然而，该文档的核心见解是正确的：这种降级是**被动**的（基于依赖健康度），而非由操作者**主动**触发的。不存在按特征开关 API、维护模式端点或维护页面模板。

4. 工具链中的 `drain` 引用（relay、gRPC graceful shutdown）均涉及 outbox draining 和连接 draining——而非文档所寻求的维护模式功能。

---

## 既有文档集的交叉验证

您在 `docs/requirements/` 下有 **163 份文档**（非 `.out.md`），在 `docs/REVIEW_*` 下有 **7 份审查文档**，加上 `ARCHITECTURE.md`。我已扫描了文档标题模式，以验证"零系统性覆盖"的声明。

**方向一的覆盖情况**: 既有文档中**未**系统性覆盖双服务层死代码。这是一个真正的盲区。

**方向二-五的覆盖情况**: 批处理操作、回收站恢复、运行时配置和特征开关/降级等概念主题在不同需求文档中被偶尔提及，但**从未作为独立的、充分论证的方向出现**。就该文档所主张的具体形式而言，零覆盖的声明是站得住脚的。

---

## 优先级评估的修正

| # | 方向 | 文档优先级 | 我的修正 | 理由 |
|---|------|-----------|---------|------|
| 1 | 双服务层 | **P0** | **P0** ✅ | 没错。这是工程基础——在任何新功能开始前就应解决 |
| 2 | 回收站 | **P1** | **P1** ✅ | 同意。产品层面的基本数据安全保障 |
| 3 | 批量操作 | **P1** | **P2** ⬇️ | 有价值但较增量式。可在两套服务层统一之后通过数小时的仓库级工作完成 |
| 4 | 运行时配置 | **P2** | **P2** ✅ | 同意。高运维价值但依赖于服务层基础的稳定 |
| 5 | 维护模式 | **P2** | **P3** ⬇️ | 现有降级健康状态已覆盖最简单的用例。完整的特征注册表/路由条件化实现投入巨大。建议先完成方向一和方向二 |

---

## 总结

这份文档识别了架构中 **3 个真正盲区**（方向一、三、四）和 **2 个方向**，其核心见解有效但代码级证据存在细微的事实错误（方向二、五）。

**最有力的发现是方向一**（双服务层死代码）——这一严重的架构问题在其他 163 份需求文档中均未被发现。无论整体优先级如何，这都应在工程 backlog 中作为 P0 处理。

该文档将在发布前从以下修正中获益：
1. 修正"`PermSystemConfig` 零使用"的声明，以认可其在角色管理中的实际使用
2. 认可 health.go 中已有的"降级"概念，同时澄清其为何不能满足所提议的维护模式 API
3. 将 92 份文档的计数更新为实际数量（163 份源文档）
4. 明确指出 `internal/{file,comment,...}/repository.go` 中的额外仓库死代码，使影响范围估算更准确
