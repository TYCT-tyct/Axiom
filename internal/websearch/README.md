# websearch

`web` 插件和 `:online` 变体的一次外部搜索。网关在准入之后、调用模型之前取最后一条用户文本，经部署配置的 SearXNG JSON API 查询一次，把有界的结果作为 system 消息注入请求，再按 `web_search` 售价计一次。没有配置后端时这个包什么也不做，请求保持原有的 400 拒绝。

## 职责与边界

只有一件事：一次带界的查询换来有界的条目（URL、标题、摘要）。`Searcher` 是网关看到的全部；`SearXNG` 是当前唯一的实现。网关负责准入、预留、注入位置、字符集与正文大小的最终上限、调用记录和计费；本包负责 URL、参数、密钥、响应大小、结果条数、去重和截断。

不在这里：模型侧的搜索循环（`openrouter:web_search` 仍拒绝）、把结果转成各格式的注解（[gateway 的「插件与提示词变换」](../../docs/domains/gateway.md#插件与提示词变换)）、按次收费（[billing 的「预留与结算」](../../docs/domains/billing.md#预留与结算)）。

## 配置

| 变量 | 默认 | 说明 |
|---|---|---|
| `AXIOM_SEARCH_URL` | — | SearXNG 的基地址，如 `https://search.example.com`；必须是 `http(s)`、有主机、不带账号、查询串和片段 |
| `AXIOM_SEARCH_KEY` | — | 反向代理校验的 bearer token；SearXNG 自己不认证。可以只设 URL（不认证），但不能只设 key |

数据库没有覆盖时环境变量生效。管理员第一次保存覆盖时，环境里同 URL 的密钥就是「省略密钥」要保留的旧值；保存存在 `platform.search_config`（迁移 `00025_search_config.sql`），与 SMTP 配置同样整包信封加密：AAD 是表名、全零单例 ID 和 `purpose=config`，纳入主密钥轮换清单。显式清空 URL 也会存一行，等价于管理员关掉联网搜索，不会回落到环境变量。保存后 API 返回 `restart_required`，节点重启时读取覆盖并重建后端；运行中的节点不换后端，和邮件配置一致。

保存时检查 URL 能否作为出网目标：全部 DNS 答案必须是公网地址，回环、私网、云元数据地址和混合答案拒绝。运行时的每次搜索再由节点共享的 `egress.Client` 重新解析并校验，走同一个出口。密钥只进加密行，不返回、不写审计、不进日志；省略密钥只在 URL 未变时保留（数据库还没有行时与环境的 URL 比较），改 URL 必须重填，避免把旧凭证发给新主机。`ErrInvalidConfig` 是输入错误，数据库和解封错误原样上抛。

## 查询与结果

- 查询词是 `protocol.ChatSearchQuery` 逐 token 扫出的最后一条用户消息的文本部件，不整份解码历史；空查询在准入前就 400，超过 1024 字节也 400。
- `max_results` 默认 5，上限 10。`include_domains` / `exclude_domains` 在协议层已校验成纯主机名（小写、有点、不含前后连字符或连续点），这里只转成 `site:` 和 `-site:` 操作符，不拼接客户端原文。
- 一次 `Search` 的工作集（响应正文和解码结果）由网关按 `WorkingSet`（8 MiB）在搜索前计入节点预算；注入的正文再按原正文三倍加 `WebSearchMaxBytes` 计入，调用结束才归还。
- 请求是 `GET {base}/search?q=...&format=json`，`Accept: application/json`，配了 key 时带 `Authorization: Bearer`。响应必须是 200，正文最多 4 MiB；只收 `http`/`https` 且有 URL 的条目，按 URL 去重，标题截到 256 字节、摘要截到 1024 字节，按 UTF-8 边界截断。
- 查询词、响应正文和密钥都不进错误和日志；错误只说明类别（`ErrUpstream`、`ErrNoQuery`、超时）。搜索失败在网关里是普通上游失败：没有模型调用、没有模型用量，也不收这次搜索。

## 部署 SearXNG

SearXNG 默认只开 HTML，必须显式打开 JSON：

```yaml
search:
  formats: [html, json]
server:
  secret_key: "<random>"
  limiter: false
```

没有 `server.secret_key` 时 SearXNG 会静默忽略配置（包括 JSON 格式），所以这是必需的。`limiter: false` 让同一个节点的高并发请求不被本机的限流器拒绝；真正的入口限制由前面的反向代理承担。容器测试用 `json_engine` 指向一个本地 nginx 作为记录响应源，不需要外网（`container_test.go`）。

## 测试

- `websearch_test.go` 用录制的 SearXNG 响应（`testdata/searxng.json`）在 `httptest` TLS 服务器上验证路径、`format=json`、`site:` 参数、bearer、去重和截断；失败用例覆盖非 200、非 JSON、超限正文，并断言查询和正文不回显。
- `container_test.go` 起真实的 `searxng/searxng` 容器（镜像版本在文件顶部）和 nginx 侧车；它需要容器运行时，按仓库规则在 `flock /tmp/axiom-remote-check.lock` 下跑。
- 网关的 `TestWebPluginSearchesInjectsAndAnnotates` 和 `TestWebPluginSearchFailureSearchesNothingElse` 覆盖注入、`url_citation`、按次计费和搜索失败不调用上游。
- 上线前对真实 SearXNG 的活检查见 #497：在后台填 URL 和 key，保存并重启，用一个 `plugins:[{id:"web"}]` 的 Chat 请求确认回复里有结果、非流式回复带 `url_citation`、调用记录里 `web_search` 计一次；再清空 URL 确认回到 400。

## 已知边界

- 只有 Chat 插件；Messages 和 Responses 的 `plugins` 仍拒绝。
- `include_domains` 依赖所选引擎是否理解 `site:` 操作符；不理解时结果不会过滤，但查询仍合法，不会把操作符当正文转发给模型。
- 流式回复没有整条消息，不补 `url_citation`；结果 URL 已写在注入的 system 消息里，正文里的链接由模型自己写。
- 结果文本是外部内容，按不可信数据处理：只做长度和字符集边界，不执行其中的指令；注入的 system 消息措辞明确要求引用来源。
- SearXNG 页面本身可能返回 `answers`、`infoboxes`、`suggestions`；这里只用 `results`。
