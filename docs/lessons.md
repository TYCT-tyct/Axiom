上次复盘：2026-10-14

# 错题

已经发生、还没转成检查或规则的问题。进门、转正和过期的规则见 project-workflow 的复盘方法；转正后删掉条目。上一轮的问题都已转成检查（`just secrets`、`just up` 的密码保护、`just generated`）或 [CONTRIBUTING](../CONTRIBUTING.md) 的规则。

## 测试按全进程状态判成败

`TestFailedStartLeavesNoRecorderWriter` 用 `runtime.Stack(_, true)` 扫描全部 goroutine 找 `Recorder.writer`，判断依据是整个进程而不是这次 `serve` 调用自己的对象；#327 把测试并行后，它看到其他测试节点的写入器。该测试已改成断言本次调用创建的 `Recorder`（`Done()`，#338）。同类问题在 #313 的付得起分配测试中再次出现：`testing.AllocsPerRun` 也统计全进程，现已改为 `alloctest.Measure` 的隔离子进程。待加的检查：lint 禁止 `_test.go` 中出现 `runtime.Stack(…, true)`、`runtime.NumGoroutine`、`runtime.ReadMemStats`、`testing.AllocsPerRun`（`alloctest` 除外）。

证据：合并车 train/main/39266027524-2 的 go-test 失败（`serve_test.go:390`）
哪一层没拦住：CONTRIBUTING「测试与时序」只写了「不用 `runtime.ReadMemStats` 之类的全进程计数」，没有 lint；#327 的评审没看出并行化影响了这个测试
出现：2026-10-19

## 真实依赖的内容断言与生产期限竞速

`TestDeferredChecksReportOnly` 为了断言启动错误，用 `server.Config` 加 `kv.Open` 建客户端，绕过了 `kvtest.Open`/`OpenIn` 的一分钟默认期限；`TestTimeoutReturnsAndTheBreakerRecovers` 则把预热、超时和恢复都交给同一个 20ms 客户端。两条路径都让正常回复在 CI 负载下与生产期限竞速。现在真实服务端的测试客户端统一经过 `kvtest.Deps`，未指定时用一分钟；生产期限和熔断恢复由已有受控假服务端及 `synctest` 验证。真实服务端的并发测试先用宽期限完成启动，再显式暂停写入、停止后台检查并切换到生产期限；冻结时钟使熔断一直打开，启动不受短期限影响。延迟策略检查从启动起冻结熔断时钟，等待后台检查退出后再推进时钟并执行一次测量，避免争抢半开探测，不靠睡眠或重试次数。

证据：合并车 run 39439661276 的 go-test 失败（`internal/platform/kv/outage_test.go:347`：`the node stopped serving over a policy it only reports: valkey unavailable: context deadline exceeded`）；PR #372 的 `47f2d7e` CI 在同一文件第 174 行失败（`TestTimeoutReturnsAndTheBreakerRecovers`：`run the count: valkey unavailable: context deadline exceeded`）
同类问题还出现在控制台流水导出：`TestStatementExportCutsInsideAPage` 通过 HTTP 守卫读取 10 000 条记录，把内容断言绑在生产的 15 秒请求期限上，列车负载下读到一半超时。两个满额导出测试改为在账户范围事务里直接调用 CSV 导出器，沿用测试上下文；小夹具继续覆盖 HTTP，受控测试继续验证期限、取消与写入失败，没有增加重试或延长期限。

证据：合并车 run [40791946142](https://github.com/TYCT-tyct/Axiom/actions/runs/40791946142) 的 go-test 在 `TestStatementExportCutsInsideAPage` 中报 `context deadline exceeded`，build 随之失败，PR #423 被移出队列。
哪一层没拦住：PR #358 只在客户端构造函数里设置宽期限，直接调用 `kv.Open` 的路径漏过；超时测试虽用 `CLIENT PAUSE` 控制失败阶段，预热和恢复阶段仍要求真实网络在 20ms 内回复；流水满额测试同样没有隔离内容正确性与生产期限。性能文档已禁止单元测试按绝对墙钟预算判失败，但尚未机械检查。待加的检查：测试中的直接 `kv.Open` 必须经过 `kvtest.Deps`；使用真实依赖验证大批量内容的测试不得沿用生产请求期限，期限行为单独用受控阻塞或虚拟时间验证。
`TestCallCSVUsesOneSnapshot` 经 HTTP 守卫导出 300 条调用，中途写入迟到的一条，整段跑在导出的 10 秒请求期限里，main 的完整 CI 负载下用尽期限。处理器在期限内调用的读事务提成 `consoleAPI.writeCalls`，快照测试和满额导出测试直接用测试上下文调用它，不再各自复制事务体。

证据：main run [41569157437](https://github.com/TYCT-tyct/Axiom/actions/runs/41569157437) 的 `panic: console: stream calls: timeout: context deadline exceeded`；本地让迟到的写入拖过期限，得到同一 panic。
出现：2026-10-20、2026-10-29、2026-11-03

## 凭证份额测试遗漏注册空档和路由投影外的占用

E3-3 最初把旧二进制、从未准入的新进程和重新加入的进程都编码成空值视图；交接测试先同步加入者，漏过了注册后、首次确认前的空档。排空检查又只遍历可路由快照，余额不足或停用后隐藏的凭证仍持有的在途和账号窗口被漏掉。本轮以明确的空列表、独立的加入状态和保留的上次成员列表区分三种状态，并遍历全部保留运行时。回归覆盖 `TestUnconfirmedJoinKeepsExistingCoreUsable`、`TestUnconfirmedDepartureDoesNotReserveRPM`、`TestHiddenCredentialsMustDrainBeforeMembershipConfirmation`。

证据：PR #433 的 de0a6a0 独立评审第 1、2 条；新增回归在修改实现前均失败，分别复现全停和提前释放加入者的份额。
哪一层没拦住：份额测试只覆盖加入者已确认后的顺序，没有覆盖注册但未确认、未确认就退出，以及凭证从可路由投影中暂时消失的阶段；设计没有区分“从未准入”和“保留历史占用”的成员状态。后续份额协议修改需保留这组注册与投影变化的时序回归。
出现：2026-10-29

## 供应商持久化说明在恢复旧分支时回退

证据：PR #420 的 `5703a9b` 把 `suppliers/README.md` 的清单投影、登录唤醒、状态行先于供应商行的锁顺序说明替换成旧版，独立评审要求恢复；旧扣费写入接口的名字也混回文档，而当前实现是 `reconcile.Ingest`。
哪一层没拦住：冲突解决后的自审只核对了新增位置协议，没有逐条检查同节原有的不变量；文档检查不核验「登录被替换时唤醒」这种正文引用。供应商模块此类合并应以 main 的持久化小节为底，核对投影、唤醒、锁顺序、状态格式、next_due 六项均保留，再加本次的位置协议；不能把旧分支的整段当冲突答案。
出现：2026-10-28

## 原生成本完整性误用售价上架门槛

#313 最初把供应商成本计划直接交给售价的严格报价路径。new-api 只公布标准档 token 价格；真实请求未指定服务档时可能接受目录的全部等级，目录也可能报告音频或搜索，于是正余额端点被当成 `supplier_balance` 暂时排除。原生成本现按最高已知 token 单价估算缺项，其他不能报价的原因单独记录；售价准入保持严格。回归用 new-api 形状的成本计划、真实默认的任意等级集合，并覆盖音频、搜索、缓存重叠、已知更高价格和溢出分类。

证据：提交 6e3ae54 的独立评审第 1 条；修复前默认档、音频和搜索三例均在余额充足时失败，溢出被记为暂时余额不足。
哪一层没拦住：路由测试的请求辅助函数固定标准档，端到端夹具又补齐成本价格，未保留适配器真实的不完整计划形状。今后扩展原生成本检查需保留这组不完整计划与默认请求的组合回归。
出现：2026-10-29

## 用户提醒不能削弱既有密码安全邮件

证据：PR #447 的 c827c87 独立评审第 1 条；因素偏好由 signed_in 会话修改，旧实现允许关闭密码变更邮件。`TestPasswordSecurityMailCannotBeDisabled` 保留关闭偏好后的必发安全邮件回归。
哪一层没拦住：偏好测试把“每类可关闭”直接推广到已有密码安全邮件，没有核对既有保证和偏好写入的认证等级。新增通知开关必须逐个列出可关闭消息与必发安全消息，并以较低认证等级的关闭操作验证原安全保证仍成立。
出现：2026-10-29

## 定价变更的验证遗漏支付迁移夹具和既有浏览器断言

证据：PR #447 的列车构建 [40849458430](https://github.com/TYCT-tyct/Axiom/actions/runs/40849458430) 在 billing 26 后删除 24 的标记，同样触发乱序迁移拒绝；PR #445 被合并车 [run 40847775911](https://github.com/TYCT-tyct/Axiom/actions/runs/40847775911) 移出队列：支付恢复夹具在 billing 27 后删除 24 的标记，触发乱序迁移拒绝；定价浏览器用例把发布表计入规则行数，并把原样停用误当成清除旧限制的编辑。
哪一层没拦住：模块测试只覆盖定价及路由，没有追到依赖 billing 迁移的 payments 恢复用例；浏览器截图与单元测试没有执行既有保存流程的断言。恢复夹具现保留当前 schema 供播种，只重放故障时点及以前的迁移并恢复后继标记；该夹具覆盖局部 DDL 重入与数据保留，历史版本升级需独立的固定版本夹具。浏览器按规则表定位并分别核验编辑与原样停用。后续新增 billing 迁移或改变规则保存语义时，需要把 payments 恢复用例和定价浏览器用例纳入受影响检查；待检查选择器能按这些跨模块依赖自动选择后移除此条。
出现：2026-10-29

## 默认压缩不能把预留字节上界当作实际 token 消耗

证据：PR #455 的 cf160d6 独立评审第 1 条；`TestDefaultCompressionPreservesServiceableRequests` 在修复前复现 5 KB 普通请求被改写、4.5 KB 系统提示被拒绝、默认输出等于上下文时全拒绝，以及两条溢出回退失败。`TestMiddleTextRetainsUnicodeEnds` 同时复现不同 UTF-8 宽度的首尾字符被丢失。
哪一层没拦住：最初回归只测显式启用和等宽 emoji 首尾，没有覆盖小窗口模型的默认请求、输出上限等于上下文和不同宽度的边界。默认的有损优化需用未超触发阈值的小请求与关闭策略逐字节对照，失败或资源不足不新增拒绝；首尾不变量需覆盖不同宽度的 Unicode 字符。上述测试保留这些组合。
出现：2026-10-29

## 后台可写配置不能沿用运维参数的信任边界

证据：#464 的 `699698b2` 独立评审第 1、2 条发现 SMTP 密码参与无盐幂等摘要，且后台可写目标直接拨号；诊断新增日志时也必须剔除 SMTP 回复里的凭证或收件人回显。信封加密本身不能保护摘要里的离线猜测校验器，保存时的地址检查也不能替代实际拨号时的 DNS 校验与 IP 钉扎。
哪一层没拦住：最初只验证密文存储、HTTP 响应和审计脱敏，没有检查幂等表中的派生数据，也沿用了仅来自环境变量时的直接连接。回归 `TestSMTPFingerprintExcludesPassword` 检查持久化摘要与密码无关，`TestSMTPDestinationBoundary` 覆盖后台保存和旧配置测试的私网拒绝，`TestTCPRevalidatesDNSAndPinsTheConnection` 覆盖 DNS 重绑定，`TestSMTPDiagnosticRedactsServerEcho` 检查服务器回显脱敏。后续新增凭证写入口必须同时检查幂等记录；新增后台可写网络目标必须验证实际拨号边界。
出现：2026-10-29

## 供应商修订必须保留创建、旧位置与首版授权的边界

证据：PR #461 的 a577486 独立评审第 1–3 条。原实现把待映射页的创建 POST 当作纠正，过期页面能无声覆盖他人的映射；版本 1 的 key 位置缺策略却继续被接受，旧页会投影为全拒；首版别名仍有列级 UPDATE 授权。`TestGroupMappingRevisionsAndIdempotentReplay`、`TestConcurrentMappingCorrectionsRequireCurrentRevision`、两个适配器的 `TestLegacyKeyListingRestartsBeforeProjectingPolicies` 和 `TestAliasRevisionsPreserveOwnershipAndHistory` 保留这些边界回归。
哪一层没拦住：只验证追加历史和最终快照，没有回放既有前端的新建语义、升级前的分页位置及旧迁移留下的权限。后续供应商修订改动须同时核对创建冲突与显式版本前提、旧位置重读、原始行不可改写；不能仅凭新表只追加就称整条历史不可变。
出现：2026-10-29

## 按路径豁免把 CI 里的安全改动放过了评审

#523（6f42a20）改的是发布镜像的签名和生产验签，文件全在 `.github/`、`scripts/ci/`、测试和文档里，流水线按路径标了「免评审」；PR 描述自己写着它改变部署门禁、不应免评审。合并后的独立深审发现阻断 1、应修 2：生产入口从 `.env` 取镜像而 Compose 拉的是环境变量覆盖的另一个；随交付物保存的 SBOM 没和已签名 attestation 的 predicate 绑定；签名负例在元数据比较处就退出，没到 cosign。

证据：提交 6f42a20（PR #523）合并后的独立评审；修复在本 PR 的 `scripts/ops/deploy.sh`、`scripts/ci/verify-release.sh` 和 `scripts/ci/signing-check.sh`，规则修改待负责人同意后另起 PR。

哪一层没拦住：免评审被当成路径判断。豁免的条件应是改动的影响；但改这条流程规则要负责人同意，这次没有取得，所以本分支撤回规则改动。现行 `CONTRIBUTING` 和流水线行为未变，这条错题保留到负责人批准规则修改、或流水线不再按路径自动标免评审。

出现：2026-10-31

## 集成探测的时间锚点与重复账本夹具放大列车负载

证据：PR #462 的列车构建 [40914818602](https://github.com/TYCT-tyct/Axiom/actions/runs/40914818602) 在健康探测恢复断言失败，并在第二份大账本夹具播种时达到 billing 包的十分钟期限。
哪一层没拦住：健康测试从组装前冻结的时钟推进 31 秒，实际熔断时间却由网关按真实时钟记录；两种账本计划测试分别播种 200 万笔数据，单包累计成本没有被局部测试暴露。集成测试应从实际事件时刻推进受控时钟，并用时钟偏移复现组装延迟；同模块同量级的计划验证共用大夹具，保留每种查询的完整计划断言。`TestGatewayHealthRemoveProbeRestore` 固定模拟两分钟时钟落后；`TestLedgerReadsGrowWithThePage` 同时验证 movements 和 statement，后续此类计划用例加在同一夹具下。
`cmd/axiom` 的通话导出夹具用 `INSERT … SELECT (jsonb_populate_record(…)).*` 复制一万行，PostgreSQL 对展开的每一列各算一次函数，单个测试播种 50 秒；改为 `CROSS JOIN LATERAL jsonb_populate_record(…)` 后约 2 秒。`internal/payments` 的播种还是旧写法。同一轮还有 42 个耗时的数据库测试没有 `t.Parallel()`，串行跑了约 95 秒，`cmd/axiom` 包在列车上超过十分钟（run [41581562012](https://github.com/TYCT-tyct/Axiom/actions/runs/41581562012)）。同一天 `internal/gateway` 在 main 上超时（run [41623800762](https://github.com/TYCT-tyct/Axiom/actions/runs/41623800762)），是同一类：279 个顶层测试没有 `t.Parallel()`，串行阶段 120 秒；并行阶段又被同时只放 3 个 rig 的信号量卡住，整包只用到约一个核。三次都是数据库测试漏了 `t.Parallel()`，还没有机械检查拦下（`paralleltest` 未启用）。
出现：2026-10-29、2026-11-03

## 只读状态和故障兜底的测试也为容器排队

证据：合并车构建 [41341652838](https://github.com/TYCT-tyct/Axiom/actions/runs/41341652838) 的 go-test 里，`TestStickyDoesNotRenewOnUse`、`TestKeyRateHoldsWhileTheCacheIsDown`、`TestSharedNodeRejectionsPreserveAccountHistory` 都在 120 秒时以 `Post .../containers/create: context deadline exceeded` 失败；同一个运行已经共享一个 Valkey，它们却各自又起容器，Docker 在负载下两分钟没有返回。

哪一层没拦住：`kvtest.Own` 被用来读 TTL、键列表和制造故障，而共享 Valkey 的 admin 用户当时不允许读；时序清单只写了不按墙钟、睡眠和重试判成败，没有说测试不该在只读或故障场景起容器。现在共享容器的只读 admin 由 `KVTEST_ADMIN_PASSWORD_FILE` 传入，只读用例走 `Server.Read`/`Server.TTL`，故障用例用 `kvtest.Down` 或关闭客户端。

出现：2026-11-01

## 请求期限被当成整个节点的期限

证据：同一构建里 `TestRequestsHaveADeadline` 的第一次请求按 300 ms 期限返回 503，下一次正常请求也返回 503「the request took too long」；300 ms 是通过 `withDeadline` 设在节点上的，节点上的每个请求都只有 300 ms，负载下正常请求也会超时。

哪一层没拦住：测试把「被阻塞的请求有短期限」写成了「节点有短期限」，而时序清单只要求不按墙钟判成败，没有指出期限应该属于哪一次请求。现在 300 ms 放在等待中的那次请求的 context 上，节点的下一次请求用各自的正常期限；只为测试存在的节点期限字段随之删除。

出现：2026-11-01

## 领域文档和实现一起偏离产品要求，评审没有回到产品文档

证据：#436（8447af3c）为了在滚动发布中只保留一个模型价，在 `docs/domains/billing.md` 的方法、保本线和决策理由里写成模型级成本上包络，`internal/billing/publication_price.go` 与 `internal/gateway/route.go` 按包络发布；`docs/product.md`「计价」的「同一模型走不同端点，售价可以不同」没有被改，但评审没有拿它对照。此后 #319 的端点定价一直无法落地，直到对照 `docs/product.md`「计价」才发现实现与产品要求相反。

哪一层没拦住：评审读了领域文档与代码，两者互相一致，于是把「文档写了」当成「要求允许」；没有把改动逐条对回产品文档的计价要求，也没有发现领域文档自己改写了产品要求而没有负责人批准。领域文档可以细化实现，不能替产品改售价模型；改售价、计费对象或对外价格形状时，评审必须同时打开产品文档的对应段落，并在 PR 里引用那一句。

建议（未改 `CONTRIBUTING.md`，需负责人同意）：在评审清单加一条「领域文档是否改写或偏离产品要求；改了就要有负责人同意的出处」，由流水线或评审人执行。本轮先记在这里，规则修改另起 PR。

出现：2026-11-02
