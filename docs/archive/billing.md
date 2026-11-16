# 计费存档

[计费领域](../domains/billing.md)决策背后的调研、实测和被否决方案的比较。这里是历史依据，不描述系统现在的行为；行为以领域文档和[模块 README](../../internal/billing/README.md)为准。外部源码的位置按当时核对的提交记，之后可能移动。

## 参考实现

| 主题 | new-api | sub2api | OpenRouter |
|---|---|---|---|
| 资金来源 | 钱包与订阅在同一接口后（`service/funding_source.go` 的 `FundingSource`、`WalletFunding`、`SubscriptionFunding`），四个偏好 `subscription_first`、`wallet_first`、`subscription_only`、`wallet_only`（`common/str.go:108`、`service/billing_session.go:388` 起）；订阅窗口在预扣时惰性推进（`model/subscription.go:1267`、`:1304`），过期由周期任务清理（`:1434`） | 订阅是分组属性，按日/周/月窗口限额、不走余额，没有钱包兜底（`backend/internal/service/billing_cache_service.go:759`）；续期在期限上累加（`subscription_service.go:290`、`:382`） | — |
| 套餐追加不够 | `PostConsumeUserSubscriptionDelta` 在超额时拒绝、不改扣钱包（`service/model/subscription.go:1516`、`service/tiered_settle.go:233`） | — | — |
| 套餐退款 | — | 用户入口按订单类型拒绝套餐自退（`service/payment_refund.go:186`），带扣减的退款固定扣整段 `SubscriptionDays`（`:258`），期限缩不了时撤销（`:331`） | — |
| 额度到期 | 只有兑换码的兑换期限（`model/redemption.go` 的 `ExpiredTime`） | 同（`service/redeem_code.go` 的 `ExpiresAt`） | — |
| 欠款控制 | 准入时按估算预消费，先放行事后扣（`service/billing.go`） | 在途预留放 Redis，故障时放行（`billing_cache_service.go`）；转发成功后才提交计费（`handler/gateway_handler.go`） | — |
| 去重 | — | `usage_billing_dedup` 表 | — |
| 不传递取消 | 客户端离开时关上游连接，仅流式（`relay/helper/stream_scanner.go:304-307`） | 断开后读完上游再计费（`gateway_upstream_response.go` 的 "continuing to drain upstream for billing"） | 按端点声明是否支持取消（「Stream cancellation」） |
| 失败与免单 | 对 Claude 流只补缺失项（`relay/channel/claude/relay-claude.go` 的 `HandleStreamFinalResponse`） | 先解析失败帧 usage 再报错（`openai_gateway_passthrough.go`） | 输出为 0 且结束原因为空或出错时免单，按次项照收；出错或被回退的尝试不收（「Zero Completion Insurance」、价格页 FAQ）；TTS 按是否交付可用音频决定 |
| 按端点定价 | 按 channel/group 计价（`service/quota.go`，`e449cc7`） | `ChannelModelPricing`（`backend/internal/service/channel.go`，`a3298b4`）；利润门拒绝不合格账号而不是夹价（`gateway_profit_control.go:40`、`openai_profit_control.go:302`） | 模型 `pricing` 是单个 provider 的价，`/endpoints` 逐端点列价；`provider.max_price` |
| 按请求价 | `tier("request", fixed(...))`（`pkg/billingexpr/fixed.go`） | `per_request_price` / `BillingModePerRequest`（`channel.go:106`、`channel_service.go:793`） | `max_price.request` |
| 批量 embeddings、rerank | 按上游整批 `usage.PromptTokens` 分档（`relay/embedding_handler.go:91`、`relay/rerank_handler.go:103`、`service/tiered_settle.go:33`） | 按整个请求体预留、按整批用量记账（`handler/openai_embeddings.go:112`、`:275`） | 一个整批 `usage` |
| 缓存读写口径 | `service/text_quota.go:303–350` | `openai_gateway_usage.go:180`、`:190`、`openai_images_direct.go:195` | `prompt_tokens_details.video_tokens` |
| 图片缺价回落 | `model_ratio.go:658` | `billing_service.go:1704,1732` | — |
| 文件搜索 | 按 `file_search_call` 输出项计费，内置价 2.5 / 1K 次（`service/responses_usage.go:73-84`、`setting/operation_setting/tools.go:31-52`） | — | — |
| 代码执行 | 按 `code_execution_requests` 计次，无内置价（`relay-claude.go:256–270`） | 只对联网搜索另收按次费（`billing_service.go:2173`） | bash 按秒计，新容器最少 30 秒 |
| 文本形状权重 | `tokenkit/tokenkit.go`（`e449cc7`，用 54 段会话文本校准） | — | — |
| 分词器 | `tiktoken-go/tokenizer`（`service/token_counter.go`，`206e0fa`） | 同（`openai_gateway_count_tokens.go`，`8f2cbb5`） | — |
| 记账 | 付款金额存在充值单（`model/topup.go` 的 `Money float64`），无复式账本 | 订单上存付款金额和币种，浮点折算（`service/payment_fulfillment.go`、`payment_amounts.go`） | `GET /api/v1/credits` 只有总额度和总用量 |
| BYOK | — | — | 按本应收取价格的 5%（`BYOK_FEE_PERCENTAGE = '5'`），PAYG 每月 $25,000 免收 |

## 官方价格（2026-10 到 2026-11 查）

- OpenAI `file_search` 每千次 $2.50，向量存储 $0.10 / GB / 天（1 GB 免费）；代码解释器按 20 分钟会话：1 GB $0.03、4 GB $0.12、16 GB $0.48、64 GB $1.92，最少 5 分钟。
- Anthropic 代码执行每小时 $0.05、最少按 5 分钟计，与 `web_search_20260209` / `web_fetch_20260209` 同用时免费；5 分钟缓存写是输入价的 1.25 倍、1 小时是 2 倍。
- OpenRouter File Parser：`mistral-ocr` 每千页 $2（美国区 $2.2），`cloudflare-ai` 免费，`native` 按输入 token。
- 联网搜索默认价 0.025 取自各家最高单次价（OpenAI 非推理模型 web search preview 每千次 $25）。
- 图片暂估 1600：Anthropic 按 28×28 像素一个视觉 token，标准档单图上限 1568；new-api 不知尺寸时按 1024×1024 估（520 到 1373）。文件 4096：new-api 对非图片文件一律 4096（`service/token_counter.go:166-169`），Anthropic PDF 一页约 3000–4600。高分辨率档（Opus 4.7 起单图上限 4784）会少估到约三分之一。
- ECB 日汇率：工作日约 16:00 CET 更新，TARGET 休市日不更新，非可成交报价。

## 实测

- 2026-10-14 本地实测 new-api v1.0.0-rc.44、sub2api 0.2.16：两家都不对联网搜索单独收费，sub2api 扣费恰好等于各 token 项之和；new-api 把断开传到上游，Anthropic 格式输入和缓存按 `message_start` 精确、输出按已转发文本估，OpenAI Chat 两者都估；sub2api 30 段里第 3 段断开仍按 300 个输出 token 全量扣费。两家的请求号对账头以此为准。
- 热点账户（容器、共享负载，#296）：逐请求准入 7 次往返、结算 9 次；16 个结算逐个 84.9ms、成批 18.2ms；账本一条带 `ON CONFLICT` 的 CTE 14.4ms，改三条集合语句 5.5ms；结束预留一条集合语句 8.2ms。
- 分词器（2026-10-21，Linux/amd64、Go 1.27.2，各三次中位）：o200k 初始化 121.4ms、6.67 MiB 堆，cl100k 75.2ms、3.33 MiB；两份词表让最小程序增加 9.02 MiB。v0.10.0 在长无断点文本上分配字节 +88.8%、次数 +18.2%。
- UUID 数组用 `pgtype.FlatArray`：单元素数组编解码从 5＋13 次分配降到 4＋8 次。
- 受众定价：50 候选下快照预计算约 12–15 µs、3752 B、5 次分配；准入每模型派生约 14–15 µs、4280 B、27 次分配。核心刷新/读价：700 层（500 模型、200 单模型受众）约 217/68 ms，1100 层（100 模型、10 全模型受众）约 330/117 ms，是当前建议的规模上限；扩大前用 `load` 标签的 `BenchmarkAudienceRefresh` 验证发布事务显著短于 5 秒刷新周期。

## 暂估的误差

误差 = (暂估 − 上游报告) / 上游报告；「合计」是按 token 合计的偏差。样本是 2026-11-01 本机 Claude Code 会话日志（`scripts/usage_samples.py` 抽出，含用户内容、不入库），2026-11-02 按中日韩权重 1.24 复测；入库的 50 条回复样本守住中位数（`TestEstimateError`）。

| 族 / 侧 / 内容 | 样本 | 中位数 | p5 | p95 | 合计 | 原字节规则的合计 |
|---|---|---|---|---|---|---|
| Claude 4.7+ 输入，英文 | 2032 | −8.2% | −63.2% | +15.1% | −4.0% | −48.7% |
| Claude 4.7+ 输入，中文 | 238 | 0.0% | −25.7% | +9.5% | −0.5% | −42.4% |
| Claude 4.7+ 输出，纯文本英文 | 345 | +9.4% | −9.5% | +22.2% | +8.8% | −7.7% |
| Claude 4.7+ 输出，纯文本中文 | 282 | +6.8% | −3.8% | +13.5% | +3.9% | −19.0% |
| Claude 4.7+ 输出，带工具调用 | 1478 | −38.5% | −60.7% | −3.0% | −24.4% | −49.0% |
| o200k 输出，英文（对词表精确计数） | 513 | −4.9% | −13.0% | +8.8% | −4.6% | +22.9% |
| o200k 输出，中文 | 375 | +9.7% | −4.4% | +21.3% | +3.5% | +13.3% |
| o200k 输出，工具调用 | 1478 | −1.0% | −15.2% | +15.0% | −3.6% | +6.8% |

- 权重 1.40 时中文输入合计 +6.2%、纯文本输出 +12.8%（中位 +16.4%），偏差随中文占比单调上升（中日韩占七成以上的 258 条 +14.9%）；降到 1.24 后为 −0.5% 和 +3.9%。中文输入的 p5 来自日志缺口：样本取相邻两次回复之间上下文的增量，Claude Code 附加而日志未记的内容落进误差。
- 按输出价为输入价 5 倍合计，Claude 费用偏差 −8.3%（原字节规则 −40.6%）；带工具调用的回复主导，表里不含每次调用约 45 个 token 的框架。入库 50 条样本上费用偏差 −0.2%（原字节规则 −27.0%）。带工具调用的 Claude 回复中位低估 45.4%，一次调用多出约 50 个 token、两次 98 个，所以框架取 45。
- Claude 4.7 之前的族沿用 new-api 在 claude-haiku-4-5 上的校准（输入 +0.5%、平均误差 3.3%，输出 −0.6%、3.8%）。OpenAI 输入用词表精确计数，误差只来自 1 KiB 切块（低于 0.5%，`TestTextCount`）和未计入的消息框架。
- 真词表乘常数的复核：o200k、cl100k 各自最合适的常数是中文输出 1.1、英文输入 1.6、代码输出 2.3，没有一个同时适合三者。

## Claude 分词器调查（2026-10-30）

没有一个来源同时满足「对 Claude 3 以后准确」和「许可证允许内置词表」：

- `anthropics/anthropic-tokenizer-typescript`（MIT）只有 Claude 2 的 `claude.json`，README 说明对 Claude 3 起只是粗略近似；`github.com/tmc/tokencount`（ISC）内置的就是这份词表，在 50 条 Claude 5.5 回复上合计偏差 −28.0%（形状估计同批 −0.2%）。
- Hugging Face `leafspark/claude-3-tokenizer`（Apache-2.0）自测与 Claude 3 Haiku 只有 82.69% 相符。
- `claylo/ctoc` 用约 27.6 万次 `count_tokens` 反推出 36,495 个 token、声称约 4% 误差，但没有 LICENSE，不能内置。
- `POST /v1/messages/count_tokens` 要 key 和一次网络往返、只数输入。

## 被否决的方案

- **一个模型统一售价**：便宜端点的成本被贵端点抬高，用户为没服务的端点付费；**调用时读渠道实时价**：无法在准入时冻结、审计和复算。
- **按节点租约过期判断收尾**：时间可能过时，清理与写入没有互斥，数据库恢复后会回收活着的进程；**被回收后进程内换新持有者继续服务**：在途结算已交给清理，问题没消失。
- **热点账户**：每请求一个保存点（多一次往返和一套回滚状态）；只靠余额行锁在数据库里等（等锁占满连接池，拖住冷 key）；全局写线程或跨账户合并（一个热点拖住所有账户、失败和审计范围放大）；只做请求内合并（#428，实测仍不够）；只调大完成批上限（锁持有时间按项增长）；流水线发出每项语句（服务端仍逐项执行）。
- **未声明档位按端点隔离**：要持久化端点实际档位、处理端点替换与别名、另建审计清除路径；仅告警会让平台损失持续累积。
- **兜底行 `markup` 分两次发布**：只在有旧节点时需要；迁移改写全部预留行会改历史成交输入并长时间锁表。
- **控制台批次另建汇总表**：多一份可能与账本不一致的状态；在内存里重放不保存：长历史账户永远追不上。
- **账本只用额度一个单位、现金按参考汇率折算**：10/71 这类汇率除不尽，现金账永远对不上渠道的原币金额。
- **TigerBeetle**：正式环境建议 6 台专用机器，集群规模建好后不能改，导出还需要 RabbitMQ。
- **分项明细在结算时复制整份发票**：在钱的热路径分配，产生第二份可漂移的金额。
- **受众定价为账户和每个等级组合复制完整路由**：账户 × 等级的乘积；全模型的账户规则仍让发布行数是账户数 × 模型数。
