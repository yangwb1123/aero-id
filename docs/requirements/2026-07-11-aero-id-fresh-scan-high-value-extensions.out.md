我的验证覆盖了约 20 个代码点。以下是最终评估。

---

## 完整评审结论

### 方向一：双实现结构（P0）
**✅ 确认 — 事实准确，证据充分**
- 精确死代码规模：**4,593 行非测试 + 2,406 行测试 = ~7,000 行**
- CI 中 `make test` 执行 `go test ./...`，**覆盖所有新包测试文件**，贡献假阳性覆盖率
- 新包内部有依赖链（`internal/search/` → `internal/{comment,message,file}/`），删除需按拓扑序

**修正建议：**
1. 先分析 DAG 依赖（`internal/search/` → `internal/comment/` 等）
2. 从消费者端开始逆向删除：先 `internal/search/`、`internal/notification/dispatcher.go`
3. 再删除被依赖方的未引用文件（`internal/{comment,message,file,block}/` 中仅被新包引用的文件）

---

### 方向二：跨用户内容去重（P1）
**✅ 确认 — 事实准确，证据充分**
- `FindByChecksum` 使用 `WHERE owner_uid = ? AND checksum = ?` 已核实
- `storage_refs` 基础设施就绪且已在 `Create`/`Delete` 路径被调用，但只为单用户场景服务
- 测试 `internal/repository/file_test.go:177-178` **显式断言跨用户查找不应命中**，说明当前行为是故意的

**修正建议：**
- 只需修改查询范围：`WHERE checksum = ?`（移除 `owner_uid`）
- 测试文件中的跨用户断言需改为期望成功
- 引用计数语义已正确，无需额外改动

---

### 方向三：软删除级联语义（P1）
**✅ 确认 — 事实准确，证据充分**
- 评论 `Delete` 仅 `SoftDelete(id)`，不处理回复 → 确认于 `internal/comment/service.go:227`
- 文件 `Delete` 不清理 `file_shares` → 确认于 `internal/service/file.go:240-252`（只有 storage ref 清理，无 shares 清理）
- `message_reactions` 的 `ON DELETE CASCADE` 对软删除无效 → 确认于 `migrations/000013_message_reactions.up.sql:2`
- `comment_likes` 连 `ON DELETE CASCADE` 都没有 → 确认于 `migrations/000005_create_comment_likes.up.sql:2`

**补充发现：** `comment_likes` 无 `ON DELETE CASCADE`，但这在软删除方案中无关紧要——软删除是 `UPDATE`，不是 `DELETE`，所以 FK 不会被触发。真正的问题是：软删除的语义应该是"物理不存在"还是"仅标记不可见"？当前是后者，但 UI 层可能需要额外处理。

---

### 方向四：搜索范围扩展（P2）
**✅ 确认 — 事实准确，证据充分**
- `internal/repository/message.go:320`: `WHERE (from_uid = ? OR to_uid = ?) AND search_vector @@ ...`
- `internal/service/search.go:110`: `SearchFiles(ctx, actor, query, limit)` — `actor` 作为 `owner_uid`
- 无 `file_shares` JOIN 路径，无私密/分享搜索
- GIN 索引已存在于 `migrations/000016_fts_indexes.up.sql`

**补充建议：** 修改查询作用域时需注意验证分享搜索结果的权限必须与 `CanReadFile` 逻辑一致——服务层已有 `CheckFileReadAccess`。

---

### 方向五：批量操作（P2）
**⚠️ 存在一处事实偏差**

> **报告原文：** "虽然 Shares.Grant 的 repository 接口接受 granteeUIDs []string（slice），但 HTTP/gRPC 传输层只暴露了单用户版本"

**验证结果：这是不准确的。**

HTTP 文件分享已暴露批量接口：
```go
// internal/handler/http/file.go:248-256
var body struct {
    UIDs []string `json:"uids"`       // ← 已经是数组
}
h.files.ShareFile(c.Request.Context(), id, body.UIDs)  // 传递整个 slice
```

gRPC 同样：
```go
// internal/handler/grpc/file.go:82
s.files.ShareFile(ctx, id, req.GranteeUids)  // ← repeated 字段
```

**正确的表述应该是：**
- **Share（授权）**：HTTP 和 gRPC 均已支持批量（`[]string` / `repeated`）
- **Unshare（撤销）**：仅支持单用户（URL 参数 / `GranteeUid` 单值）
- **Delete / MarkRead / Update**：全部单实体

**方向五的大方向仍然成立**（批量删除、批量标记已读、批量修改元数据均缺失），但文件分享的批量授权证据需修正。

---

## 总体评分

| 方向 | 事实准确度 | 证据深度 | 遗漏 |
|---|---|---|---|
| #1 双实现清理 | ✅ | ✅ | 新包内部依赖链需说明 |
| #2 跨用户去重 | ✅ | ✅ | `Increment` 已活跃调用的影响 |
| #3 软删除级联 | ✅ | ✅ | `comment_likes` 无 CASCADE |
| #4 搜索范围扩展 | ✅ | ✅ | — |
| #5 批量操作 | ✅ 方向正确 | ✅ 证据大部分准确 | 分享已支持批量，需修正 |

**总体评分：** 报告极其扎实，5 个方向中 4.5 个完全准确。方向五存在一处事实偏差（分享已有批量），但不影响结论。

**如需我将这份评审结果保存为文件，请告知。**
