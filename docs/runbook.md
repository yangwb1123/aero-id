# aero-id 运维 Runbook

> 适用版本：2026-08-05 仓库状态（账户聚合/审计/操作控制面已本地落地，源系统发布中）。
> 每个场景按「症状 → 影响 → 快速处置 → 根因排查 → 恢复确认」组织。所有处置命令以区域 cell 为单位执行。

## 0. 前置：识别与指标入口

- 健康分层：`/healthz`（进程存活）、`/readyz`（本地依赖：DB/SSO）、`/health/sources`（各源连接器状态，独立于 readyz）。
- 控制面指标（`/metrics`）：per-source 延迟/错误/熔断状态、投影滞后、consumer lag、operation 年龄、
  unknown 结果数、审计 ingest 失败、callback 重试/DLQ 深度、对账漂移、区域新鲜度。
- 业务追溯：`trace_id`（技术调用图）、`operation_id`（一次用户意图）、`request_id`（一次传输请求）、
  `causation_id`（父事实）。日志、指标 exemplar、审计查询三处必须可关联。
- **Outbox 专项**：`docs/runbooks/outbox-retry-eligibility.md` 覆盖 eligible backlog、数据库时钟/租约、
  claim/finalization 失败、poison event、死信和发布回滚；不要用本页的通用 DLQ 步骤替代它。

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
   在 WORM 归档中比对锚定快照（`GET /v1/admin/audit/anchor/manifest/:source/:partition`）。
4. 检查 audit hold 是否误配（hold 只阻止清理，不阻止写入）。

Legal Hold 列表的缺失/非数组 `holds` 或任一非对象成员按 `response.invalid` 整份拒绝，不能将读取错误当作无保全。
页面只在显式「重试」时 GET 原列表；成功空查询也仅代表本次无可见的生效 Hold，不证明不存在适用 Hold，
更不授权解除保全、清理或修改账本。基本列表形状不是完整行/tenant/到期时间验证、当前授权或全局快照；
仍需按实际适用范围与合规 owner 核实，原权限、设置/解除、保留策略及事务审计未改变。
页面将既有读取 hook 的取消 signal 经 `listHolds(signal?)` 传到原 GET，卸载/原读取替换时取消 fetch/正文，
不取消同 client 的其他读取；保原15秒/2MiB、显式 GET 重试和迟到结果守卫，不自动补读/重试或发命令。
取消依赖协作，不能撤回已准入访问，也不是全局并发硬上限、当前权限、解除保全或命令回滚。
Web 解除选择还要求原支持的 `scope_type` 和非空字符串 `scope_id`；非string/空目标不强转、补位或打开确认，原行 listener 也复查。
坏目标行仍只读展示、不丢弃合法行；合法 opaque 字符串原样确认，不规范化或自动执行。这不是完整行验证、当前权限或 Hold 版本 CAS。
Web 解除 Hold 的对话框和原命令 handler 都要求当前输入与所选 Scope ID 精确相等；空值、错值或后来清空/改错
在 client 调用和状态修改前拒绝，不 trim/忽略大小写，不只依赖 disabled；修正后仍需另一次显式确认，不自动 DELETE。
这是本地人工意图检查，不是当前权限、Hold 版本 CAS 或合规 owner 的解除/retention 批准；原权限/tenant/事务审计不变。
输入变化或关闭对话框不能撤回已准入 DELETE；超时/丢回应先 GET 核原 scope 与审计再决定，不自动重试或假设回滚。
解除确认 handler 还在原列表 loading（含响应正文）或读取 error 时拒绝旧 listener 的新调用，即使已填精确 Scope ID。
拒绝保原选择/输入，显式 GET 成功恢复列表不会自动解除，仍需另一次人工确认；不改设置 Hold/审计导出或原服务端权限/tenant/事务审计。
此本地读取准入不是当前授权、Hold 版本 CAS、所选行未变化或全局时点证明，不能撤回已准入 DELETE，也不授权保留清理。
合规页的准备/打开和最终确认处理函数还拒绝卸载后的新调用，早于生成意图键、命令状态修改或 client 调用；
新页面/会话不能为旧 listener 准入。此本地守卫覆盖设置/解除 Hold 与创建审计导出，不改各自确认/scope、单在途或服务端权限/事务审计。
它不是当前授权、持久幂等或跨刷新恢复；原内存键不保证换页/刷新恢复。卸载/取消读取不能撤回已准入 DELETE/POST，
迟到结果不应用到新页，不自动重试、换键、解除或创建导出；丢回应仍先核原 scope/任务列表及审计，不能推断回滚。
审计导出创建成功的实际 HTTP 状态为201；合规页只采用根级非nil UUID 字符串 `job_id`，不接受 `id` 别名或类型强转/trim。
坏 ID 不显示创建成功、读取坏目标或替换此前已采用详情，保原内存意图/键；先核实原任务列表和审计，不能推断未提交。
之后若仍需另一次显式同意图请求，仍使用原键，取消重开不授权换键；换页/刷新仍不保证内存键恢复。
没有自动查询/重试/生成新导出或跟随回应 download_url；新任务 ID 事先未知，不与原请求键/旧任务 ID 比较。
此局部采用检查不是完整回执/tenant/状态/当前授权、制品可用性或远端完成证明，服务端权限/事务审计及下载消费不变。

需人工锚定时，Web 原处理函数和按钮均要求当前精确短语 `ANCHOR ALL CHAINS`；空值或大小写/空白变体不准入，
修正输入不会自动执行。原打开/确认处理函数在更改意图状态、生成键或调用 client 前也拒绝卸载后的新调用；
新面板/会话不能为旧 listener 准入，已准入 POST 不因卸载或取消读取被取消/回滚，迟到结果不应用到新面板。
挂载守卫/短语不是 `system.config` 授权或持久业务幂等，原内存键不保证换页/刷新恢复；服务端仍在归档前持久化管理意图。
超时/丢回应/单链失败可能已有部分不可变对象，先核原审计与 Manifest，再决定是否显式以原键重试；
不自动换键、补锚或修改账本，不能把本地确认回归当作真实 WORM/存储验收。
单链 Manifest 查询处理函数也在修改查询状态或调用 client 前拒绝卸载后的新 submit；新面板/会话不能为旧 listener 准入。
原 GET/client promise 未结束（含正文读取）时，handler 在输入校验/查询状态修改前忽略重复 submit，不排队后来输入；
成功或报错后仍需另一次显式读取。保原 source/partition、30秒/2MiB预算、服务端当前 `audit.read` 与返回前访问审计，
不续原期限、不自动查询/重试或补锚；单在途仅属当前面板，不是全局上限。
挂载/单在途守卫不能撤回已准入读取/已持久化事实，也不是完整证据验证或授权；原读取取消依赖协作，
deadline 信号本身不释放尚未结束的 client 调用，旧结果不应用到新面板。
Manifest 回应只有根级 `source`/`partition` 为与本次提交目标精确相等的字符串才采用，不借后来输入/别名/嵌套字段补位；
缺失、坏类型或错目标以固定 `response.invalid` 整份拒绝，不展示被拒证据/目标，也不自动重读、补锚或修复。
需要时显式 GET 核实原链；回应拒绝不证明未发生读取或撤回访问事实，匹配目标也不是完整文档/hash/signature 验证、
当前授权、全局时点、归档完整性或 WORM/真实存储验收，不能据此解除证据调查。

**根因排查**：双写冲突（两个 writer 同 partition）、迁移/回填绕过 append-only 触发器
（`prevent_audit_ledger_mutation` 应拦截）、签名密钥未轮换但版本号未递增、时钟回拨。

**恢复确认**：全链验证通过；锚定 manifest 与链尾一致；归档 lag 归零。

---

## 5. Callback 风暴 / 投递故障

密钥缺失、加密格式升级与滚动轮换见 [Callback master-key maintenance](runbooks/callback-keys.md)。
本地密钥故障延后投递，不消耗接收端重试预算；暂停投递不会暂停审计事实或持久入队。

**症状**：`aero_control_callback_deliveries_total{status="pending"|"dead_letter"|"mark_failed"}` 增长；回调接收端 5xx/超时；`audit_publications` 堆积。该已接线 counter 不是 backlog gauge。

**影响**：投递退避重试（指数 + jitter），**不影响**审计事实（事实已先持久化，投递失败不回滚）；
风暴可能占满 callback bulkhead（`callback.worker_concurrency`）。

**快速处置**：

1. 接收端先扩容/限流；需要停发时按密钥维护手册将 `callback.delivery_paused=true` 静态配置滚动覆盖**所有 API/worker sender**。
   这不是动态 pause/ack 屏障，不能撤回已准入请求/字节或接收端效果；不要借删除订阅、SQL reset 或清历史掩盖不确定结果。
2. 出站并发受每进程 `callback.worker_concurrency` 约束（示例配置4；0在服务内回落串行；上限64），不是全局配额；配置变更需重启。
   单次网络阶段共享 `callback.request_timeout`（默认5s，最大5m，含 DNS/两次发送检查/dial/TLS/有界回应），不是每阶段续期或整个 drain 的硬中断。
3. 先用 [投递恢复元数据接口](runbooks/callback-deliveries.md) 在自己的订阅/tenant 下核实原 Delivery、事件 ID、revision/generation 和本地状态。
   新人工重放优先使用现有 `callback_replay` CommandIntent：原 dead-letter、无 owner/lease timestamp、当前授权和原版本检查后，明确预览/双 ID 确认，得到的只是本地排队回执。
   原意图只用原 ID/键 GET 核实，不自动重发/换键；事件 ID/订阅 signing secret 不轮换，接收端按实际 `X-Aero-Callback-Event-ID` / signed body 的原 `event_id` 去重。
   旧 replay API 仍不是版本 CAS/持久业务意图；缓存丢失/过期、在途或旧 unknown 先核实远端，列表/过期 lease/本地回执不能证明无效果或允许第二个键。
4. 核实生产 DNS/CA/egress 策略：[网络阶段约束](runbooks/callback-deliveries.md#network-phase-budget-and-destination-policy) 会整组拒绝混合私网/共享/特殊地址答案、超32答案和受限 IPv6 地址，回应 header 上限64KiB。
   注册前策略拒绝可为 `callback.endpoint_invalid`，worker 目的端/DNS/TLS/回应失败仍走 `callback.delivery_failed` 的原有有界重试/DLQ；计数不能证明实际发过 HTTP。
   显式发送资格检查拒绝则保留原 delivering claim/attempt，区分 `send_ineligible` / `send_check_failed`。不能一概把 timeout 当未发送，或在生产开启 private-target 绕过。
   先修复获批配置/接收端并核实原事件；不要仅因新 DNS、2xx 或超时便创建另一重放意图。

**根因排查**：接收端故障、订阅事件过滤过宽（`event_types` 空 = 全量）、审计事实激增、
密钥/签名轮换后接收端未更新校验。

**恢复确认**：受控 backlog/接收端证据显示故障已恢复，重复投递按原事件 ID 去重，无重复业务副作用。
有界当前行列表不证明全局 DLQ 清空，counter 也不能代替尚未接线的深度/最旧年龄 gauge。

---

## 6. Operation / Sync 继续执行停滞（含 unknown）

**症状**：`operation_age` 增长；原 Operation 或 `GET /v1/sync/jobs/:id` 的状态不推进，
或详情出现「继续执行授权阻断记录」。

**影响**：阻断本身不等于执行失败或回滚，原 `unknown` 仍不确定；**严禁**盲目重发源命令、换键或重置结果。

**快速处置**：

1. 先 GET 原任务，核对 tenant/account、状态、更新时间、根级 `error_code` 和原来源 operation ID；
   Web 详情 GET 的固定 `response.invalid` 也可能是根级状态缺失、非字符串、未知或不属对应 family，而不只是 ID 不匹配。
   坏回应整份拒绝、自动 GET 暂停；保原键/回执，显式重试仍只 GET 原 ID，不手工改状态/正文或关闭校验恢复显示。
   这不证明任务失败、远端未执行或回滚，也不代替当前读取授权、完整元数据/状态转移校验。
   手动详情刷新期间（含正文）自动 GET 暂停，不抢占该读取；读取失败仍停轮询，仅显式 GET 原 ID 重试。
   合法 active 回应后按原退避间隔恢复，unknown/终态停止，不重置60次/五分钟预算或单次 GET 期限。
   显式刷新仍可取消已开始的后台 GET，迟到结果不采用；这是本地详情协调，不是全局并发硬上限或独立审计读取的串行屏障。
   查有权读取的脱敏审计，Operation 时间线为 `GET /v1/audit/operations/:id`。
   Web 审计首屏的加载/读取失败不表示「暂无事实」；显式重试仅 GET 原 ID 的审计首屏，不运行/对账任务或换键。
   成功空查询也只表示本次无可见事实，不证明完整历史为空或远端未执行。后续页失败保留已查看事实、以原 cursor 重试；
   每次 GET 仍由服务端核当前读取权限/tenant 和读取审计，不通过扩大权限、改事实或重发命令恢复显示。
   审计页 `response.invalid` 也可能是根级 `operation_id` 与原请求不符、`events` 不是对象数组或 cursor 类型错误；
   客户端拒绝整页，不显示被拒正文、静默丢弃坏项或当作无事实。保原 ID/已查看事实/原 cursor，仅显式 GET 重查；
   不手工修 payload/事实或改用另一目标。ID/形状匹配不是签名验证、当前授权或完整历史证明。
   `operation.execution.blocked` / `sync.execution.blocked` 是继续执行阻断事实，不是来源未执行证明。
2. 精确 `operation.authorization_denied` / `sync.authorization_denied` 表示记录时拒绝原发起者所需动作；
   对应 `authorization_unavailable` 表示当时授权或目标无法核实，不一定是明确拒绝或仅 Snaplink 故障。
   交身份/部署 owner 核实获批权限、目标与依赖，不自动扩大权限、启用 fallback 或借用操作者 grant。
   UI 只对已知非终态、无完成时间的四个精确码显示历史警告；worker 继续后旧码可能仍存在，不能当作当前授权。
   终态、未知码、scope/lease/erase/fence 错误不按这类授权阻断恢复。
3. 原 unknown 仅通过已有原 ID 的显式对账核实原 outcome；使用当前操作者权限及原对账键，
   服务仍独立检查持久发起者当前授权、租户/账户/home、活租约与 writer fence。超时或丢回应保原键，先 GET 原回执，
   不自动重发。Web Job/Operation 对账还要求本地详情读取成功、精确根级 unknown 及原当前动作资格；前台刷新中（含正文）、读取错误或已观察到其他状态后，旧处理函数也不准入 POST，卸载后同样拒绝。
   拒绝不生成/持久化新对账键，不改原 ID/键/结果；只 GET 恢复 unknown 不自动对账，仍需显式人工动作。
   本地守卫不是持久发起者 grant、全局时点或远端结果证明，不能召回已准入效果；不手工改状态/正文或换键恢复按钮。
   若 Web 显示运行/对账回执与原任务不一致，可能是缺少精确根级 `job_id`/`operation_id`、错 family/ID 或原 scope 不符；
   别名不能替代。根级 `status` 非精确字符串、不属对应 OpenAPI/domain family enum 或为未支持的 `canceled` 也会拒绝；
   不强转数组、trim 或以嵌套/另一 family 状态补位。拒绝时保留上次已观察状态/结果和原键，不展示坏回应或发成功通知；这不证明远端未执行或事务回滚。
   先 GET 原 ID 核实，后续人工动作仍用原 ID/键，不修改 payload/回执来通过检查，不升级兼容 HTTP 缓存的幂等保证。
   原 pending 的后台检查有本地退避，警告不会新增 Run、对账或调度，也不能据此手动改状态/续 lease。
4. 已知远端结果由原 Saga/Sync 的受权恢复路径推进，不保证一条成功结果就让整个 Operation completed；
   补偿也受授权/租约检查，unknown 不是补偿或 rollback 许可。导出 unknown 只核原制品 key/digest/size/TTL，
   NotFound 不允许重 Put、换键或续 TTL。
5. 查 worker 崩溃、原 attempt/lease 过期、home-region 覆盖和目录 fence；过期尝试保守进入 unknown，
   不手工重置租约绕过。审计/持久化不确定只报告原结果待核实，不能按 best effort 继续。

**Web 操作注意**：危险 Run 确认打开后，后续 GET 失败/被拒/陈旧/错目标或已观察到非 `requested/waiting` 状态、完成时间，
确认会暂停；前台详情刷新中也不放行。提示不重写输入/原键，不是任务取消或回滚。显式关闭后仅 GET 原详情，
不要为恢复按钮手工改状态、换键或重新创建 Operation。普通 Run 入口及危险最终确认的处理函数还拒绝卸载后的新调用，
同 ID 新视图/client 不为旧 listener 续权；原 Operation ID/稳定 Run 键、读取/状态/当前资格检查不变，不自动运行或重试。
卸载不取消已准入 POST，迟到回应不采用也不证明回滚；丢回应先 GET 原回执核实，不生成新命令或推导远端未执行。
最近本地状态与无完成时间不证明远端未执行；后台仍独立核授权/lease/fence。

若出现「正在加载页面代码」或「页面代码加载失败」，这是 Operation 静态代码按需加载，不是原任务/审计 GET 结果或远端失败。
15秒显示期限不取消浏览器 import；晚到代码仍服从当前路由/会话准入，代码缓存不是 grant。
不自动重试，不清原键或重发命令；显式刷新页面后按原意图/回执只读核实。
由部署 owner 核 index/chunk 配套、旧会话所需资源可达及实际浏览器错误，不通过放宽权限、关闭校验或重建任务恢复页面。
能力重查直到新 data/error 发布后才结束 checking，不能拿回调返回瞬间的旧允许提示当作新 grant。

**根因排查**：持久发起者权限撤销、权威或目标核验不可用、worker/区域覆盖、源超时但实际成功、补偿或租约异常。

**恢复确认**：原回执和已知结果未被覆盖；当前受权的原恢复路径取得可核实结果，审计与原 attempt 对应；
没有以本地状态/警告消失冒充远端完成、当前 grant 或无重复副作用证明。真实来源/权限/跨区演练仍需部署验收。

---

## 7. 导出泄露处置（export artifact 外泄怀疑）

**症状**：导出下载 URL 泄露 / 审计发现异常下载 / 导出工件 TTL 未过期即被外部访问。

**影响**：敏感数据（账户/资源摘要，**非**对象字节）可能暴露。

**快速处置**：

1. `/v1/sync/jobs/:id/export/download` 是 Bearer 认证读取，不是签名 URL；`/files/dl` 的本地签名不能借用。
   核对 verified tenant/caller、原 job/artifact 范围、摘要、大小和原 TTL。对象读取前后直接检查当前读取者的
   `account.export`，或审计制品的 `audit.export` **和** `audit.read`；每个检查点内权限共用三秒预算。
   缺权威/故障/取消/迟到允许应 503，明确拒绝应 403，不允许 legacy/ownership/原发起者 fallback；核实全部 API 副本已升级。
2. 由身份 owner 处置当前读取权限/会话，并由存储/合规 owner 批准精确原制品的受控处置，遵守 Legal Hold、驻留和保留政策。
   不直接改 job/checkpoint、延长 TTL 或生成替代导出；撤权/删除不能召回已准入或在途字节，也不是迟到 Put/跨区擦除证明。
3. 查 `account.export.downloaded` / `audit.export.downloaded` 脱敏事实的当前读取者、原 job 和执行 ID，评估暴露面。
   成功读取审计和一次性指针消费原子提交；拒绝不写成功事实。响应丢失/删除不确定可能已消费，只 GET 核原任务，不自动重下或新建。
   Web 下载入口与最终确认还要求原详情读取成功、精确 completed 及当前读取者资格；前台刷新中（含正文）、读取错误或其他已观察状态时，旧确认 listener 不准入下载，卸载后也不准入。
   只 GET 恢复 completed 元数据不自动消费，仍需显式确认；不修改原 ID/键/结果或 artifact/TTL 来恢复按钮。
   本地暂停不是撤权、回滚或制品可用性证明，不能召回已准入消费/字节、保证重下或据此生成第二份导出；后端当前读取者/tenant/原制品/审计核验不变。
4. 若怀疑 `export.encryption_key` 泄露，由 owner 批准轮换及旧制品处置；当前实现无历史解密 keyring，轮换可能使旧制品不可读，不能承诺透明恢复。
5. 按数据驻留策略评估是否涉及跨区域违规（raw payload/PII 不得出境）；响应 `no-store` 不保证接收设备没有另存副本。

**根因排查**：日志中泄露 URL、下载端点绕过校验（`/files/dl` 签名仅限本地存储）、
回调接收端转发工件链接、审计缺 metadata-only 记录。

**恢复确认**：记录 owner 批准处置与原制品结果，复测当前读取权限（含审计双权限）、租户/原制品/TTL、事务审计回滚和 `no-store`；
分别保留本地测试与实际部署证据。未核实的迟到写、跨区域/设备副本和在途字节继续标为未知；必要时走合规上报。

---

## 8. 通用原则（所有场景）

1. **先读后写**：任何写处置前，先查审计与 operation 时间线，避免凭猜测重放。
2. **不双写**：区域故障、源故障期间，禁止用绕过 fencing/幂等键的方式重发命令。
3. **DLQ 优先于重试风暴**：反复失败的事件/投递进 DLQ/quarantine，人工处置，不无限重试。
4. **审计优先**：任何数据修复/回滚操作本身也要产生审计事实（新 operation_id/幂等键）。
5. **演练**：发布前按 `scripts/load/` 场景 + chaos 用例（源 503/429/超时、NATS Nak 重投、
   区域 loss/fencing、callback outage）演练本 runbook；真实 WAN 分区与 DB failover 属外部基础设施门禁。
6. **Profile 换页与保存**：卸载后的原 submit 处理函数不准入新的 PATCH，同账户新视图/会话不借给旧草稿。
   这不是当前授权、持久幂等或回滚；已准入保存仍可能提交，丢回应/超时/关闭页面先 GET 当前 Profile，
   再决定是否显式人工保存，不自动补发。四字段单在途/草稿保护及服务端身份/tenant/home/fence、事务审计不变；
   草稿只在当前挂载内存，换页/刷新不保证恢复。
