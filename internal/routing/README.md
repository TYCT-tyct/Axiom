# routing

路由在调用路径上的接口和不变量：快照、会话标识、回退路线、换不换端点的判定、路由预设的存取。行为和规则见[路由](../../docs/domains/routing.md)，一次调用的编排见 [gateway](../gateway/README.md)。代码与本文不一致时，在同一个 PR 里改本文。

## 接口

```go
func New(cfg Config) (*Router, error)
func (r *Router) NewSnapshot(built time.Time, endpoints []Endpoint, presets []Preset, rules ...ByokRule) (*Snapshot, error)
func (r *Router) Replace(s *Snapshot) // 只接受更新的快照
func (r *Router) SessionKey(acct AccountID, req *protocol.Request, body []byte, header string) SessionKey // 只扫有界前缀
func (r *Router) Plan(in Input) (Plan, *Wait, error) // 纯内存；*NoEndpoint{Reasons, Permanent}；ErrStale；ErrPresetUnavailable
func (r *Router) KeyView(a Admission) (KeyView, error) // 模型列表看一把 key 能走到什么
func (v KeyView) Serves(m catalog.ModelID) bool
func (v KeyView) Allows(m catalog.ModelID, e EndpointID) bool
func (r *Router) Block(e Entry, scope Scope, after time.Duration) // 暂停端点或供应商，最多两小时；没给用 Config.Pause
func (r *Router) Blocked(e Entry) bool // 已定的条目在发出前再查一次
func Decide(f Failure) Action // NextEntry | OtherCredential | ReturnToClient

func (p *Preset) ReadRules(rules []byte) error // 校验并编译一个版本的规则；写入和快照共用
func (s *Presets) Load(ctx context.Context, tx pg.Tx) ([]Preset, error) // 快照的预设来源，平台范围
func DefaultPreset(ctx context.Context, tx pg.Tx, site SiteID, account AccountID, workspace WorkspaceID, currency platform.Currency) (id PresetID, layer PresetScope, err error)
func Module() pg.Module // 迁移：预设三张表、全局预算行、上限和暂停开关
func ListPresets(ctx context.Context, tx pg.Tx, a Admission) ([]StoredPreset, error) // 一把 key 能选的活预设
func LayerPresets(ctx context.Context, tx pg.Tx, layer Layer) ([]StoredPreset, error) // 一层自己的全部预设
func GetPreset(ctx context.Context, tx pg.Tx, id PresetID) (StoredPreset, error)
func CreatePreset(ctx context.Context, tx pg.Tx, actor audit.Actor, layer Layer, slug string, c PresetContent, reason string) (StoredPreset, error)
func UpdatePreset(ctx context.Context, tx pg.Tx, actor audit.Actor, id PresetID, revision int64, c PresetContent, reason string) (StoredPreset, error)
func DisablePreset(ctx context.Context, tx pg.Tx, actor audit.Actor, id PresetID, revision int64, reason string) error
func ReadDefault(ctx context.Context, tx pg.Tx, layer Layer) (Default, error)
func SetDefault(ctx context.Context, tx pg.Tx, actor audit.Actor, layer Layer, currency platform.Currency, preset *PresetID, version int64, reason string) (Default, error)
func SelectablePreset(ctx context.Context, tx pg.Tx, a Admission, id PresetID) error // key 显式选的预设能不能用
func ReadLimits(ctx context.Context, tx pg.Tx) (Limits, int64, error) // 零值是代码默认（DefaultLimits）
func SetLimits(ctx context.Context, tx pg.Tx, actor audit.Actor, l Limits, rev int64, reason string) (int64, error) // 越界 *LimitError，rev 已变 ErrPresetStale
func ReadSuspension(ctx context.Context, tx pg.Tx) (time.Time, error) // 没暂停是零值
func SetSuspension(ctx context.Context, tx pg.Tx, actor audit.Actor, suspend bool, reason string) (time.Time, error) // 已是所求状态时什么都不写
```

`Config` 的默认值在 `New`：`MaxStale` 30s、`StickyTTL` 1h、`SessionPrefix` 16 KiB、`MaxAttempts` 4、`AttemptWindow` 2m、`Recovery` 1m、`ThrottleFloor` 0.1、`Pause` 5s、`RetryRatio` 0.2、`RetryFloor` 3、`RetryWindow` 1m。给了 `Config.Alerts` 却没给 `Node` 时 `New` 报错。

## 不变量

- **`Plan` 只读内存**：在准入事务里调用，不查库、不访问缓存、不改共享状态；重试预算只「看」，gateway 发出重试时才 `TakeRetry`。预设内容取快照，预设 ID、可用模型、账户、站点、工作区、币种和等级取 `Admission`。快照没有这个预设或 `Preset.visibleTo` 不放行时返回 `ErrPresetUnavailable`，从不改用别的预设；快照超过 `MaxStale` 返回 `ErrStale`，`Fresh` 报告同一条件。
- **路线**：最多 `MaxEntries`（3）个不同的端点，在预留前定死，之后只会缩短。`MaxAttempts` 是硬上限（含同条目换凭证），取预设的、否则路由器的，从不因路线更长而提高；`Input.FileRef` 时为 1。`StartBy` 取预设的窗口、否则 `Config.AttemptWindow`。`Input.Auto`、`Input.Free` 不用 `Models`，在全部模型或快照的 `free` 列表里取候选并跨模型整体排序；`free` 为空时是永久的 `NoEndpoint`（`NoCandidates`）。
- **条目**带端点、供应商、`Target`（上游名字、规格写法、`Plan` 按 `callFormat` 选的调用格式、`MaxOutput`）、模型、价格版本、输出上限、`Segmentable`、`Limits`（报价用的 `billing.Model`，与过滤用的同一份；`Limits.ID` 必须是条目的 `Model`）和同次构建的 `platform.ExchangeRate` 副本。gateway 用 `DemandOf` 把请求摘要转成报价的 `Demand`，路由比较价格上限用的也是它。
- **过滤**，按顺序，先便宜的：可见性 → BYOK 不回落 → 已启用、静态熔断、供应商余额已同步、出口 → 格式 → 能力、可流式（`NoSegments` 与直接格式例外，见 `streamsFor`）、服务等级、上下文 → 预设 → `VerifiedOnly`（`NotVerified`，暂时）→ 请求覆盖 → 有售价 → 无未知计费项 → 没在暂停 → 付得起（`Funds`、`Endpoint.Cost`、`billing.ReservedCost`）→ 未满载（`MaxConcurrent` 或 `Endpoint.Capacity.Full()`）。暂停排在永久条件之后，所以不合适的端点记永久原因。账户看不到的端点不进原因。
- **永久与暂时**：能力、上下文、格式、服务等级、预设或覆盖过滤、没有售价、未知计费项、原生成本无法报价、已停用、模型不对 key 开放是永久的（gateway 404）；离线、供应商余额、没有出口、重试预算、`cooling_down`、`NotVerified` 是暂时的（503）；混合时按暂时。候选都满载返回 `*Wait`，gateway 在事务外排队。
- **上下文**：输入按 (`TextBytes` + `SearchBytes`) / `billing.InputBytesPerToken` 估计，只在肯定装不下时排除。条目输出上限只有一条规则：请求写了取它与端点上限的较小者，没写取端点默认并裁到上下文剩下的部分；请求写明的装不下就排除。没写时 `Target.MaxOutput` 就是报价所用的值，`Prepare` 把它发给上游；端点上限未知（0）时不发。直接格式的上限恒为 0，输入恰好等于上下文也装得下。
- **价格**：`Endpoint.AudiencePrices` 是快照预编译的稀疏受众索引，`priceFor` 用计费的同一选择器在账户、等级与全部之间选版本；匹配层没有版本就拒绝售卖，不回落到宽泛规则。过滤、最高价、排序、粘性和冻结都用选中的版本。最高价按请求可能的服务等级加标准档、`billing.InputBound` 的长度，在 `Schedule.Highest` 里取最高；缺价的 token 项按最高 token 单价，缺价的请求项按最高请求价。
- **预设与覆盖各检查一遍**，端点必须都通过，所以覆盖只能收紧；最高价各自比较即取较低者。预设 `NoFallbacks` 时只留 `OrderNames` 里的分组。
- **排序**：`compare` 按 `order` → BYOK → 没达到性能期望 → 不粘 → 策略分数，平局按端点 ID。均衡用随机键 ln(u)/w 不放回取样；最快对没测过的端点抽 `Rand`，低于 `exploreShare`（5%）时把随机键最大的那个放到策略层最前。粘性要求带了 `Sticky`、缓存读单价低于输入单价、健康度不低于 0.5、没在 429 降权、没满载。
- **熔断**在请求自己的过滤与排序之后处理，不改变已排好的先后：熔断的端点只作最后备选，`Offline` 一律排除；`Blocked` 在发出前复查离线和暂停。没有其他健康候选时忽略慢启动上限。`PausedUntil` 向 quality 暴露两种暂停的较晚时刻。
- **`Segmentable`** = ¬`NoSegments` ∧ 能流式 ∧ 实测传递取消 ∧（模型会推理时）实际调用格式下推理可见 ∧ 请求没有隐藏推理 ∧ `OutputSpeed` > 0 ∧ 段大于 0 ∧ 条目上限大于 `Segments.Output + Segments.Allowance(速度)`。
- **`Decide`** 是纯函数，只看作用范围：请求 → 返回客户端；凭证 → 同端点换凭证；出口、端点、分组、供应商 → 下一个条目。提交后 gateway 不再调用它。
- **共享状态在进程内**：在途数（`Begin`/`End`）、429 降权（`Throttled`）、暂停（凭证范围的 `Block` 什么也不做）、重试预算（`Sent`/`TakeRetry`，十个桶的环，重试数 ≤ 请求数 × `RetryRatio` + `RetryFloor`）、进程内粘性表（分片有界 map）。快照只持有指针，整体替换后状态还在；暂停只延长、不缩短。凭证容量是供应商的 `Capacity` 视图。共享粘性的 `StickyKey`、`StickyEndpoint`、`StickyWrite` 只定义键、值和 TTL，gateway 负责同批预检和后台投递；配了 Valkey 就不读写进程表。
- **`SessionKey`** 最多读 `Config.SessionPrefix` 字节，顺序见[会话粘性](../../docs/domains/routing.md#会话粘性)；兜底的哈希只取第一条消息（最多 1 KiB），对话变长键不变；键总含账户。透传给上游的标识由 gateway 的 `Executor.identity` 派生。
- **`Record`**：被排除的候选（存储时逐个列出最多 16 个，每个原因的个数全记）、路线、每次尝试（gateway 追加）、`Unmeasured`、`Preset`/`PresetVersion`、`Overrides`（位集合，不分配）；`JSON()` 是存储形式，契约模式是 `CallRoute`（`TestRecordKeepsItsContract`）。
- **请求的名称选择**：`Override.OrderNames`、`OnlyNames`、`ExcludeNames` 和 `Preset.OrderNames` 在 `Endpoint.Codename` 上精确匹配，从不转成供应商 ID。`Override.RequiredParameters` 来自 `protocol.RequestParameters`，按选中格式的 `Endpoint.Specs[f].Parameters` 判定（`HasAll`），不过记 `OverrideFilter`。
- **BYOK**：账户的不回落选择（`ByokRule`，经 `NewSnapshot` 的 `rules` 传入，与凭证状态无关）或一个可见的 `Byok && !ByokFallback` 候选，让同模型的非 BYOK 候选以 `ByokNoFallback` 出局；其他 BYOK 候选不受影响。
- **`KeyView`** 只问快照里有没有这把 key 看得见、可收费、预设留下的端点，不走 `Plan`，不看熔断、负载、暂停、预算和快照新旧；预设判定与 `Plan` 共用 `presetAllows`（经 `viewAllows`）。

## 输入类型

suppliers、accounts、quality、billing 还没公开 routing 要的类型，routing 先用最窄的自有类型，对方公开后在调用处换掉：`EndpointID`、`SupplierID`、`PresetID`、`AccountID`、`SiteID`、`WorkspaceID`（`uuid.UUID` 之上的定义类型）；`Admission`（账户、站点、工作区、币种、预设 ID、可用模型、等级）；`Endpoint`（快照重建时从 suppliers、quality、catalog、billing 合并）；`Capacity`（只有 `Full()` 的窄接口）；`Failure`、`Scope`（与 suppliers 的顺序相同，调用处转换）；`PriceRef`（版本号加锁定的 `catalog.Schedule`）。`Endpoint.Target` 由 `suppliers.Service.Routable()`、`catalog.Model.Serving()` 和 `Model.Dialect()` 组装，规格没写的键留作未设置；空的 `Target.Model` 会发出 `"model":""`，组装时必须填。`Endpoint.Class` 是来源类别 slug，`Entry.Class` 把它冻结进计划。

## 路由预设

迁移在 `migrations/`，表和规则见[路由的「路由预设」](../../docs/domains/routing.md#路由预设)。

- **`ReadRules`** 严格解码（不认识的成员拒绝，新字段在旧节点上只让这一个预设不可用），编译成 `Preset.Rule`、`Authors`、`Models` 和平台的运行时设置；覆盖规则在编译时补全继承并校验只能收紧，所以运行时不可能放宽；错误都包着 `ErrRules`。平台时长按整秒比较上限，不经 `time.Duration` 换算。
- **`Presets.Load`** 先读暂停开关并当场告诉 `Presets.Router`，再读全部预设的头（含停用的），只为没编译过的 `(id, version)` 读规则；读不懂的版本记 `routing.preset_isolated` 并不进快照；系统预设缺失或读不懂时返回错误。暂停由 `Plan` 和 `KeyView` 共用的 `presetOf` 读，与快照内容无关。
- **`NewSnapshot`** 把按作者、按模型的规则建成指针表，`Plan` 每个模型查一到两次，不复制、不分配（`BenchmarkPlanPreset`）。
- **写**（`manage.go`、`defaults.go`）在调用方的范围里进行，表的策略限在本层；规则先经 PostgreSQL 打印回文本再 `ReadRules`，所以写入检查的就是快照读到的文本。错误：`ErrPresetStale`（`revision`/`rev` 不符）、`ErrPresetCurrency`、`ErrPresetNotFound`（含已停用）、`ErrPresetReadOnly`、`ErrPresetInUse`、`ErrPresetNotSelectable`、`ErrDefaultInvalid`、`ErrPresetsSuspended`。什么都没变的 `UpdatePreset` 什么都不写。
- **锁的顺序**：建、改、停用和改默认都经 `lockWrites` 先锁 `routing.preset_budget` 行、再读开关，`takeBudget` 在锁后数本租户的行，`SetLimits` 也先锁它；所以配额不被并发抢穿，停用与改默认互相看得见，一次写要么在暂停前提交要么被拒。`SelectablePreset` 只读开关不锁；`DefaultPreset` 和 `SetLimits` 不看开关。各层上限由 `limitsOf` 按 `Limits` 取。
- **`StoredPreset.Summary`** 从编译后的规则算摘要，读不懂的返回 false。**`Router.CheckRules`** 先按层 `ReadRules`，再用 `viewAllows` 对当前快照里这一层能看到（`Owner.visibleTo`）、有售价的端点判断；没有快照时只给摘要。
- **告警**（`watch.go`）：`Router.Planned` 每次调用记一次结局，原子计数报为 `axiom.routing.preset_refusals`；平台和站点预设挂一个跨快照的 `presetTally`，`Replace` 每满一分钟结一个窗口，合并键 `<Config.Node>/<预设 UUID>`，只解除本节点发的。

## 决策与理由

| 决策 | 理由 | 不选 |
|---|---|---|
| 回退判定是纯函数 `Decide`，只看作用范围 | 适配器已把每种错误分到范围，规则在一处，可以表驱动地测 | gateway 按状态码判断：两处都要认识每种原因 |
| `Segmentable` 按条目和请求算 | 端点能否截断、请求是否隐藏推理都会改变它 | 按端点固定 |
| 受众索引和派生价在快照预计算，账户与等级在准入选择 | 每次最多三次索引查询，不随账户规则数增长；等级变更不必重新发布组合 | 准入时解析规则；每账户×等级复制路由 |
| 预设内容来自快照，ID 来自 `Admission` | 不新增查库；内容滞后至多一个快照陈旧上限 | 准入时读预设内容 |
| 覆盖规则在编译时补全，`Plan` 按模型 → 作者 → 默认取第一条 | 调用路径不逐字段合并、不分配 | 运行时合并三层 |
| 编译结果按 `(预设, 版本)` 缓存 | 重建只读头，规模由条数决定，不由字节决定 | 每次解析全部规则 |
| 每步过滤给一个原因，永久与否看原因 | gateway 的 404/503 之分和排查都靠它 | 只返回「没有端点」 |
| 已停用算永久 | 停用是配置，不会自己好 | 算暂时：只剩停用端点时叫客户端重试 |
| 上下文用 `TextBytes / 4` 估计，只在肯定装不下时排除 | `TextBytes` 是 token 数的上界，拿它排除会误拒约 4 倍上下文的合法请求 | 直接用 `TextBytes`；图片和页按最大值算 |
| 预设和请求覆盖各检查一遍 | 等价于只能收紧，不用合并规则，也能分别记原因 | 合并成一个条件：每个字段都要合并规则 |
| 加权用不放回的随机键 | 一次排序得到整条路线 | 每个位置各抽一次：要重复归一化，易抽到重复 |
| 只对请求自己的 `Models` 去重 | 快照的 `models`、`free` 构造时已唯一，再去重会让准入里的扫描随模型数平方增长 | 每份列表都去重 |
| 状态在进程内，快照只持有指针 | 快照整体替换不丢负载和降权 | 状态放进快照：每次重建清零 |
| `KeyView` 不走 `Plan`，预设部分共用 `presetAllows` | 列表要的是「有路可走」，满载、熔断只是暂时的，不该让模型闪进闪出 | 用 `Plan` 探测：要造假请求；在 `KeyView` 里重写过滤：会漂移 |
| `BaseRate` 加在 catalog 的 `Schedule` 上 | 路由要读标准档单价，价目表字段不导出 | routing 自己解析 JSON |
