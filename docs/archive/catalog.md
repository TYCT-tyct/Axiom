# 模型目录的调研存档

[模型目录](../domains/catalog.md)和[目录模块](../../internal/catalog/README.md)背后的调研、条款取证和一次性运行记录。这里是当时读到的事实，不是现行规则；规则以那两份文档为准。

## 请求写法的键的出处

`deploy/catalog/dialects.json` 里每个值的依据，读于 2026-10-19。

**Anthropic**：`mid_system` 为 yes 的是厂商文档列出的那几个型号，没列的为 no；`untrusted_system_after_tools` 只有 Sonnet 5.5、Haiku 5.5 为 yes。

- `thinking`：Anthropic《Thinking》「Configuring thinking」的各型号表（<https://platform.claude.com/docs/en/build-with-claude/thinking>，2026-10-19）：Opus 4.6 起的每个型号接受 `adaptive`（Opus 4.6、Sonnet 4.6 也接受已弃用的手动预算，取 adaptive）；Opus 4.5、Sonnet 4.5、Haiku 4.5 对 `adaptive` 返回 400，只接受 `enabled` 加 `budget_tokens`，即 manual；Opus 4.7 起手动预算返回 400。与 new-api `relaykit/relayconvert/reasoning/claude.go` 对过：4.6 起 adaptive，4.5 只有手动。
- `mid_system`：Anthropic《Mid-conversation system messages》（<https://platform.claude.com/docs/en/build-with-claude/mid-conversation-system-messages>，2026-10-19）：「available on Claude Fable 5.1, Mythos 5.1, Fable 5, Mythos 5, Opus 5.5, Opus 4.8, Opus 5, Sonnet 5.5, and Haiku 5.5 … not available on Claude Sonnet 5」；列表之外的型号为 no 是据这份列表推出，文档没有逐个写出。`untrusted_system_after_tools`：同页「On Claude Sonnet 5.5 and Claude Haiku 5.5 … can treat a system message placed right after a tool result as untrusted text」。
- `default_sampling_only`：Anthropic《Claude Opus 5.5 migration guide》（<https://platform.claude.com/docs/en/models/opus-5-5/migration-guide>）：非默认的 `temperature`、`top_p`、`top_k`「on Claude Opus 4.7 and later models … returns a 400 error」，Sonnet 5 同样（同文「Migrating … from Claude Sonnet 5」）；Sonnet 5.5 迁移指南：「Sonnet 4.6 and earlier models and Claude Haiku 4.5 accept …, on Claude Sonnet 5.5 a non-default value returns a 400 error」；Haiku 5.5 迁移指南「Remove sampling parameters」。Fable 5、5.1 没有页面单独写这条，按 Fable 5.1 概览「same patterns as Claude Opus 5」归为 yes（过度拒绝的一侧，不会把上游拒绝的请求发出去），拿到实测样本后再确认。
- `forced_tool_choice`：只有 Opus 5.5、Sonnet 5.5、Fable 5.1 拒绝（《Thinking》「Thinking with tool use」：adaptive 思考支持强制调用工具，「except on Claude Opus 5.5, Claude Sonnet 5.5, Claude Fable 5.1, and Claude Mythos 5.1」；各自的迁移指南「Forced tool use is not supported」）；Haiku 5.5 迁移指南明说接受。思考时 `protocol` 无论如何拒绝强制调用，这里只说不思考时。

**OpenAI**：

- `encrypted_reasoning`：OpenAI《Reasoning》（<https://developers.openai.com/api/docs/guides/reasoning>，2026-10-19）：`store` 为 `false` 或零数据保留时，推理项「include an `encrypted_content` property by default」，页内示例用 `gpt-6-astra`、`gpt-6.1-sol`；文档没有逐个型号列出支持者，上表是它举例或点名的推理模型，GPT-5.5 及更早的没有证据，不在文件里，check 会报，由人对着文档补。
- `cache_breakpoints`：OpenAI《Prompt caching》（<https://developers.openai.com/api/docs/guides/prompt-caching>，2026-10-19）：显式断点「GPT-5.6 and later」支持，「GPT-5.5 and GPT-5.5 Pro」和更早的不支持（用 `prompt_cache_breakpoint: {"mode": "explicit"}`）。

## 服务端工具次数

服务端工具次数采用可选的规格数据，不给所有模型批量补常量：2026-10-28 查阅 [OpenRouter 服务端工具循环](https://openrouter.ai/docs/guides/features/server-tools#tool-call-limits)，共享 `max_tool_calls` 默认及最大均为 30；这可作为确实经过该循环的端点的默认依据（为每项预留 30 次会偏多）。[Anthropic web fetch](https://platform.claude.com/docs/en/agents-and-tools/tool-use/web-fetch-tool) 明确没有默认上限，[code execution](https://platform.claude.com/docs/en/agents-and-tools/tool-use/code-execution-tool) 文档也未给出调用次数默认上限，因此不能把 OpenRouter 的 30 自动套到原生工具。管理员必须在规格更新理由中记录端点依据和日期。未采用全局硬编码默认：无法由管理员修正，且会把没有上界的上游错误声明为有界。

## 计价事实

- Anthropic Haiku 5.5 的分档原文："prompt length counts all of its input tokens, including cache reads and cache writes"；Claude 4.6 及以后的其他模型 1M 内统一价（Anthropic Pricing 页，2026-10-14）。Gemini 提示词超过 200k 换档，输出和缓存价同时换（Gemini 定价页，2026-10-14）。
- Gemini 2.5 Flash 的音频输入每百万 $1.00、文本 $0.30；Gemini 3.6 Flash 的图片和视频每百万 $1.00、文本 $0.75（Gemini 定价页，2026-10-14）。Anthropic 联网搜索每千次 $10，带回的内容按普通 token 计入输入（2026-10-14）。
- OpenAI `file_search` 每千次 $2.50（2026-11-02）。OpenRouter 的 File Parser：`mistral-ocr` 每千页 $2（美国区 $2.2，含 Mistral 10% 区域加价），`cloudflare-ai` 免费，`native` 按输入 token（2026-11-02）；本站不提供区域选择，$2 / 1,000 页可作管理员定价的参考。
- `video_tokens` 并入图片量的依据：OpenRouter《OpenAPI》`ChatUsage.prompt_tokens_details.video_tokens`「Video input tokens」（2026-10-19）。Gemini 的 `promptTokenCount` 包含缓存内容（Gemini API《UsageMetadata》）。Anthropic 的 `cache_creation_input_tokens` 等于 `cache_creation` 各项之和（Anthropic《Prompt caching》）。
- new-api 的 `tier("request", fixed(...))` 和 sub2api 的 `per_request_price` 都按请求计价（2026-11-01 只读核对）。
- 2026-10-29 OpenRouter 的真实数据里，Gemini 的 `image`、`image_output` 等于它的 image token 价（`image` 与 `prompt` 同价），所以不读。LiteLLM 的 `deepseek-chat` 与 `deepseek/deepseek-chat` 只差一个值为 0 的缓存写，这是「取说出其余所有条目所说的那一个」的来由。

## 后台任务角色与 `lock_models`（设计，未实现）

- 后台任务以 `axiom_catalog_job` 角色执行：由本模块的迁移建立，只授予它要写的表：平台数据表，以及自动发布要写的 `reference_price`、`revision`；任务的事务用 `pg.Tx.SetRole` 切过去。
- 模型行只经 `catalog.lock_models(ids uuid[])` 加锁（设计，随后台任务角色一起实现）：`SECURITY DEFINER`，属主 `axiom_owner`，固定 `search_path`，按 id 升序 `SELECT … FOR UPDATE` 并返回 `price_source`、`rev`；只把 EXECUTE 授予 `axiom_app` 和 `axiom_catalog_job`。它是运行时唯一以 `axiom_owner` 身份执行的代码，只加锁、不写。PostgreSQL 的行锁要求对该表至少一列有 UPDATE 权限（PostgreSQL 文档《SELECT》），任务角色没有 `model` 的 UPDATE 权限，也不该有：给它任何一列都等于允许它改这一列。表强制启用行级安全，所以函数里的查询仍受范围约束。测试：切到任务角色后自动发布成功；直接 `SELECT … FOR UPDATE` 模型行被拒。

`lock_models` 要求 `SECURITY DEFINER` 函数属 `axiom_owner`，与[平台的启动检查](../../internal/platform/README.md#迁移)（只许 `axiom_lookup` 拥有）冲突，建它时一并改检查。数据集审核项的 `subject_key` 设计为 `page:<page_id>`、`price:<model_id>:<currency>`、`mismatch:<model_id>:<dataset>:<currency>`、`new:<dataset>:<external_id>`，处理时绑定 `content_seq`。

## 官方价格页的条款取证

2026-11-07 的条款取证（只读条文与 robots.txt，不作法律意见；"读取日"是取得该页面文本的日期，取自读取工具的获取时间）。对象是每小时一次、只取登记的公开价格页、遵守 robots.txt、固定 UA、不登录的抓取（见上文「取数」）：

| 厂商 | 条文（原文，读取日） | robots.txt（读取日 2026-11-07） | 结论 | 抓取器怎么办 |
|---|---|---|---|---|
| OpenAI | [Terms of Use](https://openai.com/policies/row-terms-of-use/)「Using our Services」，读取日 2026-10-22：“Automatically or programmatically extract data or Output”（列在不得做的事里）；价格页本身无单独的网站条款 | `openai.com/robots.txt`：`User-agent: *` `Allow: /` `Disallow: /microsoft-for-startups/`；`/api/pricing/` 允许 | 条款禁止“自动提取数据”，读不出它是否只指服务内的数据；负责人 2026-11-08 决定抓取（见下）。`openai.com/api/pricing/` 在 2026-10-31 和 11-08 都回 403 | 启用，登记 `developers.openai.com/api/docs/pricing`（OpenAI 的 API 文档站价格页，同样的价目，11-08 回 200，`developers.openai.com/robots.txt` 为 `User-agent: *` `Allow: /`）；不换 UA 去绕 `openai.com` 的 403 |
| Anthropic | [Consumer Terms](https://www.anthropic.com/legal/consumer-terms) §3，读取日 2026-11-07：禁止“crawl, scrape, or otherwise harvest data or information from our Services other than as permitted”，以及除通过 Anthropic API key 或明确许可外“access the Services through automated or non-human means, whether through a bot, script, or otherwise”（读到的是该页韩文版，英文措辞据其对应条目转述，英文原文待核）；[Commercial Terms](https://www.anthropic.com/legal/commercial-terms)（读取日 2026-11-06）未见对公开文档页的抓取条款；未找到专门覆盖 `platform.claude.com` 文档站的网站条款 | `platform.claude.com/robots.txt`：`User-Agent: *` `Disallow: /api/`；`/docs/en/about-claude/pricing` 允许 | 消费者条款的适用范围（是否含公开文档页）读不出，商业条款没有对应禁令；负责人 2026-11-08 决定抓取 | 启用 |
| Google（Gemini API） | [Google Terms](https://policies.google.com/terms)「Don't abuse our services」，读取日 2026-11-07：“using automated means to access content from any of our services in violation of the machine-readable instructions on our web pages (for example, robots.txt files that disallow crawling, training, or other activities)”；[Gemini API Additional Terms](https://ai.google.dev/gemini-api/terms)（读取日 2026-11-02）的抓取限制只针对 Grounding 与 Maps 数据，不涉及文档页；文档内容按 [Site Policies](https://developers.google.com/site-policies)（读取日 2026-10-14）以 CC BY 4.0 授权 | `ai.google.dev/robots.txt`：`User-agent: *` `Disallow: /*?*authuser=`；`/gemini-api/docs/pricing` 允许 | 有条件允许：只禁违反 robots 的自动访问，价格页未被禁止。条件：每次先读 robots 并照做；不带 `authuser` 参数；固定 UA；间隔不早于 robots 许可；robots 变得禁止时停取 | 条件已由抓取器的不变量满足；仍按「人工确认后才登记」由负责人登记 |
| DeepSeek | [Terms of Use](https://cdn.deepseek.com/policies/en-US/deepseek-terms-of-use.html) §3.5(3)，读取日 2026-11-06：“Engage in activities to steal network data, such as: … capturing, copying any content of the Services, including but not limited to using any robots, spiders, or other automatic setups, setting mirrors.” | `www.deepseek.com/robots.txt`：`Allow: /`；`api-docs.deepseek.com/robots.txt` 返回的是文档首页 HTML，没有任何指令（读取日 2026-10-28） | 条文字面上禁止用 robots、spiders 等自动方式“复制服务内容”，但它列在“窃取网络数据”之下，读不出是否含公开价格文档；站点本身没有 robots 指令；负责人 2026-11-08 决定抓取 | 启用，USD 与 CNY 两页 |

四家都没有明文允许每小时抓价格页；Google 是唯一条文把限制写成“违反 robots”的，其余三家的字面禁令是否覆盖公开价格页属法律解释，负责人 2026-11-08 决定四家都抓（[#309 评论](https://github.com/TYCT-tyct/Axiom/issues/309#issuecomment-6715695418)第 3 项）。抓取器没有改，`deploy/catalog/price-pages.json` 去掉了三家的排除；管理员仍用 `watch-page` 登记，页面被站方拦截（如 403）时按既有规则保留旧快照并告警，不换 UA 绕过。

清单在 `deploy/catalog/price-pages.json`，是 live 验收输入，也是管理员登记的依据，不自动启用；2026-11-08 起四家的页面都不再带 `excluded`，OpenAI 改用 `developers.openai.com/api/docs/pricing`（正文标记“Flagship models”到“Cloud platforms”），Anthropic 的标记是“The following table shows pricing for all Claude models:”到“Private offers”；当日对五个页面各跑 `check-pages`，OpenAI（sha256 `eac73c17…`）、Anthropic（`f278aed1…`）、DeepSeek USD（`752d5c0c…`）、DeepSeek CNY（`7a04e595…`）抽取成功。同日 Google 的 `ai.google.dev/gemini-api/docs/pricing` 回 301 到 `aistudio.google.com/docs/pricing`，抓取器不跟重定向，该页要换登记，另行处理；页面 GET 的结果、hash、失败与重试原因留在本文的 2026-10-31 运行一节和 PR。完整四家逐模型验收仍须真实目录数据，检验上架、名字、每币种当前价与来源、分档、证据和未处理核对；没有真实目录连接时如实报告未验证。

## 2026-10-31 的四家 live 运行

本次在一台开发服务器上对四家的五个公开价格页各跑一次 `check-pages`（DeepSeek 的 USD 与 CNY 分页；每个价格页一次 GET，robots 由工具按同源读取），并用同一 UA 另存每个页面的原始响应，供修复后的抽取器复算 hash；没有重试。统一出站、robots、4 分钟期限和 32 MiB 上限不变。运行用的清单是临时文件，当时清单里三家仍带排除，这是一次诊断性读取，不启用定时监测；2026-11-08 负责人决定后的状态见上文。四个 USD 页的主运行以非零退出，唯一失败是 OpenAI 的 403；CNY 页另跑一次，退出 0。

| 厂商 | URL | robots.txt | 页面 | 抽取正文 |
|---|---|---|---|---|
| OpenAI | `https://openai.com/api/pricing/` | 200，`Allow: /`（另禁 `/microsoft-for-startups/`） | **403**（拦截页 9,662 B） | 无 |
| Anthropic | `https://platform.claude.com/docs/en/about-claude/pricing` | 200，`Disallow: /api/` | 200，1,307,125 B | 2,386 B，sha256 `5a3c20c9…` |
| Google | `https://ai.google.dev/gemini-api/docs/pricing` | 200，`Disallow: /*?*authuser=` | 200，261,792 B | 57,627 B，sha256 `25100a50…` |
| DeepSeek USD | `https://api-docs.deepseek.com/quick_start/pricing/` | 200，但返回文档首页 HTML（`text/html`，无 robots 指令） | 200，27,815 B | 1,636 B，sha256 `752d5c0c…` |
| DeepSeek CNY | `https://api-docs.deepseek.com/zh-cn/quick_start/pricing/` | 同站，同上 | 200，27,987 B | 1,670 B，sha256 `7a04e595…` |

运行时保存的原始响应 sha256：OpenAI `7fec3897…`、Anthropic `9de88a38…`、Google `41919361…`、DeepSeek USD `57941235…`、DeepSeek CNY `ed99e49b…`。抽取正文的 hash 是最终抽取器对同一份保存的响应重算的；修复前的运行 hash 见 PR。

USD 表用 USD / 百万 token，DeepSeek CNY 表用人民币 / 百万 token；“读”是缓存命中，“写 5m”“写 1h”是缓存写入的两种保留时间；“—”表示页面没有该项。

**Anthropic**（`platform.claude.com`，Model pricing 与 Additional models 两表）

| 模型 | 输入 | 输出 | 读 | 写 5m | 写 1h |
|---|---|---|---|---|---|
| Claude Fable 5.1 | 10 | 50 | 0.25 | 12.50 | 20 |
| Claude Opus 5.5 | 4 | 20 | 0.20 | 5 | 8 |
| Claude Sonnet 5.5 | 2 | 10 | 0.10 | 2.50 | 4 |
| Claude Haiku 5.5，提示 ≤100k | 0.10 | 0.50 | 0.01 | 0.125 | 0.20 |
| Claude Haiku 5.5，提示 >100k | 0.50 | 2.50 | 0.05 | 0.625 | 1 |
| Claude Mythos 5.1 | 10 | 50 | 0.25 | 12.50 | 20 |
| Claude Fable 5 | 10 | 50 | 1 | 12.50 | 20 |
| Claude Mythos 5 | 10 | 50 | 1 | 12.50 | 20 |
| Claude Opus 5、4.8、4.7、4.6、4.5 | 5 | 25 | 0.50 | 6.25 | 10 |
| Claude Opus 4.1、4 | 15 | 75 | 1.50 | 18.75 | 30 |
| Claude Sonnet 5 | 2 | 10 | 0.20 | 2.50 | 4 |
| Claude Sonnet 4.6、4.5、4 | 3 | 15 | 0.30 | 3.75 | 6 |
| Claude Haiku 4.5 | 1 | 5 | 0.10 | 1.25 | 2 |
| Claude Haiku 3.5 | 0.80 | 4 | 0.08 | 1 | 1.60 |

**Google**（`ai.google.dev`，Standard 档；未列 image、TTS、Live、Transcribe 等非纯文本模型）

| 模型（页面 ID） | 输入 | 输出 | 缓存读 | 备注 |
|---|---|---|---|---|
| Gemini 4 Argon（`gemini-4-argon`） | 2.00 | 10.00 | 0.10 | 2026-12-01 起 4.00 / 20.00 / 0.20 |
| Gemini 3.8 Flash（`gemini-3.8-flash`） | 0.75 | 3.75 | 0.075 | 2027-01-01 起 1.50 / 7.50 / 0.15；缓存存储 0.50（2027-01-01 起 1.00）/ 百万 token·小时 |
| Gemini 3.6 Flash（`gemini-3.6-flash`） | 0.75 | 3.75 | 0.075 | 同上 |
| Gemini 3.5 Flash-Lite（`gemini-3.5-flash-lite`） | 0.30 | 2.50 | 0.03 | 文本/图像/视频/音频同价；存储 1.00 |
| Gemini 3.1 Flash-Lite（`gemini-3.1-flash-lite`） | 0.25 | 1.50 | 0.025 | 音频输入 0.50、读 0.05；存储 1.00 |
| Gemini 3.1 Pro Preview（`gemini-3.1-pro-preview`、`-customtools`） | 2.00 | 12.00 | 0.20 | >200k：4.00 / 18.00 / 0.40；存储 4.50 |
| Gemini 3 Flash Preview（`gemini-3-flash-preview`） | 0.50 | 3.00 | 0.05 | 音频输入 1.00、读 0.10；存储 1.00 |
| Gemini 2.5 Pro（`gemini-2.5-pro`） | 1.25 | 10.00 | 0.125 | >200k：2.50 / 15.00 / 0.25；存储 4.50 |
| Gemini 2.5 Flash（`gemini-2.5-flash`） | 0.30 | 2.50 | 0.03 | 音频输入 1.00、读 0.10；存储 1.00 |
| Gemini 2.5 Flash-Lite（`gemini-2.5-flash-lite`） | 0.10 | 0.40 | 0.01 | 音频输入 0.30、读 0.03；存储 1.00 |

每个 Google 模型还列了 Search 与 Maps 的按次价格（有每月免费额度），随模型不同：Gemini 4 Argon 与 3.x 的多数模型是 `$14 / 1,000`，Gemini 2.5 Pro、Flash、Flash-Lite 是 Search `$35 / 1,000`、Maps `$25 / 1,000`。这是工具项，不在上表的 token 价格里。

**DeepSeek USD**（`api-docs.deepseek.com`，高峰价）

| 模型（页面写明的版本） | 输入（缓存未命中） | 输出 | 缓存命中 | 备注 |
|---|---|---|---|---|
| `deepseek-flash`（DeepSeek-V4.1-Flash-1017） | 0.30 | 1.20 | 0.006 | 低谷半价：0.15 / 0.60 / 0.003；旧名 `deepseek-v4-flash`、`deepseek-v4-flash-vision-exp` 仍被接受，但对应型号已退役，由这个版本按 Flash 价服务 |
| `deepseek-v4-pro`（DeepSeek-V4-Pro-0813） | 1.32 | 3.96 | 0.044 | 低谷半价：0.66 / 1.98 / 0.022 |

**DeepSeek CNY**（`api-docs.deepseek.com/zh-cn`，高峰价；与 USD 是两个独立页面，不是汇率换算）

| 模型（页面写明的版本） | 输入（缓存未命中） | 输出 | 缓存命中 | 备注 |
|---|---|---|---|---|
| `deepseek-flash`（DeepSeek-V4.1-Flash-1017） | 2.00 | 8.00 | 0.04 | 低谷半价：1.00 / 4.00 / 0.02 |
| `deepseek-v4-pro`（DeepSeek-V4-Pro-0813） | 9.00 | 27.00 | 0.30 | 低谷半价：4.50 / 13.50 / 0.15 |

**OpenAI**：页面 403，没有模型清单和价格。`deploy/catalog/dialects.json` 里的 6 个 OpenAI slug（`gpt-5.6-luna`、`gpt-5.6-sol`、`gpt-5.6-terra`、`gpt-6-astra`、`gpt-6-luna`、`gpt-6.1-sol`）未与官方页面核对。

目录侧没有可读的数据：这台服务器没有 `AXIOM_DATABASE_URL`，`go run ./cmd/axiom catalog check` 在配置检查处退出（`AXIOM_DATABASE_URL: required`）。所以上面只能记录页面和两个数据集的事实，不能回答“已上架、公开名字、当前参考价与选定来源、证据、未处理核对”；`deploy/catalog/dialects.json` 的 slug 只说明请求写法的数据覆盖了哪些模型，不能代替目录行。完整四家逐模型验收仍须在有真实目录的机器上跑，不能用这张表或临时数据冒充通过。

两个数据集与页面的交叉核对（只列不一致和会被 held 的；其余在 LiteLLM 有直接条目的文本模型，输入、输出和缓存读价与页面一致，LiteLLM 多数没有 1h 缓存写，OpenRouter 有；LiteLLM 没有 `claude-opus-4`、`claude-3-5-haiku` 的直接条目，只有 Bedrock/Vertex 前缀；两个数据集都只有美元，DeepSeek CNY 没有数据集对照）：

| 模型 | 页面 | LiteLLM | OpenRouter | 结论 |
|---|---|---|---|---|
| `deepseek-flash` | 高峰 0.30 / 1.20 / 0.006 | 同页面高峰 | `deepseek/deepseek-v4.1-flash-1017` 的基价是低谷 0.15 / 0.60 / 0.003，高峰在 `overrides` | OpenRouter 的条目 ID 与目录 slug 不同，基价也不是目录要的高峰价；catalog 只录高峰，不能把 OpenRouter 选作来源 |
| `deepseek-v4-pro` | 1.32 / 3.96 / 0.044 | 同页面 | `deepseek/deepseek-v4-pro` 1.60 / 3.20 / 0.135，`-0813` 0.115 / 4.00 / 0.11 | OpenRouter 与页面、LiteLLM 都不一致 |
| `google/gemini-3.8-flash` | 0.75 / 3.75 / 0.075 | 同页面 | 0.825 / 4.125 / 0.0825 | OpenRouter 高 10% |
| `anthropic/claude-haiku-5.5` | ≤100k 0.10 / 0.50 / 0.01；>100k 0.50 / 2.50 / 0.05 | 只有基础档 | 基础档 + `overrides`，数值与页面一致 | OpenRouter 能表达分档但按设计 held（数据集的阈值依据未写明）；LiteLLM 没有分档，当前价目表有分档时也不能跟随 |
| 12 个 Google 条目（本页 7 个） | — | 没有 `cache_write` | `input_cache_write` 有 16 位小数，如 `0.0000000416666666666667` | 按[模块说明](../../internal/catalog/README.md)拒绝整条，不能作为这些模型的来源 |

运行暴露的抽取问题：Anthropic 把模型名和说明放在相邻的内联标签里，CSS 的 flex 列把它们排成两行；DeepSeek 的标题锚点带 U+200B。旧抽取把前者粘成 `Claude Fable 5.1For…`、`$0.10 / MTokfor…`，并把后者留在正文。最终抽取按布局补边界：表格单元格、块标签、flex 列/网格、`display:block` 和图标前后留边界；`$0` + `.10`、`Pri` + `cing` 这类连续内联文本保持粘连；隐藏节点不贡献边界。`pageExtractorVersion` 为 2，`TestRecordedPricePagesSeparateTextRuns` 用两段裁剪过的页面固定逐字正文，另有 `$0.10`、`Pricing`、隐藏节点、flex/block/icon 和版本升级的回归测试。对同一份保存的响应重算：Anthropic 6 处空格、DeepSeek USD 1 个零宽字符、Google 14 个图标后的空格并去掉 1 个脚注前的假空格（正文 57,614 → 57,627 B），没有其他正文变化；CNY 页是在修复后取的，没有修复前的对照。版本升级时对同一响应运行旧抽取器，当前快照与旧结果逐字相同才保留快照，不新建、不产生复核，保留当前证据和未处理复核；不采用删空白后的比较，`1 23` 和 `12 3` 会碰撞。旧 worker 不能把当前指针移回，数据库触发器也拒绝当前指针回退到更低版本，真实内容变化仍按原基线复核。
