All gates green. Here's the report.

---

# 审查报告：audit anchor 与 region epoch fencing

## 一、缺陷清单（每条附证据）

### A. `internal/service/audit_anchor.go`

**A1. 锚定链从未真正链接（stub 函数）**
- 位置：`audit_anchor.go:136-153` `latestAnchorHash`
- 证据：函数注释声称"re-reading the anchor for the previous sequence deterministically"，但函数体只计算了一个 `prefix` 后无条件 `return ""`（`_ = prefix` 是死代码）。因此每个锚对象都写入 `previous_anchor_hash: ""`，P4-03"chained to the previous anchor"契约从未实现。后果：篡改锚 N-1 无法被锚 N 发现，锚对象退化为孤立快照，tamper-evidence 链形同虚设。
- 修复：用 `previousAnchorHash()` 按内容枚举（复用 `anchorObject` 校验 kind/source/partition），取 `chain_sequence` 严格小于当前 head 的最大序列的 `anchor_hash`；无 Lister 的存储降级为空 previous hash（文档化）；枚举失败则 fail-closed 中止本次锚定。

**A2. `AnchorManifest` 返回"第一个被列出的"锚，而不是最新的**
- 位置：`audit_anchor.go:206-209`（旧代码 `if document != nil && newest == nil { newest = document }`）
- 证据：`memoryStore.List` 按 Go map 随机序迭代；S3 按 key 字典序，`…-10.json` 排在 `…-2.json` 之前。函数注释声称"returns the newest stored anchor"，实际返回的是任意/最旧的。
- 修复：按 `chain_sequence` 数值取最大者。

**A3. TOCTOU：锚定了未经验证的链头**
- 位置：`audit_anchor.go:62-70`（旧代码先 `VerifyChain` 后 `ChainHead`）
- 证据：两次调用之间若有新 fact 追加，读到的 head 从未被 `VerifyChain` 覆盖，却仍被写入锚对象——"A broken chain must not be anchored silently"的 fail-closed 保证被静默绕过（验证通过的是旧头，锚定的是新头）。
- 修复：新增 `verifiedHead()`：读 head → 验证 → 重读 head，序列+哈希不一致则重试（最多 3 次）；持续变化则返回 `audit_anchor.chain_busy` 错误，拒绝锚定未验证头。

**A4. 锚 key 文件名段未消毒（路径逃逸）**
- 位置：`audit_anchor.go:100-104`（旧代码）
- 证据：路径前缀段用了 `sanitizeSource/sanitizePartition`，文件名段却用原始 `head.Source + "-" + head.PartitionKey`。partition 含 `/`（如 `tenant/a`）或 `..` 时 key 逃出 `audit-anchor/…` 前缀（key 型后端可造成路径穿越），并破坏 key 约定。
- 修复：抽出 `anchorKey()`，所有段统一消毒；顺带把 `json.Marshal` 的裸错误包成 `audit_anchor.encode_failed`（服务边界不裸传错误）。

### B. `internal/middleware/region_epoch.go`

**B1. 缓存降级竞态：过期响应可覆盖新 generation**
- 位置：`region_epoch.go:96-108`（旧代码 `h.cachedEpoch.Store(epoch); h.cachedAt.Store(now)` 无条件写入）
- 证据：TTL 过期后并发刷新，两个 goroutine 都调 provider；较慢的响应持有旧 epoch 却带更晚的 `cachedAt`，写入后缓存会以旧值再服务一个完整 TTL。由于 durable epoch 严格递增（`region_directory.go:150` `newEpoch = expectedEpoch + 1`），旧响应永远不该获胜。后果：客户端 pinned 旧 generation 读到晋升后的新 region，因缓存仍返回旧 epoch 而**不被标记 stale**（read-your-writes 信号错误；写路径仍由 durable fencing 兜底，但这是真实竞态）。
- 修复：单调 CAS 循环——`epoch < cached` 时保留缓存中的更新值并保留其新鲜度；否则 CAS 写入。任何交错都收敛到最新值，且只有新值能延长 TTL。

### C. `internal/repository/region_directory.go`（fencing epoch CAS 的持久层，middleware 读的就是它）

**C1. `PromoteWithFencing` 会降级共享的 region epoch 行**
- 位置：`region_directory.go:142-151`（旧代码 `newEpoch = expectedEpoch + 1` 后无条件 upsert）
- 证据：`region_epochs` 行按 region 主键、**所有账号共享**。低 epoch 账号（`expectedEpoch=0`）晋升进 row=3 的 region 时会把行写成 1。后果：① 当前合法 writer 的守卫 `RegionEpoch(home) == FencedEpoch` 失败，被错误 fence out（可用性）；② 行被降回旧值后，同一账号**已被 fenced 的旧 generation** 的相等性检查再次通过——旧 writer 复活，双 region 同时写 = split-brain（违反 P6-02 核心保证）。
- 修复：`newEpoch = max(target.Epoch, expectedEpoch) + 1`——region 行单调不减，目录 fenced 与行值保持一致。

**C2. `EnsureDirectory` 每次新建账号都把 region epoch 行重置为 0**
- 位置：`region_directory.go:89-91`（旧代码 `upsertRegionEpoch(tx, homeRegion, 0, now)`，OnConflict `DoUpdates` 无条件覆盖）
- 证据：新账号在已有 region 创建目录行时，把共享行覆盖为 0，fence 掉该 region 内所有 `FencedEpoch > 0` 的 writer。
- 修复：新增 `ensureRegionEpochRow()`，ON CONFLICT DO NOTHING——行不存在才建（初始 0），存在则绝不触碰。

## 二、修复内容

| 文件 | 改动 |
|---|---|
| `internal/service/audit_anchor.go` | `verifiedHead()`（验证后重读，防 TOCTOU）；`previousAnchorHash()`（真实链式解析，替代 stub）；`anchorKey()` 全段消毒；`AnchorManifest` 按最大序列选最新；marshal 错误包装为 AppError |
| `internal/middleware/region_epoch.go` | `currentEpoch` 单调 CAS 刷新，过期响应不能降级缓存 |
| `internal/repository/region_directory.go` | 晋升取 `max(row, expected)+1`；`ensureRegionEpochRow` 仅建行不覆盖 |

## 三、新增测试

| 测试 | 覆盖缺陷 | 旧代码验证 |
|---|---|---|
| `TestAuditAnchor_ChainsToPreviousAnchor` | A1 | 旧代码 FAIL：`second anchor previous hash = , want ...` |
| `TestAuditAnchor_ManifestReturnsNewestAnchor`（含 `orderedAnchorStore` 字典序陷阱） | A2 | 旧代码 FAIL：返回 `chain_sequence:1` |
| `TestAuditAnchor_AnchorsOnlyVerifiedHead`（`advancingChainLedger` 模拟验证期间 head 前进） | A3 | 旧代码 FAIL：`chain verified 1 times, want >= 2` |
| `TestAuditAnchor_SanitizesKeySegments` | A4 | 旧代码 FAIL：key 含 `tenant/a` 原始段 |
| `TestRegionEpochHeader_StaleResponseCannotDowngradeCache` | B1 | 旧代码 FAIL：`epoch after stale refresh = 1, want cached 2` |
| `TestRegionEpochHeader_ConcurrentRefreshStaysMonotonic`（16 goroutine，-race） | B1 CAS 路径 | — |
| `TestRegionDirectoryRegionEpochsNeverDowngrade`（integration, 真 Postgres） | C1/C2 | 旧代码 FAIL：`second promote epoch = 1, want 4` |

## 四、验证结果

- `make test`：34 个包全部 ok（含新增测试，`-race` 下 service/middleware 通过）
- `make lint`（go vet）：通过
- `make lint-strict`（golangci-lint，funlen 50/cyclop 10）：通过（修复过程中 `PromoteWithFencing` 一度 cyclop=11，改用 Go 内置 `max` 后回到 10）
- `make check-file-sizes`：全部 ≤500 行（audit_anchor.go 275 行）
- `make build`：通过
- `go test -tags integration ./internal/repository/ -run TestRegionDirectory`：新旧测试均通过（新测试在旧代码上确定性失败，已还原验证）

未改动无关代码；6 个文件均为目标文件及其直接测试。
