# gateway

编排一次模型调用：认 key、读请求、准入与路由、调上游、计量与追加、结算、记录。对外行为见[接入与对外接口](../../docs/domains/gateway.md)，调用经过哪些模块见[总体架构的「一次调用怎么走」](../../docs/architecture.md#一次调用怎么走)；这里写代码层面的顺序、所有权、期限、并发、故障和测试。

> 本文先于代码写成，是 [Issue #36](https://github.com/TYCT-tyct/Axiom/issues/36) 的调用路径契约；各部分随下文「PR 拆分」落地，已落地的是 G1、G2、G3（下文「入口与准入」写它们的代码结构），代码与本文不一致时，在同一个 PR 里改本文。下层的接口见 [billing](../billing/README.md)、[routing](../routing/README.md)、[suppliers](../suppliers/README.md)、[protocol](../protocol/README.md#转发草图)、[accounts](../accounts/README.md#认出与准入)。

M1 是单个核心节点，共享状态都在进程内；多核心和 Valkey 在 M2。出口只有 `platform/egress` 直连。

## 不变量

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
| 1 | 按 IP 的连接与在途上限；只用 Host 和请求头：`SiteForHost`、`AuthenticateKey`（未命中查库、合并并发查找）。不读请求体 | gateway、accounts | 0；未命中 1 个事务 | 1s | 429；401 立即关闭；库不可用或站点快照过旧 503 |
| 2 | 按账户在途上限，再取节点在途名额，每路记 64 KiB | gateway | — | — | 429 / 503 |
| 3 | 有上限地读请求体，长度计入节点内存预算 | gateway | — | 2 分钟 | 413 / 408 / 503 |
| 4 | `protocol.Inspect`；catalog 解析名字（含 `models`）；`routing.SessionKey` | protocol、catalog、routing | 0 | — | 400 / 404 |
| 5 | 预检：限流计数加一（速率计数，不需释放），读会话粘性；只计数不判定 | gateway | M1 0；M2 1 C | 20ms | 保守计数、无粘性 |
| 6 | 账户闸 → 准入名额 → `accounts.Admit`，`fn`：判定限流 → `routing.Plan` → `billing.Reserve`；提交 | accounts、routing、billing | 8 DB | 合计 1s | 429 / 402 / 404 / 503；`Wait` 则回滚、事务外排队，醒来只重做本步 |
| 7 | 尝试：本地持有者检查 → `Acquire` → `Prepare` → `Bind` → `Send` → 写对账日志；从读到第一个字节起计量，暂存开头事件到提交点 | suppliers、protocol、billing | 上游 | 见「超时与限额」 | 提交前按 `routing.Decide` 换条目 |
| 8 | 提交后边读边计量边转发，按段 `Extend` | gateway、protocol、billing | 每段 9 DB（已覆盖时 6） | 追加 1s | 截断 |
| 9 | 上游正常结束：`Settle` 提交 → `Relay.Finish` 写最后一个事件 | billing、suppliers、protocol | 15 DB（一次尝试） | 2s × 重试 | 「结束与结算」 |
| 10 | 每条退出路径释放凭证、名额；异步写调用记录、quality 观测、粘性 | gateway、observability、quality | 0 | — | 「并发、背压与共享状态」 |

## 准入

- `fn` 由本模块组装，依次：用预检计数对照限额（M1 只有平台配置的防滥用限额；以后的 key、等级速率值从 `Admission` 读，判定位置不变）；`routing.Plan`；最后 `billing.Reserve`。`Reserve` 返回保留条目在原路线里的下标，本模块按它裁剪路线；首选条目被删就是 402。
- 往返 8 次，分解见[性能的「延迟预算」](../../docs/performance.md#延迟预算)；锁与读依次发出的语义见 [billing 的「准入里的预留」](../billing/README.md#准入里的预留)，`pg.Tx` 有批量后可减到 6 次。
- `Plan` 返回 `*Wait`：`Admit` 回滚（还没有预留），在事务外按 FIFO 等进程内名额，醒来只重做第 6 步，不访问缓存、不重复计限流。
- **准入的 COMMIT 确认丢失**：返回 503、不发上游，交 `billing.SettleOrphan(adm, id)` 收尾（见 [billing 的「追加与结算」](../billing/README.md#追加与结算)）。

## 入口与准入

G1、G3 的代码结构。`Handler`（`NewHandler(Deps)`，依赖缺一个就不启动）是 `http.Handler`，外面要套 `httpserver.Public`，请求 ID 由它生成，`Call.ID` 就是它（UUIDv7），每个响应（含错误）带同值的 `X-Request-ID` 和 `X-Generation-Id`：后者由 `Handler.ServeHTTP` 统一设置，所以模型列表、拒绝升级、405 和 404 也有。`/v1` 和 `/api/v1` 两套前缀的路由相同：`POST /chat/completions`、`/responses`、`/messages`，`GET /models`；三个调用路径上的 GET 只用来拒绝 WebSocket 握手（带 `Upgrade` 回 400，否则 405）。`/responses/compact` 随 G8；`count_tokens` 和只读的账户接口见「辅助接口」。

**一次请求的顺序**（任何一步失败都由错误出口作答，已取的名额和内存经 `Call.Done` 归还）：

| 步 | 做什么 | 失败 |
|---|---|---|
| 1 | 按 IP 的在途上限（`Backpressure.IPCalls`）；`LimitConnections` 在 accept 时按 IP 限连接数，超出直接关闭。IP 只认 `RemoteAddr`（M1 不信任任何代理头），IPv6 按 /64 计 | 429 |
| 2 | 带 `Upgrade` 头的请求 | 400 |
| 3 | `SiteForHost(r.Host)`，必须是 API 域名；`Authorization: Bearer`（不分大小写）或 `x-api-key` 认 key（有 Bearer 用 Bearer）；未登记的域名、控制台域名、认不出的 key 都是同一个 401，带 `Connection: close`，体从不被读。key 不在缓存里时查库，查库在共享的一次任务里取一个准入类连接名额（`accounts.Config.LookupSlot`，见「并发、背压与共享状态」）；认出只表示 key 存在，能否使用由第 7 步的 `Admit` 判定 | 401；站点快照过旧、库不答、名额等不到 503 |
| 4 | 账户在途名额，再节点名额（`InFlight.Acquire`） | 429 / 503 |
| 5 | 读体（`MaxBody`，默认 32 MiB）：宣告的长度先计入节点内存预算（`protocol.Memory.Charge`），按宣告的长度一次分配；未宣告的从 4 KiB 起翻倍增长，每次分配前先计入新缓冲、复制后退还旧的，峰值就是已计入的量，读完留下的是最终缓冲（至多正文的两倍） | 413；408（超时）；503（预算不够）；断开或读到一半 400 |
| 6 | `Inspect`；`previous_response_id` 非空 400；`provider` 对象转成路由覆盖（下文「路由参数」）；`catalog.ResolvePublic` 认主模型（不存在或已下线 404），`models` 里的备选认得出的跟在后面；`SessionKey`；`Stuck` 读粘性；`RateCounter.Count(key)` | 400 / 404 / 503 |
| 7 | 准入循环（下文） | 见「错误出口」 |
| 8 | 交给 `Run` | — |

**准入循环**（`admit`）：取 `Queue.Epoch()`，再 `admitOnce`；`*routing.Wait` 时事务已回滚，在事务外 `Queue.Wait`（按 key 计），醒来只重做 `admitOnce`，不重读体、不重复计速率。排队期限 `QueueWait` 从这次调用第一次排队起算一次（`Queue.Wait` 收到的是绝对期限），被无关的唤醒叫醒再排队也共用剩余时间，到期 429。期限在 `Wait` 里先于 `Epoch` 快路径检查，所以别处不断释放名额也不能让过期的调用继续重试；期限过后至多再做正在进行的那一次准入。`admitOnce` 的 1s 期限（`AdmissionBudget`）从取账户闸开始，闸、连接、事务共用：`Funds.Admit` → `accounts.Admit`，`fn` 依次是速率判定（预检计数超过 `KeyRate` 回 429）→ `routing.Plan` → `billing.Reserve`。`Reserve` 返回的下标裁剪路线（`Call.Plan.Entries` 与 `Record.Route` 只留保留下来的条目），首选条目被删就是 402。`Input.Admission` 里 key 没有预设时用零 ID 的预设，路由快照必须有它；`Admission.Models()` 为 nil 是全部模型、空列表是一个都不行。`billing.Model` 由路由条目带来（`Entry.Limits`），与路由过滤用的是同一份。

**提交确认丢失**：`fn` 写完预留后 `Admit` 仍返回错误（提交失败、确认丢失、期限到），无法区分已提交还是已回滚，一律按"可能已提交"处理：回 503，不发上游，名额不立即归还，后台 `settleOrphan` 调 `billing.SettleOrphan`（账户闸的完成类、2s 一次，库出错退避 50ms 到 2s 重试，到 `SettleBackground` 为止），成功或放弃后才 `Call.Done`。`Handler.Drain` 等它们，期限到则停止重试，剩下的归清理。

**`Run` 与 `Call`**：`Run` 是准入之后的缝（G4–G7 填），拿走 `Call` 的所有权：`Call` 带请求 ID、格式、摘要、体、白名单头、会话键、`accounts.Admission`、`billing.Admitted`、`*billing.Hold`、裁剪后的 `routing.Plan`；`Run` 在结算之后调 `Done`，归还账户和节点名额与体的内存。`NewHandler` 要求 `Run` 非空，生产路径没有 stub；测试里的 `Run` 只用 `SettleOrphan` 释放预留。

**错误出口**（`writeRefusal`）：状态码、`error_type` 和外壳按[接入的「错误」](../../docs/domains/gateway.md#错误)。Messages 有自己的外壳，Chat 和 Responses 用 Chat 的；Messages 的 `type` 由状态码决定。`Retry-After` 取整数秒、最多 60。文字不用 new-api 自动停用渠道的措辞（`TestMessagesAvoidAutoDisablePhrases`）。判定：
- 402：`limit_source` 取 `balance`、`workspace_budget`、`key_limit`；会重置的上限（`Insufficient.Transient`）带 `Retry-After: 60`，billing 不报重置时刻。所需金额和在途预留、`pending` 两种来源还没有，待 billing 报告。
- `NoEndpoint`：理由全是 `NotAllowed`（key 不能用这个模型）回 404 `model_not_found`，不是 403；其余永久的回 404 `no_endpoint` 附理由；暂时的回 503 `no_endpoint`，`Retry-After: 1`，附理由。
- 满载排队超时回 429，`Retry-After` 是 `Wait.RetryAfter` 取整。
- 路由、目录、站点快照过旧，持有者失效（`billing.ErrTerminated`），准入期限到，提交确认丢失：503 `overloaded`。其余没预料到的错误同样回 503，并记 error 日志。
- 客户端已断开的请求不写响应，读体超时除外：net/http 在读期限到时会取消请求的 context，但连接还能写，所以 408 照写，并带 `Connection: close`（`TestSlowBodyIsAnswered408OverARealConnection` 用真实连接守住这一点；用模拟读失败的测试看不出）。

**派生标识的长度**：`protocol.Rewrites.Identity` 返回的会话和用户标识（`SessionKey.Upstream` 等）须不超过 128 字节，`Prepare` 对超出的返回错误（`IdentityLen` 为 0 时的默认上限，#121）；G1、G3 自己不调 `Identity`，调用 `Prepare` 的尝试（G4）按此约束派生，或显式设 `IdentityLen`。

**路由参数**（`provider`，`override`）：`Inspect` 把 `provider` 对象原样交出（至多 4 KiB），gateway 转成 `routing.Override`，每个成员要么生效要么按名字回 400，从不当作没写。生效的：`sort`（`price` 最便宜，`throughput` 最快——routing 的“最快”先比吞吐、吞吐相等才比首字延迟；`latency` 没有对应的排序，返回 400，不按吞吐代替）、`data_collection: "deny"` 与 `zdr: true`（只留数据政策为不留存的端点）、`max_price` 的 `prompt`、`completion`（本站记账单位每百万 token，十进制精确换算成每 token 的单价，超过 9 位小数不取整而是拒绝）、`preferred_min_throughput`、`preferred_max_latency`（秒；百分位对象不支持）。`max_price` 的数在构造大整数之前先按十进制位数和指数（至多三位）检查范围和精度，`1e1000000` 这类写法不会被算出来。不支持的：`only`、`ignore`、`order` 写了名字，`allow_fallbacks: false`（没有 `order` 就没有“首选”），`require_parameters: true`，`quantizations`，`max_price` 的其他项，不认识的成员。写了名字要把名称或代号换成 `SupplierID`，现在没有哪个模块给得出这张表（routing 的 `Endpoint` 不带名字，代号在 suppliers 的分组上）；表来了才能支持，见 [docs/domains/gateway.md 的「待定问题」](../../docs/domains/gateway.md#待定问题)。

**头白名单**：只转发 `Anthropic-Beta`、`Anthropic-Version`，值原样；其余一律不转发（含 `Authorization`、`x-api-key`、`x-axiom-*`、`x-claude-code-*`、`HTTP-Referer`、`X-Title`、`Cookie`）。要加协议头只改 `forwarded` 这一处。

**模型列表**：`GET /v1/models` 和 `/api/v1/models` 要认 key，并像准入一样判定它能否使用：取一个准入类连接名额（不过账户闸，不动钱），调 `accounts.Admit`，`fn` 为空，所以停用、过期、删除、管理类 key 和 IP 白名单都生效，缓存里的认出不算数。不占账户名额，只受 IP 在途上限约束。每个上架的模型列出目录里的名字（`Slug`），以及去掉作者前缀的名字——只在它是公开名字、解析到同一个模型时才列，所以 `claude` 开头的厂商原生名字能被 Claude Code 发现。每项带 OpenAI 的 `id`、`object`、`created`、`owned_by` 和 Anthropic 的 `type`、`display_name`、`created_at`；目录没有上架时间，`created` 恒为 0。列表按目录快照缓存。`/api/v1/models` 现在与 `/v1/models` 相同，OpenRouter 格式的价格、上下文长度等随售价接口加入。

## 辅助接口

G9 的代码在 `readonly.go`（只读接口）、`count.go`（`count_tokens`）。两套前缀（`/v1`、`/api/v1`）下路由相同：`POST /messages/count_tokens`，`GET /key`、`/credits`、`/generation?id=`、`/dashboard/billing/subscription`、`/dashboard/billing/usage`。行为基线是 OpenRouter 的 `GET /api/v1/key`、`/credits`（OpenRouter《API Reference》，2026-10-16 读取；`generation` 的字段取自同一套文档的惯例，该页当日取不到）、Anthropic 的 `count_tokens` 和 OpenAI 旧版的两个余额接口（new-api 源码，2026-10）。

**认 key 与读数据**：与模型列表同一条路——`IPCalls`、`authenticate`、一个准入类连接名额（不过账户闸，不动钱）、`accounts.Admit`——区别是 `fn` 在这个事务里读。事务的范围就是 key 的账户，行级安全是全部的访问控制：别的账户的调用记录、余额、花费读不到，`generation` 取不到别人的编号（与不存在的编号同为 404）。停用、过期、删除、管理类 key、IP 白名单、控制台域名都是同一个 401。M1 没有管理 key 的接口，所以 `/credits` 不像 OpenRouter 那样要求管理 key（OpenRouter《Get remaining credits》："Management key required"，2026-10-16），调用 key 就可以读本账户的余额：偏离写在[接入的决策表](../../docs/domains/gateway.md#决策与理由)，M2 有管理 key 时再按其权限收紧。错误用 Chat 的外壳；`count_tokens` 用 Messages 的外壳。成功响应带 `Cache-Control: no-store`。

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
- **`generation` 的来源与缺口**：只读调用记录。记录由后台批量写，请求结束后至多约 200 ms 才可查，此前（和不存在、别的账户的）都是 404 `generation_not_found`，客户端稍后重试；接入文档里"记录还没写入时给账本里的精简版"没有做：账本（`billing.transactions`）只记金额，不记用量和价格版本，精简版的内容要等调用记录的写入方（G10）和账本一起定，现在记录没写入就是 404。编号不是 UUID 或缺失是 400。`GetOwn` 按主键在各月分区里查，每个分区一次索引探查，不在调用路径上。
- **`count_tokens`**（`POST /messages/count_tokens`）：Anthropic 的格式，返回 `{"input_tokens": n}`。**不调上游**，所以 n 是估算，口径固定为：`⌈TextBytes / 3⌉ + 图片数 × 1600 + 文件数 × 3000`（粗估，不查模型的页数上限；`billing.InputBound` 才按模型的页数界定上限）。`TextBytes` 是 `protocol.Inspect` 读出的请求体字节数减去图片、文件的数据（含 JSON 的键和引号，不去除）；每三字节一个 token 是经验比例：英文约四字节一个，所以文本通常偏高，但不是保证的上界（字节多、token 更密的文本会低估）；图片取一张最多约 1,600 个 token（上游会把大图缩到这个量级），是上界；文件的页数不解码就不知道，取固定的 3000，页数多的文件会被低估。因此不承诺“只偏早、不超窗口”：带大文件的请求，按这个数裁剪的客户端（Claude Code 用它定压缩时机）可能偏晚。以后经上游的 `count_tokens` 转发时才有精确值。不读 `max_tokens`，不要求它。它不预留、不收费、不写调用记录；前置检查同调用：IP 在途、认 key、账户在途名额、请求体按内存预算、key 的计数请求速率（单独计数，键 `count:`+key，超过 `CountRate` 回 429，不占用也不受 `KeyRate` 影响），然后 `Admit` 确认 key 可用。**模型**必须存在（404 `model_not_found`），且 key 可用（`Admission.Models()`，用 `allowedModels`，与路由同一逻辑）；不可用的同样回 404 `model_not_found`，不回 403。要精确计数，以后可经上游的 `count_tokens` 转发（G4 之后），这里的估算作为没有上游时的回答。
- **测试**：`readonly_test.go`（rig，真实数据库：成功、无效 key、无权限类 key、跨账户读取、不存在、计数口径、额度窗口）；`billing/read_test.go`（窗口边缘、行级安全）。

## 尝试

- 逐条目：先查本地持有者（上次心跳成功 + TTL − 5s 晚于现在），失效就不再发起尝试、按「结束与结算」收尾。`Acquire`（内存比较交换）→ `Prepare`；拿不到凭证或 `*NotSupported` 就跳过，不算尝试，写进路由记录。全部因 `*NotSupported` 跳过：`Settle` 只释放，400 附原因；全部因凭证名额跳过：`Settle` 只释放，429、`Retry-After: 1`。
- **尝试的身份在进入 `Send` 时确定**：`Hold.Bind(seq, entry)` 分配序号（从 1 起，最多 3）、绑定不可变的报价条目，记开始时间（`priced_at`）。此后不论结果都算一次尝试，计入次数和重试预算。失败的尝试是**确知未执行**（用量 0）还是**结果未知**，取 `Failure.Executed`，判定规则见 [suppliers 的「调用面」](../suppliers/README.md#调用面)。
- 进入 `Send` 时、拿到响应头里的对账键时、尝试提交时，各调一次 `suppliers.Journal`（见 [suppliers 的「对账日志」](../suppliers/README.md#对账日志)）。
- 非 2xx 由适配器归为 `Failure`，`routing.Decide` 定换条目、换凭证还是返回。2xx 后暂存开头事件，第一段正文、推理或工具调用到达即提交。提交前出错、空闲超时、200 无正文都换条目，换前取消当前上游。
- 回退不追加预留：准入已按保留条目的最高报价预留。客户端断开后不再发起新尝试。

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

三个 context：`clientCtx`；`attemptCtx`（`AttemptContext`），带单次调用最长时间，只对传递取消的端点还跟随 `clientCtx`；`settleCtx`（`SettleContext`），不跟随取消、自带期限，保留 `clientCtx` 里的值。取消的传递：`clientCtx` → `attemptCtx` → 关闭上游响应体 → `Next` 返回。用户看到的四种情况见[接入的「用量与费用」](../../docs/domains/gateway.md#用量与费用)；代码上：

- 排队中：停止等待，什么都不写。已预留、未发上游：`Settle` 只释放。
- 端点传递取消：取消 `attemptCtx`，已累计用量加估算缺项交 `Settle`，标暂估。
- 端点不传递取消（不论是否已提交）：`attemptCtx` 与 `clientCtx` 脱钩，读完上游不转发，仍受空闲计时和最长时间约束、仍占名额；读也超时就取消、按暂估结算。
- 取消不直接动钱，预留只经 `Settle` 或清理落到终态。

## Relay 的所有权

- 一个请求一个所有者 goroutine：只有它调 `Relay` 的方法，同一时刻至多一个 `Next` 在读；`Next`、`Finish`、`Fail` 返回的切片在下一次调用 `Relay` 前有效，所有者写完才再调 `Next`。
- 写客户端经一把写锁。提交后的保活由计时器在写锁下写 `protocol.KeepAlive(format)` 的预生成字节，不碰 `Relay`。空闲计时到期只取消 `attemptCtx`。
- 收尾顺序固定：取消 `attemptCtx` → 等 `Next` 返回 → 所有者调 `Settle` → `Finish` 或 `Fail`。
- 非流式客户端：Relay 聚合，每路输出预算 16 MiB；聚合内存申请不到是本地容量故障，按对外错误结束，不再回退。

## 超时与限额

用户遇到的期限（读请求体、排队、等响应头、数据空闲、`StartBy`、单次调用最长、尝试次数）见[接入的「路径与格式」](../../docs/domains/gateway.md#路径与格式)。内部的期限：

| 跳 | 默认 | 到期 |
|---|---|---|
| key 未命中查库 | 1s | 503 |
| 预检缓存（M2） | 20ms | 按缓存不可用 |
| 账户闸等待 | 200ms | 429 |
| 准入名额加事务 | 合计 1s（一个 context 期限，闸、连接和事务共用）；`lock_timeout` 500ms、`statement_timeout` 800ms 只校验、暂不下发（见「入口与准入」） | 503，回滚无副作用 |
| 建连、TLS | 各 10s（egress） | 换条目 |
| 追加 / 客户端单次写 | 1s / min(10s, 当前段余量 ÷ 最快输出速度) | 截断 / 按断开处理 |
| 结算 | 单次 2s，退避重试 10s 后改为后台重试到截止时间 | 「结束与结算」 |
| 持有者 | TTL 60s，心跳 10s，回收宽限 60s | billing 的「持有者与截止时间」 |
| 预留截止时间 | 准入 + 单次调用最长 + 30 分钟结算宽限 | 清理接管 |
| 优雅退出 | `AXIOM_SHUTDOWN_TIMEOUT` 到期取消全部上游，再给结算 30s | 交给清理 |

- 默认值是后台配置的种子，保存时校验下列关系，不满足拒绝保存，启动时不满足不启动（`gateway.New` 先 `Config.Validate`，一次报出全部问题；数值都须为正；入口的数值是 `IPConnections`、`IPInFlight`、`MaxBody`、`KeyRate`、`CountRate`、`RateWindow`、`KeyLookup`，默认 1000、500、32 MiB、每 key 每分钟 6000 次（调用）、每 key 每分钟 6000 次（计数）、1 分钟、1s）：`lock_timeout`、`statement_timeout` < 准入合计；排队 ≤ `StartBy` < 单次调用最长；TTL ≥ 3 × 心跳，宽限 ≥ TTL；结算宽限 > 结算的后台重试（`SettleBackground`，默认 20 分钟，给清理接管留 10 分钟）；批量写的连接数 ≤ N − 保留数；每 key 等待者 ≤ 全局等待者。
- **限额**：每个请求最多 3 次尝试（含结果未知的），共享期限，换端点不重置时钟。重试预算按供应商、在进程内：1 分钟内重试不超过首次请求数的 10%，保底每分钟 5 次；用完跳过该供应商并写进路由记录。凭证范围的失败在同端点换凭证，也算一次尝试；疑似出口拦截在 M1 按端点处理并报告 quality。suppliers、egress 不暗中重试 POST。

## 并发、背压与共享状态

- **入口**：按 IP 的连接数与在途数上限（[安全的「防攻击」](../../docs/security.md#防攻击)）在一切之前；认 key 只看头，失败立即 401，未认证的连接从不读体、不占业务名额。
- **节点在途上限** 5000（含已断开、仍在读完的），超出 503 不排队；**按账户在途上限**默认节点名额的 10%（`InFlight.Acquire` 的 `accountMax` 为 0 时），可按账户调，超出 429；先取账户的，再取节点的，节点拒绝时账户的名额随即归还。按 IP 的连接数与在途数用同一个 `Limiter`（按键计数、到限即拒、不排队），数值是 G1 的后台配置。名额在请求执行期间和结算后台重试期间一直占着，只在两种情况下释放：`Settle` 提交（随后调 `suppliers.Settled` 丢弃还没写出的条目）；或交给清理（结算永久性错误，或到截止时间放弃重试），这时要等执行器已停止上游、不会再追加对账日志、不再重试结算，并且这个请求的全部对账日志都已落库（`suppliers.JournalFlushed`）之后。待结算数和对账日志的缓冲因此都受节点名额约束（[suppliers 的「对账日志」](../suppliers/README.md#对账日志)）。
- **账户闸**（进程内，按账户）：同一账户的资金事务（准入、追加、结算、`SettleOrphan`）一次一个。`Funds.Admit` 和 `Funds.Complete` 先排闸、再取同类的连接，所以排队的事务不占连接；闸释放时直接交给最老的等待者，完成类（追加、结算、`SettleOrphan`）先于准入类。准入等闸超过 200ms 回 429；完成类只受自己的 context 约束，因为它带的是已提交的钱。账户的表项只在闸被持有时存在，冷账户洪峰不留残余。等锁不再占连接，一个热点账户拖不垮其他租户；行锁仍是多核心下的最终保证。
- **内存**：每路 64 KiB 加节点预算 2 GiB，由 protocol 记账，请求体、转换、暂存、聚合都计入。
- **连接池** N（默认 64），所有入口都经分类的信号量 `Pool`：只有完成类（追加、结算、`SettleOrphan`）能用到最后 16 个，即池内在用的连接数达到 N − 16 之后，准入类（准入、冷 key 查找、只读 key 接口）和批量写都不再授予；冷 key 查找经 `accounts.Config.LookupSlot`（`Backpressure.KeyLookupSlot`）取准入类名额：名额由共享的那一次查库持有，等它的所有请求只占一个，发起的请求中途走了也不提前还；`NewHandler` 检查 accounts 配了名额（`Service.GatesLookups`），没配就不启动；调用记录与对账日志的批量写各自最多持有 2 个。批量写也不动保留的 16 个：它们是后台任务，让它们侵蚀结算的保底得不偿失。各类内部先到先得，等待者不占连接，等待的期限就是调用者给的 context（准入事务合计 1s 是调用者的期限）。心跳与回收用池外的一条专用连接，不会被饿死。
- **排队**：只在候选端点都满载时，在事务之外；全局最多 256、每 key 最多 16 个等待者，满了 429；FIFO，每释放一个名额调一次 `Queue.Wake`，唤醒一个。调用者在看负载之前先取 `Queue.Epoch()` 并传给 `Wait`：看负载和入队之间释放的名额没有等待者可唤醒，`Epoch` 会记下这种释放，`Wait` 见到就直接返回让调用者重做，不会睡到 20 秒期限；有人在排队时的释放归队首，不计入，新来的不会插队。醒来时 context 已结束的等待者把唤醒转给下一个。排队期限是 `QueueWait`，从调用第一次排队起算一次，之后再排队共用剩余时间，到期 429；调用者的 context 结束则返回它的错误，什么都不写。
- **调用记录与观测**：`observability.Recorder.Enqueue(ctx, CallRecord)` 是有界队列，满了等待，在释放名额之前调用，背压规则见[记录与告警的「写入路径」](../../docs/domains/observability.md#写入路径)；`quality.Observe(Observation)` 非阻塞，有界缓冲满了丢弃并计数（只是观测）。`CallRecord` 只用 observability、catalog、protocol、accounts 的类型和跨层 ID，由本模块转换。
- **预检计数**：`RateCounter` 按键做滑动窗口计数（当前窗口加上一窗口按重叠比例折算的部分），只计数不判定，不需要释放；两张轮换的表使内存只与最近两个窗口出现过的键成正比。会话粘性随 routing 的端点类型落地，不在 G2。
- **粘性**：排序第 2 位用预检取到的端点；成功端点不同时，结束后异步写回。

| 共享状态 | 放在哪 | 理由 |
|---|---|---|
| 余额、预留、计数、账本、持有者 | PostgreSQL | 钱只在这里 |
| 限流计数、会话粘性 | M1 进程内；M2 Valkey，一个脚本一次往返；超时率过阈值即熔断 5s 后半开，期间退回进程内、限额按核心数等分；粘性在结束后异步写回 | 丢了不影响钱；放库每个请求多一次写 |
| 端点负载、429 降权、凭证在途数与冷却、重试预算、账户闸 | 进程内；M2 后台每秒同步，凭证上限按核心静态分份额 | 请求里不访问缓存；硬上限不依赖共享提示 |

缓存只在准入之前同步访问一次，规则见[路由的「容量感知」](../../docs/domains/routing.md#容量感知)。

## 故障矩阵

"钱守恒"：账本平衡、余额等于分录之和、不重复扣、收费不越底线；平台承担只出现在[计费](../../docs/domains/billing.md#预留与结算)列出的情况里，且有记录。

| 步 | 故障 | 结果与钱 |
|---|---|---|
| 1–5 | 节点崩溃；缓存不可用（M2） | 什么都没写；保守限流、无粘性 |
| 1 | 慢速请求体攻击 | 未认证的不读体；有 key 的受按 IP、按账户上限约束 |
| 6 | 数据库慢、锁等待 / 持有者失效 | 1s 内回滚 503 / 写入 0 行 503；都没有预留 |
| 6 | COMMIT 确认丢失 | 503 不发上游；`SettleOrphan` 锁余额行后释放或写墓碑；进程死了由清理释放 |
| 7–8 | 节点崩溃（已发出、结算前） | 回收后按[计费的「持有者与截止时间」](../../docs/domains/billing.md#持有者与截止时间)收尾：有已提交且带对账键的尝试的，清理待定、按记录清理结算；其余清理释放并记丢失的请求 |
| 7 | 上游连不上、出错 | 换条目；失败尝试的成本在最后结算里记 |
| 8 | 追加时数据库慢 | 等满 1s 截断为 `billing_unavailable`；消耗不超过超额额度 |
| 9 | 结算失败或超时 | 内容完整的照常结束、不带费用；执行器继续重试，比较后再写只有一方生效，迟到方按 billing 的终态分支 |
| 9 | 结算已提交、结尾未写出就崩溃 | 收费有效；客户端重试是新请求 |
| 9 | 结算永久性错误 | 停止重试、告警，交给清理：上游已停、日志不再追加且全部落库后释放名额；截止时间到由清理接管 |
| 7–9 | 对账日志持续写不进 | 未结算请求的名额不释放，节点占满后新请求 503；缓冲不超过节点名额 × 9 行 |
| — | 数据库不可用短于 TTL + 宽限（120s） | 新准入 503；恢复后心跳续上，在途照常结算 |
| — | 数据库不可用达到 TTL + 宽限 | 持有者被回收，在途的追加得 `ErrTerminated` 截断；迟到的 `Settle` 按终态分支，钱不重复 |
| — | 主备切换 | 已确认的提交不丢，靠[运营的复制要求](../../docs/domains/operations.md#部署与节点管理)；切换演练在 G11 |
| 10 | 调用记录写不进 | 队列满即背压到节点名额，不丢；崩溃丢最近一次成功写入之后的记录，`generation` 回退到账本 |
| 校正 | 1 小时内中转站记录缺失 | 暂估定死，释放保留余额；以后到的记录只调成本 |
| 对账 | 某供应商持续缺逐笔记录 | 停止向它路由并告警 |
| 退出 | 优雅退出到期 | 取消全部上游（不传递取消的因此暂估、按记录校正），结算 30s，之后的交给回收 |

每个 HTTP 请求一个请求 ID，客户端重试是新请求。不承诺外部调用恰好一次：上游已执行而响应丢失的窗口消除不了，由对账兜底。

## 性能估算

预算见[性能的「延迟预算」](../../docs/performance.md#延迟预算)。下表全是估算（同机房 RTT 0.2–0.5ms、提交 fsync 约 1ms），G1 实测了组合扫描（表后），准入和结算的往返留给 G11。

| 环节 | 预算 | 本设计 | 估算 |
|---|---|---|---|
| 入口转核心 | 1ms | M1 同进程 | 达标 |
| 认 key、读请求、路由 | 2ms | 缓存命中 + `Inspect`（全扫一遍）+ `SessionKey`（有界前缀）+ 名字解析 + `Plan`；`Prepare` 另扫一遍，在事务外 | **风险**：两遍全扫，1–4 MiB 估 4–16ms。G1 实测见下；超标先让 `Inspect` 产出可复用的位置表给 `Prepare` |
| 限流和粘性 | 2ms | M1 为 0；M2 1 C | 达标 |
| 准入 | 5ms | 8 DB（含提交） | 约 3–5ms，待实测；RTT 1ms 即超标，核心与库必须同机房 |
| 出口发请求、首字节 | 2ms + 1ms | `Prepare` 在事务外，复用长连接 | 达标；转换单独基准 |
| 追加、结算 | 不在首字路径 | 追加 9 DB（已覆盖时 6），结算 15 DB（一次尝试） | 结算尾延迟目标 p95 10ms，单独报告 |

**组合扫描实测**（`BenchmarkScan`：`Inspect`、`SessionKey`、`Prepare` 依次各一遍，开发机 i5-12500H、Windows、同时在跑别的任务，每组 3 轮 100 次，取范围）：

| 请求体 | Messages | Responses | 分配次数 |
|---|---|---|---|
| 1 KiB | 23–28µs | 15–16µs | 135 / 104 |
| 1 MiB | 6.6–7.9ms | 4.1–4.6ms | 144 / 112 |
| 4 MiB | 25–30ms | 21–23ms | 144 / 112 |

吞吐约 150–250 MB/s，两遍全扫各占一半。预算的 2ms 在 Messages 请求体约 250 KiB 以下达标，1 MiB 超出 2–4 倍，4 MiB 约 10–15 倍（比上面 4–16ms 的估算略高）。分配次数过了会话前缀（16 KiB）以后与请求体大小无关（`TestScanAllocationsDoNotGrow`）。Claude Code 的长对话落在 100 KiB 到数 MiB 之间，所以这是真实风险；按上文的办法，先让 `Inspect` 产出位置表给 `Prepare` 复用，能省掉第二遍的部分开销，留给超标后单独的 PR。

## PR 拆分

每个约 400 行非生成代码，测试先行；G1、G2、G3、G9 已落地（G1、G3 合为一个 PR，约 900 行非生成代码，因为准入的错误出口和限额由入口一并定下）。下层的工作在各自的 Issue：accounts #23（关键路径：G1 要 `SiteForHost`、`AuthenticateKey`，G3 要 `Admit`）；catalog #24（名字解析、快照）；suppliers #25（S8 调用面、`Journal`、`Cost`、`RecordAttempts`；S9 对账）；routing #26（R1 快照含负载与端点实测的取消；R2 `Plan`、`Segmentable`、`NoEndpoint`、`Decide`；R3 路由记录）；protocol #27（P4 `Prepare`，已合并（#78），按 protocol 的「转发（草图）」扩展签名；P5 `Relay`；P6–P10 跨格式与聚合）；billing #28（B1 表、持有者、截止时间；B2 `Reserve`；B3 `Meter`、`Extend`；B4 `Settle`；B5 回收、清理、`SettleOrphan`、补扣；B6 校正与保留释放）；observability #29、#54（O1 调用记录批量写；O2 指标与链路）。各线按各模块 README 的接口草图用测试替身并行。

| # | 内容 | 依赖 |
|---|---|---|
| G1 | 路径与别名、按 IP 上限、先认 key 后读体、节点与账户名额、头白名单、`/v1/models`、拒绝升级与 `previous_response_id`、错误出口；组合扫描基准 | accounts、protocol |
| G2 | 三个 context、进程内预检、账户闸、分类连接信号量、FIFO 排队、启动检查 | — |
| G3 | 准入编排、裁剪路线、402/429/404/503、`SettleOrphan` | R2、B2 |
| G4 | 尝试循环、`Bind`、结果未知判定、对账日志、`Decide`、重试预算、`StartBy` | S8、P4、P5 |
| G5 | Relay 所有者、转发、保活、空闲计时、写期限、断开四种情况 | P5 |
| G6 | 全阶段计量、追加、暂停、截断 | B3 |
| G7 | 收费表、结算与结尾、`billing_pending`、持续重试、优雅退出 | B4 |
| G8 | 非流式聚合、Chat 跨格式、`/responses/compact` | P6–P10 |
| G9 | `count_tokens`、`/api/v1/key`、`credits`、`generation`、旧版余额接口 | O1、B1 |
| G10 | 调用记录背压、quality 观测、指标、链路 | O1、O2 |
| G11 | 端到端：Claude Code、Codex、Chat SDK 经 new-api、sub2api；故障注入；主备切换；压测 | 全部 |

测试先行，至少包括：B 的读语句在 A 提交前开始（B2）；孤儿处理先于迟到的 COMMIT、回收与在途准入和追加交错、结算确认丢失后重试不补扣（B4、B5）；`ChargeSettled` 重复与乱序（B6）；聚合与写阻塞时消耗不超超额额度（G6）；大事件越过当前段、事件持续到达却迟迟不结束都在读的过程中触发追加或停读（P5、G6）；冷 key 洪峰叠加热点账户结算（G2）；数据库中断 30s 与 150s（G11）。压测场景见[性能的「怎么验证」](../../docs/performance.md#怎么验证)。

## 实现时验证的前提

- Codex 固定版本经 HTTP 每轮发完整历史（G11；不成立就把续接提前到 M1）。
- `thinking.display: omitted` 时提交前几分钟无字节，Claude Code 能容忍（G11；不能就在提交前透传 `ping`）。
- 两家中转站的逐条记录 1 小时内可查（S9）。
- 联网搜索在流式 `message_delta` 里的累计输入（G11，决定输入上界能否收紧）。
- sub2api 是否回显客户端发来的 `X-Client-Request-Id`（S8；是则发请求前就知道对账键）。
- 锁语句与读语句依次发出（`pg.Tx` 还没有批量），语义见 billing 的「准入里的预留」，准入 8 次往返；有批量后可减到 6 次。

## 决策与理由

| 决策 | 理由 | 不选 |
|---|---|---|
| 请求 ID 即调用编号 | 一个身份贯穿日志、账本、记录 | 另生成调用编号 |
| 每个准入的请求恰好一次 `Settle`，只释放也走它 | 终结只有一个入口 | 单独的 `Release`：多一条漏记尝试成本的路径 |
| 预检只计数，判定在 `fn` | 以后速率值从库里读，判定位置不变 | 在预检里判定 |
| M1 共享状态在进程内，Valkey 到 M2；多核心没有 Valkey 不启动 | 进程内实现本就是缓存不可用时的兜底 | M1 就接 Valkey |
| M1 直接用 `platform/egress` | 一种实现不加抽象 | 先定义出口接口 |
| 准入后凭证竞争失败即释放并 429 | 竞争罕见 | 持钱等待：多一条排队路径 |
| 重试预算按比例并保底，按供应商、按进程 | 低流量时一次故障不至于封死回退 | 纯比例 |
| 一个连接池、按类分信号量，心跳用池外专用连接 | 准入不挤占完成类，心跳不被饿死 | 两个池：隔离需求只在心跳 |
| 进程内账户闸，完成类优先 | 等锁不占连接、不拖别的租户 | 只靠行锁：等锁的事务占满准入名额 |
| 连接分类按池内总在用数授予，批量写同准入类 | 保证最后 16 个只给完成类；若只按各类自己的数量限制，批量写加准入可以把保底吃到 16 以下 | 按类各自的上限 |
| 排队用 `Epoch` 补上"看负载与入队之间的释放" | 没有它，那次释放无人可唤醒，请求睡满 20 秒还拿 429，而容量是空的 | 无等待者时的释放攒成许可：空闲时攒下的许可会让以后每个新来的白跑一遍 6 次往返的准入 |
| 先认 key 再读请求体 | 认 key 只要头；未认证的慢速请求体不占业务名额 | 先读体 |
| 准入的 1s 是一个 context 期限，不下发 `lock_timeout`、`statement_timeout` | `pg.Tx` 没有批量，`SET LOCAL` 多一次往返（准入 8 次变 9 次）；期限到时 pgx 取消语句、事务回滚，对客户端效果相同 | 事务里 `SET LOCAL`：区分等锁和慢语句，但每次准入多一次往返；有批量后再加 |
| `fn` 写完预留之后 `Admit` 的任何错误都按"可能已提交"处理 | 提交失败和确认丢失无法区分；`SettleOrphan` 对已提交的释放、对已回滚的写墓碑，重复执行无害 | 只认特定错误：漏掉的那一种会留下永不释放的预留 |
| key 没有预设时用零 ID 的预设 | 预设内容在路由快照里，网关不另带一份默认 | 网关内置默认预设：两份会不一致 |
| 未宣告长度的请求体从 4 KiB 起翻倍增长，每次分配前先计入、复制后退还旧的 | 分配之前计入，所以并发的慢上传和大请求受预算约束，小的分块请求也不预占整个上限；峰值是新旧两个缓冲，不是 `io.ReadAll` 的全部分块加整体复制（约两倍正文，其中的一半没人计）。代价：留下的缓冲至多正文的两倍，预算中途不够会在读到一半时回 503 | 按上限预占：每个分块请求占 32 MiB；`io.ReadAll` 配 2 倍上限预占：仍然是预占；读完再复制成恰好的大小：多一份峰值 |
| 请求体全额计入内存预算，不扣每路 64 KiB 的基数 | 基数是给流缓冲的；全额简单且偏保守 | 扣基数 |
| 输出速度未知的条目不分段（routing） | 分段预留的超额额度是速度乘等待时间，速度为 0 就少预留 | 编一个默认速度 |
| 升级请求回 400 `invalid_request` | 文档只说拒绝；Codex 握手收到任何非 101 就回退到 HTTP | 426：那是邀请升级 |
| 模型列表像准入一样查库判定 key | 缓存只说 key 存在；停用、过期、IP 白名单只有 `Admit` 知道，否则被停用的 key 仍能列模型 | 缓存里附带状态：多节点下不一致 |
| 冷 key 查找的名额由共享的那次查库持有，在 accounts 里取 | 查库在 singleflight 的函数里、脱离发起请求运行；按等待者取名额会重复计，或在发起者走后提前还掉仍在用的连接 | 在 gateway 里包住 `AuthenticateKey`：每个等待者各取一个，与连接数对不上 |
| 路由参数里无法兑现的成员回 400，包括 `sort: latency`（routing 的“最快”按吞吐排） | 静默当作没写，客户端以为排除了的供应商或超出的价格仍会被用；等供应商名称的映射有了再支持 | 先支持，名字映射用猜的 |
| `/v1/models` 要认 key，M1 不带价格 | key 决定站点和域名；价格要等售价接口 | 匿名可读 |
| 402 只报来源，暂时性的带 `Retry-After: 60` | billing 的 `Insufficient` 只有来源和是否会重置 | 在 gateway 里重算窗口：重复 billing 的规则 |
| 只读接口在认 key 的同一个事务里读数据 | 范围就是 key 的账户，行级安全是全部的访问控制；不另开事务，也不在 gateway 里组装范围 | 先认 key、再用账户范围另开事务：多一次往返，且 gateway 要自己取范围 |
| `/credits` 不要求管理 key，调用 key 可读 | M1 没有管理 key 的接口，要求它就无人可用；读的只是本账户的余额 | 照 OpenRouter 要求管理 key：M1 无人可调 |
| 余额类字段：`total_usage` 取账户各工作区的 `spend.spent` 之和，`total_credits` 为余额加它 | 账本每次调用一行，每次轮询都扫全部不可接受；`spend` 随天数增长，且保持"额度 − 用量 = 余额" | 对 `entries` 求和：随调用数增长；把工作区和 key 的计数器一起加：每笔收费记两份，会重复；只给可用余额、用量 0：用量是假的 |
| `count_tokens` 本地估算，三字节一 token，图片 1600、文件 3000 | 没有上游就没有精确值；文本通常偏高，客户端多半偏早压缩，但不保证不超出窗口（大文件、token 密的文本会低估）。路由、报价的输入上界（字节数）偏高太多，会让 Claude Code 压缩得太早 | 用报价的输入上界：高出约三到四倍；转发上游：G4 之后再做，且要为一个免费接口占用端点名额 |
