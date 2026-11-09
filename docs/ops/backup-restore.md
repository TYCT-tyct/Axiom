# 备份与恢复

本文是异地加密备份、空白环境恢复、恢复演练、WAL 连续归档与时间点恢复、生产入口的 runbook 与理由。逻辑 set（一次备份写进桶里的四个文件）在
`deploy/ops` 的运维镜像里跑，从 `deploy/compose.yaml` 的 `ops` profile 启动；WAL 归档和基础备份在数据库镜像
`deploy/postgres` 里跑，由数据库入口在配置了桶时打开。入口在 `deploy/compose.edge.yaml`，配置是
`deploy/ingress/Caddyfile`。这些备份作业都不随 `just up` 启动；数据库本身会启动，并按配置决定是否归档。

为什么是独立的运维镜像：`pg_dump` 的版本不能低于数据库（`deploy/compose.yaml` 把库钉在 `postgres:18.6-alpine`，运维镜像
用同一个 tag），而 age、桶凭证和写桶的代码都没有理由进那个面向公网的 scratch 镜像。镜像里的 S3 客户端不是另装的工具，
而是从发布镜像取出的 `axiom` 二进制（下一节）。WAL 归档不走这个镜像：pgBackRest 必须和 PGDATA 在同一台机器上，装在数据库
镜像里（「WAL 连续归档与时间点恢复」）。

## 备份

一次备份（一个 set）写进桶里，四个文件：

| 文件 | 内容 | 收件人 |
|---|---|---|
| `db.dump.age` | 全库的逻辑转储，`pg_dump --format=custom`，带属主与授权 | `AXIOM_BACKUP_DB_RECIPIENT` |
| `deploy.env.age` | 部署的 `deploy/.env`，即库的三个口令和桶的凭证 | `AXIOM_BACKUP_DB_RECIPIENT` |
| `keys.tar.age` | 密钥环 `master`、`hash`、`identity`，带权限与属主 | `AXIOM_BACKUP_SECRETS_RECIPIENT` |

节点证书（`node.crt`、`node.key`、`ca.crt`）不在 set 里：它们由部署的内部证书机构签发，丢了可以重新签发，不该跟主密钥一起复制到备份桶。每个节点只保存自己的私钥，入口不拿核心的证书。
| `manifest.txt` | 时间、`pg_dump` 与服务器版本、镜像引用（生产为 digest）、各文件大小、两个收件人 | — |

- set 名是 UTC 的 `YYYYMMDDHH24MISS` 加三个随机字节，按名字排序即按时间排序。
- `manifest.txt` 最后上传，它是"这组文件完整"的唯一标志：恢复只认有 manifest 的 set，落后的半组文件由保留策略按天
  清理。
- 密钥环单独一个收件人，且必须与库的收件人不同（脚本拒绝相同）：库的备份可以重放，密钥环丢了就没有第二份，一份身份
  泄露也不该同时打开两者。私钥只以文件形式留在别处，不在这台机器上，也不在桶里。
- 转储用超级用户 `postgres` 读：每张表都 `FORCE ROW LEVEL SECURITY`，按行安全策略导出的备份会静默丢行。版本清单也从 `db.dump` 内用同一镜像的 `pg_restore` 读出，不再用备份前后的实时查询：只有转储快照里的 `platform.key_versions` 决定归档必须含哪些密钥。

加密用 [age](https://age-encryption.org)：备份侧只需要公钥，因此这台机器被攻破也解不开历史 set。上传、列举、删除和
恢复时的下载都调程序自己的 S3 客户端（`axiom objectstore`，见 [platform/objectstore](../../internal/platform/objectstore/README.md)）：
运维镜像从发布镜像里取同一个 `/axiom` 二进制，用 `AXIOM_BACKUP_S3_*` 映射出的 `AXIOM_OBJECTSTORE_*` 配它自己的桶。
这样签名、期限、重试和 multipart 只有一份实现，备份不会因为另装一个 S3 工具而与程序的行为分叉。`AXIOM_BACKUP_S3_ENDPOINT`
留空表示 AWS S3 本身（与 `deploy/.env.example` 一致），ops 脚本按 `AXIOM_BACKUP_S3_REGION` 补成
`https://s3.<region>.amazonaws.com`（中国区为 `amazonaws.com.cn`）；程序自己的 `AXIOM_OBJECTSTORE_ENDPOINT` 仍然必须显式给出。

### 与程序的对象存储分开

程序有自己的可选对象存储（`AXIOM_OBJECTSTORE_*`，见 [platform/objectstore](../../internal/platform/objectstore/README.md)），
它不是一个备份桶：两者用不同的桶和不同的凭证。备份 set 里有密钥环和 `deploy/.env`，拿到桶的凭证就能读走或删掉恢复一台
机器所需的一切；程序的凭证只给归档，公开面上的进程被攻破也不应该碰到备份。`deploy/compose.yaml` 只把
`AXIOM_OBJECTSTORE_*` 传给 `axiom`；`ops` 服务的同名变量在容器内由 `AXIOM_BACKUP_BUCKET`、`AXIOM_BACKUP_S3_*` 映射而来，
宿主的备份凭证因此进不了程序容器。两边用的是同一个客户端，不是同一个桶。

备份的 check、上传、列举、删除和恢复的下载都走 `axiom objectstore`。程序那一侧的写入方是调用记录的保留任务：整月
离开热窗口的 `call_log` / `call_attempt` 分区经同一个客户端写到 `call-log/v1/<YYYY-MM>/`，清单确认后才从 PostgreSQL
删除；没有配置程序桶时任务只预建分区，不删任何东西。程序的桶一旦配好，`AXIOM_RETENTION_DATABASE_URL` 也必须设置，
否则启动拒绝，免得归档看着是开的、其实没有任务在跑。`just up` 会写这个变量；手工部署按 `.env.example` 补。这条链不是
备份：归档对象不设到期，也不在备份 set 里，丢了它只丢调用明细，账本和汇总仍在。对话正文的开关也仍然关着。
核对程序那一侧的桶和凭证（只做一次 HEAD，不读不写；桶不存在时可以用 `axiom objectstore create` 建；桶已存在时它只做
HEAD，不再 PUT，避免 S3 重置已有桶的 ACL；命令会报错，确认桶归你再加 `--allow-existing`）。HEAD 只能证明这些凭证能访问桶，
不能证明桶归你所有：桶策略必须只允许这组凭证，不要指向别人拥有的公开桶。

```bash
docker compose -f deploy/compose.yaml --env-file deploy/.env exec axiom axiom objectstore check
```

### 保留

`AXIOM_BACKUP_KEEP_SETS`（默认 30）与 `AXIOM_BACKUP_KEEP_DAYS`（默认 30）同时生效：超过份数的丢、超过天数的也丢，所以
忙的一个月按份数收敛，闲的一个月仍按天数收敛。计数只数完整的 set，半组文件只受天数约束——这样一次失败的运行既不会挤掉
一个好的 set，也不会毁掉另一台机器正在写的 set。

两个都必须 ≥ 1，否则运行在拿锁、上传之前就退出 1：份数为 0 会删掉刚写完的 set，天数为 0 会把截止时间定在这次运行自己的那一
秒，年龄判断于是把它也算成"更早"，同样删掉它；两种情况都只剩一句 `done`。没有"永不删除"的取值，要表达它就给一个大到用不到
的天数。本次运行写出的那个 set 永远不在删除之列。

`AXIOM_BACKUP_TRANSFER_TIMEOUT`（默认 `12h`）是一次上传或下载的总期限，按 5 GiB 转储、2 MiB/s 的链路留出余量；CLI 的
`--timeout` 默认仍是 30 分钟，备份和恢复脚本显式传这个值，传输卡住时仍会结束。

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

## WAL 连续归档与时间点恢复

逻辑 set 回答「机器没了怎么重建」；这一节回答「库还在，但写错了怎么回到写错之前」。两者写同一个桶，但前缀不同：逻辑 set 在
`<AXIOM_BACKUP_PREFIX>` 下，WAL 仓库在 `<AXIOM_BACKUP_WAL_PREFIX>` 下（默认 `<AXIOM_BACKUP_PREFIX>-pgbackrest`），两者平级；
逻辑 set 的保留只列举和删除自己的前缀，删不到 WAL 仓库。两个前缀都会去掉首尾斜杠（`axiom/` 按 `axiom` 算）；
`AXIOM_BACKUP_WAL_PREFIX` 不能设成 `<AXIOM_BACKUP_PREFIX>` 或它的子路径，否则逻辑 set 的保留会把归档一起删掉，`pgbackrest`
和数据库启动会直接拒绝这个组合。

数据库镜像 `deploy/postgres` 是钉住的 `postgres:18.6-alpine` 加 pgBackRest 2.58.0（Alpine 3.24 的
`pgbackrest=2.58.0-r0`）。WAL 桶（默认 `AXIOM_BACKUP_BUCKET`，可用 `AXIOM_BACKUP_WAL_BUCKET` 覆盖）非空时，`entrypoint.sh` 给服务器加 `archive_mode=on`、
`archive_command=pgbackrest --stanza=axiom archive-push %p`、`restore_command=pgbackrest --stanza=axiom archive-get %f %p`
和 `archive_timeout=300`；为空时一个参数都不加，`archive_mode` 仍是 `off`。`archive_timeout` 让空闲库最多 5 分钟就封存当前
WAL 段，时间点恢复最多丢这 5 分钟，而不是等段写满。这些是启动参数，不写进 PGDATA，所以同一个镜像开与不开归档的数据目录
一样，恢复出来的库也不会自动往源仓库归档（`pitr.sh` 启动时没有这些参数）。

仓库整体用 `AXIOM_BACKUP_CIPHER_PASS` 加密（AES-256-CBC，基础备份和 WAL 都加密）。它和 age 私钥一样要另存一份：丢了它，
WAL 仓库读不出来；逻辑 set 仍能用 age 私钥恢复。仓库里已经有明文 pgBackRest 数据时不能直接改口令，要另建仓库。

### 开启与日常

1. `deploy/.env` 设 `AXIOM_BACKUP_BUCKET`、`AXIOM_BACKUP_S3_*`、`AXIOM_BACKUP_CIPHER_PASS`（前缀不是 `axiom` 时还有
   `AXIOM_BACKUP_PREFIX`；WAL 用单独的桶时设 `AXIOM_BACKUP_WAL_BUCKET`，否则跟随备份桶；AWS S3 把
   `AXIOM_BACKUP_S3_URI_STYLE` 改成 `host`）。WAL 桶非空却不设口令时数据库拒绝启动并列出缺的变量。
2. 数据库镜像从本仓库构建，不像程序镜像那样按 digest 拉取：

   ```bash
   docker compose -f deploy/compose.yaml --env-file deploy/.env build postgres
   docker compose -f deploy/compose.yaml --env-file deploy/.env up --detach --wait postgres
   ```

3. 第一次启动后马上建 stanza。之前 archive-push 会失败（仓库里还没有 `archive.info`），这是预期的，不是数据损坏：

   ```bash
   docker compose -f deploy/compose.yaml --env-file deploy/.env exec -T postgres \
     pgbackrest --stanza=axiom --log-level-console=info stanza-create
   docker compose -f deploy/compose.yaml --env-file deploy/.env exec -T postgres \
     pgbackrest --stanza=axiom check
   ```

4. 全量基础备份：`docker compose ... run --rm basebackup`。`basebackup.sh` 每次都做 `--type=full`：PITR 不依赖可能被一次失败
   运行打断的增量链，代价是每次读全库、仓库里留 7 份全量。`repo1-retention-full=7` 是份数，`repo1-retention-archive` 没单独
   设时按这 7 份全量保留 WAL（`pgbackrest help expire repo1-retention-archive`，2.58.0），PITR 窗口约等于 7 个基础备份的间隔。
   调度归宿主；下面每天一次，大库改成每周一次，窗口就是 7 周：

   ```bash
   17 3 * * * cd /srv/axiom && docker compose -f deploy/compose.yaml --env-file deploy/.env run --rm basebackup
   ```

5. 核对：`docker compose ... exec -T postgres pgbackrest --stanza=axiom info` 看 `wal archive min/max` 和备份；`check` 验证
   仓库可读、WAL 可推。
6. 不要在桶上给 `<prefix>-pgbackrest` 配生命周期删除规则：pgBackRest 的 `expire` 已经按备份保留清理，外部删除会让剩下的
   基础备份缺 WAL。桶的版本控制、对象锁和第二个副本由部署方按需要开。

### 时间点恢复

目标时间必须晚于要用的基础备份的 stop time（pgBackRest 拒绝同一秒内的目标），先用 `pgbackrest info` 确认那个时间点有基础
备份、WAL 覆盖到目标。恢复写进单独的卷 `postgres_pitr`，绝不覆盖正在运行的库；大库恢复前停掉源库的写入：恢复和源的
`archive-push` 共用 `pgbackrest` 锁卷，恢复期间源库的归档会排队，WAL 在 `pg_wal` 里堆积。

```bash
# 目标时间用 UTC 带时区；例如误删发生在 2026-10-30 12:00，恢复到 11:59
export AXIOM_PITR_TARGET='2026-10-30 11:59:00+00'
docker compose -f deploy/compose.yaml --env-file deploy/.env run --rm --service-ports --name axiom-pitr pitr
```

`pitr.sh` 要求 `postgres_pitr` 是空的，取满足目标时间的最新基础备份，用 `--type=time --target` 重放到目标、`--target-action=promote`
提升，然后在 `127.0.0.1:${AXIOM_PITR_PORT:-5433}` 上启动。它在恢复实例上不打开归档：这个实例只用来核对，不能把 WAL 推回
源仓库；确认接管时才停源库、换卷，正常入口会按配置重新打开归档。

在另一个终端核对：

```bash
docker exec axiom-pitr psql -U postgres -d axiom -Atc \
  "select pg_is_in_recovery(); select count(*) from <表>;"
```

`pg_is_in_recovery()` 必须是 `f`；行数和关键行与目标时刻一致。对不上就丢掉这个卷重来：先 Ctrl-C 停掉恢复实例（`--rm` 会删除
容器，或者 `docker rm -f axiom-pitr`），再删掉 `postgres_pitr` 卷（默认项目名 `axiom`，按 `docker volume ls` 的实际名字替换；
不要删 `postgres` 卷），然后从空的卷重新跑。不要在半恢复的卷上继续。

**密钥环也要覆盖目标时刻。** PITR 只恢复数据库，不恢复 `keys` 卷。如果目标时间早于一次主密钥轮换，恢复库的
`platform.key_versions` 仍引用旧版本，而当前的 `keys/master` 可能已在退役后删掉它：行数核对会通过，程序启动却报
`missing versions [1]`，旧凭证也解不开。接管前先看恢复库要求的版本：

```bash
docker exec axiom-pitr psql -U postgres -d axiom -Atc \
  "select version, state from platform.key_versions order by version"
```

每次轮换前把当时的完整 `master` 文件另存一份（和 age 私钥分开，覆盖整个 PITR 窗口）；这份归档保持不可变，不要直接拿它检查。
`axiom keyring check` 不是只读的：数据库里已退役且没有任何信封引用的版本，会像节点启动时一样从它在 `AXIOM_MASTER_KEY_FILE`
指向的文件里删掉（`internal/platform/secret/rotation.go` 的 `pruneRetired`）。所以先复制到独立、可写的候选目录，只对副本检查：

```bash
work=$(mktemp -d)
install -m 600 /path/to/historical-master "$work/master"
# 把 AXIOM_MASTER_KEY_FILE 指向 "$work/master"（容器挂载必须可写，不要 :ro），其余连接参数同恢复库。
```

通过后才把这份已检查的副本放进 `keys` 卷（0600，属主按部署的密钥服务要求），归档原件保持不动；恢复到更早目标时从归档重新复制，
不要复用已被检查删减的副本。检查失败就不要接管：换用含所需版本的历史文件，或放弃这个目标时间。

确认正确后，先停掉恢复实例，再停源库，把恢复卷的内容换进 `postgres` 卷。旧卷先复制一份到 `axiom_postgres_before_pitr`，确认
新库没问题再删；不要在没有任何副本的情况下直接覆盖旧卷：

```bash
# 先 Ctrl-C 停掉上一步的 pitr run，再：
docker compose -f deploy/compose.yaml --env-file deploy/.env stop postgres
# 名字已存在时先删掉旧的或换一个名字，不要往非空的副本卷里再 cp 一遍
docker volume create axiom_postgres_before_pitr
docker run --rm --entrypoint sh -v axiom_postgres:/from -v axiom_postgres_before_pitr:/to postgres:18.6-alpine \
  -c 'cp -a /from/. /to/'
docker run --rm --entrypoint sh -v axiom_postgres:/from -v axiom_postgres_pitr:/to postgres:18.6-alpine \
  -c 'rm -rf /from/* /from/.[!.]*; cp -a /to/. /from/'
docker compose -f deploy/compose.yaml --env-file deploy/.env up --detach --wait postgres
```

换卷后正常入口会以提升后的 timeline 继续写同一个 stanza（system identifier 没变）。核对程序能起来、`pgbackrest check` 通过后，
再决定旧卷和旧 WAL 的去留。这个换卷动作是手工的，测试只执行到「恢复实例的行数核对」；第一次真正做 PITR 前，在演练环境按本节
走一遍换卷。

### 和逻辑 set 的关系

逻辑恢复（`scripts/ops/restore.sh`）会 initdb 出一个新的 system identifier；旧的 WAL 仓库属于已经丢掉的集群，不能直接接上。
恢复脚本把 `AXIOM_BACKUP_WAL_BUCKET` 置空并写进 set 的 `deploy/.env`，所以不只是第一次启动，后续每次启动都关归档；逻辑 set 的
`AXIOM_BACKUP_BUCKET` 保留，恢复作业和以后的逻辑备份仍读同一个桶。确认要长期保留旧仓库时，给新集群设一个新的
`AXIOM_BACKUP_WAL_BUCKET`（通常还是备份桶）和 `AXIOM_BACKUP_WAL_PREFIX`（例如 `<前缀>-pgbackrest-restored`）再 `stanza-create`，
不要拿旧 stanza 名硬接新集群。演练（`just backup-rehearsal`）的恢复也走这条线：脚本把初始集群的 WAL 桶置空（它还没有
stanza），并在恢复后断言安装文件里出现了空的 `AXIOM_BACKUP_WAL_BUCKET=`；WAL 这条路由 `internal/platform/backup` 的容器测试守。

## 主密钥轮换

命令使用 `AXIOM_DATABASE_URL`（应用角色）和 `AXIOM_MASTER_KEY_FILE`。数据库迁移须包含 platform `00022`。
只有负责人接触密钥文件；以服务账户执行，目录仅允许该账户和负责人访问，文件 0600 或 0400，Windows 用目录和文件 ACL。
命令只打印版本号、操作结果和每批数量，不打印密钥、摘要或文件路径。不要用 shell 输出文件来核对。

轮换在线进行：节点在每次心跳里报告自己已与数据库摘要核对通过的版本和当前写版本（`platform.nodes.key_versions`、`key_active`），
运行中的进程按心跳周期重新加载同一份文件。新版本只进入「可读」，所有在线节点都报告它之后才自动切换写版本；
旧版本在所有信封都重包裹完、且所有在线节点已经写出新版本超过一个心跳窗口后自动退役。全程不用停 `serve`。

1. 先做一个完整备份 set，并暂停备份调度直到轮换完成。自动退役可能在几分钟内删掉旧版本，备份若跨过
   数据库转储和密钥归档两个时刻就可能缺版本；备份脚本从转储本身读出版本清单（和转储是同一个快照）并核对密钥归档，
   缺一个就让这种 set 失败重跑。不要用备份前后两次查询代替转储里的清单：版本可能在两次查询之间新增并退役。
   保留旧 set 配套的 `keys.tar.age`：零在线引用并不等于历史备份不再需要旧密钥。还要为 PITR 窗口保留每次轮换前的完整
   `master` 文件：PITR 只恢复数据库，不恢复 `keys` 卷，目标时间在轮换前时恢复库引用的版本可能已从当前文件退役删除。
   恢复旧 set 时必须用该 set 的数据库和密钥环一起恢复，不能搭配已经退役并删去旧版本的当前文件。
2. 在密钥目录中运行 `axiom keyring add --version 2`（用未使用过的正版本号）。
   它原子替换本机 `master` 文件，生成新随机材料，登记为可读；数据库写版本保持不变。
   文件父目录须可写，不能对只读挂载或单文件 bind mount 直接执行；可以在受保护目录中的副本上操作，再替换部署卷里的文件。
   在隔离目录操作时，先把现有完整 `master` 文件复制过去，不能拿空文件开始轮换。
3. **分发同一份完整文件**到所有节点（包括离线备用和将来要恢复的节点）的 `AXIOM_MASTER_KEY_FILE`，不要在各节点分别生成同版本的密钥。
   可以在受保护目录里改好再替换部署卷里的文件。对每个实际部署文件运行 `axiom keyring check`；它检查全部可读版本、写版本、
   数据库引用和材料摘要。文件替换是原子的，运行中的节点下一次心跳就会读到新版本，不需要重启。
4. 等待。在线节点在下一轮心跳后报告版本 2；所有在线节点都报告后，其中一个节点把写版本切到 2 并记审计。
   之后后台按批重包裹，每批 100 个信封、每轮一步；旧版本在所有引用消失、且所有在线节点写出 2 超过 30 秒后退役。
   用 `axiom keyring check` 确认最终状态：`active=2`，`readable` 只剩 2，`retired` 含 1，文件里不再有 1。
   节点日志会输出 `keyring: activated a distributed version`、`rewrapped envelopes` 和
   `retired an unreferenced version`，只带版本号和数量。
5. 恢复备份调度并做新的完整 set。保留轮换前的加密备份直到其正常过期。
   审计 `keyring.initialize/add/activate/rewrap/retire` 在 `platform.audit_log`，只含版本、表名与数量。

自动步骤也可以用命令手动完成；命令与在线循环使用同一把生命周期锁，可以同时运行，但手动命令不是必需：
`axiom keyring activate --version 2` 仍要求全部在线节点报告版本 2；`axiom keyring rewrap [--batch-size 100] [--max-batches N]`
分批重包裹；`axiom keyring retire --version 1` 仍拒绝写版本、仍被引用的版本和仍在使用旧写版本的在线节点。
命令适合停掉全部 `serve` 后的维护，或自动循环被卡住时排查。在线循环只向更高的可读版本切换，所以手工回退写版本
（激活后、退役前重新激活 1）必须先停掉全部 `serve`，否则下一轮又会把 2 激活；回退并重包裹完成后停掉的节点再启动。

`retire` 提交墓碑后才删本机文件；进程在两者之间退出会留下「已退役但仍在本机文件」的状态，启动、`check` 和在线节点在确认没有信封引用后会先删掉本机行再重载，也可以重跑 `retire`。删除文件失败时重复此命令，不能复用旧版本号或旧材料。
**退役是单向门：** 此后任何仍引用旧版本的行都读不出，也不能用 `add` 把同一材料换一个新版本号登记（指纹墓碑拒绝）。
退役后只能靠恢复整套旧备份找回这类行。
启动时缺少全部可读版本、数据库引用的版本或摘要不符，进程拒绝启动，错误只报版本号；文件里仍有已退役版本但确认没有信封引用时，启动会清掉它，有引用则报错并保留密钥，说明墓碑与存储的数据不一致。数据库和文件的联合状态由 `check` 核对通过后才能启动。

登录键（`axiom kv login v1`）和邮件键在**进程启动**时从当时文件里的最高版本派生，不随热加载改变，避免同一个进程内登录键漂移；
轮换完成后陆续重启的节点会改用新版本派生，混合运行期间登录尝试的计数桶可能按版本分开。这只影响限流分桶，不影响认证；
需要完全一致时在轮换完成后滚动重启全部节点。

重包裹只更新[平台信封清单](../../internal/platform/README.md#凭证加密)中各存储的包裹 DEK 与主密钥版本，不改数据密文、nonce、所属业务对象的状态或修订；Webhook 签名密钥也在该清单内，其签名材料和轮换重叠期限不变。
新增存储信封的表必须加入 `cmd/axiom` 的清单；启动和命令遇到未登记的 `wrapped_dek` 列会拒绝继续，避免漏迁后退役。
API key 哈希用的 HMAC `hash` 和派生用的 `identity` 文件不在此命令范围，不能把它们当成 `master` 操作。

## 恢复

恢复只支持**空白环境**：没有数据库、没有密钥环。这样 initdb 才会用 set 里的口令建集群、建两个登录角色
（`deploy/postgres/roles.sh`），程序和库的口令自然一致。就地覆盖一台正在运行的库不在支持范围里（口令、角色和正在进行
的写入都不是 set 能决定的）。

前提：

1. `deploy/.env` 至少有桶的设置（`AXIOM_BACKUP_BUCKET`、`AXIOM_BACKUP_S3_*`，前缀不是 `axiom` 时还有
   `AXIOM_BACKUP_PREFIX`）。三个口令可以随便填占位值：恢复的第 1 步会用 set 的副本把整个文件换掉。恢复脚本在口令缺失时
   自己也会补占位值好让 compose 能解析，但下面显式执行的运维镜像 `build` 不会，所以先填上更省事。
2. 镜像要在这台机器上，并且与 set 的版本一致（第 1 步打印的 `manifest.txt` 里有 `app_image`）。比 set 新的镜像会先跑完自己
   的迁移，多出来的表、列和约束在 set 里没有，随后的恢复会失败（第 4 步先把迁移建的对象整体删掉，正是为了让它们不参与
   冲突，但新迁移写进 `platform.goose_*` 的版本行也在转储之外）。升级放在恢复之后做。按发布记录选择 set 的 digest，配置 `DEPLOY_IMAGE` 后拉取，程序不重新构建；数据库镜像和独立的运维镜像从本仓库构建：

   ```bash
   docker compose -f deploy/compose.yaml --env-file deploy/.env pull axiom migrate keys valkey
   # The database image is built from this tree (pgBackRest on the pinned PostgreSQL tag), not pulled as a release
   # artifact; the restore script's image check needs it before step 2.
   docker compose -f deploy/compose.yaml --env-file deploy/.env build postgres
   docker compose -f deploy/compose.yaml --env-file deploy/.env --profile ops build backup restore
   ```

   运维镜像用程序镜像作构建阶段（`FROM ${PROGRAM_IMAGE} AS program`），最终镜像基于 `postgres:18.6-alpine`，只把
   `/axiom` 取进 `/usr/local/bin`，所以上面的 `pull axiom` 必须在前。数据库镜像基于同一个 `postgres:18.6-alpine`，加
   pgBackRest 和入口脚本，不含程序二进制。`just backup-rehearsal` 会先构建程序镜像、ops 镜像和数据库镜像；`just up` 构建
   程序和数据库，不构建 ops profile，恢复前按上面的 `--profile ops build` 构建。恢复脚本开头用 `docker image inspect` 逐个
   检查镜像在不在（包括数据库镜像），不在就在第 1 步之前报错。
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
2. 用这份配置启动 `postgres`：空卷 → initdb → `deploy/postgres/roles.sh` 建 `axiom_app` 与 `axiom_migrator`。脚本也会
   删掉 Valkey 和 `keys` 容器：恢复后的 `/keys` 没有 `valkey-password`，重跑 `keys` 会换掉缓存里的旧 ACL 并生成新口令；
   复用已退出的 `keys` 容器则会让 Valkey 拿着旧 ACL 启动，程序报 `WRONGPASS`。缓存卷本身只放限流和会话状态，不删。
3. `axiom migrate`：`axiom_owner`、`axiom_lookup` 和各模块的 schema 属于迁移，由程序建。转储里每个对象的属主都是这两个
   `NOLOGIN` 角色，所以恢复前它们必须存在；角色定义只有一处（迁移），脚本不抄第二份。
4. 取 `db.dump.age` 解开后先删掉迁移建的那些 schema（属主是 `axiom_owner` 的全部，`public` 不在其中），再
   `pg_restore --single-transaction`：对象、属主、授权都来自 set。删而不是 `--clean`，是因为分区表的分区继承父表的约束，
   单独删一个继承来的约束会被拒绝。取 `keys.tar.age` 解开进密钥卷，属主 65532、权限 0400（程序启动时检查）。
5. 再 `axiom migrate` 一次：set 比镜像旧时补上新的迁移，一致时是空操作。
6. `compose up --wait` 启动程序，等它自己的启动检查通过，再验一次请求：文件组合里有入口时，经入口按各自域名打控制台和模型
   API（控制台域名上 `/api/console/v1/site` 是 200，API 域名上同一路径是 404，`/v1/models` 无 key 是 401），没有入口时才
   打程序自己发布的两个回环端口。**只查回环端口是不够的**：入口连不上程序时那里照样 200，而外部请求全是 502。

整个恢复过程固定使用启动时那一份桶的位置和凭证：脚本先问恢复容器自己解析出的 `AXIOM_BACKUP_*`、`AXIOM_OBJECTSTORE_*`
是什么，再把 `AXIOM_BACKUP_BUCKET`、`AXIOM_BACKUP_PREFIX` 和 `AXIOM_BACKUP_S3_*` 导出。compose 用它们重新算出 `ops`
服务的 `AXIOM_OBJECTSTORE_*`，而第 1 步装进来的 set 副本改不了它们。不固定的话，set 里的 `deploy/.env` 记的是备份那天的
位置和凭证，可能已经轮换或搬走，第 4 步就会拿旧凭证去取转储——而那时库已经被换掉了。不能直接导出
`AXIOM_OBJECTSTORE_*`：那会同时改掉第 6 步程序的桶。放环境里而不是当参数传，是因为参数会留在进程列表里，而它们是凭证。

恢复完要做的：

- 用 `--overwrite` 重跑一次之前失败的恢复时，脚本会先删掉这个项目的库卷和密钥卷，再从头来一遍（它只删用这两个卷的容器，
  同项目里别的容器——演练的 S3——留着，因为 set 还在那里）。
- 恢复之后核对 `manifest.txt` 里的镜像引用与本机部署的 digest 是否一致；不一致的话，先让程序在第 5 步的迁移上跑通再对外。
- 换掉泄露的口令与桶凭证后，重新做一次备份：set 里的 `deploy/.env` 是旧的。

## 恢复演练

`scripts/ops/rehearse.sh`（`just backup-rehearsal`）把上面整条路走一遍，并且不碰任何真实部署：

- 自己的 compose 项目 `axiom-rehearsal`、自己的 `deploy/.env.rehearsal`、内核分配的宿主机端口（程序的和入口的都是）；
- 先用两种 `AXIOM_EDGE_TLS_ISSUER` 各解析一次 `deploy/ingress/Caddyfile`（`caddy validate`）：`internal`，和变量缺失时 compose 填进去的那个值。
  演练自己跑 `internal`，所以另一个取值——有公网证书的部署用的那个——只能这样验到；
- 开跑先把继承来的 `AXIOM_*`、`DEPLOY_*` 和 `COMPOSE_FILE` 一类变量清掉：compose 的插值优先取 Shell 环境，
  操作者导出过的生产桶名和凭证会覆盖 `--env-file`，第一次备份就写进真桶、再按演练的保留策略删掉真实 set；
- 本地 S3 容器（`deploy/compose.rehearsal.yaml` 里的 RustFS）代替桶，set 随栈一起销毁；
- 上传、删除和建桶（备份、第 10 步的搬迁，以及第 4 步的 `create`）先在容器里断言 `AXIOM_BACKUP_BUCKET`、`AXIOM_BACKUP_PREFIX`、
  `AXIOM_OBJECTSTORE_BUCKET`、`AXIOM_OBJECTSTORE_ENDPOINT`、`AXIOM_OBJECTSTORE_ACCESS_KEY_ID` 确实是演练 S3 和它的专用桶、
  前缀，不是就退出；
- 种一个站点、一个用户和一条非默认的价格政策，停程序，记录全库内容指纹（含每张表的行数）、角色与成员关系、密钥环哈希、`deploy/.env`；种数验证
  按名断言 `accounts.sites` 与 `accounts.users` 各有行——空表也有一行指纹，只数"有指纹的表"证明不了种子发生过；
- 备份；验证保留为 0（份数和天数各一次）在写任何东西之前退出 1 且桶不变；验证持锁时第二个运行退出 75；验证密钥环用库的
  身份解不开、用密钥环的身份解得开；
- 删掉库卷和密钥卷（S3 容器留着，set 在它这里），删掉 `deploy/.env` 里的一行标记，再走一遍上面的恢复脚本，文件组合带上
  `deploy/compose.edge.yaml`；
- 比对表清单与指纹（下列行除外，见下）、角色、密钥环、环境文件，并经入口各打一次控制台域名和 API 域名；
- 连续再备份两次，验证保留只留下最新两份、最早那份被删；
- 轮换 S3 凭证（重建演练的 S3 容器，卷不动）并把整个前缀搬到 `axiom-moved`、把 `AXIOM_BACKUP_PREFIX` 改过去，再恢复一次：
  这一遍只有"固定使用启动时的位置和凭证"才能走完，因为 set 里的配置写的是搬走之前的位置和已吊销的凭证；
- 打印一行可直接粘到下表的记录，含 RTO（从执行恢复脚本到 `compose up --wait` 返回）。

指纹比较按行内容，跳过会被程序自己改动的行：River 的队列表（库自己的迁移版本由程序的启动检查核对）、
`billing.holders`（每次核心进程启动插一行，恢复后的库里既有 set 里的那行也有新的）、`billing.expiry_cursor`（到期任务每分钟
更新自己扫到哪）、`platform.alert_signals`（启动产生的新告警）、`platform.credential_share_departures`（新进程清理 set 里过期的
节点行）和 `catalog.sync_state`（目录同步的进度）。`billing.price_policies` 不整表跳过：只有发布任务每次改写的 `confirmed_at`
列不进哈希，`rev` 和三个乘数仍然要逐行一致。表本身少一张就失败，所以跳过的是行，不是表。

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
| 2026-10-30 11:00 | 20261030105920-5d0746 | 29s | 数据库、角色、密钥环、配置一致，入口两个域名应答正确 | 通过 |
| 2026-10-30 11:08 | 20261030110649-57e259 | 32s | 数据库、角色、密钥环、配置一致，入口两个域名应答正确 | 通过 |
| 2026-10-30 11:13 | 20261030111116-4e91d2 | 38s | 数据库、角色、密钥环、配置一致，入口两个域名应答正确 | 通过 |
| 2026-10-30 11:37 | 20261030113558-676233 | 33s | 数据库、角色、密钥环、配置一致，入口两个域名应答正确 | 通过 |
| 2026-10-30 11:47 | 20261030114440-bb2802 | 91s | 数据库、角色、密钥环、配置一致，入口两个域名应答正确 | 通过 |
| 2026-10-30 12:45 | 20261030124404-0967c4 | 27s | 数据库、角色、密钥环、配置一致，入口两个域名应答正确 | 通过 |
| 2026-10-31 08:50 | 20261031084908-c274fc | 29s | 数据库、角色、密钥环、配置一致，入口两个域名应答正确 | 通过 |

## 生产入口

`deploy/compose.edge.yaml` 在程序前面加一个 Caddy 容器，配置是 `deploy/ingress/Caddyfile`。
生产先按[发布的 digest 选择与核对](release.md#选择和核对)确定交付物，在 `deploy/.env` 写入
`DEPLOY_IMAGE=<仓库>@sha256:<摘要>`；程序镜像不使用 tag、不在部署机重建，也不运行开发用的 `just up`。数据库镜像
不是发布产物，由同一个入口从本仓库构建（见下）。

部署只经一个失败即停的入口。它从这次 `-f`/`--env-file` 解析出的**实际** Compose 配置里读 `axiom` 和 `migrate`
会用的镜像（Shell 已导出的 `DEPLOY_IMAGE` 优先于 `.env`，所以被验证的就是会被拉取的），要求两者是同一个 digest，
用[发布的「镜像签名与验证」](release.md#镜像签名与验证)核对签名、workflow 身份、digest 和 SBOM，再把该 digest
固定给同一个脚本里的 pull 和 up：

```bash
tools="$(bash scripts/ci/install-release-tools.sh)"
PATH="$tools:$PATH" bash scripts/ops/deploy.sh \
  -f deploy/compose.yaml -f deploy/compose.edge.yaml --env-file deploy/.env tmp/release/release.json
```

入口按顺序拉取该 digest、发布本构建的前端资源、从本仓库构建数据库镜像、启动数据库、重放 `deploy/postgres/roles.sh`
的角色授权、再以 `--no-build` 启动整套栈。任何一步失败即停：验签失败时没有 pull，也没有启动。不要把它拆成手工的
`sed`、验签、`pull`、`up`：只从 `deploy/.env` 取镜像会验文件里的 A 而 Compose 拉环境变量里的 B，分成几段执行也没有
失败控制。三份数据库口令须已在 `deploy/.env` 中，否则启动数据库或重放角色授权会失败并停下。

**密钥环只在第一次启动时生成。** `keys` 服务只在 `keys` 卷里 `master`、`hash`、`identity` 三个文件都没有、且
`AXIOM_KEYRING_GENERATE=true` 时一起生成整套；卷里只有其中一部分时，不论这个值，它报出有哪几个、缺哪几个后退出 1，
程序不会启动。这个值不是 `true` 或 `false`（例如 `yes`、`False`、`0`）也一样退出。生产部署第一次启动成功后，把
`deploy/.env` 里的它改成 `false` 并一直保持：之后卷丢了或换了机器，`keys` 拒绝空卷，要按[恢复](#恢复)把同一个 set 里的
整套密钥环放回。缺文件时不要手工补生成，也不要从别处拷单个文件：新的哈希密钥让库里所有 API key 的哈希都对不上，节点
却照常启动。

迁移和程序使用同一 digest；备份 manifest 的镜像字段也因此记录这个引用。恢复和回滚沿用发布记录里的摘要，不能将
本地开发的 `axiom:local` 当生产交付物。数据库镜像不是发布产物：它的 Dockerfile 钉住 PostgreSQL 的基础 tag，pgBackRest 和
三个入口脚本随本仓库走，所以 `scripts/ops/deploy.sh` 在启动数据库前先 `build postgres`，再用 `--no-build` 启动；升级它的
内容和升级程序是两件事，升级后跑一次 `pgbackrest check` 和 `deploy/postgres` 的恢复测试。

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
- **来源限制**：`/api/admin/v1/*` 在控制台域名和 API 域名上都一律 404；管理应用只在 `AXIOM_EDGE_ADMIN_HOST` 上回答，来源由程序的
  管理监听按 `AXIOM_ADMIN_SOURCES`（逗号分隔，默认只有本机，忘了配就是把入口关掉）判断。Caddy 只做 TLS 和转发，不重复判一次；
  地址从 Caddy 重写的 `X-Forwarded-For` 读，因为 `AXIOM_TRUSTED_PROXIES` 已把入口地址设为可信代理。以前配 `AXIOM_EDGE_ADMIN_ALLOW_CIDRS`（空格分隔，Caddy 读）的部署要把同一组来源改配成 `AXIOM_ADMIN_SOURCES`（逗号分隔，程序读）；旧变量不再生效，留空则只有本机能进管理后台。`/console/*` 与
  `/api/console/v1/*` 对 `AXIOM_EDGE_CONSOLE_ALLOW_CIDRS`（空格分隔，Caddy 读）开放，默认所有人：它是用户的控制台，私有部署
  可以把它关进内网。API 域名不设来源限制：它的调用方本来就是任意的机器。
- 三个域名都要有指向这台机器的 DNS。
- 前面再放 CDN 的话，给 `servers > trusted_proxies` 加 CDN 的网段、开 `trusted_proxies_strict`，并把 443 只对 CDN 开放，
  否则客户端可以自带 `X-Forwarded-For`。

## 决策与代价

- **逻辑 set 和 WAL 归档各管一件事**：逻辑 set 是跨机器的灾难恢复（角色、schema、密钥环、配置），WAL 归档加基础备份
  是同一台机器上的时间点恢复，回到误删之前。不要拿 WAL 归档代替逻辑 set：它依赖同一个 PostgreSQL 大版本、同一份
  pgBackRest 仓库和口令，仓库丢了就一起丢。代价是两套作业、一个额外口令和一次恢复演练；换机器仍走逻辑恢复，WAL 恢复的
  RTO 取决于基础备份和要重放多少 WAL。
- **pgBackRest 2.58.0，不是 wal-g 3.0.9**：一个程序做基础备份、WAL 归档、恢复、保留和 `check`，仓库整体加密
  （AES-256-CBC，连 WAL 一起）、校验和、S3 兼容端点和 `bundle` 都是仓库配置的一部分；wal-g 也成熟，但加密和保留靠环境
  变量与 `delete` 子命令另配，默认不加密整个仓库，而且 Alpine 3.24 没有 wal-g 包（pgBackRest 有
  `pgbackrest=2.58.0-r0`，基础镜像直接 `apk add`；装 wal-g 要从 GitHub 取第二个二进制）。pgBackRest 2.58.0 是第一个支持
  `http://` S3 端点的版本（本地 SeaweedFS/RustFS 需要；2026-01-19 发布），上游最新是 2.60.0（2026-10-18），Alpine 3.24
  打包的是 2.58.0；wal-g 最新 v3.0.9（2026-08-20）。文档：pgBackRest 用户指南
  https://pgbackrest.org/user-guide.html、版本记录 https://pgbackrest.org/release.html；wal-g
  https://github.com/wal-g/wal-g/releases（均 2026-10-31 读取）。代价：数据库镜像从本仓库构建，不像程序镜像那样按
  digest 拉取；升级 pgBackRest 要重建镜像并跑 `check` 和 `deploy/postgres` 的恢复测试。按[负责人授权](https://github.com/TYCT-tyct/Axiom/issues/309#issuecomment-6478816797)
  「遇到问题自己解决问题和判断最优而不是问我。」落地，独立评审仍执行。
- **用程序自己的对象存储客户端，不再装 rclone 或 aws-cli**：备份的上传、列举、删除和恢复的下载都调 `axiom objectstore`，
  与程序的归档共用一份 SigV4、期限、重试和 multipart；运维镜像从发布镜像取同一个二进制，不另装 S3 实现。代价：运维镜像
  因此多带一份发布二进制（几十 MB，也含 HTTP 服务面，但它不监听端口），且必须和程序镜像同版本构建或发布；桶要支持客户端
  用到的子集（桶 HEAD/PUT、对象 PUT/GET/HEAD/DELETE、ListObjectsV2、multipart），换 S3 实现时要跑演练。备份的桶和凭证仍然与程序分开，
  见「与程序的对象存储分开」。按[负责人授权](https://github.com/TYCT-tyct/Axiom/issues/309#issuecomment-6478816797)
  「遇到问题自己解决问题和判断最优而不是问我。」落地，独立评审仍执行。
- **程序桶只放调用记录的归档，不是备份的一部分**：归档对象和备份 set 用两个桶、两组凭证，备份不包含归档，恢复一台
  机器也不依赖它。归档对象的丢失只丢调用明细，账本、汇总和审计各有自己的备份。热窗口是管理数据，由管理员在管理接口
  设置并留审计；窗口为 0 或没有程序桶时全部留在 PostgreSQL，代价是库会随日志增长。
- **age，不是 gpg**：只认公钥、没有信任网、两个收件人就是两行；gpg 的信任、过期和子密钥在"备份机器只放公钥"这件事上
  没有收益。
- **角色不单独备份**：`axiom_app`、`axiom_migrator` 由 `deploy/postgres/roles.sh` 建，`axiom_owner`、`axiom_lookup` 由
  迁移建；恢复时先跑迁移（角色与 schema 由它建，这一步结束再整体删掉 schema），角色定义只有一份，口令在 `deploy/.env`
  里随库一起备份。
- **仓库里的文件不进 set**：`deploy/compose.yaml`、`deploy/compose.edge.yaml`、`deploy/ingress/Caddyfile` 都在 git 里，
  manifest 记下镜像引用（生产为 digest）就够了；只有不是代码的 `deploy/.env` 需要备份。恢复因此要显式拿到这套文件（`-f`）：set 里没有它们，
  而缺了入口那一份，程序回来时就没有入口需要的网络和代理地址。
- **入口分两个公开域名**：`accounts.site_domains` 的域名是主键，一个域名只属于一种 surface，所以控制台与模型 API 只能各用
  一个域名。代价是每个部署要三份 DNS 与证书，注册站点时也要三个 host；换来的是每种 surface 的来源限制能分开设，程序自己
  的拒绝逻辑不必改动。

- **部分密钥环由 `keys` 服务拒绝，不另给哈希密钥记库里的摘要**：缺一个文件就补生成，正是 #735 发现的问题——新的
  哈希密钥让已有 API key 全部失效，节点却照常启动，因为只有主密钥在库里有摘要可以核对。三个文件只能一起生成、缺一个
  就停，在产生坏状态的那一步关掉它；整套被重新生成时，新主密钥与库里的摘要不符（[主密钥轮换](#主密钥轮换)），`serve`
  拒绝启动。手工换掉 `hash` 文件仍然查不出来，要查得给哈希密钥也在库里记一份摘要，是数据结构的改动，不在这次的范围。
  代价：以后新增第四个密钥文件的版本，已有部署的卷里会缺这个文件而被拒绝，那个版本要自带一步单独补它的流程。

## 待定

- 定时归宿主（cron / systemd timer），镜像里没有调度器。要变成多机部署时，"同时只跑一个"得换成桶或数据库上的协调，锁的
  范围会失效。
- 基础备份的间隔和 `repo1-retention-full=7` 是部署方的选择：测试库只有 30 MB，每天全量、留 7 份的代价要按真实库的大小和
  写入量重算；改成每周全量，PITR 窗口就是 7 周。没有做增量/差异备份，先把全量的恢复路径走通。
- `AXIOM_BACKUP_CIPHER_PASS` 没有自动托管：它要和 age 私钥一样离线保存，丢了 WAL 仓库读不出来。换口令要另建仓库，轮换
  步骤还没有写；在写出来之前不要改一个已有仓库的口令。
- PITR 和源库在同一台机器上：恢复期间源库的 `archive-push` 会在共享锁上排队，WAL 在 `pg_wal` 里堆积；大库先停写或换一台
  机器恢复。没有自动切主，也没有热备；HA 和它的花费不在本任务里，负责人定。
- 换卷和提升后的接管是手工步骤，测试只覆盖到恢复实例的行数核对；第一次真正做 PITR 前，在演练环境按「时间点恢复」走一遍，
  把实测的 RTO 和问题记下来。
- 演练还没有进 CI 常跑：要 Docker、要建镜像、约十五分钟，而且自带破坏性。要不要作为发布前的必跑项，等 CI 机器的余量定下来
  再定。
- 公网证书这条路没有在任何地方验过：演练用 `.example` 域名和 Caddy 自己的 CA，没有公网 DNS 也就没有 ACME。第一次真实部署时
  才第一次走 HTTP-01，失败原因看 `docker compose logs caddy`。
- 演练用的桶是 `rustfs/rustfs`，一个钉住版本的第三方 S3 实现。MinIO 自己已经拿不到公开镜像（Docker Hub 的仓库没了，
  quay.io 返回 401），而 `adobe/s3mock` 之类的替身并不真存对象，测不出保留和重取。桶的语义只用到对象读写、列举和
  multipart，换成别的实现时改 `deploy/compose.rehearsal.yaml` 的一个服务就够，但要重跑演练确认客户端仍兼容。
