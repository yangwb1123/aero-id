提案已完成并通过校验：`docs/proposals/audit-contract-batch-aero-id.md`（205 行，未修改任何代码）。

## 核心发现（实证核对，commit `756b65b`）

**清单路径准确性**：B2 全部 6 项的路径均 Verified，且行号精确（account.go:155,184 / callback.go:160,221,247 / audit_hold.go:59,74 / legacy_cutover.go:88）。关键机制已存在可复用：`AppendInTx` + `XxxWithAudit` 仓库模式、`audit_publications` durable outbox worker（`drainPublications`）、`(tenant_id, source, event_id)` 本地去重、激活门 fail-closed 校验（`validateAuditBindings`）。

**必须标注的未验证项（不冒充 Verified）**：
1. **"49 个 `_ =` 站点"无法复现** — 实测 raw `_ =` = 142 站点、`_ = s.audit.Record/Log(` = 25 站点；SEC-V2-7 原始 grep 在本仓无存档。提案定义了可执行的 `make check-audit-swallow` 门禁模式。
2. **§1.9 event_id 派生公式**在外部 v2 契约仓，本仓无正文 — 按 vault B3-3 同构设计，标 `[PROPOSED]`，实施前须拉取核对。
3. **清单遗漏第 9 个 Record 点**：`account_provision.go:168`（live 树）。
4. T-4/T-5/T-10.1/T-10.2 完整规程在外部审查文档，本仓仅有 gate 摘要。

## 设计要点
- **B2-1**：6(+1) 写路径移入 `XxxWithAudit`（同 tx 原子提交/回滚，T-5 "操作失败"分支）；2 读路径保留 `Record` + 可观测（counter 递增分支）
- **B2-2**：删 `Record` 的 inline publisher 循环（实证为未接线的死路径），唯一投递 = 既有 `drainPublications` outbox worker
- **B2-3**：`TokenSource` 加 `Invalidate`，Publish 遇 401 立即失效 + epoch 防单飞回填；403 scope-missing 分类告警不循环
- **B2-5**：`RequeueDead`/`RequeuePublication` + 新 `cmd/audit-replay` 工具 + purge 暂停标志；注意 `repository/audit_ledger.go` 已 499 行，新增放 sibling 文件
- **B2-6**：空 bindings + enabled → boot 失败**已实现**，补测试锁定 + 首个事件 e2e 编排

**顺序**：B2-4（独立）→ B2-1 → B2-2 → B2-3 → B2-5 → B2-6；G3 完整通过以 G1（P0 信任路径）为前提，但 B2-1 可先于 B1 启动。
