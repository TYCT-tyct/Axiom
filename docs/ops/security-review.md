# 安全审查记录

上线前的独立安全审查：查了什么、怎么查、发现了什么、哪些没查。规则本身在[安全](../security.md)；这里只记每次审查的证据和结论，结论要能被下一个人复核。

一次审查由写手之外的人或另一个模型做，只读；发现按阻断、应修、细节分级，阻断和应修在本轮修掉，细节能顺手修就修，其余连同理由记在下面，由编排者转成 Issue。每条发现先写能复现的测试再改代码；修不掉的写下位置和原因，不写成"已处理"。

## 2026-11 · M3 上线前（#325）

### 范围

`docs/security.md`「安全基线」点名的五组路径，都是上线前必须有人独立看一遍的：

1. 支付回调：验签、重放、金额与币种绑定、幂等（[支付的「回调与到账」](../domains/payments.md#回调与到账)）。
2. 会话与 OAuth、通行密钥：会话固定、CSRF、回调跳转（[账户的「登录与会话」](../domains/accounts.md#登录与会话)）。
3. 主密钥分发与节点间 mTLS（[平台的「凭证加密」](../../internal/platform/README.md#凭证加密)、[nodetls](../../internal/platform/nodetls/README.md)）。
4. 每一处出站请求：通知 Webhook、价格页抓取、供应商接口、对象存储（[平台的「出站 HTTP」](../../internal/platform/README.md#出站-http)）。
5. 出站邮件：头部注入（[mail](../../internal/platform/mail/README.md)）。

### 方法

- 只读代码和迁移，不运行被测服务；结论都指到文件行号，不靠"看起来没问题"。
- 每组路径先按威胁列攻击者能控制的输入，再顺着输入走到真正决定的那行代码，最后找对应的测试：没有测试覆盖的性质记为"缺测试"，测试覆盖但代码没做到的记为发现。
- 三个模型各查一组，写手复核每条发现的代码位置，能复现的写成测试。复核后不成立的写"不成立"和理由。
- 查的范围是规则，不是风格；枚举类检查（地址段、参数白名单）逐个对照 IANA 登记和协议原文。

### 结论

**没有阻断项。** 五组路径的规则都实现并有测试；本轮修了两处主密钥的类型和文件边界（见下），补了三段与代码不符的文档。出站请求的地址策略本身没有找到可绕过的路径：全部候选地址在拨号前校验、连接校验过的 IP、不跟随重定向、不用代理、IPv6 特殊段和元数据地址都在拒绝表里，测试覆盖 DNS 重绑定、TLS 主机名、响应头大小、代理环境变量和 101 升级。

### 发现与处理

| # | 级别 | 位置 | 发现 | 处理 |
|---|---|---|---|---|
| 1 | 应修 | `internal/platform/secret/keyring.go` 的 `readKeyFile` | 主密钥文件路径可以是指向别处的符号链接：`os.Open` 跟随链接，`Stat` 报的是目标的模式，于是任何 `0600` 的普通文件都能通过"必须是普通文件、组和其他人不可读"的检查。写路径 `replaceKeys` 用 `Lstat` 明确拒绝链接，两处规则不一致；文档写的是"文件必须是普通文件"。谁能让变量指向一个链接，谁就决定了节点用来封装的主密钥 | 已修：读取前 `Lstat`，链接和其他非普通文件（FIFO、设备）在启动时按"want a regular file"拒绝。回归 `TestKeyFileSymlinkRefused`（修前失败） |
| 2 | 应修 | `internal/platform/secret` 的 `MasterKeys`、`MACKeys` | 两个持有原始 32 字节密钥的类型没有任何格式化方法，`%v`/`%+v`/`%#v` 和 `%x` 都把密钥按十进制或十六进制打进日志，`slog.Any` 也一样。`Secret` 早就为同一件事实现了 `Format` 和 `LogValue`，这两个类型漏了 | 已修：按 `Secret` 的做法实现 `Format`（无效动词也覆盖，因为 fmt 对不适用动词会退回反射打印字段）和 `LogValue`。回归 `TestKeyMaterialFormatting`（修前失败） |
| 3 | 细节 | [支付的「回调与到账」](../domains/payments.md#回调与到账)第 1 条 | 文档写"POST 读表单或 JSON 原文"，代码只接受 `application/x-www-form-urlencoded`，带查询串的 POST 也拒绝；易支付协议没有定义 JSON 的签名串，按 JSON 投递的网关会一直 400 | 已改文档：POST 只读表单，并写明理由 |
| 4 | 细节 | [支付的「回调与到账」](../domains/payments.md#回调与到账)第 5 条 | 文档把"向网关再查一次"写成"回调伪造或网关被入侵时多一道关"，但站长可以自己配置商户账户的网关地址和密钥：对站长本人，通知和查单都来自他自己的服务器，这一关是自己查自己，挡不住他 | 已改文档：写清这一关挡的是没有密钥的第三方伪造；商户账户归谁配置才是平台能依靠的边界 |
| 5 | 细节 | `internal/platform/nodetls/README.md`「部署」 | 主密钥目录不能对组可写（`nodetls` 的目录检查），而发货的 `deploy/compose.yaml` 特意把 `/keys` 设成 `0770`（程序要能原子替换主密钥文件），默认的证书目录又是主密钥文件所在目录：打开节点监听（`AXIOM_NODE_ADDR`）而没另设 `AXIOM_NODE_TLS_DIR` 时核心正好在这条自相矛盾的配置上拒绝启动 | 已改文档：证书目录必须另设一个组不可写的目录（如卷下的子目录），不要用密钥环的卷根本身 |
| 6 | 细节 | `internal/accounts/passkeys.go` 的 `BeginPasskeyMFA` | 唯一没有来源 IP 限流的 begin 端点：拿到密码、停在 `pending_mfa` 的人可以反复调用，每次都插一行 `accounts.webauthn_flows`，清理每轮最多删 1000 行，进得比清得快。不是接管路径 | 未修：需要带容器的回归测试，本轮时间不够；记在这里待转 Issue。同级端点都走 `fromIP`，补上即可 |
| 7 | 细节 | `internal/accounts/login.go` 的登录路径 | 邮箱不存在时一次事务加一次假哈希，存在时两次事务。响应体和状态码一致，数据库往返次数不同，理论上能按延迟排名哪些地址注册过；IP 桶每站点每分钟 10 次把可利用性压得很低。文档说的是"不存在也算一次哈希"，这句成立 | 未修：要相等就得在未命中时多发一次空事务，为计时加代码比这条信道更糟；记在这里 |
| 8 | 细节 | `cmd/axiom/catalog.go` 的 `watch-page` | 价格页地址保存时只检查形状（https、无用户信息），不调用 `egress.Check`；其他后台可写的出站目标（SMTP、告警 Webhook、用户 Webhook、供应商、支付网关）都在保存时预检 | 未修：这是命令行运维操作，边界是操作员的 shell，拨号时仍会拒绝私网地址；为它在命令里接一个 egress 客户端不增加保护。要加时应在 `catalog` 的写入路径上接 |

### 逐项核对

每一行是"这条规则由哪行代码决定，哪条测试盯着它"。

| 威胁 | 规则 | 代码 | 测试 | 结论 |
|---|---|---|---|---|
| 支付回调验签 | 常量时间比较、签名串来自解析后的字段集、未知参数和重复参数拒绝、`&`/`=` 走私拒绝 | `hmac.Equal` 比较（`internal/payments/easypay_notify.go:48`）、白名单与唯一值（`:63`） | `TestEasyPaySignatureVectors`、`TestEasyPayCallbackSecurity`、`TestEasyPayCallbackFramingAndRotation`、`TestUnsignedOrAmbiguousCallbackWritesNothing` | 成立 |
| 密钥缺失时一律拒绝 | 商户配置打不开就不写任何数据 | `Open` 要求密钥非空（`internal/payments/easypay.go:91`）、回调取不到渠道回 503（`internal/payments/notify.go:45`） | `TestEasyPayConfiguration`、`TestCallbackChannelFailuresAreLogged` | 成立 |
| 回调请求体大小 | 正文和查询串各 1 MiB（`internal/payments/notify.go:16`、`:40`） | 同上 | `TestCallbackBodyBoundAndIngressLimit`、`TestEasyPayCallbackFramingAndRotation` | 成立 |
| 重放 | 同一（商户，网关订单号）只入账一次；状态不可再进"已支付"时按已记录的付款直接回成功 | `recordReceipt` 的 `ON CONFLICT` 与唯一索引（`internal/payments/credit.go:127`） | `TestCallbacksCommitOnceAndReplayWithoutQuery`、`TestCallbackPollAndVerifyRace`、`TestPaymentReplayProperty` | 成立 |
| 金额与币种绑定 | 用订单存下的总额和付款币种比较，不等就转待处理；查单的金额覆盖通知的金额 | `internal/payments/credit.go:100` | `TestPaymentConflictsAreRecordedAndAcknowledged`、`TestAmountMismatchPolicyAndManualCredit` | 成立 |
| 幂等与并发 | 订单行先锁再判断，条件更新，额度批次以订单 ID 为幂等键 | `internal/payments/credit.go:58`、`:111` | `TestCallbacksCommitOnceAndReplayWithoutQuery`、`TestCallbackPollAndVerifyRace`、`TestPaymentAndCreditRollbackTogether` | 成立 |
| 会话固定 | 登录、二次验证、改密、增删因素、绑定身份都换新会话；只存哈希 | `internal/accounts/session.go` 的发放路径、`internal/accounts/login.go:172` | `TestChangePasswordEndsOtherSessionsAndTokens`、`TestOneChallengeOneSession` | 成立 |
| 会话 Cookie | `__Host-` 前缀、HttpOnly、Secure、SameSite=Lax，只有登记的 `http://localhost` 去掉前缀和 Secure | `internal/accounts/session.go:367` | `TestRequestsMustComeFromTheSite` | 成立 |
| CSRF | Origin 精确等于登记值、必须是 JSON、会话 CSRF 令牌常量时间比较 | `checkCSRF`（`internal/accounts/session.go:350`） | `TestRequestsMustComeFromTheSite`、`console_guard_test.go`、`admin_guard_test.go` | 成立 |
| OAuth 状态与 nonce | state 与 nonce 一次性、只存哈希、绑定 `__Host-` Cookie、条件消耗 | `internal/accounts/oauth.go:103`、`:209`、`:222` | `assertSignsInBySubject`、`assertNoMergeByEmail`（`oauth_test.go`） | 成立 |
| OAuth 跳转 | 回调只回到站点登记的控制台域名，跳转目标是字面量；不使用 `next`、Referer 或 Host | `internal/accounts/oauth.go:414`、`cmd/axiom/console_oauth.go` | `TestOAuthStartOnAnotherConsoleDomainIsRefused`、`web/e2e/console-pages.spec.ts` | 成立 |
| 通行密钥 | RP ID 与 origin 来自登记的登录域名、要求用户在场、计数器只增不减、流程行一次性 | `internal/accounts/passkeys.go:82`、`:93`、`internal/accounts/webauthn.go:34` | `assertCounterRefusals`（`passkey_test.go`）、`TestPasskeySignInNeedsTheLoginDomain` | 成立 |
| 因素版本 | 每次改因素都加版本，会话每次请求重新核对 | `internal/accounts/factors.go`、`queries/console.sql` 的 `ValidSession` | `TestConsolePasswordSessionCannotSkipAPasskey`、`TestSessionDevicesLeaveOutInvalid` | 成立 |
| 主密钥信封 | 每行独立数据密钥、AAD 绑定表/行/用途、nonce 随机、解不开就失败 | `internal/platform/secret/envelope.go`、`rewrap.go` | `TestOpenRejectsTampering`、`TestLocationsDoNotCollide`、`FuzzOpen` | 成立 |
| 主密钥文件 | 普通文件、组和其他人不可读、重复版本和重复密钥拒绝、不打印路径和内容 | `readKeyFile`（本轮加 `Lstat`）、`internal/platform/secret/keyfile_write.go` | `TestKeyFilePermissions`、`TestKeyFilePaths`、`TestInvalidKeyFiles`、`TestKeyFileSymlinkRefused` | 本轮修复后成立 |
| 密钥不打印 | 持有密钥的类型都脱敏 | `Secret`、`MasterKeys`、`MACKeys` 的 `Format`/`LogValue` | `TestSecretFormatting`、`TestSecretSlog`、`TestKeyMaterialFormatting` | 本轮修复后成立 |
| 分发与激活 | 每个在线节点都报告了版本才激活；节点读不到就阻塞而不是跳过 | `internal/platform/secret/rotation.go:347` | `TestKeyringWaitsForEveryLiveNode`、`TestKeyringReportsOnlyFingerprintConfirmedVersions` | 成立 |
| 退役与迟到写 | 退役取排他 advisory 锁写墓碑；信封表的触发器取共享锁并检查标记 | `internal/platform/secret/rotation.go:445`、`pg/migrations/00023_envelope_key_fence.sql` | `TestKeyringRetirementRefusesStaleWrites`、`TestKeyringRetirementWaitsForAnOpenEnvelopeWrite`、`TestKeyringWillNotPruneAnUnfencedRetirement` | 成立 |
| 节点 mTLS | 最低 TLS 1.3、要求并对端证书、按用途校验整条链、私钥权限、错误不打印路径 | `internal/platform/nodetls/nodetls.go:33`、`:69`、`:141` | `TestMutualTLSRequiresACertificateFromBothSides`、`TestCertificatePurposeCoversTheWholeChain`、`TestClientVerifiesTheServersName`、`TestRefusesUnusableMaterial` | 成立（部署文档见发现 5） |
| SSRF：地址 | 拒绝私网、回环、链路本地、`0.0.0.0`、IPv4 映射、IPv6 特殊段、元数据地址和数字写法（`0x7f.1`、`2130706433`、结尾点） | `allowedAddress`、`numericHost`（`internal/platform/egress/address.go:46`、`:68`） | `TestAddressPolicy`、`TestHostParsing`、`TestCheckAppliesTheDialPolicy` | 成立 |
| SSRF：DNS 重绑定 | 先解析并校验每个答案，再连接校验过的 IP，不把名字交给第二次解析 | `resolve`、`addresses`（`internal/platform/egress/dial.go:55`、`:73`） | `TestDNSMustValidateEveryAnswerBeforeDial`、`TestDialPinsAddressesAndBoundsResolution`、`TestTCPRevalidatesDNSAndPinsTheConnection` | 成立 |
| SSRF：重定向与代理 | 不跟随重定向、不用代理、不改 Host、TLS 按主机名校验、101 拒绝 | `internal/platform/egress/client.go:121`、`:130`、`:143` | `TestLoopbackRedirectAndUserAgent`、`TestIgnoresProxyEnvironment`、`TestTLSKeepsHostnameAndVerifiesCertificate`、`TestRefusesProtocolSwitch` | 成立 |
| SSRF：出站调用方 | 通知 Webhook、告警 Webhook、用户 Webhook、OAuth 提供方、价格页与数据集、供应商接口、汇率、支付网关、SMTP 全部经同一个出站客户端，并各自有期限和响应体上限 | 见下表 | 各模块测试 | 成立 |
| 邮件头部注入 | 收件人和主题含控制字符拒绝、主题按 RFC 2047 编码、信封只收可打印 ASCII、折行后展开一致 | `internal/platform/mail/message.go:35`、`:61`、`internal/platform/mail/header.go` | `TestMessageValidation`、`TestMessageParses`、`TestSendEnvelope` | 成立 |

出站调用方逐个核对（都在请求上带期限，读正文时都有上限）：

| 调用方 | 位置 | 目的地由谁决定 |
|---|---|---|
| 告警 Webhook | `internal/observability/webhook.go:79` | 负责人；保存时 `egress.Check`（`cmd/axiom/admin_alerts.go:70`） |
| 用户通知 Webhook | `internal/accounts/notice_jobs.go:260` | 账户自己；保存时 `egress.Check`（`cmd/axiom/user_notices.go:39`） |
| OAuth 提供方 | `internal/accounts/oauth_provider.go:160` | 程序里的常量 |
| 数据集与价格页 | `internal/catalog/fetch.go:47`、`internal/catalog/page_fetch.go:176` | 数据集是常量；价格页是运维命令保存的 https 地址，拨号时校验（发现 8） |
| 供应商接口 | `internal/suppliers/internal/client/client.go:66` | 管理员保存的 origin，保存时 `egress.Check`（`internal/suppliers/admin_write.go:63`），路径只来自适配器常量 |
| 汇率 | `internal/billing/exchange_fetch.go:26` | 常量（欧洲央行） |
| 支付网关 | `internal/payments/easypay_api.go:18` | 管理员保存的 https 网关 |
| SMTP | `internal/platform/mail/smtp.go:160` | 部署配置或后台保存的地址，保存时 `egress.Check`（`cmd/axiom/admin_system.go:202`） |
| 对象存储 | `internal/platform/objectstore/request.go:323` | 只有部署环境变量；用它自己的 transport，不做公网地址校验（见下方决定） |

### 决定

| 决定 | 理由 | 不选 |
|---|---|---|
| 对象存储继续用自己的 transport，不套出站地址策略 | 目的地只有 `AXIOM_OBJECTSTORE_ENDPOINT` 一个来源，租户数据只进桶名和键，构造请求时只换 path 不换 host（`internal/platform/objectstore/request.go:110`）；自建 SeaweedFS 正常就在私网。套上公网策略会让发货的部署连不上自己的存储 | 强制公网地址：部署装不起来；再加一个"允许私网"的开关：等于把策略变成注释 |
| 出站地址预检放在 HTTP 边界，拨号处再校验一次 | 边界只做一次（保存时尽早报错），真正的强制在唯一的连接点；同一份地址在保存后可能变（DNS），所以拨号必须重新算。放到领域写入里会在同一条路径上再查一遍，多一次解析不增加保护 | 只在保存时校验：DNS 会变；只在拨号时校验：配置错误要到第一笔请求才暴露 |
| 主密钥文件拒绝符号链接，即使有人用链接挂载 | 文档和写路径都已经要求普通文件，读路径跟随链接会让"变量指向哪个文件"由链接决定；发货的部署把文件直接放在卷里，不用链接。用链接挂载的部署要在卷里放真实文件 | 允许链接：`Lstat` 之外的检查都只能看到目标，等于放弃这条规则 |

### 未覆盖

- **没有做外部渗透测试**：本轮是代码审查，没有对外网 IP 做真实探测，也没有做流量和时序实验。
- **联网搜索后端不存在**：服务端工具按设计拒绝（`internal/protocol/plugins.go` 的 `NoWebSearch`），没有可审的抓取路径。接入外部搜索服务时，它必须经 `egress` 并自带响应体上限与期限，届时按本轮"出站调用方"一表补一行。
- **代理与隧道出口没有实现**：`egress` 只有直连一种出口，`Exit` 的其它值在构造时拒绝；以后加隧道时，"连到校验过的 IP"这条要重新审。
- **没有在真实网关上验证**：支付回调的结论来自假的网关（`fakeserver` 与 `httptest`），真实易支付分支的字段差异只在录制样本里；SMTP 同理，只对着假服务器和 TLS 测试服务器跑过。
- **没有跑多节点激活**：分发的结论来自代码和容器测试，没有在真实多机部署上走一遍"分发、心跳、激活、退役"。
- **前端**：本次没有审 Web 应用的 CSP、第三方脚本和浏览器端令牌存放，只核对了跳转目标的白名单测试。
- **主密钥轮换的 2³² 次随机 nonce 上限**：代码不计数，属于部署纪律，本轮没有量化。
