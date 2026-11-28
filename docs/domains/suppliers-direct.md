# 直连接口与云平台

官方 API 作为平台货源的两种接法：贴 key 直连（OpenAI、Anthropic、Google、DeepSeek、Kimi 等厂商和其他兼容接口）和贴凭证接云平台（Vertex AI、Bedrock、Azure OpenAI）。它们和中转站共用[供应商](suppliers.md)的线路、凭证、端点、成本计划和路由，这里只写不同的地方：渠道类型怎么描述、key 怎么来、成本从哪来、请求怎么签。对应代码在 `suppliers` 模块，公开接口和不变量见[模块 README](../../internal/suppliers/README.md)。

## 设计

### 渠道类型

一个渠道类型就是一个适配器，注册表是类型的唯一清单：契约、页面和别处都不再抄一份类型列表。每个适配器自己给出描述，节点启动时注册表逐个核对，描述不合法的不启动。

| 描述项 | 含义 |
|---|---|
| `kind` | 适配器名，存在 `suppliers.kind`，创建后不变 |
| `mode` | `relay`（登录中转站或号池）、`direct`（贴 key）、`cloud`（云平台凭证） |
| `vendor` | 厂商标识，也决定图标；只卖一家模型的类型就是目录作者的 slug（`openai`、`deepseek`、`moonshotai`），云平台和聚合商是它自己的标识（`aws`、`azure`） |
| `serves` | 按原厂官方价卖哪些目录作者的模型；空表示可以卖任何作者的模型、都按它自己的价（兼容接口、聚合商、中转站），目录的价一个都不用 |
| `name`、`group` | 显示名（厂商的品牌名，不翻译）；分组：官方 API、国内厂商、云平台、中转站 |
| `entries` | 价目入口：每个有自己的 ID、地址和价目表币种，地址和币种是一个值；同一地址可以有两个入口（DeepSeek 的人民币价和美元价）；请求选入口的 ID，从不另给币种；第一个是默认；没有入口的类型由人填地址和结算币种（兼容接口、Azure） |
| `families` | 能说的调用族：`openai`、`anthropic`、`gemini`；兼容接口是人选其一 |
| `forms` | 凭证的形式，每种是一组字段；多于一种时人选一种，第一种是默认 |
| `fields` | 所有形式共用的字段，按顺序显示 |
| 能力 | 列模型（`lists`：有不收费、要鉴权的清单接口，检测和定期重读都用它；没有的不定期重读，模型来自目录，key 只在加入时用一次最小生成检测）、读余额、按区域、要填部署 |

- **字段**：每个字段有名字、种类、是否必填、格式（正则）和长度上限。种类：`keys`（一行一把 key，每把一条凭证）、`secret`（一段密文）、`json`（一份密文 JSON，如服务账号）、`text`（不是秘密的设置）、`regions`（区域清单，每个一条凭证）、`deployments`（Azure 的部署）。字段名取自一个固定词表（`keys`、`api_key`、`access_key_id`、`secret_access_key`、`service_account_json`、`organization`、`project`、`api_version`、`regions`、`deployments`），页面按名字写标签；新类型要新字段时先加进词表。
- **启动时核对**：`kind` 合法且唯一；`mode` 与适配器实现的接口一致（`direct` 实现 `Direct`，`cloud` 实现 `Cloud` 和 `Signer`）；地址是裸 origin、币种是三位代码、默认在第一位；字段名在词表里、不重复，格式能编译；`regions` 只出现在按区域的类型，`deployments` 只出现在要填部署的类型；每种形式至少一个密文字段。
- **目录先行**：类型能用的前提是有能定价的模型：目录里它 `serves` 的作者已上架、且有某个价目入口币种的参考价的模型（自带价目表的聚合商按它自己的价目）。`listSupplierTypes` 给每个价目入口一个模型数；所有入口都是 0 的类型才置灰，选定入口后显示该入口的数；`serves` 为空又没有自己价目表的类型不给数、不置灰。接入时仍按每把 key 自己列出的名字建端点，模型数只说明「接了能卖什么」。
- **类型清单接口**：`GET /api/admin/v1/supplier-types` 返回全部描述和模型数，按分组、名字排序；中转站类型只有名字和分组，接入仍走识别和登录。

### 兼容 OpenAI 的厂商表

说 OpenAI 格式、只是地址和路径不同的厂商，是 `vendors` 包里一张表的行，不各开一个包；删一行就删了这个类型，其余照常编译。有自己协议或签名的厂商各自一个包：Anthropic、Google（Gemini 原生调用）、Azure、Bedrock、Vertex。人填地址和调用族的 `compatible` 也是自己的包。

一行写：`kind`、显示名、`vendor` 和 `serves`；地址及各自币种；OpenAI 格式所在的路径前缀（代替 `/v1`）；同一地址下 Anthropic 兼容接口的路径前缀（厂商提供时）；是否服务 Responses 和旧版补全；模型清单的路径（缺省是前缀加 `/models`）；余额读法；额外的设置字段。

| `kind` | 名字 | 地址（币种） | OpenAI 前缀 | Anthropic 前缀 | 余额 |
|---|---|---|---|---|---|
| `openai` | OpenAI | `api.openai.com`（USD） | `/v1`，含 Responses、旧版补全 | — | — |
| `moonshot` | Kimi | `api.moonshot.cn`（CNY）、`api.moonshot.ai`（USD） | `/v1` | `/anthropic` | `/v1/users/me/balance` |
| `deepseek` | DeepSeek | `api.deepseek.com`（CNY，或同址 USD） | `/v1` | `/anthropic` | `/user/balance` |
| `ark` | 火山方舟 | `ark.cn-beijing.volces.com`（CNY） | `/api/v3` | — | — |
| `qianfan` | 百度千帆 | `qianfan.baidubce.com`（CNY） | `/v2` | — | — |
| `minimax` | MiniMax | `api.minimax.cn`（CNY）、`api.minimax.io`（USD） | `/v1` | `/anthropic` | — |
| `zhipu` | 智谱 | `open.bigmodel.cn`（CNY）、`api.z.ai`（USD） | `/api/paas/v4` | —（不用 `/api/anthropic`，见「决策与理由」） | — |
| `bailian` | 阿里云百炼 | 中国站账号：`dashscope.aliyuncs.com`（CNY）；中国站账号 · 新加坡：`dashscope-intl.aliyuncs.com`（CNY，目录无价，成本未知）；国际站账号：`dashscope-intl.aliyuncs.com`（USD） | `/compatible-mode/v1` | `/apps/anthropic` | — |
| `openrouter` | OpenRouter | `openrouter.ai`（USD） | `/api/v1`，清单 `/api/v1/models/user` | — | — |
| `siliconflow` | 硅基流动 | `api.siliconflow.cn`（CNY）、`api.siliconflow.com`（USD） | `/v1` | — | — |
| `xai` | xAI | `api.x.ai`（USD） | `/v1`，含 Responses | — | — |
| `mistral` | Mistral | `api.mistral.ai`（USD） | `/v1` | — | — |

- **检测用的清单**：缺省是前缀加 `/models`，必须是要鉴权的请求：无效的 key 得到 401 才判得了 key。xAI 对无效 key 回 400，检测判为不可判定，不会放过坏 key。智谱的 `/api/paas/v4/models` 不在它的接口文档里，但两个地址对无效 key 都回 401，与百炼的 `/compatible-mode/v1/models` 一样能检测。OpenRouter 的公开 `/api/v1/models` 不校验 key，检测用要鉴权的 `/api/v1/models/user`。不能免费列模型的类型（`lists` 为假）不做定期重读，模型来自目录，key 只在加入时用一次输出上限 1 的最小生成检测，重读永远不发收费的请求。
- **没有模型清单的不收**：腾讯混元原平台已停服、迁往 TokenHub，这一批不收。
- **不在这一批**：Perplexity 的模型清单接口未经证实会校验 key（见「待定问题」）。

- **两个调用族**：有 Anthropic 前缀的行同时说两族，`families` 是 `openai` 和 `anthropic`。Messages 按模型声明，不按厂商：行里列出厂商的 Anthropic 兼容接口服务的模型名（按厂商文档列出的名字，如 Kimi、DeepSeek、MiniMax 的；`*` 只用于厂商写明全部模型都服务的），名单里的生成模型端点同时声明 Chat 和 Messages，其余只有 Chat，Messages 请求由网关转换。Messages 发到 `<地址><Anthropic 前缀>/v1/messages`，用 `x-api-key`，与 Chat 同一把 key、同一个价；网关对 Messages 请求优先选原生的 Messages，Claude Code 这类客户端不必经过转换。Anthropic 前缀与 OpenAI 前缀在同一地址下，凭证只发往供应商自己的地址这一条不变。
- **差异只用声明**：厂商的差异是行里的类型化字段或这一行声明的小钩子（模型清单路径、余额读法、额外的请求头），不是可配置的改写。请求体层面的差异（推理字段、`max_completion_tokens`、`stream_options`）由协议层按模型规格处理；某家确实要不同的写法，先录一份样本证明，再作为端点的类型化事实交给协议层，不在供应商一侧改请求体。每一行带一组录制的请求和回答样本，覆盖它声明的差异。
- **币种跟着价目入口**：结算币种就是所选价目入口的币种，内置类型不能另选：同一模型的人民币价和美元价不是汇率关系（[目录基线的「币种」](catalog.md#目录基线)），另选币种就是用错价目表。只有人填地址的类型（`compatible`、Azure）给结算币种。供应商之间币种不同时，按已有的汇率换算记账（`Credits`）。读得到余额的入口在接入时核对：余额接口报的币种与入口不同，是 422。账号而不是地址决定价目表的厂商（阿里云的中国站和国际站），入口带上账号的说法（`account`），由人按自己的账号选。
- **聚合商按自己的价**：卖多家模型的类型（OpenRouter、硅基流动）不列作者，成本只能用它对这次调用实际收的价乘线路倍率，不拿原厂官方价乘一个倍率去凑。不变量：聚合商的端点只按「它对这次调用会收的价」定成本，取不到就未知、不进路由。OpenRouter 在回答里报出这次扣的钱，按它记成本（见「OpenRouter 的成本」）；硅基流动的模型清单只有名字、价目只在网页上，回答也不报扣费，端点成本未知。

### 凭证：形式、字段和设置

- **形式**：类型的凭证可以有几种形式，每种是一组字段。Bedrock 是访问密钥（`access_key_id`、`secret_access_key`）或 API key（`api_key`）；Vertex 是服务账号（`service_account_json`）或 API key；其余只有一种。请求用 `form` 选一种（缺省第一种），只能填这一种的字段和共用字段，必填的都要有；适配器读整份凭证（`ReadCredential`），读不懂的指出字段名和原因，不带任何贴进来的文字。
- **设置**：凭证不是秘密的部分在每条凭证一份 `credentials.settings`（jsonb 对象，值都是字符串）：所用的形式（类型有多种时）和 `text` 字段的值，如 OpenAI 的 `organization`、`project`，Azure 的 `api_version`。写入前按描述核对字段名和格式；`Reload` 读到对不上描述的设置，整家供应商排除，不带着错的设置去调用。设置只由描述声明的字段组成，不是自由的请求头或请求体覆盖。
- **用在哪**：直连调用由适配器的 `Wire.Authorize` 带上设置（OpenAI 发 `OpenAI-Organization`、`OpenAI-Project`）；云平台的 `SignedCall` 带上设置（Azure 的 `api-version`、Bedrock 用签名还是 Bearer）。设置随凭证：同一条线路后来加的 key 可以属于另一个项目。
- **只写不读**：密文字段不进响应、日志、审计和幂等记录；凭证视图只回末四位和设置。
- `suppliers.cloud_account` 仍是云供应商凭证的显示身份（项目、服务账号、访问密钥末四位）；部署仍是线路的模型和端点，不在设置里另存一份。

### 接入

所有贴 key 和贴凭证的类型用同一个操作：`POST /api/admin/v1/supplier-types/{kind}/suppliers`（`createTypedSupplier`），按类型的 `mode` 走直连或云平台的流程。请求：名字、价目入口的 ID（`entry`，缺省第一个）或人填的地址、`format`（只有 `compatible`）、`form`、`fields`（密文和设置）、`keys`、`regions`、`deployments`、结算币种（只有没有入口的类型）、线路倍率、每把 key 或每个区域的并发和每分钟上限、原因。未知的 `kind` 是 404；入口 ID 不是这个类型的、有入口的类型给了地址或结算币种，都是 400。原来的 `createDirectSupplier`、`createCloudSupplier` 由它取代。

- **顺序**：先在只读事务里核对请求、在请求内和已存凭证里找重复（`PlanDirect`），再在事务外逐把检测（`CheckDirect`，同时最多 16 把），最后在幂等事务里写（`CreateDirect`），写前再找一次重复。同一幂等键的重发直接得到第一次的回答，不再问厂商；指纹只含 key 的个数和末四位。
- **检测**：每把 key 经出站客户端 `GET` 一次模型清单（厂商不收费），10 秒，带上它的设置。200 且是清单为通过；401 和 Google 的 400 `API_KEY_INVALID` 为失败（`credential_rejected`）；403（可能是地区封锁）、429、超时、5xx、读不懂的回答都不可判定，不存，可以重贴。没有一把通过时什么都不写。读余额的类型对每把通过的 key 再读一次余额（也不收费）；读不出的留空，等定期重读。
- **写什么**：供应商 `active`、`native_unit='usd'`、单位价值 1；一个选中的分组（`upstream_ref` 是 origin，`ratio` 是线路倍率）；通过的每把 key 一条 `active` 凭证，封存原文，`upstream_key_ref` 是 SHA-256 前 128 位、`hint` 是末四位，带请求的上限、它的设置、它自己列出的名字和读到的余额。线路倍率是请求的 `cost_ratio`，缺省 1，端点的成本计划一开始就按它算。清单里每个名字写进 `supplier_models`：目录映射到上架模型的，按模型自己的计费项定格式（同 BYOK 的规则）建端点；目录认不出的留作待映射。端点先写、凭证后存：某把 key 把线路已有端点的模型列成另一个名字时，它也服务这个端点，`allowed_models` 加上端点的名字，`call_names` 记它自己的名字，调用时发它自己的名字。审计 `supplier.direct_connect` 只记末四位和各自结果。
- **加 key**：`addSupplierKeys` 不带 `group_id`（带了 400），可带这批 key 的设置；按供应商自己的地址、格式和结算币种走与接入相同的三步（`PlanDirectKeys`、`CheckDirect`、`AddDirectKeys`）。重复的不检测，通过的每把一条凭证；它列出、线路还没有端点的目录模型补一个端点，已有的不动。审计 `credential.paste`。云供应商的凭证只能整份替换，加 key 是 409。

### 调用

- **路径和鉴权**：按格式走类型的路径前缀和鉴权头（`adapter.Wire`）：OpenAI 族用 Bearer，Messages 用 `x-api-key` 和 `anthropic-version`。
- **Google**：`google` 包，地址 `generativelanguage.googleapis.com`（USD）。key 的检测、模型清单和嵌入走 OpenAI 兼容层（`/v1beta/openai`，Bearer）。Gemini 2、3 的 Chat 请求的每个成员（含每条消息、每个内容块的成员）都能被原生格式确切说出时，由 `Signer` 转成 Gemini 的 `generateContent` 请求体（`protocol.ChatToGemini`，按调用用的模型名写思考），流式发 `/v1beta/models/<模型>:streamGenerateContent?alt=sse`，否则 `:generateContent`，key 在 `x-goog-api-key`；会续传长回答（`CONTINUATION`）的 Gemini 4 和其他模型，以及说不确切的请求（按地址或指定精度的图片、音频输入、助手消息的 `audio` 等转换不读的成员、`extra_body`、部分严格的工具、可调工具时的 `parallel_tool_calls:false`、思考表里没有的模型或设置、超过单次输出上限 64K 的 `max_tokens`），在同一次尝试里改发兼容层的 `/chat/completions`，续传由 Google 在那里做，同一把 key、同一个端点，原生路径不比兼容层少服务任何请求。原生回答经 `protocol.ChatFromGemini` 读成 Chat 流，整份回答当作只有一个事件的流、由中继读时才读正文。用量（缓存、思考、按模态的 token，含工具提示的）与 Gemini 入站同一张表；结束回答的那一帧或其后的用量才是终值，中途的只把输入侧随下一段内容交给中继；结束帧不带用量、截流或断流时按已交付的估输出。档位取回答里的，没有时取响应头 `x-gemini-service-tier`，兼容层的回答同样补上，降档按实际档计费。被拦的提示词是内容策略拒绝，回 403；原生回答的 `CONTINUATION` 当作到了上限（`length`）。
- **模型名**：请求体的 `model` 是端点的上游名；所选凭证的 `call_names` 给这个端点另起了名字时，发它自己的名字（云平台由 `Signer` 按 `SignedCall.Model` 签出，直连由调用面替换请求体顶层的 `model`，只在名字不同时做）。
- **失败**：401 和 Google 的无效 key 是凭证被拒，OpenAI 的 `insufficient_quota` 和 402（DeepSeek 的余额不足）是这把 key 额度用尽，只歇这把；403 不标 key。调用面不取请求号（`ChargeRef` 为空）。

### 成本与线路倍率

以下只对平台的直连和云供应商（`owner_scope='platform'`）。BYOK 也是 `mode='direct'`，但它的成本恒为零，重新投影、重新定价和改倍率都不碰它。

- **成本计划**：端点的成本计划是这个渠道的价乘线路倍率：`basis='published'`，原生金额即结算币种金额，单位价值 1；价的每个计费单位（token、按次、按张、按秒、按字符）一并带上。渠道的价是目录在价目入口币种下的参考价，来源是 OpenRouter 数据集的除外：那是聚合商的卖价，开源权重模型的是第三方服务商的价，常远低于原厂；这样的端点成本未知、不进路由。官方页面、LiteLLM（汇总各厂商的标价）和手填（人确认的数）都算作者自己的价。它只用于作者自己的价就是这个渠道的价的模型（`serves` 列出的作者）：直连厂商卖它自己的模型；Vertex 只有 Anthropic、Google 的模型；Bedrock 只有 Anthropic、Amazon 的模型，其余作者的模型在 Bedrock 上另有价，成本未知、不进路由。`compatible` 不列作者：人填的地址卖的是谁的模型、按什么价都不知道，它的端点成本未知、不进路由（见「待定问题」）。自带价目表的聚合商用它自己的价（见上）。没有价的端点成本未知、不进路由。
- **线路倍率**：线路（直连和云平台唯一的分组）的 `ratio`，缺省 1，范围 (0, 10]、至多 6 位小数，用于低于标价买到的 key（企业折扣、承诺用量、充值赠送）。`setSupplierCostRatio` 只对平台的直连和云供应商（中转站是 409，它的倍率由同步读出；BYOK 不可见），在一个事务里改倍率、让线路上每个有价的端点用上新倍率的成本计划、加配置代次，审计记前后值。
- **推导因子与指纹**：倍率不是 1 的计划带因子「官方价 × 分组倍率」（`official_price`、`group_ratio`），指纹是 `price_list:<价 id>:ratio:<倍率>`；倍率是 1 的计划不带因子，指纹仍是 `price_list:<价 id>`，已有端点升级后不出新版本。成本计划只追加，`(endpoint_id, fingerprint)` 唯一：写计划用「有就取、没有才插」（`EnsureCostSchedule`），倍率从 1 改到 0.8 再改回 1 时取回原来那行，并在同一事务里让端点重新指向它，不插重复行。
- **重新定价**：目录发布新的参考价、聚合商的价变了、或倍率变了，线路的端点按同一个函数重算并指向对应的计划；目录撤下了结算币种的参考价（或类型不再按目录卖这个作者）的端点成本改为未知、退出路由，不沿用已撤的价。倍率在供应商锁里、定价的同一个事务里读，重读和重新投影不会用锁前读到的旧倍率盖掉刚改的。已开始的尝试按回执里的计划版本结算。
- **不变量**：平台的直连和云线路上，有价端点的参考价都不来自 OpenRouter 数据集；每个有价端点在用的成本计划 = 渠道当前的价按线路倍率缩放、每个单价向上取整一次，因子乘得出（`Derivation.Check`）；改倍率与端点改指同成同败；BYOK 端点的成本计划不因这些操作改变。
- **只算一次**：倍率就是折扣，直连和云平台的成本按它算好；它们不取请求号、不对账，以后接入官方账单核对时与倍率后的成本比较，不再把折扣当作利润另记一次。

### OpenRouter 的成本

OpenRouter 把一个模型交给几家服务商中的一家，按实际服务的那家收费，调用前不知道是哪家：预留按所有服务商里的最高价，结算按回答报出的实际扣费，回答没报的按 OpenRouter 的生成记录补。用户的售价不变，照目录和定价规则出，这里只改平台的成本、毛利和端点能不能卖。线路要用没有配置 BYOK 的 OpenRouter 工作区。代码在 `suppliers` 的 `aggregator.go`、`aggregator_job.go`，OpenRouter 的读法在 `vendors` 的 `openrouter.go`。

- **最高价**：每小时的重读之后，用一把通过检测的 key（经它的出口）对线路上目录映射到的每个名字发一次 `GET /api/v1/models/{作者}/{模型}/endpoints`（不收费，10 秒，同时 4 个），对 `data.endpoints[]` 每一家的 `pricing` 和它 `pricing.overrides` 里的每一档（长上下文档在内）逐个计费项取最大值，不判断档位边界；`discount` 不看（折前价不低于折后价），超过 15 位的小数向上取整。Chat 的 `cache_write_tokens` 分不出缓存时长，两种缓存写入价（5 分钟、1 小时）取大，两项都按它；缓存音频算缓存读取。结果是美元每单位的价目表，存在 `supplier_models.ceiling`，读到的时间在 `ceiling_read_at`。有服务商对某项报了非零价、而价目表表达不了这一项的，404、列表为空、名字带 `:` 变体后缀的，都清掉最高价；超时和 5xx 保留上次的值。每个名字读到即单独提交，一轮被任务期限截断时已读到的照样留下。调用路径从不发这个请求；接入后到第一次重读（一小时内）端点成本未知。
- **过期**：最高价读到超过 24 小时的端点成本未知，下次读到再恢复。24 小时容得下 OpenRouter 的接口坏一整天而不让所有端点下线；更久时，新上的、涨价的服务商可能已经超出，而 `max_price` 只管得住它能表达的那几项。
- **成本计划**：端点的最高价是它所有在用名字（自己的上游名和各把 key 的 `call_names`）的最高价逐项取大；任一名字没有一天内的最高价，端点成本未知，不会有 key 按别的名字的价出站。计划 = 最高价 × 线路倍率，只给标准档定价，端点也只在标准档路由（`Routable.StandardOnly`）：要 flex、priority 的请求不路由到它，由别的供应商服务，没有就是已有的无端点回答；不指定服务档的请求不带服务档发给 OpenRouter，按标准档跑，它回报了别的档就照回答的 `usage.cost` 结算。计划仍有模型会报的计费项定不了价的（按次项目有站点默认价，不算），端点成本未知，不让一个定不了价的成本进模型的共享售价、连累别的供应商。端点缺的服务档不算缺价，也不进共享售价的保本线（`PriceCost.StandardOnly`），OpenRouter 线路不会撤掉模型给别的供应商的共享售价。`basis='ceiling'`，指纹 `ceiling:<最高价规范 JSON 的 SHA-256>`（倍率不是 1 时加 `:ratio:<倍率>`），照「有就取、没有才插」写，用 `SetProjectedEndpointCost` 改指，同价重读不撤掉摘牌。准入、路由和保本线都读这份计划，与其他端点一样，售价按最高价的成本定。
- **请求**：端点带一份 `provider.max_price`（`prompt`、`completion` 每百万 token，`request` 每次），是计划 ÷ 倍率，即 credits 的最高价，由 protocol 写进发往 OpenRouter 的请求（`protocol.Target.Provider`）；没有服务商在价内时 OpenRouter 不执行，算这个端点的失败，换下一个条目。OpenRouter 的调用策略是不传递取消且不被实测覆盖（`CallPolicy.CancelsFixed`）：它内部回退到的服务商可能不停止生成、照收整份回答，所以路由一次预留到输出上限、不分段；客户端断开后网关照规则读完上游，正好拿到扣费。
- **结算**：protocol 从结束回答的那次用量读 `usage.cost` 和 `is_byok`（流式、整份 Chat 和 embeddings 的整份回答都读），按 JSON 原文解析成精确小数（不经浮点，多出的位向上取整），放进 `protocol.Usage.Cost`；回答没正常结束、用量作废、不是非负数都算没报。`suppliers.Credits` 只对 `basis='ceiling'` 的计划采信它，别的计划一律不看（中转站或另一个 Axiom 回的 `cost` 不是我们付给它的钱，币种也未必是美元）；联网搜索循环的每次采样是各自的尝试，按各自回答里的数结算：
  - 报了、`is_byok` 不为真：成本 = `usage.cost` × 线路倍率，成本已知；显式的 `0`（如 OpenRouter 的空回答保险）同样是已知成本。
  - 报了、`is_byok` 为真：报的只是 OpenRouter 的手续费，成本 = 手续费 × 倍率 + 计划 × 用量，成本未知；检查任务把端点改为成本未知并告警。
  - 没报（断流、截断、读到一部分后出错）：成本 = 计划 × 用量，用量按[暂估](billing.md#用量来源与暂估)补齐，成本未知，等检查任务补。
- **记什么**：响应头 `X-Generation-Id` 是 OpenRouter 的生成号，回答和补查共用这一个键，存在 `attempt_costs.generation_id`（唯一），不当作扣费记录的对账键。`estimated_native` 是结算时入账的成本，之后不改；补查得到的成本记在 `actual_native`，历史和待处理队列先取它；`reported_cost` 是 OpenRouter 报的 credits（报了才有，`0` 也记），`cost_report` 是 `charged` 或 `byok_fee`，`bound_cost` 是计划 × 用量 ÷ 倍率（credits），`line_ratio` 是这次尝试结算时的线路倍率，补查和历史都按它算，之后改倍率不改旧账；`check_state` 是还要做的事：`lookup` 等读生成记录、`judge` 等对上界、`done`。
- **补没报的**：每分钟的检查任务（`suppliers.ceiling_checks`）对结束一分钟以上、还是 `lookup` 的尝试读 `GET /api/v1/generation?id=`，在事务外读、读完一个事务记：`actual_native` 是 `total_cost` × 倍率（BYOK 的记录是手续费 × 倍率加原来的上界），按生成号只记一次，再跑一遍什么也不变。记录还没有、读失败（超时、5xx）都下一分钟再读，从不因此结束；一天内没读到就不再读，按上界不动；凭证已擦除的尝试直接结束、按上界不动；本节点缺解密用的密钥版本只是打不开，下一分钟再读。每把 key 一轮最多 20 次，一把读不了的 key 占不满整批。改的只是这次尝试自己的成本行；账本里结算时记的供应商成本仍是上界，那次请求的毛利照旧算作未知。它和对账分开跑：对账是只读数据库的钱的任务，这里要等 OpenRouter 的回答。
- **扣费超过上界**：用户的收费不变。检查任务在 credits、倍率之前比较：`reported_cost` > `bound_cost` + 容差（1e-10 + 上界的 1%）、用量全是上游报的（`complete`），端点仍用这份计划时改为成本未知（`under_estimated`）；BYOK 的扣费一律这样处理。两种各发一次告警 `suppliers.reconciliation_difference`（`ceiling_exceeded`、`ceiling_byok`），日志 `suppliers.ceiling` 记发现和端点。都不在结算事务里写端点行。
- **供应商历史**：`getSupplierReconciliation` 把 OpenRouter 报过（或补到）扣费的尝试也算进去：估算按上界 × 那次的倍率，扣费按入账的成本，各算一笔对上的扣费；只有这种尝试、没有扣费记录的供应商也有天。
- **不变量**：计划 ≥ 每家服务商每一档的价；成本未知时入账的成本不为零（为零只在每家的价都为零时）；只有 `ceiling` 计划采信回答里的扣费；显式的零与没报分开存；结算按「请求 ID + 尝试序号」幂等，扣费随结局一起定下，重试得到同一行；补查按生成号幂等。

### 模型重读与余额

以下任务只扫平台的直连和云供应商，不碰 BYOK。

- **重读**：直连 key 的模型清单定期免费重读（只对能列模型的类型，复用 `CheckKey`，每把 key 每小时一次，River 任务，只扫 `active` 的供应商）：新列出的名字照「写什么」补进 `supplier_models` 和端点，不再列的名字不删端点，凭证的 `allowed_models` 跟着清单走；聚合商顺带重读价。每小时重读之后，以及目录的修订变了（每分钟比较一次；新上架的模型、新的参考价），直连和云线路重新投影一次：之前待映射的名字映射上了就建端点（要填部署的类型除外：部署名不是目录认得的名字）；Vertex 这类不列模型的按目录在售模型补端点；有价的端点按当前的价和线路倍率重新定价。重读被拒（401）照调用路径的规则标 `rejected`，不可判定的不改任何东西。
- **余额按钱包，不按供应商**：同一线路的 key 可能属于不同项目或钱包，所以声明了余额读法的类型（DeepSeek、Kimi）每把 key 各读各的余额，存在凭证上（金额和读到的时间，币种是价目入口的币种）；接入时随检测读一次，之后随每小时的重读读。接入时报的币种与入口不同是 422（见「币种跟着价目入口」）；之后才变的按平台的汇率换算再存，没有汇率不更新。从没读到余额的 key 算付不起。付不付得起用厂商自己会扣的钱比余额：请求的成本上界按标价算（倍率只用于算我们的成本），即计划上的上界 ÷ 线路倍率，等价于比较余额 × 倍率与计划上的上界，向下取整，精确比较。路由和挑凭证用同一个判断：供应商的可用资金是可用 key 中余额 × 倍率最大的一个，路由按它筛端点；挑凭证（`AcquireWithin`，带请求的成本上界）时跳过自己钱包付不起的 key，都付不起是 `ErrNoSlot`。厂商以余额不足拒绝时只歇这把 key，直到之后读到大于零的余额。某把 key 的余额低于供应商的告警线时发一次 `suppliers.low_balance`，回到线上或这把 key 不再可用时恢复（[记录与告警的「告警信号、事件与规则」](observability.md#告警信号事件与规则e5-3)）。一个钱包空了，其余照常。没声明余额读法的仍不看余额（`Funds.Unbounded`）。

### 云平台

Vertex AI、Bedrock、Azure OpenAI 贴凭证接入，`mode='cloud'`，其余同直连。

- **`vertex`**：服务账号 JSON。RS256 签 JWT，只向 Google 的令牌服务换令牌（不读 JSON 的 `token_uri`），到期前 5 分钟换新。Anthropic 模型走 `rawPredict`、`streamRawPredict`，Gemini 走 OpenAI 兼容的 Chat。检测换令牌后读区域。API key 形式见「待定问题」。
- **`bedrock`**：两种形式。访问密钥用 SigV4 签名；API key 用 `Authorization: Bearer`，区域照选。Anthropic 模型走 `InvokeModel`、`InvokeModelWithResponseStream`，事件流边读边转成 SSE；其余模型走 Converse（`Converse`、`ConverseStream`），Chat 与 Converse 之间的转换在 `bedrock` 包里。检测列本区域的基础模型和推理配置（key 要有列表权限），AWS 以 403 `UnrecognizedClientException` 拒绝 key。
- **`azure`**：资源地址、key 和部署（`deployments`，名字和所部署的模型），没有区域；`api_version` 是设置，缺省是代码里的当前版本。Chat 和嵌入发到部署的路径，Responses 发到资源的 `/openai/v1/responses`。检测是一次 `GET /openai/models`，与调用同一个 `api-version`，不收费。
- **区域**：一个区域一条凭证（`region`），各有上限和检测，满了换下一条；别的供应商已存的同一凭证同一区域是重复；区域不服务是 `region_unavailable`，其余判定同直连。
- **模型**：Bedrock 用检测列出的，去掉厂商前缀和版本后缀映射，只能经推理配置调用的记进 `call_names`；Azure 是填的部署，按填的模型（没填按部署名）映射；Vertex 没有按项目列模型的免费接口，按目录在售的 Anthropic、Google 模型和官方名生成。
- **换凭证**（`replaceSupplierCloudCredential`，请求同接入的 `form`、`fields`、`regions`）：给定区域（默认现有的）至少一个通过才在一个事务里换：现有区域沿用该区域现在的上限，新区域用给的；同一凭证回原行，其余成新行，两者都写这次的设置和检测走的出口（线路现有凭证共同的出口）；未通过和没给的区域擦除。Azure 的新 key 服务原来填的部署。
- **同一凭证只存一份**：建直连、建云平台和换云凭证在写入事务里依次进行（事务级咨询锁），写前再找一次重复，所以同时贴同一把 key 或同一凭证的两个请求，后一个得到 `duplicate`。

### 后台的「添加上游账号」

一个可搜索的类型选择器代替原来的三张卡片和厂商下拉：按分组排列，按名字、厂商和 `kind` 过滤，每项有图标、一行由描述生成的说明（调用族、币种、能否读余额）和目录模型数。中转站进识别流程；直连和云平台按描述渲染字段，复用 key、上限、区域和部署的控件；类型允许多个地址时才出现地址选择，多种凭证形式时才出现形式切换。图标是自托管的单色 SVG，按 `vendor` 取，没有图标的用首字母。

## 决策与理由

| 决策 | 理由 |
|---|---|
| 类型就是适配器，描述由适配器给出，注册表是唯一清单；契约里的厂商枚举和页面里的类型列表删掉，`kind` 是对着注册表核对的字符串 | 三处抄写的清单迟早有一处忘改；加一个类型只动适配器一处 |
| 只说 OpenAI 格式的厂商是一张表的行，有自己协议或签名的才单独成包；`just adapters-removable` 对表所在的包整体验证 | 十几家只差地址和路径，一家一个包是十几份相同的样板；删一行即删类型，其余不引用行 |
| 地址和价目表币种是一个值（价目入口），内置类型的结算币种就是入口的币种、不能另选；每个类型列出允许的入口，不收任意地址 | 同一模型的人民币价和美元价不是汇率关系，另选币种就是用错价目表；内置类型收任意地址就不再是官方渠道，也绕过地址核对；币种不同的供应商之间按已有汇率记账 |
| 有 Anthropic 兼容接口的厂商，按模型声明 Messages：只有厂商的 Anthropic 接口服务的模型同时声明 Chat 和 Messages | Claude Code 等客户端直接说 Messages，原生发过去免去转换；按厂商整体声明，会让 Anthropic 接口不服务的模型被发 Messages 而失败 |
| 凭证的形式是描述里的字段组，请求只能填一种 | Bedrock、Vertex 各有两种凭证，靠「填了哪些」猜形式会拼出半份凭证 |
| 不是秘密的设置存每条凭证一份 `settings`，按描述核对，不开放自由的请求头和请求体覆盖，不做每渠道的系统提示和状态码改写 | 设置跟着 key 走（同一线路的 key 可属不同项目）；自由覆盖会改变计费依据和实测能力，new-api 的模型映射、优先级、权重、多 key 轮询、自动禁用和重试由目录、路由和挑凭证覆盖 |
| 部署仍是线路的模型，不进设置 | 一件事只存一处；端点和 `call_names` 已经记着部署名 |
| 合并直连和云平台的接入为 `createTypedSupplier` | 页面只按描述渲染一个表单；`just api-compat` 只拦已有结构新增的必填项，首次生产部署前没有旧节点要兼容 |
| 直连和云线路的成本 = 渠道的价 × 线路倍率（线路分组的 `ratio`），写入时乘好并带推导因子；对账不再另记折扣 | 负责人在 #955 选定线路倍率，代替原先「按原价算、折扣体现在对账后的利润」；先例是 sub2api 账号的费率倍率。与中转站分组倍率、确认基础价同一机制：路由和结算只认存下的计划；折扣只在成本里算一次，否则利润里会再算一遍 |
| 线路倍率的不变量见「成本与线路倍率」；重新投影、重新定价、改倍率只对平台的供应商；计划按指纹取回已有行 | 成本偏低会让毛利看起来比实际高；BYOK 也是 `direct`，它的零成本不能被改写；`(endpoint_id, fingerprint)` 唯一，改回原倍率要取回旧行而不是再插一行 |
| 类型的模型数按价目入口分别算，所有入口都为 0 才置灰 | 目录先行：没有价的模型成本未知、进不了路由；只看默认入口会把国际站有价的类型误置灰 |
| 直连 key 定期免费重读模型清单，目录修订变了重新投影 | 厂商上新模型、目录上架后，线路不需要人重贴 key 就能卖 |
| 只给读得到余额的厂商读余额，按 key（钱包）读，准入按标价比余额 | 预付费的国内厂商余额归零就停，不读余额只能等用户请求失败才知道；同一线路的 key 可能是不同钱包，按供应商读会让一个空钱包停掉其余；厂商按标价扣钱，折扣只影响我们的成本 |
| 聚合商只按它对调用实际收的价定成本；硅基流动、Bedrock 上非 Anthropic、Amazon 的模型和 `compatible` 的端点成本未知 | 聚合商和云平台给第三方模型的价不是原厂官方价，拿原厂价乘一个倍率会把成本算错，成本算低就是毛利虚高；接入、列模型、检测 key 不依赖价，先接入，价源定下后端点在下一次投影时定价 |
| OpenRouter 预留按所有服务商、所有价档的逐项最高价，结算按回答的 `usage.cost` | 负责人在 #955 [选方案 A](https://github.com/TYCT-tyct/Axiom/issues/955#issuecomment-7233863853)。清单的 `pricing` 是首位服务商的价（OpenRouter [《Models》](https://openrouter.ai/docs/guides/overview/models)），调用按实际服务的那家收费，按它定价会低估；每家的价和长上下文档在 `/api/v1/models/{作者}/{模型}/endpoints` 的 `pricing` 与 `pricing.overrides`（[《Models API》](https://openrouter.ai/docs/api_reference/models)，美元每 token）。逐项取大不用判断档位边界，只会高估，售价和保本线按它定，不亏；实际扣费是 OpenRouter 自己的账，比任何估算都准 |
| `usage.cost` 默认就有，不发 `usage: {include: true}`；单位是 credits，即美元 | OpenRouter [《Usage Accounting》](https://openrouter.ai/docs/guides/administration/usage-accounting)：每个回答都带用量和以 credits 计的 `cost`，流式在最后一块，`usage.include` 与 `stream_options.include_usage` 已废弃、不起作用。[《API Reference》](https://openrouter.ai/docs/api_reference/overview)：流式时用量恰好一次、在 `[DONE]` 之前，生成号可经 `/api/v1/generation` 事后查成本。[《FAQ》](https://openrouter.ai/docs/faq)：credits 以美元为基准。new-api（8faf1d45e）对 OpenRouter 仍补发 `usage: {include: true}`（`relay/channel/openai/adaptor.go`），读 `usage.cost` 只为反推 Claude 的缓存写入 token（`service/quota.go` 的 `CalcOpenRouterCacheCreateTokens`），按自己的倍率收费、不记成本；sub2api（ee58c0013）没有 OpenRouter 上游。两家都不按它记成本，没有可照搬的做法 |
| 扣费随用量在网关里带进结算（`protocol.Usage.Cost`）；回答没报的才读生成记录，按生成号直接改那次尝试的成本行 | 回答里有准确的数时结算就能定下，与账本同成同败，不多一个请求、毛利不先错一阵。只按补查（先按上界入账，事后逐笔读 `/api/v1/generation`）每次调用多一个请求、记录有延迟，所以只用在断流的尝试上。补查不走扣费记录和 `ChargeEvent`：它们不带金额，校正也保留原来的供应商成本，记不进差额；生成号是回答和补查共用的唯一键，一条按状态更新就幂等 |
| 结算之前崩溃的尝试不补供应商成本 | 负责人在 #955 裁定：要补就得在拿到响应头时先把生成号写进库，异步写有窗口、同步写拖慢转发，迟到的结算还会与补记重复。缺的只是平台自己的成本账，用户的预留和清理接管照旧，不多收也不少收 |
| OpenRouter 线路的倍率是一个 credit 我们实际花多少钱，成本 = `usage.cost` × 倍率；超上界的比较用 credits、在倍率之前 | 低于面值买到的 credits 与直连 key 的折扣同理，成本按倍率算一次（见「只算一次」）。上界是 OpenRouter 的价，`usage.cost` 也是 OpenRouter 的数，两者在同一单位里比；乘了倍率再比，倍率会同时缩小两边，看不出 OpenRouter 多收 |
| OpenRouter 的端点只卖标准档 | OpenRouter 的 endpoints 清单没有按服务档的价，所以只卖它的标准档：给 flex、priority 套上标准档的上界，既不是那一档的成本，又会在共享售价低于它时亏本或连累别的供应商 |
| OpenRouter 不分段预留，实测的取消结果也不改它（`CancelsFixed`） | OpenRouter 内部会回退到别的服务商，有的不因断开而停止生成、照收整份回答（[《Streaming》的「Stream cancellation」](https://openrouter.ai/docs/api_reference/streaming#stream-cancellation)）；只留首段时余额续不上，多出的由平台承担。某个端点实测会取消，只说明测的那次落到的服务商会，下一次未必，所以按输出上限整额预留 |
| 发 `provider.max_price`，最高价超过 24 小时作废 | `max_price` 让 OpenRouter 自己不选超出我们上界的服务商，没有在价内的就不执行（[《Provider Routing》](https://openrouter.ai/docs/guides/routing/provider-selection)），新上的、涨价的服务商在下次重读之前也超不出；它只能表达输入、输出和按次，其余项靠过期作废和超上界摘牌兜住 |
| 只有 `basis='ceiling'` 的计划采信回答里的 `cost`；`is_byok` 为真时两笔都记、端点下线 | 回答里的 `cost` 只有 OpenRouter 自己报才是我们付的钱：中转站可能透传上游的数，另一个 Axiom 回的是它的站点货币。BYOK 时 `cost` 只是 OpenRouter 的手续费、模型费由服务商另收（[《BYOK》](https://openrouter.ai/docs/guides/overview/auth/byok)），只记一笔就少记；线路本应用没有 BYOK 的工作区，出现就是配置错了，下线并告警 |
| 显式 `cost: 0` 是已知成本，没报不是零 | OpenRouter 对空回答等情况不收费（[《Zero Completion Insurance》](https://openrouter.ai/docs/guides/features/zero-completion-insurance)），这时零就是它的扣费；字段缺失则什么也说明不了，按上界记，并分开存以便核查 |
| 扣费超过上界时照实际入账，由每分钟的检查任务摘牌，不在结算里写端点；同价重读用 `SetProjectedEndpointCost` | 用户的收费已按锁定的售价定，不该因此变；摘牌沿用对账的 `under_estimated`，价不变就一直摘着，避免一小时后同一个偏低的价又上线；结算事务只写自己的行，热点账户的批次不多锁端点行 |
| 智谱、百炼按目录的官方价卖，用模型清单检测 | 两家的模型清单要鉴权、不收费，无效 key 回 401（四个地址都实测过）；百炼还卖 DeepSeek、Kimi、GLM 等别家的模型，价是百炼自己的，`serves` 只有 Qwen，这些端点成本未知 |
| 直连和云线路不用来源为 OpenRouter 数据集的参考价，这样的端点成本未知；官方、LiteLLM、手填照用 | 目录给每个模型选一个价源（[价格以模型选定的来源为准](catalog.md#目录基线)）。OpenRouter 的价是聚合商的卖价，开源权重模型的是第三方服务商的：`glm-5.3` 在那里是每百万 $0.07／$1.40，智谱自己收 $1.40／$4.40，拿它给智谱直连定价会把成本算低，路由以为便宜，毛利虚高。LiteLLM 汇总的是各厂商自己的标价，官方是厂商页面，手填是人确认的数，new-api、sub2api 给直连渠道定价也是人设的数乘倍率；只要求官方会让大多数按 LiteLLM 定价的模型退出直连路由 |
| 智谱只说 OpenAI 格式，不用 `/api/anthropic` | 智谱的 [API Key 分类管理说明](https://docs.bigmodel.cn/cn/coding-plan/notice/api-key-management)：分开管理之前建的「兼容」key，账号曾有编程套餐时在 `/api/anthropic` 消耗套餐额度、套餐到期后中断服务，而同一把 key 在 `/api/paas/v4` 照常按余额计费；检测走的是后者，验得过的 key 发 Messages 仍会失败，套餐期内按标准价记的成本也不对。Messages 请求由网关转成 Chat |
| 百炼的入口按账号站点分：中国站账号在北京（人民币）、中国站账号在新加坡（人民币，成本未知）、国际站账号在国际地址（美元） | 阿里云账号按所属站点的价和币种结算，与 key 用在哪个地域无关（[阿里云与 Alibaba Cloud 账号说明](https://www.alibabacloud.com/help/en/account/aliyun-vs-alibaba-cloud)）。中国站账号在新加坡的价（`qwen3.6-plus` 输入每百万 ¥3.747）不是目录里的人民币价（北京 ¥2）：这个入口照常接入、列模型，但入口标为目录无价（`Unpriced`），端点成本未知、不进路由，等目录有了这张价目表再卖；不卖比按错的价卖好。地址分不出账号，入口写明账号由人选；中国站账号的 key 选了国际站入口会按美元价记，这一条只靠人选对 |
| 硅基流动不读余额 | 国内站的 `/v1/user/info` 已停用（官方公告），余额读法目前按类型而不是按价目入口声明；端点成本未知时余额也用不上 |
| 直连的调用面共用 `direct` 包；OpenAI 那一行同时服务 BYOK，按 `kind` 取 | 同一个 `kind` 只能有一个适配器；BYOK 与平台的 OpenAI 线路是同一个调用面 |
| 供应商带接入方式 `mode`：`relay` 登录中转站或号池，分组是线路；`direct` 贴 key 直连，一条线路；`cloud` 云平台凭证，一条线路 | 线路、凭证、端点、成本计划和路由三种方式共用，不同的只有 key 怎么来、成本从哪来 |
| 直连接口和云平台作为平台货源 | 负责人在 #948 定的第一版范围，#955 扩到国内外主流厂商 |
| 不读余额的供应商：`balance_native` 留空，`Funds.Unbounded`；厂商以余额不足拒绝时照常暂停 | 写一个假余额会让成本上界大于它的请求被路由滤掉，后台也会显示一个不存在的余额 |
| 直连接口的 `native_unit` 是 `usd`，含义是「结算币种的钱」，单位价值恒为 1（云平台同样），`confirmSupplierUnitValue` 对两者回 409 | 成本计划直接是结算币种的官方价，不需要换算；页面按 `mode` 看待这一列 |
| 直连接口的 `upstream_account` 是新生成的 UUID | 没有可登录的账号；随机值让同一家可以接多个账户 |
| 直连接口建好即 `active` | key 已逐把检测、模型已读出，没有同步会把它从 `connecting` 移走 |
| 直连 key 各算各的每分钟窗口；`upstream_key_ref` 是 key 的 SHA-256 前 128 位 | 贴多把 key 就是为了多份额度；摘要能找出重复而不保存原文 |
| 直连接口不取请求号，成本按官方价估算、不对账 | 厂商的扣费读不到，取了请求号的尝试每条都会被报「等不到扣费」 |
| Google 的 Chat 能确切说出时走 Gemini 原生的 `generateContent`，否则在同一次尝试里走 OpenAI 兼容层；检测、模型清单和嵌入走兼容层 | 原生接口带回完整的思考签名、按模态的用量和实际服务档位；兼容层是 Google 自己的映射，原生说不确切的请求交给它，不因换了路径少服务或换端点。两条路径计费逐项相同（`TestChatGoesNative`、`TestChatFallsBackToTheCompatibleLayer`）；兼容层的模型清单能用同一把 key 免费检测 |
| Chat 转 Gemini：改变不了回答的丢掉，能确切说出的照说（`service_tier` 的 flex、priority 写 `serviceTier`，default、auto 不写；`tool_choice:"none"` 时不看 `parallel_tool_calls`；全部严格的工具在可选调用时写 `VALIDATED`；思考按 Google《Gemini thinking》逐模型的表：2.5 按兼容层的预算表和各自的预算范围，Flash、Flash-Lite 的 none 是 0，其余按模型列出的档位），其余改走兼容层 | 自己猜的映射（如把 minimal 写成 low、给默认不思考的模型补预算）会让同一请求在两条路径上思考量不同；表外的交给 Google 自己映射。比 Converse 多一条退路，所以说不了的不必换端点 |
| 只有声明了 Responses 的行（OpenAI、xAI）的端点声明 Responses，其余 OpenAI 族只声明 Chat | 兼容接口多数没有 Responses；网关会把 Responses 请求转成 Chat |
| 贴进来的 key 和云凭证只写不读，接口只回末四位和不是秘密的部分；幂等指纹不含原文 | 与 BYOK、商户密钥同一规则：明文只在 `credentials` 的密文里 |
| 云平台一个区域一条凭证，并发和每分钟上限按区域设；换凭证时所有区域一起换 | 复用挑凭证：一个区域满了自然换到下一个 |
| Azure 的部署由人填，检测只证明地址和 key 对得上 | 现行数据面版本不向资源 key 列部署；对部署发一次调用要花钱，还会误判 |
| 经出口代理的调用先在本地按出站策略检查目标主机，再把主机名交给代理那端解析，不在本地解析后按 IP 连 | 负责人在 #955 同意了这一做法，与 sub2api 相同：在国内的节点本地解析官方厂商的域名常被污染或按地区给出别的地址，按它连会到错的地方。代价：本地检查挡住写成或在本地解析成内网的目标，但一个在本地看是公网、在代理那端解析成内网的名字，会到达代理运营者自己的网络 |
| 发往 Vertex AI 和 Bedrock 的 `anthropic-beta` 只留各自接受的功能（表在各适配器里），一个不剩就不带该头或 `anthropic_beta` | 两个平台遇到不认识的功能整个请求都失败，Claude Code 会带它们不支持的。名单按各平台文档（AWS 的 Claude Messages 与工具文档、Anthropic 的 Google 托管版功能清单）列出，不照抄 sub2api：它漏了 Bedrock 的交错思考和 Vertex 的电脑操作 |
| Chat 转 Converse：改变不了回答的成员（`store`、`metadata`、`top_logprobs`、标识、上一轮的 `reasoning_content`）丢掉，取默认值的 `n`、`logprobs`、惩罚、`logit_bias`、`modalities`、`service_tier`、文本 `response_format` 也丢掉，空的 `reasoning_effort` 当没设，没有工具时 `parallel_tool_calls` 不看；`name` 照 protocol 写成正文前的「名字: 」；`reasoning_effort` 对 Nova 2 Lite 写成 `reasoningConfig`（取最近的档，`none` 即它的默认不推理，`high` 带输出上限时 Nova 会拒）。会改变回答又表达不了的（`seed`、非零惩罚、低精度图片、其他模型的 `reasoning_effort`、`n>1`、JSON 格式与 schema、严格工具、按地址的图片、不认识的成员）一律不丢，算端点的失败，不执行，换下一个条目 | 网关已按 Chat 读过请求体，Converse 说不出的是这个端点的能力，别的端点可能接得住；回 400 会让有备选的请求也失败（[网关「路由参数」](gateway.md#路由参数)）。悄悄丢掉会让回答不是客户端要的，与 protocol 转 Messages 拒绝同样的成员一致；其他模型默认推理与否不一，`reasoning_effort` 说不准就不发 |
| 同时写同一凭证用事务级咨询锁排队，不加跨供应商的唯一索引 | 重复的判定跨所有凭证（含 CPA 列出的 key），锁让后到的得到 `duplicate` 而不是写入报错 |

## 待定问题

- Vertex 的 API key 只能调 Gemini 的 `generateContent`，不能调 Claude 的 `rawPredict`，也不能调 OpenAI 兼容的 Chat。Chat 转 Gemini 原生已有（`google` 包），开这种形式还要：新的凭证形式和它的检测（API key 不能列模型）、只给这种凭证建 Gemini 模型的端点、Vertex 的地址和鉴权；不是一行的改动，开之前描述里不列它。
- 硅基流动、`compatible` 接入的地址和 Bedrock 上第三方模型的价从哪来，定下之前三者的端点成本未知、不进路由。候选：人确认基础价（`confirmBasePrices` 现在只对中转站，要给直连线路另存基础价并由定价函数读取）。AWS 的 Price List 接口待查。Perplexity 等它的清单接口证实会校验 key；腾讯 TokenHub 待查证后再加。
- OpenRouter：结算之前崩溃的尝试不记供应商成本，是接受的缺口（见「决策与理由」）；补查改了尝试的成本行后，账本里那次的供应商成本仍是上界，要不要把差额也记进账本（`cost_adjust`）待定；读 OpenRouter 的余额（`/api/v1/key`）不在这一版。补查期满时恰逢 5xx、同一把 key 一轮超过 20 笔到期记录时，有的尝试没读到生成记录就结束；这两种情况成本都按上界记，上界不低于每家服务商的标价，只有 OpenRouter 收得比它自己的最高标价还高时，我们的账才会少记。
- 余额读法按价目入口声明（硅基流动只有国际站能读余额），要与路由的「从没读到余额的 key 算付不起」一起按入口判断；等硅基流动的端点有价再做。
- 同一钱包的几把 key 各读一次余额、各自比较，若厂商的余额接口给出账户标识，可以合成一个钱包少读几次；看录制样本再定。
- 每把 key 的花费上限、new-api 的任务类渠道（Midjourney、Suno、视频）不在这一版。
- 各厂商的路径、Anthropic 兼容接口支持哪些模型、余额接口的报文，以录制样本为准；云平台和各厂商的真实账号待实测（#497）。
