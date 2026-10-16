# suppliers

供应商模块：供应商及其账号凭证、分组、模型、端点的表，适配器的契约和注册，以及调用路径上的接口（挑凭证、发请求、记尝试、写对账日志）。适配器、同步、对账的要求见[供应商](../../docs/domains/suppliers.md)和[计费的「供应商成本」](../../docs/domains/billing.md#供应商成本)，一次调用的编排见 [gateway](../gateway/README.md)。

> [Issue #25](https://github.com/TYCT-tyct/Axiom/issues/25) 分多个 PR 实现。已实现：表、适配器契约、注册表、启动检查、new-api 适配器、调用面（挑凭证、发请求、记尝试、对账日志）和对账，写在「代码结构」到「对账」；同步引擎和管理接口在后续 PR。「对账日志」一节是日志的设计，已实现，实现的接口在「调用面」。代码与本文不一致时，在同一个 PR 里改本文。

## 代码结构

```
internal/suppliers/                 公开接口：Module()、CheckAdapters()、Service（调用面）、RecordAttempts 等、Reconcile
internal/suppliers/internal/
  adapter/       契约：Adapter、Manager、CallQuirks、Failure、CallPolicy、Client、BucketSpec
  registry/      注册表：唯一同时引用所有适配器的文件，一个适配器一行；按 kind 查找
  client/        适配器唯一的出站入口：经 platform/egress、按声明的限速桶放行
  newapi/        new-api 适配器（含 testdata/ 录制样本和测试）
  adaptertest/   每个适配器都要过的契约测试（只被测试引用）
  reconcile/     对账：匹配、判定、暂挂、事件
  store/         迁移、查询（sqlc 生成）
  removable/     `just adapters-removable` 的程序
```

后续 PR 增加其余适配器的包（`sub2api/`）、`wire/`（与系统无关的路径和鉴权头）和同步引擎。

## 表

schema `suppliers`，迁移在 `internal/suppliers/internal/store/migrations/`（设计草稿里的 `store/migrations/` 按[总体架构](../../docs/architecture.md)的 `internal/` 规则改为此路径），版本表 `platform.goose_suppliers`。`00001` 是配置表；`00002` 是调用面和对账的表。同步状态、限速桶、租约和单位价值随写它们的代码加入，迁移只增不改。

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

- **授权的偏差**：设计里这几张表只有对账角色能改，那个后台任务角色还没有（security.md 的角色表），所以应用角色现在能改 `attempt_costs` 的 `charge_record_id`、`state` 和 `charge_records` 的状态与归属列，别的列、DELETE 都没有；角色隔离随后台任务角色加入。
- **行级安全**：`suppliers` 的策略是 `platform.in_scope(site_id, account_id)`，平台的供应商只有平台范围看得见；其余各表不存归属列，只存 `supplier_id`，策略是"这个供应商对本事务可见"，所以插入也挡住"挂到看不见的供应商下"，也不会有与父对象不一致的标签。
- **复合外键**：凡同时存 `supplier_id` 和 `supplier_group_id` 的表，用 `(supplier_group_id, supplier_id) → supplier_groups (id, supplier_id)`，分组与行不可能属于不同供应商。别名的分组可空，空表示整个供应商。
- **授权**：应用角色对配置表没有 DELETE（移除供应商是状态，要删密文行的移除流程随它的代码加授权），只有对账日志可以删（结算两天后清除）；UPDATE 按列授予，不含 `id`、`supplier_id`、归属列、`kind`、`origin`、`upstream_account`：身份和凭证发往的地址不能改。
- **密钥**：表里没有明文列。加密值写进 `ciphertext`、`nonce`、`wrapped_dek`、`key_version` 四列，数据库只检查字段齐全和长度（nonce 12 字节、密文不短于 16 字节、包裹的数据密钥非空、版本号合法），**不能**分辨密文和别的字节：任何满足长度的明文也写得进去。加密由写入边界保证（只把 `secret.Sealed` 交给查询），值是否真实、是否属于这一行由 `secret.Open` 验证。`secret.Location` 的行 ID 在加密前就要知道，所以 `credentials.id` 由调用方生成，不用默认值。
- **跨模块引用**：`model_id` 是目录的 ID，没有外键；`endpoints.cost_schedule_id` 与 `cost_schedules` 用（成本计划，端点）复合外键。

## 适配器

- **契约**（`adapter`）：`Adapter` 有 `Kind`、`Buckets`、`Detect`、`Open`、`Calls`；`Open` 返回只读的 `Manager`（账户、报价、key 状态、按对方 ID 读 key 原文、扣费记录分页；每笔记录带标准化用量 `catalog.Usage` 和"已报告的项"集合，没报告和报告为零不同，暂估才能按项、按请求锁定的单价校正）；`Calls` 返回 `CallQuirks`（`Formats`、`ChargeRef`、`Classify`、`Policy`）。写能力以后用单独的可选接口加入，能力就是实现了什么。
- **`Failure`** 是 `{Executed, Scope, Cause}`。`Cause` 是封闭的和类型（`gochecksumtype` 检查每个 switch 都处理了全部成员），成员只带数字和时间，**没有来自对方的文本**，所以回显了凭证的响应进不了日志和表。`Executed` 的零值是 `ExecutionUnknown`：没把握就按"可能执行了"处理，成本按估算记、由对账补正；`NotExecuted` 只给对方在执行前就拒绝的响应。`Transport` 只由通用调用层设置，适配器不得返回。作用范围比设计草稿多一个 `Endpoint`（限流、过载作用于一个分组和模型）。
- **`Client`** 是适配器唯一的出站入口，`Request` 声明占用的全部桶，`BucketSpec` 声明对方的限速；任一桶没有名额就不发，返回包装了 `ErrBucketsFull` 的错误，调用方推迟而不是当失败。实现是 `internal/suppliers/internal/client`（进程内计数）；多节点共享一个出口地址时的数据库版（`rate_buckets` 表）随同步引擎加入。
- **注册表**（`registry`）：`adapters` 列表是唯一同时引用所有适配器的地方，一行一个，不用 `init()`。`registry.New` 在启动时拒绝不合法的 `kind`、重复的 `kind`、不可能放行请求的限速桶。
- **移除**：删适配器的目录和注册表里它的那一行。`just adapters-removable` 对注册表里的每个适配器，用 `go build -overlay` 在不改工作区的前提下删掉二者，再跑 `go build ./...` 和 `go vet ./...`；还有别的代码依赖它时失败，并给出编译器的原话。注册表里现在有 new-api，检查对它真实生效。
- **启动检查**：`CheckAdapters` 在 `serve` 启动时列出仍在使用（状态不是"已移除"）而本程序没有注册适配器的供应商，有就拒绝启动并逐个指出名字和 `kind`。它要看到全部供应商，所以由 `cmd/axiom` 传入平台范围。

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

- **一遍** 读固定的 `[start, end]`（服务端按秒级时间戳过滤），读到不满一页（含恰好整页之后的空页）为止。前进遍：`start = 水位 − 5 分钟`，`end = min(水位 + 跨度, 现在 − 30 秒)`，读完把水位升到 `end`，水位只升不降；水位之后不足一分钟时不发请求，返回空页和 `Done`。每天一次的补扫遍读最近 24 小时，不动水位，用来拿比重叠回扫更晚到的记录；它从"上一次补扫覆盖到的位置往前一天"开始，所以每一段时间在一天之后还会被再读一遍，补扫晚了几天也不留空隙；晚到不超过一天的记录，哪怕属于已被补扫覆盖过的一段，也被下一遍补扫读到（前进遍的 5 分钟重叠只管更近的晚到）。
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
| 失败不带对方的文本，没把握不算免费 | `Cause` 的成员；`adaptertest` |
| 正常供应商一定有注册的适配器；删任一适配器其余照常编译 | `CheckAdapters`；`just adapters-removable`（现在对 new-api 真实生效） |
| 不超过对方的限速，被限速时不丢进度 | `client.Limiter` 全部取到名额才发、429 封全部所占的桶；`Charges` 出错时返回原位置；扫描测试在小名额和 12,000 条记录下统计每个窗口的请求数 |
| 扣费水位只升不降，每条记录至少读到一次 | 前进遍的 `end` 大于水位、水位取最大值；新记录只出现在列表最前面；测试覆盖整页边界、扫描期间新增、迟到记录（重叠回扫和每日补扫） |
| 凭证只发往接入时的主机，错误里没有对方的文字 | `client.New` 只接受裸 origin，路径不能改主机；管理接口的错误只带固定清单里的 `code` 和权限名 |

## 调用面

已实现（`service.go`、`attempt.go`、`journal.go`、`record.go`）。`Service` 是一个节点上的调用面：快照、凭证运行状态、对账日志缓冲、成本计划。

```go
func New(cfg Config) (*Service, error)                        // DB、平台范围、主密钥、出口、适配器注册表、时钟
func (s *Service) Reload(ctx context.Context) error            // 重建快照，在本节点内存里解密 key；key 打不开或缺适配器就失败，旧快照不动
func (s *Service) Acquire(ep EndpointID) (*Attempt, error)     // 内存比较交换；ErrNoEndpoint、ErrNoSlot
func (a *Attempt) Send(ctx context.Context, c Call) (*http.Response, *Failure) // 内含 Classify
func (a *Attempt) Receipt() AttemptReceipt                     // 到目前为止的回执，给对账日志
func (a *Attempt) Done() AttemptReceipt                        // 幂等；释放占位；带凭证、端点、成本计划版本、已到的对账键
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

与设计草图的差别：`RecordAttempts` 等是包级函数（不需要 `Service`）；`Cost` 没有时间参数和单位价值（单位价值表随 billing 的换算加入）；`Send` 的入参是 `Call{Format, Body}`，`protocol.Upstream` 合并后换成它。`Failure` 是 `adapter.Failure`。

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
| 每秒批量的对账日志，不在发请求前单独写；只有标了已提交的尝试能让清理向用户收费 | 每节点每秒一个事务，崩溃窗口从整个请求时长缩到最近一次成功写入之后，计费的清理结算才走得到。日志看不到之后的尝试，所以只认已提交的：提交后不再换端点，已提交的必是最后一次 | 每次尝试先写一行：往返随尝试数增长 |
