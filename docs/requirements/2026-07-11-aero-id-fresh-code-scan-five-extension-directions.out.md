扫描验证完成。以下是基于实际代码验证的架构反馈：

---

## 扫描验证结果

| 文档主张 | 验证结果 | 说明 |
|---------|---------|------|
| **方向一: Cursor 裸 UUID、无元数据** | ✅ 确认 | `domain.Page.Cursor` 是 `string`；所有 repo 用 `WHERE id < ?` 直传 |
| **方向一: Cursor 无过期/上下文绑定** | ✅ 确认 | `PageFromQuery` 无校验；但隐私泄漏风险**比文档所述轻**——每个列表查询已用 actor 过滤 |
| **方向二: 路由硬编码 `/v1`** | ✅ 确认 | `router.go:88` 写死 |
| **方向二: Deprecation/Sunset = 0** | ✅ 确认 | 搜索 0 结果 |
| **方向二: 死代码 `internal/{entity}/service.go`** | ⚠️ 部分正确 | `internal/{comment,message,notification}/service.go` 存在且是死代码；但 `internal/{file,block}/service.go` **不存在**。另有 `internal/search/` 整个包（148 行）是死代码 |
| **方向三: SearchService 无 peerUID 参数** | ✅ 确认 | 接口签名 `Search(ctx, query, kinds, limit)` 无对话参数 |
| **方向三: ListConversation 已存在** | ✅ 确认 | `repository.MessageRepository` 有 `ListConversation(ctx, uidA, uidB, page)` |
| **方向四: 单全局 token bucket** | ✅ 确认 | `app.go:276` 创建单个 `ratelimit.New(rps, burst, idleTTL)` |
| **方向四: 无端点级别配置** | ✅ 确认 | YAML 只有 `enabled/rps/burst/idle_ttl/cleanup_every` |
| **方向五: 无 statement_timeout** | ✅ 确认 | grep 0 结果 |
| **方向五: 仅搜索有 per-query timeout** | ✅ 确认 | `service/search.go:81` 独有 `searchTimeout = 5s` |
| **方向五: 连接池共享** | ✅ 确认 | `db.go:40-46` 设置 `MaxOpenConns: 50`，所有 repo 共用 |

---

## 关键架构反馈

### 1. 方向一（Cursor 分页）— 隐私风险被高估

文档称 cursor 可被跨用户复用导致隐私泄漏。实际代码中**每个列表查询都先用 actor 过滤了**（`WHERE owner_uid = ?`、`WHERE to_uid = ?`、`WHERE (from_uid = ? OR to_uid = ?)`）。用户 A 偷用 B 的 cursor 只会得到"空列表"或基于 A 自己数据的继续翻页，不会看到 B 的数据。

cursor 缺少 expiry 的真正风险是**静默提前终止**——软删除行让 cursor 指向一个不存在的 UUID，`WHERE id < ?` 匹配不到行，用户看到空列表误以为没有更多数据。

**建议微调优先级**：这是一个 P2 正确性问题（静默终止），非 P1。投入 0.5 天在 cursor 中嵌入时间戳即可。

### 2. 方向二（API 生命周期）— 死代码的范围更广但分析漏了

文档只提到了 `internal/{entity}/service.go`。实际代码中存在**更大的废弃区域**：

```
internal/search/search.go       (100 行, 完整 Service 接口 + 实现)
internal/search/handler_grpc.go  (48 行, 完整 gRPC handler)
```

这两个文件无任何生产引用，却导入了 `internal/{comment,message,file}` 三个包，迫使这些包里的死代码也必须保持编译通过。这是比文档描述的更严重的死代码问题——不是一个文件而是一整个废弃子系统。

建议在方向二中增加：`internal/search/` 整包标记 `// Deprecated` + 加入清除计划。

### 3. 方向三（对话内搜索）— 最高 ROI，值得优先做

这是文档中唯一真正**低成本高回报**的方向：
- FTS 索引已存在
- `ListConversation` 的参与者过滤 SQL 可直接复用
- Service 层只需加可选参数
- 无新依赖、无新表、无新索引

**估值工作量**：1 天（repository 加参数 + service 传播 + handler 解析 query 参数），符合文档的 1-2 天。

### 4. 方向四（Per-Endpoint 限流）— 配置结构需提前设计

文档建议用 per-endpoint rules。一个更务实的路径是利用 gin 的 RouterGroup 中间件嵌套：

```
Auth group (读/写混合, 20 rps)
  ├── 读操作组 (50 rps)
  │     ├── GET /v1/files
  │     ├── GET /v1/messages
  │     └── GET /v1/search
  ├── 写操作组 (10 rps)
  │     ├── POST /v1/messages
  │     ├── POST /v1/comments
  │     └── POST /v1/files
  └── 高风险组 (5 rps)
        ├── POST /v1/files/upload
        └── POST /v1/reports
```

YAML 配置设计：

```yaml
rate_limit:
  enabled: false
  default_rps: 20
  default_burst: 40
  groups:
    - name: reads
      rps: 50
      burst: 100
      patterns: ["GET /v1/*"]
    - name: writes
      rps: 10
      burst: 20
      patterns: ["POST /v1/messages", "POST /v1/comments", "POST /v1/files*"]
    - name: heavy
      rps: 5
      burst: 5
      patterns: ["POST /v1/files/upload*", "POST /v1/reports"]
```

### 5. 方向五（Statement-Level 超时）— 最被低估的 P0

文档的 P0 评价完全正确。实际上这个问题比文档描述的更严重：

**连接池耗尽模拟**：`max_open_conns: 50`，`request_timeout: 30s`。如果 50 个查询都在等待没有 `statement_timeout` 的慢查询，30 秒内系统彻底不可用。对于生产环境，这等同于拒绝服务。

**最低成本方案**（0.5 天）：

```go
// db.go 中 GORM 连接器注入 statement_timeout
sqlDB.SetConnMaxLifetime(opts.ConnMaxLifetime)

// 在 repository 基类中设置默认超时
func (r *baseRepo) withTimeout(ctx context.Context, timeout time.Duration) context.Context {
    // 仅当 ctx 没有 deadline 时设置
    if _, ok := ctx.Deadline(); !ok {
        var cancel context.CancelFunc
        ctx, cancel = context.WithTimeout(ctx, timeout)
        defer cancel() // 注意：这需要在调用者中 defer
    }
    return ctx
}
```

或者更简单的 GORM hook：

```go
// 在所有读查询前注入 statement_timeout
gdb.Callback().Query().Before("gorm:query").Register("set_statement_timeout", func(db *gorm.DB) {
    db.Exec("SET LOCAL statement_timeout = '5s'")
})
```

### 6. 文档的未尽之点

以下内容值得在分析中补充：

**顺序互锁**：方向四（per-endpoint 限流）和方向一（Redis 分布式缓存）有依赖关系——如果先做 Redis 共享，per-endpoint 限流也用 Redis 实现，一套 Redis 基础设施同时服务多个方向。文档的独立分析未考虑这种交叉收益。

**死代码规模**：文档未量化死代码行数。实际扫描：
- `internal/search/search.go` — 100 行
- `internal/search/handler_grpc.go` — 48 行
- `internal/comment/service.go` — 470 行（被死代码引用）
- `internal/message/service.go` — 338 行（被死代码引用）
- `internal/notification/service.go` — 74 行

约 1030 行死代码在编译路径中，增加维护成本。

**CI 门禁**：方向五建议的 metrics（DB 连接池状态）直接关联 `make check-health` 的运维能力。建议在方向的"影响"部分加入：`/metrics` 增加 `db_conn_pool_open{state="in_use|idle|wait"}` 指标。

---

## 总体评价

这是一份高质量的分析文档。5 个方向的选择体现了对代码库的深入理解，代码级证据准确（除 `{file,block}/service.go` 不存在的细节错误外）。最大的价值在于**方向三（对话内搜索）**和**方向五（Statement-Level 超时）**——前者是最高 ROI 的产品功能，后者是成本最低的稳定性加固。

优先级建议微调：
- **P0**：方向五（DB 超时）— 0.5 天，防止级联故障
- **P1**：方向三（对话内搜索）— 1 天，最高产品价值
- **P1**：方向四（Per-Endpoint 限流）— 3 天，安全基线
- **P2**：方向一（Cursor 分页）— 0.5 天，边缘情况修复
- **P2**：方向二（API 生命周期 + 死代码清理）— 1 天，平台健康
