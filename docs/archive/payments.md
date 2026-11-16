# 支付：调研存档

[支付](../domains/payments.md)里的决定背后的调研证据。这里不是现行规则；系统现在的样子以领域文档和[模块 README](../../internal/payments/README.md) 为准。

## 参考实现对照

设计时读的是 sub2api 提交 `8f2cbb5`（`backend/internal/` 下的路径；支付宝按 `a3298b46` 核对）和 new-api 提交 `32e5874`，2026-10-19 查。照做的部分：

| Axiom 的行为 | sub2api 的出处 |
|---|---|
| 商户账户、付款方式、按种类构造插件、`Provider` 及扩展接口 | `ent/schema/payment_provider_instance.go:31`、`payment/types.go:14-24`、`:217-263`、`payment/provider/factory.go:10-25` |
| 出站 10 秒、响应体 1 MiB | `payment/provider/easypay.go:29-30`、`:674` |
| 先落订单再调渠道 | `service/payment_order.go:108-119`、`:411-499` |
| 订单有效期 30 分钟、待支付上限 3、取消限流 | `service/payment_config_service.go:47-48`、`service/payment_order.go:253-266`、`service/payment_order_lifecycle.go:37-97` |
| 每天上限、单笔上下限 | `service/payment_order.go:336-358`、`:122-142` |
| 过期后 5 分钟宽限期 | `service/payment_fulfillment.go:181-190`、`service/payment_service.go:42` |
| 负载均衡（轮询、当天最少、当天额度含待支付） | `payment/load_balancer.go:79-115`、`:167-246`、`:273-293` |
| 回调读请求 1 MiB、验签、查询确认、提交后回 `success` | `handler/payment_webhook_handler.go:26-27`、`:71-84`、`:106-116`、`:126-130`、`:215-226`，`service/payment_fulfillment.go:60-85` |
| 易支付网关后缀、签名、白名单、相对链接、宽松读数、查单、退款 | `easypay.go:72-96`、`:128-214`、`:216-255`、`:271-304`、`:332-407`、`:464-506`、`:508-574`、`:637-648`、`:681-727`；白名单起因是 sub2api issue #7881 |
| 补单、"我已付款"、过期与关单 | `service/payment_order_expiry_service.go:15-22`、`:89-119`，`payment_order_lifecycle.go:144-227`、`:275-339`、`:379-399` |
| 退款三段式（准入、执行、待确认）、能查就查、不能就转人工 | `service/payment_refund.go:208-256`、`:307-358`、`:431-489`、`:583-657`、`:804-857` |
| 商户账户允许用户申请退款 | `ent/schema/payment_provider_instance.go:54-57` |
| 有在途订单时不能改身份 | `service/payment_config_providers.go:121-165`、`:297-329` |
| 支付宝退款查询 10 分钟宽限 | `payment/provider/alipay.go:30-31`、`:548-573` |
| 微信退款先查原单金额 | `payment/provider/wxpay.go:458-470`、`:535-548` |

刻意偏离的部分（理由见领域文档的「决策与理由」）：

| sub2api 的做法 | 出处 |
|---|---|
| 金额 `decimal(20,2)` 加 `float64`，0.01 元容差；易支付 0.005 容差 | `ent/schema/payment_order.go:46-56`、`service/payment_service.go:47`、`easypay.go:32` |
| 手续费是全站一个比例，按"充值金额 + 充值金额 × 比例"算 | `payment_config_service.go` 的 `RECHARGE_FEE_RATE`、`payment/fee.go:12-22` |
| 订单号"前缀 + 日期 + 随机数"，查重 5 次 | `service/payment_service.go:58-62`、`payment_order.go:238-251` |
| 下单出错就标失败 | `payment_order.go:112-118` |
| 取消前查单 | `payment_order_lifecycle.go:99-142` |
| 取消次数查询出错时放行；查用量出错时放行；全被过滤时退回全部候选 | `payment_order_lifecycle.go:50-52`、`load_balancer.go:199-201`、`:105-110` |
| 商户配置明文 JSON，解不开当作空配置 | `payment/load_balancer.go:317-347` |
| 回调先解析 `out_trade_no` 再找商户；按渠道一个回调地址 | `handler/payment_webhook_handler.go:155-179`、`service/payment_webhook_provider.go:32-113` |
| 找不到订单回成功只记警告；金额不符只记审计 | `payment_webhook_handler.go:132-146`、`payment_fulfillment.go:140-143` |
| 已支付 → 充值中 → 已完成，加 5 分钟租约 | `service/payment_fulfillment.go:178-215`、`:314-363`、`:907-952` |
| 超过宽限期只记审计不入账；同订单第二笔付款直接回成功 | `payment_fulfillment.go:217-242` |
| 退款状态挂在订单上；先扣余额再调渠道 | `payment/types.go:35-40`、`service/payment_refund.go:315-330`、`:866-883` |
| 按订单金额比例退 | `service/payment_amounts.go:35-48` |
| 支付宝用 SDK、桌面先试当面付、退款单号用订单号加时间戳 | `alipay.go:14`、`:81-96`、`:173-211`、`:508-523` |
| 微信 `ABNORMAL` 当失败；用请求参数选 Native/H5/JSAPI | `wxpay.go:171-236`、`:500-515` |
| Stripe 退款幂等键 `re-<订单>-<金额>`；缺退款号时取最新一笔 | `provider/stripe.go:129`、`:233`、`:271-279` |
| 兑换只数失败、按用户、Redis 出错放行 | `service/redeem_service.go:28`、`:336-352`、`:446-452` |

new-api：易支付到账是行锁加一个事务（`model/topup.go:189-219`）；Stripe 用固定 Price ID 乘数量（`controller/topup_stripe.go:358-395`），用 SDK 的 `ConstructEventWithOptions` 验签（`:181-184`）；兑换码明文存 `key` 列（`model/redemption.go:16`），兑换是锁行加状态条件更新（`:136-180`），生成按名称和数量（`controller/redemption.go:75-124`），管理页把删除和状态更新放在一起（`:206-231`）；没有兑换限流。两家都没有收据或发票（new-api `model/topup.go:15`、`controller/topup.go:528`；sub2api `service/payment_order.go:856`、`handler/payment_handler.go:342`）。

外部规则的出处：OpenRouter《FAQ》和《Terms of Service》§4.1（未用额度原路退回、不退平台费、加密货币不可退，2026-10-29 核对）；Stripe《Refund and cancel payments》（退款不退手续费）；支付宝《电脑网站支付申请条件》；微信支付《Native 支付》《支付结果通知》（回调重试约 24 小时）和《退款常见问题》（`CLOSED` 是退款失败）。

## USDT 网关选择

2026-10-29 按源码、README 和发布页比较自托管网关。"商户查单"指带商户身份（PID/密钥）、按我们的订单号查、返回法币金额的接口，下单应答丢失后能补单。

| 网关 | 版本 | 协议 | 商户查单 | 结论 |
|---|---|---|---|---|
| [Epusdt](https://github.com/GMWalletApp/epusdt/releases/tag/v2.0.0) | v2.0.0（2026-07-23） | 易支付 `submit.php` | 无；只有公开的 `GET /pay/check-status/{trade_id}` | 不满足 |
| [BEpusdt](https://github.com/v03413/BEpusdt/releases/tag/v1.24.3) | v1.24.3（2026-10-21） | 易支付 `submit.php` 与 `/api/v1/order/*` | 无；`POST /api/v1/pay/info` 只按 `trade_id` 且不验商户 | 不满足 |
| [UPAY_PRO](https://github.com/wangegou/UPAY_PRO/releases/tag/v1.1.27) | v1.1.27（2026-02-09） | 自有 `/create_order` | 无；只有公开的 `check-status` | 不满足 |
| [UPay](https://github.com/UPay-USDT/USDT-Payment-Gateway/releases/tag/v1.1.2) | v1.1.2（2024-01-15） | 自有 | 有 `search`，但停更 | 不选 |
| [EzPay](https://github.com/opinework/ezpay/releases/tag/v1.1.9) | v1.1.9（2026-10-18，GPL-3.0） | 易支付 `submit.php` | `GET /api.php?act=order`，`pid`+`key`+`out_trade_no`，返回 `money`、`trade_status`、`trade_no` | 选用 |

EzPay 是唯一同时满足自托管、维护中、易支付兼容和商户查单的候选；自带 Dockerfile 和 docker-compose，SQLite/MySQL 均可，USD 结算，支持 TRC20。代价是只收 USD、没有退款和关单接口、回调多三个签名字段、查单必须 GET。

EzPay v1.1.9 源码（提交 `0912d988146ea83a1946b5fd963d104ab111b088`）：`submit.php` 在 `main.go:165`，`GET /api.php` 在 `main.go:173`，查单在 `internal/handler/epay.go:475`，应答的 `money`/`trade_status` 在 `:545`，`act=refund` 明确拒绝退款在 `:297`；创建签名按 RFC 3986（`internal/util/sign.go:42`），回调签名用原始值（`:269`）；回调参数在 `internal/service/notify.go:82`，回调地址的 SSRF 检查拒绝 localhost 和私网地址（`:132`），所以本机回调测试要用可公网访问的地址或 host 网络。没有"手动完成订单"的商户接口，补单只能靠查单。BEpusdt 的 EPay 路由只有 `submit.php`（`app/router/epay.go:8`，提交 `07164853`），`Info` 只按 `trade_id` 读单（`app/handler/epusdt/epusdt.go:388`）；UPAY_PRO 只有下单和公开状态（`web/web.go:731`、`:739`，提交 `a2327836`）。

官方镜像实测：2026-11-02 在 `ezpay:test-v1.1.9`（SQLite，host 网络）上通过 `TestEzPayOfficialImage`：保存探测、下单、查单、丢回调补单入账，以及另一笔直接下单的真实回调送达和验签。除测试注释里的两个变量外，还要设 `AXIOM_EZPAY_TEST_KEY` 和 `AXIOM_EZPAY_TEST_ADMIN_PASSWORD`，`AXIOM_EZPAY_TEST_PID` 默认 `1001`。

## Epusdt 的兼容证据

2026-10-29 复核：Epusdt 没有任何已发布版本提供易支付商户查单。EPay 兼容路由只有 `submit.php`（v2.0.0 `src/route/router.go:98`，提交 `619cecb1`）；唯一的公开查询 `GET /pay/check-status/{trade_id}` 只返回网关交易号和状态，不验商户、不认我们的订单号、不带法币金额（`src/controller/comm/pay_controller.go:68`）。按全部发布标签（v0.0.1 至 v2.0.0，如 v0.0.3 的 `e69cade7`、v1.0.10 的 `d564ef6f`）和 master `aed4a970`（2026-07-29）核对都是如此；官方文档《Payment API》（2026-08-22）只列 `check-status`；Docker Hub 最新标签仍是 v2.0.0。

本机实测 `gmwallet/epusdt@sha256:9d2fef82fb875e83f968870bee010ce593ef7bcb5362d09ef1c7f871fa27c55e`（v2.0.0）：预设前缀下的 `api.php?act=order` 返回 404，根路径的 `api.php` 返回 HTML，用镜像自带的默认商户签名后结果相同，所以启用的商户保存按设计被拒绝。`TestEpusdtOfficialImageQueryGate` 重跑这条检查，`TestEpusdtPublicStatusCannotAuthorizeSave` 证明公开状态即使回答已支付也不能让商户保存。
