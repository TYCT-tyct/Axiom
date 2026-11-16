# operations

业务目标、运营接口的操作记录、运营操作与审批、资金上限、售价包络和审计链的公开接口与不变量。行为、规则和理由见[运营](../../docs/domains/operations.md)；本文只写调用方依赖的部分。本模块不引用被审批的模块（`suppliers`、`billing`、`payments`、`catalog`，见[模块边界规则](../../docs/architecture.md#模块边界规则)）：金额、变动和它们的依据由 `cmd/axiom` 算好传进来，目标模块的改动由 `cmd/axiom` 在同一个事务里调用。本模块不判权限，权限和两步验证在 `cmd/axiom` 的守卫里。所有函数都在调用方的事务里工作，不提交。

本模块的数据库结构由 [`00049_baseline.sql`](migrations/00049_baseline.sql) 创建；编号、等价验证与开发库重建规则见[平台迁移说明](../platform/README.md#迁移)。

## 业务目标

```go
type Metric string // calls、errors、revenue、cost、paid_users、monthly_revenue、gross_margin、retention
type Targets struct {
    Month time.Time; Revision int64
    Calls, Errors *int64; Revenue, Cost *Money
    PaidUsers *int64; MonthlyRevenue *Money; GrossMargin, Retention *int64
} // nil：这个月没设；比率是基点；Money 是十亿分之一的十进制文本加币种
func Metrics() []Metric
func Read(ctx, tx, site uuid.UUID, month time.Time) (Targets, error)
func ExpectedFor(ctx, tx, site uuid.UUID, currency platform.Currency, since, until time.Time) (Expected, error)
func Set(ctx, tx, actor audit.Actor, site uuid.UUID, set Targets, expectedRev int64, reason string, opts SetOptions) error
func (t *Targets) Check() error
```

| 不变量 | 怎么保证 |
|---|---|
| 一个站点一个月每个指标最多一行；没设的不存在，0 是设了的 | 主键 `(site_id, metric, month)`；`Targets` 用指针 |
| 读者看不到半套目标，旧表单不覆盖新目标 | `Set` 先 `LockTargets`（按 `site:YYYY-MM` 的 advisory lock），版本不等于 `expectedRev` 是 `ErrStaleRevision`、不写；八项、版本加一和审计 `targets.update` 在同一个事务 |
| 旧页面不清掉后四项 | `SetOptions.KeepCommercial` 跳过后四项的写入，审计仍记它们的现值 |
| 旧节点读不到不认识的指标 | 后四项在 `operations.commercial_targets`，前四项在 `operations.targets`；共用 `operations.target_revisions` 的版本，清空不删版本行 |
| 跨月窗口不丢目标，比率不折算 | `ExpectedFor` 逐月按 `max(since, month)`–`min(until, nextMonth)` 裁剪、按纳秒用 `big.Int` 折算，最后四舍五入一次；比率只取 `until - 1ns` 所在月；币种不同的金额行跳过 |
| 金额不经过浮点、不截断 | 表存 `numeric(38,0)`，读 `amount::text`；超出 `int64` 的折算结果被拒绝而不是截断 |

`Read` 把任何时刻截到 UTC 当月第一天，`Check` 要求月份正好是第一天零点；表的 CHECK 要求金额指标带 `^[A-Z]{3}$` 币种、其他指标不带，比率 0–10000。

## 操作记录

```go
func RecordCall(ctx, tx, c LoggedCall) (uuid.UUID, error)
func LoggedCalls(ctx, tx, credential, after uuid.UUID, pageSize int) ([]LoggedCall, bool, error)
```

`operations.operation_log` 只插入。输入用 `jsonx.MarshalCanonical` 编码，超过 8 KiB 改记 `{"truncated":true,"bytes":n}`；列是 `json` 不是 `jsonb`，因为 `jsonb` 输出时加空格，会让 Go 量着在 8 KiB 内的输入在库里超出 CHECK。`logged_at` 默认 `now()`（数据库时钟），摘要按它归日。守卫在写进记录之后才发答案。

## 运营操作与审批

```go
func Propose(ctx, tx, p Proposal) (Operation, bool, error) // Summary 非 nil 即要审批，Reasons 至少一条；bool：按键找到的旧操作
func Settle(ctx, tx, op Operation, result map[string]any, refused string) (Operation, error)
func Decide(ctx, tx, actor audit.Actor, approval uuid.UUID, approve bool, reason string) (Operation, error)
func Withdraw(ctx, tx, credential, approval uuid.UUID, reason string) (Operation, error)
func Cancel(ctx, tx, actor audit.Actor, approval uuid.UUID, reason string) (Operation, error)    // 提出方已不在
func CancelLive(ctx, tx, actor audit.Actor, credential *uuid.UUID, reason string) (int, error) // nil：全部凭证
func ExpireDue(ctx, tx, batch int) (int, error)
func RegisterExpiry(db, scope, queue, logger) error
func RaiseAlert(ctx, tx, op Operation, severity, summary string) error
func ResolveAlert(ctx, tx, op Operation) error
func AlertKey(op Operation) string // <凭证 UUID>/<agent 给的键>
func ReadAgentLimits(ctx, tx) (AgentLimits, error)
func SetAgentLimits(ctx, tx, actor audit.Actor, l AgentLimits, rev int64, reason string) (AgentLimits, error)
```

另有读：`Get`、`Operations`、`Approvals`（给凭证只列它自己的，零 ID 是负责人的全部队列）、`ApprovalOperation`。调用方先 `accounts.HoldAgent` 再 `Propose`；直接执行的在同一个事务里调目标模块再 `Settle`，批准时 `Decide` 之后同样执行再 `Settle`，目标拒绝时调用方回到保存点、以拒绝码 `Settle`。

| 不变量 | 怎么保证 |
|---|---|
| 一个凭证的一个幂等键只对应一个运营操作 | `(credential_id, idempotency_key)` 唯一，`INSERT … ON CONFLICT DO NOTHING` 占键；第二个请求等第一个结束，请求哈希相同返回现在的状态，不同是 `ErrKeyReused` |
| 批准的就是执行的 | `axiom_app` 只能改操作的 `state`、`result`、`error_code`、`updated_at` 和审批的状态与决定列；参数整份保存（规范化后超过 8 KiB 是 `ErrParamsTooLarge`，不写） |
| 审批只决定一次，过期的不能批 | 所有路径先锁运营操作、再锁审批；锁住后用 `clock_timestamp()` 判断（到期是 `least(clock_timestamp() + 30 分钟, 当天 UTC 结束)`），已决定或已到期是 `ErrNotPending`；清扫用 `SKIP LOCKED`，每批 100 个 |
| 原因和能否决定就是服务端的判定 | `Proposal.Reasons` 由判定函数给出（`weigh`、`PriceEnvelope.outside`，下架是 `ReasonAlways`），写进 `reasons`；`Approval.Decidable` 和 `Decide` 用同一个表达式、同一个时钟 |
| 吊销和停止不漏掉待审批的提案；不再存活的凭证的提案不执行 | 提案前和批准前都 `HoldAgent`（`FOR SHARE`）；凭证不在了调用方用 `Cancel` 作废 |
| 每个状态变化都有审计，写的答案和读到的一致 | 状态和 `audit.Record` 同一个事务；改状态的语句 `RETURNING` 库里的时间 |
| 已批准的操作不留在 `approved` | 直接执行和批准后都在同一个事务里 `Settle`，交给渠道的退款 `Execute`；`CancelLive` 和清扫只处理 `pending_approval` |
| 审批信号随审批开关 | 提出时同事务 `alert.Persist` 一个 `operations.approval_pending`（键是审批 ID），决定、到期、撤回、作废时同事务恢复 |
| agent 的限频只能调低 | `operations.agent_limits` 一行，每项在 1 和 `MaxRequestsPerMinute`、`MaxMoneyWritesPerMinute`、`MaxMoneyWritesPerHour`、`MaxAlertsPerHour` 之间（CHECK 和 `ErrBadLimit`）；空列读成上限；按版本改，不符是 `ErrLimitsChanged` |

## 资金上限

```go
func CapPolicies(ctx, tx) ([]CapPolicy, error)
func SetCapPolicy(ctx, tx, actor, p CapPolicy, rev int64, reason string) (CapPolicy, error)
func CapCurrency(ctx, tx, site *uuid.UUID, category Category) (platform.Currency, error)
func ProposeMoney(ctx, tx, p Proposal, m Transfer) (Operation, bool, error)
func CheckReservation(ctx, tx, op Operation) (Reservation, error)                         // 批准时复核
func CommitReservation(ctx, tx, confirm Operation, proposal uuid.UUID, object Target, proof string) (Reservation, error)
func CommitApproved(ctx, tx, op Operation, proof string) (Reservation, error)              // 批准的事务里已动了钱
func Execute(ctx, tx, op Operation, result map[string]any) (Operation, error)              // 交给目标：executing
func ExpireApproved(ctx, tx, op Operation) (Operation, error)                              // Execute 答 ErrWindowEnded 之后
func Executing(ctx, tx, tool string, after uuid.UUID, batch int) ([]Operation, error)
func Conclude(ctx, tx, id uuid.UUID, succeeded bool, proof string) (Operation, error)
func ExpireReservations(ctx, tx, batch int) (int, error)
func ReservationOf(ctx, tx, id uuid.UUID) (Reservation, bool, error)
func CapUsages(ctx, tx) ([]CapUsage, error)
func ReconcileCaps(ctx, db, scope) (int, error)
func RepairCaps(ctx, tx, actor, category Category, site *uuid.UUID, reason string) (CapPolicy, error)
func RegisterReconcile(db, scope, queue, logger) error
```

表：`operations.cap_policies`（每个 `(类别, 站点)` 一行，`UNIQUE NULLS NOT DISTINCT`，硬上限不低于自主上限）、`operations.cap_counters`（类别日、类别周、对象日三种行，CHECK 不为负）、`operations.money_actions`（每个资金操作一行：金额、币种、依据、策略版本、预留的日和周、到期、状态 `reserved`/`committed`/`released`、凭证和确认者）。

| 不变量 | 怎么保证 |
|---|---|
| 没有策略就不动钱；计数器里只有一种币种 | `ErrNoCapPolicy`；币种建了不变（`ErrCurrencyFixed`，应用角色也改不了这一列），`ProposeMoney` 核对 `Transfer` 的币种 |
| 一个窗口里 `reserved + committed` 不超过硬上限 | 判断和预留在同一个事务、持有三行计数器的锁；超过是 `*CapExceeded`，什么都不写 |
| 待批的金额也占额度；同一个键不预留两次 | 待批的也记进 `reserved`；锁计数器之后先按键找重放 |
| 只有充值不经过人 | `Category.autonomous` 是唯一一处；其他类别在自主上限内也建待审批提案 |
| 预留不记进已经过去的窗口 | 窗口按 `clock_timestamp()` 取；全部锁拿到后再看一次，这一天已结束是 `ErrWindowEnded`、整体回滚 |
| 批准不越过现在的硬上限 | `CheckReservation` 锁住计数器重算，装不下是 `ErrPolicyChanged`，预留已不在或已过它的日是 `ErrNotReserved` |
| 预留只结一次；过了提案的日不能确认 | 确认、释放、清扫、`Conclude` 都锁住资金操作再看状态；确认最后才锁资金操作，锁住时按数据库时钟判断 |
| 退款的预留不被任何清扫释放 | 退款预留的到期是 9999-12-31（`untilOutcome`），只由 `Conclude` 按结局结算；不在 `executing` 的 `Conclude` 什么都不改 |
| 计数器等于资金操作加起来的数 | 计数器只在改资金操作的同一个事务里改；`ReconcileCaps` 每分钟用一条语句比当前窗口，在策略锁下复比后暂停；`RepairCaps` 只按资金操作重算；触发器 `refuse_autonomous_money_when_paused` 让暂停的范围只收 `pending_approval` 的预留 |
| 批量操作不和别的批次互相等待 | 释放第一笔之前，`holdScopes` 按全局顺序锁齐整批的策略和计数器（`LockReservedPolicies`、`LockReservedCounters`） |

**锁的顺序**：凭证（`HoldAgent`）→ 运营操作 → 策略 → 计数器 → 审批信号。审批和资金操作只由持有所属运营操作的事务加锁，拿它们从不等待，放在哪一步都不成环；策略按类别、平台在前再按站点排；计数器先按范围，范围内按「对象种类、窗口种类、窗口开始、对象 ID」排，`LockCapCounters` 和 `LockScopeCounters` 都是它的子序列。确认最后才锁资金操作；每条动计数器的路径都先持有策略（`FOR SHARE`），否则会和补行的修复互相等待。批准赠额在计数器之后还锁计费的余额行，调用路径从不等运营的行。

## 售价包络

```go
func PriceEnvelopes(ctx, tx) ([]PriceEnvelope, error)
func SetPriceEnvelope(ctx, tx, actor, e PriceEnvelope, rev int64, reason string) (PriceEnvelope, error)
func ProposePrice(ctx, tx, p Proposal, r Repricing) (Operation, bool, error)
func ProposePrices(ctx, tx, p Proposal, site uuid.UUID, moves []Repricing, undo func() error) (Operation, bool, error)
func CountPrices(ctx, tx, site uuid.UUID, moves []Repricing) error // 批准时计数
func MovesDetail(moves []Repricing) []map[string]any                 // 最多 12 笔
func PriceUsages(ctx, tx) ([]PriceUsage, error)
```

`Repricing` 的 `Change`、`Margin` 是精确的 `*big.Rat`，nil 表示量不出比例或成本未知。表：`operations.price_envelopes`（每个站点一行）和 `operations.price_counters`（每个 `(站点, UTC 日, 模型)` 一行，模型为空的是站点行），只增不减。

| 不变量 | 怎么保证 |
|---|---|
| 没有包络就不改价 | `ErrNoPriceEnvelope` |
| 包络内才不经过人，并发的改价合起来也不越线 | 判断在持有站点行和模型行锁的事务里；`ProposePrices` 按模型 UUID 升序锁行，每笔按已计入前几笔的数判断，结果和顺序无关 |
| 越线时规则和价格都不动 | `ProposePrices` 先调调用方的 `undo`（回到保存点）再建提案；重试同样 `undo` |
| 超出包络最大值的变动不被截断后放行 | 按精确基点比较；计数记 `maxModelDailyChange`（10⁸）加一 |
| 人批过的改价照样计数 | 批准时 `CountPrices`，量不出比例的计 10000 |

锁的顺序：凭证 → billing 的站点定价政策行 → 包络 → 计数器（站点行、模型行）→ 运营操作 → 信号。刷新提案可能在政策行之后就拿定价的信号锁，不会成环，因为每条持有售价计数器的路径都先持有同一个站点的政策行。同一个键的并发重试先在政策行上排队再重放，只发布一次。

## 审计链

```go
func ChainAudit(ctx, db, scope) (int, error)
func VerifyAuditChain(ctx, db, scope) (bool, error)
func RegisterAuditChain(db, scope, queue, logger) error
func ReplicateAuditChain(ctx, db, scope, r Replica) (int, error) // Replica{Store, Target}：Target 是端点加桶名
func RegisterAuditReplica(db, scope, queue, r Replica, logger) error
func MakeAuditDigests(ctx, db, scope) (int, error)
func AuditDigests(ctx, tx, before *time.Time, n int) ([]AuditDigest, bool, error)
func RegisterAuditDigest(db, scope, queue, logger) error
```

表：`operations.audit_chain`（主键 `seq`，`entry_id` 唯一，不对审计日志建外键）、`audit_chain_faults`、`audit_chain_checks`（一行）、`audit_replica`（每个目标桶一行）、`audit_digests`（主键是日期）；应用角色对链、故障和摘要只能读和插入。

| 不变量 | 怎么保证 |
|---|---|
| 链按 `(at, id)` 覆盖链头之前的每条条目，不分叉、不跳号 | 触发器 `audit_chain_follows` 只收接在链头之后的一条；只接 `AuditChainFrontier`（60 秒前和本库每个开着的事务的 `xact_start` 取早的）之前的条目；边界单独一条语句先读，再读条目 |
| 链的格式不变 | `linkHash` 是 SHA-256(前一条的哈希 ‖ RFC 8785 字段数组)，第一个元素 `axiom.audit.v1`；`TestLinkHash` 的期望值在 Go 之外算出，改编码就要换版本号 |
| 发现不一致就停下 | `verifyBatch` 逐个对位，`haltAt` 同事务写故障、审计和信号；有故障时每个事务都什么都不做 |
| 整条核对被打断不从头来 | 位置和每一批在同一个事务里存进 `audit_chain_checks` |
| 副本只写核对过的链；写过的对象不被改写，范围不重叠 | 只复制 `checkedLinks` 通过的；批次的范围写前记进 `pending_last`，确认前不开始别的批次，重试总是同一个键和字节；写前 `Get`，字节不同就告警不写；不存在时再 `List`，范围里已有别的对象开头也告警不写；`Put` 带 `NoOverwrite` 和 SHA-256 校验 |
| 换桶后新桶有完整的链 | 进度和告警键按 `Replica.Target` 分开 |
| 每个 UTC 日恰好一份摘要、一个信号 | `audit_digests` 主键是日期，插进去了才同事务发信号；从上一份的后一天往后做、每次最多 7 天；边界先在自己的事务里读，再在 `ReadTx`（可重复读）里读那一天 |
