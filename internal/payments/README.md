# payments

订单、收款渠道、回调、补单、退款、拒付、兑换码与对账的公开接口和不变量。行为和理由见[支付](../../docs/domains/payments.md)，记账、额度批次和冻结见[计费](../../docs/domains/billing.md)。签名以 godoc 为准，这里只列入口和约定。

## 接口

- **组装**：`Open(Config)` 接收按 `Kind` 登记的插件、数据库、平台范围、Keyring、出站客户端、accounts（拒付暂停账户）、`Alerts`（突增信号的 Sink）、`Instance`（信号键里的节点身份，与 `Alerts` 一起给）和 `Signals`（同一节点的监听器、任务和管理入口共用一份，否则一分钟的计数会被拆开）。`RegisterPoll`、`RegisterReconcile` 登记 `payments.poll` 和 `payments.reconcile`；`Notify()` 是 `GET/POST /api/payments/v1/notify/{merchant}` 的处理器。
- **充值**（账户范围）：`Options` 报价，与下单共用一条选择规则（`ranked`），但不推进轮询、不预留：中间没有别的订单插进来时，订单才落在报价的账户上；`CreateOrder`、`Cancel`、`GetOrder`、`ListOrders`（`(created_at,id)` 倒序，一页 1–200）、`Verify`（"我已付款"）。可等待的拒绝是 `Wait{Err, After}`，处理器据此写 `Retry-After`。`ParseMerchantID`、`ParseOrderID` 格式不对与不存在同为 `ErrNotFound`。
- **退款**：`Refund` 冻结并等待审批（`Key` 幂等）；`ApproveRefund`/`ApproveRefundTx` 记第一道或最终批准，提交后 `SendRefund`；`PrepareRejectRefund` 在决策事务前推进账本（单连接池不能在事务内再取连接），再 `RejectRefund`/`RejectRefundTx` 解冻；`GetRefund`、`ListRefunds`（一页订单的退款，一次查询）。运营 agent：`PrepareAgentRefund`（事务外推进分配）、`QuoteAgentRefundTx`（回滚的保存点里试一遍）、`RequestAgentRefundTx`（冻结并以负责人批准）、`AgentRefundOf`。`OrderOwnerTx` 读订单的站点和账户。
- **人工结论**：`Resolve` 保存提案与凭证，`GetResolution` 供另一人检视，`ApproveResolution` 按存储的内容执行。
- **管理**（站点范围）：`ListSiteOrders`、`ListSiteRefunds`；`ListDiscrepancies`、`GetDiscrepancy`、`InvestigateDiscrepancy`；`ReadSiteSettings`、`SetSiteSettings`。
- **商户账户**：`SaveMerchant` = `PrepareMerchant`（本地校验和事务外探测）+ `WriteMerchant`（在调用方事务里复核修订号与身份、封装密钥、记审计）；`ListMerchants`（没有密钥）、`DisableMerchant`（只改启用位，已停用时什么也不写）、`TestMerchant`（复用调用方的幂等事务读商户，再做 10 秒内的只读测试）。
- **兑换码**：`IssueCodes`（在调用方事务里生成并审计，明文只在这里返回）、`Redeem`（调用方先确认账户范围）、`ListCodes`、`DisableCode`、`ListRedemptions`。对外 ID 前缀：`mch_`、`ord_`、`rfd_`、`rdm_`、`rdn_`。

### 渠道接口

```go
type Plugin interface {
	Kind() Kind
	Open(c MerchantConfig) (Channel, error) // 只做本地校验；保存配置时也调用，不合法就拒绝保存
}
type MerchantConfig struct {
	ID MerchantID; Currency platform.Currency
	Settings []byte                   // 非密配置，规范 JSON，插件严格解析；核心已核对它的哈希
	Secrets  map[string]secret.Secret // 解封后的密钥，只在内存里；含轮换期的旧密钥
	Client   *egress.Client
}
type Channel interface {
	Caps() Caps // Query | Close | Refund | QueryRefund | RefundKey | Statements
	Methods() []Method // nil：管理员可配置任意付款方式
	Create(ctx context.Context, r CreateRequest) (Checkout, error)
	Verify(ctx context.Context, c Callback) (Notice, error) // 验签加商户身份；不相关的事件 Relevant=false
	Query(ctx context.Context, outTradeNo, tradeNo string) (Payment, error)
	Close(ctx context.Context, order OrderID, tradeNo string) error
	Refund(ctx context.Context, r RefundCall) (RefundState, error)
	QueryRefund(ctx context.Context, r RefundCall) (RefundState, error)
	Ack(ok bool) (status int, body []byte) // ok 只在事务提交后为真
}
// CreateRequest: Order, Merchant, Method, Upstream, Total, Subject, ReturnURL（核心校验 https）, NotifyURL（核心生成）, ClientIP, Mobile, ExpiresAt
// Checkout: TradeNo, PayURL, QRCode, ClientSecret（只交给浏览器，不存库）
// Notice: Relevant, Success, Order, TradeNo, Paid, Raw, Dispute *DisputeNotice（非 nil 时只走拒付）
// DisputeNotice: ID, PaymentIntent, Order（可空）, Status, Reason, Amount, Action（Open | Lost | Won | Warning）
// RefundCall: Refund, Order, TradeNo, ProviderRef, Amount, Reason, SentAt（首次发送时间，零值表示不知道）
// Payment{State Pending|Paid|Closed}；RefundState{State Succeeded|Failed|Pending, ProviderRef}；*Rejected 实现 error
```

- **错误只分两种**：`*Rejected` 是确定的失败或确定未发送；其他错误都是结果未知，调用方不得据此改为失败。
- **能力**：调用没有声明的能力是程序错误，panic。只有声明 `RefundKey`（渠道把 `RefundCall.Refund` 当退款单号、重发只退一次）的渠道，未知退款才能重发。当前没有插件声明 `Statements`。
- **金额**：`Notice.Paid`、`Payment.Paid` 由插件把网关文本精确解析成最小单位：`Minor == 0` 表示缺失（留给补查），`Minor == -1` 表示无效付款（转待处理）；没有币种字段的渠道填商户币种。
- **链接**：核心检查 `PayURL`、`QRCode`，只放行 `https`、`alipays://`、`weixin://`、`wxp://`；插件只负责补全单个 `/` 开头的相对链接。存库前去掉 NUL。
- **拒付**：插件把渠道状态归一成 `DisputeAction`；找订单、扣多少、改不改订单由核心和 billing 决定。
- **插件拿不到数据库**，错误和日志不带查询串、表单、应答正文和密钥。
- **身份字段**：插件可实现 `normalizeSettings`（身份比较和封装前归一化）和 `identitySettings`（订单在途时不能改的字段）；密钥和付款币种总在身份之内，所有 `settings` 都参与封装哈希。
- **契约**：每种插件的 `settings` 在管理契约里各有模式（`AlipaySettings`、`EasyPaySettings`、`WechatPaySettings`、`StripeSettings`），`TestSettingsKeepTheirContract` 对照插件结构和契约成员。

### 支付宝配置与边界

`Alipay{}`，`Kind` 是 `alipay`，币种固定 CNY，`Methods()` 为 nil。`settings`：`app_id`（必填，数字，至多 32 位）、`seller_id`（可省略，数字）、`gateway`（可省略，默认 `https://openapi.alipay.com/gateway.do`；沙箱 `https://openapi-sandbox.dl.alipaydev.com/gateway.do`；只接受无用户信息、查询串和片段的 HTTPS）。密钥：`private_key`（PKCS#8 或 PKCS#1，RSA ≥ 2048 位）、`public_key`（PEM 或裸 base64）、可选 `old_public_key`。`upstream` 只接受 `FAST_INSTANT_TRADE_PAY`、`QUICK_WAP_WAY`、`FACE_TO_FACE_PAYMENT`。`Caps`：`Query|Close|Refund|QueryRefund|RefundKey`。身份字段：`app_id`、`seller_id`、`gateway`。

### 易支付配置与边界

`EasyPay{}`，`Kind` 是 `easypay`，币种来自商户账户。`settings`：`preset`（省略为通用，或 `epusdt`、`ezpay`）、`gateway`、`pid`、`mode`（`redirect`/`api`）、`sign_type`（省略为 `MD5`）、`cids`（付款方式 → 通道号）、`query`、`close`、`refund`（省略为不支持）。密钥：`key`，轮换期可加 `old_key`（只验签）。能力按配置声明，从不声明 `RefundKey`、`QueryRefund`、`Statements`。身份字段：`preset`、`gateway`、`pid`、`sign_type`。

- `epusdt`：强制 `query`、`redirect`，拒绝接口下单、cid、关单和退款；币种 CNY/USD；`pid` 是无前导零的正 int64。
- `ezpay`：只接受 `preset`、`gateway`、`pid`（1–32 位字母、数字、下划线或连字符）；币种 USD；`Methods()` 只返回 `usdt.tron`，`upstream` 必须是 `usdt_trc20`；`Caps` 只有 `Query`；创建签名用 `ezPayCreateSign`，回调仍用 `easyPaySign`。
- 两个预设保存为启用前由 `SaveMerchant` 在事务外探测查单，失败信息不含应答正文、PID、查询串或密钥。

### 微信支付配置与边界

`WechatPay{}`，`Kind` 是 `wechatpay`，币种必须是 CNY，`Methods()` 为 nil。`settings`：`app_id`、`mch_id`、`serial_no`（16–64 位十六进制，归一成大写）、`platform_public_key_id`（公钥模式必填，`PUB_KEY_ID_` 开头）、`old_platform_public_key_id`、`gateway`（默认 `https://api.mch.weixin.qq.com`，HTTPS，无用户信息、查询串、片段和路径）。密钥：`private_key`（RSA ≥ 2048 位）、`api_v3_key`（恰好 32 字节）、`platform_certificate` 或 `platform_public_key`（至少一份；证书序列号从证书算）、可选的 `old_platform_certificate`、`old_platform_public_key`。`upstream` 只接受 `native`、`h5`。`Caps`：`Query|Close|Refund|QueryRefund|RefundKey`。`Ack` 成功 200 空正文，失败 500 和 `{"code":"FAIL","message":"失败"}`。身份字段：全部 `settings`。

### Stripe 配置与边界

`Stripe{}`，`Kind` 是 `stripe`，`settings` 是空对象（未知成员拒绝），币种来自商户账户。密钥：`secret_key`（`sk_` 或 `rk_`）、`webhook_secret`（`whsec_`）、可选 `old_webhook_secret`；不接受 `old_secret_key`。`upstream` 只接受 `card`、`alipay`、`wechat_pay`、`link`。`Caps`：`Query|Close|Refund|QueryRefund|RefundKey`。SDK 的 HTTP 经 `egress.Client`，没有期限时补 10 秒，不重试、不记日志；API 地址固定，只有测试能注入。

### billing 要提供的接口

payments 在自己的事务里调用（[总体架构的规则 5](../../docs/architecture.md#模块边界规则)）：

| 函数 | 约定 |
|---|---|
| `billing.TopUpRate` | 参考汇率版本和站点充值缓冲（没有行时 2%）；`Rate.Applied` 是精确分数，可能超出 int64。平台范围的只读事务里、下单事务之前读；汇率版本只追加 |
| `billing.CreditPaid` | 锁余额行，写付费批次（ID = 订单 ID 或待处理记录 ID）、充值分录和审计。同 ID 再调用时入账额度、实付、手续费一致就什么也不写，不一致报错 |
| `billing.PrepareRefund` / `FreezeRefund` / `QuoteRefund` | 订单锁之前无锁预放；锁余额行后写带序号的冻结事件，返回实际冻结额度；报价随冻结保存 |
| `billing.SettleRefund` / `ReleaseRefund` / `RefundRecordedAt` | 冻结转退款分录 / 解冻；重复调用由退款行的条件更新挡住；成功时间取账本时间 |
| `billing.ChargeBack` / `ReverseChargeBack` | 拒付扣回与冲正，以 `(批次, revision)` 为账本幂等键 |
| `billing.CreditRedeem` | `redeem` 来源批次，批次 ID = 兑换记录 ID |
| `billing.MerchantCash` / `MerchantBatches` / `ReadCredits` | 日对账按页批量读账 |

`PaidCredit` 带账户、站点、批次 ID、入账额度、商户账户（现金科目按它区分）、付款币种、实付、手续费和参考汇率版本 ID；成交汇率由 (实付 − 手续费) ÷ 入账额度 推出。订单的 `paid_total`、`paid_fee`、`credited` 与批次出自同一个 `PaidCredit`、同一事务。

## 不变量

| 不变量 | 靠什么 |
|---|---|
| 一笔订单至多入账一次；已支付 ⇔ 有 ID 等于订单 ID 的付费批次 | 状态的条件更新加批次主键，同一事务写；`CreditPaid` 同 ID 不同金额报错；日对账核对 |
| 提交后才回 `Ack(true)`；没通过验签什么都不写 | 处理器在提交返回后才调 `Ack(true)`；`Verify` 成功前不开写事务 |
| 验签通过的成功付款都有记录 | 找不到订单、网关订单号冲突都先写待处理（去重）再回成功 |
| 支持查单时，没有查询确认不入账，入账金额来自查单 | 回调与补单共用入账函数，它要求 `Payment{State: Paid}` 并用它的金额 |
| 一个商户账户的回调只能改它自己的订单 | 订单按（商户账户 ID，订单 ID）查找，用该账户的密钥验签 |
| 实付与总额按最小单位完全相等才入订单额度 | `Money` 是整数，比较用 `==` |
| 总额可从订单的列复算 | `total = ceil((credits × applied + fee_fixed) ÷ (1 − fee_ppm/10⁶))`，`math/big` 算一次；锁定的 `applied_*` 不覆盖，到账汇率另存 `arrival_*` |
| 订单状态只按设计表变化 | 转移表是代码里唯一的一份 |
| 限额在锁内检查，计数出错不放行 | 账户级计数在账户咨询锁内；商户当天额度和每 IP 次数在各自计数行的行锁内占用，随订单提交或回滚 |
| 选不出商户账户就不下单 | 过滤后为空、查用量出错都返回错误 |
| 一笔订单在途和成功的退款额度之和 ≤ 入账额度；退款金额之和 ≤ 净付款额，退完恰好相等 | 订单行锁下插入，部分唯一索引保证每单至多一笔在途；最后一笔取余数；billing 在余额行锁内核对累计现金 |
| 退款只按"已冻结 → 发送中 → 成功 / 失败 / 待确认 → 成功 / 失败"变化 | 条件更新与 billing 调用同事务；"发送中"在调渠道前单独提交；只有匹配尝试序号的结果能落定 |
| 未知退款只在 `RefundKey` 渠道上重发；未知从不变成失败 | 冻结时快照 `refund_key`，重发要求快照和当前都声明；只有 `*Rejected` 能让下单或退款变失败 |
| 请求人不能批准自己的退款，超额度要两名不同的人 | 冻结时快照 `second_required`；`refunds_first_approver`、`refunds_second_approval` 两个 CHECK；未声明额度的插入由 `BEFORE INSERT` 触发器按当前额度补快照 |
| 一个人不能落定未知或线下资金 | `Resolve` 的提案不可改，审批者不等于提出人 |
| 一笔拒付至多扣一次、至多冲正一次；扣回加成功退款 ≤ 入账额度；拒付不和在途退款同动一笔额度 | `order_disputes` 唯一键和 `state` 条件更新（`recorded → applied → reversed`，`won` 终态）；扣钱前在订单行锁下检查批次、在途退款和净额 |
| 拒付暂停账户只一次，胜诉不解除 | `suspended_at` 记在拒付行上；暂停与扣钱同事务 |
| 加锁顺序 | 下单：账户咨询锁 → IP 计数行 → 商户身份共享锁 → 用量行；下单失败：订单行 → IP 计数行 → 用量行；入账：商户身份共享锁 → 订单行 → 余额行 → 用量行；退款：商户身份共享锁 → 订单行 → 退款行 → 余额行；拒付：订单行 → 商户身份共享锁 → 余额行 → 账户行 → 告警锁。保存商户取同键的排他锁 |
| 密钥不离开密文，网关地址不能绕过保存改掉 | 解封位置 `{payments.merchant_configs, 商户 ID, "config"}`，密文里封着 `settings` 的 SHA-256，不一致就拒绝并告警 |
| 账户范围读不到商户配置和密文 | `merchant_configs` 只对站点和平台范围开放 |
| 一个兑换码至多兑一次，同账户同键至多一笔；到期在锁后判断 | 账户咨询锁（命名空间 0x52）、码行 `FOR UPDATE`、带状态和 `clock_timestamp()` 的条件更新、`code_id` 与 `(account_id, idem_key)` 唯一 |
| 兑换限流不被并发和跨节点超过，计数出错拒绝 | 独立已提交事务里 `INSERT … ON CONFLICT DO UPDATE … WHERE attempts + 1 <= 限额` |
| 码的明文不落库 | 只存 SHA-256 和末四位；`RedeemCode.Code` 只在 `IssueCodes` 的返回值里有 |

咨询锁的键都由完整 ID 的散列派生（不用 UUIDv7 的时间前缀），命名空间：商户身份 0x4d、账户订单 0x4f、兑换 0x52。

## 数据

schema `payments`，每张表 ENABLE 并 FORCE 行级安全（[平台的「范围」](../platform/README.md#范围)），表达式见 [pg 的决策与理由](../platform/pg/README.md#决策与理由)。

| 表 | 用途与范围 |
|---|---|
| `merchants`、`merchant_methods`、`settings` | 站点公开配置：平台和站点范围读写，账户范围只读所属站点（来自账户授权）；主站行只有平台范围能改。商户不删除，只停用 |
| `merchant_configs` | 非密 `settings` 明文和密文；只有站点和平台范围；拒绝写入已退役的 `key_version` |
| `merchant_usage`、`ip_usage`、`redeem_attempts` | 共享计数：读、INSERT、UPDATE 都限于该行站点，应用只有计数列的 UPDATE、没有 DELETE；子表以 `(merchant_id, site_id)` 引用 `merchants`，站点 A 不能为 B 的商户造行 |
| `orders`、`order_payments`、`order_events`、`order_disputes`、`refunds`、`redeem_redemptions` | 账户数据；`order_events` 只插入；`order_disputes` 只允许平台范围写；`refunds` 只允许站点和平台范围写 |
| `resolutions`、`query_health`、`discrepancies`、`redeem_codes` | 站点数据；`resolutions` 应用只能更新 `approved_by`；`redeem_codes` 账户范围读不到 |

- 订单、退款、订单事件与账本同寿命，不随账户删除。人工另行入账的批次 ID 用待处理记录的 ID。
- 账户、站点、汇率版本只按 ID 引用，不建跨模块外键（[总体架构的规则 1](../../docs/architecture.md#模块边界规则)）。
- `statement_lines` 随第一个声明 `Statements` 的插件加。
