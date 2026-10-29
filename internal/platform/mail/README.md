# mail

出站邮件：调用方写好正文，这里把它经 SMTP 发出。普通投递每次是一个 River 任务（`mail.send`，`notifications` 队列），按失败能否重试分别重试或放弃，队列投递对同一收件人一分钟最多一封。

投递放在 `platform` 而不是 accounts：收件人、模板、站点品牌和多语言属于账户流程（[accounts 的「给用户的邮件」](../../../docs/domains/accounts.md#给用户的邮件)），这里只有各模块共用的发送能力。走队列而不是进程内的 channel：任务和触发它的写入在同一个事务里入队，重启不丢，重试次数和最后错误由 River 记下（sub2api 的 `email_queue_service.go` 用 channel，重启即丢，2026-10）。

## 配置

| 变量 | 默认 | 说明 |
|---|---|---|
| `AXIOM_SMTP_HOST` | — | 提交服务器的主机名，如 `smtp.example.com`；不带协议和端口 |
| `AXIOM_SMTP_PORT` | `587` | 提交端口；`465` 是从第一个字节起就是 TLS，其余端口用 STARTTLS 升级 |
| `AXIOM_SMTP_USERNAME` | — | 账号；和密码要么都设，要么都不设（不设用于按地址放行的中继） |
| `AXIOM_SMTP_PASSWORD` | — | 密码或服务商的应用专用密码；以 `secret.Secret` 保存，任何格式都不打印 |
| `AXIOM_SMTP_FROM` | — | 发件人，一个地址，可带显示名：`Axiom <no-reply@example.com>` |

平台管理员可用 `StoredConfig`、`SaveConfig` 保存共享的 SMTP 覆盖，组装层在启动注册 worker 前读取；无覆盖仍使用环境变量。保存不改变在跑的 sender，API 返回 `restart_required`，重启节点后生效。显式禁用也保存覆盖，因此不会意外回落到环境变量。迁移 platform 00017 的 `smtp_config` 只限平台范围；配置整包加密，AAD 是表名、全零单例 ID 与 purpose=config，纳入主密钥轮换清单。API 不返回密码，审计只记 enabled；省略密码仅在连接目标未变时保留，改服务器、端口或用户名必须重填。保存校验失败返回 `ErrInvalidConfig`，数据库和解封失败原样上抛，不能误报为输入错误。

`TestConfig` 使用原 sender 同步发固定测试邮件，返回 accepted/rejected/failed/disabled；错误原文、凭证与收件人不进入响应和审计。accepted 是 SMTP 接受；网络错误可能已送达，不能当作确定未送达。配置测试的临时 sender 使用独立会话预算，不占用普通邮件的共享收件人冷却；HTTP 层限会话频率、要求 MFA/CSRF/幂等/理由，重放已提交结果不重发；SMTP 接受后数据库提交失败仍可能重发，测试邮件可重复。测试复用 sender 的受控信任库连接真实 TLS 测试服务器，兼容各平台且不放松生产证书校验。

`Mailer.Configured` 让账户流程在查用户之前统一判断此节点能否入队。

没有设置任何一项时邮件关闭：启动日志写 `email disabled`，`Enqueue` 返回 `ErrNotConfigured`，节点的其他功能照常。设置了任意一项就要求整套配置，缺项或取值不合法时启动失败，并一次报出全部问题；错误只写变量名和期望的格式，不写取值。发件人同时出现在信封和 `From` 头里，所以两者都要在启动时写得出来：信封放不下的地址，以及带显示名时整行折不进 998 字符的 `From` 头，都在这里失败（只有 ASCII 能进信封路径，见「传输」）。发信域名的 SPF、DKIM、DMARC 仍待配置（[accounts 的「待定问题」](../../../docs/domains/accounts.md#待定问题)）。

## 传输

- **始终加密**：端口 465 直接 TLS，其余端口在发出任何命令前用 STARTTLS 升级；服务器不提供 STARTTLS 就报错，不退回明文。证书按主机名校验，没有跳过校验的开关：私有 CA 放进系统的信任库，而不是关掉校验（new-api 的 `SMTPInsecureSkipVerify` 会让任何网络位置读到密码和全部邮件）。最低 TLS 1.2。
- **认证**：用户名和密码都设置时，在 TLS 之后用 `smtp.PlainAuth`；只设一个在启动时就报错。
- **握手**：读完 220 问候后立刻发 EHLO，失败再退到 HELO，并把这一步的错误单独报出来（`mail: hello: ...`），而不是留到 `MAIL FROM` 时才由服务器回复。EHLO 和 HELO 都被 5xx 拒绝才算拒绝，4xx 交给重试。
- **信封**：`MAIL FROM` 和 `RCPT TO` 里的地址按 RFC 5321 的 mailbox 语法写：本地部分是 dot-atom 就原样写，否则加引号并转义 `"` 和 `\`。只有可打印 ASCII 能进信封（不提供 SMTPUTF8），整个路径不超过 256 字节，写不出的地址在发送前就拒绝。
- **超时**：只有一个期限：整次会话 30 秒，从拨号算起，DNS、连接、握手、TLS 和每一行回复都在其中。拨号另有 10 秒的上限，只覆盖连接本身。期限到了关闭连接，阻塞中的读立即返回，错误记为 `context.DeadlineExceeded`；连接自己先出错（对端关闭、RST）时原样报出，不会被当成超时。任务被取消（队列停止）同样关闭连接，错误记为 `context.Canceled`。

## 消息

调用方给收件人、主题、纯文本和 HTML 两份正文；两份都必需，没有模板。构建出的消息：

- `From`、`To`、`Subject`、`Date`（RFC 1123Z，UTC）、`Message-ID`（随机串 `@` 发件域）、`MIME-Version`、`Content-Type`；
- 主题和显示名是可打印 ASCII 就原样写，否则按 RFC 2047 编成 UTF-8 的 encoded-word，每个不超过 75 字符，多字节字符不在词中间切断；
- 头部在空格处折行，续行以那个空格开头（RFC 5322 §2.2.3），展开后与原文一致；行宽 78 字符，超过 998 字符的值拒绝（§2.1.1）。没有空格可折的超长词写不出，入队时就报 `ErrInvalidMessage`；
- 两份正文是 `multipart/alternative` 的两个部分，quoted-printable 编码，中文和长行都不会被中继改写；
- 所有换行都是 CRLF（RFC 5322 §2.3）。

主题为空或含控制字符、收件人不是单个地址或含控制字符、任一正文为空都在入队时拒绝（`ErrInvalidMessage`）：头部里的换行会让调用方注入自己的头，空正文发出去也没有意义。收件人和主题还必须是发送时写得出来的：`Enqueue` 用与 `Send` 同一个渲染函数试写 `To` 和 `Subject`，信封放不下的地址、折不进 998 字符的值在这里就返回 `ErrInvalidMessage`，不留 outbox 行也不排任务——否则这封信会以同样的原因被取消，收件人那一行还会被删掉。

## 队列与重试

普通任务种类 `mail.send`，`notifications` 队列，最多尝试 5 次：River 的默认退避约为 16 秒、81 秒、256 秒、625 秒（五次共约 16 分钟），覆盖服务商几分钟的故障；用完仍失败的任务被丢弃，由队列的扫描报告（`queue.job_discarded` 和 `axiom.pg.queue.discarded_jobs`，[observability 的「监控与告警」](../../../docs/domains/observability.md#监控与告警)）。

邮件正文不进任务参数：River 的任务行不属于任何租户，没有策略覆盖，所以地址、主题和正文都存在 `platform.mail_outbox`，普通任务参数只有那一行的 id。这张表开了 `FORCE ROW LEVEL SECURITY`，策略使用 [pg 的账户范围表达式](../pg/README.md#决策与理由)：调用方在租户的事务里入队，`WITH CHECK` 挡住替别的租户写的可能，读取同样受它限制（[平台基础设施的「租户隔离」](../README.md#范围)）。

工作进程手里只有 id，没有租户，所以先用 `platform.mail_tenant(id)` 查到这一行的 `site_id`、`account_id`，再在该租户的作用域里读正文——这个函数是 `SECURITY DEFINER`、属主 `axiom_lookup`、只授权给应用，只返回这两个租户列，而这个角色只被授予 `id`、`site_id`、`account_id` 三列（`id` 是函数按它匹配的那一列，不能少；与 accounts 模块的登录前查找同一套做法，见 [accounts 的迁移](../../accounts/migrations/00002_lookup.sql)）。`pg.TenantTx` 先以空作用域查一次，再换成查到的租户，`fn` 在其中运行。

- 任务开始时就查不到这一行，说明已经发过或已被删除：记 `mail skipped: the queued message is gone`，任务完成。
- 网络错误、超时、4xx 回复由 River 重试，**消息留在表里**，重试时照样读得到；5xx 回复（`*textproto.Error` 500–599）用 `river.JobCancel` 结束，同样的消息再发一次也会被同样拒绝。读 outbox 行本身失败（数据库连不上、语句超时）也算可重试。
- 参数本身不合法（地址写不进信封、头部写不下）同样取消，不重试。这两类现在都在入队时挡住，走到工作进程的只剩被改过或旧版本留下的行。
- 交给 River 的错误只说明类别（`errSendFailed`、`errSendRefused`），不带收件人、主题、正文，也不带服务器的回复：River 把它原样写进 `river.river_job.errors`（jsonb 数组，每次尝试一项），而任务行和这一列不属于任何租户，没有策略覆盖。原文——包括回复里被拒绝的那个地址——只进工作进程的日志行。
- 发出或被永久拒绝后，这一行在同一租户的作用域里删除；删除失败只记 `mail not deleted from the outbox`，任务结果不变——邮件已经发了，重试只会再发一次。
- 至少一次：任务在邮件已被服务器接受、但还没标成完成时节点崩溃，会再发一次；调用方的流程要能容忍重复的邮件。被丢弃的任务（5 次都失败）不删这一行，供运维排查，清理策略待定。

## 限流

同一收件人（地址不分大小写）一分钟内只发一封。配置了 Valkey 时计数在共享库里：每个收件人一个键，主体是小写地址的 HMAC-SHA256 前 16 字节（密钥用 HKDF 从 `serve` 启动时读一次的主密钥快照派生，标签 `axiom kv mail v1`，与登录键同一做法），值是下一次可发的时刻（毫秒），到期时间是一分钟的相对时长。脚本在同一个原子步骤里比较并写入，所以两个节点同时准入同一个地址时只有一个成功。站点字段写成零：一个邮箱就是一个收件人，与进程内实现的作用域相同；键一分钟到期，不参与按站点的清理。键里没有明文地址（[kv 的「键」](../kv/README.md#键)）。

Valkey 不可用（出错、期限到、熔断打开）时退回进程内的桶，与没有 Valkey 时相同：每个节点对每个收件人每分钟最多一封。共享准入成功时也会记进本节点的桶，所以随后 Valkey 出错，切换后的本地判断仍保留这次冷却；共享拒绝不写本地。故障期间 N 个节点合计至多放大到 N 倍，恢复后以共享键为准，兜底期间的准入不合并回去。准入在发送之前写入，发送失败也占着这一分钟，与原来相同；被限流的任务用 `river.JobSnooze` 睡到一分钟之后，不消耗尝试次数（消息留在表里）。

## 投递结果

发出去记 `mail sent`（`job_id`、`attempt`、`to`、`message_id`），失败记 `mail send failed`（另加 `permanent` 和 `err`）。正文、主题和密码从不进日志；收件人地址进，因为运维要靠它和发信服务商的日志对上。日志不属于任何一张表，所以这里是唯一记得下原文的地方。

持久记录是 River 的任务行（状态、尝试次数、最后错误）加上 `platform.mail_outbox` 里的那一行：任务行说这次投递怎么样，outbox 行说发的是什么、属于哪个租户，两者用任务参数里的 id 相连。正文随发随删（见上一节），不长期留在库里。任务行里的「最后错误」是上面那两个不带值的类别，不是服务器的回复：运维要的地址和 Message-ID 在日志行里。

## 不变量

| 不变量 | 靠什么保证 |
|---|---|
| 邮件不会明文发出 | 465 之外必须 STARTTLS，服务器不提供就失败；没有跳过校验的配置 |
| 邮件内容不进入不属于任何租户的表 | 正文在 `platform.mail_outbox`，任务参数只有 outbox id（回执模式另有回执 id）；表使用 [pg 的账户范围表达式](../pg/README.md#决策与理由)，别的站点和空作用域都读不到 |
| 一个租户不能替另一个租户入队 | 同一策略的 `WITH CHECK` 比较 `site_id`、`account_id`，租户从调用方的事务里来 |
| 一封信一个任务 | `Enqueue` 在调用方的事务里插一行 outbox、一个 `mail.send`，参数是那一行的 id |
| 队列投递对同一收件人一分钟最多一封 | 有 Valkey 时每个规范化地址一个原子准入键，不可用时退回进程内 `limiter`；限流的任务 snooze，不耗尝试次数 |
| 共享键里没有明文收件人 | 键的主体是 HMAC-SHA256 的前 16 字节，密钥由主密钥快照派生；值只是时刻 |
| 5xx 不重试 | `wrapRejected` 把 500–599 包成 `ErrRejected`，工作函数转成 `JobCancel` |
| 信封和头部写的是同一个地址 | `mailbox` 同时给 `MAIL FROM`、`RCPT TO` 和 `From`、`To` 用 |
| 写给 River 的错误不带租户数据 | 工作函数只返回 `errSendFailed`、`errSendRefused` 和 `JobSnooze`；`mailbox`、`validate`、找不到地址时的限流错误都不引用取值 |
| 密码和正文不进日志和错误 | `secret.Secret` 的脱敏；错误只带主机名、步骤和服务器回复（服务器回复只进日志，不进 River） |

## 与参考实现的差异

| 参考 | 沿用 | 不同 |
|---|---|---|
| new-api `common/email.go` | 465 用隐式 TLS；账号和密码都设才认证；`Date`、`Message-ID` 头 | new-api 的 STARTTLS 是开关、可以关掉校验、正文只有一段 HTML；这里强制加密、没有跳过校验的开关、纯文本和 HTML 两段 |
| sub2api `email_service.go` | 587 默认；拨号 10 秒；TLS 1.2 起；每收件人一分钟冷却 | 进程内 channel 换成 River 队列；整次发送 30 秒统一期限；正文放租户表而不是任务参数 |

测试用进程内的假 SMTP 服务器（成功、认证失败 535、六个步骤各自的 451/550、缺 STARTTLS、隐式 TLS、超时、对着可控连接的期限先后、信封地址的引号和转义），消息用 `testdata/` 里的 golden 文件逐字节比对，头部用「解析回来的值与写入的一致」检查，配置用表驱动，队列用 `pgtest` 对着真实的 PostgreSQL 跑（含另一个站点和空作用域读不到正文、替别的租户入队被拒、发不出的收件人和主题在入队时被拒、任务参数和 `errors` 列里都没有地址、主题和正文、被限流的任务 snooze 且不消耗尝试次数），`go test -update` 重写 golden。共享限流用 `kvtest` 的真实 Valkey 验证两个节点共同准入、大小写归一、并发准入只有一个成功、受控时钟推进（不等待墙钟）、发送失败后的重试仍占着间隔，以及 Valkey 不可用时退回各节点的桶。

`EnqueueTracked` 另外接受调用方的回执 ID，使用独立的 `mail.send_tracked` 种类（同样五次重试和 notifications 队列），防止旧版本 worker 忽略回执。启动前设置 `Mailer.Receipt` 回调，SMTP 接受、永久拒绝或最后一次失败时写回持久结果；写回失败则重试，允许重复网络投递。救援丢弃的结果由 `Queue.OnDiscard` 补记。运维邮件使用保留的全零站点归属，真实站点的范围读不到；普通租户邮件的归属不变。回执 ID 可以进入 River，地址与正文仍只在 outbox。
