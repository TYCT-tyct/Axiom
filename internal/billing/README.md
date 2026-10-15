# billing

预留、计量、追加、结算、清理与校正在代码层面的接口和规则。钱怎么算、收谁多少见[计费](../../docs/domains/billing.md)，一次调用的编排见 [gateway](../gateway/README.md)；这里只写调用路径上下游代码要依赖的部分，定价、账本、额度批次的代码结构随实现补入本文。

> 本文先于代码写成，是 [Issue #28](https://github.com/TYCT-tyct/Axiom/issues/28) 在调用路径上的设计（PR B1–B6，见 [gateway 的「PR 拆分」](../gateway/README.md#pr-拆分)）；代码与本文不一致时，在同一个 PR 里改本文。

## 接口（草图）

```go
// 引用 catalog、suppliers、quality、accounts；不引用 protocol、routing
type Demand struct { // gateway 由 protocol.Request 填写
	TextBytes, Images, Documents, MaxOutput, Choices, WebSearches, CodeExecutions, Samplings int64
	InputItems catalog.ItemSet; ServiceTiers catalog.ServiceTierSet
}
type Quotable struct{ Endpoint suppliers.EndpointID; Model catalog.ModelID; Price PriceRef; MaxOutput int64; Segmentable bool }
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
type Progress struct{ ContentBytes, PartialBytes, Searches int64; Reported Segment } // gateway 从 protocol.Progress 复制
type Segment struct{ Usage catalog.Usage; Reported catalog.ItemSet }
type Outcome struct{ Attempts []AttemptResult; Cancelled bool } // 收费的是最后一个
type AttemptResult struct {
	Seq platform.AttemptSeq; Entry int; Receipt suppliers.AttemptReceipt; Segments []Segment
	Result Result // NotExecuted | Unknown | FailedBeforeCommit | Aggregated | Committed
	Finish FinishReason; Failure *suppliers.Failure
}
type FinishReason uint8 // Stop | Length | ToolUse | Refusal | Error | Truncated | Timeout | Cancelled
type Final struct{ Segments []Segment; Cost platform.Exact; Currency platform.Currency; Estimated bool }
```

- 金额沿用 `platform.Exact`；`platform.RequestID`（即调用编号）、`platform.AttemptSeq`（从 1 起，进入 `Send` 时分配）由第一个用到它们的 PR 加进 `platform`。价格版本只追加、不删除，`PriceRef` 就是版本号。
- 后续事务（追加、结算、`SettleOrphan`）的账户范围只能从 `accounts.Admission.Scope()` 取得。
- `Reserve` 在 accounts 打开的准入事务里执行；`Settle` 在自己的事务里调用 `suppliers.RecordAttempts`、`RecordLostRequest`、`ResolveLostRequest`。这是[总体架构规则 5](../../docs/architecture.md#模块边界规则) 的两处跨模块同事务。
- **输出上界**（`Observe` 用它）：已报告的项用准数；其余 = 已完整读到的事件里解码后的正文、推理、工具参数的 UTF-8 字节（不含 SSE、JSON 外壳和签名；token 不多于字节）+ 正在读的不完整事件已到的原始字节（`PartialBytes`）；搜索按事件计数。`Relay.Next` 每做一次至多 16 KiB 的底层读就返回，所以两次计量之间没计量的输出不超过一次读。
- **`Segmentable`** 由 routing 按条目和请求算出（见 [routing 的「接口」](../routing/README.md#接口草图)）；不可分段的条目全额预留、不追加、不截断。

## 准入里的预留

`Reserve` 最后执行，两条语句同批发出：先 `SELECT … FOR UPDATE` 锁本模块的**余额行**，并以 `FOR KEY SHARE` 核对持有者（见下文）；后一条语句读余额、在途预留、工作区与 key 的计数和占用。READ COMMITTED 下后一条语句在锁取得后才开始、取新快照，所以看得到锁前事务提交的全部修改；一条语句锁读多表做不到，被等待的锁只刷新被锁的那一行。然后在内存里给每个条目报价，删掉报价超过余量的条目，按剩余的最高报价预留，首选被删则 `*Insufficient`；一条语句插入预留行并更新三层占用。

- 余额行是账户资金写入的唯一串行点（[计费的「预留与结算」](../../docs/domains/billing.md#预留与结算)），锁两个账户时的顺序也在那里。没有余额行的账户按余额 0 处理，行在开户时由本模块建。
- 报价规则（每个条目自己报价、首段的组成、输入按采样次数的上界）同在那一节。
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
| 清理待定 | 清理：对账日志里有标了**已提交**且带对账键的尝试（已提交的必是最后一次），按日志写尝试行（用量未知）供对账匹配，保留余额，`correct_by` = 清理时刻 + 1h | 按真实用量正常结算，落"已结算"；尝试行只插入不改，成本差额经 `ChargeSettled` 调整 |
| 清理结算 | 清理待定的尝试对上中转站记录：按记录用量 × 锁定价格收费，释放其余 | 只补差额或退差额 |
| 清理释放 | 清理：日志里没有已提交的尝试，或已提交的没有对账键，或清理待定到 `correct_by` 仍无记录。日志里的尝试只记成本，从不据此向用户收费：看不到的后续尝试可能才是最后一次 | 补扣（只对已提交的尝试，最多到底线，其余平台承担）并 `ResolveLostRequest` |

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
| 可回收的持有者 + 截止时间；写入的 `FOR KEY SHARE` 与回收的 `FOR UPDATE` 互斥 | 进程死了、活着但漏结算、库长时间不可用都有人收尾，回收前的能续上 | `expires_at > now()` 的节点租约：时间可能过时、清理与写入无互斥、库恢复后回收活着的进程 |
