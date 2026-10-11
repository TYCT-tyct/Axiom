# 文档地图

每个主题只由一份文档负责。找信息从这里开始；新增文档必须登记在这里，CI 会检查。

## 现有文档

| 我要知道 | 读 | 谁、什么时候读 |
|---|---|---|
| Axiom 是什么 | [README.md](../README.md) | 第一次接触项目的人 |
| 产品需求：做什么、给谁、和 OpenRouter 有何不同、售卖形式、端点属性、范围与待定问题 | [product.md](product.md) | 设计或开发任何功能之前 |
| 术语与核心概念：统一叫法、代码命名、概念之间的关系 | [glossary.md](glossary.md) | 写文档、写代码、给东西起名之前 |
| 总体架构：模块划分、部署拓扑、调用链路、技术选型、工程原则、底线指标 | [architecture.md](architecture.md) | 新建模块、跨模块改动、选依赖、做部署之前 |
| 账户与租户：登录、账户、工作区、授权、API key、等级、站点配置、配置链 | [domains/accounts.md](domains/accounts.md) | 改动 `accounts` 模块或涉及登录、权限、key 之前 |
| 计费：钱的表示、汇率、额度批次、供应商成本、定价、冻结与结算、免单、账本 | [domains/billing.md](domains/billing.md) | 改动 `billing` 模块或任何涉及钱的逻辑之前 |
| 模型目录：标准 ID 与别名、生命周期、名字映射、目录基线、价目表结构 | [domains/catalog.md](domains/catalog.md) | 改动 `catalog` 模块或涉及模型名字、官方价格之前 |
| 供应商：适配器、接入流程、凭证、同步、号池 | [domains/suppliers.md](domains/suppliers.md) | 改动 `suppliers` 模块或接入新的供应商、中转系统之前 |
| 端点评估：检测、线上观测、外部评测、属性合并、熔断 | [domains/quality.md](domains/quality.md) | 改动 `quality` 模块或新增检测项之前 |
| 路由：候选与过滤、排序策略、会话粘性、回退与重试、容量感知 | [domains/routing.md](domains/routing.md) | 改动 `routing` 模块或调整路由策略之前 |
| 接入与对外接口：路径与格式、转发规则、用量与费用、错误、路由参数、对接接口、会话状态、版本 | [domains/gateway.md](domains/gateway.md) | 改动 `gateway` 模块、对外接口或兼容行为之前 |
| 怎么参与开发：流程、提交、评审、文档规则 | [CONTRIBUTING.md](../CONTRIBUTING.md) | 每个开发者（人或 AI）动手之前 |
| AI 协作者的专属规则 | [AGENTS.md](../AGENTS.md) | AI 每次会话开始时（Claude Code 经 `CLAUDE.md` 自动加载） |

## 文档怎么组织

文档按领域组织，不按阶段：一个领域的要求、设计和决策写在同一份文档里，不拆成需求文档和设计文档两份，以免同一主题散在两处、互相对不上。领域的划分与系统的模块划分一一对应，以后也是代码目录的划分：改哪个模块，就改对应的那份文档。

| 内容 | 位置 |
|---|---|
| 全局需求：定位、用户、市场、范围、质量目标 | `docs/product.md` |
| 术语与核心概念：全项目统一的叫法和代码命名 | `docs/glossary.md` |
| 全局设计：模块划分、请求链路、技术选型、部署、工程原则 | `docs/architecture.md` |
| 单个领域的要求和设计，领域清单以架构文档的模块划分为准 | `docs/domains/<领域>.md`，固定分四节：要求、设计、决策与理由、待定问题 |
| 对外接口、数据库结构等机器可读的契约 | 契约文件本身，位置由架构文档规定 |
| 一个模块的代码结构、接口、必须遵守的规则、测试方法 | 模块目录下的 `README.md`，改该模块代码前必读 |
| 部署、发布、监控、故障处理 | `docs/ops/` |
| 给客户看的使用文档 | `docs/guides/` |

文档写什么、怎么维护，见 [CONTRIBUTING.md 的文档规则](../CONTRIBUTING.md#文档规则)。
