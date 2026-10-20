# quality

端点评估在调用路径上的代码。规则和理由见[端点评估](../../docs/domains/quality.md)的「健康与熔断」，本文写代码层面的接口、不变量和取舍。

> 已实现：[#313](https://github.com/TYCT-tyct/Axiom/issues/313) E3-4 的 H1（见[实现拆分](../../docs/domains/quality.md#实现拆分)）：判定表、`Observe`、健康度、首字延迟、吞吐，路由快照读它们。熔断、试探、表和持久化是 H2 起的事，现在没有。

## 接口

```go
func New(meter metric.Meter) *Monitor // meter 为 nil 时不报指标
func (m *Monitor) Observe(o Observation) // 每次尝试结束时一次；0 分配
func (m *Monitor) Measured(id suppliers.EndpointID, now time.Time) Measures
```

- **谁调**：gateway 的尝试在 `close` 里调 `Observe`，每次尝试恰好一次，含流式阶段（`attempt.observe`）；`RouteDeps.Build` 每次重建快照时对每个端点调 `Measured`，填进 `routing.Endpoint` 的 `Health`、`Throughput`、`Latency`。两边用的是同一个 `Monitor`，由 `cmd/axiom` 建一个交给两边。
- **`Observation` 是事实，不是结论**：适配器的 `*suppliers.Failure`（没有 2xx 时）、流读失败的 `protocol.Fault`、网关为什么取消了尝试（`Cut`：响应头超时、空闲超时、调用自己的原因）、客户端是否已走、是否正常结束、是否 200 无正文、预估输入 token（`billing.InputEstimate`）、输出 token（结算用的数，上游没报时是估算）、发出、首段内容、结束的时刻。判定只在本模块（`judge`），网关不预先下结论。
- **`Measures`**：`Health` = 1 − 失败的加权平均（α 0.2，没有计入的结局时为 1）；`Latency`、`Throughput` 在"测过"（样本 ≥ 5 且最近一个在 30 分钟内）之前为 0，路由按 0 认作没测过。
- **指标**：`axiom.quality.unclassified`（端点范围、状态码 < 500 的 `Unknown`）、`axiom.quality.suspected_block`（疑似出口拦截），都不带端点标签。

## 不变量

| 不变量 | 怎么保证 |
|---|---|
| H-I4 只有判定表里"失败"的结局记为失败，"不计"的不进健康度 | `judge` 是唯一的判定；`TestJudge` 逐行覆盖：每个 `Cause` × 作用范围、客户端是否已离开、流内错误类别（`protocol.Fault` 的每个值）、预估输入是否超过阈值；`TestUncountedChangesNothing`：不计的结局不改变健康度（既不降、也不重置）；`TestEveryAttemptIsObservedOnce`：网关的每条尝试结束路径（5xx、429、响应头超时、200 无正文、提交前后断流、空闲、客户端提交前后离开、compact）恰好报一次、按表计 |
| `Observe` 不分配 | `BenchmarkObserve` 先建好端点的条目，再用 `testing.AllocsPerRun` 断言 0，否则失败 |
| 共享状态有锁 | 端点表由 `RWMutex` 保护，每个端点的数由它自己的锁保护；`TestObserveConcurrently` 在竞态检测下并发 `Observe` 和 `Measured` |

## 判定的输入从哪来

- **作用范围和原因**：适配器的分类（[suppliers 的「调用面」](../suppliers/README.md#调用面)）。只有端点范围的能计；suppliers 把原因的类型都以别名公开（`suppliers.UpstreamError` 等），本模块按它们穷尽地判（`gochecksumtype`），加一个原因就要在这里决定它算不算。
- **流内错误的类别**：`protocol.FaultOf`。三种格式的出错事件按 `error.type`、`error.code`、`code`、`response.error.code` 归类，工具调用参数不是 JSON 是单独的一类；其余读失败（断流、格式错）是 `FaultBroken`。类别只给本模块判健康用，提交前换不换端点不看它（那是 #311 E2-4）。
- **超时**：网关的 `cutOf` 从尝试的 context 原因读出：`errHeaderWait` → `HeaderWait`，`errIdle` → `Idle`，其余（调用最长时间、退出、追加没批准、客户端离开）→ `Ours`。

## 决策与理由

| 决策 | 理由 |
|---|---|
| 健康度、延迟、吞吐在快照重建时按值拷进 `routing.Endpoint`，不是指针 | 端点评估的设计让快照持有指向实时状态的指针，是为了熔断在下一次 `Plan` 就生效（H2）；健康度只影响均衡的权重和粘性，晚一次重建（≤ 5 秒）无害，按值拷贝不用改 routing 的类型和全部测试。H2 加熔断时再给快照加指针，健康度可以随它一起改读指针 |
| 平均值的第一个样本直接作为平均值，错误率从 0 起 | 与 sub2api 相同（`openai_account_scheduler.go` 的 `report`：首字时间以 NaN 起、第一个样本直接写入；错误率以 0 起）：没有样本时健康度是 1，第一个延迟样本不被一个任意的初值拉偏 |
| 阈值（4k、32k、50 token、5 个样本、30 分钟、α）是代码里的常量 | 设计说它们是后台配置、默认值在代码里；后台在 H3 才有，现在只有默认值 |
| 吞吐只取正常结束、且判为成功的尝试 | 设计规定"正常结束"；断流的尝试的输出 token 是估算，结束时刻也不是生成结束的时刻 |
| 判定按"客户端已走 → 调用自己的原因 → 适配器的失败 → 正常结束 → 空闲 → 流内类别"的顺序 | 前两项能解释它们之后的任何失败（取消引起的断流、我们的期限），先看它们才不会把取消算成端点的错；但已提交之后客户端离开是成功：端点当时在正常回答 |
