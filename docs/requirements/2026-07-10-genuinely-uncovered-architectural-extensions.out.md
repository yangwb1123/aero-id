文件已保存至 `/home/u1/aero-id/docs/requirements/2026-07-11-aero-id-truly-uncovered-architecture-expansion-directions.md`（12.6 KB）。

内容涵盖 5 个方向：

1. **多人群聊与协作空间** — 从 `Message.ToUID` 单值到 `conversations`/`conversation_members` 实体
2. **数据生命周期管理** — 消息/评论 GC 缺失、保留策略、合规删除、数据导出
3. **实时状态与活动感知** — SSE Hub 未用于 presence、无输入指示器、无活动 feed
4. **策略化文件访问控制** — Share 模型二元无过期、无分享链接、无目录级分享
5. **跨实体订阅与通知路由** — Dispatcher switch 硬编码、仅 SSE 渠道、无用户级订阅

优先级排序：P0 方向二（数据生命周期）+ 方向三（实时状态）；P1 方向一（群聊）+ 方向四（文件策略）；P2 方向五（订阅路由）。
