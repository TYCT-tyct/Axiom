# billing

预留、计量、追加、结算与清理在代码层面的接口和规则。钱怎么算、收谁多少见[计费](../../docs/domains/billing.md)，一次调用的编排见 [gateway](../gateway/README.md)；这里只写调用路径上下游代码要依赖的部分，账本、额度批次的代码结构随实现补入本文。

> 本文先于代码写成，是 [Issue #28](https://github.com/TYCT-tyct/Axiom/issues/28) 在调用路径上的设计（PR B1–B6，见 [gateway 的「PR 拆分」](../gateway/README.md#pr-拆分)）；代码与本文不一致时，在同一个 PR 里改本文。已实现的是报价 `Quote`、计量 `Meter`、预留的表、持有者、`Reserve`、`Extend`、`SettleOrphan`，以及结算 `Settle`、清理 `Cleanup`、账本、后台加额度 `AddCredit`、每日自平 `Reconcile` 和它在队列上的周期任务 `RegisterReconcile`、缺项的兜底价、售价 `Sell`／`SalePrices`，和结算写回供应商（`Suppliers`，见下文「报价」「计量」「数据」「结算与账本」「兜底价」「定价规则」）；后台循环 `Run`（心跳、回收、清理）也已实现；额度批次的分配与到期 `ExpireAccount`、`ExpireDue`、`RegisterExpire` 也已实现（见「额度批次的分配与到期」）；付费来源、退款、拒付随支付设计。

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
type Quotable struct{ Price catalog.Schedule; Model Model; MaxOutput int64; Segmentable bool; Endpoint uuid.UUID } // 已实现；Endpoint：预留作为丢失的请求的路线记下的端点；价格版本 `PriceRef` 在 routing 的条目里，不在这里
type Model struct{ ID catalog.ModelID; ContextLength, ImageTokens, PageTokens, FilePages, OutputSpeed int64; MaxUses map[catalog.Item]int64; Reports catalog.ItemSet } // 调用方从目录快照填；ImageTokens、PageTokens、FilePages 为 0 表示不知道，报价按上下文长度
type Segments struct{ Output int64; Overshoot time.Duration } // 站点设置：每段输出 token；追加等待加取消生效的延迟上限
func Quote(d Demand, q []Quotable, s Segments, avail []Available) (Reservation, error) // 已实现，纯函数；Reserve 在锁下调用它
type Reservation struct{ Amount platform.Exact; Kept []int }
func Start(ctx context.Context, db *pg.DB, platform pg.Scope, c Config, s Suppliers) (*Billing, error) // 已实现：登记一个新持有者
func (b *Billing) Heartbeat(ctx context.Context) error // 已实现；ErrReaped：进程停止准入、换新持有者（再 Start）
func (b *Billing) Reap(ctx context.Context) (int, error) // 已实现：回收过期超过宽限的持有者
func (b *Billing) Alive() bool // 已实现：本地持有者检查，纯内存：最近一次心跳成功（`Start` 算第一次）加 TTL 减 5s 晚于现在；gateway 每次尝试前问一次
func OpenBalance(ctx context.Context, tx pg.Tx, site, account uuid.UUID, floor int64) error // 已实现：开户时建余额行；cmd/axiom 把它接成 accounts 的开户回调（底线 0）
func ReadBalance(ctx context.Context, tx pg.Tx, account uuid.UUID, workspaces []uuid.UUID) (BalanceView, error) // 已实现：控制台的余额
func KeysUsed(ctx context.Context, tx pg.Tx, keys []KeyWindow) (map[uuid.UUID]int64, error) // 已实现：控制台的 key 剩余额度
func ReadFunds(ctx context.Context, tx pg.Tx, account uuid.UUID, workspaces []uuid.UUID) (Funds, error) // 已实现：余额行加这些工作区的累计 spent，不加锁；没有余额行全是 0；只读接口用
func ReadKeyUsage(ctx context.Context, tx pg.Tx, key uuid.UUID, reset Reset) (KeyUsage, error) // 已实现：key 的累计和当前 UTC 日、周、月已收，以及上限窗口里的已收加预留；只读接口用
type Admitted struct{ Scope pg.Scope; Site, Account, Workspace, Key uuid.UUID; WorkspaceBudget, KeyLimit *Limit } // 已实现；gateway 把 accounts.Admission 转成它（`billingAdmitted`），这一层转换是设计，不是临时
func (b *Billing) Reserve(ctx context.Context, tx pg.Tx, a Admitted, id uuid.UUID,
	d Demand, q []Quotable) (*Hold, []int, error) // 已实现；[]int：保留条目在 q（即原路线）里的下标；*Insufficient{Source, Transient}、ErrTerminated
func (h *Hold) Bind(entry int) *Meter // 已实现（经内嵌的 Reservation）；尝试序号随结算加入
func (m *Meter) Observe(p Progress) MeterStep // 已实现，纯内存、不分配、只归所有者
type MeterStep struct{ Extend bool; Request ExtendRequest; Exhausted bool; Left int64 } // 一次读可能同时越过追加点和当前段，两者都报；Left：已批准的段还能覆盖的输出 token，全额预留为 math.MaxInt64
func (m *Meter) Extended(seq int, err error)  // 已实现；所有者取回追加结果后调用，成功才采用新段、解除"追加中"
type ExtendRequest struct{ Seq int; Entry int; Target platform.Exact } // 已实现；值类型，即不可变快照；Target 是累计目标
func (b *Billing) Extend(ctx context.Context, h *Hold, r ExtendRequest) error // 已实现；ErrTerminated、*Insufficient；不读 Meter
func (b *Billing) Settle(ctx context.Context, h *Hold, o Outcome) (Final, error) // 已实现；前提：无追加在途
func (b *Billing) Cleanup(ctx context.Context, max int32) (int, error) // 已实现：截止时间、被回收的持有者
func AddCredit(ctx context.Context, tx pg.Tx, c Credit) error         // 已实现：只有赠送；tx 须是平台范围（审计日志要求）
func ReadCredit(ctx context.Context, tx pg.Tx, id uuid.UUID) (CreditBatch, bool, error)  // 已实现：AddCredit 做的批次（金额、过期、创建时间），管理接口回答赠送时读
func ExpireAccount(ctx context.Context, tx pg.Tx, account uuid.UUID) (int64, error) // 已实现：一个账户的到期作废，tx 须是平台范围；返回作废的金额
func ExpireDue(ctx context.Context, db *pg.DB, platform pg.Scope, logger *slog.Logger) (int, error) // 已实现：对每个有到期批次的账户作废，一个账户一个事务；一个失败不拦住其余
func RegisterExpire(cfg ExpireConfig) error // 已实现：登记为队列上的周期任务 billing.expire_credit，每分钟，在队列 Start 之前调用
func Reconcile(ctx context.Context, db *pg.DB, platform pg.Scope) ([]string, error) // 已实现：每日自平，列出不平的交易和与账本不符的余额
func RegisterReconcile(cfg ReconcileConfig) error // 已实现：把自平登记为队列上的周期任务，在队列 Start 之前调用（cmd/axiom 的 startSync）
func LedgerOf(ctx, tx pg.Tx, requestID uuid.UUID) ([]LedgerTransaction, error) // 已实现：一个请求的账本交易与分录，按请求 ID 读；只从运营或管理的处理器调用，事务须是平台范围
type Suppliers interface{ RecordAttempts; RecordLostRequest; ResolveLostRequest; EndRequest } // 已实现，见「结算与账本」
func (b *Billing) SettleOrphan(ctx context.Context, a Admitted, id uuid.UUID) error // 已实现：一个事务；数据库错误由调用方退避重试
func (b *Billing) Run(ctx context.Context) error // 已实现：心跳、回收、截止时间清理
type Progress struct{ ContentBytes, PartialBytes, Searches int64; Reported catalog.Usage; Final catalog.ItemSet } // 已实现；gateway 从 protocol.Progress 复制；Final：已是最终值的已报告项
type Segment struct{ Usage catalog.Usage; Reported catalog.ItemSet } // 已实现；Reported 之外有用量的项是估的
type Outcome struct{ Attempts []AttemptResult } // 已实现；收费的是最后一个；客户端是否离开记在收费那次尝试的 Finish（Cancelled）里，不在这里
type AttemptResult struct { // 已实现
	Seq, Entry int
	Cost int64 // 这次尝试的成本，十亿分之一额度，由 suppliers 的 Credits 估算
	Segments []Segment
	Result Result // Committed（零值，照收）| Aggregated | FailedBeforeCommit | Unknown | NotExecuted；失败时后两种取自 Failure.Executed
	Finish FinishReason // FinishNone（零值）| Stop | Length | ToolUse | Refusal | Error | Truncated | Timeout | Cancelled
	Receipt suppliers.AttemptReceipt; Estimate suppliers.AttemptCost; FinishedAt time.Time // 写进 suppliers 的那一面：对账键所在的回执、供应商单位下的估算、尝试结束时间
}
type Final struct{ Segments []Segment; Charged platform.Exact; Estimated bool } // 已实现；币种随站点的额度
type LedgerTransaction struct{ ID uuid.UUID; Kind string; AttemptSeq int32; Revision int64; CreatedAt time.Time; Entries []LedgerEntry } // 已实现；LedgerOf 的一项
type LedgerEntry struct{ Book string; Amount int64 } // 已实现；借正贷负，一笔交易的分录合计为 0
```

- 金额沿用 `platform.Exact`；`platform.RequestID`（即调用编号）、`platform.AttemptSeq`（从 1 起，进入 `Send` 时分配）由第一个用到它们的 PR 加进 `platform`。价格版本只追加、不删除，`PriceRef` 就是版本号。
- 后续事务（追加、结算、`SettleOrphan`）的账户范围只能从 `accounts.Admission.Scope()` 取得。
- `Reserve` 在 accounts 打开的准入事务里执行；`Settle`、`Cleanup`（接管）在自己的事务里调用 `suppliers.RecordAttempts`、`RecordLostRequest`、`ResolveLostRequest`、`EndRequest`。这是[总体架构规则 5](../../docs/architecture.md#模块边界规则) 的两处跨模块同事务，接口直接说 suppliers 的类型，由 `suppliers.CallWrites` 实现。
- **输出上界**（`Observe` 用它）：字节上界 = 已完整读到的事件里解码后的正文、推理、工具参数、思考的签名和加密推理的 UTF-8 字节（不含 SSE 和 JSON 外壳；计哪些成员见 [protocol 的「转发（草图）」](../protocol/README.md#转发草图)的「字节上界」；token 不多于字节）+ 正在读的不完整事件已到的原始字节（`PartialBytes`）；搜索按事件计数。已报告的输入侧项（输入、缓存读写）一经报告就是准数；输出侧的项（输出、推理、搜索）在最后的用量事件之前报告的只是下界（Anthropic `message_start` 里的 `output_tokens`、任何累计中的计数），所以在 `Final` 标明它之前，计量的输出 = max(已报告, 字节上界)。测试用已有的样本 `internal/protocol/testdata/messages/text/upstream.sse`：`message_start` 报 `output_tokens: 1`，之后的正文仍按字节计量、越过当前段时触发追加，`message_delta` 的 15 才是最终值。`Relay.Next` 每做一次至多 16 KiB 的底层读就返回，所以两次计量之间没计量的输出不超过一次读。
- **`InputBound(d, m)`** 给出首次采样和任一次采样输入的最大 token 数，决定价格落在哪一档；`Quote` 和路由的最高价过滤用同一个函数。**`InputEstimate(d, m)`** 是上游没报告输入时收费用的暂估：文本字节 ÷ `InputBytesPerToken`（4，向上取整，routing 估上下文也用它）+ 每张图片 min(单张上界, 1600) + 每个文件 min(单个文件上界, 4096)，不超过上下文长度；因为每一项都不大于 `InputBound` 的对应项，暂估永远不超过预留（`TestInputEstimateNeverExceedsTheBound`）。预留仍按上界，两者分开：暂估不能高过预留，预留不能低过用户可能花的。
- **`Segmentable`** 由 routing 按条目和请求算出（见 [routing 的「接口」](../routing/README.md#接口)）；不可分段的条目全额预留、不追加、不截断。

## 控制台的读取

控制台的余额和 key 页读两个函数，都在账户范围的事务里，不加锁，几个字节旧一点无妨（页面不是准入）：

- `ReadBalance(ctx, tx, account, workspaces) BalanceView`：余额行的 `balance`、`reserved`、`floor`（没有余额行按全 0），以及 `SpentThisMonth`，即给定的这些工作区自本 UTC 月初起被收的 `spend.spent` 之和。按工作区加是因为每笔收费在 `spend` 里记两行（工作区一行、key 一行），不能全加；账户的工作区由 accounts 的 `AccountWorkspaces` 给出（含已删除的，它们的花费仍算）。按 `(subject, window_start)` 主键读，每个工作区一次索引范围扫描，与账本的大小无关。可用额度 = 余额 − 在途预留，由调用方算（要防回绕用大整数）。
- `KeysUsed(ctx, tx, []KeyWindow) map[key]int64`：每把 key 在**自己重置周期**的当前窗口里已用的（花掉的加预留的），与准入检查上限用的是同一个数（`ReadUsage`），一条语句读完所有 key；控制台的 `limit_remaining` = 上限 − 已用，不低于 0。

## 报价

`Quote` 按[计费的「预留与结算」](../../docs/domains/billing.md#预留与结算)给每个条目报价，删掉付不起的，返回保留条目报价的最大值。不读库，`Reserve` 在锁下读出余量后调用它，所以报价规则不需要数据库就能逐条测试。

- **单价**：每一项都按 `catalog.Schedule.Ceiling` 取上界：请求可能产生的输入侧项（总含 `input`；模型可能报告缓存读时含 `cache_read`，缓存命中由上游决定，不看请求）在请求可能用到的服务等级（总含标准档）里、在提示词可能落入的每一档中的最高单价。档位只看基数里的项，提示词怎么拆分都可能落到长度以下的任一档，所以不能只取一档或一项。输入第一次采样按首次提示词的上界取档，之后每次按上下文长度；输出（`output`、`reasoning` 取高者）和按次项目按最长的提示词取档；请求启用了联网搜索时最长的提示词就是上下文长度，即使最大次数为 0，因为超出次数的搜索也会新增采样。
- **数量**：首次提示词 = min(上下文长度, 文本字节 + 图片数 × 单张图片上限 + 文件数 × 单个文件上限)，单个文件上限 = 单页上限 × 单个文件最大页数，没有页数上限时为上下文长度（protocol 只数文件个数）；单张图片上限不知道（`ImageTokens` 为 0）时同样取上下文长度，M1 的模型规格没有图片和页的 token 数，所以带图片或文件的请求按上下文长度预留；采样次数 = 1 + 各服务端工具最大次数之和（`MaxUses` 为 0 用该条目模型的默认值，同一工具列两次就加两次），开启压缩再乘 2，之后每次按上下文长度；输出 = 输出上限 × max(`Choices`, 1)，可分段的条目改为 min(它, 一段) + 超额额度，超额额度 = ⌈`Overshoot` × 最快输出速度⌉ token，按输出单价。
- **溢出**：计数饱和在 `math.MaxInt64` 而不回绕；饱和会少算，所以除首次提示词（被上下文长度截住）外，任何计数到达 `math.MaxInt64` 的条目报错不报价，输出上限 × 选择数也一样，即使首段比它小：计量器按它截住输出。在此之内每项乘积小于 2¹²³，至多 `NumItems` + 3 项相加，128 位不会溢出。
- **缺价、模型没有上下文长度**：返回错误，整个请求不报价。上架门槛保证可能用到的项和等级都有价，已知的按次项目由准入补上默认单价（`withFallbacks`），所以这是数据错误，不按 0 计也不猜价。
- **余量**：`[]Available` 按顺序列出各来源（余额、站长余额、工作区预算、key 上限）的余量；报价不超过每一个余量的条目保留，相等算付得起。首选条目被删时返回 `*Insufficient`，指明列表里第一个不够的来源；其余被删的条目只是不进 `Kept`。`Transient`（限额窗口下一刻会恢复）随 B2 的限额一起加。
- **分段配置**：可分段的条目遇到没配置分段大小或超额额度（`Segments.Output`、`Overshoot` 不大于 0）时报 `errNoSegments`，不报一个输出为 0 的价（计量器把 `segment` 为 0 读成整笔预留，输出就没有上界）。M1 没有这份配置，路由在 `FirstSegment` 为 0 时也不标出可分段的条目，所以 M1 的预留全部是全额；分段的接线见 [#229](https://github.com/TYCT-tyct/Axiom/issues/229)。
- **测试**：表驱动用例逐条核对上面每条规则的金额；两个性质测试：报价不低于按 `catalog.Schedule.Cost` 逐次采样计价的任意一次合规调用（随机价目表让低档更贵、缓存写或缓存读比输入贵都经常出现，评审给的缓存读两倍于输入价的反例每次先跑），以及与 `math/big` 无界计算的结果相等、计数越界时报错。`BenchmarkQuote` 报单条目和三条目多档的耗时与分配，随 `just bench-compare` 比较分配次数。

## 计量

`Reservation.Bind(entry)` 给保留的条目建一个 `Meter`（绑定被删的条目是调用方的错，panic）。它按[gateway 的「计量与追加」](../gateway/README.md#计量与追加)只归所有者 goroutine，`Observe` 每次读之后调用一次，不分配。

- **计什么**：只计报价没按最大值预留的部分：输出，以及超过报价次数的联网搜索。输入侧（输入、缓存读写）一经报告就是准数，但不让计量后退：报价已按每次采样的上界预留输入，报告的数小于上界也不能挪给输出，因为之后的采样还可能重读整个上下文。
- **输出** = 已报告的 `output + reasoning`，在 `Final` 含 `output` 之前与字节上界（`ContentBytes + PartialBytes`）取大，任何时候不超过输出上限 × 选择数（上游不会超过它，字节上界却可能）。搜索 = max(事件数, 已报告的数)，超出报价次数的每次搜索按联网搜索的上界单价，再加它新增的采样：重读整个上下文（开启压缩时两次），按之后各次采样的输入单价上界计。
- **段**：第一段覆盖 min(一段, 输出上限) 个输出 token。花费超过当前段起点 + 一段 × 追加比例（每个请求在报价时取一次 40%–60%，向下取整，只会早不会晚）时返回 `Extend`，请求第 `seq + 1` 段：覆盖到 min(max(已覆盖, 已用) + 一段, 输出上限)，`Target` = 输入与按次项目的固定部分 + 超额额度 + 这些输出 + 超出的搜索，`Extend` 只需把它与已预留的取大写入。花费超过已覆盖的部分时返回 `Exhausted`。输出上限已覆盖时只有超出的搜索会再触发追加。
- **追加在途与重试**：`Observe` 不再返回 `Extend`，仍照常报 `Exhausted`；`Extended(seq, nil)` 采用新段。失败时保持原段，下一次 `Observe` 原样重发同一个请求（同一段号、同一 `Target`）：失败的那次可能已提交、只是确认丢了，`Extend` 对已是这个段号的会直接报成功，所以目标不能变；在途期间多出来的需求，成功之后用下一个段号请求。
- **还剩多少**：`Left` = 已批准的输出 token − 已用，不小于 0；已批准的覆盖到输出上限时为 `math.MaxInt64`（上游不会越过上限），输出单价为 0 时也是（输出不花钱，段永远用不完）；所以 `Left` 为 0 只出现在有追加到期或在途时，调用方等的就是它；gateway 用它定客户端单次写的期限，停读期间的生成不越过已批准的段加超额额度。
- **`OutputCap()`**：本次尝试的输出上限乘选择数，计量按它截住输出，调用方暂估没报告的输出时也按它封顶，是同一个值。
- **不可分段的条目**：`Observe` 只返回 `Left = math.MaxInt64`：全额预留，不追加、不截断。
- **测试**：录制样本 `internal/protocol/testdata/messages/text/upstream.sse` 逐事件计量（`message_start` 报 1 是下界，"Hello" 按字节计 5 越过追加点；最终的 15 照报告计量并越过下一段的追加点）；一个比一段还大的事件同时报两者、批准后继续；在途时不再请求、失败后原样重发、确认丢失而进度增长时；追加比例 40% 和 60% 落在段内的位置；超出的搜索按逐次采样的 `Cost` 核对覆盖；输出上限；`BenchmarkObserve` 断言 0 次分配。

## 准入里的预留

`Reserve` 最后执行，依次三条语句：先 `SELECT … FOR UPDATE` 锁本模块的**余额行**（同时读出余额、底线、在途预留），再以 `FOR KEY SHARE` 核对持有者（见下文），最后一条读工作区与 key 在各自窗口的花费和占用。READ COMMITTED 下最后一条在锁取得后才开始、取新快照，所以看得到锁前事务提交的全部修改；一条语句锁读多表做不到，被等待的锁只刷新被锁的那一行。`pg.Tx` 还没有批量发送，三条依次发出，比同批多两次往返，都在账户锁内：准入合计 9 次往返（开始、设范围、核对 key、读兜底价、锁余额行、栅栏、读计数、写预留、提交；兜底价是平台数据，在锁之前读），追加 9 次（开始、设范围、锁余额行、栅栏、读预留行、读计数、写预留行、写占用、提交），目标已覆盖时 6 次（读完预留行即提交）。然后在内存里给每个条目报价，删掉报价超过余量的条目，按剩余的最高报价预留，首选被删则 `*Insufficient`；一条语句插入预留行并更新三层占用。

- 余额行是账户资金写入的唯一串行点（[计费的「预留与结算」](../../docs/domains/billing.md#预留与结算)），锁两个账户时的顺序也在那里。没有余额行的账户按余额 0 处理，行在开户时由本模块建。
- 报价规则（每个条目自己报价、首段的组成、输入按采样次数的上界）同在那一节。采样次数和按次项目的最大次数在报价时逐条目算：请求没写某个服务端工具的最大次数，就取该条目模型在 catalog 里的默认值，所以跨模型路线的条目可以不同。报价的测试至少包括：请求没写工具的最大次数；路线里两个条目的模型默认次数不同；开启服务端压缩时采样次数乘 2。
- 余额不足是正常结果（`*Insufficient`），不让事务出错。

## 数据

迁移 `migrations/00001_reservations.sql`，查询 `queries/reservations.sql`（sqlc 生成）。金额都以十亿分之一额度存储；报价的 `Exact` 向上取整成金额（`platform.Exact.CeilAmount`）。别的模块的行只按 ID 引用，不建外键。应用没有任何表的 DELETE，UPDATE 只给下列可改的列。

| 表 | 内容 | 谁能读写 |
|---|---|---|
| `holders` | `id`、`started_at`、`expires_at`、`reaped_at` | 任何范围可读、可加 `FOR KEY SHARE`（加锁要过 UPDATE 策略的 `USING`），只有平台范围能插入和改；可改 `expires_at`、`reaped_at` |
| `balances` | `account_id`（主键）、`site_id`、`balance`（账本合计）、`floor`（≤ 0）、`reserved`（在途预留） | 账户范围；可改 `reserved`、`balance` |
| `spend` | `(subject, window_start)` 主键：工作区或 key 在一个限额窗口里的 `spent`、`reserved` | 账户范围；可改 `reserved`、`spent` |
| `reservations` | `request_id`（主键）、租户与工作区、key、`holder_id`、锁定的两个窗口、`segment`、累计的 `amount`、`state`（见「预留的状态」）、`deadline`，结算加入的列见「结算与账本」 | 账户范围；已释放、已结算的行不能改 |

- **窗口**：计数按 UTC 日存（`spend.window_start` 是那天的开始），预留行记下准入那天，追加和结算都记回那一行。限额读它含某一天的那个窗口里各天之和（`billing.window_from`、`window_to`：每日、每周、每月是那天所在的 UTC 日、周、月，不重置的是全部日子），按数据库时钟、在锁后的读语句里算：准入按今天，追加按预留行记下的那天。所以追加的检查和归账在同一个窗口，跨过 UTC 日、周、月的边界继续追加也越不过限额；改了重置周期，新窗口照样算上已花和在途的。
- **余额行**在开户时由 `OpenBalance` 建；没有余额行就没有锁可取，`Reserve` 直接返回余额不足（零价也一样），什么也不写；追加和 `SettleOrphan` 遇到它报错。
- **余量**按 `Exact` 精确算（余额 − 在途 − 底线，限额 − 窗口内已用），任何存得下的组合都不会回绕。
- **墓碑**：`SettleOrphan` 找不到预留行时写一行 `segment = 0`、`amount = 0`、`released` 的行占住请求 ID，丢失的准入不可能再落库。
- **`endpoints`**（迁移 `00005_reservations_endpoints.sql`）：`Reserve` 与 `prices` 一起写，保留条目的端点在路线里的下标处，被删的条目写零 UUID，所以两列等长同序；只在准入写一次，与 `prices` 一样，应用对它没有 UPDATE 的授权。迁移前的预留行是 `'{}'`，与 `prices` 不等长，这样的行清理出的路线为空，只在迁移时的存量里有。清理读它记丢失的请求（[suppliers 的「丢失的请求」](../suppliers/README.md#丢失的请求)），迟到的 `Settle` 用 `prices` 定价，两者出自同一条路线。

## 结算与账本

迁移 `migrations/00002_settlement.sql`，查询 `queries/settlement.sql`。

| 表 / 列 | 内容 | 谁能读写 |
|---|---|---|
| `reservations` 新增列 | `prices`（保留条目锁定的价目表，规范 JSON，按路线下标，被删的为空串）、`charged_seq` 与 `entry`（收费的那次尝试和它的条目）、`charged`（实收）、`price`（规则算出、底线截断前的应收）、`waived`（是否只收了按次项目）、`estimated`（收费是否基于上游没报告的用量或兜底价）、`final_usage`（收费所用的用量，含映射不了的，JSON，`Final` 从它读出） | 已释放、已结算的行不能再改 |
| `transactions` | 一个事件一笔：租户、`kind`（credit、settle、reverse、attempt_cost、expire）、`(request_id, attempt_seq, kind, revision)` 唯一 | 账户范围；只插入 |
| `entries` | 交易的分录：租户、`book`（user_balance、revenue、platform_borne、supplier_cost、supplier_prepaid、gift_expense、credit_expired）、`amount`（借正贷负，不为 0）；`user_balance` 有按账户的部分索引，对账一次聚合 | 账户范围；只插入 |
| `credit_batches` | 每次加额度一个批次：来源（目前只有赠送）、金额、过期时间；`consumed`、`expired` 是分配存下的已用和已作废（见「额度批次的分配与到期」） | 账户范围；插入，可改 `consumed`、`expired` |

- **收缩**（迁移 `00006_drop_unused.sql`）：删了没有读写方的 `corrections`、`waivers` 两张表，`reservations` 的 `unpriced`、`correct_by` 两列和 `estimated_by_correct_by` 索引，状态约束收窄到下文「预留的状态」的五种（`pending`、`released`、`settled`、`cleanup_settled`、`cleanup_released`）；旧版本不读写它们，滚动发布不受影响。`fallbacks` 里的 `markup` 还在：行的解析不认没声明的成员，删它要先迁移改写 `body`（[#232](https://github.com/TYCT-tyct/Axiom/issues/232)）。
- **每日自平**：`RegisterReconcile` 把 `Reconcile` 登记为队列上的周期任务 `billing.reconcile`（节点启动时先跑一次，之后每 24 小时；同一时刻只有一个在排队或在跑，多节点时只有队列的领导节点插入）。它只读，找出分录合计不为 0 的交易和与账本合计不符的余额；每个问题记一条 Error 日志（最多列 20 条，其余给总数）并计入 `axiom.billing.reconcile.problems`。队列先于 billing 启动，所以它不依赖 `*Billing`，只要数据库和平台范围。这是账本自己对平，与供应商逐条对账无关（[#227](https://github.com/TYCT-tyct/Axiom/issues/227)）。
- **账本**：一笔交易的分录合计为 0（写入时断言），用户余额是负债，`balances.balance` = −Σ user_balance 分录，与分录在同一条语句里更新；`spend.spent` 与收费在同一事务里更新。每条分录带产生它的租户，行级安全据此读；平台收入按 `book` 在平台范围汇总。按月分区随规模加入。
- **按请求读账本**：`LedgerOf(ctx, tx, requestID)` 按 `transactions.request_id` 读一个请求的全部交易，每笔带自己的分录；交易按 `created_at, id`、分录按 `book, amount` 排序。`(request_id, attempt_seq, kind, revision)` 的唯一索引以请求 ID 开头，所以是一次范围扫描加按 `transaction_id` 的连接，与账本总量无关。它是运营和管理读的内部接口，不是用户的：分录里也有供应商成本。**调用方必须用平台范围，并且只在运营或管理的处理器里调用**；`pg` 没有能判断范围的接口，函数本身拦不住，账户范围在数据库层面读得到自己的这些分录。调用记录不复制交易或分录的 ID，调用、尝试和账本都只用请求 ID 关联（[observability 的「追溯」](../observability/README.md#追溯)）。
- **`Settle`**：一个事务、账户范围。先锁余额行、读预留行，再在锁内写每次尝试的成本和 `Suppliers.RecordAttempts`，按预留状态分支（下文「预留的状态」）。收费 = min(应收, 余额 − 底线 − 其他预留占用)，按精确值算，超出部分记 `platform_borne`，事务提交后记一条 Error 日志（请求、应收、实收、承担）并计入 `axiom.billing.platform_borne` 和 `axiom.billing.platform_borne.amount`（重试时预留已是终态，不重复报）；其他预留仍被覆盖，所以并发的请求不会因为这一笔而越过底线。收费所按的尝试、免单与否、是否暂估和用量写进预留行，重试返回的就是这份 `Final`。没有尝试的结局（已预留、没发上游）只释放。语句依次发出、没有批量，尝试成本逐次写（写交易、写分录两条）：一次尝试、成本非零、走免单检查并进入终态时 13 次往返（开始、设范围、锁余额行、读预留行、每次尝试 2 条、写尝试、记收费 2 条、移动计数、写预留行、`EndRequest`、提交），每多一次尝试多 2 次；从锁余额行到提交持锁 10 次；各段先按精确值求和再向上取整一次。纯成本的分录不动余额行。
- **免单**：最后一次尝试 `NotExecuted` 不收；收费的那次尝试是因客户端离开而结束的（`Finish` 为 `FinishCancelled`）照收，客户端在一次失败的尝试之后才离开则按那次失败判断（`waivable` 只看收费的那次尝试，`Outcome` 里没有整个调用的"取消"）；内容没交到客户端（`FailedBeforeCommit`、`Unknown`），或交到了但每次采样都报告了输出、输出与推理合计为 0、结束原因为空或出错的，只收按次项目（仍按整次采样所在的档位计价）。没有上限，也不数次数：同一个账户连续多少次零输出调用都全免（`settle_test.go` 的 `TestWaiversHaveNoCap`），`billing.waivers` 表已随迁移 `00006_drop_unused.sql` 删除。判定只看这三条，与金额无关（`waivable`、`zeroOutput`）。
- **暂估**：收费的项里有上游没报告的、价目表缺价的，或有 protocol 映射不了的用量（`Usage.Unknown()`），收费标暂估，`Final.Estimated` 如实报出。缺价的都按「兜底价」收，结算和迟到的 `Settle` 都按它，所以从不免费：映射不了的用量由 `addFallbacks` 收，价目表给了项却给不了价的由 `addMissing` 收——上游按 `priority` 服务而锁定的售价只有标准档时 `Cost` 一项也价不了，token 按该价目表全部服务等级和档位里最高的 token 单价、按次项目按锁定的默认单价。已知的按次项目缺价时由准入补上的默认单价正常计价，不算暂估。上架门槛挡不住前一种：它查的是售价能不能给得出价（报价取请求等级与端点等级的交集再加标准档），不管上游回报什么。暂估不保留任何东西：预留里没用掉的部分在结算里一起释放，没有稍后校正它的作业（下文「预留的状态」）。
- **`Cleanup`**：平台范围，逐笔一个事务。挂起且过了截止时间或持有者已回收的：记一行丢失的请求（带预留冻结的路线，不含被删条目的零 UUID），再清理释放；只释放，不收费，也不留待校正的状态。一笔失败不拦后面的：错误记进返回的联合错误，其余照常接管（`sweep` 记日志，一轮一笔也没清掉才停）；每释放一笔记一条警告日志（请求和释放的金额）。供应商侧因此留着「这个请求可能产生过费用，它的尝试可能落在这些端点上」，迟到的 `Settle` 或对账据此补上。没有尝试的记录可读：内容已交到客户端、结算还没提交时节点崩了，这次交付的尝试没有成本行，对账键也没有保存，由供应商的逐笔记录补齐。
- **迟到的 `Settle`**：按完整的 `Outcome` 收（版本号 −1，先冲正清理记过的、再记这一笔），补扣最多到底线、其余平台承担，并 `ResolveLostRequest`。没有任何尝试的迟到 `Settle`（调用没到过上游）不收费，也 `ResolveLostRequest`。清理只释放、不收钱，所以冲正的那笔通常没有分录。清理不收钱而迟到的 `Settle` 收，是因为收费要按用户最终看到的用量，清理手上没有（理由见[计费的「持有者与截止时间」](../../docs/domains/billing.md#持有者与截止时间)）。找不到预留行时（主备切换丢了提交）现在返回错误，按清理释放补扣还没有：要先补一行预留。
- **`AddCredit`**：锁余额行，记 credit 交易（借赠送支出）、批次行和审计日志，同成同败；同一 ID 重复无害。审计日志只在平台范围可写，所以它在调用方的平台范围事务里执行。线下付款还不收：现金要按实付金额和锁定汇率记，随支付一起做。
- **`Suppliers`**：billing 在自己的事务里写尝试（每个尝试的成本、用量、回执、供应商单位下的估算）、丢失的请求与其解决、请求的终结（`EndRequest`）；接口在 billing，参数直接用 suppliers 的类型，`suppliers.CallWrites` 是它的实现（无状态，转发 suppliers 的 `RecordAttempts`、`RecordLostRequest`、`ResolveLostRequest`、`EndRequest`），测试用替身。
- **`EndRequest`**：预留进入终态（已结算、已释放、清理释放）的那个事务里调用，时间取写预留行时的数据库时钟，suppliers 据此知道请求已有定论。迟到的 `Settle` 只在预留行上补记，不改写请求的结束。`SettleOrphan` 释放或写墓碑时同样调用。

## 额度批次的分配与到期

[计费的「额度批次」](../../docs/domains/billing.md#额度批次)的实现。迁移 `migrations/00007_credit_expiry.sql`，查询 `queries/allocation.sql`，代码 `allocation.go`（重放，纯函数）、`expire.go`（读写和任务）。

- **序号**：`transactions.seq` 取自序列，在 INSERT 时取。每笔动用户余额的交易都在账户的余额行锁内插入（`lockAccount`），所以同一账户的余额变动，序号按提交先后递增；时间戳和 UUID 给不出这个顺序（时钟会回拨，同一次结算的冲回和结算可以同一微秒）。`(account_id, seq)` 唯一。加这一列重写了表，已有行按存放顺序编一次号；这之前没有批次会到期，顺序不影响结果。**必须在余额行锁内插入动用户余额的交易**是这个顺序成立的前提；以后加新的这类交易，要守它。
- **状态**：`credit_batches.consumed`、`expired`（剩余 = 金额 − 已用 − 已作废，建批次时为 0）；`balances.allocated_seq`（已重放到哪一笔）、`unfunded`（超出所有批次的扣费，即负余额）、`unbatched`（不属于任何批次的额度，如批次作废后又冲回的扣费）。没被重放过的账户全是 0。
- **重放**（`allocation.apply`）：按序号读账户 `user_balance` 的净借方（每笔交易一行），逐笔：`credit` 开出批次并先还欠款；`expire` 从它指名的批次里扣掉作废额；其余借方为正是扣费，按「先到期、不过期的最后、同到期先赠送、再先进账」用批次，批次不够的计入 `unfunded`，`unbatched` 排在所有批次之后；借方为负是冲回，先抵 `unfunded`，余下计入 `unbatched`。已作废的批次不复活。结果只由账本决定：从头重放和接着上次存下的位置重放相同（`TestReplayProperty` 检查）。
- **不变量**：①重放到最新后，Σ（批次剩余）+ `unbatched` − `unfunded` = `balances.balance`；②每个批次已用 + 已作废 ≤ 金额；③`unfunded` > 0 时没有批次还剩额度、`unbatched` 为 0；④到期作废不超过 `room`（余额 − 在途预留 − 底线，与结算收费的上限 `cap` 同一个函数），所以作废之前准入的预留都还收得下；⑤作废幂等：一轮作废后批次剩余为 0 或被 `room` 卡住，再跑一轮没有可作废的就什么也不写；⑥同一单位的扣费只落在一个批次上：重放在账户锁内串行进行，结算不写批次。
- **`ExpireAccount`**：平台范围，一个事务。锁余额行 → 读状态和未用完的批次 → 重放到最新 → 核对不变量①（对不上就报错，什么也不作废）→ 按到期先后对每个到期的、仍有剩余的批次作废 min(剩余, 余额可降的量)，记 `expire` 交易（借用户余额、贷 `credit_expired`；`request_id` 是批次 ID，版本号是作废前已作废的累计额）→ 再重放一次吃进自己写的交易 → 存下状态。到期的判断用数据库时钟。
- **`ExpireDue`**：按账户 ID 分页取有到期批次的账户（部分索引 `credit_batches_due`），一个账户一个事务；失败的合并报错，不拦住其余。队列任务 `billing.expire_credit` 每分钟一次，只有队列的领导节点插入，同一时刻只有一个在排队或在跑；账户作废失败只记 Error 日志、不让任务失败，因为任务重试会退避、并且重试中的任务会挡住下一轮的插入，一个坏账户会拖住所有账户。
- **以后的来源**：付费批次由支付设计加进 `credit_batches.source` 的检查约束和 `sourceRank`；退款在账户锁内先 `loadAllocation` + `catchUp` 再选批次。动用户余额的新交易种类，在 `apply` 里按借方正负归为扣费或冲回，需要指定批次的（退款、拒付）在那时单独处理。

## 兜底价

[计费的「价目表缺项」](../../docs/domains/billing.md#价目表缺项)的实现。迁移 `migrations/00003_fallbacks.sql`，查询 `queries/fallbacks.sql`，代码 `fallback.go`。

| 表 / 列 | 内容 | 谁能读写 |
|---|---|---|
| `fallbacks` | 每个记账币种一行，`body` 是规范 JSON：每种已知按次项目（`web_search`、`code_execution`）的默认单价，和一个 M1 已不用的加价倍率（见下条） | 任何范围可读；只有平台范围能写，可改 `body` |
| `reservations` 新增列 | `fallbacks`（保留条目锁定的兜底行，按路线下标，被删的为空串）、`models`（各条目的模型）、`attempt_entries`（各次尝试绑的条目，按尝试编号，未见过的编号为 -1，由结算和清理写入） | 同 `reservations` |
| `unknown_items` | `(model_id, field)` 主键：模型第一次报告的映射不了的字段、那次请求和它的租户、`cleared_at` | 写入它的租户和平台范围可读；只有平台范围能清除 |

- **按币种存**：站点的额度就是一种币种（「钱的表示」），站点自己的设置还没有，所以兜底价按币种存，所有站点共用；二级站点各自的覆盖随二级站点一起做。迁移按各项目的官方价格页初始化 USD 一行（来源与日期在迁移注释里）：联网搜索取各家最高的单次价 0.025（OpenAI 非推理模型的 web search preview，每千次 $25），代码执行取 Anthropic 一小时容器 0.05，加价 1.2。
- **校验**：币种是三个大写字母；每种已知按次项目都有、没有别的项；单价大于 0、不超过 1 个币种单位（catalog 对按次参考价的合理性上限）；加价 1 到 10（低于 1 是配置让平台亏本卖，高于 10 当作输错）。`Start` 检查全部行，并要求有 USD（主站币种）的一行，不合格不启动，`serve` 启动时同样检查（`CheckFallbacks`）；`SaveFallbacks` 保存前同样检查、记审计，不合格拒绝。加价是 M1 里已经没有消费者的设置：它只用于按供应商记录的成本校正收费，而那套校正随本 PR 删除（[计费的「用量来源与暂估」](../../docs/domains/billing.md#用量来源与暂估)）；字段留着是因为已写好的行里有它，而行的严格解析不认没声明的成员，删它要一次迁移，与「本 PR 不改迁移」一起放到后面。
- **准入锁定**：`Reserve` 在锁之前读条目币种的兜底行，用 `catalog.Schedule.Fill` 给每个服务等级缺价的已知按次项目补上默认单价，按补过的价目表报价并锁进 `prices`，兜底行锁进 `fallbacks`。之后改兜底价不影响已准入的请求。没有兜底行的币种按缺价处理（`ErrUnpriced`，整个请求不报价），所以每个预留都有兜底价可用，`unpriced` 不再出现。
- **兜底价**：像 token 的映射不了的用量（字段名以 `tokens` 结尾、取值是 0 到 10¹⁰ 的整数）按该模型所有服务等级和分档里最高的 token 单价，免单时作为模型用量不收；其余的按次数 × 锁定的最高按次默认单价，次数是取值的十进制向上取整、最多 10¹⁰，不是十进制数（认不出的服务等级、带指数的数）按 1 次，免单时作为已执行的按次项目照收。都标暂估。合法设置也能让应收超出金额的表示范围（最高默认单价 1 × 10¹⁰ 次），应收按 int64 上限饱和，实收照常以余额为限。
- **移出路由**：结算遇到任何一次执行过的尝试里映射不了的字段，按那次尝试的条目的模型写 `unknown_items`（回退前失败的模型同样移出）；冲突即已见过。条目只来自 `attempt_entries`，所以结算之前到达、带这类字段的供应商记录找不到条目；记录不改收费（M1 没有校正），要的是这个模型离开路由。第一次出现的在提交后以 error 日志告警（`Config.Logger`）。`UnknownItems` 列出未清除的，路由快照组装时据此给该模型的端点置 `routing.Endpoint.UnknownItem`（过滤原因 `unknown_item`，永久）；管理员定价后 `ClearUnknownItem`（记审计）清除；清除后同一字段再出现不再移出，按兜底价暂估。
- **监控与人工入口**：`UnknownItems` 之外没有列出暂估的入口。原来的 `FallbackHolds`（按 `(correct_by, request_id)` 翻页列出仍留着预留的暂估请求）和它的指标 `axiom.billing.fallback_holds`、`axiom.billing.fallback_holds.held` 随校正一起删掉了：它们列的是"结算后还留着预留、等校正再定"的行，而 M1 的暂估在结算里一起释放，预留里没有这种行，`correct_by` 列已随迁移 `00006_drop_unused.sql` 删除——这套代码从来没跑出过一行，留着它只是让运维以为有东西可看。命令 `axiom billing unknown-items | save-fallbacks <file> | clear-unknown-item <file>`，管理接口另接。

## 追加与结算

- **`Extend`**：一个事务，锁余额行并核对持有者，让预留覆盖计量器算好的**累计目标**（`ExtendRequest.Target`）：已覆盖即成功、什么也不写（确认丢失后的重发、前一次尝试已追加够的都在内），否则写入差额，以锁内读到的段号比较后再写，金额只增不减；已终结或持有者被回收返回 `ErrTerminated`。细节见本节末条。
- `Final` 是补估后的最终用量（响应和账本用同一份）、账上**实收**金额（被底线截断时为实收）、是否暂估。比较后再写失败时按预留的持久化终态分支（下文「预留的状态」）；同一请求重试返回已存的 `Final`。
- **`SettleOrphan`**：准入的 COMMIT 确认丢失时由 gateway 交来。一个事务，先锁该账户余额行（准入事务持有它直到结束，所以拿到锁时准入已提交或已回滚），有挂起预留就以"没有尝试"释放，没有就插入一行"已释放"墓碑，已终结的不动；重复执行无害。它是同步的一个事务，数据库错误由调用方退避重试到成功（gateway 的后台重试，`Run` 不做这件事），进程死了由清理接管。
- **`Extend`** 在锁内先读预留行：已终结返回 `ErrTerminated`；已预留的不低于累计目标就成功、什么也不写（确认丢失后的重试，或前一次尝试的追加已经覆盖）；否则把差额与各来源的余量比较，以锁内读到的段号比较后再写，并在同一事务里移动三层占用。成功当且仅当提交后的已预留不低于目标。每次尝试的计量器各自从第 1 段编号，段号只是计量器自己的账，不用来判断重试。

## 持有者与截止时间

**持有者**是进程每次启动生成的 UUID，一行 `(holder, expires_at, reaped_at)`；时间比较一律用取锁之后的 `clock_timestamp()`，不用事务开始时的 `now()`。默认值和它们之间的关系见 [gateway 的「超时与限额」](../gateway/README.md#超时与限额)。

- **写入栅栏**：准入、追加在锁余额行之后，以 `FOR KEY SHARE` 读自己的持有者行，要求 `reaped_at IS NULL AND expires_at > clock_timestamp()`。
- **实现**：`Start` 登记持有者，`Heartbeat`、`Reap` 在平台范围的事务里执行；`Run` 把心跳和清理放在各自的 goroutine 里，每次心跳有以心跳周期为限的期限；心跳"用连接池外的专用连接"还没有，`pg` 不交出连接，见 [Issue #217](https://github.com/TYCT-tyct/Axiom/issues/217)。
- **心跳**：每 10s `UPDATE … SET expires_at = clock_timestamp() + TTL WHERE holder=$h AND reaped_at IS NULL`，不检查是否已过期：库短暂不可用后，没被回收的仍能续上。普通更新不与 `FOR KEY SHARE` 冲突。影响 0 行即已被回收：`Run` 返回 `ErrReaped`，节点退出重启、换新持有者。心跳和回收将来用连接池外的一条专用连接（#217），现在与调用共用连接池。
- **回收**：清理先 `SELECT … FOR UPDATE` 持有者行（等在途的写事务结束、挡住新的），条件 `reaped_at IS NULL AND expires_at < clock_timestamp() − 宽限`，置 `reaped_at` 并提交；之后再逐个处理它名下的挂起预留，这时已不可能有新的。
- **截止时间**：每笔预留带 `deadline`（准入 + 单次调用最长 + 结算宽限）。过了截止时间仍挂起的，不论持有者是否活着都由清理处理（漏 `Settle`、永久性错误、panic）。节点身份只作诊断。

## 预留的状态

| 状态 | 从哪来 | 迟到的 `Settle`（比较后再写失败时） |
|---|---|---|
| 挂起 | 准入 | — |
| 已结算 / 已释放 | `Settle`（暂估的也在内：收费写完就释放，没有窗口）；`SettleOrphan`（含墓碑） | 返回已存的 `Final`，不再记账 |
| 清理释放 | 清理：过了截止时间仍挂起、或持有者已回收。记一行丢失的请求（带路线），不收费：崩溃的节点没留下尝试，谁也不知道那次调用走到了哪一步 | 迟到的 `Settle` 带着完整的 `Outcome`，按正常规则收费（客户端取消、非流式聚合成功、免单规则都照常），补扣最多到底线、其余平台承担，并 `ResolveLostRequest` |

- **旧库里的状态**：`cleanup_settled` 现在没有代码会写入（从前清理在对账日志里有已提交的尝试时写它；日志删了，没有写入者了），约束和 `terminal` 里仍允许，库里若有这样的行，迟到的 `Settle` 照清理释放那样补扣，只是不 `ResolveLostRequest`（这个请求没记过丢失）。`estimated`、`cleanup_pending` 两个旧状态连同分支、查询、指标和 CLI 列表一并删了：billing 的结算从没在生产里跑过，库里不会有这样的行，迁移 `00006_drop_unused.sql` 因此把它们也从状态约束里去掉了（库里若有这样的行，迁移会因约束而失败，要先结算或释放）。列名 `estimated` 与状态名是两件事，列还在、还在写。
- **找不到预留行**（例如主备切换丢了提交）：按清理释放处理，补扣并记丢失的请求。

## 写入的数据

| 写什么 | 事务 | 谁写 | 幂等键 / 守护 |
|---|---|---|---|
| 预留行（请求 ID、持有者、限额窗口、段号 1、金额、截止时间、挂起）与三层占用 | 准入 | billing | 主键 `request_id`；写入栅栏；余额行锁守底线 |
| 追加（累计目标） | 每段一个 | billing | 已覆盖即成功不写；否则 `WHERE request_id=$r AND segment=<锁内读到的> AND state='pending'`；写入栅栏 |
| 每次尝试的成本分录，以及供应商侧该尝试的成本行（用量、回执、供应商单位下的估算、结束时间） | 结算 | billing、suppliers | `(request_id, attempt_seq, attempt_cost)`；cost 分录与 `attempt_costs` 行同成同败；主键 `(request_id, attempt_seq)` |
| 请求结算、余额、花费计数、预留终态；或只释放（终态时同事务写 `EndRequest`） | 结算 | billing、suppliers | `(request_id, 0, settle, 0)`；锁内按预留状态分支 |
| 加额度 | 后台（平台范围） | billing | `(credit_id, 0, credit, 0)`；批次主键；审计日志同事务 |
| 额度到期作废、批次的已用与已作废、分配的位置 | 到期任务（平台范围），一个账户一个事务 | billing | `(batch_id, 0, expire, 作废前的累计作废额)`；余额行锁；重放核对余额 |
| 清理释放、丢失的请求（带路线）、`EndRequest` | 清理任务（后台角色） | billing、suppliers | 预留比较后再写；`lost_requests` 主键 `request_id` |
| 迟到补扣 / 补差 | 迟到结算 | billing + suppliers | `(request_id, 0, reverse / settle, -1)`；只对清理释放发生 |
| 调用记录 | 异步批量 | observability | 主键 `request_id`，`ON CONFLICT DO NOTHING` |

## 定价规则

已实现存储、维护（`pricing.go`、`queries/pricing.sql`、迁移 `00004_pricing_rules.sql`）与售价（`sale.go`）：规则怎么变成售价有了，`Sell`/`SalePrices` 把参考价乘成售价，管理接口的 `PreviewPricing` 用它试算；报价读的就是规则算出的售价：路由快照每次重建时（`cmd/axiom/callpath.go`）用 `SalePrices` 给每个模型算售价，条目带着它的价目表进预留（`Quotable.Price`）。成本侧（供应商单位价值、汇率、保本价）随后续 PR（[#234](https://github.com/TYCT-tyct/Axiom/issues/234)）。

一条规则：目标（端点、分组、供应商、模型、厂商、来源类型或全部）、受众（账户、等级或全部）、方法（`cost_plus`：加成率或毛利率二选一、可附固定加价；`reference_multiplier`：参考价倍率；`fixed`：整张固定价格表）和约束（最低毛利率、相对参考价的上下限）。同一站点里目标和受众相同的规则只能有一条（唯一约束），所以任意两条都能排出先后：`Rules` 按目标越具体越先、受众越具体越先、ID 排序；`Place` 把一条还没存的规则按同一顺序放进这个列表（试算用）。

- **函数**：`Rules(ctx, tx, site)`、`CreateRule(ctx, tx, actor, site, currency, input, reason)`、`UpdateRule(..., id, rev, input, reason)`，都在调用方的平台范围事务里；`currency` 是站点的结算货币，固定价格必须是它。`RuleInput.Check` 检查契约的模式表达不了的部分：目标和受众的 ID 是对应种类的 ID，每种方法只带自己的成员，比率是不为负的十进制，毛利率（含最低毛利率）小于 1，参考价的上下限大于 0 且下限不超过上限，固定价格表合法且是站点的币种。它还拒绝 M1 不生效的部分，而不是接受之后悄悄由更宽的规则定价：目标不是模型、厂商、全部（`/rule/target/kind`），方法是 `cost_plus`（`/rule/method/kind`），`reference_multiplier` 的换算方式不是 `face`（含缺省，`/rule/method/currency_conversion`），与受众只接受"全部"同理；存储形式和 `targetKinds` 的先后不动，以后放开只删这几处拒绝（`honouredTargets`）。已经存下的这类规则不受影响，照旧不生效。出错是 `*RuleError`，只带成员名与规则，不带值。
- **售价 `Sell`**：纯函数，不碰数据库。`reference_multiplier` 把参考价的每个单价（各服务等级、各档位）乘 `Multiplier`，`currency_conversion` 为 `face` 时数字直接记入站点币种——只在参考价本来就是站点币种时成立（`face` 说的不是汇率，M1 也没有汇率），为 `exchange` 时要汇率，M1 没有（`ErrUnpriced`）；`fixed` 原样用规则里的价目表。`cost_plus` 要每端点成本，M1 没有（`ErrUnpriced`）。没有方法、方法不支持、没有参考价，都是 `ErrUnpriced`：**不会悄悄按参考价卖**。四舍五入只发生一次，与供应商成本计划的 `Schedule.Scaled` 是同一段代码，所以同一个倍率在成本和售价两边得到同一个数。
- **`SalePrices`**：一份规则、一份目录快照和要问的那些模型，给出每个模型最具体的规则的售价（`map[catalog.ModelID]Sold`）；只算问到的模型，`fixed` 的价目表一个规则解析一次。`Sold.Version` 是规则 ID + `rev` + 参考价行的 SHA-256，取版本 8 的 UUID，同一份输入的版本不变，规则或参考价一改就变；固定价不读参考价，所以它的版本里参考价行是 `uuid.Nil`，发布参考价不会让固定价换版本。价格适用的模型按这个顺序看：端点、分组、供应商、模型、厂商、来源类型、全部；前面的种类 M1 没有对应的价格，所以今天只有模型、厂商、全部能命中，`RuleInput.Check` 拒绝其余的目标。规则的**约束**（最低毛利率、相对参考价的上下限）还不在售价里：前两者要每端点的成本和发布流程，随 M2 的成本侧一起接上；所以今天试算的 `cost` 和 `margin_ratio` 是空的。
- **从不删除**：停用代替删除；每次修改把当时的整条规则存进 `pricing_rule_versions`（带操作者和原因，只追加），说明过去的价格是怎么来的。
- **乐观令牌**：`rev` 从 0 起，修改必须带读到的 `rev`，不同报 `ErrStaleRule`（锁住行后比较）；目标和受众撞上另一条规则报 `ErrRuleExists`，找不到报 `ErrRuleNotFound`。
- **审计**：`pricing_rule.create`、`pricing_rule.update` 带原因，`detail` 只放规则 ID 和 `rev`。
- **范围**：两张表只有平台范围能读写；站点由调用方传入（管理接口按路径里的站点），表里的 `site_id` 不是租户隔离，是规则的归属。

## 决策与理由

| 决策 | 理由 | 不选 |
|---|---|---|
| 先锁余额行，计数用同批的下一条语句读 | READ COMMITTED 下新语句才看得到锁前的提交；同批发出不多一次往返 | 一条语句锁读多表：读到旧快照 |
| 追加按累计目标，已覆盖即成功，写入以锁内读到的段号比较后再写；金额只增不减 | 重发和前一次尝试的追加都不会多扣，回退到便宜条目也不缩小预留 | 按增量追加：重发会多扣 |
| 尝试成本与对账键在最后的结算事务里写，成本逐次尝试写 | 与收费同成同败：供应商那一行和账上的成本分录一起出现或都不出现；写入是 `unnest` 一条语句，往返与尝试次数无关。崩溃的请求没有尝试可写，由丢失的请求留下路线 | 每次尝试结束各写一次：成本与收费不在一个事务里，崩溃时两边不一致 |
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
| 收费的那次尝试是否因客户端离开而结束，看它自己的 `Finish`，不看整个调用 | 失败的尝试之后客户端才断开，不该让这次失败被收模型用量；`Outcome.Cancelled` 记的是调用结束那一刻的状态，会把前面那次失败也判成取消。每条"客户端走了"的路径都已把那次尝试的 `Finish` 记为 `FinishCancelled`（`classify`、`complete`、`pass` 和 `failed` 都先查 `isGone`，包括请求已写出、等响应头时客户端离开），所以判断不丢任何一种取消 | 留 `Outcome.Cancelled`：整个调用一个标志，判错的是它 |
| 免单只由三条规则决定，不看金额、不数次数 | 收不收一个调用是产品规则，不是账务计算：把它挂到金额上，就要先知道这次调用值多少，判定和记账就纠缠在一起；不设上限是因为上限让同一份用量时收时不收，用户没法预期，而平台为免单付出的钱由「零输出」这条规则自己限住（没有输出就没有可交付的内容） | 每天按免单成本设上限：要一个所有账户范围都能写的计数，行级安全的口径待定，且判定依赖金额 |
| 锁定的价目表存进预留行 | 清理和迟到的 `Settle` 没有准入时的进程，只能从库里拿到锁定价格；`PriceRef` 还没有 | 只在内存里：进程一死，清理结算就没有价可用 |
| 兜底价在准入时锁进预留行，按次默认单价直接补进锁定的价目表 | 与价目表同理：清理没有准入时的进程，管理员改价也不该改在途请求的收费；补进价目表后报价、结算都按同一份算，不需要另一条缺价路径。代价是准入多一次往返（在锁外） | 结算时现读：改价会改在途请求，结算也多一次往返；缓存在内存：`axiom billing` 是另一个进程，改了不生效 |
| 缺兜底行的币种不报价，`unpriced` 状态随之取消 | 每个预留都有兜底价，映射不了的用量总有价可收，迟到的 `Settle` 也不会免费终结；缺行是配置错误，拒绝调用立刻暴露 | 留着 `unpriced` 作最后防线：清理之后已没有保留可扣，迟到的结算无法保持暂估（#112 裁决） |
| `Cost` 报告为缺价的项，结算按兜底价补收（`addMissing`） | 价目表缺一项不等于这次调用免费：上游回报了我们没定价的等级时，`Cost` 会把各项都报告为缺价，只给它加 `Unknown()` 的兜底价就按 0 收了（#216）。补收只用已有的兜底价，不新造价格，也不拒绝已经发生的调用。为什么不与 token 同取模型自己的价，见[计费的决策表](../../docs/domains/billing.md#决策与理由) | 按 0 收：正常交付的回答免费，供应商照样向我们收；按整笔预留收：不可解释 |
| 上架门槛和路由的售价检查都跳过已知按次项目（`Unpriced`） | 准入保证每个币种的兜底行有这些默认单价并 `Fill` 进锁定的价目表，报价和结算都有价；不跳过就会因为一条固定价没写 `web_search` 把整个模型移出路由，普通文本调用也 404（#216） | 要求固定价也写全按次项目：模型因为一个用不上的工具消失，而准入本来就补上了价 |
| 售价是纯函数 `Sell`，倍率乘在 `catalog.Schedule` 上，进位一次 | 与报价同理：定价规则最多、最容易错，脱开数据库才能用表驱动逐条证明；乘在价目表上而不是乘在金额上，成本侧和售价侧用同一段代码（`Schedule.Scaled`），同一个倍率两边得到同一个数，保本线比较才有意义 | 在 `Settle` 里乘金额：每次尝试都要重算，进位的次数随尝试数变，成本与售价可能差一个最小单位 |
| 没有规则就没有售价，`PreviewPricing` 显示 `unpriced` | 「缺价不猜」：悄悄按参考价卖等于让定价规则形同虚设，站长以为设了价其实没生效 | 回落参考价：管理面看不出规则没生效，账也按一个没人决定的价收 |
| 写入栅栏同时要求 `reaped_at IS NULL` 和未过期 | 回收后心跳续不上，正常情况下被回收的必已过期；但数据库时钟回拨（如主备切换）会让过期时间重新落在未来，只看过期就会放行已回收的持有者 | 只看 `expires_at` |
| 计数按 UTC 日存，限额按含某天的窗口求和；准入按今天，追加按预留那天；时间按数据库时钟、在锁后的读语句里算 | 追加归到预留那天，检查也必须在那天所在的窗口，否则跨过边界后看到的是空窗口、写进的是旧窗口，能一直越过限额；与栅栏同一个时钟，多节点的本地时钟不一致也落在同一天；限额改了重置周期也不会漏算 | 追加按当前窗口检查：跨边界后检查与归账错位；追加改记到当天：一个预留的占用散在几天里，结算要逐天释放；每个限额窗口一行：改周期后漏算 |
| `Reserve` 接受 billing 自己的 `Admitted` | `Admitted` 只带预留要的字段和范围，范围仍只能由 accounts 构造后传入；gateway 把 `accounts.Admission` 转成它，billing 的测试可以直接构造 | 直接收 `accounts.Admission`：测试要先造出整个准入 |
| 可回收的持有者 + 截止时间；写入的 `FOR KEY SHARE` 与回收的 `FOR UPDATE` 互斥 | 进程死了、活着但漏结算、库长时间不可用都有人收尾，回收前的能续上 | `expires_at > now()` 的节点租约：时间可能过时、清理与写入无互斥、库恢复后回收活着的进程 |
