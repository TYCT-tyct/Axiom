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

`sub2api/`（适配器，`testdata/` 里是从 sub2api 0.2.16 录制并脱敏的样本）、`syncer/`（同步引擎）已有；后续 PR 增加其余适配器的包、`wire/`（与系统无关的路径和鉴权头）、同步、对账和挑凭证。

## 表

schema `suppliers`，迁移在 `internal/suppliers/internal/store/migrations/`（设计草稿里的 `store/migrations/` 按[总体架构](../../docs/architecture.md)的 `internal/` 规则改为此路径），版本表 `platform.goose_suppliers`。现有的是配置表；同步状态、限速桶、成本计划、单位价值、扣费记录和尝试成本随写它们的代码加入，迁移只增不改。

| 表 | 内容 | 键与约束 |
|---|---|---|
| `suppliers` | 归属（`owner_scope` 与 `site_id`、`account_id` 由 CHECK 保持一致）、`kind`、`origin`（`https://host[:port]`，小写）、`upstream_account`、原生单位与结算币种、管理出口、状态、`config_gen`、带符号的有限余额（`numeric` 的 NaN 和无穷被 CHECK 排除，倍率、阈值同样） | `UNIQUE (origin, upstream_account)` |
| `supplier_logins` | 账号凭证：`kind` 加一份加密值（密文、nonce、包裹的数据密钥、主密钥版本），`version` 供比较后再写 | PK `supplier_id` |
| `supplier_groups` | 上游的分组：`upstream_ref`、倍率、原始事实（jsonb）、是否选中、代号、是否仍存在 | `UNIQUE (supplier_id, upstream_ref)`、`UNIQUE (id, supplier_id)`、代号全局唯一且设定后不变 |
| `credentials` | 调用凭证：对方的 key ID、加密的 key、状态、冷却、并发上限、出口；状态为"绑定中"时还没读到 key | `UNIQUE (supplier_id, upstream_key_ref)` |
| `supplier_models` | 分组里的上游模型名、映射到的目录模型（空为待映射）、格式 | PK `(supplier_group_id, upstream_name)` |
| `supplier_aliases` | 供应商别名，分组可空（整个供应商有效） | `UNIQUE NULLS NOT DISTINCT (supplier_id, supplier_group_id, upstream_name)` |
| `endpoints` | 分组 × 模型：格式、成本状态（已知 / 未知 / 冲突）、成本计划编号、是否启用 | `UNIQUE (supplier_group_id, model_id)`；成本状态为已知必有成本计划编号 |

- **行级安全**：`suppliers` 的策略是 `platform.in_scope(site_id, account_id)`，平台的供应商只有平台范围看得见；其余各表不存归属列，只存 `supplier_id`，策略是"这个供应商对本事务可见"，所以插入也挡住"挂到看不见的供应商下"，也不会有与父对象不一致的标签。
- **复合外键**：凡同时存 `supplier_id` 和 `supplier_group_id` 的表，用 `(supplier_group_id, supplier_id) → supplier_groups (id, supplier_id)`，分组与行不可能属于不同供应商。别名的分组可空，空表示整个供应商。
- **授权**：应用角色没有任何 DELETE（移除供应商是状态，要删密文行的移除流程随它的代码加授权）；UPDATE 按列授予，不含 `id`、`supplier_id`、归属列、`kind`、`origin`、`upstream_account`：身份和凭证发往的地址不能改。
- **密钥**：表里没有明文列。加密值写进 `ciphertext`、`nonce`、`wrapped_dek`、`key_version` 四列，数据库只检查字段齐全和长度（nonce 12 字节、密文不短于 16 字节、包裹的数据密钥非空、版本号合法），**不能**分辨密文和别的字节：任何满足长度的明文也写得进去。加密由写入边界保证（只把 `secret.Sealed` 交给查询），值是否真实、是否属于这一行由 `secret.Open` 验证。`secret.Location` 的行 ID 在加密前就要知道，所以 `credentials.id` 由调用方生成，不用默认值。
- **跨模块引用**：`model_id` 是目录的 ID，没有外键；`cost_schedule_id` 的表还没有，也没有外键，加外键是增量迁移。

## 适配器

- **契约**（`adapter`）：`Adapter` 有 `Kind`、`Buckets`、`Detect`、`Open`、`Calls`；`Open` 返回只读的 `Manager`（账户、报价、key 状态、按对方 ID 读 key 原文、扣费记录分页；每笔记录带标准化用量 `catalog.Usage` 和"已报告的项"集合，没报告和报告为零不同，暂估才能按项、按请求锁定的单价校正）；`Calls` 返回 `CallQuirks`（`Formats`、`ChargeRef`、`Classify`、`Policy`）。写能力以后用单独的可选接口加入，能力就是实现了什么。
- **`Failure`** 是 `{Executed, Scope, Cause}`。`Cause` 是封闭的和类型（`gochecksumtype` 检查每个 switch 都处理了全部成员），成员只带数字和时间，**没有来自对方的文本**，所以回显了凭证的响应进不了日志和表。`Executed` 的零值是 `ExecutionUnknown`：没把握就按"可能执行了"处理，成本按估算记、由对账补正；`NotExecuted` 只给对方在执行前就拒绝的响应。`Transport` 只由通用调用层设置，适配器不得返回。作用范围比设计草稿多一个 `Endpoint`（限流、过载作用于一个分组和模型）。
- **`Client`** 是适配器唯一的出站入口，`Request` 声明占用的全部桶，`BucketSpec` 声明对方的限速；任一桶没有名额就不发，返回包装了 `ErrBucketsFull` 的错误，调用方推迟而不是当失败。基于 `rate_buckets` 的实现随同步引擎加入（需要 `rate_buckets` 表），现在只有接口。
- **注册表**（`registry`）：`adapters` 列表是唯一同时引用所有适配器的地方，一行一个，不用 `init()`。`registry.New` 在启动时拒绝不合法的 `kind`、重复的 `kind`、不可能放行请求的限速桶。
- **移除**：删适配器的目录和注册表里它的那一行。`just adapters-removable` 对注册表里的每个适配器，用 `go build -overlay` 在不改工作区的前提下删掉二者，再跑 `go build ./...` 和 `go vet ./...`；还有别的代码依赖它时失败，并给出编译器的原话。现在注册表是空的，检查只证明了机制（测试用一个临时的小仓库跑通"可移除"和"有人依赖"两种），第一个适配器加入时开始守住真实的代码。
- **契约测试**（`adaptertest`）：每个适配器的测试都调用 `adaptertest.Run`，检查类型签名表达不了的规则：`Classify` 对 400 到 599 的每个状态码和各种正文（空、截断、超长、HTML 页、认不出的错误）都不 panic、都有原因；认不出的响应、所有 5xx 都不是 `NotExecuted`；HTML 页是"疑似出口拦截"且作用于出口；`Unknown` 带着响应的状态码；`ChargeRef` 在没有头时不返回值；`Formats` 对坏的事实不 panic、不重复。
- **启动检查**：`CheckAdapters` 在 `serve` 启动时列出仍在使用（状态不是"已移除"）而本程序没有注册适配器的供应商，有就拒绝启动并逐个指出名字和 `kind`。它要看到全部供应商，所以由 `cmd/axiom` 传入平台范围。

## sub2api

适配器在 `sub2api/`，`kind` 是 `sub2api`。事实来自 0.2.16 的官方镜像（本地实测，2026-10-15）和只读的源码调研（#25）。

- **账号凭证**：`Login`（邮箱、密码、访问令牌、刷新令牌、过期时间和有效期），编码成带版本号的明文 JSON 交给 `LoginStore` 加密；解码拒绝未知成员；`Secret` 的打印一律脱敏。sub2api 没有个人访问令牌，刷新令牌**只能用一次**。
- **保持登录**：访问令牌只剩四分之一寿命时刷新；对方说刷新令牌无效（`REFRESH_TOKEN_INVALID`）才用密码重新登录；刷新结果不明（网络）时不换密码，留给下一轮（令牌若已用掉，下一轮会被拒绝，再重新登录）。写回用 `Swap` 比较版本，失败（别的持有者先写）就改用已存的，不再发请求。对方说账号被撤销、停用、会话绑定变化（`TOKEN_REVOKED`、`USER_INACTIVE`、`SESSION_BINDING_MISMATCH`），或密码被拒、要二步验证，返回 `ErrAuthRejected`：不再登录，避免封号。调用中途令牌过期只续一次。
- **桶**：登录 5/分、刷新 7/分（按 IP），面板 60/分、用量 15/分（按账号），是对方默认值的四分之一；每个请求声明所占的桶，测试核对。
- **读取**：余额 `/api/v1/auth/me`，有生效订阅即资金来源不是钱包（`/subscriptions/active`）；分组 `/groups/available`、用户倍率 `/groups/rates`；每个分组用其中一把可用的 key 读 `/v1/models` 和 `/v1/sub2api/billing` 的 `resolved_rate_multiplier`（含高峰）；key 的余额为 0 时列不了模型，该组标 `ModelsUnread`，保留上次的模型。key 列表按 `/api/v1/keys` 分页，`KeySecret` 按 ID 取（ID 只许数字）。
- **价格**：默认安装里普通用户看不到价格，模型的成本一律是空（未知），不上架，直到人确认底价；`adapter.ScaleSchedule` 把目录官方价乘分组倍率做成草稿（币种写 `XXX`，单位是对方的美元）。能公布价格的 `channels/available` 尚未读取（实例里关着，没有样本）。
- **调用面**：`anthropic` 分组服务 Messages，`openai` 分组服务 Responses 与 Chat；订阅分组、高峰分组、其他平台不服务。对账键是响应头 `X-Client-Request-Id` 加 `client:` 前缀，等于扣费记录的 `request_id`（实测）；对方自己生成，不读我们传的。`Classify` 只读错误代码，不留对方的文字；`Cancels` 为否（对方把上游读完并全额扣费，实测）。
- **扣费记录的扫描**：`/api/v1/usage` 只认日期，所以一遍按 key 逐把读，新的在前、按页翻；起止日取窗口两端的日期（跨午夜两天都读），每页 200 条（每条还带着 key、用户和分组，页太大会超响应上限），一把 key 在短页或最旧一条早于窗口起点时结束，全部 key 结束才算读完。记录的用量映射到不重叠的计费项（输入、输出、缓存读、5 分钟和 1 小时缓存写），**只有记录里写了的项算已报告**；非 token 计费（按次、图片）带 `UnknownUsage`；`billing_type` 不是 0 的不是钱包。记录里的 key 原文、用户、地址一概不读。
- **未验证**：key 的重置时间字段（`reset_*_at`）没有样本；分页 `total` 是否精确；订阅分组的记录形态。

## 同步

`syncer/` 是每个供应商一份的同步引擎，只做决定，不自己存任何东西：状态（`State`）由 `Store` 整份读出、整份提交，每一步的事件、成本计划版本和扣费记录与状态在同一次提交里，提交带读到的版本（栅栏），别的写入者先提交则这一步整个作废（`ErrFenced`）。租约、数据库事务、River 的调度和对应的表随后加入，接在 `Store` 上。

- **轮次**：`Round` 按顺序执行到期的任务（余额 1 分钟、目录 5 分钟、key 状态 5 分钟、扣费记录 2 分钟，默认值在 `Defaults`）。每个任务各提交一次。
- **失败**：网络错误和 5xx 指数退避（1 分钟起、上限 30 分钟、可加抖动），期间不调用，别的任务照跑，成功即清零；桶不够（`ErrBucketsFull`）不算失败，到 `Blocked.Until` 再来；响应认不出（`ErrUnrecognised`）只停这个任务，什么也不写，发事件；**鉴权被拒（`ErrAuthRejected`）停掉这个供应商的全部轮询**，状态"需处理"，发一个事件，之后的轮次什么也不调用。
- **变化即事件**：目录规范化后与上次比较，只把差异写成事件（组、模型的增减，倍率变化，成本计划新版本）；同样的响应读两次没有新事件、新计划。成本计划 = 对方公布的价，或人确认的底价乘当前倍率；指纹变了才写新版本。
- **扣费记录**：一遍的窗口是（水位 − 5 分钟）到（现在 − 30 秒），第一遍往回 24 小时；每一页连同它之后的位置在一次提交里存下，被限速、出错、重启后从这个位置接着读；读完才把水位推进到窗口末尾，水位只增不减；记录按对方的请求编号去重。资金来源不是钱包的记录让供应商进入"需处理"。
- **可路由**：`EndpointFacts.Check` 逐条判断（这份清单是设计草稿 #25 的第 4 节，本文是它的落地处）：供应商正常、资金是钱包且余额减预留大于 0、分组被选中、模型已映射、成本已知、至少一个可用凭证；给出第一个不满足的原因。供应商状态决定的几条由 `State.Facts` 填，其余由分组、映射、凭证的表补。余额低于告警线发一次 `BalanceLow`，回升后才会再发。
- **移除**：`Remove` 是持久化的分步状态，每一步先提交再做下一步，可重复调用：离开快照（配置代次加一）→ 等在途尝试结束（最多 5 分钟）→ 读最后一遍扣费记录 → 等 30 分钟宽限 → 再读一遍 → 删除加密凭证（`DeleteSecrets` 必须可重复执行）→ 完成。完成时的事件列出这家供应商的 key，由人到对方后台吊销（M1 不对对方做写操作）。账号已被拒绝则跳过读取。
- **尚未做**：每天对最近 24 小时的补扫、按需的"无逐笔记录"判定、成本低估下架（随对账）。

## 不变量

| 不变量 | 靠什么保证 |
|---|---|
| 密钥明文不落库 | 表里没有明文列，加密列只由 CHECK 约束长度（不证明是密文）；加密由写入边界保证，真实性由 `Open` 验证；测试在整行、全部列里搜索 key 的字节（文本和十六进制），并且加密值只能在自己那一行打开。**尚未覆盖**：写入处只传 `secret.Sealed`，由第一个适配器的编解码函数保证，随它的 PR 加测试 |
| 供应商只在自己的范围内可见、可写；子表经父表判断 | 策略；测试覆盖无范围、平台、站点、账户、另一个站点或账户，以及六张子表各自的插入 |
| 分组、凭证、模型、别名、端点属于同一个供应商 | 复合外键；测试对四张表各试一次跨供应商引用 |
| 身份和发往的地址不能改，不能删除 | 按列授权、不授 DELETE；测试逐列、逐表 |
| 成本未知的端点不会被当成已知 | `known_has_schedule` CHECK；成本状态进快照的条件在后续的快照查询里 |
| 对方的文字不进错误、日志和表 | sub2api 的失败路径只带状态码和白名单内的代码；测试让响应回显一个凭证，核对每个读取的错误里都没有它；录制样本里的令牌、key、邮箱、地址、请求编号都是占位符 |
| 对方撤销账号后不再登录或轮询 | `ErrAuthRejected` 后适配器不再发登录，引擎把供应商置为"需处理"；测试数发出的请求 |
| 同一个响应同步两次没有新事件 | 目录以规范化内容比较；测试 |
| 一次移除的每一步都可重复，效果只有一次 | `Remove` 的步骤持久化；测试对每一次提交失败和每个钩子失败重跑，核对事件、代次 |
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
