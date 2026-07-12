# 代码实现报告

## 实现概述

基于全面审查，代码库中5个工程方向约70%的工作已经被之前实现。我完成了剩余的缺失功能，重点关注最高价值项。

### 完成状态汇总

| 方向 | 任务 | 状态 | 说明 |
|------|------|------|------|
| 四 — 传输加固 | T015-T021 | ✅ **100%** | 已全部实现（保持活跃、流控制、消息限制、配置、测试） |
| 五 — 数据生命周期 | T022-T024 | ✅ **100%** | audit/notification/outbox 清洁器已实现并接入 |
| 五 — 文档 | T025 | 🆕 **已完成** | 新建 `docs/DATA_RETENTION.md` |
| 二 — 可观测性 | T006, T008 | ✅ **已完成** | AccessLog 采样器 + Relay 日志聚合 |
| 二 — 自适应追踪 | T007 | 🆕 **已完成** | 添加 DynamicSampler + 监控 goroutine |
| 三 — 降级契约 | T010, T011, T012, T014 | ✅ **已完成** | 矩阵文档、状态管理器、中间件、测试 |
| 三 — 处理器适配 | T013 | 🆕 **已完成** | 添加辅助函数 + 接入应用 |
| 一 — 发布工程 | T001-T005 | 🆕 **已完成** | 全套 CI/CD 构建发布工作流 |

---

## 文件清单

### 创建的文件（5 个）
- `.goreleaser.yaml` - GoReleaser 构建/发布配置
- `.github/workflows/docker.yml` - Docker 镜像 CI 工作流
- `.github/workflows/release.yml` - 版本发布工作流（GoReleaser + git-cliff + syft + cosign）
- `cliff.toml` - git-cliff 变更日志生成配置
- `docs/DATA_RETENTION.md` - 数据生命周期策略文档

### 修改的文件（4 个）
- `internal/pkg/tracing/tracing.go` - 添加自适应 DynamicSampler
- `internal/middleware/degradation.go` - 添加 DegradationAction 辅助函数
- `internal/handler/http/router.go` - 接入 DegradationHeader 中间件
- `internal/app/new.go` + `app.go` - 创建并接入 DegradationState 管理器

---

## 核心代码实现

### 1. 自适应追踪降采样 (T007)

#### 代码实现

**DynamicSampler** — 位于 `internal/pkg/tracing/tracing.go`：

```go
// DynamicSampler wraps an atomic ratio (stored as uint64 bits) and adjusts it
// based on exporter health. It implements sdktrace.Sampler.
type DynamicSampler struct {
    ratio         atomic.Uint64    // math.Float64bits-encoded current ratio
    originalRatio float64
    exportErrors   atomic.Uint64
    exportAttempts atomic.Uint64
    stopCh  chan struct{}
    stopped chan struct{}
}
```

**自适应算法：**
- 每 10 秒一个窗口检查导出错误率
- 错误率 > 10% → 将采样率降至 `max(0.1, current/10)`
- 错误率 < 2% 持续 5 个窗口 → 标记为恢复
- 恢复后每 2 分钟将采样率增加 0.1，直到恢复原始值

**关键设计决策：**
- 使用 `atomic.Uint64` + `math.Float64bits`/`Float64frombits` 替代不存在的 `atomic.Float64`
- 使用 TraceID 的低 64 位作为伪随机源，避免额外的随机数生成开销
- 监控 goroutine 通过 channel 优雅关闭

### 2. 降级契约处理器适配 (T013)

#### 代码实现

**辅助函数** — 位于 `internal/middleware/degradation.go`：

```go
type DegradedResponse int

const (
    DegradedStaleOK  DegradedResponse = iota  // 200 + stale data
    DegradedFail503                            // 503 Service Unavailable
    DegradedProceed                            // 正常处理
)

func DegradationAction(state *DegradationState, deps []DegradedDep, readOnly bool) DegradedResponse {
    // 对 DB 降级 + 只读端点 → DegradedStaleOK
    // 其他所有降级组合 → DegradedFail503
}
```

**应用层接入** — 位于 `internal/app/new.go`：

```go
// Degradation state manager — tracks upstream dependency health.
degradationState := middleware.NewDegradationState()
degradationState.SetProber(middleware.DepDB, sqlDB.PingContext)
if ssoClient != nil {
    degradationState.SetProber(middleware.DepSSO, ssoClient.HealthCheck)
}
stopDegradation := degradationState.StartProbing(
    context.Background(), 30*time.Second, 3, 2,
)
```

**关键设计决策：**
- 探针使用独立的 3 秒超时和断路器阈值（3 次失败 → 标记降级）
- `DegradationHeader` 中间件在 gin 响应头写入 `X-Aero-Degraded: db,sso`
- 探针失败不影响业务路径——只更新降级状态位

### 3. 发布工程 (T001-T005)

**`.goreleaser.yaml`** 配置了：
- 两个构建目标：`aero-id` 服务器 + `migrate` 工具
- Docker 镜像构建（使用现有 `deployments/Dockerfile`）
- CycloneDX SBOM 生成（通过 syft）
- cosign 签名

**CI/CD 工作流：**
- `docker.yml`：每次 push 到 main 自动构建并推送到 ghcr.io
- `release.yml`：`git tag v*` 触发完整发布流程（GoReleaser + git-cliff 变更日志 + 签名）

**`cliff.toml`** 配置了 conventional commits 分组（feat/fix/perf/docs/ci 等）

### 4. 数据生命周期文档 (T025)

**`docs/DATA_RETENTION.md`** 覆盖了：
- 8 张表的 retention 策略表
- 每个清洁器的 YAML 配置示例和 SQL 语句
- 实现模式（OPT-IN、分批删除、优雅关闭）
- 监控指标说明
- 不清理的表清单及原因

---

## 测试结果

```
$ go build ./...          # ✅ 编译通过
$ go vet ./...            # ✅ 静态分析通过
$ go test ./... -count=1  # ✅ 所有测试通过
$ make check-file-sizes   # ✅ 文件大小合规
```

所有 30+ 测试包全部通过，包括 gRPC 服务器选项测试（T021）、降级状态测试（T014）、HTTP 处理器测试。

---

## 依赖说明

无新增外部依赖。所有实现使用现有依赖：
- `go.opentelemetry.io/otel/sdk/trace` — Sampler 接口（已有依赖）
- `sync/atomic` — 标准库（已有）

---

## 配置要求

请将以下配置项的值替换为生产环境实际值：

| 配置 | 文件 | 说明 |
|------|------|------|
| `COSIGN_PRIVATE_KEY` | GitHub Secrets | SBOM 签名密钥 |
| `COSIGN_PASSWORD` | GitHub Secrets | 签名密钥密码 |

其他配置项（`data_retention.*`、`log.sample_ratio`、`server.grpc.*` 等）已在 `config.yaml` 中设置默认值，可作为示例参考。
