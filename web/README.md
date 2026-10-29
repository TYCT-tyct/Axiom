# web

前端：官网、用户控制台、管理后台，和它们共用的包。要做什么、按什么验收见[前端与设计](../docs/frontend.md)，技术选型见[总体架构](../docs/architecture.md#技术选型)。

## 结构

| 目录 | 是什么 | 路径 |
|---|---|---|
| `site/` | 官网 | 公网监听的 `/` |
| `console/` | 用户控制台 | 公网监听的 `/console/` |
| `admin/` | 管理后台 | 只在管理监听上，不进公网 |
| `packages/ui/` | 设计系统：变量（`src/styles.css`）、组件、翻译、外观与语言偏好、数字和时间的格式 | — |
| `theme.js` | 在首次绘制前应用保存的外观；构建时按内容哈希命名，插到每个入口页 `<head>` 的开头 | 各应用的 `assets/` |
| `packages/api/` | 由 `api/console.yaml`、`api/admin.yaml` 生成的类型（`*.gen.ts`）和调用它们的客户端（`consoleApi`、`adminApi`）；`@axiom/api/auth` 是两个界面的登录、两步验证调用，形状相同，登录页面只对着它写 | — |
| `packages/app/` | 控制台和后台共用、又不属于设计系统的部分：会话与 `Gate`、登录、第二步、强制开启两步验证、恢复码、外壳、新版本提示、错误译文。设计系统不依赖路由，所以它们不放进 `packages/ui/` | — |
| `e2e/` | Playwright 端到端测试 | — |
| `journey/` | 对着真实 Axiom 进程的旅程测试和启动它的 `node.ts`（[测试](#测试)） | — |
| `scripts/embed.ts`、`scripts/compress.ts` | 把构建产物复制到 Go 代码树供程序嵌入，附预压缩的副本和内容安全策略；压缩函数只有这一个，体积预算也用它 | — |
| `scripts/budget.ts` | 体积预算检查 | — |
| `scripts/limits.ts` | 函数的圈复杂度和长度、文件长度检查 | — |

共用的 Vite 配置在 `vite.shared.ts`，各应用的 `vite.config.ts` 只写端口和路径。

## 命令

需要 Node 24 和 pnpm（版本见 `package.json` 的 `packageManager`；装了 pnpm 后它会自动换成这个版本）。第一次跑端到端测试前装一次浏览器：`pnpm --dir web exec playwright install --only-shell chromium`。

| 命令 | 做什么 |
|---|---|
| `just web-dev` | 开发服务器：官网 `:5173`，控制台 `:5174/console/`（官网的 `/console/` 也转给它，和线上一样从官网点进去），后台 `:5175`；接口请求转给 `just up` 起的程序（`:8080`） |
| `just web-build` | 构建三个应用到各自的 `dist/`，控制台和管理后台带上同一棵代码树构建出的程序的版本号 |
| `just web-embed` | 先构建，再把三个应用复制到 `internal/platform/webui/build/`（不入库）供程序嵌入；`just up`、`just test`、`just web` 都先做这一步，没做的程序启动时会说要做 |
| `just web-checks` | 构建、嵌入和除端到端之外的全部前端检查；CI 的 `web` 作业运行它 |
| `just web-e2e "1/2"` | 构建、嵌入并运行第 1/2 个端到端分片；不传参数时跑完整的 `1/1`，CI 的两个 `e2e` 分片与 `web` 并行 |
| `just web-journey` | 构建、嵌入并运行真实服务的旅程（要 Docker）；不含在 `just web` 里，CI 的 `journey` 作业只在列车和 `main` 上跑 |
| `just web` | 前端的全部检查：类型、lint、函数和文件长度、未使用的代码、重复代码、单元测试、构建、体积预算、端到端测试；`just check` 包含它 |
| `just generate` | 重新生成接口类型；`just generated` 检查生成结果已提交 |

## 规则

| 规则 | 靠什么保证 |
|---|---|
| 页面上的文字全部来自翻译文件；中文是源文件，英文的键与中文完全一致 | Biome 的 `noJsxLiterals` 拒绝写在 JSX 里的文字；`Messages<typeof zh>` 让缺键、多键都编译不过；`t()` 的键有类型。属性里的文字（`aria-label` 等）这条规则查不到，靠评审 |
| 共用包里组件写的类名在每个用到它的应用里都生效：Tailwind 只生成 `@source` 指到的文件里的类，且跳过链接到 `node_modules` 的工作区包 | 各应用的 `src/main.css` 用 `@source` 指到它用的每个带组件的包（`ui` 由自己的 `styles.css` 指）；`scripts/sources.test.ts` 按 `package.json` 的依赖核对 |
| 颜色、圆角、阴影只用 `styles.css` 的变量 | Tailwind 自带的调色板已清空，写死的颜色没有对应的类可用 |
| 页面在 `vite.shared.ts` 的内容安全策略下工作：没有内联脚本，没有 `<style>` 元素 | 端到端测试在 Go 服务发送的同一策略下加载三个应用，违规会成为控制台错误，测试失败；`embed.ts` 把 `vite.shared.ts` 的字符串交给 Go 服务原样发送，测试核对响应头与它完全一致，且不含 `unsafe-` |
| 保存的外观在首次绘制前生效，不先闪一下系统的外观 | `theme.js` 作为同源的普通脚本在 `<head>` 开头同步运行，读 `preferences.ts` 的同一个键；端到端测试拦下应用脚本后检查 |
| 构建产物的文件名带内容哈希，可以长期缓存 | Vite 给打包的文件命名；它不打包的 `theme.js` 由 `vite.shared.ts` 按内容算哈希。端到端测试检查入口页的每个脚本 |
| 每个页面有自己的标题；跳转后焦点移到页面，读屏软件会读出新页面 | `PageHeader` 和整页的状态设置标题，路由的 `onResolved` 调 `focusPage`；端到端测试检查 |
| 每个数据视图都有加载中、空、出错、没有权限四种状态 | 用 `DataState`；出错时显示请求 ID和时间；要找的东西不存在（404）时说明它找不到、给回首页的链接，不让人重试；整页的视图（详情页）传 `page`，失败时页面仍有一级标题和标签页标题；`isTransient` 判定的暂时性失败（没有收到响应、服务端错误 5xx）自动重试两次，被拒绝（4xx）和响应读不出来不重试。端到端测试覆盖没有权限、服务端错误、断网和连接无响应 |
| 每个请求都会结束 | `@axiom/api` 的客户端给每个请求设 10 秒超时（含读响应体），并接上调用方的 signal；查询把 TanStack Query 的 signal 传下去，没人再看的查询会取消请求。已知更慢的调用用 `fetchWithin` 给自己的超时：后台的端点测试是 75 秒，长过服务端给它的一分钟，否则每次慢测试都拿不到结果、只能重发 |
| 切换页面不出现空白 | 两个应用的路由都从 `routerDefaults` 开始：保留当前页面，等待超过 150 毫秒时只在内容区显示骨架 |
| 部署后旧页面加载不到新的代码块时自动刷新一次 | 路由的 `lazyRouteComponent`：只在打开页面时刷新，悬停或聚焦链接引起的预加载失败不刷新；同一个代码块在本标签页只刷新一次，再失败就显示 `PageFailedState`；会话存储不可用时不刷新，直接显示。端到端测试覆盖 |
| 数字和时间按页面语言格式化，时间注明时区 | `packages/ui/src/format.ts` 的 `useFormat` |
| 登录状态只由服务端说了算：页面启动时问 `getSession`，401 就是没登录（不是故障）；CSRF 令牌只放内存（Cookie 是 HttpOnly），每个改数据的请求自动带它和 JSON 内容类型，登录、第二步和改认证因素的响应换新令牌 | `@axiom/api` 的 `setCsrfToken` 与客户端中间件，`packages/app/src/session.tsx`；端到端的接口模拟按契约校验会话、等级、权限、CSRF、Origin，缺一个就拒绝 |
| 会话中途失效（任何请求得到 `unauthenticated`）：清掉会话和账户的缓存，去登录页，登录后回到原来的页面（路径加查询）；只有登录流程的页面（登录、第二步、开启两步验证、注册）沿用它地址里经过校验的 `next`，受保护页面地址里的 `next` 是页面自己的参数；`next` 按浏览器的 URL 规则（含编码的 `..`）解析，只收落在本应用路径范围内的地址 | `createQueryClient`（`packages/app/src/providers.tsx`）里 `QueryCache`/`MutationCache` 的 `onError`、`Gate`、`safeNext`；`safeNext` 的单元测试和 `e2e/console-pages.spec.ts` |
| 浏览器的会话由所有标签页共用：页面重新读到的会话换了用户或账户时，取消并清掉上一个账户加载的数据，页面从头开始（打开着的对话框和填了一半的表单不留）；同一账户的权限变了时页面保留（恢复码对话框不丢），但没人看的账户数据清掉、正在看的重新请求，失去权限的按「没有权限」显示；重新读会话失败时保留已有的会话，不把正在用的页面换成错误页 | `packages/app/src/session.tsx` 的 `sessionQuery`（比较前后会话的身份）、`Gate`（按身份重建、失败时保留）；端到端测试覆盖 |
| 退出登录失败要说清：没有退出、原因、可以重试 | `packages/app/src/parts.tsx` 的 `SignOutError`，侧栏和第二步页都用；端到端测试覆盖 |
| 失败只按契约的错误码翻译，服务端的英文 `message` 不给用户看；每个错误码都有译文 | `packages/ui/src/locales/zh.ts` 的 `satisfies Record<ErrorCode, string>`：契约加一个码，不写译文就编译不过 |
| 金额是精确的整数文本，显示和输入都不经过浮点数 | `useFormat().money`、`parseMoney`、`moneyInput`；单元测试含 int64 的上下界 |
| 只显示一次的内容（key 明文、恢复码）放在不能用 Esc 或点背景关掉的对话框里，用户确认后才关；生成恢复码的请求还没回来时，发起请求的对话框也不能关；确认时读新会话失败，恢复码仍留在屏幕上并说明原因，可以重试 | `Dialog` 的 `dismissible`、`packages/app/src/totp-dialogs.tsx`；端到端测试覆盖 |
| 两步验证的二维码在本地用 `qrcode-generator` 画成 SVG 的 `<path>`，不拼 HTML 字符串，也不把密钥交给外部服务 | `packages/app/src/qr.tsx` |
| 验收门槛的无障碍和不破版：控制台和后台的每个页面、每个对话框在深浅色、中英文下没有 WCAG 2.2 AA 的违规，用最坏数据在 360 到 2560px 宽下页面不出现横向滚动，表格行内的按钮和链接不跑出表格自己的滚动框 | `e2e/gate.spec.ts`（axe-core 与溢出检查），页面列表在 `e2e/console-scenes.ts` 和 `e2e/admin-scenes.ts` |
| 后台的每个改数据的操作都填理由、有确认（高风险的要输入对象的名字）、防重复提交；一个操作一个 Idempotency-Key，对话框打开时生成，发出后锁定表单，结果未知时原样重发，只有服务端明确拒绝后才换新；按修订号改的对话框收到 `stale_revision` 要重新载入才能再保存；结果标明是不是之前已完成的那次 | `admin/src/change.tsx` 的 `ChangeDialog`，状态转换在 `admin/src/attempt.ts`（规则见[前端](../docs/frontend.md)「决策与理由」）；端到端的模拟按契约要求 Idempotency-Key 和理由，同一个键带同样的请求再来就返回存下的结果并带 `Idempotency-Replayed`，换了请求就拒绝（`idempotency_key_reused`）；「同样」按服务端的指纹判断：整数按原样比较，供应商的登录只比成员名、不比密钥，地址先规范化，与 Go 共用 `internal/platform/idem/testdata/same-request.json` 里的样例 |
| 改数据的对话框在结果未知时，请求（含幂等键）留在内存里，关闭后重开、会话过期重新登录后，该改动的对话框只提示“上次的改动结果未知”，可原样重发或放弃，不给新表单 | `admin/src/unsettled.ts`、`admin/src/change.tsx` 的 `UnsettledChange`；`e2e/admin-change.spec.ts` |
| 后台的所有操作要求通过两步验证的会话：没有第二因素的账户只能到开启两步验证的页面，恢复码对话框在页面之上，换了会话也不会丢 | `packages/app` 的 `Gate` 的 `enroll` 阶段与 `RecoveryCodesHost`；`e2e/admin-access.spec.ts` |
| 接口只经 `@axiom/api` 调用，错误统一成带状态码和请求 ID的 `ApiError`，状态码 0 表示没有收到响应；收到了但解析不了的响应是普通的 `Error`，没有状态码。都保留原始错误作为 `cause` | 生成的类型不手改；`just generated` 检查 |
| 依赖用精确版本，发布满 7 天才能装上；依赖的安装脚本不运行 | `pnpm-workspace.yaml` 的 `minimumReleaseAge` 和 `strictDepBuilds`：需要安装脚本的依赖让安装失败，必须明确决定；目前没有放行任何依赖 |
| 认知复杂度不超过 15，圈复杂度不超过 12，函数不超过 60 行，文件不超过 500 行，与 Go 一侧相同 | 认知复杂度由 Biome 的 `noExcessiveCognitiveComplexity`（`biome.json`）查；圈复杂度和长度由 `scripts/limits.ts` 查，覆盖 git 不忽略的每个 TypeScript 和 JavaScript 文件（生成的 `*.gen.ts` 除外），长度不计空行和注释，测试不限函数长度。理由见[前端的「决策与理由」](../docs/frontend.md#决策与理由) |
| 没有没人用的文件、导出和依赖 | knip（`knip.jsonc`）；包只给本仓库的应用用，入口文件的导出也要有应用引用 |
| 没有重复代码 | jscpd（`.jscpd.json`），50 个词法单元以上算重复；测试和端到端的模拟（`e2e/`）不查 |
| 首屏脚本、每多开一页增加的脚本、西文字体不超过[前端性能指标](../docs/frontend.md#前端性能指标) | `scripts/budget.ts` 按程序发送时的压缩（`scripts/compress.ts`，brotli q11）后的大小计脚本，字体取 woff2 文件本身的大小；首屏算入口页加载的每个脚本和它们的静态依赖，再加入口地址打开的页面（控制台的概览、后台的首页） |

## 测试

- 接口模拟的机制集中在 `e2e/mock.ts`（会话、权限、CSRF、Origin 的校验，后台操作的 Idempotency-Key 与理由），各界面只写自己的路由和数据：`e2e/console-api.ts`、`e2e/console-data.ts` 和 `e2e/admin-*.ts`（数据：常见、空、最坏）。路径、方法、请求体和每个响应的状态与 JSON 都由契约的类型约束，与契约不符就编译不过。
- 验收门槛的截图：`AXIOM_SHOTS_DIR=<目录> pnpm --dir web e2e shots`（可用 `-g "admin users"` 缩小范围），写出控制台和后台每个页面在四种宽度、深浅色、中英文下的截图，以及最坏数据和极端品牌色（改令牌）的截图，只给人看，不入库。
- 真实服务的旅程（`journey/`、`playwright.journey.config.ts`）：一个测试带着浏览器走一遍新用户的第一天，对着真实的 Axiom 进程，不模拟接口：用邀请码注册、登录、创建 key、充值、调用一次模型，在调用记录和额度流水里看到这次调用和扣费。服务端在 `cmd/axiom/journey_test.go`（构建标签 `journey`）：真实的 PostgreSQL 与 Valkey（容器），`axiom site`、`axiom user` 建站点和邀请人，目录、供应商、定价照调用路径测试的世界，只有外部的两样是假的：供应商（回放录制的流）和 EasyPay 网关（接单、回答查询、由旅程通知它「已支付」）。`journey/node.ts` 在 Playwright 的 globalSetup 里编译并启动它，节点写出地址后才开始测试，标准输入一关（含旅程崩溃）节点就停。站点域名是 `localhost:<端口>`，因为只有 localhost 在明文 http 下发会话 Cookie；API 域名是同一端口的 IP。控制台还没有充值页面（#317），所以充值这一步用浏览器会话调控制台的接口（订单、验证），再在页面上看余额。要 Docker：本地用 `just web-journey` 在宿主上跑（`just web` 不含它）；CI 的 `journey` 作业由 `withcluster` 在宿主上起数据库，节点和 Chromium 在 web 容器里跑（独立网络命名空间，#363），经 `scripts/ci/web-container.sh` 按网桥地址连数据库，只在列车和 `main` 上跑，失败时上传 `test-results/`（浏览器 trace 和 `journey-node.log`）。
- 单元测试（Vitest）测不需要浏览器的逻辑，和代码放在一起（`*.test.ts`）。
- 端到端测试（Playwright）对着嵌入程序的构建产物跑：`internal/platform/webui/cmd/serveweb` 起程序自己的静态服务（公网监听上是官网和控制台，管理监听上是管理后台），发送服务端真实的缓存头、压缩和内容安全策略；接口用 `page.route` 模拟，不需要数据库。用 `just web` 跑全部检查，或用 `just web-e2e` 单跑端到端：两者都先构建并嵌入，再把构建写入的版本号传给测试。分片使用 `just web-e2e "1/2"`（内部为 `pnpm --dir web e2e --shard=1/2`），通过结果按完整命令缓存，分片序号和总数不同就不能复用；测试自己挑三个应用及其服务端口都空闲的端口（随机选，两个工作区同时跑不会撞），不设固定端口，也不会拿别的工作区的服务测；`AXIOM_WEB_PORT` 设了就用它，端口已被占用时测试失败。开发服务器要在另一个工作区同时跑，设环境变量 `AXIOM_WEB_PORT` 把三个应用的端口整体挪开（默认 `5173`，测试用的服务端口在其上加 1000），例如 `AXIOM_WEB_PORT=5273 just web`。并行的 worker 最多 8 个（`playwright.config.ts`）：Playwright 默认按整机核数的一半开，在 96 核的 CI 机上是 48 个，几个作业同时跑时每项检查被拖到超时（#297）。同一台 8 核机器、负载 10 到 20 时测 `gate.spec.ts` 里 axe 扫描的耗时：1 个 worker 中位 1.0 秒、最大 2.0 秒；4 个 1.2 秒、2.6 秒；8 个 1.8 秒、3.7 秒；48 个 14.5 秒、29 秒，20 项超过 30 秒。扫描本身不变，慢的是 worker 比核多后互相抢 CPU，所以靠上面的 worker 上限解决，不单独放宽超时、不重试。

定价页的发布列表显示受众、当前价格版本、待确认与暂停状态；窄屏把受众、状态和完整版本放在模型下方，操作留在可视范围，宽屏分列。`Table` 的滚动区按 [MDN 的键盘滚动说明](https://developer.mozilla.org/en-US/docs/Web/CSS/Reference/Properties/overflow#accessibility)可聚焦并带区域名称，纯读价表也能用键盘横向滚动；该区域的 `tabIndex` 是 Biome 非交互元素禁令的局部例外。验收夹具包含受众发布及当前价格，场景覆盖等级/账户规则和预览结果。规则列表的浏览器断言按有名称的规则表定位，避免把发布表或预览表的行算入；启用规则的编辑清除尚未支持的限制，原样停用则保留既有目标、受众、方法与限制，两条路径分别验证请求和保存结果。发布对话框比较规则原价和保本提案，需确认模型并填写理由，低价例外显式勾选。调用列表在费用下显示冻结价格版本，旧节点不返回字段时显示不可用。

概览的最近报错通过 `status=error&limit=5` 向服务器查询；只有返回可选的 `status_filter=error` 才展示结果，旧节点未回显时显示不可用，不在浏览器筛选一页普通调用。筛选、聚合与导出 API 的边界见[记录与告警](../docs/domains/observability.md#用户调用记录查询与导出)，本轮不增加视觉界面。
