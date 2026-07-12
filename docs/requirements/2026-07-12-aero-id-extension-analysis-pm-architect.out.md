文件已保存至 **`docs/requirements/2026-07-12-aero-id-extension-direction-analysis.md`** (9.7 KB)。

---

这个分析的质量很高。几个观察：

**亮点：**
1. **#1 上传 Checksum Gap** —— 精准命中 `CompleteUpload` 里的 `// Dedup is skipped (no checksum without downloading)` 注释。这是文件域唯一的数据完整性的缺口。
2. **#4 分布式速率限制** —— 正确识别了 `RateLimit.RPS/Burst` 配置和内存 token bucket 的多副本失效问题。当前 `ratelimit.Limiter` 接口确实可以新增 Redis 实现，零侵入。
3. **每个方向的 Edge Case 表** —— 对生产落地非常有价值（特别是 #4 的 Redis 降级、#5 的写扩散与退群清理）。

**可以补充的几个交叉点（已存在于代码中但分析未提及）：**
- #1 的 checksum 验证可以与**审计日志**联动——`audit_logs.detail` 已经是 JSONB，checksum 匹配/不匹配可以记录为 `checksum_verified: true/false` 字段。
- #3 消息附件 + #5 对话抽象之间有耦合——如果先做 #5（conversation 表），`message_files` 快照表的设计可以省掉 `from_uid`/`to_uid` 字段，直接关联 `conversation_id`。
- #2 的设备注册表与当前的 **SSE 连接**可以打通——`uid → []*subscription` 的 hub 结构稍加扩展就能绑定 `device_id`，实现"按设备推送"而非"广播给所有该 uid 的连接"。

**推荐实施顺序方面**，#1 和 #4 定为 P0 是合理的判断——#1 是数据正确性，#4 是多副本安全的前提条件。#5 排 P2 也是对的，因为它的 schema 变更量最大，且现有点对点消息在短期不影响用户体验。
