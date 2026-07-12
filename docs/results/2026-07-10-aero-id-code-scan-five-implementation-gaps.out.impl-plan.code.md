# 代码实现报告

## 实现概述

实现了 **方向二（Redis共享状态）** 的前两个核心任务：

1. **TASK-011：Redis客户端配置+连接池** — 新增 `internal/pkg/redis/redis.go`，基于 `rueidis` 实现Redis客户端，支持连接池配置、TCP拨号超时、KeepAlive、连接健康检查，配置为空时优雅回退
2. **TASK-012：幂等性Redis后端** — 新增 `internal/idempotency/store_redis.go`，实现 `Store` 接口的 `Reserve`/`Complete`/`Release`，使用 `SET NX EX` 原子预留，JSON序列化记录，TTL自动回收stale reservation

同时完成：
- **配置集成**：`config.RedisConfig` 定义、`config.yaml` 默认值、config校验
- **应用级路由**：`new.go` 中根据Redis配置自动选择 `RedisStore`/`GormStore`，启动时打印存储后端类型
- **生命周期管理**：`app.go` 中添加 `redisClient` 字段，`Close()` 中自动关闭

## 文件清单

### 新建文件
- `internal/pkg/redis/redis.go` — Redis客户端配置和连接池（76行）
- `internal/idempotency/store_redis.go` — Redis幂等性存储后端（145行）
- `internal/idempotency/store_redis_test.go` — 4个测试用例（186行）

### 修改文件
- `internal/config/config.go` — 新增 `RedisConfig` 结构体 + validation
- `configs/config.yaml` — 新增Redis配置节默认值
- `internal/app/new.go` — Redis客户端初始化 + Idempotency store路由
- `internal/app/app.go` — `redisClient` 字段 + `Close()` 清理
- `go.mod` / `go.sum` — 新增 `github.com/redis/rueidis v1.0.76`, `github.com/alicebob/miniredis/v2 v2.38.0`

## 核心代码实现

### RedisStore 关键设计

```
┌─────────────────────────────────────────────────────┐
│ Reserve(ctx, uid, endpoint, key)                     │
│   Key: "idempot:{uid}:{endpoint}:{key}"              │
│   1. 序列化 redisRecord{Status:0, Completed:false}    │
│   2. SET key json_string NX EX 120s (2min TTL)       │
│   3. NX成功 → (nil, true, nil)                       │
│   4. NX失败 → GET key → 反序列化 → 返回现有Record     │
│                                                       │
│ Complete(ctx, uid, endpoint, key, rec, ttl)           │
│   1. 序列化 redisRecord{Status, CT, Body, true}       │
│   2. SET key json_string EX ttl (默认24h)             │
│                                                       │
│ Release(ctx, uid, endpoint, key)                      │
│   1. DEL key (best-effort)                           │
└─────────────────────────────────────────────────────┘
```

**关键决策：**
- **Stale reservation回收**：Redis TTL自动处理（2分钟），无需GormStore那样的手动reaper goroutine
- **JSON序列化**：紧凑格式（`{"s":200,"ct":"...","b":"...","c":true,"t":...}`），兼容未来扩展
- **rueidis**选择：高性能，单次往返 `Do` 调用，多路复用，适合高吞吐idempotency场景
- **miniredis**测试：纯Go内存实现，无需Docker，确保CI可运行

## 测试覆盖

| 测试用例 | 场景 | 验证点 |
|---------|------|-------|
| `TestRedisStore_Reserve_then_Complete` | 正常路径 | Reserve成功 → Complete → 再次Reserve返回已完成记录 |
| `TestRedisStore_Reserve_conflict_inFlight` | 并发冲突 | 两个并发请求，第二个获得in-progress状态（409） |
| `TestRedisStore_Release` | 释放后重试 | Release后Reserve重新成功 |
| `TestRedisStore_Reserve_autoExpiry` | TTL自动回收 | FastForward 3分钟 → Reserve重新成功 |

## 验证步骤

```bash
make build    # ✅ 编译通过
make test     # ✅ All 28 packages pass
make lint     # ✅ go vet ok
```

## 配置说明

```yaml
redis:
  endpoint: "127.0.0.1:6379"    # empty = Redis禁用（回退到GORM）
  password: ""                    # AUTH密码（env: AERO_ID_REDIS_PASSWORD）
  db: 0                           # SELECT数据库编号
  blocking_pool_size: 100         # 连接池大小
  blocking_pool_min_size: 10      # 最小空闲连接
  conn_write_timeout: 3s          # 连接级超时
  dial_timeout: 5s                # TCP拨号超时
  keepalive: 1s                   # TCP keepalive间隔
```

## 下一步建议

1. **TASK-013：Idempotency回退+配置路由** ✅ 已完成（new.go中根据Redis可用性自动路由）
2. **TASK-014：Outbox FOR UPDATE SKIP LOCKED** — 需要检查repository.go是否已实现
3. **TASK-015：Outbox leader election（Redis租约）** — 使用 `SET outbox:leader {instance} NX EX 10`
4. **TASK-016：SSO缓存L1+L2** — 内存L1（30s TTL）+ Redis L2（5min TTL）
