# catalog

模型、名字、官方规格和参考价，以及目录、成本计划、售价共用的价目表结构。要求和业务设计见[模型目录](../../docs/domains/catalog.md)，这里写代码结构、数据、不变量和接口。

> 本文先于代码写成，是目录模块的完整设计；各部分随实现的 PR 落地，代码与本文不一致时，在同一个 PR 里改本文。已实现的是价目表结构与计价（#70、#71）：计费项和服务等级及其集合，`Usage`（`NewUsage`、`Unknown`、`OutputTotal`、`Count`、`ServiceTier`），`Schedule`（`ParseSchedule`、`MarshalJSON`、`Cost`、`Ceiling`、`Missing`、`Lost`）和 `Reportable`，以及「数值域」「存储」两节规定的数值和存储格式；以及存储、快照与管理操作：表 `author`、`model`、`name`、`reference_price`、`revision`、`change`，行级安全与授权，`Normalize`，内存快照与名字解析，管理操作和 `axiom catalog` 命令；以及 LiteLLM、OpenRouter 两个数据集的取数、解析、自动发布、改选到数据集和第一版验收的只读检查（#24，见「参考价的录入」）。页面监测、审核项、数据集核对、后台任务角色和 `lock_models` 是设计，尚未实现，各节标题下有标注。

## 包

| 包 | 做什么 |
|---|---|
| `catalog` | 价目表结构与计价；名字的归一化与解析；内存快照；管理操作；数据集取数、解析与自动发布、改选到数据集、只读检查（均已实现）；页面监测与数据集核对（设计，尚未实现） |
| `catalog/migrations` | schema `catalog` 的表、授权和行级安全策略，版本表 `platform.goose_catalog`（`watched_page` 等页面与数据集的表随它们的 PR 加入） |
| `catalog/queries` | sqlc 的查询，生成的 `*.gen.go` 在 `catalog` 包里；生成的 `Queries` 构造函数叫 `New`，所以目录的构造函数叫 `Open` |

## 数据

### 表（schema `catalog`）

> 已实现：`author`、`model`、`name`、`reference_price`、`revision`、`change`。设计，尚未实现：`watched_page`、`page_snapshot`、`page_observation`、`dataset` / `dataset_entry`、`review`（页面监测与数据集的 PR 一起加入）。数据集同步现在不建表：每一轮取数后条目在内存里与模型对应，没发布的结果在报告和日志里，下一轮重新算，所以不丢；`dataset` / `dataset_entry` / `review` 随页面监测与审核项一起加入。

"只追加"指 `axiom_app` 没有 UPDATE、DELETE 权限。类别的含义见[安全的「租户隔离」](../../docs/security.md#租户隔离)。

| 表 | 主要列 | 键与约束 | 类别 | 写法 |
|---|---|---|---|---|
| `author` | `slug`、`name` | PK `slug` | 公开 | 增、改名 |
| `model` | `id`、`author`、`slug`、`status`（draft / listed / retired）、`version_basis`（dated / floating）、`price_source`（litellm / openrouter / official / manual）、`spec` jsonb、`rev` | PK `id`；UNIQUE `slug`；`split_part(slug, '/', 1) = author` | 公开，draft 行只有平台可见 | 列级授权只许改 `status`、`price_source`、`spec`、`rev` |
| `name` | `name`（原样）、`norm`、`visibility`（public / internal）、`model_id` | PK `name`；UNIQUE `norm`；public 时 `norm !~ '-latest$' AND norm !~ '[:~@]'`；所有名字 `norm !~ '[:~]'` | 公开，只含 listed 模型的名字，其余只有平台可见 | 只追加 |
| `reference_price` | `id`、`model_id`、`currency`、`source`、`schedule` jsonb（撤下时为空）、`schedule_sha256`、`published_at`、`supersedes` | PK `id`；UNIQUE `supersedes`；部分唯一索引 `(model_id, currency) WHERE supersedes IS NULL`；UNIQUE `(id, model_id, currency)`，外键 `(supersedes, model_id, currency)` 引用它，只能替代同一条时间线上的行；`supersedes <> id`；`currency ~ '^[A-Z]{3}$'`，`schedule` 与 `schedule_sha256` 同时为空或同时非空，非空时币种等于 `schedule` 里的币种 | 公开，只含 listed 模型的参考价，其余只有平台可见 | 只追加 |
| `revision` | 单行 `n` | 只有影响快照的写入才加一 | 公开 | 改 |
| `change` | `id`、`op_key` uuid、`at`、`actor`、`reason`、`kind`、`subject`、`before`、`after`、`evidence` jsonb | PK `id`；UNIQUE `op_key` | 平台 | 只追加 |
| `watched_page` | `id`、`author`、`url`、`currency`、`section_start`、`section_end`（截取价格段的标记）、`interval`、`enabled`、`lease_owner`、`lease_gen`、`lease_until`、`last_ok_at`、`failures`、`current_snapshot_id` | PK `id`；`url ~ '^https://'` | 平台 | 改 |
| `page_snapshot` | `id`、`page_id`、`first_seen_at`、`extractor_version`、`text`（截取并规范化的正文）、`text_sha256` | PK `id`；UNIQUE `(page_id, extractor_version, text_sha256)` | 平台 | 只追加 |
| `page_observation` | `page_id`、`observed_at`、`snapshot_id` | PK `(page_id, observed_at)`；只在当前内容变化时追加 | 平台 | 只追加 |
| `dataset` / `dataset_entry` | 数据集（litellm / openrouter / modelsdev）及其租约和健康状态；条目的 `external_id`、`model_id`（可空）、`schedule`、`spec`、`parser_version`、`fetch_id`、`seen_at` | PK `(dataset, external_id)` | 平台 | 改 |
| `review` | `id`、`subject_key`、`kind`（page_changed / price_change / mismatch / new_model / parse_failed）、`content` jsonb、`content_sha256`、`content_seq`、`status`（open / done / dismissed）、`resolution` | PK `id`；部分唯一索引 `subject_key WHERE status = 'open'`；dismissed 的 `(subject_key, content_sha256)` 不再重开 | 平台 | 改 `content`、`content_seq`、`status`、`resolution` |

- `model.id` 是 UUIDv7，Go 类型 `catalog.ModelID`。
- `model.slug` 是后台和日志用的唯一标识：`<厂商>/<模型>`，厂商给出版本标识时加 `@<版本>`；厂商部分 `^[a-z0-9][a-z0-9-]{0,39}$`。它不是名字，不进 `name` 表。
- 标准 ID 是用户调用的 `<厂商>/<模型>`，存成一个 public 的 `name`，格式 `^<厂商>/[a-z0-9][a-z0-9._-]{0,99}$`，不含 `@`。
- `name` 上的 CHECK 只是兜底，上架审核时逐个确认可见性。改 `Normalize` 的规则要随迁移重算 `norm` 并重新检查唯一性。
- 参考价的历史以 `supersedes` 串起；当前价是没有被替代的那一行，它是撤下行（`schedule` 为空）时这个币种没有当前价。更正也是发布新的一行。`source` 记下这一行来自哪个价格来源，证据在对应的 `change` 里。
- `spec` 原地更新，旧值在 `change` 里。它包括目录基线的全部规格和计费要用的模型数据，其中 `items`、`tiers` 是上游可能报告的计费项和服务等级（见「上架门槛与缺项」）。
- `model.rev` 是改模型时的乐观并发令牌：`ListModel`、`Retire`、`UpdateSpec`、`SelectPriceSource` 带上审核时看到的 `rev`，锁模型行后比较，不同就拒绝，成功后加一。没有它，两个基于同一份规格的修改，后提交的会悄悄覆盖先提交的。测试：基于同一 `rev` 的两个 `UpdateSpec` 只成功一个。
- `subject_key` 非空且稳定：`page:<page_id>`、`price:<model_id>:<currency>`、`mismatch:<model_id>:<dataset>:<currency>`、`new:<dataset>:<external_id>`。没关联模型的条目用来源身份，不同的新模型不会合成一条。处理审核项时绑定 `content_seq`，期间内容又变了就处理失败。
- `actor` 暂存不透明字符串（`agent:<id>`、`user:<id>`、`system:watch`、`system:sync`），#23 定义操作者类型后替换。

### 行级安全与权限

> 已实现：公开数据和平台数据的策略与授权。设计，尚未实现：后台任务角色 `axiom_catalog_job` 和 `catalog.lock_models`。

- **公开数据**（`author`、`model`、`name`、`reference_price`、`revision`）：读用 `platform.in_any_scope()`（任意非空范围），写用 `platform.in_platform_scope()`。`model` 的读策略另加 `status <> 'draft' OR platform.in_platform_scope()`；`name` 和 `reference_price` 的读策略另加"所属模型已 listed，或在平台范围"，所以给草稿模型录的参考价不会提前公开。
- **平台数据**（其余各表）：读写都用 `platform.in_platform_scope()`。
- 后台任务以 `axiom_catalog_job` 角色执行：由本模块的迁移建立，只授予它要写的表：平台数据表，以及自动发布要写的 `reference_price`、`revision`；任务的事务用 `pg.Tx.SetRole` 切过去。
- **现在的加锁**：管理操作以 `axiom_app` 执行，它对 `model` 的 `status`、`price_source`、`spec`、`rev` 有 UPDATE 权限，所以直接 `SELECT … FOR UPDATE`（`LockModel`），每个操作只锁一个模型，不需要排序。后台任务角色出现时才需要下面的 `lock_models`。
- 模型行只经 `catalog.lock_models(ids uuid[])` 加锁（设计，随后台任务角色一起实现）：`SECURITY DEFINER`，属主 `axiom_owner`，固定 `search_path`，按 id 升序 `SELECT … FOR UPDATE` 并返回 `price_source`、`rev`；只把 EXECUTE 授予 `axiom_app` 和 `axiom_catalog_job`。它是运行时唯一以 `axiom_owner` 身份执行的代码，只加锁、不写。PostgreSQL 的行锁要求对该表至少一列有 UPDATE 权限（PostgreSQL 文档《SELECT》），任务角色没有 `model` 的 UPDATE 权限，也不该有：给它任何一列都等于允许它改这一列。表强制启用行级安全，所以函数里的查询仍受范围约束。测试：切到任务角色后自动发布成功；直接 `SELECT … FOR UPDATE` 模型行被拒。
- `in_any_scope()`、`in_platform_scope()`、切换角色的 `pg.Tx.SetRole` 和生产代码可用的平台范围 `pg.PlatformScope` 由 platform 提供。

## 价目表

### 计费项与用量

`catalog.Item` 是封闭枚举，`ItemSet` 是它的位集合。各项互不重叠，同一个 token 只计一次；`protocol` 按下表把上游报告的用量折成 `Usage`：

| Item | 单位 | OpenAI（Chat / Responses） | Anthropic Messages | Gemini |
|---|---|---|---|---|
| `input` | token | `prompt_tokens − cached_tokens − cache_write_tokens − audio_tokens`（缺的字段按 0） | `input_tokens`（含图片） | `promptTokenCount − cachedContentTokenCount` − 单独定价的模态 |
| `cache_read` | token | `cached_tokens` | `cache_read_input_tokens` | `cachedContentTokenCount` |
| `cache_write` | token | `cache_write_tokens` | `cache_creation.ephemeral_5m_input_tokens` | — |
| `cache_write_1h` | token | — | `cache_creation.ephemeral_1h_input_tokens` | — |
| `output` | token | `completion_tokens − reasoning_tokens − audio_tokens` | `output_tokens`（含思考，不单列） | `candidatesTokenCount` |
| `reasoning` | token | `reasoning_tokens` | 0 | `thoughtsTokenCount` |
| `audio_in` / `audio_out` | token | 输入、输出详情里的 `audio_tokens` | — | `promptTokensDetails` 里模态为 `AUDIO` 的数量 / — |
| `image_in` / `image_out` | token | — / 图片生成的输出 token | — | 单独定价时，`promptTokensDetails` 里模态为 `IMAGE`、`VIDEO` 的数量 / — |
| `web_search` | 次 | 待核对 | `server_tool_use.web_search_requests` | 待核对 |
| `code_execution` | 次 | 待核对 | `server_tool_use.code_execution_requests` | 待核对 |

- 只有上游单独报告、厂商单独定价的模态才折成自己的项；Anthropic 的图片按 token 计入 `input_tokens`、价格相同，就留在 `input`。视频没有自己的计费项，计入 `image_in`。例如 Gemini 2.5 Flash 的音频输入每百万 $1.00、文本 $0.30，Gemini 3.6 Flash 的图片和视频每百万 $1.00、文本 $0.75（Gemini 定价页，2026-10-14）。
- 守恒：输入侧各项之和等于上游报告的提示词总量；输出侧各项之和等于上游报告的输出总量。测试断言这两条。Anthropic 没有单独的提示词总量字段，按 `input_tokens + cache_read_input_tokens + cache_creation_input_tokens` 算（Anthropic《Prompt caching》）。
- OpenAI 的总输入同时包含缓存读和缓存写，写入价替代普通输入价而不是叠加（OpenAI《Prompt caching》）。例：`input_tokens=12000, cached_tokens=0, cache_write_tokens=12000` → `input` 0、`cache_write` 12,000，分档门槛求和 12,000，费用只含写入价。没有写入计费的模型不报这个字段，按 0。
- Anthropic 的缓存写入以顶层的 `cache_creation_input_tokens` 为准，它等于 `cache_creation` 里各项之和（Anthropic《Prompt caching》）。有 `cache_creation` 时断言 `ephemeral_5m_input_tokens + ephemeral_1h_input_tokens = cache_creation_input_tokens`，不等就是用量错误；没有时（流式的部分事件、只转发顶层字段的中转），按请求里的缓存断点归类：没有 `ttl:"1h"` 的断点，全部记 `cache_write`；有，就全部记 `cache_write_1h`，拆不开时取贵的一项。
- 已核对（2026-10-14）：Gemini 的 `promptTokenCount` 包含缓存的内容（Gemini API《UsageMetadata》："this includes the number of tokens in the cached content"），所以 `input` 是两者之差；OpenAI Responses 的字段是 `usage.input_tokens_details.cached_tokens` 和 `cache_write_tokens`（OpenAI《Prompt caching》）。其余（Chat 的缓存写入和两种格式的音频字段路径、图片输出和联网搜索的计数字段、Gemini 按模态的明细是否包含缓存）由 `protocol` 实现这张表时按各家的接口参考核对，并写进它的测试：折算的代码在 `protocol`，本模块只接收 `Usage`。
- **代码执行按次计**，是[计费的「定价」](../../docs/domains/billing.md#定价)列出的按次工具，缺价时用站点级默认单价（[计费的「价目表缺项」](../../docs/domains/billing.md#价目表缺项)），不进 `Unknown()`。厂商不按次收费：Anthropic 按容器的运行小时（每小时 $0.05，与联网搜索或网页抓取同用时免费），OpenAI 按 20 分钟的容器会话（按分钟计、最少 5 分钟），Gemini 只按 token（Anthropic、OpenAI、Gemini 的定价页，2026-10-14），用量里也没有时长；所以每次的单价是按这些计法折算的近似值。
- **不认识的用量**：上游报告了映射表里没有、数值不为零的用量字段时，`protocol` 把字段名和数量放进 `Usage.Unknown()`，不丢弃，也不归进已有的项。计费据此按[计费的「价目表缺项」](../../docs/domains/billing.md#价目表缺项)处理：模型停止接受新请求，在途请求按兜底价暂估。
- **服务等级**：`Usage` 的服务等级 是这次实际服务的等级，封闭枚举 `ServiceTier`（standard、flex、priority、fast），`protocol` 按每家的取值表从响应里读（OpenAI 的 `service_tier`，Anthropic 的 `usage.service_tier` 和快速模式，Gemini 待核对）；响应没有回报时取请求里明确写的等级，都没有就是标准档。认不出的取值同样放进 `Unknown()`，不猜成标准档：这时 `ServiceTier` 填标准档只是占位，`Unknown()` 不为空，计费就按「价目表缺项」处理，不只看 `Cost` 的结果。`UnknownUsage{Field, Value}` 是上游的字段路径和原样的取值：数量不一定是整数，等级是字符串，由计费解读。
- 免单判断用 `Usage.OutputTotal()`（`output + reasoning`），只看 `output` 会把只有推理的回复误判为零输出。
- Anthropic 联网搜索每千次 $10，搜索带回的内容按普通 token 计入输入（Anthropic Pricing 页，2026-10-14）。

### 条件

价目表声明一个门槛口径 `TierBasis`（各档共用）和若干 `ContextTier{Above, Rates}`，计法是整单按档：`TierBasis` 里各项用量之和严格大于 `Above` 时，`Rates` 列出的项换价，没列出的沿用基础价。门槛严格递增，取满足条件的最高一档；口径只有一个，所以用量越大，档位只会越高。`TierBasis` 按厂商原文的口径列出全部对应的计费项：从某个总量里拆出去单独计价的项（如 Gemini 从提示词里拆出的音频、图片和视频）仍属于那个总量。已知的两种：

- Anthropic Haiku 5.5：超过 100,000 token，"prompt length counts all of its input tokens, including cache reads and cache writes"，`TierBasis` = {`input`, `cache_read`, `cache_write`, `cache_write_1h`}；Claude 4.6 及以后的其他模型 1M 内统一价（Anthropic Pricing 页，2026-10-14）。
- Gemini：提示词超过 200k，`TierBasis` = {`input`, `cache_read`, `audio_in`, `image_in`}，输出和缓存价同时换档（Gemini 定价页，2026-10-14）。

标准档以外的每个服务等级（如 flex、priority、快速模式）有自己的一整套单价，组合规则：

- **先选服务等级，再选档位**：`Usage` 的服务等级 是标准档时用价目表顶层的基础价和 `ContextTier`，否则用该等级的 `Rates` 和 `ContextTiers`；档位按共用的 `TierBasis` 选，`Rates` 列出的项换价，没列出的沿用该等级的 `Rates`。
- **不跨等级继承**：等级的 `Rates` 没列出的项就是这个等级没有价格，不沿用标准档。沿用会把 priority 下没录的缓存读按标准价悄悄少收；缺价能被报告，由计费按「价目表缺项」处理。`reasoning` 回退到同一等级、同一档的 `output`，这仍是唯一的回退。
- **门槛一致**：每个等级的 `ContextTiers` 的门槛必须与顶层的门槛逐个相同，不换价的档位写空的 `Rates`。否则 `ParseSchedule` 拒绝：等级漏写长上下文档时，长请求会按该等级的基础价算低。
- 价目表没列出的等级就是没有它的价格。例：标准档输出 2、200k 以上 4，priority 输出 3、200k 以上 5，priority 的长请求输出按 5 计。

「价目表的结构」里的超出部分按档和倍率型条件，随第一个需要它的价目表带着完整的规则加入；在那之前 `ParseSchedule` 拒绝它们和任何不认识的条件类型（所以没有分时段价格），也拒绝门槛不递增、有档位却没有 `TierBasis`、服务等级的门槛与顶层不同、等级重复或写成标准档。快速模式就是服务等级 `fast`，用一整套单价表达，不用倍率。计法在存储里写明（`"method":"whole"`），不靠默认。拒绝的是价目表，不是请求：需要它们的成本计划，端点不使用（见[计费的「供应商成本」](../../docs/domains/billing.md#供应商成本)）；需要它们的数据集条目，不能被选为价格来源。

### 上架门槛与缺项

> `Schedule.Missing`、`Schedule.Lost` 和 `ErrTierBasisChanged` 已实现，上架门槛见「管理操作」。`Model.Reportable()` 已实现。

- **上游可能报告什么**是模型数据 `spec.items` 和 `spec.tiers`，审核时确认：输入输出类型、工具和厂商提供的服务等级决定它，例如支持音频输入就有 `audio_in`，有联网搜索就有 `web_search`，OpenAI 有 flex、priority。`Model.Reportable()` 返回它。
- **`Schedule.Missing(Reportable) Reportable`** 返回其中没有价格的项和等级，算上 `reasoning` 回退到 `output`：标准档在任何一档缺价的项，以及价目表没有、或任何一档缺任何一个可能报告的项的等级。计费用它检查售价是否满足[计费的「定价」](../../docs/domains/billing.md#定价)里的上架门槛（已知的按次项目可以由站点默认单价补上）。门槛在 billing 检查，不在本模块：目录在模块依赖里位于 billing 之下，不知道售价。
- **`Schedule.Lost(prev Schedule, r Reportable) ([]PricePoint, error)`** 返回 `prev` 有价格而本价目表没有的位置 `PricePoint{ServiceTier, Above, Item}`，只看 `r` 里的等级和项，两边都算上 `reasoning` 回退到 `output`。区间按两张价目表门槛的并集切分：每个区间里两边的价各自不变，所以逐个（等级、区间、计费项）比较就是精确的。这只在两张价目表的 `TierBasis` 相同时成立：口径不同（例如旧表按 {`input`, `cache_read`} 算门槛、新表只按 {`input`}），同一个数字门槛对应的用量范围就不同，两边都有分档时两张表不可比较，`Lost` 返回 `ErrTierBasisChanged`，不返回位置列表；只有一边有分档时，另一边的价格与口径无关，按那一边的门槛切分后照常比较（新价目表去掉旧门槛就是这样）；两边都没有分档时同样照常比较。替换参考价前用它防止价格覆盖退化；不用 `Missing` 比较，因为它把一个等级里的缺项合成一个等级，旧 priority 只缺 `web_search`、新 priority 连输入价都没有时两边的结果相同。
- **`Cost` 不因缺价失败**：它给有价格的项计价，并返回用量不为零却没有价格的项。`Usage` 的服务等级 在价目表里没有价格时，用量不为零的各项都算缺价。billing 按「价目表缺项」给这些项兜底，所以红线"不会算不出钱"不靠本模块拒绝请求。
- **`Ceiling` 给预留用的单价上界**：这些项在这些服务等级里、在基数合计不超过 `length` 的提示词可能落入的每一档中的最高单价，任一处缺价返回 false。档位只按基数里的项求和，提示词在基数内外怎么拆分都可能落到 `length` 以下的任一档，价格也不一定随档位上升，所以只看某一档或某一项都不是上界。
- **能力不归这里**：模型根本不支持的功能（例如纯文本模型收到音频）由路由按端点能力过滤；本模块不在调用前按请求特征拒绝。

### 数值域

- `platform.UnitPrice`：int64，单位 10⁻¹⁵ 个币种单位每计费单位，取值 0 … 10¹⁸（每单位最多 1000 个币种单位）。只有两种构造途径：录入的单价从十进制文本构造，超过 15 位小数或超出范围时拒绝保存；算出来的单价（如成本计划里官方价乘分组倍率）从精确分数向上取整到 10⁻¹⁵，超出范围时拒绝（见[计费的「钱的表示」](../../docs/domains/billing.md#钱的表示)）。
- `Usage` 每项 0 … 10¹⁰，字段不导出，只能经 `NewUsage` 构造，负数或超限返回错误；零值是全零的合法用量。不变量由构造守住，`Cost` 不再校验。门槛求和最多 `NumItems` × 10¹⁰，int64 不会溢出。
- 单项乘积 ≤ 10²⁸，`NumItems`（少于 16）项之和 < 1.6 × 10²⁹，在 128 位有符号整数（约 1.7 × 10³⁸）之内。这个域里不可能溢出，仍逐步检查，溢出返回错误而不是截断。
- `Cost` 返回 `platform.Exact`：128 位整数，单位 10⁻¹⁵，币种随价目表。汇率和加价倍率由 billing 转成 `math/big.Rat` 再乘，只在记账时取整一次；本模块不做币种换算。

### 存储

规范 JSON，另存 SHA-256。suppliers 和 billing 在各自的表里存同样的 jsonb，写入前都经 `catalog.ParseSchedule`；比较和幂等都是对 `MarshalJSON` 输出的字节比较。jsonb 不保留成员顺序和写法，从数据库读回的文本要先 `ParseSchedule` 再 `MarshalJSON`，不能直接比较。

```json
{"context_tiers":{"basis":["input","cache_read","audio_in","image_in"],"method":"whole","tiers":[{"above":200000,
"rates":{"cache_read":"0.00000025","input":"0.0000025","output":"0.000015"}}]},"currency":"USD",
"rates":{"cache_read":"0.000000125","input":"0.00000125","output":"0.00001"},"v":1}
```

（Gemini 2.5 Pro，2026-10-14；规范形式没有换行，这里为排版折行。）

- 成员：`v`（版本，现在是 1）、`currency`、`rates`（标准档的计费项 → 每计费单位的单价）、可选的 `context_tiers`（`basis`、`method`、按 `above` 严格递增的 `tiers`）、可选的 `service_tiers`（等级名 → 该等级的 `rates` 和 `context_tiers`，后者是与顶层门槛逐个相同的 `tiers`）。每一处的 `rates` 都必须写，可以是 `{}`；每一档都必须写 `above`。
- 规范形式：由 `platform/jsonx.MarshalCanonical` 写出，成员按 RFC 8785 排序，整数原样；单价写成 `UnitPrice` 的规范文本（十进制字符串，不用 JSON 数字，免得经过浮点），列表按枚举顺序。
- 拒绝未知成员和重复成员（`platform/jsonx.UnmarshalKnown`）：读到不认识的条件就报错，而不是忽略后按基础价算。

## 快照

> 已实现：`Load`（`Refresh`）、发布、陈旧上限、`ResolvePublic`、`Recognize`、`Run` 的快照刷新。设计，尚未实现：`CurrentPrices`、LISTEN/NOTIFY。这里的「快照」是[术语表](../../docs/glossary.md#术语表)的快照；`page_snapshot` 是其中的页面快照，与它无关。

- **加载**：`Load` 在一个只读的 Repeatable Read 事务里先读 `revision`，再读全部公开表。快照不可变，只含 listed 和 retired 模型、它们的名字和 listed 模型各币种的当前参考价；加载在平台范围执行，所以读得到 retired 模型的名字。retired 模型只用于两处：`ResolvePublic` 返回 `ErrRetired`，`Recognize` 仍认得出它的名字，供应商据此不把这个名字映射到别的模型。`Models()` 只给 listed 模型，`Model` 带 `Status`。快照大小随模型数增长，不随历史增长。
- **发布**：比较后替换，只接受更大的 `revision`。
- **传播**：写入的节点立即重建；其他节点每 5 秒读一次 `revision`，变了才全量加载。以后有 LISTEN/NOTIFY 时加一个触发源，接口不变。
- **陈旧上限**：快照记下 `confirmed_at`，即最后一次确认"数据库的 `revision` 就是本快照的版本"的时刻，取那次读取开始的时间。读到相同的 revision 才续期；读到更高的版本不续期，必须成功加载并发布它，新快照以加载事务开始的时刻作为 `confirmed_at`；加载失败不续期旧快照。超过最大陈旧时间（默认 30 秒，启动配置，必须大于轮询周期）后 `Current()` 返回 `ErrStale`，gateway 对需要解析模型的新请求返回 503，节点不就绪，直到确认或发布了当前版本。首次加载成功前同样不就绪。
- **影响有界**：快照里的参考价只用于展示和路由提示；售价由 billing 的价格版本决定并在请求开始时锁定，不读快照。下线的模型最多在陈旧上限内还能被新请求调用。
- **解析**：`ResolvePublic` 遇到含 `:` 的名字一律返回"模型不存在"，不切后缀，所以带后缀的写法永远落不到基础模型；M1 没有任何变体，`:free`、`:batch` 是供应商别名，路由变体随路由支持它时再加。其余名字 `Normalize` 后查预建的 map。`Recognize` 公开和内部名字都查，带日期的名字和移动名字的模型的名字返回 `Exact=true`，绑定到固定版本模型的不带日期名字返回 `Exact=false`。
- **billing 跟上改价**：billing 的价格版本记下所用参考价的版本号（`reference_price.id`），后台任务定期用 `CurrentPrices` 按站点币种重新选出该用的参考价，版本号不同就重算（选用规则见[计费的「定价」](../../docs/domains/billing.md#定价)）。

## 参考价的录入

> 已实现：LiteLLM 与 OpenRouter 的取数、解析、自动发布、改选到数据集（`Fetcher`、`SyncDataset`、`SyncAll`、`SelectDatasetSource`，命令 `axiom catalog sync`、`check`、`select-dataset-source`），各条的标注写明与设计的差别。设计，尚未实现：页面监测、官方价格页的证据核对、审核项、其余数据集（models.dev）的核对、定时调度（River 随 #25 合并后接入，现在由 `axiom catalog sync` 手动运行；`SyncAll` 是调度要调用的函数）。选价格来源和发布见「管理操作」。

- **页面监测**：租约用一条带条件的更新取得并递增 `lease_gen`；写入事务先 `SELECT … FOR UPDATE` 校验 `lease_gen`。正文先按内容找或存 `page_snapshot`；它不同于 `current_snapshot_id` 时，在同一事务里改 `current_snapshot_id`、追加 `page_observation`，并开或更新 `page_changed` 审核项（内容是与前一个当前快照的差异）。内容没变的成功取数只更新 `last_ok_at`、清零 `failures`。正文和 `extractor_version` 都没变才跳过后续处理。
- **取数**：经统一出站客户端，只访问登记的地址；超时 60 秒，正文上限 32MB，不跟随重定向。已实现：`Fetcher` 只读两个固定地址（`raw.githubusercontent.com` 上 LiteLLM 的 `model_prices_and_context_window.json`、`openrouter.ai/api/v1/models`），只收 200，重定向不跟随；超时 60 秒、正文上限 32MB。取数或解析失败时这一数据集整轮不发布，模型保持最后的价格，日志记 error。
- **数据集**：provider 名由代码里的一张小对照表映射到厂商（如 LiteLLM 的 `gemini`、models.dev 的 `google`）。数字从 JSON 原文取，十进制系数加指数（如 `3e-06`）精确展开后再转 `UnitPrice`；指数限制在 ±30，展开后超过 15 位小数的条目拒绝，全程不经 float64。只和 `Recognize` 返回 `Exact=true` 的模型对应，其余开审核项。已实现的读法：
  - LiteLLM 只读厂商自己的 provider（`openai`、`anthropic`、`gemini`→google、`deepseek`）下 `mode` 为 `chat` 或 `responses` 的条目，不读 `vertex_ai`、`bedrock`、`azure` 这类转售方按自己的价格表的条目；条目名去掉 `<provider>/` 前缀。字段先去服务等级后缀（`_priority`、`_flex`），再去 `_above_<N>k_tokens`（N 千 token），再按字段表认计费项；`_batches` 等不对应计费项的字段不读。联网搜索取 `search_context_cost_per_query`，三种上下文大小同价才取。
  - OpenRouter 取 `pricing` 里单位明确的字段（`prompt`、`completion`、`input_cache_read`、`input_cache_write`、`input_cache_write_1h`、`internal_reasoning`、`web_search`）；条目以 `id` 和带日期的 `canonical_slug` 为名；带 `:` 的变体（`:batch`、`:free`）是供应商别名，不当模型。带 `overrides` 的条目不可用：它写了 `min_prompt_tokens` 却没说按什么用量求和、门槛本身算不算进该档，有的还按时段（`utc_start`），所以 OpenRouter 现在只表达无分档的价格。
  - 币种固定 USD。条目有错（负数、指数越界、超过 15 位小数、字段不是数）时整条不可用，原因随结果报出，不取其余字段。
  - **分档的口径来自当前参考价**：数据集只说门槛在哪，不说哪些用量相加，而 `ParseSchedule` 要口径。条目的门槛与当前参考价的门槛逐个相同时，沿用当前的 `TierBasis`；没有当前价、门槛不同、条目没有分档而当前价有，都不发布（`ErrDatasetTiers` 或「分档结构不同」），由人发布后同步才接手。
  - **对应**：模型的名字（公开和内部，经 `ListNamesOfModel`）归一化后查条目。只有「名字本身是版本证据」的名字才算：移动名字的模型的任何名字，或带日期的名字（与 `Recognize` 的 `Exact` 同一规则）；只靠不带日期的名字找到条目的，结果是 held，因为数据集可能已把它换到新版本。同一模型对应到几个条目时，若有一个条目说出了其余所有条目说的（同一位置同一价格，如 LiteLLM 的 `deepseek-chat` 与 `deepseek/deepseek-chat` 后者多一个 0 的缓存写），取它；否则有歧义，held。不会从不同条目各取一个字段。
- **自动发布**（已实现，审核项除外）：本轮取数成功后，对 `price_source` 等于这个数据集的每个 draft 或 listed 模型，把条目与当前美元参考价按整张价目表比较（规范字节相同为 unchanged）。不同就先过安全阀：合理性（每个计费项在单位范围内，token 项不超过每百万 1000 美元、按次项不超过 1 美元，缓存读价不高于输入价）、结构（门槛口径与门槛和当前价相同）、变化阈值（`Config.PriceChangeLimit`，默认 25%：任一服务等级、档位、计费项的价格相对当前价变化不超过它），再过发布自己的检查（`Lost` 为空、上架门槛 `Missing` 为空，在锁住的模型行上再查一次来源）。通过就以 `system:catalog.sync` 发布，证据是条目原文、数据集名、整份正文的 SHA-256 和取数时间；不通过、条目不可用、有歧义、少了档位或价格，就是 held，不发布，日志记 warn，报告里写原因，价目表有效时附上候选，由管理员用 `publish-price` 采用：结果里的 `base` 是报告对着的当前价行（无历史则为空），原样作为 `Base`，期间有人发布过就被拒。取数早于当前价的发布时间的一轮不发布（held，「当前价发布在取数之后」），所以两轮先后取数而逆序完成时，旧数据不会盖掉新价；取数时间是取数前从数据库读的时钟（`Catalog.Fetch`），发布时间也是数据库的，两者由同一个钟排序，不依赖节点的时钟；更严的互斥随租约代次。没有 `review` 表，所以没有审核项和 `fetch_id`，每轮重新算。billing 的参考价安全阀在 catalog 之上、调用不了，所以 catalog 带自己的一份，阈值在 `Config` 里，billing 的安全阀落地后以它为准并改本条。
- **核对**：其余数据集的条目与当前参考价按整张价目表比较，不一致开 `mismatch` 审核项，不改价。
- **选价格来源**：改选就是一次发布，在同一个事务里完成：改 `price_source`，为新来源的每个币种发布一行（价目表与旧行相同也发布，`source` 变了），为新来源没有的币种各写一行撤下。所以改选之后每个币种的当前价都来自新来源，旧来源的币种不会留下来继续生效，计费在站点币种没有当前价时按汇率换算（见[计费的「定价」](../../docs/domains/billing.md#定价)）。新的价目表来自所选数据集的当前条目（要能解析，并且带有当前参考价的每一档），或者选 `official`、`manual` 时由操作带上（每个币种的价目表和证据，与 `PublishPrice` 的检查相同），至少一个币种；每张新价目表对当前每个币种的参考价的 `Lost`（`r` 取 `Model.Reportable()`）都为空，否则拒绝，改选不会悄悄丢掉 priority 这类等级里已有的价格；`Lost` 返回 `ErrTierBasisChanged` 时，改选必须在操作里明确确认门槛口径的变化（审核记录写明新旧口径），没有确认就拒绝，所以口径变化不会让防退化检查被绕过。选 `manual` 或 `official` 后，同步不再为它写行。数据集来源由 `SelectDatasetSource` 执行：它按同步的规则找到模型的条目（无歧义、可用、过合理性），读出 `rev` 和每个币种的当前价作为 `bases`，其余交给同一个改选事务，所以 `Lost`、口径变化确认、其他币种撤下都照旧；条目没有当前任一币种价目表已有的分档（门槛或口径不同）时拒绝，因为基础价能盖住每个区间，`Lost` 看不出长请求会按基础价计；价目表只来自条目，`SelectPriceSource` 不接受数据集来源，手工的价格不会被记在数据集名下。
- **发布**（页面证据的部分是设计，其余见「管理操作」）：一个事务里，引用页面证据的先 `SELECT … FOR SHARE` 锁页面行，让页面监测在提交前改不了 `current_snapshot_id`；再经 `lock_models` 锁模型行，确认 `price_source` 与这次发布的来源相同，读当前参考价与操作带来的 `base` 比较，写 `reference_price` 和 `change`，`revision` 加一。官方价格页的证据必须引用该页面的 `current_snapshot_id`，摘录必须逐字出现在快照正文里；每个单价换算成每百万后的数字应出现在摘录里，不出现时给警告，由审核者确认。

## 管理操作

> 已实现，平台范围。`axiom catalog` 命令是现在的入口；管理接口落地后经 operations 的审批中间层调用同样的方法。

每个操作是 `Catalog` 的一个方法，带 `Op{Actor, Key, Reason}`，在一个事务里依次：

0. `Key` 不能缺省或全零（`ErrNoOpKey`，数据库也拒绝），否则不同的操作会被当成第一个的重试。
1. 按 `Key` 查 `change`：已有就返回第一次的 `Result`（`Replayed`），不再执行；种类不同报 `ErrOpKeyReused`。同一个 `Key` 的两个并发请求，后一个在唯一约束上失败，重试即得第一次的结果。
2. 锁模型行（`LockModel`），带 `rev` 的操作比较 `rev`，不同报 `ErrStaleRev`。
3. 对着锁住的行检查，写入。
4. 写 `change`（改前改后和证据）和 `audit.Record`（动作名与 `change` 同名，`detail` 只放 `change_id`）；审计写不进，操作不成立。影响快照的写入（模型不是草稿时的改动）把 `revision` 加一。
5. 提交后本节点立即重建快照，失败只记日志，轮询会补上。

| 操作 | 做什么 | 检查 |
|---|---|---|
| `SaveAuthor` | 建厂商或改显示名 | 理由必填 |
| `CreateModel` | 建草稿模型，厂商取自 `slug` 的前半 | 厂商存在；`spec` 是对象，`items`、`tiers` 若有则是已知的计费项和服务等级 |
| `AddName` | 给模型加名字，归一化后全局唯一；名字长度 1 到 200 字节（入口、数据库 `octet_length`、解析器同一规则） | 模型未下线；`norm` 未被占用（`ErrNameTaken`）；公开名字的约束由数据库兜底 |
| `UpdateSpec` | 原地改规格（带 `rev`） | 模型未下线；已上架的，新规格仍要声明 `items` 和 `tiers`（缺省或 null 不算，显式空列表算），且声明的东西每个当前参考价都定了价 |
| `ListModel` | 草稿 → 上架（带 `rev`） | 上架门槛，见下 |
| `Retire` | 上架 → 下线（带 `rev`） | 必须是上架状态 |
| `PublishPrice` | 给一个币种追加一个参考价版本 | `base` 是审核时看到的当前行（没有历史时为空），否则 `ErrPriceChanged`；`source` 等于模型的 `price_source`；证据是非空对象；已上架的模型过上架门槛；`Lost` 为空，口径变化要操作确认（`ConfirmBasisChange`） |
| `SelectDatasetSource` | 改选到 `litellm` 或 `openrouter`，美元价目表取自数据集里模型的条目，请求只带模型、来源和是否确认口径变化 | 条目按「数据集」的对应规则找到且可用、过合理性检查；其余同 `SelectPriceSource`，`rev` 与 `bases` 由命令读取、在模型锁内复核 |
| `SelectPriceSource` | 改选来源（带 `rev`），同一个事务里为新来源的每个币种发布一行、撤下它没有的币种 | 请求带审核时每个币种的当前行（`bases`），在模型锁内与现在的逐一核对，多或少都是 `ErrPriceChanged`；每张新价目表对每个现有价格的币种（不只是同币种）都过 `Lost`，被撤下的币种的价格也不能丢；同 `PublishPrice` 的其余检查；至少一个币种；只能选 `official` 和 `manual`；数据集来源经 `SelectDatasetSource` |

- **上架门槛**：规格声明了 `items` 和 `tiers`（可以为空，但要写，免得漏写让门槛形同虚设）；标准 ID（`slug` 去掉 `@版本`）是它的一个公开名字；至少有一个币种的当前参考价；每个当前参考价对 `Model.Reportable()` 的 `Missing` 为空。这是目录自己的门槛，保证上架的模型的参考价覆盖它声明的一切；计费对售价的上架门槛另算。
- **证据**：现在只检查是非空的 JSON 对象，原样记进 `change`；引用页面快照、摘录逐字出现在正文里的检查随页面监测。
- **`change` 之外是否另调 `audit.Record`**：调。`change` 是目录自己读回的领域数据（核对证据、靠 `op_key` 幂等），审计日志是所有模块共用的、运营检索与回放的一条线；改参考价和模型状态属于[审计日志](../platform/README.md#审计日志)要求留审计的后台数据改动，且两者在同一个事务里，不会一边有一边没有。`detail` 只放 `change_id`，改前改后在 `change` 里，所以不受 8 KiB 的限制。
- **读取**（`read.go`，只读、平台范围、各一个一致的读，草稿也读）：`Authors`、`GetAuthor`、`Models`（按状态、厂商、`slug` 前缀筛选，前缀按字面匹配，`slug` 键集分页，迁移 `00002_slug_prefix.sql` 的索引服务前缀）、`Model`（带名字和各币种当前参考价）。管理接口的 `Idempotency-Key` 就是 `Op.Key`，处理器在占键的事务里带着 `Op.Tx` 调用操作（`ModelIn` 在同一事务里读回模型），由平台的 `idem` 绑定操作者、操作、目标和内容，重发得到第一次的响应；键用在另一种操作上、或不经管理接口（命令行）已用过，报 `ErrOpKeyReused`。
- **命令**：`axiom catalog <操作> <文件>`，文件是该操作的请求 JSON（多出的成员报错），操作名是 `save-author`、`create-model`、`add-name`、`update-spec`、`list-model`、`retire`、`publish-price`、`select-price-source`、`select-dataset-source`（命令取数）；以平台范围和 `audit.System("catalog.cli")` 执行，输出一行结果 JSON。另有两个不带文件的命令：`axiom catalog sync` 对两个数据集各跑一轮并发布（`audit.System("catalog.sync")`），每个模型一行；`axiom catalog check` 只读，见「第一版验收的检查」。操作者的身份随 #23。

### 第一版验收的检查

`axiom catalog check`，只读，对 `openai`、`anthropic`、`google`、`deepseek` 四家，每个模型一行 JSON：是否上架、公开名字、选定来源、有当前价的币种、问题（不是上架状态、没有公开名字、规格没有声明 `items` 与 `tiers`、没有当前价、某币种的价目表缺规格声明的项或服务等级）。再取两个数据集做演习：每个跟随它的模型，价格是否与条目一致（`unchanged`）、会不会被发布（`differs`）、为什么 held；以及 OpenRouter 列出而目录里没有名字能确认其版本的纯文本模型（`unrecognized`；只靠不带日期的名字对上的也列出，因为数据集可能已把它换到新版本）。有任何一条问题就以非零状态退出，最后一行是汇总。

它不知道四家现在卖哪些模型：那在厂商的价格页上，页面监测随后；OpenRouter 的列表是最接近的参考，选官方价格页或手填的模型，价格与来源是否一致由人对着页面看。OpenRouter 的条目要有目录里的名字才对得上：它的带日期名字（如 `anthropic/claude-4.5-sonnet-20250929`）与厂商原生名字（`claude-sonnet-4-5-20250929`）词序不同，归一化不会把两者合并，所以上架时要把 OpenRouter 的名字也加成这个模型的内部名字，`unrecognized` 会把漏掉的列出来。

## 不变量

> 已实现（有测试）：名字归一化后全局唯一、用户只能调用公开名字、快照一致且新的不被旧的覆盖、快照陈旧超限不再解析（含 revision 已变而全量加载失败时不续期旧快照）、参考价的历史不分叉并基于审核时看到的当前价（数据库约束与 `base`）、管理操作重试不重复生效、公开表里没有操作者、理由、证据，以及「单价精确、非负，计算不溢出」「用量不为零却没有价格的项一定被报告」和 `Usage` 保留未知用量那一半。数据集部分：`manual` 等来源的模型不在同步里、超过阈值的改价不自动生效、手工价格不会记在数据集名下。尚未实现：过期的取数者写不进数据、锁顺序（现在每个操作只锁一个模型）、参考价的证据可复核（页面部分）。

| 不变量 | 靠什么保证 |
|---|---|
| 名字归一化后全局唯一 | `name.norm` UNIQUE；测试：只差大小写或 `.`、`-` 的第二个名字被拒 |
| 名字上架后不改指向（切换与迁移在 M1 之后）；带日期的名字不会被合并到别的模型 | `name` 只追加；`Normalize` 不增删数字，所以保留日期。模糊测试：输入和输出各自只留 ASCII 数字后完全相同 |
| 用户只能调用公开名字 | `ResolvePublic` 只查 public；CHECK 拒绝 `-latest` 结尾、含 `:` `~` `@` 的 public 名字；调用、模型列表、回退列表三处入口用同一个函数，测试覆盖 |
| 快照是数据库某一时刻的一致状态，新快照不会被旧快照覆盖 | 只读 Repeatable Read 加载、比较后替换；测试：两表读取之间插入并发提交，结果仍属同一时刻；两次构建逆序完成，留下的是新的 |
| 快照陈旧超过上限就不再解析 | 见「快照」的陈旧上限；测试：revision 查询一直成功、全量加载一直失败，超过上限后返回 `ErrStale`、不就绪，成功发布后恢复 |
| 参考价的历史不分叉，发布基于审核时看到的当前价 | 发布带 `base`，先锁模型行再比较；`supersedes` UNIQUE、起点的部分唯一索引、同一时间线的复合外键兜底；测试：基于同一 `base` 的两个发布只成功一个；替代另一个模型或币种的行、替代自己，都被数据库拒绝 |
| 参考价只来自模型选定的来源，手填不被同步覆盖；每个币种的当前价的 `source` 都等于模型的 `price_source` | 发布在锁住的模型行上比较 `price_source` 与发布的来源；改选在同一事务里为每个币种发布或撤下；测试：`manual`、`official` 和另一个数据集的模型不在同步里，数据集改价后不变（`TestSyncLeavesOtherSourcesAlone`）；同步与改选来源并发时，改选先提交则同步的发布被拒；官方来源有 CNY 和 USD、改选只有 USD 的数据集后，`CurrentPrices` 里没有 CNY；当前价的 priority 有输入、输出价只缺 `web_search`，所选来源的 priority 只有输出价或没有 priority 时，改选被拒；同价改选后 USD 的当前行是新的 `id`、`source` 是新来源 |
| 超过阈值的改价不自动生效 | 自动发布先过安全阀；测试（`TestSyncPublishes`、`TestSyncHolds`、`TestSyncBadDatasets`）：阈值内的变化发布，超过的是 held（报告里写原因和候选，日志记 warn），参考价不变，审核项随 `review` 表；取数或解析失败时保留最后的价格并告警，不换来源 |
| 管理操作重试不重复生效 | 每个操作带 `op_key`，`change.op_key` UNIQUE，重试返回第一次的结果；测试：提交成功、响应丢失后重试 |
| 过期的取数者写不进数据 | 租约加代次，提交前校验；测试：A 取数期间租约被 B 接管，A 提交失败 |
| 锁顺序固定 | 页面或数据集行 → 审核项 → 模型行（按 id 升序，经 `lock_models`）→ `revision`；所有加锁都经同一个函数按这个顺序取。测试：两个事务以相反的顺序请求同一组模型行，都成功、不死锁 |
| 单价精确、非负，计算不溢出 | 见「数值域」；性质测试对照 `math/big`，覆盖最小单价、最大乘积、负用量、门槛求和、转分数后取整 |
| 用量不为零却没有价格的项一定被报告，不按零计 | `Cost` 返回缺价的项；`reasoning` 回退到 `output` 是唯一的例外；没有价格的服务等级让各项都缺价。表驱动测试：每个 Item 单独缺价时都被报告，包括 OpenAI 报了缓存写入而价目表没有写入价；性质测试：已定价部分的金额等于参考计算的结果，参考计算用完整的用量选档、只对有价格的项求和（去掉缺价项的用量会改变档位）；用例：缺价的 `cache_read` 200,000 加有价的输入 1，输出按高档计 |
| 上游报告的用量不会被悄悄丢掉 | 映射表外的非零字段和认不出的服务等级进 `Usage.Unknown()`。已实现：`NewUsage` 保留调用方交入的未知用量，构造与读取都复制（`TestUsageUnknown`）。protocol 从 Anthropic Messages 的流里识别：`TestRelayUsage` 加虚构的用量字段、`server_tool_use` 计数和等级，都出现在 `Unknown()` 里。尚未实现：OpenAI 两种格式的识别和测试 |
| 参考价有可复核的证据 | 发布时锁住页面行，检查证据引用当前快照、摘录逐字出现在正文里；自动发布记下条目原文和 `fetch_id`；测试：引用旧快照、摘录不在当前快照正文里，都被拒 |
| 公开表里没有操作者、理由、证据 | 它们只在平台数据的 `change` 里；测试：查 `information_schema`，公开表没有 `actor`、`reason`、`evidence` 列；非平台范围读不到平台数据，读不到草稿模型和它的名字、参考价 |

## 公开接口

```go
package catalog // internal/catalog

type ModelID uuid.UUID                 // 文本形式同 uuid.UUID
type Item uint8                        // 封闭枚举
type ItemSet = set[Item]               // Item 的位集合（16 位）；NumItems 是 Item 的个数
type ServiceTier uint8                 // 封闭枚举；不叫 Tier，那是账户的等级
type ServiceTierSet = set[ServiceTier] // ServiceTier 的位集合；两种集合都有 Has、Add
type Reportable struct{ Items ItemSet; ServiceTiers ServiceTierSet } // 上游可能报告的计费项和服务等级
type PricePoint struct{ ServiceTier ServiceTier; Above int64; Item Item } // Above 是区间的下门槛，第一段为 0
type Usage struct{ /* 各项数量、服务等级、不认识的用量，字段不导出 */ } // 只能经 NewUsage 构造
func NewUsage(q map[Item]int64, st ServiceTier, unknown []UnknownUsage) (Usage, error)
func (u Usage) OutputTotal() int64
func (u Usage) Count(it Item) int64     // 一项的数量：protocol 按它渲染客户端看到的用量，billing 的计量读已报告的数
func (u Usage) ServiceTier() ServiceTier // 实际服务的等级，供应商记录扣费所报的等级
func (u Usage) Unknown() []UnknownUsage // 映射表外的用量字段和认不出的服务等级，返回副本
type UnknownUsage struct{ Field, Value string }

type Schedule struct{ /* 币种、门槛口径、门槛、每个服务等级的单价；字段不导出 */ }
func ParseSchedule(b []byte) (Schedule, error)                       // 拒绝还不支持的条件
func (s Schedule) MarshalJSON() ([]byte, error)                      // 规范编码
func (s Schedule) Cost(u Usage) (platform.Exact, ItemSet, error)     // 纯函数、精确、不分配内存；返回已定价部分和缺价的项
func (s Schedule) Ceiling(items ItemSet, tiers ServiceTierSet, length int64) (platform.UnitPrice, bool) // 预留的单价上界，见下
func (s Schedule) Highest(items ItemSet, tiers ServiceTierSet, length int64) (platform.UnitPrice, bool) // Ceiling 不在缺价处停下：已知价格里最高的，以及是否有缺价
func (s Schedule) BaseRate(it Item) (platform.UnitPrice, bool) // 标准档、第一道门槛以下的单价，给路由比较价格用
func (s Schedule) Missing(r Reportable) Reportable                    // 上架门槛用
func (s Schedule) Lost(prev Schedule, r Reportable) ([]PricePoint, error) // 替换参考价前防退化；两边都有分档而口径不同时 ErrTierBasisChanged
func Normalize(name string) string

type Snapshot struct{ /* 不可变 */ }
func (s *Snapshot) Revision() int64
func (s *Snapshot) ResolvePublic(name string) (Resolution, error) // 用户请求、模型列表、回退列表
func (s *Snapshot) Recognize(name string) (Recognition, bool)    // 给 suppliers 和数据集核对
func (s *Snapshot) Model(id ModelID) (Model, bool)                // Model.Reportable() 给上架门槛
func (s *Snapshot) ReferencePrice(id ModelID, currency string) (ReferencePrice, bool)
func (s *Snapshot) Models() iter.Seq[Model]                       // 只有 listed 模型

type Resolution struct{ Model ModelID; Floating bool }
type Recognition struct{ Model ModelID; Exact bool } // Exact：名字本身就是版本证据

type Config struct{ MaxStale, PollEvery time.Duration; Logger *slog.Logger } // 默认 30 秒、5 秒、slog.Default()；MaxStale 必须大于 PollEvery
type Catalog struct{ /* db、当前快照 */ }
func Open(db *pg.DB, scope pg.Scope, cfg Config) (*Catalog, error) // 平台范围由 cmd/axiom 注入
func (c *Catalog) Current() (*Snapshot, error)              // 超过陈旧上限返回 ErrStale；一个请求从头到尾用同一个快照
func (c *Catalog) Ready() bool
func (c *Catalog) Refresh(ctx context.Context) error        // 读 revision，变了才全量加载
func (c *Catalog) Run(ctx context.Context) error            // 每 PollEvery 刷新快照；数据集同步由 SyncAll 提供、调度随 River，页面监测与核对随它们的 PR（设计，尚未实现）
func (c *Catalog) CurrentPrices(ctx context.Context) ([]ReferencePrice, error) // 从数据库一致读取，给 billing 比对；设计，尚未实现
// 读取（「管理操作」一节），平台范围：Authors、GetAuthor、Models(ModelQuery)、Model(ModelID)，返回 Author、ModelSummary、ModelView。
// 管理操作（「管理操作」一节），平台范围：SaveAuthor、CreateModel、AddName、UpdateSpec、ListModel、Retire、PublishPrice、
// SelectPriceSource、SelectDatasetSource，请求类型带 Op{Actor, Key, Reason}，返回 Result。ListReviews、ResolveReview 随审核项（设计，尚未实现）。

type Fetched struct{ Source PriceSource; Body []byte; At time.Time } // 一份取回的数据集
func NewFetcher(client *egress.Client) *Fetcher                     // 固定地址、60 秒、32MB、只收 200
func (f *Fetcher) Fetch(ctx context.Context, src PriceSource) (Fetched, error) // At 为零
func (c *Catalog) Fetch(ctx context.Context, f *Fetcher, src PriceSource) (Fetched, error) // 取数，At 取自取数前数据库的时钟
func ParseLiteLLM(body []byte) ([]Entry, error)                     // 条目；ParseOpenRouter 同；整份不能解析是错误，单条不可用记在 Entry.Err
func (c *Catalog) SyncDataset(ctx context.Context, f Fetched, publish bool) (SyncReport, error) // publish 为 false 是演习：只报告
func (c *Catalog) SyncAll(ctx context.Context, f *Fetcher, publish bool) ([]SyncReport, error)  // 调度要调用的函数
func (c *Catalog) SelectDatasetSource(ctx context.Context, f Fetched, r SelectDatasetSourceRequest) (Result, error)
func (c *Catalog) Check(ctx context.Context, authors []string) ([]ModelCheck, error)           // 只读
func (c *Catalog) Unrecognized(entries []Entry, authors []string) ([]string, error)           // OpenRouter 列出而目录不认识的纯文本模型
type SyncResult struct{ Model ModelID; Slug, Entry string; Outcome Outcome; Reason string; Candidate jsonx.Raw } // published / unchanged / differs（演习） / held
```

## 测试

> 已有：计价的性质测试和基准、服务等级与档位交叉、`Missing`、`Lost`、`NewUsage`、`Unknown`、价目表解析与规范编码的用例；`Normalize` 的黄金表和模糊测试；真实 PostgreSQL 上的行级安全与授权、参考价历史的约束、上架门槛、改选来源、重试与并发的 `rev`、快照加载与陈旧上限、名字解析（`ResolvePublic` 零分配用测试断言，没有单独的基准）；`axiom catalog` 命令。数据集的数字、两个数据集的解析（`testdata` 里 2026-10-15 取回并裁剪的 LiteLLM 与 OpenRouter 样本，加几条编造的坏数据）、取数的限制、同步、改选到数据集、检查和 `MapUpstream` 的测试也已有，测试里不联网。尚未有：页面监测的测试，以及「两表读取之间插入并发提交」的测试（一致性由 `pg.ReadTx` 的测试担保）。

- 数据库测试用 `pgtest`，与 platform 相同：每个测试一份模板副本，可以并行。
- 计价对照 `math/big` 做性质测试：随机的价目表（零到三档、随机口径、每个服务等级随机有无价格、0 到 1000 的单价）、随机的服务等级和 0 到 10¹⁰ 的用量，与独立的模型比较选等级、选档、沿用该等级的基础价、推理按输出价、缺价的项和已定价部分的总额，并检查 `Missing` 覆盖 `Cost` 报告的每个缺价项：通过上架门槛的价目表，调用时不会缺价。`ParseSchedule` 做模糊测试：能解析的输入，规范形式再解析一次写出的字节不变。`Normalize` 做黄金表，覆盖 `-YYYYMMDD`、`-YYYY-MM-DD`、`-MMDD`、`-MM-DD` 四种日期写法，加上数字序列不变的模糊测试。
- `ResolvePublic`（包括需要归一化的名字，如 `claude-sonnet-4.5`）和 `Cost` 有零分配的基准测试：归一化写进栈上的缓冲区，用 `m[string(buf)]` 查 map，Go 对这种写法不分配。
- 名字解析的用例：含 `:` 的名字（包括 `:free`）返回"模型不存在"，不落到基础模型；下线模型的公开名字返回 `ErrRetired`，它也不出现在 `Models()` 里；绑定到固定版本模型的不带日期名字，`Recognize` 得到 `Exact=false`，`ResolvePublic` 仍得到原来的版本。
- 数据集数字的用例（已有，`TestPlainDecimal`、`FuzzPlainDecimal`、`TestParseLiteLLM`）：`3e-06` 与 `0.000003` 得到同一个 `UnitPrice`，全程不经 float64；指数超出 ±30、展开后超过 15 位小数的条目被拒。
- 每家的计价示例（缓存读 99,999 加输入 1 不换档、再加 1 换档；Gemini 缓存 200,000 加新输入 1 换档；Gemini 文本 190,000 加单独计价的图片 20,000 换档）写成用例。
- 用量折算与缺价的用例：Anthropic 只有顶层 `cache_creation_input_tokens` 时按请求的缓存断点归类，有 `cache_creation` 而两者之和不等时报错；Gemini 超过 200k 且有思考 token 时，思考按高档的输出价计；Gemini 的音频输入折成 `audio_in`；`Missing` 对缺音频价、缺 priority 价的价目表返回 `audio_in` 和 priority；有档位却没有 `TierBasis` 的价目表被拒。
- 服务等级与档位交叉的用例：标准档输出 2、长上下文 4，priority 输出 3、长上下文 5，四种组合各得其价；priority 没录 `cache_read` 时，priority 请求的缓存读被报告缺价，标准档请求不受影响，`Missing` 返回 priority；priority 漏写长上下文档或门槛与顶层不同的价目表被拒。
- `Lost` 的表驱动用例：旧 priority 有输入、输出价只缺 `web_search`，新 priority 只有输出价时返回 priority 的 `input`，新价目表没有 priority 时返回 priority 的 `input` 和 `output`；旧价有 `reasoning`、新价没有但有 `output` 时为空；新价目表多一道门槛、高区间缺 `output` 时只返回高区间；新价目表去掉旧门槛时为空（基础价覆盖高区间，档位的丢失由自动发布另查）；两表都只在 200k 以上为 priority 提供 `output`、旧表口径 {`input`, `cache_read`} 而新表 {`input`} 时返回 `ErrTierBasisChanged`（请求 `input=1、cache_read=200000、output=1` 在旧表有输出价、在新表没有），两表都没有分档而口径不同时照常比较。自动发布的条目让 `Lost` 不为空时只开审核项，参考价不变。

## 决策与理由

| 决策 | 理由 | 不选 |
|---|---|---|
| 公开和内部名字同表，`visibility` 区分，`norm` 唯一 | 唯一性对两类一起成立，内部名字不会和公开名字撞 | 两张表：唯一性要跨表检查 |
| 价目表存规范 jsonb，结构和校验在 Go | 三个模块共用；规范编码让比较和幂等变成字节比较 | 拆成计费项、条件、档位三张表：每个模块都要三张表和重组代码 |
| 单价是 int64 的 10⁻¹⁵ 定点，`Cost` 返回 128 位精确值；汇率和倍率在 billing 用分数 | 域有界时 128 位整数不会溢出、不分配内存，调用路径上快；10/71 这样的值有限小数表示不了，换算必须用分数 | 全程 `math/big`：每次都分配；全程十进制：汇率不精确 |
| `Cost` 返回缺价的项，不返回错误 | 计费的红线是不会算不出钱：缺项要按已定价部分加兜底价收，返回错误就丢了已定价部分 | 缺价就返回错误，或调用前按请求特征拒绝：负责人否决了后者（Issue #24），计费的上架门槛按模型检查 |
| 价格来源是模型上的一列，发布时在锁住的模型行上核对来源 | "手填不被覆盖""不自动换来源"变成一个比较，和发布同一个事务，没有竞态 | 每次同步时查一张来源配置表：同步与改选并发时可能写进旧来源的价格 |
| 快照用只读 Repeatable Read 加载，只接受更大的 revision | Read Committed 下同一事务的两次查询可能看到不同的提交（PostgreSQL 文档《Transaction Isolation》） | 多表分别查询 |
| 快照陈旧超限就拒绝新请求 | 刷新持续失败时，其他节点已下线的模型不能无限期地继续被调用 | 一直用旧快照 |
| billing 定期比对当前参考价跟上改价 | 状态比对不怕漏消息、不怕重启；事件机制还没有 | 一次性的变化通知 |
| 改动历史写本模块的 `change` 表，管理操作同时调 `audit.Record` | 改前改后的值、证据和 `op_key` 是目录自己要读回的领域数据：发布核对证据，重试靠 `op_key` 返回第一次的结果；操作者、理由、证据是平台数据，不能让租户范围读到。审计日志是所有模块共用、运营检索与回放的一条线，改参考价和模型状态是它要求留审计的后台数据改动；两个写入在同一个事务里，审计写不进操作就不成立，`detail` 只放 `change_id`，不受 8 KiB 限制（2026-10-15 定） | 公开表里带 `actor` 列；只写平台的审计日志：它不存证据、不管幂等，补充信息最多 8 KiB；只写 `change`：运营看不到目录的改动 |
| 构造函数叫 `Open`，不叫 `New` | sqlc 生成的 `Queries` 构造函数在同一个包里叫 `New`，无法改名 | 把生成物放进子包：多一层包，类型要加前缀才能和目录自己的 `Model` 区分 |
| 现在直接 `SELECT … FOR UPDATE` 锁模型行，`lock_models` 随后台任务角色 | `axiom_app` 有 `model` 四列的 UPDATE 权限，已能加锁；`lock_models` 要求 `SECURITY DEFINER` 函数属 `axiom_owner`，与[平台的启动检查](../platform/README.md#迁移)（只许 `axiom_lookup` 拥有）冲突，先有需要的是任务角色，到那时一并改检查 | 现在就建 `lock_models`：要改平台的检查，却没有调用方 |
| 上架门槛现在检查目录自己的参考价覆盖规格声明的一切（`Missing`），不等计费 | 目录在依赖表里在计费之下，不知道售价；但上架的模型的参考价不完整，计费的缺项兜底就成了常态 | 只留给计费检查：不完整的参考价能上架，要到计费才发现 |
| 页面监测用租约加代次，内容去重与当前指向分开 | 过期的执行者提交不了；页面 A→B→A 时当前指回 A，证据跟着页面现在的内容 | 没有代次的租约；按最晚入库的快照校验证据 |
| 参考价的表和类型叫 `reference_price` | `price_version` 在术语表里是 billing 的售价版本，同名会让两个模块的概念混在一起；来源可能是数据集或手填，叫 `official_price` 名不副实 | `price_version`、`official_price` |
| 服务等级各带一整套单价和同样的门槛，先选等级再选档，不跨等级继承 | 每个（等级、档位、计费项）只有一个价，`Cost`、`Missing` 和计费的保本线读法相同；缺的价被报告而不是按标准价少收 | 等级只列差价、其余沿用标准档：长上下文的 priority 价无从表达，覆盖顺序不同就得出不同的价 |
| 改选来源在同一事务里为每个币种发布或撤下 | 当前价只看时间线的头一行，不用再跨表比较来源；旧来源的币种不会在改选后继续生效，同价改选也留下新版本 | 当前价另按 `source = price_source` 过滤：改回旧来源时，没被替代的旧行会重新生效 |
| 模型行经 `SECURITY DEFINER` 的 `lock_models` 加锁 | 任务角色不必有 `model` 任何一列的 UPDATE 权限；锁顺序集中在一处 | 给任务角色一列的 UPDATE：它就能改那一列；advisory 锁：管理操作和任务都要记得另取一把锁，漏一处就有竞态 |
| `ModelID` 定义在本模块，不放 platform | 只被上层模块引用；platform 只放被下层引用的实体 ID | 放进 platform |
| 数据集同步现在不建 `dataset`、`review` 表，没发布的结果在报告和日志里，每轮重算 | 条目与模型的对应、候选价目表、比较都能从当次取回的数据和当前参考价算出，要持久化的只有发布本身，它已在 `reference_price` 和 `change` 里；审核项要有处理它的界面和通知才有意义，一个没人看的队列只是积压；现在定表结构，页面监测需要的字段还没出现，会猜错 | 现在建 `review`、`dataset_entry`：没有读者，迁移要先兼容再清理 |
| 分档的口径取自当前参考价；没有当前价或门槛不同就不发布 | 数据集只给门槛（LiteLLM 的 `_above_200k_tokens`、OpenRouter 的 `min_prompt_tokens`），不说哪些用量相加，口径错了长请求就按错的档计价；口径是厂商的规定，要逐家引用原文，现在只查实过 Anthropic 和 Gemini | 代码里写一张厂商口径表：没查实的厂商会被猜；表与当前价冲突时还要另处理 |
| 只有「名字本身是版本证据」的名字才对应条目 | 数据集里 `gpt-4o` 这样的条目可能已指向新版本，把它的价格发布给固定在旧版本的模型就是错价；与 `Recognize` 的 `Exact` 同一规则 | 所有名字都对应：数据集换版本时悄悄改了旧模型的价格 |
| 同一模型对应到几个条目时，取说出其余所有条目所说的那一个，否则 held | 实测 LiteLLM 的 `deepseek-chat` 与 `deepseek/deepseek-chat` 只差一个值为 0 的缓存写；取并集或逐字段表决会拼出没人发布过的价目表（见 docs/domains/catalog.md「参考价怎么进入目录」6） | 有歧义就全部 held：DeepSeek 永远同步不了；取第一个：随键的顺序错价 |
| OpenRouter 只用无 `overrides` 的条目，带 `overrides` 的 held | 文档与数据都没说 `min_prompt_tokens` 的门槛含义和求和口径，且有按时段的覆盖；猜一个就是错价 | 按 `min_prompt_tokens` 当严格大于、口径同当前价：恰在门槛处差一个 token，时段覆盖无法表达 |
| catalog 自带变化阈值（`Config.PriceChangeLimit`，默认 25%）和合理性检查 | billing 的安全阀在 catalog 之上、调用不了；25% 是我定的过渡值：厂商改价多在这以内，超过的让人看一眼代价小，漏掉一次错价代价大；阈值应是后台数据，现在是启动配置 | 等 billing 的安全阀：自动发布先没有保护 |
| `SelectPriceSource` 仍只接受 `official`、`manual`，数据集来源另设 `SelectDatasetSource` | 价目表只来自条目这一点由方法保证，不靠证据里写了什么；放开原方法，任何调用者都能把手填的价目表记在数据集名下 | 放开原方法、命令从条目拼请求 |
| 供应商上游名字的映射在 `syncer.MapUpstream`：先别名，再目录的内部识别，目标必须是上架的模型；未映射则 `EndpointFacts.Mapped` 为假 | 与 docs/domains/suppliers.md 的前两步一致；退役或不存在的模型不能接流量。不带日期的名字只是候选（`Unverified`），端点可建但要先过真伪检测；调用点在写 `supplier_models.model_id` 的同步持久化（#25，见 internal/suppliers/README.md） | 目录里加一个「可路由」查询：suppliers 的映射规则放进了 catalog |
