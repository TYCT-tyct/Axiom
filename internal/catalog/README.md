# catalog

模型、名字、官方规格和参考价，以及目录、成本计划、售价共用的价目表结构。要求和业务设计见[模型目录](../../docs/domains/catalog.md)，这里写代码结构、数据、不变量和接口。

> 本文先于代码写成，是目录模块的完整设计；各部分随实现的 PR 落地，代码与本文不一致时，在同一个 PR 里改本文。已实现的是价目表结构与计价（#70、#71）：计费项和服务等级及其集合，`Usage`（`NewUsage`、`Unknown`、`OutputTotal`、`Count`、`ServiceTier`），`Schedule`（`ParseSchedule`、`MarshalJSON`、`Cost`、`Ceiling`、`Missing`、`Lost`）和 `Reportable`，以及「数值域」「存储」两节规定的数值和存储格式；以及存储、快照与管理操作：表 `author`、`model`、`name`、`alias`、`reference_price`、`revision`、`change`，行级安全与授权，`Normalize`，内存快照与名字解析，管理操作和 `axiom catalog` 命令；以及 LiteLLM、OpenRouter 两个数据集的取数、解析、自动发布、改选到数据集和第一版验收的只读检查（#24，见「参考价的录入」），以及数据集同步的定时调度、它留在 `catalog.sync_state` 的记录和失败过久的告警（#319 E7-4、#281）。页面监测、页面审核项和当前快照证据校验已实现；数据集核对、数据集审核项、后台任务角色和 `lock_models` 仍是设计。

## 包

| 包 | 做什么 |
|---|---|
| `catalog` | 价目表结构与计价；名字的归一化与解析；内存快照；管理操作；数据集取数、解析与自动发布、改选到数据集、只读检查、定时同步与告警（均已实现）；页面监测与证据校验（已实现）；数据集核对（设计） |
| `catalog/migrations` | schema `catalog` 的表、授权和行级安全策略，版本表 `platform.goose_catalog`（页面表已实现，数据集条目表仍是设计） |
| `catalog/queries` | sqlc 的查询，生成的 `*.gen.go` 在 `catalog` 包里；生成的 `Queries` 构造函数叫 `New`，所以目录的构造函数叫 `Open` |

## 数据

### 表（schema `catalog`）

> 已实现：`author`、`model`、`name`、`reference_price`、`revision`、`change`、`sync_state`、`page_site`、`watched_page`、`page_snapshot`、`page_observation` 和页面专用 `review`。设计：`dataset` / `dataset_entry` 和数据集审核项。数据集条目仍每轮在内存中与模型对应，未发布的候选留在报告和日志，下一轮重新算；`sync_state` 只保存同步状态。

"只追加"指 `axiom_app` 没有 UPDATE、DELETE 权限。类别的含义见[安全的「租户隔离」](../../docs/security.md#租户隔离)。

| 表 | 主要列 | 键与约束 | 类别 | 写法 |
|---|---|---|---|---|
| `author` | `slug`、`name` | PK `slug` | 公开 | 增、改名 |
| `model` | `id`、`author`、`slug`、`status`（draft / listed / retired）、`version_basis`（dated / floating）、`price_source`（litellm / openrouter / official / manual）、`spec` jsonb、`rev` | PK `id`；UNIQUE `slug`；`split_part(slug, '/', 1) = author` | 公开，draft 行只有平台可见 | 列级授权只许改 `status`、`price_source`、`spec`、`rev` |
| `name` | `name`（原样）、`norm`、`visibility`（public / internal）、`model_id` | PK `name`；UNIQUE `norm`；public 时 `norm !~ '-latest$' AND norm !~ '[~@]'`，且只允许 `:free` 一个冒号；所有名字 `norm !~ '~'`，`:` 只能是结尾的 `:free` | 公开，只含 listed 模型的名字，其余只有平台可见 | 只追加 |
| `alias` | `name`（归一化）、`rev`、`model_id`、`set_at` | PK `(name, rev)`；`name ~ '^~…-latest$'`；只追加 | 公开 | 只追加 |
| `reference_price` | `id`、`model_id`、`currency`、`source`、`schedule` jsonb（撤下时为空）、`schedule_sha256`、`published_at`、`supersedes` | PK `id`；UNIQUE `supersedes`；部分唯一索引 `(model_id, currency) WHERE supersedes IS NULL`；UNIQUE `(id, model_id, currency)`，外键 `(supersedes, model_id, currency)` 引用它，只能替代同一条时间线上的行；`supersedes <> id`；`currency ~ '^[A-Z]{3}$'`，`schedule` 与 `schedule_sha256` 同时为空或同时非空，非空时币种等于 `schedule` 里的币种 | 公开，只含 listed 模型的参考价，其余只有平台可见 | 只追加 |
| `revision` | 单行 `n` | 只有影响快照的写入才加一 | 公开 | 改 |
| `change` | `id`、`op_key` uuid、`at`、`actor`、`reason`、`kind`、`subject`、`before`、`after`、`evidence` jsonb | PK `id`；UNIQUE `op_key` | 平台 | 只追加 |
| `sync_state` | 单行：`last_success_at`、`failing_since`、`failures`、`pending`（有候选价的 held 数） | PK `singleton`；`(failing_since IS NULL) = (failures = 0)` | 平台 | 改 |
| `page_site` | `origin`、`delay_seconds`、`last_request_at`、`next_request_at` | PK `origin`；间隔 1 … 86400 秒 | 平台 | 改预约与间隔 |
| `watched_page` | `id`、`author`、`url`、`origin`、`currency`、`section_start`、`section_end`、`interval_seconds`、`enabled`、`created_at`、`next_check_at`、`lease_gen`、`lease_until`、`last_ok_at`、`failures`、`current_snapshot_id` | PK `id`；同 URL 最多一个 enabled 登记；HTTPS；复合外键保证当前快照属于本页；配置只增、可停用 | 平台 | 改健康、租约、指针与启用状态 |
| `page_snapshot` | `id`、`page_id`、`first_seen_at`、`extractor_version`、`text`、`text_sha256` | PK `id`；UNIQUE `(page_id, extractor_version, text_sha256)` | 平台 | 只追加 |
| `page_observation` | `id`、`page_id`、`observed_at`、`snapshot_id` | PK `id`；只在当前内容变化时追加 | 平台 | 只追加 |
| `dataset` / `dataset_entry` | 数据集及条目、租约和健康状态 | 设计，尚未实现 | 平台 | 改 |
| `review` | `id`、`page_id`、`kind`（仅 page_changed）、`snapshot_id`、`previous_snapshot_id`、`removed`、`added`、`content_seq`、`status`、`resolution` | PK `id`；部分唯一索引 `page_id WHERE status = 'open'`；dismissed 的 `(page_id, snapshot_id)` 不重开 | 平台 | 改差异、版本与处理状态 |

- `model.id` 是 UUIDv7，Go 类型 `catalog.ModelID`。
- `model.slug` 是后台和日志用的唯一标识：`<厂商>/<模型>`，厂商给出版本标识时加 `@<版本>`；厂商部分 `^[a-z0-9][a-z0-9-]{0,39}$`。它不是名字，不进 `name` 表。`:free` 条目在模型部分后加 `:free`，它的标准 ID 就是去掉 `@版本` 的 slug（如 `acme/alpha:free`）。
- 标准 ID 是用户调用的 `<厂商>/<模型>`，存成一个 public 的 `name`，格式 `^<厂商>/[a-z0-9][a-z0-9._-]{0,99}(:free)?$`，不含 `@`。`:free` 是独立条目：自己的规格、参考价、端点，精确匹配，永远不会落到基础模型。
- `name` 上的 CHECK 只是兜底，上架审核时逐个确认可见性。改 `Normalize` 的规则要随迁移重算 `norm` 并重新检查唯一性。
- 参考价的历史以 `supersedes` 串起；当前价是没有被替代的那一行，它是撤下行（`schedule` 为空）时这个币种没有当前价。更正也是发布新的一行。`source` 记下这一行来自哪个价格来源，证据在对应的 `change` 里。
- `spec` 原地更新，旧值在 `change` 里。它包括目录基线的全部规格和计费要用的模型数据，其中 `items`、`tiers` 是上游可能报告的计费项和服务等级（见「上架门槛与缺项」）。
- `spec` 里另外四个键是端点接请求的规格：`context_length`（一次请求最多的 token，输入加输出）、`max_output_tokens`（模型的输出上限，也是默认输出上限）、`streams`（厂商是否流式）、`abilities`（能力名字的列表：`tools`、`images`、`documents`、`reasoning`、`prompt_cache`、`structured_output`、`web_search`、`code_execution`）。`Model.Serving()`（`snapshot.go`）把它们读成 `Serving{Context, MaxOutput, Streams, Abilities}`；**四个键缺一个就返回 false，绝不补默认值**。只有 `ServingDirect()` 是例外：直接格式（embeddings、rerank）的端点只要求 `context_length`，不要求 `max_output_tokens`、`streams`、`abilities`，因为这类模型不生成输出、不流式、不用能力位；它的 `Serving{Context}` 由路由用来检查每个输入——没写清能吃多少上下文的端点不能进路由，猜一个 128000 会让它带着错的上限接流量。组装路由快照的代码对 `Serving()` 为假的端点不放进快照；万一漏了，`routing` 的 `candidate`（`internal/routing/snapshot.go:273`）也会因为上下文长度不是正数拒绝它，而不是让它接流量。`context_length` 或 `max_output_tokens` 不是正数、`abilities` 里有不认识的名字、同一项写两遍，都在解析时拒绝，不忽略。`spec` 里还有七个**可缺**的键，说请求怎么写给这个模型：`thinking`（`adaptive`、`manual`、`none`）、`encrypted_reasoning`、`mid_system`、`untrusted_system_after_tools`、`default_sampling_only`（不接受非默认的 `temperature`、`top_p`）、`forced_tool_choice`（不思考时接受强制调用工具的 `tool_choice`；厂商把这两件事分开定，Claude Haiku 5.5 拒绝前者、接受后者）、`cache_breakpoints`（后六个是布尔）。`Model.Dialect()` 把它们读成 `Dialect`；与上面四个键不同，缺了不影响进路由，也没有 ok：缺的键读成 `ClaimUnset` / `ThinkingUnset`，**未设置不是 no**（缺与 false 是两种说法，和 `Serving` 的缺键同理），谁用它谁决定，从不当成模型的默认。`thinking` 写 `none` 是说模型不思考（明确不支持），不写是没说。不认识的 `thinking` 名字、类型不对的值在保存规格时拒绝，保存时还拒绝自相矛盾的两种写法（`abilities` 有 `reasoning` 而 `thinking` 是 `none`；`thinking` 是 `adaptive` 或 `manual` 而 `abilities` 没有 `reasoning`；`abilities` 不写则无从判断，放过，「有 `reasoning` 而 `thinking` 没写」也不在这里拒绝，由 `axiom catalog check` 报告）。这个一致性检查只在保存时做（`UpdateSpec`、`CreateModel`），不在解析时做：解析还要读库里已有的行，一行旧数据不该让整个快照加载失败。已进目录的模型要写全这几个键，Chat 转 Messages 的推理、非默认采样、强制调用工具和对话中途的系统消息才不会被拒绝（`protocol` 对未设置的拒绝见其 README）；值怎么定、怎么写进去、缺了怎么发现，见下一节。`Serving.Abilities` 是 `catalog.AbilitySet`（spec 里的名字），不是 `protocol.Capabilities`：依赖上 `catalog` 在 `protocol` 之下，能力名字换成能力位由 `protocol` 在建路由快照时做。
- `spec.max_uses` 是可选的服务端工具默认最大次数对象，例如 `{"web_search":30,"code_execution":30}`。仅接受 `web_search`、`code_execution`，每个值为 1 … 10¹⁰ 的整数；不支持的键、零、负数、非整数和重复键均拒绝。缺省、null 或空对象表示没有默认。`Model.ToolUses()` 返回副本，gateway 建快照时传给 `routing.Endpoint.ToolUses`，再由 `Entry.Limits.MaxUses` 交给报价。它不改变四个必填键的上架门槛，也不自行启用能力。
- `spec.output_speed` 与 `spec.reasoning_visible` 是可选的两项模型数据：`output_speed` 是模型最快输出速度（每秒 token 的正整数），`reasoning_visible` 是流里显示推理进度的调用格式名列表（`messages`、`responses`、`chat`，可以为空）。`Model.OutputSpeed()`、`Model.ReasoningVisible()` 读出它们；缺省是 0 和空集，也就是不知道、不放行，谁用谁决定，不当成默认值。解析时拒绝非正的速度、不认识或重复的格式名；保存时还拒绝 `reasoning_visible` 列了格式而 `abilities` 列出的能力里没有 `reasoning`（解析不查这一条：旧数据不该让快照加载失败）。路由把速度读成 `Endpoint.OutputSpeed`，计费据它算超额额度（[计费的「预留与结算」](../../docs/domains/billing.md#预留与结算)）；`reasoning_visible` 只在模型会推理时需要（没有这一位的模型若列了格式就不许保存，所以“列了能力却不说在哪里可见”与“不会推理却声明可见”都存不进去）。
- 管理员用 `axiom catalog update-spec <file>`（`UpdateSpecRequest`：`op_key`、`reason`、`model`、`rev`、完整的 `spec`）原地修改 `max_uses`；`rev` 防止覆盖并发修改，旧规格与理由进 `change`，快照随 revision 刷新。以 [OpenRouter server tools](https://openrouter.ai/docs/guides/features/server-tools#tool-call-limits) 为依据的端点可填 30（2026-10-28 查，外层循环共享默认及最大均为 30；按每项 30 次预留更保守）。这不是 Anthropic 原生工具的默认；模型的所有可路由端点没有共同、可验证的上界就不填。各上游没有默认时保留缺省，报价仍返回 `ErrUnbounded`，不批量改写存量规格；现有 jsonb 支持这份数据，无 schema 迁移。具体来源和范围见[目录领域决策](../../docs/domains/catalog.md#决策与理由)。
- `model.rev` 是改模型时的乐观并发令牌：`ListModel`、`Retire`、`UpdateSpec`、`SelectPriceSource` 带上审核时看到的 `rev`，锁模型行后比较，不同就拒绝，成功后加一。没有它，两个基于同一份规格的修改，后提交的会悄悄覆盖先提交的。测试：基于同一 `rev` 的两个 `UpdateSpec` 只成功一个。
- 数据集审核项的 `subject_key` 是设计：`page:<page_id>`、`price:<model_id>:<currency>`、`mismatch:<model_id>:<dataset>:<currency>`、`new:<dataset>:<external_id>`。没关联模型的条目用来源身份，不同的新模型不会合成一条。处理审核项时绑定 `content_seq`，期间内容又变了就处理失败。
- `actor` 暂存不透明字符串（`agent:<id>`、`user:<id>`、`system:catalog.sync`），#23 定义操作者类型后替换。

### 请求写法的键：值照厂商文档，不手填

这些键的值来自厂商的官方文档，不是运维凭印象填的：值抄进数据文件 `deploy/catalog/dialects.json`（`DialectFile`，逐个写出模型的 slug，不按名字前缀推断，因为名字像不等于文档这么说），由 `axiom catalog apply-dialect` 经 `UpdateSpec` 写进规格——有 `change` 和审计、按读到的 `rev` 检查，其余的键原样保留。不带 `--apply` 只显示会改什么；已经相同的模型不动，所以重跑什么也不变；目录里没有的 slug 只报告不创建；一个模型被拒（例如规格没列 `reasoning` 却要写 `thinking`）不拦其他模型，命令最后以非零状态退出。`just catalog-dialect-plan`、`just catalog-dialect-apply`、`just catalog-check` 是它们的入口，发布前的步骤见 [发布前](../../docs/ops/release.md)。

`axiom catalog check` 报告每个缺键的模型（作者是模型的一列，不是对名字的猜测）：`anthropic` 的模型要 `default_sampling_only`、`forced_tool_choice`、`mid_system`，`mid_system` 为 yes 时还要 `untrusted_system_after_tools`，有 `reasoning` 能力时还要 `thinking`；`openai` 的模型要 `cache_breakpoints`，有 `reasoning` 能力时还要 `encrypted_reasoning`。这些键只在 Chat 转 Messages 的转换（Anthropic 的模型）和发往 Responses 的请求（OpenAI 的模型）里被读；别家的模型在被 Messages 或 Responses 的端点服务之前不要求它们。`TestTheDialectFileAnswersWhatTheCheckAsks` 保证数据文件里的每个模型都带着 check 要的全部键。

**Anthropic**（读于 2026-10-19）。`mid_system` 为 yes 的是厂商文档列出的那几个型号，没列的为 no；`untrusted_system_after_tools` 只有 Sonnet 5.5、Haiku 5.5 为 yes。

| slug | thinking | mid_system | untrusted_system_after_tools | default_sampling_only | forced_tool_choice |
|---|---|---|---|---|---|
| `anthropic/claude-fable-5.1` | adaptive | yes | no | yes | no |
| `anthropic/claude-fable-5` | adaptive | yes | no | yes | yes |
| `anthropic/claude-opus-5.5` | adaptive | yes | no | yes | no |
| `anthropic/claude-opus-5` | adaptive | yes | no | yes | yes |
| `anthropic/claude-opus-4.8` | adaptive | yes | no | yes | yes |
| `anthropic/claude-opus-4.7` | adaptive | no | — | yes | yes |
| `anthropic/claude-opus-4.6` | adaptive | no | — | no | yes |
| `anthropic/claude-opus-4.5@20251101` | manual | no | — | no | yes |
| `anthropic/claude-sonnet-5.5` | adaptive | yes | yes | yes | no |
| `anthropic/claude-sonnet-5` | adaptive | no | — | yes | yes |
| `anthropic/claude-sonnet-4.6` | adaptive | no | — | no | yes |
| `anthropic/claude-sonnet-4.5@20250929` | manual | no | — | no | yes |
| `anthropic/claude-haiku-5.5` | adaptive | yes | yes | yes | yes |
| `anthropic/claude-haiku-4.5@20251001` | manual | no | — | no | yes |

- `thinking`：Anthropic《Thinking》「Configuring thinking」的各型号表（<https://platform.claude.com/docs/en/build-with-claude/thinking>，2026-10-19）：Opus 4.6 起的每个型号接受 `adaptive`（Opus 4.6、Sonnet 4.6 也接受已弃用的手动预算，取 adaptive）；Opus 4.5、Sonnet 4.5、Haiku 4.5 对 `adaptive` 返回 400，只接受 `enabled` 加 `budget_tokens`，即 manual；Opus 4.7 起手动预算返回 400。与 new-api `relaykit/relayconvert/reasoning/claude.go` 对过：4.6 起 adaptive，4.5 只有手动。
- `mid_system`：Anthropic《Mid-conversation system messages》（<https://platform.claude.com/docs/en/build-with-claude/mid-conversation-system-messages>，2026-10-19）：「available on Claude Fable 5.1, Mythos 5.1, Fable 5, Mythos 5, Opus 5.5, Opus 4.8, Opus 5, Sonnet 5.5, and Haiku 5.5 … not available on Claude Sonnet 5」；列表之外的型号为 no 是据这份列表推出，文档没有逐个写出。`untrusted_system_after_tools`：同页「On Claude Sonnet 5.5 and Claude Haiku 5.5 … can treat a system message placed right after a tool result as untrusted text」。
- `default_sampling_only`：Anthropic《Claude Opus 5.5 migration guide》（<https://platform.claude.com/docs/en/models/opus-5-5/migration-guide>）：非默认的 `temperature`、`top_p`、`top_k`「on Claude Opus 4.7 and later models … returns a 400 error」，Sonnet 5 同样（同文「Migrating … from Claude Sonnet 5」）；Sonnet 5.5 迁移指南：「Sonnet 4.6 and earlier models and Claude Haiku 4.5 accept …, on Claude Sonnet 5.5 a non-default value returns a 400 error」；Haiku 5.5 迁移指南「Remove sampling parameters」。Fable 5、5.1 没有页面单独写这条，按 Fable 5.1 概览「same patterns as Claude Opus 5」归为 yes（过度拒绝的一侧，不会把上游拒绝的请求发出去），拿到实测样本后再确认。
- `forced_tool_choice`：只有 Opus 5.5、Sonnet 5.5、Fable 5.1 拒绝（《Thinking》「Thinking with tool use」：adaptive 思考支持强制调用工具，「except on Claude Opus 5.5, Claude Sonnet 5.5, Claude Fable 5.1, and Claude Mythos 5.1」；各自的迁移指南「Forced tool use is not supported」）；Haiku 5.5 迁移指南明说接受。思考时 `protocol` 无论如何拒绝强制调用，这里只说不思考时。

**OpenAI**（读于 2026-10-19）：

| slug | encrypted_reasoning | cache_breakpoints |
|---|---|---|
| `openai/gpt-6-astra`、`openai/gpt-6.1-sol`、`openai/gpt-6-luna` | yes | yes |
| `openai/gpt-5.6-sol`、`openai/gpt-5.6-terra`、`openai/gpt-5.6-luna` | yes | yes |

- `encrypted_reasoning`：OpenAI《Reasoning》（<https://developers.openai.com/api/docs/guides/reasoning>，2026-10-19）：`store` 为 `false` 或零数据保留时，推理项「include an `encrypted_content` property by default」，页内示例用 `gpt-6-astra`、`gpt-6.1-sol`；文档没有逐个型号列出支持者，上表是它举例或点名的推理模型，GPT-5.5 及更早的没有证据，不在文件里，check 会报，由人对着文档补。
- `cache_breakpoints`：OpenAI《Prompt caching》（<https://developers.openai.com/api/docs/guides/prompt-caching>，2026-10-19）：显式断点「GPT-5.6 and later」支持，「GPT-5.5 and GPT-5.5 Pro」和更早的不支持（用 `prompt_cache_breakpoint: {"mode": "explicit"}`）。

数据文件里没有的型号（别的厂商、更早的型号、新发布的型号）没有证据就不猜：`check` 报缺哪些键，对着文档补进数据文件再 `apply-dialect`；厂商改了行为也是改数据文件，用同一条路。

### 行级安全与权限

范围读取位于策略级 InitPlan，写法和测量见 [pg](../platform/pg/README.md#决策与理由)。

> 已实现：公开数据和平台数据的策略与授权。设计，尚未实现：后台任务角色 `axiom_catalog_job` 和 `catalog.lock_models`。

- **公开数据**（`author`、`model`、`name`、`reference_price`、`revision`）：读用 `(SELECT platform.in_any_scope())`（任意非空范围），写用 `(SELECT platform.in_platform_scope())`（[pg 的范围表达式](../platform/pg/README.md#决策与理由)）。`model` 的读策略另加 `status <> 'draft' OR (SELECT platform.in_platform_scope())`；`name` 和 `reference_price` 的读策略另加"所属模型已 listed，或在平台范围"，所以给草稿模型录的参考价不会提前公开。
- **平台数据**（其余各表）：读写都用 `(SELECT platform.in_platform_scope())`。
- 后台任务以 `axiom_catalog_job` 角色执行：由本模块的迁移建立，只授予它要写的表：平台数据表，以及自动发布要写的 `reference_price`、`revision`；任务的事务用 `pg.Tx.SetRole` 切过去。
- **现在的加锁**：管理操作以 `axiom_app` 执行，它对 `model` 的 `status`、`price_source`、`spec`、`rev` 有 UPDATE 权限，所以直接 `SELECT … FOR UPDATE`（`LockModel`），每个操作只锁一个模型，不需要排序。后台任务角色出现时才需要下面的 `lock_models`。
- 模型行只经 `catalog.lock_models(ids uuid[])` 加锁（设计，随后台任务角色一起实现）：`SECURITY DEFINER`，属主 `axiom_owner`，固定 `search_path`，按 id 升序 `SELECT … FOR UPDATE` 并返回 `price_source`、`rev`；只把 EXECUTE 授予 `axiom_app` 和 `axiom_catalog_job`。它是运行时唯一以 `axiom_owner` 身份执行的代码，只加锁、不写。PostgreSQL 的行锁要求对该表至少一列有 UPDATE 权限（PostgreSQL 文档《SELECT》），任务角色没有 `model` 的 UPDATE 权限，也不该有：给它任何一列都等于允许它改这一列。表强制启用行级安全，所以函数里的查询仍受范围约束。测试：切到任务角色后自动发布成功；直接 `SELECT … FOR UPDATE` 模型行被拒。
- `in_any_scope()`、`in_platform_scope()`、切换角色的 `pg.Tx.SetRole` 和生产代码可用的平台范围 `pg.PlatformScope` 由 platform 提供。

## 价目表

### 计费项与用量

`Schedule.Rate(usage, item)` 返回该采样实际使用的单价、上下文档下标和是否有价；0 是基础档，后续档按阈值顺序编号。它复用 `Cost` 的物理输入分档、服务档和继承规则，供 billing 在只读接口展开用户的分项售价。

`Schedule.View()` 返回公开页面用的 `ScheduleView{Currency, Basis, Rates}`：按服务等级、上下文档、计费项展开每个**实际生效**的单价。`Base` 标出基础档，`Above` 是这一档的门槛；显式 `above: 0` 的门槛是 `Base: false`，不和基础档混在一起。某档没写的计费项继承基础价，所以空档也会展开成恢复后的基础价，而不是从视图里消失。它只读价目表本身，不读成本、供应商或价格来源；`catalog.PriceView` 仍是管理接口的单模型参考价摘要，两者不是一回事。

`catalog.Item` 是封闭枚举，`ItemSet` 是它的位集合（32 位，最多 32 项，`NumItems` 超过 32 编译不过）。各项是计费数量；除 OpenAI 兼容上游的缓存读写可重叠外，模态项从总量中拆出，不重复收费；`protocol` 按下表把上游报告的用量折成 `Usage`：

| Item | 单位 | OpenAI（Chat / Responses） | Anthropic Messages | Gemini |
|---|---|---|---|---|
| `input` | token | `max(总输入 − 缓存读 − 缓存写 − 正规化音频 − 正规化图片, 0)`（缺字段按 0） | `input_tokens`（含图片） | `promptTokenCount − cachedContentTokenCount` − 单独定价的模态 |
| `cache_read` | token | `cached_tokens` | `cache_read_input_tokens` | `cachedContentTokenCount` |
| `cache_write` | token | `cache_write_tokens` | `cache_creation.ephemeral_5m_input_tokens` | — |
| `cache_write_1h` | token | — | `cache_creation.ephemeral_1h_input_tokens` | — |
| `output` | token | `completion_tokens − reasoning_tokens − audio_tokens − image_tokens` | `output_tokens`（含思考，不单列） | `candidatesTokenCount` |
| `reasoning` | token | `reasoning_tokens` | 0 | `thoughtsTokenCount` |
| `audio_in` / `audio_out` | token | 输入、输出详情里的 `audio_tokens` | — | `promptTokensDetails` 里模态为 `AUDIO` 的数量 / — |
| `image_in` / `image_out` | token | 两侧 details 的 `image_tokens`，输入另加 `video_tokens`；输入先扣缓存交集 | — | 单独定价时，`promptTokensDetails` 里模态为 `IMAGE`、`VIDEO` 的数量 / — |
| `image_in_piece` / `image_out_piece` | 张 | —（尚无端点） | — | — |
| `audio_in_second` / `audio_out_second` | 秒 | —（尚无端点） | — | — |
| `character_in` | 字符 | —（尚无端点） | — | — |
| `web_search` | 次 | 待核对 | `server_tool_use.web_search_requests` | 待核对 |
| `code_execution` | 次 | 待核对 | `server_tool_use.code_execution_requests` | 待核对 |

- OpenAI 上游单独报告的图片直接折成图片项，不依赖价目表；缺图片价时按同侧普通价回落。其他格式只有上游单独报告、厂商单独定价的模态才折成自己的项；Anthropic 的图片按 token 计入 `input_tokens`、价格相同，就留在 `input`。视频没有自己的计费项，计入 `image_in`。例如 Gemini 2.5 Flash 的音频输入每百万 $1.00、文本 $0.30，Gemini 3.6 Flash 的图片和视频每百万 $1.00、文本 $0.75（Gemini 定价页，2026-10-14）。
- **物理总数与计费数量分开**：OpenAI 的 `PhysicalInput()` 保留原始总数及存在性（显式 0 不等于缺失），客户端输入回显用 `InputTotal()`，不把重叠计费量重新相加。输入计费总和是 `max(物理总数, 缓存读 + 缓存写)`，输出各项之和仍等于上游输出总数。Messages 的输入、缓存读写继续互斥，物理提示词长度由它们相加得到。
- **OpenAI 缓存重叠兼容口径**：每项都必须是 0 … 10¹⁰ 且不大于总数；缓存读、写各按报告量收费，普通输入取剩余量并夹到 0。`100/80/80` 的计费量是 `0/80/80`，物理输入和回显都是 100。这是经参考实现验证的兼容计费政策，不是对所有供应商交集语义的推断；供应商实际扣费仍须对账确认。没有写入计费的模型不报该字段，按 0。
- **图片、视频与缓存交叉统计**：输入的 `cached_tokens_details.image_tokens` 从图片量扣除，`video_tokens` 先并入图片量（OpenRouter《OpenAPI》`ChatUsage.prompt_tokens_details.video_tokens`「Video input tokens」，2026-10-19 读取）；明细必须不超过图片和缓存读总量，缓存图片与文本明细之和不超过缓存读。再把模态限制到 `max(总数 − 缓存读 − 缓存写, 0)` 中，先音频、后图片；无交集明细时也只收这个余量内的图片，不能把全缓存图片再收一次图片价。音频同理：`cached_tokens_details.audio_tokens` 不超过音频量，先从音频量扣除。`text_tokens` 是总量内的已知明细，只校验计数，不加入 `Unknown` 或额外收费。交集对象里其他未映射的成员（如 `cached_tokens_details.video_tokens`）已含在 `cached_tokens` 里，只记入 `Unknown` 供监控，计费不补收（`billing` 的 `unknownCost` 按路径 `.cached_tokens_details.` 判断）。输出侧没有视频项，`output/completion_tokens_details.video_tokens` 只校验为总量内的计数，已含在输出总量里，按普通输出收费。两种格式的 `input/prompt_tokens_details` 和 `output/completion_tokens_details` 别名均适用。
- Anthropic 的缓存写入以顶层的 `cache_creation_input_tokens` 为准，它等于 `cache_creation` 里各项之和（Anthropic《Prompt caching》）。有 `cache_creation` 时断言 `ephemeral_5m_input_tokens + ephemeral_1h_input_tokens = cache_creation_input_tokens`，不等就是用量错误；没有时（流式的部分事件、只转发顶层字段的中转），按请求里的缓存断点归类：没有 `ttl:"1h"` 的断点，全部记 `cache_write`；有，就全部记 `cache_write_1h`，拆不开时取贵的一项。
- 已核对（2026-10-14）：Gemini 的 `promptTokenCount` 包含缓存的内容（Gemini API《UsageMetadata》："this includes the number of tokens in the cached content"），所以 `input` 是两者之差；OpenAI Responses 的字段是 `usage.input_tokens_details.cached_tokens` 和 `cache_write_tokens`（OpenAI《Prompt caching》）。其余（联网搜索的计数字段、Gemini 按模态的明细是否包含缓存）由 `protocol` 实现这张表时按各家的接口参考核对，并写进它的测试：折算的代码在 `protocol`，本模块只接收 `Usage`。
- **代码执行按次计**，是[计费的「定价」](../../docs/domains/billing.md#定价)列出的按次工具，缺价时用站点级默认单价（[计费的「价目表缺项」](../../docs/domains/billing.md#价目表缺项)），不进 `Unknown()`。厂商不按次收费：Anthropic 按容器的运行小时（每小时 $0.05，与 `web_search_20260209` / `web_fetch_20260209` 同用时免费），OpenAI 按 20 分钟的容器会话（按分钟计、最少 5 分钟），Gemini 只按 token（Anthropic、OpenAI、Gemini 的定价页，2026-10-14），用量里也没有时长；所以每次的单价是近似值。站点默认价取一小时容器价（$0.05）作为保守的上限：短调用会多收，超过一小时的调用会少收，模型知道自己的典型时长时应写自己的按次价；取 5 分钟最低价会低于多数调用的实际成本。
- **按张、按秒、按字符**：这三类是独立的计费项，单价按每个计费单位（一张、一秒、一个字符），不是 token 价，所以永不与 token 项重叠。数量必须是整数：张数和字符数是数出来的，秒数是上游报告的最小单位，端点在报告时取整（不足一秒向上取整，与取整方向一致）。`Item.Unit()` 给出每项的单位，`CountedItems` 是这三类的集合；`protocol` 目前没有端点报告它们（图片、语音生成属于产品的「以后」），报价与结算已经支持，见[计费的「预留与结算」](../../docs/domains/billing.md#预留与结算)。
- **不认识的用量**：上游报告了映射表里没有、数值不为零的用量字段时，`protocol` 把字段名和数量放进 `Usage.Unknown()`，不丢弃，也不归进已有的项。计费据此按[计费的「价目表缺项」](../../docs/domains/billing.md#价目表缺项)处理：模型停止接受新请求，在途请求按兜底价暂估。
- **服务等级**：`Usage` 的服务等级 是这次实际服务的等级，封闭枚举 `ServiceTier`（standard、flex、priority、fast），`protocol` 按每家的取值表从响应里读（OpenAI 的 `service_tier`，Anthropic 的 `usage.service_tier` 和快速模式，Gemini 待核对）；响应没有回报时取请求里明确写的等级，都没有就是标准档。认不出的取值同样放进 `Unknown()`，不猜成标准档：这时 `ServiceTier` 填标准档只是占位，`Unknown()` 不为空，计费就按「价目表缺项」处理，不只看 `Cost` 的结果。`UnknownUsage{Field, Value}` 是上游的字段路径和原样的取值：数量不一定是整数，等级是字符串，由计费解读。
- **输出的三个口径**：`Usage.OutputTotal()` 是计量、预留的计费输出（普通、推理、音频、图片之和）；OpenAI 客户端总数也显式加这四项；billing 的免单判断检查普通、推理、音频和图片，加上交付的图片张数与音频输出秒数（`deliveredOutputs`），纯图片或纯音频回复都不免单。不能由一个总数函数隐式决定所有政策。
- **原生细项**：`NativeTokens` 是供应商报告的、没有自己计费项、已经含在计费总量里的拆分：`video_input`（输入视频，已计入 `image_in`）和 `accepted_prediction`/`rejected_prediction`（输出预测 token，已计入 `output`）。`WithNativeTokens` 逐项校验 0 … 10¹⁰，与 `WithPhysicalInput` 一样保留「报告过」的存在性（全零也是报告过，字段缺失不是）；它们只用于客户端回显（`native_tokens_*`）和 `final_usage` 的存储，`Cost` 不再计价，`Recount` 替换计费数量时原样保留，`HasNativeTokens` 区分缺失与全零。
- Anthropic 联网搜索每千次 $10，搜索带回的内容按普通 token 计入输入（Anthropic Pricing 页，2026-10-14）。

### 条件

价目表声明一个门槛口径 `TierBasis`（各档共用）和若干 `ContextTier{Above, Rates}`（概念名；代码里是价目表的字段 `basis`、`above` 和每档的价格），计法是整单按档：`TierBasis` 所选项的数量之和（有物理输入时不超过物理总数）严格大于 `Above` 时，`Rates` 列出的项换价，没列出的沿用基础价。门槛严格递增，取满足条件的最高一档；口径只有一个，所以用量越大，档位只会越高。完整输入口径用物理总数；部分口径仍只选它声明的项并以物理总数封顶，不能把只看缓存读的口径也改成整份提示词。口径含 `input` 而不含 `image_in` 时，图片输入算作输入一并求和：图片拆出之前它本来就在输入里，门槛含义不变（口径 `input`、门槛 200000，250000 的提示词里 100000 是图片，仍进高一档）。`TierBasis` 按厂商原文的口径列出全部对应的计费项：从某个总量里拆出去单独计价的项（如 Gemini 从提示词里拆出的音频、图片和视频）仍属于那个总量。已知的两种：

- Anthropic Haiku 5.5：超过 100,000 token，"prompt length counts all of its input tokens, including cache reads and cache writes"，`TierBasis` = {`input`, `cache_read`, `cache_write`, `cache_write_1h`}；Claude 4.6 及以后的其他模型 1M 内统一价（Anthropic Pricing 页，2026-10-14）。
- Gemini：提示词超过 200k，`TierBasis` = {`input`, `cache_read`, `audio_in`, `image_in`}，输出和缓存价同时换档（Gemini 定价页，2026-10-14）。

标准档以外的每个服务等级（如 flex、priority、快速模式）有自己的一整套单价，组合规则：

- **先选服务等级，再选档位**：`Usage` 的服务等级 是标准档时用价目表顶层的基础价和 `ContextTier`，否则用该等级的 `Rates` 和 `ContextTiers`；档位按共用的 `TierBasis` 选，`Rates` 列出的项换价，没列出的沿用该等级的 `Rates`。
- **不跨等级继承**：等级的 `Rates` 没列出的项就是这个等级没有价格，不沿用标准档。沿用会把 priority 下没录的缓存读按标准价悄悄少收；缺价能被报告，由计费按「价目表缺项」处理。`reasoning` 回退到同一等级、同一档的 `output`，图片回退到同侧的普通价；细则见下文「图片有效价格」。
- **门槛一致**：每个等级的 `ContextTiers` 的门槛必须与顶层的门槛逐个相同，不换价的档位写空的 `Rates`。否则 `ParseSchedule` 拒绝：等级漏写长上下文档时，长请求会按该等级的基础价算低。
- 价目表没列出的等级就是没有它的价格。例：标准档输出 2、200k 以上 4，priority 输出 3、200k 以上 5，priority 的长请求输出按 5 计。

「价目表的结构」里的超出部分按档和倍率型条件，随第一个需要它的价目表带着完整的规则加入；在那之前 `ParseSchedule` 拒绝它们和任何不认识的条件类型（所以没有分时段价格），也拒绝门槛不递增、有档位却没有 `TierBasis`、服务等级的门槛与顶层不同、等级重复或写成标准档。快速模式就是服务等级 `fast`，用一整套单价表达，不用倍率。计法在存储里写明（`"method":"whole"`），不靠默认。拒绝的是价目表，不是请求：需要它们的成本计划，端点不使用（见[计费的「供应商成本」](../../docs/domains/billing.md#供应商成本)）；需要它们的数据集条目，不能被选为价格来源。

### 上架门槛与缺项

> `Schedule.Missing`、`Schedule.Lost` 和 `ErrTierBasisChanged` 已实现，上架门槛见「管理操作」。`Model.Reportable()` 已实现。

- **上游可能报告什么**是模型数据 `spec.items` 和 `spec.tiers`，审核时确认：输入输出类型、工具和厂商提供的服务等级决定它，例如支持音频输入就有 `audio_in`，有联网搜索就有 `web_search`，OpenAI 有 flex、priority。`Model.Reportable()` 返回它。
- **`Schedule.Missing(Reportable) Reportable`** 返回其中没有价格的项和等级，算上 `reasoning` 回退到 `output`：标准档在任何一档缺价的项，以及价目表没有、或任何一档缺任何一个可能报告的项的等级。计费用它检查售价是否满足[计费的「定价」](../../docs/domains/billing.md#定价)里的上架门槛（已知的按次项目可以由站点默认单价补上，所以 billing 的门槛把它们从 `Reportable.Items` 里去掉再问），路由经 `billing.Unpriced` 用它拒绝售价不全的模型，同样先去掉已知的按次项目。它答的是"这份价目表能不能给这些项和等级定价"，不是"上游会回报什么"：上游回报了价目表没有的等级时结算按兜底价收，不经它。门槛在 billing 检查，不在本模块：目录在模块依赖里位于 billing 之下，不知道售价。
- **`Schedule.Lost(prev Schedule, r Reportable) ([]PricePoint, error)`** 返回 `prev` 有价格而本价目表没有的位置 `PricePoint{ServiceTier, Above, Item}`，只看 `r` 里的等级和项，两边都算上 `reasoning` 回退到 `output`。区间按两张价目表门槛的并集切分：每个区间里两边的价各自不变，所以逐个（等级、区间、计费项）比较就是精确的。这只在两张价目表的 `TierBasis` 相同时成立：口径不同（例如旧表按 {`input`, `cache_read`} 算门槛、新表只按 {`input`}），同一个数字门槛对应的用量范围就不同，两边都有分档时两张表不可比较，`Lost` 返回 `ErrTierBasisChanged`，不返回位置列表；只有一边有分档时，另一边的价格与口径无关，按那一边的门槛切分后照常比较（新价目表去掉旧门槛就是这样）；两边都没有分档时同样照常比较。替换参考价前用它防止价格覆盖退化；不用 `Missing` 比较，因为它把一个等级里的缺项合成一个等级，旧 priority 只缺 `web_search`、新 priority 连输入价都没有时两边的结果相同。
- **`Fill`** 返回补过价的副本：对每个提供的服务等级，基础价里没有的项补上给定价格；分档只在基础价之上覆盖，所以补上的项在每一档都有价，价目表自己在任何位置写了的价格都保留。billing 用它把站点的按次默认单价补进锁定的售价（[计费的「价目表缺项」](../../docs/domains/billing.md#价目表缺项)）。
- **`Cost` 不因缺价失败**：它给有价格的项计价，并返回用量不为零却没有价格的项。`Usage` 的服务等级 在价目表里没有价格时，用量不为零的各项都算缺价。billing 按「价目表缺项」给这些项兜底，所以红线"不会算不出钱"不靠本模块拒绝请求。
- **`Ceiling` 给预留用的单价上界**：这些项在这些服务等级里、在基数合计不超过 `length` 的提示词可能落入的每一档中的最高单价，任一处缺价返回 false。档位只按基数里的项求和，提示词在基数内外怎么拆分都可能落到 `length` 以下的任一档，价格也不一定随档位上升，所以只看某一档或某一项都不是上界。
- **能力不归这里**：模型根本不支持的功能（例如纯文本模型收到音频）由路由按端点能力过滤；本模块不在调用前按请求特征拒绝。本模块只把 `spec.abilities` 读成 `Serving.Abilities`（见「表」下 `spec` 一条），带不带能力位由 `protocol` 转，判断归路由。

- **图片有效价格**：`Schedule.price` 是 `Cost`、`Missing`、`Ceiling` 共用的解析器；先查该服务等级、该上下文档的图片项（包括继承的基础图片价），仅缺项才回落到同档的 input/output。显式 0 是有效价格，不回落；不跨服务等级继承。路由门槛、预留、售价和供应商成本因而使用同一政策。同价回落是兼容默认值，不是保证保本，真实图片更贵时须录独立价。
- **重叠的预留**：billing 在每次采样的物理输入上界 N 上，取 `N × max(输入侧最高价, 缓存读最高价 + 缓存写最高价)`，读写只有在调用格式是 Chat 或 Responses 时合并（`billing.Model.OpenAIUsage`，由路由按端点的调用格式设置；Messages 的各项互不重叠，仍取单项最高价）；这时输入侧价格集合固定含图片输入，不论规格是否列出；各最高价覆盖允许的服务等级和上下文档。后续采样及追加搜索的重读用同一上界。两项分别相乘再比较精确金额，避免单价之和超出单价类型上限。

### 数值域

- `platform.UnitPrice`：int64，单位 10⁻¹⁵ 个币种单位每计费单位，取值 0 … 10¹⁸（每单位最多 1000 个币种单位）。只有两种构造途径：录入的单价从十进制文本构造，超过 15 位小数或超出范围时拒绝保存；算出来的单价（`Schedule.Scaled`：成本计划里官方价乘分组倍率，售价里参考价乘站点倍率）从精确分数向上取整到 10⁻¹⁵，超出范围时拒绝（见[计费的「钱的表示」](../../docs/domains/billing.md#钱的表示)）。
- `Usage` 每项 0 … 10¹⁰，字段不导出，只能经 `NewUsage` 构造，负数或超限返回错误；`WithPhysicalInput` 同样校验物理输入，`Recount` 替换计费数量时保留它；零值是全零的合法用量。不变量由构造守住，`Cost` 不再校验。门槛求和最多 `NumItems` × 10¹⁰，int64 不会溢出。
- 单项乘积 ≤ 10²⁸，`NumItems`（少于 32）项之和 < 3.2 × 10²⁹，在 128 位有符号整数（约 1.7 × 10³⁸）之内。这个域里不可能溢出，仍逐步检查，溢出返回错误而不是截断。
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

- **加载**：`Load` 在一个只读的 Repeatable Read 事务里先读 `revision`，再读全部公开表。快照不可变，只含 listed 和 retired 模型、它们的名字和 listed 模型各币种的当前参考价；加载在平台范围执行，所以读得到 retired 模型的名字。retired 模型只用于两处：`ResolvePublic` 返回 `ErrRetired`，`Recognize` 仍认得出它的名字，供应商据此不把这个名字映射到别的模型。`Models()` 只给 listed 模型，`Model` 带 `Status`；`Model.Author` 是 `author.slug`，`Model.AuthorName` 是它的显示名（没有显示名时回落为 slug），真伪检测用后者核对模型的回答（[端点评估的「真伪检测」](../../docs/domains/quality.md#真伪检测)）。快照大小随模型数增长，不随历史增长。
- **发布**：比较后替换，只接受更大的 `revision`。
- **传播**：写入的节点立即重建；其他节点每 5 秒读一次 `revision`，变了才全量加载。以后有 LISTEN/NOTIFY 时加一个触发源，接口不变。
- **陈旧上限**：快照记下 `confirmed_at`，即最后一次确认"数据库的 `revision` 就是本快照的版本"的时刻，取那次读取开始的时间。读到相同的 revision 才续期；读到更高的版本不续期，必须成功加载并发布它，新快照以加载事务开始的时刻作为 `confirmed_at`；加载失败不续期旧快照。超过最大陈旧时间（默认 30 秒，启动配置，必须大于轮询周期）后 `Current()` 返回 `ErrStale`，gateway 对需要解析模型的新请求返回 503，节点不就绪，直到确认或发布了当前版本。首次加载成功前同样不就绪。
- **新增计费项与滚动升级**：旧版本的 `parseRates` 用旧的封闭枚举，读到不认识的计费项名字就拒绝整张价目表；加载失败不续期旧快照，超过陈旧上限后节点对所有模型都不就绪，回滚旧二进制同样加载不了目录。所以新增计费项分两步。本版本先能读：`Config.CountedItems` 关闭（`cmd/axiom` 现在传的就是关闭）时，`PublishPrice`、改选价格来源和数据集同步都拒绝含按张、按秒、按字符价格的价目表，同步把 `ErrCountedItems` 当 held 写进报告，所以 listed 模型的价目表不会带旧节点读不了的名字，草稿上的这类价格也不进快照。打开它的发布必须已经让全部节点跑能读这些项的版本：那时正在被替换的旧节点是本版本的读取方，写入才安全；`Config.CountedItems` 的注释写明这一步，打开它的发布只需在 `cmd/axiom` 传 `true`，不需要改解析或发布逻辑。回滚边界：一旦 listed 模型的价目表含这些项，回滚到本版本之前的二进制无法加载目录快照，只能先撤下这些价格；还没写过这类价格时回滚安全。供应商成本计划是管理员直接写进 billing 的另一处价目表，打开图片、音频接口的发布要按同样的顺序写它。验证：`TestAllowCounted` 覆盖开关与拒绝，`TestSyncHoldsCountedPrices` 覆盖同步 held、不写价格和打开后发布；用量 JSON 末尾零值的兼容另见[计费的「分项计价」](../../docs/domains/billing.md)。
- **影响有界**：快照里的参考价用于展示，也是售价的输入：路由快照每次重建时，按站点的定价规则对它现算售价（见[路由的「路由快照从哪来」](../../docs/domains/routing.md#路由快照从哪来)），所以参考价的改动在两个 5 秒的周期内进入售价。下线的模型最多在陈旧上限内还能被新请求调用。
- **解析**：`ResolvePublic` 对名字做精确匹配，不切后缀；`:free` 只命中目录里同名条目，基础模型不会替它被调用。`:batch` 等仍不存在，`:nitro`、`:floor` 由 gateway 在调用目录之前读掉（`gateway/variant.go`）。别名 `~作者/系列-latest` 查 `alias` 的最新 `rev`，返回它指向的已上架模型；目标下线时返回 `ErrRetired`。其余名字 `Normalize` 后查预建的 map。`Recognize` 公开和内部名字都查，带日期的名字和移动名字的模型的名字返回 `Exact=true`，绑定到固定版本模型的不带日期名字返回 `Exact=false`；别名不是供应商名字，不参与 `Recognize`。
- **billing 跟上改价**：售价在路由快照重建时现算，版本号里含所用参考价的行（`reference_price.id`），所以参考价变了售价随之变，不需要通知。按站点币种重新选参考价的后台任务（`CurrentPrices`）和发布动作随 M2，见 [#234](https://github.com/TYCT-tyct/Axiom/issues/234)（选用规则见[计费的「定价」](../../docs/domains/billing.md#定价)）。

## 参考价的录入

> 已实现：LiteLLM / OpenRouter 的取数、解析、自动发布、改选到数据集及每日调度；官方页面监测、页面审核项、证据校验和只读 live 检查。其余数据集核对与数据集审核项仍是设计。

- **页面登记与调度**：管理员先审阅条款，用 `axiom catalog watch-page <file>`（`WatchPageRequest`，`Op` 加 `PageTarget`）登记厂商、HTTPS URL、币种和两个正文标记。`interval_seconds` 缺省 3600（可设 3600 … 604800），`rate_seconds` 缺省 10（可设 1 … 60），是同源共享设置，新登记可调低或调高它；已预约的等待不缩短。`unwatch-page`（`UnwatchPageRequest`）停用并增加租约代次；配置不可原地改，换 URL 或范围需新登记，避免在途抓取混入新配置。安装不预置页面。`RegisterPages` 在 serve 启动队列前登记 `catalog.pages`（syncs 队列，未终态按参数唯一，MaxAttempts=1），每分钟及启动时发现到期页；页面串行，每轮最多 9 分钟。条件更新用 FOR UPDATE SKIP LOCKED 取得 5 分钟租约并增加 lease_gen；网络和站点等待合计最多 4 分钟，写回最多 5 秒。节点关闭或轮次到期立即取消网络，不记页面失败、不推迟下次检查，等待租约自然到期；页面自身的 4 分钟超时仍记失败。写回先锁页面检查代次、有效期和启用状态，旧 worker 不得覆盖接手后的内容。成功或普通抓取失败后，下一轮等页面间隔加 0 … 10% 正向抖动。
- **页面取数**：只 GET 登记 URL 和同源 `/robots.txt`，共用 egress（Axiom User-Agent），每次请求 60 秒，robots 上限 512 KiB、页面 32 MiB，不跟重定向。只把 robots 的 404/410 当作没有规则；其他非 200、解析失败或拒绝时不读价格页。`temoto/robotstxt` 处理 Axiom group、Allow/Disallow、通配符和 Crawl-delay，不使用它把任意 4xx 当作允许的默认。每个请求在 page_site 短事务里预约，间隔取当前登记值与本次 robots Crawl-delay 的较大值；robots 不改登记间隔，仅影响已预约的下一次访问时间，跨节点和同源页面共享；robots 拒绝也留下下一次允许访问时间。429/503 尊重 Retry-After（秒或 HTTP 日期），无有效值时等一小时，不在本轮重试；超过 60 秒的 Crawl-delay 明确拒绝取页面，仍留下延后预约。等待超过本次预算就失败并保留旧证据。
- **页面截取与变化**：优先取 article，否则 main，否则整个文档；去脚本、样式、导航、页头页脚和隐藏节点。按布局补词边界：表格单元格、块标签、flex 列/网格、`display:block` 和图标前后留边界；连续的内联标签（`$0` + `.10`、`Pri` + `cing`）保持粘连；被跳过的隐藏节点不贡献边界。再解码 HTML 实体、去掉零宽字符并归一空白。两个标记必须唯一（结束标记在开始之后）、中间内容不能为空，否则失败；只对标记之间的文本算 SHA-256。按页面、抽取版本与 hash 复用快照；抽取版本 2 起用新的边界和零宽处理。版本升级时 `fetchPage` 用旧抽取器对同一响应重算，结果与当前快照逐字相同才保留快照，不新建、不产生变化复核，保留当前证据和未处理复核；不能用删空白后的比较，`1 23` 和 `12 3` 会碰撞。旧版本的 worker 不能把当前指针移回（`savePageContent` 的版本栅栏与数据库触发器双重检查）。当前指针变化才追加观测，变回旧内容则指回旧快照。差异是去掉公共前后缀的精确替换段 removed/added，不做价格解析。
- **页面审核与告警**：快照、当前指针、观测、审核项和 `platform/alert.Persist` 信号同事务提交；任何一处失败都回滚。首次成功也开审核项；不变只续健康。每页最多一个 open 项，新内容保留它的原始 previous_snapshot_id，重算该基线到最新内容的差异并增加 content_seq，首次登记的基线为空；避免后续小改动掩盖未审的涨价。处理审核与写回按页面锁串行。`review-page`（`ReviewPageRequest`）按 review UUID 和审核时的 content_seq 标 done/dismissed，旧版本拒绝，不发布价格。dismissed 的同一快照不重开，回到它时旧 open 项自动结束，避免审核过时内容。`catalog.page_changed` 以页面 UUID 合并，人工处理或回到已驳回内容恢复；每次页面取数失败发 `catalog.page_failed`，成功或停用页面恢复；每分钟检查距最后成功（从未成功则登记时刻）超过两倍页面间隔的 `catalog.page_overdue`，成功或停用页面恢复。管理员须配置这三类规则与渠道；信号只含页面 UUID、分类，不含 URL 和正文。
- **页面 live 验收**：`axiom catalog check-pages deploy/catalog/price-pages.json` 不打开数据库、不写目录；逐目标输出正文与 hash，失败输出错误并以非零退出，excluded 目标完全不发 GET。`PageInspector` 在同一进程中共享同源间隔，调度另有跨节点预约。清单不是自动启用配置；它验证网络、robots 和抽取，不代替连接真实目录数据的 `axiom catalog check`。条款依据与最终审阅归属见[领域文档](../../docs/domains/catalog.md#决策与理由)。
- **取数**：经统一出站客户端，只访问登记的地址；超时 60 秒，正文上限 32MB，不跟随重定向。已实现：`Fetcher` 只读两个固定地址（`raw.githubusercontent.com` 上 LiteLLM 的 `model_prices_and_context_window.json`、`openrouter.ai/api/v1/models`），只收 200，重定向不跟随；超时 60 秒、正文上限 32MB。取数或解析失败时这一数据集整轮不发布，模型保持最后的价格，日志记 error。
- **定时**（已实现，`sync_job.go`）：`catalog.sync` 每天在 `syncs` 队列上跑一轮，`axiom serve` 在队列启动前用 `RegisterSync` 登记；只有队列的领导节点插入，启动或换主也先跑一轮。参数相同，未结束任务按参数保持唯一，重启不会堆积任务。`MaxAttempts` 为 1；正常取数或解析失败写入 `catalog.sync_state` 后返回 nil，任务完成，等下一日轮次，不因一次短暂上游故障触发 `queue.job_discarded`。只有状态记录写不进才返回错误，队列丢弃信号暴露的是记录设施故障。任务自己打开目录，只做取数和发布；对外快照由调用路径打开。价格 worker 同时响应 River 任务和登记时传入的节点 context；节点开始关停即取消网络取数，启动失败也在停止队列前取消，不等通用队列的优雅收尾期限。结果记录使用不继承任务取消、最多 5 秒的 context，生产时间来自同一记录事务里的 `clock_timestamp()`，测试可注入时钟。读全两个数据集才写最后成功、清零连续失败，并重算待确认改价数（仅 `OutcomeHeld` 且 `Candidate != nil`）；缺条目、歧义、无法构造价目表的 held 仍留在报告和日志里，但不算待确认改价。失败累加连续失败，首次失败记录 `failing_since`，保留最后成功和上一完整轮次的待确认数，后续成功再重算。
- **同步状态与告警**（已实现，`sync_metrics.go`）：登记任务时同时登记无标签 gauge `axiom_catalog_sync_last_success_age_seconds` 和 `axiom_catalog_sync_consecutive_failures`。每次采集都以平台范围读持久化状态和数据库时钟（最多 2 秒），距最后成功的秒数即使没有新任务执行也继续增长；从未成功时从 `failing_since` 起算，尚未执行过为 0，时钟倒退时不报负数。连续失败直接取 `failures`。读库失败返回采集错误、不伪报 0；运维节点需同时监测指标缺失。运维节点按后台数据配置时间和连续失败阈值、严重程度及通知渠道（[记录与告警的「监控与告警」](../../docs/domains/observability.md#监控与告警)）。节点仍在执行失败轮次时提供 `catalog.sync.overdue` 日志和 `axiom_catalog_sync_overdue_total` 计数作为兜底；`SyncJobConfig.AlertAfter` 的 48 小时是代码默认值，没有后台配置入口，显式启动配置可覆盖，不能把它当作后台规则。单次取数错误仍由 `SyncAll` 记录，但不走队列丢弃告警。
- **数据集**：provider 名由代码里的一张小对照表映射到厂商（如 LiteLLM 的 `gemini`、models.dev 的 `google`）。数字从 JSON 原文取，十进制系数加指数（如 `3e-06`）精确展开后再转 `UnitPrice`；指数限制在 ±30，展开后超过 15 位小数的条目拒绝，全程不经 float64。只和 `Recognize` 返回 `Exact=true` 的模型对应，其余开审核项。已实现的读法：
  - LiteLLM 只读厂商自己的 provider（`openai`、`anthropic`、`gemini`→google、`deepseek`）下 `mode` 为 `chat`、`responses`、`image_generation`、`audio_speech`、`audio_transcription` 的条目，不读 `vertex_ai`、`bedrock`、`azure` 这类转售方按自己的价格表的条目；条目名去掉 `<provider>/` 前缀。字段先去服务等级后缀（`_priority`、`_flex`），再去 `_above_<N>k_tokens`（N 千 token），再按字段表认计费项；字段表按 `mode` 收窄，同一个字段在不同 mode 下含义不同的不读：`output_cost_per_image` 只在 `image_generation` 里是每张图的价（chat 行里它是每 image token 的价），`output_cost_per_second` 只认 `audio_speech`，`input_cost_per_second` 只认 `audio_transcription`，`input_cost_per_character` 只认 `audio_speech`；`input_cost_per_image` 整个不读（`high/1024-x-1024/gpt-image-1` 这类带尺寸前缀的图片键里它是生成一张的价，chat 行里是 image token 的价，含义不唯一）。`_batches` 等不对应计费项的字段不读。联网搜索取 `search_context_cost_per_query`，三种上下文大小同价才取。
  - OpenRouter 取 `pricing` 里单位明确的字段（`prompt`、`completion`、`input_cache_read`、`input_cache_write`、`input_cache_write_1h`、`internal_reasoning`、`web_search`）；`image`、`image_output` 不读：文档说按张，但 2026-10-29 的真实数据里 Gemini 的这两个值等于它的 image token 价（`image` 与 `prompt` 同价），取它就是一个 token 的价当一张图的价；条目以 `id` 和带日期的 `canonical_slug` 为名；`:free` 是独立模型，只以 `id` 为名（它的 `canonical_slug` 是付费模型的名字，保留会让免费价匹配到付费模型）；其他带 `:` 的变体（如 `:batch`）是供应商别名，不当模型。带 `overrides` 的条目不可用：它写了 `min_prompt_tokens` 却没说按什么用量求和、门槛本身算不算进该档，有的还按时段（`utc_start`），所以 OpenRouter 现在只表达无分档的价格。
  - 币种固定 USD。条目有错（负数、指数越界、超过 15 位小数、字段不是数）时整条不可用，原因随结果报出，不取其余字段。
  - **分档的口径来自当前参考价**：数据集只说门槛在哪，不说哪些用量相加，而 `ParseSchedule` 要口径。条目的门槛与当前参考价的门槛逐个相同时，沿用当前的 `TierBasis`；没有当前价、门槛不同、条目没有分档而当前价有，都不发布（`ErrDatasetTiers` 或「分档结构不同」），由人发布后同步才接手。
  - **对应**：模型的名字（公开和内部，经 `ListNamesOfModel`）归一化后查条目。只有「名字本身是版本证据」的名字才算：移动名字的模型的任何名字，或带日期的名字（与 `Recognize` 的 `Exact` 同一规则）；只靠不带日期的名字找到条目的，结果是 held，因为数据集可能已把它换到新版本。同一模型对应到几个条目时，若有一个条目说出了其余所有条目说的（同一位置同一价格，如 LiteLLM 的 `deepseek-chat` 与 `deepseek/deepseek-chat` 后者多一个 0 的缓存写），取它；否则有歧义，held。不会从不同条目各取一个字段。
- **自动发布**（已实现，审核项除外）：本轮取数成功后，对 `price_source` 等于这个数据集的每个 draft 或 listed 模型，把条目与当前美元参考价按整张价目表比较（规范字节相同为 unchanged）。不同就先过安全阀：合理性（每个计费项在单位范围内，token 项不超过每百万 1000 美元、非 token 项（按次、按张、按秒、按字符）不超过 1 美元，缓存读价不高于输入价）、结构（门槛口径与门槛和当前价相同）、变化阈值（`Config.PriceChangeLimit`，默认 25%：任一服务等级、档位、计费项的价格相对当前价变化不超过它），再过发布自己的检查（`Lost` 为空、上架门槛 `Missing` 为空，在锁住的模型行上再查一次来源）。通过就以 `system:catalog.sync` 发布，证据是条目原文、数据集名、整份正文的 SHA-256 和取数时间；不通过、条目不可用、有歧义、少了档位或价格，就是 held，不发布，日志记 warn，报告里写原因，价目表有效时附上候选，由管理员用 `publish-price` 采用：结果里的 `base` 是报告对着的当前价行（无历史则为空），原样作为 `Base`，期间有人发布过就被拒。取数早于当前价的发布时间的一轮不发布（held，「当前价发布在取数之后」），所以两轮先后取数而逆序完成时，旧数据不会盖掉新价；取数时间是取数前从数据库读的时钟（`Catalog.Fetch`），发布时间也是数据库的，两者由同一个钟排序，不依赖节点的时钟；更严的互斥随租约代次。`review` 当前只收页面变化，数据集仍没有审核项和 `fetch_id`，每轮重新算。billing 的参考价安全阀在 catalog 之上、调用不了，所以 catalog 带自己的一份，阈值在 `Config` 里，billing 的安全阀落地后以它为准并改本条。
- **核对**：其余数据集的条目与当前参考价按整张价目表比较，不一致开 `mismatch` 审核项，不改价。
- **选价格来源**：改选就是一次发布，在同一个事务里完成：改 `price_source`，为新来源的每个币种发布一行（价目表与旧行相同也发布，`source` 变了），为新来源没有的币种各写一行撤下。所以改选之后每个币种的当前价都来自新来源，旧来源的币种不会留下来继续生效，计费在站点币种没有当前价时按汇率换算（见[计费的「定价」](../../docs/domains/billing.md#定价)）。新的价目表来自所选数据集的当前条目（要能解析，并且带有当前参考价的每一档），或者选 `official`、`manual` 时由操作带上（每个币种的价目表和证据，与 `PublishPrice` 的检查相同），至少一个币种；每张新价目表对当前每个币种的参考价的 `Lost`（`r` 取 `Model.Reportable()`）都为空，否则拒绝，改选不会悄悄丢掉 priority 这类等级里已有的价格；`Lost` 返回 `ErrTierBasisChanged` 时，改选必须在操作里明确确认门槛口径的变化（审核记录写明新旧口径），没有确认就拒绝，所以口径变化不会让防退化检查被绕过。选 `manual` 或 `official` 后，同步不再为它写行。数据集来源由 `SelectDatasetSource` 执行：它按同步的规则找到模型的条目（无歧义、可用、过合理性），读出 `rev` 和每个币种的当前价作为 `bases`，其余交给同一个改选事务，所以 `Lost`、口径变化确认、其他币种撤下都照旧；条目没有当前任一币种价目表已有的分档（门槛或口径不同）时拒绝，因为基础价能盖住每个区间，`Lost` 看不出长请求会按基础价计；价目表只来自条目，`SelectPriceSource` 不接受数据集来源，手工的价格不会被记在数据集名下。
- **发布**（页面证据已实现，其余见「管理操作」）：一个事务里，引用页面证据的先 `SELECT … FOR SHARE` 锁页面行，让页面监测在提交前改不了 `current_snapshot_id`；再锁模型行（批量 `lock_models` 尚未实现），确认 `price_source` 与这次发布的来源相同，读当前参考价与操作带来的 `base` 比较，写 `reference_price` 和 `change`，`revision` 加一。官方证据为 `PageEvidence`（`page_id`、`snapshot_id`、非空 `excerpts`），必须引用仍启用页面的当前快照，厂商、币种一致，每段非空摘录逐字出现在正文。来源切换按币种排序锁页面，避免反序锁；每页币种不可变。`axiom catalog check` 对 official 价格补查已记录证据、当前快照、摘录和未处理页面审核；数据库 I/O 错误向上传递。数字与门槛是否真能支持整张价目表由审核者判断，监测不自动发布。

## 管理操作

> 已实现，平台范围。管理接口（`cmd/axiom/admin_catalog.go`）和 `axiom catalog` 命令调用同样的方法；operations 的审批中间层随 operations。别名目前只有命令入口（`axiom catalog save-alias`），HTTP 管理接口还没有它的路径。

每个操作是 `Catalog` 的一个方法，带 `Op{Actor, Key, Reason}`，在一个事务里依次：

0. `Key` 不能缺省或全零（`ErrNoOpKey`，数据库也拒绝），否则不同的操作会被当成第一个的重试。
1. 按 `Key` 查 `change`：已有就返回第一次的 `Result`（`Replayed`），不再执行；种类不同报 `ErrOpKeyReused`。带 `Op.Tx`（管理接口）时已有一律报 `ErrOpKeyReused`：接口自己的重发已由占键回答，到这里的键是别处（命令行）用过的，重放会替另一个操作者、目标或内容作答。同一个 `Key` 的两个并发请求，后一个在唯一约束上失败，重试即得第一次的结果。
2. 锁模型行（`LockModel`），带 `rev` 的操作比较 `rev`，不同报 `ErrStaleRev`。
3. 对着锁住的行检查，写入。
4. 写 `change`（改前改后和证据）和 `audit.Record`（动作名与 `change` 同名，`detail` 只放 `change_id`）；审计写不进，操作不成立。影响快照的写入（模型不是草稿时的改动）把 `revision` 加一。
5. 提交后本节点立即重建快照，失败只记日志，轮询会补上。

| 操作 | 做什么 | 检查 |
|---|---|---|
| `SaveAuthor` | 建厂商或改显示名 | 理由必填 |
| `CreateModel` | 建草稿模型，厂商取自 `slug` 的前半 | 厂商存在；`spec` 是对象，`items`、`tiers` 若有则是已知的计费项和服务等级 |
| `AddName` | 给模型加名字，归一化后全局唯一；名字长度 1 到 200 字节（入口、数据库 `octet_length`、解析器同一规则）；`:` 只允许结尾的 `:free` | 模型未下线；`norm` 未被占用（`ErrNameTaken`）；公开名字的约束由数据库兜底 |
| `SaveAlias` | 给 `~作者/系列-latest` 追加一个目标模型版本（命令入口；HTTP 管理接口暂缺） | 名字符合别名格式；目标是已上架模型；目标作者与别名作者相同；目标不是 `:free` 条目；`change` 与审计同事务，`revision` 加一 |
| `UpdateSpec` | 原地改规格（带 `rev`） | 模型未下线；已上架的，新规格仍要声明 `items` 和 `tiers`（缺省或 null 不算，显式空列表算），且声明的东西每个当前参考价都定了价 |
| `ListModel` | 草稿 → 上架（带 `rev`） | 上架门槛，见下 |
| `Retire` | 上架 → 下线（带 `rev`） | 必须是上架状态 |
| `PublishPrice` | 给一个币种追加一个参考价版本 | `base` 是审核时看到的当前行（没有历史时为空），否则 `ErrPriceChanged`；`source` 等于模型的 `price_source`；证据是非空对象；已上架的模型过上架门槛；`Lost` 为空，口径变化要操作确认（`ConfirmBasisChange`） |
| `SelectDatasetSource` | 改选到 `litellm` 或 `openrouter`，美元价目表取自数据集里模型的条目，请求只带模型、来源和是否确认口径变化 | 条目按「数据集」的对应规则找到且可用、过合理性检查；其余同 `SelectPriceSource`，`rev` 与 `bases` 由命令读取、在模型锁内复核 |
| `SelectPriceSource` | 改选来源（带 `rev`），同一个事务里为新来源的每个币种发布一行、撤下它没有的币种 | 请求带审核时每个币种的当前行（`bases`），在模型锁内与现在的逐一核对，多或少都是 `ErrPriceChanged`；每张新价目表对每个现有价格的币种（不只是同币种）都过 `Lost`，被撤下的币种的价格也不能丢；同 `PublishPrice` 的其余检查；至少一个币种；只能选 `official` 和 `manual`；数据集来源经 `SelectDatasetSource` |

- **上架门槛**：规格声明了 `items` 和 `tiers`（可以为空，但要写，免得漏写让门槛形同虚设）；标准 ID（`slug` 去掉 `@版本`）是它的一个公开名字；至少有一个币种的当前参考价；每个当前参考价对 `Model.Reportable()` 的 `Missing` 为空。`:free` 条目还要求基础模型有公开名字且已上架，并且每个币种的每个价都是 0。这是目录自己的门槛，保证上架的模型的参考价覆盖它声明的一切；计费对售价的上架门槛另算。
- **证据**：现在只检查是非空的 JSON 对象，原样记进 `change`；引用页面快照、摘录逐字出现在正文里的检查随页面监测。
- **`change` 之外是否另调 `audit.Record`**：调。`change` 是目录自己读回的领域数据（核对证据、靠 `op_key` 幂等），审计日志是所有模块共用的、运营检索与回放的一条线；改参考价和模型状态属于[审计日志](../platform/README.md#审计日志)要求留审计的后台数据改动，且两者在同一个事务里，不会一边有一边没有。`detail` 只放 `change_id`，改前改后在 `change` 里，所以不受 8 KiB 的限制。
- **读取**（`read.go`，只读、平台范围、各一个一致的读，草稿也读）：`Authors`、`GetAuthor`、`Models`（按状态、厂商、`slug` 前缀筛选，前缀按字面匹配，`slug` 键集分页，迁移 `00002_slug_prefix.sql` 的索引服务前缀）、`Model`（带名字和各币种当前参考价）、`Aliases`（当前别名及目标）。管理接口的 `Idempotency-Key` 就是 `Op.Key`，处理器在占键的事务里带着 `Op.Tx` 调用操作（`ModelIn` 在同一事务里读回模型），由平台的 `idem` 绑定操作者、操作、目标和内容，重发得到第一次的响应；键用在另一种操作上、或不经管理接口（命令行）已用过，报 `ErrOpKeyReused`。
- **命令**：`axiom catalog <操作> <文件>`，文件是该操作的请求 JSON（多出的成员报错），操作名是 `save-author`、`create-model`、`add-name`、`save-alias`、`update-spec`、`list-model`、`retire`、`publish-price`、`select-price-source`、`select-dataset-source`（命令取数）；以平台范围和 `audit.System("catalog.cli")` 执行，输出一行结果 JSON。另有两个不带文件的命令：`axiom catalog sync` 对两个数据集各跑一轮并发布（`audit.System("catalog.sync")`），每个模型一行；`axiom catalog check` 只读，见「第一版验收的检查」；`axiom catalog apply-dialect [--apply] <文件>` 把数据文件里的请求写法的键写进同 slug 的模型（见「请求写法的键」）。操作者的身份随 #23。

### 第一版验收的检查

`axiom catalog check`，只读，对 `openai`、`anthropic`、`google`、`deepseek` 四家，每个模型一行 JSON：是否上架、公开名字、选定来源、有当前价的币种、问题（不是上架状态、没有公开名字、规格解析不了、规格没有写请求写法的哪些键（见「请求写法的键」）、规格没有声明 `items` 与 `tiers`、没有当前价、某币种的价目表缺规格声明的项或服务等级）。规格解析不了的行只记在它自己的问题里，检查接着看下一个模型，一次运行列出所有要改的行；快照加载遇到这样的行则整个失败（`ReadSnapshot`，`axiom serve` 启动即退出）——快照没有跳过单个模型的做法，启动时失败是对的层，发现靠这条检查。再取两个数据集做演习：每个跟随它的模型，价格是否与条目一致（`unchanged`）、会不会被发布（`differs`）、为什么 held；以及 OpenRouter 列出而目录里没有名字能确认其版本的纯文本模型（`unrecognized`；只靠不带日期的名字对上的也列出，因为数据集可能已把它换到新版本）。有任何一条问题就以非零状态退出，最后一行是汇总。

它不知道四家现在卖哪些模型：那在厂商的价格页上，页面监测随后；OpenRouter 的列表是最接近的参考，选官方价格页或手填的模型，价格与来源是否一致由人对着页面看。OpenRouter 的条目要有目录里的名字才对得上：它的带日期名字（如 `anthropic/claude-4.5-sonnet-20250929`）与厂商原生名字（`claude-sonnet-4-5-20250929`）词序不同，归一化不会把两者合并，所以上架时要把 OpenRouter 的名字也加成这个模型的内部名字，`unrecognized` 会把漏掉的列出来。

## 不变量

> 已实现（有测试）：名字归一化后全局唯一、用户只能调用公开名字、快照一致且新的不被旧的覆盖、快照陈旧超限不再解析（含 revision 已变而全量加载失败时不续期旧快照）、参考价的历史不分叉并基于审核时看到的当前价（数据库约束与 `base`）、管理操作重试不重复生效、公开表里没有操作者、理由、证据，以及「单价精确、非负，计算不溢出」「用量不为零却没有价格的项一定被报告」和 `Usage` 保留未知用量那一半。数据集部分：`manual` 等来源的模型不在同步里、超过阈值的改价不自动生效、手工价格不会记在数据集名下；定时同步的记录跨重启和换主存活，一轮失败不改最后成功和待确认改价，连续失败的起点不随后续轮次移动。页面部分已实现：代次租约拒绝过期写回，按币种锁页面再锁模型，逐字复核官方证据；数据集租约和批量锁模型仍是设计。

| 不变量 | 靠什么保证 |
|---|---|
| 名字归一化后全局唯一 | `name.norm` UNIQUE；测试：只差大小写或 `.`、`-` 的第二个名字被拒 |
| 名字上架后不改指向（切换与迁移在 M1 之后）；带日期的名字不会被合并到别的模型 | `name` 只追加；`Normalize` 不增删数字，所以保留日期。模糊测试：输入和输出各自只留 ASCII 数字后完全相同 |
| 用户只能调用公开名字 | `ResolvePublic` 只查 public 和别名；CHECK 拒绝 `-latest` 结尾、含 `~` `@` 的 public 名字，`:` 只允许 `:free`；调用、模型列表、回退列表三处入口用同一个函数，测试覆盖 |
| 快照是数据库某一时刻的一致状态，新快照不会被旧快照覆盖 | 只读 Repeatable Read 加载、比较后替换；测试：两表读取之间插入并发提交，结果仍属同一时刻；两次构建逆序完成，留下的是新的 |
| 快照陈旧超过上限就不再解析 | 见「快照」的陈旧上限；测试：revision 查询一直成功、全量加载一直失败，超过上限后返回 `ErrStale`、不就绪，成功发布后恢复 |
| 参考价的历史不分叉，发布基于审核时看到的当前价 | 发布带 `base`，先锁模型行再比较；`supersedes` UNIQUE、起点的部分唯一索引、同一时间线的复合外键兜底；测试：基于同一 `base` 的两个发布只成功一个；替代另一个模型或币种的行、替代自己，都被数据库拒绝 |
| 参考价只来自模型选定的来源，手填不被同步覆盖；每个币种的当前价的 `source` 都等于模型的 `price_source` | 发布在锁住的模型行上比较 `price_source` 与发布的来源；改选在同一事务里为每个币种发布或撤下；测试：`manual`、`official` 和另一个数据集的模型不在同步里，数据集改价后不变（`TestSyncLeavesOtherSourcesAlone`）；同步与改选来源并发时，改选先提交则同步的发布被拒；官方来源有 CNY 和 USD、改选只有 USD 的数据集后，`CurrentPrices` 里没有 CNY；当前价的 priority 有输入、输出价只缺 `web_search`，所选来源的 priority 只有输出价或没有 priority 时，改选被拒；同价改选后 USD 的当前行是新的 `id`、`source` 是新来源 |
| 超过阈值的改价不自动生效 | 自动发布先过安全阀；测试（`TestSyncPublishes`、`TestSyncHolds`、`TestSyncBadDatasets`）：阈值内的变化发布，超过的是 held（报告里写原因和候选，日志记 warn），参考价不变，数据集审核项尚未实现；取数或解析失败时保留最后的价格并告警，不换来源 |
| 定时同步的记录跨重启和换主存活，一轮失败不改写最后成功和待确认改价 | 记录在 `catalog.sync_state` 的一行里，每轮在自己的事务里写；测试（`TestTheSyncRecordsEachRound`）：成功写最后成功并清零连续失败，失败累加并保留两者，再失败不移动连续失败的起点 |
| 管理操作重试不重复生效 | 每个操作带 `op_key`，`change.op_key` UNIQUE，重试返回第一次的结果；测试：提交成功、响应丢失后重试 |
| 过期的取数者写不进数据 | 租约加代次，提交前校验；测试：A 取数期间租约被 B 接管，A 提交失败 |
| 锁顺序固定 | 页面或数据集行 → 审核项 → 模型行（按 id 升序，经 `lock_models`）→ `revision`；所有加锁都经同一个函数按这个顺序取。测试：两个事务以相反的顺序请求同一组模型行，都成功、不死锁 |
| 单价精确、非负，计算不溢出 | 见「数值域」；性质测试对照 `math/big`，覆盖最小单价、最大乘积、负用量、门槛求和、转分数后取整 |
| 用量不为零却没有价格的项一定被报告，不按零计 | `Cost` 返回缺价的项；`reasoning` 回退到 `output`，图片回退到同侧普通价；没有价格的服务等级让各项都缺价。表驱动测试：每个 Item 单独缺价时都被报告，包括 OpenAI 报了缓存写入而价目表没有写入价；性质测试：已定价部分的金额等于参考计算的结果，参考计算用完整的用量选档、只对有价格的项求和（去掉缺价项的用量会改变档位）；用例：缺价的 `cache_read` 200,000 加有价的输入 1，输出按高档计 |
| 上游报告的用量不会被悄悄丢掉 | 映射表外的非零字段和认不出的服务等级进 `Usage.Unknown()`。已实现：`NewUsage` 保留调用方交入的未知用量，构造与读取都复制（`TestUsageUnknown`）。protocol 从 Anthropic Messages 的流里识别：`TestRelayUsage` 加虚构的用量字段、`server_tool_use` 计数和等级，都出现在 `Unknown()` 里。protocol 也从 OpenAI 两种格式里识别：`TestRelayOpenAIUsage` 含未知用量和未知等级的用例 |
| 参考价有可复核的证据 | 发布时锁住页面行，检查证据引用当前快照、摘录逐字出现在正文里；自动发布记下条目原文（`fetch_id` 是设计，尚未实现）；测试：引用旧快照、摘录不在当前快照正文里，都被拒 |
| 公开表里没有操作者、理由、证据 | 它们只在平台数据的 `change` 里；测试：查 `information_schema`，公开表没有 `actor`、`reason`、`evidence` 列；非平台范围读不到平台数据，读不到草稿模型和它的名字、参考价 |

## 公开接口

```go
package catalog // internal/catalog

type ModelID uuid.UUID                 // 文本形式同 uuid.UUID
type Item uint8                        // 封闭枚举
type ItemSet = set[Item]               // Item 的位集合（32 位）；NumItems 是 Item 的个数
type ServiceTier uint8                 // 封闭枚举；不叫 Tier，那是账户的等级
type ServiceTierSet = set[ServiceTier] // ServiceTier 的位集合；两种集合都有 Has、Add
type Reportable struct{ Items ItemSet; ServiceTiers ServiceTierSet } // 上游可能报告的计费项和服务等级
type PricePoint struct{ ServiceTier ServiceTier; Above int64; Item Item } // Above 是区间的下门槛，第一段为 0
type Usage struct{ /* 各项数量、服务等级、不认识的用量、物理输入、原生细项，字段不导出 */ } // 只能经 NewUsage 构造
func NewUsage(q map[Item]int64, st ServiceTier, unknown []UnknownUsage) (Usage, error)
func (u Usage) OutputTotal() int64
func (u Usage) InputTotal() int64
func (u Usage) PhysicalInput() (int64, bool)
func (u Usage) WithPhysicalInput(int64) (Usage, error)
func (u Usage) NativeTokens() NativeTokens // 已含在计费总量里、只回显的供应商拆分
func (u Usage) HasNativeTokens() bool      // 供应商报告过这些拆分，全零也算；缺失不是零
func (u Usage) WithNativeTokens(NativeTokens) (Usage, error)
func (u Usage) Recount(map[Item]int64, []UnknownUsage) (Usage, error)
func (u Usage) Count(it Item) int64     // 一项的数量：protocol 按它渲染客户端看到的用量，billing 的计量读已报告的数
func (u Usage) ServiceTier() ServiceTier // 实际服务的等级，供应商记录扣费所报的等级
func (u Usage) Unknown() []UnknownUsage // 映射表外的用量字段和认不出的服务等级，返回副本
type UnknownUsage struct{ Field, Value string }
type NativeTokens struct{ VideoInput, AcceptedPrediction, RejectedPrediction int64 } // 各有 JSON 标签，零值省略
func ParseItem(name string) (Item, error)               // 存下的计费项名的反解，与 Item.String 互逆；认不出的报错
func ParseServiceTier(name string) (ServiceTier, error) // 同上，服务等级

type Schedule struct{ /* 币种、门槛口径、门槛、每个服务等级的单价；字段不导出 */ }
func ParseSchedule(b []byte) (Schedule, error)                       // 拒绝还不支持的条件
func (s Schedule) MarshalJSON() ([]byte, error)                      // 规范编码
func (s Schedule) Currency() platform.Currency                       // 价格的币种，结尾的 cost_currency
func (s Schedule) Cost(u Usage) (platform.Exact, ItemSet, error)     // 纯函数、精确、不分配内存；返回已定价部分和缺价的项
func (s Schedule) Ceiling(items ItemSet, tiers ServiceTierSet, length int64) (platform.UnitPrice, bool) // 预留的单价上界，见下
func (s Schedule) Highest(items ItemSet, tiers ServiceTierSet, length int64) (platform.UnitPrice, bool) // Ceiling 不在缺价处停下：已知价格里最高的，以及是否有缺价
func (s Schedule) BaseRate(it Item) (platform.UnitPrice, bool) // 标准档、第一道门槛以下的单价，给路由比较价格用
func (s Schedule) Missing(r Reportable) Reportable                    // 上架门槛用
func (s Schedule) Lost(prev Schedule, r Reportable) ([]PricePoint, error) // 替换参考价前防退化；两边都有分档而口径不同时 ErrTierBasisChanged
func (s Schedule) Fill(prices map[Item]platform.UnitPrice) Schedule   // 给每个服务等级缺基础价的这些项补上价格，已有的不动
func (s Schedule) Scaled(multiplier string, currency platform.Currency) (Schedule, error) // 每个单价乘倍率、改记币种，向上取整一次；没定价的项仍没有价
func Normalize(name string) string

type Snapshot struct{ /* 不可变 */ }
func (s *Snapshot) Revision() int64
func (s *Snapshot) ResolvePublic(name string) (Resolution, error) // 用户请求、模型列表、回退列表；名字和别名
func (s *Snapshot) Recognize(name string) (Recognition, bool)    // 给 suppliers 和数据集核对
func (s *Snapshot) Model(id ModelID) (Model, bool)                // Model.Reportable() 给上架门槛
func (s *Snapshot) ReferencePrice(id ModelID, currency string) (ReferencePrice, bool)
func (s *Snapshot) Models() iter.Seq[Model]                       // 只有 listed 模型

type Resolution struct{ Model ModelID; Floating bool }
type Recognition struct{ Model ModelID; Exact bool } // Exact：名字本身就是版本证据

type Config struct{ MaxStale, PollEvery time.Duration; Logger *slog.Logger; PriceChangeLimit int } // 默认 30 秒、5 秒、slog.Default()、25（百分比）；MaxStale 必须大于 PollEvery
type Catalog struct{ /* db、当前快照 */ }
func Open(db *pg.DB, scope pg.Scope, cfg Config) (*Catalog, error) // 平台范围由 cmd/axiom 注入
func (c *Catalog) Current() (*Snapshot, error)              // 超过陈旧上限返回 ErrStale；一个请求从头到尾用同一个快照
func (c *Catalog) Ready() bool
func (c *Catalog) Refresh(ctx context.Context) error        // 读 revision，变了才全量加载
func (c *Catalog) Run(ctx context.Context) error            // 每 PollEvery 刷新快照；数据集同步由 RegisterSync 登记到队列，页面监测由 RegisterPages 登记，数据集核对仍是设计
func (c *Catalog) CurrentPrices(ctx context.Context) ([]ReferencePrice, error) // 从数据库一致读取，给 billing 比对；设计，尚未实现
// 读取（「管理操作」一节），平台范围：Authors、GetAuthor、Models(ModelQuery)、Model(ModelID)、Aliases，返回 Author、ModelSummary、ModelView、Alias。
// 管理操作（「管理操作」一节），平台范围：SaveAuthor、CreateModel、AddName、SaveAlias、UpdateSpec、ListModel、Retire、PublishPrice、
// SelectPriceSource、SelectDatasetSource、WatchPage、UnwatchPage、ReviewPage，请求类型带 Op{Actor, Key, Reason}，返回 Result。通用 ListReviews、ResolveReview 随数据集审核项（设计，尚未实现）。

type Fetched struct{ Source PriceSource; Body []byte; At time.Time } // 一份取回的数据集
func NewFetcher(client *egress.Client) *Fetcher                     // 固定地址、60 秒、32MB、只收 200
func NewTestFetcher(t testing.TB, client *egress.Client, urls map[PriceSource]string) *Fetcher // 仅测试二进制能覆盖地址，节点集成测试用本地数据集
func (f *Fetcher) Fetch(ctx context.Context, src PriceSource) (Fetched, error) // At 为零
func (c *Catalog) Fetch(ctx context.Context, f *Fetcher, src PriceSource) (Fetched, error) // 取数，At 取自取数前数据库的时钟
func ParseLiteLLM(body []byte) ([]Entry, error)                     // 条目；ParseOpenRouter 同；整份不能解析是错误，单条不可用记在 Entry.Err
func (c *Catalog) SyncDataset(ctx context.Context, f Fetched, publish bool) (SyncReport, error) // publish 为 false 是演习：只报告
func (c *Catalog) SyncAll(ctx context.Context, f *Fetcher, publish bool) ([]SyncReport, error)  // 一轮的全部数据集；定时任务调的就是它
type SyncJobConfig struct{ DB *pg.DB; Scope pg.Scope; Queue *pg.Queue; Fetcher *Fetcher; Logger *slog.Logger; Meter metric.Meter; Clock func() time.Time; AlertAfter time.Duration } // Clock 为空用数据库 clock_timestamp()；AlertAfter 为零用 48 小时代码兜底
func RegisterSync(ctx context.Context, cfg SyncJobConfig) error // 队列启动前登记：每天一轮 catalog.sync 在 syncs 队列上；ctx 结束立即取消取数，结果仍有独立的记录期限
func (c *Catalog) SelectDatasetSource(ctx context.Context, f Fetched, r SelectDatasetSourceRequest) (Result, error)
func (c *Catalog) Check(ctx context.Context, authors []string) ([]ModelCheck, error)           // 只读
func (c *Catalog) Unrecognized(entries []Entry, authors []string) ([]string, error)           // OpenRouter 列出而目录不认识的纯文本模型
type SyncResult struct{ Model ModelID; Slug, Entry string; Base *uuid.UUID; Outcome Outcome; Reason string; Candidate jsonx.Raw } // published / unchanged / differs（演习） / held
```

## 测试

> 已有：计价的性质测试和基准、服务等级与档位交叉、`Missing`、`Lost`、`NewUsage`、`Unknown`、价目表解析与规范编码的用例；`Normalize` 的黄金表和模糊测试；真实 PostgreSQL 上的行级安全与授权、参考价历史的约束、上架门槛、改选来源、重试与并发的 `rev`、快照加载与陈旧上限、名字解析（`ResolvePublic` 零分配用测试断言，没有单独的基准）；`axiom catalog` 命令。数据集的数字、两个数据集的解析（`testdata` 里 2026-10-15 取回并裁剪的 LiteLLM 与 OpenRouter 样本，加几条编造的坏数据）、取数的限制、同步、改选到数据集、检查和 `MapUpstream` 的测试也已有，测试里不联网。页面监测的测试也已有：取数的 robots、间隔、期限、大小与重定向（`TestPageFetchRespectsRobotsLimitsAndRedirects`），抽取与真页片段（`TestPageExtractionIgnoresLayoutAndRejectsAmbiguity`、`TestRecordedPricePagesSeparateTextRuns`、`TestLegacyPageExtractionKeepsAdjacentCellBoundaries`），抽取版本升级与旧 worker（`TestPageExtractorUpgradeWithSameContentKeepsSnapshot`、`TestPageExtractorUpgradeWithChangedContentReviewsAgainstPriorSnapshot`、`TestPageExtractorUpgradeDoesNotCollapseAdjacentCells`、`TestPageExtractorUpgradeKeepsAnOpenReview`、`TestPageOlderExtractorCannotMoveSnapshotBack`、`TestPageSnapshotVersionCannotGoBackwards`），快照、审核与信号同事务（`TestPageHistoryReviewsAndSignalsCommitTogether`），过期租约（`TestExpiredAndDisabledPageWorkersCannotWrite`），证据核对（`TestOfficialEvidenceMustMatchCurrentPageAuthorAndCurrency`、`TestOfficialPublishHoldsCurrentPageUntilCommit`）和站点预约与过期告警（`TestPageSiteReservationsAndOverdueSignalsAreShared`）。尚未有：「两表读取之间插入并发提交」的测试（一致性由 `pg.ReadTx` 的测试担保）。

- 数据库测试用 `pgtest`，与 platform 相同：每个测试一份模板副本，可以并行。
- 计价对照 `math/big` 做性质测试：随机的价目表（零到三档、随机口径、每个服务等级随机有无价格、0 到 1000 的单价）、随机的服务等级和 0 到 10¹⁰ 的用量，与独立的模型比较选等级、选档、沿用该等级的基础价、推理按输出价、缺价的项和已定价部分的总额，并检查 `Missing` 覆盖 `Cost` 报告的每个缺价项：通过上架门槛的价目表，调用时不会缺价。`ParseSchedule` 做模糊测试：能解析的输入，规范形式再解析一次写出的字节不变。`Normalize` 做黄金表，覆盖 `-YYYYMMDD`、`-YYYY-MM-DD`、`-MMDD`、`-MM-DD` 四种日期写法，加上数字序列不变的模糊测试。
- `ResolvePublic`（包括需要归一化的名字，如 `claude-sonnet-4.5`）和 `Cost` 有零分配的基准测试：归一化写进栈上的缓冲区，用 `m[string(buf)]` 查 map，Go 对这种写法不分配。
- 名字解析的用例：没有 `:free` 条目时 `:free` 返回"模型不存在"，不落到基础模型；有 `:free` 条目时精确命中它；别名读最新 `rev`，目标下线返回 `ErrRetired`；下线模型的公开名字返回 `ErrRetired`，它也不出现在 `Models()` 里；绑定到固定版本模型的不带日期名字，`Recognize` 得到 `Exact=false`，`ResolvePublic` 仍得到原来的版本。
- 数据集数字的用例（已有，`TestPlainDecimal`、`FuzzPlainDecimal`、`TestParseLiteLLM`）：`3e-06` 与 `0.000003` 得到同一个 `UnitPrice`，全程不经 float64；指数超出 ±30、展开后超过 15 位小数的条目被拒。
- 每家的计价示例（缓存读 99,999 加输入 1 不换档、再加 1 换档；Gemini 缓存 200,000 加新输入 1 换档；Gemini 文本 190,000 加单独计价的图片 20,000 换档）写成用例。
- 用量折算与缺价的用例：Anthropic 只有顶层 `cache_creation_input_tokens` 时按请求的缓存断点归类，有 `cache_creation` 而两者之和不等时报错；Gemini 超过 200k 且有思考 token 时，思考按高档的输出价计；Gemini 的音频输入折成 `audio_in`；`Missing` 对缺音频价、缺 priority 价的价目表返回 `audio_in` 和 priority；有档位却没有 `TierBasis` 的价目表被拒。
- 服务等级与档位交叉的用例：标准档输出 2、长上下文 4，priority 输出 3、长上下文 5，四种组合各得其价；priority 没录 `cache_read` 时，priority 请求的缓存读被报告缺价，标准档请求不受影响，`Missing` 返回 priority；priority 漏写长上下文档或门槛与顶层不同的价目表被拒。
- `Lost` 的表驱动用例：旧 priority 有输入、输出价只缺 `web_search`，新 priority 只有输出价时返回 priority 的 `input`，新价目表没有 priority 时返回 priority 的 `input` 和 `output`；旧价有 `reasoning`、新价没有但有 `output` 时为空；新价目表多一道门槛、高区间缺 `output` 时只返回高区间；新价目表去掉旧门槛时为空（基础价覆盖高区间，档位的丢失由自动发布另查）；两表都只在 200k 以上为 priority 提供 `output`、旧表口径 {`input`, `cache_read`} 而新表 {`input`} 时返回 `ErrTierBasisChanged`（请求 `input=1、cache_read=200000、output=1` 在旧表有输出价、在新表没有），两表都没有分档而口径不同时照常比较。自动发布的条目让 `Lost` 不为空时只开审核项，参考价不变。
- 节点集成测试默认不登记价格周期任务；`TestServeCancelsAPriceFetchOnShutdown` 显式使用本地数据集，在等待响应头和读取响应体两种阶段暂停取数，取消节点后验证请求立即取消、节点无错误退出、失败记账且任务完成，不等 30 秒队列期限。
- 定时同步的用例（`sync_job_test.go`、`sync_job_recovery_test.go`）：队列启动即执行，健康或上游 503 的轮次均完成，`queue=syncs`、`max_attempts=1`，信号用 `sync.Once`；成功、失败、恢复及部分发布后的下一轮不重复发布；失败保留最后成功和完整待确认数，取消后仍记录并完成；恰在 48 小时不触发兜底、超过后日志和计数上升、恢复停止、从未成功也有兜底；manual/official 来源和涨跌幅安全阀保持不变。
- 状态指标和边界用例（`sync_metrics_test.go`）：没有下一轮时 gauge 仍按受控时钟增长，从未成功、失败和恢复均能读到；生产记录时间在前后两次数据库 `clock_timestamp()` 之间；缺条目的 held 不计入待确认数且保持价格；记录写入失败仍返回错误。

`Schedule.Aligned` 合并两张表的门槛并逐项取高（或相加），`Schedule.AddMarkup` 把固定加价应用到各服务等级，`Schedule.CoversCost` 与 `Schedule.PriceChange` 分别比较保本和相对变化；都复用实际计费的有效取价，口径不同返回 `ErrTierBasisChanged`。这些是后台售价发布的纯运算，不在逐事件调用路径执行。

## 决策与理由

| 决策 | 理由 | 不选 |
|---|---|---|
| 公开和内部名字同表，`visibility` 区分，`norm` 唯一 | 唯一性对两类一起成立，内部名字不会和公开名字撞 | 两张表：唯一性要跨表检查 |
| `:free` 是独立的 `model` 行（slug 和公开名字都带 `:free`），不是基础模型的变体列 | 它有自己的规格、上下限、参考价和端点，OpenRouter 也把它当独立条目；复用现有名字、上架门槛和路由，`:free` 精确命中，不会落到基础模型 | 在 `model` 上加 `free_of` 或在解析时切后缀：发现、价格和路由要另写一套，且容易把付费模型的价带进免费名字 |
| 别名指向一个具体模型，不按作者/系列前缀或日期推导 | 前缀推导会把 `acme/gpt-5` 和 `acme/gpt-50` 串成一个系列，也会在新成员出现时悄悄换掉发现和计费用的模型；显式目标让运营决定何时切换，`change` 留下审计 | 解析时按前缀挑最新：无法表达系列边界，新的上架会改变已发布名字的含义 |
| `alias` 只追加 `(name, rev)`，快照读最高 `rev` | 切换别名也是一次有审计的目录改动，旧快照仍能读到它当时的映射；写时用事务级 advisory 锁，避免两个首个版本竞争 | 原地更新一行：要 UPDATE 权限，旧快照与 `change` 对不上 |
| 价目表存规范 jsonb，结构和校验在 Go | 三个模块共用；规范编码让比较和幂等变成字节比较 | 拆成计费项、条件、档位三张表：每个模块都要三张表和重组代码 |
| 单价是 int64 的 10⁻¹⁵ 定点，`Cost` 返回 128 位精确值；汇率和倍率在 billing 用分数 | 域有界时 128 位整数不会溢出、不分配内存，调用路径上快；10/71 这样的值有限小数表示不了，换算必须用分数 | 全程 `math/big`：每次都分配；全程十进制：汇率不精确 |
| `Cost` 返回缺价的项，不返回错误 | 计费的红线是不会算不出钱：缺项要按已定价部分加兜底价收，返回错误就丢了已定价部分 | 缺价就返回错误，或调用前按请求特征拒绝：负责人否决了后者（Issue #24），计费的上架门槛按模型检查 |
| 价格来源是模型上的一列，发布时在锁住的模型行上核对来源 | "手填不被覆盖""不自动换来源"变成一个比较，和发布同一个事务，没有竞态 | 每次同步时查一张来源配置表：同步与改选并发时可能写进旧来源的价格 |
| 快照用只读 Repeatable Read 加载，只接受更大的 revision | Read Committed 下同一事务的两次查询可能看到不同的提交（PostgreSQL 文档《Transaction Isolation》） | 多表分别查询 |
| 快照陈旧超限就拒绝新请求 | 刷新持续失败时，其他节点已下线的模型不能无限期地继续被调用 | 一直用旧快照 |
| 售价由 billing 根据状态生成候选、检查成本与变化阈值后持久发布，路由只使用已发布价格 | 参考价和售价版本分别归各自模块；保本和确认规则见 [billing](../billing/README.md#定价规则) | 在路由快照里直接把参考价当售价 |
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
| 数据集同步尚不落条目和数据集审核项，页面 review 只收 page_changed | 条目、候选和比较每轮可重算，发布已持久化；页面有独立的证据、通知与处理命令，不把数据集需求猜进其表结构 | 提前合并两类审核内容：扩展尚未实现的数据集流程会绑定页面生命周期 |
| 分档的口径取自当前参考价；没有当前价或门槛不同就不发布 | 数据集只给门槛（LiteLLM 的 `_above_200k_tokens`、OpenRouter 的 `min_prompt_tokens`），不说哪些用量相加，口径错了长请求就按错的档计价；口径是厂商的规定，要逐家引用原文，现在只查实过 Anthropic 和 Gemini | 代码里写一张厂商口径表：没查实的厂商会被猜；表与当前价冲突时还要另处理 |
| 只有「名字本身是版本证据」的名字才对应条目 | 数据集里 `gpt-4o` 这样的条目可能已指向新版本，把它的价格发布给固定在旧版本的模型就是错价；与 `Recognize` 的 `Exact` 同一规则 | 所有名字都对应：数据集换版本时悄悄改了旧模型的价格 |
| 同一模型对应到几个条目时，取说出其余所有条目所说的那一个，否则 held | 实测 LiteLLM 的 `deepseek-chat` 与 `deepseek/deepseek-chat` 只差一个值为 0 的缓存写；取并集或逐字段表决会拼出没人发布过的价目表（见 docs/domains/catalog.md「参考价怎么进入目录」6） | 有歧义就全部 held：DeepSeek 永远同步不了；取第一个：随键的顺序错价 |
| OpenRouter 只用无 `overrides` 的条目，带 `overrides` 的 held | 文档与数据都没说 `min_prompt_tokens` 的门槛含义和求和口径，且有按时段的覆盖；猜一个就是错价 | 按 `min_prompt_tokens` 当严格大于、口径同当前价：恰在门槛处差一个 token，时段覆盖无法表达 |
| catalog 自带变化阈值（`Config.PriceChangeLimit`，默认 25%）和合理性检查 | billing 的安全阀在 catalog 之上、调用不了；25% 是我定的过渡值：厂商改价多在这以内，超过的让人看一眼代价小，漏掉一次错价代价大；阈值应是后台数据，现在是启动配置 | 等 billing 的安全阀：自动发布先没有保护 |
| `SelectPriceSource` 仍只接受 `official`、`manual`，数据集来源另设 `SelectDatasetSource` | 价目表只来自条目这一点由方法保证，不靠证据里写了什么；放开原方法，任何调用者都能把手填的价目表记在数据集名下 | 放开原方法、命令从条目拼请求 |
| 供应商上游名字的映射在 `syncer.MapUpstream`：先别名，再目录的内部识别，目标必须是上架的模型；未映射则 `EndpointFacts.Mapped` 为假 | 与 docs/domains/suppliers.md 的前两步一致；退役或不存在的模型不能接流量。不带日期的名字只是候选（`Unverified`），端点可建但要先过真伪检测；调用点在写 `supplier_models.model_id` 的同步持久化（#25，见 internal/suppliers/README.md） | 目录里加一个「可路由」查询：suppliers 的映射规则放进了 catalog |
| 同步的记录放在数据库的一行里，不放在节点的内存里 | 只有领导节点插入周期任务、任何 worker 执行，换主和重启都会清掉内存里的计数；告警量的是「多久没有成功」，记录必须跨这两件事活着，operator 也能直接查 | 节点内存里的计数：重启即归零；告警规则直接查 `river.river_job`：它记不了待确认改价，还把模块的语义搬进运维系统 |
| 取数失败写进状态后完成任务，`MaxAttempts=1`，等待下一日轮次 | 负责人在本 PR 评审后的修复指令中选择方案 (a)。River 默认短退避会在一次短暂上游故障后耗尽尝试并触发丢弃告警，与过久未成功告警重复；状态、gauge 和兜底 overdue 已提供所需信号 | 短间隔重试：过早告警；小时级重试：引入另一套同步时序，当前不采用 |
| 状态导出为 gauge，运维节点判断后台阈值；节点 overdue 保留代码默认兜底 | gauge 每次采集读持久化状态与数据库时钟，不依赖下一轮执行；运维可设置更早的时间阈值或连续失败阈值。节点的 48 小时只是代码默认值，未接入后台配置；通知仍由运维节点决定 | 只在失败轮次上升的计数：任务不再执行时看不到陈旧；把代码常量称为后台数据：运维无法提前判断 |

`ReadSnapshot` 也供售价发布者在其事务内读取数据库拥有的目录输入，避免以某节点自己的旧目录覆盖共享候选。
