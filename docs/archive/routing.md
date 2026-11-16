# 路由的存档

[路由](../domains/routing.md)的调研基线、方案对比和推导。这里不是现行规则：行为以领域文档和代码为准，本文只说明当初为什么这样定。

## 调研基线

- OpenRouter（2026-11-02 核对）没有「把路由预设挂在 API key 上」的对象：请求预设（`@preset/`）可以包含 provider 路由，但按请求引用；workspace 有自己的路由默认（[Workspaces](https://openrouter.ai/docs/guides/features/workspaces)），guardrail 把 provider、model 允许列表和预算分到 key（[Guardrails](https://openrouter.ai/docs/guides/features/guardrails)），请求再用 `provider` 覆盖（[Provider Routing](https://openrouter.ai/docs/guides/routing/provider-selection)）；创建 key 的接口没有 preset 字段（[Create API key](https://openrouter.ai/docs/api/api-reference/api-keys/create-a-new-api-key)）。Axiom 把预设绑在 key 上，是为不能加请求参数的工具做的差异。
- new-api 的 token 只有分组、模型限制和自动分组，sub2api 的 API key 只有分组、IP 和额度，都没有筛选/排序预设（2026-11-02）。
- OpenRouter 的 BYOK 不豁免 `data_collection` 限制，优先区也在请求 `order` 之后（[BYOK](https://openrouter.ai/docs/guides/overview/auth/byok)，2026-11-02）；它另有按 key 的 ZDR 声明，Axiom 没有。OpenRouter 按 provider 关闭共享容量（「Never use shared capacity」），Axiom 按模型关闭。
- 命名预设对照 [OpenRouter Presets](https://openrouter.ai/docs/guides/features/presets) 和 [List presets](https://openrouter.ai/docs/api/api-reference/presets/list-presets)（2026-10-29）；没做版本历史、指定版本和从推理请求建版本的端点。
- 默认负载均衡、`sort`、`:floor`、`:nitro` 对照 OpenRouter《Provider Routing》（2026-10）；性能阈值的百分位对照其「Performance Thresholds」（2026-11-01）；工具调用重排对照《Auto Exacto》（2026-10），它也指出重排会和粘性冲突；粘性只在缓存读更便宜时启用对照《Prompt Caching》（2026-10）；跨模型回退对照 [Model Fallbacks](https://openrouter.ai/docs/guides/routing/model-fallbacks)。
- `metadata.user_id` 的结构与 E2-6 共用：JSON 字符串内含分别派生的 `device_id`、`session_id` 和空 `account_uuid`；参考 sub2api [ParseMetadataUserID](https://github.com/Wei-Shaw/sub2api/blob/a3298b46ebe239fb0720b068a1eb3383955bdac2/backend/internal/service/metadata_userid.go#L35) 和 [GenerateSessionHash](https://github.com/Wei-Shaw/sub2api/blob/a3298b46ebe239fb0720b068a1eb3383955bdac2/backend/internal/service/gateway_service.go#L901)（2026-10-29）。
- 供应商余额：OpenRouter 没公布余额算法，本地预付余额检查是 Axiom 的加强。[new-api 余额扫描](https://github.com/QuantumNous/new-api/blob/e449cc700eb569cdfc2199cb73e97de46944f43c/controller/channel-billing.go#L757)在余额非正时经 [DisableChannel](https://github.com/QuantumNous/new-api/blob/e449cc700eb569cdfc2199cb73e97de46944f43c/service/channel.go#L29) 自动停用渠道；[sub2api](https://github.com/Wei-Shaw/sub2api/blob/a3298b46ebe239fb0720b068a1eb3383955bdac2/backend/internal/service/ratelimit_service.go#L433) 把 402 归为账号错误并停止调度。缺项按最高已知 token 单价估算，是因为 new-api 的计划只有标准 token 价格，目录却可能声明其他等级、音频或搜索。
- `Retry-After`：[sub2api](https://github.com/Wei-Shaw/sub2api/blob/a3298b46ebe239fb0720b068a1eb3383955bdac2/backend/internal/service/ratelimit_service.go#L2504) 支持秒数和 HTTP 日期，通用 429 上限 7200 秒，没给时 `defaultRateLimit429CooldownSeconds = 5`；[new-api](https://github.com/QuantumNous/new-api/blob/e449cc700eb569cdfc2199cb73e97de46944f43c/middleware/rate-limit.go#L136) 发秒数，对 429 不停用渠道。
- 换不换端点：sub2api 不为上下文超长和内容策略拒绝换账号，却为请求体过大换（`openai_gateway_upstream_errors.go` 的 `shouldFailoverOpenAIUpstreamResponse`，a3298b46e）；new-api 停用出错的渠道再试另一个。sub2api 出现过空流反复重试的风暴（issue #7895），这是按供应商设重试预算的原因。
- 粘性续期：new-api 成功后 `SetWithTTL`（`service/channel_affinity.go`，7d8b912），sub2api `RefreshSessionTTL`（`gateway_cache.go`，dba770d56），都续期；Axiom 不续期。

## 路由预设的方案对比

| 方案 | 做法 | 为什么不选 |
|---|---|---|
| A. 规则直接写在 key 上 | 每把 key 存完整规则 | 产品要命名预设、平台预设和复用；改一处要改所有 key |
| B. 各层存规则片段，请求时合并 | 各层存部分规则，取最紧 | 生效规则是算出来的，难展示、难审计；上层改片段悄悄改所有 key，下层可能误放宽 |
| C. 命名预设 + 层默认指针 + key 落章（选） | 预设是完整规则，每层指向一个，key 创建时写解析结果 | 代价：为平台基线加小改动要复制一份 |
| D. 请求按名字另选预设 | 请求可命名任意可见预设 | 请求能把管理员设的严格预设换成宽松的；`provider` 已覆盖会写代码的用户 |

## 推导

- **粘性不续期**：同一端点每秒服务一次、持续一小时的确定性序列，固定到期只需首次写入，逐次续期要 3600 次写入；一个账户 10 个这样的会话就用尽每秒 10 次的后台写预算。这是工作量计算，不是线上命中率；`TestStickyDoesNotRenewOnUse` 用连续 100 次已交付请求检查原 TTL 不变。代价是超过一小时的会话到期后可能重新选择、损失一次提示缓存。
- **受众价格的开销**：50 候选基准中，快照排名约 12–15 µs、3752 B、5 次分配；准入按模型只算一次派生价并复用预分配视图，约 14–15 µs、4280 B、27 次分配。
- **预设的开销**：`BenchmarkPlanPreset` 下 50 个候选在 100 条规则的预设里不比放行预设多分配；`just bench-compare` 下 `BenchmarkPlan` 与引入预设前同为 11 次分配。
- **模型去重**：#537 发现准入事务里对每个模型扫一遍已见列表，耗时随模型数平方增长；`TestPlanScalesAcrossModels` 比较 100 与 6,400 个模型的最快耗时之比，超过 300 倍即失败。
- **全局预算**：10 万条不能都按 16 KiB 估，那已经约 1.6 GiB，所以另设字节预算；上限封顶为默认的十倍和八倍。
- **按 key 数的索引**：`api_keys_by_preset` 是 `(preset_id, account_id) INCLUDE (site_id)`，只含未删除且不是零 ID 的行；预设在前，管理端按预设数所有账户，控制台再按账户取一段；带上 `site_id`（读策略要用）计数才是只读索引的扫描（`TestPresetKeys` 核对计划）。
- **容量视图**：按端点 ID 查供应商的当前快照，而不是在路由里复制计数或把 `MaxConcurrent` 设成凭证份额之和：份额每秒随成员变化，休息和账号分钟在调用之间变化，复制的数字会过期；代价是每次计划多一次原子读和一次凭证扫描。
- **「最快」的冷启动**：实测接通前所有端点的吞吐都是 1，「最快」实际按端点 ID 排，稳定偏向某一家（#313 E3-16），因此引入「没测过排后 + 5% 探索 + 30 分钟过期」。

## 分步落地时的混跑规则

路由预设按十几步落地（#562），每一步都要求集群里没有比第一步更旧的节点、回滚目标不早于第一步：第一步起每个节点都读表并按默认链落章，第一步时落章只会写零 ID，比它旧的节点按代码里的零号路由同一个 ID，所以混跑不改变路线；比第一步更旧的二进制遇到非空非零的 `preset_id` 回通用 503 `overloaded`，因此不能作回滚目标。其后各步只新增表、接口和可选字段：旧节点不读新表、按代码默认检查配额；`/api/v1/keys` 的新成员在旧节点上整份 400；新页面碰到旧节点时缺的字段按「未知」显示、缺的接口说明节点不支持。kill switch 要等所有节点更新后再用，暂停中要回滚先恢复。服务还没有首次生产部署，这些混跑规则只在部署前有意义。

## 多出口的选择

现在只有 `direct` 一个出口。设计：每个端点附带一份按实测线路延迟和成功率排的出口列表（见[端点评估](../domains/quality.md)）；凭证绑定了固定出口就用那个，绑定的出口不可用时换这个端点的另一个凭证；没绑定的用排序第一的出口。先挑凭证再定出口，因为凭证可能绑定出口，出口故障不该冤枉端点。

## 移出领域文档的决策

| 决策 | 理由 |
|---|---|
| 一个预设内按作者、模型分规则，不是每个模型绑一个预设 | 产品说预设挂在 key 上（单数）；逐层收紧在一个对象里校验和执行都更简单 |
| 工作区权限沿用 `api_keys.view`/`api_keys.manage` | 账户只有个人成员关系，没有按工作区授予 |
| 上限的改动带 `rev`，清空不删行 | 整体替换没有修订号时后保存的人会悄悄覆盖前一个（[platform 的约定](../../internal/platform/README.md)） |
| 版本保留数不低于 50 | 表的删除策略保证当前版本往前 50 版都在；50 版每个预设最多 800 KiB，没必要调低 |
| 上限、暂停开关各自单独一行 | 预算行任何范围都要写，上限和开关只许平台写；开关和上限分开，上限的 `rev` 才只代表上限 |
| 平台、站点各 50 条活预设、500 条累计 | 一把 key 能选的最多 130 条，页面能整表展示 |
| 来源类别的勾选项从节点内存读 | `quality.source_classes` 里有识别中转站的指纹规则，不给租户看；节点已每 5 秒读进内存 |
| 管理端的列和建按层分路径，读、改、停用共用按 ID 的路径 | 预设 ID 全局唯一、层不变；列和建分路径后漏写站点不会落到更宽的平台层 |
| 写入时不拒收找不到端点的规则 | 端点集合每 5 秒在变，平台也要先建预设再接端点；检查只描述现在 |
| 检查按层的视野判断端点（`Owner.visibleTo`） | 平台的检查只看平台端点，站点加本站，账户加本账户，不把别人的端点算进来 |
| 404 里预设排除过端点就算预设拒绝 | 预设检查排在格式、能力和上下文之后，被它排除的端点本来能服务这次调用 |
| 命名配置按 jsonb 文本表示校验大小 | 数据库会展开指数数字，只校验客户端 JSON 会放过超限的 |
| 请求的名称选择用分组代号，不在准入查供应商名 | 查名要多一次数据库往返、暴露真实名称，并把分组扩大成整家供应商 |
| 工作区归属在 `cmd` 经 `accounts.Service.Workspace` 核对 | routing 的表不引用 accounts，表的策略挡不住本账户里不存在或已删除的工作区 ID |
| 无效默认不回退到更宽的层 | 回退会让新 key 静默落到更宽的限制 |
| 暂停时的写回 503 `presets_suspended` | 这是平台主动停的服务，不是冲突；单独的码让页面说清原因和下一步 |
| 不设站点开关 | 产品要求用户都能用预设；要让站点强制路由，需要的是强制策略层（见「待定问题」） |
| 只告警平台和站点的预设 | 账户和工作区的预设由用它的人自己建，直接收到 404；平台和站点的预设只有运营能修 |
| 调用记录只记预设 ID、版本和覆盖的成员名 | 规则和值能从版本表和请求复现，复制进记录会撑大 8 KiB 的 `route` 列 |
| 候选都没测过时「最快」照常服务，记 `fastest_unmeasured` | 能服务的请求不因排序依据缺失而失败，端点也才能得到测量 |
| 号池容量低时按分组把权重减半 | 余量是分组共用的；减半只降低概率，恢复后自动加回，不用人改配置 |
| 工作区层的接口在不指定工作区时按默认工作区 | 控制台的 key 都建在默认工作区；要求每个请求带工作区 ID 会让现有页面全部改动 |
| 熔断与暂停是两种状态，熔断不调用 `Block` | 暂停到点就回来，熔断要试探通过；合成一种，要么试探通过还得等暂停，要么上游的等待时间被提前打破 |
