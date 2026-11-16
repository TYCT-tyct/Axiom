# 接入与协议转换：存档

[接入域](../domains/gateway.md)、[gateway](../../internal/gateway/README.md) 和 [protocol](../../internal/protocol/README.md) 的调研、测量与推导。这里不是现行规则：规则以那三份文档和代码为准，这里的数字和来源可能已经过时。

## 接入域

### 参考基线与核对来源

接入域的行为对照下列外部文档定下，核对日期见各条：

- OpenRouter：Models / Endpoints / Providers 的 OpenAPI（2026-10-29），Errors and Debugging（2026-11-02，`error.code` 为与状态行相同的数字；预算错误 403 带 `metadata.limit_source`，余额 402），Limits（2026-10-29，付费模型无平台级速率上限、`rate_limit` 已弃用、Reset 头未标单位），Provider Routing（2026-11-01，排序对象只有 `by`、`partition`），Model Variants 各页（2026-10-29），Response Caching（2026-11-02），Plugins / Response Healing / Message Transforms（2026-10-29），Server Tools 与 Web Search（2026-11-01），Free Models Router（2026-10-31），Reasoning Tokens（2026-10-29，exclude 不减费用），Prompt caching（2026-10-16，`cache_control` → `prompt_cache_breakpoint`、TTL 不转），公开 `/api/v1/models` 无需 key 返回价格（2026-11-02）。OpenRouter 没有 `/completions`，它让客户端把 `prompt` 发到 `/chat/completions`（OpenAPI v1.0.0，2026-10-31）。
- Claude Code《Gateway compatibility guide》（2026-10）：协议头原样转发、`/v1/models` 3 秒超时且不跟跳转、Messages 要补发 `ping`、`Retry-After` 超过 60 秒不重试。
- OpenAI《Create completion》（2026-10-31）：`prompt` 的四种形态、`best_of` 不能流式、`max_tokens: 0` 合法；《Conversation state》默认保留 30 天；《Unrolling the Codex agent loop》说明 Codex 的 HTTP 传输每轮发完整历史，只有 WebSocket 用 `previous_response_id`。
- new-api：上游 4xx 的四项原样交出并遮蔽网址与 key；每个流带 `X-Accel-Buffering: no`；Chat 强制 `stream_options.include_usage`；`max_completion_tokens` 的模型名单；DeepSeek `prompt_cache_hit_tokens` 的处理；未知路径回 OpenAI 外壳的 404；旧版余额接口的计算方式；若干措辞会触发自动禁用渠道。sub2api 同样强制 Chat 流的 usage；它曾因自创 JSON 心跳让严格客户端崩溃。
- 推理字段命名：`reasoning_content` 是 DeepSeek、new-api、LiteLLM 的名字，`reasoning` 是 OpenRouter 和新版 vLLM 的名字（2026-10-19）。两个都写有一个未实测的风险：同时显示两个字段的客户端会显示两遍。

### 方案比较

- **搜索后端**：Firecrawl 按“每次搜索 $0.005 + 每条结果 $0.0125”计价（2026-11-01），对上两个成本字段；SearXNG 可自建在大陆。Tavily 按请求扣 credit、不按结果；Parallel 的定价与国内可达性不注册无法确认。大陆侧直连 Firecrawl 未在本机验证，部署时用后台测试按钮确认。
- **文件解析后端**：选 Docling serve（MIT、可自托管、CPU 能跑、模型可预装）。比较过 Mistral OCR 与 Cloudflare Workers AI（要账户和密钥、文件出境或绑区域）、Apache Tika（无 OCR）、MinerU（要 GPU）、Marker（许可限制）、unstructured（依赖重）。OpenRouter File Parser 价格：`mistral-ocr` $2/1000 页（美国区 $2.2），`cloudflare-ai` 免费（2026-11-02）。
- **旧补全 logprobs 聚合**：比较过每块克隆合并（O(n²)，临时副本记账难以退回）、解码成 Go 结构（内存数倍、转义和重复键要自理）、按四个数组追加原字节（选定）。
- **`openrouter:web_search` 的计费粒度**：比较过一次尝试下挂多跳（回执和成本只能合在一起，需要逐跳明细表和第二套对账）与每次采样一次尝试（选定，`attempt_seq` 上限放宽到 33，主键不变）。
- **响应缓存命中位置**：在准入前查缓存能省一次预留，但需要一条不预留、不结算、不写记录的路径，余额不足的账户会绕开准入；选择命中仍走准入。
- **续接排期**：第一版要有续接；排进 M2 是因为 M1 验收客户端用不到（Codex HTTP 传输不用 `previous_response_id`）。

## gateway 模块

### 读取与改写的微基准

`BenchmarkInspect` + `BenchmarkPrepare`，Linux、共享 8 核 AMD EPYC，1 MiB、4 MiB 各五轮中位数相加；基线是复用位置表之前的代码。

| 请求体 | 格式 | 优化前 | 复用位置后 | `Inspect` 分配 | `Prepare` 分配（前 → 后） |
|---|---|---|---|---|---|
| 1 MiB | Messages | 8.61ms | 5.06ms | 36 | 37 → 4 |
| 1 MiB | Responses | 6.20ms | 3.90ms | 29 | 30 → 4 |
| 4 MiB | Messages | 31.26ms | 18.76ms | 36 | 37 → 4 |
| 4 MiB | Responses | 25.23ms | 14.92ms | 29 | 30 → 4 |

`Inspect` 仍做重复键、UTF-8 与深度检查；位置表只存有限的顶层改写成员，不按消息存位置。1 MiB 的单次严格检查已占约 3–4ms，去掉第二遍也到不了 2ms，所以性能文档对大请求用分级预算，而不取消重名检查。`BenchmarkRequestRead` 五轮中位数：256 KiB 0.99–1.32ms，1 MiB 3.96–5.32ms，4 MiB 16.79–31.22ms；一轮 4 MiB Responses 在机器负载约 36 时到 35.81ms（112%），保留为超标证据，不据此放宽预算。

### 账户闸期限的容量推导

闸每秒放行约「批大小 ÷ 持锁时间」个准入。压测里持锁 50–100ms（共享 CI 机器，比性能文档估的 5ms 慢一个量级）：批大小为 1 时 200ms 的期限只容 2–4 个，1 秒内每账户 20 个新请求就有六成回 429；5s 容 50–100 个。合成批次后固定成本按 1/批摊薄，放行速率随之上升，所以压测必须按批大小报告。sub2api 对账号和用户并发名额也是有界等待加长期限（30s），队列满或超时才回 429。

### 准备阶段内存记账的由来

先分配再记账时，预算满了分配已经发生，503 撤销不了峰值。逐处补申请收不拢（连续三轮评审每轮都找到新的漏记点），所以改成两条机制：转换的输出只有一种会增长的缓冲、增长前必申请；Messages、Responses 的读取经 `encoding/json`，分配不经我们的缓冲，整段预留 `nativeRequestCost`。Chat 转出于是按实际申请：1 MiB 长文本约 1.01 倍请求体，此前按形状常数约 6 倍；反向仍约 12 倍，改成流式读入 `writer` 后才能按实际申请（待定）。全部用形状上界时，32 MiB 的 Chat 转出请求预占约 1 GiB；用 `runtime` 计数按实际分配记账分不开并发请求。

### 模型列表按快照预编码的测量

300 个模型（600 项）时，请求只挑选和拼接预编码的条目约 25 µs、1 次分配（`BenchmarkModelList`）；原先逐项建 map 再编码约 2.3 ms、10911 次分配。整份应答取决于 key 的可见集合和每 5 秒重建的路由快照，缓存命中率低，所以不缓存整份。

### 会话请求头名的来源

Responses、Chat 端点上换成派生值的七个会话头（`session_id`、`session-id`、`conversation_id`、`X-Session-Affinity`、`X-Session-Id`、`X-OpenCode-Session`、`X-Conversation-ID`）取自 sub2api 用来定账号的会话名清单（`explicitOpenAIHeaderSessionNames`）。`TestRecordedSub2apiMetadataContract` 的样本是内存测试上游所见的 Axiom 请求，按 sub2api 的 `metadata.user_id` 解析与会话选择契约读取，不是线上抓包。

## protocol 模块

### Chat 请求转换成 Messages、Responses 的逐项映射

`internal/protocol/README.md` 只留代码里看不出的规则；下面是按 Anthropic《Messages API reference》、OpenAI《Create a model response》核对过的逐项映射，测试 `TestConvertToMessages`、`TestConvertToResponses` 每行至少一例。

| Chat | → Messages | → Responses |
|---|---|---|
| 开头的 `system`、`developer` | 顶层 `system` 文本块 | 原位 `message` 项 |
| 对话中的系统消息 | `MidSystem` 为 yes 才原位写 `role: "system"`，须紧跟用户或工具结果；紧跟工具结果还要 `UntrustedSystemAfterTools` 为 no | 原位 |
| 第一条非系统消息不是用户 | `*NotSupported` | 照写 |
| 文本 | 文本块，带 `cache_control`，空文本不写 | `input_text`/`output_text`；`CacheBreakpoints` 为 yes 时 `cache_control` 写成 `prompt_cache_breakpoint`，TTL 丢掉 |
| 消息的 `name` | 按 OpenRouter 的做法以 `name: ` 写在第一段文本前；工具结果不写 | 同左 |
| 助手的 `refusal` | 一个文本块 | `refusal` 部件；别的角色的拒答两边都 `*NotSupported` |
| 图片 | data URL 写 base64，其余写 URL；`detail: low` `*NotSupported` | `input_image` |
| 文件 | 只有 PDF data URL，写成 `document`（`filename` 写 `title`） | `input_file`；`file_id` 原样，只在 `provider.only` 指定唯一供应商时发出 |
| 工具调用、结果 | `tool_use`（`arguments` 须为 JSON 对象，空串当 `{}`）；连续结果合成一条用户消息 | `function_call`、`function_call_output` |
| 空助手消息 | 不写 | 一个空 `output_text` |
| 函数工具 | `input_schema`；`strict: true` 只在 schema 属于子集时写 | `strict` 总写，没写写 `false` |
| `tool_choice`、`parallel_tool_calls: false` | `auto`/`none`/`any`/`tool`、`disable_parallel_tool_use`；思考时或 `ForcedToolChoice` 不是 yes 时强制选工具 `*NotSupported` | 原样 |
| `response_format` | `json_schema` 在子集内写 `output_config.format`；`json_object` `*NotSupported` | `text.format` |
| `temperature`、`top_p` | 1 不写；0 到 1 照写，思考时或 `DefaultSamplingOnly` 不是 no 时 `*NotSupported` | 照写 |
| `stop`、`verbosity` | `stop_sequences`；`verbosity` `*NotSupported` | `stop` `*NotSupported`；`text.verbosity` |
| `user` 等标识 | `user` 写进 `metadata.user_id` | 派生值 |
| `service_tier` | `auto` 照写，`default` 写 `standard_only`，其余 `*NotSupported` | 照写 |

反向（Messages、Responses → Chat）沿用同一张表，参考了 new-api 的 `to_oai_chat_req.go` 和 sub2api 的 `chatcompletions_anthropic_bridge.go`（2026-10-19/20）。差别：Messages 空工具结果写成空字符串；`is_error` 结果在文本前加 `Error:`；工具结果里的媒体拒绝，不像参考实现那样挪到后续用户消息；Responses 的 `truncation` 只收 `disabled`、`background` 只收 `false`；工具结果之前的孤立推理丢弃。

### 准备内存的并发方案

开始时一次申请峰值可以免去重做，但粗上界会失去 Chat 长文本约 1.01 倍的按实申请，精确预估又要复制解码和写出的逻辑；全部串行则预算充足时也失去并行。选了共享首试：失败时只有其他准备的额度能覆盖已知峰值才退回额度、独占写闸重试，装不下就不排队。代价是重试者逐个竞争写闸；`sync.RWMutex` 不保证先来先到，接口没有 context，等闸不能取消。实测的申请量：Chat 转出的长文本约为请求体的 1.01 倍，base64 文件约 3 倍，2 万个小消息约 22 倍；原生转出按每字节 12、每对象 3072、每成员 768 预留。

### SSE 校验器的解码器留存

解码器给读过的每一层嵌套留一张成员名表，只有之后的文档再到那一层才清掉，所以留着的是各层的表之和；只限文档大小界不住（深度递减、每份 200 个成员的 256 份文档实测留着 1.9 MB，`TestValidatorRetains`）。同时限 2 KiB 和 8 层后，上界是 8 张至多 2 KiB 名字的表，为填满它造的文档实测约 120 KiB。不用 `sync.Pool`：分配次数随 GC 时机变化，精确的分配测试会不稳定。

原生两格式之间的 Relay 两层缓冲同时在场，逐字节读取的样本峰值实测 68,251 字节，所以基础额度另加 16 KiB。

### 录制命令的设计

前两轮评审的阻断都出自同一机制：在 SSE 文本上用手写扫描器找 ID 和 key，单独的 CR 行尾、注释里落单的引号或行末反斜杠都能让它错位。改为用 `sse.Reader` 按事件读、用 `jsontext` 定位字符串后，这一类错位不再存在。按 JSON Pointer 位置替换 ID：漏列位置只会留下一个真实 ID，提交前看得到；按成员名猜会改掉工具参数这类调用方数据，改错了没有检查能发现。命令放在 `internal/record` 而不是 `tools/`，是为了写盘前引用 `internal/streamcheck`；它用自己的 HTTP 客户端而不是 `platform/egress`，因为开发者填的地址可能在本机或内网。

### 几条取舍的外部依据

- Chat 输出上限写进哪个成员，照 new-api 的 `GetOpenAIChatCapabilities`（按模型名判断）；强制 `include_usage` 与 new-api 的 `ForceStreamOption`、sub2api 的 `ensureOpenAIChatStreamUsage` 一致。
- `include` 只对返回加密推理的模型加：不推理的模型收到它返回 400 "Encrypted content is not supported with this model"。
- `session_id` 删除：Anthropic 回 "Extra inputs are not permitted"，OpenAI 回 "Unrecognized request argument supplied"。
- `RenderError` 和出错结尾不含 new-api 用来自动停用渠道的关键词（`AutomaticDisableKeywords`），`TestErrorMessages` 检查。
