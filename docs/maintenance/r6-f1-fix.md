Validator passes. All work complete — summary:

## 变更说明

**漏洞（F1）**：`internal/repository/audit_ledger.go` 的 `VerifyChain` 中，`record_hash` 是纯公开函数且不绑定 `SignatureVersion`；per-row version-0 跳过 HMAC 校验且无单调性检查。DB 写者可篡改行 → 重算哈希 → 重链 → 剥 HMAC → 降级 `signature_version=0`，验证通过（审查者已用真实 PostgreSQL 复现）。

**修复**：
- `VerifyChain` 按 `chain_sequence ASC` 遍历时维护 `lastSignatureVersion`；
- 新增 `verifyAuditRow`：payload/链链接/HMAC 校验外，强制执行**单调 version 检查**（`fact.SignatureVersion < lastSignatureVersion` → `audit signature version downgrade` 失败）；
- 遗留前缀 `0,0,1,1`（签名前未签名行在前）保持可验证；
- 攻击面闭合：篡改第 k 行必须重链后续行，后续行 HMAC 无法伪造，第一个被降级行必然触发 `0 < lastSignatureVersion`。唯一残余是把整条链全部降为 0（与纯遗留链字节不可区分），已记录为 residual risk。

**回归测试**：
- 单测（`make test` 内，无需 Docker）：`TestVerifyAuditRowRejectsSignatureVersionDowngrade`（篡改+重链+剥 HMAC+version 1→0 必须失败）、`TestVerifyAuditRowAcceptsLegacyUnsignedPrefix`（0,0,1,1 必须通过）；
- PostgreSQL 集成：`TestAuditLedgerVerifyChainRejectsSignatureVersionDowngrade`（禁用 append-only 触发器模拟 DB 写者，F1 探针转正）、`TestAuditLedgerVerifyChainAcceptsLegacyUnsignedPrefix`。

## 测试结果（全部实际执行）

| 门禁 | 结果 |
|---|---|
| `make test` | ✅ 34 包 ok，0 FAIL |
| `make lint` (go vet) | ✅ 通过 |
| `make lint-strict` (golangci-lint) | ✅ 通过 |
| `make check-file-sizes` | ✅ audit_ledger.go = 499/500 行；VerifyChain 20 行、verifyAuditRow 17 行（≤50） |
| `make build` | ✅ bin/aero-id |
| 聚焦集成（真实 PG17） | ✅ 11/11 PASS，含降级回归与 0,0,1,1 前缀 |
| `make test-integration` | ✅ exit 0 |

报告：`docs/maintenance/r6-f1-fix.md`，已通过官方校验器 `check-completion-report.py`（COMPLETION: OK, exit 0）。

```yaml
completion_report:
  summary: >-
    Fixed security review F1 (signature downgrade bypass) in
    internal/repository/audit_ledger.go. VerifyChain now tracks
    lastSignatureVersion across the chain and verifyAuditRow enforces a
    monotonic signature_version check (non-decreasing; legacy 0,0,1,1 prefix
    still verifies), so a DB writer who tampers a row, re-chains it, strips
    the HMAC, and downgrades signature_version from 1 to 0 fails verification
    with "audit signature version downgrade". Added unit-level regression
    (TestVerifyAuditRowRejectsSignatureVersionDowngrade) and PostgreSQL
    integration regression (TestAuditLedgerVerifyChainRejectsSignatureVersionDowngrade)
    plus the 0,0,1,1 legacy-prefix acceptance test. All gates green.
  changed_files:
    - internal/repository/audit_ledger.go
    - internal/repository/audit_ledger_test.go
    - internal/repository/audit_ledger_integration_test.go
    - docs/maintenance/r6-f1-fix.md
  commands_executed:
    - command: make test
      result: passed
    - command: make lint
      result: passed
    - command: make lint-strict
      result: passed
    - command: make check-file-sizes
      result: passed
    - command: make build
      result: passed
    - command: go test -tags integration ./internal/repository/ -run TestAuditLedger -count=1
      result: passed
    - command: make test-integration
      result: passed
  not_executed: []
  residual_risks:
    - >-
      A DB writer who downgrades the entire chain to signature_version 0 is
      byte-indistinguishable from a pre-signing legacy chain; monotonicity
      closes the proven per-row/suffix strip but not the whole-chain strip,
      which requires binding SignatureVersion into record_hash plus anchoring
      signed chain heads as a separate design change.
    - >-
      F2 (unbounded full-chain VerifyChain scan, rate limiter off by default)
      and F4/F5 from the r5 review remain open; out of scope for this task.
  assumptions:
    - >-
      Legacy chains written entirely before a signing key existed (all rows
      version 0) must remain verifiable by hash chain alone; the fix preserves
      this compatibility contract from the r5 plan.
    - >-
      The monotonic check and integration regression tests present in the
      working tree from the interrupted prior attempt were reviewed line by
      line and verified; this round added the unit-level regression and
      legacy-prefix controls and re-ran every gate on the final tree.
    - >-
      Integration tests run against real PostgreSQL via testcontainers
      (Docker available in the environment); the F1 regression disables the
      append-only trigger to simulate a DB writer, exactly as the reviewer's
      probe did.
    - >-
      Other working-tree modifications (config, middleware, event_store,
      legacy migration, etc.) belong to concurrent tasks and were not touched;
      gate results above cover the tree as it stands.
```
