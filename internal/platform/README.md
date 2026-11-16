# platform

所有模块共用的基础设施，不放业务规则；总体规则见[总体架构](../../docs/architecture.md)，推导和被否方案在[存档](../../docs/archive/platform.md)。

## 包

| 包 | 公开接口 |
|---|---|
| `platform` | 值类型 `UnitPrice`、`Exact`、`Currency`、`ExchangeRate`、`RequestID`、`AttemptSeq`；规则由 [billing](../../docs/domains/billing.md#钱的表示) 定 |
| `config` | 把 `AXIOM_*` 读成带类型的值，问题一次报告；默认值以 `cmd/axiom` 为准 |
| `privfile`、`secret`、`nodetls` | 私密文件的唯一打开入口；密钥加载、信封加密与脱敏；节点间双向 TLS（[nodetls](nodetls/README.md)） |
| `httpserver` | 监听的中间件、`Compress`、健康检查、`/metrics` |
| `clientip` | 客户端地址的唯一读法与来源分组 `Group`（IPv6 按 /64） |
| `webui`、`egress`、`egresstest`、`*api` | 嵌入的前端、出站 HTTP、测试用回环放行、由 `api/*.yaml` 生成的接口，见下文 |
| `textshape` | 不分配地数十类字符特征，乘 [billing](../billing/README.md) 的权重估 token，不超过字节数 |
| `jsonx` | 唯一的 JSON 策略：`Validate`、`Unmarshal`、`Decode`、`UnmarshalKnown`、`MarshalCanonical`（RFC 8785，整数原样） |
| `pg` | 连接池、范围事务、迁移、启动检查、任务队列（下文与 [pg](pg/README.md)） |
| `kv`、`mail`、`objectstore` | [Valkey 与节点心跳](kv/README.md)、[出站邮件](mail/README.md)、[S3 客户端](objectstore/README.md) |
| `audit`、`idem`、`page`、`typeid`、`alert`、`telemetry` | 审计日志、幂等键、时间/UUID 游标、对外 ID（`前缀_` 加 26 字符的 TypeID v0.3.0）、告警信号、遥测，见下文 |
| `containertest`、`pgtest`、`kvtest`、`alloctest`、`backup`、`telemetrytest` | 测试设施，见[存档](../../docs/archive/platform.md#测试设施) |

## 不变量

- **配置**：非法值和没人读的 `AXIOM_*` 在抢占监听之前一次报出，空值等于未设定；错误只含变量名和期望形状。
- **密钥**：明文只在 `secret.Secret` 里，一切格式化输出 `[REDACTED]`，只有 `Reveal()` 取出；持有密钥材料的类型都把它放在闭包后面。私密文件的类型和权限从打开后的描述符读（`privfile.Open`），拒绝符号链接和组/其他人权限。
- **出站**：只连已校验的公网 IP，调用方换不掉 Transport、Resolver、Dialer；请求必须有期限；不跟随重定向，拒绝 `101`；URL 不带用户信息，错误里只有主机名。
- **JSON**：只经 `jsonx`（depguard 禁止别处用 `encoding/json`）：拒绝非法 UTF-8 和重复键，嵌套至多 256 层，未知成员跳过（`UnmarshalKnown` 拒绝）。
- **数据库**：模块只能经 `pg.DB.Tx`/`ReadTx`/`TenantTx` 访问，`pg.Tx` 交不出连接；depguard 禁止 `internal/platform` 之外引用 pgx 和 `database/sql`。范围和角色只在事务内有效，只由 `pg` 设定（`just scope` 拦下别处的 `set_config`、范围参数名和 `SET/RESET ROLE`）。没有范围什么都查不到；范围是平台 ∪ 站点集合 ∪ 账户集合；只有 `cmd/` 能构造平台范围，只有 `internal/accounts` 能构造站点和账户范围（forbidigo）。`ReadTx` 是只读的 REPEATABLE READ。
- **启动检查**（`serve`）：登录角色能成为的角色里没有超级用户、BYPASSRLS、REPLICATION、CREATEROLE、CREATEDB、表的属主、任务角色和服务器文件/程序角色；模块 schema 里每张表（版本表除外）ENABLE 且 FORCE 行级安全并有策略；定义者函数都属 `axiom_lookup`、`search_path` 固定、只有 `axiom_app` 能执行；缺迁移、摘要不符或有本程序没有的旧迁移就不启动。
- **对外 ID**：`Prefix.Decode` 只认恰好的前缀、`_` 和 26 个小写字符，错误一律 `ErrInvalid`、不带输入；写错的前缀常量启动时 panic。
- **钱的值类型**：单价 0…1000、单位 10⁻¹⁵，只从十进制文本解析，算出的向上取整，写法唯一；`Exact` 是 128 位整数，溢出返回错误，负用量和超出 int64 panic。

## 凭证加密

- `secret.LoadMaster` 在启动时读一次 `AXIOM_MASTER_KEY_FILE`，同一快照给出 `Keyring()`、`LoginKey()`、`DeriveKey(label)`；`secret.LoadMAC` 读 API key 的 HMAC 密钥 `AXIOM_KEY_HASH_FILE`；`secret.LoadSecret` 读 `AXIOM_IDENTITY_KEY_FILE`，用最新一把派生发往上游的会话和用户标识。缺一或不合法就不启动；文件的生成与 `AXIOM_KEYRING_GENERATE` 见[生产入口](../../docs/ops/backup-restore.md#生产入口)。
- 文件格式：每行 `版本号:base64密钥`（无前导零的正 uint32；解码后 32 字节），允许 LF/CRLF，不许空行、注释、空白、BOM、重复版本或密钥。
- `Keyring.Seal(Secret, Location)` 每次用独立的 AES-256-GCM 数据密钥加密，再用写版本包裹它；`Open` 失败不返回明文。`Location{Table, RowID, Purpose}` 由包编码进 AAD（字段为空 panic），读取时从预期位置重建，记录移位或字段被换都认证失败。一把主密钥合计最多包裹 2³² 个数据密钥，轮换要用新的随机密钥。
- 写版本记在 `platform.key_versions`（只存摘要，不存材料）。`axiom keyring add|activate|rewrap|retire|check` 管理它，步骤见[主密钥轮换](../../docs/ops/backup-restore.md#主密钥轮换)。节点每 10 秒原子替换密钥环并在心跳里报告已核对的版本；全部在线节点报告后自动激活，按 100 个一批重包裹，零引用且所有节点写出新版本满 30 秒后退役；退役与信封写由一把 advisory 锁和 `platform.key_retirements` 互斥，迟到的旧版本写被拒。
- 信封存储的清单在 `cmd/axiom`（`secret.Rotation`），每个信封表还要挂退役触发器；启动、激活和退役核对两者，缺一拒绝。

## 出站 HTTP

- `egress.New(egress.Direct)` 是并发安全的客户端，只支持直连；`DialContext` 给 SMTP 复用同一套校验；`ReadBody(body, max)` 超限报错、不返回截断内容。
- DNS 结果全部合格才拨号（任一不合格整组拒绝），按 IP 拨，Host 和 SNI 保留原主机；拒绝私网、回环、链路本地、特殊用途段、非 `2000::/3` 的 IPv6、NAT64 知名与本地前缀、Azure 的 `168.63.129.16`，以及 URL 里的映射 IPv6、非常规 IPv4 写法和 zone。
- 连接合计 10 秒（候选 IP 平分），TLS 握手 10 秒，空闲 90 秒，响应头至多 1 MiB；其余只受调用方期限约束。
- 部署前提：出站网络没有转到内网的自选 NAT64 前缀（本包认不出）。

## 监听

| 监听 | 内容 |
|---|---|
| 公网 `AXIOM_PUBLIC_ADDR` | 官网、控制台与 `/api/console/v1`、模型调用 `/v1/`、`/api/v1/`、支付回调 `/api/payments/v1/notify/{merchant}` |
| 管理 `AXIOM_ADMIN_ADDR` | 管理后台与 `/api/admin/v1`；只在核心，默认 `127.0.0.1:8082`，按 `AXIOM_ADMIN_SOURCES` 限来源，非回环又没配来源不启动 |
| 节点 `AXIOM_NODE_ADDR` | 与公网相同的路由加 `/livez`、`/readyz`，只收 `ca.crt` 签发的客户端证书 |
| 运营 `AXIOM_OPERATIONS_ADDR` | `/api/operations/v1`；客户端证书须带 `ops` 用途，按 `AXIOM_OPERATIONS_SOURCES` 只看连接地址 |
| MCP `AXIOM_MCP_ADDR` | 只在 `ops` 角色、只绑回环，见[运营的「MCP 映射」](../../docs/domains/operations.md#mcp-映射) |
| 探针 `AXIOM_PROBES_ADDR` | `/livez`、`/readyz`、`/metrics`；读写各限 2 秒 |

- `AXIOM_NODE_ROLES`（`core`、`ingress`、`ops`，默认 `core`；`core` 与 `ingress` 互斥）决定开哪些监听、读哪些变量，角色不读的变量设了算错（拿错环境就在启动时看见）。`AXIOM_TRUSTED_PROXIES` 里的代理才读 `X-Forwarded-For`，取最右一个不受信地址。
- 启动：检查配置 → 抢占监听（被占即失败）→ 数据库检查等，合计至多 `AXIOM_STARTUP_TIMEOUT`，失败关闭已抢的监听；检查通过前 `/readyz` 不应答。
- 退出：第一个信号后 `/readyz` 和探针外监听的新请求答 503（`Retry-After: 1`），在途请求至多等 `AXIOM_SHUTDOWN_TIMEOUT`；第二个信号立即退出。
- 探针外的监听都经 `httpserver.Public`：自生成 UUIDv7 请求 ID（`X-Request-ID`）；访问日志记路由模式不记路径；请求体须 2 分钟内发完；panic 未响应时答 500，已响应时断连接；处理器错误只答 500。
- 入口转发到 `AXIOM_CORE_URL`：响应头最多等 15 分钟，响应体静默 5 分钟结束，整个请求最多 65 分钟，每次写最多 10 秒；每秒探核心 `/readyz`。

## 前端文件

- `webui` 嵌入 `just web-embed` 的产出，没嵌入时启动报错。
- `assets/` 下以 8 位哈希结尾的文件 `public, max-age=31536000, immutable`，其余（含入口页和 404）`no-store`。
- 构建时预压缩 br 和 gzip，按 `Accept-Encoding` 的 q 值选（同值 br 优先），带 `Vary`；`httpserver.Compress` 只在控制台接口上、只压至少 1 KiB 的 `application/json`，流式响应不压。
- 只有 GET/HEAD、无扩展名、`Accept` 含 `text/html` 的未知路径回退到入口页，`/api/`、`/v1/` 下一律 404。公网处理器不持有管理后台的文件。
- 每个应答带 CSP（只在 `vite.shared.ts` 定义）、`nosniff`、`X-Frame-Options: DENY`、`Referrer-Policy`、`Permissions-Policy`。
- `AXIOM_WEB_ASSETS_DIR`：按请求读其他构建的带哈希文件（本构建优先，入口页和策略不从目录读），`axiom web-assets <dir>` 写入本构建的，同名不同内容失败；滚动发布时旧页面因此取得到旧代码块。

## 接口约定

契约每个监听一份：`api/console.yaml`、`api/admin.yaml`、`api/operations.yaml`。`just api` 校验契约和下表的约定（括号注明的除外）；`just api-compat` 禁止已有结构新增必填字段（`x-axiom-newly-required` 写理由豁免）。客户端视角见[前端接口指南](../../docs/frontend-api.md)。

| 约定 | 内容 |
|---|---|
| 路径 | `/api/<文件名>/v1/`；只加不改，改含义或删除才升版本 |
| 安全声明 | 每个操作写 `security`；会话操作写 `x-axiom-level`、`x-axiom-permission`，改数据的加 `csrf`；管理接口非 Session 操作要 `mfa` |
| 改数据的请求 | 站点登记的 `Origin`、JSON、会话的 `X-CSRF-Token`，否则 403 `origin_rejected`；不开 CORS |
| 错误体 | `{"error": {code, message, fields}}`，`code` 取封闭枚举 `ErrorCode` |
| 分页 | 键集游标 `cursor`、`limit`（默认 50，最大 200），响应 `{data, next_cursor}` |
| 值（不检查） | `Money.amount` 是十亿分之一单位的整数文本，倍率等是 `Decimal` 文本；账户实体用 TypeID；路径 ID 格式错同不存在 404；没有的值不出现；`PUT` 整体替换 |
| 管理写操作 | 非 Session 的写带 `Idempotency-Key`、`reason`，`rev` 过期 409 `stale_revision`；只读的 POST 标 `x-axiom-safe: true` |
| 运营接口 | Bearer 凭证；`x-axiom-permission`、`x-axiom-money`、`x-axiom-scope`、唯一的 `x-mcp-name`；读带 `Reason`，写带 `IdempotencyKey` 和 `reason`；`agentGuard` 先写 `operations.operation_log` 再答，写不进 503；读 15 秒、写 30 秒 |
| 共有部分 | 控制台与管理契约各抄一份，同名须相同；运营契约 `$ref` 到 `admin.yaml` |
| 无固定形状的 JSON（测试） | 形状固定的给命名模式、以 `jsonx.Raw` 照存；成员归别人的只在描述里列；请求里的模式不让守卫多拒 |
| 没有处理器（`just generated`） | `tools/pendingapi` 的桩答 501 `not_implemented`；现在每个操作都有处理器 |

`cmd/axiom` 的守卫在生成的路由之前，按嵌入的契约强制：读不通或有程序强制不了的声明就不启动。顺序：站点（Host 不对 404）→ Origin 与 JSON → 会话、级别、CSRF → 权限 → `jsonx` 策略 → 参数与请求体 schema（400 `invalid_request`，`fields` 写位置和规则、不写值）。期限：控制台 15 秒、管理一分钟，到期 503 `unavailable`。生成的处理器用 `jsonx.Decode` 读请求体（改过的模板 `consoleapi/templates/strict-http.tmpl`）。权限名和是否敏感在 `internal/accounts/authorize.go` 声明。

## 遥测

- `telemetry.New` 建提供者，由 `cmd/axiom` 显式交给 `pg`、`egress`、`httpserver` 和模块，不用 OpenTelemetry 全局。指标只在探针的 `/metrics`，专用注册表，不含 Go 运行时默认集合。
- 命名 `axiom.<模块>.<对象>.<量>`；标签只用有界枚举、每个指标至多 1000 种组合，路径、ID、账户、key、用户都不是标签，未匹配的路由记 `unmatched`。新指标要有测试读到它的名字；现有指标见[存档](../../docs/archive/platform.md#指标)。
- 每个请求一个跨度，`db.tx`、`egress.request` 是子跨度；属性只有有界值，不记语句、错误文本、路径和查询。没有导出器（`NeverSample`），属性只在 `span.IsRecording()` 时构造。
- 稳态记一次测量不分配（标签组合按组合缓存），由基准对比守住，见[性能](../../docs/performance.md)。

## 数据库

`pg.Tx.Batch` 在范围事务里一次发多条语句，按序取结果，退出时排空关闭；任一失败回滚整个事务。

### 角色

- `axiom_owner`（不登录）拥有全部 schema 和对象，只在迁移时进入；`axiom_lookup`（不登录）只拥有登录前查找的定义者函数（[accounts](../accounts/README.md#登录前的查找)）；任务角色（`pg.Module.JobRole`，不登录）拥有要建删的表，部署的登录角色（如 `axiom_retention`）是成员，`axiom_app` 不是。
- `axiom_app`（`AXIOM_DATABASE_URL`）是运行时唯一的应用登录角色，由部署建并设密码；迁移管理员（`AXIOM_MIGRATE_DATABASE_URL`）要 CREATEROLE 和库的 CREATE，不是超级用户。

### 连接池

- 每节点连接预算 N = `(max_connections − 服务器预留槽位 − pg.OperationsReserve) ÷ AXIOM_CORE_NODES − pg.OutsidePoolConns`，最多 `pg.PoolSize`；`pool_max_conns` 只能在份额内覆盖。其中 R（`AXIOM_DB_COMPLETION_RESERVE`，默认 `pg.CompletionReserve`）条只给 gateway 的追加、结算和孤儿结算（`pg.CompletionContext`），其余进普通池；要求 0 < R < N，装不下就不启动（错误和启动日志列出全部输入）。
- 两个池互不借用，其余事务（含 River、`TenantTx`）都走普通池；gateway 的许可数取自 `db.MaxConns()`、`db.CompletionConns()`（[gateway](../gateway/README.md#并发背压与共享状态)）。
- 池外连接三条：名字锁 `Hold`、River 的 LISTEN、持有者心跳与回收的 `pg.DB.Dedicated`（[计费的「持有者与截止时间」](../../docs/domains/billing.md#持有者与截止时间)）。

### 范围

- 四个事务级参数 `axiom.scope_platform`、`scope_sites`、`scope_accounts`、`scope_home`，由 `platform` 的范围函数读取，表达式见 [pg](pg/README.md)。`scope_home` 由 `pg.AccountScopeIn` 按授权认出的站点设定，只放宽站点的公开配置和共享计数。
- 账户数据的行存非空的 `site_id`、`account_id`，站点数据存 `site_id`；公开数据任一非空范围可读、只有平台可写。

### 迁移

- 模块以 `pg.Module` 列进 `cmd/axiom` 的 `modules()`，同交 `pg.Migrate` 和 `DB.Check`；`platform` 先执行，其余按[依赖表](../../docs/architecture.md#模块之间的依赖)，名单外或重复的失败。
- 迁移以 `axiom_owner` 持 goose 会话锁、带平台范围执行，各锁最多等约 5 秒；连接期限 `AXIOM_MIGRATE_CONNECT_TIMEOUT`（默认 10 秒），整次 `AXIOM_MIGRATE_TIMEOUT`（默认 30 分钟），到期按本次的 `application_name` 终止会话。
- `platform.migration_checksums` 与版本行同事务记下每个迁移的 SHA-256，执行前核对全部历史，改过、改名、删掉的立即停下；CI 另以 `just migrations <base>` 拦。
- River 的表在 schema `river`，由 `migrate` 迁移并授给 `axiom_app`；启动检查核对其版本。

### 任务队列

- `pg.Queue`（`db.NewQueue`）封装 River（v0.51.0）。具名队列的 worker 数：`money` 3、`reconcile` 1、`notifications` 2、`syncs` 4、`archive` 1；每个任务种类在 `InsertOpts` 写明队列（`default` 没有 worker），测试检查。
- `Start` 前用 `q.Workers()`、`q.Every` 登记 worker 和周期任务（领导节点插入，须容忍晚到和重复），之后登记 panic。`q.InsertManyTx` 在引起它的写入事务里入队；至少执行一次，worker 要容忍重复。节点只取有 worker 的种类（SMTP 未配置的节点不登记邮件 worker）。
- 任务最多 10 分钟，运行满 15 分钟算丢失；丢弃的任务每分钟扫描、在 `platform.discarded_jobs` 认领并告警，终态满 24 小时才删。
- 租户数据不进任务参数和 worker 错误（River 的表没有策略）：数据放模块的表，参数只放 id，worker 用 `db.TenantTx` 换到该租户的范围。

### 查询（sqlc）

- SQL 在模块的 `queries/*.sql`；`sqlc.yaml` 每模块只读本模块迁移，查别的模块的表生成就失败。
- 生成物一律 `.gen.go`，方法把 `pg.Tx` 作参数，模块不碰 pgx 类型（`pg.SQLState`、`pg.ErrNoRows`）。
- `just generated` 先删后生成（有未提交改动先拒绝），拒绝带生成头却不叫 `.gen.go` 的文件；生成文件不进 lint。

## 审计日志

`audit.Record` 在改动自己的事务里写 `platform.audit_log`，写不进改动就不成立；所有模块共用。`audit.List`、`audit.Get` 供管理和运营接口读（平台 `audit.view`），`audit.Since` 供运营的哈希链和异地副本。

- 列：操作者（`audit.User`、`System`、`ManagementKey`、`OperationsAgent`）、`request_id`、稳定的 `action`（`<对象>.<动作>`）、模块与目标、租户 `site_id`/`account_id`、`reason`（至多 1000 字）、`detail`（JSON 对象，至多 8 KiB）、库生成的 `id`、`at`。
- 不记密钥、凭证明文、对话内容和 ID 以外的个人信息（靠调用方和评审）。
- `axiom_app` 只有 SELECT 和除 `id`、`at` 外的 INSERT；读写同一条账户范围策略，无租户的条目只有平台可读写。站点和账户必须是 `platform.tenants` 里登记的组合（accounts 在建租户的事务里调 `audit.RegisterSite`/`RegisterAccount`）；规则都在表约束上。
- 列表窗口默认 24 小时、上限 31 天，至多 200 行、10 秒，按 `(at, id)` 倒序。保留期待定，之前不删。

## 幂等

`idem.Once(ctx, tx, claim, do)` 在改动自己的事务里占 `platform.idempotency` 的键（只有平台范围），执行并存下响应；同一操作者、操作和指纹（路径参数与请求体规范 JSON 的 SHA-256）重放第一次的响应，否则 `ErrReused`（409 `idempotency_key_reused`）。`do` 失败则占键一起回滚；同键并发由唯一索引排队，至多执行一次。`idem.Lookup` 只读已提交的响应。缺键或非小写 UUID 答 400，全零 UUID 也拒绝。保留期同审计。

## 运维告警信号

`alert.New` 的 `Raise`/`Resolve` 不等数据库：本机缓冲 1024 个，种类 128、键 256 字节，至多 16 个字段（名 64、值 512 字节），拒绝非法 UTF-8 和 NUL；写入期限 5 秒，`Close` 排空。`alert.Persist` 提交后才持久，UUID 防重写；`alert.Consume` 与生产共用一把锁。来源位置（`alert.LockPosition`、`SavePosition`、`ClaimSources`）与信号同事务提交。只放运维元数据，规则见 [observability](../observability/README.md#告警接口与不变量)。

## 决策与理由

| 决策 | 理由 |
|---|---|
| 校验全部 DNS 结果后按 IP 拨号 | 没有重绑定窗口，仍保留证书校验 |
| 配置只来自环境变量，未知变量算错 | 一个来源；拼错的名字不会落回默认值 |
| 连接预算启动时按服务器与节点数算，两个物理池 | 扩节点不会当天连接满；River 直用 pgxpool，信号量拦不住 |
| River 在 `pg` 里、同一事务入队 | 任务不丢不早发，模块拿不到连接 |
| 审计在平台、在改动事务里同步写，不接哈希链 | 模块不能引用 `operations`；同成同败；链头锁会让管理操作排队 |
| 守卫按嵌入的契约强制，在生成路由之前 | 规则只有一处；包装不会抢先答 400 |
| 金额是整数文本，错误码是封闭枚举 | 浏览器舍入超过 2⁵³ 的数；前端按原因翻译 |
| 设置用 `PUT` 整体替换 | 生成类型分不清没写和 null |
| 前端嵌入程序、构建时预压缩 | 一个文件部署，请求不占 CPU |
| 指标用 OpenTelemetry、Prometheus 抓取 | 换导出方式不改模块；不依赖境外服务 |
| 根证书由 `x509roots/fallback` 内嵌 | scratch 镜像没有证书库 |
