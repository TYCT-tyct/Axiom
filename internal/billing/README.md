# billing

计费模块的公开接口和它守住的不变量。行为和规则见[计费领域](../../docs/domains/billing.md)，一次调用的编排见 [gateway](../gateway/README.md)；实测数据和历史推导见[计费存档](../../docs/archive/billing.md)。依赖 catalog、suppliers、quality、accounts、platform；不依赖 protocol、routing。金额都是十亿分之一记账货币的 int64，精确计算用 `platform.Exact`。

## 接口（草图）

| 组 | 导出 | 说明 |
|---|---|---|
| 报价 | `Quote(Demand, []Quotable, Segments, []Available) (Reservation, error)`、`InputBound`、`InputEstimate`、`ReservedCost` | 纯函数；准入在锁下调用 `Quote`。`InputBound` 决定价格档，`Quote` 和路由最高价过滤共用；`ReservedCost` 给路由按原生成本计划判断供应商付不付得起 |
| 计量 | `Reservation.Bind(entry) *Meter`、`Meter.Observe(Progress) MeterStep`、`Meter.Extended`、`Meter.Complete`、`Meter.OutputCap` | 只归所有者 goroutine，`Observe` 不分配 |
| 持有者 | `Start`、`(*Billing).Heartbeat`、`Reap`、`Alive`、`Run`、`ErrReaped` | 见[持有者与截止时间](#持有者与截止时间) |
| 准入 | `(*Billing).ReserveMany(ctx, tx, []ReserveRequest) ([]*Hold, [][]int, []error, error)`、`Admitted`、`*Insufficient` | 同一账户一批，一个事务、一把余额行锁 |
| 追加与结算 | `Extend`/`ExtendIn`、`Settle`/`SettleIn`、`SettleBatch`、`RunBatch`、`SettleOrphan`/`SettleOrphanIn`、`Cleanup`、`Outcome`、`AttemptResult`、`Final`、`BatchWorkError` | `*In` 在调用方事务里，返回的 `after` 提交后才执行 |
| 额度 | `OpenBalance`、`AddCredit`、`CreditPaid`、`CreditRedeem`、`CreditsFor`、`ReadCredit`、`ReadCredits`、`TopUpRate`、`MinorUnits` | 写入要平台范围事务（审计） |
| 退款与拒付 | `PrepareRefund`、`FreezeRefund`、`QuoteRefund`、`SettleRefund`、`ReleaseRefund`、`RefundRecordedAt`、`ChargeBack`、`ReverseChargeBack` | 重复调用由 payments 的订单行挡住，失败须回滚整个事务 |
| 套餐 | `CreatePlan`/`UpdatePlan`/`ReadPlan`/`Plans`、`CreditSubscription`、`RequestSubscriptionRefund`/`ApproveSubscriptionRefund`、`Subscriptions`、`RegisterSubscriptions` | 见[订阅套餐](#订阅套餐) |
| 只读 | `ReadBalance`、`ReadFunds`、`KeysUsed`、`ReadKeyUsage(s)`、`ReadBudgetUsage`、`KeyDailySpend`、`ReadCreditBatches`、`ListStatement`、`NewChargeReader`（`ReadCharges`、`SettledFinals`）、`LedgerOf`、`ReadPlatformReport`、`ListCashMovements`、`MerchantCash`、`MerchantBatches` | 账户范围、不加锁；`LedgerOf` 含供应商成本，只在运营或管理处理器里用平台范围调用 |
| 分配缓存 | `AdvanceCreditBatches(ctx, db, scope, account, pages)` | 写：按页在自己的短事务里锁余额行、推进重放并保存位置；不能在已持有该账户余额行锁的事务里调用 |
| 定价 | `Rules`、`CreateRule`、`UpdateRule`、`Sell`、`PreviewPrices`、`RefreshPrices`、`PublishedPrices`、`PublishPrice`、`SetPricePolicy`、`PriceMove`、`ByokFee`、`SegmentPolicy`、`DefaultSegments` | 见[定价规则](#定价规则) |
| 汇率 | `SetExchangeRate`、`ExchangeRates`、`ExchangeRateHistory`、`RegisterExchangeSync` | 见[汇率与成本依据](#汇率与成本依据) |
| 周期任务 | `RegisterExpire`、`RegisterReconcile`、`Reconcile`、`RegisterCorrections`、`RegisterPrices`、`ConsumeBalanceNotices` | 在队列 Start 之前登记（`cmd/axiom`） |
| 兜底价 | `CheckFallbacks`、`SaveFallbacks`、`UnknownItems`、`ClearUnknownItem` | 见[兜底价](#兜底价) |
| 跨模块写 | `Suppliers` 接口（`RecordAttempts`、`RecordLostRequest`、`ResolveLostRequest`、`EndRequest`、`EndRequests`） | 由 `suppliers.CallWrites` 实现；结算和清理在 billing 的事务里写供应商侧，是[模块边界规则 5](../../docs/architecture.md#模块边界规则) 的同事务例外 |

- 后续事务（追加、结算、`SettleOrphan`）的账户范围只能来自 `accounts.Admission.Scope()`；gateway 把 `accounts.Admission` 转成 `Admitted`。
- `ReserveRequest.Segments` 是路由计划时冻结的值，热更新不改已计划请求的报价；`Segmentable` 由 routing 按条目算。
- **输出上界**：字节上界 = 已读完事件里解码后的正文、推理、工具参数、签名和加密推理的 UTF-8 字节（[protocol 的「字节上界」](../protocol/README.md#转发草图)）+ 未读完事件已到的字节，加 `ToolCalls × ToolCallTokens`；`Final` 标明之前，输出 = max(已报告, 字节上界)，且不超过 `OutputCap()`。`Relay.Next` 每次至多读 16 KiB，两次计量之间漏计不超过一次读。
- **只读接口的上限**：`KeyDailySpend` 只读最近 14 个 UTC 日；`ListStatement` 每页最多 200、一次最多读 8 轮候选，长段零变动可返回空页加游标，游标带下一行的余额和冻结额起点，序号之前的行 `Balance` 为 nil；`ExchangeRateHistory` 每页最多 100 行，`before_version` 是排他游标；`ConsumeBalanceNotices` 一次认领并删除最多 1000 行。
- **`InputEstimate` ≤ `InputBound`**：每一项都不大于上界的对应项，暂估永远不超过预留（`TestInputEstimateNeverExceedsTheBound`）。

## 报价

- 每一项按 `catalog.Schedule.Ceiling` 取请求可能用到的服务等级（总含标准档）和提示词可能落入的每一档里的最高单价；输入首次按首次提示词上界取档，之后每次按上下文长度。
- 计数饱和在 `math.MaxInt64`；除被上下文截住的首次提示词外，任何计数到达它就报错不报价（饱和会少预留）。在此之内 128 位不溢出。
- 缺价或模型没有上下文长度时整个请求不报价；可分段条目缺分段配置报 `errNoSegments`，不报一个输出为 0 的价。
- 余量按 `[]Available` 的顺序（余额、站长余额、工作区预算、key 上限），报价不超过每一个余量的条目保留，相等算付得起；首选被删返回指明第一个不够来源的 `*Insufficient`。
- 不变量：报价不低于按 `catalog.Schedule.Cost` 逐次采样计价的任一合规调用（性质测试），且与 `math/big` 无界计算相等。

## 计量

- 只计报价没按最大值预留的部分：输出，和超过报价次数的联网搜索（每次再加一次重读上下文）。输入侧准数不让计量后退。
- 花费越过当前段起点 + 一段 × 追加比例时 `Extend` 请求下一段，`Target` 是累计目标；越过已覆盖部分时 `Exhausted`，两者可同一步报。追加在途时不再请求；失败后原样重发同一段号和目标。
- `Left` = 已批准输出 − 已用；覆盖到输出上限、输出单价为 0 或条目不可分段时为 `math.MaxInt64`。

## 同一账户的合并预留

- `ReserveMany`：锁余额行、核对持有者、读兜底价和数据库时钟一次；锁后的语句才读工作区和 key 的计数（新快照看得到锁前的提交）。窗口按批次开始时的数据库时钟截断到 UTC 日。后面的请求使用前面请求将占的额度（饱和相加），整批不越底线或限额。业务拒绝放进 `[]error`、不写行；数据库错误整批回滚。
- `RunBatch`：同一账户的多个 `SettleIn`/`ExtendIn`/`SettleOrphanIn` 一个事务；失败的 work 带索引（`BatchWorkError`），整批回滚后其余由网关在新事务重试。`SettleBatch`：全是结算时一把锁、集合语句写整批；集合 SQL 错误没有项号时回滚后逐项走单结算定位，成功项各自提交。没有保存点，按请求 ID 幂等，重试最多收一次。
- 不变量：①余额行是账户资金写入的唯一串行点；②批内按项、项内按尝试顺序写事件，`seq` 按锁的取得顺序递增；③每项按前项留下的余额规划，收费上限与逐个结算相同；④`(request_id, attempt_seq, kind, revision)` 唯一，重试返回已存的 `Final`；⑤终态、`EndRequest`、供应商尝试与账本同一事务，报告在提交后；⑥批的余额按精确合计写入终值，越过 int64 回滚。⑦套餐结算按 `(subscription_id, seq)` 去重核对窗口更新行数，少更新一个窗口就回滚整批账本、预留和计数（`TestSettlementBatchChecksDistinctWindows`）。

## 结算与账本

- 收费 = min(应收, 余额 − 底线 − 其他预留 − 全部冻结)，精确计算，超出记 `platform_borne`，提交后记 Error 日志并计数（重试时不重复报）。没有尝试的结局只释放。
- 免单只看收费那次尝试（`waivable`、`zeroOutput`），它的 `Finish` 为 `FinishCancelled` 才算取消；没有上限、不数次数。
- 暂估（`Final.Estimated`）：收费项里有上游没报告的、缺价的（`addMissing`）或映射不了的用量（`addFallbacks`）。
- 迟到的 `Settle`：版本号 −1，先冲正再记这一笔，补扣到底线为止，`ResolveLostRequest`。找不到预留行时返回错误。
- 账本：每笔交易的分录在每个币种里合计为 0（`post` 断言）；`balances.balance` = −Σ user_balance 分录，同一条语句更新；分录带产生它的租户，平台收入在平台范围按 `book` 汇总。`PostLedger` 先锁余额行再插入交易、分配序号，所以同一账户的每笔交易都在锁内编号。
- `CreditPaid` 检查渠道恰好一个、`RateID` 是付款币种与记账货币这一对（同币种为零）、入账额度在 1 到 `CreditsFor` 之间；同 ID 不一致返回 `ErrCreditConflict`（不比较过期时间）。

### 暂估校正

- `RegisterCorrections` 每分钟在 money 队列运行：游标 `billing.charge_correction_cursor`，待结算事件在 `billing.charge_corrections`。在账户事务里锁余额和预留，用锁定的价目表、兜底价和档位重算，在事件版本上 `reverse` 再 `settle`；利润按原利润加实收差额（原来未知仍未知）。`charged_seq` 为 NULL 的循环收费不修正；`cost_flag` 不作为记录完整的依据；检测事件直接丢弃。
- 事件生产者与消费者共用事务级 advisory lock，事件时间用 `clock_timestamp()`。

## 额度批次的分配与到期

- 状态在 `credit_batches`（`consumed`、`expired`、`freeze_total`、`unfreeze_total`、`refunded`、`charged_back`）和 `balances`（`allocated_*` 位置、`unfunded`）。剩余 = 金额 − 已用 − 已作废 − 已退款 − 已拒付；冻结中 = 冻结累计 − 解冻累计 − 已退款。
- 重放（`allocation.apply`）按序号读，`seq` 为空的老交易按 `(created_at, id)` 排在最前；一页的读量随页大小、不随历史增长。无锁预放在读批次之前固定可见的最大序号，读批次前后各读一次位置，变了就丢弃（`errStale`）。
- 不变量：①重放到最新后 Σ 批次剩余 − `unfunded` = `balances.balance`；②每批已用 + 已作废 + 已退款 + 冻结中 ≤ 金额，已退款 + 已拒付 ≤ 金额，冻结中 ≥ 0；③`unfunded` > 0 时没有批次还剩未冻结额度；④作废不超过 `room`（余额 − 在途预留 − 全部冻结，与收费上限同一个函数、底线取 0）；⑤作废幂等；⑥一笔扣费可以跨批次，每单位额度只分配一次；⑦Σ 批次冻结中 = `balances.frozen`；⑧从头重放与从任一存下的位置续放结果相同（`TestReplayProperty`）。
- 冻结、解冻、退款以批次 ID 为 `request_id`，版本号是该类写入后的累计额（超过 int64 报错）；拒付的版本号是第几笔拒付。`FreezeRefund`、`ReleaseRefund`、`SettleRefund` 把调用方事务的 `enable_sort` 设为 `off` 直到结束（[pg 的计划取舍](../platform/pg/README.md#决策与理由)）；`PrepareRefund` 必须在取订单锁之前调用。

## 订阅套餐

- `reservations.funding`、`subscription_id`、`window_seq` 记住来源；结算、释放、清理、校正都按它们回到原窗口。`Admitted.Preference` 是账户生效的计费偏好。
- 不变量（另见[领域文档](../../docs/domains/billing.md#订阅套餐)）：⑥退款的 `refunded_value` 不超过付费额度、`refunded_windows` 不超过窗口数，收回窗口 `voided_at` 非空且不参与准入；⑦同一退款 ID 重放返回同一行、不再记账；余额行的 `reserved` 只含钱包来源，每个窗口的 `reserved` 是对应套餐预留之和。

## 兜底价

- `billing.fallbacks` 每币种一行，`body` 只含币种与 `web_search`、`code_execution` 的单价；`Start` 和 `serve` 要求有 USD 行、每项 0 < 单价 ≤ 1，不合格不启动。`ReserveMany` 用 `catalog.Schedule.Fill` 补默认价后锁进 `prices`，兜底行锁进 `reservations.fallbacks`；没有兜底行的币种 `ErrUnpriced`。
- 映射不了的像 token 的字段（以 `tokens` 结尾、0 到 10¹⁰ 的整数）按最高 token 单价；其余按次数（十进制向上取整，最多 10¹⁰，非十进制按 1）× 最高按次默认价；应收按 int64 饱和。
- 结算遇到映射不了的字段或未声明的已知服务档（`service_tier.<名称>`），按尝试的条目模型写 `unknown_items`，提交后告警并发 `billing.settlement_anomaly/unknown_items`；路由据此置 `routing.Endpoint.UnknownItem`。设置行和预留锁定的兜底文本用同一个闭集解析器。

## 追加与结算

- `Extend` 让预留覆盖累计目标：已覆盖即成功不写；否则以锁内读到的段号比较后写差额，金额只增不减；已终结或持有者被回收返回 `ErrTerminated`。各次尝试的计量器各自从第 1 段编号，段号不用来判断重试。
- `SettleOrphan`：准入确认丢失时调用，锁余额行后有挂起预留就以"没有尝试"释放，没有就写 `released`、金额 0 的墓碑；重复执行无害，数据库错误由调用方重试。

## 持有者与截止时间

- 持有者一行 `(id, expires_at, reaped_at)`，时间一律用锁后的 `clock_timestamp()`；默认值见 [gateway 的「超时与限额」](../gateway/README.md#超时与限额)。
- 写入栅栏：准入、追加锁余额行后以 `FOR KEY SHARE` 读自己的持有者，要求 `reaped_at IS NULL AND expires_at > clock_timestamp()`（两条都要：时钟回拨会让已回收的重新"未过期"）。
- 回收 `SELECT … FOR UPDATE` 持有者，与写入栅栏互斥；置 `reaped_at` 提交后才处理它名下的预留，这时不可能再有新的。
- `Cleanup(ctx, max)` 逐笔一个事务，一次最多尝试 `max` 笔；游标跨调用延续、越过失败的，所以恒失败的积压挡不住后面的。

## 预留的状态

| 状态 | 从哪来 | 迟到的 `Settle` |
|---|---|---|
| `pending` | 准入 | — |
| `settled` / `released` | `Settle`；`SettleOrphan`（含墓碑） | 返回已存的 `Final`，不再记账 |
| `cleanup_released` | 清理：过截止时间或持有者已回收，记丢失的请求 | 按完整 `Outcome` 收费，补扣到底线，`ResolveLostRequest` |
| `cleanup_settled` | 旧库遗留，不再写入 | 同上，但不 `ResolveLostRequest` |

已结算、已释放的行不能再改；状态变化一律比较后再写。找不到预留行时 `Settle` 返回错误。

## 定价规则

- `RuleInput.Check` 拒绝契约模式表达不了的：ID 种类、每种方法只带自己的成员、比率非负、毛利率 < 1、固定价表合法且是站点币种、任何约束成员、未生效的对象、没选 `currency_conversion` 的参考价倍率；错误是只带成员名的 `*RuleError`。已有不支持的规则可以原样停用。
- `Sell` 纯函数：没有方法、不支持、没有参考价都是 `ErrUnpriced`，带约束的是 `ErrRuleRefuses`，不悄悄按参考价卖；倍率乘在 `catalog.Schedule` 上，与成本侧 `Schedule.Scaled` 同一段代码，只取整一次。
- `:free` 模型的提案非零时 `ErrFreePrice`、不发布；`Sold.Zero()` 供路由核对每层都是零。
- `PublishedPrices` 只读；`Sold.WithCosts`/`AllowsCosts` 按本节点成本逐端点过滤；发布者 30 秒没有成功刷新时读不到可服务版本。
- 默认值只在代码：保本倍率 1、确认阈值 0.1、自动提价上限 1、BYOK 500 bps；政策用 rev 并发控制。

## 汇率与成本依据

- 同一规范化货币对用事务级 advisory lock 串行（键是命名空间字节加六个币种 ASCII 字节）；`billing.exchange_rates` 只有平台范围 SELECT/INSERT。`ExchangeRates` 每对按 `(base, quote, version)` 倒序取一行。
- 准入把保留条目需要的汇率（含缺失）写入 `reservations.exchange_rates`：NULL 是历史未知，`[]` 是确定不需要；目前只用于审计。
- 结算精确相加各尝试成本：每次都已知（换算已知且 `CostFlag` 为 `complete`）才写 `reservations.profit` = 实收 − 成本，否则 NULL；没有尝试为 0。账本里换算未知的尝试按 0 记 `supplier_cost`：汇总成本时排除 `profit IS NULL` 的请求或关联 `attempt_costs.conversion_state`。
- `RefreshPrices`、`PreviewPrices` 的成本与售价用同一份汇率切片，提案内部不再查当前值。

## 写入的数据

应用没有任何表的 DELETE，UPDATE 只给可改的列；别的模块的行只按 ID 引用、不建外键。

| 写什么 | 幂等键 / 守护 |
|---|---|
| 预留行与三层占用 | 主键 `request_id`；写入栅栏；余额行锁 |
| 追加 | 已覆盖即成功；否则 `segment` 与 `state='pending'` 比较后写 |
| 尝试成本分录与 `attempt_costs` | `(request_id, attempt_seq, attempt_cost)`，与收费同事务 |
| 请求结算、花费、预留终态、`EndRequest` | `(request_id, 0, settle, 0)` |
| 加额度 | `(credit_id, 0, credit, 0)`；批次主键；审计同事务 |
| 额度过期 | `(batch_id, 0, expire, 作废前累计)` |
| 冻结、解冻、退款 | `(batch_id, 0, kind, 写入后累计)` |
| 迟到补扣 | `(request_id, 0, reverse / settle, -1)` |
| 暂估校正 | `(request_id, attempt_seq, reverse / settle, 事件版本)` |
| 套餐发放 / 作废 / 退款 | 发放以请求 ID 为主键；作废以订阅 ID 为 `request_id`，`closed_at` 只置一次；退款申请 ID 为主键 |

## 决策与理由

| 决策 | 理由 |
|---|---|
| 报价是纯函数，准入在锁下调用 | 规则最多、最易错，脱开数据库才能逐条证明。 |
| 单价取 `Ceiling`：所有可能项、等级和档中的最高价 | 提示词拆分可落到任一档，只有这样是可证明的上界。 |
| 计量器自己算累计目标，`Extend` 只比较后写 | 重发和前一次尝试的追加都不会多扣。 |
| `Extend` 看目标是否已覆盖，不看段号 | 各次尝试从第 1 段编号，按段号会把更大的目标当重试放行。 |
| 只保留批量准入 `ReserveMany` | 单请求路径没有调用方，两份写预留的 SQL 已经分叉。 |
| 结算批用集合语句写整批，单项走原路径 | 完成批仍按项写会让尾延迟随批线性增长；n=1 时集合语句更贵。 |
| 锁定的价目表和兜底行存进预留行 | 清理和迟到结算没有准入时的进程，改价不该改在途请求。 |
| 心跳与回收用池外专用连接 | 池占满时等连接会错过心跳，`Alive` 变假让整个节点 503。 |
| 计数按 UTC 日存，追加记回预留那天 | 跨窗口边界时检查与归账不错位。 |
| 每条分录带产生它的租户 | 账户范围事务写不进没有租户的行。 |
| 付费额度与赠送同一种 `credit` 交易 | 分配重放不用改。 |
| 余额提醒用 invoker trigger 写 `balance_notices` journal | 不增加往返；每条变动冻结前后金额，乱序消费也不漏阈值。 |
