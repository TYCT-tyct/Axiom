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
| `packages/api/` | 由 `api/console.yaml` 生成的类型（`*.gen.ts`）和调用它的客户端 | — |
| `e2e/` | Playwright 端到端测试 | — |
| `scripts/budget.ts` | 体积预算检查 | — |
| `scripts/limits.ts` | 函数的圈复杂度和长度、文件长度检查 | — |

共用的 Vite 配置在 `vite.shared.ts`，各应用的 `vite.config.ts` 只写端口和路径。

## 命令

需要 Node 24 和 pnpm（版本见 `package.json` 的 `packageManager`；装了 pnpm 后它会自动换成这个版本）。第一次跑端到端测试前装一次浏览器：`pnpm --dir web exec playwright install --only-shell chromium`。

| 命令 | 做什么 |
|---|---|
| `just web-dev` | 开发服务器：官网 `:5173`，控制台 `:5174/console/`（官网的 `/console/` 也转给它，和线上一样从官网点进去），后台 `:5175`；接口请求转给 `just up` 起的程序（`:8080`） |
| `just web-build` | 构建三个应用到各自的 `dist/`，控制台带上同一棵代码树构建出的程序的版本号 |
| `just web` | 前端的全部检查：类型、lint、函数和文件长度、未使用的代码、重复代码、单元测试、构建、体积预算、端到端测试；`just check` 包含它 |
| `just generate` | 重新生成接口类型；`just generated` 检查生成结果已提交 |

## 规则

| 规则 | 靠什么保证 |
|---|---|
| 页面上的文字全部来自翻译文件；中文是源文件，英文的键与中文完全一致 | Biome 的 `noJsxLiterals` 拒绝写在 JSX 里的文字；`Messages<typeof zh>` 让缺键、多键都编译不过；`t()` 的键有类型。属性里的文字（`aria-label` 等）这条规则查不到，靠评审 |
| 颜色、圆角、阴影只用 `styles.css` 的变量 | Tailwind 自带的调色板已清空，写死的颜色没有对应的类可用 |
| 页面在 `vite.shared.ts` 的内容安全策略下工作：没有内联脚本，没有 `<style>` 元素 | 端到端测试在同一策略下加载三个应用，违规会成为控制台错误，测试失败；响应头必须与 `vite.shared.ts` 的定义完全一致，且不含 `unsafe-` |
| 保存的外观在首次绘制前生效，不先闪一下系统的外观 | `theme.js` 作为同源的普通脚本在 `<head>` 开头同步运行，读 `preferences.ts` 的同一个键；端到端测试拦下应用脚本后检查 |
| 构建产物的文件名带内容哈希，可以长期缓存 | Vite 给打包的文件命名；它不打包的 `theme.js` 由 `vite.shared.ts` 按内容算哈希。端到端测试检查入口页的每个脚本 |
| 每个页面有自己的标题；跳转后焦点移到页面，读屏软件会读出新页面 | `PageHeader` 和整页的状态设置标题，路由的 `onResolved` 调 `focusPage`；端到端测试检查 |
| 每个数据视图都有加载中、空、出错、没有权限四种状态 | 用 `DataState`；出错时显示请求编号和时间；服务端错误（5xx）和断网自动重试两次，其他错误不重试 |
| 切换页面不出现空白 | 两个应用的路由都从 `routerDefaults` 开始：保留当前页面，等待超过 150 毫秒时只在内容区显示骨架 |
| 部署后旧页面加载不到新的代码块时自动刷新一次 | 路由的 `lazyRouteComponent`：只在打开页面时刷新，悬停或聚焦链接引起的预加载失败不刷新；同一个代码块在本标签页只刷新一次，再失败就显示 `PageFailedState`；会话存储不可用时不刷新，直接显示。端到端测试覆盖 |
| 数字和时间按页面语言格式化，时间注明时区 | `packages/ui/src/format.ts` 的 `useFormat` |
| 接口只经 `@axiom/api` 调用，错误统一成带状态码和请求编号的 `ApiError` | 生成的类型不手改；`just generated` 检查 |
| 依赖用精确版本，发布满 7 天才能装上；依赖的安装脚本不运行 | `pnpm-workspace.yaml` 的 `minimumReleaseAge` 和 `strictDepBuilds`：需要安装脚本的依赖让安装失败，必须明确决定；目前没有放行任何依赖 |
| 认知复杂度不超过 15，圈复杂度不超过 12，函数不超过 60 行，文件不超过 500 行，与 Go 一侧相同 | 认知复杂度由 Biome 的 `noExcessiveCognitiveComplexity`（`biome.json`）查；圈复杂度和长度由 `scripts/limits.ts` 查，覆盖 git 不忽略的每个 TypeScript 和 JavaScript 文件（生成的 `*.gen.ts` 除外），长度不计空行和注释，测试不限函数长度。理由见[前端的「决策与理由」](../docs/frontend.md#决策与理由) |
| 没有没人用的文件、导出和依赖 | knip（`knip.jsonc`）；包只给本仓库的应用用，入口文件的导出也要有应用引用 |
| 没有重复代码 | jscpd（`.jscpd.json`），50 个词法单元以上算重复；测试不查 |
| 首屏脚本、每多开一页增加的脚本、西文字体不超过[前端性能指标](../docs/frontend.md#前端性能指标) | `scripts/budget.ts` 按 brotli 压缩后的大小计；首屏算入口页加载的每个脚本和它们的静态依赖 |

## 测试

- 单元测试（Vitest）测不需要浏览器的逻辑，和代码放在一起（`*.test.ts`）。
- 端到端测试（Playwright）对着构建产物跑：`vite preview` 起服务并发送内容安全策略，接口用 `page.route` 模拟，不需要数据库和 Go 程序。用 `just web` 跑：它把构建写入的版本号传给测试；端口已被占用时测试失败，不会拿别的工作区的服务测。
