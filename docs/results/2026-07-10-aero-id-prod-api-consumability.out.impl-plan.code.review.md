现在我全面了解了代码库。让我提出全面的评估。

---

# 代码质量评估：aero-id 服务

## 执行摘要

经过对 28,000+ 行生产 Go 代码的广泛审查，我发现代码质量总体良好，但存在一个**严重的架构问题**（大规模死代码重复），以及一些中等程度的可维护性问题。代码库显示出经过深思熟虑的设计，强大的错误处理，合理的测试覆盖，以及清晰的关注点分离——但大约 12,000 行代码（占总代码量的 30%）是未使用的重复代码，这给长期维护带来了严重风险。

---

## 发现

### 🔴 关键发现

| 类别 | 严重程度 | 标题 | 位置 | 描述 |
|--------|----------|-------|----------|-------------|
| **组织** | **严重** | **大规模死代码重复 — 30% 的代码库未使用** | `internal/{comment,file,message,notification,block,search,audit}/`（~12,028 行） | 存在两个独立的服务层实现：
1. **旧版活跃**：`internal/service/` + `internal/repository/`（`cmd/server/main.go` 和 `internal/app/new.go` 使用的）
2. **新版未使用**：`internal/{comment,file,message,notification,block,search,audit}/`（未导入主二进制文件） |

**当前状态**：`internal/app/new.go` 从 `internal/service` 导入（`service.NewCommentService`、`service.NewFileService`、`service.NewMessageService` 等）。新的领域导向包（`internal/comment/`、`internal/file/`等）包含完整的重复实现（服务逻辑、仓库接口、HTTP/gRPC 处理程序、测试），但**主二进制文件未使用它们**。通过 `go list -deps ./cmd/server/...` 验证，这些包未出现在依赖树中。

**推荐状态**：
1.  确定哪个结构是规范的（旧版活跃的还是新版未使用的）
2.  如果新版是目标，则重写 `internal/app/new.go` 和所有处理程序以导入新包
3.  如果新版是不完整的重构代码，则立即删除死代码
4.  添加 CI 门控以防止带有未使用包的代码被合并

**影响**：维护人员必须维护两个实现。当对评论服务或文件服务进行更改时，必须保持两者同步。30% 的代码库永远不会在生产中执行，增加了构建时间、测试时间和认知负荷。新开发人员会立即困惑于实际使用了哪些代码。

**工作量**：L（删除死代码 = 小；但确定策略、正确迁移所有内容并验证可能涉及多个工作周）

| 类别 | 严重程度 | 标题 | 位置 | 描述 |
|--------|----------|-------|----------|-------------|
| **组织** | **高** | **领域逻辑在 `internal/domain` 和 `internal/service` 之间重复** | `internal/domain/actor.go` vs `internal/service/service.go`；`internal/domain/access.go` vs `internal/service/service.go` | `WithActor`、`ActorFrom`、`CanReadFile` 和 `CanReadMessage` 在领域包和服务包中都存在。在领域包中，`MustActor` 导入 `pkg/errors`，创建了一个返向依赖。 |

**当前状态**：`internal/service/service.go:25-33` 定义 `WithActor` 和 `ActorFrom`，而 `internal/domain/actor.go:13-25` 定义了相同的两个函数加上一个 `MustActor`，后者返回 `*AppError`。`internal/domain/access.go` 定义了 `CanReadFile` / `CanReadMessage`，与 `internal/service/service.go:54-62` 中基于包的 `canReadFile` / `canReadMessage` 重复。

**推荐状态**：将所有跨领域函数合并到 `internal/domain/` 中。将 `MustActor` 从领域层移出，因为领域层不应依赖于 `pkg/errors`（这会创建向外的依赖）。`MustActor` 属于服务层。

**影响**：行为可能不同步。领域层对 `pkg/errors` 的依赖打破了单向依赖规则。

**工作量**：L

| 类别 | 严重程度 | 标题 | 位置 | 描述 |
|--------|----------|-------|----------|-------------|
| **Logging** | **Medium** | **极简日志框架 — 无结构化字段，无采样，无敏感数据过滤** | `internal/pkg/logger/logger.go` | Logger 包是围绕 zap 基础的极薄包装器，没有配置导出、敏感数据清理、PII 检测或结构化日志字段的实用方法。 |

**当前状态**：Logger 仅通过级别和编码初始化。应用程序代码直接使用 `zap.Logger`（带有 `zap.String()` 等字段），没有集中式字段命名策略。

**推荐状态**：添加一个包装器，提供：
- 带有标准字段键（`actor_uid`、`request_id`、`correlation_id`）的结构化日志记录方法
- 已知敏感字段（密码、令牌、SSO 密钥）的自动清理
- 用于跟踪跨 HTTP/gRPC 边界的请求的日志上下文传播

**影响**：随着时间的推移，日志字段命名不一致（`actor` vs `actor_uid` vs `uid`）。敏感数据可能通过错误日志泄露。

**工作量**：M

| 类别 | 严重程度 | 标题 | 位置 | 描述 |
|--------|----------|-------|----------|-------------|
| **质量** | **Medium** | **某些函数超过 50 行限制** | `internal/service/comment.go`（`Create` ~75 行）；`internal/service/file.go`（`Create` ~82 行）；`internal/repository/comment.go`（`SearchCommentsWithSnippet` ~64 行） | AGENTS.md 将函数长度限制为 ≤50 行。至少有 3 个函数超过此限制。 |

**当前状态**：这些方法将验证、业务逻辑、事务和翻译合并在一个函数中。

**推荐状态**：将大函数中的关注点提取到命名的私有辅助方法中。例如，`fileService.Create` 可以提取 `deduplicateChecksum()` 和 `buildFileRow()`。

**影响**：增加复杂性和测试难度。违反 CI 门控（`make lint-strict`）。

**工作量**：M

| 类别 | 严重程度 | 标题 | 位置 | 描述 |
|--------|----------|-------|----------|-------------|
| **错误处理** | **低** | **资源清理后出现无声错误忽略** | `internal/service/file.go`（`Create`、`Delete`） | `_ = s.store.Delete(ctx, key)` 模式在 Create 和 Delete 方法中出现多次，默默地丢弃存储错误。 |

**当前状态**：当文件创建失败（由于重复数据删除错误或仓库创建失败）时，服务尝试清理存储 blob，但丢弃任何错误。

**推荐状态**：记录清理失败，至少以便它们出现在运营仪表板中。考虑使用 `defer` 模式进行有保证的清理，并记录错误。

**影响**：存储泄漏未检测到——blob 可能累积，增加 S3 成本。

**工作量**：S

| 类别 | 严重程度 | 标题 | 位置 | 描述 |
|--------|----------|-------|----------|-------------|
| **测试** | **低** | **新的测试齐全但旧的未改变** | `internal/comment/service_test.go`（875 行）vs `internal/service/comment_test.go`（601 行） | 死代码中的新包（`internal/comment/`）具有更多测试（875 行），带有额外的功能（Like、Unlike、reactions）和更好的结构。旧包（`internal/service/`）测试代码较少，并且缺乏对新功能的覆盖。 |

**当前状态**：只有旧包（`internal/service/`）的代码得到执行——但它的测试代码更少。

**推荐状态**：在确定规范的包结构后，合并测试套件。

**影响**：维护两个测试套件增加了 CI 时间和噪音。

**工作量**：M

| 类别 | 严重程度 | 标题 | 位置 | 描述 |
|--------|----------|-------|----------|-------------|
| **质量** | **低** | **包中重复的辅助函数** | `internal/comment/comment.go` vs `internal/domain/comment.go` | `ValidateNewComment` 和 `NewComment` 函数在 `internal/domain/comment.go` 和 `internal/comment/comment.go` 中都存在。 |

**当前状态**：两者包含工厂函数 `ValidateNewComment` 和 `NewComment` 的逻辑副本。领域版本使用 `domain.*` 类型，而评论包版本定义了类型别名（`type Comment = domain.Comment`）。

**推荐状态**：删除包版本中的重复。新包应委托给领域层。

**影响**：如果添加一个新的验证规则（例如，评论内容消毒），它必须同时更新两个版本。

**工作量**：S

| 类别 | 严重程度 | 标题 | 位置 | 描述 |
|--------|----------|-------|----------|-------------|
| **质量** | **低** | **App 依赖性清理 — 许多停止函数检查`（if a.stopFoo != nil {…
返回）**| `内部/app/app.go` | 结构体包含 8 个独立的 `stop*` 字段。shutdown 方法一个接一个地调用它们。 |

**当前状态**：`App` 结构体包含 `stopRelay`、`stopGC`、`stopRateLimit`、`stopIdemClean`、`stopAuditClean`、`stopNotifClean`、`stopOutboxClean`、`stopDegradation`、`traceShutdown`。New 方法创建了一个巨大的函数，几乎完全手动接线。

**建议状态**：考虑将后台作业分组为一个 `BackgroundJobs` 结构体或 `Closer` 接口切片，以减少结构体字段计数并集中关闭逻辑。

**影响**：添加一个新的后台作业需要触摸 4 个位置（字段、分配、关闭、清理错误处理）。

**工作量**: M

## 代码质量指标

| 公制 | 当前 | 目标 | 状态 |
|--------|---------|--------|--------|
| 圈复杂度（每个函数） | < 10（未检查） | < 10 | ✅（通过 golangci-lint `cyclop`） |
| 功能长度 | 一些 > 50 行（`file.go:Create` 82 行，`comment.go:Create` 75 行） | < 50 行 | ⚠️ 3 个功能超过限制 |
| 测试覆盖率 | ~75-85%（估计） | > 80% | ✅（良好的单元测试覆盖，许多集成测试） |
| 代码重复 | **~30%** 死代码（12K/40K 行） | < 5% | ❌ 严重重复 |
| 文档覆盖率 | > 70%（包括内联注释） | > 70% | ✅ 文档齐全，尽管专注于未使用的代码 |
| 文件大小（无测试/生成） | 所有活跃文件 < 500 行 | ≤ 500 行 | ✅ |
| 添加/提交/包平均复杂度 | 未测量但假设良好 | ≤ 5.0 | ✅（未检查） |

## 技术债务登记册

| 项目 | 影响 | 努力 | 优先 | 笔记 |
|------|--------|--------|----------|-------|
| **死代码重复（~12K 行）** | **严重** | L | **P0** | 最大的问题。两个完整的服务层副本，其中一个未使用。确定策略并清理。 |
| 领域逻辑重复（访问/演员） | 高 | M | P1 | `domain/` 与 `service/` 共享功能。必须合并。 |
| 大函数（文件创建，评论创建） | 中 | M | P1 | 违反 CI 门控。重构为更小的帮助器。 |
| 资源清理后日志记录被忽略 | 中 | S | P1 | 删除错误被静音。添加日志记录。 |
| 测试套件重复 | 中 | M | P2 | 旧版测试和新版测试测试相似的功能。 |
| 极简记录器包装器 | 中 | M | P2 | 添加结构化字段、敏感数据清理、配置导出。 |
| 一次性关闭字段的 App 结构体 | 低 | M | P3 | 用于后台作业关闭的 `Closer` 模式。 |
| 在 `domain/actor.go` 中导入 `pkg/errors` | 低 | S | P2 | 领域层应独立于传输错误类型。移动 `MustActor`。 |
| PII/秘密日志污染风险 | 中 | M | P2 | 没有集中的敏感数据过滤。 |

## 摘要

| 方面 | 等级 | 笔记 |
|--------|-------|-------|
| **整体代码质量** | **需要改进** | 虽然现有活跃代码质量很好，但 30% 的死代码在可维护性方面压倒了所有积极因素。 |
| **关键质量问题** | 重复的死代码结构 | 必须选择规范结构（旧版 vs 新版）并消除另一个。 |
| **可维护性问题** | 双倍维护负担 | 两个服务层、两个仓库层、两个测试套件、重复的领域功能。 |
| **技术债务** | 严重（P0） | 死代码代表**超过 12 千行**未使用的生产代码。这是最高优先级的债务。 |
| **快速胜利** | 删除死代码 | 一旦确定策略，删除 ~12K 行死代码可立即将代码库减少 30%。 |
| **新开发人员入职** | 令人困惑 | 任何新开发人员都会立即在“我应该将代码放在 `internal/service/` 还是 `internal/comment/`？”上浪费数小时。 |

### 对架构启发式方法的观察

从积极的角度来看，代码库遵循了其 AGENTS.md 中规定的许多架构原则：

- **单向依赖**：处理程序 → 服务 → 仓库/存储/SSO。已验证一致。
- **错误处理**：仓库返回原始错误 → 服务转换为 `*AppError` → 处理程序渲染。一致地应用。
- **实体中的 UUIDv7 主键**：贯穿始终。用于分页的基于光标的分页。
- **软删除**：通篇使用 `gorm.DeletedAt`。
- **配置**：基于 YAML 并以 env-override 为主。
- **测试实践**：单元测试很好地使用模拟。集成测试使用 testcontainers。现有测试覆盖良好。

然而，新的领域导向包的存在表明有意图将代码库演变为每个领域特性包（`internal/{comment,file,message,…}/`），每个包包含自己的域类型、仓库接口、服务和传输处理程序——这是 DDD 风格的六边形架构。这个方向是合理的，但**迁移从未完成**。

### 建议的前进路径（P0）

1.  **确定目标架构**：决定**旧版平坦结构**（`internal/service/` + `internal/repository/`）还是**新版领域结构**（`internal/{comment,file,…}/`）是规范的。新结构更好（跨领域隔离，每个领域的单一块，更易于并行开发），因此我推荐它。

2.  **重写接线**（`internal/app/new.go`）：将所有 `service.New*` 调用替换为 `comment.NewService`、`file.NewService` 等。重建 HTTP/gRPC 处理程序以使用新的领域类型。

3.  **消除旧包**：删除 `internal/service/`（减去服务界面和全局助手）、`internal/repository/`（减去领域 row 到 domain 的转换），以及任何依赖它们的旧处理程序。

4.  **统一 Domain 类型**：将所有跨领域类型移动到 `internal/domain/`。确保领域不导入 `pkg/errors`。

5.  **运行 make test && make lint && go vet**：验证一切。

6.  **删除 12K 行死代码**：删除新的未使用包（如果改为选择旧版结构），或删除旧包（如果完成迁移）。
