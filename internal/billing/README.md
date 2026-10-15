# billing

预留、计量、追加、结算、清理与校正在代码层面的接口和规则。钱怎么算、收谁多少见[计费](../../docs/domains/billing.md)，一次调用的编排见 [gateway](../gateway/README.md)；这里只写调用路径上下游代码要依赖的部分，定价、账本、额度批次的代码结构随实现补入本文。

> 本文先于代码写成，是 [Issue #28](https://github.com/TYCT-tyct/Axiom/issues/28) 在调用路径上的设计（PR B1–B6，见 [gateway 的「PR 拆分」](../gateway/README.md#pr-拆分)）；代码与本文不一致时，在同一个 PR 里改本文。已实现的只有报价 `Quote`（B2 的纯内存部分，见下文「报价」）；其余是设计，尚未实现。

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
type Model struct{ ContextLength, ImageTokens, PageTokens, OutputSpeed int64; MaxUses map[catalog.Item]int64 } // 调用方从目录快照填
type Segments struct{ Output int64; Overshoot time.Duration } // 站点设置：每段输出 token；追加等待加取消生效的延迟上限
func Quote(d Demand, q []Quotable, s Segments, avail []Available) (Reservation, error) // 已实现，纯函数；Reserve 在锁下调用它
type Reservation struct{ Amount platform.Exact; Kept []int }
func (b *Billing) Reserve(ctx context.Context, tx pg.Tx, a accounts.Admission, id platform.RequestID,
	d Demand, q []Quotable) (*Hold, []int, error) // []int：保留条目在 q（即原路线）里的下标；*Insufficient{Source, Transient}
func (h *Hold) Bind(seq platform.AttemptSeq, entry int) *Meter // entry 是原路线下标；进入 Send 时调用
func (m *Meter) Observe(p Progress) MeterStep // 纯内存、只归所有者：Continue | ExtendNow(ExtendRequest) | Exhausted
func (m *Meter) Extended(seq int, err error)  // 所有者取回追加结果后调用，更新已批准段号、解除"追加中"
type ExtendRequest struct{ Seq int; Entry int; Used []Segment } // 不可变快照
func (b *Billing) Extend(ctx context.Context, h *Hold, r ExtendRequest) error // ErrTerminated、*Insufficient；不读 Meter
func (b *Billing) Settle(ctx context.Context, h *Hold, o Outcome) (Final, error) // 前提：无追加在途；*Permanent 不再重试
func (b *Billing) SettleOrphan(a accounts.Admission, id platform.RequestID) // 非阻塞，进程内队列
func (b *Billing) Run(ctx context.Context) error // 心跳、回收、截止时间清理、按 ChargeSettled 校正、到期释放保留
type Progress struct{ ContentBytes, PartialBytes, Searches int64; Reported Segment; Final catalog.ItemSet } // gateway 从 protocol.Progress 复制；Final：已是最终值的已报告项
type Segment struct{ Usage catalog.Usage; Reported catalog.ItemSet }
type Outcome struct{ Attempts []AttemptResult; Cancelled bool } // 收费的是最后一个
type AttemptResult struct {
	Seq platform.AttemptSeq; Entry int; Receipt suppliers.AttemptReceipt; Segments []Segment
	Result Result // NotExecuted | Unknown | FailedBeforeCommit | Aggregated | Committed；失败时前两种取自 Failure.Executed
	Finish FinishReason; Failure *suppliers.Failure
}
type FinishReason uint8 // Stop | Length | ToolUse | Refusal | Error | Truncated | Timeout | Cancelled
type Final struct{ Segments []Segment; Cost platform.Exact; Currency platform.Currency; Estimated bool }
```

- 金额沿用 `platform.Exact`；`platform.RequestID`（即调用编号）、`platform.AttemptSeq`（从 1 起，进入 `Send` 时分配）由第一个用到它们的 PR 加进 `platform`。价格版本只追加、不删除，`PriceRef` 就是版本号。
- 后续事务（追加、结算、`SettleOrphan`）的账户范围只能从 `accounts.Admission.Scope()` 取得。
- `Reserve` 在 accounts 打开的准入事务里执行；`Settle` 在自己的事务里调用 `suppliers.RecordAttempts`、`RecordLostRequest`、`ResolveLostRequest`。这是[总体架构规则 5](../../docs/architecture.md#模块边界规则) 的两处跨模块同事务。
- **输出上界**（`Observe` 用它）：字节上界 = 已完整读到的事件里解码后的正文、推理、工具参数的 UTF-8 字节（不含 SSE、JSON 外壳和签名；token 不多于字节）+ 正在读的不完整事件已到的原始字节（`PartialBytes`）；搜索按事件计数。已报告的输入侧项（输入、缓存读写）一经报告就是准数；输出侧的项（输出、推理、搜索）在最后的用量事件之前报告的只是下界（Anthropic `message_start` 里的 `output_tokens`、任何累计中的计数），所以在 `Final` 标明它之前，计量的输出 = max(已报告, 字节上界)。测试用已有的样本 `internal/protocol/testdata/messages/text/upstream.sse`：`message_start` 报 `output_tokens: 1`，之后的正文仍按字节计量、越过当前段时触发追加，`message_delta` 的 15 才是最终值。`Relay.Next` 每做一次至多 16 KiB 的底层读就返回，所以两次计量之间没计量的输出不超过一次读。
- **`Segmentable`** 由 routing 按条目和请求算出（见 [routing 的「接口」](../routing/README.md#接口草图)）；不可分段的条目全额预留、不追加、不截断。

## 报价

`Quote` 按[计费的「预留与结算」](../../docs/domains/billing.md#预留与结算)给每个条目报价，删掉付不起的，返回保留条目报价的最大值。不读库，`Reserve` 在锁下读出余量后调用它，所以报价规则不需要数据库就能逐条测试。

- **单价**：每一项都按 `catalog.Schedule.Ceiling` 取上界：请求可能产生的输入侧项（总含 `input`）在请求可能用到的服务等级（总含标准档）里、在提示词可能落入的每一档中的最高单价。档位只看基数里的项，提示词怎么拆分都可能落到长度以下的任一档，所以不能只取一档或一项。输入第一次采样按首次提示词的上界取档，之后每次按上下文长度；输出（`output`、`reasoning` 取高者）和按次项目按最长的提示词取档。
- **数量**：首次提示词 = min(上下文长度, 文本字节 + 图片数 × 单张图片上限 + 页数 × 单页上限)；采样次数 = 1 + 各服务端工具最大次数之和（`MaxUses` 为 0 用该条目模型的默认值，同一工具列两次就加两次），开启压缩再乘 2，之后每次按上下文长度；输出 = 输出上限 × max(`Choices`, 1)，可分段的条目改为 min(它, 一段) + 超额额度，超额额度 = ⌈`Overshoot` × 最快输出速度⌉ token，按输出单价。
- **溢出**：计数饱和在 `math.MaxInt64` 而不回绕；饱和会少算，所以除首次提示词（被上下文长度截住）外，任何计数到达 `math.MaxInt64` 的条目报错不报价。在此之内每项乘积小于 2¹²³，至多 `NumItems` + 3 项相加，128 位不会溢出。
- **缺价、模型没有上下文长度**：返回错误，整个请求不报价。上架门槛保证可能用到的项都有价，所以这是数据错误，不按 0 计也不猜价。
- **余量**：`[]Available` 按顺序列出各来源（余额、站长余额、工作区预算、key 上限）的余量；报价不超过每一个余量的条目保留，相等算付得起。首选条目被删时返回 `*Insufficient`，指明列表里第一个不够的来源；其余被删的条目只是不进 `Kept`。`Transient`（限额窗口下一刻会恢复）随 B2 的限额一起加。
- **测试**：表驱动用例逐条核对上面每条规则的金额；两个性质测试：报价不低于按 `catalog.Schedule.Cost` 逐次采样计价的任意一次合规调用（随机价目表让低档更贵、缓存写比输入贵都经常出现），以及与 `math/big` 无界计算的结果相等、计数越界时报错。

## 准入里的预留

`Reserve` 最后执行，两条语句同批发出：先 `SELECT … FOR UPDATE` 锁本模块的**余额行**，并以 `FOR KEY SHARE` 核对持有者（见下文）；后一条语句读余额、在途预留、工作区与 key 的计数和占用。READ COMMITTED 下后一条语句在锁取得后才开始、取新快照，所以看得到锁前事务提交的全部修改；一条语句锁读多表做不到，被等待的锁只刷新被锁的那一行。然后在内存里给每个条目报价，删掉报价超过余量的条目，按剩余的最高报价预留，首选被删则 `*Insufficient`；一条语句插入预留行并更新三层占用。

- 余额行是账户资金写入的唯一串行点（[计费的「预留与结算」](../../docs/domains/billing.md#预留与结算)），锁两个账户时的顺序也在那里。没有余额行的账户按余额 0 处理，行在开户时由本模块建。
- 报价规则（每个条目自己报价、首段的组成、输入按采样次数的上界）同在那一节。采样次数和按次项目的最大次数在报价时逐条目算：请求没写某个服务端工具的最大次数，就取该条目模型在 catalog 里的默认值，所以跨模型路线的条目可以不同。报价的测试至少包括：请求没写工具的最大次数；路线里两个条目的模型默认次数不同；开启服务端压缩时采样次数乘 2。
- 余额不足是正常结果（`*Insufficient`），不让事务出错。

## 追加与结算

- **`Extend`**：一个事务，锁余额行并核对持有者，按 `req` 绑定条目的价格版本和限额值算**累计目标**（取它与已预留的较大值，只增不减），以 `WHERE request_id=$r AND segment=$seq-1 AND state='pending'` 写成 `seq`。同一 `seq` 重试幂等；0 行时重读，已是 `seq` 即成功，已终结或持有者被回收返回 `ErrTerminated`。
- **`Settle`**：以 `Admission.Scope()` 开事务，同批先写不需要账户锁的行（每次尝试的成本分录、`suppliers.RecordAttempts` 的对账键，都是 `ON CONFLICT DO NOTHING`），再锁余额行、读预留；按[免单规则](../../docs/domains/billing.md#免单规则)收费、记账本和花费计数；预留从挂起比较后再写到终态（暂估的转"已暂估"并保留余额）；COMMIT。5 次往返（BEGIN、设范围、批 1、批 2、COMMIT），持锁约 2 次往返加提交。各段先按精确值求和再取整一次。
- `Final` 是补估后的最终用量（响应和账本用同一份）、账上**实收**金额（被底线截断时为实收）、是否暂估。比较后再写失败时按预留的持久化终态分支（下文「预留的状态」）；同一请求重试返回已存的 `Final`。
- **`SettleOrphan`**：准入的 COMMIT 确认丢失时由 gateway 交来。一个事务，先锁该账户余额行（准入事务持有它直到结束，所以拿到锁时准入已提交或已回滚），有挂起预留就以"没有尝试"释放，没有就插入一行"已释放"墓碑；数据库错误退避重试到成功，进程死了由清理接管。

## 持有者与截止时间

**持有者**是进程每次启动生成的 UUID，一行 `(holder, expires_at, reaped_at)`；时间比较一律用取锁之后的 `clock_timestamp()`，不用事务开始时的 `now()`。默认值和它们之间的关系见 [gateway 的「超时与限额」](../gateway/README.md#超时与限额)。

- **写入栅栏**：准入、追加在锁余额行之后，以 `FOR KEY SHARE` 读自己的持有者行，要求 `reaped_at IS NULL AND expires_at > clock_timestamp()`。
- **心跳**：每 10s `UPDATE … SET expires_at = clock_timestamp() + TTL WHERE holder=$h AND reaped_at IS NULL`，不检查是否已过期：库短暂不可用后，没被回收的仍能续上。普通更新不与 `FOR KEY SHARE` 冲突。影响 0 行即已被回收：进程改为未就绪、换新持有者。心跳和回收用连接池外的一条专用连接。
- **回收**：清理先 `SELECT … FOR UPDATE` 持有者行（等在途的写事务结束、挡住新的），条件 `reaped_at IS NULL AND expires_at < clock_timestamp() − 宽限`，置 `reaped_at` 并提交；之后再逐个处理它名下的挂起预留，这时已不可能有新的。
- **截止时间**：每笔预留带 `deadline`（准入 + 单次调用最长 + 结算宽限）。过了截止时间仍挂起的，不论持有者是否活着都由清理处理（漏 `Settle`、永久性错误、panic）。节点身份只作诊断。

## 预留的状态

| 状态 | 从哪来 | 迟到的 `Settle`（比较后再写失败时） |
|---|---|---|
| 挂起 | 准入 | — |
| 已结算 / 已释放 | `Settle`；`SettleOrphan`（含墓碑） | 返回已存的 `Final`，不再记账 |
| 已暂估 | 暂估的 `Settle`，保留余额，`correct_by` = 结算时刻 + 1h | 返回已存的 `Final` |
| 清理待定 | 清理：对账日志里有标了**已提交**且带对账键的尝试（已提交的必是最后一次），按日志写尝试行（用量未知）供对账匹配，保留余额，`correct_by` = 清理时刻 + 1h | 按完整的 `Outcome` 和正常规则结算，落"已结算"；尝试行只插入不改，成本差额经 `ChargeSettled` 调整 |
| 清理结算 | 清理待定的尝试对上中转站记录：按记录用量 × 锁定价格收费，释放其余 | 按完整的 `Outcome` 和正常规则算出应收，与清理已收的多退少补 |
| 清理释放 | 清理：日志里没有已提交的尝试，或已提交的没有对账键，或清理待定到 `correct_by` 仍无记录。只靠日志时，只有标了已提交的尝试会向用户收费，日志里其余的尝试只记成本：看不到的后续尝试可能才是最后一次 | 迟到的 `Settle` 带着完整的 `Outcome`，按正常规则收费（客户端取消、非流式聚合成功、免单规则都照常），补扣最多到底线、其余平台承担，并 `ResolveLostRequest` |

- **校正**：`ChargeSettled{charge_record_id, revision}` 的处理在事务里 `UPDATE … SET applied_revision=$rev WHERE applied_revision < $rev`，0 行即重复或陈旧，直接成功（River 至少一次投递）；以库时钟对照 `correct_by`，到期后只调供应商成本、不动用户。扫描任务把到 `correct_by` 的保留余额释放；对账记录先到就先释放。
- **找不到预留行**（例如主备切换丢了提交）：按清理释放处理，补扣并记丢失的请求。

## 写入的数据

| 写什么 | 事务 | 谁写 | 幂等键 / 守护 |
|---|---|---|---|
| 预留行（请求 ID、持有者、价格版本、限额窗口、段号 1、金额、截止时间、挂起）与三层占用 | 准入 | billing | 主键 `request_id`；写入栅栏；余额行锁守底线 |
| 追加第 seq 段（累计目标） | 每段一个 | billing | `WHERE request_id=$r AND segment=$seq-1 AND state='pending'`；写入栅栏 |
| 对账日志 | 每节点每秒一个 | suppliers | 见 [suppliers 的「对账日志」](../suppliers/README.md#对账日志) |
| 每次尝试的成本分录、对账键 | 结算 | billing、suppliers | `(request_id, attempt_seq, attempt_cost)`；主键 `(request_id, attempt_seq)` |
| 请求结算、余额、花费计数、预留终态；或只释放 | 结算 | billing | `(request_id, settle)`；预留从挂起比较后再写 |
| 暂估校正 | `ChargeSettled` 任务 | billing | `(request_id, attempt_seq, correct, revision)` 加 `applied_revision` 守卫 |
| 清理待定 / 清理结算 / 清理释放、丢失的请求 | 清理任务（后台角色） | billing + suppliers | 预留比较后再写；`lost_requests` 主键 `request_id` |
| 迟到补扣 / 补差 | 迟到结算 | billing + suppliers | `(request_id, makeup)`；只对清理释放、清理结算发生 |
| 调用记录 | 异步批量 | observability | 主键 `request_id`，`ON CONFLICT DO NOTHING` |

## 决策与理由

| 决策 | 理由 | 不选 |
|---|---|---|
| 先锁余额行，计数用同批的下一条语句读 | READ COMMITTED 下新语句才看得到锁前的提交；同批发出不多一次往返 | 一条语句锁读多表：读到旧快照 |
| 追加以段号比较后再写，累计目标只增不减 | 同段重试幂等，回退到便宜条目也不缩小预留 | 按增量追加 |
| 尝试成本与对账键在最后的结算事务里批量写 | 往返与尝试数无关；崩溃由对账日志兜底 | 每次尝试结束各写一次 |
| 结算事务由 billing 打开 | M1 里只有 billing、suppliers 写 | gateway 打开、各模块传 `pg.Tx` |
| 报价的单价取 `Ceiling`：所有可能的项、服务等级和长度以下的每一档中的最高价 | 档位只按基数求和，拆分可以让提示词落到任一档，价格也不一定随档上升；只有这样才是可证明的上界，性质测试按 `Cost` 逐次计价核对 | 按提示词长度所在的那一档、最贵的那一项：基数外的项或更低的档可能更贵，会少预留 |
| 计数饱和后到达 `math.MaxInt64` 就报错，不报价 | 饱和的数比真数小，报出的价会低于上界；正常请求离这个量级差很多数量级，拒绝不误伤 | 饱和后照常报价：少预留；用 `math/big`：每个请求都分配，换来的只是让不可能的请求也有个数 |
| 报价是纯函数 `Quote`，`Reserve` 在锁下调用它 | 报价规则最多、最容易错，脱开数据库才能用表驱动和性质测试逐条证明；锁内只多一次内存计算 | 在 `Reserve` 里边读边算：规则只能经数据库测试 |
| 可回收的持有者 + 截止时间；写入的 `FOR KEY SHARE` 与回收的 `FOR UPDATE` 互斥 | 进程死了、活着但漏结算、库长时间不可用都有人收尾，回收前的能续上 | `expires_at > now()` 的节点租约：时间可能过时、清理与写入无互斥、库恢复后回收活着的进程 |
