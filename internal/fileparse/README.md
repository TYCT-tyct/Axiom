# fileparse

`file-parser` 插件的 `mistral-ocr` 和 `cloudflare-ai` 两个引擎：网关在准入之前把请求里的内联文件交给部署配置的 Docling serve 转成文本，用文本替换文件部件，再按替换后的正文报价、路由和调用。没有配置后端时这个包什么也不做，请求保持原来的 400 拒绝。

## 职责与边界

只有一件事：一个文档进，有界的文本和页数出。`Parser` 是网关看到的全部；`Docling` 是当前唯一的实现。网关负责插件选项、文件定位与替换、内存预留、注解和计费（`mistral-ocr` 的每一页一个 `file_parse` 按次单位，`cloudflare-ai` 免费，`native` 按输入 token；开关关闭时仍按旧规则每个文件一次 `web_search`）；本包负责 URL、密钥、请求形状、页数与文本上限、响应大小和失败分类。

不在这里：`native` 引擎（文件原样发给接受文档的端点，见 [protocol 的「请求摘要」](../protocol/README.md)）、把解析结果写回各格式的正文（`protocol.RewriteFiles`）、按次收费（[billing 的「预留与结算」](../../docs/domains/billing.md#预留与结算)）、后台配置的存取（`settings.go`，与搜索后端同一套）。

## 配置

| 变量 | 默认 | 说明 |
|---|---|---|
| `AXIOM_FILE_PARSER_URL` | — | Docling serve 的基地址，如 `http://docling:5001`；必须是 `http(s)`、有主机、不带账号、查询串和片段 |
| `AXIOM_FILE_PARSER_KEY` | — | `DOCLING_SERVE_API_KEY` 打开时校验的 `X-Api-Key`。可以只设 URL（不认证），但不能只设 key |

数据库没有覆盖时环境变量生效。管理员保存的覆盖存在 `platform.file_parser_config`（迁移 `00026_file_parser_config.sql`），与 SMTP、搜索配置同样整包信封加密：AAD 是表名、全零单例 ID 和 `purpose=config`，纳入主密钥轮换清单。显式清空 URL 也会存一行，等价于管理员关掉文件解析，不会回落到环境变量。保存后 API 返回 `restart_required`，节点重启时读取覆盖并重建后端；运行中的节点不换后端。

保存时检查 URL 能否作为出网目标：全部 DNS 答案必须是公网地址，回环、私网、云元数据地址和混合答案拒绝。运行时的每次解析再由节点共享的 `egress.Client` 重新解析并校验。密钥只进加密行，不返回、不写审计、不进日志；省略密钥只在 URL 未变时保留，改 URL 必须重填。

## 一次解析

- 请求：`POST {base}/v1/convert/source`，`Content-Type: application/json`，配了 key 时带 `X-Api-Key`。开关打开时正文是 `{"sources":[{"kind":"file","base64_string":"…","filename":"scan.pdf"}],"options":{"to_formats":["md"],"do_ocr":true,"md_page_break_placeholder":"…"}}`，页数由占位符在返回的 markdown 里出现的次数加一得到，占位符由文件字节的 SHA-256 前缀生成，数完从正文里删掉；关闭时请求不带这个选项，旧版 Docling 收到的请求和以前一样，`Parse` 返回一页，网关不使用这个计数。打开计费前要确认 Docling serve 支持 `md_page_break_placeholder`（[docling-serve PR #194](https://github.com/docling-project/docling-serve/pull/194)）；不支持的旧后端会忽略这个选项，所有文档都只算一页，这是上线前活检查要确认的。`target` 省略即 `inbody`，文本在响应里回来。图片不导出：模型读的是文本，页面图片只会按输入 token 收费。
- 响应：`{"status":"success","document":{"md_content":"…"}}`。`status` 是 `failure` 或没有 `document` 算失败；正文最多 4 MiB（`ResponseLimit`），文本最多 2 MiB（`MaxTextBytes`），不是合法 UTF-8 或去掉空白后为空也拒绝（`ErrUpstream`、`ErrTooLarge`、`ErrEmpty`）。
- 行为参考 Docling serve 的 v1 API（Docling serve《Usage》《v1 migration》，读于 2026-11-01）：`kind:"file"` 的内联来源、`to_formats`、`do_ocr`、`target:"inbody"`。文件名带扩展名，Docling 按扩展名选转换器；网关在没有文件名时按媒体类型补（`protocol` 的 `fileExt`）。
- 健康检查是 `GET {base}/health`，后台的「运行测试」只做这一件事：配置测试没有文档可转，造一个合成文档测的是 Docling 的模型而不是部署的接线。
- 文件内容、文本、响应正文和密钥都不进错误和日志；错误只说明类别（`ErrUpstream`、`ErrTooLarge`、`ErrEmpty`、超时）。超时由调用方设置，网关用 `Timeout`（60 秒，整个解析步骤共用）。

## 部署 Docling serve

Docling 是 MIT 许可、可自托管的文档转换服务，CPU 也能跑，官方镜像 `ghcr.io/docling-project/docling-serve`：

```sh
docker run -p 5001:5001 ghcr.io/docling-project/docling-serve
# 后台填 http://docling:5001；要认证时加 -e DOCLING_SERVE_API_KEY=... 并在后台填同一个 key
```

首次启动会下载模型。离线或中国大陆部署要把模型预装进镜像，必要时用 `HF_ENDPOINT` 指向可用的镜像站；OCR 默认走 CPU，扫描件多的部署按解析并发给足 CPU 和内存。服务放在内网即可：节点经 `egress.Client` 访问，地址必须是公网可解析的，内网地址要在部署层用可解析的域名指向，或把 Docling 放在带公网地址的反向代理后面。

## 测试

- `fileparse_test.go` 用 `httptest` TLS 服务器验证请求形状（路径、`X-Api-Key`、base64、文件名、`to_formats`、`do_ocr`）和文本读取；失败用例覆盖非 200、非 JSON、`failure`、非 UTF-8、空文本、文本超限、正文超限、超时，并断言文件和正文不回显。`TestConfig` 覆盖 `/health`。
- 网关的 `TestParseFiles*` 覆盖替换、注解跳过、`native` 不解析、无后端 400、后端失败 502、超限 413、文件数上限、不可解析位置 400 和 `file_id` 引用保留；`TestParsesAreChargedAsFileParses` 覆盖按页 `file_parse` 计费，`TestFileParseKeepsSearchBillingUntilTheSwitchIsOn` 覆盖开关关闭时的旧 `web_search` 单位，`TestCloudflareParsingIsFree` 覆盖免费引擎；`TestFileParserThroughTheHandler` 经真实 handler（数据库、准入、结算）验证转发正文、一次预留、一次结算、`server_tool_cost` 与回复注记，`TestFileParserUnconfiguredThroughTheHandler` 验证未配置后端的 400，`TestAParseIsRefusedBeforeTheBackend` 验证 key 检查在解析之前。
- 真实 Docling 的容器测试没有做：镜像要下载数百 MB 到数 GB 的模型，按仓库规则不适合放进 `just test`。上线前对真实 Docling serve 的活检查：后台填地址、保存并重启，用一个带扫描 PDF 的 `plugins:[{id:"file-parser","pdf":{"engine":"mistral-ocr"}}]` Chat 请求确认回复正常、上游正文里是文本而不是文件、调用记录里 `file_parse` 的页数与 PDF 页数一致（没有内容的页可能少计，见上）；再把注解放回历史确认不再计费。

## 已知边界

- 只解析消息顶层的文件部件：Chat 的 `file`、Responses 的 `input_file`、Messages 的 `document` 的 base64 源。工具结果里的文档、`source.type` 为 `url` 或 `text` 的 Messages 文档不解析，请求要求解析却仍有这类文件时 400，而不是把文件悄悄转发；只带 `file_id` 的引用原样保留，由路由按 `provider.only` 固定供应商。
- 只接受内联的 base64 或 data URL；同时带 `file_data` 和 `file_id` 的部件拒绝，和格式转换的规则一致。
- 一次请求最多 8 个文件、每个 20 MiB，替换进正文的文本合计最多 2 MiB，同一个文件每出现一次都算一份；网关按 `2×文件大小 + ResponseLimit` 和正文增量计入节点预算，申请不到 503。
- 解析在准入与预留之前，所以解析失败或超限不会调用模型，也不会预留。发文件之前网关先做一次只读的 key 检查（停用、过期、IP 白名单、速率），明显不能用的 key 在解析前就被拒绝；余额和预算要看报价，只能在准入里判断，所以余额不足时解析已经发生，这部分工作不收钱。
- 只在非流式 Chat 回复里返回 `file` 注解（成功和失败体都带）；流式回复到结束才知道整条消息，`file-parser` 的流式请求不返回注解，客户端要跳过一次解析需用非流式回复里的注解。
- 引擎名沿用 OpenRouter 的 `mistral-ocr` / `cloudflare-ai`，但两者都落到本站配置的同一个后端：本站没有 Mistral 和 Cloudflare 的账户，也不把文件发给它们。
