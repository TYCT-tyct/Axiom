# billing

预留、计量、追加、结算、清理与校正在代码层面的接口和规则。钱怎么算、收谁多少见[计费](../../docs/domains/billing.md)，一次调用的编排见 [gateway](../gateway/README.md)；这里只写调用路径上下游代码要依赖的部分，定价、账本、额度批次的代码结构随实现补入本文。

> 本文先于代码写成，是 [Issue #28](https://github.com/TYCT-tyct/Axiom/issues/28) 在调用路径上的设计（PR B1–B6，见 [gateway 的「PR 拆分」](../gateway/README.md#pr-拆分)）；代码与本文不一致时，在同一个 PR 里改本文。已实现的是报价 `Quote`、计量 `Meter`、预留的表、持有者、`Reserve`、`Extend`、`SettleOrphan`，以及结算 `Settle`、清理 `Cleanup`、校正 `Correct`、账本、后台加额度 `AddCredit` 和对账 `Reconcile`（见下文「报价」「计量」「数据」「结算与账本」）；后台循环 `Run`、额度批次的分配与到期、缺项的兜底价是设计，尚未实现。

## 接口（草图）

```go
// 引用 catalog、suppliers、quality、accounts；不引用 protocol、routing
type Demand struct { // gateway 由 protocol.Request 填写；只放请求里的事实，依赖模型的数由报价时按条目算
	TextBytes, Images, Documents, Choices int64 // 输出上限按条目，在 Quotable 里
	ServerTools []ServerTool // 请求启用的服务端工具（联网搜索、代码执行）
	Compaction bool          // 开启了服务端压缩或上下文管理（Anthropic context_management、Responses compaction）
	InputItems catalog.ItemSet; ServiceTiers catalog.ServiceTierSet
}
type ServerTool struct{ Item catalog.Item; MaxUses int64 } // MaxUses 0：请求没写，报价时用该条目模型的默认最大次数
type Quotable struct{ Price catalog.Schedule; Model Model; MaxOutput int64; Segmentable bool } // 已实现；B2 加上 Endpoint、Model 的 ID 和 PriceRef
type Model struct{ ContextLength, ImageTokens, PageTokens, FilePages, OutputSpeed int64; MaxUses map[catalog.Item]int64; Reports catalog.ItemSet } // 调用方从目录快照填
type Segments struct{ Output int64; Overshoot time.Duration } // 站点设置：每段输出 token；追加等待加取消生效的延迟上限
func Quote(d Demand, q []Quotable, s Segments, avail []Available) (Reservation, error) // 已实现，纯函数；Reserve 在锁下调用它
type Reservation struct{ Amount platform.Exact; Kept []int }
func Start(ctx context.Context, db *pg.DB, platform pg.Scope, c Config) (*Billing, error) // 已实现：登记一个新持有者
func (b *Billing) Heartbeat(ctx context.Context) error // 已实现；ErrReaped：进程停止准入、换新持有者（再 Start）
func (b *Billing) Reap(ctx context.Context) (int, error) // 已实现：回收过期超过宽限的持有者
func OpenBalance(ctx context.Context, tx pg.Tx, site, account uuid.UUID, floor int64) error // 已实现：开户时建余额行
func ReadFunds(ctx context.Context, tx pg.Tx, account uuid.UUID, workspaces []uuid.UUID) (Funds, error) // 已实现：余额行加这些工作区的累计 spent，不加锁；没有余额行全是 0；只读接口用
func ReadKeyUsage(ctx context.Context, tx pg.Tx, key uuid.UUID, reset Reset) (KeyUsage, error) // 已实现：key 的累计和当前 UTC 日、周、月已收，以及上限窗口里的已收加预留；只读接口用
type Admitted struct{ Scope pg.Scope; Site, Account, Workspace, Key uuid.UUID; WorkspaceBudget, KeyLimit *Limit } // 已实现；accounts.Admit 落地后换成 accounts.Admission
func (b *Billing) Reserve(ctx context.Context, tx pg.Tx, a Admitted, id uuid.UUID,
	d Demand, q []Quotable) (*Hold, []int, error) // 已实现；[]int：保留条目在 q（即原路线）里的下标；*Insufficient{Source, Transient}、ErrTerminated
func (h *Hold) Bind(entry int) *Meter // 已实现（经内嵌的 Reservation）；尝试序号随结算加入
func (m *Meter) Observe(p Progress) MeterStep // 已实现，纯内存、不分配、只归所有者
type MeterStep struct{ Extend bool; Request ExtendRequest; Exhausted bool } // 一次读可能同时越过追加点和当前段，两者都报
func (m *Meter) Extended(seq int, err error)  // 已实现；所有者取回追加结果后调用，成功才采用新段、解除"追加中"
type ExtendRequest struct{ Seq int; Entry int; Target platform.Exact } // 已实现；值类型，即不可变快照；Target 是累计目标
func (b *Billing) Extend(ctx context.Context, h *Hold, r ExtendRequest) error // 已实现；ErrTerminated、*Insufficient；不读 Meter
func (b *Billing) Settle(ctx context.Context, h *Hold, o Outcome) (Final, error) // 已实现；前提：无追加在途
func (b *Billing) Cleanup(ctx context.Context, max int32) (int, error) // 已实现：截止时间、被回收的持有者、校正窗口到期
func (b *Billing) Correct(ctx context.Context, c Correction) error       // 已实现：ChargeSettled 的处理，按版本号只生效一次
func AddCredit(ctx context.Context, tx pg.Tx, c Credit) error         // 已实现：只有赠送；tx 须是平台范围（审计日志要求）
func (b *Billing) Reconcile(ctx context.Context) ([]string, error)    // 已实现：每日对账，列出不平的交易和与账本不符的余额
type Suppliers interface{ RecordAttempts; CommittedAttempt; RecordLostRequest; ResolveLostRequest; EndRequest } // 已实现，见「结算与账本」
func (b *Billing) SettleOrphan(ctx context.Context, a Admitted, id uuid.UUID) error // 已实现：一个事务；数据库错误由调用方退避重试
func (b *Billing) Run(ctx context.Context) error // 心跳、回收、截止时间清理、按 ChargeSettled 校正、到期释放保留
type Progress struct{ ContentBytes, PartialBytes, Searches int64; Reported catalog.Usage; Final catalog.ItemSet } // 已实现；gateway 从 protocol.Progress 复制；Final：已是最终值的已报告项
type Segment struct{ Usage catalog.Usage; Reported catalog.ItemSet } // 已实现；Reported 之外有用量的项是估的
type Outcome struct{ Attempts []AttemptResult; Cancelled bool } // 已实现；收费的是最后一个
type AttemptResult struct { // 已实现
	Seq, Entry int; Key string // 对账键
	Cost int64 // 这次尝试的成本，十亿分之一额度，由 suppliers.Cost 估算（还没有）
	Segments []Segment
	Result Result // Committed（零值，照收）| Aggregated | FailedBeforeCommit | Unknown | NotExecuted；失败时后两种取自 Failure.Executed
	Finish FinishReason // FinishNone（零值）| Stop | Length | ToolUse | Refusal | Error | Truncated | Timeout | Cancelled
}
type Final struct{ Segments []Segment; Charged platform.Exact; Estimated bool } // 已实现；币种随站点的额度
```

- 金额沿用 `platform.Exact`；`platform.RequestID`（即调用编号）、`platform.AttemptSeq`（从 1 起，进入 `Send` 时分配）由第一个用到它们的 PR 加进 `platform`。价格版本只追加、不删除，`PriceRef` 就是版本号。
- 后续事务（追加、结算、`SettleOrphan`）的账户范围只能从 `accounts.Admission.Scope()` 取得。
- `Reserve` 在 accounts 打开的准入事务里执行；`Settle` 在自己的事务里调用 `suppliers.RecordAttempts`、`RecordLostRequest`、`ResolveLostRequest`。这是[总体架构规则 5](../../docs/architecture.md#模块边界规则) 的两处跨模块同事务。
- **输出上界**（`Observe` 用它）：字节上界 = 已完整读到的事件里解码后的正文、推理、工具参数的 UTF-8 字节（不含 SSE、JSON 外壳和签名；token 不多于字节）+ 正在读的不完整事件已到的原始字节（`PartialBytes`）；搜索按事件计数。已报告的输入侧项（输入、缓存读写）一经报告就是准数；输出侧的项（输出、推理、搜索）在最后的用量事件之前报告的只是下界（Anthropic `message_start` 里的 `output_tokens`、任何累计中的计数），所以在 `Final` 标明它之前，计量的输出 = max(已报告, 字节上界)。测试用已有的样本 `internal/protocol/testdata/messages/text/upstream.sse`：`message_start` 报 `output_tokens: 1`，之后的正文仍按字节计量、越过当前段时触发追加，`message_delta` 的 15 才是最终值。`Relay.Next` 每做一次至多 16 KiB 的底层读就返回，所以两次计量之间没计量的输出不超过一次读。
- **`InputBound(d, m)`** 给出首次采样和任一次采样输入的最大 token 数，决定价格落在哪一档；`Quote` 和路由的最高价过滤用同一个函数。
- **`Segmentable`** 由 routing 按条目和请求算出（见 [routing 的「接口」](../routing/README.md#接口)）；不可分段的条目全额预留、不追加、不截断。

## 报价

`Quote` 按[计费的「预留与结算」](../../docs/domains/billing.md#预留与结算)给每个条目报价，删掉付不起的，返回保留条目报价的最大值。不读库，`Reserve` 在锁下读出余量后调用它，所以报价规则不需要数据库就能逐条测试。

- **单价**：每一项都按 `catalog.Schedule.Ceiling` 取上界：请求可能产生的输入侧项（总含 `input`；模型可能报告缓存读时含 `cache_read`，缓存命中由上游决定，不看请求）在请求可能用到的服务等级（总含标准档）里、在提示词可能落入的每一档中的最高单价。档位只看基数里的项，提示词怎么拆分都可能落到长度以下的任一档，所以不能只取一档或一项。输入第一次采样按首次提示词的上界取档，之后每次按上下文长度；输出（`output`、`reasoning` 取高者）和按次项目按最长的提示词取档；请求启用了联网搜索时最长的提示词就是上下文长度，即使最大次数为 0，因为超出次数的搜索也会新增采样。
- **数量**：首次提示词 = min(上下文长度, 文本字节 + 图片数 × 单张图片上限 + 文件数 × 单个文件上限)，单个文件上限 = 单页上限 × 单个文件最大页数，没有页数上限时为上下文长度（protocol 只数文件个数）；采样次数 = 1 + 各服务端工具最大次数之和（`MaxUses` 为 0 用该条目模型的默认值，同一工具列两次就加两次），开启压缩再乘 2，之后每次按上下文长度；输出 = 输出上限 × max(`Choices`, 1)，可分段的条目改为 min(它, 一段) + 超额额度，超额额度 = ⌈`Overshoot` × 最快输出速度⌉ token，按输出单价。
- **溢出**：计数饱和在 `math.MaxInt64` 而不回绕；饱和会少算，所以除首次提示词（被上下文长度截住）外，任何计数到达 `math.MaxInt64` 的条目报错不报价，输出上限 × 选择数也一样，即使首段比它小：计量器按它截住输出。在此之内每项乘积小于 2¹²³，至多 `NumItems` + 3 项相加，128 位不会溢出。
- **缺价、模型没有上下文长度**：返回错误，整个请求不报价。上架门槛保证可能用到的项都有价，所以这是数据错误，不按 0 计也不猜价。
- **余量**：`[]Available` 按顺序列出各来源（余额、站长余额、工作区预算、key 上限）的余量；报价不超过每一个余量的条目保留，相等算付得起。首选条目被删时返回 `*Insufficient`，指明列表里第一个不够的来源；其余被删的条目只是不进 `Kept`。`Transient`（限额窗口下一刻会恢复）随 B2 的限额一起加。
- **测试**：表驱动用例逐条核对上面每条规则的金额；两个性质测试：报价不低于按 `catalog.Schedule.Cost` 逐次采样计价的任意一次合规调用（随机价目表让低档更贵、缓存写或缓存读比输入贵都经常出现，评审给的缓存读两倍于输入价的反例每次先跑），以及与 `math/big` 无界计算的结果相等、计数越界时报错。`BenchmarkQuote` 报单条目和三条目多档的耗时与分配，随 `just bench-compare` 比较分配次数。

## 计量

`Reservation.Bind(entry)` 给保留的条目建一个 `Meter`（绑定被删的条目是调用方的错，panic）。它按[gateway 的「计量与追加」](../gateway/README.md#计量与追加)只归所有者 goroutine，`Observe` 每次读之后调用一次，不分配。

- **计什么**：只计报价没按最大值预留的部分：输出，以及超过报价次数的联网搜索。输入侧（输入、缓存读写）一经报告就是准数，但不让计量后退：报价已按每次采样的上界预留输入，报告的数小于上界也不能挪给输出，因为之后的采样还可能重读整个上下文。
- **输出** = 已报告的 `output + reasoning`，在 `Final` 含 `output` 之前与字节上界（`ContentBytes + PartialBytes`）取大，任何时候不超过输出上限 × 选择数（上游不会超过它，字节上界却可能）。搜索 = max(事件数, 已报告的数)，超出报价次数的每次搜索按联网搜索的上界单价，再加它新增的采样：重读整个上下文（开启压缩时两次），按之后各次采样的输入单价上界计。
- **段**：第一段覆盖 min(一段, 输出上限) 个输出 token。花费超过当前段起点 + 一段 × 追加比例（每个请求在报价时取一次 40%–60%，向下取整，只会早不会晚）时返回 `Extend`，请求第 `seq + 1` 段：覆盖到 min(max(已覆盖, 已用) + 一段, 输出上限)，`Target` = 输入与按次项目的固定部分 + 超额额度 + 这些输出 + 超出的搜索，`Extend` 只需把它与已预留的取大写入。花费超过已覆盖的部分时返回 `Exhausted`。输出上限已覆盖时只有超出的搜索会再触发追加。
- **追加在途与重试**：`Observe` 不再返回 `Extend`，仍照常报 `Exhausted`；`Extended(seq, nil)` 采用新段。失败时保持原段，下一次 `Observe` 原样重发同一个请求（同一段号、同一 `Target`）：失败的那次可能已提交、只是确认丢了，`Extend` 对已是这个段号的会直接报成功，所以目标不能变；在途期间多出来的需求，成功之后用下一个段号请求。
- **不可分段的条目**：`Observe` 总是返回零值：全额预留，不追加、不截断。
- **测试**：录制样本 `internal/protocol/testdata/messages/text/upstream.sse` 逐事件计量（`message_start` 报 1 是下界，"Hello" 按字节计 5 越过追加点；最终的 15 照报告计量并越过下一段的追加点）；一个比一段还大的事件同时报两者、批准后继续；在途时不再请求、失败后原样重发、确认丢失而进度增长时；追加比例 40% 和 60% 落在段内的位置；超出的搜索按逐次采样的 `Cost` 核对覆盖；输出上限；`BenchmarkObserve` 断言 0 次分配。

## 准入里的预留

`Reserve` 最后执行，依次三条语句：先 `SELECT … FOR UPDATE` 锁本模块的**余额行**（同时读出余额、底线、在途预留），再以 `FOR KEY SHARE` 核对持有者（见下文），最后一条读工作区与 key 在各自窗口的花费和占用。READ COMMITTED 下最后一条在锁取得后才开始、取新快照，所以看得到锁前事务提交的全部修改；一条语句锁读多表做不到，被等待的锁只刷新被锁的那一行。`pg.Tx` 还没有批量发送，三条依次发出，比同批多两次往返，都在账户锁内：准入合计 8 次往返（开始、设范围、核对 key、锁余额行、栅栏、读计数、写预留、提交），追加 9 次（开始、设范围、锁余额行、栅栏、读预留行、读计数、写预留行、写占用、提交），目标已覆盖时 6 次（读完预留行即提交）。然后在内存里给每个条目报价，删掉报价超过余量的条目，按剩余的最高报价预留，首选被删则 `*Insufficient`；一条语句插入预留行并更新三层占用。

- 余额行是账户资金写入的唯一串行点（[计费的「预留与结算」](../../docs/domains/billing.md#预留与结算)），锁两个账户时的顺序也在那里。没有余额行的账户按余额 0 处理，行在开户时由本模块建。
- 报价规则（每个条目自己报价、首段的组成、输入按采样次数的上界）同在那一节。采样次数和按次项目的最大次数在报价时逐条目算：请求没写某个服务端工具的最大次数，就取该条目模型在 catalog 里的默认值，所以跨模型路线的条目可以不同。报价的测试至少包括：请求没写工具的最大次数；路线里两个条目的模型默认次数不同；开启服务端压缩时采样次数乘 2。
- 余额不足是正常结果（`*Insufficient`），不让事务出错。

## 数据

迁移 `migrations/00001_reservations.sql`，查询 `queries/reservations.sql`（sqlc 生成）。金额都以十亿分之一额度存储；报价的 `Exact` 向上取整成金额（`platform.Exact.CeilAmount`）。别的模块的行只按 ID 引用，不建外键。应用没有任何表的 DELETE，UPDATE 只给下列可改的列。

| 表 | 内容 | 谁能读写 |
|---|---|---|
| `holders` | `id`、`started_at`、`expires_at`、`reaped_at` | 任何范围可读、可加 `FOR KEY SHARE`（加锁要过 UPDATE 策略的 `USING`），只有平台范围能插入和改；可改 `expires_at`、`reaped_at` |
| `balances` | `account_id`（主键）、`site_id`、`balance`（账本合计）、`floor`（≤ 0）、`reserved`（在途预留和暂估保留） | 账户范围；可改 `reserved`、`balance` |
| `spend` | `(subject, window_start)` 主键：工作区或 key 在一个限额窗口里的 `spent`、`reserved` | 账户范围；可改 `reserved`、`spent` |
| `reservations` | `request_id`（主键）、租户与工作区、key、`holder_id`、锁定的两个窗口、`segment`、累计的 `amount`、`state`（见「预留的状态」）、`deadline`，结算加入的列见「结算与账本」 | 账户范围；已释放、已结算的行不能改 |

- **窗口**：计数按 UTC 日存（`spend.window_start` 是那天的开始），预留行记下准入那天，追加和结算都记回那一行。限额读它含某一天的那个窗口里各天之和（`billing.window_from`、`window_to`：每日、每周、每月是那天所在的 UTC 日、周、月，不重置的是全部日子），按数据库时钟、在锁后的读语句里算：准入按今天，追加按预留行记下的那天。所以追加的检查和归账在同一个窗口，跨过 UTC 日、周、月的边界继续追加也越不过限额；改了重置周期，新窗口照样算上已花和在途的。
- **余额行**在开户时由 `OpenBalance` 建；没有余额行就没有锁可取，`Reserve` 直接返回余额不足（零价也一样），什么也不写；追加和 `SettleOrphan` 遇到它报错。
- **余量**按 `Exact` 精确算（余额 − 在途 − 底线，限额 − 窗口内已用），任何存得下的组合都不会回绕。
- **墓碑**：`SettleOrphan` 找不到预留行时写一行 `segment = 0`、`amount = 0`、`released` 的行占住请求 ID，丢失的准入不可能再落库。

## 结算与账本

迁移 `migrations/00002_settlement.sql`，查询 `queries/settlement.sql`。

| 表 / 列 | 内容 | 谁能读写 |
|---|---|---|
| `reservations` 新增列 | `prices`（保留条目锁定的价目表，规范 JSON，按路线下标，被删的为空串）、`charged_seq` 与 `entry`（收费的那次尝试和它的条目）、`charged`（实收）、`price`（规则算出、底线截断前的应收）、`waived`（是否只收了按次项目）、`estimated`（收费是否基于上游没报告的用量）、`unpriced`（是否有还没有价格可收的用量，见下文「暂估」）、`final_usage`（收费所用的用量，含映射不了的，JSON，`Final` 从它读出）、`correct_by` | 已释放、已结算的行不能再改 |
| `transactions` | 一个事件一笔：租户、`kind`（credit、settle、reverse、attempt_cost、cost_adjust）、`(request_id, attempt_seq, kind, revision)` 唯一 | 账户范围；只插入 |
| `entries` | 交易的分录：租户、`book`（user_balance、revenue、platform_borne、supplier_cost、supplier_prepaid、gift_expense）、`amount`（借正贷负，不为 0）；`user_balance` 有按账户的部分索引，对账一次聚合 | 账户范围；只插入 |
| `credit_batches` | 每次加额度一个批次：来源（目前只有赠送）、金额、过期时间 | 账户范围；只插入 |
| `corrections` | 每次尝试已用到的供应商记录版本 `(request_id, attempt_seq) → revision` | 账户范围；可改 `revision` |
| `waivers` | 账户每个 UTC 日免单的成本 | 账户范围；可改 `cost` |

- **账本**：一笔交易的分录合计为 0（写入时断言），用户余额是负债，`balances.balance` = −Σ user_balance 分录，与分录在同一条语句里更新；`spend.spent` 与收费在同一事务里更新。每条分录带产生它的租户，行级安全据此读；平台收入按 `book` 在平台范围汇总。按月分区随规模加入。
- **`Settle`**：一个事务、账户范围。先锁余额行、读预留行，再在锁内写每次尝试的成本（已有供应商记录的尝试不再记估算的成本）和 `Suppliers.RecordAttempts`，按预留状态分支（下文「预留的状态」）。收费 = min(应收, 余额 − 底线 − 其他预留占用)，按精确值算，超出部分记 `platform_borne`；其他预留仍被覆盖，所以并发的请求不会因为这一笔而越过底线。收费所按的尝试、免单与否、是否暂估和用量写进预留行，重试返回的就是这份 `Final`。没有尝试的结局（已预留、没发上游）只释放。语句依次发出、没有批量，尝试成本逐次写（查已有记录、写交易、写分录）：一次尝试、成本非零、走免单检查并进入终态时 15 次往返（开始、设范围、锁余额行、读预留行、每次尝试 3 条、写尝试、记免单、记收费 2 条、移动计数、写预留行、`EndRequest`、提交），每多一次尝试多 3 次；从锁余额行到提交持锁 13 次；各段先按精确值求和再向上取整一次。纯成本的分录不动余额行。
- **免单**：最后一次尝试 `NotExecuted` 不收；客户端取消照收；内容没交到客户端（`FailedBeforeCommit`、`Unknown`），或交到了但每次采样都报告了输出、输出与推理合计为 0、结束原因为空或出错的，可以免单：账户当天免单成本（这个请求全部尝试的成本之和）不超过 `WaiverCap` 时只收按次项目（仍按整次采样所在的档位计价），超过后只有零输出的条件还能免。站点级的每日上限还没有：它要一个所有账户范围都能写的计数，行级安全的口径待定。
- **暂估**：收费的项里有上游没报告的、价目表缺价的，或有 protocol 映射不了的用量（`Usage.Unknown()`），收费标暂估、留住预留里没用掉的部分到 `correct_by`。映射不了的按「价目表缺项」的兜底价收：像 token 的（字段名以 `tokens` 结尾、取值是 0 到 10¹⁰ 的整数）按该模型所有服务等级和分档里最高的 token 单价，首次结算、校正和窗口到期都按它，所以从不免费。其余的（按次计数、认不出的服务等级、非 token 的计费方式、小数）要用站点的按次默认单价或「成本 × 加价」，两者都还没有：收费只含已知部分并标 `unpriced`，保留预留、不开窗口，清理不终结它，直到一条能完整定价的供应商记录到来，那时再开 1 小时窗口。价目表缺价的已知项仍按 0 计，靠上架门槛排除。
- **`Correct`**：版本号按尝试记在 `corrections`，只增，旧的或重复的版本直接成功，数据库错误照常返回让调用方重试。任何状态下，尝试成本都调到这条记录的数（`cost_adjust`）。只有收费的那次尝试、且在窗口内，才动用户的收费：暂估的冲正原结算再按记录记新的一笔（`reverse` 与 `settle` 用同一个版本号），沿用原来是否免单，补扣以保留的部分为限、超出部分平台承担，并继续保留到窗口结束，以便更新的版本；清理待定的按记录用量和锁定价格收费，转清理结算，释放其余。记录里有没有价格可收的用量时，两者都转已暂估、标 `unpriced`、继续保留；之前是 `unpriced`、这条记录能完整定价的，从现在开窗口。
- **`Cleanup`**：平台范围，逐笔一个事务。挂起且过了截止时间或持有者已回收的：对账日志有已提交、带对账键的尝试就转清理待定（写尝试行、保留预留），否则清理释放并记丢失的请求；暂估到期释放保留、落已结算；清理待定到期仍无记录转清理释放。
- **迟到的 `Settle`**：清理待定按正常结算；清理结算、清理释放冲正清理已收的，再按完整的 `Outcome` 收（版本号 −1），补扣最多到底线、其余平台承担；清理释放的还 `ResolveLostRequest`。找不到预留行时（主备切换丢了提交）现在返回错误，按清理释放补扣还没有：要先补一行预留。
- **`AddCredit`**：锁余额行，记 credit 交易（借赠送支出）、批次行和审计日志，同成同败；同一 ID 重复无害。审计日志只在平台范围可写，所以它在调用方的平台范围事务里执行。线下付款还不收：现金要按实付金额和锁定汇率记，随支付一起做。
- **`Suppliers`**：billing 在自己的事务里写尝试、丢失的请求与解决、请求的终结（`EndRequest`），读对账日志里已提交的尝试；接口在 billing，`cmd` 把 suppliers 的函数接上（suppliers 的 `RecordAttempts`、`RecordLostRequest`、`ResolveLostRequest`、`EndRequest`、`JournalOf`），测试用替身。
- **`EndRequest`**：预留进入终态（已结算、已释放、清理结算、清理释放）的那个事务里调用，时间取写预留行时的数据库时钟，suppliers 据此在 2 天后清掉对账日志；暂估和清理待定还要等校正，不算终态。`SettleOrphan` 释放或写墓碑时同样调用。

## 追加与结算

- **`Extend`**：一个事务，锁余额行并核对持有者，让预留覆盖计量器算好的**累计目标**（`ExtendRequest.Target`）：已覆盖即成功、什么也不写（确认丢失后的重发、前一次尝试已追加够的都在内），否则写入差额，以锁内读到的段号比较后再写，金额只增不减；已终结或持有者被回收返回 `ErrTerminated`。细节见本节末条。
- `Final` 是补估后的最终用量（响应和账本用同一份）、账上**实收**金额（被底线截断时为实收）、是否暂估。比较后再写失败时按预留的持久化终态分支（下文「预留的状态」）；同一请求重试返回已存的 `Final`。
- **`SettleOrphan`**：准入的 COMMIT 确认丢失时由 gateway 交来。一个事务，先锁该账户余额行（准入事务持有它直到结束，所以拿到锁时准入已提交或已回滚），有挂起预留就以"没有尝试"释放，没有就插入一行"已释放"墓碑，已终结的不动；重复执行无害。它是同步的一个事务，数据库错误由调用方退避重试到成功（进程内队列随 `Run` 加入），进程死了由清理接管。
- **`Extend`** 在锁内先读预留行：已终结返回 `ErrTerminated`；已预留的不低于累计目标就成功、什么也不写（确认丢失后的重试，或前一次尝试的追加已经覆盖）；否则把差额与各来源的余量比较，以锁内读到的段号比较后再写，并在同一事务里移动三层占用。成功当且仅当提交后的已预留不低于目标。每次尝试的计量器各自从第 1 段编号，段号只是计量器自己的账，不用来判断重试。

## 持有者与截止时间

**持有者**是进程每次启动生成的 UUID，一行 `(holder, expires_at, reaped_at)`；时间比较一律用取锁之后的 `clock_timestamp()`，不用事务开始时的 `now()`。默认值和它们之间的关系见 [gateway 的「超时与限额」](../gateway/README.md#超时与限额)。

- **写入栅栏**：准入、追加在锁余额行之后，以 `FOR KEY SHARE` 读自己的持有者行，要求 `reaped_at IS NULL AND expires_at > clock_timestamp()`。
- **实现**：`Start` 登记持有者，`Heartbeat`、`Reap` 在平台范围的事务里执行；心跳"用连接池外的专用连接"还没有，`pg` 不交出连接，随 `Run` 一起做。
- **心跳**：每 10s `UPDATE … SET expires_at = clock_timestamp() + TTL WHERE holder=$h AND reaped_at IS NULL`，不检查是否已过期：库短暂不可用后，没被回收的仍能续上。普通更新不与 `FOR KEY SHARE` 冲突。影响 0 行即已被回收：进程改为未就绪、换新持有者。心跳和回收用连接池外的一条专用连接。
- **回收**：清理先 `SELECT … FOR UPDATE` 持有者行（等在途的写事务结束、挡住新的），条件 `reaped_at IS NULL AND expires_at < clock_timestamp() − 宽限`，置 `reaped_at` 并提交；之后再逐个处理它名下的挂起预留，这时已不可能有新的。
- **截止时间**：每笔预留带 `deadline`（准入 + 单次调用最长 + 结算宽限）。过了截止时间仍挂起的，不论持有者是否活着都由清理处理（漏 `Settle`、永久性错误、panic）。节点身份只作诊断。

## 预留的状态

| 状态 | 从哪来 | 迟到的 `Settle`（比较后再写失败时） |
|---|---|---|
| 挂起 | 准入 | — |
| 已结算 / 已释放 | `Settle`；`SettleOrphan`（含墓碑） | 返回已存的 `Final`，不再记账 |
| 已暂估 | 暂估的 `Settle`，或清理待定收到有用量没有价格可收的记录，保留余额，`correct_by` = 结算时刻 + 1h；窗口内的校正替换收费、继续保留；到期由清理释放并落已结算。`unpriced` 的没有到期，等能定价的记录 | 返回已存的 `Final` |
| 清理待定 | 清理：对账日志里有标了**已提交**且带对账键的尝试（已提交的必是最后一次），按日志写尝试行（用量未知）供对账匹配，保留余额，`correct_by` = 清理时刻 + 1h | 按完整的 `Outcome` 和正常规则结算，落"已结算"；尝试行只插入不改，成本差额经 `ChargeSettled` 调整 |
| 清理结算 | 清理待定的尝试对上中转站记录：按记录用量 × 锁定价格收费，释放其余 | 按完整的 `Outcome` 和正常规则算出应收，与清理已收的多退少补 |
| 清理释放 | 清理：日志里没有已提交的尝试，或已提交的没有对账键，或清理待定到 `correct_by` 仍无记录。只靠日志时，只有标了已提交的尝试会向用户收费，日志里其余的尝试只记成本：看不到的后续尝试可能才是最后一次 | 迟到的 `Settle` 带着完整的 `Outcome`，按正常规则收费（客户端取消、非流式聚合成功、免单规则都照常），补扣最多到底线、其余平台承担，并 `ResolveLostRequest` |

- **校正**：`ChargeSettled{charge_record_id, revision}` 的处理见「结算与账本」的 `Correct`：版本按尝试记在 `corrections`，0 行即重复或陈旧，直接成功（River 至少一次投递）；以库时钟对照 `correct_by`，到期后只调供应商成本、不动用户。扫描任务把到 `correct_by` 的保留余额释放（`unpriced` 的除外）。记录先到时：清理待定的按记录收费后立即释放；已暂估的照样保留到 `correct_by`，以便更新的版本。
- **找不到预留行**（例如主备切换丢了提交）：按清理释放处理，补扣并记丢失的请求。

## 写入的数据

| 写什么 | 事务 | 谁写 | 幂等键 / 守护 |
|---|---|---|---|
| 预留行（请求 ID、持有者、限额窗口、段号 1、金额、截止时间、挂起）与三层占用 | 准入 | billing | 主键 `request_id`；写入栅栏；余额行锁守底线 |
| 追加（累计目标） | 每段一个 | billing | 已覆盖即成功不写；否则 `WHERE request_id=$r AND segment=<锁内读到的> AND state='pending'`；写入栅栏 |
| 对账日志 | 每节点每秒一个 | suppliers | 见 [suppliers 的「对账日志」](../suppliers/README.md#对账日志) |
| 每次尝试的成本分录、对账键 | 结算 | billing、suppliers | `(request_id, attempt_seq, attempt_cost)`；主键 `(request_id, attempt_seq)` |
| 请求结算、余额、花费计数、预留终态；或只释放 | 结算 | billing | `(request_id, 0, settle, 0)`；锁内按预留状态分支 |
| 暂估校正 | `ChargeSettled` 任务 | billing | `(request_id, 0, reverse / settle, revision)`、`(request_id, attempt_seq, cost_adjust, revision)` 加 `corrections` 的版本守卫 |
| 加额度 | 后台（平台范围） | billing | `(credit_id, 0, credit, 0)`；批次主键；审计日志同事务 |
| 清理待定 / 清理结算 / 清理释放、丢失的请求 | 清理任务（后台角色） | billing + suppliers | 预留比较后再写；`lost_requests` 主键 `request_id` |
| 迟到补扣 / 补差 | 迟到结算 | billing + suppliers | `(request_id, 0, reverse / settle, -1)`；只对清理释放、清理结算发生 |
| 调用记录 | 异步批量 | observability | 主键 `request_id`，`ON CONFLICT DO NOTHING` |

## 决策与理由

| 决策 | 理由 | 不选 |
|---|---|---|
| 先锁余额行，计数用同批的下一条语句读 | READ COMMITTED 下新语句才看得到锁前的提交；同批发出不多一次往返 | 一条语句锁读多表：读到旧快照 |
| 追加按累计目标，已覆盖即成功，写入以锁内读到的段号比较后再写；金额只增不减 | 重发和前一次尝试的追加都不会多扣，回退到便宜条目也不缩小预留 | 按增量追加：重发会多扣 |
| 尝试成本与对账键在最后的结算事务里写，成本逐次尝试写 | 与收费同成同败；每次尝试要先查有没有供应商记录先到，所以每多一次尝试多 3 次往返，`pg.Tx` 有批量后可合并；崩溃由对账日志兜底 | 每次尝试结束各写一次：成本与收费不在一个事务里 |
| 结算事务由 billing 打开 | M1 里只有 billing、suppliers 写 | gateway 打开、各模块传 `pg.Tx` |
| 报价的单价取 `Ceiling`：所有可能的项、服务等级和长度以下的每一档中的最高价 | 档位只按基数求和，拆分可以让提示词落到任一档，价格也不一定随档上升；只有这样才是可证明的上界，性质测试按 `Cost` 逐次计价核对 | 按提示词长度所在的那一档、最贵的那一项：基数外的项或更低的档可能更贵，会少预留 |
| 计数饱和后到达 `math.MaxInt64` 就报错，不报价 | 饱和的数比真数小，报出的价会低于上界；正常请求离这个量级差很多数量级，拒绝不误伤 | 饱和后照常报价：少预留；用 `math/big`：每个请求都分配，换来的只是让不可能的请求也有个数 |
| 计量器自己算出追加的累计目标，放进 `ExtendRequest.Target` | 目标只取决于报价的各部分和已用量，所有者在请求时就知道批准后覆盖到哪里；`Extend` 只做比较后再写，重试同一段幂等 | 草图里的 `Used []Segment` 交给 `Extend` 重算：要分配切片，`Meter` 也得再算一遍才知道新段的边界 |
| 输入侧的准数不让计量后退 | 报价按每次采样的上界预留输入，之后的采样还会重读上下文；把报告的小数挪给输出会让输出越过预留 | 用报告的输入准数算花费：多采样时少预留 |
| 同一段号失败后原样重发，不按新进度重算 | 计量器批准的覆盖范围由请求决定，重发同一个请求，确认丢失时第一次提交的就是它要的 | 每次按最新进度重算：计量器以为批准的与库里提交的不一致 |
| `Extend` 以目标是否已被覆盖判断成功，不看段号 | 同一请求的几次尝试各自从第 1 段编号，后一次的段号可能前一次已用过；按段号判断会把更大的目标当成重试放行，少预留 | 请求内统一编号：要在尝试之间传递计量器状态 |
| 一步同时报 `Extend` 和 `Exhausted` | 一次读可以同时越过追加点和当前段；只报其一，所有者要么停读却没有追加可等，要么继续读越过预留 | 枚举 Continue / ExtendNow / Exhausted |
| 报价是纯函数 `Quote`，`Reserve` 在锁下调用它 | 报价规则最多、最容易错，脱开数据库才能用表驱动和性质测试逐条证明；锁内只多一次内存计算 | 在 `Reserve` 里边读边算：规则只能经数据库测试 |
| 每条分录带产生它的租户，账户名记在 `book` | 结算在账户范围的事务里写收入和成本分录，没有租户的行过不了行级安全；平台的报表在平台范围按 `book` 汇总 | 平台科目的分录不带租户：账户范围写不进 |
| 校正冲正再重记，不记差额 | 账本的规定（「账本」的费用校正）：每次有效的结算只有一笔，冲正可追溯；用同一个版本号做幂等键 | 记差额：省一笔，但有效结算要靠累加才看得出 |
| 暂估在窗口内保留，校正后不立刻释放 | 同一请求可能先后来几个版本的供应商记录，每次补扣都要从保留里扣才守得住底线 | 第一次校正就释放：之后的版本没有保留可扣 |
| 免单成本按这个请求全部尝试的成本计 | 上限是为了限住平台为免单付出的钱，那就是供应商对这些尝试收的成本 | 按免掉的售价计：与平台的实际损失无关 |
| 锁定的价目表存进预留行 | 清理和校正没有准入时的进程，只能从库里拿到锁定价格；`PriceRef` 还没有 | 只在内存里：进程一死，清理结算就没有价可用 |
| 写入栅栏同时要求 `reaped_at IS NULL` 和未过期 | 回收后心跳续不上，正常情况下被回收的必已过期；但数据库时钟回拨（如主备切换）会让过期时间重新落在未来，只看过期就会放行已回收的持有者 | 只看 `expires_at` |
| 计数按 UTC 日存，限额按含某天的窗口求和；准入按今天，追加按预留那天；时间按数据库时钟、在锁后的读语句里算 | 追加归到预留那天，检查也必须在那天所在的窗口，否则跨过边界后看到的是空窗口、写进的是旧窗口，能一直越过限额；与栅栏同一个时钟，多节点的本地时钟不一致也落在同一天；限额改了重置周期也不会漏算 | 追加按当前窗口检查：跨边界后检查与归账错位；追加改记到当天：一个预留的占用散在几天里，结算要逐天释放；每个限额窗口一行：改周期后漏算 |
| `Reserve` 接受 billing 自己的 `Admitted` | `accounts.Admit` 还没有；`Admitted` 只带预留要的字段和范围，范围仍只能由 accounts 构造后传入 | 等 accounts 落地再做预留 |
| 可回收的持有者 + 截止时间；写入的 `FOR KEY SHARE` 与回收的 `FOR UPDATE` 互斥 | 进程死了、活着但漏结算、库长时间不可用都有人收尾，回收前的能续上 | `expires_at > now()` 的节点租约：时间可能过时、清理与写入无互斥、库恢复后回收活着的进程 |
