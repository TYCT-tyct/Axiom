# CI、runner 与缓存

系统边界见[总体架构](../architecture.md)，流程见 [CONTRIBUTING](../../CONTRIBUTING.md#ci-与检查)，列车恢复见[合并队列](merge-train.md)。执行入口是 `justfile`、`.github/workflows/ci.yml` 和 `scripts/ci/`；机器地址和凭证不入库。

## 作业与路径门禁

| 场景 | 执行 |
|---|---|
| PR | `static`、按路径选择的 `web` 和 `vitals`；另有 `docs`、`secrets`、`pr`、`review`、`docs-impact` |
| 列车 | 全量作业，按整班车相对 `base` 的路径跳过无关检查；Go 测试和 e2e 每个 PR 只在列车跑一次，失败由列车二分定位 |
| `main` | 每次推送全跑，不用路径豁免 |
| 轻量 PR / 列车 | 按受管的 `[lanes] light` 排除 `[lanes] code`，包括纯 Markdown；`static` 跑 `just md-checks scope`（含脚本测试），列车再跑 `secrets` |

非轻量作业：

- `static`：`check-ci` 中的静态部分、迁移版本与 `api-compat`；列车和 main 加 docs/secrets。
- `go-test`：两个分片各跑 `just test i/2`（按 `go list` 顺序隔一取一，各自起集群、四个 worker，工具模块在第一片），仅可缓存的 `go test -race` 参数；耗时由最长的包决定，不再是总和。每个数据库测试复制新迁移模板库；集群每次运行新起，连接串不属于测试输入，经 `containertest.Untracked` 读取。
- `web`：构建、类型、lint、长度、未使用、重复、单元测试、体积预算。PR 只在 `web/`、`api/`、`justfile`、`scripts/web_cache.py`、`scripts/ci/web-container.sh`、`scripts/ci/runner-host.sh`、`ci.yml`、setup action 或 Go 模块文件变化时跑。
- `vitals`：独立串行页面计时；PR 和列车仅在 `web/` 或 `api/*.yaml` 变化时跑，main 总跑。目标与实际门禁见[前端](../frontend.md#实测)。
- `e2e`：两个 `playwright test --shard=i/2` 分片，准备嵌入前端与 Go 静态服务。
- `journey`：浏览器对真实 Axiom、PostgreSQL、Valkey 走注册到调用，宿主 `withcluster` 起库，节点与浏览器在 web 容器经 Docker 网桥连库；失败保留 trace 和节点日志。
- `bench`：列车用 base 的 `tools/benchgate` 比分配，门禁或其模块依赖变化时再用当前版本比，两次都须过；main 用 `bench-once` 带竞态每项一次。基准不混进 `test`；非轻量改动仅纯 Markdown 或纯 `web/` 可跳过基准。
- `deploy`：只有每个改动文件都明确不影响容器时跳过。排除 Markdown、Go 测试、testdata、`.github/`、轻量脚本、web e2e/journey 与其 Playwright 配置；生产 Go、模块文件、应用源码、迁移、deploy、Dockerfile、justfile 都触发。签名检查也在列车和 main 执行。

路径判断由 `scripts/ci_skips.py` 负责，读取失败不跳过；`*-needed` 仅退出 10 表示无需运行。`build` 是 ci.yml 唯一进入规则集的聚合检查，任一作业失败或取消即失败；非轻量列车/main 的 e2e、journey、signing 不可跳过，选中的 vitals 必须成功；普通 main 构建还要求 deploy 成功。其他必需的 workflow 检查独立保留。`just check` 本地全跑；`check-ci` 避免重复 workflow 已跑的 secrets/docs 部分，仓库脚本测试仍跑。

## 运行池

`runs-on` 从 Actions 变量读，未设回退 GitHub 托管 `ubuntu-latest`。

| 变量 | 标签与分配 |
|---|---|
| `RUNS_ON` | `axiom-light`，便宜检查和列车驱动 |
| `RUNS_ON_TRAIN` | `self-hosted, axiom-train`，前 `TRAIN_RUNNERS` 个，默认 4 |
| `RUNS_ON_HEAVY` | `self-hosted, axiom-pr`，排除前 `TRAIN_ONLY` 个（默认 2），八个 runner 中六个可跑 PR |

列车构建的所有作业（含 changes/build）都用列车池，避免占便宜池等待的列车驱动与构建互相等待；前两个专用，第 3、4 个共享。PR 成功的 `workflow_run` 发车；落地用部署密钥一次快进 main，触发 push 工作流，拒绝推送则整班失败、PR 留队。

持久 self-hosted 只接受私有仓库可信贡献；开放 fork 或外部贡献前，所有 PR 作业（便宜与重作业）必须迁到 GitHub 托管 runner，不能只靠标签隔离。

## runner 管理

`scripts/ci/runner-host.sh` 统一安装、自检、删除与资源限额，工具链预置 Actions 缓存，每 runner 独立 Go 缓存。

- buildx 固定 v0.38.0，按发布 `checksums.txt` 校验 SHA-256，`check` 执行版本检查；发布步骤在列车和 main 都跑，仅 main 保留交付 artifact，防止未验证步骤落地才失败。
- `RUNNER_TOKEN` 注册，具仓库 Administration 写权限的 `GH_TOKEN` 同步标签；请求头经管道、注册 token 经 `ACTIONS_RUNNER_INPUT_TOKEN` 传递，不进命令行。修改宿主前只读查询 runner 列表，写权限在标签更新时验证。
- 删除时先无模式查询 systemd 服务再按本地名筛选；没有已安装或已加载的 `actions.runner.TYCT-tyct-Axiom.*` 才删共享 slice、父 slice drop-in 和 Go/Node 工具链，查询失败保留；`not-found` 失败残影不算剩余单元。
- runner 压缩包每次校验，下载至同目录临时文件，校验成功才原子改名，失败删临时文件、坏缓存重下。宿主 pre-PR 在同一 slice 最多并发三个。
- 便宜池不由该脚本管理：`axiom-l1/l2` 用 `node:24-bookworm` 容器运行官方 runner、`unless-stopped`，每个 1 核/1.5G，只挂自身目录和只读 gh，标签仅 `axiom-light`，不接触宿主凭证。

## 缓存与工具下载

runner 保留 Go 构建、模块缓存与 `AXIOM_CACHE_DIR`（默认自身 `_webcache`），web/e2e 在宿主装工具链和依赖后挂入容器，避免每次冷编译。

- 前端构建键包括 `web/`、版本戳、工具链、`VITE_`/`NODE_`/`AXIOM_` 变量及 `web_cache.py` 自身哈希。
- e2e 通过键还包括 `api/`、全部 Go 代码、go.mod/go.sum、测试配置和 Go 版本；命令含分片序号与总数，`1/2` 不可替代 `2/2` 或其他总数。
- 缓存不自动淘汰，维护时清理 `<cache>/web-build` 与 `<cache>/e2e` 的过期内容。
- `fetch-release-binary.sh` 缓存 just 压缩包到 `$RUNNER_TOOL_CACHE/<名>/<版本>/`，每次核对固定 SHA-256，缺失或不符才重下。cosign/syft 由 `install-release-tools.sh` 经 `release-asset.sh` 下载：网址失败后走带 `GH_TOKEN` 的 releases REST API、`Accept: application/octet-stream`，两条路径都必须过校验和，不符不入缓存。Go/Node 也用工具缓存；受管的 gitleaks 下载尚无同等兜底。

## 测试容器

runner `.env` 设置 `AXIOM_TEST_CGROUP_PARENT=axiom-ci.slice`。`containertest.WithHostPolicy` 对 PostgreSQL（含 withcluster）、Valkey、diff 的 new-api 设置父 cgroup；空值保留开发机默认。CI diff 经 host-gateway 直连监听所有 IPv4 的宿主测试上游，Axiom 仍走回环；开发机保留端口隧道，避免 CI 使用无法定制 cgroup 的 SSHD sidecar。

Ryuk 无公开父 cgroup 配置；仅 go-test/journey/bench 在父级非空时设 `TESTCONTAINERS_RYUK_DISABLED=true`，其他作业和开发机保留 Ryuk。作业以 `GITHUB_RUN_ID-GITHUB_RUN_ATTEMPT-GITHUB_JOB` 设置 `AXIOM_TEST_JOB`，所有测试服务标 `axiom.ci.job`，经 Untracked 读取、不污染测试缓存。

`clean-test-containers.sh` 在测试前与结束（always）删本作业容器，并清理同标签键且创建至少 40 分钟的遗留容器；不碰无标签容器，正常共享服务由 withcluster 删除，强杀由后续作业兜底，不增加测试重试或启动期限。

端口统一在 `127.0.0.1:20000–29999` 自选发布（脚本用 `publish.sh` 的 `run_published`），避开 Linux 临时端口 `32768–60999` 的出站连接与 bind(0) 竞争；绑定碰撞时 Start 删除未启动容器、换号，最多 8 次且记录。`just port-stress` 在独立 Docker-in-Docker 网络命名空间把临时范围设为 1000、轮换占用 900，验证默认分配失败而 Start 成功；不在共享宿主直接跑、不进 CI。桥接直连不替代端口发布，因为等待策略与 Docker Desktop 仍需发布端口。

## 部署演练容器

`deploy-check.sh` 为每个 runner 与工作区哈希生成独立 compose 项目、镜像、卷和空闲端口；前后清自身资源，失败先输出日志。数据库镜像每作业只构建一次，列车复用已构建镜像，防止标签或 provenance 变化重建正在服务的库。

- Compose 不另加命令墙钟期限或 `--wait-timeout`；步骤上限 40 分钟，健康等待由健康条件控制，数据库 `start_period` 2 分钟；down 最多 5 秒，CI 容器无重启策略。
- 旧迁移角色升级先停节点，再改数据库环境并启动，避免旧进程丢会话锁退出时被误算为新启动。
- `old-binary-check.sh` 用当前二进制迁移独立库，再启动上一版本并等 readyz；不在 compose 栈里的遗留库按 `axiom.ci.old-binary-check` 和 40 分钟年龄清理。
- 下一作业清理自身旧栈，任意 runner 的部署检查也清理带本脚本标签、名以 `axiom-ci-` 开头且创建超过 40 分钟的遗留栈，不碰其他容器。
- Compose 的 `x-hardened` 将 `${AXIOM_TEST_CGROUP_PARENT:-}` 传给 PG、Valkey、Axiom、迁移与密钥初始化；空值省略。启动后、删除前 inspect 全部容器（包括退出的一次性服务），非空则核对父级；默认父级由 Docker 决定。隔离后可并行、不用文件锁；本地 just up 仍用 `axiom`、`axiom:local` 与 8080/8082。

## 前端容器与诊断

`web-container.sh` 使用固定 digest 的 Playwright 镜像，浏览器和 serveweb 共用独立网络命名空间，避免其他作业增删网卡中断浏览器。

- 共享 `axiom-ci.slice`：96 核宿主总限 80 核，内存高水位 45G、硬限 50G；容器再限 8 核且保留私有 cgroup 命名空间，避免 Node 误读父级 80 核。
- Playwright 单分片用可用 CPU 一半（8 核为四 worker），两分片各三、合计六，保留浏览器与测试进程余量；总体 worker 上限 8。容器内记录自身 cgroup 与配额，runner 从容器宿主 PID 核验实际 slice 及共享配额。
- 容器以 runner UID/GID 运行，复用工具缓存与临时目录，checkout 后在容器 HOME 信任工作区；名含作业和分片，取消只删本次尝试。失败 trace 保留七天。
- `gate_stress` 只在 main 或有独立提交的一次性分支运行，不能用待合并 PR 的 SHA（另建同 SHA 分支也不行）。它绕过通过缓存，跑 web 和不分片 e2e，把价格发布、供应商连接详情、账户、模型名四场景各重复 50 次。
- 诊断由 `gate-stress` 聚合，字面名 `build` 的完整聚合明确失败，防止 GitHub 把跳过当通过。误用 PR SHA 后需完整 CI 或新提交才能入队；普通检查覆盖不变。诊断不能替代完整构建。
