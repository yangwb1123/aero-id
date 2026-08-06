# aero-id — 未被触及的高价值产品方向

> 日期: 2026-07-10
> 视角: 资深架构师 + 产品经理
> 方法: 全局代码扫描（`internal/` 全量 22 个包、22 个迁移、`docs/` 已有 12 份审查文档）
> 前置条件: `feat/roadmap` 已完全合并，38 个 gRPC RPC + 全 HTTP 对等 + 生产加固已交付
> 与现有文档的关系: 当前 12 份审查文档覆盖了群聊、Webhook 平台、多副本扩展、WebSocket、存储配额、
   多租户、搜索增强、事件平台、数据导出、消息会话管理等方向。本文聚焦于**没有任何现有文档涉及**的 5 个方向。

---

## 发现方法

逐一对比 12 份既有审查文档的主题索引，识别出以下**零覆盖**的高价值方向：

| 方向 | `PRODUCT_GAPS` | `expansion-directions` | `architecture-extensions` | `edge-cases-and-performance` | 其他 8 份 |
|------|:---:|:---:|:---:|:---:|:---:|
| 文件版本管理 | ✗ | ✗ | ✗ | ✗ | ✗ |
| 活动流/时间线 | ✗ | ✗ | ✗ | ✗ | ✗ |
| 定时/延迟动作引擎 | ✗ | ✗ | ✗ | ✗ | ✗ |
| 自适应限流与滥用智能 | ✗ | ✗ | ✗ | ✗ | ✗ |
| 数据平台/嵌入式分析 | ✗ | ✗ | ✗ | ✗ | ✗ |

---

## 方向一：文件版本管理与修订历史

### 为什么需要它

aero-id 的核心资产是**文件**，但当前文件是不可变的——上传后只能更新名称和元数据，无法追踪变更历史。这是一个显著的架构缺口：

1. **用户期望。** Dropbox、Google Drive、Notion 等产品中，文件版本管理是基础功能。用户希望看到"谁在什么时候修改了什么"，能够回滚到旧版本。
2. **数据所有权。** 项目的核心使命是"用户自有数据服务"（"data owned by accounts"）。如果数据不可版本化，用户对数据的控制权是残缺的。不能回滚意味着用户无法撤销误操作。
3. **协作基础。** 文件分享（`file_shares` 表已存在）只是第一步。多个用户能查看同一文件时，版本管理变得必要——谁上传了新版本？最新版本是什么？
4. **技术可行性高。** 当前的文件模型已经有了 `checksum`（SHA256）、`storage_key`、`storage_refs` 引用计数。版本管理可以建立在这套基础设施之上。

### 设计要点

#### 核心模型

```sql
-- 新增：文件版本记录，每条记录对应文件的一次内容变更
CREATE TABLE file_versions (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    file_id      UUID NOT NULL REFERENCES files(id) ON DELETE CASCADE,
    version      INTEGER NOT NULL DEFAULT 1,
    storage_key  TEXT NOT NULL,
    checksum     TEXT NOT NULL,
    size         BIGINT NOT NULL,
    mime_type    TEXT,
    uploaded_by  TEXT NOT NULL,  -- SSO uid
    commit_msg   TEXT,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE(file_id, version)
);

-- files 表增加当前版本指针
ALTER TABLE files ADD COLUMN current_version INTEGER NOT NULL DEFAULT 1;
```

#### 核心 API

| 操作 | 说明 |
|------|------|
| `PUT /v1/files/:id/versions` | 上传文件新版本（multipart，与 Create 共享上传逻辑） |
| `GET /v1/files/:id/versions` | 列出文件所有版本（分页，含 checksum、size、uploaded_by） |
| `GET /v1/files/:id/versions/:ver` | 获取特定版本的元数据 + 下载 URL |
| `POST /v1/files/:id/versions/:ver/restore` | 回滚到指定版本（创建新版本，内容来自旧版本——不破坏历史） |
| `DELETE /v1/files/:id/versions/:ver` | 删除历史版本（仅非 current_version 的可删除） |

#### 实现要点

- **存储去重。** `storage_refs` 表已记录每个 `storage_key` 的引用计数。如果两个版本有相同的 `checksum`（内容未变），共享同一个 blob——引用计数自动处理。
- **配额计算。** 文件版本占用的存储按所有版本之和计算，但引用去重后的物理空间由 `storage_refs` 追踪。管理员可以选择是否将历史版本计入用户配额。
- **GC 感知。** `storage/gc/sweeper.go` 的 `purgeSoftDeleted` 逻辑需要扩展：硬删除文件行时，级联检查 `file_versions` 中是否还有引用同一 `storage_key` 的记录。
- **版本上限。** 可配置每个文件的最大版本数（如 `max_versions_per_file: 100`），超过时自动淘汰最旧版本。
- **审计日志。** 每次版本创建/恢复/删除写入 `audit_events`。现有 `AuditLogger` 接口可直接复用。

### 边界情况

| Edge Case | 处理方式 |
|-----------|----------|
| **并发上传同一文件的两个版本** | `version` 使用 `SELECT COALESCE(MAX(version), 0) + 1 FROM file_versions WHERE file_id = ? FOR UPDATE` 原子递增 |
| **回滚到旧版本后"当前版本"指针** | restore 创建新版本（version = max+1），内容复制自旧版本。历史不被重写 |
| **分享中的文件被更新版本** | 分享者看到新版本；如有需要可通知所有分享者"文件已更新" |
| **版本删除与去重** | 版本删除时检查 `storage_refs.GetCount`，仅当引用归零时删除 blob |
| **软删除文件后版本保留** | 文件的软删除 cascade 到 `file_versions`（软删除列），GC 在适当时机硬删除 |

---

## 方向二：统一活动流（Unified Activity Stream / Timeline）

### 为什么需要它

aero-id 拥有完整的事件基础设施（事务性 `outbox` → `notification` → SSE 推送），但当前的**通知模型是面向消息的**——只推送给直接相关方（消息接收者、评论作者）。不存在一个**面向用户本人的、跨所有数据域的、按时间排序的活动记录流**。

1. **产品盲区。** 用户无法回答"我上周做了什么"。没有活动日志（Activity Log），用户不知道自己上传了什么文件、给谁发了消息、评论了什么内容。
2. **搜索的有限替代。** 全文搜索（`SearchService`）是跨实体的，但需要用户主动查询。活动流是被动呈现的——用户打开页面就能看到"你昨天上传了 3 个文件，回复了 5 条评论，收到了 2 条消息"。
3. **现有技术基础几乎完备。** Outbox 中的每个事件（`message.sent`、`comment.replied`、`file.share.grant` 等）都可以作为活动流的素材。只需要一个聚合读取模型 + 一个新的事件类型注册表。
4. **差异化能力。** 大多数竞品不做活动流。这是一个低投入、高感知价值的功能。

### 设计要点

#### 核心模型

```sql
CREATE TABLE activity_events (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_uid     TEXT NOT NULL,       -- 这条活动属于谁（不是"谁做的"，而是"谁的时间线"）
    actor_uid     TEXT NOT NULL,       -- 谁触发了这个事件
    event_type    TEXT NOT NULL,       -- 'file.uploaded', 'message.sent', 'comment.created', 'file.shared', ...
    subject_type  TEXT NOT NULL,       -- 'file', 'message', 'comment'
    subject_id    TEXT NOT NULL,       -- 资源的 ID
    target_uid    TEXT,                -- 如果事件涉及另一个用户（如消息发送），对方的 uid
    summary       TEXT NOT NULL,       -- 人类可读的描述，如 "你上传了「项目方案.pdf」" 或 "你给 Alice 发了消息"
    meta          JSONB,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_activity_owner_time ON activity_events(owner_uid, created_at DESC);
```

#### 事件映射表

从 Outbox 事件到 Activity Event 的映射（在 `NotificationDispatcher` 同层处理）：

| Outbox 事件类型 | 活动类型（owner_uid） | 摘要模板 |
|----------------|----------------------|----------|
| `message.sent` | 发件人(from_uid) | "你给 {to_uid} 发送了一条消息" |
| `message.sent` | 收件人(to_uid) | "你收到了一条来自 {from_uid} 的消息" |
| `comment.replied` | 回复作者 | "你回复了 {target_uid} 的评论" |
| `comment.replied` | 被回复者(parent_author) | "{replier_uid} 回复了你的评论" |
| `file.uploaded` | 文件所有者 | "你上传了「{filename}」" |
| `file.share.grant` | 分享者(owner) | "你将「{filename}」分享给了 {grantee_uid}" |
| `file.share.grant` | 被分享者(grantee) | "{owner_uid} 将「{filename}」分享给了你" |

#### API

```
GET /v1/activity?cursor=&limit=20          -- 我的活动流
GET /v1/activity?event_type=file.uploaded  -- 按类型过滤
```

#### 实现要点

- **不要 double-write。** Activity Event 不是从 handler 写入的——而是从 Outbox 派生，在 `NotificationDispatcher`（或下游的 `ActivityStreamSink`）中异步生成。这样不会增加请求路径的延迟。
- **事件类型注册表。** 在 `domain` 包中维护一个已知事件类型的集中映射，包括每个事件类型对应哪些 `owner_uid`（大部分事件会产生 1-2 条活动记录）。
- **文本摘要的国际化预留。** `summary` 使用结构化模板（如 `"file.uploaded": {actor} uploaded {subject}`），后续可以加 i18n。
- **存储策略。** 活动流数据量大，但价值随时间递减。默认保留 90 天，超过的可压缩或清除。使用 `created_at` 分区表可高效清理旧数据。
- **与现有通知的关系。** 通知（`notifications`）是**需要用户注意的事件**，用于驱动客户端行为（小红点、弹窗）。活动流（`activity_events`）是**历史记录**，用于用户回顾。两者互补但不重叠。

### 边界情况

| Edge Case | 处理方式 |
|-----------|----------|
| **用户删除资源后活动记录** | 活动记录是**不可变的**历史归档——资源被删除，活动记录仍然保留（摘要中的文件名可以回退到通用描述） |
| **活动风暴（批量上传 100 个文件）** | 活动流支持合并：`batch_uploaded` 事件类型 + `count` 字段，"你批量上传了 100 个文件" |
| **Block 用户的活动** | 如果 A Block 了 B，A 的活动流中不显示与 B 相关的活动（包括 A 发往 B 的消息） |
| **活动记录的 TTL 清理** | 周期性 Job：`DELETE FROM activity_events WHERE created_at < now() - interval '90 days'` |
| **隐私：文件分享活动的可见性** | 分享事件的双方（分享者与被分享者）都能看到该活动。非参与者不可见 |

---

## 方向三：定时 / 延迟动作引擎（Scheduled Actions Engine）

### 为什么需要它

aero-id 所有操作都是**即时**的——消息发送、文件上传、评论创建——没有任何定时或延迟能力。这是一个被低估的产品差距：

1. **用户刚需：定时消息。** "生日提醒"、"明天上午 10 点发送提醒"、"项目截止前 1 小时发消息"——这些是消息产品的标配。
2. **用户刚需：消息撤回（Recall）。** "发送后 5 分钟内可以撤回"是 IM 的基本功。当前消息一旦发送就无法收回（只能软删除，但接收方可能已经看到通知和 SSE 推送）。
3. **自动清理策略。** "自动删除 30 天前的消息"、"7 天后取消分享"——这些应该是用户可配置的策略，而不是需要手动执行的操作。
4. **基础设施已存在。** Outbox relay 提供了一个现成的**异步任务执行框架**——定时任务可以建模为"在未来时间点发布的一个 Outbox 事件"。

### 设计要点

#### 核心模型

```sql
CREATE TABLE scheduled_actions (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_uid     TEXT NOT NULL,
    action_type   TEXT NOT NULL,         -- 'message.send', 'message.recall', 'file.unshare', 'message.auto_delete', ...
    execute_at    TIMESTAMPTZ NOT NULL,  -- 首次执行时间
    status        TEXT NOT NULL DEFAULT 'pending',
                  -- 'pending', 'executing', 'completed', 'failed', 'cancelled'
    payload       JSONB NOT NULL,        -- 动作参数（如：{to_uid, content, kind}）
    max_attempts  INTEGER NOT NULL DEFAULT 3,
    attempt_count INTEGER NOT NULL DEFAULT 0,
    last_error    TEXT,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_scheduled_actions_execute ON scheduled_actions(execute_at, status)
    WHERE status = 'pending';
```

#### 核心 API

| 操作 | 说明 |
|------|------|
| `POST /v1/schedule` | 创建定时动作（指定 `action_type` + `execute_at` + `payload`） |
| `GET /v1/schedule` | 列出我的定时动作（分页，按状态/时间过滤） |
| `PATCH /v1/schedule/:id` | 修改执行时间或取消（`status = cancelled`） |
| `DELETE /v1/schedule/:id` | 删除定时动作 |

#### 高价值 Action Types

| Action Type | 描述 | 实现路径 |
|-------------|------|----------|
| `message.send` | 定时消息 | 到期后调用 `message.Send()` |
| `message.recall` | 消息撤回（发送后 N 分钟内可设置） | 到期后检查 `created_at + recall_window`，仍有效则软删除消息 + 发送撤回通知 |
| `file.auto_unshare` | 定时取消文件分享 | 到期后调用 `UnshareFile()` |
| `message.auto_delete` | 自动清理旧消息 | 扫描 `messages` 表，对匹配条件的消息执行 `SoftDelete()` |
| `notification.snooze` | 通知暂勿打扰 | 定时启用/禁用通知偏好中的静音时段 |

#### 执行引擎

`ScheduledActionWorker` 作为与 `outbox.Relay` 并行运行的独立 Worker：

```go
// 伪代码
func (w *Worker) Run(ctx context.Context) {
    ticker := time.NewTicker(10 * time.Second)
    for {
        select {
        case <-ticker.C:
            actions := w.repo.FetchDue(ctx, w.batch)
            for _, a := range actions {
                w.repo.UpdateStatus(ctx, a.ID, "executing")
                err := w.executor.Execute(ctx, a)
                if err != nil {
                    w.repo.MarkFailed(ctx, a.ID, err)
                } else {
                    w.repo.MarkCompleted(ctx, a.ID)
                }
            }
        case <-ctx.Done():
            return
        }
    }
}
```

#### 实现要点

- **执行器模式。** `ScheduledActionExecutor` 接口：`Execute(ctx, *ScheduledAction) error`。每个 `action_type` 注册一个 executor。
- **幂等性。** `execute_at + action_type + payload` 的哈希作为幂等键，防止重复执行。Worker 崩溃重启后不会重复执行已完成的动作。
- **消息撤回的时限窗口。** `recall_window` 是撤回的有效期（如 5 分钟）。超过窗口的撤回请求被拒绝。这个检查在 executor 中执行。
- **事务性。** 执行动作的所有副作用（发送消息、删除文件等）必须在一个事务中完成。利用现有的 `outbox.NewEvent` 机制——动作执行时写入领域事件，由 Outbox 异步处理。
- **优雅关机。** Worker 在收到停止信号时，完成当前批次后再退出。

### 边界情况

| Edge Case | 处理方式 |
|-----------|----------|
| **定时消息发出后接收方 block 了发送方** | 执行时检查 block 关系（复用现有 `block.BlockChecker`），blocked 则标记为 failed 通知用户 |
| **撤回定时设置后消息已被读取** | 撤回逻辑检查 `read_at`：如果已读，撤回失败（但不能让发送方知道对方已读——隐私） |
| **大量定时任务在同一时刻到期** | `FetchDue` 使用 `LIMIT` + `FOR UPDATE SKIP LOCKED`，多个 Worker 实例可并行消费 |
| **定时消息的接收方在发送前注销了 SSO 账号** | 执行时目标用户不存在→标记为 failed，通知发送方 |
| **动作执行时 SSO 不可用** | 依赖 SSO 的动作（如验证接收方存在性）通过断路器重试；跨重试上限后标记 failed |
| **用户删除自己的帐号前** | 展示所有待执行的定时动作，让用户选择取消或保留 |

---

## 方向四：自适应限流与滥用智能（Adaptive Rate Limiting & Abuse Intelligence）

### 为什么需要它

当前的限流器是**静态、无状态、单实例**的：

```go
// internal/pkg/ratelimit/ratelimit.go
// 硬编码的 token bucket，每实例独立，无共享状态
limiter := ratelimit.New(cfg.RateLimit.RPS, cfg.RateLimit.Burst, idleTTL)
```

这在生产环境中有四个根本性缺口：

1. **静态阈值无法适应负载模式。** 高峰期（晚 8 点）和低谷期（凌晨 4 点）使用相同的 RPS 限制。要么高峰期不够用，要么低谷期限制过松。
2. **多实例绕行。** Comment in code already: "Distributed/global rate-limiting is intentionally out of scope"。4 个实例各 20 RPS = 实际容量 80 RPS。
3. **无智能检测。** 不能区分"正常用户的突发行为"和"攻击者的探测行为"。一个用户 5 分钟内发送 50 条消息可能是正常聊天，也可能是垃圾消息。
4. **无分层保护。** 所有 API 端点共享同一个限流配额。文件上传（高成本操作）和消息列表（低成本操作）没有区别对待。

### 设计要点

#### 架构层次

```
┌─────────────────────────────────────────────┐
│ Layer 4: 行为分析层                          │
│  模式识别、异常检测、信誉评分                  │
├─────────────────────────────────────────────┤
│ Layer 3: 自适应限流层                         │
│  Redis 滑动窗口 + 动态阈值调整                 │
├─────────────────────────────────────────────┤
│ Layer 2: 分布式静态限流层 (new)               │
│  Redis-backed token bucket，CF 共享状态       │
├─────────────────────────────────────────────┤
│ Layer 1: 本地静态限流层 (existing)            │
│  per-instance memory token bucket (fallback)  │
└─────────────────────────────────────────────┘
```

#### Layer 2: 分布式静态限流

```go
// 新接口，与原 in-memory Limiter 共享同一接口
type Limiter interface {
    Allow(ctx context.Context, key string) (bool, time.Duration)
}

// Redis-backed 实现
type RedisLimiter struct {
    client *redis.Client
    rps    float64
    burst  int
}

func (l *RedisLimiter) Allow(ctx context.Context, key string) (bool, time.Duration) {
    // Lua 脚本：滑动窗口 + INCR + EXPIRE
    // key: "ratelimit:{uid}:{endpoint}"
    // 返回是否允许 + 下次允许前的等待时间
}
```

配置化切换：`rate_limit.driver = memory | redis`，默认 `memory` 保持向后兼容。

#### Layer 3: 自适应阈值

不是静态配置 `rps: 20`，而是根据**历史模式 + 系统负载**动态调整：

```go
type AdaptiveConfig struct {
    BaseRPS        float64  // 基数，如 20
    MaxRPS         float64  // 上限，如 100
    MinRPS         float64  // 下限，如 5
    WindowSize     Duration // 统计窗口，如 5 分钟
    Sensitivity    float64  // 敏感度（0-1），越高越容易降级
}
```

**动态调整策略：**

| 信号 | 响应 |
|------|------|
| 该用户的 P99 延迟升高 > 30% | 降低该用户限流阈值的 20% |
| 该用户的错误率（4xx+5xx）> 5% | 降低该用户限流阈值的 50% |
| 系统整体 CPU > 80% | 所有用户阈值降低 30% |
| 该用户历史同时间段行为模式 | 根据 7 天历史基线动态设定阈值上限 |

#### Layer 4: 行为分析

引入**可插拔的匿名化行为分析管道**（不可追溯到具体用户，只检测异常模式）：

| 检测器 | 检测内容 | 响应 |
|--------|----------|------|
| **速率异常器** | 单一 uid 的请求速率突然暴增 >10x 基线 | 临时限制至基线的 50% |
| **内容异常器** | 短时间内大量相同内容的评论/消息 | 触发内容审核（status = hidden） |
| **模式异常器** | 爬虫模式：固定间隔的请求，遍历 ID 序列 | 返回 429 + 要求 Captcha |
| **IP 聚合器** | 多 uid 从同一 IP 以相似模式访问 | 对该 IP 加全局限流 |
| **新账号检测器** | 创建 < 24h 的 uid 的高风险操作限流更严格 | 新账户阈值 = 正常阈值的 20% |

#### API 端点（管理员）

```
GET /v1/admin/ratelimit/status        -- 当前限流整体状态
GET /v1/admin/ratelimit/users         -- 被限制最多的用户列表
GET /v1/admin/ratelimit/users/:uid    -- 特定用户的限流详情
DELETE /v1/admin/ratelimit/users/:uid -- 手动清除某用户的限流状态
```

#### 实现要点

- **数据隐私第一。** 行为分析层不存储 PII（个人可识别信息），只存储聚合特征（hash(uid) + 时间窗口 + 请求特征向量）。
- **通过/拒绝的判定链。** 每个请求按 `Layer 4 → Layer 3 → Layer 2 → Layer 1` 顺序判定。任一 Layer 拒绝则拒绝；所有 Layer 通过则通过。
- **降级策略。** Redis 不可达时，Layer 2 和 Layer 3 回退到 Layer 1（当前行为），保证不停服。
- **可观测性。** 每个 Layer 暴露指标：`layer_allowed_total`、`layer_denied_total`、`current_threshold`、`adaptive_adjustments_total`。

### 边界情况

| Edge Case | 处理方式 |
|-----------|----------|
| **误杀正常用户（false positive）** | Layer 4 不自动封禁——只降级限流阈值。管理员可通过 API 手动恢复 |
| **Redis 故障** | 逐层降级：Layer 3 → Layer 2 → Layer 1（最终回到纯内存模式，不停服） |
| **新用户的冷启动行为基线** | 前 24 小时使用保守阈值（正常用户的 20%），之后逐步建立基线 |
| **DDos 攻击：大量不同 uid 均匀请求** | IP-based 限流（Layer 2）+ 整体容量限流（Layer 3 检测系统负载） |
| **行为分析数据的存储成本** | 使用 `cron` 压缩：聚合窗口的旧数据以日均值摘要存储，原始数据保留 7 天 |

---

## 方向五：数据平台 / 嵌入式分析（Data Platform & Embedded Analytics）

### 为什么需要它

aero-id 生产了大量的用户行为数据（文件上传、消息发送、评论创建、搜索查询），但**完全没有分析能力**：

1. **产品决策盲区。** "用户最常用哪些功能？"、"文件上传的平均大小是多少？"、"搜索查询的热门关键词是什么？"——这些基本问题当前无法回答。
2. **用户价值。** 用户想知道自己的使用统计："你本月发送了 128 条消息，上传了 15 个文件"。这些"年度报告"式的数据能显著提升用户粘性。
3. **运维价值。** "哪个用户的存储用量最高？"、"通知系统的 SSO 调用量趋势？"——当前没有大盘视图。
4. **基础设施已就位。** 现有 `metrics.Registry` 记录了请求级别的 RED 指标（Rate/Errors/Duration），但没有**面向业务的分析数据**。

### 设计要点

#### 架构

```
┌──────────────┐    outbox events    ┌──────────────────┐
│  Application  │ ──────────────────► │  Analytics Sink   │
│  (handler→    │   (async, off-path) │                   │
│   service→    │                     │  ┌──────────┐    │
│   repository) │                     │  │ Aggregator│    │
└──────────────┘                      │  └──────────┘    │
                                      └───────┬──────────┘
                                              │ batch insert
                                              ▼
                                     ┌──────────────────┐
                                     │  analytics_events │
                                     │  (PostgreSQL)     │
                                     └──────────────────┘
                                              │
                                     ┌───────▼──────────┐
                                     │   REST API       │
                                     │  /v1/analytics/*  │
                                     │  /v1/admin/stats/*│
                                     └──────────────────┘
```

#### 核心模型

```sql
-- 分析事件（预聚合的统计记录）
CREATE TABLE analytics_events (
    id         BIGSERIAL,
    owner_uid  TEXT,                    -- NULL = 全局统计
    event_type TEXT NOT NULL,           -- 'file.uploaded', 'message.sent', 'search.query', ...
    metric     TEXT NOT NULL,           -- 'count', 'bytes', 'latency_p50', 'latency_p99', ...
    value      DOUBLE PRECISION NOT NULL,
    bucket     TIMESTAMPTZ NOT NULL,    -- 时间桶（按小时对齐）
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    PRIMARY KEY (owner_uid, event_type, metric, bucket)
);

-- 搜索查询日志（用于搜索热点分析，不包含 PII）
CREATE TABLE search_query_log (
    id         BIGSERIAL PRIMARY KEY,
    query_hash TEXT NOT NULL,           -- SHA256(query)，不可逆
    user_hash  TEXT NOT NULL,           -- SHA256(uid)，不可逆
    kind       TEXT,                    -- 'messages', 'comments', 'files'
    hit_count  INTEGER NOT NULL DEFAULT 0,
    timestamp  TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

#### 使用场景与 API

| 场景 | API | 说明 |
|------|-----|------|
| **用户个人统计** | `GET /v1/me/stats?period=30d` | 上传文件数、消息数、评论数、存储用量 |
| **全局业务看板** | `GET /v1/admin/stats/dashboard` | DAU、上传总量、消息总量、热门搜索词 |
| **存储趋势** | `GET /v1/admin/stats/storage?granularity=day` | 存储用量时间序列（去重后/逻辑上） |
| **热门搜索词** | `GET /v1/admin/stats/search-top?limit=20` | 过去 24h 的热门搜索（仅 hash，不可逆） |
| **用户排行榜** | `GET /v1/admin/stats/top-users?metric=storage_bytes` | 按存储用量/消息数排序的用户 |

#### 实现要点

- **异步写入（Analytics Sink）。** `AnalyticsSink` 实现 `outbox.Handler` 接口，作为 Chain 的一环。每次 Outbox 事件经过时，根据事件类型更新聚合计数器。不阻塞请求路径。
- **预聚合（Rollup）。** 原始数据→每小时聚合→每天聚合→删除原始数据。90 天前的数据聚合为天级精度，365 天前的数据仅保留周级精度。
- **搜索分析的隐私保护。** `search_query_log` 中的 `query_hash` 和 `user_hash` 是单向哈希，无法还原原始搜索词或用户身份。仅用于趋势分析。
- **存储统计的去重感知。** 文件存储用量统计需区分"逻辑用量"（所有版本之和）和"物理用量"（去重后的 `storage_refs` 实际占用）。两者都应暴露。
- **查询缓存。** 统计 API 返回的数据可以缓存 5-60 分钟（取决于粒度），避免每次请求都扫描大量数据。
- **与现有 Metrics 的关系。** 现有的 RED 指标（RPS、错误率、延迟）用于运维监控。分析平台的数据用于**产品分析和业务决策**。两者不重叠。

### 边界情况

| Edge Case | 处理方式 |
|-----------|----------|
| **超大用户的统计聚合** | 对超大用户（如存储 > 100GB）的统计通过独立定时 Job 计算，不走实时聚合路径 |
| **搜索热门的隐私争议** | 只存 query_hash（不可逆），不存原文。即使 hash 碰撞攻击者也无法还原准确搜索词 |
| **分析数据存储成本** | `analytics_events` 使用分区表（按 bucket 月分区），旧分区自动删除 |
| **用户注销后分析数据** | 匿名化（删除 owner_uid 但保留聚合统计），或按策略全部删除 |
| **不同时区的统计桶** | 所有时间桶以 UTC 对齐，在查询时按客户端时区转换 |

---

## 综合优先级建议

| 方向 | 用户价值 | 架构杠杆 | 技术复杂度 | 推荐优先级 | 启动前置条件 |
|------|:--------:|:--------:|:----------:|:----------:|-------------|
| **1. 文件版本管理** | 🔥🔥🔥🔥 | 高（复用 storage_refs + GC） | 中 | **P1** | 无 |
| **2. 统一活动流** | 🔥🔥🔥🔥🔥 | 高（复用 Outbox 基础设施） | 低-中 | **P1** | 无 |
| **3. 定时动作引擎** | 🔥🔥🔥🔥 | 中（复用 Outbox relay 模式） | 中 | **P2** | 方向 2 先完成（共享 execution 模式） |
| **4. 自适应限流** | 🔥🔥🔥 | 低（运维价值 > 用户价值） | 高 | **P3** | Redis 引入决策 |
| **5. 数据平台** | 🔥🔥🔥 | 中（复用 Metrics + Outbox） | 中 | **P2** | 方向 2 先完成（共享 Event Sink 模式） |

### 执行建议

- **立即启动（低风险、高 ROI）：方向 2（活动流）。** 完全复用现有 Outbox + NotificationDispatcher 的架构模式，可以在 1-2 周内交付。无数据模型变更（只需要一张新表 + 一个 Outbox Handler）。
- **紧随其后（有杠杆效应）：方向 1（文件版本管理）。** 文件去重和引用计数机制已经完整，版本管理是"水到渠成"的扩展。版本管理完成后，文件的"不可变性"限制被解除，后续所有文件相关功能（分享增强、评论引用具体版本）都受益。
- **捆绑执行：方向 3（定时动作）+ 方向 2（活动流）。** 两者都使用 Outbox Sink 模式。可以先完成方向 2 的活动流框架，然后基于同样的 Outbox Handler 接口实现定时动作的执行器。两个方向共享 `scheduled_actions` 表的 worker 和 `activity_events` 表的 sink。
- **暂缓：方向 4（自适应限流）。** 技术复杂度高、用户价值低。建议在出现明确的限流误判投诉或流量攻击事件后再启动。

---

## 附录：与现有设施的关系矩阵

| 新方向 | 依赖的现有设施 | 需要修改的现有代码 |
|--------|---------------|-------------------|
| **方向 1: 文件版本管理** | `storage_refs`、`FileStorage.Put/Delete`、`GC sweeper`、`audit_events` | `file/service_crud.go`、`file/repository.go`、`storage/gc/sweeper.go`、新增 `file/version.go` |
| **方向 2: 统一活动流** | `outbox.Event`、`outbox.Handler`、`NotificationDispatcher` 的 Sink Chain | 新增 `activity/sink.go`、`activity/repository.go`、`activity/handler_http.go` |
| **方向 3: 定时动作引擎** | `outbox` 的异步执行模式、`block.BlockChecker`、`notification.Dispatcher` | 新增 `schedule/worker.go`、`schedule/repository.go`、`schedule/executor.go` |
| **方向 4: 自适应限流** | `pkg/ratelimit.Limiter` 接口、Redis（新增依赖）、`middleware` 的限流集成 | `pkg/ratelimit/` 新增 `redis.go`、新增 `pkg/ratelimit/adaptive.go`、`internal/app/app.go` |
| **方向 5: 数据平台** | `metrics.Registry`、`outbox.Event`、SSO 用户解析（用于去匿名化） | 新增 `analytics/sink.go`、`analytics/handler_admin.go`、`analytics/handler_user.go` |

---

*本文仅包含全局代码扫描驱动的分析，未涉及任何代码修改。建议作为后续 Sprint 规划讨论的输入。*
