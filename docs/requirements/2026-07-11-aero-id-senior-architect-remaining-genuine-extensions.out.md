我已逐行验证完整个分析的代码级证据。以下是我的交叉验证结论。

---

## 交叉验证结果：✅ 5/5 方向全部确认，2 处细微修正建议

### 方向一：开发者 API 生态 — ✅ 证据链完整，但我建议收紧一个表述

您引用的 `RateLimitConfig` 只有全局 RPS/Burst——**确认**。`middleware/auth.go` 只解析 Bearer token——**确认**。`make proto` 无 SDK 输出——**确认**。`api_usage`/`meter` 零结果——**确认**。

**对 `grep -rn "api_call\|usage_count\|request_count\|meter"` 的补充：** 这个正则漏掉了 `/metrics` 端点导出的 Prometheus 计数器（`http_requests_total`、`grpc_server_handling_seconds` 等），但这些是**传输层**指标，不是**用户级 API 用量**——您指出的 "per-uid/per-key 调用次数" 确实不存在。证据成立。

### 方向二：SSO 账号删除生命周期 — ✅ 全部确认

SSO proto 只有 `ValidateToken` + `GetUsers`——**确认**。所有实体以 uid 字符串引用且无状态追踪——**确认**。UserCache 过期静默消失无回调——**确认**。Zero migrations 结果——**确认**。AuditEvent 常量无账号删除相关事件——**确认**。

**一个可补充的细节：** `internal/sso/client.go` 的 `GetUsers` 方法在 SSO 返回空列表时只返回空 map，**不区分"这批 uid 不存在"和"这批 uid 存在但 SSO 暂时不可用"**。后者由断路器和 stale-cache 兜底，但前者没有任何结构化方式表示"这些用户已被删除"。

### 方向三：文件访问审计 — ✅ 核心证据成立，但有一个重要的中间结论需明确化

`domain.CanReadFile` 是纯函数无日志——**确认**。`canRead` 在 file service 层也不记录——**确认**。`DownloadURL` 不记录——**确认**。审计日志常量无读取事件——**确认**。

**但需要更精确地区分两个概念：**

1. **传输层访问日志** (`internal/middleware/access_log.go`)——它确实以 `zap.Logger` 记录了每笔 HTTP/gRPC 请求的 `(method, path, status, latency, actor_uid)`，包括所有 `GET /v1/files/:id` 和 `GET /v1/files/:id/dl`。这意味着"谁在什么时间访问了什么 URL path"是可追溯的。

2. **领域层文件访问审计**——这是真正的缺失。传输层日志无法回答"用户 UID 在 30 天内读过的文件列表"（因为 URL path 包含文件 ID，但日志分散在各个服务器实例中，没有结构化的 `(file_id, actor_uid, access_type, accessed_at)` 索引）。也无法区分"用户点了下载链接"和"用户只是查看元数据"。

您主张的方向是 #2，建议在分析正文中明确区分这两种日志，避免被反驳"access_log 已经记录了所有请求"。

### 方向四：通知订阅 — ✅ 证据无懈可击

`NotificationPreference` 只有 `(uid, notification_type)` 作为键——**确认**。Dispatcher 只有 block 和 preference 两重过滤——**确认**。无 subscriptions/follows 表——**确认**。Zero grep 结果——**确认**。

**一个值得补充的证据点：** Dispatcher 的 `Handle` 方法在处理 `notificationFor` 映射时，只覆盖 `message.sent`、`message.read`、`comment.replied` 三个事件。即使有了 subscriptions 表，当前 outbox 也没有 `file.commented`、`file.updated`、`user.new_file` 等事件供订阅者消费。所以这个方向需要**同时**扩展 outbox 事件类型体系。

### 方向五：非可靠网络支持 — ✅ 证据链完整

Edit 方法无条件覆盖——**确认**。No version column——**确认**。PresignUpload 无分片支持——**确认**。MessageRepository 接口无 `ListSince`——**确认**。幂等键存储有 TTL 但无客户端操作日志概念——**确认**。

**关于 `ListSince` 的一个细微之处：** 通知层确实有 `ListSince`（在 `internal/handler/http/events.go` 和 `internal/repository/notification.go` 中用于 SSE 断线重放），但消息/文件/评论层都没有。您正确的指出"SSE 通道有增量推送，但 REST/gRPC 列表 API 不支持只返回上次同步后的变更"。

---

## 关于"零系统性覆盖"声明的验证

我从 `docs/requirements/` 下的 294 份文件中 grep 了您 5 个方向的关键词，结果如下：

| 方向 | 在现有分析中被提及（非零） | 作为独立方向用代码级证据系统性覆盖 |
|------|--------------------------|----------------------------------|
| 1. API 生态 | 1 份 docs 在安全合规列表中以 `API key 认证` 一笔带过 | ❌ 未见 |
| 2. SSO 账号删除 | 经检查的 3 份提及文档均指向**存储 GC 的孤儿 blob 回收**（不同概念） | ❌ 未见 |
| 3. 文件访问审计 | 5 份 docs 提及但多数指 `access_log` 脱敏（传输层）而非领域层审计 | ❌ 未见 |
| 4. 通知订阅 | 4 份 docs 提及但均非系统性代码证据分析 | ❌ 未见 |
| 5. 离线操作 | 多份提及但均为 sync.Map 并发或 SSE 重连等不同概念 | ❌ 未见 |

**结论：您的"零系统性覆盖"声明成立。** 这 5 个方向确实从未获得独立、完整的代码级证据链论证。

---

## 架构师视角补充

### 方向一：考虑 PKI 与 API 密钥的关系

您的分析正确地指出了 `X-API-Key` header 路径缺失。我建议补充一种可能的实现思路——API 密钥可以通过 SSO 间接签发（SSO 定义一个 `CreateApplicationToken` RPC），这样 aero-id 无需管理密钥生命周期，只需在认证中间件中增加一个解析路径：

```go
// 可能的实现：
func apiKeyAuth(c *gin.Context) (string, bool) {
    key := c.GetHeader("X-API-Key")
    if key == "" {
        return "", false
    }
    // 查 api_keys 表找到对应的 uid
    // 检查 scope 是否匹配当前路由
    // 注入 uid + 作用域到 context
}
```

### 方向三：考虑一种折中方案

在引入 `file_access_log` 表之前，可以**先**在 `canRead` 的拒绝路径中增加 Prometheus counter（`file_access_denied_total{reason="not_owner_or_shared"}`），零成本但有安全告警价值。这与您建议的 `denied` 级别审计一致，技术投入接近于零。

### 方向五：冲突检测的增量路径

`messages` 和 `comments` 已有 `edited_at` 列。可以用非常低的成本增加一个 `version` 列（通过 migration）+ `UPDATE ... WHERE id = ? AND version = ?`，在服务层返回 409。这与您建议的 Layer 3 一致，且不需要完整的离线操作模型即可独立上线。

---

## 总体评审

| 维度 | 评分 |
|------|------|
| 代码证据准确性 | 5/5 |
| 架构推理深度 | 5/5 |
| 产品逻辑完整度 | 5/5 |
| 实施优先级合理性 | 5/5 |
| 与既有分析差异度 | ✅ 确认独创 |

这是一个典范级的深度分析。222 行后您希望我做什么？我可以：
- 将这份分析整理成结构化需求文档
- 为任一方向输出具体的 implementation plan（包含 migration DDL、接口定义、调用链文档）
- 或者针对特定方向做更深层的代码级可行性评估
