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
type State string    // 订单状态："pending"、"paid"……；转移表是 payments.transitions
type Order struct{}  // ID、站点、账户、用户、商户账户、付款方式、状态、额度、总额、手续费、网关订单号、checkout、有效期、时间戳
type CreateOrder struct{} // Site、Account、User、Method、Credits、Key（幂等键）、ReturnURL、ClientIP、Mobile

func Open(c Config) (*Payments, error) // Plugins（按 Kind 登记）、*pg.DB、平台范围、Keyring、egress.Client；告警经平台事务信号
func (p *Payments) Options(ctx context.Context, s pg.Scope, site accounts.Site, credits int64) ([]Option, error) // 充值页：每种付款方式的总额、手续费、币种；显示的是此刻下单会选中的商户账户的报价：与 `choose` 共用一条选择规则（`ranked`），中间没有别的订单插进来时，订单就落在页面显示的那个账户上
func (p *Payments) CreateOrder(ctx context.Context, s pg.Scope, r CreateOrder) (Order, Checkout, error)  // 账户范围；事务、渠道调用、事务
func (p *Payments) Cancel(ctx context.Context, s pg.Scope, id OrderID) (Order, error)                     // 待支付 → 已取消，减回用量，之后请渠道关单
func (p *Payments) GetOrder(ctx context.Context, s pg.Scope, id OrderID) (Order, error)                     // 账户范围读自己的订单；别的账户的是 ErrNotFound
func ParseOrderID(s string) (OrderID, error)                                                              // 对外写法转 ID；格式不对与不存在同为 ErrNotFound
type Wait struct{ Err error; After time.Duration }                                                        // 等一等就能放行的拒绝（取消次数、每 IP 限额、核验），控制台据此写 Retry-After
func RegisterPoll(c PollConfig) error                // payments.poll：查单、入账、过期、商户查询并发和熔断
func RegisterReconcile(c PollConfig) error           // payments.reconcile：每日核对、按商户/单笔查询派生持久作业
func (p *Payments) Verify(ctx context.Context, s pg.Scope, id OrderID) (Order, error)   // "我已付款"：账户范围授权，先取商户查询名额再占每单每分钟核验次数，繁忙不扣次数
func (p *Payments) Notify() http.Handler                                               // GET/POST /api/payments/v1/notify/{merchant}
func (p *Payments) Refund(ctx context.Context, s pg.Scope, r RefundRequest) (Refund, error)              // 冻结并等待审批；r.Key 幂等
func (p *Payments) ApproveRefund(ctx context.Context, s pg.Scope, id RefundID, user uuid.UUID) (Refund, error) // 独立管理员批准后发送；线下退款继续等待凭证
func (p *Payments) RejectRefund(ctx context.Context, s pg.Scope, id RefundID, user uuid.UUID, reason string) (Refund, error) // 拒绝尚未审批、确定未发送的申请
func (p *Payments) GetRefund(ctx context.Context, s pg.Scope, id RefundID) (Refund, error)
func (p *Payments) GetResolution(ctx context.Context, s pg.Scope, order OrderID, key string) (Resolution, error) // 审批前读取持久提案
func (p *Payments) ApproveResolution(ctx context.Context, s pg.Scope, order OrderID, key string, user uuid.UUID) error
func (p *Payments) Resolve(ctx context.Context, s pg.Scope, r Resolution) error                          // 保存人工提案与渠道凭证；另一个管理员审批后落定
func (p *Payments) SaveMerchant(ctx context.Context, tx pg.Tx, actor audit.Actor, m MerchantInput) (Merchant, error)        // 调 Plugin.Open 校验后封装密钥；有未完成订单时拒改身份字段
```

### 渠道接口

照 sub2api 的 `Provider` 和它的扩展接口（`payment/types.go:217-263`），合成一个接口加能力集合：

```go
type Plugin interface {
	Kind() Kind
	Open(c MerchantConfig) (Channel, error) // 校验并绑定一个商户账户；保存配置时也调用，不合法就拒绝保存
}
type MerchantConfig struct {
	ID MerchantID; Currency platform.Currency
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
type CreateRequest struct{ Order OrderID; Method Method; Upstream string; Total Money; Subject, ReturnURL, NotifyURL string; ClientIP netip.Addr; Mobile bool } // ReturnURL 由控制台处理器按站点登记的 origin 生成（`accounts.Origin`），核心校验 https；NotifyURL 由核心按同一 origin 和 NotifyPath 生成
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
- **金额**：`Notice.Paid`、`Payment.Paid` 由插件把网关的十进制文本精确解析成最小单位。`Minor == 0` 表示缺失，`Minor == -1` 表示无效付款并转待处理；不做容差。没有币种字段的渠道填商户账户的币种。具体规则见[易支付配置与边界](#易支付配置与边界)。
- **插件拿不到数据库**：只拿到配置、密钥和出站客户端；状态和钱都在核心里。插件的错误和日志不带请求的查询串和表单（易支付把密钥放在查询串里）。

### 易支付配置与边界

`EasyPay{}` 实现 `Plugin`，通过内部可选的 `normalizeSettings` 在身份比较和加密封装之前归一化配置，由公网回调与后台任务共用的插件清单登记。`Settings` 严格读取 `gateway`、`pid`、`mode`（`redirect` 或 `api`）、`sign_type`（省略即 `MD5`）、`cids`（本站方式到通道号的映射，可省略）、`query`、`close`、`refund`（省略即不支持）。`Secrets.key` 必填，轮换期间可同时提供 `old_key` 验签，其他密钥名拒绝保存；下单和查询只使用当前密钥。付款币种来自 `MerchantConfig.Currency`。示例：

```json
{"gateway":"https://pay.example.com","pid":"1001","mode":"api","cids":{"alipay":"2"},"query":true,"close":false,"refund":true}
```

付款方式映射只存于核心的 `MethodInput.Upstream`，下单时由核心读取并通过 `CreateRequest.Upstream` 传入。`Channel.Methods()` 返回 `nil` 表示协议允许管理员配置任意本站方式；返回非 `nil` 清单的插件仍受清单约束。网关地址保存前和绑定时去掉查询串、片段及三个 PHP 端点后缀，只接受无用户信息的 HTTPS 地址。`identitySettings` 只比较 `gateway`、`pid`、`sign_type`；订单在途时可以改付款方式、cid、下单模式和能力开关，网关、商户号、签名方式、币种和密钥仍受身份保护。所有设置仍整体参与密钥封装的哈希校验，不能通过库外编辑改变运行配置。

`close` 仅用于明确支持 `api.php?act=close` 的网关，参数与查单一样同时发 URL 和表单。退款使用 `api.php?act=refund` 和表单；不声明 `RefundKey`、`QueryRefund` 或 `Statements`。退款只在有效的 2xx JSON 明确说“订单不存在”时切换订单标识，网络错误、非 2xx 或无法解析的响应不能触发第二次退款。

插件只持有不可变的商户绑定，每次调用创建自己的参数表。核心按服务端生成的返回地址同源域名和唯一的 `NotifyPath` 生成 `CreateRequest.NotifyURL`，运行程序也用这个常量登记 GET/POST 路由，插件直接使用该地址。GET 只读查询串，POST 只读表单并拒绝非空查询串，重复字段一律拒绝；`Notice.Raw` 只写固定协议描述。响应 JSON 使用平台严格解析，字符串或数字标量以原文读取，避免经过浮点。

只有缺失或空的金额文本用 `Minor == 0` 表示，仍由核心补查；非空金额的零值、格式非法、精度超限和整数溢出统一用 `Minor == -1` 表示，保留为无效付款并转待处理。不接受千分位、指数、分数或符号，也不将它们误报为验签失败。所有网关响应、解析和网络错误仅给出固定上下文及 HTTP 状态，不能回显网关返回的消息。

### billing 要提供的接口

payments 引用 billing，在自己的事务里调用（[总体架构的规则 5](../../docs/architecture.md#模块边界规则)：数据的所有方提供接受调用方事务的函数）：

| 函数 | 用途 | 由谁实现 |
|---|---|---|
| `billing.TopUpRate(ctx, tx, site, from, to platform.Currency) (Rate, error)` | 当前参考汇率版本与站点的充值缓冲（`billing.top_up_buffers`，没有行时用代码里的 2%），`Rate.Applied` 是精确分数（分子分母可能超出 int64，订单的成交汇率列不能用 bigint）；参考汇率是平台数据，所以 payments 在平台范围的只读事务里调它，在下单事务 1 之前读好（汇率版本只追加不改，读到的版本 ID 随订单锁定）；过期后按到账汇率入账时也调它 | E1-2a |
| `billing.CreditPaid(ctx, tx, PaidCredit)` | 锁余额行，写付费额度批次（ID = 订单 ID 或待处理记录 ID，记实付、其中的手续费、入账额度和所用汇率）、"用户充值"分录和审计。同一 ID 再次调用时比较入账额度、实付和手续费：一致就什么也不写，不一致返回错误，调用方转待处理；不用 `ON CONFLICT DO NOTHING`，否则金额不同的第二次调用会被静默丢掉 | E1-1 |
| `billing.PrepareRefund(ctx, db, scope, Freeze) error` / `billing.FreezeRefund(ctx, tx, Freeze) (int64, error)` | 订单事务和订单锁之前先无锁分页准备；锁余额行后补尾、算可退额度，写一笔带序号的冻结事件，再重放吃进它；返回实际冻结的额度 | E1-6a |
| `billing.SettleRefund(ctx, tx, refund)` / `ReleaseRefund(ctx, tx, refund)` | 冻结转成"退款"分录（billing 根据原付款、账本历史和手续费政策算现金与手续费；冻结后先用 `QuoteRefund` 取得并保存报价）/ 写一笔解冻事件。重复调用由 payments 在同一事务里对退款行的条件更新挡住 | E1-6a |

`PaidCredit` 带账户、站点、批次 ID、入账额度、商户账户（现金科目按它区分）、付款币种、实付、其中的手续费、所用参考汇率的版本 ID（`RateID`；成交汇率由批次的 (实付 − 手续费) ÷ 入账额度 推出，不另传）。跨币种时两本账（站点 × 货币）各用汇兑损益配平：付款币种的账借现金（实付）、贷手续费收入（手续费）和汇兑损益（其余）；记账货币的账借汇兑损益、贷用户余额（额度）；两边按参考汇率的差额就是充值缓冲带来的汇兑收益。同币种时没有汇兑损益两行。订单上的 `paid_total`、`paid_fee`、`credited` 与批次出自同一个 `PaidCredit`、同一事务写入，退款金额由 billing 按付费批次和账本退款历史计算，payments 用 `QuoteRefund` 保存报价并在订单上保存相同的手续费政策；每日对账核对两边一致。

## 不变量

| 不变量 | 靠什么保证 |
|---|---|
| 一笔订单至多入账一次 | 状态的条件更新（只有待支付、已过期、已取消、失败、待处理能变成已支付）加额度批次以订单 ID 为主键，`CreditPaid` 同 ID 不同金额报错；性质测试随机重放、乱序、并发投递回调与补单，批次恰好一个 |
| 订单是已支付 ⇔ 存在 ID 等于订单 ID 的付费批次 | 两者在同一个事务里写；故障注入让 `CreditPaid` 失败，订单仍是原状态；每日对账核对 |
| 只有事务提交后才回 `Ack(true)` | 处理器在提交返回之后才调 `Ack(true)`，回滚、提交失败调 `Ack(false)`（非 2xx）；故障注入让提交失败，断言应答不是成功、网关会重试 |
| 没有通过验签，回调什么都不写 | 处理器在 `Verify` 返回成功之前只读配置，不开写事务；测试对坏签名、缺签名、多余参数、`pid` 不符逐个断言库里没有新行 |
| 验签通过的成功付款都有记录 | 找不到订单、网关订单号已记在别的订单上，都写待处理记录（按网关订单号去重）后才回成功；测试：两种情况都有一行待处理记录、应答不是 500 |
| 渠道支持查单时，没有查询确认就不入账，入账金额来自查单 | 回调与补单走同一个入账函数，它要求一个 `Payment{State: Paid}` 并用它的 `Paid`；测试：查单失败返回 502 且不入账；通知与查单金额不同时订单转待处理 |
| 一个商户账户的回调只能改它自己的订单 | 订单按（商户账户 ID，订单 ID）查找；测试用 A 的密钥给 B 的订单签名，被拒 |
| 实付与总额按最小单位完全相等才入订单的额度 | `Money` 是整数，比较用 `==`；表驱动测试覆盖多一分、少一分、末尾零、超出精度 |
| 总额可以从订单的列复算 | `total = ceil((credits × applied + fee_fixed) ÷ (1 − fee_ppm/10⁶))`，按 `math/big` 精确算一次；锁定的 `applied_*` 永不覆盖，到账汇率另存在 `arrival_*`；性质测试：复算等于存值，总额 ≥ 充值部分 + 固定手续费，手续费 ≥ 0 |
| 订单状态只按设计表里的边变化 | 转移表是代码里唯一的一份；测试枚举所有（从，到）组合，表外的全部被拒 |
| 限额在锁内检查 | 账户级计数在账户的事务级咨询锁内读、插入；商户当天额度在用量计数行的行锁内读、加；并发测试：最后一点额度或最后一个待支付名额同时被 10 个请求抢，只有放得下的那些成功 |
| 一笔订单的已冻结、发送中、待确认、成功的退款额度之和 ≤ 入账额度 | 退款在订单行锁下插入并检查；数据库 CHECK 管单行，合计由锁和性质测试守 |
| 一笔订单的退款金额之和 ≤ 净付款额，退完时恰好相等 | 金额 = ⌊额度 × 净付款额 ÷ 入账额度⌋，让累计额度达到入账额度的那一笔取余数；退不退手续费由第一笔退款成功时在 billing 批次上固定，订单在同一事务保存相同政策（`refund_with_fee`；第一笔失败则解冻，该字段保持未设，下次重试按当时的政策重新定），净付款额不随政策变；订单行锁保证一笔在途，billing 在余额行锁内按批次和账本计算并核对累计现金；性质测试：任意汇率序列（锁定、宽限期内、到账汇率）和任意部分退款拆分下，Σ退款 ≤ Σ实付 |
| 退款只按"已冻结 → 发送中 → 成功 / 失败 / 待确认 → 成功 / 失败"变化，转分录、解冻各在一个事务里 | 状态的条件更新与 billing 的调用同事务；"发送中"在调渠道之前单独提交；测试注入渠道超时、渠道失败、提交失败，不出现"扣了没退"或"退了没扣" |
| 渠道不认退款单号时，结果未知的退款从不重发 | 只有 `Caps` 含 `RefundKey` 时，任务才对发送中、待确认的退款再调 `Refund`；测试：第一个事务后崩溃，退款恰好发一次；发送中崩溃，不认单号的渠道一次也不重发、进待处理；同一订单的第二笔退款在第一笔落定前被拒 |
| 一个人不能把未知退款标为失败 | `Resolve` 标失败要求凭证和另一个人的批准；测试：同一人发起并批准被拒；渠道其实已退时，只按凭证落定为成功 |
| 渠道的未知结果从不变成失败 | 只有 `*Rejected` 能让下单变失败、退款变失败；测试把超时、5xx、HTML 响应逐个喂给核心 |
| 加锁顺序：下单是账户咨询锁 → IP 计数行 → 商户身份共享咨询锁 → 用量计数行；下单被渠道拒绝而失败是订单行 → IP 计数行 → 用量计数行；入账是商户身份共享咨询锁 → 订单行 → 余额行 → 用量计数行；新建退款先取商户身份共享咨询锁，再按订单行 → 退款行 → 余额行；退款自动落定与审批经 `changeRefund` 按订单行 → 退款行 → 余额行，人工结论在已有订单事务内同序加锁 | 所有写路径经同一个入口函数；同账户并发的下单、入账与退款测试不死锁 |
| 每 IP 的下单与失败限额不被并发、跨账户超过，计数出错时不放行 | 下单事务里先取账户锁，再用 `ip_usage` 行的 `INSERT … ON CONFLICT DO UPDATE … WHERE 未超限` 原子占一个名额，该行锁是串行点，随订单一起提交或回滚；占名额的语句出错，错误原样上抛、订单不建、不调渠道；失败订单的 `failed` 与失败计数在同一事务（先计数再改状态，与下单同为「IP 行 → 用量行」的顺序）；测试：边界 20/21 与 10/11、十个账户同 IP 并发只放行限额、计数故障注入（表上的触发器）下单被拒且渠道没被问、失败计数故障时订单仍是待支付 |
| 密钥不离开密文，网关地址不能绕过保存流程改掉 | 密钥只以 `secret.Secret` 在内存里，解封位置 `{payments.merchant_configs, 商户账户 ID, "config"}`；密文里同时封着 `settings` 的 SHA-256，取用时不一致就拒绝并告警；测试：直接改库里的网关地址后这个商户账户用不了；扫描日志、链路、订单事件、审计和接口响应，找不到密钥，也找不到出站请求的查询串 |
| 账户范围读不到商户配置和密文 | `merchant_configs` 只对站点和平台范围开放；行级安全测试：账户范围读得到付款方式与费率，读 `merchant_configs` 得 0 行 |
| 支付链接只有白名单协议 | 核心统一检查 `Checkout`；表驱动测试：`//evil.com/x`、`javascript:`、`data:`、`http:` 被拒，`/pay?x` 补全后放行 |
| 选不出商户账户就不下单 | 过滤后为空、查用量出错都返回错误，不退回全部候选；表驱动测试 |

## 数据

schema `payments`，迁移在 `migrations/`，查询在 `queries/`。每张表 ENABLE 并 FORCE 行级安全（[平台的「范围」](../platform/README.md#范围)）。

| 表 | 关键列 | 约束与索引 | 类别与策略 |
|---|---|---|---|
| `merchants` | `id`、`site_id`、`kind`、`name`、`enabled`、`currency`、`ttl_seconds`、`refund_enabled`、`user_refund`、`sort`、`rev` | `currency ~ '^[A-Z]{3}$'`；`ttl_seconds` 在 60 到 86400；唯一 `(id, site_id)` 供本模块的子表引用；`(site_id, sort)`；不删除，只停用 | 站点公开配置：平台范围和站点范围可读，普通账户范围只能读授权给出的所属站点。使用 [pg 的站点公开配置表达式](../platform/pg/README.md#决策与理由)，每个范围读取每条语句至多执行一次。所属站点由 `pg.AccountScopeIn(站点, 账户)` 携带，来自账户授权，不依据账户可以伪造的 `platform.tenants` 登记。写入由站点范围表达式限制；主站行另由管理接口的控制面守卫限制，只有平台范围可改 |
| `merchant_configs` | `merchant_id` 主键、`site_id`、`settings`（非密配置，规范 JSON，供后台展示）、`ciphertext`/`nonce`/`wrapped_dek`/`key_version`（密钥、轮换中的旧密钥、`settings` 的 SHA-256）、`rev` | 改 `settings` 必须同时重新封装 | 站点数据：读写均由 [pg 的站点范围表达式](../platform/pg/README.md#决策与理由)限制；账户范围读不到 |
| `merchant_methods` | `merchant_id`、`site_id`、`method`、`upstream`、`fee_ppm`、`fee_fixed`、`single_min`、`single_max`、`daily_limit`、`enabled` | 主键 `(merchant_id, method)`；`fee_ppm` 0…500000；`fee_fixed ≥ 0`；`single_min ≤ single_max`（都设了时）；后三项是付款币种的总额（最小单位） | 同 `merchants` |
| `merchant_usage` | `merchant_id`、`site_id`、`day`（UTC 日）、`used`（付款币种的最小单位） | 主键 `(merchant_id, day)`；`used ≥ 0` | 共享计数（[安全的「租户隔离」](../../docs/security.md#租户隔离)）：读、INSERT、UPDATE 的策略都是「范围的站点等于该行 `site_id`」（INSERT 用 `WITH CHECK`，`ON CONFLICT` 的 UPDATE 同受 `USING` 与 `WITH CHECK` 约束）；`axiom_app` 只有 SELECT、INSERT 和 `UPDATE (used)` 的列权限，没有 DELETE。账户范围的下单事务要锁它、加它，却读不到别的账户的订单，也碰不到别的站点的行 |
| `ip_usage` | `site_id`、`ip`（`clientip.Group` 的结果：IPv4 地址，或 IPv6 的 /64 网络地址）、`window_start`、`orders`、`failures` | 主键 `(site_id, ip)`；计数 ≥ 0；窗口 1 小时，从窗口过后的第一次下单或失败起算，到期时下一次事件把行重置 | 共享计数，策略同 `merchant_usage`（读、INSERT、UPDATE 都是站点范围并上所属站点，`axiom_app` 只有 SELECT、INSERT 和三个计数列的 UPDATE，没有 DELETE）。行只在这个 IP 下过单或有订单失败后才出现，不会比订单多 |
| `settings` | `site_id` 主键，加「后台配置」表里站点级的各项（含 `ip_order_limit`、`ip_failure_limit`、`refund_with_fee`、`amount_mismatch_policy`；后两项未设时分别不退手续费、人工处理） | 各项范围 CHECK；没有行就用代码里的默认值 | 同 `merchants` |
| `orders` | `id`、`site_id`、`account_id`、`user_id`、`merchant_id`、`method`、`state`、`idem_key`、`fingerprint`、`credits`、`site_currency`、`pay_currency`、`total`、`fee`、`fee_ppm`、`fee_fixed`、`rate_id`、`ref_num`/`ref_den`、`applied_num`/`applied_den`、`arrival_rate_id`、`arrival_ref_num`/`arrival_ref_den`、`arrival_num`/`arrival_den`、`trade_no`（下单时网关给的，可空）、`checkout`（支付链接或二维码，不含 `ClientSecret`）、`expires_at`、`paid_total`、`paid_fee`、`credited`、`paid_at`、`refund_with_fee`（第一笔成功退款按冻结报价的政策定下，之后沿用，失败不设）、`next_query_at`、`query_delay_seconds`、`next_verify_at`（E1-2c）、`client_ip`、`created_at`、`updated_at` | `credits > 0`；`total > 0`；`0 ≤ fee ≤ total`；`(rate_id IS NULL) = (pay_currency = site_currency)`；`arrival_*` 同空同非空；`state` 枚举；`state = 'paid' → paid_at, paid_total, paid_fee, credited` 非空、`credited > 0`、`0 ≤ paid_fee ≤ paid_total`；唯一 `(account_id, idem_key)`；`(merchant_id, trade_no)` 普通索引；部分索引 `(expires_at) WHERE state = 'pending'`；`(account_id, created_at DESC)`；`(merchant_id, created_at)` 供对账复算用量 | 账户数据：读写由 [pg 的账户范围表达式](../platform/pg/README.md#决策与理由)限制 |
| `order_payments` | `id`、`order_id`、`site_id`、`account_id`、`merchant_id`、`trade_no`、`amount`、`currency`、`source`（notify、query、admin）、`at` | 唯一 `(merchant_id, trade_no)`，冲突时进待处理记录、不报错；`amount > 0`；`(order_id)` | 账户数据 |
| `order_events` | `id`、`order_id`、`site_id`、`account_id`、`at`、`source`（notify、query、user、admin、job）、`kind`、`from_state`、`to_state`、`trade_no`、`amount`、`raw`（截到 8 KiB） | 只插入：`axiom_app` 只有 INSERT、SELECT；`(order_id, at)` | 账户数据 |
| `refunds` | `id`、`order_id`、`site_id`、`account_id`、`state`（frozen、sending、pending、succeeded、failed）、`via`（channel、offline）、`credits`（实际冻结）、`requested_credits`（原请求，重放比较）、`amount`（报价）、`with_fee`、`refund_key`（冻结时的能力快照）、`manual_only`（差异已记录且只能人工落定）、`provider_ref`、`reason`、`requested_by`、`approved_by`、`idem_key`、`attempts`（尝试栅栏）、`sent_at`、`next_query_at`、`created_at`、`finished_at`（成功时取 billing 退款分录的时间） | `0 < credits ≤ requested_credits`；`amount > 0`；`state IN ('sending','pending') → sent_at` 非空；终态等价于有 `finished_at`；审批者不等于申请人；唯一 `(order_id, idem_key)`；部分唯一索引确保每单至多一笔在途，另有只含已批准且仍可自动处理的渠道退款的到期索引和成功现金日期索引 | 账户数据可读；只允许站点和平台范围写，用户申请经范围检查后由核心事务写入 |
| `resolutions` | `id`、`order_id`、`site_id`、`account_id`、`refund_id`（退款结论时，与 `order_id` 共同引用退款）、`outcome`（succeeded、failed、credit）、`trade_no`、`amount`（人工入账时）、`evidence`、`reason`、`requested_by`、`approved_by`、`idem_key`、`created_at` | 唯一 `(order_id, idem_key)`；提案内容不可修改，应用只有 `approved_by` 的列更新权限；审批者不等于提出人；退款和入账目标互斥 | 站点数据，只允许站点和平台范围读写；审批前可经 `GetResolution` 检视原提案 |
| `query_health` | `merchant_id`、`site_id`、`failures`（0…4）、`paused_until`、两组查询令牌及期限 | 商户主键与同站外键；单条条件 UPDATE 原子取两者之一；查询有 5 秒期限，租约 10 秒；连续 5 次网关未知暂停 5 分钟，旧令牌完成不改新令牌；暂停时钟在状态行锁取得后读取 | 站点数据，用策略级 InitPlan 读取范围，仅站点/平台范围可读写；用户核验经账户授权后用平台范围占名额 |
| `discrepancies` | `id`、`site_id`、`account_id`（可空）、`order_id`（可空）、`merchant_id`、`kind`（amount_mismatch、multiple_payment、duplicate_payment、unmatched_payment、trade_no_conflict、statement、refund_unknown、ledger、usage）、`dedupe`（回调/查询付款事实用订单号和网关单号的指纹；退款 ID 或对账日期与规范化明细的指纹）、`detail`、`state`（open、resolved）、`created_at`；`investigated_by`、`investigated_at`、`note`；resolved 仅表示调查已记录，不表示已处理资金 | 唯一 `(merchant_id, kind, dedupe)`，同一事实重复上报只留一行；`(site_id, state, created_at)` | 站点数据，带账户时同账户数据 |

- **对外 ID**：订单 ID 的对外写法就是发给渠道的商户订单号，`ord_` 加 26 个字符。
- **另行入账**：人工把一条待处理的付款（重复付款、有付款没订单）入到某个账户时，额度批次的 ID 用这条待处理记录的 ID，重复提交不会入两次。
- **账户删除**：订单、退款、订单事件与账本同寿命，不随账户删除（保留期与审计日志一起由负责人定）。
- **没有外键**：`merchant_id`、`order_id` 只在本模块内引用（`merchant_configs`、`merchant_methods`、`merchant_usage` 以 `(merchant_id, site_id)` 引用 `merchants (id, site_id)`，子行只能在商户账户所在的站点：否则站点 A 的账户能以 A 的站点为 B 的商户账户先造一行当天用量，挡住 B 自己的 upsert）；账户、站点、汇率版本只按 ID 引用（[总体架构的规则 1](../../docs/architecture.md#模块边界规则)）。
- **对账单**：第一个声明 `Statements` 能力的插件随它加 `statement_lines` 表；易支付没有账单接口，E1-5 不建这张表。

## 事务与任务

- **下单**：先按账户幂等键读已存在的订单，重放不受当前充值开关、费率和限额影响；新请求再读「菜单」（`menu`）：账户范围的只读事务读设置、这种付款方式的商户账户和当天用量；平台范围的只读事务检查这些商户账户的配置能否打开（插件存在、密钥解得开、设置哈希一致、插件接受；结果按商户账户的 `rev` 缓存一分钟，绕过保存改库的最迟一分钟后被排除并告警，打不开的不参与报价和挑选、不影响别的账户；账户范围从不读密文），再读 `TopUpRate`。事务 1（账户范围）设 `lock_timeout = 2s`、`statement_timeout = 5s`（超时回 503，订单未建），取账户的事务级咨询锁（`pg_advisory_xact_lock`，键由整个账户 ID 的 SHA-256 派生，避免 UUIDv7 的时间前缀把无关账户串行化）→ 读这个账户的计数与待支付、取消、当天、首笔上限 → 选商户账户，持有商户身份的共享咨询锁并核对报价修订号（保存配置先取同键的排他锁，再检查在途订单；旧报价拒绝重试；身份锁用完整商户 ID 的散列、独立命名空间 0x4d，账户订单锁用 0x4f，锁与修订查询均经 sqlc）→ `INSERT … ON CONFLICT DO UPDATE` 锁住并加上它当天的用量计数 → 算总额 → 插入订单和事件。创建、有效期都取数据库事务时钟（`transaction_timestamp() + ttl`），通过 `RETURNING` 交回时间；checkout 保存后也返回实际的 `updated_at`。提交后调 `Create`；事务 2 检查链接，写回 `trade_no` 和 `checkout`。调用 `Create` 之前配置打不开（确定未发送）、`Create` 返回 `*Rejected` 或链接不合格时事务 2 改为失败并减回用量；链接不合格的订单提交失败状态后还会按网关单号关单；未知错误不改，返回 502 给用户，订单由补单和过期收尾。
  返回地址（`CreateRequest.ReturnURL`）是控制台处理器用 `accounts.Origin`（请求的域名登记的 origin，不是请求头里的 Host 或 Origin）加控制台的挂载前缀和 `/balance`（`webui.ConsoleMount`，即 `/console/balance`）拼出来的，浏览器传不进来；origin 不是 https（本机开发的 `http://localhost`）时下单被拒，核心再检查一遍它是 https。订单 ID 在下单时才生成，所以地址里写不下它，页面靠订单接口取。幂等键是请求头的值，`(account_id, idem_key)` 唯一，指纹是（付款方式，额度），同键同请求返回原订单与已存的 checkout（没有 `ClientSecret`，它从不入库；与首次下单并发时 checkout 可能尚未保存），同键异请求返回 409。重放返回 nil 错误只表示找到了同一请求，不重放首次错误：处理器按 `State` 和 `Checkout` 映射应答，首次请求和重放走同一个函数（`cmd/axiom` 的 `placed`）：`failed`（首次是 `ErrRefused`）回 422，待支付且 checkout 已存回 201，待支付而 checkout 为空（首次结果未知返回 `ErrUnconfirmed` 并附订单，或另一个同键请求还在调用渠道）回 202 和这笔订单、不带支付链接，其余状态回 200 且不带支付链接；不能仅凭 err 为 nil 展示付款成功。每 IP 限额在账户检查之后、选商户账户之前占名额（被账户自己的限额或选不出商户账户拒绝的请求回滚，不占），重放在这之前已经返回，不占；窗口、默认值和计数方式见「数据」的 `ip_usage` 与「决策与理由」。
- **取消**：账户范围的事务锁订单行 → 待支付变已取消 → 减回用量 → 事件；已取消的再取消就返回原订单、不重复关单。仅本次实际取消的，提交后请渠道关单（`Close`），不声明这个能力的跳过，失败只记日志：订单已经不能在本站付款，渠道那边还能付的由补单兜住。取消前不查单，避免渠道故障阻止本地取消；取消与已确认付款竞争时，由订单行锁决定先后；取消后渠道仍收到的钱由同一入账路径处理。
- **过期**（`payments.poll` 的第 2 步）：每分钟一轮，按 `expires_at` 取一批已过有效期的待支付订单（最老的先，一轮 30 秒预算）→ 逐个锁行，状态仍是待支付就变已过期、减回用量、写事件 → 先提交本批全部过期事务，再逐个请渠道关单。每次关单（含打开配置）最多 5 秒，本轮超时过的商户不再关单；整个过程仍受 30 秒预算约束。网络阻塞不会占住本批其他订单的待支付名额或用量。失败的一个不挡后面的。本轮先用最多 10 秒查询待支付和补查窗口内的订单（未过期待支付优先，其次刚到期的待支付，最后晚付窗口；到期的待支付订单不受下次查询时间限制）；查到已付的同事务入账，后续过期步骤跳过。余下预算用于本地过期和关单。每次查询最多 5 秒，正常查询间隔从 30 秒翻倍至有效期内 10 分钟、过期后 1 小时；每单的用户核验独立限每分钟一次；回调确认同样占商户查询名额，暂停或满额时回 502 让渠道重试，已入账的精确重放仍免查。商户间并发执行；查询名额和连续未知计数由数据库跨节点共享，最多 2 个在途查询，连续 5 次网关未知暂停 5 分钟。
- **回调**：可信代理经平台 `clientip` 解析，`Group` 将映射 IPv4 还原、IPv6 去 zone 后按 /64 归并；先按来源分组限每分钟 600 次 → 读配置（解密后的渠道缓存一分钟，修订变化立即失效）→ 仅为存在且配置可打开的商户按（商户账户，来源分组）限每分钟 120 次（两级合计最多 4096 桶，满时淘汰最久未访问的桶）→ `Verify` → 无锁预读，已入账的同商户、同订单、同网关单号且金额币种相同才跳过查单 → 最多 5 秒查单 → 入账事务 → 提交后 `Ack(true)`。事务内按商户身份共享咨询锁 → 订单行 → 余额行 → `merchant_usage` 行的顺序锁定（待处理跳过余额；下单只插入新订单，过期先订单后用量，不形成反向等待）；唯一网关单号冲突记待处理，不把确定差异变成无限重试。已付但金额为缺失哨兵 0 或网关单号为空时同一期限内重查一次，仍空只记事件和警告、不改状态；金额为无效哨兵 -1 时直接待处理，后续完整回执不能自动清除差异；非法 UTF-8/NUL 的身份字段清理后只用于记录，已付进待处理。金额不符默认记待处理；站点的 `amount_mismatch_policy = actual` 只对单笔、同币种、身份无冲突的付款共用 `creditAt` 按实付折算，币种不符始终人工。已有待处理的订单不因重放或后来修改政策自动恢复入账，需凭证提案与另一人审批。取消/过期/失败释放过用量的订单进入已付或待处理时，按创建日恢复用量一次。
- **窗口不变量**：`order_windows` 视图（调用者权限与原表 RLS）统一商户身份保护、用户核验、队列发现和排期的可查条件与默认值；待支付始终保护，待处理另行保护，已过期/已取消/有网关单号的失败单保护到过期时间加 `max(宽限期, 自动入账期限, 补查窗口)`。默认宽限 300 秒，后两项共用 604800 秒；即使补查配置较短，也不能先释放可自动入账订单的商户身份。
- **补单容量**：每轮最多发现 100 笔，待支付优先，组内按商户交错取单，同商户最多两个工作者；查询阶段最多 10 秒，下一阶段的本地过期和关单仍有预算。只看批量上限，每天理论上不超过 144000 次查询，实际还受网关往返、数据库开销、暂停和晚付积压影响，不作为吞吐承诺。默认 30 分钟有效期约查 7 次，过期后仍按最长 1 小时退避；上线前须按真实延迟与未付量评估。
- **入账**（回调、补单、"我已付款"、人工）：平台范围的一个事务，`SELECT … FOR UPDATE` 订单行 → 插入 `order_payments`（冲突写待处理记录）→ 条件转移 → `CreditPaid` → 事件；过期后到账的同时加回用量计数。事务里不做网络调用。
- **退款**：账户范围只在商户允许用户申请时请求渠道退款；管理员可请求渠道或线下退款，所有请求先冻结待审。先经原范围确认订单归属，再在订单事务之前 `PrepareRefund` 无锁分页预放；事务 1 取商户身份共享锁、订单行锁，检查在途与额度上限，`FreezeRefund`、`QuoteRefund` 保存实际冻结与原币现金报价。零现金报价回滚整个冻结。`ApproveRefund` 只接受原申请人以外的站点/平台操作者，终态拒绝再次批准且不写审批或审计，审批和审计同事务；已冻结但还未审批的请求可由 `RejectRefund` 拒绝并原子解冻。批准提交后，事务 2 锁订单、退款行，将已冻结变发送中、设尝试序号与一分钟租约、记首次 `sent_at`；认领前剩余轮次预算必须足够五秒；独立于调用方的五秒调用期限在这个事务之前开始，过期发送者不能醒来后重新发送。认领后即使请求断开或轮询发现预算到期，也用独立的最多三十秒完成窗口保存结果、准备账本和落定，保留请求 ID；轮询的十秒预算只决定是否继续认领。渠道绑定打开失败一律不认领；已冻结但退款能力被停用的保持未发送，不误报渠道未知。每次落定、拒绝或人工解冻前也先 `PrepareRefund` 无锁预放等待期间新增的账本历史，再进入事务 3 同序加锁，只有匹配尝试序号且非终态的结果才能 `SettleRefund` 或 `ReleaseRefund`，成功时再按成功额度合计更新订单与手续费政策；退款引用只写退款行，不覆盖订单原网关单号；格式非法或过长时仅丢弃该引用，不丢失已确认的成功或失败。成功的 `finished_at` 经 `billing.RefundRecordedAt` 取刚写入账本的同一时间，跨 UTC 午夜也归同一现金日。
- **`payments.poll`**（每分钟，队列的周期任务，只有领导节点插入）：一轮有时间预算，查询先按未过期待支付、刚到期待支付、晚付窗口排序，组内按 `next_query_at` 依次处理 (1) 待支付订单：查单；(2) 已过有效期的待支付订单：查单、关单、过期、减回用量；(3) 已过期、已取消及有网关单号的失败订单：在补查窗口内按退避查单；(4) 已审批、停在已冻结且排期到期的渠道退款：置发送中再发；(5) 发送中、待确认的退款优先查退款；没有查询能力时，只有冻结快照和当前能力都含 `RefundKey` 才用原单号重发，其余不重发并立即记待处理，同时设 `manual_only`，从到期索引和自动认领中退出、保留冻结供人工落定；仍能自动查询或重发的超过 24 小时也记待处理。退款每次尝试间隔一分钟、最多发现 100 笔；本地过期提交后给退款最多 10 秒预算，再关过期订单，避免慢关单饿死退款。未批准、线下和已停用自动处理的未知退款不在自动发现队列里。退款查询与查单共用健康判定：明确拒绝是已知，请求取消或轮次截断不计失败，核心自己的五秒网关期限仍计未知。每次查询后按退避写下一次的 `next_query_at`；同一商户账户跨节点同时最多 2 个查询、连续 5 次网关未知暂停这个账户 5 分钟并告警。失败的不挡后面的（与 billing 的到期任务同一做法）。
- **`payments.reconcile`**（每天，启动时也运行）：协调作业固定前一个 UTC 日，分页发现包括已停用的商户，再派生商户作业，商户核对与差异落库合计最多 5 分钟。作业走独立的 `pg.QueueReconcile`，单节点一个工作者，不占 money 队列。在 `ReadTx` 的一致快照内按日索引核对现金、当日变更订单与当日批次，并复算当天及这些变更影响的创建日日用量；现金和订单共用批次创建日，避免跨午夜入账的假差额，按币种换成十亿分之一精确比较，大数合计不截成 int64。借助 billing 的 `MerchantCash`、`MerchantBatches`、`ReadCredits` 按页批量读账，不直接访问 billing 表；同时检查缺批次、孤立批次及 paid_total/paid_fee/credited/账户/币种/商户不符。按成功退款的账本日期从订单侧扣除原付款币种的现金；没有对应成功支付退款记录的现金冲出仍进差异。迁移 00006 的订单日期/游标索引并发构建，之前的表与列步骤可在失败后重入，避免加索引阻塞已有订单写入。对账差额按商户、类别、日期/订单及规范化明细的指纹去重，与 `payments.discrepancy` 平台信号在同一事务提交，回滚没有独立告警。当前编入的易支付不声明 `Statements`，所以按前一天非终态/变化订单及补查窗口里的订单再查；每个查询作业只处理一单，成功后持久派生带日与游标的下一单；跨节点仅使用商户第一个名额，给回调、轮询和用户核验保留第二个。名额忙返回 `ErrQueryBusy`，通过 River 的有次数上限重试和抖动避开轮询；仅熔断 `ErrQueryPaused` 延后一分钟，不重做成功前缀；查询失败或付款信息仍不完整时重试当前单。`Statements` 的下载接口和明细表随第一个支持它的插件交付。
- **管理调查**：`ListDiscrepancies`、`GetDiscrepancy`、`InvestigateDiscrepancy` 经调用方的范围事务读取；调查写入 `resolved`、操作者、时间和说明并记审计、清除平台告警，不触碰订单、余额和退款。管理 API 的三项操作都需 MFA 和平台 `sites.manage`；变更需 CSRF、理由和幂等键。响应的新字段全部可选；旧付款差异中的 JSON 整数金额读取时转成十进制文本，避免大于 2^53 的最小单位值被舍入。列表一页最多 100 行，以最后一行 UUID 的 `before` 继续；记录本身用 UUID，站点、账户、商户、订单引用沿用各自的 TypeID。资金结论需调用独立的提案与审批接口。
- **范围**：回调和两个任务用 `cmd/axiom` 交来的平台范围；控制台用 accounts 交出的账户范围，站长后台用站点范围。

### 人工结论与验证

`Resolve` 只持久保存同订单幂等键下的提案：目标退款与成功/失败，或待处理订单的付款事实；理由、渠道凭证和提出人必填。`GetResolution` 供另一个管理员检查，再由 `ApproveResolution` 按存储的内容执行，不能由调用者传两个 ID 就当作已有批准。当前没有运营累计资金上限服务，所有退款发起和所有资金人工结论都取保守的第二人审批；用户身份来自调用方已完成的 accounts 授权，不从请求体取。所有退款接口是模块 Go 接口，控制台和管理退款 HTTP 契约随对应页面接入。未知退款仅在待确认时接受人工结论，查询/发送在途时不能解冻；线下退款批准后保持冻结，转账凭证经同样的第二人批准才结算。待处理付款仅对同币种、无冲突的单笔付款入账；重复或无订单付款的另行入账不在本接口内。人工入账不受自动入账时间窗限制，但仍使用锁定/到账汇率、手续费和账本校验，失败时不记录批准。提案、批准、拒绝与资金变化都记同事务审计。

验收用真实 PostgreSQL 与受控渠道：`TestRefundTimeoutQueriesAndAtomicFailure`、`TestRefundCrashBoundariesAndIdempotency`、`TestSendingCrashNeverResendsAnUnknownUnkeyedRefund` 验证两处崩溃和查退款收敛；`TestConcurrentRefundRequestsSerializeAtTheOrder`、`TestRefundConcurrentApprovalSendsOnce` 验证跨请求的冻结与发送串行；`TestRefundOutcomeAndLedgerRollBackTogether` 注入延迟到提交时的异常，验证退款、解冻与账本同成同败；`TestUnknownRefundManualConclusionNeedsEvidenceAndSecondPerson`、`TestOfflineRefundUsesTheSameFreezeAndLedger` 验证持久凭证、身份分离与线下结论；`TestRefundFXSequenceAndSplitProperty` 随机改变下单、到账和退款时汇率，遍历锁定、宽限与到账三种入账路径及两种手续费政策、随机拆分，检查每一步累计现金不超过实付、全退恰等于净付款、分币种账本平衡和日对账；`TestRefundCompletionPreparesHistoryBeforeOrderLock` 锁住账本表时仍能取得订单锁，验证完成前的历史预放不占订单锁；`TestRefundFinishedAtUsesTheLedgerClock` 验证跨 UTC 日期；`TestRefundCapabilityUpgradeCannotResendAnEarlierUnknown` 和 `TestExpiredRefundSenderDoesNotReachChannel` 验证能力变更与过期发送者不能重新发钱；`TestEasyPayRefundCoreTreatsAmbiguousHTTPAsUnknown` 通过本地 TLS 易支付网关覆盖成功、明确拒绝、HTML 和 503，不确定结果持续冻结且不重发。金额不符的人工/实付策略由 `TestAmountMismatchPolicyAndManualCredit` 覆盖。`TestRefundReviewCallerDeadlinePreservesDefinitiveOutcome` 与 `TestRefundReviewBudgetReservesWholeCall` 验证调用方期限和剩余认领预算；`TestRefundReviewQueryHealthClassifiesFailures` 验证共享健康规则；`TestRefundReviewUnrecoverableWorkLeavesAutomaticQueue` 验证人工退款不再被反复认领且仍能落定；其余 TestRefundReview 前缀的回归覆盖停用能力、终态审批、订单复合外键、非法引用、绑定故障和币种重放边界。

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
| 非密的商户信息、付款方式、站点设置给所属站点的范围读；密文和网关配置单独一张表，只给站点和平台范围 | 账户范围的充值页要读付款方式和限额；行级安全按行不按列，密文必须在另一张表才读不到 | 整表仅按站点范围限制：账户范围读到 0 行，充值页没法用；整表放宽：用户读得到密文 |
| 密钥和 `settings` 的哈希封在同一份密文里，`settings` 明文存放 | 网关地址要能在后台展示，又不能被单独改掉；平台 `secret` 包只按表、行、用途绑定，不收调用方的附加数据 | 整份配置明文（sub2api 现状，`payment/load_balancer.go:317-347`）；把哈希放进绑定的位置：要改 `secret` 包的存储格式 |
| 报价和下单用同一条选择规则：轮询按（站点，付款方式）各自计数，取能接这笔的候选中的第 n 个；当天已用最少按参考汇率把各账户的用量精确换成站点货币再比 | 页面显示的就是订单落下的那个账户和总额；各站点、各付款方式的轮询互不干扰；不同币种的最小单位不能直接比较 | 页面显示最便宜的、下单另选（显示与扣款不一致）；全进程一个轮询计数（两个站点交替下单时各自永远落在同一个账户上）；按最小单位整数比较用量（100000 分人民币被当成比 18000 美分多） |
| 商户当天额度用一行计数守 | 账户范围读不到别的账户的订单，也锁不了商户行（加锁要过 UPDATE 策略）；一行计数既是读数又是串行点 | 每次按订单求和（sub2api `load_balancer.go:167-246`）：账户范围求不出，读后写也挡不住并发；咨询锁加平台范围求和：下单事务就得用平台范围 |
| 每 IP 的下单与失败限额用一行计数守：键是（站点，IP 分组），固定一小时窗口，在下单的事务里占名额 | 与商户当天额度同一个道理：账户范围读不到同 IP 其他账户的订单，行计数既是读数又是串行点；名额随订单一起提交或回滚，被账户自己的限额拒绝的请求不占；计数出错就上抛、不放行，多节点自然一致。固定窗口在边界上最多放行限额的两倍，换来的是一行一个 IP、不随事件增长；IPv6 按 /64 归并，换地址买不到新名额 | 进程内令牌桶（回调入口用）：N 个节点各放一份，故障注入测不到；Valkey 计数（登录前的限额用）：Valkey 不可用时按那边的设计退回进程内、会放行，与这里「计数故障不放行」相反（登录为了不把用户锁在门外，支付为了不放开试卡）；对 `orders.client_ip` 求和：账户范围读不到别人的订单，换平台范围就得让下单事务换范围 |
| 控制台充值操作用 `balance.top_up`，余额与订单读取用 `balance.view` | accounts 00010 给 owner、admin、finance 充值权限，viewer 仍只读；报价涉及付款选择，与创建、取消、补查共四项需要购买权限，见 [账户模块权限表](../accounts/README.md#管理接口) | 读写共用 balance.view：观察者会获得付款能力 |
| 回调地址带商户账户 ID，按它找配置 | 验签前不用解析请求体去猜订单（sub2api `handler/payment_webhook_handler.go:155-179` 先解析 `out_trade_no` 再找商户），同一渠道多个商户账户时没有歧义 | 每种渠道一个回调地址加"只有一个账户时才允许回退"（`service/payment_webhook_provider.go:96-113`） |
| 回调与任务用平台范围 | 回调没有操作者；`CreditPaid` 写 billing 的表，跨模块授权会打破"只写自己的表" | 每个站点一个任务角色 |
| USDT 走易支付兼容网关 | 不写第二个插件、不保管私钥；链上细节由网关处理 | 自建链上监听：重组、确认数、私钥都要自己做 |

## 实现拆分

按顺序；每个 PR 交付一件能单独评审、回滚的事，非生成代码不超过约 2000 行，超了按能力边界再拆：E1-2b 超了时，订单表、下单、取消和不入账的过期留在 E1-2b，回调处理器、`order_payments`、`discrepancies`、入账和过期后到账拆成 E1-2c。E1-2b 这一行就这么拆了：只下单、取消、不入账的过期和它们的测试已接近 2000 行非生成代码，加上回调、入账、待处理、过期后到账和控制台契约会明显超过 2000 行。与设计原话的两处差别：「我已付款」随 E1-2c，它要把渠道的已付变成入账，而 E1-2b 不入账；回调与入账加上测试后，控制台契约和每 IP 下单/失败限流另按能力边界交付 E1-2d；核心只接收服务端生成的 https 返回地址，控制台处理器需按 `State` 与 `Checkout` 映射重放应答。表的迁移随第一个用到它的 PR。E1-1（billing 的付费额度批次，`CreditPaid`）是 E1-2a 的前提；E1-3、E1-4 的渠道端到端验收和 E1-5 的补单入账均依赖 E1-2c；E1-2c 之后这三项可以并行；E1-6a 改 billing 的写路径，不与其他改 billing 写路径的 PR 同时开，E1-6b 在它之后。

| PR | 内容 | 验收测试 |
|---|---|---|
| E1-2a `feat(payments): configure merchant accounts and quote top-ups` | 表 `merchants`、`merchant_configs`、`merchant_methods`、`merchant_usage`、`settings` 和迁移；`Channel`、`Plugin`、`Caps`、登记、测试用的假渠道；商户账户的保存（校验、密钥与 `settings` 哈希的封装）；选商户账户与用量计数；手续费与汇率锁定（含 billing 的 `TopUpRate` 与充值缓冲）；`Options` | 总额复算与手续费反推的性质测试；比例超过 50%、额度乘汇率越界被拒；选商户账户的单笔、当天额度、全被过滤和查用量出错都拒绝；并发抢最后的当天额度只放行放得下的；直接改库里的网关地址后商户账户用不了；密钥不出现在日志和响应里；行级安全：账户范围读得到本站的付款方式和费率、读不到 `merchant_configs`，站长只看到本站的商户账户；站点 A 的账户范围读不到、改不了站点 B 的 `merchants` 与 `merchant_usage` 行（含 INSERT … ON CONFLICT 造行） |
| E1-2a2 `feat(payments): manage merchant accounts in the admin API` | 管理接口的商户账户契约与处理器（列出、读取、创建、修改；密钥只写不读，响应只有非密配置），站点支付设置与充值缓冲的读写；从 E1-2a 拆出，它已接近 2000 行 | 契约测试；密钥不出现在响应和审计里；主站行只有平台范围能改；修订号过期 409、字段错误 422 带指针 |
| E1-2b `feat(payments): take orders and cancel them before payment` | 表 `orders`、`order_events` 和 `settings` 的限额列与迁移；转移表；下单（账户咨询锁、待支付/取消/当天/首笔上限、链接检查、幂等键、校验服务端传入的 https 返回地址）、取消、不入账的过期（`payments.poll` 的第 2 步）与减回用量；有未完成订单时拒改商户身份字段 | 转移表的全部（从，到）组合；同键同请求返回同一订单（充值关闭后仍可重放）、同键异请求 409，同键并发只调用一次渠道；10 个并发下单只放行 3 个待支付，取消空出名额，取消 5 次后拒新单；当天上限与首笔上限，已付过的账户不再受首笔限制；`//`、`javascript:`、`data:`、相对链接被拒且订单失败、用量减回，NUL 被清掉；渠道答不上时订单留在待支付、用量不减、返回结果未知，写 checkout 的事务失败时不回成功（故障注入）；过期只发生一次、之后请渠道关单；取消与过期竞争只释放一次用量，跨 UTC 日释放创建日用量；有订单在途时改币种、身份设置、密钥被拒，改名和非身份设置可以；行级安全：账户只看到自己的订单与事件，也取消不了别人的 |
| E1-2c `feat(payments): credit orders from callbacks and queries` | 表 `order_payments`、`discrepancies` 和迁移；回调处理器（限流、配置缓存、无锁预读、查单金额为准、找不到订单与网关订单号冲突记待处理、提交后才应答）和入账；"我已付款"；过期前的查单与过期后到账（宽限期、到账汇率、自动入账期限）；补查窗口内已过期、已取消及有网关单号的失败订单的商户身份保护（[#390](https://github.com/TYCT-tyct/Axiom/issues/390)，真实渠道启用前完成） | 身份保护的窗口边界、关单失败/不支持与晚付；性质测试：重复、乱序、并发、丢失回调下批次恰好一个、账本平衡；金额不符、多笔付款、折算为 0 进待处理，币种不符不入账；通知与查单金额不同进待处理；验签通过找不到订单、网关订单号冲突都有待处理记录且不回 500；坏签名不写库；查单失败返回 502；提交失败时应答不是成功（故障注入）；宽限期内按锁定汇率、之后按到账汇率、超过期限进待处理，锁定的汇率列不变 |
| E1-2d `feat(payments): expose the console order contract` | 控制台订单契约与处理器：按站点域名生成 https 返回地址，不收浏览器传入的返回地址；每 IP 下单/失败限流，默认每小时 20/10 次，计数故障不放行；按订单 `State` 与 `Checkout` 映射重放应答（#389 评审第 10 项） | IP 限流边界（20/21、10/11 次，同 /64 与映射 IPv4 归并，窗口到期重置）、十个账户同 IP 并发只放行限额、计数故障（表上的触发器）下单被拒且渠道没被问、失败计数故障时订单仍是待支付；服务器生成返回地址（请求带 `return_url` 被契约拒绝，渠道收到的是登记 origin 加 `/console/balance`，且该路径在公共路由上命中控制台页面）；失败单重放回 422、无 checkout 的待支付单（结果未知）重放回 202 且不带支付链接、已取消的回 200；同键并发只问一次渠道；CSRF、会话、别的账户的订单读/取消/核验一律 404、渠道不被问；`Retry-After` |
| E1-3 `feat(payments): add the easypay protocol channel` | 易支付插件：配置项（[支付的「易支付协议插件」](../../docs/domains/payments.md#易支付协议插件)）、跳转与接口两种下单、验签、白名单、查单确认、关单、退款 | 按协议自写的签名向量；参数走私（下单参数里嵌 `trade_status`）被拒；多余参数、`pid` 不符被拒；GET 与 POST 回调；PHP 风格的字符串整数、明确失败码优先、回显订单号不符；相对支付链接补全、`//` 开头被拒；退款响应是 HTML、空、字符串 `code`；退款先按订单号、明确"订单不存在"才按网关订单号；全部经出站客户端，日志和链路里没有查询串；本机假网关的端到端 |
| E1-4 `feat(payments): take USDT through an easypay-compatible gateway` | Epusdt 作为易支付商户账户的接入与预设：`usdt.tron` 映射、数字 `pid`、法币金额；退款只能线下；接入前实测它的查单接口 | 用 Epusdt 官方镜像在本地部署后的下单、GET 回调、查单确认、入账；`money=100.0000` 精确解析；查单不可用时这个商户账户保存失败 |
| E1-5 `feat(payments): reconcile unfinished orders and daily channel statements` | `payments.poll` 的商户并发上限与熔断（每单退避和过期后补查由 E1-2c 提供）；`payments.reconcile`；待处理记录的管理页与告警 | 丢回调由补单入账；一笔订单在有效期内的查询次数符合退避；一个商户账户超时不挡其他账户，连续未知触发暂停和告警；不能关单的渠道过期后付款，在补查窗口内补到；现金分录与订单不符、用量计数与订单复算不符时出现待处理记录并告警；同一差额重复上报只留一行 |
| E1-6a `feat(billing): freeze paid credit as replayed events` | billing：冻结、解冻、退款三种带序号的交易，批次的冻结状态，重放、准入、到期都按冻结额处理；`FreezeRefund`、`SettleRefund`、`ReleaseRefund`（[billing 的「额度批次的分配与到期」](../billing/README.md#额度批次的分配与到期)） | 随机冻结、解冻、退款、扣费、到期，从头重放等于现场，且从任意位置续放相同（扩展 `TestReplayProperty`）；冻结期间并发调用花不掉冻结的额度；到期不作废冻结的额度；冻结后余额底线以下的扣费不动冻结额度；billing 不变量①–⑦（⑦ 随本 PR 加入）在冻结后成立 |
| E1-6b `feat(payments): refund through channels with a sending state` | 表 `refunds` 和迁移；发起、审批、发送中、查询、人工结论（凭证、第二人批准）与金额不符策略（默认人工，可选按实付折算）；`payments.poll` 的第 4、5 步；订单状态按成功退款合计 | 渠道超时 → 待确认 → 查询收敛到成功或失败；第一个事务后崩溃只发一次；发送中崩溃时不认单号的渠道不重发、进待处理，认单号的用同一单号重发；不认单号的渠道同一订单只有一笔在途；同一人不能把未知退款标为失败；同一幂等键只退一次；多次部分退款合计不超过入账额度；性质测试：任意汇率序列和拆分下 Σ退款 ≤ Σ实付，全退恰好等于净付款额；退不退手续费两种策略；失败在一个事务里解冻 |

**上线门槛**：主站打开"是否开放充值"之前，E1-3 在负责人提供的真实网关上做小额实测（下单、回调、查单确认、关单、退款、手续费的舍入、币种配置），结果记在 [#309](https://github.com/TYCT-tyct/Axiom/issues/309)；没有这份记录不打开。
