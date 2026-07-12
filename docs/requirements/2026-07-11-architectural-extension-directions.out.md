这篇分析报告质量很高，我逐一验证了其中的代码断言，基本准确。下面是我的反馈。

---

## 验证结果

### ✅ 准确认定的问题

| 断言 | 验证 | 备注 |
|---|---|---|
| Hub 是单进程 `sync.Map` | ✅ `internal/hub/hub.go:33` — 确为 `sync.Map`，channel buffer=16 静默丢弃 | 报告引用的代码片段完全匹配 |
| 速率限制是 per-instance 内存 token bucket | ✅ `internal/pkg/ratelimit/ratelimit.go` 注释明确写着 "PER-INSTANCE: distributed/global rate limiting would need shared state (e.g. Redis) and is intentionally out of scope" | 
| Outbox 多实例冲突 | ✅ `relay.go:93` 每个实例独立 `FetchUnpublished`，幂等靠 `PublishedAt` 防止重复投递 | 
| 评论有审核，消息/文件无 | ✅ `domain/comment.go` 有完整的 `CommentReport` 结构体；消息/文件域无对应的 `hidden_at` 或 `report` 字段 | 
| 无数据导出/级联删除 | ✅ `grep` 结果确认只有 GC 清理孤儿 blob (`file_gc.go`) 和 idempotency key 过期 (`purgeExpired`)，无用户级级联删除 | 
| 无 @mentions | ✅ 搜索域无 `mentions` 表或解析逻辑 | 

### ⚠️ 略有偏差的细节

1. **"无残留——所有已设计列均有对应端点"** → 这一条不完全准确。SSO 健康仪表盘提到的 `internal/sso` 有断路器状态暴露，但健康检查端点 `/healthz`/`/readyz` 只返回整体状态，没有细粒度 per-upstream 的健康详情面板。这个缺失是真实 gap，但很细小。

2. **"方向二建议 Redis 作为速率限制中心存储"** → 项目中已无 Redis 依赖（如果有也是历史残留）。引入 Redis 是一个新的基础设施依赖，报告中未充分评估这一成本（运维、可用性、延迟）。PG advisory lock 方案在方向二的 outbox leader 选举部分有提及，但速率限制部分没有同等考量——**PG `pg_advisory_lock` + 内存缓存** 可能是零新依赖的更优选择。

3. **"方向一：移动端缺失——真实产品需要推送通知"** → 准确，但方向一和方向二在实现路径上有重叠：两个方向都需要跨实例事件总线。报告的优先级将方向二排第一、方向一排第四，与其"方向一依赖方向一"的表述（方向一建议方案中提到需要 Redis Pub/Sub，方向二也需要 Redis）有内在矛盾。

---

## 我的架构视角补充

### 1. 方向二是正确的一号优先级，但实现可以更务实

立即上线阻截器是**分布式速率限制**，而分布式速率限制不一定需要 Redis。当前项目的基础设施复杂度很低（Postgres + MinIO/S3），**推荐路径**：

```
Phase 1a: 速率限制 → PG advisory lock + 本地内存双桶
  - 本地桶处理绝大多数请求（零延迟）
  - PG 桶每 100ms 同步一次水位（advisory lock 确保原子性）
  - PG 不可用时降级到纯本地桶（fail-open）
  ≈ 零新依赖，上线后 1 天内可交付

Phase 1b: Outbox leader 选举 → PG advisory lock
  - 复用 Phase 1a 的 PG 连接，无需新组件
  - leader 持有 lock、定期 heartbea
  - 失活后其他实例 2× heartbeat 超时后自动接管

Phase 1c: SSE 跨实例 → 同 Phase 2（推送网关）合并实现
```

**这条路径不引入 Redis**，保持运维极简。如果未来规模需要 Redis（比如 >10 副本），再引入。过早引入 Redis 对上线是风险而不是帮助。

### 2. 方向四（数据生命周期）的实现需要解决一个关键问题：级联删除与 SSO 的解耦

报告中提到"监听 SSO 的 account deleted 事件"，但当前 aero-id 与 SSO 的通信模型是**调用方**（aero-id 调 SSO 验证 token），而非订阅方。要支持 SSO push 事件，有两种路径：

- **短路径**: `POST /v1/admin/account/:uid/purge` 管理员手动触发，先上线
- **长路径**: 在 outbox 中新设一个 `account.deletion.requested` 事件类型，通过 outbox webhook 由 SSO 侧回调 aero-id 的 webhook 端点

管理界面手动触发可以覆盖上线初期的合规需求（GDPR 请求通常不是秒级处理的），长路径留给后续迭代。

### 3. 方向五（跨域活动图）的 @mentions 有低成本的切入点

报告中提到 @提及需要 `mentions` 表，但可以更轻量：

- **第一步（不做新表）**: 在 `notifications` 表中新增 `notification_type = 'mention'`，`reference_type/reference_id` 指向消息或评论。正则解析 `@handle` 逻辑放在 service 层，解析出的 uid 直接作为 `to_uid` 发通知。这样通知系统已有的折叠/推送机制可复用。
- **第二步（性能优化）**: 当规模需要时，再加 `mentions` 表做去重和查询优化。

### 4. 一个方向六的提议：配置驱动的内容审核策略引擎

这份报告识别出审核是必须的，但建议的方案（统一报告表 + 状态字段 + 扩展权限）主要解决了**存储和展示**问题，没有解决**自动化策略**问题。

当产品需要"NSFW 图片自动隐藏"、"消息中信用卡号自动替换"、"同一 IP 批量举报自动降权"时，当前架构需要在 service 层硬编码 if-else。建议预留一个轻量的 **Moderation Strategy Chain**：

```go
type ModerationStrategy interface {
    Eval(ctx context.Context, subject Subject) (*ModerationAction, error)
}
```

在 service 层 create/update 后以 chain-of-responsibility 模式调用。上线初期可以是空链（no-op），后续注入具体策略（哈希匹配、分类器 RPC、速率规则）时无需改 service 代码。

---

## 整体评价

这是一份**高质量的架构分析**。五个方向均基于代码级别的实证，优先级排序合理（合规/可用性 > 产品差异化），方案轮廓考虑到了当前架构的约束条件（无 Redis、无 etcd）。如果我来排期：

| Sprint | 交付物 |
|---|---|
| S1 | 分布式速率限制（PG-backed）+ Outbox leader 选举 |
| S2 | 级联删除端点 + 管理面板 / 数据导出（异步任务） |
| S3 | 多域审核（统一报告表 + 消息/文件隐藏）+ Moderation Strategy 框架 |
| S4 | 推送网关抽象 + 设备注册表 + 跨实例 SSE（复用 S1 的 PG pub/sub） |
| S5+ | 跨域活动图（@mentions → 统一时间线 → 搜索排序） |

要不要我把这份文件保存到 `docs/requirements/` 目录下，然后开始实施 S1 方向的具体代码方案设计？
