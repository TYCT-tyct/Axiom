# mail

出站邮件：调用方写好正文，这里把它经 SMTP 发出。每次发送是一个 River 任务（`mail.send`，默认队列），按失败能否重试分别重试或放弃，同一收件人一分钟最多一封。

投递放在 `platform` 而不是 accounts：收件人、模板、站点品牌和多语言属于账户流程（[accounts 的「给用户的邮件」](../../../docs/domains/accounts.md#给用户的邮件)），这里只有各模块共用的发送能力。走队列而不是进程内的 channel：任务和触发它的写入在同一个事务里入队，重启不丢，重试次数和最后错误由 River 记下（sub2api 的 `email_queue_service.go` 用 channel，重启即丢，2026-10）。

## 配置

| 变量 | 默认 | 说明 |
|---|---|---|
| `AXIOM_SMTP_HOST` | — | 提交服务器的主机名，如 `smtp.example.com`；不带协议和端口 |
| `AXIOM_SMTP_PORT` | `587` | 提交端口；`465` 是从第一个字节起就是 TLS，其余端口用 STARTTLS 升级 |
| `AXIOM_SMTP_USERNAME` | — | 账号；和密码要么都设，要么都不设（不设用于按地址放行的中继） |
| `AXIOM_SMTP_PASSWORD` | — | 密码或服务商的应用专用密码；以 `secret.Secret` 保存，任何格式都不打印 |
| `AXIOM_SMTP_FROM` | — | 发件人，一个地址，可带显示名：`Axiom <no-reply@example.com>` |

没有设置任何一项时邮件关闭：启动日志写 `email disabled`，`Enqueue` 返回 `ErrNotConfigured`，节点的其他功能照常。设置了任意一项就要求整套配置，缺项或取值不合法时启动失败，并一次报出全部问题；错误只写变量名和期望的格式，不写取值。发件人同时出现在信封和 `From` 头里，所以两者写不出的地址在启动时就失败（只有 ASCII 能进信封路径，见「传输」）。发信域名的 SPF、DKIM、DMARC 仍待配置（[accounts 的「待定问题」](../../../docs/domains/accounts.md#待定问题)）。

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
- 头部在空格处折行，续行以那个空格开头（RFC 5322 §2.2.3），展开后与原文一致；行宽 78 字符，超过 998 字符的值拒绝（§2.1.1）。没有空格可折的超长词写不出，发送前报 `ErrInvalidMessage`；
- 两份正文是 `multipart/alternative` 的两个部分，quoted-printable 编码，中文和长行都不会被中继改写；
- 所有换行都是 CRLF（RFC 5322 §2.3）。

主题为空或含控制字符、收件人不是单个地址或含控制字符、任一正文为空都在入队时拒绝（`ErrInvalidMessage`）：头部里的换行会让调用方注入自己的头，空正文发出去也没有意义。

## 队列与重试

任务种类 `mail.send`，默认队列，最多尝试 5 次：River 的默认退避约为 16 秒、81 秒、256 秒、625 秒（五次共约 16 分钟），覆盖服务商几分钟的故障；用完仍失败的任务被丢弃，由队列的扫描报告（`queue.job_discarded` 和 `axiom.pg.queue.discarded_jobs`，[observability 的「监控与告警」](../../../docs/domains/observability.md#监控与告警)）。

邮件正文不进任务参数：River 的任务行不属于任何租户，没有策略覆盖，所以地址、主题和正文都存在 `platform.mail_outbox`，任务参数只有那一行的 id。这张表开了 `FORCE ROW LEVEL SECURITY`，策略是 `platform.in_scope(site_id, account_id)`：调用方在租户的事务里入队，`WITH CHECK` 挡住替别的租户写的可能，读取同样受它限制（[平台基础设施的「租户隔离」](../README.md#租户隔离)）。

工作进程手里只有 id，没有租户，所以先用 `platform.mail_tenant(id)` 查到这一行的 `site_id`、`account_id`，再在该租户的作用域里读正文——这个函数是 `SECURITY DEFINER`、属主 `axiom_lookup`、只授权给应用，只返回这两个租户列（与 accounts 模块的登录前查找同一套做法，见 [accounts 的迁移](../../accounts/migrations/00002_lookup.sql)）。`pg.TenantTx` 先以空作用域查一次，再换成查到的租户，`fn` 在其中运行。

- 任务开始时就查不到这一行，说明已经发过或已被删除：记 `mail skipped: the queued message is gone`，任务完成。
- 网络错误、超时、4xx 回复原样返回，由 River 重试，**消息留在表里**，重试时照样读得到；5xx 回复（`*textproto.Error` 500–599）用 `river.JobCancel` 结束，同样的消息再发一次也会被同样拒绝。
- 参数本身不合法（地址写不进信封、头部写不下）同样取消，不重试。
- 发出或被永久拒绝后，这一行在同一租户的作用域里删除；删除失败只记 `mail not deleted from the outbox`，任务结果不变——邮件已经发了，重试只会再发一次。
- 至少一次：任务在邮件已被服务器接受、但还没标成完成时节点崩溃，会再发一次；调用方的流程要能容忍重复的邮件。被丢弃的任务（5 次都失败）不删这一行，供运维排查，清理策略待定。

## 限流

同一收件人（地址不分大小写）一分钟内只发一封，进程内记下最近一次发送的时间，过期的条目在下一次检查时清掉。被限流的任务用 `river.JobSnooze` 睡到一分钟之后，不消耗尝试次数（消息留在表里）。Valkey 之后接管跨节点的计数（[总体架构的「缓存与协调」](../../../docs/architecture.md#技术选型)）；在那之前，每个节点各自挡住一个流程刷满收件箱。

## 投递结果

发出去记 `mail sent`（`job_id`、`attempt`、`to`、`message_id`），失败记 `mail send failed`（另加 `permanent` 和 `err`）。正文、主题和密码从不进日志；收件人地址进，因为运维要靠它和发信服务商的日志对上。

持久记录是 River 的任务行（状态、尝试次数、最后错误）加上 `platform.mail_outbox` 里的那一行：任务行说这次投递怎么样，outbox 行说发的是什么、属于哪个租户，两者用任务参数里的 id 相连。正文随发随删（见上一节），不长期留在库里。

## 不变量

| 不变量 | 靠什么保证 |
|---|---|
| 邮件不会明文发出 | 465 之外必须 STARTTLS，服务器不提供就失败；没有跳过校验的配置 |
| 邮件内容不进入不属于任何租户的表 | 正文在 `platform.mail_outbox`，任务参数只有 id；表的策略是 `platform.in_scope`，别的站点和空作用域都读不到 |
| 一个租户不能替另一个租户入队 | 同一策略的 `WITH CHECK` 比较 `site_id`、`account_id`，租户从调用方的事务里来 |
| 一封信一个任务 | `Enqueue` 在调用方的事务里插一行 outbox、一个 `mail.send`，参数是那一行的 id |
| 同一收件人一分钟最多一封 | `limiter`；限流的任务 snooze，不耗尝试次数 |
| 5xx 不重试 | `wrapRejected` 把 500–599 包成 `ErrRejected`，工作函数转成 `JobCancel` |
| 信封和头部写的是同一个地址 | `mailbox` 同时给 `MAIL FROM`、`RCPT TO` 和 `From`、`To` 用 |
| 密码和正文不进日志和错误 | `secret.Secret` 的脱敏；错误只带主机名、步骤和服务器回复 |

## 与参考实现的差异

| 参考 | 沿用 | 不同 |
|---|---|---|
| new-api `common/email.go` | 465 用隐式 TLS；账号和密码都设才认证；`Date`、`Message-ID` 头 | new-api 的 STARTTLS 是开关、可以关掉校验、正文只有一段 HTML；这里强制加密、没有跳过校验的开关、纯文本和 HTML 两段 |
| sub2api `email_service.go` | 587 默认；拨号 10 秒；TLS 1.2 起；每收件人一分钟冷却 | 进程内 channel 换成 River 队列；整次发送 30 秒统一期限；正文放租户表而不是任务参数 |

测试用进程内的假 SMTP 服务器（成功、认证失败 535、六个步骤各自的 451/550、缺 STARTTLS、隐式 TLS、超时、对着可控连接的期限先后、信封地址的引号和转义），消息用 `testdata/` 里的 golden 文件逐字节比对，头部用「解析回来的值与写入的一致」检查，配置用表驱动，队列用 `pgtest` 对着真实的 PostgreSQL 跑（含另一个站点和空作用域读不到正文、替别的租户入队被拒），`go test -update` 重写 golden。
