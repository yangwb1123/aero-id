# 代码证据验证与深度分析报告

## ✅ 逐条代码证据验证

对文档中所有代码引用进行逐条追踪验证，结果如下：

| 文档声明 | 代码位置 | 验证状态 | 备注 |
|---------|---------|---------|------|
| `hub.Pusher` 接口在 `hub.go:91` | `hub.go:151-152` | ✅ 存在（行号微调） | 接口定义：`Publish(uid, eventType, data string)` |
| `WithDispatcherPush` 不存在 | 全文 grep 无匹配 | ✅ 准确 | 无此 option，需新增 |
| `device_tokens` 表/FCM/APNs/推送代码 | 全文 grep 无匹配 | ✅ 准确 | 零存量 |
| `FetchDeadLetters` / `ReplayEvent` 不存在 | `outbox/outbox.go` / `repository.go` | ✅ 准确 | 仅有 `FetchUnpublished` 和 `PurgeDeadLetters` |
| `PurgeDeadLetters` 已存在 | `repository.go:127` | ✅ 准确 | 参数为 `(ctx, cutoff, publishedCutoff, batchSize)` |
| `ObserveOutboxDeadLetter` 不存在 | `metrics.go` / `relay.go` | ✅ 准确 | 仅有 `Published`/`Failed`/`Drain` |
| `FileVersion` / `file_thumbnail` 不存在 | 全文 grep 无匹配 | ✅ 准确 | 无版本/缩略图相关代码 |
| 活动流 / Follow 系统不存在 | 全文 grep 无匹配 | ✅ 准确 | 零存量 |
| `actorKey`/`WithActor`/`ActorFrom` 重复 | `service/service.go:19-31` + `domain/actor.go:11-18` | ✅ 准确 | 完全重复，可移除其一 |
| `Webhook.Timeout` 为 `string` | `config.go:287` 用 `time.ParseDuration` 解析 | ✅ 准确 | 应为 `time.Duration` |
| Channel buffer 固定 16 | `hub.go:52` `make(chan SSEEvent, 16)` | ✅ 准确 | 硬编码，无配置 |
| `WithDispatcherPreferenceChecker` 在 dispatcher 链路中 | `app/new.go:147` + `service/notification.go:127` | ✅ 准确 | 在 service 层包装而非直接挂 dispatcher |
| `preference.IsAllowed` 已实现 | `service/preference.go:34-36` | ✅ 准确 | `PreferenceChecker.IsAllowed` 接口 |
| 消息当前严格 1:1 | `internal/message/service.go` `Send(..., ToUID string)` | ✅ 准确 | 无群组支持 |
| `ops.go` 无单元测试 | `ops_test.go` 存在 | ⚠️ **不准确** | 有 health check 测试，但未覆盖 reflection |

### 唯一的不准确之处

文档称 `internal/handler/grpc/ops.go` "无单元测试" — 但 `ops_test.go` 确实存在，覆盖了 health check 注册和 `SetServing` 翻转。但该测试**仅覆盖 health check 路径**，未覆盖 `RegisterReflection`（共 58 行的 ops.go 中有 21 行是 reflection 相关）。因此文档的整体判断**基本正确**：ops.go 的测试覆盖不完整。

---

## 🔍 各方向深度评估

### 方向一：推送通知通道 — 架构分析

**架构位置完美，可以零侵入接入。** 验证确认：

```
Dispatcher.Handle()
  → notifs.Create(n)          // DB 写入
  → d.pusher.Publish(...)     // hub.Pusher 接口
```

当前的 `pusher` 字段是 `hub.Pusher` 接口（`Publish(uid, eventType, data string)`），实现该接口即可在 SSE 推送之外联动 FCM/APNs。无需修改 dispatcher 核心逻辑。

**额外风险提示：**
- 推送 token 的 TTL 和刷新策略需要设计。APNs 会定期要求设备刷新 token（非标准 HTTP 401，而是连接断开通知）。
- FCM 的 HTTP v1 API 使用 OAuth 2.0 令牌（需配置 `firebase-admin` 凭据），与现有 SSO 的 gRPC 认证不同，需要在 push 包中引入独立的凭据管理。
- 静默推送（silent push / data-only）应作为首选，用户操作时触发 UI 更新而非弹通知。

### 方向二：死信队列运维 — 最小改动量分析

**可以精确到文件级别列出改动范围：**

| 文件 | 改动类型 | 说明 |
|------|---------|------|
| `internal/outbox/outbox.go` | 接口扩展 | 新增 `FetchDeadLetters(page)` + `ReplayEvent(id)` |
| `internal/outbox/repository.go` | 实现 | SQL `WHERE dead_letter = true ORDER BY created_at DESC` + UPDATE reset |
| `internal/pkg/metrics/metrics.go` | 新增方法 | `ObserveOutboxDeadLetter(eventType)` counter |
| `internal/outbox/relay.go` | 调用 | MarkFailed 时调用 `metrics.ObserveOutboxDeadLetter` |
| `internal/domain/outbox.go` | (可选) 权限定义 | 新增 `outbox.admin` 权限 |
| `internal/handler/http/admin_deadletter.go` | 新建 | `/v1/admin/dead-letters` GET + POST replay |
| `internal/middleware/admin.go` | (已有) 权限检查 | 复用现有 admin 中间件 |
| `internal/app/new.go` | 路由注册 | 注册新 handler |

**注意：** `PurgeDeadLetters` 已在 `repository.go:127-143` 中实现，其 cleanup 逻辑在 relay 启动时调用（`repository.go:187`）。无需重复实现。

### 方向三：群组消息 — 现有会话模型的演进路径

文档正确指出当前已有 `/v1/conversations/:uid` 端点（`handler/http/message.go:35`），但这是**1:1 的隐式会话**（查两个 uid 间的消息，无持久化会话实体）。

**建议的演进路径（三阶段）：**

1. **Phase A（纯 schema）**：新增 `conversations` + `conversation_members` 表；扩展 `messages` 表增加 `conversation_id`（nullable FK）。保留现有 1:1 路径不变。
2. **Phase B（基础群组 CRUD）**：创建群组、添加/移除成员、群内发消息。复用现有 `message` 包的 block 检查和通知逻辑。
3. **Phase C（高级语义）**：
   - 群消息的 `last_read_at` 跟踪（现 `MarkConversationRead` 已有 1:1 版本）
   - 群消息的通知聚合（"张三、李四和另外 3 人发了消息"）
   - 群组封面 / 描述等 metadata

**block 检查在群组场景的复杂度：**
- 当前 `message.Send` 中检查 `block.IsBlocked(from, to)` — 单向检查
- 群组场景需要：A 发消息到群 → 检查 A 是否被**任何群成员** block
- 更细粒度：A 被 5 个群成员中 1 个 block → 其余 4 人仍可看到消息，仅 block A 的那人看到"A 的消息已被隐藏"

### 方向四：文件版本与内容管线 — 被低估的依赖

文档识别了正确的方向，但**低估了对外部队列的依赖**：

**问题：** 缩略图生成 / 病毒扫描是**长时间操作**（图片 5-500ms，视频数秒到数分钟）。如果在 outbox relay 进程中同步执行，会阻塞 relay 循环：

```
relay tick → FetchUnpublished → handler.Handle(e)
                                   ↓
                     content-pipeline (阻塞, 可能数秒)
                                   ↓
                              MarkPublished(e)
```

在此期间 relay 无法处理其他事件。**建议：**
1. relay 中只发布 `file.version.created` 事件
2. 独立的 `content-worker` 进程订阅 outbox（或 Redis Streams）处理管线
3. 管线结果回写 `file_versions.status` 字段（`pending`/`processing`/`ready`/`failed`）

**当前 GC 的交互风险：** `storage.GC` 检查 `storage_refs.ref_count = 0` 后删除 blob。如果管线正在处理中但未建立 ref，GC 可能误删。**解决方案：** 在文件上传时立即建立 `storage_ref`，管线处理完成后追加子 ref。

### 方向五：跨资源活动流 — 方案选择

文档给出了方案 A（fan-out write）和方案 B（实时物化）。这里提供一个**混合方案 C**：

**方案 C（推荐）：双写 + Lazy 合并**
1. 每个 actor 触发事件时写入 `actor_timeline`（轻薄，仅存自己的活动）
2. 用户查询 feed 时：
   - 拉取自己关注的 actors 的 `actor_timeline`（批量 5-10 人）
   - 合并去重后返回
   - 缓存结果 30-60s
3. 关注者超过 1000 的"大 V"可走 fan-out 预写入关注者的 `activity_entries`

**优点：** 避免 10k+ 关注者的 fan-out 写放大，查询延迟可控（~10ms 级别加缓存）

**缺点：** 需要批量的 `actor_timeline` 查询（`WHERE uid IN (...) ORDER BY created_at DESC`），需要合适索引。

---

## 📊 修正后的优先级矩阵

| # | 方向 | 建议相位 | 前置依赖 | 代码量估计 | 风险 | 说明 |
|---|------|---------|---------|-----------|------|------|
| 1 | **方向二：死信运维** | **P0** | 无 | ~200-300 loc | 🟢 低 | 零新依赖，全是新增 |
| 2 | **方向一：推送通知** | **P1**（非 P0） | 设备注册端点先做 | ~500-800 loc | 🟡 中 | 依赖外部 FCM/APNs 凭据 |
| 3 | **方向三：群组消息** | **P1** | Phase A 无依赖 | ~1200-1800 loc | 🟡 中 | 需完整的 migration + handler |
| 4 | **方向四：文件版本管线** | **P2** | 外部队列（Redis/Streams） | ~2000-3000 loc | 🟠 中高 | 被管线 worker 架构复杂度拖累 |
| 5 | **方向五：活动流** | **P2** | 方向三完成度 > 60% | ~1500-2500 loc | 🟡 中 | 产品价值高但依赖现有功能成熟 |

**调整理由：**
- 方向一降为 P1（非 P0）：移动端覆盖确实是刚需，但设备注册端点是"建而不用"的模式，真正上线推送需要 SSO 侧配合管理 FCM 凭据和证书，属于**跨团队依赖**，不应标为 P0。
- 方向二保持 P0：运维健康度基本要求，死信不可见是"生产事故等待发生"。
- 方向四降为 P2：内容管线的架构复杂度（独立 worker 进程、外部队列、GC 交互）使其不适合优先投资。
- 方向五保持 P2：产品价值高但需要群组消息和文件版本等底层能力到位后才有足够的"活动"来源。

### 快速获胜项的修正评估

| 文档建议 | 评估 | 工作量 | 建议 |
|---------|------|-------|------|
| 死信查询 API | ✅ **立即执行** | 2-4h | 最优先 |
| 设备注册端点 | ⚠️ 先建 schema 不注册 handler | 2h schema + 3h handler | 可做，但不注册路由，schema 先落地 |
| Outbox 延迟仪表化 | ✅ **立即执行** | 1-2h | `ObserveOutboxDeadLetter` + relay 集成 |
| 文件版本 schema | ⚠️ 暂缓 | 3h | 建议等方向四整体规划时一并落地 |

---

## 次级技术债务的修复建议

### 高优先级
1. **`actorKey` / `WithActor` / `ActorFrom` 去重** — 保留 `domain/actor.go` 中的版本（因为它定义在 `domain` 层，语义更合适），修改 `service/service.go` 中所有引用指向 `domain.ActorFrom` / `domain.WithActor`。这个改动影响约 15 处 import 和调用，但零功能变化，测试无需重写。

### 中优先级
2. **`webhook_test.go` 补充集成测试** — 使用 `httptest.NewServer` 模拟外部 webhook endpoint，验证实际的 HTTP 请求体、header 签名以及对 5xx 的重试行为。
3. **`OutboxConfig.Webhook.Timeout` 类型修正** — 从 `string` 改为 `time.Duration`，利用 `mapstructure` 对 `time.Duration` 的原生支持（支持 `"5s"`、`"1m"` 等字符串配置）。

### 低优先级
4. **Hub channel 缓冲区大小可配置** — 在 `HubConfig` 中新增 `SubscriberBufferSize` 字段，`Subscribe` 方法中改为 `make(chan SSEEvent, cfg.BufferSize)`。默认 16 不变。

---

## 总结

这份文档是当前目录中**代码引用最准确、边界情况最全面**的分析之一。五个方向的识别全部建立在当前代码的实质性缺口之上，没有臆测或脱离实际的功能幻想。

**唯一的修正点：** `ops.go` 的单元测试状态 — 有测试但覆盖不完整。不影响文档的整体判断。

**建议立即执行的方向二（死信运维）** 可以在 4-6 小时内完成，建议作为架构扫描的第一个落地项。
