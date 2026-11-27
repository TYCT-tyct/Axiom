# secret

密钥加载、信封加密与主密钥轮换。`secret.Secret` 的脱敏规则见 [platform 的「不变量」](../README.md#不变量)。

## 凭证加密

- `secret.LoadMaster` 在启动时读一次 `AXIOM_MASTER_KEY_FILE`，同一快照给出 `Keyring()`、`LoginKey()`、`DeriveKey(label)`；`secret.LoadMAC` 读 API key 的 HMAC 密钥 `AXIOM_KEY_HASH_FILE`；`secret.LoadSecret` 读 `AXIOM_IDENTITY_KEY_FILE`，用最新一把派生发往上游的会话和用户标识。缺一或不合法就不启动；文件的生成与 `AXIOM_KEYRING_GENERATE` 见[生产入口](../../../docs/ops/backup-restore.md#生产入口)。
- 文件格式：每行 `版本号:base64密钥`（无前导零的正 uint32；解码后 32 字节），允许 LF/CRLF，不许空行、注释、空白、BOM、重复版本或密钥。
- `Keyring.Seal(Secret, Location)` 每次用独立的 AES-256-GCM 数据密钥加密，再用写版本包裹它；`Open` 失败不返回明文。`Location{Table, RowID, Purpose}` 由包编码进 AAD（字段为空 panic），读取时从预期位置重建，记录移位或字段被换都认证失败。一把主密钥合计最多包裹 2³² 个数据密钥，轮换要用新的随机密钥。
- 写版本记在 `platform.key_versions`（只存摘要，不存材料）。`axiom keyring add|activate|rewrap|retire|check` 管理它，步骤见[主密钥轮换](../../../docs/ops/backup-restore.md#主密钥轮换)。节点每 10 秒原子替换密钥环并在心跳里报告已核对的版本；全部在线节点报告后自动激活，按 100 个一批重包裹，零引用且所有节点写出新版本满 30 秒后退役；退役与信封写由一把 advisory 锁和 `platform.key_retirements` 互斥，迟到的旧版本写被拒。
- 信封表登记在 `cmd/axiom` 的 `secret.Rotation`、挂退役触发器，缺则不启动；未登记列不挡服务，只挡退役和重包裹收尾。
