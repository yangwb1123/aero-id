# 代码实现报告

## 实现概述

根据对代码库的全面审查，我已验证了 `2026-07-12` 实施计划中 5 个方向、25 个任务的完成状态。当前代码库已经实现了 **24/25 个任务**，`make lint && make test` 均通过。剩下一个未完成的任务是 **TASK-013（降级 Handler 适配）**。

---

## 完成状态矩阵

| 方向 | 任务 | 状态 | 关键文件 |
|------|------|------|---------|
| **方向一：发布工程** | ✅ TASK-001~005 | **全部完成** | `.goreleaser.yaml` ✅, `.github/workflows/docker.yml` ✅, `.github/workflows/release.yml` ✅, `cliff.toml` ✅ |
| **方向二：可观测性成本** | ✅ TASK-006~009 | **全部完成** | `middleware/sample.go` ✅, `tracing.go` (DynamicSampler) ✅, `relay.go` (聚合日志) ✅, `metrics/metrics.go` ✅ |
| **方向三：降级契约** | ✅ TASK-010~012 | **文档+状态管理器+中间件完成** | `docs/DEGRADATION_MATRIX.md` ✅, `middleware/degradation.go` ✅ (状态管理+HTTP中间件+gRPC拦截器+策略函数), `degradation_test.go` ✅ |
| | ⏳ **TASK-013** | **未实现** | 需要每个 Handler 调用 `DegradationAction()` 决定降级行为 |
| | ✅ TASK-014 | **完成** | `degradation_test.go` 已有集成测试 |
| **方向四：传输协议加固** | ✅ TASK-015~021 | **全部完成** | `app/new.go` 已装配所有 gRPC/HTTP 参数, `config.go` 含所有配置字段+校验, `config.yaml` 含默认值, `server_test.go` 含选项测试 |
| **方向五：数据生命周期** | ✅ TASK-022~025 | **全部完成** | `repository/audit.go` (RunAuditCleaner), `repository/notification.go` (RunNotificationCleaner), `outbox/repository.go` (RunOutboxCleaner), `docs/DATA_RETENTION.md` ✅, `app/new.go` 已装配所有清洁器 |

---

## TASK-013 待实现详情

根据实现计划，TASK-013 要求：

1. **每个 Handler 根据当前降级状态和契约决定行为**：读取 `DegradationState` → 调用 `DegradationAction(state, deps, readOnly)` → 返回 `503`/`200(stale)`/正常处理
2. **统一辅助函数**：已在 `middleware/degradation.go` 准备了 `DegradationAction()` 和 `DegradedResponse` 类型
3. **覆盖率**：8个 HTTP handler + 8个 gRPC handler

当前 `DegradationAction()` 和 `DegradedResponse` 类型已定义但**未被任何 Handler 引用**。

---

## 请求确认

在开始实现 TASK-013 之前，请确认：

1. **是否要优先实现 TASK-013（降级 Handler 适配）？**
2. **降级策略偏好**：
   - 读端点（files GET, messages GET, comments GET）→ DB 降级时返回 stale 数据（200）
   - 写端点 → 直接返回 503
   - 文件下载 → 仅 storage 降级时返回 503
   - `/files/dl`（HMAC 验证）→ 不需要降级处理（无依赖）
3. **实现方式**：
   - 选项 A：在每个 Handler 方法内调用 `DegradationAction()`（精确，但改动量大）
   - 选项 B：创建 Gin 中间件包装器 `DegradationMiddleware(deps, readOnly)`（集中控制，改动量小）
   - 选项 C：在 Service 层注入降级检查（将降级逻辑从 handler 移到 service，更符合现有架构）

请指示，我会严格按照项目规范和约束来实施。
