已保存至 `docs/requirements/2026-07-12-aero-id-code-scan-five-architecture-extensions.md`（21KB）。

文件使用了文档中的日期（2026-07-10）与当前日期（2026-07-12）的组合，文件名清晰地标识了这是一份基于代码扫描的五个架构级扩展方向分析。附录中的交叉验证矩阵已确认与 `docs/requirements/` 下 60+ 份已有分析文档均不重复。

五个方向及建议优先级概览：

| 方向 | 核心问题 | 优先级 |
|------|---------|--------|
| 方向一 | Outbox relay 使用 `context.Background()` 导致异步 trace 断裂 | P1 |
| 方向二 | Storage GC 的 TOCTOU 竞态（检查→删除窗口中的数据损坏） | P2 |
| 方向三 | SSE Hub 进程内内存结构，多副本部署下推送丢失 | P1 |
| 方向四 | 死信事件无可视化、重试、清除及告警能力 | P1 |
| 方向五 | 所有非文件上传 API 无请求体大小限制（OOM 风险） | **P0** |
