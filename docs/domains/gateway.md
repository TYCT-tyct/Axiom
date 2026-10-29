# 接入与对外接口

客户端怎么调用 Axiom：路径、鉴权、转发规则、用量与费用、错误、路由参数、给其他中转站对接的接口。对应代码模块 `gateway`，一次调用在代码里的步骤、期限、并发和故障处理见[模块 README](../../internal/gateway/README.md)；格式之间的转换由 `protocol` 模块负责（见[总体架构的「协议转换」](../architecture.md#协议转换)）。对外的模型调用接口沿用 OpenAI 和 Anthropic 的规范，本文只写与它们不同或需要特别注意的地方，字段细节以接口契约为准。

## 要求

- Claude Code、Codex、OpenRouter 的客户端只改地址和 key 就能接入。
- 第一版支持三种调用格式：OpenAI Chat、OpenAI Responses、Anthropic Messages（见[产品需求的「范围」](../product.md#范围)）。
- 其他中转站可以把 Axiom 当上游，用它们习惯的方式查余额、判断错误。
- 行为默认对齐 OpenRouter。

## 设计

### 路径与格式

| 格式 | 路径 | 主要使用者 |
|---|---|---|
| OpenAI Chat | `POST /v1/chat/completions` | 大多数 SDK、Cursor、OpenRouter 客户端 |
| OpenAI Responses | `POST /v1/responses`、`POST /v1/responses/compact`；按 ID 删除随续接提供（见下文「Responses 的会话状态」） | Codex |
| Anthropic Messages | `POST /v1/messages`、`POST /v1/messages/count_tokens` | Claude Code |
| 模型列表 | `GET /v1/models`、`GET /v1/models/{model}` | 所有客户端 |

- WebSocket 的升级请求一律拒绝，回 400 `invalid_request`。

- **兼容 OpenRouter 的路径**：以上路径同时以 `/api/v1/...` 的形式提供。`GET /api/v1/models` 与 `GET /api/v1/models/{model}` 返回 OpenRouter 的模型对象（价格取主站售价，带 `context_length`、`top_provider`、`architecture`、`supported_parameters`、`reasoning`，外壳是 `data`/`links.next`/`total_count`），并读 OpenRouter 文档里的查询参数；`/v1/models` 仍是 OpenAI 与 Anthropic 的合并形，两种客户端各读各的。逐项的差距见 [OpenRouter 能力对照表](../openrouter-parity.md)。
- **挂载**：`/v1/` 与 `/api/v1/` 只挂在节点的公开监听上，管理端监听不提供；它们在控制台的 API 和网站页面之前截走，不经过控制台那一层的响应压缩（网关自己写应答、自己决定何时 flush），也不会被控制台 API 的 404 回答。所以客户端只需要知道站点的 API 域名，认 key 的规则见下文「鉴权」。节点在路由快照可用之前不报告就绪（见[运维的「节点启动自检」](operations.md#部署与节点管理)）。
- **`GET /v1/models` 与 `GET /v1/models/{model}`**：
  - 返回一个同时满足 OpenAI 和 Anthropic 两种格式的列表；只列这把 key 能调用、且有端点可走的模型，权限用的是与调用相同的判断，不能用的模型、没有端点的模型和不存在的模型一样回 404 `model_not_found`（不是 403）；
  - Anthropic 模型同时列出厂商原生的名字。Claude Code 只认 ID 里含 `claude` 或 `anthropic` 的条目；
  - `GET /v1/models/{model}` 按调用路径的方式解析别名（`catalog.ResolvePublic`，与 sub2api 从列表项取单个模型的做法一致），返回列表里同一项的形状，`id` 是请求里的名字；
  - 列表看有没有端点，不看端点的实时状态：没有端点（没接供应商，或缺规格/售价而被路由排除）的模型不列，因为调用它必然失败；端点暂时满载、熔断、暂停或预算用尽不让模型少列，容量由调用自己排队或报错；
  - OpenRouter 在 `/models` 下的 `count`、`user`、`{author}/{slug}/endpoints` 不提供，回 404 `not_found`；
  - 响应带 `Cache-Control: no-store`：列表是这把 key 的，不能进共享缓存；
  - Claude Code 启动时用它发现模型，超时只有 3 秒，任何跳转都算失败，所以必须在原地址直接、快速地返回（Claude Code《Gateway compatibility guide》，2026-10）。
- **`GET /api/v1/models` 与 `GET /api/v1/models/{model}`**：
  - 模型对象是 OpenRouter 的形状：`id`、`canonical_slug` 都是这个模型的公开标准 ID（`author/slug`，即目录名去掉内部版本后缀，`acme/claude@20250929` 按 `acme/claude` 列出），`name` 是去掉作者的部分，`created` 恒为 0；`pricing` 的每个成员都是字符串，取主站售价的标准服务等级、第一档（美元 / token，`web_search` 每次），售价表没有的单位（`image`、`image_output` 按图，`request` 按次，`discount`、`overrides`）省略而不是写 0，`image_token`、`internal_reasoning` 只给能收图片、会推理的模型（价目表对这两项会用所在侧的原价兜底，纯文本模型不该出现）；`context_length` 与 `top_provider.*` 取规格，`architecture` 的模态取规格声明的能力，`supported_parameters`、`reasoning` 按能力与方言给出；`default_parameters`、`per_request_limits`、`supported_voices` 是 null，`description`、`knowledge_cutoff`、`hugging_face_id`、`expiration_date`、`alias_target`、`benchmarks` 不输出（目录没有这些数据）。`id` 只用能解析回这个模型的公开名字：标准 ID 解析不到它（目录不该出现）就整个不列，因为那个名字既取不到详情也调不通。同一把 key 看到哪些模型与 `/v1/models` 一样：能调用、路由快照里有端点、有售价。
  - 列表读 `offset`、`limit`、`q`、`context`、`min_price`/`max_price`、`min_output_price`/`max_output_price`、`input_modalities`、`output_modalities`、`supported_parameters`、`model_authors`，`sort` 支持 `pricing-low-to-high`、`pricing-high-to-low`、`context-high-to-low`；OpenRouter 文档里有而 Axiom 没有数据的参数按名字回 400 并说明缺什么，文档之外的参数（缓存参数、`utm_*`）忽略。查询串本身解不开（`%ZZ`、用 `;` 分隔）也回 400：标准库会静默丢掉解不开的那一项，过滤条件就这样变成「整个列表」。`links.next` 是同一个查询把 `offset` 往后挪一格，只在这页之后还有模型时给出。
  - 单模型按调用路径的方式解析别名（`claude-live` 与 `anthropic/claude-live` 是同一个模型），返回 `{data: 模型对象}`，`id` 是标准 ID 而不是请求里的名字：OpenRouter 的形状里一个模型只有一个 `id`。
  - 列表与路由快照是同一次构建的产物，一起发布：同一次读目录、同一次读售价，所以列表上的价格就是快照里那个售价版本的收费价格，两者不会各读各的（`RouteDeps.Build` 同时产出 `*routing.Snapshot` 与列表，`Publish` 在同一发布锁内一起换；取列表和调用规划持该锁的读锁，锁内只读内存，不包住鉴权事务、编码或网络写入；构建失败或迟到则两者都不换）。请求路径上没有读价：列表和它服务的模型集合都在构建时定下来，一次响应里的 `total_count`、这一页和它之后的链接因此都属于同一次构建，写响应时路由怎么变都不会让它们互相矛盾。
  - 列表按模型编码一次并缓存，请求只做过滤、排序和一次缓冲区拷贝，缓冲区按这一页的大小申请而不是整个目录（`BenchmarkOpenRouterList`、`TestOpenRouterPageBuffer`，后者在 `alloctest` 子进程里测分配字节数）。
- **token 计数**：
  - 没有这个接口时，Claude Code 只能按字符数粗估上下文用量；
  - 计数优先交给支持该接口的端点，没有时用本地估算；M1 只用本地估算，交给端点在 M2；
  - 计数免费，但有单独的频率限制：每个 key 的计数请求与调用各算各的（`CountRate`、`KeyRate`），互不占用。
- **鉴权**：`Authorization: Bearer` 和 `x-api-key` 两种写法都接受。key 只能在所属站点登记的 API 域名上使用（见[账户与租户的「API key」](accounts.md#api-key)）。只凭域名和请求头认 key，认出之前不读请求体，认不出立即返回 401 并关闭连接。
- **浏览器跨域**：调用接口默认不允许，因为把 key 放在网页里不安全；站点可以为指定的来源开放。
- **限制**：请求体大小、各项超时和并发上限都是后台配置，下表是默认值；保存时校验它们之间的关系，不满足就拒绝保存，启动时不满足就不启动（各项的关系和内部的期限见 [gateway 模块的「超时与限额」](../../internal/gateway/README.md#超时与限额)）。

| 用户遇到的限制 | 默认 | 到了之后 |
|---|---|---|
| 读完请求体 | 2 分钟 | 408 |
| 候选端点都满载时排队 | 20 秒 | 429 |
| 等上游的响应头 | 60 秒（现在全站一个值，按端点调还没有）；`/responses/compact` 10 分钟 | 换下一个端点 |
| 上游没有新数据（心跳、注释、重复快照不算） | 180 秒（现在全站一个值，按模型调还没有） | 开始转发前换端点，之后以 `timeout` 结束 |
| 准入后不再发起新的尝试 | 120 秒 | 返回最后一次尝试的错误 |
| 单次调用最长 | 60 分钟 | 取消上游、结算，以 `timeout` 结束 |
| 一个请求的尝试次数 | 3 次，共用上面的期限，换端点不重新计时 | 返回最后一次尝试的错误 |

### 转发规则

依据 Claude Code 官方的网关兼容指南（Claude Code《Gateway compatibility guide》，2026-10）：

- **请求头按目标协议的白名单转发**（`internal/gateway/headers.go` 是唯一的一处表）：
  - 协议需要的头原样转发，例如 Anthropic 的 `anthropic-beta`、`anthropic-version`，Responses 端点的 `x-codex-turn-state`。`anthropic-beta` 不筛选具体的值，因为 Claude Code 每个版本发的值都不同，筛选会让长上下文、交替思考等功能悄悄失效或直接报 400；
  - 会话请求头（`session_id`、`session-id`、`conversation_id`、`X-Session-Affinity`、`X-Session-Id`、`X-OpenCode-Session`、`X-Conversation-ID`）发给 Responses 和 Chat 端点（两者都是 OpenAI 的方言，中转站按这些名字定账号），不发给 Messages 端点，值是下面那一条的派生值，中转站据此把两轮对话固定到同一个账号；客户端的 `Authorization`、`x-api-key`、`x-axiom-*`、`x-claude-code-*`、`HTTP-Referer`、`X-Title`、`User-Agent` 一律不转发，其中有用的信息由我们自己记录。
- **会话和用户标识换成派生值**：`prompt_cache_key`、Anthropic 的 `metadata.user_id` 和上面的会话请求头，替换成用账户密钥派生的不透明标识再转发（见[路由的「会话粘性」](routing.md#会话粘性)），这样上游的缓存仍然有效。`metadata.user_id` 写成 JSON 字符串，内容是 `{"device_id":"…","account_uuid":"","session_id":"…"}`：`device_id` 每个账户固定，`session_id` 与同值的会话请求头是同一个派生，中转站从中取出会话固定账号（格式见 `internal/protocol/README.md` 的「同格式改写」）；`account_uuid` 留空，中转站接受空值，我们也不把账户标识交给上游。空串或纯空白的 `metadata.user_id`（含 Chat `user` 转换）写为空串，不生成会话结构，让中转站按内容回退。`user`、`safety_identifier` 换成哈希值，不把终端用户标识原样发往上游。`session_id` 作为路由参数用过之后与其他路由参数一样删除；请求头里的那个不是路由参数，它是客户端自己的会话名。
- **上游一律流式调用**：生成请求都以流式调用上游，路由只保留能流式的端点；客户端要的是非流式也由网关聚合后一次返回，这样计费进度看得见（见[计费的「预留与结算」](billing.md#预留与结算)）。非流式的回答在返回之前没有"已转发内容"这一刻，所以中途失败仍可换端点，各次尝试的内容互不混入。`/responses/compact` 例外：上游不流式，回复原样透传，请求只改必须改的成员，一次预留足。
- **跨格式**：Chat 客户端调用说 Chat 的端点（DeepSeek、Qwen 等 OpenAI 兼容的模型，同格式转发），也调用 Messages、Responses 端点（转换）；端点同时说客户端格式和别的格式时优先原格式。Messages、Responses 客户端也能调用只说 Chat 的端点：请求反向使用 protocol 的同一套映射，表达不了的成员拒绝，不靠删除约束凑请求。Chat 的文本、推理、工具参数增量转换成客户端的事件，非流式由这些事件聚合；Messages 的块必须顺序输出，交错的后续工具参数暂存到前块关闭；Responses 的完成事件包含完整输出快照。缓存、推理用量仍由 Chat 读取器计量，费用和错误按客户端格式写出。跨格式只是请求和流的转换不同，预留、尝试、结算与同格式相同。
- **请求内容不改**：同格式转发时，缓存标记、块状的 system 内容保持原样，否则每一轮都按无缓存计费。Chat 请求里模型方言的成员（`reasoning`、`reasoning_effort`、`cache_control`、助手消息里回传的 `reasoning_content`、图片部件）也照客户端写的发给端点，由端点按它自己的方言处理；按模型规格把推理参数换成端点认的写法，现在没有，待定。
- **Chat 端点的请求改什么**：`model` 换成端点的名字，`user`、`safety_identifier`、`prompt_cache_key` 换成派生值，路由参数删除，一律要流，并且 `stream_options.include_usage` 一律为 `true`（客户端写了 `false` 或 `null` 也一样，`stream_options` 里的其他选项不动）：计量和非流式的聚合都读上游报告的用量，这和 new-api（`relay/compatible_handler.go` 的 `ForceStreamOption`）、sub2api（`openai_gateway_chat_completions_raw.go` 的 `ensureOpenAIChatStreamUsage`）一样。输出上限：客户端的 `max_tokens`、`max_completion_tokens` 两个成员都拿掉，把其中最小的正数（都没有就是报价用的上限）写进端点读的那一个成员：端点的上游模型名是 OpenAI 的 o1、o3、o4、GPT-5 或 GPT-6 的 Astra、Sol、Luna 时写 `max_completion_tokens`（它们拒绝 `max_tokens`），其余写 `max_tokens`（DeepSeek、Qwen 一类忽略 `max_completion_tokens`，留着它输出上限就没有了）；这是 new-api 的规则（`GetOpenAIChatCapabilities`）。`store` 等其余成员不动。
- **发出的输出上限就是预留用的上限**：请求没写输出上限（或写 `null`）时，发给上游的是该条目报价用的上限，也就是模型的默认值，上下文装不下时则是裁短后的值（Messages 与 Responses 的数值 0（含 `-0`）也算没写；Messages 补 `max_tokens`，Responses 补 `max_output_tokens`，Chat 写在端点读的那个成员里，见上）；请求写了就照它发。模型的上限未知时不发，由上游自己的默认值管。否则上游可以生成比预留依据更多的输出。
- **Chat 旧版函数与控制字段**：Chat 同格式改写与跨格式转换都先把 `functions` / `function_call` 升级成 `tools` / `tool_choice`，旧调用与同名结果按消息顺序稳定配对，生成的 ID 避开已有调用和结果。新旧工具定义合并，冲突的工具选择或同一消息中的新旧调用拒绝。只用了旧版表示（没有非空的现代工具声明或现代工具选择）的客户端收到 `function_call`（流式为 `delta.function_call`，非流式为 `message.function_call`）和 `finish_reason: "function_call"`；现代或混用新旧声明的客户端仍收到 `tool_calls`。旧版请求强制 `parallel_tool_calls:false`，避免把多个调用压成一个；上游仍返回并行调用时按上游协议错误处理。控制字段按解码值判断，转义的选择、字段名、块类型与普通写法等价，推理片段的数值索引 `0` 与 `-0` 归到同一片段；正文、签名和工具参数保留原字节（转换格式必需的字符串解包除外）。
- **跨格式时缓存标记按 OpenRouter 的做法转换**（OpenRouter《Prompt caching》「OpenAI」、OpenAI《Prompt caching》，2026-10-16）：发往 Messages 时原样带着；发往 Responses 时，规格写明支持显式断点的模型（OpenAI 从 GPT-5.6 起）上，用户、系统、开发者、助手和工具结果的文本块的 `cache_control` 写成 `prompt_cache_breakpoint`，TTL 丢掉；不支持断点（规格写明不支持，或没写）的模型上的标记，以及工具定义、图片和文件上的标记丢掉，由 OpenAI 自动缓存前缀。对费用的影响：OpenAI 的缓存写入从 GPT-5.6 起按输入价的 1.25 倍收，更早的模型没有写入加价；Responses 只有 30 分钟起的缓存期，Anthropic 的 1 小时 TTL 表达不了，所以发往 Responses 的请求不会产生 1 小时的写入价（Anthropic 是输入价的 2 倍）；丢掉的标记可能让命中率比在 Anthropic 上低。收费始终按上游报告的实际缓存读写，不按请求里的标记。
- **只认原上游的内容**：请求里带着只有原上游才认的内容时（Claude 推理块的签名、OpenAI 的加密推理内容），优先粘回产生它们的端点。同格式透传时这些内容原样转发、从不删改：删掉中间的思考块会让其后的块全部失效，而换了模型或账户时，Anthropic 自己丢弃读不了的思考块、请求照常成功（Anthropic《Preserved thinking》，2026-10）；OpenAI 的加密推理换了组织是否可用，要录样本确认，被拒时按原话返回。
- **格式特有的能力只走原格式的端点**，见[路由的「一个请求的路由步骤」](routing.md#一个请求的路由步骤)。
- **Claude Code 的提示信息**（会话 ID、请求类别、"上下文已压缩"标记）用于会话粘性和路由，见[路由](routing.md)。
- **事件流带 `X-Accel-Buffering: no`**：前面有 nginx 时，它据此不缓冲，事件才能及时到达客户端；new-api 的每个流都带这个头（new-api 源码 `relay/helper/common.go`，2026-10-18）。
- **Chat 回复的推理文本写两个字段，内容相同**：`reasoning`（OpenRouter 的名字）和 `reasoning_content`（new-api、DeepSeek 的名字，不少客户端读它），流式的每一块、非流式的消息里都是；带签名和密文的 `reasoning_details` 照旧。客户端读哪个都行；代价是推理文本多发一份，只在模型推理时发生。依据见「决策与理由」。Chat 端点只写其中一个名字时（DeepSeek 写 `reasoning_content`，OpenRouter 写 `reasoning`），另一个由本站按同样的文本补上；两个都有文本的块不动。
- **响应头按白名单放行**：只返回内容类型、我们自己的调用编号、我们计算的限流和重试信息。上游的 `x-request-id`、`openai-organization`、`cf-ray`、`server` 等会暴露上游身份的头一律删除；唯一的例外是上游铸造的 `x-codex-turn-state`，它只在作答的那次尝试的上游设了它时回传，客户端自己发的值不回显——那是厂商给下一轮的凭据，换一个出来没有意义。
- **自己回显**：响应里的 `id` 用我们的调用编号，`model` 回显客户端请求的名字。

### 用量与费用

- **每次调用都返回用量和费用**：输入、输出、缓存读、缓存写、推理 token 和费用，与 OpenRouter 的用量统计对齐（OpenRouter《Usage Accounting》，2026-10）。
- **流式输出放在最后一个事件里**：Chat 格式的最后一块带一个空内容的选项，避免客户端崩溃（与 OpenRouter 一致）。费用在回答结束前结算好再发出，字段是 `usage.cost` 和 `usage.cost_currency`；用量来自估算的另带 `usage.cost_estimated: true`，标出这次收费建立在估算上；M1 不按供应商记录校正它（见[计费的「用量来源与暂估」](billing.md#用量来源与暂估)）。
- **Chat 端点报的用量**：按 OpenAI 的口径映射（`prompt_tokens` 含缓存的 token，`completion_tokens` 含推理的 token，缓存读写和图片的 token 只算一次，不在输入里再算一遍）。DeepSeek 把缓存命中写在 `prompt_cache_hit_tokens`（与 `prompt_cache_miss_tokens` 之和等于 `prompt_tokens`），`prompt_tokens_details.cached_tokens` 缺失或为 0、而命中数大于 0 时，命中数就是缓存读；两者都大于 0 时以 `cached_tokens` 为准，不比较是否相等。这和 new-api 一样（`relay/channel/openai/usage.go` 的 `applyUsagePostProcessing`）；两个名字都不当作额外的用量，否则会按未知 token 再收一遍。用量块可以在带内容的最后一块里（DeepSeek），也可以是没有选项的单独一块（OpenAI、Qwen）。
- **结算写不进时**：上游已经正常结束、内容完整的，照常结束，但结尾不带费用，改带 `axiom.billing_pending: true`，非流式同样返回 200；费用由后台继续结算，在调用记录里查。只有被截断的流才以 `billing_unavailable` 报错结束。
- **流开始前就出错也可能收费**（例如已执行的联网搜索，见[计费的「免单规则」](billing.md#免单规则)）：错误体的 `axiom` 对象里带用量和费用。
- **余额截断**：转发中追加预留失败时，按下文「流中途出错」的方式发错误事件结束回答，说明余额或某个 key 的额度不足、回答已截断（见[计费的「预留与结算」](billing.md#预留与结算)）。
- **费用的单位**：用本站的记账货币（见[计费的「钱的表示」](billing.md#钱的表示)），并附带币种字段。主站是美元，与 OpenRouter 客户端的预期一致。
- **各格式的放置位置**：各格式按自己的习惯放用量字段，费用字段附加在该格式允许扩展的位置。Responses 在回答结束之前的事件（`response.created`、`response.in_progress`）里 `response.usage` 是 `null`（OpenAI 如此，new-api 原样转发），我们也原样保留；结尾事件里才是用量对象。
- **流以失败结束时先读取并校验失败帧的用量**：计费层保留其中最终确定的项，缺失项按既有规则暂估，免单规则不变；口径只在[计费的「用量来源与暂估」](billing.md#用量来源与暂估)定义。
- **调用编号**：就是请求 ID，每个响应（含错误）都在 `X-Request-ID` 和 `X-Generation-Id` 两个响应头里带同一个值，响应体的 `id` 也是它（与 OpenRouter 一致）。凭它可以查询调用记录：每次尝试、路由过程、费用明细。查询只返回用户付的售价，不返回我们的成本；中转站只显示代号。调用记录写入前（约 200 ms 内）查询返回 404 `generation_not_found`，客户端稍后重试；账本只记金额、没有用量，所以不提供精简版，等调用记录的写入方（G10）定了再补。
- **客户端断开**：

| 什么时候断开 | 怎样 |
|---|---|
| 排队中 | 停止等待，什么都不收 |
| 已准入、还没调用上游 | 释放预留，什么都不收 |
| 已调用上游，端点传递取消 | 立即中断上游，按已产生的用量收费，缺的项估算并标暂估 |
| 已调用上游，端点不传递取消（不论是否已转发内容） | 继续读完上游但不再转发，按完整用量收费，是准数；读也超时就中断、按暂估收费 |

  端点是否传递取消，按[计费的「客户端断开」](billing.md#客户端断开)判断。客户端取消从不免单。客户端自行重试视为新的请求。

### 错误

- **外壳与类型**：Messages 用 Anthropic 的外壳；Chat 和 Responses 用 new-api 与 OpenAI 的 `error` 对象：`message`、`type`、`param`、`code`，另带统一的错误类型字段 `error_type`，程序判断它就够了（与 OpenRouter 一致，见 OpenRouter《Errors and Debugging》，2026-10）。
  - `type` 用状态码表里「Messages 的 `type`」那一列的词（`invalid_request_error`、`authentication_error` 等），`param` 没有时是空字符串，`code` 是字符串：我们自己的错误里就是统一的错误类型（`invalid_request`、`model_not_found`……），上游拒绝请求时是上游给的 `code`（没给为 `null`）；
  - `error_type` 的位置：Chat 在 `error.metadata.error_type`，Messages 在 `error.error_type`，Responses 在 `response.failed` 的顶层；Responses 在流开始之前出错时用 Chat 的外壳；Chat 流中途的出错块里的 `error` 也是同一个对象；
  - 与 OpenRouter 的不同：它的 `error.code` 是与状态码相同的数字，我们的是字符串，状态码在响应的状态行里。读 OpenAI 或 new-api 的 `error.code`、`error.type` 的客户端能读到，读 OpenRouter 数字 `code` 的要改读状态行或 `error_type`。偏离的理由见「决策与理由」。
- **上游拒绝请求本身（4xx）时，客户端读到上游的原话**：Claude Code 靠上游错误的原话自动恢复，例如推理签名被拒、`output_config.effort` 被拒，改写会让它们变成无法恢复的 400；new-api 也把上游的 `message`、`type`、`param`、`code` 原样交给客户端。我们交出上游错误对象里的这四项（OpenAI 和 Anthropic 的外壳都有 `error.message`，`type` 也同位置），去掉能指向供应商的内容后：
  - **去掉的**：其中所有的网址（含文档链接）、本站调用它用的凭证（先整串遮蔽，再处理其他模式，免得被切成碎片）、形似凭证的串（`sk-`、`ak-` 开头，`Bearer`，`api_key=`）、供应商自己的域名及它的每一级上级域名（两段以上，如 `api.us.relay.example` 连同 `us.relay.example`、`relay.example`；不引入公共后缀表，多遮蔽 `co.uk` 这类后缀只是多遮蔽，不会漏）、new-api 附在文字后面的 `(request id: …)`、new-api、one-api、sub2api 这些软件的名字；每项最多 2 KiB；流内错误超过此限的字段整项省略，避免先截断凭证导致后续脱敏漏掉前缀；
  - **`type` 只留通用的**：OpenAI、Anthropic 自己用的取值（`invalid_request_error`、`authentication_error`、`not_found_error` 等）；中转站软件自己的（new-api 的 `new_api_error`、sub2api 的 `scheduling_error`）不传，Chat、Responses 外壳改用统一的 `type`。名字里带软件名的 `code` 也不传；
  - **没有原话时**：上游的响应不是这种错误对象、或没有 `message`，用统一的说法；
  - **只用于这一次回复**：原话只进给客户端的响应，不进日志，也不进任何表（suppliers 的 `Failure` 只在这一处带它，`Cause` 仍不带任何文字）；
  - **依据**：new-api 的 `ToOpenAIError` 带上游的四项并遮蔽上游的网址和 API key（new-api `relaykit/types/error.go`、`relaykit/relayconvert/kitutil/mask.go`，2026-10-18）；OpenRouter 在非 500 的错误里带供应商的话，500 的换成通用说法，供应商的原始细节在 `metadata`（OpenRouter《Errors and Debugging》，2026-10-19）。我们比 new-api 多去掉所有网址，因为供应商的充值页、文档页会暴露货源。
  - 其余的错误（含上游的 429 和 5xx）仍然是统一的说法。上游在流里用错误事件报的也一样：按它的类别算这个请求的还是供应商的，只有拒绝请求本身的（下文）才带上游的原话、`type`、`param` 和 `code`，别的类别都换成统一的说法。
- **我们自己的错误和上游的 5xx**：使用统一的说法，不出现中转站的名字或原始报错。
- **请求体不是合法 JSON**：400，说 "Invalid request: invalid JSON request body"（new-api 的同一句）；解析库的原话（偏移量、Go 类型名）不返回。类型不对的值用它的 JSON 指针指出位置。
- **状态码**：

| 失败 | 流开始前 | Messages 的 `type` | 流开始后（状态码仍是 200） |
|---|---|---|---|
| key 无效、过期、停用，IP 或域名不允许 | 401 `authentication` | `authentication_error` | — |
| 余额、工作区预算或 key 上限不够 | 402 `credits_insufficient`，`limit_source` 为 `balance`、`workspace_budget` 或 `key_limit`；会重置的上限带 `Retry-After` | `billing_error` | 追加预留失败，截断，类型同左 |
| 进行中的请求暂时占满了余额 | 402 同上，`limit_source` 为 `pending`，`Retry-After` 是最早释放的时间（待定：见「待定问题」；现在回 `balance`，与上一条相同） | `billing_error` | 同上 |
| 追加预留时数据库出错 | 503 `billing_unavailable`（转发开始前就用完了第一段） | `api_error` | `billing_unavailable`，不带费用 |
| 结算写不进，内容完整 | 200，带 `axiom.billing_pending: true`，不带费用 | 同左 | 同左 |
| 限流（每个 key 调用和计数的频率）；按 IP、按账户的在途上限；同一账户的资金操作排队超过 5 秒（一次突发在一个账户上排的队，见 [gateway 模块的「并发、背压与共享状态」](../../internal/gateway/README.md#并发背压与共享状态)） | 429 `rate_limited` | `rate_limit_error` | — |
| 端点都满载，排队的人数满了或排队超时；上游凭证被并发的请求占满 | 429 `rate_limited`，`Retry-After` 是估计的等待时间，凭证占满时为 1 | `rate_limit_error` | — |
| 节点满载、准入超时、路由快照过旧、数据库不可用、本节点的持有者失效 | 503 `overloaded`，`Retry-After: 1` | `overloaded_error` | — |
| 模型不存在；key 不允许用这个模型 | 404 `model_not_found`，不返回 403 | `not_found_error` | — |
| 没有可用端点，永久性的：能力、上下文、格式、预设或请求覆盖过滤掉了全部端点，或没有售价 | 404 `no_endpoint`，附过滤原因 | `not_found_error` | — |
| 没有可用端点，暂时性的：全部熔断、供应商余额不足、重试预算用完 | 503 `no_endpoint`，附原因和 `Retry-After` | `overloaded_error` | — |
| 每个端点都不支持这个请求的写法 | 400 `invalid_request`，附原因 | `invalid_request_error` | — |
| 开启了联网搜索或代码执行，却没写最多用几次，而模型也没有默认次数（费用没有上界，预留不了） | 400 `invalid_request`，说明要写 `max_uses`（Messages 联网搜索）或 `max_tool_calls`（Responses）；没有上限字段的（Chat 的工具、Messages 的代码执行）说明该格式暂不支持，不调用上游 | `invalid_request_error` | — |
| 上游因请求本身拒绝（4xx，或在流里以错误事件报出） | 400 `invalid_request`，带上游的原话、`type`、`param`、`code`（上文），没有原话时用统一的说法；不再换端点 | `invalid_request_error` | 错误事件里是同样的原话和 `code`（格式有这两项的；开头的 Responses `error` 事件还有 `param`），`error_type` 是 `invalid_request`，已转发的内容保留 |
| 所有尝试都失败，含各自超时 | 502 `upstream_error` | `api_error` | 上游中途出错，`upstream_error` |
| 单次调用最长、不再发起尝试的期限到了，节点退出；读请求体超时 | 408 `timeout` | `timeout_error` | `timeout` |
| 格式错误、带 `previous_response_id`（M1）；请求过大 | 400 `invalid_request`；413 `request_too_large` | `invalid_request_error`；`request_too_large` | — |

- **没有路由接的路径和方法**：也按上面的外壳作答——`/v1/messages`（含 `/api/v1/messages`）下用 Messages 的外壳，其余用 OpenAI（Chat）的外壳；路径不存在回 404 `not_found`，方法不对回 405 `method_not_allowed` 并保留 `Allow` 响应头，请求 ID 两个头照常带。new-api 对未知路径也回 404 的 OpenAI 外壳（new-api `controller/relay.go` 的 `RelayNotFound`，2026-10-19）。
- **`Retry-After`** 用整数秒，不超过 60 秒。Claude Code 对超过 60 秒的值不再重试。
- **429 的原因只进指标**：上表所有 429 的错误码都是 `rate_limited`，客户端程序不必区分；节点按原因（`ip_in_flight`、`account_in_flight`、`key_rate`、`count_rate`、`account_gate`、`queue_full`、`queue_key_full`、`queue_wait`、`credentials_busy`）计入指标 `axiom.gateway.rate_limited`，运维凭它判断是哪一道限制在拒绝（逐项含义见 [gateway 模块](../../internal/gateway/README.md#并发背压与共享状态)）。原因不写进响应：OpenRouter 的 429 也只给统一的 `error_type`（`rate_limit_exceeded`），要等的时间放在 `Retry-After`，它对付费模型没有平台级的请求上限，只有免费模型的额度和防 DDoS（OpenRouter《Limits》《Errors and Debugging》，2026-10-18）；sub2api 把原因编进错误码是它自己的做法（`gateway_queue_full`、`gateway_concurrency_limit`，`backend/internal/handler/concurrency_error_response.go`）。
- **供应商自己的 401、402、403、429** 只用来换凭证（同一端点另一把凭证，没有可用凭证时换下一个端点）或换端点，从不当作我们的认证或钱包错误返回给客户端。
- **流中途出错**：状态码保持 200，按各格式的方式在流里报错，然后结束。结算已提交的结尾带用量和费用，没提交的只带用量：
  - Chat：一个带顶层 `error`、结束原因为 `error` 的数据块；
  - Anthropic：`error` 事件；
  - Responses：`response.failed` 事件。
  - 上游自己报的错按它的类别定（`protocol.FaultOf`）：拒绝请求本身的（上下文过长、内容被拒、请求不合法）算这个请求的，不换端点重试，客户端读到上游的原话（格式支持的还带 `code`、`param`）；请求的写法没问题而端点失败、限流、看不出来的，都算供应商的失败，用统一的说法，提交点之前还可以交给下一个端点（[路由的「失败怎么办」](routing.md#失败怎么办)）。失败事件立即结束尝试，不等上游关连接；按字段值解码类别，转义写法与 HTTP 错误同义。
- **非流式请求出错**：最后一次尝试生成了部分内容后失败时，Chat 返回 200，带已生成的内容、`finish_reason: "error"`、`error`、用量和费用；Responses 返回 200，`status: "failed"`，带 `output`、用量和费用；Messages 例外，返回 502 错误体、不带内容，用量和费用放在 `axiom` 对象里。没生成内容的，返回上表的状态码。未完成的工具参数不修补。
- **对中转站对接方友好**：计费类问题绝不返回 401；不使用 new-api 默认会触发自动禁用渠道的措辞，例如 "You exceeded your current quota"、"Your credit balance is too low"（new-api 源码，2026-10）。这份措辞清单写成测试，防止以后被无意改坏。

### 保活

- **开始转发内容之前不发任何东西**：这时还可能换端点，状态码也还没确定（见[路由的「失败怎么办」](routing.md#失败怎么办)）。
- **开始转发之后**：上游仍在正常传输、但暂时没有新内容时，才发 SSE 注释行保活。上游长时间完全无响应，就报错结束，不无限保活。
- **Anthropic 格式**：上游原生的 `ping` 事件照常透传；从没有 `ping` 的上游转换过来时，由我们补发，这是 Claude Code 指南的要求。
- **不发自创的 JSON 心跳**：只用各格式自己定义的保活方式。sub2api 曾因发送自创的 JSON 心跳导致严格的客户端崩溃（sub2api issue #7946，2026-10）。

### 路由参数

- **接受 OpenRouter 的参数**：
  - `provider` 对象：只走、排除、指定顺序、是否允许回退、要求支持全部参数、数据政策、排序、最高价、性能条件；
  - `models` 备选模型；
  - 模型名变体 `:nitro`、`:floor`：等同 `provider.sort` 的 `throughput`、`price`；`:exacto`、`:online`、`~…-latest` 别名返回 400，`:free` 一类目录变体返回 404，见下文「模型名变体」；
  - `@preset/名称`：尚未实现，现在按模型不存在处理（[#313](https://github.com/TYCT-tyct/Axiom/issues/313)）；
  - `session_id`、`prompt_cache_key`；
  - `user`：终端用户标识，用于防滥用和归因。

  这些参数的语义见[路由的「请求覆盖」](routing.md#请求覆盖)。
  - **第一版能兑现的**：`provider` 里的 `sort`（`price`、`throughput`；`latency` 无对应排序，返回 400）、`data_collection`、`zdr`、`max_price` 的 `prompt` 与 `completion`、`preferred_min_throughput`、`preferred_max_latency`；写了名字的 `only`、`ignore`、`order`，`allow_fallbacks: false`，`require_parameters: true`，`quantizations` 和不认识的成员返回 400 `invalid_request` 并指明成员名，不当作没写（原因见「待定问题」）。
- **我们自己的扩展**：放在单独的 `axiom` 对象里，也可以用 `x-axiom-*` 请求头传，方便无法修改请求体的工具。
- **转发前处理**：路由参数和扩展字段在转发前删除；会话和用户标识换成派生值（见上文「转发规则」）。

### 模型名变体

模型名后面可以跟 `:` 开头的变体，来源是 OpenRouter 的《Model Variants》总览页和各变体页（`openrouter.ai/docs/guides/routing/model-variants/` 下的 overview、nitro、floor、exacto、online、thinking、free，2026-10-19）。网关在查目录之前读掉变体，目录只收到基础名字：目录里没有带 `:` 的名字（见[模型目录的「名字解析」](catalog.md#名字解析)），变体从不新建模型，也不落到基础模型。读法只在 `internal/gateway/variant.go` 一处，调用、`models` 里的备选和 `count_tokens` 共用。响应和调用记录里的 `model` 仍是客户端写的整个名字。

| 变体 | OpenRouter 的行为 | 这里 |
|---|---|---|
| `:nitro` | 按吞吐排序，并准入 priority 层的端点（nitro 页） | 等同 `provider.sort: throughput`；层级准入见下 |
| `:floor` | 按价格排序，并准入 flex 层的端点（floor 页） | 等同 `provider.sort: price`；层级准入见下 |
| `:exacto` | 按质量信号排序：工具调用成功率、吞吐、延迟、基准结果（exacto 页） | 400 暂不支持：端点的质量分和工具调用错误率还没有测量（`route.go` 里质量固定为 1），按它排出来是任意的顺序 |
| `:online` | 已弃用，等于 `web` 插件（online 页） | 400：没有联网搜索插件（`plugins` 现在被丢弃，见[对照表](../openrouter-parity.md)） |
| `:free` `:batch` `:thinking` `:extended` | 目录变体，在目录里有自己的条目和价格；没有对应条目时不回落到基础模型，查不到（总览页：去掉 `:free` 会得到付费条目） | 目录没有这些条目：404 `model_not_found`，说明写出是哪个变体。`:thinking` 已弃用，OpenRouter 改用 `reasoning` 参数（thinking 页），说明里提示 |
| 别的后缀 | 总览页：只有列出的才是变体 | 404 `model_not_found` |
| `~作者/系列-latest` | 解析到该系列最新的具体模型，响应的 `model` 是具体模型（latest-resolution 页） | 400 暂不支持，见「待定问题」 |

- **叠加**：变体可以任意顺序叠加，最后一个排序变体生效（总览页）：`:floor:nitro` 按吞吐，`:nitro:floor` 按价格。`:exacto` 之后又写了排序变体时，它已被替换，不报错。
- **与 `provider.sort` 同时写**：显式的 `provider.sort` 优先。依据是 exacto 页的一句话："An explicit sort by price, throughput, or latency overrides it"；nitro 和 floor 页没写这一点，按同样处理，所以 `:exacto` 加 `provider.sort` 也可用。
- **备选 `models`**：排序是整个请求一个（路由没有按模型的排序）。备选名字按同样的规则读：不支持的变体回 400，不当作略去；目录变体和不认识的后缀，备选按"不存在"略去（与没有变体时一样）；备选的排序变体与请求最终的排序不同回 400；备选的 `:exacto` 只有显式 `provider.sort`（或它自己名字里后面的排序变体）能替换，主模型名上的排序变体不算，因为它不会按质量排备选。`count_tokens` 读同样的名字和 `provider`，拒绝的与调用一致。
- **名字长度**：整个模型名（含变体）至多 200 字节，超过回 404 `model_not_found`，在准入之前。调用记录保存客户端写的整个名字，列最多 200 个字符；目录原来也不认更长的名字，不能让叠加的后缀绕过这个界限，被服务之后才丢了记录。
- **层级准入没有做**：nitro 页说它同时准入 priority 层，floor 页说它同时准入 flex 层。这里只做排序一半。不写 `service_tier` 的请求本来就可以由 standard 和 priority 服务（协议 `anyTier`），flex 要客户端自己写 `service_tier`；为 `:floor` 改写发往上游的 `service_tier` 会改变预留和计费依据，另做。

### 给其他中转站对接的接口

| 接口 | 用途 |
|---|---|
| `GET /v1/dashboard/billing/subscription`、`GET /v1/dashboard/billing/usage` | OpenAI 旧版的余额接口。new-api 用它查询 OpenAI 类型渠道的余额：余额 = `hard_limit_usd` 减去以 0.01 为单位的 `total_usage`；`usage` 的 `total_usage` 是累计消费，不看 `start_date`、`end_date`（new-api 源码，2026-10） |
| `GET /api/v1/key` | key 的上限、剩余额度、各时间窗口的用量（OpenRouter 格式），供兼容 OpenRouter 的工具使用 |
| `GET /api/v1/credits` | 账户余额（OpenRouter 格式），供兼容 OpenRouter 的工具使用 |
| `GET /api/v1/generation?id=` | 按调用编号查询单次调用的详情 |

- **单位**：这些接口里的金额都以本站的记账货币计。二级站点用人民币记账时，对方会把数字当美元显示，这在站点的接入说明里写明。
- **余额与用量**：`total_credits` 和旧版的 `hard_limit_usd` 是余额加累计消费，`total_usage` 是累计消费（旧版接口以 0.01 为单位，不看日期参数），所以额度减用量总是余额；在途预留不扣。`generation` 只读调用记录，行为见上文「用量与费用」的调用编号。
- **`POST /v1/messages/count_tokens`** 返回估算的输入 token 数（`{"input_tokens": n}`），口径、前置检查和理由见[模块 README](../../internal/gateway/README.md#辅助接口)；它不收费、不记录。

### Responses 的会话状态

- **排在 M2**：续接是第一版的功能，在 M2 提供。在那之前，带 `previous_response_id` 的请求返回 400，`DELETE /v1/responses/{id}` 返回 404。
- **单独保存**：会话状态是续接功能所需的内部数据，与对话内容的记录分开保存，不受对话内容开关的影响。保留时间由平台配置，默认 30 天，与 OpenAI 的默认一致（OpenAI《Conversation state》，2026-10）。它和对话内容一样按账户加密、按消息去重，销毁账户密钥时一并销毁（见[记录与告警的「加密与密钥」](observability.md#加密与密钥)）。
- **读一次会话状态**：续接按工作区和回复 ID 从保存处读一次，是[总体架构的规则 3](../architecture.md#模块边界规则)的第四个例外；读不到就当作没有这个回复，不重试，也不按 ID 扫其他工作区。
- **不提供读取**：不支持按 ID 读回回复内容（OpenAI 的 `GET /v1/responses/{id}`），因为任何人都不能通过产品查看对话内容；续接不需要这个接口。
- **按工作区隔离**：同一工作区的 key 可以凭回复 ID 续接，其他工作区不行。
- **`store: false`** 的请求不保存状态，不能凭 ID 续接。
- **删除**：`DELETE /v1/responses/{id}` 立即删除；用户注销时按注销规则处理（见[账户与租户的「停用与注销」](accounts.md#停用与注销)）。
- **续接方式**：由我们把历史拼回完整的输入再发给上游，不依赖上游自己保存状态，所以换端点也能续接。其中只有原上游才认的加密推理内容，按上文「转发规则」处理。

### 与 new-api 的有意差异

对外行为要和 new-api 对得上，靠差分测试保证：`just diff`（在 CI 服务器上：`remote-check.sh <worktree> diff`）用 new-api 的官方镜像（按摘要固定，v1.0.0-rc.45，2026-10-17）和一个 Axiom 节点接同一个假上游，上游回放 `internal/protocol/testdata` 的录制样本；同一组请求分别发给两边，比状态码、响应头、JSON 体、SSE 事件。它要拉镜像、起容器，所以不在 `just check` 里，也不在每个 PR 的 CI 里；升级镜像是一个决定：读发布说明、跑一遍、逐条处理新出现的差异。

- **比什么**：Messages 和 Responses 客户端调用同格式的模型（透传），Chat 客户端调用 Messages 和 Responses 的模型（两边都做转换）和调用说 Chat 的模型（DeepSeek 一类，透传）；Messages、Responses 客户端也调用该 Chat 模型（反向转换）；各自的文本、工具调用、推理、图片，流式和非流式，上游的 400、429、500，以及请求自己的错误（JSON 不合法、缺必填项、模型不存在、key 错）。每种格式让 new-api 的渠道和 Axiom 的端点说同一种格式，上游收到的是同样的路径，比的是怎么转发回复，不是谁选了哪种格式。
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
| Chat 转成 Messages 时，thinking 块以空 signature 开始；不伪造思考签名，再次调用原生端点前删除无签名思考 | Chat 明文推理没有 Anthropic 签名，不能制造可回放的签名 |
| Chat 转成 Messages 时，message_start 的用量从 0 开始，结算后的 message_delta 才携带输入、缓存、输出的完整计数 | Chat 上游通常在最后一块才报用量；不能把尚未收到的计数写成真实值。内部计费沿用 Chat 的原始报告 |
| Chat 转成 Messages 时，new-api 在用量里附加 `billing_usage`、`claude_cache_creation_*` 等内部计费成员，Axiom 不写这些成员 | 客户端只接收 Messages 格式的用量，内部计费不改变协议外壳 |
| Chat 转成 Messages 时，Axiom 在最终用量里补齐为 0 的 `cache_creation.ephemeral_*_input_tokens`、`server_tool_use.web_search_requests`，new-api 省略 | 与既有 Messages 结算用量形状一致，不把缺报解释为实际使用 |
| Chat 转成 Messages 时，Axiom 写 `stop_sequence: null`，message_start 也写 `stop_reason: null`，new-api 省略 | 与 Anthropic 的消息生命周期一致，尚未结束或未匹配停止串时使用 null |
| Chat 转成 Messages 时，并行工具块在下一个块开始前关闭，new-api 到回合末尾才统一关闭 | Messages 的内容块依次输出；工具参数保持各自归属 |
| Chat 转成 Responses 时，输出项 ID 由调用编号和输出序号组成，created_at 为调用时间 | 不泄露上游身份；再次调用原生 Responses 端点时删除本站输出项 id |
| Chat 转成 Responses 时，成功响应的 `error`、`incomplete_details` 为 null，new-api 省略 | 与 [OpenAI 的响应事件定义](https://developers.openai.com/api/reference/resources/responses/streaming-events)一致 |
| Chat 转成 Responses 时，new-api 附加 null 的 `instructions`、`metadata`、`previous_response_id`、`reasoning`、`tool_choice`、`tools`、`truncation`、`user`，以及 0 的 `max_output_tokens`、`temperature`、`top_p`、false 的 `parallel_tool_calls`、`store`；Axiom 省略 | Chat 上游没有回报这些设置，转换器不凭空声称请求采用了这些值 |
| Chat 转成 Responses 时，new-api 为非图片输出项附加空 `quality`、`size`，为非消息项附加空 `role` 和 null `content`，为推理项附加 `status`；Axiom 省略 | 按输出项自己的类型写成员，推理项的状态是可选的 |
| Chat 转成 Responses 时，new-api 在 `output_item.added` 顶层另写工具的 `item_id`，Axiom 只在 `item.id` 标识输出项、`item.call_id` 标识调用 | 与 OpenAI 的输出项事件外壳一致，避免混淆输出项和工具调用 |
| Chat 转成 Responses 时，Axiom 输出 `content_part.added/done` 并按输出项出现顺序结束各项，new-api 省略文本内容块事件且先结束文本再结束推理；事件序号因此不同 | 完整发送 OpenAI 的内容块生命周期；每边的序号必须恰好对应自己的事件位置，内容和工具参数仍逐字段比较 |
| Chat 转成 Responses 时，new-api 的用量还带 Chat 名称、`billing_usage` 和 `claude_cache_creation_*`；Axiom 使用 Responses 名称，省略 null 的旧明细及为 0 的 `audio/image/text_tokens`，补齐为 0 的缓存写明细 | 复用既有 Responses 结算用量口径，不暴露内部计费外壳，不伪造媒体用量 |
| Chat 转成 Responses 流时，new-api 省略 `output_tokens_details.reasoning_tokens`，Axiom 保留上游报告（推理样本为 30，其余样本为 0） | 推理 token 属于真实用量，转换不能丢失；与同格式 Chat 的计费项一致 |
| 上游没有报用量时（Messages 的思考样本），两边各自估算 token 数 | 估算口径不同，见[计费的「用量来源与暂估」](billing.md#用量来源与暂估) |

### 版本与兼容

- OpenAI、Anthropic 格式跟随它们的官方规范。
- 我们自己的扩展只增不改。
- 确实需要不兼容的修改时，使用新的版本路径；旧版本经过公告期后才下线，公告期在发布时说明。
- 管理接口（控制台、管理后台、运营）用 OpenAPI 契约，版本规则相同。

## 决策与理由

| 决策 | 理由 |
|---|---|
| Chat 旧客户端的回复恢复旧函数表示，现代客户端保持工具数组 | [OpenAI Chat reference](https://developers.openai.com/api/reference/resources/chat/subresources/completions/methods/create) 明确 `functions` → `tools`、`function_call` → `tool_choice`，消息中的旧调用由 `tool_calls` 取代。[OpenRouter 的工具规范化说明](https://openrouter.ai/blog/insights/agent-frameworks-compared-tool-calling-schema-handling/)和 [API reference](https://openrouter.ai/docs/api/reference/overview) 只承诺现代 `tool_calls`，未说明旧请求的回映射（2026-10-29 核实，未花费调用实测）。本任务要求兼容旧客户端，因此有意增加只针对旧请求的回映射，并关闭并行；不采用对所有回复统一降级，因为会破坏现代客户端和并行调用。既有稳定 ID 配对逻辑同时用于同格式与转换；不重编码整份请求，以保留工具参数、签名和供应商扩展。此对外兼容决策依负责人在 [#309 的授权](https://github.com/TYCT-tyct/Axiom/issues/309#issuecomment-6478816797)「遇到问题自己解决问题和判断最优而不是问我。」执行，仍需独立评审 |
| 同时提供 `/v1` 和 `/api/v1` 路径 | 负责人同意。OpenRouter 的客户端只改地址就能接入 |
| 请求头按白名单转发，`anthropic-beta` 不筛选具体的值 | Claude Code 官方的网关兼容指南明确要求原样转发协议头；客户端的凭证和身份信息不能流向上游 |
| 上游 4xx 保留原文，只抹掉供应商身份 | Claude Code 依赖上游错误的原话自动恢复；同时不能暴露货源。负责人 2026-10-18：影响兼容的行为照 new-api、sub2api。实现见「错误」：适配器仍只读代码、不留文字，共用的调用代码在拒绝请求的失败上另存一份清洗过的 `Rejection`，只给这次回复用。不选让适配器带原文：每个适配器各清洗一遍，漏一个就泄露 |
| Chat、Responses 的错误外壳照 new-api 与 OpenAI（字符串 `code`、`type`、`param`），不照 OpenRouter 的数字 `code` | 负责人 2026-10-18：错误码和外壳影响兼容，照搬 new-api。OpenAI 官方客户端和多数 SDK 读 `error.type`、`error.code` 字符串；OpenRouter 的数字 `code` 重复了状态码，状态码在状态行里本来就有。统一的 `error_type` 仍在 `metadata` 里，两种读法并存，不丢信息。代价：读 OpenRouter 数字 `code` 的客户端要改读状态行 |
| Chat 回复的推理文本同时写 `reasoning` 和 `reasoning_content` | 负责人 2026-10-18：照 new-api 写 `reasoning_content`。两个名字都有人用：`reasoning_content` 是 DeepSeek、new-api、LiteLLM 的（LiteLLM 文档《Reasoning Content》），`reasoning` 是 OpenRouter 和新版 vLLM 的（OpenRouter《Reasoning Tokens》，vLLM《Reasoning Outputs》，2026-10-19）；读一个的客户端对另一个当作未知字段忽略。同文并存，客户端读哪个都对；只留一个就会让另一半客户端读不到推理。已知风险：若有客户端把两个字段都显示，推理会出现两遍，没有实测过，发现再收回其中一个。代价是推理文本多发一份 |
| 变体在网关里读，目录和路由都不认识后缀 | 变体是路由开关，不是模型：放进目录会让一个模型分裂成多个条目，也要放宽目录"带 `:` 的名字永远查不到"的不变量，那条不变量正是为了保证带后缀的写法落不到基础模型。读法集中在 `variant.go`，调用、备选和 `count_tokens` 共用，不会各自读出不同的结果 |
| `:free`、`:thinking` 等目录变体回 404，不改写请求 | 总览页把它们列为目录变体，没有条目就是查不到，也不回落到基础模型：`:free` 落到付费模型会替客户端花钱。做不了的请求回 400、不静默忽略（见「路由参数」）；这里不是做不了而是没有这个模型，所以 404。`:thinking` 若改成"打开推理"要改写请求内容，违反"请求内容不改"，OpenRouter 也已弃用它 |
| 显式 `provider.sort` 压过变体 | exacto 页明说显式排序覆盖它；请求体里的字段比名字里的后缀更具体，nitro 和 floor 照此处理。代价：写了 `:nitro` 又写 `sort: price` 的客户端得到按价格排序，与 OpenRouter 的实际行为未核对（页面没写） |
| `~…-latest` 别名回 400，不做 | 要让它成为数据，目录要有可由管理员改指向并留审计的别名（现在名字只追加、公开名字不许 `-latest` 和 `~`，见[目录的决策](catalog.md#决策与理由)），这是数据模型改动，需要负责人同意和独立评审；OpenRouter 的响应 `model` 是具体模型，与本站"回显客户端写的名字"也要先定。认得出写法并明说不支持，好过 404 让客户端以为模型被下线 |
| 会话、用户标识换成派生值，而不是删除 | 删除会让上游缓存失效；原样转发会串租户、泄露标识 |
| 响应头按白名单放行 | 上游的请求 ID、组织名等头会暴露货源 |
| 开始转发内容之前不发保活 | 此时还可能换端点；提前发出任何字节，状态码就无法再改 |
| 402 区分上限来源，只有暂时性的带 `Retry-After` | 与 OpenRouter 一致；客户端据此判断该等待重试还是去充值 |
| 计费类问题不返回 401，避开自动禁用措辞，并写成测试 | 把我们当上游的 new-api 收到 401 或特定措辞会自动禁用渠道，临时问题会被放大成停用 |
| 扩展字段放在独立的 `axiom` 对象 | 不与 OpenRouter 的字段冲突 |
| 提供 OpenAI 旧版和 OpenRouter 格式的余额接口 | 第一版范围要求支持其他中转站接入（见产品需求）。new-api 查询 OpenAI 类型渠道时用前者；查询 OpenRouter 渠道时写死访问 openrouter.ai，所以后者只服务兼容 OpenRouter 的工具 |
| `/api/v1/credits` 调用 key 就能读，不要求管理 key | 开发者按负责人的授权决定（对话中的全权委托）。OpenRouter 要求管理 key（OpenRouter《Get remaining credits》，2026-10-16）；M1 没有管理 key 的接口，照办则无人能调，而读到的只是本账户的余额。M2 有管理 key 时按其权限再定 |
| `count_tokens` 是本站的估算，不转发上游 | 开发者按负责人的授权决定（对话中的全权委托）。Anthropic 的接口返回精确值；转发要占用端点名额，且需要走路由。文本和图片的估算偏高，客户端据此裁剪只会偏早；文件按固定页数估，页数多的文件会被低估，所以不承诺不超窗口。口径见[模块 README 的「辅助接口」](../../internal/gateway/README.md#辅助接口) |
| Responses 会话状态由我们保存，偏离 OpenRouter | 负责人同意。OpenRouter 的 Responses 接口不保存状态，`store: true` 和 `previous_response_id` 都会返回 400（OpenRouter《Responses API》，2026-10）；支持续接能让 Codex 少发重复内容、省钱。会话状态单独保存，内容保存关闭期间续接也可用 |
| 会话状态按工作区隔离，单独保存 | 同一账户的不同工作区可能属于不同项目；负责人授权开发者按用户体验判断，单独保存让对话内容关闭期间续接仍然可用 |
| 辅助请求走便宜端点作为预设可选规则，默认关闭 | 负责人同意。见[路由](routing.md#排序的先后) |
| 没有可用端点：永久性的返回 404，暂时性的返回 503，满载返回 429 | 开发者按负责人的授权决定（对话中的全权委托）。CONTRIBUTING「产品设计」：行为默认对齐 OpenRouter，它对过滤后没有端点返回 404 "No endpoints found…"（文档与帮助中心，2026-09；价格页 FAQ 的模型下线同为 404，2026-10）；客户端会自动重试 5xx，而永久性的条件重试也不会变。暂时性的保留 503 |
| Messages 的非流式请求中途失败返回 502、不带内容，用量和费用放在 `axiom` 对象里 | 开发者按负责人的授权决定（对话中的全权委托）。通用规则是把已生成的内容和错误一起返回（上文「非流式请求出错」），但 Messages 的非流式响应没有表示出错的结束原因（`stop_reason` 只有 `end_turn`、`max_tokens`、`stop_sequence`、`tool_use`、`pause_turn`、`refusal` 等），把半截内容当成功返回会让客户端以为完整。这是按格式落实同一条规则；Chat、Responses 照规则返回内容 |
| 续接在第一版做，排在 M2；M1 对 `previous_response_id` 返回 400 | 开发者按负责人的授权决定（对话中的全权委托）。负责人同意的是第一版要有续接（本表「Responses 会话状态由我们保存」），第一版是 M0–M3，排进哪个里程碑由开发者定。M1 的验收客户端用不到：Codex 的 HTTP 传输每轮发完整历史（OpenAI《Unrolling the Codex agent loop》，2026-01-23："Codex does not use it today"），只有 WebSocket 用它，M1 不支持 WebSocket。M1 的端到端测试用录制样本验证这个前提，不成立就提前到 M1。省四个 PR，不推迟验收 |
| Chat 请求落在说 Chat 的端点上时同格式转发，端点同时说 Chat 和 Messages、Responses 时也先选 Chat | 转发不改变含义，转换会拒绝表达不了的成员（`NotSupported`，客户端得到 400），所以能不转换就不转换；new-api 的做法相同（`chat_completions_to_responses_policy` 默认关）。代价：此前走 Messages 的 Chat 请求，在同时说 Chat 的端点上改走 Chat，行为按端点的 Chat 实现 |
| Chat 端点的输出上限写在端点读的那个成员里，按上游模型名定（o1、o3、o4、GPT-5、GPT-6 Astra/Sol/Luna 用 `max_completion_tokens`，其余 `max_tokens`），客户端的两个成员都拿掉再写 | 端点忽略的成员不是预留的上界：客户端只写 `max_completion_tokens:1`、端点只认 `max_tokens`，上游就按自己的默认值生成，超出预留的费用没人付（I1）。规则抄 new-api `relaykit/dto/openai_request.go` 的 `GetOpenAIChatCapabilities`（32e5874）；它按模型名判断，规格里还没有说明这件事的键，所以和「方言是数据、不按名字猜」有出入，规格有了这个键再改用它，待定。代价：名字不在名单里的新 OpenAI 模型会收到 `max_tokens`，被拒绝时按原话返回 |
| `stream_options.include_usage` 对 Chat 端点一律强制为 `true` | 没有上游报的用量，计量只能估算，收费不准；new-api、sub2api 都强制。代价：不支持 `stream_options` 的上游会拒绝请求，new-api 对非 OpenAI 渠道会删掉它（`ConvertOpenAIRequest`），本站没有这类上游的数据，遇到再按端点加开关 |
| Chat 端点只写一个推理名字时，本站补另一个 | 「转发规则」的 Chat 回复写两个名字是负责人 2026-10-18 的决定，对 Chat 上游同样适用，否则 DeepSeek 的客户端读得到、OpenRouter 的客户端读不到；补在流的每一块里，聚合的整份回复自然两个都有 |
| 跨格式时缓存标记按 OpenRouter 转换，表达不了的丢掉，不拒绝请求 | 负责人在对话中授权开发者按推荐自行决定。CONTRIBUTING「产品设计」要求对齐 OpenRouter：它把文本块的 `cache_control` 转成 OpenAI 的 `prompt_cache_breakpoint`，TTL 不转（OpenRouter《Prompt caching》，2026-10-16）。拒绝会让带缓存标记的 Chat 请求到不了任何 Responses 端点；标记不改变模型读到的内容，收费按上游报告的实际缓存用量，丢掉只影响命中率，不会算错钱 |
| 结算写不进时，内容完整的照常结束、不带费用、带 `billing_pending`；只有截断的才报 `billing_unavailable` | 不发出没入账的费用，也不让客户端把已完成的一轮当失败重发、付两次。没选一律报 `billing_unavailable`：主库切换时，整批在途的请求会被客户端重发、重复收费 |
| `/responses/compact` 的回复原样透传、请求只做最小改写：请求带 `stream: true` 回 400，结算写不进时响应体里也不加 `axiom.billing_pending` | 回复是上游的整份回复，不是我们生成的流：加任何字段就不再是原样，客户端按上游的格式读它；这个端点只给整份回复。请求则不能原样：模型名要换成端点的名字（否则上游拒绝，或按别的模型计费），`user`、`safety_identifier`、`prompt_cache_key` 要换成派生值（上文「会话和用户标识换成派生值」），路由参数要删除；生成才用的成员（`store`、`include`、输出上限、`stream`）不加也不改，空闲超时与预算照旧。费用只在调用记录里查，M1 没有校正（上文「用量与费用」）。不选在透传的 JSON 里插字段或为压缩请求再拼一份生成请求的改写：前者改了上游的回复，后者要猜它接受哪些成员 |

## 待定问题

- 各字段的最终名称和结构写入接口契约时确定
- `provider.only`、`ignore`、`order`：用户写的是官方 API 的名称或中转站分组的代号（[术语表](../glossary.md)），路由按供应商 ID 过滤；名称到 ID 的对应还没有模块提供（routing 的 `Endpoint` 不带名字，代号在 suppliers 的分组上），有了才能支持，在此之前返回 400。`max_price` 的单位按本站记账单位每百万 token，二级站点用人民币记账时与 OpenRouter 的美元不同，是否要换算待负责人定
- 402 的错误体附所需金额和在途预留，`limit_source` 加上 `pending`（进行中的请求占满了余额），会重置的上限的 `Retry-After` 是窗口结束的时间：billing 的 `Insufficient` 现在只报来源和是否会重置，网关回 `balance`、`workspace_budget`、`key_limit`，会重置的上限固定 `Retry-After: 60`；随 billing 补上这些信息后改。现状：`Insufficient` 只有 `Source` 和 `Transient`，也没有“最早释放的时间”可报（`deadline` 是兜底期限，约 90 分钟）；报金额还要定币种字段和金额的显示格式，这随 `usage.cost` 在 G5 定（[gateway 模块 README](../../internal/gateway/README.md#入口与准入)）
- 准入后不再发起新尝试的窗口（默认 120 秒）与上游空闲超时（默认 180 秒）：提交前的空闲超时要超过窗口才到，所以默认值下它不触发换端点，只返回最后一次尝试的错误；要换端点，空闲超时按模型调到窗口以内。上文的限制表和错误表对窗口到期是“返回最后一次尝试的错误”还是 408 说法不一，gateway 照限制表：有过尝试返回 502，一次也没发出过才是 408
- `~作者/系列-latest` 别名（需要负责人定）：推荐在目录加一张别名表（别名、指向的模型、版本号），管理员改指向时记审计，公开名字规则对别名单独放行 `~` 前缀；网关像变体一样在查目录前识别 `~`，再用别名表解析。要先定响应的 `model` 回显客户端写的别名还是具体模型（OpenRouter 回具体模型）。不选"按发布时间自动取最新"：目录没有系列和发布时间，自动取会在没人确认时改变用户实际用的模型和价格（见[目录的「切换与迁移」](catalog.md#模型的生命周期)）
- `:nitro`、`:floor` 的层级准入：为 `:floor` 准入 flex 层要决定是否改写发给上游的 `service_tier`，及其对预留和计费的影响
- Claude Code 的 Bedrock、Vertex 格式和 Gemini 原生格式的支持，以后再做
