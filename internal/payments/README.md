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
func (p *Payments) Refund(ctx context.Context, s pg.Scope, r RefundRequest) (Refund, error)              // 冻结 → 渠道 → 落定；r.Key 幂等
func (p *Payments) Resolve(ctx context.Context, s pg.Scope, r Resolution) error                          // 待处理订单、未知退款的人工结论，写理由
func (p *Payments) SaveMerchant(ctx context.Context, tx pg.Tx, m MerchantInput) (Merchant, error)        // 调 Plugin.Open 校验后封装密钥
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
	Settings []byte                    // 非密配置，规范 JSON，插件自己定义结构并严格解析
	Secrets  map[string]secret.Secret  // 解封后的密钥，只在内存里
	Client   *egress.Client
}
type Channel interface {
	Caps() Caps // Query | Close | Refund | QueryRefund | Statements；易支付按商户配置声明
	Methods() []Method
	Create(ctx context.Context, r CreateRequest) (Checkout, error)
	Verify(ctx context.Context, c Callback) (Notice, error)      // 验签加商户身份；不相关的事件返回 Notice{Relevant: false}
	Query(ctx context.Context, order OrderID, tradeNo string) (Payment, error)
	Close(ctx context.Context, order OrderID, tradeNo string) error
	Refund(ctx context.Context, r RefundCall) (RefundState, error)
	QueryRefund(ctx context.Context, r RefundCall) (RefundState, error)
	Ack(ok bool) (status int, body []byte)                       // 渠道要求的应答，sub2api handler/payment_webhook_handler.go:215-226
}
type CreateRequest struct{ Order OrderID; Method Method; Total Money; Subject, ReturnURL string; ClientIP netip.Addr; Mobile bool }
type Checkout struct{ TradeNo, PayURL, QRCode, ClientSecret string } // 存库前去掉 NUL（sub2api payment_order.go:509-523）
type Callback struct{ Method string; RawQuery string; Body []byte; Header http.Header }
type Notice struct{ Relevant, Success bool; Order, TradeNo string; Paid Money; Raw string }
type Payment struct{ State PayState; TradeNo string; Paid Money } // Pending | Paid | Closed
type RefundCall struct{ Refund RefundID; Order OrderID; TradeNo, ProviderRef string; Amount Money; Reason string }
type RefundState struct{ State RefundOutcome; ProviderRef string } // Succeeded | Failed | Pending
type Rejected struct{ Code, Message string }                      // 渠道明确拒绝；实现 error
```

- **错误只分两种**：`*Rejected` 是确定的失败（下单被拒、退款被拒）；其他任何错误都是"结果未知"，调用方不得据此改订单或退款为失败。超时、断连、5xx、HTML 或解析不了的响应都是未知。
- **用能力集合而不是可选接口**：sub2api 用类型断言判断扩展接口（`payment_fulfillment.go:65-69`），能力随插件种类固定；易支付是否能查单、能退款取决于这家网关，是商户账户的配置，所以由 `Caps` 声明。调用没有声明的能力是程序错误，panic。
- **金额**：`Notice.Paid`、`Payment.Paid` 由插件把网关的十进制文本精确解析成最小单位（`platform` 的币种表），位数超出就当作金额不符，交给核心比较；插件里不做容差。
- **插件拿不到数据库**：只拿到配置、密钥和出站客户端；状态和钱都在核心里。

### billing 要提供的接口

payments 引用 billing，在自己的事务里调用（[总体架构的规则 5](../../docs/architecture.md#模块边界规则)：数据的所有方提供接受调用方事务的函数）：

| 函数 | 用途 | 由谁实现 |
|---|---|---|
| `billing.TopUpRate(ctx, tx, from, to platform.Currency) (Rate, error)` | 当前参考汇率版本（ID、分子、分母）与充值方向的缓冲，订单据此锁定 | [#167](https://github.com/TYCT-tyct/Axiom/issues/167) 的汇率加上充值缓冲，E1-2 之前或之中 |
| `billing.CreditPaid(ctx, tx, PaidCredit)` | 锁余额行，写付费额度批次（ID = 订单 ID）、"用户充值"分录和审计；同一 ID 重复无害 | E1-1 |
| `billing.FreezeRefund(ctx, tx, Freeze) (int64, error)` | 锁余额行、补算分配、算可退额度并冻结；返回实际冻结的额度 | E1-6 |
| `billing.SettleRefund(ctx, tx, refund)` / `ReleaseRefund(ctx, tx, refund)` | 冻结转成"退款"分录 / 解冻；以退款 ID 幂等 | E1-6 |

`PaidCredit` 带账户、站点、额度、商户账户（现金科目按它区分）、付款币种、总额、手续费、锁定的参考汇率和成交汇率。跨币种时两本账（站点 × 货币）各用汇兑损益配平：付款币种的账借现金（总额）、贷手续费收入（手续费）和汇兑损益（充值部分）；记账货币的账借汇兑损益、贷用户余额（额度）；两边按参考汇率的差额就是充值缓冲带来的汇兑收益。同币种时没有汇兑损益两行。

## 不变量

| 不变量 | 靠什么保证 |
|---|---|
| 一笔订单至多入账一次 | 状态的条件更新（只有待支付、已过期、已取消、失败、待处理能变成已支付）加额度批次以订单 ID 为主键；性质测试随机重放、乱序、并发投递回调与补单，批次恰好一个 |
| 订单是已支付 ⇔ 存在 ID 等于订单 ID 的付费批次 | 两者在同一个事务里写；故障注入让 `CreditPaid` 失败，订单仍是原状态；每日对账核对 |
| 没有通过验签，回调什么都不写 | 处理器在 `Verify` 返回成功之前不开事务；测试对坏签名、缺签名、多余参数、`pid` 不符逐个断言库里没有新行 |
| 渠道支持查单时，没有查询确认就不入账 | 回调与补单走同一个入账函数，它要求一个 `Payment{State: Paid}`；测试：查单失败返回 502 且不入账 |
| 一个商户账户的回调只能改它自己的订单 | 订单按（商户账户 ID，订单 ID）查找；测试用 A 的密钥给 B 的订单签名，被拒 |
| 实付与总额按最小单位完全相等才入订单的额度 | `Money` 是整数，比较用 `==`；表驱动测试覆盖多一分、少一分、末尾零、超出精度 |
| 总额可以从订单的列复算 | `total = ceil((credits × applied + fee_fixed) ÷ (1 − fee_ppm/10⁶))`，按 `math/big` 精确算一次；性质测试：复算等于存值，总额 ≥ 充值部分 + 固定手续费，手续费 ≥ 0 |
| 订单状态只按设计表里的边变化 | 转移表是代码里唯一的一份；测试枚举所有（从，到）组合，表外的全部被拒 |
| 一笔订单的冻结、待确认、成功的退款额度之和 ≤ 入账额度 | 退款在订单行锁下插入并检查；数据库 CHECK 管单行，合计由锁和性质测试守 |
| 退款只从"已冻结 / 待确认"走到"成功"（转分录）或"失败"（解冻），各在一个事务里 | 状态的条件更新与 billing 的调用同事务；测试注入渠道超时、渠道失败、提交失败，不出现"扣了没退"或"退了没扣" |
| 渠道的未知结果从不变成失败 | 只有 `*Rejected` 能让下单变失败、退款变失败；测试把超时、5xx、HTML 响应逐个喂给核心 |
| 加锁顺序：订单行 → 退款行 → 余额行 | 所有写路径经同一个入口函数；同账户并发的入账与退款测试不死锁 |
| 密钥不离开密文 | 密钥只以 `secret.Secret` 在内存里，解封位置 `{payments.merchants, 行 ID, "config"}`；测试扫描日志、订单事件、审计和接口响应，找不到密钥 |
| 选不出商户账户就不下单 | 过滤后为空、查用量出错都返回错误，不退回全部候选；表驱动测试 |

## 数据

schema `payments`，迁移在 `migrations/`，查询在 `queries/`。每张表 ENABLE 并 FORCE 行级安全（[平台的「范围」](../platform/README.md#范围)）。

| 表 | 关键列 | 约束与索引 | 类别与策略 |
|---|---|---|---|
| `merchants` | `id`、`site_id`、`kind`、`name`、`enabled`、`currency`、`ttl`、`mode`、`settings`（非密配置，规范 JSON）、`ciphertext`/`nonce`/`wrapped_dek`/`key_version`、`refund_enabled`、`user_refund`、`sort`、`rev` | `currency ~ '^[A-Z]{3}$'`；`ttl` 在 1 分钟到 24 小时；`(site_id, sort)` | 站点数据：`in_scope(site_id, NULL)` 读写；主站行只有平台范围能改，由管理接口的控制面守卫 |
| `merchant_methods` | `merchant_id`、`site_id`、`method`、`upstream`、`fee_ppm`、`fee_fixed`、`single_min`、`single_max`、`daily_limit`、`enabled` | 主键 `(merchant_id, method)`；`fee_ppm` 0…999999；`fee_fixed ≥ 0`；`single_min ≤ single_max`（都设了时） | 站点数据 |
| `settings` | `site_id` 主键，加「后台配置」表里站点级的各项 | 各项范围 CHECK；没有行就用代码里的默认值 | 站点数据 |
| `orders` | `id`、`site_id`、`account_id`、`user_id`、`merchant_id`、`method`、`state`、`idem_key`、`fingerprint`、`credits`、`site_currency`、`pay_currency`、`total`、`fee`、`fee_ppm`、`fee_fixed`、`rate_id`、`ref_num`/`ref_den`、`applied_num`/`applied_den`、`trade_no`、`checkout`、`expires_at`、`paid_total`、`credited`、`paid_at`、`client_ip`、`created_at`、`updated_at` | `credits > 0`；`total > 0`；`0 ≤ fee ≤ total`；`(rate_id IS NULL) = (pay_currency = site_currency)`；`state` 枚举；`state = 'paid' → paid_at, paid_total, credited` 非空；唯一 `(account_id, idem_key)`；唯一 `(merchant_id, trade_no)`（非空时）；部分索引 `(expires_at) WHERE state = 'pending'`；`(account_id, created_at DESC)`；`(merchant_id, created_at) WHERE state IN ('pending','paid','partially_refunded','refunded','review')` 供当天用量 | 账户数据：`in_scope(site_id, account_id)` |
| `order_events` | `id`、`order_id`、`site_id`、`account_id`、`at`、`source`（notify、query、user、admin、job）、`kind`、`from_state`、`to_state`、`trade_no`、`amount`、`raw`（截到 8 KiB） | 只插入：`axiom_app` 只有 INSERT、SELECT；`(order_id, at)` | 账户数据 |
| `refunds` | `id`、`order_id`、`site_id`、`account_id`、`state`（frozen、pending、succeeded、failed）、`via`（channel、offline）、`credits`、`amount`、`with_fee`、`provider_ref`、`reason`、`requested_by`、`idem_key`、`attempts`、`next_query_at`、`created_at`、`finished_at` | `credits > 0`；`amount > 0`；唯一 `(order_id, idem_key)`；部分索引 `(next_query_at) WHERE state IN ('frozen','pending')` | 账户数据 |
| `discrepancies` | `id`、`site_id`、`account_id`（可空）、`order_id`（可空）、`merchant_id`、`kind`（amount_mismatch、duplicate_payment、statement、refund_unknown、ledger）、`detail`、`state`（open、resolved）、`resolution`、`resolved_by`、`created_at`、`resolved_at` | 唯一 `(merchant_id, kind, dedupe)`，同一事实重复上报只留一行；`(site_id, state, created_at)` | 站点数据，带账户时同账户数据 |

- **对外 ID**：订单 ID 的对外写法就是发给渠道的商户订单号，`ord_` 加 26 个字符。
- **账户删除**：订单、退款、订单事件与账本同寿命，不随账户删除（保留期与审计日志一起由负责人定）。
- **没有外键**：`merchant_id`、`order_id` 只在本模块内引用；账户、站点、汇率版本只按 ID 引用（[总体架构的规则 1](../../docs/architecture.md#模块边界规则)）。
- **对账单**：第一个声明 `Statements` 能力的插件随它加 `statement_lines` 表；易支付没有账单接口，E1-5 不建这张表。

## 事务与任务

- **下单**：事务 1（账户范围）读设置、选商户账户、`TopUpRate`、算总额、插入订单和事件；提交后调 `Create`；事务 2 写回 `trade_no` 和 `checkout`。`Create` 返回 `*Rejected` 时事务 2 改为失败；未知错误不改，返回 502 给用户，订单由补单和过期收尾。
- **入账**（回调、补单、"我已付款"、人工）：平台范围的一个事务，`SELECT … FOR UPDATE` 订单行 → 条件转移 → `CreditPaid` → 事件。回调在验签和查询确认之后才开事务，事务里不做网络调用。
- **退款**：事务 1 锁订单行、插入退款行、`FreezeRefund`；提交后调渠道；事务 2 锁订单行、退款行，`SettleRefund` 或 `ReleaseRefund`，更新订单。
- **`payments.poll`**（每分钟，队列的周期任务，只有领导节点插入）：一轮有时间预算，依次处理 (1) 创建超过 30 秒、未过期的待支付订单：查单；(2) 已过有效期的待支付订单：查单、关单、过期；(3) 到了 `next_query_at` 的待确认退款：查退款，按指数退避排下一次，超过 24 小时记待处理记录。游标按 ID 前进，下一轮接着走，失败的不挡后面的（与 billing 的到期任务同一做法）。
- **`payments.reconcile`**（每天）：每个商户账户的现金分录合计对订单与退款；声明 `Statements` 的拉账单逐笔比对；其余把前一天非终态和有变化的订单再查一次。差额写 `discrepancies` 并告警。
- **范围**：回调和两个任务用 `cmd/axiom` 交来的平台范围；控制台用 accounts 交出的账户范围，站长后台用站点范围。

## 决策与理由

| 决策 | 理由 | 否决的做法 |
|---|---|---|
| 订单、批次、分录一个事务，没有"充值中" | 入账与改状态同成同败，重复回调只是一次不改行的条件更新 | sub2api 的已支付 → 充值中 → 已完成加 5 分钟租约（`service/payment_fulfillment.go:314-363`）：只因它的加余额不在同一事务里才需要 |
| 退款独立成表，先冻结后调渠道 | 一笔订单多次部分退款；渠道失败只解冻 | 退款状态挂在订单上（sub2api `payment/types.go:35-40`）：同时只能有一个退款；先扣余额再调渠道（`service/payment_refund.go:315-330`）：失败要回滚扣减，回滚还可能失败 |
| 金额是最小单位的整数，汇率是精确分数，总额只取整一次 | 负责人要求；相等比较不会把少付当作付清 | `decimal(20,2)` 加 `float64` 和 0.01 容差（sub2api `ent/schema/payment_order.go:46-56`、`service/payment_service.go:47`） |
| 一个 `Channel` 接口加 `Caps` | 易支付的能力取决于网关，是配置而不是插件种类 | 每种能力一个可选接口（sub2api）：同一个易支付插件要么全有、要么全无 |
| 插件编译进程序，易支付做成通用协议加配置 | 不同网关只差配置；代码审得到、测得到 | 动态插件或脚本：多一种运行时，签名逻辑跑在审不到的地方；每家网关一个插件：重复 |
| 商户订单号就是订单 ID | 唯一、30 个字符、只有字母数字下划线 | 前缀加日期加随机数再查重（sub2api `service/payment_order.go:238-251`） |
| 商户配置信封加密，非密部分是规范 JSON 列 | 安全基线；非密部分可以查询和展示 | 整份配置明文 JSON（sub2api 现状，`payment/load_balancer.go:317-347`） |
| 回调地址带商户账户 ID，按它找配置 | 验签前不用解析请求体去猜订单（sub2api `handler/payment_webhook_handler.go:155-179` 先解析 `out_trade_no` 再找商户），同一渠道多个商户账户时没有歧义 | 每种渠道一个回调地址加"只有一个账户时才允许回退"（`service/payment_webhook_provider.go:96-113`） |
| 回调与任务用平台范围 | 回调没有操作者；`CreditPaid` 写 billing 的表，跨模块授权会打破"只写自己的表" | 每个站点一个任务角色 |
| USDT 走易支付兼容网关 | 不写第二个插件、不保管私钥；链上细节由网关处理 | 自建链上监听：重组、确认数、私钥都要自己做 |

## 实现拆分

按顺序；每个 PR 交付一件能单独评审、回滚的事。E1-1（billing 的付费额度批次，`CreditPaid`）是 E1-2 的前提；E1-3 之后 E1-4、E1-5、E1-6 可以并行，E1-6 与 E1-1 都改 billing 的写路径，不同时开。

| PR | 内容 | 验收测试 |
|---|---|---|
| E1-2 `feat(payments): persist orders and a channel interface` | 本文的表（对账单除外）和迁移；转移表；`Channel`、`Plugin`、登记；选商户账户；手续费与汇率锁定；下单、取消、"我已付款"、回调处理器、入账；过期（`payments.poll` 的第 2 步）；商户账户的保存与密钥封装；控制台与管理接口的契约；一个测试用的假渠道 | 转移表的全部（从，到）组合；性质测试：重复、乱序、并发、丢失回调下批次恰好一个、账本平衡；金额不符进待处理、币种不符不入账；总额复算与手续费反推；选商户账户的单笔、当天额度（含待支付）、全被过滤和查用量出错都拒绝；坏签名不写库；查单失败返回 502；密钥不出现在日志、事件、审计和响应里；行级安全：账户只看到自己的订单，站长只看到本站的商户账户 |
| E1-3 `feat(payments): add the easypay protocol channel` | 易支付插件：配置项（[支付的「易支付协议插件」](../../docs/domains/payments.md#易支付协议插件)）、跳转与接口两种下单、验签、白名单、查单确认、关单、退款 | 按协议自写的签名向量；参数走私（下单参数里嵌 `trade_status`）被拒；多余参数、`pid` 不符被拒；GET 与 POST 回调；PHP 风格的字符串整数、明确失败码优先、回显订单号不符；相对支付链接补全；退款响应是 HTML、空、字符串 `code`；全部经出站客户端；本机假网关的端到端；负责人提供商户后在真实网关小额实测 |
| E1-4 `feat(payments): take USDT through an easypay-compatible gateway` | Epusdt 作为易支付商户账户的接入与预设：`usdt.tron` 映射、数字 `pid`、法币金额；退款只能线下；接入前实测它的查单接口 | 用 Epusdt 官方镜像在本地部署后的下单、GET 回调、查单确认、入账；`money=100.0000` 精确解析；查单不可用时这个商户账户保存失败 |
| E1-5 `feat(payments): reconcile unfinished orders and daily channel statements` | `payments.poll` 的补单与退款查询部分；`payments.reconcile`；待处理记录的管理页与告警 | 丢回调由补单入账；过期宽限期内到账按锁定汇率、之后按到账时汇率；补单积压时游标推进、坏订单不挡后面；现金分录与订单不符时出现待处理记录并告警；同一差额重复上报只留一行 |
| E1-6 `feat(payments): refund with the paid batch frozen` | billing 的冻结（准入、分配、到期都排除冻结额度）；退款表、发起、审批、查询、人工结论；订单状态按成功退款合计 | 渠道超时 → 待确认 → 查询收敛到成功或失败；同一幂等键只退一次；多次部分退款合计不超过入账额度；冻结期间并发调用花不掉冻结的额度；失败在一个事务里解冻；全额退款金额精确、部分退款向下取整；退不退手续费两种策略；不支持查退款的渠道超时后进待处理 |
