# 备份与恢复

本文是异地加密备份、空白环境恢复、恢复演练和生产入口的 runbook 与理由。程序本身不导出数据：备份、加密和上传都在
`deploy/ops` 的运维镜像里跑，从 `deploy/compose.yaml` 的 `ops` profile 启动；入口在 `deploy/compose.edge.yaml`，
配置是 `deploy/ingress/Caddyfile`。两者都不随 `just up` 启动。

为什么不在程序镜像里：`pg_dump` 的版本不能低于数据库（`deploy/compose.yaml` 把库钉在 `postgres:18.6-alpine`，运维镜像
用同一个 tag），而 age、rclone、桶凭证都没有理由进那个 scratch 镜像。

## 备份

一次备份（一个 set）写进桶里，四个文件：

| 文件 | 内容 | 收件人 |
|---|---|---|
| `db.dump.age` | 全库的逻辑转储，`pg_dump --format=custom`，带属主与授权 | `AXIOM_BACKUP_DB_RECIPIENT` |
| `deploy.env.age` | 部署的 `deploy/.env`，即库的三个口令和桶的凭证 | `AXIOM_BACKUP_DB_RECIPIENT` |
| `keys.tar.age` | 密钥环 `master`、`hash`、`identity`，带权限与属主 | `AXIOM_BACKUP_SECRETS_RECIPIENT` |
| `manifest.txt` | 时间、`pg_dump` 与服务器版本、镜像标签、各文件大小、两个收件人 | — |

- set 名是 UTC 的 `YYYYMMDDHH24MISS` 加三个随机字节，按名字排序即按时间排序。
- `manifest.txt` 最后上传，它是"这组文件完整"的唯一标志：恢复只认有 manifest 的 set，落后的半组文件由保留策略按天
  清理。
- 密钥环单独一个收件人，且必须与库的收件人不同（脚本拒绝相同）：库的备份可以重放，密钥环丢了就没有第二份，一份身份
  泄露也不该同时打开两者。私钥只以文件形式留在别处，不在这台机器上，也不在桶里。
- 转储用超级用户 `postgres` 读：每张表都 `FORCE ROW LEVEL SECURITY`，按行安全策略导出的备份会静默丢行。

加密用 [age](https://age-encryption.org)：备份侧只需要公钥，因此这台机器被攻破也解不开历史 set。上传用 rclone，因为
它一个静态二进制就把 S3 兼容的桶（MinIO、R2、Backblaze、OSS）全收下，远端只用 `RCLONE_CONFIG_AXIOM_*` 环境变量配置，
凭证不进命令行。

### 保留

`AXIOM_BACKUP_KEEP_SETS`（默认 30）与 `AXIOM_BACKUP_KEEP_DAYS`（默认 30）同时生效：超过份数的丢、超过天数的也丢，所以
忙的一个月按份数收敛，闲的一个月仍按天数收敛。计数只数完整的 set，半组文件只受天数约束——这样一次失败的运行既不会挤掉
一个好的 set，也不会毁掉另一台机器正在写的 set。

两个都必须 ≥ 1，否则运行在拿锁、上传之前就退出 1：份数为 0 会删掉刚写完的 set，天数为 0 会把截止时间定在这次运行自己的那一
秒，年龄判断于是把它也算成"更早"，同样删掉它；两种情况都只剩一句 `done`。没有"永不删除"的取值，要表达它就给一个大到用不到
的天数。本次运行写出的那个 set 永远不在删除之列。

### 单实例

一次运行在状态卷（`backup_state`）上的锁文件上拿 `flock`；拿到锁的那个才继续，第二个直接退出码 75（`EX_TEMPFAIL`），
调度器据此区分"忙"和"坏了"。锁随容器消失而释放，所以进程被杀不会留下死锁；它只守住这台机器，这与"单机部署"一致
（见 [architecture.md](../architecture.md) 的「部署拓扑」）。跨机器的"同时只跑一个"不在本文范围里，真要两台一起跑，
先让调度器保证。

### 运行

```bash
# 一次备份（镜像与桶的配置在 deploy/.env，见 deploy/.env.example）
docker compose -f deploy/compose.yaml --env-file deploy/.env run --rm backup

# 定时：宿主的 cron 或 systemd timer 每天调一次，非 0 退出码就是要看的信号（75 是"上一次还没跑完"）
17 3 * * * cd /srv/axiom && docker compose -f deploy/compose.yaml --env-file deploy/.env run --rm backup
```

`run` 直接点名服务，不需要 `--profile ops`；`up` 会按 profile 跳过这两个服务，所以 `just up` 与 CI 的部署检查都不会备份
或恢复任何东西。

## 主密钥轮换

命令使用 `AXIOM_DATABASE_URL`（应用角色）和 `AXIOM_MASTER_KEY_FILE`。数据库迁移须包含 platform `00011`。
只有负责人接触密钥文件；以服务账户执行，目录仅允许该账户和负责人访问，文件 0600 或 0400，Windows 用目录和文件 ACL。
命令只打印版本号、操作结果和每批数量，不打印密钥、摘要或文件路径。不要用 shell 输出文件来核对。

1. 先做一个完整备份 set。暂停备份调度直到轮换完成，避免数据库转储和密钥归档跨过删除旧版本的时刻。
   保留旧 set 配套的 `keys.tar.age`：零在线引用并不等于历史备份不再需要旧密钥。
2. 在一个受保护的密钥目录中运行 `axiom keyring add --version 2`（用未使用过的正版本号）。
   它原子替换本机 `master` 文件，生成新随机材料，登记为可读；数据库写版本保持不变。
   文件父目录须可写，不能对只读挂载或单文件 bind mount 直接执行；可以在受保护目录中的副本上操作，再替换部署卷里的文件。
   在隔离目录操作时，先把现有完整 `master` 文件复制过去，不能拿空文件开始轮换。
3. **人工分发同一份完整文件**到所有节点（包括离线备用和将来要恢复的节点），不要在各节点分别生成同版本的密钥。
   对每个实际部署文件运行 `axiom keyring check`；它检查全部可读版本、写版本、数据库引用和材料摘要。
   所有节点通过后才能继续；命令不能证明你已经检查了所有节点，自动分发和确认留给 #322。
4. 排空并停止所有 `serve` 实例及其他会写信封的进程，禁用自动重启。密钥环只在启动加载；
   advisory lock 只串行化轮换命令，不能阻止一个旧进程继续写旧版本。**维护窗口中保持停写，直到第 7 步。**
   停写后至少等 30 秒再执行第 5 步：`activate` 和 `retire` 在同一事务里检查 `platform.nodes` 的最近心跳，
   还有一个节点在 kv 的心跳窗口（30 秒，三次心跳）内写过就拒绝，不区分它是不是本机。
5. 运行 `axiom keyring activate --version 2`，再运行
   `axiom keyring rewrap --batch-size 100`。每批成功输出一行；结束输出 `{"done":"rewrap"}`。
   可加 `--max-batches 10`，达到上限输出 `{"paused":"rewrap"}`，表示仍需继续，不表示完成。
   被中断、输出管道断开或某批失败，重复同一命令即可；已提交批次不会重复加密或重复记审计。
   错误批次整批回滚；认证失败需先排查损坏或 AAD，不能通过改版本号跳过。
6. 运行 `axiom keyring retire --version 1`。写版本和任何存量引用都会拒绝退役；
   成功先提交退役墓碑及审计，再从本机文件移除旧行。文件删除失败时重复此命令，不能复用旧版本号或旧材料。
   文件替换与数据库提交跨两个存储：新增登记失败可能留下一把未登记密钥，退役删文件失败可能多留旧密钥，重试会恢复。
   把退役后的文件再次分发至全部节点，逐节点运行 `axiom keyring check`；文件里仍带已退役版本的节点会报
   `retired versions still in the local file`（只报版本号），替换文件前不能启动。
   **退役是单向门：** 此后任何仍引用旧版本的行都读不出，也不能用 `add` 把同一材料换一个新版本号登记（指纹墓碑拒绝）。
   这类行只能靠恢复整套旧备份找回，即数据库和该 set 配套的密钥环一起恢复；没有只找回这些行的办法。
7. 重启所有节点（启动前也会校验密钥版本），恢复备份调度并做新的完整 set。保留轮换前的加密备份直到其正常过期。
   审计 `keyring.initialize/add/activate/rewrap/retire` 在 `platform.audit_log`，只含版本、表名与数量。

重包裹只更新[平台信封清单](../../internal/platform/README.md#凭证加密)中各存储的包裹 DEK 与主密钥版本，不改数据密文、nonce、所属业务对象的状态或修订；Webhook 签名密钥也在该清单内，其签名材料和轮换重叠期限不变。
新增存储信封的表必须加入 `cmd/axiom` 的清单；启动和命令遇到未登记的 `wrapped_dek` 列会拒绝继续，避免漏迁后退役。
API key 哈希用的 HMAC `hash` 和派生用的 `identity` 文件不在此命令范围，不能把它们当成 `master` 操作。

恢复旧备份时必须恢复该 set 的数据库和密钥环，不能搭配已经退役并删去旧版本的当前文件。
轮换失败但新版本尚未退役时，可以在保持停写的情况下重新激活旧可读版本，再重包裹；不要直接回滚文件而保留新数据库状态。
数据库和文件的联合状态由 `check` 核对通过后才能启动。

## 恢复

恢复只支持**空白环境**：没有数据库、没有密钥环。这样 initdb 才会用 set 里的口令建集群、建两个登录角色
（`deploy/postgres/roles.sh`），程序和库的口令自然一致。就地覆盖一台正在运行的库不在支持范围里（口令、角色和正在进行
的写入都不是 set 能决定的）。

前提：

1. `deploy/.env` 至少有桶的设置（`AXIOM_BACKUP_BUCKET`、`AXIOM_BACKUP_S3_*`，前缀不是 `axiom` 时还有
   `AXIOM_BACKUP_PREFIX`）。三个口令可以随便填占位值：恢复的第 1 步会用 set 的副本把整个文件换掉。恢复脚本在口令缺失时
   自己也会补占位值好让 compose 能解析，但下面那条自己敲的 `build` 不会，所以先填上更省事。
2. 镜像要在这台机器上，并且与 set 的版本一致（第 1 步打印的 `manifest.txt` 里有 `app_image`）。比 set 新的镜像会先跑完自己
   的迁移，多出来的表、列和约束在 set 里没有，随后的恢复会失败（第 4 步先把迁移建的对象整体删掉，正是为了让它们不参与
   冲突，但新迁移写进 `platform.goose_*` 的版本行也在转储之外）。升级放在恢复之后做。构建方式与 `just up` 相同：

   ```bash
   just web-embed
   CGO_ENABLED=0 GOOS=linux go build -trimpath -o deploy/bin/axiom ./cmd/axiom
   docker compose -f deploy/compose.yaml --env-file deploy/.env --profile ops build
   ```

   恢复脚本开头用 `docker image inspect` 逐个检查镜像在不在，不在就在第 1 步之前报错。
3. 两个 age 私钥文件（库的、密钥环的），路径给 `AXIOM_RESTORE_DB_IDENTITY` 和 `AXIOM_RESTORE_SECRETS_IDENTITY`。它们只
   在恢复时只读挂进容器，不属于部署配置，也不进 `deploy/.env`。

```bash
export AXIOM_RESTORE_DB_IDENTITY=/etc/axiom-backup/db.key
export AXIOM_RESTORE_SECRETS_IDENTITY=/etc/axiom-backup/keys.key
bash scripts/ops/restore.sh 20261019030017-4f8a1c

# 带生产入口的部署：-f 传的就是它平时启动用的那组文件，程序才会回到入口需要的那张网络和那个代理地址上
bash scripts/ops/restore.sh -f deploy/compose.yaml -f deploy/compose.edge.yaml 20261019030017-4f8a1c
```

`-f` 与 compose 的规则一致：给了就替换默认的 `deploy/compose.yaml`，不给就用它。

脚本按顺序做六步，每步都有输出，失败即停：

1. 取 set 的 `deploy.env.age`，用库的身份解开，装成 `deploy/.env`（权限 600）。没有口令先建集群的顺序问题就在这里解决。
2. 用这份配置启动 `postgres`：空卷 → initdb → `deploy/postgres/roles.sh` 建 `axiom_app` 与 `axiom_migrator`。
3. `axiom migrate`：`axiom_owner`、`axiom_lookup` 和各模块的 schema 属于迁移，由程序建。转储里每个对象的属主都是这两个
   `NOLOGIN` 角色，所以恢复前它们必须存在；角色定义只有一处（迁移），脚本不抄第二份。
4. 取 `db.dump.age` 解开后先删掉迁移建的那些 schema（属主是 `axiom_owner` 的全部，`public` 不在其中），再
   `pg_restore --single-transaction`：对象、属主、授权都来自 set。删而不是 `--clean`，是因为分区表的分区继承父表的约束，
   单独删一个继承来的约束会被拒绝。取 `keys.tar.age` 解开进密钥卷，属主 65532、权限 0400（程序启动时检查）。
5. 再 `axiom migrate` 一次：set 比镜像旧时补上新的迁移，一致时是空操作。
6. `compose up --wait` 启动程序，等它自己的启动检查通过，再验一次请求：文件组合里有入口时，经入口按各自域名打控制台和模型
   API（控制台域名上 `/api/console/v1/site` 是 200，API 域名上同一路径是 404，`/v1/models` 无 key 是 401），没有入口时才
   打程序自己发布的两个回环端口。**只查回环端口是不够的**：入口连不上程序时那里照样 200，而外部请求全是 502。

整个恢复过程固定使用启动时那一份桶的位置和凭证：脚本先问恢复容器自己解析出的 `AXIOM_BACKUP_*`、`RCLONE_CONFIG_AXIOM_*`
是什么，再把它们导出成 compose 的同名变量。compose 的插值先看环境再看 `--env-file`，所以第 1 步装进来的 set 副本改不了它们；
不固定的话，set 里的 `deploy/.env` 记的是备份那天的位置和凭证，可能已经轮换或搬走，第 4 步就会拿旧凭证去取转储——而那时库已经
被换掉了。放环境里而不是当参数传，是因为参数会留在进程列表里，而它们是凭证。

恢复完要做的：

- 用 `--overwrite` 重跑一次之前失败的恢复时，脚本会先删掉这个项目的库卷和密钥卷，再从头来一遍（它只删用这两个卷的容器，
  同项目里别的容器——演练的 S3——留着，因为 set 还在那里）。
- 恢复之后核对 `manifest.txt` 里的镜像标签与本机部署的是否一致；不一致的话，先让程序在第 5 步的迁移上跑通再对外。
- 换掉泄露的口令与桶凭证后，重新做一次备份：set 里的 `deploy/.env` 是旧的。

## 恢复演练

`scripts/ops/rehearse.sh`（`just backup-rehearsal`）把上面整条路走一遍，并且不碰任何真实部署：

- 自己的 compose 项目 `axiom-rehearsal`、自己的 `deploy/.env.rehearsal`、内核分配的宿主机端口（程序的和入口的都是）；
- 先用两种 `AXIOM_EDGE_TLS_ISSUER` 各解析一次 `deploy/ingress/Caddyfile`（`caddy validate`）：`internal`，和变量缺失时 compose 填进去的那个值。
  演练自己跑 `internal`，所以另一个取值——有公网证书的部署用的那个——只能这样验到；
- 开跑先把继承来的 `AXIOM_*`、`RCLONE_*`、`DEPLOY_*` 和 `COMPOSE_FILE` 一类变量清掉：compose 的插值优先取 Shell 环境，
  操作者导出过的生产桶名和凭证会覆盖 `--env-file`，第一次备份就写进真桶、再按演练的保留策略删掉真实 set；
- 本地 S3 容器（`deploy/compose.rehearsal.yaml` 里的 RustFS）代替桶，set 随栈一起销毁；
- 上传和删除（备份、以及第 10 步的搬迁）先在容器里断言 `AXIOM_BACKUP_BUCKET`、`AXIOM_BACKUP_PREFIX`、
  `RCLONE_CONFIG_AXIOM_ENDPOINT`、`RCLONE_CONFIG_AXIOM_ACCESS_KEY_ID` 确实是演练 S3 和它的专用桶、前缀，不是就退出；
- 种一个站点和一个用户，停程序，记录全库内容指纹（含每张表的行数）、角色与成员关系、密钥环哈希、`deploy/.env`；种数验证
  按名断言 `accounts.sites` 与 `accounts.users` 各有行——空表也有一行指纹，只数"有指纹的表"证明不了种子发生过；
- 备份；验证保留为 0（份数和天数各一次）在写任何东西之前退出 1 且桶不变；验证持锁时第二个运行退出 75；验证密钥环用库的
  身份解不开、用密钥环的身份解得开；
- 删掉库卷和密钥卷（S3 容器留着，set 在它这里），删掉 `deploy/.env` 里的一行标记，再走一遍上面的恢复脚本，文件组合带上
  `deploy/compose.edge.yaml`；
- 比对表清单与指纹（三类行除外，见下）、角色、密钥环、环境文件，并经入口各打一次控制台域名和 API 域名；
- 连续再备份两次，验证保留只留下最新两份、最早那份被删；
- 轮换 S3 凭证（重建演练的 S3 容器，卷不动）并把整个前缀搬到 `axiom-moved`、把 `AXIOM_BACKUP_PREFIX` 改过去，再恢复一次：
  这一遍只有"固定使用启动时的位置和凭证"才能走完，因为 set 里的配置写的是搬走之前的位置和已吊销的凭证；
- 打印一行可直接粘到下表的记录，含 RTO（从执行恢复脚本到 `compose up --wait` 返回）。

指纹比较按行内容，跳过三类会被程序自己改动的行：River 的队列表（库自己的迁移版本由程序的启动检查核对）、
`billing.holders`（每次核心进程启动插一行，恢复后的库里既有 set 里的那行也有新的）、`billing.expiry_cursor`（到期任务每分钟
更新自己扫到哪）。表本身少一张就失败，所以跳过的是行，不是表。

它要 Docker、go、node、pnpm 和约十五分钟，并且会删掉自己创建的东西（`trap` 里 `compose down --volumes` 加删镜像）。同一台
机器上只能跑一个。入口在演练里用 `AXIOM_EDGE_TLS_ISSUER=internal`（自己的 CA，所以检查带 `--insecure`），域名是 `.example`，
没有任何请求离开这台机器；发布的端口由内核分配，检查用 `--connect-to` 指过去而请求仍走 443，免得端口进了 `Host` 头（程序按它
比对站点域名）；网络换成 `172.29.0.0/24`，免得和这台机器上真在跑的入口抢。

### 恢复演练记录

每次演练追加一行：脚本最后打印的那行可以直接粘进来。RTO 从执行恢复脚本算到 `compose up --wait` 返回，不含备份；备份集是那次恢复
用的 set，它可能随后被同一场演练的保留步骤按份数清掉，记录仍然指它。

| 时间（UTC） | 备份集 | RTO | 自动检查 | 结论 |
|---|---|---|---|---|
| 2026-10-20 02:01 | 20261020020036-cef1ec | 19s | 数据库、角色、密钥环、配置一致，入口两个域名应答正确 | 通过 |

## 生产入口

`deploy/compose.edge.yaml` 在程序前面加一个 Caddy 容器，配置是 `deploy/ingress/Caddyfile`：

```bash
docker compose -f deploy/compose.yaml -f deploy/compose.edge.yaml --env-file deploy/.env up --detach --wait
```

单独一个文件，是因为入口用的网络钉了子网（程序只认地址/CIDR，入口得有固定地址），而钉死的子网在跑多个项目的机器上可能
撞车：没有入口的部署（`just up`、CI 的部署检查）不该带上它。

**三个域名，不是两个。** `accounts.site_domains` 的域名是主键，一个域名只属于一种 surface（`console`、`api`、`admin`），
所以控制台和模型 API 必须是两个域名：

- `AXIOM_EDGE_CONSOLE_HOST`：站点的页面、控制台和控制台 API。`/api/console/v1/*` 在这里，`/v1/*` 不在这里。
- `AXIOM_EDGE_API_HOST`：站点的模型 API（`/v1/*`、`/api/v1/*`，流式）。控制台的路由在这里一律 404。
- `AXIOM_EDGE_ADMIN_HOST`：管理应用和管理 API。

用同一个域名兼作两者做不到：程序自己就会拒绝。域名登记为 `console` 时，模型请求经 `internal/gateway/handler.go` 的
`authenticate` 得到 401；登记为 `api` 时，控制台的路由经 `cmd/axiom/console_guard.go` 的 `admit` 得到 404（`ErrUnknownHost`）。
两个站点的 `reverse_proxy` 都原样转发请求，Host 不变——程序就是按它找站点和 surface 的，所以域名必须与
`axiom site init --console HOST --api HOST --admin HOST` 登记的一致。

- **TLS**：默认由 Caddy 自己申请和续期证书（ACME HTTP-01，80 端口要能到达这个容器），证书与账号密钥放在 `caddy_data` 卷里；
  失败原因看 `docker compose logs caddy`。没有公网 DNS 的部署（演练、内网）设 `AXIOM_EDGE_TLS_ISSUER=internal`，改用 Caddy
  自己的 CA，根证书在 `caddy_data` 卷的 `pki/authorities/local/root.crt`，客户端要信任它。
  Caddyfile 里写的是 `tls { issuer {$AXIOM_EDGE_TLS_ISSUER:acme} }`，不是 `tls {$AXIOM_EDGE_TLS_ISSUER}`：后者在值为空时
  只剩一个 `tls`，Caddy 直接报解析错误，而「公网证书」这一档以前正是用空值表示的。`{$VAR:默认}` 也只在变量**未设置**
  时生效，空值会盖掉默认——所以 `compose.edge.yaml` 一律把 `acme` 填进去，不让空值传到容器里。
- **HTTP/2**：`servers { protocols h1 h2 }`。只开 TCP：栈发布的是 TCP 的 80/443，广告 HTTP/3 只会白费第一次 UDP 往返。
- **端口**：`AXIOM_EDGE_HTTP_PORT` / `AXIOM_EDGE_HTTPS_PORT` 默认 80/443；留空则由内核分配，给同机的第二套栈或演练用。
- **X-Forwarded-For**：Caddy 是第一个 hop，默认忽略客户端自带的 `X-Forwarded-*`，按连接对端地址重写。程序只在请求来自
  可信地址时才读它，所以 `compose.edge.yaml` 把 `AXIOM_TRUSTED_PROXIES` 设成入口在这个网络里的固定地址
  （`${AXIOM_EDGE_PROXY_ADDRESS:-172.28.0.10}/32`）。经宿主发布端口进来的请求来自另一个网络，因此不被信任：伪造
  `X-Forwarded-For` 拿不到别人的限流额度，也改不了审计里的地址。
- **流式**：不写 `flush_interval`、不加 `encode`。Caddy 对 `Content-Type: text/event-stream`（以及长度未知的响应）写一次
  下发一次，客户端断开就取消上游请求；负的 `flush_interval` 反而会在断开后把这次调用跑完。路径不用单独配：判断依据是
  响应头，不是路径。
- **超时**：`read_header 10s`、`read_body 2m`（与程序自己 2 分钟的请求体上限一致）、`write_idle 1h`（默认 1 分钟会把
  长时间没有新 token 的流掐掉）；不设总写超时——长回答不是故障，单次调用由程序自己限时。
- **来源限制**：`/api/admin/v1/*` 在控制台域名和 API 域名上都一律 404；管理应用只在 `AXIOM_EDGE_ADMIN_HOST` 上回答，且只对
  `AXIOM_EDGE_ADMIN_ALLOW_CIDRS`（默认只有本机，忘了配就是把入口关掉）。`/console/*` 与 `/api/console/v1/*` 对
  `AXIOM_EDGE_CONSOLE_ALLOW_CIDRS` 开放，默认所有人：它是用户的控制台，私有部署可以把它关进内网。API 域名不设来源限制：
  它的调用方本来就是任意的机器。
- 三个域名都要有指向这台机器的 DNS。
- 前面再放 CDN 的话，给 `servers > trusted_proxies` 加 CDN 的网段、开 `trusted_proxies_strict`，并把 443 只对 CDN 开放，
  否则客户端可以自带 `X-Forwarded-For`。

## 决策与代价

- **逻辑转储，不是 WAL 归档或基础备份**：单机、库在一个卷里；PITR 要多一套归档、恢复目标和操作练习，换来的是"误删后
  回滚到某一秒"，而这里要的是"机器没了能重建"。代价：RTO 是一次全量恢复（演练记录里有实测值），不是秒级。
- **rclone，不是 aws-cli**：一个静态二进制覆盖所有 S3 兼容实现，`copyto`、`lsf --dirs-only`、`purge` 正好对上 set 目录与
  保留；aws-cli 要 Python 运行时，也只对 AWS 的 S3 顺手。代价：桶的语义停在 rclone 支持的子集里（这里只用到对象读写与
  列举）。
- **age，不是 gpg**：只认公钥、没有信任网、两个收件人就是两行；gpg 的信任、过期和子密钥在"备份机器只放公钥"这件事上
  没有收益。
- **角色不单独备份**：`axiom_app`、`axiom_migrator` 由 `deploy/postgres/roles.sh` 建，`axiom_owner`、`axiom_lookup` 由
  迁移建；恢复时先跑迁移（角色与 schema 由它建，这一步结束再整体删掉 schema），角色定义只有一份，口令在 `deploy/.env`
  里随库一起备份。
- **仓库里的文件不进 set**：`deploy/compose.yaml`、`deploy/compose.edge.yaml`、`deploy/ingress/Caddyfile` 都在 git 里，
  manifest 记下镜像标签就够了；只有不是代码的 `deploy/.env` 需要备份。恢复因此要显式拿到这套文件（`-f`）：set 里没有它们，
  而缺了入口那一份，程序回来时就没有入口需要的网络和代理地址。
- **入口分两个公开域名**：`accounts.site_domains` 的域名是主键，一个域名只属于一种 surface，所以控制台与模型 API 只能各用
  一个域名。代价是每个部署要三份 DNS 与证书，注册站点时也要三个 host；换来的是每种 surface 的来源限制能分开设，程序自己
  的拒绝逻辑不必改动。

## 待定

- 定时归宿主（cron / systemd timer），镜像里没有调度器。要变成多机部署时，"同时只跑一个"得换成桶或数据库上的协调，锁的
  范围会失效。
- 演练还没有进 CI 常跑：要 Docker、要建镜像、约十五分钟，而且自带破坏性。要不要作为发布前的必跑项，等 CI 机器的余量定下来
  再定。
- 公网证书这条路没有在任何地方验过：演练用 `.example` 域名和 Caddy 自己的 CA，没有公网 DNS 也就没有 ACME。第一次真实部署时
  才第一次走 HTTP-01，失败原因看 `docker compose logs caddy`。
- 演练用的桶是 `rustfs/rustfs`，一个钉住版本的第三方 S3 实现。MinIO 自己已经拿不到公开镜像（Docker Hub 的仓库没了，
  quay.io 返回 401），而 `adobe/s3mock` 之类的替身并不真存对象，测不出保留和重取。桶的语义只用到对象读写和列举，换成别的
  实现时改 `deploy/compose.rehearsal.yaml` 的一个服务就够。
