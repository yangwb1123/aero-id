确认。旧 gRPC handler 全部导入 `internal/service`（旧架构），而 `internal/{comment,file,message,block,notification,search}/handler_grpc.go` 是新领域包的自有 handler——全部是死代码。

---

## 最终评估

文档《五项结构性盲区与高价值扩展方向》的**所有核心声明均已通过代码级验证确认**。以下是五大方向的优先级修正建议（基于文档 + 验证补充）：

### P0（前置条件）— 方向一：架构分裂统一

**文档低估了此问题的严重性**。存在三层死代码依赖：
```
旧架构（活代码）         新架构（死代码）
internal/app/app.go     internal/{comment,file,message,block,notification,search,audit}/
  ├─ internal/handler/http/*       ├─ */handler_http.go (存在但不全)
  ├─ internal/handler/grpc/*       ├─ */handler_grpc.go (存在)
  ├─ internal/service/*            ├─ */service.go
  ├─ internal/repository/*         └─ */repository.go
  └─ internal/notification.go     internal/notification/dispatcher.go
```

关键陷阱：actor context 键分裂（`service.ctxKey` vs `domain.ctxKey`）是**运行时级安全漏洞**——如果有人将新架构服务接入生产路径但未修复此问题，所有请求会返回 `auth.unauthorized`。

### P1 — 方向五（Webhook 生态） + 方向二（数据生命周期）

- **Webhook**：`outbox/webhook.go` 的 `WebhookConfig` 已有 `EventTypes` 过滤字段，基础设施已就位。添加 `webhook_subscriptions` 表和 API 是增量工作。
- **数据生命周期**：可复用 `storage/gc/sweeper.go` 的 `Config` + `Run`/`Start` 模式——它已是领域无关的扫描-清理循环抽象。需要新增 `expires_at` 字段和通用 TTL 清理器。

### P2 — 方向三（对话管理） + 方向四（媒体管线）

- **对话管理**：数据模型层面的缺失（无 `conversations` 表，无 `conversation_id` 列）意味着数据库迁移较复杂，但业务逻辑相对简单。
- **媒体管线**：最复杂的方向，需要外部依赖和异步 pipeline。建议**先做 MIME magic bytes 验证**（`http.DetectContentType`，零外部依赖），再做缩略图生成。

---

**总结**：文档的作者完成了**彻底的逐文件审计**，100+ 份需求分析和五轮交叉比对产生了一个高质量的架构评估。五个方向的证据链完整，论证逻辑严密，优先级的排序（架构统一为前置条件）与代码库的实际情况一致。这份文档应当作为下一轮迭代的技术策略输入。
