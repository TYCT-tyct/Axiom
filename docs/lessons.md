上次复盘：2026-10-14

# 错题

已经发生、还没转成检查或规则的问题。进门、转正和过期的规则见 project-workflow 的复盘方法；转正后删掉条目。上一轮的问题都已转成检查（`just secrets`、`just up` 的密码保护、`just generated`）或 [CONTRIBUTING](../CONTRIBUTING.md) 的规则。

## 待处理

- **2026-10-19 全进程检查被改成并行**：#327 给 `cmd/axiom` 的测试加 `t.Parallel()` 时，连带改了 `TestFailedStartLeavesNoRecorderWriter`；它用 `runtime.Stack(_, true)` 扫描全部 goroutine，并行后看到其他测试节点的写入器，合并车失败（train/main/39266027524-2）。CONTRIBUTING「时序」已禁止用全进程状态判断一次调用，规则在，没有检查。候选检查：lint 禁止同一个测试函数里既有 `t.Parallel()` 又有 `runtime.Stack(…, true)`、`runtime.NumGoroutine`、`runtime.ReadMemStats`。
