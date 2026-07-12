现在我已拥有完成全面代码审查所需的所有信息。以下是报告。

---

# Staff Engineer 代码审查报告 — aero-id

## 总体评估：**有待改进 (Needs Work)**

代码库包含大量死代码、一个已部分完成但被遗弃的架构迁移，以及多个代码质量和维护性问题。其中最关键的问题是大约 **12,000 行死代码**，构成 7 个未使用的包，这将使任何新加入的开发者感到困惑，并使工具链变慢。

---

## 一、发现项

### #1 — 严重：大量死代码（被遗弃的架构迁移）

| 字段 | 内容 |
|------|------|
| **类别** | 组织架构 |
| **严重程度** | **严重 (Critical)** |
| **标题** | 7 个未使用的包共约 12,000 行死代码 |
| **位置** | `internal/comment/`, `internal/file/`, `internal/message/`, `internal/block/`, `internal/notification/`, `internal/search/`, `internal/audit/` |
| **描述** | 代码库中存在两套完整的评论、文件、消息和通知领域实现。旧的架构（`internal/service/` → `internal/repository/` → `internal/handler/{http,grpc}/`）是正在运行的生产代码，通过 `internal/app/new.go` 进行装配。新的包（每个领域包都包含自己的 handler_http、handler_grpc、service 和 repository）完全未被使用——没有任何导入路径通向 `internal/app/new.go`。 |
| **现状** | 约 5,600 行生产代码 + 约 6,400 行测试代码，分布在 7 个领域包中，未从组合根引用。这些包定义了重复的 GORM 行类型、重复的仓库实现、重复的服务，以及重复的 HTTP/gRPC 处理器。 |
| **推荐状态** | 选项 A：删除这 7 个包，清理 `go.mod` 中任何孤立依赖。选项 B：完成迁移——用新包替换旧架构，并从组合根删除旧包。 |
| **影响** | **维护性**：开发者在确定要修改哪个文件之前，必须先弄清楚 "哪个" 是当前使用的架构。新加入的开发者会因为两个看似相同的文件而浪费数小时。**构建速度**：编译约 12,000 行不必要的代码。**测试运行时间**：约 6,400 行死测试代码每次都会运行。 |
| **工作量** | XL（清理约 2 天，迁移约 1-2 周） |

---

### #2 — 高：死代码中的 `search` 是唯一消费者，且本身也是死代码

| 字段 | 内容 |
|------|------|
| **类别** | 组织架构 |
| **严重程度** | **高 (High)** |
| **标题** | `internal/search/` 是唯一使用新领域包的包，但 `search` 本身也未使用 |
| **位置** | `internal/search/search.go`, `internal/search/handler_grpc.go`, `internal/search/handler_http.go` |
| **描述** | `internal/search/` 包导入新领域包（`internal/comment/`, `internal/file/`, `internal/message/`），但组合根创建的是 `service.NewSearchService(...)`（旧架构），并将其传递给旧的 `httpapi.NewSearchHandler(searchSvc)` 和 `grpcsrv.NewSearchServer(searchSvc)`。新搜索包完全未被使用。 |
| **现状** | `internal/search/` 中约 700 行（含测试）均处于孤立状态，无人使用。 |
| **推荐状态** | 删除 `internal/search/` 包，或将其接入组合根以取代旧的搜索实现。 |
| **影响** | 维护性：增加困惑。 |
| **工作量** | M |

---

### #3 — 高：死代码中重复的 GORM 行类型

| 字段 | 内容 |
|------|------|
| **类别** | 质量 / 重复 |
| **严重程度** | **高 (High)** |
| **标题** | GORM 行类型在死包中重复定义 |
| **位置** | `internal/repository/db.go` vs `internal/file/repository.go` vs `internal/comment/repository.go` 等 |
| **描述** | 新的领域包定义了它们自己的 `fileRow`、`commentRow`、`messageRow` 等，这些与 `internal/repository/db.go` 中的行类型相似但不相同。对数据库 schema 的任何变更都必须同步更新这两处的行类型。 |
| **现状** | 两套行类型定义 + 两套 `rowToDomain` / `domainToRow` 转换函数。 |
| **推荐状态** | 删除死包，消除重复定义。 |
| **影响** | 维护性：schema 变更时容易因只修改一处而导致运行时损坏。 |
| **工作量** | M |

---

### #4 — 高：`domain/comment.go` 中的导入技巧（`var _ = idgen.NewID`）

| 字段 | 内容 |
|------|------|
| **类别** | 代码质量 |
| **严重程度** | **高 (High)** |
| **标题** | 为保持导入而添加的 `var _ = idgen.NewID` |
| **位置** | `internal/domain/comment.go:81` |
| **描述** | `idgen` 在 `domain/comment.go` 中除了保持导入外没有任何用处。这是一个设计缺陷——领域包不应依赖 ID 生成器；ID 应由调用者注入。 |
| **现状** | `var _ = idgen.NewID` 是一个隐蔽的技巧，用于防止编译器在仅使用类型别名时剥离导入。 |
| **推荐状态** | 从 `domain/comment.go` 中移除 `idgen` 导入和 `var _` 行。`NewComment` 工厂函数已接收 `ID domain.ID` 作为参数，因此不需要 `idgen`。 |
| **影响** | 维护性：任何接触此文件的开发者都会浪费时间试图理解这行代码的目的。 |
| **工作量** | S |

---

### #5 — 高：文件大小接近 500 行限制

| 字段 | 内容 |
|------|------|
| **类别** | 质量 / 规模 |
| **严重程度** | **高 (High)** |
| **标题** | 9 个非生成文件接近或超过 400 行（AGENTS.md 触发值） |
| **位置** | `internal/service/comment.go` (481), `internal/repository/comment.go` (463), `internal/app/new.go` (461), `internal/service/notification.go` (437), `internal/repository/message.go` (423), `internal/service/message.go` (417), 以及 4 个死包文件 |
| **描述** | AGENTS.md 明确指出"文件接近 400 行 → 计划拆分"和"超过 500 行 → 立即拆分"。当前 5 个活跃文件超过 400 行，其中 3 个超过 460 行。 |
| **现状** | `internal/service/comment.go` 包含评论服务的整个业务逻辑（创建、列出、编辑、删除、报告、隐藏、举报处理）在一个文件中。 |
| **推荐状态** | 拆分大文件，按关注点分组，例如 `service_comment_crud.go`、`service_comment_moderation.go`、`service_comment_reactions.go`（已有 `comment_reactions.go` 作为一个好的开始）。 |
| **影响** | 维护性：大文件难以导航、审查和理解。增加 merge 冲突的风险。 |
| **工作量** | M |

---

### #6 — 中：错误被静默丢弃（`_ =` 模式）

| 字段 | 内容 |
|------|------|
| **类别** | 错误处理 |
| **严重程度** | **中 (Medium)** |
| **标题** | 审计日志和其他辅助操作中存在的"最佳努力"错误忽略模式 |
| **位置** | `internal/service/admin.go:73,100`, `internal/service/block.go:54,77`, `internal/service/comment_reactions.go:33`, `internal/service/comment.go:413`, `internal/service/file.go:128,133,155,260`, `internal/service/message.go:312`, `internal/notification/dispatcher.go:274` 等。共约 20 处位置。 |
| **描述** | 审计日志记录、事件追加和存储清理失败被静默忽略，使用 `_ = ...`。虽然某些场景（如审计日志）确实需要"不阻塞主操作"，但完全静默丢弃意味着运维人员无法得知审计线索何时中断。 |
| **现状** | `_ = s.audit.Log(ctx, ...)` — 错误被丢弃且不留任何痕迹。 |
| **推荐状态** | 添加结构化日志记录失败的辅助操作，避免静默丢弃错误。例如：`if err := s.audit.Log(ctx, e); err != nil { s.log.Warn("audit log failed", zap.Error(err)) }`。至少应在服务层记录一条警告日志。 |
| **影响** | 可靠性：审计线索可能在无任何告警的情况下静默中断。合规审计可能无法发现记录缺失。 |
| **工作量** | S（对 ~20 处位置添加日志记录，每处约 3 行） |

---

### #7 — 中：`context.Background()` 在后台 goroutine 中的使用

| 字段 | 内容 |
|------|------|
| **类别** | 可靠性 |
| **严重程度** | **中 (Medium)** |
| **标题** | `context.Background()` 在后台任务中导致潜在的 goroutine 泄漏 |
| **位置** | `internal/outbox/relay.go:182,189`, `internal/storage/gc/sweeper.go:255`, `internal/repository/audit.go:144`, `internal/repository/notification.go:278` |
| **描述** | 后台 goroutine（中继扇出、GC sweep、TTL 清理器、退化探测器）使用 `context.Background()` 而不是在关闭时取消的派生 context。如果这些 goroutine 执行了阻塞操作（如网络调用），它们将无法在关闭时干净退出。 |
| **现状** | 使用 `context.Background()` 创建的 context 永远不会取消。 |
| **推荐状态** | 在 `app.shutdown()` 中创建一个可取消的 context 并在此类后台任务中传递。例如，`ctx, cancel := context.WithCancel(context.Background())` 并将其存储在 `App` 中，在关闭时取消。 |
| **影响** | 可靠性：优雅关闭可能不会在所有情况下完成，退出时可能泄漏 goroutine。 |
| **工作量** | M |

---

### #8 — 中：双调度器实现

| 字段 | 内容 |
|------|------|
| **类别** | 组织架构 |
| **严重程度** | **中 (Medium)** |
| **标题** | 两个 NotificationDispatcher 实现，功能集不同 |
| **位置** | `internal/service/notification.go`（功能丰富，已使用）vs `internal/notification/dispatcher.go`（功能简单，未使用） |
| **描述** | 旧服务包中的 `service.NotificationDispatcher` 有 rate limiting、preference checking、collapse 和 SSE push。新领域包中的 `notification.Dispatcher` 功能更简单，仅包含 block checking 和 SSE push。 |
| **现状** | 两个实现互不相知。 |
| **推荐状态** | 删除未使用的实现（作为 #1 清理的一部分）。 |
| **影响** | 维护性：代码膨胀，重复的业务逻辑。 |
| **工作量** | S |

---

### #9 — 中：错误哨兵值重复

| 字段 | 内容 |
|------|------|
| **类别** | 代码质量 |
| **严重程度** | **中 (Medium)** |
| **标题** | `ErrNotFound` 和 `ErrDuplicate` 在多个包中重复定义 |
| **位置** | `internal/repository/db.go:15,19`, `internal/domain/errors.go:5,7`, `internal/storage/storage.go:11` |
| **描述** | `repository.ErrNotFound` 和 `domain.ErrNotFound` 都定义了类似的语义。调用者必须知道从哪个包导入。服务层使用 `stderrors.Is(err, repository.ErrNotFound)`，某些死包则引用 `domain.ErrNotFound`。 |
| **现状** | 各层各有自己的错误哨兵值，增加了困惑。 |
| **推荐状态** | 统一错误哨兵值。推荐：在 `internal/domain/errors.go` 中定义所有哨兵值，仓库从领域包导入。但目前的旧包直接从 `repository` 包使用 `ErrNotFound`，这导致紧密耦合。 |
| **影响** | 维护性：`errors.Is()` 根据导入的变量工作；使用错误哨兵值会增加耦合度。 |
| **工作量** | S |

---

### #10 — 中：文件大小检查排除 mocks

| 字段 | 内容 |
|------|------|
| **类别** | 质量 / 工程标准 |
| **严重程度** | **中 (Medium)** |
| **标题** | `make check-file-sizes` 排除了 `mocks` 目录，但 `mocks.go` 有 639 行 |
| **位置** | `Makefile` 第 30 行 + `internal/service/mocks/mocks.go` (639 行) |
| **描述** | `make check-file-sizes` 使用 `! -path '*/mocks/*'` 排除 mocks，但 mocks 文件是手写维护的，不是生成代码。639 行的文件违反了"文件 ≤500 行"的原则，且测试依赖它。 |
| **现状** | `internal/service/mocks/mocks.go` 对所有服务接口都有手写 mock，共 639 行。 |
| **推荐状态** | 生成 mock（使用 `go.uber.org/mock/mockgen`）或将其拆分为多个文件（每个领域一个 mock 文件）。AGENTS.md 的拆分策略应适用于此。 |
| **影响** | 维护性：大文件难以导航；添加新服务接口时需要在其末尾手动添加 mock 方法。 |
| **工作量** | M |

---

### #11 — 低：没有 TODO/FIXME 跟踪

| 字段 | 内容 |
|------|------|
| **类别** | 技术债务 |
| **严重程度** | **低 (Low)** |
| **标题** | Go 源代码中未发现 TODO/FIXME/HACK 注释 |
| **位置** | （所有文件） |
| **描述** | 代码库中有零个 TODO、FIXME、HACK 或 XXX 注释。虽然这看起来是干净的信号，但实际上这可能意味着技术债务没有被追踪。12,000 行死代码就是一个例子——如果进行了标记，可能早就被处理了。 |
| **现状** | 没有 TODO 注释。 |
| **推荐状态** | 鼓励团队使用 TODO 注释来标记技术债务项，并考虑在 CI 中部署待办事项追踪器。 |
| **影响** | 维护性：没有正式的方法来识别和优先处理技术债务。 |
| **工作量** | S |

---

### #12 — 低：测试覆盖率缺口

| 字段 | 内容 |
|------|------|
| **类别** | 测试 |
| **严重程度** | **低 (Low)** |
| **标题** | 多个包缺乏测试文件 |
| **位置** | `internal/app`, `internal/pkg/redis`, `internal/repository/testpg` |
| **描述** | 三个包没有 `_test.go` 文件。虽然 `testpg` 和 `aeropb`/`ssopb` 是集成/生成代码，但 `internal/app`（组合根）没有任何测试。`internal/pkg/redis` 也缺少测试。 |
| **现状** | 关键装配逻辑没有测试。 |
| **推荐状态** | 为 `internal/app` 添加集成测试，验证组合能在真实数据库等条件下启动。为 `internal/pkg/redis` 添加单元测试以验证连接逻辑。 |
| **影响** | 可靠性：只能通过手动测试或生产环境来发现装配错误。 |
| **工作量** | M |

---

## 二、代码质量指标

| 指标 | 当前值 | 目标值 | 状态 |
|--------|--------|--------|--------|
| 圈复杂度 | ≈ 中等（未检查） | < 10 | ⚠️ `funlen`/`cyclop` linter 已配置但未在 `make test` 中运行 |
| 函数长度 | 大部分 < 50 行 | < 50 行 | ✅（linter 强制执行） |
| 测试覆盖率 | 未知（未测量） | > 80% | ⚠️ `go test` 未启用覆盖率模式 |
| 代码重复 | 高（12K 死代码 + 重复的行类型） | < 5% | ❌ 双架构问题 |
| 文档覆盖率 | 良好 | > 70% | ✅ 顶级导出有文档，转换函数有 Go 风格注释 |
| 文件大小限制 | 0 个违规文件 | ≤ 500 行 | ✅（但 9 个文件 > 400 行） |

---

## 三、技术债务登记册

| 项目 | 影响 | 工作量 | 优先级 | 备注 |
|------|--------|--------|----------|-------|
| 7 个死代码包（12K 行） | 高 | XL | P0 | 清理或完成迁移 |
| 重复的 GORM 行类型 | 高 | M | P0 | schema 变更时运行风险 |
| 导入技巧 `var _ = idgen.NewID` | 低 | S | P1 | 容易修复 |
| 9 个文件 > 400 行 | 中 | M | P1 | 拆分 |
| 错误被静默丢弃（`_ =`） | 中 | S | P1 | 添加日志记录 |
| `context.Background()` 泄漏 | 中 | M | P1 | 更改为可取消的 context |
| 双调度器 | 中 | S | P1 | 作为 #1 清理的一部分 |
| 错误哨兵值重复 | 低 | S | P2 | 统一 |
| Mocks 文件 639 行 | 中 | M | P2 | 生成或拆分 mock |
| 无 TODO 跟踪 | 低 | S | P3 | 流程变更 |
| 测试覆盖率缺口 | 中 | M | P2 | 添加 app/redis 测试 |
| 关于死代码的搜索包 | 中 | M | P1 | 作为 #1 清理的一部分 |

---

## 四、整体总结

### 整体代码质量：**有待改进 (Needs Work)**

### 须修复的关键质量问题：
1. **12,000 行死代码**——这是最大问题。7 个领域包完全未被使用，代表一个被遗弃的架构迁移。要么删除它们，要么完成迁移。在当前状态下，它们使构建变慢、使开发者困惑，并增加了维护成本。
2. **`domain/comment.go` 中的导入技巧**——这是一个易碎的模式，表明设计存在缺陷：领域类型不需要导入 ID 生成器。
3. **9 个文件接近 500 行限制**——根据 AGENTS.md 自身的原则，应立即拆分。

### 维护性问题：
- 两个并行架构意味着任何 schema 变更或业务逻辑更新都必须两处都改（如果只有一处，就是 bug）。
- 静默丢弃错误意味着关键审计日志和存储清理故障不会被注意到。
- 使用 `context.Background()` 而非可取消 context 的后台 goroutine 可能在关闭时泄漏。

### 技术债务：
- 被遗弃的迁移是最大债务项。它增加了约 50% 的代码库体积，却没有带来任何价值。
- Mock 是手写维护的（639 行），而不是生成自接口，增加了维护成本。
- 配置、装配和 Redis 连接缺少测试覆盖。

### 快速改善项：
1. **删除 `var _ = idgen.NewID`** 和多余的导入——2 分钟修复。
2. **为 `_ = s.audit.Log(...)` 添加日志记录**——查找所有约 20 个使用 `_ =` 忽略审计日志错误的地方，并添加 `s.log.Warn(...)`。约 1 小时工作。
3. **将 `context.Background()` 重构为可取消的上下文**——约 2 小时工作。
4. **拆分 `internal/service/comment.go`**（481 行）——按照已有的 `comment_reactions.go` 模式，提取 `service_comment_moderation.go` 来处理报告/隐藏/取消隐藏的逻辑。约 1 小时工作。

### 建议的行动计划：
1. **P0：** 做出清理还是完成迁移的决定。如果是清理，立即删除 7 个死代码包（约 12K 行）。如果是完成，预留 1-2 周的迁移工作。
2. **P1：** 修复导入技巧、添加可取消的上下文、为大文件添加拆分计划。
3. **P1：** 将 `_ =` 错误丢弃替换为日志记录。
4. **P2：** 生成 mock（或拆分 639 行的 mocks 文件）。
5. **P2：** 为组合根和 Redis 客户端添加测试。
