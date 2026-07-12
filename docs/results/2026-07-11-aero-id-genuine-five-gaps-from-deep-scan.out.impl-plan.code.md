All checks pass. Let me now provide the comprehensive implementation report.

---

# 代码实现报告

## 实现概述

基于 `docs/imp plan/2026-07-12-implementation-plan.md` 的技术方案，对现存代码进行了全面评估，并对三个未完成的任务（TASK-009、TASK-013、TASK-014）进行了实现。**报告显示约 90% 的代码已经在实施计划之前完成**。

## 当前状态总览

| 方向 | 任务 | 状态 | 说明 |
|------|------|------|------|
| **四 — 传输协议加固** | T015~T021 | ✅ **已完成** | gRPC keepalive、MaxConcurrentStreams、消息大小限制、窗口大小、HTTP ReadHeaderTimeout、配置暴露与校验、单元测试 |
| **一 — 发布工程** | T001~T005 | ✅ **已完成** | `.goreleaser.yaml`、Docker CI workflow、Release workflow、`cliff.toml`、SBOM + cosign 签名 |
| **二 — 可观测性成本** | T006~T008 | ✅ **已完成** | AccessLog 采样器、追踪自适应降采样（DynamicSampler）、Relay 日志聚合 |
| **三 — 降级契约文档** | T010 | ✅ **已完成** | `docs/DEGRADATION_MATRIX.md` 详细矩阵 |
| **三 — 降级状态管理器** | T011~T012 | ✅ **已完成** | `DegradationState` 管理器、HTTP 中间件、gRPC 拦截器、`DegradationAction` 辅助函数 |
| **五 — 数据生命周期** | T022~T025 | ✅ **已完成** | 3 个清洁器（audit、notification、outbox）均已实现、文档完整、在 `new.go` 中布线和优雅关闭 |
| **二 — 指标写放大优化** | T009 | ✅ **本次实现** | 使用分离的 `outboxMu` 互斥锁和原子量优化 outbox 指标路径 |
| **三 — Handler 适配** | T013 | ✅ **本次实现** | 为 file 和 message handler 添加了降级检查，创建了 `AbortIfDegraded` 辅助函数 |
| **三 — 降级集成测试** | T014 | 🟡 **部分完成** | gRPC 降级拦截器已有单元测试和集成测试；HTTP handler 的降级测试仍需添加 |

## 文件清单（本次修改）

### 新建文件
无（所有功能基于现有文件修改）

### 修改文件

| 文件 | 变更说明 |
|------|---------|
| `internal/pkg/metrics/metrics.go` | ✅ TASK-009: 将 outbox 指标分离到 `outboxMu` 互斥锁，`outboxPending`/`outboxLagSecs` 改为 `atomic.Uint64` 以减少锁竞争 |
| `internal/middleware/degradation.go` | ✅ TASK-013: 添加 `AbortIfDegraded` 辅助函数和 `DegradationFromCtx` 上下文提取函数；`DegradationHeader` 中间件现在将状态存入 gin 上下文 |
| `internal/handler/http/file.go` | ✅ TASK-013: 为 Upload、PresignUpload、Delete、UpdateMetadata、Share 添加降级检查 |
| `internal/handler/http/message.go` | ✅ TASK-013: 为 Send、Edit、Delete、ReactToMessage 添加降级检查 |
| `docs/DEGRADATION_MATRIX.md` | ✅ TASK-013: 更新实现状态和优先级信息 |

## 核心代码实现

### TASK-009：指标写放大优化

#### 问题分析
`ObserveOutboxDrain` 每次 drain 周期（每 5 秒）都持有全局 `mu.Lock()`，这会阻塞 HTTP/gRPC 和 SSO 指标路径。在每小时 17,280 次 drain 调用下，累积的锁竞争在高并发场景下可能影响请求延迟。

#### 实现方案
1. **分离互斥锁**：为 outbox 指标新增 `outboxMu sync.Mutex`，与 HTTP/gRPC 和 SSO 指标使用的 `mu` 互斥锁分离
2. **原子量**：将 `outboxPending` 和 `outboxLagSecs` 从 `float64` 改为 `atomic.Uint64`（使用 `math.Float64bits` 编码），使 gauge 写入无需任何锁
3. **分离计数 map**：`outboxPublished` 和 `outboxFailed` map 受 `outboxMu` 保护，与 `mu` 分离

#### 核心代码

```go
// Registry with outbox-specific locking
type Registry struct {
    mu       sync.Mutex          // protects requests + latency + SSO metrics
    outboxMu sync.Mutex          // protects outboxPublished, outboxFailed, outboxDrain
    
    outboxPending atomic.Uint64  // gauge, lock-free write
    outboxLagSecs atomic.Uint64  // gauge, lock-free write
    outboxDrain   *histogram     // histogram, protected by outboxMu
}

func (r *Registry) ObserveOutboxDrain(pending int, oldestAge, dur time.Duration) {
    // Atomics — no lock needed for gauge values
    r.outboxPending.Store(math.Float64bits(float64(pending)))
    r.outboxLagSecs.Store(math.Float64bits(oldestAge.Seconds()))

    secs := dur.Seconds()
    r.outboxMu.Lock()
    // ... histogram update ...
    r.outboxMu.Unlock()
}
```

#### 关键设计决策
- **为什么不用累加器+周期性 flush？** 累计器模式需要额外的 goroutine 和通道，而分离互斥锁原子模式更简单、更可靠，且同样达到消除跨域锁竞争的核心目标。
- **为什么保留 histograms 的互斥锁？** histograms 的累计（bucket counts + sum + count）需要原子操作整个结构体，完全无锁化需要复杂的 atomic 数组操作，收益有限。

---

### TASK-013：Handler 适配降级逻辑

#### 问题分析
`DegradationState` 管理器、`DegradationAction` 辅助函数和 `DegradationHeader` 中间件均已就位，但没有任何 handler 实际检查降级状态并据此调整行为。当 DB 降级时，handler 仍尝试正常处理请求，导致超时后才返回 500，而非立即返回 503。

#### 实现方案
1. **上下文存储**：修改 `DegradationHeader` 中间件，将 `DegradationState` 引用存入 gin 上下文
2. **辅助函数**：新增 `DegradationFromCtx(c) *DegradationState` 和 `AbortIfDegraded(c, deps, readOnly) bool`
3. **Handler 集成**：在 file 和 message handler 的写操作入口处添加降级检查

#### 核心代码

```go
// In middleware/degradation.go
const degradationStateKey = "__deg_state"

func DegradationHeader(state *DegradationState) gin.HandlerFunc {
    return func(c *gin.Context) {
        c.Set(degradationStateKey, state)  // store for downstream handlers
        c.Next()
        c.Header("X-Aero-Degraded", state.Status())
    }
}

func AbortIfDegraded(c *gin.Context, deps []DegradedDep, readOnly bool) bool {
    state := DegradationFromCtx(c)
    if state == nil {
        return false
    }
    action := DegradationAction(state, deps, readOnly)
    if action == DegradedFail503 {
        httpx.WriteError(c, apperr.Internal("degraded", "service unavailable — upstream dependency degraded"))
        c.Abort()
        return true
    }
    return false
}

// In handler code (e.g., file upload):
func (h *FileHandler) Upload(c *gin.Context) {
    if middleware.AbortIfDegraded(c, middleware.DepsForOp(middleware.DepDB, middleware.DepStorage), false) {
        return
    }
    // ... continue with upload ...
}
```

#### 关键设计决策
- **为什么不在构造函数中注入 `DegradationState`？** 使用 gin 上下文传递避免修改所有 handler 构造函数，减少改动量和测试影响。handler 只需导入 middleware 包并在写操作前添加一行检查。
- **为什么只实现 file 和 message handler？** 这两个是最关键的写密集型 handler。其他 handler（comment、notification、block、preference）可以在未来需要时通过同样的一行模式增量添加。

---

## 依赖说明

本次实现未新增任何外部依赖。所有功能均使用现有标准库和项目内部包实现。

## 已知限制

1. **读端点的降级处理**：当 DB 降级时，读端点的理想行为是返回 stale 数据（`DegradedStaleOK`），但当前没有实现缓存层。所有读请求仍会因 DB 不可用而失败。
2. **Handler 覆盖范围**：目前只有 file 和 message handler 进行了降级适配。comment、notification、block、preference、search handler 尚未适配。
3. **SSO 降级**：SSO 降级在 auth 中间件层通过 stale cache 处理，handler 层无需特别处理。如果 token 不在缓存中，请求会在 auth 中间件被拒绝。
4. **gRPC handler**：gRPC handler 尚未添加降级检查（但 degradation interceptor 已在所有 gRPC 响应中添加了降级 trailer）。

## 验证步骤

1. **编译**：`go build ./...` ✅ 通过
2. **Vet**：`go vet ./...` ✅ 通过  
3. **单元测试**：`go test ./... -count=1` ✅ 全部通过
4. **Lint**：`make lint` ✅ 通过

## 后续工作建议

1. **添加其他 handler 的降级检查**：comment、notification、block 等 handler 可按 file/message 的同一模式添加 `AbortIfDegraded` 检查
2. **降级集成测试**：编写 bufconn/httptest 测试，验证 DB/SSO/S3 降级时 handler 的响应行为
3. **读端点的 stale 缓存**：考虑为读端点添加内存缓存层，使 DB 降级时仍能返回 stale 数据（200 + `Warning: 110 stale-data`）
4. **监控降级率**：添加 `aeroid_degraded_requests_total` 指标，区分 503（拒绝）和 200（stale）
