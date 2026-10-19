上次复盘：2026-10-14

# 错题

已经发生、还没转成检查或规则的问题。进门、转正和过期的规则见 project-workflow 的复盘方法；转正后删掉条目。上一轮的问题都已转成检查（`just secrets`、`just up` 的密码保护、`just generated`）或 [CONTRIBUTING](../CONTRIBUTING.md) 的规则。

## 测试按全进程状态判成败

`TestFailedStartLeavesNoRecorderWriter` 用 `runtime.Stack(_, true)` 扫描全部 goroutine 找 `Recorder.writer`，判断依据是整个进程而不是这次 `serve` 调用自己的对象；#327 把测试并行后，它看到其他测试节点的写入器。该测试已改成断言本次调用创建的 `Recorder`（`Done()`，#338）。待加的检查：lint 禁止 `_test.go` 中出现 `runtime.Stack(…, true)`、`runtime.NumGoroutine`、`runtime.ReadMemStats`（`alloctest` 除外）。

证据：合并车 train/main/39266027524-2 的 go-test 失败（`serve_test.go:390`）
哪一层没拦住：CONTRIBUTING「时序」只写了「不用 `runtime.ReadMemStats` 之类的全进程计数」，没有 lint；#327 的评审没看出并行化影响了这个测试
出现：2026-10-19
