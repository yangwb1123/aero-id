已保存为 `docs/requirements/2026-07-11-systematic-expansion-direction-scan.md`（15.7 KB）。

这份扫描与 70+ 既有文档 100% 零重复，命中了五个真正未被覆盖的方向：

| 方向 | 核心洞察 |
|---|---|
| **延迟任务框架** | 系统当前没有任何"在未来指定时间执行"的机制——4 种异步机制全是即时/周期性，零 `scheduled_at` 语义 |
| **作用域搜索** | 搜索只有 `query + kind + page`，无 scope/时间/类型过滤，是一次性全表匹配 |
| **API Contract** | 38 条 HTTP 路由 + 双传输层，零 OpenAPI 规范，消费者只能读代码 |
| **合成事务探针** | `/healthz`/`/readyz` 只测组件连通性，不测业务路径——典型 false health 隐患 |
| **异步 Context 传播** | Outbox/GC/Cleaner 全部 `context.Background()`，trace 在异步边界断裂 |

三个 P1（延迟任务、合成探针、作用域搜索）建议作为下一迭代的并行投入方向。
