# 供应商：调研与推导存档

[供应商](../domains/suppliers.md)和[模块 README](../../internal/suppliers/README.md) 的决定背后的调研、上游出处和被否决的方案。这里不是现行规则；规则以那两份文档和代码为准。

## 上游出处

| 事实 | 出处 |
|---|---|
| new-api 的对账键 `X-Oneapi-Request-Id`、sub2api 的 `X-Client-Request-Id` 等于扣费记录的请求号 | 2026-10-14 本地实例实测 |
| new-api 渠道是数据行，但新增上游仍要实现 15 个方法的 Adaptor、登记约 60 种渠道类型；渠道不含余额、key、扣费等管理操作 | new-api `e449cc7`：[`model/channel.go`](https://github.com/QuantumNous/new-api/blob/e449cc700eb569cdfc2199cb73e97de46944f43c/model/channel.go#L23)、[`relay/channel/adapter.go`](https://github.com/QuantumNous/new-api/blob/e449cc700eb569cdfc2199cb73e97de46944f43c/relay/channel/adapter.go#L18)、[`constant/channel.go`](https://github.com/QuantumNous/new-api/blob/e449cc700eb569cdfc2199cb73e97de46944f43c/constant/channel.go#L3) |
| sub2api 的 Channel 带价格和映射，同样没把管理面做成配置 | sub2api `a3298b4`：[`channel.go`](https://github.com/Wei-Shaw/sub2api/blob/a3298b46ebe239fb0720b068a1eb3383955bdac2/backend/internal/service/channel.go#L50)、[`channel_service.go`](https://github.com/Wei-Shaw/sub2api/blob/a3298b46ebe239fb0720b068a1eb3383955bdac2/backend/internal/service/channel_service.go) |
| 普通账号可用登录令牌建 key；new-api 建 key 只回 `success`，sub2api 回 ID；读原文的接口和作用域 | [new-api `AddToken`](https://github.com/QuantumNous/new-api/blob/e449cc700eb569cdfc2199cb73e97de46944f43c/controller/token.go#L299)、[`POST /api/token/:id/key`](https://github.com/QuantumNous/new-api/blob/e449cc700eb569cdfc2199cb73e97de46944f43c/controller/token.go#L190)、[作用域表](https://github.com/QuantumNous/new-api/blob/e449cc700eb569cdfc2199cb73e97de46944f43c/middleware/access_token_routes.go#L214)；[sub2api `APIKeyService.Create`](https://github.com/Wei-Shaw/sub2api/blob/a3298b46ebe239fb0720b068a1eb3383955bdac2/backend/internal/service/api_key_service.go#L494)、[`GET /api/v1/keys/:id`](https://github.com/Wei-Shaw/sub2api/blob/a3298b46ebe239fb0720b068a1eb3383955bdac2/backend/internal/handler/api_key_handler.go#L152) |
| new-api 登录和 key 日志接口默认每 20 分钟 20 次，运营方可改 | new-api 源码，2026-10 |
| sub2api 刷新令牌时比较后再写 | sub2api 源码，2026-10 |
| sub2api 事实：0.2.16 官方镜像实测，刷新令牌一次性；不传递取消、读完上游并全额扣费 | 2026-10-15 本地实测，#25 |
| new-api 事实：v1.0.0-rc.44 本地实例核对 | #25 的两条调研评论 |
| 删渠道/账号：new-api 立即硬删整行，sub2api 立即删除并清调度快照（表带软删除），都不等在途、不做最后读取 | new-api `model/channel.go`；sub2api `backend/internal/repository/account_repo.go` |
| 按用户（可再分组）计 RPM，新增 key 不新增额度 | new-api `middleware/model-rate-limit.go`（206e0fa5）；sub2api `admin_service.go` 的 `UserRPMStatus` 与 `user_rpm_cache.go`（8f2cbb53，2026-10-19）；OpenRouter [Limits](https://github.com/OpenRouterTeam/docs/blob/main/api_reference/limits.mdx)（2026-10-28） |
| 路由前按 key 模型过滤、key 带到期时间 | OpenRouter [BYOK](https://github.com/OpenRouterTeam/docs/blob/main/guides/overview/auth/byok.mdx)、[API key expiry](https://openrouter.ai/docs/api/api-reference/api-keys/create-keys)（2026-10-29） |
| 检测请求体：三种 Chat 形态取自 new-api channel test，另给 Responses 也带上限、不要求推理；旧补全按 OpenAI《Create completion》的流式形状 | new-api `controller/channel-test.go:702-782`（`7d8b912`，2026-10-22）；[OpenAI](https://developers.openai.com/api/reference/resources/completions/methods/create)（2026-10-31） |
| 两家的结算都不拉供应商扣费记录做容差比较或估低摘牌 | [new-api `BillingSession.Settle`](https://github.com/QuantumNous/new-api/blob/6cf0d90b5ff27f901697dddd61e0c28aba8efefc/service/billing_session.go#L47)、[`quota.go`](https://github.com/QuantumNous/new-api/blob/6cf0d90b5ff27f901697dddd61e0c28aba8efefc/service/quota.go#L250)；[sub2api `usageBillingRepository.Apply`](https://github.com/Wei-Shaw/sub2api/blob/b0a02a3304a0a78cdd003ddda1dd23ba02c03084/backend/internal/repository/usage_billing_repo.go#L22)（2026-10-18） |

## CPA（CLIProxyAPI）的上游事实

按 `6e6afab99a1939525ecb6bb1f0873c8e7a9df877`（v8，2026-11-03）核对：

- 用量：`GET /v0/management/usage-queue?count=N` 每弹出一条就从内存队列删除（[`usage.go`](https://github.com/router-for-me/CLIProxyAPI/blob/6e6afab99a1939525ecb6bb1f0873c8e7a9df877/internal/api/handlers/management/usage.go)、[`queue.go`](https://github.com/router-for-me/CLIProxyAPI/blob/6e6afab99a1939525ecb6bb1f0873c8e7a9df877/internal/redisqueue/queue.go)、路由在 [`server_management.go`](https://github.com/router-for-me/CLIProxyAPI/blob/6e6afab99a1939525ecb6bb1f0873c8e7a9df877/internal/api/server_management.go#L93)）；6.10 起不再内置用量统计。队列开关是 `hasManagementSecret || cfg.Home.Enabled`（[`server.go`](https://github.com/router-for-me/CLIProxyAPI/blob/6e6afab99a1939525ecb6bb1f0873c8e7a9df877/internal/api/server.go)），有订阅者时 `Enqueue` 直接发给订阅者、不入队。记录有 `request_id`、`timestamp`、`model`、`provider`、`auth_index`、明文 `api_key` 和 `token_breakdown`；v2 breakdown 把两种缓存写合在 `cache_write_tokens`。
- 识别：`GET /` 回答带 `"message":"CLI Proxy API Server"`（[`server_routes.go`](https://github.com/router-for-me/CLIProxyAPI/blob/6e6afab99a1939525ecb6bb1f0873c8e7a9df877/internal/api/server_routes.go)）；管理鉴权失败五次封地址三十分钟（[`handler.go`](https://github.com/router-for-me/CLIProxyAPI/blob/6e6afab99a1939525ecb6bb1f0873c8e7a9df877/internal/api/handlers/management/handler.go)）。
- 账号与模型：`GET /v0/management/auth-files`、`GET /auth-files/models?name=`（[`auth_files.go`](https://github.com/router-for-me/CLIProxyAPI/blob/6e6afab99a1939525ecb6bb1f0873c8e7a9df877/internal/api/handlers/management/auth_files.go)）；`success`/`failed`/`recent_requests` 只是进程内计数，证明不了记录被谁拿走。
- key：`GET /api-keys` 是没有名字、分组、权限的全局明文列表，不分页（[`config_lists.go`](https://github.com/router-for-me/CLIProxyAPI/blob/6e6afab99a1939525ecb6bb1f0873c8e7a9df877/internal/api/handlers/management/config_lists.go)），没有建 key 的接口。
- 窗口：上游只对 claude、codex、devin 写被动配额信号（[`quota_signals.go`](https://github.com/router-for-me/CLIProxyAPI/blob/6e6afab99a1939525ecb6bb1f0873c8e7a9df877/sdk/cliproxy/auth/quota_signals.go)）；devin 的窗口形状未知。
- 对账：`X-CPA-TRACE-ID` 的最后 36 个字符是 usage 记录的 `request_id`（UUIDv7，Axiom 不能指定）（[`cpa_trace.go`](https://github.com/router-for-me/CLIProxyAPI/blob/6e6afab99a1939525ecb6bb1f0873c8e7a9df877/internal/logging/cpa_trace.go)）。`cost_details` 默认不填，且各 provider 币种不一。
- 采集中断丢失的部分只能从日志（空批、批大小骤降、`rejected`）和"尝试过了宽限还没有扣费"的报告发现，不能补读。

## 被否决的方案

- **声明式适配器**：数据库存声明、各节点轮询——省重启，但多一套生效、版本和回滚，坏数据先跑起来；编译进镜像——每加一个站都要构建发布；给声明加"队列消费者"操作——把常驻状态、去重和单采集器约束藏进配置，比 Go 适配器更难审查。
- **CPA**：只报余额变化——丢掉逐笔成本，对账无法按请求归属；直接弹出后一个事务入账——数据库慢或崩溃时整批没了，也没处查解析失败；本地 spool 文件——多一份节点本地状态，要自己 fsync 和清理，跨节点看不见。staging 表把崩溃窗口从"整轮"缩到"一批"，但消不掉。给 CPA 零成本或摊订阅费——前者让 `cost_plus` 售价成零，后者是没有来源的单价。
- **自动建 key**：继续让人建 key（要解决的痛点）；给所有分组建（订阅组不能用、浪费额度）；只信创建响应的 ID（new-api 不返回）；把每组建 key 状态放进同步状态（旧节点严格解码拒绝），所以放在 `supplier_groups` 的列上。
- **移除**：分步移除（等在途、读尽记录、再删密文）——多一个持久状态机，上游不可达时延长密文留存；硬删凭证行——成本记录的外键断掉。比 sub2api 严（不留凭证），比 new-api 保住成本历史。以后要加最后一次读取，加在擦除之前。审计只记 key 个数，因为 `audit_log.detail` 上限 8192 字节，列 ID 会让 key 多的移除回滚。
- **修改供应商**：一个"修改供应商"入口——审批策略分不开。单独的恢复操作——要同时改状态和引擎状态，多一步人会忘；自动定时重试"需处理"——封禁不是重试能解决的。
- **别名**：直接改端点的模型——同一写法在别的中转站可能是别的模型；覆盖原映射——失去纠错历史；无前提的 POST 自动追加修订——过期的待映射页面会覆盖别人的选择。
- **单位价值**：按端点存——同一份余额要逐端点改；存在 billing——钱的数据离开供应商、结算跨模块查；折进成本计划——每次改值都要重写全部计划；只在代码中补币种列——旧节点写入失败；读取时按当前配置解释历史——配置变化会改写旧钱的含义。
- **RPM 与并发**：只靠适配器的限速桶——那是同步自己的限速，不能用来拒用户请求；计数放数据库——每次调用一次事务；`golang.org/x/time/rate`——多一个依赖，令牌补充口径不等于按秒的滚动窗口；保存每次请求的时间戳——内存随上限增长；上限存在供应商上、凭证继承——不能按 key 单独压低；共享 Redis 槽位——即时借用空闲容量，但每次调用多共享计数、释放和失联清理；只按本地看到的 N 等分——扩容时新旧视图混合会超额，也识别不了同人数换成员。代价是交接期欠用、低额度集中在排序靠前的节点。TPM 等有可信额度数据再加。
- **同步调度**：每个任务结束时入队后继——River 的唯一性含"运行"，任务结束前插不进后继，不唯一的链多入队一次就永远多一条；River 自己重试——与引擎的退避成两份；轮前打开适配器——登录被拒时引擎不知道，下一轮还会再登录；被限速时在任务里等——原先每任务最多等 8 分钟，少量供应商就占满 4 个工人，重启丢进度；River snooze——位置照样要存进状态，多一条队列状态。
- **同步存储**：状态拆成列——引擎一变就要迁移；清单位置放进状态 JSON 并把格式升到 2——旧节点读不了；放进 `sync_events`——每页一行历史；只靠 `lease_until > now()`——暂停醒来的节点与接手之间没有顺序；只在语句里用快照读栅栏——`UPDATE … FROM` 只锁目标行，等锁期间被接手仍会按旧栅栏写入；限速名额均分给各节点——节点数一变要重分、空闲名额浪费；建桶用 `DO NOTHING`——请求持新桶去等已有桶，相反顺序声明会死锁。
- **成本计划与端点**：只留 `sync_plans`、端点靠指纹去查——调用面每次反查；只留 `cost_schedules`——引擎不知道哪版是新的；先生成未知端点——后台多一堆不可用的行；计划消失时禁用端点——分不清人停还是同步停。
- **号池**：读数放进同步状态——旧节点读不了；另立表——多一套 RLS、授权和清理；每次读都写——破坏投影"同一清单不写"的语义；全量问窗口——慢、耗额度；只看第一个账号——不代表分组；把用量错误当账号失败——非 OAuth 账号本来就不报窗口；新增 `pool_low` 同步事件——滚动发布时旧消费者跳过不认识的事件并推进位置，告警永久丢失。告警桥的位置来自旧版本（没有低分组集合）时会对当前不低的分组补发恢复。
- **对账日志**：进程内日志每秒批量落库——多一条写入路径和一类连接名额，只把崩溃窗口缩小一点。早期实现中刷写它的 `Run` 在生产里没人启动，清理读到的永远是空的（[M1 计划](https://github.com/TYCT-tyct/Axiom/issues/147)），已删除。`ended_requests` 原先的读者是日志清理，计划随日志表在收缩发布里删（#213）。
- **检测成本事务**：另开短事务补写成本——不能阻止同键重试再次生成；彻底消除生成后回滚再付费的窗口要持久化进行中的认领和恢复协议，未做。
- **对账任务**：只在同步成功后运行；每个节点各起计时器。

## 滚动发布的兼容推导

首次生产部署前不要求新旧节点同时运行，下面这些兼容做法大多已从代码里删掉（`AXIOM_DIRECT_FORMATS` 闸、保留的移除成员、清单位置绑定状态版本、读旧值时按上限截断）；留下当初的理由，以后真要滚动发布时参考：

- 同步状态严格解码，新成员会让旧节点拒绝，所以清单位置、key 策略和名字、号池读数、建 key 状态都放在状态 JSON 之外；`Removal` 等不再用的成员保留，状态成员集合由测试钉住，`Task`、`Status`、`RemovalStep` 常量只在末尾增加。
- 新格式由 `AXIOM_DIRECT_FORMATS` 闸在状态上，因为旧节点接手任务会载入状态并重新投影，一个不认识的格式编号会让它整个提交回滚。全部节点认识新格式后才设 `on`；当时回滚前按历史迁移 `00020` 的 Down 提示清掉新格式的行；该迁移现已压入 [`00049_baseline.sql`](../../internal/suppliers/internal/store/migrations/00049_baseline.sql)，开发库重建规则见[平台迁移说明](../../internal/platform/README.md#迁移)。
- BYOK 用 `status='byok'`：旧节点按 `status IN ('active','low_balance')` 路由、按 `syncable` 同步，自然跳过它。
- RPM 的数据库上限 CHECK 待旧节点退出后再加；首次从旧二进制升级到静态份额时，旧版本退出不保留窗口，要先停旧核心并等 61 秒。
- 分组别名修订和凭证模型限制旧节点不读，依赖它们之前要排空旧节点。

## 测量与样本

- 9,050 个 key 是 91 页，new-api `global` 桶 90 次一窗，所以要分两次任务读完；sub2api 29 个有 key 的分组要 90 次面板请求，窗口上限 60。
- sub2api 未验证：key 的 `reset_*_at` 没有样本；分页 `total` 是否精确；订阅分组的记录形态。`channels/available` 能公布价格，但实例里关着、没有样本，未读取。
- CPA 的 `testdata/` 按源码和上游测试的报文形状整理，不是真实实例录制。
- 检测估价的例子：18 个 token 与 16 个 token 的封顶合成 6.15。
