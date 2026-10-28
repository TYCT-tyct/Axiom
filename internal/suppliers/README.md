# suppliers

供应商模块：供应商及其账号凭证、分组、模型、端点的表，适配器的契约和注册，以及调用路径上的接口（挑凭证、发请求、记尝试）。适配器、同步、对账的要求见[供应商](../../docs/domains/suppliers.md)和[计费的「供应商成本」](../../docs/domains/billing.md#供应商成本)，一次调用的编排见 [gateway](../gateway/README.md)。

> [Issue #25](https://github.com/TYCT-tyct/Axiom/issues/25) 分多个 PR 实现。已实现：表、适配器契约、注册表、启动检查、new-api 与 sub2api 适配器、同步引擎及其状态、租约和限速桶的持久化、同步的投影（分组、模型、端点、成本计划）、调用面（挑凭证、发请求、记尝试）和对账的写入，写在「代码结构」到「对账」；同步的调度（River，在 `serve` 里运行）已实现，写在「调度」；用真实生成测试端点（`TestEndpoint`、`RecordTest`，[#313](https://github.com/TYCT-tyct/Axiom/issues/313) 的 E3-5）已实现，写在「用真实生成测试端点」；管理接口见「管理接口」，事件投递在后续 PR。请求进行中的对账日志已删（「没有对账日志」）。代码与本文不一致时，在同一个 PR 里改本文。

## 代码结构

```
internal/suppliers/                 公开接口：Module()、CheckAdapters()、Service（调用面）、RecordAttempts 等、Reconcile、Admin 与 NewAdmin（管理接口）、RegisterSync（同步的调度）
internal/suppliers/internal/
  adapter/       契约：Adapter、Manager、CallQuirks、Failure、CallPolicy、Client、BucketSpec
  registry/      注册表：唯一同时引用所有适配器的文件，一个适配器一行；按 kind 查找
  client/        适配器唯一的出站入口：经 platform/egress、按声明的限速桶放行（进程内或数据库里的桶）
  newapi/        new-api 适配器（含 testdata/ 录制样本和测试）
  adaptertest/   每个适配器都要过的契约测试（只被测试引用）
  syncer/        同步引擎：只做决定，状态由 Store 整份读出、整份提交
  persist/       同步状态的持久化：租约与栅栏、Store、LoginStore、把状态投影到分组/模型/端点/成本计划
  schedule/      调度：周期扫描到期的供应商，用任务队列为每个供应商跑一轮引擎
  reconcile/     对账：匹配、判定、暂挂、事件
  store/         迁移、查询（sqlc 生成）
  removable/     `just adapters-removable` 的程序
```

`newapi/` 和 `sub2api/`（适配器，`testdata/` 里是录制并脱敏的样本）已有，`syncer/`（同步引擎）、`persist/`（引擎状态的持久化）、`schedule/`（任务调度）、`registry/`（适配器注册表）也有；与系统无关的路径和鉴权头（`wire`）是 `attempt.go` 里的一张表，不是包；挑凭证在 `Acquire`。

## 表

schema `suppliers`，迁移在 `internal/suppliers/internal/store/migrations/`（设计草稿里的 `store/migrations/` 按[总体架构](../../docs/architecture.md)的 `internal/` 规则改为此路径），版本表 `platform.goose_suppliers`。`00001` 是配置表；`00002` 是调用面和对账的表；`00003` 是同步的表；`00004` 是单位价值；`00005` 是管理接口用的（`supplier_models.first_seen_at`）；`00006` 是移除供应商的擦除（凭证状态 `erased`、对 `supplier_logins` 的 DELETE 授权）；`00007`、`00008` 是换算依据；`00009` 是凭证的每分钟请求上限；`00010` 是检测成本行的标记（`attempt_costs.test`，带默认值，沿用现有 INSERT 授权）；`00011` 在 `pg.Migrate` 提供的平台范围里把高于 100,000 的旧值降到上界；拒绝新写入的上限 CHECK 待旧节点退出后再加（[检查清单](../../CONTRIBUTING.md#检查清单)）。`00012` 将范围读取改为策略级 InitPlan，权限边界不变，表达式与可重跑方式见 [pg](../platform/pg/README.md#决策与理由)。迁移只增不改。

| 表 | 内容 | 键与约束 |
|---|---|---|
| `suppliers` | 归属（`owner_scope` 与 `site_id`、`account_id` 由 CHECK 保持一致）、`kind`、`origin`（`https://host[:port]`，小写）、`upstream_account`、原生单位与结算币种、管理出口、状态、`config_gen`、带符号的有限余额（`numeric` 的 NaN 和无穷被 CHECK 排除，倍率、阈值同样） | `UNIQUE (origin, upstream_account)` |
| `supplier_logins` | 账号凭证：`kind` 加一份加密值（密文、nonce、包裹的数据密钥、主密钥版本），`version` 供比较后再写 | PK `supplier_id` |
| `supplier_groups` | 上游的分组：`upstream_ref`、倍率、原始事实（jsonb）、是否选中、代号、是否仍存在 | `UNIQUE (supplier_id, upstream_ref)`、`UNIQUE (id, supplier_id)`、代号全局唯一且设定后不变 |
| `credentials` | 调用凭证：对方的 key ID、加密的 key、状态、冷却、并发上限、每分钟请求上限（1 到 100,000，可空）、出口；状态为"绑定中"时还没读到 key，"已清除"（`erased`）时 key 已在移除时清空，行保留，因为用它做过的尝试指向这一行 | `UNIQUE (supplier_id, upstream_key_ref)` |
| `supplier_models` | 分组里的上游模型名、映射到的目录模型（空为待映射）、格式、首次列出的时间（待映射队列按它排） | PK `(supplier_group_id, upstream_name)` |
| `supplier_aliases` | 供应商别名，分组可空（整个供应商有效） | `UNIQUE NULLS NOT DISTINCT (supplier_id, supplier_group_id, upstream_name)` |
| `endpoints` | 分组 × 模型：格式、成本状态（已知 / 未知 / 冲突）、成本计划编号、是否启用 | `UNIQUE (supplier_group_id, model_id)`；成本状态为已知必有成本计划编号 |
| `cost_schedules` | 端点的成本计划，`catalog.Schedule` 的规范 JSON，币种 `XXX`；`endpoints.cost_schedule_id` 指向当前的一版 | 只追加；`UNIQUE (endpoint_id, fingerprint)` |
| `attempt_journal` | 旧的对账日志（「没有对账日志」）：没有代码再读写，表按收缩规则留到下一个发布删 | PK `(request_id, attempt_seq)`；平台范围 |
| `attempt_costs` | 每次尝试的成本和对账键，billing 在结算事务里写；`usage` 在各项数量旁存 `physical_input`（OpenAI 上游报告的提示词总数，各采样相加；缺失表示上游没报），因为 OpenAI 的缓存读写可重叠，各项之和可大于提示词；`test` 标记检测的成本行（平台自己付，见「用真实生成测试端点」） | PK `(request_id, attempt_seq)`；`charge_record_id` 唯一；行带请求所属的租户，检测的行两列都为空 |
| `ended_requests` | billing 告知的请求终结时刻与到达终态的时间 | PK `request_id`；只插入；行带请求所属的租户 |
| `lost_requests`、`lost_request_resolutions` | 节点失联的请求，和节点带着真实尝试回来 | 只插入；行带请求所属的租户 |
| `charge_records` | 供应商的扣费记录：状态 `new/matched/held/assigned`、归属、版本 | `UNIQUE (supplier_id, charge_ref)`；归属和状态的 CHECK；平台范围 |
| `charge_events` | 给 billing 的事件，每个版本一行 | `UNIQUE (charge_record_id, revision)`；平台范围 |
| `sync_states` | 每个供应商一行：引擎的整份状态（jsonb，`format` 记格式）、`version`（引擎的栅栏）、`next_due`、租约（`lease_holder`、`lease_fence`、`lease_until`） | PK `supplier_id`；`lease_holder` 与 `lease_until` 同有同无；平台范围 |
| `sync_events` | 引擎每一步产生的事件，只插入，事件按名字存 | PK `(supplier_id, version, seq)`；平台范围 |
| `sync_plans` | 引擎算出的成本计划的每个版本（端点是「分组/模型」文本），只插入 | PK `(supplier_id, version, seq)`；平台范围 |
| `unit_values` | 供应商的单位余额价值：1 单位余额值多少结算币种，管理员确认的值、操作人、理由 | 只插入，最新一行生效；`value` 在 (0, 1000] 且小数不超过 15 位；行级安全随所属供应商可见 |
| `rate_buckets` | 供应商的限速桶：窗口起点、已用次数、封到何时 | PK `(scope, name)`；平台范围 |

- **授权的偏差**：设计里这几张表只有对账角色能改，那个后台任务角色还没有（security.md 的角色表），所以应用角色现在能改 `attempt_costs` 的 `charge_record_id`、`state` 和 `charge_records` 的状态与归属列，别的列、DELETE 都没有；`attempt_journal` 的授权还在（连同它的 DELETE），但没有代码用它，删表时一起删；角色隔离随后台任务角色加入。
- **行级安全**：`suppliers` 的策略用本行的站点、账户与 [pg 的 InitPlan 范围参数](../platform/pg/README.md#决策与理由)比较，平台的供应商只有平台范围看得见；其余各表不存归属列，只存 `supplier_id`，策略是"这个供应商对本事务可见"，所以插入也挡住"挂到看不见的供应商下"，也不会有与父对象不一致的标签。
- **复合外键**：凡同时存 `supplier_id` 和 `supplier_group_id` 的表，用 `(supplier_group_id, supplier_id) → supplier_groups (id, supplier_id)`，分组与行不可能属于不同供应商。别名的分组可空，空表示整个供应商。
- **授权**：应用角色对配置表没有 DELETE，只有 `supplier_logins` 有（移除供应商时删封存的登录，没有外键指向它，迁移 `00006`）；旧的对账日志还留着 DELETE，但没有代码用它（「没有对账日志」）；UPDATE 按列授予，不含 `id`、`supplier_id`、归属列、`kind`、`origin`、`upstream_account`：身份和凭证发往的地址不能改。
- **密钥**：表里没有明文列。加密值写进 `ciphertext`、`nonce`、`wrapped_dek`、`key_version` 四列，数据库只检查字段齐全和长度（nonce 12 字节、密文不短于 16 字节、包裹的数据密钥非空、版本号合法），**不能**分辨密文和别的字节：任何满足长度的明文也写得进去。加密由写入边界保证（只把 `secret.Sealed` 交给查询），值是否真实、是否属于这一行由 `secret.Open` 验证。`secret.Location` 的行 ID 在加密前就要知道，所以 `credentials.id` 由调用方生成，不用默认值。
- **跨模块引用**：`model_id` 是目录的 ID，没有外键；`endpoints.cost_schedule_id` 与 `cost_schedules` 用（成本计划，端点）复合外键。

## 适配器

- **契约**（`adapter`）：`Adapter` 有 `Kind`、`Buckets`、`Detect`、`Open`、`Calls`；`Open` 返回只读的 `Manager`（账户、报价、key 状态、按对方 ID 读 key 原文、扣费记录分页；每笔记录带标准化用量 `catalog.Usage` 和"已报告的项"集合，没报告和报告为零不同，暂估才能按项、按请求锁定的单价校正）；`Calls` 返回 `CallQuirks`（`Formats`、`ChargeRef`、`Classify`、`Policy`）。写能力以后用单独的可选接口加入，能力就是实现了什么。
- **接入**（`adapter.Onboarding`，可选，一个适配器要能在后台接入才实现）：`LoginFields(captcha)` 说这个类型的登录用哪几个成员（new-api 是一个个人访问令牌，sub2api 是令牌或邮箱加密码），`NewLogin` 把给定的成员检查后做成要封存的登录，`NativeUnit` 说余额的单位（new-api 的 `quota`，sub2api 的 `usd`）。缺成员、多给成员都是 `LoginError`，只带成员名，不带值。
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
- **价格**：默认安装里普通用户看不到价格，模型的成本一律是空（未知），不上架，直到人确认底价；`adapter.ScaleSchedule` 把目录官方价乘分组倍率做成草稿（币种写 `XXX`，单位是对方的美元），用的是 `catalog.Schedule.Scaled`，与售价同一个取整。能公布价格的 `channels/available` 尚未读取（实例里关着，没有样本）。
- **调用面**：`anthropic` 分组服务 Messages，`openai` 分组服务 Responses 与 Chat；订阅分组、高峰分组、其他平台不服务。对账键是响应头 `X-Client-Request-Id` 加 `client:` 前缀，等于扣费记录的 `request_id`（实测）；对方自己生成，不读我们传的。`Classify` 只读错误代码，不留对方的文字；`Cancels` 为否（对方把上游读完并全额扣费，实测）。
- **扣费记录的扫描**：位置（`scanState`）是适配器自己的，模块当作不透明的字节，每一页连同它存下；空位置是第一次扫描，只记下"从现在起"（水位），本次返回 Done；之后每遍从（水位 − 5 分钟）读到（现在 − 30 秒），每天再读一遍最近 24 小时（补扫，不动水位）。`/api/v1/usage` 只认日期，所以一遍按 key 逐把读，新的在前、按页翻；起止日取窗口两端的日期（跨午夜两天都读），每页 200 条（每条还带着 key、用户和分组，页太大会超响应上限），一把 key 在短页或最旧一条早于窗口起点时结束，全部 key 结束才算读完。记录的用量映射到不重叠的计费项（输入、输出、缓存读、5 分钟和 1 小时缓存写），**只有记录里写了的项算已报告**；非 token 计费（按次、图片）带 `UnknownUsage`；`billing_type` 不是 0 的不是钱包。记录里的 key 原文、用户、地址一概不读。
- **未验证**：key 的重置时间字段（`reset_*_at`）没有样本；分页 `total` 是否精确；订阅分组的记录形态。

## 同步

`syncer/` 是每个供应商一份的同步引擎，只做决定，不自己存任何东西：状态（`State`）由 `Store` 整份读出、整份提交，每一步的事件、成本计划版本和扣费记录与状态在同一次提交里，提交带读到的版本（栅栏），别的写入者先提交则这一步整个作废（`ErrFenced`）。租约、数据库事务和对应的表是下面的「持久化」，River 的调度是再下面的「调度」。

- **轮次**：`Round` 按顺序执行到期的任务（余额 1 分钟、目录 5 分钟、key 状态 5 分钟、扣费记录 2 分钟，默认值在 `Defaults`）。每个任务各提交一次。
- **失败**：网络错误和 5xx 指数退避（1 分钟起、上限 30 分钟、可加抖动），期间不调用，别的任务照跑，成功即清零；桶不够（`ErrBucketsFull`）不算失败，到 `Blocked.Until` 再来；响应认不出（`ErrUnrecognised`）只停这个任务，什么也不写，发事件；**鉴权被拒（`ErrAuthRejected`）停掉这个供应商的全部轮询**，状态"需处理"，发一个事件，之后的轮次什么也不调用；人换上新的登录后它从当时重新开始（「持久化」的「登录被替换时唤醒」）。
- **变化即事件**：目录规范化后与上次比较，只把差异写成事件（组、模型的增减，倍率变化，成本计划新版本）；同样的响应读两次没有新事件、新计划。成本计划 = 对方公布的价，或人确认的底价乘当前倍率；指纹变了才写新版本。路由要用的变化都有事件：组的倍率、组的事实（平台、订阅类型、高峰、状态）、模型的格式和能力限制（`Without`，快照里完整保存，没读到的组也保留）、资金来源、余额越过预留线和回升到告警线之上；相同的响应不发事件。
- **扣费记录**：位置是适配器自己的（见各适配器一节），引擎一概不读：第一次调用不带位置，每一页连同返回的位置在一次提交里存下，被限速、出错、重启后从这个位置接着读，适配器说 Done 即一遍读完；记录按对方的请求编号去重。资金来源不是钱包的记录让供应商进入"需处理"。
- **可路由**：路由快照（`ListRoutable`）取的是：供应商 `active` 或 `low_balance` 且**余额大于 0**（余额没读到过的也不进来，与"单位价值确认之前不进路由"同一原则；新接入的供应商在第一次余额同步之前最多约 1 分钟不可路由，节点此时报"not ready"是预期的）、已确认单位价值、分组被选中、端点启用且成本已知、至少一个 `active` 且有密文的凭证。"模型已映射"由 `syncer.MapUpstream` 判定：先看供应商别名（调用方从 `supplier_aliases` 查出），再用目录快照的内部识别查公开和内部名字，映射到的模型必须是上架的，退役或目录不认识的名字、带 `:free` 这类后缀而没有别名的名字都是未映射（`Mapping.Mapped` 为假），端点不生成，不进路由；不带日期的名字只是候选（`Mapping.Unverified`），M1 没有真伪检测，候选的端点照常接流量。持久化的投影（见「持久化」）每一步都用它写 `supplier_models.model_id`，并据此决定生成哪些端点；端点被挡在外的原因（`syncer.Reason`）写在端点行上。资金是不是钱包不在这条线上：一笔不是从钱包扣的扣费记录会让供应商停为"需处理"。余额低于告警线发一次 `BalanceLow`，回升后才会再发；余额越过 0 发 `BalanceCrossed`，与上面的门槛同一条线。按请求的预估成本过滤、余额低时降权、余额不足的失败让整家歇着，都尚未做（M2）。
- **移除**：`Admin.Remove` 当场完成，一个事务：状态直接是 `removed`、`config_gen` 加一（`ListRoutable` 不再取它，下一次 `Reload` 起不接新尝试，正在跑的一轮同步作废）；删 `supplier_logins` 的那一行；把它全部凭证的 `ciphertext`、`nonce`、`wrapped_dek`、`key_version` 置空并标为 `erased`（行保留：`attempt_costs` 的外键指向凭证）；要吊销的 key 的清单就是这些 `erased` 凭证行的 `upstream_key_ref`（`ListCredentialsOf` 不按状态过滤，后台详情里看得到），由人到对方后台吊销（M1 不对对方做写操作）；审计记录的 `detail` 只放 `keys_to_revoke_count`，不列 ID，因为 `audit_log.detail` 上限 8192 字节，一家供应商的 key 多了列出来会让移除的事务回滚、永远删不掉密文。不等在途尝试，它们手里有 `Reload` 时读进内存的 key，结算写成本用的外键行还在；不再读一次扣费记录，对账没有跑，读了也没人用。已 `removed` 的再移除返回现状；`removing`（早先的版本留下的）再移除就完成它。排队的同步任务取到租约后读到 `removed` 就结束（见「调度」），打开适配器之前不读登录。引擎不再带移除的步骤：`State` 里 `Removal`、`GraceUntil`、`RescanFrom`、`RemovalAt` 等成员留着，只为读得了已存的状态。
- **尚未做**：按需的"无逐笔记录"判定、成本低估下架（随对账）。

## 持久化

`persist/` 把引擎的 `Store` 和适配器的 `LoginStore` 接到数据库上。一个节点对一个供应商 `Acquire` 一次租约得到一个 `Session`，一轮同步（`Round`）用它的 `Store()`，登录令牌的比较后再写用它的 `Logins()`，轮后 `Release`。

- **租约与栅栏**：取租约是一条 `INSERT … ON CONFLICT DO UPDATE … WHERE 租约已空或已过期`，每次取到 `lease_fence` 加一；租约还有效就取不到（`ErrLeaseHeld`，不是故障：另一个节点正在同步它）。每一次写（提交一步、`Swap`）在自己的事务开头先 `SELECT … FOR UPDATE` 锁住租约行，要求 `lease_fence = 手上的`，再锁供应商行（`FOR NO KEY UPDATE`，同 `UPDATE` 非键列的锁，不挡别的表往它上面写子行）并要求 `config_gen` 还是取租约时的，两者持到事务结束；任一不符返回 `ErrFenced`，整个事务回滚。接手租约和改 `config_gen` 都要等这个事务结束，所以通过的写排在接手或改动之前，不通过的绝不会落在它之后：租约过期后被接手的旧持有者，哪怕晚了很久才写、哪怕写到一半对方才来接手，也写不进去。只在语句里用快照读栅栏不行：`UPDATE … FROM` 只锁目标行，等锁期间别的事务接手，它仍会按旧栅栏写入。**锁的顺序**固定为租约行 → 供应商行 → 登录等依赖行；改凭证的代码要先加 `config_gen`（锁供应商行）再写登录，不能倒过来，否则会和 `Swap` 死锁。换登录时复位同步状态（见「登录被替换时唤醒」）因此要最早做：它锁的是状态行（没有时先建一行），排在供应商行之前。版本（`version = 读到的`）仍在 `UPDATE` 里比较。时钟只决定租约何时可以被接手，不决定写入放不放行，节点之间时钟差几秒只会让接手早一点或晚一点，不会让两个节点都写进去。每次提交把租约续到「提交时刻 + `LeaseTTL`」，一轮最多 50 页，每页一次提交，所以一轮可以比 `LeaseTTL` 长；停止提交的节点在 `LeaseTTL` 后失去租约。
- **一步的提交是一个事务**：先 `hold`（上一条的两把锁），再写状态、这一步的事件（`sync_events`）和成本计划（`sync_plans`）、扣费记录（`reconcile.Ingest`）和对供应商的投影，要么全在要么全不在。栅栏挡住时（`ErrFenced`：租约被接手、配置代次变了、版本不符）没有写任何东西。配置代次变了就整步作废，包括因旧凭证被拒而要写的「需处理」，以免它把已经改好的供应商再次停在永不同步。事件按名字存（`Kind.String()`），不按常量的序号。
- **状态的格式**：`state` 是引擎的 `State` 的规范 JSON，`format` 记它的格式号，读不认识的格式或多出来的成员就失败而不是猜。新版本写的状态旧版本也读得了，反过来也一样（滚动发布时两个版本同时运行），所以删掉不再用的成员会让已存的状态读不了：`Removal` 等移除的成员不删，`TestTheStoredStateIsWhatAnOlderNodeReads` 钉住成员的集合。`Task`、`Status`、`RemovalStep` 的常量只在末尾增加。第一次同步的供应商没有状态，读出来是零值的 `State`。
- **投影**：每次提交把最后读到的余额和观察时间写到 `suppliers`，并按引擎的状态写 `status`：正常时 `connecting → active`、`active ↔ low_balance`，需处理写 `needs_attention`，引擎不再写 `removing`、`removed`：移除是管理操作写的状态（见「移除」）。人暂停的 `paused` 和对账暂停的 `needs_attention` 不被「正常」覆盖，移除不会被改回需处理。路由的快照看这个状态。配置代次（`config_gen`）不投影：引擎的代次只管它自己的轮次，`suppliers.config_gen` 由改凭证、暂停、移除的代码加一，`hold` 用它判断一轮是不是在凭证被换之前开始的。
- **清单的投影**（`project.go`，与状态同一次提交）：引擎最后读到的分组和模型也落到表上，`routing` 与后台读的是这些行。同一步里先写 `supplier_groups`（名字、倍率、事实、`last_seen_at`），再写 `supplier_models`（每个上游名字，`model_id` 由 `MapUpstream` 按别名和目录定，映射不上就是 NULL，那就是待映射队列；别名是人或运营 agent 的决定，投影只读、不建），最后写 `endpoints` 和 `cost_schedules`。每条语句都先读同一事务里的行，一样就不写：同一份清单读两次，五张表一行都不动（`sync_events` 以外零写入，测试用 `xmin` 核对）；`first_seen_at` 只在插入时由数据库取、之后不改（待映射队列按它排序），`last_seen_at` 因此是"这个分组最后一次被改动"，不是"最后一次出现在清单里"。
  - **端点**：键是 `(supplier_group_id, model_id)`（`UNIQUE`，一个分组的同一个模型只有一个端点）。只有映射到上架目录模型的、且拿到了成本计划的模型才有端点，上游名字和格式跟着清单走；一个分组的两个名字指向同一个模型时，端点是带日期的那个（名字本身是版本的证据），都不带日期时取名字排序里的第一个，所以选谁与清单读到的顺序无关。`enabled` 只在插入时取默认的真，之后同步从不写它，人停用的端点不会被重新打开；`model_id` 也不改（改名在 `upstream_name` 里）。
  - **成本计划**：`sync_plans` 里按「分组/模型」和指纹存的版本，投影时按指纹取回计划本身（引擎的状态只存指纹），写进 `cost_schedules`（只追加，`UNIQUE (endpoint_id, fingerprint)` 让同一版只有一行），再把 `endpoints.cost_state` 置为 `known`、指向那一行。没有计划就没有端点；计划消失（模型不再公布价、或人撤回了确认）时端点是 `unknown` 加原因，行和 `enabled` 都留着，计划回来时还是同一个端点和同一行成本。`cost_state_reason` 是投影自己写的几种原因之一（未映射、未上架、不可调用、成本未知、清单不再提供）时照常改写，对账写的 `under_estimated`（估算偏低）不动，除非出现新的一版计划——新版本就是新估算。
  - **不删除**：清单里没有的分组和模型标 `present=false`，端点置为成本未知，原因 `the listing no longer offers the model`；回头时还是原来那些行，`first_seen_at` 不变，成本计划也是原来那一行。应用角色本来就没有 DELETE。
- **`next_due`**：提交时从状态算出，是各任务到期时间和退避结束时间中最早的一个（已停的任务不算；状态不是正常，即需处理，算作永不；已移除的供应商不在 `syncable` 里，扫描不选它），调度据此找到该同步的供应商。
- **登录被替换时唤醒**：因登录被拒停在「需处理」的供应商，状态不是正常、`next_due` 是永不，扫描再也不选它。`Admin.ReplaceLogin` 在同一个事务里先锁住并读出它的状态（没有时 `EnsureSyncState` 先建行，`LockSyncState` 再 `FOR UPDATE`：不锁就可能读到之后又有一步提交进来，水位被写回旧值），再换成一份能接着跑的：状态正常、原因清空，每个任务的到期时间、退避、失败次数和 `halted` 清零——到期时间归零即立刻全部到期——余额、资金来源、清单读到的分组和 key、每个任务的 `lastOK`、`scan`、`scanOpen`、`through` 原样保留：扣费记录的位置因此不重读，已读过的清单也仍然由之后的提交投影（`readsListing`），人刚补的别名不必等目录任务再成功一次就有端点；`version` 加一（`sync_events`、`sync_plans` 按它命名，换过的状态是新的一版），`next_due` 写成当时的时刻，租约清空并把 `lease_fence` 加一，握着旧登录的节点此后的提交和 `Swap` 都被栅栏挡住。从没同步过的供应商没有状态行可锁，唤醒在锁供应商行之前先建一行并持有它的锁（顺序仍是状态行 → 供应商行），首次同步若先持有该行，唤醒等待它提交后再复位；否则首次同步在唤醒之后读取新状态，旧持有者的后续写入由栅栏拒绝。
- **`LoginStore`**：`Load` 用供应商 ID 为位置打开密文（`suppliers.supplier_logins`、供应商 ID、`login`），整条记录挪到别处就打不开；`Swap` 封存新值后在一个事务里先 `hold`（租约栅栏还是手上的、`config_gen` 还是取租约时的），再要求版本没变地写入，任一不符返回 `ErrStaleLogin`，登录令牌不会被一个已失去租约的节点或一个在凭证被人换过之后才到的刷新覆盖。
- **限速桶**：`rate_buckets` 的键是（谁，桶名）。适配器的 `BucketSpec.Per` 为按 IP 的，「谁」是 origin 加出口，同一站点的所有供应商共用；为按账号的，是供应商 ID。一个请求声明的桶在一个事务里计数：建桶和封桶都是 `INSERT … ON CONFLICT DO UPDATE`，按（谁，桶名）排序：新桶由插入持有，已有的桶由冲突时的更新持有，所以建桶这一步结束时已按键序持有声明的全部桶，之后的 `FOR UPDATE`（也按键序）不再取新的锁。若建桶用 `DO NOTHING`，请求会持着新桶去等别人持有的已有桶，而那个请求正要新桶，两条语句各自排序也躲不开这种死锁。所以两个请求以相反的顺序声明同样的桶，无论桶是否已存在，都只是互相等待；测试覆盖全部已有、全部新建、已有加新建三种状态；检查、再一起计数，所以全取或全不取；429 把所占的桶封到 `Retry-After` 对所有节点生效。代价：每个对外请求多一个数据库事务；管理面的请求每个供应商每分钟几十次，调用面的请求不走这里。窗口从第一个请求的节点的时钟开始，节点间几秒的时钟差对几十秒到几十分钟的窗口没有影响。
- **尚未做**：事件的投递（`sync_events` 是出口，投递随后）；在发出会用掉一次性令牌的请求（sub2api 的刷新）之前先续一次租约以缩小窗口（设计草稿 #25 §4）：现在只靠 `Swap` 的栅栏拒绝失去租约的写回，令牌已经用掉时，下一个持有者读到的是旧版本，刷新被对方拒绝后改用密码重新登录。

## 调度

`schedule/` 把引擎接到任务队列（`platform/pg` 的 `Queue`，River）上，`suppliers.RegisterSync` 在 `Start` 之前把它登记到队列，`serve` 调用它。

- **扫描**：周期任务 `suppliers.sync_sweep` 每 15 秒一次（启动时先来一次，只有队列的领导节点插入）。它在一个事务里读出该同步的供应商，并为每一个入队一个 `suppliers.sync` 任务：状态是 `connecting`、`active`、`low_balance`，并且从没同步过，或 `next_due` 到了而且没有节点持有租约（`ListSyncDue`，最久的先来，一次最多 200 个）。一个供应商的状态变成需处理、暂停、移除后就不再被扫到；这三种状态之外的名单只在 `schedule` 里定义一处（`syncable`），扫描和任务共用。
- **任务**：`suppliers.sync` 带供应商 ID。按参数唯一，在等待、运行、重试、定时这几种状态下同一个供应商只有一个，**完成的不占位**（River 默认完成的任务也算重复，要保留 24 小时，那样一个供应商一天只能同步一次）；失败不由队列重试（`MaxAttempts` 为 1），下一次扫描在它再次到期时补上，到期时间由引擎的退避决定。
- **一轮**：任务取供应商的租约（别的节点持有就什么也不做，正常结束），按供应商的 `kind` 找适配器，用 `SharedLimiter`（按地址的键是 origin 加出口，按账号的是供应商 ID）和 `platform/egress` 做出站客户端，再让引擎跑一轮；适配器的 `Open`（要登录）推迟到第一个任务要用时，失败就记为那个任务的失败，所以被拒绝的账号、对方宕机都走引擎已有的处理（停轮询、退避），而不是任务出错；一轮里只登录一次。轮后归还租约。租约被接手（`ErrFenced`）也正常结束：这一步什么都没存下，接手的节点继续。
- **取到租约后再核对状态**：任务排队之后供应商可能被暂停、移除或停为需处理，扫描时的过滤管不到这些。取到租约就读到当前状态，不在 `syncable` 里的任务直接结束（还租约，不打开适配器、不发请求）。取租约之后才发生的暂停会把 `config_gen` 加一，这一轮的提交因此作废（见「持久化」的 `hold`）。
- **接不上的供应商也是失败**：出口是本节点没有的（现在只有 `direct`）、没有这种 `kind` 的适配器、origin 不能用，这些在打开适配器的地方失败，同样记为每个任务的失败并退避（同时写一条错误日志），而不是让任务出错而不动 `next_due`：那样这个供应商永远是最久到期的，每次扫描的 200 个名额都被这类供应商占去，到期得晚一点的正常供应商永远排不到。
- **被限速时等着接着读**：适配器只在 `Manager` 活着时记得清单（key、报价）读到哪一页。一个任务被限速结束时，引擎记下它能继续的时刻，直到它成功或以别的方式结束才忘掉；`Engine.Blocked` 报告所有还在等的任务里最早的一个，所以恢复时间不同的两个任务（报价等 T₁、key 清单等 T₂）会依次等到，较早的那个成功后，等 T₂ 的 Manager 仍被保留。打开适配器（登录）被限速也是这样：被限速的 `Open` 不当作这一轮的失败缓存，等限速重开后重新打开，其他失败仍一轮只试一次。这个时刻在 `maxBlockedWait`（4 分钟，短于租约的 5 分钟，因为上一次提交刚续过租约）之内、也在这个任务开始后的 `roundsBudget`（8 分钟，短于队列对一个任务的 10 分钟上限）之内（预算也按当前时间独立检查，限速给出的时刻已经过去时不会一直循环），任务就持着租约和同一个适配器等到那时再跑一轮，直到不再被限速。这样 9,050 个 key（91 页，每个窗口 90 次请求）在两个窗口里读完，而不是每个任务都从第一页开始。代价：被限速的供应商占着一个队列工人最多 8 分钟（一个节点同时在同步队列上做 4 个）；超出的部分留给下一个任务，它的适配器是新的，从头读。跨任务、跨节点保留进度需要把进度存进状态，适配器还没有这个接口，留给后续。
- **主密钥**：`serve` 要 `AXIOM_MASTER_KEY_FILE`（格式和保管见[platform 的「凭证加密」](../platform/README.md#凭证加密)），缺失、读不了或格式不对就不启动，错误不含路径和内容。同步用它打开登录令牌。本机的 `just up` 由 `deploy/compose.yaml` 的 `keys` 服务在卷里生成一次（属主是程序的用户、权限 0400，`just down -v` 会连它一起删），部署要把它放在受保护的文件里并另有备份。
- **关停**：`serve` 先停监听，再停队列（在跑的一轮最多等 `AXIOM_SHUTDOWN_TIMEOUT`，到期取消；被取消的一轮已提交的页保留，租约到期后由别的节点接着读）。队列启动时用的 context 不决定任务的生死：`pg.Queue.Start` 内部与它脱钩，River 否则会在收到退出信号的那一刻就取消在跑的任务（它的工作 context 继承启动 context），早于监听排空和 `Stop` 的等待，可能打断已用掉刷新令牌、还没存下新令牌的过程。任务的取消只由 `Stop` 的期限决定。节点崩溃（不是优雅停机）则任务停在运行中，同一个供应商的新任务因唯一性入不了队，直到队列把它当作丢失（运行满 15 分钟，即一次任务的上限加 5 分钟，River 默认是 1 小时），那时它被放弃（`MaxAttempts` 为 1），下一次扫描补上；租约在 5 分钟后就能被接手，但接手要靠这个任务。
- **时间**：租约 5 分钟（每次提交续期），一次任务最多 10 分钟，扫描间隔 15 秒、一次最多入队 200 个，按到期先后：这是扫描的节奏和批量，不是开始同步的时间保证。任务实际何时开始，还看队列的积压和工人（一个节点同时在同步队列上做 4 个，扫描任务自己也用这个队列）的占用，被限速等待的任务会长时间占着工人；接不上的供应商退避后不再占位。这些是代码里的默认值，不是配置；要改成后台设置时一起做。
- **入队与业务写入同事务**：扫描在读出到期供应商的同一个事务里入队；`Queue.InsertManyTx` 是通用的原语，测试覆盖回滚时任务不存在。每一轮的"业务写入"是引擎自己的提交，它们不入队后继任务（见下表）。

## new-api 适配器

`newapi/` 实现 `adapter.Adapter`。事实来自 new-api 的源码，并在运行 v1.0.0-rc.44 的本地实例上核对（Issue #25 的两条调研评论）；`testdata/` 是脱敏后的录制应答，测试回放它们。

- **识别**：`/api/status` 有应答、每个应答带 `X-Oneapi-Request-Id` 才算 new-api；`/api/user/login/encryption-key` 存在是 1.0，不存在是 0.13（版本字符串可能是空的，不信它）。同时读出登录是否要验证码。
- **账号凭证**：1.0 的个人访问令牌（PAT）加过期时间，`EncodeLogin`/`DecodeLogin` 在封存前后转换，明文只在这两个函数里出现，拒绝未知成员；令牌用 `Authorization: Bearer`，不会过期时没有要写回的状态。**0.13 不接入**：它要 cookie 或访问令牌加 `New-Api-User`，没有录制样本，也没在本机跑起来验证，所以只识别、`Open` 对非 PAT 的凭证报"凭证被拒"。以后接入是增量。
- **读取**：余额是 `/api/user/self` 的整数 `quota`，单位提示是 `/api/status` 的 `quota_per_unit`；有生效中的订阅就是"资金来源不支持"（`/api/subscription/self`）。key 列表按页读，状态 1 到 4 对应正常、停用、过期、额度用尽，认不出的状态停下这个任务。key 原文按对方 ID 读（`POST /api/token/:id/key`，ID 必须是纯数字才会进路径），占关键桶。
- **目录和成本计划**：分组来自 `/api/user/self/groups`（倍率明确写着 `auto` 的没有自己的倍率，不收；倍率缺失、为 null 或不是数字是"认不出"，不当作要略过的分组），每组的模型来自 `/api/user/models?group=`，价格来自 `/api/pricing`。成本计划是 `catalog.Schedule`，币种 `XXX`（原生单位是每 token 多少 quota，不是钱）：输入 = `model_ratio × 分组倍率`，输出 = 输入 × `completion_ratio`，缓存读 = 输入 × `cache_ratio`（缺省 1），缓存写 = 输入 × `create_cache_ratio`（缺省 1.25），一小时缓存写 = 缓存写 × 1.6；用精确分数算，向上取整到 15 位小数，所以价格不会低于对方的。按次计费、`tiered_expr`、倍率缺失、价格超过上限的模型没有成本计划（成本未知）；`image_ratio` 显式且不为 1 的模型不声明图片能力（`Model.Without`）。推理没有单独的价，`Cost` 按输出价算。
- **限速桶**：`global`（每个 `/api/*`）90 次 / 180 秒，`critical`（读 key 原文）5 次 / 1200 秒，各是对方默认的四分之一。
- **失败归类**（`Classify`）：只认 `type` 为 `new_api_error` 的错误，同样的状态码没有这个类型是渠道上游透传的，不当成我们的 key 或余额的问题。403 `insufficient_user_quota` 是供应商余额不足（作用于供应商）；401 且 `code` 为空是凭证被拒（作用于凭证，透支用尽和吊销同形）；403 `pre_consume_token_quota_failed` 是凭证额度用尽；503 `model_not_found` 是没有可用渠道（过载，作用于端点）；429 是限流，等待时间读 `Retry-After` 的秒数或 HTTP 日期两种形式，负数、过去的日期和认不出的值算没给，适配器共用 `adapter.RetryAfter`（sub2api 同样认两种形式；两小时的暂停上限在 `Router.Block`，防止上游的错误时钟把路由停太久），但执行状态不确定（空正文既可能是 new-api 自己的限流器，也可能是没读到正文，渠道上游或状态码映射也可能在请求执行后给出 429）；400 是请求格式错，执行状态不确定（渠道错误可以被映射成 400）；其余 5xx 和认不出的是上游错误或未知。只有对方在执行前就拒绝的才是 `NotExecuted`。对账键是响应头 `X-Oneapi-Request-Id`。
- **管理接口的错误**：401/403 带 `code` 的按固定清单报"凭证被拒"（缺权限时带上权限名），200 加 `success:false` 和认不出的结构报"认不出"，错误里没有对方的文字。
- **成功应答缺成员不是空结果**：`{"success":true,"data":{}}` 这类缺少必需成员（列表、总数、数量、详情）或为 null 的应答报"认不出"，不当成没有 key、没有分组或没有扣费记录：否则对方改了结构，水位会被推进、同步结果被清空、端点被下线。
- **清单可以续读**：key 列表和报价每页或每组一个请求，可能多于一个窗口的名额（9,050 个 key 是 91 页，桶是 90 次）。请求因没有名额被拒时 `Manager` 记下读到哪里，同一个 `Manager` 再调用就接着读，读完或超过 10 分钟就丢弃；调用方在重试期间保留同一个 `Manager`（`schedule` 在一个任务内做到，见「调度」）。

### 扣费扫描

`/api/log/self?type=2` 新的在前、按偏移翻页。翻页期间写入的记录只会出现在最前面，把后面的往后推：后页重复读到前页的末尾，不会漏，所以按请求编号去重、页码可以跨调用保存。`Manager.Charges` 每次读一页，位置 `ScanState` 是适配器自己的 JSON（版本 1），由同步引擎和这一页的记录在同一个事务里保存：

- **一遍** 读固定的 `[start, end]`（服务端按秒级时间戳过滤），读到不满一页（含恰好整页之后的空页）为止。前进遍：`start = max(水位 − 5 分钟, 开始扫描的时刻)`，`end = min(水位 + 跨度, 现在 − 30 秒)`，读完把水位升到 `end`，水位只升不降；水位之后不足一分钟时不发请求，返回空页和 `Done`。每天一次的补扫遍读最近 24 小时，不动水位，用来拿比重叠回扫更晚到的记录；它从"上一次补扫覆盖到的位置往前一天"开始，所以每一段时间在一天之后还会被再读一遍，补扫晚了几天也不留空隙；晚到不超过一天的记录，哪怕属于已被补扫覆盖过的一段，也被下一遍补扫读到（前进遍的 5 分钟重叠只管更近的晚到）。
- **窗口自适应**：跨度默认 1 小时；一遍超过 20 页，下一遍减半（最短 1 分钟）；连续三遍短，加倍（最长 1 天）。
- **限速**：桶没名额或收到 429（`client` 封住所占的桶）时返回错误且位置不变，下次从同一页继续，不算失败。
- **重启、丢状态**：每次调用都可以从保存的位置重新开始，重复读到的页同样的内容；没有位置（第一次）从现在开始，不读连接之前的记录；读不懂的位置报"认不出"并保持原样。
- **记录**：`Ref` 是记录的 `request_id`（与响应头同值），`Key` 是 `token_id`，`Amount` 是整数 `quota`，`Usage` 是 `prompt_tokens` 为输入、`completion_tokens` 为输出。`Reported` 只在没有缓存读写 token 时含输入，**从不含输出**：`prompt_tokens` 含不含缓存 token 随用量的格式（OpenAI 含、Anthropic 不含）而不同，记录里看不出是哪种；`completion_tokens` 含推理 token 且记录不拆分，当作输出项校正会让推理被算两次（一次在输出里、一次在没有校正的推理项里）。这些记录只剩金额能校正。没有请求编号、key、整数金额或读不懂的记录让这一遍失败而不是跳过。

### 出站入口（`client`）

`client.Client` 实现 `adapter.Client`：地址是创建时给的 origin 加适配器给的路径（路径必须以单个 `/` 开头，所以改不了主机），请求经 `platform/egress`，响应正文上限 8 MiB。`Limiter` 是进程内的固定窗口计数：请求声明的桶要么全部取到名额，要么一个都不取；429 把请求所占的全部桶封到 `Retry-After`（new-api 的 429 没有正文，认不出是哪个桶），并返回 `adapter.Blocked`（包装 `ErrBucketsFull`）。桶名在入口去重：`Limiter.Take` 与 `SharedLimiter` 的 `Take`、`Block` 都把重复的名字合并成一个，所以一次请求重复声明同一个桶只算一次，共享桶的建桶和封桶语句（`INSERT … ON CONFLICT DO UPDATE`）也不会在同一句里改同一行两次。多节点共享一个出口地址时要把名额分给各节点或把状态放进数据库，接口不变。

## 不变量

| 不变量 | 靠什么保证 |
|---|---|
| 密钥明文不落库 | 表里没有明文列，加密列只由 CHECK 约束长度（不证明是密文）；加密由写入边界保证，真实性由 `Open` 验证；测试在整行、全部列里搜索 key 的字节（文本和十六进制），并且加密值只能在自己那一行打开。**尚未覆盖**：写入处只传 `secret.Sealed`，由第一个适配器的编解码函数保证，随它的 PR 加测试 |
| 没有确认单位价值的供应商不被调用；一次尝试的成本按它开始时的单位价值换算，向上取整一次，只换成结算币种；回执记下那一行的 id | `ListRoutable` 的 LATERAL 连接；`TestOnlySuppliersWithAUnitValueAreRouted`、`TestCreditsPricesAtTheUnitValue` |
| 供应商只在自己的范围内可见、可写；子表经父表判断 | 策略；测试覆盖无范围、平台、站点、账户、另一个站点或账户，以及六张子表各自的插入 |
| 分组、凭证、模型、别名、端点属于同一个供应商 | 复合外键；测试对四张表各试一次跨供应商引用 |
| 身份和发往的地址不能改，不能删除 | 按列授权、不授 DELETE；测试逐列、逐表 |
| 成本未知的端点不会被当成已知 | `known_has_schedule` CHECK；成本状态进快照的条件在后续的快照查询里 |
| 对方的文字不进错误、日志和表（唯一的例外是下一行的 `Failure.Rejection`） | sub2api 的失败路径只带状态码和白名单内的代码（`knownCodes`，不在表内的代码一律说"没有已知代码"，回显了凭证的 `reason`、`code` 也进不来）；测试让响应回显一个凭证，核对每个读取的错误里都没有它；录制样本里的令牌、key、邮箱、地址、请求编号都是占位符 |
| 缺成员的成功响应不是空结果 | `open` 要求 `code` 为 0 且有 `data`，列表和页数缺失即 `ErrUnrecognised`；表驱动测试覆盖 `{"code":0}`、`null`、`{}`、缺 `pages` 等 |
| 5xx 不会因为响应里的代码被当成"没执行" | `Classify` 先排除 5xx；契约测试（`adaptertest`）对已知拒绝代码的 5xx 也要求"可能执行了" |
| 引擎与适配器的扫描协议一致 | 位置是适配器自己的不透明字节，第一次调用不带位置；`adaptertest.RunScan` 对每个适配器检查（含结束一遍后的位置能再传回），新 API 和 sub2api 各有一个真实适配器经引擎读完一遍的测试 |
| 对方撤销账号后不再登录或轮询 | `ErrAuthRejected` 后适配器不再发登录，引擎把供应商置为"需处理"；测试数发出的请求 |
| 同一个响应同步两次没有新事件 | 目录以规范化内容比较；测试 |
| 移除之后库里没有这家供应商的密文，用它做过的尝试仍能结算 | `Admin.Remove` 一个事务：登录行删除、凭证的四个密文列清空且状态为 `erased`（表的 CHECK 拒绝"已清除却还有密文"），行保留；测试：移除后登录行为 0、凭证全部清空、审计记录列出 key 的 ID 而不含密文、下一次 `Reload` 后不在路由里、之后仍能写 `attempt_costs`、再移除一次不改代次也不重复审计 |
| 没有余额的供应商不进路由 | `ListRoutable` 要求 `balance_native > 0`；测试：没读过、为 0、透支的不进，为正的进 |
| 失败不带对方的文本，没把握不算免费 | `Cause` 的成员；`adaptertest` |
| 对方拒绝请求本身时，它的话只给这次回复用，且已去掉能指向对方的内容 | 只有 `Send` 在 `ScopeRequest` 的失败上填 `Failure.Rejection`（`rejectionOf`），适配器的 `Classify` 仍不留文字；流里的错误事件由 `Attempt.Sanitize` 用同一份清洗（`cleanRejection`）；去掉所有网址、本次用的凭证、对方的域名及每一级上级域名（`hostNames`，两段以上）、先整串遮蔽的本次凭证、形似凭证的串、`(request id: …)`、new-api / sub2api 的名字，`type` 只留 OpenAI、Anthropic 的通用取值，每项至多 2 KiB；`TestRejectionOfKeepsTheWordsAndNotTheSupplier`、`TestARefusedRequestKeepsWhatTheSupplierSaidOfIt` |
| 正常供应商一定有注册的适配器；删任一适配器其余照常编译 | `CheckAdapters`；`just adapters-removable`（现在对 new-api 真实生效） |
| 一个供应商同一时刻最多一个同步任务在排队或运行 | 任务按参数唯一，状态含运行；测试：入队两次只插入一个，完成后能再入队；扫描三次只为到期、没人持有的供应商入队一个任务 |
| 排队后被暂停或移除的供应商不被调用 | 取到租约后读状态，不在 `syncable` 里就结束；测试：入队后改为暂停、移除中、已移除、需处理，再跑任务，适配器没被打开、状态没动、租约已还 |
| 接不上的供应商不会让别的供应商一直排不到 | 打开适配器处的失败同样记为任务失败并退避；测试：超过一批（200）个出口不可用的、更早到期的供应商，之后的扫描仍会同步到期得晚一点的正常供应商 |
| 被限速的清单读完而不是每次从头读 | 任务在限速重开之后持着租约用同一个适配器再跑一轮；测试：清单第一次被限速、第二次完成，一个任务内适配器只打开一次、清单读完 |
| 退出信号不取消在跑的任务 | `pg.Queue.Start` 与启动 context 脱钩，只有 `Stop` 的期限取消任务；测试：启动 context 结束后任务不被取消，`Stop` 的期限内完成则不被取消、超过期限才被取消 |
| 被拒绝的账号不被再次调用，登录也只一次 | 登录失败和请求被拒都经引擎记为需处理，之后的扫描不再选它；测试数适配器的 `Open` 和请求次数 |
| 不超过对方的限速，被限速时不丢进度 | `client.Limiter` 全部取到名额才发、429 封全部所占的桶；`Charges` 出错时返回原位置；扫描测试在小名额和 12,000 条记录下统计每个窗口的请求数 |
| 多个节点从一个出口地址访问同一个供应商，合计不超过对方的限速 | `SharedLimiter` 在数据库里计数；测试让两个节点（各自的连接池）同时取同一个桶，合计只放行上限那么多，一个桶满时另一个桶不被扣，一个节点封桶另一个节点也被封 |
| 一个凭证的调用不超过它的并发位和 Axiom 滑动窗口的请求上限；任意滚动 60 秒内不超过上限 | `Acquire` 先比较交换占位，再在 `minuteRuntime` 的锁里读时钟、汇总 [now-60, now] 的 61 个单调秒桶并加一（`take` 把读时钟、检查和计数做成一步），拿不到就把占位还回去、换一个凭证；`leastBusy` 先跳过这个窗口已满的。测试：40 个 goroutine 抢 3 个名额，同一秒里正好 3 个成功，第 61 秒后仍能取到（占位没有被漏掉）；迟到的调用看到的是锁里的新秒，回拨不会重开旧桶；`TestNeverMoreRequestsThanTheWindowWhoeverRaces`、`TestACredentialTakesOnlyItsWindow`、`TestAccountSharesWindowAcrossCredentials`、`TestUnlimitedCredentialsConsumeSharedWindow`、`TestTheWindowEndsAtTheSixtiethSecond`、`TestABurstAtTheWindowEdgeIsBounded`、`TestTheClockIsReadUnderTheAccountLock`、`TestARollbackDoesNotReopenTheWindow` |
| 同一时刻只有一个节点写一个供应商的同步状态 | 租约加栅栏；测试让多个节点同时取租约只有一个成功，租约过期被接手后旧持有者的提交和登录令牌的写回都被拒绝且什么都没留下 |
| 一步的状态、事件、成本计划、扣费记录同成同败 | 一个事务；测试让扣费记录被数据库拒绝，核对状态、事件、计划都没有留下 |
| 同一份清单同步两次，分组、模型、端点、成本计划、别名一行都不写 | 投影先读同一事务里的行，一样就不写；测试用每行的 `xmin` 核对五张表，并核对事件数没变 |
| 未映射、未上架、清单里已不存在的模型没有端点 | 端点在映射到上架目录模型且有成本计划时才生成；未映射和未上架的名字只留 `supplier_models` 的空 `model_id`，待映射队列列得到；测试逐个核对端点集合 |
| 没有成本计划的端点不进路由 | `ListRoutable` 要求 `cost_state = 'known'` 且连得上 `cost_schedules`，写入侧只在拿到计划后置 `known`；测试先读没有计划的清单、再读有计划的 |
| 人停用的端点不会被同步重新打开 | `enabled` 只在插入时取默认值，投影的 UPDATE 不含它；测试停用后换一版计划再同步 |
| `Routable()` 与 `ListRoutable` 是同一批端点 | 两者按同一套条件（启用、成本已知、分组选中、凭证可用），`Routable()` 取 `Reload` 时的快照；测试对比两边的 id、上游名字、模型、格式和成本计划 |
| 一家供应商读不出来不拖垮其余，`Reload` 一直失败时不无限期沿用旧端点 | `Reload` 按供应商收集错误、整家排除并记日志、计数；快照带最后一次成功 `Reload` 的时间，`Routable()` 超过 `MaxStale` 返回 `ErrStale`；测试：一个凭证 key 打不开时另一家照常、这一家被排除、日志和计数有记录、库里状态不变且下次 `Reload` 仍成功；`Reload` 失败时在 `MaxStale` 内仍返回快照、超过后 `ErrStale`；失败期间被停用的供应商过了 `MaxStale` 不再被路由 |
| 节点崩溃后从存下的位置接着读扣费记录 | 每页与位置同一次提交；测试让一个节点读到第三页失败且不还租约，租约过期后另一个节点从第三页开始，记录各存一次 |
| 失去租约或凭证已被换过的节点不覆盖登录令牌 | `Swap` 和提交先锁住租约行与供应商行再比较栅栏和 `config_gen`；测试逐项，并在写被锁挡住时让接手、改凭证同时发生 |
| 换上登录的供应商重新进入扫描，握着旧登录的节点写不进任何东西 | `Admin.ReplaceLogin` 在同一事务里复位状态（正常、失败与 `halted` 清零、`lastOK` 与扣费水位保留），并把 `lease_fence` 和 `version` 加一、清空租约；测试：停在需处理的供应商换登录后 `next_due` 是当时、下一个节点取得租约，旧持有者的提交和 `Swap` 都被拒绝；从没同步过的供应商由同时进行的第一次同步让出状态行，复位照样落地 |
| 扣费水位只升不降，每条记录至少读到一次 | 前进遍的 `end` 大于水位、水位取最大值；新记录只出现在列表最前面；测试覆盖整页边界、扫描期间新增、迟到记录（重叠回扫和每日补扫） |
| 凭证只发往接入时的主机，错误里没有对方的文字 | `client.New` 只接受裸 origin，路径不能改主机；管理接口的错误只带固定清单里的 `code` 和权限名 |
| H-I9 检测成本行的估价不低于供应商的实际扣费，对账从不判它 | 行带 `test` 标记，判定见标记即返回未判，等待扣费的列表排除它；测试 `TestReconciliationMatchesATestButNeverJudgesIt`：扣费比估价高 60 倍仍不摘牌、不报偏高，扣费照常认到这一行，检测的行不列在等待里 |

## 调用面

已实现（`service.go`、`attempt.go`、`record.go`）。`Service` 是一个节点上的调用面：快照、凭证运行状态、成本计划。

```go
func New(cfg Config) (*Service, error)                        // DB、平台范围、主密钥、出口、适配器注册表、时钟
func (s *Service) Reload(ctx context.Context) error            // 重建快照，在本节点内存里解密 key；读不了库才失败（旧快照和确认时间不动）；key 打不开或缺适配器的供应商整家不进本节点的快照（记日志、计数），其余照常
func (s *Service) Routable() ([]Routable, error)                // 快照里的端点，按 id 排：routing 建快照用（见下）；最后一次成功的 Reload 超过 MaxStale（默认 30 秒）返回 ErrStale
func (s *Service) Acquire(ep EndpointID) (*Attempt, error)     // 内存比较交换；ErrNoEndpoint、ErrNoSlot
func (a *Attempt) Send(ctx context.Context, c Call) (*http.Response, *Failure) // 内含 Classify；Call{Format, Compact, Body, Header}
const NotExecuted = adapter.NotExecuted // Failure.Executed 的取值：确知上游没有执行；gateway 够不到适配器的类型，只用这两个
type RateLimited = adapter.RateLimited  // Failure.Cause 的一种：上游回了“请求过多”，路由据此给端点降权
// Failure.Cause 的全部 13 种都在本包以同名类型别名导出（Overloaded、UpstreamError、ContextTooLong、ModelNotFound……）：
// gateway 与 quality 够不到适配器的类型，quality 按每一种原因判定端点健康（docs/domains/quality.md「什么算一次失败」）
func (a *Attempt) Done() AttemptReceipt                        // 幂等；释放占位；带凭证、端点、成本计划版本、单位价值行 id、已到的对账键；字段不对外，billing 只原样传回
func (a *Attempt) Policy() CallPolicy                          // 适配器默认；端点实测值覆盖 Cancels
func (s *Service) SetMeasuredCancels(ep EndpointID, cancels bool)
func (s *Service) TestEndpoint(ctx context.Context, r TestRequest) (TestResult, error) // 一次真实的检测生成（「用真实生成测试端点」）
func RecordTest(ctx context.Context, tx pg.Tx, who audit.Actor, reason string, r TestResult) error // 检测的成本行和审计，在调用方的事务里
func (s *Service) Cost(r AttemptReceipt, u []catalog.Usage) (AttemptCost, error) // 纯内存：原生金额、缺项、cost_flag
func (s *Service) Credits(r AttemptReceipt, u []catalog.Usage, c platform.Currency) (AttemptCost, int64, bool, error) // 纯内存：同一次定价的 Cost 和 c 的十亿分之一；即 gateway 的 Costs
func RecordAttempts(ctx context.Context, tx pg.Tx, rows []AttemptRecord) error
func RecordLostRequest(ctx, tx, request, site, account uuid.UUID, route []uuid.UUID) error // 清理：请求可能被上游收过钱，路线是它冻结的端点
func ResolveLostRequest(ctx, tx, request uuid.UUID) error      // 迟到的结算带着真实尝试回来
func EndRequest(ctx, tx, id, site, account, at) error          // billing 在终结请求的事务里调用
type CallWrites struct{}                                       // 上面四个写入作为 billing.Suppliers：无状态，请求用 uuid.UUID，零值交给 billing
func Reconcile / AssignToLostRequest / AssignUnattributed(ctx, tx, ...)       // 对账事务与人工归属，见下；Reconcile 由周期任务调用，归属接口供运营调用
func RegisterReconcile(ReconcileJobConfig) error                            // 节点队列启动前注册逐条对账与告警
```

与设计草图的差别：`RecordAttempts` 等是包级函数（不需要 `Service`），`CallWrites` 把它们按 billing 要的形状（请求是 `uuid.UUID`）转发出去；`Cost` 没有时间参数，按回执里的成本计划计价；`Send` 的入参是 `Call{Format, Compact, Body, Header}`，`protocol.Upstream` 合并后换成它。`Header` 是 gateway 的白名单头（`anthropic-beta`、`anthropic-version`），在默认头之上设置、再由凭证和内容类型盖过：客户端换不掉凭证，写了 `anthropic-version` 就用它的，没写才加默认的 `2023-06-01`。`Compact` 选格式的压缩端点路径（`compactPaths` 表里，目前只有 Responses 的 `/v1/responses/compact`）：那个端点不是格式自己的路径，路径只在 `wire` 与 `compactPaths` 两张表里（[接入的「路径与格式」](../../docs/domains/gateway.md#路径与格式)）。`Failure` 是 `adapter.Failure`。

- **按供应商隔离**：`Reload` 里一家供应商的数据读不出来（凭证的 key 打不开、没有这个类型的适配器、成本计划或单位价值不合法），这一家的全部端点不进本节点的快照，其余供应商照常载入，这次 `Reload` 仍然成功。告警靠两样：错误日志带固定事件名 `suppliers.unloadable`（含供应商 ID 和原因），计数指标 `axiom.suppliers.unloadable` 按供应商（`supplier` 属性）每次 `Reload` 加一，看的是上升而不是总数（与队列的 `queue.job_discarded`、`axiom.pg.queue.discarded_jobs` 同一做法）；指标的 Meter 由 `Config.Meter` 给，nil 为不计。**不写库、不改供应商状态**：原因可能只在本节点（例如只有它缺一个 key 版本），写全局状态会让别的节点正常的供应商也停掉；`Reload` 里的写会与管理员刚做的修复竞争（没有 `config_gen` 比较时旧的读结果会盖掉新的修复），也会在等行锁时拿着 `reloadMu` 卡住整个刷新循环，让所有供应商一起过期。代价：状态页不显示这家出了问题，要靠告警发现；快照里缺它，其余节点是否也缺它各自独立。
- **不等重建**：`Reload` 在自己的状态里解析成本计划、解密 key，建好后整体替换；调用路径只做一次原子读取，`Cost`、`Policy`、`Acquire` 不拿 `Reload` 用的锁。在途数和冷却按凭证 ID 保存，每把锁只在查表时短暂持有。
- **`Acquire`**：在没有冷却、没有过期（按时钟，不靠数据库变更）、在途数低于上限、这一分钟的请求没用完的凭证里挑负载最低的，对在途数做一次比较交换；拿不到返回 `ErrNoSlot`，gateway 跳过这个条目，不算尝试。在途数、冷却时间按凭证 ID，这一分钟已用的请求数按供应商 ID，存在快照之外，重建快照不清零。
- **`Routable`** 是给 `routing` 建快照的投影，只含已经能调的端点（启用、成本已知、分组选中、有可用的凭证、供应商在正常状态）：`ID`、`Supplier`、`Model`（目录模型）、`Upstream`（发给上游的名字）、`Formats`（这个端点能用的调用格式，来自 `endpoints.formats`）、`Schedule`（成本计划，已按分组倍率缩放）、`Streams` 和 `Cancels`（适配器对这个供应商的说法）。它读的是 `Reload` 的快照，所以同一个快照里不会一半旧一半新；快照记着最后一次成功的 `Reload` 开始读库的时间，超过 `Config.MaxStale`（默认 30 秒，同目录）就返回 `ErrStale`，这样 `Reload` 一直失败时，期间被停用或移除的供应商不会无限期接流量（`gateway` 的重建因此失败，路由快照按自己的最长陈旧时间过期）。代价：一次 `Reload` 整体失败（读不了库）持续时，最后一次成功的 `Reload` 约 30 秒后 `Routable()` 开始返回 `ErrStale`（重建每 5 秒一次），路由快照再过自己的 30 秒过期，节点约 1 分钟后对所有调用回 503 `overloaded`，直到一次 `Reload` 成功；这是「宁可整节点停路由，也不往无法确认的端点发」的取舍。单个凭证的 key 打不开不让 `Reload` 失败，只让那一家供应商从本节点快照消失（见「按供应商隔离」）。`ListRoutable` 是同一批行的查询，测试核对两边一致。模型的规格（上下文、输出上限、能力位）不在返回值里：那是目录的 `Model.Serving()`，`routing` 建快照时自己查。
- **`Send`** 对拒绝请求本身的失败（`ScopeRequest`）还附上 `Failure.Rejection`：响应体里 `error` 对象的 `message`、`type`、`param`、`code`（OpenAI 和 Anthropic 的外壳同位置），清洗见「不变量」；响应体不是这种对象时为空，网关回统一的说法。上游在流里报的同一个错误由网关交给 **`Sanitize`**：`Sanitize(protocol.Rejection) *protocol.Rejection` 用同一份清洗（`cleanRejection`），因为协议只按帧取出有界的几项，丢掉供应商身份要凭证和端点地址，只有 suppliers 知道；返回 nil 表示没有能交出去的话。**给每个失败标明上游是否执行过**（`Failure.Executed`）：用 `httptrace` 的 `WroteRequest` 判断，请求写完之前失败是 `NotExecuted`（作用于出口）；写完之后，非 2xx 由适配器的 `Classify` 归类，其余（5xx、断线、超时）是 `Unknown`。请求路径（压缩调用取压缩端点的）和鉴权头只由调用格式定（Bearer 加 Anthropic 版本头），响应头里的对账键记在 `Attempt` 上，失败的响应也记。凭证被拒、额度用尽、被要求等待时，凭证按适配器给的时间（没有就按配置）歇着。suppliers 和 egress 不暗中重试 POST。
- **对账键**只取上游的响应头，由适配器的 `ChargeRef` 读：new-api 是 `X-Oneapi-Request-Id`；sub2api 是 `client:` 加 `X-Client-Request-Id`。对账只比较字符串是否相等。
- **`Done()`** 没有参数，幂等：重复调用、取消与结束竞争都只释放一次占位。
- **单位价值**：`Reload` 随端点读出供应商最新的单位价值、那一行的 id 和结算币种，没有确认单位价值的供应商，端点不进快照，与管理接口「确认之前不进路由」一致；回执带上 `Acquire` 时快照里的值和那一行的 id，所以一次尝试按它开始时的值计价，之后改的值只管之后的尝试。节点用的是它最近一次 Reload 时的快照，不一定是数据库里最新的一行；复算用的是回执里的 id，不靠时间推断。回执的 `unit_value_id` 随结算写入 `attempt_costs`；旧行保持 NULL，不能拿最新单位价值推断过去。
- **`Credits`**（连同换算前的 `Cost`，一次定价）= 原生金额 × 尝试的单位余额价值 × 预留冻结的汇率，精确相乘后转成十亿分之一时向上取整一次。同币种不读汇率，结果与原算法一致。gateway 将路由冻结的 `platform.ExchangeRate` 经 `AttemptReceipt.WithExchange` 传回；供应商不读 billing 表。缺汇率、转换溢出返回未知，缺计费项给已定价部分与未知；原生 `CostFlag` 不因缺汇率改变，对账仍能比较原生估算与实际扣费。gateway 保留 `known` 给 billing，任何尝试未知都让请求利润为 NULL。
- **换算依据**（迁移 `00007_exchange_basis.sql`，其检查约束先 `NOT VALID`，`00008_validate_exchange_basis.sql` 再 `VALIDATE`，不在大表上持排他锁扫描）：`attempt_costs` 保存 `unit_value_id`、`exchange_rate_id`、`conversion_from`/`conversion_to` 和 `conversion_state`（`Conversion` 枚举：same_currency、converted、missing_rate、overflow、unpriced、not_executed；SQL 的检查约束重复了这些名字，`TestConversionStatesAreStoredAndKeepTheRateOnlyWhenUsed` 逐个写入核对）。方向由源/目标币种与汇率行的规范化币种对确定；同币种、缺汇率和未执行的尝试没有用汇率，不写汇率 ID，只有 converted 和 overflow 写。旧节点/旧行的 NULL 是未知依据，绝不回填当前配置；字段随尝试只插入，不随重试变化，不建到 billing 的外键。测试在 `exchange_test.go`。
- **缺汇率告警的数据**：`EnabledCurrencies` 在平台事务列出 active、low_balance 的供应商，包括暂时没有可路由端点的。组装层每轮路由构建与当前汇率一同读取，日志/指标只记录缺失及恢复状态变化，读取失败不能推进路由快照的新鲜时间。
- **`RecordAttempts`** 在 billing 的结算事务里一次写入这个请求的全部尝试（`ON CONFLICT DO NOTHING`，用 `unnest` 一条语句），往返与尝试次数无关；已有的行不变。`RecordLostRequest`、`ResolveLostRequest` 同样只插入。
- **`CallPolicy.Cancels`**：适配器给默认值（new-api 传递，sub2api 不传递），quality 按端点实测的值覆盖它，规则见[计费的「客户端断开」](../../docs/domains/billing.md#客户端断开)。

## 用真实生成测试端点

已实现（`test.go`）。`TestEndpoint` 只测试平台供应商的端点，非平台归属返回 `ErrTestOwner`；它测一个端点能不能用：一次真正的生成，走真实调用面（`Acquire`、`Send`、`Done`），用端点自己的调用格式、流式、输出上限 16 个 token、不开推理；`RecordTest` 写它的成本行和审计。管理接口的 `testSupplierEndpoint` 就是这两步（「管理接口」），[quality 的试探](../../docs/domains/quality.md#试探)以后从同一处调用。

- **发什么**：`TestRequest{Endpoint, Format, Prompt}`，`Prompt` 为空是 `TestPrompt`（`hi`）。格式必须是端点 `formats` 里的一个，否则 `ErrNoFormat`，什么都不发。三个格式的请求体是 new-api 的 channel test（`controller/channel-test.go:702-782`，提交 `7d8b912`，2026-10-22）加两处改动：Responses 也带上限（new-api 不带），并且没有任何成员要求推理。Messages 和 Chat 用 `max_tokens`，Responses 用 `max_output_tokens`；Chat 的新模型改用 `max_completion_tokens` 时会回 400，这次检测因此是不确定，而不是端点没干活（见末行）。
- **读到流结束，从不提前取消**：调用方的取消被 `context.WithoutCancel` 挡在外面，并发位持有到读完整条流；只留自己的 45 秒上限（比管理请求的 1 分钟短，慢的答案也还来得及记账）。不传递取消的供应商（sub2api 一类）照样生成到底并收费，提前断开只会让我们记的成本少于对方扣的。测试 `TestATestIsReadToItsEndThoughTheCallerGoesAway`：假上游写第二段之前调用方就取消，第二段仍写得出去，请求的 context 直到最后都没被取消。
- **判什么**：`TestResult.Outcome()` 按[什么算一次失败](../../docs/domains/quality.md#什么算一次失败)只看端点自己那一格：2xx 且流读到上游自己的正常结束事件是通过（包括没有可见内容、输出上限耗在隐藏推理上的情况）；没有事件的空 body 或未到结束事件就断流才是 `stream_broken`；端点自己那一格的 `UpstreamError`（5xx）、`Overloaded`、`ModelNotFound`、`Transport` 和 500 及以上且不是疑似出口拦截的 `Unknown` 是失败；别的范围（请求、出口、凭证、分组、供应商）、`RateLimited`、500 以下的未知状态、我们自己的期限和内存，都是不确定——既不通过也不失败。`ProblemCauses()` 从 `Cause.String()` 和流错误映射派生实际名字，`cmd/axiom` 的契约测试逐项核对；`Problem()` 给出这些名字及 `ProblemScopes` 里的范围，流断了是 `stream_broken`。
- **成本行**：`RecordTest` 在**调用方的事务**里写，不另起一个：`billing` 的结算事务用 `RecordAttempts` 写真实尝试，检测借同一个函数和同一个事务（后台的测试接口就是它自己的幂等事务，事务也不受浏览器断连取消，且有 1 分钟上限，给 45 秒的生成留出提交时间；重放得到第一次的答案，不会为同一次检测再付一次钱）。**只有事务提交后**成本行、审计和幂等响应才存在；生成后写入、`FinishKey` 或提交失败会全部回滚，成本和认领都不存在。供应商仍可能扣费：没有成本行可匹配的扣费在宽限期（默认 30 分钟）后照常暂挂、告警、由人处理；同键重试会再次生成、再次付费。`TestAdminEndpointTestRollbackAllowsAnotherGeneration` 注入响应写入失败，核对回滚、重试及之后的重放；`TestEndpointCostAndAuditRollBackTogether` 核对回滚后扣费暂挂。行带 `test` 标记，两列租户为空（平台自己付），没有预留、不经 billing 的结算，`state` 停在 `pending` 等对账。
- **估价是确定的上界**：输入按题目的 token 数上界（字节数 + 16 的框架开销，一个字节至多一个 token），输出按上限 16，两侧都取输入上界可落入的所有上下文档位和标准档里的最高价，绝不用上游报告的用量。适配器或传输层已确定 `NotExecuted` 的尝试不产生检测成本：估价为 0、用量为空、换算状态为 `not_executed`，仍写检测行和审计；未知执行状态（包括可能已执行的 5xx）仍按上界估价。成本计划缺一侧的价时返回 `ErrTestCost`（管理接口为 422），在付钱之前拒绝这次检测。测试 `TestATestSendsACappedStreamAndBooksWhatItFound`：18 个 token 与 16 个 token 的封顶合成 6.15。
- **对账**：成本事务已提交的检测扣费照常认到这一行（也照常给 billing 发带 `test: true` 的事件），但从不用估价判它：`reconcile.judge` 见 `test` 即返回未判，`ListUnmatchedAttempts` 也排除它，所以一次检测既不会把端点摘牌，也不会被当成"等不到扣费的尝试"告警（H-I9）。
- **审计**：`endpoint.test`，目标是供应商，明细里有端点、格式、结局、原因与范围、上游状态与它的请求 ID、估价和原生单位；不带凭证和 key。
- **没有做的**：`max_completion_tokens` 那条分支（设计稿 [quality](../../docs/domains/quality.md) 说不需要 o 系和 gpt-5 的细节）；一次检测里不换凭证（quality 的试探会刻意换一把，见[试探](../../docs/domains/quality.md#试探)）。

## 并发与速率

一个凭证有两个上限，都存在 `suppliers.credentials` 上，由管理接口一起设定（「管理接口」的 `SetLimits`）：

- **并发**（`max_concurrency`，至少 1，新绑定是 4）：同一时刻在这把 key 上进行的调用数。同一上游账号下各 key 的并发上限之和应不超过上游账号并发；例如账号并发 5，两把 key 不能各设 4。账号级并发准入属于 [#313 E3-3](https://github.com/TYCT-tyct/Axiom/issues/313) 的后续工作，本次仍按凭证计数。
- **每分钟请求**（`requests_per_minute`，可空，1 到 100,000）：上游账号在滚动 60 秒内的请求数达到这个值时，不再选这把 key。空是不额外限制这把 key，默认不猜上游的额度；它发出的请求仍计入账号。

**RPM 按上游账号共用**：`suppliers` 的身份是来源加上游账号，同一供应商的全部 key、端点、模型和格式共用 `minuteRuntime`；不同供应商互不影响。new-api 的 `middleware/model-rate-limit.go`（206e0fa5）按用户（可再分组）计数，sub2api 的 `backend/internal/service/admin_service.go`（`UserRPMStatus`）与 `backend/internal/repository/user_rpm_cache.go`（8f2cbb53，2026-10-19）按用户（可再分组）计数，新增 key 不应凭空新增一份账号额度。适配器尚未提供上游分组限流的配置，因此 Axiom 保守地合并整个账号，不把分组额度误当作独立账号额度。

共用一份上游额度的 key 应设相同 RPM。若设成不同值，各自按自己的上限与账号计数比较：例如 10 和 20，账号用了 10 次后只有后者还能接，合计到 20 后都停止；不是各发 10 和 20 次。未设上限的 key 仍可接请求，同时增加账号计数，免得有上限的 key 漏算它的调用。

计数在节点内存里，由 `Acquire` 一次取得并发位和请求名额：`leastBusy` 每个端点只加锁读取一次账号窗口计数，各 key 按自己的上限比较，跳过冷却、过期、并发位已满或账号窗口已满的 key；比较交换占并发位后，在账号的锁里读取同一把锁保护的时钟，汇总 [now-60, now] 的 61 个一秒桶并加一。时钟在锁里读，所以一个在 `Acquire` 之后被暂停的调用不会沿用旧秒，跨秒交错不会多放行；桶按首次准入起的单调秒计，时钟回拨时窗口不倒退。检查失败就还回并发位再挑，成功计入的请求不退，因为它可能已经到达上游。每个桶记着它属于哪一秒，过期的桶直接读作 0，不用计时器也不用清理；内存每个账号固定 61 个桶，与上限无关。任意滚动 60 秒都不会超过所选凭证的上限：按秒取整最多把不到一秒的旧请求仍算在窗口里，所以只会少放行，不会多放行。计数饱和在上限（100,000），未设限 key 也不会把它加溢出。管理接口和写入校验拒绝超过 100,000 的值；滚动发布期间旧节点仍可写更大的值，运行时和读接口按 100,000 读，数据库的上限 CHECK 待旧节点退出后再加。多节点还需分别分配这份额度。

**RPM 不跨节点**：共享 RPM 计数随 Valkey 后续实现，进程重启清零。并发仍沿用 [gateway 的静态份额设计](../gateway/README.md#并发背压与共享状态)，本次不实现份额分配或共享槽位；当前单核心直接使用配置上限。`Reload` 每 5 秒载入新上限，保留在途调用和账号窗口；降低上限不取消已有调用。**TPM** 等有可信额度数据时再加，目前只有余额和 key 状态。

## 管理接口

已实现（`admin.go`、`admin_write.go`）。`Admin` 是管理后台对供应商做的事，处理器（`cmd/axiom/admin_suppliers.go`）只做请求与响应的转换；每个函数在调用方的事务里写（事务要有平台范围，审计条目在同一个事务里写），审计条目带操作者和原因，不带登录、账号和邮箱。除 `Detect` 外不碰供应商：同步引擎读到这里改的行和代次后做其余的事。

| 操作 | 做什么 | 拒绝 |
|---|---|---|
| `NormalizeOrigin` | 只读文本、不碰网络，得到存下的来源：小写；端口按十进制整数解析，须在 1 到 65535，按规范写法输出（去掉前导零），是 https 默认的 443 时省略。同一个站点写成两种样子不会成为两个供应商，凭证也只发往这个地址。处理器在占幂等键之前调用它，规范化后的来源进入键的指纹 | 不是 `https://主机[:端口]`，或端口不在 1 到 65535（`ErrBadOrigin`） |
| `CheckOrigin` | 用出站客户端的 `Check` 解析并验证规范化后的来源，回答的是网络此刻的样子。建供应商时只在幂等回调里、也就是真正执行改动的那次请求里调用；同一个键的重发直接得到第一次的响应，不再问网络，所以 DNS 或出口变了也不影响重放。检查失败时事务回滚，占键一起撤销 | 内网、回环等地址（`ErrAddressRefused`）；解析不出（`ErrUnreachable`） |
| `Detect` | 每次都先 `NormalizeOrigin` 再 `CheckOrigin`（探测没有重放）；按注册顺序问每个能被接入的适配器是否认得这个站点；站点要验证码时，只列不需要密码的登录成员；不存任何东西，每次探测最多 45 秒 | 同上两行；没有适配器能判断出站点答了没有（`ErrUnreachable`）；认不出不是错误，返回没有 `Kind` 的结果 |
| `Connect` | 建供应商（`connecting`），来源须是 `NormalizeOrigin` 的结果、调用方已在同一次执行里 `CheckOrigin` 过；登录用 `master` 密钥封到 `Location{suppliers.supplier_logins, 供应商 ID, login}`；同一来源和账号只能接入一次 | `ErrNoAdapter`、`*LoginError`（缺成员，或给了这个类型不用的成员）、`ErrSupplierExists` |
| `SetStatus` | 使用中或低余额的暂停，暂停的恢复；已经是要的状态则不动、不记审计、不升代次 | 其他状态（`ErrStatusLocked`）：`connecting`、`needs_attention` 由同步改，移除中和已移除不能回头 |
| `Remove` | 从任何状态当场移除（见「同步」的「移除」）：状态 `removed`，代次加一让正在跑的一轮同步作废，登录删除，凭证清空为 `erased`；已移除的再移除不动、不记审计 | 无：供应商不在是 `ErrNoSupplier` |
| `ReplaceLogin` | 新登录封在旧的位置上，版本加一，供应商回到 `connecting`，代次加一，同步状态复位（见「持久化」的「登录被替换时唤醒」） | 同 `Connect` 的登录检查；移除中和已移除 |
| `ConfirmUnitValue` | 一单位余额值多少结算货币：大于 0、至多 1000、至多 15 位小数；只追加行，新的值从现在起生效，已算过的成本保留自己的 | `ErrUnitValue` |
| `SelectGroup` | 选用或取消选用一个分组；第一次选用时给分组起代号（`g` 加 8 个 base32 字符），之后取消再选用不变 | `ErrNoGroup`、`ErrGroupGone`（选用已不在列表里的分组；取消不受限） |
| `BindKey` | 把供应商上的一把 key 绑到已选用的分组：只记 key 的引用，状态 `binding`、没有密文，同步去读出并封存；默认最大并发 4、每分钟请求不限 | `ErrNoGroup`、`ErrGroupNotSelected`、`ErrKeyBound` |
| `SetLimits` | 一个凭证同时服务几个并发、一分钟放几个请求（RPM 为 1 到 100,000 或空）：两者一起设定（PUT 省略 RPM 或传 null 都清除上限）、记一条审计（`credential.limits`，前后各值）；已经是这个数则不动、不记审计、不升代次——同步不写这两列，没有要作废的一轮 | `ErrNoCredential`（不是这家供应商的也算没有）、`ErrLimits`、`ErrStatusLocked`（凭证已擦除或供应商移除中、已移除） |
| `MapModel` | 别名：上游名字在一个分组或整个供应商里指向某个目录模型，建了不改，改错在更窄的范围再建一个；模型由调用方查过 | `ErrAliasExists`、`ErrNoGroup`、`ErrStatusLocked` |
| `PendingMappings` | 还没有模型的上游名字，最早出现的在前（`first_seen_at`），被别名盖住的不出现，键集分页 | |

处理器的 `Idempotency-Key` 指纹里没有登录的值，只有给了哪几个成员：指纹存在库里，带值会让它成为猜密码的依据；代价是同一个键换一个密钥重发得到第一次的结果，所以页面每次尝试用新键。

上面改配置的操作都在 `suppliers` 行上加锁、校验状态后才写，所以和同步、彼此之间串行。读（`Suppliers`、`Supplier`）带最近一次的单位价值、余额和分组与 key，`Supplier` 还带端点（按上游名排序，含格式、成本状态、是否启用），后台从中选端点测试；没有同步写过的字段是空。

`POST /api/admin/v1/suppliers/endpoint-tests`（`testSupplierEndpoint`）不走 `Admin`：它要的是本节点的调用面（`callNode.upstreams`），经 `TestEndpoint` 发一次真实生成、`RecordTest` 在同一个事务里写成本行和审计（「用真实生成测试端点」）。每个会话每分钟 10 次，同 `detectSupplier`，因为每次都是平台付钱的一次生成。

## 对账

`internal/suppliers/internal/reconcile`。同步引擎在栅栏事务里经 `reconcile.Ingest` 写入扣费记录（一条记录读两次只存一次），结算在自己的事务里写入 `attempt_costs`（含对账键），清理写入丢失的请求。节点在 `pg.Queue` 启动前调用 `RegisterReconcile`：River leader 每分钟投递 `suppliers.reconcile`，启动或换主也投递，任一节点的 worker 都可执行。相同参数的未完成任务去重；匹配、暂挂、摘牌、事件及下一页任务在同一平台范围事务提交，重复和迟到执行不改变已有归属或重复增加版本。

选择独立周期任务，而非接在每家供应商同步之后：同步暂停、上游断线、尝试迟到时，已有扣费记录仍要继续对账。未匹配尝试的下一页游标随事务写入 River，换 worker 或重启不丢进度；周期任务仍从头检查，持续缺失的扣费记录会再次告警。匹配和暂挂超过每轮上限的积压留给下一轮。

告警使用结构化日志：`suppliers.reconcile.under_estimated`（ERROR，端点 ID）、`over_estimated`（WARN，扣费 ID）、`held`（WARN，扣费 ID）、`unmatched`（WARN，请求 ID 和尝试序号）、`invalid`（ERROR，无法判定的原因），后四项同用 `suppliers.reconcile.` 前缀。与队列丢弃任务的告警相同，在事务提交前记录，避免已提交摘牌却因进程崩溃漏报；事务回滚可能重复告警，不能把日志条数当作扣费笔数。摘牌由现有的供应商与路由快照重建传播（各 5 秒）。

1. **匹配**：一条语句，先占住只有这个对账键的那一个尝试，再把扣费记录归到它，尝试和扣费记录各有唯一约束，两个节点同时跑也只有一个成功。有两个以上尝试共用一个键时都不对上。已暂挂、已归到丢失请求或已确认未归属的扣费记录，尝试到了就改归到尝试，旧归属在同一条语句里撤销，所以归属的 CHECK 始终成立。
2. **判定**：只对估算完整（`cost_flag` 为 `complete`）的尝试：写行的 gateway 把上游一项用量都没报告的标 `no_usage`，估了一部分或有项没价格的标 `unpriced`（[gateway 的「尝试的结局」](../gateway/README.md)），所以估出来的数不会拿去判价格。检测的行（`test`）一律不判：它的估价是上界而不是估计，判它只会把付得起的端点摘掉（「用真实生成测试端点」）。|实际 − 估算| ≤ 供应商自己的取整（quota 为 1，美元为 1e-10）+ 估算 × 相对容差（默认 1%）。实际偏高：端点改回"成本未知"（原因 `under_estimated`），退出路由；偏低：只在报告里列出。
3. **暂挂**：过了宽限（默认 30 分钟）还没有对上的新扣费记录改为 `held`；有键的尝试过了宽限仍没有扣费记录，报告里列出，只告警，检测的尝试不在其中。人工处理经 `AssignToLostRequest`、`AssignUnattributed`，只对 `held` 的记录生效，不按冻结路线猜用户。
4. **扣费记录保留明细**：`charge_records.usage` 存每项的数量、记录声明了哪些项（`reported`，可以含声明为零的项，没声明的不在其中）、供应商给出但没有计费项的用量（`unknown`）和等级，因为只有声明了的项才能校正暂估。
5. **资金来源**：不是钱包的扣费记录（订阅）入库时存下来、不参与匹配、暂挂和事件，同时把供应商暂停（`needs_attention`，原因 `funding_not_wallet`，配置代次加一），重建快照后不再路由到它。
6. **有界**：一遍对账每种语句最多 `MaxBatches`（默认 20）批、每批 `Batch`（默认 500）行，没做完的留给下一遍，报告里 `More` 为真；等待中的行（`state='new'`、`state<>'matched'`、`state='pending'` 且有键）各有部分索引，代价随积压而不随历史增长；等待的尝试列表一批一批地列，`Config.After` 和报告里的 `Next`（按完成时间、请求、序号排序的游标）把它跨轮推进，列到末尾 `Next` 归零、下一遍从头开始，所以超过一批、始终对不上的尝试最终都会被报告一次。
7. **事件**：每次版本变化在同一事务里写一行 `charge_events`（扣费记录，版本，载荷），唯一键是（扣费记录，版本），重复运行不会重复。billing 的事件消费与成本分录校正尚未接入（[#28](https://github.com/TYCT-tyct/Axiom/issues/28)）；尝试归属的事件带布尔 `test`，检测为 `true`、真实调用为 `false`，billing 消费时据此跳过没有租户与账本的检测；本任务只做匹配、判定、暂挂、归属记录和告警，不修改用户收费。

## 没有对账日志

早期设计里，请求进行中由本模块每秒把每次尝试的对账键和"已提交"写进 `suppliers.attempt_journal`，供崩溃后的清理结算。设计上它够不着生产路径：刷写它的 `Run` 在生产里没人启动，gateway 追加的条目因此只攒在内存里，清理读到的永远是空的（[M1 计划](https://github.com/TYCT-tyct/Axiom/issues/147)）。它已从代码里删掉：`journal.go`、`Service.Journal`/`Settled`/`JournalFlushed`/`Run`、`PurgeJournal`/`JournalOf`、`call.sql` 的两条查询、gateway 的三处调用，以及 gateway 连接池里给它的一类名额。表与授权按收缩规则留到下一个发布删。

### 丢失的请求

崩溃留下的预留改用两件东西收尾，都在 database 里、都与结算同一次提交：

- **尝试的成本行**（`attempt_costs`）：结算事务里写，与账本的 `supplier_cost` 分录同成同败，所以「这次尝试花了多少钱、对账键是什么、用的是哪个端点」不会只留在崩溃的进程里（[计费的「结算与账本」](../billing/README.md#结算与账本)）。
- **丢失的请求**（`lost_requests`）：清理接管预留时写一行，带 `route_endpoint_ids`——预留冻结的路线里保留条目的端点（`billing.reservations.endpoints`，与 `prices` 等长同序）。没有尝试行时这是唯一可用的信息：这个请求可能被上游收过钱，钱最可能落在这些端点上。迟到的结算带着真实尝试回来时写 `lost_request_resolutions`。

代价是崩溃前那一刻的尝试没有任何记账：进程没来得及结算，就只有丢失的请求，没有成本行。原来的日志只是把这个窗口从「整个请求」缩到「最近一次写入之后」，而这个写入本身也在请求路径之外；M1 用「崩溃的请求记成丢失、对不上尝试的扣费暂挂后由人归属」接受这个窗口（[计费的「持有者与截止时间」](../../docs/domains/billing.md#持有者与截止时间)）。

`ended_requests`（`EndRequest`）原先的读者是日志的清理 `PurgeJournal`，它删了以后这张表没有读者了：billing 仍在请求进入终态的事务里写它，但 M1 里没有代码读它（对账按尝试与扣费记录匹配，不读它）。它同日志表一起在收缩的发布里删，连同 `EndRequest` 的调用（[Issue #213](https://github.com/TYCT-tyct/Axiom/issues/213)）；若在那之前有代码要读请求的结束时刻，就保留表，并在这里写明谁读。

## 决策与理由

| 决策 | 理由 | 不选 |
|---|---|---|
| 独立 River 周期任务执行供应商逐条对账，估低摘牌；归属、事件与后续页同事务 | 即使同步暂停或尝试迟到，已有记录也要继续核对；重试不能重复加版本，下一页不能只留在某个 worker 内存里 | 只在同步成功后运行；每个节点各起计时器。参考源码（只读，2026-10-18）：[new-api `BillingSession.Settle`](https://github.com/QuantumNous/new-api/blob/6cf0d90b5ff27f901697dddd61e0c28aba8efefc/service/billing_session.go#L47) 根据本地算出的实际额度调整预扣，[`quota.go`](https://github.com/QuantumNous/new-api/blob/6cf0d90b5ff27f901697dddd61e0c28aba8efefc/service/quota.go#L250) 写消费日志；[sub2api `usageBillingRepository.Apply`](https://github.com/Wei-Shaw/sub2api/blob/b0a02a3304a0a78cdd003ddda1dd23ba02c03084/backend/internal/repository/usage_billing_repo.go#L22) 在事务内按请求号与 API key 去重并应用计费。这些结算路径没有拉取供应商扣费记录来做容差比较或估低摘牌，Axiom 沿用自己的[对账规则](../../docs/domains/billing.md#供应商成本) |
| 单位价值按供应商存在 `suppliers.unit_values`，`Reload` 时读进快照，回执带着它和行 id | 它是供应商余额的属性（[计费的「供应商成本」](../../docs/domains/billing.md#供应商成本)），与管理接口按供应商确认一致；读进快照让 `Credits` 仍是纯内存，结算路径上不多一次查询；回执里的 id 让复算不靠时间推断（节点用的是它最近一次 Reload 时的快照，id 随结算落库，旧行未知） | 按端点存：同一份余额的值要逐端点改，漏改一个就算错；结算时查库：钱的路径上多一次往返；折进成本计划：计划是原生单位、对账按原生单位比，每次充值改值都要重写全部计划 |
| 用周期扫描补任务，不让每个任务在结束时入队它的后继 | River 的唯一性必须含"运行"这一状态，任务结束前不能为同一个供应商插入后继；不唯一的后继链会在多入队一次后永远多一条。扫描没有状态，节点挂了、任务丢了都自愈，代价是到期后最多晚一个扫描间隔 | 后继链加扫描补漏：两套机制，还要防链增殖 |
| 任务失败不由队列重试 | 重试的时间由引擎的退避决定，写在状态里，队列再重试一份会绕过它 | River 的重试：两份退避 |
| 适配器的 `Open` 推迟到任务第一次用 | 登录失败成为那个任务的失败，进入引擎的处理（需处理、退避）；提前打开则一轮之前就失败，引擎看不到 | 轮前打开：登录被拒时引擎不知道，下一轮还会再登录，正是要避免的封号 |
| 同步状态存成一份 jsonb，加格式号，不拆成列 | 引擎的状态整份读出、整份提交，字段随引擎演进；拆成列每加一个字段就要迁移，且读出时要拼回去。查询只需要 `next_due`，它单独成列。代价：不能用 SQL 查状态里的细节，要看时读 jsonb | 状态的每个字段一列：引擎一变就要迁移 |
| 栅栏是租约的 `lease_fence`，时钟只决定何时可以接手 | 两个节点的时钟差不能让它们同时写进去；写入由数据库里一个只增的数字挡住，与时钟、网络延迟、暂停无关 | 只靠 `lease_until > now()` 判断：被暂停的节点醒来时租约已过期，但这一刻它的判断与别的节点接手之间没有顺序 |
| 限速桶放数据库里，每个请求一个事务 | 多节点共享一个出口地址时，各自在内存里计数会让合计超过对方的限速，被封号的是预付的账号；管理面请求少，一次事务的代价可忽略 | 把名额均分给各节点：节点数一变就要重分，空闲节点的名额浪费 |
| 成本计划在 `sync_plans` 里按版本留全，投影时按指纹写进 `cost_schedules`，一处只存一份 | `sync_plans` 是版本的流水（引擎每步只提交差异，读起来便宜），`cost_schedules` 是端点在用的那一版，按 `(endpoint_id, fingerprint)` 唯一，同一版不会写两次；端点引用它，复算和审计看得到一次尝试当时按什么计价。投影按指纹回读计划本身，所以引擎的状态里只放指纹 | 只留 `sync_plans`、端点靠指纹去查：调用面每次要反查版本；只留 `cost_schedules`：引擎不知道哪一版是新的，也留不下没映射上名字的计划 |
| 端点只在拿到成本计划后生成，计划消失时把成本置为未知而不是删行、也不动 `enabled` | 端点存在就等于"有东西可以按这个价调"，`ListRoutable` 的条件与写入侧一致；行留着，`attempt_costs` 引用的成本计划版本和人的开关都不丢，计划回来时是同一个端点 | 先生成端点为未知：后台多出一堆不可用的行；计划消失时改成禁用：分不清是人停的还是同步停的，人停用的语义被吞掉 |
| 成本行写进 billing 的结算事务，不另起一份「进行中尝试」的日志 | 一个事务里成或不成，所以「这次尝试花了多少钱、对账键是什么、哪个端点」与账本同一次提交；崩溃后剩下的是丢失的请求（带预留冻结的路线），不需要第二张表、第二个写入线程和第二个连接池名额 | 进程内日志每秒批量落库：多一条写入路径、一类连接名额，而它只把崩溃窗口缩小一点，仍无法自动确认崩溃前最后一次尝试的归属 |
| 分组代号在第一次选用时随机生成，取消选用不收回 | 用户看到的是代号不是上游名字，换回来或重选不能让同一个分组换名字 | 由名字派生：名字泄露代号；每次选用重新生成：用户记下的代号失效 |
| 检测成本、审计和幂等响应共用事务，生成期间保留幂等认领 | 同键并发请求在认领上等待，正常提交后重放同一答案；代价是外部 HTTP 期间（最多 45 秒）占用一个数据库连接并保留事务快照，生成后的回滚会丢失成本和认领、允许再次付费（见「成本行」）。每会话每分钟 10 次限制触发频率，不保证回滚后的付费至多一次 | 仅另开短事务补写成本不能阻止同键重试再次生成，原事务提交结果不明时也不能独立判定响应归属；彻底消除失败窗口需要持久化进行中的认领及恢复协议，不在本次 H1.5 中引入 |
| 并发和每分钟请求上限按凭证存两列，管理接口一起设定，计数在各节点内存里 | 同一供应商账号的全部 key 和端点共用 RPM，配置上限按待选凭证应用（见「并发与速率」）；上限存在行上，`Reload` 随端点读进快照（5 秒内生效），调用路径仍是纯内存。窗口是 61 个按首次准入起的单调秒索引、各带秒标记的一秒桶：[now-60, now] 的合计就是滚动一分钟的计数，时钟在账号锁里读，过期桶读作 0，所以没有计时器、没有清理、内存不随上限增长，也没有每次调用一次事务；任意滚动 60 秒不超过上限，代价是边界上最多少放行不到一秒的请求。窗口用完的凭证与并发位用完的一样返回 `ErrNoSlot`，gateway 跳过条目，最终回 429 `rate_limited`（指标原因 `credentials_busy`）、`Retry-After: 1`（上限不超过一分钟）。RPM 上界是 100,000，管理接口与写入校验拒绝更大的值，计数饱和在那里；滚动发布期间旧节点写入的更大值按上界读，数据库的上限 CHECK 待旧节点退出后再加。代价：RPM 不跨节点，共享计数随 Valkey 后续实现；并发沿用静态份额设计；TPM 等有可信的额度数据时再加，现在只有余额和 key 状态 | 只靠 `client.Limiter` 的桶：那是同步和适配器自己的限速，调用量由用户的请求决定，因此拒绝用户是把计量和业务搞混；计数放数据库（`rate_buckets`，`SharedLimiter` 的做法）：每次调用一次事务，热路径上多一次往返；`golang.org/x/time/rate`：多一个依赖，其令牌补充口径也不等同于按秒计数的滚动窗口；保存每次请求的时间戳：内存随上限增长，未设限的 key 更会无限增长 |
| RPM 计数按账号共用，上限仍按凭证设定 | 同一上游额度不会因新增 key 增加，仍可按 key 单独压低上限；共享额度的 key 应设相同值 | 上限存在 `suppliers` 上、凭证继承：无法按 key 单独压低 |
| 新绑定的默认是并发 4、每分钟请求不限 | 绑定时对方允许几个并发还不知道，用保守值，免得一次打满对方；每分钟不设，因为限流值猜错会让本来能发的请求失败，不设只是没有额外的天花板 | 不设并发上限：新接入的供应商可能被打满；给每分钟一个默认数字：猜错就拒了本来能发的请求 |
