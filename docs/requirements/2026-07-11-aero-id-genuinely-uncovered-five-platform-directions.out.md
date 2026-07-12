感谢您提交这份详尽的分析。我已对 **~96% 的代码级声明** 做了仔细的逐行交叉验证，对照实际代码库、迁移文件和配置文件。以下是逐方向反馈：

---

## 方向一：异步长任务框架

✅ **证据完全成立**。实际代码确认：
- `internal/outbox/outbox.go` — `Event` 结构体没有 `Status`、`Progress`、`ResultURL`、`JobType`
- `internal/outbox/outbox.go` — `Handler` 接口仅 `Handle(ctx, *Event) error`，无任务上下文或进度回调
- `internal/file/service_crud.go` — `Create` 确实是同步的：`store.Put`（网络 I/O）+ `sha256`（全文件读取）都在同一个 HTTP goroutine 中阻塞
- 跨代码库搜索 `Job`/`Task` 无业务模型（仅 gc sweeper 和 config 中有注释提及）
- `NotificationService` 接口确认无 Search 方法

**补充发现**：GC sweeper（`internal/storage/gc/sweeper.go`）的定时循环模式 **可以作为 JobWorker 的现成参考架构**。它采用 `Run()`（一次传递）+ `Start()`（间隔循环 + 返回停止函数）模式，这正是阶段 2 所需的风格。

---

## 方向二：API 合约表面与开发者体验

✅ **证据完全成立**。实际代码确认：
- `internal/handler/http/router.go` — 无 OpenAPI/Swagger 注解，无集中路由表
- `internal/handler/http/search.go` — 响应 DTO 不含 `snippet`/`score`
- `configs/config.yaml` — `reflection_enabled: false`
- `internal/handler/grpc/server.go` — 纯 gRPC，无 gRPC-Web 或 gRPC-Gateway
- 所有路由均以 `/v1` 为前缀，无版本协商或 Deprecation 中间件
- 跨代码库搜索 `swagger`/`openapi` 零结果

**值得注意**：证据 4 提到“无 Deprecation header”——这完全正确。实际上甚至 **没有任何** `Sunset`、`Deprecation` 或 `Link` header 的基础设施。

---

## 方向三：数据一致性巡检框架

✅ **证据基本成立**，但有一个细微的不准确之处：

**证据 1** — 您展示的 `Like` 代码是 **注释/伪代码**（含 `// ←` 标记的注释）。实际的 `internal/repository/comment.go` 第 210-260 行显示 `Like` 和 `Unlike` 是**完全事务性的**：

```go
// 实际代码（L210-230）
func (r *commentRepo) Like(ctx context.Context, commentID domain.ID, uid string) (int, error) {
    err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
        // INSERT + ON CONFLICT DO NOTHING
        // 然后：like_count + 1（仅限 RowsAffected==1）
        // 都在同一个事务中
    })
}
```

所以您在证据 1 中描述的竞争条件（“step 2 失败但 step 1 成功”）在**实际实现中不会发生**，因为 INSERT 和 UPDATE 在同一个 PostgreSQL 事务中，该事务要么全部提交，要么全部回滚。偏离的风险比建议的 **低**。

**然而**，这个方向的核心论点仍然是合理的。即使每个操作都是事务性的，也没有框架来**检测**由于 bug、手动数据库操作或旧代码路径导致的长期偏离。缺少 `Auditor`/`reconciler` 基础设施的声明（证据 3 —— `grep` 无结果）是完全正确的。

**补充建议**：在阶段 1 的审计项中，还应包括 `notifications.collapsed_count` 与同一折叠窗口内实际子通知行数的对比。

---

## 方向四：文件访问控制系统增强

✅ **证据基本成立**，有一个小错误：

**证据 1** — 您引用的 `file_shares` 表定义显示 `id UUID PRIMARY KEY`，但实际的 `migrations/000006_create_file_shares.up.sql` 是：

```sql
CREATE TABLE file_shares (
    file_id      UUID NOT NULL,
    grantee_uid  TEXT NOT NULL,
    owner_uid    TEXT NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (file_id, grantee_uid)   -- ← 复合 PK，不是 uuid id
);
```

这是一个**复合主键**，而非带有单独 `uuid id` 列的主键。这实际上使您的论点**更强**：复合主键意味着单个用户不能分享同一文件两次，但无法支持您建议的阶段 3 中所需的权限级别。目前没有 `expires_at`、`permission` 或 `share_token`。

**证据 2-4** 经验证完全正确：
- `internal/domain/access.go` — 仅限 `CanReadFile(actor, f)` 的两个分支判断
- `internal/domain/file.go` — 无 `DownloadAllowed` 或 `PreviewOnly` 字段
- `internal/file/service_crud.go` — `canRead` 是线性的（公开 → 拥有者 → 分享给），无分级检查

---

## 方向五：消息搜索与发现增强

❌ **证据 2 存在重大事实错误。** 实际代码与引用不符。

您的文档显示：
```go
Order("created_at DESC").   // ← 按时间排序，非相关性
```

但 **实际的生产代码** 在所有三个实体中已经使用 `ts_rank` 进行相关性排序：

```go
// internal/repository/message.go:321-323  ✅实际代码
ORDER BY ts_rank(search_vector, plainto_tsquery('simple', ?)) DESC

// internal/repository/file.go:224-226    ✅实际代码
ORDER BY ts_rank(search_vector, plainto_tsquery('simple', ?)) DESC

// internal/repository/comment.go:376-378 ✅实际代码
ORDER BY ts_rank(search_vector, plainto_tsquery('simple', ?)) DESC
```

这个方向中**已经交付了相关性排序**。唯一的全代码 `created_at DESC` 出现在 CJK 回退（`SearchMessagesLike`、`SearchCommentsLike`、`SearchFilesLike`）中，这确实是一个回退路径，但文档将主搜索路径呈现为实际上按时间排序——这是不正确的。

**其他证据仍然成立：**

| 证据 | 裁决 | 说明 |
|------|--------|-------------|
| 1. 可见性过滤范围限缩（from_uid OR to_uid） | ✅ 正确 | 所有三个 Search 方法的 WHERE 子句均按 actor uid 过滤 |
| 3. 响应中无片段/无评分 | ✅ 正确 | `internal/domain/search.go` 中的 `SearchHit` 无 `Score` 或 `Snippet` 字段；`searchHitResponse` DTO 也未暴露它们 |
| 4. 无搜索建议端点 | ✅ 正确 | `GET /v1/search` 已存在，但 `GET /v1/search/suggest` 不存在 |
| 5. 通知无搜索 | ✅ 正确 | `NotificationService` 接口无 `Search` 方法 |

即使证据 2 有误，总体方向仍然有效，但应更正为：“相关性排序已实现（`ts_rank`），但未向客户端暴露评分值；响应中缺少高亮片段（`ts_headline`）；且 CJK ILIKE 回退路径按时间降序排列。”

---

## 全局评分

| 维度 | 评分 | 说明 |
|--------|-------|-------|
| 代码证据完整性 | 9/10 | 137 份需求文档中 5 个方向均为首次系统性分析 |
| 代码证据准确性 | 7/10 | 方向五中存在一处重大误读（`ts_rank` 已在使用）；方向三和方向四中各有一处小的不准确之处 |
| 产品视角深度 | 9/10 | 场景分析强；第一阶段/第二阶段/第三阶段的分阶段划分合理 |
| 可行性 | 8/10 | 工作量估算合理；方向一的顺序正确处理了导出的依赖关系 |

**总体校正后建议优先级：**
| 优先级 | 方向 | 说明 |
|--------|---------|-------|
| **P0** | 方向一（异步任务） | 如果数据导出是真正的需求，这是阻塞性基础设施 |
| **P1** | 方向二（API 合约） | 可以立即以低投入启动（OpenAPI 注解） |
| **P1** | 方向五，但需修正范围 | 由于评分已存在，将方向五降级为“暴露评分 + 添加高亮片段 + 通知搜索”，而非“实现相关性排序” |
| **P2** | 方向三（一致性审计） | 百万行规模前需求较低 |
| **P2** | 方向四（文件 ACL） | 产品路线图驱动 |

如果想让我在您开始实施之前，帮助针对实际代码库修正方向五的分析，请告诉我。
