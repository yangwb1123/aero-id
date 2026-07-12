Now I have a comprehensive understanding of the codebase. Let me create the Tech Lead analysis.

---

# Tech Lead 分析报告：aero-id 四项高价值扩展

## 1. 任务分解

### 方向一：多副本 Outbox + SSE（P0）

| 任务 ID | 任务标题 | 涉及文件 | 前置依赖 | 预估工时 | 验收标准 |
|---|---|---|---|---|---|
| TASK-001 | FetchUnpublished 加 `FOR UPDATE SKIP LOCKED` | `internal/outbox/repository.go` | — | 2h | 多个 relay 实例并发 drain 不争抢相同事件行；集成测试验证并发 fetch 不重复 |
| TASK-002 | SSE Hub 改 Redis Pub/Sub 实现 | `internal/hub/hub.go`, `internal/hub/adapter.go`, `internal/hub/redis.go` | TASK-001 | 8h | 多实例部署下，一个实例 Publish 后所有实例的订阅者均收到事件；Hub 接口不变 |
| TASK-003 | SSE Push 移到 MarkPublished 之后（at-least-once 语义） | `internal/outbox/relay.go` | TASK-001 | 3h | relay crash 后事件重放不会产生重复 SSE push；新增 `HandleAndAck` 回调接口 |
| TASK-004 | 添加 Notif Dedup（去重）机制 | `internal/service/notification.go` (dispatcher) | TASK-003 | 4h | 同一事件被重放时，基于 (recipient, type, actor, subject) 的幂等键去重，不创建重复 notification row |
| TASK-005 | 添加 Redis Session Registry（断线重连时的 Hub 归属查询） | `internal/hub/session.go`, `internal/handler/http/events.go` | TASK-002 | 4h | SSE 客户端断开后通过 `Last-Event-ID` 重连时，从正确的实例恢复事件流 |

**方向一总计：21h**

### 方向二：异步进程可观测性（P1）

| 任务 ID | 任务标题 | 涉及文件 | 前置依赖 | 预估工时 | 验收标准 |
|---|---|---|---|---|---|
| TASK-006 | 为 6 个 Cleaner 添加 Prometheus metrics | `internal/pkg/metrics/metrics.go`, `internal/repository/audit.go`, `internal/repository/notification.go`, `internal/outbox/repository.go`, `internal/idempotency/store.go`, `internal/sso/token_cache.go` | — | 4h | 每个 cleaner 暴露：`aeroid_cleaner_total{cleaner="..."}` (counter)，`aeroid_cleaner_duration_seconds` (histogram)，`aeroid_cleaner_deleted_total` (counter) |
| TASK-007 | 用带 cancel 的 context 替换 context.Background() | `internal/outbox/relay.go`, `internal/outbox/repository.go`, `internal/repository/audit.go`, `internal/repository/notification.go`, `internal/idempotency/store.go`, `internal/sso/token_cache.go`, `internal/storage/gc/sweeper.go`, `internal/middleware/degradation.go` | — | 2h | 所有后台 goroutine 接收从 `app.New()` 传递的 cancel-aware context，优雅关闭 |
| TASK-008 | 添加结构化日志（已处理的记录数、耗时、错误详情） | 同上 | TASK-007 | 2h | 每个 cleaner 每次运行输出结构化日志行，包含 duration 和 deleted count（error 时含 zap.Error） |

**方向二总计：8h**

### 方向三：文件魔数校验（P1）

| 任务 ID | 任务标题 | 涉及文件 | 前置依赖 | 预估工时 | 验收标准 |
|---|---|---|---|---|---|
| TASK-009 | 实现魔数检测库 | `internal/pkg/magic/magic.go` | — | 4h | 对常见类型（JPEG/PNG/GIF/PDF/ZIP）的魔数正确识别；覆盖所有 `mimeCategoryMap` 中列出的类型 |
| TASK-010 | File 实体添加 `DetectedMimeType` 字段 + migration | `internal/domain/file.go`, `migrations/xxxxx_add_detected_mime_type.up.sql`, `migrations/xxxxx_add_detected_mime_type.down.sql` | TASK-009 | 2h | `domain.File` 结构体包含 `DetectedMimeType` 字段；migration 兼容现有数据 |
| TASK-011 | `PresignUpload` 只在 Content-Type 层面校验，`CompleteUpload` 补魔数校验 | `internal/file/service_upload.go` | TASK-009, TASK-010 | 3h | `CompleteUpload` 从 storage 读取前 n 字节做魔数检测，DetectedMimeType != declared MimeType 时返回 BadRequest (`file.mime_mismatch`) |
| TASK-012 | 魔数检测在直接上传路径中生效 | `internal/file/service_crud.go` | TASK-009 | 2h | `Create`（直接上传）也走魔数检测，与 `CompleteUpload` 行为一致 |
| TASK-013 | 完善文件类型分类（`ClassifyFile` 优先用 `DetectedMimeType`） | `internal/domain/file.go` | TASK-010 | 1h | `File.Category` 的推导优先使用 `DetectedMimeType`，再回退到 `MimeType` |

**方向三总计：12h**

### 方向四：Counter 一致性（P2）

| 任务 ID | 任务标题 | 涉及文件 | 前置依赖 | 预估工时 | 验收标准 |
|---|---|---|---|---|---|
| TASK-014 | 设计 counter drift 检测修复接口 | `internal/repository/comment.go` (interface), `internal/domain/comment.go` | — | 3h | 新增 `DetectCounterDrift` 方法遍历所有 comment，对照真实 COUNT(*) 和 denormalized 字段报告偏差 |
| TASK-015 | 实现 `like_count` 修复命令 | `internal/comment/repository.go` | TASK-014 | 2h | `RepairLikeCount(commentID)` 用 `SELECT COUNT(*) FROM comment_likes WHERE comment_id = ?` 修复；支持 dry-run |
| TASK-016 | 实现 `reply_count` 修复命令 | `internal/comment/repository.go` | TASK-014 | 2h | `RepairReplyCount(parentID)` 用 `SELECT COUNT(*) FROM comments WHERE parent_id = ? AND deleted_at IS NULL` 修复 |
| TASK-017 | 添加 counter 每日校正 cron（可配置） | `internal/app/app.go`, `internal/app/cron.go` 新文件 | TASK-015, TASK-016 | 3h | 启动一个可选的定时任务（默认关闭，i.e. opt-in），每天凌晨运行，扫偏差并修复，记录日志 + metrics |

**方向四总计：10h**

---

## 2. 执行顺序

```mermaid
graph TD
    %% 方向一：多副本 Outbox + SSE
    subgraph "方向一 (P0) — 21h"
        T001[TASK-001<br/>FetchUnpublished<br/>+ FOR UPDATE SKIP LOCKED<br/>2h]
        T002[TASK-002<br/>SSE Hub Redis Pub/Sub<br/>8h]
        T003[TASK-003<br/>SSE Push 后置到<br/>MarkPublished 之后<br/>3h]
        T004[TASK-004<br/>Notif Dedup 去重<br/>4h]
        T005[TASK-005<br/>Redis Session Registry<br/>4h]
        
        T001 --> T003
        T001 --> T002
        T002 --> T005
        T003 --> T004
    end

    %% 方向二：可观测性
    subgraph "方向二 (P1) — 8h"
        T006[TASK-006<br/>Cleaner Prometheus metrics<br/>4h]
        T007[TASK-007<br/>Context.Background() →<br/>cancel-aware context<br/>2h]
        T008[TASK-008<br/>结构化日志增强<br/>2h]
        
        T007 --> T008
        T006 -.-> T007 可选并行
    end

    %% 方向三：文件魔数校验
    subgraph "方向三 (P1) — 12h"
        T009[TASK-009<br/>魔数检测库<br/>4h]
        T010[TASK-010<br/>DetectedMimeType<br/>字段 + migration<br/>2h]
        T011[TASK-011<br/>CompleteUpload<br/>魔数校验<br/>3h]
        T012[TASK-012<br/>直接上传路径<br/>魔数校验<br/>2h]
        T013[TASK-013<br/>ClassifyFile 优先<br/>DetectedMimeType<br/>1h]
        
        T009 --> T010
        T009 --> T011
        T009 --> T012
        T010 --> T011
        T010 --> T013
        T011 -.-> T012 可并行
    end

    %% 方向四：Counter 一致性
    subgraph "方向四 (P2) — 10h"
        T014[TASK-014<br/>Counter drift 检测<br/>设计 + interface<br/>3h]
        T015[TASK-015<br/>like_count 修复<br/>2h]
        T016[TASK-016<br/>reply_count 修复<br/>2h]
        T017[TASK-017<br/>每日校正 cron<br/>3h]
        
        T014 --> T015
        T014 --> T016
        T015 --> T017
        T016 --> T017
    end

    %% 跨方向依赖
    T001 -.-> T011 可选: 方向一无阻塞方向三
    T007 -.-> T017 可选: context 模式可复用
```

### 可并行执行的任务组

| 并行组 | 包含任务 | 说明 |
|---|---|---|
| **G1** | TASK-001, TASK-002 | 方向一基础：锁 + Hub 改造，无交叉依赖 |
| **G2** | TASK-006, TASK-007 | 方向二：metrics 和 context 可独立设计 |
| **G3** | TASK-009 | 方向三魔数检测库是其他任务的前置，可优先启动 |
| **G4** | TASK-014 | 方向四的驱动设计，独立于其他方向 |
| **G5** | TASK-001, TASK-009, TASK-014 | 三个方向的基座任务可以完全并行 |

---

## 3. 技术风险

### 3.1 方向一：多副本 Outbox + SSE

| 风险 | 等级 | 描述 | 缓解策略 |
|---|---|---|---|
| Redis 单点故障 | **高** | SSE Hub 依赖 Redis Pub/Sub；Redis 宕机导致 SSE 全停 | — 引入 Redis Sentinel/Cluster 高可用 — fallback 到 in-memory-only 模式（降级不阻塞业务） |
| `FOR UPDATE SKIP LOCKED` 锁升级 | **中** | PostgreSQL 行锁可能升级为页锁（大量并发 drain），影响吞吐 | 保持每批 `defaultBatchSize=100`；监控 `pg_locks`；必要时加 `ORDER BY id` 限制锁范围 |
| SSE push 重放爆炸 | **中** | 即使移到 `MarkPublished` 之后，crash 后重放仍会产生重复 SSE | 结合 TASK-004 去重：客户端用 `Last-Event-ID` 去重；服务端幂等键 (recipient, type, actor, subject, created_at 窗口) |
| Redis Pub/Sub 消息丢失 | **中** | Redis Pub/Sub 不持久化；连接断开会丢失中间消息 | 客户端用 `Last-Event-ID` 重连补救；可接受短期实时消息丢失（通知最终一致性） |
| 跨实例 Session 路由 | **低** | 需要将 SSE 客户端路由到正确的实例 | 引入 WebSocket 网关或 Session Registry（TASK-005）；MVP 可容忍反代轮询重连 |

### 3.2 方向二：异步进程可观测性

| 风险 | 等级 | 描述 | 缓解策略 |
|---|---|---|---|
| Context 传递路径重构 | **中** | 当前所有 Cleaner 签名是 `func(...) func()` 无 context 参数；改签名影响所有调用方和测试 | 新增加 `WithContext(ctx)` 选项模式，向后兼容；分两步改：先加 option，后统一切换 |
| Metrics 基数膨胀 | **低** | 每个 Cleaner 加 label（cleaner 名、phase）基数低，风险小 | 使用固定的 label 集（`cleaner`, `phase`） |
| 日志噪声 | **低** | 空跑时每次输出日志可能嘈杂 | 无数据时不输出（当前已做 `deleted > 0` 判定） |

### 3.3 方向三：文件魔数校验

| 风险 | 等级 | 描述 | 缓解策略 |
|---|---|---|---|
| 魔数库维护成本 | **中** | 需要覆盖所有支持的 MIME 类型（约 40 种），有些类型无标准魔数（如 SVG, Markdown） | — 对于有魔数的类型严格检测 — 无魔数类型回退到扩展名检查或 Content-Type 信任 |
| 大文件首字节读取成本 | **中** | `CompleteUpload` 要从 storage 读取文件头；S3 需要额外 GET 请求（开销 ~1 HTTP + 网络） | — 只读前 512 bytes（`Range: bytes=0-511`）— 本地存储直接 `os.Open` + `Read` |
| `Stat` 失效 | **低** | S3 `Stat`（HeadObject）不读内容，魔数检测需额外 GetObject — 增加 $ | 每上传多一次 GET 请求；对大规模文件上传场景可配置关闭魔数检测 |
| Content-Type 魔数不匹配 | **低** | 合法文件被前导零等干扰误判 | 魔数检测库使用 lenient 策略：仅检测已知魔数，非魔数字节跳过 |

### 3.4 方向四：Counter 一致性

| 风险 | 等级 | 描述 | 缓解策略 |
|---|---|---|---|
| 大规模数据修复的 DB 压力 | **中** | `SELECT COUNT(*)` 在大表上消耗资源；夜间全表扫描可能锁竞争 | — 分批扫描（`WHERE id > last_id ORDER BY id LIMIT 1000`）— 使用可重复读隔离级别 — 默认关闭，opt-in 启动 |
| 修复窗口期数据不一致 | **低** | 校正扫描期间又有新的点赞/回复写入，修复完又有偏差 | — 校正不是强一致的，最终一致性可接受 — 每次修复记录 `repaired_at`，下次跳过 5 分钟内的记录 |
| `reply_count` 包含软删除 | **中** | 当前 `reply_count` 在 normal 回复创建时递增，但软删除不递减 | 厘清语义：`reply_count` 是「累计回复数」还是「当前可见回复数」？建议保持「累计」语义（不递减），修复按此语义 |
| `like_count` 因 `ON CONFLICT DO NOTHING` 无递减风险 | **低** | 分析已验证 `Like` 用 atomic UPDATE 且 `Unlike` 用 `GREATEST(like_count - 1, 0)` | 修复脚本对齐此语义 |

---

## 4. 资源评估

### 4.1 人员配置

| 角色 | 数量 | 技能要求 | 负责方向 |
|---|---|---|---|
| **Senior Go Engineer** | 1 | Go 并发、PostgreSQL 锁机制、Pub/Sub 架构 | 方向一（核心） |
| **Full-stack Go Engineer** | 1 | Go、文件格式知识、Prometheus metrics | 方向二 + 方向三 |
| **Backend Engineer** | 1 | Go、数据库知识、数据修复脚本书写 | 方向四 + 方向一辅助 |
| **QA Engineer** | 1 | 集成测试、多副本场景、性能测试 | 全方向覆盖 |

**推荐：2 个 Senior + 1 个 Mid-level + 1 个 QA**

### 4.2 关键里程碑

| 里程碑 | 时间点 | 交付物 | 验收依据 |
|---|---|---|---|
| M1: 基础设施就绪 | 第 3 天末 | TASK-001(FetchUnpublished SKIP LOCKED) + TASK-009(魔数库) 完成 | `make test && make test-integration` 通过 |
| M2: 核心功能可用 | 第 5 天末 | 方向一核心(TASK-001/002/003) + 方向三核心(TASK-009/010/011) | 双实例部署验证 SSE 跨实例广播；恶意修改 Content-Type 上传被拦截 |
| M3: 可观测性到位 | 第 6 天末 | 方向二全部(TASK-006/007/008) | `curl /metrics` 包含所有 cleaner 指标；服务优雅关闭 |
| M4: 全功能就绪 | 第 8 天末 | 方向一(TASK-004/005) + 方向四(TASK-014/015/016) 完成 | Notif 去重正确；counter 修复脚本 dry-run 报告偏差 |
| M5: 加固上线 | 第 10 天末 | TASK-017 + 全面集成测试 + 性能压测 | 修复 cron 生产就绪；压测无退化；文档更新 |

### 4.3 阻塞点及解决策略

| Blocking | 描述 | 解决策略 |
|---|---|---|
| **Redis 依赖** | 方向一需要 Redis；当前项目无 Redis 依赖 | — 引入 `go-redis/redis` v9 — 添加 docker-compose 集成测试 — 配置中 `hub.type: "redis"` / `"memory"` 可切换 |
| **protoc 工具链** | 若需要新增 gRPC service 需要 protoc | 本分析不涉及新 gRPC 接口；无阻塞 |
| **多副本环境验证** | 本地 `docker-compose` 需要启两个实例 + Redis | 添加 `docker-compose.multi.yml` 预配置镜像 |
| **S3 读取开销** | 魔数检测需额外 GET；S3 有成本 | 文档中记录成本估算；支持配置跳过魔数检测 |

---

## 5. 质量保证

### 5.1 单元测试覆盖要求

| 任务 | 覆盖要求 | 关键测试场景 |
|---|---|---|
| TASK-001 SKIP LOCKED | 100% | 并发 fetch 不重复、并发 fetch 均获取到数据、空表返回空 |
| TASK-002 Redis Hub | 90% | Publish 到所有实例的订阅者、去掉订阅者、Hub 关闭后的 Publish |
| TASK-003 后置 SSE push | 100% | Handle 后 MarkPublished 失败、crash 重放无重复 push（mocked） |
| TASK-006 Cleaner metrics | 90% | 每次运行 metrics 正确递增、空跑无删除 metrics=0 |
| TASK-009 魔数库 | 100% | JPEG/PNG/GIF/WEBP/PDF/ZIP 正确识别；空文件、随机字节不误报 |
| TASK-011 CompleteUpload 魔数 | 90% | 魔数不匹配 → BadRequest；匹配 → OK；Stat 失败 → Internal |
| TASK-014/015 Counter 修复 | 90% | 偏差检测正确；修复后 COUNT 一致；空跑无更改 |

### 5.2 集成测试策略

| 测试场景 | 工具/环境 | 频次 |
|---|---|---|
| **多实例 outbox 并发 drain** | `make test-integration`，PostgreSQL testcontainers，开两个 relay goroutine 竞争 drain | 每次方向一 PR |
| **SSE 跨实例广播** | docker-compose up 两个实例 + Redis，curl SSE 端验证双实例收到 | 手动测试 + CI 脚本 |
| **文件魔数端到端** | `make test-integration`，MinIO testcontainers，上传改 MIME 文件验证拦截 | 每次方向三 PR |
| **Cleaner metrics `/metrics`** | `go test -tags=integration` 启动 service，curl metrics 端点 | 每次方向二 PR |
| **Counter 修复 dry-run** | `make test-integration`，操作故意产生偏差（手动 INSERT），运行修复验证 | 每次方向四 PR |

### 5.3 代码审查要点

| 关注领域 | 审查要点 |
|---|---|
| **并发安全** | `FOR UPDATE SKIP LOCKED` 正确使用、事务边界不扩大、锁等待超时 |
| **Redis 连接** | 连接池配置、断线重连、Key 前缀隔离（prod/staging） |
| **Monkey-patching** | 不要在 `domain` 层引入外部依赖；magic 库必须是纯 Go（无 CGO） |
| **向后兼容** | `CompleteUpload` 签名不变；`File` 实体新字段零值默认；Hub 接口不破坏已有测试 |
| **Config** | 新配置项有 `validate()` 检查；env override 遵循 `AERO_ID_*` 模式 |
| **Metrics 命名** | 遵循 `aeroid_*` 前缀 + `_total`/`_seconds`/`_count` Prometheus 规范 |
| **Migration** | 空值处理：现有行的 `detected_mime_type` 设置为空串（回退到 `mime_type`） |

### 5.4 性能测试需求

| 测试 | 场景 | 目标 | 工具 |
|---|---|---|---|
| **Outbox 并发 drain** | 3 个 relay 实例，10K 待发送事件 | 每秒 drain > 500 events，无重复 | Go benchmark + testcontainers |
| **SSE Hub 吞吐** | 10K 订阅者 1 个 uid，每秒 100 次 Publish | P99 延迟 < 10ms，不丢消息（buffer=16） | 自写 benchmark |
| **CompleteUpload 魔数开销** | 1MB/10MB 文件，S3/MinIO | 魔数检测增加耗时 < 200ms | `go test -bench` |
| **Counter 修复扫描** | 10M comments 表 | 全表扫描 < 5 分钟（batch size=1000） | 线下测试 |

---

## 6. 实施计划

### 甘特图

```mermaid
gantt
    title aero-id 四项高价值扩展实施计划
    dateFormat  YYYY-MM-DD
    axisFormat  %m-%d

    section 方向一 (P0) — 多副本 Outbox+SSE
    TASK-001 FetchUnpublished SKIP LOCKED   :a1, 2026-07-13, 1d
    TASK-002 SSE Hub Redis Pub/Sub          :a2, 2026-07-13, 2d
    TASK-003 SSE Push 后置 MarkPublished    :a3, after a1, 1d
    TASK-004 Notif 去重机制                  :a4, after a3, 1d
    TASK-005 Redis Session Registry         :a5, after a2, 1d

    section 方向二 (P1) — 可观测性
    TASK-006 Cleaner Prometheus metrics     :b1, 2026-07-14, 1d
    TASK-007 Context 替换                   :b2, 2026-07-14, 1d
    TASK-008 结构化日志增强                  :b3, after b2, 1d

    section 方向三 (P1) — 文件魔数校验
    TASK-009 魔数检测库                     :c1, 2026-07-13, 2d
    TASK-010 DetectedMimeType 字段+迁移     :c2, after c1, 1d
    TASK-011 CompleteUpload 魔数校验        :c3, after c1, 1d
    TASK-012 直接上传路径魔数校验            :c4, after c1, 1d
    TASK-013 ClassifyFile 优先检测类型       :c5, after c2, 0.5d

    section 方向四 (P2) — Counter 一致性
    TASK-014 Drift 检测设计                 :d1, 2026-07-15, 1d
    TASK-015 like_count 修复                :d2, after d1, 1d
    TASK-016 reply_count 修复               :d3, after d1, 1d
    TASK-017 每日校正 cron                  :d4, after d2, after d3, 1d

    section 集成测试 & 加固
    M1 验证 (SKIP LOCKED + 魔数库)          :m1, 2026-07-14, 0d
    M2 验证 (方向一核心 + 方向三核心)        :m2, 2026-07-16, 0d
    M3 验证 (可观测性)                      :m3, 2026-07-16, 0d
    多副本集成测试                           :it1, after a5, 2d
    性能压测                                 :pt1, after it1, 1d
    文档 & Changelog 更新                    :docs, after pt1, 1d
    M4 上线就绪                             :release, after pt1, 0d
```

### 阶段划分

#### **阶段 1：基础设施搭建（第 1-2 天）**
- 团队分配：2 人并行
  - **工程 A**：TASK-001（SKIP LOCKED） + TASK-002（Redis Hub）
  - **工程 B**：TASK-009（魔数检测库）
- 产出：
  - `internal/outbox/repository.go` 修改完成
  - 引入 `go-redis/redis`
  - `internal/hub/redis.go` 实现
  - `internal/pkg/magic/magic.go` 完成
  - 单元测试覆盖
- **验证**：`M1` 里程碑 (Day 2 末)

#### **阶段 2：核心功能实现（第 3-5 天）**
- **工程 A**：TASK-003（SSE push 后置）+ TASK-006/007（metrics + context）
- **工程 B**：TASK-010（DetectedMimeType）+ TASK-011（魔数校验）+ TASK-012（直接上传路径）
- **工程 C**（QA）：搭建 `docker-compose.multi.yml` 多副本环境
- 产出：
  - Relay 处理顺序修正
  - 文件上传双路径魔数校验
  - 6 个 Cleaner 的 metrics 和 context 改造
- **验证**：`M2` + `M3` 里程碑 (Day 5 末)

#### **阶段 3：集成测试和优化（第 6-8 天）**
- 所有工程师：
  - TASK-004（Notif 去重）
  - TASK-005（Redis Session Registry）
  - TASK-014/015/016（Counter 检测修复）
  - TASK-008（结构化日志增强）
  - TASK-013（ClassifyFile 优先魔数）
- QA：全量集成测试 + 性能压测
- **验证**：`M4` 里程碑 (Day 8 末)

#### **阶段 4：发布准备（第 9-10 天）**
- TASK-017（每日校正 cron）
- 全量回归：`make test && make test-integration && make lint`
- 配置文件更新（`configs/config.yaml` 新增 `magic_detection.enabled`、`hub.type`、`counter_repair.schedule`）
- `CHANGELOG.md`、`README.md` 更新
- Code Review + 架构决策记录（ADR）
- **上线窗口**：Day 10 末

---

## 7. 工作量总结

| 方向 | 任务数 | 总工时 | 优先级 | 风险等级 |
|---|---|---|---|---|
| 方向一：多副本 Outbox + SSE | 5 | 21h | **P0** | **高**（多实例生产阻塞） |
| 方向二：异步进程可观测性 | 3 | 8h | **P1** | **低**（无后患，运维改进） |
| 方向三：文件魔数校验 | 5 | 12h | **P1** | **中**（安全合规需求） |
| 方向四：Counter 一致性 | 4 | 10h | **P2** | **低**（事务内已原子，Edge case） |
| **合计** | **17** | **51h** | | |

**建议并行度**：3 人开发 + 1 人 QA → **约 5~7 个工作日**（含集成测试和压测）。

**首先冲刺方向一（P0）**，方向二和方向三可在第 2 天开始并行不阻塞。方向四（P2）可放在第 2 周或发布后迭代。
