# platform

所有模块共用的基础设施，不放业务规则。总体规则见[总体架构](../../docs/architecture.md)。

## 包

| 包 | 做什么 |
|---|---|
| `platform`（本目录） | 各模块共用的值类型：单价 `UnitPrice`、精确金额 `Exact`、币种 `Currency`（三个大写字母，由 `ParseCurrency` 检查）、可复制的汇率依据 `ExchangeRate`（行 ID、版本、规范化币种对与整数分子/分母，`Between` 只取精确分数或倒数）。只有类型和运算；支持币种、数值范围、冻结与取整规则由 [billing](../../docs/domains/billing.md#汇率) 校验和决定 |
| `config` | 把 `AXIOM_*` 环境变量读成带类型的值，收集全部问题后一次报告 |
| `secret` | 从受保护文件加载主密钥和 HMAC 密钥；供应商凭证的信封加密与明文脱敏 |
| `httpserver` | 所有监听共用的限制；公网监听的中间件；`Compress`（只压缩较大的 JSON 响应，见下文「前端文件」）；健康检查；`/metrics` 的挂载点；公网请求的跨度与指标 |
| `clientip` | 客户端地址的唯一一份读法：连接对端的地址，或 `AXIOM_TRUSTED_PROXIES` 里的代理给的最右一个不受信地址（变量见下文「监听」）；控制台守卫和网关都用它，读不出的头一律不信；`Group` 为支付回调归并映射 IPv4 与 IPv6 /64 的来源额度，登录与网关的分组迁移由 #393 交付 |
| `webui` | 提供嵌入程序的前端三个应用：缓存、预压缩、安全头、单页应用回退，见下文「前端文件」；`cmd/serveweb` 给端到端测试起同样的服务 |
| `egress` | 共用出站 HTTP 客户端：校验全部目标 IP、固定拨号目标、限制请求期限和响应体读取 |
| `egresstest` | 仅测试进程可构造的回环放行能力，零值不放行任何地址 |
| `consoleapi`、`adminapi` | 由 `api/console.yaml`、`api/admin.yaml` 生成的接口类型和服务端接口，除包说明外不手写；约定见下文「接口约定」。生成的代码里内嵌的契约是 gzip 后的 base64，会被 gitleaks 的 `generic-api-key` 误报，所以仓库根的 `.gitleaks.toml` 只对这两个包的 `.gen.go` 放行，别处的密钥照常拦下 |
| `jsonx` | 全项目的 JSON 策略：`Validate` 检查一段数据恰好是一个合规的 JSON 值，一个接一个检查许多段（流的事件）的调用方用 `Validator`，复用解码器，规则同一份；`Unmarshal` 在 `Validate` 通过后按 v2 的默认规则解码（只取目标类型列出的成员，名字区分大小写），数据读两遍；`Decode` 读完一个 `io.Reader` 再 `Unmarshal`，空的是 `io.EOF`，生成的处理器用它读请求体（见「接口约定」的守卫）；`UnmarshalKnown` 同 `Unmarshal`，另外拒绝目标类型没有列出的成员，给我们自己定义的存储格式用；`MarshalCanonical` 按 RFC 8785 写出（成员排序、最短的字符串写法），整数原样；按位置读改字段的接口随第一个用到它们的 PR 加入，现在还没有；`protocol` 为了一次扫描读完请求体，直接用 `jsontext` 逐个读记号，是这条规则记录在案的例外（nolint 在引用处，理由见 [protocol 的决策表](../protocol/README.md)「摘要一次扫描」一行） |
| `pg` | 连接池、`Hold`（在池外的一条专用连接上持有会话级 advisory lock，连接断了就知道，节点名用它，见 [kv](kv/README.md#配置与启动)）、带租户范围的事务、迁移、启动时的数据库检查、任务队列（River，见下文「任务队列」）；`IsDataError` 区分「数据本身被拒」（SQLSTATE 22、23）和「没写进去」，后台写入者只重试后者；`Unavailable` 认出数据库连不上或不接连接（连接失败、SQLSTATE 08、57P01 到 57P03、53300），返回固定的原因文本 |
| `containertest` | 测试容器的共享宿主策略：`WithCgroupParent` 读取 `AXIOM_TEST_CGROUP_PARENT`，在已有宿主配置之后应用；未设或空值保留原配置。所有直接启动测试容器的入口都使用它；testcontainers 内部的 Ryuk 没有对应定制入口，diff 在 CI 避免启动 SSHD sidecar，见[CI 在哪跑](../../docs/architecture.md) |
| `pgtest` | 测试用的 PostgreSQL，按部署的方式准备好；`New(t, 模块…)` 把给出的模块的迁移也做进模板，同一次运行里每次调用给的模块必须相同；一次 `just test` 的所有包共用一个集群（`withcluster` 同时起一个 Valkey，见 `kvtest`） |
| `alloctest` | 测试用的分配测量：父进程重跑本测试二进制，子进程只跑被测函数（堆先结算、GC 关掉、单核），在 stdout 报告单次调用的分配次数和字节数（5 次中最小的，运行时自身的分配只会加到某一次上），测试二进制里其他 goroutine 的分配不计入 |
| `telemetry` | 指标和链路的提供者，见下文「遥测」 |
| `audit` | 在改动自己的事务里写审计日志，见下文「审计日志」 |
| `idem` | 管理接口写操作的幂等键：`Once` 在改动自己的事务里占键、执行、存下响应，见下文「幂等」 |
| `typeid` | 对外 ID 的写法：`Prefix` 把 `uuid.UUID` 编成 `前缀_` 加 26 个字符（TypeID 规范 v0.3.0），`Decode` 读回；前缀由各实体的所属模块定义，类型化的 ID（`SiteID` 等）随各自的表加入，见 [accounts 的「ID」](../accounts/README.md#id) |
| `kv` | Valkey 的访问层：每次访问的期限、熔断、与 Valkey 的时钟修正、节点心跳（`platform.nodes`）和指标；限流计数（K1）和登录前的尝试次数（K2）已经在用，登录脚本由 accounts 持有，通过 `Login` 用途标签和 `LoginSpace` 键空间复用访问层；登录键的隐私、到期及按节点等分的故障兜底见 [kv 的每种用途](kv/README.md#每种用途)，会话粘性（K3）同批读取、经有界后台队列写回（后台最多占 32 个在途位置、不排在前台之前、不驱动熔断）；缓存三类状态已共享，供应商并发与 RPM 仍未分节点份额，因此保留 N > 1 的生产入口限制（[状态核对](kv/README.md#多节点状态核对)）；`kvtest` 给测试一个按部署方式配好的真实 Valkey，见 [kv 的 README](kv/README.md) |
| `mail` | 出站邮件：SMTP 发送、消息组装、任务队列（`mail.send`，`notifications` 队列）和按收件人的限流，见 [mail/README.md](mail/README.md) |

配置项和默认值以 `cmd/axiom` 里的读取为准，不在文档里另列；下文只在说明行为时提到变量名。

## 不变量

| 不变量 | 靠什么保证 |
|---|---|
| 出站连接只到已校验的公网 IP | 自定义 DialContext：一次解析、全量校验（任一结果不合格则整组拒绝）、按 IP 拨号；Host 和 TLS SNI 保留 URL 原主机；连接池复用已校验的连接，新连接重新解析校验 |
| 调用方不能替换出站安全策略 | `egress.Client` 不暴露 Transport、Resolver 或 Dialer；生产构造器只支持 `Direct` 出口，未知出口报错，不读取代理环境变量 |
| 生产不能放行内网 | `egresstest.Policy` 的放行状态未导出，构造器检查 `testing.Testing()` 且要求测试句柄；只放行无 zone、非映射的回环地址，其余策略保持不变 |
| 出站请求有期限，不因重定向改变目标 | `Do` 拒绝没有 context deadline 的请求；直接 RoundTrip 返回第一个响应，不解析 Location；拒绝并关闭 `101 Switching Protocols` 响应；固定 User-Agent 为 `Axiom` |
| 出站错误不包含 URL 用户信息 | 拒绝 URL userinfo、opaque URL、空主机和非 HTTP(S) 协议（`ErrInvalidURL`），不把完整 URL 拼进错误，地址被拒时只带主机名；凭证通过 Authorization 头传递 |
| 配置有错就不启动，并指出是哪一项 | `config.Env.Err` 一次列出全部非法取值和未知变量；`serve` 在监听任何端口之前检查；地址被占用或不合法时启动失败 |
| 设了却没有被读取的 `AXIOM_*` 变量算错 | 同上 |
| 配置问题只报变量名和期望的格式，不报取值 | 密钥可能填错了变量或拼错了名字；监听失败只报原因，不报地址 |
| 密钥不出现在日志里 | 凭证明文用 `secret.Secret` 保存，格式化、slog、JSON 和文本序列化统一脱敏；只有显式 `Reveal()` 取出明文。主密钥加载错误只含变量名、行号和原因，不含文件内容或路径；调用方不得记录 `Reveal()` 的结果 |
| JSON 只按一套策略读 | `jsonx` 用 v2 默认的严格模式（拒绝非法 UTF-8 和重复键：两个解析器各取一份时，我们计费的用量就不是客户端读到的用量），嵌套不超过 256 层；`jsontext` 自己的上限固定为 10000，所以逐个读记号时检查 `StackDepth`。模糊测试与 `jsontext` 自带的校验对照，只允许在超过 256 层时不同。未知成员默认跳过，因为上游和客户端会加字段；我们自己定义、要存下来的格式（如价目表）用 `UnmarshalKnown` 拒绝，读到不认识的条件就报错，不会忽略后按别的含义处理。存储并按字节比较的 JSON 用 `MarshalCanonical` 写，相同的值得到相同的字节；整数不按 RFC 8785 经过 float64，超过 2⁵³ 的门槛和计数才不会变。入口按行为命名，不给调用方传选项，宽严只在这里定。depguard 禁止 `jsonx` 之外引入 `encoding/json` 及其子包；生成代码本来不在 lint 范围内，其余例外在引用处用 nolint 写明理由。`jsonx` 的豁免是 `.golangci.yml` 里锚定在仓库根的路径规则（`^internal/platform/jsonx/`），不是 depguard 的 `files` 通配：后者匹配绝对路径，`**/internal/platform/**` 连 `internal/<模块>/internal/platform` 也放过 |
| 空值等于未设定 | Docker Compose 把未设定的 `${VAR}` 传成空串，按未设定处理才不会把默认值意外覆盖成空 |
| 模块只能经带范围的事务访问数据库 | `pg.DB.Tx` 和只读的 `pg.DB.ReadTx` 是仅有的入口，先设范围再执行；`pg.Tx` 不暴露连接：`Exec`、`Query`、`QueryRow` 的签名与 sqlc 生成的代码要的一致，`Query` 的结果逐个转发 `pgx.Rows` 的方法，唯独 `Conn` 返回空，pgx 给 `Rows` 加方法时编译失败，不会悄悄转发出去；pgx 另外两处交出连接的回调也拦下：作为唯一扫描目标的 `RowScanner` 拿到的是这种不带连接的结果，参数开头的 `QueryRewriter` 拿到的连接是空。测试检查结果，以及经 `Rows.Scan`、`Row.Scan`、`Exec`、`Query`、`QueryRow` 的回调，都交不出连接，结果也没有 `pgx.Rows`、`pgx.Row` 以外的方法。depguard 禁止 `internal/platform` 之外引用 pgx 和 `database/sql`，豁免同样是锚定在仓库根的路径规则（`^internal/platform/`） |
| `ReadTx` 的各条查询看到同一时刻的数据库，而且不能写 | 隔离级别 REPEATABLE READ 和只读由 `BEGIN` 本身设定：设范围的查询执行后 PostgreSQL 不再允许改隔离级别。测试在两次读取之间从另一条连接提交插入，第二次读取看不到，下一个事务看得到；`ReadTx` 里删除失败 |
| 范围只在本事务内有效 | 用 `set_config(…, true)` 设定；测试在同一条连接上让事务分别提交、出错、panic，之后检查连接本身不带范围 |
| 范围和角色只由 `pg` 设定 | 行级安全防的是漏写的条件，不防我们自己代码里有意放宽范围的 SQL：自定义参数任何角色都能设，事务里一句 `set_config` 或 `SET LOCAL` 就能扩大范围，数据库拦不住；会话级切换的角色还会留在池里的连接上。所以 `just scope`（规则和理由写在 `scripts/check_scope.py`，用例在 `scripts/test_scope.py`）在 `internal/platform/pg` 之外见到 `set_config`、范围参数名（`scope_platform`、`scope_sites`、`scope_accounts`）或 `SET`/`RESET ROLE` 语句就失败，不分大小写，测试和迁移也不例外；只认参数名本身、不认 `axiom.` 前缀，点号两侧的空格、引号、转义和换行都藏不住它；角色语句按数据库读到的样子匹配（`.sql` 文件和 Go 字符串，先解开 Go 转义，原始字符串丢掉回车），先认字符串再认注释（引号、转义字符串 `E'..'`、美元引号 `$$..$$`/`$tag$..$tag$` 里的注释标记是普通文字，美元引号正文里的注释照样抹掉，DO 块会执行它），`SET` 与 `ROLE` 之间的空白、换行和 `--`、`/* */`（可嵌套）注释都藏不住它，注释里的语句不会执行，不算；运行时拼出的 SQL、相邻字符串字面量拼成的 SQL 和 `pg` 里面靠评审 |
| 切换的角色只在本事务内有效 | `pg.Tx.SetRole` 用 `set_config('role', …, true)`，等于 `SET LOCAL ROLE`；普通的 `SET ROLE` 会留在池里的连接上。测试在同一条连接上让切换过角色的事务分别提交、出错，下一个事务仍以 `axiom_app` 执行；切换后范围照样生效；切到登录角色不是其成员的角色失败 |
| 没有范围就什么都查不到、写不进 | `platform.in_scope` 把未设定或空的参数当作空集合；测试覆盖读、插入、修改、删除。格式不合法时查询报错，不会放宽 |
| 范围是集合 | 平台 ∪ 站点集合 ∪ 账户集合；测试覆盖"整站 S1 加 S2 的账户 A3"不会扩大到 S2 的其他账户 |
| 策略里的范围函数被内联进查询 | `in_scope` 的函数体是简单表达式，PostgreSQL 把它代进策略，过滤条件里出现的是函数体而不是调用；`TestScopeIsInlinedIntoPolicies` 读 `EXPLAIN` 的输出，要求函数体的两个 `current_setting` 都在、函数名不出现，两组断言同时排除空计划和子查询写法（带子查询的函数体不再被内联，每行一次调用） |
| 公开数据任意非空范围可读、只有平台范围可写 | `platform.in_any_scope` 与 `in_scope` 一样把未设定或空的参数当作空集合，格式不合法时报错；测试覆盖站点、账户、平台范围可读，无范围读不到，站点和账户范围里插入失败、修改和删除找不到行，平台范围可写 |
| 只有 `cmd/` 能构造平台范围，只有 `internal/accounts` 能构造站点和账户范围 | 能构造范围的模块就能读写范围里所有租户的数据。forbidigo 在测试文件之外拒绝构造任意范围的 `pg.NewScope`，在 `cmd/` 和测试文件之外拒绝 `pg.PlatformScope`，在 `internal/accounts` 和测试文件之外拒绝 `pg.SiteScope`、`pg.AccountScope`（`cmd/` 也不例外），`internal/platform` 自己也不例外 |
| 应用的数据库角色受行级安全约束 | 表强制启用行级安全，所有者也受限；`serve` 启动时从登录身份本身（`session_user`，不是连接后切换成的角色）出发，检查它能成为或继承的全部角色：不是超级用户，没有 BYPASSRLS、REPLICATION、CREATEROLE、CREATEDB，进不了 `axiom_owner` 和 `axiom_lookup`，也不在读写服务器文件、在服务器上执行程序的预定义角色里；迁移时已存在的 `axiom_owner`、`axiom_lookup` 不能是超级用户、带 BYPASSRLS 或能登录 |
| Axiom 的 schema 里每张表都受行级安全约束 | `serve` 启动时检查各模块同名的 schema（`platform` 和[依赖表](../../docs/architecture.md#模块之间的依赖)里的模块）：除版本表外，每张表都 ENABLE 并 FORCE 行级安全，并且有策略；没有策略的表什么都查不到，是迁移漏了一步，在启动时报出，而不是在运行时表现为查不到数据。测试分别建只 FORCE 没 ENABLE、只 ENABLE 没 FORCE、没有策略的表 |
| 登录前的查找只经 `axiom_lookup` 的定义者函数 | `serve` 启动时检查：这些 schema 里的 `SECURITY DEFINER` 函数都属于 `axiom_lookup`，`search_path` 固定为 `pg_catalog, pg_temp`，除所有者外只有 `axiom_app` 能执行；`axiom_lookup` 存在（不存在说明没跑过迁移，提示先运行 `axiom migrate`），不能登录、不是超级用户、没有 BYPASSRLS，不拥有任何表，在任何 schema 里都没有 CREATE。测试分别构造函数的三种违规，以及查找角色能登录、拥有表、能在 schema 里建对象的情况；查找角色是超级用户、带 BYPASSRLS、不存在这三种改的是整个实例的角色，在不并行的测试里改完即恢复 |
| 数据库缺少本程序的迁移就不启动 | `serve` 启动时逐个模块比较已执行的版本，缺版本表也不启动；数据库比程序新可以运行，因为迁移只做扩展 |
| 数据库不应答就不启动，不在监听之前无声地卡住 | `serve` 连接数据库和启动检查合计最多等 `AXIOM_STARTUP_TIMEOUT`，超时后报这两个变量名，不报地址；测试用只建立连接、从不应答的服务器 |
| 退出时停止接收新请求，在途请求在上限时间内完成 | 第一个 SIGINT 或 SIGTERM：就绪状态先改为否，随即关闭监听，在途请求最多等 `AXIOM_SHUTDOWN_TIMEOUT`，到时关闭剩下的连接，请求的 context 随之结束，按 context 执行的数据库查询中止并归还连接，连接池才能关闭（不看 context 的处理器仍会拖住退出）；第二个信号立即退出 |
| 对外 ID 只有一种写法，别的实体的 ID 读不进来 | `Prefix.Decode` 要求整串恰好是前缀、`_` 和 26 个小写字符，首字符不超过 `7`（超出 128 位），其余按字母表逐个查，不接受大写、连字符或易混字符；错误一律是 `ErrInvalid`，不带输入。`Prefix` 构造时按规范检查，常量写错在启动时就 panic。测试：规范自带的有效、无效样例（`testdata`，原样），模糊测试检查凡读得进的，`Encode` 写回原串 |
| 改动和它的审计条目一起提交或回滚 | `audit.Record` 只在调用方的 `pg.Tx` 里写；条目被拒时返回错误，PostgreSQL 也让出过错的事务只能回滚。测试：条目被拒时，同一事务里先写的改动也不在 |
| 应用不能改写、删除审计条目，也不能倒填时间 | `axiom_app` 对 `platform.audit_log` 只有 SELECT 和除 `id`、`at` 之外各列的 INSERT。测试：UPDATE、DELETE、TRUNCATE 和指定 `id` 或 `at` 的 INSERT 都报权限不足，平台范围里也一样 |
| 事务只读写范围内租户的审计条目，没有租户的条目只有平台范围能读写 | 一条 `platform.in_scope(site_id, account_id)` 策略管读和写。测试：已有平台的条目时，无范围、一个账户、一个站点的事务都记不进、读不到，平台范围能记能读 |
| 审计条目的站点和账户属于同一租户 | 租户登记表 `platform.tenants` 加从 `audit_log` 到它的外键：站点登记为 (站点, 站点)，账户为 (站点, 账户)，条目按生成列 `tenant_id = coalesce(account_id, site_id)` 引用；只带账户不带站点由 CHECK 拒绝。测试：两个已登记账户交叉的两种组合、未登记的站点都被外键拒绝，只带账户被 CHECK 拒绝；账户范围能登记自己、记自己的条目，读不到登记表 |
| 审计条目必有操作者、动作和对象，补充信息小 | 表上的 CHECK，Go 不另查。测试：缺操作者、动作、模块、对象种类或 ID，动作只有一个词，补充信息超过 8 KiB，都被拒 |
| 单价取值 0 … 1000，单位 10⁻¹⁵；录入的单价只来自十进制文本；同一单价只有一种写法 | 规则见[计费的「钱的表示」](../../docs/domains/billing.md#钱的表示)；算出来的单价从分数向上取整，本包还没有这个构造方式。`ParseUnitPrice` 拒绝符号、指数、超过 15 位小数（是 0 也拒绝）和超出上限的值；`String` 的输出没有前导零，小数末尾不留零。模糊测试对照 `math/big`：任意文本的解析，以及整个取值范围的格式化往返 |
| 精确金额的运算精确、不回绕 | `Exact` 是 128 位有符号整数，单位同单价。`UnitPrice.Mul` 的乘积小于 2¹²³，不会溢出；用量为负是调用方的错误，直接 panic。`Exact.Add` 溢出时返回错误，`Exact.Neg` 取相反数（最小值没有相反数，panic）。`Exact.Rat` 转成精确分数；`UnitPrice.Rat` 把单价转成精确分数，供应商的原生金额乘单位价值就靠它保持精确；`AmountExact` 把以十亿分之一为单位的金额转成 `Exact`，`Exact.CeilAmount` 向上取整回金额，超出 int64 是程序错误、panic，不回绕；模糊测试对照 `math/big` 覆盖整个 128 位范围（含两种金额换算），转分数的测试覆盖 128 位的两端和乘 10/71 后向上取整的边界 |

## 凭证加密

`secret.Load(path)` 在启动阶段加载主密钥：调用方用 `env.Required("AXIOM_MASTER_KEY_FILE")` 读出路径，`env.Err()` 为空后才调用 `Load`（否则变量未设定的错误会报两遍），加载失败同样不启动。`serve` 启动时读 `AXIOM_MASTER_KEY_FILE`（TOTP 密钥、供应商凭证和支付商户配置的封装）、`AXIOM_KEY_HASH_FILE`（API key 的哈希）和 `AXIOM_IDENTITY_KEY_FILE`（发往上游的会话与用户标识的派生，见下），缺了或不合法就不启动；本地的 `deploy/compose.yaml` 由 `keys` 服务第一次启动时生成这三个文件，放在与数据库同寿命的卷里，权限只有程序的用户能读。选择路径参数而不是另建环境变量读取器，复用 `config.Env` 的必填和未知变量检查。

文件为 UTF-8 文本，每行 `版本号:base64密钥`，允许 LF、CRLF 和末尾换行，不允许空行、注释、额外空白或 BOM（Windows PowerShell 5 的 `Out-File -Encoding utf8` 会写入 BOM，错误会落在第 1 行的版本号上）。版本号是无前导零的十进制正 uint32；密钥是带填充的标准 base64，解码后恰好 32 字节。至少一行，版本不得重复，不同版本的密钥也不得相同。文件列出可读密钥；数据库 `platform.key_versions` 单独记录唯一的 `active` 写版本，其他版本为 `readable` 或 `retired`，并存 SHA-256 摘要以检查同版本不同材料，绝不存材料。首次启动或首次轮换命令把旧文件的最高版本登记为写版本，以后增加文件行不会激活它；读取按记录的 `KeyVersion` 选择。文件必须是普通文件；Unix 拒绝任何组或其他人权限（建议 0600 或 0400）。Windows 的 Go 文件模式不能表达 NTFS ACL，因此不检查权限位，由部署使用 ACL 限制到服务账户和负责人。

`secret.LoadMAC(path)` 读 `AXIOM_KEY_HASH_FILE` 指定的文件，格式、权限和报错方式同主密钥文件（共用一个解析器），用于按哈希存放和查找的秘密（如 API key）：`Versions()` 从新到旧列出版本，`Sum(版本, 数据)` 是该版本密钥的 HMAC-SHA256，传入不存在的版本是调用方的错误，panic。最多几个版本由使用方规定。

`secret.LoadSecret(变量名, 路径)` 读 `AXIOM_IDENTITY_KEY_FILE`，格式、权限和报错同上，只返回最新一把密钥的 32 字节：网关用它经 HMAC 派生替代客户端会话和终端用户标识发往上游的值，上游的提示缓存按这些值分组，所以同一个会话从任何节点出去都要得到同一个值，密钥不能随存放版本变化，也不能与 API key 哈希同轮换，因此是单独的文件。轮换它会让所有会话的上游缓存失效一次，别的没有影响。

`secret.New(string)` 包装明文，零值表示空明文。`Keyring.Seal(Secret, Location)` 返回 `Sealed`，每次生成独立随机 AES-256 数据密钥，用 AES-GCM 加密数据，再用当前主密钥的 AES-GCM 包裹数据密钥。`Open(Sealed, Location)` 返回 `Secret`，失败不返回明文。`secret.Location{Table, RowID, Purpose}` 指明密文存在哪张表、哪一行、作什么用；任一字段为空都会让密文在同样留空的位置也能打开，属于调用方的错误，`Seal` 和 `Open` 遇到时 panic。`Seal` 唯一可能的失败是这个 panic，因此不返回错误。密钥环加载后只读，可并发使用；不支持热加载。

`Sealed` 包含 `Ciphertext`（含 16 字节认证标签）、`Nonce`（12 字节）、`WrappedDEK`（12 字节包裹 nonce + 32 字节加密数据密钥 + 16 字节标签）、`KeyVersion`（uint32）。两层 nonce 均由标准库随机生成。位置编码为固定域标识 `axiom/secret/location/v1`、一个零字节，再依次为 `Table`、`RowID`、`Purpose` 各写 4 字节大端字节长度和字段的原始字节；长度前缀保证不同位置编码不同（如 `{"ab","c"}` 与 `{"a","bc"}`）。数据层以位置编码为 AAD；包裹层认证固定域标识 `axiom/secret/dek/v1`、一个零字节、4 字节大端版本号和位置编码：加载器只能查出同一文件里的重复密钥，若有人在后来的文件中把旧密钥换个版本号重新登记，改了 `KeyVersion` 的记录仍然打不开，轮换进度不会被误报。版本不绑定到数据层，后续轮换只需重新包裹数据密钥。

调用方读取时从预期的记录位置重新构造 `Location`，不能使用随密文一起存的位置。整条记录移到另一张表、另一行或另一用途，替换任何密文字段，都必须认证失败。比起一把主密钥直接加密所有凭证，独立数据密钥允许轮换时保留数据密文；比起自行管理随机 nonce，采用标准库 `NewGCMWithRandomNonce` 避免调用方复用 nonce。随机 nonce 的上限按密钥材料计，不按版本计：同一把主密钥在所有版本、进程、节点和重启中合计最多包裹 2³² 个数据密钥，重新包裹也算在内；部署应在达到之前轮换。轮换必须新生成一把独立的随机密钥，不能沿用旧密钥只改版本号。包不持久化计数。每个数据密钥只加密一次，数据层没有这个上限。

`axiom keyring add|activate|rewrap|retire|check` 管理主密钥，操作顺序与停写要求见[运维步骤](../../docs/ops/backup-restore.md#主密钥轮换)。`add --version N` 在本机文件原子加入随机密钥并登记为可读，不改变写版本。`activate --version N` 只改数据库写版本；必须先人工分发并检查全部节点，M3 #322 才做自动分发。`retire --version N` 拒绝当前写版本及仍被信封引用的版本，保留数据库墓碑防止重用版本或材料，提交后移除本机文件行，失败可重试。文件与 PostgreSQL 不能同事务：增加先持久化文件再登记，退役先提交数据库再删文件，崩溃只可能多留一把密钥；重试恢复。文件目录需可写，替换保留文件权限，Unix 同步文件和目录；不能直接替换只读单文件挂载，应挂载密钥目录。

`secret.Rotation` 从 `cmd/axiom` 接收平台范围及信封列清单（账户 TOTP、供应商登录和凭证、支付商户配置）。SQL 只通过 `pg.Tx`，AAD 从清单和行主键重新构造；新增信封存储必须登记清单。`Rewrap` 认证原信封后只重包裹 DEK，保留数据密文与 nonce。每批锁行，修改和计数审计同事务；只挑非当前版本行，提交的版本号就是检查点，中断重跑跳过已完成行，无需另外存游标。一个运行内按主键推进内存游标；完成前在生命周期锁内从头核验一次，避免其他轮换命令使游标之前重新出现旧版本，总扫描量仍保持线性。串行轮换操作用事务 advisory lock，数据库写入版本切换不会穿过一个批次；不使用 `SKIP LOCKED`，避免把被锁的剩余行误报为完成。每批有期限，批大小 1–1000；命令支持限制批次数便于分次维护。

`serve` 在启动后台任务和监听前运行与 `check` 相同的校验：当前写版本、全部可读版本及数据库引用版本都必须在本机存在且摘要一致；缺失或错误只报告版本号，不报告路径、摘要或材料。密钥环不热加载：M2 激活、重包裹和退役期间须停掉所有旧写进程，完成后重新启动；命令的锁不约束旧进程的 `Seal`，不能把它当作在线多节点切换。相比在每次加密时读数据库，这保持了原有加密 API 与调用路径，代价是维护窗口。

`Secret` 的所有输出方法都使用值接收者，嵌入结构体或通过指针输出也脱敏。明文保存在闭包中：`fmt` 的错误动词诊断会绕过格式化方法并递归输出私有字段，普通私有字符串字段仍会泄露；闭包不会被展开。`%T`、`%p` 和非法格式（如 `Sprintf` 的 `%w`）由 `fmt` 自行处理，输出类型、地址或诊断信息；交给 `Format` 的所有动词均输出固定脱敏值。它防止意外输出，不保证进程内存擦除，也不能保护已通过 `Reveal()` 取出的普通字符串。闭包也让 `Secret` 有意不可比较：不能做 map 的键，包含它的结构体不能用 `==`，非零值的 `reflect.DeepEqual` 为 false；比较时用 `Reveal()`，go-cmp 需要 `cmp.Comparer`。

## 出站 HTTP

`egress.New(egress.Direct)` 创建可并发复用的客户端，关闭时调用 `CloseIdleConnections`。出口用独立的 `Exit` 类型指定，目前只接受直连；外部代理和出口注册随实际接入加入，不提供能绕过地址校验的自定义拨号接口。

调用方在 context 上设置整次请求的期限（包括响应体），通过 `Do` 发出请求，并关闭响应体。连接阶段（含 DNS 和所有候选 IP）最多 10 秒，每个候选 IP 平分剩余时间，前一个不通不会耗尽后面的时间；TLS 握手 10 秒；空闲连接 90 秒。等待响应头只受请求期限约束：非流式的模型调用要等生成完才返回响应头，可能要几分钟。不设统一的整体请求超时，以免截断合法的长流。响应头合计最多 1 MiB，上游网址可由站长设置，默认的 10 MB 太宽；HTTP/2 按每个字段另加 32 字节计算。Axiom 不做协议升级：上游回 `101 Switching Protocols` 时 `Do` 关闭连接并报错，因为 101 之后响应体就是裸连接，请求期限不再关闭它，静默的上游能让读取一直阻塞。非流式读取使用 `ReadBody(body, maxBytes)`，超过调用方给出的上限时报错，不返回截断内容；该函数不关闭 body。流式调用方自行限制读取和转发的字节数。

地址策略结合 `net/netip` 的私网、回环、链路本地、非全局单播判断和 `egress/address.go` 的特殊用途前缀表。表引用 IANA IPv4/IPv6 Special-Purpose Address Registry 和对应 RFC；特殊用途聚合段整体拒绝（包括其中的公网例外），IPv6 只允许当前的全局单播 `2000::/3`，另排除特殊用途段。URL 里写成 IPv4 映射 IPv6 的地址直接拒绝；解析结果里的映射地址先还原成 IPv4 再按 IPv4 规则校验，因为 Go 的纯 Go 解析器把 `/etc/hosts` 的 IPv4 条目返回成映射形式。十六进制、八进制、短 IPv4 和整数写法不交给操作系统猜测，直接拒绝；IPv6 zone 也不允许。IANA 分配变动时需要复核前缀表；IPv6 没有广播地址，CGNAT 是 IPv4 专有的地址段。

云元数据地址：`169.254.169.254`（链路本地）、`fd00:ec2::254`（ULA）、`100.100.100.200`（CGNAT）、`192.0.0.192`（`192.0.0.0/24`）由上面的规则覆盖；Azure 的 `168.63.129.16` 在公网段，单独列进前缀表。选定云厂商时复核这份清单。

NAT64：只覆盖不在 `2000::/3` 内的转换前缀，即知名前缀 `64:ff9b::/96`（RFC 6052）和本地前缀 `64:ff9b:1::/48`（RFC 8215）。在用这两个前缀的 DNS64 纯 IPv6 网络里，合成的 AAAA 地址整组被拒，只有 IPv4 的上游因此不可达，这是失败关闭。RFC 6052 §3.4 还允许网络自选 `2000::/3` 内的转换前缀，这类地址看起来是普通公网 IPv6，本包认不出其中嵌入的 IPv4：URL 里写 `前缀 + 10.0.0.1` 或只返回这种 AAAA 的域名都能通过校验，经网关到达内网。

部署前提：出站网络不能有转换到私网、回环或元数据地址的网络自选 NAT64 前缀。哪个部署需要这种网络，再由可信的部署配置给出前缀，整段拒绝或还原出 IPv4 后再校验，并补 URL 字面量和 AAAA 两条路径的测试。

测试使用 `egress.NewForTesting(egresstest.Loopback(t))` 请求本机服务器；TLS 假网关使用 `egresstest.Loopback(t, server.Certificate())` 额外信任该测试证书，仍校验主机名和证书链。信任根只能由测试进程构造，生产的 `egress.New` 不提供注入入口；不能借此放行 RFC 1918、ULA 或云元数据地址。

## 监听

| 监听 | 提供什么 |
|---|---|
| 公网 `AXIOM_PUBLIC_ADDR` | 官网（`/`）、控制台页面（`/console/`）、控制台接口（`/api/console/v1/…`，23 个操作都有处理器，见「接口约定」的守卫）；模型调用接口（`/v1/`、`/api/v1/`，`cmd/axiom` 的 `withModelCalls`）；支付回调（GET/POST `/api/payments/v1/notify/{merchant}`，渠道签名鉴权，独立于控制台会话） |
| 管理 `AXIOM_ADMIN_ADDR` | 管理后台页面和管理接口（`/api/admin/v1/…`，守卫只放行管理域名）；默认只听本机（`127.0.0.1:8082`），容器里由 compose 改成 `:8082` 并只映射到本机。管理接口的 45 个操作都有处理器（见「接口约定」）；定价试算（`previewPricing`）在 M1 只给售价，利润要每端点的成本，随它一起加入。部署要让它只对管理域名和受限来源开放（总体架构），监听本身不做来源限制 |
| 探针 `AXIOM_PROBES_ADDR` | 健康检查；`/metrics` |

`serve` 读的变量（未知的 `AXIOM_*` 变量算错，值不进错误）：`AXIOM_PUBLIC_ADDR`、`AXIOM_ADMIN_ADDR`、`AXIOM_PROBES_ADDR`、`AXIOM_STARTUP_TIMEOUT`、`AXIOM_SHUTDOWN_TIMEOUT`、`AXIOM_DATABASE_URL`、`AXIOM_DB_COMPLETION_RESERVE`（完成类预留的连接数，默认 16，见「连接池」）、`AXIOM_MASTER_KEY_FILE`、`AXIOM_KEY_HASH_FILE`、`AXIOM_IDENTITY_KEY_FILE`（见「凭证加密」），以及：`AXIOM_TRUSTED_PROXIES`（逗号分隔的代理地址或网段，只有来自它们的连接才读 `X-Forwarded-For`，从右往左取第一个不受信的地址；默认不信任任何代理。地址比较前已 `Unmap`，所以 v4-mapped 写法 `::ffff:10.0.0.0/104` 按它的 IPv4 网段 `10.0.0.0/8` 读，短于 /96 的 v4-mapped 网段还覆盖没映射的部分，启动时报错）、`AXIOM_HASH_CONCURRENCY`（同时算几个 argon2id，默认 `GOMAXPROCS`）、`AXIOM_HASH_WAIT`（排队多久返回忙，默认 2 秒）、`AXIOM_VALKEY_URL`（不设就是单核心模式，共享状态都在进程内）、`AXIOM_VALKEY_PASSWORD_FILE`（受保护文件，权限要求同主密钥文件）、`AXIOM_CORE_NODES`（核心节点数，默认 1；现在大于 1 不启动）、`AXIOM_NODE_ID`（本节点的名字，默认主机名，心跳行用它，重启的同一节点替换自己的行），见 [kv 的「配置与启动」](kv/README.md#配置与启动)。命令（`catalog`、`site`、`user`）在节点自己的容器里运行时环境里有这些变量，所以它们只核对自己读的变量的值（`Env.Problems`），不把别的变量报成未知。

公网和管理监听的每个请求都经过 `httpserver.Public`：
- **请求 ID**：自己生成 UUIDv7，放在 `X-Request-ID` 响应头和请求的 context 里（`httpserver.RequestID`）；不采用客户端传来的，免得伪造或冲突。
- **请求与尝试的编号**：`platform.RequestID` 是请求 ID 的类型，各模块的表都用它记同一个调用；`platform.AttemptSeq` 从 1 起给一个请求的尝试编号，上限 `MaxAttempts`（3，含结果未知的尝试）。放在平台是因为 billing、suppliers、observability 都要用，而它们之间不互相依赖。
- **访问日志**：每个请求一行，记方法、路由模式、状态、字节数、耗时和请求 ID；记路由模式而不是路径，路径里可能有 ID 或密钥。
- **请求体限时**：带请求体的请求要在 2 分钟内发完（`cmd/axiom` 的 `bodyTimeout`），否则声明了请求体却不发送的客户端能一直占住连接。请求体读完后 net/http 自己清掉这个期限，长时间的流式响应不受影响，有测试守着；不带请求体的请求不设。
- **panic**：还没发出响应时返回 500（带请求 ID）；响应已经开始时（写过内容，或只刷新过响应头）中断连接，让客户端看到响应不完整，而不是一个状态码错误却"完整"的响应。被中断的请求也记访问日志。
- 包装响应时实现 `FlushError` 和 `Unwrap`，流式处理器通过 `http.ResponseController` 刷新和设置期限，刷新也算响应已开始。
- 生成的接口代码的每条出错路径都在 `cmd/axiom` 里设定，不留默认值：请求本身不合法时返回 400，说明只涉及客户端自己发来的内容；处理器出错时记日志、只返回 500，不把内部错误原文发给客户端。

## 前端文件

`webui` 提供 `web/` 构建出的官网、控制台和管理后台，文件由 `go:embed` 读入，启动时全部载入内存。

| 规则 | 做法 | 为什么 |
|---|---|---|
| 嵌入的一定是本次的构建 | `just web-embed`（`web/scripts/embed.ts`）把三个 `dist/` 复制到 `webui/build/`；`just up`、`just test`、`just web` 都依赖它。`build/` 除一份说明外不入库。没有构建时 `go build` 照常通过，程序启动报「前端没有嵌入，先运行 `just web-embed`」 | 嵌入目录缺失时 `go:embed` 的报错不说怎么办；放在启动时是本地能查的事不拖到运行时 |
| 带哈希的资源长期缓存 | `assets/` 下名字以 8 位哈希结尾的文件：`public, max-age=31536000, immutable`。其余一律 `no-store`，包括入口页和所有 404 | 名字随内容变，永远不会拿到别的内容；入口页是发布时唯一要立刻换掉的文件 |
| 静态文件预压缩 | 构建时给文本文件（html、js、css、svg、json、txt）各写一份 brotli（q11）和 gzip（9），比原文小才写；服务器按 `Accept-Encoding` 的 q 值选，相同时 br 优先，都不接受或 `q=0` 发原文，一律带 `Vary: Accept-Encoding`。压缩函数只有一个（`web/scripts/compress.ts`），体积预算也用它 | 不在请求路径上压缩，不占 CPU；预算量的就是发出去的 |
| 不压缩流式响应 | 压缩只有两处：上面的预压缩，和 `httpserver.Compress`，后者只在控制台接口上，只压 `application/json`、至少 1 KiB、客户端接受 gzip 的响应。其他类型（`text/event-stream`、NDJSON 等）原样通过；带 `ETag` 的、已编码的、被刷新过的也原样。模型调用接口不挂它 | 压缩器的缓冲会让流式输出攒着不发；按类型白名单排除，新的流式类型默认不被压 |
| 单页应用回退 | 只对 GET/HEAD、路径不带扩展名、`Accept` 含 `text/html` 的未知路径回到该应用的入口页。`/api/`、`/v1/` 下的一律 404，扩展名不为空的未知路径（缺失的脚本、过期的代码块）也是 404 | 回退成 200 的 HTML 会把过期的代码块伪装成脚本；接口调用方要的是 404，不是页面 |
| 公网监听不含管理后台 | 公网的处理器只持有官网和控制台的文件；管理后台的处理器只交给管理监听 | 结构上不可达，而不是靠路径判断 |
| 安全响应头 | `Content-Security-Policy`（`vite.shared.ts` 里唯一的一份，由 `embed.ts` 写进 `build/content-security-policy.txt`，Go 原样发送）、`X-Content-Type-Options: nosniff`、`X-Frame-Options: DENY`、`Referrer-Policy: strict-origin-when-cross-origin`、`Permissions-Policy`（关摄像头、麦克风、定位）。每个应答都带，错误也带 | 策略只在一处定义，两边不会漂移；HSTS 由终结 TLS 的入口节点发 |
| 不支持范围请求 | 整个文件带 `Content-Length` 发送 | 脚本、样式和字体没有人要一段；`http.ServeContent` 对已编码的响应不写长度 |
| 滚动发布时 | 旧页面请求新版本已经没有的代码块得到 404，前端自动刷新一次（`web/README.md`）；一台机器上的页面和资源总来自同一个构建 | 多台机器新旧并存时，页面和它的代码块可能落在不同版本上，靠这一次刷新恢复；要彻底避免，由入口节点按会话粘到同一台 |

模型与价格页由 Go 在返回页面时写入数据的位置留在 `webui` 的应用里：以后在回退到入口页的地方改成渲染，不改路由。

## 接口约定

控制台接口和管理接口的契约在 `api/` 里，每个监听一份、自成一体：`console.yaml` 在公网监听的 `/api/console/v1`，`admin.yaml` 在管理监听的 `/api/admin/v1`。运营接口以后另加一份。`just generate` 生成 Go 端的 `consoleapi`、`adminapi` 和 `web/packages/api/src` 里的 `console.gen.ts`、`admin.gen.ts`；`just api`（`tools/checkapi`）用 kin-openapi 校验契约，并检查下表中标「检查」的约定。每个操作的权限、限次、幂等和 CSRF 写在契约里，不在这里重复。

| 约定 | 内容 | 检查 |
|---|---|---|
| 路径与版本 | 路径以 `/api/<文件名>/v1/` 开头。加操作、加字段、加可选参数向后兼容，不升版本；改变含义或删除才升 v2 | 检查前缀 |
| 安全声明 | 每个操作显式写 `security`：`[]` 是公开；要会话的写 `session`，改数据的再加 `csrf`。`x-axiom-level` 写会话要证明到哪一步（`any`、`pending_mfa`、`signed_in`、`mfa`），`x-axiom-permission` 写在该会话的范围上需要的权限名（不需要写 `none`）。管理接口里 Session 标签以外的操作都作用于平台，所以都要 `mfa` | 检查 |
| 改数据的请求 | 带站点登记的 `Origin`、`Content-Type: application/json`，有会话的再带 `X-CSRF-Token`；不满足返回 403 `origin_rejected`。接口不开放 CORS | 改数据的操作必须有 403 |
| 错误体 | 所有失败都是 `{"error": {"code", "message", "fields"}}`，`code` 取自封闭的枚举 `ErrorCode`，前端按它翻译，`message` 是写给日志和开发者的英文；请求 ID 在 `X-Request-ID` 头里。`default` 响应和每个非 2xx 响应都用这个体 | 检查 |
| 分页 | 键集游标：`cursor`（不透明，原样传回，只对同一组筛选有效）、`limit`（默认 50，最大 200），响应 `{data, next_cursor}`，最后一页的 `next_cursor` 是 null。游标里放什么由实现定，只要沿用所在查询的排序键（如调用记录的 `(at, id)`），不用 OFFSET | 返回 `next_cursor` 的操作必须带这两个参数 |
| 金额 | `Money`：`amount` 是有符号整数的十进制文本，单位是币种的十亿分之一，`currency` 是站点的记账币种。写成文本是因为 JSON 数字在浏览器里超过 2⁵³（约 900 万个单位）就失真，而账户余额、站长的总额会超过 | — |
| 比率与十进制数 | 倍率、单位价值、余额的原生数量都是十进制文本（`Decimal`），单价是 `UnitPrice`（至多 15 位小数），不经过浮点数 | — |
| ID | 账户模块的实体用 TypeID（`user_…`、`acct_…`、`key_…`、`ws_…`、`site_…`、`tier_…`）；调用编号、目录、供应商、定价的行用 UUID 文本。路径里格式不对和不存在一样返回 404 | 模式写在契约里 |
| 缺省与空 | 响应里没有的值就不出现，不写 null（`next_cursor` 是唯一的例外）；`PUT` 整体替换，请求里没写的可选项取「没有限制」或默认值，所以页面把读到的设置连同修改一起送回 | — |
| 管理接口里 Session 标签以外的写操作 | 带 `Idempotency-Key`（调用方为一次想做的改动选的 UUID；重发返回第一次的结果并带 `Idempotency-Replayed: true`，被别的操作者、别的操作或别的内容用过是 409 `idempotency_key_reused`，存法见「幂等」）和写进审计的 `reason`；读到的 `rev` 随改动送回，已变化返回 409 `stale_revision`。不改数据却要请求体的 POST 标 `x-axiom-safe: true`，不带前两项 | 契约里有 `IdempotencyKey` 参数时检查 |
| 没有处理器的操作 | 返回 501 `not_implemented`。契约一加操作，生成的 `StrictServerInterface` 就多一个方法；`tools/pendingapi` 生成嵌入的占位类型，让程序在处理器一个个写出来之前照常编译，处理器声明了同名方法就接管，重新生成后那个桩消失，全部接管后连同 `//go:generate` 和嵌入一起删掉。控制台的 23 个操作都有处理器，那一套已经删掉；管理接口的 41 个也都有处理器（定价试算补上最后一条），那一套同样已经删掉；下一个契约再加回来 | `just generated` |
| 两份契约共有的部分 | 会话与两步验证的操作和 `Error`、`Money`、`Balance` 等模式在两份契约里各写一遍，不引用外部文件（生成器要为外部引用另做一个包，每份契约才不自成一体）。两份里同名的操作和定义必须完全相同 | 检查 |
| 契约的约束谁来强制 | **定论**：`cmd/axiom` 的守卫（`guard`）在处理器之前，按嵌入程序的契约本身强制；契约由 oapi-codegen 的 `embedded-spec` 编进生成的 `consoleapi`（`GetSpec`，随 `just generate` 更新，`just generated` 保证不过期），启动时用 kin-openapi 读出每个操作的规则，读不通或有操作的声明程序强制不了（没有 `security`、会话操作缺 `x-axiom-level`、权限名没在 accounts 声明、改数据的会话操作没有 `csrf`）就不启动。守卫在生成的路由之前运行（生成的包装先解析查询参数、后跑中间件，守卫放在中间件里就挡不住"无会话却先得到 400"），整个请求在控制台有 15 秒的期限（`consoleDeadline`），在管理接口有一分钟（`adminDeadline`），到期是 503 `unavailable`，说明工作可能已经做完，让客户端先看再重试。守卫的顺序：站点（Host 是某站点的控制台域名，否则 404）→ 改数据的请求的 Origin 与 JSON → 会话与它的级别、CSRF 令牌（`verifyMfa` 的待验证会话和令牌也先在这里认出，用户行锁内的复核仍在 accounts）→ 账户上的权限 → 请求体先过 `jsonx` 的策略（UTF-8、成员名不重复、嵌套不超过 256 层，生成代码和校验器用的解析器不执行它，所以在守卫里读一遍）→ 查询参数与请求体的 schema。生成的处理器读请求体也用 `jsonx.Decode`（名字区分大小写，未知成员跳过），与校验器按同一条规则读成员名；用 encoding/json 读，`MODELS` 会被校验器当作未知成员放行、又被读进 `models`，绕过 schema 的上限。生成代码走的是 `consoleapi/templates/strict-http.tmpl`，即 oapi-codegen v2.8.0 的同名模板改了解码那一行，升级 oapi-codegen 时按新模板重做这一处。生成的包装自己的解析失败只答"请求读不了"，不重复解析器看到的内容。所以没有会话的客户端只会知道它没有会话；校验失败是 400 `invalid_request`，`fields` 逐条写出出错的位置（请求体的 JSON 指针，或查询参数名）和规则，不写值。路径里的 ID 不在此校验：格式不对与不存在同样是 404。契约表达不了的（跨成员的规则，如 `limit_reset` 要有 `spend_limit`；表的约束，如地址能解析）由处理器的输入检查补上，同样写成 400 和 `fields` | — |

契约用到的权限名，处理器的 PR 在 `accounts` 的代码和迁移里声明（权限在代码里声明名字和是否敏感，角色快照里有代码不认识的权限就不启动）：

| 面 | 权限 | 敏感 |
|---|---|---|
| 控制台 | `api_keys.view`、`api_keys.manage`（已有）；`calls.view`、`balance.view`（也守充值的五个操作，见[支付的决策表](../payments/README.md#决策与理由)） | 否 |
| 管理 | `sites.manage`（已有）、`credits.grant`、`suppliers.manage`、`catalog.manage`、`pricing.manage` | 是 |
| 管理 | `users.view`、`suppliers.view`、`catalog.view`、`pricing.view` | 否 |

控制台的处理器已经补上它要的读写：`accounts` 的 key 列表、读取、整体替换设置、创建时带设置、会话信息、接受待验证会话的认证和 `AccountWorkspaces`；`billing` 的 `ReadBalance`（余额、在途预留、底线、本月花费）和 `KeysUsed`（key 在自己重置窗口里已用的）；`observability` 的游标编码与解码。管理接口的处理器补上了：`accounts` 的站点设置读写、等级、用户和账户的读取、八个管理权限（见 accounts 的「管理接口」）；`billing` 的 `ReadCredit`（赠送批次）；平台的幂等键（`idem`）；`catalog` 的模型和参考价的读取、定价规则；`suppliers` 的管理操作与待映射队列；`billing` 的定价试算（`previewPricing`，经 `billing.SalePrices` 算出售价，`cost` 与 `margin_ratio` 要每端点的成本，随 M2 的路由端点一起加入）。

M1 的契约没有的，以后加（向后兼容）：试运行（`dry_run`）、审核项、声明式适配器、调用详情里的尝试与路由、额度批次与来源、预设、用量统计、站长的站点管理。

## 遥测

指标和链路追踪的基座（[Issue #54](https://github.com/TYCT-tyct/Axiom/issues/54)）：OpenTelemetry 的 API 与 SDK，指标经 Prometheus 导出器暴露。`telemetry.New` 在 `serve` 里建出提供者，`cmd/axiom` 把它们作为参数交给 `pg.Open`（`pg.WithTracerProvider`）、`egress`（`egress.WithTracerProvider`）、`httpserver.NewTelemetry` 和各模块；**不用 OpenTelemetry 的全局**，并行的测试各自持有自己的，互不影响。

**决策与理由**（2026-10-16）：

- **选 OpenTelemetry 的 API 与 SDK 做指标和链路，指标用 Prometheus 导出器暴露，抓取式。** 各模块只依赖 `metric.Meter` 一套接口，换导出方式（以后推到收集器）不用改任何模块；指标和链路同属一个系列，请求的上下文把二者连起来；Prometheus 文本格式是运维节点上监控系统通用的读法。
- **不选 `prometheus/client_golang` 直接记指标、链路另用 OpenTelemetry**：模块要认两套接口，以后改成推送要改遍所有模块；性能没有优势（两者记一次测量都是零分配，见下文「调用路径」）。client_golang 仍是依赖，但只做导出器的底层和 `/metrics` 的处理器。
- **不选现在就用 OTLP 推送指标和链路**：要先有收集器，部署还没有；OTLP/HTTP 导出器会带进 grpc、grpc-gateway、`go.opentelemetry.io/proto/otlp`、genproto 一批依赖（`otlptracehttp` v1.47.0 的 go.mod，2026-10-16 查），而现在 `go.mod` 里只有 protobuf。抓取不需要这些，等有收集器的部署再加，只是 `telemetry.New` 的一个处理器参数。
- **不选厂商的 SDK 或托管服务（Datadog、New Relic 等）**：端点在境外，国内的节点不一定连得上，调用元数据也会离开部署；换厂商要改调用代码。指标靠本机抓取，将来的链路只发到自己部署的收集器，都不依赖境外的服务。
- **产品基线**：OpenRouter 不给用户暴露指标端点；它的 Broadcast 把每次调用的链路用 OTLP/HTTP 发到用户指定的收集器，每个目的地可设 0.0001 到 1 的采样率，默认 1（OpenRouter 文档「Broadcast」，2026-10-16）。这是面向用户的功能，属于调用记录的导出（见[记录与告警](../../docs/domains/observability.md)）；本节是运维用的节点遥测，选 OTLP/HTTP 与它的做法一致，便于以后接同一类收集器。

- **指标只在探针监听 `/metrics` 上**，公网监听没有这个路径（测试：公网 404，探针 200，`text/plain`）。指标放在专用的注册表里，只有经 `Metrics` 注册的才会出现，不带 Go 运行时和进程的默认集合（运行时指标随第一个需要它们的告警加入）。
- **命名**：`axiom.<模块或包>.<对象>.<量>`，单位写进 `WithUnit`；Prometheus 里点变下划线，计数加 `_total`，秒加 `_seconds`。现有的指标：

  | 指标（Prometheus 名） | 类型 | 标签 |
  |---|---|---|
  | `axiom_http_server_requests_total` | 计数 | `route`、`status_class`、`outcome` |
  | `axiom_http_server_duration_seconds` | 直方图 | 同上 |
  | `axiom_accounts_key_cache_misses_total` | 计数 | 无：key 查找没命中缓存、去问了数据库的次数（#23；缓存命中时调用路径不查库，用它和请求数对照；测试：`TestKeyCacheMissesAreCounted` 经 `telemetry.New` 的处理器读到这个名字） |
  | `axiom_observability_records_written_total`、`…_records_lost_total`、`…_attempts_written_total`、`…_attempts_lost_total`、`…_write_retries_total` | 计数 | 无 |
  | `axiom_gateway_rate_limited_total` | 计数 | `reason`：节点回 429 的次数，按拒绝的那道限制（`ip_in_flight`、`account_in_flight`、`key_rate`、`count_rate`、`account_gate`、`queue_full`、`queue_key_full`、`queue_wait`、`credentials_busy`，含义见 [gateway 的「并发、背压与共享状态」](../gateway/README.md#并发背压与共享状态)；#295；测试：`TestEachRateLimitIsCountedByItsReason` 等） |
  | `axiom_pg_queue_discarded_jobs_total` | 计数 | 任务类型：队列报告的被丢弃的任务（`pg.Queue`） |
  | `axiom_pg_queue_oldest_job_wait_seconds` | 仪表 | `queue`：每个队列最老的可用任务已经等了多久，没有在等的为 0（`pg.Queue`；它上升说明这个队列的 worker 跟不上；采集的数据库访问有 2 秒期限，超时这次采集报错、不报数（不伪报 0）。测试：`TestTheOldestWaitingJobOfEachQueueIsMeasured` 经 `telemetry.New` 的处理器读到这个名字和四个队列；`TestACollectionEndsWhileTheDatabaseBlocksAndRecovers` 锁住任务表，采集仍会结束，锁放开后恢复） |
  | `axiom_observability_queue_depth` | 仪表 | 无 |
| `axiom_gateway_outage_refusals_total` | 计数 | `dependency`（`database`、`accounts`、`catalog`、`routing`）：因依赖故障被拒的请求数，日志只在故障开始和结束时各记一条（#294；测试：`TestAnOutageIsLoggedOnceNotPerRequest` 经 `telemetry.New` 的处理器读到这个名字） |
  | `axiom_billing_platform_borne_total`、`axiom_billing_platform_borne_amount_total`、`axiom_billing_reconcile_problems_total` | 计数 | 无：账户的余额底线没让它付清的结算次数和平台承担的金额（十亿分之一额度），账本每日自平找到的问题数（测试：`TestThePlatformBearingASettlementIsReported`、`TestTheReconciliationReportsWhatItRefuses` 经 `telemetry.New` 的处理器读到这些名字） |
  | `axiom_catalog_sync_last_success_age_seconds` | 仪表 | 无：持久化记录距最后成功的秒数；从未成功则距首次失败，尚未执行为 0；每次采集读数据库时钟，即使没有下一轮也增长，运维按后台阈值判断；2 秒期限，读库失败返回采集错误、不伪报 0（测试：`TestTheSyncStateIsMeasuredWithoutAnotherRound`） |
  | `axiom_catalog_sync_consecutive_failures` | 仪表 | 无：持久化连续失败轮次数，成功清零；正常取数失败写状态后任务完成（`MaxAttempts=1`），不因同一任务的短退避重试重复计数（测试同上） |
  | `axiom_catalog_sync_overdue_total` | 计数 | 无：失败轮次超过节点代码默认阈值（48 小时）时的兜底计数，每次执行最多加一；日志为 `catalog.sync.overdue`，运维后台阈值判断使用上述 gauge（测试：`TestTheSyncAlertsWhenThePricesGoUnrefreshed`） |

  新指标加进这张表，并在同一个 PR 里加测试读到它的名字。
- **标签**：只用有界的枚举，**每个指标的标签组合不超过 1000 个**（靠评审核对，暂无自动检查）。公网请求用路由模式（注册的模式，没匹配上的合成一个 `unmatched`）和状态类别（`2xx`、…，连接在发出状态前就断了是 `none`）和结果（`completed`、`aborted`：响应开始后被中止的，状态仍是已发出的那个，但算 `aborted`，跨度标为错误）；方法单独不是标签，路径、查询、请求头、请求 ID、账户、key、用户都不是。按账户的统计从调用记录里算。测试：带标识符的路径请求之后，指标文本里没有这个标识符，`unmatched` 只有一条序列。
- **链路**：每个公网请求一个服务端跨度，名字是路由模式，属性 `request_id`（与 `X-Request-ID` 相同）、状态码；`pg` 的事务（`db.tx`）和 `egress` 的出站请求（`egress.request`，到响应头为止）是它的子跨度，同一个 trace。跨度属性只有主机名、状态码、`db.system` 这类有界的值：不记语句（可能含数据）、不记错误文本（可能引用地址或数据）、不记路径和查询。测试：请求路径、查询和语句里放入标记值，没有任何跨度属性含它；并核对父子关系和请求 ID。
- **调用路径**：标签组合初始化后，稳态下记一次测量不分配内存，指标就不会让调用路径的垃圾回收随流量增长；一个组合第一次出现时要建属性和选项切片并复制缓存的映射，是一次性的开销，组合数有上限，基准重复同一个组合，量不到它。`BenchmarkRecord`（无标签的计数、带标签的计数和直方图，上下文里是被采样的跨度）全部是 0 allocs/op，`bench-compare` 在任何一项开始分配时失败。做到的两条规则：标签集合预先建好、按组合缓存（`httpserver` 的 `seriesCache`，组合数就是标签上限里的那个数，读取是一次原子加载），因为每次调用现建 `metric.WithAttributes` 要分配；选项切片也建一次，用 `Add(ctx, 1, opts...)` 传入，因为逐个传的可变参数会在接口调用处分配一个切片。每个请求的跨度仍有开销：`BenchmarkRequestTelemetry` 在没有处理器时是 3 allocs/op（240 B，跨度的上下文和结束函数），有处理器、跨度被采样时是 9 allocs/op（约 1.5 KiB）；关闭遥测是 0。事务和出站请求的跨度同理，量的是开关一次跨度的开销：`BenchmarkTransactionSpan` 未采样时是 4 allocs/op（176 B），采样时 8（约 1.1 KiB）；`BenchmarkEgressSpan` 未采样时是 4（176 B），采样时 10（约 1.3 KiB）；未采样时不为属性构造参数。
- **采样**：没有处理器时没有人读跨度，`telemetry.New` 不采样（`NeverSample`）：跨度仍在，子跨度照常连在一起，但不记属性、不记事件。公网请求的服务端跨度（`httpserver`）、事务跨度（`pg`）和出站请求跨度（`egress`）因此都只在 `span.IsRecording()` 为真时构造属性：`trace.WithAttributes`、`SetAttributes` 的参数先求值、再由不记录的跨度丢掉，不判断就白建。`BenchmarkRequestTelemetry`、`BenchmarkTransactionSpan`、`BenchmarkEgressSpan` 守住这一步。有处理器（接上了导出器）时用默认的父级决定加全采，下一条定下比例。
- **导出**：
  - **指标**：只抓取，没有推送，不需要配置：监控系统从探针监听（`AXIOM_PROBES_ADDR`，默认只听本机；要跨机抓取就把它绑到内网地址，由部署的防火墙限制来源）取 `/metrics`。
  - **链路**：现在没有导出器，跨度在进程内产生后丢弃，所以没有导出配置可错。第一个有收集器的部署再加：OTLP/HTTP 导出，作为 `telemetry.New` 的一个 `SpanProcessor`，只发到自己部署的收集器；端点和凭证用标准的 `OTEL_EXPORTER_OTLP_*` 变量，采样比例是 `AXIOM_TRACE_SAMPLE_RATIO`（0 到 1，默认 0.1，父级已决定的跟随父级）。**配置错了不启动**：端点不是绝对的 http(s) 地址、比例不在 0 到 1 之间、凭证没配而端点配了，都在 `serve` 解析配置时报错，报错只说变量名，不带值。出错的请求总采要靠收集器的尾部采样，节点上的比例采样事先不知道请求会不会出错；用哪一种等有收集器时和它的容量一起定。
  - 这条的实现和依赖随那个部署的 PR 评审；现在加，是为一个没有使用者的导出器多带一批依赖和一个没有效果的配置项。
- **不记**对话正文和密钥：指标没有任何自由文本，跨度属性按上面的白名单。

## 健康检查

探针监听 `AXIOM_PROBES_ADDR` 默认只听本机，不对公网：

- `/livez`：进程在运行；
- `/readyz`：能接新工作时 200，开始退出后 503。启动时监听在数据库检查通过后才打开，之前探针连不上，编排工具把连不上和 503 一样算作未就绪；这样启动失败的节点从不占用端口。退出时就绪先改为否，公网监听等在途请求完成期间，探针监听仍然应答（`/readyz` 返回 503），之后才关闭。
- 探针读请求头、读整个请求（含请求体）、写响应各限时 2 秒：探针从不流式响应，不限时的话，声明了请求体却不发送的客户端能一直占住连接。

## 数据库

### 角色

| 角色 | 登录 | 用途 |
|---|---|---|
| `axiom_owner` | 否 | 拥有全部 schema 和对象，只在迁移时进入；没有 BYPASSRLS |
| `axiom_lookup` | 否 | 只拥有登录前查找的定义者函数，见 [accounts 的「登录前的查找」](../accounts/README.md#登录前的查找)；`axiom_app` 能调用这些函数，不能成为这个角色 |
| `axiom_app` | 是 | 运行时唯一的登录角色，`AXIOM_DATABASE_URL`；由部署创建并设密码，密码不经过本程序 |
| 迁移管理员 | 是 | `AXIOM_MIGRATE_DATABASE_URL`，部署里叫 `axiom_migrator`；需要 CREATEROLE 和数据库的 CREATE 权限，不需要超级用户；`axiom_owner`、`axiom_lookup` 由别的角色建好时，还要有它们的 ADMIN 权限。建好 `axiom_owner`、`axiom_lookup` 和各模块的 schema，再切成 `axiom_owner` 执行迁移 |

[安全](../../docs/security.md#租户隔离)角色表里的后台任务角色，由第一个用到它的模块在迁移里建立并授予 `axiom_app`，任务的事务用 `pg.Tx.SetRole` 切过去；`serve` 的启动检查覆盖 `axiom_app` 能成为的全部角色，也就覆盖它。`axiom_owner` 和 `axiom_lookup` 由迁移的第一步以迁移管理员建立；`axiom_owner` 能切换成 `axiom_lookup`（不继承它的权限），因为迁移要用 `ALTER FUNCTION … OWNER TO axiom_lookup` 把函数交给它，这一步要求执行者能切换成新的所有者。

### 连接池

`pg.Open` 管理两个物理池，共用一个连接预算 N：默认 64（`pg.PoolSize`），连接串 `pool_max_conns` 可覆盖，包括调小。`serve` 和打开应用数据库的运维命令用 `AXIOM_DB_COMPLETION_RESERVE` 配置其中只供完成类使用的 R 条，默认 16（`pg.CompletionReserve`）；普通池最多 N−R 条。启动时要求 0 < R < N，连接串的 `pool_min_conns` 和 `pool_min_idle_conns` 不得超过 N−R；预留池的最小连接数为 0，按需建立，不把普通池的最小值复制一遍。错误只报配置名和预算，不带连接串。gateway 的许可数从打开后的 N、R 派生，部署不必为了调小预算改代码。

`pg.DB.Tx` / `ReadTx` 在同一个入口选池：默认使用普通池；gateway 的追加、结算和孤儿结算入口用 `pg.CompletionContext` 标记该次事务，才使用预留池。后台查找租户后执行的 `TenantTx` 是另一个事务入口，固定使用普通池。两池沿用相同的登录角色、事务范围、追踪、回滚和取消规则。控制台、recorder、快照加载、River worker 和 River 自己的数据库操作都走普通池，不需要主动领取 gateway 的许可，也不能借用空闲的预留容量。完成类并发超过 R 时受自身 context 限制排队；预留保证的是连接容量，不保证行锁、数据库停机或结算自身饱和时仍成功。持有者心跳仍走普通池，专用连接见 #217。

- **和 gateway 的许可同一份数**：`serve` 取 `db.MaxConns()` 和 `db.CompletionConns()`，准入和完成类分别限 N−R、R 个许可、各自计数，持有和等待完成许可都不减少准入容量（[gateway 的「并发、背压与共享状态」](../gateway/README.md#并发背压与共享状态)）。预算调小，许可跟着调小，不必改代码。
- **两个池分别定容量**：R 至少按结算、孤儿结算和追加的峰值合计速率乘各自占用连接的时间估算，并为突发和尾延迟留余量；N−R 另按准入、查找、控制台和后台事务估算。若一次完成事务占连接 5–10ms，默认 R=16 的理论吞吐约每秒 1600–3200 次，不能据 N=64 推断能承受每秒 5000 次结算；持续超出会排队、超过前台结算期限后转后台重试或清理接管。5000 路在途不等于每秒 5000 次完成，默认值是否足够由[性能的压测](../../docs/performance.md#怎么验证)验证。
- **一个账户占不满**：单个账户受账户闸和每秒 151–222 个请求的上限约束，约占 2 条。
- **留在默认的 max_connections 以内**：64 加迁移、查找和管理连接仍在 PostgreSQL 默认的 100 以内，单机不用改配置。多个核心节点连同一个库时，按节点数调大 max_connections，或者在连接串里写 `pool_max_conns`。

默认 N=64 保留原有事务连接总预算，R=16 保留既定的完成类保底。选择两个物理池而不是仅在 `Tx` 前加信号量，是因为 River 驱动直接使用 pgxpool，其查询不经 `Tx`；只在 gateway 发许可同样拦不住控制台和 recorder。代价是普通池繁忙时不能借用空闲的预留连接，完成类也不能借用普通池，吞吐受 R 限制。new-api `model/main.go`（206e0fa，2026-10-20）和 sub2api `backend/internal/repository/db_pool.go`、`backend/internal/config/config.go`（8f2cbb5，2026-10-19）都配置普通池上限；Axiom 另分预留池以履行结算容量保证。

部署按**所有进程**计预算：每个 `serve` 节点另有两条不在池里的连接，都从池里取走后 `Hijack`，不计入 pgxpool 的上限——持有节点名的 advisory lock 的那条（`pg.DB.Hold`，见 [kv 的「配置与启动」](kv/README.md#配置与启动)），以及 River v0.49.0 的 LISTEN 监听器；其余 River 查询仍计入 N−R。因此每个 `serve` 节点按 N+2 计算，再为迁移、运维以及部署中其他连接留余量。例如两个节点各 N=40、R=10，事务池、节点名和 River 监听合计最多 84 条，余量留给 PostgreSQL 的管理和其他连接。不能只把单进程默认 64 与数据库的 `max_connections` 比较。

集成测试 `TestBackgroundAndConsoleCannotTakeTheSettlementReserve` 用真实 PostgreSQL、River worker、recorder 和普通控制台事务入口占满 N−R 条，通过事务回调、通道和数据库锁等待确认饱和；普通事务无法借用预留，结算和孤儿结算仍在各自期限内完成。没有用睡眠推断饱和。

### 范围

- 设定在 `axiom.scope_platform`、`axiom.scope_sites`、`axiom.scope_accounts`、`axiom.scope_home` 四个事务级参数里，由 `platform.in_scope(site_id, account_id)` 和 `platform.sees_site(site_id)` 读取。`scope_home` 是账户范围所属的站点，只给「站点的公开配置」和「共享计数」（[安全的「租户隔离」](../../docs/security.md#租户隔离)）用：`sees_site` 是 `in_scope(site, NULL)` 加上这个站点，不放宽账户读写的任何其他行。它由 `pg.AccountScopeIn(站点, 账户)` 设定，站点来自认出这个账户的授权，而不是账户自己能写的行（例如它在 `platform.tenants` 里的登记）。
- 表强制启用行级安全（`FORCE`，所有者也受限）。按[安全](../../docs/security.md#租户隔离)的数据分类选策略：账户和站点数据读写都用 `platform.in_scope`；站点的公开配置和共享计数读用 `platform.sees_site(site_id)`；公开数据读用 `platform.in_any_scope()`（设了任意非空范围），写用 `platform.in_platform_scope()`；平台数据读写都用 `platform.in_platform_scope()`。
- 平台范围由 `pg.PlatformScope()` 构造，只有 `cmd/` 能调用，再交给需要它的模块。一个站点、一个账户的范围由 `pg.SiteScope`、`pg.AccountScope`（带所属站点时用 `pg.AccountScopeIn`）构造，只有 `internal/accounts` 能调用，在授权通过后交出（见 [accounts 的「授权与租户范围」](../accounts/README.md#授权与租户范围)）。各模块的测试用 `pg.NewScope` 构造任意范围。
- 站点数据的表把 `site_id`、账户数据的表把 `site_id` 和 `account_id` 直接存在行上，都不为空，策略只看本行的列；怎么保持它们一致见 [accounts 的「表」](../accounts/README.md#表schema-accounts)。

### 迁移

- 每个模块的迁移在自己目录的 `migrations/` 下，模块把名字和嵌入的迁移文件作为 `pg.Module` 交给 `cmd/axiom`（如 `accounts.Module()`），由它的模块列表 `modules()` 同样传给 `pg.Migrate` 和 `DB.Check`；有迁移的模块都要加进这个列表，否则 `migrate` 不执行它、`serve` 也不检查它。模块名就是它的 schema，迁移开始前由迁移管理员建好、归 `axiom_owner`（`axiom_owner` 没有建 schema 的权限）；版本表是 `platform.goose_<模块>`。
- 执行顺序固定：`platform` 先执行（各模块的策略调用它的函数），其余按[依赖表](../../docs/architecture.md#模块之间的依赖)从上到下，与传入的顺序无关；`pg` 里的模块清单由测试与依赖表核对。不在清单里或重复的模块名，迁移和启动都失败。
- 迁移以 `axiom_owner` 在持有 goose 会话锁的连接上执行，并发的执行互相等待；初始化角色的 advisory 锁、goose 的会话锁和迁移里的每把锁都最多等约 5 秒，不让另一次迁移或长事务把部署和应用的查询堵住。中断后重跑，从记录的版本继续。
- **期限**：每个连接从域名解析到握手结束合计最多等 `AXIOM_MIGRATE_CONNECT_TIMEOUT`（默认 10 秒；一个主机有几个地址也合计，URL 里的 `connect_timeout` 只能让它更短），整次执行最多 `AXIOM_MIGRATE_TIMEOUT`（默认 30 分钟，长 DDL 要的时间）。到期时迁移自己结束它在服务器上的会话：每个连接带着本次执行独有的 `application_name`，期限到后用一个独立的短连接（自己最多 5 秒）对这些会话执行 `pg_terminate_backend`，等它返回才返回。这样结果既不依赖 pgx 在后台发出的取消请求（进程退出可能抢在它前面，语句也可以写成吞掉取消），也不是每条语句各自重新计时的语句超时；按应用名而不是按进程号，是因为进程号可能被别的会话复用。事务回滚，会话级的 goose 锁随会话释放。goose 释放会话锁用的是取消不掉的 context，服务器不应答时解锁会一直等，所以终止之后再给 15 秒宽限，仍未返回就放弃等待、让进程退出。两者都在连接之前校验：必须是正的时长，连接期限必须短于执行期限，拼错的变量名报错。执行期限让一次卡住的迁移不会一直占着 goose 的会话锁，挡住下一次部署。
- 任务队列 River 的表在 schema `river`，由 `migrate` 在建 schema 的同一个连接上、以 `axiom_owner` 迁移（River 自己的迁移，不是 goose 文件），随后把表、序列、函数授给 `axiom_app`，每次执行都重授一遍；它不是模块，不在 `moduleOrder` 里，所以启动检查对表的行级安全审计不包括它（任务行不属于任何租户，也不存租户的数据；要带租户的数据放模块自己的表，见下文），启动检查另核对 River 的迁移版本（缺了就让运行 `axiom migrate`）。
- 数据库连不上时，错误只说明是哪个变量和哪类失败（SQLSTATE、网络原因、握手没在期限内完成），不带用户名、库名和主机：误填在这些位置的密钥不会进日志。脱敏只作用于连接这一步（迁移的连接、结束会话用的连接），迁移工作本身的错误和期限到后的错误原样带上下文传出；终止连接也失败时，两个错误合在一起返回，互不改写。
- 合并后不改：CI 对比基准提交，`migrations/` 下只能新增文件（`just migrations <base>`）。
- **收缩迁移晚一个版本**：列或表只在停止使用它的那个版本发布之后的版本里删除，这样滚动发布期间新旧节点可以同时运行（[CONTRIBUTING 的「发布与缓存」](../../CONTRIBUTING.md#检查清单)）。

### 任务队列

`pg.Queue`（`db.NewQueue(logger, scope, meter)`，scope 是平台范围，只有 cmd/ 能构造）是 River 在应用连接池上的封装，River 的类型只经它出入，模块拿不到连接池或 `pgx.Tx`。队列按任务的性质分成四个具名队列：`money`（额度到期、扣费与账本的对账）、`notifications`（邮件和告警，目前只有 `mail.send`）、`syncs`（供应商同步的扫描和一轮同步、价格数据集的一天一轮）、`archive`（丢弃任务的扫描等清理任务）；每个具名队列有自己的 worker 数（资金 3、通知 2、同步 4、归档 1，四个合计仍是原来的 10 个，是代码里的默认值，不设配置项），所以一批同步或归档任务不会挡住资金任务。队列每分钟扫描 `river.river_job` 里 `discarded` 的任务，在 `platform.discarded_jobs` 里按任务 ID 认领后报出告警（最后一次尝试的节点消失时由 River 救援直接丢弃的任务也在内，见 [记录与告警的「监控与告警」](../../docs/domains/observability.md#监控与告警)）；扫描 worker 和每分钟的扫描任务由队列自己注册，在归档队列上运行。River 对 `discarded` 的自动清理关闭，扫描在认领后、任务终态满 24 小时才删除任务和认领行，未认领的旧任务不会被先删掉。模块在 `Start` 之前用 `river.AddWorker(q.Workers(), …)` 登记 worker、用 `q.Every(间隔, 参数)` 登记周期任务（只有队列的领导节点插入，启动时先插入一次；领导换人或节点重启会重新计时，所以周期任务必须是重复或晚到都没有代价的）；每个任务种类在它的 `InsertOpts` 里写明队列，这是唯一一处；没写队列的任务落在 River 的 `default` 队列，只有排空的 1 个 worker 会做它，丢掉了隔离，所以 `TestEveryJobKindNamesOneOfTheQueues` 从源码里列出全部种类，新增种类没写队列就失败。River 的 `default` 队列也配一个 worker：旧版本把每种任务都入在那里，做完它们才会放掉其唯一键，否则新队列里的同名周期任务再也插不进来（River 的唯一键不含队列）；没有任务种类写这个队列，测试保证这一点。任务在引起它的写入的事务里入队：`q.InsertManyTx(ctx, tx, 任务…)`，事务回滚任务就不存在，提交了才会被取走；`InsertOpts` 的唯一性让已有同样任务时跳过，返回的数量只数新插入的。任务至少执行一次，worker 必须容忍重复。节点只取自己有 worker 的任务种类：滚动发布期间旧节点碰到新版本登记的种类时不取，不消耗尝试次数也不退避，唯一任务因此不会被卡住（代价是领导节点的救援仍会丢弃它不认识的、已运行满 15 分钟的任务，所以新旧版本并存只应持续一次发布的时间）。价格同步另外订阅节点的关停信号，关停或启动失败时立即取消下载，仍给结果记录最多 5 秒；不改变资金和邮件任务的优雅收尾。节点测试默认不登记价格周期任务，仅价格关停测试显式注入本地数据集。`Stop(ctx)` 先等在跑的任务做完，到期后取消它们并再等几秒；第二次调用什么也不做。一个节点在四个具名队列上同时做共 10 个任务（另有排空 `default` 的 1 个），一次任务最多 10 分钟（River 默认的 1 分钟短于一轮同步）；运行满 15 分钟的任务被当作丢失（节点崩溃时它停在运行中，River 的默认是 1 小时，期间唯一的任务入不了队）。每个队列最老的可用任务等了多久由 `axiom_pg_queue_oldest_job_wait_seconds` 按 `queue` 报告（见「遥测」）；它上升或长时间不降，说明这个队列的 worker 跟不上。River 固定在 v0.49.0（2026-10-05）；升级前读变更说明。v0.49 起它的事务辅助函数不再用保存点，出错时调用方必须回滚整个事务，`pg` 的事务出错即回滚，符合；v0.48 对 `ByPeriod` 唯一性按 UTC 计时，这里没用到。

**回滚到拆队列之前的版本**：旧版本只做 `default` 队列，也只在 `default` 里入队；River 的唯一键不含队列，所以留在具名队列里的未结束任务（例如 `money` 里一个 `available` 的额度到期任务）会一直占着唯一键，旧节点既不会去做它，它自己后续插入同样的周期任务也被当作重复跳过，额度到期因此停摆。只看运行中的任务不够，`scheduled`（延迟）和 `retryable`（等重试）的也会留下。按顺序做：

1. **停止新增任务**：先停掉所有新版本节点（`Stop` 会等在跑的任务做完；任务都在引起它的写入的事务里入队，新版本节点停了就不再有新任务），再启动旧版本。
2. **处理具名队列里全部非终态任务**：先看有哪些，`SELECT queue, state, kind, count(*) FROM river.river_job WHERE queue <> 'default' AND state IN ('available','scheduled','retryable','running') GROUP BY 1,2,3`。`running` 的等它做完；节点已经停了的，由旧版本领导节点的救援在 15 分钟后收回，再按下一条处理。`catalog.sync` 是此后新增的种类，旧版本不认识，不能迁到 `default` 等它执行。确认新节点已停后，删除其未运行的非终态任务：`DELETE FROM river.river_job WHERE kind = 'catalog.sync' AND state IN ('available','pending','scheduled','retryable')`；残留的 running 不动，按上面的救援流程收回后再检查清理。目录价格与 `catalog.sync_state` 保留，重新升级时由 RunOnStart 新开一轮。其余旧版本认识的种类才改放到它能做的队列：`UPDATE river.river_job SET queue = 'default' WHERE queue <> 'default' AND kind <> 'catalog.sync' AND state IN ('available','scheduled','retryable')`。这些任务的种类旧版本都认识，唯一键不受队列影响，所以不会冲突，也不丢任务；`running` 的任务不要改，它的节点会按原队列写回结果。
3. **验证没有遗留**：重跑上面的查询，`queue <> 'default'` 的行必须是 0；再看 `SELECT queue, kind, count(*) FROM river.river_job WHERE state IN ('available','scheduled','retryable','running') GROUP BY 1,2`，每个周期任务的种类（`billing.expire_credit`、`billing.reconcile`、`suppliers.sync_sweep`、`suppliers.reconcile`、`queue.discard_scan`）都只在 `default`，且每种至多一个，旧版本的下一次周期插入才不会被跳过。 另查 `SELECT count(*) FROM river.river_job WHERE kind = 'catalog.sync' AND state IN ('available','pending','scheduled','retryable','running')`，必须为 0；它不属于旧版本可执行种类。
4. **新版本起不来、旧版本已经在跑时**：具名队列里又出现了任务（例如新节点停得晚），重做第 2 步和第 3 步；旧节点会在 `default` 里把它们做完。要再升级回新版本，直接部署即可：新版本留着 1 个 `default` 的 worker，会把 `default` 里剩下的任务做完。

任务参数不属于任何租户（任务行的策略只看平台范围），所以**租户的数据不进参数**：要带租户数据的任务把数据放进模块自己的表，表上开 `FORCE ROW LEVEL SECURITY`、策略写 `platform.in_scope(site_id, account_id)`，参数里只放那一行的 id。工作进程手里只有 id，没有租户的作用域，所以用 `db.TenantTx(ctx, "schema.function", id, fn)` 换取作用域：先以空作用域调一个 `SECURITY DEFINER`、属主 `axiom_lookup`、只授权给应用的查找函数，它只返回那一行的 `site_id` 和 `account_id`（列级授权加 `lookup` 策略，与 [accounts 的登录前查找](../accounts/migrations/00002_lookup.sql) 同一套做法），`TenantTx` 再把这个租户设进作用域才运行 `fn`，查找不到时 `found` 为 false、`fn` 不运行。作用的构造因此仍只在 `pg` 里。第一个这样做的模块是 [mail](mail/README.md)（`platform.mail_outbox`、`platform.mail_tenant`）。

worker 返回的错误也一样：River 把它的文本写进 `river.river_job.errors`（jsonb 数组，每次尝试一项），这一列和参数一样没有策略覆盖。所以 worker 交给 River 的只能是分类：成功、snooze，和「可重试」「永久失败」两种通用错误；收件人、正文和服务器回复只进日志行，日志本来就不属于任何租户。

### 查询（sqlc）

模块的 SQL 写在自己目录的 `queries/*.sql`，由 [sqlc](https://sqlc.dev) 生成类型化的 Go 代码，不用 ORM（[总体架构的「技术选型」](../../docs/architecture.md#技术选型)）。

- **配置**：根目录的 `sqlc.yaml`，每个有查询的模块一项：`schema` 是 `sqlc/schemas.sql`（各模块的 schema 由迁移之前的步骤建好，迁移里没有 `CREATE SCHEMA`，sqlc 只读迁移，所以在这里补上；模块加第一条查询时加一行）加这个模块自己的迁移目录，只列这两样：模块只读写自己的表（[总体架构的规则 1](../../docs/architecture.md#模块边界规则)），别的模块的数据经它的公开接口取得。查询若写到别的模块的表，sqlc 报表不存在而生成失败，所以这一条由生成本身守住；解析本模块的迁移不需要别的模块的建表语句（指向 `platform` 函数的策略照样能解析）。
- **生成物**：写进模块的包里，一律以 `.gen.go` 结尾（`output_files_suffix`）：每个查询文件一个 `<名>.sql.gen.go`，加 `db.gen.go`、`models.gen.go`（没有查询用到的表就没有结构体）。方法把事务作为参数：`New().Insert…(ctx, tx, 参数)`，`Queries` 不持有连接，`tx` 就是 `pg.Tx`（它满足 sqlc 的 `DBTX`，见上文「不变量」），所以查询只能在带范围的事务里执行。模块的 `sqlc.yaml` 项用 `overrides` 把 `uuid`、`numeric`、`timestamptz`、`int8` 生成成 `uuid.UUID`、`string`、`time.Time`、`int64`（可空的为指针），模块的手写代码和测试就不必引用 pgx 的类型；要判断错误的 SQLSTATE（唯一冲突、被拒绝的行）用 `pg.SQLState(err)`。查询没有找到行用 `pg.ErrNoRows` 判断（模块不能引用 pgx）；模块原样保存、不解析的 JSON 字段用 `jsonx.Raw`；模块自己的改动历史要记操作者时用 `audit.Actor.String()`（`<种类>:<ID>`）。生成的文件不手改。
- **重新生成**：`just generate`（`go tool sqlc generate`，版本钉在 `tools/go.mod`）。`just generated` 先删掉已提交的全部 `*.gen.go`、`*.gen.ts`，再重新生成，看有没有变化或没提交：手写却起了这种名字的文件会显示为被删，过期的也一样。删除会丢掉未提交的改动，所以这些文件有未提交的改动或是新文件时，它先失败并列出文件，一个也不删。生成之后，它还拒绝名字不是 `.gen.go`、却带 `// Code generated ... DO NOT EDIT.` 头的 Go 文件（不限 sqlc；头可以缩进、跟在块注释后面，与 Go 解析器的认法一致），生成器首次写出的错名文件（如 sqlc 条目漏了后缀的 `db.go`）也在其内，否则改了查询却忘了重新生成，没有检查看得见。生成器中途失败会留下被删的文件，`git restore` 即可找回。
- **豁免**：生成的文件整体不进 lint（`.golangci.yml` 的 `exclusions.generated`，唯一的一处）。其中包括「pgx 只能在 `internal/platform` 里导入」：`platform` 之外模块的生成代码要导入 pgx 的类型，手写代码仍然不能。lint 凭文件头认生成代码，所以豁免的前提由 `just generated` 守住：带这个头的只有生成器写的 `.gen.go`。前端的 `*.gen.ts` 本来就按文件名免检，同样靠先删后生成证明名字没被冒用。

## 审计日志

谁在何时对什么做了什么改动，记在 `platform.audit_log`，由 `audit.Record` 写入。所有模块都写这一张表，[运营](../../docs/domains/operations.md#审计)读取、检索和回放。它在平台，是因为 accounts、billing、catalog、payments 都要留审计，又都不能引用 `operations`。

- **什么时候记**：改后台数据（设置、价格、额度、角色、凭证等）和领域文档要求留审计的操作，由做改动的模块在改动自己的事务里调用 `Record`，并把它的错误当作改动的错误返回：审计写不进，改动也不成立。accounts、billing、catalog、suppliers 的改动已经调用它。模块自己的改动历史（如 catalog 的 `change` 表）是它的领域数据，与审计日志互不替代；catalog 的管理操作在自己的 `change` 表之外也调 `Record`，见 [catalog 的「决策与理由」](../catalog/README.md#决策与理由)。
- **记什么**：

  | 列 | 内容 |
  |---|---|
  | `actor_kind`、`actor_id` | 操作者：`audit.User(用户 ID)` 或 `audit.System("<模块>.<任务>")`。管理 key 和运营 agent 的种类随第一个以它们身份写入的 PR 加入，届时由迁移放宽约束 |
  | `request_id` | 请求 ID，`Record` 从 context 读取（`httpserver.RequestID`）；后台任务没有 |
  | `action` | 稳定的动作名 `<对象>.<动作>`，如 `api_key.revoke`。检索和回放按它分类，改名就是另一种动作 |
  | `module`、`target_kind`、`target_id` | 改动的对象：所属模块、种类、ID |
  | `site_id`、`account_id` | 对象所属的租户（`audit.Target` 的 `Site`、`Account`）：账户数据两个都填，站点数据只填站点，平台和公开数据都不填；必须是已登记的组合，见「谁能读写」 |
  | `reason` | 规则要求写理由时（如计费的人工调整），操作者写的理由，最多 1000 字 |
  | `detail` | 小的结构化补充：JSON 对象，文本不超过 8 KiB，放 ID、名字、改前改后的值；按 `jsonx.MarshalCanonical` 写 |
  | `id`、`at` | 由数据库生成：UUIDv7，事务开始的时间 |

- **不记什么**：密钥、密码、key、凭证的明文，对话内容，ID 之外的个人信息（邮箱、IP、姓名）；要指向它们就记 ID。8 KiB 的上限只限制条目的大小，挡不住这些内容（`{"text": "一段短对话"}` 照样写得进）；不记它们靠调用方只写允许的字段，由评审把关。
- **谁能读写**：只追加，权限见上文「不变量」。读和写用同一个条件 `platform.in_scope(site_id, account_id)`：租户范围的改动只能记到范围内的租户，没有租户的条目只有平台范围能读写。策略按站点或账户之一放行，所以只要 `site_id` 和 `account_id` 可能不属于同一租户，账户范围的事务就能记下（别的站点，本账户），那个站点的范围就读得到。所以两列由平台自己的租户登记表 `platform.tenants` 绑在一起（负责人定的做法，见「决策与理由」）：accounts 在创建站点、账户的同一个事务里调用 `audit.RegisterSite`、`audit.RegisterAccount` 登记，条目只能写已登记的组合。登记表是平台数据，只有平台范围能读；写入的策略是 `in_scope(site_id, tenant_id)`，因为注册在新账户自己的范围里进行。登记是否与账户表一致，靠 accounts 在插入账户的同一个事务里登记，平台不知道账户表，也不对它建外键。以后租户在控制台看自己的条目时，显示哪些列由那个界面决定，因为理由可能是写给平台内部的。
- **索引**：检索用的索引随第一个读接口加入。
- **保留**：保留期待定，由负责人决定；与钱有关的条目至少与账本保留一样久。到期删除由一个后台任务角色执行，只授予它删除过期条目的权限，随删除任务一起做；在那之前不删除任何条目。
- **防篡改**：数据库权限只挡住应用，拥有表的 `axiom_owner` 和数据库管理员仍能改。运营的哈希链和异地留存建在已提交的条目之上，由 `operations` 实现（[运营的「审计」](../../docs/domains/operations.md#审计)）。

## 幂等

Session 标签以外的管理接口写操作带 `Idempotency-Key`（调用方为一次想做的改动选的 UUID）：丢了响应就用同一个键重发，得到第一次的结果，改动不做第二次。`internal/platform/idem` 把键存在 `platform.idempotency`，只有平台范围读写。

| 列 | 内容 |
|---|---|
| `key` | 调用方的 UUID，主键 |
| `actor`、`operation` | 第一次用它的操作者（`audit.Actor.String()`）和操作名（契约里的 operationId） |
| `fingerprint` | 请求的指纹：路径参数和请求体的规范 JSON（`jsonx.MarshalCanonical`，成员顺序和空白不算）的 SHA-256 |
| `response` | 当时回答调用方的对象，JSON；与占键在同一个事务里写入 |

- **`idem.Once(ctx, tx, claim, do)`** 在改动自己的事务里：先 `INSERT … ON CONFLICT DO NOTHING` 占键，再执行 `do`，最后把响应写进同一行。占到键就执行；没占到，读出第一次的行，操作者、操作、指纹都相同则返回存下的响应（`replayed`），不执行 `do`，否则 `ErrReused`（409 `idempotency_key_reused`）。
- **改动与占键同成同败**：`do` 失败则事务回滚，占键随之消失，调用方可以带同一个键、改正后重发；键被占用的行一定带着响应，所以重放不会读到做到一半的改动。
- **同键的并发请求**：第二个请求的 `INSERT` 在唯一索引上等第一个事务提交或回滚；提交则重放，回滚则轮到它自己执行。所以同一个键至多执行一次，不需要应用层的锁。
- **键属于一次改动**：另一个操作者、另一个操作、同一个操作但内容不同，都是 `ErrReused`。这比契约的字面（"另一种改动"）严：内容不同的请求若重放"第一次的结果"，调用方会以为新的金额已经生效。
- **缺键与坏键**：守卫在生成的路由之前，按契约校验头参数：缺 `Idempotency-Key`、不是小写 UUID 都是 400 `invalid_request`，`fields` 点名这个头，在会话和权限之后。全零的 UUID 由 `idempotently` 拒绝。
- **目录的写操作**自带 `op_key` 和 `catalog.change`，管理接口的处理器仍在占键的事务里调用它们（`Op.Tx`）：同一个键既占 `platform.idempotency`，又写 `change.op_key`，响应在同一事务里存下，所以绑定操作者、操作、目标和内容，并发排队，重放返回第一次的响应。`ErrOpKeyReused` 只留给命令行里用文件给的键。
- **保留**：与审计日志同，期限待定；重放要读那一行，所以在那之前不删。响应里不放密钥：会话类操作不带键，供应商的登录、key 只进密文表。

## 测试

在 Windows 上，`pgtest` 没有 `DOCKER_HOST` 时先把它设成 Docker Desktop 的命名管道：不设的话 testcontainers 靠 `os.Stat` 管道路径找 Docker，所有管道实例都忙（很多测试二进制同时启动）时这个检查失败，转到无根 Docker 的分支，报“rootless Docker is not supported on Windows”，而 Docker 其实在运行；设了以后它直接拨管道，会等到有空闲实例。`pgtest` 的集群：`just test` 经 `internal/platform/pgtest/cmd/withcluster` 起一个 PostgreSQL 容器，把它的超级用户连接串放进环境变量 `PGTEST_CLUSTER_URL`，再运行 `go test`，结束后删除容器；连接串每次都变，所以 `pgtest` 用 `os.Environ` 而不是 `os.Getenv` 读取它，不让它进入 `go test` 的测试结果缓存键（每个测试的库由 `pgtest.New` 新建，连接串不是测试输入）；这一次运行里所有测试包共用它，容器数是同时进行的运行数，不是运行数乘包数。没有这个变量时（直接 `go test ./某包`），`pgtest` 自己起一个容器，行为和以前一样。集群准备好时已建好角色，并用非超级用户的迁移管理员执行全部迁移，做成模板；每个测试复制一个独立的库，名字带本测试二进制的随机标识（共用集群里不同二进制不会重名），测试结束时不单个删库（`DROP DATABASE` 会强制做一次检查点，共用集群时会拖住其他包），整个集群在运行结束时连同这些库一起删掉。`pgtest.New(t, 模块…)` 给出还带着这些模块迁移的库：第一次要某组模块时，在集群的 advisory lock 下复制模板、执行迁移，改名为最终名字（按模块名排序拼成），之后的二进制和测试直接复制它；`pg.Migrate` 授予集群范围的角色，不能同时执行，所以建模板在锁里；先用固定的短名字建，做完再改名，建到一半退出的二进制不会留下看起来完整的模板。容器关闭 fsync 以求快，`max_connections` 调到 1000，因为所有二进制的连接池都在同一个集群里。连接串的主机用 `127.0.0.1` 而不是 `localhost`：`localhost` 让 pgx 先试 `::1`，Docker Desktop 的 IPv6 转发在负载下会卡几十秒，握手超时。会改集群范围状态的测试包要有自己的集群：`internal/platform/pg` 的测试创建、修改、改名角色，还数整个服务器的会话，它的 `TestMain` 清掉 `PGTEST_CLUSTER_URL`，自己起容器；其他包的测试不得依赖整个集群的状态，查 `pg_stat_activity` 要加 `datname = current_database()`。容器由 testcontainers 的 Ryuk 兜底删除：运行被强杀时，Ryuk 在最后一个客户端断开约 2 分钟后删掉容器，环境变量可以改这个时间；等这么久是为了绕开 testcontainers-go 的一个竞态，原因写在 `pgtest` 的 `keepReaperAlive`。

## 决策与理由

| 决策 | 理由 | 不选 |
|---|---|---|
| 全量校验 DNS 结果后按 IP 拨号，连接池仍按原主机复用 | 混合公网/内网结果整体拒绝，DNS 后续变化不能替换已经选择的连接目标；保留 TLS 证书校验和长连接 | 只在 URL 保存时校验或让 Transport 再解析：存在 DNS 重绑定窗口 |
| 私有测试能力值，只能在测试进程构造 | 类型和未导出字段阻止伪造，测试开关不能在生产启用 | 导出 AllowPrivate、任意 Resolver/Dialer：生产调用方能绕过 SSRF 边界 |
| RoundTrip 返回第一个响应 | 重定向既不请求也不解析，畸形 Location 也不能把凭证带进重定向错误 | 默认 http.Client：默认跟随重定向；拒绝回调前仍可能解析 Location |
| 任务队列用 River，放在 `pg` 里，用应用自己的连接池，表在自己的 schema | 任务和业务写入在同一个事务里入队，事件不会丢也不会早发（[总体架构的「技术选型」](../../docs/architecture.md#技术选型)）；`pg.Tx` 有意不暴露 `pgx.Tx`，入队只能由 `pg` 提供；队列的表不存租户的行，不进行级安全审计，和模块的表分开；要带租户数据的任务把数据放模块自己的表、参数只放 id，`pg.TenantTx` 用模块的查找函数换到那个租户的作用域；worker 返回给 River 的错误同样只写分类，`errors` 列也属于没有策略的表 | River 的迁移手抄进 goose 文件：升级 River 时要同步，且 River 启动时核对自己的迁移版本；让模块自己建连接池：绕过范围；把租户数据放进任务参数或错误：任务行和 `errors` 列都没有策略，谁也管不住它们 |
| River 的迁移跟在建 schema 的连接上做，不另开连接 | 每多一个连接，负载高的机器多一次连不上的机会；两者都在迁移管理员的权限下，顺序无关 | 在 goose 之后另开连接：多一个连接 |
| 配置只来自环境变量 | 架构规定不用反射加载配置；容器和 Compose 原生支持；只有一个来源，不用判断优先级 | 配置文件加环境变量覆盖：两个来源，要定优先级 |
| 未知的 `AXIOM_*` 变量算错 | 拼错的名字会悄悄落回默认值，到运行时才发现 | 忽略未知变量 |
| 运行时只有一个登录角色，没有任何角色有 BYPASSRLS | 迁移和回填也受行级安全约束，需要时显式设定平台范围；"能看全部数据"是看得见、能审计的设定，不是某个角色的隐藏能力 | 迁移角色带 BYPASSRLS：多一个能绕过的身份 |
| `in_scope` 的函数体保持简单表达式，不放子查询 | PostgreSQL 只内联简单的函数体；带子查询后策略里每行调用一次，2M 行的扫描实测 53 秒，原来的函数体是 1.9 秒（#63）。要一次解析，只能让策略把解析好的数组当参数传进来，那要改每张表的策略 | 把设置读成标量子查询（#63 原本的写法）：慢 29 倍；用 `IMMUTABLE` 包一层让计划只解析一次：计划在连接上复用，范围会串到别的事务 |
| 登录角色和密码由部署创建 | 密码不经过本程序。部署的初始化脚本用 `CREATE ROLE … PASSWORD` 设置它，语句里有明文密码，数据库开启 DDL 日志时会记下来，所以初始化时不开 | `axiom migrate` 按连接串设置密码：密码要经过程序 |
| 一次 `just test` 的所有测试包共用一个 PostgreSQL 集群，由 `withcluster` 启动，连接串经 `PGTEST_CLUSTER_URL` 传入；没有变量就自己起容器 | 容器数从“包数 × 并发运行数”降到“并发运行数”：几个 worktree 同时检查时，每包一个容器会让 Docker 虚拟机里同时有几十个容器，连接握手超时、测试随机失败；变量缺省时 `go test ./某包` 照旧能单独跑；每个运行自己的容器，不同 worktree、不同 runner 互不串 | testcontainers 的复用（固定容器名加 reuse）：名字要带运行标识才不串，复用的容器没有明确的结束时刻，清不掉；一台机器一个常驻集群：并发运行共用同一份角色和模板，改集群范围状态的测试会互相影响 |
| 模块把迁移作为 `pg.Module` 值交给 `cmd/axiom`，由它传给 `Migrate` 和 `Check`；执行顺序和合法的模块名在 `pg` 的清单里固定 | 依赖一眼可见，没有导入顺序决定的全局状态；顺序与调用方怎么排无关；拼错的模块名在迁移和启动时就失败 | 各模块在包初始化时登记到全局表：靠导入的副作用，gochecknoinits 也禁止；按传入的顺序执行：测试和生产可能以不同顺序迁移 |
| 启动检查按模块名单（各模块同名的 schema）找表和函数，版本表按名字精确排除 | 名单就是 Axiom 自己的全部 schema，迁移不会建名单之外的；比程序新的模块只要在名单里也被检查 | 按 schema 所有者找：以别的角色建的 schema（例如手工修复）会漏查；按 `goose_` 前缀排除版本表：同名前缀的表会漏查 |
| 镜像里的根证书由程序内嵌（`golang.org/x/crypto/x509roots/fallback`，系统没有根证书时 Go 才用它） | scratch 镜像没有证书库，出站 HTTPS 会全部失败；内嵌只多一行导入、不改镜像、不依赖构建机上有没有证书文件，根证书随依赖升级更新 | 往镜像里复制 `ca-certificates.crt`：要在构建机上找到这个文件，Windows 上没有；多一个构建阶段或基础镜像：违背“镜像只有程序” |
| 迁移管理员不必是超级用户，测试也这样跑 | 生产上缺的权限在测试里就暴露 | 测试用超级用户：权限问题到部署时才发现 |
| 测试用 testcontainers，一个测试二进制一个容器，每个测试一份模板副本 | 对着真实的 PostgreSQL 测；复制模板只要几十毫秒，测试互不影响 | 每个测试一个容器：慢；共用一个库：测试互相干扰 |
| 契约按接口面分文件：`api/console.yaml`（控制台，公网监听），`api/admin.yaml`（管理后台，管理监听），以后的运营接口再加一份，在自己的监听上 | 一个前端应用只引用它那一份；公网监听只提供数据面和控制台接口，管理后台和运营接口只挂在管理监听上（[总体架构](../../docs/architecture.md#技术选型)「前端交付」），分文件后不会被意外挂到公网 | 一份大契约：前端类型和服务端接口混在一起，管理接口容易挂错监听 |
| 前端三个应用嵌入程序，预压缩在构建时做，不在请求时压 | 一个文件部署（[总体架构](../../docs/architecture.md#技术选型)「前端交付」）；请求路径上不占 CPU，体积预算量的就是发出去的字节 | 请求时压缩加缓存：占 CPU，要管缓存；只嵌 br 或 gz，旧客户端要解压：多一份代码 |
| 缓存策略按文件名判断（`assets/` 下带 8 位哈希），不读清单 | Vite 和 `theme.js` 都用这个名字；一条正则，不依赖构建的内部文件 | 读 `manifest.json`：多一个要随构建同步的文件 |
| 管理监听默认 `127.0.0.1:8082`，不是 `:8082` | 管理后台不对公网开放；忘了配置的部署也是安全的，容器里由 compose 明确打开 | 默认 `:8082`：忘了限制来源就暴露 |
| 控制台靠 `GET /api/console/v1/version` 发现新版本 | 页面和程序一起发布，版本相同就是同一份；不缓存的接口比轮询入口页更直接 | 前端轮询 `index.html` 的哈希 |
| 密文绑定的位置由 `secret` 包编码，调用方传 `Location`，不传原始 AAD | 数据写入后，位置编码就是存储格式的一部分，永远不能再改；只有一种由包持有的编码，每个模块绑定密文的方式才一致，密文移到另一张表、另一行或另一用途都打不开 | 调用方自行编码 AAD：每个模块各写一种，容易有歧义，改动会让存量数据打不开 |
| `typeid` 用 `Prefix` 值，不用每次传前缀字符串 | 前缀在构造时检查一次，`Encode` 不会失败，`Decode` 不重复检查，请求路径零分配；写错的常量在启动时就 panic | 函数 `Encode(前缀, id)`：每次调用都要检查，或者把写错的前缀留到运行时才报错 |
| 不做泛型的 ID 框架，类型化的 ID 随各自的表加入 | 现在只有一种用法；第三个实体出现时再看共性 | 现在就写 `ID[T]` 之类的泛型：先猜了形状 |
| sqlc 生成的方法把 `tx` 作为参数（`emit_methods_with_db_argument`），`Queries` 是空的 | 事务由 `pg.DB.Tx` 开关，持有 `New(tx)` 的对象会让人把它留到事务之外；空结构体没有状态，也就没有 `WithTx`（调用不到，`just deadcode` 也过不了） | `New(tx)`：多一个可以逃出事务的对象，还带一个没人调用的 `WithTx` |
| sqlc 一个根配置、每个有查询的模块一项，由 `just generate` 运行 | 一条命令生成全部，`generated` 一次检查全部 | 每个包里一条 `//go:generate`：每个包都跑一遍完整配置，或者各有一份配置 |
| 生成物一律 `.gen.go` | `just generated` 只看这个后缀，别的名字过期了也没人发现 | 默认的 `*.sql.go`：不被检查 |
| `just generated` 先删后生成，保留 `exclusions.generated: strict` | 只查文件头抓不到手写的 `x.gen.go`（重新生成不会碰它）；先删再生成，不是生成器写出的就显示为被删；有未提交改动的生成文件先拒绝，不删，否则检查会丢用户的文件。生成步骤是 `just generated` 的参数（默认 `just generate`），测试才能换成假的生成器，把这些行为写成测试。lint 的豁免继续由文件头决定，新模块的 sqlc 输出不用再登记第二处 | `generated: disable` 加按路径逐个列出生成文件：每个有查询的模块要在 `.golangci.yml` 再登记一次，与 `sqlc.yaml` 重复；只查文件头：不拦名字冒用 |
| 角色语句的匹配写成 Python 脚本，不用 `git grep` 或 Go 工具 | `git grep` 逐行匹配，`SET` 与 `ROLE` 隔行或隔注释就漏；Go 工具的测试里要写 `SET ROLE` 字符串，会被 `just scope` 当成违规，Python 文件不在检查范围内 | `git grep -P` 加 `-z`：仍不会剥注释，也不会解开 Go 转义；`tools/` 里的 Go 程序：测试自己触发规则 |
| 审计日志在平台，所有模块经 `audit.Record` 写同一张表 | 要留审计的 accounts、billing、catalog、payments 都不能引用 `operations`；一张表让运营按操作者、对象、时间检索全部改动 | 由 `operations` 提供写接口：违反依赖方向；各模块各建审计表：同样的列写好几份，检索要拼多张表 |
| 审计在改动自己的事务里同步写 | 改动和条目一起成败，没有不留审计的改动，也没有记了却没发生的改动 | 提交后发事件再写：两者之间崩溃就丢条目；单独的事务写：改动回滚后留下没发生的条目 |
| 写入时不接哈希链 | 链头要在每个写审计的事务里加锁，所有模块的管理操作排成一队，锁持有到各自提交；接链和异地留存放在提交之后 | 在 `Record` 里接链 |
| 审计条目用 `in_scope` 一条策略管读和写 | 租户要能看自己的条目，租户范围里的改动也要能记；同一个条件限制条目能记到哪个租户 | 按平台数据只让平台读写：账户范围里的改动记不进；写入不限范围：租户的改动能记到别人头上 |
| 审计条目的租户由平台自有的登记表 `platform.tenants` 核对，accounts 在创建租户的事务里登记 | 平台在依赖的最底层，不能对 accounts 的表建外键；一张登记表加一个外键就把站点和账户绑到同一租户 | 从 `audit_log` 到 accounts 表的外键：平台的表依赖上层模块的表；只靠代码保证：任何一处写错就把条目记到别的租户 |
| 审计条目的规则只写在表的约束上 | 一处规则，不论从哪写都守住 | Go 先校验一遍：两份规则会分叉 |
| 不设整体写超时，只设读请求头超时和空闲超时 | 流式响应的时长取决于模型，由处理器设自己的期限；请求头超时挡住迟迟不发完请求头的连接，空闲超时关掉客户端丢下的长连接 | 统一的写超时：会切断长流 |
| 接口里的金额是整数文本，不是 JSON 数字 | 浏览器在任何代码运行之前就把超过 2⁵³ 的数字舍入；余额、站长的总额和累计流水会超过约 900 万个单位。文本让前端用 BigInt 处理，错了是报错而不是悄悄少一位 | JSON 数字：舍入无声无息；浮点的「额度」：汇总出现误差，违背[计费的「钱的表示」](../../docs/domains/billing.md#钱的表示) |
| 错误体沿用 OpenRouter 的 `{"error": {…}}` 外壳，`code` 是封闭的字符串枚举 | OpenRouter 的 `error.code` 是与 HTTP 状态相同的数字，细节放在 `metadata`（OpenRouter《Errors and Debugging》，2026-10-15 查）。外壳相同，习惯那种写法的客户端不用改；数字重复了状态码，区分不了同一个状态下的 `email_taken` 与 `conflict`，而前端要按原因翻译，所以用枚举。枚举封闭：加一个码要改契约，前端的翻译表编译不过就会发现 | 数字 `code`；RFC 9457 的 `problem+json`：外壳不同，没有现成的客户端依赖 |
| 列表用键集游标，所有列表同一套 | 不用 OFFSET，新记录到达时翻页不重不漏，深页不变慢（[observability 的「读」](../observability/README.md#读)）；OpenRouter 的 key 列表用 offset，但我们的列表里有会增长到几十万行的调用记录。一套约定，前端一个分页组件 | OFFSET：深页读得多；按列表分别约定：每个页面一套分页代码 |
| 每个监听面一份自成一体的契约，会话操作在两份里各写一遍，检查保证两份相同 | 会话属于签发它的面（域名、Cookie、路径各一套），管理接口不能被挂到公网（上一节）。跨文件引用在 oapi-codegen 里要 `--import-mapping`，还得为共享部分另生成一个包（2026-10-15 实测：不给映射直接报错）；共享一份会话契约、两个前缀各挂一次，则前端要为同一组操作维护两套路径 | 外部引用加共享包：多一个包、三条生成命令，换来省下约十个操作的重复；一份大契约：管理接口容易挂错监听 |
| 契约先行，没有处理器的操作由生成的桩返回 501 | 处理器分几个 PR 并行写；生成的 `StrictServerInterface` 要求每个操作都有方法，没有桩则契约一加操作构建就断，有了桩，每个 PR 只加自己的方法 | 手写桩：并行的 PR 改同一个文件；嵌入空接口：未实现的操作调用时 panic；等所有处理器写完再合契约：前后端不能并行 |
| 管理接口里 Session 标签以外的写操作一律带 `Idempotency-Key` 和 `reason` | [运营](../../docs/domains/operations.md#运营接口)要求所有写操作能防重复、能审计；目录的 `op_key`、`reason`、`rev` 已经是这个形状，接口原样透传；一个形状，agent 和管理后台用同一套 | 只给花钱的操作加：agent 要判断哪些要加 |
| 管理接口的幂等键存在平台的一张表里，在改动自己的事务里占键 | accounts 的设置、供应商、计费的赠送都没有自己的 `op_key`，各加一个，每个模块一份实现，也查不出一个键被别种改动用过；一张平台表一份实现，占键与改动同成同败，并发由唯一索引排队。代价是平台多一张只增长的表（保留期待定）和一个泛型函数 | 每个模块各加 `op_key` 列（目录的做法）：重复实现，不能跨操作检出复用；用赠送批次的 ID 当键（`AddCredit` 的 ID 幂等）：只覆盖一个操作；应用层锁加缓存：多节点不成立 |
| 设置用 `PUT` 整体替换，不用带 `null` 的 `PATCH` | Go 生成的类型分不清「没写」和「写成了 null」；整体替换没有这个歧义，页面本来就提交整张表单 | JSON Merge Patch：要引入可空类型，两端的语义要对齐 |
| 契约的约束由守卫按嵌入程序的契约统一强制（kin-openapi），会话、CSRF、权限也从契约的 `security`、`x-axiom-level`、`x-axiom-permission` 读 | 规则只有契约一处，不会和处理器漂移；管理接口的契约有上百个约束，逐个处理器手写必漏；契约读不通启动就失败，没有"忘了校验"的操作。代价是主模块多了 kin-openapi 和它的 5 个依赖（jsonpointer、swag/jsonname、oasdiff/yaml、yaml3、jsonschema/v6），每个请求多一次 schema 校验（微秒级，控制台不在调用路径上） | 每个处理器手写校验：漂移，也要在每个新操作里重写；`go:embed` 契约原文件：`api/` 下不能放 Go 文件（[模块边界规则](../../docs/architecture.md#模块边界规则)），放到别处就要复制一份 YAML；在运行时读文件：部署要多带文件 |
| 守卫在生成的路由之前运行，不作中间件 | 生成的包装在中间件之前解析查询参数，`/keys?limit=many` 会先得到 400（还带着解析器的原话），绕过本该先答的 401／404；放在前面，顺序就是守卫声明的顺序，包装的解析错误只剩 schema 够不着的角落 | 作为 `Middlewares`：包装的解析抢在前面；守卫里再补一遍参数解析：两处规则会漂移 |
| 控制台请求一律 15 秒的期限，到期答 503 | 登录等用户行锁、改 key 等 key 的行锁、数据库不应答，都会让请求一直占着连接，积累到耗尽连接池（"每一跳都有超时"）；期限随 context 一路传到事务，pgx 到期取消语句并丢弃连接。最慢的合法请求是 argon2id 排队加计算，几秒。不设可配置项：没有部署需要不同的值，需要时再加 | 数据库语句超时：只管语句，不管等哪个锁之外的卡；`http.Server` 的写超时：切断响应而事务照跑，也不适合以后的流式接口 |
| 生成的处理器用 `jsonx.Decode` 读请求体，经 oapi-codegen 的用户模板 | 校验器按精确的名字匹配，encoding/json 不分大小写，两者读到的不是同一个请求；项目的 JSON 策略本来就是区分大小写、跳过未知成员 | 契约里给请求体都写 `additionalProperties: false`：新页面比服务器新时多发一个字段就被拒，与"多发的成员被忽略"的现有约定相反；守卫里自己比对大小写：多一套按 schema 走 JSON 的代码，且与解码器的折叠规则可能不一致 |
| 路径里的 ID 不在守卫里校验，格式不对与不存在同样 404 | 契约写明如此；防枚举：客户端分不出"不存在"与"格式不对" | 守卫按 schema 的 pattern 返回 400 |
| 处理器返回错误，守卫和生成代码的出错路径经同一张表回答（状态、`ErrorCode`、不含客户端值的英文说明） | 一个操作的失败只有一种写法；模块新增一种拒绝，加一行 | 每个操作各写各的错误响应：生成的响应类型每个操作各有一套，重复 |
| 客户端 IP 由 `platform/clientip` 解析，控制台守卫和网关读同一份；accounts 收到的是已经判定的地址 | 谁是受信任的代理是部署的事（`AXIOM_TRUSTED_PROXIES`），模块不该知道部署的网络；网关和控制台各写一份就会在代理后各错一处 | 在 accounts 里读 `X-Forwarded-For`：模块要知道部署的网络；在网关里另写一份：同一行为两处维护 |
