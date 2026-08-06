# aero-id — 生产交付基础设施缺口（架构师/产品经理双视角）

> **日期**: 2026-07-11  
> **视角**: 资深架构师 + 产品经理  
> **方法**: 全量代码库通读（`internal/` 全部 Go 包 + `cmd/` + `configs/` + `deployments/` + `.github/workflows/` + `Makefile` + `migrations/`），与 `docs/requirements/` 下 137+ 份既有分析文档进行交叉比对  
> **核心承诺**: 以下 **5 个方向** 聚焦于 **生产交付基础设施（Production Delivery Infrastructure）** 的成熟度缺口——不是新功能，而是让已有功能可以安全、可靠地部署到生产环境的必备基础设施。每个方向均附行级代码证据。  
> **约束**: 不编写任何代码。

---

## 代码库成熟度摘要（与本文范畴的交集）

aero-id 已在功能、可靠性、可观测性方面达到极高成熟度。但 **"如何将代码交付到生产"** 这一维度存在系统性空白：

| 领域 | 代码功能成熟度 | 交付基础设施成熟度 |
|------|---------------|-------------------|
| **应用代码** | 22 份可逆迁移 · 双传输完全对等 · 事务性 Outbox · SSO 弹性 · SSE 实时推送 · FTS 搜索 · 管理角色 · 审计日志 | **无版本化发布流程 · 无 CHANGELOG 自动化 · 无 Artifact 签名** |
| **测试** | 27 包单元测试 · 21 包集成测试（testcontainers） · `make check-file-sizes` · `lint-strict` | **集成测试不在 CI 中运行 · 无性能基准测试 · 无混沌测试** |
| **部署** | 单阶段 Dockerfile · docker-compose.yml · GitHub Actions CI | **Docker Compose 引用不存在的 SSO 服务 · 无健康检查 · 无 K8s 清单 · 无 Helm Chart · 无 Terraform** |
| **运维** | RESTART 策略 · 优雅关停 · 冷启动预热 · per-dependency `/readyz` · RED 指标 | **无备份/恢复流程 · 无数据完整性巡检 · 无迁移验证工具 · 无 Runbook** |
| **配置** | 完整 Viper 配置 · 环境变量覆盖 · 启动时校验 | **无配置版本化 · 无热加载 · 无 dev/staging/prod 环境配置模板** |

---

## 方向一：集成测试在 CI 中完全缺席——数据库 Schema 变更存在盲区

### 性质

**质量基础设施缺口** — 22 份 SQL 迁移文件定义了完整的数据模型，但验证它们正确性的集成测试从未在 CI 中运行。

### 代码级证据

**证据 1：`.github/workflows/ci.yml` 只运行单元测试**

```yaml
# .github/workflows/ci.yml:38-44
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: ${{ env.GO_VERSION }}
          cache: true
      - run: go test ./... -count=1 -race  # ← 只运行 go test ./...
```

`go test ./...` **不带 `-tags=integration`**，因此所有带 `//go:build integration` 的文件被 Go 编译器自动排除。这意味着以下关键测试文件**从未在 CI 中执行**：

| 测试文件 | 覆盖内容 | 风险 |
|----------|----------|------|
| `internal/repository/block_integration_test.go` | Block CRUD + 索引 | Block 查询在实际 PG 上可能失败 |
| `internal/repository/comment_test.go` | Comment CRUD + 事务写 | `outbox` + `comment` 联合事务可能断裂 |
| `internal/repository/file_gc_integration_test.go` | GC 扫表 + 引用计数 | GC 逻辑依赖实际 DB 事务行为 |
| `internal/repository/file_share_integration_test.go` | 分享授权/撤销 | 分享查询依赖 PG 唯一约束 |
| `internal/repository/notification_integration_test.go` | 通知创建/列表/未读计数 | 复合索引优化不可验证 |
| `internal/repository/reaction_audit_integration_test.go` | Reactions + 审计日志 | 审计与 reaction 的事务完整性 |
| `internal/repository/search_integration_test.go` | FTS 查询 + CJK ILIKE 回退 | FTS 索引变更不可验证 |
| `internal/pkg/migrate/migrate_integration_test.go` | 迁移 up/down | 迁移文件损坏只能在集成测试中发现 |
| `internal/outbox/repository_integration_test.go` | Outbox 写入/轮询/死信 | Outbox 死信逻辑依赖 DB 行为 |

**证据 2：`Makefile` 中 `test` 与 `test-integration` 分离**

```makefile
# Makefile:11-12
test:        ; go test ./... -count=1              # ← 无 integration tag
test-integration: ; go test -tags=integration ./... -count=1  # ← 需要 Docker，本地手动执行
```

Devs 日常使用 `make test`（快速，不需要 Docker），**没有人在 CI 中运行 `make test-integration`**。

**证据 3：迁移文件可能无声地损坏**

22 份 SQL 迁移文件中包含复杂操作：

```sql
-- migrations/000018_outbox_retry.up.sql
DROP INDEX IF EXISTS idx_outbox_unpublished;
CREATE INDEX idx_outbox_unpublished
    ON outbox_events(created_at, id)
    WHERE published_at IS NULL AND NOT dead_letter;
```

这条迁移先 `DROP INDEX` 再 `CREATE INDEX`——如果 `dead_letter` 列尚未存在（例如迁移按错误顺序应用），`CREATE INDEX` 会失败。`migrate_integration_test.go` 覆盖了这个场景，但在 CI 中从不执行。

### 为什么需要（产品经理视角）

这直接对应 **"持续交付信心"** metrics：
- **当前状态**：Schema 变更 → 合入 `main` → CI 全绿 → 部署到 staging → 集成测试失败 → 回滚。反馈周期：小时级。
- **期望状态**：Schema 变更 → PR 触发 CI + 集成测试 → 10 分钟告知是否损坏。
- **具体影响**：当 22 份迁移累积到生产环境时，任何一次迁移顺序错乱、列类型变更、索引创建失败，都只能在上线后由 SRE 发现。以 `000015_perf_indexes` 为例——这条迁移创建复合部分索引，如果生产表已有百万行，`CREATE INDEX` 会长时间锁表。集成测试可以使用千行级 fixture 验证索引行为并预估锁表时间。

---

## 方向二：Docker Compose 环境不完整——依赖缺失导致开箱不可用

### 性质

**运维基础设施缺口** — 开发/演示用的 Docker Compose 堆栈引用了不存在的依赖服务，无法直接启动。

### 代码级证据

**证据 1：`deployments/docker-compose.yml` 中 `sso` 主机名无对应 service 定义**

```yaml
# deployments/docker-compose.yml:32-38
  aero-id:
    ...
    environment:
      AERO_ID_DATABASE_HOST: postgres
      AERO_ID_DATABASE_PASSWORD: aero
      AERO_ID_SIGNING_KEY: docker-dev-signing-key-change-me
      AERO_ID_SSO_ENDPOINT: "sso:9000"     # ← 指向 sso:9000，但 compose 中没有 "sso" service
    ports: ["8080:8080", "9090:9090"]
```

`"sso:9000"` 是一个 DNS 名称，在 Compose 网络中只会在存在名为 `sso` 的服务时才会解析。Compose 定义的服务只有 `postgres`、`migrate`、`minio` 和 `aero-id`。这意味着：

```bash
$ docker compose -f deployments/docker-compose.yml up
# → aero-id 容器启动后立即尝试连接 sso:9000
# → DNS 解析失败 → 连接超时 → 健康检查/readiness 永远无法通过
# → 堆栈不可用
```

**证据 2：aero-id 服务缺少 `healthcheck` 定义**

```yaml
# deployments/docker-compose.yml:32-46
  aero-id:
    build:
      context: ..
      dockerfile: deployments/Dockerfile
    depends_on:
      postgres:
        condition: service_healthy
      migrate:
        condition: service_completed_successfully
    environment:
      ...
    ports: ["8080:8080", "9090:9090"]
    # ← 没有 healthcheck: 定义
    # ← 没有 restart: unless-stopped
    # ← 没有 deploy.resources.limits
```

对比 `postgres` 服务的健康检查配置：

```yaml
  postgres:
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U aero -d aero_id"]
      interval: 5s
      timeout: 3s
      retries: 10
```

aero-id 自身的 HTTP `/healthz` 端点已实现（`internal/handler/http/health.go`），但 Compose 文件没有利用它来监控容器健康。

**证据 3：没有任何 `restart` 策略**

当 aero-id 进程因为 SSO 连接超时、OOM 或其他原因崩溃时，Docker 不会自动重启它。对于开发环境来说这导致频繁的手动干预；对于生产环境这是不可接受的。

### 为什么需要

- **入职体验**：新开发者运行 `docker compose up` 期望一切都可用——但如果 SSO 不存在，服务无法启动，开发者需要花时间调试环境问题。这直接拖慢 onboarding。
- **演示/展示**：无法快速拉起一个可用的演示环境。
- **CI 集成测试的基础**：当前集成测试用 testcontainers 自建 Postgres + MinIO，但如果 Compose 环境本身可用，集成测试可以直接利用已有的 Compose 服务，减少 CI 资源消耗。
- **Ops 兜底**：生产部署中 `restart: unless-stopped` 是最低要求，`healthcheck` 是自愈的前提。

---

## 方向三：异步后台操作使用 `context.Background()` —— 无超时、无追踪、无可取消

### 性质

**系统正确性缺口** — Outbox relay、Storage GC sweeper 和 SSO 缓存预热等后台 Goroutine 使用 `context.Background()`，导致超时无法传播、Trace 断裂、优雅关停期间可能无限阻塞。

### 代码级证据

**证据 1：Outbox relay 使用 `context.Background()`**

```go
// internal/outbox/relay.go:111-115
func (r *Relay) Start(pollInterval time.Duration) func() {
    go func() {
        ticker := time.NewTicker(pollInterval)
        defer ticker.Stop()
        for {
            select {
            case <-ticker.C:
                ctx := context.Background()     // ← 无超时、无传播
                r.drain(ctx)                    // ← 可能无限阻塞
            case <-r.stop:
                ctx := context.Background()
                r.drain(ctx)                    // ← 最后的 drain，仍然无超时
                return
            }
        }
    }()
    return func() { close(r.stop) }
}
```

具体风险：
1. **无超时**：`drain` 中调用 `FetchUnpublished` 和 `Handle`。如果一个事件 handler（例如 Webhook）卡住，`drain` 可能无限等待。
2. **无法取消**：shutdown 时调用 `r.drain(context.Background())`——如果 handler 无限阻塞，shutdown 永远不会完成。
3. **Trace 断裂**：event 创建时的 trace context 在 create 请求中，但 relay 使用全新的 Background context，导致父 span 和异步处理 span 之间完全断裂。

**证据 2：Storage GC sweeper 同样使用 `context.Background()`**

```go
// internal/storage/gc/sweeper.go:234 (在 worktree 中)
ctx := context.Background()
```

GC sweeper 执行 `ListSoftDeletedBefore` + `HardDelete` + 可选的 `List`（孤儿扫描）。如果某个操作超时，没有机制可以中断它。

**证据 3：SSO warmup 探针也有类似问题**

```go
// internal/app/app.go:344-354
probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
if err := a.sso.HealthCheck(probeCtx); err != nil {
    ...
}
cancel()
```

warmup 探针本身是正确的（使用带超时的派生 context），但**异步 relay 和 sweeper 没有采用相同模式**。不一致的 context 治理导致代码维护者难以判断应该使用哪个 context。

**证据 4：优雅关停的 shutdown 函数也有超时，但 relay drain 没有协同**

```go
// internal/app/app.go:370-374
func (a *App) shutdown() {
    ...
    if a.stopRelay != nil {
        a.stopRelay()       // ← 触发 relay 停止，但 relay 的 final drain 可能无限阻塞
    }
    ...
    shCtx, cancel := context.WithTimeout(context.Background(), a.cfg.Server.HTTP.ShutdownTimeout)
    defer cancel()
    if err := a.httpServer.Shutdown(shCtx); err != nil { ... }
    a.grpcServer.GracefulStop()
}
```

HTTP 和 gRPC 服务器的优雅关停都有超时保护（`ShutdownTimeout`），但 **relay 的 final drain 没有**。如果 relay 卡住，shutdown 函数会完成 HTTP/gRPC 关停，但 stopRelay 所在的 goroutine 可能永远不返回。

### 为什么需要（产品经理视角）

- **可靠性**：生产环境中后台组件的超时/取消机制不是"可选的优化"，而是**基本正确性要求**。一次慢速的 Webhook 投递不应阻塞整个 Outbox relay。
- **可观测性**：当前 OTel 追踪在跨 async 边界时断裂——无法回答"这个通知创建请求对应的 outbox event 何时被消费"这类问题。这对于诊断通知延迟至关重要。
- **运维**：当需要热重启部署时，relay 可能因为卡住的 handler 而阻止进程退出。K8s 会发送 SIGKILL（强制杀死），但优雅关停的优势就丢失了。

---

## 方向四：缺少 Kubernetes 部署基础设施——生产环境缺少标准交付方式

### 性质

**运维基础设施缺口** — 代码质量达到生产标准，但缺乏标准的生产部署方式（Kubernetes 清单、Helm Chart、部署策略）。

### 代码级证据

**证据 1：`deployments/` 目录中只有 Dockerfile 和 Compose 文件**

```bash
$ ls -la deployments/
docker-compose.yml
Dockerfile
# ← 没有 k8s/ 目录
# ← 没有 helm/ 目录
# ← 没有 terraform/ 目录
```

对于 K8s 环境，以下文件完全缺失：

| 必要文件 | 用途 | 当前状态 |
|----------|------|----------|
| `deployments/k8s/deployment.yaml` | Deployment 定义（副本数、滚动更新策略、资源限制） | ❌ |
| `deployments/k8s/service.yaml` | Service 定义（ClusterIP + gRPC 端口命名） | ❌ |
| `deployments/k8s/ingress.yaml` | Ingress 定义（HTTP 路由 + TLS 终止） | ❌ |
| `deployments/k8s/configmap.yaml` | ConfigMap（非敏感配置） | ❌ |
| `deployments/k8s/secret.yaml` | Secret 模板（数据库密码、签名密钥） | ❌ |
| `deployments/k8s/hpa.yaml` | HorizontalPodAutoscaler（自动扩缩） | ❌ |
| `deployments/k8s/pdb.yaml` | PodDisruptionBudget（高可用兜底） | ❌ |
| `deployments/k8s/network-policy.yaml` | NetworkPolicy（微隔离） | ❌ |
| `deployments/k8s/service-monitor.yaml` | ServiceMonitor（Prometheus 算子自动抓取） | ❌ |
| `deployments/helm/` | Helm Chart（参数化部署） | ❌ |

**证据 2：Docker image 没有版本化标签策略**

```dockerfile
# deployments/Dockerfile:1-2
FROM golang:1.26 AS build
...
# Image 被 `docker compose` 构建时没有 tag
# 没有 CI 步骤来打版本标签（git commit SHA / semver）
```

`.github/workflows/ci.yml` 只运行测试和 lint，没有 `docker build`、`docker push` 或镜像标签步骤：

```yaml
# .github/workflows/ci.yml — 没有 image build/push 步骤
jobs:
  lint: ...
  vet: ...
  build: ...
  test: ...
  # ← 没有 docker-build-push 步骤
  # ← 没有 image-tagging 步骤
```

**证据 3：没有 gRPC 端口命名约定**（K8s Service 需要）

当前 gRPC Server 在端口 9090 上监听：

```go
// internal/app/app.go:327
grpcAddr := fmt.Sprintf("%s:%d", cfg.Server.GRPC.Host, cfg.Server.GRPC.Port)
```

但在 K8s Service 中，gRPC 需要端口命名为 `grpc` 才能让 Istio/Linkerd 等服务网格识别并启用 mTLS 和 gRPC 负载均衡：

```yaml
# 需要的 K8s Service 配置
spec:
  ports:
  - name: grpc        # ← 命名约定，服务网格依赖此标识
    port: 9090
    targetPort: 9090
  - name: http
    port: 8080
    targetPort: 8080
```

**证据 4：没有 readiness/liveness probe 的 K8s 等效定义**

应用已经实现了：
- `/healthz`（存活探针）— `internal/handler/http/health.go`
- `/readyz`（就绪探针）— 检查 DB + SSO

但在 K8s 部署中，这些端点没有被配置为 liveness/readiness probes。

### 为什么需要（产品经理视角）

- **可部署性**：K8s 已成为生产部署的事实标准。没有 K8s 清单意味着每个新环境都需要手动配置，增加部署时间和出错概率。
- **可靠性**：HorizontalPodAutoscaler、PodDisruptionBudget 和滚动更新策略是将单实例服务演变为高可用生产系统的必要条件。
- **成本治理**：没有 Resource Limits / Requests，Pod 可能无限制地使用节点资源，影响同节点其他服务。
- **版本化发布**：没有 image tag 策略意味着无法快速回滚到上一个版本。当紧急修复需要部署时，找不到上一个已知良好的镜像。

---

## 方向五：数据完整性巡检与修复工具链缺失——计数器漂移、孤立引用、软删除残留无检测机制

### 性质

**运维基础设施缺口** — 系统有复杂的跨实体引用（文件→消息、评论→父评论、通知→actor）、计数列（`like_count`、`reply_count`、`collapsed_count`）和软删除语义，但**没有任何自动化的数据完整性巡检或修复工具**。

### 代码级证据

**证据 1：`like_count` 和 `reply_count` 是物化计数列，但没有任何校正机制**

```sql
-- migrations/000003_create_comments.up.sql
CREATE TABLE comments (
    ...
    like_count   INT NOT NULL DEFAULT 0,
    reply_count  INT NOT NULL DEFAULT 0,
    ...
);
```

这些计数器在写入时更新：

```go
// internal/comment/repository.go — Like 方法
func (r *repo) Like(ctx context.Context, commentID domain.ID, uid string) (int, error) {
    // INSERT INTO comment_likes ... ON CONFLICT DO NOTHING
    // UPDATE comments SET like_count = (SELECT count(*) FROM comment_likes WHERE comment_id = ?)
    // RETURNING like_count
}
```

当前实现通过 `SELECT count(*)` 原子地重算计数，看似避免了漂移。但：

1. **软删除不级联**：当一条评论被软删除时，其 `like_count` 和 `reply_count` 不会自动归零。`idx_comments_target` 索引的 `WHERE deleted_at IS NULL` 条件又确保软删除的评论不会被查询到——计数器在查询路径中被忽略，但数据本身从未被修正。
2. **并发 TOCTOU**：`Like` 和 `Unlike` 之间没有串行化——在高并发下，`SELECT count(*)` 和 `UPDATE` 之间存在微小窗口，理论上可能丢失更新。
3. **无定期巡检**：没有后台任务定期验证 `like_count` ＝ `SELECT count(*) FROM comment_likes WHERE comment_id = ?`，也没有工具在漂移发生时校正。

**证据 2：`collapsed_count`（通知折叠计数）无上限保护**

```sql
-- migrations/000020_notification_collapse.up.sql
ALTER TABLE notifications
    ADD COLUMN collapsed_count INT NOT NULL DEFAULT 1;
```

`collapsed_count` 在 dispatcher 中递增：

```go
// internal/notification/dispatcher.go — collapse 逻辑
// 当相同类型、相同 actor 的通知在 5 分钟内到达，递增 collapsed_count
```

对于一个高频互动的对话方，`collapsed_count` 可能增长到数万（例如 1000 条连续消息的未读提示折叠为一条）。没有上限保护：
- 客户端渲染时 `collapsed_count: 99999` 可能导致 UI 显示异常
- 数据库中没有 `CHECK (collapsed_count <= 999)` 约束

**证据 3：软删除的引用完整性无验证**

当前系统中，软删除的实体可能留下悬挂引用：

| 父实体 | 子实体 | 软删除影响 |
|--------|--------|-----------|
| `message` | `comment` (target_id) | 评论引用已删除消息 |
| `comment` | `comment` (parent_id) | 回复引用已删除父评论 |
| `file` | `message` (file_id) | 消息引用已删除文件 |
| `file` | `file_share` | 分享记录引用已删除文件 |

```go
// internal/comment/service.go — Delete
func (s *serviceImpl) Delete(ctx context.Context, id domain.ID) error {
    // 只设置 deleted_at，不检查子评论
    // 子评论的 parent_id 继续指向已删除的父评论
}
```

当前没有任何巡检来：
1. 检测 `comment.parent_id` 指向已软删除的评论
2. 检测 `message.file_id` 指向已软删除的文件
3. 检测 `file_share` 指向已软删除的文件

**证据 4：没有任何数据完整性的 CLI 工具或端点**

```bash
$ grep -rn "integrity\|consistency\|repair\|reconcile\|audit.*all\|verify" cmd/ internal/cmd/ --include="*.go" | grep -v "_test.go"
# → 空。没有用于数据完整性检查的子命令或工具。
```

`cmd/server/main.go` 和 `cmd/migrate/main.go` 是两个入口点，没有 `cmd/audit` 或 `cmd/consistency-check`。

### 为什么需要（产品经理视角）

- **数据信任**：当用户看到 `您有 99,999 条未读消息` 或 `collapsed_count: 87321` 时，会对系统数据的准确性失去信任。物化计数器必须与真实数据一致。
- **运维**：生产环境中数据漂移是 inevitability（不可避免的）。没有自动化检测和修复工具，SRE 只能通过手动 SQL 查询来诊断问题。
- **GDPR/合规**：软删除的数据可能留下跨实体的"幽灵引用"。当用户请求删除账户时，SSO 端删除用户后，aero-id 中的孤儿数据级联清理需要明确的策略和工具。
- **自动修复 vs. 人工诊断**：当前没有机制区分"计数器稍微漂移"（自动校正）和"数据损坏"（需要人工干预）。一个简单的 `make audit-data` 目标可以在数秒内扫描所有计数列并报告偏差。

---

## 综合优先级与路线图建议

| 方向 | 对生产的影响 | 实现复杂度 | 建议优先级 | 前置依赖 |
|------|------------|-----------|-----------|---------|
| **方向一：集成测试 CI 接入** | 高——阻止无声的 Schema 损坏 | 低——只需修改 CI 配置 | **P0** | 确保 `test-integration` 在 CI runner 上能稳定运行 |
| **方向二：Docker Compose 环境补全** | 高——解锁 onboard/demo/CI | 低——添加 mock SSO 或去掉 SSO 依赖 | **P0** | 需要一个可运行的 SSO mock（或配置选项允许 SSO 延迟连接） |
| **方向三：Context 治理** | 中——影响可靠性和可观测性 | 中——需要在 relay/sweeper 中重构 context 传递 | **P1** | 无 |
| **方向四：K8s 部署基础设施** | 中——决定部署方式 | 中——需要 K8s 专家 + 运维团队输入 | **P1** | 方向二（Docker 镜像 + Compose 先行） |
| **方向五：数据完整性工具链** | 中——长期数据健康 | 中——需要设计巡检框架 + CLI 工具 | **P2** | 需要领域专家定义"完整性"的边界 |

### 执行建议

1. **本周（P0）**：修复 Docker Compose（添加 SSO mock 或条件性跳过 SSO 连接）+ 修改 CI 配置让集成测试在 PR 中运行
2. **下个迭代（P1）**：重构 relay/sweeper 的 context 传递 + 生成 K8s 部署 YAML 基模板
3. **下下个迭代（P2）**：设计 `cmd/audit` 工具，实现 `like_count`、`reply_count`、`collapsed_count` 的自动巡检

---

## 边缘情况备注

- **SSO mock 的策略**：可以选择 (a) 在 Compose 中添加一个简单的 SSO 模拟服务器，或 (b) 添加 `--skip-sso` 启动标志使 aero-id 在不连接 SSO 的情况下运行（仅用于开发/演示）。选项 (b) 改动更小，但需要确保所有 auth 路径在无 SSO 时优雅降级。
- **集成测试的 Docker-in-Docker 问题**：GitHub Actions Ubuntu runner 原生支持 Docker，但 testcontainers 需要 Docker socket 可用。当前工作流运行 `go test` 时不绑定 Docker socket——需要显式添加 Docker service 或使用 `docker compose run --rm` 方式运行集成测试。
- **计数器极值的产品策略**：`collapsed_count` 应有硬性上限（例如 999），超过时显示 `999+`。这需要在 DB 层（CHECK 约束）和应用层（钳制）双重保障。
