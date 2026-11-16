# 接入与对外接口

客户端怎么调用 Axiom：路径、鉴权、转发、用量与费用、错误、路由参数、给其他中转站的接口。代码在 `gateway` 模块，调用步骤、期限、并发和故障处理见[模块 README](../../internal/gateway/README.md)，格式转换见 [protocol 的 README](../../internal/protocol/README.md)。调用接口沿用 OpenAI 和 Anthropic 的规范，本文只写不同之处，字段细节以接口契约为准。

## 要求

- Claude Code、Codex、OpenRouter 的客户端只改地址和 key 就能接入。
- 三种生成格式：OpenAI Chat、OpenAI Responses、Anthropic Messages（[产品需求的「范围」](../product.md#范围)）；另有 Embeddings、Rerank 和 OpenAI 旧版补全，三者只走原生端点。
- 其他中转站可以把 Axiom 当上游，用习惯的方式查余额、判断错误。
- 行为默认对齐 OpenRouter。

## 设计

### 路径与格式

| 格式 | 路径 | 主要使用者 |
|---|---|---|
| OpenAI Chat | `POST /v1/chat/completions` | 多数 SDK、Cursor、OpenRouter 客户端 |
| OpenAI Responses | `POST /v1/responses`、`/v1/responses/compact` | Codex |
| Anthropic Messages | `POST /v1/messages`、`/v1/messages/count_tokens` | Claude Code |
| Embeddings、Rerank | `POST /v1/embeddings`、`/v1/rerank` | RAG |
| 旧版补全 | `POST /v1/completions` | 旧 SDK |
| 模型列表 | `GET /v1/models`、`/v1/models/{model}` | 所有客户端 |

- **前缀**：所有路径同时以 `/v1` 和 `/api/v1` 提供，只挂在公开监听上，在控制台和网站之前截走（不经控制台的压缩和 404）；路由快照可用前节点不报就绪（[运维](operations.md#部署与节点管理)）。调用路径上的 WebSocket 升级回 400 `invalid_request`。
- **直接格式**（embeddings、rerank）：上游回一份 JSON，`stream:true` 回 400；只路由到 `formats` 写了该格式的端点，不做转换。
- **旧版补全**：只路由到 `formats` 含 `completions` 的端点，不与 Chat 互转；上游一律流式并强制 `stream_options.include_usage:true`，非流式由网关按序聚合每个 choice 的 `text` 和四个 `logprobs` 数组。`prompt` 的数组是一次请求里的多份补全：输入按整批（含 `suffix`）预留、暂估、分档，上下文按最长一份（含 `suffix`）检查，输出按 `n × prompt 数 × 上限` 一次预留、不分段（[计费](billing.md#预留与结算)）。`best_of > 1` 准入前 400；超过报价上限的 `max_tokens` 按上限发；`max_tokens: 0` 是零输出，只带结束原因的空 choice 是正常回答。`echo` 回显算交付文字（决定提交点和空响应）而不算生成输出，无用量时只按回显之后的文字暂估输出，token 数组按每 token 4 字节估。
- **格式闸**：同步写入 `embeddings`、`rerank`、`completions` 受 `AXIOM_DIRECT_FORMATS` 控制（默认 `off`，只写 messages、responses、chat），全部节点认识后才开，否则旧节点会摘掉整家供应商（[suppliers](../../internal/suppliers/README.md)）。适配器推不出的格式由管理接口模型映射的 `formats` 声明（省略不改、`[]` 清除、未知 422），与适配器格式取并集，同步不覆盖。
- **`GET /v1/models`**：OpenAI 与 Anthropic 合一的形状，只列 key 能调用、有端点的模型（权限与调用同一判断）；不能用、没端点和不存在的都回 404 `model_not_found`。Anthropic 模型另列厂商原生名（Claude Code 只认含 `claude`/`anthropic` 的 ID，启动探测 3 秒超时、跳转算失败）。单模型按调用的方式解析别名，`id` 是请求里的名字。没端点（没接供应商、缺规格或售价）的不列，满载、熔断、暂停、预算用尽不让模型少列。`/v1` 下的 `models/count`、`models/user`、`models/{author}/{slug}/endpoints` 回 404 `not_found`。响应带 `Cache-Control: no-store`。`/embeddings/models` 不提供。
- **`count_tokens`**：返回本地估算的 `{"input_tokens": n}`，不转发、不收费、不记录，只收调用 key，频率池与调用分开。
- **鉴权**：`Authorization: Bearer` 或 `x-api-key`；key 只能在所属站点登记的 API 域名上用（[API key](accounts.md#api-key)）；只凭域名和请求头认 key，认出前不读请求体，认不出立即 401 并关连接。浏览器跨域默认不允许，站点可为指定来源开放。
- **限制**：下表是代码默认值（`gateway.DefaultConfig`），各项关系启动时校验，不满足不启动。

| 限制 | 默认 | 到了之后 |
|---|---|---|
| 请求体 | 32 MiB | 413 |
| 读完请求体 | 2 分钟 | 408 |
| 端点都满载时排队 | 20 秒 | 429 |
| 等上游响应头 | 60 秒，compact 10 分钟 | 换端点 |
| 上游没有新数据（心跳、注释、重复快照不算） | 180 秒 | 转发前换端点，之后以 `timeout` 结束 |
| 准入后不再发起新尝试 | 120 秒 | 有过尝试 502，没有 408 |
| 单次调用最长 | 60 分钟 | 取消上游、结算，以 `timeout` 结束 |
| 尝试次数 | 3，共用上述期限 | 返回最后一次的错误 |

### 模型发现接口：基线、投影与偏离

基线是 OpenRouter 公开 OpenAPI；`testdata/openrouter-models/` 存官方 200 样本作契约回放，每个官方查询参数都有行为测试。

- **同一次构建**：`/api/v1` 的列表、计数、端点、供应商投影与路由快照一起构建、一起发布，失败或迟到都不换，所以列表价就是收费价，一次响应的计数、本页和下一页链接一致。响应数据只读内存，鉴权仍走准入事务（实时查停用、过期、IP、模型权限）。
- **过滤**：模型列表按 key 的路由预设过滤硬条件，价格取预设下最便宜的端点，预设不可用回 503 `preset_unavailable`；只列有公共端点（Owner 为零）和售价的模型，私有端点不进投影。端点列表不按 key 过滤。

模型对象（`/api/v1/models`、`/{model}`、`/user`）：

| 成员 | 值 |
|---|---|
| `id`、`canonical_slug` | 公开标准 ID `author/slug`（去掉内部版本后缀）；解析不回本模型的不列 |
| `name`、`created` | 去掉作者的部分；0 |
| `links.details` | 指向本 API 的 endpoints 路径 |
| `pricing` | 账户当前等级的主站售价里，可服务且预设留下的最便宜端点；标准服务等级、第一档，精确十进制字符串（token 项美元/token，`web_search` 每次，`image`/`image_output` 每张）。没有的单位（`request`、`input_audio_cache`、`discount`、`overrides`、无按张价的图片项）省略；`image_token` 只给收图片的，`internal_reasoning` 只给会推理的 |
| `context_length`、`top_provider` | 规格的上下文和输出上限；`is_moderated` false；`per_request_limits` null |
| `architecture` | text/image/file 输入、text 输出；无 `tokenizer`、`instruct_type` |
| `supported_parameters`、`reasoning` | 按能力与方言推导；手动思考预算给 `supports_max_tokens:true`；`mandatory` 和 effort 列表省略（有意偏离：未知不写 false） |
| `default_parameters`、`supported_voices` | null；`description` 等目录没有的数据省略 |

- **列表查询**：`offset`、`limit`、`q`、`context`、`min_price`/`max_price`、`min_output_price`/`max_output_price`（美元/百万 token）、`input_modalities`、`output_modalities`、`supported_parameters`、`model_authors`；`sort` 为 `pricing-low-to-high`、`pricing-high-to-low`、`context-high-to-low`。无分页参数返回全量，只给 `offset` 时 `limit` 为 500。其余官方参数（`category`、`providers`、`zdr`、`use_rss` 等）按名字 400 并说明缺什么；非官方参数忽略；查询串解不开（`%ZZ`、`;`）回 400，免得丢掉的过滤变成全量。`links.next` 只在后面还有模型时给。
- **单模型**返回 `{data: 模型对象}`，`id` 是标准 ID。**`/models/user`** 复用 key 的权限和同一列表，不假装按供应商偏好、隐私或护栏过滤。**`/models/count`** 是本 key 可见的公共模型数，`output_modalities` 同列表（默认 text，`all` 不限），非法值和重复参数 400。
- **`/models/{author}/{slug}/endpoints`**：公开投影（页面注明可能不适用于当前 key）。`tag` 是供应商分组的持久代号（`g` 加 8 位 base32），首次选用时随机分配、不再改、跨模型相同、不从供应商数据派生，也是 `provider.only`/`ignore` 和平台预设顺序认的身份；中转分组的 `provider_name` 是 Axiom，官方直连接入后用真名；不给内部 UUID。上下文与输出上限取各原生格式实测值的下限，参数取逐格式实测的交集（[端点评估](quality.md#实测的能力与上限)），`pricing` 是端点自己的价，`quantization` 是管理员记录值或 null；没有数据的成员为 null 或省略，不把未知写成 false（官方必填的几项是有意偏离）。实时状态不影响结果。`/api/v1/providers` 有可见端点时只有 Axiom/`axiom` 一项，否则空数组。

**网站和控制台的模型与价格**是同一次构建的另一份投影：`GET /api/console/v1/models` 匿名、按站点默认等级；`/models/account` 要登录、按账户等级。含已发布有价的公共模型（ID、名字、厂商、上下文、最大输出、流式、能力、`catalog.ScheduleView` 的逐项单价）和它的每个端点（代号、限制、能力、量化、是否验证、该受众的价；无价的端点不列），模型价取最便宜端点；单价带 `headline` 和 `display_amount`/`display_per`（[目录的「决策与理由」](catalog.md#决策与理由)）。不返回供应商、成本或价格来源；缺 `endpoints` 或价格时页面显示不可用，不当免费。

**价格公开开关** `model_prices_public`（`accounts.site_settings`，NULL 为公开）：关闭时匿名 `/models` 省略模型和端点的 `pricing`，`/models/account` 照常；匿名答案不认会话，对所有访客相同；有列表无价时页面显示「价格仅对登录用户可见」。只有所有存活节点的心跳都声明 `supports_model_prices_public`（`platform.model_prices_public_readiness`）时关闭才生效，否则匿名照常带价、管理接口拒绝关闭，旧节点重新加入时回退为公开；旧管理页省略字段时保留原值，新管理页核对 PUT 的返回、字段缺席提示未保存，且只在节点 `defaults` 带此字段时显示它。

### 转发规则

依据 Claude Code 的网关兼容指南。

- **请求头白名单**（`internal/gateway/headers.go`）：Messages 端点收 `anthropic-beta`（不筛值）、`anthropic-version`；Responses 端点收 `x-codex-turn-state`；Responses、Chat、旧版补全端点收会话头 `session_id`、`session-id`、`conversation_id`、`X-Session-Affinity`、`X-Session-Id`、`X-OpenCode-Session`、`X-Conversation-ID`，值换成派生值。其余（`Authorization`、`x-api-key`、`x-axiom-*`、`x-claude-code-*`、`User-Agent`、归因头 `HTTP-Referer`、`X-OpenRouter-Title`、`X-Title`、`X-OpenRouter-Categories`）不转发，归因头记入调用记录（[记录](observability.md#写入时记全)）。
- **派生标识**：`prompt_cache_key`、会话头、`metadata.user_id` 换成用账户密钥派生的不透明值（[会话粘性](routing.md#会话粘性)）。`metadata.user_id` 写成 JSON 串 `{"device_id":"…","account_uuid":"","session_id":"…"}`，`device_id` 每账户固定，空白的写空串；`user`、`safety_identifier` 换成哈希。
- **上游一律流式**，非流式由网关聚合，所以交付前失败仍可换端点、各次尝试不混。
- **只改必须改的**：`model` 换成端点的名字，标识派生，路由参数和 `axiom` 扩展删除；同格式时缓存标记、块状 system 和 Chat 的方言成员（`reasoning`、`cache_control`、回传的 `reasoning_content` 等）原样发。
- **Chat 端点**：`stream_options.include_usage` 一律 `true`；客户端的 `max_tokens`、`max_completion_tokens` 都拿掉，把最小的正数写进端点读的那个（上游模型名是 o1、o3、o4、GPT-5 或 GPT-6 Astra/Sol/Luna 写 `max_completion_tokens`，其余 `max_tokens`）。
- **输出上限即预留上限**：没写（`null`，Messages 与 Responses 的 0、`-0` 也算）时发报价用的上限（模型默认，装不下时裁短）；模型上限未知时不发。
- **Chat 旧版函数**：先把 `functions`/`function_call` 升级成 `tools`/`tool_choice`，旧调用与结果按序稳定配对；冲突的选择或同一消息的新旧调用拒绝。只用旧表示的客户端收回 `function_call` 和 `finish_reason: "function_call"`；旧版有工具声明时强制 `parallel_tool_calls:false`，上游仍并行按协议错误处理。控制字段按解码值判断，正文、签名、工具参数保留原字节。
- **跨格式**：三种生成格式两两可转，端点也说客户端格式时优先原格式；原生两格式经 Chat 形状中转。表达不了的成员回 400，不删约束凑请求。Anthropic 思考块只回放给 Messages，Responses 的 reasoning 项只回放给 Responses；`file_id` 只在 Chat 与 Responses 之间带回。预留、尝试、结算同同格式。
- **缓存标记跨格式**：发往 Messages 原样；发往 Responses 时，规格写明支持显式断点的模型上，文本块的 `cache_control` 写成 `prompt_cache_breakpoint`（丢 TTL），其余丢掉。收费按上游报告的实际缓存读写。
- **只认原上游的内容**（推理签名、加密推理）优先粘回原端点，同格式时从不删改。格式特有能力只走原格式端点（[路由](routing.md#一个请求的路由步骤)）。
- **推理**：缺省保留模型默认，显式关闭写成 thinking disabled 或 effort none。`reasoning.exclude:true`、thinking display omitted 只隐藏、照常计费；隐藏在计量后由本站执行，Chat 不返回推理明文，`reasoning_details` 只留签名和密文，续聊状态保留。Chat 回复的推理文本同时写 `reasoning` 和 `reasoning_content`，端点只写一个时本站补另一个。
- **响应头白名单**：只回内容类型、调用编号、本站的限流与重试信息；`x-codex-turn-state` 只在作答尝试的上游设了它时回传。事件流带 `X-Accel-Buffering: no`。`id` 是本站调用编号，`model` 是实际服务的模型。
- **保活**：开始转发前不发任何字节；之后上游在传输而无新内容时发 SSE 注释行。Messages 透传上游 `ping`，上游没有时本站补发。不发自创的 JSON 心跳。
- **直接格式的回复**读完整份（上限 16 MiB），校验恰好一个带 `data` 或 `results` 的 JSON 对象，结算后配上本站的 `id`（rerank）、`model`、`usage`、`cost` 写出，不合格仍可换端点；请求删掉 `prompt_cache_key`、`safety_identifier`，`encoding_format`（只认 `float`、`base64`）、`dimensions`、`top_n`（正整数）、`input`/`documents`（`documents` 收字符串或 `{text,image}`，不收空数组）校验后原样转发。
- **`/responses/compact`**：不流式（`stream:true` 400），回复整份读入并校验，只把顶层 `usage` 换成本站结算的用量和费用、丢弃上游的顶层 `axiom`，其余成员、次序、字节不变；结算写不进时只带用量和 `axiom.billing_pending: true`。请求只改模型名、标识和路由参数。

### 用量与费用

- **每次调用都返回用量和费用**（对齐 OpenRouter）：输入、输出、缓存读写、推理 token 和费用。Chat/Responses 另带音频、视频和预测 token 明细、`server_tool_use_details`、`is_byok`（作答的是账户自带的 key，[BYOK](accounts.md#自带的供应商-keybyok)）和为正时才有的 `cache_discount`；`cost_details.server_tool_cost` 只写按次工具的售价。搜索计入 `server_tool_use.web_search_requests`；Responses 的 `file_search`、代码解释器计入 `tool_calls_requested`/`tool_calls_executed`（[用量来源与暂估](billing.md#用量来源与暂估)）。
- **Embeddings 与 rerank**：token 取上游的输入；rerank 的 `search_units` 按次计入 `web_search` 并按它收费（规格要写 `input` 和 `web_search`，缺价的端点被[排除](routing.md#一个请求的路由步骤)）。
- **流式放在最后**：费用在结束前结算，写在 `usage.cost`、`usage.cost_currency`，估算的带 `usage.cost_estimated: true`，之后的校正不追改。Chat 最后一块带一个空内容的选项；Responses 结束前的 `response.usage` 为 `null`。
- **Chat 端点的用量**按 OpenAI 口径（`prompt_tokens` 含缓存，`completion_tokens` 含推理）；DeepSeek 的 `prompt_cache_hit_tokens` 在 `cached_tokens` 缺失或为 0 时当缓存读。
- **结算写不进**：内容完整的照常结束，带 `axiom.billing_pending: true` 不带费用（非流式也是 200）；只有截断的流以 `billing_unavailable` 结束。
- **出错也可能收费**（如已执行的搜索，[免单规则](billing.md#免单规则)）：错误体的 `axiom` 带用量和费用。转发中追加预留失败时按流中途出错截断（[预留与结算](billing.md#预留与结算)）。金额是本站记账货币并附币种（[钱的表示](billing.md#钱的表示)）。
- **调用编号**就是请求 ID：每个响应都在 `X-Request-ID` 和 `X-Generation-Id` 里带它。`GET /api/v1/generation?id=` 查尝试、路由和售价明细，可选给 `price_version`、`charge_details`（[用户明细](billing.md#用户看得到什么)）；记录写入前回 404 `generation_not_found`。
- **客户端断开**（取消不免单，重试算新请求，[计费](billing.md#客户端断开)）：排队或准入后未调用上游时不收费；端点传递取消则中断、按已产生的用量收并暂估缺项；不传递则读完不转发、按完整用量收，读也超时按暂估。

### 响应缓存

照 OpenRouter 的语义（成功回复命中时重放、换新 `id`、用量清零、免费，TTL 默认 300 秒、1–86400），不同处：按**账户**隔离并按当前 key 复核；命中仍走准入与预留；不合并并发请求；只缓存三种生成格式的普通调用；平台默认关闭。

| 来源 | 值 | 结果 |
|---|---|---|
| 平台 `AXIOM_RESPONSE_CACHE_CONTENT` | `off`（默认） | 不建缓存 |
| 命名预设 `cache_enabled` | `false` | 关闭，请求头打不开 |
| 请求头 `X-OpenRouter-Cache` | `false` | 关闭 |
| 命名预设缺省 | — | 请求头为 `true` 才开 |
| 命名预设 `cache_enabled` | `true` | 打开 |

- **TTL**：请求头 `X-OpenRouter-Cache-TTL` 优先于命名预设 `cache_ttl_seconds`，都没有为 300 秒。头取开头的十进制数字（`60abc` 为 60，`1.5` 为 1），夹到 1–86400，不以数字开头则忽略；命名预设的两个成员保存时校验，不并进上游请求。
- **键**：账户 ID、客户端格式、是否流式、规范化正文（只删字符串外的空白）的 SHA-256；查询在账户 RLS 内。条目存服务模型与端点，命中前按当前 key 的准入路由复核，不在路由里算 MISS。
- **不缓存**：直接格式、旧版补全、compact、`previous_response_id`、`web`/`file-parser`、`openrouter:web_search`、服务端原生工具、服务端压缩、文件引用。
- **响应头**（开启且成功时，不发给上游）：`X-OpenRouter-Cache-Status` 为 `MISS`/`HIT`；`X-OpenRouter-Cache-TTL` 是本次 TTL 或剩余秒数；HIT 另带 `X-OpenRouter-Cache-Age` 和 `X-OpenRouter-Cache-Source-Id`。`X-OpenRouter-Cache-Clear: true` 先删这个键再按 MISS，删除失败只记日志。
- **命中**在准入后、第一次尝试前：用存的上游原始字节按当前客户端格式重走 relay，调用 ID、`created`、`X-Generation-Id` 新生成，`X-Codex-Turn-State` 用存的值；用量和费用为 0，预留全额释放，记录 `cache_hit=true`，不占供应商容量。并发相同请求各自 MISS、各自计费。
- **写入**：只写上游 200、完整、结算成功的回复，交付后异步写，队列满就丢，失败只记日志；命中不延长过期。条目最多 1 MiB，每账户最多 64 MiB 或 1024 条，超出删最旧；每分钟清理过期。
- **加密**：正文用 `secret.Keyring` 信封加密，AAD 绑 `gateway.response_cache` 的表、行和用途，表按账户 RLS；受控导出单独写 `cache.jsonl` 和 `cache/<cache_id>`（[受控导出](observability.md#受控导出)）。

### 错误

- **外壳**：Messages 用 Anthropic 的；其余用 OpenAI 的 `error` 对象（`message`、`type`、`param`、`code`），`type` 用下面 Messages `type` 的词，`param` 缺省为空串，`code` 是与状态行相同的数字。统一的 `error_type` 在 Chat 的 `error.metadata.error_type`、Messages 的 `error.error_type`、Responses `response.failed` 的顶层；Responses 流开始前出错用 Chat 外壳，原生事件用字符串 `code`。
- **上游拒绝请求本身时给原话**（Claude Code 靠它恢复）：交出 `message`、`type`、`param`，上游 code 放 `metadata.provider_code`（Responses 原生流在 `response.error.code`），先去掉网址、本站的凭证、形似凭证的串（`sk-`、`ak-`、`Bearer`、`api_key=`）、供应商域名及其各级上级域名、new-api 的 `(request id: …)` 和 new-api/one-api/sub2api 的名字；每项最多 2 KiB，流内超长的整项省略；中转软件自己的 `type` 和 code 不传。原话只进这次回复，不进日志和表。
- **其余错误**（本站的、上游 429 和 5xx）用统一说法。非法 JSON 回 400 “Invalid request: invalid JSON request body”，类型错的值用 JSON 指针指出。
- **Messages 的 `type`**：400 `invalid_request_error`、401 `authentication_error`、402 和预算 403 `billing_error`、其余 403 `permission_error`、404 `not_found_error`、408 `timeout_error`、413 `request_too_large`、429 `rate_limit_error`、502 和 `billing_unavailable` `api_error`、其余 503 `overloaded_error`。

| 失败 | 回答 |
|---|---|
| key 无效、过期、停用，IP 或域名不允许 | 401 `authentication` |
| 账户暂停调用 | 403 `permission_denied` |
| 余额不够（含在途请求占满） | 402 `credits_insufficient`，`limit_source: balance` |
| 工作区预算、key 上限不够 | 403 `credits_insufficient`，`openrouter_workspace_budget`/`openrouter_key_limit`，会重置的带 `Retry-After: 60` |
| 套餐不够 | 402，`subscription`；流中追加时为 `balance`（[订阅套餐](billing.md#订阅套餐)） |
| 追加预留时数据库出错 | 503 `billing_unavailable` |
| 限流（key 节点保护、账户与 key 业务速率、IP 或账户在途上限、账户资金操作排队超 5 秒）；端点满载且排队满或超时；凭证占满 | 429 `rate_limited`，排队的带估计等待，凭证占满为 1 |
| 节点满载、准入超时、快照过旧、数据库不可用、持有者失效、节点内存预算不够 | 503 `overloaded`，`Retry-After: 1` |
| 模型不存在或 key 不许用 | 404 `model_not_found` |
| 永久没有端点（过滤掉全部或没有售价） | 404 `no_endpoint`，附 `reasons` |
| 暂时没有端点（全部离线、供应商余额不足、重试预算用完、`:exacto` 无验证端点） | 503 `no_endpoint`，`Retry-After: 1` |
| 路由预设不可用（快照不认识，或不属于该 key 的站点、账户、工作区、币种） | 503 `preset_unavailable`，`Retry-After: 5` |
| 没有端点支持这种写法；格式错误；`previous_response_id` | 400 `invalid_request` |
| 开了搜索或代码执行而次数无上界 | 400，说明写 `max_uses` 或 `max_tool_calls`，没有上限字段的格式暂不支持 |
| 上游拒绝：上下文过长、内容策略、权限、请求体超限、其他 | 400 `context_length_exceeded`、403 `content_policy_violation`、403 `permission_denied`、413 `payload_too_large`、400 `invalid_request`，不换端点（OpenAI 直连 HTTP 答复的上下文过长先换，[失败怎么办](routing.md#失败怎么办)） |
| 请求过大；`file-parser` 超限 | 413 `payload_too_large` |
| `web`、`file-parser` 后端失败；所有尝试失败；120 秒窗口到期时有过尝试 | 502 `upstream_error` |
| 调用最长到期、节点退出、读请求体超时、120 秒窗口到期而无尝试 | 408 `timeout` |

- **流开始后**状态仍是 200：Chat 发带顶层 `error`、`finish_reason: "error"` 的块，Messages 发 `error` 事件，Responses 发 `response.failed`，然后结束；已结算的带用量和费用，否则只带用量。余额不够是截断；上游拒绝请求本身的保留原话、同一 `error_type` 和已转发内容，其余算供应商失败，提交点前还能换端点。失败事件立即结束尝试。
- **非流式中途出错**且有部分内容：Chat 回 200（内容、`finish_reason: "error"`、`error`、用量费用），Responses 回 200（`status: "failed"`、`output`、用量费用），Messages 回 502、不带内容、用量费用在 `axiom`。未完成的工具参数不修补。
- **未路由的路径**：`/v1/messages` 下用 Messages 外壳，其余用 Chat 外壳；404 `not_found`，方法不对 405 `method_not_allowed` 带 `Allow`。
- **`Retry-After`** 是整数秒，最多 60。**429 的原因**只进指标 `axiom.gateway.rate_limited`（`ip_in_flight`、`account_in_flight`、`key_rate`、`count_rate`、`account_rate`、`business_key_rate`、`account_gate`、`queue_full`、`queue_key_full`、`queue_wait`、`credentials_busy`）。
- **供应商的 401、402、403、429** 只用来换凭证或端点，不当作我们的认证、钱包或用户权限错误。
- **对中转站友好**：计费类问题绝不回 401，不用会让 new-api 自动禁用渠道的措辞（如 “You exceeded your current quota”、“Your credit balance is too low”），清单写成了测试。

### 业务速率与 OpenRouter 的差异

OpenRouter 付费模型没有平台级速率上限；Axiom 对付费调用也施加后台配置的账户、等级、key 业务限流并保留节点保护（语义见[账户](accounts.md#业务速率限制)），调用与 `count_tokens` 分开计数，读 key 信息不计。

- 只有请求速率的 429 带 `X-RateLimit-Limit`（触发的额度）、`X-RateLimit-Remaining`（0）、`X-RateLimit-Reset`（Unix **秒**的保守上界：预检时刻加两个窗口，向上取整）；`Retry-After` 为两个窗口、上限 60。容量和排队的 429 只给 `Retry-After`，成功不带限流头。
- `GET /api/v1/key`：`rate_limit` 留 `requests`（含节点保护的最小正上限）/`interval`（默认 `1m0s`），另给 `account_requests`/`key_requests`。`budgets` 只列已设的 workspace/key_limit 预算，`effective_budget` 取第一个耗尽的（工作区优先）否则剩余最少的，无预算为 `[]`/null；`*_usd` 是本站货币，`reserved_usd` 是在途预留，remaining 扣已收和预留、不低于 0；`reset_interval` 与 `resets_at`（数据库时钟）给 UTC 边界，永久为 null；`guardrail_id`、`budget_id` 为 null，`include_byok` 为 false（BYOK 照常占额度，[计费](billing.md#定价)）。`limit`/`limit_remaining` 只描述 key，与 key_limit 预算同一查询读出。

### 路由参数

- **接受**（语义与组合见[请求覆盖](routing.md#请求覆盖)和[请求覆盖与组合](routing.md#请求覆盖与组合)，都不能放宽 key 的预设）：`provider` 对象（`only`、`ignore`、`order`、`allow_fallbacks`、`require_parameters`、`data_collection`、`zdr`、`sort`、`max_price`、性能偏好、`quantizations`）；`models` 备选，Messages 也认 `fallbacks: [{model}]`（[命名预设](routing.md#命名预设)）；模型名变体；`@preset/名称`、`model@preset/名称`、`preset`（账户的命名请求配置）；`session_id`、`x-session-id` 头、`prompt_cache_key`（体优先，最多 256 字符）；`user`。
- **要点**：`only`/`ignore`/`order` 用分组公开代号精确匹配（区分大小写），未知的 `only`、关闭回退时未知的 `order` 回 404，空列表和 `null` 不加限制。`sort` 收 `price`/`throughput`/`latency` 或 `{by, partition}`。`max_price.prompt`/`completion` 按本站货币每百万 token、`request` 按每个生成请求，只收可精确表示的十进制，非美元站点不声称美元上限。性能偏好的数字按 EWMA、分位对象按滚动五分钟窗口，不满足的排后不排除。`quantizations` 匹配管理员记录的级别。`require_parameters: true` 按端点实际调用格式的有效参数清单（含实测）过滤，请求里出现的能力参数才算需要。
- **偏离**：无法兑现的在预留前回 400 `invalid_request` 并点名 `provider` 成员。

| 参数 | 这里与理由 | 支持还需要 |
|---|---|---|
| 供应商名称 | 中转分组只用匿名代号；官方直连接入后用真名 | 官方直连接入 |
| `require_parameters` 下的 `seed`、`logprobs`、`frequency_penalty`、`verbosity`、专有参数 | 400：没有逐端点的采样和专有参数清单 | 逐端点清单 |
| `sort` 的 `percentile` 等未知成员 | 400：排序对象只有 `by`、`partition` | 无 |
| `max_price.image` | 400：没定管输入图还是生成图，端点不报按张用量 | 定义与按张用量 |
| `max_price` 其他成员 | 400 | 对应计费单位 |

- **自己的扩展**放 `axiom` 对象或 `x-axiom-*` 请求头，用后删除。

### 插件与提示词变换

`plugins`、`transforms` 在入口校验，执行了或明确关闭的才在转发前删除；只有 Chat 能启用本地插件。

| 成员 | 行为 |
|---|---|
| `response-healing` | 非流式、`response_format` 为 `json_object`/`json_schema` 时本地修复 JSON 语法、提取代码块或前缀后的 JSON，删掉不再对应的 logprobs；流式、普通文本、失败或 `length` 截断不修 |
| `context-compression`、`transforms:["middle-out"]` | 准入前从中间删完整对话轮次、保留首尾，仍超长再截短纯文本消息，最多 100000 条，未完成的工具调用不拆；系统指令、工具调用与结果、签名、多模态部件不截，放不下回 400 |
| 没有显式策略 | 上下文 ≤8192 token 的模型在正文超过上下文的字节上界或消息条数上限时尝试压缩，失败原样转发 |
| `enabled:false`、`transforms:[]`、空数组、null | 等于关闭；显式插件优先于 `transforms` |
| 未知插件或 transform（即使关闭）、未支持的选项、非 Chat 启用 | 400，含 `not supported` |

- **压缩口径**：用目录主模型的上下文和输出上限，在路由和预留之前；输入按 JSON 正文字节上界算，不按回退端点重压。
- **`web`：一次请求只走一条路、只收一次费。** `engine` 是启用的后端名或 `native`；省略时模型声明了 `web_search` 能力就走厂商原生，否则走默认后端；未知或停用的后端、不能原生却写 `native`、没有后端且不能原生，都回 400（含 `not supported`）；管理员清空引擎列表后环境变量不会恢复搜索。`:online` 等于省略 `engine`。
  - **原生**：在转换后的正文里打开开关（Chat `web_search_options:{}`，Responses `web_search` 工具，Messages `web_search_20250305` 工具，`max_uses` 取规格值、默认 1、最多 10），已有同名工具不重复加；Chat 预留按 1 次、按上游实际结算。引用转成 Chat 的 `url_citation`。
  - **后端**：最后一条用户文本为查询（空或超 1024 字节 400），按注入上界重新路由；`max_results` 默认 5、最多 10，`include_domains`/`exclude_domains` 转成 `site:`/`-site:`，`search_prompt` ≤512 字节；限时 15 秒，结果编号列出作为 system 消息插在第一条用户消息之前，按后端价收一次。URL ≤8 KiB、标题 256 字节、摘要 1024 字节、响应 4 MiB，注入 ≤40 KiB。非流式 Chat 补 `url_citation`。内存预算不够 503，失败或超时 502，不调模型。
  - **不搜两次**：请求已声明厂商搜索工具或模型 `always_searches` 时插件丢弃、不收费；自动搜索模型只回退到同样自动搜索的模型。
- **`file-parser`**：`pdf.engine` 缺省 `native`，文件原样发给接受文档的端点；`mistral-ocr`、`cloudflare-ai`（`pdf-text` 同后者）走部署配置的 Docling 后端（[fileparse](../../internal/fileparse/README.md)），没配置回 400；不做“不支持原生就 OCR”的回退。
  - 准入前把内联 base64 文件换成文本再路由、报价；单文件 20 MiB、最多 8 个、文本合计 2 MiB（同一文件在历史里出现几次计几次），超出 413；后端响应 8 MiB、整步 60 秒；内存预算不够 503。解析前先做只读 key 检查和速率判断。
  - 计费：`Config.FileParse` 打开时 `mistral-ocr` 按页计 `file_parse`（价在模型价目表，页数口径见[计费](billing.md#预留与结算)），`cloudflare-ai` 免费，`native` 按输入 token；关闭时每个文件一次 `web_search`。
  - 后端失败 502。非 base64、同时带 `file_data` 和 `file_id`、文件不在消息顶层却要求解析，回 400；只带 `file_id` 的原样保留，须用 `provider.only` 固定一个供应商。
  - 非流式 Chat 在 `choices[].message.annotations` 返回 `{"type":"file","file":{"hash","name","content":[…]}}`（`hash` 是文件的 SHA-256），客户端带回注解和文件时用注解文本、不再解析不收费；任一 choice 装不下就整份不写，流式不返回。

### 服务端工具

模型决定调用零到多次，网关执行后把结果送回继续采样。`openrouter:` 下 13 种只认 `openrouter:web_search`，其余准入前 400（含 `not supported`），`GET /api/v1/tools` 不提供。

- **接入**：三种生成格式都认；工具对象准入前删掉，正文里加函数 `openrouter_web_search`，到上限不再声明。`n>1`、与 `web` 插件或厂商搜索、代码执行工具同时声明，回 400。`engine` 默认 `auto`、规则同 `web`：能原生就映射成厂商工具，不跑循环。
- **参数**：`max_results` 1–25（默认 5）、`max_uses` 1–30（默认 30）、`max_total_results` 1–1000、`allowed_domains`/`excluded_domains` 各 ≤20；`mode`、`search_context_size`、`max_characters`、`user_location`、`x_search`、`stop_server_tools_when` 和超过 30 的 `max_tool_calls` 回 400。原生时次数折成厂商 `max_uses`，最多 10。
- **循环**：读 `query`（≤1024 字节；没有可用查询时不搜索，只把说明作为工具结果返回）去搜索，保留 assistant 回合原样，按客户端格式追加结果再采样，直到不再调用或到上限：步数为 `max_uses` 与 `max_tool_calls` 的较小值，结果正文共 32 KiB、每次 8 KiB。到上限后追加说明、去掉函数声明和强制的 `tool_choice`，再采样一次。同一回合兼有服务端和客户端调用回 502。`max_tool_calls` 发往 Chat/Messages 前删掉。
- **每步一次尝试**：同一条目上依次发出，各取凭证槽、配额和尝试序号；回退最多 3 次（`platform.MaxAttempts`），循环最多 30 步（`platform.MaxServerToolSteps`），回退的序号在 1–3。
- **流式与期限**：每跳事件先留在网关（计入内存预算），确认不是工具调用才发给客户端。每步受调用期限、响应头等待、空闲超时、关停约束；搜索期间停掉空闲计时，每次搜索限时 15 秒，期限到或关停后不发新搜索；客户端断开取消采样。末跳没有内容按空回复 502。
- **计费**：每次成功搜索收一次 `web_search`；各次采样合起来按最后一次的结局套[免单规则](billing.md#免单规则)。预留：输入按 1 + 步数上限次采样，输出按 1 + 步数上限乘每次上限。中途失败、超时、空 200、流中断回 502，模型用量免单、搜索照收、不换端点。
- **厂商自己的工具**：Responses 的 `web_search`、`code_interpreter`、`file_search` 原样转发给能执行的 Responses 端点，按调用项计费；`file_search` 要供应商账号拥有 `vector_store_ids`，转到 Chat 或 Messages 时拒绝。

### 模型名变体

网关在查目录前读掉变体，表在 `internal/gateway/variant.go`，调用、备选、`count_tokens` 和发现共用；`:free` 是唯一的目录后缀（[名字解析](catalog.md#名字解析)）。认得但不能兑现的回 400 说明原因，不调用基础模型。

| 变体 | 这里 |
|---|---|
| `:nitro`、`:floor` | `provider.sort` 的 `throughput`、`price`；不做 OpenRouter 的 priority/flex 层准入 |
| `:exacto` | 质量优先，只留验证过的端点，没有时 503 `no_endpoint`（`not_verified`） |
| `:online` | 等于 `web` 插件 |
| `:free` | 目录独立条目（自己的规格、零价、端点）；没有就 404 `model_not_found`，不回落付费；计费发布拒绝给它非零售价，路由对已发布非零价的条目没有端点 |
| `:thinking` | 请求已要推理（effort、`enabled: true` 或预算；空 effort 不算）时按基础模型服务，否则 400 |
| `:batch`、`:extended` | 400 |
| 其他后缀 | 404 `model_not_found` |
| `~作者/系列-latest` | 目录别名表的目标；没有别名 404 |

- **叠加**：最后一个排序变体生效，`:exacto` 被后面的排序变体替换后不再要求验证，`:thinking` 与排序无关、叠加时保留；显式 `provider.sort` 压过变体。
- **别名**只追加，由 `axiom catalog save-alias` 切换并审计；目标须是同作者的已上架模型。
- **发现**只解析基础模型，`:exacto`、`:thinking` 也回 200，不代表可调用。
- **备选**：不支持的变体 400；不认识的后缀、缺条目或别名的按不存在略去；排序变体与请求最终排序不同 400；`:exacto` 只在请求本身是 `:exacto` 或有显式 `provider.sort` 时成立。
- **长度**：整个名字 ≤200 字节，超过准入前 404。响应 `model` 是实际服务的模型，记录另存客户端写的全名。

**`openrouter/auto`、`openrouter/free`** 把请求的排序（变体、`provider.sort`，否则 key 预设策略）作用到 key 能准入的全部模型上（free 只取 `:free` 条目），取第一个，之后的端点排序和跨模型回退照旧，请求的 `models` 不再追加。没有免费条目回 404 `no_endpoint`（`no_candidates`），全被 key 权限排除回 404 `model_not_found`。`count_tokens` 和 `context-compression` 回 400，`/models` 不列它们。

### 给其他中转站对接的接口

| 接口 | 用途 |
|---|---|
| `GET /v1/dashboard/billing/subscription`、`/usage` | OpenAI 旧版余额：余额 = `hard_limit_usd` − `total_usage`（0.01 为单位），`total_usage` 是累计消费、不看日期 |
| `GET /api/v1/key`、`/api/v1/credits` | key 额度和账户余额（OpenRouter 格式），`is_management_key` 按实际种类 |
| `GET /api/v1/generation?id=` | 单次调用详情，含 `native_tokens_*`、`streamed`、`cancelled`、`cache_discount`、`session_id` |
| `GET /api/v1/generation/content?id=` | 本账户的回 404 `content_not_available`，别人的回 `generation_not_found` |
| `GET /api/v1/activity` | 最近 30 个完整 UTC 日按日、按模型的用量，今天不算；`api_key_hash` 收 key UUID，`user_id` 不支持，`endpoint_id` 为零、`provider_name` 为 Axiom；金额读校正后的实收，输入优先用写入时的物理输入 |

- 金额是本站记账货币（人民币站点在接入说明里写明）；`total_credits`、`hard_limit_usd` 是余额加累计消费，在途预留不扣。
- 管理 key 只能读 `/key` 和 `/credits`，不能发起调用；其余辅助接口只收调用 key。

### Responses 的会话状态

- **还没提供**：带 `previous_response_id` 回 400，`DELETE /v1/responses/{id}` 回 404。以下是已同意的设计。
- 会话状态与对话内容分开保存，不受内容开关影响；保留期平台配置，默认 30 天；按账户加密、按消息去重，随账户密钥销毁（[加密与密钥](observability.md#加密与密钥)）。
- 按工作区隔离，读一次（[模块边界规则](../architecture.md#模块边界规则) 3 的例外），读不到就当没有。`store: false` 不保存；不提供按 ID 读回；`DELETE` 立即删除，注销见[停用与注销](accounts.md#停用与注销)。
- 续接由本站拼回完整输入，换端点也能续。

### 与 new-api 的有意差异

`just diff` 用固定摘要的 new-api 官方镜像和 Axiom 节点接同一个回放录制样本的假上游，比状态码、响应头、JSON 和 SSE；要起容器，不在 PR 的 CI 里。每个差异必须由 `cmd/axiom/diff_rules_test.go` 的一条规则解释（`TestDiffHarness*` 配反例），没命中的规则也失败。有意的差异：本站的 `id`、实际服务的 `model` 和费用字段；用量按格式自己的成员写；不暴露上游的头和错误；成员按格式定义写；不改上游的工具参数和推理字节。

### 版本与兼容

OpenAI、Anthropic 格式跟随官方规范；扩展只增不改；不兼容的修改用新版本路径，旧版本经公告期才下线；管理接口用 OpenAPI 契约，规则相同。

## 决策与理由

| 决策 | 理由 |
|---|---|
| 同时提供 `/v1` 和 `/api/v1` | OpenRouter 的客户端只改地址就能接入。 |
| 请求头白名单，`anthropic-beta` 不筛值 | 筛值会让新功能失效；客户端凭证和身份不能流向上游。 |
| 标识换成派生值 | 删除让上游缓存失效，原样转发会串租户。 |
| 上游一律流式，强制 `include_usage`，输出上限写在端点读的成员里 | 计量要靠上游用量，端点忽略的上限罩不住预留。 |
| 能同格式就不转换 | 转换会拒绝表达不了的成员。 |
| 跨格式缓存标记表达不了就丢 | 拒绝会让请求到不了 Responses 端点，丢掉只影响命中率。 |
| Chat 推理文本写两个字段 | 两个名字都有客户端在读。 |
| 推理隐藏在计量后由本站执行 | exclude 不减费；防止端点漏出明文，保留续聊要的签名。 |
| 旧版客户端收回旧函数表示 | 统一降级会破坏现代客户端和并行调用。 |
| 响应头白名单；转发前不发字节 | 上游头暴露货源；发了字节就不能换端点或改状态码。 |
| 上游拒绝时给脱敏原话 | Claude Code 靠原话恢复；清洗在共用代码做一次。 |
| `error.code` 是状态数字 | 产品要求与 OpenRouter 一致的错误码。批准：2026-10-28，#309。 |
| 余额 402、预算 403，永久无端点 404、暂时 503、满载 429 | 与 OpenRouter 一致，客户端据此决定等、充值或放弃。 |
| 计费类不回 401、避开禁用措辞 | new-api 会因此自动禁用渠道。 |
| 结算写不进时照常结束并带 `billing_pending` | 不发未入账的费用，也不让客户端重发付两次。 |
| Messages 非流式中途失败回 502 | Messages 没有表示出错的结束原因。 |
| 付费调用也限流；窗口头只给请求速率 429 | 保护共享容量；并发和排队不是窗口。 |
| `/key` 只投影已有策略和计数 | 不造 ID，不引入第二份资金事实。 |
| 发现与价格页复用路由构建的投影 | 页面价、列表价和扣费出自同一份发布。 |
| 端点 `tag` 用持久分组代号 | 它就是路由认的值，跨模型不变且不泄露货源。 |
| 价格用 `ScheduleView` | 折成每百万 token 会让按次和分档的价显示错。 |
| 价格默认公开，等全部节点就绪才可关 | 与 OpenRouter 一致；有旧节点时会一半隐藏一半泄露。 |
| 发现接口保留实时准入 | 缓存身份会放行已撤销的 key。 |
| 缓存按账户隔离、按当前 key 复核 | 换 key 仍命中，权限收窄后不重放。 |
| 缓存存上游字节、命中重放进 relay | 命中与正常回复同一条路径。 |
| 缓存命中仍走准入与预留 | 绕开准入会让余额不足的账户过关。 |
| 缓存平台默认关闭、有上限、信封加密 | 内容保存由平台统一决定；上限限住占用；密文不能搬到别的行。 |
| 只缓存普通生成 | 其他路径有正文外的搜索、解析或历史，重放会漏收费。 |
| 直接格式只走原生端点、整份读完 | 没有可靠的转换；整份校验让失败还能换端点。 |
| rerank 的 `search_units` 计入 `web_search` | 同是一次服务端调用，不必改价目表。 |
| 旧版补全原生提供、一律流式、拒绝 `best_of > 1` | 转成 Chat 会丢 `n`、`echo`、`logprobs` 语义；网关要读完流才能结算。 |
| 旧版补全按整批预留、不分段，计入 `suffix` | 上游按整批计费，少留由平台承担。 |
| `max_tokens: 0` 是零输出，`echo` 不算生成 | 只回显是合法回答；算进生成会多收。 |
| `logprobs` 分数组追加，结束时编码一次 | 不复制历史，长流不退化成 O(n²)。 |
| `completions` 编号追加在末尾，受格式闸控制 | 已存编号不变；旧节点不认识的格式会让它摘掉供应商。 |
| 推不出的格式由模型映射声明 | 按渠道类型猜会误标；直接改同步表会被覆盖。 |
| compact 只换 `usage` | 每次调用都要返回用量和费用。批准：2026-10-28，#309。 |
| `count_tokens` 本地估算 | 转发要占端点名额；文件按固定页数可能偏低，不承诺不超窗口。 |
| `/credits`、`/key` 也收管理 key | 与 OpenRouter 一致，保留中转站常用的调用 key。批准：2026-10-28，#309。 |
| 提供两种余额接口 | new-api 查 OpenAI 类渠道用旧版接口，OpenRouter 格式服务兼容工具。 |
| 内容接口不给内容，`activity` 不给端点 | 产品不让人查看内容，货源不公开。 |
| 扩展放 `axiom` 对象 | 不与 OpenRouter 的字段冲突。 |
| 本地插件准入前压缩、聚合后修复 | 上游会静默忽略；路由、报价和转发用同一份正文。 |
| 原生搜索按目录能力判断，两条搜索路径共用后端和价 | 厂商白名单会变；不出现两个价。 |
| 后端用自托管 SearXNG 与付费 Firecrawl | 前者可在大陆自建，后者“每次 + 每条结果”的计价对上两个成本字段。 |
| 后端价替换锁定的 `web_search` 单价，0 沿用模型价 | 预留与结算同一张表；0 让旧部署不变。 |
| 循环的每次采样是一次尝试，输出预留按步数放大 | 每次采样是独立扣费；不放大罩不住每步的输出（I1）。 |
| `openrouter:web_search` 不与厂商工具组合 | 循环会重复跑厂商工具而报价只算一次。 |
| OCR 用自托管 Docling，默认 `native`，按页计费 | 可自托管；不替客户端花没要求的钱。 |
| 解析前先查 key | 不能用的 key 不该触发最长 60 秒的 OCR。 |
| 变体在网关里读；不能兑现的 400、未知的 404 | 变体是路由开关不是模型；让客户端区分暂不支持和不存在。 |
| `:exacto` 只走验证过的端点 | 比 400 更接近意图，不假装有基准分。 |
| 显式 `provider.sort` 压过变体 | 请求体字段比后缀具体。 |
| `:free` 独立条目，`-latest` 只追加的别名表 | 免费版有自己的上下文和价；显式映射不串族、可审计。 |
| auto、free 用路由排序选模型 | 没有分类器数据；随机不可复现。 |
| 会话状态本站保存，按工作区、与内容分开 | 续接让 Codex 少发重复内容，内容关闭时仍可续接。 |
| 辅助请求走便宜端点是可选预设规则，默认关 | 见[路由](routing.md#排序的先后)。 |

## 待定问题

- 限制现在是代码默认值，后台配置和按端点、按模型调超时没有（[#731](https://github.com/TYCT-tyct/Axiom/issues/731)）；默认值下空闲超时（180 秒）晚于 120 秒窗口，不会触发换端点。
- 响应缓存没有按账户的保留设置，账户删除流程未实现；实现时纳入 `gateway.response_cache`。此前 `AXIOM_RESPONSE_CACHE_CONTENT` 不应打开。
- 402/403 附所需金额、`limit_source` 加 `pending`、按窗口结束给 `Retry-After`：等 billing 报出这些信息。
- Chat 输出上限成员按模型名判断，规格有了对应键再改；按规格改写推理参数没有。
- 供应商真名的可信映射、逐端点采样参数清单缺数据；`max_price.image` 的含义和非美元站点的换算未定。
- `:floor` 准入 flex 层要不要改写上游 `service_tier`。
- `-latest` 别名没有后台界面。
- `/embeddings/models` 与嵌入模型进列表：要先给规格加输出模态。
- Bedrock、Vertex 和 Gemini 原生格式以后再做。
