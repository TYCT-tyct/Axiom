上次复盘：2026-10-14

# 错题

已经发生、还没转成检查或规则的问题。进门、转正和过期的规则见 project-workflow 的复盘方法；转正后删掉条目。上一轮的问题都已转成检查（`just secrets`、`just up` 的密码保护、`just generated`）或 [CONTRIBUTING](../CONTRIBUTING.md) 的规则。

## 测试按全进程状态判成败

`TestFailedStartLeavesNoRecorderWriter` 用 `runtime.Stack(_, true)` 扫描全部 goroutine 找 `Recorder.writer`，判断依据是整个进程而不是这次 `serve` 调用自己的对象；#327 把测试并行后，它看到其他测试节点的写入器。该测试已改成断言本次调用创建的 `Recorder`（`Done()`，#338）。待加的检查：lint 禁止 `_test.go` 中出现 `runtime.Stack(…, true)`、`runtime.NumGoroutine`、`runtime.ReadMemStats`（`alloctest` 除外）。

证据：合并车 train/main/39266027524-2 的 go-test 失败（`serve_test.go:390`）
哪一层没拦住：CONTRIBUTING「时序」只写了「不用 `runtime.ReadMemStats` 之类的全进程计数」，没有 lint；#327 的评审没看出并行化影响了这个测试
出现：2026-10-19

## 测试客户端的访问期限漏过了 kvtest 的宽默认

`TestDeferredChecksReportOnly` 要断言启动返回的错误，所以没走 `kvtest` 的 `Open`/`OpenIn`，而是 `server.Config` 加 `kv.Open` 自己建客户端；放宽期限的代码只在 `kvtest` 那两个构造函数里，这条路拿到的仍是生产期限 `kv.Timeout`（20ms），末尾那次 `count` 于是在 CI 负载下超时。现在放宽只在 `kvtest.Deps` 一处，包内每个连服务端的 `kv.Open` 都经过它；那条等待循环也改成固定的几次访问，不再按墙钟睡和超时。待加的检查：lint 禁止 `internal/platform/kv` 的测试用未包 `kvtest.Deps` 的 `kv.Deps` 调 `kv.Open`。

证据：合并车 run 39439661276 的 go-test 失败（`internal/platform/kv/outage_test.go:347`：`the node stopped serving over a policy it only reports: valkey unavailable: context deadline exceeded`）
哪一层没拦住：PR #358 把宽期限只放进了 `kvtest` 的客户端构造函数，测试自己调 `kv.Open` 的启动路径没走它，也没有检查能发现这条路径；CONTRIBUTING「时序」只禁止按墙钟判成败，没写测试客户端该用哪个期限
出现：2026-10-20
