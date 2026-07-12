我现在已经对整个代码库完成了逐项验证。以下是我的评审总结：

---

## 代码级交叉验证结果

| 方向 | 代码证据验证 | 核心断言结论 |
|------|------------|-----------|
| **方向一** Admin 角色管理 API 缺失传输层 | 逐文件核查 HTTP + gRPC + Proto + Service | ❌ **核心断言错误** |
| **方向二** `internal/search/` + `internal/notification/` 死代码包 | `grep -rn` 零引用（排除 test/pb） | ✅ **确认** |
| **方向三** 文件分享无通知链路（Outbound） | 验证 `share.go`、`notification.go`、`outbox.go` | ✅ **核心成立，细节有误** |
| **方向四** 后台 Goroutine 缺少统一 Context 治理 | 验证 `relay.go`、`sweeper.go`、`app.go` | ✅ **确认，需补充细节** |
| **方向五** 文件分享事件无通知链路（Inbound） | 验证 `service/file.go`、`service_crud.go` | ✅ **确认，但与方向三重叠** |

---

## 逐方向详细评审

### 方向一：⚠️ **核心断言错误——传输层已完整实现**

该方向的中心论点是"没有任何 API 端点暴露 GrantRole/RevokeRole/ListRoles/ListUserRoles"，但实际代码证明：

**HTTP** (`internal/handler/http/admin.go`):
```go
admin.GET("/roles", h.ListRoles)           // ✅ 存在
admin.GET("/roles/:uid", h.ListUserRoles)  // ✅ 存在
syscfg.POST("/roles/grant", h.GrantRole)   // ✅ 存在
syscfg.POST("/roles/revoke", h.RevokeRole) // ✅ 存在
```

**gRPC** (`internal/handler/grpc/admin.go` + `aeropb/aero_id_grpc.pb.go`):
- `AdminService/GrantRole` ✅
- `AdminService/RevokeRole` ✅
- `AdminService/ListRoles` ✅
- `AdminService/ListUserRoles` ✅
- 额外还有 `ListAudit`、`Ban`、`Unban`、`ListBans`

**Service** (`internal/service/admin.go`): 全部 4 个方法完整实现，含 `mustActor` 鉴权 + `audit.Log` 审计。

**该方向不成立。** 正确的问题应是"文档引用的代码已过时"，而非存在架构缺口。

---

### 方向二：✅ **确认——死代码包**

`internal/search/`（6 个文件）和 `internal/notification/`（11 个文件）确实未被任何生产代码引用。验证方法：

```bash
# 两者均返回空（无生产引用）
grep -rn "internal/search" internal/ --include='*.go' | grep -v "_test.go"
grep -rn "internal/notification" internal/ --include='*.go' | grep -v "_test.go"
```

生产代码使用 `internal/service/search.go` 和 `internal/handler/{http,grpc}/search.go`。死代码包存在被误 import 的风险（尤其是 CJK 回退缺失的旧 `SearchService`）。建议删除。

---

### 方向三：✅ **核心成立，细节有误**

核心发现成立：`ShareFile`/`UnshareFile` 确实不产生 outbox 事件——文件 (`internal/file/service_share.go`) 仅做 `Grant` + `audit.Log`，无 `outbox.NewEvent`。与 `message.Service.Send()`（写入 `message.sent` outbox 事件）形成不对称。

但文档的细节有若干错误：

1. **通知类型数量错误**：文档声称 `domain/notification.go` 只有 3 个 `NotifType*`，实际有 **8 个**：
   ```go
   NotifTypeMessageReceived    // message.received
   NotifTypeMessageRead        // message.read_receipt
   NotifTypeCommentReply       // comment.reply
   NotifTypeFileCommented      // file.commented
   NotifTypeMessageCommented   // message.commented
   NotifTypeMessageReacted     // message.reacted
   NotifTypeCommentLiked       // comment.liked
   NotifTypeMessageDeleted     // message.deleted
   ```

2. **`notificationFor` 处理的事件类型错误**：文档声称只处理 3 种，实际确认处理 **7 种**：`message.sent`、`message.read`、`comment.replied`、`file.commented`、`message.commented`、`comment.liked`、`message.reacted`、`message.deleted`。

So the core UX gap exists, but the supporting code evidence needs corrections.

---

### 方向四：✅ **确认，需补充关键 nuance**

核心问题确认：
- `internal/outbox/relay.go:Start` 的 goroutine 中使用 `ctx := context.Background()`
- `stop()` 函数中最终 drain 使用 `r.drain(context.Background())`
- `internal/storage/gc/sweeper.go:Start` 同样使用 `context.Background()`
- rate limit cleaner 和 idempotency cleaner 无 context 参数

但是文档遗漏了一个关键的 **shutdown 时序问题**——`app.go` 的 `shutdown()` 调用 `stopRelay()` 后**不等待 final drain 完成**就直接进入 HTTP graceful shutdown：

```go
func (a *App) shutdown() {
    // ...
    if a.stopRelay != nil {
        a.stopRelay()            // 立即返回，final drain 在后台 goroutine 中
    }
    // ... 更多 stop 调用 ...
    shCtx, cancel := context.WithTimeout(context.Background(), ...)
    defer cancel()
    a.httpServer.Shutdown(shCtx) // 此时 final drain 可能仍在运行
    a.grpcServer.GracefulStop()
}
```

这意味着 final drain 可能在工作负载已关闭后仍在运行，且使用 `context.Background()` 无超时。这个时序问题比文档描述的更严重。

---

### 方向五：✅ **确认，但与方向三高度重叠**

文件更新/删除不通知 share granttee 是正确的发现。但文档自己也承认这是"方向三的第三个对称性缺口"——两者本质上是同一个主题（文件分享事件通知）的两个子维度：

- **方向三**：主动分享不通知接收方（Outbound）
- **方向五**：文件变更不通知受让方（Inbound）

建议合并为一个方向："文件分享全生命周期事件通知缺失"。

---

## 新颖性验证：既有文档覆盖状态

我检查了若干 `.out.md` 文档以验证方向的新颖性声明：

| 方向 | 已有覆盖状态 |
|------|------------|
| **方向一**（已证伪） | n/a |
| **方向二** 死代码包 | **新颖**——无文档系统性分析过死代码问题 |
| **方向三** 文件分享通知 | **部分新颖**——方向在 `round5-cross-analysis.out.md` 中提及"文件分享事件流"，但未从 UX 对称性角度分析 |
| **方向四** Context 治理 | **部分新颖**——`production-scale-architecture-gaps.out.md` 提到"统一上下文传播"，但未专门分析后台 goroutine 的 shutdown 时序 |
| **方向五** Inbound 变更通知 | **高度新颖**——无既有文档从 grantee 视角分析文件变更通知缺口 |

---

## 修正后的事实矩阵

| # | 方向 | 实际优先级 | 修正后状态 |
|---|------|-----------|-----------|
| 1 | ~~Admin 角色管理 API 缺失传输层~~ | **无效** | 传输层已完整实现。建议删除此方向 |
| 2 | 死代码包 (`search/` + `notification/`) | **P0** | 确认。17 个文件可安全删除 |
| 3 | 文件分享无通知链路 (Outbound) | **P1** | 核心成立。需修正通知类型计数等细节 |
| 4 | 后台 Goroutine Context 治理 | **P1** | 确认。需补充 shutdown 时序断裂的分析 |
| 5 | 文件变更不通知受让方 (Inbound) | **P2** | 确认。建议与方向三合并 |

**总计**：5 个方向中有 4 个实质有效（方向一证伪），其中方向三和五可合并为 1 个主题。
