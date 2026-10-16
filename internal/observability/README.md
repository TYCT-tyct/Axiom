# observability

调用记录的代码接口：写什么、怎么写、怎么读、数据怎么放。要不要记、记多久、谁能看是[记录与告警](../../docs/domains/observability.md)的事，本文只写代码里的做法和理由。指标与链路的基座在 [platform 的「遥测」](../platform/README.md#遥测)，不在本模块。

目前实现的是调用记录。对话内容、分析标签、受控导出、告警与通知渠道随各自的 Issue 加入，不在本文。

## 接口

```go
func Module() pg.Module                                   // 迁移，交给 cmd/axiom
func NewRecorder(ctx, db *pg.DB, scope pg.Scope, cfg Config, logger *slog.Logger) *Recorder // Config 的各项都必须为正，否则 panic
func (r *Recorder) Enqueue(ctx context.Context, rec CallRecord) error // 队列满了就等
func (r *Recorder) Close(ctx context.Context) error       // 写完队列里的，ctx 到期则报告丢了几条
func ListOwn(ctx, tx pg.Tx, q OwnQuery) ([]OwnCall, *Cursor, error)
func GetOwn(ctx, tx pg.Tx, id uuid.UUID) (OwnCall, error)
func HashEndUser(key []byte, id string) EndUserHash       // 带密钥的哈希；EndUserHash 的数组是私有的，包外只能经它得到非零值，零值表示没有终端用户
```

查询和生成的代码在 `internal/store`（sqlc，`sqlc.yaml` 的 `observability` 项），包外引用不到，所以所有写入都经过 `CallRecord`、`Recorder`，所有读取都经过下面按账户的查询。`CallRecord` 只用本模块、`catalog`、`platform` 的类型和 `uuid.UUID`（`accounts` 的带类型 ID 随它的表加入后换上）。HTTP 处理器随 gateway 加入，调用的就是 `ListOwn` 和 `GetOwn`。

## 表（schema `observability`）

`call_log`：一个请求一行，按请求开始时间 `at` 分区，主键 `(id, at)`，`id` 就是请求 ID（调用编号）。

- **没有 NULL**：可选的文字是 `''`，没有产出首字时 `first_token_ms` 是 0，没有尝试时 `endpoint_id` 是全零 UUID。生成的代码因此不需要可空类型。
- **谁**：`site_id`、`account_id`（行级安全看这两列）、`workspace_id`、`key_id`、`end_user_hash`（HMAC-SHA256 的十六进制）、`ip`。
- **走了哪**：`model`（客户端要的公开名字）、最终尝试的 `endpoint_id`、`attempt_count`、`route`（路由过程，≤ 8 KiB 的 JSON 对象，结构由 routing 定）、`ledger_entry_ids`（≤ 16 个）。**每次尝试和账本交易由 suppliers、billing 按同一个请求 ID 保存**，这里只记数量和 ID，不重复存。
- **结果**：`usage`（计费项名字到数量的 JSON）、`service_tier`、`price_nano`、`cost_nano`（计费的最小单位）、`currency`、`free_of_charge`、`latency_ms`、`first_token_ms`、`finish_reason`、`error_kind`。
- **权利**：`consent_version`、`training_allowed`，只能在写入时记下。
- **写入标签**：`client_app`、`session_id`、`subagent_id`、`request_class`、`language`、`used_tools`、`has_image`、`has_file`。分析标签随后台分类器加入，另表保存。

## 不变量

| 不变量 | 靠什么保证 |
|---|---|
| 租户只读写自己账户的行 | 表和每个分区强制行级安全，策略 `platform.in_scope(site_id, account_id)`；写入者在平台范围里（`pg.PlatformScope()` 由 cmd 交给 `NewRecorder`）。测试：账户 A 读不到 B 的行也按 ID 取不到，A 的范围写不进 B 的行 |
| 记录不可改、不可删 | `axiom_app` 只有 SELECT 和 INSERT；到期按分区整块处理，由持有表的角色执行。测试：UPDATE、DELETE、TRUNCATE 被拒 |
| 用户看不到成本、端点和路由 | `OwnCall` 没有这些字段，查询不选这些列（[接入与对外接口的「用量与费用」](../../docs/domains/gateway.md#用量与费用)） |
| 文字能存进库 | `Enqueue` 拒绝不合法的 UTF-8 和 NUL；编码本身失败（`errEncoding`）和数据库的数据错误一样走坏行隔离。测试：非法 UTF-8 的行夹在好行之间，经真实的编码和库写入，好行落库、坏行被计数 |
| 终端用户标识只以带密钥的哈希存放 | `EndUserHash` 是带私有数组的结构，包外无法构造非零值 |
| 没有对话正文、密钥或凭证 | 列里没有这样的名字或字节类型，文字和 JSON 列都有长度或格式检查；测试读 `information_schema` 核对这三点。请求头一律不记 |
| 同一个请求写两次是一行 | `ON CONFLICT (id, at) DO NOTHING`；`at` 是请求开始时间，不是写入时间，所以重试的是同一个键 |
| 写入从不因没有分区而失败 | 默认分区兜底；测试写入 2001 年和 2090 年的记录 |
| 所有表和分区都过启动检查 | 每个分区各自强制行级安全并有策略（分区不继承）；测试 `DB.Check` 通过 |

## 写入

`Enqueue` 把记录交给有界队列就返回；队列满了就等，直到有空位、ctx 结束或 `Close` 开始，**不丢弃**。队列容量与节点在途名额相当（cmd 里是 5000），所以队列满只在数据库远远落后时发生，等待的请求数不会超过在途名额。ctx 结束时这条记录没有入队，`Enqueue` 返回 ctx 的错误：调用方传一个只随进程退出结束的 ctx，不要传客户端断开会取消的那个。

后台一个写入协程，每 200 ms 或攒满 500 行写一批：一条 `INSERT … SELECT FROM jsonb_array_elements($1)`，参数是整批行的一个 JSON 数组，成员名就是列名，类型和检查由表来做。每次写入有自己的期限（`WriteTimeout`，cmd 里 10 s），超时后算失败，退避重试（100 ms 起翻倍，上限 5 s，带抖动），重试到成功为止；超时的那次结果不明，重写同一批由 `ON CONFLICT` 兜底。一个不再应答的连接因此拖不住唯一的写入协程；整体仍由 `Close` 的期限控制。数据库说数据本身有问题（`pg.IsDataError`：SQLSTATE 22、23），或编码这批行失败（同样的行每次都失败）时不重试：把批一分为二找到那一行，记日志（请求 ID 和错误，不含内容）、计入丢失数后跳过，否则一条坏记录会堵住后面所有记录，进而堵住网关。

`Close` 先拒绝新的 `Enqueue` 并放走正在等的，等已进入的都入队，再关队列让写入协程写完剩下的；ctx 到期就停止重试，报告丢了几条。cmd 的退出顺序是：监听停止并排空，`Close`，最后关数据库；启动失败等其他返回路径也会关闭它，不留写入协程。进程崩溃丢的是最近一次成功写入之后的记录（[记录与告警的「写入路径」](../../docs/domains/observability.md#写入路径)）。

指标：`axiom.observability.queue.depth`、`records.written`、`write.retries`、`records.lost`，没有账户、key 或用户标签。

## 读

`ListOwn` 必须给账户（`OwnQuery.Account`），按 `(at, id)` 倒序、键集分页，条件与索引 `(account_id, at DESC, id DESC)` 对应，计划都走该账户的索引范围，不扫全表；账户一个窗口里有几万条时是索引上按序读、遇 LIMIT 即停（约一页的行，无排序），只有几百条时是位图扫描加前 N 排序（读该账户窗口内全部几百行，代价由账户自己的量封顶，量大了计划自动换成按序扫描）。按模型、客户端应用筛选时，这两列不在索引里：命中稀疏或为零时，按序扫描要读完账户窗口内的记录才凑够一页，上面「约一页」只对不筛选或命中密集的页成立，测试只验证了后者。`ORDER BY` 的列写成 `call_log.at`、`call_log.id`：裸的 `id` 指输出列 `id::text`，按文本排序，会让大账户也退回位图加排序。`TestListPlansFollowTheAccountIndex` 以应用角色对首页、后续页、带筛选的页分别 `EXPLAIN (ANALYZE, BUFFERS)` 实际的列表 SQL（`store.ListQuery`）并断言上述形状（不用 OFFSET：新记录到达时翻页不重不漏），可按时间、模型、客户端应用筛选，一页最多 200 条。**范围就是全部访问控制**：调用方用授权通过后的账户范围打开事务，行级安全只放行这个账户的行，显式的账户条件只是让计划走索引，不是访问控制。站长看本站、平台运维看全部的查询随 operations 和 accounts 加入，用的是它们的范围。

## 分区

迁移建了 2026-10 到 2028-12 的月分区和一个默认分区。**此后的分区、到期分区的转出与删除是保留任务的事**（[记录与告警的「存储」](../../docs/domains/observability.md#存储)），它随温层存储一起做；在那之前，超出范围的记录进默认分区，不丢，只是还没法整块转出。需要持有表的角色（`axiom_owner`）执行，不能由 `axiom_app` 做，所以不是调用路径上的事。

## 决策与理由

| 决策 | 理由 | 否决的做法 |
|---|---|---|
| 一张 `call_log`，不另建尝试表 | 尝试由 suppliers 在结算事务里按请求 ID 写（`RecordAttempts`），账本由 billing 写，记录里再存一份会有两个来源、可能不一致；从记录追溯靠同一个请求 ID | 记录里内嵌尝试数组：两份真相；单独的 `call_attempt` 表：重复 suppliers 已有的表 |
| 整批行作为一个 JSON 数组传给一条 `INSERT … SELECT` | 一次往返、一个参数；类型转换和检查留给表。sqlc 不认 30 多个参数的 `unnest`，也没有 `:copyfrom`（`pg.Tx` 没有 `CopyFrom`） | 逐行 `INSERT`：每条一次往返；列式 `unnest`：生成不了 |
| 金额存最小单位的整数，币种单列 | 与 billing 的存储一致（[计费的「钱的表示」](../../docs/domains/billing.md#钱的表示)），汇总不丢精度 | 浮点数、字符串 |
| 默认分区加预建月分区 | 记录不能丢，写入不能因缺分区失败；预建的月数让保留任务有时间落地 | 调用路径上建分区：`axiom_app` 没有权限，也会在热路径上拿锁；没有默认分区：缺分区时写入失败，队列堵住网关 |
| 终端用户标识用 HMAC，类型上只能由 `HashEndUser` 生成 | 无密钥的哈希能被穷举还原；类型让原始标识符无法误入 | 在调用方各自哈希 |
| 重试到成功，只对数据错误跳过 | 数据库短暂不可用不应丢记录；数据错误重试没有意义，不跳过会堵死 | 有限次重试后丢弃：违背"始终保存" |
