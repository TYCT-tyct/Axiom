# routing

路由在调用路径上的接口：会话标识、回退路线、换不换端点的判定。路由的规则见[路由](../../docs/domains/routing.md)，一次调用的编排见 [gateway](../gateway/README.md)。

> 已实现（[Issue #26](https://github.com/TYCT-tyct/Axiom/issues/26)，PR R1–R3 合为一个，见 [gateway 的「PR 拆分」](../gateway/README.md#pr-拆分)）：路由快照、`SessionKey`、`Plan`、`Decide`、按供应商的重试预算、进程内的端点负载与 429 降权、会话粘性存储、路由记录。快照由 `NewSnapshot` 从端点和预设的数据组装，这些数据由 suppliers、quality、billing、accounts 提供；它们的类型还没有公开，所以 routing 先用最窄的自有类型（见「现在的输入类型」），对方公开后在调用处换掉。代码与本文不一致时，在同一个 PR 里改本文。

## 接口

```go
func New(cfg Config) (*Router, error)
func (r *Router) NewSnapshot(built time.Time, endpoints []Endpoint, presets []Preset) (*Snapshot, error)
func (r *Router) Replace(s *Snapshot) // 只接受更新的快照
func (r *Router) SessionKey(acct AccountID, req *protocol.Request, body []byte) SessionKey // 只扫有界前缀
func (r *Router) Plan(in Input) (Plan, *Wait, error) // 纯内存；*NoEndpoint{Reasons, Permanent}；ErrStale
func Decide(f Failure) Action // NextEntry | OtherCredential | ReturnToClient
```

- **`SessionKey`** 在准入之前算出，最多读 `Config.SessionPrefix`（默认 16 KiB）字节：客户端自己的 `session_id`、`prompt_cache_key`，或 Anthropic `metadata.user_id`（Claude Code 的会话）；都没有时，用账户、模型和对话（`messages`、`input`、`contents`）的第一条消息（最多 1 KiB）的哈希：只取第一条，所以对话变长、或后面的参数变化，键都不变。键里总含账户，所以不同账户都发 `default` 也不会串。透传给上游的不透明标识不在这里：gateway 的 `Executor.identity` 按账户和客户端的值派生。gateway 用 `Stuck` 读粘住的端点，作为 `Input.Sticky` 传入；gateway 在调用交付了内容、且服务它的端点与粘住的不同时调 `Stick`，收到“上下文已压缩”标记时调 `Forget`（写回规则见 [gateway 的「粘性」](../gateway/README.md)）。
- **`Plan`** 在准入事务的 `fn` 里调用，只读内存快照和请求，不查库、不访问缓存。预设内容取快照，预设 ID、可用模型取 `Admission`；快照陈旧超过 `MaxStale`（30s）返回 `ErrStale`（gateway 回 503）。步骤：对每个模型（主模型在前，备选在后）取候选，逐个过滤并记下原因，按“指定顺序 → 没达到性能期望的靠后 → 粘住的端点 → 策略”排序，取前 `MaxEntries`（3）个不同的端点（同一个模型写了两次只算一次，同一个端点只进一次）；第二个起的条目所在供应商的重试预算用完则跳过。每个条目带端点、供应商（尝试花它的重试预算）、`Target`（怎么调这个端点：它对模型的名字和模型规格里的写法，`Endpoint.Target` 带来；调用格式由 `Plan` 按请求选：端点说请求的格式就用它，否则是 Chat 请求转换给的第一个原生格式，先 Messages 后 Responses；`MaxOutput` 也由 `Plan` 填，等于下面的条目输出上限，端点的上限未知时为 0，见下）、模型、价格版本、输出上限（请求的上限与端点上限取小，请求没写取端点的默认）、`Segmentable` 和 `Limits`（报价用的 `billing.Model`，上下文、图片和页的 token 数、工具次数、输出速度、可上报的计费项，与过滤用的是同一份）。gateway 用 `DemandOf` 把请求摘要转成报价的 `Demand`，路由比较价格上限用的也是它。`StartBy` 是准入后不再发起新尝试的时刻（窗口取预设的、没有则取 `Config.AttemptWindow`；节点里 `cmd/axiom` 把它设成 gateway 配置的 `StartBy`，两处不会不同），`MaxAttempts` 是最多尝试几次（含同一条目换凭证），是硬上限，取预设的、没有则取路由器的默认，从不因路线更长而提高；路线里到不了的条目（超过它的）被去掉。
- **结果**：候选都满载（过了全部过滤但已达 `MaxConcurrent`）返回 `*Wait`，gateway 回滚准入、在事务外排队；没有可用端点返回 `*NoEndpoint`，`Permanent` 为真（所有被排除的原因都是永久的：能力、上下文、格式、预设或覆盖过滤、没有售价、未知计费项、已停用、模型不可用于这把 key）时 gateway 回 404，否则（有熔断、供应商余额、没有出口、重试预算）回 503，见[接入的「错误」](../../docs/domains/gateway.md#错误)。账户看不到的端点（别人的自带端点）不进原因，不泄露存在。
- **过滤**，按顺序，先便宜的：可见性 → 已启用 → 熔断 → 供应商余额（M1 组装时 `BalanceOK` 恒为真：suppliers 的快照已只取余额大于 0 的供应商，按请求预估成本付不起的过滤和低余额降权尚未接入）→ 出口 → 能力（`protocol.Request.Needs` 必须是端点 `Caps` 的子集）→ 格式（`NativeOnly` 时要原生支持；否则可以经转换提供，而 Chat 请求只经转换交给 Messages 或 Responses 端点，M1 不做同格式 Chat 的改写，见 `callFormat` 与[接入的「转发规则」](../../docs/domains/gateway.md#转发规则)；网关以流读上游，所以生成请求只保留能流式的端点，`NoSegments` 的压缩调用是唯一的例外，见 `streamsFor`）→ 服务等级（请求可能的等级 `ServiceTiers` 与端点提供的 `ServiceTiers` 要有交集）→ 上下文（输入按 `TextBytes / billing.InputBytesPerToken`（4，不是路由器的设置：计费暂估用同一个常量）估计，与下面的输出上限是同一条规则）→ 预设的过滤（来源类别、质量与置信度、只走已验证的、数据政策、最高价）→ 请求覆盖的过滤（只走、排除、类别、数据政策、最高价、关闭回退时只留指定的供应商）→ 有售价 → 没有未知计费项（`Endpoint.UnknownItem`，billing 遇到该模型报告了没有价格的字段，见 [billing 的「兜底价」](../billing/README.md#兜底价)）→ 未满载。预设和覆盖各检查一遍，端点必须都通过，所以覆盖只能收紧；最高价各自比较，等于取较低者，比较的是请求可能被收的最高价：请求可能的服务等级加标准档（计费也这样取），长度取 `billing.InputBound`（文本、图片、文件按最大值，会再次采样的工具调用则取整个上下文，与计费报价用同一个函数），在 `Schedule.Highest` 的各档里取最高。某个等级缺价不会让其他已知价格被忽略；缺价的 token 项按计费实际的兜底价，即该价目表最高的 token 单价，参与比较；缺价的按次项目是站点默认价，路由不知道，不比较。预设关闭回退（`NoFallbacks`）时只留预设 `Order` 里的供应商，请求写什么顺序都不放宽。
- **排序策略**：均衡是价格加权随机，权重 = 健康度 × 质量分 × 429 降权系数 ÷ 价格（输入加输出的标准档单价），低余额减半，带工具调用时再乘 (1 − 工具错误率)²；用 Efraimidis–Spirakis 的随机键取样，所以第一个按权重比例选出，后面在剩下的里同样处理（不放回）。最便宜按价格升序，最快按吞吐降序再按首字延迟升序，质量优先按质量分 × 置信度（带工具调用时再乘 (1 − 工具错误率)）降序；平局按端点 ID，同样的输入得到同样的顺序。
- **粘性**只在这些同时成立时生效：请求带了 `Sticky`，端点缓存读取单价低于输入单价，健康度不低于 0.5，没在 429 降权中，没满载。生效时它排在同一指定顺序组的最前。
- **条目的输出上限**只有一条规则，过滤和条目共用：请求写了上限就取它与端点上限中的较小者，没写取端点的默认；上下文要装得下输入加这个上限，请求写明的上限装不下就排除该端点，默认值则裁到上下文剩下的部分。报价用的就是发出去的：请求没写上限时，`Target.MaxOutput` 是这个（可能被裁短的）值，`Prepare` 把它写进发给上游的请求（`TestOutputCapQuotedIsTheCapSent`），否则上游按模型的默认值生成，比预留依据的多；端点的上限未知（0）时，报价取上下文剩下的部分，这个值上游不一定收，所以不发（`Target.MaxOutput` 为 0）。
- **`Segmentable`**（按条目和这个请求算）= 网关能跟着计量（`Input.NoSegments` 为假）∧ 端点能流式 ∧ 实测传递取消 ∧ 该格式下推理进度可见 ∧ 请求没有隐藏推理 ∧ 端点的输出速度已知（`Endpoint.OutputSpeed` 大于 0；超额额度是速度乘等待时间，速度未知会少预留）∧ `FirstSegment` 大于 0 ∧ 条目输出上限大于 `FirstSegment + Overshoot`（billing 的分段大小与超额额度，由 gateway 配置；M1 没有这份配置，`FirstSegment` 为 0，所以没有条目可分段，全部全额预留）：上限不高于两者之和时一次预留足，不分段。`NoSegments` 是 gateway 对整份答复的调用（`/responses/compact`）设的：那种尝试没有可观测的进度，只能在准入时按上限预留，端点也不必能流式，因为网关读的是整份答复。
- **`Decide`** 是纯函数，只看失败的作用范围：请求自身（`ScopeRequest`，如其他的 400）→ 返回客户端；凭证、出口 → 同端点换凭证或出口；端点、分组、供应商 → 下一个条目。上下文超长、内容审核、余额不足、限流、过载、超时这些，由 suppliers 的适配器分到端点、分组或供应商范围，所以都是换条目，规则即[路由的「失败怎么办」](../../docs/domains/routing.md#失败怎么办)。提交后 gateway 不再调用它。
- **共享状态在进程内**（M1，D10；[Issue #26](https://github.com/TYCT-tyct/Axiom/issues/26) 的验收项）：端点在途数（`Begin`/`End`）、429 降权（`Throttled`，`ThrottleFloor` 起在 `Recovery` 内线性恢复）、按供应商的重试预算（`Sent` 记首次请求，`TakeRetry` 取用一次重试；窗口内重试数不得超过请求数 × `RetryRatio` 加 `RetryFloor`，十个桶的环让旧流量过期）和会话粘性（分片的有界 map，到期或超限时丢弃，丢失只让下一次重新选择）。它们不在快照里，快照只持有指向它们的指针，所以快照整体替换后状态还在。M2 多节点时在后台同步，硬上限不依赖共享的数字，调用路径上不访问缓存；限流计数不在 routing。
- **路由记录** `Record`：被排除的候选和原因（`Plan` 填）、选定的路线（`Plan` 填）、每次尝试的结果（gateway 追加）；`JSON()` 给出存储形式，observability 随调用保存。

## 现在的输入类型

suppliers、accounts、quality、billing 还没有公开 routing 要的类型，下面是 routing 自己的最窄定义，对方公开后各换一行：

| routing 的类型 | 将来来自 | 说明 |
|---|---|---|
| `EndpointID`、`SupplierID`、`PresetID`、`AccountID`、`SiteID` | suppliers、accounts | 都是 `uuid.UUID` 之上的定义类型 |
| `Admission` | accounts 的 `Admission` | 只取账户、站点、预设 ID、key 可用的模型 |
| `Endpoint` | suppliers（分组 × 模型、凭证余额、出口数）、quality（熔断、健康度、质量、置信度、吞吐、延迟、工具错误率、实测取消）、catalog（最快输出速度 `OutputSpeed`、可上报的计费项 `Reports`）、billing（价格版本；`UnknownItem` 来自 `billing.UnknownItems`） | 快照重建时从各处合并成一份 |
| `Failure`、`Scope` | suppliers 的 `Failure`、`Scope`（在 `suppliers/internal/adapter`，顺序相同） | 调用处转换 |
| `Endpoint.Target` | suppliers 的 `Routable`（`Upstream` 是发给上游的模型名，`Formats` 是这个端点能用的调用格式，`Schedule` 是成本计划）、catalog 的 `Model.Serving()`（`Context`、`MaxOutput`、`Streams`、`Abilities`）加模型规格里的其余字段（推理的写法、系统消息的位置等） | 前半已能组装：`Service.Routable()` 给出上游名字、模型、格式、成本计划和这家供应商的 `Streams`/`Cancels`（suppliers README 的「调用面」），`Model.Serving()` 给出上下文、输出上限、流式和能力名（catalog README 的 `spec` 一条；能力名换成 `protocol.Capabilities` 的位在这一侧做）。**还没组装**：其余 `Target` 字段（`Thinking`、`MidSystem`、`EncryptedReasoning` 等）catalog 的 spec 还没有，随组装快照的代码一起加；空的 `Target.Model` 会让尝试发出 `"model":""`，所以组装时必须填 |
| `PriceRef` | billing 的价格版本 | 版本号加锁定的 `catalog.Schedule`，正是 `billing.Quotable.Price` 要的 |

## 决策与理由

| 决策 | 理由 | 不选 |
|---|---|---|
| 回退判定是纯函数 `Decide`，只看失败的作用范围 | 适配器已经把每种上游错误分到作用范围；规则与路由文档的「失败怎么办」在一处，可以表驱动地测 | 由 gateway 按状态码判断；`Decide` 再按原因细分：两处都要认识每种原因 |
| `Segmentable` 在 `Plan` 里按条目和请求算 | 端点能否截断、请求是否隐藏推理都会改变它；预留按条目报价时就要知道 | 按端点固定 |
| 预设内容来自快照，预设 ID 来自 `Admission` | 不新增查库；改预设内容滞后至多一个快照陈旧上限 | 准入时读预设内容 |
| 过滤的每一步给一个原因，永久与否看原因 | gateway 的 404 与 503 之分、运营的排查都靠它；混合时按暂时处理，因为等待可能治好 | 只返回“没有端点” |
| 已停用算永久，熔断、余额、出口、重试预算算暂时 | 停用是配置，不会自己好；后几个会自己恢复。文档的列表没写停用，按它不会自己恢复归入永久 | 停用算暂时：模型只剩停用的端点时回 503 并叫客户端重试 |
| 上下文用 `TextBytes / 4` 估计，只在“肯定装不下”时排除 | `TextBytes` 是 token 数的上界（字节），拿它排除会把约 4 倍上下文的合法请求永久拒掉；估计偏小时上游返回上下文超长，`Decide` 换下一个条目 | 直接用 `TextBytes`；把图片和 PDF 页按最大值算：同样会误拒 |
| 价格上限、质量等预设条件和请求覆盖各检查一遍 | 两者都必须通过，等价于“只能收紧”，不需要合并规则，也能分别记原因 | 合并成一个有效条件再检查：要为每个字段写合并规则 |
| 加权用不放回取样（随机键 ln(u)/w） | 第一个按权重比例，后面在剩下的里同样处理，一次排序得到整条路线；性质测试用频率验证 | 每个位置各抽一次：要重复归一化，且容易抽到重复 |
| 重试预算在 `Plan` 里只“看”，在 gateway 发出重试时“取” | `Plan` 在准入事务里，不该改共享状态；几个并发的重试要靠 `TakeRetry` 原子地各占一次 | `Plan` 里直接扣：Plan 可能因排队重做，会重复扣 |
| 状态在进程内，快照只持有指针 | M1 只有一个核心节点（D10）；快照整体替换不丢负载和降权 | 状态放进快照：每次重建清零 |
| 会话键含账户，透传标识用密钥派生 | 不同租户常发相同的会话值；原样透传会在上游串到一起并泄露客户端的标识 | 只用客户端的值 |
| `BaseRate` 加在 catalog 的 `Schedule` 上 | 路由要比较价格和算权重，需要读标准档的单价；价目表的字段不导出 | routing 自己解析 JSON |
