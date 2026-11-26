# pg

连接池、事务与角色的公开接口见 [platform](../README.md#范围)。本文件负责迁移执行与校验、行级安全策略的表达式和查询计划；租户数据分类以[安全文档](../../../docs/security.md#租户隔离)为准。

## 不变量

- 范围是平台、站点集合和账户集合的并集。没有范围或空集合不放行任何行；无效 UUID 数组报错。平台自有行的租户列可以为空，只有平台范围能读写。
- 授权给账户的所属站点只允许读取站点公开配置和读写共享计数，不能因此读到同站其他账户的数据，也不能读商户密文。
- `USING` 检查旧行，`WITH CHECK` 检查新行；更新的终态条件、命令类型、策略适用角色和表的列权限都属于安全边界。改写范围表达式不能改动它们。
- `FORCE ROW LEVEL SECURITY` 保留；查找角色只经固定路径的定义者函数读所需列。优化不能增加 `BYPASSRLS`、定义者权限或 `LEAKPROOF` 标记。
- 缓存计划可以跨事务复用，范围值不能缓存为常量。范围读取函数必须是 `STABLE`，不能是 `IMMUTABLE`。

## 决策与理由

选择**策略级无关联标量子查询**。策略直接把本行的列与已解析的范围参数比较：

```sql
USING ((SELECT platform.in_platform_scope())
    OR site_id = ANY ((SELECT platform.allowed_sites())::uuid[])
    OR account_id = ANY ((SELECT platform.allowed_accounts())::uuid[]))
```

站点数据省略账户分支；站点公开配置和共享计数再加 `site_id = (SELECT platform.home_site())`。公开数据和平台数据分别用 `(SELECT platform.in_any_scope())`、`(SELECT platform.in_platform_scope())`。函数只解析设置，子查询在策略里，行的列留在子查询外；因此每个范围读取出现处是独立 InitPlan，每次语句执行至多一次（可以因短路而不执行），不是每行调用。一个语句读多个表、别名或同时检查旧行和新行时可以有多组 InitPlan；不承诺全语句只存在一组。

InitPlan 的布尔参数按未知布尔值估计，避免原来的无列文本等式默认 0.5% 和账户占比再次相乘造成的极低估计。它是保守估计，不是精确的授权行数；尤其空范围可能估多，但执行仍拒绝全部行。这个取舍让带显式账户条件的有序分页仍有提前停止的收益，不要求绕过安全屏障或添加规划器扩展。

**实测**（2026-10-28，官方 `postgres:18.6-alpine` 容器，2 CPU、1 GiB，默认持久性和规划器设置，关闭并行扫描以比较单进程的表达式成本）。200 万行、每行额外 80 字节；目标账户 10 万行，另 1900 个账户各 1000 行。三次 `EXPLAIN (ANALYZE, BUFFERS)` 的执行时间中位数，单位 ms。平台范围命中 200 万行，账户范围命中 10 万行，16 账户范围命中 11.5 万行，空范围过滤全部；所有全表扫描均触碰 38,462 个块。实验把空集合写成 `{}`；Go 入口的空串也解释为空集合，真实 `pg.Scope` 的设置由下面的 200 万行结构测试另外覆盖。

| 方案 | 平台全部命中 | 账户命中和过滤 | 全部过滤 | 16 个账户的范围 |
|---|---:|---:|---:|---:|
| 原来的内联文本判断 | 1004.901 | 3322.623 | 1681.702 | 26336.940 |
| 直接稳定布尔值转换，数组仍直接读取 | 892.505 | 3571.889 | 2052.861 | 26095.402 |
| 策略级 InitPlan（选择） | 427.449 | 451.829 | 405.041 | 534.036 |

有序查询使用 `(account_id, seq)` 索引，倒序取 101 行；深页在约 75,000 行深处继续（`seq < 25000`），所有语句都带具体值，已经是 custom plan。下表是首页 / 深页的根节点 shared hit + read 块数；时间是深页中位数。

| 方案 | 平台块数；深页 ms | 单账户块数；深页 ms | 16 账户块数；深页 ms |
|---|---|---|---|
| 原来 | 10 / 610；17.790 | 10 / 610；46.172 | 10 / 610；333.118 |
| 直接布尔值转换 | 10 / 9；0.288 | 10 / 9；0.543 | 10 / 9；1.619 |
| 策略级 InitPlan | 10 / 9；0.225 | 10 / 9；0.263 | 10 / 9；0.341 |

原来三种范围的深页均为 Bitmap Heap Scan + Sort，分别估 6、63、72 行，实际扫描 24,999 行；InitPlan 均为倒序 Index Scan，估 1188 行，实际在 101 行停止。全被过滤的全表扫描由 1681.702 降到 405.041 ms（4.15 倍）；与同方案全部命中的 427.449 ms 接近，消除了逐行解析使过滤额外变贵的问题。空范围下的分页仍需检查查询指定账户的全部候选行：没有可返回的行就不能提前凑满一页；这不属于命中分页的有界读取承诺。

不选直接布尔值转换：只解决有序计划，解析数组的成本仍随被检查行数增长，而且 PostgreSQL 的布尔转换会把 `yes`、`true` 等原来不放行的值当成平台范围。不选只强制 custom plan：上述具体值计划已经退化；`enable_sort = off` 只能保护逐个调用的查询，且不能解决全表过滤成本。不选 support function / 扩展：增加部署权限和平台依赖；扩展统计不能消除逐行解析。不把标量子查询放进 `platform.in_scope`：#122 已测得阻止 SQL 函数内联，从约 2 秒退化至 15 秒。保留原迁移创建的 `in_scope`、`sees_site`，让 `pg.Check` 能通过函数依赖识别尚未转换或重新引入的逐行策略；新策略不再调用它们。旧 Go 节点不直接调用这两个函数。

**真实查询的补充取舍**：只改策略通过了编号交易的分页（平台和账户范围，首页 / 深页 407 / 407–408 块），但真实查询仍有以下限制：

- 历史交易的 `account_id` 与 `seq IS NULL` 强相关。超过 200 万笔的真实账本里，历史账户首页仍为 bitmap，流水页 1010 块、深页 614 块；InitPlan 不会修复账户与历史状态的独立性假设。保留 #405 的 `enable_sort = off`，并让分配重放在 `loadAllocation` 中每次加载设置一次，后续各页共用。`SET LOCAL` 持续到调用方事务提交或回滚，包括重放后的写入和其他模块在同一事务中的后续查询；退款接口的调用约定见 [billing](../../billing/README.md#额度批次的分配与到期)。它不改全库开关，也不承诺精确行数。扩展统计仍需持续采样、ANALYZE，并不能消除参数化计划的所有低估；在冻结的历史分支上强制已有索引顺序更直接。
- 调用记录的标量时间上界与游标行上界竞争 B-tree 起点，与范围估计无关；三个列表先取时间窗与游标中较小的元组上界，把它放在标量时间窗前，保留分区裁剪与完整的 `(at, id)` 定位，实测与取舍见 [记录与告警存档的「深页的游标条件」](../../../docs/archive/observability.md#深页的游标条件)。

范围的逐行成本与极低估计由策略 InitPlan 统一处理；账本条件自身的相关性由读取入口的事务内排序设置处理。

基线迁移 `00049_baseline.sql` 直接创建 InitPlan 范围策略，包括 observability 父表和每个初始分区；基线在空库的一个事务内执行，不再重放历史范围改写。

## 迁移执行与校验

- `axiom_owner` 持 goose 会话锁、带平台范围迁移；每个锁最多等约 5 秒。`AXIOM_MIGRATE_CONNECT_TIMEOUT` 默认 10 秒，`AXIOM_MIGRATE_TIMEOUT` 默认 30 分钟，到期按本次 `application_name` 终止会话。
- `platform.migration_checksums` 与版本行同事务记录 SHA-256，执行前核对全部历史，改过、改名、删掉即停止；CI 用 `just migrations <base>` 检查。
- 取得锁后、执行任何待执行迁移前，Axiom 的 schema（各模块与 `river`）里有 `indisvalid = false` 的索引即停止，错误列出每个索引的 `DROP INDEX CONCURRENTLY <名字>;`：中断的 `CREATE INDEX CONCURRENTLY` 会留下同名的无效索引，重跑时 `IF NOT EXISTS` 会跳过它并把迁移记为完成。失败的那次迁移还没有记录，按提示以 `axiom_owner` 删掉后重跑 `axiom migrate`，由它重建。

## 在线迁移

首次生产部署之后，每个迁移都在有数据、有流量的库上执行。`lock_timeout` 只限制等锁，拿到锁之后的扫描和改写不受限，持锁期间挡住业务的读写。规则：

- **短事务扩展**：事务内的迁移只做改目录的变更：建新表，加可空列或常量默认值的列，加 `NOT VALID` 约束；不在同一事务里改已有表的数据或扫全表。
- **分批回填**：回填已有表放在 `-- +goose NO TRANSACTION` 的迁移或任务里，按主键区间分批、每批单独提交，条件只选还没回填的行，中断后重跑从剩下的继续。全部回填后再用新迁移收紧：先加 `CHECK (列 IS NOT NULL) NOT VALID` 并验证，`SET NOT NULL` 就不再扫表。
- **独立验证**：外键和 `CHECK` 先以 `NOT VALID` 加上，`VALIDATE CONSTRAINT` 放在之后的迁移：它只取 `SHARE UPDATE EXCLUSIVE`，不挡读写；同一事务里接着验证，会一直持有加约束时的强锁。
- **并发索引**：已有表的索引用 `CREATE INDEX CONCURRENTLY IF NOT EXISTS`，放在 `NO TRANSACTION` 迁移里，中断后重跑跳过已建好的；中断留下的无效索引由上节的检查拦下。分区表先 `CREATE INDEX … ON ONLY` 父表，再在每个分区并发建索引并 `ATTACH PARTITION`。唯一约束先并发建唯一索引，再 `ADD CONSTRAINT … UNIQUE USING INDEX`。
- 有数据的库升级前，在接近生产规模的数据上演练并计时。

`just migrations <base>`（`scripts/migration_safety.py`）拒绝新增迁移里的：已有表上不带 `CONCURRENTLY` 的 `CREATE INDEX`（`ON ONLY` 除外）、没有 `NO TRANSACTION` 的 `CONCURRENTLY`、已有表上不带 `NOT VALID` 的外键或 `CHECK`、与加约束同一事务的 `VALIDATE CONSTRAINT`、事务内对已有表的 `UPDATE` 或 `INSERT … SELECT`。已有表指同一文件没有创建的表；函数体和 `DO` 块不检查。确属小表的语句在前一行写 `-- migration-safety: <为什么有界>`，由评审核对。检查只看相对基础提交新增的文件：已合并的迁移不能再改，所以基础提交就是起点，发布时不需要移动。在此之前合并、不符合规则的迁移（各模块 `00049` 之后的现有文件）在首次生产部署时面对的是空库，不在有数据的库上执行。

## 验证

复现实验：`flock /tmp/axiom-remote-check.lock python3 internal/platform/pg/testdata/compare-scope.py --output scope-comparison.json`。JSON 保留完整计划，计时只作证据，不作 CI 门槛。

`TestScopeSettingsAreStatementInitPlans` 检查被过滤与命中扫描的每个设置读取均在 InitPlan 内且执行至多一次；`TestScopePlansDoNotCacheAnEarlierScope` 强制两种计划并交替范围；`TestMalformedScopeArraysFailClosed` 检查非法数组报错。各模块既有隔离测试检查读、写与终态权限。账本计划测试检查平台和账户范围、首页与深页的索引顺序及块预算，EXPLAIN 在实际读取入口设置了有序扫描后进行。调用记录的 `TestListPlansFollowTheIndexes` 复用同一份 12 万行数据，覆盖首页、紧随页及深 45,000 行的页，在平台与账户两种范围检查 50、200 行页的有序扫描和块预算；证据见[记录与告警存档](../../../docs/archive/observability.md#深页的游标条件)。

基线等价证明由 `scripts/compare_migrations.py` 在 CI static 执行：两个空库分别应用冻结历史和基线，先独立迁移 platform，再创建任务角色并迁移其余模块。完整对象排序后逐字节比较 `pg_dump --schema-only`，比较全部初始行、序列及角色授权；固定时间与 UUID 输入，导出前恢复内置函数，不忽略权限、策略、约束或数据字段。基线直接建最终定义，避免重放回填与反复改表。

PostgreSQL 官方说明：无关联的单行子查询可以成为每次执行只运行一次的 [InitPlan](https://www.postgresql.org/docs/18/using-explain.html)；[函数易变性](https://www.postgresql.org/docs/18/xfunc-volatility.html)决定计划复用时能否把结果折叠为常量。查证日期：2026-10-28。

真实账本计划证据（超过 200 万笔、2000 个噪声账户，平台 / 账户两种范围）：`Movements` 首页 407、深页 407–408 块，`LegacyMovements` 405 / 406 块，`ReplayThrough` 编号账户 4 / 4 块；历史账户的冻结前缀一次读取为 109–414 块。`StatementPage` 编号账户 612 / 613 块，历史账户 672 / 614 块，100 和 200 行的页都按索引顺序读取。
