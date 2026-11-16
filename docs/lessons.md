上次复盘：2026-10-14

# 错题

只记可复发的机制和守护检查；已有通用规则见 [CONTRIBUTING](../CONTRIBUTING.md#检查清单)。尚无机械检查的明确标出，不把建议写成已落地。

## 测试按全进程状态判成败

哪一层没拦住：全进程 goroutine/分配计数混入并行测试。
证据：测试断言本次 Recorder 的 Done，分配用 `alloctest.Measure` 隔离子进程；禁止测试使用全进程计数的 lint 尚待补充。
出现：2026-10-19

## 真实依赖的内容断言与生产期限竞速

哪一层没拦住：内容正确性绑生产期限会把宿主负载当失败。
证据：真实 KV 走 `kvtest.Deps` 的测试期限，导出直接以测试上下文调用同一导出事务，小夹具保留 HTTP 覆盖；期限另用受控阻塞或虚拟时间测。账本追加 rig 等 25 秒、模型每秒 4 token、超额 100 token 保持等待包络，生产截断由 `TestATruncatedStreamConsumesNoMoreThanItsAllowance` 守护；直接 kv.Open 路径的机械检查待补。
出现：2026-10-20、2026-10-29、2026-11-03、2026-11-08

## 凭证份额测试遗漏注册空档和路由投影外的占用

哪一层没拦住：空视图混淆旧进程、未准入与重加入；可路由投影漏掉隐藏凭证占用。
证据：显式空列表、独立加入状态与上次成员列表，排空遍历全部运行时；保留 `TestUnconfirmedJoinKeepsExistingCoreUsable`、`TestUnconfirmedDepartureDoesNotReserveRPM`、`TestHiddenCredentialsMustDrainBeforeMembershipConfirmation`。
出现：2026-10-29

## 供应商持久化说明在恢复旧分支时回退

哪一层没拦住：整段接受旧分支会删掉已有不变量。
证据：解决此类冲突以 main 为底，逐项核对投影、登录替换唤醒、锁顺序、状态格式、next_due、接口名（当前 `reconcile.Ingest`）；正文语义尚无机械检查。
出现：2026-10-28

## 原生成本完整性误用售价上架门槛

哪一层没拦住：售价严格完整性不能直接用于供应商不完整成本计划。
证据：缺成本按最高已知 token 单价估算，其他报价失败单列，售价准入仍严格；回归保留真实 new-api 计划与默认任意等级，覆盖音频、搜索、缓存重叠、更高已知价和溢出分类。
出现：2026-10-29

## 用户提醒不能削弱既有密码安全邮件

哪一层没拦住：低认证等级可改偏好，不能推广成所有邮件可关。
证据：逐项区分提醒与必发安全邮件，`TestPasswordSecurityMailCannotBeDisabled` 验证关闭偏好后仍发送。
出现：2026-10-29

## 定价变更的验证遗漏支付迁移夹具和既有浏览器断言

哪一层没拦住：跨模块迁移夹具与页面表格语义会随定价变化。
证据：支付恢复只重放故障时点及之前迁移并恢复后继标记，当前 schema 用于播种；历史升级另用固定版本夹具。浏览器按命名规则表定位，分别测编辑清限制和原样停用保留限制；跨模块自动选择检查尚待补。
出现：2026-10-29

## 默认压缩不能把预留字节上界当作实际 token 消耗

哪一层没拦住：保守预留上界用作压缩触发会误改小请求。
证据：`TestDefaultCompressionPreservesServiceableRequests` 比未超阈值与关闭策略逐字节一致，覆盖小窗口、输出等于上下文、失败与资源不足不新增拒绝；`TestMiddleTextRetainsUnicodeEnds` 覆盖不同 UTF-8 宽度首尾。
出现：2026-10-29

## 后台可写配置不能沿用运维参数的信任边界

哪一层没拦住：加密挡不住无盐摘要猜测，保存时查地址挡不住拨号 DNS 重绑定。
证据：`TestSMTPFingerprintExcludesPassword`、`TestSMTPDestinationBoundary`、`TestTCPRevalidatesDNSAndPinsTheConnection`、`TestSMTPDiagnosticRedactsServerEcho` 分别检查摘要、私网、钉扎和回显；新凭证入口同时查幂等记录。
出现：2026-10-29

## 供应商修订必须保留创建、旧位置与首版授权的边界

哪一层没拦住：只追加新表不保证旧行不可改，也不能把创建当纠正。
证据：`TestGroupMappingRevisionsAndIdempotentReplay`、`TestConcurrentMappingCorrectionsRequireCurrentRevision`、适配器的 `TestLegacyKeyListingRestartsBeforeProjectingPolicies`、`TestAliasRevisionsPreserveOwnershipAndHistory` 覆盖创建冲突、修订前提、旧位置重读与原始行权限。
出现：2026-10-29

## 按路径豁免把 CI 里的安全改动放过了评审

哪一层没拦住：文件路径不代表风险，验签也可能验错实际交付物。
证据：CONTRIBUTING 按行为与决策定档；deploy.sh 从实际 Compose 取同一 digest，verify-release/signing-check 核对签名与 SBOM，负例须到达 cosign。流水线是否同样按行为判断仍须核对，不能由文档/CI 路径推断豁免。
出现：2026-10-31

## 集成探测的时间锚点与重复账本夹具放大列车负载

哪一层没拦住：组装前时钟不等于事件时钟；重复大夹具、串行测试和逐列重复 SQL 函数放大成本。
证据：`TestGatewayHealthRemoveProbeRestore` 模拟两分钟时钟落后，`TestLedgerReadsGrowWithThePage` 共用大夹具验证两种查询；播种用 LATERAL 只算一次，独立测试并行，分配测量复用结果。漏加 t.Parallel 的机械检查尚未启用。
出现：2026-10-29、2026-11-03

## 只读状态和故障兜底的测试也为容器排队

哪一层没拦住：读 TTL 或制造断线不应另起容器。
证据：共享 KV 只读 admin 经 `KVTEST_ADMIN_PASSWORD_FILE` 提供，使用 Read/TTL；故障走 `kvtest.Down` 或关客户端。
出现：2026-11-01

## 请求期限被当成整个节点的期限

哪一层没拦住：给节点短期限会误伤下一次正常请求。
证据：`TestRequestsHaveADeadline` 将短期限放在被阻塞请求的 context，下一请求用自己的正常期限，删除仅供测试的节点期限字段。
出现：2026-11-01

## 领域文档和实现一起偏离产品要求，评审没有回到产品文档

哪一层没拦住：实现与领域文档一致不代表符合产品。
证据：任务要求以产品为准；改售价、计费对象、价格形状时同时读产品对应段并在 PR 引用，冲突停止。专门的评审清单与自动核对尚无落地证据。
出现：2026-11-02

## 测试设的期限没到达实际触发的那个期限

哪一层没拦住：等待者期限不能改变脱离调用方执行的共享操作期限。
证据：`TestALookupKeepsItsCallersDeadline` 验证共享查找继承发起者 deadline，rig 预热也经自己的 KeyLookup；排查从实际报错处反查期限来源。
出现：2026-11-04
