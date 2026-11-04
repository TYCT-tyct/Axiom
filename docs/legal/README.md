# 示例法律文本（demo）

本目录给开发站点准备了一份**演示用**的服务条款和隐私政策，中文和英文各一份，结构参照 OpenRouter 的
[Terms of Service](https://openrouter.ai/terms) 和 [Privacy Policy](https://openrouter.ai/privacy)（两页均标注
Last Updated: August 31, 2026，2026-11-04 只读核对）。它只用于演示和界面联调，**不是法律意见，也不能直接上线**。

> 每份文本的第一行都是可见横幅：
>
> `【示例文本（demo），不构成法律意见，正式上线前须由负责人和律师审定替换】`
>
> `Demo text, not legal advice; replace before launch.`

正式文本仍由负责人提供；在负责人和律师审定并替换之前，[#325](https://github.com/TYCT-tyct/Axiom/issues/325) 保持打开。

## 文件

| 文件 | 用途 |
|---|---|
| [`demo/terms.zh-en.txt`](demo/terms.zh-en.txt) | 服务条款，中文在前、英文在后 |
| [`demo/privacy.zh-en.txt`](demo/privacy.zh-en.txt) | 隐私政策，中文在前、英文在后 |
| [`demo/seed.py`](demo/seed.py) | 通过现有的后台发布接口，把两份文本发到一个开发站点；默认拒绝非 loopback 地址 |
| [`../domains/accounts.md`](../domains/accounts.md) | 站点级、按版本发布的条款和注册同意机制 |
| [`../../api/admin.yaml`](../../api/admin.yaml) | `POST /api/admin/v1/sites/{site_id}/legal-documents`，发布入口 |

## 为什么中英文放在同一份正文里

现有数据结构是每个站点、每种 `kind`（`terms` / `privacy`）各一份正文，只有版本，没有语言维度
（`accounts.legal_documents`，见 [`domains/accounts.md`](../domains/accounts.md) 和
[`queries/legal.sql`](../../internal/accounts/queries/legal.sql)）。为了不为了演示改一个以后很难回退的数据结构，
两份文本都采用“同一正文，中文在前、英文在后”，并在开头放两种语言的横幅。这样：

- 注册页无论界面语言是中文还是英文，都能看到完整的中英文文本；
- 发布仍然是一次 `POST`，版本号仍由服务端按 `(site, kind)` 递增；
- 将来如果产品真的需要按语言分别展示，再加语言维度或按站点分别发布，不需要改这份 demo 的正文内容。

## 覆盖了什么

服务条款按 OpenRouter 的章节骨架组织，保留了：服务说明、资格与账户、预付费额度、退款、上游模型条款、用户内容、
禁止行为、终止、知识产权、免责声明、责任限制、协议变更、适用法律与争议解决、一般条款。

隐私政策按 OpenRouter 的骨架组织，保留了：收集范围、使用目的、共享、用户选择、保存期限、安全、未成年人、政策更新、
联系方式；另外把 Axiom 特有的三类数据写清楚：

- **调用记录（元数据）全部保存，不能关闭**，用户可以在控制台查看和导出元数据；
- **对话正文是否保存由平台决定**，站点和账户不能单独关闭；开启后默认 90 天，可调，也可长期保存；
- **任何用户和站点管理员都不能通过产品查看正文**，只有平台负责人在双人签名、审计、加密导出的受控流程下才能访问。

金额、退款、拒付的写法对齐
[`domains/payments.md`](../domains/payments.md#退款)、[`domains/billing.md`](../domains/billing.md#额度批次) 和
[`domains/billing.md`](../domains/billing.md#风控状态)：只退付费批次中未使用、未过期、未冻结的部分；退款要审核；
默认不退渠道手续费；加密货币充值默认不退；拒付会扣回额度并暂停账户，只有操作员能解除，胜诉不自动解除。

## 与 OpenRouter 的有意差异

| OpenRouter 的做法 | 本 demo 的写法 | 原因 |
|---|---|---|
| 未使用额度只能在付款后 24 小时内申请退款（Terms §4.1） | 退款申请期限写成【退款申请期限】，并写明由站点政策决定 | Axiom 的退款由管理员/站长审核，期限是站点配置；不照搬 24 小时 |
| 额度可能在购买后 365 天过期（Terms §4.2） | 付费额度默认不过期；赠送、兑换码等按发放时说明 | 对齐 `docs/domains/payments.md` 的批次规则，不虚构统一过期日 |
| 提示和对话日志默认不记录，用户可选择加入（Terms §6.2） | 平台统一决定是否保存正文，站点和账户不能关闭；开启后默认 90 天 | 对齐 `docs/product.md` 的数据留存和 `docs/domains/observability.md` |
| 争议一般提交纽约州的有约束力仲裁（Terms §19） | 适用法律、管辖法院/仲裁机构留【】占位 | 运营主体和法域未定，不能替负责人选 |
| 模型条款主要面向 OpenRouter 的模型提供方 | 明确写到官方 API、第三方中转站、自有号池，以及匿名端点代号、BYOK、子站点 | Axiom 的转售形态和 OpenRouter 不同 |
| 账户以个人/组织为主 | 写到账户、工作区、成员、平台角色和子站点运营者 | Axiom 有平台/站点/账户三层 |

OpenRouter 的付款、退款、用户内容、终止、免责、责任限制、协议变更等条款的写法只作为结构和基线；所有金额、期限、
主体、法域和联系信息都必须由负责人和律师按实际业务替换。

## 发布到开发站点

不要发布到生产站点。`seed.py` 默认只接受 loopback 的后台地址；`--allow-nonlocal` 只用于你确认过的开发机。

1. 启动本机栈（会构建前端并启动 PostgreSQL、Valkey、程序）：

   ```sh
   just up
   ```

2. 初始化演示站点。控制台、API、后台必须是三个不同的 host：

   ```sh
   docker compose -f deploy/compose.yaml exec axiom \
     /axiom site init --name demo --currency CNY \
     --console localhost:8080 --api 127.0.0.1:8080 --admin localhost:8082
   ```

   记下输出的 `site` ID。

3. 建一个平台管理员。密码从标准输入读，不写进 shell 历史：

   ```sh
   printf '%s\n' 'choose-a-long-demo-password' | \
     docker compose -f deploy/compose.yaml exec -T axiom \
     /axiom user create --email admin@example.test
   docker compose -f deploy/compose.yaml exec axiom \
     /axiom user grant-platform --email admin@example.test --role admin
   ```

4. 用管理员账号发布两份 demo 文本。脚本会自动完成 TOTP 注册（后台发布要求 MFA 会话）：

   ```sh
   AXIOM_ADMIN_URL=http://localhost:8082 \
   AXIOM_SITE_ID=site_... \
   AXIOM_EMAIL=admin@example.test \
   AXIOM_PASSWORD='choose-a-long-demo-password' \
   python3 docs/legal/demo/seed.py
   ```

   脚本先做离线检查（横幅、大小），再登录、按需注册 TOTP、逐份 `POST`，打印发布后的版本号；首次为账号开启
   TOTP 时会把 secret 打印到 stderr，保存它。如果账号已经开过 MFA，设置 `AXIOM_TOTP_SECRET` 为当前的 base32
   secret，脚本会用 TOTP 通过第二步验证。

5. 验证公开接口：

   ```sh
   curl -s http://localhost:8080/api/console/v1/legal | jq
   ```

   应看到 `terms` 和 `privacy` 各一个版本，正文以 demo 横幅开头。打开 `http://localhost:8080/console/` 的注册页，
   应看到两份文本和同意勾选框；已登录用户的当前接受版本落后时，控制台会先跳到 `/console/consent`，同意后才能继续。

## 负责人替换时要改什么

- 所有【】占位：运营主体名称、注册地址、联系方式、适用法律、管辖法院/仲裁机构、退款申请期限、发票政策、
  数据保护负责人、跨境传输保障、最低年龄、日志保留期限、责任上限、通知期限、语言优先级、生效日期。
- 退款政策：手续费是否退、加密货币是否退、申请期限、审核流程；必须和站点后台配置、`docs/domains/payments.md`
  的实际行为一致。
- 数据留存：对话正文是否开启、保留天数、训练许可；必须和 `docs/product.md`、`docs/domains/observability.md`
  以及后台配置一致。
- 上游名单和数据实践：官方 API、第三方中转站、自有号池的实际范围和所在地区。
- 子站点和 BYOK 是否开放，以及对应的费率、责任和数据处理安排。

替换后仍应保留“demo”横幅，直到负责人和律师书面确认正式版本；正式发布前把新正文作为新版本发布，不要修改旧版本
（版本和正文不可变，见 `domains/accounts.md`）。
