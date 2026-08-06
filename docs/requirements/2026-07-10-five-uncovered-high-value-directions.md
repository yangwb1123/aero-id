# aero-id — 五项未被已有分析覆盖的高价值扩展方向

> **日期**: 2026-07-10
> **视角**: 资深架构师 + 产品经理
> **方法**: 全局代码扫描后，逐一比对 `docs/requirements/` 下 13 份及 `docs/REVIEW_*` 下 8 份既有分析文档的主题索引，确认零覆盖后提出
> **无代码变更**

---

## 交叉验证方法

1. 提取 `docs/requirements/`（13 份）全部方向标题 → **~55 个方向**
2. 提取 `docs/REVIEW_*`（8 份）全部方向标题 → **~20 个方向**
3. 合并去重 → **~60 个唯一方向**
4. 回扫 `internal/` 全部 ~22 个 Go 包 + 22 份迁移 SQL → 验证代码级缺口
5. 定位最终 **5 个在所有既有文档中零系统分析** 的方向

**已有文档覆盖的方向举例（证明确实已覆盖）**：群聊与频道 · 统一活动流 · Webhook 事件平台 · 文件版本管理 · 公开分享链接 · AI/ML 智能 · 多租户/组织 · GraphQL/BFF · 特征开关 · 死信控制台 · 文件加密 · 会话线程化 · 移动推送 · 存储配额 · 批量操作 · ……（共 ~60 个方向）

---

## 五项全新方向概览

| # | 方向 | 类别 | 现行代码缺口 |
|---|------|------|------------|
| 1 | **实时在线状态与打字指示器**（Presence & Typing Indicators） | 实时协作 | 仅有 SSE 通知推送，无在线/离线/活跃度追踪 |
| 2 | **消息召回、定时发送与自动销毁**（Recall, Schedule & Expiring Messages） | 消息生命周期 | 仅有软删除+编辑，无发送者召回/定时投递/阅后即焚 |
| 3 | **Bot 框架与三方集成平台**（Bot Framework & Integration Platform） | 开发者平台 | Outbox 仅支持事件外推，无 Bot 注册/斜杠命令/Webhook 指令 |
| 4 | **统一资源计量与配额管理**（Resource Metering & Quota Management） | SaaS 运营基础 | 无用量追踪表，无限流/存储/消息数的累计配额 |
| 5 | **多设备会话管理与跨端状态同步**（Multi-Device Session Management & Cross-Device Sync） | 终端用户体验 | Hub 允许多连接但无会话管理，无设备注册/远程登出/跨端已读同步 |

---

## 方向一：实时在线状态与打字指示器（Presence & Typing Indicators）

### 问题陈述

当前 aero-id 的实时通信能力止步于 **服务端 → 客户端** 的单向 SSE 通知推送。用户打开与 B 的对话时，无法知晓 B 是否在线、上次活跃时间、或者 B 正在输入。

### 为什么现有分析未覆盖

- 已有 `REVIEW_2026-06-30_FLOW_CONCURRENCY_CACHE.md` 分析了 SSE hub 的并发模型，但未提出扩展为双向实时通道的方向
- `expansion-directions.md` 提出 WebSocket 是作为替代传输层（替代 SSE），而非作为在线状态载体
- 所有 13 份需求文档均聚焦于"数据持久化与查询"功能，忽略了"协作感知"（Collaboration Awareness）这一层

### 代码级证据

| 位置 | 发现 |
|------|------|
| `internal/hub/hub.go` | `Hub` 仅支持 `Publish(uid, eventType, data)` — 无 `Subscribe(uid)` 的主动推送反向通道；无心跳检测逻辑判断客户端活跃状态 |
| `internal/handler/http/events.go` | SSE 端点只发不接，无法从客户端接收 typing 信号或心跳 |
| `internal/domain/message.go` | `Message` 无 `read_at` 以外的存在跟踪字段 |
| `internal/middleware/auth.go` | 每次请求都重新 ValidateToken，无"当前会话在线"缓存 |
| `api/proto/aero_id/v1/aero_id.proto` | 无 `TypingIndicator`、`Presence`、`LastSeen` 等 message 或 RPC |

### 用户场景

1. **对话列表显示在线状态**：用户打开消息应用，对话列表在每个联系人旁边显示绿色圆点（在线）/ 上次在线时间（离线）
2. **打字指示器**：用户 A 正在输入时，B 的界面显示"A is typing…"；多人群聊中显示"A and B are typing…"
3. **主动状态切换**：用户可设为"隐身"（对其他用户显示离线）或"勿扰"（抑制通知但显示在线）
4. **闲置检测**：用户 5 分钟无操作自动标记为"away"，30 分钟无操作标记为"offline"

### 架构设计要点

```
┌──────────────────────────────────────────────────┐
│  Presence Service                                 │
│  ┌─────────────┐  ┌─────────────┐  ┌──────────┐ │
│  │ Heartbeat    │  │ Presence    │  │ Typing   │ │
│  │ Detector     │  │ Cache       │  │ Router   │ │
│  │ (WS/SSE ping)│  │ (Redis TTL) │  │ (per con)│ │
│  └─────────────┘  └─────────────┘  └──────────┘ │
└──────────────────────────────────────────────────┘
         ↕                    ↕
  ┌──────────┐       ┌──────────────┐
  │ Hub.WS   │       │ SSE Event    │
  │ (双向)    │       │ (通知载体)     │
  └──────────┘       └──────────────┘
```

#### 核心模型

```go
type PresenceStatus int8
const (
    PresenceOffline  PresenceStatus = 0
    PresenceOnline   PresenceStatus = 1
    PresenceAway     PresenceStatus = 2 // idle >5m
    PresenceBusy     PresenceStatus = 3 // DND
    PresenceInvisible PresenceStatus = 4 // hidden from others
)

type Presence struct {
    UID        string
    Status     PresenceStatus
    LastSeenAt time.Time     // 最后活跃时间戳
    Device     string        // "web", "mobile_ios", "mobile_android"
    ExpiresAt  time.Time     // TTL 过期自动标记离线
}

type TypingIndicator struct {
    ConversationID string    // 1:1 → "fromUID:toUID" 排序后的复合 key
    ActorUID       string
    TargetUID      string    // 接收 typing 事件的 uid
    Timestamp      int64     // unix millis — 去重与过期依据
}
```

#### 需要的新基础设施

| 基础设施 | 理由 | 备选方案 |
|----------|------|----------|
| **Redis** (或类似 pub/sub) | 跨实例共享 Presence 状态（多副本部署必须）；TTL 自动过期 | PostgreSQL NOTIFY（功能弱，不适合高频写） |
| **WebSocket** (或 WebTransport) | SSE 是单向的，无法接收客户端→服务端的 typing/心跳 | 维持 SSE + 额外 HTTP POST /typing（延迟较高） |
| **Presence 缓存** | 在线状态查询频率极高（每次打开对话列表），必须毫秒级响应 | 无缓存则每次查 Redis → 延迟增加 10-50ms |

#### Edge Cases

| 场景 | 处理方式 |
|------|---------|
| 用户断网后快速重连 | 客户端发心跳 + `last_seen` 续期；短时间断线仍视为"在线"（5min 窗口） |
| 用户隐身但之前在线 | Presence Cache 标记 `invisible`，好友侧不显示在线但系统仍知道 alive（用于通知投递决策） |
| 多设备同时在线 | 同 uid 多个设备各自心跳；存在状态取"任一设备在线"；设备列表暴露给用户 |
| 打字指示器风暴 | 服务端 1s 窗口去重：同一 uid 的 typing 信号在 1s 内只广播一次；接收端 3s 无信号自动清除 |
| 反向验证（B blocked A） | Typing 事件路由前检查 block 关系：A typing → B 已 block A → 不投递 |

### 优先级判断

| 维度 | 评分 |
|------|------|
| 用户感知价值 | 🔥🔥🔥🔥🔥（消息类产品的核心 UX 要素） |
| 架构杠杆 | 高（Redis 引入后可复用分布式会话/分布式限流/缓存） |
| 实现成本 | 中高（新基础设施 + 双向传输层 + 状态机） |
| 依赖前置 | Redis 部署 |

**建议**: P1 — 这是消息产品从"功能完整"到"体验完整"的质变级特性。

---

## 方向二：消息召回、定时发送与自动销毁（Recall, Schedule & Expiring Messages）

### 问题陈述

当前消息仅有 `SoftDelete`（发送者本端删除）和 `Edit`（修改内容）。产品侧缺少三个关键的消息生命周期控制能力：

1. **Recall（召回）**：发送者在对方阅读前撤回消息，双方对话中均消失
2. **定时发送（Schedule）**：用户编写消息后设定未来时间发送
3. **自动销毁（Expire/Disappear）**：设定消息的生存时间 TTL，到期后自动从双方对话中消失

### 为什么现有分析未覆盖

- `product-directions.md` 提出"定时动作引擎"，但那是**系统级定时任务调度**（如定时清理、定时报告），而非**消息级别的定时发送**
- `architecture-extensions.md` 提出"内容生命周期策略"，但那是**资源级生命周期**（文件归档/转冷存/删除策略），而非单条消息的 TTL
- 其余所有文档均未涉及消息召回或阅后即焚

### 代码级证据

| 位置 | 发现 |
|------|------|
| `internal/domain/message.go` | `Message` 无 `recalled_at`、`scheduled_at`、`expires_at` 字段 |
| `internal/service/message.go` (modular) | `Edit()` / `Delete()` 均操作已发送消息，无语义区分"删除本端" vs "撤回全局" |
| `internal/repository/message.go` | `SoftDelete` 设置 `deleted_at`，但从软删除到硬删除/隐藏无后续处理 |
| `internal/handler/grpc/message.go` | 无 `RecallMessage`、`ScheduleMessage`、`SetExpiration` RPC |
| `internal/outbox/outbox.go` | Outbox 事件类型现为 `message.sent` / `message.read`，无 `message.recalled` / `message.scheduled` |
| `internal/notification/dispatcher.go` | 通知只处理 sent/read/replied，无 recall 通知（"A recalled a message"） |

### 用户场景

1. **消息召回**：用户 A 发送了一条包含敏感信息的消息，在 B 已读之前（或 2 分钟内）点击"撤回"→ 双方对话中该消息消失，B 看到 "A recalled a message"
2. **定时发送**：用户编写生日祝福，设定明天上午 9:00 发送 → 服务端在指定时间点自动触发发送
3. **阅后即焚**：用户发送消息时启用"看完即消失"→ B 读取后消息在 5 秒后自动从双方对话消失
4. **自动过期消息**：用户发送"临时密码：123456"，设定 30 分钟后自动删除

### 架构设计要点

#### 核心模型扩展

```go
// 现有 Message 的字段扩展
type Message struct {
    // ... 现有字段 ...
    RecalledAt  *time.Time     `json:"recalled_at,omitempty"`  // 非 nil 表示已召回
    ScheduledAt *time.Time     `json:"scheduled_at,omitempty"` // 非 nil 表示待定时发送
    ExpiresAt   *time.Time     `json:"expires_at,omitempty"`   // 非 nil 表示到期自动销毁
}
```

#### 新表

```sql
-- 定时发送队列
CREATE TABLE scheduled_messages (
    id             UUID PRIMARY KEY,
    message_id     UUID NOT NULL REFERENCES messages(id),
    send_at        TIMESTAMPTZ NOT NULL,  -- 触发时间
    status         VARCHAR(16) NOT NULL DEFAULT 'pending',  -- pending | sent | cancelled
    created_at     TIMESTAMPTZ NOT NULL,
    INDEX (send_at, status)  -- 轮询 pending + 到期的记录
);

-- 消息召回记录（审计用途）
CREATE TABLE message_recalls (
    id              UUID PRIMARY KEY,
    message_id      UUID NOT NULL REFERENCES messages(id),
    recalled_by     VARCHAR(255) NOT NULL,
    recalled_at     TIMESTAMPTZ NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL
);
```

#### 流程

```
Recall 流程:
SendMessage → SoftDelete(本端) → [Recall] → HardDelete(双方行) OR SetStatus(recalled)
                                              → 推送 SSE "message.recalled" 事件
                                              → 写入 outbox 事件
                                              → NotificationDispatcher 生成通知

Schedule 流程:
ScheduleMessage → INSERT scheduled_messages
                → 轮询协程 (类似 outbox relay) 每 5s 扫描到期的 scheduled_messages
                → 触发实际发送 → 删除 scheduled 行

Expire 流程:
SendMessage (with expires_at) → [Expiry time reached]
                               → GC 风格的 sweeper 扫描过期消息
                               → 软删除（或硬删除，取决于策略）
                               → 推送 "message.expired" 事件
                               → 双方消息消失
```

#### Edge Cases

| 场景 | 处理方式 |
|------|---------|
| B 已读消息后 A 尝试 Recall | 超过 recall 窗口（可配置，默认 2min）→ 拒绝召回；返回 `message.recall_window_expired` |
| 定时消息在发送前用户手动删除 | 删除 scheduled_messages 行 + 原始 message 草稿，标记 cancelled；不触发发送 |
| 已阅消息自动销毁后 B 想查看 | 消息已不可见；可保留审计痕迹"该消息于 X 时间已过期删除" |
| A 定时发送给 B，但 B 在发送时已 block A | 发送时检查 block 关系 → 跳过发送，scheduled 行标记 cancelled |
| 多设备同步：A 在手机上 Recall 消息 | Service 层处理 Recall → 推送 SSE 事件 → 所有连接的设备收到"隐藏消息 X"信号 |

### 优先级判断

| 维度 | 评分 |
|------|------|
| 用户感知价值 | 🔥🔥🔥🔥（隐私/安全强诉求，尤其在内部通讯场景） |
| 架构杠杆 | 中（可利用现有 outbox relay / sweeper 模式） |
| 实现成本 | 中（无需新基础设施，但状态机复杂） |
| 依赖前置 | 无 |

**建议**: P2 — 可与方向一并行开发，共用消息服务扩展。

---

## 方向三：Bot 框架与三方集成平台（Bot Framework & Integration Platform）

### 问题陈述

当前 aero-id 的 `outbox` + webhook 实现了**事件外推**（系统 → 外部）：当 message.sent / comment.replied 等事件发生时，系统可以 HMAC 签名推送至外部 URL。但这是**单向、静态配置**的：

1. 外部服务无法**主动向系统写入**（发送消息、创建评论、上传文件）
2. 没有**Bot 用户账号**体系（伪 uid 代表 bot，消息流中标识为 bot）
3. 没有**斜杠命令路由**（用户在输入框输入 `/weather Shanghai` → 触发 bot）
4. Webhook 注册是启动时静态配置的（`config.yaml`），无运行时自注册 API

### 为什么现有分析未覆盖

- `untapped-directions.md` 提出 "SDK/开发者平台"，但那是为**外部开发者**封装 aero-id 的 REST/gRPC 客户端 SDK，而非服务端的 Bot 托管框架
- `expansion-directions.md` 提出 "用户 Webhook" 但仅关注**用户级别的通知推送**，而非 bot 主动交互
- `architecture-extensions.md` 的"协作审核工作台"假设人工审核，bot 作为自动审核判断者的场景未被分析
- 所有 13 份文档均未定义"bot 作为头等参与者"的概念

### 代码级证据

| 位置 | 发现 |
|------|------|
| `internal/domain/shared.go` | `SystemSenderUID = "system"` 是唯一的伪 uid。系统消息只能通过 `SendSystem` 方法发送，且内容受限 |
| `internal/service/message.go` | `Send()` 方法校验 `MessageKindSystem` 不能由普通用户发送，但也没有 bot 通道 |
| `internal/middleware/auth.go` | Auth 中间件要求 Bearer token → SSO ValidateToken。Bot 没有 SSO 账号，无法通过该认证路径 |
| `internal/config/config.go` | `Outbox.Webhook` 是启动时一次性读取的静态配置 |
| `internal/outbox/webhook.go` | Webhook 仅支持 POST 事件负载，不支持从 webhook 响应中读取指令或回复消息 |
| `api/proto/aero_id/v1/aero_id.proto` | 无 `RegisterBot` 、`SlashCommand`、`ListBots` 等 RPC |
| 全代码库 | 无 bot 注册表、无斜杠命令路由表、无 bot 运行时沙箱 |

### 用户场景

1. **自动化工作流 Bot**：用户输入 `/create_ticket 服务器宕机` → Bot 在外部工单系统创建工单并回复确认消息
2. **信息查询 Bot**：用户输入 `/jira ISSUE-123` → Bot 查询 Jira 并返回 Issues 标题/状态/负责人
3. **通知集成 Bot**：GitHub webhook 推送 → Bot 在频道中发送 "u1 pushed 3 commits to aero-id/main"
4. **审核辅助 Bot**：评论被举报时触发 Bot 检查内容 → 若包含敏感词则自动隐藏并给举报者发送系统消息
5. **定时提醒 Bot**：Bot 按 corn 表达式在指定时间发送提醒消息（"15分钟后有站会"）

### 架构设计要点

#### 新表

```sql
-- Bot 注册表
CREATE TABLE bots (
    bot_uid       VARCHAR(255) PRIMARY KEY,   -- 伪 uid，如 "bot_weather"
    name          VARCHAR(255) NOT NULL,
    description   TEXT,
    avatar_url    TEXT,
    owner_uid     VARCHAR(255) NOT NULL,       -- 创建该 bot 的管理员
    webhook_url   TEXT NOT NULL,               -- bot 接收消息的 endpoint
    webhook_secret TEXT NOT NULL,              -- HMAC 签名密钥
    permissions   TEXT[] NOT NULL DEFAULT '{}', -- bot 可执行的 API 权限
    enabled       BOOLEAN NOT NULL DEFAULT true,
    created_at    TIMESTAMPTZ NOT NULL,
    updated_at    TIMESTAMPTZ NOT NULL
);

-- 斜杠命令注册表
CREATE TABLE slash_commands (
    id            UUID PRIMARY KEY,
    bot_uid       VARCHAR(255) NOT NULL REFERENCES bots(bot_uid),
    command       VARCHAR(64) NOT NULL,        -- e.g. "/weather"
    description   TEXT,
    scope         VARCHAR(16) NOT NULL DEFAULT 'global', -- global | conversation
    created_at    TIMESTAMPTZ NOT NULL,
    UNIQUE(command)
);
```

#### Bot 消息投递流程

```
用户输入 "/weather Beijing"
      │
      ▼
MessageService.Send (kind=MessageKindBotCommand, content="/weather Beijing")
      │
      ▼
Service 层检测 content 以 "/" 开头 → 查询 slash_commands 表
      │
      ▼ (匹配 bot_weather)
发送 HTTP POST → Bot webhook_url (HMAC 签名)
      │
      ▼ (Bot 响应)
Bot 返回 { "messages": [{ "content": "Beijing: 25°C, Sunny" }] }
      │
      ▼
Bot 框架自动调用 MessageService.SendSystem(
    toUID=原始发送者,
    content="Beijing: 25°C, Sunny"
)
      │
      ▼ (或 Bot 主动推送到频道)
Bot 调用 REST API POST /v1/messages (携带 bot_token)
```

#### 认证方案

```go
// Bot 的认证方式不同于 SSO Bearer token：
// 1. 注册时生成 bot_token（UUIDv7 + HMAC 签名）
// 2. Bot 调用 API 时携带 "Authorization: Bot <token>"
// 3. Auth 中间件检查 token 前缀 → 路由到 BotAuthValidator
// 4. BotAuthValidator 查询 bots 表验证 token
// 5. 认证成功后 context.WithActor(ctx, bot_uid)
```

#### Edge Cases

| 场景 | 处理方式 |
|------|---------|
| Bot webhook 超时（> 5s） | 触发超时后 bot 回复失败，向用户返回"Bot 暂时无响应" |
| Bot 发送恶意消息 | Bot 权限受限（permissions 白名单），超出权限的 API 调用返回 Forbidden |
| Bot 账号被禁用 | 查询 bot_uid 时检查 enabled=false → 返回 "bot_unavailable" |
| 用户 block 了 Bot | Bot 消息路由前检查 block 关系 → block 者不收到 Bot 消息 |
| Bot webhook 连续失败（n 次） | 自动禁用 Bot（enabled=false），通知 Bot owner_uid |

### 优先级判断

| 维度 | 评分 |
|------|------|
| 生态价值 | 🔥🔥🔥🔥🔥（Bot 平台是消息产品从工具到平台的转折点） |
| 架构杠杆 | 极高（Bot 框架解锁无限扩展能力） |
| 实现成本 | 高（认证体系变更 + Bot 运行时 + 注册 API） |
| 依赖前置 | 方向一（Presence）+ 方向二（Recall）建议在前 |

**建议**: P2 — 建议放在方向一、二之后，作为平台化阶段的旗舰特性。

---

## 方向四：统一资源计量与配额管理（Resource Metering & Quota Management）

### 问题陈述

当前系统对用户没有任何**累计资源使用的可见性**和**控制边界**：

- `internal/service/file.go` 的 `maxBytes` 是全局配置，不可按用户差异化
- `internal/pkg/ratelimit/ratelimit.go` 的限流是 RPS（每秒请求数），而非累计配额（每月 API 调用次数 / 消息条数 / 存储用量）
- 没有任何数据库表记录用户的**资源消耗累计值**
- 管理员无法查看"用户 A 的存储使用量"或"上月总消息量"

### 为什么现有分析未覆盖

- `expansion-directions.md` 提出"存储配额"（quota），但仅停留在概念层面，未涉及**统一的资源计量体系**（messages/comments/files/API calls 统一计量 + 配额 + 预警）
- `edge-cases-and-performance.md` 提出"分布式限流"但关注的是**瞬时 RPS 而非累计配额**
- 所有其他文档均未提出跨实体的资源计量与管理平台

这对于一个 SaaS 产品是致命缺失 — 没有计量就无法收费，没有配额就无法防止滥用。

### 代码级证据

| 位置 | 发现 |
|------|------|
| `internal/service/file.go` | `maxBytes` 是 `fileService` 结构体上的一个 int64 字段，所有用户共用同一个阈值 |
| `internal/service/comment.go` | 无任何"近期评论数"校验 — 用户可以无限创建评论 |
| `internal/service/message.go` | 无任何"发送频率"校验（RPS 限流防不了持续高压发消息） |
| `internal/config/config.go` | `StorageConfig.MaxUploadBytes` 是全局配置；`RateLimitConfig` 只配 RPS |
| `internal/repository/file.go` | `FindByChecksum` 按 owner 查重但不关心该 owner 的累计用量 |
| `internal/admin` | `adminRepo.ListAudit` 可以审计动作，但查不到资源使用统计 |
| 所有 migrations `*.up.sql` | 无任何 `user_resource_usage` 或 `user_quotas` 表 |

### 用户场景

1. **存储用量仪表盘**：用户在设置页面看到 "已用 256MB / 总共 1GB"
2. **配额达标预警**：当用户存储使用量达到 80% 时，系统推送通知 "您的存储空间即将用尽"
3. **管理员差异化配额**：团队管理员可为不同部门设置不同的存储/消息/API 配额
4. **API 调用计量**：Bot 调用 API 时记录调用次数，用于开发者结算
5. **滥用自动截停**：用户消息发送量超过套餐限额 → 自动拒绝发送，返回 `quota.exceeded`

### 架构设计要点

#### 新表

```sql
-- 用户资源配额（按产品套餐预设）
CREATE TABLE user_quotas (
    uid              VARCHAR(255) PRIMARY KEY,
    storage_bytes    BIGINT NOT NULL DEFAULT 1073741824,  -- 1GB default
    messages_per_day INT NOT NULL DEFAULT 10000,
    api_calls_per_hour INT NOT NULL DEFAULT 1000,
    max_file_count   INT NOT NULL DEFAULT 5000,
    max_comment_count INT NOT NULL DEFAULT 10000,
    updated_at       TIMESTAMPTZ NOT NULL
);

-- 用户累计资源使用（插入/删除时更新计数器）
CREATE TABLE user_resource_usage (
    uid                   VARCHAR(255) PRIMARY KEY,
    storage_bytes_used    BIGINT NOT NULL DEFAULT 0,   -- 所有文件总大小
    file_count            INT NOT NULL DEFAULT 0,       -- 未删除文件数
    messages_sent_today   INT NOT NULL DEFAULT 0,       -- 当日发送消息计数
    messages_sent_date    DATE NOT NULL,                 -- 分区日期
    comments_created_today INT NOT NULL DEFAULT 0,
    comments_date         DATE NOT NULL,
    api_calls_hour        INT NOT NULL DEFAULT 0,       -- 当前小时 API 调用
    api_calls_hour_starts TIMESTAMPTZ NOT NULL,          -- 小时周期开始
    updated_at            TIMESTAMPTZ NOT NULL
);
```

#### 计量模式

```go
// MeterStore 接口
type MeterStore interface {
    // IncrementUsage 原子递增某计量项的计数器
    IncrementUsage(ctx context.Context, uid string, meter MeterType, delta int64) error
    // GetUsage 查询当前累计使用
    GetUsage(ctx context.Context, uid string, meter MeterType) (int64, error)
    // CheckQuota 校验是否超过配额，超过返回 QuotaExceeded 错误
    CheckQuota(ctx context.Context, uid string, meter MeterType, delta int64) error
}

// 在 Service 层的调用点
func (s *fileService) Create(ctx context.Context, in CreateFileInput) (*domain.File, error) {
    // 1. 校验配额
    if err := s.meter.CheckQuota(ctx, actor, MeterStorageBytes, in.Size); err != nil {
        return nil, err
    }
    // 2. 执行创建（现有逻辑）
    // ...
    // 3. 扣减配额
    _ = s.meter.IncrementUsage(ctx, actor, MeterStorageBytes, in.Size)
    _ = s.meter.IncrementUsage(ctx, actor, MeterFileCount, 1)
}
```

#### Edge Cases

| 场景 | 处理方式 |
|------|---------|
| 文件删除后释放配额 | Delete 时调用 `IncrementUsage(ctx, actor, MeterStorageBytes, -file.Size)` |
| 时钟周期切换（天/小时） | Metering 服务在每次查询时检查当前周期是否已变；若是则重置计数器 |
| 并发扣减竞争 | 使用 PostgreSQL `INSERT ... ON CONFLICT DO UPDATE` 或 Redis `INCR` 原子操作 |
| 配额实时修正 | 管理后台修改配额后，下一次 CheckQuota 自动生效（无缓存或短 TTL 缓存） |
| 硬限制 vs 软限制 | 软限制（超过后警告，不拒绝）+ 硬限制（超过后拒绝）。通过 `user_quotas` 的 `soft_limit_bytes` 字段区分 |

### 优先级判断

| 维度 | 评分 |
|------|------|
| 商业价值 | 🔥🔥🔥🔥🔥（SaaS 商业化/防止滥用的根基） |
| 架构杠杆 | 高（统一的 MeterStore 可被所有 Service 复用） |
| 实现成本 | 中（无需新基础设施，核心是 insert/update 计数器和配额校验） |
| 依赖前置 | 无（可独立开发，逐步接入各 Service） |

**建议**: P1 — 这是从"内部工具"到"商业 SaaS"的门槛能力。建议先做存储配额 + 消息日限流，再做 API 调用计量。

---

## 方向五：多设备会话管理与跨端状态同步（Multi-Device Session Management & Cross-Device Sync）

### 问题陈述

虽然 `Hub` 支持同一个 uid 的多个 SSE 订阅（`hub.go` 的 `sync.Map` 每个 uid 可关联多个 channel），但：

1. **没有会话对象** — 无法区分"Alice 的 iPhone"和"Alice 的 Web 浏览器"
2. **没有设备注册** — 无法推送通知到特定设备（APNs/FCM token）
3. **无法远程登出** — 手机丢了无法在其他设备上注销手机登录态
4. **跨设备已读状态不同步** — 在手机上读了消息，网页上仍然是未读
5. **没有设备管理 UI** — 用户无法查看已登录设备、名称、最后活跃时间

### 为什么现有分析未覆盖

- `untapped-directions.md` 提出"移动推送网关"但只关注**推送通道本身**（APNs/FCM），不包含设备注册、会话标识、跨端同步等上层管理
- `expansion-directions.md` 提出"多副本扩展"但关注的是**服务端水平扩展**，不是客户端会话管理
- 其他所有文档均未涉及"一个用户 + 多台设备"带来的工程挑战

### 代码级证据

| 位置 | 发现 |
|------|------|
| `internal/hub/hub.go` | `uidSubs` 用自增 int64 标识订阅者，该标识无业务语义 — 无法关联到设备 |
| `internal/hub/hub.go` | `Publish` 推送给 uid 的所有 subscribers，无法选择特定设备 |
| `internal/middleware/auth.go` | `Auth` 中间件验证 token 后只注入 uid，无 session/device 信息 |
| `internal/service/notification.go` | `MarkRead` 操作只 flip `read_at`，无 device_uid 记录 |
| `internal/repository/db.go` | 没有任何 `sessions`、`devices`、`push_tokens` 表的定义 |
| `internal/domain/message.go` | `Message.ReadAt` 无法区分"Alice 的哪个设备读了这条消息" |
| `api/proto/aero_id/v1/aero_id.proto` | 无 `ListSessions`、`LogoutDevice`、`RegisterDevice` 等 RPC |

### 用户场景

1. **设备管理页面**：用户看到 "已登录设备 (3)" → iPhone 14 Pro（上次活跃 2 分钟前）| Chrome on MacBook（上次活跃 1 小时前）| Firefox on Linux（上次活跃 3 天前）
2. **远程登出**：手机丢失 → 登录网页端 → 设置 → 点击"登出所有其他设备" → 手机端未读标记失效、SSE 连接被关
3. **跨设备已读同步**：在手机上读了消息 → 通过 SSE 推送 `message.read_synced` → Web 端实时更新已读状态
4. **端到端加密准备**：每个设备持有独立的密钥对 → 消息发送时用接收方的每个在线设备的公钥加密
5. **设备级通知设置**：手机端接收 push notifications，桌面端只接收未读计数徽标

### 架构设计要点

#### 新表

```sql
-- 设备注册表
CREATE TABLE user_devices (
    id                  UUID PRIMARY KEY,
    uid                 VARCHAR(255) NOT NULL,
    device_name         VARCHAR(255) NOT NULL,       -- "iPhone 14 Pro"
    device_type         VARCHAR(32) NOT NULL,         -- "ios" | "android" | "web" | "desktop"
    push_token          TEXT,                         -- APNs / FCM token（NULL = 不支持 push）
    push_token_updated  TIMESTAMPTZ,
    last_seen_at        TIMESTAMPTZ NOT NULL,         -- 最后活跃（SSE 心跳 / API 调用）
    last_ip             INET,
    user_agent          TEXT,
    is_active           BOOLEAN NOT NULL DEFAULT true,
    created_at          TIMESTAMPTZ NOT NULL,
    INDEX(uid, is_active)
);

-- 活跃会话表（Token → Session 映射）
CREATE TABLE user_sessions (
    id                  UUID PRIMARY KEY,
    uid                 VARCHAR(255) NOT NULL,
    device_id           UUID NOT NULL REFERENCES user_devices(id),
    token_hash          VARCHAR(64) NOT NULL UNIQUE,  -- SSO token 的 sha256
    issued_at           TIMESTAMPTZ NOT NULL,
    expires_at          TIMESTAMPTZ NOT NULL,
    revoked_at          TIMESTAMPTZ,
    INDEX(uid, expires_at, revoked_at)
);
```

#### 跨端已读同步流程

```
Alice 在手机上读消息 M1
      │
      ▼
MessageService.MarkRead(ctx, M1)
  → 写入 messages.read_at
  → 写入 outbox event "message.read" (含 device_id)
      │
      ▼
NotificationDispatcher 处理 "message.read"
  → 写入通知行 (recipient = message.FromUID)
  → 推送 SSE 事件 "message.read_receipt"
      │
      ▼
Bob 收到 SSE "message.read_receipt"
  → Bob 的 Web 端标记 M1 为已读

-------------------  额外关键步骤 -------------------

MessageService.MarkRead 处理完写入后：
  → 检查 M1 所在会话中还有哪些其他设备订阅了同一会话
  → 向同一个 uid 的其他设备推送 "message.read_synced" 事件
      │
      ▼
Alice 的 Web 端收到 "message.read_synced"
  → Web 端更新消息列表中的已读状态（"已在其他设备阅读"）
  → 如果 Web 端也打开着该对话，可以保持已读状态一致
```

#### Hub 扩展

```go
// 现有 hub.Hub 的扩展改造
type Hub struct {
    subs   sync.Map // uid → *uidSubs (uidSubs.keyed by device_id 而非自增 int64)
}

// PublishToDevice 只推送给特定设备
func (h *Hub) PublishToDevice(uid, deviceID string, evt SSEEvent) {
    raw, ok := h.subs.Load(uid)
    if !ok { return }
    us := raw.(*uidSubs)
    us.mu.Lock()
    if ch, ok := us.subs[deviceID]; ok {
        select {
        case ch <- evt:
        default: // drop
        }
    }
    us.mu.Unlock()
}
```

#### Edge Cases

| 场景 | 处理方式 |
|------|---------|
| 用户登出 | 删除/标记 `user_sessions.revoked_at`，Hub 移除该 device 的订阅通道；webhook token 失效 |
| 管理员远程登出用户 | 通过 admin API 直接设置 `user_sessions.revoked_at = now()`；SSE 连接被主动关闭 |
| 设备 Push Token 变化 | SSE 连接建立时携 `device_id` 和 `push_token` → Hub 更新 `user_devices.push_token` |
| 同设备重新连接 | 携带同一个 `device_id` → Hub 复用或替换旧的订阅通道 |
| 跨设备删除消息 | A 在手机上删除消息 → 广播 `message.deleted_synced` → A 的其他设备实时隐藏该消息 |

### 优先级判断

| 维度 | 评分 |
|------|------|
| 用户体验价值 | 🔥🔥🔥🔥（多设备用户占比 > 60%，当前体验断裂） |
| 架构杠杆 | 极高（设备模型是 E2E 加密 / 移动推送 / 跨端同步的基础） |
| 实现成本 | 中高（Schema 变更 + Hub 改造 + 同步协议） |
| 依赖前置 | 无 |

**建议**: P1 — 设备模型是 2026 年消息应用的基础假设。建议先做设备注册 + 列表 + 远程登出，再做跨端已读同步。

---

## 综合路线图建议

```
Phase 1 (Near-term, P1)
├── 方向四：资源计量 + 存储/消息配额
│   └── 核心 infra，立即释放 SaaS 运营压力
├── 方向五：设备注册 + 会话管理 + 远程登出
│   └── 多用户体验基础
└── 方向一：Presence 在线状态
    └── 需要 WebSocket/Redis，但用户感知最强

Phase 2 (Medium-term, P2)
├── 方向二：消息召回 + 定时发送 + 阅后即焚
│   └── 可并行开发，复用 outbox relay 模式
├── 方向五续：跨端已读同步
│   └── 依赖 Phase 1 的设备模型就绪
└── 方向一起续：Typing Indicator
    └── 需要 WebSocket/Redis 和 Presence 基础设施

Phase 3 (Long-term, P2)
└── 方向三：Bot 框架 + 斜杠命令
    └── 平台化旗舰特性，依赖方向一的 Presence + WebSocket
```

---

## 总结对比

| 方向 | 用户价值 | 架构复杂度 | 新增基础设施 | 唯一性（vs 13 份已有分析） |
|------|---------|-----------|-------------|--------------------------|
| ① 在线状态与 Typing | 🔥🔥🔥🔥🔥 | 高 | Redis + WebSocket | ✅ 零覆盖 |
| ② Recall/Schedule/Expire | 🔥🔥🔥🔥 | 中 | 无 | ✅ 零覆盖 |
| ③ Bot 框架 | 🔥🔥🔥🔥🔥 | 高 | 无（纯业务扩展） | ✅ 零覆盖 |
| ④ 计量与配额 | 🔥🔥🔥🔥🔥 | 中 | 无 | ✅ 零覆盖 |
| ⑤ 多设备管理 | 🔥🔥🔥🔥 | 中高 | 无（Hub 扩展） | ✅ 零覆盖 |

**共同特征**：这 5 个方向不依赖彼此，除 ③→① 的有限依赖外均可独立开发。每个方向都针对当前代码库中明确可识别的空白，且均未被 13 份既有需求文档和 8 份审查文档系统分析过。
