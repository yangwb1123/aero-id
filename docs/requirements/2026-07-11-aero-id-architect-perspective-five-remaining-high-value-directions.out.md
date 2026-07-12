## 审阅结果：交叉验证、证据核查与质量评估

我已通读全量关键代码文件（`internal/message/repository.go`、`internal/repository/message.go`、`internal/service/search.go`、`internal/service/message.go`、`internal/middleware/ratelimit.go`、`internal/pkg/ratelimit/ratelimit.go`、`internal/config/config.go`、`internal/app/app.go`、`internal/storage/gc/sweeper.go`），并交叉比对了 `docs/requirements/` 下既有分析文档。以下为逐方向审查结论。

---

### 方向一：对话级生命周期管理 ★★★☆☆（需修正）

**交叉验证**: 部分覆盖。`2026-07-10-architect-perspective-extensions.md` 已覆盖"Threaded Inbox（会话列表）"，聚焦于**收件箱聚合**（按发送方分组 + 最新预览 + 未读数）。本文提出的 Pin/Mute/Archive/Trash **生命周期状态管理** 在此基础上有所延伸，但并非从零到一的发现。建议在文中承认"Threaded Inbox"的已有覆盖，并说明本文扩展的是生命周期状态维度。

**代码证据**: ✅ 基本准确。
- ListInbox 确实无 pin/mute/archive 过滤
- 无 `conversation_meta` 表
- SoftDelete 影响双方视图

---

### 方向二：消息双视角删除体系 ★★★☆☆（需修正）

**交叉验证**: 部分覆盖。7 份既有文档涉及 "unsend/retract" 或 "delete for me"。例如 `2026-07-10-aero-id-scan-genuine-five-extension-directions.md` 和 `2026-07-11-aero-id-code-scan-five-freshly-evidenced-extension-directions.md`。本文的双视角（发送方/接收方）设计有增量价值，但"零覆盖"claim 不准确。

**代码证据**: ⚠️ 小误差。
- 证据 1 展示的伪代码为 `.Update("deleted_at", time.Now().UTC())`，实际代码（两处 repo 均）使用 **`r.db.WithContext(ctx).Where("id = ?", id).Delete(&messageRow{})`**（GORM `.Delete()`）。效果相同（软删除双方不可见），但代码截图不精确。
- 证据 2 ✅ 准确：`m.FromUID != actor` 限制存在
- 证据 3 ✅ 准确：无 per-user 可见性列

---

### 方向三：搜索体验增强 ★☆☆☆☆（严重问题）

**交叉验证**: ❌ **核心 claim 有误**。两处既有文档明确将搜索增强列为已覆盖方向：

1. **`2026-07-11-aero-id-genuine-architectural-blindspots.md`**（第 30-42 行）
   ```
   **前置确认：已被充分覆盖的方向**
   ...
   - 全文搜索增强（Search Relevance / Highlighting）
   ```

2. **`2026-07-10-five-uncovered-architectural-extension-directions.md`**（第 17 行）
   ```
   | 全文搜索相关性/去重/分片 | 8+ |
   | 搜索跨类型排序 | 3+ |
   ```

这针方向三的"在既有材料中未被系统性覆盖"claim 构成直接反证。

**代码证据**: ⚠️ **重大事实错误**。

> 原文证据 1 声称："`ORDER BY m.created_at DESC, m.id DESC` // ← 使用 created_at 而非 rank"

但 **实际代码**（两处 repo 均）已是：
```go
// internal/repository/message.go:319-320
// internal/message/repository.go:278-279
ORDER BY ts_rank(search_vector, plainto_tsquery('simple', ?)) DESC
```

即 **`ts_rank` 排序已上线**。这是分析伪代码与仓库实际代码不一致的硬伤。

其余子点（无 ts_headline、无搜索游标分页、无 search/suggest 端点）✅ 准确。

**建议**: 删除"排序"子方向（已实现），修正证据 1 的伪代码，承认先期覆盖。

---

### 方向四：资源级速率限制 ★★★★☆（较高质量）

**交叉验证**: ✅ Largely novel。仅匹配到 1 份 2026-07-11 文档，之前的 100+ 份均未系统论证 per-endpoint weighted rate limiting。

**代码证据**: ✅ **全准**。验证确认为：
- `ratelimit.Limiter` 是 `(rate, burst)` 全局单一配置
- 中间件对所有端点调用 `l.Allow(key)`，无差异
- 配置结构仅 `{enabled, rps, burst, idle_ttl, cleanup_every}`

建议补充细节：gRPC 侧拦截器在 `internal/handler/grpc/server.go` 中同样适用单例 limiter，可一并列出。

---

### 方向五：全实体数据生命周期与归档策略 ★★★☆☆（需修正）

**交叉验证**: 部分覆盖。10+ 份既有文档讨论数据生命周期，尤其是文件 GC（`internal/storage/gc/sweeper.go`）已有完整的 `ListSoftDeletedBefore` → `HardDelete` 流水线。`2026-07-10-fifth-wave-platform-extensions.md` 等文件系统讨论了存储生命周期。

但"全实体（消息/评论/通知/审计日志）统一生命周期"视角在既有材料中的确未以独立方向系统性论证。建议在文中明确：**扩展的是覆盖范围从文件扩展到全部业务实体**，而非"零覆盖"。

**代码证据**: ✅ 基本准确。
- 仅 `internal/repository/file.go` 有 `ListSoftDeletedBefore` + `HardDelete`
- 其他实体（消息、评论、通知）确认无生命周期清理查询
- 无后台清理任务（`app.go` 中只有 GC relay、limiter cleaner、idempotency cleaner）

---

## 跨方向问题汇总

### 1. 路径不一致
多处引用 `internal/message/repository.go`（领域包路径），但生产中实际 wired 的路径是 `internal/repository/message.go`（中央仓库包）。两套实现功能相同，但读者可能混淆。建议在分析开始时说明"domain 包与 repository 包共存且功能一致"。

### 2. `cross-reference` claim 过度泛化
原文声称"以下 5 个方向在既有材料中未被作为独立方向进行完整的代码证据链论证"，但：

| 方向 | 实际覆盖情况 | 建议修正 |
|------|------------|---------|
| 方向一 | Threaded Inbox 已覆盖，Lifecycle 未覆盖 | 承认部分覆盖 |
| 方向二 | 7 份文档涉及 unsend/retract | 承认部分覆盖 |
| 方向三 | **已被其他文档列为"已覆盖"** | 删除 claim 或重新论证 |
| 方向四 | 基本未被覆盖 | 保留 |
| 方向五 | 文件生命周期已覆盖，全实体未覆盖 | 缩小范围声明 |

### 3. 伪代码 vs 实际代码
多处代码片段不是从实际文件逐字截取的，例如：
- SoftDelete 用 `.Update()` 代替实际 `.Delete()`
- SearchMessages 用 `created_at DESC` 代替实际 `ts_rank(...) DESC`
- 证据 show `LIMIT limit` 但实际是参数化的 `LIMIT ?`

建议从实际代码文件精确截取，避免展示伪代码。

---

## 质量评分矩阵

| 维度 | 方向一 | 方向二 | 方向三 | 方向四 | 方向五 |
|------|--------|--------|--------|--------|--------|
| 代码证据准确性 | ⚠️ 小误差 | ⚠️ 小误差 | ❌ 重大错误 | ✅ 全准 | ✅ 基本准确 |
| 方向新颖性 | ★★★ | ★★★ | ★★ | ★★★★★ | ★★★ |
| 方案完整性 | ★★★★ | ★★★★ | ★★★★ | ★★★★ | ★★★★ |
| 边缘情况覆盖 | ★★★★ | ★★★★ | ★★★ | ★★★★ | ★★★★ |
| 交叉验证可靠性 | ★★ | ★★ | ★ | ★★★★ | ★★ |

---

## 建议优先级重新排列

基于代码库实际成熟度和验证结果，建议：

| 新优先级 | 方向 | 说明 |
|---------|------|------|
| **P1** | **方向四：资源级速率限制** | 唯一在交叉验证和代码证据两方面均无缺陷的方向，且运维价值最高 |
| **P1** | **方向二（修正后）：双视角删除** | 修复代码截图后，产品价值清晰，架构影响低 |
| **P2** | **方向一（修正后）：对话生命周期** | 需区分 Threaded Inbox（已覆盖）与 Pin/Mute/Archive（新增） |
| **P2** | **方向五（修正后）：全实体生命周期** | 需承认文件生命周期已存在，重点是扩展到其他实体 |
| **P3** | **方向三：搜索体验增强** | ⚠️ 需大幅修正——ts_rank 排序已实现、搜索增强已被其他文档覆盖。如果重新定位为"搜索高亮 + 游标分页 + 建议补全"（去掉排序）可能仍有增量价值 |

---

**总结**：分析文档有扎实的产品思考和方案设计，但交叉验证过程存在明显遗漏（方向三的两份反证材料未被处理），且方向三的证据 1 有与代码事实不符的硬伤。建议在定稿前：
1. 重读者复审 `docs/requirements/` 中所有标记为"搜索增强已覆盖"的文件
2. 修正方向三的证据 1 为实际 SQL 查询
3. 为每个方向添加"与既有分析的差异化说明"段落
