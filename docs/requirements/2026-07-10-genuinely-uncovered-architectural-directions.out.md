Now I have a thorough picture of the codebase. Here's my assessment of each direction:

---

## 逐项评估（基于实际代码库现状）

### 方向一：`internal/file` 死代码 — ⚠️ **结论正确，但描述过激**

**准确的部分**：`internal/file` 包中确实存在一套与 `internal/service/`、`internal/repository/`、`internal/handler/http|grpc/` 平行的副本：

| 文件 | 内容 | 生产是否引用 |
|------|------|------------|
| `service_crud.go` | Service CRUD 实现 | ❌（生产走 `internal/service/file.go`） |
| `service_upload.go` | Service 上传实现 | ❌ |
| `service_share.go` | Service 分享实现 | ❌ |
| `handler_http.go` | HTTP handler | ❌ |
| `handler_grpc.go` | gRPC Server struct | ❌（但其中的 `FileToPB()` 被使用） |
| `repository.go` | GORM repo 实现 | ❌ |
| `repository_share.go` | Share repo GORM 实现 | ❌ |

**不准确的部分**：文档说 "`internal/file` 包是完整的死代码"。实际 **不是全部** 死代码：

```go
// internal/search/search.go:7  — 引用 file.Repository interface
import "github.com/aero/aero-id/internal/file"

// internal/search/handler_grpc.go:10  — 引用 FileToPB()
import "github.com/aero/aero-id/internal/file"
// 实际调用：internal/search/handler_grpc.go:43
pb.File = file.FileToPB(hit.File)
```

`internal/file/file.go` 中的 `Repository` 接口和 `internal/file/handler_grpc.go` 中的 `FileToPB()` 被 `internal/search` 包**生产使用**。文档的表格后半部分是准确的（service/handler/repo 实现未被引用），但"完整死代码"的论断过头了。

**修正后的建议**：
- 保留 `file.go`（接口定义）和 `handler_grpc.go`（包含 `FileToPB` 导出函数）
- 在 `service_*.go`、`handler_http.go`、`repository*.go` 加 `// Deprecated: unused.` 注释
- 清理测试文件（`service_test.go`、`handler_http_test.go`、`handler_grpc_test.go`、`repository_integration_test.go`）

---

### 方向二：Admin 角色管理 API 只有一半 — ❌ **结论基本错误，代码已完整实现**

文档称 proto 无 `AdminService`、gRPC 无注册、HTTP 无 `GrantRole`/`RevokeRole` 路由。但实际代码库中这三项**全部存在**：

**Proto 定义 — 已存在**：
```protobuf
// api/proto/aero_id/v1/aero_id.proto:320
service AdminService {
  rpc GrantRole(GrantRoleRequest) returns (GrantRoleResponse);
  rpc RevokeRole(RevokeRoleRequest) returns (RevokeRoleResponse);
  rpc ListRoles(ListRolesRequest) returns (ListRolesResponse);
  rpc ListUserRoles(ListUserRolesRequest) returns (ListUserRolesResponse);
  rpc ListAudit(ListAuditRequest) returns (ListAuditResponse);
}
```

**HTTP Handler — 已注册**：
```go
// internal/handler/http/admin.go:47-54
syscfg := r.Auth.Group("/admin")
syscfg.Use(middleware.RequirePermission(domain.PermSystemConfig, h.permChecker))
{
    syscfg.POST("/roles/grant", h.GrantRole)    // ✅ 存在
    syscfg.POST("/roles/revoke", h.RevokeRole)  // ✅ 存在
}
```

**gRPC Server — 已注册**：
```go
// internal/handler/grpc/admin.go — 完整的 AdminServer 实现（GrantRole, RevokeRole, ListRoles, ListUserRoles, ListAudit 全部实现）

// internal/app/app.go:258
adminSvc := service.NewAdminService(adminRepo, ...)
// internal/app/app.go:337
grpcsrv.NewAdminServer(adminSvc),  // 在 composition root 中注册
```

**统一权限中间件 — 已存在**：
```go
// internal/middleware/permission.go — RequirePermission 中间件
// internal/handler/grpc/server.go:243 — PermissionInterceptor + requiredPermForMethod 映射
```

文档中的表（Repository 有但 Handler 无）和"证据"（proto 无定义/gRPC 无注册/HTTP 只有审计日志）**均不反映当前代码状态**。这些功能显然是在文档编写后完成的。

---

### 方向三：PresignUpload 跳过去重和引用计数 — ✅ **完全准确**

代码确认：

```go
// internal/service/file_presign.go:60-65 (注释) — 明确声明跳过去重
// Content-checksum dedup is intentionally SKIPPED for direct uploads:

// internal/service/file_presign.go:108-120 (结尾) — Create 后无 refs.Increment
if err := s.files.Create(ctx, f); err != nil {
    _ = s.store.Delete(ctx, in.StorageKey)
    return nil, apperr.Internal("file.create_failed", ...)
}
return f, nil  // ← 无 s.refs.Increment()
```

对比普通路径：
```go
// internal/service/file.go:162-165
if s.refs != nil {
    _, _ = s.refs.Increment(ctx, f.StorageKey)  // ✅ 普通路径有调用
}
```

这是真实的不一致。需要补充 `s.refs.Increment()`。

---

### 方向四：内容安全管线完全缺失 — ✅ **基本准确**

搜索确认：`internal/` 中无任何病毒扫描、NSFW 检测、敏感词过滤、URL 安全检测代码。但需注意两点修正：

1. **MIME 白名单默认行为**：文档说"配置若留空则全部放行"是准确的，但这在多数场景下是预期行为（如果服务不想做内容类型限制）。不算架构缺陷，只是策略选择。

2. **基础设施已有的推论**准确：outbox relay chain 确实是天然的插入点。`internal/outbox/` 的 `Chain` 模式已支持多个 Handler 串联。

需要补充的精确信息：
- `internal/service/message.go` 的 `Send` 方法确实有长度上限（由 `config` 的 `max_message_length` 控制？），需进一步验证
- `internal/service/comment.go` 的 `Create` 确实无内容检测

---

### 方向五：RED 断路器 — ⚠️ **部分准确，文档遗漏了已有工作**

**现有健康端点**比文档描述的更强：
```go
// internal/handler/http/health.go:23-60 — HealthHandler
// 支持 Critical 和 non-critical 两种检查级别
// 健康状态输出 degraded / unavailable / ready 三种状态

// internal/app/app.go:291-295
health := httpapi.NewHealthHandler(
    httpapi.Check{Name: "database", Critical: true, Probe: sqlDB.PingContext},
    httpapi.Check{Name: "sso", Critical: cfg.SSO.ReadinessCritical, Probe: ssoClient.HealthCheck},
)
```

`/readyz` 已经支持 degraded 状态（non-critical 检查失败时返回 200 with status "degraded"）。但文档说的三个问题确实存在：

1. **S3 存储延迟不纳入 readiness** — 没有 `storage` 的 health check
2. **outbox 积压无指标暴露** — 没有 pending_events 指标
3. **无全局协调断路器** — 各实例独立做 circuit break

`metrics` 包确实只采集不决策（`internal/pkg/metrics/metrics.go`）。

---

## 修正后的总体评估

| # | 方向 | 文档准确性 | 实际优先级 |
|---|------|-----------|----------|
| 1 | `internal/file` 双实现 | ⚠️ 部分正确但夸大 | P1（部分代码可删） |
| 2 | Admin API 不完整 | ❌ 已完全实现 | 不适用 |
| 3 | Presign 缺 ref count | ✅ 完全准确 | **P0**  |
| 4 | 内容安全缺失 | ✅ 基本准确 | P1（合规红线） |
| 5 | 健康/RED 缺失 | ⚠️ 部分过时 | P2 |

**建议的立即行动**：

1. **PR: PresignUpload 补充 ref count**（方向三）— 10 分钟修复，纯收益
2. **PR: 标记 internal/file 中的死代码**（方向一）— 加注释 + 确认无引用即可  
3. **Pruning: 清理 internal/file 中未使用的 service/handler/repo 实现文件**（方向一后续）

方向二不再需要任何操作。方向四和五值得继续跟踪但不在本次 sprint 的紧急范围内。

需要我直接执行方向一和方向三的修复吗？
