上次复盘：2026-10-14

# 错题

已经发生、还没转成检查或规则的问题。进门、转正和过期的规则见 project-workflow 的复盘方法；转正后删掉条目。上一轮的问题都已转成检查（`just secrets`、`just up` 的密码保护、`just generated`）或 [CONTRIBUTING](../CONTRIBUTING.md) 的规则。

## 测试按全进程状态判成败

`TestFailedStartLeavesNoRecorderWriter` 用 `runtime.Stack(_, true)` 扫描全部 goroutine 找 `Recorder.writer`，判断依据是整个进程而不是这次 `serve` 调用自己的对象；#327 把测试并行后，它看到其他测试节点的写入器。该测试已改成断言本次调用创建的 `Recorder`（`Done()`，#338）。待加的检查：lint 禁止 `_test.go` 中出现 `runtime.Stack(…, true)`、`runtime.NumGoroutine`、`runtime.ReadMemStats`（`alloctest` 除外）。

证据：合并车 train/main/39266027524-2 的 go-test 失败（`serve_test.go:390`）
哪一层没拦住：CONTRIBUTING「时序」只写了「不用 `runtime.ReadMemStats` 之类的全进程计数」，没有 lint；#327 的评审没看出并行化影响了这个测试
出现：2026-10-19

## 真实依赖的内容断言与生产期限竞速

`TestDeferredChecksReportOnly` 为了断言启动错误，用 `server.Config` 加 `kv.Open` 建客户端，绕过了 `kvtest.Open`/`OpenIn` 的一分钟默认期限；`TestTimeoutReturnsAndTheBreakerRecovers` 则把预热、超时和恢复都交给同一个 20ms 客户端。两条路径都让正常回复在 CI 负载下与生产期限竞速。现在真实服务端的测试客户端统一经过 `kvtest.Deps`，未指定时用一分钟；生产期限和熔断恢复由已有受控假服务端及 `synctest` 验证。真实服务端的并发测试先用宽期限完成启动，再显式暂停写入、停止后台检查并切换到生产期限；冻结时钟使熔断一直打开，启动不受短期限影响。延迟策略检查从启动起冻结熔断时钟，等待后台检查退出后再推进时钟并执行一次测量，避免争抢半开探测，不靠睡眠或重试次数。

证据：合并车 run 39439661276 的 go-test 失败（`internal/platform/kv/outage_test.go:347`：`the node stopped serving over a policy it only reports: valkey unavailable: context deadline exceeded`）；PR #372 的 `47f2d7e` CI 在同一文件第 174 行失败（`TestTimeoutReturnsAndTheBreakerRecovers`：`run the count: valkey unavailable: context deadline exceeded`）
同类问题还出现在控制台流水导出：`TestStatementExportCutsInsideAPage` 通过 HTTP 守卫读取 10 000 条记录，把内容断言绑在生产的 15 秒请求期限上，列车负载下读到一半超时。两个满额导出测试改为在账户范围事务里直接调用 CSV 导出器，沿用测试上下文；小夹具继续覆盖 HTTP，受控测试继续验证期限、取消与写入失败，没有增加重试或延长期限。

证据：合并车 run [40791946142](https://github.com/TYCT-tyct/Axiom/actions/runs/40791946142) 的 go-test 在 `TestStatementExportCutsInsideAPage` 中报 `context deadline exceeded`，build 随之失败，PR #423 被移出队列。
哪一层没拦住：PR #358 只在客户端构造函数里设置宽期限，直接调用 `kv.Open` 的路径漏过；超时测试虽用 `CLIENT PAUSE` 控制失败阶段，预热和恢复阶段仍要求真实网络在 20ms 内回复；流水满额测试同样没有隔离内容正确性与生产期限。性能文档已禁止单元测试按绝对墙钟预算判失败，但尚未机械检查。待加的检查：测试中的直接 `kv.Open` 必须经过 `kvtest.Deps`；使用真实依赖验证大批量内容的测试不得沿用生产请求期限，期限行为单独用受控阻塞或虚拟时间验证。
出现：2026-10-20、2026-10-29

## 凭证份额测试遗漏注册空档和路由投影外的占用

E3-3 最初把旧二进制、从未准入的新进程和重新加入的进程都编码成空值视图；交接测试先同步加入者，漏过了注册后、首次确认前的空档。排空检查又只遍历可路由快照，余额不足或停用后隐藏的凭证仍持有的在途和账号窗口被漏掉。本轮以明确的空列表、独立的加入状态和保留的上次成员列表区分三种状态，并遍历全部保留运行时。回归覆盖 `TestUnconfirmedJoinKeepsExistingCoreUsable`、`TestUnconfirmedDepartureDoesNotReserveRPM`、`TestHiddenCredentialsMustDrainBeforeMembershipConfirmation`。

证据：PR #433 的 de0a6a0 独立评审第 1、2 条；新增回归在修改实现前均失败，分别复现全停和提前释放加入者的份额。
哪一层没拦住：份额测试只覆盖加入者已确认后的顺序，没有覆盖注册但未确认、未确认就退出，以及凭证从可路由投影中暂时消失的阶段；设计没有区分“从未准入”和“保留历史占用”的成员状态。后续份额协议修改需保留这组注册与投影变化的时序回归。
出现：2026-10-29
