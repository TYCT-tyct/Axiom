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

没有设置任何一项时邮件关闭：启动日志写 `email disabled`，`Enqueue` 返回 `ErrNotConfigured`，节点的其他功能照常。设置了任意一项就要求整套配置，缺项或取值不合法时启动失败，并一次报出全部问题；错误只写变量名和期望的格式，不写取值。发信域名的 SPF、DKIM、DMARC 仍待配置（[accounts 的「待定问题」](../../../docs/domains/accounts.md#待定问题)）。

## 传输

- **始终加密**：端口 465 直接 TLS，其余端口在发出任何命令前用 STARTTLS 升级；服务器不提供 STARTTLS 就报错，不退回明文。证书按主机名校验，没有跳过校验的开关：私有 CA 放进系统的信任库，而不是关掉校验（new-api 的 `SMTPInsecureSkipVerify` 会让任何网络位置读到密码和全部邮件）。最低 TLS 1.2。
- **认证**：用户名和密码都设置时，在 TLS 之后用 `smtp.PlainAuth`；只设一个在启动时就报错。
- **超时**：拨号 10 秒，整次会话 30 秒（连接上一个总期限，任何一步都不会超出）。任务被取消（队列停止）时关闭连接，阻塞中的读立即返回，不必等满 30 秒。

## 消息

调用方给收件人、主题、纯文本和 HTML 两份正文；两份都必需，没有模板。构建出的消息：

- `From`、`To`、`Subject`、`Date`（RFC 1123Z，UTC）、`Message-ID`（随机串 `@` 发件域）、`MIME-Version`、`Content-Type`；
- 主题含非 ASCII 时按 RFC 2047 编成 UTF-8 的 encoded-word；
- 两份正文是 `multipart/alternative` 的两个部分，quoted-printable 编码，中文和长行都不会被中继改写；
- 所有换行都是 CRLF（RFC 5322 §2.3）。

主题为空或跨行、收件人不是单个地址或跨行、任一正文为空都在入队时拒绝（`ErrInvalidMessage`）：头部里的换行会让调用方注入自己的头，空正文发出去也没有意义。

## 队列与重试

- 任务种类 `mail.send`，默认队列，最多尝试 5 次：River 的默认退避约为 16 秒、81 秒、256 秒、625 秒（五次共约 16 分钟），覆盖服务商几分钟的故障；用完仍失败的任务被丢弃，由队列的扫描报告（`queue.job_discarded` 和 `axiom.pg.queue.discarded_jobs`，[observability 的「监控与告警」](../../../docs/domains/observability.md#监控与告警)）。
- 网络错误、超时、4xx 回复原样返回，由 River 重试；5xx 回复（`*textproto.Error` 500–599）用 `river.JobCancel` 结束，同样的消息再发一次也会被同样拒绝。
- 参数本身不合法（只可能来自代码错误或改动）同样取消，不重试。
- 至少一次：任务在邮件已被服务器接受、但还没标成完成时节点崩溃，会再发一次；调用方的流程要能容忍重复的邮件。

## 限流

同一收件人（地址不分大小写）一分钟内只发一封，进程内记下最近一次发送的时间，过期的条目在下一次检查时清掉。被限流的任务用 `river.JobSnooze` 睡到一分钟之后，不消耗尝试次数。Valkey 之后接管跨节点的计数（[总体架构的「缓存与协调」](../../../docs/architecture.md#技术选型)）；在那之前，每个节点各自挡住一个流程刷满收件箱。

## 投递结果

发出去记 `mail sent`（`job_id`、`attempt`、`to`、`message_id`），失败记 `mail send failed`（另加 `permanent` 和 `err`）。正文、主题和密码从不进日志；收件人地址进，因为运维要靠它和发信服务商的日志对上。

持久记录是 River 的任务行（状态、尝试次数、最后错误），没有新表：任务行已经够用，丢弃的任务还有队列的告警；为了「投递结果」再加一张表和一次迁移，收益不抵它带来的第二份状态。

## 不变量

| 不变量 | 靠什么保证 |
|---|---|
| 邮件不会明文发出 | 465 之外必须 STARTTLS，服务器不提供就失败；没有跳过校验的配置 |
| 一封信一个任务 | `Enqueue` 只插入一个 `mail.send`，正文在任务参数里 |
| 同一收件人一分钟最多一封 | `limiter`；限流的任务 snooze，不耗尝试次数 |
| 5xx 不重试 | `wrapRejected` 把 500–599 包成 `ErrRejected`，工作函数转成 `JobCancel` |
| 密码和正文不进日志和错误 | `secret.Secret` 的脱敏；错误只带主机名、步骤和服务器回复 |

## 与参考实现的差异

| 参考 | 沿用 | 不同 |
|---|---|---|
| new-api `common/email.go` | 465 用隐式 TLS；账号和密码都设才认证；`Date`、`Message-ID` 头 | new-api 的 STARTTLS 是开关、可以关掉校验、正文只有一段 HTML；这里强制加密、没有跳过校验的开关、纯文本和 HTML 两段 |
| sub2api `email_service.go` | 587 默认；拨号 10 秒；TLS 1.2 起；每收件人一分钟冷却 | 进程内 channel 换成 River 队列；整次发送 30 秒统一期限 |

测试用进程内的假 SMTP 服务器（成功、认证失败 535、按步骤的 451/550、缺 STARTTLS、隐式 TLS、超时），消息用 `testdata/` 里的 golden 文件逐字节比对，配置用表驱动，队列用 `pgtest` 对着真实的 PostgreSQL 跑（`go test -update` 重写 golden）。
