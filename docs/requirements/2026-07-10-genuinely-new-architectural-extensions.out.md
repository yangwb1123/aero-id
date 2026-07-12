Now I have a comprehensive picture of the codebase. Here's my detailed engineering review of the analysis.

---

## 代码地基审查

先确认几项分析中引用的实际代码状态：

| 分析声称 | 实际代码 | 判断 |
|---------|---------|------|
| 方向一：`inbox` 查询需要新增 `sync` 端点 | `events.go` 已有 `ListSince` + `replayMissed`。模式已存在，但仅限于 notification scope | ✅ 分析正确——模式可泛化但尚未泛化到 messages |
| 方向二：`files` 只有 `maxBytes` 全局限制，没有用户级配额 | `file.go` 的 `WithMaxUploadBytes` 是实例级全局硬限制。`SUM(size)` 用户级配额完全不存在 | ✅ 分析正确 |
| 方向二：审计日志 append-only，无 TTL | `auditEventRow` 无 TTL 列，无 retention | ✅ 分析正确 |
| 方向三：webhook 当前是单端点、纯配置驱动 | `config.yaml` 中 `outbox.webhook.url` 是单一字符串。无 API 注册、无订阅管理 | ✅ 分析正确 |
| 方向四：`admin.go` 有 `Role`/`UserRole` 但未 wired | `internal/app/app.go` 中无 `RoleService`/`UserRoleRepository` 注册 | ✅ 分析正确 |
| 方向五：`messages.body` 需要 JSONB 列 | `messageRow` 有 `Meta datatypes.JSONMap` 但 `Content string` 是纯文本。无 `Body` 列 | ✅ 分析正确 |

---

## 逐方向深度审查

### 方向一：移动端同步协议

**最大的亮点**是这个方向完全复用现有且已生产验证的基础设施：

- `events.go` 的 `replayMissed()` 已经实现了时间戳驱动的增量重放模式
- `hub.Hub` 已经支持每用户多设备订阅（per-uid sharded mutex + nextID 原子递增）
- `last_event_id` 机制已经在前端 SSE 客户端中使用

**机会成本极低**：`GET /v1/messages/sync?since=<timestamp>` 可以基于现有的 `WHERE created_at > ? OR updated_at > ? OR deleted_at > ?` 构建，不需要新 migration。唯一需要新增的是 messages 表的 `updated_at` 索引（当前只有 `deleted_at` 有索引）。

**但分析漏了一个关键点**：冲突标记部分。当前 `domain.Message` 有 `EditedAt *time.Time`，但没有 `Version int` 列。客户端离线期间产生乐观 UI 更新后，服务端可能已经对同一条消息做了两次编辑。没有版本号，客户端无法做 CAS（Compare-And-Swap）。建议在 `messages` 表加 `version INTEGER DEFAULT 1`，每次编辑 +1，客户端提交编辑时带 `expected_version`。

### 方向二：成本治理

**分析在技术判断上非常准确**，但有几点需要补充：

**1. 配额校验的位置**：分析说在 `file.service.go` 的 `Create` 中加 `SUM(size)` 查询。但 `Create` 的 reader 流是单向的——已经在 `cappedReader` 中做了流式限流。如果先做 `SUM(size)` 再写 blob，存在 TOCTOU 问题（用户同时上传两个大文件，两个请求都通过校验，实际超过配额）。建议的方案：

```go
// 在存储 blob 之前先检查配额（乐观），存储 blob 之后再做最终校验
// 如果超配额，删除 blob + 删除 file row（回滚）
```

不过更务实的方案是**使用数据库级别的 `CHECK` 约束 + 物化聚合表**（`user_storage_usage`），在 `files` 表的 `AFTER INSERT/DELETE` 触发器更新聚合。但 aero-id 的约定是 GORM 管理 schema，没有触发器——所以分析建议的应用层校验是合适的。

**2. 分析的阶段划分非常务实**。Phase 1（配额校验）7 天可交付，Phase 2（归档 Job）3 周。但注意归档 Job 的风险：从主表 `DELETE` 数据后，如果后续需求变化（"恢复一条已归档的消息"），需要重建行。分析没有提及**归档的可恢复性**——建议归档格式选择 Parquet + 元数据表，而不是纯 CSV。

### 方向三：集成网关

**这是五个方向中产品价值最高但工程挑战也最大的。**

**好消息**：`outbox/relay.go` 的架构天然支持多消费者。当前的 `NotificationDispatcher` 和 `WebhookHandler` 是两个独立的 Handler。分析建议的 `Channel` 适配器框架就是第三个 Handler。

**但分析低估了复杂性**：

1. **OAuth 集成**：Slack/Telegram 都需要 OAuth 授权流程。aero-id 当前只支持 Bearer token（SSO 验证），没有 OAuth 客户端支持。需要新建 `internal/pkg/oauth` 包或集成第三方库（如 `golang.org/x/oauth2`）。这不是"中"复杂度——这是**高**复杂度。

2. **Inbound 消息**：Telegram Bot 接收用户回复并转发为 aero-id 消息，这需要在 Telegram Bot 侧维护从 `chat_id` 到 aero-id conversation 的映射。这意味着需要状态存储（`channel_mappings` 表）。分析没有提及。

3. **幂等性**：Channel 发送（尤其是 Slack/Telegram）可能因为网络问题重复投递。outbox 的事件幂等性只保证一次事件处理，但如果 channel worker 处理成功但确认丢失，会导致重复发送。需要 channel 侧的去重。

**建议**：方向三的 P2 阶段应该先实现 `channel/email`（SMTP，不需要 OAuth，复杂度最低），然后 `channel/webhook`（已有，只需要加用户级订阅管理），最后才是 Slack/Telegram。

### 方向四：RLS

**这是最值得商榷的方向。** 分析在论证上很精彩，但存在一些现实问题：

**1. GORM + RLS 的兼容性问题没有得到充分论证。** 分析只说"需要确认"——但我看过代码后可以给出结论：**GORM 的 PrepareStmt 模式会缓存 prepared statements，而 `current_setting('app.tenant_id')` 是会话级变量。** 如果 GORM 连接池中的同一连接被不同租户复用，`current_setting` 的值会是第一个设置的值，导致 RLS 策略使用错误的 tenant_id。

`db.go` 中的 `gorm.DB` 是通过 `gorm.Open(postgres.Open(dsn), &gorm.Config{})` 创建的，默认没有 `PrepareStmt`。但在查询热路径上，GORM 内部仍然会做 `Stmt` 的多轮复用。

**可行的解决方案**（分析没有提及）：
- 在每个查询前设置 `SET LOCAL app.tenant_id = '...'`（通过 `db.Exec()` 或 GORM hooks）。但 `SET LOCAL` 只在事务内有效，如果查询不在事务中，需要 `SET SESSION` 然后用完重置。
- 或者使用 PostgreSQL 的 `session_variable` 扩展（需要单独安装）。
- 或者放弃 RLS，保持应用层隔离 + 定期渗透测试。

**2. 22 张表加 `tenant_id` 列的成本：** 每个表的现有数据都需要 backfill。对已有的 000001–000022 迁移，每个都需要 `ALTER TABLE ... ADD COLUMN tenant_id UUID`（可空）→ `UPDATE` → `ALTER COLUMN SET NOT NULL`。对于生产环境的表（`messages` 可能数百万行），`UPDATE` 需要 `ACCESS EXCLUSIVE` 锁。这是一个**高风险的在线 DDL** 操作。

**3. 向后兼容的代价：** `tenant_id IS NULL` 的个人空间模式虽然有道理，但意味着所有现有 query 都需要加 `WHERE (tenant_id IS NULL OR tenant_id = current_setting('app.tenant_id'))`。RLS 策略可以自动附加，但 GORM 的 count、exists、update 操作需要确保策略不会阻塞。

**结论**：方向四应该拆成两个独立阶段：
- Phase A（分析建议的 P3）：只加 `tenants` 表 + 应用层 `X-Tenant-ID` 传播，**不做 RLS**。
- Phase B（如果确实需要 RLS）：在专门的安全审查 + 性能基准测试后，逐个表启用 RLS。

### 方向五：富媒体消息

**这是分析中最务实、代码证据最充分的方向。**

**现有基础设施分析准确**：
- `Meta datatypes.JSONMap` 列已经存在，可以承载结构化的 `body` 数据，不需要新列
- 实际上，方向五的 Phase 1 可以不新增 `body` 列，而是**在 `Meta` 中约定 `body_schema` key**。这样零 migration 成本：

```json
{
  "meta": {
    "body_schema": "v1",
    "body": {
      "type": "doc",
      "content": [
        {"type": "text", "text": "Hello "},
        {"type": "text", "marks": ["bold"], "text": "world"}
      ]
    }
  }
}
```

**但有一个代码级冲突分析没有发现**：`domain.Message` 的 `Content` 字段当前被全文搜索（FTS）索引使用（如果存在）。如果消息体移到 `Meta` 中的结构化格式，FTS 需要更新为从结构化 body 中提取纯文本：

```sql
-- 当前（假设 content 被索引）
CREATE INDEX messages_content_idx ON messages USING GIN(to_tsvector('english', content));

-- 未来：需要从 JSONB 提取 text 节点
CREATE INDEX messages_body_idx ON messages USING GIN(
  to_tsvector('english', COALESCE(meta->'body'->>'plain_text', content))
);
```

这个细节意味着方向五需要**同步更新 search 索引逻辑**。

---

## 整体评估

### 分析质量

| 维度 | 评价 |
|------|------|
| **代码证据** | 方向一、二、四、五都有精确的文件引用和行级代码分析。方向三的代码引用相对薄弱（只引了 `relay.go`，没有分析 OAuth 缺失） |
| **差异性论证** | "与已有 35+ 方向不重复"的论证令人信服。每个方向的 Why-not-covered section 都很清晰 |
| **复杂度评估** | 大部分正确。低估了方向三（OAuth）和方向四（RLS+GORM 兼容性）的实际复杂度 |
| **工程可操作性** | 实施建议的分阶段策略非常好。P1/P2/P3 的划分合理，符合"先交付价值、再追求完善"的原则 |
| **缺失内容** | 没有涉及测试策略。方向一需要增量同步的集成测试（模拟断线重连），方向三需要 channel worker 的 `testcontainers` 测试 |

### 最大遗漏

分析没有提及**增量同步的隐私/安全含义**。方向一的 `sync` 端点如果暴露所有 `updated_at > since` 的消息，攻击者可以通过反复调用 `sync?since=<old>` 来跟踪用户的所有活动。需要严格的作用域限制——`sync` 端点只返回**当前 actor 是参与方**的消息。

### 优先级建议

我同意分析的 P1/P2/P3 排序，但有以下调整：

```
P1 — Q3 2026
├── 方向一：增量同步 API（复用 ListSince 模式，messages + notifications）
├── 方向五 Phase 1：在 Meta 中约定 body_schema（零 migration，纯应用层）
├── 方向二 Phase 1：用户级配额校验（file/service_crud.go 加 SUM 检查）
└── 方向五 Phase 1.5：XSS 安全清洗（当前 content 无清洗就存储——这个实际上是安全漏洞，应该作为 P0）

P2 — Q4 2026
├── 方向三：channel/email（SMTP，最低复杂度）
├── 方向二 Phase 2：归档 Worker（复用 outbox relay 模式）
├── 方向四 Phase A：tenants 表 + 应用层 tenant propagation（无 RLS）
└── 方向五 Phase 2：富文本渲染支持（bold/italic/link/mention）

P3 — Q1 2027
├── 方向三：channel/slack + channel/telegram（需要 OAuth 包）
├── 方向二 Phase 3：存储用量仪表盘 + 告警
├── 方向四 Phase B：逐表 RLS 评估和启用（如果有安全审计需求）
└── 方向五 Phase 3：结构化类型扩展（投票、位置、代码块）
```

**一个额外的 P0 建议**：分析方向五提到的 XSS 风险在 `messageRow.Content` 中真实存在且当前没有任何清理。建议在方向五 Phase 1 之前或同时，在 `service.SendMessage` 中添加 `html.EscapeString()` 或 `bluemonday` 清理，作为独立的安全修复 PR。
