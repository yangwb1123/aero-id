# aero-account-console 后端契约

这是独立账户客户端的最小后端契约。客户端只访问 aero-id；除登录跳转外，不直接调用 Snaplink、Aero IM 或 Aero Vault。

## OAuth client registration（P7-02）

在 Snaplink 注册**独立** OAuth/OIDC client（禁止复用 snaplink-console 的 client）：

| 项           | 值（示例）                                                                                                                                   |
| ------------ | -------------------------------------------------------------------------------------------------------------------------------------------- |
| client_id    | `aero-account-console`                                                                                                                       |
| redirect_uri | `https://console.<region>.example.com/`                                                                                                      |
| audience     | `aero-id`                                                                                                                                    |
| grant        | Authorization Code + PKCE（S256，强制）                                                                                                      |
| scopes       | `openid profile account:read` 起步；按需加 `account:write account:manage audit:read audit:replay operation:read callback:manage source:sync` |

前端使用 Iris UI 的 Vue 适配器，可运行客户端位于本仓库 `web/`。本契约与 OpenAPI
（`api/openapi/aero-id-account.yaml`，仓库有守卫测试防漂移）仍保持技术栈无关；
客户端不复用 `snaplink-console` 的 OAuth client，也不把 aero-id audience 的 token
转发给 Audit Governance、Aero IM 或 Aero Vault。

当前 Web 保留16个入口，仅 Operation 路由在原 shell 准入后延迟加载页面代码，不新增后端请求契约。
代码加载中/失败单独显示；15秒未完成使用固定错误，不展示原始 import URL/诊断，不自动重试或执行命令。
此显示期限不取消浏览器 import；迟到代码仍服从当前 route/client/身份 remount，不能借代码缓存恢复 grant。
已准入精确页面的能力重查保留规则不变，报告不可用时原动作仍暂停。失败不清原意图指针或授权换键重发，
显式刷新后仍按原 ID/键恢复。部署须验证 index/chunk 配套及旧会话资源可达；本地产物分离不代表浏览器或性能验收。

## 登录与请求上下文

- 在 Snaplink 注册独立 OAuth/OIDC client，不复用 `snaplink-console` 的 client ID。
- 浏览器使用 Authorization Code + PKCE；令牌的 issuer、audience、subject 必须由 Snaplink 校验。
- 每次请求发送 `Authorization: Bearer <access-token>`。客户端可发送 `X-Request-ID`，服务端会在响应中返回它；服务端还返回 `X-Trace-ID`。
- 需要幂等的 POST 同时发送 `Idempotency-Key`。重复请求返回同一任务/操作结果，不重复触发源系统副作用。
- 浏览器 CORS 预检允许 `Idempotency-Key`、`X-Aero-Write-Epoch` 和 W3C trace headers，并暴露
  `X-Request-ID`、`X-Trace-ID`、降级、写 epoch、陈旧读取、幂等重放与重试响应头。

## 响应和数据集

成功响应使用 `{"data": ...}`。错误使用稳定的 `code` 和 `message`；不要把 endpoint、SQL、堆栈或凭据展示给用户。

```text
GET  /v1/account-datasets
GET  /v1/me
GET  /v1/me/capabilities
GET  /v1/me/profile
PATCH /v1/me/profile
GET  /v1/me/sources?limit=50&cursor=<cursor>
GET  /v1/me/memberships?limit=50&cursor=<cursor>
GET  /v1/me/data?dataset=aero-im.workspaces&consistency=eventual
GET  /v1/me/overview
GET  /v1/me/activity
```

`/v1/account-datasets` 是 allow-list；客户端只能请求返回的 dataset 名称，不能拼接源系统路径或 SQL。

### 来源账户与成员范围的只读核对

原 `GET /me/sources` 与 `/me/memberships` 是当前认证身份解析出的账户引用页，不接受浏览器选择另一个 tenant/account。Web 从概览进入原来源与成员关系页，分别读取两个列表（每页50项），选择已返回行即可查看原范围并返回列表，不另查来源详情或发 N+1 请求。列表故障相互独立；显式刷新/失败重试只 GET 对应原列表，后续页保原 cursor，失败不清已查看行或自动扫描全部。取消信号传入首屏/后续页 fetch 及正文，换页/替换读取不采用迟到结果；保原15秒/2MiB请求预算。

视图只使用 HTTP 精确字段：来源的 `source/source_account_id/scope_type/scope_id/status/source_region/version/generated_at/last_synced_at/error_code`，成员的 `source/source_member_id/scope_type/scope_id/role/status/source_region/version/last_synced_at`，及各自本地引用 `id`。不借别名、会话租户或嵌套 data 补位；成员没有来源生成时间或错误码的 HTTP 字段，不从来源行复制。文本有界、版本仅非负安全整数；缺失为「来源未提供」，坏类型不强转/补零。来源 data/ACL、正文、凭据和未知字段不通用展开。

来源+原生 scope 类型+opaque ID 一起显示；同文本 ID 不合并 tenant/workspace/bucket，来源角色（含 owner）不是全局 grant。记录 active/unavailable 与连接启用、投影新鲜度、当前权限各自独立；当前 DTO 未提供 coverage/complete、总数或新鲜度预算，不推断全量、完整空结果或当前来源可用。根列表必须是对象数组、可选 cursor 必须是 string；坏页整页拒绝，不冒充空页。空查询只说明本次无可见引用，不能证明来源没有账户/成员关系。

账户/会话租户/主区域来自原 shell 已核对的当前能力上下文，不从原生 scope 推导；`account.self.read` 提示未确认不继续查询或展示引用详情，链接/角色/菜单都不是来源 ACL。原服务端 verified identity/tenant 解析、账户生命周期、例行读取/审计策略和写命令契约不变；没有新增来源管理 authority 或启用来源能力。资源摘要/范围同步入口仅导航，后续仍须人工选择与原 CommandIntent 预览确认，不自动创建或运行。

### 从原来源引用显式读取同源本地投影

Web 来源账户详情的「选择此来源的数据集」仅建立本页内存中的选择上下文；成员关系没有此入口，来源角色不是 grant。显式打开后只 GET 原 registry，交集为已批准且 name/source 配对的同源目录与当前报告 `registered:true/authorization:allowed`；重复、未知或不配对的同源目录不提供选择。读取前还核当前 self-read 提示与原 issuer/client/tenant/account 上下文；报告/菜单仅为提示，服务端原认证身份/租户/数据集读取授权不变。

选择至少一项并显式点击后才调用原 `/me/data`，只传非空 `dataset` 和固定 `consistency=eventual`。不传引用 ID、原生 scope ID、角色、account/tenant 选择参数或来源路径；该视图覆盖当前账户的所选数据集，**不是原生租户/工作空间/桶筛选**。连接 enabled、投影新鲜度及来源资源 ACL 各自独立；允许读取历史本地投影不启用 connector。无新 HTTP/Protobuf、来源调用、任务或审计策略，不借此提供来源管理能力。

返回原引用/列表不请求或运行；目录故障显式重试只重读 registry，投影失败保所选范围但需再点击读取。返回/替换条件/卸载/当前范围或资格变化取消旧 GET（含正文），原 generation 与提交条件核对拒绝迟到结果，不自动重读；取消不是回滚。结果复用原有界批准业务字段和部分/陈旧提示，不展开未知 data；其他数据集只显示元数据。无 URL/浏览器持久化载荷，刷新不恢复该选择或自动读取。独立普通数据浏览和管理员目标保原空选择全部 registry、可选 bounded/strong 契约，不以调用者自己的数据集资格给管理员目标授权。

`/v1/me/capabilities` 要求验证后的 issuer、subject、tenant 和 client claims，并与当前账户范围一致；返回
`Cache-Control: no-store` 的元数据报告。`registered`、`enabled`、`authorization`（`allowed/denied/unknown`）、
`available` 和投影 `status/fresh/version/last_synced_at` 分别表示不同条件，不能互相推断。权限检查有界并发和总期限；
依赖故障返回脱敏的 `unknown`/`partial`，不会回退到本地角色允许。数据集资格只表示自己的本地投影读取，
`resource_authorization` 始终为 `unknown`，来源调用仍检查资源 ACL；报告不探测源健康或发起源 fan-out。
Snaplink Check 当前没有策略版本，`authorization_version` 明确为 `unsupported`；没有伪造策略水位或承诺
该报告可以授权后续命令。前端导航和按钮依据报告提示，后端每次写入仍重新授权。`callback.read` 是新元数据读取资格，
仍由同一个当前全局 `callback.manage` 权限决定，但不依赖投递密钥/暂停状态；它不授予注册、删除或重放。
Web 写入指导 `callback.manage` 在未配置密钥或暂停时禁用，兼容 API 的管理操作仍按其独立契约授权。
`aero-im.notifications` 是 Aero IM 通知收件箱的有界、无正文投影；只含通知与关联资源 ID、类型、时间、
聚合数、重要度、未读总数和截断标记。账户控制台不通过 aero-id 修改通知状态，处理动作仍进入 Aero IM，
避免在账户系统中建立第二套通知权威。尚未在 Aero IM 完成首次 Snaplink JIT 关联时，投影保持相同结构并以
`source_account_status=not_found` 明确标记；只读摘要请求不得隐式创建 Aero IM 账户。
`aero-vault.tenants`、`aero-vault.buckets` 和 `aero-vault.usage` 只提供租户状态、桶名称及
容量/对象数配额用量，不包含文件正文、对象内容或下载凭据。账户控制台只读展示这些投影；上传、下载、
删除、分享和保留策略仍由 Aero Vault 执行，aero-id audience 的 access token 不得转发给 Aero Vault。
资源页通过原 `GET /v1/me/resources?consistency=eventual`，账户概览通过原 `/me/overview`，按范围数据浏览通过原 `/me/data` 或管理员 `/admin/accounts/:id/data` 的 `snapshots[].data`，共用以下明确业务摘要，不增加来源 API 或启用能力：

| 数据集               | 本轮确认并展示的字段                                                  | 语义                                                                        |
| -------------------- | --------------------------------------------------------------------- | --------------------------------------------------------------------------- |
| `aero-vault.usage`   | `tenant_id`, `used_bytes`, `used_objects`, `max_bytes`, `max_objects` | Vault 租户级用量/配额原值，不是个人用量；字节单位 B，不推测占比或零上限含义 |
| `aero-vault.buckets` | `items: string[]`                                                     | 来源报告的桶名称引用，不含对象或 ACL                                        |
| `aero-im.workspaces` | `items: {id,name,slug}[]`，或 `status: not_found`                     | 工作空间引用；未关联来源账户与空引用分开，不触发 JIT 创建                   |

数值只展示非负安全整数，缺失为「来源未提供」、坏类型/超出精度为固定无效提示，不强转/补零。每个引用列表只验证并显示前50条，超出部分明确未展示；边界脱敏产生的 `truncated:true` 也保留警告，不把截断的空/缺字段当完整空结果。未知嵌套字段不展开，来源/数据集不配对、重复快照或坏引用不能冒充有效空列表。当前聚合 DTO 未传来源 `coverage/complete`，不能据此认定全量、跨源同一时点或来源总数。来源版本、区域、生成/同步时间与陈旧/部分状态保留；不可用/未知状态不把历史值当当前资源。本地读取资格、连接启用和来源 ACL 分开，明确拒绝时不展示该块业务内容。

资源/概览刷新只 GET 本地投影；概览账户、目录和聚合 GET 共用该次读取取消信号。账户概览先显示业务摘要与账户/租户/主区域，技术可见条数与能力表折叠，不把缺失引用列表显示为零。新的同步/导出仍通过原显式范围和 CommandIntent 预览/确认，不自动提交任务。

数据浏览只有显式点击才 GET；展示本次提交范围、返回模式和账户上下文，仅为所选的上述三类生成业务块，不把未选择的类别当作待同步或空结果。保留原空 `datasets` 表示服务端全部 registry 的 GET 兼容契约，读取前后明确提示，不能据此为同步/导出选择隐含全范围。条件修改/替换/卸载取消旧 GET（含正文）并保原 generation 迟到守卫，不自动重读或续期限；取消依赖协作，不是命令取消或撤回来源刷新。Bounded / Strong 可能执行来源有界刷新，但不是全局事务或因果水位。管理员目标不采用调用者自己的数据集资格作为目标 grant/denial，原服务端权限与 tenant/target 验证不变。

其他注册数据集当前只显示投影元数据并明确业务字段视图尚未提供；技术区不通用展开 `data`，不借未知 JSON 构造身份、ACL 或资源权威。注册目录不是已接通来源；本地源码核对及受控 Iris/Chrome browser fixture 不替代 source owner/部署、真实登录与远端业务验收。

`snaplink.identity`、`snaplink.tenants`、`snaplink.permissions` 和 `snaplink.security` 仅按 Snaplink
明确响应字段展示身份、账户状态、租户、角色与权限。客户端不得通用展开未知字段，也不得显示密码、
MFA 密钥、会话令牌或其他凭据；身份验证和授权写操作必须回到 Snaplink 管理端执行。
聚合快照包含 `source`、`source_region`、`status`、`version`、`generated_at`、`last_synced_at` 和可选
`expires_at`。`status` 可能为 `fresh`、`stale`、`unavailable` 或 `unknown`。聚合响应中的 `partial`、
`source_errors` 和 `stale_datasets` 必须在界面上明确展示。

`consistency=eventual` 只读本地投影；`bounded` 和 `strong` 会在有界 deadline 内刷新指定源，不能保证跨源
事务一致性。源服务不可用时，其他源仍可返回。

## 账户管理和异步任务

客户端可以修改的 Profile 仅限 aero-id 拥有的展示/偏好字段；邮箱、电话、验证、MFA 和生命周期字段不在
Profile PATCH 中。当前 Web 表单只发送 `display_name`、`avatar_url`、`locale`、`timezone`；
保存处理函数在卸载后拒绝新的调用，不只依赖 DOM 移除，也不能借同账户新视图或新会话发送旧草稿。
拒绝发生在保存状态/提交快照修改及调用原 client 之前；不取消或回滚已经准入的 PATCH。
已有 PATCH 未结束（包括响应正文读取）时，处理函数忽略重复 submit，不排队或自动重试。
完成或报错后另一次显式保存仍走原 PATCH；原服务端身份/租户/fencing 与事务审计不变。
每次请求捕获四字段快照，成功时只为仍等于该次提交值的字段采用服务端返回值；保存期间后来编辑/清空的字段留在草稿，
不写入已采用的服务端结果。有差异时明确提示上次提交已保存、仍有未保存修改；失败保留当前输入，不自动补发。
此保护仅属当前挂载表单的内存/值比较，不是 Profile 版本 CAS、跨客户端冲突合并、持久幂等、跨标签页锁或 grant；
草稿不存浏览器存储，刷新/换页不保证恢复，不新增预览、键或来源命令。
丢回应、超时或关闭页面不能证明回滚，应先 GET 当前 Profile，再决定是否重新人工保存。
原 GET/PATCH 成功 DTO 总有四个根级文本字符串，空字符串有效，OpenAPI `Profile` 明确要求这些字段。
Web 在采用回应前核对四字段均为 string；缺失、null、其他类型、别名或嵌套字段不替代，不强转或补默认值。
固定 `response.invalid` 拒绝整份坏回应，不回显被拒内容、不采用部分字段、不自动请求或保存。
初次读取失败的显式重试只 GET；PATCH 坏回应保当前草稿、不显示保存成功，不能推断服务端更新回滚，先 GET 核实。
这是最小文本形状检查，不是完整 metadata/preferences 校验、账户/租户关联证明、当前 grant、业务字段校验或版本 CAS；
不改变原取消/迟到回应守卫、四字段请求及服务端身份/租户/fence/审计契约。

```text
POST /v1/me/sync
POST /v1/me/export
POST /v1/me/erase
GET  /v1/sync/jobs?limit=&cursor=
GET  /v1/sync/jobs/:id
POST /v1/sync/jobs/:id/reconcile
GET  /v1/sync/jobs/:id/export/download
POST /v1/operations
GET  /v1/operations/:id
POST /v1/operations/:id/run
POST /v1/operations/:id/reconcile
GET  /v1/audit/operations/:id
```

同步、导出、删除和跨源命令均为异步任务或 Operation。客户端按 `job_id`/`operation_id` 轮询，展示
`completed`、`partially_completed`、`failed` 等终态及 `unknown` 不确定状态；`unknown` 不能自动当作失败重试，必须等待
对账或人工确认。Sync Job 与 Operation 的 `reconcile` POST 都使用 `Idempotency-Key`；结果不确定时保留原键，Web 仅在采用匹配原任务的回执后才清本次键，下一次人工对账再创建新键。Operation `run` 复用稳定键。
Web 的原任务 GET 在初次读取、显式刷新和原有轮询时核对响应根级 `job_id`/`operation_id` 与原请求 ID 的精确字符串相等，
并要求根级 `status` 为下述对应 family 的精确字符串。不以 `id` 别名、另一任务类型字段、嵌套 ID/状态或类型强转代替。
缺失、错目标、非字符串、未知或跨 family 状态以固定 `response.invalid` 拒绝整份回应，不展示被拒内容、清原键/回执或触发命令。
读取错误暂停自动 metadata GET；显式重试只 GET 原 ID，保留原取消/迟到回应守卫。
前台详情刷新中（包括正文读取）也暂停轮询，不让新的自动 GET 取消该读取或掩盖其错误。
采用合法 active 回应后才按既有退避间隔恢复；unknown/终态/读取错误仍停止，不立即补发。
原挂载周期最多60次/五分钟轮询预算与单次 GET 期限不重置、不延长。
显式刷新仍可取消已开始的后台读取，旧回应受原迟到守卫约束；取消依赖协作，不是全局并发硬上限。
此协调只覆盖同一详情的轮询/前台 GET，不串行化独立审计读取，不授权或重发任何命令。
这只是响应关联与基本形状检查，不代替服务端当前授权/租户隔离、完整元数据/状态转移校验或远端完成证明。
详情动作采用 Run/Reconcile 回应前，也按原路由 family 核对原详情和回执的精确根级 ID；Sync Job 只用 `job_id`，
Operation 只用 `operation_id`，`id`/另一 family 字段/嵌套 ID/强转不补位。原 account/tenant/actor/type 比较仍保留。
回执根级 `status` 还必须是对应 family 的精确字符串：Job 为 `pending/running/completed/partially_completed/failed/unknown`；
Operation 为 `requested/running/waiting/unknown/partially_completed/completed/failed/compensating`。
数组等非字符串、跨 family、未知或带空格值不强转/trim/借嵌套状态补位；两种任务目前都没有 `canceled` 状态。
OpenAPI 两个 envelope 明确 required `status` 及各自 enum；GET 与动作回执共享根级状态 allow-list，
不扩展到步骤状态、状态转移或完成时间一致性校验。
不匹配仅显示固定回执错误，不展示被拒内容、替换原状态/结果、发成功通知或清原对账键；不自动 GET/重试/换键。
先 GET 原回执核实，之后若仍需人工动作只使用原 ID/键；坏回应可能发生在效果已提交后，不证明回滚或远端未执行。
这是详情页的局部采用检查，不是完整 DTO 验证、grant、事务或对旧 HTTP 通用幂等缓存/gRPC 重试语义的升级。
Web Operation 的脱敏审计首屏单独显示加载/读取失败，仅成功空查询显示本次无可见事实；
拒绝、故障、无效 JSON 或陈旧读取不表示「无事实」，空查询也不证明远端未执行或完整历史为空。
显式重试只 GET 原 `/audit/operations/:id` 首屏，不重读或运行任务、不换键；后续页错误保留已查看事实并重试原 cursor。
既有每页50项、15秒/2MiB传输预算及取消/卸载迟到守卫不变，不自动轮询/重试审计或新增修复命令。
服务端仍检查当前 `audit.read`、认证 tenant，并在成功返回前持久化读取审计；重试不是恢复权限或历史完整性证明。
审计接口的实际响应是 `data: {operation_id, events, next_cursor}`，不是 Operation 步骤/尝试；支持原 opaque cursor 和 limit。
OpenAPI 使用独立 `OperationAuditEnvelope`。Web 对每页核对根级 `operation_id` 精确等于原请求 ID、`events` 必须是对象数组，
存在的 `next_cursor` 必须为字符串；缺失/错目标/类型错误均以固定 `response.invalid` 拒绝整页，不静默过滤坏项或当作空结果。
原事实与原 cursor 不因后续坏页被替换/消费，显式重试只 GET 原 ID/cursor，不回显被拒正文或换键。
这只是响应关联与基本形状检查，不是完整事实字段/签名验证、当前 grant、全局时点或完整历史证明。
Operation Run 入口与最终确认另共用本地读取/状态门禁：原详情读取成功、根级状态精确为 `requested` 或 `waiting`、
且 `completed_at` 缺失/null，并继续要求原当前动作资格。前台刷新中、GET 失败/拒绝/陈旧/错目标，或已观察到其他状态/完成证据时，
即使危险确认框已打开并填好短语，也不能发送 Run。提示不重写确认输入、原 ID/键或结果；显式关闭后再 GET 原详情。
普通 Run 入口和危险最终确认的处理函数还在卸载后拒绝新调用，不依赖 DOM 移除；同 ID 的新视图/client 不为旧 listener 续权。
已准入 POST 不因卸载而取消或回滚，迟到回应仍不采用；挂载后的显式人工动作保原 Operation ID/稳定 Run 键，
不自动运行/重试、换键或加强旧 HTTP 缓存的幂等保证。
轮询节奏、原 POST/回执及后端当前授权/租约/fence 不变，不新增自动运行/对账/取消或换键。
最近一次采用的本地元数据仍非全局时点；没有完成时间也不证明未执行，不能撤回已准入动作或冒充后台继续执行许可。
Job/Operation 对账按钮和处理函数各自共享本地门禁：原详情读取成功、精确根级 `unknown` 和原当前动作资格。
前台刷新中（含正文）、读取失败/被拒/陈旧/错目标或已观察到其他状态时，保留的旧事件处理函数也不能 POST；卸载后不准入。
拒绝发生在生成/持久化对账键之前，保原 ID/键/结果；仅 GET 恢复合法 unknown 不自动对账，仍需另一次显式人工动作。
此本地守卫不改变原 POST/回执和服务端持久发起者授权、tenant/account/home/lease/fence/审计，不是 grant、全局时点或远端结果证明，不能撤回已准入效果。
导出文件是短时、加密、受权限保护的临时工件，不能缓存到公共 URL。账户/审计导出任务只返回元数据；
历史任务读取过滤不代表旧持久内容已经清理。制品结果不确定时仅对账原 key、摘要、大小和 TTL，
不会重新生成或延长有效期；对象暂时不存在仍可能是旧写入尚未完成，不能据此创建第二份导出。
下载核对持久检查点、内容与任务的范围/有效期；对象读取前和最终本地准入前均以当前 verified 读取者直接查询 Snaplink，
账户导出要求 `account.export`，审计导出同时要求 `audit.export` 和 `audit.read`。每个检查点内的所有权限共用三秒期限，
继承更早的请求期限；缺 checker、故障、取消和迟到允许均返回 `export.permission_check_failed`（503），拒绝为 `export.forbidden`（403）。
不以旧 fallback、ownership、原发起者或已生成制品代替当前读取许可；不完整/不一致的 verified claims 返回 401。
下载 handler 的成功与错误响应均 `Cache-Control: no-store`，不声称覆盖上游代理/鉴权中间件生成的响应。
三秒不是全下载/对象 IO 期限，也不能硬中断忽略取消的依赖（迟到允许仍拒绝），不是 policy version/session 或分布式事务证明。
最终本地下载准入与擦除共用事务锁；一次性下载的指针消费与成功审计原子提交，删除不确定时保留内部清理检查点。
一次性响应丢失后不能保证再次下载成功。擦除前已获准的响应仍可能随后到达，无法撤回已经发送的字节；
这些本地屏障不等于跨区域擦除、源删除证明或 Legal Hold 验收。

### Web 原回执导航

列表选择与合法创建/恢复回执统一更新 `#/jobs/<UUID>` / `#/operations/<UUID>`，复制链接、刷新及浏览器前进/后退都定位原 ID。返回/关闭回到列表 URL，刷新不重开旧详情；选中结果优先于长列表并获得焦点。URL 只含 family/UUID，不含计划、确认输入、凭据或命令键，不提供跨租户/当前授权。新目标先经原 shell 准入，再走原 GET 的精确 ID/status 校验与服务端原读取契约；不借旧详情准入新目标，不改 HTTP/Protobuf。

列表/目录读取失败不再遮住独立可读的原详情；详情 GET 拒绝或 NotFound 仍提供原链接/返回，不据此换键、重新创建或推断无效果。切换目标取消旧 GET，原迟到守卫、动作资格和恢复键保留；返回/刷新不 Prepare/Execute/Run/对账或消费制品，也不取消或回滚已准入命令。受控真实 Iris/Chrome 与 component/unit、live OIDC/来源验收分别记录。

### Web 原结果到审计的明确筛选导航

结果链接使用 `#/audit?request_id=<encoded>&from=jobs&task_id=<UUID>`，或 `#/audit?operation_id=<UUID>&from=operations&task_id=<same UUID>`；Operation 也可单独按 Request ID 查询。`from/task_id` 只属本地返回上下文，绝不作为 HTTP tenant/权限/Job 查询参数。一次链接仅一种过滤，避免将创建请求与后续步骤错误交集。保留普通 `#/audit` 入口及原手动筛选。

Hash 解析逐参数解码并拒绝额外/重复字段、任意返回站点、非nil UUID 以外的返回目标及不匹配的 Operation ID。Request ID 只取原根级 string，本地最多512字符、无控制/孤立代理字符、无首尾空白；整条路由最多2400字符。缺失/无效不从 Job ID、来源 operation ID 或 event ID 推导；不把这些 UI 预算改成服务端 Request ID 契约。仅传批准标识，不携带请求载荷、token、确认输入或命令键。

台账预填不发送 GET，复制/刷新只恢复初始条件；用户确认当前输入后显式查询原 `GET /audit/events?request_id=` 或 `?operation_id=`，每页50项。分页/重试绑定已提交过滤和原 opaque cursor，不采用尚未提交的编辑；上下文替换/卸载透传 AbortSignal、保原15秒/2MiB预算和迟到守卫。「重置」清空条件/结果而不自动全量读取。读取失败保筛选/返回入口；成功空页不证明未执行或完整历史为空。

返回链接仅定位原 Job/Operation UUID，仍经 shell 当前资格和原 HTTP GET 读取授权，不重建、Run/Reconcile、下载或 replay，也不取消/回滚已准入命令。`audit.read` 提示未知/拒绝时不新查询，服务端认证 tenant、当前权限和成功返回前持久读取审计不变；链接/过滤不是 grant、来源效果或完整因果时间线。无新 HTTP/Protobuf 字段/事务或来源能力。

### Web 任务结果的业务映射

原 `GET /sync/jobs/:id` 详情优先显示中文状态、目标账户/租户、原 `request.datasets` 范围与时间，不推算百分比。Sync/Reconcile/Erase 只为该范围的已注册来源映射 `result.sources[source]` 的明确状态，以及摘要 `count/complete/skipped_datasets`、对账 `drift_count/repaired_count/outcome` 或擦除 `source_operation_id` 和固定形状结果码；不展开嵌套来源载荷。缺项明确未报告，不能推断未执行，单源投影条数不是资源总数。

账户导出只读取 safe result 现有的 `dataset_count/source_count/membership_count`（投影条数/来源引用/成员关系引用）、`missing_datasets/complete/coverage_complete/traversal_complete`、`generated_at/expires_at`。`completed` 与数据覆盖/可用性独立，不完整有显著警告；可用也可能陈旧，不是跨源同一时点。元数据没有逐来源区域/版本/新鲜度，不编造，原明细仍只在制品。审计导出单独映射 `event_count/truncated`，无截断字段不默认完整，无数据集覆盖推断或自动续批。数值仅非负安全整数，缺失不补零；范围列表最多11个唯一 registry 名称，未知字段不进入业务视图，原 JSON 仅保留默认折叠的脱敏诊断。

显式结果绑定字段存在时须与原 job/account/tenant/type 一致；账户清单选择/缺失范围须匹配原请求，坏/矛盾/不支持版本元数据不采用。已知到期、`artifact_deleted_at` 或这些元数据问题会暂停下载；有效期只按有时区的合法日期解析，页面时钟更新及最终 handler 均检查已知期限，但服务端时钟/当前读取者/原指针/TTL才是最终依据。历史结果缺有效期不冒称可用，仍允许原有显式请求并交服务端拒绝/核验。收到一次性响应后仅本页内存标记阻止再次消费，提示检查保存位置，不保证磁盘保存、跨刷新恢复或重复下载。丢响应、清理不确定、unknown 仍保原回执/键，不自动请求、重 Put、续 TTL 或生成新导出。

结果提供返回任务、当前资源页及审计台账的原路由；原 Request ID 可通过明确链接预填后显式查询，不把 Job ID 当审计事件 ID 或来源操作 ID，不自动读取或验证完整审计链。列表和技术表在窄屏局部横向滚动。当前动作资格、原 GET 取消/迟到守卫、轮询预算、unknown 对账及服务端授权/事务消费/审计均保留，无新后端契约。

### Web Operation 步骤与补偿的业务映射

原详情先显示根状态的中文语义、目标账户/租户/主区域与已报告时间，再展示最多32个服务端有序步骤的最新记录。明确映射 `step_key/source/action/status/attempt/source_operation_id/error_code`；补偿使用独立 `compensation_action/status/attempt/source_operation_id/result/error_code`，不借正向字段补位。原始请求/结果与技术表默认折叠。未知/失败/部分完成各自说明安全下一步，补偿计划不是执行记录，单步补偿成功不证明整体回滚，计数缺失不补零。

摘要动作只读取 `result.status/count/complete/application_outcome/skipped_datasets`：条数是本次摘要投影数，不是资源总数；应用结果按 `SummaryApplyOutcome` 映射，`applied` 不保证每条投影更新，旧版本忽略不当作新应用。跳过范围最多11个批准且同源数据集；未知/坏字段不通用展开。非摘要结果仅展示已确认状态标记/结果码，仍以步骤持久状态区分未知与成功。

HTTP Create/Get/List/Run/Reconcile 的每步新增 `dataset_scope: {status, datasets}` 安全只读元数据，正常读取权限不变。`explicit` 只包含该步原已持久化的非空、唯一、同来源 registry 名称，最多11项且保原顺序；`missing` 表示未存储此字段，`invalid` 表示存在但不合法，二者 `datasets: []` 且不暴露坏名称。不执行来源调用、不展开 legacy 摘要默认范围、不写回原载荷，不返回任意逐步 `request_payload`、凭据或来源正文。正向与补偿使用该步同一持久范围；字段是数据集范围，不是桶/工作空间资源 ACL 或执行授权。

Web 业务视图与原动作资格共用精确 scope 解析；只为 `explicit` 且合法的同源名单构造本地动作提示，拒绝坏状态/类型/重复/未注册/跨源或空范围，不从 root request、raw step payload、历史审计或 Protobuf 补位。原 `commandAvailable` 仍核当前根/正向/补偿资格、来源摘要启用及 target/tenant/home；原详情读取/挂载/状态、危险精确确认、原 Run 稳定键及 unknown 原对账键均保留。具备条件时可显式继续原回执，不再因 HTTP 漏逐步范围而一律暂停；来源未启用、动作/补偿拒绝、范围未知或主区域不符有具体原因。服务端仍独立复核原发起者、当前操作者、account/tenant/home/lease/fence 与来源 ACL；本地提示、范围及原预览都不授予 grant，不自动 Run/Reconcile/换键。

HTTP 仍没有逐步骤时间；步骤视图不是尝试历史，补偿可能逆序，实际时间另读原 `/audit/operations/:id` 的事实。审计保原 action 和 `occurred_at`；补偿 expiry 的 `attempt` 按真实事实形状显示补偿次数，不与正向次数混用。不从缺失补偿来源 ID 推导正向 ID。受控 Iris/Chrome 合成 HTTP 正常及拒绝验收不替代真实 OIDC、原发起者授权、来源效果或完整集成验收。

原状态刷新、审计首屏/原 cursor 显式错误恢复与取消/迟到守卫不变；返回列表、当前资源及审计台账导航不触发命令。原 Operation ID / Request ID 可分别通过链接预填后显式筛选，当前资源不是操作当时快照；不合并为全局时间线、来源效果证明或授权。系统 Chrome/真实 Iris 的受控合成 HTTP 流程与 component/unit、真实 OIDC/来源验收分别记录。

## 服务端命令预览与执行

```text
POST /v1/command-intents                  # Idempotency-Key；显式 plan/datasets
GET  /v1/command-intents/:id               # 原 caller/issuer/subject/client/tenant 范围
GET  /v1/command-intents/by-key            # 原 Idempotency-Key header；只读查找
POST /v1/command-intents/:id/execute       # 仅 version + confirmation_digest
POST /v1/command-intents/:id/abandon       # 原 version=1 + confirmation_digest；不取消任务
```

预览持久化规范化计划、目标账户、本地 disposition/目录/区域 epoch 和所选快照元数据，默认有效期五分钟。
创建和执行有事务审计；GET 是原调用者范围内的脱敏计划/回执读取，不读来源内容。
工作流步骤必须显式声明同源数据集；补偿动作也需匹配实际权限和范围。export 必须启用，未启用源不新建同步/擦除。
所有接口 `no-store`，请求有 256 KiB/32 步预算。拒绝客户端 tenant、未知顶层字段、尾随 JSON 和执行载荷覆盖。

执行重新授权、重建并比对原计划，再在 directory → regional epoch → account → intent 锁序下核对
本地前置条件、版本和数据库当前时钟，原子创建现有 Operation/Sync receipt、执行审计及 intent 状态。
同一确认的并发/丢响应重试只返回原回执，不自动运行命令。未确认结果保留原意图；关闭页面不是取消或回滚。
过期、版本/确认冲突、本地版本变化分别返回稳定的 `command.expired`、
`command.confirmation_conflict`、`command.stale`（409），不会自动换键。
原键查找只 GET、不准备/执行；返回 `data.intent` 和 `data.key_digest`（原键的 `v1:` SHA-256），不回显原键。
客户端核对 key digest、caller/target/kind，再保存原 ID；404 不证明旧在途 prepare 不会随后提交。
显式 abandon 只允许原 verified actor/issuer/subject/client/tenant 关闭自己的未执行预览，不借管理员权限。
关闭本身不需要重新获得生成/来源权限，因其不产生来源调用或回执；新预览/执行仍必须当前授权。
处置共享 directory → epoch → account → intent 锁序、当前 home-region writer/epoch fence 和三秒本地事务预算。
处置先赢则原意图为 `abandoned` v2，带数据库时钟 `abandoned_at`、无 receipt，旧 Execute 返回 `command.abandoned`（409）。
执行先赢则返回原 executed receipt，不冒称取消或回滚；已有/未知任务仍按原回执处理。
重复处置返回原结果，计划/键/原 TTL 和事实保留；审计失败回滚或报告 `command.persistence_unknown`（503）。
异地/过时 writer 返回 `command.disposition_fenced`（409），须正确路由或查询原意图，不能据此换键。
迁移 `000073` 的终态约束/触发器拒绝本地旧 writer 重开意图；down 不允许丢弃关闭证明，不授予 purge/Legal Hold 解除。
`authorization_version: unsupported`、`resource_authorization: unknown` 必须保留；本地源版本/Writer Epoch
不证明全局时点、源 ACL 或投影可见性。权限检查不是 Snaplink 与本地数据库的分布式事务，来源仍复查资源授权。
Operation 的后台/手动执行现在在领取前和每个正向、补偿、状态对账调用前复查持久发起者的当前 Snaplink
根命令及实际动作权限，使用三秒共享准入期限；不以当前管理员、账户 ownership、预览资格或本地 fallback 代替。
发送前还核对本地活租约、账户 disposition、目录与 writer epoch。拒绝/依赖故障保留原回执和已有结果，
事务记录 `operation.execution.blocked`；后台资格查询按本地一分钟间隔重新检查，不代表全局限流。
只在确定尚未调用 runner 时，领取的正向/补偿动作才可回到 pending；原 unknown 仍保持 unknown。
过期正向/补偿尝试以事务审计转为 unknown，不能重新发送，未知正向结果不授权开始回滚。
`operation.authorization_denied`（403）、`operation.authorization_unavailable`、
`operation.execution_lease_lost`、`operation.execution_defer_unknown`、`operation.execution_block_unknown`（503）
均需查询原 Operation，不能换键创建第二个命令。准入后发生撤权/租约/区域变化仍不能撤回已发送动作，来源必须重新授权。
Sync/Erase/Export/审计导出的延迟执行也已接入持久发起者的权威权限复查，不借用手动操作者或 ownership。
领取后的准入及每次来源调用、投影应用、内容分页、对象写入/恢复读取和最终制品发布前，核对当前权限与活租约/目录 fence；
审计导出同时要求 `audit.export` 和 `audit.read`，每页复查。每次准入共享三秒期限；账户/审计导出生成和原制品恢复各有五分钟整体期限。
本地检查仍不是来源 ACL 或分布式事务。
拒绝/故障以 `sync.execution.blocked` 事务审计暂停，保留原来源结果、summary replay、制品 key/digest/TTL。
只有尚未读取内容/调用来源、没有恢复记录的新执行领取才可回 pending；已开始及原 unknown/reconcile 保持 unknown。
后台 pending/unknown 查询按本地一分钟间隔重新检查，手动重查可覆盖间隔但不能覆盖当前授权/活租约；不是全局限流。
`sync.authorization_denied`（403）、`sync.authorization_unavailable`、`sync.execution_lease_lost`、
`sync.execution_defer_unknown`（503）要求查询原 job，不换键、重生成制品、延长 TTL 或将超时当作删除失败重发。
Web 详情仅将根级四个 `sync/operation.authorization_denied/unavailable` 精确码，在对应已知非终态且无完成时间时
显示为历史授权阻断警告，拒绝与授权/目标核验不可用分开说明；不解析任意 message 或提升嵌套 step/result 诊断。
Job 非终态 allow-list 为 pending/running/unknown；Operation 为 requested/running/waiting/compensating/unknown。
终态、未知状态/代码、scope/lease/erase/fence 及矛盾完成证据保持原错误展示，不转换成可恢复授权。
诊断可能在 worker 已继续后仍留存，不证明当前拒绝/允许、全局时点、远端未执行或任何回滚；已有状态、原键、回执和结果不变。
提示不触发请求或新命令，原 GET 刷新、显式动作资格和后台调度不变；继续执行仍复查原发起者，不借当前操作者的权限。
未知恢复端口缺失会停止，不以重新读取来源替代原 summary replay。未开始对象 Put、但已留检查点的结果也只恢复/处置原制品；
暂时 NotFound 不授权再次 Put。擦除/到期后的原制品处置不依赖重新获得导出生成权限，且不授权扩大删除范围或解除 Legal Hold。
Web Sync/Export/Erase、管理员账户任务及 Operation 创建已使用这些服务端预览接口，不调用旧直接创建 API。
所有来源的工作流步骤都须显式同源 scope，补偿也在预览中显示；破坏性确认输入精确目标 Account ID。

Operation 创建的正常路径默认使用既有 `operation_type: sync` 和 `action: summary`，先选择来源，再显式勾选同源数据集；初始/新增步骤均不预选，不能隐含全部。只使用当前目录中已批准的名称，展示业务标签及精确标识。勾选只更新原步骤 `request_payload.datasets`，顶层 `plan.datasets` 仍是所有步骤范围的排序并集；步骤顺序、稳定 `step_key`、其他载荷与补偿保留。移位不重写键，新增键避开已有 trim 后的键。切换来源只清空该步 datasets，不隐式选择新范围或清理高级参数；JSON/范围无法确认时选择器不自动修复，原 parser/服务端继续拒绝无效计划。

Step Key、Operation 类型/载荷、别名动作及补偿留在默认折叠的高级区，与选择器共用同一份计划。服务端返回的有序步骤在确认页直接展示来源、动作、明确数据集和补偿，规范化 JSON 退到折叠诊断。创建仍只调用原 CommandIntent 准备/执行/GET/处置契约，无新 HTTP 字段或来源能力；确认只提交原 version/digest，不覆盖载荷、不自动 Run。危险正向/补偿的精确 Account ID、当前动作/来源提示、原 caller/tenant/target/home/lease/fence 和服务端重新规划/事务审计均不变。受控 Iris/Chrome 演示不授予真实来源权限。

首次预览 POST 前保存 caller/部署隔离的原键，已知 ID 仅 GET 恢复；无 ID 可按原键只 GET 查找。浏览器只持久 UUID/hash/处置标记，不存载荷或历史凭据。
GET 恢复比对原 caller/target 和不可变元数据 hash（计划/有效期/前置条件），合法原回执才连接原任务；不自动 Run。
旧直接创建记录阻断新接口；旧服务端 Sync/Export 预览仅能以精确原范围/原键恢复。旧版仅存内存的 Operation/admin 未确认创建
不能在刷新后自动识别，版本切换前必须核实原列表/回执。
能力重查只 GET、可见时有界合并，查询中/故障/超过本地提示预算不沿用旧写入资格；保护覆盖检查回调返回到新 data/error 发布完成的交接，不在该间隙重新提供旧允许报告。当前根命令/正向/补偿权限分别提示。
未知/撤权暂停确认但保留原输入，授权错误触发重查而非自动重发。现有 Run/Reconcile/下载也逐动作 gating；菜单/ownership 不是许可。
审计下载在打开与最终确认时均要求当前 `audit.export` 和 `audit.read` 提示；后端仍独立复查，拒绝/未知不自动下载、消费或准备新导出。
一次性下载入口与最终确认还共用原详情读取成功、精确根级 `completed` 和当前读取者资格门禁；前台刷新中（含正文）、读取错误或已观察到其他状态后，保留的确认 listener 也不发送下载 GET，卸载后不准入。
拒绝不改原 ID/结果/恢复键；仅 GET 恢复 completed 元数据不自动下载，仍需显式确认。
此局部门禁不是当前 grant、全局时点或原制品可用性证明，不改变原 artifact/key/digest/size/TTL、服务端当前读取者复查及消费/审计事务；不能撤回已准入消费、保证重下、生成替代导出或续 TTL。
过期/冲突仍可 GET 核实原回执，不续期或换键；prepared 读取不证明旧在途执行不存在。已确认擦除的浏览器指针也保留，不能刷新创建第二个擦除。
Web 放弃预览需另输入精确原意图 ID；不自动放弃、不自动准备新意图。仅服务端关闭证明允许用户显式选择并准备新预览，
原键/ID 指针仍保留。刷新后的本地处置标记不能作为证明：生成新键前仅 GET 复核全部相关关闭记录（整体15秒）。
prepared/NotFound/故障/元数据变化均阻断新键；迟到回应不得重开关闭指针或替换原回执。
hold-aware retention/物理清理、其他治理动作、全量集成与真实权威/源/browser/跨区域验收仍未关闭。

## 管理端

管理入口由 Snaplink 权限控制，服务端仍会再次检查目标账户和任务权限：

```text
GET  /v1/admin/accounts?q=&status=&home_region=&limit=&cursor=
GET  /v1/admin/accounts/:id
GET  /v1/admin/accounts/:id/data
GET  /v1/admin/accounts/:id/overview
POST /v1/admin/accounts/:id/sync
POST /v1/admin/accounts/:id/export
POST /v1/admin/accounts/:id/erase
POST /v1/admin/accounts/backfill/legacy-identities
POST /v1/admin/migration/legacy/compare
GET  /v1/admin/migration/legacy/cutover/:tenant_id
POST /v1/admin/migration/legacy/cutover
GET  /v1/admin/migration/legacy/cleanup-readiness/:tenant_id
GET  /v1/admin/regions/directory/:account_id
POST /v1/admin/regions/promote
GET  /v1/audit/events
GET  /v1/audit/operations/:id
GET  /v1/audit/verify?source=&partition=
GET  /v1/admin/audit/holds
POST /v1/admin/audit/holds
DELETE /v1/admin/audit/holds/:scope_type/:scope_id
POST /v1/admin/audit/anchor
GET  /v1/admin/audit/anchor/manifest/:source/:partition
POST /v1/audit/export
POST /v1/audit/replays
POST /v1/audit/replays/projection
GET  /v1/audit/replays/:id
GET  /v1/admin/audit-log?action=&limit=&cursor=
GET  /v1/admin/roles
GET  /v1/admin/roles/:uid
POST /v1/admin/roles/grant
POST /v1/admin/roles/revoke
GET  /v1/admin/bans?limit=&cursor=
POST /v1/admin/bans
DELETE /v1/admin/bans/:uid
GET  /v1/admin/callback-subscriptions
GET  /v1/admin/callback-subscriptions/:id/deliveries?status=&limit=&cursor=
GET  /v1/admin/callback-subscriptions/:id/deliveries/:delivery_id
POST /v1/admin/callback-subscriptions
DELETE /v1/admin/callback-subscriptions/:id
POST /v1/admin/callback-subscriptions/deliveries/:id/replay
```

Legal Hold 列表成功回应必须包含根级 `data.holds` 对象数组；空数组有效。
缺失/非数组列表或任一非对象成员以固定 `response.invalid` 整份拒绝，不过滤坏行、补默认空页或回显被拒内容。
页面区分加载/读取失败与成功空查询，只有显式「重试」才 GET 原列表，不自动读取、写入或清换恢复键。
成功空查询仅表示本次无可见的生效 Hold，不证明不存在适用的 Hold，也不授权解除保全或清理。
这是列表基本形状检查，不是完整行字段/tenant/到期时间验证、当前授权、全局快照或 retention/Legal Hold 验收；
原服务端 `audit.read`、认证 tenant 筛选、设置/解除和事务审计均未改变。
原 `listHolds(signal?)` 接受可选 AbortSignal；页面将既有读取 hook 的 signal 传到原 GET，覆盖 fetch/正文，
卸载或原读取替换时取消该次读取，不通过 client-wide 取消干扰同 client 的其他读取。
保原15秒/2MiB预算、显式 GET 重试和迟到结果守卫，不自动补读/重试、写入或清换恢复键。
取消依赖协作，忽略取消的依赖仍可能结束，但旧结果不应用到替换/新挂载视图；这不是全局并发硬上限、
当前授权、撤回已准入访问、解除保全、命令取消/回滚或跨区/retention 验收。
Legal Hold 解除选择要求原支持的 `scope_type` 与非空字符串 `scope_id`；显示入口和原行 listener 共用此检查。
缺失、null、数值、布尔、数组、对象或空字符串不强转/借别名补位，也不打开解除确认；坏目标行仍只读展示，不丢弃同页合法行。
合法 opaque 字符串原样进入既有精确确认，不 trim/规范化或自动执行；拒绝不覆盖已有选择/确认输入。
此项只是本地目标类型准入，不是完整行校验、当前授权、Hold 版本 CAS、所选行未变化或保留策略批准。
Legal Hold 解除对话框和原 `runCommand` 处理函数还要求当前确认输入与所选 Scope ID 精确相等；
空值、错值或后来清空/改错时，在 client 调用和命令状态修改前拒绝，不 trim、强转或忽略大小写，也不只靠 disabled。
修正输入不自动执行，仍需显式确认；保原 scope/DELETE、单在途和错误后原列表刷新，不改变设置 Hold/审计导出的确认契约。
此本地人工意图检查不是当前授权、Hold 版本 CAS、保留策略批准或持久幂等，不增加后端确认字段/API。
原服务端权限/tenant 与事务审计不变；已准入 DELETE 不因输入变化或关闭对话框被撤回。
超时/丢回应可能已经解除，先 GET 核实原 scope 与审计，再决定是否另行人工操作，不自动重试或推断回滚。
解除确认 handler 还要求原列表非 loading、无读取 error；显式重读的 fetch/正文在途或读取失败后，
即使保留的旧 listener 有精确 Scope ID，也不能调用原 DELETE。拒绝不清原选择/输入，显式 GET 成功恢复列表
不会自动解除，仍需另一次人工确认。不扩展到设置 Hold/审计导出，也不改变原卸载/单在途或服务端权限/tenant/事务审计。
这是本地读取准入，不是当前授权、Hold 版本 CAS、所选行未变化或全局时点证明，也不能撤回已准入 DELETE。
合规页原准备/打开与最终确认处理函数还在卸载后拒绝新调用，早于意图键生成、命令状态修改或 client 调用；
新视图/会话不能为旧 listener 准入。共享守卫覆盖设置/解除 Hold 和创建审计导出，不改变各自确认、scope、单在途及服务端权限/事务审计。
这是本地生命周期准入，不是当前授权、持久幂等或跨刷新恢复；原内存键仍不保证刷新恢复。
卸载或取消读取不能取消/回滚已准入 DELETE/POST，迟到结果不应用到新页，也不自动重试、换键、解除或新建导出。
审计导出创建的实际 HTTP 成功为201，回应 `data.job_id` 为根级 UUID；OpenAPI 现明确201及必填 data/job_id。
合规页采用该新任务前要求根级 `job_id` 是非nil UUID 字符串，不借 `id`/另一 family/嵌套字段补位，也不强转或 trim。
新 ID 事先未知，不与请求键或某个旧任务 ID 比较；有效 ID 原样传给既有详情 GET，不跟随回应的 `download_url`。
坏 ID 保原内存意图/键及此前已采用的详情，不显示创建成功或读取坏目标；先核实原任务列表与审计。
没有自动 GET/重试/换键或再创建；之后若另行显式提交同一意图仍用原键，取消重开不丢该键，换页/刷新仍不保证恢复。
拒绝回应可能发生在任务已提交之后，不证明回滚或允许生成第二份导出；此页局部检查不是完整回执/状态/tenant/当前权限验证、
制品可用性或远端完成证明，不改变共享 client/helper、其他创建接口、服务端身份/授权/fence/事务审计或下载消费契约。

投递查询要求 verified caller + 当前 Snaplink `callback.manage`，并联合核对原 owner、tenant、订阅和投递 ID；
不是 tenant 管理员任意查阅所有订阅。每次读取与新的脱敏事实原子提交，审计失败返回503，不返回部分元数据。
只选择 ID、action、本地状态、attempt、revision、lease generation/expiry、HTTP状态、固定错误码及时间；不读投递 body、
远端回应、端点、ciphertext 或 worker 标识。status 过滤仅支持 pending/delivering/success/dead_letter，
降序 UUID keyset，默认20/max100（Web50），无隐含全量扫描/自动轮询。页面与 worker 可并发，非全局时点或完整历史。
HTTP no-store；gRPC `ListCallbackDeliveries`/`GetCallbackDelivery` 使用同一个服务与权限映射。
元数据新增正整数 `revision`（所有本地 UPDATE 递增，非因果水位）。新预览计划为
`{kind:"callback_replay",datasets:[],callback:{subscription_id,delivery_id,event_id,revision,lease_generation}}`，
仅绑定原发起者自己的 active canonical account、原订阅和投递；仍需当前直接 `callback.manage`。
确认时要求原版本/事件/claim generation、dead-letter 且没有任何 lease owner/timestamp，
原投递排队、generation 递增、fresh replay fact 和 `executed` intent 回执同事务提交。
`receipt_kind:"callback_replay",receipt_id:intent_id` 仅证明本地已排队，不能当作投递完成。
原 ID/原键 GET 恢复及显式 abandon 复用现有 CommandIntent 契约；不自动换键、取消已排队投递或撤回字节。
页面已切换此持久预览路径，不调用旧 replay API。用户必须输入原 Delivery ID 和 Callback Event ID 才可确认。
原 tuple/hash/身份/不可变元数据和排队回执严格绑定，原键 POST 准入前持久化 UUID/hash 指针；
刷新只 GET 原 ID/原键，不重 Prepare/Execute，不持久计划/事件/确认输入/token。排队回执指针保留，
输入精确原意图 ID 可仅 GET 核 executed 回执并显式允许另选人工意图（不是接收端完成/取消）。
后续新预览前逐条 GET 复核所有相关 abandoned/已核实回执，在整体15秒预算及当前动作资格下再准入；
本地标记、NotFound、prepared、过期/故障不放行换键，最多16指针含关闭/已核实记录，无自动淘汰。
原意图恢复不借订阅读取/写入资格，仍以服务端原 caller 契约授权。详见 [Callback recovery runbook](../runbooks/callback-deliveries.md)。

兼容 Callback 注册和旧 HTTP Delivery replay 必须为该次意图生成 `Idempotency-Key`；超时或响应丢失时，只显式复用原意图/键。
注册回执须与原端点、事件集合、tenant 一致，不能因错误/缺字段丢键或推断成功。安全幂等重放只返回订阅元数据，
不会再次返回一次性 Secret；提示 Secret 不可恢复，不因此新建订阅。当前 Web 选择已验证投递详情中的失败项，再单独确认；
不手工拼 Delivery ID，不自动 POST。当前动作拒绝/未知保留原意图而禁确认，迟到回应不能跨身份采用。
兼容 replay 只有 queued 回应，不代表远端完成，也没有版本 CAS 或持久请求指纹；通用响应缓存的 TTL/丢失可能再执行。
当前元数据/过期 lease/NotFound 不证明远端没有效果，未知结果先核实原行、审计与接收端，不能自动換键开始新意图。
兼容与新路径均保留原事件 ID/尝试次数（CLI requeue 的预算重置是另一契约）。新页面的持久预览/CAS 不升级旧 HTTP/gRPC replay 或迁移旧未知结果；动态管理、hold-aware retention、真实浏览器/接收端/滚动部署/WAN 验收仍开放。

审计查询返回脱敏 payload、链序号、hash/signature 版本、`request_id`、`trace_id`、`operation_id` 和因果 ID。
Forensic replay 不产生副作用；dry-run 只模拟；controlled replay 必须重新授权、使用新的幂等键并产生新的
审计事实。Projection replay 只接受 `snaplink | aero-im | aero-vault | aero-id`，服务端再次校验 `audit.replay`
权限和当前认证 Tenant 下的 Account；每个窗口限制为 1–10000 条脱敏事件并独立审计；同一窗口请求超时重试复用 `Idempotency-Key`，确认成功后下次显式读取（包括下一 cursor）使用新键。`completed=false` 时客户端只能
用服务端返回的 opaque `next_cursor` 手动读取下一窗口，不能自动全量扫描，也不能把单窗口 matched 当作全局一致；
该工具永远不写入或修复 live projection。

Legacy backfill 每批限制为 1–1000 个 UID，使用固定 `Idempotency-Key`，并在任何账户创建前记录独立的管理意图；
shadow compare 只接受 `aero-vault | aero-im`，最多读取 5000 条 content-free digest 输入，来源失败只返回稳定错误，
成功结果在离开服务前写入敏感读取审计；同一 compare 意图的超时重试复用 `Idempotency-Key`，明确成功后新的人工 compare 使用新键。Cutover 只允许服务端状态机定义的下一跳，写命令使用固定幂等键和 Tenant ID
二次确认；服务端以读取到的旧状态做事务内 CAS，并与审计事实原子提交，`409 cutover.concurrent_transition` 后必须重新读取，
不能拿旧决策自动重试。cleanup readiness 只报告删除前置条件，不执行 legacy 数据清理。

WORM Anchor 的 Web 按钮和原 `runAnchors` 处理函数都要求当前输入精确等于 `ANCHOR ALL CHAINS`；
空值、大小写/空白变体或后来改错的输入均在调用 client 前拒绝，不只依赖 disabled。拒绝不清原内存键、输入或错误，
修正输入不会自动执行，仍需显式人工确认；原单在途保护覆盖 fetch 和正文读取。原 `openRun/runAnchors` 处理函数还在更改意图状态、
生成键或调用 client 前拒绝卸载后的新调用，不能借新挂载面板/会话为旧 listener 准入；已准入 POST 不因卸载或取消读取被撤回，
迟到结果不会应用到新面板。此守卫/短语是本地准入，不是当前授权或持久业务幂等，
不升级原 HTTP 通用缓存、跨刷新恢复或其他治理动作。WORM Anchor POST 仍需要 `system.config` 权限，
扫描并校验全部当前审计链，且必须在一次确认流程中保持同一个 `Idempotency-Key`；服务端在写入任何不可变归档对象前先持久化
`audit.anchor.requested`，审计不可用时不产生归档。
后续单链失败会停止本轮执行，但之前写入的不可变锚点不会回滚；超时也不是远端失败证明，应先查询 Manifest。Manifest GET 只返回
指定 `(source, partition)` 的最新锚点，并在成功返回敏感证据前持久化 `audit.anchor.manifest_viewed`。
原 `queryManifest` 处理函数也在修改查询状态或调用 client 前拒绝卸载后的新 submit；新面板/会话不为旧 listener 准入。
原 GET/client promise 未结束（含响应正文读取）时，handler 还在输入校验/查询状态修改前直接忽略重复 submit，
不把后来输入排队；成功或报错后均需另一次显式读取。保原 source/partition trim 与路径编码、30秒/2MiB传输预算及错误展示，
不续原请求期限、不自动查询/重试或执行 Anchor。这仅是当前面板内的单在途保护，不是跨页面/会话或全局锁；
取消依赖协作，deadline 信号触发本身不会释放尚未结束的 client 调用。
挂载/单在途守卫不是当前 `audit.read` 授权、证据完整性或全局时点；不能撤回已准入读取/已持久化访问事实。
原 client 的读取取消仍依赖协作，忽略取消的旧读取可迟到结束，但结果不应用到新面板。
Manifest GET 的根级 `source` 和 `partition` 必须是与本次原请求精确相等的字符串，采用前由 client 检查；
不以后来编辑的输入、别名或嵌套字段补位，不强转、trim 或改写回应。缺失、坏类型或错目标整份固定
`response.invalid` 拒绝，不展示被拒证据、回显目标或自动查询/重试；需要另一次显式 GET 核实原目标。
原30秒/2MiB、单在途和当前服务端 `audit.read`/返回前访问审计均不变，拒绝回应不撤回已经持久化的访问事实。
OpenAPI `AnchorManifestEnvelope` 只明确实际 data 对象及两个必填根级字符串，其他归档字段仍保留；
该关联检查不是完整文档/hash/signature 校验、当前 grant、全局时点、归档完整性或 WORM/真实存储验收，不授权补锚或修复。

`/admin/audit-log`、`/admin/roles*` 和 `/admin/bans*` 是当前 OpenAPI 与 live handler 已发布的 legacy 管理面，
每个请求仍由服务端按权限校验；角色/封禁变更还会写管理审计事实。角色授予、撤销和封禁 POST 使用通用持久化
幂等中间件与 `Idempotency-Key`，控制台仍要求二次确认并在超时后复用同一意图和键；解封当前是 DELETE，未接入
通用幂等中间件，超时后必须先重新读取状态，不自动重试。

## 分页、区域和安全约束

- 高基数列表使用 keyset `cursor`，不要使用 offset；客户端应原样保存并回传 `next_cursor`。
- 原 Job/Operation 列表回应分别要求根级 `jobs`/`operations` 为对象数组，存在的 `next_cursor` 为字符串；空数组、缺失/空字符串 cursor 仍有效。Web 在首屏和后续页都检查，不用 `items`、另一 family 或嵌套列表补位；坏成员不静默过滤，坏 cursor 不冒充末页，固定 `response.invalid` 拒绝整页且不回显坏内容。首屏失败不是成功空列表，后续坏页保已查看行与原 cursor，显式重试只 GET 原列表/原 cursor，不自动请求、清恢复键或发命令。OpenAPI 两个列表 envelope 明确必填 data/对应列表；这是基本形状校验，不是完整行/ID/状态校验、当前 grant/租户证明、全局快照或远端完成证明。
- Web Job/Operation 列表后续页将表格的取消信号传到原 GET（含 fetch 和响应正文）；卸载或列表替换时取消，迟到结果不并入替换后的列表。保持原 cursor/limit50 与15秒/2MiB传输预算；错误保留已查看行和原 cursor，显式重试只 GET，不自动补读/重试或清恢复键、发命令。取消依赖协作，不是全局并发硬上限、当前授权、任务取消或效果回滚证明；服务端当前授权和租户隔离不变。
- `home_region` 是账户写入路由依据；读取可来自本地区域的版本化投影，并展示来源区域和新鲜度。
- Region 提升必须使用刚读取的 `fenced_epoch` 做 CAS、携带固定 `Idempotency-Key` 并二次确认
  Account ID；成功响应的 `X-Aero-Write-Epoch` 指向目标 Writer，冲突后必须重新读取而不是自动重试旧
  Epoch。
- Snaplink 决定全局权限，IM/Vault 仍决定其资源 ACL；缓存 miss 或授权不确定不能当作允许。
- 客户端不得显示或记录 bearer token、callback secret、源凭据、对象字节、消息正文或 MFA/恢复材料。

回调请求带有 `X-Aero-Callback-Schema-Version`、`X-Aero-Callback-Timestamp` 和
`X-Aero-Callback-Signature: sha256=<hex>`。签名输入为
`<timestamp> + "\\n" + 原始请求体`；客户端应先校验时间窗口，再使用幂等的 `event_id` 处理事实。
