# 发布前

发布前逐项确认，本文记录固定交付物和模型目录检查；其余按 project-workflow 的发布方法。部署到服务器要先问负责人（[CONTRIBUTING](../../CONTRIBUTING.md)），本文的写库步骤改的是线上目录，同样先问。

## 账户速率限额上线前核对

首次上线 E4-2/P13 时，默认 6000 次/窗口从原有每 key 节点保护扩展到**按账户聚合**的业务额度（调用与 count_tokens 各一池，默认窗口一分钟）。已有账户若 4 把 key 各 2000 rpm，升级后会收到 account_rate 429。账户/等级覆盖值未设置时自动继承此默认值；迁移不会自动识别高流量账户。

发布执行者须在获准访问生产后完成以下核对，并把时间范围、账户峰值及覆盖值清单写入发布记录；本地测试不替代生产核对，未取得完整数据不得标成通过：

1. 从能识别已认证账户的入口请求记录，分别汇总推理与 count_tokens 在配置 RateWindow 内的峰值，覆盖代表性业务高峰并评估突发余量；计入业务 key 拒绝，排除超过节点保护的尝试。只有计费/完成调用记录不足以证明安全，它遗漏 count_tokens、未预留及失败的请求；没有完整入口数据时先补观测，暂缓默认限额上线。
2. 对达到或超过默认值、或需要余量的现有账户，先通过后台 `PUT /api/admin/v1/rate-limits/account/{id}` 配置经确认的 rate_requests，并读取核对，保留 MFA、幂等键、理由与审计。需要保持不设账户业务上限时可明确设 0，仍受节点/key 限制；不要以 null 代替 0，null 会重新继承等级或 6000。旧节点不提供该接口时，须在放流量前通过隔离的新版本管理入口预设，核对完成后再切流量。
3. 切换后核对 `axiom_gateway_rate_limited_total{reason="account_rate"}` 与 reason="business_key_rate" 的增量、受影响账户的入口记录和 429 头。按账户的识别靠访问记录，不把账户 ID 加入低基数指标；异常时调整已核对的后台覆盖值或按下节回滚镜像，保留兼容的数据库列。

## 注册同意上线前核对

注册同意（[账户与租户](../domains/accounts.md#登录与会话)）要求所有控制台操作先检查当前条款版本。旧节点没有这段查询和守卫，迁移又只新增表，所以混合版本期间发布文档会让同一个用户随负载均衡结果被放行或拒绝。

1. 先升级全部服务节点和页面，确认每个节点都返回 `GET /api/console/v1/legal` 和 `getSession.consent`；旧页面没有阅读和接受入口，先升级页面再发布。
2. 全部节点和页面就绪后才发布首份文档；发布前站点没有文档，机制不改变行为。
3. 发布后不把流量切回不支持该机制的版本；确需回退时停用站点或保持新版本，不能只换镜像。回退前先确认旧程序仍兼容已经写入的接受记录。

## 固定交付物与 digest

`ci.yml` 的 `deploy` 作业在列车和 `main`（push 运行，或收尾旧版逐个合并的记录时调起的 dispatch）上都运行 `release-check.sh`，所以只在 `main` 上会跑的发布步骤会先在列车里失败、退回弄坏它的 PR；只有 `main` 保留发布交付物，PR 不生成。runner 由 `runner-host.sh` 安装固定版本（当前 v0.38.0）、按发布页 `checksums.txt` 校验 SHA-256 的 buildx CLI 插件，`check` 运行 `docker buildx version`；缺少它时 `docker buildx create --name …` 会落到 `docker --name` 并以 125 退出（2026-10-29 `main` 的 `deploy`）。
`release-check.sh` 构建一次 Linux/amd64 程序和 scratch 镜像，通过同一次 [Buildx 多 exporter 构建](https://docs.docker.com/build/exporters/#multiple-exporters)导出 OCI 归档并写入临时的 loopback
registry（仅绑定 `127.0.0.1`，不上传外部仓库）。它按归档的 manifest digest 拉取镜像，迁移和服务都消费同一个
`repository@sha256:…`，包括旧迁移角色升级后的再次启动；重启不重新构建。其他 CI 作业仍会构建自己的测试程序和前端。

构建完成后，`release-check.sh` 按 digest 用 Sigstore cosign keyless 签名，再用 syft 从同一个 OCI 归档生成 SPDX JSON，并以同一身份作为 `spdxjson` attestation 附在该 digest 上。签名身份是这次运行的 GitHub OIDC 短期证书，即
`https://github.com/<仓库>/.github/workflows/ci.yml@<ref>`；`main` 的发布是 `...@refs/heads/main`，没有保存的私钥，也没有私有透明度日志。列车用列车的 ref 演练同一流程，只有 `main` 保留交付物。

成功的部署作业保留 `release-<源码 SHA>-<run attempt>` Actions artifact，保留 30 天，内含：

- `image.oci.tar`：原始镜像字节；保留和核对用这份，不在部署机重新构建。
- `sbom.spdx.json`：syft 对 `image.oci.tar` 生成的 SPDX 2.3 JSON；同一个文件是镜像 `spdxjson` attestation 的 predicate。
- `signed/`：`cosign save` 的 OCI layout，含镜像、签名和 attestation。签名是 registry 里的独立 artifact，不在 `image.oci.tar` 里；上传时用 `cosign load` 把它连同镜像一起送到外部 registry。
- `release.json`：源码完整提交、实际 Go/Node 工具链、前端构建版本、迁移 SQL 路径及 SHA-256、平台和镜像 manifest digest；
  签名后追加 `signing_identity`、`signing_issuer` 和 `sbom_sha256`。
  前五项同时写进镜像标签：`org.opencontainers.image.revision`、`io.axiom.go-toolchain`、`io.axiom.node-toolchain`、
  `org.opencontainers.image.version`、`io.axiom.migrations`。迁移清单包含仓库内嵌 SQL；River 自带的迁移随源码提交锁定的 Go 模块版本追溯。

### 镜像签名与验证

`scripts/ci/verify-release.sh` 是唯一的验签实现，开发和发布使用同一份代码；生产由[部署入口](backup-restore.md#生产入口)调用它：

```bash
tools="$(bash scripts/ci/install-release-tools.sh)"
PATH="$tools:$PATH" bash scripts/ci/verify-release.sh "$RELEASE_REPOSITORY@$digest" tmp/release/release.json
```

它要求引用是 `repo@sha256:…`，`release.json` 的 `image_digest` 与它相同、`signing_identity` 与本次期望的身份相同；`cosign verify` 只接受该 workflow/ref 用 GitHub OIDC 签出的证书，`cosign verify-attestation --type spdxjson` 还要证明 attestation 的 subject 就是这个 digest。`release.json` 必须与 `sbom.spdx.json` 放在一起：脚本先核对文件哈希等于 `sbom_sha256`（只能发现拷贝损坏），再把本地 SBOM 规范化后与验证成功的 attestation 的 predicate 逐字段比较。记录和 SBOM 都没有签名，换掉文件再改哈希也能互相对上，只有这一步能把随交付物保存的 SBOM 绑到签名上。没有签名、签名来自别的 workflow 或 ref、digest 与记录不一致、没有 SPDX attestation 或 SBOM 不是该 predicate 时，命令退出 1，部署停下，不会 pull 或启动镜像。生产期望的身份固定为 `https://github.com/TYCT-tyct/Axiom/.github/workflows/ci.yml@refs/heads/main`；在 GitHub Actions 内运行时按 `GITHUB_REPOSITORY` 和 `GITHUB_REF` 取当前运行的身份，列车因此能验证自己签的演练镜像。

`install-release-tools.sh` 把 cosign 和 syft 按版本与发布页 SHA-256 装进作业私有的 `RUNNER_TEMP` 目录，不写共享的 home。

`ci.yml` 的 `signing` 作业在列车和 `main` 上签一个一次性镜像，再证明 `verify-release.sh` 接受这个 digest 和它的 SBOM、`cosign save`/`load` 之后仍接受，并拒绝未签名或与记录不一致的 digest、tag、把 A 的签名搬到 B、错误 issuer、别的仓库/workflow/ref（元数据与期望身份一致，由 cosign 拒绝）、subject 指向别的 digest 的 SBOM attestation，以及同时替换 SBOM 和 `sbom_sha256`。它用与发布相同的脚本，所以签名流程坏了会在合并前失败，而不是等到发布。

### 选择和核对

1. 在 Actions 找到目标 `main` 提交的 **整个 ci 工作流成功**的运行（不能只看 deploy 成功），记录 run ID、attempt、源码 SHA；
   下载该运行同 SHA/attempt 的 release artifact。失败运行、PR、列车产物都不能作为发布版本。
2. 在该源码 SHA 的检出中，解压 artifact 到独立目录，例如 `tmp/release`。运行：

   ```bash
   python3 scripts/release_image.py verify tmp/release/release.json tmp/release/image.oci.tar
   jq '{source_commit, go_toolchain, node_toolchain, frontend_version, migrations, platform, image_digest, signing_identity, sbom_sha256}' tmp/release/release.json
   test "$(jq -r .source_commit tmp/release/release.json)" = "$(git rev-parse HEAD)"
   test -f tmp/release/sbom.spdx.json
   test -d tmp/release/signed
   ```

   verifier 检查 manifest、config、每层的长度与 SHA-256，标签与 JSON 一致，平台是 Linux/amd64，输出的 manifest digest
   等于 JSON 的 `image_digest`。digest 是 OCI manifest 的哈希，不是镜像 config ID、tar 文件校验和或 tag。
   同时逐项核对工具链、前端版本和迁移是否符合本次发布与目标库；镜像 digest 不保证数据库回滚兼容。
   `sbom_sha256` 只是交付物内部的损坏检查，和 SBOM 一起被替换仍会通过；上传后由 `verify-release.sh` 把本地 SBOM 与
   已验证 attestation 的 predicate 比较，那才是绑定。
3. **外部 registry 的选择、登录与上传由负责人执行**（花钱/对外动作，CI 不持有上传凭证）。负责人同意发布后，登录该 registry（cosign 读写 Docker 的凭证存储），用保留的签名布局上传同一个 digest；`cosign load` 同时带上镜像、签名和 SBOM attestation，不重建 manifest。registry 要支持 cosign 的签名存储（OCI 1.1 referrers）；不支持时上传或验证会失败，换仓库，不要改成只传镜像：

   ```bash
   # RELEASE_REPOSITORY 是负责人选择的仓库；不要先经过会重建 manifest 的 tag 中转。
   tools="$(bash scripts/ci/install-release-tools.sh)"
   digest="$(jq -r .image_digest tmp/release/release.json)"
   PATH="$tools:$PATH" cosign load --dir tmp/release/signed "$RELEASE_REPOSITORY@$digest"
   PATH="$tools:$PATH" bash scripts/ci/verify-release.sh "$RELEASE_REPOSITORY@$digest" tmp/release/release.json
   ```

   不允许通过重新构建或转换 manifest 来消除 digest 不一致。保留 release JSON、归档、签名、SBOM 及成功运行的引用到发布记录；30 天的
   Actions 保留期不能替代负责人维护的长期归档。provenance 和 digest 核对都不是签名证明；上面的 verify 才是。
4. 在生产 `deploy/.env` 设置 `DEPLOY_IMAGE=<负责人选择的仓库>@sha256:<上一步的 digest>`；迁移和服务共用此值。部署走[生产入口](backup-restore.md#生产入口)的脚本，它在验签通过前不 pull、不启动：

   ```bash
   tools="$(bash scripts/ci/install-release-tools.sh)"
   PATH="$tools:$PATH" bash scripts/ops/deploy.sh \
     -f deploy/compose.yaml -f deploy/compose.edge.yaml --env-file deploy/.env tmp/release/release.json
   ```

   入口从这次 `-f`/`--env-file` 解析出的实际 Compose 配置里读 `axiom` 和 `migrate` 会用的镜像（Shell 已经导出的 `DEPLOY_IMAGE` 优先于 `.env`，所以被验证的就是会被拉取的），要求二者是同一个 digest；`verify-release.sh` 通过后把该 digest 导出给后续命令，再拉取、把本构建的前端资源发布进共享卷、从本仓库构建数据库镜像、启动数据库、重放角色授权并以 `--no-build` 启动整套栈。任何一步失败即停，验签失败时没有 pull 或启动。不要先手工 `sed` 取 `.env` 里的值、验签，再分开执行 `pull`/`up`：那样验的是文件里的 A，Compose 可能拉环境变量里的 B，验签失败后命令也还会继续。启动任何节点前先按[滚动发布](#滚动发布)写下这个构建的前端文件，再逐个替换节点。用 `docker inspect` 核对迁移与服务的 `.Config.Image` 是该 digest 引用，版本 API 与 JSON 的 `frontend_version` 一致。
   回滚选择上次成功发布记录里的 digest，重复核对和部署步骤；已经执行的迁移不会因此自动撤回，先确认旧程序仍兼容当前 schema。本版本之前的构建没有签名，`verify-release.sh` 会拒绝，回滚窗口从第一个按此流程发布的签名版本开始算。
   本版本起 `axiom migrate` 在 `platform.migration_checksums` 记下每个已应用迁移的文件名和 SHA-256，迁移和节点启动都核对：
   已合并的迁移被改过、改名或删掉时两者都停下并报出文件与摘要，改动要走新的迁移；回滚到不认识这张表的旧版本不受影响，它只多看到一张启用行级安全的表（[platform 的「迁移」](../../internal/platform/README.md#迁移)）。

### 决策与理由

选择「CI 一次构建 + OCI 原件 + 本地 registry 按 digest 演练」，而不是「部署机按提交重建」或「直接发布外部 registry」。
重建不能保证工具链和依赖环境一致；外部上传涉及费用与凭证且尚未获准。本地 registry 只用于证明与真实部署相同的 digest
拉取路径；CI 结束删除 registry 和独占 builder，交付物由 Actions artifact 留存。代价是多一个临时 registry 和 BuildKit 容器，
以及负责人发布前的复制步骤；不新增常驻基础设施、不决定生产 registry。

- **keyless，而不是负责人保管私钥**：签名身份由 GitHub OIDC 换成 Fulcio 短期证书，记录 workflow 和 ref；私钥不落地、不用轮换，公开 Rekor 留下可审计的时间点。验证因此依赖 Fulcio、Rekor 和公开日志的可用性，发布也只能由该 workflow/ref 产生。生产私钥或私有透明度日志不是本阶段需要的；以后要离线验证或不让 digest 进公开日志，才换成负责人保管的密钥或私有 Sigstore，届时 `verify-release.sh` 的信任根要一起改。按[负责人授权](https://github.com/TYCT-tyct/Axiom/issues/309#issuecomment-6478816797)「遇到问题自己解决问题和判断最优而不是问我。」落地，独立评审仍执行。
- **SBOM 从交付的 OCI 归档生成，不另构建**：syft 扫描的就是 `release_image.py verify` 检查过的那份字节，SPDX JSON 由同一个 keyless 身份作为 `spdxjson` attestation 附加；验证时既查身份、查 attestation 的 subject digest，还要求交付物里的 `sbom.spdx.json` 与已验证的 predicate 规范化后相同，`sbom_sha256` 只做损坏检查。没有用 Builder 的 `--sbom`：它要额外的扫描器插件，报告会跟 builder 配置走；独立的 syft 由脚本按版本和 SHA-256 钉住。
- **生产部署只经一个入口，镜像从 Compose 解析结果里读**：`scripts/ops/deploy.sh` 用 `docker compose config --format json` 读 `axiom` 和 `migrate` 最终会用的镜像，要求同一个 digest，验签后把 `DEPLOY_IMAGE` 导出给同一脚本里的 pull/up。只从 `.env` 取值会验 A 而拉 B（环境变量优先）；把验签和 pull/up 分成几段命令，验签失败后仍会继续。一个 `set -euo pipefail` 的脚本同时关掉这两个缺口，代价是部署机要有 `jq` 和 `cosign`（后者由 `install-release-tools.sh` 安装）。
- **签名用 `cosign save`/`cosign load` 跟镜像走**：签名和 attestation 是 registry 里的独立 artifact，不装进 `image.oci.tar`；只复制归档会让外部 registry 没有可验证的证明。`cosign save` 把镜像和证明存成一份 OCI layout，`cosign load` 按同一 digest 上传，代价是 artifact 里多一份镜像字节。未选「发布时在外部 registry 重新签一次」：那要在部署机上持有 OIDC，且签名时间点和 CI 记录的源码、工具链分开。
- **生产只认 `refs/heads/main` 的身份**：列车签的是列车 ref，用于在同一流程里演练验证，永不保留；生产默认的 `--certificate-identity` 只有 main，替掉证书或换 ref 都验不过。

## 滚动发布

目标：排空中的节点不再接新流，在途的在同一个期限里结束，边缘不再往它送新请求；新旧构建重叠期间，两套带内容哈希的页面文件都取得到。节点的角色和监听见[总体架构](../architecture.md#部署拓扑)，部署的入口见[生产入口](backup-restore.md#生产入口)。

**一次换一个节点，换之前新节点已经就绪。** 同时停掉所有旧节点会让滚动期间没有节点接流量；核心和入口都要至少留一个就绪的节点。

**被替换的版本必须在新 schema 上启动。** CI 的部署检查在列车和 `main` 上构建被替换的版本（列车的 `BASE`，`main` 落地提交的 `Train` 记录里的基础提交，普通提交的上一提交），用本树的 `axiom migrate` 迁移一个独立数据库，启动旧程序并等它的 `/readyz` 通过（`scripts/ci/old-binary-check.sh`，在 `scripts/ci/deploy-check.sh` 里先于旧迁移角色升级运行）。只回溯一个发布；更旧的版本可能被一次性的单向改动破坏，`00040_fallbacks_no_markup.sql` 就是一次（[billing 的决策与理由](../domains/billing.md#决策与理由)）。当前发布如果再做这种改动，这个检查会失败，是否接受必须作为发布决定显式记录；检查不把任意历史提交当作兼容目标。旧版本的程序在本树里没有前端构建，检查给它一个最小的嵌入前端；页面与 schema 兼容无关。

**排空**（`axiom serve` 收到 SIGTERM 后自己完成，部署不需要发额外信号）：

1. `/readyz` 改成 503，公开、管理和节点监听对之后开始的请求答 503（`Retry-After: 1`、`no-store`）；已经在跑的请求不受影响。从没有就绪过的节点跳过下一步。
2. 等边缘摘掉流量：`AXIOM_SHUTDOWN_TIMEOUT / 3`，最多 10 秒。负载均衡按 `/readyz` 摘节点；入口节点每秒探核心的 `/readyz`，核心排空后入口也报告未就绪。监听还没关，所以负载均衡看到的是明确的 503，不是连接错误。`deploy/ingress/Caddyfile` 只有一个上游、也不按 `/readyz` 摘节点，排空期间客户端会收到程序自己的 503（带 `Retry-After`）；生产的负载均衡要按 `/readyz` 摘节点，有多个入口或核心时新请求才会送到别的节点。
3. 关公开、管理和节点监听；监听、在途调用和它们的结算共用 `AXIOM_SHUTDOWN_TIMEOUT`（默认 30 秒）。期限前 5 秒取消全部上游尝试，最后 5 秒结算；到期还没结束的连接被关闭，没结算的交给清理。监听和调用同时等这个期限，不各自计时，最坏情况因此是 30 秒而不是两段之和。调用这一侧只等一个计数：请求从过 gate 起、到它交给后台的结算写完为止都算在内，计数归零或到期限才停止重试、取消结算（[gateway 的「优雅退出」](../../internal/gateway/README.md#计量追加与结算)）。
4. 健康循环、任务队列和调用记录的写入各自在自己的期限内收尾（健康最多 55 + 10 秒，队列和记录各 30 秒）；缓存退出最多 10 秒。`stop_grace_period` 给 170 秒：边缘 10 + 排空 30 + 健康 65 + 队列与记录 30 + 缓存 10，留余量。

**新旧构建的页面文件**：每个构建在启动任何节点之前，把它带内容哈希的文件（`assets/` 下、名字带 8 位哈希的）写进共享目录，旧构建的文件保留。节点启动时 `AXIOM_WEB_ASSETS_DIR` 指向该目录：自己构建里有的文件优先，缺的才从目录读，所以旧页面请求的旧代码块落到新节点上也取得到。入口页和内容安全策略永远用节点自己嵌入的那份（`no-store`），共享目录里的同名入口页不会被读；不是带哈希的名字也不会从目录读。文件只增不改：同一个带哈希的名字出现不同内容时，命令失败，先查清谁写坏了，不要覆盖。发布用不覆盖的创建，两个进程同时写同一个名字也只有一个成功。

生产入口在 pull 之后、启动服务之前已经执行这一步；卷不共享的多机部署仍要在每台机器上分别执行。手动补发或单独换一个节点时，在仓库根目录运行（`-f` 指向实际的 Compose 文件；`--no-deps` 不启动数据库，卷第一次由 Docker 以 root 创建，所以这一步用 root；节点仍以 65532 只读）：

```bash
docker compose -f deploy/compose.yaml --env-file deploy/.env run --rm --no-deps --user 0:0 axiom web-assets /assets
```

生产部署如果还加了 `-f deploy/compose.edge.yaml` 或用 `-p` 改了项目名，这条命令要带上同一组 `-f` 和 `-p`：卷名由项目名决定，参数不同会把文件写进另一个卷。`deploy/compose.yaml` 已经把 `web_assets` 卷挂在 `/assets` 并设 `AXIOM_WEB_ASSETS_DIR=/assets`。多机部署要让这个卷在各节点间共享，或者在每台机器上各发布一次；只挂本机卷的节点看不到别的机器发布的文件。回滚到上一个 digest 时它的文件通常已经在卷里；回滚到卷里没有的构建，旧页面缺失的代码块仍会触发一次自动刷新（[web 的规则](../../web/README.md#规则)）。不要在发布之间清空这个卷：重叠期间和回滚都要用它，保留多久由负责人按回滚窗口决定。没有共享目录的节点（例如本地 `just up`）只服务自己嵌入的构建，行为与以前相同。

### 决策与理由

- **排空用一个共享期限，且先摘流量再关监听。** 监听和调用各自计时会把最坏情况变成两段之和，部署的 `stop_grace_period` 和边缘摘流量的时间就对不上；一个期限让最坏情况可算，期限前 5 秒取消尝试也给结算留了固定的尾。先置未就绪、等一个探测周期，是因为监听一关，负载均衡只能看到连接错误，客户端要等自己的超时才知道换节点；503 带 `Retry-After` 让它在一次探测内换到别的节点。不选「收到信号立即关监听」：在途请求会被切断，边缘也来不及停止发送。
- **调用的排空只看一个计数。** 请求、执行中的调用和交给后台的结算记在同一个计数上，交出方先让接手方计上再释放自己；归零就是做完了。不选「handler 和执行器各一个计数，再协调两者」：每一种协调都在交接的瞬间读到某一边为空，abort 前后各漏过一次（结算 context 被提前取消、后台重试被提前停掉）。
- **新旧构建的带哈希文件放共享卷，入口页仍各用各的。** 滚动期间页面和代码块的请求会落到不同构建；只保留带哈希的文件，既让旧页面在新节点上取到旧代码块，又不会让旧入口页盖掉新页面，回滚到旧 digest 也直接可用。代价是部署多一个卷和发布前的一步写入。不选「入口按会话粘到同一节点」：要求所有入口都做会话粘滞，节点故障时仍然断；不选「把整份旧构建也放进去」：入口页和策略会跟着旧构建走，新页面反而可能拿到旧入口。


## 目录的规格要补全

**为什么**：规格里说请求怎么写给模型的键（`thinking`、`mid_system`、`untrusted_system_after_tools`、`default_sampling_only`、`forced_tool_choice`、`cache_breakpoints`、`encrypted_reasoning`）缺了不是 no。Chat 请求转给 Claude 模型时，用到缺的那个键的请求被拒（对客户端是 400「模型暂不支持该参数」），换完所有端点也一样，所以发布一个新的转换逻辑之前，库里每个已进目录的模型都要有这些键；规格解析不了的行（例如 `"thinking": true`）还会让 `axiom serve` 加载快照时直接退出。键的含义、每个型号的值和出处见[目录模块的「请求写法的键」](../../internal/catalog/README.md#请求写法的键值照厂商文档不手填)。

**步骤**：在要发布的提交的检出里，`AXIOM_DATABASE_URL` 指向目标库（连接串不进仓库）：

1. 只读查询，找类型不对的值，应当没有结果（有结果就是 `check` 会报「规格解析不了」的行，先用 `axiom catalog update-spec` 改对）：

   ```sql
   SELECT m.slug, k.key, jsonb_typeof(m.spec -> k.key) AS found
   FROM catalog.model m
   CROSS JOIN unnest(ARRAY['encrypted_reasoning', 'mid_system', 'untrusted_system_after_tools',
                           'default_sampling_only', 'forced_tool_choice', 'cache_breakpoints']) AS k(key)
   WHERE jsonb_typeof(m.spec -> k.key) NOT IN ('boolean', 'null')
   UNION ALL
   SELECT slug, 'thinking', COALESCE(spec ->> 'thinking', jsonb_typeof(spec -> 'thinking'))
   FROM catalog.model
   WHERE jsonb_typeof(spec -> 'thinking') NOT IN ('null')
     AND (jsonb_typeof(spec -> 'thinking') <> 'string' OR spec ->> 'thinking' NOT IN ('adaptive', 'manual', 'none'));
   ```

2. `just catalog-dialect-plan`：不写库，每个数据文件里的模型一行：`set`（会改，带「键: 旧值 -> 新值」）、`unchanged`、`absent`（目录里没有这个 slug，只报告）、`retired`、`failed`（目录拒绝新规格，原因在行里）。看过 `set` 的改动、`failed` 的原因。
3. `just catalog-dialect-apply`：同样的一遍，真写，经 `update-spec` 的审计路径（`change`、`rev`）。再跑一次应当全是 `unchanged`。
4. `just catalog-check` 必须通过（退出码 0）：每个已进目录的模型都有它厂商要的键，规格都解析得了，价格与数据集一致。有问题就不发布。缺键的模型不在数据文件里，对着厂商文档把它补进 `deploy/catalog/dialects.json`（改动走 PR 和评审），再回到第 2 步；不要手改库里的规格绕过。

**数据文件覆盖哪些模型**：仓库里没有「哪些模型已上架」的清单（上架是各环境的运营操作），所以数据文件按厂商文档列出了 Claude 和 GPT 现行与仍可用的型号，命令只对库里 slug 完全相同的模型生效（`absent` 的不管，也不创建）。库里用了别的 slug 写法的，`check` 会报缺键，把数据文件里的 slug 改成库里的写法，或反过来统一。

**回滚**：每次写入的旧规格都在 `change` 里（`kind = 'model.update_spec'`），用 `axiom catalog update-spec` 写回。
