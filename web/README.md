# web

前端：官网、用户控制台、管理后台，和它们共用的包。要做什么、按什么验收见[前端与设计](../docs/frontend.md)，技术选型见[总体架构](../docs/architecture.md#技术选型)。

## 结构

| 目录 | 是什么 | 路径 |
|---|---|---|
| `site/` | 官网 | 公网监听的 `/` |
| `console/` | 用户控制台 | 公网监听的 `/console/` |
| `admin/` | 管理后台 | 只在管理监听上，不进公网 |
| `packages/ui/` | 设计系统：令牌和字体（`src/css/`，全局 CSS 只有 `tokens.css`、`fonts.css` 和 `base.css`（重置与文档基础）；令牌、字体和组件样式的归属规则见[前端与设计的「前端工程」](../docs/frontend.md#前端工程)）、组件、翻译、外观与语言偏好、数字和时间的格式 | — |
| `theme.js` | 在首次绘制前应用保存的外观；构建时按内容哈希命名，插到每个入口页 `<head>` 的开头 | 各应用的 `assets/` |
| `packages/api/` | 由 `api/console.yaml`、`api/admin.yaml` 生成的类型（`*.gen.ts`）和调用它们的客户端（`consoleApi`、`adminApi`）；`@axiom/api/auth` 是两个界面的登录、两步验证调用，形状相同，登录页面只对着它写 | — |
| `packages/app/` | 控制台和后台共用、又不属于设计系统的部分：会话与 `Gate`、登录、第二步、强制开启两步验证、恢复码、外壳、新版本提示、错误译文、路由预设的规则编辑器和规则摘要（`presets/`，文字在 `packages/ui/src/locales/preset-rules.ts`）。设计系统不依赖路由，所以它们不放进 `packages/ui/` | — |
| `packages/fx/` | 动效运行时：`useEffectModule`（首帧之后的空闲时刻加载效果模块）、共用帧循环（`own`，标签页隐藏或所有者滚出视口时停，计数器在 `window.axiomFx.state`）、减少动态时不排帧；`sea`、`prefs` 两个入口原样托管画板的海与入场、语言与深浅的引擎（[动画](../docs/frontend.md#动画)） | 官网首屏之后的块 |
| `e2e/` | Playwright 端到端测试 | — |
| `journey/` | 对着真实 Axiom 进程的旅程测试和启动它的 `node.ts`（[测试](#测试)） | — |
| `scripts/embed.ts`、`scripts/compress.ts` | 把构建产物复制到 Go 代码树供程序嵌入，附预压缩的副本和内容安全策略；压缩函数只有这一个，体积预算也用它 | — |
| `scripts/budget.ts`、`scripts/font-budget.ts` | 体积预算检查 | — |
| `scripts/fonts/` | 字体管线：按翻译文件里的字（控制台和后台按每个路由页的代码用到的键，`routes.ts`）把源字体裁成 woff2，写进 `packages/ui/src/css/fonts/` 和 `fonts.css`（见下文「字体」） | — |
| `scripts/limits.ts` | 函数的圈复杂度和长度、文件长度检查 | — |
| `scripts/vitals.ts` | 三次目标页 LCP 各按前后两次概览页的平均值折算成一个值（[前端性能指标](../docs/frontend.md#前端性能指标)）；不需要浏览器，单独用 vitest 测 | — |

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
| `pnpm --dir web site-assets` | 重新切出接入屏的等距图（`site/src/home/connect-figure.svg` 和 `connect-figure.gen.ts`，来自 Site 画板脚本的 `FIG` 与绘图函数），产物入库，`just generate` 也会跑它；加 `--check` 只检查是否最新 |
| `pnpm --dir web fonts` | 重新裁剪字体（要 Python 3 和网络；第一次下载源字体），产物入库；见下文「字体」 |

## 规则

| 规则 | 靠什么保证 |
|---|---|
| 页面上的文字全部来自翻译文件；中文是源文件，英文的键与中文完全一致 | Biome 的 `noJsxLiterals` 拒绝写在 JSX 里的文字；`Messages<typeof zh>` 让缺键、多键都编译不过；`t()` 的键有类型。属性里的文字（`aria-label` 等）这条规则查不到，靠评审 |
| 颜色只写在 `packages/ui/src/css/tokens.css`，字体族名只写在同目录的 `fonts.css`；页面只用变量 | `scripts/styles.ts` 拒绝 `tokens.css` 之外的颜色字面量；`scripts/tokens.test.ts` 从 `tokens.css` 读值，检查文字在每个底色上不低于 4.5:1、控件边界和焦点不低于 3:1 |
| 样式归组件：应用代码不传 `className`、`style` 只写 `--*`、不导入 CSS；`*.module.css` 只在 `packages/ui/src/components/` 和 `site/src/home/`，只被同目录同名的 TSX 导入，选择器从本模块的类开始（`:global` 只许根元素的 `data-theme`、`data-neutral`），规则平铺（不嵌套，at 规则只用 `@media`、`@supports`、`@container`、`@layer`、`@keyframes`）；颜色字面量只在 `tokens.css`（自定义属性、`var()` 的回退值和关键帧里也算）；全局 CSS 只有 `tokens.css`、`fonts.css`、`base.css`；源码不用 `innerHTML`、`outerHTML`、`insertAdjacentHTML`、`cssText`、`dangerouslySetInnerHTML`、`setAttribute('style')`；组件里 1px 以上的边线对 sand 和 slate、深浅两套的底色不低于 3:1，经本模块自定义属性引用的按它的每个值算，边线画出的颜色必须正是一个颜色令牌或 `transparent`，混色、`currentColor`、没写颜色、未定义的名字都直接失败，`--bw` 的装饰线和带模糊的阴影不算边线 | `scripts/styles.ts`（`scripts/styles.test.ts` 对整棵树运行，随 `pnpm test` 进 CI）。旧用法按规则和文件记在 `scripts/style-baseline.json`：数量只能减少，减少时同一改动里跑 `node scripts/styles.ts --shrink` 降下基线；新文件为 0；`migrated` 里的路径一律为 0。规则见[前端的「前端工程」](../docs/frontend.md#前端工程) |
| 数据层的组件（`ListDetail`、`FilterBar`、`BarChart`、`LineChart`、`Metric`，`packages/ui`）：长列表只画视口附近的行（固定行高，由页面滚动，`aria-setsize` 报总数）；选中的行和筛选值写在地址的查询参数里（`useUrlSelection`、`useUrlParams`），刷新和后退都保留，方向键只在窗口够宽时改选中项；平板和手机上详情是盖住列表的面板，关闭后焦点回到原来的行；图表用手写 SVG，没有新依赖，空数据、一个点、0 和极大值都能画，键盘用左右键逐点读，读屏软件读到同样数据的隐藏表格，坐标轴、提示和数字、金额经 `format.ts` 按语言、时区、币种显示，减少动态时入场动画不播放 | `packages/ui/src/components/data.test.tsx`、`url-state.test.tsx`、`format.test.ts`；组件各有 `demo.tsx` 的条目 |
| 页面在 `vite.shared.ts` 的内容安全策略下工作：没有内联脚本，没有 `<style>` 元素 | 端到端测试在 Go 服务发送的同一策略下加载三个应用，违规会成为控制台错误，测试失败；`embed.ts` 把 `vite.shared.ts` 的字符串交给 Go 服务原样发送，测试核对响应头与它完全一致，且不含 `unsafe-` |
| 保存的外观在首次绘制前生效，不先闪一下系统的外观 | `theme.js` 作为同源的普通脚本在 `<head>` 开头同步运行，读 `preferences.ts` 的同一个键；端到端测试拦下应用脚本后检查 |
| 构建产物的文件名带内容哈希，可以长期缓存 | Vite 给打包的文件命名；它不打包的 `theme.js` 由 `vite.shared.ts` 按内容算哈希。端到端测试检查入口页的每个脚本 |
| 每个页面有自己的标题；替换整个页面的状态也保留一个一级标题；跳转后焦点移到页面，读屏软件会读出新页面 | `PageHeader` 和整页的状态设置标题，加载中给的是“正在加载”的一级标题（视觉隐藏，见下一行），路由的 `onResolved` 调 `focusPage`；端到端测试检查 |
| 每个数据视图都有加载中、空、出错、没有权限四种状态 | 用 `DataState`；出错时显示请求 ID和时间；要找的东西不存在（404）时说明它找不到、给回首页的链接，不让人重试；整页的视图（详情页）传 `page`，加载中、空、失败时页面都有一个一级标题，失败时还有标签页标题；加载中的标题视觉隐藏，和骨架的状态分开（`states.tsx`）；`isTransient` 判定的暂时性失败（没有收到响应、服务端错误 5xx）自动重试两次，被拒绝（4xx）和响应读不出来不重试。端到端测试覆盖没有权限、服务端错误、断网和连接无响应，以及持有会话、页面代码块和页面数据时标题仍在 |
| 每个请求都会结束 | `@axiom/api` 的客户端给每个请求设 10 秒超时（含读响应体），并接上调用方的 signal；查询把 TanStack Query 的 signal 传下去，没人再看的查询会取消请求。已知更慢的调用用 `fetchWithin` 给自己的超时：后台的端点测试是 75 秒，长过服务端给它的一分钟，否则每次慢测试都拿不到结果、只能重发 |
| 切换页面不出现空白 | 两个应用的路由都从 `routerDefaults` 开始：保留当前页面，等待超过 150 毫秒时只在内容区显示骨架；骨架也是整页状态，带着一个一级标题，端到端场景等的是页面自己的标题而不是加载中的那个（`e2e/page-heading.ts`） |
| 部署后旧页面加载不到新的代码块时自动刷新一次；滚动发布保留了旧构建的带哈希文件时通常不触发，回滚到没有保留的构建仍靠它 | 路由的 `lazyRouteComponent`：只在打开页面时刷新，悬停或聚焦链接引起的预加载失败不刷新；同一个代码块在本标签页只刷新一次，再失败就显示 `PageFailedState`；会话存储不可用时不刷新，直接显示。保留旧文件的机制在 [platform 的「前端文件」](../internal/platform/README.md#前端文件)，端到端测试覆盖刷新 |
| 数字和时间按页面语言格式化，时间按查看者的时区显示并注明时区；按 UTC 定义的月份在控件旁写明 UTC | `packages/ui/src/format.ts` 的 `useFormat`、`MonthField` |
| 表格的列标题不换行；长标识符在中间截断，保留首尾，悬停可看到完整值，旁边有复制按钮；其余文字不截断 | `Th` 的 `whitespace-nowrap`、`Id`/`IdText`；`e2e/gate.spec.ts` 和 `e2e/admin-targets.spec.ts` |
| 登录状态只由服务端说了算：页面启动时问 `getSession`，401 就是没登录（不是故障）；CSRF 令牌只放内存（Cookie 是 HttpOnly），每个改数据的请求自动带它和 JSON 内容类型，登录、第二步和改认证因素的响应换新令牌 | `@axiom/api` 的 `setCsrfToken` 与客户端中间件，`packages/app/src/session.tsx`；端到端的接口模拟按契约校验会话、等级、权限、CSRF、Origin，缺一个就拒绝 |
| 会话中途失效：只有报错的请求由当前会话发起时，`unauthenticated` 才清掉会话和账户的缓存，去登录页，登录后回到原来的页面（路径加查询）；账户切换或同一人换会话后迟到的 `unauthenticated` 不清掉新会话；只有登录流程的页面（登录、第二步、开启两步验证、注册）沿用它地址里经过校验的 `next`，受保护页面地址里的 `next` 是页面自己的参数；`next` 按浏览器的 URL 规则（含编码的 `..`）解析，只收落在本应用路径范围内的地址 | `createQueryClient`（`packages/app/src/providers.tsx`）里的 `SessionBoundQueryCache`、`MutationCache` 的 `onMutate`/`onError`、`endUnauthenticated`、`Gate`、`safeNext`；`providers.test.ts` 的迟到 401 回归、`safeNext` 的单元测试和 `e2e/console-pages.spec.ts` |
| 浏览器的会话由所有标签页共用：页面重新读到的会话换了用户或账户时，取消并清掉上一个账户加载的数据，页面从头开始（打开着的对话框和填了一半的表单不留）；同一账户的权限变了时页面保留（恢复码对话框不丢），但没人看的账户数据清掉、正在看的重新请求，失去权限的按「没有权限」显示；重新读会话失败时保留已有的会话，不把正在用的页面换成错误页 | `packages/app/src/session.tsx` 的 `sessionQuery`（比较前后会话的身份）、`Gate`（按身份重建、失败时保留）；端到端测试覆盖 |
| 只显示一次的内容和导出只回到发起请求的会话：发起时记下当时的用户、账户和会话，回答到达时绑定的对象变了就不显示、不下载，并取消请求；因素变更自己换会话，恢复码只按人判断 | `packages/app/src/session.tsx` 的 `claimSession`/`holdsSession`/`holdsIdentity`/`useSessionBound`；`totp-dialogs.tsx`、`console/src/pages/security.tsx`、`calls.tsx`、`statement.tsx`；端到端测试覆盖换人后不显示、不下载 |
| 退出登录失败要说清：没有退出、原因、可以重试 | `packages/app/src/parts.tsx` 的 `SignOutError`，侧栏和第二步页都用；端到端测试覆盖 |
| 失败只按契约的错误码翻译，服务端的英文 `message` 不给用户看；每个错误码都有译文 | `packages/ui/src/locales/zh.ts` 的 `satisfies Record<ErrorCode, string>`：契约加一个码，不写译文就编译不过 |
| 金额是精确的整数文本，显示和输入都不经过浮点数 | `useFormat().money`、`parseMoney`、`moneyInput`；单元测试含 int64 的上下界 |
| 只显示一次的内容（key 明文、恢复码）放在不能用 Esc 或点背景关掉的对话框里，用户确认后才关；生成恢复码的请求还没回来时，发起请求的对话框也不能关；确认时读新会话失败，恢复码仍留在屏幕上并说明原因，可以重试 | `Dialog` 的 `dismissible`、`packages/app/src/totp-dialogs.tsx`；端到端测试覆盖 |
| 两步验证的二维码在本地用 `qrcode-generator` 画成 SVG 的 `<path>`，不拼 HTML 字符串，也不把密钥交给外部服务 | `packages/app/src/qr.tsx` |
| 验收门槛的无障碍和不破版：控制台和后台的每个页面、每个对话框在深浅色、中英文下没有 WCAG 2.2 AA 的违规，用最坏数据在 360 到 2560px 宽下页面不出现横向滚动，列标题不换行，侧栏铺满长页面，表格行内的按钮和链接不跑出表格自己的滚动框 | `e2e/gate.spec.ts`（axe-core 与溢出检查），页面列表在 `e2e/console-scenes.ts` 和 `e2e/admin-scenes.ts` |
| 站点的品牌色自动配出深浅两套强调色和主按钮，对比度不低于 4.5:1，主按钮与页面、面板、对话框任一底色对比不到 3:1 时加边线（`--btn-line`）；不是 `#rrggbb` 的写法被拒绝。后台的系统设置没有这一项，颜色经 `PUT /api/admin/v1/sites/{site_id}/brand` 设置，控制台从 `GET /api/console/v1/site` 的 `brand_color` 取用；没有时用默认配色 | `packages/ui/src/brand.ts` 的 `brandStyle`，由 `AppShell` 和 `AuthFrame` 通过 `useBrandStyle` 写到文档根元素，页面外的恢复码对话框也继承；单元测试 `brand.test.ts`（极端色与色彩网格）；`e2e/gate.spec.ts` 对 5 种极端品牌色做 axe 对比度检查，`e2e/shots.spec.ts` 截图 |
| 后台的每个改数据的操作都填理由、有确认（高风险的要输入对象的名字）、防重复提交；一个操作一个 Idempotency-Key，对话框打开时生成，发出后锁定表单，结果未知时原样重发，只有服务端明确拒绝后才换新；按修订号改的对话框收到 `stale_revision` 要重新载入才能再保存；结果标明是不是之前已完成的那次 | `admin/src/change.tsx` 的 `ChangeDialog`，状态转换在 `admin/src/attempt.ts`（规则见[前端](../docs/frontend.md)「决策与理由」）；端到端的模拟按契约要求 Idempotency-Key 和理由，同一个键带同样的请求再来就返回存下的结果并带 `Idempotency-Replayed`，换了请求就拒绝（`idempotency_key_reused`）；「同样」按服务端的指纹判断：整数按原样比较，供应商的登录只比成员名、不比密钥，地址先规范化，与 Go 共用 `internal/platform/idem/testdata/same-request.json` 里的样例 |
| 改数据的对话框在结果未知时，请求（含幂等键）留在内存里，关闭后重开、会话过期重新登录后，该改动的对话框只提示“上次的改动结果未知”，可原样重发或放弃，不给新表单 | `admin/src/unsettled.ts`、`admin/src/change.tsx` 的 `UnsettledChange`；`e2e/admin-change.spec.ts` |
| 后台的所有操作要求通过两步验证的会话：没有第二因素的账户只能到开启两步验证的页面，恢复码对话框在页面之上，换了会话也不会丢 | `packages/app` 的 `Gate` 的 `enroll` 阶段与 `RecoveryCodesHost`；`e2e/admin-access.spec.ts` |
| 接口只经 `@axiom/api` 调用，错误统一成带状态码和请求 ID的 `ApiError`，状态码 0 表示没有收到响应；收到了但解析不了的响应是普通的 `Error`，没有状态码。都保留原始错误作为 `cause` | 生成的类型不手改；`just generated` 检查 |
| 依赖用精确版本，发布满 7 天才能装上；依赖的安装脚本不运行 | `pnpm-workspace.yaml` 的 `minimumReleaseAge` 和 `strictDepBuilds`：需要安装脚本的依赖让安装失败，必须明确决定；目前没有放行任何依赖 |
| 认知复杂度不超过 15，圈复杂度不超过 12，函数不超过 60 行，文件不超过 500 行，与 Go 一侧相同 | 认知复杂度由 Biome 的 `noExcessiveCognitiveComplexity`（`biome.json`）查；圈复杂度和长度由 `scripts/limits.ts` 查，覆盖 git 不忽略的每个 TypeScript 和 JavaScript 文件（生成的 `*.gen.ts` 除外），长度不计空行和注释，测试不限函数长度。理由见[前端的「决策与理由」](../docs/frontend.md#决策与理由) |
| 没有没人用的文件、导出和依赖 | knip（`knip.jsonc`）；包只给本仓库的应用用，入口文件的导出也要有应用引用 |
| 没有重复代码 | jscpd（`.jscpd.json`），50 个词法单元以上算重复；测试和端到端的模拟（`e2e/`）不查 |
| 首屏脚本、每多开一页增加的脚本、每页字体不超过[前端性能指标](../docs/frontend.md#前端性能指标) | `scripts/budget.ts` 按程序发送时的压缩（`scripts/compress.ts`，brotli q11）后的大小计脚本，字体取 woff2 文件本身的大小；首屏算入口页加载的每个脚本和它们的静态依赖，再加入口地址打开的页面（控制台的概览、后台的首页）；官网另算首屏之后经动态导入能加载的全部块和资源（合计）和每个这样的块。每页字体是这一页可能加载的字体文件去重后的和（`scripts/font-budget.ts` 读 `fonts.json`）：这一页所用字形的全部文件，因为接口返回的文字能触发任何分片，另列本页文案用到的部分，超出时列出每个文件；首屏预载的字体单独打印，入口页预载的必须正好是 `fonts.json` 标了 `preload` 的本应用文件（控制台和后台为空）；构建里缺了 `fonts.json` 列出的文件也失败 |
| 每页能显示的字都在这一页的分片里 | `scripts/fonts/coverage.test.ts` 用和管线同一份取字代码（`scripts/fonts/text.ts`、`routes.ts`）比对 `fonts.json`，缺字就失败并提示重跑 `pnpm --dir web fonts`：翻译文件添了字，或控制台、后台的某页代码开始用一个它原来不用的键，都会触发；`sources.json`、`build.py`、`text.ts`、`routes.ts` 或 `requirements.txt` 改了而产物没重生成（`fonts.json` 记着它们的指纹）也失败；源字体本来就没有的字记在 `fonts.json` 的 `absent`，落到后备字体 |
| 主要页面的 LCP、CLS，常用操作的 INP，调用日志和用户列表各 1000 行滚动的掉帧和长任务不超过[前端性能指标](../docs/frontend.md#前端性能指标)：LCP、CLS 和概览页的主线程 CPU 时间超出上限才失败，INP、长列表滚动只报告（INP 和掉帧随共享 CI 机器的负载摆动），数字写进测试的注解；它是计时，单独在 CI 的 `vitals` 任务里用 `just web-vitals` 一次一个测试地跑，普通分片排除它（`playwright.vitals.config.ts`） | `e2e/vitals.spec.ts`：web-vitals 的 IIFE 构建在页面脚本之前注入；手机档（Pixel 5、慢速 4G、CPU 降速 4 倍）和电脑档（10 Mbps）把其余页面各测三次，每次夹在前后两次概览页加载之间，按这两次概览页 LCP 的平均值折算到设定上限的基线 run 后取中位数，折算系数最多为 1；概览页在不降 CPU 下（降速的忙等会按宿主机调度往 `tdur` 里加空转）从 Chrome trace 取主线程任务的 `tdur`（不含被换下的时间）：PR 和列车里与 `BASE` 提交的概览页（`playwright.vitals.config.ts` 用 `scripts/ci/vitals-base.sh` 并排提供）交替加载五对，本树中位数比基准中位数不超过 1.25，本树中位数另有绝对兜底 1020/1200 毫秒（干净构建的三倍）；`main` 上没有基准，只报告；它的 LCP 和 CLS 另取三次按本档降速的加载，LCP 只报告；原始值、参照值、折算结果和 CPU 时长写进输出；INP 在 CPU 降速 4 倍下取该操作的最差值 |

## 字体

字体都自己托管，按页面用到的字裁剪；字体族名只写在 `packages/ui/src/css/fonts.css`。

- `scripts/fonts/sources.json` 是源字体清单：每个源的下载地址和 SHA-256（源是压缩包时再写 `member`：取出哪个文件）；页面（`pages`：每个命名空间选哪些翻译键）；路由（`routes`：应用的入口，`routes.ts` 从它找出每个按需加载的页面和它能显示的键，字形用 `console/*` 指这个应用的所有页）；字形（`faces`：字体族、字重、轴、哪些页面用、取哪些字、`preload` 哪些页面的首屏预载它）；回退字体（`fallbacks`：本机字体，管线按源字体的度量写上覆盖值）。
- `pnpm --dir web fonts` 在 `~/.cache/axiom-fonts` 建虚拟环境、按 `requirements.txt` 的哈希装 fontTools，下载并核对源字体（不入库；MiSans 的压缩包约 217MB，只下载一次），运行 `build.py`，约一分钟。它重写 `packages/ui/src/css/fonts/` 下的 woff2、`fonts.json`、`coverage.txt`，以及 `fonts.css` 标记行以下的 `@font-face`；同目录的 `LICENSE-MiSans.txt` 是手写的，管线不动它。重跑结果逐字节相同。改了翻译文件里的中文、`sources.json`，或检查说某页缺字，就重跑并提交产物；CI 不跑 Python。
- 标了 `split` 的字形（中文）先按「哪些页面用到这个字」切开，再把分片按代价合并（见[字体](../docs/frontend.md#字体)）：一个字只在一个文件里，`unicode-range` 让页面只下载它用到的分片。
- MiSans 的版权声明和协议在 `packages/ui/src/css/fonts/LICENSE-MiSans.txt`，官网页脚的「使用了 MiSans 字体」是许可要求的，不能删（[字体](../docs/frontend.md#字体)）。
- 换中文正文字体：改 `sources.json` 里 `cjk-body` 的地址和哈希，和 `fonts.css` 里 `--family-cjk-local` 的第一个名字（装在本机的同款字体），再重跑。

## 测试

- 接口模拟的机制集中在 `e2e/mock.ts`（会话、权限、CSRF、Origin 的校验，后台操作的 Idempotency-Key 与理由），各界面只写自己的路由和数据：`e2e/console-api.ts`、`e2e/console-data.ts` 和 `e2e/admin-*.ts`（数据：常见、空、最坏）。路径、方法、请求体和每个响应的状态与 JSON 都由契约的类型约束，与契约不符就编译不过。
- 验收门槛的截图：`AXIOM_SHOTS_DIR=<目录> pnpm --dir web e2e shots`（可用 `-g "admin users"` 缩小范围），写出控制台和后台每个页面在四种宽度、深浅色、中英文下的截图，以及最坏数据和极端品牌色（改令牌）的截图，只给人看，不入库。
- 对照设计稿的截图：`AXIOM_DESIGN_DIR=<设计稿目录> AXIOM_DESIGN_OUT=/tmp/design pnpm --dir web e2e design.spec -g "console keys"`（文件名要写 `design.spec`：只写 `design` 会匹配到路径里带 design 的工作区，跑起整套测试）。每个对照场景、宽度、深浅写出 `board.png`（画板）、`page.png`（页面）和有差异时的 `diff.png`，`summary.json` 记不同像素的占比、用到的产品裁决和画板被拦下的请求；`AXIOM_DESIGN_OUT` 没设时写到系统临时目录的 `axiom-design/`。画板用它自己的组件类渲染，在一次性、断网的浏览器上下文里，除 `board.local` 外的请求一律中止，`support.js` 和画板引用的引擎脚本不运行；参考字体是 `pnpm --dir web fonts` 下载并核对过哈希的源字体，没下载过就先跑一次。哪个场景对哪块画板写在 `e2e/design-map.ts`，产品裁决造成的差异按区域登记在 `e2e/design/rulings.ts`（每条写依据），登记之外只允许抗锯齿量级的差异。没设 `AXIOM_DESIGN_DIR` 时全部跳过，CI 不跑；设计稿文件不入库。规则见[前端](../docs/frontend.md)「截图对比」。
- 真实服务的旅程（`journey/`、`playwright.journey.config.ts`）：一个测试带着浏览器走一遍新用户的第一天，对着真实的 Axiom 进程，不模拟接口：用邀请码注册、登录、创建 key、充值、调用一次模型，在调用记录和额度流水里看到这次调用和扣费。服务端在 `cmd/axiom/journey_test.go`（构建标签 `journey`）：真实的 PostgreSQL 与 Valkey（容器），`axiom site`、`axiom user` 建站点和邀请人，目录、供应商、定价照调用路径测试的世界，只有外部的两样是假的：供应商（回放录制的流）和 EasyPay 网关（接单、回答查询、由旅程通知它「已支付」）。`journey/node.ts` 在 Playwright 的 globalSetup 里编译并启动它，节点写出地址后才开始测试，标准输入一关（含旅程崩溃）节点就停。站点域名是 `localhost:<端口>`，因为只有 localhost 在明文 http 下发会话 Cookie；API 域名是同一端口的 IP。控制台还没有充值页面（#317），所以充值这一步用浏览器会话调控制台的接口（订单、验证），再在页面上看余额。要 Docker：本地用 `just web-journey` 在宿主上跑（`just web` 不含它）；CI 的 `journey` 作业由 `withcluster` 在宿主上起数据库，节点和 Chromium 在 web 容器里跑（独立网络命名空间，#363），经 `scripts/ci/web-container.sh` 按网桥地址连数据库，只在列车和 `main` 上跑，失败时上传 `test-results/`（浏览器 trace 和 `journey-node.log`）。
- 单元测试（Vitest）测不需要浏览器的逻辑，和代码放在一起（`*.test.ts`）。
- 端到端测试（Playwright）对着嵌入程序的构建产物跑：`internal/platform/webui/cmd/serveweb` 起程序自己的静态服务（公网监听上是官网和控制台，管理监听上是管理后台），发送服务端真实的缓存头、压缩和内容安全策略；接口用 `page.route` 模拟，不需要数据库。用 `just web` 跑全部检查，或用 `just web-e2e` 单跑端到端：两者都先构建并嵌入，再把构建写入的版本号传给测试。分片使用 `just web-e2e "1/2"`（内部为 `pnpm --dir web e2e --shard=1/2`），通过结果按完整命令缓存，分片序号和总数不同就不能复用；测试自己挑三个应用及其服务端口都空闲的端口（随机选，两个工作区同时跑不会撞），不设固定端口，也不会拿别的工作区的服务测；`AXIOM_WEB_PORT` 设了就用它，端口已被占用时测试失败。开发服务器要在另一个工作区同时跑，设环境变量 `AXIOM_WEB_PORT` 把三个应用的端口整体挪开（默认 `5173`，测试用的服务端口在其上加 1000），例如 `AXIOM_WEB_PORT=5273 just web`。并行的 worker 上限是 8 个（`playwright.config.ts`）：Playwright 默认按整机核数的一半开，在 96 核的 CI 机上是 48 个，几个作业同时跑时每项检查被拖到超时（#297）。同一台 8 核机器、负载 10 到 20 时测 `gate.spec.ts` 里 axe 扫描的耗时：1 个 worker 中位 1.0 秒、最大 2.0 秒；4 个 1.2 秒、2.6 秒；8 个 1.8 秒、3.7 秒；48 个 14.5 秒、29 秒，20 项超过 30 秒。扫描本身不变，慢的是 worker 比核多后互相抢 CPU，所以靠上面的 worker 上限解决，不单独放宽超时、不重试。上面这组数字是限制行数之前的扫描；#624 之后每个表只扫前 12 行（`e2e/gate.spec.ts`），扫描更轻，worker 上限的结论不变。CI 的两个分片同时跑时，合计最多用配额的四分之三（8 核上六个，每个分片三个），留四分之一给浏览器和测试进程：构建 41922423312 里两个分片各四个 worker，页面在 5 秒断言时还停在加载状态，trace 显示测试进程 2.7 秒没拿到 CPU；8 核机器上 32 个忙循环下，两个 4-worker 运行 60 项里失败 2 项，两个 3-worker 运行 60 项全过，两个 2-worker 运行也全过但同一项慢 53%。单个分片（包括 `gate_stress` 的不分片运行）仍用完整的上限。

定价页的发布列表显示受众、当前价格版本、待确认与暂停状态；窄屏把受众、状态和完整版本放在模型下方，操作留在可视范围，宽屏分列。`Table` 的滚动区按 [MDN 的键盘滚动说明](https://developer.mozilla.org/en-US/docs/Web/CSS/Reference/Properties/overflow#accessibility)可聚焦并带区域名称，纯读价表也能用键盘横向滚动；该区域的 `tabIndex` 是 Biome 非交互元素禁令的局部例外。验收夹具包含受众发布及当前价格，场景覆盖等级/账户规则和预览结果。规则列表的浏览器断言按有名称的规则表定位，避免把发布表或预览表的行算入；启用规则的编辑清除尚未支持的限制，原样停用则保留既有目标、受众、方法与限制，两条路径分别验证请求和保存结果。发布对话框比较规则原价和保本提案，需确认模型并填写理由，低价例外显式勾选。调用列表在费用下显示冻结价格版本，旧节点不返回字段时显示不可用。

概览的最近报错通过 `status=error&limit=5` 向服务器查询；只有返回可选的 `status_filter=error` 才展示结果，旧节点未回显时显示不可用，不在浏览器筛选一页普通调用。筛选、聚合与导出 API 的边界见[记录与告警](../docs/domains/observability.md#用户调用记录查询与导出)，本轮不增加视觉界面。

控制台概览的 `announcements.tsx` 用原生 details 按需请求本站公告；加载、空、失败（可重试）、旧节点 404 或缺少 data 均有文本状态。公告标题和正文按文本换行并允许任意长词折行，不解析 HTML，也不要求已读。此入口按 #316 只做未加样式的数据阅读，不扩展后台视觉界面。

画板机械转换：`AXIOM_DESIGN_DIR=/path/to/design pnpm --dir web boards`，导入清单见 `packages/boards/catalog.ts`。
不指定目录时，从 `packages/boards/inputs/` 的固定输入重新生成；`pnpm --dir web boards --check` 校验产物，单元测试在 CI 执行同一校验。
生成目录带 `@generated` 标记，禁止手改；演示用 `pnpm --dir web --filter @axiom/boards dev` 打开 `/<Board>`，整板保留原始演示逻辑，随路由加载。
画板及引擎随路由加载；演示字体复用 F1a 固定源，按摘要校验后原子写入共享下载缓存，归档从已校验内存解包，生产页面的构建入口不变。
`pnpm --dir web e2e --config=playwright.boards.config.ts` 对固定画板做 1440/390、深浅色截图和真实 CSP 交互检查，保留完整动画。
参考演示不压缩 CSS，保留透明颜色的小数精度；画板检查独立运行，不进入产品端到端分片。
AxApp 画板及生成的外壳固定宽 1440px，AxAppPhone 固定宽 390px；两种浏览器视口验证截取和裁切，不改变画板的布局宽度。渲染区域比较会断言两侧框架都保持清单的截取宽度。

产品页面使用区域入口，例如 `@axiom/boards/AxApp/regions/Shell` 和 `@axiom/boards/AxApp/regions/Overview`；手机对应 AxAppPhone。
区域由手写的 `packages/boards/manifest/<页面>.ts` 描述（每页一个文件，默认导出该页的区域，`regions.ts` 按文件名汇总）：`roots` 选择原始节点，`omit` 去掉区域外节点，`children` 保留容器并把内容变为插槽，`slots` 按属性名再给出其他插槽（如标志），由页面传入内容。
脚本绘制的页面声明 `render: { root: ".p-logs", actions: [["go", "logs"], { click: ".lgr" }] }`：复用隔离画板浏览器，依次调用画板方法或点击首个匹配节点，再从 `root` 的运行时 DOM 中按 `roots` 切区域。`props` 可设置画板属性，`viewport` 默认为 1440×900；手机清单明确设置 390×900。只有选中的条目含 `render` 时才启动一个共享 Chromium（首次运行需 `pnpm --dir web exec playwright install chromium`），每条在独立离线上下文中运行；时间固定为 2026-10-24 12:00（上海），随机数、源字体和 5 秒收敛时间与画板证明一致。`boards --check` 同样重放渲染，不缓存快照；画板脚本始终只在浏览器中执行。
渲染来源与静态来源共用孔洞、重复行、文案、CSP 和 CSS 筛选；保留模板类孔洞的其他状态供 CSS 筛选，避免浅色快照丢掉深色规则。画板的 ref、事件绑定和定位标记不会成为组件行为，产品通过 `elements` 声明自己的事件孔洞。`Logs`、`LogsFilters`、`LogsDetail`、`LogsAttempts` 分别固定列表、筛选弹层、详情和重试线路详情；手机详情的 `.lgsheet` 根由调用方放在 `.fr` 下，与画板一致。各区域的 `.demo.ts` 从该状态取数据。
`text` 只替换所选元素的直接文本节点，不破坏货币、小数等子元素；`attributes` 替换指定属性。多处匹配按文档顺序生成编号字段。
`elements` 补画板没画的产品需要：按选择器（含根节点）换标签（如可点击的 `<div>` 换成 `<button>`）、设置属性（值可含孔洞），`tag: "link"` 渲染页面通过 `link` 传入的路由链接。布尔 ARIA 状态的孔洞类型为 `boolean`。选择器不匹配即生成失败。
`repeats` 把连续的同级节点提取为类型化数组，原有不同节点结构成为联合类型，渲染模板仍由工具复制；数据字段 `fN` 对应模板中按顺序遍历的属性和文本，具体值见旁边的 `.demo.ts`。
`values` 提供原有模板孔洞的类型化数据、事件及 ref；区域不导入整板控制器。页面只传数据和行为，不复制 JSX。

CSS 只保留可匹配本区域节点的规则、分组及所用关键帧，声明原文不改。每个节点保留原类名，并附上 CSS Module 的局部标记；选择器加零优先级的 `:where` 标记，跨区域祖先作用域和级联顺序因此不变。
动态状态参与筛选，`page` 限制页面状态类，`classes` 可明确孔洞的其他候选类；清单变动后重跑转换器。
中文文本及属性生成稳定内容键和 `.zh.ts`，组件通过调用方的 `t` 取文案；`.en.ts` 生成相同键的空值，页面卡在清单的 `english` 中填翻译后再生成，不直接编辑产物。缺英文键会使类型检查和 `boards --check` 失败。
文案入口为 `@axiom/boards/AxApp/regions/Overview.zh`、`Overview.en`（手机同理）。
`.demo.ts` 仅为截图数据，产品入口不要导入；`/<Board>/regions` 是桌面、手机外壳加概览的参考路由，仅该路由接回原画板控制器。
转换器单测比较完整区域 DOM、核对 CSS 规则数量，并用独立 Vite 构建检查模块隔离、报告逐文件及合计 brotli 体积（React 外置）。新增引擎保留原始固定输入，仅适配 CSP 的 DOM 写入；原始画板和引擎从不在 Node 中执行。
