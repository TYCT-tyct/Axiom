# accounts

站点、用户、账户、成员、工作区、API key、等级、登录与会话、授权，以及租户范围的构造。要求和业务设计见[账户与租户](../../docs/domains/accounts.md)，这里写代码结构、数据、不变量和接口。

> 本文先于代码写成，是 [Issue #23](https://github.com/TYCT-tyct/Axiom/issues/23) 的设计；各部分随实现的 PR 落地，代码与本文不一致时，在同一个 PR 里改本文。

## 范围

| 本模块管 | 不归本模块，归谁 |
|---|---|
| 站点、域名、key 前缀、注册策略、默认等级、站点的安全与会话参数 | 收款渠道（`payments` 声明为站点配置项） |
| 用户、密码、TOTP、恢复码、会话、邀请关系 | 发邮件（M2） |
| 账户、成员、工作区及其停用 | 余额、在途预留、额度、账本，以及「余额不足」「暂停」（`billing`） |
| 等级本身、账户在哪个等级 | 等级的加价（`billing` 的定价规则） |
| key 的设置：花费上限与重置、工作区预算、过期、预设 ID、可用模型、IP 白名单 | 花费计数与准入时的检查（`billing`，设置作参数传入）；预设内容（`routing`）；限流计数（`gateway`） |
| 权限、角色、授予；构造账户和站点的租户范围 | 资金上限、审批、审计的哈希链与回放（`operations`） |

## 数据

### 表（schema `accounts`）

迁移 `00009_scope_once.sql` 将全部范围策略改为 [pg 的 InitPlan 表达式](../platform/pg/README.md#决策与理由)，包括密码找回请求和令牌，保留终态与到期清理条件。

- 主键 `id uuid`，由 Go 生成 UUIDv7。金额是 `bigint`，单位同[计费的「钱的表示」](../../docs/domains/billing.md#钱的表示)。`status` 取 `active` / `disabled`。
- 类别的含义见[安全的「租户隔离」](../../docs/security.md#租户隔离)。站点数据的表用 `site_id`、账户数据的表用 `(site_id, account_id)` 与 [pg 的 InitPlan 范围参数](../platform/pg/README.md#决策与理由)比较；`sites` 自己用 `id` 当 `site_id`，`accounts` 用 `id` 当 `account_id`。
- `axiom_app` 只对「可改的列」有 `UPDATE` 权限，除密码找回请求和令牌到期清理外，没有 `DELETE` 权限。

| 表 | 主要列 | 键与约束 | 类别 | 可改的列 |
|---|---|---|---|---|
| `sites` | `parent_site_id`、`owner_account_id`、`is_main`（`GENERATED ALWAYS AS (parent_site_id IS NULL) STORED`）、`parent_is_main`（恒为真的生成列）、`name`、`status`、`key_prefix`（`[a-z0-9-]{2,16}` 且以 `-` 结尾）、`registration`（invite / closed；open 等人机验证）、`currency`（站点的记账币种，三个大写字母，默认 `USD` 只补迁移前已有的站点，`axiom site init` 必须写明；创建后不可改，所以 `Money` 的 `currency` 一经发出不会变；`getSite` 也把它交给页面），`default_tier_id`（不为空） | `UNIQUE (id, is_main)`；部分唯一 `(is_main) WHERE is_main`；`(parent_site_id IS NULL) = (owner_account_id IS NULL)`；`(parent_site_id, parent_is_main) → sites(id, is_main)`：上级只能是主站，站点只有两层；`(parent_site_id, owner_account_id) → accounts(site_id, id)`；`(id, default_tier_id) → tiers(site_id, id)`，延迟到提交时检查 | 站点 | `name`、`status`、`key_prefix`、`registration`、`default_tier_id` |
| `site_domains` | `domain`（小写，主键）、`site_id`、`surface`（console / api / admin）、`origin`（完整的源，生产是 `https://域名`，开发可以是 `http://localhost:端口`）、`disabled_at` | `origin` 是 `https://` 加 `domain`，或 `http://localhost[:端口]` | 站点 | `disabled_at` |
| `site_settings` | `site_id`（主键）；可空的 `console_idle_timeout`、`console_max_age`、`admin_idle_timeout`、`admin_max_age`、`new_key_daily_limit`，以及「注册、登录与会话」的尝试上限 `ip_attempt_burst`、`ip_attempts_per_minute`、`email_sign_in_failures`、`user_code_failures`、`challenge_code_attempts` | 每列有上下界：时长最短 5 分钟，最长依次 30 天、90 天、12 小时、1 天；每日上限 0 到 100 万额度；尝试上限至少 1，最多依次 1 万、1 万、1000、1000、100；空表示用代码里的默认值。另有可空的 `max_keys_per_account`、`key_creations_per_hour`（站点的 key 限额，各 1 到 1 万，由 `/key-limits` 读改，不在站点设置的整体替换里） | 站点 | 全部参数列 |
| `tiers` | `site_id`、`name` | `UNIQUE (site_id, id)` | 站点 | `name` |
| `roles` | `name`、`permissions text[]`、`builtin` | `UNIQUE (name)`；迁移写入默认角色 owner、admin、member、finance、viewer、support，权限为空，由在代码里声明权限的 PR 用迁移补上 | 平台 | `permissions`（M1 只经迁移） |
| `accounts` | `site_id`、`kind`（personal / organization）、`name`、`status`、`tier_id`，可空的 `max_keys`、`key_creations_per_hour`（账户自己的 key 限额，各 1 到 1 万，空表示用站点的） | `UNIQUE (site_id, id)`、`UNIQUE (site_id, id, kind)`；`(site_id, tier_id) → tiers(site_id, id)`，它同时保证 `site_id` 是一个站点 | 账户 | `name`、`status`、`tier_id`、`max_keys`、`key_creations_per_hour` |
| `workspaces` | `site_id`、`account_id`、`name`、`status`、`is_default`、`budget`、`budget_reset`、`deleted_at` | `UNIQUE (site_id, account_id, id)`；`(site_id, account_id) → accounts`；部分唯一 `(account_id) WHERE is_default`；默认工作区的 `deleted_at` 为空；`budget_reset` 取 daily / weekly / monthly，空表示不重置的总额，有它就要有 `budget` | 账户 | `name`、`status`、`budget`、`budget_reset`、`deleted_at` |
| `users` | `site_id`、`account_id`（个人账户）、`account_kind`（恒为 `personal` 的生成列）、`email`（规范化）、`password_hash`（argon2id 的 PHC 串）、`factor_version`、`status`、`invite_code`、`inviter_user_id`、`totp_ciphertext`、`totp_nonce`、`totp_wrapped_dek`、`totp_key_version`（`secret.Sealed` 的四个字段，同为空或同不为空）、`totp_last_step`（默认 0）、`mfa_enabled_at` | `UNIQUE (site_id, id)`、`UNIQUE (site_id, account_id, id)`；`UNIQUE (account_id)`、`(site_id, account_id, account_kind) → accounts(site_id, id, kind)`：个人账户只属于这一个用户，不能绑到企业账户上，两个登录身份不会共用一个账户范围；`UNIQUE (site_id, email)`；`UNIQUE (site_id, invite_code)`；`(site_id, inviter_user_id) → users(site_id, id)`；`inviter_user_id <> id` | 账户 | `password_hash`、`factor_version`、`status`、四个 `totp_` 密文列、`totp_last_step`、`mfa_enabled_at` |
| `members` | `site_id`、`account_id`、`account_kind`、`user_id`、`user_account_id`、`role_id`、`personal_owner`（`GENERATED ALWAYS AS (CASE WHEN account_kind = 'personal' THEN account_id END) STORED`） | 主键 `(account_id, user_id)`；`(site_id, account_id, account_kind) → accounts(site_id, id, kind)`；`(site_id, user_account_id, user_id) → users(site_id, account_id, id)`；`(site_id, personal_owner, user_id) → users(site_id, account_id, id)`：个人账户的成员只能是它唯一的用户，所以不需要另加部分唯一索引；`role_id → roles` | 账户 | 无，也没有 `UPDATE` 策略（移除成员随企业账户加 `removed_at`） |
| `platform_grants` | `site_id`、`site_is_main`（恒为真）、`account_id`（被授予者的个人账户）、`user_id`、`role_id`、`granted_by`（授予者的用户 ID，命令行授予时为空）、`revoked_at` | `(site_id, site_is_main) → sites(id, is_main)`：只能授予主站用户；`(site_id, account_id, user_id) → users(site_id, account_id, id)`；部分唯一 `(user_id, role_id) WHERE revoked_at IS NULL`：一个用户同一角色只有一条有效授予 | 账户（被授予者的），写入另限平台范围 | `revoked_at`（终态；恢复插入新授予） |
| `api_keys` | `site_id`、`account_id`、`workspace_id`、`kind`（call / management，M1 只开放 call）、`name`、`prefix`、`last4`、`secret_hash`、`secret_version`、`created_by`、`created_at`、`expires_at`、`status`、`spend_limit`、`limit_reset`、`preset_id`、`models`、`ip_allow`、`deleted_at` | `(site_id, account_id, workspace_id) → workspaces(site_id, account_id, id)`；`(site_id, created_by) → users(site_id, id)`；`UNIQUE (secret_hash)`；管理 key 必须有 `expires_at`；`limit_reset` 同工作区的 `budget_reset` | 账户 | `name`、`status`、`spend_limit`、`limit_reset`、`preset_id`、`models`、`ip_allow`、`deleted_at`（终态） |
| `sessions` | `site_id`、`account_id`（个人账户）、`user_id`、`surface`（console / admin）、`token_hash`、`csrf_token`、`level`（pending_mfa / password / mfa）、`factor_version`、`mfa_attempts`、`created_at`、`last_seen_at`、`expires_at`、`idle_timeout`、`revoked_at`、`ip`、`user_agent` | `UNIQUE (token_hash)`；`(site_id, account_id, user_id) → users(site_id, account_id, id)` | 账户 | `last_seen_at`、`mfa_attempts`、`expires_at` 和 `idle_timeout`（只往小改，见「站点缩短会话时长」）、`revoked_at`（终态） |
| `recovery_codes` | `site_id`、`account_id`、`user_id`、`factor_version`、`code_hash`、`used_at` | 同上外键；`UNIQUE (user_id, code_hash)`，也是消费时的查找 | 账户 | `used_at`（终态） |
| `security_failures` | `site_id`、`started_at`、`failures` | 每站点一行，一分钟窗口，原子增加/重置；非零窗口按 started_at 的部分索引供到期批次认领；只含计数 | 站点 | 仅平台可写计数与起点 |
| `password_reset_requests` | `site_id`、`email_hash`、`id`、可空 `email`、`domain`、`language`、`expires_at` | 主键 `(site_id, email_hash)`；唯一 `id`；摘要 32 字节；语言 zh/en | 站点 | `id`、`email`、`domain`、`language`、`expires_at`（重发替换，处理后清除邮箱） |
| `password_reset_tokens` | `id`、`site_id`、`account_id`、`user_id`、`token_hash`、`factor_version`、`expires_at`、`used_at` | `token_hash` 唯一、32 字节；`(site_id, account_id, user_id) → users` | 账户 | `used_at`（终态） |

- **租户列直接存在行上**：账户数据的每张表都有 `site_id`、`account_id`，都不为空。被引用的父表有 `UNIQUE (site_id, id)` 一类的复合唯一键，子表用复合外键引用它，所以两列不可能与"key → 工作区 → 账户 → 站点"不一致；两列不在可改的列里，写入后不变。外键检查不受行级安全影响。理由见[账户与租户的「决策与理由」](../../docs/domains/accounts.md#决策与理由)。
- **授予每种目标一张表**：账户角色在 `members.role_id`，平台角色在 `platform_grants`；站点员工、工作区角色以后各加一张。每张表的外键都直接，不需要一张多态表靠条件约束拼归属。
- `members` 在 M1 只有个人账户的主人一行，按所属账户归类即可。企业账户的成员要读到自己在别的账户里的成员关系，怎么分类随企业账户确定（待定）。
- 新账户的 `tier_id` 取 `invite_tenant` 在库里读出的 `default_tier_id`（M2 的开放注册同样在库里读出），之后与默认等级无关。不取快照：快照可能晚 30 秒，而写进账户的等级以后不会再改。
- 名字（站点、等级、角色、账户、工作区）1 到 100 个字符。
- 新 key 的 `spend_limit` 取快照里站点的每日上限（`site_settings` 为空则用代码默认值 10 额度，开发者先定、负责人可改），`limit_reset = daily`；用户可以改成别的值或不设。

### 行级安全的写法

所有表 `ENABLE` 并 `FORCE ROW LEVEL SECURITY`。每条命令一条策略，不用 `FOR ALL`：多条宽松策略按 OR 合并，一条 `FOR ALL` 就能绕过终态的限制。

- `FOR SELECT USING (范围)`；`FOR INSERT WITH CHECK (范围)`。
- 有终态列（`revoked_at`、`deleted_at`、`used_at`）的表：`FOR UPDATE USING (范围 AND 终态列 IS NULL) WITH CHECK (范围)`。旧行必须还没进终态，新行可以进入；进入后整行不能再改，包括清除终态列，平台范围也一样。
- 其他表：`FOR UPDATE USING (范围) WITH CHECK (范围)`。
- `platform_grants` 的 `INSERT`、`UPDATE` 用 `(SELECT platform.in_platform_scope())`：平台范围包含所有账户，所以它比类别更严，不更宽。
- 测试：首次撤销或删除成功，之后清除终态列、改其他列都影响 0 行；迁移测试查 `pg_policies`，有终态列的表上没有 `FOR ALL`，也没有第二条 `UPDATE` 策略。

## ID

- 库里是 `uuid`；Go 里每种实体一个以 `uuid.UUID` 为底层的命名类型（`SiteID`、`TierID`、`AccountID`、`WorkspaceID`、`UserID`、`KeyID`、`SessionID`），不能互相赋值，pgx 直接按 `uuid` 读写。对外写成 `id.TypeID()`，读回用 `ParseSiteID` 等，别的种类的 ID 报 `typeid.ErrInvalid`。
- 对外用 TypeID 规范 v0.3.0（github.com/jetify-com/typeid，2026-10 查）：`前缀_` 加 26 个小写的 Crockford base32 字符。前缀 `site`、`tier`、`acct`、`ws`、`user`、`key`、`sess`。编解码在 `platform/typeid`，前缀由各实体的所属模块定义。
- 只在 HTTP 边界转换：ID 类型不实现 `TextMarshaler`，pgx 和 JSON 都不会意外用到另一种写法。格式不对与不存在返回同样的 404。

## 授权与租户范围

本模块是生产代码里唯一构造账户和站点范围的地方：`pg` 加按账户、按站点构造范围的函数，forbidigo 只允许 `internal/accounts` 调用（`pg.NewScope` 仍只给测试）。平台范围照[平台的规则](../platform/README.md#范围)由 `cmd/axiom` 构造，交给 `accounts.New`，本模块只在授权通过后交出；后台任务的数据库角色照平台的 `pg.Tx.SetRole` 做法，不经本模块。

| 操作者 | 本次事务的范围 |
|---|---|
| 登录前（注册、登录、认会话、认 key） | 无范围，只能调用下文「登录前的查找」的函数；注册另见下文 |
| 控制台用户 | 用个人账户的范围读出自己的成员关系，再按请求的目标账户给该账户的范围，前提是他在该账户的角色含这项权限 |
| 平台管理员（管理后台） | 平台范围；要求会话的监听面是管理后台、`level = mfa`，且未撤销的平台授予含这项权限 |
| 调用 key | `Admit` 打开的事务里是 key 所属账户的范围 |
| 快照加载 | 平台范围的 `ReadTx` |

- `Caller`（认出的 key）、`Principal`（认出的用户）、`Admission`（准入结果）的字段都不导出，只有认证成功的函数能造出非零值；其他模块只读访问器。`Caller` 不带范围：带 key 的事务只能经 `Admit` 打开。零值的 `Caller` 交给 `Admit` 被拒，有测试。
- `Authorize(principal, permission, target)` 只返回覆盖本次目标的范围。账户目标的范围是 `pg.AccountScopeIn(会话签发的站点, 账户)`：除了账户自己的行，还读这个站点的公开配置和共享计数（策略把站点列与 `platform.home_site()` 的 InitPlan 参数比较，如充值页的付款方式、每 IP 的下单计数），别的站点的读不到；站点来自会话，不来自账户能写的行。目标由服务端从 URL 里的 ID 解析，行级安全再挡一层：别的账户的工作区在这个账户的范围里不存在，返回 404。
- 权限在代码里声明名字和是否敏感；角色快照里有代码不认识的权限就不启动。敏感权限要求本次会话 `level = mfa`。
- M1 声明的权限：`api_keys.view`、`api_keys.manage`、`workspaces.manage`、`calls.view`、`balance.view`、`balance.top_up`，以及敏感的 `sites.manage`、`platform_grants.manage`、`users.manage`，和管理接口的八个（见下文「管理接口」）。默认角色由迁移 00003 写入，00005、00006、00010 改成现在的集合，列在「管理接口」的表里；以后加权限的 PR 用迁移改这些集合。`PermissionNamed` 按名字取声明过的权限，契约里的 `x-axiom-permission` 靠它在启动时核对。
- `Authorize(ctx, principal, permission, target)`：账户目标读操作者在该账户的成员关系；企业账户的成员关系怎么归类待定，所以 M1 只有自己的个人账户能被授权，别的账户返回 `ErrNotFound`。平台目标要求会话在管理后台、`level = mfa`，再读未撤销的平台授予；有成员关系或授予但角色不含这项权限返回 `ErrForbidden`。

## 登录前的查找

登录前还不知道租户，事务没有范围，什么表都读不到。几个 `SECURITY DEFINER` 函数把秘密或名字换成标识：

| 函数 | 输入 | 输出 |
|---|---|---|
| `accounts.key_tenant(site, hashes bytea[])` | 请求站点、key 在各哈希版本下的哈希（最多 2 个） | `key_id`、`account_id`、`workspace_id` |
| `accounts.session_tenant(domain, surface, token_hash)` | 请求的域名、监听面、令牌哈希 | `session_id`、`user_id`、`account_id` |
| `accounts.login_tenant(domain, email)` | 请求的域名、规范化的邮箱 | `user_id`、`account_id` |
| `accounts.invite_tenant(domain, code)` | 请求的域名、邀请码 | 邀请人的 `user_id`、站点当前的 `default_tier_id`；注册策略不是 invite 时无结果 |
| `accounts.domain_site(domain, surface)` | 请求的域名、监听面 | `site_id`；供 `Admit` 在账户范围里核对域名 |

- 上述五个函数都已实现（迁移 00002、00003）。`login_tenant` 只认控制台和管理后台的域名，`invite_tenant` 只认控制台域名。
- 域名或站点停用、域名不属于这个监听面时都无结果；`key_tenant` 只认请求的站点和未删除的 key；都在库里判定，不看快照。都以请求的域名为条件，所以别站的 key、别站的会话令牌和不存在的走同一条路径。配置和密码哈希在拿到账户后用账户范围再读。
- **函数的所有者是专用角色 `axiom_lookup`**，不用平台范围：它不能登录，不拥有任何表，`axiom_app` 不是它的成员（不能 `SetRole` 过去）。这几张表上只为它加 `FOR SELECT TO axiom_lookup USING (true)` 的策略，只授予函数要读的列。函数固定 `search_path = pg_catalog, pg_temp`，名字全部带 schema，只有 `axiom_app` 能执行。
- 不在函数里设范围：`just scope` 禁止平台的 `pg` 之外出现范围参数，迁移也一样；按角色加策略不需要范围，也没有"函数返回后范围留在调用方事务里"的风险。
- `serve` 启动时检查：Axiom 自己的 schema（`platform` 和各模块的）里每个 `SECURITY DEFINER` 函数的所有者都是 `axiom_lookup`，`search_path` 是上面的固定值，执行权限只有 `axiom_app`；`axiom_app` 不能成为 `axiom_lookup`；`axiom_lookup` 不能登录、不拥有表。任一不满足就不启动。检查在平台的 `DB.Check` 里，见[平台的「不变量」](../platform/README.md#不变量)。
- `axiom_lookup` 由迁移的第一步建立（见[平台的「角色」](../platform/README.md#角色)）。迁移以 `axiom_owner` 建函数，`REVOKE EXECUTE … FROM PUBLIC`、`GRANT EXECUTE … TO axiom_app`，再 `ALTER FUNCTION … OWNER TO axiom_lookup`：改所有者要求新所有者对 schema 有 CREATE，所以只在这一句前后授予、随即收回。平台的测试用非超级用户的迁移管理员照这个写法执行通过。

## API key

### 格式与哈希

- `sk-ax-v1-`（主站；二级站点自配，`[a-z0-9-]{2,16}` 且以 `-` 结尾）+ 43 个 base62 字符（32 字节随机）+ 6 个 base62 校验字符（前缀加随机串的 CRC32）。前缀复制到 `api_keys.prefix`，站点改前缀不影响已发出的 key。格式或校验位不对，直接拒绝，不算哈希、不查库。
- 哈希是 `HMAC-SHA256(版本 v 的密钥, 整个 key)`。密钥放在 `AXIOM_KEY_HASH_FILE` 指定的受保护文件里，格式和权限要求同[主密钥文件](../platform/README.md#凭证加密)（`secret.LoadMAC`）；同时最多 2 个版本，数值大的是当前版本，不合法就不启动。旧版本可以退役的条件：未删除的 key 里没有用它的（后台显示计数）。

### 认出与准入

**缓存只存不会变的东西；能被改的（停用、删除、过期、IP 白名单、可用模型、预设、上限、等级）每次调用都在准入事务里读库，判定能否使用、定规则和路由都只用读出的值。** 所以缓存陈旧、并发的缓存填充交出旧结果、提交确认丢失、多个核心节点，都不会让停用后的 key 通过，也不会按旧预设路由，不需要失效协议。

1. **认出**（`AuthenticateKey`，内存）：解析并核对校验位，用 `(site_id, 当前版本的哈希)` 查缓存。条目里只有 `key_id`、`account_id`、`workspace_id`、`site_id`，建后永不改变。未命中时按 `(site, 哈希)` 合并并发的查找：配了 `Config.LookupSlot` 时，这次共享的查找先取一个名额、查完才还（发起它的请求走了也不提前还，等它的请求不另取），名额取不到就按查找失败处理、不写负缓存；无范围调用 `key_tenant`，无结果写负缓存、返回 401；有结果写入缓存。
2. **准入**（`Admit`，数据库）：gateway 调用 `accounts.Admit(ctx, caller, req, fn)`，`req` 带请求的域名、可信解析的客户端 IP。本模块用 key 所属账户的范围打开事务，执行一条查询：key ⨝ 工作区 ⨝ 账户，条件为 `domain_site(请求的域名, 'api') = key.site_id`，key、工作区、账户都启用，key 与工作区未删除，未过期，`kind = call`，`ip_allow` 为空或包含客户端 IP。其他条件有效而账户停用时回滚并返回 `ErrAccountSuspended`（403）；其余条件不满足返回与"key 不存在"相同的 `ErrUnknownKey`（401）。满足就把当前的 `preset_id`、`models`、`tier_id`、`spend_limit`、`limit_reset`、工作区预算放进 `Admission` 交给 `fn`。`fn` 由 gateway 组装：用 `Admission` 定规则、路由（认模型里名字解析在准入之前做，key 的可用模型这一半在 `fn` 里按 `Admission.models` 约束，不允许返回 403），再调 billing 的准入在同一个事务里预留。`fn` 里只有对内存快照的有界计算和预留的数据库语句，不访问缓存、不等待：限流计数和会话粘性在 `Admit` 之前取好，端点负载在进程内，都作为路由的输入（见[路由的「容量感知」](../../docs/domains/routing.md#容量感知)）；候选都满载时 `fn` 返回"需要等待"，`Admit` 回滚（这时还没有预留），gateway 在事务之外等，醒来后只重新调 `Admit`，不再访问缓存、不重复计限流；权限、预设、可用模型、等级都重新读库，路线也重新计算。
3. 不预留的 key 接口（`GET /api/v1/key`、`/credits`）同样经 `Admit` 打开事务，`fn` 只读。

- **为什么停用立即生效**：停用的提交被确认之后开始的调用，其准入查询都在提交之后执行，读到的就是停用（Read Committed 每条语句取新快照），与缓存内容、节点数、提交确认是否送达都无关。改预设、可用模型、等级同理：下一次调用就按新值路由和计价，最高价也按新等级的售价比较。
- **路由在准入事务里，不在它之前**：路由要的预设、可用模型、等级都会被改，在准入之前路由就只能用缓存里的旧值，收紧的预设会被绕过，放宽的会被旧值一直拒在准入之前。事务里的路由只读内存快照和事务外取好的缓存结果，不加锁、不查库、不访问缓存，所以不增加[总体架构](../../docs/architecture.md#模块边界规则)调用路径上的查库例外，只让事务多开着路由计算那一段时间。
- **满载排队不占数据库连接**：等待放在事务里，一个满载模型的排队请求每个都占着一条连接，能把连接池占满，别的模型的准入和结算都拿不到连接；在事务外等，排队的请求不占连接，醒来重新准入还顺带读到等待期间的停用和改动。重新准入的次数受路由的排队时间上限约束，到期返回 429。
- **停用的 key 在准入之前能看到什么**：解析和名字解析在准入之前，所以停用的 key 仍可能收到请求格式类的错误（400、模型不存在），但不会调用上游、读不到租户数据、不产生费用。准入的拒绝不缓存：拒绝可能与请求有关（IP），缓存它会误拒同一把 key 的合法请求。
- **防枚举只承诺到这里**：不存在、属于别的站点、停用、过期、删除、IP 不允许，状态码和正文完全相同。剩余的时间差：本站真实存在的 key 多一次准入查询；缓存命中与未命中不同。只有持有这把 key 的人能利用。
- **key 的设置**（`KeySettings`）：名称、停用、花费上限（十亿分之一额度，空表示不限）、重置周期（空表示总额不重置，要有上限）、可用模型（空表示任意，空列表按空处理）、IP 白名单（`cidr`，主机位清零）。创建时不写上限就取站点的新 key 每日上限、按日重置；`UpdateKey` 整体替换，没写的就是不限制，所以页面把读到的设置连同修改送回；到期时间只能在创建时给。创建、整体替换、删除各记一条审计（`api_key.create`、`api_key.update`、`api_key.delete`），改成停用或重新启用也是 `update`。`Key.CreatedAt` 是库里盖的 `created_at`（建 key 的 `InsertKey` 里取 `statement_timestamp()`），小时频率限额用同一个时间。输入的检查（币种、不为负、重置需要上限、地址能解析、到期在将来）在 `cmd/axiom` 的处理器里，进模块的设置已满足表的约束。
- **key 的数量与创建频率**：每个账户最多保有多少把未删除的调用 key，以及每小时最多创建多少把（已删除的也算），按三层取值：账户自己的（`accounts.max_keys`、`accounts.key_creations_per_hour`）盖过站点的（`site_settings.max_keys_per_account`、`key_creations_per_hour`），站点没设的用代码里的默认（100、50）；每个限额单独往上找，空表示没设。`CreateKey` 在插入前检查（`admitKey`），都在同一个事务里：先取账户行的 `FOR NO KEY UPDATE`（`LockAccount`，同时读出账户自己的限额；与引用账户行的外键互不阻塞），再数。两个请求同时建最后一把时后一个等前一个提交后数到它，所以不会都插进去。数量是 `CountKeys`（未删除的调用 key）。频率是 `RecentKeys`：这个账户一小时内建的调用 key 的真实数量，和要等多久才有空位。一小时按**库的时钟**判：`api_keys.created_at` 由 `InsertKey` 里的 `statement_timestamp()` 盖，窗口起点也在同一个语句里取 `statement_timestamp()`，两者都在取得账户锁之后，所以各节点自己的时钟差不影响（测试 `TestKeyCreationsIgnoreTheNodesClocks`）。因此不用 ID 里的时间：UUIDv7 是建 key 的节点按自己的时钟生成的。要等的时间是「使计数降到限额以下的那把 key」满一小时的时间，即按时间从旧到新数的第「数量 − 限额 + 1」把；限额调低后数量仍是真实的窗口数，不被截断。索引 `api_keys_made_by_account (account_id, created_at) WHERE kind = 'call'` 含已删除的 key，所以没建过 key 的账户只读到自己的几行，不随全站一小时内的建 key 数增长（`TestKeyCreationsUseTheIndex` 看执行计划）。两种拒绝都是 `*TooManyKeys`（`errors.Is(err, ErrTooManyKeys)` 成立，带生效的限额和数到的数量）；超频率的还带 `RetryAfter`。控制台接口据此写 409（`too_many_keys`）和 429（`rate_limited`，带 `Retry-After`），正文写明限额和数量。站点的限额取快照里的值，别的节点最久 30 秒后生效；账户的限额在建 key 的事务里读，立即生效。调低限额不会删掉已有的 key，超过的账户只是不能再建。
- **key 限额的读改**（`SiteKeyLimits`/`SetSiteKeyLimits`，`AccountKeyLimits`/`SetAccountKeyLimits`）：站点的和账户的都经管理接口的 `/sites/{site_id}/key-limits`、`/accounts/{account_id}/key-limits` 整体替换（没写的就是没设），要 `sites.manage`、理由和幂等键，在持有对应行锁的事务里取旧值、写、记审计（`site_key_limits.update`、`account_key_limits.update`，`detail` 有 `before`、`after`）。站点的限额不放在站点设置里：站点设置的 `PUT` 整体替换，多出的字段会被没见过它们的旧页面当作「没设」抹掉；放在自己的接口里，`WriteSiteSettings` 不写这两列，旧请求碰不到它们（`TestKeyLimits` 用旧页面的请求格式验证）。
- 创建 key 不做幂等键：提交后响应丢失，用户在列表里看到一把从未用过的 key，删掉即可，不为重试保存明文。
- 创建、停用、删除 key 在调用方的事务里经 `audit.Record` 记审计（`api_key.create`、`api_key.update`、`api_key.delete`），与改动一起成败。条目带 key 所属的站点和账户；注册在创建账户的同一事务里经 `audit.RegisterAccount` 登记租户，所以账户范围里就能记（平台的「审计日志」）。登记表出现之前已有的站点和账户，由迁移 `00003_console.sql` 一次补登（平台的迁移先于本模块运行，那时登记表已建好且为空）；测试 `TestUpgradeRegistersExistingTenants` 从旧版本的库升级后对旧账户做这三种操作。
- **`api_key.update` 记改了什么**：同一个事务里先用 `LockKey` 取这把 key 的旧值（`FOR UPDATE`，所以同时的另一处修改排在这一处之后，条目记的是它真正替换掉的值），再写，再记。`detail` 里 `changed` 是改了的设置名，`before`、`after` 是其中允许披露的那些改前改后的值：名称、是否停用、花费上限、重置周期；没改的项两边都没有。可用模型和 IP 白名单只出现在 `changed` 里：模型最多 256 个、每个 200 字，会超过条目 8 KiB 的上限（`00003_audit_log.sql`）；地址是日志不记的个人信息（平台的「审计日志」）。key 的文本和哈希从不进来，它们不是设置。
- 以后的二级站点：`Admit` 的范围并上站长账户，同一事务里一起预留（见[计费的「预留与结算」](../../docs/domains/billing.md#预留与结算)「二级站点的请求」）。

| 缓存 | 内容 | 容量与过期 |
|---|---|---|
| 正缓存 | 不变的标识 | 默认 20 万条，LRU |
| 负缓存 | 不存在的 `(site, 哈希)` | 默认 20 万条，60 秒；256 位随机串不会先被猜中再被创建 |

**代价**：命中是一次 HMAC 加一次查表，基准测试进分配门槛。准入在预留的事务里多一条查询，计入[性能](../../docs/performance.md#延迟预算)准入那一格的 5ms；路由移进事务后，事务多开着路由计算的那段时间（"认站点和 key、读取请求、定规则、路由"那一格的预算），不持锁、不等待；限流计数和会话粘性在事务之前取好（M2 起是一次缓存往返），计入"限流和会话粘性"那一格，超时见[路由的「容量感知」](../../docs/domains/routing.md#容量感知)。`BenchmarkAdmit` 在同一个库、同一个账户范围里对照空事务和 `Admit`：实现它的 PR 在开发机（Windows 上的 Docker Desktop，PostgreSQL 18，机器同时在跑别的测试）测得增量约 0.5–0.9ms（各 6 轮×5000 次，空事务中位 2.4ms、`Admit` 中位 3.2ms；最快一轮 1.2ms 对 1.7ms），即准入查询多出的一次往返；全程 p95 在 gateway 和 billing 接上后、在与核心同机房的库上验收。命中路径（`BenchmarkKnownKey`）约 4µs、10 次分配。冷的未命中另有一个事务，只在未命中时发生。

### 快照

- 站点、域名、站点设置、角色各是一份不可变的值，用 `atomic.Pointer` 整体替换。只有一个后台循环重载：每 30 秒无条件重载一次（本进程写这些表的接口加入时，再加写后标记"需要重载"），覆盖别的节点和命令行的修改；循环串行，旧值不会覆盖新值。平台的变更通知（[总体架构的「技术选型」](../../docs/architecture.md#技术选型)）做好后也触发重载，30 秒的兜底仍保留：快照不决定能否访问，滞后只影响价格页和参数。
- **快照不决定能否访问**：域名和站点的启用、注册策略在库里判定，平台授予每个请求读库。快照只用来预筛没登记的域名（快照里没有就直接拒，省一次查库）、匿名价格页和计价预览按站点默认等级（`Site.DefaultTier`）选规则、尝试上限等参数、角色的权限集合。
- 站点快照还带着本站 API 域名的 origin（`Site.APIBase`，没有启用的 API 域名时为空；有多个时优先 HTTPS，同协议取 origin 最小的那个，快照每 30 秒重载，地址不能因此变来变去）：控制台用它告诉用户把客户端指向哪里（[API key](../../docs/domains/accounts.md#api-key)「接入地址」）。它只是给界面看的地址，和准入、计价无关。
- 超过 90 秒（3 个周期）没有成功重载，所有请求返回 503，直到重载成功；启动时第一次加载失败就不启动。

## 注册、登录与会话

安全事件写 `platform.audit_log`：`security.registration`、`security.sign_in`、`security.mfa` 与成功状态同事务；失败的 `security.registration_failed`、`security.sign_in_failed`、`security.authentication_failed`、`security.mfa_failed`、`security.factor_failed`、`security.password_failed` 在独立、有 5 秒期限的事务提交。只含固定分类、监听面、用户/会话/租户 ID 和上下文请求 ID，未知用户记站点和系统操作者；缺 Cookie 的普通未登录访问不记失败。邮箱、IP、User-Agent、密码、邀请码、TOTP/恢复码及令牌不进条目。审计写失败使操作失败，不伪报成功。

失败事务原子累加每站点 `security_failures` 的一分钟窗口并持久化 `accounts.authentication_failures` 信号；计数行锁保证多节点不会丢计数。`SampleSecurityAlerts` 在平台后台每分钟用 `FOR UPDATE SKIP LOCKED` 最多认领 100 个已过期且非零窗口，清零并发一次恢复；与失败更新同样先锁计数、再持久化信号，防止旧恢复覆盖新失败。平台告警配置中添加 kind 为 `accounts.authentication_failures` 的规则（例如 threshold=20、duration_seconds=0，表示每站点一分钟 20 次失败），配置通知渠道；未配置时只保留审计和信号，不发通知。阈值、持续时间、静默与重复由平台告警规则配置，只有匹配阈值的安全信号才创建告警事件；不自动封号。审计条目的保留沿用平台策略，计数每站点一行，无个人信息，不随事件数增长。

| 能力 | M1 | M2 |
|---|---|---|
| 注册 | 邀请码；邮箱不验证 | 开放注册（要人机验证）、邮箱验证 |
| 登录 | 邮箱 + 密码，开了 TOTP 的再过 TOTP 或恢复码 | 第三方登录、通行密钥、邮箱验证码 |
| 找回密码 | `axiom user set-password`（从标准输入读） | 控制台邮件链接（已实现） |
| 第一个用户 | `axiom site init`、`axiom user create`、`axiom user grant-platform`（见下文「命令行」） | — |
| 认证因素 | 开启 TOTP、重建恢复码 | 控制台改密码（已实现）；关闭或更换 TOTP 待实现，仍可经命令行 `reset-mfa` |
| 会话管理 | 退出 | 设备列表、踢掉一台、退出全部 |

- **注册**：`invite_tenant` 在库里核对站点启用、策略为 invite、邀请码有效，得到邀请人和默认等级。服务端生成新账户、工作区、用户的 ID，在只覆盖这个新账户的范围里，一个事务插入账户、默认工作区、用户、成员（所有者）。邮箱重复由唯一索引拒绝，整个事务回滚；重试得到"已注册"。邮箱规范化（去空白、转小写）后须是 3 到 254 个可打印 ASCII 字符、`@` 两侧各有字符，且必须通过 `net/mail.ParseAddress`、解析地址等于输入，否则 `ErrBadEmail`：表的约束（`lower()`、`LIKE`）在各地区设置下对非 ASCII 的判断不一定与 Go 一致，放进库再被拒会变成 500；国际化邮箱地址 M1 不支持。不用站点或平台范围包住注册。
- **认证因素的一条规则**：`users.factor_version` 是密码、TOTP、恢复码这一组的版本。签发会话、消费一次性因素、修改因素的事务，先锁用户行 `SELECT … FROM users WHERE id = $u FOR UPDATE`（邮件重置先锁请求行，再锁用户；所有涉及请求的路径同序），在锁内核对用户启用和版本；修改因素的事务在同一把锁内把版本加一。会话有效 = 未撤销、未过期、未超空闲、用户启用、版本等于用户当前版本，每个请求读会话时同一条查询一起核对；恢复码只在版本相等且未用过时可用。
- **密码登录**：argon2id，参数写进 PHC 串（M1 取 OWASP 的基线：19 MiB、2 遍、1 线程；以后提高参数，旧哈希照样能验）；密码 12 到 256 个字符；邮箱不存在也算一次假哈希。先在事务外读出哈希和版本并计算，再开签发事务，锁内版本没变且用户启用才签发。
- **两步验证**：开了 TOTP 的用户密码通过后先拿到 `pending_mfa` 会话（5 分钟，只能调两步验证接口）。验证事务：锁用户行 → 核对待验证会话未撤销、未过期、版本相等、尝试次数未满 → 验码（TOTP 做 `totp_last_step < $step` 的条件更新，恢复码做条件消费，影响行数都必须为 1）→ 码不对只把尝试次数加一并提交（每个待验证会话的次数是站点的 `challenge_code_attempts`） → 码对就条件撤销待验证会话，签发新令牌、新 CSRF 令牌的 `mfa` 会话。用户行锁让一个挑战只换出一个会话。
- **修改认证因素**：开启 TOTP 要重新输入密码并用新密钥的一个码确认，只对还没开启的用户：已开启的在用户行锁内拒绝（`ErrTOTPOn`），版本、密钥、恢复码、会话都不变，更换和关闭是 M2 的事；重建恢复码要一个当前的 TOTP 码，错码计入每用户的失败次数。两者都加版本，为当前设备签发新的 `mfa` 会话，并在同一事务里记审计 `user.totp_enable`、`user.recovery_codes_regenerate`（操作者和对象都是这个用户，带站点和个人账户，不记密钥和恢复码）：审计写不进，因素、恢复码和新会话一起回滚。TOTP 密钥用 `secret.Keyring.Seal` 封装，`Location` 是 `{accounts.users, 用户 ID 的标准 UUID 写法, totp}`，`Sealed` 的四个字段存进 `users` 的四个 `totp_` 列；`BeginTOTP` 只生成密钥和 `otpauth` 链接，不存库，确认时客户端把密钥带回。TOTP 按 RFC 6238（HMAC-SHA1、30 秒、6 位），接受前后各一个步长，同一步长只用一次。恢复码 10 个、各 80 位，写成 `xxxx-xxxx-xxxx-xxxx`，不分大小写、忽略连字符，只存 SHA-256。
- **Cookie 与 CSRF**：`__Host-axiom_session`（HttpOnly、Secure、SameSite=Lax、Path=/），库里只存令牌的 SHA-256。所有会改数据的请求，包括登录、注册、两步验证：`Origin` 必须完全等于该站点该监听面登记的 `origin`，缺失或 `null` 拒绝；`Content-Type: application/json`，接口不开放 CORS；有会话的再核对 `X-CSRF-Token`。登记的源是 `http://localhost` 时 Cookie 才不带 `__Host-` 和 `Secure`，由登记决定、不看请求头。认证相关的响应 `Cache-Control: no-store`。
- **参数**：默认值只在代码里。按站点的（会话时长：控制台空闲 7 天、最长 30 天，管理后台 30 分钟、12 小时；下表的尝试上限；新 key 的每日上限）存在 `site_settings`，经 `GET/PUT /api/admin/v1/sites/{site_id}/settings` 读改，要敏感权限并记审计；按节点的资源上限用 `AXIOM_*` 环境变量。

### 邮件找回和改密码

业务流程和取舍见[登录与会话](../../docs/domains/accounts.md#登录与会话)。`RegisterPasswordRecovery` 在队列启动前注册 `accounts.password_reset`（notifications 队列，最多 5 次）；`Config.Recovery` 把同一对象交给 HTTP 服务。SMTP 未配置时不注册签发 worker，避免取走其他节点能处理的任务；SMTP 未配置或没有接入时服务返回 `ErrBusy`，HTTP 映射 503。队列只携带请求 UUID，无个人信息或凭证。工作期限 15 秒；错误日志 `accounts.password_reset.failed` 只记 job_id、attempt、类别和 SQLSTATE，原始错误及 detail 不进日志。交给 River 的错误仍为固定文字。没有 HTTPS 域名或历史邮箱不合法时提交清空邮箱并取消任务，临时故障由 River 重试、最终失败由现有告警暴露。

迁移 00008：`password_reset_requests` 属站点范围，主键 `(site_id, email_hash)`，摘要为规范化邮箱的 SHA-256；`id` 是每次请求的新 UUIDv7，`email` 在处理后清空，`domain` 是请求时登记的控制台域名，`language` 为 zh/en，`expires_at` 为请求提交起 30 分钟。每个邮箱无条件 upsert 再入队，调用方路径不读用户。请求记录复用，所以没有终态策略；它的新 ID 同时阻止旧链接和旧任务生效。原始待处理邮箱只在 RLS 请求表，邮件正文只在 RLS outbox；`accounts.password_reset_cleanup` 在 archive 队列每分钟清理到期请求、摘要和令牌，每次每表最多 1000 行，15 秒超时。RLS 的 DELETE 策略仅允许删除范围内已过期行，保护有效凭证，过期无需保留来防重放；SMTP 关闭时仍注册清理任务。清理失败重试并留下分类日志。

`password_reset_tokens` 属个人账户范围，复合外键 `(site_id, account_id, user_id)` 指用户；`id` 对应签发请求编号，唯一 `token_hash` 是 `randomToken` 生成的 256 位随机串的 SHA-256；只可更新 `used_at`，已消费的行策略不再允许更新。后台通过 `db.TenantTx` 和 `password_reset_request_tenant(id)` 定位请求的站点范围；登录前函数 `password_reset_tenant(domain, token_hash)` 由专用 `axiom_lookup` 持有，固定 search_path、撤销 PUBLIC 的执行权，只交出租户与令牌 ID，不判最终可用性。所有表强制 RLS，只有这两张新表授予受过期策略约束的 DELETE。

后台与完成重置都按「请求行 → 用户行 → 令牌」加锁；重发只锁请求行，其他因素变化只锁用户行，避免锁顺序反转。后台先锁最新请求，检查未过期且尚未处理，查询本站当前启用的 HTTPS 控制台域名，优先本次请求登记的域名，停用后才回退，再锁用户，按当前因素版本签发令牌；令牌、outbox、请求邮箱清除同事务。未知或停用用户只清除请求邮箱。事务提交后任务重试看到邮箱为空，直接结束。重置先在无范围事务里按秘密定位，再在站点范围内重锁请求与用户；条件消费要同时匹配令牌哈希、用户、因素版本、未用及库的实际时钟未到期，影响行数必须为 1。使用 `clock_timestamp()` 而不是事务开始时间，避免锁等待跨过期限后仍可消费。

`RequestPasswordReset` 接收邮箱、语言、客户端 IP；`ResetPassword` 接收本站 host、令牌与新密码；`ChangePassword` 接收 Principal 和当前/新密码。新密码均复用 `passwordAllowed`、`hashPassword` 和并发哈希槽；慢哈希在锁外。改密码需要非 pending 控制台会话，TOTP 开启时须已验证 MFA；当前密码错误计用户失败桶。两种密码写入都调用已有 `SetPassword` 提升因素版本，写同一种 `user.password_set` 审计，并经 outbox 发送 `passwordMail` 通知，任一步失败整体回滚。重置不签发会话；改密码签发原等级的新会话，新 CSRF 和 MFA 用户补发的恢复码在响应中均为可选字段；`insertRecoveryCodes` 与因素变更共用。TOTP 保持，旧会话、恢复码和重置令牌因版本变化失效；邮件重置通知提醒登录后重新生成恢复码。历史无效邮箱返回 `ErrBadEmail`（422）并回滚，注册和找回使用相同的可解析邮箱边界校验。

找回邮箱计数独立使用 `reset_email` 键及本地 `guards.resets`（复用可配置的 `email_sign_in_failures`，默认 15 分钟 10 次），不返还；IP 共用登录计数。共享键仍只含 HMAC、故障按节点分摊，算法复用 `takeAttempt`。测试见 `password_reset_test.go`、`password_change_test.go` 和控制台的 `console_password_test.go`：真实数据库、受控过期、显式后台推进，不用耗时阈值判断安全。

### 登录前的资源

| 登录前的资源 | 默认上限（配了 Valkey 时各节点共享计数，不可用时退回进程内、按核心节点数等分；没配时进程内计数，只用于单核心） | 站点设置 |
|---|---|---|
| 客户端 IP | 只认 `RemoteAddr`；`AXIOM_TRUSTED_PROXIES` 里的代理才读 `X-Forwarded-For`，取最右一个不受信的地址；默认不信任任何代理 | — |
| 每 IP 的登录、注册、两步验证、找回密码 | 令牌桶，突发 20 次、每分钟 10 次，按（站点, IP）计 | `ip_attempt_burst`、`ip_attempts_per_minute` |
| 每邮箱的登录失败、找回请求（各自计数） | 每 15 分钟 10 次，按（站点, 规范化邮箱）计，不存在的邮箱也计；超出后等待，不锁账户 | `email_sign_in_failures` |
| 每用户的 TOTP、恢复码失败 | 每小时 10 次，跨待验证会话累计，重建恢复码的错码也计 | `user_code_failures` |
| 每个待验证会话 | 5 次，计在 `sessions.mfa_attempts` | `challenge_code_attempts` |
| argon2id 并发 | `GOMAXPROCS` 个，排队超过 2 秒返回 `ErrBusy`（503）；请求在排队时结束就带它自己的错误返回 | 按节点：`Config.HashConcurrency`、`HashWait` |
| key 未命中查库 | 每 IP 令牌桶加全局并发 32，超出返回 503 和 `Retry-After` | — |
| 计数器本身 | Valkey 里带到期时间的键，装满的桶没有键；进程内（单核心模式和兜底）是有容量上限的 LRU。argon2id 名额和 key 未命中查库的令牌桶保护的是本节点，始终按节点 | — |

- 超限返回 `*TooManyAttempts`（`errors.Is(err, ErrTooManyAttempts)` 仍成立），带 `RetryAfter`：桶再放过一次尝试的时间，控制台接口据此写 `Retry-After`（至少 1 秒）。开启 TOTP 也算：错的验证码或密码用掉用户的验证码失败次数（与登录的验证共用），"已经开启"这类没在猜什么的拒绝不算。
- 三种计数都是令牌桶：每段时间 N 次，即每过「段时间 ÷ N」回一次。桶只存"再次装满的时刻"（GCRA），装满的桶等于没有，到时过期。次数用完返回 `ErrTooManyAttempts`（429），等待即恢复。
- **多节点**（M2）：配了 Valkey 时，桶在 Valkey 里，由 `take`、`give` 两个脚本按同样的算法原子地扣和退，键里的邮箱和 IP 只以 HMAC 出现（从主密钥派生的专用密钥；IP 先经 `clientip.Group` 归组——`Unmap`、去 zone、IPv6 取 /64，与网关的按 IP 限流同一份，进程内的桶同样按此计）；建任何登录键之前先过进程内的粗 IP 桶；`give` 只退到 `take` 扣的那一边；Valkey 不可用时退回进程内的桶，突发除以节点数 N（至少 1）、间隔乘 N，不拒绝登录。脚本、键、兜底和恢复见 [platform/kv 的设计](../platform/kv/README.md)。
- 失败次数**先扣后退**：尝试之前就扣一次，成功才退回，所以同时到达的尝试也超不过上限；扣不到的尝试不再读取凭证或计算哈希；安全拒绝仍单独写审计与失败计数。IP 的次数不退。
- 每次 argon2id 计算（登录、未知邮箱的假哈希、注册、改密码、邮件重置、开启 TOTP 时核对密码）都要先拿到并发名额，用 `golang.org/x/sync/semaphore`。

## 命令行

`Operator` 以 `cmd/axiom` 交入的平台范围执行，是做出第一个站点和用户、授予平台角色、在 M1 改密码和重置两步验证的途径；每个命令在同一事务里记审计，操作者是 `system:accounts.cli`，输出一行 JSON。

| 命令 | 做什么 |
|---|---|
| `axiom site init --name N --currency CODE --console HOST --api HOST [--admin HOST] [--key-prefix P]` | 一个事务建主站（注册策略 invite）、首个等级「Default」（新账户的默认等级）、各监听面的域名；`localhost` 的域名登记 `http://` 的源，其余登记 `https://`。主站已存在返回 `ErrSiteExists`，域名已被占用时，域名的唯一约束拒绝写入，整个事务回滚。币种必须写明且主站只能是 `USD`（[计费](../../docs/domains/billing.md)「钱的表示」），其他值在写库前被拒 |
| `axiom user create --email E`（密码读标准输入） | 在主站建用户和个人账户，与注册共用 `createUser`（账户、租户登记、默认工作区、用户、所有者，再调开户回调），无邀请人；邮箱与密码的规则同注册 |
| `axiom user grant-platform --email E [--role admin]` | 授予内置角色；已持有则不变，不重复记审计（同时授予也只留一条，靠部分唯一索引） |
| `axiom user set-password --email E`（密码读标准输入） | 换密码并加因素版本：旧会话、待验证的登录、未用的恢复码失效 |
| `axiom user reset-mfa --email E` | 清除 TOTP 密钥并加因素版本；用户可在控制台重新开启 |

- 密码只读标准输入（最多一行，去掉末尾的换行），不进进程列表和 shell 历史。找不到用户返回 `ErrNotFound`；站点还没建返回 `ErrNoSite`，提示先运行 `site init`。
- 改因素的两个命令同控制台一样：事务第一条语句锁用户行，在锁内改，所以与进行中的登录互斥。
- **开户回调**：`Config.OnAccountOpened` 和 `NewOperator` 的参数是同一个 `OpenedFunc`，在开户事务里账户建好之后调用；`cmd/axiom` 接到 `billing.OpenBalance`（底线 0），所以账户一出现就有余额行，回调失败整个事务回滚。accounts 不能引用 billing（[依赖表](../../docs/architecture.md#模块边界规则)），回调就是「下层定义接口、上层实现」。

## 不变量

| 不变量 | 靠什么保证 |
|---|---|
| 账户数据的 `site_id`、`account_id` 与"key → 工作区 → 账户 → 站点"一致 | 两列不为空、复合外键、不在可改的列里；测试插入混合的组合失败 |
| 成员是账户所在站点的用户；个人账户只有主人一个成员 | `members` 的主键、复合外键和生成列外键，加上 `users.account_id` 的唯一键和 `users` 恒为 personal 的生成列外键 |
| 每个账户恰好一个默认工作区，不能删 | 注册事务建；部分唯一索引、`is_default` 不可改、约束、无 DELETE |
| 平台角色只授予主站用户 | `platform_grants` 对 `sites(id, is_main)` 的外键 |
| 撤销和删除是终态 | 「行级安全的写法」；`pg_policies` 检查 |
| key 只存哈希，明文只在创建响应里出现一次 | 表里没有明文列；创建响应是唯一带明文的类型 |
| key 能否使用每次调用以库为准 | `Admit` 在准入事务里读库；缓存只存不变的标识。测试：两个 `Service` 共用一个库，一个停用，另一个下一次 `Admit` 被拒；客户端先超时、服务端随后提交停用，之后的 `Admit` 被拒；禁止的 IP 先请求，允许的紧接着成功 |
| 定规则和路由只用库里的当前值 | `Admission` 带当前的 `preset_id`、`models`、`tier_id`，缓存条目里没有这几项；测试：两个 `Service` 共用一个库，各认出同一把 key 并预热缓存后，一个改预设、可用模型（从 A 改成 A+B）、等级，另一个下一次 `Admit` 交给 `fn` 的是新值；`fn` 里的路由与最高价检查用 `Admission` 的值，接线的证明在 #27 |
| 准入事务里不等待，满载排队不占数据库连接 | `fn` 只做内存计算和预留，满载时返回"需要等待"、在事务外等；本模块测试：`fn` 返回"需要等待"后事务已回滚、连接已归还池子。接线的验收在 #27：连接池设为 N，模型 A 只有一个并发名额且有多于 N 个请求在排队，期间模型 B 的准入和另一个调用的结算照常完成、在预算之内，排队中的 A 请求占用的连接数为 0 |
| 缓存命中时不查库 | 预热后关闭 `pg.DB`，`AuthenticateKey` 仍成功；指标 `axiom_accounts_key_cache_misses_total`（OpenTelemetry 的 `axiom.accounts.key_cache.misses`，每次真去问数据库的查找加一，命中和负缓存都不加） |
| 登录前拿不到租户数据 | 无范围的事务 + 「登录前的查找」的函数和启动检查 |
| 范围和认证结果不可伪造 | 生产代码只有本模块能构造账户和站点范围（forbidigo）；`Caller`、`Principal`、`Admission` 的字段不导出 |
| 敏感权限只给本次做过两步验证的会话 | `level = mfa`；测试：先登录的密码会话后来被授予管理员，仍被拒 |
| 一次性因素只能用一次，一个挑战只换出一个会话 | 用户行锁 + 条件更新 + 影响行数；测试：两条连接同时提交同一个码、同一挑战的两个码，都只有一个成功 |
| 登录前的猜测有上限，换挑战、换会话不重置 | 每邮箱、每用户的失败计数先扣后退，每 IP 的令牌桶，argon2id 的并发名额；测试：站点设置的 IP 和邮箱上限，等待后恢复，成功不计；两个挑战各错 5 次后第三个挑战的对码也被拒，回一次后通过；名额被占时登录返回忙，请求结束时等待随之结束 |
| 两步验证开启后只能经命令行重置，不能被覆盖 | 用户行锁内核对 `mfa_enabled_at`；测试：新密钥、原密钥同一步长都被拒，版本、会话、恢复码不变；两次同时开启只有一次成功 |
| 账户一出现就有余额行 | 注册和命令行建用户都经 `createUser`，开户回调在同一事务里；测试：回调失败则没有用户，命令行建出的账户有余额行 |
| 一个用户同一平台角色只有一条有效授予 | 部分唯一索引 `platform_grants_held` + `ON CONFLICT DO NOTHING`；测试：两条授予同时开始（授予表被锁住直到两条都在等待），只留一条授予、一条审计 |
| 主站的币种是 `USD` | `InitSite` 写库前核对；测试：EUR 被拒，没有任何写入（核对在事务之前） |
| 站点的币种不可改 | `axiom_app` 对 `sites.currency` 没有 `UPDATE` 权限；`Money` 的币种由它决定 |
| 认证因素的修改都有审计 | 审计在修改的事务里写；测试：两种修改各一条，数据库拒绝审计时开启失败，因素、恢复码、会话都没有留下 |
| 改了认证因素，旧会话、旧恢复码、进行中的登录都失效 | `factor_version` 相等才有效，签发在用户行锁内核对；测试两种先后：重置先提交，之后用旧密码算出的结果签发失败；签发先持锁时重置等待，签发成功，重置提交后刚签发的会话认证失败 |
| 快照不决定能否访问 | 站点、域名的状态和注册策略在库里判定；测试：只停用域名，另一个 `Service` 的准入、会话、登录、注册立即被拒；重载持续失败 90 秒后全部 503 |
| 站点改短会话时长，已签发的会话也按新的算，之后放宽也不延长 | `SetSiteSettings` 在同一事务里把新时长写进已有会话（`CutSessions`，只取小）；测试：改短后空闲过久的、年龄过大的会话下一个请求被拒，没超的不受影响（`TestShorterSessionsEndOnTheNextRequest`）；改短、期间没有请求、再放宽，会话仍失效（`TestALoosenedSiteDoesNotRevive`） |
| 站点设置的替换与审计同成同败，改前的值就是被替换的值 | 站点行锁内读改前、写、记审计；测试：越界的替换既不改设置也不留审计，替换两次有两条，条目有改前改后 |

## 管理接口

管理后台（`api/admin.yaml`）对站点、用户和账户做的读写。函数都收调用方的事务，事务要在平台范围（管理接口的守卫授权后给出），审计在同一个事务里写（[平台的「审计日志」](../platform/README.md#审计日志)）。

| 函数 | 做什么 |
|---|---|
| `SiteSettings`、`SetSiteSettings` | 读、整体替换站点自己的会话时长和新 key 的每日上限；没设的列是空，用代码里的默认（`DefaultSiteSettings`）。越界是 `*SettingError`（名字是接口里的字段名），站点不存在是 `ErrNotFound`。替换在站点行锁里做，审计条目 `site_settings.update` 记改前和改后的值，改前就是被替换的值 |
| `SiteLoginLimits`、`SetSiteLoginLimits` | 独立 `/login-limits` 读写现有五项阈值，空值选择 `DefaultLoginLimits`；站点锁内读旧值、替换、审计，与其他站点设置互不覆盖；写后刷新，其余节点 30 秒内重载 |
| `SetAccountSuspended`、`SetUserSignInDisabled` | 分开控制调用和登录，前者复用准入同一条查询里的账户状态，后者在用户锁内吊销全部会话，恢复均不修改因素 |
| `UserPlatformGrants`、`SetPlatformGrantRevoked` | 列表各角色最后状态，撤销终态不回退；恢复要求历史授予且权限为操作者当前平台角色权限的子集，插入新行并记录 granted_by；按用户锁串行 |
| `Refresh` | 立即重载快照。改了站点设置的节点调用它；别的节点在重载间隔内（30 秒）跟上 |
| `Site`、`MainSite` | 快照里的站点；管理接口用它取币种和默认站点 |
| `Tiers` | 站点的等级，按名字 |
| `Users` | 用户列表，新的在前，键集分页（上一页最后一个用户的 ID），可按站点、按邮箱前缀筛。前缀按字面匹配（`%`、`_` 不是通配符）、不分大小写，用 `users_email_prefix`（`text_pattern_ops`）的索引。创建时间取自 UUIDv7 的时间戳（ID 的顺序就是创建的顺序，没有 `created_at` 列） |
| `Account` | 账户、等级、主人；账户没有成员时是 `ErrNotFound`（账户总有主人） |

- **权限**：管理接口的八个（均在 `authorize.go` 声明，迁移 `00006` 给内置角色）：`users.view`、`suppliers.view`、`catalog.view`、`pricing.view`（非敏感）；`credits.grant`、`suppliers.manage`、`catalog.manage`、`pricing.manage`（敏感，要做过两步验证的会话）。内置角色（迁移后）：

  | 角色 | 权限 |
  |---|---|
  | owner | `api_keys.view`、`api_keys.manage`、`workspaces.manage`、`calls.view`、`balance.view`、`balance.top_up` |
  | admin | owner 的全部，加 `sites.manage`、`platform_grants.manage`、`users.manage` 和管理接口的八个 |
  | member | `api_keys.view`、`api_keys.manage`、`calls.view` |
  | finance | `api_keys.view`、`balance.view`、`balance.top_up`、`users.view` |
  | viewer | `api_keys.view`、`calls.view`、`balance.view`、`users.view`、`suppliers.view`、`catalog.view`、`pricing.view` |
  | support | `api_keys.view`、`calls.view`、`users.view` |

  只有 admin 能赠送/调整余额、改凭证、价格和目录；owner、admin、finance 能用自己的资金充值，其余角色最多看。谁该有 `credits.grant` 这类权限是负责人的决定，现在只给 admin。
- **管理会话的权限是平台授予的**：管理监听面的会话，`SessionInfo` 的权限列表取用户未撤销的平台授予的角色，不是账户里的成员关系；`Authorize` 对平台目标本来如此。
- **站点缩短会话时长，已有的会话也缩短**：`SetSiteSettings` 在改设置的同一事务里，把站点新的空闲时长、最长时长（没设的取代码默认值）写进该站点、该监听面未撤销的会话：`expires_at` 与 `created_at` 加新最长时长取小，`idle_timeout` 与新空闲时长取小（`CutSessions`）。所以改短立即对所有节点生效，并且存进了会话本身：之后站点再放宽，被改短的会话也不会恢复，哪怕改短期间它没有发过请求。只会缩短，不会延长已签发的会话。`ValidSession` 和 `SessionInfo` 另把存下的时长与快照里站点现在的值取小，挡住别的节点在重载快照前按旧时长新签发的会话。应用因此对 `sessions` 的 `expires_at`、`idle_timeout` 有更新权限（迁移 00006），只经 `CutSessions` 往小改。

## 公开接口（草图）

```go
func NewService(db *pg.DB, cfg Config) (*Service, error) // cfg: 平台范围、哈希密钥、时钟，以及共享的缓存与登录键（secret 从主密钥快照派生）；New 是 sqlc 生成的
func (s *Service) Load(ctx context.Context, logger *slog.Logger) error // 站点、域名、设置、角色的快照；核对权限名；启动重载循环
func (s *Service) SiteForHost(host string) (Site, Surface, error) // Host 原样（小写、非默认端口带端口）；ErrUnknownHost、ErrStale
func (s *Service) Origin(host string) (string, error) // 该域名登记的 origin（含协议和端口），服务端拼链接时用，不用请求头里的 Origin；错误同上
func (s *Service) CreateKey(ctx context.Context, tx pg.Tx, actor audit.Actor, r KeyRequest) (CreatedKey, error)
func (s *Service) DeleteKey(ctx context.Context, tx pg.Tx, actor audit.Actor, key KeyID) error
func (s *Service) AuthenticateKey(ctx context.Context, site SiteID, raw string) (Caller, error) // 只认出，不判定能否使用
func (s *Service) GetKeyInfo(ctx context.Context, tx pg.Tx, key KeyID) (KeyInfo, error)      // 名称、标签（前缀...末四位）、过期时间，供 GET /key；别的账户的 key 是 ErrKeyNotFound
func (s *Service) AccountWorkspaces(ctx context.Context, tx pg.Tx, account AccountID) ([]uuid.UUID, error) // 账户全部工作区的 ID（含已删除），供 billing 汇总累计花费
func (s *Service) GatesLookups() bool                    // Config.LookupSlot 是否配了；gateway 的 NewHandler 要求配了
func (s *Service) Admit(ctx context.Context, c Caller, r AdmitRequest,
	fn func(tx pg.Tx, a Admission) error) error              // fn 由 gateway 组装：定规则、路由、billing 的准入
func (a Admission) Scope() pg.Scope                         // 同一调用后续事务（追加、结算、SettleOrphan）的账户范围，只能从 Admission 取得
func (s *Service) Authenticate(ctx context.Context, r *http.Request) (Principal, error) // 站点和监听面取自 Host；改数据的请求另核对 Origin、JSON、CSRF
func (s *Service) Authorize(ctx context.Context, p Principal, perm Permission, t Target) (pg.Scope, error)
func (s *Service) Register(ctx context.Context, r RegisterRequest) (UserID, error)
func (s *Service) SignIn(ctx context.Context, r SignInRequest) (Issued, error)    // 开了 TOTP 的得到待验证会话
func (s *Service) VerifyMFA(ctx context.Context, r *http.Request, code string, ip netip.Addr, userAgent string) (Issued, error)
func (s *Service) SignOut(ctx context.Context, p Principal) error
func (s *Service) SiteSettings(ctx context.Context, tx pg.Tx, site SiteID) (SiteSettings, error) // 管理接口：平台范围的事务；见「管理接口」
func (s *Service) SetSiteSettings(ctx context.Context, tx pg.Tx, actor audit.Actor, site SiteID, v SiteSettings, reason string) error
func (s *Service) Users(ctx context.Context, tx pg.Tx, q UserQuery) ([]UserSummary, bool, error) // 另有 Tiers、Account、Site、MainSite、Refresh
func (*Service) BeginTOTP(p Principal) (secret, uri string)
func (s *Service) ConfirmTOTP(ctx context.Context, p Principal, c FactorChange) (Issued, []string, error)
func (s *Service) RegenerateRecoveryCodes(ctx context.Context, p Principal, c FactorChange) (Issued, []string, error)
func NewOperator(db *pg.DB, platform pg.Scope, opened OpenedFunc) *Operator // 命令行：InitSite、CreateUser、GrantPlatform、SetPassword、ResetMFA
func (s *Service) AuthenticateAny(ctx context.Context, r *http.Request) (Principal, error) // 同 Authenticate，但接受待验证会话：getSession、signOut
func (p Principal) Pending() bool; func (p Principal) Verified() bool
func (p Principal) SessionID() SessionID // 认出主体的会话，管理接口的探测限次按会话计
func (s *Service) SessionInfo(ctx context.Context, p Principal) (SessionInfo, error)  // 邮箱、邀请码、级别、CSRF、到期、按级别过滤的权限名
func (s *Service) ListKeys(ctx context.Context, tx pg.Tx, account AccountID, after KeyID, limit int) ([]Key, bool, error) // 新的在前，按 ID 翻页
func (s *Service) GetKey(ctx context.Context, tx pg.Tx, id KeyID) (Key, error)
func (s *Service) UpdateKey(ctx context.Context, tx pg.Tx, actor audit.Actor, id KeyID, k KeySettings) (Key, error) // 整体替换，含重新启用；先取旧值，审计记改了什么
func (s *Service) CheckOrigin(r *http.Request) error                         // 登录、注册等没有会话的改数据请求
func (s *Service) Cookie(host string, issued Issued) (*http.Cookie, error)
func (s *Service) ExpiredCookie(host string) (*http.Cookie, error) // 登出时让浏览器丢掉会话 Cookie
```

控制台和管理接口：处理器在 `cmd/axiom`（它们要组合 accounts、billing、observability，只有 `cmd/axiom` 能同时引用三者），只经各模块的公开接口；`cmd/axiom` 的守卫先按契约做站点、Origin、会话、CSRF、权限的检查（见[平台的「接口约定」](../platform/README.md#接口约定)），处理器拿到的 `Principal` 和范围都是 accounts 签发的。gateway 和 billing 的准入必须经 `Admit` 打开事务，billing 的预留只接受 `Admission`；本模块的测试只证明 `Admit` 本身，接线的证明在 #27、#28。

## 决策与理由

| 决策 | 理由 | 不选 |
|---|---|---|
| 登录前的查找用专用角色拥有的定义者函数 | 只把秘密或名字换成标识；角色只读几列，`axiom_app` 进不去 | 不受行级安全的查找表：破坏"全部 FORCE"；调用路径用平台范围：漏一个条件就跨租户；函数里设平台范围：`just scope` 禁止，而且要另外证明范围不会留在调用方事务里 |
| 定义者函数按所有者统一检查，不另建登记表 | 函数的配置只有固定的 `search_path`，所有者唯一，规则对所有函数相同；新迁移加函数，旧程序照样能启动 | 库里或程序里的函数清单：要多维护一份数据，程序里的清单还会让旧程序在新迁移后无法启动 |
| 准入时读库判定 key 能否使用 | 见[账户与租户的「决策与理由」](../../docs/domains/accounts.md#决策与理由) | 缓存状态 + 失效协议 + 单核心锁：两轮评审都找到反例；LISTEN/NOTIFY：同样有提交不确定和断线的窗口 |
| 定规则和路由放进准入事务，用 `Admission` 的预设、可用模型、等级 | 能被改的路由依据只有一个来源，没有过时的副本可用 | 缓存路由依据、准入时比较、不同就重新路由：要补过期时间让在路由阶段就被拒的请求也能刷新，多三个机制，过期之前仍会误拒；缓存只靠准入刷新：收紧的预设被绕过，被拒在准入之前的请求永远刷新不到 |
| 满载时结束准入事务，在事务外等待，醒来重新 `Admit` | 等待时不占数据库连接，一个模型满载拖不垮别的模型的准入和结算；重新读库也就看到等待期间的停用 | 在事务里排队：排队的请求占满连接池；在事务外等完后沿用旧的 `Admission`：等待期间的停用和改动不生效 |
| 缓存键是 `(站点, 哈希)` | 错站点的请求不会污染负缓存 | 只用哈希 |
| 认证因素的事务先锁用户行，会话和恢复码绑定 `factor_version` | 锁让核对与签发之间插不进重置；版本让旧会话不用逐条吊销 | 只在签发前核对版本：Read Committed 下核对与插入之间仍能插入重置；逐条吊销：漏一条就留后门 |
| 会话级的 `level` | 证明这条会话做过两步验证 | 用户级的 `mfa_enabled_at`：证明不了 |
| key 哈希用 HMAC-SHA256，最多 2 个版本一起查，版本记在行上 | key 文本与密钥轮换无关 | 版本写进 key：key 文本跟着轮换走；argon2：调用路径太慢，256 位随机串也不需要 |
| 哈希密钥放受保护的文件 | 与主密钥同样的保管方式，环境变量会出现在 `docker inspect` 和子进程里 | 环境变量 |
| 登录的 CSRF 用精确 Origin + JSON + 会话令牌 | 不多一种登录前状态 | 预会话令牌：多一种状态；只靠 SameSite：同站的子域仍能发起 |
| 注册用只覆盖新账户的范围 | 注册拿不到别人的数据 | 用站点范围包住注册：能读写全站用户 |
| 安全参数默认值在代码，按站点的运行值在 `site_settings`，按节点的在环境变量 | 安全措施要能配置（[安全的「要求」](../../docs/security.md#要求)），M1 就能改 | 写死到以后：违反要求；通用配置链：M1 用不到它的分层合并 |
| key 数上限在插入 key 的事务里、取账户行锁之后数 | 先数后插在并发下不安全：两个创建同时读到"还剩一个"就都插入；锁与插入在同一个事务里，计数必然看到前一个提交 | 唯一约束：每把 key 都不同，约束不上；进程内计数：多节点各数一份；先数后插（new-api `token.go:326`、sub2api `api_key_service.go:442` 都是这样）：并发下超上限 |
| 小时频率按库盖的 `created_at` 和库的时钟数，含已删除的 key | 窗口起点和建 key 的时间要出自同一个时钟：UUIDv7 的时间是各节点自己生成的，慢两分钟的节点建的 key 会被别的节点早两分钟排出窗口，同一小时就能超限额；库只有一个时钟，且在取得账户锁之后取 | 数 ID 里的时间：各节点时钟不一，测试也只能手工造 ID；每节点令牌桶：限额乘上节点数；Valkey 计数：多一个依赖，而要数的 key 本来就在库里 |
| key 限额分站点和账户两层，各自单独的接口，账户的盖过站点的 | 任务卡要求后台可按账户调整；站点的是该站点所有账户的缺省，账户上只记它与缺省不同的；站点的不放进整体替换的站点设置，免得旧页面的请求把它抹掉 | 只有站点一层：不能让一个账户比别的多或少；再加等级一层：等级现在没有设置项，要先有地方放 |
| 审计里可用模型和 IP 白名单只记「变了」 | 模型最多 256 个、每个 200 字，写进 `detail` 会超过 8 KiB；地址是日志不记的个人信息 | 记全部值：条目可能写不进去，或把地址写进日志；记条数：说不清改成了什么 |
| 恢复码和会话令牌只存 SHA-256，密码用 argon2id | 80 位和 256 位的随机串猜不中，快哈希就够；密码是人选的，要慢哈希 | 全部 argon2：每个请求认会话都要一次慢哈希 |
| 对外 ID 只在 HTTP 边界转换 | 库里索引小，Go 里类型安全 | 库里存 TypeID：索引更大；对外用原始 UUID：看不出是什么 |
| 每种 ID 各写 `TypeID` 和 `Parse…ID`，不用泛型 | 每种只多一个类型和两个短函数，类型名和报错直接是 `SiteID`；前缀在本模块，一眼能对上 | `ID[K]` 加每种一个标记类型：省不了几行，读的人要多绕一层 |
| Valkey 不可用时登录前的次数退回进程内、按节点数等分，不拒绝登录 | Valkey 只放丢了也不影响正确性的数据（[总体架构](../../docs/architecture.md#技术选型)），它出故障就挡住所有人登录，正是[安全的「账户安全」](../../docs/security.md#账户安全)要避免的「把用户锁在门外」；进程内的桶按份额限制后，N 个节点合计的回填速度不超过设定、突发不超过 max(突发, N)，代价只是每次故障或重启至多多一次突发，argon2id 名额仍按节点限制猜测速度 | 失败即拒（sub2api 认证路由在 Redis 故障时一律 429，`routes/auth.go:34`；new-api 回 500，`middleware/rate-limit.go:119`）：缓存故障变成登录故障；兜底不等分：故障期间限额放大 N 倍 |
