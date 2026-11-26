# suppliers

本模块的公开接口和不变量；行为、规则和理由见[供应商](../../docs/domains/suppliers.md)，供应商成本见[计费](../../docs/domains/billing.md#供应商成本)，一次调用的编排见 [gateway](../gateway/README.md)。

结构自 [`00049_baseline.sql`](internal/store/migrations/00049_baseline.sql) 起；编号与重建规则见[平台迁移说明](../platform/README.md#迁移)。

## 代码结构

```
internal/suppliers/            公开接口：Service（调用面）、RecordAttempts 等写入、Admin（管理接口）、BYOK、
                               CheckAdapters、RegisterSync、RegisterReconcile、PriceCosts
internal/suppliers/internal/
  adapter/      契约：Adapter、Manager、CallQuirks、Failure、CallPolicy、Client、BucketSpec，可选 Onboarding、KeyMaker、KeyFinder、Pool、UsageQueue、Direct、Wire、Cloud、Signer
  registry/     唯一同时引用所有适配器的文件，一个内置适配器一行，另加声明式目录
  newapi/ sub2api/ cpa/   中转站和号池的内置适配器（testdata/ 是脱敏样本）
  openai/ anthropic/ google/ moonshot/ compatible/   直连接口，共用 direct/
  vertex/ bedrock/ azure/   云平台，共用 cloud/
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

schema `suppliers`，版本表 `platform.goose_suppliers`，迁移只增不改。

| 表 | 键与约束 |
|---|---|
| `suppliers` | `UNIQUE (origin, upstream_account)`；`mode`（relay、direct、cloud）和 `api_format` 创建后不变；`cloud_account` 只有云平台有；归属列与 `owner_scope` 由 CHECK 保持一致；余额、倍率、阈值是有限 `numeric`；`status_reason` 至多 200 字符 |
| `supplier_logins` | PK `supplier_id`；信封加密的登录，`version` 供比较后再写 |
| `supplier_groups` | `UNIQUE (supplier_id, upstream_ref)`；代号全局唯一、设定后不变；`key_error` 与 `key_retry_at` 同空同有；`pool_*` 号池读数 |
| `credentials` | `UNIQUE (supplier_id, upstream_key_ref)`；`erased` 不能还有密文；RPM 1 到 100,000 或空；`id` 由调用方生成（加密位置要先知道行 ID）；检测结果同空同有；`UNIQUE (supplier_id, region)`，`call_names` 是这条凭证（云平台区域或直连 key）调用端点时用的模型名 |
| `intake_rules`、`inbox_items` | PK `supplier_id`；`UNIQUE (supplier_id, kind, subject)` |
| `supplier_models` | PK `(supplier_group_id, upstream_name)`；`model_id` 空即待映射 |
| `supplier_aliases`、`supplier_alias_revisions` | 首版不可变，`UNIQUE NULLS NOT DISTINCT (supplier_id, supplier_group_id, upstream_name)`；修订 PK `(alias_id, revision)`，只追加 |
| `endpoints` | `UNIQUE (supplier_group_id, model_id)`；成本已知必有成本计划（`known_has_schedule`） |
| `cost_schedules` | 只追加，`UNIQUE (endpoint_id, fingerprint)` |
| `attempt_costs` | PK `(request_id, attempt_seq)`，`attempt_seq` 1 到 33；`charge_record_id` 唯一；`test` 标记检测行；索引 `(endpoint_id, finished_at)` |
| `ended_requests`、`lost_requests`、`lost_request_resolutions` | 只插入；带请求所属租户 |
| `charge_records`、`charge_events` | `UNIQUE (supplier_id, charge_ref)`；事件 `UNIQUE (charge_record_id, revision)` |
| `sync_states`、`sync_events`、`sync_plans` | 状态 jsonb 加格式号、清单位置 `listing`、栅栏、租约；事件和计划只插入 |
| `unit_values` | 只插入；`value` 在 (0, 1000]、至多 15 位小数；币种与原生单位都匹配的最新一行生效 |
| `rate_buckets` | PK `(scope, name)` |
| `usage_records` | `UNIQUE (supplier_id, request_id)`；`new`/`rejected` |
| `attempt_journal` | 旧对账日志，无人读写，待删 |

- **行级安全**：`suppliers` 按本行归属与范围比较，平台供应商只有平台范围可见；子表策略是"所属供应商对本事务可见"，插入也挡住挂到看不见的供应商下。同步、对账、限速和采集的表是平台范围。
- **复合外键**：同时存 `supplier_id` 与 `supplier_group_id` 的表外键指向 `supplier_groups (id, supplier_id)`；`model_id` 是目录 ID，没有外键。
- **授权**：应用角色只对 `supplier_logins`（移除时）、`usage_records` 和 `inbox_items`（情况结束）有 DELETE；UPDATE 按列授予，不含 `id`、`supplier_id`、归属、`kind`、`origin`、`upstream_account`、`upstream_key_ref`。后台任务角色还没有，应用角色暂能改 `attempt_costs` 与 `charge_records` 的对账列。
- **密钥**：没有明文列；数据库只检查信封四列齐全和长度，加密由写入边界只收 `secret.Sealed` 保证。触发器 `fence_envelope_key_version` 拒绝已退役的主密钥版本（见 [platform 的「凭证加密」](../platform/README.md#凭证加密)）。`serve` 缺 `AXIOM_MASTER_KEY_FILE` 不启动。

## 调用面

`Service` 是一个节点上的调用面：快照、凭证运行状态、成本计划。

```go
func New(cfg Config) (*Service, error)                       // DB、平台范围、主密钥、出口、注册表、时钟、Meter、Nodes
func (s *Service) Reload(ctx context.Context) error           // 每 5 秒；只有读不了库才失败，读不出的供应商整家排除
func (s *Service) Routable() ([]Routable, error)               // 快照里的端点，按 id；超过 MaxStale（30 秒）返回 ErrStale
func (s *Service) EndpointCode(ep EndpointID) string           // 端点的公开代号；缺失或过期为空串
func (s *Service) RefreshBalances(ctx context.Context) error   // 后台：合并、限速请求余额同步
func (s *Service) Acquire(ep EndpointID) (*Attempt, error)     // ErrNoEndpoint、ErrSupplierResting、ErrNoSlot
func (a *Attempt) Send(ctx context.Context, c Call) (*http.Response, *Failure) // Call{Format, Compact, Body, Header}
func (a *Attempt) Sanitize(protocol.Rejection) *protocol.Rejection // 流里的拒绝，同一份清洗；nil 为没有可交出的话
func (a *Attempt) Policy() CallPolicy                          // 适配器默认，端点实测覆盖 Cancels
func (a *Attempt) Done() AttemptReceipt                        // 幂等，释放占位；回执字段不对外
func (s *Service) SetMeasuredCancels(ep EndpointID, cancels bool)
func (s *Service) CredentialLoad(credential, supplier uuid.UUID) Load // 本节点的在途数、账号这一分钟的请求数
func (s *Service) Cost(r AttemptReceipt, u []catalog.Usage) (AttemptCost, error)
func (s *Service) Credits(r AttemptReceipt, u []catalog.Usage, c platform.Currency) (AttemptCost, int64, bool, error)
func (s *Service) TestEndpoint(ctx context.Context, r TestRequest) (TestResult, error)
func RecordTest(ctx context.Context, tx pg.Tx, who audit.Actor, reason string, r TestResult) error
func RecordAttempts(ctx context.Context, tx pg.Tx, rows []AttemptRecord) error
func RecordLostRequest / ResolveLostRequest / EndRequest / EndRequests(ctx, tx, ...) error
type CallWrites struct{}                                       // 上面的写入按 billing.Suppliers 的形状转发
func Reconcile / AssignToLostRequest / AssignUnattributed(ctx, tx, ...)
func RegisterReconcile(ReconcileJobConfig) error
func PriceCosts(ctx context.Context, tx pg.Tx) ([]PriceCost, error) // 从库投影可调用端点的成本，不解密、不依赖本节点注册表
func EndpointLines(ctx context.Context, tx pg.Tx, endpoints []uuid.UUID) (map[uuid.UUID]Line, error) // 端点所属的供应商和分组
```

`Failure.Cause` 的 13 种原因以同名类型别名导出；`Failure.Executed` 只需与 `NotExecuted` 比较。

- **`Routable`** 只含能调的端点，与 `ListRoutable` 是同一批行：`Codename` 是分组的公开代号（不暴露名称、地址或内部 ID），`Generation` 是快照的单调版本，`Schedule` 已按分组倍率缩放，`Funds` 是原生余额与自动暂停（路由用 `billing.ReservedCost` 调 `Funds.CanPay`），`Credentials` 是未过期、未冷却的凭证数。模型规格由 routing 向目录取。
- **回执**带凭证、端点、成本计划版本、单位价值行 id 和对账键；gateway 用 `AttemptReceipt.WithExchange` 传回冻结的汇率。`Credits` = 原生金额 × 单位价值 × 汇率，精确相乘、转成十亿分之一时向上取整一次；缺汇率或溢出返回未知，原生 `CostFlag` 不变。`attempt_costs` 存换算依据 `Conversion`，只有 converted 和 overflow 写汇率 ID。
- **写入**：`RecordAttempts` 在 billing 的结算事务里用一条 `unnest` 写全部尝试，`EndRequests` 同样一条语句，重复都保留第一次。平台自己的尝试传零值租户，写成 NULL；失联请求必须有租户。
- **路径**：调用路径只来自 `attempt.go` 的 `wire` 表（`/v1/chat/completions`、`/v1/responses`、`/v1/messages`、`/v1/completions`、`/v1/embeddings`、`/v1/rerank`，只有 `/v1/messages` 加 Anthropic 版本头）和 `compactPaths`（`Call.Compact` 时 Responses 走 `/v1/responses/compact`，见[接入的「路径与格式」](../../docs/domains/gateway.md#路径与格式)）。云平台的请求由适配器按区域和模型名签出（`adapter.Signer`），签不出算凭证的失败，2xx 由它转成格式的帧。
- **`EnabledCurrencies`**：active、low_balance 供应商的结算币种，缺汇率时告警。
- **`TestRequest.AvoidCredential`** 在有另一把可用 key 时避开它；`TestResult.CredentialID` 只给 ID。

## 并发与速率

- **窗口**：每个上游账号（中转站是供应商，直连和云平台是凭证）固定 61 个一秒桶，按首次准入起的单调秒索引，每个桶带自己的秒标记、过期读作 0。`Acquire` 先比较交换占并发位，再在账号锁里读时钟、汇总 [now-60, now] 并加一；失败就还回并发位换一把 key。所以任意滚动 60 秒不超过所选凭证的上限，时钟回拨不重开旧桶，成功计入的请求不退，一秒的计数饱和在 100,000。
- **容量视图**：`Capacity` 用与 `Acquire` 相同的 `leastBusy` 判断，只读；真正占位仍以 `Acquire` 为准，两个调用抢一个空位只有一个成功。
- **静态份额**：`Config.Nodes` 接入 `platform.nodes` 的存活成员，`SyncShares` 每秒重算。按节点 ID 排序，额度 C 分成 `C/N`，前 `C%N` 个各加一；有限 RPM 分得 0 即不可选。份额变化时先按新旧列表逐项取小，本节点在途和 RPM 用量降到目标才确认；新成员要等原有成员都确认包含它才有份额。交接比较带启动身份的完整成员列表。
- **失联与退出**：注册表按 30 秒判失联；准入期限是自己心跳剩余寿命减 5 秒、至多 25 秒，过期即停止准入、以加入者重新进入。离开节点的份额再保留 61 秒约束有限 RPM（`platform.credential_share_departures`）。
- **生效**：上限存在凭证行上，下一次 `Reload` 生效，降低上限不取消调用。

## 用真实生成测试端点

`TestEndpoint` 只测平台供应商的端点（否则 `ErrTestOwner`），走真实的 `Acquire`、`Send`、`Done`：一次流式生成，输出上限 16（真伪检测 48，留 4 KiB 回答），不开推理。格式必须是端点的一种生成格式（Messages、Responses、Chat、Completions）（否则 `ErrNoFormat`），管理接口只收前三种。

- **读到流结束**：调用方的取消被挡在外面，只留 45 秒上限。
- **判定**（`TestResult.Outcome()`）见[什么算一次失败](../../docs/domains/quality.md#什么算一次失败)：2xx 且读到正常结束事件才通过，未到结束就断是 `stream_broken`。
- **成本**：`RecordTest` 在调用方的事务里写成本行（`test` 标记，租户为空）和审计 `endpoint.test`；管理接口的幂等事务有 1 分钟上限，提交前失败全部回滚。估价是确定的上界：输入按字节数 + 16，输出按上限，取可落入档位里的最高价，不用上游报告的用量；未执行的估价为 0。成本计划缺一侧的价时 `ErrTestCost`（422），不付钱。

## 对账

`RegisterReconcile` 在队列启动前注册逐条对账和告警；同步经 `reconcile.Ingest` 在栅栏事务里写扣费记录。规则见[供应商的「对账」](../../docs/domains/suppliers.md#对账)。告警日志在提交前记，回滚会重复。

## 没有对账日志

请求进行中不记"哪些尝试到过供应商"。已结算的尝试由 `attempt_costs` 在结算事务里留下成本、对账键和端点，与账本同成同败（[计费的「结算与账本」](../billing/README.md#结算与账本)）。清理接管崩溃留下的预留时写 `lost_requests`，迟到的结算回来时写 `lost_request_resolutions`；对不上的扣费暂挂后由人归属（[计费的「持有者与截止时间」](../../docs/domains/billing.md#持有者与截止时间)）。

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
| 正常供应商一定有注册的适配器；删任一适配器其余照常编译 | `CheckAdapters`；`just adapters-removable` 对注册表的每一个 |
| 检测成本行不被对账判 | `reconcile.judge` 见 `test` 即返回 |
| 价目行就是收费：同一份计划、同一个 `inSettlement` | `TestEndpointCostIsWhatAnAttemptIsCharged` |
| 推导因子乘出向上取整即原生单价 | 写入前 `Derivation.Check` |
| 停用、擦除、被拒的凭证不再被挑 | `ListRoutable` 只取 `active`；应答节点提交后即 `Reload` |

## 管理接口

`Admin`（`admin*.go`）在调用方的平台范围事务里写，处理器只做转换；行为见[供应商的「管理操作」](../../docs/domains/suppliers.md#管理操作)。

| 操作 | 拒绝 |
|---|---|
| `NormalizeOrigin`、`CheckOrigin`（只在幂等回调里）、`Detect` | `ErrBadOrigin`、`ErrAddressRefused`、`ErrUnreachable` |
| `Connect` | `ErrNoAdapter`、`*LoginError`（只带字段名）、`ErrSupplierExists` |
| `PlanDirect`、`PlanDirectKeys`、`CheckDirect`、`CreateDirect`、`AddDirectKeys` | `ErrDirect`：`Provider`、`Origin`、`Format`、`Gemini`、`Currency`、`Name`，`*KeyError`、`ErrLimits`、`ErrAddressRefused` |
| `PlanCloud`、`CheckCloud`、`CreateCloud`、`PlanCloudReplace`、`ReplaceCloud`（云平台） | `ErrCloudProvider`、`ErrCloudOrigin`、`ErrCloudRegions`、`ErrCloudDeployments`、`*CredentialError`、`ErrNotCloud` |
| `SetStatus`、`Remove`、`ReplaceLogin`、`ConfirmUnitValue` | `ErrStatusLocked`、`ErrNoSupplier`、`ErrUnitValue`、`ErrUnitValueFixed` |
| `SelectGroup`、`BindKey` | `ErrNoGroup`、`ErrGroupGone`、`ErrGroupNotSelected`、`ErrKeyBound` |
| `SetLimits` | `ErrNoCredential`、`ErrLimits`、`ErrStatusLocked` |
| `MapModel`、`MapPending` | `ErrAliasExists`、`ErrAliasRevision`、`ErrNotPending`、`ErrModelTaken`、`ErrNoGroup`、`ErrStatusLocked` |
| `SetEndpointEnabled`、`SetEndpointQuantization` | `ErrNoSupplierEndpoint`、`ErrStatusLocked` |
| `Suppliers`、`Supplier`、`PendingMappings`、`TopUpBasis`、`SupplierModels`、`EndpointCost`、`EndpointTraffic`（只读） | `ErrNoSupplier`、`ErrNoSupplierEndpoint` |
| `MakeKey`、`FindKey`、`CheckKey`、`CheckCredential`（经 `Reach`，不在调用方事务里） | `ErrCannotMakeKey`、`ErrCannotFindKey`、`*KeyRefusal`、`ErrSupplierRefused`、`ErrSupplierUnanswered`、`ErrStatusLocked` |
| `ConnectGroup`、`AddKeys`、`RecordCheck`、`SetCredentialEnabled`、`RemoveCredential`、`RemoveInvalid` | `ErrWrongGroup`、`ErrGroupHasKey`、`ErrKeyBound`、`ErrStatusLocked` |
| `RequestSync`、`SetIntakeRule`、`IgnoreInboxItem` | `ErrStatusLocked`、`ErrNotSynced`、`ErrIntakeRule`、`ErrIntakeRevision`、`ErrNoInboxItem` |
