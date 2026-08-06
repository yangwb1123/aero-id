# aero-id 维护周期记录（ai-batch-runner 驱动）

> 10 轮执行，覆盖全部特性。每轮记录：特性、命令、结果、证据。

- **R1 需求评估栈**：`assess`（production 处方 12 规则）+ `classify`（backend 路由）+ `rules --check`（注册表 schema OK）
- **R2 规则双向校验**：`rules --summary/--llm-json`（必选规则 LLM 跳过被算法否决 `algorithm-vetoed-llm-skip`；坏 JSON fail closed）+ `eval`（27/27 PASS）+ `check`（全门禁过）
- **R3 单批修复**：`-p` + `--classify` + `--validate gobuild,gotest`，真 bug 修复
- **R3 单批修复**：`-p` + `--classify` + `--validate gobuild,gotest`（go build+vet+test 门禁，失败零落盘）+ `--decision-log` + 稳定会话；修复 7 个真实缺陷：audit anchor 链从未链接（stub）、AnchorManifest 返回任意锚、TOCTOU 锚定未验证链头、锚 key 路径逃逸、region epoch 缓存降级竞态、PromoteWithFencing 降级共享 epoch 行（split-brain 风险）、EnsureDirectory 重置共享行。门禁全绿。
- **R4 campaign**：4 模块并行分析（12 个证据方向，结构化 JSON）→ 选中方向跑完整流水线（requirements→design→meta 对抗 3 角色→design_gate）；**gate FAIL fail-closed 阻断**——4 个阻断项全部带证据（含 F5：audit_ledger.signing_key 默认空 → VerifyChain 静默跳过验证）。安全设计验证：campaign 拒绝仓库外资源（role_dir 逃逸）。
- **R5 审计链加固流水线**：plan（agent 实测验证两条主张，纠正方案：jsonb 规范化 → 用 canonical form 而非原始字节）→ implement（首轮 900s 硬超时零落盘，代码部分完成且编译通过；`--reuse --timeout 2400` 续跑）→ 对抗审查 → 裁决门。
- **R7 advance**：8 维度扫描，50 条发现（be_errors 18 / fe_complexity 28 / be_architecture 4），状态记入 docs/advance/state.jsonl（可续跑）；webhook.go 跨租户样本经核对属 AGENTS.md 明示未接线死树，不适用。
- **R8 learn**：R4 gate FAIL 事故 → docs/rules/drafts/AERO-001-20260805.yaml（fail-open 安全默认 + 原子变更拆分，RCA 已填）。
- **R5 审计链加固（续）**：implement 两次被 completion 门禁拒绝（我给的格式指引错误——验证器要求 changed_files/commands_executed/residual_risks/assumptions 必填），修正契约后通过；meta 对抗审查（testing_reviewer + security_engineer 两轮编排）→ **裁决门 FAIL**：F1 签名降级绕过被独立复现（真实 PG17 探针，record_hash 不绑定 signature_version）。**R6 单批修复 F1**（单调 version 检查 + 降级回归测试 + 0,0,1,1 前缀兼容，门禁+完成报告契约）→ 重跑流水线（删 FAIL gate 产物强制重审）→ **gate PASS** → 归档 3 份交付物。git 每阶段提交。
- **R9 memory**：ingest 1322 条历史消息 → recent/find/read；`--memory-mode on` 任务产出证据化缺陷清单（R3/R5/R6 全部命中，未编造）。
- **R10 最终验收**：完整 done gate + eval 套件 + 提交。
