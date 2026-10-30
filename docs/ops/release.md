# 发布前

发布前逐项确认，本文记录固定交付物和模型目录检查；其余按 project-workflow 的发布方法。部署到服务器要先问负责人（[CONTRIBUTING](../../CONTRIBUTING.md)），本文的写库步骤改的是线上目录，同样先问。

## 账户速率限额上线前核对

首次上线 E4-2/P13 时，默认 6000 次/窗口从原有每 key 节点保护扩展到**按账户聚合**的业务额度（调用与 count_tokens 各一池，默认窗口一分钟）。已有账户若 4 把 key 各 2000 rpm，升级后会收到 account_rate 429。账户/等级覆盖值未设置时自动继承此默认值；迁移不会自动识别高流量账户。

发布执行者须在获准访问生产后完成以下核对，并把时间范围、账户峰值及覆盖值清单写入发布记录；本地测试不替代生产核对，未取得完整数据不得标成通过：

1. 从能识别已认证账户的入口请求记录，分别汇总推理与 count_tokens 在配置 RateWindow 内的峰值，覆盖代表性业务高峰并评估突发余量；计入业务 key 拒绝，排除超过节点保护的尝试。只有计费/完成调用记录不足以证明安全，它遗漏 count_tokens、未预留及失败的请求；没有完整入口数据时先补观测，暂缓默认限额上线。
2. 对达到或超过默认值、或需要余量的现有账户，先通过后台 `PUT /api/admin/v1/rate-limits/account/{id}` 配置经确认的 rate_requests，并读取核对，保留 MFA、幂等键、理由与审计。需要保持不设账户业务上限时可明确设 0，仍受节点/key 限制；不要以 null 代替 0，null 会重新继承等级或 6000。旧节点不提供该接口时，须在放流量前通过隔离的新版本管理入口预设，核对完成后再切流量。
3. 切换后核对 `axiom_gateway_rate_limited_total{reason="account_rate"}` 与 reason="business_key_rate" 的增量、受影响账户的入口记录和 429 头。按账户的识别靠访问记录，不把账户 ID 加入低基数指标；异常时调整已核对的后台覆盖值或按下节回滚镜像，保留兼容的数据库列。

## 固定交付物与 digest

`ci.yml` 的 `deploy` 作业在列车和 `main`（push 运行，或落地退回逐个合并后调起的 dispatch）上都运行 `release-check.sh`，所以只在 `main` 上会跑的发布步骤会先在列车里失败、退回弄坏它的 PR；只有 `main` 保留发布交付物，PR 不生成。runner 由 `runner-host.sh` 安装固定版本（当前 v0.38.0）、按发布页 `checksums.txt` 校验 SHA-256 的 buildx CLI 插件，`check` 运行 `docker buildx version`；缺少它时 `docker buildx create --name …` 会落到 `docker --name` 并以 125 退出（2026-10-29 `main` 的 `deploy`）。
`release-check.sh` 构建一次 Linux/amd64 程序和 scratch 镜像，通过同一次 [Buildx 多 exporter 构建](https://docs.docker.com/build/exporters/#multiple-exporters)导出 OCI 归档并写入临时的 loopback
registry（仅绑定 `127.0.0.1`，不上传外部仓库）。它按归档的 manifest digest 拉取镜像，迁移和服务都消费同一个
`repository@sha256:…`，包括旧迁移角色升级后的再次启动；重启不重新构建。其他 CI 作业仍会构建自己的测试程序和前端。

成功的部署作业保留 `release-<源码 SHA>-<run attempt>` Actions artifact，保留 30 天，内含：

- `image.oci.tar`：原始镜像字节；发布时只复制这份，不在部署机重新构建。
- `release.json`：源码完整提交、实际 Go/Node 工具链、前端构建版本、迁移 SQL 路径及 SHA-256、平台和镜像 manifest digest。
  前五项同时写进镜像标签：`org.opencontainers.image.revision`、`io.axiom.go-toolchain`、`io.axiom.node-toolchain`、
  `org.opencontainers.image.version`、`io.axiom.migrations`。迁移清单包含仓库内嵌 SQL；River 自带的迁移随源码提交锁定的 Go 模块版本追溯。

### 选择和核对

1. 在 Actions 找到目标 `main` 提交的 **整个 ci 工作流成功**的运行（不能只看 deploy 成功），记录 run ID、attempt、源码 SHA；
   下载该运行同 SHA/attempt 的 release artifact。失败运行、PR、列车产物都不能作为发布版本。
2. 在该源码 SHA 的检出中，解压 artifact 到独立目录，例如 `tmp/release`。运行：

   ```bash
   python3 scripts/release_image.py verify tmp/release/release.json tmp/release/image.oci.tar
   jq '{source_commit, go_toolchain, node_toolchain, frontend_version, migrations, platform, image_digest}' tmp/release/release.json
   test "$(jq -r .source_commit tmp/release/release.json)" = "$(git rev-parse HEAD)"
   ```

   verifier 检查 manifest、config、每层的长度与 SHA-256，标签与 JSON 一致，平台是 Linux/amd64，输出的 manifest digest
   等于 JSON 的 `image_digest`。digest 是 OCI manifest 的哈希，不是镜像 config ID、tar 文件校验和或 tag。
   同时逐项核对工具链、前端版本和迁移是否符合本次发布与目标库；镜像 digest 不保证数据库回滚兼容。
3. **外部 registry 的选择、登录与上传由负责人执行**（花钱/对外动作，CI 不持有上传凭证）。负责人同意发布后，用能保留
   digest 的工具复制同一 OCI 归档，例如已安装的 skopeo（这里不把它加入项目依赖）：

   ```bash
   # RELEASE_REPOSITORY 是负责人选择的仓库；staging 只用于传输，不能用于部署。
   skopeo copy --preserve-digests oci-archive:tmp/release/image.oci.tar "docker://$RELEASE_REPOSITORY:staging"
   digest="$(jq -r .image_digest tmp/release/release.json)"
   skopeo inspect --raw "docker://$RELEASE_REPOSITORY@$digest" > tmp/release/registry-manifest.json
   test "sha256:$(sha256sum tmp/release/registry-manifest.json | cut -d ' ' -f1)" = "$digest"
   ```

   不允许通过重新构建或转换 manifest 来消除 digest 不一致。保留 release JSON、归档及成功运行的引用到发布记录；30 天的
   Actions 保留期不能替代负责人维护的长期归档。签名留给 M3；当前 provenance 和 digest 核对不是签名证明。
4. 在生产 `deploy/.env` 设置 `DEPLOY_IMAGE=<负责人选择的仓库>@sha256:<上一步的 digest>`；迁移和服务共用此值。
   按[生产入口](backup-restore.md#生产入口)执行，必须带 `--no-build`，先 pull 这个 digest，不运行开发用的 `just up`。
   用 `docker inspect` 核对迁移与服务的 `.Config.Image` 是该 digest 引用，版本 API 与 JSON 的 `frontend_version` 一致。
   回滚选择上次成功发布记录里的 digest，重复核对和部署步骤；已经执行的迁移不会因此自动撤回，先确认旧程序仍兼容当前 schema。
   本版本起 `axiom migrate` 在 `platform.migration_checksums` 记下每个已应用迁移的文件名和 SHA-256，迁移和节点启动都核对：
   已合并的迁移被改过或改名时两者都停下并报出文件与摘要，改动要走新的迁移；回滚到不认识这张表的旧版本不受影响，它只多看到一张启用行级安全的表（[platform 的「迁移」](../../internal/platform/README.md#迁移)）。

### 决策与理由

选择「CI 一次构建 + OCI 原件 + 本地 registry 按 digest 演练」，而不是「部署机按提交重建」或「直接发布外部 registry」。
重建不能保证工具链和依赖环境一致；外部上传涉及费用与凭证且尚未获准。本地 registry 只用于证明与真实部署相同的 digest
拉取路径；CI 结束删除 registry 和独占 builder，交付物由 Actions artifact 留存。代价是多一个临时 registry 和 BuildKit 容器，
以及负责人发布前的复制步骤；不新增常驻基础设施、不决定生产 registry。

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
