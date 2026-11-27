# suppliers

本模块的公开接口和不变量；行为、规则和理由见[供应商](../../docs/domains/suppliers.md)，供应商成本见[计费](../../docs/domains/billing.md#供应商成本)，一次调用的编排见 [gateway](../gateway/README.md)。

结构自 [`00049_baseline.sql`](internal/store/migrations/00049_baseline.sql) 起；编号与重建规则见[平台迁移说明](../platform/README.md#迁移)。

## 代码结构

```
internal/suppliers/  公开接口：Service（调用面）、RecordAttempts 等写入、Admin、BYOK、
  CheckAdapters、RegisterSync、RegisterReconcile、PriceCosts
internal/suppliers/internal/
  adapter/      契约：Adapter、Type、Manager、CallQuirks、Failure、CallPolicy、Client，及可选能力的接口（Onboarding、Direct、Cloud 等）
  registry/     唯一同时引用所有适配器的文件，一个内置适配器包一行
  newapi/ sub2api/ cpa/ 中转站和号池的内置适配器
  vendors/ anthropic/ google/ compatible/ 直连（vendors 是 OpenAI 格式的厂商表，google 的 Chat 走 Gemini 原生），共用 direct/
  vertex/ bedrock/ azure/ 云平台，共用 cloud/
  declared/     声明式适配器
  client/       适配器唯一的出站入口：egress 加限速桶（进程内 Limiter、数据库 SharedLimiter）
  collector/    CPA 用量队列的常驻采集器
  adaptertest/  每个适配器都要过的契约测试
  syncer/       同步引擎：只做决定
  persist/      租约与栅栏、状态与登录的存取、投影
  schedule/     River 扫描与任务
  reconcile/    对账
  store/        迁移与 sqlc 查询
```

## 表

每张表的键与约束、行级安全、复合外键、授权和密钥列见 [store](internal/store/README.md)。


## 调用面

`Service` 是一个节点上的调用面：快照、凭证运行状态、成本计划。

```go
func New(cfg Config) (*Service, error) // DB、平台范围、主密钥、出口、注册表、时钟、Meter、Nodes
func (s *Service) Reload(ctx context.Context) error // 每 5 秒；只有读不了库才失败，读不出的供应商整家排除
func (s *Service) Routable() ([]Routable, error) // 快照里的端点，按 id；超过 MaxStale（30 秒）返回 ErrStale
func (s *Service) EndpointCode(ep EndpointID) string // 端点的公开代号；缺失或过期为空串
func (s *Service) RefreshBalances(ctx context.Context) error // 后台：合并、限速请求余额同步
func (s *Service) Acquire(ep EndpointID) (*Attempt, error) // ErrNoEndpoint、ErrSupplierResting、ErrNoSlot
func (s *Service) AcquireWithin(ep EndpointID, bound platform.Exact) (*Attempt, error) // 跳过付不起 bound÷倍率者
func (a *Attempt) Send(ctx context.Context, c Call) (*http.Response, *Failure) // Call{Format, Compact, Count, Body, Header}
func (a *Attempt) Sanitize(protocol.Rejection) *protocol.Rejection // 流里的拒绝，同一份清洗；nil 为没有可交出的话
func (a *Attempt) Policy() CallPolicy // 适配器默认，端点实测覆盖 Cancels
func (a *Attempt) Done() AttemptReceipt // 幂等，释放占位；回执字段不对外
func (s *Service) SetMeasuredCancels(ep EndpointID, cancels bool)
func (s *Service) CredentialLoad(credential, supplier uuid.UUID) Load // 本节点的在途数、账号这一分钟的请求数
func (s *Service) Cost(r AttemptReceipt, u []catalog.Usage) (AttemptCost, error)
func (s *Service) Credits(r AttemptReceipt, u []catalog.Usage, c platform.Currency) (AttemptCost, int64, bool, error)
func (s *Service) TestEndpoint(ctx context.Context, r TestRequest) (TestResult, error)
func RecordTest(ctx context.Context, tx pg.Tx, who audit.Actor, reason string, r TestResult) error
func RecordAttempts(ctx context.Context, tx pg.Tx, rows []AttemptRecord) error
func RecordLostRequest / ResolveLostRequest / EndRequest / EndRequests(ctx, tx, ...) error
type CallWrites struct{} // 上面的写入按 billing.Suppliers 的形状转发
func Reconcile / AssignToLostRequest / AssignUnattributed(ctx, tx, ...)
func RegisterReconcile(ReconcileJobConfig) error
func PriceCosts(ctx context.Context, tx pg.Tx) ([]PriceCost, error) // 从库投影可调用端点的成本，不解密、不依赖本节点注册表
func EndpointLines(ctx context.Context, tx pg.Tx, endpoints []uuid.UUID) (map[uuid.UUID]Line, error) // 端点所属的供应商和分组
```

`Failure.Cause` 的 13 种原因以同名类型别名导出；`Failure.Executed` 只需与 `NotExecuted` 比较。

- **`Routable`** 只含能调的端点，与 `ListRoutable` 是同一批行：`Codename` 是分组的公开代号，`Generation` 是快照的单调版本，`Schedule` 已按分组倍率缩放，`Funds` 是原生余额（钱包 key 取最富的）与自动暂停（路由用 `billing.ReservedCost` 调 `Funds.CanPay`），`Credentials` 是未过期、未冷却的凭证数。规格由 routing 向目录取。
- **回执**带凭证、端点、成本计划版本、单位价值行 id 和对账键；gateway 用 `AttemptReceipt.WithExchange` 传回冻结的汇率。`Credits` = 原生金额 × 单位价值 × 汇率，精确相乘、转成十亿分之一时向上取整一次；缺汇率或溢出返回未知，原生 `CostFlag` 不变。`attempt_costs` 存换算依据 `Conversion`，只有 converted 和 overflow 写汇率 ID。
- **写入**：`RecordAttempts` 在 billing 的结算事务里用一条 `unnest` 写全部尝试，`EndRequests` 同样一条语句，重复都保留第一次。平台自己的尝试传零值租户，写成 NULL；失联请求必须有租户。
- **路径**：`attempt.go` 的 `wire` 表（各格式的 `/v1/…`，Messages 加版本头）、`compactPaths`（[路径与格式](../../docs/domains/gateway.md#路径与格式)）和 `countPaths`（count_tokens，失败不冷却凭证，云平台不发）；直连经 `adapter.Wire` 换前缀、带凭证的设置和名字。云平台由 `adapter.Signer` 签出，签不出算凭证的失败，2xx 转成格式的帧。
- **出口**：调用、检测、重读走 `exits.client`；代理不可用的凭证跳过、建不出的整家排除，不回直连；经代理传输失败是 `ScopeEgress`。
- **`EnabledCurrencies`**：active、low_balance 供应商的结算币种，缺汇率时告警。
- **`TestRequest.AvoidCredential`** 在有另一把可用 key 时避开它；`TestResult.CredentialID` 只给 ID。

## 并发与速率

- **窗口**：每个上游账号（中转站是供应商，直连和云平台是凭证）固定 61 个一秒桶，按首次准入起的单调秒索引，每个桶带自己的秒标记、过期读作 0。`Acquire` 先比较交换占并发位，再在账号锁里读时钟、汇总 [now-60, now] 并加一；失败就还回并发位换一把 key。所以任意滚动 60 秒不超过所选凭证的上限，时钟回拨不重开旧桶，成功计入的请求不退，一秒的计数饱和在 100,000。
- **静态份额**：`Config.Nodes` 接入 `platform.nodes` 的存活成员，`SyncShares` 每秒重算。按节点 ID 排序，额度 C 分成 `C/N`，前 `C%N` 个各加一；有限 RPM 分得 0 即不可选。份额变化时先按新旧列表逐项取小，本节点在途和 RPM 用量降到目标才确认；新成员要等原有成员都确认包含它才有份额。交接比较带启动身份的完整成员列表。
- **失联与退出**：注册表按 30 秒判失联；准入期限是自己心跳剩余寿命减 5 秒、至多 25 秒，过期即停止准入、以加入者重新进入。离开节点的份额再保留 61 秒约束有限 RPM（`platform.credential_share_departures`）。

## 用真实生成测试端点

`TestEndpoint` 只测平台供应商的端点（否则 `ErrTestOwner`），走真实的 `Acquire`、`Send`、`Done`：一次流式生成，输出上限 16（真伪检测 48，留 4 KiB 回答），不开推理。格式必须是端点的生成格式之一（否则 `ErrNoFormat`），管理接口不收 Completions。

- **读到流结束**：调用方的取消被挡在外面，只留 45 秒上限。
- **判定**（`TestResult.Outcome()`）见[什么算一次失败](../../docs/domains/quality.md#什么算一次失败)：2xx 且读到正常结束事件才通过，未到结束就断是 `stream_broken`。
- **成本**：`RecordTest` 在调用方的事务里写成本行（`test` 标记，租户为空）和审计 `endpoint.test`；估价是确定的上界：输入按字节数 + 16，输出按上限，取可落入档位里的最高价，不用上游报告的用量；未执行的估价为 0。成本计划缺一侧的价时 `ErrTestCost`（422），不付钱。

## 对账

`RegisterReconcile` 在队列启动前注册逐条对账和告警；同步经 `reconcile.Ingest` 在栅栏事务里写扣费记录。规则见[供应商的「对账」](../../docs/domains/suppliers.md#对账)。告警日志在提交前记，回滚会重复。

## 没有对账日志

请求进行中不记哪些尝试到过供应商。已结算的尝试由 `attempt_costs` 在结算事务里留下成本、对账键和端点，与账本同成同败（[计费的「结算与账本」](../billing/README.md#结算与账本)）。清理接管崩溃留下的预留时写 `lost_requests`，迟到的结算回来时写 `lost_request_resolutions`；对不上的扣费暂挂后由人归属（[计费的「持有者与截止时间」](../../docs/domains/billing.md#持有者与截止时间)）。

## 不变量

| 不变量 | 靠什么保证 |
|---|---|
| 密钥明文不落库，日志和错误里没有对方的文字 | 没有明文列；`Failure` 的原因只带数字和时间；契约测试让响应回显凭证并搜索每个错误 |
| 5xx 不会被当成"未执行" | `Classify` 先排除 5xx；`adaptertest` |
| 供应商只在自己的范围内可见、可写；子表、分组、凭证、别名、端点属于同一供应商 | 策略和复合外键；测试逐范围、逐表 |
| 身份和凭证发往的地址不能改 | 按列授权；`client.New` 只接受裸 origin |
| 没有余额（`mode` 是 relay 时）、没确认单位价值、没有成本计划或停用的端点不进路由 | `ListRoutable`；写入侧拿到计划才置 `known` |
| 人停用的端点不被同步重开 | 投影的 UPDATE 不含 `enabled` |
| 一家读不出不拖垮其余；`Reload` 持续失败时不无限期沿用旧端点 | 按供应商排除；`MaxStale` |
| 一个凭证不超过并发位和滚动 60 秒上限 | `Acquire` 的比较交换和账号锁内的读钟计数 |
| 一个供应商同一时刻最多一个同步任务 | 任务按参数唯一；崩溃的任务 15 分钟后被放弃 |
| 同一时刻只有一个节点写一个供应商的同步状态 | 事务开头锁租约行（`lease_fence`）再锁供应商行（`config_gen`），不符即 `ErrFenced`；锁序租约行 → 供应商行 → 登录 |
| 失去租约或凭证已换的节点不覆盖登录令牌 | `LoginStore.Swap` 先过同样的栅栏，再比较版本，否则 `ErrStaleLogin` |
| 一步的状态、事件、计划、扣费记录和投影同成同败 | 一个事务；配置代次变了整步作废，包括"需处理" |
| 同一份清单同步两次，五张投影表一行不写 | 投影先读同一事务里的行、只写差异；每页至多 1000 行 |
| 清单位置不丢页、不含凭证 | 位置存在 `sync_states.listing`，读不懂或配置代次不对就丢掉重读 |
| 扣费水位只升不降，每条记录至少读到一次 | 每页与位置同一次提交；重叠回扫和每日补扫 |
| 多节点合计不超过对方的限速 | `SharedLimiter` 一个事务内按键序占全部桶，全取或全不取；429 封桶对所有节点生效 |
| 退出信号不取消在跑的同步 | `pg.Queue.Start` 与启动 context 脱钩，只有 `Stop` 的期限取消 |
| 移除后库里没有密文，做过的尝试仍能结算 | `Admin.Remove` 一个事务；`erased` 的 CHECK |
| 正常供应商一定有注册的适配器；删任一适配器其余照常编译；描述不合法不启动 | `CheckAdapters`；`just adapters-removable` 逐行；`registry.New` 核对描述 |
| 平台直连和云线路成本 = 价 × 线路倍率，与倍率同成同败 | 定价与倍率同一事务、锁里读倍率 |
| 检测成本行不被对账判 | `reconcile.judge` 见 `test` 即返回 |
| 价目行即收费；确认价 = 基础价 × 倍率 | `TestEndpointCostIsWhatAnAttemptIsCharged`、`TestConfirmedBaseIsWhatACallIsCharged` |
| 推导因子乘出向上取整即原生单价 | 写入前 `Derivation.Check` |
| 停用、擦除、被拒的凭证不再被挑 | `ListRoutable` 只取 `active`；应答节点提交后即 `Reload` |

## 管理接口

`Admin`（`admin*.go`）在调用方的平台范围事务里写，处理器只做转换；行为见[供应商的「管理操作」](../../docs/domains/suppliers.md#管理操作)。

| 操作 | 拒绝 |
|---|---|
| `NormalizeOrigin`、`CheckOrigin`（只在幂等回调里）、`Detect` | `ErrBadOrigin`、`ErrAddressRefused`、`ErrUnreachable` |
| `Connect` | `ErrNoAdapter`、`*LoginError`（只带字段名）、`ErrSupplierExists` |
| `Types`（只读） | |
| `PlanDirect`、`PlanDirectKeys`、`CheckDirect`、`CreateDirect`、`AddDirectKeys` | `ErrNoType`、`ErrTypeOrigin`、`*FieldError`，`ErrDirect`：`Format`、`Gemini`、`Currency`、`Name`，`*KeyError`、`ErrLimits`、`ErrAddressRefused`、`ErrBalanceCurrency` |
| `PlanCloud`、`CheckCloud`、`CreateCloud`、`PlanCloudReplace`、`ReplaceCloud` | 前三种，`ErrCloudRegions`、`ErrCloudDeployments`、`ErrNotCloud` |
| `SetCostRatio` | `ErrCostRatio`、`ErrNotPasted`、`ErrStatusLocked` |
| `SetStatus`、`Remove`、`ReplaceLogin`、`ConfirmUnitValue` | `ErrStatusLocked`、`ErrNoSupplier`、`ErrUnitValue`、`ErrUnitValueFixed` |
| `SelectGroup`、`BindKey` | `ErrNoGroup`、`ErrGroupGone`、`ErrGroupNotSelected`、`ErrKeyBound` |
| `SetLimits` | `ErrNoCredential`、`ErrLimits`、`ErrStatusLocked` |
| `MapModel`、`MapPending` | `ErrAliasExists`、`ErrAliasRevision`、`ErrNotPending`、`ErrModelTaken`、`ErrNoGroup`、`ErrStatusLocked` |
| `SetEndpointEnabled`、`SetEndpointQuantization` | `ErrNoSupplierEndpoint`、`ErrStatusLocked` |
| `Suppliers`、`Supplier`、`PendingMappings`、`TopUpBasis`、`SupplierModels`、`EndpointCost`、`EndpointTraffic`（只读） | `ErrNoSupplier`、`ErrNoSupplierEndpoint` |
| `MakeKey`、`FindKey`、`CheckKey`、`CheckCredential`（经 `Reach`，不在调用方事务里） | `ErrCannotMakeKey`、`ErrCannotFindKey`、`*KeyRefusal`、`ErrSupplierRefused`、`ErrSupplierUnanswered`、`ErrStatusLocked` |
| `ConnectGroup`、`AddKeys`、`RecordCheck`、`SetCredentialEnabled`、`RemoveCredential`、`RemoveInvalid` | `ErrWrongGroup`、`ErrGroupHasKey`、`ErrKeyBound`、`ErrStatusLocked` |
| `*EgressProxy*`、`SetCredentialEgress` | `ErrEgress*`、`ErrNoEgressProxy`、`ErrUnknownEgress` |
| `RequestSync`、`SetIntakeRule`、`IgnoreInboxItem`、`*BasePrices` | `ErrStatusLocked`、`ErrNotSynced`、`ErrNoOfficialPrice`、`ErrBasePrice`、`ErrIntakeRule`、`ErrIntakeRevision`、`ErrNoInboxItem` |
