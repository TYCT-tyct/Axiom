# observability

调用记录的代码接口：写什么、怎么写、怎么读、数据怎么放。要不要记、记多久、谁能看是[记录与告警](../../docs/domains/observability.md)的事，本文只写代码里的做法和理由。指标与链路的基座在 [platform 的「遥测」](../platform/README.md#遥测)，不在本模块。

目前实现的是调用与尝试记录。对话内容、分析标签、受控导出、告警与通知渠道随各自的 Issue 加入，不在本文。

## 接口

```go
func Module() pg.Module                                   // 迁移，交给 cmd/axiom
func NewRecorder(ctx, db *pg.DB, scope pg.Scope, cfg Config, logger *slog.Logger) *Recorder // Config 的各项都必须为正，否则 panic
func (r *Recorder) Enqueue(rec CallRecord) error          // 从不等待；队列满返回 ErrQueueFull 并计入丢失
func (r *Recorder) Close(ctx context.Context) error       // 写完队列里的，ctx 到期则报告丢了几条（含入队时丢的）
func (r *Recorder) Done() <-chan struct{}                 // 写入协程退出时关闭；Close 返回前已等待它，测试据此断言没有写入协程留下
func ListOwn(ctx, tx pg.Tx, q OwnQuery) ([]OwnCall, *Cursor, error)
func GetOwn(ctx, tx pg.Tx, id uuid.UUID) (OwnCall, error)
func GetTrace(ctx, tx pg.Tx, id uuid.UUID) (CallRecord, error) // 一条调用、它的尝试和 route，按请求 ID；只从运营或管理的处理器调用，事务须是平台范围
func (c Cursor) String() string; func ParseCursor(s string) (Cursor, error) // 控制台翻页用的不透明游标；格式不对 ErrBadCursor
func HashEndUser(key []byte, id string) EndUserHash       // 带密钥的哈希；EndUserHash 的数组是私有的，包外只能经它得到非零值，零值表示没有终端用户；目前没有生产调用者，由 [#29](https://github.com/TYCT-tyct/Axiom/issues/29) 写 `end_user_hash` 时接上
var ErrClosed, ErrQueueFull error                         // Enqueue 在 Close 之后、或在队列满时给出的错误
```

`CallRecord` 带一个请求的元数据和它的每次尝试（`AttemptRecord`）；`call_log` 一行是调用，`call_attempt` 一行是尝试。尝试的 `Seq` 从 1 连续到最多 3，写入时校验；调用行的 `endpoint_id`、`source_class`、`egress`、`attempt_count` 由最后一个尝试算出，因此不会与尝试表不一致。查询和生成的代码在 `internal/store`（sqlc，`sqlc.yaml` 的 `observability` 项），包外引用不到，所以所有写入都经过 `CallRecord`、`AttemptRecord`、`Recorder`，所有读取都经过下面按账户的查询和「追溯」的 `GetTrace`。`CallRecord` 只用本模块、`catalog`、`platform` 的类型和 `uuid.UUID`（`accounts` 的带类型 ID 随它的表加入后换上）；`AttemptRecord` 用 `uuid.UUID`、`platform.AttemptSeq` 和时间。HTTP 处理器随 gateway 加入，调用的就是 `ListOwn` 和 `GetOwn`；尝试不向用户开放（用户看不到端点、成本和路由）。

## 表（schema `observability`）

`call_log`：一个请求一行，按请求开始时间 `at` 分区，主键 `(id, at)`，`id` 就是请求 ID（调用编号）。

- **没有 NULL**：可选的文字是 `''`，没有产出首字时 `first_token_ms` 是 0，没有尝试时 `endpoint_id` 是全零 UUID。生成的代码因此不需要可空类型。
- **谁**：`site_id`、`account_id`（行级安全看这两列）、`workspace_id`、`key_id`、`end_user_hash`（HMAC-SHA256 的十六进制）、`ip`。
- **走了哪**：`model`（客户端要的公开名字）；`endpoint_id`、`source_class`、`egress`、`attempt_count` 都取自最后一个 `AttemptRecord`（没有尝试时分别是全零 UUID、`''`、`''`、0）；`route`（路由过程，≤ 8 KiB 的 JSON 对象，结构由 routing 定）。**每次尝试在 `call_attempt`**，供应商的成本和对账键仍在 suppliers，按同一个请求 ID 和尝试序号关联；账本交易不抄在这里，按请求 ID 从 billing 读（见「追溯」）。
- **结果**：`usage`（计费项名字到数量的 JSON）、`service_tier`、`price_nano`、`cost_nano`（计费的最小单位）、`currency`、`free_of_charge`、`latency_ms`、`first_token_ms`、`finish_reason`、`error_kind`。
- **权利**：`consent_version`、`training_allowed`，只能在写入时记下。
- **写入标签**：`client_app`、`session_id`、`subagent_id`、`request_class`、`language`、`used_tools`、`has_image`、`has_file`。分析标签随后台分类器加入，另表保存。

`call_attempt`：一次尝试一行，外键意义上的归属是调用，按调用的 `at` 分区，主键 `(request_id, at, attempt_seq)`。

- **谁**：`site_id`、`account_id`（与调用相同，行级安全看这两列）。
- **走了哪**：`endpoint_id`、`supplier_id`、`source_class`（official、cloud、relay）、`egress`（按名字）。
- **结果**：`outcome`（`committed`：内容已交给客户端；`failed`：没有；`unknown`：上游可能已执行）、`error_kind`、`started_at`、`finished_at`、`first_token_ms`。成本、用量和供应商的对账键不在这里重复：suppliers 的 `attempt_costs` 按 `(request_id, attempt_seq)` 保存，与计费同成同败；崩溃或超过截止时间的请求记在 `suppliers.lost_requests` 里。
- **没有 NULL，文字有界**：与 `call_log` 相同；没有正文、密钥或凭证列。

## 不变量

| 不变量 | 靠什么保证 |
|---|---|
| 租户只读写自己账户的行 | 两张表和每个分区强制行级安全，策略 `platform.in_scope(site_id, account_id)`；写入者在平台范围里（`pg.PlatformScope()` 由 cmd 交给 `NewRecorder`）。测试：账户 A 读不到 B 的行也按 ID 取不到，A 的范围写不进 B 的调用或尝试 |
| 记录不可改、不可删 | `axiom_app` 只有 SELECT 和 INSERT；到期按分区整块处理，由持有表的角色执行。测试：两张表的 UPDATE、DELETE、TRUNCATE 被拒 |
| 用户看不到成本、端点和路由 | `OwnCall` 没有这些字段；尝试表不向用户开放，查询也不选尝试（[接入与对外接口的「用量与费用」](../../docs/domains/gateway.md#用量与费用)） |
| 文字能存进库 | `Enqueue` 拒绝不合法的 UTF-8 和 NUL，包括每次尝试的文字；编码本身失败（`errEncoding`）和数据库的数据错误一样走坏行隔离。测试：非法 UTF-8 的行夹在好行之间，经真实的编码和库写入，好行落库、坏行被计数 |
| 终端用户标识只以带密钥的哈希存放 | `EndUserHash` 是带私有数组的结构，包外无法构造非零值 |
| 没有对话正文、密钥或凭证 | 两张表的列里没有这样的名字或字节类型，文字和 JSON 列都有长度或格式检查；测试读 `information_schema` 核对两张表。请求头一律不记 |
| 每次尝试有且只有一行，且能从调用找到 | `call_attempt` 的主键 `(request_id, at, attempt_seq)`；`Seq` 必须从 1 连续递增，最多 3 次；调用行的端点、来源类别、出口和次数由最后一个尝试算出。测试：写两次只有一行，按序号读回每次尝试，调用行与最后尝试一致 |
| 同一个请求写两次是一行 | 两张表都是 `ON CONFLICT … DO NOTHING`；`at` 是请求开始时间，不是写入时间，所以重试的是同一个键。测试：同一个 `CallRecord` 写两次，调用一行、每次尝试一行 |
| 写入从不因没有分区而失败 | 两张表都有默认分区兜底；测试写入 2001 年和 2090 年的调用和尝试 |
| 队列满不阻塞调用 | `Enqueue` 只做非阻塞发送；满了就丢掉这条调用和它的尝试，计入 `records.lost` / `attempts.lost`（reason `queue_full`）并返回 `ErrQueueFull`。测试：写协程卡住、队列满时第四次 `Enqueue` 立即返回，之前入队的照常写入 |
| 所有表和分区都过启动检查 | 每个分区各自强制行级安全并有策略（分区不继承）；测试 `DB.Check` 通过 |

## 写入

`Enqueue` 校验并把记录交给有界队列就返回，**从不等待**：队列满时丢掉这条调用和它的尝试，计入丢失数并返回 `ErrQueueFull`。调用方可以忽略这个错误，模块已经计过数；它留给测试和想自己记一笔的调用方。队列容量与节点在途名额相当（cmd 里是 5000），所以只有数据库远远落后、持续写完跟不上到达时才会丢。丢掉的条数在 `records.lost{reason="queue_full"}` 和 `attempts.lost{reason="queue_full"}` 里，`Close` 的错误也会算进去；这是告警的依据，而不是调用方的负担（[记录与告警的「写入路径」](../../docs/domains/observability.md#写入路径)）。

写入事务走 [platform 的普通连接池](../platform/README.md#连接池)，不占完成类预留；无需经过 gateway 的许可。后台一个写入协程，每 200 ms 或攒满 500 条调用写一批：一个事务里先写 `call_log`，再写 `call_attempt`，各是一条 `INSERT … SELECT FROM jsonb_array_elements($1)`，参数是整批行的一个 JSON 数组，成员名就是列名，类型和检查由表来做。先写调用再写尝试，重试也按这个顺序，所以不会出现有尝试、没有调用的批次；同一次写入里两者一起提交，调用总能找到它的尝试。每次写入有自己的期限（`WriteTimeout`，cmd 里 10 s），超时后算失败，退避重试（100 ms 起翻倍，上限 5 s，带抖动），重试到成功为止；超时的那次结果不明，重写同一批由 `ON CONFLICT` 兜底。一个不再应答的连接因此拖不住唯一的写入协程；整体仍由 `Close` 的期限控制。数据库说数据本身有问题（`pg.IsDataError`：SQLSTATE 22、23），或编码这批行失败（同样的行每次都失败）时不重试：把批一分为二找到那一条调用，记日志（请求 ID 和错误，不含内容）、连同它的尝试计入丢失数后跳过，否则一条坏记录会堵住后面所有记录。

`Close` 先拒绝新的 `Enqueue`，关队列，让写入协程写完剩下的；ctx 到期就停止重试，报告丢了几条（包括队列满时丢的）。cmd 的退出顺序是：监听停止并排空，`Close`，最后关数据库；启动失败等其他返回路径也会关闭它，不留写入协程。进程崩溃丢的是最近一次成功写入之后的记录（[记录与告警的「写入路径」](../../docs/domains/observability.md#写入路径)）。

指标：`axiom.observability.queue.depth`（排队的调用数）、`records.written`、`attempts.written`、`write.retries`、`records.lost`、`attempts.lost`；丢失的计数器带 `reason`（`queue_full`、`malformed`、`shutdown`），没有账户、key 或用户标签。

## 读

`ListOwn` 必须给账户（`OwnQuery.Account`），按 `(at, id)` 倒序、键集分页（不用 OFFSET：新记录到达时翻页不重不漏），可按时间、模型、客户端应用筛选，一页最多 200 条。**范围就是全部访问控制**：调用方用授权通过后的账户范围打开事务，行级安全只放行这个账户的行；显式的账户条件只是让计划走索引，不是访问控制。站长看本站、平台运维看全部的查询随 operations 和 accounts 加入，用的是它们的范围。

按筛选分三条查询，各读自己的索引（[索引](#索引)）：不筛选读 `(account_id, at DESC, id DESC)`；给了模型（可再给客户端应用，在该模型的行上检查）读 `(account_id, model, at DESC, id DESC)`；只给客户端应用读 `(account_id, client_app, at DESC, id DESC)`。`ORDER BY` 的列写成 `call_log.at`、`call_log.id`：裸的 `id` 指输出列 `id::text`，按文本排序，会让大账户也退回位图加排序。每次查询前 `SET LOCAL plan_cache_mode = force_custom_plan`：服务端对预处理语句执行几次后可能改用通用计划，通用计划看不到参数，把行级安全的过滤估成约 1%，选了读账户窗口内全部记录再排序的计划（6 万条的账户一页读 4,300 个缓冲，筛选时更多）；列表是给控制台用的，不在调用路径上，每次按参数规划的代价（不到 1 ms）可以接受。

测得的读取量（应用角色，行级安全保留，12 万条、1 个大账户 6 万条、100 个小账户各 600 条；`TestListPlansFollowTheIndexes`）：

| 请求 | 计划 | 缓冲 |
|---|---|---|
| 大账户不筛选，首页和后续页 | 索引上按序读，遇 LIMIT 即停，无排序 | 1 到 8 |
| 模型或应用密集（几乎全部命中） | 同上，读该筛选的索引 | 6 到 8 |
| 稀疏（6 万条里命中 100 条） | 读该筛选索引里的全部命中再取前 N | 53 到 103 |
| 一个都不命中 | 索引上一次探查，返回空页 | 1 到 3 |
| 小账户（600 条）不筛选 | 位图扫描加前 N 排序，读窗口内全部 600 条，由账户自己的量封顶 | 560 |

稀疏筛选读的是命中的条数（命中不多于一页就读完），不是账户或分区的量：没有这两个索引时，稀疏和零命中的筛选读整个月分区（36 万条时 7,059 个缓冲，随表增长，60 到 100 ms），不管有没有命中。
控制台把 `Cursor` 写成 32 个字符的不透明文本（`String`：调用开始时间的微秒数 8 字节、调用编号 16 字节，base64url 无填充），`ParseCursor` 读回，不是这个长度或字母表就是 `ErrBadCursor`（控制台答 400）。游标只标记一个账户的调用里的一个位置：伪造一个只会得到范围本来就让读的、从别处开始的一页，所以不签名；它也不绑定筛选条件，换了筛选条件续用同一个游标得到的是按新条件、从那个位置往后的一页。

## 追溯

一条调用、它的每次尝试和它的账本交易只靠一个键相连：请求 ID（调用编号）。它已经是 `call_log.id`、`call_attempt.request_id`、`billing.reservations.request_id` 和 `billing.transactions.request_id`，suppliers 的扣费也按请求 ID 加尝试序号关联（[记录与告警的「追溯」](../../docs/domains/observability.md#追溯)）。

- **调用和尝试**：`GetTrace(ctx, tx, id)` 按请求 ID 读调用行，再按同一个 ID 和调用的 `at` 读尝试，按 `attempt_seq` 排序；返回的就是写进去的 `CallRecord`，带 `Attempts` 和 `Route`。**调用行只按 `id` 查、不带 `at`，所以每个分区各探查一次主键索引**（和 `GetOwn` 一样，次数随保留的月份增长）；返回的 `at` 再用来只读尝试所在的那一个分区。调用行不存在时返回 `pg.ErrNoRows`。
- **谁能调用**：它是运营和管理读的链，不是用户的接口：它带成本、端点和路由。**调用方必须用平台范围，并且只在运营或管理的处理器里调用**；`pg` 没有能判断范围的接口，函数本身拦不住，账户范围在数据库层面读得到自己调用的这些字段（用户读 `ListOwn`/`GetOwn`，那两个没有这些字段）。
- **路由过程**：调用行的 `route` 列，内容是 routing 的 `Record.JSON()`：被排除的候选和原因、选定的路线、每次尝试的结局。尝试不重复存路由。
- **账本交易**：由 billing 的 `LedgerOf(ctx, tx, id)` 按同一个请求 ID 读，每笔交易带自己的分录。observability 不读 billing 的表，也不引用 billing（依赖表里 observability 在 billing 之上），所以「串起」由调用方把两个函数的结果合起来：三个查询、两个模块，各读自己的表（[总体架构的「模块边界规则」](../../docs/architecture.md#模块边界规则)）。
- **不在调用记录里存账本 ID**：`billing.entries` 没有自己的 ID，`ledger_entry_ids` 里的值从来不存在，也没有写入方。抄交易 ID 同样不行：调用记录异步写，队列满时会丢而账本照记，校正还会在记录写好后再记交易，抄下来的链接会缺、会旧。请求 ID 是账本自己的键（`transactions` 的唯一约束以它开头），按它读总是完整、最新。这一列留在表里（`NOT NULL DEFAULT '{}'`），新代码既不读也不写；按[平台的迁移规则](../platform/README.md#迁移)，等停止使用它的版本发布之后再删（#204）。
- **不变量**：尝试序号从 1 连续到 `len(Attempts)`，调用行的 `attempt_count` 等于它（写入时算好）；`route` 是 JSON 对象；`LedgerOf` 的每笔交易 `request_id` 等于调用 ID、`attempt_seq` 是 0（请求本身）或 1 到 3（某次尝试），每笔交易的分录合计为 0（billing 写入时断言）。测试：`TestTraceReadsTheWholeChain`（cmd/axiom，走一次真实结算）与 `TestCallAndAttemptsRoundTrip`。

## 分区

迁移给 `call_log` 和 `call_attempt` 都建了 2026-10 到 2028-12 的月分区和一个默认分区，两表的月份一一对应，按调用开始的 `at` 分区。**此后的分区、到期分区的转出与删除是保留任务的事**（[记录与告警的「存储」](../../docs/domains/observability.md#存储)），它随温层存储一起做；在那之前，超出范围的调用和尝试进各自的默认分区，不丢，只是还没法整块转出。需要持有表的角色（`axiom_owner`）执行，不能由 `axiom_app` 做，所以不是调用路径上的事。保留任务按同一组月份处理两表，先删尝试再删调用，或在一个月的分区上一起操作。

## 索引

`call_log` 在每个分区上有四个索引：主键 `(id, at)`；`(account_id, at DESC, id DESC)`，不筛选的列表；`(account_id, model, at DESC, id DESC)` 和 `(account_id, client_app, at DESC, id DESC)`，两种筛选（迁移 `00002`）。

`call_attempt` 只有主键索引 `(request_id, at, attempt_seq)`：按请求查它的尝试走这个前缀，保留任务按分区整块处理；没有按尝试的列表查询，所以暂不加别的索引。它的写入量是调用行乘以每次尝试，见下文的写入放大。

- **选择**：比较了四种做法，量的是 36 万条（表 83 MB，原有索引 50 MB）。(a) 不加：稀疏和零命中读整个分区（7,059 缓冲，随表增长）。(b) 在账户索引上加 `INCLUDE (model, client_app)`：计划不变，仍然读整个分区，没有用。(c) 只加模型索引：模型筛选解决，应用筛选仍读整个分区。(d) 模型和应用各一个：两种筛选都读命中的行（见上表）。取 (d)，是让两个筛选都有界的最小做法。
- **写入放大**：每个索引每行多一条索引项，36 万条各多约 41 MB（表加索引合计 133 MB 到 174 MB 到 215 MB，每个约 +31%）；批量写入 12 万条的最快一次是 4.6 s、3.9 s、7.9 s（无、一个、两个；机器噪声约三成，体积是确定的）。调用记录由后台按批写（满 500 条立即写，否则最多攒 200 ms），不在请求路径上；持续吞吐取决于调用到达的速率和数据库，没有固定上限。队列满时 `Enqueue` 丢掉新记录并计数，所以写入持续跟不上时只会少记录，不会拖慢调用；代价是 `call_attempt` 的每个尝试一行和它的主键索引，两条筛选索引在持续满批写入下的吞吐**待验证**，随[性能](../../docs/performance.md#怎么验证)的压测一起量。如果运营中发现应用筛选很少用，删掉 `call_log_account_app_at` 是一条只减不增的迁移。
- **迁移方式**：加索引兼容旧程序（旧程序的查询没有它也对），按平时的顺序先迁移再发新程序。分区表的 `CREATE INDEX` 在每个分区上建一个，建的时候对该分区加 SHARE 锁，挡住写入器的插入，时长随该分区的行数增长（12 万条约 1.7 秒）；分区表的父表不支持 `CONCURRENTLY`。`00002` 用普通的 `CREATE INDEX`，只适合表还小的时候，也就是 `call_log` 第一次部署时。**已经有大量数据的调用记录再加索引**，迁移要这样写：标 `-- +goose NO TRANSACTION`；`CREATE INDEX 名 ON ONLY observability.call_log (...)`（建出无效的父索引，不锁写）；对每个分区 `CREATE INDEX CONCURRENTLY 分区索引名 ON 分区 (...)`（不挡写；`migrate` 的 5 秒 `lock_timeout` 对它等老事务也生效，超时就失败，留下无效索引，要先删掉再重跑）；再 `ALTER INDEX 父索引 ATTACH PARTITION 分区索引`，全部挂上后父索引才生效。新建的月分区自动带上父索引。

## 决策与理由

| 决策 | 理由 | 否决的做法 |
|---|---|---|
| 另建 `call_attempt`，每次尝试一行 | 调用记录要能串起每次尝试（[记录与告警的「追溯」](../../docs/domains/observability.md#追溯)）；suppliers 的 `attempt_costs` 只在结算事务里写，失败或崩溃的请求可能没有可查的尝试；单独的表让尝试有类型、时间和结果，并按 `(request_id, attempt_seq)` 连到供应商的扣费。负责人已在对话中把设计决定交给开发者（Issue #29） | 只存 `route` JSON：没有类型和耗时，查一次尝试要解 JSON；把尝试嵌进 `call_log`：一个请求一行会被撑大，按尝试查询和保留都更难；让 suppliers 直写：跨模块写同一张表，违反模块边界 |
| 调用行的 `endpoint_id`、`source_class`、`egress`、`attempt_count` 取自最后一个尝试 | 一处事实：调用行与尝试表不可能不一致；网关也不用把同样的值填两遍 | 调用记录再带一份端点、来源和出口：两套值迟早对不上 |
| 调用、尝试和账本交易只用请求 ID 关联，`ledger_entry_ids` 不再读写 | 分录没有 ID，这一列从来没有写入方；请求 ID 已经是账本自己的键，照抄一份会在调用记录丢失或校正追加交易时与账本不一致。列按平台的迁移规则留到停止使用它的版本之后删除 | 抄交易 ID：要结算把 ID 带进异步记录，仍会缺、会旧；让 observability 查 billing 的表：违反模块边界 |
| 队列满时丢掉新记录并计数，不等待 | 写记录不能阻塞或拖慢调用（Issue #29 的验收「写记录不阻塞调用」）；容量与在途名额相当，只有数据库长期落后才会丢；丢失的条数进指标和 `Close`，可以告警。负责人已在对话中把这一取舍交给开发者 | 等待空位：违反不阻塞，还会把数据库的慢传回调用；丢最旧：要加锁、会悄悄丢掉已经接受的记录；写本地磁盘缓冲：文件 I/O 同样会阻塞，重放和崩溃一致性也不在本 PR 的范围；无界队列：内存没有上限 |
| 整批行作为一个 JSON 数组传给一条 `INSERT … SELECT` | 一次往返、一个参数；类型转换和检查留给表。sqlc 不认 30 多个参数的 `unnest`，也没有 `:copyfrom`（`pg.Tx` 没有 `CopyFrom`） | 逐行 `INSERT`：每条一次往返；列式 `unnest`：生成不了 |
| 金额存最小单位的整数，币种单列 | 与 billing 的存储一致（[计费的「钱的表示」](../../docs/domains/billing.md#钱的表示)），汇总不丢精度 | 浮点数、字符串 |
| 默认分区加预建月分区 | 记录不能丢，写入不能因缺分区失败；预建的月数让保留任务有时间落地 | 调用路径上建分区：`axiom_app` 没有权限，也会在热路径上拿锁；没有默认分区：缺分区时写入失败，该批被当成坏行丢掉 |
| 终端用户标识用 HMAC，类型上只能由 `HashEndUser` 生成 | 无密钥的哈希能被穷举还原；类型让原始标识符无法误入 | 在调用方各自哈希 |
| 重试到成功，只对数据错误跳过 | 数据库短暂不可用不应丢记录；数据错误重试没有意义，不跳过会堵死 | 有限次重试后丢弃：数据库的短暂故障也会丢 |
