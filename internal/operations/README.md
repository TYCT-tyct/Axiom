# operations

运营看板的业务目标、运营接口的操作记录、运营操作与审批、资金上限在代码里的接口、表和不变量。规则和理由见[运营的「业务目标」](../../docs/domains/operations.md#业务目标)、[「审计」](../../docs/domains/operations.md#审计)、[「风险分级与审批」](../../docs/domains/operations.md#风险分级与审批)和[「资金操作上限」](../../docs/domains/operations.md#资金操作上限)；本文只写实现时依赖的部分。

## 接口

```go
type Metric string // calls、errors、revenue、cost、paid_users、monthly_revenue、gross_margin、retention
type Money struct{ Amount string; Currency platform.Currency } // 十亿分之一，十进制文本
type Targets struct {
    Month time.Time; Revision int64
    Calls, Errors *int64; Revenue, Cost *Money
    PaidUsers *int64; MonthlyRevenue *Money; GrossMargin, Retention *int64
} // nil：这个月没设；比率是基点
type Expected struct { /* 同样的八项，已折算到窗口；比率不折算 */ }
type SetOptions struct{ KeepCommercial bool }
func Metrics() []Metric
func Read(ctx context.Context, tx pg.Tx, site uuid.UUID, month time.Time) (Targets, error)
func ExpectedFor(ctx context.Context, tx pg.Tx, site uuid.UUID, currency platform.Currency, since, until time.Time) (Expected, error)
func Set(ctx context.Context, tx pg.Tx, actor audit.Actor, site uuid.UUID, set Targets, expectedRev int64, reason string, opts SetOptions) error
func (t *Targets) Check() error
```

- **谁调**：`cmd/axiom` 的 `GetOperationsTargets` 和 `UpdateOperationsTargets` 读写编辑器；`GetOperationsOverview` 用 `ExpectedFor` 把目标放进看板。本模块不引用 `observability` 或 `billing`：实际值由 `cmd/axiom` 算好，`Expected` 只带目标。`Metrics()` 给 API 的 `metrics` 字段，页面据此决定能编辑几项。
- **月度**：`Read` 把任何时刻截到 UTC 该月第一天；`Check` 要求 `Month` 正好是第一天零点。API 的路径参数只到 `YYYY-MM`，所以不会出现半天或本地时区的月份。
- **折算**：`ExpectedFor` 读窗口触及的每个月（`since` 所在月到 `until` 所在月）。数量和金额行按 `start = max(since, month)`、`end = min(until, nextMonth)` 裁剪，`prorate` 用 `目标 × 重叠时长 ÷ 该月时长` 四舍五入到指标的最小单位（调用、错误、付费用户到 1，金额到十亿分之一），再逐月相加。时长以 `time.Duration` 的纳秒直接参与 `big.Int` 比例，只在最后的目标单位上舍入一次，所以毫秒级跨月窗口不会先截断整秒。乘加用 `big.Int`，最后才检查结果的 `int64` 宽度和币种。窗口没有长度时返回空的 `Expected`。
- **比率**：毛利率和留存率不按窗口折算。取 `until - 1ns` 所在 UTC 自然月的那一行，原值放进 `Expected`；窗口结束正好是某月第一天时，这个月是上一个月，和数量目标对那个月给出零份额的边界一致。
- **币种**：金额行的币种和调用方给的站点记账币种不同就跳过，不换算；调用、错误、付费用户、毛利率和留存率没有币种。
- **写**：`Set` 先 `LockTargets`（`pg_advisory_xact_lock(hashtextextended(site + ":" + month, 0))`），再 `Read` 旧值和版本；`expectedRev` 不等于当前版本时返回 `ErrStaleRevision`，不写任何目标。版本相符时按 `calls`、`errors`、`revenue`、`cost` 的固定顺序 upsert 或 delete 前四项，再按 `paid_users`、`monthly_revenue`、`gross_margin`、`retention` 的固定顺序写后四项，然后把 `target_revisions` 的版本加一，最后 `audit.Record`。`opts.KeepCommercial` 为真时跳过第二个循环，后四项保持原值，但审计的 `before` 和 `after` 仍带它们的现值。这些都在调用方的事务里；成功返回前没有提交，失败由调用方回滚。
- **权限**：本模块不判权限。`sites.manage` 和 `mfa` 在 `cmd/axiom` 的 guard 里；`Set` 的 `actor` 和 `reason` 只用于审计。

## 操作记录

```go
type LoggedCall struct {
    ID, Credential uuid.UUID; Run, Tool, Reason string; Input map[string]any
    Status int; Error, RequestID string; Started, Ended time.Time; Operation uuid.UUID
}
func RecordCall(ctx context.Context, tx pg.Tx, c LoggedCall) (uuid.UUID, error)
func LoggedCalls(ctx context.Context, tx pg.Tx, credential, after uuid.UUID, pageSize int) ([]LoggedCall, bool, error)
```

- **谁调**：`cmd/axiom` 的运营接口守卫（`agentGuard`）在每个认出凭证的请求结束时调 `RecordCall`，写进去才把缓存的答案发出；`operationLogList` 用 `LoggedCalls` 让凭证读自己的记录。本模块不认凭证，`Credential` 只是 ID。
- **表**：`migrations/00003_operation_log.sql` 的 `operations.operation_log`：ID（UUIDv7）、凭证、运行 ID、工具名（`x-mcp-name`）、理由（agent 写的，不可信；可以为空：缺理由被拒的请求也记）、输入（规范 JSON，最多 8 KiB）、HTTP 状态、错误码、请求 ID、开始和结束时间，以及写操作建出或按键找到的运营操作（`operation_id`，迁移 00004 加的可空列；旧节点不写写操作，留空）。索引 `(credential_id, id DESC)` 供按凭证从新到旧分页。平台数据：只有平台范围能读写；只插入，没有 UPDATE 和 DELETE 权限。
- **输入**：读是路径和查询参数（不含理由），写还有请求体除 `reason` 以外的成员（`body`），理由取自请求体。`RecordCall` 用 `jsonx.MarshalCanonical` 编码，超过 8 KiB 改记 `{"truncated":true,"bytes":n}`，表的 CHECK 再兜一层。列是 `json` 而不是 `jsonb`：`json` 原样保存文本，CHECK 量的字节和 Go 量的相同；`jsonb` 输出时在冒号和逗号后加空格，会让刚好 8 KiB 以内的输入在库里超限、整次调用答 503。

## 运营操作与审批

```go
type Proposal struct {
    Credential, Key uuid.UUID; Tool string; Target Target; Params map[string]any
    Risk Risk; Reason, Run string; Summary map[string]any // Summary 非 nil：要负责人审批
}
func Propose(ctx, tx, p Proposal) (Operation, bool, error)                    // bool：按键找到的旧操作
func Settle(ctx, tx, op Operation, result map[string]any, refused string) (Operation, error)
func Decide(ctx, tx, actor audit.Actor, approval uuid.UUID, approve bool, reason string) (Operation, error)
func Withdraw(ctx, tx, credential, approval uuid.UUID, reason string) (Operation, error)
func Cancel(ctx, tx, actor audit.Actor, approval uuid.UUID, reason string) (Operation, error) // 提出方已不在
func CancelLive(ctx, tx, actor audit.Actor, credential *uuid.UUID, reason string) (int, error) // nil：全部凭证
func ExpireDue(ctx, tx, batch int) (int, error)
func Get / Operations / Approvals / ApprovalOperation                       // 读
func RegisterExpiry(db, scope, queue, logger) error
```

- **谁调**：`cmd/axiom` 的运营接口写操作（`operations_writes.go`）先 `accounts.HoldAgent` 再 `Propose`：不要审批的直接是 `approved`，处理器在同一个事务里调目标模块（现在只有 `catalog.ListModel`）再 `Settle`；要审批的是 `pending_approval`，带一条审批。负责人在管理接口 `ApproveOperationsApproval` 里 `Decide`，同一个事务里执行目标改动再 `Settle`；目标在提案之后变了（目标模块的 4xx 拒绝，如 `stale_revision`），改动回滚到保存点，操作以 `rejected` 和那个错误码结束，决定照样提交。吊销凭证和紧急停止在各自的事务里 `CancelLive`。本模块不引用被审批的模块（[总体架构的「模块边界规则」](../../docs/architecture.md#模块边界规则)）。
- **表**：`migrations/00004_operations.sql`。`operations.operations` 一行一个运营操作：凭证、幂等键（`(credential_id, idempotency_key)` 唯一）、请求哈希（工具、目标、规范化参数的 SHA-256）、工具名、目标种类和 ID、参数（`json`，最多 8 KiB，含目标的 `rev`）、风险类别、状态、agent 的理由、运行 ID、结果、错误码和时间。`operations.approvals` 一行一个审批：运营操作（唯一）、档位（现在只有 `human`）、状态、服务端读出的摘要、请求、到期和决定的时间、决定者（`<种类>:<ID>`）和理由。两张表都是平台数据，策略只放行平台范围；`axiom_app` 只能改操作的 `state`、`result`、`error_code`、`updated_at` 和审批的 `state` 与决定列，不能删。
- **幂等**：`Propose` 用 `INSERT … ON CONFLICT DO NOTHING` 占键；同一个键的第二个请求等第一个事务结束，找到它后比请求哈希：相同返回现在的状态（`replayed`），不同是 `ErrKeyReused`。占键和操作同事务，失败就一起回滚，同一个键可以重试。
- **到期**：审批在数据库时钟上到期，`least(now() + 30 分钟, 当天 UTC 结束)`。`Decide`、`Withdraw` 和 `Cancel` 在锁住之后用 `clock_timestamp()` 判断是否已到期，不用 `now()`：`now()` 是事务开始的时刻，到期前开始、等锁等到到期后的决定用它会看不到到期。对已到期的审批答 `ErrNotPending`，不管清扫到没到；`RegisterExpiry` 在 `archive` 队列上每分钟一轮（决定自己就拒绝过期的审批，清扫只补记录，可以排在调用记录的保留任务后面），`ExpireDue` 每批最多 100 个，以 `system:operations.approval_expiry` 记 `approval.expire`。
- **批准时复核提出方**：`ApproveOperationsApproval` 先无锁读出审批所属的凭证，`accounts.HoldAgent` 持有它，再 `Decide`，和提案时一样先凭证后操作，所以和吊销、停止的锁顺序一致。凭证已吊销、停止或过期时什么都不执行，用 `Cancel` 把审批和操作记为 `canceled`：滚动发布期间旧节点的吊销和停止只吊销凭证、不作废提案，新节点不能拿它们去执行。
- **锁的顺序**：所有路径先锁运营操作，再锁它的审批。`Decide`、`Withdraw` 先无锁读出审批所属的操作，再按这个顺序加锁、重新判断状态；`CancelLive` 按 ID 升序锁；清扫用 `FOR UPDATE OF o SKIP LOCKED`，跳过正被决定的操作，条件里带操作的状态，所以快照之后已被决定的行在新版本上重判后被排除。
- **审计与通知**：`approval.request`、`operation.succeed`、`operation.reject`、`operation.cancel` 的操作者是 `operations_agent:<凭证>`（提出、执行和撤回都以 agent 的名义；吊销和停止时的取消是负责人）；`approval.approve`、`approval.reject` 是负责人，`approval.expire` 是系统。审批提出时同事务 `alert.Persist` 一个 `operations.approval_pending` 信号（键是审批 ID），决定、到期、撤回、取消时同事务恢复它；负责人给这个种类配一条告警规则和渠道才会收到通知。

| 不变量 | 怎么保证 |
|---|---|
| 一个凭证的一个幂等键只对应一个运营操作 | 唯一约束；第二个请求等第一个提交后按请求哈希返回原操作或 `ErrKeyReused`。测试 `TestProposeOnceAKey`、`TestRaces` |
| 批准的就是执行的 | 应用角色不能改操作的工具、目标和参数；执行只读这三列。决定时再由目标模块按参数里的 `rev` 核对目标，变了就 `rejected` |
| 审批只决定一次 | 锁住操作和审批后才判断 `pending` 和到期；决定、撤回、到期、取消互斥。测试 `TestDecide`、`TestRaces`（撤回等在批准之后，得到 `ErrNotPending`；清扫跳过被持有的操作，放开后再到期） |
| 过期的审批不能被批准 | 锁住后用数据库的 `clock_timestamp()` 判断，和到期时间同一个时钟，等锁的时间也算进去；清扫晚到只推迟记录。测试 `TestRaces`（到期前开始、等锁到到期后的决定被拒绝） |
| 不再存活的凭证的提案不会执行 | 批准前持有提出方的凭证，不在了就作废。测试 `cmd/axiom` 的 `TestOperationsApprovalAfterAnOldNodesStop`（旧节点只吊销凭证，新节点批准时作废） |
| 写的答案和读到的一致 | 改状态和决定的语句 `RETURNING` 时间，答案和幂等存下的答案带的是库里的 `updated_at`、`decided_at`。测试 `TestOperationsWithdrawalPreview`、`TestOperationsApprovalAfterAnOldNodesStop` |
| 吊销和停止不会漏掉一个待审批的提案 | 提案前 `HoldAgent`（`FOR SHARE`），吊销要改同一行，只能在提案之前失败或之后取消它。测试 `TestCancelLive`、`accounts.TestHoldAgent` |
| 每个状态变化都有审计 | `finish`、`Settle` 在同一个事务里写状态和 `audit.Record`；审计写不进整个改动回滚 |
| 已批准的操作不留在 `approved` | 直接执行的和批准后的都在同一个事务里 `Settle`；所以 `CancelLive` 和清扫只处理 `pending_approval` |

## 资金上限

```go
type Category string // CategoryTopUp = "money.top_up"
type CapPolicy struct {
    Site *uuid.UUID; Category Category; Currency platform.Currency // Site nil：平台
    Single, Daily, Weekly, ObjectDaily int64; DailyCount int32   // 自主上限：都在内才不经过人
    HardSingle, HardDaily, HardWeekly int64                       // 硬上限：超过就拒绝
    Rev int64; Updated time.Time
}
type Transfer struct { Site *uuid.UUID; Category Category; Object Target; Amount int64; Currency platform.Currency; Basis, Summary map[string]any }
type CapExceeded struct { Limit string; Max, Used, Amount int64 } // error
func CapPolicies(ctx, tx) ([]CapPolicy, error)
func SetCapPolicy(ctx, tx, actor, p CapPolicy, rev int64, reason string) (CapPolicy, error)
func CapCurrency(ctx, tx, site *uuid.UUID, category Category) (platform.Currency, error)
func ProposeMoney(ctx, tx, p Proposal, m Transfer) (Operation, bool, error)
func CheckReservation(ctx, tx, op Operation) (Reservation, error)                  // 批准时复核
func CommitReservation(ctx, tx, confirm Operation, proposal uuid.UUID, object Target, proof string) (Reservation, error)
func ExpireReservations(ctx, tx, batch int) (int, error)
func ReservationOf(ctx, tx, id uuid.UUID) (Reservation, bool, error)
```

- **谁调**：`cmd/axiom` 的 `supplierTopUpPropose` 先 `HoldAgent`，用 `suppliers.Admin.TopUpBasis` 读出供应商的归属、单位价值和结算币种，按 `CapCurrency` 的币种折算金额（原生金额 × 单位价值，币种不同再乘 `billing.ExchangeRates` 的当前汇率，向上取整到 10⁻⁹），再 `ProposeMoney`；直接执行的在同一个事务里 `Settle`。批准时 `runOperation` 调 `CheckReservation`。`supplierTopUpConfirm` 先 `Propose` 一个确认操作，再 `CommitReservation`，最后 `Settle`。管理接口的 `listOperationsCapPolicies`、`setOperationsCapPolicy` 调 `CapPolicies`、`SetCapPolicy`。本模块不引用 `suppliers` 和 `billing`：金额和它的依据由调用方算好放进 `Transfer`。
- **表**：`migrations/00005_money_caps.sql`。`operations.cap_policies` 每个 `(类别, 站点)` 一行（`UNIQUE NULLS NOT DISTINCT`，站点为空是平台）：币种、五个自主上限、三个硬上限（表的 CHECK 要求每个硬上限不低于对应的自主上限）、版本。`operations.cap_counters` 每个 `(类别, 窗口种类, 窗口开始, 对象种类, 对象 ID, 站点)` 一行：类别的 UTC 日和 ISO 周（对象为空），对象的 UTC 日；记 `reserved`、`committed` 和笔数，初值 0，CHECK 不为负。`operations.money_actions` 每个资金操作一行：类别、范围、对象、金额、币种、依据（`basis`：原生金额、原生单位、单位价值行 ID 与值、结算币种、汇率的 ID、版本和分子分母、结果）、策略版本、预留所在的日和周、到期（当天 UTC 结束）、状态（`reserved`/`committed`/`released`）、付款凭证和确认它的操作。应用角色只能改策略的上限和版本、计数器的三个数、资金操作的状态与确认列；策略的币种建了就不变。
- **一笔提案**：`ProposeMoney` 先 `FOR SHARE` 锁策略（没有就是 `ErrNoCapPolicy`），用数据库时钟取当天和本周，补齐三行计数器后按「类别日、类别周、对象日」的顺序 `FOR UPDATE`；这把锁让同一范围和类别的提案排队，所以接着按键找重放（找到就返回，不再按已含它自己预留的计数器重新判断），再判断：硬上限任一超过是 `*CapExceeded`，什么都不写；自主上限任一超过（含当天笔数）就带摘要和上限前后的数（`cap`，金额是十进制文本）建待审批提案；都在内就直接 `approved`。两种都把金额记进三行的 `reserved`、笔数加一，写资金操作，一起提交。笔数数的是当天的提案，释放的也算，所以撤回再提不会刷出额度外的笔数。
- **预留的结局**：提案被驳回、撤回、作废、到期，或批准时被目标拒绝（`Settle` 带拒绝码），`finish` 和 `Settle` 都调 `release` 释放：按资金操作记下的窗口锁计数器，减掉 `reserved`，状态 `released`，审计 `reservation.release`。批准时 `CheckReservation` 锁住资金操作、策略和三行计数器，预留已不在或已过它的日是 `ErrNotReserved`；按现在的策略，这笔超过单笔硬上限、或窗口里的已用（已含这笔）超过日或周硬上限，是 `ErrPolicyChanged`（策略放宽不影响待批的提案），处理器回到保存点、以 `rejected` 结束操作并释放。充值提案成功只表示负责人可以付款：预留留到 agent 确认到账（`CommitReservation`：同一张凭证、同一家供应商的已成功提案，预留还在且没过它的日，在原来的窗口里把金额从 `reserved` 移到 `committed`，审计 `reservation.commit`），或到当天 UTC 结束由清扫释放（`ExpireReservations`，`system:operations.reservation_expiry`）。已计入 `committed` 的不会因为之后的退款或冲正退回。
- **锁的顺序**：提案是「策略 → 计数器」，之后只插入新行；批准是「运营操作 → 审批 → 资金操作 → 策略 → 计数器」，驳回、撤回、作废、到期是「运营操作 → 审批 → 资金操作 → 计数器」；确认是「被确认的运营操作 → 资金操作 → 计数器」；清扫是「资金操作（`SKIP LOCKED`）→ 计数器」；改策略只锁策略（`FOR UPDATE`，等正在按它判断的提案）。没有路径反过来先拿计数器再等运营操作或资金操作，所以不成环。
- **清扫**：`RegisterExpiry` 的同一个每分钟任务里，先到期审批，再释放过了日的预留，各自每批最多 100 个。确认在锁住后用 `clock_timestamp()` 判断是否已过日，不等清扫。
- **滚动发布**：迁移只加表和放宽 `risk` 的 CHECK，旧节点不提充值。旧节点驳回、撤回、作废或到期一个待批的充值时不会释放它的预留，预留留到当天结束由新节点的清扫释放：只会多占、不会少算。旧节点批准充值时不认识这个工具，整个批准回滚，提案仍待批。

| 不变量 | 怎么保证 |
|---|---|
| 没有策略就不动钱 | `ProposeMoney`、`CapCurrency` 找不到策略都是 `ErrNoCapPolicy`。测试 `TestCapDimensions` |
| 任何时候一个窗口里 `reserved + committed` 不超过硬上限（策略不调低时） | 判断和预留在同一个事务里、持有三行计数器的锁；超过的不建操作。测试 `TestCapDimensions`、`TestCapRaces`（两笔各自在内、合起来超过的并发提案，后一笔等前一笔提交后被拒） |
| 待批的金额也占额度 | 待审批的提案同样记进 `reserved`；释放只在操作不再执行时发生。测试 `TestReservations` |
| 同一个键不会预留两次 | 计数器锁之后先按键找重放。测试 `TestReservations`、`TestCapRaces` |
| 批准不越过现在的硬上限 | 批准时锁住计数器，按现在的策略重算硬上限，装不下就 `rejected` 并释放；放宽的不作废。测试 `TestReservations`（放宽后批准成功、调低后被拒）、`cmd/axiom` 的 `TestOperationsTopUp` |
| 预留只结一次 | 确认、释放、清扫都锁住资金操作再看状态；`settled_at` 的 CHECK 让 `reserved` 之外的状态都有结算时间。测试 `TestReservations`（第二次确认被拒）、`TestCapRaces`（清扫跳过被持有的预留） |
| 计数器里的币种只有一种 | 策略的币种建了就不变（`ErrCurrencyFixed`，应用角色也没有改这一列的权限）；`ProposeMoney` 再核对 `Transfer` 的币种 |

## 表

`targets.go`、`migrations/00001_targets.sql` 与 `migrations/00002_commercial_targets.sql`：

- `operations.targets(site_id, metric, month, amount, currency, updated_at)`，主键 `(site_id, metric, month)`；`metric` 只允许 `calls`、`errors`、`revenue`、`cost`，`month` 必须是当月第一天，`amount` 是非负 `numeric(38,0)`。
- `operations.commercial_targets(site_id, metric, month, amount, currency, updated_at)`，同样的主键和月份规则；`metric` 只允许 `paid_users`、`monthly_revenue`、`gross_margin`、`retention`。分表让旧节点的 `ReadTargets`（不按指标过滤）不会读到不认识的指标。
- `operations.target_revisions(site_id, month, revision, updated_at)`，主键 `(site_id, month)`；`revision` 从 1 开始，空月份没有行、读作 0。两张目标表共用它，清空目标的写入不删除这一行，所以版本继续增长。
- `currency` 只在 `revenue`、`cost`、`monthly_revenue` 上有值；表级 check 让“是金额指标”和“有币种”等价，币种格式是 `^[A-Z]{3}$`。毛利率和留存率是 0 到 10000 的整数基点，不能带币种。
- `targets_by_month` 和 `commercial_targets_by_month` 索引 `(site_id, month)` 供看板按窗口读多个月。
- 读查询把 `amount::text` 取出，不经过 `int64` 或浮点；调用、错误、付费用户在 Go 里 `ParseInt` 到 `int64`，比率也解析为 `int64` 并检查 0–10000，金额保留十进制文本。
- 行级安全用语句级 InitPlan：平台范围看全部，否则只允许 `site_id` 在 `platform.allowed_sites()` 里的行；每张表有 SELECT、INSERT、UPDATE、DELETE 四条策略（`target_revisions` 只有 SELECT、INSERT、UPDATE，版本不删除）。`axiom_app` 只有这些表的必要权限。
- 付费用户、月流水和留存读 `billing.credit_batches`。这个索引归 billing：`migrations/00041_platform_paid.sql` 建部分覆盖索引 `credit_batches_by_site_paid_created (site_id, created_at) INCLUDE (account_id, amount) WHERE source = 'paid'`。operations 不拥有账本表，也不在它的迁移里改账本。

## 不变量

| 不变量 | 怎么保证 |
|---|---|
| 一个站点一个月每个指标最多一行 | 两张表的主键都是 `(site_id, metric, month)`；`Set` 对每项各写一次或删一次 |
| 没设的目标不存在，0 是设了的目标 | `Targets` 用指针区分；`detail()` 省略 nil；API 的 `omitempty` 和页面分开显示“未设目标” |
| 读者看不到半套目标 | 同一次写入的八个指标、旧值读取和审计在同一个事务；同一站点和月份的写入先取 advisory lock，并发写有先后 |
| 旧表单不会覆盖新目标 | 每次成功写入（含清空）把月份版本加一；写入在锁内比较请求携带的 `expectedRev`，不符返回 `ErrStaleRevision`，不写、不审计 |
| 旧节点不会遇到不认识的指标 | 后四项在独立表；旧节点的 `ReadTargets` 只读 `operations.targets`。整组替换走旧节点没有的 `/reports/targets/{month}/all`，旧节点直接 404；旧路径只接受前四项，新节点在没有 `X-Axiom-Target-Metrics: all` 和 `all_metrics: true` 时也不写后四项 |
| 旧页面不会清掉它看不见的后四项 | `SetOptions.KeepCommercial` 跳过第二个写入循环，审计仍记录它们的现值 |
| 窗口跨月时不丢数量或金额目标 | `ExpectedFor` 读 `since` 到 `until` 触及的每个月，逐月裁剪后相加；表驱动测试覆盖整月、部分月、跨月和空窗口 |
| 比率只在结束月取值，不被折算 | `expectedFromStored` 先算 `endMonth = monthStart(until - 1ns)`，只把该月的比率行放进结果；测试覆盖跨月和月首边界 |
| 金额不经过浮点，超出 64 位的中间结果不溢出 | 表和查询用 `numeric(38,0)` 文本，折算用 `big.Int`；目标本身可超过 int64，写入只受 38 位上限约束；超宽的结果被 `countTarget` 拒绝而不是截断 |
| 币种不符的金额目标不参与对照 | `addExpected` 跳过；`Expected` 的金额只按站点当前币种呈现 |
| 未记录的成功改动不能留下 | `audit.Record` 和写入同一事务；`TestAdminOperationsTargets` 读审计的 `before`/`after`，包括旧页面写入时后四项的现值 |

## 测试

- `actions_test.go`：`TestProposeOnceAKey` 覆盖直接执行与结算、按键重放、键复用、别的凭证同一个键、审批的到期上界、审计和信号；`TestDecide` 覆盖批准、重复决定、驳回、过期后拒绝决定与清扫、撤回只限自己；`TestCancelLive` 覆盖按凭证和全部取消；`TestRaces` 用持有事务等对方进入锁等待的显式顺序覆盖同键并发、批准与撤回、等锁期间到期的决定、清扫与决定。
- `cmd/axiom/operations_writes_test.go`：`TestOperationsAPIWrites` 走真实 HTTP 和数据库，覆盖上架立即执行、重放、键复用、试运行和旧 `rev` 不写、下架等负责人、负责人看到的摘要和理由、批准后以 agent 的名义执行与审计、操作记录里的理由和输入；`TestOperationsApprovalOutcomes` 覆盖驳回、批准时模型已变、撤回与重放、紧急停止和吊销取消待审批、没有权限、别的凭证的审批、下架草稿；`TestOperationsApprovalAfterAnOldNodesStop` 覆盖旧节点停止后批准；`TestOperationsWithdrawalPreview` 覆盖撤回的试运行不改审批、审计和幂等键，以及撤回和重放的时间与详情一致。

- `caps_test.go`：`TestCapDimensions` 表驱动覆盖每个自主上限（单笔、日、周、对象日、笔数）转人工、每个硬上限（单笔、日、周）拒绝且不写、刚好在上限上直接执行，以及没有策略的站点和平台；`TestSetCapPolicy` 覆盖建、按版本改、币种不变、硬上限不低于自主上限和审计；`TestReservations` 覆盖驳回、撤回、作废、策略调低后的批准各自释放，策略放宽后的批准照常成功，确认一次、第二次被拒，过了日的确认被拒和清扫释放，以及重放不重复预留；`TestCapRaces` 覆盖并发提案在计数器上排队、同键并发只预留一次、清扫跳过被持有的预留。
- `cmd/axiom/operations_money_test.go`：`TestOperationsTopUp` 走真实 HTTP 和数据库，覆盖没有 `operations.money` 的 403、没有单位价值和没有策略的 422、直接执行与重放、试运行不写、硬上限拒绝、待审批与负责人看到的摘要和上限、确认与重复确认、别的凭证确认 404、策略调低后批准以 `stale_revision` 结束，以及策略的列表、币种不变和旧版本；`TestOperationsTopUpExchange` 覆盖跨币种按汇率折算、没有汇率 422 和向上取整到 10⁻⁹。

- `log_test.go`：`TestOperationLog` 在真实 PostgreSQL 上覆盖从新到旧分页、只读自己凭证的记录；`TestOperationLogBounds` 覆盖刚好 8192 字节的输入原样保存、多一个字节只记大小、站点范围读不到。

- `targets_test.go`：`TestExpectedFromProratesEachMonthByDuration` 表驱动覆盖整月、部分月、跨月、闰年、空窗口、金额币种不符、四种指标的半入和带毫秒的跨月折算；`TestExpectedFromRefusesACountWiderThanItsType` 覆盖超宽；`TestTargetsCheck` 覆盖月份、负数、非法金额、币种和 38 位上限；`TestTargetsCheckCommercial` 覆盖付费用户、月流水和两个比率的范围；`TestExpectedFromAppliesRatesToTheEndMonth` 覆盖比率的结束月规则和月首边界；`TestExpectedFromProratesCommercialCountsAndMoney` 覆盖后两项数量/金额的折算。
- `cmd/axiom/admin_targets_test.go`：`TestAdminOperationsTargets` 走真实 HTTP 和数据库，覆盖空月份、八项写入、审计、看板折算、重放、旧页面只写前四项时后四项保留、整月替换和清零；`TestAdminOperationsTargetsRefuseAStaleForm` 与 `...AfterClear` 覆盖两个编辑者交错提交和清空后的版本；`TestAdminOperationsTargetsKeepWideMoney` 覆盖超过 int64 和 38 位边界；`TestTargetComparisonHidesAnUnknownCostGap` 覆盖成本未知；`TestAdminOperationsTargetsNeedSitesManage` 覆盖无权限；`TestAdminOperationsTargetsRefuseAnotherCurrency` 覆盖币种。
- `cmd/axiom/admin_reports_test.go`：`TestAdminOperationsOverviewCommercial` 覆盖付费用户、月流水、留存实际值和三项新增目标的差距；`TestAdminOperationsOverview` 覆盖收入、成本、现金和成本未知。
- `internal/billing/report_test.go`：`TestPlatformReportReadsPaidUsersAndRetention` 覆盖付费用户去重、窗口边界、上月同期群和跨窗口；`TestPlatformPaidPlanReadsTheWindow` 要求付费批次查询走部分索引、不做 Seq Scan。
- `cmd/axiom/admin_guard_test.go` 把两个操作登记进权限契约：GET 和 PUT 都需要 `sites.manage`，PUT 还要 `mfa`、CSRF 和 `Idempotency-Key`。

## 决策与理由

- **数值用文本，不用 `int64` 存金额**：目标可以是很大的月流水，`numeric(38,0)` 和文本能保住所有位；折算的中间结果用 `big.Int`，写入保留十进制文本并单独检查 38 位，不套用账本单笔的 `int64` 限制。调用、错误和付费用户天然是 `int64`，读取时再检查；比率是 0–10000 的整数基点。
- **后四项单独一张表，不放进 `operations.targets`**：旧节点按月份范围读目标时不按 `metric` 过滤，遇到不认识的指标会让整个看板失败。分表让旧节点继续工作；共享版本表又让两个版本的编辑器仍然只看到一个顺序。代价是多一张表和一段写入循环，换来滚动发布期间不需要按节点分叉。
- **版本单独一张表，不放在目标行上**：清空目标会删除所有目标行，版本必须留在清空之后才能拒绝旧表单；`target_revisions` 让版本在空月份也连续，并覆盖两张目标表。
- **按站点和月份加锁，不锁整张表**：同一站点的同一个月才需要串行；不同站点或月份的写入可以并行。锁的键是 `site:YYYY-MM`，和主键的前两维一致。
- **折算在 Go 里做，不在 SQL 里做**：窗口跨月、每月天数不同、半入规则和币种过滤都在纯函数里，测试不需要数据库；SQL 只按月份范围取行。比率另走一条不折算的路径，避免把百分比当成可加总的数量。
- **毛利率在没有收入或成本未知时为空**：没有收入时除数为 0；成本未知时 `cost` 合计不含那些调用，用它会算出偏高的毛利率。两者都只隐藏实际值和差距，不隐藏负责人设的目标。
- **留存用付费账户的月环比，不用注册同期群**：现有数据只有充值批次，月环比不需要新增埋点；它回答“上月付费的人本月还付费吗”，而不是“注册的人首次付费了吗”。窗口结束所在月是当期，分子只看到 `until`，所以当月是月初至今。
- **API 的 `targets` 和 `metrics` 是可选字段**：滚动发布时旧节点不返回它们，新页面显示“不提供”而不是“没设目标”；新节点的 `metrics` 让页面知道能编辑后四项。整组替换走 `/reports/targets/{month}/all`，同时用请求头 `X-Axiom-Target-Metrics: all` 和请求体的 `all_metrics: true` 表示：旧节点没有这条路径，直接 404，不会把整组请求当成四项写入；旧页面不发后四项，新节点在旧路径上收到整组标志会返回 400。
