# aero-id: 下一波高价值扩展方向

> 日期：2026-07-10 · 基于 `main` 分支全量代码扫描
> 视角：资深架构师 + 产品经理 · 未编写任何代码

---

## 当前状态摘要

aero-id 已从一个以文件为中心的数据服务演进为一个具备完整消息、评论、通知、搜索、审计、block、admin 权限、存储 GC、outbox 事件驱动等能力的企业级后端。核心特性：

| 领域 | 已实现 |
|------|--------|
| 文件 | CRUD + 内容寻址去重 + 流式上传 + presigned S3 + 元数据更新 + 共享/撤销 + 存储 GC 回收 |
| 消息 | 发送/收件箱/对话/已读回执/编辑/删除/Reactions/全文搜索 + block 感知 |
| 评论 | 嵌套两层 + 点赞/取消 + 举报/审核队列/隐藏/编辑/搜索 |
| 通知 | outbox 驱动 + SSE 实时推送 + last_event_id 重连 + 偏好 + 折叠 + 速率限制 |
| 安全 | 方向性 block + 权限角色 + 审计日志 + 幂等性 key + rate limiting |
| 可观测 | RED metrics + OTel tracing + 逐依赖 /readyz + 熔断器 + 优雅关闭 |
| 搜索 | 全文搜索 (Postgres FTS) 跨消息/评论/文件 |
| gRPC | HTTP ↔ gRPC 全对等 (38 RPCs) |

22 项迁移、全部可逆。单元测试 + 集成测试（testcontainers）通过。

---

## 推荐方向

以下 5 个方向中，前 3 个基于现有基础设施有"低成本、高杠杆"的切入点，后 2 个是面向平台化的远期能力储备。每个方向都附有"为什么现在做"的判断逻辑。

---

## 方向 1：多对多群组 / 频道 / 房间

### 问题

当前消息模型是严格的 **1-to-1**：`messages` 表只有 `from_uid` 和 `to_uid`，无 `conversation_id` / `room_id` / `group_id`。群发只能通过遍历 to_uid 列表、每条消息独立存储来实现，且接收方感知不到"这是一条群消息"。

通知系统同样只支持按 uid 推送，无"频道订阅"语义。

### 为什么需要

- **产品层面**：单聊 + 评论的组合无法覆盖团队协作场景（项目组讨论、频道公告、客服对话）。群组/频道是私信之外最基础的消息形态。
- **基础设施层面**：现有 outbox + SSE hub + notification dispatcher 已经具备事件驱动分发的能力骨架。只需要引入 `group_members` + `group_messages`/`room_assignments` 概念，即可将通知推送从"发给一个 uid"扩展为"发给一组 uid"。
- **切入成本低**：消息系统已经是接口完备的，`MessageRepository` 的抽象层允许在保持现有 1-to-1 功能完整的同时，新增一个 `GroupMessageRepository` 实现，从架构上不必重构已有代码。

### 架构影响

- 新增 `internal/groups/` 包（或 `internal/room/`），包含：群组 CRUD、成员管理、角色（owner/admin/member）、消息路由
- `messages` 表增加可空 `group_id` 列（或新增 `group_messages` 表），`ListInbox` / `ListConversation` 可扩展为包含群组消息
- 通知分发增加"按群组成员展开"的逻辑：outbox 中写入一条 `group.message.sent` 事件 → dispatcher 查询成员列表 → 为每个成员创建通知 + SSE 推送
- Block 系统需要扩展：阻塞一个用户时应可选择"屏蔽其所有群组消息"

### 边界情况

| 场景 | 处理 |
|------|------|
| 群主删除群组后未读消息 | 软删除群组，保留消息显示"已删除的群组" |
| 成员被移除后看到历史消息 | 按加入时间过滤，移除后不可见新增消息 |
| 超大群 (10k+ 成员) 的消息推送 | 通知不能逐条展开，改用聚合轮询或仅标记未读计数 |
| 群聊中的回复引用 | 扩展 `ParentID` 逻辑支持引用特定消息 |

---

## 方向 2：文件多版本管理 + 共享链接生命周期

### 问题

文件目前是"上传即终态"的：
- 更新文件内容就是上传一个全新的 `File` 记录，旧记录被软删除。**没有版本历史**——无法追溯谁在什么时候改了文件。
- 共享是永久性的——`Grant` 之后目标用户可以无限期访问，无法设置过期时间。
- 无法生成"一次性下载链接"或"密码保护的分享链接"。

### 为什么需要

- **产品层面**：文件服务如果有版本历史，就可以支撑"文档协作"场景（覆盖、回滚、diff）。分享链接的过期 + 密码保护是企业合规的基本要求。
- **基础设施层面**：`storage_refs` 表（migration 000021）已经实现了**存储键引用计数**——这恰好是版本管理所需的基础。一个文件的不同版本可以共享相同的存储键（未修改的部分通过内容寻址去重指向同一 blob），版本元信息独立记录。
- **切入成本低**：GC sweeper 已经具备 `RefCounter` 接口，版本管理只需要在 `files` 表外新增 `file_versions` 表，即可复用现有引用计数 + 去重 + GC 全链路。

### 架构影响

- 新增 `file_versions` 表：(file_id, version_number, storage_key, checksum, size, mime_type, created_by, created_at, meta)
- Service 层 `UpdateContent` / `UploadNewVersion` 方法：写入新版本 + 更新主记录
- `ShareFile` 支持 `expires_at` 参数：GC sweep 或定时任务清理过期分享
- `PresignUpload` 返回的 URL 可配合 `ttl` 参数限制上传窗口
- 下载 URL 生成可选一次性令牌（统计 `token` 表 + `used_at` 标记）

### 边界情况

| 场景 | 处理 |
|------|------|
| 并发上传同一文件的两个版本 | 均由内容寻址决定 storage_key；version_number 由 service 层 CAS 保证递增 |
| 删除文件时是否删除所有版本 | 软删除文件 + 版本行；GC 做硬删除时检查版本引用计数 |
| 分享的过期链接在过期后访问 | 返回 410 Gone 而非 404，提示"分享链接已过期" |
| 百万级文件版本数 | 分页查询，限制单个文件最多保留 N 个版本（可配置） |

---

## 方向 3：智能防滥用体系（Spam Detection + Rate Limit by Action + Appeal Workflow）

### 问题

当前的防滥用只有两层：
1. **全局 rate limiting**（per-actor token bucket）——对所有请求一视同仁，无法区分"发消息"和"看消息"的滥用权重。
2. **用户 block**——被动防御，且只屏蔽消息和通知，不阻止被 block 的用户点赞/评论/举报。
3. **评论举报**——手动、作者自审、无自动处理。

缺少：自动垃圾/骚扰内容检测、分级限流（发消息限流阈值低于读操作）、举报→审核→申诉的完整工作流、静默限流（shadow ban）。

### 为什么需要

- **产品层面**：一旦服务达到一定规模，bot 攻击、群发 spam、恶意举报就是必然后果。被动防御意味着运营成本线性增长。
- **基础设施层面**：已有 outbox 事件总线 + audit log + rate limiter + admin roles。这些组件组合起来可以构成一个"检测→裁决→执行"的防滥用管道，而不需要从零搭建。

### 架构影响

- **分层限流**：`middleware/ratelimit.go` 扩展为支持按 action 类别限流（如 `message.send` 与 `message.list` 使用不同配额），配置下沉到 `config.yaml`
- **内容安全钩子**：在 service 层的 `Send` / `CreateComment` 中插入可插拔的 `ContentModerator` 接口（初始实现可以用正则/关键词过滤，未来可对接外部审核 API）
- **自动降权**：新增 `shadow_ban` 标记或 `trust_score` 字段，block 记录可附带 `mute` 类型（静默使内容对他人不可见但不提示用户）
- **申诉工作流**：利用 outbox 事件触发通知给审核管理员，admin 端可查看举报历史 + 判决记录 + 申诉记录
- **举报对象扩展**：当前只支持举报评论，扩展到支持举报消息、文件、用户

### 边界情况

| 场景 | 处理 |
|------|------|
| 误判正常内容为 spam | 申诉流程必须最终由人工审核裁决；内容审核钩子仅标记不自动删除 |
| 攻击者利用举报机制骚扰正常用户 | 举报自身需要限流 + 反滥用：同一用户 24h 内举报上限，同一内容被 N 人举报才触发审核 |
| shadow ban 用户发现异常 | 这是预期的——shadow ban 的设计就是不让攻击者感知到已被限制 |
| 多语言 spam 检测 | 初始用正则 + 频率模式匹配；预留接口给 ML 模型接入 |

---

## 方向 4：Webhook 平台 / 事件生态（Event Subscriptions + Delivery Guarantees）

### 问题

当前 outbox webhook 是**单出口、全量转发**：
- 只支持一个 webhook URL
- 转发所有事件类型（或配置的固定白名单）
- 无去重、无重试策略定制、无签名算法可选
- 无回调鉴权（除了 HMAC 签名）

客户端（如其他微服务、移动端推送网关）无法按需订阅特定事件。

### 为什么需要

- **产品层面**：一个支持外部集成的平台需要给第三方提供可编程的事件钩子——"当我的文件被访问时通知我的服务"、"当有人回复我的评论时推送到钉钉/企微"。这是从"功能"到"平台"的跨越。
- **基础设施层面**：outbox 的 `Handler` 接口已经是可组合的（`Chain`），`Repository` 已经支持事件查询。在此基础上增加 `webhook_subscriptions` 表 + 订阅管理 API + 独立的 `Dispatcher` 实现，即可构建完整的 webhook 平台。

### 架构影响

- 新增 `webhook_subscriptions` 表：(id, owner_uid, url, event_types[], secret, headers, retry_config, active, created_at)
- 新增 `internal/webhook/` 包：订阅管理 service + HTTP handler（CRUD 端点） + 事件分发器
- 分发策略变更：outbox relay 不再只有 `Chain` 式单链，改为 fan-out：查询该事件类型的所有活跃订阅 → 并行分发
- 交付保障：每个订阅独立记录 delivery 日志（`webhook_deliveries`），支持重试 + 死信 + 手动重放
- 签名算法扩展：除 HMAC-SHA256 外支持 `X-Hub-Signature` (SHA1) 等标准

### 边界情况

| 场景 | 处理 |
|------|------|
| 下游服务响应慢导致积压 | 每个订阅使用独立 goroutine + buffered channel + 超时；超过 N 次失败自动暂停订阅 |
| 恶意用户创建大量无用 webhook | 按用户限制最大活跃订阅数；事件频率计入 rate limit |
| 更新订阅密钥后如何保证旧事件不失效 | 每个 delivery 记录签名时使用当时密钥；重放时用最新密钥重新签名 |
| 端到端延迟要求 < 1s | relay 使用 push 模式（事务提交后立即触发）而非 poll 模式（避免 5s 轮询间隔） |

---

## 方向 5：多租户 / 工作空间隔离（Organizations / Workspaces）

### 问题

当前所有数据是**扁平的用户归属**——每个 `uid` 独立拥有自己的文件、消息、评论。没有任何"组织"、"团队"、"工作空间"的概念。

这意味着：
- 无法让一个团队拥有共享的文件库（共享工作是逐个 grant，不具备继承性和可管理性）
- 无法让企业客户为员工统一管理权限
- 所有 block、notification preference 都是个人维度的，无法按组织维度统一配置

### 为什么需要

- **产品层面**：多租户是 SaaS 化的基础。如果目标是成为"面向团队的数据平台"，工作空间隔离是必须的。它直接影响定价模型（按空间/按成员计费）和合规（GDPR 数据隔离）。
- **基础设施层面**：现有的 `owner_uid` 字段无法轻易扩展为"组+用户"的复合归属。但引入 `TenantID` 或 `WorkspaceID` 维度后，现有所有表（files, messages, comments, notifications）都可以通过新增一列来隔离，而不需要改变核心数据模型。这是一个"成本高但杠杆极高"的投资。

### 架构影响（最大、最深远）

- **元数据侵入**：所有表增加 `workspace_id`（可空，向后兼容）。现有单用户数据视作 `workspace_id = NULL`。
- **新增表**：`workspaces`（id, name, owner_uid, settings, created_at）、`workspace_members`（workspace_id, uid, role, joined_at）
- **权限系统**：现有 `roles` 表扩展支持 workspace 级别的角色 + resource 级别的 ACL（文件/消息属于哪个 workspace，谁可以读/写/管理）
- **服务层重构**：`service.WithActor()` 扩展为 `service.WithWorkspace()`，列表查询增加 workspace 过滤
- **计费/配额**：workspace 级别配置：存储上限、成员上限、功能开关
- **数据导出**：每个 workspace 支持一键导出所有数据（GDPR）

### 边界情况

| 场景 | 处理 |
|------|------|
| 用户同时属于多个 workspace | 每次请求传递 `X-Workspace-ID` header；切换 workspace 相当于切换数据视图 |
| 用户被移出 workspace 后数据归属 | 作为"内容创作者"，评论/消息保留但显示"已离开"；文件归 workspace 所有 |
| workspace 删除 | 软删除 workspaces + 数据标记为 orphan；保留期后 GC 清除 |
| workspace 级别 block 与个人 block 的关系 | workspace 管理员可以屏蔽某用户在 workspace 内的发言（空间级 block），不影响该用户与其他用户的个人通信 |

---

## 推荐实施顺序

| 方向 | 优先级 | 杠杆率 | 风险 | 建议时机 |
|------|--------|--------|------|----------|
| 1. 群组/频道 | P1 | 高（基础通信模型升级） | 中 | 下一个迭代 |
| 2. 文件版本/分享生命周期 | P1 | 高（文件服务核心能力补齐） | 低 | 下一个迭代 |
| 3. 防滥用体系 | P0 | 高（生产安全必备） | 低 | **立即开始**——攻击不等人 |
| 4. Webhook 平台 | P2 | 中（生态基础） | 中 | 方向 1-3 完成后 |
| 5. 多租户/工作空间 | P3 | 极高（SaaS 基础） | 高（架构侵入） | 明确商业化方向后再启动 |

---

## 关于边界的补充思考

上述 5 个方向未涉及但值得纳入长期观察的领域：

1. **消息撤回 (Recall)**：与编辑不同，撤回是在时间窗口内从接收方视角消除已发送消息。对群组场景尤其重要。可在方向 1 之后追加。
2. **文件预览 / 缩略图生成**：图片、PDF 的预览需要异步处理管道。可与方向 2 的版本管理配合，在上传完成时触发缩略图生成。
3. **评论/消息的富文本支持**：当前 content 是纯文本。Markdown 渲染、@提及、表情包 requires 扩展评论模型。
4. **离线推送 (Push Notification)**：SSE 只在客户端在线时有效。移动端需要 APNS/FCM 网桥。方向 4 的 webhook 平台可为外部推送服务提供事件源。
5. **数据分析 / 运营看板**：现有 metrics 只覆盖服务健康度，无业务指标（DAU、消息量、文件存储增长）。可考虑将 outbox 事件导出到数据管道。
