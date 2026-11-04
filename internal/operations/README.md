# operations

运营看板的业务目标在代码里的接口、表和不变量。规则和理由见[运营的「业务目标」](../../docs/domains/operations.md#业务目标)；本文只写实现时依赖的部分。

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
