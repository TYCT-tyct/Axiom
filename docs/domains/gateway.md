# 接入与对外接口

客户端怎么调用 Axiom：路径、鉴权、转发规则、用量与费用、错误、路由参数、给其他中转站对接的接口。对应代码模块 `gateway`，一次调用在代码里的步骤、期限、并发和故障处理见[模块 README](../../internal/gateway/README.md)；格式之间的转换由 `protocol` 模块负责（见[总体架构的「协议转换」](../architecture.md#协议转换)）。对外的模型调用接口沿用 OpenAI 和 Anthropic 的规范，本文只写与它们不同或需要特别注意的地方，字段细节以接口契约为准。

## 要求

- Claude Code、Codex、OpenRouter 的客户端只改地址和 key 就能接入。
- 第一版支持三种生成格式：OpenAI Chat、OpenAI Responses、Anthropic Messages（见[产品需求的「范围」](../product.md#范围)）；P18 另加两种直接格式（Embeddings、Rerank）和 OpenAI 旧版补全（`/completions`），后者只走原生端点、不与 Chat 互转（见「决策与理由」）。
- 其他中转站可以把 Axiom 当上游，用它们习惯的方式查余额、判断错误。
- 行为默认对齐 OpenRouter。

## 设计

### 路径与格式

| 格式 | 路径 | 主要使用者 |
|---|---|---|
| OpenAI Chat | `POST /v1/chat/completions` | 大多数 SDK、Cursor、OpenRouter 客户端 |
| OpenAI Responses | `POST /v1/responses`、`POST /v1/responses/compact`；按 ID 删除随续接提供（见下文「Responses 的会话状态」） | Codex |
| Anthropic Messages | `POST /v1/messages`、`POST /v1/messages/count_tokens` | Claude Code |
| OpenAI Embeddings | `POST /v1/embeddings` | RAG 客户端、SDK |
| Rerank | `POST /v1/rerank` | RAG 检索与重排 |
| OpenAI 旧版补全 | `POST /v1/completions` | 旧版 SDK、按 `prompt` 调用的工具 |
| 模型列表 | `GET /v1/models`、`GET /v1/models/{model}` | 所有客户端 |

- WebSocket 的升级请求一律拒绝，回 400 `invalid_request`。

- **直接格式**：`/v1/embeddings`、`/v1/rerank` 与其余调用路径一样，同时以 `/v1/...` 和 `/api/v1/...` 提供。embeddings、rerank 是“直接格式”：上游回一份 JSON，不流式，网关读完整份、结算后原样改写回复（见「转发规则」）。两者都只走原生端点，不做格式转换；一个供应商模型记录必须把 `embeddings` 或 `rerank` 写进 `formats` 才会被路由到，端点路径分别是 `/v1/embeddings`、`/v1/rerank`。
- **旧版补全**：`/v1/completions` 也是生成格式，但和直接格式一样只走原生端点：`prompt`（字符串、字符串数组、token 数组或 token 数组的数组）与原样返回 `text` 的 choice 没有对应的 Chat 转换，请求里的 `n`、`best_of`、`echo`、`logprobs` 也不能在不改变语义的情况下改写成 `messages`。一个供应商模型记录必须把 `completions` 写进 `formats` 才会被路由到，端点路径是 `/v1/completions`；适配器推不出的格式由管理接口的映射声明（见[决策与理由](#决策与理由)），格式名和迁移见同一节。上游一律按流式调用（`stream:true`，并强制 `stream_options.include_usage:true`），非流式客户端由网关把每个 choice 的 `text` 和四个 `logprobs` 数组聚合后一次返回；`prompt` 数组是一次请求里的多份补全，输入按整批预留（含 `suffix`）、上下文按最长的一份检查，输出按 `n × prompt 数` 一次预留（见[计费](billing.md#预留与结算)）。`best_of > 1` 在准入前以 400 拒绝（OpenAI 不能流式返回它）；超过报价上限的 `max_tokens` 按报价上限发送；`max_tokens: 0` 是明确的零输出，结束的空 choice 就是它的回答，`echo` 回显的 prompt 不计入输出。
- **`/embeddings/models` 没有提供**：OpenRouter 另有这个列表，但本站在模型目录里还没有“输出模态”这一事实，嵌入模型没有输出价，默认也不会出现在 `/api/v1/models`，更没有单独列表。调用按请求里的 `model` 走，见「待定问题」。
- **兼容 OpenRouter 的路径**：以上路径同时以 `/api/v1/...` 的形式提供。`GET /api/v1/models` 与 `GET /api/v1/models/{model}` 返回 OpenRouter 的模型对象（价格取准入账户及当前等级下实际可服务、且 key 的路由预设留下的端点中最便宜的一个主站发布售价，带 `context_length`、`top_provider`、`architecture`、`supported_parameters`、`reasoning`，外壳是 `data`/`links.next`/`total_count`），并读 OpenRouter 文档里的查询参数；`/v1/models` 仍是 OpenAI 与 Anthropic 的合并形，两种客户端各读各的。逐项的差距见 [OpenRouter 能力对照表](../openrouter-parity.md)。
- **挂载**：`/v1/` 与 `/api/v1/` 只挂在节点的公开监听上，管理端监听不提供；它们在控制台的 API 和网站页面之前截走，不经过控制台那一层的响应压缩（网关自己写应答、自己决定何时 flush），也不会被控制台 API 的 404 回答。所以客户端只需要知道站点的 API 域名，认 key 的规则见下文「鉴权」。节点在路由快照可用之前不报告就绪（见[运维的「节点启动自检」](operations.md#部署与节点管理)）。
- **`GET /v1/models` 与 `GET /v1/models/{model}`**：
  - 返回一个同时满足 OpenAI 和 Anthropic 两种格式的列表；只列这把 key 能调用、且有端点可走的模型，权限用的是与调用相同的判断，不能用的模型、没有端点的模型和不存在的模型一样回 404 `model_not_found`（不是 403）；
  - Anthropic 模型同时列出厂商原生的名字。Claude Code 只认 ID 里含 `claude` 或 `anthropic` 的条目；
  - `GET /v1/models/{model}` 按调用路径的方式解析别名（`catalog.ResolvePublic`，与 sub2api 从列表项取单个模型的做法一致），返回列表里同一项的形状，`id` 是请求里的名字；
  - 列表看有没有端点，不看端点的实时状态：没有端点（没接供应商，或缺规格/售价而被路由排除）的模型不列，因为调用它必然失败；端点暂时满载、熔断、暂停或预算用尽不让模型少列，容量由调用自己排队或报错；
  - `/v1/models` 下的 `count`、`user`、`{author}/{slug}/endpoints` 不提供，回 404 `not_found`；`/api/v1` 对应路径见下文模型发现接口；
  - 响应带 `Cache-Control: no-store`：列表是这把 key 的，不能进共享缓存；
  - Claude Code 启动时用它发现模型，超时只有 3 秒，任何跳转都算失败，所以必须在原地址直接、快速地返回（Claude Code《Gateway compatibility guide》，2026-10）。
- **`GET /api/v1/models` 与 `GET /api/v1/models/{model}`**：
  - 模型对象是 OpenRouter 的形状：`id`、`canonical_slug` 都是这个模型的公开标准 ID（`author/slug`，即目录名去掉内部版本后缀，`acme/claude@20250929` 按 `acme/claude` 列出），`name` 是去掉作者的部分，`created` 恒为 0；`pricing` 的每个成员都是字符串，取准入账户及当前等级发布售价中实际可服务、且 key 的路由预设留下的端点里最便宜的一个（见[路由的「请求覆盖与组合」](routing.md#请求覆盖与组合)），标准服务等级、第一档（美元 / token，`web_search` 每次，`image`、`image_output` 每张），售价表没有的单位（价目表没有按张价时的 `image`、`image_output`，`request` 按次，`discount`、`overrides`）省略而不是写 0，`image_token`、`internal_reasoning` 只给能收图片、会推理的模型（价目表对这两项会用所在侧的原价兜底，纯文本模型不该出现）；`context_length` 与 `top_provider.*` 取规格，`architecture` 的模态取规格声明的能力，`supported_parameters`、`reasoning` 按能力与方言给出（未知 mandatory 不声称）；`default_parameters`、`per_request_limits`、`supported_voices` 是 null，`description`、`knowledge_cutoff`、`hugging_face_id`、`expiration_date`、`alias_target`、`benchmarks` 不输出（目录没有这些数据）。`id` 只用能解析回这个模型的公开名字：标准 ID 解析不到它（目录不该出现）就整个不列，因为那个名字既取不到详情也调不通。只列 key 能调用、路由快照里有公共端点、有售价的模型；私有端点不进入这份共享投影。
  - 列表读 `offset`、`limit`、`q`、`context`、`min_price`/`max_price`、`min_output_price`/`max_output_price`、`input_modalities`、`output_modalities`、`supported_parameters`、`model_authors`，`sort` 支持 `pricing-low-to-high`、`pricing-high-to-low`、`context-high-to-low`；OpenRouter 文档里有而 Axiom 没有数据的参数按名字回 400 并说明缺什么，文档之外的参数（缓存参数、`utm_*`）忽略。查询串本身解不开（`%ZZ`、用 `;` 分隔）也回 400：标准库会静默丢掉解不开的那一项，过滤条件就这样变成「整个列表」。`links.next` 是同一个查询把 `offset` 往后挪一格，只在这页之后还有模型时给出。
  - 单模型按调用路径的方式解析别名（`claude-live` 与 `anthropic/claude-live` 是同一个模型），返回 `{data: 模型对象}`，`id` 是标准 ID 而不是请求里的名字：OpenRouter 的形状里一个模型只有一个 `id`。
  - 列表与路由快照是同一次构建的产物，一起发布；每个受众的模型与端点条目预先编码，价格过滤、排序、计数及分页使用同一受众选择器，没有可服务版本的受众不列出该模型：同一次读目录、同一次读售价，所以列表上的价格就是快照里那个售价版本的收费价格，两者不会各读各的（`RouteDeps.Build` 同时产出 `*routing.Snapshot` 与列表，`Publish` 在同一发布锁内一起换；取列表和调用规划持该锁的读锁，锁内只读内存，不包住鉴权事务、编码或网络写入；构建失败或迟到则两者都不换）。请求路径上没有读价：列表和它服务的模型集合都在构建时定下来，一次响应里的 `total_count`、这一页和它之后的链接因此都属于同一次构建，写响应时路由怎么变都不会让它们互相矛盾。
  - 列表按模型编码一次并缓存，请求只做过滤、排序和一次缓冲区拷贝，缓冲区按这一页的大小申请而不是整个目录（`BenchmarkOpenRouterList`、`TestOpenRouterPageBuffer`，后者在 `alloctest` 子进程里测分配字节数）。
- **token 计数**：
  - 没有这个接口时，Claude Code 只能按字符数粗估上下文用量；
  - 计数一律用本地估算，不转发给端点：转发要占端点名额且要走路由，理由见「决策与理由」的 `count_tokens` 一行；
  - 计数免费，但有单独的频率限制：每个 key 的计数请求与调用各算各的（`CountRate`、`KeyRate`），互不占用。
- **鉴权**：`Authorization: Bearer` 和 `x-api-key` 两种写法都接受。key 只能在所属站点登记的 API 域名上使用（见[账户与租户的「API key」](accounts.md#api-key)）。只凭域名和请求头认 key，认出之前不读请求体，认不出立即返回 401 并关闭连接。
- **浏览器跨域**：调用接口默认不允许，因为把 key 放在网页里不安全；站点可以为指定的来源开放。
- **限制**：请求体大小、各项超时和并发上限是后台配置——这是 `product.md` 的「可配置」原则和[安全](../security.md)对请求体与在途请求阈值的要求，**当前尚未实现**（[#731](https://github.com/TYCT-tyct/Axiom/issues/731) 跟踪）：现在是代码里的默认值（`gateway.DefaultConfig()`），没有后台读写和热更新。下表是这些默认值；设计是保存时校验它们之间的关系，不满足就拒绝保存，启动时不满足就不启动（各项的关系和内部的期限见 [gateway 模块的「超时与限额」](../../internal/gateway/README.md#超时与限额)）。

| 用户遇到的限制 | 默认 | 到了之后 |
|---|---|---|
| 读完请求体 | 2 分钟 | 408 |
| 候选端点都满载时排队 | 20 秒 | 429 |
| 等上游的响应头 | 60 秒（现在全站一个值，按端点调还没有）；`/responses/compact` 10 分钟 | 换下一个端点 |
| 上游没有新数据（心跳、注释、重复快照不算） | 180 秒（现在全站一个值，按模型调还没有） | 开始转发前换端点，之后以 `timeout` 结束 |
| 准入后不再发起新的尝试 | 120 秒 | 返回最后一次尝试的错误 |
| 单次调用最长 | 60 分钟 | 取消上游、结算，以 `timeout` 结束 |
| 一个请求的尝试次数 | 3 次，共用上面的期限，换端点不重新计时 | 返回最后一次尝试的错误 |

### 模型发现接口：基线、投影与偏离

2026-10-29 核对 OpenRouter 官方公开 OpenAPI。下面每行的 URL 与日期覆盖该行列出的所有字段；契约回放在网关的 `testdata/openrouter-models/` 保存官方文档中的 200 响应样本及来源，不拿 OpenRouter 的价格当本站售价。

| 字段或行为 | 官方基线（核对日期均为 2026-10-29） | Axiom 数据与偏离 |
|---|---|---|
| 模型 `id`、`canonical_slug`、`name`、`created`、`description`、`hugging_face_id`、`knowledge_cutoff`、`expiration_date`、`alias_target`、`benchmarks`、`links.details` | [模型列表](https://openrouter.ai/docs/api/api-reference/models/list-all-models-and-their-properties) | 标准公开 ID、名字尾段、创建时间 0；没有描述、日期、别名目标和评分数据则省略；详情链接指向本 API 的 endpoints 路径 |
| `pricing.prompt`、`completion`、`input_cache_read`、`input_cache_write`、`input_cache_write_1h`、`internal_reasoning`、`image_token`、`audio`、`audio_output`、`web_search`，以及价目表有按张价时的 `image`、`image_output`；缺失的 `request`、`input_audio_cache`、`discount`、`overrides` | [模型列表 PublicPricing](https://openrouter.ai/docs/api/api-reference/models/list-all-models-and-their-properties) | 该账户及当前等级的主站已发布售价，标准服务等级、第一档，精确十进制字符串；token 项为美元/token，web_search 为美元/次，image、image_output 为美元/张。没有对应单位的成员省略；不是免费，也不拼参考价。图片、推理价只给声明了相应能力的模型（image_output 不需要图片能力，只有按张价才出现） |
| `context_length`、`top_provider.context_length`、`top_provider.max_completion_tokens`、`top_provider.is_moderated`、`per_request_limits` | [模型列表 TopProviderInfo](https://openrouter.ai/docs/api/api-reference/models/list-all-models-and-their-properties) | 目录上下文和输出上限；本站网关不审查内容，is_moderated 为 false，不承诺上游的审查政策；per_request_limits 为 null |
| `architecture.modality`、`input_modalities`、`output_modalities`、`tokenizer`、`instruct_type` | [模型列表 ModelArchitecture](https://openrouter.ai/docs/api/api-reference/models/list-all-models-and-their-properties) | 能力位投影 text/image/file 输入、text 输出；目录没有分词器和指令模板，后两项省略 |
| `supported_parameters`、`default_parameters`、`supported_voices`、`reasoning.mandatory`、`supported_efforts`、`default_effort`、`default_enabled`、`supports_max_tokens` | [模型列表 ModelReasoning / DefaultParameters](https://openrouter.ai/docs/api/api-reference/models/list-all-models-and-their-properties) | 支持参数按规格能力及方言推导，未声明的不声称；默认采样值和声音为 null。手动思考预算时给 supports_max_tokens:true；目录没有 mandatory、默认推理和 effort 列表，省略。mandatory 在官方 reasoning 对象中必填，这是有意的契约偏离，不能把未知写成 false |
| `offset`、`limit`、`category`、`supported_parameters`、`output_modalities`、`sort`、`use_rss`、`use_rss_chat_links`、`q`、`input_modalities`、`context`、`min_price`、`max_price`、`arch`、`model_authors`、`providers`、`distillable`、`zdr`、`region`、`min_output_price`、`max_output_price`、`min_age_days`、`max_age_days`、`min_intelligence_index`、`max_intelligence_index`、`min_coding_index`、`max_coding_index`、`min_agentic_index`、`max_agentic_index`、`min_tool_success_rate`、`max_tool_success_rate`；`data`、`links.next`、`total_count` | [模型列表查询](https://openrouter.ai/docs/api/api-reference/models/list-all-models-and-their-properties) | 支持和拒绝列表见上文；每个官方参数都由行为测试核对。价格过滤为美元/百万 token；无分页参数返回全量，只有 offset 时采用官方 limit 默认 500；未知非官方参数忽略 |
| `GET /api/v1/models/user` 的 `data`、`links.next`、`total_count` 与 `offset`、`limit`、`output_modalities`、`sort` | [用户模型](https://openrouter.ai/docs/api/api-reference/models/list-models-filtered-by-user-provider-preferences-privacy-settings-and-guardrails) | 复用 key 的模型权限与同一份列表；支持同样的模型过滤，下一页保留 user 路径；尚无账户供应商偏好、隐私设置和护栏层，不假装已过滤这些条件 |
| `GET /api/v1/models/count` 的 `data.count`、`output_modalities` | [模型计数](https://openrouter.ai/docs/api/api-reference/models/get-total-count-of-available-models) | 本 key 在同一次发布中可见的公共模型总数，output_modalities 与列表采用同一模态语义（默认 text，all 不限模态）；非法值、重复参数和解不开的查询串回 400，不应用分页与其他列表过滤 |
| `GET /api/v1/models/{author}/{slug}/endpoints` 的 `data.id`、`name`、`created`、`description`、`architecture`、`endpoints`；端点 `name`、`model_id`、`model_name`、`provider_name`、`tag`、`context_length`、`max_completion_tokens`、`max_prompt_tokens`、`pricing`、`supported_parameters`、`service_tier`、`quantization`、`uptime_last_5m`、`uptime_last_30m`、`uptime_last_1d`、`latency_last_30m`、`throughput_last_30m` | [模型端点](https://openrouter.ai/docs/api/api-reference/endpoints/list-all-endpoints-for-a-model) | 仅按路由构建留下的公共端点（Owner 为零）列项，站点与账户私有端点不进入这份 OpenRouter 共享发现投影；这是公开发现投影，不按 key 的路由预设过滤，页面标注“可能不适用于当前 key”，可用性以带 key 的模型列表为准；`tag` 是分组的持久代号，同一分组在所有模型上相同，也是 `provider.only`、`provider.ignore` 和平台预设供应商顺序的公开身份。代号在分组首次被选用时随机分配（`g` 加 8 位 base32），之后不变，端点集合变化不重新编号，也不从供应商名、地址或其他可反推的数据生成；不公开内部 UUID 或创建时间。当前接入的都是中转分组，所以 `provider_name` 为 Axiom，公开身份取 `tag` 的代号；官方直连 API 接入后，同一公开身份用其真实名称。上下文与输出上限取该构建的端点有效面：每个原生格式分别用实测替换目录值（更高或更低都一样），再取所有原生格式的下限，所以不会把只有一个格式能接受的窗口声称为全端点可用（见[端点评估的「实测的能力与上限」](quality.md#实测的能力与上限)）；参数取该端点在此构建的有效参数位，逐格式实测后取交集，而不是模型级清单；售价取这个端点自己的版本价（成本加成时端点间可不同，固定价和参考价倍率时相同）。标准服务等级为 null；量化为管理员记录的级别，没有记录为 null；独立提示词上限、窗口可用率和性能百分位仍为 null；描述省略，architecture 的 tokenizer/instruct_type 也省略（官方在此要求这三个成员） |
| 端点 `status`、`supports_implicit_caching`、`supports_tool_choice`、`native_tools`、`supports_diarization`、`supports_image_reference`、`supports_multiple_audio_references`、`supports_voice_cloning`、`perf_last_30m_by_workload` | [模型端点 PublicEndpoint](https://openrouter.ai/docs/api/api-reference/endpoints/list-all-endpoints-for-a-model) | 没有窗口统计、逐值测试、原生工具映射或相关能力数据，省略而非把未知写成 false 或空集合；前四项能力字段（supports_implicit_caching、supports_tool_choice、native_tools、supports_diarization）在官方为必填，这是有意的契约偏离。实时暂停、满载和熔断不改变发现列表 |
| `GET /api/v1/providers` 的 `data[].name`、`slug`、`privacy_policy_url`、`terms_of_service_url`、`status_page_url`、`headquarters`、`datacenters` | [供应商列表](https://openrouter.ai/docs/api/api-reference/providers/list-all-providers) | 有可见端点时给公开聚合供应商 Axiom/axiom；没有则空数组。不暴露货源、凭证、上游名字和成本；没有登记的政策链接和地域均为 null |

带调用 key 的 `GET /api/v1/models` 和 `/v1/models` 按该 key 的路由预设过滤硬条件，价格取预设下能走到的最便宜端点，预设不可用时回 503 `preset_unavailable`；不带 key 的公共列表不按预设过滤。端点列表是公开发现投影，不按 key 过滤。目录、售价、端点字段只从构建时发布的内存投影读取，没有逐请求目录、计费或供应商查询。**鉴权例外**：这些路径仍像既有模型列表一样要求调用 key，实时停用、过期、IP 和模型权限通过 `accounts.AdmitBatch`（单请求路径为 `Admit`）的数据库事务检查；不能用仅缓存身份的 AuthenticateKey 替代准入。因此不承诺“每请求零 DB”，承诺的是响应数据零 DB。完全零 DB 需要另定具有失效机制的权限快照，当前没有这份数据，不在本次虚构它。

**网站和控制台的模型与价格**不复制这份 OpenRouter 投影，而是从同一次构建的 `ModelList` 取 `Public(billing.Audience)`：只含已发布、有可用售价的公共模型，字段是目录 ID、名字、厂商、上下文、最大输出、是否流式、能力和 `catalog.ScheduleView`（币种、分档口径、每个服务等级/档位/计费项的实际生效单价，含空档恢复的基础价）。每个模型还逐个列出服务它的端点：分组代号（同一分组跨模型相同，也是 `provider.only`、`provider.ignore` 和平台预设供应商顺序的公开身份；官方直连接入后用真实名称）、上下文、最大输出、是否流式、能力、量化、是否验证，以及该受众在每个端点的 `ScheduleView`。端点的上下文、最大输出和能力取该端点所有原生格式的交集与下限，参数由发现接口逐端点发布；模型级能力仍是目录的模型能力，不把某一个端点或格式的限制当成整个模型的能力。模型级价格仍取最便宜端点，所以端点表里各自的价可能不同；这个受众的版本没有定价的端点不列。`GET /api/console/v1/models` 匿名，按站点默认等级取价；`GET /api/console/v1/models/account` 要登录，按账户当前等级取价。每条单价另带列表该显示的标记 `headline` 和显示用的 `display_amount`/`display_per`（token 项每百万，其他每 1 个），规则和理由见[目录的「决策与理由」](catalog.md#决策与理由)；旧节点不带时页面的列表价显示不可用、明细按每单位原样显示。两者都只读内存快照，与调用扣费同一次构建，所以页面的价就是下单的价；不返回供应商名称、上游、内部 UUID、成本或价格来源。旧节点没有 `endpoints` 字段时页面显示「端点暂不可用」，不把它当成没有端点，也不把缺失的价当成免费。

价格是否公开是站点的设置 `model_prices_public`（`accounts.site_settings`，NULL 表示未设置，代码默认公开；产品要求见 [`docs/product.md`](../product.md) 的官网行：「模型与价格页，无需登录即可查看，是否公开由站点配置、默认公开」）。关闭且所有存活节点都支持时，只有匿名的 `GET /api/console/v1/models` 省略每个模型和每个端点的 `pricing`，模型、端点、限制和能力照常列出；`GET /api/console/v1/models/account` 始终带账户自己的价，登录成员在控制台照常看到。这样选是因为匿名接口是同一份答案：它不认会话，带 cookie 的请求也按站点默认等级回答，所以答案可以对所有访客共享、可以缓存，不会因为某个访客登录过而泄露价格。公开页面看到非空的模型列表却没有任何价格时显示「价格仅对登录用户可见」，不是「价格暂不可用」；旧节点始终带价，因此这个判断不会把旧节点的回答误认成隐藏。OpenRouter 的公开模型接口不需要 key 就返回价格（<https://openrouter.ai/api/v1/models>，2026-11-02 核对 HTTP 200 且含 `pricing`），所以默认公开与基线一致，关闭是站点自己的选择。滚动发布：设置只在所有存活节点都认识它时才生效。新节点心跳带 `supports_model_prices_public`，并把声明绑定到本进程的 `share_session`；`platform.model_prices_public_readiness` 汇总所有存活节点，只有每个节点的声明都与存活会话一致才为真。只要还有一个旧节点，匿名投影继续带价格（新节点也这样回答，避免同一站点一半请求隐藏、一半泄露），管理接口拒绝把开关改成关闭并在 `model_prices_public` 字段上报错；旧节点退出后下一次心跳把就绪算回真，各节点在下一次快照刷新时按站点选择隐藏，不需要改配置。已存的 false 在旧节点重新加入时，下一次心跳和快照刷新后同样回退为公开；旧节点离开后再恢复。旧管理页省略字段时服务端保留原值；新管理页从新节点读取、PUT 却落到旧节点时，旧节点忽略字段并返回 200，页面核对 PUT 返回的设置，字段缺席就提示这一项没有保存，不显示保存成功；新管理页也只在节点的 `defaults` 带这个字段时才显示和发送它。

### 转发规则

依据 Claude Code 官方的网关兼容指南（Claude Code《Gateway compatibility guide》，2026-10）：

- **请求头按目标协议的白名单转发**（`internal/gateway/headers.go` 是唯一的一处表）：
  - 协议需要的头原样转发，例如 Anthropic 的 `anthropic-beta`、`anthropic-version`，Responses 端点的 `x-codex-turn-state`。`anthropic-beta` 不筛选具体的值，因为 Claude Code 每个版本发的值都不同，筛选会让长上下文、交替思考等功能悄悄失效或直接报 400；
  - 会话请求头（`session_id`、`session-id`、`conversation_id`、`X-Session-Affinity`、`X-Session-Id`、`X-OpenCode-Session`、`X-Conversation-ID`）发给 Responses 和 Chat 端点（两者都是 OpenAI 的方言，中转站按这些名字定账号），不发给 Messages 端点，值是下面那一条的派生值，中转站据此把两轮对话固定到同一个账号；客户端的 `Authorization`、`x-api-key`、`x-axiom-*`、`x-claude-code-*`、`HTTP-Referer`、`X-OpenRouter-Title`、`X-Title`、`X-OpenRouter-Categories`、`User-Agent` 一律不转发，其中应用归因的三个记入调用记录（[记录与告警的「写入时记全」](observability.md#写入时记全)）。
- **会话和用户标识换成派生值**：`prompt_cache_key`、Anthropic 的 `metadata.user_id` 和上面的会话请求头，替换成用账户密钥派生的不透明标识再转发（见[路由的「会话粘性」](routing.md#会话粘性)），这样上游的缓存仍然有效。`metadata.user_id` 写成 JSON 字符串，内容是 `{"device_id":"…","account_uuid":"","session_id":"…"}`：`device_id` 每个账户固定，`session_id` 与同值的会话请求头是同一个派生，中转站从中取出会话固定账号（格式见 `internal/protocol/README.md` 的「同格式改写」）；`account_uuid` 留空，中转站接受空值，我们也不把账户标识交给上游。空串或纯空白的 `metadata.user_id`（含 Chat `user` 转换）写为空串，不生成会话结构，让中转站按内容回退。`user`、`safety_identifier` 换成哈希值，不把终端用户标识原样发往上游。`session_id` 作为路由参数用过之后与其他路由参数一样删除；请求头里的那个不是路由参数，它是客户端自己的会话名。
- **上游一律流式调用**：生成请求都以流式调用上游，路由只保留能流式的端点；客户端要的是非流式也由网关聚合后一次返回，这样计费进度看得见（见[计费的「预留与结算」](billing.md#预留与结算)）。非流式的回答在返回之前没有"已转发内容"这一刻，所以中途失败仍可换端点，各次尝试的内容互不混入。`/responses/compact` 例外：上游不流式，回复整份读入，除顶层的 `usage` 在结算后换成 Axiom 的用量和费用、上游误带的顶层 `axiom` 被丢弃外，其余成员和成员自身的字节不变；请求只改必须改的成员，一次预留足，见下文「compact 的用量与费用」。
- **直接格式整份回复**：`/embeddings` 和 `/rerank` 的上游不流式，网关把整份回复读进内存（上限 16 MiB，超出按上游失败处理）并校验它恰好是一个 JSON 对象、有本格式的 `data` 或 `results`、对象后没有多余内容（前导空白允许）；读取时只保留 `data`/`results` 在原文里的位置，不复制整份数组，改写时才分配输出缓冲，并在分配前向节点内存预算记账；通过后结算，再把上游的 `data`/`results` 配上本站的 `id`（rerank）、`model`、`usage` 和 `cost` 写出。读取失败或回复不合格时，这一次尝试还没有向客户端提交任何内容，仍可换下一个端点。请求只改必须改的成员：`model` 换成端点的名字，`user` 换成派生值，路由参数（`provider`、`models`、`preset`、`session_id` 等）删除；这两种格式没有意义的 `prompt_cache_key`、`safety_identifier` 也删除，`encoding_format`、`dimensions`、`top_n`、`input`/`documents` 原样转发。`stream:true` 在 Inspect 阶段就以 400 拒绝，因为这两种格式没有流式回复。
- **直接格式的错误**：外壳与 Chat 相同（OpenAI 的 `error` 对象），状态码沿用统一表；错误体里的 `axiom.usage` 按格式给出 embeddings 的 `prompt_tokens`/`total_tokens` 或 rerank 的 `total_tokens`/`search_units`。
- **compact 的用量与费用**：`/responses/compact` 的回复在结算后把顶层 `usage` 换成 Axiom 的用量和费用：与其他格式同一套成员（`input_tokens`、`output_tokens`、明细、`total_tokens`、`is_byok`、`cost`、`cost_currency`），成本按结算的实收；结算写不进时只带用量和 `axiom.billing_pending: true`，不带费用。回复的其余成员和次序不变，成员自身的字节原样保留；上游若带了本站保留的顶层 `axiom`，丢弃后只写本站的结算状态，结算状态只出现一次。改写前先校验回复是恰好一个 JSON 对象（`usage` 可缺，缺时补在末尾），输出按成员、`usage` 键和本站 `axiom` 一次分配足够容量；校验与改写缓冲的记账都在结算之前完成，所以不合格的回复仍可换端点。
- **跨格式**：三种 Chat 形态的生成格式两两可以转换，端点同时说客户端格式和别的格式时优先原格式；直接格式和旧版补全不参与转换（P18）。Chat 客户端调用说 Chat 的端点（DeepSeek、Qwen 等 OpenAI 兼容的模型，同格式转发），也调用 Messages、Responses 端点（转换）；Messages、Responses 客户端能调用只说 Chat 的端点，也能调用说另一个原生格式的端点（Messages 与 Responses 互转）。转换都走 protocol 的同一套映射：原生两格式之间先把请求读成 Chat 形状、再按 Chat 到目标的映射写出（`convertNative`），流也一样（先把上游的流写成 Chat 的块，再写成客户端格式），所以两个方向只有一套映射。表达不了的成员拒绝，不靠删除约束凑请求。Chat 的文本、推理、工具参数增量转换成客户端的事件，非流式由这些事件聚合；Messages 的块必须顺序输出，交错的后续工具参数暂存到前块关闭；Responses 的完成事件包含完整输出快照。缓存、推理用量仍由读上游那一路的读取器计量，费用和错误按客户端格式写出。跨格式只是请求和流的转换不同，预留、尝试、结算与同格式相同。三处只在原生两格式之间出现的差别：Anthropic 的思考块（含签名和 `redacted_thinking`）只回放给 Messages，Responses 的 reasoning 项（摘要加密文）只回放给 Responses，别家模型的推理对当前模型没有意义；给 Messages 客户端的请求不要加密推理的 `include`，它收不回密文；`file_id` 只在 Chat 和 Responses 之间带回，到 Messages 返回 `*NotSupported`（Anthropic 的文件引用是本站不接的 beta）。
- **请求内容不改**：同格式转发时，缓存标记、块状的 system 内容保持原样，否则每一轮都按无缓存计费。Chat 请求里模型方言的成员（`reasoning`、`reasoning_effort`、`cache_control`、助手消息里回传的 `reasoning_content`、图片部件）也照客户端写的发给端点，由端点按它自己的方言处理；按模型规格把推理参数换成端点认的写法，现在没有，待定。
- **Chat 端点的请求改什么**：`model` 换成端点的名字，`user`、`safety_identifier`、`prompt_cache_key` 换成派生值，路由参数删除，一律要流，并且 `stream_options.include_usage` 一律为 `true`（客户端写了 `false` 或 `null` 也一样，`stream_options` 里的其他选项不动）：计量和非流式的聚合都读上游报告的用量，这和 new-api（`relay/compatible_handler.go` 的 `ForceStreamOption`）、sub2api（`openai_gateway_chat_completions_raw.go` 的 `ensureOpenAIChatStreamUsage`）一样。输出上限：客户端的 `max_tokens`、`max_completion_tokens` 两个成员都拿掉，把其中最小的正数（都没有就是报价用的上限）写进端点读的那一个成员：端点的上游模型名是 OpenAI 的 o1、o3、o4、GPT-5 或 GPT-6 的 Astra、Sol、Luna 时写 `max_completion_tokens`（它们拒绝 `max_tokens`），其余写 `max_tokens`（DeepSeek、Qwen 一类忽略 `max_completion_tokens`，留着它输出上限就没有了）；这是 new-api 的规则（`GetOpenAIChatCapabilities`）。`store` 等其余成员不动。
- **发出的输出上限就是预留用的上限**：请求没写输出上限（或写 `null`）时，发给上游的是该条目报价用的上限，也就是模型的默认值，上下文装不下时则是裁短后的值（Messages 与 Responses 的数值 0（含 `-0`）也算没写；Messages 补 `max_tokens`，Responses 补 `max_output_tokens`，Chat 写在端点读的那个成员里，见上）；请求写了就照它发。模型的上限未知时不发，由上游自己的默认值管。否则上游可以生成比预留依据更多的输出。
- **Chat 旧版函数与控制字段**：Chat 同格式改写与跨格式转换都先把 `functions` / `function_call` 升级成 `tools` / `tool_choice`，旧调用与同名结果按消息顺序稳定配对，生成的 ID 避开已有调用和结果。新旧工具定义合并，冲突的工具选择或同一消息中的新旧调用拒绝。只用了旧版表示（没有非空的现代工具声明或现代工具选择）的客户端收到 `function_call`（流式为 `delta.function_call`，非流式为 `message.function_call`）和 `finish_reason: "function_call"`；现代或混用新旧声明的客户端仍收到 `tool_calls`。旧版请求有工具声明时才强制 `parallel_tool_calls:false`，避免把多个调用压成一个；没有工具声明的历史请求省略该开关。上游仍返回并行调用时按上游协议错误处理。控制字段按解码值判断，转义的选择、字段名、块类型与普通写法等价，推理片段的数值索引 `0` 与 `-0` 归到同一片段；正文、签名和工具参数保留原字节（转换格式必需的字符串解包除外）。
- **跨格式时缓存标记按 OpenRouter 的做法转换**（OpenRouter《Prompt caching》「OpenAI」、OpenAI《Prompt caching》，2026-10-16）：发往 Messages 时原样带着；发往 Responses 时，规格写明支持显式断点的模型（OpenAI 从 GPT-5.6 起）上，用户、系统、开发者、助手和工具结果的文本块的 `cache_control` 写成 `prompt_cache_breakpoint`，TTL 丢掉；不支持断点（规格写明不支持，或没写）的模型上的标记，以及工具定义、图片和文件上的标记丢掉，由 OpenAI 自动缓存前缀。对费用的影响：OpenAI 的缓存写入从 GPT-5.6 起按输入价的 1.25 倍收，更早的模型没有写入加价；Responses 只有 30 分钟起的缓存期，Anthropic 的 1 小时 TTL 表达不了，所以发往 Responses 的请求不会产生 1 小时的写入价（Anthropic 是输入价的 2 倍）；丢掉的标记可能让命中率比在 Anthropic 上低。收费始终按上游报告的实际缓存读写，不按请求里的标记。
- **只认原上游的内容**：请求里带着只有原上游才认的内容时（Claude 推理块的签名、OpenAI 的加密推理内容），优先粘回产生它们的端点。同格式透传时这些内容原样转发、从不删改：删掉中间的思考块会让其后的块全部失效，而换了模型或账户时，Anthropic 自己丢弃读不了的思考块、请求照常成功（Anthropic《Preserved thinking》，2026-10）；OpenAI 的加密推理换了组织是否可用，要录样本确认，被拒时按原话返回。
- **格式特有的能力只走原格式的端点**，见[路由的「一个请求的路由步骤」](routing.md#一个请求的路由步骤)。
- **Claude Code 的提示信息**（会话 ID、请求类别、"上下文已压缩"标记）用于会话粘性和路由，见[路由](routing.md)。
- **事件流带 `X-Accel-Buffering: no`**：前面有 nginx 时，它据此不缓冲，事件才能及时到达客户端；new-api 的每个流都带这个头（new-api 源码 `relay/helper/common.go`，2026-10-18）。
- **推理开关与展示分开**：缺省保留模型默认，显式关闭在转换时写成 Messages 的 thinking disabled 或 Responses 的 effort none；`reasoning.exclude:true` / Messages 的 thinking display omitted 只隐藏输出，推理仍计入用量和费用。本站在计量之后过滤，适用于同格式、跨格式、流式和非流式；Chat 不返回两个推理明文字段，details 只保留置空 text 的签名条目及密文，摘要不返回；三种格式均保留续聊状态。只给展示参数时不补写 thinking 模式，纯隐藏 Chat 块不在提交前积压。预算与 effort 的映射、近似和模型能力边界见 [protocol 换算表](../../internal/protocol/README.md#推理控制与换算)。
- **Chat 回复的推理文本写两个字段，内容相同**：`reasoning`（OpenRouter 的名字）和 `reasoning_content`（new-api、DeepSeek 的名字，不少客户端读它），未要求隐藏时，流式的每一块、非流式的消息里都是；带签名和密文的 `reasoning_details` 照旧。客户端读哪个都行；代价是推理文本多发一份，只在模型推理时发生。依据见「决策与理由」。Chat 端点只写其中一个名字时（DeepSeek 写 `reasoning_content`，OpenRouter 写 `reasoning`），另一个由本站按同样的文本补上；两个都有文本的块不动。
- **响应头按白名单放行**：只返回内容类型、我们自己的调用编号、我们计算的限流和重试信息。上游的 `x-request-id`、`openai-organization`、`cf-ray`、`server` 等会暴露上游身份的头一律删除；唯一的例外是上游铸造的 `x-codex-turn-state`，它只在作答的那次尝试的上游设了它时回传，客户端自己发的值不回显——那是厂商给下一轮的凭据，换一个出来没有意义。
- **自己回显**：响应里的 `id` 用我们的调用编号，`model` 写实际服务的模型（备选 `models` 或别名解析之后），不是客户端请求的原名；供应商仍然不暴露。

### 请求准备的内存预算

`session.prepare` 先把请求交给 `protocol.PrepareBudgeted`，由它保证这次准备分配的每个字节都先记到节点的 `Memory` 上；记不到就返回 `ErrMemory`，网关回 503，不发起上游尝试。`Inspect` 读过的请求体在准入时已经计入预算，并一直留到调用结束（`Call.Done`）：解码后的树里，`jsontext.ReadValue` 返回的值是请求体的切片，不复制正文，所以请求体必须比树活得久。

并发准备先共享本地准备闸门。只有失败时另有准备持有额度，且这些额度退回后可覆盖这次已经出现的峰值和失败的申请，才全退首试额度，再独占重试一次；否则立即返回 503 `memory`，不排写闸。份额由 `budget.draw/give` 在短锁中维护；成功交回的输出仍计入节点预算，但已移出可回收的准备份额，读体、发送缓冲和中继也不属于这个份额。预算在上述持有量之外够一份准备的峰值时，不会因准备之间瓜分额度而全部失败。预算充足仍并行；剩余额度小于正文或为零时在入闸前立即拒绝。锁不跨外部 I/O，也不等发送释放输出额度，最终不足沿用 503 `memory`。

合格的失败请求可能在写闸上排队：除了在途读者，还会等待先取得写闸的重试和穿插放行的读者。gateway 的在途名额由 `Config.NodeInFlight` 限制（默认 5000），所以同一时刻等闸者至多为在途名额减一；锁不承诺 FIFO 或固定等待时长，持续新流量可延长等待。`PrepareBudgeted` 没有 context，断开和超时不能取消等闸，也不打断已经开始的本地准备；调用名额保留至收尾。保留现有接口与错误契约，接受仅在真实准备额度竞争时等待纯本地计算的限制，不把它描述成可取消或有排队期限。

数据流：

1. 同一格式：先按 `rewriteSize` 为输出缓冲申请容量，改写器去转义标识和 `previous_response_id` 时经同一个 budget 申请临时缓冲和字符串；结束后退回临时部分，只留输出缓冲的容量。
2. Chat → Messages/Responses：解码 Chat 请求体时，每次切片增长、映射项、字符串和指针之前申请；写出只经一个 `writer`，它每次增长前申请新数组，复制期间新旧数组同时在账上，申请失败后不再写。解码树在转换返回前退回。
3. Messages/Responses → Chat：读取经 `encoding/json` 构造映射和值，分配不经我们的缓冲，所以开始前按 `nativeRequestCost` 整段预留，结束时只留写出的 Chat 请求体；之后的同格式改写按第 1 步申请。
4. Messages 与 Responses 之间：先走第 3 步得到 Chat 请求体，再走第 2 步；Chat 请求体在目标格式写出后退回。
5. 联网搜索的注入原地读请求体，新请求体在生成前申请，旧的退回。
6. 没有预算时（`Prepare` 在节点外调用）走同一套代码，只是不申请；解码走 `jsonx.Unmarshal` 的 `decodeCanon` 快路径，`TestBudgetedDecodeMatchesTheFastPath` 核对两者是同一棵树。

不变量：

- 分配之前已经申请：申请量按运行时的分配粒度算（`rounded`）；每次准备申请失败时已经申请的部分全部退回；首试的内存拒绝仅在其他准备额度可覆盖已知峰值时独占重试一次，最终返回 `ErrMemory` 后不再分配。
- 成功返回时只有 `Upstream.Body` 的容量还在账上，网关在 `Send` 返回后按这个值退还；失败返回时为 0。
- 申请量：Chat 转出按实际，1 MiB 长文本约为请求体的 1.01 倍，base64 文件约 3 倍，2 万个小消息约 22 倍；Messages、Responses 转出仍是每字节 12、每个对象 3072、每个成员 768 的预留。

发布与失败：

- 没有存储或对外接口变化，新旧节点可以同时运行；同一请求在两种节点上写出的字节相同。旧节点并发准备时可能互相瓜分额度而全部 503，新节点仅在竞争方准备额度可覆盖已知峰值时独占重试一次；滚动发布期间客户端可能遇到两种拒绝频率，不需要迁移或回滚脚本。
- `ErrMemory` 只阻止**当前待发起的尝试**：它发生在这一次上游调用之前，所以这一次没有上游重试、没有供应商费用、没有迟到的回调。准入可能已经用 `ReserveMany` 写入了数据库资金预留；执行器仍会进入 `conclude`，已有尝试照常结算，预留按既有规则释放。若进程在准备期间退出，数据库预留不会随进程消失，由既有的过期预留清理机制处理。
- 进程内的内存预算与数据库资金预留是两件事：前者限制这次准备和发送的峰值，后者保证账户余额；`ErrMemory` 不写账本，也不把这次 503 当成一次上游调用。
- 钱：这条路径不写预留、账本或调用记录。被 503 拒绝的请求不计模型用量，也不产生供应商费用；成功的请求按原来的结算路径计费，内存预算不是资金。

测试：`TestPrepareAllocatesOnlyWhatItDrew` 对每一对源、目标格式和最坏形状，在子进程里测准备分配的总字节数不超过申请总量加 32 KiB，再把预算随机压到峰值以下，核对每次都是 `ErrMemory` 且拒绝前的分配不超过已申请的（竞态构建里只核对拒绝）；`TestPrepareLargeRequestReservesAboutItsBody` 核对 Chat 转出的大请求申请量不超过请求体的五倍；`TestPrepareConcurrentLargeRequests` 核对预算紧张时至少一份成功、足够时全部成功，失败方不占预算、成功方只留输出容量；`TestPrepareRetriesAfterCompetingPartialCharges` 固定部分额度占用与独占重试的交错；`TestPrepareBudgetedRefundsFailures` 核对中途失败全退和后续请求，`TestPrepareBudgetedRefusesWithoutWaiting` 核对零预算和正文超预算不等闸；`TestConcurrentLargeRequestsStayWithinTheBudget` 在子进程里核对并发准备的实际活堆峰值不超过预算加 368 KiB（每个请求的未计费额度、页取整和请求对象，加一次采样的 16 KiB），并以花不完的预算作对照，证明测量能看见超支；`TestMemoryErrorKeepsItsType` 核对旧版函数调用和 thinking 预算处耗尽预算仍是 `ErrMemory`；gateway 的 `TestMemoryIsAskedForBeforePrepareAllocates`、`TestMemoryHoldsARequestByItsCapacityWhileItIsSent`、`TestMemoryHoldsConcurrentRequestsToTheBudget` 核对预算的申请、持有和退还。

### 用量与费用

- **每次调用都返回用量和费用**：输入、输出、缓存读、缓存写、推理 token 和费用，与 OpenRouter 的用量统计对齐（OpenRouter《Usage Accounting》，2026-10）。Chat/Responses 的用量块另带 `prompt_tokens_details` 的音频/视频、`completion_tokens_details` 的音频/接受/拒绝预测 token、`server_tool_use_details` 和 `is_byok`（服务这个回答的端点是账户自带的供应商 key 时为 `true`，否则 `false`；见[账户](accounts.md#自带的供应商-keybyok)）；`usage.cost_details.server_tool_cost` 只写按次工具的用户售价，供应商成本不写。用量块里的 `cache_discount` 是缓存读相对输入价省下的用户售价，只在为正时出现。`web` 插件的一次搜索按 `web_search` 按次项收费，计入 `server_tool_use.web_search_requests` 和 `cost_details.server_tool_cost`；`file-parser` 在 `Config.FileParse` 打开时按页收费：`mistral-ocr` 每页记一次 `file_parse`，只进 `cost_details.server_tool_cost`，没有自己的计数字段（OpenRouter 的用量对象也没有解析次数）；`cloudflare-ai` 免费，`native` 按输入 token。开关关闭时保留旧规则，每个解析的文件按 `web_search` 计，并计入 `server_tool_use.web_search_requests` 和 `server_tool_cost`；上游自己的搜索次数另算；OpenAI 的 `file_search` 和代码解释器没有各自的计数字段，只计入 `server_tool_use_details.tool_calls_requested` / `tool_calls_executed` 的总数，费用同样进 `server_tool_cost`，次数由回复的调用项计数（见[计费的「用量来源与暂估」](billing.md#用量来源与暂估)）。
- **Embeddings 与 rerank 的用量**：embeddings 的 `prompt_tokens` 和 `total_tokens` 取上游报告的输入 token；rerank 的 `total_tokens` 取上游报告的输入 token，`search_units` 记作按次的 web search 单位，按现有的 `web_search` 售价计费，上游没报 `search_units` 时不写这个成员。因此 rerank 模型的规格 `items` 要写 `input` 和 `web_search`，售价表也要有 `web_search` 的按次价；缺了 `web_search` 的价，这个端点会按[路由的“价目表缺项”](routing.md#一个请求的路由步骤)排除，不会先接单再在结算时失败。两者都写 `usage.cost`、`cost_currency`，暂估时带 `cost_estimated:true`，并写 `is_byok`（含义同上）；不写 `prompt_tokens_details`，因为上游没有按模态拆分。
- **流式输出放在最后一个事件里**：Chat 格式的最后一块带一个空内容的选项，避免客户端崩溃（与 OpenRouter 一致）。费用在回答结束前结算好再发出，字段是 `usage.cost` 和 `usage.cost_currency`；用量来自估算的另带 `usage.cost_estimated: true`，标出这次收费建立在估算上；供应商的完整记录到了以后由暂估校正冲正并重结，响应里已发出的数字不追改（见[计费的「用量来源与暂估」](billing.md#用量来源与暂估)）。
- **Chat 端点报的用量**：按 OpenAI 的口径映射（`prompt_tokens` 含缓存的 token，`completion_tokens` 含推理的 token，缓存读写和图片的 token 只算一次，不在输入里再算一遍）。DeepSeek 把缓存命中写在 `prompt_cache_hit_tokens`（与 `prompt_cache_miss_tokens` 之和等于 `prompt_tokens`），`prompt_tokens_details.cached_tokens` 缺失或为 0、而命中数大于 0 时，命中数就是缓存读；两者都大于 0 时以 `cached_tokens` 为准，不比较是否相等。这和 new-api 一样（`relay/channel/openai/usage.go` 的 `applyUsagePostProcessing`）；两个名字都不当作额外的用量，否则会按未知 token 再收一遍。用量块可以在带内容的最后一块里（DeepSeek），也可以是没有选项的单独一块（OpenAI、Qwen）。
- **结算写不进时**：上游已经正常结束、内容完整的，照常结束，但结尾不带费用，改带 `axiom.billing_pending: true`，非流式同样返回 200；费用由后台继续结算，在调用记录里查。只有被截断的流才以 `billing_unavailable` 报错结束。
- **流开始前就出错也可能收费**（例如已执行的联网搜索，见[计费的「免单规则」](billing.md#免单规则)）：错误体的 `axiom` 对象里带用量和费用。
- **余额截断**：转发中追加预留失败时，按下文「流中途出错」的方式发错误事件结束回答，说明余额或某个 key 的额度不足、回答已截断（见[计费的「预留与结算」](billing.md#预留与结算)）。
- **费用的单位**：用本站的记账货币（见[计费的「钱的表示」](billing.md#钱的表示)），并附带币种字段。主站是美元，与 OpenRouter 客户端的预期一致。
- **各格式的放置位置**：各格式按自己的习惯放用量字段，费用字段附加在该格式允许扩展的位置。Responses 在回答结束之前的事件（`response.created`、`response.in_progress`）里 `response.usage` 是 `null`（OpenAI 如此，new-api 原样转发），我们也原样保留；结尾事件里才是用量对象。
- **流以失败结束时先读取并校验失败帧的用量**：计费层保留其中最终确定的项，缺失项按既有规则暂估，免单规则不变；口径只在[计费的「用量来源与暂估」](billing.md#用量来源与暂估)定义。
- **调用编号**：就是请求 ID，每个响应（含错误）都在 `X-Request-ID` 和 `X-Generation-Id` 两个响应头里带同一个值，响应体的 `id` 也是它（与 OpenRouter 一致）。凭它可以查询调用记录：每次尝试、路由过程、费用明细。查询只返回用户付的售价，不返回我们的成本或供应商信息；`generation` 可选返回冻结的 `price_version` 和 `charge_details`，逐项售价、精度及历史缺失语义见[计费的用户明细](billing.md#用户看得到什么)。调用记录写入前（约 200 ms 内）查询返回 404 `generation_not_found`，客户端稍后重试；账本只记金额、没有用量，所以不提供精简版，等调用记录的写入方（G10）定了再补。
- **客户端断开**：

| 什么时候断开 | 怎样 |
|---|---|
| 排队中 | 停止等待，什么都不收 |
| 已准入、还没调用上游 | 释放预留，什么都不收 |
| 已调用上游，端点传递取消 | 立即中断上游，按已产生的用量收费，缺的项估算并标暂估 |
| 已调用上游，端点不传递取消（不论是否已转发内容） | 继续读完上游但不再转发，按完整用量收费，是准数；读也超时就中断、按暂估收费 |

  端点是否传递取消，按[计费的「客户端断开」](billing.md#客户端断开)判断。客户端取消从不免单。客户端自行重试视为新的请求。

### 响应缓存

基线是 OpenRouter 的[响应缓存](https://openrouter.ai/docs/guides/features/response-caching)（2026-11-02 核对）：它按 key、模型、端点类型、流式与否和规范化后的请求体缓存**成功**回复，命中时重放、换新的 `id`、用量清零、免费，TTL 默认 300 秒、范围 1–86400 秒。Axiom 照这套语义，但有五处有意不同，理由在「决策与理由」：按**账户**而不是按 API key 隔离，命中再用当前 key 的模型和端点权限复核；命中仍先走正常准入与预留，结算时释放；不做并发相同请求的合并；只缓存 Chat、Responses、Messages 的生成回复，不缓存直连格式、compact 和带状态的请求；**平台未明确允许前，持久化默认由平台开关关闭**。

**开关与优先级。** 缓存默认关闭，只有平台允许且请求明确要求才开。`AXIOM_RESPONSE_CACHE_CONTENT` 是平台对「保存回复正文」的决定，默认 `off`；为 `off` 时网关根本不建缓存，缓存头被忽略，为 `on` 时才看下面的请求级开关。这遵守 `docs/product.md` 的「对话内容保存由平台统一决定」，在 #325 的数据保留和删除流程落地前不打开（受控导出已落地，但缓存仍只是短期副本）：

| 来源 | 值 | 结果 |
|---|---|---|
| 平台 `AXIOM_RESPONSE_CACHE_CONTENT` | `off`（默认） | 关闭，任何请求头和命名预设都不能打开 |
| 平台 `AXIOM_RESPONSE_CACHE_CONTENT` | `on` | 允许保存，以下开关决定本次调用 |
| 命名预设 `cache_enabled` | `false` | 关闭，请求头 `true` 也不能打开 |
| 请求头 `X-OpenRouter-Cache` | `false` | 关闭 |
| 命名预设 `cache_enabled` 缺省 | — | 请求头 `true` 才开；头缺省或不是 `true`/`false` 时关闭 |
| 命名预设 `cache_enabled` | `true` | 打开 |
| 请求头 `X-OpenRouter-Cache-TTL` | 1–86400 | 覆盖命名预设的 `cache_ttl_seconds`；只在缓存打开时有意义 |
| 命名预设 `cache_ttl_seconds` | 1–86400 | 请求头没有有效 TTL 时用它 |
| 两处都没有 | — | 300 秒 |

TTL 按 OpenRouter 的读法：以开头的十进制数字为准，小数截断，后面的字符忽略（`60abc` 读作 60，`1.5` 读作 1），小于 1 按 1、大于 86400 按 86400，开头不是数字则忽略这个头。命名预设里的 `cache_enabled` 必须是布尔值、`cache_ttl_seconds` 必须是 1–86400 的整数，保存在 `accounts.presets` 的既有校验里，且这两个成员不会并进发给上游的请求体。

**键与隔离。** 键是账户 ID、客户端格式（生成格式之间分别区分）、是否流式和规范化请求体的 SHA-256。规范化只删字符串外的空白（空格、制表、回车、换行），保留属性顺序、重复键、数字写法和字符串里的空白——与 OpenRouter 一致，属性顺序不同就是不同的键。账户 ID 进哈希且查询在账户的 RLS 范围内完成，所以同一账户的不同 key 共用，不同账户即使请求字节完全相同也不共用。同一账户的 key 权限可以不同，因此条目还保存写入时的服务模型与端点；命中前用**当前** key 的准入结果重新算出的路由复核，服务模型或端点不在当前路由里就按 MISS 处理，权限收窄后旧条目不会被重放。请求在准入前已按命名预设合并、压缩和解析文件，哈希的是最终发给上游的那份正文；带 `previous_response_id`、文件解析、`web` 插件、服务端搜索、服务端原生工具、服务端压缩或文件引用的请求不缓存，因为这些内容不在正文里或不适合跨请求复用。

**响应头。** 缓存开启且回复成功时：

| 头 | MISS | HIT |
|---|---|---|
| `X-OpenRouter-Cache-Status` | `MISS` | `HIT` |
| `X-OpenRouter-Cache-TTL` | 本次请求的 TTL | 条目剩余秒数 |
| `X-OpenRouter-Cache-Age` | 不出现 | 条目已存秒数 |
| `X-OpenRouter-Cache-Source-Id` | 不出现 | 写入该条目的原调用 ID |

`X-OpenRouter-Cache-Clear: true` 在缓存打开时先删掉这个账户、这个键的条目，再按 MISS 处理；删除失败只记日志，不失败调用，本次成功回复会覆盖它。缓存头不转发给上游，也不出现在错误响应上。

**命中。** 命中在准入之后、第一次上游尝试之前判断。缓存里存的是上游的原始回复字节（SSE 或整份 JSON）和实际服务的模型；命中时用**当前**客户端格式重新走一遍 `protocol.Relay`，所以格式转换、流式/非流式、推理隐藏和结束事件都与正常回复同一条路径，但调用 ID、`created` 和响应头 `X-Generation-Id` 是新的，用量和费用清零，`X-Codex-Turn-State` 用条目里保存的值。结算没有尝试，预留全额释放，账本不产生扣费；调用记录写 `cache_hit=true`、价格和成本为 0。命中不占供应商的速率和凭证槽。并发相同请求不合并：两个都 MISS、都调用上游、都各自计费，后写入的覆盖先写入的。

**未命中与写入。** 只有上游 `200` 且回复完整、结算成功的调用才写入，错误、限流、半截流、客户端断开、结算失败的都不写。写入在回复交付之后异步进行，有界队列满了就丢弃并计数；写缓存失败只记日志，永远不失败或拖慢调用。条目在写入时按本次请求的 TTL 定过期时间，命中不会延长它。每个条目最多 1 MiB（上游原始回复字节），每个账户最多 64 MiB 或 1024 条，超出时按创建时间删最旧的；过期的条目由每分钟一次的清理删除，查询到过期条目也当未命中。

**加密与保留。** 正文用信封加密保存（`secret.Keyring`，AAD 绑定 `gateway.response_cache` 的表、行和 `body` 用途），表按账户 RLS 隔离；数据库里没有明文正文。TTL 上限 24 小时，到期删除，`X-OpenRouter-Cache-Clear` 和未来的账户数据删除流程可以按账户删。现在没有按账户的数据保留设置：`docs/product.md` 规定对话正文保存由平台统一决定，`docs/security.md` 的删除流程还没有实现；因此平台开关默认关闭，只有平台明确允许才写入，写入时也只保留 TTL 这么短，正文不进入调用记录或内容接口。受控导出也覆盖缓存：它是 ≤24h 的回复副本、没有请求 ID，所以单独写成 `cache.jsonl` 和 `cache/<cache_id>`，仍受批准的时间窗和租户范围约束（见[记录的「受控导出」](observability.md#受控导出)）。导出在同一只读快照里读取缓存元数据和密封正文，正文在快照内打开并用本次导出的临时密钥加密到 0600 暂存文件，之后正常的过期清理、容量淘汰或同键替换不会造成导出失败或混入替换后的回复。账户数据删除流程实现时必须把 `gateway.response_cache` 纳入删除范围。

**不缓存的路径。** 直连格式（Embeddings、Rerank、Completions）、`/responses/compact`、带 `previous_response_id` 的 Responses、`web`/`file-parser` 插件、`openrouter:web_search` 循环、服务端压缩和文件引用都不缓存。Embeddings 的整份 JSON 重放要另写一条不经 relay 的路径，等有需求时再加；在此之前，这些请求即使带缓存头也没有缓存响应头。

### 错误

- **外壳与类型**：Messages 用 Anthropic 的外壳；Chat、Responses、Embeddings、Rerank 用 new-api 与 OpenAI 的 `error` 对象：`message`、`type`、`param`、`code`，另带统一的错误类型字段 `error_type`，程序判断它就够了（与 OpenRouter 一致，见 OpenRouter《Errors and Debugging》，2026-10）。
  - `type` 用状态码表里「Messages 的 `type`」那一列的词（`invalid_request_error`、`authentication_error` 等），`param` 没有时是空字符串，`code` 是与状态行相同的数字，和 OpenRouter 一样（OpenRouter《Errors and Debugging》，2026-11-02 核对）；我们自己的错误里这个数字就是状态表的状态，精确分类在 `error_type`；上游拒绝请求时，上游给的字符串 `code` 放到 `metadata.provider_code`（没给时省略），不改变数字 `code`；
  - `error_type` 的位置：Chat 在 `error.metadata.error_type`，Messages 在 `error.error_type`，Responses 在 `response.failed` 的顶层；Responses 在流开始之前出错时用 Chat 的外壳；Chat 流中途的出错块里的 `error` 也是同一个对象；
  - 账户暂停调用是 403，`code` 为 403，Messages 的 `type` 是 `permission_error`，`error_type` 是 `permission_denied`；
  - Chat 的流中途出错块与流开始前的错误体用同一个数字 `code`：`Error.Status` 让 403、402 这类状态压过类型表里的默认状态，状态行与体内 `code` 始终一致；Responses 的原生 `response.failed`/`error` 事件沿用它的字符串 `code`（上游拒绝时是上游的 code），Messages 用自己的 `type`，两者都不带状态行。
- **上游拒绝请求本身（4xx）时，客户端读到上游的原话**：Claude Code 靠上游错误的原话自动恢复，例如推理签名被拒、`output_config.effort` 被拒，改写会让它们变成无法恢复的 400；new-api 也把上游的 `message`、`type`、`param`、`code` 原样交给客户端。我们交出上游错误对象里的 `message`、`type`、`param`，上游的字符串 `code` 放进 `metadata.provider_code`（数字 `code` 保持状态行，见上；Responses 原生流里按它自己的外壳留在 `response.error.code`），去掉能指向供应商的内容后：
  - **去掉的**：其中所有的网址（含文档链接）、本站调用它用的凭证（先整串遮蔽，再处理其他模式，免得被切成碎片）、形似凭证的串（`sk-`、`ak-` 开头，`Bearer`，`api_key=`）、供应商自己的域名及它的每一级上级域名（两段以上，如 `api.us.relay.example` 连同 `us.relay.example`、`relay.example`；不引入公共后缀表，多遮蔽 `co.uk` 这类后缀只是多遮蔽，不会漏）、new-api 附在文字后面的 `(request id: …)`、new-api、one-api、sub2api 这些软件的名字；每项最多 2 KiB；流内错误超过此限的字段整项省略，避免先截断凭证导致后续脱敏漏掉前缀；
  - **`type` 只留通用的**：OpenAI、Anthropic 自己用的取值（`invalid_request_error`、`authentication_error`、`not_found_error` 等）；中转站软件自己的（new-api 的 `new_api_error`、sub2api 的 `scheduling_error`）不传，Chat、Responses 外壳改用统一的 `type`。名字里带软件名的 `code` 也不传；
  - **没有原话时**：上游的响应不是这种错误对象、或没有 `message`，用统一的说法；
  - **只用于这一次回复**：原话只进给客户端的响应，不进日志，也不进任何表（suppliers 的 `Failure` 只在这一处带它，`Cause` 仍不带任何文字）；
  - **依据**：new-api 的 `ToOpenAIError` 带上游的四项并遮蔽上游的网址和 API key（new-api `relaykit/types/error.go`、`relaykit/relayconvert/kitutil/mask.go`，2026-10-18）；OpenRouter 在非 500 的错误里带供应商的话，500 的换成通用说法，供应商的原始细节在 `metadata`（OpenRouter《Errors and Debugging》，2026-10-19）。我们比 new-api 多去掉所有网址，因为供应商的充值页、文档页会暴露货源。
  - 其余的错误（含上游的 429 和 5xx）仍然是统一的说法。上游在流里用错误事件报的也一样：按它的类别算这个请求的还是供应商的，只有拒绝请求本身的（下文）才带上游的原话、`type`、`param` 和 `metadata.provider_code`（Responses 原生流是 `response.error.code`），别的类别都换成统一的说法。
- **我们自己的错误和上游的 5xx**：使用统一的说法，不出现中转站的名字或原始报错。
- **请求体不是合法 JSON**：400，说 "Invalid request: invalid JSON request body"（new-api 的同一句）；解析库的原话（偏移量、Go 类型名）不返回。类型不对的值用它的 JSON 指针指出位置。
- **状态码**：以下类型与外壳以 [OpenRouter Errors and Debugging](https://openrouter.ai/docs/api_reference/errors-and-debugging)（2026-11-02 核对）为基线。OpenRouter 的上下文错误为 400、权限与内容策略拒绝为 403、请求体超限为 413、预算不够为 403、余额不够为 402；本地模型权限继续隐藏为 404。只细分已被供应商或流读取器判定为请求自身的失败，不把供应商的凭证 403 当作用户权限错误。

| 失败 | 流开始前 | Messages 的 `type` | 流开始后（状态码仍是 200） |
|---|---|---|---|
| key 无效、过期、停用，IP 或域名不允许 | 401 `authentication` | `authentication_error` | — |
| 账户暂停调用，其余 key 条件有效 | 403 `permission_denied`，说明账户暂停调用 | `permission_error` | — |
| 余额（账户或站点）不够 | 402 `credits_insufficient`，`limit_source` 为 `balance`；会重置的带 `Retry-After` | `billing_error` | 追加预留失败，截断，类型同左 |
| 工作区预算或 key 上限不够 | 403 `credits_insufficient`，`limit_source` 为 `openrouter_workspace_budget` 或 `openrouter_key_limit`（OpenRouter 的取值）；会重置的带 `Retry-After` | `billing_error` | 同上 |
| 套餐不够（`subscription_only`，没有套餐也算；或 `wallet_first` 先试过钱包再试套餐） | 402 `credits_insufficient`，`limit_source` 为 `subscription`（本站的名字，OpenRouter 没有套餐）；不带 `Retry-After` | `billing_error` | 追加预留失败，截断，`limit_source` 为 `balance`（窗口不够按余额不足处理，见[计费的「订阅套餐」](billing.md#订阅套餐)） |
| 进行中的请求暂时占满了余额 | 402 同余额，`limit_source` 现在也是 `balance`；`Retry-After` 是最早释放的时间（待定：见「待定问题」） | `billing_error` | 同上 |
| 追加预留时数据库出错 | 503 `billing_unavailable`（转发开始前就用完了第一段） | `api_error` | `billing_unavailable`，不带费用 |
| 结算写不进，内容完整 | 200，带 `axiom.billing_pending: true`，不带费用 | 同左 | 同左 |
| 限流（每个 key 的节点保护、账户聚合业务速率与 key 业务速率，调用和计数各一池）；按 IP、按账户的在途上限；同一账户的资金操作排队超过 5 秒（一次突发在一个账户上排的队，见 [gateway 模块的「并发、背压与共享状态」](../../internal/gateway/README.md#并发背压与共享状态)） | 429 `rate_limited` | `rate_limit_error` | — |
| 端点都满载，排队的人数满了或排队超时；上游凭证被并发的请求占满 | 429 `rate_limited`，`Retry-After` 是估计的等待时间，凭证占满时为 1 | `rate_limit_error` | — |
| 节点满载、准入超时、路由快照过旧、数据库不可用、本节点的持有者失效 | 503 `overloaded`，`Retry-After: 1` | `overloaded_error` | — |
| 模型不存在；key 不允许用这个模型 | 404 `model_not_found`，不返回 403 | `not_found_error` | — |
| 没有可用端点，永久性的：能力、上下文、格式、路由预设或请求覆盖过滤掉了全部端点，或没有售价 | 404 `no_endpoint`，附过滤原因 | `not_found_error` | — |
| 没有可用端点，暂时性的：全部离线（第一次重新检测仍失败）、供应商余额不足、重试预算用完，或 `:exacto` 没有已验证的端点 | 503 `no_endpoint`，附原因和 `Retry-After` | `overloaded_error` | — |
| 路由预设暂不可用：快照不认识 key 的 `preset_id`（滚动发布/回滚、刚创建还没重建、本节点读不懂它的规则、数据损坏），或它不属于这把 key 的站点、账户、工作区或币种 | 503 `preset_unavailable`，`Retry-After: 5`；不按放行预设继续 | `overloaded_error` | — |
| 每个端点都不支持这个请求的写法 | 400 `invalid_request`，附原因 | `invalid_request_error` | — |
| 开启了联网搜索或代码执行，却没写最多用几次，而模型也没有默认次数（费用没有上界，预留不了） | 400 `invalid_request`，说明要写 `max_uses`（Messages 联网搜索）或 `max_tool_calls`（Responses）；没有上限字段的（Chat 的工具、Messages 的代码执行）说明该格式暂不支持，不调用上游 | `invalid_request_error` | — |
| 上游拒绝上下文过长（HTTP 或流内错误） | 400 `context_length_exceeded`，保留脱敏原话、`param` 和上游的 code（Chat 在 `metadata.provider_code`，Responses 原生流在 `response.error.code`）；不重试（OpenAI 直连的 HTTP 答复判为端点的上限，先换端点，见[路由的「失败怎么办」](routing.md#失败怎么办)） | `invalid_request_error` | 同一 `error_type`，保留已转发内容 |
| 上游内容策略拒绝请求（HTTP 或流内错误） | 403 `content_policy_violation`，保留脱敏原话；不重试 | `permission_error` | 同一 `error_type`，保留已转发内容 |
| 请求权限拒绝；上游已明确分类的请求体超限 | 403 `permission_denied`；413 `payload_too_large` | `permission_error`；`request_too_large` | 同一 `error_type`；供应商凭证权限失败仍只用来换凭证或端点 |
| 上游因其他请求问题拒绝（HTTP 或流内错误） | 400 `invalid_request`，带上游的原话、`type`、`param` 和上游的 code（上文），没有原话时用统一的说法；不再换端点 | `invalid_request_error` | 错误事件里保留原话和上游的 code，`error_type` 是 `invalid_request`，已转发的内容保留 |
| `web` 插件的搜索失败、超时或结果无法注入（在第一次尝试之前） | 502 `upstream_error` | `api_error` | 上游中途出错，`upstream_error` |
| `file-parser` 的 OCR 后端失败、超时、转换失败或没有文本（在第一次尝试之前） | 502 `upstream_error` | `api_error` | 同上 |
| `file-parser` 的文件超过 20 MiB、超过 8 个，或解析出的文本合计超过 2 MiB | 413 `payload_too_large` | `request_too_large` | — |
| 所有尝试都失败，含各自超时 | 502 `upstream_error` | `api_error` | 上游中途出错，`upstream_error` |
| 单次调用最长、不再发起尝试的期限到了，节点退出；读请求体超时 | 408 `timeout` | `timeout_error` | `timeout` |
| 格式错误、带 `previous_response_id`（M1）；请求过大 | 400 `invalid_request`；413 `payload_too_large` | `invalid_request_error`；`request_too_large` | — |

- **没有路由接的路径和方法**：也按上面的外壳作答——`/v1/messages`（含 `/api/v1/messages`）下用 Messages 的外壳，其余用 OpenAI（Chat）的外壳；路径不存在回 404 `not_found`，方法不对回 405 `method_not_allowed` 并保留 `Allow` 响应头，请求 ID 两个头照常带。new-api 对未知路径也回 404 的 OpenAI 外壳（new-api `controller/relay.go` 的 `RelayNotFound`，2026-10-19）。
- **`Retry-After`** 用整数秒，不超过 60 秒。Claude Code 对超过 60 秒的值不再重试。
- **429 的原因只进指标**：上表所有 429 的错误码都是 `rate_limited`，客户端程序不必区分；节点按原因（`ip_in_flight`、`account_in_flight`、`key_rate`、`count_rate`、`account_rate`、`business_key_rate`、`account_gate`、`queue_full`、`queue_key_full`、`queue_wait`、`credentials_busy`）计入指标 `axiom.gateway.rate_limited`，运维凭它判断是哪一道限制在拒绝（逐项含义见 [gateway 模块](../../internal/gateway/README.md#并发背压与共享状态)）。原因不写进响应：OpenRouter 的 429 也只给统一的 `error_type`（`rate_limit_exceeded`），要等的时间放在 `Retry-After`，它对付费模型没有平台级的请求上限，只有免费模型的额度和防 DDoS（OpenRouter《Limits》《Errors and Debugging》，2026-10-18）；sub2api 把原因编进错误码是它自己的做法（`gateway_queue_full`、`gateway_concurrency_limit`，`backend/internal/handler/concurrency_error_response.go`）。
- **供应商自己的 401、402、403、429** 只用来换凭证（同一端点另一把凭证，没有可用凭证时换下一个端点）或换端点，从不当作我们的认证或钱包错误返回给客户端。
- **流中途出错**：状态码保持 200，按各格式的方式在流里报错，然后结束。结算已提交的结尾带用量和费用，没提交的只带用量：
  - Chat：一个带顶层 `error`、结束原因为 `error` 的数据块；
  - Anthropic：`error` 事件；
  - Responses：`response.failed` 事件。
  - 上游自己报的错按它的类别定（`protocol.FaultOf`）：拒绝请求本身的（上下文过长、内容被拒、请求不合法）算这个请求的，不换端点重试，客户端读到上游的原话（格式支持的还带 `code`、`param`）；请求的写法没问题而端点失败、限流、看不出来的，都算供应商的失败，用统一的说法，提交点之前还可以交给下一个端点（[路由的「失败怎么办」](routing.md#失败怎么办)）。失败事件立即结束尝试，不等上游关连接；按字段值解码类别，转义写法与 HTTP 错误同义。
- **非流式请求出错**：最后一次尝试生成了部分内容后失败时，Chat 返回 200，带已生成的内容、`finish_reason: "error"`、`error`、用量和费用；Responses 返回 200，`status: "failed"`，带 `output`、用量和费用；Messages 例外，返回 502 错误体、不带内容，用量和费用放在 `axiom` 对象里。没生成内容的，返回上表的状态码。未完成的工具参数不修补。
- **对中转站对接方友好**：计费类问题绝不返回 401；不使用 new-api 默认会触发自动禁用渠道的措辞，例如 "You exceeded your current quota"、"Your credit balance is too low"（new-api 源码，2026-10）。这份措辞清单写成测试，防止以后被无意改坏。

### 业务速率与 OpenRouter 的差异

基线：[OpenRouter Limits](https://openrouter.ai/docs/api/reference/limits)，2026-10-29 核实：付费模型没有平台级请求速率上限；平台速率 429 带 `X-RateLimit-Limit`、`X-RateLimit-Remaining`、`X-RateLimit-Reset`，成功推理响应不带；`GET /api/v1/key` 报告预算与 effective_budget，`rate_limit` 已标为 deprecated。

Axiom 有意对付费调用也施加后台配置的账户/等级/key 业务限流，防滥用并保护共享容量；等级给每个账户默认值、账户覆盖等级、key 独立受限，同时保留节点保护。继承、0 不限、默认和共享故障语义以[账户域](accounts.md#业务速率限制)为准。调用和 count_tokens 分开计数；读 key 信息不消费调用次数。

只有请求速率触发的 429 带这三个头，Limit 是触发额度，Remaining 为 0，Reset 是 Unix **秒**的保守清空上界（预检时刻加两个窗口，向上取整到秒），Retry-After 为两个窗口并沿用最多 60 秒的兼容上限，Reset 可以更晚；滑动近似及持续重试计数使它不承诺精确放行时刻。容量/队列 429 继续只给已有的重试信息；成功不发限流头。秒是本站明确契约，客户端应按秒解析并遵循 Retry-After；上述 OpenRouter 官方文档只列头名、未标单位，无法确认其线上值是否毫秒，不宣称单位兼容。

`GET /api/v1/key` 的 `rate_limit` 保留 requests/interval，另给 account_requests/key_requests；requests 是包含节点保护在内的有效最小正上限，interval 是配置窗口（默认 `1m0s`），没有免费模型专用策略。`budgets` 仅列已设置的 workspace/key_limit 预算，effective_budget 为第一个耗尽项（工作区优先，与计费检查顺序一致），否则为剩余最少项；无预算是 [] / null。limit_usd、spend_usd、remaining_usd 沿用兼容字段名但表示本站额度，新增 reserved_usd 明示预留，remaining 扣已收和预留并下限为 0；reset_interval 与数据库时钟的 resets_at 给 UTC 重置边界，永久预算为 null。没有 BYOK 用量汇总、组织护栏/member/全局 workspace 合并预算，guardrail_id、budget_id 为 null，include_byok 为 false（这个字段只出现在预算接口里，描述是否把 BYOK 花费计入预算；Axiom 的 BYOK 调用照常占用余额和限额，见[计费](billing.md#定价)）；余额另由 credits 表达。原 limit/limit_remaining 继续只描述 key，与 key_limit 预算复用同一条查询的已收、预留和重置边界，不会因两次读取间的提交或跨 UTC 零点相互矛盾。

### 保活

- **开始转发内容之前不发任何东西**：这时还可能换端点，状态码也还没确定（见[路由的「失败怎么办」](routing.md#失败怎么办)）。
- **开始转发之后**：上游仍在正常传输、但暂时没有新内容时，才发 SSE 注释行保活。上游长时间完全无响应，就报错结束，不无限保活。
- **Anthropic 格式**：上游原生的 `ping` 事件照常透传；从没有 `ping` 的上游转换过来时，由我们补发，这是 Claude Code 指南的要求。
- **不发自创的 JSON 心跳**：只用各格式自己定义的保活方式。sub2api 曾因发送自创的 JSON 心跳导致严格的客户端崩溃（sub2api issue #7946，2026-10）。

### 路由参数

- **接受 OpenRouter 的参数**：
  - `provider` 对象：只走、排除、指定顺序、是否允许回退、要求支持全部参数、数据政策、排序、最高价、性能条件；
  - `models` 备选模型；Messages 也支持 `fallbacks: [{model: "…"}]`，限制与映射见[路由的「命名预设」](routing.md#命名预设)和「失败怎么办」；
  - 模型名变体 `:nitro`、`:floor`：等同 `provider.sort` 的 `throughput`、`price`；`:free` 查目录里的独立条目，没有就是 404；`~…-latest` 解析目录别名；`:exacto` 只在质量验证过的端点上按质量优先，没有时 503 `no_endpoint`（原因 `not_verified`）；`:online` 未配置搜索后端时 400、配置后等同 `web` 插件，`:batch`、`:extended` 返回 400，`:thinking` 只在请求已要求推理时接受，见下文「模型名变体」；`openrouter/auto`、`openrouter/free` 按路由排序选具体模型，见下文；
  - `@preset/名称`、`model@preset/名称`、`preset` 字段：使用当前账户的命名请求配置（可包含 provider 路由，但不是 key 的路由预设），覆盖与管理接口见[路由的「命名预设」](routing.md#命名预设)；
  - `session_id`、`x-session-id` 请求头、`prompt_cache_key`；请求体 session_id 优先于请求头，二者最多 256 字符；
  - `user`：终端用户标识，用于防滥用和归因。

  这些参数的语义见[路由的「请求覆盖」](routing.md#请求覆盖)。
  - **能兑现的**（2026-11-01 核对 [OpenRouter Provider Routing](https://openrouter.ai/docs/guides/routing/provider-selection)）：`order`、`only`、`ignore` 使用供应商分组的公开代号，精确、区分大小写匹配；不接受真实供应商名称或内部 ID，也不把 OpenRouter 的供应商 slug 猜成本地供应商。未知的 `only` 或关闭回退的未知 `order` 无匹配时返回 404；未知的 `ignore` 无效果，未知的开放 `order` 仍可回退。空列表和 `null` 不增加限制。`allow_fallbacks: false` 有非空 `order` 时只试命名组；否则每个模型仅保留排序后的第一个端点；`models` 的跨模型回退仍保留，`partition: none` 也按模型去重。`sort` 支持 `price`、`throughput`、`latency` 字符串，以及 `{by, partition}` 对象：默认 `partition: model` 按模型分组，`none` 跨主模型和备选排序。`data_collection`、`zdr` 生效；`max_price.prompt/completion/request` 分别按每百万 token 和每个生成请求计价，请求项在某服务档位缺价时按价目表最高请求价，报价、上限和结算一致；数字性能偏好继续按 EWMA 比较，p50/p75/p90/p99 对象按滚动五分钟窗口比较，写出的每个截断都必须满足，不满足的端点排到后面而不是被排除；`quantizations` 按管理员记录的端点级别精确匹配，没有记录的端点不匹配任何非空列表。`require_parameters: true` 按端点实际调用格式的有效 `supported_parameters` 过滤；这份清单由模型规格的能力和方言及该格式的实测共同推出，覆盖 `temperature`/`top_p`、输出上限、`stop`、`response_format`、`tools`/`tool_choice`/`parallel_tool_calls`、`reasoning`/`reasoning_effort`。端点发现接口逐端点发布这份清单；`/api/v1/models` 的模型级清单只是模型基线，不能替代逐端点的判断。路由按端点评估的实测加上或去掉参数位，并把能力参数（`tools`、`response_format`、`reasoning`）实际出现在请求里的成员计入 `RequiredParameters`，所以实测不支持的 `tool_choice` 等会真正过滤。空 `tools` 不算需要工具；采样、输出上限和 `stop` 也按同一份清单判断。所有过滤都叠加在 key 的路由预设与归属权限之内，不能放宽（见[路由的「请求覆盖与组合」](routing.md#请求覆盖与组合)）。

  **与 OpenRouter 的偏离**（无法兑现的限制在预留与调用上游前回 400 `invalid_request`，错误消息指出 `provider` 成员；Chat/Responses 与 Messages 各用上文「错误」的外壳，Chat/Responses 的 `error.code` 是与状态行相同的数字（与 [OpenRouter Errors and Debugging](https://openrouter.ai/docs/api_reference/errors-and-debugging) 一致）：

  | 参数或行为 | 这里与理由 | 支持它还需要的数据 |
  |---|---|---|
  | 供应商名称 | 当前接入的都是中转系统，产品要求这类分组只用匿名代号：代号随机生成，不从供应商名、地址或其他可反推的数据派生，同一分组在所有模型上相同，也是 `provider.only`、`provider.ignore` 和平台预设供应商顺序的公开身份。官方直连 API 接入后，公开页和发现接口的同一字段显示其真实名称 | 官方直连供应商的接入与命名 |
  | `require_parameters: true` | 只保证该端点实际调用格式公布的能力参数和采样/输出参数；`seed`、`logprobs`、`frequency_penalty`、`verbosity`（含 Responses 的 `text.verbosity`）和供应商专有参数不在清单里，请求写了这些就 400 并点名，因为供应商同步的模型记录和目录规格没有逐端点的采样/提供商参数清单。能力位缺失按能力缺失 404；清单内的采样、输出上限、`stop` 或能力参数位没有端点满足时按覆盖过滤 404；`false` 或未写仍强制已有能力匹配 | 逐端点的采样和供应商专有参数清单（检测实测到的参数位已按[端点评估](quality.md#实测的能力与上限)替换） |
  | `sort` 对象的 `percentile` 与其他未知成员 | 400；OpenRouter 的排序对象只有 `by` 和 `partition`（2026-11-01 核对），`percentile` 不是排序成员；百分位截断属于 `preferred_min_throughput` / `preferred_max_latency`，已按滚动五分钟窗口支持。其他未知成员也按名字拒绝，不静默忽略 | 无；若 OpenRouter 将来把分位数加进排序对象，还要定义它和 `by` 的组合语义 |
  | `max_price.image` | 400；价目表已有按张的图片项（`image_in_piece`、`image_out_piece`，#320，模型列表的 `pricing.image` 也从前者发布），但 OpenRouter 的单个 `image` 上限没有说管输入图、生成图还是按 token 计价的图；没有端点报告按张的图片用量，本站也不输出生成的图，该比哪一项未定，所以不拿 `image_in`/`image_out` 的 token 价冒充 | 先定 `max_price.image` 管输入、生成还是两者，并让端点报告按张的图片用量；在那之前保持 400 |
  | `max_price.prompt` / `completion` | 本站记账货币每百万 token，比较每个端点自己的受众售价的适用服务等级与上下文档位最高价；同一模型的不同端点各自过滤；只接收可精确表示的十进制数，不四舍五入。非美元站点不声称美元上限 | 无；已实现 |
  | `max_price` 的其他成员 | 400；不认识就不静默忽略 | 对应的计费单位与来源价 |

- **我们自己的扩展**：放在单独的 `axiom` 对象里，也可以用 `x-axiom-*` 请求头传，方便无法修改请求体的工具。
- **转发前处理**：路由参数和扩展字段在消费后删除；`plugins` / `transforms` 按下节执行或拒绝，不当作没写；会话和用户标识换成派生值（见上文「转发规则」）。

### 插件与提示词变换

基线于 2026-10-29 核对：[插件](https://openrouter.ai/docs/guides/features/plugins)、[响应修复](https://openrouter.ai/docs/guides/features/plugins/response-healing)、[上下文压缩](https://openrouter.ai/docs/guides/features/message-transforms)。`plugins` 和 `transforms` 在入口校验，只有已经执行或明确关闭的控制项才在转发前删除。

| 成员 | 执行 / 拒绝 / 忽略 |
|---|---|
| `plugins:[{id:"response-healing"}]` | 执行：Chat 非流式且 `response_format.type` 为 `json_object` 或 `json_schema` 时，本地修复 JSON 语法、提取代码块/前缀后的 JSON。流式、普通文本、失败或 `length` 截断回复不修复，与基线一致；修复后移除不再对应文本的 logprobs；不保证 schema 语义校验，也不保证任意坏 JSON 可修复 |
| `plugins:[{id:"context-compression"}]`、`transforms:["middle-out"]` | 执行：Chat 在准入之前从中间删除完整对话轮次，保留首尾；仍超长时从纯文本消息中间截短；按 [Anthropic 消息上限](https://platform.claude.com/docs/en/api/messages/create)（2026-10-29）裁到最多 100000 条，跨消息的未完成工具调用不拆组。系统指令、工具调用及结果、签名和多模态部件不截短；剩余固定内容放不下则 400，不把不完整的工具对发给上游 |
| `plugins:[{id:"context-compression",enabled:false}]`、`transforms:[]` | 执行关闭；显式插件策略优先于旧 `transforms`，没有显式策略时，Chat 上下文不超过 8192 token 默认尝试：正文超过整个上下文的字节上界（不扣默认输出上限），或超过消息条数上限才启动；压缩失败或临时内存不足时原样转发，不新增拒绝 |
| 已知插件的 `enabled:false`、空数组、null | 忽略（等价于关闭或未指定）；未知 id 即使关闭仍拒绝 |
| `plugins:[{id:"web"}]` | 执行联网搜索，**一次请求只走一条路径、只收一次费**。`engine` 写明启用的后端名（见下）或 `native`；省略时，所选 entry 的模型在规格里声明 `web_search` 能力且端点格式是 Chat、Responses 或 Messages，就走模型厂商自己的搜索，否则走默认后端。指定未知或已停用的后端、在模型没有该能力时指定 `native`，都回 400。**模型原生搜索**：网关不调用自己的搜索服务，而是在转成端点格式后的正文里打开厂商的开关——Chat 加 `web_search_options:{}`，Responses 加 `tools:[{"type":"web_search"}]`，Messages 加 `tools:[{"type":"web_search_20250305","name":"web_search","max_uses":N}]`；`N` 取模型规格 `max_uses.web_search`，没有就 1，最多 10；Chat 的 `web_search_options` 没有次数字段，预留按 1 次，上游报告更多就按实际次数结算（超出预留的部分按余额底线截断）。已有同名工具时原样保留，所以不会开两次。按上游报告的 `server_tool_use.web_search_requests` 在模型售价上加价收费（[计费](billing.md#预留与结算)）。**后端搜索**：准入时选定后端后取最后一条用户文本为查询，设置注入上界并重新路由一次（按这个上界检查上下文和输出上限，必要时换到大窗口回退；原生路径仍用真实请求长度），按 `max_results`（默认 5，最多 10）、`include_domains` / `exclude_domains`（转成 `site:` / `-site:`）和可选的 `search_prompt`（≤512 字节）调用该后端；查询经出网规则并限时 15 秒，结果作为 system 消息注入第一条用户消息之前，按该后端发布的价格收取一次搜索费。后端未配置且模型也不能原生搜索时 400，消息含 `not supported`；管理员清空引擎列表会写一条空的旧单后端行，环境变量不会把搜索带回来；`:online` 等同省略 `engine` 的插件。写入超过 1024 字节或查询为空时 400。自动搜索模型（`always_searches`）的回退只保留同样自动搜索的模型，普通回退没有搜索结果，不能接在失败的自动搜索之后。**不搜索两次**：请求自己已经声明厂商搜索工具（`Needs` 含 `WebSearch`），或模型规格写了 `always_searches`（例如 Perplexity 的 sonar），插件被丢弃，既不搜索也不收费；`engine` 只是选择路径，不改变这条规则。原生路径不看 `max_results`、`search_prompt` 和域名过滤，因为搜索由厂商执行，网关只能决定开不开。原生搜索时 Responses/Messages 的引用注解在转成 Chat 时写成 `url_citation`；Chat 原生搜索的注解原样透传。插件的结果只给非流式 Chat 补 `url_citation`（正文里与结果 URL 对应的 markdown 链接），流式回复不补（整条消息要到结束才知道）。搜索前按 `websearch.WorkingSet` 把响应与解码工作集计入节点内存预算；预算不够 503，不搜索、不调用上游。搜索失败或超时是普通上游失败，回 502 且不调用模型、不计模型用量 |
| `plugins:[{id:"file-parser"}]` | 解析文件：`pdf.engine` 为 `native` 时把文件原样发给接受文档的端点（路由只保留这类端点）；为 `mistral-ocr` 或 `cloudflare-ai` 时，准入前把请求里的内联文件（Chat `file`、Responses `input_file`、Messages `document` 的 base64 源）交给部署配置的 Docling serve 转成文本，用文本部件替换文件部件，再取会话键、路由、报价，所以报价就是转发的正文。两个 OCR 引擎名落到同一个后端：本站没有 Mistral 和 Cloudflare 的账户。没写 `pdf.engine` 等同 `native`，不做 OpenRouter 那种「模型不支持原生文件输入就改用 OCR」的回退，因为路由之前不知道模型，也不替客户端发起没要求的付费解析；`pdf-text` 按 OpenRouter 的弃用重定向等同 `cloudflare-ai`。未配置后端时 OCR 引擎 400，消息含 `not supported` 与「没有配置外部服务」，和接入前一样；`native` 不需要后端。`Config.FileParse` 打开时按页计：`mistral-ocr` 每页一次 `file_parse`，价格在模型价目表里；`cloudflare-ai` 免费；`native` 按输入 token，不经过网关解析。PDF 请求同时要 `json`，页数取 Docling 的 `json_content.pages` 条目数，空页也计入，和 OpenRouter 按 PDF 物理页一致；非 PDF 输入没有 PDF 页树，才退回 Docling 的 `md_page_break_placeholder`（占位符由文件字节的 SHA-256 前缀生成，出现次数加一，然后从正文删掉）。PDF 页数字典缺失时按上游失败拒绝，不会退回内容页少计。解析是采样之前的 `Flat` 收费，不增加采样。开关关闭时保留旧规则：每个解析的文件按 `web_search` 计，已有站点升级后不会因为 `file_parse` 未发布而失败。跳过解析的复用不计。单个文件解码后最多 20 MiB、一次最多 8 个文件、替换进正文的文本合计最多 2 MiB（同一个文件在历史里出现几次就计几次），超过 413；后端响应最多 8 MiB（PDF 的应答带 JSON 页字典）、整个解析步骤限时 60 秒；改写之前按 `3×正文 + 12×MaxParsedBytes` 的上限计入改写的工作集，改写完成后只保留正文增长到调用结束、其余退回，不够 503（所以预算不足时不会先分配再拒绝）；每个文件的解析另按 `2×文件大小 + ResponseLimit` 计入，正文增长再计一次；回复里的注记在编码之前按编码后的长度预留，每个 choice 的容量增长按两份计入聚合预算（部件和写进正文的副本），任一 choice 装不下就整个回复都不写注记。发文件给后端之前先做一次只读的 key 检查（和准入同一条 `AdmitKey` 查询）并按 key 的速率窗口判断限额：停用、过期、IP 白名单不符或已超速的 key 在这里就收到准入会给的拒绝，文件不会被解析；真正的预留仍然只写在准入事务里，所以那次检查只是把明显不能用的 key 挡在解析之前，不改变计费。后端失败、超时或返回无法使用的结果是 502 `upstream_error`，不调用模型、不计模型用量；不是 base64、同时带 `file_data` 和 `file_id`、文件不在消息顶层内容里（工具结果里的文档、`source.type` 为 `url` 或 `text` 的 Messages 文档）、请求要求解析却仍有无法解析的文件时 400；只带 `file_id` 的引用原样保留，由路由按 `provider.only` 固定供应商。非流式 Chat 在 `choices[].message.annotations` 里返回 `{"type":"file","file":{"hash","name","content":[{"type":"text","text"}]}}`，`hash` 是文件字节的 SHA-256；客户端把这条注解随上一次 assistant 消息发回、同时照旧带文件时，本站用注解里的文本替换文件、不再解析也不计费。流式回复不返回注解。显式关闭可接受 |
| 未知插件 id、未知 transform、未支持的插件选项；非 Chat 格式启用本地插件 | 400，消息含 `not supported`，不静默删除 |

**有意偏离**：压缩使用目录主模型的上下文和输出上限，在路由与预留之前完成；不实现 OpenRouter 跨模型的「半个所需上下文」候选扩展，也不对每个回退端点重新压缩。仍按正常路由过滤装不下的备选模型。输入使用 JSON 正文字节上界（包含工具定义及控制字段）而非调用外部分词服务，因此仍可能更早压缩，显式开启时保护内容装不下会拒绝；默认策略只在超过整个上下文的字节上界或消息上限后尝试，失败时保留原文和原摘要。默认输出上限等于上下文也不会因此拒绝请求，实际输出上限仍由正常路由裁到剩余窗口。同一份摘要、预留与转发正文一致。只提供 Chat 插件接口，其他格式显式拒绝启用；不增加账户级默认插件设置。`file-parser` 的 OCR 引擎同样只走部署侧的一个后端（[fileparse 模块的 README](../../internal/fileparse/README.md)），`native` 引擎不需要后端；两者都不把文件发给 Mistral 或 Cloudflare。`file-parser` 的注解只在非流式 Chat 回复里给出，流式到结束才知道整条消息，这是与 OpenRouter 的一处差异。联网搜索的注入文本是本站自己的偏离：OpenRouter 的插件文档只写结果以注解返回，没有写注入格式（OpenRouter《Web Search》，2026-10-31 核对的页面没有该细节），这里照它官方博客的 system 消息措辞（《Introducing web search via the API》，2026-10-31），把标题、URL 和摘要编号列出，并允许 `search_prompt` 替换指令；结果逐项读取，只保留最多 `max_results` 条有效结果，URL 最长 8 KiB，标题 256 字节、摘要 1024 字节、响应 4 MiB 截断，空对象或无效结果不按响应条数分配，注入正文不超过 40 KiB，所以客户端内容不会被搜索结果无限放大。

### 服务端工具

2026-11-01 核对 [Server Tools](https://openrouter.ai/docs/guides/features/server-tools) 与 [Web Search](https://openrouter.ai/docs/guides/features/server-tools/web-search)。服务端工具和客户端执行的 `function` 不是一回事：模型自己决定调用零到多次，网关执行后把结果作为工具结果送回模型继续采样。本轮实现 `openrouter:web_search`，其余 12 个仍按名字拒绝。

- **认哪些、怎么接**：`openrouter:` 命名空间里文档列出的 13 种仍由 `internal/protocol/grammar.go` 的 `openrouterTools` 一处认名；只有 `web_search` 接受，其余在准入前 400，消息含 `not supported` 和工具名。三种格式的 `tools` 数组都认，`parameters` 按 OpenRouter 的字段解析（见下表）。客户端写的工具对象在准入前从正文里删掉，网关按端点格式在转换后的正文里加一个普通函数声明，名字是 `openrouter_web_search`：OpenAI 与 Anthropic 的函数名只允许 `[a-zA-Z0-9_-]`，冒号不合法。函数声明随每次采样重写，步数或结果上限到了以后不再声明，让模型用已有结果作答。一次只支持一个 choice（`n>1` 回 400）：多个 choice 的工具调用要各自维护一条对话，当前实现只追加一条。
- **走哪条路**：`engine` 和默认值的规则与 `web` 插件相同（见「插件与提示词变换」）。所选 entry 的模型在规格里声明 `web_search` 能力、端点格式是三种 Chat 形态之一，且 `engine` 是 `native`，或省略/`auto` 而模型能原生搜索时，工具映射成厂商自己的搜索工具：Chat 加 `web_search_options:{}`，Responses 加 `tools:[{"type":"web_search"}]`，Messages 加 `tools:[{"type":"web_search_20250305","name":"web_search","max_uses":N}]`；网关不跑循环，也不调用外部搜索。否则网关跑模型侧循环，用所选后端搜索（`engine` 指定的后端或默认后端）；后端未配置且模型不能原生搜索时，仍回原来的 400，消息含 `not supported`。
- **循环**：模型在回复里调用 `openrouter_web_search`，网关读出 `query`，按 `max_results` 和允许/排除域名调用后端，把这次 assistant 回合（文本、thinking 块及其签名、工具调用）原样保留，再按客户端格式追加工具结果（Chat 的 `role:"tool"`、Messages 的 `tool_result`、Responses 的 `function_call_output`），采一次模型；直到模型不再调用，或步数、结果上限到达。每步是同一条目上的一次独立尝试：先结束上一步的供应商请求、归还凭证槽并按它自己的回执计价，再取新的凭证槽和滚动分钟配额发出，所以容量为 1 的凭证也能走完循环；每步有自己的尝试序号、`attempt_costs` 成本行和对账键，每笔供应商扣费对上自己那一步。路由的回退最多 `platform.MaxAttempts`（3）次，循环全程最多 `platform.MaxServerToolSteps`（30）步，尝试序号因此最大 `platform.MaxAttemptSeq`（33）；回退次数按全部尝试计，循环的每次采样也算在内：没跑过搜索的循环之后还能回退，但回退的尝试序号一定在 3 以内。单独收费的尝试因此都编在 1–3，滚动发布期间旧版本的账单修正任务也认得它的序号，不会丢掉修正。步数上限是 `parameters.max_uses`（默认 30）与请求的 `max_tool_calls`（默认 30，最多 30）的较小值；`max_total_results` 还限制整个循环返回的结果条数。到上限后追加一条说明，去掉函数声明，并在没有客户端工具时去掉强制的 `tool_choice`/`function_call`，做最后一次采样；模型仍可能不调用工具直接作答。同一回合里既有服务端搜索又有客户端函数调用时回 502，不静默丢掉客户端调用。结果正文总预算 32 KiB、每次搜索最多 8 KiB，超过就停；查询最多 1024 字节，没有可用查询时不搜索，只把说明作为工具结果返回。`max_tool_calls` 是 OpenRouter 的循环控制，Chat/Messages 上游不认，发出去前删掉；Responses 的原生工具限额保留。
- **参数**：`engine`（`auto`、`native` 或已启用的后端名，默认 `auto`：能原生就原生，否则默认后端；显式 `native` 而模型没有原生搜索时回 400，OpenRouter 会改走 Exa，这里沿用 #542 插件的拒绝规则）、`max_results`（1–25，默认 5；后端本身最多返回 10 条）、`max_uses`（1–30，默认 30）、`max_total_results`（1–1000）、`allowed_domains` 与 `excluded_domains`（各最多 20 项，转成 `site:` / `-site:`）。`mode`、`search_context_size`、`max_characters`、`user_location`、`x_search` 和顶层的 `stop_server_tools_when` 还没实现，显式写就 400 `invalid_request`，不静默忽略；`max_tool_calls` 超过 30 也 400。原生路径忽略 `max_results` 和域名过滤，因为搜索由厂商执行；`max_uses` 与 `max_tool_calls` 折算成厂商工具的 `max_uses`，最多 10。
- **流式与超时**：客户端只看到一条流。循环中每一跳的事件先整跳留在网关（按节点内存预算记账），跳结束后确认不是工具调用才作为回答写给客户端；因此工具调用和中间结果不会出现在客户端的流里，代价是首字节要等到最后一次采样结束。客户端断开取消正在进行的采样；第一次采样按端点的 `Cancels` 决定是否读完以取得用量；新搜索和之后的各步是新的付费工作，一律跟随客户端。各步都在尝试的上下文里发出，受调用期限、响应头等待、空闲超时和节点关停约束；搜索也在尝试的上下文里跑、另跟随客户端，每次限时 15 秒，每次搜索前检查调用是否已经结束，所以期限到了或节点关停后不再发起新的付费搜索（已经完成的搜索照收）。一跳的上游结束时就停掉它的空闲计时器：搜索期间没有上游在发数据，空闲计时器不应切断尝试。整个调用受 `MaxCall` 约束。网关为循环保留的东西都记入节点内存预算并在分配前申请：每跳缓存的客户端事件按缓冲区容量记账，丢弃时连缓冲区一起释放；`Relay` 捕获助手回合时逐个 token 读事件，只物化要保留的成员、跳过其余，上游控制的数组不解码成切片；每个事件先按可能的增长上界记账、读完后按实际占用（含块、调用和索引的容器）退还多记的部分，预算不够就不读这个事件；构造回放用的回合前按上界记账、写进不再扩容的缓冲区，随 relay 释放；下一跳正文的复制按会话正文、回放回合和转义后的结果之和记账。非流式调用里工具跳也会过 relay 的提交点，所以每跳开始时清掉"已有内容"，只有最后作答那一跳的内容决定是否交付：末跳正常结束但没有内容，按空回复失败处理（502，只收搜索费）。Chat 客户端经 Messages 或 Responses 上游时，签名和加密推理在 `reasoning_details` 里，回合捕获原样保留它们并回放，下一跳准备请求时再按 type 与 index 合并，否则下一跳没有可回放的推理。
- **失败与计费**：每次成功搜索按该后端的价格收一次 `web_search`，与 `web` 插件共用价格和按次项；每次继续采样的 token 照常计费：用户为循环的各次采样合起来付一次，按最后一次采样的结局套免单规则（见[计费的「免单规则」](billing.md#免单规则)），平台成本是各次尝试成本之和，从不拿最后一张回执给全部用量重新计价。预留按步数上限的最坏情况：输入按 1 + 步数上限次采样（第一次用请求正文的上界，之后各按模型上下文长度，回填的结果也算在里面）、输出按 1 + 步数上限乘每次的输出上限（`billing.ServerTool.Repeats`）。循环中途后端失败、上游失败、超时、空 200 或流中断：没有内容交到客户端，模型用量免单，已经执行的搜索照收，不换端点（换端点要重新搜索并重新预留），回 502 `upstream_error`；客户端主动断开不免单。
- **厂商自己的服务端工具**：Responses 的 `web_search`、`code_interpreter`、`file_search` 不在 `openrouter:` 命名空间里，也不是这个循环：`protocol` 认出它们、原样转发给能执行它们的 Responses 端点，按调用项计费（见[计费的决策表](billing.md#决策与理由)）。`file_search` 需要供应商账号拥有请求里的 `vector_store_ids`，网关不代建、不代传；它只在 Responses 格式里声明，转成 Chat 或 Messages 时按 `NotSupported` 拒绝。
- **不做的部分**：其余 12 个工具仍拒绝，原因是没有它们的执行环境或计费映射（见 [billing 的「预留与结算」](billing.md#预留与结算)）。`openrouter:web_search` 不能与 `web` 插件、厂商自己的联网搜索或代码执行工具同时声明，准入前 400：循环的每次采样都可能再跑一次厂商的代码执行，报价却只算一次它的次数，而循环会删掉上游唯一能限住它的 `max_tool_calls`；要支持组合，得先让报价按采样次数累计原生工具的费用并保留上游限额。`GET /api/v1/tools` 的目录仍没有：清单不能先于执行公开。

### 模型名变体

模型名后面可以跟 `:` 开头的变体，2026-10-29 核对 [OpenRouter Model Variants](https://openrouter.ai/docs/guides/routing/model-variants/overview)。网关在查目录之前读掉路由变体；`:free` 是唯一的目录后缀，作为独立条目原样交给目录（见[模型目录的「名字解析」](catalog.md#名字解析)），其余路由变体不会新建目录模型。读法只在 `internal/gateway/variant.go` 一处，调用、`models` 里的备选、`count_tokens`、两种模型详情和 OpenRouter endpoints 查询共用。调用响应和记录里的 `model` 在成功时是实际服务的具体模型（`RelayOptions.ActualModel`），调用记录仍保存客户端写的整个名字；OpenRouter 模型发现响应报告具体模型的标准 ID。认得出但本站不能兑现的请求回 400 `invalid_request` 并说明原因，不伪装成模型不存在，不调用基础模型。

| 变体 | OpenRouter 的行为 | 这里 |
|---|---|---|
| `:nitro` | 按吞吐排序，并准入 priority 层的端点（[Nitro](https://openrouter.ai/docs/guides/routing/model-variants/nitro)） | 等同 `provider.sort: throughput`；层级准入见下 |
| `:floor` | 按价格排序，并准入 flex 层的端点（[Floor](https://openrouter.ai/docs/guides/routing/model-variants/floor)） | 等同 `provider.sort: price`；层级准入见下 |
| `:exacto` | 按质量信号排序：工具调用成功率、吞吐、延迟、基准结果（[Exacto](https://openrouter.ai/docs/guides/routing/model-variants/exacto)） | 质量优先排序（`routing.BestQuality`），并只保留质量验证过的端点；没有这样的端点时 503 `no_endpoint`，原因 `not_verified`。运营质量分只含可靠性、首字延迟和输出速度，没有 Exacto 的工具调用成功率和基准评分，所以不声称按同样信号排序 |
| `:online` | 已弃用，等于 `web` 插件（[Online](https://openrouter.ai/docs/guides/routing/model-variants/online)） | 等同 `plugins:[{id:"web"}]`：配置了搜索服务时执行一次搜索并计一次费，未配置时 400，与 `web` 插件共用一句话；`openrouter:web_search` 另走模型侧循环（见「服务端工具」） |
| `:free` | [独立免费目录条目](https://openrouter.ai/docs/guides/routing/model-variants/free)，与付费条目的规格、价格和端点分开，OpenRouter 仍在提供 | 目录里的独立模型：标准 ID 是 `基础标准 ID:free`，有自己的规格、上下限、零价和端点，精确查询；目录没有这个条目时 404 `model_not_found`，不回落到付费基础模型，也不按零价猜测。计费发布同样拒绝给它非零售价（`ErrFreePrice`），路由对已发布非零价的条目没有端点 |
| `:thinking` | OpenRouter 已弃用，总览页让改用 `reasoning` 参数；原先是独立的思考目录条目 | 请求自己已经要求推理时**接受**：本目录没有独立条目，这个后缀唯一能兑现的含义就是开启推理，此时已经满足，按基础模型服务；没要求推理时 400，提示改用 `reasoning` 参数。发现接口按基础模型回 200 |
| `:batch` | 独立批量计价条目 | 400：本目录没有批量条目与价格，不按普通价冒充批量价 |
| `:extended` | 已弃用，且没有模型再提供（总览页） | 400：本目录没有这个条目 |
| 别的后缀 | 总览页：只有列出的才是变体 | 404 `model_not_found` |
| `~作者/系列-latest` | [Latest Model Resolution](https://openrouter.ai/docs/guides/routing/routers/latest-resolution)：按已登记的模型族映射选最新可见模型，响应 `model` 是具体模型 | 目录别名表的最新目标：管理员把别名指向具体模型，切换追加版本并留审计；解析后按该模型的规格、价格和端点服务，响应 `model` 是具体标准 ID。没有别名时 404，不按前缀或日期猜最新版本 |

- **一张表**：每个后缀做什么、拒绝时说什么，都在 `internal/gateway/variant.go` 的 `variants` 表里，三种格式、调用、备选、`count_tokens` 和发现共用。表外的后缀是 404。`:free` 是例外的一条：它是目录数据，表里只把后缀留在名字上。
- **`:free` 与 `:batch` 的区别**：OpenRouter 把免费版当独立模型，有独立的上下文、价格和端点，本站也建独立目录条目，所以 `:free` 能真正兑现，不是把付费请求改写成免费；`:batch` 还没有独立条目和价格，仍是 400。免费条目的上架门槛要求基础模型已上架、每个币种每个价都是 0，避免出现“免费名字按付费价收费”。
- **别名**：`~作者/系列-latest` 走目录的 `alias` 表，表里只追加 `(名字, 目标模型)`，快照读最新版本；切换由 `axiom catalog save-alias` 完成，`change` 和审计记录改前改后。目标必须是同作者的已上架模型，避免跨厂商串族；目标下线时解析返回“已下线”。别名本身不按前缀或日期推导，所以 `acme/gpt-5` 与 `acme/gpt-50` 不会串族。
- **`:thinking` 为什么只有一半接受**：它不是排序，而是「换成会思考的条目」。本目录一个模型只有一份规格和价格，没有思考条目；请求已经用 `thinking` / `reasoning` 参数要了推理时，后缀要的东西已经满足，再拒绝只会让照 OpenRouter 写的客户端多改一处。判断按整理后的能力标记：空的 `reasoning_effort`（或 `reasoning.effort` 的空值）不算开启推理，因为转换后的上游请求不会带任何推理设置；只有真正写出的 effort、`enabled: true` 或预算才算。反过来，请求没要推理时接受它，就会把「开启思考」悄悄降级成普通回答，所以仍是 400，并告诉客户端改成参数。发现接口不执行请求级判断，因此 `:thinking` 也像 `:exacto` 一样返回基础模型元数据。
- **叠加**：变体可以任意顺序叠加，最后一个排序变体生效（总览页）：`:floor:nitro` 按吞吐，`:nitro:floor` 按价格。`:exacto` 之后又写了排序变体时，它已被替换，不报错，也不再要求端点已验证；`:thinking` 与排序无关，叠加时保留。
- **与 `provider.sort` 同时写**：显式的 `provider.sort` 优先。依据是 exacto 页的一句话："An explicit sort by price, throughput, or latency overrides it"；nitro 和 floor 页没写这一点，按同样处理，所以 `:exacto` 加 `provider.sort` 也可用。
- **发现与调用的区别**：两种模型详情及 OpenRouter endpoints 查询只解析基础模型、检查可见性，不执行调用的请求级校验；`:exacto` 和 `:thinking` 因此可以返回基础模型元数据（200），不代表相同名字在未指定 `provider.sort` 或未要求推理时可调用。调用和 `count_tokens` 按上表行事：`:exacto` 可以调用，但没有已验证端点时仍回 503；模型发现的成功不承诺某个请求的路由要求能被兑现。
- **备选 `models`**：排序是整个请求一个（路由没有按模型的排序）。备选名字按同样的规则读：不支持的变体回 400，不当作略去；不认识的后缀，备选按"不存在"略去；没有 `:free` 条目或没有别名的备选也按"不存在"略去；备选的排序变体与请求最终的排序不同回 400；备选的 `:exacto` 只有请求本身已经是 `:exacto`（主模型名上）或有显式 `provider.sort` 时才成立，其余回 400，因为备选不单独按质量排。`count_tokens` 读同样的名字和 `provider`，拒绝的与调用一致。
- **名字长度**：整个模型名（含变体）至多 200 字节，超过回 404 `model_not_found`，在准入之前。调用记录保存客户端写的整个名字，列最多 200 个字符；目录原来也不认更长的名字，不能让叠加的后缀绕过这个界限，被服务之后才丢了记录。
- **层级准入没有做**：nitro 页说它同时准入 priority 层，floor 页说它同时准入 flex 层。这里只做排序一半。不写 `service_tier` 的请求本来就可以由 standard 和 priority 服务（协议 `anyTier`），flex 要客户端自己写 `service_tier`；为 `:floor` 改写发往上游的 `service_tier` 会改变预留和计费依据，另做。

### `openrouter/auto`

[Auto Router](https://openrouter.ai/docs/guides/routing/routers/auto-router) 让 OpenRouter 按任务分类、花费份额和成本档选一个模型。本站没有分类器和花费份额数据，用已有的路由排序代替：`openrouter/auto` 不是目录名字，网关在查目录之前认出它，把请求的排序（`:nitro`、`:floor`、`provider.sort` 或 key 的路由预设策略）作用到**这个 key 能调用的全部目录模型**上，取排序后的第一条，响应和记录里的 `model` 是它实际服务的具体模型。

- **范围**：只考虑本次请求的准入能通过的模型（key 的模型权限、key 的路由预设的类别和数据策略、上下文和输出上限、供应商余额和熔断等）；请求自己的 `models` 列表不再追加候选，因为 auto 已经覆盖全部模型。
- **排序**：用请求的显式排序，没有就用 key 的路由预设策略（默认均衡）；`openrouter/auto:nitro`、`:floor` 仍按变体规则改排序。随后的端点排序和回退照旧，所以一次 auto 调用失败后可以换到排名更后的模型，每次尝试的 `ActualModel` 都按该端点报告。
- **不支持**：`count_tokens` 回 400，因为没有具体模型能算 token；`context-compression` 回 400，因为压缩需要知道所选模型的上下文窗口。
- **发现**：`/models` 不列出 `openrouter/auto`，它不是可上架的模型。

### `openrouter/free`

[Free Models Router](https://openrouter.ai/docs/guides/routing/routers/free-router)（2026-10-31 核对）让 OpenRouter 从当前可用的免费模型里随机选一个，先按请求需要的能力（图片理解、工具调用、结构化输出）过滤，响应里的 `model` 是实际服务的免费模型。本站同样在查目录之前认出 `openrouter/free`，但用已有的路由排序代替随机：它把请求的排序（`:nitro`、`:floor`、`provider.sort` 或 key 的路由预设策略）作用到**这个 key 能调用的全部 `:free` 目录条目**上，取排序后的第一条；响应和记录里的 `model` 是实际服务的具体模型。

- **范围**：只认目录里的 `:free` 条目（[模型目录的「模型与名字」](catalog.md#模型与名字)），没有硬编的列表：运营加、删、下线免费条目后，下一次路由快照重建（约 5 秒）就改变可选项。请求自己的 `models` 列表不再追加候选，因为 free 已经覆盖全部免费条目；付费条目永远不进入这次排序。路由的常规过滤照常执行（能力、上下文、格式、路由预设与覆盖），不满足请求要求的免费条目不会被选中。
- **排序与回退**：和 `openrouter/auto` 共用同一套过滤、排序、熔断与跨模型回退；`openrouter/free:nitro`、`:floor` 仍按变体规则改排序。一次 free 调用失败后可以换到排名更后的免费模型，每次尝试的 `ActualModel` 都按该端点报告。
- **没有免费条目时**：回「错误」表里永久性的没有可用端点，即 404 `no_endpoint`，`reasons` 至少含 `no_candidates`；有免费条目但被过滤掉时，照常带上各端点被排除的原因。key 的模型权限把全部免费条目都排除时，仍按同一条规则隐藏成 404 `model_not_found`，与 `openrouter/auto` 和普通模型一致。
- **不支持**：`count_tokens` 回 400，因为路由前没有具体模型能算 token；`context-compression` 回 400，因为压缩需要知道所选模型的上下文窗口。
- **发现**：`/models` 不列出 `openrouter/free`，它不是可上架的模型。

### 给其他中转站对接的接口

| 接口 | 用途 |
|---|---|
| `GET /v1/dashboard/billing/subscription`、`GET /v1/dashboard/billing/usage` | OpenAI 旧版的余额接口。new-api 用它查询 OpenAI 类型渠道的余额：余额 = `hard_limit_usd` 减去以 0.01 为单位的 `total_usage`；`usage` 的 `total_usage` 是累计消费，不看 `start_date`、`end_date`（new-api 源码，2026-10） |
| `GET /api/v1/key` | key 的上限、剩余额度、各时间窗口的用量（OpenRouter 格式），供兼容 OpenRouter 的工具使用；调用 key 和管理 key 都可读，`is_management_key` 按实际种类给出 |
| `GET /api/v1/credits` | 账户余额（OpenRouter 格式），供兼容 OpenRouter 的工具使用；调用 key 和管理 key 都可读 |
| `GET /api/v1/generation?id=` | 按调用编号查询单次调用的详情（含 `native_tokens_*`、`streamed`、`cancelled`、`cache_discount`、`session_id`） |
| `GET /api/v1/generation/content?id=` | OpenRouter 的内容接口；本站不保存对话内容，属于本账户的调用也返回 404 `content_not_available`，别人的调用按 `generation_not_found` 处理 |
| `GET /api/v1/activity?date=&api_key_hash=&workspace_id=&group_by=workspace` | 最近 30 个完整 UTC 日的按日/模型用量，`api_key_hash` 用 key UUID，`user_id` 不支持；今天的调用不进结果，未完成的当天不算；金额读当前结算的实收（校正后），输入优先用写入的物理输入；官方要求管理 key，Axiom 的调用 key 可读本账户（见[决策](#决策与理由)）；管理 key 仍只读 `/key` 和 `/credits` |

- **单位**：这些接口里的金额都以本站的记账货币计。二级站点用人民币记账时，对方会把数字当美元显示，这在站点的接入说明里写明。
- **余额与用量**：`total_credits` 和旧版的 `hard_limit_usd` 是余额加累计消费，`total_usage` 是累计消费（旧版接口以 0.01 为单位，不看日期参数），所以额度减用量总是余额；在途预留不扣。`generation` 只读调用记录，行为见上文「用量与费用」的调用编号；`activity` 从同一份调用记录按 UTC 日聚合，只覆盖最近 30 个完整日；金额再按请求编号读 billing 的当前实收，所以校正后的调用不会继续显示旧快照，输入统计优先用物理输入，历史缺失时才用计费项之和。
- **管理 key**：只有 `/api/v1/key` 和 `/api/v1/credits` 接受管理 key（OpenRouter 的这两个接口也如此），其余辅助接口——`generation`、`generation/content`、`activity`、两个旧版余额接口和 `count_tokens`——仍只接受调用 key；管理 key 不能发起模型调用。
- **`POST /v1/messages/count_tokens`** 返回估算的输入 token 数（`{"input_tokens": n}`），口径、前置检查和理由见[模块 README](../../internal/gateway/README.md#辅助接口)；它不收费、不记录。

### Responses 的会话状态

- **排在 M2**：续接是第一版的功能，在 M2 提供。在那之前，带 `previous_response_id` 的请求返回 400，`DELETE /v1/responses/{id}` 返回 404。
- **单独保存**：会话状态是续接功能所需的内部数据，与对话内容的记录分开保存，不受对话内容开关的影响。保留时间由平台配置，默认 30 天，与 OpenAI 的默认一致（OpenAI《Conversation state》，2026-10）。它和对话内容一样按账户加密、按消息去重，销毁账户密钥时一并销毁（见[记录与告警的「加密与密钥」](observability.md#加密与密钥)）。
- **读一次会话状态**：续接按工作区和回复 ID 从保存处读一次，是[总体架构的规则 3](../architecture.md#模块边界规则)的例外之一；读不到就当作没有这个回复，不重试，也不按 ID 扫其他工作区。
- **不提供读取**：不支持按 ID 读回回复内容（OpenAI 的 `GET /v1/responses/{id}`），因为任何人都不能通过产品查看对话内容；续接不需要这个接口。
- **按工作区隔离**：同一工作区的 key 可以凭回复 ID 续接，其他工作区不行。
- **`store: false`** 的请求不保存状态，不能凭 ID 续接。
- **删除**：`DELETE /v1/responses/{id}` 立即删除；用户注销时按注销规则处理（见[账户与租户的「停用与注销」](accounts.md#停用与注销)）。
- **续接方式**：由我们把历史拼回完整的输入再发给上游，不依赖上游自己保存状态，所以换端点也能续接。其中只有原上游才认的加密推理内容，按上文「转发规则」处理。

### 与 new-api 的有意差异

对外行为要和 new-api 对得上，靠差分测试保证：`just diff`（在 CI 服务器上：`remote-check.sh <worktree> diff`）用 new-api 的官方镜像（按摘要固定，v1.0.0-rc.45，2026-10-17）和一个 Axiom 节点接同一个假上游，上游回放 `internal/protocol/testdata` 的录制样本；同一组请求分别发给两边，比状态码、响应头、JSON 体、SSE 事件。它要拉镜像、起容器，所以不在 `just check` 里，也不在每个 PR 的 CI 里；升级镜像是一个决定：读发布说明、跑一遍、逐条处理新出现的差异。

- **比什么**：Messages 和 Responses 客户端调用同格式的模型（透传），Chat 客户端调用 Messages 和 Responses 的模型（两边都做转换）和调用说 Chat 的模型（DeepSeek 一类，透传）；Messages、Responses 客户端也调用该 Chat 模型（反向转换），以及互相调用对方的原生模型（Messages 客户端到 Responses 模型、Responses 客户端到 Messages 模型，两边都做原生两格式之间的转换）；各自的文本、工具调用、推理、图片，流式和非流式，上游的 400、429、500，以及请求自己的错误（JSON 不合法、缺必填项、模型不存在、key 错）。每种格式让 new-api 的渠道和 Axiom 的端点说同一种格式，上游收到的是同样的路径，比的是怎么转发回复，不是谁选了哪种格式。
- **怎么比**：JSON 按成员比，不看顺序；数组按位置比；SSE 在 Messages 和 Responses 上按事件对齐后比，Chat 流按累加的结果比（文本、工具调用、结束原因、用量），因为切块是转换器自己的选择。非流式的上游回复是手写的、与录制流内容一致的官方形状（Responses 取流里 `response.completed` 的响应）；Responses 文本样本的增量被截短了，假上游把它补成与最终文本一致。
- **规则**：每个差异要么被 `cmd/axiom/diff_rules_test.go` 里的一条规则解释（写明理由），要么用例失败；没有匹配任何差异的规则同样使测试失败，修好的差异必须从清单里删掉。规则不只看路径和用例名，还限定两边的值：状态码是具体的一对（new-api 429、Axiom 502），上游身份头必须在 Axiom 一侧缺失，调用编号必须是 UUID，工具参数按解码后的 JSON 比较，措辞必须是 Axiom 的统一说法；退化的值（429 变成 200、参数变成 `{}`、泄露上游头）不被放行，`TestDiffHarness*` 为此给每类规则配了反例。规则只有有意的差异（下表）；还没人拍板的差异不进清单，它让测试失败，直到对应的 Issue 裁定。
- **Chat 流的结尾**：恰好一个 `[DONE]`，之后不能再有数据；提前的 `[DONE]`、重复的 `[DONE]`、`[DONE]` 之后的数据、其他非 JSON 的数据都是差异。工具参数的碎片先解码、再拼接、最后统一编码，所以切在转义字符中间的分块也算相同。

| 差异 | 理由 |
|---|---|
| 响应里的 `id`（含流里 `message_start`、`response.*` 里的）是 Axiom 的调用编号；请求 ID 头不同 | 见「转发规则」的自己回显和「用量与费用」的调用编号 |
| 用量里的 `cost`、`cost_currency`、`cost_estimated` | Axiom 每次调用都返回费用 |
| Chat 用量里 DeepSeek 的 `prompt_cache_hit_tokens`、`prompt_cache_miss_tokens`：new-api 原样带着上游的，Axiom 没有 | Axiom 的用量用格式自己的成员（`prompt_tokens_details.cached_tokens`）报缓存读，见「用量与费用」 |
| Chat 用量里上游没写 `prompt_tokens_details`、`completion_tokens_details` 时，Axiom 写 `cached_tokens`、`reasoning_tokens` 为 0，new-api 原样不写 | Axiom 的用量按结算写出格式自己的明细，客户端总能读到 |
| Chat 用量里 new-api 加的 `billing_usage`、`usage_semantic`、`claude_cache_creation_*`、`input_tokens`、`output_tokens` 等；Axiom 的 `prompt_tokens_details.cache_write_tokens` | 前者不属于 Chat 格式；后者与 OpenRouter 一致 |
| new-api 透传上游的 `cf-ray`、`openai-organization` 等头，Axiom 不放行 | 响应头按白名单放行，不暴露上游身份 |
| 上游的 429、5xx，new-api 原样返回，Axiom 一律 502 `upstream_error` | 见「错误」：供应商自己的 429 只用来换凭证或换端点 |
| 模型不存在：new-api 503，Axiom 404 `model_not_found` | 与 OpenRouter 一致，见「决策与理由」 |
| Messages 的错误：Axiom 用 Anthropic 的外壳（顶层 `type: "error"`）和统一的 `error_type`，措辞也统一；new-api 的外壳没有顶层 `type` | 各格式使用自己原生的错误外壳 |
| Chat、Responses 里我们自己的错误：措辞，`error.type`（new-api 写它自己的名字 `new_api_error`，我们写格式通用的词），个别错误的 `code`（new-api 在 JSON 不合法、key 错误时留空，我们写统一的错误类型），`metadata.error_type`；上游的 429、5xx 变成我们的 502 后，`type` 和 `code` 也是我们的 | 统一的说法和统一的错误类型，见「错误」 |
| Chat 回复的推理文本：`reasoning` 与 `reasoning_content` 同文并存，new-api 只有 `reasoning_content` | 见「转发规则」和「决策与理由」 |
| 流式请求在上游失败时，new-api 用 `text/event-stream` 包一个错误（带流的响应头，包括 `X-Accel-Buffering`），差分测试不比它的事件；Axiom 返回 JSON，不带流的响应头。SSE 的 `Content-Type` 带不带 `charset=utf-8` | Axiom 在流开始前出错就是普通的 JSON 错误 |
| 第一块内容之前上游的 `ping`，Axiom 丢弃；Messages 的 `message_delta.usage` 里 new-api 重复 `input_tokens` | 开始转发内容之前不发任何东西（见「保活」）；Axiom 原样转发上游的用量 |
| Chat 回复里 Axiom 写 `logprobs: null`、`message.refusal: null`，只有工具调用的消息 `content` 是 `null`，new-api 写空字符串，`created` 取调用时间，工具参数是流里的原文，Responses 转换成 Chat 时带上推理 token 数 | 与 OpenAI 的回复形状一致；new-api 的转换丢了这些 |
| Chat 流里 Claude 思考块的推理文本，new-api 在末尾多一个换行 | new-api 的转换自己加的；非流式没有这个差异 |
| `reasoning_details`（带签名和加密内容） | OpenRouter 的格式，后续请求要把它们带回去 |
| 转换给 Messages 时，thinking 块以空 signature 开始；不伪造思考签名，再次调用原生端点前删除无签名思考 | Chat 明文推理没有 Anthropic 签名，不能制造可回放的签名 |
| 转换给 Messages 时，message_start 的用量从 0 开始，结算后的 message_delta 才携带输入、缓存、输出的完整计数 | Chat 上游通常在最后一块才报用量；不能把尚未收到的计数写成真实值。内部计费沿用 Chat 的原始报告 |
| 转换给 Messages 时，new-api 在用量里附加 `billing_usage`、`claude_cache_creation_*` 等内部计费成员，Axiom 不写这些成员 | 客户端只接收 Messages 格式的用量，内部计费不改变协议外壳 |
| 转换给 Messages 时，Axiom 在最终用量里补齐为 0 的 `cache_creation.ephemeral_*_input_tokens`、`server_tool_use.web_search_requests`，new-api 省略 | 与既有 Messages 结算用量形状一致，不把缺报解释为实际使用 |
| 转换给 Messages 时，Axiom 写 `stop_sequence: null`，message_start 也写 `stop_reason: null`，new-api 省略 | 与 Anthropic 的消息生命周期一致，尚未结束或未匹配停止串时使用 null |
| 转换给 Messages 时，并行工具块在下一个块开始前关闭，new-api 到回合末尾才统一关闭 | Messages 的内容块依次输出；工具参数保持各自归属 |
| 转换给 Responses 时，输出项 ID 由调用编号和输出序号组成，created_at 为调用时间 | 不泄露上游身份；再次调用原生 Responses 端点时删除本站输出项 id |
| 转换给 Responses 时，成功响应的 `error`、`incomplete_details` 为 null，new-api 省略 | 与 [OpenAI 的响应事件定义](https://developers.openai.com/api/reference/resources/responses/streaming-events)一致 |
| 转换给 Responses 时，new-api 附加 null 的 `instructions`、`metadata`、`previous_response_id`、`reasoning`、`tool_choice`、`tools`、`truncation`、`user`，以及 0 的 `max_output_tokens`、`temperature`、`top_p`、false 的 `parallel_tool_calls`、`store`；Axiom 省略 | Chat 上游没有回报这些设置，转换器不凭空声称请求采用了这些值 |
| 转换给 Responses 时，new-api 为非图片输出项附加空 `quality`、`size`，为非消息项附加空 `role` 和 null `content`，为推理项附加 `status`；Axiom 省略 | 按输出项自己的类型写成员，推理项的状态是可选的 |
| 转换给 Responses 时，new-api 在 `output_item.added` 顶层另写工具的 `item_id`，Axiom 只在 `item.id` 标识输出项、`item.call_id` 标识调用 | 与 OpenAI 的输出项事件外壳一致，避免混淆输出项和工具调用 |
| 转换给 Responses 时，Axiom 输出 `content_part.added/done` 并按输出项出现顺序结束各项，new-api 省略文本内容块事件且先结束文本再结束推理；事件序号因此不同 | 完整发送 OpenAI 的内容块生命周期；每边的序号必须恰好对应自己的事件位置，内容和工具参数仍逐字段比较 |
| 转换给 Responses 时，new-api 的用量还带 Chat 名称、`billing_usage` 和 `claude_cache_creation_*`；Axiom 使用 Responses 名称，省略 null 的旧明细及为 0 的 `audio/image/text_tokens`，补齐为 0 的缓存写明细 | 复用既有 Responses 结算用量口径，不暴露内部计费外壳，不伪造媒体用量 |
| 转换给 Responses 流时，new-api 省略 `output_tokens_details.reasoning_tokens`，Axiom 保留上游报告（推理样本为 30，其余样本为 0） | 推理 token 属于真实用量，转换不能丢失；与同格式 Chat 的计费项一致 |
| 响应里的 `model`：Axiom 写目录里实际服务的模型（如 `anthropic/claude-opus-5-5`），new-api 回显请求里用的名字 | #319 P11：`models` 回退之后回显请求名会让客户端以为原模型回答；用量只暴露用户售价，见「决策与理由」 |
| Chat、Responses 用量里的 `is_byok` 和为正的 `cache_discount` | OpenRouter 的用量字段：`is_byok` 由服务这个回答的条目是否为账户自带的 key 决定（`Final.IsBYOK`），不是请求里写的；缓存折扣按锁定的输入价算，见「用量与费用」 |
| Responses 从 Messages 上游转换时，用量里的 `input_tokens`、`total_tokens` 是物理总量（含缓存读），`output_tokens` 用 Responses 的名字；new-api 写 Anthropic 的 `input_tokens`（不含缓存读）和 Chat 名字的输出 | Axiom 的用量口径：输入保留物理总量，缓存读写可重叠（[catalog 的「计费项与用量」](catalog.md#计费项与用量)），见 protocol README 的「用量」 |
| 从 Messages 上游转换的推理摘要文本不带 new-api 多加的结尾换行 | 摘要文本是上游的字节，转换不增删内容 |
| 转换出的工具参数保留上游的 JSON 原文，new-api 重新编码 | 参数是客户端要交回去的字节，改动可能改变它的语义（数字精度、重复键） |
| 上游没有报用量时（Messages 的思考样本），两边各自估算 token 数 | 估算口径不同，见[计费的「用量来源与暂估」](billing.md#用量来源与暂估) |

### 版本与兼容

- OpenAI、Anthropic 格式跟随它们的官方规范。
- 我们自己的扩展只增不改。
- 确实需要不兼容的修改时，使用新的版本路径；旧版本经过公告期后才下线，公告期在发布时说明。
- 管理接口（控制台、管理后台、运营）用 OpenAPI 契约，版本规则相同。

## 决策与理由

| 决策 | 理由 |
|---|---|
| 请求准备只为竞争方额度可覆盖的已知峰值独占重试一次 | [#768](https://github.com/TYCT-tyct/Axiom/issues/768)：逐项扣额度会互相瓜分。比较一次预留峰值、全部串行与共享首试加条件重试，选第三种，保留实际申请和充足预算下的并行，避免粗上界扩大长文本预留、精确预估复制转换逻辑。只在另有准备占用额度且失败的已知峰值可由退回这些额度覆盖时排写闸，形状预留超过总预算的请求直接 503；成功输出不计入可回收份额。用短锁保持失败快照与份额一致，普通读体和中继的原子记账不变。代价是最多两次本地准备，并可能排在其他合格重试和读者之后；等待人数受节点在途名额约束，但无固定等待时间，断开和超时不能取消等闸。保留现有接口及错误契约，接受这一仅涉及本地计算的限制，不承诺可取消或排队期限；不跨外部 I/O，不改费用规则。 |
| 搜索后端用自托管 SearXNG 加付费 Firecrawl，不选 Tavily、Parallel | 按 [#309 授权](https://github.com/TYCT-tyct/Axiom/issues/309#issuecomment-6478816797)「遇到问题自己解决问题和判断最优而不是问我。」落地 #311。Firecrawl 按「每次搜索 + 每条结果」计价（$0.005 + $0.0125，2026-11-01 核对其搜索文档），正好对应两个成本字段，HTTPS JSON、Bearer 密钥；2026-11-01 从部署节点核对 `api.firecrawl.dev` 根路径 200、`/v2/search` 对 GET 返回 405（端点存在且只收 POST）。SearXNG 可以自建在中国大陆，Firecrawl 作为公网付费后备；大陆侧的直连没有在本机验证，部署时用后台的测试按钮确认。Tavily 按请求扣 credit、不按结果，装不进两个成本字段；Parallel 的公开定价与国内可达性无法在不注册的情况下确认，不选。 |
| 每个后端有自己的发布价，报价时替换该条目锁定的 `web_search` 单价；搜索价为 0 沿用模型价 | 成本乘（1 + 加价比例）用 `platform.UnitPrice` 精确计算、向上取整，和模型售价同一套钱的表示；价格和版本存在后端行，预留锁定的就是换过价的售价表，结算按同一份表收费，不会出现预留一个价、结算另一个价。不另建一套引擎价目表：模型售价表已经按条目锁定，换一个单价就够。0 表示沿用模型价，让旧的单后端和环境变量部署行为不变。 |
| 原生搜索按目录的 `web_search` 能力和目标格式判断，不按厂商白名单；插件最多开 10 次 | 端点能不能原生搜索是目录数据，OpenRouter 文档列的 OpenAI、Anthropic、Google、Perplexity、xAI 只是它当时的代理范围，写死会随代理变化而误判。10 次是一次插件请求的花钱上限；Chat 的 `web_search_options` 没有次数成员，预留按 1 次、按上游报告的实际次数结算，超出部分按余额底线截断。 |
| 模型与价格页复用路由构建的投影，按受众取售价，逐个列出分组代号和端点价，公开接口不含供应商与成本 | 依据 [#309 授权](https://github.com/TYCT-tyct/Axiom/issues/309#issuecomment-6478816797)「遇到问题自己解决问题和判断最优而不是问我。」落地 #317、#559 D3/U5。对比逐请求读目录与售价、构建时投影，选择后者：页面价格和调用扣费来自同一份 `ModelList` 发布，不会出现页面一个价、扣费另一个价；不选把 OpenRouter 发现字段整份复用，因为其中的供应商和成本会泄露货源（端点代号本身现在是公开身份，见下一行）；价格用 `ScheduleView` 保留分档、服务等级和按张/按秒等单位，不选折成每百万 token 的近似值，否则页面会对按次或分档模型给出错误价格。端点的公开身份直接用持久分组代号：它已经是路由、`provider.only`/`ignore` 和平台预设供应商顺序使用的值，页面显示与路由接受的是同一个，不需要第二套编号；代号在分组首次选用时随机分配、之后不变，端点集合变化不重编号，也不从供应商数据派生。模型级价格仍取最便宜端点，端点表逐项给各自售价。官网按站点默认等级、控制台按账户当前等级取价，未登录看到的就是注册后付的价（站点关闭价格公开且生效时除外，页面显示登录后可见）；旧节点没有 `endpoints` 时页面显示端点暂不可用。价格公开由站点设置 `model_prices_public` 控制（`accounts.site_settings`，NULL 默认公开）：关闭时匿名投影省略模型和端点的 `pricing`，登录账户的投影不变。匿名接口不认会话，带 cookie 也按站点默认等级回答，因此一个答案对所有访客成立，不会因缓存或共享链接把登录价给未登录的人；OpenRouter 的公开模型接口无需 key 就返回价格（<https://openrouter.ai/api/v1/models>，2026-11-02 核对），默认公开与基线一致。滚动发布由节点心跳的能力声明和 `platform.model_prices_public_readiness` 控制：还有旧节点时匿名投影继续公开价格、管理接口拒绝关闭，旧节点退出后在下一次心跳和快照刷新时生效；旧节点重新加入则回退公开。新页面核对 PUT 响应，旧节点忽略字段时提示未保存。 |
| E4-2/P13 的速率与预算只投影已有策略和计数，不增加预算状态 | 依据 [#309 授权](https://github.com/TYCT-tyct/Axiom/issues/309#issuecomment-6478816797)「遇到问题自己解决问题和判断最优而不是问我。」落地。对比复制 OpenRouter 的组织/护栏对象与复用本站工作区/key，选择后者：没有对应对象时不造 ID，不引入第二份资金事实。remaining 扣预留，effective 按整数比较后才转显示单位，保持与准入检查一致 |
| 只给请求速率 429 发窗口额度头，Reset 给保守清空时刻，Retry-After 保留 60 秒上限 | 并发/排队不是请求窗口，不伪造 Limit；滑动近似没有固定桶的精确重置时刻，两个窗口的上界不冒充剩余精确秒数；沿用既有客户端兼容的 60 秒 Retry-After 上限，有意差异和基线见上文 |
| 推理开关、展示和强度分开，隐藏在计量之后执行 | [OpenRouter Reasoning Tokens](https://openrouter.ai/docs/guides/best-practices/reasoning-tokens)（2026-10-29）规定 exclude 不减费用、预算与 effort 可换算；隐藏明文仍保留工具续聊必需的签名/密文，不以显示要求推断启用模式；[Anthropic Thinking](https://platform.claude.com/docs/en/build-with-claude/thinking) 的 omitted 保留签名且收费，[OpenAI Reasoning models](https://developers.openai.com/api/docs/guides/reasoning) 当前包含 minimal/xhigh/max。复用规范请求、UsageContext 与现有流读取器，不另建协议树；相比只信上游隐藏，在本地计量后过滤能防忽略隐藏的端点漏出明文。无法精确表达预算时按公开比例取最近档，同距取低，不承诺近似是硬上限；不按模型名猜 supported_efforts/mandatory。换算表在 protocol README。依负责人在 [#309 的授权](https://github.com/TYCT-tyct/Axiom/issues/309#issuecomment-6478816797)「遇到问题自己解决问题和判断最优而不是问我。」执行，独立评审由流水线安排 |
| Chat 旧客户端的回复恢复旧函数表示，现代客户端保持工具数组 | [OpenAI Chat reference](https://developers.openai.com/api/reference/resources/chat/subresources/completions/methods/create) 明确 `functions` → `tools`、`function_call` → `tool_choice`，消息中的旧调用由 `tool_calls` 取代。[OpenRouter 的工具规范化说明](https://openrouter.ai/blog/insights/agent-frameworks-compared-tool-calling-schema-handling/)和 [API reference](https://openrouter.ai/docs/api/reference/overview) 只承诺现代 `tool_calls`，未说明旧请求的回映射（2026-10-29 核实，未花费调用实测）。本任务要求兼容旧客户端，因此有意增加只针对旧请求的回映射，并在有工具声明时关闭并行；不采用对所有回复统一降级，因为会破坏现代客户端和并行调用。既有稳定 ID 配对逻辑同时用于同格式与转换；不重编码整份请求，以保留工具参数、签名和供应商扩展。此对外兼容决策依负责人在 [#309 的授权](https://github.com/TYCT-tyct/Axiom/issues/309#issuecomment-6478816797)「遇到问题自己解决问题和判断最优而不是问我。」执行，仍需独立评审 |
| 模型发现复用路由构建，端点 tag 为持久分组代号，公开 provider 为 Axiom | 按 [#309 授权](https://github.com/TYCT-tyct/Axiom/issues/309#issuecomment-6478816797)「遇到问题自己解决问题和判断最优而不是问我。」落实 #311 P5/P10、#559 D3/U5。比较逐请求读 suppliers 与构建时投影，选择后者：一份响应的价格、端点和计数一致，构建失败保留整份旧投影；不选把全部端点合成一个，因为产品要求逐端点列出，也不公开供应商名字与政策猜测。每个端点的 `pricing` 取它自己的售价，模型 `pricing` 取这些端点里最便宜的一个；价格版本和端点集合都来自同一次构建。共享投影在分组前排除非零 Owner，未来自带端点不能使别的账户看出其存在。代号选持久分组代号而不是模型内序号：序号只在单个模型内唯一，端点集合一变就可能重新编号，路由、`provider.only`/`ignore` 和平台预设的供应商顺序都不认它，客户端拿到 tag 也不能用来指定端点；持久代号在分组首次选用时随机分配，数据库唯一且触发器禁止修改，同一分组跨模型相同，既是不泄露供应商的公开名字，也是路由已有的标识。官方直连 API 接入后，同一字段用真实名称，`provider_name` 用官方供应商名。保留实时准入，避免缓存身份误放行已撤销的 key；独立评审仍由流水线执行 |
| 响应缓存按账户隔离，不按 API key | 卡片的验收写的是「keyed per account（never shared across accounts）」，比 OpenRouter 的按 key 更严：同一账户的 key 轮换后仍能命中，账户管理员本来就能看到该账户的调用；不同账户的哈希都带账户 ID 且查询受 RLS 约束，不可能共用。代价：一个 key 写入的缓存，同账户的另一个 key 能读到；因此条目保存服务模型与端点，命中前按当前 key 的准入路由复核，权限不同或收窄时不重放 |
| 缓存存上游原始回复，命中时重放进 `protocol.Relay` | 直接存客户端回复要为三种格式、流式/非流式、聚合和每块的 id/usage 各写一套重放；存上游字节让现有 relay 做格式转换、推理隐藏、结束事件和用量清零，命中与正常回复走同一条经过测试的路径。新调用 ID、`created`、`X-Generation-Id` 由 relay 的选项给出，`X-Codex-Turn-State` 与正文一起放在同一份加密载荷里。代价：条目与上游格式绑定，格式编号变化或 relay 不再支持该组合时按未命中处理并删除 |
| 命中仍走正常准入和预留，结算时全额释放 | 钱和并发的不变量（I1/I3）都在准入与结算里；在准入前查缓存能省一次预留，但要另开一条不预留、不结算、不写调用记录的路径，余额不足的账户会因此绕开准入。选择保持一条路径：命中免费、账本不扣、调用记录写命中，但账户仍需先有这次调用的预留额度。代价是低余额账户可能明明有缓存也拿不到 |
| 响应缓存默认关闭，且先由平台 `AXIOM_RESPONSE_CACHE_CONTENT` 放行，再只由请求头或命名预设打开，TTL 1–86400 秒、条目 1 MiB、账户 64 MiB/1024 条 | 缓存保存对话正文；`docs/product.md` 把内容保存交给平台统一决定，所以先用平台开关默认关闭，避免请求自己绕过产品决定。OpenRouter 的默认 TTL 是 300 秒、上限 86400，照它让客户端可预期。1 MiB 覆盖正常的整条回复，64 MiB/1024 条把单账户最坏占用限住；超出按最旧删除，过期每分钟清理。不选无限 TTL 或按磁盘配额，那会把内容保留交给运维而不是请求 |
| 正文信封加密、按账户 RLS、AAD 绑表行和用途 | 与供应商凭证、SMTP 密钥同一套 `secret.Keyring`；AAD 让一行密文不能被搬到另一行或另一个账户下解开，RLS 让查询在数据库层就看不到别的账户。缓存是临时内容，仍按 `docs/security.md` 的租户隔离和静态加密要求处理；密钥轮换把 `gateway.response_cache` 登记进 `keyRotation` 并加 fence 触发器 |
| 只缓存生成格式的完整成功回复，不缓存直连格式、插件和服务端工具路径 | 这些路径的正文之外还有文件解析、搜索、历史或上游资源引用，重放会漏掉按次费用或让另一请求读到不属于它的上下文；直连格式还要另写一条不经 relay 的重放。先只做 Chat、Responses、Messages 的普通调用，范围小、每条路径都能用同一套 relay 测试；Embeddings 等有需求再加 |
| 同时提供 `/v1` 和 `/api/v1` 路径 | 负责人同意。OpenRouter 的客户端只改地址就能接入 |
| 请求头按白名单转发，`anthropic-beta` 不筛选具体的值 | Claude Code 官方的网关兼容指南明确要求原样转发协议头；客户端的凭证和身份信息不能流向上游 |
| 上游 4xx 保留原文，只抹掉供应商身份 | Claude Code 依赖上游错误的原话自动恢复；同时不能暴露货源。负责人 2026-10-18：影响兼容的行为照 new-api、sub2api。实现见「错误」：适配器仍只读代码、不留文字，共用的调用代码在拒绝请求的失败上另存一份清洗过的 `Rejection`，只给这次回复用。不选让适配器带原文：每个适配器各清洗一遍，漏一个就泄露 |
| Chat（含流开始前的 Responses）的 `error.code` 是与状态行相同的数字，上游的字符串 code 放 `metadata.provider_code` | [产品需求](../product.md)第 92 行要求「与 OpenRouter 一致的错误码」；2026-11-02 核对 [OpenRouter Errors and Debugging](https://openrouter.ai/docs/api_reference/errors-and-debugging)，它的 `error.code` 就是与状态行相同的数字，正文和状态行不会给出两个事实。原先按字符串 code 分支的客户端要改读 `error.type`（状态表里的通用类型）或 `metadata.error_type`（精确分类），上游自己的 code 在 `metadata.provider_code`；按状态行或 `type` 分支的客户端不用改。不选保留字符串再加一个数字字段：两个都叫 `code` 的字段会互相矛盾，也仍然不符合产品要求。账户暂停归 `permission_denied`。批准：2026-10-28，https://github.com/TYCT-tyct/Axiom/issues/309#issuecomment-6478816797 |
| Chat 回复的推理文本同时写 `reasoning` 和 `reasoning_content` | 负责人 2026-10-18：照 new-api 写 `reasoning_content`。两个名字都有人用：`reasoning_content` 是 DeepSeek、new-api、LiteLLM 的（LiteLLM 文档《Reasoning Content》），`reasoning` 是 OpenRouter 和新版 vLLM 的（OpenRouter《Reasoning Tokens》，vLLM《Reasoning Outputs》，2026-10-19）；读一个的客户端对另一个当作未知字段忽略。同文并存，客户端读哪个都对；只留一个就会让另一半客户端读不到推理。已知风险：若有客户端把两个字段都显示，推理会出现两遍，没有实测过，发现再收回其中一个。代价是推理文本多发一份 |
| 变体在网关里读，目录和路由都不认识后缀 | 变体是路由开关，不是模型：放进目录会让一个模型分裂成多个条目，也要放宽目录"带 `:` 的名字永远查不到"的不变量，那条不变量正是为了保证带后缀的写法落不到基础模型。读法集中在 `variant.go`，调用、备选和 `count_tokens` 共用，不会各自读出不同的结果 |
| 已知且不能兑现的目录变体回 400，未知后缀仍 404 | 选择在网关解释并拒绝，而不放宽目录约束或将后缀剥掉调用基础模型；后者可能收费或丢失推理约束。OpenRouter 的缺失目录条目 404 是其具有变体目录时的行为，本站因功能不可用主动回 400，是本任务要求的有意偏离。错误要让客户端区分功能尚不可用与模型下线 |
| `:exacto` 只路由到质量验证过的端点 | 目录里没有 Exacto 的工具调用成功率和基准评分，但真伪检测已经能给出「这个端点自称的模型是假的吗」；把「按未测量的 Exacto 信号排序」降级成「质量优先且只信验证过的端点」，比继续 400 更接近用户想要的，也不假装有基准分。没有已验证端点时失败关闭（503 `not_verified`），不回落到未验证端点。 |
| 显式 `provider.sort` 压过变体 | exacto 页明说显式排序覆盖它；请求体里的字段比名字里的后缀更具体，nitro 和 floor 照此处理。代价：写了 `:nitro` 又写 `sort: price` 的客户端得到按价格排序，与 OpenRouter 的实际行为未核对（页面没写） |
| `openrouter:web_search` 跑模型侧循环，能原生时映射成厂商工具，其余服务端工具继续拒绝 | 按 [#309 授权](https://github.com/TYCT-tyct/Axiom/issues/309#issuecomment-6478816797)「遇到问题自己解决问题和判断最优而不是问我。」实现 #311 P2。OpenRouter 的逐工具页、new-api 和 sub2api 对联网搜索的行为一致（都把它当一次按次服务），所以照参考实现做；`engine`、默认后端、后端价格和 `web` 插件复用同一套，避免两条搜索路径给出两个价。每次采样是一次尝试（同一条目、同一预留、计量跨步累计），因为每次采样是一次独立的供应商请求和一笔独立的扣费：回执、成本、RPM 和凭证槽都按尝试走现有代码，对账键一笔对一笔；比较过的方案是一次尝试下挂多跳，回执和成本只能合在一起，要另建逐跳明细表和第二套对账路径（PR #543 的前几轮）。`attempt_seq` 的 CHECK 只放宽到 33（suppliers 00024、observability 00016），主键不变，旧节点写入的序号仍在范围内，滚动发布安全；输出预留按 1 + 步数乘每次上限（`Repeats`），否则每次采样都可能生成到 `max_tokens`，原预留罩不住（I1）。原生路径不跑循环，省掉中转延迟和按次费，和 #542 的插件规则一致。其余 12 个工具没有执行环境、沙箱或计费映射，继续拒绝；`stop_server_tools_when` 没有对应的条件语言，显式写就拒绝而不是跑一个停不下来的循环。流式把每跳留在网关，客户端只看到最终回答，是这条路径的代价：要区分工具调用和回答必须读完这一跳，换来一条连贯的流和不泄露中间结果 |
| 模型名变体逐项定行为：能兑现的兑现，不能的带原因拒绝 | 按 [#309 授权](https://github.com/TYCT-tyct/Axiom/issues/309#issuecomment-6478816797)「遇到问题自己解决问题和判断最优而不是问我。」实现 #311 P3。2026-10-29 核对官方页面：`:nitro`、`:floor` 排序，`:exacto` 质量信号，`:thinking` 已弃用，`:online` 等于 web 插件，`:free`、`:batch` 独立目录条目，`:extended` 已下线，`~…-latest` 要模型族映射。能在本站兑现的是排序、`:free` 的独立目录条目、别名表和「请求已要求推理时的 `:thinking`」；`:exacto` 缺工具成功率与基准，`:batch`、`:extended` 没有条目和价格，所以各自说明原因后拒绝，不静默剥掉后缀。`:thinking` 的接受条件放在 `readRouting`，发现接口不执行请求级判断；`:online` 改说搜索服务而不是「暂不支持」 |
| `:free` 作为目录里的独立模型，不做名称改写 | OpenRouter 的免费版有自己的上下文、价格和端点，改写付费名字会把付费价带进免费请求或反过来；独立条目让发现、上架门槛、零价校验和路由都复用现有路径，没有条目时返回 404 而不是偷偷用付费模型 | 解析时去掉 `:free` 或加 `free_of` 列：要另写发现、价格和路由，且无法表达免费版更小的上下文 |
| `~…-latest` 用目录别名表，显式目标、只追加 | 只有显式映射能保证不串模型族、发现与调用用同一个目标、切换有审计；按日期或前缀猜最新会在新模型上架时悄悄改掉用户实际调用和计费的模型。别名目标必须同作者且已上架，目标下线时解析为“已下线” | 按名字前缀挑最新；把别名做成 `name` 表的可变行：前者会串族，后者要 UPDATE 权限且旧快照与 `change` 对不上 |
| `openrouter/auto` 用现有路由排序代替 OpenRouter 的分类器 | OpenRouter 按任务分类、花费份额和成本档选模型；本站没有分类器所需的标注数据和跨租户花费份额，硬编一个会把没有证据的偏好说成“智能”。把请求的排序作用到全部可准入模型上，选择可解释、可测试，也复用端点过滤、熔断和计价；选中后回显具体模型，客户端能知道实际用了谁 | 新写一个 auto 打分器或固定挑最便宜：前者没有数据支撑、还要新维护一套权重，后者忽略 `:nitro` 和用户排序；两者都绕开现有路由的准入与回退 |
| `openrouter/free` 用现有路由排序在免费条目里选，不随机 | OpenRouter 从免费模型里随机选；随机结果不可解释，测试只能靠碰运气，`:nitro`、`:floor` 和 key 的路由预设策略也无从生效。免费条目本身已经是目录数据（`:free` 独立模型），把请求排序作用到它们上面复用 auto 的同一套过滤、熔断、回退和计价，选择确定、可测试，响应仍回显具体模型 | 新加一个随机策略或按模型 ID 轮询：前者让同一请求的结果不可复现、排除原因无法解释，后者忽略用户排序和端点质量，也绕开路由的重试预算 |
| 会话、用户标识换成派生值，而不是删除 | 删除会让上游缓存失效；原样转发会串租户、泄露标识 |
| 响应头按白名单放行 | 上游的请求 ID、组织名等头会暴露货源 |
| 开始转发内容之前不发保活 | 此时还可能换端点；提前发出任何字节，状态码就无法再改 |
| 余额 402、工作区预算和 key 上限 403，按来源区分，只有暂时性的带 `Retry-After` | 与 OpenRouter 一致（它的预算错误是 403，`metadata.limit_source` 为 `openrouter_workspace_budget` 或 `openrouter_key_limit`；账户余额不足是 402，2026-11-02 核对）；客户端据此判断该等待重试还是去充值。套餐窗口不够同样是 402，`limit_source` 为 `subscription`；OpenRouter 没有套餐，这是本站自己的取值（见[计费的「资金来源」](billing.md#资金来源)） |
| 计费类问题不返回 401，避开自动禁用措辞，并写成测试 | 把我们当上游的 new-api 收到 401 或特定措辞会自动禁用渠道，临时问题会被放大成停用 |
| 生成查询补原生 token、流式/取消/会话，内容接口不保存内容；活动按日与模型汇总 | 按 [#309 授权](https://github.com/TYCT-tyct/Axiom/issues/309#issuecomment-6478816797)「遇到问题自己解决问题和判断最优而不是问我。」落实 #319 P10/P11。对照 [OpenRouter 生成查询](https://openrouter.ai/docs/api/api-reference/generations/get-request-&-usage-metadata-for-a-generation)、[内容](https://openrouter.ai/docs/api/api-reference/generations/get-stored-prompt-completion-and-error-content-for-a-generation)、[活动](https://openrouter.ai/docs/api/api-reference/analytics/get-user-activity-grouped-by-endpoint) 和[官方 OpenAPI](https://openrouter.ai/docs/openapi/openapi.yaml)（2026-10-30）。原生 prompt 取调用写入时已存的物理输入（迁移 `00012_streamed.sql`），不从缓存读写反推，避免把重叠的 token 相加；内容接口对已有调用也返回 404 `content_not_available`，因为 observability 有意不保存对话内容，新增留存面需要单独的产品决定；活动只算最近 30 个完整 UTC 日，`endpoint_id` 写零、`provider_name` 写 Axiom，避免暴露端点与货源，`api_key_hash` 暂按 key UUID 接受而官方是 SHA-256（keys API 未暴露该哈希），这是已记下的偏离。独立评审仍由流水线执行 |
| 响应 `model` 写实际服务的模型，用量只暴露用户售价 | #319 P11 与 OpenRouter 的 [model fallbacks](https://openrouter.ai/docs/guides/routing/model-fallbacks) 和 [Usage Accounting](https://openrouter.ai/docs/cookbook/administration/usage-accounting)（2026-10-30）：`models` 回退后回显请求名会让客户端以为原模型回答，故 `RelayOptions.ActualModel` 由 routing 解析后的条目给出；`cost_details.server_tool_cost` 只写按次工具售价，`cache_discount` 按锁定输入价与缓存读价之差计算，均不含供应商成本。`is_byok` 由实际服务的条目给出（账户自带的 key 为 `true`），见[账户](accounts.md#自带的供应商-keybyok)。独立评审仍由流水线执行 |
| `web` 插件走后台配置的搜索后端（SearXNG 或 Firecrawl，可配多个），默认关闭；#311 P1 当时只有 SearXNG（后来 Firecrawl 与原生搜索已取代下面标明的几处） | 按 [#309 授权](https://github.com/TYCT-tyct/Axiom/issues/309#issuecomment-6478816797)「遇到问题自己解决问题和判断最优而不是问我。」落地 #311 P1。负责人自己配付费服务，所以默认关闭、不假设任何外部服务存在；当时选择自建 SearXNG 的 JSON API 作为唯一适配器：它可自托管、无需付费密钥，JSON 结果是稳定的接口，接口 `websearch.Searcher` 让以后换服务只改一个实现。对比直接接 Exa/Firecrawl 等付费服务：那要负责人先选供应商、接受数据保留条款并持有密钥，代码不能代替这些决定。URL 和密钥存进 `platform.search_config`（信封加密，与 SMTP 同一套），改完要求重启，和邮件配置一致；环境变量仍可作初始值，数据库没有记录时由它决定。一次插件请求只搜索一次（`max_uses` 恒为 1），按 `web_search` 售价收一次，预留按 `WebSearchMaxBytes` 覆盖注入的输入上界，搜索失败在发起模型调用前结束、不收模型用量。注入格式照 OpenRouter 博客的 system 消息，非流式 Chat 才补 `url_citation`；流式拿不到整条消息，客户端应读正文里的链接。查询不写日志，结果有大小上限。后来的变化：客户端格式：`web` 插件仍只接受 Chat 请求（Responses、Messages 请求带它回 400 `not supported`，`internal/protocol/plugins.go` 的 `applyWeb`），`openrouter:web_search` 工具认 Chat、Responses、Messages 三种客户端格式（见「插件与提示词变换」和「服务端工具」）；两者按路由选中的上游格式都可以用三种格式的厂商原生搜索；后端可以是 SearXNG 或 Firecrawl（`internal/websearch/engines.go`，名字在后台登记），`engine` 写后端名或 `native`，省略时模型有原生搜索就走原生，否则走默认后端（`internal/gateway/admit_search.go` 的 `chooseSearchFor`），未知后端回 400，`mode` 仍明确拒绝。现在的代价：`include_domains` 依赖引擎是否理解 `site:` 操作符；原生搜索与后端搜索一次请求只走一条路径 |
| `file-parser` 的 OCR 引擎走一个可配置的 Docling serve 后端，后台存 URL 与密钥，默认关闭 | 按 [#309 授权](https://github.com/TYCT-tyct/Axiom/issues/309#issuecomment-6478816797)「遇到问题自己解决问题和判断最优而不是问我。」落地 #311 P1。对照 [OpenRouter 的 File Parser 插件](https://openrouter.ai/docs/guides/features/plugins/file-parser)（2026-11-01）：引擎名、`pdf-text` 的弃用重定向、文件注解的形状和 `hash` 复用语义照它，执行换成自托管。选 Docling（`docling-serve` 的 v1 REST API）：MIT 许可、可自托管、CPU 能跑、接口稳定、中国大陆部署只受模型下载影响，模型可以预装或用镜像站；对比 Mistral OCR（要付费账户和密钥，文件出境）、Cloudflare Workers AI（同样要账户，且绑定区域）、Apache Tika（没有 OCR，扫描件只有乱码）、MinerU（要 GPU，重）、Marker（许可与商用限制）、unstructured（依赖更重、容器更大）。对比直接接 OpenRouter 一样的两个付费服务：那要负责人先选供应商、接受数据保留条款并持有密钥，代码不能代替这些决定。两个引擎名落到同一个后端，因为本站没有那两家的账户；`native` 不需要后端，所以默认引擎是 `native`：没写引擎就发起 OCR 会在报价里多一笔客户端没要求的费用，而路由之前又不知道模型能否读文件，OpenRouter 的「原生不行就 OCR」回退在这里做不到，也不该由网关替客户端花钱。解析在准入与报价之前，替换后的正文就是报价和转发的正文；开关打开时按页收 `file_parse`（`mistral-ocr` 每页一次，`cloudflare-ai` 免费，`native` 按输入 token），价格只来自模型自己的价目表，没有站点默认价（`billing.fallbacks` 的每一行都必须带齐已知的按次项，滚动发布期间新增项会让旧节点写不进结算，见 billing 的「预留与结算」与「决策与理由」）。注解照 OpenRouter 的 `{"type":"file","file":{"hash","name","content":[…]}}` 放在非流式 Chat 的 `choices[].message.annotations`；`hash` 用解码后字节的 SHA-256，客户端伪造注解只能换掉自己的文本，且受 2 MiB 上限约束。解析前先做一次只读的 key 检查并按速率窗口拒绝：否则停用或超速的 key 仍能让网关先做 OCR（一次最长 60 秒、最多 20 MiB），再在准入被拒，破坏「不能用的 key 不产生后端开销」。代价：流式回复不返回注解、`file_id` 引用的文件不解析、只认内联 base64、工具结果与 URL 文档要求解析时 400 |
| 文件解析按页计：`mistral-ocr` 用独立的 `file_parse` 项，`cloudflare-ai` 免费，`native` 按输入 token；`Config.FileParse` 关闭时保留旧 `web_search` | 按 [OpenRouter File Parser 的价](https://openrouter.ai/docs/guides/overview/multimodal/pdfs)（`mistral-ocr` `$2 / 1,000` 页，美国区 $2.2；`cloudflare-ai` 免费；`native` 按输入 token，2026-11-02 查）定价。不借用 `web_search`：那是搜索的价，出处不支持“解析算一次搜索”。PDF 请求同时要 `json`，页数取 Docling 的 `json_content.pages` 条目数，空页也计入，和 OpenRouter 按 PDF 物理页一致；非 PDF 没有 PDF 页树，才退回 Docling 的 `md_page_break_placeholder`，PDF 页数字典缺失时按上游失败拒绝。价格只来自模型价目表，不新增 `billing.fallbacks` 的站点默认项。`catalog.Config.FileParse` 同时管发布和网关计费：这一版在所有生产节点关闭，网关仍按旧规则每个文件一次 `web_search`，已有 OCR 站点升级后不会因为新项未发布而失败；打开前要先让全部节点读得懂这一项，并把已有模型的 `file_parse` 价补齐。详细取舍见计费的「决策与理由」 |
| 本地插件在准入前压缩、在聚合后修复 | 按 [#309 授权](https://github.com/TYCT-tyct/Axiom/issues/309#issuecomment-6478816797)「遇到问题自己解决问题和判断最优而不是问我。」实现 #311 P1/E2-7。比较在上游执行与本站执行，选择纯本地：避免上游静默忽略；比较按端点重复压缩与准入前按主模型压缩，选后者让路由、报价、用量暂估和发送正文一致，不改计费契约，不引入依赖或外部服务。默认压缩作为可选优化失败即保留原请求，显式开启才执行严格的字节预算，避免以预留上界缩小默认请求的可服务范围。代价和偏离见「插件与提示词变换」；独立评审由流水线安排 |
| 扩展字段放在独立的 `axiom` 对象 | 不与 OpenRouter 的字段冲突 |
| 提供 OpenAI 旧版和 OpenRouter 格式的余额接口 | 第一版范围要求支持其他中转站接入（见产品需求）。new-api 查询 OpenAI 类型渠道时用前者；查询 OpenRouter 渠道时写死访问 openrouter.ai，所以后者只服务兼容 OpenRouter 的工具 |
| `/api/v1/credits`、`/api/v1/key` 接受调用 key 和管理 key；其余辅助接口只接受调用 key | [产品需求](../product.md)第 92 行要求与 OpenRouter 一致；2026-11-02 核对 OpenRouter 的[余额接口](https://openrouter.ai/docs/api/api-reference/credits/get-remaining-credits)要管理 key，[当前 key 信息](https://openrouter.ai/docs/api/api-reference/api-keys/get-current-api-key)也回报管理 key 类型。本站同时保留调用 key：其他中转站常用调用 key 查余额和用量，这是产品要求的对接方式。管理 key 仍不能发起模型调用（模型调用只经 `Admit`/`AdmitBatch`，只收 `call`），`/key` 的 `is_management_key` 按实际种类写出，`generation`、`activity`、两个旧版余额接口和 `count_tokens` 仍只收调用 key；读的是它所在账户的数据，作用域与调用 key 相同。批准：2026-10-28，https://github.com/TYCT-tyct/Axiom/issues/309#issuecomment-6478816797 |
| `count_tokens` 是本站的估算，不转发上游 | 开发者按负责人的授权决定（对话中的全权委托）。Anthropic 的接口返回精确值；转发要占用端点名额，且需要走路由。文本和图片的估算偏高，客户端据此裁剪只会偏早；文件按固定页数估，页数多的文件会被低估，所以不承诺不超窗口。口径见[模块 README 的「辅助接口」](../../internal/gateway/README.md#辅助接口) |
| Responses 会话状态由我们保存，偏离 OpenRouter | 负责人同意。OpenRouter 的 Responses 接口不保存状态，`store: true` 和 `previous_response_id` 都会返回 400（OpenRouter《Responses API》，2026-10）；支持续接能让 Codex 少发重复内容、省钱。会话状态单独保存，内容保存关闭期间续接也可用 |
| 会话状态按工作区隔离，单独保存 | 同一账户的不同工作区可能属于不同项目；负责人授权开发者按用户体验判断，单独保存让对话内容关闭期间续接仍然可用 |
| 辅助请求走便宜端点作为平台预设的可选规则，默认关闭 | 负责人同意。见[路由](routing.md#排序的先后) |
| 没有可用端点：永久性的返回 404，暂时性的返回 503，满载返回 429 | 开发者按负责人的授权决定（对话中的全权委托）。CONTRIBUTING「产品设计」：行为默认对齐 OpenRouter，它对过滤后没有端点返回 404 "No endpoints found…"（文档与帮助中心，2026-09；价格页 FAQ 的模型下线同为 404，2026-10）；客户端会自动重试 5xx，而永久性的条件重试也不会变。暂时性的保留 503 |
| Messages 的非流式请求中途失败返回 502、不带内容，用量和费用放在 `axiom` 对象里 | 开发者按负责人的授权决定（对话中的全权委托）。通用规则是把已生成的内容和错误一起返回（上文「非流式请求出错」），但 Messages 的非流式响应没有表示出错的结束原因（`stop_reason` 只有 `end_turn`、`max_tokens`、`stop_sequence`、`tool_use`、`pause_turn`、`refusal` 等），把半截内容当成功返回会让客户端以为完整。这是按格式落实同一条规则；Chat、Responses 照规则返回内容 |
| 续接在第一版做，排在 M2；M1 对 `previous_response_id` 返回 400 | 开发者按负责人的授权决定（对话中的全权委托）。负责人同意的是第一版要有续接（本表「Responses 会话状态由我们保存」），第一版是 M0–M3，排进哪个里程碑由开发者定。M1 的验收客户端用不到：Codex 的 HTTP 传输每轮发完整历史（OpenAI《Unrolling the Codex agent loop》，2026-01-23："Codex does not use it today"），只有 WebSocket 用它，M1 不支持 WebSocket。M1 的端到端测试用录制样本验证这个前提，不成立就提前到 M1。省四个 PR，不推迟验收 |
| Chat 请求落在说 Chat 的端点上时同格式转发，端点同时说 Chat 和 Messages、Responses 时也先选 Chat | 转发不改变含义，转换会拒绝表达不了的成员（`NotSupported`，客户端得到 400），所以能不转换就不转换；new-api 的做法相同（`chat_completions_to_responses_policy` 默认关）。代价：此前走 Messages 的 Chat 请求，在同时说 Chat 的端点上改走 Chat，行为按端点的 Chat 实现 |
| Chat 端点的输出上限写在端点读的那个成员里，按上游模型名定（o1、o3、o4、GPT-5、GPT-6 Astra/Sol/Luna 用 `max_completion_tokens`，其余 `max_tokens`），客户端的两个成员都拿掉再写 | 端点忽略的成员不是预留的上界：客户端只写 `max_completion_tokens:1`、端点只认 `max_tokens`，上游就按自己的默认值生成，超出预留的费用没人付（I1）。规则抄 new-api `relaykit/dto/openai_request.go` 的 `GetOpenAIChatCapabilities`（32e5874）；它按模型名判断，规格里还没有说明这件事的键，所以和「方言是数据、不按名字猜」有出入，规格有了这个键再改用它，待定。代价：名字不在名单里的新 OpenAI 模型会收到 `max_tokens`，被拒绝时按原话返回 |
| `stream_options.include_usage` 对 Chat 端点一律强制为 `true` | 没有上游报的用量，计量只能估算，收费不准；new-api、sub2api 都强制。代价：不支持 `stream_options` 的上游会拒绝请求，new-api 对非 OpenAI 渠道会删掉它（`ConvertOpenAIRequest`），本站没有这类上游的数据，遇到再按端点加开关 |
| Chat 端点只写一个推理名字时，本站补另一个 | 「转发规则」的 Chat 回复写两个名字是负责人 2026-10-18 的决定，对 Chat 上游同样适用，否则 DeepSeek 的客户端读得到、OpenRouter 的客户端读不到；补在流的每一块里，聚合的整份回复自然两个都有 |
| 跨格式时缓存标记按 OpenRouter 转换，表达不了的丢掉，不拒绝请求 | 负责人在对话中授权开发者按推荐自行决定。CONTRIBUTING「产品设计」要求对齐 OpenRouter：它把文本块的 `cache_control` 转成 OpenAI 的 `prompt_cache_breakpoint`，TTL 不转（OpenRouter《Prompt caching》，2026-10-16）。拒绝会让带缓存标记的 Chat 请求到不了任何 Responses 端点；标记不改变模型读到的内容，收费按上游报告的实际缓存用量，丢掉只影响命中率，不会算错钱 |
| 结算写不进时，内容完整的照常结束、不带费用、带 `billing_pending`；只有截断的才报 `billing_unavailable` | 不发出没入账的费用，也不让客户端把已完成的一轮当失败重发、付两次。没选一律报 `billing_unavailable`：主库切换时，整批在途的请求会被客户端重发、重复收费 |
| 直接格式只走原生端点、整份回复 | [OpenRouter 的 Embeddings](https://openrouter.ai/docs/api/api-reference/embeddings/submit-an-embedding-request)、[Rerank](https://openrouter.ai/docs/api/api-reference/rerank/submit-a-rerank-request)（2026-10-30 核对官方 OpenAPI）都回一份 JSON，没有流；本站还没有跨格式转换这两者的映射，硬转会把表达不了的 `input`/`documents` 丢掉或猜错，所以 `NativeOnly`，没有原生端点时按「没有可用端点」报错。整份回复在读取阶段校验，失败还能换端点，不会把半份向量或半份排序当成功。此公开接口取舍依 [#309 授权](https://github.com/TYCT-tyct/Axiom/issues/309#issuecomment-6478816797)「遇到问题自己解决问题和判断最优而不是问我。」执行，仍需独立评审 |
| rerank 的 `search_units` 计入 `web_search`，不新增计费项 | OpenRouter 的 rerank 用量示例是 `{"search_units":1,"total_tokens":150}`；本站按次计价的单位只有 `web_search`，它表示“一次服务端调用”，与重排的一次搜索单位语义相同。新加一个只服务 rerank 的 item 要改价目表、售价发布和所有格式的用量映射，收益只是名字更贴切；复用现有单位让 rerank 立刻能按已发布的 web search 售价收费。代价：调用记录里它显示为 web search，文档必须写明这个映射 |
| `/completions` 只按 OpenAI 旧接口原生提供，不转成 Chat，也不把 Chat 的 `prompt` 变体加到 `/chat/completions` | [OpenAI《Create completion》](https://developers.openai.com/api/reference/resources/completions/methods/create)（2026-10-31）的 `prompt` 是字符串、字符串数组、token 数组或 token 数组的数组，回复每个 choice 是 `text` 而非 `delta`，流式与非流式同形，`stream_options.include_usage` 在流式时给最后一帧的 usage。OpenRouter 没有 `/completions` 路径：官方 [OpenAPI](https://openrouter.ai/openapi.json)（v1.0.0，2026-10-31）只有 `/chat/completions`，[API reference](https://openrouter.ai/docs/api_reference/overview.md)（同日）让客户端把 `prompt` 发到 `/chat/completions`、回 `NonChatChoice.text`。所以没有 OpenRouter 契约可对齐，基线只能是 OpenAI 的旧接口；但不采用 OpenRouter 的 `prompt` 变体，因为那要改 Chat 的请求与回复形状，而本站的 Chat 透传和转换已经稳定。也不把旧补全转成 Chat：一个 `prompt` 数组是一次请求里的多份补全，转成 `messages` 要么拆成多次上游调用（一次预留变成多次，重试与结算对不上），要么丢掉 `n`、`best_of`、`echo`、`logprobs` 的语义（`best_of > 1` 另按 400 拒绝，见下）。代价：只有 `formats` 里写了 `completions` 的端点能服务它，没有该格式的模型回 404 `no_endpoint`。此公开接口取舍依 [#309 授权](https://github.com/TYCT-tyct/Axiom/issues/309#issuecomment-6478816797)「遇到问题自己解决问题和判断最优而不是问我。」执行，仍需独立评审 |
| 非流式旧补全的 `logprobs` 按四个数组分别追加，整份 body 在结束时编码一次 | 上游每一块的 `logprobs` 是 `tokens`、`token_logprobs`、`top_logprobs`、`text_offset` 四个数组的片段，必须按到达顺序拼起来。比较过三种做法。(1) 每块把旧对象和新对象解析成成员、克隆两个数组合并、再序列化成新对象（`wip/p18-legacy-completions` 的做法）：每块要分配约两份历史，n 块累计 O(n²)；临时副本若计入 16 MiB 上限就会永久占用（130 个 1 KiB 的块在历史只有 130 KiB 时被判超限），若合并后归还又得把已分配过的峰值从账上扣回，两次修改都没修干净（[#514 的评审](https://github.com/TYCT-tyct/Axiom/pull/514)）。(2) 把条目解码成 Go 结构再编码：每个 token 一个 string、一个 map，内存是原文的数倍，还要自己保证 JSON 转义和重复键。(3) 选：每块只解析一次，把四个数组的元素按原字节追加到每个 choice 自己的四个缓冲，结束时一次写出 `{"tokens":[...],...}`。不变量：聚合保留的每个字节在缓冲扩容之前已计入 `aggregateLimit` 和节点内存预算；内容字节按 2 倍记账（一份留在聚合里，一份给最后的 body），每个 choice 的固定语法另记 128 字节、整份另记 512 字节，没有第二份临时副本，也不会为被替换的旧对象重复计数；因此任何一块超出任一预算都在分配前以 `ErrMemory` 失败，长流只要最终体积在上限内就能完成。测试：130 个 1 KiB 的块最终 body 含 130 个 token（`TestCompletionsLogprobsLongStream`）；15 个 1 MiB 的块把 16 MiB 预算用满后，再一块 1 MiB 在分配前失败（不记预算，分配不超过随后成功的 64 字节块），64 字节的块仍能写入（`TestCompletionsLogprobsBudget`，`alloctest` 子进程） |
| 旧补全的批量 `prompt` 按整批预留和暂估输入，上下文仍按最长的单个 prompt 检查 | OpenAI 的 `prompt` 数组是一次请求里的多份补全，`usage.prompt_tokens` 是整批的合计；`billing.Demand` 原来的 `min(context, total)` 会在两份各 6000 token、上下文 8192 时只预留 8192，执行 12000 的输入却没有对应的钱（[#514 的评审](https://github.com/TYCT-tyct/Axiom/pull/514)指出过）。`Demand.InputBytes` 记录最长的一份（`Inspect` 的 `MaxInputBytes`），路由和上下文检查用它；只要它非 0，`InputBound`/`InputEstimate` 就按整批总量，价格档也按整批总量取：结算按上游报告的物理输入分档，按单项分档会在批量跨档时少留一档的价差。输出按 `n × prompt 数 × MaxOutput` 一次预留，与 Chat 的 n 个 choice 同一种预留，且这类 entry 不做分段预留（`best_of > 1` 在准入前拒绝）。代价：批量跨档时预留比单份贵，结算仍按上游用量算，不会多收 |
| `completions` 追加在 `protocol.Format` 的末尾（`Rerank` 之后） | 格式编号会进同步引擎的快照（`suppliers` 的 `formats` 列与状态用名字，但 routing 的位集和测试按编号），插在中间会改变已存的 `embeddings`、`rerank` 编号，追加在末尾让已有编号不变。迁移 `00021` 把名字加进两处 CHECK，Down 在有 `completions` 行时明确 `RAISE`，不给一个裸约束错误 |
| `/responses/compact` 的回复只把 `usage` 换成 Axiom 的用量和费用、丢弃上游误带的 `axiom`，其余成员和成员字节不变；请求只做最小改写 | [产品需求](../product.md)第 92 行要求「每次调用都返回用量和费用」，compact 也是调用，所以它的 `usage` 必须是我们结算的用量和费用，而不是上游的另一套数字；除 `usage` 和本站保留的 `axiom` 外仍是上游的整份回复，客户端按上游格式读的其余成员、次序和成员字节都不动；上游误带的 `axiom` 丢弃，只出现本站的结算状态，结算写不进时是 `axiom.billing_pending: true` 且不带费用，与各格式的流一致。请求则不能原样：模型名要换成端点的名字（否则上游拒绝，或按别的模型计费），`user`、`safety_identifier`、`prompt_cache_key` 要换成派生值（上文「会话和用户标识换成派生值」），路由参数要删除；生成才用的成员（`store`、`include`、输出上限、`stream`）不加也不改，空闲超时与预算照旧。改写前先校验回复，校验和改写缓冲都在结算前完成，不合格仍可换端点。不选把费用只留在调用记录里：客户端读不到这次调用花了多少，也违背产品要求。批准：2026-10-28，https://github.com/TYCT-tyct/Axiom/issues/309#issuecomment-6478816797 |

| 旧补全调用一律流式，`best_of > 1` 在准入前以 400 拒绝 | OpenAI《Create completion》对 `best_of` 明说 “Results cannot be streamed”（本地存档 `/tmp/openai-completions.html`，2026-10-31 核对）：网关要读完整条流才能提交、计量和结算，非流式直达会绕过提交点与用量估算，另开一条非流式路径则要重做失败回退和结算。转发 `best_of > 1` 会让上游直接拒绝一个网关本可回答的请求，所以 `Inspect` 在分配前返回 `ErrInvalid`（400），`best_of` 为 1 或缺省照常通过。代价：依赖 `best_of` 服务端选优的客户端暂时用不了；要做时另开非流式调用路径，不在本 PR 里做半套 |
| 旧补全的 `suffix` 计入输入上界和最长 prompt | `suffix` 是模型实际读到的插入文本，不计就是在长 prompt 上少留上下文、少收输入。`Inspect` 把每个 prompt 的 `suffix` 字节数加进 `TextBytes`（整批总量），最长的一份加进 `MaxInputBytes`，路由的上下文检查与报价的输入上界都包含它；token 数组 prompt 的 token 按既有规则每个 4 字节估。代价：带 `suffix` 的请求比不带时多预留一点输入，结算仍按上游报告 |
| `max_tokens: 0` 保持“零输出”，不按缺省上限处理 | OpenAI 的旧补全允许 `max_tokens: 0`（配合 `echo` 和 `logprobs` 只取 prompt 的分数），它是请求明确设的上限，不是“没设”。`protocol.Request` 用 `MaxOutputSet` 把它与缺省分开，路由的 `outputLimit` 不再用端点默认值替换它，报价的 `Quotable.MaxOutput` 也为 0，预留只剩输入。零输出的正常回复是没有文字、但带结束原因的 choice：中继据 `RelayOptions.ZeroOutput` 把它当作提交点，否则网关会把这份合法回复判成空响应、换端点重试，全部端点都这样回时客户端反而拿到错误（`TestCompletionsZeroOutput`、`TestExecutorCompletionsEcho` 的空 prompt 与无 echo 两条）。代价：多一个字段穿过 routing 和 billing；其它格式不设这个字段，行为不变 |
| `echo` 回显的 prompt 是交付的文字，不是生成输出 | 一个 choice 的 `text` 拆成两个量：**可交付文字**是客户端拿到的全部文字（回显加生成），只有它决定提交点、空响应和回退，所以只回显的回复（`echo` 配 `max_tokens: 0`）是正常回答，不判空、不换端点；**计费的生成输出**只算去掉回显前缀后的文字，只有它进 `ContentBytes`、文本形状和输出暂估。两者曾被当成一个量：回显算进生成会多收输出费，扣掉回显再判空又把只回显的回复当成空响应。`echo: true` 时每个 choice 的 `text` 以 prompt 原文开头，把它算进 `ContentBytes` 会在上游未报 usage 时按 prompt 长度收输出费（300 字节的 prompt 被估成 16 个输出 token），流中断时也会把回显当成已生成。网关在第一次尝试前用 `protocol.EchoOf` 读出每个 prompt 的文本字节（token 数组按 4 字节/token 估），读之前按每份 8 字节向节点预算申请、调用结束归还：一百万个空 prompt 的正文只有约 3 MB，尺寸数组却要 8 MB，不记账就能绕过节点预算（`TestMemoryHoldsEchoPromptSizes`）；中继按 choice 跟踪已经收到的回显前缀，只把超出前缀的部分计入 `ContentBytes` 和文本形状，因此批量里尚未返回的 choice 不会扣掉已收到的生成内容，文字仍原样转发，usage 存在时仍以 usage 为准。代价：token 数组的原文无法在读取前得知，回显按 4 字节/token 估，可能少扣或多扣；有 usage 时不受影响。测试：`TestCompletionsEchoPerChoice`（无回显、只回显、只回显带 logprobs、只回显后断流、双 prompt 乱序、双 prompt 只回一份、单 prompt n=2 只回一份、两份都回、回显跨块，每条分别断言交付与提交点和生成字节、文本形状）、`TestExecutorCompletionsEcho`（流式与非流式下只回显、有上限只回显、回显加生成：断言回复交付、只有一次尝试不回退、无 usage 时输出按生成字节估）、`TestEstimatedChargesPartialEchoContent`（30 字节生成按 3 字节/token 估 10 个输出 token）、`TestCompletionsInspect`（记录 prompt 大小） |
| 旧补全的 entry 不做分段预留 | 旧补全的输出承诺是 `n × prompt 数 × MaxOutput` 一次预留（上文「决策与理由」）；按段预留只留 `min(output, segment)`，两份 prompt、n=1、`max_tokens 4096`、512 的分段只留 612 token 而不是 8192，结算时平台承担差额（评审指出）。`routing.entry` 对 `Completions` 直接令 `Segmentable=false`，仍要求端点能流式（`Streams`），结算走整份输出上限；`TestCompletionsNeverSegment` 用启用了分段的配置断言，`TestQuoteCompletionsReservesEveryChoice` 断言 2×4096 的整额 |
| 适配器推不出的格式由模型映射的 `formats` 声明，投影取并集 | 生产代码里没有任何适配器会产出 `completions`：new-api 的 `/v1/completions` 路由存在，但它的端点类型没有旧补全类型（`relaykit/types/endpoint_type.go`），OpenAI 类型既可能是 gpt-4o 也可能是旧模型，按类型猜会把所有 OpenAI 模型都标成支持旧补全；sub2api 没有这条路径。另一个做法是直接改 `supplier_models.formats`，但同步每次都会按适配器重写，改动会被覆盖且没有审计。选：`POST /api/admin/v1/suppliers/{id}/model-mappings` 的 `formats` 字段（可省略=不改、`[]`=清除），存在 `supplier_aliases.formats`，投影把它与适配器的格式取并集，写入 `supplier_models` 和 `endpoints` 的 `formats`，因此下一次同步不会覆盖；未知格式在写之前以 422 拒绝。代价：声明要等下一次同步和快照重载才生效。测试：`TestDeclaredFormatsSurviveSync`（两次同步）与 `TestDeclaredFormats`（规范化、收窄、清除、未知格式） |
## 待定问题

- 响应缓存没有按账户的数据保留设置可遵守：`docs/product.md` 说对话正文保存由平台统一决定，`docs/security.md` 的账户删除流程还没有实现。现在用「平台开关默认关闭 + 请求级开关 + 最长 24 小时 TTL + 按账户清理」代替；删除流程实现时必须把 `gateway.response_cache` 纳入，并决定是否允许账户关闭缓存。受控导出也覆盖它：它是 ≤24h 的回复副本、没有请求 ID，所以单独写成 `cache.jsonl` 和 `cache/<cache_id>`，仍受批准的时间窗和租户范围约束（见[记录的「受控导出」](observability.md#受控导出)）。在此之前 `AXIOM_RESPONSE_CACHE_CONTENT` 不应打开。
- 各字段的最终名称和结构写入接口契约时确定
- 官方供应商名称的可信映射、逐端点的采样/提供商参数清单仍缺数据；量化事实、五分钟窗口性能分位数和按请求计价已落地；按张的图片价已有目录项，但 `max_price.image` 管输入还是生成、端点按张用量怎么报还没定。每项缺的具体字段和补上后能支持什么，见上文「路由参数」偏离表的第三列。`require_parameters` 目前用逐端点、按实际调用格式生效的参数清单，清单外的参数明确 400；`max_price` 在非美元站点是否统一换成美元待定。
- 402/403 的错误体附所需金额和在途预留，`limit_source` 加上 `pending`（进行中的请求占满了余额），会重置的上限的 `Retry-After` 是窗口结束的时间：billing 的 `Insufficient` 现在只报来源和是否会重置，网关回 `balance`、`subscription`、`openrouter_workspace_budget`、`openrouter_key_limit`，会重置的上限固定 `Retry-After: 60`；随 billing 补上这些信息后改。现状：`Insufficient` 只有 `Source` 和 `Transient`，也没有“最早释放的时间”可报（`deadline` 是兜底期限，约 90 分钟）；报金额还要定币种字段和金额的显示格式，这随 `usage.cost` 在 G5 定（[gateway 模块 README](../../internal/gateway/README.md#入口与准入)）
- 准入后不再发起新尝试的窗口（默认 120 秒）与上游空闲超时（默认 180 秒）：提交前的空闲超时要超过窗口才到，所以默认值下它不触发换端点，只返回最后一次尝试的错误；要换端点，空闲超时按模型调到窗口以内。上文的限制表和错误表对窗口到期是“返回最后一次尝试的错误”还是 408 说法不一，gateway 照限制表：有过尝试返回 502，一次也没发出过才是 408
- `~作者/系列-latest` 的别名表已经落地；后续只缺后台界面：现在由 `axiom catalog save-alias` 写。发现和调用用同一次快照的目标、规格与价格，响应 `model` 是具体标准 ID。价格与能力变化仍遵循[目录的切换与迁移](catalog.md#模型的生命周期)。
- `:nitro`、`:floor` 的层级准入：为 `:floor` 准入 flex 层要决定是否改写发给上游的 `service_tier`，及其对预留和计费的影响
- Claude Code 的 Bedrock、Vertex 格式和 Gemini 原生格式的支持，以后再做
- `/embeddings/models` 与嵌入模型在 `/api/v1/models` 里的出现：目录的 `Serving` 目前只有输入能力，没有“输出模态”，而模型列表要求 `pricing.completion`，嵌入模型没有输出价。要补齐得给规格加输出模态、让列表的 `architecture` 和价格按它投影，再给 `/embeddings/models` 一份过滤视图；这是目录形状的改动，影响 `supported_parameters`、路由能力和全部模型对象，留到有第二个直接格式（或管理员需要从列表发现嵌入模型）时做。现在调用按请求里的 `model` 走，供应商端点记录声明 `embeddings` 即可路由
