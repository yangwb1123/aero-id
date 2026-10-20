# Aero Platform Console

`aero-id/web` 下基于 Iris UI Vue 适配器的独立平台控制台。以下功能已接入代码；真实登录、源部署和浏览器 E2E 验收另行记录，不以页面存在或单元测试通过代替：

- 通过 Snaplink Authorization Code + PKCE（S256）登录；
- 在浏览器内存中持有短期 access token，不写入 localStorage/sessionStorage；
- 退出时调用 discovery 声明的 `end_session_endpoint`，销毁 Snaplink 会话后回跳；
- 调用 aero-id `/v1/me`、Profile、来源、成员关系、allow-list 数据浏览、脱敏资源摘要、活动、同步/导出任务和 Operation API；
- 支持任务详情、unknown 结果对账，以及带 Bearer 且确认后销毁制品的一次性导出下载；
- Operation 页面使用显式同源数据集勾选、有序有界 Saga 步骤和可读服务端预览；正向/补偿危险动作按目标 Account ID 确认后才创建原回执，再按 Operation ID 手动运行或对账；管理员账户支持 `/data` 数据查看；
- 展示 Operation/Saga 步骤、来源 operation ID、补偿状态与追加式脱敏审计时间线；
- 管理员可按关键词、状态和 home region 查询账户，并检查来源与投影的新鲜度；
- 普通账户擦除和管理员账户同步/导出/擦除均先显式选择数据集、获取服务端计划，再确认创建原任务；不隐含全部来源，破坏性计划要求输入精确目标 Account ID；
- 账户详情展示 durable Region Directory，并通过观察到的 Fenced Epoch、Account ID 二次确认执行 Home Region Writer CAS 提升；
- Legacy 迁移控制面支持有界 shadow identity backfill、content-free digest 对比、cleanup readiness 和经 Tenant ID 二次确认的状态机 Cutover；
- 审计台账支持过滤、keyset 分页、脱敏证据详情和来源分区哈希链验证；
- 管理员治理覆盖 `/admin/audit-log`、`/admin/roles*`、`/admin/bans*`，角色授予/撤销与封禁使用幂等键并二次确认；解封 DELETE 超时后先刷新状态，不自动重试；
- 审计合规管理支持 Legal Hold 查询、设置、经 Scope ID 二次确认的解除，以及带一次性下载保护的审计导出；
- WORM Anchor 操作会使用固定幂等键、全链二次确认和 fail-closed 管理意图审计；单链 Manifest 敏感读取也会留存审计事实；
- Callback 治理支持 challenge 注册、一次性 Secret、订阅删除，以及按订阅/状态分页读取投递元数据和详情；重放改用持久 `callback_replay` 预览、原 Delivery/Event ID 二次确认、版本/lease CAS 排队和跨刷新原意图恢复；不手工拼任意 Delivery ID，不展示投递载荷/远端回应；
- Replay 管理严格区分 forensic、dry-run、controlled，并为真实命令强制新幂等键和 Account ID 二次确认；支持按 Replay ID 恢复查看持久化详情；另提供按 Tenant/Account 鉴权、逐窗口审计且绝不自动修复的 Shadow Projection Replay；
- 明确展示 partial、stale、unavailable 和 unknown 状态；
- 使用独立链接进入 Audit Governance、Aero IM 和 Aero Vault 控制台，不跨 audience 转发 token。

## 资源摘要：先看业务，再看诊断

资源摘要、账户概览和按范围数据浏览共用同一组批准字段视图：Vault **租户级**已报告字节/对象用量、配额原值、桶名称引用，以及 IM 工作空间名称、ID、slug。账户概览先呈现业务摘要和账户/租户/主区域，技术计数与来源新鲜度可折叠；计数只是本次回应可见条数。每块保留来源、区域、版本、生成/同步时间，陈旧和不完整警告不会折叠。缺失字段显示「来源未提供」，不补零；IM `not_found` 表示尚无关联来源账户，不当作工作空间为空。

- 「刷新资源摘要」只重读 `/account-datasets` 与原 `/me/resources?consistency=eventual`；「刷新账户概览」只重读原 `/me`、目录和 `/me/overview`。两者不提交同步任务；需要任务时，进入「选择范围同步 / 导出」显式选择数据集并走原预览/确认。
- 概览「按范围浏览」与数据页「返回账户概览」互通。数据浏览不自动查询：选定数据集后点击「读取账户数据」，只为本次提交范围展示摘要；未选数据集不冒充待同步或空结果。保留原 GET 留空即全部已注册数据集的兼容语义，但读取前后明确标注；这不是同步/导出的默认范围。改变条件清除展示并取消旧 GET，不自动重读；原迟到守卫仍生效。Bounded / Strong 可能刷新来源，不是全局事务或同一时点。
- 其余已注册数据集目前只列投影元数据，明确「暂未提供业务字段视图」，不再通用展开 `data` JSON；不把未提供视图说成数据为空。管理员目标的数据浏览不借当前调用者自己的数据集资格判断另一个目标授权；服务端仍验证目标范围与当前权限。
- 只映射已确认字段，不展开任意 source JSON。数值只显示非负安全整数，字节以 B 展示，不推测占比、剩余额度或零配额的无限制语义。引用最多显示/验证前50条；来源截断、页面截断及空投影分开说明。当前 HTTP 聚合没有来源 `coverage/complete` 证明，可见条数不是全量或全局总数。
- 未接通、待同步、读取拒绝/资格未知、故障、陈旧、有效空引用分别呈现。连接启用、本地读取资格和来源资源 ACL 不互相推导；目录与额外技术边界移至可折叠区。只读摘要不创建 IM 账户，不操作 Vault 对象或复制 ACL。

## 来源与成员：选择原引用，读懂原范围

从账户概览「来源与成员关系」进入原入口，先核当前账户/会话租户/主区域，再在来源或成员列表点击「查看来源范围 / 查看成员范围」。直接查看该行的来源账户/成员 ID、原 scope 类型/ID、来源角色、记录状态及版本/同步时间，返回时保已查看页和原 cursor；不另查来源、不创建命令。两张列表独立，来源列表故障不遮住成员结果，刷新/重试仅 GET 对应原列表。手机端保局部键盘横向滚动和固定查看入口，详情/返回转移焦点。

同一 ID 文本的 Vault 租户、IM 工作空间或桶不是同一范围，更不等于当前会话租户；owner/active 不是全局权限、来源 ACL 或当前健康。缺失不补零/借别名或嵌套 data 补位，业务详情不展开任意 JSON。列表没有 coverage/complete、总数或新鲜度预算，空页不证明来源不存在账户/成员，旧版本/同步时间不承诺当前新鲜。当前 self-read 提示未确认不继续查询/展示详情；后端仍核认证身份与租户。可返回概览或看当前资源，更新仍需显式选择范围并走原预览/确认。

### 从来源引用选择本地账户投影

在来源账户详情点击「选择此来源的数据集」，目录只列该来源且当前本地读取资格允许的数据集。手动选择至少一项，再点「读取账户数据」；留空不会请求或默认全部，选择本身也不读取。此入口固定 Eventual，不刷新来源；资格拒绝/未知不借用成员角色或换读其他来源，未启用连接仍可能存在可读历史投影。

这是当前账户的数据集范围，**不是按原生租户、工作空间或桶 ID 过滤**，也不是所选原生资源的详情/ACL。原生 ID 仅用于返回上下文，不进入读取参数；只有原 `GET /me/data?dataset=...&consistency=eventual`，不新建命令。摘要保留部分/陈旧/不可用与已批准字段；其他数据集仍只显示元数据。目录或读取失败只显式重读，不自动换范围。返回原来源范围再返回列表，不重读列表；返回/换当前账户范围或读取资格变化取消旧 GET，迟到内容不进入新范围，不把取消当回滚。选择/内容只在内存中，刷新不恢复或自动读取。普通数据浏览及管理员目标的原空选择/一致性契约不变。

## 任务结果：范围、来源与原制品

原任务列表进入详情（或 `#/jobs/<UUID>`）后，可直接看到中文状态、原数据集范围、逐来源摘要/失败/缺失结果，以及对账投影计数；没有来源结果不表示未执行，尝试次数不是百分比。账户导出分别列出投影、来源引用、成员关系引用计数、缺失数据集、覆盖/遍历与投影可用性；即使任务 `completed`，范围不完整也显著警告。审计导出显示本次窗口事实数和截断，不冒充完整历史。原请求/结果 JSON 降为默认折叠的脱敏诊断。

制品生成/到期时间与下载条件在正文和确认框可见。已知到期、已消费/清理、元数据错目标/坏范围暂停下载；未提供计数不补零，坏类型不强转。历史任务缺有效期仅提示不能确认可用，仍可显式请求原端点，由服务端核原 TTL/指针及当前权限。页面本地时钟不是服务端时钟或 grant；确认时再次检查本地已知期限，不续期、不生成替代制品。收到一次性响应后本页禁再次消费，提示检查下载记录，不宣称文件已经写入磁盘；此内存标记不是跨刷新保证，服务端消费边界不变。

可从结果进入当前资源摘要，或点击「按原 Request ID 核对审计」预填原筛选，无需手抄；当前资源页不是原导出时点。手机端列表/技术表保留可读列宽和局部横向滚动。受控 Chrome 验收包含列表→结果、原端点确认下载并核对合成制品字节、已知到期/处置/unknown/失败/截断/拒绝、GET 恢复及审计筛选；不替代真实来源、权限和制品服务验收。

## Operation：直接读懂步骤、补偿与下一步

从原操作列表「查看时间线」或 `#/operations/<UUID>` 进入详情，先看中文状态、目标账户/租户/主区域，再按返回顺序读来源动作、正向结果/尝试/来源操作 ID、摘要投影数/完整性/应用结果与跳过范围。补偿单列原动作、状态、次数和来源操作 ID，不借正向结果补位；未知不当失败，补偿成功不等于整体回滚。技术表和原 JSON 默认折叠，必要危险/不完整警告仍可见，窄屏表格可键盘横向滚动。

步骤区是最新记录，不是尝试历史；HTTP 未提供逐步骤时间，实际先后查看下方本地审计事实。事实保留 action 码并增加步骤/状态/正向或补偿次数/已报告范围的中文说明；缺失不补零、不推断未执行。原刷新/返回/动作入口位于长步骤列表之前；可显式 GET 刷新原状态、返回原列表，或分别按原 Operation ID / Request ID 链接预填台账筛选，不自动运行、对账或补偿。

每步现在显示 HTTP `dataset_scope` 的明确数据集名称与业务含义；该元数据只投影原已存储、非空、唯一且同源的批准名称（最多11个），不返回任意逐步 `request_payload`。正向与补偿沿用同一步持久范围；缺失/坏范围分别说明，不扩展兼容默认值、不借顶层请求、Protobuf 或历史审计补位。具备原范围、当前动作/补偿提示与主区域条件时，用户可显式 Run 原回执或对账 unknown，不再因 HTTP 漏字段而永久禁用；原状态/读取/挂载门禁、原键、危险精确确认及服务端原发起者/tenant/home/lease/fence/来源 ACL 复查不变。页面说明暂停的实际原因，如来源摘要未启用、当前动作/补偿拒绝、主区域不符或范围未确认；提示不是 grant。受控 Chrome 验证正常操作和拒绝路径，不等于真实登录/来源运行闭环已验收。

### 创建 Operation：选择范围而非先写 JSON

在原「创建 Operation / 核实原意图」中，默认动作是同步来源摘要并更新本地投影。每步先选来源、再勾选明确数据集；初始和新增步骤都不预选范围。业务名称旁保留精确 dataset 标识，不隐含全部、不把注册当启用/授权。可以添加、移位或移除步骤，存活步骤的键和载荷不因移位改写；首次生成的键不与已有规范化键重复。

Step Key、其他类型/动作、补偿及原 JSON 保留在默认折叠的高级区。切换来源只清空本步 datasets，其他高级参数不变；坏 JSON/未知或跨源范围不被选择器自动修复。高级编辑与勾选使用同一份原载荷，不维护两份会漂移的计划。服务端预览按原顺序直接列出来源、动作、数据集业务名称/标识与独立补偿，规范化 JSON 折叠保留。

确认仍执行原 CommandIntent 的 version/digest，只创建原回执，不自动 Run；危险补偿仍需精确 Account ID。当前动作/来源拒绝或未知暂停预览/确认。丢准备回应和丢执行回应的刷新恢复分别只 GET 原键/原 ID，不重新准备、换键、续期或补发 Execute。来源选择器留在原对话框内，避免发布版 Iris 默认 portal 被 modal 遮挡；没有升级组件库或绕过焦点/确认。

### 从原结果核对审计并返回

Job 结果只用已报告的原 Request ID；Operation 另提供原 Operation ID 的独立入口，不默认把两者交集查询。缺少/无效 Request ID 时明确不提供预填，不以 Job ID、来源操作 ID 或审计事件 ID 补位。Request ID 只关联该请求，不代表后台后续尝试的全部历史。

链接进入台账后先展示原任务上下文与可编辑筛选，点击「查询」才 GET 原 `/audit/events`；复制/刷新只恢复初始筛选，不自动查询。分页和错误重试沿用本次已提交条件/原 cursor，不借后来编辑的草稿；切换筛选上下文/返回取消旧 GET 并拒绝迟到结果。「重置」只清空条件与结果，不自动读取全部台账。拒绝/故障时仍可看到筛选和「返回原任务/操作结果」，返回原 UUID 详情只 GET，不重建或运行。

Hash 链接仅接受明确的一种筛选、有界原标识和 `jobs/operations` + 非nil UUID 返回指针，不接受任意 return URL、tenant 或载荷。当前 `audit.read` 提示未确认时暂停查询，原 shell 和服务端仍各自核当前授权/租户；链接不是 grant。结果/空查询不证明远端效果或完整历史，不执行 replay。390px键盘操作及实际 Iris/Chrome 受控 API 验收与真实 OIDC/来源验收分别记录。

## 范围、重试与任务跟踪

- Legal Hold 列表要求根级 `holds` 为对象数组；缺失/非数组或任一坏成员以固定 `response.invalid` 整份拒绝，不静默过滤或冒充空页，不回显被拒内容。页面读取错误只支持显式 GET 重试，不自动读写或清换恢复键；有效空数组仅表示本次无可见的生效 Hold，不证明不存在适用 Hold，也不授权解除保全或清理。此项不是完整行字段/租户/到期时间校验、当前权限、全局快照或 retention/Legal Hold 验收；原权限、范围、设置/解除及事务审计不变。
- Legal Hold 列表将既有读取 hook 的 signal 经 `listHolds(signal?)` 传到原 GET，卸载/原读取替换时取消 fetch/响应正文，不用 client-wide 取消影响同 client 的其他读取。原15秒/2MiB、迟到结果守卫和显式 GET 重试保持，不自动补读/重试、发命令或清换恢复键。取消依赖协作，忽略取消的依赖仍可能结束，但旧结果不应用到替换/新挂载视图；这不是全局并发硬上限、当前权限、撤回已准入访问、解除保全或回滚，也不关闭跨区/retention 验收。
- Legal Hold 解除选择仅接受原支持的 `scope_type` 与非空字符串 `scope_id`，显示入口和原行 listener 共用目标检查；缺失、null、数值、布尔、数组、对象或空字符串不强转/补位，也不打开解除确认。坏目标行仍可只读展示，不丢弃同页合法行；合法 opaque 字符串原样进入既有精确确认，不 trim/规范化或自动执行。这只是本地目标类型准入，不是完整行、当前权限、Hold 版本 CAS 或保留策略批准。
- Legal Hold 解除对话框和原命令处理函数都要求当前输入与所选 Scope ID 精确相等，不 trim、强转或忽略大小写；空值、错值、后来清空/改错的输入在 client 调用和命令状态修改前拒绝，不只依赖 disabled。修正输入不会自动执行，仍需显式确认；保原 scope/DELETE、单在途和错误后的原列表刷新，不为设置 Hold 或审计导出添加解除短语要求。这是本地人工意图检查，不是当前权限、Hold 版本 CAS 或保留策略批准，不能撤回已准入 DELETE。超时/丢回应先核原 scope/审计再决定是否另行人工操作，不自动重试或推断回滚；原服务端权限/tenant 与事务审计不变。
- Legal Hold 解除确认处理函数还要求原列表非 loading、无读取 error；显式重读的 fetch/正文在途或读取失败后，即使旧 listener 保留精确 Scope ID，也不能调用原 DELETE。拒绝保原选择/确认输入；显式 GET 成功恢复列表不会自动解除，仍需另一次人工确认。不改设置 Hold/审计导出、本来就有的卸载/单在途守卫或服务端权限/tenant/事务审计。这只是本地读取准入，不是当前权限、Hold 版本 CAS、所选行未变化或全局时点证明，不能撤回已准入 DELETE，也不授权 retention 清理。
- 合规页原准备/打开和最终确认处理函数在卸载后拒绝新调用，早于意图键生成、命令状态修改或 client 调用；新页面/会话不能为旧 listener 准入。共享守卫覆盖设置/解除 Hold 与创建审计导出，不改各自确认/scope、单在途及服务端权限/事务审计。它只是本地生命周期准入，不是当前授权、持久幂等或跨刷新恢复，原内存键不保证刷新恢复。卸载/取消读取不能撤回已准入 DELETE/POST，迟到结果不应用到新页，不自动重试、换键、解除或新建导出；丢回应先核原 scope/任务列表与审计，不推断回滚。
- 合规页创建审计导出只采用根级非nil UUID 字符串 `job_id`，不借 `id`/另一 family/嵌套字段补位，也不强转或 trim。新 ID 事先未知，不与请求键/旧任务 ID 比较；有效 ID 原样交原详情 GET，不跟随回应的 `download_url`。坏 ID 保原内存意图/键及此前已采用详情，不显示创建成功或读取坏目标；先核原任务列表与审计，取消重开后的另一次显式同意图提交仍用原键，不自动 GET/重试/换键/新建导出。拒绝可能发生在已提交之后，不证明回滚；此页局部检查不是完整回执/状态/tenant/当前权限验证或制品可用性，原内存键仍不保证刷新恢复。实际 HTTP 创建状态201及必填 data/job_id 已在 OpenAPI 明确，没有更改后端行为、共享 client/helper、其他创建或下载契约。
- 同步/导出必须显式勾选数据集；已注册不代表源已启用、获授权或新鲜。导出包含核心账户/资料和所选来源引用，不包含 Vault 文件或完整 IM 消息；任务结果仅显示元数据，内容留在加密 TTL 制品。
- 同步/导出/擦除、管理员账户任务和 Operation 创建均调用 `/command-intents` 获取五分钟服务端预览，显示目标、显式范围、有序步骤/补偿、脱敏规范化载荷、本地版本和到期时间。输入确认后才调用原意图的 execute 创建既有回执，不自动 Run。当前能力未知/撤销时禁确认但保留原意图与输入；关闭页面不回滚。
- 首次 POST 前保存原键；丢回应可按原键仅 GET 查找预览 ID，校验 key digest，不需重发 POST。NotFound 不证明旧 prepare 不会提交，不能改范围或换键。已知 ID 的恢复只调用 GET；验证原身份、目标、不可变元数据 hash 与回执引擎后才接入详情。浏览器记录按部署、issuer、client、tenant、account 隔离（最多 16 项），仅保存 UUID 指针/hash/处置标记，不保存计划载荷、确认输入、token 或导出内容。存储被禁用则仅保留内存，不能保证刷新后恢复；损坏记录不作为“无原意图”放行。
- 原列表的「查看详情 / 查看时间线」与合法创建/恢复回执会更新到 `#/jobs/<UUID>`、`#/operations/<UUID>`；可复制原链接、刷新恢复同一回执，并用浏览器前进/后退切换。选中详情置于长列表之前并转移焦点，不必再越过创建表单找结果；返回/关闭同步回到列表 URL，刷新不会重新打开已关闭详情。原有 `#jobs`、`#/jobs` 等链接继续有效。
- 列表/目录 GET 与原详情读取独立：列表失败不遮住可读的原结果；原详情拒绝/NotFound 时仍有原链接和返回入口，只能显式 GET 核实。导航不重 Prepare/Execute/Run/对账或下载，不清原键/指针，不把关闭当取消/回滚。URL 仅含 family/UUID，不含计划、确认输入、token 或命令键；链接不是权限凭据。新目标先经原 shell 当前准入，再由原 ID GET 及服务端授权核验，不借已准入旧详情续权；切换目标取消旧 GET，迟到结果不进入新详情。
- Job/Operation 列表首屏与后续页要求根级 `jobs`/`operations` 为对象数组，存在的 `next_cursor` 为字符串；空数组及缺失/空字符串 cursor 仍有效。缺失/非数组列表、坏成员或坏 cursor 以固定 `response.invalid` 整页拒绝，不静默过滤或冒充空页/末页，也不回显坏内容。首屏失败可显式重读；后续坏页保已查看行和原 cursor，只显式 GET 重试，不自动请求、发命令或清恢复键。OpenAPI 明确必填 data/对应列表；这是基本形状检查，不是完整行/ID/状态校验、当前权限/租户、全局快照或远端完成证明。
- Job/Operation 列表后续页把表格的取消信号传到原 GET，覆盖 fetch 和响应正文；卸载或列表替换时取消，迟到结果不并入新表格。保原 cursor/每页50项及15秒/2MiB请求预算；错误保已查看行和原 cursor，只在显式「重试」时 GET，不自动补读/重试、发命令或清恢复键。取消依赖协作，不是全局并发硬上限、任务取消/回滚、当前 grant 或远端完成证明。
- 现有16入口不变，仅 Operation 页面代码按需加载：首次通过原 shell 准入后才开始 import，显示独立「正在加载页面代码」，不冒充任务/审计 GET。拒绝或15秒未完成显示固定「页面代码加载失败」，不输出原始 import 诊断/URL，不自动重试或丢原意图指针；显式刷新页面后仍按原 ID/键恢复，不重发命令。15秒只是显示期限，不能取消浏览器 import；迟到代码只接入当时仍准入的 route/client/会话，换页、当前明确拒绝、退出或身份 remount 不采用旧页面。已准入精确页面在能力重查中仍可保留，但注入报告为不可用，不能借代码缓存或旧允许资格执行动作。发布须单独验证 index/chunk 同代及旧会话所需静态资源可达，本地 bundle/组件测试不是浏览器性能或部署验收。
- 详情自动刷新只调用 metadata GET，等待上一自动请求完成，逐步退避，最多 60 次/5 分钟；隐藏标签页、前台详情刷新（包括正文读取）、操作进行中或读取错误时暂停，终态/unknown 停止。新的自动 GET 不取消手动读取或掩盖其错误；合法 active 回应采用后按原间隔恢复，不立即补发，也不重置轮询预算/GET期限。显式刷新仍可取消已开始的后台读取，保原取消/迟到回应守卫；协作取消不是全局并发硬上限，此协调不覆盖独立审计 GET 或授予命令权限。unknown 不是失败，对账不会盲目重发原命令。
- Job/Operation 详情的初次读取、显式刷新和原 GET 轮询都要求响应根级 `job_id`/`operation_id` 为与原请求完全一致的字符串，根级 `status` 为下述对应 family 的精确字符串。缺失、错目标、错误类型、未知或跨 family 状态拒绝整份回应，不用别名/嵌套字段补位。固定 `response.invalid` 不展示被拒内容、不清原恢复键、不自动重发命令；读取错误暂停自动 GET，显式重试仍 GET 原 ID，取消/卸载守卫保持。这不是当前权限/租户、完整元数据/状态转移校验或远端完成证明。
- 详情页采用 Run/Reconcile 回应前按原路由 family 核对原详情和回执的精确根级 ID（Job 用 `job_id`，Operation 用 `operation_id`），保留原 account/tenant/actor/type 比较；`id` 别名、另一 family、嵌套或强转不补位。回执根级 `status` 必须是对应 OpenAPI/domain family enum 的精确字符串；数组等非字符串、跨 family、未知或带空格状态不转换或借嵌套状态补位，两种任务目前均无 `canceled`。GET 与动作回执共享根级状态 allow-list，不扩展到步骤状态或状态转移/完成时间一致性校验。拒绝时固定错误、不展示坏内容、不替换原状态/结果、不发成功通知或清对账键，不自动 GET/重试/换键。先 GET 原回执，再决定是否以原 ID/键显式人工动作；坏回应不证明回滚或远端未执行。本地采用检查不升级旧 HTTP 缓存为持久业务幂等，也不是完整 DTO/当前授权证明。
- Operation 脱敏审计时间线区分首屏加载、读取失败与成功空查询；拒绝、依赖/网络故障、无效 JSON、陈旧读取不再显示为「暂无审计事实」。只有显式点「重试」才 GET 原 Operation 的审计首屏，不刷新或运行任务；成功空查询只表示本次无可见事实，不证明远端未执行或完整历史为空。后续页错误保留已查看事实并以原 cursor 显式重试；原每页50项、deadline/body预算和取消/卸载迟到守卫不变。服务端仍独立检查当前读取权限、tenant 并记录读取审计；不新增权限、自动 Run/对账、换键或审计修复。
- 每页 Operation 审计 GET 还核对根级 `operation_id` 精确匹配原 ID、`events` 为对象数组、存在的 `next_cursor` 为字符串；错目标/缺失/坏类型拒绝整页，不静默过滤坏项或冒充空列表。固定 `response.invalid` 不回显被拒内容，后续页失败不合并事实或消费原 cursor，显式重试仍只 GET 原 ID/cursor。OpenAPI 已使用实际 `OperationAuditEnvelope`，不是任务步骤/尝试；此检查不是完整字段/签名验证、当前授权或完整历史证明。
- Operation Run 的入口和最终确认同时检查当前动作资格、原详情读取成功、精确 `requested`/`waiting` 状态及无完成时间（`completed_at` 缺失/null）。前台刷新中、GET 错目标/失败/拒绝/陈旧，或已观察到其他状态/完成证据时，已打开并填好短语的危险确认也暂停，显示固定提示；不因旧详情仍在内存就放行。提示不重写输入、原 ID/键或结果，显式关闭后再 GET 原详情；不新增请求、自动 Run/对账、换键、取消或回滚。普通 Run 入口与危险最终确认的处理函数还拒绝卸载后的新调用，不能借同 ID 新视图/client 准入旧 listener；不只靠 DOM 移除。保原 Operation ID/稳定 Run 键、当前资格及读取/状态门禁，不自动运行、重试或换键。已准入 POST 不因卸载被取消，迟到回应不采用；不是持久业务幂等升级或回滚。原轮询和服务端当前授权/租约/fence 仍独立；最后采用的本地状态不是全局时点，没有完成时间也非远端未执行证明，无法撤回已准入动作。
- Job/Operation 对账入口和事件处理函数同时要求原详情读取成功、精确根级 `unknown` 及原当前动作资格；前台刷新中（含正文）、读取错误或已观察到其他状态后，旧 listener 不能再发送 POST，卸载后也不准入。不只依赖按钮隐藏/disabled；在生成或持久化键前拒绝，保原 ID/键/结果。仅 GET 恢复 unknown 不会自动对账，仍需显式人工动作；在途/丢回应仍保原键，不自动重试。本地 UI 守卫不是原发起者 grant、全局状态或远端结果证明，不改变原服务端授权/tenant/account/home/lease/fence/审计，也不能撤回已准入效果。
- Job/Operation 详情仅对根级 `sync.authorization_denied/unavailable`、`operation.authorization_denied/unavailable` 显示固定文案的「继续执行授权阻断记录」警告：Job 限 pending/running/unknown，Operation 限 requested/running/waiting/compensating/unknown，且不得有完成时间。拒绝与授权/目标核验不可用分别说明；终态、未知类型、scope/lease/erase/fence 诊断保留原错误展示，不归类为可恢复授权。记录可能在 worker 已继续后仍存在，不代表当前授权、任务失败、远端未执行或回滚。保原状态/回执/键，先 GET 刷新并核审计；操作者能力不替代原发起者的当前授权及本地 lease/fence。提示本身不新增请求、自动 Run/对账、换键、重写制品或续 TTL，也不改变既有后台调度和按钮资格。
- 身份/租户会话变更会重新挂载页面、清除旧上下文/writer pin 并取消未完成读取；取消读取或关闭页面不是取消/回滚已接受的命令。租户不能仅靠修改请求 `tenant_id` 切换。
- Header 使用已验证 `/me` 的账户/租户，不使用占位身份。`/me/capabilities` 报告验证客户端上下文；菜单和深链接区分未配置、无权限与未知，未知不作为允许。源新鲜度面板分别统计注册、启用、本地读取资格及新鲜度；不探测源健康或推断源 ACL。
- 账户任务/Operation 的创建、确认、Run、Reconcile 和下载使用当前动作资格，Operation 同时检查根命令、正向及补偿；ownership 和菜单不借出 worker grant。账户下载需要当前 `account.export`，审计下载打开及最终确认都需要 `audit.export` **和** `audit.read`。服务端在对象读取前/后直接复查当前 verified 读取者，原发起者的生成授权不能替代读取许可；故障/撤权只触发能力 GET，不自动下载或准备新导出。一次性消费和读取审计先于响应提交，丢回应/删除不确定可能无法重下。无注入报告/账户上下文不放行；未启用摘要源不能新同步，但可显式导出历史本地投影。摘要启用不是擦除启用，逐来源擦除与资源 ACL 仍由服务端核对。
- 一次性下载入口和最终确认共用原详情读取成功、精确根级 `completed` 及当前读取者资格门禁；前台 metadata 刷新中（含正文）、读取错误或已观察到其他状态后，旧确认 listener 也不能发送下载 GET，卸载后不准入。拒绝不改原 ID/结果/恢复键；GET-only 恢复 completed 不自动下载，仍需显式确认。这不是当前 grant、全局状态或制品可用性证明，不改原 artifact/key/digest/size/TTL、后端当前读取者复查或一次性消费/审计；不能撤回已准入消费、保证重下、生成新导出或续 TTL。
- 能力查询仅 GET：显式刷新、可见窗口每分钟、恢复焦点/可见时重查，合并并发刷新；查询中、故障及超过本地两分钟提示预算时不沿用旧写入资格。保护持续到新 data/error 发布完成，不能在检查回调已返回、页面状态尚未采用新结果的交接间隙重新暴露旧允许。只保留已准入的精确页面/详情和原意图；新菜单/深链接不借旧报告放行。授权错误促使重查，不自动重试命令。该预算不是授权 TTL、策略版本或全局限流；报告、预览和 Writer Epoch 不是执行凭据/因果水位。后端及延迟 worker 的当前授权/租约/fence 已接入，真实源/浏览器验收仍开放。
- 旧直接创建的未确认键或旧擦除记录阻断新接口；不会把未绑定的旧键迁移成第二个命令。旧服务端 Sync/Export 预览只有精确原范围可沿用原键恢复。旧版仅存内存的 Operation/admin 创建请求无法在刷新后自动识别，上线切换前须核实原列表/回执。
- 过期/冲突仍可仅 GET 核实原意图和已执行回执；prepared/NotFound 不证明旧在途请求不存在，不会因它们续期、丢键或重生成导出。擦除回执指针在刷新后保留。
- 放弃未执行预览需另输入精确原意图 ID，并 POST 原版本/digest。处置与 Execute 原子竞争：已创建回执则返回原任务、不取消或回滚；只有服务端 `abandoned` v2 关闭证明才允许用户显式选择并准备新预览。关闭无需重新获得生成权限，但新命令仍重新授权。原计划/键/TTL/审计保留，不授予清理或 Legal Hold 解除。
- 本地关闭标记不是权威：每次显式准备新意图前，仅 GET 复核相关关闭记录、ID/caller/target/元数据 hash，整体最多15秒，生成新键前再查当前动作资格。prepared/NotFound/outage/损坏均 fail closed；迟到回应不能回退关闭状态或替换原键/回执，不自动提交新命令。hold-aware retention、浏览器指针清理策略及真实浏览器/源/WAN 验收仍开放。菜单、链接和前端状态都不是安全边界。

### Profile 保存边界

- 当前表单仍只提交 `display_name`、`avatar_url`、`locale`、`timezone` 四个自有文本字段，不回写账户 ID、preferences 或 Snaplink 身份字段。服务端身份/租户、写 fencing 和事务审计不变。
- 原 GET/PATCH 的成功回应必须有这四个根级字符串，空字符串有效；缺失、null、非字符串、嵌套字段或别名不补位，也不强转/补默认值。坏回应整份拒绝为固定 `response.invalid`，不采用部分字段；初次读取失败不显示可保存表单，显式「重试」只 GET。PATCH 坏回应保当前草稿、不显示保存成功、不自动 GET/保存；可能已提交，先 GET 核实后再决定是否另行人工保存。此项只是文本基本形状检查，不是完整 metadata/preferences 校验、账户/租户绑定、当前授权或版本 CAS。
- `save` 处理函数先拒绝卸载后的新调用，不只依赖 DOM 移除；同账户新视图/会话不能为旧 submit listener 准入或发送旧草稿。拒绝早于保存状态/提交快照修改和原 client 调用，不取消或回滚已准入 PATCH。已有 PATCH 未结束时仍直接忽略重复 submit，覆盖同一轮事件、在途请求和响应正文读取，不只依赖按钮 loading。不排队，不自动重试；完成或报错后仍需另一次显式保存。原错误展示及空字段 fallback 保持。
- 每次请求捕获四字段快照；成功回应只为仍与本次提交值相同的字段采用服务端值（包括服务端规范化），保存期间后来改动/清空的字段保留在草稿。原服务端结果与草稿分离，有差异时提示「上次提交已保存，仍有未保存的修改」，不能把新输入说成已保存；报错也保留当前输入，不自动补发。
- 这是当前挂载表单的内存/值比较保护，不是 Profile 版本 CAS、跨客户端冲突合并、跨页面/标签页/会话锁、持久幂等或授权；刷新、换页不保证草稿恢复，也不持久化草稿。丢回应、超时、取消读取或关闭页面不证明 PATCH 回滚；先 GET 核实当前资料，再决定是否另行人工保存。没有新增 key、预览、来源命令、自动恢复或全局限制。

### WORM Anchor 确认边界

- 按钮和原 `runAnchors` handler 都要求当前输入精确等于 `ANCHOR ALL CHAINS`；空值、大小写/空白变体或后来改错的输入在 client 调用前拒绝，不只依赖 disabled。拒绝不清原内存键、确认输入或错误，修正输入不自动执行，仍需显式确认。原 fetch/正文单在途保护不变，输入改变不能撤回已准入请求。
- 原 `openRun/runAnchors` 处理函数还在更改意图状态、生成键或调用 client 前拒绝卸载后的新调用，不只依赖移除 DOM；新面板/会话不为旧 listener 准入。已准入 POST 不因卸载或取消读取被取消/回滚，迟到结果不会应用到新面板，也不会自动重试；原内存键不保证换页/刷新恢复。
- 单链 Manifest 的原 `queryManifest` 处理函数也在修改查询状态或调用 client 前拒绝卸载后的新 submit，新面板/会话不为旧 listener 准入。原 GET/client promise 未结束（含正文读取）时，handler 在输入校验/查询状态修改前忽略重复 submit，不排队后来输入；成功或报错后仍需另一次显式读取。保原 source/partition trim/编码、30秒/2MiB传输预算和错误展示，不续原期限、不自动查询/重试或执行 Anchor。单在途仅属当前面板，不是跨页面/会话或全局锁；取消依赖协作，deadline 信号本身不释放尚未结束的 client 调用。挂载/单在途守卫不是当前 `audit.read` 授权或证据完整性保证，不撤回已准入读取/访问审计；旧读取晚结果不应用到新面板。
- Manifest GET 回应的根级 `source`/`partition` 必须是与本次提交目标精确相等的字符串，不借后来编辑的输入、别名或嵌套字段补位，也不强转/trim 回应。缺失、坏类型或错目标整份固定 `response.invalid` 拒绝，不展示被拒证据或回显目标；仍需另一次显式 GET，不自动查询/重试、Anchor 或修复。OpenAPI 明确实际 data/目标字符串；匹配目标不是完整文档/hash/signature 验证、当前权限、全局时点、完整归档或 WORM/存储验收，拒绝回应也不撤回已持久化访问事实。
- 服务端 `system.config`、归档前 `audit.anchor.requested` 和 Manifest 读取审计保持原契约。挂载守卫/短语不是授权、持久业务幂等或跨刷新恢复；原 HTTP 通用缓存未升级。超时/丢回应/后续链失败可能已有部分锚点，先核原审计与 Manifest 再决定是否显式用原键重试，不自动锚定、换键或把本地组件测试当作真实 WORM/存储验收。

### Callback 操作边界

- `callback.read` 只报告已接线的元数据读取资格，服务端仍以当前 Snaplink `callback.manage` 加原 owner/tenant 查阅；暂停或缺投递密钥时可读，不推导新的写入许可。缺新读取能力的旧报告不放行新页面。`callback.manage` 的 Web 写入指导还要求投递配置可用且未暂停；兼容 API 的删除/replay 可在暂停时单独鉴权调用，不借出读取资格。
- 列表/详情校验 UUID、tenant、订阅/投递 ID、排序/游标、状态、预算与固定脱敏错误字段；不能通用展开未知 JSON。GET 可取消，换订阅/筛选、卸载或身份变化不采用迟到回应。分页与 worker 更新并发，只是当前本地行，不是完整尝试时间线或接收端业务回执。
- 注册、删除、选择重放及最终确认都按当前动作能力 fail closed。只对已验证详情中的 dead_letter 提示重放；delivering、过期 lease、success、pending 或未知不能被当成“远端未执行”。HTTP 2xx 也不证明业务完成。权限错误只刷新能力 GET，不重发写入。
- 注册未知结果保留原表单/键，回执必须匹配原端点、事件集和 tenant，缺失/错误元数据不推断成功或清键。晚到注册 Secret 不显示在另一身份/已卸载页面。注册仍是有限期 HTTP 缓存/内存键恢复，删除仍是兼容管理命令；旧 HTTP/gRPC replay 的缓存/非持久保证不变，不能把旧未知结果自动升级成新预览。
- 页面重放不再调用旧 replay API；从已验证 dead-letter 且 **没有任何 lease timestamp（包括过期值）** 的详情获取精确 subscription/delivery/event/revision/claim generation 预览。必须输入原 Delivery ID 和 Callback Event ID 才能 Execute。当前资格在异步 hash 后/新 POST 前复查，撤权/查询中/故障禁确认但保留输入，不自动重试或续期。
- 首次 POST 前保原 UUID 键/hash，丢 prepare 回应按原键 GET；返回的 Callback tuple 必须匹配原请求签名。已知 ID 只 GET 恢复原 metadata/hash/回执，跨刷新不重 Prepare 或 Execute。prepared/NotFound/expiry/状态变化不准换键；仍在原页面且保原 tuple 时可显式以原参数/键重取预览，刷新后不保存 tuple，只按原键查回。损坏/超预算指针 fail closed。
- `executed` + `receipt_kind:callback_replay` + `receipt_id:intent_id` 只代表本地 CAS 排队。排队回执指针跨刷新保留，不自动丢键；输入精确原意图 ID 后可 **仅 GET** 核实原 executed 回执并显式允许另选人工意图（不取消、不证明接收端无效果）。核实标记不是权威，新预览前再逐条 GET 检查原 ID/caller/account/hash/回执，合用15秒预算，并在新 POST 前再核动作资格。最多16项包括未确认/abandoned/已核实回执；没有自动淘汰、清理或 retention 许可。
- 原预览的 abandon 仍与 Execute 原子竞争：关未执行准入或返回执行已赢的排队回执，不取消发送。Metadata/原意图恢复与写入资格分离，订阅读取故障或暂停时仍可核原意图，但不能借恢复续权。关闭、身份切换/卸载不采用迟到回应，取消 GET 不能撤回已提交命令或字节。真实浏览器/接收端/撤权/滚动部署/WAN、动态暂停、hold-aware retention、exception center 仍待验收；本地 CAS 不消除远端未知效果。

完整进度及未通过门禁见 [`../docs/plans/control-plane-production-readiness.md`](../docs/plans/control-plane-production-readiness.md)。

## Snaplink client

注册独立 public client，不能复用 `snaplink-console`：

```text
client_id: aero-account-console
token_endpoint_auth_method: none
grant_types: authorization_code
response_types: code
require_pkce: true
redirect_uri: https://accounts.example.com/
tenant_id: <Snaplink tenant ID>
allowed_resources: aero-id
scopes: openid profile account:read account:write operation:read source:sync
```

Snaplink 当前把 `tenant_id` 从 client 绑定写入 access token，而 aero-id 的生产校验要求非空
tenant claim。因此每个租户应注册独立 client（或使用独立域名/运行配置选择对应 client_id），不能
用一个无租户 client 给所有租户签发 token。

Snaplink discovery 的 `authorization_endpoint` 是 `/auth/login` JSON API，不是可导航的
登录页面。`snaplinkHostedLoginUrl` 必须指向单独部署的 Hosted Login UI（例如 Snaplink
Console 的 `/login/`）；Token endpoint 始终来自 OIDC discovery。

## 运行配置

生产部署应在加载应用前提供 `/runtime-config.js`：

```js
window.__AERO_PLATFORM_CONFIG__ = {
  snaplinkIssuer: "https://identity.example.com",
  snaplinkHostedLoginUrl: "https://identity.example.com/login/",
  snaplinkClientId: "aero-account-console",
  snaplinkResource: "aero-id",
  snaplinkScopes: [
    "openid",
    "profile",
    "account:read",
    "account:write",
    "operation:read",
    "source:sync",
  ],
  redirectUri: "https://accounts.example.com/",
  aeroIdApiBase: "https://accounts-api.example.com/v1",
  auditConsoleUrl: "https://audit.example.com",
  aeroImConsoleUrl: "https://messages.example.com",
  aeroVaultConsoleUrl: "https://files.example.com",
};
```

也可以复制 `.env.example` 用于本地构建。`redirectUri` 必须与前端同源，并与 Snaplink 注册的
回调地址完全一致；`aeroIdApiBase` 必须指向 HTTPS 的 `/v1` API（仅 localhost 回环开发地址允许
HTTP），且 URL 不得包含 query、fragment 或 userinfo。`snaplinkScopes` 必须包含 `openid`。
不要把 client secret、service credential 或 bearer token 写进任何前端配置。

## 开发与验证

```bash
pnpm install --frozen-lockfile
pnpm dev
pnpm check
```

### 可重复受控浏览器验收

```bash
# 复用系统 /usr/bin/google-chrome，不下载或替换浏览器
AERO_BROWSER_ARTIFACT_DIR=/absolute/run/cycle/browser pnpm test:browser
```

`playwright-core` 固定为开发依赖1.58.2。此入口启动仅监听回环的独占 Vite 和新临时 Chrome profile，记录 PID/端口所有权并有界关闭、独立核对已退出/端口关闭；不接管已有 Chrome、profile 或服务。提供制品目录时保存合成截图、DOM、无 header/token 的方法/路径操作日志；未提供时输出操作/清理日志，临时 profile 仍删除。确认 Chrome 退出后，仅对本次 `mkdtemp` profile 的文件系统清理允许最多3次线性退避（50ms起）重试，不使用 force 或忽略错误；仍失败则整个验收失败，不因此重试业务命令。

Operation 的手动刷新失败/显式恢复场景先独立加载终态基线，再注入读取故障；`requested` 等状态的自动 GET 可能合法地先取得故障回应、切换到错误视图，不能把这件事误当作手动按钮失效。自动 GET 故障另有真实浏览器场景，手动场景保留原 ID、单次 GET、显式重试和无命令断言；不靠加超时、sleep、force 点击或关闭生产轮询消除测试竞态。

这是 `browser-fixtures/` 下的隔离入口：真实 Iris 组件、原 ResourcesPage / OverviewPage / ConnectionsPage / DataPage / AccountDataPanel / JobsPage / JobDetails / OperationsPage / OperationDetails / AuditPage 与 AeroIdClient，只有受控合成 API，不经过登录、不伪造 verified JWT/生产授权。随机非 JWT marker 仅在本次拦截 transport 内存中使用，不记录或持久化。fixture 不由生产 index 引用，也不进入正常生产构建。验收涵盖读用量/引用、显式本地刷新、概览→选择范围→读取摘要→返回/资源链接、清除选择不自动请求、原 GET 空范围兼容、诊断不展开未知字段、读取失败后显式恢复，以及空/缺失/故障/拒绝/未知资格、陈旧/截断和390px布局；不代表真实 Snaplink 登录、来源连通、资源授权或远端业务验收。Operation 场景另含列表→步骤/补偿结果→返回、unknown 原 GET、缺范围时动作暂停、审计拒绝/空查询与原 cursor 恢复、按原 ID 筛选及390px键盘诊断。新增继续操作场景仅对隔离合成 API 允许精确原路径/原键的显式 Run、Reconcile 和危险确认，验证结果采用与 GET-only 刷新；拒绝/未启用/异地主区域/坏范围不发命令，服务端拒绝不自动重试。原只读场景仍逐项禁止 POST。来源引用场景实测概览→两个独立引用列表→来源/成员原范围→返回、同文本ID不同scope、原cursor错误恢复、单列表刷新、缺失/不可用/空页/坏列表、当前资格拒绝/未知不查询及390px键盘详情/返回；只调用原GET、不展开data或发来源管理命令。任务导航场景实测列表选中→原链接→刷新→浏览器前进/后退→关闭后刷新、复制另一回执、列表故障下原详情仍可读、详情拒绝/NotFound 返回及390px键盘导航，全部只 GET；创建回执刷新也不重准备/执行。Operation 创建场景允许且验证原 `/command-intents` 准备和原意图 execute，演示勾选两来源→调整顺序→可读预览→精确确认→原回执；含390px键盘、高级坏范围/拒绝、丢回应刷新只 GET 原键/ID，禁止直接创建或自动 Run。合成响应不代表真实来源效果或生产身份。component/unit、受控 browser fixture 和 live browser/source 是三层独立证据；真实环境未提供时后者仍 `not_executed`。

`pnpm dev:iris` 会在当前工作站直接解析 `~/iris-ui/packages/*/src`，适合组件库与产品 UI
联调；`pnpm build` 使用 lockfile 固定的已发布 Iris 包，因此 CI 和容器不依赖个人目录。
业务源码的 import 始终保持 npm 包名（例如 `@iris-ui-kit/vue`），Vite 只在
`IRIS_UI_SOURCE=1` 时把这些包名 alias 到本地源码。`pnpm check:iris-imports` 会禁止业务源码
直接导入本地 Iris 路径以及 `workspace:`、`file:`、`link:` 依赖。

生产镜像：

```bash
docker build -t aero-id-web ./web
docker run --rm -p 8081:8080 aero-id-web
```

部署时用 ConfigMap 或只读卷覆盖 `/usr/share/nginx/html/runtime-config.js`，不要在镜像中写入
client secret、service credential 或 bearer token。
