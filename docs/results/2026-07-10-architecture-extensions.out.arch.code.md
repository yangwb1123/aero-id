Everything is clean and verified. Here is the complete implementation report:

---

# 代码实现报告

## 实现概述

在对项目代码库进行全面分析后，发现**大多数实现计划中的任务已在之前的迭代中完成**。本次实现专注于填补一个关键缺口：**gRPC 降级拦截器**（实现计划中的 TASK-012 gRPC 部分）。

主要变更：
1. 在 gRPC server 中新增 `WithDegradation` 选项和 `DegradationInterceptor` 拦截器
2. 在 `new.go` 中将降级状态注入 gRPC 服务器
3. 为降级拦截器添加单元测试和集成测试

## 文件清单

### 修改的文件

- `internal/handler/grpc/server.go` - 新增 `WithDegradation()` 选项、`DegradationInterceptor()` 函数、以及拦截器链集成
- `internal/app/new.go` - 将 `grpcsrv.WithDegradation(degradationState)` 添加到 gRPC 服务器选项
- `internal/handler/grpc/server_test.go` - 新增降级拦截器的单元测试和集成测试

## 核心代码实现

### gRPC 降级拦截器

#### 1. ServerOption 和 options 结构体扩展

在 `server.go` 的 `serverOptions` 结构体中添加 `degradation` 字段，并新增 `WithDegradation()` 选项函数：

```go
type serverOptions struct {
    limiter        *ratelimit.Limiter
    permChecker    middleware.PermissionChecker
    degradation    *middleware.DegradationState  // 新增
    grpcOpts       []grpc.ServerOption
}

func WithDegradation(d *middleware.DegradationState) ServerOption {
    return func(o *serverOptions) { o.degradation = d }
}
```

#### 2. DegradationInterceptor 实现

```go
func DegradationInterceptor(state *middleware.DegradationState) grpc.UnaryServerInterceptor {
    return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
        status := state.Status()
        if info != nil && infraMethod(info.FullMethod) {
            status = "none"
        }
        // SetTrailer 必须在 handler 之前调用：gRPC 内部存储 metadata.MD
        // 的引用，在响应发送时序列化。
        if err := grpc.SetTrailer(ctx, metadata.Pairs("x-aero-degraded", status)); err != nil {
            // 降级元数据是辅助信息，非关键信息，静默跳过
        }
        return handler(ctx, req)
    }
}
```

#### 3. 拦截器链集成

拦截器在 `NewServerWithOptions` 中的放置位置：

```
Tracing → Metrics → [Degradation] → Recovery → Auth → RateLimit → Permission → handler
```

降解拦截器放置在 `Tracing`/`Metrics` 之后、`Recovery` 之前，以便：
- 每个响应都带有 `x-aero-degraded` trailer（包括错误响应）
- 基础设施方法（health/reflection）始终标记为 `"none"`
- 不影响请求路径——仅装饰元数据

#### 4. new.go 集成

```go
grpcsrv.WithDegradation(degradationState),
```

添加到 `NewServerWithOptions` 的 options 列表中。

## 当前实现状态总结

| 方向 | 任务 | 状态 | 备注 |
|------|------|------|------|
| 一：发布工程 | TASK-001~005 | ✅ 已完成 | `.goreleaser.yaml`, docker/release CI, cliff.toml, SBOM/signing |
| 二：可观测性成本 | TASK-006~008 | ✅ 已完成 | 采样、自适应降采样、relay 聚合日志 |
| 二：可观测性成本 | TASK-009 | ⏳ P2 | 当前实现可用，atomic+flush 优化可后续迭代 |
| 三：降级契约（文档） | TASK-010 | ✅ 已完成 | `docs/DEGRADATION_MATRIX.md` |
| 三：降级契约（状态管理器） | TASK-011 | ✅ 已完成 | `degradation.go` 含完整的状态管理和探测 |
| 三：降级契约（HTTP 中间件） | TASK-012 HTTP | ✅ 已完成 | 在 `router.go` 中集成 |
| 三：降级契约（gRPC 中间件） | TASK-012 gRPC | ✅ **本次完成** | 新增 `DegradationInterceptor` |
| 三：降级契约（Handler 适配） | TASK-013 | ❌ 未实现 | 辅助函数已存在 (`DegradationAction`, `DepsForOp`)，handler 未接入——P2 |
| 三：降级契约（集成测试） | TASK-014 | 🟡 部分 | 单测 + health 集成测试已添加，自定义服务测试因 gRPC API 限制跳过 |
| 四：传输协议加固 | TASK-015~021 | ✅ 已完成 | 所有 gRPC/HTTP 安全配置、验证、测试 |
| 五：数据生命周期 | TASK-022~025 | ✅ 已完成 | 所有清理器 + 文档 |

## 关键设计决策

1. **`grpc.SetTrailer` 在 handler 之前调用**：gRPC v1.80.0 的 `SetTrailer` 存储 `metadata.MD`（map 引用类型）的引用，在响应发送时序列化。必须在 handler *之前* 调用。将 trailer 设置在 handler 之后太晚——trailer 已经最终确定。

2. **基础设施方法标记为 "none"**：Health check 和反射方法不依赖任何业务依赖，因此即使依赖降级也始终标记为 `"none"`。

3. **降级元数据是非关键的**：`grpc.SetTrailer` 的错误被静默忽略。降级状态是用于客户端/负载均衡器的操作元数据——永远不会因为元数据写入失败而拒绝请求。

## 验证步骤

```bash
# 编译检查
make build

# Lint 检查
make lint

# 单元测试
go test ./internal/handler/grpc/... -count=1 -v -run "TestDegradation"

# 完整测试
make test
```

所有测试通过，Lint 通过。
