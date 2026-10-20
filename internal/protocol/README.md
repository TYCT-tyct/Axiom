# protocol

三种调用格式（OpenAI Chat、OpenAI Responses、Anthropic Messages）的读和写。总体规则见[总体架构的「协议转换」](../../docs/architecture.md#协议转换)，对外行为见[接入与对外接口](../../docs/domains/gateway.md)。

目前有读请求摘要的 `Inspect`、改写同格式请求并把 Chat 请求转换给另两种格式的 `Prepare`、转发流的 `Relay`（同格式，以及把 Messages、Responses 的流转给 Chat 客户端）、回答流开始前错误的 `RenderError`、读上游 SSE 的 `internal/sse`、检查流结构的 `internal/streamcheck` 和录制样本的命令 `internal/record`；非流式聚合（`RelayOptions.Aggregate`）是同一个 `Relay` 的另一种输出，见「非流式聚合」；`UsageOf` 读一份完整答复（compact 端点透传的那种）里的用量。

## 包

| 包 | 做什么 |
|---|---|
| `protocol`（本目录） | `Inspect` 读出请求摘要，见下文「请求摘要」；`Prepare` 把请求改写给同格式的端点，见「同格式改写」；`Relay` 把上游的流转给客户端、计量、写结尾，见「转发（草图）」；给不要流的客户端则聚合成整个响应体，见「非流式聚合」 |
| `internal/sse` | 把上游的 SSE 字节流读成完整事件，逐个严格校验后交出事件的 data |
| `internal/streamcheck` | 按格式检查一路流的结构：事件顺序、把增量挂到部件上的序号、恰好一个结尾；不看内容。`Sample` 按下文「录制样本」的约定检查一个样本的三个文件 |
| `internal/record` | 开发用的命令（`just record`）：向真实上游发一个请求，把流存成样本，见下文「录制」 |

## 请求摘要

`Inspect(f, body)` 一次扫描读出计费准入和路由要从请求里知道的全部事实，返回值 `Request`，不借用 `body`。它按 token 读，不把请求体解成树，不读的成员只检查是合规的 JSON，所以它自己的分配次数与请求体大小无关；`jsontext` 查重复键时，超过 64 个成员名或 1 KiB 名字的对象每个名字分配一次，这部分随这类宽对象的成员数增长，`jsonx.Validate` 同样如此（[性能的「调用路径上的规则」](../../docs/performance.md#调用路径上的规则)）。`Request` 用目录的计费项和服务等级表达，不引用 billing：模块依赖里 protocol 在 billing 之下。字段名取自 Anthropic《Messages API reference》《Service tiers》《Fast mode》《Extended thinking》《Compaction》、OpenAI《Create chat completion》《Responses API reference》《Prompt caching》《Compaction》、OpenRouter《Reasoning tokens》《Prompt caching》（2026-10-14 读取）；`search_result` 取自 Anthropic《Search results》（2026-10-15 读取）：用户消息顶层和 `tool_result` 里都可以有，`content` 是文本块数组，所以沿 `content` 进入的部件规则照常适用。

| 字段 | 谁用 | Messages | Responses | Chat |
|---|---|---|---|---|
| `Model`、`Stream` | 路由认模型；gateway 定回答方式 | `model`、`stream` | 同左 | 同左 |
| `Models`：OpenRouter 的备选模型，最多 8 个，多于 8 个按请求无效 | gateway 在主模型之后认备选，交给路由 | `models` | 同左 | 同左 |
| `MaxOutput`：0 是没给，用模型的默认值 | [计费的「预留与结算」](../../docs/domains/billing.md#预留与结算)的首段输出；路由的上下文过滤 | `max_tokens` | `max_output_tokens` | `max_tokens`、`max_completion_tokens` 取大 |
| `Choices`：每个回答各有一份输出上限 | 同上 | 1 | 1 | `n` |
| `TextBytes`：请求体字节数减去图片、文件的数据；`Images`、`Documents`：部件个数 | 输入上界：文本 token 不多于字节数，图片按单张、文件按单页的最大值 | `image`、`document` 部件，含 `source.type` 是 `content` 的文档里的部件；`source.type` 是 `base64` 时 `data` 是数据；`search_result` 的 `source` 是字符串（URL 或标识），按值的形状认，不当对象读 | `input_image`、`computer_screenshot` 的 `image_url` 是 data URL 时、`input_file` 的 `file_data` | `image_url.url` 是 data URL 时、`file.file_data` |
| `InputItems`：输入可能按哪些计费项收费 | 输入按其中最贵的单价预留 | `input`；有 `cache_control` 加 `cache_write`，其 `ttl` 是 `1h` 再加 `cache_write_1h` | `input`、`cache_write`：不写标记也会隐式缓存，GPT-5.6 起写入按输入价的 1.25 倍收，模型收不收由价目表定 | 同 Responses，再按 OpenRouter 写法的 `cache_control` 加 |
| `ServiceTiers`：可能以哪些等级服务 | 按其中最贵的等级预留；路由排除没有这些等级的端点 | `service_tier` 的 `auto` 或不写 → standard、priority（有 Priority Tier 容量时就用），`standard_only` → standard；`speed: "fast"` → fast，优先于前者 | `auto` 或不写 → standard、priority（项目设置的等级），`default` → standard，`flex`、`priority` | 同 Responses |
| `WebSearches`、`CodeExecutions`：0 是没给，用模型的默认最大次数 | 按次项目的最大次数 | 各 `web_search_*` 工具的 `max_uses` 相加 | `max_tool_calls`，只给请求启用了的那种工具 | 不限 |
| `Compacts` | 开了服务端压缩，会多采样一次，billing 的 `Demand` 据此乘 2 | `context_management.edits` 里有 `type` 以 `compact_` 开头的项（`compact_20260112`）；只清理内容的 `clear_*` 不算 | `context_management` 里有 `type` 为 `compaction` 的项 | — |
| `HidesReasoning` | 推理不在流里，进度看不到，要一次预留足 | `thinking.display` 是 `omitted` | — | `reasoning.exclude` 是 true |
| `Previous` | gateway 把它指向的历史计入输入 | — | `previous_response_id` | — |
| `Provider`：OpenRouter 的 `provider` 对象，原样的字节，至多 4 KiB，没写或 `null` 为空；`Inspect` 只检查它是对象、大小在界内，含义由 gateway 读 | gateway 转成路由的覆盖条件（[gateway 的「入口与准入」](../gateway/README.md#入口与准入)） | `provider` | 同左 | 同左 |
| `Needs` | 路由的能力过滤（[路由的「一个请求的路由步骤」](../../docs/domains/routing.md#一个请求的路由步骤)） | 见下 | 见下 | 见下 |

`Needs` 的能力：`Images`、`Documents` 同上；`PromptCache` 是有 `cache_control`；`StructuredOutput` 是回复格式（Messages 的 `output_config.format`、`output_format`，Responses 的 `text.format`，Chat 的 `response_format`）的 `type` 为 `json_schema` 或 `json_object`；`Reasoning` 是推理设置（Messages 的 `thinking`，Responses、Chat 的 `reasoning`）没有说关（`type: "disabled"`、`effort: "none"`、`enabled: false`），或 Chat 的 `reasoning_effort` 不是 `none`；工具按 `type` 分：Messages 的 `web_search_*`、`code_execution_*`，Responses 的 `web_search*`、`code_interpreter` 是 `WebSearch`、`CodeExecution`，其余工具都是 `Tools`；Chat 有 `web_search_options` 是 `WebSearch`。

- **只按位置读**：从顶层出发，只沿消息、条目、部件的 `content` 和 `output`（Messages 的 `tool_result` 和 `document` 的 `source.content` 里也有部件）进入部件；Messages 和 Responses 部件的 `content`、`output` 也可以是单个部件，如服务端工具的结果、`computer_call_output` 的截图，按值的形状认，因为成员顺序不定，读到它时还不知道外层的 `type`；工具的 schema、工具调用的参数从不进入，所以客户端数据里叫 `cache_control`、`type`、`max_uses` 的键不算数。
- **出错返回 `ErrInvalid`**（gateway 回 400）：不是 jsonx 接受的 JSON（非法 UTF-8、重复键、超过 `jsonx.MaxDepth`、值后还有数据）、不是对象、没有 `model`；读的成员类型不对、`provider` 超过 4 KiB、计数不是非负整数、同类工具的 `max_uses` 相加超出 int64；`service_tier`、`speed` 是表里没有的取值，因为定不了价。`null` 等于没写。错误说明是我们的话：JSON 语法错是 "Invalid request: invalid JSON request body"（new-api 的同一句），类型不对的值用 JSON 指针指出位置，解码库的原话（偏移量、Go 类型名）不外传（`bodyFault`，`TestAnInvalidBodyIsDescribedInOurWords`）。

## 同格式改写

`Prepare(f, body, t, w)` 把 `Inspect` 接受过的请求体改写给同格式的端点，返回新分配的请求体。它和 `Inspect` 一样按 token 走一遍，只认顶层成员和 Messages 的 `metadata`，按位置分段拷贝原文，所以下表之外的字节（空白、键序、转义写法、思考块的签名、`redacted_thinking`、`encrypted_content`、压缩项）原样到达上游。Chat 的改写取决于端点的方言（推理载体、`cache_control`、推理参数的写法，也包括没写输出上限时补上 `Target.MaxOutput`），随 Chat 的转换一起加入，现在返回 `*NotSupported`，网关因此跳过 Chat 端点（[接入的「转发规则」](../../docs/domains/gateway.md#转发规则)）。`Target.Compact` 时不是生成请求，而是压缩请求：只用下表里 `model`、路由参数和标识那三行，什么都不补（见下），目前只有 Responses 有。字段取自 Anthropic《Messages API reference》、OpenAI《Create a model response》《Compaction》（2026-10-14 读取）。

| 成员 | Messages | Responses | 理由 |
|---|---|---|---|
| `model` | 换成 `Target.Model`，端点的名字 | 同左 | |
| `provider`、`models`、`route`、`transforms`、`plugins`、`usage`、`session_id`、`axiom` | 删除 | 同左 | OpenRouter 的路由参数和本站扩展，gateway 已经用过；官方接口拒绝不认识的成员 |
| `stream` | `Target.Streams` 时写成 `true`，没有就加上；否则原样 | 同左 | gateway 总是以流读上游，非流式客户端由它聚合（[接入的「转发规则」](../../docs/domains/gateway.md#转发规则)）；路由对生成请求只保留能流式的端点，`Streams` 为假时保持客户端的写法是协议层的约定（压缩请求用不到它，见下） |
| 会话和终端用户的标识 | `metadata.user_id` | `prompt_cache_key`、`user`、`safety_identifier` | 换成 `Rewrites.Identity` 给出的派生值（[接入的「转发规则」](../../docs/domains/gateway.md#转发规则)）；`null` 不动 |
| `previous_response_id` | — | 删除；`Rewrites.History` 的条目按原样、从旧到新放到 `input` 最前：`input` 是数组就插在开头，是字符串就先换成一条 user 消息，没有或 `null` 就以历史为 `input` | 上游不保存状态，由本站拼回历史（[接入的「Responses 的会话状态」](../../docs/domains/gateway.md#responses-的会话状态)）；`instructions` 不随 `previous_response_id` 延续（同一文档的 `instructions`），所以只拼 `input` |
| `store` | — | 一律 `false`，没有就加上 | 状态由本站保存 |
| `max_output_tokens` | — | 没有或 `null` 时设为 `Target.MaxOutput`（大于 0 才设）；写了的不动 | 预留按条目的输出上限算，发出去的必须是同一个值（I1）；Messages 的 `max_tokens` 上游必须有，请求没写就是上游的错，不补 |
| `include` | — | `Target.EncryptedReasoning` 为 yes 时保证含 `reasoning.encrypted_content`：没有就加，`null` 换成只含它的数组，数组里没有就插在开头；否则不动 | 上游不保存状态时，推理只能加密后带回下一轮；不推理的模型收到它返回 400 |

- 其余成员一律原样，包括准入读过的 `max_tokens`、`max_uses`、`cache_control`、`service_tier`：预留按 `Inspect` 读到的值算，发出去的必须是同一份。`compaction` 项和 `context_management` 也原样：压缩项要原样带入下一次请求（OpenAI《Compaction》）。
- **压缩请求**（`Target.Compact`）：它不是生成，只改必须改的三样——`model` 换成端点的名字，`prompt_cache_key`、`user`、`safety_identifier` 换成派生值，路由参数删除——其余成员原样，`store`、`include`、`max_output_tokens`、`stream`、`input` 既不补也不改，也不拼历史，因为压缩请求没有这些语义（[接入的「转发规则」](../../docs/domains/gateway.md#转发规则)）。`TestPrepareCompactionRewritesOnlyWhatMustNotGoUpstream` 逐字节核对。
- **输出缓冲**：一次分配，容量是原请求体加模型名、每个可换标识的 `Rewrites.IdentityLen`（`Identity` 返回的最大字节数，转义前；不写按 128；返回更长的值是 gateway 的错，`Prepare` 拒绝，这样不论客户端原来写了什么，容量都能事先定下），两者都按 JSON 编码后最坏的长度算（每字节最多 6 字节）、以及表里新增成员的字节数（`store`、`include`、`max_output_tokens`、包住字符串 `input` 或补出 `input` 的字节），所以改写不会因为缓冲不够把整个请求体再拷一遍；`TestPrepareBuffer`、`TestPrepareBufferEscapes`（模型名和标识全是要转义的字节）、`TestPrepareBufferDefaultIdentityLen`（不写 `IdentityLen`，标识是 128 个要转义的字节）用带长派生标识和历史的大请求核对分配次数与短标识时相同。
- **`PrepareCost(r, body, t, w)`**：`Prepare` 一次最多同时占用多少内存（不含请求体本身），让 gateway 在 `Prepare` 分配之前就向节点的内存预算申请，申请不到就不调用。同格式改写是输出缓冲的容量加请求体的三倍：缓冲的容量事先定得下，另外对标识和 `previous_response_id` 去转义要一块临时缓冲、再转成字符串、缓冲翻倍增长，最坏的是一个很长的标识，实测（2026-10-16）为请求体的 2.5 倍。跨格式转换是 6 × 请求体字节数 + 2048 × （`{` 与 `null` 的个数）+ 1024 × `:` 的个数：转换把字符串多拷几份，并把每个对象读进一个结构（`canonMessage` 224 字节，所在的切片翻倍增长），对象里不认识的成员每个进一个映射项；请求体不说有几个对象和成员，但数括号、`null` 和冒号给出上界（字符串里的只会多算；`null` 元素也各解出一个零值的结构，数组里其他非对象的元素在原地就被拒绝，`Inspect` 先拒绝了）。实测每个 `{}` 消息分配约 1.3 KiB、每个不认识的成员约 485 字节，都在常数之内；评审 #157 指出过只按字节数的 32 倍不是上界（一堆 `{}` 是 74 倍）。结果保留的是 `Upstream.Body` 的容量。不是从别的格式转换的请求不分配，成本为 0。`TestPrepareCost` 用最坏形状的请求体（全是空对象、最小的消息、不认识的成员、全是转义的标识）核对实际分配不超过成本。
- **出错**：标识不是字符串、`metadata` 不是对象、要加 `include` 时它不是数组或含非字符串的元素、有历史时 `input` 不是字符串、数组或 `null`，返回 `ErrInvalid`；`previous_response_id` 非空与是否给了历史不一致是 gateway 的错，返回另一个错误，不当成客户端的 400。历史条目是本站存下的原样 JSON，不再校验。

## 跨格式转换

M1 只把 Chat 客户端的请求转换给 Messages、Responses 端点（[接入的「转发规则」](../../docs/domains/gateway.md#转发规则)）；Messages、Responses 客户端只走原格式的端点，其他方向返回 `*NotSupported`。本节是请求一侧，流见「跨格式的流」，非流式聚合见「非流式聚合」。

- **规范模型**：`canon` 用 jsonx 读 Chat 请求，形状就是 Chat 的：M1 只有 Chat 请求要转换，以后另两种格式的客户端要跨格式时，各自读进同一个模型。凡是原样搬运的字符串都存成原文（`jsontext.Value`），引号和转义照旧写出，所以上游读到的解码值与客户端写的完全相同，不经过解码再编码。
- **读一次**：每条消息的内容在读请求时一次读成部件，两个方向都用它；名字和拒答按下表放进部件，流式交回的推理条目在这里合并。
- **先查形状，再查能不能表达**：函数工具和函数调用的名字、文本部件的 `text` 是字符串，调用的 `arguments` 是字符串，名字和拒答是字符串，JSON schema 回复有名字和对象 schema，否则是客户端的错（`ErrInvalid`）；Chat 有而目标格式表达不了的，是这个端点的不足（`*NotSupported`），gateway 换端点。Chat 定义了而 `canon` 没读的成员，只有取默认值或 `null` 时才放过（`frequency_penalty` 0、`n` 1、`logprobs` false、`modalities` 只有文本等），否则 `*NotSupported`；路由参数和本站扩展读过就丢。工具调用要一一配对、ID 不重复不为空，函数以外的工具和调用（如 `custom`）也是 `*NotSupported`，不当成形状错。
- **`Target` 里的模型数据**：`MaxOutput`（请求没写输出上限时发给上游的上限，也就是它被报价的上限，gateway 取条目的输出上限；Messages 必须有，Responses 写成 `max_output_tokens`，0 为不发）、`Streams`（端点能流式：请求里一律要流，见「同格式改写」）、`Thinking`（`ThinkingAdaptive` 用 `thinking.type: adaptive` 和 `output_config.effort`，`ThinkingManual` 用 `budget_tokens`，`ThinkingNone` 是模型不思考）、`MidSystem`、`UntrustedSystemAfterTools`、`DefaultSamplingOnly`（拒绝非默认的 `temperature`、`top_p`）、`ForcedToolChoice`（接受强制调用工具；厂商把这两件事分开定），Responses 端点的 `CacheBreakpoints`，以及两种格式都用的 `EncryptedReasoning`，由 gateway 从模型规格（`catalog.Dialect`）原样填，不按模型名猜。
- **规格没说不是 no**：这些字段是 `catalog.Claim`（`ClaimUnset`、`ClaimNo`、`ClaimYes`）和 `catalog.Thinking`（零值 `ThinkingUnset`），零值是「没配置」，不是任何一个答案，所以零值的 `Target` 不会悄悄当成自适应思考之类的默认（`TestAZeroTargetAnswersNothing`）。一条规则：请求要用到这个答案时，未设置和明确否定一样返回 `*NotSupported`（原因写明规格没说），gateway 换端点；请求用不到时，未设置无关。具体：要推理而 `Thinking` 不是 `adaptive`、`manual`（`none` 和未设置）；对话中途的系统消息而 `MidSystem` 不是 yes；该消息紧跟工具结果而 `UntrustedSystemAfterTools` 不是 no；非默认的 `temperature`、`top_p` 而 `DefaultSamplingOnly` 不是 no；强制选工具而 `ForcedToolChoice` 不是 yes（思考时无论规格怎么说都拒绝）。这个原因会经 `exhausted` 到客户端（没有端点可用时的 400），所以 `*NotSupported.Reason` 写给客户端看，规格没说的情形只写「模型暂不支持 X」，哪个键没写放在 `Detail`，由 gateway 写进日志，不出节点（`TestAnUnsetClaimIsTheOperatorsToKnow`、gateway 的 `TestAnUnsetSpecKeyIsTheOperatorsToKnow`）。另外两个只是给请求添东西：`EncryptedReasoning` 不是 yes 时不加 `include`，`CacheBreakpoints` 不是 yes 时不写断点，未设置与 no 的写法相同（不加不会出错，加错会被上游拒绝），区别只留在 `Target` 里，让它能被看见。`TestAnUnsetClaimIsNeverAnAnswer`、`TestThinkingIsAsTheSpecSaysOrNotAtAll` 逐项列出三种答案。

| Chat | → Messages（Anthropic《Messages API reference》，2026-10-14） | → Responses（OpenAI《Create a model response》，2026-10-14） |
|---|---|---|
| 开头的 `system`、`developer` 消息 | 顶层 `system` 的文本块 | 原位的 `message` 项，角色不变 |
| 对话中的 `system`、`developer` 消息 | `MidSystem` 为 yes 时原位的 `role: "system"`，须紧跟用户或工具结果、后面是助手或结尾；`MidSystem` 不是 yes 时、紧跟工具结果而 `UntrustedSystemAfterTools` 不是 no 时、位置不合规时 `*NotSupported`（《Mid-conversation system messages》，2026-10-13） | 原位 |
| 第一条非系统消息不是用户 | `*NotSupported` | 照写 |
| 文本 | 文本块，带着 OpenRouter 写法的 `cache_control`；空文本不写 | `input_text`；助手的是 `output_text`；文本块（助手的也一样）的 `cache_control` 在 `CacheBreakpoints` 为 yes 的模型上写成 `prompt_cache_breakpoint: {"mode": "explicit"}`，其余模型（包括规格没说的）和图片、文件、工具上的不写，TTL 不转，规则和对费用的影响见[接入的「转发规则」](../../docs/domains/gateway.md#转发规则)。所以发往 Responses 时 `UsageContext.CacheTTL1h` 总是 false |
| 消息的 `name` | 按 OpenRouter 对非 OpenAI 模型的做法，以 `name: ` 写在第一段文本（或拒答）之前，没有文本就单独一段（OpenRouter《API reference》「Requests」，2026-10-16）；工具结果的不写，它的调用 ID 已经说明 | 同左 |
| 助手的 `refusal`，消息的成员或 `refusal` 部件 | 写成一个文本块：拒答就是助手说的话，Anthropic 自己的拒答也是文本 | 助手消息里的 `refusal` 部件 |
| 别的角色的 `refusal` 部件 | `*NotSupported` | `*NotSupported` |
| 图片 | data URL 写成 base64，其余写成 URL；`detail: low` `*NotSupported` | `input_image`，`detail` 照写 |
| 文件 | PDF 的 data URL 写成 `document`；其他文件、`file_id` `*NotSupported` | `input_file`；`file_id` `*NotSupported` |
| 音频等其他部件 | `*NotSupported` | `*NotSupported` |
| 工具调用 | `tool_use`，ID 见下；`arguments` 必须是 JSON 对象，空串当 `{}` | `function_call`，`call_id`、`arguments` 原样 |
| 工具结果 | 连续的几条合成一条用户消息的 `tool_result` 块 | `function_call_output` |
| 回放的推理（OpenRouter `reasoning_details`） | 先把客户端按流式收到的样子交回的条目合并：类型和 `index` 相同的是一条，文本和摘要按顺序拼接，签名、密文、ID 取给了的那个（OpenRouter《Reasoning tokens》「Streaming Response」，2026-10-15）；只回放 `anthropic-claude-v1` 格式、带签名的 `reasoning.text` 和 `reasoning.encrypted`，放在助手消息最前 | 同左合并后，只回放 `openai-responses-v1` 格式、带密文的推理项，按项 ID 合并摘要和密文：每个 ID 解码一次作为映射键，按出现顺序写出、写原文 |
| 空的助手消息 | 不写（Anthropic 拒绝空内容，前后的用户消息由它合并） | 一个空的 `output_text` |
| 函数工具 | `input_schema`，没有就是空对象；`strict: true` 只在 schema 属于 Anthropic 的子集时写，否则 `*NotSupported` | `strict` 总是写，Chat 没写就写 `false`：Responses 把没写当成 `true` 并改写 schema（OpenAI《Function calling》「Strict mode」） |
| `tool_choice`、`parallel_tool_calls: false` | `auto`、`none`、`any`、`tool`，`disable_parallel_tool_use`；思考时或 `ForcedToolChoice` 不是 yes 时强制选工具 `*NotSupported` | 原样，指定的写成 `{"type":"function","name":…}` |
| 推理强度 | `Thinking` 是 `none` 或未设置时 `*NotSupported`；自适应模型：`low` 到 `max` 照写，`none` 关闭，其余和推理预算 `*NotSupported`；手动模型：预算 = max(min(上限 × 比例, 128000), 1024)，比例按 OpenRouter（xhigh 0.95、high 0.8、medium 0.5、low 0.2、minimal 0.1），必须小于 `max_tokens`；最后一条助手消息调用了工具却没有可回放的推理时 `*NotSupported`（《Extended thinking》） | `reasoning.effort`，要看推理时加 `summary: "auto"`；推理预算 `*NotSupported` |
| `response_format` | `json_schema` 在子集内时写进 `output_config.format`；`json_object` 和子集外的 `*NotSupported` | `text.format` |
| `temperature`、`top_p` | 默认值 1 不写；其余在 0 到 1 之间照写，思考时或 `DefaultSamplingOnly` 不是 no 时 `*NotSupported` | 照写 |
| `stop` | `stop_sequences` | `*NotSupported` |
| `verbosity` | `*NotSupported` | `text.verbosity` |
| 输出上限 | `max_tokens`：`max_tokens`、`max_completion_tokens` 取大，都没有用 `Target.MaxOutput` | `max_output_tokens`，没有时用 `Target.MaxOutput`（大于 0 才写） |
| `user`、`safety_identifier`、`prompt_cache_key` | `user` 的派生值写进 `metadata.user_id`，另两个不写 | 都换成派生值 |
| `service_tier` | `auto` 照写，`default` 写成 `standard_only`，其余 `*NotSupported` | 照写 |
| `stream` | `Target.Streams` 或客户端要流时写 `"stream": true` | 同左 |
| 存储 | — | `store: false`，加密推理的模型带 `include`，与同格式改写相同 |

- **工具调用 ID**：Anthropic 只收 `^[a-zA-Z0-9_-]+$`。合规且不以 `axid_` 开头的 ID 原样；其余写成 `axid_` 加原 ID 字节的十六进制。两类不相交、十六进制一一对应，所以不同的 ID 写出来仍不同；调用和结果用同一个函数，配得上。
- **结构化输出的子集**（Anthropic《Structured outputs》「JSON Schema limitations」，2026-10-15）：没有数值、字符串约束，数组只有 `minItems` 0 或 1，每个对象都写 `additionalProperties: false`，枚举只含简单值，`format` 在它的列表里，`$ref` 只指向本 schema 的定义、不递归（每个定义只展开一次，已查完的不再展开，所以共享定义的 schema 按大小线性检查），`allOf` 里没有 `$ref`。不在子集内就换端点，不靠删约束凑。

## 读 SSE

规则按 WHATWG《Server-sent events》的解析部分（2026-10-14 读取）：

- 行尾 LF、CRLF、CR 都认；一次读取末尾的 CR 要等下一个字节才知道是不是 CRLF，但读已经失败或流已经结束时不会再有字节，就当作行尾，所以读失败前收齐的事件（含以 CR 结尾的）都先交出，再报错。流开头的 BOM 去掉，别处的不去。
- 空行结束一个事件；多行 `data` 用 LF 连接；冒号后只去掉一个空格；没有冒号的行是值为空的字段。没有 `data` 的事件不算事件。
- 只看 `data`：三种格式都在 data 的 `type` 里写事件类型，有中转站不发 `event` 行，所以 `event` 忽略；上游流断了不续传，`id`、`retry` 也忽略；`:` 开头的注释跳过，不交给调用方。
- 每个事件的 data 必须是 `[DONE]`（`sse.Done`，Chat 的结束标记）或 `jsonx.Validate` 接受的一个 JSON 值（每路流用一个 `jsonx.Validator`，复用解码器），否则返回 `ErrInvalid`。不修复：签名和密文原样搬运，修复会改动签名绑定的文本。`[DONE]` 在这种格式里是否合法，由知道格式的调用方判断。
- 上限由调用方给出，计入一个事件的全部字节：各行、行尾和夹在其中的注释；一直不结束的行同样受它约束。超过返回 `ErrTooLarge`。按事件类别区分的上限，在读出 `type` 之后由调用方判断。
- 流结束时：没有未完成的事件返回 `io.EOF`；已经有 data 的事件没等到空行就结束，返回 `io.ErrUnexpectedEOF`。是否正常结束只看有没有收到格式规定的结尾事件，由调用方判断。
- 出错后每次调用都返回同一个错误。`Next` 返回的切片在下次调用前有效；只有一行 `data` 的事件（最常见的）直接返回缓冲里的那一段，不再拷贝一份，所以这段数据不能追加（容量与长度相等），下次调用读上游时缓冲会移动。
- `Poll` 是至多读一次的 `Next`：这次读没凑成事件就返回空，`Pending` 给出已读到、还没交出的字节。每次读至多 16 KiB，所以 `Relay` 每读 16 KiB 至少返回一次。
- 缓冲增长之前先经 `ChargeGrowth` 设的函数申请增加的容量，被拒就不增长、以拒绝的错误失败；`Relay` 用它给节点的内存预算记账。大事件之后缓冲不一直留着：连续 16 个事件都只用到缓冲的四分之一以下，且缓冲超过 64 KiB 时，换成刚好够用的小缓冲（data 里正在用的字节和未解析的字节各搬到刚好容得下它们的缓冲里；单行事件的第一行此时已拷进 data，所以不能只在 data 为空时才缩），并以负数调用该函数归还（不到 16 个就换，会让每个大事件都重新分配一遍）。`AppendEvent` 按规范形式写一个事件（`event` 行只写不会改变分帧的名字），`Relay` 和录制命令共用。

## 检查流结构

`streamcheck.Messages`、`Responses`、`Chat` 各读一路流，经 `sse.Reader` 读出事件（单事件上限 32 MiB：Responses 的快照事件回显请求和全部输出，是最大的一类事件，这是给它的封顶值；按事件类别的上限由转发流的代码判断），结构不合规时返回 `ErrInvalid`。规则只取官方文档写明的（2026-10-14 读取）：

| 格式 | 规则 |
|---|---|
| Messages（Anthropic《Streaming messages》） | `message_start` 恰好一次且在最前；内容块一个接一个，`index` 从 0 连续，每块 `content_block_start` → 增量 → `content_block_stop`，增量和结束只能指向正打开的块；已知块类型只收对应的增量（`text`：`text_delta`、`citations_delta`；`thinking`：`thinking_delta`、`signature_delta`；`tool_use`、`server_tool_use`：`input_json_delta`）；块可以没有增量（`fallback` 块就没有）；所有块关闭后才有 `message_delta`，至少一个，之后不再开始新块；`message_stop` 结尾；`ping` 随处可有；`error` 事件结束流 |
| Responses（OpenAI《Responses streaming events》） | 每个事件带 `sequence_number` 且递增；`response.created` 在最前；输出项的 `output_index` 从 0 连续，项内的内容部件（`content_index`）、摘要部件（`summary_index`）同样连续；部件在项之前完成，项在回复之前完成，`output_item.done` 带着所关闭的项的 `id` 和类型；凡是指明了项、部件或 `item_id` 的事件，指向的都必须正打开，只有 `item_id` 的未列出事件按 id 找项；带类型的增量必须指明所属部件，且与所指部件或项的类型一致（如 `output_text.delta` 带 `content_index`、只给 `output_text` 部件，`reasoning_summary_text.delta` 带 `summary_index`、只给 `reasoning` 项的 `summary_text` 摘要部件，`function_call_arguments.delta` 只给 `function_call` 项）；`response.completed`、`response.incomplete` 时全部项已完成，且回复的 `output` 条数等于流里加入的项数；`response.failed` 或 `error` 也结束流 |
| Chat（OpenAI《Chat Completions streaming events》《Create chat completion》） | 每块都是 `chat.completion.chunk`，`id` 全程相同；选项给出 `finish_reason` 后不再有增量；工具调用的序号要么续写已开始的调用，要么恰好开始下一个，开始时带 `id` 和函数名；用量块在全部选项结束后、`choices` 为空，之后只能是 `[DONE]`；`[DONE]` 在全部选项结束后出现一次，是唯一的正常结尾；顶层带 `error` 的块（OpenRouter《Errors》的流中报错）结束流：OpenRouter 发完它就断开，之后至多再有一个 `[DONE]` |

三种格式都：结尾之后再有事件、流在结尾之前结束，都不合规。未列出的事件类型照常接受，只按通用规则检查（Responses 的序号和所指的项、部件）。`[样本]`：`response.incomplete` 时项是否都已完成，官方文档没有写明，按 `response.completed` 的规则检查，等录到达到输出上限的流再确认。

## 转发（草图）

`Prepare` 已合并（P4，#78），这里的草图扩展它；`Relay` 已有三种格式的同格式转发（P5）和给 Chat 客户端的跨格式转发，`RenderError` 已实现，非流式聚合见下文「非流式聚合」。gateway 怎么用它们见 [gateway 的「Relay 的所有权」](../gateway/README.md#relay-的所有权)。

```go
func Prepare(r *Request, body []byte, t Target, w Rewrites) (Upstream, error) // *NotSupported → gateway 跳过这个条目
type UsageContext struct{ CacheTTL1h bool; Tier catalog.ServiceTier } // 实际发往上游的请求里的缓存断点与明确写的等级，Upstream 带出
func NewRelay(client, upstream Format, body io.Reader, u UsageContext, o RelayOptions) *Relay
func (r *Relay) Next() (Step, error) // 阻塞读；取消 attemptCtx 关闭 body 即返回
type Step struct{ Out []byte; Kind StepKind } // StepBuffered | StepCommit（Out 含暂存的开头）| StepData | StepProgress（未成事件，Out 为空）| StepHeartbeat | StepUpstreamDone
func (r *Relay) Progress() Progress // 值类型、不分配：ContentBytes、PartialBytes、Searches、已报告的数，以及其中哪些已是最终值（Final）
func (r *Relay) Usage() Usage       // Segments []Segment{Usage catalog.Usage; Reported catalog.ItemSet}，每段各自已是最终值的报告项；Invalid
func (r *Relay) Finish(f Final) []byte
func (r *Relay) Fail(e Error, f *Final) []byte // f 为 nil：结算没提交，结尾不带费用
func (r *Relay) Release()                      // 归还向节点预算申请的量，幂等；非聚合的 Finish、Fail 自己调，聚合的由调用方在写完响应体后调
func KeepAlive(f Format) []byte
func RenderError(f Format, e Error, fin *Final) (status int, body []byte)
type RelayOptions struct{ ID, Model string; Created int64; Memory *Memory; Aggregate bool } // Aggregate：客户端没要流，Next 的 Out 为空，Finish、Fail 返回整个响应体
func (r *Relay) Status() int // 聚合时，Finish、Fail 返回的响应体的 HTTP 状态
type Final struct{ Usage Usage; Cost platform.Exact; Currency platform.Currency; Estimated, Pending bool }
```

- **`Prepare`**：`Prepare(r *Request, body, t, w) (Upstream, error)`，`r` 是 `Inspect` 读出的摘要，`Target.Format` 是端点的格式。`Upstream` 带发往端点的请求体和 `UsageContext`：请求里有 1 小时的缓存断点（摘要的输入计费项含 `cache_write_1h`），以及请求只写了一个服务等级时的那个等级，写了 `auto` 或没写时按标准档。`ErrInvalid` 仍是客户端的错误（gateway 回 400），不换端点；`*NotSupported` 表示这个端点表达不了这个请求，gateway 跳过这个条目、不算尝试。同格式改写沿用 `TestPrepare`、`TestPrepareSamples`、`FuzzPrepare`；`TestAllocs`、`BenchmarkPrepare` 只计 `Prepare` 自己的分配。
- **`Inspect` 的后续**：billing 的 `Demand` 要请求里的事实来算内部采样次数：启用了哪些服务端工具、各自写没写最大次数，以及是否开启了服务端压缩（Anthropic 的 `context_management` 里的 `compact_*`、Responses 的 `context_management` 里的 `compaction`）。`Request` 的 `WebSearches`、`CodeExecutions`、`Compacts` 已交出这些事实，乘出采样次数是 billing 的事。
- **拉取式**：调用方循环调用 `Next`。`Next` 每做一次底层读（至多 16 KiB）就返回；读到的字节还没凑成完整事件时返回 `Progress`，已到的原始字节记进 `Progress().PartialBytes`，所以调用方可以在每次读之后计量，一个越过当前段的大事件、一个持续到达却迟迟不结束的事件，都在读的过程中被看到。
- **已报告的数是否最终**：输入侧（输入、缓存读写）一经报告就是最终值；输出侧（输出、推理、搜索）在格式规定的最后一个用量事件（Messages 带 `stop_reason` 的 `message_delta`、Chat 的用量块、Responses 的结尾快照）之前报告的只是下界，例如 Messages 在 `message_start` 里报 `output_tokens: 1`（`testdata/messages/text/upstream.sse`）。`Progress` 逐项标明哪些已是最终值，billing 据此决定是否还要按字节上界计量（[billing 的「接口」](../billing/README.md#接口草图)）。
- **切片的有效期**：`Next`、`Finish`、`Fail` 返回的切片在下一次调用 `Relay` 前有效；`Relay` 只由一个 goroutine 调用。
- **保活**：`KeepAlive(format)` 返回预生成的字节（Messages 是 `event: ping`，Chat 和 Responses 是注释行），调用方在已提交之后自己写，不经过 `Relay`。
- **`UsageContext`**：用量归一化要知道实际发往上游的请求里有没有 1 小时的缓存断点、明确写了哪个服务等级。Anthropic 没给 `cache_creation` 明细时按它归类，规则以 [catalog 的用量折算](../catalog/README.md)为准。
- `Final.Pending` 为真时（结算写不进），结尾带 `axiom.billing_pending`、不带费用（[接入的「用量与费用」](../../docs/domains/gateway.md#用量与费用)）。

已实现的同格式转发。状态机与格式无关（`relay.go`），每种格式一个读取器（`messages.go`、`responses.go`、`chat.go`）给每个事件定角色：

| 角色 | 处理 | Messages（Anthropic《Streaming messages》） | Responses（OpenAI《Responses streaming events》） | Chat（OpenAI《Chat Completions streaming events》） |
|---|---|---|---|---|
| 内容 | 第一个是提交点 | `content_block_delta`；带内容的 `content_block_start`（工具调用、服务端工具结果；空的文本、思考块开头不算） | 每个 `*.delta`；`response.output_item.done`（有的项一次给全，如只有密文的推理项） | 有正文、推理、拒答或工具调用的块；只有 `role` 的块不算 |
| 心跳 | 提交前丢弃，之后原样给出（`StepHeartbeat`） | `ping` | — | — |
| 扣住 | 留到正常结束，写在结尾之前 | 较早的 `message_delta` | — | 每个选项的 `finish_reason`：从块里拆出，块里换成 `null` 照常转发，按选项另写一块扣住，客户端据此认为这个选项完整；拆完后按成员判断还剩不剩东西：选项里除 `index`、`finish_reason` 外有非 `null` 的成员（如 `logprobs`），或 `delta` 里有非 `null` 的成员（如另一个选项的 `role`），就照常转发，否则不再转发；不按计入的内容字节判断，零字节的成员也要送到 |
| 结尾 | 扣住，结算后 `Finish` 带上费用；之后不带内容的事件（如不认识的类型）跟在它后面写出，之后再来内容是上游出错 | 带 `stop_reason` 的 `message_delta` | `response.completed`、`response.incomplete`，同时是正常结束 | 用量块；由本站按结算重写，见下 |
| 正常结束 | 返回 `StepUpstreamDone` | `message_stop`，之前必须有结尾 | 同上 | `[DONE]`，开始过的选项都已给出 `finish_reason`；没有用量块也行 |
| 上游出错 | 返回 `ErrUpstream` | `error` | `response.failed`、`error` | 顶层带 `error` 的块 |

其余事件都按顺序写出，提交前暂存。日期：Messages 2026-10-14，OpenAI 两种 2026-10-14，OpenRouter《Streaming》2026-10-15。

- **改写**：Messages 的 `message.id`、Responses 快照里 `response.id`、Chat 每块的 `id` 换成调用编号，对应的 `model` 换成客户端请求的名字。Chat 每块都有这两个成员，原地替换、不重新扫描；带 `provider`（OpenRouter 写上游是谁）或 `axiom` 的块才整块改写，删掉它们。`usage` 只留官方的用量成员（Messages：`input_tokens`、`cache_read_input_tokens`、`cache_creation_input_tokens`、`cache_creation`、`output_tokens`、`output_tokens_details`、`server_tool_use`、`service_tier`、`speed`、`inference_geo`、`iterations`，Anthropic《Messages API reference》2026-10-15；Responses：`input_tokens`、`input_tokens_details`、`output_tokens`、`output_tokens_details`、`total_tokens`），中转站的 `cost` 之类删掉；`usage: null` 在结尾事件里删掉（结算在那里写用量对象），在之前的事件里（Responses 的 `response.created`、`response.in_progress`）原样保留，OpenAI 如此写（`TestRelayResponsesKeepsANullUsage`）；上游写的顶层 `axiom` 删掉，免得与 `billing_pending` 重复。其余字节原样。
- **结尾的费用**：Messages 和 Responses 在结尾的 `usage`（Responses 是 `response.usage`）末尾加 `cost`、`cost_currency`，暂估再加 `cost_estimated: true`，没有 `usage` 就在事件顶层加一个；Chat 的用量块由本站从结算写出：`usage` 按 Chat 的字段从回复段渲染再加费用，`choices` 与 OpenRouter 一样是一个不带内容、重复 `finish_reason` 的选项（很多客户端对每块都读 `choices[0]`），上游没发用量块也照样写。结算写不进时事件顶层加 `axiom.billing_pending`、不带费用。
- **出错结尾**：Messages 是 `error` 事件，`error.type` 取 Messages 的类型（`credits_insufficient` → `billing_error`，`timeout` → `timeout_error`，`billing_unavailable`、`upstream_error` → `api_error`），`error.error_type` 是本站的类型；Responses 是 `response.failed`（`sequence_number` 接在上游最后一个之后，`response` 带调用编号、`status: "failed"`、`error.code` 为本站的类型），`error_type` 在事件顶层，`response.created` 之前出错时改写 `error` 事件，因为流只能以它们之一开头；Chat 是顶层带 `error`（与 `RenderError` 的 Chat 外壳同一个对象，`appendOpenAIError`：`message`、`type`、`param`、字符串 `code`，`metadata.error_type` 是本站的类型）、`finish_reason: "error"` 的块。三种都带按本格式字段从回复段渲染的 `usage`，结算提交了再加费用；扣住的块不写，提交前出错时暂存的开头随出错结尾一起写出。
- **用量**：按 [catalog 的「计费项与用量」](../catalog/README.md#计费项与用量)折算。几次报告按字段合并：一次报告没写的服务等级、未知计数和 `iterations` 保留之前报告的，没有报告过服务等级就按请求的。Messages：`message_start` 和各个 `message_delta` 的数按项取最新，后来的 0 不覆盖先前的正数，变小判为无效；`iterations` 里每个 `compaction` 条目是自己的一段，`message` 条目已含在顶层里；`speed: "fast"` 是快速模式；`server_tool_use.web_fetch_requests` 进 `Unknown()`。OpenAI 两种格式字段不同、含义相同：输入保留物理总量，缓存读写可重叠、各项不得超过总量；图片及文本明细不再进入未知项，缓存图片先扣交集，缺交集也限制在非缓存余量内。输出扣除推理、音频和图片后是普通输出，输出细项之和不得超过总量；完整公式和两套字段别名见 [catalog 的「计费项与用量」](../catalog/README.md#计费项与用量)。所有计数先检查在 0 到 10¹⁰ 内再计算；Chat 的用量可以在带选项的块里，从块里拿出、换成 `null`，块照常转发；`service_tier` 的 `default`、`flex`、`priority` 对应三档，其余进 `Unknown()`；输入侧（含音频、图片）一经报告就是最终值，输出仍等结尾事件。OpenAI 每侧带总量的报告，只有带这一侧的明细对象或总量变大时才按完整拆分一起替换；总量相同又不带明细的后一次报告保留之前的拆分（例：先报 100 中缓存 80，再报 100 不带明细，仍是缓存 80、普通 20，不是普通 100）；物理总量不得下降（中转清零时保留整份旧快照），避免后补缓存或图片明细时把旧普通 token 再收费；Messages 仍按项合并。三种格式都把映射表外的非零计数放进 `Unknown()`，中转站的 `cost`、`cost_details`、`is_byok` 不是用量，不进。无效时（变小、细项超过总数、不是整数、超出范围、未知的 `iterations` 类型）不中断流，`Usage().Invalid` 写明原因，所有项都不算已报告。`Usage()` 只把最终值算作已报告：流在最后的用量事件之前断了，之前报告的输出只是下界，按准数结算会少收。
- **字节上界**：正文、推理、工具参数、思考的签名（`signature_delta` 的 `signature`，以及思考块开头里带的）和加密推理（`redacted_thinking.data`）按解码后的字节计；Chat 的 `reasoning_details` 和 Messages 的工具 `input` 按原文字节计。签名和加密推理计入：思考按输出 token 计费（[catalog 的「计费项与用量」](../catalog/README.md#计费项与用量)：Anthropic 的 `output_tokens` 含思考、不单列），加密推理的文本客户端读不到，只有这些字节能体现它；签名是思考块的一部分，计入只会让上界更保守；Messages 流转换给 Chat 客户端时沿用这个计数（见下文「跨格式的流」）。Responses 的项完成时（`response.output_item.done`）只加它比自己的增量多出的字节，所以同一段文字不计两次，没有增量、一次给全的项也计入；`web_search_call` 项计一次搜索。
- **错误**：上游断流、流不合规、出错事件都包装 `ErrUpstream`，`FaultOf` 给出它说明了端点的什么（`Fault`）：出错事件按类别（Messages 的 `error.type`，Chat 的 `error.code` 再 `error.type`，Responses 的 `code` 或 `response.error.code`，Anthropic《Errors》、OpenAI《Error codes》2026-10-19）分为服务端错误、限流、上下文超长、内容审核、请求错误和认不出的，聚合时工具调用的参数不是 JSON 是单独一类，其余读失败是断流；类别不带上游的原文，只给 quality 判健康用；读失败（例如取消 `attemptCtx` 关掉响应体）原样返回；内存申请不到返回 `ErrMemory`。出错之后 `Next` 一直返回同一个错误，上游正常结束或写过结尾之后返回 `ErrTerminated`；`Finish`、`Fail` 只有第一次写结尾，之后返回空。
- **内存**：Relay 和它的读取器跨事件留着的一切都按容量在增长之前记账：读 SSE 的两块（缓冲，和多行或跨读的事件才用的 data）、暂存与本步的输出、扣住的事件、结尾、原地替换和整块改写用的缓冲、原地替换的列表、反转义成员名和字符串的暂存、Chat 扣住的结束信号、Responses 每个输出项的计数、Chat 和 Responses 记下的服务等级；Chat 已开始的选项存在映射里，没有容量可查，每项按 48 字节估（推导见 `choiceSize`）。前 64 KiB 是 gateway 每路记下的基础额度，超出的向 `RelayOptions.Memory`（节点预算）申请（缓冲缩小时 `held` 随之减少，已向节点预算申请的不退，留给之后的增长，到结束才归还），`Memory.Charge`、`Refund` 也是 gateway 给请求体记账的接口；`Finish`、`Fail` 时全部归还（聚合例外，见「非流式聚合」的容量）。只在一个事件内用的副本（成员名、解出的用量结构）以事件上限为界、下一个事件前就放掉，不记账。读 SSE 的校验器跨事件留着的解码器同样不记账：它只为不超过 2 KiB 且嵌套不超过 8 层的事件留着（`jsonx.keepMax`、`keepDepth`）：解码器给读过的每一层嵌套留一张成员名表，只有之后的文档再到那一层才清掉，所以留着的是各层的表之和，层数不同的文档可以把各层都填上，只限文档大小界不住（深度递减、每份 200 个成员的 256 份文档，实测留着 1.9 MB）；两条限制合起来，上界是 8 张至多 2 KiB 名字的表，为填满它而造的文档实测约 120 KiB，真实的事件远小于此；更大或更深的事件用一次性的解码器。计量器要活到结算之后，所以给它定上限并在开始时先记 16 KiB（`meterMemory`）：未知用量字段回复段和压缩段各最多 16 个，字段名和值各最长 128 字节，压缩段最多 8 个；超出就判为用量无效，按估算结算，与其他无法采用的用量一样。读失败后 gateway 先结算再调 `Fail`，期间 Relay 还活着，所以失败时清空全部读取状态（读 SSE 的、解码器和成员名、各格式的暂存、扣住的、结尾、改写的），只留 `Fail` 要写的暂存开头和计量器，持有量改为这两者并归还其余。单个事件上限：Messages、Chat 1 MiB（增量和控制事件的上限），Responses 32 MiB（快照事件回显请求和全部输出，取「检查流结构」的封顶值）。

## 跨格式的流

`NewRelay(Chat, Messages 或 Responses, …)` 把上游的流转换给 Chat 客户端，其余跨格式的组合第一步就失败。每个转换器（`chatfrommessages.go`、`chatfromresponses.go`）持有上游格式自己的读取器：计量、内容字节上界和上游出错都由它按同格式的规则判断，只是不做改写；转换器再走一遍同一事件的数据，写出 Chat 的块。块的 `id`、`model` 是调用编号和客户端请求的名字，`created` 是 `RelayOptions.Created`。结束原因的块扣到正常结束，用量块由本站从结算写出，与同格式的 Chat 相同，`[DONE]` 结束。字段取自 Anthropic《Streaming messages》、OpenAI《Responses streaming events》（2026-10-14 读取）、OpenRouter《Reasoning tokens》「Streaming Response」（2026-10-15 读取）。

| 上游 | Chat 客户端得到 |
|---|---|
| `message_start`、`response.created` | 一个 `role: "assistant"`、`content: ""` 的块，不是提交点 |
| 文本：`text` 块和它的 `text_delta`，`output_text` | `content` |
| Responses 的拒答 | `refusal` |
| 思考 `thinking_delta`，推理文本 `reasoning_text`，推理摘要 `reasoning_summary_text` | `reasoning` 和 `reasoning_content`（同一段文本：前者是 OpenRouter 的名字，后者是 new-api、DeepSeek 的，见 gateway.md「决策与理由」），再加一条 `reasoning_details`：`reasoning.text` 或 `reasoning.summary`，带 `format`（`anthropic-claude-v1` 或 `openai-responses-v1`）和 `index`；Responses 的还带项 ID |
| 签名 `signature_delta` | 同一 `index` 的 `reasoning.text` 条目带上 `signature`，只给开头的思考（见下） |
| `redacted_thinking`；推理项的 `encrypted_content`（只在 `output_item.done` 里） | `reasoning.encrypted` 条目，`data` 是密文；Responses 的带项 ID |
| `tool_use` 块，`function_call` 项 | `tool_calls`，序号从 0 起；`id` 是 `tool_use` 的 ID 或 Responses 的 `call_id`（下一轮交回时就是它）；名字在第一段，`arguments` 随增量 |
| `tool_use` 的 `input` | 后面有 `input_json_delta` 就丢掉（官方给 `{}`）；没有增量就在块结束时作为整段参数（sub2api 遇到过只给 `input` 的上游） |
| `ping` | 提交后写 Chat 的保活注释 |
| 服务端工具的块和项、引用（`citations_delta`、`output_text.annotation.added`）、不认识的块或项、没有 ID（`tool_use` 的 `id`、`function_call` 的 `call_id`）或名字的工具调用（缺失、`null`、空串）、结束原因之后又来的内容 | `ErrUpstream`：转换出来的请求不会要它们，Chat 也没有地方放 |

| 上游的结束 | `finish_reason` |
|---|---|
| `end_turn`、`stop_sequence`；`response.completed` 没有函数调用 | `stop` |
| `tool_use`；`response.completed` 有函数调用 | `tool_calls` |
| `max_tokens`、`model_context_window_exceeded`；`response.incomplete` 因为 `max_output_tokens` | `length` |
| `refusal`；`response.incomplete` 因为 `content_filter` | `content_filter` |
| `pause_turn`（只随服务端工具出现）、不认识的停止原因、`message_stop` 之前没有停止原因；`response.incomplete` 因为别的原因 | `ErrUpstream` |

- **推理的回放**：一条回复里，排在所有非思考块之前的思考块给出签名和 `redacted_thinking`；排在正文或工具调用之后的只给文本、不给签名，`redacted_thinking` 不给。Chat 的助手消息表达不了「正文之后又思考」，回放时推理只能放在最前，而 Anthropic 的思考块绑定它之前的全部内容（《Preserved thinking》），挪了位置的块会被拒绝。客户端按块收到的条目原样交回或合并后交回，请求一侧都按类型和 `index` 合并（「跨格式转换」）；`TestConvertRoundTrip` 核对流 → 客户端 → 下一轮请求的往返。
- **快照比对**：Responses 的部件会在 `*.done`、`content_part.done`、`reasoning_summary_part.done`、`output_item.done` 里再给整段。每个部件（项序号、部件序号、种类）记已发出文本解码后的长度和 SHA-256，不留原文：与已发出的相同就忽略；已发出的是它的严格前缀，就发出余下部分（重新编码，解码值不变）；从没发出过就原样整段发出；其他是 `ErrUpstream`。每一层都比也不会重复，只在 `output_item.done` 给全文的上游也完整。
- **内容字节**：Messages 沿用上游格式读取器的计数，与同格式转发相同（同一段推理在 `reasoning` 和 `reasoning_details` 里各出现一次，按转换后的字节计会重复）。Responses 的部件可能只在快照里给出，读取器要到 `output_item.done` 才计入，所以转换器自己计：每一段发给客户端的文本（增量或快照补出的，解码后的字节）在发出的事件里计入并记在项上，重复的快照不计；`output_item.done` 再按项的大小补上没被各段覆盖的部分（如密文），因此客户端读到的内容在写出前就已计入，计到的总数与同一条流转发给 Responses 客户端相同。
- **内存**：块的缓冲、各成员拼接中的缓冲、`tool_use` 暂存的 `input` 都在增长前记账；Responses 的项和部件存在映射里，每项按 96 字节、每个部件按 320 字节（含 SHA-256 的状态）估，见 `itemSize`、`partSize`。
- **`RenderError(f, e, fin)`**：流开始之前的错误。状态码和 Messages 的类型按[接入的「错误」](../../docs/domains/gateway.md#错误)的表，一种类型一行（`no_endpoint` 永久的 404、暂时的 503 是名字相同的两个类型）；Chat 和 Responses 用 Chat 的外壳 `{"error":{"message","type","param","code","metadata":{"error_type",…}}}`（`type` 取 Messages 的类型，`param` 没有时为空字符串，`code` 是类型的名字），Messages 用 `{"type":"error","error":{"type","message","error_type",…}}`。`Error.Details` 写在类型旁边（如 402 的 `limit_source`、所需金额）；`fin` 不为空时带 `axiom.usage`：本格式的用量字段（Messages 的 `input_tokens`、`cache_*`、`output_tokens`；Responses 的 `input_tokens`、`output_tokens` 及其 `*_details`；Chat 的 `prompt_tokens`、`completion_tokens` 及其 `*_details`，不混用）加费用，结算写不进时带 `billing_pending`。`Error.Rejection` 是上游拒绝请求本身时说的话（`Message`、`Type`、`Param`、`Code`，已由 suppliers 去掉能指向供应商的内容）：`Message` 成为错误的说明；Chat、Responses 外壳的 `type`、`param`、`code` 取上游给的（没给 `code` 为 `null`），Messages 的 `type` 仍取自己的。没有 `Rejection` 也没有说明时用每种类型自己的说法；流里的出错结尾用同一张表。


## 非流式聚合

客户端没要流时（`RelayOptions.Aggregate`），上游照样以流读（[接入的「转发规则」](../../docs/domains/gateway.md#转发规则)）：读、计量、内存记账都与流式相同，不同的只有输出。`Relay` 把要写给客户端的、客户端格式的事件交给聚合器，`Next` 的 `Out` 恒为空（提交点照常是 `StepCommit`，心跳是 `StepHeartbeat` 但不写保活），`Finish`、`Fail` 返回整个响应体，`Status()` 给出它的 HTTP 状态（还没有结尾时为 0）。每种客户端格式一个聚合器（`aggregatemessages.go`、`aggregatechat.go`、`aggregateresponses.go`），五条路径共用这三个：Messages、Responses 的流转给 Chat 客户端时，聚合器看到的就是 Chat 的块，不知道上游是谁。聚合器收到的是 `Relay` 写出的事件字节（`Finish`、`Fail` 写的结尾也一样，所以带费用），按 `sse.AppendEvent` 的格式切开，多行 data 用换行连接。Chat 聚合器只跳过 `[DONE]` 和空的 data，其余都当 JSON 读（前导空白、制表符、多行都照常），不是对象就是上游的失败；Responses 聚合器收到的项和部件，`type` 不是字符串（或转义不合法）就在收到时以 `ErrUpstream` 失败，转义的类型按它拼出的名字算。字段取自 Anthropic《Messages API reference》、OpenAI《Create chat completion》《Responses API reference》、OpenRouter《Reasoning tokens》（均沿用上文已读的版本，本 PR 没有重读）。

| 客户端格式 | 响应体 | 怎么聚合 |
|---|---|---|
| Messages | `message`：`message_start` 的成员（`content`、`stop_reason`、`stop_sequence`、`usage` 除外），`content`，结尾的 `stop_reason`、`stop_sequence`、`usage`，有 `axiom` 时再加 `axiom` | 块一个接一个，所以整个消息写在一个缓冲区里：文本和思考的增量原位写进块的字符串，字符串到块结束才闭合；`input_json_delta` 的片段反转义后接成 `input`，没有增量就是块开头给的 `input`，开头也没有就是 `{}`，有增量时块结束要检查它是 JSON；签名和 `citations_delta` 的引用攒到块结束再写；开头里文本、思考、签名、`input`、引用以外的成员原样，其他类型的块（服务端工具及其结果、`redacted_thinking`）整块原样。`usage` 是 `message_start` 和各个 `message_delta` 里的 `usage` 按成员合并，后来的覆盖，费用在最后一个里；`delta` 同样合并，所以较早的 `message_delta` 被最后一个覆盖。不认识的事件和增量类型忽略 |
| Chat | `chat.completion`：第一块的 `id`、`created`、`model`，`choices`，最后的 `usage`（含费用），出错时顶层 `error`，有 `axiom`（`billing_pending`）时再加 `axiom` | 选项交错，所以每个选项各存各的片段：`content`、`refusal`、`reasoning`、`reasoning_content` 的字符串内容拼接（不解转义，转义不会跨片段），`tool_calls` 按 `index` 归到一个调用（`id`、`type` 取给出的，`name`、`arguments` 拼接），`reasoning_details` 的条目攒起来、结束时用请求一侧同一个 `mergeDetails` 按类型和 `index` 合并，`annotations` 和 `logprobs` 的 `content`、`refusal` 数组的元素按序拼接。`finish_reason` 取给出的最后一个；有工具调用而没有文本的消息 `content` 为 `null`，`refusal` 没有时为 `null`，`logprobs` 没有时为 `null`。出错结尾（`error` 块）把顶层 `error` 放进响应体，没有结束原因的选项以 `error` 结束。`audio` 和旧的 `function_call` 成员没有聚合 |
| Responses | `response` | 成功：就是结尾快照（`response.completed`、`response.incomplete`）的 `response`，它已带最终的 `output`、`usage` 和费用，原样返回，事件顶层的 `axiom`（结算没写时的 `billing_pending`）加到响应体顶层。失败：`Fail` 写的 `response.failed` 里的 `response`，换上聚合器按事件记下的 `output`（见下） |

- **Responses 成功取快照，不从增量重建**：官方的完整响应就是快照；而官方样本的中间事件被省成一行注释（`testdata/responses/text/meta.json`），增量拼不出最终文本，所以从增量重建的响应体与官方非流式响应对不上。代价是依赖上游的快照带全部输出项：官方如此，[检查流结构](#检查流结构)也要求 `response.completed` 的 `output` 条数等于流里加入的项数。
- **Responses 失败的响应体要有仍在进行中的输出项**（[接入的「错误」](../../docs/domains/gateway.md#错误)）：聚合器按 `output_index` 记每个项：`output_item.done` 给全的项原样，`output_item.added` 给出骨架的项加上之后的 `content_part.added`、`reasoning_summary_part.added` 的部件骨架和 `*.delta` 的文本、`annotation.added` 的注释、`function_call_arguments.delta` 的参数。失败时，没完成的项按骨架重建，`status` 改为 `incomplete`，文本部件写上已收到的文本（`refusal` 部件写在 `refusal` 里），函数调用的参数是已收到的字符串，不修补。响应体其余成员取自 `response.failed`（调用编号、模型名、`error`、`usage`），`created_at` 取自 `response.created`，`error_type` 在顶层，与流里的失败事件一致。序号对不上已记的项的事件不记，与 `Relay` 的内容计数同样处理。
- **失败的状态码**（[接入的「错误」](../../docs/domains/gateway.md#错误)）：

| 失败时 | 状态 | 响应体 |
|---|---|---|
| 还没有内容（提交点之前） | `RenderError` 给的状态码，网关可以换端点重试 | `RenderError` 的外壳，带 `axiom.usage`；没写结算时只有用量，不带费用、不带 `billing_pending` |
| 有内容，Chat | 200 | 已生成的内容，`finish_reason: "error"`，顶层 `error`，`usage` |
| 有内容，Responses | 200 | `status: "failed"`，已生成的 `output`，`usage` |
| 有内容，Messages | 502（D16） | `RenderError` 的外壳，不带内容，用量和费用在 `axiom` 里 |

  聚合器可以拒绝 `streamcheck` 接受、却造不出回复的流（没有 `message` 的 `message_start`、顺序不对的块、不是 JSON 的工具输入、没有 `response` 的结尾），按上游的失败处理：在 `Next` 里是 `ErrUpstream`，在结尾里则 `Finish` 改以 `upstream_error` 失败，结算已写的费用照常在里面。
- **容量**：每个流聚合的片段合计最多 `aggregateLimit` = 16 MiB，算的是缓冲的容量（含消息头、`usage` 这些小片段），翻倍的富余到上限为止，所以 15.5 MiB 的文本放得下（`TestAggregateBudget`）；超出以 `ErrMemory` 结束，是本地的容量错误，不是上游的（`errors.Is(err, ErrUpstream)` 为假），换端点重试不会让它变小。节点预算同样经 `Relay` 的记账申请：要拷成响应体的片段（Chat 的文本、推理、参数）记两倍，要解码合并再写出的 `reasoning_details` 记四倍，Messages 的消息记一倍；Responses 记下的项和部件记两倍（失败时要重建成响应体），成功的快照就是 `Relay` 扣住的结尾事件，不另占，它超过 16 MiB 时在扣住的时候就以 `ErrMemory` 失败。`Finish`、`Fail` 不申请（它们只用已记账的字节），所以 `Relay` 扣住到正常结束才交给聚合器的事件（Chat 的结束选项），在扣住的那一刻就让聚合器按它们要开的选项和结束原因记账（`aggregator.hold`），超限在 `Next` 里就以 `ErrMemory` 结束；结束时全部归还（聚合的 `Finish`、`Fail` 例外：返回的响应体就是这些缓冲，预算留到网关写完响应体后调 `Release` 归还，`Release` 幂等，非聚合的 `Finish`、`Fail` 仍自己归还，`TestAggregateHoldsItsBudgetUntilReleased`）；失败后聚合器的片段留到 `Fail`（Messages 例外，它不带内容，立即放掉），持有量算进失败后的 `held`。
- **分配**：聚合器按位置读事件，不把事件解成树；每个事件的分配次数有常数上界，缓冲翻倍带来的分配随长度对数增长，与增量的大小无关（`TestAggregateAllocs`）。
- **整份答复的用量**：`UsageOf(body)` 读一份完整的 Responses 回复（`/responses/compact` 的答案，gateway 把回复原样透传、只改写请求）顶层的 `usage` 与 `service_tier`，按与流的结尾相同的口径折算成计费项；没有 `usage` 是上游没报告用量（不是没用模型），由调用方按暂估收，读不出或折算不了判为 `Invalid`，同样按估算（[计费的「免单规则」](../../docs/domains/billing.md#免单规则)）。`TestUsageOf` 覆盖拆分、等级和两种无效。
## 录制样本

协议的测试用固定样本回放（[总体架构的「协议转换」](../../docs/architecture.md#协议转换)）。样本放在 `testdata/<上游格式>/<场景>/`，上游格式是 `messages`、`responses`、`chat`，场景名用英文小写和连字符。每个场景三个文件，已实现的转发方向再加一个黄金输出：

| 文件 | 内容 |
|---|---|
| `request.json` | 发给上游的请求体 |
| `upstream.sse` | 上游返回的流。官方样本是原样的字节，`.gitattributes` 让 `*.sse` 不做换行转换，所以 CRLF 的流也原样保存；录制的样本是按事件重建的规范形式，见「录制」 |
| `to-<上游格式>.json` | 只在 Chat 样本里：`request.json` 转换给该格式端点的请求体，按测试里固定的模型名、输出上限和派生值；`go test ./internal/protocol -update` 重写 |
| `client-<客户端格式>.sse` | `Relay` 把这个流转给该格式客户端的输出，按测试里固定的调用编号、模型名和费用；`go test ./internal/protocol -update` 重写，PR 里审差异 |
| `aggregate-<客户端格式>.json` | `Relay` 把这个流聚合给不要流的该格式客户端的响应体，条件同上；Messages、Responses 的流给 Chat 客户端的叫 `aggregate-to-chat.json`；`go test ./internal/protocol -update` 重写 |
| `meta.json` | `source`：官方样本是来源的网址，录制的是 `recorded from a live upstream: POST <官方端点>`，不写上游的地址；`date`：取得的日期（`YYYY-MM-DD`）；`note`：与上游原样不同的地方，没有就不写 |

- `testdata/assembled` 里是按官方事件形状组装的流，不是样本，只给转换的黄金输出和往返测试用：`responses-reasoning-tool.sse` 是一个带摘要和密文的推理项、一个函数调用，官方没有带这两样的 Responses 完整流。它的非流式黄金输出是 `responses-reasoning-tool.aggregate-to-chat.json` 和 `responses-reasoning-tool.aggregate-responses.json`。
- 只收两种来源：官方文档里的完整流，以及用合成提示词录制的真实上游。官方示例常把中间事件省略成一行注释（`:` 开头），读 SSE 时注释本来就跳过，所以原样保留，在 `note` 里写明省略了什么；网页渲染改动过的空白按接口的原样还原，同样写进 `note`。
- 样本里不能有密钥、服务器地址、用户数据；`just secrets` 在推送前扫描。样本只存请求体和流，不存头；录制真实上游时，上游生成的标识换成占位值，在 `note` 里写明（规则见「录制」）。官方示例里的 ID 本来就是示例值，原样保留。
- `streamcheck.Sample` 检查 `meta.json` 有来源和合法日期、`request.json` 是合规的 JSON，并用对应格式的检查通过 `upstream.sse`；`TestSamples` 对每个场景读齐三个文件后调用它，录制工具写盘前也调用它，所以写下的样本一定通过样本测试。官方文档没有给出完整流的情形（Chat 的工具调用和用量块、各格式的出错结尾），只在测试里构造，不放进样本目录。

### 录制

`just record <格式> <场景> <请求文件>`：把请求文件原样发给上游，流读完后在 `testdata/<格式>/<场景>/` 写下三个文件。请求文件的路径从仓库根目录算起，写进 `request.json` 前同样替换 ID。

- **上游**：地址和 key 取自环境变量，没设的再从仓库根目录的 `.env` 读（git 忽略它，变量见 `.env.example`）。变量名和地址写法与两家 SDK 相同：`messages` 用 `ANTHROPIC_BASE_URL`（主机，路径补 `/v1/messages`）、`ANTHROPIC_API_KEY`；`responses`、`chat` 用 `OPENAI_BASE_URL`（以 `/v1` 结尾，路径补 `/responses`、`/chat/completions`）、`OPENAI_API_KEY`。
- **发出前拒绝**：格式不认识、场景名不合规、样本目录已存在（从不覆盖）、没有地址或 key、请求不是严格 JSON 或没有 `"stream": true`、请求里有 key。
- **调用**：不跟随重定向（Go 换主机时只去掉 `Authorization` 和 cookie，`X-Api-Key` 会跟过去），重定向按非 200 失败；连接、等响应头和读流的每一段，10 分钟收不到字节就失败（推理模型思考时可能很久不发数据，所以按空闲计，不限总时长）。非 200 的响应体含 key 时，错误里只给状态，不给响应体。
- **流的重建**：录下的流用 `internal/sse` 的 `Reader` 读成事件（行尾、BOM、注释都按 SSE 的规则处理），每个事件的数据是 `jsonx` 接受的 JSON 或 `[DONE]`。写下的 `upstream.sse` 由这些事件重建：Messages 和 Responses 先写一行 `event: <数据的 type>`（type 含换行等字符时不写这一行，免得改变分帧），再每行数据写一行 `data: `，最后一个空行；Chat 没有 `event` 行。注释、`id`、`retry`、上游原来的事件名和行尾都不保留，所以录制的样本是规范化的副本，不是逐字节的副本，`meta.json` 的 `note` 写明这一点；数据本身除 ID 外逐字节保留（空白、键序、数字写法）。
- **ID 替换**：只换各格式放上游 ID 的位置，以 JSON Pointer 列在 `formats` 的 `ids` 里（`*` 是数组下标）：Messages 流里的 `message.id`、`content_block` 的 `id` 和 `tool_use_id`、出错事件的 `request_id`，请求里 `messages[].content[]` 的 `id`、`tool_use_id`；Responses 流里快照 `response` 的 `id`、`previous_response_id` 和 `output[]` 的 `id`、`call_id`，`item` 的 `id`、`call_id`，事件的 `item_id`，请求的 `previous_response_id` 和 `input[]` 的 `id`、`call_id`；Chat 流里块的 `id` 和 `choices[].delta.tool_calls[].id`，请求的 `messages[].tool_calls[].id`、`tool_call_id`。这些位置取自三种格式文档里的对象形状和 `streamcheck` 读的字段。位置上的非空字符串按解码后的文本查一张两个文件共用的表，按出现顺序换成它的前缀（开头的字母加 `_` 或 `-`，如 `msg_`、`chatcmpl-`；没有就用 `id_`）加序号，如 `msg_1`，只改写这个字符串自己的字节。所以同一个 ID 不论写没写转义，在两个文件里都换成同一个占位值；按前缀计数、包括本来就像占位值的 `call_1` 也换，所以不同的 ID 不会撞上。其他位置一律不动，不论值像不像 ID：工具参数里的 `"id":"call_customer"`、元数据、工具名、正文都原样保留；签名、`encrypted_content` 也不在位置上。录到新的 ID 位置时，把它加进对应格式的 `ids`。
- **key**：配置的 key 原文，或词首的 `sk-` 加 20 个以上 key 字符（两家 key 的格式；base64url 中间偶然出现的 `sk-` 不算）。请求和每个事件的数据里，每个字符串（成员名和值）都解码后查，所以转义藏不住 key；写盘前再查三个文件的原始字节里有没有 key 原文。注释不写进样本，所以不查。非 200 的响应体查原始字节，是 JSON 的再查解码后的字符串。出现在请求里就不发，出现在要写的文件里就不写。
- **写盘**：上游返回非 200、流读不完、超时或超过 64 MiB、流读不成事件（如数据不是 JSON）、`Sample` 不通过，都不写；写到一半失败就删掉样本目录。录完把上游是谁（官方或哪家中转站）、用的模型版本补进 `note`。

## 不变量

| 不变量 | 靠什么保证 |
|---|---|
| 读请求摘要、改写请求比 `jsonx.Validate` 多出的分配次数与请求体大小无关 | `TestAllocs` 对 `Inspect` 的三种格式和 `Prepare` 的两种格式 1 KiB、1 MiB 的请求体，分别在普通对象和含 100 个成员的宽对象下比较两者分配次数之差；`BenchmarkInspect`、`BenchmarkPrepare` 由 `just bench-compare` 与 `main` 比较 |
| 同格式改写只改「同格式改写」表里的位置，其余字节原样；签名、加密内容、压缩项原样到达 | `TestPrepare` 逐字节比较整个请求体，用例含签名和 `redacted_thinking`、转义的思考正文、Codex 压缩后的第二轮（OpenAI《Compaction》的做法，ID 和密文是编的）、表里每条规则；`TestPrepareSamples` 对已提交的样本请求只差模型名（Responses 再加 `store`） |
| 端点能流式时发给它的请求总带 `stream: true`，不能流式的端点保持客户端的写法 | `TestPrepareAsksForAStream` 覆盖同格式与转换、没有/`false`/`null`/`true` 四种写法；`TestPrepareSamples`、`TestConvertSamples` 的目标不流式，黄金输出不变 |
| `UsageOf` 读出的用量与流的结尾同一口径 | `TestUsageOf`：输入拆成缓存读、输出拆成推理、服务等级、没有 `usage` 为空、部件超过总数和不是 JSON 都判无效 |
| 改写后准入和路由读到的摘要不变，输出合规 | 模糊测试 `FuzzPrepare`：`Inspect` 接受的请求，改写结果仍被 jsonx 接受，再读出的摘要除模型名、`Previous` 和 `TextBytes`（随增删的字节变）外完全相同；`Prepare` 返回 `ErrInvalid` 当且仅当把请求体整体解码后看到上面「出错」里的类型错误，所以错拒合法请求和漏拒错误类型都会失败；以 `Inspect` 和 `Prepare` 的表驱动用例（含拒绝的用例）为种子；每条规则都经过变异验证 |
| `Inspect` 接受的请求体 jsonx 也接受；读出的 `model`、`stream` 与完整解码一致；`0 ≤ TextBytes ≤` 请求体字节数 | 模糊测试 `FuzzInspect`，以下面的表驱动用例和样本为种子 |
| 每个字段都按上表读；客户端数据里的同名键不改变摘要 | 表驱动用例：三种格式各读全部字段，加 schema 和工具参数里放同名键的用例；每条规则都经过变异验证 |
| 结果与上游字节怎么分片无关 | 表驱动用例分别整段读、逐字节读、数据与 EOF 同时到达；读失败与最后的数据同时或分开到达，三种行尾下之前完整的事件（含刚好以 CR 结尾的）都交出（`TestReaderReadError`）；模糊测试按随机位置切分，与一次读完比较全部事件和最终错误 |
| 交出的事件要么是 `[DONE]`，要么是合规的 JSON | `Next` 返回前校验；模糊测试对每个事件再校验一次 |
| 一路流的缓冲受上限约束，不随上限本身预先分配 | 上限计入事件的全部字节；测试用一个永不换行的上游，确认在上限处失败；缓冲从 16 KiB 起按实际事件增长 |
| 事件的最后一个字节一到就交出，不等下一次读，不攒够字节 | `TestReaderHandsOverAtOnce`：上游交出一个完整事件后阻塞，`Next` 仍返回它，LF、CRLF、CR（要等到下一个字节）各一条，整段和逐字节各一遍 |
| 读一个长行的时间与行长成正比 | 记住已经扫描过、没有行尾的位置；只有缓冲前部有已消费的字节时才搬移；`TestReaderLineScales` 比较 1 MiB 与 8 MiB 的行读完的最快耗时，长八倍的行五次里没有一次在 24 倍的时间内读完就失败（线性约 8 倍，每次读后重扫或自我搬移是 64 倍；超时的那次读中止，所以退化时很快失败），规则同 `TestPlanScales` |
| 缓冲长齐之后，读一个事件不分配（单行和多行的 data 都是），解码器不是每个事件新建一个 | `TestReaderAllocs` 在子进程里用 `alloctest.Measure` 比较 1000 与 2000 个事件的分配次数（堆先结算、GC 关掉、单核）；基准测试 1 KiB 和 1 MiB 的事件，CI 与 `main` 对比分配次数 |
| 一个大事件不让一路流一直占着它的缓冲，一串大事件也不让缓冲反复重新分配 | `TestReaderShrinks`：512 KiB 的事件（含多行 data 的事件，以及每次读都止于下一个事件的 data 行之后的分片）之后跟小事件，持有量回到 64 KiB 以内并以负数归还；连续的大事件不归还；`TestReaderAppendToEvent` 核对交出的单行 data 不能越过自己的长度写到后面的字节 |
| 提交点之前不向客户端写字节；每路流恰好一个结尾，成功结尾只在上游正常结束后发出；扣住的块不随出错结尾写出 | `TestRelaySteps` 逐步核对步骤的类型；`TestRelayChat` 覆盖 Chat 的用量块、没有用量块、给出 `finish_reason` 后断流、选项没结束就 `[DONE]`、内容结束和用量在同一块、出错块；`TestRelayChatChoices` 核对两个选项交错时各自的内容和工具调用顺序不变；`TestRelayResponses` 覆盖 `response.created` 前后断流和 `response.incomplete`；`TestRelayCommitPoint` 核对哪些块的开头是提交点、计入多少内容和搜索；`TestRelayEndings` 每种结束方式一个用例：正常结束、提交前后的 `error` 事件、结尾前后断流、没有用量事件、结尾后又来内容、结尾后不带内容的事件按上游的顺序写出、事件不是对象、没有内容、两个用量事件都写出；测试的驱动在结束后再调 `Finish`、`Fail`、`Next`，断言不再写出、返回 `ErrTerminated`。模糊测试 `FuzzRelay` 覆盖三种格式：`streamcheck` 接受的上游流，客户端拿到的流也被它接受，并且与怎么分片无关；正常结束的上游流不报错，客户端拿到的内容与上游逐项相同：Messages 每个内容块的事件（含工具块开头的 `input`）、Responses 每个输出项的事件（含只在 `output_item.done` 里整块给出的项）原样相同，Chat 每个选项 `delta` 的每个成员（含 `role`、`reasoning_details`、工具调用的 `id`、`type`）、其余非 `null` 的成员和最后的 `finish_reason` 相同；`TestRelayChatSplitKeeps` 覆盖拆出结束信号后只剩零字节成员的块 |
| 客户端看到调用编号、自己请求的模型名，用量里只有官方的用量成员和本站的费用 | 三种格式的黄金输出；`TestRelayChat` 核对每块的 `id`、`model`，`provider`、上游的 `cost` 和 `is_byok` 不出现；`TestRelayRewrites`：上游的 `cost`、`cost_details`、`axiom` 不出现，三种 `Final`（已结算、暂估、待结算）的结尾逐项比较，结尾没有 `usage`、`usage` 为 `null`、过滤后为空都得到合规的费用 |
| 官方样本转给客户端的输出与黄金输出逐字节相同，整段读和逐字节读结果相同 | `TestRelaySamples` 覆盖全部样本；黄金输出也通过本格式的 `streamcheck` |
| 用量按表折算；输入侧一经报告即最终，输出侧到带 `stop_reason` 的 `message_delta` 才最终，与成员顺序无关；`Usage()` 只报最终值；计数只增 | `TestRelayUsage` 每条折算规则一个用例（含无效的五种）；`TestRelayMeter` 用 `message_start` 报 `output_tokens: 1` 的流，`type` 排在 `delta` 之后；`TestRelayMeterSample` 用官方样本 `messages/text`：提交时输出 1 不是最终值，结束时 15 是；`TestRelayOpenAIUsage` 覆盖 OpenAI 两种格式的细项、超出总数、负数、溢出、未知计数和服务等级；`TestRelayUsageMerge` 核对后来的报告没写的等级、未知计数和 `iterations` 保留 |
| 每次底层读至多 16 KiB 就返回，越过一次读的事件、不结束的事件都在读的过程中计入 `PartialBytes`；字节上界不漏不重 | `TestRelayProgress`；`TestRelayChatContent` 计入各方言的推理、拒答和工具调用；`TestRelayResponsesContent` 核对项完成时只加多出的字节、没有增量的项照样计入、`web_search_call` 计一次搜索；`TestRelaySignatureContent` 核对只有签名或加密推理的流在同格式和转换两条路径上都计入；`FuzzReader` 用 `Poll` 读时每次至多读一次 |
| 每路流持有的缓冲都记账，超出基础额度的在增长前申请、结束时归还；申请不到时提交前什么都不写、提交后以出错结尾结束 | `TestRelayMemory` 三个用例都核对结束时节点预算回到原值；`TestRelayMemoryRewrite` 核对快照改写的缓冲记账且复用；`TestRelayMemoryFailure` 核对失败后结算期间暂存的开头仍记着、其余已归还，`Fail` 后全部归还；`TestChargeGrowth`；`TestRelayMemoryHeld` 遍历 Relay 实际引用的全部切片、字符串和映射，按底层数组去重求和：上千个选项结束的 Chat 块、上千个 Responses 输出项、很长的转义成员名、每块都带的很长的服务等级和读完用量后才出错的块、超出上限的未知用量、`tool_use` 起始事件之后上游立刻出错（读取状态里的事件成员不能留着），每一步引用的都不超过已记账的，失败后引用的不超过 `out` 的容量加计量器的预留；`TestMeterMemory` 核对计量器在上限处不超过预留、超出就判无效；样本在预算为 0 时照常转发，说明基础额度够用 |
| 转发每个事件不比读 SSE 多分配 | `TestRelayAllocs` 对三种格式比较 10 个与 40 个增量的流，1 KiB 与 64 KiB 的增量；`BenchmarkRelay` 由 `just bench-compare` 与 `main` 比较 |
| Messages、Responses 的流转给 Chat 客户端时，每条映射和结束原因都按上表，输出与黄金输出 `to-chat.sse` 逐字节相同，整段读和逐字节读结果相同，都通过 Chat 的检查 | `TestConvertStreams` 覆盖全部样本和 `testdata/assembled` 的流；`TestConvertMessages`、`TestConvertResponses` 每条映射、每个结束原因各一例，含出错的行和快照比对的四种情况；`TestConvertToolCallFields` 核对工具调用缺 ID 或名字（缺失、`null`、空串）时在写出前以 `ErrUpstream` 失败；`TestConvertProgress` 核对快照补出的内容在该事件处就已计入、重复的快照和 `output_item.done` 不重复计，终值与同格式转发相同；`TestConvertKeepAlive`；模糊测试 `FuzzConvertStream`：检查接受的上游流转换结果与分片无关、只以 `ErrUpstream` 失败、通过 Chat 的检查，Messages 的正文与上游一致 |
| 转换给 Chat 客户端的回复，交回下一轮时原样回到上游（I2） | `TestConvertRoundTrip`：客户端按流合并后交回，Messages 开头的思考及签名、工具调用的 ID 和参数，Responses 的推理项及摘要、密文，函数调用的 `call_id` 和参数都在转换出的请求里 |
| 错误按表回答，不说 new-api 会据以停用渠道的话 | `TestRenderError` 每种类型 × 三种格式核对状态码、类型的位置、详情和 `axiom`，其中 `axiom.usage` 逐字段与该格式的完整用量对比（`chargedUsage`）；`TestRenderErrorCharges`；`TestErrorMessages` 核对 `RenderError` 和三种出错结尾都不含 new-api 的 `AutomaticDisableKeywords`（源码 `setting/operation_setting/operation_setting.go`，提交 ebe4c36，2026-09-08），比较不区分大小写 |
| Chat 请求转换给 Messages、Responses 端点时，表里每一行都按表写，表达不了的返回 `*NotSupported`、类型不对的返回 `ErrInvalid` | `TestConvertToMessages`、`TestConvertToResponses` 每行至少一个用例，比较解码后的值；`TestConvertSamples` 把官方样本的请求与黄金输出逐字节比较；`TestToolUseIDs` 核对 ID 写出来仍不同、调用和结果配得上 |
| 转换只取决于历史：第 n 轮的转换结果是第 n+1 轮的前缀（I11） | `TestConvertPrefix` 用一段含工具调用、合并的工具结果、对话中的系统消息和回放推理的对话，逐轮转换比较 |
| Chat 请求只要 `Inspect` 接受，转换要么得到我们自己读得了的请求体，要么是 `*NotSupported` 或 `ErrInvalid`，不会 panic | 模糊测试 `FuzzConvert`，以 `Inspect` 的 Chat 用例和上面的对话为种子 |
| 官方样本都通过检查，每条结构规则都有违反它的流被拒 | `TestSamples`；表驱动的坏流按错误里的说法区分规则，所以去掉任何一条规则，它的用例就失败 |
| 上游给的序号再离谱，检查也只报错 | 序号只与已见到的部件个数比较，从不用来分配或下标越界；模糊测试以全部样本和极端序号为种子 |
| 录制的样本要么完整、通过样本测试、不含 key 和 ID 位置上的原值，要么不存在；ID 位置之外的数据原样保留 | 本地 httptest 上游回放三种格式的已提交样本，分别用 LF、单独 CR、CRLF 行尾，前面加一组评审找到的注释（落单引号、`"input": {`、行末反斜杠），断言写下的流就是样本的事件按规范形式重建、只换了 ID，请求里工具参数的 `input.id` 不动，同一个 ID 在请求里转义写、在流里原样写也对应同一个占位值；每种拒绝一个用例（含评审的注释反例后跟转义写出的 key），断言没有发出请求（发出前的拒绝）、错误里没有 key 且目录为空；重定向、空闲超时各有用例；模糊测试：任意流重建后读回的事件数相同，每个事件除 ID 位置外 JSON 值相同，占位值一一对应，不含 key；每条规则都经过变异验证 |
| 非流式的响应体有官方响应的形状，内容就是客户端的流所说的，整段读和逐字节读结果相同 | `TestAggregateSamples` 对每个样本的每条路径（三种同格式加 Messages、Responses 转 Chat）：形状检查，与 `aggregate-*.json` 逐字节相同，与测试里独立写的 `foldStream` 对已解码的值相同；`TestAggregateMessages`（每种块和增量、片段的转义、工具输入的三种来源、引用、不认识的事件）、`TestAggregateMessagesFraming`（多行 data、`billing_pending`）、`TestAggregateChat`（交错的选项、按序号归并的工具调用、合并的推理细节、注释、logprobs）；模糊测试 `FuzzRelay` 的 `checkAggregate`：`streamcheck` 接受的流聚合出的响应体与分片无关，聚合失败当且仅当转发失败，成功时与流的折叠相同 |
| 失败按「失败的状态码」表答：没有内容是 `RenderError` 的答案，有内容时 Chat、Responses 是带内容和错误的 200，Messages 是不带内容的 502；用量总在，费用当且仅当结算写了；Responses 的响应体有仍在进行中的输出项 | `TestAggregateFail` 五条路径 × 结算写了和没写，`TestAggregateFailBeforeContent`（与 `RenderError` 逐字节相同），`TestAggregateResponsesFail`、`TestAggregateResponsesFailOpenReasoning`（完成的项、打开的消息、推理、拒答、函数调用），`TestAggregateRejects`（聚合器拒绝的事件顺序、结尾造不出响应体）、`TestAggregateChatFraming`（空格、制表符、多行的块与普通的块聚合出同一个回复）、`TestAggregateResponsesTypes`（`type` 是数字、null、对象的部件和项以 `ErrUpstream` 失败而不 panic，转义的类型照常） |
| 一个流聚合的片段不超过 16 MiB，超出和节点预算不够都以 `ErrMemory` 结束而不是 `ErrUpstream`，结束时节点预算回到原值，聚合器持有的不超过已记账的 | `TestAggregateBudget`（三种格式的上限内、上限外、节点预算不够，Responses 超限的结尾）；`TestAggregateChatHeld`（每块都远小于事件上限、合起来超过上限的纯结束选项，在 `Next` 里就以 `ErrMemory` 失败并归还预算）；`TestRelayMemoryHeld` 的聚合变体：五百个选项、五百个 Responses 项、五百个函数调用、工具的长输入，每一步和失败之后（每一步都要数一遍持有量，代价随个数平方增长，所以比非聚合的 5000 个少） |
| 聚合每个事件不比读 SSE 多分配 | `TestAggregateAllocs` 对三种格式比较 10 个与 200 个增量、1 KiB 与 64 KiB 的增量；`BenchmarkAggregate` |

## 决策与理由

| 决策 | 理由 | 不选 |
|---|---|---|
| `Relay` 也是拉取式，单一调用方，保活用预生成字节 | 推送事件的接口要用 `internal/sse` 的类型，gateway 引用不了；只有一个调用方，就不用处理并发调用 | `Push(sse.Event)`：暴露内部类型 |
| 结尾保留上游的官方用量成员、只追加本站的费用；出错结尾从 `Usage` 渲染 | 透传的客户端看得到 `iterations`、`inference_geo`、`thinking_tokens` 这些计费之外的明细；白名单挡住中转站写的上游成本。出错时没有上游的结尾用量，只能渲染 | 结尾也从 `Final.Usage` 渲染：丢掉官方明细；原样透传 `usage`：上游成本泄露 |
| `Usage()` 只把最终值算作已报告 | 流在最后的用量事件之前断了，之前报告的输出只是下界，按准数结算会少收；billing 对没报告的项按字节上界估 | 报告见过的全部数 |
| 用量无效时不中断流，`Invalid` 写明原因、全部按估算 | [计费的「用量来源与暂估」](../../docs/domains/billing.md#用量来源与暂估)：不截到 0，按估算结算并标记；回答本身没有问题 | 返回错误：上游的一个用量瑕疵毁掉整个回答 |
| 内存按缓冲的容量在增长之前记账 | 申请不到时在分配之前就拒绝；容量才是真实的占用 | 按事件字节数事后记：分配已经发生 |
| 出错事件的用量放在事件顶层的 `usage` | 与 `message_delta` 的位置相同；官方的 `error` 事件没有用量字段 | `axiom` 对象：流里的结尾用 `usage`，同一条流两种位置 |
| Chat 的 `finish_reason` 按选项从块里拆出、扣到 `[DONE]`，块里的内容照常转发 | 客户端见到 `finish_reason` 就认为这个选项完整；之后断流再报错，客户端已经当成功收下了。只拆结束信号，不扣整块：一个块里一个选项结束、另一个选项还在输出时，扣整块会让后到的内容先于它写出，顺序就错了 | 原样转发：断流的回答被当成完整的；扣住整块 |
| Chat 的用量块由本站从结算写出，形状同 OpenRouter | [接入的「用量与费用」](../../docs/domains/gateway.md#用量与费用)要求最后一块带一个空内容的选项，与 OpenRouter 一致（OpenRouter《Streaming》"The final usage chunk"，2026-10-15）；上游没发用量块（没有要 `include_usage` 的方言）时客户端照样看到用量和费用；`streamcheck.Chat` 相应接受这种用量块，OpenRouter 作上游时本来就发它 | 在上游的用量块里插费用：没有用量块的上游就没有费用，`choices: []` 让读 `choices[0]` 的客户端崩溃 |
| Chat 每块的 `id`、`model` 原地替换，只有要删成员的块才整块改写 | 每块都有这两个成员，整块改写会让每个事件都分配一个解码器和一份拷贝 | 每块都用改写器 |
| Responses 在 `response.created` 之前出错时写 `error` 事件 | 流只能以 `response.created` 或 `error` 开头；网关提交前出错本来换端点，不写结尾，这是防御 | 一律 `response.failed` |
| Responses 的项完成时只加比增量多出的字节 | 增量和完成的项重复同一段文字，两次都计会让追加提前一倍；只计增量又漏掉一次给全的项 | 只计增量；增量和项都计 |
| 转换器持有上游格式自己的读取器，再走一遍同一事件写出 Chat 的块 | 计量、内容上界和出错判断已按同格式的规则评审过，各写一份会分叉；代价是每个事件多解析一遍，读取器不再做改写 | 转换器自己计量和判断：两份规则 |
| `created` 用 `RelayOptions.Created`，调用开始的时间 | Messages 没有时间戳；同一路流的块都一样，黄金输出可重复 | 用上游的 `created_at`：只有 Responses 有 |
| 推理细节每块一条、带 `index`，签名单独一条 | 与 OpenRouter 的流式写法相同，客户端看得到推理的进度；请求一侧按 `index` 合并，原样交回也能回放 | 扣到块结束再一次给出：流式客户端看不到推理 |
| 步骤类型带 `Step` 前缀 | 草图里的 `Progress` 既是类型又是步骤类型，同一个包里不能重名 | 改名 `Progress` 类型：billing 的草图用这个名字 |
| M1 的规范模型就是 Chat 的形状，用 jsonx 读成结构体，字符串存原文 | 只有 Chat 请求要转换；结构体让每个转换函数只看自己要的成员，原文让搬运的文本解码后不变 | 先定义与格式无关的部件类型再逐格式解码：M1 只有一个来源，多一层没有用处 |
| 表达不了的请求返回 `*NotSupported`，不近似 | 删掉约束、换写法会让模型做的不是客户端要的；换个端点能原样表达 | 尽量转换：结构化输出删掉子集外的约束、低细节图片按高细节发 |
| 模型的思考方式、对话中系统消息、采样限制由 `Target` 带来 | 按模型名猜，新模型一出就错；catalog 的模型规格还没实现，由 gateway 从后台数据填 | 在 protocol 里按模型名判断 |
| Responses 的推理项只回放带密文的 | 上游不存状态，只有 ID 的推理项查不到 | 只有摘要也回放 |
| 拉取式：调用方循环调用 `Next` | 空闲超时只被真正的数据事件重置（[总体架构](../../docs/architecture.md#协议转换)）：注释在 `Next` 内部跳过，天然不算；`Next` 不认识数据里的事件类型，所以由认识格式的调用方先分出 Messages 的 `ping` 这类心跳，只在真正的数据事件上重置 | 回调或通道：取消和计时分散到两处 |
| 截断的 JSON 报 `ErrInvalid`，不包装 JSON 的原始错误 | 原始错误包装着 `io.ErrUnexpectedEOF`，会被当成流提前结束 | 直接包装：两类错误分不开 |
| 检查接受未列出的事件类型 | Anthropic 的版本策略会不另行通知地增加事件类型，要求客户端容忍；OpenAI 的两种格式同样对待，免得把新版本的接口判成坏上游 | 只认列出的类型 |
| `sequence_number` 只要求递增 | OpenAI 自己的流式示例省略了 5 到 9 号事件，中转站也可能不转发某些事件 | 要求逐一加 1 |
| Chat 的顶层 `error` 块算结尾 | OpenRouter 在已开始输出后用它报错，本站给 Chat 客户端的出错结尾也用这种写法 | 只认 `[DONE]`：把已报告的失败当成截断 |
| 严格校验每个完整事件 | 宽松读入会让原文搬运写出非法 JSON；重复键会让我们读到的用量与客户端读到的不同 | 按 WHATWG 把非法 UTF-8 替换成 U+FFFD：改动签名绑定的文本 |
| 录制命令放在 protocol 的 `internal/record`，不放 `tools/` | 写盘前要用 `streamcheck` 检查，Go 只允许 `internal/protocol` 之下的包引用它；它只用标准库和本模块，`tools/` 单独成模块是为了锁定第三方工具的版本，这里用不上 | 放 `tools/`：只能另起进程跑测试来检查，或者为它把检查提前做成 protocol 的公开接口 |
| 录制命令用自己的 HTTP 客户端，只加上不跟随重定向和空闲超时 | 地址是开发者自己填的，可能是本机或局域网里的中转站，也可能要经代理才连得上 | `platform/egress`：它拒绝回环和内网地址、不读代理环境变量，防的是站长配置的上游地址带来的 SSRF |
| 录下的流按事件解析后重建，ID 只在各格式的 ID 位置上替换，key 在解码后的字符串上查 | 前两轮评审的阻断问题都出自同一个机制：在 SSE 文本上用手写的 JSON 字符串扫描器找 ID 和 key，单独 CR 的行尾、注释里的落单引号或行末反斜杠、像 `"input": {` 的注释行都能让它错位。改用已有的 `sse.Reader`（按 SSE 规则分行、跳过注释）和经 `jsonx.Validate` 的 JSON 后，这一类错位不再存在；找字符串的位置用 `jsontext`，因为 `jsonx` 不提供按 token 读的接口。按位置替换，漏列一个位置只会留下一个真实 ID，录制者提交前看得到；按成员名猜，会改掉工具参数这类调用方的数据，改错了没有检查能发现。数据的字节除 ID 外原样拼回，空白、键序、数字写法照旧可测；SSE 的分帧（注释、行尾、`id`、`retry`）不保留，读流的代码本来不看它们，要测分帧的用官方样本，它们仍是原样的字节 | 在原始字节上扫描（前一版）：两轮评审各找出新的错位反例，补一处又出一处；解码后重新编码整个 JSON：改变键序和数字写法；按 `id`、`*_id` 成员名猜 ID 再加一张跳过调用方数据的列表：列表外的工具参数照样被改；在没列出的位置发现像 ID 的值时提醒：现在三种格式的位置都已列出，等录到新形状的上游时再看是否需要 |
| `Prepare` 只在 `Target.Streams` 时把 `stream` 写成 true | gateway 总是以流读上游，非流式客户端拿聚合结果；端点能否流式是适配器的声明，不流式的端点保持客户端的写法 | 无条件改写：给不能流式的端点发它不支持的请求 |
| 同格式 Chat 的改写返回 `*NotSupported`，不拼近似 | 方言（推理载体、缓存标记、推理参数的写法）决定请求对不对，拼错了模型做的不是客户端要的；gateway 跳过这个端点，不再回 503 | 返回普通错误：网关认成节点故障，与端点的能力无关 |
| `Target` 加 `Streams`，由路由按端点的能力填 | 请求发出去的 `stream` 是端点的属性，不是请求的；网关一处设置，`Prepare` 两处（改写、转换）照它写 | 在网关里改请求体：protocol 拥有请求体 |
| `UsageOf` 只读顶层 `usage` 与 `service_tier` | compact 的回复整份读一次就完了，复用聚合器的成员读出即可，不需要新的解码器 | 解成结构体：多一份随回复大小增长的分配 |
| 两个文件共用一张替换表，并替换请求里的 ID | 多轮请求里的工具调用 ID、`previous_response_id` 会出现在流里，替换后两边仍对得上 | 只替换流：请求里留下真实 ID，两边也对不上 |
| 摘要一次扫描：按 token 读，深度在每个 token 上对照 `jsonx.MaxDepth` 检查 | 请求体可达数 MB，先 `jsonx.Validate` 再读字段要扫两遍；`jsontext` 的默认选项就是 jsonx 的严格程度，只有深度要自己查，所以 `jsontext` 只在这里和录制命令里引用，各自注明理由 | 先校验再读：两遍；解码到结构体：要为每种内容形状写解码器，分配随部件个数增长 |
| 输入上界是请求体字节数减去图片、文件的数据，不是只数正文字段 | 只数正文要认出每一种放文本的字段，漏认一种就少算，少算就是欠款；多算的只是 JSON 语法和成员名 | 只数 `text` 等正文字段 |
| 按位置读部件和工具，不在任意深度找 `cache_control`、`type` | schema 和工具参数里的同名键是客户端的数据，按名字找会让路由要求不需要的能力，预留也多算 | 任意深度按名字找 |
| 服务等级给集合，不写或 `auto` 含 priority；认不出的取值拒绝 | 两家的 `auto` 都可能用 priority 容量，只报标准档会少预留；认不出的等级没有价格 | 只报明确写的等级 |
| 改写按位置分段拷贝原文，不在 `Inspect` 里记下位置 | 改写要等路由选定端点之后，位置存下来就要按消息数分配，`Inspect` 也不再能保证不借用请求体；再走一遍是 O(n) 且分配次数不变 | 整体解码再编码：改变键序、数字和转义的写法，分配随大小增长；在 `Inspect` 里记位置（设计草稿的做法） |
| `include` 只对返回加密推理的模型加，由 `Target` 说明 | 不推理的模型收到它返回 400 "Encrypted content is not supported with this model"（pydantic-ai issue #2865，2025-09；hermes-agent issue #23450，2026-05）；推理模型不写 `reasoning` 也会推理，看请求判断不了 | 一律加（设计草稿）；按请求是否要求推理决定 |
| `session_id` 删除，不换成派生值转发 | 三种官方格式都没有这个参数，带着不认识的成员的请求会被官方端点以 400 拒绝（Anthropic 的 "Extra inputs are not permitted"，见 anthropics/claude-code issue #21612，2026-01；OpenAI 的 "Unrecognized request argument supplied"）；它是 OpenRouter 的路由参数，粘性由 routing 用，上游的粘性靠 `prompt_cache_key`、`metadata.user_id` 和会话请求头 | 换成派生值转发 |
| 文件只数个数，不数页数 | 页数要解析 PDF；一个文件的上界由计费用模型数据（单页最大值和页数上限）或上下文长度给出 | 在调用路径上解析 PDF |
| 聚合器吃客户端格式的事件字节，不吃上游的事件 | 一份聚合器服务五条路径，转换路径不用再写；它的输入就是黄金流（`client-*.sse`、`to-chat.sse`）写出的那种事件，能单独回放；代价是多一次切开事件的小开销，只在非流式路径上 | 每条路径一个聚合器，挂在各格式的读取器上 |
| Responses 成功时取结尾快照的 `response`，不从增量重建 | 官方的完整响应就是快照；官方样本的增量对不上最终文本（`testdata/responses/text/meta.json`）；省掉一份与快照重复的重建 | 全部从增量重建：形状对不上官方，还要持有两份输出 |
| Messages 的非流式失败返回 502，不带内容，用量和费用在 `axiom` 里 | [接入的 D16](../../docs/domains/gateway.md)：Messages 没有表示出错的结束原因，半截内容当成功返回会让客户端以为完整；聚合器因此在失败时放掉全部片段 | 带半截内容的 200 |
| 16 MiB 算片段的缓冲容量，要拷成响应体的片段多记一倍，推理细节记四倍 | 记账要对得上实际持有：Chat 的片段在 `Finish` 时才拷成响应体，那时不能再申请（`Finish` 不会失败），所以预先留出；容量而非字节数，因为内存是按容量占的，翻倍的富余到上限为止，内容接近 16 MiB 的回答放得下 | 只按字节数算：低估持有量；`Finish` 里申请：它没有失败的出口 |
| 失败时打开的 Responses 项 `status` 写 `incomplete` | 响应已经 `failed`，不会再有事件让 `in_progress` 变成别的；`incomplete` 是 OpenAI 项状态里表示「没做完」的那个 | 保持上游骨架里的 `in_progress` |
| 读 SSE 的校验器每路流一个，只给不超过 2 KiB 且嵌套不超过 8 层的事件留着解码器；单行 data 的事件直接返回缓冲里的一段；大事件之后连续 16 个小事件才缩小缓冲 | 每事件新建解码器要多 8 次分配，复用后稳态为 0（`TestReaderAllocs`）；解码器给读过的每一层留一张成员名表、不记账，各层的表来自不同的文档，只限文档大小界不住（`TestValidatorRetains`：深度递减的文档留着 1.9 MB），所以大小和层数都限，上界是 8 张至多 2 KiB 名字的表，造来填满它的文档实测约 120 KiB；大或深的事件的开销远大于一个解码器；单行 data 是常见情形，缓冲在事件跨读时才移动，那时才拷贝；缩小要等一串小事件，否则一连串大事件每个都重新分配一遍缓冲，基准的分配次数会上升 | `sync.Pool` 里的解码器：分配次数随垃圾回收时机变，`TestRelayAllocs` 这类精确比较会不稳定；一直留着解码器：留着的内存随最大事件增长；事件一结束就缩小缓冲：大事件多的流分配更多 |
