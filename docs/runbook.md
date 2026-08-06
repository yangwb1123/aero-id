# aero-id 运维 Runbook

> 适用版本：2026-08-05 仓库状态（账户聚合/审计/操作控制面已本地落地，源系统发布中）。
> 每个场景按「症状 → 影响 → 快速处置 → 根因排查 → 恢复确认」组织。所有处置命令以区域 cell 为单位执行。

## 0. 前置：识别与指标入口

- 健康分层：`/healthz`（进程存活）、`/readyz`（本地依赖：DB/SSO）、`/health/sources`（各源连接器状态，独立于 readyz）。
- 控制面指标（`/metrics`）：per-source 延迟/错误/熔断状态、投影滞后、consumer lag、operation 年龄、
  unknown 结果数、审计 ingest 失败、callback 重试/DLQ 深度、对账漂移、区域新鲜度。
- 业务追溯：`trace_id`（技术调用图）、`operation_id`（一次用户意图）、`request_id`（一次传输请求）、
  `causation_id`（父事实）。日志、指标 exemplar、审计查询三处必须可关联。

---

## 1. 源系统故障（Snaplink / Aero IM / Aero Vault 不可达）

**症状**：`/health/sources` 显示源 `down`/`degraded`；connector 熔断（`circuit open`）；聚合响应带
`partial:true` + `source_errors`；`X-Aero-Degraded` 头出现。

**影响**：聚合列表/统计/首页仍由本地投影服务（设计上不依赖同步 fan-out）；point read（strong 一致性）
和跨源操作（export/erase/sync）会失败；**不会**导致全站不可用。

**快速处置**：
1. 确认是源故障还是网络分区：`curl -s <source-endpoint>/healthz`；查 connector 指标中
   `timeout`/`connection refused`/`5xx` 比例。
2. 不需要人工切换——熔断器自动降级；观察 `partial` 响应中的 `status`/`generated_at`/`last_synced_at`，
   确认本地投影新鲜度未超 SLO（区域 snapshot 读 p95 ≤ 100ms）。
3. 若投影滞后超阈值：运行对账任务修复（见场景 3）。

**根因排查**：源发布/证书过期（mTLS）/负载超限（429 触发退避）/DNS。查 SSO 熔断（token 校验走本地 JWKS，
不应因源故障影响登录热路径；若 `authz` gRPC 故障，检查 `authz_legacy_fallback` 配置是否允许降级）。

**恢复确认**：`/health/sources` 全绿；熔断器 half-open 探测通过；聚合 `partial:false` 恢复；
对账漂移指标归零。

---

## 2. 区域隔离 / 区域故障

**症状**：某区域 `readyz` 失败或 `X-Aero-Stale-Read: true` 高频出现；写请求返回
`region.write_fenced`（503/fail-closed）。

**影响**：该区域暂停**账户写**（防 split-brain，fencing epoch 裁决）；本地投影读仍可用；跨区复制停滞。

**快速处置**：
1. 读 side：客户端带 `X-Aero-Write-Epoch` 重试到 writer 区域；投影滞后只影响 freshness，不阻塞读。
2. 写 side：**不要**在另一区域开启写，除非已执行 durable promotion（新 fencing epoch 证明旧 writer 无法提交）。
   Promotion 流程：`POST /v1/admin/regions/promote`（区域目录 + epoch 更新），确认旧区域
   `RegionEpoch` 已失效后再接受写。
3. 复制停滞：检查 replication cursor 指标与 `replication_cursors` 表；修复后自动追平（backfill 有带宽上限）。

**根因排查**：DB 故障/网络分区/epoch 缓存过期（1s TTL 内读到旧 epoch 是正常抖动，非故障）。

**恢复确认**：旧区域重新可写后应**保持降级角色**，由 promotion/回切流程决定，不要直接恢复双写；
投影复制 lag 归零；`X-Aero-Stale-Read` 消失。

---

## 3. 事件缺口（event gap）/ 投影漂移

**症状**：consumer lag 增长但无新事件；`reconcile_drift` 指标增长；聚合 `last_synced_at` 远旧于源。

**影响**：聚合数据陈旧；**不会**丢权威数据（投影可重建）。

**快速处置**：
1. 手动对账：`POST /v1/sync/jobs/:id/reconcile`（有界扫描源 summary/cursor，修复投影漂移）。
2. 若 receipt 显示 gap（`out_of_order`）：按 `(source, aggregate_id, aggregate_version)` 排序重建，
   新版本不回滚；必要时 `POST /v1/audit/replays/projection` 重建 shadow 投影并与 live 对比
   （matched/divergent/shadow_only/live_only）。
3. poison/quarantine：检查 event ingest 的 quarantine 路径（`event.region_not_allowed`、
   `event.unsupported_version` 等）——quarantine 事件**不会**自动重投，需人工处置后重放。

**根因排查**：源发布事件失败、NATS consumer 落后（`consumer_lag`）、投影 worker 崩溃后租约接管、
ingest 签名/绑定配置错误（`X-Aero-Event-Signature` 校验失败率）。

**恢复确认**：consumer lag 归零；对账漂移 = 0；投影 `last_synced_at` 在新鲜度 SLO 内。

---

## 4. 审计链失败 / 验证不通过

**症状**：`GET /v1/audit/verify` 返回失败；`audit_hash_verify_failure` 指标 > 0；锚定 manifest 缺失。

**影响**：合规证据完整性存疑——**最高优先级**；审计链失败不代表数据丢失，但必须立即查明。

**快速处置**：
1. 定位断裂点：按 `(source, partition_key, chain_sequence)` 找到第一个 hash/signature 不匹配的事实。
2. 若为密钥轮换导致（`signature_version` 切换）：用对应版本的密钥验证，更新验证器配置后重验。
3. 若为真篡改/写坏：**保留现场**（不可修改账本），导出受影响 partition 的事实列表，启动合规流程；
   在 WORM 归档中比对锚定快照（`GET /v1/audit/anchors/manifest/:source/:partition`）。
4. 检查 audit hold 是否误配（hold 只阻止清理，不阻止写入）。

**根因排查**：双写冲突（两个 writer 同 partition）、迁移/回填绕过 append-only 触发器
（`prevent_audit_ledger_mutation` 应拦截）、签名密钥未轮换但版本号未递增、时钟回拨。

**恢复确认**：全链验证通过；锚定 manifest 与链尾一致；归档 lag 归零。

---

## 5. Callback 风暴 / 投递故障

**症状**：`callback_retry`/`callback_dlq` 指标飙升；回调接收端 5xx/超时；`audit_publications` 堆积。

**影响**：投递退避重试（指数 + jitter），**不影响**审计事实（事实已先持久化，投递失败不回滚）；
风暴可能占满 callback bulkhead（`callback.worker_concurrency`）。

**快速处置**：
1. 先止血接收端（接收端扩容/限流），或对异常订阅 `DELETE /v1/admin/callback-subscriptions/:id`。
2. 风暴中调低 `callback.worker_concurrency`（默认 1，上限 64）以限制出站并发，防止拖垮出站网络。
3. DLQ 处置：修复接收端后 `POST /v1/admin/callback-subscriptions/deliveries/:id/replay` 手动重放；
   重放使用**新的幂等键/签名**，接收端必须按 `event_id` 幂等。
4. 检查 SSRF 防护是否误拦（DNS 变更导致接收端解析到私网 → `callback.endpoint_invalid`），
   更新端点或 DNS 后重放。

**根因排查**：接收端故障、订阅事件过滤过宽（`event_types` 空 = 全量）、审计事实激增、
密钥/签名轮换后接收端未更新校验。

**恢复确认**：DLQ 清空；`callback_oldest_age` 回到基线；重复投递被接收端幂等去重（无重复副作用）。

---

## 6. Operation 卡住（unknown 不推进）

**症状**：`operation_age` 指标增长；`GET /v1/operations/:id` 显示 `unknown`/`compensating` 停滞；
对账 worker 无进展。

**影响**：跨应用写操作结果不确定——**严禁**盲目重发源命令（可能重复副作用）。

**快速处置**：
1. 查 operation 的 step/attempt 历史（`GET /v1/audit/operations/:id` 时间线），确认最后一步的
   source operation ID 与远端状态。
2. 对账：`POST /v1/operations/:id/reconcile` —— 查源 outcome（source operation status）；
   超时 ≠ 失败，先确认远端结果再推进。
3. 若远端已成功但本地 `unknown`：对账后推进到 `completed`（幂等键保证不重发）。
4. 若远端失败/部分成功：走补偿（`compensating`），补偿也记录 attempt 与结果。
5. 租约检查：worker 崩溃后执行租约过期会自动转 `unknown` 并保留退避（1m），不要手工重置租约绕过。

**根因排查**：worker 崩溃、源命令超时但实际成功、补偿本身失败、对账 worker 未覆盖该 home region。

**恢复确认**：operation 进入终态（`completed | partially_completed | failed`）；无重复副作用；
审计时间线完整（每步 attempt/结果/补偿都有事实）。

---

## 7. 导出泄露处置（export artifact 外泄怀疑）

**症状**：导出下载 URL 泄露 / 审计发现异常下载 / 导出工件 TTL 未过期即被外部访问。

**影响**：敏感数据（账户/资源摘要，**非**对象字节）可能暴露。

**快速处置**：
1. 导出工件为加密 + TTL（`export.encryption_key`、`export.artifact_ttl`）；确认下载 URL 的签名与
   TTL 校验生效（一次性下载在实现中受控）。
2. 立即终止：删除对应 sync job 的工件（对象存储），使 URL 失效。
3. 查 `sync.export.*` 审计事实（谁、何时、哪个工件、下载次数），评估暴露面。
4. 若怀疑加密密钥泄露：轮换 `export.encryption_key`（新密钥只解密新工件，旧工件按策略销毁）。
5. 按数据驻留策略评估是否涉及跨区域违规（raw payload/PII 不得出境）。

**根因排查**：日志中泄露 URL、下载端点绕过校验（`/files/dl` 签名仅限本地存储）、
回调接收端转发工件链接、审计缺 metadata-only 记录。

**恢复确认**：工件删除 + 密钥轮换 + 审计事实完整；下载端点复测签名/过期校验；必要时走合规上报。

---

## 8. 通用原则（所有场景）

1. **先读后写**：任何写处置前，先查审计与 operation 时间线，避免凭猜测重放。
2. **不双写**：区域故障、源故障期间，禁止用绕过 fencing/幂等键的方式重发命令。
3. **DLQ 优先于重试风暴**：反复失败的事件/投递进 DLQ/quarantine，人工处置，不无限重试。
4. **审计优先**：任何数据修复/回滚操作本身也要产生审计事实（新 operation_id/幂等键）。
5. **演练**：发布前按 `scripts/load/` 场景 + chaos 用例（源 503/429/超时、NATS Nak 重投、
   区域 loss/fencing、callback outage）演练本 runbook；真实 WAN 分区与 DB failover 属外部基础设施门禁。
