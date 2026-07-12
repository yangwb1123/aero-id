已将完整的架构扫描分析文档写入 `/home/u1/aero-id/docs/requirements/architecture-scan-2026-07-11.md`。

文档覆盖：

- **方向一：推送通知通道** — FCM/APNs 集成分析，含设备注册、适配器插入点、静默时段兼容
- **方向二：死信队列运维** — `FetchDeadLetters`/`ReplayEvent`/`PurgeDeadLetters` API 设计，告警接入
- **方向三：群组消息** — `conversations` + `conversation_members` 表设计，block 语义，通知 fan-out
- **方向四：文件版本与内容管道** — `file_versions` 表、缩略图生成、病毒扫描、GC 扩展
- **方向五：活动流** — `activity_entries` 表、follow 模型、fan-out 策略对比、block 过滤
- **优先级矩阵** — 五项方向的 P0/P1/P2 划分 + 4 个快速获胜项（1-2 天完成）
- **技术债务附录** — 7 项次级问题标注严重度
