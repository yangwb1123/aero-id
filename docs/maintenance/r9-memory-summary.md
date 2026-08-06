记忆索引已核实完毕（recent/find + 读取索引中记录的会话产物 `r3-bugfix-report.md`、`r6-f1-fix.md`、r5 审查 `security_engineer.md`、`ROUNDS.md`）。仅陈述索引中有证据的事实如下。

# 2026-08-05 起维护周期：审计链 / 区域 fencing 真实缺陷修复清单

周期内相关记录：**R3 轮**（会话 `aero-maint-r3`，`pb-20260806015741-572831-1`，PASSED 2026-08-06T02:07:57，产出 `docs/maintenance/r3-bugfix-report.md`）修复 7 个缺陷；**R5 安全审查**发现 F1，**R6 轮**（会话 `aero-maint-r6`，`pb-20260806055705-3163098-1`，PASSED，产出 `docs/maintenance/r6-f1-fix.md`）修复。此前 r6 有两次 resume 超时、一次 validation 失败，最终成功。

## 一、审计锚定链（`internal/service/audit_anchor.go`）— R3

**缺陷 A1：锚定链从未真正链接（stub 函数）**
- 证据：`audit_anchor.go:136-153` `latestAnchorHash`，函数体只算 `prefix` 后无条件 `return ""`（`_ = prefix` 死代码），导致每个锚对象 `previous_anchor_hash: ""`，P4-03"chained to previous anchor"契约从未实现，篡改锚 N-1 无法被锚 N 发现。
- 修复：新增 `previousAnchorHash()` 按内容枚举（复用 `anchorObject` 校验 kind/source/partition），取 `chain_sequence` 严格小于当前 head 的最大序列的 `anchor_hash`；无 Lister 存储降级为空（文档化）；枚举失败 fail-closed 中止锚定。
- 验证：`TestAuditAnchor_ChainsToPreviousAnchor`，旧代码上确定性 FAIL（`second anchor previous hash = , want ...`），修复后通过。

**缺陷 A2：`AnchorManifest` 返回"第一个被列出的"锚而非最新**
- 证据：旧代码 `audit_anchor.go:206-209`（`if document != nil && newest == nil`）；`memoryStore.List` 按 Go map 随机序、S3 按字典序（`…-10.json` 排在 `…-2.json` 前），函数注释声称返回 newest 实际返回任意/最旧。
- 修复：按 `chain_sequence` 数值取最大。
- 验证：`TestAuditAnchor_ManifestReturnsNewestAnchor`（含 `orderedAnchorStore` 字典序陷阱），旧代码 FAIL（返回 `chain_sequence:1`）。

**缺陷 A3：TOCTOU——锚定了未经验证的链头**
- 证据：旧代码 `audit_anchor.go:62-70` 先 `VerifyChain` 后 `ChainHead`，两次调用间若有新 fact 追加，写入锚对象的是从未被验证过的头，静默绕过"broken chain 不得锚定"的 fail-closed 保证。
- 修复：新增 `verifiedHead()`：读 head → 验证 → 重读 head，序列+哈希不一致重试（最多 3 次），持续变化返回 `audit_anchor.chain_busy` 拒绝锚定。
- 验证：`TestAuditAnchor_AnchorsOnlyVerifiedHead`（`advancingChainLedger` 模拟验证期间 head 前进），旧代码 FAIL（`chain verified 1 times, want >= 2`）。

**缺陷 A4：锚 key 文件名段未消毒（路径逃逸）**
- 证据：旧代码 `audit_anchor.go:100-104`，前缀段用 `sanitizeSource/sanitizePartition`，文件名段却用原始 `head.Source + "-" + head.PartitionKey`；partition 含 `/`（如 `tenant/a`）或 `..` 时 key 逃出 `audit-anchor/…` 前缀（key 型后端路径穿越）。
- 修复：抽出 `anchorKey()` 全段统一消毒；`json.Marshal` 裸错误包为 `audit_anchor.encode_failed`。
- 验证：`TestAuditAnchor_SanitizesKeySegments`，旧代码 FAIL（key 含原始 `tenant/a` 段）。

## 二、区域 fencing（`internal/middleware/region_epoch.go` + `internal/repository/region_directory.go`）— R3

**缺陷 B1：缓存降级竞态——过期响应可覆盖新 generation**
- 证据：旧代码 `region_epoch.go:96-108` 无条件 `Store`；TTL 过期后并发刷新，较慢的 goroutine 持旧 epoch 却带更晚 `cachedAt`，写入后旧值再服务一个完整 TTL。durable epoch 严格递增（`region_directory.go:150` `newEpoch = expectedEpoch + 1`），旧响应永远不该获胜——客户端 pinned 旧 generation 读到新 region 却不被标记 stale。
- 修复：单调 CAS 循环——`epoch < cached` 保留缓存中更新值及其新鲜度，否则 CAS 写入，任何交错收敛到最新值。
- 验证：`TestRegionEpochHeader_StaleResponseCannotDowngradeCache`（旧代码 FAIL：`epoch after stale refresh = 1, want cached 2`）+ `TestRegionEpochHeader_ConcurrentRefreshStaysMonotonic`（16 goroutine，`-race` 通过）。

**缺陷 C1：`PromoteWithFencing` 降级共享 region epoch 行（split-brain 风险）**
- 证据：旧代码 `region_directory.go:142-151`，`newEpoch = expectedEpoch + 1` 后无条件 upsert；`region_epochs` 行按 region 主键、所有账号共享。低 epoch 账号晋升会把行写成 1：① 当前合法 writer 的 `RegionEpoch(home) == FencedEpoch` 守卫失败被错误 fence out（可用性）；② 行被降回旧值后，已被 fenced 的旧 generation 相等性检查再次通过——旧 writer 复活、双 region 同时写 = split-brain（违反 P6-02）。
- 修复：`newEpoch = max(target.Epoch, expectedEpoch) + 1`，region 行单调不减。
- 验证：集成测试 `TestRegionDirectoryRegionEpochsNeverDowngrade`（真 Postgres，integration tag），旧代码 FAIL（`second promote epoch = 1, want 4`）。

**缺陷 C2：`EnsureDirectory` 每次新建账号把共享 region epoch 行重置为 0**
- 证据：旧代码 `region_directory.go:89-91`，`upsertRegionEpoch(tx, homeRegion, 0, now)`，OnConflict `DoUpdates` 无条件覆盖；新账号在已有 region 建目录行时把共享行覆盖为 0，fence 掉该 region 内所有 `FencedEpoch > 0` 的 writer。
- 修复：新增 `ensureRegionEpochRow()`，ON CONFLICT DO NOTHING——行不存在才建（初始 0），存在绝不触碰。
- 验证：与 C1 同一集成测试覆盖。

## 三、审计链验证（`internal/repository/audit_ledger.go`）— R6（F1 漏洞修复）

**缺陷 F1：签名降级绕过（HMAC strip）**（R5 安全审查评级 Medium，审查者已用真实 PostgreSQL 复现）
- 证据（审查报告）：`record_hash` 是纯公开函数、不绑定 `SignatureVersion`；`VerifyChain` 对链中任意 version-0 行跳过 HMAC 且无单调性检查（`audit_ledger.go:331-334, 379-405`）。探针实测：单独降级通过、篡改+降级+重链通过、对照组（篡改不降级）被捕获——即 DB 写者可篡改行→重算哈希→重链→剥 HMAC→降级 `signature_version=0`，验证通过。
- 修复：`VerifyChain` 按 `chain_sequence ASC` 遍历时维护 `lastSignatureVersion`；新增 `verifyAuditRow`，在 payload/链链接/HMAC 校验外强制执行单调 version 检查（`fact.SignatureVersion < lastSignatureVersion` → `audit signature version downgrade` 失败）；遗留 `0,0,1,1`（签名前未签名行在前）保持可验证。攻击面闭合逻辑：篡改第 k 行必须重链后续行，后续行 HMAC 无法伪造，第一个被降级行必然触发 `0 < lastSignatureVersion`。
- 验证：单测 `TestVerifyAuditRowRejectsSignatureVersionDowngrade`（篡改+重链+剥 HMAC+version 1→0 必须失败）、`TestVerifyAuditRowAcceptsLegacyUnsignedPrefix`（0,0,1,1 必须通过）；PostgreSQL 集成 `TestAuditLedgerVerifyChainRejectsSignatureVersionDowngrade`（禁用 append-only 触发器模拟 DB 写者，F1 探针转正）、`TestAuditLedgerVerifyChainAcceptsLegacyUnsignedPrefix`。全部门禁绿：`make test`（34 包）、`lint`、`lint-strict`、`check-file-sizes`（audit_ledger.go 499/500 行）、`build`、聚焦集成 11/11、`make test-integration`。

## 四、周期门禁与残余风险（索引中记录的）

- R3 修复后全门禁通过：`make test` 34 包（`-race` 下 service/middleware 通过）、`lint`、`lint-strict`（期间 `PromoteWithFencing` 一度 cyclop=11，改 Go 内置 `max` 后回 10）、`check-file-sizes`（audit_anchor.go 275 行）、`build`、真 Postgres 集成。
- R6 报告明确记录的残余风险（未编造为已修复）：整条链全部降级为 version 0 与纯遗留链字节不可区分（需把 SignatureVersion 绑定进 record_hash + 锚定签名链头，属单独设计变更）；F2（无界全链 VerifyChain 扫描、限流默认关）、F4（锚 manifest 未按租户隔离）、F5（占位签名 key 通过长度检查）仍开放，不在 F1 任务范围内。
- 补充语境（ROUNDS.md 索引）：R4 轮 gate 曾因 F5 等 4 个带证据阻断项 FAIL fail-closed；R5 实现首轮 900s 硬超时零落盘、`--reuse --timeout 2400` 续跑完成——这些是周期内记录，非本次缺陷修复本身。
