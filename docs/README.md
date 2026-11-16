# 文档地图

每个主题只由一份文档负责。找信息从这里开始；新增文档必须登记在这里，CI 会检查。

## 现有文档

| 我要知道 | 读 | 谁、什么时候读 |
|---|---|---|
| Axiom 是什么 | [README.md](../README.md) | 第一次接触项目的人 |
| 产品需求：做什么、给谁、和 OpenRouter 有何不同、售卖形式、端点属性、范围与待定问题 | [product.md](product.md) | 设计或开发任何功能之前 |
| 与 OpenRouter 的能力对照：OpenRouter 的每个接口和行为、Axiom 现状、参考实现位置、缺口与归属 | [openrouter-parity.md](openrouter-parity.md) | 设计或改动任何对外接口、兼容行为之前；排 M2/M3 的先后时 |
| 术语与核心概念：统一叫法、代码命名、概念之间的关系 | [glossary.md](glossary.md) | 写文档、写代码、给东西起名之前 |
| 总体架构：模块划分、部署拓扑、调用链路、技术选型、工程原则、底线指标 | [architecture.md](architecture.md) | 新建模块、跨模块改动、选依赖、做部署之前 |
| 性能：延迟预算、调用路径的规则、后台与数据、规模与扩展、基准测试与压测 | [performance.md](performance.md) | 改动调用路径、流式转发、格式转换或大表查询之前；压测和扩容之前 |
| 安全：威胁模型、安全基线、租户隔离、防滥用、账户安全、密钥管理、安全开发流程 | [security.md](security.md) | 改动鉴权、计费、出站请求或任何安全相关的代码之前 |
| 前端与设计：页面清单、验收门槛、可用性任务、前端性能指标、设计原则、主题与自由度、视觉方向 | [frontend.md](frontend.md) | 做任何页面、组件、主题或站点外观配置之前；调整前端性能预算之前 |
| 前端接口指南：只靠 HTTP 接口做客户端——主机与路径、登录与会话、CSRF、错误与请求 ID、分页、幂等键、金额与时间、版本提示、限流、页面必须取自接口的数据、本机开发 | [frontend-api.md](frontend-api.md) | 做或替换任何前端（官网、控制台、管理后台）之前；对接接口的约定之前 |
| 账户与租户：登录、账户、工作区、授权、API key、等级、站点配置、给用户的邮件、配置链 | [domains/accounts.md](domains/accounts.md) | 改动 `accounts` 模块或涉及登录、权限、key 之前 |
| 计费：钱的表示、汇率、资金来源、额度批次、供应商成本、定价与售价、预留与结算、用量来源与暂估、免单、账本 | [domains/billing.md](domains/billing.md) | 改动 `billing` 模块或任何涉及钱的逻辑之前 |
| 计费在调用路径上的代码接口：预留、计量、追加、结算、持有者与截止时间、预留的状态、写入的数据 | [internal/billing/README.md](../internal/billing/README.md) | 实现或调用 `billing` 的预留与结算之前 |
| 模型目录：标准 ID、公开与内部名字、名字解析、生命周期、规格与参考价的来源、价目表结构 | [domains/catalog.md](domains/catalog.md) | 改动 `catalog` 模块或涉及模型名字、官方价格之前 |
| 账户在代码里怎么认出、准入和授权：站点快照、会话、key、准入事务、公开接口 | [internal/accounts/README.md](../internal/accounts/README.md) | 实现或调用 `accounts` 之前 |
| 模型目录在代码里怎么加载、同步和发布快照：参考价、名字解析、价目表 | [internal/catalog/README.md](../internal/catalog/README.md) | 实现或调用 `catalog` 之前 |
| 三种调用格式的转换与透传：解析、转换、流式读写、用量 | [internal/protocol/README.md](../internal/protocol/README.md) | 实现或调用 `protocol` 之前 |
| 平台层：监听与接口约定、日志与遥测、数据库访问、加密、前端文件、配置 | [internal/platform/README.md](../internal/platform/README.md) | 改动 `platform`、`cmd/axiom` 的组装或任何跨模块的基础设施之前 |
| PostgreSQL 行级安全：范围表达式、策略迁移、行数估计与分页计划的实测 | [internal/platform/pg/README.md](../internal/platform/pg/README.md) | 改写行级安全策略或排查范围过滤与查询计划之前 |
| 对象存储：S3 客户端、配置、期限与重试、SeaweedFS 测试 | [internal/platform/objectstore/README.md](../internal/platform/objectstore/README.md) | 写归档、备份副本或对话正文之前 |
| 多个核心节点共用的计数与粘性：Valkey 里放什么、客户端与连接、键、期限与熔断、每种用途的兜底与恢复、部署、测试、PR 拆分 | [internal/platform/kv/README.md](../internal/platform/kv/README.md) | 改动限流、登录防爆破、会话粘性，或部署 Valkey 之前 |
| 前端工程：目录、开发命令、共享包、性能预算、测试 | [web/README.md](../web/README.md) | 改动 `web/` 之前 |
| 供应商：适配器、接入流程、供应商别名与待映射、凭证、同步、号池 | [domains/suppliers.md](domains/suppliers.md) | 改动 `suppliers` 模块或接入新的供应商、中转系统之前 |
| 供应商在调用路径上的代码接口：挑凭证、发请求、记尝试 | [internal/suppliers/README.md](../internal/suppliers/README.md) | 实现或调用 `suppliers` 的调用面之前 |
| 端点评估：检测、线上观测、外部评测、属性合并、熔断 | [domains/quality.md](domains/quality.md) | 改动 `quality` 模块或新增检测项之前 |
| 端点评估在调用路径上的代码接口：判定表、`Observe`、健康度与实测的延迟和吞吐 | [internal/quality/README.md](../internal/quality/README.md) | 实现或调用 `quality` 之前，或改动尝试的结束路径之前 |
| 路由：挂 key 的路由预设与默认链、候选与过滤、排序策略、会话粘性、回退与重试、容量感知 | [domains/routing.md](domains/routing.md) | 改动 `routing` 模块或调整路由策略之前 |
| 路由在调用路径上的代码接口：会话标识、回退路线、换不换端点 | [internal/routing/README.md](../internal/routing/README.md) | 实现或调用 `routing` 的路由接口之前 |
| 接入与对外接口：路径与格式、转发规则、用量与费用、错误、路由参数、对接接口、会话状态、版本 | [domains/gateway.md](domains/gateway.md) | 改动 `gateway` 模块、对外接口或兼容行为之前 |
| 联网搜索：一次查询、SearXNG 适配器、配置与覆盖、结果上限、失败与测试 | [internal/websearch/README.md](../internal/websearch/README.md) | 改 `web` 插件、换搜索后端或部署 SearXNG 之前 |
| 文件解析：一次转换、Docling serve 适配器、配置与覆盖、大小上限、失败与测试 | [internal/fileparse/README.md](../internal/fileparse/README.md) | 改 `file-parser` 插件、换解析后端或部署 Docling 之前 |
| 一次调用在代码里怎么走：不变量、步骤、所有权、期限、并发与背压、故障矩阵、性能估算、PR 拆分 | [internal/gateway/README.md](../internal/gateway/README.md) | 实现或改动调用路径上任何模块之前 |
| 运营：分工、agent 的组成与模型来源、运营接口与运营凭证、MCP、风险分级与审批、累计资金上限、审计与回滚、通知、提交 Issue | [domains/operations.md](domains/operations.md) | 改动 `operations` 模块、运营 agent 或审批策略之前 |
| 运营在代码里怎么存业务目标、操作记录、运营操作与审批、资金上限与售价包络：表与索引、月度与折算、幂等与到期、锁的顺序、读写与审计 | [internal/operations/README.md](../internal/operations/README.md) | 实现或调用 `operations` 的业务目标、操作记录或审批之前 |
| 记录与告警：记录范围、分类标签、分层存储、保留、受控导出、监控告警、通知渠道 | [domains/observability.md](domains/observability.md) | 改动 `observability` 模块、记录或告警之前 |
| 调用记录在代码里怎么写与读：表、有界队列与背压、批量写与重试、用户读自己的记录、分区 | [internal/observability/README.md](../internal/observability/README.md) | 实现或调用 `observability` 的调用记录之前 |
| 支付：收款渠道与商户账户、订单状态、金额与汇率、手续费、回调与到账、补单、加密货币、退款与拒付、防欺诈、对账、后台配置、收据 | [domains/payments.md](domains/payments.md) | 改动 `payments` 模块或接入新的收款渠道之前 |
| 支付在代码里的接口：渠道接口、billing 要提供的函数、不变量、表与行级安全、事务与任务、PR 拆分 | [internal/payments/README.md](../internal/payments/README.md) | 实现或调用 `payments`、写新的收款渠道插件之前 |
| CI：作业与路径门禁、运行池、runner、缓存、测试与部署容器、诊断 | [ops/ci.md](ops/ci.md) | 改 CI、runner、缓存或排查构建之前 |
| 合并队列：怎么排队、出队，标签的含义，列车怎么构建、找出元凶、落地和恢复，失败的列车怎么看，不稳定测试清单 | [ops/merge-train.md](ops/merge-train.md) | 给 PR 加 `queue` 之前；列车失败或卡住时 |
| 上线前的安全审查：每次查了什么、怎么查、发现了什么、哪些没覆盖 | [ops/security-review.md](ops/security-review.md) | 做上线前审查、复核审查结论、接手遗留发现时 |
| 备份与恢复：一次备份包含什么、停写与调用日志、加密与收件人、上传与保留、单实例、空白环境恢复、恢复演练、生产入口 | [ops/backup-restore.md](ops/backup-restore.md) | 配置或检查备份、恢复一台机器、改生产入口之前 |
| 发布前：目录规格要补全（请求写法的键怎么写进库、怎么检查），发布前的其他确认 | [ops/release.md](ops/release.md) | 发布或部署之前 |
| 示例法律文本（demo）：服务条款、隐私政策、OpenRouter 对照、在开发站点发布 | [legal/README.md](legal/README.md) | 准备演示条款/隐私政策、在开发站点发布 demo 版本、负责人审阅前 |
| 错题：失效机制、现有回归与尚缺的检查 | [lessons.md](lessons.md) | 复盘时；评审发现同类问题再次出现时 |
| 通用开发规则：五条硬约束、记录、对外文字、安全；由 project-workflow 管理 | [AGENTS.md](../AGENTS.md) | 每个开发者（人或 AI）动手之前；AI 每次会话开始时（Claude Code 经 `CLAUDE.md` 自动加载） |
| 本项目的补充：角色、谁拍板、产品设计基线、任务与一个 PR、评审、编排（模型清单、任务大小、循环上限）、合并队列与迁移、CI 与检查、验收与关账、代码质量标准、检查清单、审计与发布、重新讨论的条件 | [CONTRIBUTING.md](../CONTRIBUTING.md) | 同上 |

## 存档

推导、调研和被否决方案的细节，不进任务卡和阅读清单；与现有文档冲突时以现有文档为准。

| 内容 | 文档 |
|---|---|
| 端点评估的真伪信号比较与熔断概率推导 | [archive/quality.md](archive/quality.md) |
| 平台层：测试设施、现有指标、出站地址策略、密钥与信封、连接池与迁移的推导、接口约定的取舍 | [archive/platform.md](archive/platform.md) |

## 文档怎么组织

文档按领域组织，不按阶段：一个领域的要求、设计和决策写在同一份文档里，不拆成需求文档和设计文档两份，以免同一主题散在两处、互相对不上。领域的划分与系统的模块划分一一对应，以后也是代码目录的划分：改哪个模块，就改对应的那份文档。

| 内容 | 位置 |
|---|---|
| 全局需求：定位、用户、市场、范围、质量目标 | `docs/product.md` |
| 术语与核心概念：全项目统一的叫法和代码命名 | `docs/glossary.md` |
| 全局设计：模块划分、请求链路、技术选型、部署、工程原则 | `docs/architecture.md` |
| 跨模块的安全规则 | `docs/security.md` |
| 跨模块的性能规则 | `docs/performance.md` |
| 前端与设计：三类页面的要求、主题与视觉规则 | `docs/frontend.md` |
| 单个领域的要求和设计，领域清单以架构文档的模块划分为准 | `docs/domains/<领域>.md`，固定分四节：要求、设计、决策与理由、待定问题 |
| 对外接口、数据库结构等机器可读的契约 | 契约文件本身，位置由架构文档规定 |
| 一个模块的代码结构、接口、必须遵守的规则、测试方法 | 模块目录下的 `README.md`，改该模块代码前必读 |
| 部署、发布、监控、故障处理 | `docs/ops/` |
| 给客户看的使用文档 | `docs/guides/` |

文档写什么、怎么维护，见 [AGENTS.md 的「文档」](../AGENTS.md#记录)；细则在 project-workflow skill 的文档写法（`references/docs.md`）。
