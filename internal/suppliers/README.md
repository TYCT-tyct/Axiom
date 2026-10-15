# suppliers

供应商模块：供应商及其账号凭证、分组、模型、端点的表，适配器的契约和注册，以及调用路径上的接口（挑凭证、发请求、记尝试、写对账日志）。适配器、同步、对账的要求见[供应商](../../docs/domains/suppliers.md)和[计费的「供应商成本」](../../docs/domains/billing.md#供应商成本)，一次调用的编排见 [gateway](../gateway/README.md)。

> [Issue #25](https://github.com/TYCT-tyct/Axiom/issues/25) 分多个 PR 实现。已有代码的部分（表、适配器契约、注册表、启动检查）写在「代码结构」到「不变量」；「接口（草图）」和「对账日志」是调用面的设计（PR S8、S9，见 [gateway 的「PR 拆分」](../gateway/README.md#pr-拆分)），先于代码写成。代码与本文不一致时，在同一个 PR 里改本文。

## 代码结构

```
internal/suppliers/                 公开接口：Module()、CheckAdapters()；调用面的类型随调用面加入
internal/suppliers/internal/
  adapter/       契约：Adapter、Manager、CallQuirks、Failure、CallPolicy、Client、BucketSpec
  registry/      注册表：唯一同时引用所有适配器的文件，一个适配器一行；按 kind 查找
  adaptertest/   每个适配器都要过的契约测试（只被测试引用）
  store/         迁移、查询（sqlc 生成）
  removable/     `just adapters-removable` 的程序
```

后续 PR 在 `internal/` 下增加各适配器的包（`newapi/`、`sub2api/`，带录制样本）、`wire/`（与系统无关的路径和鉴权头）、同步、对账和挑凭证。

## 表

schema `suppliers`，迁移在 `internal/suppliers/internal/store/migrations/`（设计草稿里的 `store/migrations/` 按[总体架构](../../docs/architecture.md)的 `internal/` 规则改为此路径），版本表 `platform.goose_suppliers`。现有的是配置表；同步状态、限速桶、成本计划、单位价值、扣费记录和尝试成本随写它们的代码加入，迁移只增不改。

| 表 | 内容 | 键与约束 |
|---|---|---|
| `suppliers` | 归属（`owner_scope` 与 `site_id`、`account_id` 由 CHECK 保持一致）、`kind`、`origin`（`https://host[:port]`，小写）、`upstream_account`、原生单位与结算币种、管理出口、状态、`config_gen`、带符号的余额 | `UNIQUE (origin, upstream_account)` |
| `supplier_logins` | 账号凭证：`kind` 加一份加密值（密文、nonce、包裹的数据密钥、主密钥版本），`version` 供比较后再写 | PK `supplier_id` |
| `supplier_groups` | 上游的分组：`upstream_ref`、倍率、原始事实（jsonb）、是否选中、代号、是否仍存在 | `UNIQUE (supplier_id, upstream_ref)`、`UNIQUE (id, supplier_id)`、代号全局唯一且设定后不变 |
| `credentials` | 调用凭证：对方的 key ID、加密的 key、状态、冷却、并发上限、出口；状态为"绑定中"时还没读到 key | `UNIQUE (supplier_id, upstream_key_ref)` |
| `supplier_models` | 分组里的上游模型名、映射到的目录模型（空为待映射）、格式 | PK `(supplier_group_id, upstream_name)` |
| `supplier_aliases` | 供应商别名，分组可空（整个供应商有效） | `UNIQUE NULLS NOT DISTINCT (supplier_id, supplier_group_id, upstream_name)` |
| `endpoints` | 分组 × 模型：格式、成本状态（已知 / 未知 / 冲突）、成本计划编号、是否启用 | `UNIQUE (supplier_group_id, model_id)`；成本状态为已知必有成本计划编号 |

- **行级安全**：`suppliers` 的策略是 `platform.in_scope(site_id, account_id)`，平台的供应商只有平台范围看得见；其余各表不存归属列，只存 `supplier_id`，策略是"这个供应商对本事务可见"，所以插入也挡住"挂到看不见的供应商下"，也不会有与父对象不一致的标签。
- **复合外键**：凡同时存 `supplier_id` 和 `supplier_group_id` 的表，用 `(supplier_group_id, supplier_id) → supplier_groups (id, supplier_id)`，分组与行不可能属于不同供应商。别名的分组可空，空表示整个供应商。
- **授权**：应用角色没有任何 DELETE（移除供应商是状态，要删密文行的移除流程随它的代码加授权）；UPDATE 按列授予，不含 `id`、`supplier_id`、归属列、`kind`、`origin`、`upstream_account`：身份和凭证发往的地址不能改。
- **密钥**：表里没有明文列。加密值写进 `ciphertext`、`nonce`、`wrapped_dek`、`key_version` 四列，列的长度约束是 AES-256-GCM 输出的形状（nonce 12 字节、密文不短于 16 字节的标签），不是 `secret.Seal` 产出的值写不进去。`secret.Location` 的行 ID 在加密前就要知道，所以 `credentials.id` 由调用方生成，不用默认值。
- **跨模块引用**：`model_id` 是目录的 ID，没有外键；`cost_schedule_id` 的表还没有，也没有外键，加外键是增量迁移。

## 适配器

- **契约**（`adapter`）：`Adapter` 有 `Kind`、`Buckets`、`Detect`、`Open`、`Calls`；`Open` 返回只读的 `Manager`（账户、报价、key 状态、按对方 ID 读 key 原文、扣费记录分页）；`Calls` 返回 `CallQuirks`（`Formats`、`ChargeRef`、`Classify`、`Policy`）。写能力以后用单独的可选接口加入，能力就是实现了什么。
- **`Failure`** 是 `{Executed, Scope, Cause}`。`Cause` 是封闭的和类型（`gochecksumtype` 检查每个 switch 都处理了全部成员），成员只带数字和时间，**没有来自对方的文本**，所以回显了凭证的响应进不了日志和表。`Executed` 的零值是 `ExecutionUnknown`：没把握就按"可能执行了"处理，成本按估算记、由对账补正；`NotExecuted` 只给对方在执行前就拒绝的响应。`Transport` 只由通用调用层设置，适配器不得返回。作用范围比设计草稿多一个 `Endpoint`（限流、过载作用于一个分组和模型）。
- **`Client`** 是适配器唯一的出站入口，`Request` 声明占用的全部桶，`BucketSpec` 声明对方的限速；任一桶没有名额就不发，返回包装了 `ErrBucketsFull` 的错误，调用方推迟而不是当失败。基于 `rate_buckets` 的实现随同步引擎加入（需要 `rate_buckets` 表），现在只有接口。
- **注册表**（`registry`）：`adapters` 列表是唯一同时引用所有适配器的地方，一行一个，不用 `init()`。`registry.New` 在启动时拒绝不合法的 `kind`、重复的 `kind`、不可能放行请求的限速桶。
- **移除**：删适配器的目录和注册表里它的那一行。`just adapters-removable` 对注册表里的每个适配器，用 `go build -overlay` 在不改工作区的前提下删掉二者，再跑 `go build ./...` 和 `go vet ./...`；还有别的代码依赖它时失败，并给出编译器的原话。现在注册表是空的，检查只证明了机制（测试用一个临时的小仓库跑通"可移除"和"有人依赖"两种），第一个适配器加入时开始守住真实的代码。
- **契约测试**（`adaptertest`）：每个适配器的测试都调用 `adaptertest.Run`，检查类型签名表达不了的规则：`Classify` 对 400 到 599 的每个状态码和各种正文（空、截断、超长、HTML 页、认不出的错误）都不 panic、都有原因；认不出的响应、所有 5xx 都不是 `NotExecuted`；HTML 页是"疑似出口拦截"且作用于出口；`Unknown` 带着响应的状态码；`ChargeRef` 在没有头时不返回值；`Formats` 对坏的事实不 panic、不重复。
- **启动检查**：`CheckAdapters` 在 `serve` 启动时列出仍在使用（状态不是"已移除"）而本程序没有注册适配器的供应商，有就拒绝启动并逐个指出名字和 `kind`。它要看到全部供应商，所以由 `cmd/axiom` 传入平台范围。

## 不变量

| 不变量 | 靠什么保证 |
|---|---|
| 密钥明文不落库 | 表里没有明文列，加密列的形状由 CHECK 约束；测试在整行、全部列里搜索 key 的字节（文本和十六进制），并且加密值只能在自己那一行打开。**尚未覆盖**：写入处只传 `secret.Sealed`，由第一个适配器的编解码函数保证，随它的 PR 加测试 |
| 供应商只在自己的范围内可见、可写；子表经父表判断 | 策略；测试覆盖无范围、平台、站点、账户、另一个站点或账户，以及六张子表各自的插入 |
| 分组、凭证、模型、别名、端点属于同一个供应商 | 复合外键；测试对四张表各试一次跨供应商引用 |
| 身份和发往的地址不能改，不能删除 | 按列授权、不授 DELETE；测试逐列、逐表 |
| 成本未知的端点不会被当成已知 | `known_has_schedule` CHECK；成本状态进快照的条件在后续的快照查询里 |
| 失败不带对方的文本，没把握不算免费 | `Cause` 的成员；`adaptertest` |
| 正常供应商一定有注册的适配器；删任一适配器其余照常编译 | `CheckAdapters`；`just adapters-removable` |

## 接口（草图）

调用面的接口，还没有代码。`Failure` 已经在 `adapter` 里（见上文），`Send` 返回的就是它。

```go
// 引用 catalog、protocol、accounts。M1 直接用 platform/egress
func (s *Service) Acquire(ep EndpointID) (*Attempt, error) // 内存比较交换；ErrNoSlot
func (a *Attempt) Send(ctx context.Context, u protocol.Upstream) (*http.Response, *Failure) // 内含 Classify
type Failure = adapter.Failure // {Executed, Scope, Cause}；Executed：ExecutionUnknown（零值）| NotExecuted
func (a *Attempt) Done() AttemptReceipt // 幂等；释放占位；带凭证、端点、成本计划版本、已到的对账键
func (s *Service) Journal(e JournalEntry)                  // 非阻塞，进程内缓冲，每秒批量写
func (s *Service) JournalFlushed(id platform.RequestID) <-chan struct{} // 调用之前这个请求已追加的条目都落库后关闭；只在它不会再追加之后调用才代表全部条目
func (s *Service) Settled(id platform.RequestID)          // 结算提交后调用：丢弃这个请求还没写出的条目
func (s *Service) Cost(r AttemptReceipt, u []catalog.Usage, at time.Time) AttemptCost // 纯内存：原生金额、单位价值、cost_flag
func (s *Service) RecordAttempts(ctx context.Context, tx pg.Tx, rows []AttemptRecord) error
type AttemptRecord struct{ RequestID; Seq; Receipt; Usage []catalog.Usage; Cost AttemptCost; PricedAt, FinishedAt time.Time }
// RecordLostRequest、ResolveLostRequest 同样接受调用方的 tx
type CallPolicy struct{ Streams, Cancels bool } // 适配器默认；端点实测值覆盖 Cancels
```

- **`Acquire`**：凭证的在途数小于上限才占位，一次原子比较交换；拿不到返回 `ErrNoSlot`，gateway 跳过这个条目，不算尝试。
- **`Send`** 给每个失败标明上游是否执行过（`Failure.Executed`）：用 `httptrace` 的 `WroteRequest` 判断，请求写完之前失败是 `NotExecuted`；写完之后，非 2xx 由适配器的 `Classify` 归类，只有它声明该供应商在执行前就拒绝的状态（例如参数校验的 400、401、403、余额不足的 402、429）是 `NotExecuted`，其余（5xx、504、认不准的）和没拿到响应都是 `Unknown`：供应商成本按估算记、由对账补正，向用户怎么收见[计费的「免单规则」](../../docs/domains/billing.md#免单规则)。`Attempt` 自己保存响应头，`Send` 失败时 `Done()` 也带得出已经到的对账键。suppliers 和 egress 不暗中重试 POST。
- **对账键**只取上游的响应头：new-api 是 `X-Oneapi-Request-Id`；sub2api 是 `client:` 加 `X-Client-Request-Id`。
- **`Done()`** 没有参数，幂等：重复调用、取消与结束竞争都只释放一次占位。
- **`RecordAttempts`** 在 billing 的结算事务里一次写入这个请求的全部尝试（`ON CONFLICT DO NOTHING`），往返与尝试次数无关；`RecordLostRequest`、`ResolveLostRequest` 在 billing 的清理和迟到结算事务里调用。
- **`CallPolicy.Cancels`**：适配器给默认值（new-api 传递，sub2api 不传递），quality 按端点实测的值覆盖它，规则见[计费的「客户端断开」](../../docs/domains/billing.md#客户端断开)。

## 对账日志

进程崩溃后，清理要知道哪些尝试已经向客户端交付了内容、对账键是什么，才能按供应商记录收费（见[计费的「持有者与截止时间」](../../docs/domains/billing.md#持有者与截止时间)）。上游不接受调用方的幂等号，所以由本模块在请求进行中记下来：

- **何时追加**：进入 `Send` 时、拿到响应头里的对账键时、尝试提交（第一次向客户端写出内容）时，gateway 各调一次 `Journal`，追加一行到进程内缓冲：请求 ID、尝试序号、账户、端点、凭证、成本计划版本、`priced_at`、对账键、是否已提交。
- **怎么写**：后台每秒一条多行 `INSERT … ON CONFLICT`，在本模块的平台范围事务里，一个节点每秒一个事务，不在请求路径上。主键 `(request_id, attempt_seq)`；同一行只插入或补上对账键、已提交标记。写失败退避重试，不丢。
- **有界**：日志只对还没结算的请求有用。结算提交后（结算事务里已有对账键），gateway 调 `Settled`，这个请求还没写出的条目直接丢弃。一个请求的节点名额在它执行期间和结算后台重试期间一直占着，只在结算提交，或交给清理（结算永久性错误、到截止时间放弃重试）且执行器已停止上游、不再追加条目、不再重试结算、`JournalFlushed` 已关闭之后才释放（[gateway 的「并发、背压与共享状态」](../gateway/README.md#并发背压与共享状态)）。所以缓冲里只有占着名额的请求的条目，至多是节点名额 × 3 次尝试 × 3 行。日志持续写不进时，交给清理的请求名额不释放，节点占满后新请求得 503，这就是上界。表里的行在结算后 2 天删除。
- **测试**：日志持续写失败，同时有请求结算永久失败：它们的名额不释放，节点占满后返回 503，缓冲始终不超过节点名额 × 9 行；日志恢复后条目落库、名额释放。请求仍在执行时它的日志已全部落库，名额不释放。结算一直暂时性失败、仍在后台重试时日志已全部落库，名额不释放。

## 决策与理由

| 决策 | 理由 | 不选 |
|---|---|---|
| 每秒批量的对账日志，不在发请求前单独写；只有标了已提交的尝试能让清理向用户收费 | 每节点每秒一个事务，崩溃窗口从整个请求时长缩到最近一次成功写入之后，计费的清理结算才走得到。日志看不到之后的尝试，所以只认已提交的：提交后不再换端点，已提交的必是最后一次 | 每次尝试先写一行：往返随尝试数增长 |
