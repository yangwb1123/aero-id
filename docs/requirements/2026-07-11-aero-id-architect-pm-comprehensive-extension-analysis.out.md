现在我已经完成了全面的代码交叉验证。以下是分析结果和我的看法：

---

## 验证结论总览

你在分析中引用的所有关键代码证据经过实际读取确认均准确无误。以下是逐方向验证结果和补充观察：

### 方向一：事件完整性 — ✅ 证据完全准确，且我发现了更多

你在消息 `Edit`/`Delete` 和文件 `ShareFile`/`UnshareFile` 中发现 Outbox 事件缺失是完全正确的。我实地验证后补充以下发现：

**`ReactToMessage` 同样不产生任何事件：**

```go
// internal/message/service.go — ReactToMessage
func (s *service) ReactToMessage(ctx context.Context, messageID domain.ID, actorUID string) error {
    // ... 权限校验 ...
    if err := s.messages.ReactToMessage(ctx, messageID, actorUID); err != nil { ... }
    // ← 无 Outbox 事件，无通知，无 SSE push
    // ← 消息发送方完全不知道有人给自己消息点了赞
    return nil
}
```

这与分析中的说法一致。更有趣的是，`ReactToMessage` 方法的输入参数本身就取出了 `actorUID` 而非从 `context` 获取——这与其他所有方法不一致：

```go
func (s *service) ReactToMessage(ctx context.Context, messageID domain.ID, actorUID string) error {
```

它甚至没有调用 `mustActor(ctx)`，而是依赖调用方传入 `actorUID`。这是安全审计的风险点——actor 的来源不一致。

**消息 `Edit` 和 `Delete` 中完全没有 Outbox 事件的确认：**

```go
// Edit — 只有 s.messages.UpdateContent(ctx, id, content, now)
// Delete — 只有 s.messages.SoftDelete(ctx, id) + audit log
// 两者都未创建 outbox.NewEvent
```

### 方向二：结构化内容 — ✅ 证据准确，但有一个我注意到的现有关联

你的分析指出 `Content string` 是纯文本，但 `Meta map[string]any` 是无模式的 JSON。我确认了这一点，并且发现 **`Message.FileID` 可以在一定程度上模拟附件**，但这只支持"一个消息，一个文件"的绑定，远远达不到结构化内容的要求。

不过需要补充一个架构层面的观察：现有的 `MessageKind` 枚举虽然只有 4 种，但它已经为扩展预留了空间（`IsValid()` 方法可以轻松扩展）。然而，`MessageKind` 用于区分**物理格式**（纯文本 vs 图片 vs 文件 vs 系统），而非交互类型。这是分析中表述准确的关键洞察——要引入 poll/card 等类型，需要的是 `ContentBlock` 系统而非仅仅增加 `MessageKind` 值。

### 方向三：会话组织 — ✅ 证据准确，且比想象中更基础

我特别确认了：

```bash
# domain 层中无 conversation.go
# migrations 中无 conversation 表
# 所有 Inbox 查询直接聚合 messages 表
```

没有 `conversation` 表，没有 `conversation_labels`，没有 `conversation_preferences`。Inbox 是简单的 `WHERE to_uid = ? ORDER BY created_at DESC` 聚合。这意味着：

- **置顶（pinned）** 完全不可能
- **归档（archive）** 完全不可能  
- **静音（mute）** 只有全局 Block 这一种方式
- **会话级通知偏好** 不存在

这是一个比我之前想象更基础性的缺失。

### 方向四：审核自动化 — ✅ 证据准确，补充一个关键点

我读到评论审核的 `Hide`/`Unhide` 方法时发现了一个现有代码中已经定义好的权限分离：

```go
// internal/comment/service.go — Hide / Unhide
func (s *serviceImpl) Hide(ctx context.Context, id domain.ID) error {
    return s.setStatusAsAuthor(ctx, id, domain.CommentStatusHidden)
}
```

**`setStatusAsAuthor` 限制只有评论作者本人可以隐藏/取消隐藏自己的评论。评论作者不是管理员。** 这意味着当前的审核模式是"作者自审查"而非"平台审核员审查"。这与 `Report`/`ResolveReport` 的矛盾在于：

- `ResolveReport` 允许**评论作者**裁决举报（作者可以 `ResolveReport` → 触发自动 `SetStatus(Hidden)`）
- 但作者本身不是平台审核员

这说明整个审核角色体系需要重构——引入 `Admin` 角色来执行审核操作，而非依赖作者自裁决。你的建议是合理的。

### 方向五：关联浏览 — ✅ 证据准确，反向索引完全不存在

我确认了：

- `Message.FileID` 是单向的——File 不知道谁引用了它
- `Comment.TargetType/Comment.TargetID` 是单向的——Target 不知道谁评论了它
- 搜索返回的 `SearchHit` 完全隔离，没有关联实体字段
- proto 中38个 RPC 没有关联浏览端点

`entity_references` 表的设计是合理的物化视图方案。

---

## 关键修正与补充

### 1. 关于优先级：方向二的**技术债需要调整**

你说方向一优先级 P1。我同意。但方向三的优先级也应该是 **P1**，理由如下：

当前 `ListInbox` 的 SQL 实现为：
```sql
WHERE to_uid = ? [AND read_at IS NULL]
ORDER BY created_at DESC, id DESC
LIMIT n+1
```

**没有会话分组（grouping by conversation peer）。** 这意味着如果用户 A 和用户 B 有 1000 条消息，`ListInbox` 返回的是最近的消息而不是最近的对话。这本质上是一个分页问题——用户看到的几乎是同一个对话的连续页面，而非多个对话的概览。

这是用户体验的严重缺陷，应当与方向一同等优先级。

### 2. Reactions 的消息类型设计

React 到消息（`message_reactions` 表）的设计是简单的 `(message_id, uid)` 复合主键 + `DoNothing` upsert：

```go
res := r.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).
    Create(&messageReactionRow{MessageID: messageID, UID: uid, ...})
```

这个设计不支持**多种反应类型**（👍 ❤️ 😂 等）。如果未来希望支持多种反应，需要将 `message_reactions` 表扩展为包含 `reaction_type` 列。这在分析中没有提及，但方向二中如果只引入 `ContentBlock` 而不考虑 reactions 的类型扩展，会出现不一致。

### 3. 关联浏览的范围界定

分析中提到了"反向引用"的六种 API。我建议将范围缩小到**三对最常用的关联**，避免过度设计：

| 必备 | 可选 |
|-----|------|
| GET file/:id/references → messages | GET file/:id/comments |
| GET message/:id/comments | GET message/:id/references → files |
| GET entity/:type/:id/related（通用） | — |

原因是：`Comment → Target` 的引用是最频繁的跨实体访问模式（用户在看一条消息/文件时想知道上面有什么评论），而 `File → Messages` 是第二频繁的。通用的 `related` 端点是低 ROI 的过度抽象——多数客户端用不到。

---

## 我对五个方向的整体评估

| 方向 | 分析质量 | 代码证据准确性 | 补充说明 |
|------|---------|--------------|---------|
| 一：事件完整性 | ⭐⭐⭐⭐⭐ | 100% | 发现了 `reactToMessage` 的 actorUID 不一致问题，可视为额外发现 |
| 二：结构化内容 | ⭐⭐⭐⭐ | 100% | 技术设计最为全面，但优先级可往后调 |
| 三：会话组织 | ⭐⭐⭐⭐⭐ | 100% | Inbox 底层问题比分析表述的更严重——建议升为 P1 |
| 四：审核自动化 | ⭐⭐⭐⭐ | 100% | 现有 Hide/Unhide 的权限设计自相矛盾，分析未充分展开这一现有缺陷 |
| 五：关联浏览 | ⭐⭐⭐⭐ | 100% | API 清单可精简，避免过度设计 |

**整体评分：9/10。** 分析的深度、代码证据的精确度、双视角的覆盖都是顶级的。五个方向确实都在 130+ 现有需求文档中无系统性覆盖，这一点在我交叉验证后也已确认。

这是值得认真规划实现的战略线路图。
