# quality

端点评估模块。行为、阈值与决定见[端点评估](../../docs/domains/quality.md)，本文只写公开接口、调用约束与不变量。

本模块的数据库结构与来源类别种子由 [`00049_baseline.sql`](migrations/00049_baseline.sql) 初始化；编号、等价验证与开发库重建规则见[平台迁移说明](../platform/README.md#迁移)。

## 接口

```go
func New(meter metric.Meter) *Monitor // meter 为 nil 时不报指标
func (m *Monitor) Observe(ctx context.Context, o Observation) // 每次尝试结束时一次；0 分配
func (m *Monitor) MeasuredFor(ep suppliers.Routable, now time.Time) Measures // 端点当前的数（含来源类别），并核对检测证据属于这个配置
func (m *Monitor) SetSourceClasses(classes []SourceClass) error // 编译并换入类别规则；用已保存的证据重判所有端点
func (m *Monitor) SourceClassNames() []ClassName // 当前类别的 slug 和中英文名，按识别顺序、回退在最后；路由预设的页面用它
func (m *Monitor) Measure(ep suppliers.Routable, format protocol.Format, facts Facts) bool // 记下该格式的实测；旧配置或未提供的格式拒绝
func (m *Monitor) RecordOfficialBenchmark(ctx context.Context, ep suppliers.Routable, run BenchmarkRun) bool // 记下官方直连的基线并落库；只收平台端点且覆盖达标的运行
func (m *Monitor) RecordBenchmark(ctx context.Context, ep suppliers.Routable, run BenchmarkRun) (BenchmarkResult, bool) // 按官方基线给端点打分并落库；旧配置、旧基线、覆盖不足拒绝
func ParseMode(value string) (Mode, error) // 默认值与合法性只在此定义
func Start(ctx context.Context, cfg Config) (*Monitor, *Loop, error)
func (m *Monitor) Prepare(ctx context.Context, endpoints []suppliers.Routable) error // 发布当前配置和快照 Generation；旧 Generation 跳过
func (m *Monitor) Live(id suppliers.EndpointID, credentials int) *State
func (s *State) Routing(now time.Time) (opened time.Time, limit int64)
func (m *Monitor) ResetHealth(ctx context.Context, tx pg.Tx, id suppliers.EndpointID, at time.Time) (HealthReset, error) // 在调用方的事务里重置熔断
func (m *Monitor) AdoptReset(r HealthReset) // 事务提交后本节点换上重置后的状态
func (l *Loop) Tick(ctx context.Context) error
func (l *Loop) Run(ctx context.Context)
func (l *Loop) Drain(ctx context.Context) error
```

### 观测与路由调用约束

- gateway 在每次尝试结束时恰好调用一次 `Observe`。`Observation` 提交事实：端点、账户所有者、凭证、请求 ID、适配器失败、流内 `Fault`、取消原因 `Cut`、客户端离开与已交付标志、结束/空正文标志、输入估算、输出 token、工具检查计数、发出/首段/结束时刻、响应头、上游报告的用量项及 `EndpointConfig`；判定留在本模块。
- `Cut` 区分 `HeaderWait`、`Idle` 和 `Ours`；网关从尝试 context 原因转换，不能把自己的期限或退出当成端点失败。`FirstToken` / `Ended` 在读取上游时记录，不含后续追加预留、关闭和算成本的等待。
- `Source` 必须是账户所有者 UUID；没有所有者时仍可更新测量，但不进入熔断窗口。`Delivered` 表示首段内容交付时客户端还在；客户端离开后才出现的内容不能设置它。
- 响应头和 `UsageItems` 必须来自同一次响应；多跳服务端搜索取第一跳，估算补上的用量不算报告证据。`Config` 是尝试发出时的模型、上游名和格式集。
- `RouteDeps.Build` 和尝试使用同一个 `Monitor`。构建快照前调用 `Prepare`，再取 `MeasuredFor`；测量按值进快照，实时熔断和慢启动取 `Live` 返回的 `State`。`State.Routing` 给熔断时刻和临时并发上限；`State.Offline(time.Time) bool` 给离线状态。路由不调用写入接口。
- `Measures` 中运营测量、产品成绩、真伪、来源和逐格式 `Facts` 是独立信号，字段的零值、有效期及算法见[测量规则](../../docs/domains/quality.md#质量分吞吐与延迟)、[真伪检测](../../docs/domains/quality.md#真伪检测)和[来源指纹](../../docs/domains/quality.md#来源类别与指纹)。`VerifiedAt` 在过期或矛盾后保留最近通过时刻，配置变化时清零。
- `Facts` 的 `Supported` / `Unsupported` 与参数的对应字段分别表达确认的支持和不支持；零字段不改旧值，正的 `Context` / `MaxOutput` 替换旧值。同一批里同一位同时声明支持与不支持时，支持优先；后续测量可以翻转旧结论。`Measure` 拒绝旧配置或端点未提供的格式。

### 基准记录与后台数据接口

- `BenchmarkRun` 提交题库版本、运行时刻、格式和逐题回答；每条 `BenchmarkAnswer` 带题号、完成标记与回答。调用者负责确认官方运行来自目录作者的官方直连，模块只强制平台端点、生成格式、配置及覆盖条件。评分返回的 `BenchmarkResult.Points` / `Possible` 按题分计数，不按 check 数计数。
- `RecordOfficialBenchmark` / `RecordBenchmark` 是同步持久化入口，失败不发布内存。它们不发请求；格式、基线、覆盖和配置判定见[产品成绩](../../docs/domains/quality.md#产品成绩固定测评题)。
- `SourceClass`、`Rules`、`Condition` 定义类别和指纹；`Check` 校验边界，`Rules.Normalized` 规范头名。`ReadSourceClasses` / `LoadSourceClasses` 读取，`CreateSourceClass` / `UpdateSourceClass` / `DeleteSourceClass` 在调用方的 `pg.Tx` 内更新并审计，参数带 `audit.Actor` 和理由；更新与停用带 revision。HTTP 权限和幂等检查归组装层。
- `SetSourceClasses` 编译成功后整体换入规则并重判已有证据；编译失败保留旧规则。`SourceClassNames` 只给 slug 与中英文名，不含指纹规则；控制台从内存读取。`New` 初始只装回退类别，数据库迁移提供三个种子类别。
- `ProbeSettings` 的零字段表示使用代码默认值；`DefaultProbeSettings` 返回默认，`Effective` 合成有效值，`Check` 返回带字段与原因的 `SettingError`。`ReadProbeSettings` 读取记录，`SetProbeSettings` 在调用方事务内校验、替换并审计；全零记录删除。参数范围见[调度与预算](../../docs/domains/quality.md#调度与预算)。

### 生命周期与重置

- 生产在监听前调用 `Start`，加载健康行、设置、类别、官方基线和成绩；任何必需读取或设置校验失败都使启动失败。`New` 只建内存监视器，并解析校验嵌入题库。
- `Config` 提供数据库与范围、模式、日志、指标、告警和稳定节点名 `Node`；`Source` 实现 `ProbeSource`（`Routable`、`TestEndpoint`），`Pauses` 实现 `PausedUntil(endpoint, supplier uuid.UUID) time.Time`。后者由 routing 经组装层注入，本模块不引用 routing。
- `Config.Identity` 从目录提供 `Claimed` 的作者显示名、其他作者名、目录 slug 与厂商原生有效名；为 nil 时不发真伪检测。`Now` / `Rand` 可注入受控时钟和随机源，默认系统时钟与并发安全随机源。
- `Run` 驱动 `Tick`；`Tick` 与 `Drain` 必须单调用者，生成 worker 只通过结果通道返回。退出用 `Drain` 等已付费生成完成、成本写入后刷新剩余变化；试探不占调用的结算预留连接。
- 管理启用事务先写供应商开关，再调用 `ResetHealth`；仅提交成功后调用 `AdoptReset`。重置不改人工开关或成本状态。`AdoptReset` 以已从库读取或写成的 `stored` epoch 判断是否采纳，不以含未落库变化的内存 epoch 判断；已采纳重置或其后状态时不能覆盖新的熔断。

## 不变量

阈值和行为只在领域文档维护；下表给出机制与关键回归入口。

| 不变量 | 机制与验证 |
|---|---|
| 健康判定唯一，不计结局不改变健康度 | `judge`；`TestJudge`、`TestUncountedChangesNothing` |
| 每次尝试只观测一次，上游时长不含网关等待 | `TestEveryAttemptIsObservedOnce`、`TestThroughputLeavesOutOurOwnWaits` |
| 只写本模块表，自动恢复不覆盖人工停用 | `TestQualitySQLDoesNotWriteSuppliers`、`TestGatewayDisabledEndpointIsNeverProbed` |
| 熔断须同时满足窗口、硬失败和独立来源 | 固定大小 UUID 窗口；`TestIndependentFailures`、`TestWindowAndLateSuccess` |
| 降权保留最后备选，离线拒绝流量，不可试探端点不会自动离线 | `TestTrippedEndpointsAreLastResort`、`TestOfflineEndpointsTakeNoTraffic`、`TestUnprobeableEndpointsStayDegraded` |
| 旧请求、旧租约和失败的条件更新不能恢复新一轮熔断 | `TestNormalSuccessDoesNotRestoreTheBreaker`、`TestLateSuccessDoesNotRestoreUnprobeable`、`TestExpiredLeaseCannotRestoreNewProbeRound`、`TestLosingEpochDiscardsDependentTransitions` |
| 试探有界，暂停与资金拒绝不消费预算 | `TestProbeConcurrencyCap`、`TestProbeDeferrals`、`TestFundsDeferralDoesNotConsumeProbeBudget` |
| 只有获胜的状态更新写事件，状态与信号同事务 | `TestPersistLeaseRestartAndRecovery`、`TestCommittedSignalSurvivesRestart`；恢复、预算与模型告警分别见 `TestRecoveryClosesIncidentSignals`、`TestProbeBudgetSignalsFollowCapacityAndNodeIdentity`、`TestModelUnavailableOnlyCountsPlatformEndpoints` |
| 管理启用与重置同成同败，迟到的重置不覆盖之后的变化 | `TestResetHealthClosesTheBreaker`、`TestResetHealthVoidsATripNotYetStored`、`TestResetHealthKeepsATripAfterIt`、`TestEnablingAnEndpointResetsItsBreaker` |
| 真伪未知、矛盾和假冒分开，证据只属于检测过的配置 | `TestVerificationPassesAndFailsClosed`、`TestInconclusiveVerificationKeepsThePass`、`TestVerificationDropsACheckOfAnotherClaim` |
| 结构事实按配置和格式绑定，旧 Generation 不能翻回当前配置 | 端点锁内核对；`TestMeasureByFormat`、`TestMeasureStaleConfiguration`、`TestPrepareRejectsOlderGeneration` |
| 运营质量的未知不等于差，试探不进入独立来源窗口 | `TestQualityWithoutCountedOutcomesIsNeutral`、`TestProbeResultsScoreTheEndpoint` |
| 成绩只用当前题库、基线、配置与持久代次；写失败不发布 | `TestBenchmarkConfigurationChangeDoesNotResurrect`、`TestBenchmarkPersistsAcrossRestartAndNodes`、`TestBenchmarkStoreFailureLeavesMemoryUnchanged` |
| 调用路径不做 I/O，预建端点上的观测不分配；共享状态受锁保护 | 端点表用 `RWMutex`，每个端点独立锁；`BenchmarkObserve` 断言分配数，`TestObserveConcurrently` 覆盖并发访问 |
