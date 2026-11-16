# catalog

模型、名字、规格、参考价和共用的价目表结构。行为见[模型目录](../../docs/domains/catalog.md)，这里只写表、不变量和接口。数据集核对与其审核项、后台任务角色 `axiom_catalog_job` 和 `catalog.lock_models`、`CurrentPrices` 还没有实现。

本模块的数据库结构由 [`00049_baseline.sql`](migrations/00049_baseline.sql) 创建；编号、等价验证与开发库重建规则见[平台迁移说明](../platform/README.md#迁移)。

| 包 | 做什么 |
|---|---|
| `catalog` | 价目表与计价、名字、快照、管理操作、数据集同步、页面监测 |
| `catalog/migrations` | schema `catalog` 的表、授权和行级安全，版本表 `platform.goose_catalog` |
| `catalog/queries` | sqlc 查询，生成到 `catalog` 包；生成的构造函数占了 `New`，所以目录的叫 `Open` |

## 数据

### 表

"只追加"指 `axiom_app` 没有 UPDATE、DELETE 权限；类别见[安全的「租户隔离」](../../docs/security.md#租户隔离)。

| 表 | 键与约束 | 类别 | 写法 |
|---|---|---|---|
| `author` | PK `slug` | 公开 | 增、改名 |
| `model`：`status`（draft / listed / retired）、`version_basis`（dated / floating）、`price_source`（litellm / openrouter / official / manual）、`spec` jsonb、`rev` | PK `id`（UUIDv7）；UNIQUE `slug`；`split_part(slug, '/', 1) = author` | 公开，草稿只有平台可见 | 只许改 `status`、`price_source`、`spec`、`rev` |
| `name`：`name`、`norm`、`visibility`、`model_id` | PK `name`；UNIQUE `norm`；public 不以 `-latest` 结尾、不含 `~@`；`:` 只能是结尾的 `:free`；`octet_length` 1 … 200 | 公开，只含 listed 模型的 | 只追加 |
| `alias`：`name`、`rev`、`model_id` | PK `(name, rev)`；`~…-latest` 格式 | 公开 | 只追加 |
| `reference_price`：`currency`、`source`、`schedule`（撤下时为空）、`schedule_sha256`、`supersedes` | UNIQUE `supersedes`；`(model_id, currency) WHERE supersedes IS NULL` 唯一；外键 `(supersedes, model_id, currency)` 只能替代同一时间线的行；`supersedes <> id`；币种与 `schedule` 一致 | 公开，只含 listed 模型的 | 只追加 |
| `revision` | 单行，影响快照的写入才加一 | 公开 | 改 |
| `change`：`op_key`、`actor`、`reason`、`kind`、`subject`、`before`、`after`、`evidence` | UNIQUE `op_key` | 平台 | 只追加 |
| `sync_state` | 单行；`(failing_since IS NULL) = (failures = 0)` | 平台 | 改 |
| `page_site` | PK `origin`；间隔 1 … 86400 秒 | 平台 | 改预约与间隔 |
| `watched_page` | 同 URL 最多一个启用；HTTPS；当前快照属于本页；`lease_gen` | 平台 | 改健康、租约、指针、启用 |
| `page_snapshot` | UNIQUE `(page_id, extractor_version, text_sha256)`；当前指针不能退到更低的抽取版本（触发器） | 平台 | 只追加 |
| `page_observation` | 内容变化时追加 | 平台 | 只追加 |
| `review`（只有 page_changed） | 每页最多一个 open 项；带 `content_seq` | 平台 | 改差异与状态 |

- 标准 ID 是 slug 去掉 `@版本`，存成一个 public 名字，格式 `^<厂商>/[a-z0-9][a-z0-9._-]{0,99}(:free)?$`；厂商部分 `^[a-z0-9][a-z0-9-]{0,39}$`。slug 本身不进 `name` 表。
- `name` 的 CHECK 只是兜底。改 `Normalize` 要随迁移重算 `norm` 并重查唯一性。
- 当前参考价是时间线上没被替代的那一行；它是撤下行时这个币种没有当前价。更正也是发布新的一行。
- `spec` 原地更新，旧值在 `change`；键的含义见[领域文档的「目录基线」](../../docs/domains/catalog.md#目录基线)。
- `actor` 暂存不透明字符串（`agent:<id>`、`user:<id>`、`system:catalog.sync`），待 #23 替换。

### 行级安全与权限

- **公开数据**（`author`、`model`、`name`、`reference_price`、`revision`、`alias`）：读要任意非空范围（`platform.in_any_scope()`），写要平台范围（`platform.in_platform_scope()`；[写法](../platform/pg/README.md#决策与理由)）。`model` 的读另加"不是草稿或在平台范围"，`name` 和 `reference_price` 另加"所属模型已上架或在平台范围"。
- **平台数据**（其余各表）：读写都要平台范围。
- 管理操作以 `axiom_app` 执行，直接 `SELECT … FOR UPDATE` 锁一个模型行。

### 存储

价目表存规范 JSON 和 SHA-256；suppliers 和 billing 存同样的 jsonb，写入前都经 `ParseSchedule`。比较和幂等是对 `MarshalJSON` 输出的字节比较；jsonb 不保留成员顺序，读回的要先解析再编码才能比较。

```json
{"context_tiers":{"basis":["input","cache_read","audio_in","image_in"],"method":"whole","tiers":[{"above":200000,
"rates":{"cache_read":"0.00000025","input":"0.0000025","output":"0.000015"}}]},"currency":"USD",
"rates":{"cache_read":"0.000000125","input":"0.00000125","output":"0.00001"},"v":1}
```

- 成员：`v`（1）、`currency`、`rates`、可选的 `context_tiers`（`basis`、`method`、按 `above` 严格递增的 `tiers`）、可选的 `service_tiers`（等级 → 它的 `rates` 和与顶层逐个相同的 `context_tiers`）。每处 `rates` 都必须写（可为 `{}`），每档都写 `above`，计法写明 `"method":"whole"`。
- 规范形式由 `jsonx.MarshalCanonical` 写出（RFC 8785 排序），单价写成十进制字符串，不经浮点；列表按枚举顺序。未知成员和重复成员拒绝（`jsonx.UnmarshalKnown`）。

### 数值域

- `platform.UnitPrice`：int64，单位 10⁻¹⁵ 个币种单位每计费单位，0 … 10¹⁸。录入的从十进制文本构造，超过 15 位小数或越界拒绝；算出来的（`Scaled`）从精确分数向上取整到 10⁻¹⁵。
- `Usage` 每项 0 … 10¹⁰，只能经 `NewUsage` 构造，`Cost` 不再校验。单项乘积 ≤ 10²⁸，32 项之和在 128 位之内，仍逐步检查溢出。
- `Cost` 返回 `platform.Exact`（128 位，单位 10⁻¹⁵，币种随价目表）；汇率和倍率由 billing 用分数乘，本模块不换算币种。

## 快照

- `Load` 在只读 Repeatable Read 事务里先读 `revision` 再读全部公开表，以平台范围执行；快照不可变，只含 listed 和 retired 模型、它们的名字和 listed 模型各币种的当前价。`ReadSnapshot` 供售价发布者在自己的事务里读同一份输入。
- 发布是比较后替换，只接受更大的 `revision`。写入的节点立即重建，其他节点每 `PollEvery`（5 秒）读一次 `revision`，变了才全量加载。
- **陈旧上限**：`confirmed_at` 是最后一次确认数据库的 `revision` 等于本快照版本的时刻（那次读取开始时）。只有读到相同版本才续期；读到更高版本必须加载成功才算，失败不续期旧快照。超过 `MaxStale`（30 秒，必须大于 `PollEvery`）或首次加载前，`Current()` 返回 `ErrStale`、`Ready()` 为假，gateway 对要解析模型的新请求返回 503。
- `Serving()` 缺必需键返回 false，不补默认值；`Dialect()` 缺的键是 `ClaimUnset` / `ThinkingUnset`，未设置不是 no。
- 规格解析不了的行让整个快照加载失败（`axiom serve` 启动即退出），由 `axiom catalog check` 提前发现。

### 文件解析计费开关

`Config.FileParse` 默认关闭，同时管 `file_parse` 价格的写入和网关的计费口径：关闭时网关按文件数计入 `web_search`，发布、改选来源、数据集同步（记为 held）和 billing 的固定售价规则（经 `Catalog.AllowSchedule`）都拒绝含 `file_parse` 的价目表（`ErrFileParse`）；打开后网关按页计入 `file_parse`。按张、按秒、按字符、按请求和文件搜索的价格不设节点版本门禁，按规格、价格完整性和变化安全阀校验。

## 管理操作

管理接口（`cmd/axiom/admin_catalog.go`）和 `axiom catalog <操作> <请求文件>` 调用同样的方法，平台范围。每个操作带 `Op{Actor, Key, Reason}`，在一个事务里：

1. `Key` 不能缺省或全零（`ErrNoOpKey`）。按 `Key` 查 `change`：已有就返回第一次的结果，种类不同报 `ErrOpKeyReused`；带 `Op.Tx`（管理接口，`idem` 已占键）时已有一律报 `ErrOpKeyReused`。并发同键时后一个在唯一约束上失败。
2. 锁模型行，带 `rev` 的操作比较 `rev`，不同报 `ErrStaleRev`，成功后加一。
3. 对着锁住的行检查并写入；写 `change`（改前改后和证据）和 `audit.Record`（动作名同 `change`，`detail` 只放 `change_id`），审计写不进操作就不成立；影响快照的写入让 `revision` 加一。
4. 提交后本节点立即重建快照，失败只记日志。

操作：`SaveAuthor`、`CreateModel`、`AddName`、`SaveAlias`、`UpdateSpec`、`ListModel`、`Retire`（带 `rev`）、`PublishPrice`（`base` 不符报 `ErrPriceChanged`）、`SelectPriceSource`（只接受 `official`、`manual`）、`SelectDatasetSource`、`WatchPage`、`UnwatchPage`、`ReviewPage`；各自的检查见[领域文档](../../docs/domains/catalog.md#参考价怎么进入目录)和[生命周期](../../docs/domains/catalog.md#模型的生命周期)。

读取（各一个一致的读，含草稿）：`Authors`、`GetAuthor`、`Models`（按状态、厂商、slug 字面前缀筛选，slug 键集分页）、`Model`、`Aliases`；`ModelIn` 在调用方的事务里读回模型。另有命令 `sync`、`check`、`check-pages`、`apply-dialect`。

## 不变量

| 不变量 | 靠什么保证 |
|---|---|
| 名字归一化后全局唯一 | `name.norm` UNIQUE |
| 带日期的名字不会被合并 | `Normalize` 不增删数字（模糊测试） |
| 用户只能调用公开名字和别名 | `ResolvePublic` 只查它们；调用、模型列表、回退列表用同一个函数；public 名字的 CHECK |
| 新快照不会被旧快照覆盖 | 比较后替换（`TestOlderSnapshotNeverReplacesNewer`）；一致性靠 `pg.ReadTx` |
| 快照陈旧超过上限就不再解析 | `TestStaleness`：revision 一直可读、全量加载一直失败时超过上限返回 `ErrStale` |
| 参考价的历史不分叉，发布基于审核时的当前价 | 锁模型行后比较 `base`；「表」里的约束（`TestPriceChainIsEnforcedByTheDatabase`） |
| 每个币种的当前价都来自选定的来源；手填不被同步覆盖 | 发布在锁住的行上比较来源；改选同事务逐币种发布或撤下（`TestSyncLeavesOtherSourcesAlone`） |
| 改价不退化 | 发布和改选对每个现有币种过 `Lost` |
| 超过阈值的改价不自动生效 | 同步先过安全阀（`TestSyncHolds`） |
| 同步记录跨重启和换主存活，失败不改写最后成功和待确认数 | `sync_state` 一行，每轮自己的事务（`TestTheSyncRecordsEachRound`） |
| 管理操作重试不重复生效 | `change.op_key` UNIQUE（`TestOperationRetry`） |
| 过期的页面执行者写不进 | 写回时校验租约代次与启用状态（`TestExpiredAndDisabledPageWorkersCannotWrite`） |
| 锁顺序固定 | 页面行（`FOR SHARE`，多页时按币种排序）→ 模型行 → `revision` |
| 官方参考价有可复核的证据 | 发布时锁页面行，证据必须引用当前快照、摘录逐字在正文里（`TestOfficialEvidenceMustMatchCurrentPageAuthorAndCurrency`） |
| 单价精确、非负，计算不溢出 | 「数值域」；对照 `math/big` 的性质测试 |
| 用量不为零却没有价格的项一定被报告 | `Cost` 返回缺价项；性质测试检查 `Missing` 覆盖 `Cost` 报告的每个缺价项 |
| 上游报告的用量不会被悄悄丢掉 | `Unknown()`（`TestUsageUnknown`） |
| 公开表里没有操作者、理由、证据 | `TestPublicTablesHoldNoInternalColumns` |

## 公开接口

```go
package catalog // internal/catalog

type ModelID uuid.UUID
type Item uint8                        // 封闭枚举；Unit() 是它唯一的单位
type ItemSet = set[Item]               // 32 位位集合；NumItems 超过 32 编译不过
type ServiceTier uint8                 // standard、flex、priority、fast；不叫 Tier，那是账户的等级
type ServiceTierSet = set[ServiceTier]
type Reportable struct{ Items ItemSet; ServiceTiers ServiceTierSet }
type PricePoint struct{ ServiceTier ServiceTier; Above int64; Item Item }
type UnknownUsage struct{ Field, Value string } // NativeTokens 同理：VideoInput、AcceptedPrediction、RejectedPrediction
func NewUsage(q map[Item]int64, st ServiceTier, unknown []UnknownUsage) (Usage, error)
// Usage：Count、ServiceTier、Unknown（副本）、OutputTotal、InputTotal、PhysicalInput、WithPhysicalInput、
// NativeTokens、HasNativeTokens、WithNativeTokens、Recount（替换计费数量，保留物理输入和原生细项）
func ParseItem(name string) (Item, error)
func ParseServiceTier(name string) (ServiceTier, error)
func ParseAbility(name string) (Ability, error)

func ParseSchedule(b []byte) (Schedule, error)                   // 拒绝还不支持的条件
func (s Schedule) MarshalJSON() ([]byte, error)                  // 规范编码
func (s Schedule) Cost(u Usage) (platform.Exact, ItemSet, error) // 纯函数、精确、不分配；返回已定价部分和缺价的项
func (s Schedule) Rate(u Usage, it Item) (platform.UnitPrice, int, bool) // 实际单价与档位，0 是基础档
func (s Schedule) Ceiling(items ItemSet, tiers ServiceTierSet, length int64) (platform.UnitPrice, bool) // Highest 同参：已知最高价及是否有缺价
func (s Schedule) Missing(r Reportable) Reportable
func (s Schedule) Lost(prev Schedule, r Reportable) ([]PricePoint, error) // ErrTierBasisChanged
func (s Schedule) Scaled(multiplier string, currency platform.Currency) (Schedule, error) // 向上取整一次
// 另有 View、Fill、BaseRate、Currency、IsZero、Priced、WithRate，以及售价发布用的 Aligned、AddMarkup、CoversCost、PriceChange、
// PriceIncrease、LargestChange、SmallestMargin（复用计费的取价，口径不同返回 ErrTierBasisChanged，不在调用路径上）
func Normalize(name string) string

func (s *Snapshot) ResolvePublic(name string) (Resolution, error) // 零分配；ErrRetired
func (s *Snapshot) Recognize(name string) (Recognition, bool)
// Snapshot 另有 Revision、Model、ReferencePrice(id, currency)、Models（只有 listed）、OfficialName
// Model：Serving、ServingDirect、Dialect、Reportable、ToolUses（副本）、OutputSpeed、ReasoningVisible、AlwaysSearches、Free、Spec
type Resolution struct{ Model ModelID; Floating bool }
type Recognition struct{ Model ModelID; Exact bool } // Exact：名字本身就是版本证据
func ReadSnapshot(ctx context.Context, tx pg.Tx) (*Snapshot, error)

type Config struct {
	MaxStale, PollEvery time.Duration // 30 秒、5 秒
	Logger              *slog.Logger
	PriceChangeLimit    int // 25（百分比）
	FileParse           bool // 见「文件解析计费开关」
}
func Open(db *pg.DB, scope pg.Scope, cfg Config) (*Catalog, error)
func (c *Catalog) Current() (*Snapshot, error) // 一个请求只用一个快照
func (c *Catalog) Run(ctx context.Context) error // 每 PollEvery 调 Refresh；另有 Ready、AllowSchedule、FileParse
// 读取与管理操作见「管理操作」

func NewFetcher(client *egress.Client) *Fetcher // 测试用 NewTestFetcher 覆盖地址
func (c *Catalog) Fetch(ctx context.Context, f *Fetcher, src PriceSource) (Fetched, error) // At 取自数据库时钟
func ParseLiteLLM(body []byte) ([]Entry, error) // ParseOpenRouter 同；坏条目记在 Entry.Err
func (c *Catalog) SyncDataset(ctx context.Context, f Fetched, publish bool) (SyncReport, error) // publish 为假是演习
func (c *Catalog) SyncAll(ctx context.Context, f *Fetcher, publish bool) ([]SyncReport, error)
func RegisterSync(ctx context.Context, cfg SyncJobConfig) error  // AlertAfter 为零用 48 小时
func RegisterPages(ctx context.Context, cfg PageJobConfig) error
func NewPageInspector(client *egress.Client) *PageInspector // check-pages 用，不连数据库
func (c *Catalog) Check(ctx context.Context, authors []string) ([]ModelCheck, error) // 另有 Unrecognized
func (c *Catalog) ApplyDialects(ctx context.Context, actor audit.Actor, f DialectFile, reason string, apply bool) []DialectResult // 文件经 ParseDialectFile
```

## 决策与理由

| 决策 | 理由 | 不选 |
|---|---|---|
| `alias` 只追加 `(name, rev)`，快照读最高 `rev` | 旧快照仍能读到当时的映射，`change` 对得上 | 原地更新：要 UPDATE 权限 |
| 价目表存规范 jsonb，结构和校验在 Go | 三个模块共用；比较和幂等变成字节比较 | 拆成三张表：每个模块都要重组代码 |
| 单价 int64 定点，`Cost` 返回 128 位；汇率和倍率在 billing 用分数 | 域有界时不溢出、不分配；10/71 这类值有限小数表示不了 | 全程 `math/big`：每次分配 |
| 快照用只读 Repeatable Read 加载；陈旧超限拒绝新请求 | Read Committed 下两次查询可能看到不同的提交；刷新持续失败时，别处下线的模型不能无限期被调用 | 多表分别查询；一直用旧快照 |
| 改动写 `change` 并同事务调 `audit.Record` | `change` 存证据和 `op_key`，是平台数据，租户读不到；审计是运营检索的共用一条线，`detail` 只放 `change_id` 不受 8 KiB 限制 | 公开表带 `actor`；只写其中一个 |
| 现在直接 `FOR UPDATE` 锁模型行，`lock_models` 随后台任务角色 | `SECURITY DEFINER` 函数要属 `axiom_owner`，与[平台的启动检查](../platform/README.md#迁移)冲突，现在没有调用方 | 现在就建 |
| 页面租约带代次，内容去重与当前指向分开 | 过期执行者提交不了；A→B→A 时证据跟着现在的内容 | 按最晚入库的快照校验 |
| 数据集不落条目，页面 `review` 只收 page_changed | 条目和候选每轮可重算；不把数据集需求猜进页面的表 | 提前合并两类审核 |
