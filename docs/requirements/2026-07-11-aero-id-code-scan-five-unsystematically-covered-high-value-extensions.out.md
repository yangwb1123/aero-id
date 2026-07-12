所有证据点已核实完毕。以下是我的完整回应。

---

## 回应：五个方向的代码级验证结果

我逐行比对了实际代码库（`internal/` 下 22 个子包、`configs/config.yaml`、`migrations/`、`api/proto/`），针对每个方向的所有证据点进行了验证。

---

### ✅ 方向一：Cursor 分页的完整性——**证据全部属实**

| 声明 | 验证结果 | 代码锚点 |
|------|---------|---------|
| Cursor 是纯 UUID 字符串，无元数据 | ✅ 准确 | `domain.Page.Cursor string`（`internal/domain/shared.go:37`）|
| Repository 直接 `WHERE id < ?` | ✅ 准确 | `internal/repository/message.go:126`, `file.go:85`, `notification.go:69`, `comment.go:136` 等 |
| 无查询上下文绑定 | ✅ 准确 | 所有列表端点均直接透传 cursor |
| 无过期机制 | ✅ 准确 | `Page` 结构体没有 TTL 或签名字段 |
| 前端可直接复用 cursor | ✅ 准确 | `httpx.PageFromQuery`（`internal/pkg/httpx/httpx.go:58`）仅 `c.Query("cursor")` |

**补充发现**：`block` 仓库使用 `WHERE created_at < ?`（`internal/repository/block.go:82`）而非 `id`，但同样无上下文绑定。

---

### ✅ 方向二：API 生命周期管理——**证据基本属实，有一处证据需修正**

| 声明 | 验证结果 | 代码锚点 |
|------|---------|---------|
| 路由硬编码 `/v1` | ✅ 准确 | `router.go:93` — `e.Group("/v1", authMW...)` |
| 无版本协商 | ✅ 准确 | `RouterConfig` 无 `Accept-Version` |
| Proto 无版本化 namespace | ✅ 准确 | `package aero_id.v1` 有版本号，但无机制支持 v1/v2 共存 |
| 无 `Deprecation`/`Sunset` 响应头 | ✅ 准确 | `grep -r "Deprecation\|Sunset"` 返回空 |
| 响应格式统一无演化预留 | ✅ 准确 | `httpx.OK` 总是 `{"data": ...}`（`httpx.go:48`）|

**⚠️ 关于"死代码"声明的修正**：

文档称 `internal/{file,comment,...}/service.go` 定义了独立的 service 接口但未被 `internal/app/app.go` 引用。事实更复杂：

- **真实**：存在两套并行的 service 实现——`internal/service/file.go`（被 `app/new.go` 引用，是活的）和 `internal/file/file.go`（仅被 test 和 `internal/search/` 引用，确实从未被生产代码调用）
- **真实**：`internal/search/` 整个包是死代码——其 `NewService()` 从未被 `app/new.go` 引用，只在自身 test 中被引用
- **真实**：`internal/file/handler_http.go`、`internal/comment/handler_http.go` 等 handlers 也是死代码——`app/new.go` 使用的是 `internal/handler/http/*.go` 中的 handler

所以"存在死代码"的结论正确，但不仅仅是 service interface，而是整个 `internal/{file,comment,message,block,notification,search}/` 包中的 service + handler 实现是废弃的。

---

### ✅ 方向三：Per-Conversation 消息搜索——**证据全部属实**

| 声明 | 验证结果 | 代码锚点 |
|------|---------|---------|
| SearchService 无 `peerUID` 参数 | ✅ 准确 | `Search(ctx, query, kinds, limit)` → `internal/service/search.go:28` |
| HTTP handler 只解析 `q`/`kind`/`limit` | ✅ 准确 | `internal/handler/http/search.go:34-50` |
| FTS 索引已存在可复用 | ✅ 准确 | `migrations/000016_fts_indexes.up.sql` — `GIN(search_vector)` |
| Repository 已有对话查询方法但搜索未复用 | ✅ 准确 | `ListConversation(ctx, actor, peerUID, page)`（`internal/repository/message.go:144`）vs `SearchMessages(ctx, uid, query, limit)`（`internal/repository/message.go:350`） |

**补充发现**：`SearchMessages` 在 `WHERE` 中已做参与者 ACL 过滤（`WHERE (from_uid = ? OR to_uid = ?)`），增加可选 `peerUID` 过滤只需加一个 `AND` 条件。

---

### ✅ 方向四：Per-Endpoint 速率限制——**证据全部属实**

| 声明 | 验证结果 | 代码锚点 |
|------|---------|---------|
| 全局单配置 `rate_limit.rps: 20` | ✅ 准确 | `configs/config.yaml` |
| 无 `endpoints` 级别配置块 | ✅ 准确 | 同上 |
| Middleware 单一 key 无端点区分 | ✅ 准确 | `RateLimit` → `rateLimitKey(c)` 返回 `"uid:xxx"` 或 `"ip:xxx"`（`middleware/ratelimit.go:56-59`） |
| 同一 limiter 实例被所有端点共享 | ✅ 准确 | 构建时创建一个 `*ratelimit.Limiter` 传给 HTTP router 和 gRPC server（`app/new.go:240-244`） |

**补充发现**：`gin.RouterGroup` 支持嵌套中间件，为不同路由组绑定不同 limiter 实例是可行的。当前所有路由共享同一个 limiter。

---

### ✅ 方向五：DB Statement-Level 超时——**证据全部属实**

| 声明 | 验证结果 | 代码锚点 |
|------|---------|---------|
| 无 per-query context deadline 传播 | ✅ 准确 | 除搜索外，所有 repository 方法均使用从 service 传入的 ctx，而 service 不在 ctx 上设 deadline |
| 搜索是唯一设置 timeout 的路径 | ✅ 准确 | `searchTimeout = 5 * time.Second`（`service/search.go:23`）；`context.WithTimeout(ctx, searchTimeout)`（`service/search.go:89`） |
| GORM 无 `statement_timeout` | ✅ 准确 | `grep -rn "statement_timeout" internal/` 返回空 |
| 连接池共享无读/写分离 | ✅ 准确 | `db.Open()` 创建单一 `*gorm.DB`（`pkg/db/db.go`）；`app/new.go:36-46` |
| 无 DB 连接池健康指标 | ✅ 准确 | `metrics.Registry` 无 `DBStats` 相关字段（`pkg/metrics/metrics.go`） |

---

## 对文档的修正与补充

### 1. 方向二——"死代码"范围比所述更广

不仅是 `internal/{file,comment,...}/service.go`，以下都可被认为是废弃的：

```
internal/file/handler_http.go          # 死代码（未被 app/new.go 引用）
internal/file/handler_grpc.go          # 死代码
internal/comment/handler_http.go       # 死代码
internal/comment/handler_grpc.go       # 死代码
internal/message/handler_http.go       # 死代码
internal/message/handler_grpc.go       # 死代码
internal/search/handler_http.go        # 死代码
internal/search/handler_grpc.go        # 死代码
internal/search/search.go              # 死代码（NewService 从未被生产代码调用）
internal/block/handler_http.go         # 死代码
internal/notification/handler_http.go  # 死代码

total: ~11 个文件，估计 800+ 行 Duplicate 代码
```

### 2. 方向一——cursor 缺少的不仅文档所述两点

`block` 仓库使用 `WHERE created_at < ?` 作为 cursor（`block.go:82`），而非 `id`。cursor 格式在不同实体间不统一（`id` vs `created_at`），这会破坏任何尝试统一解析的尝试。文档未提及此不一致。

### 3. 方向五——数据库连接池的空闲连接 `max_idle_conns` 配置偏低

`max_idle_conns: 10` vs `max_open_conns: 50`（`configs/config.yaml`）。在低流量时，连接池只会保留 10 个空闲连接。当突发请求激增时，剩余的 40 个连接需要新建 TCP 连接到 PostgreSQL，增加了 P50 延迟。这是一个额外的"免费"优化点，文档未提及。

### 4. 方向五——除了 `statement_timeout`，还缺失 `idle_in_transaction_session_timeout`

PostgreSQL 的 `idle_in_transaction_session_timeout` 可以防止因应用层 bug 导致的事务挂起连接。当前代码库未设置此参数，这也是一个常见的生产稳定性隐患。

---

## 总体评估

| 维度 | 评分 | 说明 |
|------|------|------|
| 证据准确性 | **4.7/5** | 5 个方向共 ~30 个独立证据点，仅"死代码"描述范围偏窄一处需修正 |
| 技术深度 | **5/5** | 每个方向的证据链完整、代码级定位精确 |
| 可操作性 | **5/5** | 优先级排序合理（P0→P2），工作量估算务实 |

五个方向选择恰当——它们代表的是"基础设施级"缺口（错误边界、版本化、限流、查询治理），而非可逆的特性决策。文档为代码库提供了明确的技术债务清单，可以作为 Sprint 规划的直接输入。

---

## 建议追加的第六个方向（来自验证过程中的发现）

**方向六：废弃代码清理（~800+ 行重复实现）**

`internal/{file,comment,message,block,notification,search}/` 中的 service + handler 实现与 `internal/service/` + `internal/handler/http/` 中的实现重复，但从未在生产路径中被引用。这不仅增加维护成本，还会误导新贡献者（他们会问"我应该用哪个？"）。

| 子包 | 文件 | 状态 |
|------|------|------|
| `internal/file/` | `file.go` (service impl), `service_crud.go`, `service_upload.go`, `service_share.go`, `handler_http.go`, `handler_grpc.go` | 未被生产代码调用（仅在 test 中被引用） |
| `internal/comment/` | `service.go`, `handler_http.go`, `handler_grpc.go` | 同上 |
| `internal/message/` | `service.go`, `handler_http.go`, `handler_grpc.go` | 同上 |
| `internal/search/` | `search.go`, `handler_http.go`, `handler_grpc.go` | 整个包死亡 |
| `internal/block/` | `handler_http.go`, `handler_grpc.go` | 同上 |
| `internal/notification/` | `handler_http.go`, `handler_grpc.go` | 同上 |

**建议优先级**：P1（低风险、高可维护性收益）。删除前需确认 `internal/file/` 中的 repository 接口和类型定义是否在别处被引用（是的——`internal/search/` 引用了 `file.Repository` 等）。

**执行方案**：
1. 确认 `internal/search/` 中的业务逻辑已由 `internal/service/search.go` 完整覆盖（已覆盖，含 CJK 回退）
2. 将 `internal/file/handler_grpc_test.go` 中引用的 `file.NewFileServer` 等构造迁移至 `internal/handler/grpc/` 的测试（如适用）
3. 删除废弃文件后跑 `make build && make test && make lint`
4. 如果 `internal/search/` 中的 `searchKind` 方法有别于 `internal/service/search.go` 中的实现（差异：前者无 snippet 支持、无 CJK 回退），则先补全差异再删除
