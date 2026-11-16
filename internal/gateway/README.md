# gateway

编排一次模型调用。对外行为见[接入与对外接口](../../docs/domains/gateway.md)，格式转换见 [protocol](../protocol/README.md)，模块走向见[总体架构](../../docs/architecture.md#一次调用怎么走)；这里只写公开接口和一次调用内部的不变量。下层见 [billing](../billing/README.md)、[routing](../routing/README.md)、[suppliers](../suppliers/README.md)、[accounts](../accounts/README.md#认出与准入)。

响应缓存的数据库结构由 [`00049_baseline.sql`](migrations/00049_baseline.sql) 创建；编号、等价验证与开发库重建规则见[平台迁移说明](../platform/README.md#迁移)。

## 公开接口

- `NewHandler(Deps)` → `Handler`（外面套 `httpserver.Public`）：入口、准入、模型列表、辅助接口。`NewExecutor(ExecDeps)` → `Executor.Run`（即 `Deps.Run`）：准入后把 `Call` 做到结算。依赖缺一个不启动，没有生产 stub，由 `cmd/axiom/callpath.go` 接线。
- `New(Config)` → `Backpressure`（`InFlight`、`Funds`、`Pool`、`Queue`、`RateCounter`）：先 `Config.Validate`，一次报出全部问题；默认值只在 `DefaultConfig`。
- `Call` 的所有权归 `Run`；`Call.Done` 归还名额和内存，可能晚于 `Run` 返回。`Outstanding` 是两者共用的排空计数。
- `RouteDeps`（`Build`/`Publish`/`Rebuild`/`Run`）产出路由快照和模型列表；`ModelList.Public` 给价格页。`ResponseCache` 可为空；`StreamCacheSealed`/`OpenCachePayload` 供受控导出；`LedgerAmounts` 按账本补记录金额。

## 不变量

| 不变量 | 靠什么保证 |
|---|---|
| I1 先落库再花钱：发上游前，已提交的预留覆盖本次尝试的费用上界加超额额度（最快输出速度 × `Overshoot`） | 准入提交后才尝试；`Bind` 只收报过价的条目；追加没批准就停读；billing 拒绝 `Overshoot` < `ExtendWait` 的分段设置 |
| I2 每个准入的请求恰好一次 `billing.Settle`（只释放也走它），或由清理接管 | 唯一出口 `session.conclude`，panic 时 `settleOnPanic` |
| I3 先入账再报费用 | Relay 扣住成功结尾，`Settle` 返回后才 `Finish` |
| I4 提交后不换尝试 | 写出正文、推理或工具调用之后不再调 `routing.Decide`；聚合时提交点在整份回复写出那一刻 |
| I5 钱只在 PostgreSQL | billing 的余额行锁；缓存丢失不改变钱 |
| I6 调用路径上同步访问缓存至多一次，在准入之前 | 只有预检访问；排队醒来只重做准入 |
| I7 写入有栅栏：持有者被回收后不能再新增或追加预留 | [billing 的「持有者与截止时间」](../billing/README.md#持有者与截止时间) |

## 一次调用的步骤

| # | 做什么 | 往返 | 失败 |
|---|---|---|---|
| 0 | 请求 ID 即调用编号；`Call.At` 在 `accept` 取一次，预留和记录共用 | — | — |
| 1 | IP 在途；只凭 Host 和头认 key | 未命中 1 事务 | 429；401；503 |
| 2 | 账户在途，再节点在途 | — | 429 / 503 |
| 3 | 读体，计入节点内存预算 | — | 413 / 408 / 503 |
| 4 | `Inspect`、预设/压缩/文件解析、解析模型名、`SessionKey` | — | 400 / 404 |
| 5 | 预检：限流计数加一，读粘性；只计数不判定 | M2 1 C | 按本节点计数 |
| 6 | 账户闸 → `accounts.AdmitBatch`：限流判定 → `routing.Plan` → `billing.ReserveMany` | 7 DB，按批摊薄 | 429 / 402 / 403 / 404 / 503 |
| 7 | 缓存命中判断；尝试：`Acquire` → `Prepare` → `Bind` → `Send` | 上游 | 提交前换条目 |
| 8 | 提交后边读边计量边转发，按段 `Extend` | 每段 9 DB（已覆盖时 6） | 截断 |
| 9 | 结算提交 → `Relay.Finish` | 9 DB，按批摊薄 | 见下 |
| 10 | 释放凭证和名额；记录、观测、粘性 | — | — |

## 入口与准入

- **入口**：IP 取 `platform/clientip`，按 `clientip.Group` 计在途；不按 IP 限连接：节点前是反向代理，连接数归它。认出 key 前不读体，认不出一律 401 加 `Connection: close`；冷 key 查库在共享查找里取一个准入类名额（`accounts.Config.LookupSlot`）。读体先计入预算再分配：宣告长度一次分配，未宣告的从 4 KiB 翻倍。读体超时仍写 408 并关连接；其余已断开的请求不写响应。
- **预检计数**：key 与账户的计数和粘性读删同批一次 Valkey 往返；超了节点 `KeyRate`/`CountRate` 不推进账户计数；Valkey 不可用时用进程内计数比全额限额。只计一次、拒绝不返还；`count_tokens` 单独计数、不读粘性。判定在 `fn` 里，策略随 `accounts.Admit` 读出。
- **准入批**：`Funds.Admit` 把请求排进账户队列，领头 goroutine 取成一批，`admitBatch` 调一次 `accounts.AdmitBatch`；每项的错误只属于自己，数据库错误整批回滚。`ReserveMany` 返回的下标裁剪路线（`Call.Plan`、`Record.Route` 只留保留的条目）；首选被删是 402/403。key 没有预设用零 ID 的预设。
- **排队**：`Plan` 返回 `*routing.Wait`（候选全满，含 `suppliers.Capacity` 无位）时事务已回滚，先取 `Queue.Epoch()`，在事务外 `Queue.Wait`；醒来只重做准入，不重计速率。`QueueWait` 从第一次排队起算一次，期限先于 `Epoch` 快路径检查。
- **提交确认丢失**：`fn` 写完预留后事务报任何错误，都按"可能已提交"处理：已写预留的请求回 503、不发上游，后台各调一次 `settleOrphan`（完成类，每次 2s，退避 50ms–2s，到 `SettleBackground`），之后才 `Call.Done`；没写预留的按各自的错误返回。
- **错误出口**：`writeRefusal` 与 `protocol.RenderError` 写同一个外壳（`TestARefusalIsAnErrorOfProtocol`）；`Retry-After` 至多 60。映射表见[错误](../../docs/domains/gateway.md#错误)。
- **依赖故障按状态变化记日志**（`outage.go`）：数据库不可用（`pg.Unavailable`）和 accounts、catalog、routing 快照过旧回 503，每个依赖只在故障开始或原因变化时记一条 error，首个成功用它的请求记 warn 结束；每次拒绝计入 `axiom_gateway_outage_refusals_total{dependency}`。
- **派生标识**：`Prepare` 拒绝超过 128 字节的标识和 `metadata.user_id`；派生值是 HMAC-SHA256（`ExecDeps.IdentitySecret` 加账户 ID）的 32 个十六进制字符。所有节点的 `AXIOM_IDENTITY_KEY_FILE` 必须相同，否则上游缓存换节点就断。转发的头只在 `headers.go` 一处。
- **模型列表**：鉴权同准入（准入类名额、`accounts.Admit`，不过闸），模型集合与路由过滤同用 `allowedModels`。列表与快照同次构建，`Publish` 在 `ModelList.mu` 写锁内一起换，迟到的被拒；读锁只盖内存工作（`TestModelPublicationExcludesReaders`）；请求路径不读价。

## 辅助接口

鉴权同模型列表，数据在同一个事务里读，行级安全是全部的访问控制。只有 `/key`、`/credits` 经 `accounts.AdmitRead` 也收管理 key。

- `/key`：`usage*` 只算已收，窗口是数据库时钟的 UTC 日、周、月；`limit_remaining` = 上限 −（窗口内已收 + 在途预留），不为负，与准入拒绝的依据相同。
- 余额类字段读 `billing.spend` 的合计，不对随调用数增长的账本求和。
- `count_tokens` 不调上游：`⌈TextBytes ÷ 3⌉ + 图片数 × 1600 + 文件数 × 3000`，`TextBytes` 是体字节减去图片、文件数据。不预留、不收费、不记录；模型必须存在且 key 可用，否则 404 `model_not_found`。

## 尝试与转发

- **开始前**依次查：客户端已走、`billing.Alive`（留 5s 余量）、单次最长、尝试数到 `min(Plan.MaxAttempts, platform.MaxAttempts)`、过了 `Plan.StartBy`（不读 `Config.StartBy`）。
- `Acquire` 前再查 `Router.Blocked`；拿不到凭证、`*NotSupported`、重试预算用完都跳过，不算尝试。`Bind` 之后不论结果都算一次尝试；序号经 `session.begin` 分配，服务端循环的每次采样另占一个，合计至多 `platform.MaxAttemptSeq`。
- 等响应头用计时器取消尝试的 context，而不是包住 `Send` 的短 context，那会在头到达后继续掐流。空闲计时只在有新内容时重置，完整但无内容的事件不算。
- 上游一律以流调用；非流式客户端由 `RelayOptions.Aggregate` 聚合，计量、追加、截断、结算同一条路径。聚合的尝试有内容后失败仍可换条目，只留最后一次有内容的 `Relay`；截断、`ErrMemory`、单次最长、客户端已走不换。
- `/responses/compact` 和直接格式（embeddings、rerank）走 `attempt.pass`：`readWhole` 至多 16 MiB，校验、结算再改写；改写缓冲在结算前申请；读失败时没写任何字节，可换条目。准入设 `NoSegments`（一次报足）；compact 与不可转换的格式设 `NativeOnly`。
- 服务端搜索循环（`serverloop.go`）：每跳一次独立尝试，先结束上一跳再 `Acquire`；输出暂存在 `session.out`（扩容前记预算），只有最后一跳写出。
- 供应商范围的失败由 `Router.Block` 暂停，本次跳过该供应商其余条目。上游原话只经 `Conn.Sanitize` 交出，只进响应。
- **客户端断开**：三个 context。`clientCtx`；`attemptCtx`（`AttemptContext`）带单次最长，只对传递取消的端点跟随 `clientCtx`，空闲、等头、写失败各以自己的原因取消；`settleCtx`（`SettleContext`）不随取消、自带期限。不传递取消的端点读完不转发；写失败（`WriteTimeout`）按客户端已走处理。取消不直接动钱。

## 计量、追加与结算

- **计量**：每次 `Relay.Next` 之后、写这一步之前，把 `Relay.Progress()` 交给 `Meter.Observe`，覆盖提交前暂存、聚合和断开后读完。`Meter` 只归所有者 goroutine，每次尝试一个。
- **追加**：`Observe` 要追加时，另一个 goroutine 以 `SettleContext`（`ExtendWait`，含等闸）调 `Ledger.Extend`，请求是不可变快照，结果进容量 1 的通道，所有者在两次 `Next` 之间取。每个请求至多一个追加在途；`stream` 退出前等它返回，结算前没有追加在途。
- **停读**：段用完或 `Left` 为 0 时停读、暂停空闲计时，等在途的那次；输出免费的条目 `Left` 不设界，免得为不会来的追加空等。写期限 = min(`WriteTimeout`, `Left` ÷ 最快输出速度)；不再读上游后用 `WriteTimeout`。
- **截断**：任何一次追加失败都以 `*extendFailed` 取消尝试，阻塞的 `Next` 立刻醒来，不等空闲超时；`*Insufficient` → `credits_insufficient`，其余 → `billing_unavailable`。上游正常结束的那一步不截断。提交前截断不换条目：别的条目花的是同一个账户。
- **结局**：`attempt.result` 在 `close` 时写好 `billing.AttemptResult`，与账本同一次提交写进 suppliers。成本经 `Costs` 按条目冻结的汇率定价，定不出记 0 和 error、不挡结算。缺项按 `estimated` 暂估，不超过预留覆盖的部分，口径见[计费](../../docs/domains/billing.md#用量来源与暂估)。
- **结算**：同一个 `billing.Outcome` 反复交 `Ledger.Settle`，每次 2s，退避 50ms–2s；前台试满 `SettleRetry`，没写进时内容完整的带 `billing_pending` 结束。之后：暂时性错误由后台以同一结局重试到 `SettleBackground`；永久性错误（`ErrNoAttempt`、找不到预留行、SQLSTATE 22/23/42）记 error 交给清理。名额在写进或交给清理时归还。
- **调用记录**：准入时 `Records.Admit` 预占位置（spill 满则 503）；结算后 `Commit` 到本机 spill，再写响应；后台结算的先停放待定记录，提交后替换（`TestACallSettledInTheBackgroundKeepsItsRecordThroughACrash`）。
- **排空**：`Outstanding.Go` 在调用者 goroutine 先计数、交接都在 `ServeHTTP` 内，计数不会在交接间读到零；用锁不用 `sync.WaitGroup`，因为排空中计数可能从零再增长。期限前 `grace`（`abortGrace` 5s）取消全部尝试，到期限才停后台重试。

## Relay 的所有权

- 一个请求一个所有者 goroutine，只有它调 `Relay`；返回的切片在下一次调用前有效。
- 写客户端经写锁 `session.wmu`，它也守收尾标志；保活在写锁下写预生成字节，不碰 `Relay`。收尾：取消 `attemptCtx` → 等 `Next` 返回 → `Settle` → `Finish` 或 `Fail`。
- 每个 `Relay` 在每条退出路径上恰好归还一次节点预算，panic 也不例外：创建即记入 `session.live`，丢弃一律经 `session.drop`，`Run` 退出时 `release` 兜底；compact 回复的缓冲同理记入 `session.replyHeld`。

## 超时与限额

用户看得到的期限见[路径与格式](../../docs/domains/gateway.md#路径与格式)。默认值只在 `DefaultConfig`，尚无后台读写。

| 项 | 默认 | 到期 |
|---|---|---|
| `NodeInFlight` / `IPInFlight` / 账户在途 | 5000 / 500 / 节点的 1/10 | 503 / 429 / 429 |
| `MaxBody` | 32 MiB | 413 |
| `KeyRate` / `CountRate`，`RateWindow` | 每 key 6000 / 6000，1 分钟 | 429 |
| `KeyLookup` / 预检缓存 | 1s / 20ms | 503 / 按本节点计数 |
| `AccountGateWait` | 5s，不计入下一行 | 429 `account_gate` |
| `AdmissionBudget` | 1s，连接加事务，从取得闸起算 | 503，回滚 |
| `LockTimeout` / `StatementTimeout` | 500ms / 800ms，只校验不下发 | — |
| `QueueMax` / `QueuePerKey` | 256 / 16 | 429 |
| `HeaderWait` / compact / `IdleTimeout` / `KeepAlive` | 60s / 10 分钟 / 180s / 15s | 换条目或 `timeout` |
| `WriteTimeout` | 10s；整份回复每 64 KiB 一块，总期限 max(10s, 字节 ÷ 64 KiB/s) | 按客户端已走 |
| `ExtendWait` | 1s，含等闸 | 截断 |
| `SettleRetry` / `SettleBackground` | 10s / 20 分钟 | 交给清理 |
| 持有者 TTL / 心跳 / 宽限 | 60s / 10s / 60s | 回收 |
| `SettleGrace` | 30 分钟，截止 = 准入 + `MaxCall` + 它 | 清理 |
| 优雅退出 | `AXIOM_SHUTDOWN_TIMEOUT` 30s，其中 `abortGrace` 5s | 交给清理 |

`Validate` 要求：全部为正；`PoolReserve` < `PoolSize`；`QueuePerKey` ≤ `QueueMax`；`LockTimeout`、`StatementTimeout` < `AdmissionBudget`；`QueueWait` ≤ `StartBy` < `MaxCall`；`HeaderWait`、`CompactHeaderWait`、`IdleTimeout` < `MaxCall`，`KeepAlive` < `IdleTimeout`；TTL ≥ 3 × 心跳，宽限 ≥ TTL；`SettleGrace` > `SettleRetry` + `SettleBackground`；`RateWindow` 是 1 秒到 24 小时的整毫秒。不满足不启动。重试预算属于 routing（默认首次请求数的 20%，每分钟保底 3 次）。

## 并发、背压与共享状态

- **在途**：IP、账户、节点各一个 `Limiter`（到限即拒、不排队），先账户后节点。名额占到结算写进或交给清理，待结算数因此有界。
- **429 的原因**：每个 429 按 `Refusal.Reason` 计入 `axiom.gateway.rate_limited`；新增会回 429 的限制必须有自己的原因，原因为空的计数说明有限制没分开。原因清单见[错误](../../docs/domains/gateway.md#错误)。
- **内存**：每路 64 KiB 加节点预算 2 GiB，由 `protocol.Memory` 记账；请求体全额计入、不扣基数。准备阶段由 `protocol.PrepareBudgeted` 在分配之前申请，申请不到 `ErrMemory`（503）。
- **连接池**：准入类许可至多 N−R、完成类至多 R，分别计数，物理隔离见 [platform 的连接池](../platform/README.md#连接池)。追加、结算、孤儿结算用 `pg.CompletionContext` 走完成池。`NewHandler` 要求 accounts 配了 `LookupSlot`。
- **排队**：FIFO；`session.done` 先 `conn.Done()` 再 `Queue.Wake()`，被唤醒者看到已空出的位，用不上就 `Pass`；`Epoch` 记下"看负载与入队之间"无人可唤醒的释放，`Wait` 见到即返回；有人排队时的释放归队首。一次释放至多引起当时排队人数那么多次准入。
- **粘性**：收费的那次尝试交付了内容、且端点不是预检取到的，才 `Stick`（`Plan.StickyTTL`）；在结尾之前写，异步写入晚到只损失命中。带 `x-claude-code-context-compacted` 时先 `Forget`。

| 共享状态 | 放在哪 |
|---|---|
| 余额、预留、账本、持有者 | PostgreSQL |
| 限流计数、会话粘性 | M2 Valkey，熔断与故障语义见 [platform/kv](../platform/kv/README.md)；不可用时不粘 |
| 端点负载、凭证在途、重试预算、账户闸、排队 | 进程内；凭证份额见 [suppliers](../suppliers/README.md#并发与速率) |

## 账户闸

进程内，按账户（`gate.go`）：同一账户的资金事务一次一个批次，在闸上排队不占连接，所以热点账户填不满池。

- 完成（追加、结算、孤儿结算）先于准入，同类 FIFO：完成带着已提交的钱，准入可以重试。
- 一批一类、一个账户、一个事务；准入至多 32 个、完成至多 16 个。准入批已有同伴时才等 1ms，单个请求不等。全是结算的批走 `SettleBatch`。
- 准入等闸至多 `AccountGateWait`；完成类只受调用方 context 约束，调用方走了也照常执行。被取进批次后用 `context.WithoutCancel` 加批次期限，单个客户端取消不回滚别人。
- 一个 work 的错误整批回滚、只还给它，其余在新事务里重试；只有事务或连接错误才整批按请求重试。
- 表项只在有等待者或批次时存在，领头 goroutine 在锁内确认队列空才删除，所以不丢唤醒；领头者不随发起请求取消。

## 响应缓存

对外语义见[响应缓存](../../docs/domains/gateway.md#响应缓存)。`cachePlanFor` 在预设、压缩、文件解析之后算键，命中判断在 `webSearch` 之后、第一次尝试之前；命中仍走准入和预留，结算没有尝试。`Lookup` 先按密文和两份明文计入节点预算，2s 期限，坏行当 MISS 并删除。`newCacheCapture` 超过 `maxCacheEntry` 即放弃，流照常转发。写入队列 64，满即丢弃并计数，写库 5s 期限；`cleanup` 每分钟至多 10 批 × 1000 行。

## 故障矩阵

| 故障 | 结果与钱 |
|---|---|
| 准入时库慢、持有者失效、COMMIT 确认丢失 | 503、无预留；确认丢失由 `SettleOrphan` 释放或写墓碑 |
| 已发上游、结算前崩溃 | 清理释放预留，不收用户费；成本平台承担 |
| 追加时库慢 | 1s 后截断，消耗不超过超额额度 |
| 结算失败 | 内容完整的照常结束不带费用，执行器继续重试；比较后再写只有一方生效 |
| 数据库不可用达到 TTL + 宽限 | 只有本节点断开时它的持有者被回收，在途追加得 `ErrTerminated` 截断 |

不承诺外部调用恰好一次：上游已执行而响应丢失由对账兜底。

## 性能估算

预算见[性能](../../docs/performance.md#延迟预算)。约束代码的：准入 7 次往返，核心与库同机房；`Prepare` 在事务外、复用 `Inspect` 的位置表；扫描分配不随对话长度增长（`TestScanAllocationsDoNotGrow`）；模型列表一次请求一次分配（`BenchmarkModelList`）。

## 决策与理由

| 决策 | 理由 |
|---|---|
| 请求 ID 即调用编号 | 一个身份贯穿日志、账本、记录 |
| 预检只计数，判定在 `fn` | 速率值从库里读，判定位置不变 |
| 多核心没有 Valkey 不启动（`AXIOM_CORE_NODES` > 1 而无 `AXIOM_VALKEY_URL`） | 进程内计数只是单核心和故障时的兜底 |
| 准入的 1s 是 context 期限，不下发 `SET LOCAL` | 多一次往返；期限到时 pgx 取消语句，效果相同 |
| 结算永久错误只认三类，其余重试 | 错认成暂时只是多重试，错认成永久会把能结算的调用推给清理 |
