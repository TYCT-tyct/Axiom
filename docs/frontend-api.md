# 前端接口指南

只靠 HTTP 接口做一个客户端：官网、用户控制台、管理后台，或者别的什么界面。负责人要用自己的前端替换现有的 `web/`（2026-11-06）；这份文档把分散在契约、`web/` 代码和各领域文档里的约定收在一处，让新前端不必读 `web/` 也能对接。现有页面怎么做、要达到什么验收，见[前端与设计](frontend.md)和 [web/README.md](../web/README.md)；本文只讲接口一侧。

谁读：做前端的人，开工前读一遍；对接某个具体操作时以契约为准。每条约定只在一处写全，本文链接过去，不重复：

| 要什么 | 在哪 |
|---|---|
| 每个操作的路径、参数、请求体、响应、错误、权限、限次 | [api/console.yaml](../api/console.yaml)（控制台，也含官网用的公开接口）、[api/admin.yaml](../api/admin.yaml)（管理后台） |
| 契约的写法与检查（安全声明、错误体、分页、金额、幂等、向后兼容） | [平台的「接口约定」](../internal/platform/README.md#接口约定) |
| 登录、会话、两步验证、通行密钥、第三方登录的设计与取舍 | [账户与租户的「登录与会话」](domains/accounts.md#登录与会话) |
| 钱怎么表示 | [计费的「钱的表示」](domains/billing.md) |
| 现有页面怎么调用 | `web/packages/api/src/index.ts`（客户端与错误类型）、`web/packages/api/src/auth.ts`（登录调用） |

运营接口（`api/operations.yaml`）是给运营 agent 用的，不在浏览器里调用，不在本文范围；模型调用接口（`/v1/`、`/api/v1/`）见[接入与对外接口](domains/gateway.md)。

## 地址与主机

| 面 | 谁 | 主机 | 路径 |
|---|---|---|---|
| 官网 | 访客 | 站点的控制台域名 | 页面在 `/`；它用的是控制台契约里不需会话的接口（`getSite`、`listPublicModels`、`getLegalDocuments`、`getVersion`） |
| 控制台 | 用户 | 站点的控制台域名 | 页面在 `/console/`；接口在 `/api/console/v1/…` |
| 管理后台 | 平台管理员 | 站点的管理域名，只在管理监听上 | 页面在 `/`；接口在 `/api/admin/v1/…`；按 `AXIOM_ADMIN_SOURCES` 限制来源 |
| 模型调用 | 用户的程序 | 站点的 API 域名 | `/v1/…`；这个域名上控制台的路径一律 404 |

- **站点由 `Host` 决定。** 一个站点登记三个域名，各属一个面（`console`、`api`、`admin`），程序按请求的 `Host` 认站点和面；不认识的主机、或者别的面的主机，一律 404。反向代理要原样传 `Host`（非默认端口带端口）。登记见[平台的「监听」](../internal/platform/README.md#监听)和 `deploy/.env.example` 的入口一节。
- **接口不发 CORS 头，页面必须与接口同源。** 改数据的请求要带站点登记的 `Origin`，会话 Cookie 是 `SameSite=Lax`，所以别的源上的页面既调不通、也拿不到会话。新前端要从控制台域名（和管理域名）本身提供，或者由同一个域名的反向代理把 `/api/` 转给程序。现有做法是把构建产物嵌入程序（`just web-embed`），机制见[平台的「前端文件」](../internal/platform/README.md#前端文件)；外部静态目录和代理的支持范围还没定，记在 [#695](https://github.com/TYCT-tyct/Axiom/issues/695)。
- **服务端写死了几个控制台页面路径**，新前端要在这些路径上提供对应的页面（挂载点是 `/console`）：

  | 路径 | 谁发出 |
  |---|---|
  | `/console/` | 第三方登录成功后的 303 |
  | `/console/security`、`/console/security?oauth=identity_linked`、`/console/security?oauth=identity_already_linked` | 第三方身份绑定后的 303（成功、已绑定到本账户、已属于别的账户） |
  | `/console/sign-in?oauth=failed` | 第三方登录失败的 303 |
  | `/console/verify-email#token=…` | 开放注册的验证邮件 |
  | `/console/reset-password#token=…` | 找回密码的邮件 |
  | `/console/balance` | 支付渠道充值后把浏览器送回的页面 |

- **类型可以从契约生成。** 现有前端用 openapi-typescript 从两份 YAML 生成类型，再用 openapi-fetch 调用（`just generate`，产物在 `web/packages/api/src/*.gen.ts`）；别的语言用自己的生成器即可，契约是标准 OpenAPI 3。

## 不需要会话的接口

这些接口只要主机是某个站点的域名就能调（`getVersion` 连站点都不需要）：

| 操作 | 用途 |
|---|---|
| `getSite` | 站点名、记账币种、品牌色、logo 地址、注册方式、登录域名与可用的第三方和通行密钥、人机校验参数、API 地址（`api_base_url`）；页面据此决定显示什么，不自己猜 |
| `listPublicModels` | 官网的模型与价格页；站点只对登录用户展示价格时，每个模型没有 `pricing`，页面显示「无价格」而不是报错 |
| `getLegalDocuments` | 当前的服务条款与隐私政策（Markdown，不是 HTML） |
| `getVersion` | 正在运行的程序版本，见[版本与刷新提示](#版本与刷新提示) |
| `getSiteLogo` | logo 的字节，地址里是内容哈希，可以长期缓存 |
| `signIn`、`register`、`verifyEmail`、`requestPasswordReset`、`resetPassword`、`startOAuthSignIn`、`beginPasskeySignIn`、`finishPasskeySignIn` | 登录前的操作，改数据，要 `Origin`，没有 CSRF 令牌 |

## 登录与会话

**会话是服务端的。** 登录成功后响应的 `Set-Cookie` 给一个 `__Host-axiom_session`（HttpOnly、Secure、`SameSite=Lax`；只有站点登记的源是 `http://localhost…` 时去掉 `__Host-` 前缀，给本机开发用）。脚本读不到它，页面里不存任何会话令牌。会话只在签发它的站点和面上有效：控制台的会话到管理后台是 401。空闲超时和最长有效期由站点配置，`getSession` 的 `expires_at` 是「不再使用时的结束时间」。

**页面启动先问 `getSession`。** 200 是已登录，401 是没登录（这是正常状态，不是故障）。响应里：

| 字段 | 用途 |
|---|---|
| `level` | `pending_mfa`（密码对了、等第二因素）、`password`、`mfa`；契约里每个操作的 `x-axiom-level` 写它至少要到哪一级 |
| `csrf_token` | 页面启动时从这里拿，只放内存（Cookie 读不到，刷新页面就重新问） |
| `permissions` | 本会话在自己范围（控制台是用户的账户，后台是平台）上的权限名，**只用来显示或隐藏**；每个请求服务端都会重新判断 |
| `mfa_enabled` | 用户有没有 TOTP；后台据此决定是否进入开启流程 |
| `consent.required` | 还没接受的当前法律文本；有就先展示并调用 `acceptLegalDocuments`，在那之前除 `getSession`、`acceptLegalDocuments`、`signOut` 外的控制台操作都是 403 `consent_required` |

### 密码登录与第二因素

```
signIn ──200 status=signed_in──────────────▶ 已登录（level=password）
   └────200 status=mfa_required（5 分钟的待验证会话）
          ├─ verifyMfa（TOTP 6 位码，或 xxxx-xxxx-xxxx-xxxx 的恢复码）──▶ level=mfa
          └─ beginPasskeyMfa → 浏览器 WebAuthn → finishPasskeyMfa ───────▶ level=mfa
```

- 只有签发会话的响应带新的 `Set-Cookie` 和 `csrf_token`（`SessionGrant`）：`signIn`、`verifyMfa`、`finishPasskeySignIn`、`finishPasskeyMfa`，页面要换掉内存里的旧令牌。`beginPasskeyMfa`、`beginPasskeySignIn` 只回挑战，不带令牌，**不要拿它的响应更新令牌**，否则下一个改数据的请求会因令牌为空而 403 `origin_rejected`。待验证会话只被 `verifyMfa`、通行密钥的第二步、`getSession` 和 `signOut` 接受，别的操作对它一律 401。
- 邮箱不存在、密码错、用户停用的答案相同（`unauthenticated` 或 `invalid_credentials`）；页面不要区分。
- **后台只认 `mfa` 级会话。** 没有 TOTP 的管理员登录后（`level=password`、`mfa_enabled=false`）只能走开启流程：`beginTotpSetup` 拿到密钥和 `otpauth` 地址（现有页面在本地画二维码，不把密钥交给外部服务）→ `confirmTotpSetup`（再填一次密码和新密钥生成的码）。后台没有通行密钥和第三方登录。
- 改了认证因素（密码、TOTP、恢复码、通行密钥、第三方身份）后，这个用户的旧会话全部失效，当前设备拿到新会话（响应的 `Set-Cookie`）。令牌和恢复码怎么回来，**按操作区分**，不能一概按 `FactorChange` 处理：

  | 操作 | 响应 | 新 `csrf_token` | 恢复码 |
  |---|---|---|---|
  | `confirmTotpSetup`、`replaceTotp`、`regenerateRecoveryCodes` | `FactorChange` | 有 | `recovery_codes`，10 个 |
  | `changePassword` | 同形的对象 | 有 | 仅在开着 TOTP 时有 |
  | `deletePasskey`、`unlinkIdentity` | `CredentialChange` | 有 | 仅在开着 TOTP 时有 |
  | `finishPasskeyRegistration` | `Passkey` | **没有**：成功后调 `getSession` 取新令牌 | `recovery_codes` 仅在开着 TOTP 时有 |
  | 第三方身份绑定（`startOAuthLink` → 303 回 `/console/security`） | 重定向，没有响应体 | 没有：调 `getSession` | 没有：调 `takeRecoveryCodes` 读一次 |

  恢复码**只显示一次**，要放在用户确认前不能关掉的界面里。细节与取舍见[账户与租户](domains/accounts.md#登录与会话)。
- `signOut` 撤销当前会话并让 Cookie 过期；`listSessions`、`revokeSession`、`revokeOtherSessions` 管登录设备。

### 第三方登录与通行密钥

- **第三方登录**：`startOAuthSignIn` 返回 `authorize_url` 和一个一次性 nonce Cookie；页面把浏览器整页跳到 `authorize_url`。供应商回来时访问 `/api/console/v1/auth/oauth/{provider}/callback`，服务端换码、种会话、**303 回页面**（路径见上表），浏览器不会落在 JSON 上。只在站点的**登录域名**（`getSite` 的 `login_domain`、`login_origin`）上能发起；在别的控制台域名上，页面把访客带到 `login_origin`。第三方身份不会按邮箱并入已有账户：没绑定过的 subject 登不进来（`account_not_linked`），要先登录再 `startOAuthLink`；绑定回来的新恢复码放在会话上，用 `takeRecoveryCodes` 读一次。
- **通行密钥**：`beginPasskeySignIn` / `finishPasskeySignIn` 是不输邮箱的登录（discoverable credential），`beginPasskeyMfa` / `finishPasskeyMfa` 是密码之后的第二因素，`beginPasskeyRegistration` / `finishPasskeyRegistration` 注册（要当前密码）；`flow_id` 由 begin 给、finish 带回。同样只在登录域名上能用，限制见[账户与租户](domains/accounts.md#登录与会话)的「通行密钥」。**接口的 JSON 不能直接交给浏览器的 WebAuthn API**：选项里的 `challenge`、`user.id`、`allowCredentials` 和 `excludeCredentials` 里的凭证 `id` 是 base64url 字符串，`navigator.credentials.create/get` 要二进制缓冲区；回传时 `rawId` 和 `response` 里的二进制成员（`clientDataJSON`、`attestationObject`、`authenticatorData`、`signature`、`userHandle`）要转回无填充的 base64url 文本。现有转换在 `web/console/src/webauthn.ts`。

### 注册与找回密码

- 注册方式看 `getSite.registration`：`invite`（`register` 带邀请码）、`open`（先 `register` 提交邮箱，邮件链接到 `/console/verify-email#token=…`，页面再调 `verifyEmail` 设密码；站点开了人机校验时，页面先按 `human_check` 的参数在浏览器里算工作量证明）、`closed`（不收人）。站点有法律文本时，注册要带屏幕上实际显示的每种文本的版本。
- 找回密码：`requestPasswordReset` 对任何邮箱都答同样的空 202，页面提示「没收到请稍后再试」；邮件链接到 `/console/reset-password#token=…`（令牌在片段里，不进请求日志），页面调 `resetPassword`。重置不自动登录。

## 改数据的请求

服务端在处理器之前按契约检查，所以任何客户端都要满足：

1. **`Origin`** 等于站点为这个面登记的源（`https://域名`，本机开发是 `http://localhost[:端口]`）。浏览器的 `fetch` 自己会加；非浏览器客户端要自己写。不符是 403 `origin_rejected`。
2. **`Content-Type: application/json`**，每个改数据的请求都要，即使没有请求体。
3. **`X-CSRF-Token`**：有会话的改数据请求带当前会话的令牌（`getSession`、`signIn`、`verifyMfa`、因素变更的响应里给）。不对是 403 `origin_rejected`。
4. 请求体最大 1 MiB，必须是 UTF-8 JSON、同一个对象里成员名不能重复、嵌套不超过 256 层；不符是 400 `invalid_request`。成员名区分大小写；契约没有的成员，多数请求体（`additionalProperties: false`）会被拒绝，只送契约里有的。
5. 控制台一个请求最多 15 秒，后台 60 秒；到期是 503 `unavailable`，意思是**工作可能已经做完**，先查再重试，改数据的请求用[幂等键](#幂等键与修订号)重发。

守卫的顺序是：站点 → Origin 与 JSON → 会话与级别、CSRF → 权限 → 请求体与查询参数的 schema。所以没有会话的客户端只会得到 401，不会先得到 400。

## 错误与请求 ID

- **错误体**：所有失败都是 `{"error": {"code", "message", "fields"}}`。`code` 取自封闭的 `ErrorCode` 枚举，**界面按它翻译**；`message` 是写给日志和开发者的英文，不要展示给用户；`fields` 在 `invalid_request` 时逐条给出出错的位置（请求体的 JSON 指针，或查询参数名）和规则，不含值。
- **请求 ID** 在每个响应（含错误）的 `X-Request-ID` 头里，服务端自己生成，不采用客户端传的。出错界面要显示它，用户报问题时凭它查日志。所有响应带 `Cache-Control: no-store`。
- **状态码**（`code` 见契约的 `ErrorCode`）：

  | 状态 | 意思 | 页面怎么办 |
  |---|---|---|
  | 400 `invalid_request` | 请求不合契约，`fields` 指出哪里 | 标在对应输入上 |
  | 401 | 没有这个面的有效会话，或登录凭据不对 | 去登录页，登录后回到原页面 |
  | 403 | 缺权限、没过两步验证（`mfa_required`）、Origin 或 CSRF 不对（`origin_rejected`）、没接受法律文本（`consent_required`） | 按 `code` 区分；没权限是终态，不要重试 |
  | 404 | 没有这个资源，或本会话的范围看不到它；ID 格式不对和不存在同样 404 | 说明找不到，不让人重试 |
  | 409 | 与当前状态冲突：`stale_revision`、`idempotency_key_reused`、`conflict` 等 | `stale_revision` 要重新载入再改 |
  | 422 | 请求合法但被规则拒绝，`message` 说明原因 | 按 `code` 提示 |
  | 429 `rate_limited` | 超限，`Retry-After` 是要等的整秒数 | 显示等多久，不自动重试 |
  | 500 / 501 / 503 | `internal`（不带内部原因）/ 操作没有处理器 / `unavailable` | 5xx 可以自动重试读；写要先用幂等键确认 |

- **没有响应**也是一种结果：网络断了、超时、被中断。现有客户端给每个请求 10 秒期限（含读响应体）；没收到响应和 5xx 当作暂时性失败，读请求自动重试两次，4xx 和读不出来的响应不重试。网关或代理可能返回不是 JSON 的页面，这种响应没有错误码，按「读不出来」处理。
- 契约改动时 `ErrorCode` 会加新值；遇到不认识的码，显示通用文案和请求 ID，不要崩。

## 分页

列表接口统一用键集游标：查询参数 `cursor`（不透明，原样传回，只对同一组筛选有效，格式不对是 400）和 `limit`（默认 50，最大 200）；响应 `{data, next_cursor}`，最后一页的 `next_cursor` 是 `null`（整个契约里唯一显式的 null）。没有总数和页码；要「共多少条」用专门的聚合接口（如 `aggregateCalls`）。用量趋势使用 `getUsageStatistics`（`/api/console/v1/usage/statistics`），小时汇总、空桶省略、报告缺失和前期对比的口径见[记录与告警](domains/observability.md#用户调用记录查询与导出)。范围必须对齐 UTC 整点，最多 90 天（小时粒度 7 天）；旧节点 404 或缺响应字段时显示不可用，`backfill_complete=false` 时提示历史仍在回填。导出（`exportCalls`、`exportStatement`）返回 `text/csv`，不分页。

## 幂等键与修订号

- **后台**：除 Session 标签外的写操作都要 `Idempotency-Key`（调用方为一次想做的改动选的小写 UUID）和写进审计的 `reason`（1 到 1000 字符）。丢了响应就用**同一个键、同一个请求体**重发：得到第一次的结果，响应头 `Idempotency-Replayed: true`，改动不做第二次；同一个键用于别的操作者、别的操作或别的内容，是 409 `idempotency_key_reused`。所以一个操作一个键，对话框打开时生成；发出后结果未知就原样重发，只有服务端明确拒绝才换新键。不改数据却要请求体的 POST 标 `x-axiom-safe: true`，不带这两项。存法与保留见[平台的「幂等」](../internal/platform/README.md#幂等)。
- **控制台**里只有动钱的几个操作要键（`createOrder`、`requestRefund`、`redeemCode`），格式是 1 到 128 个字母、数字和 `._:-`。这些操作的状态码本身有含义，要按状态读：例如 `createOrder` 的 201 是等待支付的订单、202 是渠道没确认（用户不能付，页面取消它或等它过期）、200 是已不在等待的订单，见契约。
- **修订号**：读到的资源带 `rev`（或 `revision`），改的时候原样送回；已经变化就是 409 `stale_revision`，页面重新载入后才能再保存，不覆盖别人的改动。
- `PUT` 是整体替换：请求里没写的可选项取「没有限制」或默认值，所以页面把读到的设置连同修改一起送回，不能只送改动的那一项。

## 钱、数字与时间

- **金额**是 `Money`：`amount` 是有符号整数的十进制**文本**，单位是币种的十亿分之一；`currency` 是站点的记账币种（`getSite.currency`，创建后不变）。JSON 数字在浏览器里超过 2⁵³ 就失真，余额和报表会超过，所以显示、输入、比较都用整数文本或 BigInt，不经过浮点数。其他金额字段（后台审批的上限、用量）写明「整数文本」的同理。
- **比率、倍率、单价**是十进制文本（`Decimal`、`UnitPrice`，单价至多 15 位小数），同样不经过浮点数。
- **ID**：账户模块的实体是 TypeID（`user_…`、`acct_…`、`key_…`、`ws_…`、`site_…`、`tier_…`），调用、目录、供应商、定价的行是 UUID 文本。不要解析 ID 的内部。
- **时间**是 RFC 3339 的 `date-time`，页面按查看者的时区显示并注明时区；按 UTC 定义的月份（报表的 `month`）在控件旁写明 UTC。
- **缺省与空**：响应里没有的值就不出现，不写 `null`。新加的响应字段都是可选的，见[向前兼容](#向前兼容)。

## 版本与刷新提示

`getVersion`（控制台是 `/api/console/v1/version`，后台是 `/api/admin/v1/version`）不需要会话和站点，答 `{"version": "…"}`，就是 `axiom version` 输出的整行，不缓存。页面构建时把同一棵代码树构建出的程序的版本写进去（`just web-build` 设 `AXIOM_VERSION`），启动后每 5 分钟和每次标签页重新获得焦点时问一次，**整串相等才算同一版本**，不同就提示刷新。没有版本的构建（开发服务器）不问。部署时旧页面还会连着新节点、新页面也可能连到旧节点，所以接口只做向后兼容的改动；按需加载的代码块在部署后拿不到时，现有页面自动刷新一次，规则见 [web/README.md](../web/README.md#规则)。

## 限流

- 登录前的操作有每 IP 的令牌桶（登录、注册、第二步共用，突发 20、每分钟 10），登录失败按邮箱、用户和每次待验证的登录另计，次数用尽后等待，不锁账户；上限是站点配置，默认值见[账户与租户](domains/accounts.md#登录与会话)。另有几个操作各有自己的窗口（建订单、验证订单、兑换码、建 key）。具体到哪个操作有哪些限制，契约的 `description` 里写明，声明了的操作有 `429` 响应。
- 超限是 429 `rate_limited`，`Retry-After` 是整秒数；页面显示要等多久并禁用相应按钮，不自动重试。
- 模型调用的业务速率限制是另一套（按账户、key 计），不影响这些接口，见[账户与租户的「业务速率限制」](domains/accounts.md#业务速率限制)。

## 页面必须取自接口的数据

原则：页面**显示服务端给的东西**，不自己推导。服务端每次请求都重新判断权限、价格和是否需要审批，页面推导出的结果迟早和它不一致，而且换一个前端就得把同一套推导再写一遍。落到具体：

- **权限**：用 `getSession.permissions` 决定显示或隐藏操作，别按角色名猜；403 才是准确的答案。
- **名字**：模型用公开名和作者（`listPublicModels`、`listAccountModels`），端点用稳定的公开代号；不显示供应商名、上游名、成本（这些接口也不会给）。
- **价格**：模型页的价格用 `pricing`（售价，含每个端点自己的价格）；列表里一个计费项显示哪条单价读 `headline`，显示的数和单位读 `display_amount`、`display_per`（token 项是每百万 token），不自己挑服务等级和档位、不自己移小数点；旧节点不带这些字段时列表价显示「不可用」，明细按 `amount` 每单位原样显示。后台的售价用定价规则、`previewPricing` 的试算和发布列表（`listPricePublications`：当前价格版本、待确认与暂停状态都是接口给的）。调用记录的费用旁边有冻结的价格版本（`price_version`），旧节点不返回时显示「不可用」，不去重算。
- **调用会话**：`listCallSessions`（`/api/console/v1/calls/sessions`）返回非空客户端会话的次数、费用、主模型和窗口内起止时间；最多 31 天，翻页保留返回的 `since` / `until`，刷新首页查看新调用。所有响应字段可选，旧节点 404 时显示不可用。分组、币种和筛选口径见[调用记录查询](domains/observability.md#用户调用记录查询与导出)。
- **调用尝试与路由**：`getCall` 的可选 `attempts`、`route` 和 `listCalls` 的 `attempt_count`、`fell_back` 筛选口径见[调用记录查询](domains/observability.md#用户调用记录查询与导出)。缺 `endpoint_code` 或 `charged` 显示未知；旧节点缺字段不在页面推算。
- **筛选结果**：接口筛过的，要看它回显的筛选（如调用列表的 `status_filter`）才展示；旧节点没回显，就显示「不可用」，不在浏览器里筛一页普通数据冒充。
- **预设**：路由预设的规则（`RoutingRules`，契约里逐项有类型）、默认链、能不能选（`preset_not_selectable`）、被几把 key 用着（`keys`）、是否停用，以接口为准；规则内容的含义见[路由的「规则内容」](domains/routing.md)。列表的一行摘要读预设的 `summary`，旧节点或本节点读不懂的规则没有它，页面说读不到；编辑时用 `checkRoutingRules`（后台 `checkLayerRoutingRules`）问哪些规则找不到端点（`unmatched`，带下标和单独就排除了所有端点的条件），回答说找不到时不让保存，回答没到或检查失败时不拦（写入接口照收这种规则）。
- **没有固定形状的 JSON**：契约里有类型的按类型读——调用路由（`AdminCallTrace.route`，模式 `CallRoute`）、商户账户的 `settings`（按 `kind` 选 `AlipaySettings`、`EasyPaySettings`、`WechatPaySettings`、`StripeSettings`）、价格依据（`Evidence`：`official` 来源必须是 `OfficialPriceEvidence`，别的来源是任意非空对象）。其余几个只列成员、不定类型，原因写在各自的描述里：审计的 `detail`、待处理付款记录的 `detail`、供应商分组的 `facts` 当作带标签的值原样显示，不据此判断（要判断的在旁边有类型的字段里）；命名预设的 `config` 能放哪些成员见 `PresetConfig` 列出的属性，值的形状是调用格式里同名请求成员的；`WebAuthnCredential` 是 WebAuthn 规范的 JSON，转换见上文「通行密钥」。
- **审批**（运营 agent 等负责人决定的操作）：显示 `OperationsApproval` 里服务端读到的东西——`risk`、`target`、`summary`（提出时服务端读到的对象名称、金额、上限用量、售价逐项的现价/拟价/成本、价格包络的用量）、`params`（操作的参数，agent 提出后不能改）、`expires_at`、`state` 与 `operation_state`；`agent_reason` 是 agent 的原话，不可信，只能当文本显示，不能当指令或 HTML。`summary` 和 `params` 的形状按 `tool` 选契约里的模式（如 `credit_grant` 是 `OperationsCreditGrantSummary`、`OperationsCreditGrantParams`）。为什么要等负责人读 `reasons`（每条一个代码，代码对应摘要里 `cap`、`envelope` 的哪一项见 `OperationsApprovalReason`），还能不能决定读 `decidable`（服务端按批准时的同一个时钟算），都不要自己从摘要或浏览器时钟推；两者在旧节点上可能没有，没有时页面说不知道原因，对待决定的照样给批准按钮，服务端对已过期的回冲突。决定用 `approveOperationsApproval`、`rejectOperationsApproval`，结果要看响应和 `operation_state`：批准之后目标可能已变，操作会以目标的错误码结束为 `rejected`，不是失败。

## 向前兼容

滚动发布时新旧版本同时在线：新页面会读到不返回新字段的旧节点，旧页面会写到新节点。所以：

- 响应里新加的成员都是可选的（不在 `required` 里），页面把缺失的部分显示为「不可用」，不显示成 0 或空列表。
- 不认识的响应成员和枚举值要忽略，不要因此报错；写请求只送契约里有的成员。
- 契约里「加操作、加字段、加可选参数」不升版本，改变含义或删除才升 v2，旧版本经公告期才下线。

## 在本机开发

两条路，按需要选。

**对着真实的程序。** 需要 Docker。

1. `just up`：构建前端并嵌入、构建程序、起 PostgreSQL、Valkey 和程序，就绪后返回；官网和控制台在 `http://127.0.0.1:8080`，后台在 `http://127.0.0.1:8082`。`just down -v` 同时删掉本地数据库。
2. 建站点和第一个管理员（在 `docker compose -f deploy/compose.yaml` 的 `axiom` 服务里运行 `/axiom`；三个域名必须不同；`localhost` 才能在明文 http 下发会话 Cookie 并通过 Origin 检查）：

   ```sh
   docker compose -f deploy/compose.yaml exec axiom \
     /axiom site init --name demo --currency USD \
     --console localhost:8080 --api 127.0.0.1:8080 --admin localhost:8082
   printf '%s\n' 'a-long-dev-password' | \
     docker compose -f deploy/compose.yaml exec -T axiom /axiom user create --email admin@example.test
   docker compose -f deploy/compose.yaml exec axiom \
     /axiom user grant-platform --email admin@example.test --role admin
   ```

   命令与参数见[账户模块的「命令行」](../internal/accounts/README.md#命令行)，同样的步骤在[示例法律文本](legal/README.md)里用过。
3. 用 `http://localhost:8080/console/` 和 `http://localhost:8082/` 登录；后台要先开启 TOTP（见上）。没有模型目录和供应商的空站点，模型页、调用记录、价格页都是空状态；要有数据，用后台的目录、供应商、定价页面或接口配置，旅程测试（`cmd/axiom/journey_test.go`，构建标签 `journey`）里有一套完整的种子数据可参考。
4. 改前端后要重新 `just up`（嵌入的是构建产物）。想用开发服务器热更新，`just web-dev`（官网 5173、控制台 5174、后台 5175）把 `/api` 转给 8080/8082，但它**不改写 `Host` 和 `Origin`**（`web/vite.shared.ts` 的代理没有 `changeOrigin`），所以站点登记的域名要写成开发服务器自己的主机和端口（例如 `--console localhost:5174 --admin localhost:5175`）。这一条是读配置得出的，本文没有实际跑过。

**对着模拟。** 现有的模拟是 Playwright 的路由拦截（`web/e2e/mock.ts`，各面的路由和数据在 `web/e2e/console-api.ts`、`web/e2e/admin-*.ts`），只能在 Playwright 里用，不是独立的服务。它的价值是做法：路径、方法、请求体和每个响应都由契约生成的类型约束，写错就编译不过；会话、级别、权限、CSRF、Origin，以及后台的 Idempotency-Key 与理由都按契约校验，页面漏一项测试就失败。新前端想要同样的保证，用同样的办法：从契约生成类型，让模拟的每个响应过类型检查。独立的服务端模拟还没有，记在 [#695](https://github.com/TYCT-tyct/Axiom/issues/695)。

对真实服务的端到端旅程见 [web/README.md 的「测试」](../web/README.md#测试)（`just web-journey`，要 Docker）。

## 范围外与待定

- [#695](https://github.com/TYCT-tyct/Axiom/issues/695) 余下的缺口：替换前端怎么交付、独立的模拟服务。
- 本文只覆盖契约里已有的操作；契约没有的页面能力（见[前端与设计的页面清单](frontend.md#页面清单)里标 M2/M3 的）等接口加上后再写。
