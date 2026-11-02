# observability

调用记录的代码接口：写什么、怎么写、怎么读、数据怎么放。要不要记、记多久、谁能看是[记录与告警](../../docs/domains/observability.md)的事，本文只写代码里的做法和理由。指标与链路的基座在 [platform 的「遥测」](../platform/README.md#遥测)，不在本模块。

目前实现调用与尝试记录，以及运维告警事件、规则、邮件与 Webhook 通知和周期渠道自测。用户元数据筛选、聚合与导出已实现；对话内容、分析标签和内容受控导出随各自的 Issue 加入。

## 接口

```go
func Module() pg.Module                                   // 迁移，交给 cmd/axiom
func NewRecorder(ctx, db *pg.DB, scope pg.Scope, cfg Config, logger *slog.Logger) (*Recorder, error) // 锁定并校验 spill；目录被占用、不可写、段损坏时启动失败
func (r *Recorder) Admit() (*Slot, error)                 // 调用准入前预占一条记录的位置；spill 满返回 ErrBacklog，已坏返回 ErrBroken
func (s *Slot) Commit(rec CallRecord) error               // 把记录追加到本机 spill 并 fsync；返回后进程崩溃、主机断电也能重放
func (s *Slot) Release()                                  // 调用结束但没有记录时归还位置；Commit 之后是空操作
func (r *Recorder) Enqueue(rec CallRecord) error          // Admit + Commit 的便捷入口，给测试和非 gateway 调用方
func (r *Recorder) Close(ctx context.Context) error       // 写完 spill；ctx 到期则报告还留在磁盘上的条数
func (r *Recorder) Done() <-chan struct{}                 // 写入协程退出时关闭；Close 返回前已等待它，测试据此断言没有写入协程留下
func ListOwn(ctx, tx pg.Tx, q OwnQuery) ([]OwnCall, *Cursor, error)
func CheckOwnExportSize(ctx, tx pg.Tx, q OwnQuery) error // 同一 ReadTx 内、开始输出前检查最多 10001 个匹配键；超过 10000 返回 ErrExportSize
func GetRetention(ctx, tx pg.Tx) (Retention, error)         // 调用记录的热窗口；第一次保存前是代码默认值
func SetRetention(ctx, tx pg.Tx, r Retention) error        // 校验并保存；范围 0 到 3650 天，0 表示全部留在 PostgreSQL
func RegisterRetention(cfg RetentionConfig) error          // 在 archive 队列登记每天的预建、归档与删除
func AggregateOwn(ctx, tx pg.Tx, q OwnQuery, groupBy string) ([]OwnAggregate, error) // day/model/key，最多 1000 组，收费与次数为精确文本
func GetOwn(ctx, tx pg.Tx, id uuid.UUID) (OwnCall, error)
func GetTrace(ctx, tx pg.Tx, id uuid.UUID) (CallRecord, error) // 一条调用、它的尝试和 route，按请求 ID；只从运营或管理的处理器调用，事务须是平台范围
func (c Cursor) String() string; func ParseCursor(s string) (Cursor, error) // 控制台翻页用的不透明游标；格式不对 ErrBadCursor
func HashEndUser(key []byte, id string) EndUserHash       // 带密钥的哈希；EndUserHash 的数组是私有的，包外只能经它得到非零值，零值表示没有终端用户；目前没有生产调用者，由 [#29](https://github.com/TYCT-tyct/Axiom/issues/29) 写 `end_user_hash` 时接上
var ErrClosed, ErrBacklog, ErrBroken, ErrPending error     // Close 之后 / spill 满 / 已无法记录 / Close 仍有未写记录
```

`CallRecord` 带一个请求的元数据和它的每次尝试（`AttemptRecord`）；`call_log` 一行是调用，`call_attempt` 一行是尝试。尝试的 `Seq` 从 1 连续到最多 `platform.MaxAttemptSeq`（33：路由的 3 次回退加模型侧循环的每次采样，迁移 `00016_loop_attempts.sql` 放宽了表上的 CHECK），写入时校验；调用行的 `endpoint_id`、`source_class`、`egress`、`attempt_count` 由最后一个尝试算出，因此不会与尝试表不一致。查询和生成的代码在 `internal/store`（sqlc，`sqlc.yaml` 的 `observability` 项），包外引用不到，所以所有写入都经过 `CallRecord`、`AttemptRecord`、`Recorder`，所有读取都经过下面按账户的查询和「追溯」的 `GetTrace`。`CallRecord` 只用本模块、`catalog`、`platform` 的类型和 `uuid.UUID`（`accounts` 的带类型 ID 随它的表加入后换上）；`AttemptRecord` 用 `uuid.UUID`、`platform.AttemptSeq` 和时间。HTTP 处理器随 gateway 加入，调用的就是 `ListOwn`、`GetOwn` 和按日/模型/工作区的 `ActivityOwn`；尝试不向用户开放（用户看不到端点、成本和路由）。

## 表（schema `observability`）

`call_log`：一个请求一行，按请求开始时间 `at` 分区，主键 `(id, at)`，`id` 就是请求 ID（调用编号）。

- **没有 NULL**：可选的文字是 `''`，没有产出首字时 `first_token_ms` 是 0，没有尝试时 `endpoint_id` 是全零 UUID。生成的代码因此不需要可空类型。
- **谁**：`site_id`、`account_id`（行级安全看这两列）、`workspace_id`、`key_id`、`end_user_hash`（HMAC-SHA256 的十六进制）、`ip`。
- **走了哪**：`model`（客户端要的公开名字）；`endpoint_id`、`source_class`、`egress`、`attempt_count` 都取自最后一个 `AttemptRecord`（没有尝试时分别是全零 UUID、`''`、`''`、0）；`route`（路由过程，≤ 8 KiB 的 JSON 对象，结构由 routing 定）。**每次尝试在 `call_attempt`**，供应商的成本和对账键仍在 suppliers，按同一个请求 ID 和尝试序号关联；账本交易不抄在这里，按请求 ID 从 billing 读（见「追溯」）。
- **结果**：`usage`（计费项名字到数量的 JSON，含原生 token）、`service_tier`、`price_nano`、`cost_nano`（计费的最小单位）、`currency`、`free_of_charge`、`streamed`、`physical_input`（写入时能确定的物理输入；未知为 −1 的库内哨兵，读取时转成可空）、`latency_ms`、`first_token_ms`、`finish_reason`、`error_kind`。
- **权利**：`consent_version`、`training_allowed`，只能在写入时记下。
- **写入标签**：`client_app`、`session_id`、`subagent_id`、`request_class`、`language`、`used_tools`、`has_image`、`has_file`；应用自己报的名字在 `app_referer`（URL，≤2048，只留 scheme、host 和 path）、`app_title`（≤200）、`app_categories`（最多两个小写连字符类别，≤64），由 gateway 从 `HTTP-Referer`、`X-OpenRouter-Title`（或旧的 `X-Title`）、`X-OpenRouter-Categories` 取出、去控制字符并截断后写入。分析标签随后台分类器加入，另表保存。

`call_attempt`：一次尝试一行，外键意义上的归属是调用，按调用的 `at` 分区，主键 `(request_id, at, attempt_seq)`。

- **谁**：`site_id`、`account_id`（与调用相同，行级安全看这两列）。
- **走了哪**：`endpoint_id`、`supplier_id`、`source_class`（official、cloud、relay）、`egress`（按名字）。
- **结果**：`outcome`（`committed`：内容已交给客户端；`failed`：没有；`unknown`：上游可能已执行）、`error_kind`、`started_at`、`finished_at`、`first_token_ms`。成本、用量和供应商的对账键不在这里重复：suppliers 的 `attempt_costs` 按 `(request_id, attempt_seq)` 保存，与计费同成同败；崩溃或超过截止时间的请求记在 `suppliers.lost_requests` 里。
- **没有 NULL，文字有界**：与 `call_log` 相同；没有正文、密钥或凭证列。

`spend_anomalies`：检测器每个正在发生的异常一行，主键 `(account_id, kind)`；`kind` 是 `spend/<币种>`、`region` 或 `model`。同样是平台范围表：账户范围读不到，只有检测器和启动检查碰它。

`spend_detection`：单行 `jsonb`，保存管理员设置的检测开关、窗口、基线、金额下限、倍数和两类次数下限。和 `alert_configuration` 分开，旧节点整体替换规则/渠道时不会把这个不认识的对象丢掉；默认值在代码里，第一次保存前读代码默认值。

`retention`：单行 `jsonb`，保存调用记录的热窗口（`hot_days`，默认 90）。和规则、阈值一样是管理员数据，管理接口读写并留审计；0 表示全部留在 PostgreSQL。桶没配置时这个值不起作用：没有确认的归档就不能删。

## 不变量

| 不变量 | 靠什么保证 |
|---|---|
| 租户只读写自己账户的行 | 两张表和每个分区强制行级安全，策略比较本行站点/账户与 [pg 的 InitPlan 范围参数](../platform/pg/README.md#决策与理由)；写入者在平台范围里（`pg.PlatformScope()` 由 cmd 交给 `NewRecorder`）。测试：账户 A 读不到 B 的行也按 ID 取不到，A 的范围写不进 B 的调用或尝试 |
| 记录不可改、不可删 | `axiom_app` 只有 SELECT 和 INSERT；到期按分区整块处理，由持有表的角色执行。测试：两张表的 UPDATE、DELETE、TRUNCATE 被拒 |
| 归档确认后才删分区 | 清单最后上传，存在即表示两份对象都完整上传；删除前逐个 HEAD 核对清单里的字节数，核对失败不删，下次重写或重试。测试：上传失败、HEAD 失败、对象短于清单时分区都在；修复后才删 |
| 归档之后的迟到写入不丢 | 调用记录只有 INSERT（应用没有 UPDATE/DELETE），删除事务先对现存分区加 `ACCESS EXCLUSIVE` 锁并精确 `count(*)`；行数与清单不符就回滚重写整月，锁一直持到 DROP 提交，所以计数和删除之间没有写入窗口。一次运行最多重写三次，仍变化就报错留给下一轮，绝不删未归档的行。测试：确认失败后补写旧月记录再重试，新行在清单和 CSV 里 |
| 账本、汇总和审计不随调用日志删除 | 保留任务只拼 `observability.call_log_*` 与 `call_attempt_*` 的分区名，从不写 `billing` 的语句；测试在一个已归档月份旁边放另一 schema 的行，运行后仍在 |
| 分区的建与删只由专用角色做 | 两张父表和全部分区归 `axiom_retention_job`（NOLOGIN）；`axiom_owner` 继承它，部署建的 `axiom_retention` 登录是它的成员，`axiom_app` 不是。应用仍只有 SELECT/INSERT，保留任务用 `AXIOM_RETENTION_DATABASE_URL` 独立连接，拿到分区 DDL 所需的所有权。测试：任务在独立登录下能建、删分区，应用不能改表 |
| 用户看不到成本、端点和路由 | `OwnCall` 没有这些字段；尝试表不向用户开放，查询也不选尝试（[接入与对外接口的「用量与费用」](../../docs/domains/gateway.md#用量与费用)） |
| 文字能存进库 | `Commit` 在写 spill 前拒绝不合法的 UTF-8、NUL 和打印成 jsonb 文本后超过 8192 字节的路由 JSON（每个 `:`、`,` 后多一个空格，按此上界计），包括每次尝试的文字；这是 bug 或上游脏数据，不是可以跳过的行，进了 spill 它会在每次重放时被数据库拒绝。路由由 routing 有界编码：选定的路线、尝试（只有端点和结果，出口在 `call_attempt`）和每个原因的数量完整，被排除的端点最多逐个记 16 个，最坏情况约 5.2 KB，所以合法的 `openrouter/auto` 记录总能写进 `call_log.route`。记录计入 `records.lost{reason="invalid"}`，recorder 标记为不可记录，准入从此拒绝。测试：非法 UTF-8、NUL、未知 outcome、超过 jsonb 上限的路由（`TestEnqueueRejectsText`），逐项排除上千个候选的最大路由不超过同一上界（`TestRecordIsBounded`），恰好等于上界的路由能写进真实数据库、存下的文本与预测一致（`TestBoundedRouteFitsTheColumn`） |
| 终端用户标识只以带密钥的哈希存放 | `EndUserHash` 是带私有数组的结构，包外无法构造非零值 |
| 没有对话正文、密钥或凭证 | 两张表的列里没有这样的名字或字节类型，文字和 JSON 列都有长度或格式检查；测试读 `information_schema` 核对两张表。请求头只记应用归因的三个，由 gateway 去控制字符、按列宽截断（`app_referer` 另去掉 userinfo、查询串和片段）后放入 `app_referer`、`app_title`、`app_categories`；其余请求头一律不记 |
| 每次尝试有且只有一行，且能从调用找到 | `call_attempt` 的主键 `(request_id, at, attempt_seq)`；`Seq` 必须从 1 连续递增，最多 `platform.MaxAttemptSeq` 次（`TestALoopsAttemptsAreKept`）；调用行的端点、来源类别、出口和次数由最后一个尝试算出。测试：写两次只有一行，按序号读回每次尝试，调用行与最后尝试一致 |
| 同一个请求写两次是一行 | 两张表都是 `ON CONFLICT … DO NOTHING`；`at` 是请求开始时间，不是写入时间，所以重试的是同一个键。测试：同一个 `CallRecord` 写两次，调用一行、每次尝试一行 |
| 写入从不因没有分区而失败 | 两张表都有默认分区兜底；测试写入 2001 年和 2090 年的调用和尝试 |
| 记录不在过载时丢失 | 调用准入前 `Admit` 预占一条记录的位置；记录在响应结束前追加到本机 spill 并 fsync，满时 `Admit` 返回 `ErrBacklog`，gateway 答 503 并拒绝新调用，不丢已接受的记录。测试：写协程卡住、两个位置占满时第三次 `Enqueue` 立即返回 `ErrBacklog`，前两条照常写入；崩溃后重启按顺序重放；同一批重放不重复；坏行之后在途的调用仍能写入 |
| 所有表和分区都过启动检查 | 每个分区各自强制行级安全并有策略（分区不继承）；测试 `DB.Check` 通过 |
| 一次异常只通知一次 | `spend_anomalies` 的 `(account_id, kind)` 主键让重复检测 `ON CONFLICT DO NOTHING`，只有首次插入才持久化信号和用户提醒；异常消失后删除状态并只发一次恢复。测试：连续两轮同一异常只有一条 raise，窗口移走后一条 resolve |
| 阈值不随旧节点替换丢失 | `spend_detection` 独立于 `alert_configuration.body`；管理 PUT 省略 `spend_detection` 时保留已存值，管理 GET 合并返回默认值或已存值。测试：保存后模拟旧节点替换，阈值不变 |

## 写入

**记录先落本机并持久化，再异步入库。** gateway 在准入时调用 `Admit`，为这次调用预占一条记录的位置；调用结算后、响应结束前，`Executor` 用这个位置 `Commit`：记录先校验、编码成一行 JSON，追加到 `AXIOM_CALL_LOG_DIR` 下的段文件，并 `fsync` 后才返回。并发的 `Commit` 登记位置后共用一次 `file.Sync()`：一个领导者 sync 活动段到当前末尾，释放所有被覆盖的等待者，sync 期间到达的留给下一轮；顺序调用仍各付一次，并发调用把这段固定成本摊薄。`Commit` 返回后记录已经在磁盘上，进程崩溃和主机断电都不会丢。写协程按段内顺序读出来，只读到已经 fsync 的位置，批量写数据库，写成功才删段：否则它可能把一条还没 fsync 的记录写进库并把确认位置记到它后面，断电后段比确认位置短，下一次启动只能拒绝。这样调用路径不等待数据库，也不会因为积压到上限而把已服务的调用丢掉。

**spill 的格式、所有权与恢复。** 目录里是 `call-<16 位序号>.wal`，每个段以 `AXIOM-WAL-1\n` 开头，之后每条记录是 4 字节长度、4 字节 CRC32C（Castagnoli）和 JSON 正文字节。一个段 8 MiB，写满就换下一个。打开目录时先对 `call-log.lock` 取 `flock` 排他锁，第二个进程直接拒绝启动：两个进程从同一偏移追加会互相覆盖，而两次 `Commit` 都会看起来成功。节点必须各用各的目录（`.env.example` 与 `deploy/compose.yaml` 的说明）。启动时先读 `call-log.pos`：写协程每次确认一批，就把它原子替换地写成新的已确认位置，然后删掉整段已确认的段；排空时把活动段也换掉，所以低流量节点的段不会长期留着已经入库的记录。之后按序号读所有段，逐条校验长度和 CRC：最后一段末尾短头或短正文是崩溃时还没返回的 `Commit`，截掉；长度完整但 CRC 不匹配的记录是损坏，中间段损坏也一样，启动失败而不是静默丢掉一条可能已经服务的记录。错误信息给出段文件和偏移，文件保持原样，由运维判断后修复或截断再启动。已确认位置之前的记录不重放、也不计入 `pending`；崩溃恰在数据库提交与位置写入之间时，下一段会重放一小段已提交记录，`ON CONFLICT DO NOTHING` 仍然去重，归档并删除分区后的重放因此不会把旧记录插回默认分区。

**写入数据库。** 写协程走 [platform 的普通连接池](../platform/README.md#连接池)，不占完成类预留。每批最多 500 条调用，或从第一条起最多等 200 ms：一个事务里先写 `call_log`，再写 `call_attempt`，各是一条 `INSERT … SELECT FROM jsonb_array_elements($1)`，参数是整批行的一个 JSON 数组，成员名就是列名，类型和检查由表来做。先写调用再写尝试，重试也按这个顺序，所以不会出现有尝试、没有调用的批次；同一次写入里两者一起提交，调用总能找到它的尝试。每次写入有自己的期限（`WriteTimeout`，cmd 里 10 s），超时后算失败，退避重试（100 ms 起翻倍，上限 5 s，带抖动），重试到成功为止；超时的那次结果不明，重写同一批由 `ON CONFLICT` 兜底。一个不再应答的连接因此拖不住唯一的写入协程；整体仍由 `Close` 的期限控制。

**数据错误不跳过，也不连带丢掉在途调用。** 数据库说数据本身有问题（`pg.IsDataError`：SQLSTATE 22、23），或编码这批行失败（同样的行每次都失败）时，写协程保留整批、标记 recorder 不可记录并停止写数据库；`Admit` 从此返回 `ErrBroken`，gateway 答 503。已经预占位置、还在上游或结算中的调用仍可 `Commit`：追加文件保持打开，直到在途调用全部写入或释放后才关，否则一条坏行会让随后完成的调用拿到 `ErrClosed` 并永久丢掉记录。坏行留在 spill，请求 ID 和错误在日志里等运维修；代价是它挡住后面的记录，但节点同时停止接新调用，不会静默丢。

**关闭。** `Close` 先拒绝新的 `Admit`，让写协程把 spill 写完；ctx 到期就停止重试和读取，`Done` 关闭，记录留在段文件里，下一次启动接着写。cmd 的退出顺序是：监听停止并排空，`Close`，最后关数据库；启动失败等其他返回路径也会关闭它，不留写入协程。`Close` 返回 `ErrPending` 时报告还留在磁盘上的条数，不把它们算成丢失。

**容量和磁盘上限。** `Capacity`（cmd 里 5000）是在途调用和未写记录的合计上限：`Admit` 的原子计数到顶就返回 `ErrBacklog`，gateway 在准入前答 503 `overloaded`，既不服务无记录的调用，也不把数据库的慢传成无界磁盘增长。单条记录编码上限 64 KiB，所以磁盘上限约 `Capacity × (64 KiB + 8) + 8 MiB`；超过时 `Commit` 失败、计入 `records.lost{reason="spill"}` 并标记不可记录。已准入的调用如果在本机磁盘写失败或 fsync 失败，记录无法确认保存，节点会拒绝后续调用；fsync 失败后内核可能已丢掉脏页、之后的 fsync 却照样成功，所以 spill 同时停止追加和读取，留给下一次启动按磁盘上的内容恢复；这是无法从事后补救的尾部窗口，日志和 `records.lost` 会指出请求 ID。

指标：`axiom.observability.records.pending`（spill 里尚未确认的调用数）、`records.written`、`attempts.written`、`write.retries`、`records.lost`、`attempts.lost`；丢失的计数器带 `reason`（`invalid`、`spill`、`shutdown`），没有账户、key 或用户标签。保留任务另有 `axiom.observability.retention.partitions{action,table}`（`archived`、`dropped`）。`AlertSample` 返回 `records.pending` 和累计丢失数，供 `observability.write_lag` 与 `observability.records_lost` 使用。

## 读

`ListOwn` 必须给账户（`OwnQuery.Account`），按 `(at, id)` 倒序、键集分页（不用 OFFSET：新记录到达时翻页不重不漏），可按时间、模型、客户端应用、key、工作区、成功/报错和请求 ID 组合筛选，一页最多 200 条。**范围就是全部访问控制**：调用方用授权通过后的账户范围打开事务，行级安全只放行这个账户的行；显式的账户条件只是让计划走索引，不是访问控制。站长看本站、平台运维看全部的查询随 operations 和 accounts 加入，用的是它们的范围。

所有筛选共用 `ListOwnCalls` 查询，可选条件由 `force_custom_plan` 按参数折叠，让优化器按实际筛选选择有序索引。模型、应用、key、工作区和 `(error_kind IS DISTINCT FROM '')` 各有 `(account_id, 筛选值, at DESC, id DESC)` 索引；请求 ID 复用主键。状态表达式使用 `IS DISTINCT FROM`（该列 NOT NULL，口径与原 `<>` 相同）：普通 `<>` 的假值在 custom plan 中会被折叠为 `error_kind = ''`，失去表达式索引；DistinctExpr 的取反保留为 NOT，成功与报错都能匹配同一布尔索引。组合筛选由选定索引读候选，再检查其余条件，因此读取量也受候选的选择性影响。`ORDER BY call_log.at, call_log.id` 保持 UUID 索引顺序，不能用输出列 `id::text`。不使用 OFFSET。列表事务另设 `enable_sort=off`：多个稀疏筛选相关时，custom plan 仍可能把 100 个真实命中低估为 1 行，选位图加排序而不是在索引上遇 LIMIT 停止。真实 12 万行的模型+应用稀疏组合曾走该计划；设置只在有序列表及 CSV 读取的事务内，聚合仍允许排序，由优化器按估算选择扫描方式；账户与状态高度相关时仍可能选择顺扫，不承诺聚合只读取匹配行。平台 RLS 的 InitPlan 修正不能消除筛选值之间的相关性。请求 ID 沿主键反向读 `at`，也是有序索引扫描；计划 helper 同时接受正向与反向扫描，保留无位图、无排序和块预算要求。

`AggregateOwn` 复用同一组筛选及 custom plan，忽略游标与页大小；数据库按 UTC 日/模型/key 和货币分组，`sum(bigint)` 用 PostgreSQL numeric 再转文本，不溢出 int64，不将不同币种相加。多于 1000 组返回 `ErrAggregateSize`，处理器答 400。时间窗与 10 秒期限由 console HTTP 边界限制，调用路径不变。导出在 console 处理器里先调用 `CheckOwnExportSize`，再复用 `ListOwn`，两者在同一 `ReadTx` 快照内。探测只计有序前缀的键，SQL 的 LIMIT 为 `MaxOwnExportRows + 1`，超过上限返回 `ErrExportSize`，处理器在写 CSV 前答 400。成功流包含全部匹配行，不使用截断/完成 trailer；边界和安全规则见[用户调用记录查询与导出](../../docs/domains/observability.md#用户调用记录查询与导出)。

`TestListPlansFollowTheIndexes` 使用真实应用角色与 RLS，12 万条记录中一个大账户 6 万条、100 个小账户各 600 条。未筛选、模型、应用、key、工作区与状态页都要求索引顺序，不能有位图、顺序扫描或排序。原有筛选的读取预算：大账户首页和密集筛选最多 40 块，稀疏筛选最多 400 块，无匹配最多 20 块；小账户最多 800 块（候选只有 600 行），仍须索引顺序。深页及新筛选还由下面的共享 helper 检查更紧的首页/深页相对预算；组合筛选的剩余条件可能增加候选读取量，不能承诺所有组合都只碰一页。

深页的游标行比较 `(at, id) < (after_at, after_id)` 必须写在标量时间窗条件前面。PostgreSQL 18 的 B-tree 无法比较行上界与标量 `at < until` 哪个更紧，保留同方向的第一个 required key 作为扫描起点；即使两者都显示在 `Index Cond`，从 `until` 开始仍会逐项检查游标之前的索引叶子。源码见 [`_bt_compare_scankey_args` 和 `_bt_unmark_keys`](https://github.com/postgres/postgres/blob/REL_18_STABLE/src/backend/access/nbtree/nbtpreprocesskeys.c)（2026-10-29 查证）。这与 RLS 估计、custom plan 或索引列顺序无关：原计划已经裁剪到一个月、无排序、返回 51 行，12 万行中深 45,000 行却读 584 块，首页 6 块。

同库对比（12 万行、深 45,000 行）：原查询 584 块，游标条件移到时间窗前为 8 块；事务内索引块读取增量为 580 / 4，两种写法都只取 51 条堆记录。添加冗余的 `at <= after_at` 仍为 584 块，因为没有改变哪个上界作为起点。选择让共用列表查询保持这个条件顺序，并由 `cursorOf` 取游标与 `(until, 全零 UUID)` 中较小的元组上界：全零 UUID 让 `until` 仍然排除该时刻的全部行；首页、历史窗口以及游标在窗口之外时都从更紧的界限开始。保留显式时间窗供分区裁剪。不选删除 `until`：会丢失窗口语义，也会探查所有未来分区。不选收紧标量上界：严格小于游标时间会漏掉同一时间的较小 ID，非严格上界仍可能扫描大量同时间的索引项。

`TestListPlansFollowTheIndexes` 在同一份 12 万行数据里比较首页与深 45,000 行的页：平台与账户两种范围、未筛选/模型/应用三种形态、50 和 200 行页，以及历史时间窗首页、游标等于或晚于窗口末尾、覆盖未来分区的时间窗。50 行页的首页 / 深页为 7 / 8 块，200 行页为 18 / 19 块；历史窗口的页分别为 8、19 块。覆盖未来分区时用 Merge Append 合并有序扫描，分别为 61 / 62、72 / 73 块，额外探查量随未裁剪的分区数增长。测试检查有序索引扫描和 shared hit + read 块预算，不仅检查计划节点名字。深页检查复用原有数据，不单独再写一份历史。`TestListOwnPagesAcrossPartitions` 检查跨月与默认分区、相同时间的 ID 顺序、时间窗的包含/排除边界，以及未筛选/模型/应用三种形态和组合筛选连续翻页不重不漏。

稀疏筛选读的是命中的条数（命中不多于一页就读完），不是账户或分区的量：没有这两个索引时，稀疏和零命中的筛选读整个月分区（36 万条时 7,059 个缓冲，随表增长，60 到 100 ms），不管有没有命中。
控制台把 `Cursor` 写成 32 个字符的不透明文本（`String`：调用开始时间的微秒数 8 字节、调用编号 16 字节，base64url 无填充），`ParseCursor` 读回，不是这个长度或字母表就是 `ErrBadCursor`（控制台答 400）。游标只标记一个账户的调用里的一个位置：伪造一个只会得到范围本来就让读的、从别处开始的一页，所以不签名；它也不绑定筛选条件，换了筛选条件续用同一个游标得到的是按新条件、从那个位置往后的一页。

`TestOwnFiltersAndAggregates` 验证新筛选的单项、组合、游标页与账户隔离；`TestOwnAggregateKeepsCurrenciesSeparate` 保证不同货币不相加，`TestOwnAggregateRejectsPartialTotals` 守住组数上限。`TestListPlansFollowTheIndexes` 还复用 #408 的 `checkListPagePlans`，比较 key、工作区与状态筛选在平台/账户两种范围里的首页和深页，以及无匹配时的有序索引与块预算。跨分区测试也覆盖这些筛选。`TestSparseSuccessReadsFollowTheStatusIndex` 另外反转夹具分布，让 6 万条账户记录中只有 100 条成功，检查平台/账户范围的首页与深页块预算，以及导出探测只读取 100 条匹配记录；避免只用成功密集的样本掩盖反向条件失去索引的问题。

调用记录的可空 `price_version` 是实际收费条目在预留时冻结的售价版本；由 billing 的 `Final.PriceVersion` 传入，随批量记录保存，`ListOwn`、`GetOwn` 与内部追溯原样返回。历史记录和旧节点没写的版本保持未知，控制台显示不可用。

## 后台检索

`ListAdmin` 使用 `AdminQuery` 的请求 ID、账户、站点和 `[Since,Until)` 时间窗，按 `(at,id)` 倒序返回 `CallSummary` 和下一页 `Cursor`。`AdminQuery` 另可带精确模型名和结果（`ok`/`error`，错误即 `error_kind` 非空）；站点、模型、结果三个参数和列表的回显字段都可选，供看板下钻和旧节点滚动发布。HTTP 边界限制 31 天、200 条与 10 秒；SQL 仅选择客服可见字段，不读取成本、端点或 route。账户过滤复用既有账户有序索引；无账户且无请求 ID 过滤用迁移 00011 的 UTC 时间表达式索引；请求 ID 用主键。沿用列表的 custom plan、禁用排序和复合游标上界顺序，避免深页从窗口末端重扫。`TestAdminCallsFilterAndPage` 覆盖同时间 ID、账户/请求组合、站点、分页和 RLS；`TestAdminCallsPlansUseIndexOrder` 在既有 12 万行夹具检查全局与账户页的有序计划和读取预算。

完整 `GetTrace` 仅由 MFA 加平台 `calls.inspect` 的管理接口返回；客服平台 `calls.search` 不授予该权限。全局索引使用 `(at AT TIME ZONE 'UTC',id)`，全局查询以同一表达式排序和设游标上界，仍保留 at 时间窗做分区裁剪；UTC 无夏令时，因此顺序与原始时间一致。否决普通 at 索引：它会与账户索引竞争，真实 12 万行稀疏成功夹具曾读 2217 块、导出探测扫描 120003 行。账户和请求 ID 筛选继续用原始 at 排序，表达式索引不参与这些有序计划；既有稀疏和深页测试守住回退。

内置 admin 在迁移 00017 通过 `additional_permissions` 得到 `calls.search`、`calls.inspect` 和 `audit.view`，旧节点忽略新权限，support 只得到 calls.search；不复用控制台 calls.view，防止平台 member 授予扩大为全平台检索。摘要与详情响应字段均可选，滚动发布期间旧节点没有这些入口时应显示不可用。

### 运营看板

`ReadAdminOverview` 在一个事务里用同一站点和 `[Since,Until)` 窗口读三组数：总调用数与错误数（`error_kind IS DISTINCT FROM ''`），以及调用最多的前 10 个账户和模型（按调用数排序，同数分别按账户 ID、模型名）。三条 SQL 共用谓词：站点、`at` 范围，以及 `(at AT TIME ZONE 'UTC')` 的同一范围，让分区裁剪和迁移 `00011` 的表达式索引都生效。`TestReadAdminOverviewCountsGroupsAndLimits` 覆盖过滤、分组、前 10 截断和排序；`TestAdminOverviewPlansReadTheWindow` 在 12 万行夹具上要求无 Seq Scan、使用索引、读取预算不超过 1500 块。汇总字段与两个列表在合同里都可选，旧节点没有时页面显示不可用而不是 0。

`CallCoverage` 给看板和调用列表同一套判断：它读 `call_log` 的叶子分区名，记下还在的月份和最老的那个（`Since`）。`Covers(since, until)` 逐个检查窗口触及的整月，缺任何一个月就说不完整；所以较老月份归档失败、较新月份仍被删除时，最老分区还在，窗口也会因中间的空洞标为不完整。响应带 `calls_incomplete` / `calls_available_since`（调用列表为 `incomplete` / `available_since`），`available_since` 是最老的剩余月分区，它之前确定已归档；页面据此说明数字或列表不完整而不是把归档当成零；收入、成本和现金来自预留与账本，不受这个判断影响。一个命名月分区都不剩时 `Known` 为 false，任何窗口都不算完整。`TestCallCoverageStartsAtTheOldestPartition` 删掉 10 月分区后核对边界、看板和列表都只统计 11 月并报不完整；`TestCallCoverageChecksTheWindowForGaps` 保留 10 月、删掉 11 月，核对最老分区仍是 10 月、只落在 10 月的窗口完整、跨过 11 月或整个落在 11 月的窗口不完整；`TestCallCoverageWithNoMonthLeft` 删光所有命名月分区，核对覆盖变为未知。

## 追溯

一条调用、它的每次尝试和它的账本交易只靠一个键相连：请求 ID（调用编号）。它已经是 `call_log.id`、`call_attempt.request_id`、`billing.reservations.request_id` 和 `billing.transactions.request_id`，suppliers 的扣费也按请求 ID 加尝试序号关联（[记录与告警的「追溯」](../../docs/domains/observability.md#追溯)）。

- **调用和尝试**：`GetTrace(ctx, tx, id)` 按请求 ID 读调用行，再按同一个 ID 和调用的 `at` 读尝试，按 `attempt_seq` 排序；返回的就是写进去的 `CallRecord`，带 `Attempts` 和 `Route`。**调用行只按 `id` 查、不带 `at`，所以每个分区各探查一次主键索引**（和 `GetOwn` 一样，次数随保留的月份增长）；返回的 `at` 再用来只读尝试所在的那一个分区。调用行不存在时返回 `pg.ErrNoRows`。
- **谁能调用**：它是运营和管理读的链，不是用户的接口：它带成本、端点和路由。**调用方必须用平台范围，并且只在运营或管理的处理器里调用**；`pg` 没有能判断范围的接口，函数本身拦不住，账户范围在数据库层面读得到自己调用的这些字段（用户读 `ListOwn`/`GetOwn`，那两个没有这些字段）。
- **路由过程**：调用行的 `route` 列，内容是 routing 的 `Record.JSON()`：被排除的候选和原因、选定的路线、每次尝试的结局。尝试不重复存路由。
- **账本交易**：由 billing 的 `LedgerOf(ctx, tx, id)` 按同一个请求 ID 读，每笔交易带自己的分录。observability 不读 billing 的表，也不引用 billing（依赖表里 observability 在 billing 之上），所以「串起」由调用方把两个函数的结果合起来：三个查询、两个模块，各读自己的表（[总体架构的「模块边界规则」](../../docs/architecture.md#模块边界规则)）。
- **不在调用记录里存账本 ID**：`billing.entries` 没有自己的 ID，`ledger_entry_ids` 里的值从来不存在，也没有写入方。抄交易 ID 同样不行：调用记录异步写，校正还会在记录写好后再记交易，抄下来的链接会旧；记录在本地 spill 里等写库时账本已经提交，抄下来的交易看起来会比记录新。请求 ID 是账本自己的键（`transactions` 的唯一约束以它开头），按它读总是完整、最新。这一列留在表里（`NOT NULL DEFAULT '{}'`），新代码既不读也不写；按[平台的迁移规则](../platform/README.md#迁移)，等停止使用它的版本发布之后再删（#204）。本次发布仍不删：还需要一个已发布的版本先停止读写它，滚动发布期间旧节点可能仍在写。
- **不变量**：尝试序号从 1 连续到 `len(Attempts)`，调用行的 `attempt_count` 等于它（写入时算好）；`route` 是 JSON 对象；`LedgerOf` 的每笔交易 `request_id` 等于调用 ID、`attempt_seq` 是 0（请求本身）或 1 到 33（某次尝试），每笔交易的分录合计为 0（billing 写入时断言）。测试：`TestTraceReadsTheWholeChain`（cmd/axiom，走一次真实结算）与 `TestCallAndAttemptsRoundTrip`。

## 分区

列表保留标量时间窗供分区裁剪；游标行比较负责索引起点，两者的条件顺序见[读](#读)。新分区必须与父表采用相同的策略级 InitPlan 表达式；`pg.Check` 同时审计父表与分区，拒绝旧的逐行范围函数。

迁移给 `call_log` 和 `call_attempt` 都建了 2026-10 到 2028-12 的月分区和一个默认分区，两表的月份一一对应，按调用开始的 `at` 分区。父表和已有分区归 `axiom_retention_job`：PostgreSQL 只让表的属主建、删它的分区，`axiom_app` 不能是属主（启动检查会拒绝能成为属主的登录角色）。迁移的第一步建这个 NOLOGIN 角色并授予 `axiom_owner`（继承并带 ADMIN，后续迁移仍能 `ALTER TABLE` 父表），部署的 `roles.sh` 建 `axiom_retention` 登录角色、并在每次启动时补 `GRANT axiom_retention_job TO axiom_retention`（迁移时角色还不存在的库，之后启用也不会漏授权）；迁移在角色已存在时也做同样的授予。`axiom_app` 不是任务角色的成员：否则应用里的任意 SQL 都能 `SET ROLE` 过去，自己关掉行级安全或删掉分区。任务用 `AXIOM_RETENTION_DATABASE_URL` 开自己的连接池（`pool_max_conns=2`，串行），启动时在 10 秒内实际 `SET ROLE` 并检查 schema/表权限，缺授权就拒绝启动；`axiom_retention_job` 只多拿 schema 的 USAGE/CREATE、两张表的全部权限、`retention` 的 SELECT 和 `platform` 的 USAGE；没有 BYPASSRLS，读分区时照样过策略。

没有 `AXIOM_RETENTION_DATABASE_URL` 时任务不登记：分区只到迁移预建的 2028-12，之后写默认分区；配了对象存储却没有这个变量则启动失败，免得“归档开着”其实没跑。

`RegisterRetention` 在 `archive` 队列每天运行一次（唯一键让上一轮未结束时下一轮跳过），每次：

1. **预建**当月和未来 12 个月的分区，两张表各一次。先查 `to_regclass`，已存在的不建、不加锁、不重写策略；新建的在同一事务里 `ENABLE/FORCE ROW LEVEL SECURITY` 并建与迁移 `00005` 相同的 InitPlan 策略。超出预建的调用仍进默认分区，不丢。
2. **读窗口**：`hot_days` 为 0 时只预建；没有对象存储时同样只预建，并记一条日志说明为什么不删。都不会删任何东西。
3. **列出整月**已经早于 `now - hot_days` 的叶子分区（`pg_partition_tree`，只认 `call_log_YYYY_MM` / `call_attempt_YYYY_MM`；默认分区不认）。按时间从旧到新处理，每月一次。
4. **归档**：用 `COPY (SELECT * FROM 分区) TO STDOUT` 以 CSV（带表头）导出，gzip 后按 `call-log/v1/<YYYY-MM>/call_log.csv.gz`、`call_attempt.csv.gz` 上传，最后写 `manifest.json`：版本、月份、格式、每张表的行数、字节数、SHA-256 和列名/类型。清单最后写，存在即证明两份对象都已上传；`confirm` 再逐个 HEAD 核对清单里的字节数。
5. **删除**：确认后在同一个事务里先对两张现存分区加 `ACCESS EXCLUSIVE` 锁、精确核对行数，再删 `call_attempt` 的分区、后删 `call_log` 的。行数比清单多说明快照后有迟到写入，事务回滚并重写整月；锁一直持到提交，计数和删除之间没有写入窗口。上传、核对、存储或锁失败都不删；一个月的失败不挡其他月份，错误交回 River 重试，最多重写三次后留给下一轮。

归档删除的是整月分区，所以 PostgreSQL 里 `call_log` 是否能完整回答一个窗口，取决于窗口覆盖的每个整月是否都还在；`CallCoverage` 记下还在的月份，下文的「运营看板」用它按窗口判断缺月，`Since` 则是最老的剩余月分区。

`pg.Tx.CopyTo` 是 COPY 的出口：语句里的分区名只来自上面校验过的月份，数据本身从不拼进 SQL。归档文件先落到 `TMPDIR` 下的临时目录（`os.CreateTemp`），因为 `objectstore.Put` 的 body 必须可寻址；成功或失败都会删掉临时文件。程序镜像是 scratch、根文件系统只读，部署因此给 `TMPDIR=/retention-tmp` 一个由 `keys` 服务 chown 给 65532 的独立卷；没有这个卷时第一次归档会因建不了临时文件而失败，不会误删分区。归档对象没有到期删除：调用记录「全部保存，不能关闭」指的是不丢弃，热窗口只决定 PostgreSQL 里留多久，离开的月份仍在桶里。

对象存储没配置（`objectstore.ReadConfig` 返回零值）时归档关闭，任务只预建分区。读取归档还不是产品接口：`GetTrace` 对已删除的月份返回 `pg.ErrNoRows`，控制台显示为空/404；清单和列类型足以离线恢复，按需再做导出或查询接口。

## 索引

`call_log` 在每个分区上有九个索引：主键 `(id, at)`；不筛选的 `(account_id, at DESC, id DESC)`；模型和应用筛选（迁移 `00002`），以及 key、工作区与报错布尔表达式筛选（迁移 `00009`）各一个 `(account_id, 筛选值, at DESC, id DESC)`；迁移 `00011` 的全局后台检索 `call_log_admin_time`（UTC 时间表达式与 id）；迁移 `00014` 检测器专用的 `(at, account_id)` 部分索引，谓词 `price_nano >= 0` 让按账户的控制台查询不会选中它。这些新增索引每次写入各增加一条索引项，不在 gateway 的同步调用路径里；持续写入吞吐与索引总体体积仍待压测。

`call_attempt` 只有主键索引 `(request_id, at, attempt_seq)`：按请求查它的尝试走这个前缀，保留任务按分区整块处理；没有按尝试的列表查询，所以暂不加别的索引。它的写入量是调用行乘以每次尝试，见下文的写入放大。

- **选择**：比较了四种做法，量的是 36 万条（表 83 MB，原有索引 50 MB）。(a) 不加：稀疏和零命中读整个分区（7,059 缓冲，随表增长）。(b) 在账户索引上加 `INCLUDE (model, client_app)`：计划不变，仍然读整个分区，没有用。(c) 只加模型索引：模型筛选解决，应用筛选仍读整个分区。(d) 模型和应用各一个：两种筛选都读命中的行（见上表）。取 (d)，是让两个筛选都有界的最小做法。
- **写入放大**：每个索引每行多一条索引项，36 万条各多约 41 MB（表加索引合计 133 MB 到 174 MB 到 215 MB，每个约 +31%）；批量写入 12 万条的最快一次是 4.6 s、3.9 s、7.9 s（无、一个、两个；机器噪声约三成，体积是确定的）。调用记录由后台按批写（满 500 条立即写，否则最多攒 200 ms），不在请求路径上；持续吞吐取决于调用到达的速率和数据库，没有固定上限。spill 满时准入拒绝新调用，所以写入持续跟不上时会拒绝而不是少记录；代价是 `call_attempt` 的每个尝试一行和它的主键索引，筛选索引在持续满批写入下的吞吐**待验证**，随[性能](../../docs/performance.md#怎么验证)的压测一起量。如果运营中发现应用筛选很少用，删掉 `call_log_account_app_at` 是一条只减不增的迁移。
- **迁移方式**：加索引兼容旧程序（旧程序的查询没有它也对），按平时的顺序先迁移再发新程序。分区表的 `CREATE INDEX` 在每个分区上建一个，建的时候对该分区加 SHARE 锁，挡住写入器的插入，时长随该分区的行数增长（12 万条约 1.7 秒）；分区表的父表不支持 `CONCURRENTLY`。`00002`、`00009`、`00011` 与 `00014` 用普通的 `CREATE INDEX`，只适合表还小的时候，也就是 `call_log` 第一次部署时。**已经有大量数据的调用记录再加索引**，迁移要这样写：标 `-- +goose NO TRANSACTION`；`CREATE INDEX 名 ON ONLY observability.call_log (...)`（建出无效的父索引，不锁写）；对每个分区 `CREATE INDEX CONCURRENTLY 分区索引名 ON 分区 (...)`（不挡写；`migrate` 的 5 秒 `lock_timeout` 对它等老事务也生效，超时就失败，留下无效索引，要先删掉再重跑）；再 `ALTER INDEX 父索引 ATTACH PARTITION 分区索引`，全部挂上后父索引才生效。新建的月分区自动带上父索引。

## 决策与理由

| 决策 | 理由 | 否决的做法 |
|---|---|---|
| 另建 `call_attempt`，每次尝试一行 | 调用记录要能串起每次尝试（[记录与告警的「追溯」](../../docs/domains/observability.md#追溯)）；suppliers 的 `attempt_costs` 只在结算事务里写，失败或崩溃的请求可能没有可查的尝试；单独的表让尝试有类型、时间和结果，并按 `(request_id, attempt_seq)` 连到供应商的扣费。负责人已在对话中把设计决定交给开发者（Issue #29） | 只存 `route` JSON：没有类型和耗时，查一次尝试要解 JSON；把尝试嵌进 `call_log`：一个请求一行会被撑大，按尝试查询和保留都更难；让 suppliers 直写：跨模块写同一张表，违反模块边界 |
| 调用行的 `endpoint_id`、`source_class`、`egress`、`attempt_count` 取自最后一个尝试 | 一处事实：调用行与尝试表不可能不一致；网关也不用把同样的值填两遍 | 调用记录再带一份端点、来源和出口：两套值迟早对不上 |
| 调用、尝试和账本交易只用请求 ID 关联，`ledger_entry_ids` 不再读写 | 分录没有 ID，这一列从来没有写入方；请求 ID 已经是账本自己的键，照抄一份会在调用记录丢失或校正追加交易时与账本不一致。列按平台的迁移规则留到停止使用它的版本之后删除 | 抄交易 ID：要结算把 ID 带进异步记录，仍会缺、会旧；让 observability 查 billing 的表：违反模块边界 |
| 准入预占 + 本机 WAL + 顺序批量入库，spill 满时拒绝新调用 | 记录在响应结束前已追加到本机段文件，进程崩溃可重放，数据库慢不拖慢已完成调用；磁盘和接收量都有硬上限，满了拒绝而不是丢。`Commit` 等一轮组提交 fsync 才返回，主机断电也不丢已回复的调用；并发调用共用一次 fsync，调用路径的实测代价见[性能的「调用路径上的规则」](../../docs/performance.md#调用路径上的规则)。否决在结算事务里写调用记录：它虽然和结算同一原子提交，但会让一条日志记录的约束或磁盘故障失败整笔钱的结算，`SettleBatch` 还要为每笔调用多带一行日志，日志表因此进入钱的临界路径；也否决同步数据库插入：同样把数据库延迟放进调用路径，且崩溃会丢掉已结算未插入的记录。否决无界队列：内存无上限；否决队列满丢新记录：违反「全部保存」 |
| 整批行作为一个 JSON 数组传给一条 `INSERT … SELECT` | 一次往返、一个参数；类型转换和检查留给表。sqlc 不认 30 多个参数的 `unnest`，也没有 `:copyfrom`（`pg.Tx` 没有 `CopyFrom`） | 逐行 `INSERT`：每条一次往返；列式 `unnest`：生成不了 |
| 金额存最小单位的整数，币种单列 | 与 billing 的存储一致（[计费的「钱的表示」](../../docs/domains/billing.md#钱的表示)），汇总不丢精度 | 浮点数、字符串 |
| 默认分区加预建月分区 | 记录不能丢，写入不能因缺分区失败；预建的月数让保留任务有时间落地 | 调用路径上建分区：`axiom_app` 没有权限，也会在热路径上拿锁；没有默认分区：缺分区时 INSERT 失败并退避重试，积压到容量上限后拒绝新调用，不能把记录当坏行丢掉 |
| 终端用户标识用 HMAC，类型上只能由 `HashEndUser` 生成 | 无密钥的哈希能被穷举还原；类型让原始标识符无法误入 | 在调用方各自哈希 |
| 重试到成功；数据错误不跳过，标记不可记录并拒绝新调用 | 数据库短暂不可用不应丢记录；跳过一次坏行会让它之后的所有调用无记录地服务。停写并拒绝新调用把故障变成可见的 503，记录留在 spill 等修复 | 有限次重试后丢弃：数据库的短暂故障也会丢；坏行隔离：违反「全部保存」，且一条坏行会让后续调用静默缺记录 |
| 调用记录按月归档到对象存储，确认后才删 PostgreSQL 分区，窗口是管理员数据 | 整月处理，不逐行；对象存储不可用或窗口为 0 时全留，宁可多占也不能在没有确认副本时删。归档对象不删，「全部保存」仍成立；账本、汇总、审计各按自己的规则保留，不跟着调用日志走。 | 按行 TTL/删除：与分区表的整块转出和主键、索引都不合，还会在热路径上增加判断；不归档直接删：丢失无法恢复；把归档放进 billing：违反模块边界，也会让账本依赖日志；按日分区：分区数是现在的三十倍，默认分区和索引维护都更碎 |
| 异常状态和阈值各用一张平台表 | 状态必须跨节点、重启和告警配置替换保持“一次异常只通知一次”，所以不能放 `alert_windows`（替换配置会清空）；阈值是管理员数据，独立于规则 body，旧节点整体替换时不丢。两者都只有平台范围能读写。 | 把状态放 `alert_windows`：配置变更丢状态；把阈值放进规则：旧节点替换会丢；只在内存去重：多节点和重启会重复通知 |

## 账户花费异常检测

`spendWorker` 由 `RegisterAlerts` 注册为 `observability.spend`，每 10 分钟在 `notifications` 队列执行一次，单个事务完成检测、状态、信号和用户提醒。检测以传入的 `now`（生产为 `time.Now`，测试为受控时钟）切出最近窗口 `[now-W, now)` 和基线 `[now-W-B, now-W)`；窗口和基线秒数来自 `spend_detection`。只取最近窗口里有调用的账户，再按账户聚合，查询不会扫全部分区。花费按币种分别比较；来源网段用 `network(set_masklen(ip,16))`（IPv4）和 `network(set_masklen(ip,32))`（IPv6）近似，主机位归一化后再分组，同一网段换地址不算新地区，不引入 GeoIP 依赖；模型按 `model` 分组。三种异常共用 `observability.spend_anomaly`，严重度 warning，键为 `<account UUID>/<kind>`，这样同一账户的三种信号各有独立事件，用户提醒按账户合并为一封。

阈值和检测开关是后台数据：默认开启、提醒生产关闭、1 小时窗口、24 小时基线、5 额度下限、3 倍、陌生网段/模型各 5 次；管理 API 的 `spend_detection` 整体读写，校验范围和交叉约束。检测对象为空时不改已存值，关闭检测时恢复所有现存状态，避免留下没人处理的事件。用户提醒走 accounts 的 `RecordSpendAnomalyNotices`，偏好 `spend_anomaly` 默认开启，但生产由 `spend_detection.notify_users` 控制、默认关闭：等全部节点认识这个种类后再打开，避免旧派发器把它标记成已发送却发不出去；同一异常期间同一账户只写一条；`source` 是每次异常生成的随机值，`user_notices` 的唯一键让重试和并发不重复。自动暂停 key 不在这里，key 状态归 accounts，检测不碰调用路径。

## 告警接口与不变量

账户的 `accounts.authentication_failures` 是账户后台按站点每分钟至多上报一次的完整分钟峰值，只有达到已配置规则阈值才创建事件。消费每条样本时立即评估该事件并持久安排符合规则的首通知，再处理后续低值或恢复；因此消费积压不会把已完成的攻击窗口覆盖掉。低于所有阈值的样本或零窗口结束事件，实际通知派发仍在消费事务之后。账户负责限速、计数和脱敏，本模块沿用持续时间、静默、重复和通知机制。

`platform/alert.Sink` 的 Raise/Resolve 是非阻塞、有界的本机入口；`alert.Persist` 才是数据库持久边界。`RegisterAlerts` 注册 River 周期消费者（告警、Webhook 和 `observability.spend`）；消费者的时钟字段供测试控制持续时间、静默和重复判断。迁移：platform `00012` 保存待消费信号；observability `00004` 保存规则/渠道配置、事件和投递回执，`00008` 扩展冻结的 Webhook 正文、封存签名密钥和渠道测试位置，`00014` 保存检测状态、阈值和按时间取账户的索引，`00017` 保存回执的展示字段快照；platform `00015` 保存来源消费位置。

比较方案：直接让模块引用 observability 并同步建事件，接口较少，但倒置模块依赖且故障进入业务事务；选择平台信号表和 observability 消费，增加一跳延迟，换来独立事务和跨节点接管。消费锁覆盖信号转事件的有界数据库批次及完整安全样本和支付窗口样本的即时评估；事件字段保留当前样本供规则判断，回执在创建时冻结展示字段，后续低值可重置持续条件而不改写待发通知；常规周期评估、派发和保留清理另开事务，待发回执用 FOR UPDATE SKIP LOCKED 认领，邮件和 Webhook 网络请求在事务之外。Webhook 的 10 秒期限只包出站请求，回执读写使用外层作业期限。邮件回执由 mail 的注入回调写入本模块，mail 不引用 observability。

不变量：同一种类和键只有一个未恢复事件（部分唯一索引）；同一事件、规则和通知序号只有一条回执（唯一约束）；基础设施失败回滚当前阶段，已提交事件仍在待评估索引中；无效信号留在信号表的 quarantined 行供排查，不堵后续信号；发送成功后才写 delivered；恢复通知等之前的发送进入终态；首通知失败则只留 failed 恢复回执；静默只推迟发送，不删除事件；无渠道不丢事件；所有新表 FORCE RLS，仅平台范围可见。`webhook_keys` 的触发器 `fence_envelope_key_version` 拒绝写入已退役的 `key_version`（见[平台的凭证加密](../platform/README.md#凭证加密)）。评估读配置时持有 FOR SHARE，替换配置的 UPDATE 与其互斥，避免旧规则留下新窗口。管理替换配置以 rev 做乐观锁，拒绝悬空渠道引用。同一种类不能有两条规则指向同一渠道。待发通知保留渠道快照，后续配置替换不改变它的收件人。

测试用真实 PostgreSQL、受控时钟和显式调用消费：1000 次同键 raise、两个生产者同时写入、恢复、阈值/持续时间、跨午夜静默、邮件成功/失败回执、配置修订与范围隔离。网络发送允许至少一次，去重保证通知任务只安排一次。Webhook 通过 `observability.webhook`（notifications 队列）网络失败最多尝试五次，最终失败写回执后正常完成；无密钥或非法目标直接记 failed，不再触发 queue.discarded，参数只有回执 ID；派发时冻结 JSON 正文，读取时验证密钥的渠道与目标地址绑定，每次发送重新签时间。签名协议及接收端要求见[告警设计](../../docs/domains/observability.md#告警信号事件与规则e5-3)。`ValidateAlertWebhookURL` 是配置和管理面 DNS 预检共用的 URL 校验。`RotateAlertWebhookSecret` 使用配置修订号串行轮换，旧密钥一小时内继续签名，窗口内不能再次轮换，成功设置后令该渠道下一轮探测立即到期；信封行的主键由随机 UUID 与渠道/目标的摘要组成，读取时重建并验证，主密钥的轮换清单按该完整主键重包裹。过期密钥会清理，已删除目标的密钥保留到引用该目标的未恢复故障及其待发、恢复回执全部收尾。

每分钟在消费者阶段读取最新周期测试的终态回执，以渠道测试行的锁和 reported_state 去重发布健康信号，删除渠道时同阶段发恢复并删除探测位置，再消费信号；发送回调只写回执，不在持有事件锁时获取信号锁。评估阶段先独占配置，再安排到期的小时探测、评估并派发；这避免并发节点同时排探测以及回执/消费锁反序。探测失败和恢复也走普通规则，但不发送到自身故障渠道。超过 24 小时、已没有存活 River 任务的 queued 回执会转 failed；用尽重试且被 River 救援直接丢弃的作业由既有丢弃扫描完成回执。事务执行测试使用 `pgtest.WorkJob` 包装 River 官方测试执行器，真实提交重试状态并注入时钟，不依赖后台调度轮询；端到端供应商测试另运行真实队列。测试覆盖重试后一次接受、错误密钥与正文篡改、轮换到期、私有地址/重定向拒绝、并发探测和晚到结果；测试按明确数据库状态和受控时钟推进，无固定睡眠。

支付通知没有预置规则。启用时在现有配置中追加 `payments.discrepancy` 与 `payments.query_paused` 两条规则，各用唯一 id，最低严重度 warning、threshold=1、duration_seconds=0、repeat_seconds=0，channel 指向已测试的邮件渠道；使用当前 rev 保存并保留其余配置。仅绑定五条默认规则不会发送支付通知。事件持久化与渠道启用是两步，未配置不丢事件。

`pending_recovery` 表示恢复及其投递尚未收尾；部分索引 `(checked_at,id)` 只含未恢复或待收尾事件。收尾后进入按 `(resolved_at,id)` 排序的清理索引，30 天后由周期任务每批删 100 个，外键级联删除回执和窗口。回执按页内事件 ID 一次查询，每个事件沿索引最多取最近 100 条。
