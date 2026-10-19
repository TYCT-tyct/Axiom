上次复盘：2026-10-14

# 错题

已经发生、还没转成检查或规则的问题。进门、转正和过期的规则见 project-workflow 的复盘方法；转正后删掉条目。上一轮的问题都已转成检查（`just secrets`、`just up` 的密码保护、`just generated`）或 [CONTRIBUTING](../CONTRIBUTING.md) 的规则。

## 待处理

- **2026-10-19 测试扫描全进程的 goroutine**：`TestFailedStartLeavesNoRecorderWriter` 用 `runtime.Stack(_, true)` 扫描全部 goroutine 找 `Recorder.writer`，判断依据是整个进程的状态，而不是这次 `serve` 调用自己的对象；#327 把测试并行后，它看到其他测试节点的写入器，合并车失败（train/main/39266027524-2）。[CONTRIBUTING](../CONTRIBUTING.md)「时序」写的是「不用 `runtime.ReadMemStats` 之类的全进程计数衡量一次调用的分配」，规则在，没有检查。该测试已改成断言本次调用创建的 `Recorder`（`Done()`）。待加的检查：lint 禁止 `_test.go` 中出现 `runtime.Stack(…, true)`、`runtime.NumGoroutine`、`runtime.ReadMemStats`（`alloctest` 除外）。
