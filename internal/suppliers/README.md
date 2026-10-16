# suppliers

供应商模块：供应商及其账号凭证、分组、模型、端点的表，适配器的契约和注册，以及调用路径上的接口（挑凭证、发请求、记尝试、写对账日志）。适配器、同步、对账的要求见[供应商](../../docs/domains/suppliers.md)和[计费的「供应商成本」](../../docs/domains/billing.md#供应商成本)，一次调用的编排见 [gateway](../gateway/README.md)。

> [Issue #25](https://github.com/TYCT-tyct/Axiom/issues/25) 分多个 PR 实现。已实现：表、适配器契约、注册表、启动检查、new-api 与 sub2api 适配器、同步引擎及其状态、租约和限速桶的持久化、调用面（挑凭证、发请求、记尝试、对账日志）和对账，写在「代码结构」到「对账」；同步的调度（River）、事件投递和管理接口在后续 PR。「对账日志」一节是日志的设计，已实现，实现的接口在「调用面」。代码与本文不一致时，在同一个 PR 里改本文。

## 代码结构

```
internal/suppliers/                 公开接口：Module()、CheckAdapters()、Service（调用面）、RecordAttempts 等、Reconcile
internal/suppliers/internal/
  adapter/       契约：Adapter、Manager、CallQuirks、Failure、CallPolicy、Client、BucketSpec
  registry/      注册表：唯一同时引用所有适配器的文件，一个适配器一行；按 kind 查找
  client/        适配器唯一的出站入口：经 platform/egress、按声明的限速桶放行（进程内或数据库里的桶）
  newapi/        new-api 适配器（含 testdata/ 录制样本和测试）
  adaptertest/   每个适配器都要过的契约测试（只被测试引用）
  syncer/        同步引擎：只做决定，状态由 Store 整份读出、整份提交
  persist/       同步状态的持久化：租约与栅栏、Store、LoginStore
  reconcile/     对账：匹配、判定、暂挂、事件
  store/         迁移、查询（sqlc 生成）
  removable/     `just adapters-removable` 的程序
```

`sub2api/`（适配器，`testdata/` 里是从 sub2api 0.2.16 录制并脱敏的样本）已有；后续 PR 增加其余适配器的包、`wire/`（与系统无关的路径和鉴权头）、同步的调度和挑凭证。

## 表

schema `suppliers`，迁移在 `internal/suppliers/internal/store/migrations/`（设计草稿里的 `store/migrations/` 按[总体架构](../../docs/architecture.md)的 `internal/` 规则改为此路径），版本表 `platform.goose_suppliers`。`00001` 是配置表；`00002` 是调用面和对账的表；`00003` 是同步的表。单位价值随 billing 的换算加入，迁移只增不改。

| 表 | 内容 | 键与约束 |
|---|---|---|
| `suppliers` | 归属（`owner_scope` 与 `site_id`、`account_id` 由 CHECK 保持一致）、`kind`、`origin`（`https://host[:port]`，小写）、`upstream_account`、原生单位与结算币种、管理出口、状态、`config_gen`、带符号的有限余额（`numeric` 的 NaN 和无穷被 CHECK 排除，倍率、阈值同样） | `UNIQUE (origin, upstream_account)` |
| `supplier_logins` | 账号凭证：`kind` 加一份加密值（密文、nonce、包裹的数据密钥、主密钥版本），`version` 供比较后再写 | PK `supplier_id` |
| `supplier_groups` | 上游的分组：`upstream_ref`、倍率、原始事实（jsonb）、是否选中、代号、是否仍存在 | `UNIQUE (supplier_id, upstream_ref)`、`UNIQUE (id, supplier_id)`、代号全局唯一且设定后不变 |
| `credentials` | 调用凭证：对方的 key ID、加密的 key、状态、冷却、并发上限、出口；状态为"绑定中"时还没读到 key | `UNIQUE (supplier_id, upstream_key_ref)` |
| `supplier_models` | 分组里的上游模型名、映射到的目录模型（空为待映射）、格式 | PK `(supplier_group_id, upstream_name)` |
| `supplier_aliases` | 供应商别名，分组可空（整个供应商有效） | `UNIQUE NULLS NOT DISTINCT (supplier_id, supplier_group_id, upstream_name)` |
| `endpoints` | 分组 × 模型：格式、成本状态（已知 / 未知 / 冲突）、成本计划编号、是否启用 | `UNIQUE (supplier_group_id, model_id)`；成本状态为已知必有成本计划编号 |
| `cost_schedules` | 端点的成本计划，`catalog.Schedule` 的规范 JSON，币种 `XXX`；`endpoints.cost_schedule_id` 指向当前的一版 | 只追加；`UNIQUE (endpoint_id, fingerprint)` |
| `attempt_journal` | 进行中的尝试：对账键、是否已提交 | PK `(request_id, attempt_seq)`；只增不减的合并；请求终结满两天（`ended_requests` 里 billing 告知的时刻）后清除，没告知终结的保留；平台范围 |
| `attempt_costs` | 每次尝试的成本和对账键，billing 在结算事务里写 | PK `(request_id, attempt_seq)`；`charge_record_id` 唯一；行带请求所属的租户 |
| `ended_requests` | billing 告知的请求终结时刻，日志按它保留两天 | PK `request_id`；只插入；行带请求所属的租户 |
| `lost_requests`、`lost_request_resolutions` | 节点失联的请求，和节点带着真实尝试回来 | 只插入；行带请求所属的租户 |
| `charge_records` | 供应商的扣费记录：状态 `new/matched/held/assigned`、归属、版本 | `UNIQUE (supplier_id, charge_ref)`；归属和状态的 CHECK；平台范围 |
| `charge_events` | 给 billing 的事件，每个版本一行 | `UNIQUE (charge_record_id, revision)`；平台范围 |
| `sync_states` | 每个供应商一行：引擎的整份状态（jsonb，`format` 记格式）、`version`（引擎的栅栏）、`next_due`、租约（`lease_holder`、`lease_fence`、`lease_until`） | PK `supplier_id`；`lease_holder` 与 `lease_until` 同有同无；平台范围 |
| `sync_events` | 引擎每一步产生的事件，只插入，事件按名字存 | PK `(supplier_id, version, seq)`；平台范围 |
| `sync_plans` | 引擎算出的成本计划的每个版本（端点是「分组/模型」文本），只插入 | PK `(supplier_id, version, seq)`；平台范围 |
| `rate_buckets` | 供应商的限速桶：窗口起点、已用次数、封到何时 | PK `(scope, name)`；平台范围 |

- **授权的偏差**：设计里这几张表只有对账角色能改，那个后台任务角色还没有（security.md 的角色表），所以应用角色现在能改 `attempt_costs` 的 `charge_record_id`、`state` 和 `charge_records` 的状态与归属列，别的列、DELETE 都没有；角色隔离随后台任务角色加入。
- **行级安全**：`suppliers` 的策略是 `platform.in_scope(site_id, account_id)`，平台的供应商只有平台范围看得见；其余各表不存归属列，只存 `supplier_id`，策略是"这个供应商对本事务可见"，所以插入也挡住"挂到看不见的供应商下"，也不会有与父对象不一致的标签。
- **复合外键**：凡同时存 `supplier_id` 和 `supplier_group_id` 的表，用 `(supplier_group_id, supplier_id) → supplier_groups (id, supplier_id)`，分组与行不可能属于不同供应商。别名的分组可空，空表示整个供应商。
- **授权**：应用角色对配置表没有 DELETE（移除供应商是状态，要删密文行的移除流程随它的代码加授权），只有对账日志可以删（结算两天后清除）；UPDATE 按列授予，不含 `id`、`supplier_id`、归属列、`kind`、`origin`、`upstream_account`：身份和凭证发往的地址不能改。
- **密钥**：表里没有明文列。加密值写进 `ciphertext`、`nonce`、`wrapped_dek`、`key_version` 四列，数据库只检查字段齐全和长度（nonce 12 字节、密文不短于 16 字节、包裹的数据密钥非空、版本号合法），**不能**分辨密文和别的字节：任何满足长度的明文也写得进去。加密由写入边界保证（只把 `secret.Sealed` 交给查询），值是否真实、是否属于这一行由 `secret.Open` 验证。`secret.Location` 的行 ID 在加密前就要知道，所以 `credentials.id` 由调用方生成，不用默认值。
- **跨模块引用**：`model_id` 是目录的 ID，没有外键；`endpoints.cost_schedule_id` 与 `cost_schedules` 用（成本计划，端点）复合外键。

## 适配器

- **契约**（`adapter`）：`Adapter` 有 `Kind`、`Buckets`、`Detect`、`Open`、`Calls`；`Open` 返回只读的 `Manager`（账户、报价、key 状态、按对方 ID 读 key 原文、扣费记录分页；每笔记录带标准化用量 `catalog.Usage` 和"已报告的项"集合，没报告和报告为零不同，暂估才能按项、按请求锁定的单价校正）；`Calls` 返回 `CallQuirks`（`Formats`、`ChargeRef`、`Classify`、`Policy`）。写能力以后用单独的可选接口加入，能力就是实现了什么。
- **`Failure`** 是 `{Executed, Scope, Cause}`。`Cause` 是封闭的和类型（`gochecksumtype` 检查每个 switch 都处理了全部成员），成员只带数字和时间，**没有来自对方的文本**，所以回显了凭证的响应进不了日志和表。`Executed` 的零值是 `ExecutionUnknown`：没把握就按"可能执行了"处理，成本按估算记、由对账补正；`NotExecuted` 只给对方在执行前就拒绝的响应。`Transport` 只由通用调用层设置，适配器不得返回。作用范围比设计草稿多一个 `Endpoint`（限流、过载作用于一个分组和模型）。
- **`Client`** 是适配器唯一的出站入口，`Request` 声明占用的全部桶，`BucketSpec` 声明对方的限速；任一桶没有名额就不发，返回包装了 `ErrBucketsFull` 的错误，调用方推迟而不是当失败。实现是 `internal/suppliers/internal/client`：`Limiter` 在进程内计数（适配器的测试用），`SharedLimiter` 在数据库里计数（`rate_buckets` 表，多个节点从一个出口地址访问同一个供应商时用），两者都实现 `Buckets`。
- **注册表**（`registry`）：`adapters` 列表是唯一同时引用所有适配器的地方，一行一个，不用 `init()`。`registry.New` 在启动时拒绝不合法的 `kind`、重复的 `kind`、不可能放行请求的限速桶。
- **移除**：删适配器的目录和注册表里它的那一行。`just adapters-removable` 对注册表里的每个适配器，用 `go build -overlay` 在不改工作区的前提下删掉二者，再跑 `go build ./...` 和 `go vet ./...`；还有别的代码依赖它时失败，并给出编译器的原话。注册表里现在有 new-api，检查对它真实生效。
- **启动检查**：`CheckAdapters` 在 `serve` 启动时列出仍在使用（状态不是"已移除"）而本程序没有注册适配器的供应商，有就拒绝启动并逐个指出名字和 `kind`。它要看到全部供应商，所以由 `cmd/axiom` 传入平台范围。

## sub2api

适配器在 `sub2api/`，`kind` 是 `sub2api`。事实来自 0.2.16 的官方镜像（本地实测，2026-10-15）和只读的源码调研（#25）。

- **账号凭证**：`Login`（邮箱、密码、访问令牌、刷新令牌、过期时间和有效期），编码成带版本号的明文 JSON 交给 `LoginStore` 加密；解码拒绝未知成员；`Secret` 的打印一律脱敏。sub2api 没有个人访问令牌，刷新令牌**只能用一次**。
- **保持登录**：访问令牌只剩四分之一寿命时刷新；对方说刷新令牌无效（`REFRESH_TOKEN_INVALID`）才用密码重新登录；刷新结果不明（网络）时不换密码，留给下一轮（令牌若已用掉，下一轮会被拒绝，再重新登录）。写回用 `Swap` 比较版本，失败（别的持有者先写）就改用已存的，不再发请求。对方说账号被撤销、停用、会话绑定变化（`TOKEN_REVOKED`、`USER_INACTIVE`、`SESSION_BINDING_MISMATCH`），或密码被拒、要二步验证，返回 `ErrAuthRejected`：不再登录，避免封号。调用中途令牌过期只续一次。
- **桶**：登录 5/分、刷新 7/分（按 IP），面板 60/分、用量 15/分（按账号），是对方默认值的四分之一；每个请求声明所占的桶，测试核对。
- **读取**：余额 `/api/v1/auth/me`，有生效订阅即资金来源不是钱包（`/subscriptions/active`）；分组 `/groups/available`、用户倍率 `/groups/rates`；每个分组用其中一把可用的 key 读 `/v1/models` 和 `/v1/sub2api/billing` 的 `resolved_rate_multiplier`（含高峰）；key 的余额为 0 时列不了模型，该组标 `ModelsUnread`，保留上次的模型。key 列表按 `/api/v1/keys` 分页，`KeySecret` 按 ID 取（ID 只许数字）。目录和 key 清单超过一个限速窗口时（例如 29 个有 key 的分组要 61 次面板请求，窗口上限 60）读不完一轮：适配器把已读的部分留在这个 Manager 上（10 分钟内有效），被限速后再调用同一个 Manager 从断点接着读，不从头来；测试用程序自己的限速器和假时钟验证。成功的响应缺 `data`、列表或页数时不是"空结果"，而是 `ErrUnrecognised`，任务停下、状态不变。
- **价格**：默认安装里普通用户看不到价格，模型的成本一律是空（未知），不上架，直到人确认底价；`adapter.ScaleSchedule` 把目录官方价乘分组倍率做成草稿（币种写 `XXX`，单位是对方的美元）。能公布价格的 `channels/available` 尚未读取（实例里关着，没有样本）。
- **调用面**：`anthropic` 分组服务 Messages，`openai` 分组服务 Responses 与 Chat；订阅分组、高峰分组、其他平台不服务。对账键是响应头 `X-Client-Request-Id` 加 `client:` 前缀，等于扣费记录的 `request_id`（实测）；对方自己生成，不读我们传的。`Classify` 只读错误代码，不留对方的文字；`Cancels` 为否（对方把上游读完并全额扣费，实测）。
- **扣费记录的扫描**：位置（`scanState`）是适配器自己的，模块当作不透明的字节，每一页连同它存下；空位置是第一次扫描，只记下"从现在起"（水位），本次返回 Done；之后每遍从（水位 − 5 分钟）读到（现在 − 30 秒），每天再读一遍最近 24 小时（补扫，不动水位）。`/api/v1/usage` 只认日期，所以一遍按 key 逐把读，新的在前、按页翻；起止日取窗口两端的日期（跨午夜两天都读），每页 200 条（每条还带着 key、用户和分组，页太大会超响应上限），一把 key 在短页或最旧一条早于窗口起点时结束，全部 key 结束才算读完。记录的用量映射到不重叠的计费项（输入、输出、缓存读、5 分钟和 1 小时缓存写），**只有记录里写了的项算已报告**；非 token 计费（按次、图片）带 `UnknownUsage`；`billing_type` 不是 0 的不是钱包。记录里的 key 原文、用户、地址一概不读。移除的第二遍读取用 `Rescan(从何时起)` 开一个补扫。
- **未验证**：key 的重置时间字段（`reset_*_at`）没有样本；分页 `total` 是否精确；订阅分组的记录形态。

## 同步

`syncer/` 是每个供应商一份的同步引擎，只做决定，不自己存任何东西：状态（`State`）由 `Store` 整份读出、整份提交，每一步的事件、成本计划版本和扣费记录与状态在同一次提交里，提交带读到的版本（栅栏），别的写入者先提交则这一步整个作废（`ErrFenced`）。租约、数据库事务和对应的表是下面的「持久化」，River 的调度随后加入。

- **轮次**：`Round` 按顺序执行到期的任务（余额 1 分钟、目录 5 分钟、key 状态 5 分钟、扣费记录 2 分钟，默认值在 `Defaults`）。每个任务各提交一次。
- **失败**：网络错误和 5xx 指数退避（1 分钟起、上限 30 分钟、可加抖动），期间不调用，别的任务照跑，成功即清零；桶不够（`ErrBucketsFull`）不算失败，到 `Blocked.Until` 再来；响应认不出（`ErrUnrecognised`）只停这个任务，什么也不写，发事件；**鉴权被拒（`ErrAuthRejected`）停掉这个供应商的全部轮询**，状态"需处理"，发一个事件，之后的轮次什么也不调用。
- **变化即事件**：目录规范化后与上次比较，只把差异写成事件（组、模型的增减，倍率变化，成本计划新版本）；同样的响应读两次没有新事件、新计划。成本计划 = 对方公布的价，或人确认的底价乘当前倍率；指纹变了才写新版本。路由要用的变化都有事件：组的倍率、组的事实（平台、订阅类型、高峰、状态）、模型的格式和能力限制（`Without`，快照里完整保存，没读到的组也保留）、资金来源、余额越过预留线和回升到告警线之上；相同的响应不发事件。
- **扣费记录**：位置是适配器自己的（见各适配器一节），引擎一概不读：第一次调用不带位置，每一页连同返回的位置在一次提交里存下，被限速、出错、重启后从这个位置接着读，适配器说 Done 即一遍读完；记录按对方的请求编号去重。资金来源不是钱包的记录让供应商进入"需处理"。
- **可路由**：`EndpointFacts.Check` 逐条判断（这份清单是设计草稿 #25 的第 4 节，本文是它的落地处）：供应商正常、资金是钱包且余额减预留大于 0、分组被选中、模型已映射、成本已知、至少一个可用凭证；给出第一个不满足的原因。供应商状态决定的几条由 `State.Facts` 填，其余由分组、映射、凭证的表补。「模型已映射」由 `syncer.MapUpstream` 判定：先看供应商别名（调用方从 `supplier_aliases` 查出），再用目录快照的内部识别查公开和内部名字，映射到的模型必须是上架的，退役或目录不认识的名字、带 `:free` 这类后缀而没有别名的名字都是未映射，`EndpointFacts.Mapped` 为假，端点不进路由；不带日期的名字只是候选（`Mapping.Unverified`），要先过真伪检测。写 `supplier_models.model_id` 和生成端点的同步持久化（#25）调用它，调用点随那个 PR 接上。余额低于告警线发一次 `BalanceLow`，回升后才会再发。
- **移除**：`Remove` 是持久化的分步状态，每一步先提交再做下一步，可重复调用：离开快照（配置代次加一，记下重读的起点 = 开始移除的时间 − 宽限期）→ 等在途尝试结束（最多 5 分钟）→ 读扣费记录，一遍接一遍，直到适配器报告读到了开始移除的时间（`ChargePage.Through`；一遍可能只覆盖一个窗口，积压要读多遍；契约测试要求适配器在 Done 页报告它，不报告的适配器只见于没走契约测试的假适配器，以一遍结束为准；#109 的第二条，追赶久了发告警，留给后续）→ 等 30 分钟宽限 → 从起点重读一遍（Manager 实现了 `adapter.Rescanner` 就用 `Rescan(起点)`，否则接着 `Charges` 读）→ 删除加密凭证（`DeleteSecrets` 必须可重复执行）→ 完成。宽限期内才出现、时间戳在起点之后的记录都会被读到。完成时的事件列出这家供应商的 key，由人到对方后台吊销（M1 不对对方做写操作）。账号已被拒绝则跳过读取。
- **尚未做**：按需的"无逐笔记录"判定、成本低估下架（随对账）。

## 持久化

`persist/` 把引擎的 `Store` 和适配器的 `LoginStore` 接到数据库上。一个节点对一个供应商 `Acquire` 一次租约得到一个 `Session`，一轮同步（`Round`）用它的 `Store()`，登录令牌的比较后再写用它的 `Logins()`，轮后 `Release`。

- **租约与栅栏**：取租约是一条 `INSERT … ON CONFLICT DO UPDATE … WHERE 租约已空或已过期`，每次取到 `lease_fence` 加一；租约还有效就取不到（`ErrLeaseHeld`，不是故障：另一个节点正在同步它）。每一次写（提交一步、`Swap`）在自己的事务开头先 `SELECT … FOR UPDATE` 锁住租约行，要求 `lease_fence = 手上的`，再锁供应商行（`FOR NO KEY UPDATE`，同 `UPDATE` 非键列的锁，不挡别的表往它上面写子行）并要求 `config_gen` 还是取租约时的，两者持到事务结束；任一不符返回 `ErrFenced`，整个事务回滚。接手租约和改 `config_gen` 都要等这个事务结束，所以通过的写排在接手或改动之前，不通过的绝不会落在它之后：租约过期后被接手的旧持有者，哪怕晚了很久才写、哪怕写到一半对方才来接手，也写不进去。只在语句里用快照读栅栏不行：`UPDATE … FROM` 只锁目标行，等锁期间别的事务接手，它仍会按旧栅栏写入。**锁的顺序**固定为租约行 → 供应商行 → 登录等依赖行；改凭证的代码要先加 `config_gen`（锁供应商行）再写登录，不能倒过来，否则会和 `Swap` 死锁。版本（`version = 读到的`）仍在 `UPDATE` 里比较。时钟只决定租约何时可以被接手，不决定写入放不放行，节点之间时钟差几秒只会让接手早一点或晚一点，不会让两个节点都写进去。每次提交把租约续到「提交时刻 + `LeaseTTL`」，一轮最多 50 页，每页一次提交，所以一轮可以比 `LeaseTTL` 长；停止提交的节点在 `LeaseTTL` 后失去租约。
- **一步的提交是一个事务**：先 `hold`（上一条的两把锁），再写状态、这一步的事件（`sync_events`）和成本计划（`sync_plans`）、扣费记录（`RecordCharges` 同一个 `Ingest`）和对供应商的投影，要么全在要么全不在。栅栏挡住时（`ErrFenced`：租约被接手、配置代次变了、版本不符）没有写任何东西。配置代次变了就整步作废，包括因旧凭证被拒而要写的「需处理」，以免它把已经改好的供应商再次停在永不同步。事件按名字存（`Kind.String()`），不按常量的序号。
- **状态的格式**：`state` 是引擎的 `State` 的规范 JSON，`format` 记它的格式号，读不认识的格式或多出来的成员就失败而不是猜。`Task`、`Status`、`RemovalStep` 的常量只在末尾增加。第一次同步的供应商没有状态，读出来是零值的 `State`。
- **投影**：每次提交把最后读到的余额和观察时间写到 `suppliers`，并按引擎的状态写 `status`：正常时 `connecting → active`、`active ↔ low_balance`，需处理写 `needs_attention`，移除写 `removing`、`removed`。人暂停的 `paused` 和对账暂停的 `needs_attention` 不被「正常」覆盖，移除不会被改回需处理。路由的快照看这个状态。配置代次（`config_gen`）不投影：引擎的代次只管它自己的轮次，`suppliers.config_gen` 由改凭证、暂停、移除的代码加一，`hold` 用它判断一轮是不是在凭证被换之前开始的。
- **`next_due`**：提交时从状态算出，是各任务到期时间和退避结束时间中最早的一个（已停的任务不算；状态不是正常，即需处理或移除中，算作永不），调度据此找到该同步的供应商。
- **`LoginStore`**：`Load` 用供应商 ID 为位置打开密文（`suppliers.supplier_logins`、供应商 ID、`login`），整条记录挪到别处就打不开；`Swap` 封存新值后在一个事务里先 `hold`（租约栅栏还是手上的、`config_gen` 还是取租约时的），再要求版本没变地写入，任一不符返回 `ErrStaleLogin`，登录令牌不会被一个已失去租约的节点或一个在凭证被人换过之后才到的刷新覆盖。
- **限速桶**：`rate_buckets` 的键是（谁，桶名）。适配器的 `BucketSpec.Per` 为按 IP 的，「谁」是 origin 加出口，同一站点的所有供应商共用；为按账号的，是供应商 ID。一个请求声明的桶在一个事务里计数：建桶和封桶都是 `INSERT … ON CONFLICT DO UPDATE`，按（谁，桶名）排序：新桶由插入持有，已有的桶由冲突时的更新持有，所以建桶这一步结束时已按键序持有声明的全部桶，之后的 `FOR UPDATE`（也按键序）不再取新的锁。若建桶用 `DO NOTHING`，请求会持着新桶去等别人持有的已有桶，而那个请求正要新桶，两条语句各自排序也躲不开这种死锁。所以两个请求以相反的顺序声明同样的桶，无论桶是否已存在，都只是互相等待；测试覆盖全部已有、全部新建、已有加新建三种状态；检查、再一起计数，所以全取或全不取；429 把所占的桶封到 `Retry-After` 对所有节点生效。代价：每个对外请求多一个数据库事务；管理面的请求每个供应商每分钟几十次，调用面的请求不走这里。窗口从第一个请求的节点的时钟开始，节点间几秒的时钟差对几十秒到几十分钟的窗口没有影响。
- **尚未做**：把分组、模型、端点、成本计划落进 `supplier_groups`、`supplier_models`、`endpoints`、`cost_schedules`（`sync_plans` 里的端点是「分组/模型」文本，端点行要有映射到目录的 `model_id`，映射和别名还没有实现；`sync_plans` 如实保存，不丢）；事件的投递（`sync_events` 是出口，投递随后）；调度（River）和进 `cmd/axiom serve`；人把「需处理」的供应商恢复的管理操作（它要同时改 `suppliers.status` 和重置引擎的状态）；移除的触发和 `Hooks`（在途尝试数、删除密文）。

## new-api 适配器

`newapi/` 实现 `adapter.Adapter`。事实来自 new-api 的源码，并在运行 v1.0.0-rc.44 的本地实例上核对（Issue #25 的两条调研评论）；`testdata/` 是脱敏后的录制应答，测试回放它们。

- **识别**：`/api/status` 有应答、每个应答带 `X-Oneapi-Request-Id` 才算 new-api；`/api/user/login/encryption-key` 存在是 1.0，不存在是 0.13（版本字符串可能是空的，不信它）。同时读出登录是否要验证码。
- **账号凭证**：1.0 的个人访问令牌（PAT）加过期时间，`EncodeLogin`/`DecodeLogin` 在封存前后转换，明文只在这两个函数里出现，拒绝未知成员；令牌用 `Authorization: Bearer`，不会过期时没有要写回的状态。**0.13 不接入**：它要 cookie 或访问令牌加 `New-Api-User`，没有录制样本，也没在本机跑起来验证，所以只识别、`Open` 对非 PAT 的凭证报"凭证被拒"。以后接入是增量。
- **读取**：余额是 `/api/user/self` 的整数 `quota`，单位提示是 `/api/status` 的 `quota_per_unit`；有生效中的订阅就是"资金来源不支持"（`/api/subscription/self`）。key 列表按页读，状态 1 到 4 对应正常、停用、过期、额度用尽，认不出的状态停下这个任务。key 原文按对方 ID 读（`POST /api/token/:id/key`，ID 必须是纯数字才会进路径），占关键桶。
- **目录和成本计划**：分组来自 `/api/user/self/groups`（倍率明确写着 `auto` 的没有自己的倍率，不收；倍率缺失、为 null 或不是数字是"认不出"，不当作要略过的分组），每组的模型来自 `/api/user/models?group=`，价格来自 `/api/pricing`。成本计划是 `catalog.Schedule`，币种 `XXX`（原生单位是每 token 多少 quota，不是钱）：输入 = `model_ratio × 分组倍率`，输出 = 输入 × `completion_ratio`，缓存读 = 输入 × `cache_ratio`（缺省 1），缓存写 = 输入 × `create_cache_ratio`（缺省 1.25），一小时缓存写 = 缓存写 × 1.6；用精确分数算，向上取整到 15 位小数，所以价格不会低于对方的。按次计费、`tiered_expr`、倍率缺失、价格超过上限的模型没有成本计划（成本未知）；`image_ratio` 显式且不为 1 的模型不声明图片能力（`Model.Without`）。推理没有单独的价，`Cost` 按输出价算。
- **限速桶**：`global`（每个 `/api/*`）90 次 / 180 秒，`critical`（读 key 原文）5 次 / 1200 秒，各是对方默认的四分之一。
- **失败归类**（`Classify`）：只认 `type` 为 `new_api_error` 的错误，同样的状态码没有这个类型是渠道上游透传的，不当成我们的 key 或余额的问题。403 `insufficient_user_quota` 是供应商余额不足（作用于供应商）；401 且 `code` 为空是凭证被拒（作用于凭证，透支用尽和吊销同形）；403 `pre_consume_token_quota_failed` 是凭证额度用尽；503 `model_not_found` 是没有可用渠道（过载，作用于端点）；429 是限流，限流原因和等待时间照常，但执行状态不确定（空正文既可能是 new-api 自己的限流器，也可能是没读到正文，渠道上游或状态码映射也可能在请求执行后给出 429）；400 是请求格式错，执行状态不确定（渠道错误可以被映射成 400）；其余 5xx 和认不出的是上游错误或未知。只有对方在执行前就拒绝的才是 `NotExecuted`。对账键是响应头 `X-Oneapi-Request-Id`。
- **管理接口的错误**：401/403 带 `code` 的按固定清单报"凭证被拒"（缺权限时带上权限名），200 加 `success:false` 和认不出的结构报"认不出"，错误里没有对方的文字。
- **成功应答缺成员不是空结果**：`{"success":true,"data":{}}` 这类缺少必需成员（列表、总数、数量、详情）或为 null 的应答报"认不出"，不当成没有 key、没有分组或没有扣费记录：否则对方改了结构，水位会被推进、同步结果被清空、端点被下线。
- **清单可以续读**：key 列表和报价每页或每组一个请求，可能多于一个窗口的名额（9,050 个 key 是 91 页，桶是 90 次）。请求因没有名额被拒时 `Manager` 记下读到哪里，同一个 `Manager` 再调用就接着读，读完或超过 10 分钟就丢弃；调用方在重试期间保留同一个 `Manager`。

### 扣费扫描

`/api/log/self?type=2` 新的在前、按偏移翻页。翻页期间写入的记录只会出现在最前面，把后面的往后推：后页重复读到前页的末尾，不会漏，所以按请求编号去重、页码可以跨调用保存。`Manager.Charges` 每次读一页，位置 `ScanState` 是适配器自己的 JSON（版本 1），由同步引擎和这一页的记录在同一个事务里保存：

- **一遍** 读固定的 `[start, end]`（服务端按秒级时间戳过滤），读到不满一页（含恰好整页之后的空页）为止。前进遍：`start = max(水位 − 5 分钟, 开始扫描的时刻)`，`end = min(水位 + 跨度, 现在 − 30 秒)`，读完把水位升到 `end`，水位只升不降；水位之后不足一分钟时不发请求，返回空页和 `Done`。每天一次的补扫遍读最近 24 小时，不动水位，用来拿比重叠回扫更晚到的记录；它从"上一次补扫覆盖到的位置往前一天"开始，所以每一段时间在一天之后还会被再读一遍，补扫晚了几天也不留空隙；晚到不超过一天的记录，哪怕属于已被补扫覆盖过的一段，也被下一遍补扫读到（前进遍的 5 分钟重叠只管更近的晚到）。
- **窗口自适应**：跨度默认 1 小时；一遍超过 20 页，下一遍减半（最短 1 分钟）；连续三遍短，加倍（最长 1 天）。
- **限速**：桶没名额或收到 429（`client` 封住所占的桶）时返回错误且位置不变，下次从同一页继续，不算失败。
- **重启、丢状态**：每次调用都可以从保存的位置重新开始，重复读到的页同样的内容；没有位置（第一次）从现在开始，不读连接之前的记录；读不懂的位置报"认不出"并保持原样。
- **记录**：`Ref` 是记录的 `request_id`（与响应头同值），`Key` 是 `token_id`，`Amount` 是整数 `quota`，`Usage` 是 `prompt_tokens` 为输入、`completion_tokens` 为输出。`Reported` 只在没有缓存读写 token 时含输入，**从不含输出**：`prompt_tokens` 含不含缓存 token 随用量的格式（OpenAI 含、Anthropic 不含）而不同，记录里看不出是哪种；`completion_tokens` 含推理 token 且记录不拆分，当作输出项校正会让推理被算两次（一次在输出里、一次在没有校正的推理项里）。这些记录只剩金额能校正。没有请求编号、key、整数金额或读不懂的记录让这一遍失败而不是跳过。

### 出站入口（`client`）

`client.Client` 实现 `adapter.Client`：地址是创建时给的 origin 加适配器给的路径（路径必须以单个 `/` 开头，所以改不了主机），请求经 `platform/egress`，响应正文上限 8 MiB。`Limiter` 是进程内的固定窗口计数：请求声明的桶要么全部取到名额，要么一个都不取；429 把请求所占的全部桶封到 `Retry-After`（new-api 的 429 没有正文，认不出是哪个桶），并返回 `adapter.Blocked`（包装 `ErrBucketsFull`）。多节点共享一个出口地址时要把名额分给各节点或把状态放进数据库，接口不变。

## 不变量

| 不变量 | 靠什么保证 |
|---|---|
| 密钥明文不落库 | 表里没有明文列，加密列只由 CHECK 约束长度（不证明是密文）；加密由写入边界保证，真实性由 `Open` 验证；测试在整行、全部列里搜索 key 的字节（文本和十六进制），并且加密值只能在自己那一行打开。**尚未覆盖**：写入处只传 `secret.Sealed`，由第一个适配器的编解码函数保证，随它的 PR 加测试 |
| 供应商只在自己的范围内可见、可写；子表经父表判断 | 策略；测试覆盖无范围、平台、站点、账户、另一个站点或账户，以及六张子表各自的插入 |
| 分组、凭证、模型、别名、端点属于同一个供应商 | 复合外键；测试对四张表各试一次跨供应商引用 |
| 身份和发往的地址不能改，不能删除 | 按列授权、不授 DELETE；测试逐列、逐表 |
| 成本未知的端点不会被当成已知 | `known_has_schedule` CHECK；成本状态进快照的条件在后续的快照查询里 |
| 对方的文字不进错误、日志和表 | sub2api 的失败路径只带状态码和白名单内的代码（`knownCodes`，不在表内的代码一律说"没有已知代码"，回显了凭证的 `reason`、`code` 也进不来）；测试让响应回显一个凭证，核对每个读取的错误里都没有它；录制样本里的令牌、key、邮箱、地址、请求编号都是占位符 |
| 缺成员的成功响应不是空结果 | `open` 要求 `code` 为 0 且有 `data`，列表和页数缺失即 `ErrUnrecognised`；表驱动测试覆盖 `{"code":0}`、`null`、`{}`、缺 `pages` 等 |
| 5xx 不会因为响应里的代码被当成"没执行" | `Classify` 先排除 5xx；契约测试（`adaptertest`）对已知拒绝代码的 5xx 也要求"可能执行了" |
| 进宽限期之前，扣费记录已读到开始移除的时间 | 适配器在 `ChargePage.Through` 报告读到哪里，引擎读到它不早于开始移除的时间（容差 2 分钟）才进宽限期，不解析适配器的位置；真实 new-api 适配器的测试让积压跨多个窗口，把记录放在缺口里，断言删除凭证前已入库 |
| 引擎与适配器的扫描协议一致 | 位置是适配器自己的不透明字节，第一次调用不带位置；`adaptertest.RunScan` 对每个适配器检查（含结束一遍后的位置能再传回），新 API 和 sub2api 各有一个真实适配器经引擎读完一遍、并在移除时读到迟到记录的测试 |
| 对方撤销账号后不再登录或轮询 | `ErrAuthRejected` 后适配器不再发登录，引擎把供应商置为"需处理"；测试数发出的请求 |
| 同一个响应同步两次没有新事件 | 目录以规范化内容比较；测试 |
| 一次移除的每一步都可重复，效果只有一次 | `Remove` 的步骤持久化；测试对每一次提交失败和每个钩子失败重跑，核对事件、代次 |
| 失败不带对方的文本，没把握不算免费 | `Cause` 的成员；`adaptertest` |
| 正常供应商一定有注册的适配器；删任一适配器其余照常编译 | `CheckAdapters`；`just adapters-removable`（现在对 new-api 真实生效） |
| 不超过对方的限速，被限速时不丢进度 | `client.Limiter` 全部取到名额才发、429 封全部所占的桶；`Charges` 出错时返回原位置；扫描测试在小名额和 12,000 条记录下统计每个窗口的请求数 |
| 多个节点从一个出口地址访问同一个供应商，合计不超过对方的限速 | `SharedLimiter` 在数据库里计数；测试让两个节点（各自的连接池）同时取同一个桶，合计只放行上限那么多，一个桶满时另一个桶不被扣，一个节点封桶另一个节点也被封 |
| 同一时刻只有一个节点写一个供应商的同步状态 | 租约加栅栏；测试让多个节点同时取租约只有一个成功，租约过期被接手后旧持有者的提交和登录令牌的写回都被拒绝且什么都没留下 |
| 一步的状态、事件、成本计划、扣费记录同成同败 | 一个事务；测试让扣费记录被数据库拒绝，核对状态、事件、计划都没有留下 |
| 节点崩溃后从存下的位置接着读扣费记录 | 每页与位置同一次提交；测试让一个节点读到第三页失败且不还租约，租约过期后另一个节点从第三页开始，记录各存一次 |
| 失去租约或凭证已被换过的节点不覆盖登录令牌 | `Swap` 和提交先锁住租约行与供应商行再比较栅栏和 `config_gen`；测试逐项，并在写被锁挡住时让接手、改凭证同时发生 |
| 扣费水位只升不降，每条记录至少读到一次 | 前进遍的 `end` 大于水位、水位取最大值；新记录只出现在列表最前面；测试覆盖整页边界、扫描期间新增、迟到记录（重叠回扫和每日补扫） |
| 凭证只发往接入时的主机，错误里没有对方的文字 | `client.New` 只接受裸 origin，路径不能改主机；管理接口的错误只带固定清单里的 `code` 和权限名 |

## 调用面

已实现（`service.go`、`attempt.go`、`journal.go`、`record.go`）。`Service` 是一个节点上的调用面：快照、凭证运行状态、对账日志缓冲、成本计划。

```go
func New(cfg Config) (*Service, error)                        // DB、平台范围、主密钥、出口、适配器注册表、时钟
func (s *Service) Reload(ctx context.Context) error            // 重建快照，在本节点内存里解密 key；key 打不开或缺适配器就失败，旧快照不动
func (s *Service) Acquire(ep EndpointID) (*Attempt, error)     // 内存比较交换；ErrNoEndpoint、ErrNoSlot
func (a *Attempt) Send(ctx context.Context, c Call) (*http.Response, *Failure) // 内含 Classify；Call{Format, Body, Header}
const NotExecuted = adapter.NotExecuted // Failure.Executed 的取值：确知上游没有执行；gateway 够不到适配器的类型，只用这两个
type RateLimited = adapter.RateLimited  // Failure.Cause 的一种：上游回了“请求过多”，路由据此给端点降权
func (a *Attempt) Receipt() AttemptReceipt                     // 到目前为止的回执，给对账日志
func (a *Attempt) Done() AttemptReceipt                        // 幂等；释放占位；带凭证、端点、成本计划版本、已到的对账键
func (r AttemptReceipt) Key() string                           // 对账键，结算写进 billing.AttemptResult；其余字段不对外
func (a *Attempt) Policy() CallPolicy                          // 适配器默认；端点实测值覆盖 Cancels
func (s *Service) SetMeasuredCancels(ep EndpointID, cancels bool)
func (s *Service) Cost(r AttemptReceipt, u []catalog.Usage) (AttemptCost, error) // 纯内存：原生金额、缺项、cost_flag
func (s *Service) Journal(e JournalEntry)                      // 非阻塞，进程内缓冲
func (s *Service) JournalFlushed(id platform.RequestID) <-chan struct{}
func (s *Service) Settled(id platform.RequestID)
func (s *Service) Run(ctx context.Context) error               // 每秒写一次日志，结束时再写一次
func RecordAttempts(ctx context.Context, tx pg.Tx, rows []AttemptRecord) error
func RecordLostRequest / ResolveLostRequest(ctx, tx, id, site, account ...) error
func EndRequest(ctx, tx, id, site, account, at) error                // billing 在终结请求的事务里调用
func PurgeJournal(ctx context.Context, tx pg.Tx, now time.Time) (int64, error) // 删终结满 JournalRetention 的请求的日志
func JournalOf(ctx context.Context, tx pg.Tx, id platform.RequestID) ([]JournalEntry, error) // 清理读崩溃节点留下的日志
func RecordCharges / Reconcile / AssignToLostRequest / AssignUnattributed(ctx, tx, ...)       // 对账，见下
```

与设计草图的差别：`RecordAttempts` 等是包级函数（不需要 `Service`）；`Cost` 没有时间参数和单位价值（单位价值表随 billing 的换算加入）；`Send` 的入参是 `Call{Format, Body, Header}`，`protocol.Upstream` 合并后换成它。`Header` 是 gateway 的白名单头（`anthropic-beta`、`anthropic-version`），在默认头之上设置、再由凭证和内容类型盖过：客户端换不掉凭证，写了 `anthropic-version` 就用它的，没写才加默认的 `2023-06-01`。`Failure` 是 `adapter.Failure`。

- **不等重建**：`Reload` 在自己的状态里解析成本计划、解密 key，建好后整体替换；调用路径只做一次原子读取，`Cost`、`Policy`、`Acquire` 不拿 `Reload` 用的锁。在途数和冷却按凭证 ID 保存，每把锁只在查表时短暂持有。
- **日志写入有期限**：`Run` 的每次写都有自己的期限（`FlushTimeout`，默认 10 秒），超时保留数据、记日志、退避后重试。
- **`Acquire`**：在没有冷却、没有过期（按时钟，不靠数据库变更）、在途数低于上限的凭证里挑负载最低的，对在途数做一次比较交换；拿不到返回 `ErrNoSlot`，gateway 跳过这个条目，不算尝试。在途数和冷却时间按凭证 ID 存在快照之外，重建快照不清零。
- **`Send`** 给每个失败标明上游是否执行过（`Failure.Executed`）：用 `httptrace` 的 `WroteRequest` 判断，请求写完之前失败是 `NotExecuted`（作用于出口）；写完之后，非 2xx 由适配器的 `Classify` 归类，其余（5xx、断线、超时）是 `Unknown`。请求路径和鉴权头只由调用格式定（Bearer 加 Anthropic 版本头），响应头里的对账键记在 `Attempt` 上，失败的响应也记。凭证被拒、额度用尽、被要求等待时，凭证按适配器给的时间（没有就按配置）歇着。suppliers 和 egress 不暗中重试 POST。
- **对账键**只取上游的响应头，由适配器的 `ChargeRef` 读：new-api 是 `X-Oneapi-Request-Id`；sub2api 是 `client:` 加 `X-Client-Request-Id`。对账只比较字符串是否相等。
- **`Done()`** 没有参数，幂等：重复调用、取消与结束竞争都只释放一次占位。
- **`RecordAttempts`** 在 billing 的结算事务里一次写入这个请求的全部尝试（`ON CONFLICT DO NOTHING`，用 `unnest` 一条语句），往返与尝试次数无关；已有的行不变。`RecordLostRequest`、`ResolveLostRequest` 同样只插入。
- **`CallPolicy.Cancels`**：适配器给默认值（new-api 传递，sub2api 不传递），quality 按端点实测的值覆盖它，规则见[计费的「客户端断开」](../../docs/domains/billing.md#客户端断开)。

## 对账

`internal/suppliers/internal/reconcile`。扣费记录由同步引擎在栅栏事务里经 `RecordCharges` 写入（一条记录读两次只存一次）；`Reconcile` 在平台范围的事务里做一遍：

1. **匹配**：一条语句，先占住只有这个对账键的那一个尝试，再把扣费记录归到它，尝试和扣费记录各有唯一约束，两个节点同时跑也只有一个成功。有两个以上尝试共用一个键时都不对上。已暂挂、已归到丢失请求或已确认未归属的扣费记录，尝试到了就改归到尝试，旧归属在同一条语句里撤销，所以归属的 CHECK 始终成立。
2. **判定**：只对估算完整（`cost_flag` 为 `complete`）的尝试。|实际 − 估算| ≤ 供应商自己的取整（quota 为 1，美元为 1e-10）+ 估算 × 相对容差（默认 1%）。实际偏高：端点改回"成本未知"（原因 `under_estimated`），退出路由；偏低：只在报告里列出。
3. **暂挂**：宽限（默认 30 分钟）内没有对上的新扣费记录改为 `held`；有键的尝试过了宽限仍没有扣费记录，报告里列出，只告警。人工处理经 `AssignToLostRequest`、`AssignUnattributed`，只对 `held` 的记录生效。
4. **扣费记录保留明细**：`charge_records.usage` 存每项的数量、记录声明了哪些项（`reported`，可以含声明为零的项，没声明的不在其中）、供应商给出但没有计费项的用量（`unknown`）和等级，因为只有声明了的项才能校正暂估。
5. **资金来源**：不是钱包的扣费记录（订阅）入库时存下来、不参与匹配、暂挂和事件，同时把供应商暂停（`needs_attention`，原因 `funding_not_wallet`，配置代次加一），重建快照后不再路由到它。
6. **有界**：一遍对账每种语句最多 `MaxBatches`（默认 20）批、每批 `Batch`（默认 500）行，没做完的留给下一遍，报告里 `More` 为真；等待中的行（`state='new'`、`state<>'matched'`、`state='pending'` 且有键）各有部分索引，代价随积压而不随历史增长；等待的尝试列表一批一批地列，`Config.After` 和报告里的 `Next`（按完成时间、请求、序号排序的游标）把它跨轮推进，列到末尾 `Next` 归零、下一遍从头开始，所以超过一批、始终对不上的尝试最终都会被报告一次。
7. **事件**：每次版本变化在同一事务里写一行 `charge_events`（扣费记录，版本，载荷），唯一键是（扣费记录，版本），重复运行不会重复；billing 按它幂等处理。没有消息队列时这张表就是事件的出口，billing 读它。

## 对账日志

进程崩溃后，清理要知道哪些尝试已经向客户端交付了内容、对账键是什么，才能按供应商记录收费（见[计费的「持有者与截止时间」](../../docs/domains/billing.md#持有者与截止时间)）。上游不接受调用方的幂等号，所以由本模块在请求进行中记下来：

- **何时追加**：进入 `Send` 时、拿到响应头里的对账键时、尝试提交（第一次向客户端写出内容）时，gateway 各调一次 `Journal`，追加一行到进程内缓冲：请求 ID、尝试序号、账户、端点、凭证、成本计划版本、`priced_at`、对账键、是否已提交。
- **怎么写**：后台每秒一条多行 `INSERT … ON CONFLICT`，在本模块的平台范围事务里，一个节点每秒一个事务，不在请求路径上。主键 `(request_id, attempt_seq)`；同一行只插入或补上对账键、已提交标记。写失败退避重试，不丢。
- **有界**：日志只对还没结算的请求有用。结算提交后（结算事务里已有对账键），gateway 调 `Settled`，这个请求还没写出的条目直接丢弃。一个请求的节点名额在它执行期间和结算后台重试期间一直占着，只在结算提交，或交给清理（结算永久性错误、到截止时间放弃重试）且执行器已停止上游、不再追加条目、不再重试结算、`JournalFlushed` 已关闭之后才释放（[gateway 的「并发、背压与共享状态」](../gateway/README.md#并发背压与共享状态)）。所以缓冲里只有占着名额的请求的条目，至多是节点名额 × 3 次尝试 × 3 行。日志持续写不进时，交给清理的请求名额不释放，节点占满后新请求得 503，这就是上界。表里的行在结算后 2 天删除。
- **什么时候清理**：只有终结了的请求的日志才可以删，终结由 billing 经 `EndRequest` 告诉我们（结算完成，或清理完成且校正期过去的时刻，不是写入尝试行的时刻：清理待定时也写尝试行，请求还在等校正），按"终结后 `JournalRetention`（2 天）"删，不按写入时间；没告知终结的请求，日志不论多旧都保留，因为清理要靠它恢复。选这种做法而不是由 billing 传"可以删的请求集合"，是因为保留期和测试都能留在本模块里，接口只有一个幂等的插入，供应商不去推断计费的结算状态；billing 的结算分支（feat/28-settle）合并时在它终结请求的事务里调用 `EndRequest`。
- **测试**：日志持续写失败，同时有请求结算永久失败：它们的名额不释放，节点占满后返回 503，缓冲始终不超过节点名额 × 9 行；日志恢复后条目落库、名额释放。请求仍在执行时它的日志已全部落库，名额不释放。结算一直暂时性失败、仍在后台重试时日志已全部落库，名额不释放。

## 决策与理由

| 决策 | 理由 | 不选 |
|---|---|---|
| 同步状态存成一份 jsonb，加格式号，不拆成列 | 引擎的状态整份读出、整份提交，字段随引擎演进；拆成列每加一个字段就要迁移，且读出时要拼回去。查询只需要 `next_due`，它单独成列。代价：不能用 SQL 查状态里的细节，要看时读 jsonb | 状态的每个字段一列：引擎一变就要迁移 |
| 栅栏是租约的 `lease_fence`，时钟只决定何时可以接手 | 两个节点的时钟差不能让它们同时写进去；写入由数据库里一个只增的数字挡住，与时钟、网络延迟、暂停无关 | 只靠 `lease_until > now()` 判断：被暂停的节点醒来时租约已过期，但这一刻它的判断与别的节点接手之间没有顺序 |
| 限速桶放数据库里，每个请求一个事务 | 多节点共享一个出口地址时，各自在内存里计数会让合计超过对方的限速，被封号的是预付的账号；管理面请求少，一次事务的代价可忽略 | 把名额均分给各节点：节点数一变就要重分，空闲节点的名额浪费 |
| 成本计划先存在 `sync_plans`，不立刻落进 `cost_schedules` | 端点行要有目录的 `model_id`，映射还没有；先如实保存，之后落表时按版本顺序取每个端点的最新一条 | 在没有端点时丢弃计划：映射做好后要重新同步才有 |
| 每秒批量的对账日志，不在发请求前单独写；只有标了已提交的尝试能让清理向用户收费 | 每节点每秒一个事务，崩溃窗口从整个请求时长缩到最近一次成功写入之后，计费的清理结算才走得到。日志看不到之后的尝试，所以只认已提交的：提交后不再换端点，已提交的必是最后一次 | 每次尝试先写一行：往返随尝试数增长 |
