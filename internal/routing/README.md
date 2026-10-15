# routing

路由在调用路径上的接口：会话标识、回退路线、换不换端点的判定。路由的规则见[路由](../../docs/domains/routing.md)，一次调用的编排见 [gateway](../gateway/README.md)；这里只写下游代码要依赖的部分，快照的组装、排序策略的实现随代码补入本文。

> 本文先于代码写成，是 [Issue #26](https://github.com/TYCT-tyct/Axiom/issues/26) 在调用路径上的设计（PR R1–R3，见 [gateway 的「PR 拆分」](../gateway/README.md#pr-拆分)）；代码与本文不一致时，在同一个 PR 里改本文。

## 接口（草图）

```go
// 引用 catalog、protocol、suppliers、quality、billing、accounts
func (r *Router) SessionKey(acct accounts.AccountID, req *protocol.Request, body []byte) SessionKey // 只扫有界前缀
func (r *Router) Plan(in Input) (Plan, *Wait, error) // 纯内存；*NoEndpoint{Reasons, Permanent}
type Input struct {
	Admission accounts.Admission; Req *protocol.Request; Models []catalog.ModelID
	Override Override; Sticky suppliers.EndpointID
}
type Plan struct{ Entries []Entry; StartBy time.Time }
type Entry struct {
	Endpoint suppliers.EndpointID; Model catalog.ModelID
	Price billing.PriceRef; MaxOutput int64; Segmentable bool
}
type Wait struct{ RetryAfter time.Duration }
func Decide(f suppliers.Failure) Action // NextEntry | OtherCredential | ReturnToClient
```

- **`SessionKey`** 在准入之前算出，只扫请求体的有界前缀；gateway 用它读会话粘性，粘住的端点作为 `Input.Sticky` 传入。
- **`Plan`** 在准入事务的 `fn` 里调用，只读内存快照和请求，不查库、不访问缓存。预设的内容取快照，预设 ID、可用模型、等级取 `Admission`；快照陈旧超过 30s 返回错误（gateway 回 503）。路线最多 3 个条目，各带端点、模型、价格版本、输出上限和 `Segmentable`；`StartBy` 是准入后不再发起新尝试的时刻。
- **结果**：候选都满载返回 `*Wait`，gateway 回滚准入、在事务外排队；没有可用端点返回 `*NoEndpoint`，`Permanent` 为真（能力、上下文、格式、预设或覆盖过滤、无售价）时 gateway 回 404，否则（全部熔断、供应商余额、重试预算）回 503，见[接入的「错误」](../../docs/domains/gateway.md#错误)。当前价格版本里没有售价的端点（提价超过上限时）不进路线，并告警。
- **`Segmentable`**（按条目和这个请求算）= 端点能流式且实测传递取消 ∧ 该模型在该格式下推理进度可见 ∧ 请求没有隐藏推理 ∧ 超额额度不接近输出上限。是否传递取消见[计费的「客户端断开」](../../docs/domains/billing.md#客户端断开)。快照里带端点负载和实测的取消（R1）。
- **`Decide`** 是纯函数，按 `Failure` 的作用范围给出换条目、同端点换凭证或返回客户端，规则即[路由的「失败怎么办」](../../docs/domains/routing.md#失败怎么办)。提交后 gateway 不再调用它。

## 决策与理由

| 决策 | 理由 | 不选 |
|---|---|---|
| 回退判定是纯函数 `Decide` | 规则与路由文档的「失败怎么办」在一处，可以表驱动地测 | 由 gateway 按状态码判断 |
| `Segmentable` 在 `Plan` 里按条目和请求算 | 端点能否截断、请求是否隐藏推理都会改变它；预留按条目报价时就要知道 | 按端点固定 |
| 预设内容来自快照，预设 ID 来自 `Admission` | 不新增查库；改预设内容滞后至多一个快照陈旧上限 | 准入时读预设内容 |
