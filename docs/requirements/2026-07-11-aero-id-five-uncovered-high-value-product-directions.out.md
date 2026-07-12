文件已在 `docs/requirements/expansion-directions.md` 中存在，内容与您分享的一致。已通读完毕。

总结我理解的核心要点：

**五个方向及其代码级证据的重点：**

| # | 方向 | 核心缺口 | 我确认的关键代码位置 |
|---|------|---------|-------------------|
| 1 | **分享链接 → 匿名外部分享** | `FileShareRepository` 无 `share_token`/`expires_at`/`password_hash` 概念；`CanReadFile` 只有 owner-or-public 二值逻辑 | `internal/repository/file_share.go`, `domain/access.go`, `service/file_presign.go` |
| 2 | **数据导出（GDPR）** | 三个实体都有 `ListByOwner` 但无聚合导出；无 `export_jobs` 表/端点/outbox 事件 | `internal/*/repository.go` 的 List 方法族 |
| 3 | **回收站自我管理** | 全系统 `DeletedAt` 软删但无 `Restore` API、无 `?trashed=true` 过滤、无 "永久删除" 端点 | `domain/*.go` 的 `DeletedAt` 字段，`repository/*.go` 的 Delete 方法 |
| 4 | **CDN 边缘分发** | `FileStorage` 接口无 `PurgeCache`/`CDNSignedURL`；`PresignGet` 返回原生 S3 URL 而非 CDN 域名包裹 | `internal/storage/storage.go`, `s3.go`, `local.go` |
| 5 | **活动提要** | 三个实体 List 各自独立；通知系统是 push 非 log；outbox 事件 relay 消费后丢弃而不持久化为活动记录 | `internal/service/service.go` 三个独立接口，`outbox/outbox.go`, `domain/audit.go` 事件类型集合 |

**与既有分析的关键差异点** —— 已核对，确实在既有 110+ 份文档中未被覆盖。特别是：
- 方向一区分了 "身份绑定分享" vs "匿名外部分享"
- 方向三识别出软删**实现**与**用户可管理性**之间的缺口
- 方向四完全跳出了功能域，进入基础设施传输层
- 方向五提出通知与动态是互补而非替代

**您希望我基于这些方向做什么？** 比如：
- 对某个方向做更深入的设计（表结构、接口签名、迁移方案）？
- 检查既有代码中是否有被遗漏的相关资产？
- 评估各方向与 `AGENTS.md` 约束（500 行限制、cyclomatic complexity ≤10 等）的兼容性？
- 或开始实现某个方向（按建议优先级：分享链接 → 回收站 → 活动提要 → 导出 → CDN）？
