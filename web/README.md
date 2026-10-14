# web

前端：官网、用户控制台、管理后台，和它们共用的包。要做什么、按什么验收见[前端与设计](../docs/frontend.md)，技术选型见[总体架构](../docs/architecture.md#技术选型)。

## 结构

| 目录 | 是什么 |
|---|---|
| `packages/ui/` | 共用的前端基础：翻译、外观与语言偏好、数字和时间的格式 |

## 命令

需要 Node 24 和 pnpm（版本见 `package.json` 的 `packageManager`；装了 pnpm 后它会自动换成这个版本）。

| 命令 | 做什么 |
|---|---|
| `just web` | 前端的全部检查：类型、lint、单元测试；`just check` 包含它 |

## 规则

| 规则 | 靠什么保证 |
|---|---|
| 页面上的文字全部来自翻译文件；中文是源文件，英文的键与中文完全一致 | Biome 的 `noJsxLiterals` 拒绝写在 JSX 里的文字；`Messages<typeof zh>` 让缺键、多键都编译不过；`t()` 的键有类型。属性里的文字（`aria-label` 等）这条规则查不到，靠评审 |
| 数字和时间按页面语言格式化，时间注明时区 | `useFormat` |
| 依赖用精确版本，发布满 7 天才能装上；依赖的安装脚本不运行 | `pnpm-workspace.yaml` 的 `minimumReleaseAge` 和 `strictDepBuilds`：需要安装脚本的依赖让安装失败，必须明确决定；目前没有放行任何依赖 |

## 测试

单元测试（Vitest）测不需要浏览器的逻辑，和代码放在一起（`*.test.ts`）。
