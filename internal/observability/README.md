# observability

调用记录、对话内容、告警和花费异常检测的代码接口与不变量；行为见[记录与告警](../../docs/domains/observability.md)；指标与链路的基座在 [platform 的「遥测」](../platform/README.md#遥测)。比较过的做法和实测在[存档](../../docs/archive/observability.md)。

本模块的数据库结构由 [`00049_baseline.sql`](migrations/00049_baseline.sql) 创建；编号、等价验证与开发库重建规则见[平台迁移说明](../platform/README.md#迁移)。

## 接口

```go
func Module() pg.Module
// 写
func NewRecorder(ctx, db *pg.DB, scope pg.Scope, cfg Config, logger *slog.Logger) (*Recorder, error)
type Ledger func(ctx, tx pg.Tx, ids []uuid.UUID) (map[uuid.UUID]Amounts, error) // Config.Ledger，cmd 接 gateway.LedgerAmounts
func (r *Recorder) Admit() (*Slot, error)        // ErrBacklog：spill 满；ErrBroken：已无法记录
func (s *Slot) Commit(rec CallRecord) error      // 返回时已 fsync；待定记录改为停放，Slot 不关
func (s *Slot) Release()                         // 没有记录时归还位置；有停放的待定记录时把它移进 spill
func (r *Recorder) Enqueue(rec CallRecord) error // Admit + Commit
func (r *Recorder) Close(ctx) error              // 写完 spill；ctx 到期返回 ErrPending
func (r *Recorder) Done() <-chan struct{}
func (r *Recorder) AlertSample() (int, int64)    // 待写数、累计丢失数
func SpillBacklog(dir string) (int64, error)
func HashEndUser(key []byte, account uuid.UUID, id string) EndUserHash
// 用户读（账户范围事务）
func ListOwn(ctx, tx, OwnQuery) ([]OwnCall, *Cursor, error)
func GetOwn(ctx, tx, id uuid.UUID) (OwnCall, error)
func ReadOwnDetail(ctx, tx, call OwnCall, logger) (OwnCallDetail, error)
func ActivityOwn(ctx, tx, ActivityQuery) ([]ActivityItem, error)
func AggregateOwn(ctx, tx, OwnQuery, groupBy string) ([]OwnAggregate, error) // ErrAggregateSize
func ListOwnSessions(ctx, tx, OwnQuery) ([]OwnSession, *Cursor, error)     // ErrSessionSize
func CheckOwnExportSize(ctx, tx, OwnQuery) error                           // ErrExportSize；MaxOwnExportRows
func ReadUsageStatistics(ctx, tx, UsageQuery) (UsageStatistics, error)
func ValidateUsageWindow(since, until time.Time, granularity string) error // ErrUsageWindow
func ParseCursor(s string) (Cursor, error)                                 // ErrBadCursor
// 运营与管理读（平台范围事务）
func GetTrace(ctx, tx, id uuid.UUID) (CallRecord, error)
func ListAdmin(ctx, tx, AdminQuery) ([]CallSummary, *Cursor, Coverage, error)
func ReadAdminOverview(ctx, tx, site uuid.UUID, since, until time.Time) (AdminOverview, error)
func CallCoverage(ctx, tx) (Coverage, error); func (c Coverage) Covers(since, until time.Time) bool
// 保留、汇总与任务
func GetRetention(ctx, tx) (Retention, error); func SetRetention(ctx, tx, Retention) error // ErrRetention
func CheckRetentionRole(ctx, db, scope) error
func RegisterRetention(RetentionConfig) error
func RegisterUsage(db, scope, q *pg.Queue) error
func RegisterAlerts(db, scope, q, m *mail.Mailer, keys *secret.Keyring, exit *egress.Client) error
// 告警配置
func GetAlertConfiguration(ctx, tx) (AlertConfiguration, error); func SetAlertConfiguration(ctx, tx, AlertConfiguration) error
func ListAlertEvents(ctx, tx, AlertEventPage) ([]AlertEvent, error)
func SendAlertTest(ctx, tx, channel string, now time.Time) (uuid.UUID, error)
func ResolveAlertEvent(ctx, tx, id uuid.UUID, now time.Time) error
func RotateAlertWebhookSecret(ctx, tx, keys, channel string, rev int64, value secret.Secret, now time.Time) (uuid.UUID, error)
func ValidateAlertWebhookURL(raw string) (string, error)
// 对话内容（平台范围）
func StreamContentMeta(ctx, tx, ContentQuery, each func(ContentMeta) error) (ContentStats, error)
func ReadContentKey(ctx, tx, keys *secret.Keyring, account uuid.UUID) ([32]byte, error)
func NewContentReader(keys ContentKeySource, objects ContentObjects) (*ContentReader, error)
func (r *ContentReader) Open(ctx, ContentMeta) ([]byte, error) // ErrNoContentKey
func ValidContentPart(part string) bool; func (m ContentMeta) TarPath() string
```

`CallRecord` 是一次调用和它的 `[]AttemptRecord`，结算金额在内嵌的 `Amounts`，待定记录（`BillingPending`）没有它。sqlc 代码在 `internal/store`，包外引用不到。`GetTrace` 带成本、端点和路由，只能从运营或管理的处理器以平台范围调用：函数本身判断不了范围。`OwnCallDetail` 里的内部 ID 必须由 HTTP 层换成公开代号，不能直接序列化。

## 表

schema `observability`，全部强制行级安全。

- `call_log`：一次调用一行，按请求开始时间 `at` 月分区，主键 `(id, at)`，`id` 是请求 ID。没有 NULL：可选文字 `''`，没有首字时 `first_token_ms` 为 0，没有尝试时 `endpoint_id` 为全零 UUID，`physical_input` 未知为 −1（读取时转成可空）。`route` 是 routing 的 `Record.JSON()`，不超过 8192 字节。
- `call_attempt`：一次尝试一行，按调用的 `at` 分区，主键 `(request_id, at, attempt_seq)`；`outcome` 是 `committed`、`failed` 或 `unknown`；成本在 suppliers 的 `attempt_costs`。
- `usage_hourly`、`usage_journal`、`usage_backfill`：用量小时汇总、待汇总事件和单例回填位置；金额和计数是 numeric。
- `retention`、`spend_detection`：单行 jsonb 的管理员数据，第一次保存前读代码默认值。`spend_anomalies`：正在发生的花费异常，主键 `(account_id, kind)`。
- `alert_configuration`、`alert_events`、回执、持续窗口、`webhook_keys`、渠道探测位置：告警。
- `content_keys`（每账户一行，封套列 `wrapped_key`，AAD `observability.content_keys/<account_id>/content_key`）、`content_blobs`（`UNIQUE (account_id, digest)`）、`content_refs`（主键 `(request_id, kind, part)`，外键 `(blob_id, site_id, account_id)`）、`content_exports`（`approval_digest` 唯一）。

## 不变量

| 不变量 | 靠什么保证 |
|---|---|
| 租户只读写自己账户的调用和尝试 | 父表和每个分区都 FORCE RLS，策略用 [pg 的 InitPlan 范围参数](../platform/pg/README.md#决策与理由)；写入者用平台范围。`TestTenantsReadOnlyTheirOwnCalls`、`TestTenantsCannotWriteOthersCalls` |
| 调用记录不可改、不可删 | `axiom_app` 对两张表只有 SELECT 和 INSERT；到期只由保留任务按分区整块处理 |
| 同一请求写两次是一行，写入从不因缺分区失败 | 两张表 `ON CONFLICT DO NOTHING`，`at` 是请求开始时间；有默认分区。`TestRecordsAreStoredOnceAndNeverRefusedForLackOfAPartition` |
| 每次尝试有且只有一行，调用行与最后一次尝试一致 | `Seq` 从 1 连续到最多 `platform.MaxAttemptSeq`（33），写入时校验；调用行的端点、来源类别、出口和 `attempt_count` 由最后一次尝试算出。`TestCallAndAttemptsRoundTrip`、`TestALoopsAttemptsAreKept` |
| 一次调用只有一条记录进库，表只插入 | 待定记录停放，被已结算记录替换或在 `Release`、启动时移进 spill；重放的停放文件排在已结算记录之后，被去重挡掉。`TestAPendingRecordIsReplacedOrKept` |
| 结算提交过的待定记录带账本金额入库 | 插入前同一事务经 `Config.Ledger` 读已结算预留，`FOR SHARE` 锁住预留行；`state` 在锁内判断，不放进加锁读的条件，否则会跳过而不等它的写者。`TestASettlementCommittingAfterItWasGivenUpReachesItsRecord` |
| 待定的调用不读成 0 | 账户的读取都带 `NOT billing_pending`；`GetTrace` 和 `CallSummary.Pending` 标出。`TestAPendingCallIsNotReadAsZero` |
| 旧版本碰不到待定记录 | 停放在 `pending/`；移进 spill 的只写进段头 `AXIOM-WAL-2\n` 的段，排空时换掉。`TestPendingRecordsKeepOldBinariesOut` |
| 记录不在过载时丢失 | `Admit` 预占位置（停放的记录也占位置），满了返回 `ErrBacklog`。`TestFullSpillRefusesWithoutBlocking`、`TestAPendingRecordKeepsItsSlotsRoom` |
| `Commit` 返回的记录在断电后仍在 | 见「写入」。`TestWriterReadsOnlyDurableRecords`、`TestNewSegmentIsDurableInItsDirectory` |
| 坏行不被跳过，也不连带丢掉在途调用 | 数据错误（`pg.IsDataError`）或编码失败时保留整批、`ErrBroken`；追加文件等在途调用写完或释放后才关。`TestDataErrorStopsAdmissionWithoutDropping`、`TestInFlightCommitSurvivesFatalBatch` |
| 进 spill 的文字都能入库 | `Commit` 拒绝非法 UTF-8、NUL 和超过 8192 字节的路由；routing 最多逐个记 16 个被排除的端点，合法路由总在上界内。`TestEnqueueRejectsInvalidRecord`、`TestBoundedRouteFitsTheColumn` |
| 没有对话正文、密钥或凭证 | 两张表没有这样的列，文字有长度检查。`TestCallLogHoldsNoContentOrSecret` |
| 终端用户标识只以带密钥的哈希存放 | `EndUserHash` 的数组私有，包外只能经 `HashEndUser` 得到非零值 |
| 用户只读脱敏的尝试与路由 | `OwnRoute` 只投影预设、原因计数、省略数量和标志。`TestOwnAttemptDetails` |
| 归档确认后才删分区，迟到写入不丢 | 见「分区」。`TestRetentionKeepsPartitionsWhenTheArchiveFails`、`TestRetentionRearchivesAfterAWriteDuringAConfirmFailure` |
| 账本、汇总和审计不随调用日志删除 | 保留任务只拼 `call_log_*` / `call_attempt_*` 的分区名 |
| 每条已结算调用恰好累加进小时表一次 | 迁移前的行只经回填、之后的只经触发器进 journal；journal 删除与累加同一条 SQL。`TestUsageRollupRetryLateArrivalAndMoney`、`TestUsageBackfillDrainsLiveJournal` |
| 内容只有平台范围能读 | 四张内容表的策略只允许 `platform.in_platform_scope()`。`TestContentTablesArePlatformOnly` |
| 正文只能被对应账户和 blob 打开 | 对象是 `version(1) ‖ nonce(12) ‖ AES-256-GCM`，AAD 是 `axiom/content/object/v1\0` 加账户、blob 和摘要；对象上限 64 MiB。`TestContentSealOpen`、`TestContentKeyIsBoundToItsAccount` |
| 一个导出批准只能用一次，导出文件没有明文 | `approval_digest` 唯一、读正文前插入；tar.gz 只写进加密流。`TestContentExportRowsAndAudit`、`TestContentExportRoundTrip` |
| 同一 `(kind, key)` 只有一个未恢复事件，同一事件、规则和序号只有一条回执 | 部分唯一索引和唯一约束。`TestAlertStormAcrossNodesAndRecovery` |
| 告警评估不用已替换的规则 | 评估读配置持 FOR SHARE，替换配置的 UPDATE 与之互斥；待发通知保留渠道快照。`TestAlertConfigurationReplacementWaitsForEvaluation` |
| 无效信号不堵后续信号 | 隔离成 quarantined 行 |
| Webhook 密钥不能挪到别的目标，已退役的密钥版本写不进 | 主键含渠道/目标的摘要，读取时重建验证；`fence_envelope_key_version` 触发器（[平台的凭证加密](../platform/README.md#凭证加密)）。`TestWebhookKeyCannotBeRelabelledToAnotherDestination` |
| 一次花费异常只通知一次 | `spend_anomalies` 主键去重，首次插入才持久化信号和提醒。`TestSpendAnomalyDetectsRaisesNotifiesAndResolves` |
| 所有表和分区都过启动检查 | 分区不继承 RLS，各自开启并有策略。`TestStartupCheckAcceptsTheModule` |

## 写入

记录的行为和理由见[记录与告警的「写入路径」](../../docs/domains/observability.md#写入路径)。

- **段文件**：`AXIOM_CALL_LOG_DIR` 下的 `call-<16 位序号>.wal`，段头 `AXIOM-WAL-1\n`（含待定记录的段是 `AXIOM-WAL-2\n`），每条记录是 4 字节长度、4 字节 CRC32C（Castagnoli）和 JSON 正文，单条最多 64 KiB，一段 8 MiB。确认位置在 `call-log.pos`（临时文件、fsync、rename、目录 fsync），整段确认后删除。
- **持久性**：并发 `Commit` 共用一轮组提交 `Sync`。新段先 fsync 段头再 fsync 目录才接收记录；启动时先同步 spill 目录直到根，失败拒绝启动。写协程只读到已 fsync 的位置，确认位置不越过断电后可能消失的字节。fsync 失败后停止追加和读取，由下一次启动按磁盘内容恢复。
- **停放**：待定记录在 `pending/<请求 ID>.rec`，4 字节 CRC32C 加记录字节，临时文件 fsync、改名、目录 fsync，经 `os.Root` 打开；第二条待定记录返回 `ErrClosed`。启动时在写协程开始前把停放的记录追加进 spill 再删除，打断的临时文件删掉，校验和不对拒绝启动（`TestADamagedParkedRecordFailsTheStart`）。`Close` 报告的未写条数含停放的记录（`TestCloseReportsParkedRecords`）。
- **单写者**：`call-log.lock` 上的 `flock` 排他锁；`SpillBacklog` 由备份在节点停止后持共享锁调用，校验同启动但不截断、不删文件（[备份与恢复](../../docs/ops/backup-restore.md#备份)）。`testdata/spill` 是恢复演练用的两份 spill，`TestRehearsalSpillsReplay` 保证新版本仍能重放（`go test -update` 重写）。
- **恢复**：末段的短头或短正文截掉；CRC 不符或中间段损坏时启动失败，报出文件和偏移，文件不动。
- **入库**：普通连接池；每批一个事务，`call_log` 和 `call_attempt` 各一条 `INSERT … SELECT FROM jsonb_array_elements($1)`，成员名就是列名。补全后的批在内存里保留补全值，重试不再读账本；`Ledger` 为 nil（测试）时待定记录照原样写。
- **指标**：`axiom.observability.records.pending`、`records.written`、`records.unsettled`、`attempts.written`、`write.retries`、`records.lost`、`attempts.lost`，丢失的带 `reason`（`invalid`、`spill`、`shutdown`）；`retention.partitions{action,table}`。没有账户、key 或用户标签。`BenchmarkEnqueue`、`BenchmarkEnqueueParallel`、`BenchmarkEncodeRecord`、`BenchmarkSpillAppend` 的分配次数由合并队列比较。

## 追溯

一条调用、它的尝试、它的账本交易和供应商扣费只靠请求 ID（加尝试序号）相连。`GetTrace` 按 `id` 读调用行（每个分区各探一次主键），再用返回的 `at` 只读尝试所在的分区，按 `attempt_seq` 排序；不存在返回 `pg.ErrNoRows`。账本交易由 billing 的 `LedgerOf` 按同一个请求 ID 读，调用方把两者合起来：observability 不引用 billing（[总体架构的「模块边界规则」](../../docs/architecture.md#模块边界规则)）。`LedgerOf` 的每笔交易 `attempt_seq` 是 0（请求本身）或 1 到 33。`TestTraceReadsTheWholeChain`。

## 分区

- `call_log` 和 `call_attempt` 的月份一一对应，迁移预建 2026-10 到 2028-12 和默认分区。父表和分区归 NOLOGIN 的 `axiom_retention_job`；`axiom_owner` 继承它，部署的 `axiom_retention` 登录是它的成员，`axiom_app` 不是（否则应用能 `SET ROLE` 过去关掉 RLS 或删分区）。任务角色没有 BYPASSRLS。
- 保留任务用 `AXIOM_RETENTION_DATABASE_URL` 的独立连接池（`pool_max_conns=2`），启动时 10 秒内实际 `SET ROLE` 并查权限，缺授权拒绝启动；没有这个变量时任务不登记，配了对象存储却没有它则启动失败。
- `RegisterRetention` 在 `archive` 队列每天一次：预建当月和未来 12 个月（已存在的不动，新建的同事务开启 RLS 和与迁移相同的策略）；`hot_days` 为 0 或没有对象存储时只预建；否则从旧到新处理早于窗口的整月：`COPY` 成带表头的 CSV、gzip，上传 `call_log.csv.gz`、`call_attempt.csv.gz`，最后写 `manifest.json`；逐个 HEAD 核对字节数；同一事务对两张分区加 `ACCESS EXCLUSIVE` 锁、精确 `count(*)`，与清单不符就回滚重写整月（一次运行最多三次），符合才先删 `call_attempt` 再删 `call_log` 的分区。任何失败都不删，一个月失败不挡其他月份。
- 临时文件在 `TMPDIR`（部署给 `/retention-tmp` 一个属于 65532 的卷），用完即删；分区名只来自校验过的月份。
- `CallCoverage` 读叶子分区名：`Covers` 检查窗口触及的每个整月，`Since` 是最老的剩余月分区，一个命名月分区都不剩时 `Known` 为 false。

## 索引

`call_log` 每个分区的索引：主键 `(id, at)`；`(account_id, at DESC, id DESC)`；模型、应用和 key、工作区、`(error_kind IS DISTINCT FROM '')` 各一个 `(account_id, 筛选值, at DESC, id DESC)`；全局后台检索的 `call_log_admin_time`（UTC 时间表达式与 id）；花费检测的 `(at, account_id)` 部分索引（谓词 `price_nano >= 0`）；回退的部分索引（`attempt_count > 1`）；会话的部分覆盖索引（排除空会话和待定行）。`call_attempt` 只有主键。

- 列表共用一个查询，可选条件由 `force_custom_plan` 折叠；列表和 CSV 的事务设 `enable_sort=off`，聚合不设。状态条件必须写成 `IS DISTINCT FROM`：`<>` 的假值会被折叠成 `error_kind = ''`，失去表达式索引。
- 游标行比较 `(at, id) < (after_at, after_id)` 必须写在标量时间窗条件之前，上界取 `page.UpperBound` 的较紧者，否则深页从窗口末端逐项扫描（推导见[存档](../../docs/archive/observability.md#深页的游标条件)）。保留显式时间窗供分区裁剪。
- 计划与块预算由 `TestListPlansFollowTheIndexes`、`TestSparseSuccessReadsFollowTheStatusIndex`、`TestOwnSessionsPlanBoundsCallRecords`、`TestAdminCallsPlansUseIndexOrder`、`TestAdminOverviewPlansReadTheWindow`、`TestUsage90DayPlanReadsOnlyRollups` 守住。
- 分区表父表不支持 `CONCURRENTLY`，现有索引的迁移只适合表还小的时候。已有大量数据时再加索引：`-- +goose NO TRANSACTION`；`CREATE INDEX … ON ONLY` 父表；每个分区 `CREATE INDEX CONCURRENTLY`（`migrate` 的 5 秒 `lock_timeout` 也作用于它，超时留下的无效索引要先删再重跑）；再逐个 `ALTER INDEX … ATTACH PARTITION`。
