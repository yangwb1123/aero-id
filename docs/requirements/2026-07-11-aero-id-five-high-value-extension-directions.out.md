这是一个非常详实、结构清晰的架构分析报告。交叉验证了 92 份既有分析、逐文件扫描代码库、每个方向都附有具体的代码证据链——质量很高。

我看到你目前在 `docs/requirements/` 目录下，这份文档应该作为第 93 份分析保存进去。你要我帮你保存在这个目录下吗？比如命名为 `2026-07-11-five-extension-directions.md` 或类似的命名方式？

另外，对于你提出的 **五个方向**，我有些想法可以补充/讨论：

### 关于方向五（多实例并发安全）——我认为 P0 的判断完全正确
`FOR UPDATE SKIP LOCKED` 确实是成本最低、收益最高的单行改动。不过我注意到 outbox repository 中还有一个潜在问题：`MarkPublished` 使用的是 `WHERE id IN (?)` 批量更新，在多实例场景下，实例 A 已经 SKIP LOCKED 拿到事件并处理完，但实例 B 可能在 A 完成 `MarkPublished` 之前也拿到了（旧版本代码如果没有 SKIP LOCKED）。加上 SKIP LOCKED 后这个窗口就闭合了。

### 关于方向一（断点续传）——FileStorage 接口扩展的隐含成本
`FileStorage` 当前的 `local` 后端实现如果也要支持 Multipart Upload，需要引入本地文件分片合并逻辑——这会增加 `local` 后端的维护成本。一个可能的折中是：**仅在 S3 后端实现 MultipartUpload 接口**，`local` 后端返回 `ErrNotSupported`，上传时 fallback 到单次上传。这样接口扩展的破坏性最小。

### 关于方向二（计数一致性）——outbox event 的类型选择
你提到用 `comment.deleted` outbox 事件来异步递减 `reply_count`。这里有一个细节：当前 `SoftDelete` 是在 repository 层的，而 outbox 是在 service 层写入的。如果用 service 层的 `SoftDeleteWithEvent`（类似 `CreateWithReplyBump` / `MarkReadWithEvent` 的模式），需要确保事务性——软删除和 outbox 事件在同一个 DB 事务中。当前 `MarkReadWithEvent` 已经实现了这个模式，可以作为参考。

---

你觉得哪个方向可以先开始着手？还是希望先把这份文档保存下来？
