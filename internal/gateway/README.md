# gateway

编排一次模型调用：认 key、读请求、准入与路由、调上游、计量与追加、结算、记录。对外行为见[接入与对外接口](../../docs/domains/gateway.md)，调用经过哪些模块见[总体架构的「一次调用怎么走」](../../docs/architecture.md#一次调用怎么走)；这里写代码层面的顺序、所有权、期限、并发、故障和测试。

> 本文先于代码写成，是 [Issue #36](https://github.com/TYCT-tyct/Axiom/issues/36) 的调用路径契约；各部分随下文「PR 拆分」落地，已落地的是 G1–G9（下文「入口与准入」「尝试与转发」「计量、追加与结算」写它们的代码结构），代码与本文不一致时，在同一个 PR 里改本文。下层的接口见 [billing](../billing/README.md)、[routing](../routing/README.md)、[suppliers](../suppliers/README.md)、[protocol](../protocol/README.md#转发草图)、[accounts](../accounts/README.md#认出与准入)。

M1 是单个核心节点，共享状态都在进程内；多核心和 Valkey 在 M2：限流计数移到 Valkey（K1 已落地），会话粘性也已共享（K3），状态核对见 [platform/kv 的设计](../platform/kv/README.md)，供应商凭证并发与有限账号 RPM 按存活核心后台静态分份额，生产支持 N > 1。出口只有 `platform/egress` 直连。

## 不变量

账户暂停在 `accounts.Admit` 原查询读取账户状态，返回 `ErrAccountSuspended`；Chat/Responses 为字符串 `error.code: "permission_denied"`，Messages 为 `permission_error`，统一分类 `permission_denied`。已通过准入的调用正常结束，用户禁用登录不影响 key；恢复账户无需缓存失效。

| 不变量 | 靠什么保证 |
|---|---|
| I1 先落库再花钱：上游请求发出前，已提交的预留覆盖本次尝试当前允许执行的费用上界，加上停止前的最大额外消耗；不可分段的尝试就是整次的最大花费 | 准入提交后才进入尝试；`Bind` 只接受准入时报过价的条目；追加没批准就停读（「计量与追加」） |
| I2 恰好一次终结：每个准入的请求恰好落到一个终态，经 `billing.Settle`（只释放也走它），或由清理按截止时间、持有者回收接管 | 每条退出路径（含 `recover` 之后）都调 `Settle`；预留"比较后再写"；清理见 [billing 的「预留的状态」](../billing/README.md#预留的状态) |
| I3 先入账再报费用：带费用的结尾只在结算提交后发出 | Relay 扣住成功结尾，所有者在 `Settle` 返回后才调 `Finish` |
| I4 提交后不换尝试：向客户端写出正文、推理或工具调用之后，不再换端点 | Relay 返回 `Commit` 之后不再调用 `routing.Decide` |
| I5 钱只在 PostgreSQL：每笔收费写入时守余额底线；缓存丢失不改变钱；已确认的提交不因主备切换丢失 | billing 的余额行锁；[运营的复制要求](../../docs/domains/operations.md#部署与节点管理) |
| I6 调用路径上同步访问缓存至多一次，在准入之前 | 只有第 5 步访问缓存；排队醒来从第 6 步重做 |
| I7 写入有栅栏：持有者被回收后，它名下不能再新增或追加预留；回收之后的清理看得到它全部已提交的预留 | [billing 的「持有者与截止时间」](../billing/README.md#持有者与截止时间) |

## 一次调用的步骤

DB 指一次 PostgreSQL 往返（BEGIN、设范围、COMMIT 各算一次；同批发出的语句算一次），C 指一次缓存往返。

| # | 做什么 | 模块 | 往返 | 期限 | 失败时 |
|---|---|---|---|---|---|
| 0 | 生成请求 ID（UUIDv7），即调用编号 | gateway | — | — | — |
| 1 | 按 IP 的在途上限；只用 Host 和请求头：`SiteForHost`、`AuthenticateKey`（未命中查库、合并并发查找）。不读请求体 | gateway、accounts | 0；未命中 1 个事务 | 1s | 429；401 立即关闭；库不可用或站点快照过旧 503 |
| 2 | 按账户在途上限，再取节点在途名额，每路记 64 KiB | gateway | — | — | 429 / 503 |
| 3 | 有上限地读请求体，长度计入节点内存预算 | gateway | — | 2 分钟 | 413 / 408 / 503 |
| 4 | `protocol.Inspect`；catalog 解析名字（含 `models`）；`routing.SessionKey` | protocol、catalog、routing | 0 | — | 400 / 404 |
| 5 | 预检：限流计数加一（速率计数，不需释放），读会话粘性；只计数不判定 | gateway | M1 0；M2 1 C | 20ms | 按本节点计数、无粘性 |
| 6 | 账户闸 → 准入名额 → `accounts.Admit`，`fn`：判定限流 → `routing.Plan` → `billing.Reserve`；提交 | accounts、routing、billing | 7 DB | 闸 5s，之后连接加事务合计 1s | 429 / 402 / 404 / 503；`Wait` 则回滚、事务外排队，醒来只重做本步 |
| 7 | 尝试：本地持有者检查 → `Acquire` → `Prepare` → `Bind` → `Send`；从读到第一个字节起计量，暂存开头事件到提交点 | suppliers、protocol、billing | 上游 | 见「超时与限额」 | 提交前按 `routing.Decide` 换条目 |
| 8 | 提交后边读边计量边转发，按段 `Extend` | gateway、protocol、billing | 每段 9 DB（已覆盖时 6） | 追加 1s | 截断 |
| 9 | 上游正常结束：`Settle` 提交 → `Relay.Finish` 写最后一个事件 | billing、suppliers、protocol | 13 DB（一次尝试） | 2s × 重试 | 「结束与结算」 |
| 10 | 每条退出路径释放凭证、名额；异步写调用记录、quality 观测、粘性 | gateway、observability、quality | 0 | — | 「并发、背压与共享状态」 |

## 准入

- `fn` 由本模块组装，依次：用预检计数对照限额（M1 只有平台配置的防滥用限额；以后的 key、等级速率值从 `Admission` 读，判定位置不变）；`routing.Plan`；最后 `billing.Reserve`。`Reserve` 返回保留条目在原路线里的下标，本模块按它裁剪路线；首选条目被删就是 402。
- 往返 7 次，分解与批量内独立语句的锁、快照语义见 [billing 的「准入里的预留」](../billing/README.md#准入里的预留)。
- `Plan` 返回 `*Wait`：`Admit` 回滚（还没有预留），在事务外按 FIFO 等进程内名额，醒来只重做第 6 步，不访问缓存、不重复计限流。
- **准入的 COMMIT 确认丢失**：返回 503、不发上游，交 `billing.SettleOrphan(adm, id)` 收尾（见 [billing 的「追加与结算」](../billing/README.md#追加与结算)）。

## 入口与准入

G1、G3 的代码结构。`Handler`（`NewHandler(Deps)`，依赖缺一个就不启动）是 `http.Handler`，外面要套 `httpserver.Public`，请求 ID 由它生成，`Call.ID` 就是它（UUIDv7），每个响应（含错误）带同值的 `X-Request-ID` 和 `X-Generation-Id`：后者由 `Handler.ServeHTTP` 统一设置，所以模型列表、拒绝升级、405 和 404 也有。`/v1` 和 `/api/v1` 两套前缀的路由相同：`POST /chat/completions`、`/responses`、`/responses/compact`、`/messages`，`GET /models`、`GET /models/{model}`；四个调用路径上的 GET 只用来拒绝 WebSocket 握手（带 `Upgrade` 回 400，否则 405）。没有路由接的路径和方法也由错误出口按协议的外壳作答（404 `not_found`、405 `method_not_allowed`，保留 `Allow`）。`count_tokens` 和只读的账户接口见「辅助接口」。

**一次请求的顺序**（任何一步失败都由错误出口作答，已取的名额和内存经 `Call.Done` 归还）：

| 步 | 做什么 | 失败 |
|---|---|---|
| 1 | 按 IP 的在途上限（`Backpressure.IPCalls`）；不在 accept 时按 IP 限连接数：节点只绑本机、前面是反向代理，accept 时看到的是代理的地址，按它限连接会变成整个节点的连接上限，所以连接数由前置代理或 CDN 限制（[安全的「防攻击」](../../docs/security.md#防攻击)）。IP 取 `platform/clientip` 的 `Addr`：只认 `RemoteAddr`，`AXIOM_TRUSTED_PROXIES` 里的代理才读 `X-Forwarded-For`，与控制台守卫是同一份；计数的键是 `clientip.Group` 的来源分组，与登录尝试同一份（映射 IPv4 还原、去 IPv6 zone、IPv6 按 /64） | 429 |
| 2 | 带 `Upgrade` 头的请求 | 400 |
| 3 | `SiteForHost(r.Host)`，必须是 API 域名；`Authorization: Bearer`（不分大小写）或 `x-api-key` 认 key（有 Bearer 用 Bearer）；未登记的域名、控制台域名、认不出的 key 都是同一个 401，带 `Connection: close`，体从不被读。key 不在缓存里时查库，查库在共享的一次任务里取一个准入类连接名额（`accounts.Config.LookupSlot`，见「并发、背压与共享状态」）；认出只表示 key 存在，能否使用由第 7 步的 `Admit` 判定 | 401；站点快照过旧、库不答、名额等不到 503 |
| 4 | 账户在途名额，再节点名额（`InFlight.Acquire`） | 429 / 503 |
| 5 | 读体（`MaxBody`，默认 32 MiB）：宣告的长度先计入节点内存预算（`protocol.Memory.Charge`），按宣告的长度一次分配；未宣告的从 4 KiB 起翻倍增长，每次分配前先计入新缓冲、复制后退还旧的，峰值就是已计入的量，读完留下的是最终缓冲（至多正文的两倍） | 413；408（超时）；503（预算不够）；断开或读到一半 400 |
| 6 | `Inspect`；`previous_response_id` 非空 400；`provider` 对象转成路由覆盖（下文「路由参数」）；`parseModelName` 读掉模型名的变体（`:nitro`、`:floor` 定排序，`:exacto`、`:online`、`~` 别名 400，已知目录变体 400，不认识的后缀 404），`catalog.ResolvePublic` 认主模型的基础名字（不存在或已下线 404），`models` 里的备选（同样读变体）认得出的跟在后面；`SessionKey`；预检同批取限流计数与粘性（本地模式用 `Stuck` 与 `RateCounter.Count`） | 400 / 404 / 503 |
| 7 | 准入循环（下文） | 见「错误出口」 |
| 8 | 交给 `Run` | — |

**准入循环**（`admit`）：取 `Queue.Epoch()`，再 `admitOnce`；`*routing.Wait` 时事务已回滚，在事务外 `Queue.Wait`（按 key 计），醒来只重做 `admitOnce`，不重读体、不重复计速率。排队期限 `QueueWait` 从这次调用第一次排队起算一次（`Queue.Wait` 收到的是绝对期限），被无关的唤醒叫醒再排队也共用剩余时间，到期 429。期限在 `Wait` 里先于 `Epoch` 快路径检查，所以别处不断释放名额也不能让过期的调用继续重试；期限过后至多再做正在进行的那一次准入。`admitOnce` 的 1s 期限（`AdmissionBudget`）从取得账户闸起算，连接和事务共用，等闸不计入（`Funds.Admit` 把带期限的 context 交回来）：`Funds.Admit` → `accounts.Admit`，`fn` 依次是速率判定（预检计数超过 `KeyRate` 回 429）→ `routing.Plan` → `billing.Reserve`。`Reserve` 返回的下标裁剪路线（`Call.Plan.Entries` 与 `Record.Route` 只留保留下来的条目），首选条目被删就是 402。`Input.Admission` 里 key 没有预设时用零 ID 的预设，路由快照必须有它；`Admission.Models()` 为 nil 是全部模型、空列表是一个都不行。`billing.Model` 由路由条目带来（`Entry.Limits`），与路由过滤用的是同一份。

**提交确认丢失**：`fn` 写完预留后 `Admit` 仍返回错误（提交失败、确认丢失、期限到），无法区分已提交还是已回滚，一律按"可能已提交"处理：回 503，不发上游，名额不立即归还，后台 `settleOrphan` 调 `billing.SettleOrphan`（账户闸的完成类、2s 一次，库出错退避 50ms 到 2s 重试，到 `SettleBackground` 为止），成功或放弃后才 `Call.Done`。`Handler.Drain` 等它们，期限到则停止重试，剩下的归清理。

**`Run` 与 `Call`**：`Run` 是准入之后的缝（`Executor.Run` 填），拿走 `Call` 的所有权：`Call` 带请求 ID、格式、compact 标记（`/responses/compact` 的调用原样透传）、摘要、体、白名单头、会话键、`accounts.Admission`、`billing.Admitted`、`*billing.Hold`、裁剪后的 `routing.Plan`；结算写进之后（或交给清理之后，见「计量、追加与结算」）调 `Done`，归还账户和节点名额与体的内存，可能晚于 `Run` 返回。`NewHandler` 要求 `Run` 非空，生产路径没有 stub；测试里的 `Run` 只用 `SettleOrphan` 释放预留。

**错误出口**（`writeRefusal`）：状态码、`error_type` 和外壳按[接入的「错误」](../../docs/domains/gateway.md#错误)。Messages 有自己的外壳，Chat 和 Responses 用 Chat 的（`message`、`type`、`param`、`code`，`metadata.error_type` 是错误类型的名字，`code` 通常同它）；`type` 由状态码决定。`protocol.RenderError` 写同样的外壳（尝试之后的错误、流中途的错误），两个写入函数由 `TestARefusalIsAnErrorOfProtocol` 拴在一起：各类拒绝（包括 provider 参数校验）与 protocol 写的同一个错误逐字相同；账户暂停外壳另由 `TestAccountSuspensionErrorEnvelopes` 单独验证。`Retry-After` 取整数秒、最多 60。文字不用 new-api 自动停用渠道的措辞（`TestMessagesAvoidAutoDisablePhrases`）。判定：
- 402：`limit_source` 取 `balance`、`workspace_budget`、`key_limit`；会重置的上限（`Insufficient.Transient`）带 `Retry-After: 60`，billing 不报重置时刻。在途预留占满余额时同样报 `balance`：`Insufficient` 还没有区分它的来源，也没有所需金额，待 billing 报告。
- `NoEndpoint`：理由全是 `NotAllowed`（key 不能用这个模型）回 404 `model_not_found`，不是 403；其余永久的回 404 `no_endpoint` 附理由；暂时的回 503 `no_endpoint`，`Retry-After: 1`，附理由。
- 没有路由接的路径和方法：`Handler.ServeHTTP` 先用 `mux.Handler` 看有没有路由，没有就让 mux 自己判 404 还是 405（连同 `Allow`），只丢掉它的纯文本响应体，再用 `writeRefusal` 按路径选外壳——`/v1/messages` 与 `/api/v1/messages` 下用 Messages 的，其余用 Chat 的；404 是 `not_found`，405 是 `method_not_allowed`。`refuseUpgrade` 的 405 同样走 `writeRefusal`，`Allow: POST` 保留。
- 满载排队超时回 429，`Retry-After` 是 `Wait.RetryAfter` 取整。
- 路由、目录、站点快照过旧，持有者失效（`billing.ErrTerminated`），准入期限到，提交确认丢失：503 `overloaded`。其余没预料到的错误同样回 503，并记 error 日志。
- **依赖故障按状态变化记日志，不逐请求记**（#294）：数据库连不上（`pg.Unavailable`：连接失败、SQLSTATE 08 类、57P01 到 57P03、53300）和账户、目录、路由的快照过旧（各自的 `ErrStale`）都回 503 `overloaded`，但不走逐请求的日志。每个依赖（`database`、`accounts`、`catalog`、`routing`）各记各的：故障开始或原因（固定文本，不含主机和用户名）变了，记一条 error，原来的那条同时记一条 warn 结束；同一原因不重复记。成功用到该依赖的请求结束它的故障，记一条 warn（期间拒绝的请求数和时长）：通过准入事务的请求结束 `database`，认出站点的结束 `accounts`，取到目录快照的结束 `catalog`，路由出计划的结束 `routing`；不用路由的请求（如模型列表）不会结束路由故障。每个被拒的请求计入 `axiom_gateway_outage_refusals_total{dependency}`。实现和不变量见 `outage.go`；测试 `TestAnOutageIsLoggedOnceNotPerRequest`、`TestANewReasonIsTold`、`TestAHelperRequestEndsTheOutageItUsed`。
- 客户端已断开的请求不写响应，读体超时除外：net/http 在读期限到时会取消请求的 context，但连接还能写，所以 408 照写，并带 `Connection: close`（`TestSlowBodyIsAnswered408OverARealConnection` 用真实连接守住这一点；用模拟读失败的测试看不出）。

**派生标识的长度**：`protocol.Rewrites.Identity` 返回的会话和用户标识，以及它和 `Rewrites.Device` 一起写成的 `metadata.user_id`，都须不超过 128 字节（`metadata.user_id` 按内层 JSON 编码后的长度算），`Prepare` 对超出的返回错误（`IdentityLen` 为 0 时的默认上限，#121）；G1、G3 自己不调 `Identity`；调用 `Prepare` 的尝试（G4）把客户端的值经 HMAC-SHA256（密钥 `ExecDeps.IdentitySecret`，加账户 ID）派生成 32 个十六进制字符，`metadata.user_id` 里的结构因此是 114 字节（两个 32 字符的分量加 50 字节的结构），都在约束之内。

**命名预设**（`presets.go`）：`expandPreset` 在摘要初读后从 accounts 的租户快照读出配置，按 `protocol.PresetCost` 先申请合并工作集，再调用 `protocol.MergePreset` 并重新 Inspect；保留的正文容量到 Call.Done 退款，拒绝时全额退款。会话指纹扫描客户端原始正文，以免默认展开移动缓存键或插入系统提示后改变身份；显式 body session_id 仍来自完整最终摘要。所有能力、路由、输入报价和执行均使用最终摘要；count_tokens 也使用同一展开路径。未引用时只做引用判断，不建立映射或编码请求。命名配置不改变 Admission 上的模型权限或强制路由预设。API 与覆盖规则见[命名预设](../../docs/domains/routing.md#命名预设)。

**路由参数**（`provider`，`override`）：`Inspect` 把对象原样交出（至多 4 KiB），gateway 转成 `routing.Override`，每个成员要么生效要么按名字回 400。`order`、`only`、`ignore` 保存为 `OrderNames`、`OnlyNames`、`ExcludeNames`，routing 与同次快照的 `Endpoint.Codename` 比较，不能扩大到同供应商其他分组；代号由 suppliers 的 `Routable.Codename` 提供，不增加调用路径查询。`allow_fallbacks: false` 有顺序时限制命名组，无顺序时每个模型在健康排序后只取首个端点，保留 `models` 的跨模型回退；原有预设过滤仍独立检查。`sort` 字符串和 `{by, partition}` 对象共用策略，支持 price、throughput、latency；`partition: none` 在逐模型汇集候选后整体排序，默认 model 保持主模型优先。首字延迟独立升序，没测过的排后；吞吐仍降序再比首字延迟。数据政策、数字性能偏好与 token 价格上限继续按既有规则执行。`max_price.prompt/completion` 以本站记账货币每百万 token 精确转成单 token 单价，不取整；指数与精度在构造大整数前检查。校验失败沿用 Chat/Responses 的字符串 `invalid_request` 与统一错误分类，Messages 用原生外壳；`TestARefusalIsAnErrorOfProtocol` 同样覆盖 provider 拒绝。无法兑现的完整清单及 OpenRouter 来源见[接入的「路由参数」](../../docs/domains/gateway.md#路由参数)：完整参数支持、非空量化列表、百分位对象、按请求/按张价格与未知成员均在预留前 400。

**插件与变换**（`plugins.go`）：入口经 `protocol.Inspect` 校验，未知或未支持的插件/变换在准入、预留之前返回 400，Chat/Responses 的 `error.code` 为字符串 `invalid_request`，沿用统一错误契约。压缩仅对 Chat 按主模型目录规格执行：先在节点预算里计入 `protocol.CompressionCost`，再从中间裁剪，替换正文并重新生成摘要，随后才取会话键、路由和报价；原正文与替换正文的内存直到 `Call.Done` 归还。默认压缩只在正文超过整个上下文的字节上界或消息上限时尝试，不扣默认输出上限；不能压缩或临时内存不足时保留原正文与摘要，显式开启仍严格拒绝失败。重试使用相同正文。响应修复由 `Prepare` 带到非流式 Chat 聚合器，不新增上游调用，不改流式计量。执行与偏离见[接入域的插件表](../../docs/domains/gateway.md#插件与提示词变换)。

**模型名变体**（`variant.go`，[docs/domains/gateway.md 的「模型名变体」](../../docs/domains/gateway.md#模型名变体)）：`parseModelName` 在调用、`count_tokens`、模型详情和 endpoints 查目录之前把 `:` 后面的变体读掉，只把基础名字交给 `ResolvePublic`，`Request.Model` 不改，所以回显和调用记录里仍是客户端写的名字。`:nitro`、`:floor` 查 `strategies` 表，等于 `provider.sort` 的 `throughput`、`price`，最后一个排序变体生效；显式的 `provider.sort`（`override` 已读好的 `Sort`）优先，`resolve` 把最终排序交回，`read` 写进 `Override.Sort`。`:exacto` 在没有别的排序替换它时回 400（尚无工具调用成功率与基准评分，现有健康度不等于 Exacto），`:online` 回 400，`~` 开头回 400；`:free`、`:batch`、`:thinking`、`:extended` 回 400；不认识的后缀回 404，不看基础名字存不存在，从不落到基础模型。备选名字读同一个函数：400 照回，404 的略去，排序变体与请求最终排序不同回 400，备选的 `:exacto` 只有显式 `provider.sort` 能替换。调用（`read`）和 `count_tokens` 共用 `readRouting`（它连 `provider` 一起读，`override` 在其中），所以两边拒绝的一致。整个名字超过 `maxModelName`（200 字节，调用记录的列上限）在 `parseModelName` 里 404，后缀叠加也绕不过。不变量：目录只收到不带 `:` 的名字；`parseModelName` 是读变体的唯一入口，`readRouting` 是定最终排序的唯一入口。发现接口只使用 `parseModelName` 解析基础名字，不执行 `readRouting` 的排序校验，因此 `:exacto` 可查询基础元数据；调用与 `count_tokens` 仍要求显式排序覆盖它。

**头白名单**（`headers.go`）：按**目标协议**各一份表，只转发表里的名字，其余一律不转发（含 `Authorization`、`x-api-key`、`x-axiom-*`、`x-claude-code-*`、`HTTP-Referer`、`X-Title`、`Cookie`、`User-Agent`）。`accept` 时一次拷下所有协议可能用到的头（`forwardable`），每次尝试再按端点格式挑（`(*attempt).upstreamHeader`），所以同一份请求发给不同格式的端点时发的头也不同。Messages 端点：`Anthropic-Beta`、`Anthropic-Version`，值原样（`anthropic-beta` 不筛选值，见[接入的「转发规则」](../../docs/domains/gateway.md#转发规则)）。Responses 端点：`session_id`、`session-id`、`conversation_id`、`X-Session-Affinity`、`X-Session-Id`、`X-OpenCode-Session`、`X-Conversation-ID` 换成派生值——它们都是 sub2api 用来定账号的会话名（sub2api `internal/service/openai_gateway_scheduling.go` 的 `explicitOpenAIHeaderSessionNames`，2026-10-19），值与体内 `prompt_cache_key` 的派生同一个函数，客户端两处写同一个值时两条路读出来是同一个会话；`X-Codex-Turn-State` 原样转发，它是厂商铸的不透明状态，派生等于伪造。Chat 端点：那七个会话名同样换成派生值（它也是 OpenAI 的方言，中转站照样按这些名字定账号），没有状态头。空白的会话值整个头不发：派生空值会让所有客户端共用一个会话；体内 `metadata.user_id` 和 Chat `user` 转 Messages 遇到空串或纯空白也不派生，交给 protocol 的同一个编码函数写为空串，让中转站按内容回退。要加协议头只改 `requestHeaders` 这一处。

**模型列表与单个模型**：`GET /v1/models`、`/api/v1/models` 和两套前缀下的 `GET .../models/{model}` 要认 key，并像准入一样判定它能否使用：取一个准入类连接名额（不过账户闸，不动钱），调 `accounts.Admit`，在同一个事务里读 `Admission.Models()`，所以停用、过期、删除、管理类 key 和 IP 白名单都生效，缓存里的认出不算数。不占账户名额，只受 IP 在途上限约束。key 的模型集合用 `allowedModels` 换算，和 `routingAdmission` 过滤路由是同一个函数（nil 是全部，空列表是一个都不行）：列表和单个模型只出现这个集合里、且路由快照里有端点的模型（`routing.Router.ServesFor`：路由快照组装时没有售价或规格的模型已整个排除，所以没有端点的模型调用必然 404 `no_endpoint`，列出来只会让客户端选到必败的名字，`TestModelNothingServesIsNotListed`）；别的名字、没有端点的模型和不存在、已下线的名字一样回 404 `model_not_found`，不是 403。端点的实时状态不进列表：满载、熔断、暂停、预算用尽，模型仍在列表里（`TestModelListKeepsModelsWhoseEndpointsAreBusy`），容量由调用自己排队或报错；路由快照陈旧时列表照用最后一份，陈旧由调用回 503。`/v1` 下每个模型列出目录里的名字（`Slug`），以及去掉作者前缀的名字——只在它是公开名字、解析到同一个模型时才列，所以 `claude` 开头的厂商原生名字能被 Claude Code 发现。`GET /v1/models/{model}` 用 `catalog.ResolvePublic` 按调用路径的方式解析别名，投影与列表里的同一项相同（`modelEntry`），`id` 是请求里的名字。没有该受众可用发布价的模型不列出；满载与熔断等临时状态仍不影响列表。每项带 OpenAI 的 `id`、`object`、`created`、`owned_by` 和 Anthropic 的 `type`、`display_name`、`created_at`；目录没有上架时间，`created` 恒为 0。每个模型的条目按目录快照编码一次（`listing`；`created_at` 是常量，不逐项格式化），请求只按这把 key 的可见集合挑出条目、拼进一个缓冲区（`listing.encode`：一次分配，与目录大小无关，`BenchmarkModelList` 守住）；整份应答不缓存，因为它取决于 key 的可见集合和路由快照，两者都会变，而每次请求本来就要过一次准入事务。响应带 `Cache-Control: no-store`（`writeJSON`、`writeBody`），免得共享缓存把一把 key 的列表给另一把。IP 在途上限和 key 的 IP 白名单用的地址与调用路径是同一个（`h.clientIP`，第 1 步）。`/api/v1/models` 与 `/api/v1/models/{model}` 是 OpenRouter 的对象（`openrouter.go`）：`pricing` 取准入账户及当前等级的主站发布售价（标准服务等级、第一档，`web_search` 每次），`context_length`、`top_provider`、`architecture`、`supported_parameters`、`reasoning` 取目录的规格（未知 mandatory 不声称），`id`/`canonical_slug` 是公开标准 ID（目录名去掉内部 `@版本` 后缀，且要 `catalog.ResolvePublic` 解析回这个模型；解析不到就整个不列，那个名字既取不到详情也调不通），`name` 取标准 ID 的尾段，`created` 恒 0，`default_parameters`、`per_request_limits`、`supported_voices` 输出 null，售价表没有的单位（`image`、`image_output`、`request`、`discount`、`overrides`）省略而不是写 0（售价填不出 `prompt`/`completion` 的模型整个不列，OpenRouter 的对象里那两个成员没有 null 的位置），`description`、`knowledge_cutoff`、`hugging_face_id`、`expiration_date`、`alias_target`、`benchmarks` 不输出；单模型按发布的那份列表回答（`catalog.ResolvePublic` 解析别名，返回 `{data: …}`，`id` 是标准 ID：形状里一个模型只有一个 `id`）。每种受众的模型与端点条目、价格筛选和价格排序所用的值在构建时预编码，读取按与路由相同的受众索引选择，不逐请求重算价格。这份列表由 `RouteDeps.Build` 和路由快照一起产出、由 `Publish` 在 `ModelList.mu` 写锁内一起换：同一次读目录、同一次读售价，所以列表上的价格就是快照里那个售价版本的收费价格（`TestRouteBuildPricesTheListWithTheSnapshotsPrices`），构建失败或迟到则两者都不换（`Publish` 按 `built` 时间拒绝迟到的那份）；`ModelList.published` 和准入的 `ModelList.plan` 同持读锁，不能读到发布两次写入之间的状态（`TestModelPublicationExcludesReaders`）；锁只覆盖内存工作，不覆盖鉴权事务或编码。这里选择共享发布锁，不用两次独立的 atomic Store，因为两次 Store 无法排除中间状态。请求路径上没有读价（`TestOpenRouterRequestsDoNotReadPrices`），`ModelList` 只存最近发布的一份，所以一次响应里的 `total_count`、这一页和链接都属于同一次构建（`TestOpenRouterListIsOneBuild`）。查询参数按 `orQueryMembers` 生效、`orRefused` 里的名字（文档有而 Axiom 没有数据）回 400 说明缺什么、文档之外的参数忽略；查询串本身解不开（`%ZZ`、用 `;` 分隔）也回 400，因为 `url.Values` 会静默丢掉解不开的项。请求只做过滤、排序和一次缓冲区拷贝，缓冲区按这一页申请而不是整个目录（`BenchmarkOpenRouterList` 守分配次数，`TestOpenRouterPageBuffer` 通过 `alloctest.Measure` 在隔离子进程中守字节数：没有后续页链接的完整列表不排序一次分配，排序多一次给待排序的匹配项；生成翻页链接另有分配）。`/v1/models` 下的 `/count`、`/user`、`/{author}/{slug}/endpoints` 仍注册成 404，免得模型通配符吞掉；`/api/v1` 下三条路径和 `/api/v1/providers` 由 OpenRouter 模型发现实现（`openrouter_discovery.go`）。`newOpenRouterListing` 接受同次构建的路由端点，分组前排除所有非零 Owner 的私有端点，只向共享发现投影提供公共端点及模型；在副本上按内部 ID 排序，提前编码每个模型的 endpoints 响应；tag 是模型内序号 axiom/1、axiom/2，不暴露内部主键或创建时间，端点集合变化时可重新编号，不保证持久身份，provider_name 为 Axiom，供应商名、凭证、上游名和成本从不进入投影。计数和 providers 按 key 的模型可见集回答，count 只读取官方 output_modalities，复用列表的模态解析和匹配，非法或重复模态与损坏查询串回 400，没有可见模型时 providers 是空数组；user 复用列表过滤和翻页，链接保留 user 路径。未知数据、必填字段的有意偏离与逐字段来源见[领域文档](../../docs/domains/gateway.md#模型发现接口基线投影与偏离)。响应数据只读内存；实时准入仍查库，防止停用 key 和权限变更被缓存掩盖。`TestOpenRouterRecordedContracts` 回放每个端点的官方文档响应样本并比较本站完整投影，`TestOpenRouterDiscovery` 覆盖权限、空集及不存在模型，`TestOpenRouterUserPagination` 验证翻页路径；`TestOpenRouterCountOutputModalities` 对照列表总数核对模态过滤和非法查询；`TestOpenRouterDiscoveryExcludesPrivateOwners` 覆盖站点与账户私有端点及只有私有端点的模型不进入共享投影；`TestOpenRouterEndpointTagsHideIdentifiers` 验证替换内部 ID 不改变相同公共数据的响应，且编码不修改路由输入。叫 count 或 user 的公开别名依旧不能从单模型路径取出。模型接口的错误外壳按 `Anthropic-Version` 选 Messages 或 Chat；没有路由接的路径只按路径选外壳，所以 Anthropic SDK 访问不存在的 `/v1/organizations` 得到的是 Chat 外壳，这不违反「错误」一节按路径选外壳的规定。

## 辅助接口

G9 的代码在 `readonly.go`（只读接口）、`count.go`（`count_tokens`）。两套前缀（`/v1`、`/api/v1`）下路由相同：`POST /messages/count_tokens`，`GET /key`、`/credits`、`/generation?id=`、`/dashboard/billing/subscription`、`/dashboard/billing/usage`。行为基线是 OpenRouter 的 `GET /api/v1/key`、`/credits`（OpenRouter《API Reference》，2026-10-16 读取；`generation` 的字段取自同一套文档的惯例，该页当日取不到）、Anthropic 的 `count_tokens` 和 OpenAI 旧版的两个余额接口（new-api 源码，2026-10）。

**认 key 与读数据**：与模型列表同一条路——`IPCalls`、`authenticate`、一个准入类连接名额（不过账户闸，不动钱）、`accounts.Admit`——`fn` 在同一个事务里读（模型列表读 key 的模型集合，这些接口读调用记录、余额、花费）。事务的范围就是 key 的账户，行级安全是全部的访问控制：别的账户的调用记录、余额、花费读不到，`generation` 取不到别人的编号（与不存在的编号同为 404）。key/工作区停用、过期、删除、管理类 key、IP 白名单、控制台域名都是同一个 401；其余条件有效而账户暂停调用时为 403 `permission_denied`。M1 没有管理 key 的接口，所以 `/credits` 不像 OpenRouter 那样要求管理 key（OpenRouter《Get remaining credits》："Management key required"，2026-10-16），调用 key 就可以读本账户的余额：偏离写在[接入的决策表](../../docs/domains/gateway.md#决策与理由)，M2 有管理 key 时再按其权限收紧。错误用 Chat 的外壳；`count_tokens` 用 Messages 的外壳。成功响应带 `Cache-Control: no-store`。

| 接口 | 内容 | 数据从哪来 |
|---|---|---|
| `GET /key` | `label`（前缀、`...`、末四位）、`name`、`usage`（累计）、`usage_daily`、`usage_weekly`、`usage_monthly`（当前 UTC 日、周、月）、`limit`、`limit_remaining`、`limit_reset`、`expires_at`、`is_free_tier`（恒 false）、`is_management_key`（恒 false）；没有上限的 key，`limit`、`limit_remaining`、`limit_reset` 是 null，不重置的上限 `limit_reset` 也是 null | `Admission.SpendLimit()`；`billing.ReadKeyUsage`：`billing.spend` 里 subject 是这把 key 的行（工作区的行另算，不相加）；`accounts.GetKeyInfo` |
| `GET /credits` | `total_credits`、`total_usage` | `billing.ReadFunds`；工作区列表来自 `accounts.AccountWorkspaces` |
| `GET /dashboard/billing/subscription` | `hard_limit_usd`、`soft_limit_usd`、`system_hard_limit_usd`，`has_payment_method` 恒 false，`access_until` 0 | 同上 |
| `GET /dashboard/billing/usage` | `total_usage`（0.01 个单位）；`start_date`、`end_date` 不读 | 同上 |
| `GET /generation?id=` | `id`、`model`、`created_at`、`total_cost`、`currency`、`is_free`、`service_tier`、`app`、`latency`（首字，毫秒）、`generation_time`（全程，毫秒）、`finish_reason`、`error`、`usage`（计费项名到数量）、`tokens_prompt`（输入侧各项之和）、`tokens_completion`（输出侧各项之和，含推理）、`tokens_reasoning`、`tokens_cached`（缓存读） | `observability.GetOwn`；`OwnCall` 没有成本、端点、路由，所以响应里也没有；也没有 `api_type`：调用记录不存调用格式，`request_class` 是请求类别，不是格式，借来填会误导客户端，待记录存格式后再加 |

- **金额**：本站记账单位的额度，十亿分之一额度除以 10⁹ 显示（`float64` 对一个人持有的余额足够精确，只用于显示）；币种只在 `generation` 里带，OpenRouter 的 `key`、`credits` 没有币种字段。
- **余额类字段**：`balance` 取余额行（账本的合计，在途预留不扣：预留是还没发生的收费，OpenRouter 的 `credits` 同样不扣），`usage` 取账户**所有工作区**的 `billing.spend.spent` 之和（含已删除的工作区；key 的计数器记的是同样的收费，不相加），`total_credits` = `balance` + `usage`，`total_usage` = `usage`，旧版接口的 `hard_limit_usd` = `total_credits`、`total_usage`（0.01 单位）= `usage` × 100，所以两种工具算出的余额都是 `balance`。不直接对账本求和：账户每次调用一行分录，轮询的接口要扫全部，而 `spend` 的行数只随天数和工作区数增长。今天动余额的只有进账（赠送）和收费，所以这个和就是累计进账；退款、额度过期加入时，它们也动余额但不是收费，`total_credits` 要改成另记的累计进账，那时再改这一处。`start_date`、`end_date` 不读，永远是累计。没有余额行的账户全是 0。
- **`key` 的用量与上限**：`usage*` 只算已收的（`spent`）；`limit_remaining` = 上限 − 上限窗口里的（已收 + 在途预留），与准入拒绝的依据相同，不会为负。窗口按该 key 的 `limit_reset`、数据库时钟的 UTC 日、周、月；不重置的上限算全部日子。
- **`generation` 的来源与缺口**：只读调用记录。记录由后台批量写，请求结束后至多约 200 ms 才可查，此前（和不存在、别的账户的）都是 404 `generation_not_found`，客户端稍后重试；没有账本兜底：账本（`billing.transactions`）只记金额，不记用量和价格版本，所以不提供精简版，它的内容要等调用记录的写入方（G10）和账本一起定。编号不是 UUID 或缺失是 400。`GetOwn` 按主键在各月分区里查，每个分区一次索引探查，不在调用路径上。
- **`count_tokens`**（`POST /messages/count_tokens`）：Anthropic 的格式，返回 `{"input_tokens": n}`。**不调上游**，所以 n 是估算，口径固定为：`⌈TextBytes / 3⌉ + 图片数 × 1600 + 文件数 × 3000`（粗估，不查模型的页数上限；`billing.InputBound` 才按模型的页数界定上限）。`TextBytes` 是 `protocol.Inspect` 读出的请求体字节数减去图片、文件的数据（含 JSON 的键和引号，不去除）；每三字节一个 token 是经验比例：英文约四字节一个，所以文本通常偏高，但不是保证的上界（字节多、token 更密的文本会低估）；图片取一张最多约 1,600 个 token（上游会把大图缩到这个量级），是上界；文件的页数不解码就不知道，取固定的 3000，页数多的文件会被低估。因此不承诺“只偏早、不超窗口”：带大文件的请求，按这个数裁剪的客户端（Claude Code 用它定压缩时机）可能偏晚。以后经上游的 `count_tokens` 转发时才有精确值。不读 `max_tokens`，不要求它。它不预留、不收费、不写调用记录；前置检查同调用：IP 在途、认 key、账户在途名额、请求体按内存预算、key 的计数请求速率（单独计数，键 `count:`+key，超过 `CountRate` 回 429，不占用也不受 `KeyRate` 影响），然后 `Admit` 确认 key 可用。**模型**必须存在（404 `model_not_found`），且 key 可用（`Admission.Models()`，用 `allowedModels`，与路由同一逻辑）；不可用的同样回 404 `model_not_found`，不回 403。要精确计数，以后可经上游的 `count_tokens` 转发（G4 之后），这里的估算作为没有上游时的回答。
- **测试**：`readonly_test.go`（rig，真实数据库：成功、无效 key、无权限类 key、跨账户读取、不存在、计数口径、额度窗口）；`billing/read_test.go`（窗口边缘、行级安全）。

## 尝试

- 逐条目：先查本地持有者（`billing.Alive`：上次心跳成功 + TTL − 5s 晚于现在），失效就不再发起尝试、按「结束与结算」收尾。`Acquire`（内存比较交换）→ `Prepare`；拿不到凭证或 `*NotSupported` 就跳过，不算尝试，写进路由记录。全部因 `*NotSupported` 跳过：`Settle` 只释放，400 附原因；全部因凭证名额跳过：`Settle` 只释放，429、`Retry-After: 1`。
- **尝试的身份在进入 `Send` 时确定**：本模块按已有的尝试数分配序号（从 1 起，最多 `platform.MaxAttempts` = 3），`Hold.Bind(entry)` 把 `Meter` 绑到准入时报过价的条目（绑被删的条目会 panic：路线已按保留的条目裁剪，所以不会发生），开始时间（`priced_at`）在 `Acquire` 时记下。此后不论结果都算一次尝试，计入次数和重试预算。失败的尝试是**确知未执行**（用量 0）还是**结果未知**，取 `Failure.Executed`，判定规则见 [suppliers 的「调用面」](../suppliers/README.md#调用面)。
- 尝试结束时 `result` 把它的用量、成本、回执（供应商的请求 ID 所在的 `AttemptReceipt`）和结束时刻填进 `billing.AttemptResult`，结算一次写进 suppliers，与账本同一次提交；没有别的地方记「这次尝试到过供应商」（[suppliers 的「没有对账日志」](../suppliers/README.md#没有对账日志)）。
- 非 2xx 由适配器归为 `Failure`，`routing.Decide` 定换条目、换凭证还是返回。端点或供应商范围的限流、余额不足以外的供应商范围失败由 `Router.Block` 暂停到上游给的时间（没给用 `Config.Pause`，默认 5 秒；最多两小时）；供应商范围的失败还让本次调用跳过该供应商剩下的全部条目。余额不足的自动暂停归 suppliers：路由读同步原生余额与成本计划做付得起过滤，`Acquire` 再查整家暂停，新正余额同步或成功检测恢复；节点的一秒后台循环经 `RefreshBalances` 合并并限速请求余额同步。每个条目在 `Acquire` 前再查一次 `Router.Blocked`：规划之后别的请求可能已经暂停了它，这样跳过不算尝试、不花重试预算。2xx 后暂存开头事件，第一段正文、推理或工具调用到达即提交。提交前出错、空闲超时、200 无正文都换条目，换前取消当前上游。
- 回退不追加预留：准入已按保留条目的最高报价预留。客户端断开后不再发起新尝试。

## 尝试与转发

G4、G5 的代码结构。`Executor`（`NewExecutor(ExecDeps)`，依赖缺一个就不启动）的 `Run` 正是 `Deps.Run`，拿走准入后的 `Call`，把它做到最后一个字节并结算。文件：`exec.go`（`Executor`、`session`、收尾）、`attempt.go`（尝试循环）、`forward.go`（提交点之后的转发），G6、G7 的在下一节。`session` 是一次调用的运行，归运行它的 goroutine；与它共享状态的只有保活计时器和取消尝试的函数，都经写锁。

**循环**（`session.attempts`），每个条目一次 `try`，直到有一个应答、没有条目、或调用被别的原因结束：

| 步 | 做什么 | 结果 |
|---|---|---|
| 开始前 | 客户端已走、持有者可能已失效（`Alive`）、单次调用最长到了、尝试已满 `min(Plan.MaxAttempts, 3)`、过了 `Plan.StartBy` | 依次：什么都不写；503 `overloaded`；408；502（有过尝试）；有过尝试 502，没有 408 |
| `Acquire` | `ErrNoSlot` / `ErrNoEndpoint` / `ErrSupplierResting` | 跳过，不算尝试；前两种记录 `Full`，余额暂停记录 `SupplierBalance`。无尝试且含不可用端点时回 503，只有凭证满载时回 429 |
| `Prepare` | `*NotSupported`；`ErrInvalid`；其他 | 跳过，记 `FormatNotServed`；400 照客户端的错；503 并记 error |
| 重试预算 | 第一次尝试 `Router.Sent`；之后 `Router.TakeRetry`，用完 | 跳过，记 `RetryBudget` |
| 内存 | `Prepare` 之前先按 `protocol.PrepareCost` 向节点预算申请它最多同时占用的量，`Prepare` 之后只留结果缓冲的容量，`Send` 返回即退还 | 申请不到：不调 `Prepare`，503 |
| `bind` | 序号、`Hold.Bind(Kept[i])`、`Router.Begin` | 此后算一次尝试 |
| `send` | `Send`（带白名单头，会话值已派生，见「头白名单」），`HeaderWait` 的计时器到点取消尝试 | 失败：`Executed` 定 `NotExecuted` 或 `Unknown`；`RateLimited` 通知 `Router.Throttled`；`routing.Decide`：换条目 / 同条目再来（再 `Acquire`，也算一次尝试）/ 400 `invalid_request` |
| `stream` | `NewRelay`、读到结束 | 见下 |

没有任何条目可试时（`exhausted`）：有过尝试是 502 `upstream_error`；否则有凭证名额被占的是 429（`Retry-After: 1`），有条目不在 suppliers 里的是 503，其余是 400 附 `NotSupported` 的原因。原因里「规格没说」的情形（`NotSupported.Detail` 非空）对客户端只说「模型暂不支持 X」，缺哪个键写进日志（warn，带供应商和端点），不出节点。

**转发**（`attempt.stream`，所有者 goroutine 的读循环）：每次 `Relay.Next` 之后，按步的种类写：`StepCommit` 是提交点（写 200、`Content-Type: text/event-stream`、`Cache-Control: no-cache`、`X-Accel-Buffering: no`、上游这次响应带的 `X-Codex-Turn-State` 和暂存的开头，起保活计时器），`StepData`、`StepHeartbeat` 照写，`StepUpstreamDone` 结束。空闲计时从响应头到达起，遇到“新数据”就重置：提交点、`StepData`、正文字节或搜索数变化、一个事件的字节在到（`PartialBytes` 增长）；心跳、注释、没有内容的完整事件都不算（`TestAnUpstreamThatSaysNothingNewBeforeTheCommitPointIsTriedElsewhere`）。只有作答的那次尝试的上游能写这个状态头：它随尝试记下，换端点时跟着有内容的那一次走，客户端自己发的值从不回显（`TestTheTurnStateComesBackFromTheUpstream`）。

读结束的各种情况（`broke`、`complete`）：

| 情况 | 提交前 | 提交后 |
|---|---|---|
| 上游断流、格式出错、空闲超时 | 取消并换条目（空闲超时的原因是 `errIdle`）；结局 `FailedBeforeCommit` | 以 `Fail` 结束：`upstream_error` / `timeout`；结局 `Committed`，`FinishError` / `FinishTimeout` |
| 上游在流里报错（出错事件、`response.failed`） | 按 `protocol.FaultOf` 分：请求本身的（上下文过长、内容被拒、请求不合法）400 `invalid_request`，带上游的 `code`、`param` 和原话，不再换条目；其余换条目 | 请求本身的以 `invalid_request` 结束（上游的 `code`、`param` 和原话），其余以 `upstream_error` 结束；结局 `Committed`，`FinishError` |
| 200 无正文 | 换条目 | — |
| 单次调用最长 | 408，不再换 | `timeout` 结束，`FinishTimeout` |
| `ErrMemory` | 503，不再换 | `overloaded` 结束 |
| 客户端已走 | 不写，不再换；传递取消的端点已被取消，其余读到结束 | 同左，结局 `FinishCancelled` |
| 上游正常结束 | — | `Settle` 后 `Finish`；结算写不进则 `Final{Pending}` |
| 追加没批准 | 截断：回 402 / 503，不再换 | 截断：以 `credits_insufficient` / `billing_unavailable` 结束 |

`classify` 决定答复、结束原因和是否重试。请求本身的那几类不换条目，理由与 `send` 阶段的 `ScopeRequest` 相同；已生成内容的聚合回复直接作为失败回复交回，保留 Chat、Responses 的部分内容，Messages 仍只给 502 错误体。上游原话经过 `Conn.Sanitize`：协议取出有界的 `code`、`type`、`message`、`param`，去掉供应商身份仍是 suppliers 的事，网关不认识凭证和端点地址。失败事件立即返回；节点期限、内存和客户端取消仍优先按本地原因结束。`brokeAggregate` 以 `requestRefusals` 识别整个请求拒绝家族，不能只按旧的 invalid_request 判定，否则新增类型会丢掉已经生成的聚合内容。HTTP 和流内的拒绝都经 `requestError`：保留 context_length_exceeded、permission_denied、content_policy_violation、payload_too_large 的统一分类与状态，普通参数错误仍为 invalid_request。HTTP 优先供应商原因，流内优先 Fault；已知 code 再细分，凭证范围的失败不进入此路径。quality 照旧按 `protocol.FaultOf` 判断健康。`TestStreamFailureMeaningAndUsage` 覆盖七条转发路径的提交前后、聚合与流式、usage、错误范围和回退；`TestStreamRefusalMatchesHTTP400` 核对 HTTP 400 与 SSE 的脱敏原话及错误字段一致。

**收尾**（`session.conclude`）只在这一处结算，所以每条出口恰好一次：运行 panic 时 `settleOnPanic` 以已有的尝试结算再让 panic 继续。尝试在 `attempt` 返回时已 `close`：取消它的 context、还凭证的名额和端点的在途数，按用量和回执（有上游的请求号）写好它的 `billing.AttemptResult`；`Settle` 之后才写结尾（I3）；提交前的错误用 `protocol.RenderError`，收了费的带上用量和费用（`axiom` 对象），没收费的不带。

**不要流的客户端**（`!Request.Stream`）走同一条收尾路径，只是答案是一整份响应体：见下文「非流式聚合与 compact」。

**接缝**（没有生产路径上的 stub；全部由 `cmd/axiom` 的 `callpath.go` 接上）：

- `Costs`：尝试的成本，按这次尝试的价格币种计的十亿分之一额度，以及供应商单位下的估算（同一次定价）；生产实现是 `*suppliers.Service` 的 `Credits`（[suppliers 的「调用面」](../suppliers/README.md#调用面)），接线时把服务直接交给 `ExecDeps.Costs`，不加包装。`costs_test.go` 用真实的服务和账本跑通一次调用：账上是成本计划 × 单位价值，这次尝试的成本就是那个已知值（`TestTheAttemptCostIsTheSuppliersKnownCost`，同时核对 `billing.waivers` 里一行也没有），其余用例仍用固定值。
- `Alive` 是 `billing.Alive`，`Upstreams` 是 `NewUpstreams(*suppliers.Service)`，`Ledger` 是 `*billing.Billing`；billing 的 `Suppliers` 是 suppliers 自己的 `CallWrites`，节点直接把它交给 `billing.Start`：结算事务里的写入因此落进 suppliers 的表，与账本同成同败。
- `Endpoint.Target` 由 `route.go` 的 `RouteDeps` 填：`Model` 是端点的 `upstream_name`，`Streams` 是规格与供应商策略的交集；其余改写项（思考、加密推理、对话中途的系统消息、采样、缓存断点）原样取自规格的 `Model.Dialect()`（`targetOf`），规格没写的键留作未设置，不在这里补默认，由 `protocol` 按请求是否要用来决定（见 protocol README 的「`Target` 里的模型数据」）。生产装配的验收是 `route_target_test.go`：真实目录、`Build`、`Plan` 和 `Prepare` 走一遍，不手工构造 `Target`。`ExecDeps.IdentitySecret` 由节点从 `AXIOM_IDENTITY_KEY_FILE` 读出，所有节点必须相同，否则会话的上游缓存换节点就断。
- `ExecDeps.Records` 是调用记录（`*observability.Recorder`）：每次调用在结算写成之后入队一条，金额用账本实收的数，所以记录与账本一致；结算在后台重试的，成功时才记。记录是观测，入队不等写库，队列满了丢弃并计数，调用从不因它失败。是否免单取结算里已算好的 `Final.Waived`（只由三条免单规则决定），不是“实收为 0”；客户端应用在请求到达时从 `User-Agent` 认（Claude Code 记 `claude-code`，Codex 记 `codex`，其余为空）；来源类别一律 `relay`（`route.go` 给每个端点的类别）。网关不知道的字段（客户端自己的会话与子 agent 标识、语言、终端用户）留空不猜，用户协议版本写 `none`（还没有协议可记，见 observability 的「用户同意」）、类别一律 `chat`，首字时间还没有量。映射在 `record.go`。`Records` 为空时调用不记录，账本测试因此不需要记录器。
- 响应头等待和空闲超时“按端点、按模型可调”还没有：routing 的 `Endpoint` 没有这两项，现在全站一个值（`Config`）。
- `ExecDeps.Quality` 是 `*quality.Monitor`，与 `RouteDeps.Quality` 是同一个：每次尝试在 `close` 里把它怎么结束的（`quality.Observation`：适配器的失败、流读失败的 `protocol.Fault`、`cutOf` 从 context 原因读出的超时或我们自己的原因、客户端是否已走、是否正常结束或 200 无正文、预估输入、结算用的输出 token、发出和首段内容的时刻）报一次，判定在 quality（[internal/quality/README.md](../quality/README.md)）。
- 结尾的结束原因：`Relay` 不交出上游的 `stop_reason`，正常结束一律记 `FinishStop`；零输出免单要的“结束原因为空”因此只有出错的情形，protocol 补上后再接；零输出这条规则本身没有上限，连续多少次都按它免。

**路由快照**由 `RouteDeps`（`route.go`）从供应商的端点、目录的规格、站点的售价组装，规则见[路由的「路由快照从哪来」](../../docs/domains/routing.md#路由快照从哪来)。`Build` 是纯映射（一次读完所有来源，没有售价或规格的模型整个排除并记一条带原因的日志），`Publish` 替换路由的快照并通知节点，`Run` 启动后每 5 秒重建一次。健康度、吞吐和首字延迟取 `RouteDeps.Quality`（`quality.Monitor.Measured`，重建时的值，没测过的吞吐和延迟为 0），质量和置信度是 1，都只在 `endpointOf` 这一处赋。服务端工具的默认次数从 `Model.ToolUses()` 拷入 `Endpoint.ToolUses`，报价经 `Entry.Limits.MaxUses` 读取；未声明档位的缺项同样经 `UnknownItems` 标记端点并由下一次快照排除。每个端点的服务档是规格里的档加标准档，计费按端点实际提供的档报价（`billing.Model.ServiceTiers`），所以售价表只有标准档的模型也能接下没写档位的请求。

## 计量、追加与结算

G6、G7 的代码结构。文件：`meter.go`（计量、追加、截断的错误）、`settle.go`（结算的重试与交接、尝试的结局与暂估）、`background.go`（活得比请求长的工作：执行中的调用、后台结算、`SettleOrphan`）。依赖：`ExecDeps.Ledger`（`Extend`、`Settle`，即 `*billing.Billing`）、`ExecDeps.Costs`；每次追加和结算先过账户闸的完成类（`withFunds`）。

**计量**（`attempt.metered`，读循环里每次 `Next` 之后、写这一步之前，提交前后都一样）：把 `Relay.Progress()` 交给 `Meter.Observe`。

| `Observe` 说 | 做什么 |
|---|---|
| `Extend` | `extend` 在自己的 goroutine 里以 `SettleContext`（不随客户端取消，`ExtendWait` 1s，含等闸）调 `Ledger.Extend`，结果进容量 1 的通道；所有者每读一次先不等待地取一次（`landed(false)`），取到就 `Meter.Extended` |
| `Exhausted`，或 `Left` 为 0 | 停读：停空闲计时，等在途的那次（`Left` 为 0 只在有追加到期或在途时出现：`Observe` 在没有在途时总会再请求；输出免费的条目 `Left` 不设界，所以不会为一次不会来的追加空等），批准就以同一进度再计量一次（新段可能仍不够这次读），否则截断；这一步是上游正常结束时不等、也不因先前失败的追加截断，读已经完了 |
| `Left` | 这一步之后写出内容的期限 = min(`WriteTimeout`, `Left` ÷ 条目的最快输出速度)；全额预留或已覆盖到输出上限时 `Left` 是 `math.MaxInt64`，即 `WriteTimeout`。不再读上游之后（结尾、截断的错误事件）的写用 `WriteTimeout`：上游已停，阻塞的写不再让它多生成 |

- **任何一次追加失败都截断**，不等到当前段用完，也不等上游再发数据：追加的 goroutine 失败时，除了把结果放进通道，还以 `*extendFailed`（带着失败）为原因取消尝试，阻塞在 `Relay.Next` 里的所有者因此立刻醒来，`broke` 认出这个原因，按它的失败走 `truncate`，所以一个追加失败后上游完全静默的流，立即以 `billing_unavailable` 结束，不是等 180s 的空闲超时再报 `timeout`。`*Insufficient` 截断为 `credits_insufficient`（带 `limit_source`，会重置的上限在提交前的回答带 `Retry-After: 60`），其余（数据库错误、超时、`ErrTerminated`）为 `billing_unavailable`。截断（`attempt.truncate`）取消上游（原因 `errCut`），结束原因 `FinishTruncated`，花掉这一段的那一步不写；提交后以 `Relay.Fail` 结束，提交前没有内容交到客户端，以错误码回答（402 / 503），结局 `FailedBeforeCommit`，所以只收按次项目，不再换条目：别的条目花的是同一个账户。
- **结算前没有追加在途**（billing 的前提）：`stream` 退出时（含出错、panic）`defer` 等在途的那次返回，至多 `ExtendWait`。

**尝试的结局**（`attempt.result`，在 `close` 里）：回执、结束时刻、成本一并记进 `AttemptResult`，结算据此在 suppliers 写这次尝试的成本行；确知未执行的没有用量、成本 0（行里记完整、0）；其余按 `estimated` 补齐上游没报告的项再交 `Costs` 定价：`Credits` 一次给出十亿分之一额度和供应商单位下的估算（后者写进 `attempt_costs.estimated_native`），币种取这次尝试条目的价格币种。成本是平台自己的，定不出就记 0 并记 error 日志：不影响向用户收费，M1 也不再按供应商记录改正。供应商那一行的完整度标记（`attempt_costs.cost_flag`）由 `costFlag` 在这里一处定：上游一项用量都没报告的记 `no_usage`（行里的估算是我们猜的数，对账不拿它判价格），报告了一部分、其余是估的（或有项没价格）记 `unpriced`，定价出错同样记 `unpriced`（零不是"已知为零"），其余才是 `complete`；对账只对 `complete` 的估算判断价格对不对。`estimated` 只动回复那一段：输入侧一项都没报告时输入取首次采样的输入，其中文本按该条目模型族的分词器数出的 token（先 `Router.End` 释放端点在途计数，再以 `billing.NewTextCount` 数 `protocol.Text` 解码的字符串值；同一会话每族只数一次并缓存成功或失败，计数不按首个条目的上下文截断，以便复用于更大窗口。最多解码/分词 256 KiB 普通字符串，余下字符串的原始字节按 4 字节 1 个 token；不透明媒体、签名和加密内容不入分词器。未知族或计数失败沿用字节估计），每张图片 1600、每个文件 4096 个 token，模型数据里的单张图片、单个文件上界更小时取上界（`billing.InputEstimate` 算，合计不超过准入的上界 `billing.InputBound`：预留仍按上界，两者分开，所以带图片的请求被取消时不会按整个上下文收费），输出没报告最终值时取 max(已报告的下界, min(读到的正文、推理和工具参数的字节 ÷ 3 向上取整, 本次尝试的输出上限))（上限是计量器自己用的那个值，`Meter.OutputCap`：条目的输出上限乘选择数；推理算在上限里，已报告的下界高于上限时照报告），搜索没报告时取事件数；报告了的原样保留，billing 据"有数但没报告"标暂估。比例的口径和理由见[计费的「用量来源与暂估」](../../docs/domains/billing.md#用量来源与暂估)：比例和上限都在 `estimated` 里，不会超过预留覆盖的部分（`TestAnEstimateIsNeverChargedBeyondTheReservation` 用真实账本核对）；标暂估只说明这次收费建立在估算上，M1 没有校正它的后续步骤。

**结算**（`Executor.settle`，前台与后台共用）：同一个 `billing.Outcome` 反复交给 `Ledger.Settle`，每次 2s，间隔 50ms 起翻倍到 2s（`background.retry`）；确认丢失后的重试拿回 billing 存的 `Final`，不补扣。前台试满 `SettleRetry`（10s）：写进，结尾带费用；没写进，内容完整的以 `Final{Pending}` 结束（`billing_pending`），其余结尾不带费用。之后（`Executor.settled`）：

| 结算 | 名额 |
|---|---|
| 写进了 | `Run` 返回时归还 |
| 暂时性错误 | 后台以同一结局重试到 `SettleBackground`，写进就在后台归还，放弃则同下一行 |
| 永久性错误（`permanent`：`ErrNoAttempt`、找不到预留行、SQLSTATE 22/23/42 类），或后台放弃 | 记 error，交给清理：`Run` 返回时归还，清理按预留的截止时间接管并记丢失的请求（[suppliers 的「没有对账日志」](../suppliers/README.md#没有对账日志)） |

**优雅退出**（`Executor.Drain(ctx, grace)`）：等执行中的调用和后台结算，到 `ctx` 结束；然后取消全部尝试（每个尝试的 context 经 `context.AfterFunc` 挂在执行器的 `shutdown` 上，原因 `errShutdown`，不论端点是否传递取消），不再发起新尝试（408），提交后的流以 `timeout` 结束、按已有用量结算；再给 `grace`（cmd 的 drainWait 15 秒之后给 abortGrace 5 秒）；仍在重试的后台结算停下交给清理。计数用锁而不是 `sync.WaitGroup`：退出等待期间可能有调用从零开始，`WaitGroup` 不允许。`Handler.Drain` 的 `SettleOrphan` 用同一个 `background`。

## 计量与追加

- **计量覆盖每次尝试的全部阶段**：从读到上游第一个字节起，含提交前暂存、聚合、断开后读完。每次 `Relay.Next` 返回后、写客户端之前，把 `Relay.Progress()` 复制给纯内存的 `Meter.Observe`。每次尝试一个 `Meter`，彼此独立；预留由请求共享。输出上界怎么算见 [billing 的「接口」](../billing/README.md#接口草图)。
- **所有权**：`Meter` 只归所有者 goroutine。`Observe` 返回 `ExtendNow(req)` 时，`req` 是这一刻的不可变快照，`Meter` 进入"追加中"，在途期间不再返回它。所有者在另一个 goroutine 里以新建的、1s 期限、不随 `clientCtx` 取消的 context 调 `Extend(ctx, h, req)`，结果经通道送回，所有者在两次 `Next` 之间取走、调 `Meter.Extended(seq, err)`。后台的 `Extend` 从不读 `Meter`。每个请求最多一个追加在途；停用 key 不中断在途请求。
- **何时追加**：当前段用掉 40%–60% 时，比例每个请求随机取一次，避免同时开始的请求一起追加。
- **写阻塞**：可分段的尝试，客户端单次写的期限 = min(10s, 当前段余量 ÷ 模型最快输出速度)，到期按断开处理，所以停读期间上游的生成也不越过当前段加超额额度。
- **当前段先用完**（`Observe` 返回 `Exhausted`）：停止调 `Next`，空闲计时暂停，最多等追加 1s。`*Insufficient` 截断为 `credits_insufficient`；数据库失败、超时、`ErrTerminated` 截断为 `billing_unavailable`。截断 = 取消上游 → 等 `Next` 返回 → `Settle` → `Relay.Fail`。
- 达到用户设的输出上限是正常结束（`length`、`max_tokens`、`response.incomplete`），不是截断。

## 结束与结算

- 收费规则见[计费的「免单规则」](../../docs/domains/billing.md#免单规则)；本模块把每次尝试的结局（`NotExecuted`、`Unknown`、`FailedBeforeCommit`、`Aggregated`、`Committed`）和是否客户端取消交给 `Settle`。
- 上游正常结束时 Relay 扣住成功结尾。等追加在途的结束后，以 `settleCtx` 调 `Settle`，提交后 `Relay.Finish(final)`。截断、提交后出错、超时：先 `Settle` 再 `Relay.Fail`；提交前全部失败：先 `Settle` 再返回错误码。
- **结算写不进**：暂时性错误以同一幂等键退避重试满 10s。仍失败：内容完整的照常结束、不带费用，结尾带 `axiom.billing_pending`；只有截断的流发 `billing_unavailable`。之后由原执行器带准确用量继续重试到预留截止时间前，期间占着名额，所以待结算数有界；永久性错误（违反约束、范围被拒）立即停止重试、告警，交给清理，预留由截止时间清理接管；交给清理时名额何时释放见「并发、背压与共享状态」。处理函数 `recover` 之后同样走 `Settle`。

## 客户端断开

三个 context：`clientCtx`（请求的 context）；`attemptCtx`（`AttemptContext`），带单次调用最长时间，只对传递取消的端点还跟随 `clientCtx`，再加一层 `WithCancelCause`，空闲计时、等响应头超时、写失败各以自己的原因取消它，`context.Cause` 据此认出读为什么结束；`settleCtx`（`SettleContext`，单次 2s），不跟随取消、自带期限，保留 `clientCtx` 里的值。取消的传递：`clientCtx` → `attemptCtx` → 关闭上游响应体 → `Next` 返回。用户看到的四种情况见[接入的「用量与费用」](../../docs/domains/gateway.md#用量与费用)；代码上：

- 排队中：停止等待，什么都不写（`admit`）。已预留、未发上游：每次尝试之前看 `clientCtx`，客户端已走则不发，`Settle` 的结局没有尝试，整笔预留释放。收费的那次尝试若不是因客户端离开而结束（结局不是 `FinishCancelled`），之后客户端才走也不改变它：失败的尝试照免模型用量。
- 端点传递取消：`attemptCtx` 随 `clientCtx` 取消，`Next` 返回，已累计用量（`Relay.Usage()`，缺项由 billing 估算标暂估）交 `Settle`，结局 `FinishCancelled`；之后不写。
- 端点不传递取消（不论是否已提交）：`attemptCtx` 与 `clientCtx` 脱钩，读完上游不再转发（没提交的尝试不提交，结局 `FailedBeforeCommit`），仍受空闲计时和最长时间约束、仍占名额；读也超时就取消、按暂估结算。
- 向客户端写失败（写期限到或连接已断，`WriteTimeout`，默认 10s）按客户端已走处理：之后不再写，传递取消的端点当即取消，其余照上一条。`clientCtx` 已结束的客户端同样不再写，哪怕连接还能写。
- 取消不直接动钱，预留只经 `Settle` 或清理落到终态。

## Relay 的所有权

- 一个请求一个所有者 goroutine：只有它调 `Relay` 的方法，同一时刻至多一个 `Next` 在读；`Next`、`Finish`、`Fail` 返回的切片在下一次调用 `Relay` 前有效，所有者写完才再调 `Next`。
- 写客户端经一把写锁（`session.wmu`），写锁还守着“已开始”“客户端已走”“已收尾”三个标志：提交后的保活由计时器在写锁下写 `protocol.KeepAlive(format)` 的预生成字节，不碰 `Relay`；每次写都在写期限内并推迟保活，收尾时先停保活再写结尾，之后不再写。空闲计时到期只取消 `attemptCtx`。
- 收尾顺序固定：取消 `attemptCtx` → 等 `Next` 返回 → 所有者调 `Settle` → `Finish` 或 `Fail`。
- 不变量：调用拿到的每个 `Relay` 在每条退出路径上都归还节点预算，恰好一次，panic 也不例外。所有权从 `NewRelay` 起就在会话：`stream` 创建时记入 `session.live`（读到一半 panic 也找得到它），`part` 里留着的永远就是 `live`（下一次尝试在创建 `Relay` 之前先放掉它），所以 `left` 转交时 `part` 清空也不会漏，`Run` 退出时 `release` 统一 `Release`。`Relay.Release` 第二次起什么都不做；先行结束的路径（`Finish`、`Fail`）不必管 `live`，而确定丢弃一个 `Relay` 的路径一律经 `session.drop`（`Fail`、`Release` 做完才清 `live`）：丢弃的 `Relay` 缓冲不能因会话还指着它而一直可达，否则退还的预算被下一次尝试再用时（比如等响应头），旧缓冲仍占着内存而没有计数。`drop` 中途 panic 时 `live` 还在，由 `Run` 退出的 `release` 兜底，兜底只有这一条。compact 回复的 `held` 同理：`readWhole` 边读边记入 `session.replyHeld`（读到一半 panic 也找得到），读失败或客户端已走时 `dropReply` 立即退还，否则 `release` 一并归还。
- 非流式客户端：`RelayOptions.Aggregate`，`Next` 不返回 `Out`，`Finish`、`Fail` 返回整个响应体，`Status()` 给它的状态码，没有内容时的失败是 `RenderError` 的答案，可以换端点（[protocol 的「非流式聚合」](../protocol/README.md#非流式聚合)）；每路输出预算 16 MiB；聚合内存申请不到（`ErrMemory`）是本地容量故障，按对外错误结束，不再回退。网关怎么用它见下文「非流式聚合与 compact」。

## 非流式聚合与 compact

G8 的代码结构，`forward.go`（聚合的出口）、`exec.go`（`answerAggregate`；compact 的回复在 `conclude` 里直接 `respond`）、`pass.go`（compact）。不要流的客户端仍以流调用上游、由网关聚合，compact 则上游不流式（[接入的「转发规则」](../../docs/domains/gateway.md#转发规则)），区别在答案怎么得到。

**不要流的客户端**（`stream` 不为 true）：`session.aggregate` 为真，`RelayOptions.Aggregate` 让 `Next` 不交出 `Out`，读、计量、追加、截断、结算都与流式相同（同一个 `Meter`/`Extend`/`Settle` 路径，客户端断开四种情况不变）。提交点仍由 `Relay` 在第一个内容事件上给出，但客户端什么都还没收到，所以 `commit` 只记下这次尝试有内容（`attempt.content`）和它上游的状态头，不写响应头。

- **可以换端点**：读到内容之后断流、空闲超时，把这次尝试的 `Relay` 留下来（`session.part`），按 `routing.Decide` 的 `NextEntry` 换下一条，下一次尝试开始时把上一条放掉，所以各次尝试的内容从不混在一起。没有条目可试、或 `mayStart` 不再放行时，`left` 把留下的那一个交回，由它作答：Chat、Responses 以 200 带上已生成的内容和错误，Messages 以 502 只带错误（`Relay.Fail` 按格式决定），尝试的结局记 `Aggregated`（内容随回复交到客户端）或 `FailedBeforeCommit`（Messages 的 502）。保留的那次尝试在 `Run` 退出时也一定放掉（`defer s.release()`，经 `let` 调 `drop`，即 `Fail` 再 `Release`），容量与节点预算随之归还；`left` 交回作答的那一个由 `session.live` 接着管（见「Relay 的所有权」）。
- **请求本身的失败**：上游在流里拒绝请求时，不再换条目。没有内容时按 400 作答；已有内容时由 `brokeAggregate` 交给 `left` 立即生成失败回复，内容与结局按上一条的格式规则保留。
- **本地原因不换端点的失败**：截断（预留追加不到）、节点内存不够（`ErrMemory`）、单次调用最长、客户端已走：没有内容交给客户端，`conclude` 直接以错误作答（402 / 503 / 408 / 不写），这次尝试记 `FailedBeforeCommit`，不再换条目——分别因为钱、本地容量和期限。
- **写出**：`answerAggregate` 在 `Settle` 之后调 `Finish` 或 `Fail`，拿到整个响应体和 `Status()`；答 200 时用 `respond` 按 64 KiB 一块写出，整个响应体就是 `Relay` 的缓冲，所以它占的节点预算在写完（或客户端已走、写不下去）之前不归还，`Run` 退出时经 `session.live` 的 `Relay.Release` 归还，`replyHeld` 归还 compact 回复的 `held` 与之一样（每块重设写期限，整份另有总期限，见「超时与限额」；`Content-Type: application/json`，状态码取自 `Status()`；上游那次响应的 `X-Codex-Turn-State` 也由 `respond` 写，compact 的回复走的正是它）。结算写不进时照 `Final{Pending: true}` 走，响应体带 `axiom.billing_pending`、不带费用，与流式相同；失败且费用为零时与 `answer` 一样不带 `axiom` 对象。
- **16 MiB**：每个聚合的回复最多持有 `aggregateLimit`（protocol 的「非流式聚合」），超出或节点预算不够都以 `ErrMemory` 结束，不换端点。

**`/responses/compact`**（`Call.Compact`）：上游不流式、回复原样透传、请求只做最小的同格式改写、一次预留足（[接入的「转发规则」](../../docs/domains/gateway.md#转发规则)）。

- 准入：`routing.Input.NoSegments` 让条目不可分段，预留按输出上限一次报足；这类调用读的是整份答复，所以端点不必能流式（路由对此例外）。`NativeOnly` 只对压缩请求设置：只有原生支持 Responses 的端点能服务它，生成请求允许转换。
- 请求：`protocol.Prepare` 按 `Target.Compact` 只改必须改的成员——模型名换成端点的名字，`user`、`safety_identifier`、`prompt_cache_key` 换成派生值，路由参数删除——`store`、`include`、输出上限、`input` 既不补也不改（[protocol 的「同格式改写」](../protocol/README.md#同格式改写)）；路径由 `suppliers.Call.Compact` 选（`/v1/responses/compact`，在 suppliers 的路径表里），等响应头的期限是 `CompactHeaderWait`（10 分钟，[接入的「路径与格式」](../../docs/domains/gateway.md#路径与格式)）。请求带 `stream: true` 的在准入前就回 400：这个端点只给整份回复。
- 回复：`readWhole` 一次读完，边读边向节点预算申请（上限 `compactLimit`，与聚合的 16 MiB 相同；扩容时新旧两块缓冲同时在，峰值见函数注释），每次读到字节就重置空闲计时；读完用 `protocol.UsageOf` 读出顶层 `usage` 作为这次尝试的用量，上游没报告的部分由结算暂估。读成功、客户端还在，就是这次调用的答案：`Settle` 之后 `conclude` 把上游的状态码和字节按 64 KiB 一块写出（每块一个写期限，整份的总期限见「超时与限额」）；客户端已走则不写，记 `FailedBeforeCommit`。客户端在结算之后、写出之前离开，或写到一半停读：不再写，结算与金额不变，只是调用记录仍按已读到的报告记 `Aggregated`（内容没送到，这类回复没有可报告的输出，不会因此免单）。读失败（断流、空闲、断开、内存）时什么都没写，按失败的作用范围换条目或直接以错误作答（`failedReply`，与流式提交前相同）。
- **费用**：透传的响应体里没有本站的费用字段，`Settle` 写不进时也不加 `axiom.billing_pending`（加了就不是原样）；费用只在调用记录里查。

## 超时与限额

用户遇到的期限（读请求体、排队、等响应头、数据空闲、`StartBy`、单次调用最长、尝试次数）见[接入的「路径与格式」](../../docs/domains/gateway.md#路径与格式)。内部的期限：

| 跳 | 默认 | 到期 |
|---|---|---|
| key 未命中查库 | 1s | 503 |
| 预检缓存（M2） | 20ms | 按缓存不可用 |
| 账户闸等待（`AccountGateWait`） | 5s，不计入下一行的 1s | 429 `rate_limited`，原因 `account_gate` |
| 准入名额加事务 | 连接和事务合计 1s（一个 context 期限，从取得账户闸起算，不含等闸）；`lock_timeout` 500ms、`statement_timeout` 800ms 只校验、暂不下发（见「入口与准入」） | 503，回滚无副作用 |
| 建连、TLS | 各 10s（egress） | 换条目 |
| 等上游的响应头（`HeaderWait`） | 60s；`/responses/compact` 是 `CompactHeaderWait`，10 分钟 | 取消这次尝试，换条目 |
| 上游没有新数据（`IdleTimeout`） | 180s | 提交前换条目，提交后以 `timeout` 结束 |
| 保活（`KeepAlive`） | 提交后 15s 没有写过就写一次 | — |
| 向客户端写一次（`WriteTimeout`） | 10s；可分段的取 min(10s, 当前段余量 ÷ 最快输出速度) | 按客户端已走处理 |
| 向客户端写一整份回复（`respond`） | 每块 64 KiB，期限取 min(10s, 距总期限还剩)；总期限取 max(`WriteTimeout`, 回复字节数 ÷ 64 KiB/s)，16 MiB 约 4 分 16 秒 | 到期不再写，按客户端已走处理（结算已提交，金额不变） |
| 追加（`ExtendWait`） | 1s，含等账户闸 | 截断 |
| 结算（`SettleRetry`、`SettleBackground`） | 单次 2s，退避重试 10s 后改为后台重试 20 分钟 | 「计量、追加与结算」 |
| 持有者 | TTL 60s，心跳 10s，回收宽限 60s | billing 的「持有者与截止时间」 |
| 预留截止时间 | 准入 + 单次调用最长 + 30 分钟结算宽限 | 清理接管 |
| 优雅退出 | `AXIOM_SHUTDOWN_TIMEOUT` 内排空监听，再等调用和结算 `drainWait`（15s）；到期取消全部上游，再给结算 `abortGrace`（5s） | 交给清理 |

- 默认值是后台配置的种子，保存时校验下列关系，不满足拒绝保存，启动时不满足不启动（`gateway.New` 先 `Config.Validate`，一次报出全部问题；数值都须为正；入口的数值是 `IPInFlight`、`MaxBody`、`KeyRate`、`CountRate`、`RateWindow`、`KeyLookup`，默认 500、32 MiB、每 key 每分钟 6000 次（调用）、每 key 每分钟 6000 次（计数）、1 分钟、1s）：`lock_timeout`、`statement_timeout` < 准入合计；排队 ≤ `StartBy` < 单次调用最长；响应头等待、空闲 < 单次调用最长，保活 < 空闲；TTL ≥ 3 × 心跳，宽限 ≥ TTL；结算宽限 > 结算的前台加后台重试（`SettleRetry` + `SettleBackground`，默认 10s + 20 分钟，给清理接管留约 10 分钟）；0 < 保留数 < N；每 key 等待者 ≤ 全局等待者；`RateWindow` 是整毫秒、1 秒到 24 小时（共享计数的窗口按毫秒从纪元算，键活两个窗口）。
- **限额**：每个请求最多 3 次尝试（含结果未知的），共享期限，换端点不重置时钟。重试预算按供应商、在进程内：1 分钟内重试不超过首次请求数的 10%，保底每分钟 5 次；用完跳过该供应商并写进路由记录。凭证范围的失败在同端点换凭证，也算一次尝试；疑似出口拦截在 M1 按端点处理并报告 quality。suppliers、egress 不暗中重试 POST。

## 并发、背压与共享状态

- **入口**：按 IP 的在途数上限（[安全的「防攻击」](../../docs/security.md#防攻击)）在一切之前；认 key 只看头，失败立即 401，未认证的连接从不读体、不占业务名额。
- **节点在途上限** 5000（含已断开、仍在读完的），超出 503 不排队；**按账户在途上限**默认节点名额的 10%（`InFlight.Acquire` 的 `accountMax` 为 0 时），可按账户调，超出 429；先取账户的，再取节点的，节点拒绝时账户的名额随即归还。按 IP 的连接数与在途数用同一个 `Limiter`（按键计数、到限即拒、不排队），数值是 G1 的后台配置。名额在请求执行期间和结算后台重试期间一直占着，只在两种情况下释放：`Settle` 提交；或交给清理（结算永久性错误，或到截止时间放弃重试），这时执行器已经停了上游、不会再重试结算，预留由清理按截止时间接管。待结算数因此受节点名额约束。
- **账户闸**（进程内，按账户）：同一账户的资金事务（准入、追加、结算、`SettleOrphan`）一次一个。`Funds.Admit` 和 `Funds.Complete` 先排闸、再取同类的连接，所以排队的事务不占连接；闸释放时直接交给最老的等待者，完成类（追加、结算、`SettleOrphan`）先于准入类。准入等闸超过 `AccountGateWait`（5s）回 429；完成类只受自己的 context 约束，因为它带的是已提交的钱。账户的表项只在闸被持有时存在，冷账户洪峰不留残余。等锁不再占连接，一个热点账户拖不垮其他租户；行锁仍是多核心下的最终保证。等闸的期限（`AccountGateWait`）决定一次突发里同一账户能排多长的队：闸每秒放行约 1 ÷ 持锁时间个准入，等 W 秒的队伍至多这个数乘 W。压测 #295 里持锁 50–100ms（共享的 CI 机器，比 `docs/performance.md` 估的 5ms 慢一个量级），200ms 只容 2–4 个，1 秒内每账户 20 个新请求就有六成回 429；5s 容 50–100 个，再多的由按账户在途上限（默认 500）截住。等闸的请求不占连接，只占在途名额和请求体的内存，客户端走了即退出。sub2api 对账号和用户并发名额也是有界等待加长期限（`maxConcurrencyWait` 30s），队列满或超时才回 429（`backend/internal/handler/gateway_helper.go`，2026-10-18）。
- **429 的原因**：节点回的每个 429 都按原因计入指标 `axiom.gateway.rate_limited`（标签 `reason`，Prometheus 里是 `axiom_gateway_rate_limited_total`），客户端看到的错误码都是 `rate_limited`，原因不发给客户端。`ip_in_flight`（按地址在途）、`account_in_flight`（按账户在途）、`key_rate`（每 key 调用频率）、`count_rate`（每 key 计数频率）、`account_gate`（等账户闸超时）、`queue_full`（排队总数满）、`queue_key_full`（一个 key 的排队数满）、`queue_wait`（排队期限到）、`credentials_busy`（路线上的凭证并发都占满）。新增会回 429 的限制必须有自己的原因；原因为空的计数说明有限制没分开。
- **内存**：每路 64 KiB 加节点预算 2 GiB，由 protocol 记账，请求体、转换（分配之前按上界申请，见「尝试与转发」）、暂存、聚合都计入。
- **连接池**：gateway 的准入类许可最多 N−R 个，完成类最多 R 个，分别计数；完成类占满或排队时，其他账户的准入仍能取满 N−R 个许可。完成类只在账户闸上优先，两个许可队列互不挤占。物理隔离由 [platform 的连接池](../platform/README.md#连接池)保证：普通池 N−R 条，完成池 R 条，默认 N=64、R=16。追加、结算、孤儿结算在进入 billing 前用 `pg.CompletionContext` 选择完成池。recorder、River、控制台即使不经 gateway 许可也只能使用普通池。冷 key 查找和只读 key 端点仍领取准入许可；`NewHandler` 检查 `Service.GatesLookups`，没配就不启动。类内先到先得，等待者不占连接，期限来自调用者 context。持有者心跳与回收不占任何一个池，走它自己的专用连接（[platform 的连接池](../platform/README.md#连接池)）。
- **排队**：只在候选端点都满载时，在事务之外；全局最多 256、每 key 最多 16 个等待者，满了 429；FIFO，每释放一个名额调一次 `Queue.Wake`，唤醒一个。调用者在看负载之前先取 `Queue.Epoch()` 并传给 `Wait`：看负载和入队之间释放的名额没有等待者可唤醒，`Epoch` 会记下这种释放，`Wait` 见到就直接返回让调用者重做，不会睡到 20 秒期限；有人在排队时的释放归队首，不计入，新来的不会插队。醒来时 context 已结束的等待者把唤醒转给下一个。排队期限是 `QueueWait`，从调用第一次排队起算一次，之后再排队共用剩余时间，到期 429；调用者的 context 结束则返回它的错误，什么都不写。
- **调用记录与观测**：`observability.Recorder.Enqueue(CallRecord)` 非阻塞，记录里带这次调用的每次尝试；队列满时丢掉新记录并计数，从不等待，所以在释放名额之前或之后调用都可以，规则见[记录与告警的「写入路径」](../../docs/domains/observability.md#写入路径)；`quality.Monitor.Observe` 同步更新固定大小的端点窗口，不分配、不访问数据库；观察带准入里的账户所有者与关闭回执里的凭证 ID。只有健康状态变化异步交接，缓冲满时推迟该变化、计数，已有变化不丢。试探、持久化与运行模式见 [quality](../quality/README.md)。`CallRecord` 只用 observability、catalog、protocol、accounts 的类型和跨层 ID，由本模块转换。
- **预检计数**：`RateCounter` 按键做滑动窗口计数（当前窗口加上一窗口按重叠比例折算的部分），只计数不判定，不需要释放；两张轮换的表使内存只与最近两个窗口出现过的键成正比。配了 Valkey 时（M2），预检把各维度的计数交给 `count` 脚本，一次往返，粘性的 `GET`/`DEL` 在同一批，`RateCounter` 照常计数作为兜底：Valkey 不可用（出错、超过 20ms、熔断打开）时，用进程内的计数与全额限额比较，不乘 N：故障期间一个 key 合计至多放大到 N 倍，但不会因 Valkey 抖动而误拒流量集中在一个核心的 key（[platform/kv 的设计](../platform/kv/README.md)）。`count_tokens` 的预检只有计数。
- **粘性**：排序第 2 位用预检取到的端点；成功端点不同时，结束后写回。收费那次尝试交付了内容（`Committed` 或 `Aggregated`），且它的端点不是预检取到的那个，就 `Stick(会话, 端点, 路线的粘性时间)`，时间取预设的 `StickyTTL`（`Plan.StickyTTL`），预设没设就用路由的默认值；一次尝试都没交付内容的调用什么都不写，也不忘记。在结算和结尾之前更新本地表或投递后台队列；异步写入可能晚于紧跟结尾的下一个请求，只损失粘性命中。M1 的粘性表在进程内，写一次是一次 map 写入，不另起协程；M2 配了 Valkey 后经 `kv` 的后台队列写，队列满就丢弃并计数，不等待。请求带 `x-claude-code-context-compacted` 时，读粘性前先 `Forget`，旧缓存已经作废；配了 Valkey 时这一步就是预检那一批里的 `DEL`，代替读取。Valkey 不可用时不粘、写入丢弃，不退回进程内的表。

| 共享状态 | 放在哪 | 理由 |
|---|---|---|
| 余额、预留、计数、账本、持有者 | PostgreSQL | 钱只在这里 |
| 限流计数、会话粘性 | M1 进程内；M2 Valkey，计数脚本与粘性读取同批、一次往返，期限 20ms；最近 10 秒内至少 20 次且过半不可用、至少 10 次且超过 20% 超时、或连续 5 次不可用即熔断 5s（调用方自己的取消不计入），之后放一次试探（半开）；不可用期间按本节点计数与全额限额比较、不粘；粘性在结束后经后台队列写回 | 丢了不影响钱；放库每个请求多一次写。细节见 [platform/kv 的设计](../platform/kv/README.md) |
| 端点负载、429 降权、凭证在途数与冷却、重试预算、账户闸 | 进程内；M2 后台每秒同步，凭证并发与有限账号 RPM 按存活核心静态分份额，完整成员确认及排空见 [suppliers](../suppliers/README.md#并发与速率) | 请求里不访问缓存；硬上限不依赖共享提示 |

缓存只在准入之前同步访问一次，规则见[路由的「容量感知」](../../docs/domains/routing.md#容量感知)。

## 故障矩阵

"钱守恒"：账本平衡、余额等于分录之和、不重复扣、收费不越底线；平台承担只出现在[计费](../../docs/domains/billing.md#预留与结算)列出的情况里，且有记录。

| 步 | 故障 | 结果与钱 |
|---|---|---|
| 1–5 | 节点崩溃；缓存不可用（M2） | 什么都没写；限流按本节点计数、无粘性 |
| 1 | 慢速请求体攻击 | 未认证的不读体；有 key 的受按 IP、按账户上限约束 |
| 6 | 数据库慢、锁等待 / 持有者失效 | 1s 内回滚 503 / 写入 0 行 503；都没有预留 |
| 6 | COMMIT 确认丢失 | 503 不发上游；`SettleOrphan` 锁余额行后释放或写墓碑；进程死了由清理释放 |
| 7–8 | 节点崩溃（已发出、结算前） | 回收后按[计费的「持有者与截止时间」](../../docs/domains/billing.md#持有者与截止时间)收尾：清理释放并记丢失的请求，带预留冻结的路线，不向用户收费；崩溃前那次尝试没有成本行，也没有保存对账键，成本由平台承担，由供应商的逐笔记录补齐 |
| 7 | 上游连不上、出错 | 换条目；失败尝试的成本在最后结算里记 |
| 8 | 追加时数据库慢 | 等满 1s 截断为 `billing_unavailable`；消耗不超过超额额度 |
| 9 | 结算失败或超时 | 内容完整的照常结束、不带费用；执行器继续重试，比较后再写只有一方生效，迟到方按 billing 的终态分支 |
| 9 | 结算已提交、结尾未写出就崩溃 | 收费有效；客户端重试是新请求 |
| 9 | 结算永久性错误 | 停止重试、告警，交给清理：名额随即释放，截止时间到由清理接管并记丢失的请求 |
| — | 数据库不可用短于 TTL + 宽限（120s） | 新准入 503；恢复后心跳续上，在途照常结算 |
| — | 数据库不可用达到 TTL + 宽限 | 持有者被回收，在途的追加得 `ErrTerminated` 截断；迟到的 `Settle` 按终态分支，钱不重复 |
| — | 主备切换 | 已确认的提交不丢，靠[运营的复制要求](../../docs/domains/operations.md#部署与节点管理)；切换演练在 G11 |
| 10 | 调用记录写不进 | 队列满即丢弃新记录并计数，不阻塞调用；崩溃丢最近一次成功写入之后的记录。记录不在时 `generation` 返回 404 `generation_not_found`，不提供账本精简版（见[「辅助接口」的 `generation`](#辅助接口)） |
| 暂估 | 上游没报告的用量、价目表缺的项、价目表定价不了的等级 | 按锁定价格和兜底价收，标暂估；预留里没用掉的部分随结算一起释放，没有后续校正 |
| 对账 | 某供应商持续缺逐笔记录 | 停止向它路由并告警 |
| 退出 | 优雅退出到期 | 取消全部上游（不传递取消的因此暂估），结算 `abortGrace`（5s），之后的交给回收 |

每个 HTTP 请求一个请求 ID，客户端重试是新请求。不承诺外部调用恰好一次：上游已执行而响应丢失的窗口消除不了，由对账兜底。

## 性能估算

预算见[性能的「延迟预算」](../../docs/performance.md#延迟预算)。下表全是估算（同机房 RTT 0.2–0.5ms、提交 fsync 约 1ms），G1 实测了组合扫描（表后），准入和结算的往返留给 G11。

| 环节 | 预算 | 本设计 | 估算 |
|---|---|---|---|
| 入口转核心 | 1ms | M1 同进程 | 达标 |
| 认 key、读请求、路由 | 2ms | 缓存命中 + `Inspect`（严格全扫并记录固定大小的位置表）+ `SessionKey`（有界前缀）+ 名字解析 + `Plan`；事务外的同格式 `Prepare` 复用位置，只解析要改的值、分段复制其余字节 | 普通请求沿用 2ms；大请求的读取与改写按[分级预算](../../docs/performance.md#延迟预算)，实测见下 |
| 限流和粘性 | 2ms | M1 为 0；M2 1 C | 达标 |
| 准入 | 5ms | 7 DB（含提交） | 约 3–5ms，待实测；RTT 1ms 即超标，核心与库必须同机房 |
| 出口发请求、首字节 | 2ms + 1ms | `Prepare` 在事务外，复用长连接 | 达标；转换单独基准 |
| 追加、结算 | 不在首字路径 | 追加 9 DB（已覆盖时 6），结算 9 DB（一次尝试） | 结算尾延迟目标 p95 10ms，单独报告 |

**读取与改写实测**（2026-10-28，Linux、共享 8 核 AMD EPYC；`BenchmarkInspect` + `BenchmarkPrepare`，1 MiB、4 MiB 各五轮的中位数相加，基线为优化前 merge base `06ee5f8e`；绝对耗时随共享负载变化）：

| 请求体 | 格式 | 优化前 | 复用位置后 | `Inspect` 分配 | `Prepare` 分配（前 → 后） |
|---|---|---|---|---|---|
| 1 MiB | Messages | 8.61ms | 5.06ms | 36 | 37 → 4 |
| 1 MiB | Responses | 6.20ms | 3.90ms | 29 | 30 → 4 |
| 4 MiB | Messages | 31.26ms | 18.76ms | 36 | 37 → 4 |
| 4 MiB | Responses | 25.23ms | 14.92ms | 29 | 30 → 4 |

`Inspect` 仍执行重复键、UTF-8 与深度检查；位置表只保存有限的顶层改写成员，不按消息存位置，所以不增加分配次数。`Prepare` 对未改写的内容只拷贝，响应历史也直接按已校验的值拼接；回放候选清理会改变位置，保留 tokenizer 回退。1 MiB 已由单次严格检查占去约 3–4ms，单靠去掉第二遍无法达到 2ms，因此采用性能文档的分级预算，而不取消重名检查。`BenchmarkRequestRead` 五轮中位数在 256 KiB 为 0.99–1.32ms，1 MiB 为 3.96–5.32ms，4 MiB 为 16.79–31.22ms，均在分级预算内；同一次 4 MiB Responses 有一轮达 35.81ms（112%），当时共享机器负载约 36，超过 8 核，按性能文档保留超标证据，不据此放宽预算。该基准按体积报告预算占比；`BenchmarkScan` 继续同时测有界的会话前缀，`TestScanAllocationsDoNotGrow` 保证组合分配不随对话长度增长。全调用的 30ms p95 目标仍由压测验证，不能用这张微基准表代替。

## PR 拆分

G1–G9 与节点接线（`cmd/axiom` 起齐循环、组装快照、挂 `/v1`）已落地。剩下两块，各约 400 行非生成代码，测试先行：

| # | 内容 | 依赖 |
|---|---|---|
| G10 | quality 观测、粘性写回（`Router.Stick`）、网关的指标和链路 | O1、O2 |
| G11 | 端到端：Claude Code、Codex、Chat SDK 经 new-api、sub2api；故障注入；主备切换；压测 | 全部 |

测试先行，至少包括：B 的读语句在 A 提交前开始（B2）；孤儿处理先于迟到的 COMMIT、回收与在途准入和追加交错、结算确认丢失后重试不补扣（B4、B5）；聚合与写阻塞时消耗不超超额额度（G6）；大事件越过当前段、事件持续到达却迟迟不结束都在读的过程中触发追加或停读（P5、G6）；冷 key 洪峰叠加热点账户结算（G2）；数据库中断 30s 与 150s（G11）。压测场景见[性能的「怎么验证」](../../docs/performance.md#怎么验证)。

## 实现时验证的前提

- Codex 固定版本经 HTTP 每轮发完整历史（G11；不成立就把续接提前到 M1）。
- `thinking.display: omitted` 时提交前几分钟无字节，Claude Code 能容忍（G11；不能就在提交前透传 `ping`）。
- 两家中转站的逐条记录 1 小时内可查（S9）。
- 联网搜索在流式 `message_delta` 里的累计输入（G11，决定输入上界能否收紧）。
- sub2api 是否回显客户端发来的 `X-Client-Request-Id`（S8；是则发请求前就知道对账键）。
- 锁、持有者栅栏与读计数按独立语句同批发送，语义见 billing 的「准入里的预留」，准入 7 次往返；读计数的快照仍在余额锁取得之后。

## 决策与理由

| 决策 | 理由 | 不选 |
|---|---|---|
| 请求 ID 即调用编号 | 一个身份贯穿日志、账本、记录 | 另生成调用编号 |
| 每个准入的请求恰好一次 `Settle`，只释放也走它 | 终结只有一个入口 | 单独的 `Release`：多一条漏记尝试成本的路径 |
| 预检只计数，判定在 `fn` | 以后速率值从库里读，判定位置不变 | 在预检里判定 |
| M1 共享状态在进程内，Valkey 到 M2；多核心没有 Valkey 不启动（`AXIOM_CORE_NODES` 大于 1 而没有 `AXIOM_VALKEY_URL`） | 进程内实现本就是缓存不可用时的兜底 | M1 就接 Valkey |
| M1 直接用 `platform/egress` | 一种实现不加抽象 | 先定义出口接口 |
| 准入后凭证竞争失败即释放并 429 | 竞争罕见 | 持钱等待：多一条排队路径 |
| 重试预算按比例并保底，按供应商、按进程 | 低流量时一次故障不至于封死回退 | 纯比例 |
| pg 的普通池和完成池共用可配置预算，gateway 保留账户闸和准入许可 | 控制台、recorder 和直接使用 pgxpool 的 River 都无法侵占完成类容量；预算按部署缩小 | 只在 gateway 发许可或只给 `Tx` 加信号量：其他事务入口或 River 可绕过 |
| 进程内账户闸，完成类优先 | 等锁不占连接、不拖别的租户 | 只靠行锁：等锁的事务占满准入名额 |
| 账户闸等 5s 而不是 200ms，期限不并入准入的 1s | 一次突发在一个账户上排队的时间要由持锁时间乘队长决定，200ms 在持锁 50–100ms 时拒掉六成突发；等闸不占连接，长一点只多占在途名额 | 保持 200ms：热点账户的突发被拒；把闸并入 1s 并只调长闸：等闸超 1s 的变成 503 而不是 429 |
| 准入和完成许可按各自的 N−R、R 上限计数 | 物理池已隔离，完成类持有或等待许可都不减少普通池的准入容量；账户闸上的完成优先级不变 | 两类共用总在用数：完成积压会挡住准入，即使普通池空闲 |
| 排队用 `Epoch` 补上"看负载与入队之间的释放" | 没有它，那次释放无人可唤醒，请求睡满 20 秒还拿 429，而容量是空的 | 无等待者时的释放攒成许可：空闲时攒下的许可会让以后每个新来的白跑一遍 7 次往返的准入 |
| 先认 key 再读请求体 | 认 key 只要头；未认证的慢速请求体不占业务名额 | 先读体 |
| 准入的 1s 是一个 context 期限，不下发 `lock_timeout`、`statement_timeout` | 目前仍用统一 context 期限；独立 `SET LOCAL` 会多一次往返（准入 7 次变 8 次）；期限到时 pgx 取消语句、事务回滚，对客户端效果相同 | 事务里 `SET LOCAL`：区分等锁和慢语句，但每次准入多一次往返；需要单独分解服务器超时时，再评估并入已有批量 |
| `fn` 写完预留之后 `Admit` 的任何错误都按"可能已提交"处理 | 提交失败和确认丢失无法区分；`SettleOrphan` 对已提交的释放、对已回滚的写墓碑，重复执行无害 | 只认特定错误：漏掉的那一种会留下永不释放的预留 |
| key 没有预设时用零 ID 的预设 | 预设内容在路由快照里，网关不另带一份默认 | 网关内置默认预设：两份会不一致 |
| 未宣告长度的请求体从 4 KiB 起翻倍增长，每次分配前先计入、复制后退还旧的 | 分配之前计入，所以并发的慢上传和大请求受预算约束，小的分块请求也不预占整个上限；峰值是新旧两个缓冲，不是 `io.ReadAll` 的全部分块加整体复制（约两倍正文，其中的一半没人计）。代价：留下的缓冲至多正文的两倍，预算中途不够会在读到一半时回 503 | 按上限预占：每个分块请求占 32 MiB；`io.ReadAll` 配 2 倍上限预占：仍然是预占；读完再复制成恰好的大小：多一份峰值 |
| 请求体全额计入内存预算，不扣每路 64 KiB 的基数 | 基数是给流缓冲的；全额简单且偏保守 | 扣基数 |
| 输出速度未知的条目不分段（routing） | 分段预留的超额额度是速度乘等待时间，速度为 0 就少预留 | 编一个默认速度 |
| 升级请求回 400 `invalid_request` | 文档只说拒绝；Codex 握手收到任何非 101 就回退到 HTTP | 426：那是邀请升级 |
| 模型列表像准入一样查库判定 key | 缓存只说 key 存在；停用、过期、IP 白名单只有 `Admit` 知道，否则被停用的 key 仍能列模型 | 缓存里附带状态：多节点下不一致 |
| 冷 key 查找的名额由共享的那次查库持有，在 accounts 里取 | 查库在 singleflight 的函数里、脱离发起请求运行；按等待者取名额会重复计，或在发起者走后提前还掉仍在用的连接 | 在 gateway 里包住 `AuthenticateKey`：每个等待者各取一个，与连接数对不上 |
| 路由参数里无法兑现的成员回 400，分组名称只取公开代号 | 静默忽略会让客户端以为约束被执行；用代号精确选组，不泄露真实名称、不扩大到同家其他组 | 猜端点参数、量化或百分位；按整家供应商映射分组代号 |
| `/v1/models` 要认 key、只列这把 key 能用的模型，M1 不带价格 | key 决定站点、域名和可见的模型；列出别的模型会让客户端选到必然 404 的名字，key 的模型限制也就无从发现；价格要等售价接口 | 列出全部模型：客户端选到不能用的模型；匿名可读 |
| 模型列表和单个模型只给路由快照里有端点的模型（`routing.Router.ServesFor`），不看熔断、满载、暂停、预算 | 没有端点（没接供应商，或组装时缺售价、规格被整个排除）的模型调用必然 404 `no_endpoint`，列出来就是让客户端选到必败的名字，正是 #311 的 E2-9 要消除的；new-api、sub2api 也只列能调用的模型。满载、熔断等是暂时的，列表据此增减会让模型随负载闪进闪出，调用自己会排队或回 503 | 只看目录的上架状态：有名无路的模型照列；用 `routing.Plan` 探测：要造一个假请求，且满载时模型从列表消失 |
| 按目录快照把每个模型的条目编码一次，请求只挑选和拼接；整份应答不缓存 | 条目只取决于目录快照，编码一次后一次请求是对模型数的一遍挑选加一次拷贝：300 个模型（600 项）约 25 µs、1 次分配（`BenchmarkModelList`），原先逐项建 map 再编码约 2.3 ms、10911 次分配。整份应答取决于 key 的可见集合和路由快照，缓存要带上这两个维度，路由快照每 5 秒重建，命中率低，不值得 | 每次请求现建：CPU 与分配随目录线性增长，每次 Claude Code 启动都要付；按目录快照缓存整份：一把 key 的列表会给另一把 |
| 402 只报来源，暂时性的带 `Retry-After: 60` | billing 的 `Insufficient` 只有来源和是否会重置 | 在 gateway 里重算窗口：重复 billing 的规则 |
| 尝试的新数据按“内容或事件在进展”算，完整但没有内容的事件不算 | 文档只说心跳、注释、重复快照不算；Messages 的 `message_start`、`content_block_start` 之后的几分钟静默若算新数据，空闲计时就永远不到；代价：提交前只发这类事件的上游 180s 后被换掉 | 任何事件都算：重复快照能把计时无限推后 |
| 响应头等待用计时器取消尝试的 context，不是包住 `Send` 的短 context | 请求的 context 管着整个响应体的读取，短 context 会在响应头到了之后继续掐流 | 给 `Send` 一个带期限的 context |
| 重试预算在尝试之前取（`TakeRetry`），第一次尝试记 `Sent` | `Plan` 只看不取；并发的重试靠 `TakeRetry` 各占一次 | 在 `Plan` 里扣 |
| 过了 `Plan.StartBy` 不再发起尝试，且以它为准，不读 `Config.StartBy` | 它由路由器按预设的窗口算好，`Config.StartBy` 只是种子；默认值下（StartBy 120s、空闲 180s）提交前空闲超时那次已过了窗口，所以只有空闲超时调短（按模型可调）才会换端点，返回最后一次尝试的错误（502）是文档的规则 | 两处都读：同一个值两个来源 |
| 上游因请求自身拒绝（`ScopeRequest`）回 400 `invalid_request`，带上游的 `message`、`type`、`param`、`code`（`Failure.Rejection`，已去掉网址、凭证、请求编号），没有原话时用统一的说法 | 负责人 2026-10-18：影响兼容的行为照 new-api；Claude Code 靠原话自动恢复。`Rejection` 只进给客户端的响应，不进日志和表（`failed` 只把它交给 `protocol.Error`） | 让每个适配器的 `Classify` 带原文：各清洗一遍，漏一个就泄露；原样透传：会暴露供应商的网址 |
| 上游在流里报的错走同一套：`protocol.FaultOf` 分类，请求本身的按 `ScopeRequest` 答 400（提交后以错误事件）、不换端点，原话经 `Conn.Sanitize`（suppliers 的同一份清洗）；其余换端点或用统一的说法 | #311 的 E2-4：同一个参数错误无论从 HTTP 400 还是从流里来，读到的都是同一句话、同样的 `code` 和 `param`，客户端才不必按来路分两种处理；只有 suppliers 知道该遮蔽哪个凭证和地址，所以协议只交出有界的原文，清洗仍在一处（`cleanRejection`）。不换端点的理由与上一行相同：换个端点还是这个请求。代价：`Conn` 多一个方法 | 让适配器在流里各清洗一遍：漏一个就泄露，且 `Classify` 至今不留文字；把 5xx、限流的原话也交给客户端：与 HTTP 路径不一致，会漏出供应商内部报错 |
| 提交点之前按上游的类别决定换不换端点，与 `send` 阶段的 `routing.Decide` 同口径：请求本身的失败不再交给下一个端点 | 一条流在提交前失败和一次 4xx 失败是同一件事：请求本身的失败换端点必败，还多花一次尝试 | 提交前一律换端点：同一个 400 被重试两次 |
| 非流式的答案也由同一个读循环聚合，而不是另开一条路 | 上游一律以流调用，计量、追加、截断、结算、断开四种情况都只有一份；聚合只是出口不同（`RelayOptions.Aggregate`） | 上游按客户端的 `stream` 调用：计量看不到进度，非分段预留，转换后的请求体还得再转换回复 |
| 聚合时提交点不写响应头，失败的尝试可以换端点 | 客户端在整份回复写好之前什么都没收到，所以"I4 提交后不换尝试"对它是写出那一刻；代价是聚合的回复在最后一次尝试之前不占状态码 | 见到第一个内容就定 200：中途失败只能返回半截内容，或丢掉已生成的内容报错 |
| 聚合保留"最后一次有内容的尝试"，下一次尝试开始时放掉 | 文档只让"最后一次尝试生成了部分内容"随错误返回，各次尝试的内容不混；保留一个 `Relay` 最多 16 MiB | 把每次尝试的内容都留着：容量随尝试数翻倍，且违反不混的要求 |
| 聚合回复在结算之后一次写出，不按提交点写 | 一次调用只写一份整个响应体，它的"内容交到客户端"就是那一次写；结算在写之前提交，所以写不出去时钱也不重复 | 边聚合边写：客户端拿到的不是一份完整的回复，失败时也收不回已写的字节 |
| `/responses/compact` 的回复原样透传，请求只经最小的同格式改写（`Target.Compact`：模型名、标识、路由参数） | 回复是上游的整份回复，不是我们生成的流，加任何字段就不再是原样；请求则不能原样：模型名对不上上游、终端用户标识会泄露、官方端点拒绝未知的路由参数。生成请求的改写（`store`、`include`、输出上限、`stream`、`input` 与历史）对压缩请求没有依据，所以只留必须改的三样 | 请求也原样发：模型名不同时调用失败，或按别的模型计费；标识泄露。套用生成的改写：猜压缩请求接受哪些成员 |
| compact 的请求体读进节点预算、上限 16 MiB，读完分块写出 | 节点内存有界；读失败时客户端什么都没收到，还能换条目；`usage` 只能从整份回复里读。写出按 64 KiB 一块、每块一个写期限，慢但一直在读的手机不会被当成已走 | 边读边透传：`usage` 读不到就没法计费，失败时也收不回已写的字节；整份一个写期限：16 MiB 在限速网络下超过 10 秒，结算已提交而客户端只拿到半截 |
| compact 的答复不带本站的费用字段，结算写不进时也不加 `billing_pending` | 原样透传的响应体加了字段就不再是上游的回复；费用只在调用记录里查 | 在透传的 JSON 里插字段：客户端按上游的格式读它 |
| 生成请求优先客户端的原格式；Chat 可转 Messages、Responses，Messages、Responses 可转 Chat；`NativeOnly` 只限制压缩请求。路由快照把 Chat 原生端点列为三种格式可服务，`callFormat` 仍用 Native 选实际调用格式 | 同格式不经过规范模型；转换时表达不了的成员由 protocol 拒绝，预留按实际调用格式的用量口径计算 | 先转换：多一次有损转换；把 Served 当成 Native：给 Chat 端点发送错误格式 |
| `Target.Streams` 告诉 `Prepare` 端点能流式，请求里一律要流；路由对生成请求只保留能流式的端点（`FormatNotServed`） | 网关总是以流读上游，非流式客户端拿聚合结果，不能流式的端点必然让这次尝试失败：花了上游的钱、用户免单、还占一次尝试。压缩调用读的是整份答复，是唯一的例外 | 让不能流式的端点接生成请求：`Relay` 只按事件流读，整份 JSON 读不出内容，只能失败；启动时断言所有端点都能流式：适配器注册表不在路由手里 |
| `Prepare` 之前按它最多同时占用的量（`protocol.PrepareCost`）向节点内存预算申请，之后只留结果缓冲的容量，`Send` 返回即退还 | 之后再记，预算满了分配也已经发生，503 撤销不了峰值（评审 #157）。申请量是请求体的字节数、`{`、`null` 和 `:` 的个数的函数（常数和实测见 protocol README），而不是请求体的固定倍数：一堆 `{}` 每 3 字节占 1.3 KiB，固定倍数挡不住。申请只在 `Prepare` 期间占着（毫秒级、纯 CPU），所以多留对吞吐几乎没有影响，少留则预算形同虚设。代价：数 `{`、`null` 和 `:` 要多扫几遍请求体（只在跨格式时，`bytes.Count` 每秒数十 GB），含大量括号的文本（代码）申请得多些 | 之后再按 `len` 记：低估（容量大于长度），且在分配之后；请求体的固定倍数：不是上界；让 `Prepare` 逐次增长时记账：转换里的解码是一次调用，没有可记账的点 |
| 没写输出上限或写成 `null`、0（含 `-0`）的生成请求，发给上游的是报价用的上限（条目的输出上限），不是模型默认值 | 路由把默认值裁到上下文剩下的部分来报价，上游不知道就按默认值生成，多出来的不在预留里（I1）；条目的上限只有一个来源，`Entry.MaxOutput`，`Target.MaxOutput` 与它相同 | 只改跨格式：同格式的 Responses 仍会超出；靠计量器的输出上限封顶：上游照常生成，封顶只少记，花费并没有少 |
| 尝试关闭时就写好它的 `billing.AttemptResult`，结算直接交 `billing.Outcome`；回执（对账键在里面）原样交给 billing，成本经 `Costs` | 回执只在关闭时拿到，用量此时已定；中间再有一层 `Ending` 要多一次逐项转换 | G4 的 `Ending` 加 `SettleFunc`：结算之外的路径都得经它，测试也只能测到它为止 |
| 任何一次追加失败都截断，不等当前段用完再看 | 计费文档：超时或追加失败就中断上游；计量器失败后每读一次就重发，数据库出故障时每个流每次读都打一次库 | 失败后照读到段用完：省不了钱，只多打库 |
| 上游正常结束的那一步不因段用完或追加失败而截断；追加失败在读完之前发生则照常截断 | 已经读完，不再有要停的读；结算按实际用量收、守底线。失败发生在读的过程中，上游还在生成，要立刻停 | 照样截断：完整的回答被当作出错 |
| 追加失败即取消尝试，所有者从被取消的读里认出原因 | 所有者只在 `Next` 返回时才处理通道里的失败；上游静默时它到空闲超时才返回，把计费故障报成 `timeout`，期间继续生成 | 所有者另起 goroutine 监听通道：多一个等读的 goroutine，取消还是要经 context；通道旁加计时器：又一个期限 |
| 输出免费的条目 `Left` 不设界 | 费用不随输出增长，段永远用不完；`Left` 为 0 被当作「有追加要等」，没有追加时所有者空转，取消不了，`Drain` 也结束不了 | 所有者见没有在途就不等：把计量器的契约漏洞留在调用方里补 |
| 提交前截断不换条目，回 402 / 503 | 别的条目花的是同一账户的钱；没有内容交到客户端，只收按次项目 | 换条目：同一个余额再被拒一次 |
| 暂估：输入的文本按该条目模型族自己的分词器计数（族里没有公开分词器时 4 字节 1 个 token）、每张图片 1600、每个文件 4096 个 token，输出 3 字节 1 个 token，向上取整，都不超过预留覆盖的上界（输出的上界是计量器自己用的 `Meter.OutputCap`） | 按模型的比例还没有数据；分词器只在这一刻、上游没报输入时跑，口径和理由在计费的「决策与理由」；比例和上限都在 `estimated` 里，算出的数不超过预留覆盖的部分，所以不会落到底线以下 | 按 0 记缺项：免费；按上界：约多收四倍 |
| 结算的永久性错误只认 `ErrNoAttempt`、找不到预留行和 SQLSTATE 22、23、42 类，其余都重试 | 文档的"违反约束、范围被拒"；认错成暂时的只是多重试到 `SettleBackground`，认错成永久的会把能结算的调用推给清理 | 只重试连接类错误：认不出的错误一律放弃 |
| 成本定不出时记 0、记 error，照常结算 | 成本是平台的，对账按供应商记录改正；为它扣住结算会把能向用户定价的调用推给清理 | 当作结算失败 |
| 收了费的提交前错误才带 `axiom` 对象 | 文档：流开始前出错也可能收费，带上用量和费用；没收费时多一个 0 只是噪声 | 一律带 |
| 只读接口在认 key 的同一个事务里读数据 | 范围就是 key 的账户，行级安全是全部的访问控制；不另开事务，也不在 gateway 里组装范围 | 先认 key、再用账户范围另开事务：多一次往返，且 gateway 要自己取范围 |
| `/credits` 不要求管理 key，调用 key 可读 | M1 没有管理 key 的接口，要求它就无人可用；读的只是本账户的余额 | 照 OpenRouter 要求管理 key：M1 无人可调 |
| 余额类字段：`total_usage` 取账户各工作区的 `spend.spent` 之和，`total_credits` 为余额加它 | 账本每次调用一行，每次轮询都扫全部不可接受；`spend` 随天数增长，且保持"额度 − 用量 = 余额" | 对 `entries` 求和：随调用数增长；把工作区和 key 的计数器一起加：每笔收费记两份，会重复；只给可用余额、用量 0：用量是假的 |
| `count_tokens` 本地估算，三字节一 token，图片 1600、文件 3000 | 没有上游就没有精确值；文本通常偏高，客户端多半偏早压缩，但不保证不超出窗口（大文件、token 密的文本会低估）。路由、报价的输入上界（字节数）偏高太多，会让 Claude Code 压缩得太早 | 用报价的输入上界：高出约三到四倍；转发上游：G4 之后再做，且要为一个免费接口占用端点名额 |

## 成本汇率的传递

`RouteDeps.ExchangeRates` 每次构建只读一次，不成功就不发布新快照，最长陈旧时间继续由 routing 的 `MaxStale` 判断。条目携带的值经 `quotables` 进入预留，保留条目的汇率集合持久化；`attempt.result` 把同一条目的冻结汇率附到回执后交给 `Credits`，不读取新的汇率。`AttemptResult.CostKnown` 要求换算已知且最终的 `CostFlag` 为 `complete`：没有用量、用量靠暂估或有项目没有价格的成本是估计值，利润记未知（`NULL`）；缺汇率不会改写原生对账所用的 `CostFlag`。`exchange_test.go` 在 Plan 层证明条目的值副本在多次刷新后不变，及失败刷新不延长有效期；`exchange_costs_test.go` 端到端覆盖首次发送前改汇率，和第一次尝试失败、改汇率、回退到第二条目仍用预留时的汇率。这组测试的 `payInCNY` 在模拟切换供应商币种的同一事务里追加对应币种的单位价值确认，沿用原数值并保留 USD 历史；单位价值的币种或原生单位不匹配时，供应商端点本就不应进快照。

路由构建的 `RouteDeps.Prices` 接收本次已读取的目录快照、端点与汇率，由 billing 发布或保留安全的价格版本；不会分别再取一次成本。准入把条目的 `Price.Version` 送到 `Quotable.Version`，结算后的调用记录使用冻结预留行返回的版本。

路由构建的价格回调只读 `billing.PublishedPrices`，并检查本节点成本；没有可用的发布版本时，日志指向价格发布列表、发布确认是否超时和本地成本，不再归因为没有规则。
