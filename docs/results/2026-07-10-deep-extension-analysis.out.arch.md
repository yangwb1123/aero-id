好的，我已经完整阅读了分析文档（10 个方向）和现有代码库。以下是架构层面的深度分析。

---

# 架构评审：aero-id 深度扩展方向分析

## 1. 架构评估

### 1.1 现有架构优势

当前代码库的质量很高，值得肯定：

- **严格的单向依赖**：handler → service → repository/storage/sso，层间职责清晰，是典型的"端口与适配器"变体，可测试性强。
- **领域层纯净**：`internal/domain/` 下实体无任何传输或持久化注解，`ValidateNewComment` 这样的业务规则通过纯函数表达，没有 leaky abstraction。
- **错误设计优良**：`*AppError` 同时携带 HTTP 状态码和 gRPC codes，且通过 `.Wrap(cause)` 保留根因，这在双协议（HTTP + gRPC）服务中是正确做法。
- **Outbox 模式成熟**：`outbox.Event` → `outbox.Relay` → `WebhookHandler` + `NotificationDispatcher` 形成了完整的事件驱动基础设施，已覆盖 7 种事件类型，且在 `relay.go` 中实现了 at-least-once 语义。
- **存储抽象正确**：`FileStorage` 接口（local/S3）在文件上传、GC sweeper、signing 等组件中被一致使用，抽象粒度恰当。

### 1.2 关键局限性

| 局限性 | 影响 | 严重度 |
|--------|------|--------|
| **Per-instance 共享状态** | 限流、幂等、通知折叠、SSO 缓存在多实例下全部退化 | **高** — 扩容前必须解决 |
| **无会话模型** | 消息走「原始行」模式，前端需自行聚合；群组对话无法构建 | **高** — 阻塞群组功能 |
| **文件管线缺失** | 用户上传文件后无缩略图/预览/扫描，核心体验缺口 | **高** |
| **审计日志只存不用** | 只有 `ListAudit` 端点，无告警、无分析、无自动化响应 | **中** |
| **运营控制面薄弱** | 有管理角色但无内容制裁端点、无规则引擎、无批量操作 | **中** |
| **通知模型 payload 缺陷** | 附录 A 发现的 `replier_uid` 丢失已影响生产通知质量 | **中** |

### 1.3 架构债务识别

1. **`comment_likes` 和 `message_reactions` 各自为政**  
   两张表结构几乎相同（`(resource_id, uid)`），但语义不同。这是策略模式缺失的表现——应当统一到一张多态表上，减少代码分支和未来扩展成本。方向 1 分析正确。

2. **收件箱查询是全量消息平面而非聚合视图**  
   这是架构演进的自然产物——MVP 阶段消息系统以最小可行形式交付，现在需要引入会话抽象。但迁移成本较高，因为已有生产数据需要回填。

3. **文件 `Meta` 字段有 schema 但无校验者**  
   `map[string]any` 作为扩展预留是务实的，但方向 4 的分析指出了正确路径：需要定义 `meta` 的子结构体（`ThumbnailMeta`, `ScanMeta`, `ImageMeta`），在写入时经过序列化+校验，而不是任意 map。

4. **没有共享 cache 层**  
   当前 `sso.TokenCache` 和 `sso.UserCache` 是进程内 `sync.Map` + TTL，部署滚动后全部冷启动。方向 2（Redis 共享状态）是正确方向，但需要评估引入 Redis 运维成本。

---

## 2. 扩展方向评估与决策建议

### 2.1 方向 1：多表情反应体系 — **P0（提前到 P1 之前）**

**评估**：分析文档给出的设计正确。但我认为应该**从 P1 提升到 P0**，并且是最优先实施的。

**理由**：
- 代码变更量极小（约 200 行），几乎无风险，适合作为团队的"热身交付"
- 但没有表情体系，方向 4 中文件缩略图预览的情景就缺失了"点赞一张图片"的场景
- 且它能暴露出两个架构问题——遗留表清理策略和通知系统整合——在早期解决，对后续方向有指导意义

**决策选项**：

| 选项 | 方案 | 权衡 |
|------|------|------|
| **A（推荐）** | 建 `emoji_reactions` 新表，双写新旧表 1 sprint，然后迁移旧数据+下线旧表 | 迁移期短；但旧表下线后要修改现有查询 |
| **B** | 直接修改 `comment_likes` 表添加 `reaction_type` 列（迁移 ALTER TABLE） | DDL 锁表风险；`message_reactions` 改造量更大 |
| **C** | 建新表，旧表作为遗留表**永远保留**，API 层做适配 | 永久技术债，不推荐 |

**接口设计建议**：

```go
// ReactionSummary 是返回给前端的数据结构，不是 DB 模型
type ReactionSummary struct {
    Total int
    Items map[string]int           // reaction_type → count
    Mine  map[string]struct{}       // 当前用户已使用的类型
}
```

`service` 层的签名应该是：

```go
type ReactionService interface {
    React(ctx context.Context, resourceType, resourceID, reactionType string) error
    Unreact(ctx context.Context, resourceType, resourceID, reactionType string) error
    GetSummary(ctx context.Context, resourceType, resourceID string) (*ReactionSummary, error)
}
```

不要暴露 `EmojiReaction` 实体给 handler——handler 只需要 `Summary` DTO。

**与通知系统的整合延迟策略**：
- Sprint 1: 仅实现反应 CRUD + 计数
- Sprint 2: 添加通知（只对 ❤️ 等高价值表情发推送），复用 `NotificationDispatcher`

### 2.2 方向 3：内容安全与滥用防御 — **P0，但分阶段落地**

**评估**：分析覆盖全面，但我认为需要**区分两个阶段**来落地。

**阶段一（Sprint 1，无外部依赖的纯规则）**：

这是生产安全底线，建议优先实现：

1. `user_sanctions` 表 + CRUD 管理端点
2. 管理员端点：`POST /v1/admin/users/:uid/sanction`、内容隐藏/删除
3. Spam 评分规则引擎（纯本地规则：频率、重复内容、链接密度）
4. 与 `middleware/ratelimit.go` 整合——受制裁用户注入更严格的 token bucket

**阶段二（Sprint 2-3，需外部依赖）**：

1. 文件扫描集成（ClamAV 或第三方 API）
2. 举报聚集自动检测（附录 A 提到的 A8 场景）
3. 制裁过期自动恢复

**关键架构决策点：规则引擎的设计**

| 方案 | 描述 | 适用 |
|------|------|------|
| **配置驱动**（推荐） | 规则用 YAML 定义，启动加载 | 规则数量 ≤ 20 条，变更频率低 |
| **DB 存储+管理 API** | `alert_rules` 表 + CRUD | 需要运营频繁调整规则 |
| **DSL 引擎** | 自定义规则语言 | 过度设计，不推荐 |

我的建议是 YAML 配置 + `user_sanctions` 表。对于方向 3，规则数量和变化频率都不高，引入 DB 存储的成本高于收益。方向 5（审计告警）需要 DB 存储规则，因为运营面板需要动态调整。

**警告**：不要在这里引入机器学习或外部 Spam API。纯规则引擎足以覆盖 90% 的场景，且不增加外部依赖。

### 2.3 方向 5：审计告警与异常行为检测 — **P1，与方向 3 协同**

**评估**：这个方向与方向 3 有**强协同关系**——告警规则引擎驱动的内容安全自动化=方向 3 的 Spam 检+测方向 5 的规则触发。建议将两个方向的规则设计统一。

**关键问题**：规则匹配器是在请求路径上同步执行，还是异步？

我的判断：**异步为主，关键规则可同步**。

```text
审计日志写入（同步）→ outbox 发出 audit.created 事件
                   → 异步 Worker 消费 audit.created
                   → 对每个 active rule，检查窗口计数器
                   → 达到阈值 → 创建 alert_event + 通知
```

为什么异步：窗口计数器需要跨时间段聚合，同步执行会大幅增加请求延迟。但如果规则是"5 分钟内同一用户删除 50 条消息"，请求路径中做 quick check 也可以——在 middleware 里开一个 goroutine 异步写窗口，不阻塞请求。

**与方向 3 的规则合并建议**：
- 方向 3 的 Spam 规则 = 内容维度（频率、重复、链接）
- 方向 5 的告警规则 = 操作维度（批量动作、异常模式）
- 它们共享 `alert_rules` 表结构，但触发逻辑不同

建议将两套规则统一在一个 `rules` 包下，但触发引擎分开：
- `rules/spam.go` — 请求时触发，返回"允许/拒绝/审查"
- `rules/alert.go` — 审计事件后异步触发，返回"通知/忽略"

### 2.4 方向 4：文件预处理管线 — **P1，但需要注意三个架构问题**

**评估**：分析文档的架构设计正确。但作为架构师，我需要指出**三个关键设计决策**：

**决策一：Worker 是自己实现还是用已有队列？**

| 选项 | 描述 | 权衡 |
|------|------|------|
| **A（推荐）** | 使用 `file_processing_jobs` 表自建 Worker | 零新依赖；与 outbox 模式一致；但需要自己实现轮询、重试、dead-letter |
| **B** | 引入 RabbitMQ / Redis Streams | 运维复杂度增加；强于大批量、高吞吐场景 |
| **C** | 复用 outbox relay | outbox 设计是"事件"而非"任务"，语义不匹配 |

选 A 的原因：当前文件上传量不大（GB 级非 EB 级），单纯的 DB 轮询 + `FOR UPDATE SKIP LOCKED` 足以支撑。等到需要高吞吐时再替换为消息队列。

**决策二：缩略图存储策略**

- **建议**：缩略图与原图存放在**同一存储后端**，key 前缀不同（`{owner}/thumb_{size}_{hash}`）
- **原因**：复用 `FileStorage` 接口，不增加新存储依赖
- **引用计数**：缩略图**不单独计 ref count**，随原图生命周期走
- **GC**：`GC sweeper` 需要感知缩略图——原图被 GC 后，对应缩略图也删除

**决策三：Meta 结构体的 Schema 管理**

当前 `File.Meta` 是 `map[string]any`。方向 4 的填充将使其包含嵌套结构。建议：

```go
// 定义在 domain/file_meta.go 中
type FileMeta struct {
    Image     *ImageMeta     `json:"image,omitempty"`
    Thumbnails *ThumbnailMeta `json:"thumbnails,omitempty"`
    Scan      *ScanMeta      `json:"scan,omitempty"`
    Preview   *PreviewMeta   `json:"preview,omitempty"`
}
```

但**保持 `map[string]any` 作为存储格式**，只在 service 层进行序列化/反序列化。原因是 GORM 对 `map[string]any` 的原生 `json` 序列化已经足够稳定，引入新结构体反而增加迁移成本。

### 2.5 方向 2：会话模型统一 — **P2，但需要架构层面的深度思考**

**评估**：这是**工作量最大**的方向，也是**架构影响最深**的方向。分析文档的设计正确，但未充分讨论以下几个关键问题：

**问题一：会话模型与现有 Message 表的关系**

现有 `messages` 表是平面结构（`from_uid` + `to_uid` 标识对话对）。引入 `conversations` 表后，有两种路径：

| 路径 | 描述 | 权衡 |
|------|------|------|
| **A（推荐）** | `messages` 表新增 `conversation_id` 列 + 回填 | 迁移期较长，但数据结构最终统一；所有消息历史在会话中可见 |
| **B** | 新消息写入新表 `conversation_messages`，旧消息留在 `messages`（视图联合） | 避免迁移旧数据，但查询逻辑分裂，新增代码复杂度高 |

选 A。原因是 aero-id 目前处在相对早期（还没有海量旧数据），一次性的回填迁移是可接受的。而且方向 2 与群组对话（expansion-directions.md 方向一）天然耦合，合并做减少系统割裂。

**问题二：1:1 会话的自动创建时机**

分析文档说"首次发送时自动创建"。这里有一个竞态条件需要设计：

```
goroutine A: 检查会话存在 → 不存在 → CREATE 
goroutine B: 检查会话存在 → 不存在 → CREATE（竞争）
```

解决方案：使用 `pg_try_advisory_xact_lock` 或唯一约束（`(LEAST(uid1, uid2), GREATEST(uid1, uid2))`）配合 `ON CONFLICT DO NOTHING`。后者更简单。

**问题三：收件箱重构对现有 API 的向后兼容**

现有 `GET /v1/conversations/:uid` 和 `GET /v1/inbox` 是生产 API。建议：

1. 新端点 `GET /v1/conversations`（返回 `[]*ConversationSummary`）
2. 旧端点保留 2 个 minor 版本（通过 `Accept: application/vnd.aero.v1+json` 或 URL 前缀）
3. 旧端点内部调用新 service 方法，做一次转换适配

---

## 3. 跨方向协同与冲突分析

### 3.1 协同点

| 协同方向 | 共享组件 | 建议 |
|----------|----------|------|
| 方向 3 + 方向 5 | `alert_rules`, `middleware/ratelimit.go` | 统一 `rules` 包，Spam 规则 + 告警规则 |
| 方向 2 + Exp-方向一（群组） | `conversations` + `conversation_members` | 合并设计，一次 migration |
| 方向 4 + 方向 3 | 文件扫描管线 | 方向 4 的 `file_processing_jobs` 中的 `scan` job 调用方向 3 的文件扫描 |
| 方向 1 + 方向 4 | 文件反应 | 方向 4 实现后，方向 1 可以支持对文件的反应 |

### 3.2 需要注意的冲突

**方向 2（会话）与 Exp-方向四（对话聚合列表）存在重叠**

`expansion-directions.md` 的"对话聚合列表"也是基于消息表做聚合查询。如果先做会话模型（方向 2），那么对话聚合列表自然由 `conversation_summary` 提供，不需要再做单独的聚合查询。**两个方向应该合并**，不要在会话模型之外单独做一个基于 SQL DISTINCT ON 的聚合视图。

**方向 4（文件预处理）与 Exp-方向三（文件后处理管线）完全重合**

两份文档都分析了文件预处理/后处理。从架构分析角度，方向 4 + Exp-方向三应当合并为一个设计文档，避免重复劳动。

### 3.3 附录 A 和 B 的处理优先级

| 编号 | 问题 | 严重度 | 修复时机 |
|------|------|--------|----------|
| A1 | 文件去重竞态 | **高** — 数据不一致 | **立即修复** |
| A8 | outbox 中丢失 `replier_uid` | **高** — 通知断裂 | **立即修复** |
| A2 | 通知折叠 `created_at` 覆盖 | **中** — 排序异常 | Sprint 1 |
| A3 | SSE 重连并发安全 | **中** — 数据重发 | Sprint 1 |
| A4 | 消息列表时序偏移 | **低** — 边界场景 | 方向 2 时修复 |
| A5 | Block + 群组交互 | **低** — 方向 2 时处理 | 方向 2 时 |
| A6 | Search timeout 上下文泄漏 | **中** — 资源泄漏 | Sprint 1 |
| A9 | 直传无去重 | **低** — 存储浪费 | 方向 4 时 |
| B1-B5 | 性能优化 | 见下文分析 | 见下文分析 |

**「立即修复」的两项**（A1, A8）属于生产缺陷，不依赖任何扩展方向，应立刻提 PR。

---

## 4. 技术选型建议

### 4.1 新依赖评估

| 方向 | 潜在新依赖 | 评估 |
|------|-----------|------|
| 方向 3（文件扫描） | ClamAV / VirusTotal API / AWS GuardDuty | **ClamAV** 开源、可自托管，适合 P0；第三方 API 增加网络延迟和成本 |
| 方向 4（缩略图） | `github.com/disintegration/imaging` / `libvips` | **imaging** 纯 Go，无 CGo 依赖，适合图片缩略图；视频需要 `ffmpeg` CGo 或 exec |
| 方向 4（OCR） | `gosseract` / Tesseract / Google Vision API | P2 阶段再评估；当前无需决定 |
| 方向 2（会话） | 无需新依赖 | 纯 SQL + 现有 GORM |
| 方向 5（告警） | 无需新依赖 | 纯 SQL + 现有 outbox |
| 分布式状态 | Redis / KeyDB | **需审慎评估**——见下文分析 |

### 4.2 Redis 引入的决策框架

Proposed by `expansion-directions.md` 方向二（Redis 共享状态）。这是**架构层面的重大决策**，我建议延迟到 P2。

**为什么不现在引入**：

1. **当前是单实例部署**。多实例问题是未来的问题。
2. **可以先用 PG 替代**。Idempotency store 已经用 GORM 实现（不走内存），直接换用 `SELECT ... FOR UPDATE` 或 `ON CONFLICT DO NOTHING` 即可防重复。Rate limiter 可以用 PG 的 `pg_advisory_xact_lock` 做分布式限流（虽然性能不如内存，但够用）。
3. **运维成本**。引入 Redis 意味着需要管理主从、持久化、网络延迟、认证、连接池——对当前 1-2 人的团队来说值得商榷。

**替代方案**：如果未来确需 Redis，采用**渐进式引入**：
1. 先只用于 Idempotency store（数据量小，可丢失）
2. 再用于 Rate limiter（可降级到内存）
3. 最后做 SSO cache（需要高可用，影响面大）

每个阶段都可以独立上线，不需要一次性全面引入。

### 4.3 自建 vs 采购决策

| 组件 | 自建 | 采购 | 建议 |
|------|------|------|------|
| 文件扫描 | ClamAV（免费开源） | VirusTotal API（按量付费） | 先 ClamAV，大规模后加 VT 作为第二引擎 |
| 缩略图 | imaging + ffmpeg exec | Cloudinary / imgix（SaaS） | 自建，因为 aero-id 的存储后端已经自持 |
| 告警通知 | 复用现有 NotificationDispatcher | PagerDuty / OpsGenie | 自建，运营团队规模决定 |
| OCR | tesseract | Google Vision / AWS Textract | P2 阶段再评估；中文场景需要确认识别率 |
| SIEM 集成 | 审计 webhook → 用户自建 | Splunk / Datadog | 采购，不重复造轮子 |

---

## 5. 实施路线图（修正版）

### 5.1 优先级矩阵（修正）

| 方向 | 我评定的优先级 | 与原文档差异 | 理由 |
|------|---------------|-------------|------|
| 1. 多表情反应 | **P0** | ↑（P1→P0） | 代码量极小，快速热身；暴露遗留表清理策略问题 |
| 3. 内容安全 | **P0（阶段一）** | 持平 P1→分阶段 | 阶段一无外部依赖；生产安全底线，不可延迟 |
| 5. 审计告警 | **P1** | 持平 | 与方向 3 协同，可共享规则设计 |
| 4. 文件预处理 | **P1** | 持平 P2→P1 | 缩略图是核心体验缺口，Sprint 3-4 启动 Worker 框架 |
| 2. 会话模型统一 | **P2** | 持平 | 数据量大，需要回填；与群组功能合并；建议 Q4 启动 |
| A1+A8 修复 | **P0（修复缺陷）** | 未在矩阵中 | 生产缺陷，不依赖方向 |

### 5.2 阶段划分

**Phase 0 — 基础修补（Sprint 1，2 周）**
- 修复 A1（文件去重竞态）+ A8（`replier_uid` 丢失）
- 方向 1：多表情反应 → 新表 + API + 旧表双写
- 方向 3 阶段一：`user_sanctions` 表 + Spam 评分 + 管理端点
- 方向 5 规则内核：YAML 配置加载 + 窗口计数器 + 告警事件表

**Phase 1 — 生产加固（Sprint 2-3，4 周）**
- 方向 3 阶段二：文件扫描集成（ClamAV）+ 举报聚集检测
- 方向 1 通-知整合：高价值表情推送
- 方向 4 阶段一：Worker 框架 + 图片缩略图（SM/MD/LG）
- 方向 5 管理端点：规则 CRUD + 告警列表 + 确认

**Phase 2 — 体验升级（Sprint 4-5，4 周）**
- 方向 4 阶段二：视频首帧 + 文档文本提取 + EXIF 清理
- 方向 5 分析面板：审计聚合统计 + 热力图
- 附录 B 性能优化：B1（覆盖索引）、B2（批量标记）
- 方向 1 旧数据迁移 + 旧表清理

**Phase 3 — 数据模型重构（Sprint 6+，6 周）**
- 方向 2：会话模型 + `conversation_id` 列 + 数据回填
- 群组对话（Exp-方向一）
- 收件箱 API 重构 + 旧端点废弃
- 方向 4 阶段三：OCR + 搜索整合

### 5.3 风险点与缓解策略

| 风险 | 概率 | 影响 | 缓解策略 |
|------|------|------|----------|
| 方向 2 的数据回填导致生产性能下降 | 中 | 高 | 使用 `batch processing` + 限速回填；回填期间旧 API 保持可用 |
| 文件扫描集成 ClamAV 导致上传延迟增加 | 中 | 中 | 异步扫描+前端 show "正在处理"状态；不可同步阻塞 |
| 多表情反应迁移期数据不一致（双写） | 低 | 中 | 增加数据一致性巡检 Job；迁移窗口期内旧表只读 |
| 方向 5 告警风暴导致运维疲劳 | 中 | 中 | 内置 cooldown 机制 + 严重度分级 + 每日摘要而非实时推送 |
| 人员或时间不足导致 5 个方向并行冲突 | 高 | 高 | 严格遵守顺序执行（Phase 0→1→2→3），不要并行启动两个 P2+ 方向 |

---

## 6. 接口与抽象层设计建议

### 6.1 需要新建的抽象层

**`spam.Scorer` 接口**（方向 3）：

```go
type Scorer interface {
    // Score evaluates a piece of content and returns a spam score (0.0–1.0).
    // The context carries request metadata (uid, ip, rate info).
    Score(ctx context.Context, content string, meta map[string]any) float64
}
```

这是正确的抽象——允许后续替换为 ML 模型或第三方 API，而不影响调用方。

**`processing.Pipeline` 接口**（方向 4）：

```go
type Pipeline interface {
    // Submit enqueues a file for async processing.
    Submit(ctx context.Context, fileID domain.ID, jobType JobType) error
    // Status returns the processing status for a file.
    Status(ctx context.Context, fileID domain.ID) (*ProcessingStatus, error)
}
```

注意：`Pipeline` 不应该暴露 `file_processing_jobs` 表的细节。handler 层通过 `fileService.GetMeta()` 获取 `scan_status` 和 `thumbnail_keys`，不直接查询 job 表。这一点分析文档已做得很好。

**`sanctions.Store` 接口**（方向 3）：

```go
type Store interface {
    // ActiveSanctions returns the active sanctions for a user.
    ActiveSanctions(ctx context.Context, uid string) ([]domain.UserSanction, error)
    // Apply creates a sanction record.
    Apply(ctx context.Context, s *domain.UserSanction) error
    // Revoke ends a sanction.
    Revoke(ctx context.Context, sanctionID domain.ID) error
}
```

### 6.2 不需要新建抽象层的领域

**表情反应**：不需要额外的接口抽象。`service.ReactionService` 直接依赖 repository 即可。表情类型较简单，不需要策略模式。

**告警规则**：不需要抽象层。规则引擎是纯逻辑（阈值检查），直接实现在 service 层即可。如果后续需要支持复杂的规则组合（AND/OR/NOT），再考虑引入 `rule.Engine`。

### 6.3 向后兼容性策略

对于涉及 API 变更的方向（特别是方向 2），建议采用**三阶段兼容策略**：

| 阶段 | 行为 | 持续时间 |
|------|------|----------|
| **Sunset** | 新端点可用，旧端点标记 `Deprecated` header | 2 个 minor 版本 |
| **Migrate** | 旧端点内部调用新 service 方法+转换 | 1 个 minor 版本 |
| **Remove** | 移除旧端点，返回 410 Gone | — |

同时引入 `Accept-Version` header 支持：

```
Accept: application/vnd.aero.v1+json  → 旧收件箱格式
Accept: application/vnd.aero.v2+json  → 新会话格式
```

---

## 7. 总结与最终建议

### 7.1 立即执行（本周）

1. **修复 A1 和 A8** — 生产缺陷
2. **方向 1 启动** — 新表 DDL + 双写逻辑，2 天内交付
3. **方向 3 阶段一启动** — `user_sanctions` DDL + 管理端点，1 周内交付

### 7.2 需要进一步讨论的架构决策

1. **Redis 是否引入以及何时引入** — 我建议 P2 后再评估，先用 PG 方案替代
2. **方向 2 与群组对话的合并设计** — 需要跨团队讨论一次数据模型，避免方向 2 做完后群组再改一次
3. **方向 4 的缩略图存储策略** — 是复用 `FileStorage` 还是启独立 CDN？建议复用，性能不够时再分层
4. **文件扫描的二阶段策略** — ClamAV 自托管 vs 云 API，取决于合规要求

### 7.3 不要做的事

- ❌ 不要为方向 1 引入消息队列——一个表 + 一个 service 方法足够
- ❌ 不要为方向 3 引入 ML/Spam API——规则引擎覆盖 90%
- ❌ 不要为方向 2 提前拆表——先加 `conversation_id` 列，数据稳定后再考虑垂直拆分
- ❌ 不要在方向 4 中实现实时缩略图——异步 Worker 延迟 < 5s，用户可接受
- ❌ 不要在方向 5 中引入时序数据库——审计告警的窗口计数在 PG 中通过 `COUNT + WHERE created_at > now() - interval` 即可

---

以上分析基于代码扫描结果和两份扩展方向文档。整体而言，分析文档的质量很高，工程判断务实。我的修正主要集中在**执行顺序**（方向 1 优先）和**跨方向协同**（3 与 5 合并规则、2 与 Exp-方向一合并模型）。建议团队基于此生成 Sprint 规划并开始 Phase 0 的实现。
