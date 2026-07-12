现在我已完成了对文档中所有关键证据的交叉验证。以下是我的验证结果：

---

## 验证报告：6/7 证据类别通过，1 处需修正

### ✅ 方向一：通知事件覆盖（全部验证通过）

| 证据 | 结果 |
|------|------|
| `notificationFor` 只处理 3 种事件 | ✅ `notification.go:261-320` 正好有 `message.sent`, `message.read`, `comment.replied` + `default: return nil` |
| `react()` 不产生 outbox 事件 | ✅ `comment/service.go:249-273` 整个 `react` 函数没有任何 `outbox.NewEvent` 调用 |
| 顶层评论不产生事件 | ✅ `comment/service.go:122` 只包裹在 `if parent != nil` 块中 |

### ✅ 方向二：管理员操作面（全部验证通过）

| 证据 | 结果 |
|------|------|
| `RequirePermission` 中间件从未注册 | ✅ `middleware/permission.go:14-35` 完整实现，但 `router.go:48-80` 的 `authMW` 列表中无引用 |
| GrantRole/RevokeRole 无端点暴露 | ✅ `repository/admin.go:15-23` 接口定义了，`admin.go:22-23` 只注册了 `GET /admin/audit-log`；gRPC `ops.go` 无 admin RPC |
| 4 种权限 3 种无人用 | ✅ `domain/admin.go:7-10` — `PermContentModerate`, `PermUserBan`, `PermSystemConfig` 零引用 |
| `user_ban` 表不存在 | ✅ 22 份迁移中无 `user_bans`，`grep -rn` 在全部 `.go`/`.sql` 中零命中 |

### ✅ 方向三：预签名跳过引用计数（全部验证通过）

| 证据 | 结果 |
|------|------|
| Create 路径调 Increment | ✅ `file.go:161-162` — `s.refs.Increment(ctx, f.StorageKey)` |
| Delete 路径调 Decrement | ✅ `file.go:249` — `s.refs.Decrement(ctx, f.StorageKey)` |
| CompleteUpload 不调 Increment | ✅ `file_presign.go:100-105` — 创建文件行后没有任何 `s.refs.Increment` |
| 注释明确说跳过去重 | ✅ `file_presign.go:56-59` — "Content-checksum dedup is intentionally SKIPPED" |

### ⚠️ 方向四：级联分析——文档中的 SQL 级联描述有误

这是文档中唯一的**事实错误**。文档声称以下表有 `ON DELETE CASCADE`：

| 迁移 | 文档声称 | 实际代码 | 一致性 |
|------|---------|---------|--------|
| `000005` (comment_likes) | ✅ CASCADE | `REFERENCES comments(id)` **无 CASCADE** | ❌ |
| `000007` (comment_reports) | ✅ CASCADE | `REFERENCES comments(id)` **无 CASCADE** | ❌ |
| `000006` (file_shares) | ✅ CASCADE | **无外键约束**（无 `REFERENCES` 子句） | ❌ |
| `000013` (message_reactions) | ✅ CASCADE | `REFERENCES messages(id) ON DELETE CASCADE` | ✅ |

这实际上**加剧了**文档指出的问题——不仅 `parent_id` 没有 CASCADE，其他子资源表也都没有 CASCADE 保护。不过，由于整个代码库用的是 GORM 软删除（`UPDATE SET deleted_at` 而非 `DELETE`），`ON DELETE CASCADE` 本身在软删除路径上也不会触发。方向四的根本问题是**软删除的级联行为未定义**，这个核心论点仍然成立。

### ✅ 方向五：跨实例无状态化（全部验证通过）

| 证据 | 结果 |
|------|------|
| SSE Hub 是进程内 `sync.Map` | ✅ `hub/hub.go:62-80` — `subs sync.Map (uid → *uidSubs)` |
| 限流器纯内存 | ✅ `ratelimit/ratelimit.go` — 注释第 9 行明确写 "The limiter is PER-INSTANCE" |
| SSO TokenCache 纯内存 | ✅ `sso/token_cache.go:30-40` — `items map[string]*cacheEntry` |

---

## 对优先级建议的评价

文档的优先级排序**合理**，但我会建议一个微调：

```
当前: P0(方向三) → P1(方向一) → P1(方向二) → P2(方向四/五)
建议: P0(方向三) → P1(方向二) → P1(方向一) → P2(方向四/五)
```

理由：方向二（管理员操作面）的生产影响被低估了。GrantRole/RevokeRole 的 repository 实现已完备写入，只需要 HTTP handler 接线 + 路由注册 + gRPC proto 定义，**工作量极小（约 40 行 handler + 10 行路由注册）**，但收益是让运营人员能通过 API 而非直接操作数据库来管理角色。方向一虽然用户价值高，但需要新增 outbox 事件类型 + dispatcher case + payload 类型，工作量和测试覆盖面更大。

## 附录：文档中未提及但值得注意的发现

在验证过程中，我还注意到一个文档未覆盖的细节：

1. **`file_presign.go:106-107`** — CompleteUpload 创建失败时调 `s.store.Delete(ctx, in.StorageKey)` 清理 blob，但因为跳过了引用计数，如果其他文件引用了同一 key 这个删除会损坏它们。不过文档正确指出 presign 路径跳过去重（无 checksum 匹配），所以同一 key 在 presign 路径下不会被多个文件共享——这个自保护是成立的。

2. **SSE Hub 的 `Publish` 接口签名在 hub/adapter.go 和 notification.go 之间不完全一致**：`service.NotificationPusher` 接受 `Publish(uid, eventType, data string, eventID int64)`（4 个参数），而 `hub.Pusher` 是 `Publish(uid, eventType, data string)`（3 个参数）。adapter 桥接了差异。这在当前不是问题，但如果将来要提取 Redis 后端接口，需要统一签名。

---

总体来说，这份分析的**证据质量很高**——6 个方面的核心证据全部准确，唯一有误的 SQL 级联描述（方向四）是次要的且不影响主论点。文档在发现真问题（非重复、有具体代码引用、有推荐行动路径）方面的质量明显优于 "分析疲劳" 类文档。
