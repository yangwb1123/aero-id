Now let me write the final code implementation report.

---

# 代码实现报告

## 实现概述

基于架构设计文档和实现计划，对 **五个通知生命周期与领域模型缺口** 进行了完整的代码实现。本次实现覆盖了原有 working tree 中已完成的代码变更，并补充了文件分类（方向四）和通知偏好作用域（方向五）中缺失的部分。

遵循项目工程约束：所有文件 ≤ 500 行，函数 ≤ 50 行，严格分层依赖。

## 文件清单

### 新增文件
- `migrations/000026_add_file_category.up.sql` - 文件类型分类迁移（新增 category 列）
- `migrations/000026_add_file_category.down.sql` - 文件分类回滚迁移
- `migrations/000027_add_preference_scope.up.sql` - 通知偏好作用域迁移（scope_type/scope_id）
- `migrations/000027_add_preference_scope.down.sql` - 偏好作用域回滚迁移

### 修改文件
- `internal/domain/shared.go` - Page 结构体新增 Category 可选过滤字段
- `internal/repository/db.go` - fileRow struct 新增 Category 字段，更新映射函数
- `internal/repository/file.go` - ListByOwner 支持按 category 过滤
- `internal/repository/preference.go` - PreferenceRepository 接口新增 GetScoped 方法；存储层支持 scope 字段
- `internal/service/file.go` - Create 方法自动从 MimeType 推导 Category
- `internal/service/file_presign.go` - CompleteUpload 方法自动推导 Category
- `internal/service/preference.go` - PreferenceChecker 接口扩展支持 ScopeRef；新增 checkQuietHours 辅助函数
- `internal/service/mocks/mocks.go` - PreferenceRepo mock 新增 GetScopedFn
- `internal/pkg/httpx/httpx.go` - PageFromQuery 支持 `?category=` 参数解析

## 核心代码实现

### 方向四：文件类型分类

**文件上传时自动分类**：在 `fileService.Create` 和 `CompleteUpload` 方法中，上传时根据 MIME 类型自动推导 `FileCategory`：

```go
// internal/service/file.go:144
Category: domain.ClassifyFile(in.MimeType),
```

**MIME→Category 映射**：`domain/file.go` 中定义了完整的 MIME 映射表和 `ClassifyFile` 函数，覆盖 35+ 种主流 MIME 类型，未知类型默认 `CategoryOther`：

```go
func ClassifyFile(mimeType string) FileCategory {
    // 精确匹配 → 前缀匹配 → 默认 "other"
}
```

**查询过滤**：`ListByOwner` 支持按 category 过滤，HTTP 端点 `GET /v1/files?category=image`：

```go
if page.Category != "" {
    q = q.Where("category = ?", string(page.Category))
}
```

### 方向五：资源级通知偏好

**PreferenceChecker 接口扩展**：

```go
type ScopeRef struct {
    Type string // "file", "conversation", or "" (global)
    ID   string
}

type PreferenceChecker interface {
    IsAllowed(ctx context.Context, recipientUID, notificationType string, scopes ...ScopeRef) (bool, error)
}
```

**作用域查询**：`GetScoped` 方法先查精确的行，不存在则 fallback 到全局偏好：

```go
func (r *preferenceRepo) GetScoped(...) (*domain.NotificationPreference, error) {
    // Try scoped → fallback to global → nil (default enabled)
}
```

**安静时段重构**：提取 `checkQuietHours` 辅助函数，消除原 `IsAllowed` 中重复的安静时段判断逻辑：

```go
func checkQuietHours(p *domain.NotificationPreference) bool {
    // 支持跨午夜时段 (22:00-07:00) 和正常时段 (08:00-22:00)
}
```

## 已有代码实现（working tree中已存在的变更）

以下模块已在之前的代码实现中完成，本次未修改：

### 方向一：顶级评论通知
- `internal/service/comment.go` - Create 方法在 `parent == nil` 时产生 `file.commented` / `message.commented` 事件
- `internal/service/notification.go` - Dispatcher 处理两个新事件类型

### 方向二：Reactions/Likes 通知
- `internal/service/comment_reactions.go` - Like 操作发出 `comment.liked` 事件
- `internal/service/message.go` - ReactToMessage 发出 `message.reacted` 事件
- `internal/service/notification.go` - Dispatcher 支持 collapse 聚合逻辑

### 方向三：消息删除通知
- `internal/service/message.go` - SoftDelete 发出 `message.deleted` 事件
- `internal/service/notification.go` - Dispatcher 处理删除通知

### 其他基础设施
- 通知折叠（collapsed_count）、速率限制、偏好检查、阻断抑制
- SSE last_event_id 断线重播
- 搜索 CJK 支持 + 超时 + snippet
- 存储引用计数（storage_refs）
- Hub 扩展：Pusher 接口加入 eventID 参数
- 中间件：采样、熔断 Header、Ban 检查

## 依赖说明

无新增外部依赖。

## 已知限制

1. **搜索文件 mime_type 过滤**：`SearchFiles` 尚未扩展为支持 `mime_type` 参数（架构设计中的 Phase 3，非 P1/P2）
2. **对话静音/文件关注 API**：资源级偏好 API 端点（mute/follow）尚未实现，需要路由+handler 扩展
3. **事件类型注册表**：架构设计建议的集中式注册表尚未实现（当前仍使用 switch dispatch 模式）
4. **Hub 可配置 buffer**：channel buffer 仍是硬编码 16（低严重度）
5. **Dispatcher 拆分**：`notification.go` 437 行接近 500 行限制，未来需拆分为 `dispatcher_message.go` + `dispatcher_comment.go`

## 验证步骤

```bash
# 编译
make build

# 单元测试
make test

# Lint
make lint

# 集成测试（需要 Docker）
make test-integration
```

### 验证结果

```
✅ make build  — 编译通过
✅ make test   — 所有 30+ 包测试通过
✅ make lint   — go vet 无警告
```

## 配置要求

无新增配置项。文件分类和通知偏好作用域依赖数据库 migration：

```bash
# 应用新 migration
make migrate-up
```

迁移文件：
- `000026_add_file_category.up.sql` - 添加 `files.category` 列 + 索引
- `000027_add_preference_scope.up.sql` - 扩展 `notification_preferences` 表支持 scope
