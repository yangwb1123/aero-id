交叉验证完成。以下是精确评估：

---

## 文档交叉验证结果

文档日期为 2026-07-10，当前代码状态 2026-07-11。代码库在此期间已有显著演进。以下逐方向列出**文档陈述** vs **代码实际**：

### 方向一：已读回执隐私控制

| 文档声称 | 实际代码 | 裁决 |
|---------|---------|------|
| "没有 `message.read_receipt` 类型的开关" | `domain/notification.go` 中 **已有** `NotifTypeMessageRead = "message.read_receipt"` 常量 | ❌ 常量已存在 |
| "当前只有 `message.received` / `comment.reply` 两种类型" | 实际有三种：`message.received` / `message.read_receipt` / `comment.reply` | ❌ 三种已存在 |
| "dispatcher 将 `message.read` 转化为 `message.read_receipt` 通知" | `internal/notification/dispatcher.go:85-86`: `case "message.read": return d.notificationMessageRead(e)` 已在工作 | ✅ 确认 |
| "`MarkRead` 无条件写入 outbox 事件" | `internal/message/service.go` ~254-268: 确实无条件写入 `message.read` 事件，**不检查接收方偏好** | ✅ **仍为有效缺口** |

**RR-1 和 RR-2 仍有效**，但需修正论据：偏好类型常量已存在，只差在 `MarkRead()` 中查询 `notification_preferences` 表。建议：
- RR-1: 在 `service.go` 的 `MarkRead()` 中增加 `SELECT enabled FROM notification_preferences WHERE uid=? AND notification_type='message.read_receipt'`，如 `enabled=false` 则跳过 outbox 事件写入
- RR-2: 在 `toMessageResponse()` 中做同样查询，决定是否暴露 `ReadAt`

### 方向二：对话收件箱管理

| 文档声称 | 实际代码 | 裁决 |
|---------|---------|------|
| "无 `conversation_settings` 表" | 23 个迁移中无此表 | ✅ **全部有效** |
| "无 archive/pin/mute 端点" | router 中无对应路由 | ✅ |
| 全部 5 个增量 (IM-1 到 IM-5) | 全部未实现 | ✅ **完全未覆盖** |

**方向二是五个方向中最好的切入点**——零前置依赖，用户影响最大（⭐⭐⭐⭐⭐），且完全未触碰。

### 方向三：快捷回复与消息模板

| 文档声称 | 实际代码 | 裁决 |
|---------|---------|------|
| "无 `message_templates` 表" | 所有迁移中不存在 | ✅ |
| "无 CRUD 端点" | 代码中无 `/v1/templates` 路径 | ✅ |
| 全部 5 个增量 (CR-1 到 CR-5) | 全部未实现 | ✅ **完全未覆盖** |

### 方向四：限定域搜索

| 文档声称 | 实际代码 | 裁决 |
|---------|---------|------|
| "proto 只有 query 和 limit" | proto 中 `SearchRequest` **已有** `repeated string kinds` 字段 | ❌ `kinds` 已存在 |
| "HTTP handler 只解析 q 和 limit" | `handler_http.go:29` 已解析 `?kinds=messages,comments` | ❌ `kinds` 已支持 |
| "无 from_uid, with_uid, 日期范围" | 确实没有这些过滤 | ✅ **仍为有效缺口** |
| "无搜索语法解析器" | 确实没有 | ✅ |
| "无分面搜索" | 确实没有 | ✅ |

**FS-1/2/3 部分仍有效**（需扩展新过滤字段），FS-4 和 FS-5 完全有效。但应修正论据：基础 kind 过滤已有。

### 方向五：未读对话聚合

**这是差距最大的方向**——约 60% 的文档声称已过时：

| 文档声称 | 实际代码 | 裁决 |
|---------|---------|------|
| "无 `UnreadCount` 方法" | `service.go:291` — **已有** `func (s *service) UnreadCount()` | ❌ 已实现 |
| "无 `CountUnread`" | `repository.go:220` — **已有** `func (r *messageRepo) CountUnread()` | ❌ |
| "无 `MarkConversationRead`" | `service.go:277` + `repository.go:210` — **已有** | ❌ |
| "无批量标为已读端点" | `handler_http.go` — **已有** `POST /v1/conversations/:uid/read` | ❌ |
| "22 个迁移" | 实际 **23 个** — 000023 是 `unread_counts` | ❌ 多了未读计数迁移 |
| "无 `unread_count` 聚合字段" | 迁移 000023 创建了 `unread_counts` 表（按用户聚合 `msg_count` + `notif_count`） | ❌ |
| "无 `/v1/messages/unread_count`" | handler 已有 `r.Auth.GET("/messages/unread_count", h.UnreadCount)` | ❌ |
| "proto 无 MarkConversationRead" | proto 有 `MarkConversationReadRequest` / `MarkConversationReadResponse` | ❌ |

**UC-1 和 UC-2 已完全实现**。**仍有效的缺口**：
- ✅ **UC-3**: 批量已读仍为逐条 `message.read` 事件，未引入 `conversation.read` 事件类型
- ✅ **UC-4**: 无 `conversation_read_cursors` 表（当前 `unread_counts` 是**按用户**聚合，不是**按对话**聚合）
- ✅ **UC-5**: 无跨设备 SSE 推送未读更新

---

## 修正后的真实缺口评估

```
方向一: 已读回执隐私 → 2/4 增量有效 (RR-1, RR-2)  ~1.5 天  (RR-3/RR-4 已有基础设施)
方向二: 收件箱管理   → 5/5 增量有效              ~3.5 天  (完全未触碰)
方向三: 快捷回复     → 5/5 增量有效              ~2 天    (完全未触碰)
方向四: 限定搜索     → 4/5 增量有效 (FS-1..5 修正) ~2.5 天  (kind 过滤已有)
方向五: 未读聚合     → 3/5 增量有效 (UC-3,4,5)   ~3 天    (UC-1,2 已实现)
                              合计:              ~12.5 天
```

需我根据这份验证结果更新文档内容吗？或者直接进入某个方向的实现？
