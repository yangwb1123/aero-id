# aero-id — 全局代码扫描发现的高价值扩展方向

> **日期**: 2026-07-10
> **方法**: 全量源文件扫描（`internal/` 共 ~45 个包、22 份迁移 SQL、`cmd/`、`configs/`、`api/proto/`），与 `docs/requirements/` 下 27 份现有分析文档逐主题交叉比对
> **视角**: 资深架构师 / 产品经理
> **定位**: 本文档仅覆盖那些在 27 份现有分析中**未被系统性触及**的方向。如果某个方向已出现在前述文档中（如群聊、活动流、文件版本、WebSocket、SDK、移动推送、多租户等），本文不再重复。

---

## TL;DR

| # | 方向 | 类型 | 严重性 | 代码证据 |
|---|------|------|--------|----------|
| 1 | SSE 实时推送与数据库查询的毫秒级竞态 | 正确性 / Edge Case | 中高 | `created_at > since` 严格不等式 + 同毫秒通知丢失 |
| 2 | 全文搜索的 ACL 缺口 | 产品 / Edge Case | 高 | 文件/评论搜索仅限 owner，忽略公开和分享 |
| 3 | Outbox 死信队列不可见性 | 运维 / 可观测性 | 中 | 无指标、无告警、无恢复 API |
| 4 | 文件内容安全检测缺失 | 安全 / 合规 | 高 | MIME 源自客户端声明，无实际内容检查 |
| 5 | 双栈代码架构的长期维护债务 | 工程 / 架构 | 高 | 两套独立实现，~11,798 行生产死代码 |

---

## 方向一：SSE 实时推送与数据库查询的毫秒级竞态

### 问题描述

通知系统同时使用 **SSE 实时推送**和 **数据库轮询恢复**（`ListSince`）两种机制，但两者之间存在毫秒级精度竞态，可能导致通知丢失。

### 代码级证据

**证据 1**：`ListSince` 使用 `created_at > since`（严格大于）

```go
// internal/repository/notification.go:106
func (r *notificationRepo) ListSince(ctx context.Context, recipientUID string, since time.Time, limit int) ([]*domain.Notification, error) {
    // ...
    .Where("recipient_uid = ? AND created_at > ?", recipientUID, since)
    //                              ^^^^^^^^^^^^^^^^ 严格大于
```

**证据 2**：Dispatcher 将 `CreatedAt.UnixMilli()` 作为 SSE event ID 发出

```go
// internal/service/notification.go:253
d.pusher.Publish(n.RecipientUID, n.Type, string(payload), n.CreatedAt.UnixMilli())
```

**证据 3**：SSE 客户端断线重连时，将 `Last-Event-ID`（毫秒时间戳）传回给 `replayMissed`，后者调用 `ListSince`

```go
// internal/handler/http/events.go:88
func (h *EventsHandler) replayMissed(c *gin.Context, uid, lastEventID string) {
    millis, _ := strconv.ParseInt(lastEventID, 10, 64)
    since := time.UnixMilli(millis)
    missed, _ := h.fetcher.ListSince(c.Request.Context(), uid, since, 50)
```

**证据 4**：`Create` 方法使用 `time.Now().UTC()`，精度为微秒（Go 的 time.Now 精度），但 SSE ID 截断到毫秒

```go
// internal/notification/dispatcher.go:107
CreatedAt: time.Now().UTC(),
```

### 竞态场景

1. 两条通知在**同一毫秒内**创建（N1, N2），`created_at` 均为 `T ms`
2. Dispatcher 发送 N1 的 SSE 事件，event ID = `T`
3. 客户端收到 N1，断线（N2 尚未推送或尚未到达）
4. 客户端重连，发送 `Last-Event-ID: T`
5. `ListSince` 查询 `created_at > T` → **N2 被排除**（因为 N2.created_at == T，不满足 `>`）
6. N2 永久丢失（除非客户端发起完整的全量刷新）

### 影响范围

- 在通知产生速率 > 1000/秒的场景下触发概率显著上升
- 被丢弃的通知是不可恢复的：不存在客户端侧的序列号检测机制
- SSE hub 的 channel buffer 为 16，当推送速率超过消费速率时也存在静默丢弃风险

### 建议

- 将 SSE event ID 改为**纳秒精度**的 `created_at` 值（`UnixNano()` 或带计数器的全局序列号）
- 将 `ListSince` 的 `>` 改为 `>=`，代价是重连后最后一条通知可能重复推送，但客户端可幂等处理
- 或在 SSE event payload 中加入单调递增的事件序号，使客户端能检测到丢失并触发修复性全量拉取

---

## 方向二：全文搜索的 ACL 缺口

### 问题描述

搜索接口仅返回用户**自己拥有**的文件和评论，忽略用户有**读权限**但非 owner 的资源（公开文件、被分享的文件、对用户消息/文件的评论）。

### 代码级证据

**证据 1**：文件搜索仅限 owner

```go
// internal/repository/file.go:224
SELECT ... FROM files
WHERE deleted_at IS NULL
  AND owner_uid = ?        -- 仅限 owner
  AND search_vector @@ plainto_tsquery('simple', ?)
```

**证据 2**：搜索服务将 `actor` 作为 ownerUID 传入

```go
// internal/service/search.go:110
files, err = s.files.SearchFiles(searchCtx, actor, query, limit)
```

**证据 3**：评论搜索同样仅限 owner

```go
// internal/repository/comment.go:366
func (r *commentRepo) SearchComments(ctx context.Context, ownerUID, query string, limit int) {
    // ...
    WHERE deleted_at IS NULL
      AND owner_uid = ?
```

**证据 4**：文件分享表（`file_shares`）已存在，但搜索未使用

```sql
-- migrations/000006_create_file_shares.up.sql
CREATE TABLE file_shares (
  file_id    UUID NOT NULL REFERENCES files(id),
  grantee_uid TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL,
  PRIMARY KEY (file_id, grantee_uid)
);
```

### 具体用例

| 场景 | 当前行为 | 期望行为 |
|------|---------|---------|
| 用户 A 搜索公开文件 | 仅返回 A 自己的文件 | 应返回所有可见公开文件 |
| 用户 B 被分享了一个文件 | 搜索不到该文件 | 应能搜索到被分享的文件 |
| 用户 C 评论了用户 A 的文件 | A 搜索不到 C 的评论 | 应能搜索到自己文件上的评论 |

### 建议

- 文件搜索增加：`owner_uid = ? OR visibility = 1 OR file_id IN (SELECT file_id FROM file_shares WHERE grantee_uid = ?)`
- 评论搜索增加：`owner_uid = ? OR (target_type = 'file' AND target_id IN (SELECT id FROM files WHERE visibility = 1 OR owner_uid = ? OR ...))`
- 注意索引设计，避免全表扫描

---

## 方向三：Outbox 死信队列不可见性

### 问题描述

当 outbox 事件重试超过最大次数时，被标记为 `dead_letter = true` 后即永久沉默——**无指标暴露、无告警通知、无运维恢复机制**。这意味着后台处理管道可能在无人知晓的情况下停止工作。

### 代码级证据

**证据 1**：死信标记逻辑

```go
// internal/outbox/repository.go:100-108
func (r *GormRepository) MarkFailed(..., retryCount, maxRetries int, at time.Time) error {
    updates := map[string]any{"retry_count": retryCount, "last_retry_at": at}
    if retryCount >= maxRetries {
        updates["dead_letter"] = true
    }
    return r.db.Model(&outboxRow{}).Where("id = ?", id).Updates(updates).Error
}
```

**证据 2**：Relay 跳过死信事件

```go
// internal/outbox/repository.go:73
.Where("published_at IS NULL AND NOT dead_letter").
```

**证据 3**：无 Prometheus 计数器跟踪死信事件。`internal/pkg/metrics/metrics.go` 中 outbox 相关的指标只有：

```go
// published/failed counters, pending-count gauge, oldest-event-age gauge, drain-duration histogram
// 但没有任何 dead_letter 计数器
```

**证据 4**：无运维 API 查看或重试死信事件。HTTP 和 gRPC 均未暴露死信查询/重试端点。

### 影响范围

- 某次事件因 transient error 到达最大重试次数后，其对应的通知、回调或副作用永久丢失
- 运维人员只能在数据库层面手动检查：`SELECT * FROM outbox_events WHERE dead_letter = true`
- 生产环境中如果 webhook endpoint 变更/下线，所有 webhook 事件都会进入死信而无人知晓

### 建议

- **指标暴露**：增加 `outbox_dead_letter_total` counter，在 `MarkFailed` 中当 `retryCount >= maxRetries` 时递增
- **告警**：当 `rate(outbox_dead_letter_total[5m]) > 0` 时触发告警
- **恢复 API**：增加运维端点（带 admin 权限）：
  - `GET /v1/admin/outbox/dead-letters` — 分页列出死信事件
  - `POST /v1/admin/outbox/dead-letters/:id/retry` — 将指定事件的重置 `dead_letter = false, retry_count = 0`
- **Webhook 通知钩子**：当事件被标记为死信时，可选择性地向 ops webhook 发送通知

---

## 方向四：文件内容安全检测缺失

### 问题描述

文件上传的 MIME 类型校验完全依赖客户端声明的 `Content-Type` 头，服务端不验证实际文件内容。攻击者可声明 `image/png` 但实际上传可执行脚本或恶意内容，绕过安全控制。

### 代码级证据

**证据 1**：MIME 类型全部来自客户端

```go
// internal/file/service_crud.go:32-33 — 仅检查客户端声明的 MIME
if !s.mimeAllowed(in.MimeType) {
    return nil, apperr.BadRequest("file.unsupported_type", "file content type is not allowed")
}

// internal/file/file.go:142-148 — mimeAllowed 解析客户端传来的 Content-Type
func (s *fileService) mimeAllowed(contentType string) bool {
    if len(s.allowedMimes) == 0 {
        return true
    }
    mt, _, err := mime.ParseMediaType(contentType)
    if err != nil {
        return false
    }
    // ...
}
```

**证据 2**：HTTP handler 将上传的 Content-Type 直接作为 MIME 类型

```go
// internal/file/handler_http.go:73 — 客户端的 Content-Type 被直接信任
MimeType: fh.Header.Get("Content-Type"),
```

**证据 3**：文件 checksum 已计算（`files.checksum` 列已在 `000001_create_files` 中创建，service_crud.go 中 `sha256` 计算已实现），但仅用于 dedup，未用于安全检测。

**证据 4**：`storage.PresignPut` 直传路径（S3 预签名上传）更是完全跳过了 aero-id 进程，没有任何中间件层级的检查——客户端直接上传任意内容到 S3，然后调用 `CompleteUpload` 完成。

### 风险分析

| 风险 | 严重性 | 说明 |
|------|--------|------|
| 恶意软件上传 | 🔴 高 | 用户上传声称是文档/图片的文件，实际包含可执行代码 |
| 存储成本攻击 | 🟡 中 | 上传大量不匹配 MIME 声明的垃圾数据填充存储 |
| 合规违反 | 🔴 高 | 涉及 CSAM、版权内容等时，无检测能力意味着无合规防线 |
| 镜像/文档站点投毒 | 🟡 中 | 文件经 aero-id 分发，恶意内容可被下游系统误信 |

### 建议

**最低成本方案**（立即可行）：
- 使用 `net/http` 的 `DetectContentType`（基于前 512 字节魔数）验证实际 MIME 与声明 MIME 一致
- Go 标准库已内置 `mime.TypeByExtension` 和 `http.DetectContentType`

**中等方案**（推荐）：
- 引入 `io.LimitReader` 包装上传流，边读边做 content sniffing，拒绝 MIME 不匹配的请求
- 在 `CompleteUpload` 路径中增加异步内容校验

**高成本方案**（安全合规）：
- 集成第三方安全扫描器（ClamAV 防病毒、自定义 DLP 规则引擎）
- 通过 outbox 事件驱动文件安全扫描管线：文件上传 → outbox event → worker 扫描 → 更新 `file.meta` 中的扫描结果
- 对扫描结果设置可见性规则：未通过扫描的文件自动标为 `visibility = 0`（私密）或标记为不可下载

---

## 方向五：双栈代码架构的长期维护债务

### 问题描述

代码库中存在**两套完整的 handler→service→repository 堆栈**，但只有其中一套被 `app.go` 使用。另一套 `internal/{file,message,comment,notification,block,search,audit}/` 下的 ~11,798 行生产代码处于"无人认领"状态——不被任何生产代码导入，没有团队负责保持其同步，测试通过但并不覆盖生产路径。

### 代码级证据

**证据 1**：生产使用的旧栈

```
internal/
├── handler/
│   ├── http/{file,message,comment,notification,block,search,admin,preference}.go
│   └── grpc/{file,message,comment,notification,block,search,preference}.go
├── service/{file,message,comment,notification,block,search,preference}.go  (+ mocks)
├── repository/{file,message,comment,notification,block,file_share,admin,preference,audit,storage_ref}.go
```

这些都通过 `internal/app/app.go` 中的引用来实例化。

**证据 2**：未被使用的 new stack

```
internal/
├── file/       {service_crud.go, service_share.go, service_upload.go, repository.go, repository_share.go, handler_http.go, handler_grpc.go, file.go}
├── message/    {service.go, repository.go, handler_http.go, handler_grpc.go, message.go}
├── comment/    {service.go, repository.go, handler_http.go, handler_grpc.go, comment.go}
├── notification/{service.go, repository.go, handler_http.go, handler_grpc.go, dispatcher.go, notification.go}
├── block/      {block.go, repository.go, handler_http.go, handler_grpc.go}
├── search/     {search.go, handler_http.go, handler_grpc.go}
├── audit/      {repository.go}
```

**证据 3**：`app.go` 仅导入旧栈，完全不引用 new stack 的任何包

```go
// internal/app/app.go — 所有 import 均指向 internal/service/ 和 internal/repository/
import (
    "github.com/aero/aero-id/internal/service"
    "github.com/aero/aero-id/internal/repository"
    // ... 没有 internal/file/, internal/message/, internal/comment/ 等
)
```

**证据 4**：两套实现之间存在微小但关键的语义差异

| 特性 | 旧栈（生产） | 新栈（未使用） |
|------|-------------|---------------|
| 搜索 CJK 回退 | 有（`containsCJK` → `SearchMessagesLike`） | 无（始终用 tsquery） |
| 多实体搜索 timeout | 每 kind 5s context timeout | 无 timeout |
| 搜索 query 长度限制 | 200 rune max | 无限制 |
| 文件搜索 ACL | 仅 owner | 仅 owner（与旧栈一致，但同样有 #2 的问题） |

### 影响

| 影响 | 严重性 | 说明 |
|------|--------|------|
| **Bug 修复需两处修改** | 高 | 一个 bug 在旧栈修复后，如果未同步到新栈，未来迁移时 bug 复现 |
| **测试覆盖假象** | 中 | CI 显示所有测试通过，但新栈测试不覆盖生产代码路径 |
| **误导入风险** | 高 | 开发者可能误导入 `internal/file` 而非 `internal/service`，产生编译期无法发现的引用问题 |
| **重构阻力** | 高 | 两套代码使任何跨域重构的工作量翻倍 |
| **架构文档不匹配** | 中 | 查看代码的开发者看到整洁的 `internal/{comment,file,message}` 结构会误以为这是实际架构 |

### 建议

**短期**（立即行动）：
- 在 `internal/{file,message,comment,notification,block,search,audit}/` 的每个包根目录添加注释：
  ```go
  // Deprecated: this is an archived refactor attempt. The production entrypoint
  // (internal/app/app.go) uses internal/service/ and internal/repository/ instead.
  // Do not add new code here. See AGENTS.md.
  ```
- 在 `AGENTS.md` 和 `ARCHITECTURE.md` 中记录此决策

**中期**（增量迁移，推荐）：
- 制定逐步切换计划：每次将一个域从旧栈迁移到新栈（例如先切 file，验证后切 comment……）
- 每切完一个域运行 `make test && make test-integration`
- 确保新栈完整覆盖旧栈的所有功能和 edge case 处理（如上表的 CJK 回退、timeout、query 长度限制）
- 迁移完成后删除旧栈代码

**不推荐**（除非人力充足）：
- 一次性重写切换——风险过高，且旧栈经过生产验证
- 永久维持双栈——维护成本持续累积，技术债利息越来越高

---

## 总结

五个方向按实施优先级排序：

| 优先级 | 方向 | 预估投入 | 前提条件 |
|--------|------|---------|---------|
| P0 | 方向一：SSE 竞态修复 | 低（纳秒精度 + ListSince 改 >=） | 无 |
| P0 | 方向二：搜索 ACL 修复 | 中（SQL + 索引） | 无 |
| P1 | 方向四：MIME 内容验证 | 低（`http.DetectContentType`） | 无 |
| P1 | 方向三：死信可观测性 | 中（指标 + API 端点） | 已有 admin 角色系统 |
| P2 | 方向五：双栈合并 | 高（涉及全量 domain 迁移） | 需团队决策新栈是否为目标架构 |

前三个方向（P0）无需架构决策即可开始实施，总共预估投入不超过 3-5 天。
