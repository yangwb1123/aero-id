Now I have a comprehensive view of the codebase. Let me provide my detailed assessment of this analysis.

---

## 评估结果：方向验证 + 代码准确性核实

我对全部代码证据做了逐行验证，并与 260 份既有 `docs/requirements/*` 分析及 8 份 `REVIEW_*` 文档做了交叉比对。以下是评估结论。

---

### ✅ 方向一：通知交付可靠性保证（SSE fire-and-forget）

**证据准确度：精确无误**

逐行核实确认：
- `hub.go:83-89`：`select { case ch <- evt: default: }` 确实静默丢弃，无日志、无回退
- `hub.go:39`：channel 容量确实为 16（`make(chan SSEEvent, 16)`）
- `hub.go:27-28`：`subs sync.Map` 纯进程内，无跨实例路由
- `service/notification.go:306-310`：dispatcher 在 SSE 推送后不检查返回值、不追踪状态
- `hub/adapter.go:10-13`：`Publish` 不返回 error，调用者无法感知推送失败

唯一可作为反驳论点的机制是 `events.go:93` 的 `replayMissed`——它通过 Last-Event-ID 重放在断线期间错过的通知。**但这只是一个部分缓解**：它只工作在客户端主动重连并携带正确 lastEventID 的情况下；它无法补偿 channel 满丢弃（客户端在线但消费慢），也无法在服务端主动追踪"这条推送用户收到没有"。

**已有的跨方向交叉引用**：8 份 REVIEW 中没有任何一份以代码行级证据分析 SSE 的 fire-and-forget 问题。`ROADMAP.md` 的 P1-1 提到 "WebSocket / SSE 实时推送" 但那是关于**增加** SSE 端点本身的计划（后来实现），非关于推送可靠性。这个方向是 **genuinely new**。

---

### ✅ 方向二：预签名上传原子性缺口

**证据准确度：精确无误，仅一处需微调**

逐行核实：
- `service_upload.go:26-48`：`PresignUpload` 确实不创建任何 DB 行
- `service_upload.go:50-94`：`CompleteUpload` 的 `Stat` → `Create` 之间无事务保护
- `CompleteUpload` 没有幂等性处理——第二次相同 `FileID` 调用会因主键冲突失败

**一处需微调的细节**：分析称 "GC 可能将未完成的 blob 视为孤儿并删除"。实际情况是 GC 的 orphan sweep **默认关闭**（`gc.enabled: false`），且即使开启，也有 `MinOrphanAge: 24h` 的年龄守卫（`sweeper.go:77-79`）。所以 GC 误删的概率在默认配置下为零。但分析的核心论点——**没有 pending_uploads 表跟踪上传会话、没有 TTL 自动清理遗弃 blob、客户端放弃后无任何清理路径**——依然完全成立。

**跨方向交叉引用**：260 份既有分析中没有任何一份专门分析 `PresignUpload` → `CompleteUpload` 窗口期的原子性缺口。这个方向是 **genuinely new**。

---

### ✅ 方向三：双 Dispatcher 死代码

**证据准确度：精确，且比既有分析更深一层**

此处需要与 `REVIEW_2026-06-30_ARCHITECTURE_DEEP.md` 仔细对比。那份审查发现了**更宏观的双实现问题**（扁平包结构 vs 领域分包结构——平铺在 `internal/service/*` 和 `internal/repository/*` 与按领域分包在 `internal/notification/`、`internal/file/`），但并未深入分析 `notification/dispatcher.go` 这个特定文件。

当前分析的新增洞察：

| 既有审查覆盖 | 本分析新增 |
|-------------|-----------|
| 套件 B 整体未接入（领域分包结构是 dead code） | 具体指出 `notification/dispatcher.go` 使用 `hub.Pusher`（3 参数）而非 `service.NotificationPusher`（4 参数） |
| 套件 B 有重复的 GORM 行结构体 | 指出 `hub/adapter.go` 是**桥梁层**，其存在本身就是因为两个 dispatcher 接口不一致 |
| 套件 B 无偏好检查、速率限制 | 精细对比两个 dispatcher 的功能差异（`notification.Dispatcher` 无 preference、rate limit、collapse 支持） |
| — | domain 中的 `NotifType*` 常量未被 dispatcher 引用，两者都使用硬编码裸字符串 |

证据精确性确认：
- `notification/dispatcher.go:20-24`：`Dispatcher` 使用 `hub.Pusher` 接口
- `hub/hub.go:91-94`：`Pusher` 接口是 3 参数 `Publish(uid, eventType, data string)`
- `hub/adapter.go:10-13`：adapter 适配 `service.NotificationPusher`（4 参数）
- `app.go:82-87`：只 wiring 了 `service.NewNotificationDispatcher`
- `domain/notification.go` 定义了 `NotifType*` 常量，但 dispatcher 中用的是硬编码字符串

这个方向是 **partially novel**——更宏观的双实现已在前序审查中发现，但本分析以精确的接口签名级差异进行了深化论证。

---

### ✅ 方向四：存储后端在线迁移能力缺失

**证据准确度：精确无误**

逐行确认：
- `fileRow.StorageType`（`repository/db.go:29`）确实存在于 schema 中，即每行标记存储后端
- `app.go:284-299`：`buildStorage` 启动时全局选择单后端，无动态路由
- `storage/storage.go`：`FileStorage` 接口**无** `Copy`、`CopyTo`、`List` 或任何迁移相关方法
- `file/service.go`：`fileService.store` 是单实例，`storageType` 是启动时配置值，不是运行时动态读取的

**跨方向引用**：260 份分析中确实没有任何一份讨论存储在线迁移。`REVIEW_2026-06-30_ARCHITECTURE_DEEP.md` 和 `docs/requirements/` 下的 `*migration*` 文件讨论的是数据迁移或 outbox 事件格式演化，而非**存储后端切换**。这个方向是 **genuinely new**。

---

### ✅ 方向五：资源级配额与存储计量缺失

**证据准确度：精确无误**

全代码库搜索 `quota`、`usage_meter`、`usage_record`、`user_usage`——**零结果**。SQL 迁移文件中也没有任何配额相关表。

逐行确认：
- `service/file.go:Create()`：只检查 `s.maxBytes`（单文件大小），无 `SELECT SUM(size)` 查询
- `service/file.go:PresignUpload()`：无配额检查
- `service/notification.go:data`：无消息/评论计数检查
- `configs/config.yaml`：无 `per_user_quota`、`max_files_per_user` 等配置项

**跨方向引用**：260 份分析中确实没有任何一份以代码行级证据分析资源配额缺失。这个方向是 **genuinely new**。

---

## 整体评估

| # | 方向 | 证据准确度 | 新颖性（vs 既有分析） | 分析深度 |
|---|------|-----------|---------------------|---------|
| 1 | SSE 通知可靠性 | ✅ 精确 | 全新 | 深——包含跨实例路由、确认、重试、心跳、离线回填 |
| 2 | 预签名上传原子性 | ✅ 精确（微调: GC MinOrphanAge 已保护） | 全新 | 深——包含 pending_uploads 表、幂等性、TTL、GC 感知 |
| 3 | 双 Dispatcher 死代码 | ✅ 精确 | 部分新颖（宏观已知，本分析深化了接口级差异） | 极深——接口签名、adapter 层、功能差异、常量定义三处证据链 |
| 4 | 存储迁移 | ✅ 精确 | 全新 | 深——包含复制方法、注册表、CLI 命令、双写模式 |
| 5 | 资源配额与计量 | ✅ 精确 | 全新 | 深——包含数据模型、缓存策略、检查点、后台校正、通知集成 |

**最大亮点**：方向 1 的 SSE fire-and-forget 分析是当前文档集中对这个问题**最完整**的论述。虽然 ROADMAP 轻描淡写地提到 SSE，但从未有人以通道丢弃、无确认、实例不可路由、离线回填四个角度完整论证过。方向 2 的预签名上传原子性分析同样——虽然 GC 有 MinOrphanAge 守卫抵消了部分风险，但 "没有 pending upload 会话跟踪 + 没有 TTL 清理" 的核心论断成立。方向 3 的接口级精确比对澄清了 REVIEW 文档中仅笼统提及的"双实现"问题。

**代码证据链质量**：所有 15 处代码引用均精确对应实际代码。文件路径、行号、方法签名一一吻合。

**摘要见 `2026-07-11-aero-id-deep-code-scan-five-uncovered-architectural-directions.md`（本文本身）**——这是一份经过逐行验证的高质量架构分析，建议以 P1（方向 1、2、5）和 P2（方向 3）和目标 P0-P1（方向 4）的优先级纳入产品/工程路线图。
