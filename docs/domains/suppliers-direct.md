# 直连接口与云平台

官方 API 作为平台货源的两种接法：贴 key 直连（OpenAI、Anthropic、Google、Moonshot、其他兼容接口）和贴凭证接云平台（Vertex AI、Bedrock、Azure OpenAI）。它们和中转站共用[供应商](suppliers.md)的线路、凭证、端点、成本计划和路由，这里只写不同的地方：key 怎么来、成本从哪来、请求怎么签。对应代码在 `suppliers` 模块，公开接口和不变量见[模块 README](../../internal/suppliers/README.md)。

## 设计

### 直连接口

厂商自己的 API 或说同样格式的接口，贴 key 接入（`createDirectSupplier`）：`mode='direct'`，一条线路，没有登录、余额、分组清单和扣费记录，也不参与同步。`kind` 是适配器名：

| `kind` | 地址 | 调用格式 | 价目表币种 |
|---|---|---|---|
| `openai` | `https://api.openai.com` | Chat、Responses、旧版补全、嵌入 | USD |
| `anthropic` | `https://api.anthropic.com` | Messages | USD |
| `google` | `https://generativelanguage.googleapis.com` | Google 的 OpenAI 兼容层 `/v1beta/openai/…`：Chat、嵌入 | USD |
| `moonshot` | `https://api.moonshot.cn` | Chat、嵌入 | CNY |
| `compatible` | 人填，只有 origin | `api_format`：`openai` 发 Chat 和嵌入，`anthropic` 发 Messages；`gemini` 422 | 人填 |

- **顺序**：先在只读事务里核对请求、在请求内和已存凭证里找重复（`PlanDirect`），再在事务外逐把检测（`CheckDirect`，同时最多 16 把），最后在幂等事务里写（`CreateDirect`），写前再找一次重复。同一幂等键的重发直接得到第一次的回答，不再问厂商；指纹只含 key 的个数和末四位。
- **检测**：每把 key 经出站客户端 `GET` 一次模型清单（厂商不收费），10 秒。200 且是清单为通过；401 和 Google 的 400 `API_KEY_INVALID` 为失败（`credential_rejected`）；403（可能是地区封锁）、429、超时、5xx、读不懂的回答都不可判定，不存，可以重贴。Messages 族用 `x-api-key` 和 `anthropic-version`，其余用 Bearer。没有一把通过时什么都不写。
- **写什么**：供应商 `active`、`native_unit='usd'`、`balance_native` 为空、单位价值 1；一个选中的分组（`upstream_ref` 是 origin）；通过的每把 key 一条 `active` 凭证，封存原文，`upstream_key_ref` 是 SHA-256 前 128 位、`hint` 是末四位，带请求的并发和每分钟上限，`allowed_models` 是这把 key 自己列出的名字。清单里每个名字写进 `supplier_models`：目录映射到上架模型的，按模型自己的计费项定格式（同 BYOK 的规则）建端点；同一模型两个名字的取法同投影。目录认不出的留作待映射。审计 `supplier.direct_connect` 只记末四位和各自结果。
- **成本**：端点的成本计划就是目录在结算币种下的官方参考价（`basis='published'`，指纹 `price_list:<参考价 id>`），原生金额即结算币种金额，单位价值 1；目录没有这个币种的参考价，端点成本未知、不进路由。调用面不取请求号（`ChargeRef` 为空）。
- **调用**：按格式走厂商的路径和鉴权头（`adapter.Wire`）；401 和 Google 的无效 key 是凭证被拒，OpenAI 的 `insufficient_quota` 是这把 key 额度用尽，只歇这把；403 不标 key。
- **之后**：模型清单只在接入时读一次；加 key（`addSupplierKeys`）、单把检测由各自的操作做，暂停、恢复、移除与中转站相同，换登录回 `ErrNoAdapter`。

### 云平台

Vertex AI、Bedrock、Azure OpenAI 贴凭证接入（`createCloudSupplier`），`mode='cloud'`，其余同「直连接口」；`cloud_account` 存凭证不是秘密的部分。

- **`vertex`**：服务账号 JSON。RS256 签 JWT，只向 Google 的令牌服务换令牌（不读 JSON 的 `token_uri`），到期前 5 分钟换新。Anthropic 模型走 `rawPredict`、`streamRawPredict`，Gemini 走 OpenAI 兼容的 Chat。检测换令牌后读区域。
- **`bedrock`**：访问密钥，SigV4 签名。Messages 走 `InvokeModel`、`InvokeModelWithResponseStream`，事件流边读边转成 SSE。检测列本区域的 Anthropic 模型和推理配置（key 要有列表权限），AWS 以 403 `UnrecognizedClientException` 拒绝 key。
- **`azure`**：资源地址和 key，没有区域。检测列部署，部署即模型，Chat 带 `api-version`。
- **区域**：一个区域一条凭证（`region`），各有上限和检测，满了换下一条；别的供应商已存的同一凭证同一区域是重复；区域不服务是 `region_unavailable`，其余判定同直连接口。
- **模型**：Bedrock、Azure 用检测列出的，去掉 `anthropic.` 和版本后缀或按部署的模型映射，只能经推理配置调用的记进 `call_names`；Vertex 没有按项目列模型的免费接口，按目录在售的 Anthropic、Google 模型和官方名生成。
- **换凭证**（`replaceSupplierCloudCredential`）：给定区域（默认现有的）至少一个通过才在一个事务里换：通过的各成新行，沿用该区域原上限，新区域用给的；同一凭证回原行；其余擦除。

## 决策与理由

| 决策 | 理由 |
|---|---|
| 直连接口每家一个适配器包、共用 `direct` 包，OpenAI 的同时服务 BYOK | 注册表一包一行才能单独移除；同一个 `kind` 只能有一个适配器 |
| 供应商带接入方式 `mode`：`relay` 登录中转站或号池，分组是线路；`direct` 贴 key 直连，一条线路；`cloud` 云平台凭证，一条线路 | 线路、凭证、端点、成本计划和路由三种方式共用，不同的只有 key 怎么来、成本从哪来；`kind` 仍是适配器名（`openai`、`compatible`、`vertex`……） |
| 直连接口（OpenAI、Anthropic、Google、Moonshot、其他兼容接口）和云平台（Vertex AI、Bedrock、Azure OpenAI）作为平台货源 | 待定：改变了产品需求里「官方 API 暂不接入」，进入第一版要负责人在 #948 确认；接入的操作已实现 |
| 没有余额的供应商：`balance_native` 留空，按 `mode` 判断，`mode` 不是 `relay` 的不看余额，`Funds.Unbounded` 让任何成本上界都付得起；厂商以余额不足拒绝时照常暂停 | 写一个假余额（如 BYOK 的 1）会让成本上界大于它的请求被路由滤掉，后台也会显示一个不存在的余额；`mode` 是新列（迁移 00053），创建后不变，云平台沿用 |
| 直连接口的 `native_unit` 是 `usd`，含义是「结算币种的钱」，单位价值恒为 1（云平台同样），`confirmSupplierUnitValue` 对两者回 409 | 成本计划直接是结算币种的官方价，不需要换算；契约的枚举只有 `quota`、`usd`，人民币结算的直连接口（Moonshot）读这一列会误以为是美元，要页面按 `mode` 看待 |
| 直连接口的 `upstream_account` 是新生成的 UUID | 没有可登录的账号；表要求非空且 `(origin, upstream_account)` 唯一，随机值让同一家可以接多个账户 |
| 直连接口建好即 `active`，不停在 `connecting` | key 已逐把检测、模型已读出，没有同步会把它从 `connecting` 移走；契约写的「等质量检测」在中转站也还没有准入检测，统一的准入另行设计 |
| 直连 key 各算各的每分钟窗口；`upstream_key_ref` 是 key 的 SHA-256 前 128 位 | 贴多把 key 就是为了多份额度；摘要能找出重复而不保存或比较原文，与 CPA 相同 |
| 直连接口不取请求号，成本按官方价估算、不对账 | 厂商的扣费读不到，取了请求号的尝试每条都会被报「等不到扣费」 |
| Google 走它的 OpenAI 兼容层，`gemini` 格式先回 422 | 网关还不发 Gemini 原生的 `generateContent`（[接入的待定问题](gateway.md#待定问题)）；兼容层的 Chat 能直接用 |
| 只有 OpenAI 的直连端点声明 Responses，其余 OpenAI 族只声明 Chat | 兼容接口多数没有 Responses；网关会把 Responses 请求转成 Chat |
| 贴进来的 key 和云凭证只写不读（契约里 `writeOnly`），接口只回末四位和不是秘密的部分（项目、服务账号、资源地址）；幂等指纹不含原文 | 与 BYOK、商户密钥同一规则：明文只在 `credentials` 的密文里，不进响应、日志、审计和幂等记录 |
| 云平台一个区域一条凭证，并发和每分钟上限按区域设；换凭证时所有区域一起换 | 复用挑凭证：一个区域满了自然换到下一个，不另造区域调度 |

## 待定问题

- 直连接口和云平台进入第一版（#948），等负责人确认。
- 云平台只对过文档和假上游，真实账号待实测（#497）。
- 直连接口的模型清单之后多久重读、人工映射后谁补端点，未定。
