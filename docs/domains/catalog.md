# 模型目录

有哪些模型、叫什么名字、官方规格和参考价是什么。对应代码模块 `catalog`，数据、不变量和接口见[模块说明](../../internal/catalog/README.md)。概念之间的关系见[术语与核心概念](../glossary.md)。

## 要求

- 模型的标准 ID 采用 OpenRouter 的 `厂商/模型` 格式，厂商原生的名字也能直接使用。
- 目录基线自动同步，主要参考 OpenRouter，并与其他公开来源交叉核对。
- 价格一律按原价计算，不考虑任何优惠。
- 按上下文长度变化等复杂价格必须正确获取、正确计算。

## 设计

### 模型与名字

- **模型**：后台和日志里用 `厂商/模型` 标识，厂商给出版本标识时加 `@版本`，如 `anthropic/claude-sonnet-4.5@20250929`。
- **版本依据**：每个模型声明一种。
  - **固定版本**：厂商的带日期名字或页面写明的版本号，附证据。模型不可变，厂商下线那个版本时它才下线。
  - **移动名字**：厂商的名字背后版本会变，例如 DeepSeek 页面写明旧名 `deepseek-v4-flash` 的请求由 DeepSeek-V4.1-Flash 服务（DeepSeek 价格页，2026-10-14）。模型就是这个名字本身：厂商换版本时原地改规格、发布新的参考价，并在审计里记下版本标识的变化。
- **名字**分两类：

| | 公开名字 | 内部名字 |
|---|---|---|
| 谁用 | 用户请求、模型列表、回退列表 | 供应商的名字映射、数据集条目的对应、日志 |
| 第一版允许 | 标准 ID；厂商原生的名字，带日期的（`-YYYYMMDD`、`-YYYY-MM-DD`、`-MMDD`、`-MM-DD`，如 `claude-sonnet-4-5-20250929`、Google 的 `gemini-2.5-pro-preview-06-05`）和不带日期的（如 `claude-sonnet-4-5`、`gemini-2.5-pro`）；免费条目的 `标准 ID:free` | `-latest` 结尾的写法 |
| 永不允许 | `-latest`、含 `~` `@` 的写法，以及 `:free` 之外的冒号 | 含 `~` 的写法，以及 `:free` 之外的冒号 |

- 中转站里的写法，以及 `:batch` 这类后缀，是[供应商别名](suppliers.md#供应商别名与待映射)，不进目录。`:free` 是例外：OpenRouter 把它当独立模型，有独立的规格、上下文、端点和价格，所以本站也建一个独立模型，标准 ID 和公开名字都是 `基础标准 ID:free`，价格为零。
- `~作者/系列-latest` 是目录里的**别名表**，一行一次 `(名字, 目标模型)` 追加，快照读最近一次：运营可以在同一系列的新版本上架后把别名指过去，`change` 和审计留下改前改后。别名不按前缀或日期猜目标，避免 `acme/gpt-5` 与 `acme/gpt-50` 串族，也避免新模型上架时悄悄改变用户实际调用的模型。
- **名字归一化后全局唯一**，公开和内部名字一起算：归一化结果已被另一个名字占用时拒绝保存。
- **归一化**：去首尾空白 → ASCII 小写 → `.`、`_`、空白改成 `-` → 合并连续的 `-`，去掉 `/` 两侧的 `-` → 结尾的 `-YYYY-MM-DD` 改写为 `-YYYYMMDD`。不去日期、不去厂商前缀、不猜版本号，所以不同日期的版本不会被合并。
- **同一版本的两个名字**：厂商同时给出不带日期和带日期的名字时（如 `claude-sonnet-4-5` 与 `claude-sonnet-4-5-20250929`），建一个固定版本的模型，标准 ID 之外再挂这两个公开名字。厂商把不带日期的名字换到新版本后，它仍指向旧版本，直到「切换与迁移」实现。

### 名字解析

- **用户请求**：只查公开名字和别名，归一化后查找。名字精确匹配，不切后缀：`:free` 命中它自己的条目，没有该条目就是"模型不存在"，永远不会落到基础模型；`:batch` 等其他后缀同样不存在。`:nitro`、`:floor` 这类路由变体不是目录的事：网关在查目录之前读掉它们，目录收到的是不带后缀的名字，变体从不新建模型（见[接入与对外接口的「模型名变体」](gateway.md#模型名变体)）。`~作者/系列-latest` 查别名表的最新版本，返回它指向的具体模型；目标下线时返回"已下线"。模型详情和 endpoints 查询也先经网关解析路由后缀，再查同一个公开名字。已下线模型的名字返回"已下线"。
- **内部识别**：供应商的名字映射和数据集核对用。公开和内部名字都查，并说明这个名字能否当作版本证据：带日期的名字、移动名字的模型的名字能；绑定到固定版本模型的不带日期名字不能，因为厂商可能已把它换到新版本。不能当证据的结果，数据集核对不采用，供应商要经真伪检测确认（见[供应商的「供应商别名与待映射」](suppliers.md#供应商别名与待映射)）。

### 模型的生命周期

- **状态**：草稿 → 上架 → 计划下线（带下线日期）→ 已下线。草稿只在平台范围可见。计划下线、下文的切换与迁移、预约改价都在 M1 之后再做（负责人决定，[Issue #24](https://github.com/TYCT-tyct/Axiom/issues/24)）；M1 只有草稿、上架和已下线，名字上架后不改指向，下线即刻生效，改价在发布时生效。
- **新模型**：同步或页面监测发现新模型时开一个审核项。上架时由审核者确定标准 ID、公开和内部名字、版本依据和价格来源，并发布首个参考价。上架的模型能否被调用，还要过计费的上架门槛：上游可能报告的每个计费项都有售价（见[计费的「定价」](billing.md#定价)）。
- **切换与迁移**：不带日期的名字换版本、下线模型的名字迁移到替代模型，都会改变用户实际用的模型和价格：
  - 切换前通知受影响的用户（邮件的收件人、模板和站点品牌见[账户与租户的「给用户的邮件」](accounts.md#给用户的邮件)）；
  - 新版本或替代模型更贵、能力更低时，默认需要人确认；
  - 切换时刻之后开始的请求按新模型的价格版本计费，之前开始的请求不受影响；
  - 切换记入审计。

### 目录基线

- **内容**：上下文长度、最大输出、输入输出类型、能力、支持的参数、知识截止时间、参考价；以及计费要用的：上游可能报告哪些计费项和服务等级（按输入输出类型、工具和厂商提供的服务等级确定，审核时确认），各调用格式的流里是否显示推理进度（`reasoning_visible`）、单张图片和单页文件的最大输入 token 数、单个文件的最大页数、最快输出速度（`output_speed`，每秒 token），计费据此检查上架门槛、决定能否分段预留、算输入上界和超额额度（见[计费的「预留与结算」](billing.md#预留与结算)）。这些是可缺的模型数据，缺了就是不画一条上界或不分段，不猜一个值；`reasoning_visible` 列了格式而规格的 `abilities` 没有 `reasoning` 时保存被拒。
- **服务端工具默认次数**：`model.spec.max_uses` 是管理员经 `UpdateSpec` 修改的数据，键为 `web_search`、`code_execution`、`file_search`，值为 1 … 10¹⁰ 的整数；未写或 null 表示没有默认。`file_search` 的键和它的能力、计费项一样，受 `catalog.Config.FileSearch` 控制，旧节点读不了这些名字。报价优先用请求上限，否则用这份数据；没有任何上限仍拒绝。只有确认该模型的所有可路由端点都保证此上界时才填写，不能把搜索结果条数当作调用次数。现有 JSON 规格可直接存储，不需要迁移。网关给插件的 `engine:"native"` 打开厂商搜索时，用 `max_uses.web_search` 作 `max_uses`，没有就用 1，并夹在 1 到 10 之间（[gateway 的「服务端工具」](gateway.md#服务端工具)）。
- **每次调用都会搜索的模型**：`model.spec.always_searches` 是布尔值，写 `true` 表示模型不靠工具、每次调用都自己联网搜索（例如 Perplexity 的 sonar）。网关据此丢掉 `web` 插件，既不再执行一次后端搜索，也不收插件费，避免一次请求搜索两次；不写或 `false` 表示没有这个保证。它是规格数据，只有确认该模型的所有可路由端点都这样时才填。
- **数据集**：
  - OpenRouter 的模型接口：每个模型的价格和规格，无需 key 即可读取（OpenRouter 网站的定价页，2026-10）；
  - models.dev：开源模型数据库，用 `tiers` 表达分档价格（models.dev，2026-10）；
  - LiteLLM 的模型价格表：用 `input_cost_per_token_above_200k_tokens` 这类字段表达超过 20 万 token 后的价格（LiteLLM 仓库，2026-10）；
  - 厂商自己的模型列表和价格文档。
- **规格以厂商文档为准**：上下文长度、能力等规格，厂商文档是最终依据；各来源一致才自动更新，不一致不自动采用，先告警，交给运营 agent 判断，必要时请人确认。规格原地更新，旧值留在审计里。规格的核对与价格来源无关：选了哪个价格来源，都不改变规格以谁为准。
- **请求写法的键照厂商文档，由数据文件写入**：规格里说请求怎么写给这个模型的键（思考方式、对话中途的系统消息、非默认采样、强制调用工具、缓存断点、加密推理）缺了不是 no，转换遇到要用它的请求就拒绝，所以已进目录的模型必须写全。值来自厂商文档，逐型号写在 `deploy/catalog/dialects.json`，由 `axiom catalog apply-dialect` 经审计的规格更新写入，`axiom catalog check` 报缺键的模型；每个值的出处和日期、各键的含义见[目录模块的「请求写法的键」](../../internal/catalog/README.md#请求写法的键值照厂商文档不手填)，发布前的步骤见[发布前](../ops/release.md)。
- **价格以模型选定的来源为准**（负责人决定，Issue #24）：
  - 每个模型选一个价格来源：LiteLLM 价格库、OpenRouter、官方价格页或手填。选定来源表达不了这个模型的分档时（OpenRouter 的价格不一定带分档），不能选它，改选能表达分档的来源或手填；不拿别的来源补全；
  - 手填的价格不会被自动同步覆盖；
  - 选定来源的价格变化超过阈值时，由管理员确认后才采用（计费的参考价安全阀，见[计费的「定价」](billing.md#定价)）；
  - 选定来源不可用时，保留最后的价格并告警，从不自动换来源；
  - 改选来源时立即按新来源发布每种币种的参考价；新来源没有的币种，撤下旧来源的价格，计费改用其他币种按汇率换算；
  - 价格每次变化都记一个新版本（这是参考价的版本，与计费的价格版本不同）；
  - 其他数据集只用来核对：与选定来源不一致时告警，不替换。
- **OpenRouter 作为可选来源**：它的条款第 7 条禁止为建设竞争服务而访问和抓取；负责人仍要求把它保留为可选来源（Issue #24），这项风险记录在此，不因条款自动停用。
- **只用原价**：厂商的限时优惠、批量折扣都不进入目录；只录标准档的原价。第一版不做分时段价格，DeepSeek 这类按时段定价的只录高峰价（负责人决定，Issue #24）。

### 参考价怎么进入目录

1. **取数**：数据集每天取一次，登记的官方价格页（一个页面一种币种）默认每小时取一次，加随机抖动，遵守 robots.txt。都走统一的出站客户端，只访问登记的地址，有超时和正文大小上限，不跟随重定向。连续失败或距上次成功太久就告警，阈值是后台数据。
2. **选定 LiteLLM 或 OpenRouter 的模型**：条目按原文精确换算成整张价目表（不经浮点数），与当前参考价比较；变化在阈值内自动发布，超过阈值开审核项，管理员确认后发布。条目表达不了模型已有的分档时不发布，开审核项。
3. **选定官方价格页的模型**：页面去掉脚本和标签、截取价格段；内容变了就保存页面快照，开审核项并附上与该未结审核项原始基线的差异，后续变化不覆盖这个基线。运营 agent 按页面写出完整的价目表（全部档位），附快照和逐字摘录提交；代码检查至少一段非空摘录且每段逐字出现在该页面当前的快照里、单价在允许范围内及价目表的门槛口径合法；每一档是否完整且对应厂商原文由审核者判断。页面变回旧内容时指回旧快照：证据始终以页面现在的内容为准。
4. **手填**：管理员录入整张价目表并写明理由，同步从不覆盖它。
5. **发布**：提交时带上审核时看到的当前参考价；期间已有别的发布，就拒绝，参考价的历史不会分叉。每一行记下来源和证据。审批策略由[运营](operations.md)定，改参考价属于资金相关操作。
6. **核对**：没被选定的数据集与当前参考价按整张价目表比较：币种、每一项、每一档的门槛和单价。不一致就开审核项，不改价；本轮取数失败的来源整批不参与；对不上任何模型的条目开新模型审核项。

- **币种**：每个模型、每个币种一条参考价的历史。DeepSeek 在英文页发布美元价、中文页发布人民币价，两者不是按汇率换算的关系（2026-10-14：deepseek-flash 高峰时段输入每百万 $0.3 / ¥2），所以各存一条；计费选用与站点币种一致的那条，没有时按汇率换算。
- **改价**：每次选定来源的价格变化都发布进目录；售价是否跟着变，由计费的发布阈值决定（见[计费的「定价」](billing.md#定价)）。
- **第一版验收**：对 OpenAI、Anthropic、Google、DeepSeek 四家价格页上在售的文本模型逐个检查：已上架；有公开名字；选定来源发布的每种币种各有一条当前参考价，与来源一致；来源上的每一档都已录入；选官方价格页的，证据引用页面当前的快照、摘录能在其中找到；核对没有未处理的不一致。由一个只读的检查命令逐模型输出结果。

### 价目表的结构

目录里的参考价、计费的价目表、适配器生成的成本计划，三者使用同一个结构，只是用途不同：

- 目录价目表记录选定来源的原价，作为参考价；
- 成本计划记录供应商的原价，作为成本；
- 计费的价目表记录我们的售价。

结构包括：

- **计费项**：输入、输出、缓存读、缓存写（5 分钟 / 1 小时）、推理、图片（输入、输出，按 token；另有按张的输入与输出）、音频（输入、输出，按 token；另有按秒的输入与输出）、朗读文本（按字符）、按次工具费（如每千次联网搜索、每次文件搜索、每页文件解析）、按请求费（每个生成请求一次）。文件搜索的价格是模型自己的官方价，没有站点默认价，声明这个能力时规格必须同时在 `items` 里声明 `file_search`，上架门槛据此要求售价（[计费的「价目表缺项」](billing.md#价目表缺项)）。文件解析是网关自己的按页项，上游不报告，规格里没有它，上架门槛也不检查；模型要接 `mistral-ocr`，售价表里必须有 `file_parse` 的单价，价格只来自模型自己的价目表，没有站点默认价；`cloudflare-ai` 免费，`native` 按输入 token。`Config.FileParse` 关闭时网关仍按旧规则每个文件一次 `web_search`，这一版不发布 `file_parse`。每项只有一种单位（`Item.Unit()`：token、次、张、秒、字符），价目表的一项只有一个单价，所以同一侧可以同时有按 token 和按张的两个项，它们互不重叠。同一个 token 只计一次。
  - 缺价的回退在选定档位之后进行：推理没有单独定价时按该档的输出价计，Gemini 的输出价就包含思考 token。这是唯一的回退：OpenAI 对没有写入价的模型把缓存写入按普通输入收费，录入时就把缓存写的单价写成与输入相同。
- **条件**：
  - 按上下文长度分档。每一档必须声明三件事，并在证据里引用厂商原文：
    - 门槛按哪些计费项的用量之和算，整张价目表一个口径，各档共用：Anthropic 按输入加缓存读写的合计，Google 按提示词，即输入、缓存读，以及从提示词里拆出去单独计价的音频、图片和视频；
    - 计法：是"整单按档"（用量严格大于门槛后整个请求都按高档计价，Anthropic 和 Google 的长上下文就是这样），还是"超出部分按档"；
    - 哪些计费项随档位变化，例如输出价是否也变。
  - 不支持分时段价格（负责人决定，Issue #24）；
  - 服务等级，例如 flex、priority。标准档以外的每个等级有自己完整的一套单价和同样的分档门槛：先按实际服务的等级选单价，再按上下文长度选档；等级没录的计费项就是缺价，不沿用标准档的价格。
- **倍率型条件**：中转站常见的"满足条件时整体乘一个倍率"，例如 sub2api 的长上下文倍率，也用同一结构表达：条件 + 对哪些计费项乘多少。
- **缺项**：上游报告了价目表没有的计费项或条件取值（例如没有定价的服务等级）时，按[计费的「价目表缺项」](billing.md#价目表缺项)处理。目录不在调用前按请求特征拒绝；模型根本不支持的功能（例如纯文本模型收到音频）由[路由](routing.md)按端点能力过滤。
- **单价**用精确小数存储（见[计费的「钱的表示」](billing.md#钱的表示)）。
- **公开读取**：`Schedule.View()` 把整张价目表展开成 `ScheduleView{Currency, Basis, Rates}`，每个 `ScheduleRate` 带服务等级、`Base`、上下文档门槛、计费项和精确单价。展开的是每个档位的**实际生效价**：某档没写的计费项继承基础价，空档因此会显示恢复后的基础价；`Base` 与显式 `above: 0` 分开，后者是 0 以上的第一档，不是基础档。网站和控制台的模型与价格页用它显示每一个档位和计费项，不读成本、来源或供应商页。

### 计算正确的保障

- 每个模型家族都把厂商文档里的计价示例做成测试用例：给定用量，算出的金额必须和文档一致。
- 价目表和供应商实际扣费持续对账（见[计费的「供应商成本」](billing.md#供应商成本)）。如果某条规则写错了，对账差额会立刻暴露。

## 决策与理由

别名和 `:free` 的数据模型是难以逆转的决定，按 [#309 的授权](https://github.com/TYCT-tyct/Axiom/issues/309#issuecomment-6478816797)「遇到问题自己解决问题和判断最优而不是问我。」作出，理由和 rejected 方案如下。

| 决策 | 理由 |
|---|---|
| 售价的公开投影用 `ScheduleView` 展开全部服务等级、档位和计费项，不折成每百万 token 一个数 | 分档、priority/flex、按张、按秒和按字符的项目各有自己的单价，折算会掩盖真实的计价条件，页面就与扣费不一致；展开后的结构直接来自 `Schedule`，不需要第二套价格模型。列表显示哪个价、显示成什么数由服务端说（#695）：每条单价带 `headline`（每个计费项一条：标准等级的基础档，标准等级不定这项的价时取第一个提供的等级的基础档）和 `display_amount`/`display_per`（token 项是每百万 token 的价，其他单位是每 1 个，`UnitPrice.Mul` 精确相乘，不取整），两个前端显示同一个数，不必各自挑档、移小数点；每百万 token 与 OpenRouter 的模型页和它的 `max_price` 单位一致（[Provider Routing](https://openrouter.ai/docs/guides/routing/provider-selection)，2026-11-01 核对）。不选另给一个标题价对象：它是 `rates` 里某一条的副本，两份可能说得不一样；标记放在原条目上只有一份 |
| 标准 ID 用 OpenRouter 的 `厂商/模型`，厂商原生名字作为公开名字 | 负责人同意。兼容 OpenRouter 的客户端，同时让 Claude Code、Codex 这类只认原生名字的工具开箱可用 |
| 名字分公开和内部；中转站的写法是供应商别名，只在该供应商内有效 | 厂商原生的名字都能调用，Claude Code 这类工具发来的带日期名字不用改配置；`-latest` 承诺最新版本，M1 没有切换，开放调用就会悄悄落后，所以只用于识别；同一个写法在不同中转站可能指不同模型，全局共用会把请求映射到错误的模型 |
| `:free` 建独立模型；别名指向具体模型、只追加 | `:free` 有自己的上下文、端点和价格，复用模型行就能沿用发现、上架门槛和路由；别名显式指向目标，运营决定新版本何时接管，前缀或日期猜目标会把不相干的型号串族，`change` 也留不下“为什么换”。不选解析时切后缀或给基础模型加 `free_of`：要另写发现、价格和路由；也不选别名按前缀挑最新：新上架会改变已发布名字的含义 |
| 归一化不去掉日期 | 去掉日期会把同一模型的不同版本合并，价格和能力都可能不同 |
| 切换版本或迁移名字要通知，变贵或能力下降默认需人确认 | 否则用户的价格会被悄悄抬高，能力也可能下降 |
| 切换与迁移、计划下线及其通知、预约改价放到 M1 之后，各开 Issue | 负责人决定（Issue #24，2026-10-14）。它们要可靠的事件、通知和审批才能做对，现在做只会留下半套；预约生效还会因节点时钟偏差让各节点各自为政 |
| 只用原价，不考虑优惠 | 负责人决定。OpenRouter 也按供应商的原价计费（OpenRouter 网站的定价页，2026-10）；按原价估成本只会偏高，不会亏 |
| 第一版不做分时段价格，DeepSeek 只录高峰价 | 负责人决定（Issue #24，2026-10-14）："我们的上游也没有"。空闲时段的价格是一半（DeepSeek 价格页，2026-10-14），按高峰价只会偏高、不会亏；要按时段卖时另开 Issue 加分时段条件和节假日日历 |
| 分档必须声明门槛口径、计法和受影响的计费项 | 各家的门槛口径不同，两种计法在超过门槛时差额很大；不声明就会算错 |
| 按张、按秒、按字符、按请求各自是独立的计费项，不把单位放在已有项上 | 价目表一项只有一个单价，给 `image_out` 再加一个「按张」的单位会让同一项的价有两种量纲，读写与校验都分不清；独立成项后 `Unit()` 一处决定单位，免单和缺价兜底按单位分支；`request` 由网关每个请求记一次，不进上游上报集合，免单时照收。与 #320 的实现一起定下 |
| 新计费项先发布读取能力，写入等全部节点升级后再打开 | 旧版本的封闭枚举读到新名字就拒绝整张价目表，快照加载失败，节点在陈旧上限后对所有模型不就绪；`Config.CountedItems`、`Config.RequestPrices`、`Config.FileSearch` 和 `Config.FileParse` 各自默认关闭，发布、改选来源和数据集同步都拒绝这类价目表，同步按 held 报告；打开它的发布必须已在全部节点跑能读的版本，回滚边界是还没写过这类价格。`FileParse` 只管价目表里的名字：它不由上游报告，规格里没有计费项或能力要一起管 |
| 规格多来源交叉核对，不一致不自动采用，厂商文档为最终依据 | 任何单一数据集都可能滞后或出错；错误的规格会让路由和检测用错默认值 |
| 价格以模型选定的来源为准；手填不被覆盖；来源不可用时保留最后的价格，不自动换来源 | 负责人决定（Issue #24）。参考价会通过定价规则传导到售价，只有一个权威来源，售价的每次变化才说得清来自哪里；自动换来源或拿别的来源补全，会在没人确认的情况下改掉售价 |
| OpenRouter 保留为可选来源，不因条款自动停用 | 负责人决定（Issue #24），条款第 7 条的风险已记录；是否选用由每个模型的设置决定 |
| 数据集取数失败记入持久化状态，等待下一日轮次 | 负责人在 #365 评审后的修复指令中选择：不让一次短暂故障经短退避变成队列丢弃告警；运维按状态 gauge 的后台阈值判断，节点保留代码默认 overdue 兜底。实现与指标语义见[目录模块](../../internal/catalog/README.md#参考价的录入) |
| 官方价格页靠页面监测、审核录入，不写解析器 | 页面是渲染后的文档，版式一改解析器就静默出错，变化时仍要审核；只靠人工录入，改价没人发现。监测按登记间隔检查改价，每个价格都能追到页面原文 |
| 四家官方价格页都定时抓取，抓取器行为不变 | 负责人 2026-11-08 决定四家都抓（[#309 评论](https://github.com/TYCT-tyct/Axiom/issues/309#issuecomment-6715695418)第 3 项）。2026-11-07 逐条读过四家条文和 robots（见下表）：只有 Google 的限制明确限于违反 robots；其余三家的禁止自动提取或抓取条文是否覆盖公开价格页读不出，属法律解释，由负责人定，他选了全部启用。抓取器的不变量（读并遵守 robots、每页每小时一次、固定 UA、不登录、被拦（如 403）时保留旧快照并告警、不绕过）就是条款风险的边界，三家条文的字面禁令仍在，这是已接受的风险；robots 将来变得禁止或站方明确拒绝时，停用该页（`unwatch-page`）。这只决定登记哪些页面，不改变抓取器 |
| 同源请求登记间隔限制为 1–60 秒，robots 间隔按本次结果预约，不累积为站点设置 | 两次 GET 在 4 分钟预算与 5 分钟租约内留有余量；超过 60 秒的 Crawl-delay 明确拒绝并留下退避。延长租约会让失联节点长期占页，当前不采用；新登记可调整同源设置，已预约的 robots/Retry-After 等待仍保留 |
| 核对按整张价目表比较，只用本轮取数成功的数据 | 逐字段投票会拼出没人发布过的价目表；过期的数据不能冒充当前证据 |
| 每个模型每个币种一条参考价的历史 | 中国厂商同时发布人民币和美元价，两者不是汇率关系；只存美元就得自己换算，官方页面的价格就不再是原价 |
| 1M 上下文、思考模式算端点能力，不另建模型 | 同一个模型在不同端点上能力不同；另建模型会让同一个模型分裂成多个条目 |
| 管理接口一个操作一个入口，请求原样带目录的 `Op`（`Idempotency-Key` 即 `op_key`、`reason`）和 `rev`、`base`；上架和下线合成一个「改状态」入口；改动返回改后的整个模型 | 幂等、乐观并发和审核的形状在[管理操作](../../internal/catalog/README.md#管理操作)里已经定了，接口不另造一层；返回整个模型让页面少读一次，也不会读到别人在两次请求之间的改动。接口见 `api/admin.yaml`，约定见 [platform 的「接口约定」](../../internal/platform/README.md#接口约定)。别名的写入口目前只有 `axiom catalog save-alias`，HTTP 路径还没有，等后台有别名页面时再加 |
| 价目表在契约里是带类型的结构，与 `ParseSchedule` 的规范形式一一对应 | 前端能直接按计费项生成表单；入口的校验挡掉写错的价，但「价目表是否合法」仍以 `ParseSchedule` 为准，两边不一致时以它为准并报 422 |
| 请求写法的键写进规格、值照厂商文档，缺了拒绝而不是按 Claude 的通例补 | 同一代 Claude 里思考方式、采样、强制调用工具、中途系统消息各不相同（Haiku 5.5 拒绝非默认采样却接受强制调用工具），按名字前缀猜一个默认，换一代就悄悄错；数据文件逐型号写出，经 `UpdateSpec` 留审计，缺键由 `check` 在发布前发现 |

服务端工具次数采用可选的规格数据，不给所有模型批量补常量：2026-10-28 查阅 [OpenRouter 服务端工具循环](https://openrouter.ai/docs/guides/features/server-tools#tool-call-limits)，共享 `max_tool_calls` 默认及最大均为 30；这可作为确实经过该循环的端点的默认依据（为每项预留 30 次会偏多）。[Anthropic web fetch](https://platform.claude.com/docs/en/agents-and-tools/tool-use/web-fetch-tool) 明确没有默认上限，[code execution](https://platform.claude.com/docs/en/agents-and-tools/tool-use/code-execution-tool) 文档也未给出调用次数默认上限，因此不能把 OpenRouter 的 30 自动套到原生工具。管理员必须在规格更新理由中记录端点依据和日期。未采用全局硬编码默认：无法由管理员修正，且会把没有上界的上游错误声明为有界。

### 官方页面监测的落地边界（#281）

按[负责人授权](https://github.com/TYCT-tyct/Axiom/issues/309#issuecomment-6478816797)「遇到问题自己解决问题和判断最优而不是问我。」落地已有设计：页面、快照、观测、审核与站点请求间隔保存在 catalog；不改参考价。相比进程内计时，数据库租约和站点请求预约能约束多个节点；相比在事务里等待网络，短事务预约不会长期占锁。页面配置只增、可停用，改截取范围要重新登记，避免旧 worker 把新配置的内容当作旧页面证据。

采用 `temoto/robotstxt` 与`x/net/html` v0.62.0，分别处理 robots 规则和 HTML 实体/树结构；相比自写规则匹配和正则剥标签，减少误读边界。新增 HTML 解析调用后，govulncheck 发现旧间接依赖 v0.61.0 可达 [GO-2026-6860](https://pkg.go.dev/vuln/GO-2026-6860)，因此升级到已修复的 v0.62.0，避免文档文本合并的二次复杂度。HTTP 状态、访问许可和间隔仍由 catalog 明确控制，不采用依赖把任意 4xx 当作允许的默认。

不变量：只读登记 URL 和同源 robots.txt，全部走 egress；不跟重定向；robots 不可读、拒绝或截取失败时保留旧快照；同源全部请求共享间隔，取配置与 robots Crawl-delay 的较大值并尊重 Retry-After；快照、当前指针、观测、审核项和平台告警信号同事务提交；过期租约不能写回；官方价发布先锁仍启用的页面并核对当前快照、厂商、币种和逐字摘录，再锁模型。默认每小时加正向抖动，不早于站点允许的频率。人工确认条款后才登记，安装不自动启用四家页面。

风险与检查：网络期限、重定向、大小限制与 robots 拒绝由假 HTTP 服务验证；内容归一化与缺标记测试防止把导航变化或空抽取当作改价；真实 PostgreSQL 验证租约代次、事务回滚、快照复用、审核去重、平台范围和证据锁；调度启动与取消由队列集成测试验证。完整四家目录验收仍依赖已有真实目录数据，不能用空目录或自填价格冒充通过。

2026-11-07 的条款取证（只读条文与 robots.txt，不作法律意见；"读取日"是取得该页面文本的日期，取自读取工具的获取时间）。对象是每小时一次、只取登记的公开价格页、遵守 robots.txt、固定 UA、不登录的抓取（见上文「取数」）：

| 厂商 | 条文（原文，读取日） | robots.txt（读取日 2026-11-07） | 结论 | 抓取器怎么办 |
|---|---|---|---|---|
| OpenAI | [Terms of Use](https://openai.com/policies/row-terms-of-use/)「Using our Services」，读取日 2026-10-22：“Automatically or programmatically extract data or Output”（列在不得做的事里）；价格页本身无单独的网站条款 | `openai.com/robots.txt`：`User-agent: *` `Allow: /` `Disallow: /microsoft-for-startups/`；`/api/pricing/` 允许 | 条款禁止“自动提取数据”，读不出它是否只指服务内的数据；负责人 2026-11-08 决定抓取（见下）。`openai.com/api/pricing/` 在 2026-10-31 和 11-08 都回 403 | 启用，登记 `developers.openai.com/api/docs/pricing`（OpenAI 的 API 文档站价格页，同样的价目，11-08 回 200，`developers.openai.com/robots.txt` 为 `User-agent: *` `Allow: /`）；不换 UA 去绕 `openai.com` 的 403 |
| Anthropic | [Consumer Terms](https://www.anthropic.com/legal/consumer-terms) §3，读取日 2026-11-07：禁止“crawl, scrape, or otherwise harvest data or information from our Services other than as permitted”，以及除通过 Anthropic API key 或明确许可外“access the Services through automated or non-human means, whether through a bot, script, or otherwise”（读到的是该页韩文版，英文措辞据其对应条目转述，英文原文待核）；[Commercial Terms](https://www.anthropic.com/legal/commercial-terms)（读取日 2026-11-06）未见对公开文档页的抓取条款；未找到专门覆盖 `platform.claude.com` 文档站的网站条款 | `platform.claude.com/robots.txt`：`User-Agent: *` `Disallow: /api/`；`/docs/en/about-claude/pricing` 允许 | 消费者条款的适用范围（是否含公开文档页）读不出，商业条款没有对应禁令；负责人 2026-11-08 决定抓取 | 启用 |
| Google（Gemini API） | [Google Terms](https://policies.google.com/terms)「Don't abuse our services」，读取日 2026-11-07：“using automated means to access content from any of our services in violation of the machine-readable instructions on our web pages (for example, robots.txt files that disallow crawling, training, or other activities)”；[Gemini API Additional Terms](https://ai.google.dev/gemini-api/terms)（读取日 2026-11-02）的抓取限制只针对 Grounding 与 Maps 数据，不涉及文档页；文档内容按 [Site Policies](https://developers.google.com/site-policies)（读取日 2026-10-14）以 CC BY 4.0 授权 | `ai.google.dev/robots.txt`：`User-agent: *` `Disallow: /*?*authuser=`；`/gemini-api/docs/pricing` 允许 | 有条件允许：只禁违反 robots 的自动访问，价格页未被禁止。条件：每次先读 robots 并照做；不带 `authuser` 参数；固定 UA；间隔不早于 robots 许可；robots 变得禁止时停取 | 条件已由抓取器的不变量满足；仍按「人工确认后才登记」由负责人登记 |
| DeepSeek | [Terms of Use](https://cdn.deepseek.com/policies/en-US/deepseek-terms-of-use.html) §3.5(3)，读取日 2026-11-06：“Engage in activities to steal network data, such as: … capturing, copying any content of the Services, including but not limited to using any robots, spiders, or other automatic setups, setting mirrors.” | `www.deepseek.com/robots.txt`：`Allow: /`；`api-docs.deepseek.com/robots.txt` 返回的是文档首页 HTML，没有任何指令（读取日 2026-10-28） | 条文字面上禁止用 robots、spiders 等自动方式“复制服务内容”，但它列在“窃取网络数据”之下，读不出是否含公开价格文档；站点本身没有 robots 指令；负责人 2026-11-08 决定抓取 | 启用，USD 与 CNY 两页 |

四家都没有明文允许每小时抓价格页；Google 是唯一条文把限制写成“违反 robots”的，其余三家的字面禁令是否覆盖公开价格页属法律解释，负责人 2026-11-08 决定四家都抓（[#309 评论](https://github.com/TYCT-tyct/Axiom/issues/309#issuecomment-6715695418)第 3 项）。抓取器没有改，`deploy/catalog/price-pages.json` 去掉了三家的排除；管理员仍用 `watch-page` 登记，页面被站方拦截（如 403）时按既有规则保留旧快照并告警，不换 UA 绕过。

清单在 `deploy/catalog/price-pages.json`，是 live 验收输入，也是管理员登记的依据，不自动启用；2026-11-08 起四家的页面都不再带 `excluded`，OpenAI 改用 `developers.openai.com/api/docs/pricing`（正文标记“Flagship models”到“Cloud platforms”），Anthropic 的标记是“The following table shows pricing for all Claude models:”到“Private offers”；当日对五个页面各跑 `check-pages`，OpenAI（sha256 `eac73c17…`）、Anthropic（`f278aed1…`）、DeepSeek USD（`752d5c0c…`）、DeepSeek CNY（`7a04e595…`）抽取成功。同日 Google 的 `ai.google.dev/gemini-api/docs/pricing` 回 301 到 `aistudio.google.com/docs/pricing`，抓取器不跟重定向，该页要换登记，另行处理；页面 GET 的结果、hash、失败与重试原因留在本文的 2026-10-31 运行一节和 PR。完整四家逐模型验收仍须真实目录数据，检验上架、名字、每币种当前价与来源、分档、证据和未处理核对；没有真实目录连接时如实报告未验证。

### 2026-10-31 的四家 live 运行

本次在一台开发服务器上对四家的五个公开价格页各跑一次 `check-pages`（DeepSeek 的 USD 与 CNY 分页；每个价格页一次 GET，robots 由工具按同源读取），并用同一 UA 另存每个页面的原始响应，供修复后的抽取器复算 hash；没有重试。统一出站、robots、4 分钟期限和 32 MiB 上限不变。运行用的清单是临时文件，当时清单里三家仍带排除，这是一次诊断性读取，不启用定时监测；2026-11-08 负责人决定后的状态见上文。四个 USD 页的主运行以非零退出，唯一失败是 OpenAI 的 403；CNY 页另跑一次，退出 0。

| 厂商 | URL | robots.txt | 页面 | 抽取正文 |
|---|---|---|---|---|
| OpenAI | `https://openai.com/api/pricing/` | 200，`Allow: /`（另禁 `/microsoft-for-startups/`） | **403**（拦截页 9,662 B） | 无 |
| Anthropic | `https://platform.claude.com/docs/en/about-claude/pricing` | 200，`Disallow: /api/` | 200，1,307,125 B | 2,386 B，sha256 `5a3c20c9…` |
| Google | `https://ai.google.dev/gemini-api/docs/pricing` | 200，`Disallow: /*?*authuser=` | 200，261,792 B | 57,627 B，sha256 `25100a50…` |
| DeepSeek USD | `https://api-docs.deepseek.com/quick_start/pricing/` | 200，但返回文档首页 HTML（`text/html`，无 robots 指令） | 200，27,815 B | 1,636 B，sha256 `752d5c0c…` |
| DeepSeek CNY | `https://api-docs.deepseek.com/zh-cn/quick_start/pricing/` | 同站，同上 | 200，27,987 B | 1,670 B，sha256 `7a04e595…` |

运行时保存的原始响应 sha256：OpenAI `7fec3897…`、Anthropic `9de88a38…`、Google `41919361…`、DeepSeek USD `57941235…`、DeepSeek CNY `ed99e49b…`。抽取正文的 hash 是最终抽取器对同一份保存的响应重算的；修复前的运行 hash 见 PR。

USD 表用 USD / 百万 token，DeepSeek CNY 表用人民币 / 百万 token；“读”是缓存命中，“写 5m”“写 1h”是缓存写入的两种保留时间；“—”表示页面没有该项。

**Anthropic**（`platform.claude.com`，Model pricing 与 Additional models 两表）

| 模型 | 输入 | 输出 | 读 | 写 5m | 写 1h |
|---|---|---|---|---|---|
| Claude Fable 5.1 | 10 | 50 | 0.25 | 12.50 | 20 |
| Claude Opus 5.5 | 4 | 20 | 0.20 | 5 | 8 |
| Claude Sonnet 5.5 | 2 | 10 | 0.10 | 2.50 | 4 |
| Claude Haiku 5.5，提示 ≤100k | 0.10 | 0.50 | 0.01 | 0.125 | 0.20 |
| Claude Haiku 5.5，提示 >100k | 0.50 | 2.50 | 0.05 | 0.625 | 1 |
| Claude Mythos 5.1 | 10 | 50 | 0.25 | 12.50 | 20 |
| Claude Fable 5 | 10 | 50 | 1 | 12.50 | 20 |
| Claude Mythos 5 | 10 | 50 | 1 | 12.50 | 20 |
| Claude Opus 5、4.8、4.7、4.6、4.5 | 5 | 25 | 0.50 | 6.25 | 10 |
| Claude Opus 4.1、4 | 15 | 75 | 1.50 | 18.75 | 30 |
| Claude Sonnet 5 | 2 | 10 | 0.20 | 2.50 | 4 |
| Claude Sonnet 4.6、4.5、4 | 3 | 15 | 0.30 | 3.75 | 6 |
| Claude Haiku 4.5 | 1 | 5 | 0.10 | 1.25 | 2 |
| Claude Haiku 3.5 | 0.80 | 4 | 0.08 | 1 | 1.60 |

**Google**（`ai.google.dev`，Standard 档；未列 image、TTS、Live、Transcribe 等非纯文本模型）

| 模型（页面 ID） | 输入 | 输出 | 缓存读 | 备注 |
|---|---|---|---|---|
| Gemini 4 Argon（`gemini-4-argon`） | 2.00 | 10.00 | 0.10 | 2026-12-01 起 4.00 / 20.00 / 0.20 |
| Gemini 3.8 Flash（`gemini-3.8-flash`） | 0.75 | 3.75 | 0.075 | 2027-01-01 起 1.50 / 7.50 / 0.15；缓存存储 0.50（2027-01-01 起 1.00）/ 百万 token·小时 |
| Gemini 3.6 Flash（`gemini-3.6-flash`） | 0.75 | 3.75 | 0.075 | 同上 |
| Gemini 3.5 Flash-Lite（`gemini-3.5-flash-lite`） | 0.30 | 2.50 | 0.03 | 文本/图像/视频/音频同价；存储 1.00 |
| Gemini 3.1 Flash-Lite（`gemini-3.1-flash-lite`） | 0.25 | 1.50 | 0.025 | 音频输入 0.50、读 0.05；存储 1.00 |
| Gemini 3.1 Pro Preview（`gemini-3.1-pro-preview`、`-customtools`） | 2.00 | 12.00 | 0.20 | >200k：4.00 / 18.00 / 0.40；存储 4.50 |
| Gemini 3 Flash Preview（`gemini-3-flash-preview`） | 0.50 | 3.00 | 0.05 | 音频输入 1.00、读 0.10；存储 1.00 |
| Gemini 2.5 Pro（`gemini-2.5-pro`） | 1.25 | 10.00 | 0.125 | >200k：2.50 / 15.00 / 0.25；存储 4.50 |
| Gemini 2.5 Flash（`gemini-2.5-flash`） | 0.30 | 2.50 | 0.03 | 音频输入 1.00、读 0.10；存储 1.00 |
| Gemini 2.5 Flash-Lite（`gemini-2.5-flash-lite`） | 0.10 | 0.40 | 0.01 | 音频输入 0.30、读 0.03；存储 1.00 |

每个 Google 模型还列了 Search 与 Maps 的按次价格（有每月免费额度），随模型不同：Gemini 4 Argon 与 3.x 的多数模型是 `$14 / 1,000`，Gemini 2.5 Pro、Flash、Flash-Lite 是 Search `$35 / 1,000`、Maps `$25 / 1,000`。这是工具项，不在上表的 token 价格里。

**DeepSeek USD**（`api-docs.deepseek.com`，高峰价）

| 模型（页面写明的版本） | 输入（缓存未命中） | 输出 | 缓存命中 | 备注 |
|---|---|---|---|---|
| `deepseek-flash`（DeepSeek-V4.1-Flash-1017） | 0.30 | 1.20 | 0.006 | 低谷半价：0.15 / 0.60 / 0.003；旧名 `deepseek-v4-flash`、`deepseek-v4-flash-vision-exp` 仍被接受，但对应型号已退役，由这个版本按 Flash 价服务 |
| `deepseek-v4-pro`（DeepSeek-V4-Pro-0813） | 1.32 | 3.96 | 0.044 | 低谷半价：0.66 / 1.98 / 0.022 |

**DeepSeek CNY**（`api-docs.deepseek.com/zh-cn`，高峰价；与 USD 是两个独立页面，不是汇率换算）

| 模型（页面写明的版本） | 输入（缓存未命中） | 输出 | 缓存命中 | 备注 |
|---|---|---|---|---|
| `deepseek-flash`（DeepSeek-V4.1-Flash-1017） | 2.00 | 8.00 | 0.04 | 低谷半价：1.00 / 4.00 / 0.02 |
| `deepseek-v4-pro`（DeepSeek-V4-Pro-0813） | 9.00 | 27.00 | 0.30 | 低谷半价：4.50 / 13.50 / 0.15 |

**OpenAI**：页面 403，没有模型清单和价格。`deploy/catalog/dialects.json` 里的 6 个 OpenAI slug（`gpt-5.6-luna`、`gpt-5.6-sol`、`gpt-5.6-terra`、`gpt-6-astra`、`gpt-6-luna`、`gpt-6.1-sol`）未与官方页面核对。

目录侧没有可读的数据：这台服务器没有 `AXIOM_DATABASE_URL`，`go run ./cmd/axiom catalog check` 在配置检查处退出（`AXIOM_DATABASE_URL: required`）。所以上面只能记录页面和两个数据集的事实，不能回答“已上架、公开名字、当前参考价与选定来源、证据、未处理核对”；`deploy/catalog/dialects.json` 的 slug 只说明请求写法的数据覆盖了哪些模型，不能代替目录行。完整四家逐模型验收仍须在有真实目录的机器上跑，不能用这张表或临时数据冒充通过。

两个数据集与页面的交叉核对（只列不一致和会被 held 的；其余在 LiteLLM 有直接条目的文本模型，输入、输出和缓存读价与页面一致，LiteLLM 多数没有 1h 缓存写，OpenRouter 有；LiteLLM 没有 `claude-opus-4`、`claude-3-5-haiku` 的直接条目，只有 Bedrock/Vertex 前缀；两个数据集都只有美元，DeepSeek CNY 没有数据集对照）：

| 模型 | 页面 | LiteLLM | OpenRouter | 结论 |
|---|---|---|---|---|
| `deepseek-flash` | 高峰 0.30 / 1.20 / 0.006 | 同页面高峰 | `deepseek/deepseek-v4.1-flash-1017` 的基价是低谷 0.15 / 0.60 / 0.003，高峰在 `overrides` | OpenRouter 的条目 ID 与目录 slug 不同，基价也不是目录要的高峰价；catalog 只录高峰，不能把 OpenRouter 选作来源 |
| `deepseek-v4-pro` | 1.32 / 3.96 / 0.044 | 同页面 | `deepseek/deepseek-v4-pro` 1.60 / 3.20 / 0.135，`-0813` 0.115 / 4.00 / 0.11 | OpenRouter 与页面、LiteLLM 都不一致 |
| `google/gemini-3.8-flash` | 0.75 / 3.75 / 0.075 | 同页面 | 0.825 / 4.125 / 0.0825 | OpenRouter 高 10% |
| `anthropic/claude-haiku-5.5` | ≤100k 0.10 / 0.50 / 0.01；>100k 0.50 / 2.50 / 0.05 | 只有基础档 | 基础档 + `overrides`，数值与页面一致 | OpenRouter 能表达分档但按设计 held（数据集的阈值依据未写明）；LiteLLM 没有分档，当前价目表有分档时也不能跟随 |
| 12 个 Google 条目（本页 7 个） | — | 没有 `cache_write` | `input_cache_write` 有 16 位小数，如 `0.0000000416666666666667` | 按[模块说明](../../internal/catalog/README.md#参考价的录入)拒绝整条，不能作为这些模型的来源 |

运行暴露的抽取问题：Anthropic 把模型名和说明放在相邻的内联标签里，CSS 的 flex 列把它们排成两行；DeepSeek 的标题锚点带 U+200B。旧抽取把前者粘成 `Claude Fable 5.1For…`、`$0.10 / MTokfor…`，并把后者留在正文。最终抽取按布局补边界：表格单元格、块标签、flex 列/网格、`display:block` 和图标前后留边界；`$0` + `.10`、`Pri` + `cing` 这类连续内联文本保持粘连；隐藏节点不贡献边界。`pageExtractorVersion` 为 2，`TestRecordedPricePagesSeparateTextRuns` 用两段裁剪过的页面固定逐字正文，另有 `$0.10`、`Pricing`、隐藏节点、flex/block/icon 和版本升级的回归测试。对同一份保存的响应重算：Anthropic 6 处空格、DeepSeek USD 1 个零宽字符、Google 14 个图标后的空格并去掉 1 个脚注前的假空格（正文 57,614 → 57,627 B），没有其他正文变化；CNY 页是在修复后取的，没有修复前的对照。版本升级时对同一响应运行旧抽取器，当前快照与旧结果逐字相同才保留快照，不新建、不产生复核，保留当前证据和未处理复核；不采用删空白后的比较，`1 23` 和 `12 3` 会碰撞。旧 worker 不能把当前指针移回，数据库触发器也拒绝当前指针回退到更低版本，真实内容变化仍按原基线复核。

## 待定问题

- Google 价格页已迁到 `aistudio.google.com/docs/pricing`（旧地址 301，抓取器不跟重定向），需读新页的 robots 与条款、确定正文标记后换登记。
