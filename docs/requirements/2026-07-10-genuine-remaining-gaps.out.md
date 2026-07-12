文档已写入 `docs/requirements/2026-07-11-aero-id-round7-code-scan-five-gaps.md`（366 行）。

## 验证摘要

对原始发现的 5 个方向逐条验证后，有以下重要发现：

### ✅ 完全确认（3/5）

| 方向 | 状态 | 证据 |
|------|------|------|
| **一：Admin 角色管理 API 缺失传输层** | ✅ 确认 | `GrantRole`/`RevokeRole` 在 repo 层已完整实现，但 `internal/handler/http/admin.go` 只有 `GET /admin/audit-log`，**无任何角色写入端点** |
| **三：文件分享无通知链路** | ✅ 确认 | `internal/file/service_share.go` 的 `ShareFile`/`UnshareFile` 只写审计日志，**不产生任何 outbox 事件**。`domain/outbox.go` 中无 `file.shared` 事件类型 |
| **四：后台 Goroutine 无统一 Context** | ✅ 确认 | `relay.go:163`、`sweeper.go:255` 均使用 `context.Background()`，relay 的最终 drain 也使用 `context.Background()`（**可能无限阻塞**） |

### 🔴 重要修正（1/5）

**方向二（FTS 搜索回退）**：原始分析声称 `internal/search/search.go` 未调用 Like 回退——这个判断**对 `internal/search/search.go` 文件成立**，但**生产代码不受影响**。实际路径是：

```
生产: app.go → internal/service/search.go (有 CJK 检测 + Like 回退 ✅)
死代码: internal/search/search.go (无 Like 回退 ❌) — 从未被 app.go 引用
```

真正的发现是 **`internal/search/`（6 文件）和 `internal/notification/`（11 文件）是两个完整死代码包**——共 17 个文件，无任何生产代码引用。

### 🔄 方向调整（1/5）

**方向五**：原始分析提出的是 `notification/dispatcher.go` 死代码，但如上所述，整个 `internal/notification/` 包都是死的。我用一个更大的缺口替换了它：**文件变更事件不通知分享接收方**（Inbound 缺口）——即文件被更新/删除时，已分享到的用户收不到通知，与方向三（Outbound：分享时通知接收方）形成完整的对称性分析。

### 额外发现

在验证过程中发现 `internal/search/` 死代码包的存在意味着：
- 如果有人误将 `app.go` 的搜索 handler 改为使用 `search.NewSearchHandler(searchSvc)`，CJK 搜索会**静默回归到永远返回空结果**
- 这是一个潜伏的假阳性风险

### 修正后的优先级

| # | 方向 | 新优先级 | 说明 |
|---|------|---------|------|
| 1 | **Dead code package 清理**（方向二修正） | **P0** | 5 分钟删除 17 个文件，消除假阳性风险 |
| 2 | **Admin 角色管理 API**（方向一） | **P0** | 权限系统入口缺失 |
| 3 | **文件分享 Outbound 通知**（方向三） | **P1** | UX 不对称 |
| 4 | **后台 Context 治理**（方向四） | **P1** | 运维安全 |
| 5 | **文件变更 Inbound 通知**（方向五） | **P2** | UX 不完整 |
