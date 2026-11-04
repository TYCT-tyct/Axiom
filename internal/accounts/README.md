# accounts

站点、用户、账户、成员、工作区、API key、等级、登录与会话、授权，以及租户范围的构造。要求和业务设计见[账户与租户](../../docs/domains/accounts.md)，这里写代码结构、数据、不变量和接口。

> 本文先于代码写成，是 [Issue #23](https://github.com/TYCT-tyct/Axiom/issues/23) 的设计；各部分随实现的 PR 落地，代码与本文不一致时，在同一个 PR 里改本文。

## 范围

| 本模块管 | 不归本模块，归谁 |
|---|---|
| 站点、域名、key 前缀、注册策略、默认等级、站点的安全与会话参数 | 收款渠道（`payments` 声明为站点配置项） |
| 用户、密码、TOTP、恢复码、通行密钥、第三方登录身份、会话、邀请关系 | 发邮件（M2） |
| 账户、成员、工作区及其停用 | 余额、在途预留、额度、账本，以及「余额不足」「暂停」（`billing`） |
| 等级本身、账户在哪个等级 | 等级的加价（`billing` 的定价规则） |
| key 的设置：业务速率、花费上限与重置、工作区预算、过期、预设 ID、可用模型、IP 白名单 | 花费计数与准入时的检查（`billing`，设置作参数传入）；预设内容（`routing`）；限流计数（`gateway`） |
| 权限、角色、授予；构造账户和站点的租户范围 | 资金上限、审批、审计的哈希链与回放（`operations`） |

## 数据

### 表（schema `accounts`）

迁移 `00009_scope_once.sql` 将全部范围策略改为 [pg 的 InitPlan 表达式](../platform/pg/README.md#决策与理由)，包括密码找回请求和令牌，保留终态与到期清理条件。迁移 `00020_session_devices.sql` 为 `sessions` 增加按用户、监听面和创建时间过滤有效行的并发索引 `sessions_live_of_user`，供设备列表和批量撤销使用。

- 主键 `id uuid`，由 Go 生成 UUIDv7。金额是 `bigint`，单位同[计费的「钱的表示」](../../docs/domains/billing.md#钱的表示)。`status` 取 `active` / `disabled`。
- 类别的含义见[安全的「租户隔离」](../../docs/security.md#租户隔离)。站点数据的表用 `site_id`、账户数据的表用 `(site_id, account_id)` 与 [pg 的 InitPlan 范围参数](../platform/pg/README.md#决策与理由)比较；`sites` 自己用 `id` 当 `site_id`，`accounts` 用 `id` 当 `account_id`。
- `axiom_app` 只对「可改的列」有 `UPDATE` 权限，除密码找回请求、令牌、待验证注册请求和已派发用户提醒到期清理外，没有 `DELETE` 权限。

| 表 | 主要列 | 键与约束 | 类别 | 可改的列 |
|---|---|---|---|---|
| `sites` | `parent_site_id`、`owner_account_id`、`is_main`（`GENERATED ALWAYS AS (parent_site_id IS NULL) STORED`）、`parent_is_main`（恒为真的生成列）、`name`、`status`、`key_prefix`（`[a-z0-9-]{2,16}` 且以 `-` 结尾）、`registration`（invite / closed / open；默认 invite，开放注册默认关闭）、`currency`（站点的记账币种，三个大写字母，默认 `USD` 只补迁移前已有的站点，`axiom site init` 必须写明；创建后不可改，所以 `Money` 的 `currency` 一经发出不会变；`getSite` 也把它交给页面），`brand_color`（可空，`#rrggbb` 小写，空表示默认配色），`default_tier_id`（不为空）、`logo_hash`（当前 logo 的 SHA-256，可空） | `UNIQUE (id, is_main)`；部分唯一 `(is_main) WHERE is_main`；`(parent_site_id IS NULL) = (owner_account_id IS NULL)`；`(parent_site_id, parent_is_main) → sites(id, is_main)`：上级只能是主站，站点只有两层；`(parent_site_id, owner_account_id) → accounts(site_id, id)`；`(id, default_tier_id) → tiers(site_id, id)`，延迟到提交时检查；`(id, logo_hash) → site_logos(site_id, hash)` | 站点 | `name`、`status`、`key_prefix`、`registration`、`brand_color`、`default_tier_id`、`logo_hash` |
| `site_domains` | `domain`（小写，主键）、`site_id`、`surface`（console / api / admin）、`origin`（完整的源，生产是 `https://域名`，开发可以是 `http://localhost:端口`）、`disabled_at` | `origin` 是 `https://` 加 `domain`，或 `http://localhost[:端口]` | 站点 | `disabled_at` |
| `site_logos` | `site_id`、`hash`（SHA-256，32 字节）、`content_type`（image/svg+xml / image/png / image/webp）、`width`、`height`（像素，SVG 没有固有尺寸时为 0）、`bytes`（1 到 256 KiB）、`created_at` | 主键 `(site_id, hash)`；`0 ≤ width, height ≤ 1024`；同类字节只有一行，地址与内容一一对应 | 站点 | 无（只插入；旧行保留，`sites.logo_hash` 选择当前行） |
| `legal_documents` | `site_id`、`kind`（terms / privacy）、`locale`（zh / en，默认 zh）、`version`、`body`（Markdown 纯文本 1 到 65536 字符）、`published_at` | 滚动兼容阶段保留旧主键 `(site_id, kind, version)`，另加 `UNIQUE (site_id, kind, locale, version)`；`version ≥ 1`；行只插入，正文不改不删 | 公开数据 | 无（只插入） |
| `legal_acceptances` | `site_id`、`account_id`、`user_id`、`kind`、`locale`（默认 zh）、`version`、`accepted_at` | 主键 `id`；旧 `UNIQUE (user_id, kind, version)` 与新 `UNIQUE (user_id, kind, locale, version)` 并存；旧/新外键都指向文档；`BEFORE INSERT` 触发器按文档解析省略的 locale | 账户 | 无（只插入） |
| `site_billing_preferences` | `site_id`（主键）、`preference`（四个计费顺序之一，默认 `subscription_first`） | `site_id → sites`；只存站点默认的扣费顺序，不含管理员的会话与限额设置，所以账户范围可以读自己站点的这一行 | 站点 | `preference` |
| `site_settings` | `site_id`（主键）；可空的 `console_idle_timeout`、`console_max_age`、`admin_idle_timeout`、`admin_max_age`、`new_key_daily_limit`、`model_prices_public`（价格是否对未登录访客公开，空表示公开；只在所有存活节点支持时隐藏），以及「注册、登录与会话」的尝试上限 `ip_attempt_burst`、`ip_attempts_per_minute`、`email_sign_in_failures`、`user_code_failures`、`challenge_code_attempts`；开放注册的 `registration_human_check`、`registration_human_check_bits`、`registration_domain_burst`、`registration_domain_per_hour`、`registration_domain_allow`、`registration_domain_deny` | 每列有上下界：时长最短 5 分钟，最长依次 30 天、90 天、12 小时、1 天；每日上限 0 到 100 万额度；尝试上限至少 1，最多依次 1 万、1 万、1000、1000、100；空表示用代码里的默认值。另有可空的 `max_keys_per_account`、`key_creations_per_hour`（站点的 key 限额，各 1 到 1 万，由 `/key-limits` 读改，不在站点设置的整体替换里） | 站点 | 全部参数列 |
| `tiers` | `site_id`、`name`、`rate_requests` | `UNIQUE (site_id, id)` | 站点 | `name`、`rate_requests` |
| `roles` | `name`、`permissions text[]`、`additional_permissions text[]`、`builtin` | `UNIQUE (name)`；迁移写入默认角色 owner、admin、member、finance、viewer、support，权限为空，由在代码里声明权限的 PR 用迁移补上 | 平台 | `permissions`、`additional_permissions`（M1 只经迁移） |
| `accounts` | `site_id`、`kind`（personal / organization）、`name`、`status`、`tier_id`，可空的 `max_keys`、`key_creations_per_hour`（账户自己的 key 限额，各 1 到 1 万，空表示用站点的）、`rate_requests`、`billing_preference`（账户自己的扣费顺序，可空，空表示用站点默认） | `UNIQUE (site_id, id)`、`UNIQUE (site_id, id, kind)`；`(site_id, tier_id) → tiers(site_id, id)`，它同时保证 `site_id` 是一个站点 | 账户 | `name`、`status`、`tier_id`、`max_keys`、`key_creations_per_hour`、`rate_requests`、`billing_preference` |
| `workspaces` | `site_id`、`account_id`、`name`、`status`、`is_default`、`budget`、`budget_reset`、`deleted_at` | `UNIQUE (site_id, account_id, id)`；`(site_id, account_id) → accounts`；部分唯一 `(account_id) WHERE is_default`；默认工作区的 `deleted_at` 为空；`budget_reset` 取 daily / weekly / monthly，空表示不重置的总额，有它就要有 `budget` | 账户 | `name`、`status`、`budget`、`budget_reset`、`deleted_at` |
| `users` | `site_id`、`account_id`（个人账户）、`account_kind`（恒为 `personal` 的生成列）、`email`（规范化）、`email_verified_at`（邀请码注册为空，点击验证链接时写入）、`password_hash`（argon2id 的 PHC 串）、`factor_version`、`status`、`invite_code`、`inviter_user_id`、`totp_ciphertext`、`totp_nonce`、`totp_wrapped_dek`、`totp_key_version`（`secret.Sealed` 的四个字段，同为空或同不为空）、`totp_last_step`（默认 0）、`mfa_enabled_at` | `UNIQUE (site_id, id)`、`UNIQUE (site_id, account_id, id)`；`UNIQUE (account_id)`、`(site_id, account_id, account_kind) → accounts(site_id, id, kind)`：个人账户只属于这一个用户，不能绑到企业账户上，两个登录身份不会共用一个账户范围；`UNIQUE (site_id, email)`；`UNIQUE (site_id, invite_code)`；`(site_id, inviter_user_id) → users(site_id, id)`；`inviter_user_id <> id` | 账户 | `password_hash`、`factor_version`、`status`、四个 `totp_` 密文列、`totp_last_step`、`mfa_enabled_at` |
| `members` | `site_id`、`account_id`、`account_kind`、`user_id`、`user_account_id`、`role_id`、`personal_owner`（`GENERATED ALWAYS AS (CASE WHEN account_kind = 'personal' THEN account_id END) STORED`） | 主键 `(account_id, user_id)`；`(site_id, account_id, account_kind) → accounts(site_id, id, kind)`；`(site_id, user_account_id, user_id) → users(site_id, account_id, id)`；`(site_id, personal_owner, user_id) → users(site_id, account_id, id)`：个人账户的成员只能是它唯一的用户，所以不需要另加部分唯一索引；`role_id → roles` | 账户 | 无，也没有 `UPDATE` 策略（移除成员随企业账户加 `removed_at`） |
| `platform_grants` | `site_id`、`site_is_main`（恒为真）、`account_id`（被授予者的个人账户）、`user_id`、`role_id`、`granted_by`（授予者的用户 ID，命令行授予时为空）、`revoked_at` | `(site_id, site_is_main) → sites(id, is_main)`：只能授予主站用户；`(site_id, account_id, user_id) → users(site_id, account_id, id)`；部分唯一 `(user_id, role_id) WHERE revoked_at IS NULL`：一个用户同一角色只有一条有效授予 | 账户（被授予者的），写入另限平台范围 | `revoked_at`（终态；恢复插入新授予） |
| `presets` | `site_id`、`account_id`、slug、name、config、created_at、updated_at、deleted_at | 活跃 `(account_id,slug)` 唯一、复合账户外键 | 账户 | name、config、updated_at、deleted_at（终态） |
| `api_keys` | `site_id`、`account_id`、`workspace_id`、`kind`（call / management）、`name`、`prefix`、`last4`、`secret_hash`、`secret_version`、`created_by`、`created_at`、`updated_at`、`management_window`、`management_requests`、`expires_at`、`status`、`spend_limit`、`limit_reset`、`preset_id`、`models`、`ip_allow`、`deleted_at`、`rate_requests` | `(site_id, account_id, workspace_id) → workspaces(site_id, account_id, id)`；`(site_id, created_by) → users(site_id, id)`；`UNIQUE (secret_hash)`；管理 key 必须有 `expires_at`；请求计数 0–61（61 为已超限）；`limit_reset` 同工作区的 `budget_reset` | 账户 | `updated_at`、`management_window`、`management_requests`、`name`、`status`、`spend_limit`、`limit_reset`、`preset_id`、`models`、`ip_allow`、`deleted_at`（终态）、`rate_requests` |
| `sessions` | `site_id`、`account_id`（个人账户）、`user_id`、`surface`（console / admin）、`token_hash`、`csrf_token`、`level`（pending_mfa / password / mfa）、`factor_version`、`mfa_attempts`、`created_at`、`last_seen_at`、`expires_at`、`idle_timeout`、`revoked_at`、`ip`、`user_agent` | `UNIQUE (token_hash)`；`(site_id, account_id, user_id) → users(site_id, account_id, id)` | 账户 | `last_seen_at`、`mfa_attempts`、`expires_at` 和 `idle_timeout`（只往小改，见「站点缩短会话时长」）、`revoked_at`（终态）、`recovery_codes_ciphertext`、`recovery_codes_nonce`（用会话令牌派生的密钥加密，读出后同事务清空，撤销时由触发器清空） |
| `recovery_codes` | `site_id`、`account_id`、`user_id`、`factor_version`、`code_hash`、`used_at` | 同上外键；`UNIQUE (user_id, code_hash)`，也是消费时的查找 | 账户 | `used_at`（终态） |
| `security_failures` | `site_id`、`started_at`、`failures`、`completed_peak`、`sampled_at`、`reported` | 每站点一行；当前 UTC 分钟计数、尚未采样完整窗口峰值、最后采样时间及是否待恢复；部分索引只含有计数或待恢复的站点 | 站点 | 仅平台可写计数与采样状态 |
| `password_reset_requests` | `site_id`、`email_hash`、`id`、可空 `email`、`domain`、`language`、`expires_at` | 主键 `(site_id, email_hash)`；唯一 `id`；摘要 32 字节；语言 zh/en | 站点 | `id`、`email`、`domain`、`language`、`expires_at`（重发替换，处理后清除邮箱） |
| `password_reset_tokens` | `id`、`site_id`、`account_id`、`user_id`、`token_hash`、`factor_version`、`expires_at`、`used_at` | `token_hash` 唯一、32 字节；`(site_id, account_id, user_id) → users` | 账户 | `used_at`（终态） |
| `registration_requests` | `id`、`site_id`、`email_hash`、`email`、`password_hash`、`tier_id`、`domain`、`language`、可空 `token_hash`、`expires_at` | 主键 `id`；`(site_id, email_hash)` 唯一，重复请求替换；`(site_id, tier_id) → tiers`；`token_hash` 唯一、32 字节；语言 zh/en；默认 24 小时到期 | 站点 | `password_hash`、`tier_id`、`domain`、`language`、`expires_at`、`token_hash`；可删除 |
| `notification_preferences` | `site_id`、`account_id`、`user_id`；可空的 `low_balance`、`top_up`、`factor_changed`、`spend_anomaly`、`threshold`；`webhook_url`、`language` | 主键 `user_id`；复合用户外键；阈值非负、语言 zh/en | 账户 | 七项偏好列 |
| `user_notices` | 来源、种类、数值、正文补充（`detail`）、时间、`email_done`、`dispatched`、冻结的 Webhook URL 与状态 | 主键 UUID；唯一 `(user_id,kind,source)`；复合用户外键；排队与终态分开索引 | 账户 | `email_done`、`email_failed`、`dispatched`、`webhook_url`、`webhook_state`；平台仅可删除已派发、非排队、超过 30 天的行 |
| `announcements` | `site_id`、纯文本标题和正文、`starts_at`、`ends_at`、`withdrawn_at` | 站点外键、结束晚于开始；撤下为终态 | 站点公开配置 | 平台只能改 `withdrawn_at` |


- **租户列直接存在行上**：账户数据的每张表都有 `site_id`、`account_id`，都不为空。被引用的父表有 `UNIQUE (site_id, id)` 一类的复合唯一键，子表用复合外键引用它，所以两列不可能与"key → 工作区 → 账户 → 站点"不一致；两列不在可改的列里，写入后不变。外键检查不受行级安全影响。理由见[账户与租户的「决策与理由」](../../docs/domains/accounts.md#决策与理由)。
- **授予每种目标一张表**：账户角色在 `members.role_id`，平台角色在 `platform_grants`；站点员工、工作区角色以后各加一张。每张表的外键都直接，不需要一张多态表靠条件约束拼归属。
- `members` 在 M1 只有个人账户的主人一行，按所属账户归类即可。企业账户的成员要读到自己在别的账户里的成员关系，怎么分类随企业账户确定（待定）。
- 新账户的 `tier_id` 取 `invite_tenant` 或 `registration_site` 在库里读出的 `default_tier_id`，之后与默认等级无关。不取快照：快照可能晚 30 秒，而写进账户的等级以后不会再改。
- 名字（站点、等级、角色、账户、工作区）1 到 100 个字符。
- 新 key 的 `spend_limit` 取快照里站点的每日上限（`site_settings` 为空则用代码默认值 10 额度，开发者先定、负责人可改），`limit_reset = daily`；用户可以改成别的值或不设。

### 行级安全的写法

所有表 `ENABLE` 并 `FORCE ROW LEVEL SECURITY`。每条命令一条策略，不用 `FOR ALL`：多条宽松策略按 OR 合并，一条 `FOR ALL` 就能绕过终态的限制。

- `FOR SELECT USING (范围)`；`FOR INSERT WITH CHECK (范围)`。
- 有终态列（`revoked_at`、`deleted_at`、`used_at`）的表：`FOR UPDATE USING (范围 AND 终态列 IS NULL) WITH CHECK (范围)`。旧行必须还没进终态，新行可以进入；进入后整行不能再改，包括清除终态列，平台范围也一样。
- 其他表：`FOR UPDATE USING (范围) WITH CHECK (范围)`。
- `platform_grants` 的 `INSERT`、`UPDATE` 用 `(SELECT platform.in_platform_scope())`：平台范围包含所有账户，所以它比类别更严，不更宽。
- 测试：首次撤销或删除成功，之后清除终态列、改其他列都影响 0 行；迁移测试查 `pg_policies`，有终态列的表上没有 `FOR ALL`，也没有第二条 `UPDATE` 策略。

## 业务速率

迁移 `00015_rate_limits.sql` 给 tiers、accounts、api_keys 各加可空的 `rate_requests`（0–10 亿），不写默认数据；继承、不限和默认值见[账户域](../../docs/domains/accounts.md#业务速率限制)。`Admission.RateLimits` 随准入读取最新账户/key 额度，配置不进身份缓存。`Service.RateLimit` 读后台原值和有效值；`Service.SetRateLimit` 持目标行的 FOR NO KEY UPDATE 锁修改并写 before/after 审计（只改非键列，不阻塞新建 key/用户所需的外键 KEY SHARE 锁），后台独立资源要求 MFA、sites.manage、幂等与理由，target 与 tier/acct/key TypeID 必须对应。普通 key CRUD 不读写该列，旧页面和管理 key 无法清除或放宽它。

不变量：等级是账户的缺省，不是全等级共享计数；修改与审计同事务，失败一起回滚；未配置默认 6000，只在 `DefaultRateRequests` 定义一次；速率窗口由 gateway 定义，默认一分钟。等级 SELECT 策略在原站点/平台范围外，只允许账户读取自己当前指定的等级；不增加等级写入权限，不能看到同站未指定或别站等级。更换等级或修改额度在下一次准入生效，不增加准入查询次数。滚动发布中旧核心仍只执行原有节点/key 保护；全部核心升级后，账户共享计数与新业务策略才完整生效；原 key 计数键保持不变。与单独查询可变策略或缓存策略相比，沿用准入 JOIN 能立即生效且无需失效协议。

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
| 管理 key | `WithKeyManager` 打开的事务里是 key 所属账户与本站公开配置的范围 |
| 快照加载 | 平台范围的 `ReadTx` |

- `Caller`（认出的 key）、`Principal`（认出的用户）、`Admission`（准入结果）的字段都不导出，只有认证成功的函数能造出非零值；其他模块只读访问器。`Caller` 不带范围：带 key 的事务只能经 `Admit` 打开。零值的 `Caller` 交给 `Admit` 被拒，有测试。
- `Authorize(principal, permission, target)` 只返回覆盖本次目标的范围。账户目标的范围是 `pg.AccountScopeIn(会话签发的站点, 账户)`：除了账户自己的行，还读这个站点的公开配置和共享计数（策略把站点列与 `platform.home_site()` 的 InitPlan 参数比较，如充值页的付款方式、每 IP 的下单计数），别的站点的读不到；站点来自会话，不来自账户能写的行。目标由服务端从 URL 里的 ID 解析，行级安全再挡一层：别的账户的工作区在这个账户的范围里不存在，返回 404。
- 权限在代码里声明名字和是否敏感；角色快照合并 permissions 与 additional_permissions，警告并忽略代码不认识的名字，避免新版本权限使旧节点快照过期。迁移 00010 将新增权限放进 additional_permissions，保留不支持忽略未知名字的旧节点所读 permissions 原样；`TestRolePermissionsRollingCompatibility` 同时核对旧投影及迁移权限的声明，防止拼写错误。敏感权限要求本次会话 `level = mfa`。
- M1 声明的权限：`api_keys.view`、`api_keys.manage`、`workspaces.manage`、`calls.view`、`balance.view`、`balance.top_up`，以及敏感的 `sites.manage`、`platform_grants.manage`、`users.manage`，和管理接口权限（含 `calls.search`、`calls.inspect`、`audit.view`）（见下文「管理接口」）。默认角色由迁移 00003 写入，00005、00006、00010 改成现在的集合，列在「管理接口」的表里；以后加权限的 PR 用迁移改这些集合。`PermissionNamed` 按名字取声明过的权限，契约里的 `x-axiom-permission` 靠它在启动时核对。
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
| `accounts.registration_site(domain)` | 控制台域名 | `site_id`、`default_tier_id`、注册模式；站点停用或域名不属控制台时无结果 |
| `accounts.registration_tenant(domain, token_hash)` | 控制台域名、验证令牌的 SHA-256 | 待验证请求 `id`、`site_id`、邮箱和是否未过期；域名和令牌不匹配时无结果 |
| `accounts.registration_request_tenant(id)` | 待验证请求 ID | `site_id`、空 `account_id`；供后台和点击链接时构造站点范围 |

- 上述八个函数都已实现（迁移 00002、00003、00019）。`login_tenant` 只认控制台和管理后台的域名，`invite_tenant` 只认控制台域名。
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

`Admission.OwnerID` 从同一次准入查询的用户表取得当前个人账户唯一所有者（`users.account_id` 唯一且只指向个人账户），供健康窗口去重；不使用 key 的创建人或会话的终端用户。尚无所属用户的账户返回零 ID，不改变 key 能否准入；quality 仍计测量，但不把缺少所有者的结局计入摘除窗口。角色表只有平台范围可见，因此这条账户范围查询不连接角色表，也不增加数据库往返。准入查询把等级、所有者、业务速率和计费偏好作为一个 42 字节值返回（两个 UUID 各 16 字节，再加账户/key 有效速率各 4 字节、计费偏好 2 字节，网络字节序；-1 由代码补默认值；偏好 0–3 对应四个顺序），调用面直接复制到类型化 ID；逐列扫描 UUID 的实现会使准入从 64 增至 66 次分配，固定宽度组合实测为 63 次分配（基线 64），避免这项回归，不新增持久化格式。企业账户的所有者语义随企业账户功能设计。`TestAdmissionCarriesOwnerForHealthEvidence` 守住传递。

**缓存只存不会变的东西；能被改的（停用、删除、过期、IP 白名单、可用模型、预设、上限、等级）每次调用都在准入事务里读库，判定能否使用、定规则和路由都只用读出的值。** 所以缓存陈旧、并发的缓存填充交出旧结果、提交确认丢失、多个核心节点，都不会让停用后的 key 通过，也不会按旧预设路由，不需要失效协议。

1. **认出**（`AuthenticateKey`，内存）：解析并核对校验位，用 `(site_id, 当前版本的哈希)` 查缓存。条目里只有 `key_id`、`account_id`、`workspace_id`、`site_id`，建后永不改变。未命中时按 `(site, 哈希)` 合并并发的查找。共享的查找不随发起它的请求取消，但沿用它的截止时间（网关里是 `KeyLookup`，期限只在调用方一处定）；调用方没设截止时间时最多 1 秒。每个等待者仍在自己的 context 结束时离开（`TestEachWaiterKeepsItsDeadline`、`TestALookupKeepsItsCallersDeadline`）。配了 `Config.LookupSlot` 时，这次共享的查找先取一个名额、查完才还（发起它的请求走了也不提前还，等它的请求不另取），名额取不到就按查找失败处理、不写负缓存；无范围调用 `key_tenant`，无结果写负缓存、返回 401；有结果写入缓存。
2. **准入**（`Admit`，数据库）：gateway 的单请求路径调用 `accounts.Admit(ctx, caller, req, fn)`（并发路径是下面的 `AdmitBatch`），`req` 带请求的域名、可信解析的客户端 IP。本模块用 key 所属账户的范围打开事务，执行一条查询：key ⨝ 工作区 ⨝ 账户，条件为 `domain_site(请求的域名, 'api') = key.site_id`，key、工作区、账户都启用，key 与工作区未删除，未过期，`kind = call`，`ip_allow` 为空或包含客户端 IP。其他条件有效而账户停用时回滚并返回 `ErrAccountSuspended`（403）；其余条件不满足返回与"key 不存在"相同的 `ErrUnknownKey`（401）。满足就把当前的 `preset_id`、`models`、`kind`、`tier_id`、`spend_limit`、`limit_reset`、工作区预算放进 `Admission` 交给 `fn`；`AdmitRead` 是同一条查询收 `call` 和 `management` 两种 key，只给 `/key` 和 `/credits` 用，`Admission.KeyKind()` 说出实际是哪一种。`fn` 由 gateway 组装：用 `Admission` 定规则、路由（认模型里名字解析在准入之前做，key 的可用模型这一半在 `fn` 里按 `Admission.models` 约束，不允许返回 403），再调 billing 的准入在同一个事务里预留（批量路径是 `ReserveMany`）。`fn` 里只有对内存快照的有界计算和预留的数据库语句，不访问缓存、不等待：限流计数和会话粘性在 `Admit` 之前取好，端点负载在进程内，都作为路由的输入（见[路由的「容量感知」](../../docs/domains/routing.md#容量感知)）；候选都满载时 `fn` 返回"需要等待"，`Admit` 回滚（这时还没有预留），gateway 在事务之外等，醒来后只重新调 `Admit`，不再访问缓存、不重复计限流；权限、预设、可用模型、等级都重新读库，路线也重新计算。
3. 不预留的 key 接口（`GET /api/v1/key`、`/credits`）经 `AdmitRead` 打开事务（收调用 key 和管理 key），`fn` 只读。gateway 的 `file-parser` 在把文件发给后端之前调 `Check`：同一条查询、同样的拒绝，不跑 `fn` 也不预留，只用来把明显不能用的 key 挡在解析工作之前；准入仍然自己再查一次，`Check` 的答案不作为放行依据。

**批量准入**（`AdmitBatch`，数据库）：同一账户的并发调用合并成一个事务。`tx.Batch` 一次发出每个调用的准入查询，每个调用各自成功、得到 `ErrAccountSuspended` 或 `ErrUnknownKey`；成功的调用连同 `Admission` 按原顺序交给 `fn`，`fn` 的错误回滚整批。调用方保证批内是同一个账户、调用者非零；混合账户是程序错误，直接报错而不是跨租户开事务。`TestAdmitBatchChecksEveryCallAndKeepsTheRest` 守住一个被拒的调用不影响同批其他调用。

- **为什么停用立即生效**：停用的提交被确认之后开始的调用，其准入查询都在提交之后执行，读到的就是停用（Read Committed 每条语句取新快照），与缓存内容、节点数、提交确认是否送达都无关。改预设、可用模型、等级同理：下一次调用就按新值路由和计价，最高价也按新等级的售价比较。
- **路由在准入事务里，不在它之前**：路由要的预设、可用模型、等级都会被改，在准入之前路由就只能用缓存里的旧值，收紧的预设会被绕过，放宽的会被旧值一直拒在准入之前。事务里的路由只读内存快照和事务外取好的缓存结果，不加锁、不查库、不访问缓存，所以不增加[总体架构](../../docs/architecture.md#模块边界规则)调用路径上的查库例外，只让事务多开着路由计算那一段时间。
- **满载排队不占数据库连接**：等待放在事务里，一个满载模型的排队请求每个都占着一条连接，能把连接池占满，别的模型的准入和结算都拿不到连接；在事务外等，排队的请求不占连接，醒来重新准入还顺带读到等待期间的停用和改动。重新准入的次数受路由的排队时间上限约束，到期返回 429。
- **停用的 key 在准入之前能看到什么**：解析和名字解析在准入之前，所以停用的 key 仍可能收到请求格式类的错误（400、模型不存在），但不会调用上游、读不到租户数据、不产生费用。准入的拒绝不缓存：拒绝可能与请求有关（IP），缓存它会误拒同一把 key 的合法请求。
- **防枚举只承诺到这里**：不存在、属于别的站点、停用、过期、删除、IP 不允许，状态码和正文完全相同。剩余的时间差：本站真实存在的 key 多一次准入查询；缓存命中与未命中不同。只有持有这把 key 的人能利用。
- **key 的设置**（`KeySettings`）：名称、停用、花费上限（十亿分之一额度，空表示不限）、重置周期（空表示总额不重置，要有上限）、可用模型（空表示任意，空列表按空处理）、IP 白名单（`cidr`，主机位清零）。创建调用 key 时不写上限就取站点的新 key 每日上限、按日重置；管理 key 不套用此默认，不写上限表示签发上界不限，其创建的调用 key 省略 limit 时也继承此不限上界；`UpdateKey` 整体替换，没写的就是不限制，所以页面把读到的设置连同修改送回；到期时间只能在创建时给。创建、整体替换、删除各记一条审计（`api_key.create`、`api_key.update`、`api_key.delete`），改成停用或重新启用也是 `update`。`Key.CreatedAt` 是库里盖的 `created_at`（建 key 的 `InsertKey` 里取 `statement_timestamp()`），小时频率限额用同一个时间。输入的检查（币种、不为负、重置需要上限、地址能解析、到期在将来）在 `cmd/axiom` 的处理器里，进模块的设置已满足表的约束。
- **key 列表的搜索与筛选**：`ListKeys` 在同一个读事务里先 `SET LOCAL plan_cache_mode = force_custom_plan` 和 `SET LOCAL enable_sort = off`，再按 `(account_id, id DESC)` 的索引顺序读。名字（不分大小写的子串，按字面匹配，`%`、`_` 不是通配符）、前缀（不分大小写的前缀）、状态（active/disabled/expired；过期的先算过期，再分是否停用）和创建时间（起含止不含）都在读索引时过滤；游标仍是上一页最后一把 key 的 ID，所以没有筛选时第 N 页的代价随本页读到的行数增长，不随账户历史增长；稀疏筛选会沿索引顺序多读匹配范围，但仍不排序（#408）。`expired` 用数据库的 `statement_timestamp()` 判。最后使用时间不提供：库里没有这一列，从 `observability.call_log` 推导要么跨模块扫全历史，要么在准入事务里每次写 key 行；两者都比本页贵，所以列表不按它过滤。响应里的 `filters` 回显本节点实际应用的筛选；旧节点没有这个字段且会忽略筛选参数，页面在确认回显匹配前不展示它的行（滚动发布期间）。目录里已下线的模型由 `cmd/axiom` 在响应里给 `retired_models` 提示，`accounts` 不引用 `catalog`；快照不可用时省略该字段。
- **key 的数量与创建频率**：每个账户最多保有多少把未删除的 key（调用与管理共用），以及每小时最多创建多少把（已删除的也算），按三层取值：账户自己的（`accounts.max_keys`、`accounts.key_creations_per_hour`）盖过站点的（`site_settings.max_keys_per_account`、`key_creations_per_hour`），站点没设的用代码里的默认（100、50）；每个限额单独往上找，空表示没设。`CreateKey` 在插入前检查（`admitKey`），都在同一个事务里：先取账户行的 `FOR NO KEY UPDATE`（`LockAccount`，同时读出账户自己的限额；与引用账户行的外键互不阻塞），再数。两个请求同时建最后一把时后一个等前一个提交后数到它，所以不会都插进去。数量是 `CountKeys`（未删除的 key），用迁移 00013 的 `api_keys_live_of_account` 部分索引，不扫描已删除历史；控制台列表也用它按 ID 分页。频率是 `RecentKeys`：这个账户一小时内建的 key 的真实数量，和要等多久才有空位。一小时按**库的时钟**判：`api_keys.created_at` 由 `InsertKey` 里的 `statement_timestamp()` 盖，窗口起点也在同一个语句里取 `statement_timestamp()`，两者都在取得账户锁之后，所以各节点自己的时钟差不影响（测试 `TestKeyCreationsIgnoreTheNodesClocks`）。因此不用 ID 里的时间：UUIDv7 是建 key 的节点按自己的时钟生成的。要等的时间是「使计数降到限额以下的那把 key」满一小时的时间，即按时间从旧到新数的第「数量 − 限额 + 1」把；限额调低后数量仍是真实的窗口数，不被截断。索引 `api_keys_created_by_account (account_id, created_at)`（迁移 00012；旧的调用 key 部分索引保留给滚动发布的旧节点） 含已删除的 key，所以没建过 key 的账户只读到自己的几行，不随全站一小时内的建 key 数增长（`TestKeyCreationsUseTheIndex` 看执行计划）。两种拒绝都是 `*TooManyKeys`（`errors.Is(err, ErrTooManyKeys)` 成立，带生效的限额和数到的数量）；超频率的还带 `RetryAfter`。控制台接口据此写 409（`too_many_keys`）和 429（`rate_limited`，带 `Retry-After`），正文写明限额和数量。站点的限额取快照里的值，别的节点最久 30 秒后生效；账户的限额在建 key 的事务里读，立即生效。调低限额不会删掉已有的 key，超过的账户只是不能再建。
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

### 命名请求配置

`accounts.presets` 保存账户的 `{slug,name,config}`，业务合并规则与来源在[路由的「命名预设」](../../docs/domains/routing.md#命名预设)。`SavePreset`、`GetPreset`、`ListPresets`、`DeletePreset` 在调用者的事务里显式限定账户；创建/替换用账户行锁串行化，只改部分字段的 PATCH 在读之前用 `LockPresetAccount` 取同一把锁，创建锁内检查数量与唯一 slug，配置和审计原子提交。配置不写入审计明细，避免系统提示和工具说明进入通用日志。删除只置 deleted_at，旧行不能改回；名字重建用新 UUID。迁移 `00018_presets.sql` 的 SELECT/INSERT/UPDATE 均启用并强制 RLS，仅授予 name、config、updated_at、deleted_at 更新权限。

配置快照随站点快照整体发布，键为站点、账户、slug；`PresetConfig` 返回不可变字符串，不能把内部切片借给调用者。响应缓存的两个成员 `cache_enabled`（布尔）和 `cache_ttl_seconds`（1–86400 秒）也在这里校验：前者让预设决定开关，后者只给 TTL；它们都不会进入发往上游的正文，合并与命中行为由 gateway 的「响应缓存」解释。读写边界检查白名单、递归引用和瞬态字段；保存前用 PostgreSQL 规范化为 jsonb 文本，输入与存储文本均以 16 KiB 为限，数据库约束使用同一存储上限。数字指数展开超限或 jsonb 不支持的值（如 NUL）返回 ErrPreset，写入和审计均不发生。重载对不合法的单条配置记告警并跳过（日志仅含 ID 和原因），避免租户数据或滚动升级的新字段阻断全平台刷新；数据库读取失败仍阻止快照发布，超过既有陈旧期限拒绝请求。这里保存配置，不依赖 protocol 或 routing；协议解析与参数的最终可执行性由 gateway 在合并后检查。`TestAccountPresets` 覆盖同名隔离、RLS、快照、删除重建和审计；`TestPresetLimitConcurrent` 用两个真实事务竞争最后一个名额。`TestPresetStoredBoundary`、`TestPresetDatabaseSizeBoundary` 检查存储表示的边界和失败写入的原子性，`TestPresetReloadSkipsInvalidRows` 检查坏行与未来字段的故障隔离。

### 程序化 key 管理

`cmd/axiom/management.go` 将 `/api/v1/keys` 挂在公网监听的模型路径之前，独立于控制台 Cookie 守卫；`management_input.go` 负责请求边界。签发仍用控制台的 `CreateKey`，可选 `kind` 默认为 call，management 要将来到期并额外验证 `api_keys.view`，不能只靠 `api_keys.manage` 签发包含读权限的凭证。`Key.Kind` 在控制台响应中可选，旧节点不提供时调用者应视为能力不可用。管理 key 的 spend_limit 不套用新调用 key 的站点默认，空表示签发上界不限。

- `WithKeyManager` 先取已有的 `Config.LookupSlot` 准入名额，整个请求持有、所有出口归还，避免已缓存管理 key 的洪泛绕过数据库背压（拿不到名额为 `ErrManagementBusy`，HTTP 503）；然后在独立事务锁管理 key、验证、扣分钟次数；再在操作事务重新锁定和验证，直到回调提交。先按类别取行锁，再用新语句验证，避免锁等待保留旧授权快照。两次均查 `kind = management`、状态、终态、到期、IP、所属工作区和账户、API 域名及站点。请求计数与操作分开提交，失败和重试不能返还次数。SQL 用数据库分钟、饱和计数及未来窗口保留，数据库时钟后退时不额外放行；每 key 每分钟 60 次，超限给 `ManagementRateLimit.RetryAfter`。
- `KeyManager` 字段不导出，只能由成功授权构造，且只在回调内使用；`Ceiling` 复制限制中的指针和名单，HTTP 的字段合并不能改变授权对象。`Create` 只签发 call，服务端固定站点、账户和创建人；`Get`/`Update`/`Delete` 的目标必须是尚未删除的 call。账户范围的 RLS 再挡跨账户和跨站点，工作区 ID 也在该范围里验证。
- `Create` 和扩大权限的 `Update` 的最终设置必须在管理 key 的上界内：限额金额不更大且重置周期相同，模型及 IP 网段为子集，到期不晚于管理 key。没有名单表示不限，空名单不能绕过非空上界。禁用或删除管理 key 不追溯撤销先前签发的调用 key；它们有自己的生命周期和预算。旧 key 的纯收紧操作不新增授权，因此即使原设置或到期超过管理 key 的签发上界，也允许减少限额、名单及停用；不能通过这种例外把已停用的 key 启用。重新启用等新增授权仍须满足全部签发上界，故永久 key 需由控制台重新启用。
- `KeyManager.Get` 取目标行锁后读设置；PATCH 在锁内读、合并、验证、替换，防止与控制台的 PUT 丢失更新。审计复用 `recordKey`/`keyChange`，主体为 `audit.ManagementKey`，写入失败与改动整体回滚。`updated_at` 与修改一起写，旧行初始为空。迁移 00011 的新计数约束和平台 00016 的扩大操作者集合均用 NOT VALID 避免持有 DDL 锁扫描历史：旧计数全部是常量零，旧操作者集合蕴含新集合，故存量已满足约束；新增和修改仍由数据库检查。两个索引各在独立的 NO TRANSACTION 迁移里并发构建，保留旧调用 key 索引供滚动节点使用；失败后须先删同名无效索引再重跑，与现有 billing 索引迁移一致。
- 请求最多 64 KiB、总期限 5 秒（context 与网络读写 deadline）；未缓存的管理 key 的查找只有网关的 `KeyLookup`（默认 1 秒），不用整个请求的 5 秒（`TestManagementKeyLookupHasTheGatewaysBudget`）；客户端请求体在管理事务和准入名额之外读完并解析，解析错误在认证并扣次数后返回，慢上传不能占用数据库连接或管理 key 行锁。列表按 created_at/id 倒序、每页 100、offset 从零开始；默认列默认工作区且不含已停用或到期的 key，可用 include_disabled/include_expired 和 workspace_id 筛选。响应使用 OpenRouter 的 data/key/deleted 外壳与数字 HTTP 错误码；无效或类型不符的凭证统一 401。花费计数经 billing 的 `ReadKeyUsages` 在相同账户事务内一次批量读取整页的累计、日、周、月及限额窗口计数，包含在途预留的 limit_remaining 不低于零。
- PATCH 区分未写和 null；显式清空 limit 而省略 limit_reset 时同时清空周期，仍须满足签发上界；合并后设置相同则直接返回原 key，不更新 updated_at 或写空审计。未知字段拒绝，不能用 kind、account_id、预算或过期时间写入绕过边界。创建省略的限制继承管理 key；显式 null 的花费/名单清空仍要经过上界比较，永久过期时间被拒绝。limit 接受精确到十亿分之一的本站额度 JSON 数值，拒绝负数、越界或精度丢失；没有 BYOK，byok_usage 各字段为零，include_byok_in_limit 为 true（输入选择对不存在的 BYOK 无影响）。hash 为非秘密的 32 字符 UUID 十六进制，不暴露 HMAC 存储哈希；创建人的外部表示为 UUID。工作区创建参数是 UUID，PATCH 不支持迁移工作区；其余组织/外部用户字段未实现，属于有意限制，理由见[领域文档](../../docs/domains/accounts.md#决策与理由)。

验证：`TestManagementCRUDAndAudit`、`TestManagementKeyTypeAndScope`、`TestManagementDelegationNeverExpands`、`TestManagementCeilingIsIndependent`、`TestManagementRateAndAuditFailure`、`TestManagementRevalidatesOnEveryNode`；HTTP 全流程及非法输入在 cmd 的 `TestManagementAPIProvisioningLifecycle`、`TestManagementAPIRejectsEscalationAndMalformedInput`、`TestManagementAPIAuthenticationAndRate`。变异证明在提交前对限额、模型/IP 范围、自我管理、跨账户和类型隔离的关键检查各运行一次，具体结果写 PR 验证证据。

### 快照

- 第三方登录与通行密钥的表：`site_oauth_providers`（每站点每 provider 一行，client secret 以 `secret.Sealed` 四个字段信封加密，同样由触发器 `fence_envelope_key_version` 拒绝写入已退役的 `client_secret_key_version`，`UNIQUE (site_id, provider)`）、`user_identities`（`UNIQUE (site_id, provider, subject)` 与 `UNIQUE (user_id, provider)`，只按 provider 的 subject 匹配）、`passkey_credentials`（`credential_id` 全局唯一，`sign_count` 限 0 到 4294967295，每人最多 10 个）、`oauth_flows`（state 与登录 nonce 只存 SHA-256、10 分钟过期、一次性）和 `webauthn_flows`（kind register/sign_in/mfa、5 分钟过期、一次性，登记发起时的 RP ID 和 origin；`sign_in` 不带用户和账户，其余带会话）。`passkey_tenant(bytea)` 是登录前的查找函数，只返回凭据所属的站点、账户和用户。
- 站点、域名、站点设置、角色各是一份不可变的值，用 `atomic.Pointer` 整体替换。只有一个后台循环重载：每 30 秒无条件重载一次（本进程写这些表的接口加入时，再加写后标记"需要重载"），覆盖别的节点和命令行的修改；循环串行，旧值不会覆盖新值。平台的变更通知（[总体架构的「技术选型」](../../docs/architecture.md#技术选型)）做好后也触发重载，30 秒的兜底仍保留：快照不决定能否访问，滞后只影响价格页和参数。
- **快照不决定能否访问**：域名和站点的启用、注册策略在库里判定，平台授予每个请求读库。快照只用来预筛没登记的域名（快照里没有就直接拒，省一次查库）、匿名价格页和计价预览按站点默认等级（`Site.DefaultTier`）选规则、尝试上限等参数、角色的权限集合。
- 站点快照带着当前 logo 的 hash（`Site.LogoHash`，没有时为空），不带宽高和字节：`getSite` 只拼公开地址，图片请求才按 hash 读库。旧 logo 的行不会因为换图而删除，所以已经发出的地址继续有效。
- 站点快照还带着本站 API 域名的 origin（`Site.APIBase`，没有启用的 API 域名时为空；有多个时优先 HTTPS，同协议取 origin 最小的那个，快照每 30 秒重载，地址不能因此变来变去）：控制台用它告诉用户把客户端指向哪里（[API key](../../docs/domains/accounts.md#api-key)「接入地址」）。它只是给界面看的地址，和准入、计价无关。
- 超过 90 秒（3 个周期）没有成功重载，所有请求返回 503，直到重载成功；启动时第一次加载失败就不启动。

## 注册、登录与会话

安全事件写 `platform.audit_log`：`security.registration`、`security.sign_in`、`security.mfa` 与成功状态同事务，MFA 的 session_id 指向新签发的会话。猜凭证的失败（bad_credentials、bad_code、bad_passkey、invalid_invite）以 `security.registration_failed`、`security.sign_in_failed`、`security.mfa_failed`、`security.factor_failed`、`security.password_failed` 在独立、有 5 秒期限的事务提交，只含固定分类、监听面、用户/会话/租户 ID 和上下文请求 ID。缺 Cookie、已过期/吊销/待验证会话、随机或已过期的通行密钥流程、表单校验及限流拒绝不记安全审计、不计攻击。token 哈希查不到会话时，按调用者传入的可信客户端 IP 经过既有登录预算后只计数、不写逐请求审计；代理解析仍由 cmd 负责。邮箱、IP、User-Agent、密码、邀请码、TOTP/恢复码及令牌不进条目；存储失败返回错误，不伪报成功。

失败事务只原子更新每站点 `security_failures`，不调用平台信号入口。窗口按数据库 UTC 分钟对齐；跨分钟时将已完成次数并入 completed_peak 再重置当前计数，后台迟到也不丢峰值。`SampleSecurityAlerts` 每轮以 `FOR UPDATE SKIP LOCKED` 最多认领 100 个站点，按 sampled_at 限制每站点一分钟最多一条 `accounts.authentication_failures` 信号，取未采样完整窗口的最大值，保留尚未结束的窗口。非零上报后的下一个完整零窗口才发一次恢复；后台停顿期间多个窗口只保留峰值，不保留无限历史。消费逐条评估完整样本再处理下一个，保证积压中的高值后接低值或恢复也不会漏首通知。配置 kind 对应的阈值与渠道（如 threshold=20、duration_seconds=0，表示一个完整 UTC 分钟至少 20 次猜凭证失败）；未配置则不发通知，不自动封号。审计沿用平台保留策略；计数每站点一行，信号增长按采样频率而非失败频率。

| 能力 | M1 | M2 |
|---|---|---|
| 注册 | 邀请码；开放注册（邮箱验证、自托管工作量证明、域名规则和频率限制） | 邮箱验证码；第三方登录只绑定已有用户，不注册 |
| 登录 | 邮箱 + 密码；GitHub、Google、LinuxDO；通行密钥；第二因素为 TOTP、恢复码或通行密钥 | 邮箱验证码 |
| 找回密码 | `axiom user set-password`（从标准输入读） | 控制台邮件链接（已实现） |
| 第一个用户 | `axiom site init`、`axiom user create`、`axiom user grant-platform`（见下文「命令行」） | — |
| 认证因素 | 开启、更换 TOTP，重建恢复码，增删通行密钥，绑定/解绑第三方身份；控制台改密码 | 关闭 TOTP（仍可经命令行 `reset-mfa`） |
| 会话管理 | 退出、设备列表、踢掉一台、退出全部 | — |

- **邀请码注册**：`invite_tenant` 在库里核对站点启用、策略为 invite、邀请码有效，得到邀请人和默认等级。服务端生成新账户、工作区、用户的 ID，在只覆盖这个新账户的范围里，一个事务插入账户、默认工作区、用户、成员（所有者）。邮箱重复由唯一索引拒绝，整个事务回滚；重试得到"已注册"。邮箱规范化（去空白、转小写）后须是 3 到 254 个可打印 ASCII 字符、`@` 两侧各有字符，且必须通过 `net/mail.ParseAddress`、解析地址等于输入，否则 `ErrBadEmail`：表的约束（`lower()`、`LIKE`）在各地区设置下对非 ASCII 的判断不一定与 Go 一致，放进库再被拒会变成 500；国际化邮箱地址 M1 不支持。不用站点或平台范围包住注册。
- **开放注册**：`Register` 先用 `domain_site` 和 `registration_site` 在库里确认站点、默认等级和模式；open 时校验邮箱、站点域名名单和 `proofWorked`，再检查本节点能发信、按邮箱域名扣一次共享计数，在站点范围按 `(site_id, email_hash)` upsert `registration_requests` 并入队 `accounts.registration`。待验证行不保存口令：初次提交即使带了口令也不使用，点击链接时才提交、校验并算 argon2id。提交一律返回 202；已注册地址也走同一条路径，直到点击链接才由 `UserEmailTaken` 发现并返回 `ErrEmailTaken`。后台 `process` 用 `TenantTx` 锁待验证行，按请求记录的 `domain` 找仍启用的 HTTPS 控制台域名，生成 256 位令牌、只存 SHA-256、写 `token_hash` 并把邮件入 outbox，然后同事务提交；重投任务看到 `token_hash` 已写就不重复发。`SetSiteRegistration` 在同一事务写站点模式和设置并记 `site_registration.update`；审计只存域名名单的数量与 SHA-256，不复制整份名单，保证 100 项的合法配置也不会超过审计条目 8 KiB 上限。`VerifyEmail` 先花一次该 IP 的认证预算，再用 `registration_tenant` 定位并做未过期、域名名单的廉价检查，校验新口令并在锁外算 argon2id，再在站点范围锁行，核对未过期、令牌哈希和当前域名名单（不重查注册模式；站点停用后链接失效），创建用户、账户、默认工作区、余额行、所有者成员关系，写 `email_verified_at`、删行、记 `security.registration`，全部同事务；不签发会话。令牌未知、过期、已用或别站统一 `ErrInvalidVerification`；邮箱已被占用返回 `ErrEmailTaken` 并删行。
- **注册同意**：`PublishLegalDocument` 在站点行锁内取该 `kind` 的下一个版本并插入，正文是站长的 Markdown，审计只记类型、locale、版本、字符数和 SHA-256；同一行永不更新或删除。滚动兼容阶段版本号按 `kind` 跨语言共享，旧节点仍能用 `(site, kind, version)` 主键和旧发布语句；旧节点退出后，清理迁移才把编号改成每个 `(kind, locale)` 独立。`CurrentLegalDocuments` 取每个 `(kind, locale)` 的最大版本，公开读。邀请码 `Register` 和开放注册 `VerifyEmail` 都在建用户的事务里用 `currentVersionsAccepted` 重读当前集合：请求必须为每种 `kind` 恰好给出一个当前版本，缺失、过期、同一 `kind` 重复或不属于当前集合都是 `ErrConsentRequired`；省略 `locale` 的旧页面按 `kind` 和 `version` 唯一解析到当前文档。通过后 `insertLegalAcceptances` 与用户、账户、工作区同事务写入，重复接受由唯一键 `DO NOTHING`；数据库触发器让旧页面的省略 locale 也记录文档实际语言。`ValidSession` 在每次认证的同一条查询里附加 `consent_pending`，只要每种 `kind` 有一个当前版本被接受就为 false；`Authenticate` 把它放进 `Principal`；控制台守卫在授权之后、待验证会话除外，遇到未接受时只放行 `getSession`、`acceptLegalDocuments` 和 `signOut`，其余返回 `ErrConsentRequired`（403 `consent_required`）。`PendingLegalDocuments` 每个未满足的 `kind` 返回最新的一个当前版本文档，页面从完整当前列表按界面语言选择并提交实际显示的版本。`AcceptLegalDocuments` 在账户范围重做同一校验，接受行只插不改。管理历史游标兼容旧节点：新节点接受 `kind:version` 和过渡的 `kind:locale:version`，但只发出旧格式，直到清理迁移完成后切到新格式。首份文档只能在全部服务节点和页面支持同意后发布；发布后不把流量切回不认识该机制的旧版本，旧节点不检查 `legal_documents`，混合部署中发布会让结果取决于负载均衡（[上线顺序](../../docs/ops/release.md#注册同意上线前核对)）。
- **认证因素的一条规则**：`users.factor_version` 是密码、TOTP、恢复码这一组的版本。签发会话、消费一次性因素、修改因素的事务，先锁用户行 `SELECT … FROM users WHERE id = $u FOR UPDATE`（邮件重置先锁请求行，再锁用户；所有涉及请求的路径同序），在锁内核对用户启用和版本；修改因素的事务在同一把锁内把版本加一。会话有效 = 未撤销、未过期、未超空闲、用户启用、版本等于用户当前版本，每个请求读会话时同一条查询一起核对；恢复码只在版本相等且未用过时可用。
- **密码登录**：argon2id，参数写进 PHC 串（M1 取 OWASP 的基线：19 MiB、2 遍、1 线程；以后提高参数，旧哈希照样能验）；密码 12 到 256 个字符；邮箱不存在也算一次假哈希。先在事务外读出哈希和版本并计算，再开签发事务，锁内版本没变且用户启用才签发。
- **两步验证**：开了 TOTP 的用户密码通过后先拿到 `pending_mfa` 会话（5 分钟，只能调两步验证接口）。验证事务：锁用户行 → 核对待验证会话未撤销、未过期、版本相等、尝试次数未满 → 验码（TOTP 做 `totp_last_step < $step` 的条件更新，恢复码做条件消费，影响行数都必须为 1）→ 码不对只把尝试次数加一并提交（每个待验证会话的次数是站点的 `challenge_code_attempts`） → 码对就条件撤销待验证会话，签发新令牌、新 CSRF 令牌的 `mfa` 会话。用户行锁让一个挑战只换出一个会话。
- **修改认证因素**：开启 TOTP 要重新输入密码并用新密钥的一个码确认，只对还没开启的用户：已开启的在用户行锁内拒绝（`ErrTOTPOn`），版本、密钥、恢复码、会话都不变；已有通行密钥的用户还必须在本次会话证明过通行密钥（`mfa`），从密码或管理后台会话开启会被拒绝，避免只拿到密码的人新增一个管理后台能完成的第二因素；重建恢复码要一个当前的 TOTP 码，错码计入每用户的失败次数；更换 TOTP 走 `ReplaceTOTP`，要求非待验证的控制台 `mfa` 会话、当前 TOTP 码或未用恢复码，还要新密钥的一个码，因素未开启返回 `ErrTOTPOff`。三者都走下面「因素变更的收尾」：加版本、重发恢复码、为当前设备签发新的 `mfa` 会话，并在同一事务里记审计 `user.totp_enable`、`user.recovery_codes_regenerate`、`user.totp_replace`（操作者和对象都是这个用户，带站点和个人账户，不记密钥和恢复码）：审计写不进，因素、恢复码和新会话一起回滚。当前因素码在换密钥的同一事务里消费，失败时恢复码仍可用。TOTP 密钥用 `secret.Keyring.Seal` 封装，`Location` 是 `{accounts.users, 用户 ID 的标准 UUID 写法, totp}`，`Sealed` 的四个字段存进 `users` 的四个 `totp_` 列；触发器 `fence_envelope_key_version` 拒绝写入已退役的 `totp_key_version`（见[平台的凭证加密](../platform/README.md#凭证加密)）；`BeginTOTP` 只生成密钥和 `otpauth` 链接，不存库，确认和更换时客户端把密钥带回。TOTP 按 RFC 6238（HMAC-SHA1、30 秒、6 位），接受前后各一个步长，同一步长只用一次。恢复码 10 个、各 80 位，写成 `xxxx-xxxx-xxxx-xxxx`，不分大小写、忽略连字符，只存 SHA-256。
- **因素变更的收尾**：`finishFactorChange` 是唯一把 factor_version 加一、按当前因素重发恢复码并签发会话的代码；开启、更换 TOTP，重建恢复码，改密码，增删通行密钥，绑定、解绑第三方身份都调用它，锁和资格检查留在各自的入口。版本加一后旧会话、旧恢复码和半完成登录一起失效，当前设备拿到原等级的新会话；用户开着 TOTP 时新恢复码在同一事务里写入（10 个，只显示一次），没开时不发，只有通行密钥的用户不会拿到一组永远用不上的码。绑定第三方身份的答案是重定向，码不能放进响应：它们用会话令牌派生的密钥做 AES-256-GCM 加密，存在 `accounts.sessions` 的 `recovery_codes_ciphertext`、`recovery_codes_nonce` 两列（迁移 00027 给 `axiom_app` 这两列的 `GRANT UPDATE`，约束要求两列同空或同有）。会话令牌只在浏览器手里，库里只有它的哈希，所以只有持有该令牌的浏览器能解开；密钥环不持有这些码，轮换没有东西要重包裹，退役也看不到它们，旧节点启动时不会发现不认识的 `_wrapped_dek` 列。撤销会话的数据库触发器清空两列，旧节点只写 `revoked_at` 的撤销同样触发。控制台的 `POST /api/console/v1/auth/recovery-codes/take` 在账户范围、同一事务里先 `SELECT … FOR UPDATE` 读出再清空，第一次调用拿到，之后拿到空列表；接口要求已登录控制台会话与 CSRF，令牌解不开的行被拒绝而不是展示。密码重置没有会话可给，只加版本不发码；改密码的响应直接带码，不走这条通道。
- **Cookie 与 CSRF**：`__Host-axiom_session`（HttpOnly、Secure、SameSite=Lax、Path=/），库里只存令牌的 SHA-256。所有会改数据的请求，包括登录、注册、两步验证：`Origin` 必须完全等于该站点该监听面登记的 `origin`，缺失或 `null` 拒绝；`Content-Type: application/json`，接口不开放 CORS；有会话的再核对 `X-CSRF-Token`。登记的源是 `http://localhost` 时 Cookie 才不带 `__Host-` 和 `Secure`，由登记决定、不看请求头。认证相关的响应 `Cache-Control: no-store`。
- **参数**：默认值只在代码里。按站点的（会话时长：控制台空闲 7 天、最长 30 天，管理后台 30 分钟、12 小时；下表的尝试上限；新 key 的每日上限；模型价格是否对未登录访客公开，默认公开；还有旧节点时设置接口拒绝关闭、匿名投影继续公开，旧节点退出后生效）存在 `site_settings`，经 `GET/PUT /api/admin/v1/sites/{site_id}/settings` 读改，要敏感权限并记审计；按节点的资源上限用 `AXIOM_*` 环境变量。`SetSiteSettings` 整体替换站点的值，但旧页面不带 `model_prices_public` 或 `billing_preference` 时在站点锁内保留读到的原值，免得升级期间保存别的设置把它清掉。站点默认的扣费顺序存在 `site_billing_preferences`，随同一个设置接口读写；它单独一张表，是因为账户在准入时要读这个默认值，而 `site_settings` 还有账户不该看到的会话和限额参数。账户自己的选择存在 `accounts.billing_preference`，控制台设置页经 `GET/PUT /api/console/v1/billing-preference` 读写，写审计 `user.billing_preference`；账户没选时读站点默认，站点没设时读 `subscription_first`。滚动升级期间旧节点不读偏好：`platform.nodes.supports_billing_preference` 由新节点心跳置真，`platform.billing_preference_readiness` 汇总所有存活节点；有一个旧节点时准入用默认值，设置接口也拒绝非默认值，旧节点退出后由下一次心跳自动开放。

### 第三方登录与通行密钥

第三方登录的 provider 端点、scope、PKCE 能力和用户信息字段在 `oauth_provider.go` 的 `oauthProviders`；GitHub 只取数字 `id`，Google 取 `sub`，LinuxDO 取 `id`，`display_name` 只作展示。`StartOAuth` 先确认 Host 是站点的登录域名、provider 已配置并取一次 IP 预算，再生成 S256 PKCE、32 字节 state（256 位）和登录流程专用的 32 字节 nonce，把 state 与 nonce 的 SHA-256、verifier、provider、回调和（绑定时）会话/用户写进 `oauth_flows`，nonce 同时放进 `__Host-axiom_oauth`（HttpOnly、Lax、10 分钟），返回 provider 的授权地址。`CompleteOAuth` 在登录域名上重建站点、核对回调 Cookie 里的 nonce（登录流程必须有且相等），再消费 state（单次、未过期、provider 一致），用存下的回调地址换 token，`Authorization: Bearer` 读 userinfo；只有已绑定的 subject 才签发会话，未知 subject 返回 `ErrAccountNotLinked`。绑定流程要求发起时是已验证的登录会话，回调必须带回同一会话和用户；成功后插入 `user_identities`，走「因素变更的收尾」（提升 factor_version、TOTP 开着时重发恢复码、签发新会话），写 `user.identity_link` 审计。解绑删除身份，同样走收尾并写 `user.identity_unlink`；绑定流程的恢复码用会话令牌派生的密钥加密后存在新会话行上，由控制台取一次。

通行密钥用 `github.com/go-webauthn/webauthn` 做协议层。`passkeySite` 从快照取登录域名和它的登记 origin 作为 RP ID/RPOrigins，且要求站点开启 `passkeys_enabled`；注册用 `BeginRegistration`（排除已有凭据、要求 UV 和 resident key），登录用 `BeginDiscoverableLogin`（不先问邮箱），第二因素用 `BeginLogin`（只允许该用户的凭据）。三者都把 challenge、RP ID/origin 和流程写进 `webauthn_flows`，finish 时单次消费并核对 kind、站点、会话、用户和账户；没有行或已消费都返回 `ErrPasskeyFailed`。`FinishPasskeyRegistration` 要求当前密码、已验证会话、名称合法且未超过 10 个；验证后在一个站点锁 + 用户锁的事务里重读当前开关和登录域名，与仪式登记的 RP ID/origin 不一致就拒绝，再存公钥、走「因素变更的收尾」（升高版本、TOTP 开着时返回新恢复码、签发 `mfa` 会话）并写 `user.passkey_add`。`DeletePasskey` 同样要求密码和已验证会话，删除后走同一收尾并写 `user.passkey_remove`；两个密码核对都走用户的失败桶。`usePasskey` 拒绝 `CloneWarning`，并用条件更新只接受严格向前的计数器（0 只在库中也是 0 时接受），同一计数器的并发断言只有一个能写入。数据库触发器拒绝为有通行密钥的用户写入控制台 `password` 会话，也拒绝未经通行密钥证明的新 TOTP（新节点在锁内证明后设置事务级标记）；迁移会吊销这类已有会话，滚动发布中旧节点既不能用密码、也不能用后台自建 TOTP 绕过。登录和第二因素失败都返回 `ErrPasskeyFailed`。独立登录的有效流程验签失败记 `security.sign_in_failed`（bad_passkey）并计入站点失败数；第二因素的验签失败记 `security.mfa_failed`（bad_passkey）、计入站点失败数，并花掉一次待验证会话的尝试预算。两者都先解析请求体再动一次性流程：随机或过期的流程、解析失败的请求体只拒绝，不写审计也不计数。第二因素的 Begin 和 Finish 先扣来源 IP 预算；会话的 `sessions.mfa_attempts` 检查、流程写入或消费、以及 Finish 的条件占次在同一事务、同一用户锁下完成，失败一起回滚。会话次数耗尽返回需要重新登录的 `ErrMFAExhausted`（HTTP 401，不是可等待的限流），不建流程、不消费流程、不验签；来源 IP 限流仍返回带有效 `RetryAfter` 的 `ErrTooManyAttempts`。

### 会话设备与撤销

`Sessions` 读 p 的实时控制台会话，对应 `ListSessions` 和迁移 00020 的 `sessions_live_of_user` 索引：按创建时间倒序最多 100 条，套用与 `ValidSession` 相同的未撤销、未过期、未超空闲、用户启用和因素版本条件，并排除 `pending_mfa`（它还不是已登录设备）。返回 `SessionDevice`：`ID`、`Current`、`CreatedAt`、`LastSeenAt`、`IPGroup` 和 `Client`。`IPGroup` 用 `clientip.Group`，即 IPv4 原样、IPv6 取 /64，和登录预算同一分组；`Client` 由 `clientSummary` 只认固定记号，给出“Chrome on macOS”这类摘要，原始 User-Agent 不返回页面。

`RevokeSession` 和 `RevokeOtherSessions` 只动 p 自己、同站点、控制台的会话，套用列表的有效性条件，并始终排除当前会话：单条不存在、已结束、别人的、别的监听面或当前会话都返回 `ErrNotFound`（HTTP 404），批量返回实际结束数。撤销只写 `revoked_at`，不改因素版本；批量把 `pending_mfa` 也一起结束，因为拿着密码但没过第二因素的登录不应在用户选择退出其他设备后继续。两者都在写 `revoked_at` 的同一事务里记审计：`session.revoke` 以会话为对象，`session.revoke_others` 以用户为对象并在 detail 里记 `revoked`；没有实际结束时批量不写审计。失败、越权和审计拒绝都整体回滚。测试见 `devices_test.go` 和控制台的 `console_session_test.go`。

### 邮件找回和改密码

业务流程和取舍见[登录与会话](../../docs/domains/accounts.md#登录与会话)。`RegisterPasswordRecovery` 在队列启动前注册 `accounts.password_reset`（notifications 队列，最多 5 次），`Config.Recovery` 把同一对象交给 HTTP 服务；`RegisterRegistration` 注册 `accounts.registration`（notifications 队列）和每分钟的 `accounts.registration_cleanup`（archive 队列），`Config.Registration` 交给 HTTP 服务。SMTP 未配置时不注册两个签发 worker，避免取走其他节点能处理的任务，清理任务仍注册；SMTP 未配置或没有接入时服务返回 `ErrBusy`，HTTP 映射 503。队列只携带请求 UUID，无个人信息或凭证。工作期限 15 秒；错误日志只记 job_id、attempt、类别和 SQLSTATE，原始错误及 detail 不进日志。交给 River 的错误仍为固定文字。没有 HTTPS 域名或历史邮箱不合法时提交清空邮箱并取消任务，临时故障由 River 重试、最终失败由现有告警暴露。

迁移 00008：`password_reset_requests` 属站点范围，主键 `(site_id, email_hash)`，摘要为规范化邮箱的 SHA-256；`id` 是每次请求的新 UUIDv7，`email` 在处理后清空，`domain` 是请求时登记的控制台域名，`language` 为 zh/en，`expires_at` 为请求提交起 30 分钟。每个邮箱无条件 upsert 再入队，调用方路径不读用户。请求记录复用，所以没有终态策略；它的新 ID 同时阻止旧链接和旧任务生效。原始待处理邮箱只在 RLS 请求表，邮件正文只在 RLS outbox；`accounts.password_reset_cleanup` 在 archive 队列每分钟清理到期请求、摘要和令牌，每次每表最多 1000 行，15 秒超时。RLS 的 DELETE 策略仅允许删除范围内已过期行，保护有效凭证，过期无需保留来防重放；SMTP 关闭时仍注册清理任务。清理失败重试并留下分类日志。

`password_reset_tokens` 属个人账户范围，复合外键 `(site_id, account_id, user_id)` 指用户；`id` 对应签发请求编号，唯一 `token_hash` 是 `randomToken` 生成的 256 位随机串的 SHA-256；只可更新 `used_at`，已消费的行策略不再允许更新。后台通过 `db.TenantTx` 和 `password_reset_request_tenant(id)` 定位请求的站点范围；登录前函数 `password_reset_tenant(domain, token_hash)` 由专用 `axiom_lookup` 持有，固定 search_path、撤销 PUBLIC 的执行权，只交出租户与令牌 ID，不判最终可用性。所有表强制 RLS，只有这两张新表授予受过期策略约束的 DELETE。

后台与完成重置都按「请求行 → 用户行 → 令牌」加锁；重发只锁请求行，其他因素变化只锁用户行，避免锁顺序反转。后台先锁最新请求，检查未过期且尚未处理，查询本站当前启用的 HTTPS 控制台域名，优先本次请求登记的域名，停用后才回退，再锁用户，按当前因素版本签发令牌；令牌、outbox、请求邮箱清除同事务。未知或停用用户只清除请求邮箱。事务提交后任务重试看到邮箱为空，直接结束。重置先在无范围事务里按秘密定位，再在站点范围内重锁请求与用户；条件消费要同时匹配令牌哈希、用户、因素版本、未用及库的实际时钟未到期，影响行数必须为 1。使用 `clock_timestamp()` 而不是事务开始时间，避免锁等待跨过期限后仍可消费。

`RequestPasswordReset` 接收邮箱、语言、客户端 IP；`ResetPassword` 接收本站 host、令牌与新密码；`ChangePassword` 接收 Principal 和当前/新密码。新密码均复用 `passwordAllowed`、`hashPassword` 和并发哈希槽；慢哈希在锁外。改密码需要非 pending 控制台会话，TOTP 开启时须已验证 MFA；当前密码错误计用户失败桶。改密码走「因素变更的收尾」，重置在写完密码后直接 `BumpFactorVersion`（没有可签发的会话），两者都写 `user.password_set` 审计，并经 outbox 必发不可关闭的 `passwordMail` 安全邮件，任一步失败整体回滚。重置不签发会话；改密码签发原等级的新会话，新 CSRF 和 MFA 用户补发的恢复码在响应中均为可选字段；`insertRecoveryCodes` 与因素变更共用。TOTP 保持，旧会话、恢复码和重置令牌因版本变化失效；邮件重置通知提醒登录后重新生成恢复码。历史无效邮箱返回 `ErrBadEmail`（422）并回滚，注册和找回使用相同的可解析邮箱边界校验。

找回邮箱计数独立使用 `reset_email` 键及本地 `guards.resets`（复用可配置的 `email_sign_in_failures`，默认 15 分钟 10 次），不返还；IP 共用登录计数。共享键仍只含 HMAC、故障按节点分摊，算法复用 `takeAttempt`。测试见 `password_reset_test.go`、`password_change_test.go` 和控制台的 `console_password_test.go`：真实数据库、受控过期、显式后台推进，不用耗时阈值判断安全。

### 登录前的资源

| 登录前的资源 | 默认上限（配了 Valkey 时各节点共享计数，不可用时退回进程内、按核心节点数等分；没配时进程内计数，只用于单核心） | 站点设置 |
|---|---|---|
| 客户端 IP | 只认 `RemoteAddr`；`AXIOM_TRUSTED_PROXIES` 里的代理才读 `X-Forwarded-For`，取最右一个不受信的地址；默认不信任任何代理 | — |
| 每 IP 的登录、注册、两步验证、找回密码 | 令牌桶，突发 20 次、每分钟 10 次，按（站点, IP）计 | `ip_attempt_burst`、`ip_attempts_per_minute` |
| 每邮箱域名的开放注册 | 令牌桶，突发 5 次、每小时 30 次，按（站点, 小写域名）计；域名的子域名各自计数 | `registration_domain_burst`、`registration_domain_per_hour` |
| 每邮箱的登录失败、找回请求（各自计数） | 每 15 分钟 10 次，按（站点, 规范化邮箱）计，不存在的邮箱也计；超出后等待，不锁账户 | `email_sign_in_failures` |
| 每用户的 TOTP、恢复码失败 | 每小时 10 次，跨待验证会话累计，重建恢复码和更换 TOTP 的错码也计 | `user_code_failures` |
| 每个待验证会话 | 5 次，计在 `sessions.mfa_attempts` | `challenge_code_attempts` |
| argon2id 并发 | `GOMAXPROCS` 个，排队超过 2 秒返回 `ErrBusy`（503）；请求在排队时结束就带它自己的错误返回 | 按节点：`Config.HashConcurrency`、`HashWait` |
| key 未命中查库 | 每 IP 令牌桶加全局并发 32，超出返回 503 和 `Retry-After` | — |
| 计数器本身 | Valkey 里带到期时间的键，装满的桶没有键；进程内（单核心模式和兜底）是有容量上限的 LRU。argon2id 名额和 key 未命中查库的令牌桶保护的是本节点，始终按节点 | — |

- 超限返回 `*TooManyAttempts`（`errors.Is(err, ErrTooManyAttempts)` 仍成立），带 `RetryAfter`：桶再放过一次尝试的时间，控制台接口据此写 `Retry-After`（至少 1 秒）。开启 TOTP 也算：错的验证码或密码用掉用户的验证码失败次数（与登录的验证共用），"已经开启"这类没在猜什么的拒绝不算。
- 三种计数都是令牌桶：每段时间 N 次，即每过「段时间 ÷ N」回一次。桶只存"再次装满的时刻"（GCRA），装满的桶等于没有，到时过期。次数用完返回 `ErrTooManyAttempts`（429），等待即恢复。
- **多节点**（M2）：配了 Valkey 时，桶在 Valkey 里，由 `take`、`give` 两个脚本按同样的算法原子地扣和退，键里的邮箱和 IP 只以 HMAC 出现（从主密钥派生的专用密钥；IP 先经 `clientip.Group` 归组——`Unmap`、去 zone、IPv6 取 /64，与网关的按 IP 限流同一份，进程内的桶同样按此计）；建任何登录键之前先过进程内的粗 IP 桶；`give` 只退到 `take` 扣的那一边；Valkey 不可用时退回进程内的桶，突发除以节点数 N（至少 1）、间隔乘 N，不拒绝登录。脚本、键、兜底和恢复见 [platform/kv 的设计](../platform/kv/README.md)。
- 失败次数**先扣后退**：尝试之前就扣一次，成功才退回，所以同时到达的尝试也超不过上限；扣不到的尝试不再读取凭证或计算哈希；已限流拒绝不再写安全审计或失败计数。IP 的次数不退。
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
| 同一账户的并发准入合并为一个事务，逐调用返回拒绝 | 每个调用仍各自判定 key、工作区、账户和域名，`Admission` 不变；省掉每个调用的 BEGIN/COMMIT，并把准入查询流水线化。业务拒绝不写行，所以不中止同批其他调用；数据库错误让整批回滚，网关按请求 ID 重试。校验拒绝混合账户或零调用者，避免一个批次跨租户 | 每调用一个事务：热点账户的提交和锁等待按调用累加；在 `fn` 里逐个 `Admit`：多一次往返和连接占用 |
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
| 站点 logo 的类型、尺寸和字节数在入库前受约束 | `site_logos` 的 CHECK；解析器只接受 SVG、PNG、WebP，PNG 解码头部核对尺寸，WebP 先限制尺寸，再完整解码校验，SVG 重建后的字节不超过 256 KiB；测试覆盖脚本、事件属性、外部引用、DTD、超限尺寸和不可读文件 |
| 当前 logo 一定属于本站点，且旧地址不变 | `(id, logo_hash) → site_logos(site_id, hash)` 复合外键；地址里的 SHA-256 就是内容的哈希，行只插入不更新；测试换图后旧 hash 仍读出原字节 |
| 认证因素的修改都有审计 | 审计在修改的事务里写；测试：两种修改各一条，数据库拒绝审计时开启失败，因素、恢复码、会话都没有留下 |
| 改了认证因素，旧会话、旧恢复码、进行中的登录都失效 | `factor_version` 相等才有效，签发在用户行锁内核对；测试两种先后：重置先提交，之后用旧密码算出的结果签发失败；签发先持锁时重置等待，签发成功，重置提交后刚签发的会话认证失败 |
| 快照不决定能否访问 | 站点、域名的状态和注册策略在库里判定；测试：只停用域名，另一个 `Service` 的准入、会话、登录、注册立即被拒；重载持续失败 90 秒后全部 503 |
| 站点改短会话时长，已签发的会话也按新的算，之后放宽也不延长 | `SetSiteSettings` 在同一事务里把新时长写进已有会话（`CutSessions`，只取小）；测试：改短后空闲过久的、年龄过大的会话下一个请求被拒，没超的不受影响（`TestShorterSessionsEndOnTheNextRequest`）；改短、期间没有请求、再放宽，会话仍失效（`TestALoosenedSiteDoesNotRevive`） |
| 站点设置的替换与审计同成同败，改前的值就是被替换的值 | 站点行锁内读改前、写、记审计；测试：越界的替换既不改设置也不留审计，替换两次有两条，条目有改前改后 |

## 管理接口

管理后台（`api/admin.yaml`）对站点、用户和账户做的读写。函数都收调用方的事务，事务要在平台范围（管理接口的守卫授权后给出），审计在同一个事务里写（[平台的「审计日志」](../platform/README.md#审计日志)）。

| 函数 | 做什么 |
|---|---|
| `ReadSystemSettings`、`SetSystemSettings` | 站点名称与注册方式；`SetSystemSettings` 在请求省略注册方式时保留站点当前值（含 open），给出时只接受 invite/closed。站点行锁内读前值、修改和审计，写后由组装层刷新快照；管理接口响应带 `name_editable`，页面据此决定能否只改名称。后台客服/运维的新增权限通过迁移 00017 的 additional_permissions 保持旧节点兼容 |
| `ReadSiteBrand`、`SetSiteBrand` | 站点品牌色，空表示默认配色；只接受 `#rrggbb`（大写转小写），否则 `*SettingError{brand_color}`，站点不存在是 `ErrNotFound`。写入在站点行锁内读旧值、更新 sites.brand_color、审计 `site_brand.update`；写后由组装层刷新快照 |
| `SiteSettings`、`SetSiteSettings` | 读、整体替换站点自己的会话时长、新 key 的每日上限和默认扣费顺序；没设的列是空，用代码里的默认（`DefaultSiteSettings`）。越界是 `*SettingError`（名字是接口里的字段名），站点不存在是 `ErrNotFound`。替换在站点行锁里做，审计条目 `site_settings.update` 记改前和改后的值，改前就是被替换的值 |
| `BillingPreference`、`UpdateBillingPreference` | 读账户生效的扣费顺序（账户自己的、否则站点默认、否则 `subscription_first`）、是否自己选的、站点默认；更新只改账户自己的列，`nil` 清除。账户来自会话，不来自请求；审计 `user.billing_preference`。`GetBillingPreference`/`SetBillingPreference` 是同一事务里的读写，供准入测试和管理接口复用；`BillingPreferenceReady` 读上一条的滚动发布门槛 |
| `SiteLoginLimits`、`SetSiteLoginLimits` | 独立 `/login-limits` 读写现有五项阈值，空值选择 `DefaultLoginLimits`；站点锁内读旧值、替换、审计，与其他站点设置互不覆盖；写后刷新，其余节点 30 秒内重载 |
| `LegalDocumentHistory`、`PublishLegalDocument` | 按 `(kind, version)` 游标读一页法律文档版本（最新在前，页大小由调用方给，最多 200 行），并用 `more` 告诉调用方还有下一页；发布在站点行锁内取该类型的下一个版本并插入，正文只插不改。`PublishLegalDocument` 的正文 1 到 65536 字符，审计 `site_legal_document.publish` 只记类型、版本、字符数和 SHA-256 |
| `CurrentSiteLogo`、`SetSiteLogo` | 读当前 logo（没有时 nil）；`SetSiteLogo` 在站点行锁内解析并校验、插入 `site_logos`、更新 `sites.logo_hash`，`data` 为 nil 表示清除；`*SettingError` 是文件被拒，`ErrNotFound` 是站点不存在。审计 `site_logo.update` 只记 hash、类型、尺寸和字节数。写后由组装层刷新快照 |
| `SiteLogoByHash` | 公开读：按站点和 hash 取一行，当前的和旧的都能取到；hash 已由 URL 校验为 32 字节，读在平台范围，不依赖快照的当前指针 |
| `SetAccountSuspended`、`SetUserSignInDisabled` | 分开控制调用和登录，前者复用准入同一条查询里的账户状态，后者在用户锁内吊销全部会话，恢复均不修改因素。暂停/解除写 `account.status_change` 审计，并持久化 `accounts.account_suspended` 信号（暂停 critical，解除时恢复）；拒付用它暂停，操作员在后台解除 |
| `SetAccountTier` | 在账户行锁内把 `tier_id` 改成账户所在站点的另一个等级；同等级是空操作，不写审计；目标等级不属于该站点是 `*SettingError{tier_id}`，账户不存在是 `ErrNotFound`。审计 `account.tier_change` 记改前和改后的等级名 |
| `UserPlatformGrants`、`SetPlatformGrantRevoked` | 列表各角色最后状态，撤销终态不回退；恢复要求历史授予且权限为操作者当前平台角色权限的子集，插入新行并记录 granted_by；按用户锁串行 |
| `Refresh` | 立即重载快照。改了站点设置的节点调用它；别的节点在重载间隔内（30 秒）跟上 |
| `Site`、`MainSite` | 快照里的站点；管理接口用它取币种和默认站点 |
| `Tiers` | 站点的等级，按名字 |
| `Users` | 用户列表，新的在前，键集分页（上一页最后一个用户的 ID），可按站点、按邮箱前缀筛。前缀按字面匹配（`%`、`_` 不是通配符）、不分大小写，用 `users_email_prefix`（`text_pattern_ops`）的索引。创建时间取自 UUIDv7 的时间戳（ID 的顺序就是创建的顺序，没有 `created_at` 列） |
| `Account` | 账户、等级、主人；账户没有成员时是 `ErrNotFound`（账户总有主人） |

- **站点 logo 的解析与发送**：`logo.go` 的 `ParseLogo` 是唯一的解析入口，返回重建后的字节、类型、宽高和 SHA-256；`SetSiteLogo` 只存这个结果。PNG 用标准库的 `png.DecodeConfig` 读头；WebP 先用 `golang.org/x/image/webp` 的 `DecodeConfig` 限制画布尺寸，再完整解码，SVG 用 `encoding/xml` 逐个 token 重建：允许的元素和属性各是一张表，值里的 `href` 只能是本文件的 `#id`，`fill`/`stroke` 等只接受颜色或本文件的 `url(#id)`，`style`、事件属性和外部命名空间全部丢弃，DTD 直接拒绝。HTTP 层只按 `ContentType` 选生成的响应类型，并设置 `Cache-Control: public, max-age=31536000, immutable`；`Content-Type` 和 `Content-Length` 由生成代码写，`X-Content-Type-Options: nosniff` 由前端中间件统一加。
- **权限**：管理接口权限（含 `calls.search`、`calls.inspect`、`audit.view`）（均在 `authorize.go` 声明，迁移 `00006` 给内置角色）：`users.view`、`suppliers.view`、`catalog.view`、`pricing.view`（非敏感）；`credits.grant`、`suppliers.manage`、`catalog.manage`、`pricing.manage`（敏感，要做过两步验证的会话）。内置角色（迁移后）：

  | 角色 | 权限 |
  |---|---|
  | owner | `api_keys.view`、`api_keys.manage`、`workspaces.manage`、`calls.view`、`balance.view`、`balance.top_up` |
  | admin | owner 的全部，加 `sites.manage`、`platform_grants.manage`、`users.manage` 和管理接口权限（含 `calls.search`、`calls.inspect`、`audit.view`） |
  | member | `api_keys.view`、`api_keys.manage`、`calls.view` |
  | finance | `api_keys.view`、`balance.view`、`balance.top_up`、`users.view` |
  | viewer | `api_keys.view`、`calls.view`、`balance.view`、`users.view`、`suppliers.view`、`catalog.view`、`pricing.view` |
  | support | `api_keys.view`、`calls.view`、`users.view`、`calls.search`（仅后台平台授予） |

  只有 admin 能赠送/调整余额、改凭证、价格和目录；owner、admin、finance 能用自己的资金充值，其余角色最多看。谁该有 `credits.grant` 这类权限是负责人的决定，现在只给 admin。
- **管理会话的权限是平台授予的**：管理监听面的会话，`SessionInfo` 的权限列表取用户未撤销的平台授予的角色，不是账户里的成员关系；`Authorize` 对平台目标本来如此。
- **站点缩短会话时长，已有的会话也缩短**：`SetSiteSettings` 在改设置的同一事务里，把站点新的空闲时长、最长时长（没设的取代码默认值）写进该站点、该监听面未撤销的会话：`expires_at` 与 `created_at` 加新最长时长取小，`idle_timeout` 与新空闲时长取小（`CutSessions`）。所以改短立即对所有节点生效，并且存进了会话本身：之后站点再放宽，被改短的会话也不会恢复，哪怕改短期间它没有发过请求。只会缩短，不会延长已签发的会话。`ValidSession` 和 `SessionInfo` 另把存下的时长与快照里站点现在的值取小，挡住别的节点在重载快照前按旧时长新签发的会话。应用因此对 `sessions` 的 `expires_at`、`idle_timeout` 有更新权限（迁移 00006），只经 `CutSessions` 往小改。

## 公开接口（草图）

```go
func NewService(db *pg.DB, cfg Config) (*Service, error) // cfg: 平台范围、哈希密钥、时钟，以及共享的缓存与登录键（secret 从主密钥快照派生）；New 是 sqlc 生成的
func (s *Service) Load(ctx context.Context, logger *slog.Logger) error // 站点、域名、设置、角色的快照；核对权限名；启动重载循环
func (s *Service) SiteForHost(host string) (Site, Surface, error) // Host 原样（小写、非默认端口带端口）；ErrUnknownHost、ErrStale
func (s *Service) Origin(host string) (string, error) // 该域名登记的 origin（含协议和端口），服务端拼链接时用，不用请求头里的 Origin；错误同上
func (s *Service) SiteLogoByHash(ctx context.Context, site SiteID, hash [32]byte) (*Logo, error) // 公开读取，当前或旧的 hash；ErrNotFound
func (s *Service) CreateKey(ctx context.Context, tx pg.Tx, actor audit.Actor, r KeyRequest) (CreatedKey, error)
func (s *Service) DeleteKey(ctx context.Context, tx pg.Tx, actor audit.Actor, key KeyID) error
func (s *Service) AuthenticateKey(ctx context.Context, site SiteID, raw string) (Caller, error) // 只认出，不判定能否使用
func (s *Service) GetKeyInfo(ctx context.Context, tx pg.Tx, key KeyID) (KeyInfo, error)      // 名称、标签（前缀...末四位）、过期时间，供 GET /key；别的账户的 key 是 ErrKeyNotFound
func (s *Service) AccountWorkspaces(ctx context.Context, tx pg.Tx, account AccountID) ([]uuid.UUID, error) // 账户全部工作区的 ID（含已删除），供 billing 汇总累计花费
func (s *Service) GatesLookups() bool                    // Config.LookupSlot 是否配了；gateway 的 NewHandler 要求配了
func (s *Service) Admit(ctx context.Context, c Caller, r AdmitRequest,
	fn func(tx pg.Tx, a Admission) error) error              // fn 由 gateway 组装：定规则、路由、billing 的准入；只收调用 key
func (s *Service) AdmitRead(ctx context.Context, c Caller, r AdmitRequest,
	fn func(tx pg.Tx, a Admission) error) error              // 同上，收调用 key 和管理 key，供 GET /key、/credits
func (a Admission) Scope() pg.Scope                         // 同一调用后续事务（追加、结算、SettleOrphan）的账户范围，只能从 Admission 取得
func (s *Service) Authenticate(ctx context.Context, r *http.Request) (Principal, error) // 站点和监听面取自 Host；改数据的请求另核对 Origin、JSON、CSRF；Principal 带未接受的文档
func (s *Service) CurrentLegalDocuments(ctx context.Context, site SiteID) ([]LegalDocument, error) // 公开：每类当前版本
func (s *Service) PendingLegalDocuments(ctx context.Context, p Principal) ([]DocumentVersion, error) // 未接受的当前版本
func (s *Service) AcceptLegalDocuments(ctx context.Context, p Principal, accepted []DocumentVersion) error // 校验失败是 ErrConsentRequired；重复接受不改首次时间
func (s *Service) LegalDocumentHistory(ctx context.Context, tx pg.Tx, site SiteID, after *DocumentVersion, limit int) ([]LegalDocument, bool, error) // 管理接口：平台范围的事务，按 (kind, version) 续页
func (s *Service) PublishLegalDocument(ctx context.Context, tx pg.Tx, actor audit.Actor, site SiteID, kind, body, reason string) (LegalDocument, error) // 站点行锁内取下一个版本
func (s *Service) Authorize(ctx context.Context, p Principal, perm Permission, t Target) (pg.Scope, error)
func (s *Service) Register(ctx context.Context, r RegisterRequest) (UserID, error)
func (s *Service) SignIn(ctx context.Context, r SignInRequest) (Issued, error)    // 有第二因素的得到待验证会话
func (s *Service) StartOAuth(ctx context.Context, host, provider string, p *Principal, ip netip.Addr) (OAuthStart, error)
func (s *Service) CompleteOAuth(ctx context.Context, r *http.Request, provider, code, state string, ip netip.Addr, userAgent string) (OAuthCallback, error)
func (s *Service) UnlinkIdentity(ctx context.Context, p Principal, provider string, ip netip.Addr, userAgent string) (Issued, error)
func (s *Service) BeginPasskeyRegistration(ctx context.Context, r *http.Request, p Principal, name, password string) (PasskeyRegistration, error)
func (s *Service) FinishPasskeyRegistration(ctx context.Context, r *http.Request, p Principal, flowID string, ip netip.Addr, userAgent string) (Passkey, Issued, error)
func (s *Service) BeginPasskeySignIn(ctx context.Context, host string, ip netip.Addr) (PasskeyChallenge, error)
func (s *Service) FinishPasskeySignIn(ctx context.Context, r *http.Request, flowID string, ip netip.Addr, userAgent string) (Issued, error)
func (s *Service) BeginPasskeyMFA(ctx context.Context, r *http.Request, p Principal, ip netip.Addr) (PasskeyChallenge, error)
func (s *Service) FinishPasskeyMFA(ctx context.Context, r *http.Request, p Principal, flowID string, ip netip.Addr, userAgent string) (Issued, error)
func (s *Service) DeletePasskey(ctx context.Context, p Principal, id PasskeyID, password string, ip netip.Addr, userAgent string) (Issued, error)
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
func (p Principal) Pending() bool; func (p Principal) Verified() bool; func (p Principal) ConsentPending() bool
func (p Principal) SessionID() SessionID // 认出主体的会话，管理接口的探测限次按会话计
func (s *Service) SessionInfo(ctx context.Context, p Principal) (SessionInfo, error)  // 邮箱、邀请码、级别、CSRF、到期、按级别过滤的权限名
func (s *Service) AccountTier(ctx context.Context, p Principal) (TierID, error) // 本账户当前等级；行级安全按账户，读不到别人的行
func (s *Service) ListKeys(ctx context.Context, tx pg.Tx, account AccountID, q KeyQuery) ([]Key, bool, error) // 新的在前，按 ID 翻页；名字/前缀/状态/创建时间过滤，索引顺序读
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
| 开放注册默认关闭，待验证请求与验证令牌分离，待验证行不保存口令，域名名单在请求和点击时都检查，人机校验自托管，IP 与域名双限流 | 默认关闭让升级不改变任何站点；待验证请求没有 account_id，站点范围即可写入和清理；口令只在邮箱所有权证明后设置，避免攻击者替受害者预置口令；令牌只存哈希且随行删除；请求时挡住已拒绝的域名，点击时再挡住待验证期间新增的封禁；自托管证明不保存挑战，也不把访客数据交给第三方；域名桶挡住单一域名的集中注册，IP 桶复用了登录的共享计数和故障降级 | 第三方 captcha：外部可用性和数据边界；有状态挑战表：多一次写入和过期清理；只在点击后校验域名：会把已拒绝的域名存进数据库 |
| 法律文档按 (站点, 类型, 语言, 版本) 追加，正文只插不改，当前版本取每种类型和语言的最大版本；兼容阶段保留旧 (站点, 类型, 版本) 主键并跨语言共享版本号 | 接受记录引用版本，必须永远指向当时给人看的文本；更正只能发新版本并重新同意。旧节点仍按旧主键和旧 `ON CONFLICT` 写，先展开 locale 和新约束、旧节点退出后再清理，才能既不阻塞滚动发布，也能让一种语言以后独立编号。当前版本由前缀查询得到，不另设当前指针，也就没有指针与行不一致的第二种状态；发布在站点行锁内取下一个编号，两个并发发布不会撞号 | 原地修改正文：接受记录会指向没看过的文本；当前指针：多一处状态和并发更新，指针与最大版本可能不一致；立刻删除旧约束：旧节点写入失败、回滚撞主键 |
| 注册同意在创建用户的事务里校验并写入，已登录用户由 `ValidSession` 在同一查询里判断 | 开放注册点击链接前没有用户可挂接受记录，邀请码则在 `register` 就有；同事务保证不会留下没有接受记录的用户。会话查询里比较当前版本，新版本发布立即生效，不需要快照失效协议，也不增加一次往返 | 只在注册时检查：老用户和新版本都能绕过；只在页面检查：直接调 API 不受约束；缓存未接受状态：发布后要等失效 |
| 没有文档的站点不要求同意，文档是公开数据、接受记录是账户数据 | 机制不替站长决定何时上线条款，升级不改变未配置站点；注册页未登录就要显示文本，按公开数据读，接受记录属于用户，按账户数据受行级安全约束 | 内置条款或要求所有站点先配置：代码会写真实法律文本，升级也会让所有站点立即不可用 |
| Valkey 不可用时登录前的次数退回进程内、按节点数等分，不拒绝登录 | Valkey 只放丢了也不影响正确性的数据（[总体架构](../../docs/architecture.md#技术选型)），它出故障就挡住所有人登录，正是[安全的「账户安全」](../../docs/security.md#账户安全)要避免的「把用户锁在门外」；进程内的桶按份额限制后，N 个节点合计的回填速度不超过设定、突发不超过 max(突发, N)，代价只是每次故障或重启至多多一次突发，argon2id 名额仍按节点限制猜测速度 | 失败即拒（sub2api 认证路由在 Redis 故障时一律 429，`routes/auth.go:34`；new-api 回 500，`middleware/rate-limit.go:119`）：缓存故障变成登录故障；兜底不等分：故障期间限额放大 N 倍 |

## 用户提醒与站内公告

`notification_preferences` 是用户的账户数据，没有行时使用代码默认值，保存后各列均非 NULL；账户异常花费由 observability 的检测事务调用 `RecordSpendAnomalyNotices`，按 `spend_anomaly` 开关写入，同一账户一轮检测只写一条；省略 `spend_anomaly` 的偏好保存由 upsert 保留已存值（首次插入取默认），不会用先前读到的值覆盖并发修改；派发器只领取 `noticeKinds` 里的种类，不认识的提醒留给认识它的节点，不会被标记成已派发；`user_notices` 用 `(user_id,kind,source)` 唯一键去重。因素版本改变的 invoker trigger 与用户变更同事务，覆盖控制台、CLI 和旧节点。密码变更沿用同步入 outbox 的模板，不受因素开关影响，并把对应事件的 email_done 置真，后台只补 Webhook，不再排第二封邮件。其他因素变化由后台拼站点品牌的 zh/en 邮件。Webhook 发送不引用运维配置或运维密钥，URL 只在租户表，任务只有通知 ID；正文包括稳定 ID、kind、字符串 value（金额为十亿分之一站点额度；因素为版本）、currency、at，站点支付通知另带 detail（被改的二级站点名称）。此用户 Webhook 没有签名，接收方只能按通知 ID 去重，不能据此执行敏感操作。

组装层的周期任务按活跃状态去重，避免重试占满通知队列；每分钟先调用一次 `MaintainUserNotices`，再在每批平台事务内调用 billing 的集合 journal 消费（最多 1000 行）与 `RecordBalanceNotices`，再调用 `DispatchUserNotices`（最多 100 条）；即使 journal 为空也继续派发积压，直到没有进展或 30 秒预算不足以开始下一批，预算耗尽正常完成；多个节点使用 `FOR UPDATE SKIP LOCKED`，事件/派发标记、outbox、River 任务全成全败。邮件未配置的节点保留待派发，不影响原账本或因素写入；密码修改原有的 SMTP 就绪要求保持。邮件与 Webhook 最多五次持久尝试，Webhook 最后一次失败记 failed 后完成，异常丢弃/取消由下一轮检查持久任务收尾；通知 30 天后分批清理，排队 Webhook 受保护。SMTP 结果仍查 mail outbox、River 与日志；不能入 outbox 的历史非法邮箱记 email_failed，不悄悄略过，也不堵住整个批次。低余额是已过账钱包余额穿越，不是可用余额，也不是盗刷检测。

`RecordSitePaymentNotice` 在商户配置保存的同一事务里，为二级站点的 owner account 的活跃用户写一条 `site_payment_changed`，主站没有 owner，不写；这一条在 `noticeEnabled` 里恒为开，不受偏好影响，正文带主站品牌、被改的二级站点名称（`detail`）、标题和处置建议，不含商户号、密钥或网关地址。保存失败时它和审计一起回滚；每次改动用新来源，唯一键挡住同一用户的重复行，接口幂等键挡住重放。

`announcements` 对外使用 `AnnouncementID`（`ann_` TypeID），是站点公开数据，账户范围通过 home_site 读取本站；发布/撤下只限平台，内容不可更新，撤下为终态。控制台按服务端时间取 `[starts_at,ends_at)` 的前 100 条。管理写入经 MFA、sites.manage、幂等与审计；控制台不接受选择另一个站点。文本只渲染为 React 文本节点。
