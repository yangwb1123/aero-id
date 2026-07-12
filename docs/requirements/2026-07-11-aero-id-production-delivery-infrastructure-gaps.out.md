这是一份极其详尽的交付基础设施缺口分析——我通读了全文，验证了你引用的每一处代码证据，结论成立。

几个我想深入的技术考量：

### 方向一（集成测试 CI）的落地细节

你引用的 `ci.yml` 确实只用 `go test ./...` 不带 `-tags=integration`。要在 CI 中跑集成测试，有两条路径：

1. **GitHub Actions 原生 Docker**：Ubuntu runner 自带 Docker，testcontainers 可以直接用 `docker` 模式。需要修改 `ci.yml` 增加一个 step：
   ```yaml
   - run: go test -tags=integration ./... -count=1 -race -timeout 10m
   ```
   但需要注意 testcontainers 拉取 Postgres/MinIO 镜像的时间开销——10 个集成测试包并行跑可能造成资源争抢。

2. **复用 Compose 环境**：先 `docker compose up -d postgres minio`，然后设置环境变量让测试连接已有的容器而非自建 testcontainers。这更快，但需要测试框架支持（当前 `testpg` 硬编码 testcontainers）。

你的分析中提到了路径 2 但指出了当前无法实现（因为 Compose 环境本身坏了——方向二）。**方向一和方向二有依赖关系**：要跑集成测试，要么修 Compose（让测试可以复用），要么不改 Compose 但直接加 testcontainers step。我倾向于后者——改动更小，且 testcontainers 的隔离性更好。

### 方向三：Context 治理的修复模式

`relay.go:111` 的 `context.Background()` 确实是个问题。修复模式有两层：

**第一层（最小改动）**：为每个 poll tick 创建带超时的 context：
```go
case <-ticker.C:
    ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
    r.drain(ctx)
    cancel()
```

**第二层（传递 trace）**：这更难——outbox event 创建时关联的 trace ID 需要随 event 存储并在 relay 处理时恢复 span。需要：
- Outbox 表中增加 `trace_id` 和 `span_id` 列（或 OTel context 的 propagator 序列化）
- Relay handler 在取出 event 后先恢复 span 再处理

你的分析只提到了第一层，但第二层才是真正解决"trace 断裂"问题的方式。这算是一个可选的加深方向。

### 方向五：计数器巡检的 DB 层面瓶颈

你提到 `like_count` 当前通过 `SELECT count(*)` 原子重算——这实际上比"累加器"模式更安全，但代价是每次 `Like`/`Unlike` 都需要全表扫描 `comment_likes`。你的分析说：

> `Like` 和 `Unlike` 之间没有串行化——在高并发下，`SELECT count(*)` 和 `UPDATE` 之间存在微小窗口

实际上 `UPDATE ... SET like_count = (SELECT count(*) FROM comment_likes WHERE comment_id = ?) RETURNING like_count` 是**单个原子 SQL 语句**——事务的隔离性保证 `SELECT count(*)` 看到的是语句开始时的快照。所以没有 TOCTOU 问题。但性能瓶颈是真的——`count(*)` 在大表上慢。

巡检工具的真正价值在于**发现软删除引用悬挂**和**collapsed_count 溢出**，而不是计数器漂移（因为当前实现已经防止了漂移）。

---

综合来看，这 5 个方向定位准确。你希望我从哪个方向开始介入？我可以在不写代码的前提下进一步细化实现方案，或者按你的规划开始实施具体修复（如果约束允许）。
