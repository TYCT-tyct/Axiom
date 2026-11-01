# websearch

`web` 插件和 `:online` 变体的一次外部搜索，以及节点上可配置的多个搜索引擎。网关在准入之后、调用模型之前选一条路径：模型自己支持原生搜索时打开厂商的搜索开关，否则取最后一条用户文本，经请求指定的后端查询一次，把有界的结果作为 system 消息注入请求，再按该后端发布的价格收取一次搜索费。

## 职责与边界

只有两件事：一次带界的查询换来有界的条目（URL、标题、摘要），以及把管理员配置的搜索引擎加载成节点可用的后端。`Searcher` 是一次搜索的实现，`SearXNG` 和 `Firecrawl` 是两个实现；`Engine`/`Backend`/`Engines` 是配置与运行时集合；`Load`/`Save` 读写配置，`OpenEngines` 在启动时把它变成后端。网关负责准入、路径选择、预留、注入位置、正文大小的最终上限、调用记录和计费；本包负责 URL、参数、密钥、响应大小、结果条数、去重、截断和售价计算。

不在这里：模型侧的搜索循环（`openrouter:web_search` 仍拒绝）、把结果转成各格式的注解（[gateway 的「插件与提示词变换」](../../docs/domains/gateway.md#插件与提示词变换)）、按次收费（[billing 的「预留与结算」](../../docs/domains/billing.md#预留与结算)）。

## 配置

搜索引擎保存在 `platform.search_engines`（迁移 `00027_search_engines.sql`），一行一个：

| 列 | 说明 |
|---|---|
| `id`、`name` | `name` 是请求 `engine` 写的名字：小写字母或数字开头，之后可有 `-`、`_`，最长 32，不能叫 `native` |
| `kind` | `searxng` 或 `firecrawl` |
| `url` | `http(s)` 基地址，不含账号、查询串和片段；Firecrawl 留空时用 `https://api.firecrawl.dev/v2` |
| `enabled`、`is_default` | 只有启用的后端能被请求选中；恰好一个启用的后端是默认。部分唯一索引保证至多一个默认 |
| `cost_search`、`cost_result`、`markup` | 每次搜索成本、每条额外结果成本、加价比例（0–10） |
| `price_search`、`price_result`、`price_version` | 发布的对外价 = 成本 ×（1 + 加价），向上取整到最后一位；成本或比例变化时版本加一 |
| 信封列 | `ciphertext`、`nonce`、`wrapped_dek`、`key_version`，AAD 是 `platform.search_engines`、该行 ID 和 `purpose=config`，与 SMTP 配置一起纳入主密钥轮换 |

`Load` 依次读表、旧的单后端行 `platform.search_config`（迁移 `00025_search_config.sql`）、环境变量 `AXIOM_SEARCH_URL`/`AXIOM_SEARCH_KEY`。旧行和环境配置都显示成一个名为 `searxng` 的引擎，售价为 0，也就是按模型自己的 `web_search` 价卖，行为与引入多引擎之前一致。`Load` 的 `Source` 是 `database`、`saved`、`environment` 或 `none`。表为空且没有管理员的清空标记时，回到旧行或环境变量；管理员清空列表会写旧的单后端空行作为标记，所以保存空列表就是关掉搜索，环境变量不会把它带回来。

管理员保存时 `Save` 先锁一个事务级 advisory lock，再整表替换：没列出的引擎删除。每个输入可以带 `id` 编辑已有行，不带就新建。省略密钥时：同 `id` 且 URL、类型没变，沿用旧密钥；不带 `id` 时按同名行找，再按同 URL、同类型的旧单后端/环境配置找——后台把环境里的后端原样保存时不必重填密钥；换了 URL 或类型就丢掉旧密钥，绝不把旧凭证发给新主机，Firecrawl 没有密钥直接拒绝。`price_version` 只在该行成本或加价变化时加一，改 URL、名字或开关不动它。

保存时检查 URL 能否作为出网目标：全部 DNS 答案必须是公网地址，回环、私网、云元数据地址和混合答案拒绝。运行时的每次搜索再由节点共享的 `egress.Client` 重新解析并校验，走同一个出口。密钥只进加密行，不返回、不写审计、不进日志。保存后 API 返回 `restart_required`；运行中的节点不换后端，重启时由 `OpenEngines` 重建，和邮件配置一致。`ErrInvalidConfig` 是输入错误，数据库和解封错误原样上抛。

`OpenEngines` 在启动时校验：名字合法且不重复、恰好一个启用的默认、Firecrawl 有密钥、类型认识，然后为每个启用的后端建一个 `Searcher`。它不看 `platform.search_config`，也不联网探测。

## 查询与结果

- 查询词是 `protocol.ChatSearchQuery` 逐 token 扫出的最后一条用户消息的文本部件，不整份解码历史；空查询在准入前就 400，超过 1024 字节也 400。
- `max_results` 默认 5，上限 10。`include_domains` / `exclude_domains` 在协议层已校验成纯主机名（小写、有点、不含前后连字符或连续点），这里只转成 `site:` 和 `-site:` 操作符，不拼接客户端原文。
- 一次 `Search` 的工作集（响应正文和解码结果）由网关按 `WorkingSet`（16 MiB）在搜索前计入节点预算；注入的正文再按原正文三倍加 `WebSearchMaxBytes` 计入，调用结束才归还。
- 所有适配器共享 `DefaultResults=5`、`MaxResults=10`、`Timeout=15s`、4 MiB 响应上限、8 KiB URL、256 字节标题、1024 字节摘要，按 UTF-8 边界截断；只收 `http`/`https` 且有 URL 的条目，重复 URL 去重，空对象或无效结果不会按响应里的条数分配。
- 查询词、响应正文和密钥都不进错误和日志；错误只说明类别（`ErrUpstream`、`ErrNoQuery`、`ErrQueryTooLong`、超时）。搜索失败在网关里是普通上游失败：没有模型调用、没有模型用量，也不收这次搜索。

### SearXNG

请求是 `GET {base}/search?q=...&format=json`，`Accept: application/json`，配了 key 时带 `Authorization: Bearer`。响应是 `{"results":[...]}`，读 `url`、`title`、`content`。SearXNG 默认只开 HTML，部署时必须显式打开 JSON：

```yaml
search:
  formats: [html, json]
server:
  secret_key: "<random>"
  limiter: false
```

没有 `server.secret_key` 时 SearXNG 会静默忽略配置（包括 JSON 格式），所以这是必需的。`limiter: false` 让同一个节点的高并发请求不被本机的限流器拒绝；真正的入口限制由前面的反向代理承担。容器测试用 `json_engine` 指向一个本地 nginx 作为记录响应源，不需要外网（`container_test.go`）。

### Firecrawl

请求是 `POST {base}/search`，`Authorization: Bearer <key>`，JSON `{"query":...,"limit":...}`，可选 `includeDomains` / `excludeDomains`；两个过滤都写时 Firecrawl 不接受同时给，本包把它们折成查询里的 `site:` / `-site:` 操作符。响应是 `{"success":true,"data":{"web":[{"url","title","description"}]}}`。查询上限 500 字节（Firecrawl 的限制），超过返回 `ErrQueryTooLong`，网关回 400。

选 Firecrawl 的理由：它按「每次搜索 + 每条结果」计价，正好对应两个成本字段；HTTPS JSON、Bearer 密钥；2026-11-01 从部署节点核对 `api.firecrawl.dev` 根路径 200，`/v2/search` 对 GET 返回 405，说明端点存在且只收 POST。SearXNG 仍可自建在中国大陆，Firecrawl 作为公网付费后备；大陆侧直连没有在本机验证，部署时用后台的测试按钮确认。比较过的替代：Tavily 按请求扣 credit、不按结果，装不进两个成本字段；Parallel 的定价页与可达性无法在同等条件下确认，未采用。

## 测试

- `websearch_test.go` 用录制的 SearXNG 响应（`testdata/searxng.json`）在 `httptest` TLS 服务器上验证路径、`format=json`、`site:` 参数、bearer、去重和截断；失败用例覆盖非 200、非 JSON、超限正文，并断言查询和正文不回显。
- `firecrawl_test.go` 用录制的 Firecrawl 响应验证 POST、路径、Bearer、`limit`、两个过滤字段与操作符回退、去重和无效 URL；还覆盖缺密钥和超过 500 字节的查询。
- `settings_test.go` 覆盖密钥沿用/丢弃、加价后的发布价与价格版本、以及 `OpenEngines` 对重复名字、默认数量、类型、Firecrawl 密钥的校验。
- `container_test.go` 起真实的 `searxng/searxng` 容器（镜像版本在文件顶部）和 nginx 侧车；它需要容器运行时，按仓库规则在 `flock /tmp/axiom-remote-check.lock` 下跑。
- 网关的 `TestWebPluginSearchesInjectsAndAnnotates`、`TestWebPluginSearchFailureSearchesNothingElse`、`TestChooseSearch`、`TestQuotablesSellsTheEnginePrice` 覆盖注入、`url_citation`、路径选择、按后端计价和搜索失败不调用上游；协议层的 `TestInjectNativeSearch` 覆盖三种格式的原生开关。
- 上线前对真实 SearXNG 的活检查见 #497：在后台填 URL 和 key，保存并重启，用一个 `plugins:[{id:"web"}]` 的 Chat 请求确认回复里有结果、非流式回复带 `url_citation`、调用记录里 `web_search` 计一次；再清空列表确认回到 400。

## 已知边界

- 只有 Chat 插件；Messages 和 Responses 的 `plugins` 仍拒绝。
- 原生搜索只支持 Chat、Responses、Messages；没有 Gemini 原生格式，所以 Gemini 端点只走后端搜索。原生路径不看 `max_results`、`search_prompt` 和域名过滤。
- `include_domains` 依赖所选引擎是否理解 `site:` 操作符；不理解时结果不会过滤，但查询仍合法，不会把操作符当正文转发给模型。
- 流式回复没有整条消息，不补 `url_citation`；结果 URL 已写在注入的 system 消息里，正文里的链接由模型自己写。
- 结果文本是外部内容，按不可信数据处理：只做长度和字符集边界，不执行其中的指令；注入的 system 消息措辞明确要求引用来源。
- SearXNG 页面本身可能返回 `answers`、`infoboxes`、`suggestions`；这里只用 `results`。
