# 发布前

发布前逐项确认，本文写其中和模型目录有关的一步；其余按 project-workflow 的发布方法。部署到服务器要先问负责人（[CONTRIBUTING](../../CONTRIBUTING.md)），本文的写库步骤改的是线上目录，同样先问。

## 目录的规格要补全

**为什么**：规格里说请求怎么写给模型的键（`thinking`、`mid_system`、`untrusted_system_after_tools`、`default_sampling_only`、`forced_tool_choice`、`cache_breakpoints`、`encrypted_reasoning`）缺了不是 no。Chat 请求转给 Claude 模型时，用到缺的那个键的请求被拒（对客户端是 400「模型暂不支持该参数」），换完所有端点也一样，所以发布一个新的转换逻辑之前，库里每个已进目录的模型都要有这些键；规格解析不了的行（例如 `"thinking": true`）还会让 `axiom serve` 加载快照时直接退出。键的含义、每个型号的值和出处见[目录模块的「请求写法的键」](../../internal/catalog/README.md#请求写法的键值照厂商文档不手填)。

**步骤**：在要发布的提交的检出里，`AXIOM_DATABASE_URL` 指向目标库（连接串不进仓库）：

1. 只读查询，找类型不对的值，应当没有结果（有结果就是 `check` 会报「规格解析不了」的行，先用 `axiom catalog update-spec` 改对）：

   ```sql
   SELECT m.slug, k.key, jsonb_typeof(m.spec -> k.key) AS found
   FROM catalog.model m
   CROSS JOIN unnest(ARRAY['encrypted_reasoning', 'mid_system', 'untrusted_system_after_tools',
                           'default_sampling_only', 'forced_tool_choice', 'cache_breakpoints']) AS k(key)
   WHERE jsonb_typeof(m.spec -> k.key) NOT IN ('boolean', 'null')
   UNION ALL
   SELECT slug, 'thinking', COALESCE(spec ->> 'thinking', jsonb_typeof(spec -> 'thinking'))
   FROM catalog.model
   WHERE jsonb_typeof(spec -> 'thinking') NOT IN ('null')
     AND (jsonb_typeof(spec -> 'thinking') <> 'string' OR spec ->> 'thinking' NOT IN ('adaptive', 'manual', 'none'));
   ```

2. `just catalog-dialect-plan`：不写库，每个数据文件里的模型一行：`set`（会改，带「键: 旧值 -> 新值」）、`unchanged`、`absent`（目录里没有这个 slug，只报告）、`retired`、`failed`（目录拒绝新规格，原因在行里）。看过 `set` 的改动、`failed` 的原因。
3. `just catalog-dialect-apply`：同样的一遍，真写，经 `update-spec` 的审计路径（`change`、`rev`）。再跑一次应当全是 `unchanged`。
4. `just catalog-check` 必须通过（退出码 0）：每个已进目录的模型都有它厂商要的键，规格都解析得了，价格与数据集一致。有问题就不发布。缺键的模型不在数据文件里，对着厂商文档把它补进 `deploy/catalog/dialects.json`（改动走 PR 和评审），再回到第 2 步；不要手改库里的规格绕过。

**数据文件覆盖哪些模型**：仓库里没有「哪些模型已上架」的清单（上架是各环境的运营操作），所以数据文件按厂商文档列出了 Claude 和 GPT 现行与仍可用的型号，命令只对库里 slug 完全相同的模型生效（`absent` 的不管，也不创建）。库里用了别的 slug 写法的，`check` 会报缺键，把数据文件里的 slug 改成库里的写法，或反过来统一。

**回滚**：每次写入的旧规格都在 `change` 里（`kind = 'model.update_spec'`），用 `axiom catalog update-spec` 写回。
