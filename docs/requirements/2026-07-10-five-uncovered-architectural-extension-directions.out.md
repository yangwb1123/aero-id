Now I have a thorough understanding of the codebase and existing docs. Let me provide my assessment.

---

## 评估反馈

这是一份非常出色的架构级扫描。以下从证据准确性、覆盖完整性、优先级逻辑三个方面给出反馈。

---

### 一、证据准确性：3 处需要修正

#### ❌ 1. "项目根目录无 Dockerfile"

**实际情况：** 有 Dockerfile，位于 `deployments/Dockerfile`，而且已经是**多阶段 + distroless 最小镜像**：

```dockerfile
FROM golang:1.26 AS build
...
FROM gcr.io/distroless/static-debian12:nonroot
```

`docker-compose.yml` 的 `build.context` 也正确引用了它。所以方向一的核心缺口不是"没有 Dockerfile"，而是：
- **没有 Docker build/push CI job**（ci.yml 只做 `go build ./...`）
- **没有版本标签**（始终 `aero-id:latest`）
- **没有 SBOM 生成 / 签名**

建议将方向一的 Dockerfile 相关描述改为"**Docker 镜像无自动构建与发布管道**"，聚焦于 CI/CD 缺口而非 Dockerfile 有无。

#### ⚠️ 2. "优雅降级契约——零系统性覆盖"

**不完全准确。** `docs/REVIEW_2026-06-30_INFRA_DEEP.md` 的 **§4. 降级策略分析** 已经覆盖了 SSO 降级（stale cache + 断路器）、DB 不可用、S3 不可用等场景，并分析了 SSO 降级的边界情况（stale TTL 耗尽后全 401）。**但**它的形式是**描述性分析**（"当前是什么"），而不是**规范性契约**（"应当是什么"）。所以您说的"没有统一降级矩阵文档"是正确的——INFRA_DEEP 没有 `docs/DEGRADATION_MATRIX.md` 式表格来逐端点定义不同依赖故障下的行为。

建议把描述从"零覆盖"改为"**有降级行为分析但无规范性降级契约**"，这会更有说服力。

#### ⚠️ 3. "Idempotency RunCleaner 未被启动"

`app.go` 中已有：

```go
stopIdemClean := idemStore.RunCleaner(time.Hour)
```

这是在 REVIEW 文档之后修复的。不影响您的核心论点（其他表如 `notifications`、`audit_events`、`outbox_events` 确实无清理），但建议在方向五中注明 `idempotency_keys` 已有一个工作的 TTL 清洁器。

---

### 二、覆盖完整性：3 个可加强的点

#### 1. 方向三（降级契约）的改动量被低估

您估算 9 天，但请注意：要**真正实现**逐端点的降级逻辑，每个 handler 需要：
- 读取降级状态（DB dead? SSO dead?）
- 根据契约决定返回 503 vs 429 vs 200（stale）
- 写响应头 `X-Aero-Degraded`
- 写测试覆盖每种降级组合

以 aero-id 现有的测试严谨度（`middleware_test.go`、`handler` 层测试），**估算可能偏低**。建议改为 **~12-14 天**，降级矩阵文档 1 天 + 降级状态管理器 2 天 + 每个 handler 适配 ~1 天 × 8 个 handler = 10 天 + 测试 + 集成。

#### 2. 方向五缺少一个重要的表

`outbox_events` 中已有 `dead_letter` 列和 `retry_count`，但 relay 在达到 `max_retries` 后只标记 `dead_letter = true`，**从不清理**死信行。建议在方向五的生命周期策略表中补充这一条。

#### 3. 方向一可以考虑与现有的 `docs/CHANGELOG.md` 整合

仓库根目录已有 `docs/CHANGELOG.md`（手动维护，记录 `feat/roadmap` 分支的 33 个提交）。这不是自动生成的，但**说明团队已经有变更记录习惯**。方向一的 CHANGELOG 自动化方案可以设计为从这个文件迁移到 `git-cliff` / release-drafter，而不是从零开始。

---

### 三、优先级建议的认同与微调

| 方向 | 您推荐 | 我的意见 | 理由 |
|------|-------|---------|------|
| 四（传输协议加固） | **P1, 第 1 步** | ✅ **同意，但建议细化风险分级** | 缺失的配置项中，`MaxConcurrentStreams` 无限制和 `ReadHeaderTimeout` 缺失是真正的安全风险（DoS 向量），而 `MaxRecvMsgSize` 和 `InitialWindowSize` 更偏性能。可以按 **安全 vs 性能** 分两批交付。 |
| 一（发布工程） | **P1, 第 2 步** | ✅ **同意** | 但建议先从最小可行开始：Goreleaser + Docker build CI job（~1 天），CHANGELOG 自动化和 SemVer 策略可以放在后续迭代。 |
| 三（降级契约） | **P1, 第 3 步** | ⚠️ **建议降为 P2 或拆分为两部分** | 文档（降级矩阵）可以 P1（~1 天，纯文档工作）。但代码实现（降级中间件 + 每个 handler 适配）工作量较大（~12-14 天），且与方向四（传输加固）没有依赖关系。建议：**文档 P1，代码 P2**。 |
| 二（可观测性成本） | **P2** | ✅ **同意** | 但建议在追踪采样策略中加入一条规则：当 span 导出失败率 > 10% 时自动降采样（防止 tracing backpressure 拖垮应用）。 |
| 五（数据生命周期） | **P2** | ✅ **同意** | 建议明确：GC 开启并运行稳定 2 周后再启动方向五。 |

---

### 四、其他值得补充的发现

在通读过程中我注意到几个与这五个方向相关的**额外证据点**，值得考虑纳入：

#### A. 方向二的日志采样——还有一个隐蔽的日志源

除了 `middleware/access_log.go` 的请求日志外，`internal/outbox/relay.go` 在每个 drain cycle 中也有日志输出。在 1000 req/s + 5s 轮询间隔下，relay 日志量约 17,280 行/天。这不影响核心论点，但可以作为补充证据。

#### B. 方向四的 gRPC 加固——还有一个遗漏

`internal/sso/client.go` 中 gRPC 客户端使用的 `dialOptions` 由 `sso.ClientConfig` 提供 `KeepaliveTime` 和 `KeepaliveTimeout`，但**服务端没有对应的 `keepalive.EnforcementPolicy`**。这意味着：

- 客户端有 keepalive ✅
- 服务端不强制执行 ping 间隔 ❌

这在 `grpc.NewServer(...)` 调用中缺失。建议在方向四中补充这条发现。

#### C. 方向三的降级矩阵——已经有一个隐含契约值得文档化

`/files/dl`（HMAC 签名下载）当前是唯一一个**完全不依赖 DB 和 SSO** 的端点。这个行为已经在有意识地设计了，但没有文档化。您可以在方向三的降级矩阵中把这个作为"设计已存在但未文档化"的案例。

---

### 五、整体评价

| 维度 | 评分 | 说明 |
|------|------|------|
| 证据完整性 | **9/10** | 3 处小误差（Dockerfile、RunCleaner、INFRA_DEEP 降级覆盖），但不影响核心论点 |
| 交叉验证深度 | **9/10** | 确实覆盖了 50+ REVIEW 文档未触及的方向 |
| 代码级证据引用 | **10/10** | 每项发现都附了文件路径和行号，这是最高质量的架构分析 |
| 建议可操作性 | **9/10** | 改动量估算、优先级建议、执行顺序都很务实 |
| 架构一致性 | **10/10** | 五个方向都遵循了单体依赖方向，且可增量交付、可回滚 |

**总体结论：这份分析的质量极高，五个方向的选取精准填补了现有 100+ 分析文档的真实盲区。** 修正上述 3 处证据细节后，可以直接作为架构决策文档（ADR）的基础。特别是方向四（传输协议加固），它触及了一个被所有既有分析完全忽略的安全基础面——这在 Go 项目中很常见（大家都关注业务代码，忽略了几十行 server 配置参数），但恰恰是攻击者最容易利用的缺口。
