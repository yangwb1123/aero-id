分析文档质量极高，我已对所有五条方向做了代码级交叉验证。以下是验证结果与修正意见。

---

## 验证结果汇总

| 方向 | 验证 | 代码证据 | 关键修正 |
|------|------|---------|---------|
| 一：端点粒度限流 | **✅ 确认** | `ratelimit.go:Allow(key)` 仅 key 维度；`router.go` 共享同实例 | 无 |
| 二：Idempotency Cleaner 无条件启动 | **⚠️ 需修正** | 见下方 | **中间件实际始终安装**，浪费论据需调整 |
| 三：下载 Content-Type XSS | **✅ 确认** | `file.go:Download` 无 Content-Type/Content-Disposition/X-Content-Type-Options | 无 |
| 四：限流在幂等前执行 | **✅ 确认** | `router.go:44-49` 注册顺序：Auth → **RateLimit** → **Idempotency** | 无 |
| 五：搜索 limit 语义 | **✅ 确认** | `search.go:100-130` 每种独立 limit；`search.go:Search` 无 per_kind_counts | 无 |

---

## 需要修正的观点：方向二

文档断言_"当用户不配置幂等键（`RouterConfig.Idempotency = nil`），中间件不安装，幂等键功能完全关闭——但 cleaner goroutine 仍每秒运行"_。但实际代码中：

**`internal/app/app.go:141-148`**:
```go
idemStore := idempotency.NewGormStore(gdb)    // ← 始终创建，始终非 nil
// ...
stopIdemClean := idemStore.RunCleaner(time.Hour)
```

**`internal/app/app.go:153-159`** → 传递给 RouterConfig：
```go
Idempotency:    idemStore,      // ← 始终传入，始终非 nil
```

**`internal/handler/http/router.go:47-49`**:
```go
if cfg.Idempotency != nil {    // ← 永远为 true（idemStore 非 nil）
    authMW = append(authMW, middleware.Idempotency(...))
}
```

**结论**：`NewGormStore` 从不返回 nil，`idemStore` 一直是有效的 `Store` 接口值，所以 `cfg.Idempotency != nil` 永远成立 → **中间件始终安装**。Cleaner 清理的是 **确实被写入** 的 `idempotency_keys` 表，不存在「不做事的 cleaner」浪费。

**建议修正为**：方向二的核心争议不是「清洁工做了无用功」，而是 **设计不一致**——rate limiter 有 `enabled` 开关来条件启动 cleaner（`app.go:133-141`），但 idempotency 整个功能**没有配置开关**，即使 operator 想关闭也不可行。这是一个**缺少配置开关**的设计问题，而非 goroutine 泄露。

按这个角度重写方向二后，优先级可从 P3 微调至 P2-P3（影响较小但暴露了配置哲学的缺口）。

---

## 其他四个方向的深度验证

### 方向一（P1）：端点粒度限流

在中间件层做简单改造（读写两桶 + SSE 豁免）约 **2-3 小时** 的工作量，对运营价值极大。当前 `per-actor single-bucket` 在搜索用户并发上传+搜索时确实会互相饿死。

一个值得补充的边界情况：gRPC 限流拦截器（`internal/handler/grpc/server.go:WithRateLimit`）同样共享同一个 `*ratelimit.Limiter` 实例——HTTP 和 gRPC 请求**共享同一桶**。这意味着 HTTP 搜索请求可以耗尽 gRPC 写请求的额度。这个跨协议竞争在当前分析中未提及，建议补充。

### 方向三（P0）：Content-Type XSS

这是最紧急的。5 分钟修复方案（加 `X-Content-Type-Options: nosniff`）：

```go
c.Header("X-Content-Type-Options", "nosniff")
c.Header("Content-Disposition", "attachment; filename=\"download\"")
c.Status(http.StatusOK)
_, _ = io.Copy(c.Writer, rc)
```

但需注意：`gin.Context.Writer` 在 `c.Status()` 后被直接写入，`io.Copy` 前 set header 仍然有效（Gin 在第一次 `Write` 前 flush header）。已验证可行。

### 方向四（P0）：中间件顺序

这个问题比看起来更微妙。交换顺序后还需要处理一个关键交互：

```go
// 交换后的顺序：Auth → Idempotency → RateLimit
// 问题：Idempotency 的 Reserve 消耗了幂等键槽位，然后 RateLimit 返回 429
// → 幂等键被永久锁定为 "in_progress"，客户端重试会收到 409 而非重新执行
```

解决方案在文档中已提及：幂等中间件需要后置检测 `c.Writer.Status() == 429` 时调用 `Release`。当前 idempotency middleware 只在 `>=500` 时 Release，需要扩展为 `>=500 || status == 429`。

### 方向五（P2）：搜索 limit 语义

在 `search.go` 中 `for _, kind := range kinds` 前添加：

```go
perKindLimit := limit
if len(kinds) > 0 {
    perKindLimit = min(limit, searchMaxLimit/len(kinds))
}
```

简单、安全、立即消除歧义。同时建议在响应中加入 `per_kind_counts`——这在当前 `SearchHit` 结构上无侵入，因为响应是 `[]domain.SearchHit`，可以在 handler 层做一次简单的计数。

---

## 跨文档交叉比对补充验证

我通过 `grep -l` 验证了 `docs/requirements/` 下 157 个文件中引用相关关键词的情况：

| 关键词 | 命中文件 | 是否系统性覆盖 |
|--------|---------|--------------|
| `Content-Type\|content_type\|X-Content\|nosniff\|sniff` | 1 个（`genuinely-uncovered-directions.md` 仅提及 `Content-Type: application/json` 作为响应示例） | **否** |
| `RunCleaner\|cleaner.*idempotency` | 5 个（均仅作为**草稿清理**的复用建议出现） | **否** |
| `中间件顺序\|middleware.*order` | 5 个（提及但方向不同，关注批量请求部分失败） | **否** |
| `per_kind\|perKind\|跨种类\|独立上限` | 0 个 | **完全无覆盖** |
| `端点.*限流\|端点粒度\|endpoint.*ratelimit` | 0 个 | **完全无覆盖** |

综上，除方向二需修正核心论据外，其余四个方向的「零系统性覆盖」声明成立。

---

## 优先级建议（修正后）

| # | 方向 | 修正后优先级 | 工作量 |
|---|------|------------|--------|
| 三 | Content-Type XSS | **P0** | 5 分钟 |
| 四 | 中间件顺序 | **P0** | 30 分钟（含幂等 429 Release 逻辑） |
| 一 | 端点粒度限流 | **P1** | 2-3 小时 |
| 五 | 搜索 limit 语义 | **P2** | 15 分钟（perKindLimit）+ 1 小时（per_kind_counts） |
| 二 | Idempotency 缺少配置开关 | **P3** | 1 小时（添加 `idempotency.enabled` 配置项） |

建议将此文档（含修正）保存到 `docs/requirements/` 作为正式分析文档。
