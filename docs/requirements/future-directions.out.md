感谢分享这份详尽的架构分析文档！我仔细阅读了所有五个方向。以下是我的初步观察和反馈：

## 文档质量评价

这份分析质量非常高——每个方向都明确引用了**当前已有的基础设施**（代码、表结构、interface），并给出了具体的实现路径和边界情况。不是泛泛的路线图，而是可落地的工程方案。

## 几个值得探讨的问题

### 方向二：存储配额 —— 乐观锁风险

文中提到 `UPDATE … SET used_bytes = used_bytes + ?` 的原子更新，但并发上传时如果两个请求同时读取 `used_bytes` 再写回，会出现 race condition。SQL 层面的 `used_bytes = used_bytes + ?` 本身就是原子的（PostgreSQL 单条 UPDATE 是 MVCC 安全的），所以不需要应用层乐观锁——只要在同一个事务里做 `UPDATE storage_quotas SET used_bytes = used_bytes + ?` + 检查是否超过 quota 即可。建议在文档中明确这一点。

### 方向一：群组的大规模通知积压

文中提到批量 insert + 限流，我建议参考当前 `outbox.Relay` 的 `BatchSize` 配置模式——dispatcher 直接使用 `COPY` 或批量 INSERT 写入 notification rows，而不是逐条写入。当前 `NotificationDispatcher.dispatch` 可能已经支持批处理（需要确认代码），如果支持，群组场景直接复用即可。

### 方向四：Push + SSE 的扇出模式

`dispatcher → [sse, push, webhook]` 的链式扇出架构与当前 `outbox.NewChain` 模式完全一致。但需要注意：SSE 推送失败（用户离线）不应阻塞 push 投递。当前 pusher.Publish 返回 error 时 dispatcher 如何处理？如果直接返回 error 导致 outbox 重试，会重复创建 notification rows。建议在方向四中明确**SSE 推送失败不影响通知创建**这一语义。

## 想确认你的目标

你是希望我：
1. 对文档内容进行审查/补充？
2. 选定某个方向开始实现（比如你推荐的 **P0 存储配额**）？
3. 读取相关源码，验证文档中的引用是否准确？
4. 或者这是作为项目文档提交 review 的？

请告诉我下一步做什么！
