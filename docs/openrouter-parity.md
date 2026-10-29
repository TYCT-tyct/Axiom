# OpenRouter 开发者能力对照表（Axiom / new-api / sub2api）

本表列出 OpenRouter 对开发者提供的全部接口和行为，逐项对照 Axiom 的现状，是「对齐 OpenRouter」这一目标的进度表。缺口按下文末尾的顺序归入对应的总 Issue；做完一项就在同一个 PR 里改这里的「Axiom 现状」一栏，OpenRouter 改了文档就更新那一行并换上新的访问日期。

访问日期：2026-10-19。OpenRouter 一栏来自在线文档（openrouter.ai/docs 各页的 .md 版本）和官方 OpenAPI（https://openrouter.ai/docs/openapi/openapi.yaml，字段名从这份规范里逐项读出，不是凭记忆）。Axiom 一栏的「有」都有代码证据，只写在文档里的不算；代码和文档不一致的地方在差距说明里标明「文档说有、代码没有」。new-api、sub2api 只读 grep，引用格式为 `文件:行`。

约定：
- Axiom 路径简写：`gw/` = `internal/gateway/`，`proto/` = `internal/protocol/`，`rt/` = `internal/routing/`，`cat/` = `internal/catalog/`，`bill/` = `internal/billing/`。
- new-api 简写 `na:`（`E:/Anb/ref/new-api/`），sub2api 简写 `s2:`（`E:/Anb/ref/sub2api/backend/`）。
- 来源列里 `docs/` = `https://openrouter.ai/docs/`，`api/` = `https://openrouter.ai/docs/api/api-reference/`，`spec` = OpenAPI 规范里的 schema 名。
- Axiom 范围声明：`docs/product.md:92` 把向量化、重排、图片/视频/语音生成、批量调用、Auto Router 划在「以后」；用户自带厂商 key 划在「以后」（`docs/product.md:96`）。表里这些行照实标「没有」，缺口排序时单独考虑。

## 文档与代码不符的地方（本表建立时已在 gateway.md 改正）

1. `docs/domains/gateway.md:151` 写接受 `@preset/名称`；全仓库代码没有解析 `@preset` 的地方（`gw/route.go:270-272` 只有一个零号预设，注释说明 M1 只路由零号预设），目录名字解析遇到 `:` 就查不到（`cat/snapshot.go:215`）。

## 1. 三种调用格式

| 能力 | OpenRouter 行为 | 来源 | Axiom 现状 | new-api | sub2api | 差距说明 |
|---|---|---|---|---|---|---|
| `POST /api/v1/chat/completions` | OpenAI Chat 格式，`choices` 恒为数组，流式用 `delta`；以 Chat 为基准格式 | docs/api_reference/overview | 有：`gw/handler.go:66-68` 注册 `/v1` 与 `/api/v1` 两个前缀 | `na:router/relay-router.go:105` | `s2:internal/server/routes/gateway.go:233` | 无；Chat 客户端可以走 Chat、Messages、Responses 端点（`docs/domains/gateway.md` 转发规则「跨格式」），Messages、Responses 客户端也可转换到只说 Chat 的端点（#301 B） |
| `POST /api/v1/responses` | OpenAI Responses 格式，无状态 | docs/api_reference/responses/overview | 有：`gw/handler.go:66-68`；比 OpenRouter 多保存会话状态（文档偏离项） | `na:router/relay-router.go:110`（compact）及 `/responses` | `s2:internal/server/routes/gateway.go:214,221` | 无 |
| `POST /api/v1/messages` | Anthropic Messages 格式 | api/anthropic-messages/create-a-message | 有：`gw/handler.go:66-68` | `na:router/relay-router.go:97` | `s2:internal/server/routes/gateway.go:192` | 无 |
| Messages 的 `fallbacks` 参数（最多 3 项，只许 `model`，不能与 `models` 同用） | 映射到 `models` 路由 | docs/guides/routing/model-fallbacks | 没有：未映射到 `models`；`fallbacks` 不在 `proto/prepare.go:281` 的丢弃名单里，Messages 顶层对未知成员的处理未核实（待核实：原样转给上游，还是由 scanner 拒绝） | 无 | 无 | Anthropic SDK 用户的备选模型写法是否生效未核实 |
| `POST /api/v1/messages/count_tokens` | OpenRouter 文档未列此路由 | （OpenAPI 路由表无） | 有：`gw/handler.go` 经 `gw/readonly.go:35` | `na:router/relay-router.go:96`（已注释掉） | `s2:internal/server/routes/gateway.go:203` | Axiom 比 OpenRouter 多，非缺口 |
| `POST /v1/completions`（旧版补全） | OpenAPI 路由表没有该路径；Streaming 文档的措辞提到 completions | docs/api_reference/streaming | 没有：只注册 chat/responses/messages（`gw/handler.go:66`） | `na:router/relay-router.go:102` | 无 | OpenRouter 自己现在也不列它，低优先 |
| 流式 SSE，注释保活，`data: [DONE]` | 偶发 `: OPENROUTER PROCESSING` 注释；Chat 流最后带 usage 的一块 | docs/api_reference/streaming | 有：保活 `proto/relay.go:429,598`，Chat 最后一块带 usage（`docs/domains/gateway.md:73`） | 有（ping，见差分规则） | 有 | 无 |
| 流式取消（断开连接即停止上游） | 断开连接立即停止模型处理和计费 | docs/api_reference/streaming | 有：`gw/forward.go:251,276` 用 `context.Cause` 区分客户端断开；测试 `gw/exec_disconnect_test.go` | 有（relay 取消） | 有（`gateway_handler_cancellation_test.go`） | 无 |
| 响应头 `X-Generation-Id` | 所有端点都返回 | docs/api_reference/streaming | 有：`gw/handler.go:83`，同值也在 `X-Request-ID`（`platform/httpserver/public.go:27`） | 无 | 无 | 无 |
| WebSocket 升级 | 未提及 | — | 不适用：一律 400（`gw/handler.go:69` `refuseUpgrade`） | `na:router/relay-router.go:78,85` 支持 | 有 `/live` | 非 OpenRouter 能力，不计缺口 |
| `GET/POST /api/v1/embeddings`、`/embeddings/models` | 向量化，含批量 | docs/api_reference/embeddings | 没有（范围声明「以后」） | `na:router/relay-router.go:135` | `s2:internal/server/routes/gateway.go:240` | 见 §16 |
| `POST /api/v1/rerank` | 重排 | api/rerank/submit-a-rerank-request | 没有（范围声明「以后」） | `na:router/relay-router.go:151` | 无 | 见 §16 |

## 2. 模型列表与模型端点

模型对象、价格单位与查询参数于 2026-10-20 用 curl 复核：[模型列表文档](https://openrouter.ai/docs/api/api-reference/models/get-models)、[官方 OpenAPI](https://openrouter.ai/openapi.json)。旧的 `docs/api-reference/list-available-models` 当日返回 404；RSS 两个旧参数仍明确拒绝，不静默丢弃。

| 能力 | OpenRouter 行为 | 来源 | Axiom 现状 | new-api | sub2api | 差距说明 |
|---|---|---|---|---|---|---|
| `GET /api/v1/models` 基本列表 | 返回 `data[]` 模型目录 | api/models/list-all-models-and-their-properties | 有：`gw/openrouter.go` 的 `listOpenRouterModels`，外壳是 OpenRouter 的 `data`/`links.next`/`total_count`；按 key 过滤，只列这把 key 能调用、路由快照里有公共端点且有售价的模型 | `na:controller/model.go:218` | `s2:internal/server/routes/gateway.go:207` | 价格取主站售价（标准服务等级、第一档），与路由快照同一次构建、一起发布：列表上的价格就是调用收费的价格；`/v1/models` 保持 OpenAI+Anthropic 合并形，两种客户端各读各的 |
| 模型字段 `id`、`canonical_slug`、`name`、`created`、`description`、`hugging_face_id`、`knowledge_cutoff`、`expiration_date` | 模型身份与元信息 | spec `Model` | 部分：`id`、`canonical_slug` 都是这个模型的公开标准 ID（目录名去掉内部版本后缀：`acme/claude@20250929` 按 `acme/claude` 列出，并要解析回这个模型，否则整个不列），`name` 是标准 ID 去掉作者的部分，`created` 恒为 0；`description`、`hugging_face_id`、`knowledge_cutoff`、`expiration_date` 不输出（目录没有这些数据） | 无（OpenAI 形） | 无 | 缺的是目录里没有的描述和日期；`created` 恒 0，按时间排序的选择器仍会失效（`sort=newest` 因此回 400）；内部 `@版本` 后缀不对客户端露出，它不是能调用的名字 |
| `context_length`、`top_provider{context_length,max_completion_tokens,is_moderated}`、`per_request_limits` | 上下文与输出上限 | spec `Model`、`TopProviderInfo` | 有：`context_length` 与 `top_provider{context_length,max_completion_tokens}` 取规格的 `context_length`/`max_output_tokens`，`is_moderated` 恒 false；`per_request_limits` 是 null（Axiom 没有按请求的上限） | 无 | 无 | 规格没声明上限、或售价填不出 `prompt`/`completion` 的模型整个不列：OpenRouter 的对象里没有 null 的位置 |
| `pricing{prompt,completion,request,image,image_output,image_token,audio,audio_output,input_audio_cache,input_cache_read,input_cache_write,input_cache_write_1h,internal_reasoning,web_search,discount,overrides}` | 每 token/每次的价格（字符串，美元） | spec `PublicPricing` | 部分：十个成员，都是字符串，取主站售价的标准服务等级、第一档——`prompt`、`completion`、`input_cache_read`、`input_cache_write`、`input_cache_write_1h`、`audio`、`audio_output`（美元 / token）和 `web_search`（每次），以及只给能收图片、会推理的模型的 `image_token`、`internal_reasoning`（价目表对这两项会用所在侧的原价兜底，纯文本模型不该出现）；`image`、`image_output`（按图）、`request`（按次）、`input_audio_cache`、`discount`、`overrides` 省略：售价表没有这些单位 | `na:controller/pricing.go:38`（`/api/pricing`，非 OpenAI 路径） | 无 | 按图、按次计价要等 #320；缺的成员省略而不是写 0（0 会被读成免费），按 `prompt`+`completion` 估算成本已经准确 |
| `architecture{modality,input_modalities,output_modalities,tokenizer,instruct_type}` | 模态 | spec `ModelArchitecture` | 部分：`modality`、`input_modalities`、`output_modalities` 取规格声明的能力（`text`；有 `images` 加 `image`，有 `documents` 加 `file`），输出恒 `text`；`tokenizer`、`instruct_type` 不输出 | 无 | 无 | 目录没有分词器和指令类型；音频、视频输入不是能力，不声称 |
| `supported_parameters`、`default_parameters`、`reasoning{supported_efforts,default_effort,default_enabled,mandatory,supports_max_tokens}`、`supported_voices` | 支持的参数、默认采样值、推理选项 | spec `Model`、`ModelReasoning`、`DefaultParameters`；docs/guides/best-practices/reasoning-tokens | 部分：`supported_parameters` 按能力与方言推出（`temperature`、`top_p`，`default_sampling_only` 为 yes 时不给）、`max_tokens`、`max_completion_tokens`、`response_format`、`stop`、`tools`、`tool_choice`、`parallel_tool_calls`、`reasoning`、`reasoning_effort`；`reasoning` 在 `thinking:"manual"` 时给 `supports_max_tokens:true`，未知 `mandatory` 不输出（官方必填，有意偏离）；`default_parameters`、`supported_voices` 是 null | 无 | 无 | 规格没说的参数不声称（`seed`、`logprobs`、`verbosity`、`structured_outputs`、`web_search_options` 等）；`default_parameters` 与 `reasoning` 的其余成员没有数据 |
| `alias_target`、`benchmarks`、`links.details` | 别名目标、基准分 | spec `Model` | 部分：`links.details` 指向本 API 的 `/api/v1/models/{id}/endpoints`；`alias_target`、`benchmarks` 不输出 | 无 | 无 | 别名见本表 `~author/family-latest` 一行；基准分 Axiom 不测 |
| 查询参数 `category`、`supported_parameters`、`output_modalities`、`input_modalities`、`sort`、`q`、`context`、`min_price`、`max_price`、`offset`、`limit`（≤1000）、`use_rss` | 服务端过滤和排序 | api/models/list-all-models-and-their-properties（OpenAPI `/models` 的 parameters） | 部分：读 `offset`、`limit`（1–1000）、`q`、`context`、`min_price`/`max_price`（prompt，$/M token）、`min_output_price`/`max_output_price`（completion）、`input_modalities`、`output_modalities`、`supported_parameters`、`model_authors`，`sort` 只支持 `pricing-low-to-high`、`pricing-high-to-low`、`context-high-to-low`；文档里有而 Axiom 没有数据的（`category`、`arch`、`providers`、`distillable`、`zdr`、`region`、`use_rss`、`min_age_days`/`max_age_days`、intelligence/coding/agentic 的 index 与 tool success rate）按名字回 400 并说明缺什么 | 无 | 无 | 不静默忽略：过滤条件丢掉的答案客户端看不出来；文档之外的参数（缓存参数、`utm_*`）不报错 |
| `GET /api/v1/models/{author}/{slug}`、`/model/{author}/{slug}` | 单个模型详情，后缀按变体规则解析 | api/models/get-a-model-by-its-slug | 部分：`GET /api/v1/models/{model}` 返回 `{data: 模型对象}`，名字按调用规则解析（`claude-live` 与 `anthropic/claude-live` 到同一个模型），`id` 是标准 ID，`links.details` 指向 endpoints 路径；`/v1/models/{model}` 仍是 OpenAI/Anthropic 形 | `na:router/relay-router.go:36`（`/v1/models/:model`，OpenAI 形） | `s2:internal/server/routes/gateway.go:209` | OpenRouter 的 `/model/{author}/{slug}`（单数）没有；目录变体后缀见本表下一行 |
| `GET /api/v1/models/count` | 模型总数 | api/models/get-total-count-of-available-models | 有：`data.count` 为同次发布中本 key 可见公共模型数，支持 `output_modalities`，非法/重复模态和损坏查询串回 400，不应用分页 | 无 | 无 | 2026-10-29 核对官方模型计数文档；响应数据来自内存投影，实时准入仍查库 |
| `GET /api/v1/models/{author}/{slug}/endpoints` | 每个供应商端点：`provider_name`、`tag`、`quantization`、`pricing`、`context_length`、`max_completion_tokens`、`max_prompt_tokens`、`supported_parameters`、`supports_implicit_caching`、`uptime_last_5m/30m/1d`、`latency_last_30m`、`throughput_last_30m`、`status`、`service_tier` | api/endpoints/list-all-endpoints-for-a-model；spec `PublicEndpoint` | 部分：每个公共路由分组端点一个模型内序号 tag（不暴露内部 UUID），非零 Owner 的私有端点从共享投影排除，公开 provider_name 为 Axiom；价格、上下文和输出上限来自同次构建 | 无 | 无 | 2026-10-29 核对官方模型端点文档；量化、性能百分位和窗口可用率为 null；未知能力、描述和模板字段省略，不暴露货源，字段偏离清单见 gateway.md |
| `GET /api/v1/models/user` | 按账户供应商偏好、隐私设置、护栏过滤后的模型 | api/models（OpenAPI `/models/user`） | 部分：复用列表的 key 可见性、过滤和分页，next 保留 user 路径 | `na:router/api-router.go:101`（`/user/self/models`） | 无 | 2026-10-29 核对官方用户模型文档；缺供应商偏好、隐私和护栏数据，不伪称这些过滤已生效 |
| `GET /api/v1/providers` | 供应商列表及隐私政策链接 | api/providers/list-all-providers | 部分：有可见模型时列 Axiom/axiom，政策链接、总部和机房为 null，无可见模型时为空 | 无 | 无 | 2026-10-29 核对官方 providers 文档；聚合供应商不暴露真实货源，缺失政策和地域不虚构 |
| `GET /api/v1/endpoints/zdr` | ZDR 过滤影响预览 | api/endpoints/preview-the-impact-of-zdr-on-the-available-endpoints | 没有 | 无 | 无 | 低优先 |
| 模型变体后缀规则（目录变体 `:free/:batch/:thinking/:extended`，路由变体 `:nitro/:floor/:exacto/:online`；后缀可叠加；缺失的目录变体 404） | 后缀是目录条目或路由开关 | docs/guides/routing/model-variants/overview | 部分：`gw/variant.go` 的 `parseModelName` 在查目录之前读变体（调用、`models` 备选、`count_tokens`、模型详情与 endpoints 共用），`:nitro/:floor` 生效，可叠加，最后一个排序变体生效；`:exacto/:online` 回 400；目录变体回 400 并说明尚不支持，不认识的后缀回 404（`cat/snapshot.go:215` 仍拒绝带 `:` 的名字，目录没有变体条目） | 无 | 无 | 目录变体要有条目才能服务，见各行 |
| `~author/family-latest` 别名 | 总是解析到该系列最新模型，推理参数自动降级映射 | docs/guides/routing/routers/latest-resolution | 没有：`gw/variant.go` 认出 `~` 开头并回 400「暂不支持」（原来是 404）；目录没有别名表（名字只追加，公开名字不许 `~`、`-latest`，`cat/snapshot.go:146-149` 的 `Floating` 是供应商挪动版本，与别名无关） | 无 | 无 | 缺少显式模型族和可切换别名映射，不按前缀或日期猜测；取舍写在 `docs/domains/gateway.md` 决策与理由，归 #311 |
| 官方 `models` 列表里不含 `:nitro` 等路由变体，客户端自己派生 | 路由变体不在目录里 | docs/guides/routing/model-variants/overview | 不适用 | 无 | 无 | 随后缀支持一起做 |

## 3. 供应商路由参数 `provider`、模型备选 `models`、变体

Axiom 对 `provider` 对象的处理在 `gw/provider.go`：认识的成员读进覆盖，不认识的成员和暂不支持的取值一律 400 并指明成员名（不静默忽略）。

| 能力 | OpenRouter 行为 | 来源 | Axiom 现状 | new-api | sub2api | 差距说明 |
|---|---|---|---|---|---|---|
| `provider.order`（供应商 slug 列表） | 按序尝试，可含 `provider/variant/region/tier` 形式的端点 slug | docs/guides/routing/provider-selection | 有：按不可变公开分组代号排序（`gw/provider.go`、`rt/rank.go`） | 无（按渠道优先级/权重，非请求级） | 无 | 精确选组，不公开真实名称；官方名称没有可信映射；偏离见 gateway 领域文档 |
| `provider.only` / `provider.ignore` | 只允许/排除某些供应商，与账户级设置合并 | 同上 | 有：按公开分组代号过滤，不扩大到同供应商其他组（`rt/plan.go`） | 无 | 无 | 预设和归属权限独立生效 |
| `provider.allow_fallbacks` | 默认 true；false 配合 order 限定列表，无 order 则每个模型只取首选 | 同上 | 有：命名顺序限制集合，无顺序每个模型仅保留首条，保留 models 回退（`rt/plan.go`） | 无 | 无 | 不延长已冻结路线 |
| `provider.require_parameters` | 只路由到支持请求里全部参数的端点 | 同上 | false 接受，true 明确 400；已有能力匹配始终强制 | 无 | 无 | 没有中转端点完整参数清单，模型能力不能代替 |
| `provider.data_collection`（`allow`/`deny`） | 排除会存储数据的供应商 | 同上 | 有：`readDataCollection`（`gw/provider.go`），`deny` 映射到「不留存」政策 | 无 | 无 | 无 |
| `provider.zdr` | 只走零数据保留端点 | 同上；docs/guides/features/zdr | 有：`readZDR`（`gw/provider.go`；与 `data_collection: deny` 同一个过滤，`noRetention`） | 无 | 无 | 无 |
| `provider.enforce_distillable_text` | 只走允许文本蒸馏的模型 | 同上 | 没有：不在 `providerMembers`，由 `override` 拒绝 400（`gw/provider.go`） | 无 | 无 | 低优先；但 400 会让带此成员的客户端整体失败 |
| `provider.quantizations`（int4/int8/fp4/mxfp4/nvfp4/fp6/fp8/mxfp8/fp16/bf16/fp32/unknown） | 按量化级别过滤 | 同上 | 非空列表明确 400；空列表接受 | 无 | 无 | 缺端点量化事实，偏离见 gateway 领域文档 |
| `provider.sort` 字符串：`price` / `throughput` / `latency` | 关闭负载均衡，按该维度排序 | 同上 | 有：三种维度分别排序（`gw/provider.go`、`rt/rank.go`） | 无 | 无 | 未测量的按价格加权排在测量值之后 |
| `provider.sort` 对象 `{by, partition}`（`partition: model/none`） | 多模型备选时是否跨模型统一排序 | 同上 | 有：默认 model；none 跨主模型和备选排序 | 无 | 无 | 未知对象成员明确 400 |
| `provider.preferred_min_throughput` / `preferred_max_latency`（数字或 p50/p75/p90/p99 对象） | 偏好而非硬过滤，不满足的排后面 | 同上 | 数字按 EWMA 软偏好；百分位对象明确 400 | 无 | 无 | 没有五分钟窗口百分位，不以 EWMA 冒充 p50 |
| `provider.max_price{prompt,completion,request,image}` | 价格上限，过滤端点 | 同上 | prompt/completion 精确比较受众售价；request/image 明确 400 | 无 | 无 | 缺按请求/按张计费单位；非美元站点以站点记账货币为单位，偏离见 gateway 领域文档 |
| `provider.options`（按供应商透传的选项，spec `ProviderOptions`） | 给特定供应商的原生参数 | spec `ProviderPreferences.options` | 没有：不在 `providerMembers`，会 400 | 无 | 无 | 低优先 |
| 默认价格加权负载均衡（按价格平方倒数、近 30 秒无故障优先） | 默认策略 | docs/guides/routing/provider-selection | 部分：默认是价格加权随机（`docs/domains/routing.md` 决策与理由），加权公式未核对是否一致 | 无（渠道权重） | 无 | 行为相近，公式可能不同 |
| 失败后换端点 | 按序回退直到成功 | docs/guides/routing/provider-selection | 有：`gw/attempt.go`、`rt/plan.go:60`（`MaxAttempts`） | `na:controller/relay.go:177`（`RetryTimes`） | `s2:internal/service/gateway_anthropic_passthrough.go:802`（failover） | 无 |
| 工具调用请求的 Auto Exacto 重排 | 有 `tools` 时按工具调用成功率重排 | docs/guides/routing/auto-exacto | 部分：`rt/rank.go:68` 按实测工具调用错误率排序；只在「均衡」「质量优先」策略下生效（`docs/domains/routing.md:74`） | 无 | 无 | 需要 #313 的端点健康数据 |
| 会话粘性（`session_id`、`x-session-id` 头、`prompt_cache_key`，≤256 字符，10 分钟） | 缓存命中靠粘到同一供应商 | docs/guides/best-practices/prompt-caching | 部分：体内 `session_id`/`prompt_cache_key` 有（`rt/session.go:78`）；会话请求头转发给上游、值换成派生值（`gw/headers.go`），自己的粘性还没读它们；还会用首条消息哈希（`rt/session.go:80-85`） | 无 | 无 | 缺按请求头的分组（转发已有，中转站据此固定账号）；Embeddings/TTS 等端点要求用头分组 |
| `models` 数组（备选模型，主模型失败/限流/审核时顺序尝试，按最终使用的模型计价） | 跨模型回退 | docs/guides/routing/model-fallbacks | 有：解析 `proto/grammar.go:148-157`（最多 8 个），逐个模型排端点 `rt/plan.go:45-58`；默认关闭跨模型，只有请求给出 `models` 才开 | 无 | 无 | 无 |
| `route: "fallback"`（旧参数） | 老客户端会发 | spec `ChatRequest.route` | 部分：接受并丢弃（`proto/prepare.go:281`） | 无 | 无 | 丢弃而不报错，对旧参数是对的 |
| `service_tier`（priority/flex/ultrafast 等）与端点分级计价 | 控制成本/延迟，按实际层级计费 | docs/guides/features/service-tiers | 部分：识别并按层级定价（`proto/grammar.go:57,76,363`，`proto/prepare.go:135-143`）；`:nitro/:floor` 只带来排序，没有它们说的层级准入（不写 `service_tier` 的请求本来可由 standard、priority 服务，`proto/inspect.go:141`；flex 要客户端自己写） | 无 | 无 | 层级定义见 `docs/domains/catalog.md`；为 `:floor` 准入 flex 要决定是否改写上游的 `service_tier`，见 `docs/domains/gateway.md` 待定问题 |
| 变体 `:nitro`（按吞吐排序并准入 priority 层） | 路由变体 | docs/guides/routing/model-variants/nitro | 部分：等同 `provider.sort: throughput`（`gw/variant.go`，测试 `TestModelVariantsRoute`）；层级准入没有 | 无 | 无 | 排序一半做了；显式 `provider.sort` 优先于变体 |
| 变体 `:floor`（按价格排序并准入 flex 层） | 路由变体 | docs/guides/routing/model-variants/floor | 部分：等同 `provider.sort: price`；层级准入没有 | 无 | 无 | 同上 |
| 变体 `:exacto`（质量优先排序） | 路由变体 | docs/guides/routing/model-variants/exacto | 没有：回 400 说明暂不支持（端点质量分和工具调用错误率还没有测量，`gw/route.go:220` 质量固定为 1）；有显式 `provider.sort` 或后面的排序变体时被替换，不报错 | 无 | 无 | 等 G10 的质量数据，之后接 `routing.BestQuality` |
| 变体 `:free`（免费版条目，独立限额） | 目录变体 | docs/guides/routing/model-variants/free | 没有：404 `model_not_found`，说明没有这个变体，也不落到付费的基础模型；Axiom 没有免费层级；`bill/settle.go` 的免单规则是零输出不收费，与免费模型无关 | 无 | 无 | 产品是否要有「免费模型」待定，非阻断 |
| 变体 `:batch`（批量价条目） | 目录变体，配合 Batch API | docs/batch-quickstart | 没有（批量调用在「以后」） | 无 | 无 | 见 §16 |
| 变体 `:online`（已弃用，等价于 `web` 插件） | 路由变体 | docs/guides/routing/model-variants/online | 没有：回 400 说明没有联网搜索插件（原来是 404） | 无 | 无 | 官方已弃用；`web` 插件本身见 §10 |
| 变体 `:thinking`、`:extended`（已弃用） | 目录变体 | docs/guides/routing/model-variants/thinking、extended | 没有：404 `model_not_found`，`:thinking` 的说明提示用 `reasoning` 参数（OpenRouter 的迁移建议） | 无 | 无 | 官方已弃用，不补 |
| 后缀叠加（任意顺序，最后一个排序变体生效） | 规则 | docs/guides/routing/model-variants/overview | 有：`gw/variant.go`（`TestParseModelName`） | 无 | 无 | 无 |
| 提供者透传头 `x-anthropic-beta` | 透传 Anthropic beta 特性 | docs/guides/routing/provider-selection | 有：`anthropic-beta` 白名单转发，不筛选值（`docs/domains/gateway.md:53`） | 有 | 有 | 无 |

## 4. 自动路由和其他路由器

| 能力 | OpenRouter 行为 | 来源 | Axiom 现状 | new-api | sub2api | 差距说明 |
|---|---|---|---|---|---|---|
| `openrouter/auto`（按任务类型+市场花费份额选模型），插件 `auto-router`：`allowed_models`（通配）、`excluded_models`、`cost_tier`(low/medium/high/xhigh/max)；`openrouter/auto-beta` | 自动选模型，会话粘性 | docs/guides/routing/routers/auto-router | 没有（范围声明「以后」，`docs/product.md:92`） | 无 | 无 | 没有任务分类器和花费榜数据；可先做静态实现 |
| `openrouter/free`（随机选支持所需能力的免费模型） | 零成本实验用 | docs/guides/routing/routers/free-router | 没有 | 无 | 无 | 依赖免费模型条目 |
| `openrouter/pareto-code`（`pareto-router` 插件的 `min_coding_score`） | 按编码分挑最便宜的模型 | docs/guides/routing/routers/pareto-router | 没有 | 无 | 无 | 低优先，依赖外部基准数据 |
| `openrouter/bodybuilder`（自然语言生成并行请求） | 路由器模型 | docs/guides/routing/routers/body-builder | 没有 | 无 | 无 | 低优先 |
| Fusion 路由器 / Fusion 服务端工具（多模型审议） | 路由器模型 | docs/guides/routing/routers/fusion-router | 没有 | 无 | 无 | 低优先 |
| Switchyard 路由器（在多个模型间切换以降成本） | 路由器模型 | docs/guides/routing/routers/switchyard-router | 没有 | 无 | 无 | 低优先 |
| Jev 路由器 `typesafe/jev-router`（`jev-router` 插件，成本档和推理档） | 第三方决策模型选模型 | docs/guides/routing/routers/jev-router | 没有 | 无 | 无 | OpenRouter 的第三方集成，不建议跟 |
| 路由元数据：请求头 `X-OpenRouter-Metadata: enabled` 后响应带 `openrouter_metadata`（attempts、pipeline、endpoints…） | 暴露路由决策 | docs/guides/features/router-metadata | 没有：Axiom 有意不对外暴露路由记录和供应商（`docs/domains/gateway.md:198,224`） | 无 | 无 | 若要做，另用 `axiom` 扩展对象设计，别照搬 |

## 5. 调用记录查询 `GET /api/v1/generation`

| 能力 | OpenRouter 行为 | 来源 | Axiom 现状 | new-api | sub2api | 差距说明 |
|---|---|---|---|---|---|---|
| `GET /api/v1/generation?id=` | 按 `gen-…` ID 查调用详情 | api/generations/get-request-&-usage-metadata-for-a-generation | 部分：`gw/readonly.go:161-198`；ID 是 UUID 不是 `gen-` 前缀 | 无（用 `/api/log`） | `s2:internal/server/routes/gateway.go:210`（`/usage`，不同形） | 路径对，字段子集 |
| 字段 `id`、`model`、`created_at`、`total_cost`、`latency`、`generation_time`、`finish_reason`、`tokens_prompt`、`tokens_completion` | 基本字段 | spec `GenerationResponse` | 有：`internal/gateway/readonly.go`；另有可选 `price_version`、`charge_details` 用户分项售价扩展，见[计费](domains/billing.md#用户看得到什么) | 无 | 无 | 缺 `streamed`、`cancelled` |
| 字段 `native_tokens_prompt/completion/reasoning/cached/completion_images`、`num_media_prompt/completion`、`num_input_audio_prompt`、`num_search_results`、`num_fetches` | 原生 token 统计 | spec | 部分：有 `tokens_reasoning/tokens_cached` 和整个 `usage` 明细；`native_tokens_*` 名字没有 | 无 | 无 | 读 `native_tokens_*` 的工具会拿到空 |
| 字段 `provider_name`、`upstream_id`、`upstream_inference_cost`、`provider_responses`、`router`、`is_byok`、`cache_discount`、`api_type`、`origin`、`user_agent`、`http_referer`、`app_id`、`external_user`、`session_id`、`preset_id`、`workspace_id`、`data_region`、`web_search_engine`、`moderation_latency` | 来源、归因、供应商信息 | spec | 部分：`HTTP-Referer`、`X-OpenRouter-Title`、`X-OpenRouter-Categories` 记成 `app_referer`、`app_title`、`app_categories`（`internal/gateway/record.go:78`），控制台调用接口返回；没有应用页、排名和 `app_id`；供应商和成本仍不暴露（`docs/domains/gateway.md:79,198,224`） | 无 | 无 | `provider_name` 等不暴露是有意；`cache_discount`、`session_id`、`external_user` 应补 |
| `GET /api/v1/generation/content`（存储的提示词、补全、错误内容） | 查看已存内容 | api/generations/get-stored-prompt-completion-and-error-content-for-a-generation | 没有（内容会保存，`docs/product.md:82`，但没有这个接口） | 无 | 无 | 归 #317 控制台调用详情 |
| `POST /api/v1/generation/feedback` | 提交调用反馈 | api/generations/submit-feedback-for-a-generation | 没有 | 无 | 无 | 低优先 |
| `GET /api/v1/activity`（按日、按端点聚合，近 30 天，可按 `api_key_hash`、`workspace_id` 过滤） | 用量活动 | spec `/activity` | 没有 OpenRouter 同形接口：控制台 `/api/console/v1/calls` 支持 key/工作区/状态/请求 ID 筛选，`/calls/aggregate` 按日/模型/key 汇总调用与用户收费，`/calls/export` 流式 CSV；形状不同、要会话登录，见[用户调用记录查询与导出](domains/observability.md#用户调用记录查询与导出) | `na:router/api-router.go:298`（`/usage`，不同形） | 无 | 开发者用它做账单对账 |

## 6. 余额与 key 信息

| 能力 | OpenRouter 行为 | 来源 | Axiom 现状 | new-api | sub2api | 差距说明 |
|---|---|---|---|---|---|---|
| `GET /api/v1/credits` → `data.total_credits`、`data.total_usage` | 需要管理 key | api/credits/get-remaining-credits | 有：`gw/readonly.go:127-133`；只接受调用 key，管理 key 为 401（决定见账户 key 管理与 gateway 的决策） | `na:router/dashboard.go:18,20`（OpenAI 旧版） | `s2:internal/server/routes/gateway.go:186`（`/sub2api/billing`） | 有意偏离：管理 key 为 401，见 gateway 的决策 |
| `GET /api/v1/key` → `label`、`usage`、`usage_daily/weekly/monthly`、`limit`、`limit_remaining`、`limit_reset`、`expires_at`、`is_free_tier`、`is_management_key` | 当前 key 信息，接受管理 key | api/api-keys/get-current-api-key | 有：`gw/readonly.go:86-125`；只接受调用 key，管理 key 为 401 | 无 | 无 | 有意偏离：管理 key 为 401，见 gateway 的决策 |
| `/key` 的 `byok_usage*`、`include_byok_in_limit`、`is_provisioning_key`、`creator_user_id`、`organization_id`、`workspace_id`、`allowed_data_regions` | BYOK 与归属信息 | spec | 没有（无 BYOK；`gw/readonly.go:82-84` 注释） | 无 | 无 | 管理 key/工作区接口做了再补 |
| `/key` 的 `rate_limit`（`requests`、`interval`）、`free_model_daily_requests` | 限流提示；rate_limit 已弃用 | docs/api_reference/limits | 有 rate_limit：`gw/budgets.go` 同时报告账户/key 有效额度；无免费模型专用日计数 | 无 | 无 | 付费模型也受限，见[接入差异](domains/gateway.md#业务速率与-openrouter-的差异) |
| `/key` 的 `budgets[]`、`effective_budget`（`limit_usd`、`spend_usd`、`remaining_usd`、`reset_interval`、`resets_at`、`scope`） | 预算上限，被挡之前可读 | docs/api_reference/limits | 有：`gw/budgets.go` 报告工作区/key 预算、预留和 UTC 重置；原 limit 仍只指 key | 无 | 无 | 无组织/member/guardrail 预算；金额为本站额度，见接入差异 |
| 旧版 `GET /v1/dashboard/billing/subscription`、`/usage` | OpenRouter 没有，new-api 对接用 | （无） | 有：`gw/readonly.go:39-40,136-159` | `na:router/dashboard.go:18,20` | 无 | Axiom 比 OpenRouter 多，是给 new-api 当上游用的 |

## 7. BYOK（自带供应商 key）

| 能力 | OpenRouter 行为 | 来源 | Axiom 现状 | new-api | sub2api | 差距说明 |
|---|---|---|---|---|---|---|
| 在工作区里保存自己的供应商 key，请求优先走自己的 key，失败后可回落共享容量；每个 key 可设「不用共享容量」 | 优先区/回退区、可限定模型 | docs/guides/overview/auth/byok | 没有（范围声明：用户自带厂商 key 在「以后」，`docs/product.md:96`） | 渠道本身就是自带 key（管理员侧） | 账号池（管理员侧） | 只有管理员侧号池，用户侧 BYOK 没有；归「新建」 |
| `/api/v1/byok` CRUD（`CreateBYOKKeyRequest`/`UpdateBYOKKeyRequest`） | 管理 BYOK 凭据 | api/byok/* | 没有 | 无 | 无 | 同上 |
| BYOK 手续费（按同模型正常价的百分比）与响应里 `usage.is_byok`、`cost_details.upstream_inference_cost` | 计费 | docs/guides/overview/auth/byok | 没有（`proto/openai.go:29` 只解析不输出） | 无 | 无 | 同上 |

## 8. 提示缓存、结构化输出、工具调用、推理

| 能力 | OpenRouter 行为 | 来源 | Axiom 现状 | new-api | sub2api | 差距说明 |
|---|---|---|---|---|---|---|
| Anthropic 缓存：`cache_control` 放在内容块上（≤4 个断点）或请求顶层（自动缓存），`ttl: "1h"` | 显式/自动缓存 | docs/guides/best-practices/prompt-caching | 部分：内容块和工具上的 `cache_control` 同格式原样转发、Chat 转 Messages 时保留（`proto/tomessages.go:347,418`）；请求顶层 `cache_control` 的处理未核实 | 有（`claude_cache_creation_*`，差分规则） | 有 | 顶层自动缓存字段要核实 |
| OpenAI 显式缓存：`prompt_cache_breakpoint`、`prompt_cache_options{mode,ttl}` | 显式断点 | 同上 | 部分：Chat 转 Responses 时把文本块的 `cache_control` 转成断点（`proto/toresponses.go:133`）；`prompt_cache_options` 没有专门处理 | 无 | 无 | 低优先 |
| 缓存用量：`usage.prompt_tokens_details.cached_tokens`、`cache_write_tokens` | 读/写缓存 token | docs/guides/best-practices/prompt-caching | 有：`proto/openai.go:36,137` | 部分（字段名不同） | 部分 | 无 |
| `session_id`/`x-session-id`/`prompt_cache_key` 粘性 | 见 §3 | 同上 | 部分：体内有；请求头转发给上游、值换成派生值，自己的粘性还没读它们 | 无 | 无 | 缺按请求头的分组 |
| 响应缓存：请求头 `X-OpenRouter-Cache`、`-TTL`、`-Clear`，响应头 `X-OpenRouter-Cache-Status/-Age/-TTL/-Source-Id`；预设里 `cache_enabled`、`cache_ttl_seconds`；命中免费 | 网关级整包响应缓存，按 key 隔离 | docs/guides/features/response-caching | 没有：全仓库无这些头 | 无 | 无 | 单独的特性，需先定数据保留政策（`docs/product.md:82` 内容保存由平台统一决定） |
| `response_format`：`json_object`、`json_schema`(strict) | 结构化输出，按端点支持 | docs/guides/features/structured-outputs | 有：`proto/grammar.go:76,132`、`proto/canon.go:31,280` 识别并作为能力要求，端点不支持则不参与路由 | 有 | 有 | 无 |
| 响应修复插件 `response-healing` | 非流式 JSON 修复 | docs/guides/features/plugins/response-healing | 已实现：非流式 Chat JSON 的本地语法修复（§10） | 无 | 无 | 见 §10 |
| 工具调用：`tools`、`tool_choice`、`parallel_tool_calls`，流式分块 | 标准函数调用 | docs/guides/features/tool-calling | 有：`proto/grammar.go:74,279-293` 能力匹配；Chat 转 Messages 映射 `tool_choice`/`parallel_tool_calls`（`proto/tomessageschoice.go`），表达不了的返回 400；旧版 `functions`、`function_call` 在同格式与转换时均升级成 `tools`、`tool_choice`，旧消息按稳定 ID 配对（`proto/legacy.go`、`proto/legacyrewrite.go`）；仅旧客户端的回复还原 `function_call` 并关闭并行（`proto/legacyreply.go`，与 OpenRouter 现代回复基线的差异见 [gateway 的决策](domains/gateway.md#决策与理由)） | 有 | 有 | 无 |
| `strict` 工具与 `structured-outputs-2025-11-13` 头 | 严格工具调用 | docs/guides/routing/provider-selection | 部分：`anthropic-beta` 头原样转发（`docs/domains/gateway.md:53`），所以头本身可用；`strict` 字段是否被剥离未核实 | 无 | 无 | 低优先 |
| `reasoning` 对象：`effort`(max/xhigh/high/medium/low/minimal/none)、`max_tokens`、`exclude`、`enabled` | 统一推理控制，隐藏不减用量 | [Reasoning Tokens](https://openrouter.ai/docs/guides/best-practices/reasoning-tokens)，2026-10-29 | 已实现 Chat ↔ Messages/Responses 的开关、展示、预算与 effort 换算及输出隐藏，见 [协议换算表](../internal/protocol/README.md#推理控制与换算) | 模型后缀及方言转换 | 部分 | Messages ↔ Responses 直接转换未实现；目录尚无模型级 supported_efforts/mandatory |
| `reasoning_effort`（Chat 的旧写法） | 与 `reasoning.effort` 等价 | spec `ChatRequest.reasoning_effort` | 有：`proto/grammar.go:75,344-350` | 有（`na:relay/helper/model_modifier.go:207-214`） | 无 | 无 |
| 响应里的推理文本：`message.reasoning`、`reasoning_details[]`，续接时回传 | 推理内容 | 同上 | 有：同时写 `reasoning` 与 `reasoning_content`，`reasoning_details` 保留（`proto/chat.go:13-14,68-69`，`proto/aggregatechat.go:99-101`） | 只有 `reasoning_content` | 部分 | Axiom 更全 |
| `reasoning` 在 `GET /models` 的 `reasoning{…}` 元数据 | 客户端画档位用 | 同上 | 没有（见 §2） | 无 | 无 | 同 §2 |
| 模型名里的推理后缀（new-api 的 `-high` 之类） | OpenRouter 没有 | — | 没有 | `na:relay/helper/reasoning_suffix.go:111` | 无 | 非 OpenRouter 能力 |
| `verbosity`、`prediction`、`logit_bias`、`logprobs`、`top_logprobs`、`min_p`、`top_a`、`repetition_penalty`、`seed`、`stop`、`top_k` 等采样参数，不支持的参数被忽略 | 参数透传，不认识的丢弃 | docs/api_reference/parameters | 部分：同格式原样透传给上游；Chat 转其他格式时，转换无法表达且不是默认值的成员返回 400 `NotSupported`（`proto/canon.go`，具体成员名单未逐个核实），默认值按解码后的值认（转义、空白的不同写法算默认），与 OpenRouter「忽略」不同 | 有 | 有 | Chat 客户端经转换时，OpenRouter 习惯带的采样参数可能被 400 |

## 9. 图片、PDF、音频、视频

| 能力 | OpenRouter 行为 | 来源 | Axiom 现状 | new-api | sub2api | 差距说明 |
|---|---|---|---|---|---|---|
| 图片输入：`image_url`（URL 或 base64 data URL；png/jpeg/webp/gif） | 多模态输入 | docs/guides/overview/multimodal/image-understanding | 有：`proto/grammar.go:62,80`，转换 `proto/tomessages.go:336`、`proto/toresponses.go:148` | 有 | 有 | 无 |
| PDF 输入：`file` 内容块（URL 或 base64），`file-parser` 插件选引擎(`mistral-ocr`、`cloudflare-ai`、`native`)，响应带 `file` 注解可复用跳过解析费用 | 文档解析 | docs/guides/overview/multimodal/pdfs | 部分：`file.file_data` 识别并要求支持文档的端点（`proto/grammar.go:81,128`，`proto/tomessages.go:338`）；`file-parser` 启用返回 400 not supported（无外部解析/OCR 服务），没有解析注解 | 有（按模型） | 有 | 不支持原生 PDF 的模型拿不到 OCR/转文本兜底 |
| 音频输入 `input_audio`（base64，wav/mp3/…） | 音频输入 | docs/guides/overview/multimodal/audio | 没有：语法表里无 `input_audio`（`proto/grammar.go:62-83`） | 部分 | 无 | 范围里只写图片和 PDF |
| 音频输出：`modalities:["text","audio"]` + `audio{voice,format}`，必须流式，`delta.audio` | 音频输出 | 同上 | 没有：`modalities` 非 `["text"]` 在转换里被拒（`proto/canon.go:379`） | 无 | 无 | 同上 |
| 视频输入 `video_url` | 视频输入 | docs/guides/overview/multimodal/videos | 没有 | 无 | 无 | 同上 |
| 图片生成：Chat 里 `modalities:["image","text"]` + `image_config`，或 `POST /api/v1/images`、`/images/models` | 生图 | docs/guides/overview/multimodal/image-generation | 没有（范围「以后」） | `na:router/relay-router.go:127` | `s2:internal/server/routes/gateway.go:253` | 归 #320 多单位计价后再做 |
| 视频生成 `/api/v1/videos` 提交/轮询/下载 | 异步视频任务 | docs/guides/overview/multimodal/video-generation | 没有 | `na:router/main.go:20`（`SetVideoRouter`） | `s2:internal/server/routes/gateway.go:270-281` | 同上 |
| TTS `POST /api/v1/audio/speech`、STT `POST /api/v1/audio/transcriptions` | 语音 | docs/guides/overview/multimodal/tts、stt | 没有 | `na:router/relay-router.go:140-146` | `s2:internal/server/routes/gateway.go:295-296` | 同上 |
| Files API `/api/v1/files`（上传、下载、删除，请求里按文件 ID 引用） | 工作区文件 | docs/guides/features/files-api | 没有 | 返回未实现（`na:router/relay-router.go:170-174`） | 无 | 对应 #312「文件引用」 |

## 10. 插件、服务端工具、消息转换

`plugins`、`transforms` 于 2026-10-29 按 OpenRouter 官方文档核对并实现本地子集；入口对未支持的 id、选项和变换返回 400，不静默忽略。执行/拒绝/忽略和偏离的唯一表见[接入域](domains/gateway.md#插件与提示词变换)。`route`、`usage` 的独立缺口仍未处理。

| 能力 | OpenRouter 行为 | 来源 | Axiom 现状 | new-api | sub2api | 差距说明 |
|---|---|---|---|---|---|---|
| 插件机制：`plugins:[{id, enabled, …}]`，账户默认插件设置，`enabled:false` 关掉默认插件 | 请求/响应改写 | docs/guides/features/plugins | 请求级 id/选项校验已实现，未知项 400；没有账户默认设置 | 无 | `s2:internal/config/config.go:108`（另一种概念，与此无关） | 本地支持修复与压缩，其余启用项显式拒绝；详见接入域 |
| `web` 插件（已弃用）：`engine`、`max_results`、`search_prompt`… | 联网搜索 | docs/guides/features/plugins/web-search | 启用返回 400 not supported，缺外部搜索服务 | 无 | 无 | 官方已转向服务端工具 |
| 服务端工具 `{"type":"openrouter:web_search"}`，参数 `engine`(auto/native/exa/firecrawl/parallel/perplexity)、`max_results`、`max_uses`、`allowed_domains`… | 任何模型都能联网搜索 | docs/guides/features/server-tools/web-search | 没有：不认识 `openrouter:` 前缀（`proto/grammar.go:46,66` 只认厂商的 `web_search_*`/`web_search`） | 无 | 无 | 厂商原生联网（Anthropic `web_search_*`、OpenAI `web_search`）有，并按次计费（`proto/errors.go:158`、`proto/grammar.go:286-310`） |
| Chat 的 `web_search_options` | OpenAI 风格联网 | docs/api_reference/responses/web-search | 有：`proto/grammar.go:74,356-361` 作为能力要求 | 部分 | 部分 | 无 |
| 服务端工具 `openrouter:web_fetch`、`datetime`、`image_generation`、`apply_patch`、`shell`、`bash`、`search_models`、`tool_search`、`fusion`、`advisor`、`subagent`，`GET /api/v1/tools` | OpenRouter 托管的工具 | docs/guides/features/server-tools | 没有 | 无 | 无 | 低优先；多数需要托管沙箱 |
| `response-healing` 插件 | 修复非流式 JSON | [官方文档](https://openrouter.ai/docs/guides/features/plugins/response-healing)，2026-10-29 | 已实现非流式 Chat JSON 本地修复，语法缺陷失败保留原文 | 无 | 无 | 不作 schema 语义校验，边界见接入域 |
| `context-compression` 插件 / `transforms:["middle-out"]` | 从中间压缩；关闭与小上下文默认值 | [官方文档](https://openrouter.ai/docs/guides/features/message-transforms)，2026-10-29 | 已实现 Chat 准入前按主模型规格压缩，保留首尾与工具配对；按字节上界保守估算 | 无 | 无 | 不实现 OpenRouter 跨模型上下文候选扩展；显式开启时保护内容装不下则 400，默认失败保留原文；见接入域 |
| `alignment`、`armor` 插件（β） | 规则检查/工具调用安全分类 | docs/guides/features/plugins/alignment、armor | 未支持 id 返回 400 | 无 | 无 | 不跟 |
| 护栏（Guardrails）：敏感信息、提示注入、密钥格式、预算、模型/供应商白名单 | 账户级策略，命中返回 403 | docs/guides/features/guardrails | 没有（账户有预算和限额，但没有请求内容护栏） | `na:common/str.go`（有敏感词处理，未深入） | 部分（`s2:internal/server/routes/prompt_audit_route_coverage_test.go`，仅见测试） | 对应 #315 的滥用防护，但形态不同 |

## 11. 响应里的用量与费用

| 能力 | OpenRouter 行为 | 来源 | Axiom 现状 | new-api | sub2api | 差距说明 |
|---|---|---|---|---|---|---|
| `usage.prompt_tokens`、`completion_tokens`、`total_tokens`，每次响应都带，流式在最后一块 | 用量统计，无需参数 | docs/cookbook/administration/usage-accounting | 有：`proto/openai.go:27-40,137`；流式最后一块带用量（`docs/domains/gateway.md:73`） | 有 | 有 | 无 |
| `usage.cost`（本次扣费） | 美元 | 同上 | 有：`proto/relay.go:586-596`；单位是站点记账货币，另有 `cost_currency`，估算时 `cost_estimated: true` | 无 | 无 | 二级站点用人民币时数字不是美元 |
| `usage.cost_details{upstream_inference_cost,upstream_inference_prompt_cost,upstream_inference_completions_cost,server_tool_cost}` | 上游成本与服务端工具成本 | 同上；spec `CostDetails` | 没有（只解析，不输出：`proto/openai.go:28`）；上游成本有意不对用户暴露 | 无 | 无 | `server_tool_cost` 对联网搜索计费可见性有用 |
| `usage.is_byok` | 是否用了自己的 key | 同上 | 没有 | 无 | 无 | 无 BYOK |
| `usage.completion_tokens_details.{reasoning_tokens,audio_tokens,accepted/rejected_prediction_tokens}` | 明细 | spec `ChatUsage` | 部分：只有 `reasoning_tokens`（`proto/openai.go:137`） | 部分 | 部分 | 缺音频/预测 token |
| `usage.prompt_tokens_details.{cached_tokens,cache_write_tokens,audio_tokens,video_tokens}` | 明细 | spec `ChatUsage` | 部分：上游报的 `cached_tokens`、`cache_write_tokens`、`audio_tokens`（`proto/openai.go:37`）、`video_tokens`（计入 `image_in`，`:39`）都解析并计费；返回给客户端的用量块（`appendOpenAIUsage`）只写缓存读写明细，不输出音频、视频明细；DeepSeek 的 `prompt_cache_hit_tokens` 按缓存读计（`proto/openai.go`） | 部分 | 部分 | 无 |
| `usage.server_tool_use_details` | 服务端工具调用次数 | spec `ChatUsage` | 部分：Messages 用量里有 `server_tool_use.web_search_requests`（`proto/errors.go:158`）；Chat 里没有 | 无 | 无 | 小 |
| 响应 `cache_discount`（缓存省了多少） | 缓存折扣 | docs/guides/best-practices/prompt-caching | 没有 | 无 | 无 | 小 |
| 响应 `model` 为实际使用的模型（回退/路由器后） | 返回实际模型 | docs/guides/routing/model-fallbacks | 不一致：Axiom 回显客户端请求的名字（`docs/domains/gateway.md:68`，`proto/chat.go:81` `ourModel`） | 回显请求名 | 回显 | 用了 `models` 回退的客户端分不清实际用了哪个；这是有意的（隐藏供应商），但对跨模型回退不利 |
| `stream_options.include_usage` | 开关 | spec `ChatStreamOptions` | 部分：总是返回用量（比 OpenRouter 多），参数被接受；发给 Chat 上游时强制为 true（`proto/prepare.go` 的 `streamOptions`），其他选项原样 | 有（强制，`na:relay/compatible_handler.go`） | 有（`s2:internal/service/openai_gateway_chat_completions_raw.go`） | 无 |
| 请求 `usage:{include:true}`（旧参数） | 旧开关 | spec | 部分：被丢弃，用量总是有（`proto/prepare.go:281`） | 无 | 无 | 行为兼容 |
| 零补全保险：零输出且无结束原因/错误结束时不收费 | 自动 | docs/guides/features/zero-completion-insurance | 有：`bill/settle.go:241` `zeroOutput` + 免单规则 | 无 | 无 | 无 |

## 12. 错误码与错误体

| 能力 | OpenRouter 行为 | 来源 | Axiom 现状 | new-api | sub2api | 差距说明 |
|---|---|---|---|---|---|---|
| 错误体外壳 `{"error":{"code":<数字>,"message","metadata"}}`，HTTP 状态码与 `error.code` 相同 | 数字 code | docs/api_reference/errors-and-debugging | 不一致（有意）：Chat/Responses 用 `{"error":{"message","type","param","code":"字符串"}}`，Messages 用 Anthropic 外壳（[协议错误渲染](../internal/protocol/errors.go) 的 `appendOpenAIError`；字符串 code 的取舍见 [gateway 决策与理由](domains/gateway.md#决策与理由)） | OpenAI 外壳 | OpenAI/Anthropic 外壳 | 读 OpenRouter 数字 `error.code` 的客户端要改读状态行或 `error_type` |
| 状态码语义 400/401/402/403/408/429/502/503 | 见文档表 | 同上 | 部分：有 400/401/402/403/404/408/413/429/502/503（[协议错误词表](../internal/protocol/errors.go) 的 `errorTypes`）；有 403 `permission_denied`（含账户暂停）和 `content_policy_violation`，本地模型权限仍隐藏为 404 | 有 | 有 | 内容护栏本身尚未实现；能识别上游内容拒绝 |
| `error_type` 类型化词表（约 30 个：`context_length_exceeded`、`max_tokens_exceeded`、`string_too_long`、`authentication`、`permission_denied`、`payment_required`、`rate_limit_exceeded`、`provider_overloaded`、`provider_unavailable`、`invalid_request`、`invalid_prompt`、`not_found`、`payload_too_large`、`unprocessable`、`content_policy_violation`、`refusal`、`invalid_image`、`image_too_large`、`image_not_found`、`image_download_failed`、`server`、`timeout`、`unmapped`…） | 稳定、可程序判断 | 同上（Typed error codes） | 部分：新增 `context_length_exceeded`、`permission_denied`、`content_policy_violation`，请求体超限改用 `payload_too_large`；HTTP 与 SSE、流式与聚合共用，原话脱敏后保留（`proto/errors.go`、`gw/refuse.go`） | 无 | 无 | 图片等其他细分类仍缺；字符串 code 是有意差异 |
| `error_type` 的位置：Chat `error.metadata.error_type`；Messages `error.error_type`；Responses 顶层 | 三种外壳各自位置 | 同上 | 有：位置与 OpenRouter 一致（`docs/domains/gateway.md:93-95`） | 无 | 无 | 无 |
| `error.metadata.provider_code`、`provider_name`、`raw`（上游原始错误） | 非 500 时给出上游错误码 | 同上 | 不一致（有意）：上游 4xx 的原话交给客户端但去掉能指向供应商的内容；上游 429/5xx 一律 502（`docs/domains/gateway.md:97-102`） | 原样 | 原样 | 有意，不算缺口 |
| 审核错误 `error.metadata{reasons, flagged_input, provider_name, model_slug}` | 403 | 同上 | 没有 | 无 | 无 | 随内容护栏做 |
| 预算错误：403，`metadata{limit_source: openrouter_guardrail_budget/workspace_budget/key_limit, scope, limit_usd, spend_usd, resets_at, remedy_hint}` | 预算耗尽 | 同上 | 部分：Axiom 的预算耗尽返回 402 并带 `limit_source`（`balance`/`workspace_budget`/`key_limit`，`gw/meter.go:112-130`）；没有 `scope/limit_usd/spend_usd/resets_at`（`docs/domains/gateway.md` 待定问题已记） | 无 | 无 | 状态码和字段都与 OpenRouter 不同 |
| 402 `in_flight_budget_exhausted` / `weight_exceeds_budget`，带 `Retry-After` | 在途预算 | docs/api_reference/limits | 部分：有在途预留，暂时性 402 带 `Retry-After`（`gw/meter.go:125-128`、`gw/refuse.go:58,83`）；原因词不同 | 无 | 无 | 小 |
| 流中途出错：HTTP 200 + 带 `error` 的 SSE 块 + `finish_reason:"error"` | 统一错误块 | 同上 | 有：Chat 流中途的出错块里的 `error` 与普通错误同一个对象（`docs/domains/gateway.md:93`）；`finish_reason:"error"` 也有（`proto/chat.go:268-271`） | 有 | 有 | 无 |
| `Retry-After` 头（429、503、部分 402） | 重试等待 | 同上 | 有：`gw/refuse.go:83`、`gw/forward.go:209`，上限 60 秒 | 无 | 无 | 无 |
| `debug:{echo_upstream_body:true}`（返回发给上游的请求体） | 调试 | 同上 | 没有（有意：不暴露上游） | 无 | 无 | 不跟 |
| 长度类错误转为成功（`finish_reason:length`）：Responses 里 `context_length_exceeded` 等 | 优雅降级 | 同上 | 没有（原话 400） | 无 | 无 | 小 |

## 13. 限流行为与响应头

| 能力 | OpenRouter 行为 | 来源 | Axiom 现状 | new-api | sub2api | 差距说明 |
|---|---|---|---|---|---|---|
| 平台限流只对免费模型（每分钟/每天请求数，按已购积分分档），付费模型无平台级请求上限；Cloudflare DDoS 防护 | 429 | docs/api_reference/limits | 不一致：Axiom 对每个 key 有请求速率（`KeyRate`）、账户并发、IP 并发、排队上限，全部是后台配置（`docs/domains/gateway.md` 限制表；`gw/admit.go:83`（`KeyRate`）） | 有（`na:router/api-router.go:81` `CriticalRateLimit` 等中间件） | 未核实 | Axiom 的限流更严；付费用户可能撞到 OpenRouter 上没有的 429 |
| 429 响应头 `X-RateLimit-Limit/Remaining/Reset` | 错误响应带限流头 | docs/api_reference/limits | 有：请求速率 429 返回三种头；容量 429 只给重试信息，见[接入](domains/gateway.md#业务速率与-openrouter-的差异) | 无 | 只解析上游的（`s2:internal/pkg/xai/quota.go:68`） | 容量/队列不伪造速率窗口头；成功不发 |
| 429 原因区分 | 平台 vs 上游（`metadata.provider_code`） | 同上 | 不一致：所有 429 都叫 `rate_limited`，原因只进指标（`docs/domains/gateway.md:129`） | 无 | 无 | 有意 |
| 429 时自动换供应商/备选模型 | 回退路由 | docs/guides/routing/model-fallbacks | 有：上游 429 只用来换凭证/换端点（`gw/attempt.go:280`） | 有 | 有 | 无 |

## 14. 预设（Presets）

| 能力 | OpenRouter 行为 | 来源 | Axiom 现状 | new-api | sub2api | 差距说明 |
|---|---|---|---|---|---|---|
| 请求里 `model: "@preset/slug"`、`model: "provider/model@preset/slug"`、请求体 `preset: "@preset/slug"` | 配置与代码分离，覆盖 model/provider/参数/系统提示/工具 | docs/guides/features/presets | 没有：代码里没有 `@preset` 解析，名字含 `:`/`@` 查不到；`docs/domains/gateway.md:150` 写了支持，实际没有（`gw/route.go:265-270` 只有零号预设） | 无 | 无 | 产品范围里预设属于第一版（`docs/product.md:93`），但网关侧没做 |
| `/api/v1/presets` 列表、详情、版本、`POST /presets/{slug}/chat/completions|messages|responses`（从请求体创建预设）、版本回滚 | 预设管理 API | api/presets/* | 没有 | 无 | 无 | 同上 |
| 预设里的 `tools`（含服务端工具）、`cache_enabled`、`cache_ttl_seconds`、系统提示 | 预设内容 | docs/guides/features/presets | 没有 | 无 | 无 | 同上 |

## 15. Key 管理（Management / provisioning keys）

| 能力 | OpenRouter 行为 | 来源 | Axiom 现状 | new-api | sub2api | 差距说明 |
|---|---|---|---|---|---|---|
| 管理 key（不能调模型，只能调管理接口） | 程序化管理 key | docs/guides/overview/auth/management-api-keys | 有：控制台签发、账户范围、禁止调用模型（`internal/accounts/management.go`） | 无 | 无 | #315 |
| `GET /api/v1/keys`（`include_disabled`、`include_expired`、`offset`、`workspace_id`） | 列 key，带 `hash`、`usage*`、`limit*`、`byok_usage*`、`expires_at`、`disabled` | api/api-keys/list-api-keys | 有：`cmd/axiom/management.go`，只列调用 key，支持四个筛选/分页参数 | `na:router/api-router.go:282-295`（令牌 CRUD，要登录） | `s2:internal/handler/api_key_handler.go` | hash 为不透明 UUID 十六进制，限额为本站额度；无 BYOK，返回零用量 |
| `POST /api/v1/keys`（`name`、`limit`、`limit_reset`、`include_byok_in_limit`、`expires_at`、`workspace_id`、`creator_user_id`，明文只返回一次） | 创建 key | api/api-keys/create-a-new-api-key | 有：`cmd/axiom/management.go`；创建人固定为管理 key 的签发人，限制继承且只能收紧 | 同上 | 同上 | 同上 |
| `GET/PATCH/DELETE /api/v1/keys/{hash}` | 查询/修改（含停用）/删除 | api/api-keys/get-a-single-api-key、update-an-api-key、delete-an-api-key | 有：`cmd/axiom/management.go`，data/deleted 外壳；PATCH 只改设置，不改到期或工作区 | 同上 | 同上 | 同上 |
| key 限额：`limit`(USD)、`limit_reset`(daily/weekly/monthly，UTC)、`expires_at` | 每 key 限额 | 同上 | 有：key 的限额、重置周期、过期（`gw/readonly.go:86-125`、`internal/accounts/keys.go`） | 有 | 有 | 周期的边界（UTC、周一起算）与 OpenRouter 是否一致未核实 |
| OAuth PKCE 换 key（`/auth/keys/code`、`/auth/keys`） | 第三方应用让用户授权拿 key | docs/guides/overview/auth/oauth | 没有（范围「以后」：第三方应用用户用自己账户付费，`docs/product.md:96`） | 无 | 无 | 归「新建」 |
| 工作区 `/workspaces`、成员、预算 `/workspaces/{id}/budgets`；护栏 `/guardrails`；组织 `/organization/*`；SCIM/SSO | 企业管理 API | docs/guides/features/workspaces、guardrails、sso | 没有（账户域有工作区，管理 API 未暴露） | 无 | 无 | #315 / #317 |
| 终端用户登记 `/end-users`（`user` 归因） | 按终端用户追踪 | api/end-users/* | 没有 | 无 | 无 | 低优先 |

## 16. 其他面向开发者的能力

| 能力 | OpenRouter 行为 | 来源 | Axiom 现状 | new-api | sub2api | 差距说明 |
|---|---|---|---|---|---|---|
| 请求体 `user`（终端用户标识，防滥用/归因） | 透传并用于归因 | spec `ChatRequest.user` | 部分：接受，哈希后再发上游（`proto/prepare.go:237,254`）；不用于对外归因 | 部分 | 部分 | 无 |
| App 归因头 `HTTP-Referer`、`X-OpenRouter-Title`(`X-Title`)、`X-OpenRouter-Categories`、`X-OpenRouter-App-Visibility` | 应用排名与分析 | docs/app-attribution（2026-10-29 核对） | 部分：前三个头不转发（`internal/gateway/headers.go` 的白名单），由 gateway 去控制字符、按列宽截断（`app_referer` 另去掉 userinfo、查询串和片段）后记入调用记录的 `app_referer`、`app_title`、`app_categories`，并出现在控制台的调用列表与详情；没有应用页、排名或可见性（`X-OpenRouter-App-Visibility` 不记）；`client_app` 仍只从 `User-Agent` 识别 Claude Code/Codex | `na:relay/channel/openai/adaptor.go:243`（向 OpenRouter 上游发 `HTTP-Referer`） | 无 | 应用级用量统计可以按这三个字段做，但没有公开排名 |
| 响应头 `X-Generation-Id` | 见 §1 | 见 §1 | 有 | — | — | — |
| `metadata`、`trace`（`trace_id`、`span_name`…）字段 | 链路与广播 | spec `ChatRequest.trace` | 部分：`metadata` 同格式透传；`trace` 不识别 | 无 | 无 | 低优先 |
| 广播 Broadcast（把 trace 发给 Langfuse、Datadog、S3 等 20 多个平台）与 `/observability/destinations` | 可观测性集成 | docs/guides/features/broadcast | 没有（对应 #316 通知渠道，形态不同） | 无 | 无 | 低优先 |
| 日志与输入输出记录开关 | 控制台功能 | docs/guides/features/logs、input-output-logging | 部分：内容保存由平台统一决定，站点和账户不能关（`docs/product.md:82`） | 有 | 有 | 与 OpenRouter 的「用户可选」不同，有意 |
| 运行时间/性能：模型端点的 `uptime_last_*`、`throughput_last_30m`、`latency_last_30m` | 在 `/models/.../endpoints` 里公开 | docs/guides/best-practices/uptime-optimization | 没有（内部有健康度数据用于路由，不对外） | 无 | 无 | 见 §2 |
| 零数据保留（ZDR）账户级/按模型组设置 | 隐私设置 | docs/guides/features/zdr | 部分：请求级 `provider.zdr`/`data_collection` 有；账户级设置没有核实 | 无 | 无 | #315 |
| 地域路由（`eu.openrouter.ai`、`us.openrouter.ai`） | 企业功能 | docs/guides/features/in-region-routing | 没有 | 无 | 无 | 不跟 |
| 批量 API `/api/v1/batches`（异步批量，`:batch` 价） | 异步批处理 | docs/batch-quickstart | 没有（范围「以后」） | 无 | `s2:internal/server/routes/gateway.go:258-267`（图片批量） | 低优先 |
| Responses 的 `store`、`previous_response_id`、`background` | OpenRouter 的 Responses 是无状态的 | docs/api_reference/responses/overview | 不一致（有意）：Axiom 自己保存会话状态（`docs/domains/gateway.md:236`） | 无 | 部分 | Axiom 比 OpenRouter 多 |
| 嵌入 `/api/v1/embeddings`、`/embeddings/models`、重排 `/rerank`、旧补全 `/completions`、审核 `/moderations` | 其他模态端点 | docs/api_reference/embeddings | 没有 | `na:router/relay-router.go:102,135,151,164` | `s2:internal/server/routes/gateway.go:240` | 向量化和重排对 RAG 开发者是硬需求；范围里放在「以后」 |
| Provider Monitor 数据集、`/benchmarks`、`/analytics/*`、`/datasets/*`、`/classifications/task`（公开排名数据） | 市场数据 | api/datasets、benchmarks | 没有 | 无 | 无 | 不跟 |
| 客户端 SDK（TypeScript、Python、Go）、Agent SDK、MCP、Terraform provider、Ori | OpenRouter 自己的工具链 | docs/client-sdks/overview | 不适用（Axiom 说用 OpenRouter/OpenAI/Anthropic 官方 SDK 改地址即可）；Axiom 另有运营 MCP（`docs/domains/operations.md`） | 无 | 无 | 不跟 |

## 缺的和不一致的（按「挡住从 OpenRouter 切过来的开发者」程度排序）

排序依据：静默改变行为（开发者看不出来）> 对 OpenRouter 客户端常见配置直接 400/404 > 模型元数据缺失（SDK 的模型选择器、成本估算坏掉）> 读取类接口缺失 > 管理类 API 缺失 > 产品线缺失。

1. 【插件已落实，独立参数仍有缺口】`plugins` / `transforms` 已执行本地修复/压缩或明确 400（§10），不再静默丢弃；`route`、`usage`、`auto-router` 尚无实现。→ #311
2. 【静默失效】`openrouter:web_search` 等服务端工具类型不被识别（`proto/grammar.go:46,66`），Responses/Chat 里带这类工具的请求行为未定；联网搜索只有厂商原生的。→ #311
3. 【硬失败，部分已补】模型名后缀：`:nitro`、`:floor` 已支持（排序一半，`gw/variant.go`）；`:exacto`、`:online` 现在回 400 并说明原因，`:free`、`:thinking` 等目录变体回 400 并说明本站无法表示独立条目；`~anthropic/claude-sonnet-latest` 这类别名回 400 暂不支持，需要显式模型族与可切换别名映射，不能猜最新版本；`@preset/…`（文档说支持、代码没有）仍查不到。→ #311（别名解析、`:nitro/:floor` 的层级准入）；`@preset/` 前缀随预设一起归 #313
4. 【明确偏离】`provider.order/only/ignore` 按公开分组代号生效，关闭回退、三种排序与对象分区已支持。`require_parameters:true`、非空 `quantizations`、`enforce_distillable_text`、百分位对象、`max_price.request/image` 缺必要事实或计费单位而明确 400；来源和理由见 `docs/domains/gateway.md` 的路由参数。→ #313
5. 【模型元数据】`GET /api/v1/models` 与 `/api/v1/models/{model}` 已是 OpenRouter 的模型对象：`pricing`（十个按 token / 按次计价的成员，取主站售价）、`context_length`、`top_provider`、`architecture`、`supported_parameters`、`reasoning`、`links.details`，外加查询参数过滤、排序和分页；`id` 是公开标准 ID，列表与路由快照同一次构建、一起发布，价格就是收费的价格。仍缺 `description`、`knowledge_cutoff`、`hugging_face_id`、`expiration_date`（目录没有这些数据，整个不输出）、`alias_target`、`benchmarks`、按图与按请求计价（#320）；`created` 恒 0、`default_parameters`/`supported_voices`/`per_request_limits` 是 null（都是 Axiom 没有的数据，填位置而不编造）。SDK 的模型选择器、编码代理的上下文窗口探测、成本估算已经可用。→ #311
6. 【预设】`@preset/…`、`preset` 字段、`/api/v1/presets` 全部没有，但产品范围把预设列在第一版（`docs/product.md:93`）；`docs/domains/gateway.md:150` 与代码不符，先改文档或做功能。→ #313（预设属于路由配置）
7. 【错误形态，部分已补】新增 `context_length_exceeded`、`permission_denied`、`content_policy_violation`、`payload_too_large` 与 403，HTTP/SSE 分类一致；`code` 保持字符串（含 403，有意差异，见 [gateway 决策](domains/gateway.md#决策与理由)）。图片类等细分仍缺；预算耗尽仍用 402 而不是 OpenRouter 的 403+`limit_source`。→ #311
8. 【Chat 上游】Chat 客户端调用说 Chat 的端点是同格式转发（#301 A，`proto/prepare.go` 的 Chat 规则、`rt/plan.go` 的 `callFormat`）；Messages、Responses 到 Chat 的请求、流及聚合转换（#301 B）与三种原生格式一同纳入差分测试，不能表达的成员按跨格式策略拒绝。→ #311（#301）
9. 【推理】`reasoning.max_tokens` 跨格式近似、`effort: xhigh/max/minimal`、关闭与隐藏已核实实现，映射及边界见 [protocol 换算表](../internal/protocol/README.md#推理控制与换算)。模型列表的 `reasoning` 仍只给 `supports_max_tokens`（按方言的手动预算判断）；未知 mandatory、模型支持的 effort 列表不声称。→ #311
10. 【读取类】`GET /api/v1/models/{author}/{slug}/endpoints`、`/models/user`、`/models/count`、`/providers` 已提供内存快照投影（字段偏离和实时准入的 DB 例外见 gateway.md），`/endpoints/zdr` 没有；`/generation` 缺 `native_tokens_*`、`cache_discount`、`session_id`、`streamed/cancelled` 等，`/generation/content`、`/generation/feedback`、`/activity` 没有。→ `/endpoints` 等归 #311；`/generation`、`/activity` 归 #319
11. 【用量字段】`usage.cost_details`（含 `server_tool_cost`）、`is_byok`、`completion_tokens_details` 的音频/预测项、返回给客户端的 `prompt_tokens_details` 音频/视频明细、`cache_discount`、Chat 里的 `server_tool_use_details` 没有；响应 `model` 回显请求名而不是实际使用的模型（用 `models` 回退时看不出换了谁）。→ #319
12. 【会话与归因】会话请求头（`x-session-id` 等）转发给 Responses、Chat 端点并换成分值（`gw/headers.go`），Axiom 自己的粘性还没按它分组；`X-OpenRouter-Title`/`HTTP-Referer`/`X-OpenRouter-Categories` 已记入调用记录并出现在控制台的调用列表与详情，但没有应用页和排名；Anthropic Messages 的 `fallbacks` 参数不生效。→ #313（session 头、fallbacks）
13. 【限流】E4-2/P13 已实现：共享账户/key 计数、等级默认与账户覆盖、请求速率 429 的 `X-RateLimit-*` 头、`GET /key` 的 `rate_limit`、`budgets`、`effective_budget`；付费模型平台限流等有意差异见[接入](domains/gateway.md#业务速率与-openrouter-的差异)。→ #315
14. 【管理 API】`/api/v1/keys` 系列已提供管理 key 认证的调用 key CRUD（E4-9/P14）；限制与 OpenRouter 的差异见[账户域](domains/accounts.md#api-key)；`/workspaces`、`/guardrails`、`/organization` 管理 API 没有。→ #315（key）、#317（工作区/护栏的界面）
15. 【响应缓存】`X-OpenRouter-Cache*` 请求/响应头和预设里的缓存配置没有；涉及内容保存政策，需要负责人定。→ 新建
16. 【BYOK】用户自带供应商 key（`/byok`、优先/回退区、手续费）没有；范围里在「以后」。管理员侧号池已有。→ 新建
17. 【路由器】`openrouter/auto`、`auto-beta`、`free`、`pareto-code`、`bodybuilder`、`fusion`、`switchyard`、`jev` 都没有；范围里 Auto Router 在「以后」。其中 `openrouter/free` 依赖 `:free` 条目。→ 新建
18. 【产品线】`/embeddings`、`/rerank`、`/images`、`/videos`、`/audio/speech`、`/audio/transcriptions`、音频/视频输入输出、Files API、Batch API、`/completions`、`/moderations`。向量化和重排是 RAG 开发者的硬需求，其余可后置；单次/按时长/按字符计价要先有 #320。→ 向量化/重排/批量/Files：#312 或新建；图片/视频/语音：#320
19. 【认证与企业】OAuth PKCE 换 key、Workload Identity、SSO/SCIM、私有端点、终端用户登记、Broadcast 可观测性集成、护栏内容策略：不影响基本迁移。→ #315 / #316 / 新建
20. 【不跟】`alignment`/`armor` 插件、Jev/Fusion/Switchyard 路由器、`debug.echo_upstream_body`、`openrouter_metadata` 路由元数据、地域路由、公开排名数据集：与 Axiom「不暴露供应商身份」或产品定位冲突，建议明确写成「有意不做」。→ 无

## Axiom 比 OpenRouter 多的（不算缺口，只是行为不一致，迁移时要说明）

- `POST /v1/messages/count_tokens`、`POST /v1/responses/compact`、Responses 会话状态（`store`、`previous_response_id`）、`GET /v1/dashboard/billing/*`（给 new-api 当上游）。
- `/key`、`/credits` 只接受调用 key，管理 key 均为 401。OpenRouter 的 /credits 要管理 key，/key 也接受管理 key；Axiom 本轮仅为 key CRUD 接入管理认证，余额和凭证自省接口未接入独立管理权限，决定见 [gateway](domains/gateway.md#决策与理由)。
- 响应总是带 `usage.cost`/`cost_currency`/`cost_estimated`，不需要 `usage.include`。
- 推理文本同时写 `reasoning` 与 `reasoning_content`。
- 上游错误去掉供应商标识；响应头走白名单；`model` 回显请求名。
