# PostgreSQL 热备与切换演练

本文是生产 PostgreSQL 的高可用设计、代码要求和切换／恢复演练的 runbook。现状是 `deploy/compose.yaml` 里一个主库，
`deploy/postgres` 的 pgBackRest 只做 WAL 连续归档和时间点恢复（[备份与恢复](backup-restore.md#wal-连续归档与时间点恢复)）；
没有备库，也没有自动切换。本文比较托管 HA 和三个成熟组件，推荐 **Patroni + etcd 的严格同步模式**，并写清费用和它换来的
运维负担。选型和花钱由负责人决定，本文不替代批准。

## 要求

- **不丢已确认的提交**：客户端收到成功、且没有“同步等待被取消”WARNING 的提交，在主备切换后必须还在。已确认的结算和
  预留丢一个，钱就会重复扣或漏扣。
- **有备库就要有同步备库，没有就阻塞写提交**：`synchronous_commit = on`，未取消的提交要等至少一个备库确认 WAL 已落盘；
  没有同步备库时写提交会阻塞，而不是降级成异步继续写。阻塞期间如果 `statement_timeout` 或取消到来，PostgreSQL 可能先发
  WARNING 再返回成功的 `COMMIT`；这不算已确认，必须按未知结果处理（见「保证的边界」）。
- **只提升可能含有全部已确认提交的备库**：提升条件由 HA 组件判断并留下记录，不能由应用或运维凭感觉点一个备库。
- **旧主必须先被隔离**：提升前或提升的同时让旧主不能再接受写；否则两个主同时写，账本会分叉。
- **WAL 连续归档不中断**：切换后新的主库继续归档；时间点恢复能覆盖切换时刻。
- **应用能承受切换**：连接断开、同步等待被取消但 `COMMIT` 返回成功、只读连接、提交结果未知都要有明确处理；不在调用路径上
  无限重试，也不把未知结果当成失败或成功。
- **演练有证据**：上线前做一次真实的切换和恢复演练，记录 RTO、已确认提交、钱、旧主隔离和 WAL 连续性；之后每次改 HA 拓扑或
  数据库大版本都重做。

## 方案对比

费用按 AWS 香港区（`ap-east-1`）2026-10-29 的按需列表价、每月 730 小时估算，不含税、跨区流量和额外快照；预留实例和
Savings Plans 会更低。自建方案的三台机器同时跑 PostgreSQL、Patroni 和 etcd，未另算 etcd 机器。

| 方案 | 严格不丢已确认提交？ | 提升条件与旧主隔离 | 运维负担 | 费用／月 |
|---|---|---|---|---|
| **Patroni + etcd（推荐）** | **复制层是**。3 个 PostgreSQL 节点；`synchronous_mode: quorum`、`synchronous_node_count: 1`、`synchronous_mode_strict: true`，PostgreSQL 18 用 `ANY 1`；未取消时提交等同步备库确认落盘；没有同步备库时写提交阻塞。`/sync` 带 quorum，提升的节点能证明自己有全部可能已确认的提交。 | Patroni 只在 `/sync` 的成员里选主；`watchdog` 设为 `required`、`safety_margin: -1`，leader lock 过期前复位旧主；`failsafe_mode` 只在整个集群互相可达时保主。 | 最高：3 台机器、etcd 仲裁、Patroni、watchdog、每台核心机上的 HAProxy、pgBackRest、操作系统补丁。 | 3 × `m7g.large` + 3 × 100 GB gp3 ≈ **277 美元**。 |
| **RDS Multi-AZ DB instance** | **有条件**。同步备库健康时是；备库失联时 RDS 让主库单独继续，恢复后再同步（AWS 的 Multi-AZ 实现说明），这个窗口里确认的提交可以丢；RDS 没有 strict 开关。 | 主实例不健康时 RDS 自动提升备库，DNS 端点切到新主，旧主被切断并转为新的备库；切换通常 60–120 秒。提升条件由厂商固定。 | 最低：没有操作系统、etcd、Patroni 或 watchdog；自动备份和 PITR、补丁、监控由厂商做。代价是不能跑 pgBackRest、没有超级用户、扩展受限，数据库离开 Compose。 | `db.m6g.large` + 100 GB gp3 ≈ **384 美元**；`db.r7g.large` ≈ **491 美元**。 |
| **CloudNativePG** | **复制层是**。3 个实例；`postgresql.synchronous.method: any`、`number: 1`、`dataDurability: required`；没有同步副本时暂停写。 | Kubernetes Lease 选主 + 隔离检查；fencing annotation 关掉 postmaster；旧主重启后作为副本。 | 需要 Kubernetes：控制面、operator、StorageClass、PersistentVolume、网络和 operator 升级；比前两者多一层平台。 | EKS 控制面 73 + 3 × `m7g.large` 246 + EBS 32 + 负载均衡 18 ≈ **369 美元**，另加 Kubernetes 运维。 |
| **pg_auto_failover** | **默认否**。3 节点（主、备、monitor），同步复制；备库不健康时关闭同步复制继续写，之后主库再坏可能丢已确认的提交；要避免只能在备库故障时阻塞写提交，但这不是它的默认行为。 | monitor 健康检查后提升备库，用 STONITH 隔离旧主；节点失联时也会自隔离。 | 比 Patroni 少一个 etcd，但多一个 monitor；连接路由要 HAProxy 或 libpq 多主机；没有内置 WAL 归档，仍要 pgBackRest。 | 3 × `m7g.large` + 3 × 100 GB gp3 ≈ **277 美元**。 |

代码影响：四个方案都需要同一套连接与重试处理——`target_session_attrs=read-write`、启动时拒绝备库、运行中 25006 的只读检测、未知提交答 503 并交给 `SettleOrphan`；不需要在 `pg.Tx` 里重试。区别只在端点：Patroni 指向本机 HAProxy，RDS 指向 RDS 端点，CloudNativePG 指向 Kubernetes Service，pg_auto_failover 指向 HAProxy 或多主机连接串。

> “严格不丢”列只描述复制层；四个方案都还需要应用识别“同步等待被取消但 `COMMIT` 返回成功”。当前 `pgx.BeginTxFunc` 未处理，不能把复制层的保证当成端到端保证；见「保证的边界」。

结论：**推荐 Patroni + etcd 的严格同步模式**。它比 RDS 每月便宜约 107 美元，但要自己维护三台机器、etcd 仲裁、Patroni、
watchdog 和 HAProxy；换来的是没有同步备库时写提交阻塞，而不是让主库单独继续。RDS 的运维最少，但它的 Multi-AZ 没有 strict 模式：
备库失联时主库会单独继续，恢复后再同步；这个窗口里确认的提交可以丢，和本任务的要求冲突。CloudNativePG 的复制层也满足严格不丢，但要先有 Kubernetes，而且同样要补应用层的同步取消处理；为一个数据库引入控制面不值得。pg_auto_failover 的默认行为
会在备库不健康时放弃同步复制，也不满足。

## 推荐方案的架构

- **节点**：3 台机器，每台跑 PostgreSQL 18.6、Patroni 4.1.5 和 etcd 3；三台分布在同一区域的三个可用区。etcd 先与
  PostgreSQL 同机，是否拆成独立三台在「待定」里定；拆开要加机器和费用。
- **同步复制**：`synchronous_mode: quorum`、`synchronous_node_count: 1`、`synchronous_mode_strict: true`；
  `synchronous_commit = on`。在等待没有被取消时，提交在本地 WAL fsync 且至少一个同步备库确认后才返回；没有同步备库时
  写提交阻塞。`statement_timeout` 或取消可能让 PostgreSQL 先 WARNING 再返回成功的 `COMMIT`；应用必须把这种提交当未知
  （见「保证的边界」），不能当已确认。三节点下 `ANY 1` 能容忍一个备库故障；两个都坏时写阻塞，而不是降级。
- **提升条件**：Patroni 把最新主库、quorum 和同步成员写进 DCS 的 `/sync`；主库故障后，它只在能证明含有全部已确认提交的
  成员里选主。`synchronous_mode_strict` 保证没有同步成员时不会接受新写，避免“先继续写、再提升一个落后备库”。
- **旧主隔离**：每台机器启用 watchdog（`/dev/watchdog`，没有硬件 watchdog 时用 `softdog`），Patroni 配
  `watchdog: {mode: required, safety_margin: -1}`。leader lock 过期前整机复位，Patroni 崩溃或机器挂起时旧主也不能继续写。
  `failsafe_mode` 只在整个集群互相可达时保主；成员之间失去联系就降级或停库，不制造两个主。
- **连接路由**：每台核心机跑一个 HAProxy，按 Patroni REST 的 `GET /primary` 选主，只把连接转到当前主库；
  `AXIOM_DATABASE_URL` 指向本机 HAProxy。HAProxy 只做 TCP 转发，TLS 端到端到 PostgreSQL；健康检查走 Patroni REST。pgx 每次新建连接重新解析和建连，切换后旧连接被丢弃，新连接去新主。
- **WAL 归档**：继续用 pgBackRest 2.58.0。`archive_mode=on`、`archive_command=pgbackrest ... archive-push %p`、
  `restore_command=... archive-get`、`archive_timeout=300`。提升后新的主库接着同一个 stanza 归档，时间线变化由
  pgBackRest 处理；`pgbackrest check` 和 `TestPointInTimeRestore` 覆盖。备份的加密、上传和保留仍按
  [备份与恢复](backup-restore.md)。
- **网络和 TLS**：数据库和 etcd 只在内网，安全组只接受三台数据库机器和核心节点；`sslmode=verify-full`，证书不进仓库。
- **监控**：Patroni `/sync` 和 `patronictl list`、`pg_stat_replication` 的 `replay_lag`、`pg_is_in_recovery()`、watchdog、
  etcd 健康、pgBackRest `info`、连接池取连接失败、SQLSTATE 25006 计数、503 比例、在途预留和账本对账；切换超过 60 秒、
  复制延迟超过 1 秒持续 1 分钟、WAL 归档中断、旧主在切换后仍可写都告警。

## 不丢已确认提交的保证

一次确认的提交要经过这条链：

1. 网关在准入事务里写预留；只有 `COMMIT` 在本地 WAL fsync 且至少一个同步备库确认已落盘，并且没有“同步等待被取消”
   的 WARNING 时，才返回成功（`synchronous_commit=on`，Patroni 维持 `ANY 1`）。
2. 这样的成功提交，WAL 在主库和至少一个同步备库上都有；提升这个备库不会丢它。
3. 没有同步备库时，`synchronous_mode_strict` 让主库的写提交阻塞；不会出现“主库继续确认、但只有它自己有”的窗口。
4. Patroni 只在 `/sync` 的成员里选主，并用 quorum 比较 LSN，保证被提升的节点含有全部可能已确认的提交；旧主由 watchdog
   在 leader lock 过期前复位，不能再写。
5. 如果 `COMMIT` 的结果未知（连接在提交和响应之间断开，或者同步等待被取消但 `COMMIT` 返回成功），网关答 503、不向上游
   发请求，`SettleOrphan` 在后台按预留状态释放或写墓碑；用户重试是新的请求。提交可能已经成功，也可能没有，
   `SettleOrphan` 两条路都收敛到正确的账本。`TestLostCommit` 只覆盖前一种（提交结果丢失/失败）；后一种要新增用例（见演练和
   代码要求）。
6. 结算和孤儿处理按请求编号和预留状态幂等；追加只在一秒预算内重试，重试只会生效一次，见 `TestBatchedSettleSeesTheTerminalStateAfterItsLock`。

**保证的边界（当前代码的缺口）**：Patroni 文档提醒，PostgreSQL 在等待同步确认时如果后端被取消，事务的改动可能在本地
可见但还没复制。PostgreSQL 18 的实际行为更危险：`SyncRepWaitForLSN` 遇到 `QueryCancelPending` 时发出
`WARNING: canceling wait for synchronous replication due to user request`（DETAIL 说明本地已提交、可能未复制），
然后结束等待，`COMMIT` 仍返回成功。`statement_timeout` 会设置 `QueryCancelPending`；支付入账的
`internal/payments/orders.go` 设了 5 秒 `statement_timeout`，所以这个路径可达。`synchronous_mode_strict` 只让没有同步
备库时提交等待，挡不住这次取消。当前 `internal/platform/pg/pg.go` 的 `DB.begin` 直接用 `pgx.BeginTxFunc`，没有读取
Notice；`TestLostCommit` 也只覆盖提交结果丢失/失败。所以**当前实现不满足本文的无条件保证**，实现前必须补上：

- 在连接配置的 `OnNotice` 回调里按 `*pgconn.PgConn` 捕获提交期间的 Notice；`DB.begin` 在 `COMMIT` 前登记当前事务，
  `COMMIT` 返回后检查并清除。
- 识别 `canceling wait for synchronous replication due to user request` 这个 WARNING，返回一个固定的“提交结果未知”错误，
  不能返回 nil；所有对外写路径把它当未知：答非 2xx、不向上游发请求；准入答 503 并交给 `SettleOrphan`，支付回调交给网关重试和订单幂等（数据库提交未知只要求非 2xx；渠道查单未知的 502 不是这条路径的映射），后台结算和清理按各自的幂等路径处理，不在 `pg.Tx` 里重试。
- 不能只把 `statement_timeout` 设为 0 当作修复：客户端取消、`pg_cancel_backend` 和连接故障仍会进入同一路径；捕获 Notice
  才是完整处理。
- 增加真实同步复制用例：阻断复制，用一个支付入账事务（写订单、付费批次和账本），在 `COMMIT` 等待期间触发服务器
  `statement_timeout`，断言驱动返回“未知”而不是成功、支付回调答非 2xx，应用始终未确认成功。按两种时序验收：(a) WAL 未到备库就隔离旧主并提升：新主没有这笔入账，重试同一支付事件后只入账一次；(b) 复制恢复、WAL 到达备库后再切换：允许入账存在，重试同一支付事件仍只入账一次；两种时序都检查应用没有把未知当成功，订单、余额与账本一致。这个用例通过前，不能宣称无条件不丢。

**RDS 备选的边界**：RDS Multi-AZ 在备库健康时是同步的，但 AWS 的 Multi-AZ 实现说明写明：连接中断时 observer 会让可用的
实例单独继续，恢复后再同步。这个窗口里没有同步副本，确认的提交只在主库上；如果主库随后坏掉，提升备库会丢它。RDS 没有
`synchronous_mode_strict` 这样的开关。选 RDS 就是接受这个窗口；应用层的同步取消处理对 RDS 同样适用，不能只靠 Multi-AZ 这个名字。

## 代码要做什么

### 连接串、启动和只读

- **用 HAProxy，不用 IP**：`AXIOM_DATABASE_URL` 指向本机 HAProxy（或 Patroni 当前主库的地址）；`pgx` 每次新建连接时
  重新解析 DNS。切换后旧连接被丢弃，新连接去新主。不要缓存解析结果。
- **连接只接受可写的服务器**：`pgx` 5.11 支持 `target_session_attrs=read-write`，它在建连后检查
  `transaction_read_only`。`pg.Open` 在连接串没写这个参数时补上，并拒绝运行时的 `any`／`read-only`／`prefer-standby`／
  `primary`；迁移连接同样只连主库。这样应用不会把只读备库当成主库启动。
- **启动自检**：`pg.Check` 增加 `SELECT pg_is_in_recovery()`；为真时以固定错误拒绝启动（“the database is a standby”），
  不把备库当成主库接流量。
- **运行中只读**：新增 `ReadOnly(err)` 识别 SQLSTATE 25006（`read_only_sql_transaction`），和 `pg.Unavailable` 分开：
  连接失败、08 类、57P01–57P03、53300 仍是 outage；25006 表示“连到了只读的库”。只有写路径（准入、追加、结算、清理）
  把它映射成数据库不可用并答 503；`ReadTx` 里出现写仍然是 bug，不能被当成 outage 掩盖。
- **连接池**：连接串设 `pool_health_check_period=10s`、`pool_max_conn_lifetime=30m`、`pool_max_conn_idle_time=5m`、
  `connect_timeout=5s`；数据库的空闲超时要大于连接池的上限，避免拿到已被服务端关闭的连接。连接预算仍由
  `pg.Open` 按 `max_connections` 和 `AXIOM_CORE_NODES` 计算，不因为 HA 改变。
- **`/readyz` 不改成数据库探针**：它继续只看路由快照。数据库故障用 503 表达；不在一次抖动时把所有节点摘掉。

### 提交结果与同步取消

提交期间按「保证的边界」捕获 WARNING；看到该 WARNING 就返回固定的“提交结果未知”错误，所有写路径按未知处理：对外答失败（准入 503，支付回调非 2xx；渠道查单未知的 502 不属于数据库提交未知），准入交给 `SettleOrphan`，支付事件交给网关重试和订单幂等，追加按 `ExtendWait` 预算，结算按 `Settle` 幂等；不重试。真实同步复制用例见演练 10。

### 重试放在哪

- **不在 `pg.Tx` 里做通用重试**：`COMMIT` 失败时不知道事务是否已经提交；重试可能写两次。重试只放在知道幂等性的边界。
- **准入**：失败答 503，不调用上游；`settleOrphan` 用同一个请求编号在 `SettleBackground`（默认 20 分钟）内重试。
  这比 60 秒内的切换窗口长，切换不会把预留留成孤儿。
- **结算**：`Settle` 在 `SettleRetry`（默认 10 秒）内重试，之后交给 `SettleBackground`；同一个请求编号和预留状态保证
  最多收一次。
- **追加**：`Extend` 不沿用结算的期限。它必须在 `ExtendWait`（默认 1 秒，含等账户闸）的总预算内完成；预算内的重试也只能
  花这段时间，到期立即取消上游（`billing_unavailable`），本轮不再追加，最终用量由 `Settle` 记录。不得把 `Extend` 交给
  `SettleRetry` 或 `SettleBackground`：幂等只防重复追加，不能限制等待期间上游继续生成，延长等待会突破预留覆盖的超额额度。
- **孤儿**：`SettleOrphan` 用 `SettleBackground` 的后台期限和同一个请求编号重试；它处理的是已经离开调用路径的预留。
- **River 任务**：让 River 按自己的重试策略重跑；每个任务必须幂等（已有的任务按这个前提写）。
- **只读请求**：可以换一条连接重试一次；写请求不这样做。

### 监控和告警

- 数据库：Patroni `/sync`、`patronictl list`、`pg_stat_replication` 的 `replay_lag`、`pg_is_in_recovery()`、watchdog、
  etcd 健康、pgBackRest `info`、连接数／`max_connections`、长事务、锁等待。
- 应用：`pg.Unavailable`、`ReadOnly` 和“同步取消”WARNING 的计数、准入 503 比例、连接池等待、`SettleOrphan` 重试、
  未结算预留、账本自平结果。
- 告警：同步取消 WARNING 出现即告警（有提交可能未复制）；30 秒没有可写主库、切换超过 60 秒、复制延迟超过 1 秒
  持续 1 分钟、WAL 归档中断、旧主在切换后仍可写。

## 演练

演练分两层：**本地／CI** 用数据库代理和只读库（同一实例设 `default_transaction_read_only`，或起一个流复制备库）验证应用
行为，不依赖云；**staging** 用真实的 3 节点 Patroni 集群验证切换、隔离和备份。staging 实例在演练后删除，费用只有几小时。

### 场景与通过标准

| # | 场景 | 怎么注入 | 通过标准 |
|---|---|---|---|
| 1 | 计划内切换 | `patronictl switchover` | 已确认提交全在；切换期间新调用 503，恢复后第一笔准入成功；RTO 记录在案 |
| 2 | 主库崩溃 | 停掉主库的 Patroni／PostgreSQL | 同 1；旧主不再可写；没有重复扣费 |
| 3 | 网络分区与旧主隔离 | 断开旧主到 etcd 的网络，等 watchdog 复位 | 旧主复位后 `pg_is_in_recovery()` 为真；没有两个主同时接受写；旧主重新加入后时间线一致 |
| 4 | 提交结果未知 | 代理在 `COMMIT` 发出后、响应返回前切断连接 | 网关答 503、没有调用上游；`SettleOrphan` 后预留要么释放要么留墓碑；账本只记一次；`TestLostCommit` 同款断言 |
| 5 | River 重试 | 任务执行到一半切换 | 任务在恢复后重跑且只生效一次；没有重复行 |
| 6 | 预留回收 | 切换时留下在途预留，等 `HolderTTL + ReclaimGrace` | 清理释放预留；`pending=0`、`reserved=0`；账本平衡 |
| 7 | 只读检测 | 把 `AXIOM_DATABASE_URL` 指向备库；运行中把主库降为只读 | 启动被拒绝；运行中写请求 503；没有 500；没有把只读错误当成成功。`/readyz` 仍按路由快照判断，数据库故障由 503 表达 |
| 8 | WAL 与 PITR | 切换后继续写入，恢复到切换之后的时刻 | `pgbackrest check` 通过；`TestPointInTimeRestore` 同款恢复；恢复实例行数一致 |
| 9 | 负载 | `just load 1 '^[1-6]'`；新增 `7_database_failover` 场景 | 首字、排队、结算尾延迟和峰值内存有记录；机械检查通过：没有 pending 预留、账本平衡、扣费等于应得、节点还活着 |
| 10 | 同步等待被取消 | 阻断同步复制；用一个支付入账事务（写订单、付费批次和账本）在 `COMMIT` 等待期间触发服务器 `statement_timeout`；分别在不恢复复制和恢复复制后切换 | PostgreSQL 返回 WARNING + 成功的 `COMMIT`；`pg.Tx` 返回“提交结果未知”、支付回调答非 2xx，应用始终未确认成功。分两种时序：(a) WAL 未到备库即隔离旧主并提升，新主没有这笔入账，重试同一支付事件后只入账一次；(b) 复制恢复、WAL 到达备库后再切换，允许入账存在，重试同一支付事件仍只入账一次；两种时序都检查订单、余额与账本一致 |
| 11 | 追加时切换 | 流式请求接近预留上限，在 `Extend` 期间停数据库 | 上游在 `ExtendWait`（默认 1 秒）内取消；响应 `billing_unavailable`；不超出超额额度；最终由 `Settle` 记实际用量；没有后台追加 |

本地场景 4、5、6、7、11 进 `cmd/axiom` 的负载／端到端测试，能在 CI 或开发机重跑；场景 1、2、3、8、10 需要真实 HA，
放到 staging 手工跑，命令和结果贴进下面的记录。场景 9 的 `7_database_failover` 在实现 PR 里加，沿用现有的负载世界和
机械检查，不另写一套压测。

**如果负责人选 RDS**：同一个演练表删掉 Patroni 专属的 1、3，改用 RDS 的强制切换和事件日志；必须额外做一次“备库失联、
主库继续写、再强制切换”的注入，记录确认的提交有没有丢。这个窗口是 RDS 和 Patroni 的关键差别，不能只跑一次正常的
Multi-AZ 切换就宣布满足要求。

### 记录

每次演练追加一行；RTO 从注入故障到第一笔新准入成功，不含备份。记录 `patronictl list`、`pgbackrest info` 和 RDS 事件 ID
（如果选 RDS）的输出位置。

| 时间（UTC） | 方案／版本 | 场景 | RTO | 已确认提交 | 钱 | 旧主隔离／WAL | 结论 |
|---|---|---|---|---|---|---|---|
| 待第一次演练 | — | — | — | — | — | — | — |

## 费用

| 项 | Patroni + etcd（推荐） | RDS Multi-AZ（备选） |
|---|---|---|
| 计算 | 3 × `m7g.large` 0.1122 美元/小时 ≈ 246 美元/月 | `db.m6g.large` 0.485 美元/小时 ≈ 354 美元/月 |
| 存储 | 3 × 100 GB gp3，0.1056 美元/GB-月 ≈ 32 美元/月 | 100 GB gp3，0.30 美元/GB-月 = 30 美元/月 |
| 合计 | **≈ 277 美元/月** | **≈ 384 美元/月** |
| 运维 | 3 台机器 + etcd + Patroni + watchdog + HAProxy + pgBackRest | 厂商；不能跑 pgBackRest、没有超级用户 |
| 严格不丢 | 复制层是；应用需补同步取消处理 | 只在备库健康时；应用同样需补同步取消处理，备库失联的窗口不保证 |

价格来源是 AWS Price List API 在 2026-10-29 发布的 `ap-east-1` 按需价（EC2 的 `m7g.large` 0.1122 美元/小时、gp3
0.1056 美元/GB-月；RDS 的 `db.m6g.large` Multi-AZ 0.485 美元/小时、gp3 Multi-AZ 0.30 美元/GB-月）。按 730 小时和 100 GB
估算，不含税、跨区流量、额外快照和公网出口。Patroni 的 277 美元是 etcd 与 PostgreSQL 同机的下限；如果 etcd 单独三台
`t4g.small`，再加约 74 美元/月。实际账单随汇率、预留实例和用量变化；采购前用当时的价格复核。

## 给负责人

**要定什么**：为了“切换不丢已确认提交”，要不要接受三台数据库机器和 etcd／Patroni／watchdog／HAProxy 的运维；还是选
RDS 少运维，但接受它在备库失联时继续写、之后主库再坏可能丢已确认提交的窗口。

**推荐**：Patroni + etcd 的严格同步，3 × `m7g.large`，100 GB gp3，PostgreSQL 18.6，约 **277 美元/月**（etcd 同机）。
它比 RDS 便宜约 **107 美元/月**，代价是运维最重；前提是实现补上同步取消 WARNING 的未知处理，补上后它没有同步备库时
写提交会阻塞，严格满足本文的要求。

**备选**：RDS Multi-AZ DB instance，`db.m6g.large`，100 GB gp3，约 **384 美元/月**。它少运维，但 AWS 的 Multi-AZ 在备库
失联时会让主库单独继续；这个窗口里确认的提交只在主库上，主库随后坏掉就会丢。如果负责人接受这个窗口，RDS 是更省事的
选择；如果不接受，选 Patroni。这个差别必须写进批准，不能只看“Multi-AZ”这个名字。

**不批准的后果**：继续单主，主库故障时人工恢复，停机更久；没有自动切换。单主仍然能在本机 fsync 后确认提交，但机器或
磁盘整体坏掉时只能靠备份和 PITR 恢复，恢复点之后已确认的提交会丢；这不是“切换不丢提交”，而是灾难恢复。

**改回来的代价**：Patroni 和 RDS 都是 PostgreSQL，数据可以用 `pg_dump`／逻辑恢复迁移；连接串从 HAProxy 换成 RDS 端点，
或反过来。没有私有数据格式；停机时间取决于库的大小。

**还没验证**：`pg.Tx` 对同步取消 WARNING 的捕获和真实同步复制用例；3 节点 Patroni + etcd + watchdog 的 staging spike
（PostgreSQL 18.6、Patroni 4.1.5、HAProxy、pgBackRest、`pg.Check`、连接预算、TLS）；RDS 的备库失联行为在当前版本是否仍
如 AWS 的实现说明所述。批准前按[发布前](release.md)的方式记录结果；不通过就不采购。

**批准状态**：截至本设计，负责人尚未批准；PR 背景写「没有负责人批准」。负责人的同意要按 [CONTRIBUTING 的「谁拍板」](../../CONTRIBUTING.md#谁拍板)
单独记录日期和出处，本文和 PR 都不代替它。

## 决策与代价

- **Patroni + etcd 严格同步，而不是 RDS Multi-AZ**：RDS 的运维最少，但它的 Multi-AZ 在备库失联时会让主库单独继续，
  这个窗口里确认的提交可以丢；它没有 `synchronous_mode_strict` 这样的开关。Patroni 用 `/sync` quorum 和 watchdog
  在复制层给出严格保证；端到端还要应用捕获同步取消 WARNING（见「保证的边界」），代价是三台机器和
  etcd／Patroni／HAProxy 的运维。若负责人接受 RDS 的窗口，RDS 可以换成主方案。
- **Patroni + etcd，而不是 CloudNativePG**：CloudNativePG 的 `dataDurability: required` 在复制层也满足严格不丢，但要先有
  Kubernetes；Axiom 目前是 Docker Compose，为一个数据库引入控制面、operator 和存储类不划算。
- **不用 pg_auto_failover**：它默认在备库不健康时关闭同步复制继续写，之后主库再坏会丢已确认的提交；要避免只能在备库
  故障时阻塞写提交，但这不是它的默认行为，收益也不如 Patroni。
- **`synchronous_mode: quorum`，不是单个 `FIRST 1`**：三节点下 `ANY 1` 能容忍一个备库故障，写延迟由较快的备库决定；
  `FIRST 1` 在指定的同步备库故障时会阻塞写提交，可用性更差。
- **`synchronous_commit=on`，不是 `remote_apply`**：`on` 等本地 fsync 和同步备库 flush；只有在应用捕获同步取消 WARNING、
  不把这种 `COMMIT` 当成功后，才足够保证已确认提交不丢。`remote_apply` 还要等备库重放，延迟更高，收益只是读到的数据更新。
- **watchdog 是必需的**：只靠 Patroni 停止 PostgreSQL 不够——Patroni 崩溃、机器被挂起或关机太慢时旧主可能继续写。
  `watchdog: {mode: required, safety_margin: -1}` 让旧主在 leader lock 过期前整机复位；没有 watchdog 的节点不允许当主。
- **重试只放在幂等边界**：`pg.Tx` 不重试，因为 `COMMIT` 结果未知；准入答 503 并交给 `SettleOrphan`，结算和 River 任务
  按请求编号幂等重试。追加只在 `ExtendWait` 的预算内重试，到期取消上游，不进入后台长重试。通用重试会把一次提交写成两次。
- **追加不参与后台长重试**：`ExtendWait`（默认 1 秒）是预留覆盖的上游继续生成时间；把它延长到 `SettleRetry` 或
  `SettleBackground` 会让上游在预留之外继续收费。幂等只防重复追加，不能限制这段时间的成本；所以追加到期取消上游，
  最终用量交给 `Settle`。
- **只读检测分三层**：建连时 `target_session_attrs=read-write`，启动时 `pg.Check` 检查 `pg_is_in_recovery()`，运行时
  `ReadOnly` 识别 25006；前两层防止连到备库，最后一层处理运行中被降级。三者都不把 `ReadTx` 里的写当成 outage。
- **`/readyz` 不接数据库**：它继续只看路由快照；数据库故障用 503 表达。把数据库探针接进就绪会在一次抖动时摘掉所有节点，
  而且不增加钱的安全性。

## 待定

- 负责人对“Patroni 严格同步”或“RDS 接受备库失联窗口”的选择，以及每月约 277／384 美元的批准。
- etcd 是跟 PostgreSQL 同机还是单独三台、watchdog 用 softdog 还是硬件、HAProxy 放在核心机还是单独机器；同机是 277 美元
  的下限，拆开要加机器。
- 3 节点 Patroni 的 staging spike：PostgreSQL 18.6、Patroni 4.1.5、etcd 3、HAProxy、watchdog、pgBackRest、`pg.Check`、
  连接预算、TLS；结果决定方案能否落地。
- `pg.Tx` 的同步取消 Notice 捕获、真实同步复制用例，以及 `Extend` 的 1 秒预算在切换演练中的实测；这些通过前不能宣称
  端到端不丢。
- 切换 RTO 目标：本文按 60 秒告警，Patroni 计划内切换通常更快；第一次演练后把实测值写进本文。
- RDS 的备库失联行为在当前版本是否仍如 AWS 的实现说明所述；如果负责人考虑 RDS，先在 staging 复现这个窗口。
- `7_database_failover` 写进负载测试前，先用本文的场景手工跑一遍；第一次真实演练的日期和 staging 费用。
- 跨区域灾难恢复不在本文范围：机器整体丢失仍走[备份与恢复](backup-restore.md)的逻辑 set 和 PITR。

## 参考

- AWS《Multi-AZ DB instance deployments for Amazon RDS》：同步备库、另一可用区、提交延迟，2026-11-03 读取。
  <https://docs.aws.amazon.com/AmazonRDS/latest/UserGuide/Concepts.MultiAZSingleStandby.html>
- AWS《Amazon RDS Under the Hood: Multi-AZ》：连接中断时 observer 让可用实例单独继续，恢复后再同步，2026-11-03 读取。
  <https://aws.amazon.com/cn/blogs/database/amazon-rds-under-the-hood-multi-az/>
- AWS《Failing over a Multi-AZ DB instance for Amazon RDS》：切换通常 60–120 秒，2026-11-03 读取。
  <https://docs.aws.amazon.com/AmazonRDS/latest/UserGuide/Concepts.MultiAZ.Failover.html>
- AWS《Multi-AZ DB cluster deployments for Amazon RDS》：半同步、两个可读副本，2026-11-03 读取。
  <https://docs.aws.amazon.com/AmazonRDS/latest/UserGuide/multi-az-db-clusters-concepts.html>
- AWS Price List API，`AmazonRDS`／`AmazonEC2` 的 `ap-east-1` 文件，发布 2026-10-29；RDS 价格页
  <https://aws.amazon.com/rds/postgresql/pricing/>。
- Patroni 4.1.5《Replication modes》：同步模式、quorum、strict 和“等待同步确认时被取消”的边界，2026-11-03 读取。
  <https://patroni.readthedocs.io/en/latest/replication_modes.html>
- Patroni 4.1.5《Watchdog support》：watchdog 在 leader lock 过期前复位旧主，2026-11-03 读取。
  <https://patroni.readthedocs.io/en/latest/watchdog.html>
- Patroni 4.1.5《DCS Failsafe Mode》：DCS 不可用时只在所有成员可达时保主，2026-11-03 读取。
  <https://patroni.readthedocs.io/en/latest/dcs_failsafe_mode.html>
- pgBackRest 2.58.0《Archive Push》《Archive Get》《Check》：WAL 归档、恢复和检查，2026-11-03 读取。
  <https://pgbackrest.org/command.html>
- CloudNativePG《Replication》《Automated failover》《Fencing》：同步副本、Lease 选主与隔离检查、fencing，2026-11-03 读取。
  <https://cloudnative-pg.io/docs/current/replication/>、<https://cloudnative-pg.io/docs/current/failover/>、
  <https://cloudnative-pg.io/docs/current/fencing/>
- pg_auto_failover 2.2《Architecture Basics》《Failover and Fault Tolerance》：monitor、同步复制和备库不健康时的行为，
  2026-11-03 读取。<https://pg-auto-failover.readthedocs.io/en/latest/architecture.html>、
  <https://pg-auto-failover.readthedocs.io/en/latest/fault-tolerance.html>
- PostgreSQL 18 `src/backend/replication/syncrep.c`：`QueryCancelPending` 时发出
  `canceling wait for synchronous replication due to user request`，随后 `COMMIT` 仍返回成功，2026-11-03 读取。
  <https://github.com/postgres/postgres/blob/REL_18_STABLE/src/backend/replication/syncrep.c>
- pgx 5.11 `target_session_attrs` 的 read-write 校验：
  <https://pkg.go.dev/github.com/jackc/pgx/v5/pgconn#ValidateConnectTargetSessionAttrsReadWrite>
