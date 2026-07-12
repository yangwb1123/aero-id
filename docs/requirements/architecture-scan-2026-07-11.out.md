两份文件已保存到 `docs/requirements/`：

1. **`2026-07-12-aero-id-global-architecture-scan-high-value-extensions.md`** — 您共享的原始分析文档
2. **`2026-07-12-aero-id-global-architecture-scan-high-value-extensions.out.md`** — 我的代码验证与深度分析报告

---

## 验证摘要

对文档中 **所有代码引用** 进行了逐条追踪验证。**11/12 条声明全部准确**，仅有 1 处小偏差：

| 文档声明 | 实际状态 |
|---------|---------|
| `internal/handler/grpc/ops.go` 无单元测试 | `ops_test.go` **存在**，但仅覆盖 health check，未覆盖 reflection——实质判断基本正确 |

## 主要调整建议

| 方向 | 文档原定 | 建议调整 | 理由 |
|------|---------|---------|------|
| 方向一：推送通知 | **P0** | **P1** | 需要 SSO 侧跨团队配合 FCM 凭据，不应标为阻塞项 |
| 方向二：死信运维 | **P0** | **P0** ✅ | 保持——最优先，4-6h 可落地 |
| 方向四：文件版本管线 | **P1** | **P2** | 低估了外部队列（Redis Streams）和独立 worker 进程的架构复杂度 |
| 方向五：活动流 | **P2** | **P2** | 建议混合方案 C（双写 + Lazy 合并），避免 10k+ 粉丝的 fan-out 写放大 |

## 次级债务中最值得立即修复的

**`actorKey`/`WithActor`/`ActorFrom` 去重** — `internal/service/service.go` 与 `internal/domain/actor.go` 中存在完全重复的实现。建议保留 `domain/actor.go` 版本（domain 层语义更合适），消除技术债务同时不影响任何功能。改动量约 15 处 import/调用替换。
