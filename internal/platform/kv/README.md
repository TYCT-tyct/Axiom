# kv

多个核心节点共用的限流计数、登录前的尝试次数和会话粘性，放在 Valkey 里；这里写放什么、怎么连、键怎么写、Valkey 不可用时每种用途怎么兜底，以及部署和测试。选型的总体理由见[总体架构的「技术选型」](../../../docs/architecture.md#技术选型)。

> 本文先于代码写成，是 [Issue #321](https://github.com/TYCT-tyct/Axiom/issues/321) E8-b 的设计，按下文「落地顺序」的 K1–K4 实现，代码随 K1 加入。代码与本文不一致时，在同一个 PR 里改本文。

三样都是**丢了不影响正确性**的数据：丢失只会多放行一点、或让会话重新选一次端点，钱和权限不受影响。

## 放什么、不放什么

| 用途 | 归属 | 算法 | 丢了的后果 |
|---|---|---|---|
| 限流计数：每 key 的调用、计数频率；E4-2（[#315](https://github.com/TYCT-tyct/Axiom/issues/315)）的账户、等级、key 业务额度沿用同一个脚本 | gateway | 滑动窗口近似，与现在进程内的 `RateCounter` 相同 | 至多多放行一个窗口 |
| 登录前的尝试次数：每（站点, IP）、每（站点, 邮箱）、每用户 | accounts | GCRA 令牌桶，与现在进程内的相同 | 每个桶至多多一次突发 |
| 会话粘性：会话 → 上次服务它的端点 | routing 定键和值，gateway 定读写的时机 | 键值加到期时间 | 下一次请求重新选端点，丢一次上游的提示缓存 |

不放进 Valkey 的：

- **钱**：余额、预留、花费计数、持有者、账本只在 PostgreSQL（[gateway 的不变量 I5](../../gateway/README.md#不变量)）。sub2api 把在途预留放在 Redis、故障时放行（[计费的「决策与理由」](../../../docs/domains/billing.md)），new-api 用旧快照覆盖 Redis 里的额度被绕过（[安全](../../../docs/security.md)），我们两样都不做。
- **能被改的授权状态**：key 能否使用、控制台会话、幂等键都在 PostgreSQL，每次读库（[accounts 的「认出与准入」](../../accounts/README.md#认出与准入)）；key 缓存在进程内，只存不会变的对应关系。
- **保护本节点资源的上限**：在途数（按节点、IP、账户）、argon2id 名额、key 未命中查库的令牌桶、账户闸、连接池的分类名额。它们护的是这个进程的内存、CPU 和连接，本来就该按节点算。
- **路由的硬上限**：端点负载、429 降权、凭证并发、重试预算在进程内（[路由的「容量感知」](../../../docs/domains/routing.md#容量感知)），硬上限不依赖共享的数字。
- **辅助锁**：架构允许，目前没有用途，不实现。

## 客户端与连接

- **客户端**：[valkey-go](https://github.com/valkey-io/valkey-go) v1.0.78（2026-09-15），Apache-2.0，Valkey 项目自己维护；运行时只依赖 `golang.org/x/sys`。固定版本，升级前读变更说明。depguard 只许 `internal/platform/kv` 引用它；`billing`、`payments` 不得引用 `kv`（钱不进缓存，由 `tools/checkmodules` 拦）。选它而不是 go-redis 的理由见「决策与理由」。
- **服务端**：`valkey/valkey:9.1.2-alpine`，部署和测试用同一个标签。
- **连接**：自动流水线，并发的命令共用少量连接（默认每个实例 4 条），不用按并发数配连接池（sub2api 配 1024 条，`repository/redis.go:23`、`config.go:2241`）。关掉客户端缓存（`DisableCache`，不发 `CLIENT TRACKING`）和自动重试（`DisableRetry`：脚本会加计数，不能重发；一次失败只计一次，直接走兜底）。建连 1s；一条连接 1s 内收不到回复就关掉重连（`ConnWriteTimeout`，默认 10s 太长）。
- **期限**：每次同步访问的期限是调用方 context 与 `kv.Timeout`（20ms，[路由的「容量感知」](../../../docs/domains/routing.md#容量感知)）中较早的一个。到期立即返回，连接不断：迟到的回复由客户端丢掉。登录也用同一个期限，Valkey 在同机房的正常往返在 1ms 以内，20ms 还没回就是它出了问题。
- **认证**：Valkey 的 ACL 用户 `axiom`，`default` 用户关闭。`AXIOM_VALKEY_URL` 写成 `valkey://axiom:<密码>@valkey:6379`；同一台机器上走 compose 的内部网络，不加 TLS（与数据库的 `sslmode=disable` 相同）；跨机器（M3）用 `valkeys://`，按系统根证书校验服务端，自签 CA 的变量随那个部署加入。其他写法（`redis://` 等）不接受，一种写法。密码只在这个变量里，错误只报变量名，不带取值。
- **权限**：`axiom` 用户只能碰 `ax:*` 的键，命令只放行 `hello`、`auth`、`ping`、`client|setinfo`、`info`、`get`、`set`、`del`、`incr`、`pexpire`、`evalsha`、`eval`（脚本里执行的命令也按这个用户检查）。K1 的测试以这个用户跑完全部用法，出现 NOPERM 即失败；`FLUSHALL`、`KEYS`、`CONFIG` 和 `ax:*` 之外的键必须被拒。

## 接口（草图）

- `kv.Open(ctx, cfg, meter, logger) (*Client, error)`：按下文「配置与启动」检查；没设 URL 时返回单核心模式的 `Client`，`Enabled()` 为假，调用方走进程内的实现。
- `Client.Nodes() int`：核心节点数 N，兜底按它等分。
- `kv.NewScript(src)`：脚本写在用它的模块里（gateway 的 `count`，accounts 的 `take`、`give`），`kv` 只管执行（先 `EVALSHA`，服务端没有就 `EVAL`），不含业务规则。
- `Client.Do(ctx, use, cmds…)`：一批命令一次往返，统一加期限、过熔断、记指标；任何失败都返回包着原因的 `kv.ErrUnavailable`，调用方据此兜底。
- `Client.Later(use, cmd)`：后台写入，队列满即丢弃。
- `Client.Key(use, site, parts…)`：拼出带 `ax:<ns>:` 前缀的键，`ns` 只在这里定。

## 配置与启动

`serve` 多读两个变量：`AXIOM_VALKEY_URL`（不设就是单核心模式：三样都在进程内，与 M1 相同）和 `AXIOM_CORE_NODES`（核心节点数 N，默认 1）。`kv.Open` 在 `AXIOM_STARTUP_TIMEOUT` 之内做下列检查：

| 情况 | 结果 |
|---|---|
| URL 读不通、写法不对；N 不是正整数；N > 1 却没设 URL | 不启动，报变量名（[gateway 的「决策与理由」](../../gateway/README.md#决策与理由)：多核心没有 Valkey 不启动） |
| 连得上，但认证或权限被拒（`WRONGPASS`、`NOPERM`） | 不启动：这是配置错了 |
| 连得上，`INFO memory` 里 `maxmemory` 为 0 或 `maxmemory_policy` 不是 `allkeys-lru` | 不启动：内存没有上限 |
| 连不上（拒绝连接、超时） | 照常启动，熔断打开（见下文），记警告；第一次连上时补做上面两项检查，不满足记错误并计入指标（运行中不能再拒绝启动） |

就绪检查不看 Valkey：它不可用时节点仍能正确服务。N 是静态配置，因为 `serve` 还没有节点登记（[总体架构的「部署拓扑」](../../../docs/architecture.md#部署拓扑)）；M3 有了节点登记（[#322](https://github.com/TYCT-tyct/Axiom/issues/322)）后由它取代这个变量。

## 键

写法 `ax:<ns>:<用途>:<站点>:<主体>`。`ns` 在生产是 `v1`，即值的格式版本：值的格式一变就换 `ns`，滚动发布时新旧节点读不到对方的值，旧键自然到期；测试每个用例用随机的 `ns`，共用一个 Valkey 也互不可见。ID 一律写成 UUID 的 32 位小写十六进制。每个键都带站点，排障或清理一个站点时按 `SCAN MATCH ax:v1:*:<站点>:*` 找得全，代价是每个键约 33 字节。

| 用途 | 键 | 值 | 到期 |
|---|---|---|---|
| 限流计数 | `ax:v1:rate:<站点>:<维度>:<ID>:<窗口序号>`；维度现在是 `key`（调用）和 `count`（`count_tokens`），ID 是 key 的 ID；E4-2 加 `account`、`tier` 等维度 | 整数 | 2 × 窗口，脚本在第一次 `INCR` 时设 |
| 会话粘性 | `ax:v1:stick:<站点>:<会话键的 64 位十六进制>` | 端点 ID | 预设的粘性时间（`StickyTTL`，默认一小时），`SET … PX` |
| 登录前的次数 | `ax:v1:login:<站点>:ip:<h>`、`ax:v1:login:<站点>:email:<h>`、`ax:v1:login:<站点>:user:<用户 ID>`；`h` 是 `HMAC-SHA256(当前版本的 key 哈希密钥, 用途标签 ‖ 规范化的值)` 的前 16 字节 | 桶再次装满的时刻（毫秒） | 装满的时刻（`PXAT`），装满的桶就是没有键 |

邮箱和 IP 只以 HMAC 出现在键里：键能被 `SCAN` 和内存转储看到，不留个人信息；会话键本身已是账户加会话的哈希。哈希密钥轮换时，登录的次数从零重新算一次。

## 内存

compose 里 `maxmemory` 默认 256 MB、`maxmemory-policy allkeys-lru`，不持久化（理由见「决策与理由」）。粗算每个键约 150–200 字节：粘性占大头，256 MB 约容纳一小时内一百万个会话；限流计数每个活跃的（维度, ID）两个键；登录的键随来源 IP 和邮箱数增长。满了按最近最少使用淘汰：每个请求都碰的计数和正在被猜的邮箱的桶是最近用过的，先被淘汰的是不再活跃的粘性。攻击者用大量 IP 撑大登录的键，最多挤掉冷的条目，被攻击的邮箱每次尝试都会被碰到，不会被挤掉。

## 每种用途

**限流计数**

- 窗口序号 = ⌊节点时钟的毫秒 ÷ 窗口长度⌋，按纪元对齐，所以各节点的窗口边界相同；估计值 = 本窗口 + 上一窗口 ×（1 − 本窗口已过的比例），与 `RateCounter` 相同。
- 脚本 `count`：参数是若干对（本窗口键, 上一窗口键）和各自的到期时间；对每一对 `INCR` 本窗口，结果是 1 就 `PEXPIRE`，再 `GET` 上一窗口，返回两两一组的整数。一个请求的全部维度在一个脚本里。`INCR` 和设到期在同一个脚本里，所以不会留下没有到期时间的键，new-api 和 sub2api 补到期时间的分支（`rate-limit.go:23`、`rate_limiter.go:30`）在这里不需要。
- 不用固定窗口：new-api 和 sub2api 都是固定窗口，窗口交界处能放过两倍（new-api 的注释写明了，`rate-limit.go:19`）；我们进程内已经是滑动窗口近似，换到 Valkey 不改行为。
- 预检仍只计数不判定（[gateway 的「准入」](../../gateway/README.md#准入)）。预检的一批命令是 `count` 脚本加粘性的 `GET`（或 `DEL`），同批发出、一次往返；排队醒来不再发。
- 进程内的 `RateCounter` 每个请求照常计数（与 M1 一样一次 map 写入），兜底时不必从零开始。
- **不可用**（出错、到期、熔断打开）：估计值 = 进程内计数 × N，用同一个限额判定。流量平均分到 N 个核心时合计约等于限额；一个核心分到的多于它的份额就早拒，宁可误拒也不放大。
- **恢复**：下一批照常发往 Valkey。兜底期间的计数不合并回去，Valkey 少看到那一段，至多多放行一个窗口；限流不是钱，不值得对账。

**登录前的尝试次数**

- 算法不变（[accounts 的「注册、登录与会话」](../../accounts/README.md#注册登录与会话)）。脚本 `take(键, 突发, 间隔, 现在)`：`t = max(GET 或 现在, 现在)`；`等待 = t + 间隔 − 突发 × 间隔 − 现在`，大于 0 就返回它；否则 `t += 间隔`，`SET 键 t PXAT t`，返回 0。脚本 `give(键, 间隔, 现在)`：键不在就返回；`t −= 间隔`，`t ≤ 现在` 就 `DEL`，否则 `SET 键 t PXAT t`。与进程内 `buckets.take`、`give` 逐步对应，由性质测试核对两者对任意操作序列给出相同结果。
- 一次登录依次：扣 IP，扣邮箱（先扣后退），成功后退回邮箱，至多三次往返；不在调用路径上。
- **不可用**：退回进程内的桶，参数改为突发 max(1, ⌊突发 ÷ N⌋)、间隔 × N，N 个节点合计的突发和回填速度都不超过设定值。**不拒绝登录**，与 sub2api（认证路由在 Redis 故障时一律 429，`routes/auth.go:34`）和 new-api（Redis 出错回 500，`rate-limit.go:119`）不同，理由见「决策与理由」。
- **恢复**：回到 Valkey，进程内的桶不合并。每次 Valkey 不可用或重启丢数据，一个桶至多多出一次突发；argon2id 的并发名额始终按节点限制，猜测的速度仍有上限。

**会话粘性**

- **读**：预检那一批里 `GET`。请求带「上下文已压缩」标记时换成 `DEL`，不读，等于先忘记再选。
- **写**：规则不变（[gateway 的「并发、背压与共享状态」](../../gateway/README.md#并发背压与共享状态)），`SET 键 端点 PX 粘性时间`，交给后台队列（4096 条，一个 goroutine，每条同样 20ms 期限），满了丢弃并计数，从不等待。
- **不可用**：读不到就不粘，写丢掉。配了 Valkey 时不用进程内的表：别的节点看不到它，恢复后以 Valkey 为准，两份只会互相打架。sub2api 读粘性出错时同样当作没有（`service/gateway_scheduling.go:137`）。
- 单核心模式（没设 URL）照旧用 routing 进程内的表。
- 有效期从写入算起，用着不续期，与现在的进程内表相同；是否改成随使用续期（sub2api 的 `RefreshSessionTTL`，`repository/gateway_cache.go:56`）另议，见下文「落地顺序」后的待定。

## 熔断

- 每次访问的结果是成功、不可用（到期、连接错误）或出错（脚本或命令被拒，是程序错误：记错误日志，对调用方同样按不可用兜底）。
- **打开**：最近一秒内至少 20 次访问且一半以上不可用，或连续 5 次不可用。打开后 5 秒内的访问不做任何 I/O，直接返回不可用；之后半开：放一次试探出去，其余仍直接返回，试探成功即关闭，失败再打开 5 秒。后台的粘性写入同样受它约束。
- **为什么要它**：Valkey 卡住而没有熔断时，每个请求都要等满 20ms，首字延迟预算里给限流和粘性的只有 2ms（[性能的「延迟预算」](../../../docs/performance.md#延迟预算)）；有熔断时，每个节点每秒至多约 20 个请求付出这 20ms。

## 指标与告警

`axiom.kv.calls`（标签 `use`：`precheck`、`sticky_write`、`login`；`result`：`ok`、`unavailable`、`error`、`skipped`）、`axiom.kv.duration`（直方图，按 `use`）、`axiom.kv.breaker_open`（0 或 1）、`axiom.kv.dropped`（后台写入被丢弃）、`axiom.kv.memory_used_bytes` 与 `axiom.kv.evicted_keys`（后台每 30 秒读 `INFO memory`、`INFO stats`）。告警规则随告警出口（[#316](https://github.com/TYCT-tyct/Axiom/issues/316)）：熔断打开超过 1 分钟、持续有键被淘汰、内存超过上限的 90%、`precheck` 的 p95 超过 2ms。

## 部署

- **compose 服务 `valkey`**：镜像见上；以镜像自己的 `valkey` 用户启动；与其他服务同样只读根文件系统、丢弃全部 capabilities；不映射端口，只在 compose 网络里可达。启动时先把 ACL（`user default off`、`axiom` 用户及上面的权限）从环境变量 `AXIOM_VALKEY_PASSWORD` 写进 tmpfs 里的文件，再启动 `valkey-server --aclfile … --save "" --appendonly no --maxmemory 256mb --maxmemory-policy allkeys-lru`。健康检查以 `axiom` 用户 `PING`，密码经环境变量交给 `valkey-cli`，不出现在命令行里（K1 按镜像核对 `valkey-cli` 读哪个变量）。`just up` 像别的密码一样生成 `AXIOM_VALKEY_PASSWORD` 写进 `deploy/.env`。
- **`axiom` 服务**：加 `AXIOM_VALKEY_URL`；对 `valkey` 的依赖是 `service_started` 而不是 `service_healthy`，Valkey 起得慢或起不来时 Axiom 照常启动、先用兜底。
- **不持久化**：没有卷。重启后为空，即上文各用途「恢复」的情况，都有界。
- **主备与故障切换（M3）**：随热备方案（[#324](https://github.com/TYCT-tyct/Axiom/issues/324)）定，候选是一主一备加三个 Sentinel（URL 写 `master_set`，客户端自己跟随切换）或托管的 Valkey。复制是异步的，切换丢最后的写入，与重启同类。**不用集群模式**：一个请求的几个键不在同一个槽里（一批命令、多键脚本）；量也不需要：每次调用一批两条命令，每秒一千次调用约两千次操作，远低于单实例的能力。

## 测试

- **真实 Valkey**：`withcluster` 在一次 `just test` 里与 PostgreSQL 一起起一个 Valkey 容器（同一镜像、同一份 ACL 和配置），地址经 `KVTEST_URL` 传入，和 `PGTEST_CLUSTER_URL` 一样用 `os.Environ` 读，不进测试缓存键；没有这个变量时 `kvtest` 自己起容器。用例各用随机的 `ns`。改服务端状态的用例（`CLIENT PAUSE`、重启、改 ACL 或配置）用自己的容器。
- **超时**：另一条连接执行 `CLIENT PAUSE <毫秒> ALL` 让 Valkey 不应答，断言访问返回不可用、用了兜底、熔断打开，暂停结束后半开、关闭。不按墙钟判成败：期限断言的是传给客户端的 context 的截止时间不晚于 `kv.Timeout`，熔断的时钟可注入。
- **重启**：停止再启动容器，断言期间请求照常完成、恢复后重新连上，计数从零开始。
- **多节点**：同一个 Valkey 上的两个 gateway（或两个 accounts 服务）：同一个 key 的计数合在一起、不随节点数放大（#315 的验收）；一个节点写的粘性另一个读得到；登录次数共享。
- **脚本与进程内实现一致**：对 `count`、`take`、`give` 做性质测试，随机的操作序列和时间在 Lua 和 Go 两边给出相同结果；时间由调用方传入，测试不等待。
- **基准**：预检的一批（`BenchmarkPrecheck`，真实 Valkey）进分配门槛。

## 不变量

| 不变量 | 靠什么保证 |
|---|---|
| 钱和授权状态不进 Valkey | depguard 与 `tools/checkmodules`：只有 `platform/kv` 引用客户端，`billing`、`payments` 不引用 `kv`；上文「放什么、不放什么」由评审核对 |
| 每个键都有到期时间 | 计数在同一个脚本里 `INCR` 加 `PEXPIRE`，桶用 `PXAT`，粘性用 `SET … PX`；没有不带到期的写入。测试：各用法跑完后 `SCAN` 全部键，`PTTL` 都大于 0 |
| 一次模型调用至多一次同步往返，在准入之前；排队醒来为 0 | 只有预检发一批；测试按 `axiom.kv.calls` 数：一次调用 1，排队后醒来仍是 1 |
| 同步访问至多等 `kv.Timeout`，熔断打开时不做 I/O | 期限由 `kv` 统一加，调用方给不了更长的；测试见上文「超时」 |
| Valkey 不可用不会让任何请求因此失败 | 每种用途都有兜底（计数 × N、进程内的桶、不粘）；测试：暂停或停掉 Valkey，模型调用和登录照常成功 |
| 兜底不放大限额 | 计数的估计值乘 N，桶的突发除以 N、间隔乘 N；测试：N = 2 时暂停 Valkey，单个节点的计数超过限额的一半即拒绝 |
| 健康时多节点不放大限额 | 计数和桶都在 Valkey 里；测试见上文「多节点」 |
| 配置错了不启动，Valkey 暂时不在照常启动 | 「配置与启动」的表；测试逐行覆盖，连不上用不应答的端口 |
| 密码、邮箱、IP 不出现在错误、日志和键里 | 错误只报变量名；邮箱和 IP 只以 HMAC 进键；测试：错误文本不含密码，`SCAN` 出的键里没有 `@` 和测试用的 IP |

## 落地顺序

每个 PR 测试先行，都在 M2，K4 在 M3。

| # | 内容 | 验收 |
|---|---|---|
| K1 | `platform/kv`：配置与启动检查、期限、熔断、一批命令的执行、指标；`kvtest` 与 `withcluster`；compose 的 `valkey` 服务和 `just up` 的密码；gateway 预检的限流计数改走 `count` 脚本（约 1800 行） | 上表除粘性、登录外的各条，以及「超时」「重启」「多节点」的计数部分、`count` 的性质测试、`BenchmarkPrecheck`；单核心模式下现有测试不变地通过 |
| K2 | 会话粘性：预检那一批加 `GET`/`DEL`，后台写入队列，routing 的键和值（约 600 行） | 两个节点之间粘性可见；压缩标记删除；队列满丢弃并计数；Valkey 不可用时不粘、不出错；粘性键都有到期时间；单核心模式不变 |
| K3 | 登录前的尝试次数：`take`、`give` 脚本，HMAC 键，按 N 等分的兜底（约 700 行） | 两个 accounts 服务共享次数；`take`、`give` 的性质测试；键里没有个人信息；Valkey 不可用时登录照常、按份额限制；现有登录测试在两种模式下都通过 |
| K4 | 主备切换（随 #324）：部署与切换测试 | 杀掉主节点后客户端跟上新主，切换期间走兜底、没有请求因此失败 |

E4-2 的业务限额建立在 K1 的 `count` 脚本上，加维度、不加往返。

待定：粘性是否随使用续期。续期让活跃的会话一直粘着，代价是每个请求多一次后台写；与上游提示缓存的有效期（随使用续期）怎么对齐，随 K2 用数据决定，结论写回[路由的「会话粘性」](../../../docs/domains/routing.md#会话粘性)。

## 决策与理由

| 决策 | 理由 | 不选 |
|---|---|---|
| Valkey 客户端用 valkey-go，固定 v1.0.78 | 期限到时立即返回且不断开连接，迟到的回复由客户端丢掉；20ms 期限在 Valkey 变慢时会大量触发，断连接只会把变慢放大成重连风暴。自动流水线让 5000 路并发共用几条连接，不用调连接池。Valkey 项目自己维护，运行时只依赖 `x/sys`；内置 Sentinel，M3 不换客户端。代价：new-api、sub2api 都用 go-redis，它们的代码不能逐行对照 | go-redis v9（v9.22.0 实读）：context 到期被当作坏连接关掉（`error.go` 的 `isBadConn`），context 期限要另开 `ContextTimeoutEnabled`；默认对每条新连接尝试 Redis Cloud 的维护通知（`maintnotifications` 默认 `ModeAuto`）；多 xxhash、xxh3、uber atomic 等依赖。redigo：没有自动流水线，要按并发数配连接池 |
| 不持久化，`allkeys-lru`，内存设上限 | 三种数据丢了都有界（各用途的「恢复」）；持久化只换来重启后少一次突发，却要卷和磁盘写。每个键都有到期时间，淘汰只是提前到期；按最近使用淘汰留下的是正在用的计数和被攻击的桶 | RDB 或 AOF：多一个卷和写盘，快照本身也是旧的；`volatile-ttl`：先淘汰到期最早的，正好是最该留的限流计数；`noeviction`：满了写入失败，全体走兜底 |
| 核心节点数 N 是静态配置 `AXIOM_CORE_NODES` | 兜底要按 N 等分，「多核心没有 Valkey 不启动」也要知道 N；还没有节点登记，配置是最小的机制 | 在 Valkey 里登记存活的节点：Valkey 不可用时恰恰读不到，启动时连不上就不知道 N，还多一个后台循环；数 billing 的持有者：跨模块，持有者也不区分节点角色 |
| 时间由节点传入脚本，不用 Valkey 的 `TIME` | 键名要在调用前算出（窗口序号），测试才能传入时间而不等待（[检查清单的「时序」](../../../CONTRIBUTING.md#检查清单)）；节点之间的时钟差是毫秒级，对一分钟的窗口、六秒一次的回填可以忽略 | 脚本里读 `TIME`：窗口序号在脚本里算出的键没有事先声明，测试只能等真实时间 |
| Valkey 连不上照常启动，配置错了不启动 | 缓存故障不应该在重启时变成整个节点起不来；认证被拒、内存没上限是部署写错了，越早发现越好 | 连不上就不启动（new-api 的 `common/redis.go:45`）：Valkey 成了硬依赖，违背「只放丢了也不影响正确性的数据」 |
| 预检的计数和粘性同批发出，计数用一个脚本 | 一次往返；一个请求的多个维度在一个脚本里原子地加，E4-2 加维度不加往返 | 每样一次往返：首字路径多几次网络往返；计数和粘性合成一个脚本：粘性的键和值归 routing，要交给 gateway 的脚本去拼，同批发出也是一次往返 |
