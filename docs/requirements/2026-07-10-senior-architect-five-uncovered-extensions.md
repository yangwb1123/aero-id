# aero-id: 架构师视角的五个高价值扩展方向

> 日期：2026-07-10
> 范围：全局代码扫描后的架构推演
> 角度：资深架构师 × 产品经理，不做重复已有分析的事

---

## 扫描结论：已完成的功能基线

在提出扩展之前，先确认当前系统具备的能力（避免重复造轮子）：

- **文件**：CRUD、分片上传、S3 presigned 直传、内容去重、分享、GC 回收 + storage ref count、MIME 白名单、大小限制
- **消息**：1:1 P2P 消息、收件箱/对话、已读回执、编辑/删除、表情反应、已读数、Block 拦截、系统消息
- **评论**：双层嵌套（楼中楼）、点赞、举报/解决、隐藏/取消隐藏、编辑/删除、reply_count 反范式
- **通知**：SSE 实时推送、outbox 驱动、折叠/聚合、按类型限速、免打扰时段、Webhook 回调、Block 抑制
- **搜索**：PostgreSQL FTS 全文搜索 + CJK ILIKE 降级
- **基础设施**：双传输（HTTP + gRPC）、OpenTelemetry 链路追踪、RED 指标、限流、幂等、熔断（SSO）、多级缓存

以下五个方向 **在代码中完全或几乎不存在**，且每个都有明确的架构证据。

---

## 方向一：多参与者会话（群聊 / 频道）

### 为什么需要

当前 `Message` 模型只有 `FromUID` / `ToUID` 两个字段，消息严格限定在**两人之间**。`ListConversation(ctx, uidA, uidB)` 签名明确只接受两个 UID。`MessageKind` 枚举（Text/Image/File/System）完全没有 "Group" 或 "Channel" 类型。产品上，这意味着任何"群聊"、"频道"、"团队空间" 场景都无法支持——但消息功能本身已经完备，这是最直接的瓶颈。

### 代码证据

1. `internal/domain/message.go:Message` 没有 `ConversationID` 字段，没有 `ParticipantCount`；
2. `internal/repository/message.go:ListConversation()` 写死了 `(from_uid = ? AND to_uid = ?) OR (from_uid = ? AND to_uid = ?)` 的二元条件；
3. `internal/service/service.go:SendMessageInput` 只有 `ToUID string`（单接收者），没有 `ToUIDs []string`；
4. 整个代码库没有 `conversations` 或 `channels` 表迁移（migrations 只到 000022，都是关于 admin/notif 的）；
5. `internal/handler/grpc/aeropb/aero_id.proto:Message` message 也没有 conversation_id 字段。

### 需要解决的问题

- **新实体**：`Conversation`（ID, 类型, 名称, 创建者, 创建时间）+ `ConversationParticipant`（conversation_id, uid, role, joined_at）
- **消息模型改造**：`Message.ToUID` → `Message.ConversationID`（保持向后兼容：单聊可视为两人 conversation）
- **权限模型**：谁可以拉人、踢人、改群名；退出后是否可读历史
- **通知适配**：`NotificationDispatcher` 需要理解群消息的收件人是全体参与者（除了发送者自己），并且支持 @mention 定向通知
- **已读回执**：群消息的已读回执是"谁读了"的列表，不再是一个布尔值

### 优先级判断

| 维度 | 评分 |
|---|---|
| 产品价值 | ★★★★★ — 群聊是所有 IM 系统的核心功能 |
| 架构影响 | ★★★★★ — 涉及 domain、repository、service、gRPC proto、HTTP API 所有层 |
| 实施复杂度 | ★★★★☆ — 需要新增实体 + 重构现有消息读写路径，但基础设施（outbox、notif）可直接复用 |
| 与现有代码冲突 | 无破坏性——1:1 单聊退化为 Conversation 的一种特例 |

---

## 方向二：内容审核与控制面（Moderation Backplane）

### 为什么需要

当前评论区仅提供"作者自审"模式：一个评论只有其**作者本人**能看到举报列表并解决。`ListReports` 的权限检查是 `c.OwnerUID != actor`。消息和文件完全没有举报/标记机制。对于任何有公开内容（public 文件、评论）的系统，缺少集中审核面板意味着无法处理滥用、骚扰、违法内容。`AdminHandler` 目前只有一个 `GET /v1/admin/audit-log` 端点——审核功能完全缺失。

### 代码证据

1. `internal/service/comment.go:ListReports()` 和 `ResolveReport()` 全部限制为 `c.OwnerUID != actor`；
2. `internal/handler/http/admin.go` 只有一个 `ListAudit` 方法，没有 `GET /admin/reports`、`PATCH /admin/reports/:id`、`POST /admin/content/hide`；
3. `internal/domain/notification.go:NotificationType` 枚举没有 `content.reported`、`user.warned` 等审核事件；
4. `internal/service/service.go:CommentService` 没有 `ListAllReports`（跨用户）或 `EscalateReport` 方法；
5. 消息和文件 domain struct 没有 `Status` 或 `Flags` 字段用于标记违规/隐藏/待审。

### 需要解决的问题

- **集中报告队列**：新 `ReportQueue` service，允许 admin 角色查看**所有**开放的举报，按时间/类型/被举报人排序
- **全局隐藏/封禁**：admin 可以强制隐藏任意评论、限制用户发消息/评论（继承 `Block` 逻辑但为对称的）
- **消息举报**：`Message` 增加举报机制（当前只有评论有 `CommentReport`）
- **文件审核**：`File` 增加 `Status`（pending/rejected/approved）+ 举报功能
- **Auto-mod 钩子**：在 `commentService.Create()` 和 `messageService.Send()` 中插入可选的审核回调（关键词过滤、频率检测、图片 NSFW 检查）
- **通知扩展**：用户举报后 track 处理状态，处理完成后通知举报人

### 优先级判断

| 维度 | 评分 |
|---|---|
| 产品价值 | ★★★★☆ — 对 B2C/社区场景必不可少；B2B 场景中等优先级 |
| 架构影响 | ★★★☆☆ — 主要在 service 和 handler 层新增，domain 实体扩展不大 |
| 实施复杂度 | ★★★☆☆ — 核心（报告队列 + admin 面板）可增量实施 |
| 与现有代码冲突 | `CommentReport` 已有表（migration 000007），可复用；消息和文件需新增表 |

---

## 方向三：移动端推送基础设施（Push Notification Delivery）

### 为什么需要

当前通知系统架构成熟——`NotificationDispatcher` 支持折叠、限速、免打扰、Block 抑制、SSE 实时推送、Webhook 回调。**但用户如果关闭浏览器，所有通知就断了**。没有 APNS (iOS) / FCM (Android) / 邮件推送。对于任何希望用户在移动端有实时体验的产品，这是致命的短板。代码中 `NotificationPusher` 接口只有 `Publish`（即 SSE），没有注册/注销设备 token 的机制。

### 代码证据

1. `internal/service/notification.go:NotificationPusher` 接口只有 `Publish(uid, eventType, data string, eventID int64)`，是一个纯推送抽象，没有任何注册设备、持久化 token、重试失败推送的能力；
2. `internal/hub/adapter.go` 的 `Publish` 实现直接向 SSE hub 发事件，不走任何外部推送网关；
3. 全局搜索 `fcm`、`apns`、`push_token`、`device` 返回零结果；
4. 没有 `devices` 表迁移——设备注册是无状态的；
5. `configs/config.yaml` 中没有 push 相关配置块。

### 需要解决的问题

- **设备注册端点**：`POST /v1/devices`（传入 push token、平台、locale）
- **Device Repository**：持久化设备 token，支持 uid 级联删除
- **Push Dispatcher**：新的 `outbox.Handler`，在 `NotificationDispatcher` 之后链式调用，负责将通知路由到 FCM/APNS
- **批量推送**：每个用户可能有多个设备，需要去重推送（同一通知不发两次到同一设备）
- **推送撤销**：用户登出时从服务端注销设备 token
- **增量采用**：SSE 仍是实时主力，push 作为离线补充——架构上两者可以共存

### 优先级判断

| 维度 | 评分 |
|---|---|
| 产品价值 | ★★★★★ — 任何移动端应用的基础需求 |
| 架构影响 | ★★☆☆☆ — 新增 Device 实体 + push 适配层，**不修改现有通知逻辑** |
| 实施复杂度 | ★★☆☆☆ — 核心是集成 Firebase/APNS SDK，但架构插入点已由 outbox chain 预留 |
| 与现有代码冲突 | 零冲突——`outbox.NewChain(dispatcher, webhookHandler)` 模式可直接扩展为 `NewChain(dispatcher, pushHandler, webhookHandler)` |

---

## 方向四：SSO 生命周期回调 + 数据主权（GDPR / 数据导出 / 账号级联）

### 为什么需要

系统明确声明 "SSO owns identity"（AGENTS.md），但 SSO 集成是**纯拉模式**：aero-id 调用 SSO 验证 token、批量查用户。**SSO 侧的账号删除、禁用、权限变更，aero-id 永远不会知道。** 用户的数据（文件、消息、评论）在 SSO 账号消失后永远残余。此外，没有任何"导出我的数据"或"删除所有我的数据"的端点——对于 GDPR/CCPA 合规，这是必须的。

### 代码证据

1. `internal/sso/` 只有 gRPC client + 缓存/熔断——没有**入站 webhook** 或 **gRPC server** 接收 SSO 推送的事件；
2. `internal/domain/audit.go:AuditLogger` 的 actions 列表没有 `account.deletion_requested`、`account.export_requested` 等 GDPR 事件；
3. 没有跨所有实体的批量 owner 查询方法（`FindAllByOwner`、`DeleteAllByOwner`、`ExportAllByOwner`）；
4. 没有 `GET /v1/export` 或 `DELETE /v1/account` 端点；
5. 消息、评论、通知没有级联删除逻辑（owner 被删后，其发起的消息/评论如何处理）。

### 需要解决的问题

- **SSO 入站 Webhook**：SSO 回调端点 `POST /v1/sso-events`（接收 `user.deleted`、`user.disabled`、`user.email_changed` 等事件），验证 HMAC 签名后触发级联操作
- **数据导出**：`GET /v1/export` → 后台 job（异步）打包用户所有数据为 JSON/CSV，上传到临时存储并返回下载链接（或邮件发送）
- **账号级联删除**：`DELETE /v1/account` 或 SSO `user.deleted` 事件 → 软删除或匿名化所有关联数据（文件、消息、评论、通知）
- **数据保留策略**：配置化保留期，超过 N 天未激活的账号数据自动清理（可复用 GC sweeper 模式）
- **审计**：所有 GDPR 操作记录到 audit log

### 优先级判断

| 维度 | 评分 |
|---|---|
| 产品价值 | ★★★★☆ — 合规要求，B2B/SaaS 合同通常强制要求 |
| 架构影响 | ★★★☆☆ — 新增 SSO webhook consumer + 导出 job，不影响核心读写路径 |
| 实施复杂度 | ★★★☆☆ — 数据导出涉及跨三个 repository 的联合查询，级联删除需要事务边界设计 |
| 与现有代码冲突 | 低——系统设计已经是"SSO owns identity"；只是补全了缺失的另一半通信 |

---

## 方向五：多区域存储拓扑 + 边缘分发

### 为什么需要

当前 `FileStorage` 接口是单存储后端（local 或 S3）。如果采用 S3，所有文件在同一个 region 的同一个 bucket。用户在地理上分布时，上传/下载延迟差异巨大。系统虽然做了 presigned URL 可以 offload 流量，但没有 CDN 集成、没有 multi-region bucket 路由、没有热/冷存储分层。如果 aero-id 被用于全球用户的文件服务（如社交 app 的图片/附件），这是一个从第一天就要考虑的架构决策。

### 代码证据

1. `internal/storage/storage.go:FileStorage` 接口没有 `Region()`、`SetRegion()` 或任何位置感知方法；
2. `internal/service/file.go:fileService` 的 `storageType` 字段只记录一个字符串（"local"/"s3"），无法表达多区域；
3. `internal/domain/file.go:File` 没有 `StorageRegion` 或 `StorageEndpoint` 字段——storage 信息只有一个扁平的 `StorageType`；
4. `internal/storage/s3.go` 的 `NewS3` 接收单 region/bucket/endpoint 配置，不支持多区域；
5. GC sweeper 操作单 store，没有跨区域协调的概念；
6. 没有 CDN 配置（CloudFront / CloudFlare）或 `PresignGet` 返回 URL 的域名定制。

### 需要解决的问题

- **存储区域路由层**：新的 `regionRouter` 实现 `FileStorage`，根据预配置的区域列表 + 延迟/成本策略 选择合适的物理后端
- **File 扩展**：`File.StorageRegion` 字段记录 blob 的实际区域，确保下载时路由到同一区域（或最近的 CDN PoP）
- **Presigned URL 的 CDN 集成**：为下载 URL 提供自定义域名（CDN CNAME），并支持签名 cookie / 鉴权
- **跨区域复制**：可选地将热门文件复制到多个区域，读取时从最近区域返回；冷文件从源区域读取
- **GC 跨区域**：GC sweeper 遍历所有区域的后端
- **区域存储配置**：`config.yaml` 支持 `storage.regions[].{name, type, region, bucket, endpoint, ...}` 列表

### 优先级判断

| 维度 | 评分 |
|---|---|
| 产品价值 | ★★★☆☆ — 初创期可延后，规模增长后成为瓶颈 |
| 架构影响 | ★★★★☆ — 需要重新设计存储抽象层，影响 file service 的所有读写路径 |
| 实施复杂度 | ★★★★★ — CDN 集成 + 区域路由 + 数据复制 是一个系统工程 |
| 与现有代码冲突 | 低——`FileStorage` 接口的 `PresignGet` 已经返回 URL，替换 backend 即可；但 region 路由层需要对现有存储做封装而非替换 |

---

## 总结：优先级建议

| 方向 | 第一阶段（2-4 周） | 第二阶段（1-2 月） | 长期 |
|---|---|---|---|
| 群聊/会话 | 领域建模 + 表迁移 | 消息模块改造 + 通知适配 | 运营工具 |
| 审核面板 | 报告队列 + 全局隐藏 | Auto-mod 钩子 + 消息举报 | ML 辅助审核 |
| 推送通知 | 设备注册 + 离线投递 | FCM/APNS 集成 | 通知优先级 + 摘要 |
| SSO 生命周期 | 入站 webhook | 数据导出 + 级联删除 | 自动化保留策略 |
| 多区域存储 | 架构设计 + 区域路由 | CDN 集成 | 数据复制 |

**建议从方向三（推送通知）入手**：实现成本最低（outbox chain 已有扩展点）、产品价值最高（移动端必选）、且不会与任何现有逻辑冲突。其次是方向一（群聊）——它是现有消息功能最自然的成长路径。

---

*文档生成方式：全局代码扫描 + 架构自动推断，未使用 LLM 补全或幻觉内容。*
