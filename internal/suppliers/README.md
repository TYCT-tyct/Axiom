# suppliers

供应商模块在调用路径上的接口：挑凭证、发请求、记尝试、写对账日志。适配器、同步、对账的要求见[供应商](../../docs/domains/suppliers.md)和[计费的「供应商成本」](../../docs/domains/billing.md#供应商成本)，一次调用的编排见 [gateway](../gateway/README.md)；这里只写下游代码要依赖的部分，适配器、同步和对账的代码结构随实现补入本文。

> 本文先于代码写成，是 [Issue #25](https://github.com/TYCT-tyct/Axiom/issues/25) 在调用路径上的设计（PR S8、S9，见 [gateway 的「PR 拆分」](../gateway/README.md#pr-拆分)）；代码与本文不一致时，在同一个 PR 里改本文。

## 接口（草图）

```go
// 引用 catalog、protocol、accounts。M1 直接用 platform/egress
func (s *Service) Acquire(ep EndpointID) (*Attempt, error) // 内存比较交换；ErrNoSlot
func (a *Attempt) Send(ctx context.Context, u protocol.Upstream) (*http.Response, *Failure) // 内含 Classify；Failure.Unknown 表示结果未知
func (a *Attempt) Done() AttemptReceipt // 幂等；释放占位；带凭证、端点、成本计划版本、已到的对账键
func (s *Service) Journal(e JournalEntry)                  // 非阻塞，进程内缓冲，每秒批量写
func (s *Service) Cost(r AttemptReceipt, u []catalog.Usage, at time.Time) AttemptCost // 纯内存：原生金额、单位价值、cost_flag
func (s *Service) RecordAttempts(ctx context.Context, tx pg.Tx, rows []AttemptRecord) error
type AttemptRecord struct{ RequestID; Seq; Receipt; Usage []catalog.Usage; Cost AttemptCost; PricedAt, FinishedAt time.Time }
// RecordLostRequest、ResolveLostRequest 同样接受调用方的 tx
type CallPolicy struct{ Streams, Cancels bool } // 适配器默认；端点实测值覆盖 Cancels
```

- **`Acquire`**：凭证的在途数小于上限才占位，一次原子比较交换；拿不到返回 `ErrNoSlot`，gateway 跳过这个条目，不算尝试。
- **`Send`**：用 `httptrace` 的 `WroteRequest` 区分请求写完之前的失败（确知未执行）和之后的失败（`Failure.Unknown`，结果未知）；非 2xx 由适配器归为 `Failure`。`Attempt` 自己保存响应头，`Send` 失败时 `Done()` 也带得出已经到的对账键。suppliers 和 egress 不暗中重试 POST。
- **对账键**只取上游的响应头：new-api 是 `X-Oneapi-Request-Id`；sub2api 是 `client:` 加 `X-Client-Request-Id`。
- **`Done()`** 没有参数，幂等：重复调用、取消与结束竞争都只释放一次占位。
- **`RecordAttempts`** 在 billing 的结算事务里一次写入这个请求的全部尝试（`ON CONFLICT DO NOTHING`），往返与尝试次数无关；`RecordLostRequest`、`ResolveLostRequest` 在 billing 的清理和迟到结算事务里调用。
- **`CallPolicy.Cancels`**：适配器给默认值（new-api 传递，sub2api 不传递），quality 按端点实测的值覆盖它，规则见[计费的「客户端断开」](../../docs/domains/billing.md#客户端断开)。

## 对账日志

进程崩溃后，清理要知道哪些尝试已经向客户端交付了内容、对账键是什么，才能按供应商记录收费（见[计费的「持有者与截止时间」](../../docs/domains/billing.md#持有者与截止时间)）。上游不接受调用方的幂等号，所以由本模块在请求进行中记下来：

- **何时追加**：进入 `Send` 时、拿到响应头里的对账键时、尝试提交（第一次向客户端写出内容）时，gateway 各调一次 `Journal`，追加一行到进程内缓冲：请求 ID、尝试序号、账户、端点、凭证、成本计划版本、`priced_at`、对账键、是否已提交。
- **怎么写**：后台每秒一条多行 `INSERT … ON CONFLICT`，在本模块的平台范围事务里，一个节点每秒一个事务，不在请求路径上。主键 `(request_id, attempt_seq)`；同一行只插入或补上对账键、已提交标记。写失败退避重试，不丢。
- **有界**：日志只对还没结算的请求有用。结算提交后（结算事务里已有对账键），这个请求还没写出的条目直接丢弃，所以缓冲至多是节点名额 × 3 次尝试 × 3 行，不会满。表里的行在结算后 2 天删除。

## 决策与理由

| 决策 | 理由 | 不选 |
|---|---|---|
| 每秒批量的对账日志，不在发请求前单独写；只有标了已提交的尝试能让清理向用户收费 | 每节点每秒一个事务，崩溃窗口从整个请求时长缩到最近一次成功写入之后，计费的清理结算才走得到。日志看不到之后的尝试，所以只认已提交的：提交后不再换端点，已提交的必是最后一次 | 每次尝试先写一行：往返随尝试数增长 |
