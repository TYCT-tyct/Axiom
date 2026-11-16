# protocol

生成格式 Chat、Responses、Messages、旧版补全 `Completions` 和直接格式 Embeddings、Rerank 的读写。三种 Chat 形态的生成格式两两转换；旧版补全和直接格式只做同格式改写，转换返回 `*NotSupported`。总体规则见[总体架构的「协议转换」](../../docs/architecture.md#协议转换)，调用方看到的行为见[接入的「转发规则」](../../docs/domains/gateway.md#转发规则)，用法见 [gateway](../gateway/README.md)。

## 包

| 包 | 做什么 |
|---|---|
| `protocol` | `Inspect` 读摘要；`Prepare`、`PrepareBudgeted` 改写或转换请求；`Relay` 转发、计量或聚合；`RenderError` 写流开始前的错误；直接格式的 `DirectUsage`、`ValidateDirectReply`、`RewriteDirectReply`；compact 的 `UsageOf`、`ValidateCompactReply`、`RewriteCompactReply` |
| `internal/sse` | 把上游字节读成严格校验过的事件 |
| `internal/streamcheck` | 按格式检查流结构；`Sample` 检查样本 |
| `internal/record` | `just record`，见「录制样本」 |

## 请求摘要

`Inspect(f, body)` 按 token 一次严格扫描（UTF-8、重复键、`jsonx.MaxDepth`、值后无数据），返回不借用 `body` 的 `Request`，附定长 20 项的顶层位置表；分配次数与请求体大小无关（宽对象查重除外）。

- **只按位置读**：只沿 `content`、`output`（及 `document` 的 `source.content`）进入部件，单个部件按值的形状认；工具 schema 和参数从不进入，其中的同名键不改变摘要。
- **字段**：`Models` 多于 8 个无效；`MaxOutput` 0 为没给，Chat 取两个上限中较大的；`TextBytes` 是请求体字节减去图片、文件数据，宁多不少；`cache_control` 加 `cache_write`，`ttl: 1h` 再加 `cache_write_1h`；`ServiceTiers` 不写或 `auto` 含 priority，`speed: "fast"` 为 fast；按次工具上限是 Messages 各 `max_uses` 之和、Responses 的 `max_tool_calls`；`Provider` 原样，至多 4 KiB；`Needs` 里关闭优先于强度，空设置不要求推理。
- **`ErrInvalid`**（400）：不是 jsonx 接受的对象、缺 `model`、类型不对、计数为负或溢出、`service_tier`、`speed` 不认识（定不了价）。说明用我们的话，类型错用 JSON 指针定位（`bodyFault`）。
- **服务端工具**：没有完整报价的点名拒绝，`openrouter:*` 只收 `web_search`（`parseServerParams`），名单见[计费的决策表](../../docs/domains/billing.md#决策与理由)。Responses 的 `shell` 只在 `environment.type:"local"`、`tool_search` 只在 `execution:"client"` 时算客户端工具。循环的捕获逐 token 读，读事件前按 `growthBound`、写回合前按 `turnBound` 记账。
- **插件**：校验成 `Compression`、`ResponseHealing`、`Web`、`File`，不支持的为 `ExtensionError`（属 `ErrInvalid`）；规则和上限见[接入的「插件与提示词变换」](../../docs/domains/gateway.md#插件与提示词变换)，常量在 `websearch.go`、`fileparse.go`。`RewriteFiles` 只换 `content`、同一文件只解析一次；`CompressContext` 先按 `CompressionCost` 记账，之后重新 `Inspect`。
- **预设与标识**：`MergePreset` 顶层浅合并、保留原字节，之后必须重新 `Inspect`；`fallbacks` 至多 3 项、与 `models` 互斥；`session_id` 至多 256 字符；`EndUser` 先取 `user` 再取 `safety_identifier`，Messages 只取 `metadata.user_id`。
- `RequestParameters` 是 `provider.require_parameters` 时的第二次扫描，新参数登记进 `parameterNames`、`capabilityParameters` 或 `capabilityParameter`。`Text` 给缺输入用量的暂估，只回调解码后的字符串，跳过不透明的 `data`、`signature` 等。
- **直接格式与旧版补全**走 `inspectNew`；直接格式拒绝 `stream: true`。旧版补全的 `Choices` 是 `n` 乘 prompt 数，`MaxInputBytes` 是最长的 prompt 加 `suffix`；`best_of > 1` 拒绝（不能流式）；`max_tokens` 缺、`null` 或超过 `Target.MaxOutput` 时发后者，显式 0 由 `MaxOutputSet` 区分；回显不计入内容字节（`EchoOf`）。

## 同格式改写

`Prepare(r, body, t, w)` 复用位置表、只解析要改的值，其余字节（含空白、签名、密文、压缩项）原样。表记着请求体长度，不符就退回 tokenizer 改写；同长异内容防不住，所以改过正文必须重新 `Inspect`。

- `model` 换 `Target.Model`；`provider`、`models`、`preset`、`fallbacks`、`route`、`transforms`、`plugins`、`usage`、`session_id`、`axiom` 删除。
- `stream`：`Target.Streams` 时为 `true`，否则原样。
- `metadata.user_id`、`prompt_cache_key`、`user`、`safety_identifier` 换成 `Rewrites.Identity` 的派生值，`null` 不动；`metadata.user_id` 是 `{"device_id","account_uuid":"","session_id"}` 的 JSON 字符串，空串或纯空白写空串。
- Responses：删 `previous_response_id`，把 `Rewrites.History` 按序插到 `input` 前；`store: false`；`Target.EncryptedReasoning` 为 yes 时 `include` 含 `reasoning.encrypted_content`。
- 输出上限缺、`null`、0、`-0` 时写 `Target.MaxOutput`（大于 0 才写），显式正数不动。Chat 把两个上限都拿掉，最小正数写进端点读的那个（`usesCompletionCap` 按上游模型名定），流式时 `stream_options.include_usage` 强制为 `true`，旧版函数调用先升级（见下节）。
- 其余原样，包括模型方言成员和准入读过的 `max_uses`、`cache_control`、`service_tier`：发出的必须是报价时读到的那份。
- 回放清理：Messages 删无签名或空签名的 thinking 块和因此变空的助手轮次，Responses 只删本站生成的 `UUID_序号` 项 id，之后改走 tokenizer。压缩请求（`Target.Compact`）只改模型、标识、路由参数。
- 标识不是字符串、`metadata` 不是对象、`include` 或有历史时的 `input` 形状不对、流式时 `stream_options` 不是对象为 `ErrInvalid`；历史与 `previous_response_id` 不一致是 gateway 的错，不回 400。

**内存不变量**：`PrepareBudgeted(m, …)` 分配的每个字节都先向 `m` 申请过；申请不到就退回全部、返回 `ErrMemory`，不再分配；成功只留 `Upstream.Body`。同格式按 `rewriteSize` 一次分配（标识按 `Rewrites.IdentityLen`，缺省 128，按 6 倍转义）；转换的解码和 `writer` 增长前申请，原生读成 Chat 由 `nativeChatWithin` 按 `nativeRequestCost` 整段预留。`TestPrepareAllocatesOnlyWhatItDrew` 证明分配不超过申请加 32 KiB。并发时首试共享 `Memory.preparing` 读闸，只有在途准备的额度能覆盖已知峰值才独占写闸重试一次，否则立即 `ErrMemory`；剩余额度小于请求体时进闸前拒绝；等闸不可取消。

## 跨格式转换

Chat 与 Messages、Responses 双向各一套映射；Messages 与 Responses 之间由 `convertNative` 先读成 Chat 形状再写出，所以只有一套映射。逐字段映射见代码和 `TestConvertToMessages` 等测试，这里只写看不出的规则。

- **两类错误**：形状错（名字、`text`、`arguments` 不是字符串，`detail` 不是 `auto`、`low`、`high`）为 `ErrInvalid`；目标表达不了（`canon` 没读的成员取非默认值、非函数工具、调用配不上）为 `*NotSupported`，gateway 换端点。不删约束凑合。
- **规格没说不是 no**：`Target` 的 `Thinking`、`MidSystem`、`UntrustedSystemAfterTools`、`DefaultSamplingOnly`、`ForcedToolChoice` 等由 gateway 从 `catalog.Dialect` 填，零值是未设置；请求用到时未设置与否定一样 `*NotSupported`，`Reason` 给客户端，缺的键放 `Detail` 只进日志。只添东西的 `include`、缓存断点未设置按 no。
- 控制字段按解码值比较，正文、签名、工具参数保留原字节。
- **工具调用 ID**：Anthropic 只收 `^[a-zA-Z0-9_-]+$`；合规且不以 `axid_` 开头的原样，其余写成 `axid_` 加原字节的十六进制，两类不相交。
- **旧版函数调用**（`legacy.go`）：`functions`、`function_call` 升级成 `tools`、`tool_choice`，旧调用分到 `call_legacy_<n>`，结果配同名中最早未配的调用；冲突为 `ErrInvalid`，配不上为 `NotSupported`。只用旧表示的客户端（`UsageContext.LegacyFunctions`）收回 `function_call` 形状。
- **推理回放**：`reasoning_details` 先按类型和 `index` 合并（`mergeDetails`）；到 Messages 只回放带签名的 `anthropic-claude-v1`，到 Responses 只回放带密文的 `openai-responses-v1`。
- **结构化输出子集**（Messages）：无数值、字符串约束，`minItems` 只 0 或 1，对象都 `additionalProperties: false`，`$ref` 只指本 schema、不递归；否则 `*NotSupported`。
- **Messages 与 Responses 之间**只有三处不同：别家的签名或密文不带；Messages 客户端收不回密文，不要 `include`；`file_id` 到 Messages 为 `*NotSupported`。
- 第 n 轮的转换结果是第 n+1 轮的前缀（`TestConvertPrefix`）。

### 推理控制与换算

启用、强度、展示分开：未指定或空 effort 什么都不加，`none` 是关闭且优先于强度；`exclude` 只隐藏。隐藏在计量之后过滤（`UsageContext.HidesReasoning`），用量不减，签名和密文保留。同格式不改写强度。

| 输入 | Messages manual | Messages adaptive | Responses |
|---|---|---|---|
| enabled true | medium 的预算 | adaptive + medium | medium |
| enabled false、`none` | disabled | disabled | `none` |
| minimal…max | 上限的 10/20/50/80/95/95% | minimal 写 low | 原值 |
| `max_tokens` 预算 | `budget_tokens` 原值 | 最近档 | 最近档 |
| exclude true/false | 已开启时 display omitted/summarized | 同左 | 不要摘要 / summary auto |

手动预算 = `max(min(上限 × 比例, 128000), 1024)`；显式预算须为正，manual 目标还须 ≥1024 且小于输出上限。预算换档按与有效上限（请求的，否则 `Target.MaxOutput`，都没有则 `NotSupported`）之比取最近档，同距取低。反向到 Chat 用同样比例；有非空 `output_config.effort` 时以它为准。

## 读 SSE

`sse.Reader` 按 WHATWG 的解析规则：

- LF、CRLF、CR 都是行尾；读失败或结束时末尾的 CR 当行尾，之前收齐的事件先交出。开头 BOM 去掉。
- 只看 `data`。事件必须是 `sse.Done` 或 jsonx 接受的一个值，否则 `ErrInvalid`；不修复，修复会改签名绑定的文本。
- 单事件上限计入全部字节，超出 `ErrTooLarge`；事件没结束就断为 `io.ErrUnexpectedEOF`；出错后一直返回同一错误。
- 单行 data 直接返回缓冲里的一段（容量等于长度），有效到下次调用。
- `Poll` 至多读一次（16 KiB）。缓冲增长前经 `ChargeGrowth` 申请；超过 64 KiB 的缓冲在连续 16 个事件都用不到四分之一时缩小并归还。
- 解码器只在事件至多 2 KiB、深度至多 8 时留着，留存有界；稳态读事件不分配（`TestReaderAllocs`）。

## 检查流结构

`streamcheck.Messages`、`Responses`、`Chat` 按官方文档检查结构（单事件 32 MiB），违反为 `ErrInvalid`。Messages：`message_start` 一次在最前，块 `index` 连续、start 到 stop 之间只有匹配的增量，块都关闭后才有 `message_delta`。Responses：`sequence_number` 递增（不要求连续），项和部件序号连续、所指的正打开，结尾时 `output` 条数等于加入的项数。Chat：`id` 不变，`finish_reason` 后无增量，工具调用续写或恰好开始下一个，用量块在选项都结束后，`[DONE]` 是唯一正常结尾，顶层 `error` 块结束流。结尾后还有事件、结尾前结束都不合规；未列出的事件类型照常接受，厂商会不通知地加类型。

## 转发

`NewRelay` 拉取式、单 goroutine：`Next` 每做一次至多 16 KiB 的读就返回 `Step`（`StepBuffered`、`StepCommit`、`StepData`、`StepProgress`、`StepHeartbeat`、`StepUpstreamDone`）；`Progress()` 不分配，标明哪些已报告的数是最终值；`Usage()` 只把最终值算已报告；`Finish`、`Fail` 只第一次写结尾；`Release` 幂等。切片有效到下次调用。字段见 `relayoptions.go`、`relayend.go`。

- **提交点**是第一个内容事件，之前的暂存，提交前的 `ping` 丢弃。Messages 较早的 `message_delta` 扣住；Chat 的 `finish_reason` 从块里拆出、换成 `null`，扣到 `[DONE]`。
- **结尾**：带 `stop_reason` 的 `message_delta`、`response.completed`/`incomplete`、Chat 用量块（本站按结算写，带一个空内容的选项）；`Finish` 带上费用。
- **出错帧**先校验并吸收本帧 `usage`，再立即 `ErrUpstream`；`RejectionOf` 留下解码后的四项，每项超过 2 KiB 整项省略。Responses 正常事件只用 `response.usage`，失败事件缺它才用顶层的。
- **改写**：`id` 换调用编号，`model` 换 `ActualModel`；`usage` 只留官方成员再追加费用，删中转站的 `cost` 和上游的 `axiom`；`Cached` 时用量数字清零。
- **用量**按 [catalog 的「计费项与用量」](../../docs/domains/catalog.md#计费项与用量)折算：计数须在 0 到 10¹⁰；Messages 按项取最新，0 不覆盖正数，变小判无效；OpenAI 每侧带明细或总量变大才整体替换，总量不下降；DeepSeek 命中数只在 `cached_tokens` 缺或为 0 时算缓存读；映射外的非零计数进 `Unknown()`。无效不中断流，写进 `Usage().Invalid`，按估算。
- **字节上界**：正文、推理、工具参数、思考签名和加密推理按解码后的字节，`reasoning_details` 和工具 `input` 按原文字节；`output_item.done` 只加比增量多出的；`web_search_call` 计一次搜索，`file_search_call`、`code_interpreter_call` 各一次调用。
- **错误**：断流、不合规、出错事件包装 `ErrUpstream`，`FaultOf` 给类别；之后 `Next` 一直返回同一错误，正常结束后为 `ErrTerminated`。出错结尾带用量，结算提交了才带费用。外壳见[接入的「错误」](../../docs/domains/gateway.md#错误)。
- **内存**：跨事件的缓冲按容量在增长前记账，前 64 KiB（原生两格式之间加 `nativeConversionMemory`）是基础额度，超出向 `RelayOptions.Memory` 申请。计量器先记 16 KiB，未知用量字段每段至多 16 个、名和值各 128 字节，压缩段至多 8 个，超出判无效。单事件上限 Responses 32 MiB，其余 1 MiB。

## 跨格式的流

上游格式的读取器照旧计量、判错，转换器再读同一事件写出客户端格式；原生两格式之间经 Chat 组合。

| 上游结束 | `finish_reason` |
|---|---|
| `end_turn`、`stop_sequence`；`completed` 无函数调用 | `stop` |
| `tool_use`；`completed` 有函数调用 | `tool_calls` |
| `max_tokens`、`model_context_window_exceeded`；`incomplete` 因 `max_output_tokens` | `length` |
| `refusal`；`incomplete` 因 `content_filter` | `content_filter` |
| `pause_turn`、不认识的、缺停止原因 | `ErrUpstream` |

- Chat 客户端的推理同时写 `reasoning` 和 `reasoning_content`，另带 `reasoning_details`。只有排在所有非思考块之前的思考块给出签名：Chat 只能把推理回放在最前，挪了位置的思考块会被拒。
- 服务端工具、引用、不认识的块、缺 ID 或名字的工具调用、结束后的内容为 `ErrUpstream`，不静默丢。
- Responses 快照按部件比对已发文本的长度和 SHA-256：相同忽略，已发是前缀就补余下，没发过就整段发，其他 `ErrUpstream`。

## 非流式聚合

`RelayOptions.Aggregate` 时上游照样流式读，`Finish`、`Fail` 返回整个响应体，`Status()` 给状态。聚合器吃 `Relay` 写出的客户端格式事件，一份服务所有路径。Responses 成功时原样取结尾快照（官方样本的增量拼不出最终文本），失败时用记下的项重建，未完成的写 `incomplete`。

- 失败：提交点之前是 `RenderError` 的答案（可换端点）；之后 Chat、Completions、Responses 回带内容和错误的 200，Messages 回不带内容的 502，它没有表示出错的结束原因。
- **容量**：片段合计至多 `aggregateLimit`（16 MiB，按容量），超出是 `ErrMemory`，不是上游错误。节点预算：拷成响应体的片段两倍，`reasoning_details` 四倍，带引用的文本三倍加 64 KiB，响应修复 20 倍。`Finish`、`Fail` 不申请，预算留到网关写完响应体后 `Release`。
- `UsageOf` 与流的结尾同一口径；`RewriteCompactReply` 只换顶层 `usage`。

## 录制样本

样本在 `testdata/<上游格式>/<场景>/`：`request.json`、`upstream.sse`（不做换行转换）、`meta.json`（`source`、`date`、`note`）；录制的 Chat 场景必须有 `usage.json`（`TestChatUpstreamSamples` 核对）。黄金输出由 `go test ./internal/protocol -update` 重写，PR 里审差异。直接格式用 `response.json` 代替流（`TestDirectSamples`）。`testdata/assembled` 是组装的流，只给转换测试。

- 只收官方文档的完整流和合成提示词录的真实上游，不含密钥、地址、用户数据；`streamcheck.Sample` 在 `TestSamples` 和写盘前检查。
- `just record <格式> <场景> <请求文件>`：地址和 key 取环境变量或 `.env`。名字不合规、目录已存在、缺地址或 key、请求不是严格 JSON、没有 `"stream": true` 或含 key 时不发出。
- 不跟随重定向；每段等待 10 分钟无进展即失败；流超过 64 MiB 不写；错误里的 key 换成 `[REDACTED]`；失败不留目录。
- 流按事件重建成规范形式，数据除 ID 外逐字节保留。ID 只在 `formats` 的 `ids` 所列 JSON Pointer 上换成前缀加序号，请求和流共用一张表。key（配置的原文或词首 `sk-` 加 20 个以上字符）在解码后的字符串和原始字节上都查。

## 决策与理由

| 决策 | 理由 |
|---|---|
| 摘要一次扫描：直接用 `jsontext` 读 token | 请求体可达数 MB，校验后再解析要扫两遍 |
| `Relay` 拉取式、单调用方 | 空闲超时只被数据事件重置，心跳由懂格式的调用方分出 |
| 结尾保留官方用量成员、只追加费用 | 客户端看得到计费外的明细，白名单挡住中转站的成本 |
| Chat 只拆 `finish_reason`、不扣整块 | 扣整块会打乱交错选项的顺序 |
| 转换器持有上游格式的读取器 | 计量和判错只有一份规则 |
| 旧版补全、直接格式不转换 | 没有对应写法，转换要么丢信息要么发明语义 |
| `session_id` 删除，不派生转发 | 官方端点拒绝不认识的成员 |
| 录制按事件重建、按位置换 ID | 在原始字节上扫描屡次错位，按名字猜会改调用方数据 |
