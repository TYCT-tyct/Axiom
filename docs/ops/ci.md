# CI、runner 与缓存

系统边界见[总体架构](../architecture.md)，流程见 [CONTRIBUTING](../../CONTRIBUTING.md#ci-与检查)，列车恢复见[合并队列](merge-train.md)。执行入口是 `justfile`、`.github/workflows/ci.yml` 和 `scripts/ci/`；机器地址和凭证不入库。

## 作业与路径门禁

| 场景 | 执行 |
|---|---|
| PR | `static`、按路径选择的 `web` 和 `boards`；Go 道选中时跑四片 Go 测试，e2e 道选中时跑四个 e2e 分片，在进列车前发现问题；另有 `docs`、`secrets`、`pr`、`review`、`docs-impact` |
| 列车 | 按整班车相对 `base` 的路径选择，Go 道、e2e 道与 PR 同一规则，另跑 `journey`、`bench`、`deploy`、`signing`；不跑 `vitals` 和 `boards`；失败由列车二分定位；e2e 在列车里按内容复用通过结果 |
| `main` | 每次推送选择全部作业，Go 测试带竞态检测和覆盖率；同一提交、同一树已有成功列车构建时，复用其中实际通过的作业（Go 测试除外），其他作业照跑；无证据则全量。main 失败最先修 |
| 轻量 PR / 列车 | 按受管的 `[lanes] light` 排除 `[lanes] code`，包括纯 Markdown；`static` 跑 `just md-checks scope`（含脚本测试），列车再跑 `secrets` |

非轻量作业：

- `static`：`check-ci` 中的静态部分、迁移版本与 `api-compat`；列车和 main 加 docs/secrets。
- 分道（`scripts/ci_skips.py`，PR 与列车相同，main 无 base 全选）：Go 道在改到 `web/` 以外的非 Markdown 文件，或 Go 服务前端时读的 `web/vite.shared.ts`、`web/scripts/embed.ts`、`web/scripts/compress.ts` 时选中；e2e 道在 `web` 的输入、服务前端的 `internal/platform/webui/`、`scripts/ci/` 或 e2e 读的 `internal/platform/idem/testdata/same-request.json` 变化时选中。纯前端改动不跑 Go 测试，Go 测试读不到 `web/` 下的其他文件；其余 Go 改动不跑 e2e，e2e 对着契约模拟的接口，真实节点由 `journey` 覆盖。
- `go-test`：Go 道选中时跑，四个分片各跑 `just test i/4`（按 `go list` 顺序轮流分配，各自起集群、四个 worker，工具模块在第一片），仅可缓存的 `go test` 参数；PR 和列车不带竞态检测，main 用 `just test i/4 race` 加竞态检测和覆盖率（同一个包实测 38 秒对 119 秒），竞态问题落地后由 main 发现；耗时由最长的包决定，不再是总和。每个数据库测试复制新迁移模板库；集群每次运行新起，连接串不属于测试输入，经 `containertest.Untracked` 读取。
- `web`：构建、类型、lint、长度、未使用、重复、单元测试、体积预算。PR 只在 `web/`、`api/`、`justfile`、`scripts/web_cache.py`、`scripts/ci/web-container.sh`、`scripts/ci/runner-host.sh`、`ci.yml`、setup action 或 Go 模块文件变化时跑。
- `vitals`：独立串行页面计时；只在 main 上跑，PR 和列车都不跑；相同构建通过后可命中内容缓存。目标与实际门禁见[前端](../frontend.md#实测)。
- `e2e`：四个按耗时分配的分片（`scripts/ci/playwright.py e2e i/4`，默认完整文件一起分配；文件顶层明确声明 parallel 的按单例分配，保留顺序测试语义），准备嵌入前端与 Go 静态服务；排除画板证明和页面计时。
- `boards`：四个按耗时分配的分片，各两个 worker，独立运行 `playwright.boards.config.ts`，不启动产品 Go 服务。PR 按画板包、转换脚本、证明及设计工具、Playwright 配置、锁文件、固定字体数据、CI 脚本和 setup action 的路径选择，路径比较失败也跑；列车不跑；main 总是选择。每片按单板内容和选中的测试集合复用通过结果，只有未命中的比较进入 Playwright；整个选中集合通过才记通过标记。失败保留截图、diff 和 trace，不反复序列化大型整板 DOM；单例上限三分钟，分片上限 30 分钟。
- `journey`：只在列车和 main 上跑；浏览器对真实 Axiom、PostgreSQL、Valkey 走注册到调用，宿主 `withcluster` 起库，节点与浏览器在 web 容器经 Docker 网桥连库；失败保留 trace 和节点日志。
- `bench`：列车用 base 的 `tools/benchgate` 比分配，门禁或其模块依赖变化时再用当前版本比，两次都须过；main 用 `bench-once` 带竞态每项一次。基准不混进 `test`；非轻量改动仅纯 Markdown 或纯 `web/` 可跳过基准。
- `deploy`：只有每个改动文件都明确不影响容器时跳过。排除 Markdown、Go 测试、testdata、`.github/`、轻量脚本、web e2e/journey 与其 Playwright 配置；生产 Go、模块文件、应用源码、迁移、deploy、Dockerfile、justfile 都触发。签名检查也在列车和 main 执行。

路径判断由 `scripts/ci_skips.py` 和 `ci.yml` 的画板输入比较负责，读取失败不跳过；`*-needed` 仅退出 10 表示无需运行。`build` 是 ci.yml 唯一进入规则集的聚合检查，任一作业失败或取消即失败；所有选中的作业必须成功（含选中的 web、Go、e2e，列车/main 的 journey、bench、deploy、signing，以及 main 和 PR 选中的 vitals/boards）；内容命中由作业内部返回成功；只有已查证的同提交列车成功作业允许在 main 跳过，其他选中作业不能以跳过冒充通过。其他必需的 workflow 检查独立保留。`just check` 本地全跑；`check-ci` 避免重复 workflow 已跑的 secrets/docs 部分，仓库脚本测试仍跑。

列车落地后 main 不再重复整套已通过的作业：`changes` 用 `scripts/ci/train_result.py` 核对该提交的 `train/verified` 成功状态、`ci.yml` 在 `train/main/*` 上成功的 dispatch 构建、提交 SHA 与 Git 树，以及 `changes`、`static`、`build` 的成功结果；矩阵必须所有分片成功才复用。相同树已经通过相同作业，快进没有引入新代码，因此 `build` 接受这些已查证的跳过；main 的步骤摘要链接原构建并逐项报告结果。未选中、未找到或未通过的列车作业仍在 main 运行；缺记录、树不同、构建不存在或 API 查询失败均回到全量。`changes`、`build` 和 flaky Issue 关闭提醒仍执行；显式 release dispatch 不复用，发布保留步骤照跑。这样无需持久化另一份落地记录，也不依赖会被删除的列车分支。

端到端隔离与 Go 共用 `.github/flaky.toml`，格式见文件内示例。e2e 失败报告必须完整、无全局错误，且失败全由登记的 spec 与完整标题匹配、Issue 仍开着，列车才在本分片内定向重跑一次；未知失败、不完整报告和未执行的依赖测试均不放过。PR/main 只报告，失败仍阻断；原始与重跑 JSON、测试证据分开保存，重跑通过不写内容通过缓存，避免后续运行看不见 flake。Issue 关闭提醒同时检查两类条目。

每周一 01:17（UTC）`ci-report` 把上一周的 ci 与 train 运行汇成周报（通过率、作业耗时与排队、机器分钟、列车失败与退车、失败最多的测试），覆盖 #830 的正文并另发一条评论留档；表格由 `scripts/ci_report.py` 从 GitHub API 的 JSON 算出，跑在便宜池。

## 运行池

`runs-on` 从 Actions 变量读，未设回退 GitHub 托管 `ubuntu-latest`。

| 变量 | 标签与分配 |
|---|---|
| `RUNS_ON` | `axiom-light`，便宜检查和列车驱动 |
| `RUNS_ON_TRAIN` | `self-hosted, axiom-train`，前 `TRAIN_RUNNERS` 个，默认 10（全部） |
| `RUNS_ON_HEAVY` | `self-hosted, axiom-pr`，排除前 `TRAIN_ONLY` 个（默认 2），十个 runner 中八个可跑 PR |

列车构建的所有作业（含 changes/build）都用列车池，避免占便宜池等待的列车驱动与构建互相等待；前两个专用，第 3、4 个共享。PR 成功的 `workflow_run` 发车；落地用部署密钥一次快进 main，触发 push 工作流，拒绝推送则整班失败、PR 留队。

持久 self-hosted 只接受私有仓库可信贡献；开放 fork 或外部贡献前，所有 PR 作业（便宜与重作业）必须迁到 GitHub 托管 runner，不能只靠标签隔离。

## 在 GitHub 托管 runner 上

仍只需把 `RUNS_ON`、`RUNS_ON_HEAVY`、`RUNS_ON_TRAIN` 指向 JSON 标签（如 `"ubuntu-latest"`）。单次比较可在本分支 dispatch 时传 `runs_on='"ubuntu-latest"'`；留空保持原池选择，不修改仓库变量，也不改变其他 PR。

setup 按仓库版本安装 Go、Node、pnpm、just，托管环境通过 Actions 缓存工具和依赖。Playwright 镜像自带 Chromium、系统库和字体。只有宿主的 `axiom-ci.slice` 正在运行时才传父 cgroup 并核验其配额；每个容器仍用独立网络命名空间，CPU 上限取 runner 可用核数与 8 的较小值。

源字体挂入容器。托管作业按 `sources.json` 中的字体名和 SHA-256 还原 Actions 缓存，自管作业读共享卷。源文件下载失败等一秒重试一次，并用 HTTP Range 从已收到的字节续传。完整文件每次读取都校验 SHA-256。画板和 Python 字体工具共用相同的源文件名，CI 直接使用已提交的字体子集。

Go 缓存只归档模块目录，按根模块和工具模块的 `go.mod`、`go.sum` 查找，变更时可用同平台上次缓存补齐。测试服务和 Playwright 镜像各用一份归档，按平台和镜像引用查找；命中就载入 Docker，缺失时拉取，失败重试一次。Playwright 拉取固定 digest，用含该 digest 的本地标签启动，以适配只恢复标签的 Docker 镜像存储。

成功下载后立即存回，避免后续测试失败丢掉缓存。PR 和列车的归档受 ref 隔离；`download-cache` 在默认分支复用列车结果时仍填充可供两者读取的归档。冷启动时并行作业可能同时下载，缓存淘汰也会再次触发下载。验收在同一 ref 的两次运行中比较缓存键、`Source font Actions cache hit`、`Pulling` 和 `Loaded cached`。

持久 runner 继续只读写本地内容缓存。托管 runner 用官方 `@actions/cache` 客户端（setup 固定版本，避免自写缓存服务协议），在 `web_cache.py` 原键生成和成功判定之外增加存取；构建、e2e、vitals 和单板标记分别使用其原内容键，无前缀回退，归档路径相对缓存目录。故障回到实跑，失败不写通过标记；冷缓存与缓存淘汰会增加耗时。浏览器证据按实际 `runner.environment` 选择持久目录或失败 artifact，和变量是否填写无关。

公开仓库的标准托管 runner 计算时间免费，见 [GitHub 托管 runner 规格](https://docs.github.com/en/actions/reference/runners/github-hosted-runners#standard-github-hosted-runners-for-public-repositories)。两次整套 dispatch 均留空 base/release/gate_stress，先托管、后留空 `runs_on` 跑自管。作业结束后运行 `python3 scripts/ci/runner-comparison.py --hosted <运行号> --self-hosted <运行号>`，把实测耗时和排队放进 PR。准备开销包含 setup 和缓存收尾，队列只计作业就绪后等 runner 的时间。

## runner 管理

`scripts/ci/runner-host.sh` 统一安装、自检、删除与资源限额，工具链预置 Actions 缓存，每 runner 独立 Go 缓存。

- buildx 固定 v0.38.0，按发布 `checksums.txt` 校验 SHA-256，`check` 执行版本检查；发布步骤在列车和 main 都跑，仅 main 保留交付 artifact，防止未验证步骤落地才失败。
- `RUNNER_TOKEN` 注册，具仓库 Administration 写权限的 `GH_TOKEN` 同步标签；请求头经管道、注册 token 经 `ACTIONS_RUNNER_INPUT_TOKEN` 传递，不进命令行。修改宿主前只读查询 runner 列表，写权限在标签更新时验证。
- 删除时先无模式查询 systemd 服务再按本地名筛选；没有已安装或已加载的 `actions.runner.TYCT-tyct-Axiom.*` 才删共享 slice、父 slice drop-in 和 Go/Node 工具链，查询失败保留；`not-found` 失败残影不算剩余单元。
- runner 压缩包每次校验，下载至同目录临时文件，校验成功才原子改名，失败删临时文件、坏缓存重下。宿主 pre-PR 在同一 slice 最多并发三个。
- 便宜池不由该脚本管理：`axiom-l1/l2` 用 `node:24-bookworm` 容器运行官方 runner、`unless-stopped`，每个 1 核/1.5G，只挂自身目录和只读 gh，标签仅 `axiom-light`，不接触宿主凭证。

## 缓存与工具下载

runner 保留 Go 构建、模块缓存与 `AXIOM_CACHE_DIR`（默认同一 runner 用户共享的 `$XDG_CACHE_HOME/axiom`，未设 XDG 时为 `~/.cache/axiom`），web/e2e 在宿主装工具链和依赖后挂入容器，避免每次冷编译。

- 前端构建键包括 `web/`、版本戳、工具链、`VITE_`/`NODE_`/`AXIOM_` 变量及 `web_cache.py` 自身哈希。
- e2e 通过键还包括 `api/`、全部 Go 代码、go.mod/go.sum、测试配置、Go 版本和完整测试清单；一个文件或分片的通过不能代替另一集合。
- boards 按单板划分 `src/<板名>/`、固定设计的整板或拆分文件；共享 runtime、引擎、字体、转换器、测试与配置变化让所有板失效。区域模块求值后仅哈希该板条目，生成的 demo 索引仅哈希该板的加载函数；因此 AxApp/AxAppPhone 区域变化不使其他板失效。控制台产品代码不在 boards 键中。缓存输入读取失败禁用本片缓存并跑完整选中集合。
- vitals 通过键以 `web_cache.py` 的构建键为核心，加 Go 服务、命令、CI harness 和 `BASE`；只有 main 运行，构建键相同才可复用。
- 缓存不自动淘汰，维护时清理 `<cache>/web-build`、`<cache>/e2e`、`<cache>/boards` 与 `<cache>/vitals` 的过期内容。
- `fetch-release-binary.sh` 缓存 just 压缩包到 `$RUNNER_TOOL_CACHE/<名>/<版本>/`，每次核对固定 SHA-256，缺失或不符才重下。cosign/syft 由 `install-release-tools.sh` 经 `release-asset.sh` 下载：网址失败后走带 `GH_TOKEN` 的 releases REST API、`Accept: application/octet-stream`，两条路径都必须过校验和，不符不入缓存。Go/Node 也用工具缓存；受管的 gitleaks 下载尚无同等兜底。

## 测试容器

runner `.env` 设置 `AXIOM_TEST_CGROUP_PARENT=axiom-ci.slice`。`containertest.WithHostPolicy` 对 PostgreSQL（含 withcluster）、Valkey、diff 的 new-api 设置父 cgroup；空值保留开发机默认。CI diff 经 host-gateway 直连监听所有 IPv4 的宿主测试上游，Axiom 仍走回环；开发机保留端口隧道，避免 CI 使用无法定制 cgroup 的 SSHD sidecar。

Ryuk 无公开父 cgroup 配置；仅 go-test/journey/bench 在父级非空时设 `TESTCONTAINERS_RYUK_DISABLED=true`，其他作业和开发机保留 Ryuk。作业以 `GITHUB_RUN_ID-GITHUB_RUN_ATTEMPT-GITHUB_JOB` 设置 `AXIOM_TEST_JOB`，所有测试服务标 `axiom.ci.job`，经 Untracked 读取、不污染测试缓存。

测试夹具 `pgtest` 在宿主发布端口对共享 PostgreSQL 执行 `SELECT 1`，迁移比较在容器内通过 TCP 查询目标库，两者都避开只监听 socket 的初始化进程。`kvtest` 等 Valkey 的发布端口可达后，在容器内以应用测试账号认证并验证 `PONG`，确认 ACL 生效。SeaweedFS 等 S3 端口与 master 选出 leader，再通过 shell 建桶。

`clean-test-containers.sh` 在测试前与结束（always）删本作业容器，并清理同标签键且创建至少 40 分钟的遗留容器；不碰无标签容器，正常共享服务由 withcluster 删除，强杀由后续作业兜底，不增加测试重试或启动期限。

端口统一在 `127.0.0.1:20000–29999` 自选发布（脚本用 `publish.sh` 的 `run_published`），避开 Linux 临时端口 `32768–60999` 的出站连接与 bind(0) 竞争；绑定碰撞时 Start 删除未启动容器、换号，最多 8 次且记录。`just port-stress` 在独立 Docker-in-Docker 网络命名空间把临时范围设为 1000、轮换占用 900，验证默认分配失败而 Start 成功；不在共享宿主直接跑、不进 CI。桥接直连不替代端口发布，因为等待策略与 Docker Desktop 仍需发布端口。

## 部署演练容器

`deploy-check.sh` 为每个 runner 与工作区哈希生成独立 compose 项目、镜像、卷和空闲端口；前后清自身资源，失败先输出日志。数据库镜像每作业只构建一次，列车复用已构建镜像，防止标签或 provenance 变化重建正在服务的库。

- Compose 不另加命令墙钟期限或 `--wait-timeout`；步骤上限 20 分钟、作业上限 22 分钟，健康等待由健康条件控制，数据库 `start_period` 2 分钟；down 最多 5 秒，CI 容器无重启策略。
- 旧迁移角色升级先停节点，再改数据库环境并启动，避免旧进程丢会话锁退出时被误算为新启动。
- `old-binary-check.sh` 用当前二进制迁移独立库，再启动上一版本并等 readyz；不在 compose 栈里的遗留库按 `axiom.ci.old-binary-check` 和 40 分钟年龄清理。
- 下一作业清理自身旧栈，任意 runner 的部署检查也清理带本脚本标签、名以 `axiom-ci-` 开头且创建超过 40 分钟的遗留栈，不碰其他容器。
- Compose 的 `x-hardened` 将 `${AXIOM_TEST_CGROUP_PARENT:-}` 传给 PG、Valkey、Axiom、迁移与密钥初始化；空值省略。启动后、删除前 inspect 全部容器（包括退出的一次性服务），非空则核对父级；默认父级由 Docker 决定。隔离后可并行、不用文件锁；本地 just up 仍用 `axiom`、`axiom:local` 与 8080/8082。

## 前端容器与诊断

`web-container.sh` 使用固定 digest 的 Playwright 镜像，浏览器和 serveweb 共用独立网络命名空间，避免其他作业增删网卡中断浏览器。

- 共享 `axiom-ci.slice`：96 核宿主总限 80 核，内存高水位 45G、硬限 50G；容器再限 8 核且保留私有 cgroup 命名空间，避免 Node 误读父级 80 核。
- Playwright 单分片用可用 CPU 一半（8 核为四 worker），两分片各三、合计六，保留浏览器与测试进程余量；总体 worker 上限 8。容器内记录自身 cgroup 与配额，runner 从容器宿主 PID 核验实际 slice 及共享配额。
- 容器以 runner UID/GID 运行，复用工具缓存与临时目录，checkout 后在容器 HOME 信任工作区；名含作业和分片，取消只删本次尝试。失败证据保留两天，见下节。
- `gate_stress` 只在 main 或有独立提交的一次性分支运行，不能用待合并 PR 的 SHA（另建同 SHA 分支也不行）。它绕过通过缓存，跑 web 和不分片 e2e，把价格发布、供应商连接详情、账户、模型名四场景各重复 50 次。
- 诊断由 `gate-stress` 聚合，字面名 `build` 的完整聚合明确失败，防止 GitHub 把跳过当通过。误用 PR SHA 后需完整 CI 或新提交才能入队；普通检查覆盖不变。诊断不能替代完整构建。

## 分片计时与作业上限

分片权重固定在 `scripts/ci/playwright-durations.json`，每个 runner 使用同一份快照，不能各读本地最近计时，否则并发分片可能漏测。boards 按单例、e2e 默认按整文件、顶层 `test.describe.configure({ mode: "parallel" })` 的文件按单例做最长优先的贪心分配；新测试取已有单例权重的中位数，不会被漏掉。运行时先由 Playwright 枚举真实测试，再分配，清单不是测试白名单。

初始权重来自绿色 run [44127329188](https://github.com/TYCT-tyct/Axiom/actions/runs/44127329188)（boards）与 [44093853699](https://github.com/TYCT-tyct/Axiom/actions/runs/44093853699)（e2e）的分片实测总时长，按当时所列测试均摊 worker 时间；旧 reporter 没有逐例数据，所以这是冷启动估计，并非逐例测量。e2e 已声明跳过的测试不占权重。每次绿色实跑输出 `CI_TIMINGS=<JSON>`（逐例，跳过用例不占权重），并保留 `playwright-report/report.json`；刷新权重时把绿色日志中的对应键合入快照并提交。失败或缓存命中不刷新。没有动态读取各 runner 的报告。

超时依据 2026-11-17 最近 18 次绿色 ci 的已执行作业（排除排队、跳过和失败），p90 用 nearest-rank；短于一分钟的作业按分钟粒度留余量。boards 采用四片绿色 run 44127329188 的最长片，尚无足够样本估计新分片的 p90。

| 作业 | 样本数 | p90（分钟） | timeout-minutes |
|---|---:|---:|---:|
| changes | 18 | 0.20 | 1 |
| static | 18 | 9.33 | 25 |
| go-test（每片） | 28 | 10.18 | 30 |
| web | 16 | 4.62 | 12 |
| vitals | 12 | 18.22 | 45 |
| e2e（每片） | 32 | 11.12 | 30 |
| boards（每片） | 4 | 11.55（最长片代理值） | 30 |
| journey | 14 | 3.38 | 10 |
| bench | 14 | 10.85 | 30 |
| deploy | 12 | 7.28 | 22 |
| signing | 14 | 0.63 | 2 |
| build | 18 | 0.13 | 1 |
| gate-stress 聚合 | 0 | 与 build 相同的纯结果检查，未单独测量 | 1 |

## runner 证据

实际运行在 self-hosted 时，vitals/e2e/boards/journey 在运行前把宿主名与 `<AXIOM_CACHE_DIR>/reports/<run-id>/<job>/<shard>-<attempt>/` 写到步骤摘要；未显式配置缓存目录时统一用 `web_cache.py` 的宿主默认目录；容器挂载为 `/ci/axiom`。同一宿主的 runner 共享通过标记，分片换 runner 仍可复用；旧的 runner 独立 `_webcache` 不迁移，首次运行重新填充。通过 `AXIOM_TEST_RESULTS` / `AXIOM_PLAYWRIGHT_REPORTS` 指定容器挂载内的输出位置，让 trace、截图、diff、节点日志和 JSON 报告直接写进持久目录；这两个输出路径不进入内容键。结束时先清容器，补收工作区内的报告；绿色运行清掉截图等证据，只保留计时报告。不同分片和重试不覆盖。超时/取消也运行清理和留证步骤；若 runner 本身离线，已落盘的证据仍留在目录，摘要已有路径；尚未写完的 trace 不能保证完整。

每个浏览器作业开始与结束都清理最近一次收集已超过两天的 run 目录，不碰构建或通过缓存。没有后续作业时不运行定时删除。self-hosted 不调用 `upload-artifact` 保存测试证据，GitHub 上只保留显式 release artifact；GitHub 托管 runner 上传失败证据，保留两天。
