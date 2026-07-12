基于以上对代码库的实地验证，我已完成了系统的真实性核查。以下是逐方向评估：

---

## 真实性验证报告

对各方向的最关键主张进行了代码级验证（6 个独立性确认、4 个事实性错误发现）：

### ✅ 方向一：双栈功能缺口 — 部分正确，但前提判断有误

**已验证为真的主张：**
- ✅ 新栈（`internal/comment/repository.go:390`）**确实缺少** `SearchCommentsLike`（旧栈 `internal/repository/comment.go:52` 有）
- ✅ 新栈（`internal/file/repository.go:223`）**确实缺少** `SearchFilesLike`（旧栈 `internal/repository/file.go:47` 有）
- ✅ 新栈所有领域包有 ~11,798 行代码（快速求和验证）

**已验证为假的主张：**
- ❌ **新栈缺少 `ListSoftDeletedBefore`/`HardDelete`** → **FALSE**。`internal/file/file.go:26-27` 中明确存在这两个方法，`SweeperRepository` 接口和实现完整。文档引用了旧栈代码位置，但未检查新栈的接口定义。

**更关键的发现（文档完全遗漏）：**
- `internal/app/app.go` **仅引用旧栈**（`internal/service/`, `internal/repository/`），**完全不引用**新栈领域包。新栈代码在生产路径中不存在——是死代码。
- 因此 CJK 搜索缺口**不是生产 bug**——生产用的旧栈有完整的 `SearchXxxLike` 实现。
- 真正的问题是双栈**维护负担**而非"功能缺陷漂移"。

### ❌ 方向二：Admin 运营面 — 多处事实性错误

**已验证：几乎所有主要主张都是假的：**

| 文档主张 | 验证结果 |
|---------|---------|
| "RequirePermission 从未在任何路由上注册" | ❌ **FALSE** — `internal/handler/http/admin.go:31,40` 中注册了 audit-read 和 system-config 两条权限 |
| "GrantRole endpoint 不存在" | ❌ **FALSE** — HTTP: `admin.go:42,61` + gRPC: `handler/grpc/admin.go:29` |
| "RevokeRole endpoint 不存在" | ❌ **FALSE** — HTTP: `admin.go:43,77` + gRPC: `handler/grpc/admin.go:36` |
| "gRPC admin service 完全不存在" | ❌ **FALSE** — `internal/handler/grpc/admin.go` 实现了 GrantRole、RevokeRole、ListRoles、ListUserRoles、ListAudit |
| "权限检查方式不一致" | ❌ **FALSE** — 中间件在 `RegisterRoutes` 中统一使用 |

**文档中唯一有效的点：** `BanUser`/`UnbanUser` 端点确实不存在。但该方向整体可信度受到严重质疑。

### ✅ 方向三：文件分享生命周期 — 准确

所有主要主张经验证为真：
- ✅ `file_shares` 表确实缺少 `expires_at`、`permission`、`max_downloads`、`last_accessed_at` 等列
- ✅ 文件删除**不级联**分享记录（`internal/file/service_crud.go:128-148` 的 `Delete` 方法只软删文件行）
- ✅ 分享创建**不发射** any outbox/notification 事件
- ✅ 权限是二值的（Grant/Revoke 无层级）

这是文档中最准确的 direction。

### 🟡 方向四：请求级缓存 — 准确但缺乏紧急度

- ✅ 确实没有 `RequestCache` 或请求作用域缓存
- 重复查询的模式确实存在（但多为轻微冗余）
- 文档的推论有道理，但优先级合理为 P2

### ✅ 方向五：存储断路器 — 准确

- ✅ SSO 客户端有完整断路器模式（`internal/sso/client.go:63,109,222-229`）
- ✅ `internal/storage/` 中没有断路器、重试策略或 per-operation 超时
- ✅ 没有可区分的错误类型（`ErrStorageDown`、`ErrRateLimited`、`ErrTimeout`）

---

## 调整后的优先级与估算

| 方向 | 原优先级 | 调整后 | 理由 |
|------|---------|-------|------|
| 一：双栈 | P0 | **P2** | 新栈是死代码，非生产 bug。问题在于维护负担而非功能缺陷漂移 |
| 二：Admin | P1 | **P3** | 核心主张几乎全错。已有完整的角色管理端点 + 权限中间件 + gRPC |
| 三：分享生命周期 | P1 | **P1** | 全部准确，产品完整性缺口真实存在 |
| 四：请求缓存 | P2 | **P3** | 准确但低紧急度 |
| 五：存储断路器 | P1 | **P1-P2** | 准确，生产韧性缺口真实存在 |

如需进一步深入验证某个方向的细节，或者希望我将此分析结果与文档一起保存，请告知。
