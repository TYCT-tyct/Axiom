# platform 的推导与细节（存档）

[platform 的 README](../../internal/platform/README.md) 只写公开接口和不变量；这里留着值得保留的推导、细则和被否决的方案。改写前的全文见提交 `4cc2c9e6` 里的 `internal/platform/README.md`。这里与代码或 README 冲突时，以代码和 README 为准。

## 测试设施

- **`containertest`**：`WithHostPolicy` 在已有宿主配置之后应用：`AXIOM_TEST_CGROUP_PARENT` 定父 cgroup，`AXIOM_TEST_JOB` 非空时加 `axiom.ci.job` 标签；把暴露的端口发布在 127.0.0.1 上 20000–29999 中此刻没人监听的号（低于内核临时端口范围），所以要最后应用，容器要写明暴露哪些端口。`Start(ctx, 启动函数)` 在 Docker 绑不上挑的号时删掉容器换号重试，至多 8 次，别的失败直接返回；固定的号让 kvtest 重启的容器留在原地址。作业标识、集群连接串、kvtest 地址和密码文件经 `Untracked` 用 `os.Environ` 读，不进 Go 测试缓存键。`Cleanup(t, 容器, 名称)` 删除失败时，带 `axiom.ci.job` 标签或 Ryuk 回收标签的只记日志，两个都没有的让测试失败；不延长期限、不重试。`StartSeaweedFS(t, 桶…)` 起真实 S3（唯一一对测试凭证 `SeaweedFSAccessKey`、`SeaweedFSSecretKey`），`Endpoint` 给本进程，`InternalEndpoint` 给同一测试里的另一个容器。
- **`pgtest`**：`just test` 经 `withcluster` 起一个 PostgreSQL 容器和一个 Valkey，连接串放进 `PGTEST_CLUSTER_URL`，一次运行的所有包共用；没有这个变量时 `pgtest` 自己起容器。集群建好角色、以非超级用户的迁移管理员执行迁移做成模板，每个测试复制一份独立的库（名字带测试二进制的随机标识），结束时 `DROP DATABASE … WITH (FORCE)`。`pgtest.New(t, 模块…)` 在集群的 advisory lock 下按模块组建模板，先用短名字建、做完再改名，半途退出不会留下看似完整的模板；同一次运行里每次调用给的模块必须相同。`pgtest.Clone(t, source)` 从种好数据的库复制。容器关 fsync，`max_connections` 1000，WAL 封顶 `min_wal_size=32MB`、`max_wal_size=128MB`，`/dev/shm` 512 MiB（默认 64 MiB 在 `-p=4` 时会被 32 MiB 的并行查询段耗尽）；主机写 `127.0.0.1`，因为 `localhost` 先试 `::1`，Docker Desktop 的 IPv6 转发在负载下会卡住。改集群范围状态的包（`internal/platform/pg`）在 `TestMain` 里清掉变量、自己起容器；别的包查 `pg_stat_activity` 要加 `datname = current_database()`。Windows 上没有 `DOCKER_HOST` 时先设成 Docker Desktop 的命名管道，否则管道忙时 testcontainers 会误判为无根 Docker。
- **Ryuk**：Ryuk 启用时 `withcluster` 把 `TESTCONTAINERS_SESSION_ID` 传给子命令，集群连接一直保持到子命令退出，避开 testcontainers-go v0.44.0 复用正在退出的 Ryuk 的问题（[testcontainers-go#3867](https://github.com/testcontainers/testcontainers-go/issues/3867)，原因见 `pgtest` 的 `keepReaperAlive`）。CI 设了父 cgroup 时禁用 Ryuk，由作业首尾的标签清理兜底。只读或故障模拟的 Valkey 用例用共享实例或 `kvtest.Down`，只有改服务端状态的用例起自己的容器（[kv 的「测试」](../../internal/platform/kv/README.md#测试)）。
- **`alloctest`**：都在只跑被测函数的子进程里测。`Measure` 关 GC、单核，报告单次调用的分配次数和字节（5 次取最小）。`MeasureRetained` 保留返回的对象，显式回收后读 `HeapAlloc` 的增长。`MeasurePeak` 在运行期间反复回收并读 `/gc/heap/live:bytes`，报告最高读数减开始前的读数，再与结果留下的堆取较大者；子进程用 `GODEBUG=gcstoptheworld=2` 让读数是那一刻活着的堆，用 `asyncpreemptoff=1` 免得异步抢占的保守扫描把丢弃的内存算成活的（`TestPeakReadingSkipsDroppedMemory`），代价是被测函数里不能自旋等别的 goroutine；读数可能少几 KB 的运行时噪声。
- **`backup`**：只有容器测试，守 `deploy/postgres` 镜像和[时间点恢复](../ops/backup-restore.md#时间点恢复)：归档 WAL、全量备份、写两批行，恢复到两批之间，断言前 100 行都在、后 50 行不在，再以空 WAL 桶启动断言 `archive_mode=off`。
- **`telemetrytest`**：`Buffer` 收集别的 goroutine 写的日志；`Metric` 从 `/metrics` 文本读一条序列，没有时返回整段文本。
- **夹具**：`pgtest.WorkJob` 为跨模块 worker 测试提供 River 的事务执行器和受控时钟；控制台流水导出的满额夹具直接调用 CSV 导出器，不把共享机器读 10 000 行的速度当作正确性。

## 指标

现有指标（Prometheus 名；每个都有测试经 `telemetry.New` 的处理器读到名字）：

| 指标 | 类型 | 标签与含义 |
|---|---|---|
| `axiom_http_server_requests_total`、`axiom_http_server_duration_seconds` | 计数、直方图 | `route`、`status_class`（未发出状态是 `none`）、`outcome`（`completed`、`aborted`） |
| `axiom_accounts_key_cache_misses_total` | 计数 | key 查找没命中缓存去问数据库的次数 |
| `axiom_observability_records_written_total`、`…_records_lost_total`、`…_attempts_written_total`、`…_attempts_lost_total`、`…_write_retries_total`；`axiom_observability_records_pending` | 计数；仪表 | 调用记录的写入、丢失、重试；spill 中未写入的数量 |
| `axiom_gateway_rate_limited_total` | 计数 | `reason`：拒绝的那道限制（[gateway](../../internal/gateway/README.md#并发背压与共享状态)） |
| `axiom_gateway_outage_refusals_total` | 计数 | `dependency`（`database`、`accounts`、`catalog`、`routing`）；日志只在故障开始和结束各记一条 |
| `axiom_routing_preset_refusals_total` | 计数 | `reason`（`preset_unavailable`、`preset_filter`） |
| `axiom_pg_queue_discarded_jobs_total` | 计数 | 任务种类 |
| `axiom_pg_queue_oldest_job_wait_seconds` | 仪表 | `queue`：最老的可用任务等了多久；采集 2 秒期限，超时报错不报 0 |
| `axiom_billing_platform_borne_total`、`…_platform_borne_amount_total`、`axiom_billing_reconcile_problems_total` | 计数 | 平台承担的结算次数与金额、账本自平发现的问题 |
| `axiom_billing_expire_deferred_total`、`…_expire_deferred_amount_total` | 计数 | 到期作废没能全部作废的批次与剩余金额（[额度批次](../domains/billing.md#额度批次)） |
| `axiom_catalog_sync_last_success_age_seconds`、`axiom_catalog_sync_consecutive_failures` | 仪表 | 距最后成功的秒数（读数据库时钟，读失败报错不报 0）、连续失败轮次 |
| `axiom_catalog_sync_overdue_total` | 计数 | 失败超过 48 小时的兜底计数，日志 `catalog.sync.overdue` |

稳态零分配的做法：标签集合与 `metric.WithAttributes` 选项按组合缓存（`httpserver` 的 `seriesCache`，读取一次原子加载），选项切片建一次以 `Add(ctx, 1, opts...)` 传入，因为逐个传的可变参数会在接口调用处分配。

## 遥测的取舍

- 选 OpenTelemetry API 与 SDK、Prometheus 导出器、抓取式：模块只依赖 `metric.Meter`，换导出方式不改模块。不直接用 `prometheus/client_golang` 记指标：模块要认两套接口，性能也没有优势；它仍是导出器的底层。
- 暂不接 OTLP 推送：要先有收集器，`otlptracehttp` 会带进 grpc 一批依赖。有收集器的部署再加：一个 `SpanProcessor`，端点用标准 `OTEL_EXPORTER_OTLP_*`，采样比例变量（0 到 1，跟随父级），配置错了在 `serve` 解析时报错；出错请求的总采靠收集器的尾部采样。
- 不用厂商 SDK 或托管服务：端点在境外，国内节点不一定连得上，元数据会离开部署。面向用户的链路导出（OpenRouter 的 Broadcast）属于[记录与告警](../domains/observability.md)。

## 出站地址策略

- 地址表在 `egress/address.go`，引用 IANA IPv4/IPv6 Special-Purpose Address Registry；特殊用途聚合段整体拒绝（含其中的公网例外）。解析结果里的映射 IPv6 先还原成 IPv4 再校验，因为 Go 的解析器把 `/etc/hosts` 的 IPv4 条目返回成映射形式。IANA 分配变动时复核前缀表。
- 云元数据地址 `169.254.169.254`、`fd00:ec2::254`、`100.100.100.200`、`192.0.0.192` 由通用规则覆盖，Azure 的 `168.63.129.16` 在公网段，单独列出；选定云厂商时复核。
- NAT64：RFC 6052 §3.4 允许网络自选 `2000::/3` 内的转换前缀，这种地址看起来是普通公网 IPv6，包认不出嵌入的 IPv4。需要这种网络的部署要由可信配置给出前缀，整段拒绝或还原后校验，并补 URL 字面量和 AAAA 两条路径的测试。
- 拒绝 `101`：之后响应体是裸连接，请求期限不再关闭它，静默上游能让读取一直阻塞。响应头 1 MiB：上游地址可由站长设置，默认 10 MB 太宽；HTTP/2 按每个字段另加 32 字节。不设统一的整体超时：非流式的模型调用要等生成完才有响应头。

## 密钥与信封

- 闭包的理由：fmt 对不适用的动词（如 `%w`）根本不调用 `Format`，未导出字段由反射展开，只加 `Format`/`LogValue` 会把 map 打出来；材料在闭包里只打印成函数地址。`Secret` 因此不可比较（不能做 map 键，`reflect.DeepEqual` 为 false），比较用 `Reveal()`，go-cmp 用 `cmp.Comparer`。它不擦除进程内存。
- 位置编码：域标识 `axiom/secret/location/v1`、零字节，再依次写 `Table`、`RowID`、`Purpose` 的 4 字节大端长度和原始字节；包裹层认证 `axiom/secret/dek/v1`、零字节、4 字节大端版本和位置编码，所以把旧密钥换个版本号重新登记的记录仍打不开。`Sealed` 含密文（带 16 字节标签）、12 字节 nonce、`WrappedDEK`（12 + 32 + 16 字节）和 `KeyVersion`。
- 文件与数据库不能同事务：增加先持久化文件再登记，退役先提交数据库再删文件，崩溃只会多留一把密钥。启动、`check` 和在线循环在确认没有信封引用后删掉本机文件里已退役的版本；仍有引用时保留并报错。退役先取排他锁、写 `platform.key_retirements` 标记再提交墓碑；每个信封表 `BEFORE INSERT OR UPDATE OF key_version` 的触发器取同一把锁的共享模式。等待条件 `ErrNotDistributed`、`ErrQuiet`、`ErrReferenced` 是稍后再试。内容数据密钥用 `wrapped_key` 列，按 `EnvelopeStore.Wrapped` 识别。
- 心跳列：`platform.nodes` 每行带 `key_versions`（与数据库摘要核对过的版本）、`key_active`、`key_active_since`；超过 30 秒未更新不算在线。另有 `supports_billing_preference`/`billing_preference_session` 和 `supports_model_prices_public`/`model_prices_public_session`，只有声明会话与存活行一致才算支持，汇总写进 `platform.billing_preference_readiness`、`platform.model_prices_public_readiness`：还有不支持的节点时准入按默认扣费顺序、匿名模型投影继续公开价格。
- 私密文件不认符号链接：Docker 和 Kubernetes 的 secret 挂载点默认是链接，要把真实文件放进卷；Windows 不检查权限位，由部署用 ACL 限制。PowerShell 5 的 `Out-File -Encoding utf8` 会写 BOM，报错落在第 1 行的版本号上。

## 连接池的推导

- 默认 PostgreSQL 的 100 条留下 93：单节点 64，两节点各 43（R=16 时普通 27、完成 16），四节点各 20，五节点装不下 R=16、拒绝启动。算出的数不够时改节点数、R 或 `max_connections`，不改 `pool_max_conns` 绕过。
- 若一次完成事务占连接 5–10ms，R=16 约每秒 1600–3200 次；不能据单节点 64 条推断能承受每秒 5000 次结算，是否足够由[性能的压测](../performance.md#怎么验证)验证。单个账户受账户闸约束，正常时约占 2 条（[gateway 的「账户闸」](../../internal/gateway/README.md#账户闸)）。
- 滚动发布时同名进程拿不到名字锁，先停旧进程；换名字扩节点先调大 `max_connections`。非节点命令沿用 URL 的 `pool_max_conns`，但清零最小连接数。
- 为什么两个物理池：River 直接用 pgxpool、不经 `Tx`，只在 gateway 发许可拦不住控制台和 recorder；代价是两池不能互借。按存活节点动态缩池被否：旧节点不会让出连接。PgBouncer 留待测量。
- 心跳用池外连接：一次心跳的期限是 TTL 的三分之一，等池里的连接就可能错过，`Alive` 变假会让整个节点答 503。

## 数据库细则

- **连接守卫**：`pg.Tx` 的 `Query` 逐个转发 `pgx.Rows` 的方法，唯独 `Conn` 返回空，pgx 给 `Rows` 加方法时编译失败；作为唯一扫描目标的 `RowScanner` 和参数开头的 `QueryRewriter` 也拿不到连接。depguard 的豁免是锚定仓库根的路径规则（`^internal/platform/`），不用 `files` 通配，后者会连 `internal/<模块>/internal/platform` 也放过；`jsonx` 同理。
- **`just scope`**：规则在 `scripts/check_scope.py`，用例在 `scripts/test_scope.py`；不分大小写，测试和迁移也不例外；只认参数名本身，点号两侧的空格、引号、转义都藏不住；先认字符串再认注释（转义字符串、美元引号里的注释标记是文字），`SET` 与 `ROLE` 之间的注释和换行藏不住它。运行时拼出的 SQL 和 `pg` 内部靠评审。写成 Python 而不是 Go 工具，是因为 Go 测试里的 `SET ROLE` 字符串会触发规则本身。
- **通行密钥证明**：`pg.Tx.MarkPasskeyProved` 设 `axiom.passkey_proved`，TOTP 触发器核对当前用户；提供具名方法而不是通用设置入口，`just scope` 才能只放行 `pg`。
- **`Batch`**：READ COMMITTED 中每条语句取自己的快照，余额锁和锁后的计数读取必须是独立语句；一个事务里有多个请求时整批失败、按请求 ID 重试。
- **迁移期限**：到期用独立的短连接（自己最多 5 秒）按 `application_name` 执行 `pg_terminate_backend`，不依赖 pgx 后台的取消请求，也不是逐语句的超时；按应用名而不是进程号，因为进程号可能被复用。goose 释放会话锁用取消不掉的 context，终止后再给 15 秒宽限。连接期限必须短于执行期限。连接错误只报变量和失败类别，不带用户、库、主机。
- **迁移摘要**：单独一张表，因为 goose 在模块第一次有迁移时才建版本表；核对、登记和执行在同一会话同一把锁下，会话丢失就失败。记录出现之前就迁移的库第一次运行以当时的文件为基线。部署检查 `scripts/ci/old-binary-check.sh` 真的启动上一版本；列和表只在停用它的版本发布之后才删。
- **River**：迁移跟在建 schema 的连接上做（少一个连接），每次执行重授权；它不是模块，表不进行级安全审计。v0.49 起事务辅助函数不用保存点，出错须回滚整个事务，`pg` 符合。默认 1 小时的救援太长、1 分钟的任务期限短于一轮同步，所以用 15 分钟和 10 分钟；新旧并存时领导节点的救援仍会丢弃它不认识的运行满 15 分钟的任务，所以并存只应持续一次发布。
- **任务登记**：River 的生产者不加锁地读 worker 表，`Start` 后再登记只会表现为数据竞争或永远不跑的周期任务，所以直接 panic。SMTP 未配置的节点不登记邮件和找回邮件的 worker，这些任务留给已配置的节点；价格同步订阅关停信号、立即取消下载并给结果最多 5 秒记录；ECB 任务只在 `AXIOM_EXCHANGE_SOURCE=ecb` 时登记。

## 接口约定的推导

- **守卫**：放在生成的路由之前，因为生成的包装先解析查询参数、后跑中间件，放进中间件就挡不住无会话却先得到 400。契约经 oapi-codegen 的 `embedded-spec` 编进程序，`go:embed` 原文件不行，因为 `api/` 下不能放 Go 文件。代价是主模块多了 kin-openapi 和 5 个依赖。请求体先过 `jsonx`，因为生成代码和校验器的解析器不执行这些规则；生成的处理器用 `jsonx.Decode`，否则 encoding/json 不分大小写，`MODELS` 会绕过 schema 的上限。不给请求体写 `additionalProperties: false`：新页面多发一个字段就会被拒。
- **期限**：最慢的合法控制台请求是 argon2id 排队加计算，几秒；期限随 context 到事务，pgx 到期取消语句并丢弃连接。不用 `http.Server` 的写超时：它切断响应而事务照跑。
- **错误体**：OpenRouter 的 `error.code` 是与状态相同的数字，细节放在 `metadata`；外壳相同，客户端不用改，但数字分不清同一状态下的不同原因，所以用封闭枚举。不选 RFC 9457。
- **分文件**：跨文件引用在 oapi-codegen 里要 `--import-mapping` 并为共享部分另生成一个包；所以各契约抄写共用部分，由检查保证相同。
- **没有固定形状的 JSON**：价格依据 `Evidence` 保持 Go 类型 `map[string]interface{}`，因为幂等指纹按解码后的值算，换成 `jsonx.Raw` 会让超过 2⁵³ 的整数不再舍入，重试落到另一版本的节点就成了 409（`TestEvidenceFingerprintsAsReleased`）。请求里的模式不收紧服务端的判断，否则商户设置、预设的 422 会变成守卫的 400。按 `kind` 用 `oneOf` 被否：空对象同时匹配几个分支。
- **桩**：契约一加操作，`StrictServerInterface` 就多一个方法；`tools/pendingapi` 生成嵌入的占位类型让程序照常编译，处理器声明同名方法即接管，全部接管后连同 `//go:generate` 删掉。
- **gitleaks**：生成代码里内嵌的契约是 gzip 后的 base64，会被 `generic-api-key` 误报，所以 `.gitleaks.toml` 对三个接口包的 `.gen.go` 放行。`adminapi` 的类型和内嵌契约分别生成到 `types.gen.go`、`spec.gen.go`，因为合在一个文件里超过 `scripts/check_artifacts.py` 的 1 MB 上限。
- **控制台版本**：前端靠 `GET /api/console/v1/version` 发现新版本，不轮询入口页。

## 审计与幂等的细则

- **租户登记**：策略按站点或账户之一放行，`site_id` 和 `account_id` 不属于同一租户时，账户范围能记下「别的站点、本账户」，那个站点就读得到；所以用平台自有的 `platform.tenants` 加外键（站点登记为 (站点, 站点)，账户为 (站点, 账户)，条目按生成列 `tenant_id = coalesce(account_id, site_id)` 引用），平台在依赖最底层，不能对 accounts 的表建外键。
- **索引**：迁移 00017 在事务外并发建十个检索索引，免得 SHARE 锁阻断业务事务里的审计追加；代价是每次插入维护十一个索引项，持续写入吞吐未压测。并发构建失败留下的无效索引要先 CONCURRENTLY 删掉再重跑。
- **审计的其他**：模块自己的改动历史（如 catalog 的 `change`）与审计互不替代；8 KiB 上限挡不住短对话，不记敏感内容靠调用方；保留期由负责人定，与钱有关的至少与账本同寿命；数据库权限挡不住 `axiom_owner` 和 DBA，防篡改靠运营的哈希链和异地副本。修改 key 的审计只记可用模型和 IP 白名单「变了」，因为列表会超过 8 KiB、地址是个人信息。
- **幂等**：键绑定内容比契约字面严，因为内容不同时重放第一次的结果会让调用方以为新金额已生效。目录写操作在占键的事务里同时写 `change.op_key`，`ErrOpKeyReused` 只留给命令行的文件键。各模块各加 `op_key` 被否：重复实现，也查不出跨操作复用。

## 被否决的方案

| 决策 | 不选 |
|---|---|
| 按 IP 拨号 | 只在保存 URL 时校验或让 Transport 再解析：有 DNS 重绑定窗口 |
| 私有测试能力值 | 导出 AllowPrivate、任意 Resolver/Dialer：生产能绕过 SSRF 边界 |
| 第一个响应即返回 | 默认 `http.Client`：跟随重定向，拒绝回调前仍会解析 Location |
| 配置只来自环境变量 | 配置文件加环境变量覆盖：要定优先级 |
| 节点角色一个二进制 | 每个角色一个二进制：多次构建；只警告不拒绝：错误配置能上线 |
| 节点证书与主密钥分文件同目录 | 编进密钥环：证书到期牵动主密钥 |
| 没有角色带 BYPASSRLS | 迁移角色带 BYPASSRLS：多一个能绕过的身份 |
| 范围读取放在策略级 InitPlan | 逐行函数阻止内联；`IMMUTABLE` 会让复用的计划保留旧范围（[pg](../../internal/platform/pg/README.md#决策与理由)） |
| 登录角色和密码由部署建 | `axiom migrate` 设密码：密码要经过程序；DDL 日志会记下明文，所以初始化时不开 |
| 共用一个测试集群 | testcontainers 复用：没有明确的结束时刻；机器常驻集群：并发运行互相影响 |
| `pg.Module` 值 | 包初始化时登记全局表：靠导入副作用 |
| 迁移前核对摘要 | 只在 CI 比对：部署能绕过；照抄 sub2api 的白名单：放宽检查；先迁移再补记：会把改过的文件当成真历史 |
| 按模块名单找表 | 按 schema 所有者：手工建的 schema 会漏查；按 `goose_` 前缀排除：同前缀的表会漏查 |
| 内嵌根证书 | 往镜像复制 `ca-certificates.crt`：构建机要有它 |
| 迁移管理员不是超级用户 | 测试用超级用户：权限问题到部署才发现 |
| sqlc 方法带 `tx` 参数、一个根配置、`.gen.go` | `New(tx)`：对象能逃出事务；每包 `//go:generate`：重复；默认 `*.sql.go`：不被检查 |
| `just generated` 先删后生成 | 按路径列出生成文件：与 `sqlc.yaml` 重复；只查文件头：拦不住名字冒用 |
| 审计在平台、同步写 | 由 `operations` 提供写接口：违反依赖方向；提交后发事件：中间崩溃就丢条目 |
| 守卫按契约强制 | 每个处理器手写校验：漂移 |
| 金额整数文本 | JSON 数字：浏览器无声舍入 |
| 键集游标 | OFFSET：深页读得多，新记录到达时翻页重漏 |
| `PUT` 整体替换 | JSON Merge Patch：要引入可空类型 |
| 退出时先答 503、共用一个期限 | 收到信号即关监听：边缘只看到连接错误；各自计时：最坏情况是两段之和 |
| 共享卷保留旧的带哈希文件 | 入口按会话粘滞：所有入口都要做，节点故障时仍断 |
| 按文件名定缓存 | 读 `manifest.json`：多一个要同步的文件 |
| 管理监听默认 `127.0.0.1:8082` | 默认 `:8082`：忘了限来源就暴露 |
| `typeid` 用 `Prefix` 值，不做泛型 ID 框架 | `Encode(前缀, id)`：每次检查；`ID[T]`：先猜了形状 |
| `clientip` 一份 | 在 accounts 读 `X-Forwarded-For`：模块要知道部署网络；各写掩码：改粒度时不一致 |
