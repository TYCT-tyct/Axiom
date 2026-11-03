# operations

运营看板的业务目标在代码里的接口、表和不变量。规则和理由见[运营的「业务目标」](../../docs/domains/operations.md#业务目标)；本文只写实现时依赖的部分。

## 接口

```go
type Metric string // calls、errors、revenue、cost
type Money struct{ Amount string; Currency platform.Currency } // 十亿分之一，十进制文本
type Targets struct{ Month time.Time; Calls, Errors *int64; Revenue, Cost *Money } // nil：这个月没设
type Expected struct{ Calls, Errors *int64; Revenue, Cost *Money } // 已折算到窗口
func Read(ctx context.Context, tx pg.Tx, site uuid.UUID, month time.Time) (Targets, error)
func ExpectedFor(ctx context.Context, tx pg.Tx, site uuid.UUID, currency platform.Currency, since, until time.Time) (Expected, error)
func Set(ctx context.Context, tx pg.Tx, actor audit.Actor, site uuid.UUID, set Targets, reason string) error
func (t Targets) Check() error
```

- **谁调**：`cmd/axiom` 的 `GetOperationsTargets` 和 `UpdateOperationsTargets` 读写编辑器；`GetOperationsOverview` 用 `ExpectedFor` 把目标放进看板。本模块不引用 `observability` 或 `billing`：实际值由 `cmd/axiom` 算好，`Expected` 只带目标。
- **月度**：`Read` 把任何时刻截到 UTC 该月第一天；`Check` 要求 `Month` 正好是第一天零点。API 的路径参数只到 `YYYY-MM`，所以不会出现半天或本地时区的月份。
- **折算**：`ExpectedFor` 读窗口触及的每个月（`since` 所在月到 `until` 所在月），每行按 `start = max(since, month)`、`end = min(until, nextMonth)` 裁剪，`prorate` 用 `目标 × 重叠秒数 ÷ 该月秒数` 四舍五入到指标的最小单位（调用/错误到 1，金额到十亿分之一），再逐月相加。乘加用 `big.Int`，最后才检查结果的 `int64` 宽度和币种。窗口没有长度时返回空的 `Expected`。
- **币种**：金额行的币种和调用方给的站点记账币种不同就跳过，不换算；调用/错误没有币种。
- **写**：`Set` 先 `LockTargets`（`pg_advisory_xact_lock(hashtextextended(site + ":" + month, 0))`），再 `Read` 旧值，按 `calls`、`errors`、`revenue`、`cost` 的固定顺序 upsert 或 delete，最后 `audit.Record`。这些都在调用方的事务里；成功返回前没有提交，失败由调用方回滚。
- **权限**：本模块不判权限。`sites.manage` 和 `mfa` 在 `cmd/axiom` 的 guard 里；`Set` 的 `actor` 和 `reason` 只用于审计。

## 表

`operations/targets.go` 和 `migrations/00001_targets.sql`：

- `operations.targets(site_id, metric, month, amount, currency, updated_at)`，主键 `(site_id, metric, month)`；`metric` 只允许四个值，`month` 必须是当月第一天，`amount` 是非负 `numeric(38,0)`。
- `currency` 只在 `revenue` 和 `cost` 上有值；表级 check 让“是金额指标”和“有币种”等价，币种格式是 `^[A-Z]{3}$`。
- `targets_by_month` 索引 `(site_id, month)` 供看板按窗口读多个月。
- 读查询把 `amount::text` 取出，不经过 `int64` 或浮点；调用/错误在 Go 里 `ParseInt` 到 `int64`，金额保留十进制文本。
- 行级安全用语句级 InitPlan：平台范围看全部，否则只允许 `site_id` 在 `platform.allowed_sites()` 里的行；SELECT、INSERT、UPDATE、DELETE 四条策略同一条件。`axiom_app` 只有这个表的读写权限。

## 不变量

| 不变量 | 怎么保证 |
|---|---|
| 一个站点一个月每个指标最多一行 | 主键 `(site_id, metric, month)`；`Set` 对四项各写一次或删一次 |
| 没设的目标不存在，0 是设了的目标 | `Targets` 用指针区分；`detail()` 省略 nil；API 的 `omitempty` 和页面分开显示“未设目标” |
| 读者看不到半套目标 | 四项写入、旧值读取和审计在同一个事务；同一站点和月份的写入先取 advisory lock，并发写有先后 |
| 窗口跨月时不丢目标 | `ExpectedFor` 读 `since` 到 `until` 触及的每个月，逐月裁剪后相加；表驱动测试覆盖整月、部分月、跨月和空窗口 |
| 金额不经过浮点，超出 64 位的中间结果不溢出 | 表和查询用 `numeric` 文本，折算用 `big.Int`；超宽的结果被 `countTarget` 拒绝而不是截断 |
| 币种不符的金额目标不参与对照 | `expectedFrom` 跳过；handler 再检查 `Expected` 的币种与站点当前币种一致 |
| 未记录的成功改动不能留下 | `audit.Record` 和写入同一事务；`TestAdminOperationsTargets` 读审计的 `before`/`after` |

## 测试

- `targets_test.go`：`TestExpectedFromProratesEachMonthBySeconds` 表驱动覆盖整月、部分月、跨月、闰年、空窗口、金额币种不符和四种指标的半入；`TestExpectedFromRefusesACountWiderThanItsType` 覆盖超宽；`TestTargetsCheck` 覆盖月份、负数、非法金额和币种。
- `cmd/axiom/admin_targets_test.go`：`TestAdminOperationsTargets` 走真实 HTTP 和数据库，覆盖空月份、四项写入、审计、看板折算、重放、整月替换和清零；`TestAdminOperationsTargetsNeedSitesManage` 覆盖无权限；`TestAdminOperationsTargetsRefuseAnotherCurrency` 覆盖币种。
- `cmd/axiom/admin_guard_test.go` 把两个操作登记进权限契约：GET 和 PUT 都需要 `sites.manage`，PUT 还要 `mfa`、CSRF 和 `Idempotency-Key`。

## 决策与理由

- **数值用文本，不用 `int64` 存金额**：目标可以是很大的月流水，`numeric(38,0)` 和文本能保住所有位；折算的中间结果用 `big.Int`。调用和错误天然是 `int64`，读取时再检查。
- **按站点和月份加锁，不锁整张表**：同一站点的同一个月才需要串行；不同站点或月份的写入可以并行。锁的键是 `site:YYYY-MM`，和主键的前两维一致。
- **折算在 Go 里做，不在 SQL 里做**：窗口跨月、每月天数不同、半入规则和币种过滤都在一个纯函数里，测试不需要数据库；SQL 只按月份范围取行。
- **API 的 `targets` 是可选字段**：滚动发布时旧节点不返回它，新页面显示“不提供”而不是“没设目标”；新节点的空对象表示这个窗口没有目标。目标接口只在新节点上存在。
