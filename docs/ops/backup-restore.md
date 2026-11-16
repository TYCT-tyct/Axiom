# 备份与恢复

逻辑 set 用于异机重建，WAL 与基础备份用于时间点恢复（PITR）；二者不能互相替代。逻辑作业在 `deploy/ops` 运维镜像、Compose 的 ops profile 运行，不随 `just up` 启动；WAL 由 `deploy/postgres` 数据库镜像按桶配置开启。运维镜像与数据库同用 `postgres:18.6-alpine`，避免 pg_dump 版本过低；age 和备份凭证不进入公网程序镜像。

## 备份

一次 set 包含四个对象：

| 文件 | 内容 | 收件人 |
|---|---|---|
| `db.dump.age` | custom 格式全库转储，保留属主与授权 | `AXIOM_BACKUP_DB_RECIPIENT` |
| `deploy.env.age` | 本次部署环境文件，包括库口令与桶凭证 | 同上 |
| `keys.tar.age` | master、hash、identity 完整密钥环、权限与属主 | `AXIOM_BACKUP_SECRETS_RECIPIENT` |
| `manifest.txt` | 时间、转储/服务器版本、镜像引用（生产 digest）、大小、两个收件人 | 无加密 |

set 名为 UTC `YYYYMMDDHH24MISS` 加三个随机字节，按名即按时间排序。manifest 最后上传，是完整性的唯一标志；恢复只认有它的 set。节点证书不备份，由部署 CA 重签，每节点仅持自身私钥，入口不拿核心证书。

age 的两个收件人必须不同，脚本拒绝相同；备份侧仅持公钥，私钥离线另存、不留宿主或桶。转储以 postgres 超级用户执行，避免 FORCE RLS 静默漏行；密钥版本清单用同镜像 pg_restore 从转储快照的 `platform.key_versions` 读取，不能用转储前后实时查询替代。

### 停写备份

收费先入账，调用记录先 fsync 到 call_log spill 再异步入库，因此必须停写并排空后转储。

1. `scripts/ops/backup.sh` 先锁环境文件旁的 `.backup-lock`，忙则退出 75；确认 `--env-file` 可读，把其绝对路径经 `AXIOM_BACKUP_ENV_FILE` 交给作业，失败在停程序前退出 1。
2. 仅停原本运行的程序，SIGTERM 在 `stop_grace_period` 170 秒内排空监听和 spill；原本没运行的不启动。
3. 作业对 call-log.lock 取共享锁，节点持排他锁，互斥失败退出 1。无锁文件时以目录属主、0600 原子创建，堵住首次启动插入检查与转储之间的空档；除该锁外不改日志。`axiom call-log check` 检查点后的记录及停放待定记录必须为 0，否则退出 1、不转储不上传。
4. 转储完成释放锁，输出 `backup: dumped the database; the program may start`；包装脚本据此重启程序，加密、密钥打包、上传在运行期间继续。失败也重启本次停下的程序。

停机只含排空、转储、重启。排不完则让节点起来先写完再备份；多核心须全部停下、逐个检查，本脚本只管该 Compose 节点。直接跑 backup 服务不替你停写，程序运行时会拒绝。

### 与程序的对象存储分开

备份使用 `AXIOM_BACKUP_BUCKET` / `AXIOM_BACKUP_S3_*`，程序用 `AXIOM_OBJECTSTORE_*`，桶和凭证必须分开；Compose 不把备份凭证交给公网程序。运维镜像从发布镜像取同版 axiom，以 `axiom objectstore` 共用签名、期限、重试、multipart 实现；桶需支持 HEAD/PUT、对象 PUT/GET/HEAD/DELETE、ListObjectsV2、multipart。

备份 S3 endpoint 留空按 region 补 AWS 地址（中国区 amazonaws.com.cn），程序 endpoint 仍须显式设置。objectstore check 只 HEAD；create 对已有桶先 HEAD 并要求 `--allow-existing`，不重复 PUT 以免重置 ACL。HEAD 只证明能访问，不证明归属，桶策略须仅允许自己的凭证。

程序桶只放归档：整月 call_log/call_attempt 分区写到 `call-log/v1/<YYYY-MM>/`，清单确认才删数据库分区；窗口为 0 或未设桶时不删，未设桶只预建。设桶须同时设 `AXIOM_RETENTION_DATABASE_URL`，否则拒绝启动。归档无到期、不在 set 内，丢失只丢调用明细、账本与汇总仍在；对话正文开关仍关闭。

### 保留

`AXIOM_BACKUP_KEEP_SETS` 与 `AXIOM_BACKUP_KEEP_DAYS` 默认各 30，任一超出即删，两者均须 ≥1，否则锁和上传前退出 1。只数完整 set，半组只按天清，当前运行 set 永不删；没有永不删除的特殊值。上传/下载总期限 `AXIOM_BACKUP_TRANSFER_TIMEOUT` 默认 12h，按 5GiB、2MiB/s 留余量；脚本显式覆盖 CLI 的 30 分钟默认，卡住仍能退出。

### 单实例

作业另锁 backup_state 卷，忙退出 75（EX_TEMPFAIL），进程消失自动释放；包装锁覆盖停写到上传全过程。它们只守本机，多机调度须另协调，不能据此声称全局单实例。

### 运行

使用 `scripts/ops/backup.sh`，带入口时传平时部署的同组 `-f` 文件；调度归宿主 cron/systemd timer，每日运行并检查退出码（75 忙、1 失败含日志未排空）。直接 run 点名服务不需要 ops profile，普通 up 不启动备份/恢复。配置见 `deploy/.env.example`，不要另抄脚本流程。

## WAL 连续归档与时间点恢复

逻辑前缀 `AXIOM_BACKUP_PREFIX` 与 WAL 前缀 `AXIOM_BACKUP_WAL_PREFIX`（默认前者加 `-pgbackrest`）平级；去首尾斜杠后，WAL 不可等于逻辑前缀或其子路径，否则启动拒绝，防止逻辑保留删掉 WAL。

数据库镜像固定 pgBackRest 2.58.0（Alpine 3.24 的 2.58.0-r0）。WAL 桶默认备份桶，可用 `AXIOM_BACKUP_WAL_BUCKET` 覆盖；非空时入口设置 archive_mode=on、archive-push、archive-get、archive_timeout=300，空时不加参数、归档关闭。300 秒让空闲 WAL 最多 5 分钟封段；参数不写 PGDATA，恢复实例不会自动向源归档。

仓库整体以 `AXIOM_BACKUP_CIPHER_PASS` 作 AES-256-CBC 加密；口令离线另存，丢失无法读 WAL，age 逻辑恢复仍可用。已有仓库不原地换口令、也不直接把明文仓库改成加密，须另建仓库。

### 开启与日常

1. 设置备份桶、S3、cipher pass 及需要的 WAL 桶/前缀，AWS S3 用 URI_STYLE=host。WAL 桶非空却缺口令拒绝启动。
2. 数据库环境变化按[生产入口](#生产入口)的 `--database` 升级：停并排空节点后换库，不能在运行节点下直接重建。
3. 用镜像内 pgbackrest 对 stanza axiom 执行 stanza-create、check；初次 stanza 建立前 archive-push 缺 archive.info 是预期失败。
4. 用 Compose 的 basebackup 服务运行 `deploy/postgres/basebackup.sh`，每次 full；保留 7 份 full，未另设 archive retention 时 WAL 随这 7 份保留。宿主每日调度，若改每周则窗口约 7 周，需按真实库大小/写入量选择。
5. info 核对备份和 WAL min/max，check 核对可读可推。桶不要另给 WAL 前缀配置生命周期删除，expire 已管保留；版本控制、对象锁和副本由部署选择。

全量不依赖易中断的增量链，代价是每次读全库；pgBackRest 在数据库本机同时管理基础备份、归档、校验与保留，升级需重建镜像、check 和恢复测试。

### 时间点恢复

1. 先用 info 确认目标有备份和 WAL 覆盖；`AXIOM_PITR_TARGET` 用带时区 UTC，必须晚于所用基础备份 stop time，同秒也拒绝。大库先停源写入：恢复与源 archive-push 共用锁卷，期间 WAL 会积在 pg_wal。
2. Compose pitr 服务执行 `deploy/postgres/pitr.sh`，只接受空 postgres_pitr 卷，选目标前最新基础备份，按 time/target 重放并 promote，在回环 `AXIOM_PITR_PORT`（默认 5433）启动，不向源归档。
3. 确认 pg_is_in_recovery() 为 f，关键行和数量与目标一致；失败先停恢复实例，确认卷名后只删恢复卷、从空卷重做，不覆盖源 postgres、不续用半恢复卷。
4. 查恢复库 key_versions，准备覆盖该时刻的完整 master。轮换前归档原件必须不可变；先复制到独立可写目录，再用 keyring check 检查候选副本。check 会删除数据库已退役且无引用的版本，不是只读，不能直接查历史原件或只读挂载。通过才以 0600、正确属主装入 keys；失败换历史文件或放弃目标，每次更早恢复重新复制。
5. 手工接管前停恢复实例和程序，把 call_log 复制到新卷再移除原卷，避免重放目标之后有记录却无收费的调用；审计副本改新桶、保留旧桶并记录关系，避免两条分叉链冲突。
6. 停源库，先将旧 postgres 完整复制到新的空备份卷，再将恢复卷内容换入 postgres；不能无副本覆盖、不能往旧副本上重复复制。正常入口以原 system identifier 的新 timeline 续同 stanza。启动核对、pgbackrest check 通过后才决定旧卷/WAL 去留。

换卷接管尚无自动脚本，测试只到恢复实例核对；第一次真实 PITR 前须在演练环境走完整接管并记录 RTO。不可把这段手工流程当成已自动验证的恢复。

### 和逻辑 set 的关系

逻辑恢复 initdb 产生新 system identifier，不能接旧 WAL 仓库。restore.sh 在装回的环境文件将 WAL 桶置空，后续启动也保持关闭；逻辑备份桶保留。核对后给新集群设新 WAL 前缀/仓库并 stanza-create，旧仓库保留。演练断言空 WAL 桶已写入文件，WAL 恢复由 `internal/platform/backup` 容器测试覆盖。

## 主密钥轮换

使用应用角色的 `AXIOM_DATABASE_URL` 与 `AXIOM_MASTER_KEY_FILE`，须含 platform 00022 迁移。仅负责人接触文件，以服务账户执行；目录限该账户与负责人、文件 0600/0400（Windows 用 ACL）。命令仅打印版本/结果/批量，不输出材料、摘要或路径，不用 shell 输出密钥核对。

1. 完整备份后暂停备份调度，保留旧 set 配套密钥及覆盖整个 PITR 窗口的轮换前完整 master。脚本从转储快照查版本并核对归档，跨轮换缺版则失败重跑；不能把旧库搭配已删旧版的当前文件。
2. keyring add 用未使用的正版本号，原子生成新材料、登记可读，不改写版本。父目录须可写，不在只读或单文件 bind mount 操作；隔离操作先复制完整现有文件，不能从空文件开始。
3. 分发同一完整文件到所有节点，包括离线备用，不能各自生成同版本；逐实际文件 keyring check。运行节点下一心跳热加载并报告已核对摘要的 key_versions/key_active，不需停 serve。
4. 全部在线节点报告新版本后自动激活并审计，每轮重包裹 100 个信封、一步；全部引用迁完且全部在线节点写新版本超过 30 秒才退役旧版。check 应显示新 active/readable、旧 retired，文件无旧版。
5. 恢复调度并新做 set，旧 set 保留到正常过期。审计 initialize/add/activate/rewrap/retire 仅含版本、表与数量。

手工 activate/rewrap/retire 共用生命周期锁与同样前提，rewrap 可设 batch-size（默认 100）和 max-batches；退役拒绝写版本、引用中版本或仍用旧写版的在线节点。自动只往高版切换，手工回退须先停全部 serve，回退重包裹完成才重启。

retire 先提交墓碑后删文件，中断可重复；启动、check、在线循环确认无信封引用后也清退役材料。有引用则报错并保留；缺可读/被引用版本或摘要不符均拒绝启动，只报版本。退役不可逆，旧版本号和材料都不可复用（指纹墓碑拦截），只能整套旧备份恢复。

重包裹仅改[信封清单](../../internal/platform/README.md#凭证加密)的 wrapped DEK 与版本，不改密文、nonce、业务状态/revision；Webhook 签名材料与重叠期限不变。新 wrapped_dek 列必须登记 cmd/axiom 清单，启动和命令发现漏登记即拒绝。hash/identity 不属 master 轮换范围。

登录键和邮件键由进程启动时最高版本派生、不随热加载；混合重启期间计数桶可能按版本分开，只影响限流分桶不影响认证，需要一致则轮换后滚动重启全节点。

## 恢复

支持空白环境：无库、密钥环、调用日志；不在运行库上直接覆盖。先准备 set 同版程序 digest（升级在恢复之后）、本仓库 Compose 文件与数据库/ops 镜像、可访问当前备份位置的配置、两份离线 age 身份。数据库镜像也必须存在，脚本在第一步前 inspect 检查；ops 构建依赖先拉好的程序镜像，三个库口令占位让 Compose 可解析。

入口是 `scripts/ops/restore.sh`，参数为 set 与正常部署同组 `-f`/`--env-file`；给 -f 替换默认 compose.yaml。身份由 `AXIOM_RESTORE_DB_IDENTITY`、`AXIOM_RESTORE_SECRETS_IDENTITY` 文件只读挂载，不入环境配置。脚本固定启动时解析出的备份位置与凭证，经环境传递避免命令行泄漏；只固定 BACKUP 变量，不覆盖程序 OBJECTSTORE，set 里的旧凭证不能改后续下载位置。

失败即停的六步：

1. 下载解密环境文件、安装为 0600；清空审计副本六变量并注释旧桶与恢复关系，WAL 桶也清空。所有重复赋值、export、空白/引号形式由共用 env-file 解析，末尾只留空值。
2. 用 set 口令 initdb，roles.sh 建 axiom_app/migrator；删旧 Valkey 与 keys 容器以重生成 ACL/缓存口令，缓存卷不删，避免复用旧 ACL。
3. migrate 建 NOLOGIN 的 axiom_owner/lookup 和 schema，角色定义不在脚本重复。
4. 下载解密转储，先删 owner 所有 schema（不含 public），single-transaction 恢复对象/属主/授权，避免分区继承约束的逐个 clean 冲突；解密密钥到卷，属主 65532、0400。
5. 再 migrate 核对/补迁移。
6. up --wait 后验证请求；带入口时必须经入口：控制台域名 site=200、API 域名同路径=404、无 key 的 /v1/models=401；无入口才查两个回环端口，回环成功不能证明外部路由可用。

库卷或调用日志卷存在且无 `--overwrite` 则拒绝；显式覆盖会删本项目库/密钥卷及使用它们的容器，不动其他服务（如存 set 的 S3）。调用日志先复制到 `<项目>_call_log_before_<UTC>` 再删原卷，新节点从空日志开始，保留 set 之后调用的证据。

恢复后核对镜像 digest 与 manifest，在迁移与启动检查通过前不对外。审计旧桶保持不动，新建版本控制/对象锁桶、重新填副本变量，节点从第一条复制恢复链；WAL 按上节建新仓库。换泄露口令和桶凭证后再做 set，旧配置不能代表新凭证。

## 恢复演练

`scripts/ops/rehearse.sh`（`just backup-rehearsal`）需要 Docker/Go/Node/pnpm，同机只跑一次，用独立项目、env、随机发布端口、本地 RustFS，结束清自己的卷/镜像。开跑清继承 AXIOM/DEPLOY/COMPOSE 环境；每次写桶前在容器核对演练桶、前缀、endpoint/access key，避免覆盖真备份。

| 断言 | 守住的边界 |
|---|---|
| Caddy acme 默认与 internal 均 validate；演练只用 internal/.example、172.29.0.0/24，connect-to 保留 Host 443 | 无外部请求、不撞生产网络；不证明公网 ACME |
| 种站点、用户、非默认价格并断言表有行；记录全库指纹、角色/成员、密钥哈希、环境 | 不能用空表有指纹冒充种数成功 |
| spill 三条已结算加一待定时备份退出 1、报告四条、桶空；排空后四条随 set 恢复 | 不漏调用日志 |
| 程序运行时拒绝备份；保留份数/天数各为 0 拒绝且桶不变；并发退出 75；密钥只有独立身份能解 | 停写、保留、锁与身份分离 |
| 删库/密钥/日志后恢复，比指纹并经双域名入口验证；后两次备份仅留最新两份 | 空白恢复与保留 |
| set 后副本领先仍不改旧桶；恢复清副本变量并记旧桶，新桶从头复制 | 审计分叉隔离 |
| 轮换 S3 凭证并迁前缀后恢复；无 overwrite 拒绝，有则成功；后来的两条日志只在挪开卷 | 固定当前下载凭证，隔离 set 之后记录 |

指纹总核表清单，不允许少表；行比较跳过自动变化的 River 队列、billing.holders/expiry_cursor、platform.alert_signals/credential_share_departures、catalog.sync_state、platform.billing_preference_readiness/model_prices_public_readiness。observability.alert_events 与 platform.discarded_jobs 只要求原行都在；billing.price_policies 仅排除 confirmed_at，rev 与三个乘数仍逐行比较。

### 恢复演练记录

演练输出 set、RTO、检查结果，保存在发布或演练 Issue，不在本文追加历史。RTO 从执行恢复脚本到 up --wait 返回，不含备份；被保留策略清掉的 set 仍可作为该次运行标识。

## 生产入口

`deploy/compose.edge.yaml` 加 Caddy，配置在 `deploy/ingress/Caddyfile`；带固定子网，不能强加给同机多栈开发部署。按[发布](release.md#选择和核对)准备 `release.json` 与 `DEPLOY_IMAGE=<仓库>@sha256:<摘要>`，只用 `scripts/ops/deploy.sh`，不在生产运行 just up、不用 tag、不重建程序。

- deploy 从本次 -f/env-file 解析的实际 Compose 读 axiom/migrate 镜像（含 shell 覆盖），要求同 digest，核验签名、workflow 身份、digest 与 SBOM 后将它固定用于 pull、发布前端资源、调用 up.sh；任何步失败即停，验签失败不拉不启动。三份库口令必须已配置。
- **应用发布不动库**：节点/库运行时比对镜像、Compose postgres 配置哈希、镜像的 `axiom.postgres.source` 与本树构建输入；变化即不做修改、要求 `--database`。输入含 deploy/postgres 除挂载且每次重放的 roles.sh；摘要缺失也拒绝。普通发布重放角色授权、迁移完才以 no-deps/no-build 换节点。
- **数据库升级**：先暂停备份调度，`--database` 排空停节点再构建换库、起整栈，期间入口 502；首次/已停栈同路径。完成恢复调度，跑 pgbackrest check 和恢复测试。数据库镜像由仓库构建，不属发布 artifact；即使缓存命中重建也可能变 ID，因此不能每次发布顺带重建。
- **配置不静默丢弃**：Compose 逐个传核心参数，不传其固定的监听、卷路径、库/缓存地址与 `AXIOM_SHUTDOWN_TIMEOUT`；up 前按合并后未插值、全部 profile 配置核对 env 文件每个非空 AXIOM 变量确被引用，覆盖掉的引用不算。后赋值生效、引号按 Compose、未引号空格 # 后为注释、去行尾 CR；空值视作未设。拼错或试图覆盖固定代理变量会失败；不核对宿主无关导出变量。
- **密钥环整套生成**：master/hash/identity 全缺且 `AXIOM_KEYRING_GENERATE=true` 才生成；部分缺失永远退出 1。仅 true/false 有效，显式空值也拒绝，完全未设才默认 true。生产首次成功后固定 false，卷丢失必须恢复整套、不补单文件；手工替换 hash 尚不能检测，新增密钥文件需单独升级流程。

| 入口规则 | 配置与行为 |
|---|---|
| 三域名 | `AXIOM_EDGE_CONSOLE_HOST` / `AXIOM_EDGE_API_HOST` / `AXIOM_EDGE_ADMIN_HOST` 各自唯一 surface，均配 DNS，与 site init 登记一致、Host 原样转发；console 供页面和 /api/console/v1，api 供 /v1 与 /api/v1，admin 仅后台；控制台路由到 API 域名 404，模型请求到 console 被拒 |
| TLS | ACME HTTP-01 自动申请续期，80 可达，证书/账号在 caddy_data；内网设 `AXIOM_EDGE_TLS_ISSUER=internal` 用内部 CA 并信任根证书。Compose 显式填 acme，不能把空 issuer 传入；仅 h1/h2 TCP，不广告无发布端口的 HTTP/3 |
| 端口 | `AXIOM_EDGE_HTTP_PORT` / `AXIOM_EDGE_HTTPS_PORT` 默认 80/443，显式空则内核分配 |
| 代理信任 | Caddy 第一跳忽略客户端 X-Forwarded-* 并按对端重写；`AXIOM_TRUSTED_PROXIES` 只信 `${AXIOM_EDGE_PROXY_ADDRESS:-172.28.0.10}/32`，宿主发布端口来源不可信 |
| 流 | 不设 flush_interval、不加 encode，SSE/未知长度逐写下发，断开取消；负 flush_interval 会继续调用，不能用 |
| 期限 | read_header=10s，read_body=2m，write_idle=1h，无总写期限，单次调用由程序限时 |
| 来源 | 后台 API 在两个公开域名一律 404；admin 来源由程序 AXIOM_ADMIN_SOURCES（逗号分隔、默认本机）按可信代理重写地址判断；旧 AXIOM_EDGE_ADMIN_ALLOW_CIDRS 无效。console 页/API 可用 AXIOM_EDGE_CONSOLE_ALLOW_CIDRS（空格分隔）限来源，默认全开；API 域名不限来源 |
| 前置 CDN | 配 trusted_proxies 的 CDN 网段与 trusted_proxies_strict，443 仅 CDN 可达，防伪造地址 |

## 决策与代价

逻辑 set 保角色、schema、配置与密钥；PITR 依赖同 PostgreSQL 大版本、仓库和口令，RTO 随数据/WAL 量变化。仓库内 Compose/Caddy 文件不入 set，须取对应代码与同组 -f。age 只需公钥，无信任网；角色由 initdb 与迁移重建，不再复制定义。其他取舍已写在各步骤旁。

## 待定

多机单实例协调、cipher pass 轮换步骤、热备与自动切主未实现；真实库的全量频率/保留需实测。恢复演练尚未进 CI 常跑；PITR 接管和公网 ACME 未自动验证，首次真实使用前分别演练/验证。更换 RustFS 等 S3 实现必须重跑演练确认客户端兼容。
