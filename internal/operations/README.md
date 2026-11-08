# operations

运营看板的业务目标、运营接口的操作记录、运营操作与审批、资金上限、售价包络和审计链在代码里的接口、表和不变量。规则和理由见[运营的「业务目标」](../../docs/domains/operations.md#业务目标)、[「审计」](../../docs/domains/operations.md#审计)、[「风险分级与审批」](../../docs/domains/operations.md#风险分级与审批)和[「资金操作上限」](../../docs/domains/operations.md#资金操作上限)；本文只写实现时依赖的部分。

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
    Reasons []Reason // 为什么要审批，至少一条：ReasonAlways、caps.go 的 cap_*、prices.go 的 envelope_*
}
func Propose(ctx, tx, p Proposal) (Operation, bool, error)                    // bool：按键找到的旧操作
func Settle(ctx, tx, op Operation, result map[string]any, refused string) (Operation, error)
func Decide(ctx, tx, actor audit.Actor, approval uuid.UUID, approve bool, reason string) (Operation, error)
func Withdraw(ctx, tx, credential, approval uuid.UUID, reason string) (Operation, error)
func Cancel(ctx, tx, actor audit.Actor, approval uuid.UUID, reason string) (Operation, error) // 提出方已不在
func CancelLive(ctx, tx, actor audit.Actor, credential *uuid.UUID, reason string) (int, error) // nil：全部凭证
func ExpireDue(ctx, tx, batch int) (int, error)
func Get / Operations / Approvals / ApprovalOperation                       // 读；Approvals 给凭证只列它自己的操作的审批，零 ID 是负责人的全部队列
func RegisterExpiry(db, scope, queue, logger) error
func RaiseAlert(ctx, tx, op Operation, severity, summary string) error       // alert_raise：AgentAlert 信号，键是 AlertKey(op)
func ResolveAlert(ctx, tx, op Operation) error                               // alert_resolve：同一个键的恢复信号
func AlertKey(op Operation) string                                           // <凭证 UUID>/<agent 给的键>
func ReadAgentLimits(ctx, tx) (AgentLimits, error)                          // 负责人设的 agent 限频；没设过是代码的值
func SetAgentLimits(ctx, tx, actor audit.Actor, l AgentLimits, rev int64, reason string) (AgentLimits, error)
```

- **谁调**：`cmd/axiom` 的运营接口写操作（`operations_writes.go`）先 `accounts.HoldAgent` 再 `Propose`：不要审批的直接是 `approved`，处理器在同一个事务里调目标模块（现在只有 `catalog.ListModel`）再 `Settle`；要审批的是 `pending_approval`，带一条审批。负责人在管理接口 `ApproveOperationsApproval` 里 `Decide`，同一个事务里执行目标改动再 `Settle`；目标在提案之后变了（目标模块的 4xx 拒绝，如 `stale_revision`），改动回滚到保存点，操作以 `rejected` 和那个错误码结束，决定照样提交。吊销凭证和紧急停止在各自的事务里 `CancelLive`。本模块不引用被审批的模块（[总体架构的「模块边界规则」](../../docs/architecture.md#模块边界规则)）。
- **表**：`migrations/00004_operations.sql`。`operations.operations` 一行一个运营操作：凭证、幂等键（`(credential_id, idempotency_key)` 唯一）、请求哈希（工具、目标、规范化参数的 SHA-256）、工具名、目标种类和 ID、参数（`json`，最多 8 KiB，含目标的 `rev`）、风险类别、状态、agent 的理由、运行 ID、结果、错误码和时间。`operations.approvals` 一行一个审批：运营操作（唯一）、档位（现在只有 `human`）、状态、服务端读出的摘要、为什么要审批（`reasons`，迁移 00010 加的可空 `text[]`，1 到 16 条）、请求、到期和决定的时间、决定者（`<种类>:<ID>`）和理由。两张表都是平台数据，策略只放行平台范围；`axiom_app` 只能改操作的 `state`、`result`、`error_code`、`updated_at` 和审批的 `state` 与决定列，不能删。
- **幂等**：`Propose` 用 `INSERT … ON CONFLICT DO NOTHING` 占键；同一个键的第二个请求等第一个事务结束，找到它后比请求哈希：相同返回现在的状态（`replayed`），不同是 `ErrKeyReused`。占键和操作同事务，失败就一起回滚，同一个键可以重试。规范化后的参数超过 8 KiB（表的 CHECK）是 `ErrParamsTooLarge`，不写任何东西；批准的就是执行的，所以参数要么整份保存，要么拒绝。
- **到期**：审批在数据库时钟上到期，`least(clock_timestamp() + 30 分钟, 当天 UTC 结束)`，请求时间取同一个时刻：等锁跨过午夜的提案按新的一天算。`Decide`、`Withdraw` 和 `Cancel` 在锁住之后用 `clock_timestamp()` 判断是否已到期，不用 `now()`：`now()` 是事务开始的时刻，到期前开始、等锁等到到期后的决定用它会看不到到期。对已到期的审批答 `ErrNotPending`，不管清扫到没到；`RegisterExpiry` 在 `archive` 队列上每分钟一轮（决定自己就拒绝过期的审批，清扫只补记录，可以排在调用记录的保留任务后面），`ExpireDue` 每批最多 100 个，以 `system:operations.approval_expiry` 记 `approval.expire`。
- **为什么要等、还能不能决定**：原因由做判定的函数自己给出，不在读的时候从摘要再推一遍：`weigh` 返回资金提案要等的原因（暂停的范围 `cap_paused`、总要人的类别 `always`、或超过的每个自主上限），`PriceEnvelope.outside` 返回包络管不住的每一项，空就是不用等；下架由处理器写 `always`。`Propose` 把 `Proposal.Reasons` 和审批一起写进 `reasons` 列，`Approval.Reasons` 原样读回，旧节点提的审批没有这一列的值，读出 nil。`Approval.Decidable` 是读的那一刻负责人还能不能决定：`ApprovalsOf`、`Approvals` 在查询里用 `state = 'pending' AND expires_at > clock_timestamp()` 算，和 `Decide` 的判断同一个时钟；刚提出的为 true，决定、撤回、取消、到期之后为 false。
- **滚动发布（原因列）**：迁移 00010 只加一个可空列。旧节点插入时不写它（为空），新节点读出没有原因，接口不返回 `reasons`；旧节点的查询列出明确的列名，不读它。
- **批准时复核提出方**：`ApproveOperationsApproval` 先无锁读出审批所属的凭证，`accounts.HoldAgent` 持有它，再 `Decide`，和提案时一样先凭证后操作，所以和吊销、停止的锁顺序一致。凭证已吊销、停止或过期时什么都不执行，用 `Cancel` 把审批和操作记为 `canceled`：滚动发布期间旧节点的吊销和停止只吊销凭证、不作废提案，新节点不能拿它们去执行。
- **锁的顺序**：所有路径先锁运营操作，再锁它的审批。`Decide`、`Withdraw` 先无锁读出审批所属的操作，再按这个顺序加锁、重新判断状态；`CancelLive` 按 ID 升序锁；清扫用 `FOR UPDATE OF o SKIP LOCKED`，跳过正被决定的操作，条件里带操作的状态，所以快照之后已被决定的行在新版本上重判后被排除。
- **agent 的告警**：`alert_raise` 和 `alert_resolve` 是日常写，处理器 `Propose` 后在同一个事务里调 `RaiseAlert` 或 `ResolveAlert` 再 `Settle`。信号种类是 `AgentAlert`（`operations.agent_alert`），合并键是 `AlertKey`：凭证 UUID 加运营操作的目标 ID（agent 给的键），所以一张凭证恢复不了、也改写不了另一张凭证的告警，信号编号就是运营操作编号，字段只有 agent 的摘要和运营操作编号；告警消费者按 `(kind, key)` 合并成事件，所以同一个键恢复前不会再通知。
- **agent 的限频**：`operations.agent_limits`（`migrations/00011_agent_limits.sql`、`00013_agent_request_limits.sql`）一行平台数据，没有行时用代码的值；`requests_per_minute`、`money_writes_per_minute`、`money_writes_per_hour`、`alerts_per_hour` 各在 1 和 `MaxRequestsPerMinute`（600）、`MaxMoneyWritesPerMinute`（30）、`MaxMoneyWritesPerHour`（300）、`MaxAlertsPerHour`（60）之间（表的 CHECK 和 `ErrBadLimit` 两处），所以负责人只能调低。前三列可空：旧节点建的行没有它们，旧节点改这一行也不碰它们，`ReadAgentLimits` 把空的读成代码的值。`SetAgentLimits` 锁住这一行、按读到的版本改（不符是 `ErrLimitsChanged`），记审计 `agent_limits.set`（改前改后）；两个第一次设置同时到达时，后到的插入什么也不插，同样是 `ErrLimitsChanged`。
- **审计与通知**：`approval.request`、`operation.succeed`、`operation.reject`、`operation.cancel` 的操作者是 `operations_agent:<凭证>`（提出、执行和撤回都以 agent 的名义；吊销和停止时的取消是负责人）；`approval.approve`、`approval.reject` 是负责人，`approval.expire` 是系统。审批提出时同事务 `alert.Persist` 一个 `operations.approval_pending` 信号（键是审批 ID），决定、到期、撤回、取消时同事务恢复它：驳回、撤回、作废在本次改动的最后恢复，批准由随后的 `Settle` 在执行之后恢复，`CancelLive` 和到期清扫在整批都结束之后一起恢复；负责人给这个种类配一条告警规则和渠道才会收到通知。

| 不变量 | 怎么保证 |
|---|---|
| 一个凭证的一个幂等键只对应一个运营操作 | 唯一约束；第二个请求等第一个提交后按请求哈希返回原操作或 `ErrKeyReused`。测试 `TestProposeOnceAKey`、`TestRaces` |
| 批准的就是执行的 | 应用角色不能改操作的工具、目标和参数；执行只读这三列。决定时再由目标模块按参数里的 `rev` 核对目标，变了就 `rejected` |
| 审批只决定一次 | 锁住操作和审批后才判断 `pending` 和到期；决定、撤回、到期、取消互斥。测试 `TestDecide`、`TestRaces`（撤回等在批准之后，得到 `ErrNotPending`；清扫跳过被持有的操作，放开后再到期） |
| 页面看到的原因和能否决定就是服务端的判定 | 原因是 `weigh`、`outside` 判定的输出本身，同一次调用决定要不要等；能否决定和 `Decide` 用同一个表达式、同一个数据库时钟。测试 `TestCapDimensions`、`TestPriceEnvelopeDimensions`（每条原因）、`TestApprovalDecidable` |
| 过期的审批不能被批准 | 锁住后用数据库的 `clock_timestamp()` 判断，和到期时间同一个时钟，等锁的时间也算进去；清扫晚到只推迟记录。测试 `TestRaces`（到期前开始、等锁到到期后的决定被拒绝） |
| 不再存活的凭证的提案不会执行 | 批准前持有提出方的凭证，不在了就作废。测试 `cmd/axiom` 的 `TestOperationsApprovalAfterAnOldNodesStop`（旧节点只吊销凭证，新节点批准时作废） |
| 写的答案和读到的一致 | 改状态和决定的语句 `RETURNING` 时间，答案和幂等存下的答案带的是库里的 `updated_at`、`decided_at`。测试 `TestOperationsWithdrawalPreview`、`TestOperationsApprovalAfterAnOldNodesStop` |
| 吊销和停止不会漏掉一个待审批的提案 | 提案前 `HoldAgent`（`FOR SHARE`），吊销要改同一行，只能在提案之前失败或之后取消它。测试 `TestCancelLive`、`accounts.TestHoldAgent` |
| 每个状态变化都有审计 | `finish`、`Settle` 在同一个事务里写状态和 `audit.Record`；审计写不进整个改动回滚 |
| 已批准的操作不留在 `approved` | 直接执行的和批准后的都在同一个事务里 `Settle`；所以 `CancelLive` 和清扫只处理 `pending_approval` |

## 资金上限

```go
type Category string // CategoryTopUp = "money.top_up"，CategoryCredit = "money.credit"
const RiskTopUp, RiskGift Risk = "top_up", "gift"
type CapPolicy struct {
    Site *uuid.UUID; Category Category; Currency platform.Currency // Site nil：平台
    Single, Daily, Weekly, ObjectDaily int64; DailyCount int32   // 自主上限：都在内才不经过人
    HardSingle, HardDaily, HardWeekly int64                       // 硬上限：超过就拒绝
    Rev int64; Updated time.Time; Paused *time.Time              // Paused 非空：对账发现漂移，每笔都交给负责人
}
type CapUsage struct { Policy CapPolicy; DayStart, WeekStart time.Time; Day, Week WindowUsage }
type WindowUsage struct { Reserved, Committed int64; Count int32 }
type Transfer struct { Site *uuid.UUID; Category Category; Object Target; Amount int64; Currency platform.Currency; Basis, Summary map[string]any }
type CapExceeded struct { Limit string; Max, Used, Amount int64 } // error
func CapPolicies(ctx, tx) ([]CapPolicy, error)
func SetCapPolicy(ctx, tx, actor, p CapPolicy, rev int64, reason string) (CapPolicy, error)
func CapCurrency(ctx, tx, site *uuid.UUID, category Category) (platform.Currency, error)
func ProposeMoney(ctx, tx, p Proposal, m Transfer) (Operation, bool, error)
func CheckReservation(ctx, tx, op Operation) (Reservation, error)                  // 批准时复核
func CommitReservation(ctx, tx, confirm Operation, proposal uuid.UUID, object Target, proof string) (Reservation, error)
func CommitApproved(ctx, tx, op Operation, proof string) (Reservation, error)       // 批准后在同一个事务里动了钱
func ExpireReservations(ctx, tx, batch int) (int, error)
func ReservationOf(ctx, tx, id uuid.UUID) (Reservation, bool, error)
func CapUsages(ctx, tx) ([]CapUsage, error)                                          // 每条策略和它当前日、周的用量
func ReconcileCaps(ctx, db, scope) (int, error)                                      // 对账，返回这次暂停的范围数
func RepairCaps(ctx, tx, actor, category Category, site *uuid.UUID, reason string) (CapPolicy, error)
func RegisterReconcile(db, scope, queue, logger) error
```

- **谁调**：`cmd/axiom` 的 `supplierTopUpPropose` 先 `HoldAgent`，用 `suppliers.Admin.TopUpBasis` 读出供应商的归属、单位价值和结算币种，按 `CapCurrency` 的币种折算金额（原生金额 × 单位价值，币种不同再乘 `billing.ExchangeRates` 的当前汇率，向上取整到 10⁻⁹），再 `ProposeMoney`；直接执行的在同一个事务里 `Settle`。批准时 `runOperation` 调 `CheckReservation`。`supplierTopUpConfirm` 先 `Propose` 一个确认操作，再 `CommitReservation`，最后 `Settle`。`creditGrant`（`operations_credits.go`）先 `HoldAgent`、按键 `Replay`，读出账户，账户的站点不在凭证的站点里答 404，按管理接口赠额的 `grantOf` 读金额和到期时间，按 `CapCurrency` 的币种折算面值（币种相同不折算），再 `ProposeMoney`；赠额一律待批，处理器不执行。批准时 `runOperation` 的 `runCreditGrant` 先 `CheckReservation`，按参数重读账户、重新 `grantOf`，以 agent 的名义 `billing.AddCredit`（批次 ID 是运营操作的 ID），再 `CommitApproved`。管理接口的 `listOperationsCapPolicies`、`setOperationsCapPolicy`、`repairOperationsCapCounters` 调 `CapPolicies`、`SetCapPolicy`、`RepairCaps`；运营接口的 `capsGet` 调 `CapUsages`，再按凭证的站点和平台范围过滤。本模块不引用 `suppliers` 和 `billing`：金额和它的依据由调用方算好放进 `Transfer`。
- **表**：`cap_policies.go`、`caps.go`、`reconcile.go`、`migrations/00005_money_caps.sql`、`00006_cap_reconcile.sql`。`operations.cap_policies` 每个 `(类别, 站点)` 一行（`UNIQUE NULLS NOT DISTINCT`，站点为空是平台）：币种、五个自主上限、三个硬上限（表的 CHECK 要求每个硬上限不低于对应的自主上限）、版本和暂停时间（`paused_at`，可空）。`operations.cap_counters` 每个 `(类别, 窗口种类, 窗口开始, 对象种类, 对象 ID, 站点)` 一行：类别的 UTC 日和 ISO 周（对象为空），对象的 UTC 日；记 `reserved`、`committed` 和笔数，初值 0，CHECK 不为负。`operations.money_actions` 每个资金操作一行：类别、范围、对象、金额、币种、依据（`basis`：原生金额、原生单位、单位价值行 ID 与值、结算币种、汇率的 ID、版本和分子分母、结果）、策略版本、预留所在的日和周、到期（当天 UTC 结束）、状态（`reserved`/`committed`/`released`）、付款凭证和确认它的操作。应用角色只能改策略的上限、版本和暂停时间、计数器的三个数、资金操作的状态与确认列；策略的币种建了就不变。
- **一笔提案**：`ProposeMoney` 先 `FOR SHARE` 锁策略（没有就是 `ErrNoCapPolicy`），用数据库时钟取当天和本周，补齐三行计数器后按「类别日、类别周、对象日」的顺序 `FOR UPDATE`；这把锁让同一范围和类别的提案排队，所以接着按键找重放（找到就返回，不再按已含它自己预留的计数器重新判断），再判断：硬上限任一超过是 `*CapExceeded`，什么都不写；自主上限任一超过（含当天笔数）就带摘要和上限前后的数（`cap`，金额是十进制文本）建待审批提案；都在内就直接 `approved`，除非策略已暂停：暂停时一律建待审批提案，摘要的 `cap.paused` 为 true。两种都把金额记进三行的 `reserved`、笔数加一，写资金操作，一起提交。类别不是 `money.top_up` 的（现在是 `money.credit`）在自主上限内也建待审批提案：`Category.autonomous` 是「哪个类别不经过人」的唯一一处。窗口按 `clock_timestamp()` 取（`now()` 是事务开始的时刻）；所有可能等待的锁都拿到之后（审批信号的锁也在内）再看一次这一天是否已经结束，结束了是 `ErrWindowEnded`，整个提案回滚、在新的一天重提，不把预留记进已经过去的窗口。处理器在读供应商、算金额之前先 `Replay`：成功之后供应商被移除或单位价值变了，同一个键的重试照样拿回原来的操作。笔数数的是当天的提案，释放的也算，所以撤回再提不会刷出额度外的笔数。
- **预留的结局**：提案被驳回、撤回、作废、到期，或批准时被目标拒绝（`Settle` 带拒绝码），`finish` 和 `Settle` 都调 `release` 释放：按资金操作记下的窗口锁计数器，减掉 `reserved`，状态 `released`，审计 `reservation.release`。批准时 `CheckReservation` 锁住资金操作、策略和三行计数器，预留已不在或已过它的日是 `ErrNotReserved`；按现在的策略，这笔超过单笔硬上限、或窗口里的已用（已含这笔）超过日或周硬上限，是 `ErrPolicyChanged`（策略放宽不影响待批的提案），处理器回到保存点、以 `rejected` 结束操作并释放。充值提案成功只表示负责人可以付款：预留留到 agent 确认到账（`CommitReservation`：同一张凭证、同一家供应商的已成功提案，预留还在且没过它的日，在原来的窗口里把金额从 `reserved` 移到 `committed`；先锁策略和计数器、最后才锁资金操作，用这时的 `clock_timestamp()` 判断，等锁跨过午夜的确认是 `ErrNotReserved`、整个确认回滚，预留留给清扫，审计 `reservation.commit`），或到当天 UTC 结束由清扫释放（`ExpireReservations`，`system:operations.reservation_expiry`）。赠额没有付款和确认：批准的事务赠出额度后，`CommitApproved` 锁住这笔操作自己的预留（要还在、没过它的日，否则 `ErrNotReserved`），在原来的窗口里移到 `committed`，`proof` 是调用方给的批次，确认者是操作自己，审计 `reservation.commit` 以 agent 的名义；它和 `CommitReservation` 共用 `commit`。已计入 `committed` 的不会因为之后的退款或冲正退回。
- **锁的顺序**：会等待的锁按一条全局顺序拿：凭证（`HoldAgent`）→ 运营操作 → 策略 → 计数器 → 审批信号。审批和资金操作只由持有它所属运营操作的事务加锁（预留的清扫用 `SKIP LOCKED` 把两者一起锁），所以持有运营操作之后拿它们从不等待，放在哪一步都不成环。策略按类别、平台在前再按站点排；计数器先按范围（和策略同序），范围内按「对象种类、窗口种类、窗口开始、对象 ID」排：单笔的三行「类别日、类别周、对象日」（`LockCapCounters`）和修复锁的当前窗口（`LockScopeCounters`，多出的对象行按对象 ID 排在后面）都是它的子序列，所以只动一个范围的事务按各自的顺序就守住了它。一批操作（`CancelLive`、审批到期 `ExpireDue`、预留的清扫 `ExpireReservations`）会跨范围和窗口，在释放第一笔之前由 `holdScopes` 把整批预留的策略、再把计数器按这个顺序一次锁齐（`LockReservedPolicies`、`LockReservedCounters`），逐笔释放时重锁的都是已持有的行；预留的计数器和预留在同一个事务里建，应用角色不能删，所以锁住现有的行就是整批要改的全部。逐笔加锁时，两个吊销的凭证若一个按 ID 先有平台充值、后有站点赠额，另一个相反，就各持一个范围的计数器等对方，数据库回滚其中一个、连同它的吊销。各路径：提案是「凭证 → 策略 → 计数器 → 新的运营操作 → 信号」；批准在 `Decide` 里就按「运营操作 → 审批 → 资金操作 → 策略 → 计数器」持有（`holdReservation`），之后的 `CheckReservation` 和释放不再等锁，信号由 `Settle` 最后恢复；批准赠额在计数器之后还要计费的账户余额行（`billing.AddCredit`），调用路径只锁余额行、从不等运营的行，所以不会成环；驳回、撤回是「运营操作 → 审批 → 资金操作 → 策略 → 计数器 → 信号」；`CancelLive` 和审批到期是「整批的运营操作 → 整批的策略 → 整批的计数器 → 逐笔的审批和资金操作 → 信号」；确认是「被确认的运营操作 → 策略 → 计数器 → 资金操作」，资金操作最后锁、锁住时才判断预留还在且没过它的日；对账的暂停是「策略 → 信号」，修复是「策略 → 计数器 → 信号」；预留的清扫是「运营操作和资金操作（一起 `SKIP LOCKED`）→ 整批的策略 → 整批的计数器」，被作废或确认持有的跳过，它和审批的到期各用一个事务；改策略只锁策略。测试 `TestCapLockOrder` 用显式顺序覆盖两个曾经成环的交错：持有计数器的提案和要恢复信号的批准，以及持有操作的作废和清扫；`TestBatchLockOrder` 覆盖两个吊销按相反的顺序跨两个范围。
- **对账**：计数器是资金操作的累加，和资金操作在同一个事务里改，所以随时可以从 `money_actions` 重算。`RegisterReconcile` 在 `money` 队列上每分钟跑一次 `ReconcileCaps`：一条只读的 `CapDrift` 语句把当前 UTC 日和 ISO 周的每行计数器和资金操作加起来的应有值放在一起（每笔计入它的日、周和对象日，`reserved` 或 `committed` 按状态，笔数不论状态），列出不一致的行；一条语句一个快照，进行中的提案对两边都不可见，不会误报。每个不一致的范围在自己的事务里（一个失败不影响别的范围暂停，失败一起带回）`FOR UPDATE` 锁策略、再比一次，仍不一致且未暂停才记 `paused_at`、审计 `cap_policy.pause`（`system:operations.cap_reconcile`，每行的现值和应有值）、发 critical 信号 `operations.cap_drift`（键是 `<范围>:<类别>`）。`RepairCaps` 锁策略（`FOR UPDATE`，等进行中的提案和批准放开 `FOR SHARE`）、取一次当前窗口，按「类别日、类别周、对象日（按对象 ID）」锁这些窗口的全部计数器，再按同样的窗口算一次（跨过午夜也只写它锁住的行），把每行 upsert 成应有值，清掉暂停、审计 `cap_counters.repair`（改前改后），刚恢复的才解除信号。每条路径都先持有策略、再补或锁计数器（`moveReserved` 也是）：修复持有策略和已有的计数器后要补回缺的行，若确认或释放不持有策略就先补了这一行、再等修复的计数器，两边会互相等待（测试 `TestCapRepairLockOrder`）。审计的漂移行按大小拆成几条（`driftParts`：每行按规范 JSON 加上冒号和逗号后的空格估计 jsonb 文本的上界，装进 8 KiB 减去外壳），带 `part`/`parts`，对象 ID 截到 128 个字符（测试 `TestCapDriftAuditParts`）；策略不存在是 `ErrNoCapPolicy`，没有漂移时只清掉暂停。
- **清扫**：`RegisterExpiry` 的同一个每分钟任务里，先在一个事务里到期审批，再在另一个事务里释放过了日的预留，各自每批最多 100 个。确认在所有会等待的锁拿到之后用 `clock_timestamp()` 判断是否已过日，不等清扫。
- **滚动发布**：迁移只加表和放宽 `risk` 的 CHECK，旧节点不提充值。迁移 00008 只放宽策略和资金操作的 `category`、操作的 `risk`，旧节点不提赠额；上一个版本批准赠额时不认识 `credit_grant`，整个批准回滚、提案仍待批；它驳回、撤回、作废和到期时用不分类别的 `release` 照常释放预留，对账和修复也不分类别。旧节点驳回、撤回、作废或到期一个待批的充值时不会释放它的预留，预留留到当天结束由新节点的清扫释放：只会多占、不会少算。旧节点批准充值时不认识这个工具，整个批准回滚，提案仍待批。`paused_at` 是可空的新列，旧节点不读它，所以迁移在 `money_actions` 上加触发器 `refuse_autonomous_money_when_paused`：暂停的范围里，只有操作是 `pending_approval` 的预留能写进去。旧节点（以及回滚后的这个版本）额度内的提案在那里整体回滚、答 500；新节点暂停时建的是待批提案，不受影响（测试 `TestCapPauseHoldsOldNodes`）。

| 不变量 | 怎么保证 |
|---|---|
| 没有策略就不动钱 | `ProposeMoney`、`CapCurrency` 找不到策略都是 `ErrNoCapPolicy`。测试 `TestCapDimensions` |
| 任何时候一个窗口里 `reserved + committed` 不超过硬上限（策略不调低时） | 判断和预留在同一个事务里、持有三行计数器的锁；超过的不建操作。测试 `TestCapDimensions`、`TestCapRaces`（两笔各自在内、合起来超过的并发提案，后一笔等前一笔提交后被拒） |
| 待批的金额也占额度 | 待审批的提案同样记进 `reserved`；释放只在操作不再执行时发生。测试 `TestCapDimensions`、`TestReservationReleases` |
| 同一个键不会预留两次 | 计数器锁之后先按键找重放。测试 `TestReservationCommit`、`TestCapRaces` |
| 批准不越过现在的硬上限 | 批准时锁住计数器，按现在的策略重算硬上限，装不下就 `rejected` 并释放；放宽的不作废。测试 `TestReservationApproval`（放宽后批准成功、调低后被拒）、`cmd/axiom` 的 `TestOperationsTopUpPolicy` |
| 预留只结一次 | 确认、释放、清扫都锁住资金操作再看状态；`settled_at` 的 CHECK 让 `reserved` 之外的状态都有结算时间。测试 `TestReservationCommit`（第二次确认被拒）、`TestReservationExpiry`、`TestCapRaces`（清扫跳过被持有的预留） |
| 过了提案的 UTC 日不能确认 | 确认最后锁资金操作，锁住时按数据库时钟判断，等策略或计数器的时间也算进去；过了就整个确认回滚。测试 `TestReservationExpiry`、`TestConfirmWaitingPastTheDay`（确认等计数器时这一天结束） |
| 批量取消和清扫不会和别的批次互相等待 | 释放第一笔之前按全局顺序锁齐整批的策略和计数器（`holdScopes`）。测试 `TestBatchLockOrder` |
| 计数器等于资金操作加起来的数；不等时自主提案停止 | 计数器只由资金操作的预留、确认和释放在同一个事务里改；每分钟对账，不一致就暂停、转人工，修复只按资金操作重算。测试 `TestCapReconcile`（日、周、对象日的三个数，丢行和多出的行，各自只暂停自己的范围，暂停时小额也转人工）、`TestCapRepair`（修复后等于资金操作并恢复）、`TestCapReconcileRaces`（进行中的提案不算漂移；修复等进行中的提案并算上它；暂停前重比，不暂停刚修好的范围）、`TestCapPauseHoldsOldNodes`（旧节点的额度内预留在暂停的范围里被数据库拒绝）、`cmd/axiom` 的 `TestOperationsCapRepair` |
| 赠额不经过负责人不会赠出，一次操作只赠一次 | `ProposeMoney` 对 `money.credit` 总建待审批提案；批准时赠出，批次 ID 是运营操作的 ID，计费按它幂等，`Decide` 只决定一次。测试 `TestCapDimensions`（赠额在每个自主上限内也待批，硬上限照样拒绝）、`TestCreditCommit`、`cmd/axiom` 的 `TestOperationsCreditGrant`、`TestOperationsCreditGrantApprovalRace` |
| 计数器里的币种只有一种 | 策略的币种建了就不变（`ErrCurrencyFixed`，应用角色也没有改这一列的权限）；`ProposeMoney` 再核对 `Transfer` 的币种 |

## 售价包络

```go
const RiskPrice Risk = "price"
type PriceEnvelope struct {
    Site uuid.UUID; MinMargin int32; ModelDailyChange int64; DailyChanges, DailyModels int32 // 基点或整数
    Rev int64; Updated time.Time
}
type Repricing struct { Site, Model uuid.UUID; Change, Margin *big.Rat; Summary map[string]any } // nil：量不出比例、成本未知
func PriceEnvelopes(ctx, tx) ([]PriceEnvelope, error)
func SetPriceEnvelope(ctx, tx, actor, e PriceEnvelope, rev int64, reason string) (PriceEnvelope, error)
func ProposePrice(ctx, tx, p Proposal, r Repricing) (Operation, bool, error)
func ProposePrices(ctx, tx, p Proposal, site uuid.UUID, moves []Repricing, undo func() error) (Operation, bool, error)
func CountPrices(ctx, tx, site uuid.UUID, moves []Repricing) error // 负责人批准的改价，在批准时计数
func MovesDetail(moves []Repricing) []map[string]any          // 结果里列出的变动，最多 12 笔
func (r Repricing) Detail() map[string]any                    // change_bps、margin_bps，量不出的是 null
type PriceUsage struct { Envelope PriceEnvelope; DayStart time.Time; Changes, Models int32; Moves []ModelMoves }
type ModelMoves struct { Model uuid.UUID; Change int64; Count int32 }
func PriceUsages(ctx, tx) ([]PriceUsage, error)
```

- **谁调**：`cmd/axiom` 的 `pricePublish`（`operations_prices.go`）先 `HoldAgent`，再锁主站的定价政策行（`billing.LockPricePolicy`）、按键 `Replay`：同一个键并发的重试在政策行上等第一个请求提交，然后找到它的操作，不会被它刚发布的新版本判成过时；再 `repricing`：像管理接口的确认一样 `refreshSalePrices`，读 `billing.PriceState` 里这个模型的提案（候选和当前版本都要和 agent 读到的一样，否则 `billing.ErrStalePrice`），用 `billing.PriceMove` 算变动和毛利率（上下文分档的口径变了算量不出比例），然后 `ProposePrice`；直接执行的在同一个事务里 `billing.PublishPrice`（以 agent 的名义、`confirmed`、不豁免保本线）再 `Settle`。批准时 `runOperation` 的 `runPricePublish` 重新 `repricing`，`CountPrices`，再发布。`pricingRuleCreate`、`pricingRuleUpdate`（`operations_rules.go`）同样先 `HoldAgent`、锁政策行、`Replay`，再刷新提案、记下各模型各人群卖的版本，`SAVEPOINT` 之后按管理接口的同一个函数改规则、再刷新，把卖的版本变了的逐个用 `billing.PriceMove` 量成 `Repricing`，交给 `ProposePrices`；越线时它调 `undo`（`ROLLBACK TO SAVEPOINT`）再建待审批提案。批准时 `runPricingRule` 重做一遍并 `CountPrices`。管理接口的 `listOperationsPriceEnvelopes`、`setOperationsPriceEnvelope` 调 `PriceEnvelopes`、`SetPriceEnvelope`；`capsGet` 调 `PriceUsages`，按凭证的站点过滤。本模块不引用 `billing`：变动和毛利率由调用方算好放进 `Repricing`。
- **表**：`prices.go`、`migrations/00007_price_envelopes.sql`。`operations.price_envelopes` 每个站点一行：最低毛利率（0–10000 基点）、单个模型单日变动（基点）、单日笔数、单日模型数、版本。`operations.price_counters` 每个 `(站点, UTC 日, 模型)` 一行（`UNIQUE NULLS NOT DISTINCT`）：模型为空的站点行记当天笔数（`count`）和改过价的模型数（`models`），模型行记累计变动（`change_bps`）和笔数；CHECK 让站点行没有变动、模型行没有模型数，都不为负。平台数据；应用角色只能改包络的四个限值和版本、计数器的三个数，不能删。`risk` 的 CHECK 加上 `price`。
- **一次发布**：`ProposePrice` 先 `FOR SHARE` 锁包络（没有就是 `ErrNoPriceEnvelope`），用数据库时钟取当天（`CapWindows`），补齐并按「站点行、模型行」`FOR UPDATE` 锁两行计数器，再判断：`Change` 和 `Margin` 都有，毛利率不低于 `MinMargin ÷ 10000`，模型行的变动加这一笔（向上取整到基点，按精确值比较，不截断）不超过 `ModelDailyChange`，站点行笔数小于 `DailyChanges`，模型当天第一次改价时站点行的模型数也小于 `DailyModels`，就直接 `approved`、在同一个事务里计数；否则把 `envelope`（限值、版本、这一笔的变动和毛利率、已用数）放进摘要建待审批提案，不计数。计数后所有锁都已拿到，再看一次这一天是否结束，结束了是 `ErrWindowEnded`、整体回滚。`ProposePrices` 是一次移动多个售价的改动（改定价规则）：同样锁包络和当天的站点行，再按模型 UUID 升序锁所有被移动模型的行，按顺序逐笔判断，每笔都按它之前的几笔已经计入来判断（同一个模型两笔，第二笔看到第一笔的变动；当天笔数每笔加一，模型第一次出现时模型数加一），所以判断和变动的顺序无关。全部在内就 `approved` 并全部计数；任何一笔越线，先调调用方的 `undo` 撤掉改动，再把调用方的摘要加上 `envelope`（限值、版本和当天已用的笔数、模型数）、`moves`（每笔的模型、人群、变动、毛利率和这个模型当天已用的变动，最多 `maxShownMoves` 即 12 笔，摘要和结果都留在 8 KiB 内）和 `moves_total` 建待审批提案。同一个键的重试按已有的操作回答，之前同样 `undo`。一笔都没有的改动在任何包络内。`CountPrices` 按同样的顺序（有包络就先 `FOR SHARE` 锁它）锁批准那一刻当天的站点行和各模型行并计数；量不出比例的变动计 10000 基点；超过包络最大允许值（`maxModelDailyChange`，10⁸ 基点，表的 CHECK 同样限制）的变动计这个值加一，计数器不会溢出，当天这个模型也不会再落进任何包络。计数只增不减，所以改回去也要再占一次变动。
- **锁的顺序**：发布是「凭证 → billing 的站点定价政策行（刷新提案时拿）→ 包络 → 计数器（站点、模型）→ 新的运营操作 → 审批信号」；批准是「运营操作 → 审批 → billing 的站点定价政策行 → 包络 → 计数器」。改定价规则在政策行之后、包络之前还锁 billing 的规则行，计数器按站点行、各模型行（UUID 升序）的顺序锁。刷新提案可能在政策行之后就写定价的告警信号，比计数器早拿信号的锁，违反了本模块「信号最后拿」的顺序；这里不会成环，因为每条持有售价计数器的路径都先持有同一个站点的政策行，等计数器的人和持有计数器的人不可能同时存在。改包络只锁包络。
- **滚动发布**：迁移只加两张表和放宽 `risk` 的 CHECK，旧节点不发布售价。改定价规则没有迁移，计数器的查询按模型数组锁行，和旧节点的单模型查询锁同样的行、同样的顺序。旧节点批准一个售价提案或改规则的提案时不认识这个工具，整个批准回滚，提案仍待批；旧节点驳回、撤回、作废或到期它都和别的提案一样，售价提案没有预留要释放。`caps_get` 的 `price_envelopes` 是可选字段，旧节点不返回它。

| 不变量 | 怎么保证 |
|---|---|
| 没有包络就不改价 | `ProposePrice` 找不到包络是 `ErrNoPriceEnvelope`。测试 `TestPriceEnvelopeDimensions`、`cmd/axiom` 的 `TestOperationsPricePublish` |
| 包络内才不经过人；量不出、成本未知都转人工 | 判断在持有两行计数器的事务里，`within` 要求变动和毛利率都量得出。测试 `TestPriceEnvelopeDimensions`（每个限值、刚好在限值上、不足一个基点算一个、已改过价的模型不再占模型数） |
| 两笔并发的改价合起来也不越过包络 | 同一个站点的发布在站点行上排队，后一笔按前一笔提交后的计数判断。测试 `TestPriceRaces` |
| 同一个键的并发重试只发布一次、答同一个操作 | 先锁定价政策行再按键重放。测试 `cmd/axiom` 的 `TestOperationsPricePublishRetryRace` |
| 超出包络最大值的变动不会被截断后放行 | 判断用精确的基点数；计数器记最大值加一。测试 `TestPriceEnvelopeDimensions`（与最大包络等大的变动、超过它的变动、记过超大变动的一天）、`TestCountPrice` |
| 人批过的改价照样计数 | 批准时 `CountPrices`，在发布或改规则的同一个事务里。测试 `TestCountPrice`、`cmd/axiom` 的 `TestOperationsPriceApproval`、`TestOperationsPricingRuleApproval` |
| 一次改规则移动的售价合起来也不越过包络，越线时规则和价格都不动 | `ProposePrices` 按已计入前几笔的计数逐笔判断，越线先 `undo` 回保存点再建提案。测试 `TestProposePrices`、`cmd/axiom` 的 `TestOperationsPricingRuleApproval` |
| 批准的就是发布的 | 参数里有候选和当前版本，应用角色改不了；批准时重新刷新，候选或当前版本变了是 `stale_revision`，操作 `rejected`。测试 `TestOperationsPriceApproval` |

## 审计链

```go
func ChainAudit(ctx context.Context, db *pg.DB, scope pg.Scope) (int, error)       // 一轮接链，回接了几条
func VerifyAuditChain(ctx context.Context, db *pg.DB, scope pg.Scope) (bool, error) // 核对整条链，回链是否已停
func RegisterAuditChain(db *pg.DB, scope pg.Scope, queue *pg.Queue, logger *slog.Logger) error
```

`chain.go`、`migrations/00012_audit_chain.sql`。规则见[运营的「审计」](../../docs/domains/operations.md#审计)的「防篡改现在的实现」。

- **任务**：`RegisterAuditChain` 在 `archive` 队列上登记一种任务 `operations.audit_chain`，按参数排两个每分钟的周期：`ChainAudit`，以及带 `whole` 的 `VerifyAuditChain`；两者各自一次只跑一个，失败只记日志不重试。
- **一轮**：每个事务先 `LockAuditChain`（事务级咨询锁），有 `audit_chain_faults` 就什么都不做。第一个事务先用 `verifyBatch` 核对链头往回 `chainWindow`（1000）条，再接最多 `chainBatch`（500）条：`AuditChainFrontier` 单独一条语句读出边界（60 秒前和本库每个开着的事务的 `xact_start` 取早的），之后才用 `audit.Since` 读边界之前、链头之后的条目，所以在两条语句之间提交的事务一定在读到的快照里。满 500 条就开下一个事务接着接；最后在一个事务里按还没接的最早一条发或恢复 `operations.audit_chain_lag`。
- **哈希**：`linkHash` 是 SHA-256(前一条的哈希 ‖ RFC 8785 编码的字段数组)，字段和顺序见领域文档；`TestLinkHash` 的期望值是在 Go 之外另行算出的，改编码就是改链的格式，要换掉第一个元素 `axiom.audit.v1`。
- **核对**：`verifyBatch` 读 `from - 1` 那一条（取它的哈希和在日志里的位置）和其后最多 500 条，再用 `audit.Since` 从那个位置读同样多的条目，逐个对位：前一条的哈希不对是 `broken`，对位的条目 ID 不同时，条目排在链的后面是 `missing`、排在前面是 `late`，ID 相同但重算的哈希不同是 `mismatch`，哈希相同而链上的 `at` 和条目的不同也是 `broken`（`extend` 按链头的 `at` 往后读，只改它就能让接链停下）。前一条不在了也是 `broken`。`haltAt` 在同一个事务里写故障、审计 `audit_chain.halt` 和 critical 信号 `operations.audit_chain_fault`。
- **整条核对**：`VerifyAuditChain` 每次最多 `chainCheckBatches`（20）个事务，每个事务是一步 `checkStep`：没有进行中的一轮、且上一轮开始已满一天（`AuditChainCheck` 的 `due`）时，`startCheck` 以当时的链头为目标开新的一轮；否则 `advanceCheck` 用 `verifyBatch` 核对 `next_seq` 起的一批，并在同一个事务里存下新的 `next_seq`，过了目标就记 `finished_at`、恢复 `operations.audit_chain_check_overdue`。一次运行的第一步发现这一轮开始已满一天还没完，就发这个 warning。所以被期限或数据库失败打断的一次只丢掉没提交的那一批。

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

`migrations/00012_audit_chain.sql`：

- `operations.audit_chain(seq, entry_id, at, prev_hash, hash, chained_at)`，主键 `seq`，`entry_id` 唯一，两个哈希都是 32 字节；不对 `platform.audit_log` 建外键，条目被删后链还在、核对才找得到。触发器 `audit_chain_follows` 只收接在链头之后的一条（`seq = 1` 且前面是 32 个零字节，或 `seq - 1` 存在且它的哈希等于 `prev_hash`）。`axiom_app` 只有 SELECT 和数据列的 INSERT，`chained_at` 由数据库填。
- `operations.audit_chain_faults(id, found_at, kind, seq, entry_id)`，`kind` 是 `late`、`missing`、`mismatch`、`broken` 之一；`axiom_app` 只有 SELECT 和 `kind`、`seq`、`entry_id` 的 INSERT。
- `operations.audit_chain_checks(singleton, target_seq, next_seq, started_at, finished_at)`，只有一行，第一轮之前没有；`axiom_app` 能读、插入目标和位置、更新四个值。
- 两张表只有平台范围能读写。

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
| 审计链按 `(at, id)` 依次覆盖链头之前的每条审计条目，不分叉、不跳号 | 主键 `seq` 和触发器 `audit_chain_follows`；只接 `AuditChainFrontier` 之前的条目，比所有开着的事务都早的条目不会再有更早的提交；核对时对位比较，链头之前多出、少了或改了的条目都停下接链（`TestAuditChainHalts`、`TestAuditChainWaitsForOpenTransactions`） |
| 未记录的成功改动不能留下 | `audit.Record` 和写入同一事务；`TestAdminOperationsTargets` 读审计的 `before`/`after`，包括旧页面写入时后四项的现值 |

## 测试

- `actions_test.go`：`TestProposeOnceAKey` 覆盖直接执行与结算、按键重放、键复用、别的凭证同一个键、审批的到期上界、审计和信号；`TestDecide` 覆盖批准、重复决定、驳回、过期后拒绝决定与清扫、撤回只限自己；`TestCancelLive` 覆盖按凭证和全部取消；`TestRaces` 用持有事务等对方进入锁等待的显式顺序覆盖同键并发、批准与撤回、等锁期间到期的决定、清扫与决定。
- `actions_read_test.go`：`TestApprovalDecidable` 覆盖单个读和审批队列里的 `Decidable`（待批未到期为真；待批已过到期、清扫还没到时为假；已决定为假）和原因（新提的读回、旧节点没写的为 nil）。
- `cmd/axiom/operations_writes_test.go`：`TestOperationsAPIWrites` 走真实 HTTP 和数据库，覆盖上架立即执行、重放、键复用、试运行和旧 `rev` 不写、下架等负责人、负责人看到的摘要和理由、批准后以 agent 的名义执行与审计、操作记录里的理由和输入；`TestOperationsApprovalOutcomes` 覆盖驳回、批准时模型已变、撤回与重放、紧急停止和吊销取消待审批、没有权限、别的凭证的审批、下架草稿；`TestOperationsApprovalAfterAnOldNodesStop` 覆盖旧节点停止后批准；`TestOperationsWithdrawalPreview` 覆盖撤回的试运行不改审批、审计和幂等键，以及撤回和重放的时间与详情一致。
- `cmd/axiom/operations_alerts_test.go`（真实 HTTP 和数据库）：`TestOperationsAlertRaise` 覆盖发告警的信号（种类、键、严重度、摘要、运营操作编号）、重放不多发、键复用、恢复信号和两条以 agent 名义的审计；`TestOperationsAlertsAreTheCredentials` 覆盖另一张只有站点范围的凭证用同样的键发和恢复，只动它自己的事件；`TestOperationsAlertLimit` 覆盖负责人读默认的 60、超过上限 400、按版本调低到 10、旧版本 409、审计改前改后，以及调低后 agent 只放行 5 次（默认是 30）；`operations_caps_test.go` 的 `TestOperationsAgentLimitsLowered` 覆盖负责人依次调低每分钟的资金写、每小时的资金写和每分钟的请求（没给的项保持原值）、旧版本 409、超过上限 400、三条审计，以及每次调低后 agent 从下一个请求起就被拦下（默认放行的那一次答 429）；`TestOperationsAlertRefusals` 覆盖试运行、超过 512 字节的摘要、不合规的键和没有 `operations.write` 都不留操作和信号，发告警的限频（含试运行）、429 和 `Retry-After`，以及限频时仍能恢复；`TestOperationsAlertsList` 覆盖告警事件从新到旧、`open` 只列未恢复的、翻页，以及没有平台范围的凭证 404。
- `cmd/axiom/operations_audit_test.go`（真实 HTTP 和数据库）：`TestOperationsAuditReads` 覆盖带 `audit.view` 和平台范围的凭证按动作和目标精确筛出负责人建凭证的那条审计、按编号读回它，读不存在或不是编号的条目 404、超过 31 天的窗口 400，只有站点范围的凭证读列表和单条都是 404，没有 `audit.view` 是 403。
- `cmd/axiom/operations_groups_test.go`（真实 HTTP 和数据库）：`TestOperationsSupplierGroupSelect` 覆盖 agent 选用分组（直接执行、起代号、重放、以 agent 名义的审计）和取消选用保留代号；`TestOperationsSupplierModelMap` 覆盖映射分组里待映射的名字（名字离开队列、`supplier_alias.create` 的操作者和理由）；`TestOperationsSupplierMappingRefusals` 覆盖试运行、分组没列出的名字、负责人整个供应商的别名盖住的名字和分组已经经另一个名字服务的模型 409（原端点不变）、目录没有和已下架的模型 422、供应商没有的分组 404、没有 `suppliers.manage` 403、只有站点范围 404，都不留运营操作和别名。`internal/suppliers` 的 `TestMapPending` 在模块里覆盖同样的规则，另有目录已认出的名字、只有别的分组列出的名字、分组已有端点、已解析的名字、别名和还没投影的别名占着的模型、只有别的分组有的模型可以映射，以及移除中的供应商。
- `cmd/axiom/operations_credits_test.go`：`TestOperationsCreditGrant` 覆盖赠额在上限内也待批并预留、重放、负责人看到的账户和上限、批准后以 agent 的名义赠出（批次 ID 是操作 ID）、预留转为已用、`caps_get` 的用量；`TestOperationsCreditGrantRefusals` 覆盖没有策略、缺 `operations.money` 或 `credits.grant`、账户不在凭证的站点里、超过硬上限、零、过期的到期时间、别的币种、没有这个账户和试运行都不留预留；`TestOperationsCreditGrantOutcomes` 覆盖驳回、批准时到期已过、硬上限被调低后的批准都不赠出并释放；`TestOperationsCreditGrantApprovalRace` 用持有运营操作的锁让两个批准同时等待，只赠一次。

- `caps_test.go`：`TestCapDimensions` 对充值和赠额两个类别表驱动覆盖每个自主上限（单笔、日、周、对象日、笔数）转人工并给出那一条原因（同时超过几条就给几条，赠额只给 `always`）、每个硬上限（单笔、日、周）拒绝且不写、刚好在上限上充值直接执行而赠额仍待批，以及没有策略的站点和平台；`TestCreditCommit` 覆盖批准的赠额把自己的预留转为已用一次、确认者是它自己，重复或已释放的不能再转；`TestSetCapPolicy` 覆盖建、按版本改、币种不变、硬上限不低于自主上限和审计；`TestReservationReleases` 覆盖驳回、撤回、作废各自释放与审计的操作者；`TestReservationApproval` 覆盖策略放宽后的批准照常成功、调低后被拒并释放；`TestReservationCommit` 覆盖重放不重复预留、未结算的提案不能确认、确认一次、第二次被拒；`TestReservationExpiry` 覆盖过了日的确认被拒、清扫只释放一次；`TestCapRaces` 覆盖并发提案在计数器上排队、同键并发只预留一次、清扫跳过被持有的预留；`TestCapLockOrder` 覆盖锁的顺序；`TestBatchLockOrder` 覆盖两个凭证的提案按相反顺序落在平台充值和站点赠额两个范围时，两个吊销都完成并释放全部预留；`TestConfirmWaitingPastTheDay` 覆盖等计数器期间这一天结束的确认被拒、什么都不留下；`TestWindowEnded` 覆盖按当前时刻判断这一天是否结束；`TestExpiryJobRun` 覆盖任务的一次运行同时到期审批和释放过了日的预留；`TestCapPolicyReads`、`TestReservationOf` 覆盖策略列表、范围的币种和预留的读取；`TestCapUsages` 覆盖每条策略当天和本周的用量（含待批的预留）；`TestCapRefusals` 覆盖硬上限拒绝的说明、确认别的凭证或别的对象或不动钱的操作找不到、过了日的预留不能批准；`TestFailuresReachTheCaller` 覆盖数据库失败时每个调用都把失败带回给调用方。
- `reconcile_test.go`：`TestCapReconcile` 表驱动覆盖日的预留、周的已用、对象日的笔数、丢了的计数器行和没有资金的计数器行各自只暂停自己的范围（旁边一个没有策略、暂停不了的范围不影响它们）、审计和告警只发一次、暂停时额度内的小额也转人工；`TestCapRepair` 覆盖修复后计数器等于资金操作、恢复和解除告警、带理由的审计、没有策略的修复和没有漂移的修复；`TestCapDriftAuditParts` 覆盖 40 行漂移的审计拆成几条、每行都在、对象 ID 截到 128；`TestCapRepairLockOrder` 用持有修复的锁覆盖缺了对象行时确认和撤回等在策略上、不和修复互相等待；`TestCapPauseHoldsOldNodes` 覆盖旧节点在暂停的范围里写不进额度内的预留；`reconcile_job_test.go` 的 `TestReconcileWork` 覆盖任务的一次运行失败时只记日志、不重试，以及各步把数据库失败带回；`TestCapReconcileRaces` 用持有事务覆盖进行中的提案不算漂移、修复等进行中的提案并算上它、对账在策略锁下重比不暂停刚修好的范围。
- `cmd/axiom/operations_money_test.go`（走真实 HTTP 和数据库）：`TestOperationsTopUpRefusals` 覆盖没有 `operations.money` 的 403、没有单位价值和没有策略的 422、硬上限拒绝和试运行都不留预留；`TestOperationsTopUp` 覆盖直接执行与重放、待审批与负责人看到的摘要和上限、批准后仍是预留；`TestOperationsTopUpConfirm` 覆盖别的凭证确认 404、确认一次、重复确认 409 和审计；`TestOperationsTopUpPolicy` 覆盖策略调低后批准以 `stale_revision` 结束并释放，以及策略的列表、币种不变和旧版本；`TestOperationsTopUpReplay` 覆盖成功后供应商被移除、同键重试仍拿回原操作而新提案被拒；`TestOperationsTopUpExchange` 覆盖跨币种按汇率折算、没有汇率 422 和向上取整到 10⁻⁹。
- `cmd/axiom/operations_caps_test.go`（走真实 HTTP 和数据库）：`TestOperationsCapsScope` 覆盖 `caps_get` 只列凭证够得着的范围（站点凭证看不到平台），`TestOperationsCapsUse` 覆盖日、周的预留、已用、笔数；`TestOperationsCapRepair` 覆盖对账暂停后管理接口的 `paused_at` 和 `caps_get` 的 `paused`、暂停时小额转人工、修复不存在的策略 404、修复后计数器重算、审计并恢复直接执行；`TestOperationsMoneyRate` 覆盖资金写（含试运行）的单独限频、429 和 `Retry-After`、记进操作记录、读不受影响。`internal/accounts` 的 `TestAgentWritesAreLimited` 用受控时钟每秒请求两小时，检查资金写任何 60 秒不超过 30 次、任何一小时不超过 300 次，且持续速度不低于每小时 150；调低到每分钟 4、每小时 20 时和发告警时任何一个窗口也不超过设的次数；`TestAgentRequestsAreLimited` 覆盖请求每分钟 600 和调低到 10 时的突发与补回；`TestAgentLimitsLoweredMidWindow` 覆盖用过之后调低，窗口里已放行的仍算在内，直到窗口过去才再放行。`limits_test.go` 的 `TestAgentLimits` 覆盖每一项的 0 和超上限被拒、按版本改和审计，`TestAgentLimitsOfAnOldNode` 覆盖旧节点建的行把没有的项读成上限、旧节点的改动不动负责人设的其他项。

- `prices_test.go`（真实 PostgreSQL）：`TestPriceEnvelopeDimensions` 表驱动覆盖每个限值转人工、给出那一条原因且不计数、在内的计数、没有包络的站点；`TestSetPriceEnvelope` 覆盖建、按版本改、`PriceEnvelopes` 读回、旧版本和越界的限值、审计；`TestCountPrice` 覆盖批准后的计数（量不出比例按 10000）、没有包络的站点也计数，以及 `PriceUsages`；`TestProposePrices` 覆盖一次移动多个售价：合起来在内的全部计数、同一个模型合起来越过单日变动、合起来越过当天笔数和模型数、一笔量不出比例，越线时 `undo` 一次、摘要列出每笔和当天已用，以及重试找到原操作；`TestPriceRaces` 用持有事务覆盖同一个模型两笔各自在内、合起来超出的并发发布，单个发布和一次移动多个模型的改动各一遍。`TestPriceFailuresReachTheCaller` 覆盖数据库失败时每个售价调用都把失败带回给调用方。
- `cmd/axiom/operations_prices_test.go`（真实 HTTP 和数据库，调用世界里带成本的端点）：`TestOperationsPricePublish` 覆盖没有包络 422、试运行不发布、包络内以 agent 的名义发布并计数、重放、旧的当前版本 409 `stale_revision`、已发布的提案 409、`caps_get` 的包络用量；`TestOperationsPriceApproval` 覆盖超出包络转负责人、负责人看到包络、批准后发布并计数、提案在批准前被换掉以 `stale_revision` 结束；`TestOperationsPricePublishRetryRace` 用持有定价政策行的事务覆盖同一个键的两个并发请求答同一个操作、只发布并计数一次；`TestOperationsPriceRefusals` 覆盖没有 `operations.money` 的 403、包络的旧版本 409 和越界 400、列表和审计。`internal/billing` 的 `TestPriceMove` 覆盖共用售价、没有当前版本、没有成本、端点各自的变动和毛利率、端点现在没有价、端点价换回共用价、上下文分档口径改变；`internal/catalog` 的 `TestLargestChangeAndSmallestMargin` 覆盖两个量在各档、增删价格、零价和亏本上的取值。

- `chain_test.go`（真实 PostgreSQL）：`TestAuditChainLinksCommittedEntries` 覆盖按开始时刻的顺序接链、没过 60 秒的不接、不重复接、整条链核对通过，以及表只收接在链头之后的一条、应用改不了链也删不了故障；`TestAuditChainWaitsForOpenTransactions` 用持有一个写了审计的事务覆盖后提交的条目不越过它；`TestAuditChainHalts` 表驱动覆盖插到链头之前、删掉、改掉一条、改掉一条链和把链头的时刻改到将来各自被下一轮发现，故障、审计和告警各只有一次，之后不再接链；`TestAuditChainChecksBeyondTheWindow` 覆盖 1501 条分四个事务接完，改动第 501 条时每轮的核对看不到；整条核对每次两个事务时，第一次开一轮并核对到 500，取消的一次不动位置，下一次从 501 接着做并发现它；`TestAuditChainCheckRounds` 覆盖一天之内不开新的一轮、满一天开、开始满一天还没完时发逾期告警、核对完恢复；`TestAuditChainLag` 覆盖落后告警和赶上后的恢复。`chain_hash_test.go` 的 `TestLinkHash` 固定哈希的编码；`chain_fail_test.go` 的 `TestChainStepsFail` 覆盖每一步把数据库失败带回、只读事务里不写链和故障，`TestAuditChainWork` 覆盖接链和整条核对失败时只记日志。
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
