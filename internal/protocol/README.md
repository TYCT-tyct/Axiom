# protocol

三种调用格式（OpenAI Chat、OpenAI Responses、Anthropic Messages）的读和写。总体规则见[总体架构的「协议转换」](../../docs/architecture.md#协议转换)，对外行为见[接入与对外接口](../../docs/domains/gateway.md)。

目前有读上游 SSE 的 `internal/sse`、检查流结构的 `internal/streamcheck` 和录制样本的命令 `internal/record`；请求摘要、改写、转换、用量按 Issue #27 的计划逐个 PR 加入，加入时改写本文。

## 包

| 包 | 做什么 |
|---|---|
| `internal/sse` | 把上游的 SSE 字节流读成完整事件，逐个严格校验后交出事件的 data |
| `internal/streamcheck` | 按格式检查一路流的结构：事件顺序、把增量挂到部件上的序号、恰好一个结尾；不看内容。`Sample` 按下文「录制样本」的约定检查一个样本的三个文件 |
| `internal/record` | 开发用的命令（`just record`）：向真实上游发一个请求，把流存成样本，见下文「录制」 |

## 读 SSE

规则按 WHATWG《Server-sent events》的解析部分（2026-10-14 读取）：

- 行尾 LF、CRLF、CR 都认；一次读取末尾的 CR 要等下一个字节才知道是不是 CRLF。流开头的 BOM 去掉，别处的不去。
- 空行结束一个事件；多行 `data` 用 LF 连接；冒号后只去掉一个空格；没有冒号的行是值为空的字段。没有 `data` 的事件不算事件。
- 只看 `data`：三种格式都在 data 的 `type` 里写事件类型，有中转站不发 `event` 行，所以 `event` 忽略；上游流断了不续传，`id`、`retry` 也忽略；`:` 开头的注释跳过，不交给调用方。
- 每个事件的 data 必须是 `[DONE]`（`sse.Done`，Chat 的结束标记）或 `jsonx.Validate` 接受的一个 JSON 值，否则返回 `ErrInvalid`。不修复：签名和密文原样搬运，修复会改动签名绑定的文本。`[DONE]` 在这种格式里是否合法，由知道格式的调用方判断。
- 上限由调用方给出，计入一个事件的全部字节：各行、行尾和夹在其中的注释；一直不结束的行同样受它约束。超过返回 `ErrTooLarge`。按事件类别区分的上限，在读出 `type` 之后由调用方判断。
- 流结束时：没有未完成的事件返回 `io.EOF`；已经有 data 的事件没等到空行就结束，返回 `io.ErrUnexpectedEOF`。是否正常结束只看有没有收到格式规定的结尾事件，由调用方判断。
- 出错后每次调用都返回同一个错误。`Next` 返回的切片在下次调用前有效。

## 检查流结构

`streamcheck.Messages`、`Responses`、`Chat` 各读一路流，经 `sse.Reader` 读出事件（单事件上限 32 MiB：Responses 的快照事件回显请求和全部输出，是最大的一类事件，这是给它的封顶值；按事件类别的上限由转发流的代码判断），结构不合规时返回 `ErrInvalid`。规则只取官方文档写明的（2026-10-14 读取）：

| 格式 | 规则 |
|---|---|
| Messages（Anthropic《Streaming messages》） | `message_start` 恰好一次且在最前；内容块一个接一个，`index` 从 0 连续，每块 `content_block_start` → 增量 → `content_block_stop`，增量和结束只能指向正打开的块；已知块类型只收对应的增量（`text`：`text_delta`、`citations_delta`；`thinking`：`thinking_delta`、`signature_delta`；`tool_use`、`server_tool_use`：`input_json_delta`）；块可以没有增量（`fallback` 块就没有）；所有块关闭后才有 `message_delta`，至少一个，之后不再开始新块；`message_stop` 结尾；`ping` 随处可有；`error` 事件结束流 |
| Responses（OpenAI《Responses streaming events》） | 每个事件带 `sequence_number` 且递增；`response.created` 在最前；输出项的 `output_index` 从 0 连续，项内的内容部件（`content_index`）、摘要部件（`summary_index`）同样连续；部件在项之前完成，项在回复之前完成，`output_item.done` 带着所关闭的项的 `id` 和类型；凡是指明了项、部件或 `item_id` 的事件，指向的都必须正打开，只有 `item_id` 的未列出事件按 id 找项；带类型的增量必须指明所属部件，且与所指部件或项的类型一致（如 `output_text.delta` 带 `content_index`、只给 `output_text` 部件，`reasoning_summary_text.delta` 带 `summary_index`、只给 `reasoning` 项的 `summary_text` 摘要部件，`function_call_arguments.delta` 只给 `function_call` 项）；`response.completed`、`response.incomplete` 时全部项已完成，且回复的 `output` 条数等于流里加入的项数；`response.failed` 或 `error` 也结束流 |
| Chat（OpenAI《Chat Completions streaming events》《Create chat completion》） | 每块都是 `chat.completion.chunk`，`id` 全程相同；选项给出 `finish_reason` 后不再有增量；工具调用的序号要么续写已开始的调用，要么恰好开始下一个，开始时带 `id` 和函数名；用量块在全部选项结束后、`choices` 为空，之后只能是 `[DONE]`；`[DONE]` 在全部选项结束后出现一次，是唯一的正常结尾；顶层带 `error` 的块（OpenRouter《Errors》的流中报错）结束流：OpenRouter 发完它就断开，之后至多再有一个 `[DONE]` |

三种格式都：结尾之后再有事件、流在结尾之前结束，都不合规。未列出的事件类型照常接受，只按通用规则检查（Responses 的序号和所指的项、部件）。`[样本]`：`response.incomplete` 时项是否都已完成，官方文档没有写明，按 `response.completed` 的规则检查，等录到达到输出上限的流再确认。

## 录制样本

协议的测试用固定样本回放（[总体架构的「协议转换」](../../docs/architecture.md#协议转换)）。样本放在 `testdata/<上游格式>/<场景>/`，上游格式是 `messages`、`responses`、`chat`，场景名用英文小写和连字符。每个场景三个文件：

| 文件 | 内容 |
|---|---|
| `request.json` | 发给上游的请求体 |
| `upstream.sse` | 上游返回的流。官方样本是原样的字节，`.gitattributes` 让 `*.sse` 不做换行转换，所以 CRLF 的流也原样保存；录制的样本是按事件重建的规范形式，见「录制」 |
| `meta.json` | `source`：官方样本是来源的网址，录制的是 `recorded from a live upstream: POST <官方端点>`，不写上游的地址；`date`：取得的日期（`YYYY-MM-DD`）；`note`：与上游原样不同的地方，没有就不写 |

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
| 结果与上游字节怎么分片无关 | 表驱动用例分别整段读、逐字节读、数据与 EOF 同时到达；读失败与最后的数据同时或分开到达，之前完整的事件都交出；模糊测试按随机位置切分，与一次读完比较全部事件和最终错误 |
| 交出的事件要么是 `[DONE]`，要么是合规的 JSON | `Next` 返回前校验；模糊测试对每个事件再校验一次 |
| 一路流的缓冲受上限约束，不随上限本身预先分配 | 上限计入事件的全部字节；测试用一个永不换行的上游，确认在上限处失败；缓冲从 16 KiB 起按实际事件增长 |
| 读一个长行的时间与行长成正比 | 记住已经扫描过、没有行尾的位置；只有缓冲前部有已消费的字节时才搬移 |
| 每个事件的内存分配次数有常数上界 | 基准测试 1 KiB 和 1 MiB 的事件，CI 与 `main` 对比分配次数 |
| 官方样本都通过检查，每条结构规则都有违反它的流被拒 | `TestSamples`；表驱动的坏流按错误里的说法区分规则，所以去掉任何一条规则，它的用例就失败 |
| 上游给的序号再离谱，检查也只报错 | 序号只与已见到的部件个数比较，从不用来分配或下标越界；模糊测试以全部样本和极端序号为种子 |
| 录制的样本要么完整、通过样本测试、不含 key 和 ID 位置上的原值，要么不存在；ID 位置之外的数据原样保留 | 本地 httptest 上游回放三种格式的已提交样本，分别用 LF、单独 CR、CRLF 行尾，前面加一组评审找到的注释（落单引号、`"input": {`、行末反斜杠），断言写下的流就是样本的事件按规范形式重建、只换了 ID，请求里工具参数的 `input.id` 不动，同一个 ID 在请求里转义写、在流里原样写也对应同一个占位值；每种拒绝一个用例（含评审的注释反例后跟转义写出的 key），断言没有发出请求（发出前的拒绝）、错误里没有 key 且目录为空；重定向、空闲超时各有用例；模糊测试：任意流重建后读回的事件数相同，每个事件除 ID 位置外 JSON 值相同，占位值一一对应，不含 key；每条规则都经过变异验证 |

## 决策与理由

| 决策 | 理由 | 不选 |
|---|---|---|
| 拉取式：调用方循环调用 `Next` | 空闲超时只被真正的数据事件重置（[总体架构](../../docs/architecture.md#协议转换)）：注释在 `Next` 内部跳过，天然不算；`Next` 不认识数据里的事件类型，所以由认识格式的调用方先分出 Messages 的 `ping` 这类心跳，只在真正的数据事件上重置 | 回调或通道：取消和计时分散到两处 |
| 截断的 JSON 报 `ErrInvalid`，不包装 JSON 的原始错误 | 原始错误包装着 `io.ErrUnexpectedEOF`，会被当成流提前结束 | 直接包装：两类错误分不开 |
| 检查接受未列出的事件类型 | Anthropic 的版本策略会不另行通知地增加事件类型，要求客户端容忍；OpenAI 的两种格式同样对待，免得把新版本的接口判成坏上游 | 只认列出的类型 |
| `sequence_number` 只要求递增 | OpenAI 自己的流式示例省略了 5 到 9 号事件，中转站也可能不转发某些事件 | 要求逐一加 1 |
| Chat 的顶层 `error` 块算结尾 | OpenRouter 在已开始输出后用它报错，本站给 Chat 客户端的出错结尾也用这种写法 | 只认 `[DONE]`：把已报告的失败当成截断 |
| 严格校验每个完整事件 | 宽松读入会让原文搬运写出非法 JSON；重复键会让我们读到的用量与客户端读到的不同 | 按 WHATWG 把非法 UTF-8 替换成 U+FFFD：改动签名绑定的文本 |
| 录制命令放在 protocol 的 `internal/record`，不放 `tools/` | 写盘前要用 `streamcheck` 检查，Go 只允许 `internal/protocol` 之下的包引用它；它只用标准库和本模块，`tools/` 单独成模块是为了锁定第三方工具的版本，这里用不上 | 放 `tools/`：只能另起进程跑测试来检查，或者为它把检查提前做成 protocol 的公开接口 |
| 录制命令用自己的 HTTP 客户端，只加上不跟随重定向和空闲超时 | 地址是开发者自己填的，可能是本机或局域网里的中转站，也可能要经代理才连得上 | `platform/egress`：它拒绝回环和内网地址、不读代理环境变量，防的是站长配置的上游地址带来的 SSRF |
| 录下的流按事件解析后重建，ID 只在各格式的 ID 位置上替换，key 在解码后的字符串上查 | 前两轮评审的阻断问题都出自同一个机制：在 SSE 文本上用手写的 JSON 字符串扫描器找 ID 和 key，单独 CR 的行尾、注释里的落单引号或行末反斜杠、像 `"input": {` 的注释行都能让它错位。改用已有的 `sse.Reader`（按 SSE 规则分行、跳过注释）和经 `jsonx.Validate` 的 JSON 后，这一类错位不再存在；找字符串的位置用 `jsontext`，因为 `jsonx` 不提供按 token 读的接口。按位置替换，漏列一个位置只会留下一个真实 ID，录制者提交前看得到；按成员名猜，会改掉工具参数这类调用方的数据，改错了没有检查能发现。数据的字节除 ID 外原样拼回，空白、键序、数字写法照旧可测；SSE 的分帧（注释、行尾、`id`、`retry`）不保留，读流的代码本来不看它们，要测分帧的用官方样本，它们仍是原样的字节 | 在原始字节上扫描（前一版）：两轮评审各找出新的错位反例，补一处又出一处；解码后重新编码整个 JSON：改变键序和数字写法；按 `id`、`*_id` 成员名猜 ID 再加一张跳过调用方数据的列表：列表外的工具参数照样被改；在没列出的位置发现像 ID 的值时提醒：现在三种格式的位置都已列出，等录到新形状的上游时再看是否需要 |
| 两个文件共用一张替换表，并替换请求里的 ID | 多轮请求里的工具调用 ID、`previous_response_id` 会出现在流里，替换后两边仍对得上 | 只替换流：请求里留下真实 ID，两边也对不上 |
