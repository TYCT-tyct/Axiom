# payments

订单、收款渠道、回调、补单、退款与对账在代码层面的接口、不变量和数据。行为和理由见[支付](../../docs/domains/payments.md)，记账、额度批次和冻结见[计费](../../docs/domains/billing.md)。

> 本文先于代码写成，是 [Issue #309](https://github.com/TYCT-tyct/Axiom/issues/309) E1-2 至 E1-6 的设计；各部分随「实现拆分」里的 PR 落地，代码与本文不一致时，在同一个 PR 里改本文。引用的 sub2api 源码都是 `backend/internal/` 下的路径（提交 `8f2cbb5`，2026-10-19 查）。

## 接口（草图）

```go
// 引用 billing、accounts、observability（依赖表见 docs/architecture.md）
type Kind string     // 收款渠道的种类："easypay"、"stripe"……
type Method string   // 本站的付款方式代码："alipay"、"wxpay"、"usdt.tron"
type MerchantID, OrderID, RefundID uuid.UUID // 对外写法 mch_、ord_、rfd_
type Money struct{ Currency platform.Currency; Minor int64 } // 付款币种的最小单位；位数取 ISO 4217 表

func New(c Config) (*Payments, error) // Plugins（按 Kind 登记）、*pg.DB、平台范围、Keyring、egress.Client、告警信号
func (p *Payments) Options(ctx context.Context, s pg.Scope, credits int64) ([]Option, error)            // 充值页：每种付款方式的总额、手续费、币种
func (p *Payments) CreateOrder(ctx context.Context, s pg.Scope, r CreateOrder) (Order, Checkout, error)  // 账户范围；事务、渠道调用、事务
func (p *Payments) Verify(ctx context.Context, s pg.Scope, id OrderID) (Order, error)                    // "我已付款"：查一次渠道
func (p *Payments) Cancel(ctx context.Context, s pg.Scope, id OrderID) (Order, error)
func (p *Payments) Notify() http.Handler                                                                 // GET/POST /api/payments/v1/notify/{merchant}
func (p *Payments) Refund(ctx context.Context, s pg.Scope, r RefundRequest) (Refund, error)              // 冻结 → 发送中 → 渠道 → 落定；r.Key 幂等
func (p *Payments) Resolve(ctx context.Context, s pg.Scope, r Resolution) error                          // 人工结论：附渠道凭证，按风险分级审批
func (p *Payments) SaveMerchant(ctx context.Context, tx pg.Tx, m MerchantInput) (Merchant, error)        // 调 Plugin.Open 校验后封装密钥；有未完成订单时拒改身份字段
func Register(q *pg.Queue, p *Payments) error                                                            // 周期任务 payments.poll、payments.reconcile
```

### 渠道接口

照 sub2api 的 `Provider` 和它的扩展接口（`payment/types.go:217-263`），合成一个接口加能力集合：

```go
type Plugin interface {
	Kind() Kind
	Open(c MerchantConfig) (Channel, error) // 校验并绑定一个商户账户；保存配置时也调用，不合法就拒绝保存
}
type MerchantConfig struct {
	ID MerchantID; Currency platform.Currency; NotifyURL string
	Settings []byte                    // 非密配置，规范 JSON，插件自己定义结构并严格解析；核心已核对它的哈希与密文里的一致
	Secrets  map[string]secret.Secret  // 解封后的密钥，只在内存里；轮换期间含旧密钥，验签时新旧都试
	Client   *egress.Client
}
type Channel interface {
	Caps() Caps // Query | Close | Refund | QueryRefund | RefundKey | Statements；易支付按商户配置声明
	Methods() []Method
	Create(ctx context.Context, r CreateRequest) (Checkout, error)
	Verify(ctx context.Context, c Callback) (Notice, error)      // 验签加商户身份；不相关的事件返回 Notice{Relevant: false}
	Query(ctx context.Context, outTradeNo, tradeNo string) (Payment, error) // outTradeNo 是字符串：找不到订单的付款也要能查
	Close(ctx context.Context, order OrderID, tradeNo string) error
	Refund(ctx context.Context, r RefundCall) (RefundState, error)
	QueryRefund(ctx context.Context, r RefundCall) (RefundState, error)
	Ack(ok bool) (status int, body []byte) // 渠道要求的应答，sub2api handler/payment_webhook_handler.go:215-226；ok 只在事务提交后为真
}
type CreateRequest struct{ Order OrderID; Method Method; Total Money; Subject, ReturnURL string; ClientIP netip.Addr; Mobile bool } // ReturnURL 由核心按站点域名生成
type Checkout struct{ TradeNo, PayURL, QRCode, ClientSecret string } // 存库前去掉 NUL（sub2api payment_order.go:509-523）；ClientSecret 只交给浏览器，不存库
type Callback struct{ Method string; RawQuery string; Body []byte; Header http.Header }
type Notice struct{ Relevant, Success bool; Order, TradeNo string; Paid Money; Raw string }
type Payment struct{ State PayState; TradeNo string; Paid Money } // Pending | Paid | Closed
type RefundCall struct{ Refund RefundID; Order OrderID; TradeNo, ProviderRef string; Amount Money; Reason string }
type RefundState struct{ State RefundOutcome; ProviderRef string } // Succeeded | Failed | Pending
type Rejected struct{ Code, Message string }                      // 渠道明确拒绝；实现 error
```

- **错误只分两种**：`*Rejected` 是确定的失败（下单被拒、退款被拒）；其他任何错误都是"结果未知"，调用方不得据此改订单或退款为失败。超时、断连、5xx、HTML 或解析不了的响应都是未知。
- **用能力集合而不是可选接口**：sub2api 用类型断言判断扩展接口（`payment_fulfillment.go:65-69`），能力随插件种类固定；易支付是否能查单、能退款取决于这家网关，是商户账户的配置，所以由 `Caps` 声明。调用没有声明的能力是程序错误，panic。
- **`RefundKey`**：渠道把 `RefundCall.Refund` 当作退款单号，同一单号重发只退一次（Stripe 的幂等键、支付宝的 `out_request_no`、微信的 `out_refund_no`）。只有声明了它，结果未知的退款才能重发；易支付不声明。
- **链接**：核心在存库和返回之前检查 `PayURL`、`QRCode` 里的链接，只放行 `https` 和 `alipays://`、`weixin://`、`wxp://`，以 `//` 开头的和其他协议当作 `*Rejected`。插件只负责把以单个 `/` 开头的相对链接按网关地址补全。
- **金额**：`Notice.Paid`、`Payment.Paid` 由插件把网关的十进制文本精确解析成最小单位（`platform` 的币种表），位数超出就当作金额不符，交给核心比较；插件里不做容差。没有币种字段的渠道（易支付）填商户账户的币种。
- **插件拿不到数据库**：只拿到配置、密钥和出站客户端；状态和钱都在核心里。插件的错误和日志不带请求的查询串和表单（易支付把密钥放在查询串里）。

### billing 要提供的接口

payments 引用 billing，在自己的事务里调用（[总体架构的规则 5](../../docs/architecture.md#模块边界规则)：数据的所有方提供接受调用方事务的函数）：

| 函数 | 用途 | 由谁实现 |
|---|---|---|
| `billing.TopUpRate(ctx, tx, from, to platform.Currency) (Rate, error)` | 当前参考汇率版本（ID、分子、分母）与充值方向的缓冲，订单据此锁定；过期后按到账汇率入账时也调它 | E1-2a：读 [#167](https://github.com/TYCT-tyct/Axiom/issues/167) 的参考汇率版本，加上充值缓冲（站点设置，在 billing）；#167 未合并时只开放同币种的付款方式 |
| `billing.CreditPaid(ctx, tx, PaidCredit)` | 锁余额行，写付费额度批次（ID = 订单 ID 或待处理记录 ID，记实付、其中的手续费、入账额度和所用汇率）、"用户充值"分录和审计。同一 ID 再次调用时比较入账额度、实付和手续费：一致就什么也不写，不一致返回错误，调用方转待处理；不用 `ON CONFLICT DO NOTHING`，否则金额不同的第二次调用会被静默丢掉 | E1-1 |
| `billing.FreezeRefund(ctx, tx, Freeze) (int64, error)` | 锁余额行、补算分配、算可退额度，写一笔带序号的冻结事件，再重放吃进它；返回实际冻结的额度 | E1-6a |
| `billing.SettleRefund(ctx, tx, refund)` / `ReleaseRefund(ctx, tx, refund)` | 冻结转成"退款"分录（带 payments 算好的退回金额）/ 写一笔解冻事件。重复调用由 payments 在同一事务里对退款行的条件更新挡住 | E1-6a |

`PaidCredit` 带账户、站点、批次 ID、入账额度、商户账户（现金科目按它区分）、付款币种、实付、其中的手续费、所用的参考汇率和成交汇率（锁定的那组，或过期后的到账汇率）。跨币种时两本账（站点 × 货币）各用汇兑损益配平：付款币种的账借现金（实付）、贷手续费收入（手续费）和汇兑损益（其余）；记账货币的账借汇兑损益、贷用户余额（额度）；两边按参考汇率的差额就是充值缓冲带来的汇兑收益。同币种时没有汇兑损益两行。订单上的 `paid_total`、`paid_fee`、`credited` 与批次出自同一个 `PaidCredit`、同一事务写入，退款金额由 payments 按订单上的这三列和自己的退款记录计算；每日对账核对两边一致。

## 不变量

| 不变量 | 靠什么保证 |
|---|---|
| 一笔订单至多入账一次 | 状态的条件更新（只有待支付、已过期、已取消、失败、待处理能变成已支付）加额度批次以订单 ID 为主键，`CreditPaid` 同 ID 不同金额报错；性质测试随机重放、乱序、并发投递回调与补单，批次恰好一个 |
| 订单是已支付 ⇔ 存在 ID 等于订单 ID 的付费批次 | 两者在同一个事务里写；故障注入让 `CreditPaid` 失败，订单仍是原状态；每日对账核对 |
| 只有事务提交后才回 `Ack(true)` | 处理器在提交返回之后才调 `Ack(true)`，回滚、提交失败调 `Ack(false)`（非 2xx）；故障注入让提交失败，断言应答不是成功、网关会重试 |
| 没有通过验签，回调什么都不写 | 处理器在 `Verify` 返回成功之前不开事务；测试对坏签名、缺签名、多余参数、`pid` 不符逐个断言库里没有新行 |
| 验签通过的成功付款都有记录 | 找不到订单、网关订单号已记在别的订单上，都写待处理记录（按网关订单号去重）后才回成功；测试：两种情况都有一行待处理记录、应答不是 500 |
| 渠道支持查单时，没有查询确认就不入账，入账金额来自查单 | 回调与补单走同一个入账函数，它要求一个 `Payment{State: Paid}` 并用它的 `Paid`；测试：查单失败返回 502 且不入账；通知与查单金额不同时订单转待处理 |
| 一个商户账户的回调只能改它自己的订单 | 订单按（商户账户 ID，订单 ID）查找；测试用 A 的密钥给 B 的订单签名，被拒 |
| 实付与总额按最小单位完全相等才入订单的额度 | `Money` 是整数，比较用 `==`；表驱动测试覆盖多一分、少一分、末尾零、超出精度 |
| 总额可以从订单的列复算 | `total = ceil((credits × applied + fee_fixed) ÷ (1 − fee_ppm/10⁶))`，按 `math/big` 精确算一次；锁定的 `applied_*` 永不覆盖，到账汇率另存在 `arrival_*`；性质测试：复算等于存值，总额 ≥ 充值部分 + 固定手续费，手续费 ≥ 0 |
| 订单状态只按设计表里的边变化 | 转移表是代码里唯一的一份；测试枚举所有（从，到）组合，表外的全部被拒 |
| 限额在锁内检查 | 账户级计数在账户的事务级咨询锁内读、插入；商户当天额度在用量计数行的行锁内读、加；并发测试：最后一点额度或最后一个待支付名额同时被 10 个请求抢，只有放得下的那些成功 |
| 一笔订单的已冻结、发送中、待确认、成功的退款额度之和 ≤ 入账额度 | 退款在订单行锁下插入并检查；数据库 CHECK 管单行，合计由锁和性质测试守 |
| 一笔订单的退款金额之和 ≤ 净付款额，退完时恰好相等 | 金额 = ⌊额度 × 净付款额 ÷ 入账额度⌋，让累计额度达到入账额度的那一笔取余数；在订单行锁下按退款记录算；性质测试：任意汇率序列（锁定、宽限期内、到账汇率）和任意部分退款拆分下，Σ退款 ≤ Σ实付 |
| 退款只按"已冻结 → 发送中 → 成功 / 失败 / 待确认 → 成功 / 失败"变化，转分录、解冻各在一个事务里 | 状态的条件更新与 billing 的调用同事务；"发送中"在调渠道之前单独提交；测试注入渠道超时、渠道失败、提交失败，不出现"扣了没退"或"退了没扣" |
| 渠道不认退款单号时，结果未知的退款从不重发 | 只有 `Caps` 含 `RefundKey` 时，任务才对发送中、待确认的退款再调 `Refund`；测试：第一个事务后崩溃，退款恰好发一次；发送中崩溃，不认单号的渠道一次也不重发、进待处理；同一订单的第二笔退款在第一笔落定前被拒 |
| 一个人不能把未知退款标为失败 | `Resolve` 标失败要求凭证和另一个人的批准；测试：同一人发起并批准被拒；渠道其实已退时，只按凭证落定为成功 |
| 渠道的未知结果从不变成失败 | 只有 `*Rejected` 能让下单变失败、退款变失败；测试把超时、5xx、HTML 响应逐个喂给核心 |
| 加锁顺序：下单是账户咨询锁 → 用量计数行；入账与退款是订单行 → 退款行 → 余额行 | 所有写路径经同一个入口函数；同账户并发的下单、入账与退款测试不死锁 |
| 密钥不离开密文，网关地址不能绕过保存流程改掉 | 密钥只以 `secret.Secret` 在内存里，解封位置 `{payments.merchant_configs, 商户账户 ID, "config"}`；密文里同时封着 `settings` 的 SHA-256，取用时不一致就拒绝并告警；测试：直接改库里的网关地址后这个商户账户用不了；扫描日志、链路、订单事件、审计和接口响应，找不到密钥，也找不到出站请求的查询串 |
| 账户范围读不到商户配置和密文 | `merchant_configs` 只对站点和平台范围开放；行级安全测试：账户范围读得到付款方式与费率，读 `merchant_configs` 得 0 行 |
| 支付链接只有白名单协议 | 核心统一检查 `Checkout`；表驱动测试：`//evil.com/x`、`javascript:`、`data:`、`http:` 被拒，`/pay?x` 补全后放行 |
| 选不出商户账户就不下单 | 过滤后为空、查用量出错都返回错误，不退回全部候选；表驱动测试 |

## 数据

schema `payments`，迁移在 `migrations/`，查询在 `queries/`。每张表 ENABLE 并 FORCE 行级安全（[平台的「范围」](../platform/README.md#范围)）。

| 表 | 关键列 | 约束与索引 | 类别与策略 |
|---|---|---|---|
| `merchants` | `id`、`site_id`、`kind`、`name`、`enabled`、`currency`、`ttl`、`mode`、`refund_enabled`、`user_refund`、`sort`、`rev` | `currency ~ '^[A-Z]{3}$'`；`ttl` 在 1 分钟到 24 小时；`(site_id, sort)`；不删除，只停用 | 非密的站点数据：读 `in_any_scope()`（账户范围的充值页要读），查询一律带 `site_id`；写 `in_scope(site_id, NULL)`；主站行只有平台范围能改，由管理接口的控制面守卫 |
| `merchant_configs` | `merchant_id` 主键、`site_id`、`settings`（非密配置，规范 JSON，供后台展示）、`ciphertext`/`nonce`/`wrapped_dek`/`key_version`（密钥、轮换中的旧密钥、`settings` 的 SHA-256）、`rev` | 改 `settings` 必须同时重新封装 | 站点数据：`in_scope(site_id, NULL)` 读写，账户范围读不到 |
| `merchant_methods` | `merchant_id`、`site_id`、`method`、`upstream`、`fee_ppm`、`fee_fixed`、`single_min`、`single_max`、`daily_limit`、`enabled` | 主键 `(merchant_id, method)`；`fee_ppm` 0…500000；`fee_fixed ≥ 0`；`single_min ≤ single_max`（都设了时）；后三项是付款币种的总额（最小单位） | 同 `merchants` |
| `merchant_usage` | `merchant_id`、`site_id`、`day`（UTC 日）、`used`（付款币种的最小单位） | 主键 `(merchant_id, day)`；`used ≥ 0` | 例外：读写都是 `in_any_scope()`。账户范围的下单事务要锁它、加它，却读不到别的账户的订单；行里只有一个数字，没有租户的内容，错了最多让限额失准，每日对账按订单复算核对 |
| `settings` | `site_id` 主键，加「后台配置」表里站点级的各项 | 各项范围 CHECK；没有行就用代码里的默认值 | 同 `merchants` |
| `orders` | `id`、`site_id`、`account_id`、`user_id`、`merchant_id`、`method`、`state`、`idem_key`、`fingerprint`、`credits`、`site_currency`、`pay_currency`、`total`、`fee`、`fee_ppm`、`fee_fixed`、`rate_id`、`ref_num`/`ref_den`、`applied_num`/`applied_den`、`arrival_rate_id`、`arrival_ref_num`/`arrival_ref_den`、`arrival_num`/`arrival_den`、`trade_no`（下单时网关给的，可空）、`checkout`（支付链接或二维码，不含 `ClientSecret`）、`expires_at`、`paid_total`、`paid_fee`、`credited`、`paid_at`、`next_query_at`（E1-5 加）、`client_ip`、`created_at`、`updated_at` | `credits > 0`；`total > 0`；`0 ≤ fee ≤ total`；`(rate_id IS NULL) = (pay_currency = site_currency)`；`arrival_*` 同空同非空；`state` 枚举；`state = 'paid' → paid_at, paid_total, paid_fee, credited` 非空、`credited > 0`、`0 ≤ paid_fee ≤ paid_total`；唯一 `(account_id, idem_key)`；`(merchant_id, trade_no)` 普通索引；部分索引 `(expires_at) WHERE state = 'pending'`；`(account_id, created_at DESC)`；`(merchant_id, created_at)` 供对账复算用量 | 账户数据：`in_scope(site_id, account_id)` |
| `order_payments` | `id`、`order_id`、`site_id`、`account_id`、`merchant_id`、`trade_no`、`amount`、`currency`、`source`（notify、query、admin）、`at` | 唯一 `(merchant_id, trade_no)`，冲突时进待处理记录、不报错；`amount > 0`；`(order_id)` | 账户数据 |
| `order_events` | `id`、`order_id`、`site_id`、`account_id`、`at`、`source`（notify、query、user、admin、job）、`kind`、`from_state`、`to_state`、`trade_no`、`amount`、`raw`（截到 8 KiB） | 只插入：`axiom_app` 只有 INSERT、SELECT；`(order_id, at)` | 账户数据 |
| `refunds` | `id`、`order_id`、`site_id`、`account_id`、`state`（frozen、sending、pending、succeeded、failed）、`via`（channel、offline）、`credits`、`amount`、`with_fee`、`provider_ref`、`reason`、`evidence`、`requested_by`、`approved_by`、`idem_key`、`attempts`、`sent_at`、`next_query_at`、`created_at`、`finished_at` | `credits > 0`；`amount > 0`；`state IN ('sending','pending') → sent_at` 非空；唯一 `(order_id, idem_key)`；部分索引 `(next_query_at) WHERE state IN ('frozen','sending','pending')` | 账户数据 |
| `discrepancies` | `id`、`site_id`、`account_id`（可空）、`order_id`（可空）、`merchant_id`、`kind`（amount_mismatch、multiple_payment、duplicate_payment、unmatched_payment、trade_no_conflict、statement、refund_unknown、ledger、usage）、`dedupe`（同一事实的键：网关订单号、退款 ID 或对账日期）、`detail`、`state`（open、resolved）、`resolution`、`evidence`、`resolved_by`、`approved_by`、`created_at`、`resolved_at` | 唯一 `(merchant_id, kind, dedupe)`，同一事实重复上报只留一行；`(site_id, state, created_at)` | 站点数据，带账户时同账户数据 |

- **对外 ID**：订单 ID 的对外写法就是发给渠道的商户订单号，`ord_` 加 26 个字符。
- **另行入账**：人工把一条待处理的付款（重复付款、有付款没订单）入到某个账户时，额度批次的 ID 用这条待处理记录的 ID，重复提交不会入两次。
- **账户删除**：订单、退款、订单事件与账本同寿命，不随账户删除（保留期与审计日志一起由负责人定）。
- **没有外键**：`merchant_id`、`order_id` 只在本模块内引用；账户、站点、汇率版本只按 ID 引用（[总体架构的规则 1](../../docs/architecture.md#模块边界规则)）。
- **对账单**：第一个声明 `Statements` 能力的插件随它加 `statement_lines` 表；易支付没有账单接口，E1-5 不建这张表。

## 事务与任务

- **下单**：事务 1（账户范围）取账户的事务级咨询锁（`pg_advisory_xact_lock`，键由账户 ID 派生）→ 读设置和这个账户的计数 → 选商户账户 → `INSERT … ON CONFLICT DO UPDATE` 锁住并加上它当天的用量计数 → `TopUpRate` → 算总额 → 插入订单和事件。提交后调 `Create`；事务 2 检查链接，写回 `trade_no` 和 `checkout`。`Create` 返回 `*Rejected` 或链接不合格时事务 2 改为失败并减回用量；未知错误不改，返回 502 给用户，订单由补单和过期收尾。
- **回调**：限流 → 取商户配置（内存缓存，按 `rev` 失效）→ `Verify` → 不加锁读订单，已支付且网关订单号已记下就直接应答 → 查单 → 入账事务 → 提交后 `Ack(true)`。
- **入账**（回调、补单、"我已付款"、人工）：平台范围的一个事务，`SELECT … FOR UPDATE` 订单行 → 插入 `order_payments`（冲突写待处理记录）→ 条件转移 → `CreditPaid` → 事件；过期后到账的同时加回用量计数。事务里不做网络调用。
- **退款**：事务 1 锁订单行、检查在途的退款、`FreezeRefund`、算金额、插入退款行（已冻结）；事务 2 置发送中、写 `sent_at`；调渠道；事务 3 锁订单行、退款行，`SettleRefund` 或 `ReleaseRefund`，更新订单。
- **`payments.poll`**（每分钟，队列的周期任务，只有领导节点插入）：一轮有时间预算，按 `next_query_at` 依次处理 (1) 待支付订单：查单；(2) 已过有效期的待支付订单：查单、关单、过期、减回用量；(3) 渠道不能关单的已过期、已取消订单：在补查窗口内按退避查单；(4) 停在已冻结超过一分钟的退款：置发送中再发；(5) 发送中、待确认的退款：声明 `RefundKey` 的用同一单号重发或查退款，其余只查退款，超过 24 小时记待处理记录。每次查询后按退避写下一次的 `next_query_at`；同一商户账户同时最多 2 个调用，连续 5 次未知就暂停这个账户 5 分钟并告警。失败的不挡后面的（与 billing 的到期任务同一做法）。
- **`payments.reconcile`**（每天）：每个商户账户的现金分录合计对订单与退款；用量计数对按订单复算的值；订单的 `paid_total`、`paid_fee`、`credited` 对批次；声明 `Statements` 的拉账单逐笔比对，含"网关有、订单无"；其余把前一天非终态和有变化的订单、补查窗口内的已过期和已取消订单再查一次。差额写 `discrepancies` 并告警。
- **范围**：回调和两个任务用 `cmd/axiom` 交来的平台范围；控制台用 accounts 交出的账户范围，站长后台用站点范围。

## 决策与理由

| 决策 | 理由 | 否决的做法 |
|---|---|---|
| 订单、批次、分录一个事务，没有"充值中" | 入账与改状态同成同败，重复回调只是一次不改行的条件更新 | sub2api 的已支付 → 充值中 → 已完成加 5 分钟租约（`service/payment_fulfillment.go:314-363`）：只因它的加余额不在同一事务里才需要 |
| 退款独立成表，先冻结、单独提交"发送中"、再调渠道；只对声明 `RefundKey` 的渠道重发 | 一笔订单多次部分退款；渠道失败只解冻；"发送中"让崩溃后知道哪些一定没发 | 退款状态挂在订单上（sub2api `payment/types.go:35-40`）：同时只能有一个退款；先扣余额再调渠道（`service/payment_refund.go:315-330`）：失败要回滚扣减，回滚还可能失败；对所有渠道按超时重发：易支付没有退款单号，会退两次 |
| 退款金额按订单上的实付、手续费、入账额度之比算，最后一笔取余数 | 入账可能按实付或到账汇率折算，比例跟着实际入账走才不会多退；取余数让全额退款没有舍入误差 | 按订单报价的比例（sub2api `service/payment_amounts.go:35-48`）：换汇率入账后多退 |
| 金额是最小单位的整数，汇率是精确分数，总额只取整一次 | 负责人要求；相等比较不会把少付当作付清 | `decimal(20,2)` 加 `float64` 和 0.01 容差（sub2api `ent/schema/payment_order.go:46-56`、`service/payment_service.go:47`） |
| 一个 `Channel` 接口加 `Caps` | 易支付的能力取决于网关，是配置而不是插件种类 | 每种能力一个可选接口（sub2api）：同一个易支付插件要么全有、要么全无 |
| 插件编译进程序，易支付做成通用协议加配置 | 不同网关只差配置；代码审得到、测得到 | 动态插件或脚本：多一种运行时，签名逻辑跑在审不到的地方；每家网关一个插件：重复 |
| 商户订单号就是订单 ID | 唯一、30 个字符、只有字母数字下划线 | 前缀加日期加随机数再查重（sub2api `service/payment_order.go:238-251`） |
| 非密的商户信息、付款方式、站点设置给任何范围读；密文和网关配置单独一张表，只给站点和平台范围 | 账户范围的充值页要读付款方式和限额；行级安全按行不按列，密文必须在另一张表才读不到 | 整表 `in_scope(site_id, NULL)`：账户范围读到 0 行，充值页没法用；整表放宽：用户读得到密文 |
| 密钥和 `settings` 的哈希封在同一份密文里，`settings` 明文存放 | 网关地址要能在后台展示，又不能被单独改掉；平台 `secret` 包只按表、行、用途绑定，不收调用方的附加数据 | 整份配置明文（sub2api 现状，`payment/load_balancer.go:317-347`）；把哈希放进绑定的位置：要改 `secret` 包的存储格式 |
| 商户当天额度用一行计数守 | 账户范围读不到别的账户的订单，也锁不了商户行（加锁要过 UPDATE 策略）；一行计数既是读数又是串行点 | 每次按订单求和（sub2api `load_balancer.go:167-246`）：账户范围求不出，读后写也挡不住并发；咨询锁加平台范围求和：下单事务就得用平台范围 |
| 回调地址带商户账户 ID，按它找配置 | 验签前不用解析请求体去猜订单（sub2api `handler/payment_webhook_handler.go:155-179` 先解析 `out_trade_no` 再找商户），同一渠道多个商户账户时没有歧义 | 每种渠道一个回调地址加"只有一个账户时才允许回退"（`service/payment_webhook_provider.go:96-113`） |
| 回调与任务用平台范围 | 回调没有操作者；`CreditPaid` 写 billing 的表，跨模块授权会打破"只写自己的表" | 每个站点一个任务角色 |
| USDT 走易支付兼容网关 | 不写第二个插件、不保管私钥；链上细节由网关处理 | 自建链上监听：重组、确认数、私钥都要自己做 |

## 实现拆分

按顺序；每个 PR 交付一件能单独评审、回滚的事，非生成代码不超过约 2000 行，超了按能力边界再拆。表的迁移随第一个用到它的 PR。E1-1（billing 的付费额度批次，`CreditPaid`）是 E1-2a 的前提；E1-2b 之后 E1-3、E1-4、E1-5 可以并行；E1-6a 改 billing 的写路径，不与其他改 billing 写路径的 PR 同时开，E1-6b 在它之后。

| PR | 内容 | 验收测试 |
|---|---|---|
| E1-2a `feat(payments): configure merchant accounts and quote top-ups` | 表 `merchants`、`merchant_configs`、`merchant_methods`、`merchant_usage`、`settings` 和迁移；`Channel`、`Plugin`、`Caps`、登记、测试用的假渠道；商户账户的保存（校验、密钥与 `settings` 哈希的封装）；选商户账户与用量计数；手续费与汇率锁定（含 billing 的 `TopUpRate` 与充值缓冲）；`Options`；管理接口的商户账户契约 | 总额复算与手续费反推的性质测试；比例超过 50%、额度乘汇率越界被拒；选商户账户的单笔、当天额度、全被过滤和查用量出错都拒绝；并发抢最后的当天额度只放行放得下的；直接改库里的网关地址后商户账户用不了；密钥不出现在日志和响应里；行级安全：账户范围读得到付款方式和费率、读不到 `merchant_configs`，站长只看到本站的商户账户 |
| E1-2b `feat(payments): take orders and credit them from callbacks` | 表 `orders`、`order_payments`、`order_events`、`discrepancies` 和迁移；转移表；下单（账户锁与计数上限、链接检查、服务端生成的返回地址）、取消、"我已付款"；回调处理器（限流、配置缓存、无锁预读、查单金额为准、找不到订单与网关订单号冲突记待处理、提交后才应答）和入账；过期（`payments.poll` 的第 2 步）与过期后到账（宽限期、到账汇率、自动入账期限）；有未完成订单时拒改商户身份字段；控制台的订单契约 | 转移表的全部（从，到）组合；性质测试：重复、乱序、并发、丢失回调下批次恰好一个、账本平衡；金额不符、多笔付款、折算为 0 进待处理，币种不符不入账；通知与查单金额不同进待处理；验签通过找不到订单、网关订单号冲突都有待处理记录且不回 500；坏签名不写库；查单失败返回 502；提交失败时应答不是成功（故障注入）；宽限期内按锁定汇率、之后按到账汇率、超过期限进待处理，锁定的汇率列不变；10 个并发下单不突破待支付上限和每天上限；`//`、`javascript:`、`data:` 链接被拒；行级安全：账户只看到自己的订单 |
| E1-3 `feat(payments): add the easypay protocol channel` | 易支付插件：配置项（[支付的「易支付协议插件」](../../docs/domains/payments.md#易支付协议插件)）、跳转与接口两种下单、验签、白名单、查单确认、关单、退款 | 按协议自写的签名向量；参数走私（下单参数里嵌 `trade_status`）被拒；多余参数、`pid` 不符被拒；GET 与 POST 回调；PHP 风格的字符串整数、明确失败码优先、回显订单号不符；相对支付链接补全、`//` 开头被拒；退款响应是 HTML、空、字符串 `code`；退款先按订单号、明确"订单不存在"才按网关订单号；全部经出站客户端，日志和链路里没有查询串；本机假网关的端到端 |
| E1-4 `feat(payments): take USDT through an easypay-compatible gateway` | Epusdt 作为易支付商户账户的接入与预设：`usdt.tron` 映射、数字 `pid`、法币金额；退款只能线下；接入前实测它的查单接口 | 用 Epusdt 官方镜像在本地部署后的下单、GET 回调、查单确认、入账；`money=100.0000` 精确解析；查单不可用时这个商户账户保存失败 |
| E1-5 `feat(payments): reconcile unfinished orders and daily channel statements` | `orders.next_query_at` 的迁移；`payments.poll` 的补单（每单退避、每商户并发上限与熔断）与过期后补查；`payments.reconcile`；待处理记录的管理页与告警 | 丢回调由补单入账；一笔订单在有效期内的查询次数符合退避；一个商户账户超时不挡其他账户，连续未知触发暂停和告警；不能关单的渠道过期后付款，在补查窗口内补到；现金分录与订单不符、用量计数与订单复算不符时出现待处理记录并告警；同一差额重复上报只留一行 |
| E1-6a `feat(billing): freeze paid credit as replayed events` | billing：冻结、解冻、退款三种带序号的交易，批次的冻结状态，重放、准入、到期都按冻结额处理；`FreezeRefund`、`SettleRefund`、`ReleaseRefund`（[billing 的「额度批次的分配与到期」](../billing/README.md#额度批次的分配与到期)） | 随机冻结、解冻、退款、扣费、到期，从头重放等于现场，且从任意位置续放相同（扩展 `TestReplayProperty`）；冻结期间并发调用花不掉冻结的额度；到期不作废冻结的额度；冻结后余额底线以下的扣费不动冻结额度；billing 不变量①–⑦（⑦ 随本 PR 加入）在冻结后成立 |
| E1-6b `feat(payments): refund through channels with a sending state` | 表 `refunds` 和迁移；发起、审批、发送中、查询、人工结论（凭证、第二人批准）；`payments.poll` 的第 4、5 步；订单状态按成功退款合计 | 渠道超时 → 待确认 → 查询收敛到成功或失败；第一个事务后崩溃只发一次；发送中崩溃时不认单号的渠道不重发、进待处理，认单号的用同一单号重发；不认单号的渠道同一订单只有一笔在途；同一人不能把未知退款标为失败；同一幂等键只退一次；多次部分退款合计不超过入账额度；性质测试：任意汇率序列和拆分下 Σ退款 ≤ Σ实付，全退恰好等于净付款额；退不退手续费两种策略；失败在一个事务里解冻 |

**上线门槛**：主站打开"是否开放充值"之前，E1-3 在负责人提供的真实网关上做小额实测（下单、回调、查单确认、关单、退款、手续费的舍入、币种配置），结果记在 [#309](https://github.com/TYCT-tyct/Axiom/issues/309)；没有这份记录不打开。
