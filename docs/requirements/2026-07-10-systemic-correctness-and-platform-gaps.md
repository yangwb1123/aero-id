# aero-id — 系统性正确性保障与平台化缺失

> **日期**: 2026-07-10
> **视角**: 资深架构师 / 产品经理
> **方法**: 全局代码扫描（`internal/` 全部 ~45 个包、22 份迁移 SQL、`cmd/`、`configs/`、`api/proto/`），与 `docs/requirements/` 下 25+ 份分析文档做逐主题交叉比对，定位在所有前述分析中**未被系统性覆盖**的方向
> **无代码变更**

---

## 前置：本文件的独特定位

现有 25+ 份分析文档（累计覆盖 ~60+ 个方向）集中在以下类别：

| 类别 | 覆盖方向数 | 代表文档 |
|------|-----------|---------|
| **新功能 / 产品特性** | ~25 | 群聊、文件版本、@提及、表情反应、数据导出…… |
| **架构演进 / 基础设施** | ~20 | 多副本缓存、存储迁移、分布式限流、OpenAPI 规范…… |
| **运维成熟度 / 可观测性** | ~10 | SLO 框架、混沌测试、配置漂移检测、供应链安全…… |
| **安全 / 合规** | ~5 | 静态加密、证书固定、审计日志持久化…… |

本文关注的 5 个方向在以上所有分析中**未被系统性覆盖**，属于"更深层的正确性保障与平台化缺失"——不是新功能需求，而是工程系统观下的结构性问题。

| # | 方向 | 类别 | 代码证据数量 |
|---|------|------|------------|
| 1 | **写路径并发安全与计数器一致性** | 正确性 / Edge Case | 6 个竞态窗口 |
| 2 | **端到端文件完整性验证链** | 正确性 / 信任 | 2 个断层 |
| 3 | **SDK / 开发者平台** | 平台化 | 4 个缺失层 |
| 4 | **异地灾备与业务连续性架构** | 运维成熟度 | 5 个互不关联的故障域 |
| 5 | **客户端离线同步协议** | 平台化 / 移动端 | 3 个能力缺口 |

---

## 方向一：写路径并发安全与计数器一致性（Write-Path Concurrency & Counter Consistency）

### 代码级证据

aero-id 在写操作的事务性方面做了大量正确工作（事务性 outbox、原子 reply_count 更新、幂等键），但在**高并发写竞争**场景下，当前架构有多个已知的竞态窗口。

#### 竞态窗口 1：点赞/取消点赞的 TOCTOU

**位置**: `internal/comment/service.go` — `Like` / `Unlike` 方法

```go
// 伪代码（简化）
func (s *commentService) Like(ctx, commentID) (*ReactionResult, error) {
    // step 1: SELECT 检查是否已点赞
    existing, _ := s.repo.FindLike(ctx, commentID, actor)
    // ← 窗口：另一 goroutine 在此处插入 Unlike
    if existing {
        // step 2: 返回"已点赞"但不执行任何操作
        return alreadyLiked, nil
    }
    // step 3: 事务内 UPSERT + 递增 like_count
    result, err := s.repo.Like(ctx, commentID, actor) // INSERT + UPDATE like_count
    return result, nil
}
```

`Edge Cases` 文档（EC-1）已指出 TOCTOU 窗口：**step 1 和 step 3 之间存在时间窗口**。若两个并发请求同时通过 step 1（都看到未点赞），step 3 的事务性 UPSERT（唯一约束 `ON CONFLICT`）确保不会重复点赞——但 `like_count` 会多递增一次。

**现实验证**：如果两个 Like 并发到达，第一个插入 one row + inc 1，第二个因唯一约束冲突而 skip insert 但**不保证 skip inc**（取决于实现）。假设实现是 `INSERT ... ON CONFLICT DO NOTHING` + 独立的 `UPDATE like_count`，则多递增的风险确实存在。

**当前保护措施**：唯一约束防止重复行，但没有防止 `like_count` 多算。这在低并发时几乎不会出现，但在热点内容（热门评论 → 10k 并发点赞）时，`like_count` 会出现系统性正向漂移。

#### 竞态窗口 2：文件去重与并发上传

**位置**: `internal/file/service_crud.go:50-73`

```go
checksum := hex.EncodeToString(hasher.Sum(nil))
// ← 窗口：两个相同文件同时上传，hash 相同
if existing, ferr := s.files.FindByChecksum(ctx, actor, checksum); ferr == nil {
    // 已有相同文件，返回已存在的文件id
    return existing, nil
}
// ← 另一个goroutine在此处创建了同hash文件
// 实际也走到了这一行，于是产生两个 checksum 相同的文件行
```

**影响**：去重失效，存储空间浪费。`checksum` 上没有唯一约束（因为不同 owner 可以拥有相同内容的文件），但同一 owner 的 checksum 去重有**竞争窗口**。

`storage_refs` 表（migration 000021）通过引用计数解决了 GC 阶段的并发问题，但 **Create 路径上去重阶段的并发安全仍未解决**。

#### 竞态窗口 3：Outbox Relay 的 At-Least-Once 语义无去重

**位置**: `internal/outbox/relay.go:117-137`

```go
for _, e := range events {
    if err := r.handler.Handle(ctx, e); err != nil {
        // mark failed
        continue
    }
    published = append(published, e.ID)
}
// MarkPublished 成功后事件才算"已发布"
// 如果 Handle 成功 → MarkPublished 前崩溃 → 重启后同一事件被再次 Handle → 重复通知
```

**当前无去重机制**：`NotificationDispatcher.Handle` 创建 `Notification` 行时没有使用 `(actor_uid, type, subject_id)` 唯一约束，因此重复投递会创建多条重复通知。

#### 竞态窗口 4：Idempotency Key 的并发 Check-Then-Insert

**位置**: `internal/middleware/idempotency.go`

幂等键模式是标准的 check-then-execute：

```go
// step 1: 检查 key 是否存在
existing := store.Get(ctx, key)
if existing != nil {
    return existing.Response, nil
}
// ← 窗口：两个相同 key 的请求同时通过
// step 2: 执行操作
result := handler(c)
// step 3: 存储 key+结果
store.Set(ctx, key, result)
```

幂等键的唯一约束是 `(uid, key)` — 若表有唯一约束，第二个会失败。但当前 `idempotency.GormStore` 的 `Get` + `Set` 是两步操作，没有包裹在事务中。第一个请求刚写入时第二个请求可能仍看到"不存在"。

**生产影响**：幂等键的目的是**完美去重**。竞态窗口让它变成"大概率去重"。在重试风暴（网络抖动 → 所有客户端同时重试）时，仍可能产生少量重复。

#### 竞态窗口 5：并发软删除 + 软恢复

当前所有实体的删除都是软删除 (`gorm.DeletedAt`)。但是没有任何 **恢复** API。一旦软删除，数据不可见但占用空间，等待 GC 清理。

如果未来实现"恢复"功能，需要解决：**A 恢复时，B 正在删除同一记录**。如果没有版本号或乐观锁，A 可能恢复一条 B 刚刚标记为删除的记录。

#### 竞态窗口 6：文件共享授权与并发资源删除

**位置**: `internal/file/service_share.go`

用户 A 分享文件给用户 B。同时用户 A 删除该文件。当前 `DeleteFile` 删除行 + blob，没有检查活跃分享。B 已获得的 presigned URL 将指向不存在的 blob。

**影响**：已共享的 presigned URL 在文件删除后变为死链。从安全角度这不是漏洞（owner 有权删除），但从用户体验角度是**静默的 404**——B 没有任何通知就知道访问被撤销了。

### 系统性建议

以上 6 个竞态窗口的根因相同：**"先检查再执行"模式 + 无锁定机制**。

| 场景 | 模式 | 当前防护 | 建议方案 |
|------|------|---------|---------|
| Like TOCTOU | check → UPSERT | 唯一约束防重复行 | 将 `SELECT + INSERT/UPDATE` 合并为单条 SQL（`INSERT ... ON CONFLICT DO UPDATE set count = count + 1`），消除应用层判断窗口 |
| 去重 TOCTOU | check → INSERT | 无 | `(owner_uid, checksum)` 加唯一约束（允许空 checksum），或使用 `INSERT ... ON CONFLICT DO NOTHING` 返回 |
| Outbox 重复投递 | Handle → MarkPublished | 无 | Notification 表加 `(recipient_uid, type, subject_id)` 唯一约束，或 dispatcher 内幂等检查 |
| 幂等键竞态 | Get → Set | 唯一约束 | 将 `Get + Set` 改为单条 `INSERT ... ON CONFLICT DO UPDATE` 原子操作 |
| 并发软删/恢复 | — | 无 | 领域模型引入 `version` 乐观锁列（`gorm:"version"`） |
| 分享 + 删除 | 删除前不检查分享 | 无 | `DeleteFile` 前检查活跃分享；清理 presigned URL 缓存 |

**实现优先级**：
- **P0**：Outbox 去重（竞态窗口 3 → 生产数据正确性）
- **P0**：幂等键原子化（竞态窗口 4 → 支付级正确性保证）
- **P1**：Like_count 原子化（竞态窗口 1 → 热点内容计数器漂移）
- **P1**：乐观锁引入（竞态窗口 5 → 未来恢复功能的基础）
- **P2**：去重 TOCTOU（竞态窗口 2 → 存储成本）
- **P2**：删除前检查分享（竞态窗口 6 → 用户体验）

---

## 方向二：端到端文件完整性验证链（End-to-End File Integrity Chain）

### 代码级证据

| 位置 | 能力 | 状态 |
|------|------|------|
| `internal/file/service_crud.go:40-50` | 上传时计算 SHA256 checksum | ✅ |
| `internal/file/service_crud.go:53` | 用 checksum 做内容去重 | ✅ |
| `migrations/000001_create_files.up.sql` | `files.checksum` 列 | ✅ |
| — | 下载时验证 checksum | ❌ |
| — | 定期完整性扫描 | ❌ |
| — | 跨存储校验（本地 ↔ S3 迁移后） | ❌ |
| — | 静默数据损坏检测与修复 | ❌ |

**关键发现**：checksum 在**上传时**计算并持久化，但**从未在读取时验证**。

```go
// internal/storage/local.go — Get
func (s *Local) Get(ctx context.Context, key string) (io.ReadCloser, error) {
    // 直接读取文件内容 → 返回流
    return os.Open(path) // 无 checksum 验证
}

// internal/storage/s3.go — Get
func (s *S3) Get(ctx context.Context, key string) (io.ReadCloser, error) {
    // S3 GetObject → 返回 Body
    return result.Body // 无 checksum 验证
}
```

此外，`files.checksum` 列的定义是**nullable TEXT**—对于历史数据或通过 `CompleteUpload`（presigned 直传）上传的文件，checksum 可能为空：

```go
// internal/file/service_upload.go
func (s *fileService) CompleteUpload(...) (*domain.File, error) {
    // 不重新下载文件计算 checksum → checksum 留空
}
```

### 为什么需要

1. **静默数据损坏是真实威胁**。对于本地存储，磁盘位翻转（bit-rot）在非 ECC 内存/磁盘上并不罕见。对于 S3 存储，虽然 AWS 提供了存储层校验和（SHA-256 树），但应用层仍有端到端验证的责任——S3 的校验和覆盖了"传输中和存储中"，但没有覆盖"aero-id 应用层写入的内容 vs 读取的内容是否一致。"

2. **产品信任基础**。aero-id 的核心承诺是 `"data owned by accounts"`（用户的数据）。如果用户存储的照片 6 个月后打开发现文件损坏，且系统没有任何检测机制，信任将不可挽回地受损。

3. **迁移验证**。方向`storage migration`（已在其他文档中分析）需要迁移后校验：源后端读出的文件 checksum 应与目标后端写入后读取的 checksum 一致。

### 建议方案

#### 1. 读取时校验（下载路径）

`FileStorage` 接口的 `Get` 返回 `(io.ReadCloser, error)`。增加一个 `VerifyingReader` 装饰器：

```
读取流 → 同时计算 SHA256 → 流结束后与 database 记录的 checksum 比较
         → 匹配：正常返回
         → 不匹配：记录审计日志 + 返回 integrity_error + 触发修复流程
```

**代码影响**：在 service 层 `GetFile` / `DownloadURL` 中，不再直接返回 `store.Get` 的 reader，而是包装一层 `hashio.NewVerifyingReader(reader, expectedChecksum)`。

#### 2. 定期完整性扫描

一个后台任务（类似 GC sweeper 的 `Start` 模式），周期性：

```
1. 从 files 表扫描一批非空 checksum 的记录
2. 调用 store.Stat 获取后端对象元数据
3. 对本地存储：读取文件 → 计算 SHA256 → 验证
4. 对 S3 存储：利用 S3 的已存储校验和（ETag 或 checksum 属性）与 database 记录比对
5. 报告损坏记录：损坏率、损坏文件列表、预计恢复成本
```

#### 3. Checksum 覆盖率补全

**现状**：`CompleteUpload`（presigned 直传）路径不计算 checksum，导致 checksum 覆盖率存在盲区。

**建议**：在 `CompleteUpload` 增加可选的后台校验——如果存储后端支持服务器端校验和（如 S3 的 `ContentSHA256`），`PresignUpload` 时要求客户端在 PUT 时包含 `x-amz-content-sha256` 头；`CompleteUpload` 时读取该值写入 database。

### 实施估算

| 增量 | 工作量 | 影响 |
|------|--------|------|
| 下载时校验装饰器 | 2-3 天 | 高 — 用户数据信任 |
| 定期完整性扫描 | 3-5 天 | 中 — 运维监控 |
| Presigned 上传 checksum 补全 | 2-3 天 | 中 — 数据覆盖率 |

---

## 方向三：SDK / 开发者平台（Client SDK & Developer Platform）

### 代码级证据

| 维度 | 当前状态 | 证据 |
|------|---------|------|
| **HTTP API 规范** | 无 OpenAPI/Swagger 文档 | `internal/handler/http/router.go` — 路由注册无描述性元数据 |
| **gRPC 规范** | Proto 文件存在，但仅服务定义 | `api/proto/aero_id/v1/aero_id.proto` — 无 HTTP 映射注释 |
| **客户端 SDK（Go）** | 无 | `internal/service/mocks/mocks.go` — 有 mock 但无 client |
| **客户端 SDK（TypeScript）** | 无 | — |
| **客户端 SDK（Kotlin/Swift）** | 无 | — |
| **API 调试控制台** | 无 | — |
| **速率限制反馈头** | 429 时无 `X-RateLimit-*` 头 | `internal/middleware/ratelimit.go` — 只返回 JSON body |
| **错误码注册表** | 无中央文档 | `internal/pkg/errors/errors.go` — error codes 散落在各层 |
| **变更日志与迁移指南** | 无面向消费者的版本记录 | `docs/CHANGELOG.md` — 内部记录，非消费者面向 |

**核心事实**：aero-id 已是一个拥有 41 个 gRPC RPC + 30+ HTTP 端点的平台级服务。但**要集成 aero-id，开发者必须阅读 Go 源代码**。

### 为什么需要

| 视角 | 说明 |
|------|------|
| **产品/增长** | 如果 aero-id 是内部平台，缺少 SDK 意味着每个接入团队需自己实现 HTTP 调用、错误处理、重试逻辑。按平均 3 天/团队 × 10 个团队 = 30 天的重复劳动。 |
| **质量** | 没有自动化生成的 SDK，每个团队实现的 HTTP 客户端都有不同的错误处理逻辑、不同的超时设置、不同的重试策略→线上行为不一致。 |
| **效率** | 没有 OpenAPI 规范，无法在 CI 中做 breaking change detection。今天改了一个 POST 参数的类型，明天另一个团队的代码就坏了，只有运行时报错才能发现。 |
| **演进能力** | 没有版本化的 API 契约，无法做优雅弃用（deprecation）。任何 API 变更都是 breaking change。 |

### 建议方案

#### 1. OpenAPI 规范生成管线

Proto 文件已包含完整的 gRPC 服务定义。利用 `protoc-gen-openapiv2`（gRPC Gateway 项目）从 proto 生成 OpenAPI v2 规范：

```makefile
openapi:
	protoc --openapiv2_out=docs/ \
		--openapiv2_opt=allow_merge=true,merge_file_name=aero-id \
		api/proto/aero_id/v1/*.proto
```

**注意**：HTTP API 与 gRPC 的路径和参数不完全相同。建议采用两种方式之一：
- **方式 A**：在 proto 中添加 `google.api.http` 注解，使 OpenAPI 规范同时覆盖 HTTP 和 gRPC
- **方式 B**：手写（或从 Go handler 自动提取）OAS 3.1 规范，覆盖 HTTP 特有端点（multipart 上传、SSE 流、HMAC 下载）

#### 2. 客户端 SDK 生成

| 语言 | 生成工具 | 发布方式 |
|------|---------|---------|
| Go | `protoc-gen-go-grpc` + gRPC client wrapper | `go get github.com/aero/aero-id/client/go` |
| TypeScript | `protoc-gen-ts` + gRPC-web wrapper | npm package |
| Kotlin | `protoc-gen-grpc-kotlin` | Maven/Gradle |
| Swift | `protoc-gen-swiftgrpc` | SPM |

**最小可行 SDK**：一个 Go 客户端包（当前模块的一部分）：
- 封装所有 41 个 RPC 的调用
- 自动处理 token 刷新、重试、限流退避
- 一个函数 = 一个业务操作

#### 3. API 可消费性增强

| 增量 | 描述 | 工作量 |
|------|------|-------|
| `X-RateLimit-Limit` / `X-RateLimit-Remaining` / `X-RateLimit-Reset` 头 | 通过请求时告知客户端当前限流状态 | 1 天 |
| `Retry-After` 头 | 429 时告知客户端何时重试（当前已添加但未确认格式） | 半天 |
| 错误码中央注册表 | 所有 `AppError.Code` 集中在 `docs/error-codes.md`，每个代码附带含义+重试策略 | 1 天 |

### 边界情况

- **API 版本化策略**：当前无版本号前缀（除去 `/v1`）。建议采用 URL 路径版本化（`/v2/files/...`）并保证至少一个版本的并行支持期
- **SDK 版本兼容性**：Major 版本号需与 API 版本对应（`v1.0.0` 对应 API v1，`v2.0.0` 对应 API v2）
- **Deprecation 流程**：API 弃用时先在响应头加 `Deprecation: true`，一个 deprecation 周期（≥90 天）后再移除

---

## 方向四：异地灾备与业务连续性架构（Disaster Recovery & Business Continuity）

### 代码级证据

当前 aero-id 的部署架构隐含的假设是**单集群、单区域**：

| 组件 | 故障域 | 当前保护 |
|------|--------|---------|
| PostgreSQL | 单实例 / 单区域 | 无复制、无备份策略、无 RPO/RTO 定义 |
| 存储（local 模式） | 单节点磁盘 | 无复制 |
| 存储（S3 模式） | 单区域 | 依赖 AWS 持久性 99.999999999%，但无跨区域备份 |
| SSO 连接 | 单端点 | 有断路器，但无备用的 SSO 端点 |
| SSE Hub | 进程内内存 | 无持久化，连接重启即丢失 |

**关键缺失**：

| 文件 | 缺失 |
|------|------|
| `configs/config.yaml` | 无 `database.replica`、`storage.replication`、`disaster_recovery.*` 配置节 |
| `internal/config/config.go` | 无 DR 相关配置字段 |
| `internal/app/app.go` | 所有依赖在 `New` 阶段单点就绪；无"降级模式"只读副本的概念 |
| `internal/pkg/db/db.go` | 单 DSN、单连接池；无读副本、无故障转移 |
| `internal/repository/` 全部 | 所有仓库只读单个数据库；无"当前是否主库"的感知 |
| `internal/handler/http/health.go` | `/readyz` 检查 DB + SSO 活性，但不检查**数据版本**或**复制延迟** |
| `cmd/migrate/main.go` | 迁移工具不感知主从切换；若有主从复制，迁移可能在从库上错误执行 |
| `Makefile` | 无 `make backup`、`make restore` 目标 |
| `deployments/docker-compose.yml` | 单实例部署；无主从、无负载均衡 |

### 为什么需要

1. **数据丢失是终极事故**。aero-id 存储的是用户产生的数据（文件、消息、评论）。一次数据库误操作、一次存储桶意外删除、一次勒索软件攻击——没有备份意味着数据永久丢失。

2. **区域故障真实存在**。AWS/Azure/GCP 都有区域级故障的历史。如果 aero-id 部署在单一区域，该区域的故障意味着整个服务不可用，客户完全无法访问自己的数据。

3. **合规要求**。对于存储用户内容的服务，GDPR、CCPA、HIPAA 等法规对数据可恢复性和业务连续性有明确要求。

### 建议方案

#### 1. 定义 RPO 和 RTO

| 指标 | 目标 | 说明 |
|------|------|------|
| RPO（恢复点目标） | 5 分钟 | 最多丢失 5 分钟的数据 |
| RTO（恢复时间目标） | 30 分钟 | 故障后 30 分钟内恢复服务 |

这两个数字驱动所有基础设施决策。

#### 2. 数据库灾备

| 层 | 方案 | RPO | 复杂度 |
|---|------|-----|--------|
| **流复制** | PostgreSQL 流复制 → 同区域热备实例 | < 1s | 低 |
| **跨区域复制** | PostgreSQL 逻辑复制 → 跨区域温备实例 | < 5s | 中 |
| **定期备份** | `pg_dump` / `pgBackRest` → 对象存储（跨区域） | 1h | 低 |

**配置建议**：
```yaml
# configs/config.yaml 新增
database:
  primary: "postgres://..."
  read_replica: "postgres://..."   # 只读查询路由至此
  wal_archive: "s3://backups/wal/" # WAL 归档
  backup:
    enabled: true
    schedule: "0 */6 * * *"        # 每 6 小时全量备份
    retention: 30d                 # 备份保留 30 天
    target: "s3://backups/db/"
```

#### 3. 存储灾备

| 后端 | 方案 |
|------|------|
| **S3** | S3 跨区域复制（CRR）：配置 `ReplicationRule` 自动复制到另一个区域的 bucket；或使用 S3 版本控制 + 跨区域备份 |
| **Local** | 不支持跨区域；生产应用必须迁移到 S3 |

#### 4. 恢复演练流程

```
make disaster-recovery-drill  # CI/CD 流水线的一部分

流程:
  1. 在新的 VPC 中启动基础设施
  2. 从最近的备份恢复数据库
  3. 验证数据完整性（行数校验、checksum 抽检）
  4. 启动 aero-id 服务
  5. 运行 smoke test 套件（创建/读取/搜索/删除基本操作）
  6. 报告 RTO 达标情况
```

### 边界情况

- **主从切换期间迁移**：迁移工具必须感知当前角色，仅在主库执行 DDL；从库通过流复制获得 schema 变更
- **部分区域故障**：只读 API（文件列表、消息查询）可从副本服务；写操作降级返回 503
- **数据回滚场景**：如果迁移导致问题，需要有能力进行时间点恢复（PITR）到迁移前的状态

---

## 方向五：客户端离线同步协议（Offline Sync Protocol for Mobile Clients）

### 代码级证据

当前所有 API 假定**客户端始终在线**。没有支持移动端离线场景的基础设施：

| 能力 | 当前状态 | 代码证据 |
|------|---------|---------|
| **增量同步 API** | 不存在 | 所有 List 接口基于 cursor 分页，无 `since` 时间戳 |
| **变更时间线** | 不存在 | 所有实体有 `created_at` / `updated_at`，但无统一的变更日志 |
| **冲突检测** | 不存在 | 无 `version` 乐观锁、无 `If-Match` / `If-None-Match` 条件请求头 |
| **客户端缓存失效** | 不存在 | 无 ETag、无 `Cache-Control`、无 `304 Not Modified` |
| **批量同步** | 不存在 | 获取一个用户的所有消息需要逐页遍历；无 `GET /v1/sync?since=` |

### 为什么需要

1. **移动网络天然不可靠**。在弱网环境下（地铁、电梯、偏远地区），请求可能失败，用户期望应用能在有网时自动同步而不是报错。

2. **用户行为模式**。用户可能在一条消息上阅读 30 秒，期间网络切换 3 次。如果每次网络切换都需要重新拉取全部数据，流量和延迟都不可接受。

3. **竞品标准**。主流平台（Slack、Telegram、WhatsApp、iMessage）都支持离线消息访问和断点续传。用户对此有默认期待。

### 建议方案

#### 1. 增量同步端点

```http
GET /v1/sync?since=2026-07-10T00:00:00Z&types=messages,comments,notifications
```

返回自 `since` 以来，当前用户有权看到的所有新增/更新/删除的实体列表。

**实现方式**：在数据库层面为每个可同步实体维护一个 `updated_at` 索引。同步端点执行：

```sql
SELECT * FROM messages
WHERE (from_uid = :actor OR to_uid = :actor)
  AND (updated_at > :since OR created_at > :since)
ORDER BY updated_at ASC
LIMIT 100
```

对于**删除事件**，当前软删除模型支持检测——只需在查询中包含 `deleted_at IS NOT NULL` 的行，并在响应中标记 `deleted: true`。

#### 2. 变更时间线 / 操作日志

引入 `change_log` 表（类似于事件溯源的精简版本）：

```sql
CREATE TABLE change_log (
    id          BIGSERIAL PRIMARY KEY,
    actor_uid   TEXT NOT NULL,
    entity_type TEXT NOT NULL,    -- 'message', 'comment', 'file', 'notification'
    entity_id   TEXT NOT NULL,
    action      TEXT NOT NULL,    -- 'created', 'updated', 'deleted'
    payload     JSONB,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_change_log_actor_created
    ON change_log(actor_uid, created_at);
```

每个写操作（`Create`/`Update`/`Delete`/`SoftDelete`）在事务中同时写入 `change_log`。同步端点改为读取 `change_log` 而非扫描各个业务表——极大简化查询、降低锁竞争。

**与 Outbox 的关系**：`change_log` 与 `outbox_events` 是**正交的**。Outbox 用于异步侧效果应（通知），`change_log` 用于客户端同步。两者可以写入同一事务，但消费方式不同（relay vs 同步端点）。

#### 3. 冲突检测与解决

当前无冲突检测。引入**乐观锁**：

```go
type File struct {
    ID        domain.ID
    OwnerUID  string
    Name      string
    // ...
    Version   int       `gorm:"version"` // 自动递增
}
```

每当更新实体时，GORM 自动执行：

```sql
UPDATE files SET name = ?, version = version + 1
WHERE id = ? AND version = :expected_version
```

如果 `version` 不匹配，UPDATE 影响 0 行 → 返回 409 Conflict。

**HTTP 层面**：支持 `If-Match` 头（值为当前 version）实现条件更新：

```http
PATCH /v1/files/:id
If-Match: "42"
Content-Type: application/json

{"name": "new-name.jpg"}
```

**冲突解决策略**：对于大部分 aero-id 场景（消息、评论），用户很少并发编辑同一条内容。"最后写入者胜出"是合理的默认策略。对于需要精确控制的场景（如文件元数据更新），返回 409 让客户端自行解决。

#### 4. 批量操作端点

当前所有实体操作是**单条**的。离线重连后，客户端可能累积了 N 条操作需要同步。逐个发送 N 个 HTTP 请求效率极低。

```http
POST /v1/batch
Content-Type: application/json

{
    "operations": [
        {"method": "PATCH", "path": "/v1/files/abc", "body": {"name": "new-name.jpg"}},
        {"method": "DELETE", "path": "/v1/messages/def"},
        {"method": "POST", "path": "/v1/messages/def/read"}
    ]
}
```

**实现**：每条操作独立执行（同一事务），全部成功后整体返回。任何一条失败返回失败列表（不整体回滚）。

### 实施估算

| 增量 | 工作量 | 依赖 |
|------|--------|------|
| 增量同步端点 `GET /v1/sync` | 3-5 天 | `updated_at` 索引（migration 000015 已包含部分） |
| `change_log` 表 + 写入钩子 | 5-7 天 | 需要在所有 service Create/Update/Delete 中增加写入 |
| 乐观锁 + 条件请求 | 3-5 天 | GORM `version` 字段 + middleware |
| 批量操作端点 | 3-5 天 | 路由解析 + 事务执行 |
| 客户端 SDK 中的同步逻辑 | 视语言而定 | 依赖 SDK 存在（方向三） |

---

## 综合优先级矩阵

| # | 方向 | 业务价值 | 工程影响 | 实现成本 | 优先级 |
|---|------|---------|---------|---------|--------|
| 1 | 写路径并发安全 | ⭐⭐⭐⭐ — 数据正确性，防止计数器漂移和重复操作 | 高 — 修复潜藏的竞态条件 | 低-中 — 每条修复 1-2 天 | **P0（可并行）** |
| 2 | 文件完整性验证链 | ⭐⭐⭐ — 信任基础，防止静默数据丢失 | 中 — 新增验证层 + 扫描任务 | 中 — 5-8 天 | **P1** |
| 3 | SDK / 开发者平台 | ⭐⭐⭐⭐⭐ — 平台化基础，降低集成成本 10x | 中 — 新增构建流水线 + 发布流程 | 中-高 — 初始 2 周 | **P0（启动）** |
| 4 | 异地灾备与业务连续性 | ⭐⭐⭐⭐⭐ — 防止数据丢失，合规要求 | 高 — 涉及基础设施变更 | 高 — 1-2 月 | **P1（规划）** |
| 5 | 客户端离线同步协议 | ⭐⭐⭐⭐ — 移动端 UX 核心，竞争优势 | 高 — 新增端点和表 | 高 — 2-4 周 | **P2** |

### 执行建议

```
第 1 周             第 2-3 周            第 4-8 周             第 9-12 周
┌──────┐          ┌──────────┐         ┌─────────────┐        ┌─────────────┐
│方向一 │──────►  │方向三（启动）│─────►│方向二         │──────►│方向四（规划）│
│(P0)  │ 并行     │+ 方向一   │        │(P1)          │       │+ 方向五     │
│      │          │(剩余修复) │        │               │       │(P2)         │
└──────┘          └──────────┘         └─────────────┘        └─────────────┘
```

- **方向一（并发安全）**：立即启动，可并行修复 6 个竞态窗口，与任何其他工作无依赖冲突
- **方向三（SDK）**：第 2 周启动，OpenAPI 规范生成最快半天完成——这是所有后续 SDK 工作的基础
- **方向二（文件完整性）**：第 3 周启动，在方向一的并发修复之后以保证存储路径稳定
- **方向四（灾备）**：第 6 周启动，需要前期的架构讨论和基础设施规划
- **方向五（离线同步）**：第 8 周启动，依赖方向三的 SDK 存在（移动端使用 SDK 接入）

---

## 附录：与现有 25+ 份分析文档的交叉比对

本文 5 个方向与已有文档的覆盖关系：

| 方向 | 相符文档 | 差异 / 新覆盖 |
|------|---------|--------------|
| **写路径并发安全** | `edge-cases.md` EC-1 提及 Like TOCTOU，但仅作为单个 edge case 列出 | 本文首次将 6 个竞态窗口**系统性地串联分析**，识别共同根因（check-then-execute），并给出统一的修复框架 |
| **文件完整性验证** | `deep-extension.md` 方向 4（缩略图管线）聚焦预处理 | 本文关注的是**数据完整性验证**（checksum 验证链），而非转化处理——完全不同的问题域 |
| **SDK / 开发者平台** | `arch-evolution.md` 方向一（API 消费者契约）提及 OpenAPI 规范 | 本文从 SDK 生成、多语言客户端、`X-RateLimit-*` 头、错误码注册表等**完整的开发者体验**角度展开，而非仅限于 API 契约描述 |
| **异地灾备** | 所有文档中仅 `SECURITY_OPS.md` 提及 "down migration 数据丢失" 作为边角注意 | 本文首次系统性地分析灾备架构——RPO/RTO 定义、数据库复制、存储复制、恢复演练流程——作为一个完整的工程方向 |
| **离线同步协议** | 所有 25+ 份文档中**零覆盖** | 增量同步端点、`change_log` 表设计、乐观锁冲突检测、批量操作 API——全部为首次系统性分析 |

**结论**：5 个方向中，离线同步协议在所有既有分析中为零覆盖；写路径并发安全为首次系统性串联分析；灾备为首次作为完整工程方向分析；SDK 和文件完整性在已有文档中仅简要提及，未达完整分析深度。
