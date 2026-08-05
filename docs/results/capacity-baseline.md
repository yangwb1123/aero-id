# 容量基线（P0-06）

> 状态：本地微基准已建立；端到端 SLO 基线需在部署单元上执行 `scripts/load` 压测后回填。

## 本地微基准（2026-08-05 快照，Go 1.26 / 32 核工作台）

```bash
go test -bench=. -benchmem ./internal/service/ -run '^$'
```

| 基准 | 每操作耗时 | 分配 |
|---|---|---|
| BenchmarkEventIngest（envelope 校验 + 投影折叠，fake repo） | ~2.3 µs | 1.7 KB / 21 allocs |
| BenchmarkEnvelopeValidate（canonical envelope 校验） | ~83 ns | 5 B / 0 allocs |
| BenchmarkRegionResolver（home-region hash ring） | ~149 ns | 48 B / 1 alloc |
| BenchmarkEventRedactPayload | 待测 | — |
| BenchmarkProjectionReplayFolding（100 事件重建） | 待测 | — |

微基准衡量确定路径的 CPU 成本；真实 SLO 以压测为准。

## 初始 SLO 目标（计划 §5）

| 指标 | 目标 | 测量方式 |
|---|---|---|
| 区域聚合快照读取 | p95 ≤ 100ms，p99 ≤ 250ms | k6 `/v1/me/overview`、`/v1/me/data` |
| 本地账户写入/Operation 接受 | p95 ≤ 200ms | k6 `/v1/me/sync`、`/v1/operations` |
| 默认聚合事件延迟 | p95 ≤ 5s，p99 ≤ 30s | NATS ingest→投影 时延仪表 |
| 区域 API 可用性 | 99.99% | SLO 仪表 |
| 本地已确认审计事件 RPO | 0 | 审计 ingest 失败率仪表 |
| 跨区域审计复制 RPO | ≤ 60s | replication cursor lag 仪表 |
| 普通源调用 deadline | 本地 2s，跨区 5s | connector 指标 |

## 执行压测（部署单元上）

```bash
make load-test-http BASE_URL=https://api.<region>.example.com TOKEN=<token> VUS=200 DURATION=10m
make load-test-grpc TOKEN=<token> BASE_URL_GRPC=<cell-grpc-addr>
```

结果落到 `scripts/load/results/`。回填后在本文件中记录：RPS 上限、p95/p99、DB pool 饱和点、源 connector 断路器触发阈值、event/consumer lag 峰值。

## 容量规划输入（计划 §5）

至少覆盖：账户数、每账户来源数、事件/秒、审计事件/秒、快照大小、回调订阅数、导出并发、源调用并发、跨区域带宽。基线数据按季度复核。
