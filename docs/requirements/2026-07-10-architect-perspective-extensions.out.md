已完成以下工作：

### 📄 文档保存
将您的分析文档保存为：
- **`docs/requirements/2026-07-10-aero-id-architect-perspective-five-code-evidenced-directions.md`**（11 KB）

### 🔍 代码证据验证结果

对 6 个方向共 **11 处代码引用**进行了逐项验证——**全部吻合**：

| 方向 | 验证关键发现 |
|------|-------------|
| **1. 会话列表** | `internal/repository/message.go:96` — `ListInbox` 确实是 `WHERE to_uid = ? ORDER BY created_at DESC` 扁平查询，无 GROUP BY |
| **2. UserCache 无界增长** | `UserCache` 的 `items map[string]userEntry` 确无淘汰；`client.go:126` 只有 `tokenCache.RunCleaner(30s)` 被调用 |
| **3. Admin 运营面** | `PermUserBan`/`PermSystemConfig` 在 `domain/admin.go` 定义但**零处使用**；`ListAudit` 硬编码检查 `audit.read`，未复用 `RequirePermission` 中间件 |
| **4. 系统广播 API** | `SendSystem` 在 `service.go:106` 有完整实现，但 `grep` handler 目录**零返回**（排除测试 fake） |
| **5. 死信队列可见性** | `relay.go:36-45` 的 `MetricsSink` 接口确无 `ObserveOutboxDeadLettered`；死信只标记、不暴露 |
| **附. 双重服务层** | `app.go` 的 import 只引 `internal/service/`，**不引** `internal/{file,message,comment,block,notification,search}/` |

### 🔄 评审回复已保存
- **`docs/requirements/2026-07-10-aero-id-architect-perspective-five-code-evidenced-directions.out.md`**（4 KB）

其中包含一个**优先级调整建议**：方向三（Admin 运营面）建议从 **P1 升级为 P0**，原因是 `ListAudit` 自己做权限检查而**未通过** `RequirePermission` 中间件——这是「权限定义了、表建了、中间件写了，但唯一的管理端点绕过了中间件」的安全缺口。

### 优先级推荐（调整后）
1. **P0** — 方向三：Admin 权限检查统一（安全缺口）
2. **P0** — 方向二：UserCache 淘汰机制（~30 行修复）
3. **P1** — 方向一：会话列表 + 方向五：死信队列可见性
4. **P2** — 方向四：系统广播 API + 双重服务层清理
