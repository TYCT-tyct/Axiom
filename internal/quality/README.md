# quality

端点评估在调用路径上的代码。规则和理由见[端点评估](../../docs/domains/quality.md)的「健康与熔断」，本文写代码层面的接口、不变量和取舍。

> 已实现：[#313](https://github.com/TYCT-tyct/Axiom/issues/313) E3-4 的 H1、H2（见[实现拆分](../../docs/domains/quality.md#实现拆分)）：测量、独立来源窗口、实时熔断、最后备选、慢启动、持久化和有租约的试探；[#314](https://github.com/TYCT-tyct/Axiom/issues/314) E3-16 的运营质量分（首字延迟、输出速度、错误率和试探的 EWMA，未知用置信度 0 表示）和端点真伪的指纹检测（随机指纹题、已知答案、`Verified`）。默认只观察；H3 的管理接口和 H4 的线路健康尚未实现。

## 接口

```go
func New(meter metric.Meter) *Monitor // meter 为 nil 时不报指标
func (m *Monitor) Observe(ctx context.Context, o Observation) // 每次尝试结束时一次；0 分配
func (m *Monitor) MeasuredFor(ep suppliers.Routable, now time.Time) Measures // 端点当前的数，并核对检测证据属于这个配置
func ParseMode(value string) (Mode, error) // 默认值与合法性只在此定义
func Start(ctx context.Context, cfg Config) (*Monitor, *Loop, error)
func (m *Monitor) Prepare(ctx context.Context, endpoints []suppliers.Routable) error
func (m *Monitor) Live(id suppliers.EndpointID, credentials int) *State
func (s *State) Routing(now time.Time) (opened time.Time, limit int64)
func (l *Loop) Tick(ctx context.Context) error
func (l *Loop) Run(ctx context.Context)
func (l *Loop) Drain(ctx context.Context) error
```

- **谁调**：gateway 的尝试在 `close` 里调 `Observe`，每次尝试恰好一次，含流式阶段（`attempt.observe`）；`RouteDeps.Build` 每次重建快照时对每个端点调 `MeasuredFor`，填进 `routing.Endpoint` 的 `Health`、`Quality`、`Confidence`、`Throughput`、`Latency`、`Verified`。两边用的是同一个 `Monitor`，由 `cmd/axiom` 建一个交给两边。
- **`Observation` 是事实，不是结论**：账户所有者（由准入的 `OwnerID` 提供；缺失时只计测量，不进摘除窗口）、所用凭证、请求 ID 与路由是否刻意选择了熔断最后备选，适配器的 `*suppliers.Failure`（没有 2xx 时）、流读失败的 `protocol.Fault`、网关为什么取消了尝试（`Cut`：响应头超时、空闲超时、调用自己的原因）、客户端是否已走、首段内容是否在客户端还在时交付（`Delivered`）、是否正常结束、是否 200 无正文（compact 的空正文也是）、预估输入 token（`billing.InputEstimate`）、输出 token（结算用的数，上游没报时是估算）、发出、首段内容、结束的时刻；首段内容和结束（或失败）的时刻在读到时记下，不含网关自己随后的等待（追加预留、关闭、算成本）。判定只在本模块（`judge`），网关不预先下结论。
- **`Measures`**：`Health` = 1 − 失败的加权平均（α 0.2，没有计入的结局时为 1）；`Latency`、`Throughput` 在"测过"（样本 ≥ 5 且最近一个在 30 分钟内）之前为 0，路由按 0 认作没测过。`Verified` 和 `VerifyConfidence` 只在一次通过的指纹检测还在 `verifyMaxAge` 内、且之后没有矛盾时为真/非零；`VerifyConfidence` 从通过时刻的 1 线性衰减到 0。`VerifiedAt` 是最近一次通过的时刻，过期或被矛盾推翻后仍保留，用来区分"从没测过"和"测过但已经不新"。
- **质量分与置信度**：`Quality` = `Health` × 已测性能因子的平均；首字延迟 ≤ 1 秒记 1（否则 1 秒 ÷ 延迟），输出速度 ≥ 50 token/s 记 1（否则 速度 ÷ 50），都测过取平均、只测过一个取它、都没测过记 1。没有计入的结局时 `Quality` 恒为中性 1、`Confidence` 为 0，即使不计入结局的尝试已经留下足够的延迟样本（这些样本仍留在 `Latency` 里、供"最快"和"最低延迟"使用，但不改变质量分）；计入结局数达到 10 后证据满，最后一个结局 4 小时后 `Confidence` 线性到 0。试探通过/失败同样计入（`count`），但不进熔断窗口：不满足独立来源。常量是 `scoreLatency`、`scoreSpeed`、`scoreSamples`、`scoreMaxAge`；计算只在 `State.quality`，`MeasuredFor` 不查库、不分配。
- **真伪检测**：健康循环对每个平台的、未熔断的、到期（`verifyEvery` 6 小时，先让恢复试探）的端点发一次 `TestEndpoint`：提示词要求原样回显随机十六进制串、给出两个随机整数之和、用一个词说出模型作者，输出上限 `verifyOutput`（48），并用 `TestRequest.Capture` 取回答复文本。判定只在 `judgeVerification`：三项都对为通过；答案里出现目录里的另一个作者、且没有声称的作者为矛盾；其余为不确定（拒答、格式不同、传输失败），不改变上次结论。一次矛盾立即清掉 `Verified`（失败关闭），连续 `verifyFailures`（3）次才判定假冒并发一次 `endpoint_fake` 严重告警；通过清空矛盾计数并解除告警。重查从 `verifyRetry`（15 分钟）按次翻倍到 `verifyEvery`，带 ±10% 抖动。结果按节点保存在 `State.verify`，不落库，重启后未知。证据绑定检测时的目录模型、上游名字和格式（`claim`）：投影里任一变化就清掉通过和矛盾计数、解除假冒告警并立即重查，检测途中变化则丢弃结果并记 `stale`；路由读 `MeasuredFor`，配置不符时直接不认这次通过。`Config.Identity` 由 `cmd/axiom` 从目录快照实现，返回声称的作者的显示名和目录里其余作者的显示名；目录不认识模型时这一轮不检测，15 分钟后重试。`AXIOM_MODEL_VERIFICATION=off` 关掉检测，所有端点保持未验证（`cmd/axiom` 的 `verify` 依赖为 nil）。
- **启动与时钟**：生产由 `cmd/axiom` 在监听前调用 `Start`，一次加载全部健康行；首次出现且没有持久行的健康端点不再逐个读库。只有被内存回收过的端点再次出现时才单独读回；`Config.Now`、`Config.Rand` 可替换，默认使用系统时钟与并发安全随机源。`Loop.Run` 每秒调用 `Tick`，失败退避。`Tick` 与 `Drain` 只有一个调用者；生成 worker 只通过有界结果通道回报。退出等待已支付的生成读完、写成本，再刷新剩余变化；试探不占调用的结算预留连接。
- **实时路由**：快照持有 `Live` 的指针，`Routing` 返回熔断时刻和恢复后的临时上限。`Prepare` 在后台为再次出现的端点恢复被回收的内存状态；不从这里定期加载已存在的状态，跨节点定期采纳仍属 H5。`Pauses` 由 routing 实现，经组装层接入。
- **持久化与试探**：迁移 `00001_health.sql` 只建 quality 的两张表，平台范围读写；时间用 Unix 纳秒整数，零表示未发生。窗口不落库。每次变化按旧 `epoch` 条件更新并写事件；失败重试保留同一代际，落败的一方丢弃依赖它的排队变化。试探租约两分钟，覆盖 45 秒生成与事务余量。租约认领不推进健康代际，真实请求恢复不因同时认领而作废；试探结果写入额外检查认领者、精确到期时刻与代际，超期或旧认领的结果不能恢复端点。每节点最多四个试探、滚动一小时最多 600 次，暂停（含 `Funds.CanPay` 的供应商余额拒绝状态）、陈旧与自带端点不消耗预算；每次用原生格式、16 token 上限，按前次凭证轮换，成本和检测审计同事务。拿到事务连接后，剩余期限不足完整生成期限加五秒写入余量时不发生成，按不确定结果顺延，避免等待连接吃掉已付费生成的记账时间。
- **交接与事件**：`Observe` 直接在固定结构里更新测量与窗口，只有状态变化进入容量 1024 的值通道；交接满时推迟新变化并计数，已接受的变化保留重试。事件与状态原子提交，反复事件在十分钟内合并，按天清掉 90 天前事件；健康变化和平台持久信号同事务，重启不丢已提交的告警。恢复同事务解除摘除和持续故障告警，恢复信息立即完成；预算告警用 `Config.Node` 的稳定节点名，只在顺延与恢复的边沿发信号，启动后的首次正常调度清理旧进程遗留。模型不可用只检查平台端点。离开可路由集合一天的内存状态回收，持久行保留；先建 ID 集合再持锁扫描，回收是线性工作，保留已回收 ID 供再次出现时读库。
- **指标**：`axiom.quality.unclassified`（端点范围、状态码 < 500 的 `Unknown`）、`axiom.quality.suspected_block`（疑似出口拦截）、`axiom.quality.verification`（`result`：passed、contradicted、inconclusive、stale、deferred），都不带端点标签。试探时长只计带真实请求 ID 的结果，未发出请求不写零时长。

## 不变量

| 不变量 | 怎么保证 |
|---|---|
| H-I4 只有判定表里"失败"的结局记为失败，"不计"的不进健康度 | `judge` 是唯一的判定；`TestJudge` 逐行覆盖：每个 `Cause` × 作用范围、客户端是否已离开、流内错误类别（`protocol.Fault` 的每个值）、预估输入是否超过阈值；`TestUncountedChangesNothing`：不计的结局不改变健康度（既不降、也不重置）；`TestEveryAttemptIsObservedOnce`：网关的每条尝试结束路径（5xx、429、响应头超时、200 无正文、提交前后断流、空闲、客户端提交前后离开、客户端离开后才来内容再断流、compact 与空的 compact）恰好报一次、按表计；`TestThroughputLeavesOutOurOwnWaits`：上游结束后还在等的追加不算进吞吐 |
| H-I1 只写本模块的健康表 | `TestQualitySQLDoesNotWriteSuppliers` 扫描 SQL 和生产 Go 中的 SQL；`TestGatewayHealthRemoveProbeRestore`、`TestGatewayDisabledEndpointIsNeverProbed` 核对人工开关与成本状态；前者故意让测试时钟落后，以网关记录的实际熔断时刻推进探测，避免把组装耗时算进首次等待 |
| H-I2 人停用不路由、不试探 | `TestGatewayDisabledEndpointIsNeverProbed` 用真实组装，直接置位后刷新 suppliers，推进时钟；`TestProbeDeferrals` 覆盖无路由快照、暂停、自带和只观察 |
| H-I5 窗口、硬失败、独立来源同时满足 | `TestIndependentFailures`、`TestWindowAndLateSuccess`、`TestObserveModeAndHealthyTraffic` 覆盖坏 key、单所有者、仅超时、闲置清窗和固定种子的 36 万个结局 |
| H-I6 最后备选仍遵守其余过滤，恢复并发渐进放开 | `TestLiveHealthLastResortAndSlowStart`、`TestOwnedFallbackDoesNotBypassOtherFilters`、`TestLastResortPreservesRequestPriorities`；标记进路由记录的 `breaker_open_fallback` |
| H-I7 旧请求与旧试探不能恢复新一轮熔断 | `TestWindowAndLateSuccess`、`TestOnlyDeliberateLastResortSuccessRestores`、`TestProbeConcurrencyAndStaleResult`、`TestLosingEpochDiscardsDependentTransitions`、`TestLeaseDoesNotInvalidateRealRecovery`、`TestQueuedProbeCannotOverwriteNewLease` |
| H-I8 有限试探、暂停不消费预算 | `TestPersistLeaseRestartAndRecovery`、`TestProbeDeferrals`、`TestProbeConcurrencyAndStaleResult`、`TestFundsDeferralDoesNotConsumeProbeBudget`、`TestGatewayHealthDefersProbesDuringFundsRefusal`；供应商的 `TestHealthProbeRotatesCredentials` 覆盖实际选 key |
| H-I10 下一次路由即时生效、重启和快照替换保留状态 | `TestLiveHealthLastResortAndSlowStart`、`TestPersistLeaseRestartAndRecovery`；`TestPrepareOnlyRestoresCollectedEndpoints` 核对首次出现不额外读库、回收后先恢复持久行 |
| H-I11 多节点只有获胜的条件更新写事件 | `TestPersistLeaseRestartAndRecovery` 竞争认领；`TestLosingEpochDiscardsDependentTransitions` 竞争变化；`TestCommittedSignalSurvivesRestart` 核对已提交告警跨重启保留；`TestRecoveryClosesIncidentSignals`、`TestProbeBudgetSignalsFollowCapacityAndNodeIdentity` 覆盖两轮故障恢复和预算生命周期，`TestModelUnavailableOnlyCountsPlatformEndpoints` 隔离租户可见性 |
| H-I13 只观察不改路线、不发试探 | `TestGatewayHealthRemoveProbeRestore` 的 observe 分支、`TestProbeDeferrals` |
| 真伪的未知、未通过、假冒分开；一次矛盾失败关闭，连续三次才判定假冒并告警；证据绑定检测过的配置 | `TestJudgeVerification` 与 `TestAnswerSum` 逐项覆盖三项答案、另一个作者、拒答、错和、被随机串吞掉的和、把行号当答案与复述算式；`TestVerificationPassesAndFailsClosed` 用受控时钟走过通过、一次矛盾、三次矛盾与再次通过；`TestInconclusiveVerificationKeepsThePass` 核对不确定不改变结论；`TestVerificationChecksOnlyPlatformEndpoints` 核对目录不认识与租户端点不检测；`TestVerificationFollowsTheUpstreamName` 与 `TestVerificationDropsACheckOfAnotherClaim` 核对换上游名时清证据、拒收在途结果；`TestVerificationAlertsAFakeOnce` 核对告警只发一次并在通过时解除；gateway 的 `TestExactoRoutesOnlyVerifiedEndpoints` 核对 503 `not_verified` 与只选已验证端点 |
| 质量分来自实测的 EWMA，未知与坏分开，试探计分但不进熔断窗口 | `TestQualityScore` 用受控时钟逐项核对可靠性、延迟、速度、未知、置信度衰减；`TestQualityWithoutCountedOutcomesIsNeutral` 核对只有不计入结局的延迟样本时仍是中性 1 加置信度 0；`TestProbeResultsScoreTheEndpoint` 核对通过与失败的试探都计分、窗口不变；routing 的 `TestBestQualityOrdersByTheScore`、`TestBestQualityUnknownByPrice` 和 gateway 的 `TestRoutePublishesMeasuredQuality` 核对排序和快照里的值 |
| `Observe` 不分配 | `BenchmarkObserve` 先建好端点的条目，再用 `testing.AllocsPerRun` 断言 0，否则失败 |
| 共享状态有锁 | 端点表由 `RWMutex` 保护，每个端点的数由它自己的锁保护；`TestObserveConcurrently` 在竞态检测下并发 `Observe` 和 `MeasuredFor` |

## 判定的输入从哪来

- **作用范围和原因**：适配器的分类（[suppliers 的「调用面」](../suppliers/README.md#调用面)）。只有端点范围的能计；suppliers 把原因的类型都以别名公开（`suppliers.UpstreamError` 等），本模块按它们穷尽地判（`gochecksumtype`），加一个原因就要在这里决定它算不算。
- **流内错误的类别**：`protocol.FaultOf`。三种格式的出错事件按 `error.type`、`error.code`、`code`、`response.error.code` 归类，工具调用参数不是 JSON 是单独的一类；其余读失败（断流、格式错）是 `FaultBroken`。类别只给本模块判健康用，提交前换不换端点不看它（那是 #311 E2-4）。
- **超时**：网关的 `cutOf` 从尝试的 context 原因读出：`errHeaderWait` → `HeaderWait`，`errIdle` → `Idle`，其余（调用最长时间、退出、追加没批准、客户端离开）→ `Ours`。

## 决策与理由

| 决策 | 理由 |
|---|---|
| 健康度、质量分、置信度、延迟、吞吐在快照重建时按值拷进 `routing.Endpoint`，不是指针 | 端点评估的设计让快照持有指向实时状态的指针，是为了熔断在下一次 `Plan` 就生效（H2）；健康度和质量分只影响权重、粘性和质量优先排序，晚一次重建（≤ 5 秒）无害，按值拷贝不用改 routing 的类型和全部测试。H2 仅给实时熔断和慢启动加指针，测量仍按值复制 |
| 平均值的第一个样本直接作为平均值，错误率从 0 起 | 与 sub2api 相同（`openai_account_scheduler.go` 的 `report`：首字时间以 NaN 起、第一个样本直接写入；错误率以 0 起）：没有样本时健康度是 1，第一个延迟样本不被一个任意的初值拉偏 |
| 质量分由 EWMA 合成，未知是中性 1 加置信度 0，不是 0 分 | 排序现在就需要一个来自实测而不是自报的质量值；可靠性、首字延迟、输出速度已经覆盖"快不快、稳不稳"。未知记 0 会让没测过的端点永久垫底（和"最快"同一个问题），记 1 又会越过有证据的端点，所以用中性值加一个单独衰减的置信度，由路由分组处理。绝对基准（1 秒、50 token/s）而不是按同一模型互相归一，是因为 `MeasuredFor` 只看到单个端点，跨端点聚合会依赖模型规模和目录里多数模型都没有的输出速度规格 |
| 试探结果计入质量分和健康度，但不进熔断窗口 | 试探是一次真实生成，理应影响"这个端点现在多好"；但它由我们自己发出，不满足摘除要求的独立来源，所以只计分和恢复，不参与熔断判定 |
| 真伪检测用随机指纹题，结果按节点保存 | 官方基线（固定题库和每版本参考响应）证据更强，但题库、采样和版本管理要一整套后台数据，短期交付不了；随机题复用试探已有的生成通路、预算、退避和受控时钟。不落库是因为调用路径不查库：落库要新迁移、写器和跨节点采纳，属 H3/H5；重启后未知、`:exacto` 失败关闭，宁可少接也不在没证据时放流量 |
| 窗口中的来源与凭证用固定大小 UUID，不另作哈希 | 每项固定大小、不分配，避免截取 UUIDv7 时间前缀导致独立来源碰撞；标识只在本节点窗口，不写事件或指标标签 |
| 阈值（4k、32k、50 token、5 个样本、30 分钟、α、1 秒、50 token/s、10 个结局、4 小时）是代码里的常量 | 设计说它们是后台配置、默认值在代码里；后台在 H3 才有，现在只有默认值；全局模式额外由启动变量控制 |
| 吞吐只取正常结束、且判为成功的尝试 | 设计规定"正常结束"；断流的尝试的输出 token 是估算，结束时刻也不是生成结束的时刻 |
| 判定按"客户端已走 → 调用自己的原因 → 适配器的失败 → 正常结束 → 空闲 → 流内类别"的顺序 | 前两项能解释它们之后的任何失败（取消引起的断流、我们的期限），先看它们才不会把取消算成端点的错；但客户端离开前已交付首段内容（`Delivered`），或之后读到上游正常结束且有内容，是成功：端点在正常回答。只在客户端离开后才来的内容不算交付，之后断流是"客户端已走时的失败"，不计 |
