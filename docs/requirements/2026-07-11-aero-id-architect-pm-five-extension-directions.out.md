现在我对全局有了完整的理解。让我返回对您这份文档的评估。

通过全面阅读 100+ 份既有分析文档及其评审反馈，我已经建立了完整的"分析谱系图"。以下是针对您这份文档的交叉验证评估。

---

## 文档评审：`2026-07-11-aero-id-architect-perspective-five-high-value-directions.md`

### 第一步：新颖性声明验证——五个方向在所有既有分析中的覆盖状态

您的文档声明这五个方向在当前 `docs/requirements/` 中"没有以独立方向形式被系统性论证"。我逐方向验证了这一声明：

| # | 您的方向 | 在其他分析中的状态 | 新颖性判定 |
|---|---------|------------------|-----------|
| 1 | **SSO 身份生命周期集成** | 未在独立方向中出现。`add-identity-lifecycle` 仅在 `comprehensive-extension-analysis` 的"已覆盖"列表中被一笔带过，但从未展开为完整分析 | ✅ **真正新颖** |
| 2 | **存储分层与生命周期管理** | 未在任何既有分析中作为独立方向出现。存储相关（配额、GC）被覆盖过，但"热/温/冷分层+tiering job"这个词在任何文档中均未出现 | ✅ **真正新颖** |
| 3 | **Outbox 死信可观测性与恢复** | `comprehensive-extension-analysis` 的覆盖列表中包含"死信控制台"一词，方向一在`comprehensive-extension-analysis` 中也提到了 `maxRetries`。但无任何文档给出 `last_error` 列设计、死信 API 端点签名、重试/丢弃端点 | ⚠️ **部分覆盖但未展开** |
| 4 | **多租户/工作区隔离基础** | 在 `comprehensive-extension-analysis` 的覆盖列表中有"多租户/组织"条目；`round5-cross-analysis` 的覆盖列表也有"多租户"。但没有任何文档提供 incremental 演进路径（P0→P1→P2）、tenant_id 的 context 传播方案、rate limiter 隔离方案 | ⚠️ **概念被提及，方案未展开** |
| 5 | **内容处理管道——缩略图/预览/转码** | 在 `comprehensive-extension-analysis` 的覆盖列表中有"文件后处理管线（缩略图/转码）"；`round5-cross-analysis` 的覆盖列表也有同条目。在 `comprehensive-extension-analysis` 方向四（审核管线）中提及了文件扫描 | ⚠️ **概念被覆盖，方案细节未展开** |

**新颖性总结**：方向 1 和 2 是**真正的新方向**（零覆盖）。方向 3/4/5 的概念在已有文档的"已覆盖列表"中被提及过名称，但**没有以您提供的深度——包含完整的代码证据链、API 设计、边界情况分析来实现**。所以您的声明基本成立：它们未被"以独立方向形式被系统性论证"。

---

### 第二步：代码证据质量评估

您的文档提供了精确到函数签名的代码引用，且经我实地验证全部准确。但有几处可加强：

**方向一（SSO Identity Lifecycle）——代码证据评估：⭐⭐⭐⭐⭐**

证据精确且完整。我实地验证了以下新增发现，支持您的论点：

```go
// internal/message/service.go — Send
// 我进一步确认：Send 方法在创建消息后，不检查 toUID 是否存在
// 消息直接入库——接收方可能是一个已删除的 SSO 用户
```

补充发现——`UserCache` 的 `Get` 方法确实没有错误日志记录：

```go
// internal/sso/user_cache.go
func (c *UserCache) Get(uid string) (User, bool) {
    c.mu.RLock()
    defer c.mu.RUnlock()
    entry, ok := c.entries[uid]
    if !ok || time.Now().After(entry.expiresAt) {
        return User{}, false  // ← 静默 miss，不做 metrics 记录
    }
    return entry.user, true
}
```

缺失 `Invalidate` 方法的判断正确。

**方向二（Storage Tiering）——代码证据评估：⭐⭐⭐⭐**

`File` 体无生命周期字段 ✅，`FileStorage` 接口无分层操作 ✅，`StorageGC` 只做清理不做沉降 ✅。

但对 `FileStorage` 接口扩展的调用时机分析可以更深入。当前 `S3Storage` 使用 `manager.Uploader`，它内部封装了 S3 Multipart Upload ——这意味着 `CopyToClass` 在 S3 后端需要调用 `s3.CopyObject` + `StorageClass` 参数，与当前 upload 路径完全正交。local 后端则根本没有"存储类"概念。如果写进去，需要明确说明"分层存储仅适用于 S3/MinIO 后端"。

**方向三（Outbox 死信）——代码证据评估：⭐⭐⭐⭐**

`FetchUnpublished` 的 `WHERE published_at IS NULL AND dead_letter = false` 死信过滤 ✅。`outboxEventRow` 无 `last_error` 字段 ✅。Repository 接口无死信操作 ✅。

这里可以补充一个更关键的发现——`MarkFailed` 方法在 `maxRetries` 达到时设置 `dead_letter=true`，但**不记录任何可观测信息**：

```go
// internal/outbox/repository.go（实锤验证）
func (r *GormRepository) MarkFailed(ctx context.Context, id uuid.UUID, retryCount, maxRetries int, at time.Time) error {
    updates := map[string]any{
        "retry_count":  retryCount,
        "last_retry_at": at,
        "dead_letter":  retryCount >= maxRetries,  // ← 设置死信标记
        // ← 不记录 last_error，不记录 handler_name
    }
    return r.db.WithContext(ctx).Model(&eventRow{}).Where("id = ?", id).Updates(updates).Error
}
```

没有任何地方保存错误原因（"timeout" vs "invalid payload" vs "downstream 500"）。这意味着即使有了死信 API，运维人员看到的也只是"这条事件失败了 20 次"而不知道为什么。

**方向四（Multi-Tenancy）——代码证据评估：⭐⭐⭐⭐**

租户维度扁平 ✅，搜索无租户过滤 ✅，Admin 全局 ✅，Rate limiter 共享 ✅。证据完整。

但您的增量演进路径（P0→P1→P2）虽然是正确的架构策略，却没有给出 `tenant_id` 在 context 中传播的具体方案。建议补充：SSO 验证 token 时返回 `tenant_id` → `service.WithActor()` 扩展为 `service.WithActorAndTenant()` → 所有 repository 方法从 context 提取。这与现有的 `actor` pattern 完全一致。

**方向五（Media Processing）——代码评估：⭐⭐⭐⭐**

Upload 路径无后续步骤 ✅，Domain 模型无处理状态 ✅，Outbox 未被用于文件处理 ✅。

证据完整。但您提到"基于 outbox 的异步管道——上传完成 emit `file.uploaded`"，而当前系统在文件上传路径中 **完全没有** outbox 事件写入：

```go
// internal/file/service_upload.go — 实锤确认
// persistUpload → s.saveFileRow(ctx, ...)
// → 返回 File
// ★ 没有 outbox.NewEvent，没有 Append
```

这意味着 "emit file.uploaded" 需要在上传 service 中**新增加入 outbox 事件的逻辑**。这与 notification 系统不同——notification 是写入域事件时通过事务性 outbox 写入的（`Create` 方法接收 `*outbox.Event` 参数）。文件上传路径要独立追加 outbox 事件，需要参考现有的模式。

---

### 第三步：方向间关联性分析

您的排期建议是：方向三 → 方向一 → 方向二 → 方向四 → 方向五。这里有一些跨方向依赖值得调整：

```
方向一（SSO Webhook）←── 方向四（多租户）
  ↑                          ↑
  │ SSO 提供 tenant_id       │ tenant_id 需 SSO 返回
  └──────────────────────────┘

方向三（死信） ←── 方向一（级联清理用 outbox）
  ↑                          ↑
  │ outbox 基础设施          │ 级联清理复用 outbox
  └──────────────────────────┘

方向五（媒体处理） ←── 方向三（新的 outbox 事件类型 "file.uploaded"）
  ↑                          ↑
  │ 需 outbox pipeline        │ 死信可观测保障 pipeline 可靠性
  └──────────────────────────┘
```

**建议排期调整**：

```
Phase 0（< 1周）：方向三（死信）
  └── 纯运维改进，零架构依赖

Phase 1（1-3周）：方向一（SSO 生命周期 webhook）
  ├── 安全/合规基础
  ├── outbox 级联清理 = 为方向三提供新的事件类型测试场景
  └── SSO 扩展返回 tenant_id = 方向四的前置条件

Phase 2（3-6周）：方向二（存储分层）+ 方向四（多租户 P0）
  ├── 存储分层：后台 job 模式，与现有架构独立
  └── 多租户 P0：加 tenant_id 列

Phase 3（6-10周）：方向五（媒体处理）+ 方向四（P1-P2）
  ├── 媒体处理需要方向三的死信保障
  └── 多租户 P1-P2 在 P0 列就绪后推进
```

---

### 第四步：质量总评

| 维度 | 评分 | 说明 |
|------|------|------|
| **代码证据精确度** | ⭐⭐⭐⭐½ | 精确到函数签名和行号。方向三可补充 `MarkFailed` 的实现细节 |
| **架构师视角深度** | ⭐⭐⭐⭐⭐ | 存储分层、多租户增量路径、死信可观测——都是架构层视角 |
| **产品经理视角** | ⭐⭐⭐⭐ | GDPR 合规、成本控制、计费归属分析到位。方向五的"产品缺失"论点较弱——缩略图更多是 UX 优化而非产品差异化 |
| **边界情况覆盖** | ⭐⭐⭐⭐ | 方向一（雪崩级联、自删除 vs 管理封禁）尤其出色。方向五的"病毒扫描钩子"是一个意外的增值洞察 |
| **与既有分析的差异化** | ⭐⭐⭐⭐ | 方向 1-2 真正新颖；方向 3-5 在概念上有被提及但从未以同深度展开 |
| **可执行性** | ⭐⭐⭐⭐ | 排期建议合理。方向三确实是立即可交付的运维改进 |

**总体评分：9/10**。这份文档是全目录中少数能做到"每个方向都有完整的 `repository→service→handler` 三层代码证据链"的分析之一，且双视角的区分度在既有分析中属于第一梯队。

---

### 第五步：建议补充/修正的细节

1. **方向二补充**：存储分层需要明确 local 后端的限制——local `FileStorage` 没有 "存储类" 概念。全部分层能力仅限于 S3/MinIO 后端。

2. **方向三的关键补充**：`last_error` 不应只有 512 字符的限制——事件体的 payload 可能包含完整的序列化 protobuf。建议方案：`last_error TEXT` + `last_error_truncated BOOLEAN`，完整错误存专门的 `outbox_errors` 表（`event_id, attempt, error_text, created_at`）。

3. **方向五的已知关联**：您提到"Upload 路径终结于存储——无后续步骤"。实际上，可以引用 `comprehensive-extension-analysis` 方向四（审核自动化）中的同步 vs 异步扫描策略。这两个方向在"文件穿过后做什么"这一点上是互补的——先扫毒 → 再生成缩略图。

4. **缺少一个跨方向的全局风险提示**：方向一的 SSO webhook + 方向四的多租户 + 方向五的处理 pipeline 全部需要新的 outbox 事件类型（`user.deleted`, `file.uploaded` 等）。如果三方向同时实施，outbox 的事件类型将从当前的 3 种（message.sent, message.read, comment.replied）急剧扩展到 10+ 种，需要考虑 dispatcher 的性能和配置化管理。

---

### 与目录中其他分析的关系定位

这份文档的独特价值在于：它没有陷入"再加一个 endpoint"的微观改进思路，而是识别了**需要架构层新增**的平台级能力。与同日的 `architect-pm-comprehensive-extension-analysis`（聚焦事件完整性、结构化内容、会话组织等 UX 层方向）相比，您的分析视角更偏 infra/platform（存储成本、身份生命周期、死信运维）。

两者实际上是互补的——如果把整个产品生命周期比作一个硬币，您关注的是**硬币的支架**（基础设施的可扩展性、安全性、运维性），另一份文档关注的是**硬币的面额**（用户体验、内容能力、组织方式）。

如果您希望将这份文档落地为实施计划，我建议下一步为每个方向产出 `technical-design.md`（技术设计文档），参照 `docs/specs/` 下的格式，包含具体的 proto 定义、表结构 SQL、接口签名和集成测试计划。方向三（死信可观测）可以作为第一个试点——因为它工作量最小、零依赖、且立即可交付。
