# accounts

站点、用户、账户、成员、工作区、API key、等级、登录与会话、授权，以及租户范围的构造。行为、阈值和决定的理由见[账户与租户](../../docs/domains/accounts.md)；这里只写公开接口和代码必须守住的不变量。

## 范围

| 本模块管 | 不归本模块 |
|---|---|
| 站点、域名、key 前缀、注册策略、默认等级、站点参数 | 收款渠道（`payments`） |
| 用户、密码、TOTP、恢复码、通行密钥、第三方身份、会话、邀请关系、给用户的邮件的收件人与模板 | 邮件投递（`platform/mail`） |
| 账户、成员、工作区；等级本身和账户在哪个等级 | 余额、预留、账本、风控状态、等级加价（`billing`） |
| key 的设置：速率、花费上限、工作区预算、过期、预设 ID、可用模型、IP 白名单 | 花费计数（`billing`）、预设内容（`routing`）、限流计数（`gateway`） |
| 权限、角色、授予；构造账户和站点的租户范围 | 资金上限、审批（`operations`） |

## 数据

以基线 [`00049_baseline.sql`](migrations/00049_baseline.sql) 初始化；[迁移规则](../platform/README.md#迁移)。

### 表（schema `accounts`）

- 主键是 Go 生成的 UUIDv7；金额 `bigint`（[钱的表示](../../docs/domains/billing.md#钱的表示)）；名字 1 到 100 个字符。
- 站点数据的表有 `site_id`，账户数据的表有 `(site_id, account_id)`，都不为空、不可改；父表有 `UNIQUE (site_id, id)` 一类的复合唯一键，子表用复合外键引用，所以两列与"key → 工作区 → 账户 → 站点"一致。范围策略用 [pg 的 InitPlan 参数](../platform/pg/README.md#决策与理由)比较这两列。
- `axiom_app` 只对可改的列有 `UPDATE`；`DELETE` 只给过期的找回请求与令牌、待验证注册、待换邮箱和超过 30 天的已派发提醒。
- 结构约束：`sites` 只有一个主站，上级只能是主站，`(parent IS NULL) = (owner IS NULL)`，默认等级和 `logo_hash` 都用复合外键指向本站的行，`currency` 不可改；`users.account_id` 唯一且只能指向 personal 账户，`(site_id, email)`、`(site_id, invite_code)` 唯一，邀请人同站且不是自己；`members` 用生成列外键让个人账户只有它的用户；每个账户一个 `is_default` 工作区，未删除的工作区 `lower(name)` 唯一；`api_keys` 的管理 key 必须有 `expires_at`，`CHECK (kind = 'call' OR preset_id IS NULL)`；`platform_grants` 只能指向主站用户，每个（用户、角色）一条未撤销的授予；`site_logos`、`legal_documents`、`legal_acceptances`、`announcements` 只插入（公告只能写 `withdrawn_at`）；TOTP 和 OAuth secret 的 `secret.Sealed` 四列同空同有，触发器拒绝已退役的密钥版本。

### 行级安全的写法

所有表 `ENABLE` 并 `FORCE ROW LEVEL SECURITY`，每条命令一条策略，不用 `FOR ALL`。有终态列（`revoked_at`、`deleted_at`、`used_at`）的表：`FOR UPDATE USING (范围 AND 终态列 IS NULL) WITH CHECK (范围)`，进入终态后整行不能再改，平台范围也一样。`platform_grants` 的写入另限平台范围。迁移测试查 `pg_policies` 守住这两条。

## ID

库里是 `uuid`；Go 里每种实体一个以 `uuid.UUID` 为底层的命名类型（`SiteID`、`TierID`、`AccountID`、`WorkspaceID`、`UserID`、`KeyID`、`SessionID` 等），不能互相赋值。对外是 TypeID（前缀加 `_` 加 26 个小写 Crockford base32 字符；`site`、`tier`、`acct`、`ws`、`user`、`key`、`sess`、`ann` 前缀，编解码在 `platform/typeid`），只在 HTTP 边界用 `TypeID()`/`Parse…ID` 转换；ID 类型不实现 `TextMarshaler`。

## 授权与租户范围

本模块是生产代码里唯一构造账户和站点范围的地方（forbidigo 只放行 `internal/accounts`）；平台范围由 `cmd/axiom` 构造后交入，本模块只在授权通过后交出。

| 操作者 | 事务的范围 |
|---|---|
| 登录前 | 无范围，只能调「登录前的查找」 |
| 控制台用户 | `Authorize` 按目标账户给 `pg.AccountScopeIn(会话的站点, 账户)`，前提是他在该账户的角色含这项权限 |
| 管理后台 | 平台范围；会话在管理面、`level = mfa`，且未撤销的平台授予含这项权限 |
| 调用 key / 管理 key | `Admit` / `WithKeyManager` 打开的事务里 key 所属账户的范围 |
| 运营凭证 | `AuthorizeAgent` 给平台范围或凭证的站点范围 |

- `Caller`、`Principal`、`Admission`、`KeyManager` 的字段不导出，只有认证成功的函数能造出非零值；零值交给 `Admit` 被拒。
- `Authorize` 只返回覆盖本次目标的范围；现在只有自己的个人账户能被授权，别的账户 `ErrNotFound`，有关系但无权限 `ErrForbidden`。敏感权限要求 `level = mfa`。
- 权限的名字和是否敏感在 `authorize.go` 声明；角色快照合并 `permissions` 与 `additional_permissions` 并忽略不认识的名字，新权限只经迁移写进 `additional_permissions`（`TestRolePermissionsRollingCompatibility`）。`PermissionNamed` 供启动时核对契约的 `x-axiom-permission`。

## 运营凭证

`CreateAgent`、`RotateAgent`、`RevokeAgent`、`StopAgents`、`AgentCredentials`、`AuthenticateAgent`、`AuthorizeAgent`、`HoldAgent`、`SpendAgentRequest`/`Money`/`Alert`（[运营凭证](../../docs/domains/operations.md#运营凭证)）。

- 令牌是 `op-ax-v1-` 加 key 同样的随机串和校验位，只在创建或轮换的返回值里出现一次，库里只存带密钥的哈希。
- 权限至少有 `operations.read`；`operations.manage`、`operations.approve`、`platform_grants.manage` 被 `agentRefused` 和表的 CHECK 拒绝。
- 每个请求都读库认证，吊销、停止和过期从下一个请求生效；轮换的重叠最多 24 小时；`HoldAgent` 用 `FOR SHARE` 让吊销等提案提交。
- 限频是本节点的令牌桶，次数由调用方每次传入；任何一个窗口里放行不超过设定次数，调低后也一样（`TestAgentLimitsLoweredMidWindow`）。

## 登录前的查找

登录前事务没有范围，什么表都读不到；`SECURITY DEFINER` 函数把秘密或名字换成标识：`key_tenant`、`session_tenant`、`login_tenant`、`invite_tenant`、`domain_site`、`registration_site`、`registration_tenant`、`registration_request_tenant`、`password_reset_tenant`、`password_reset_request_tenant`、`passkey_tenant`。

- 都以请求的域名或站点为条件，域名或站点停用、不属于这个监听面时无结果，别站的和不存在的走同一条路径；只交出租户标识，配置和哈希在拿到账户后用账户范围再读。
- 函数的所有者是专用角色 `axiom_lookup`：不能登录、不拥有表，`axiom_app` 不是它的成员；表上只为它加 `FOR SELECT` 策略并只授予要读的列。`search_path` 固定为 `pg_catalog, pg_temp`，只有 `axiom_app` 能执行。`serve` 启动时检查 Axiom schema 里所有定义者函数都满足这些条件，否则不启动（[平台的「不变量」](../platform/README.md#不变量)）。

## API key

### 格式与哈希

`前缀` + 43 个 base62 字符（32 字节随机）+ 6 个 base62 校验字符（前缀加随机串的 CRC32）；格式或校验位不对直接拒绝，不算哈希、不查库。哈希是 `HMAC-SHA256(版本 v 的密钥, 整个 key)`，密钥在 `AXIOM_KEY_HASH_FILE`（同[主密钥文件](../platform/README.md#凭证加密)的要求），最多 2 个版本同时查，版本记在行上。

### 认出与准入

**缓存只存不会变的东西；能被改的每次调用都在准入事务里读库。**

1. `AuthenticateKey`（内存）：按 `(site_id, 哈希)` 查缓存，条目只有 key、账户、工作区和站点的 ID。正缓存 20 万条 LRU，负缓存 20 万条 60 秒。未命中时合并并发查找；共享的查找沿用调用方的截止时间（没设时最多 1 秒），配了 `Config.LookupSlot` 时先取一个名额。命中时不查库（指标 `axiom.accounts.key_cache.misses` 只数真去问库的查找）。
2. `Admit`（数据库）：在 key 所属账户的范围里一条查询核对域名属于 key 的站点、key/工作区/账户启用、未删除、未过期、`kind = call`、IP 在白名单内。账户停用而其他有效是 `ErrAccountSuspended`（403），其余都是与不存在相同的 `ErrUnknownKey`（401）。通过后把预设、可用模型、等级、限额、工作区预算、业务速率、扣费顺序、所有者及其协议同意交给 `fn`；`fn` 由 gateway 组装路由和 billing 的预留，只做内存计算和预留语句，满载时返回"需要等待"让 `Admit` 回滚，在事务外等后重新 `Admit`。拒绝不缓存。
3. `AdmitRead` 同一条查询也收管理 key，只给 `/key`、`/credits`；`Check` 只做判断不跑 `fn`；`AdmitBatch` 把同一账户的并发调用合并进一个事务，逐个返回拒绝，混合账户或零调用者直接报错。

停用、改预设或等级的提交确认之后开始的调用，准入查询都读到新值（Read Committed 每条语句取新快照），与缓存、节点数、确认是否送达无关。防枚举只承诺状态码和正文相同：本站真实存在的 key 多一次准入查询，缓存命中与未命中耗时不同；停用的 key 在准入前仍可能收到请求格式类的 400。

`Admission.OwnerID` 是个人账户的唯一所有者（还没有用户时为零，不影响准入）；`Admission.Consent` 是所有者到这次准入为止同意过的每种文档的最新版本，供调用记录写协议版本，不表示允许做什么。账户范围只能读自己当前指定的那个等级，读不到同站其他等级。

### 命名请求配置

`accounts.presets` 存账户的 `{slug, name, config}`（合并见[路由的「命名预设」](../../docs/domains/routing.md#命名预设)）。`SavePreset`、`GetPreset`、`ListPresets`、`DeletePreset`、`LockPresetAccount`：写在账户行锁内，每账户最多 100 个，未删除的 slug 唯一；配置按 jsonb 规范化后最多 16 KiB，只收控制台契约 `PresetConfig` 列出的成员（`TestPresetMembersKeepTheirContract`），`cache_ttl_seconds` 1 到 86400；配置不进审计；删除只置 `deleted_at`。快照重载跳过不合法的单行并告警。

### 程序化 key 管理

`WithKeyManager` 先取 `Config.LookupSlot` 名额（拿不到是 `ErrManagementBusy`，503），在独立事务里锁管理 key、验证并扣分钟次数，再在操作事务里重新锁定和验证直到回调提交；计数与操作分开提交，失败不返还。`KeyManager.Create` 只签发 call，`Get`/`Update`/`Delete` 的目标只能是未删除的 call，签发上界的比较见[领域文档](../../docs/domains/accounts.md#api-key)；管理 key 不套用新调用 key 的每日默认上限，没写上限即签发上界不限。

### 快照

站点、域名、站点设置、角色、命名配置是一份不可变值，`atomic.Pointer` 整体替换；一个后台循环每 30 秒重载，写过的节点调 `Refresh` 立即重载。**快照不决定能否访问**：站点和域名的启用、注册策略、平台授予都在库里判定，快照只用来预筛没登记的域名、匿名价格页的默认等级和参数。超过 90 秒没有成功重载，所有请求 503；启动时第一次加载失败就不启动。

## 注册、登录与会话

- `users.factor_version` 是密码、TOTP、恢复码这一组的版本。签发会话、消费一次性因素、修改因素的事务第一步锁用户行，在锁内核对用户启用和版本；会话有效 = 未撤销、未过期、未超空闲、用户启用、版本相等，每个请求在同一条查询里核对。
- `finishFactorChange` 是唯一加版本、按当前因素重发恢复码并签发会话的代码；所有因素变更都调用它，资格检查留在各自的入口。
- 一个挑战只换出一个会话：TOTP 做 `totp_last_step < $step` 的条件更新，恢复码条件消费，影响行数必须为 1。
- 站点改短会话时长时 `CutSessions` 在同一事务里把已有会话的 `expires_at`、`idle_timeout` 取小；`ValidSession` 另与快照的当前值取小。
- 每次 argon2id 计算先取并发名额（`golang.org/x/sync/semaphore`）。登录前的计数先扣后退。
- 通行密钥第二因素的 Begin 和 Finish 先扣 IP 预算；`sessions.mfa_attempts` 的检查、流程写入或消费和失败占次在同一事务、同一用户锁下完成。次数耗尽是 `ErrMFAExhausted`（401，要重新登录），不建也不消费流程；随机或过期的流程、解析失败的请求体只拒绝，不审计不计数。

### 邮件找回和改密码

锁顺序固定为请求行 → 用户行 → 令牌，重发只锁请求行，其他因素变化只锁用户行。消费用 `clock_timestamp()` 判到期，条件同时匹配令牌哈希、用户、因素版本和未使用，影响行数必须为 1。队列任务只带请求 ID；SMTP 未配置时不注册签发 worker（清理任务照常），服务返回 `ErrBusy`。

## 命令行

`Operator`（`NewOperator`）在平台范围执行，每个命令同事务记审计（操作者 `system:accounts.cli`），输出一行 JSON；密码只从标准输入读一行。

| 命令 | 做什么 |
|---|---|
| `axiom site init --name N --currency USD --console HOST --api HOST [--admin HOST] [--key-prefix P]` | 一个事务建主站（invite）、首个等级「Default」和各监听面的域名；`localhost` 登记 `http://` 的源；主站已存在是 `ErrSiteExists`，币种只能是 `USD` |
| `axiom user create --email E` | 在主站建用户，与注册共用 `createUser`，无邀请人 |
| `axiom user grant-platform --email E [--role admin]` | 授予内置角色；已持有不变 |
| `axiom user set-password --email E` | 换密码并加因素版本 |
| `axiom user reset-mfa --email E` | 清除 TOTP 并加因素版本 |

`Config.OnAccountOpened` 与 `NewOperator` 的 `OpenedFunc` 在开户事务里调用，`cmd/axiom` 接到 `billing.OpenBalance`，所以账户一出现就有余额行，回调失败整体回滚。

## 管理接口

管理处理器在 `cmd/axiom`，函数收调用方平台范围的事务，在目标行锁内读前值、写、同事务记审计，写后由组装层 `Refresh`：`SiteSettings`、`SiteLoginLimits`、`ReadSystemSettings`、`ReadSiteBrand`、`SiteKeyLimits`、`AccountKeyLimits`、`SiteWorkspaceLimit`、`AccountWorkspaceLimit`、`RateLimit`、`LegalDocumentHistory`（每页最多 200）、`CurrentSiteLogo`（各有对应的 `Set…`/`Publish…`），`SetAccountSuspended`、`SetUserSignInDisabled`、`SetAccountTier`、`UserPlatformGrants`、`SetPlatformGrantRevoked`，以及只读的 `Site`、`MainSite`、`Tiers`、`Users`（新的在前，键集分页，邮箱前缀按字面、不分大小写）、`Account`、`SiteLogoByHash`。越界是 `*SettingError`（带接口字段名），目标不存在是 `ErrNotFound`。`ParseLogo` 是 logo 唯一的解析入口。管理面会话的权限取自未撤销的平台授予。

内置角色（迁移写入，以后加权限也只用迁移改）：

| 角色 | 权限 |
|---|---|
| owner | `api_keys.view`、`api_keys.manage`、`workspaces.manage`、`calls.view`、`balance.view`、`balance.top_up` |
| admin | owner 的全部，加 `sites.manage`、`platform_grants.manage`、`users.manage`、`users.view`、`credits.grant`、`suppliers.*`、`catalog.*`、`pricing.*`、`calls.search`、`calls.inspect`、`audit.view`、`operations.manage`、`operations.approve` |
| member | `api_keys.view`、`api_keys.manage`、`calls.view` |
| finance | `api_keys.view`、`balance.view`、`balance.top_up`、`users.view` |
| viewer | `api_keys.view`、`calls.view`、`balance.view`、`users.view`、`suppliers.view`、`catalog.view`、`pricing.view` |
| support | `api_keys.view`、`calls.view`、`users.view`、`calls.search`（只经平台授予） |

运营另有 `operations.read`、`operations.write` 和敏感的 `operations.money`；敏感的 `refunds.manage` 只有运营凭证持有，不在任何内置角色里。

## 公开接口

签名见 godoc；`cmd/axiom` 的处理器只经这些函数，`Principal` 和范围都由本模块签发。

- 启动与站点：`NewService`、`Load`、`Refresh`、`SiteForHost`、`Origin`、`GatesLookups`。
- 调用路径：`AuthenticateKey`、`Admit`、`AdmitRead`、`AdmitBatch`、`Check`、`Admission.Scope`（同一调用后续事务的范围只能从这里取）。
- 会话：`Authenticate`、`AuthenticateAny`（接受待验证会话）、`CheckOrigin`、`Cookie`、`ExpiredCookie`、`SessionInfo`、`Sessions`、`RevokeSession`、`RevokeOtherSessions`、`SignIn`、`SignOut`、`VerifyMFA`、`Register`、`VerifyEmail`。
- 因素：`BeginTOTP`、`ConfirmTOTP`、`ReplaceTOTP`、`RegenerateRecoveryCodes`、`StartOAuth`、`CompleteOAuth`、`UnlinkIdentity`、`Begin`/`FinishPasskeyRegistration`、`…PasskeySignIn`、`…PasskeyMFA`、`DeletePasskey`、`RequestPasswordReset`、`ResetPassword`、`ChangePassword`、`RequestEmailChange`、`ConfirmEmailChange`、`ChangeDisplayName`、`DisplayName`。
- 同意：`CurrentLegalDocuments`、`PendingLegalDocuments`、`AcceptLegalDocuments`、`PublishLegalDocument`。
- key 与工作区：`CreateKey`、`GetKey`、`ListKeys`、`UpdateKey`、`DeleteKey`、`GetKeyInfo`、`ReadKeyMetadata`、`PresetKeys`、`AccountWorkspaces`、`Workspace`、`ListWorkspaces`、`GetWorkspace`、`CreateWorkspace`、`UpdateWorkspace`、`SetWorkspaceBudget`、`WithKeyManager`。
- 账户：`Authorize`、`AccountTier`、`BillingPreference`、`UpdateBillingPreference`、`Invitations`。
- 提醒：`RecordSpendAnomalyNotices`、`RecordSitePaymentNotice`、`RecordBalanceNotices`、`MaintainUserNotices`、`DispatchUserNotices`。
- 错误：`ErrUnknownKey`（401）、`ErrAccountSuspended`（403）、`ErrForbidden`、`ErrNotFound`、`ErrConsentRequired`（403 `consent_required`）、`ErrBadEmail`（422）、`ErrInvalidVerification`（422）、`ErrEmailTaken`（409）、`ErrAccountNotLinked`、`ErrPasskeyFailed`、`ErrMFAExhausted`、`ErrTOTPOn`、`ErrTOTPOff`、`*TooManyKeys`（`ErrTooManyKeys`，409 或带 `RetryAfter` 的 429）、`*TooManyAttempts`（`ErrTooManyAttempts`，429）、`ErrBusy`（503）、`ErrUnknownHost`、`ErrStale`（503）、`*SettingError`。
- 审计动作：`api_key.*`、`workspace.*`、`user.password_set`、`user.totp_enable`、`user.totp_replace`、`user.recovery_codes_regenerate`、`user.passkey_add`、`user.passkey_remove`、`user.identity_link`、`user.identity_unlink`、`user.email_changed`、`user.name_changed`、`user.billing_preference`、`session.revoke`、`session.revoke_others`、`account.status_change`、`account.tier_change`、`site_*.update`、`site_legal_document.publish`、`operations_credential.revoke`、`operations_agent.stop`，以及 `security.*`。

## 决策与理由

| 决策 | 理由 |
|---|---|
| 登录前的查找用专用角色拥有的定义者函数，按所有者统一检查 | 角色只读几列、`axiom_app` 进不去；不需要另维护一份函数清单 |
| 准入时读库判定 key 能否使用，路由放进准入事务 | 能被改的依据只有一个来源，没有过时的副本（[领域文档](../../docs/domains/accounts.md#决策与理由)） |
| 满载时结束准入事务，在事务外等待 | 排队不占数据库连接，醒来重新读库也看到等待期间的停用 |
| 缓存键是 `(站点, 哈希)` | 错站点的请求不会污染负缓存 |
| 会话级的 `level` 证明做过两步验证 | 用户级的 `mfa_enabled_at` 证明不了这条会话 |
| key 哈希用 HMAC-SHA256、版本记在行上，密钥放受保护的文件 | key 文本与密钥轮换无关；环境变量会出现在 `docker inspect` 里 |
| 登录的 CSRF 用精确 Origin + JSON + 会话令牌 | 不多一种登录前状态 |
| 注册用只覆盖新账户的范围 | 注册拿不到别人的数据 |
