您好！我已担任 **Code Implementer** 角色，并已完成项目状态分析。

## 当前项目状态

我发现了详细的实现计划文档：`docs/imp plan/2026-07-12-implementation-plan.md`，其中包含 **25 个任务**，覆盖 **5 个工程方向**：

| 方向 | 描述 | 任务数 | 优先级 |
|------|------|--------|--------|
| 一 | 发布工程 (Release Engineering) | 5 | P1 |
| 二 | 可观测性成本 (Observability Cost) | 4 | P2 |
| 三 | 降级契约 (Degradation Contract) | 5 | P1(文档)/P2(代码) |
| 四 | 传输协议加固 (Transport Hardening) | 7 | P1 |
| 五 | 数据生命周期 (Data Lifecycle) | 4 | P2 |

### 已实现的任务（工作区中有代码变更）

从工作区的变更来看，以下任务已有代码实现：
- **方向四 (T015-T021)**: gRPC keepalive enforcement、MaxConcurrentStreams、消息大小限制、HTTP ReadHeaderTimeout、初始窗口大小、配置暴露与校验、gRPC server 单元测试
- **方向二 (T006, T008)**: AccessLog 采样器、Relay 日志聚合
- **方向五 (T022)**: audit_events TTL 清洁器
- **方向三 (T010)**: 降级矩阵文档

代码编译通过 ✅，所有单元测试通过 ✅，`make lint` 通过 ✅。

### 尚未实现的任务

根据实现计划，以下任务仍在待办状态：
- **方向一 (T001-T005)**: Goreleaser 配置、Docker CI、版本标签、CHANGELOG 自动化、SBOM
- **方向二 (T007, T009)**: 追踪自适应降采样、指标写放大优化
- **方向三 (T011-T014)**: 降级状态管理器、降级中间件、Handler 适配、集成测试
- **方向五 (T023-T025)**: notifications 清洁器、outbox 死信清理、生命周期策略文档

---

请问您希望我实现哪个方向/哪些任务？或者您有新的需求文档需要我基于架构设计和实现计划进行代码实现？
