好的，我已完成了对文档中所有核心代码声明的逐项验证。以下是完整的反馈。

---

## 整体评价

这是一份**结构严谨、论证格式极佳**的分析文档。作为一份"产品经理+架构师"的沟通文档，它的框架（问题陈述→代码证据→为什么需要→建议方案→边界情况）是业界标准水平。但从**事实准确性**和**新颖性主张**两个维度来看，存在若干需要修正的问题。

---

## 🔴 严重事实错误：方向五（管理操作）

文档的核心主张——`GrantRole`/`RevokeRole` 没有 HTTP/gRPC handler、没有审计记录——**不完全正确**。

### 实际代码状态

| 文档声称 | 实际代码 |
|----------|---------|
| "没有 HTTP handler" | ❌ **有。** `internal/handler/http/admin.go` 第 42-43 行注册了 `POST /admin/roles/grant` 和 `/revoke`，第 61-90 行有完整的 `GrantRole`/`RevokeRole` handler 实现 |
| "没有 gRPC handler" | ❌ **有。** `internal/handler/grpc/admin.go` 实现了完整的 `AdminServer`，注册了 `GrantRole`/`RevokeRole`/`ListRoles`/`ListUserRoles`/`ListAudit` |
| "没有审计记录" | ❌ **有。** `internal/domain/audit.go` 第 24-25 行定义了 `AuditAdminGrantRole` 和 `AuditAdminRevokeRole`，`internal/service/admin.go` 第 75/102 行在操作后写入 audit 日志 |
| "handler 中只有 ListAudit" | ❌ 已过时。`RegisterRoutes` 中还注册了 `GET /roles`、`GET /roles/:uid`、`POST /roles/grant`、`POST /roles/revoke` |
| "PermUserBan 已定义但无对应实现" | ✅ **准确。** `PermUserBan = "user.ban"` 定义了权限但没有对应的 ban/unban API |

**结论：方向五的大部分主张是过时的。** 角色授权/撤销的 HTTP + gRPC 端点、审计日志均已实现。唯一真实的缺口是 `PermUserBan` 对应的用户封禁 API 不存在。

---

## 🟡 新颖性主张问题

文档反复声称五个方向"在 137 份既有分析中未被作为独立方向系统性论证"。经交叉比对，**情况并非如此**：

### 方向一（反应模型）

`docs/requirements/2026-07-11-aero-id-code-scan-five-architectural-extensions.md`（同一天生成）中的**方向一**已完整覆盖：
- 相同的 `message_reactions` 表结构证据（`internal/repository/db.go:229-232`）
- 相同的 `ReactToMessage` 无类型参数代码引用
- 相同的表结构迁移方案（加 `emoji` 列）
- 相同的边界情况分析（枚举限制、通知风暴、后向兼容）

差异点：您的文档强调 **Outbox 事件**和**通知**缺失，这是正确的洞察——既有文档（方向二）将反应通知缺失放在了"不完备的 Outbox 事件表面"下分析，但没有将这两个维度统一成一个方向。

### 方向二（SSE 恢复窗口）

至少有 5 份既有文档提到了 SSE replay 的限制（见 `grep` 结果）。不过，您对 `limit=50` 硬编码和缺少分页的定位更加精确。

### 方向三（文件 MIME 校验）

至少 5 份既有文档提到了文件类型验证问题。您对 `http.DetectContentType()` 前 512 字节的技术方案是合理的。

### 方向四（接收方验证）

至少 5 份既有文档提到了"发送消息给不存在用户"的问题。您对 `GetUsers` 调用的方案是合理的。

---

## 🟢 准确且具有价值的部分

以下洞察是**文档真正的价值贡献**，即使部分已有覆盖：

### 方向一的独特贡献：反应 + Outbox + 通知三位一体

既有文档认为"reactions 的变更不需要 outbox 事件参与，与通知解耦"，而您正确地指出了相反的观点——**反应操作是唯一不走事务性 Outbox 的写操作**。这是更有力的架构论点。反应操作如果走了 Outbox，可以获得：
- 幂等性保障（目前 HTTP handler 没有幂等键）
- 异步重试能力
- 通知投递的可靠性
- 速率追踪

### 方向二的精确化

`replayMissed` 返回 `(notifs, nextCursor)` 而非固定 50 条，以及 event id 改用 UUIDv7 而非 unix-millis——这两个建议比既有分析更具体。

### 方向四的 ActiveUserCache

引入 TTL 缓存的 `ActiveUserCache`，避免每次 Send 穿透 SSO——这个设计细化是现有分析没有的。

### 方向五的 `PermUserBan` 缺口

虽然角色管理端点已存在，但 `PermUserBan` 权限已定义、无 API 实现这一点是**唯一的真正缺口**。此外，gRPC 的 service/permission 映射需要补齐 `ListRoles` 和 `ListUserRoles` 的条目（当前 `server.go` 第 252-254 行只注册了 3 个方法，但 protobuf 定义了 5 个）。

---

## 对文档的具体修正建议

### 方向五需重写

```diff
- 证据 1：GrantRole/RevokeRole 在 Repository 中存在但 Handler 中不可达
+ 证据 1：GrantRole/RevokeRole 的 HTTP (admin.go:42-43) 和 gRPC (admin.go) 端点已存在，
+         但 PermUserBan 权限对应的 ban/unban API 缺失
```

### 新颖性声明需软化

不要在每一节的标题和开头声称"首次发现"。建议改为：

> "以下五个方向在既有分析中或被分散覆盖、或被以不同假设处理。本文旨在以统一框架重新审视，并补充既有分析中缺失的维度。"

### 代码行号需更新

文档中标注的许多行号（如 `service.go:31-38`、`events.go:80-95`）与当前代码不符。代码可能已有更新。

### 方向二的 event id 使用 UUIDv7

这不是一个简单的改动——SSE 的 event id 目前在 dispatcher 中生成（`notification/dispatcher.go` 的 `Dispatch` 方法），使用的是 notification 的数据库序列号。而 `ListSince` 方法按 `created_at` 时间戳过滤。如果使用 UUIDv7 作为 event id，需要确保 `ListSince` 也支持按 UUID 过滤。这涉及 NotifFetcher 接口的变化，影响面不止 handler。

---

## 总结评分

| 维度 | 评分 | 说明 |
|------|------|------|
| 问题框架 | ⭐⭐⭐⭐⭐ | "产品+架构"双视角问题是正确的分析框架 |
| 代码证据 | ⭐⭐⭐ | 方向一/二/三/四准确；方向五有严重事实错误 |
| 新颖性主张 | ⭐⭐ | 5 个方向均有既有覆盖，新颖性被夸大 |
| 技术方案 | ⭐⭐⭐⭐ | 建议方案具体、可操作，边界情况考虑周到 |
| 产品论证 | ⭐⭐⭐⭐⭐ | 产品视角的论证（用户体验、数据完整性）强而有说服力 |
| 可操作性 | ⭐⭐⭐⭐⭐ | 每个方向都给出了清晰的实施步骤和影响域 |

**最需要重构的是：方向五（需基于实际代码重写）和全局的新颖性声明（需与既有分析建立更诚实的关系）。** 方向和二的"反应+Outbox+通知"三位一体论点是最具原创性的贡献，值得作为文档的核心论点突出。
