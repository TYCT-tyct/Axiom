# store

`suppliers` 模块的迁移、sqlc 查询和表约束；公开接口和不变量见[模块 README](../../README.md)，迁移编号与重建规则见[平台迁移说明](../../../platform/README.md#迁移)。

## 表

schema `suppliers`，版本表 `platform.goose_suppliers`，迁移只增不改。

| 表 | 键与约束 |
|---|---|
| `suppliers` | `UNIQUE (origin, upstream_account)`；`mode`（relay、direct、cloud）和 `api_format` 创建后不变；`cloud_account` 只有云平台有；归属列与 `owner_scope` 由 CHECK 保持一致；余额、倍率、阈值是有限 `numeric`；`status_reason` 至多 200 字符 |
| `supplier_logins` | PK `supplier_id`；信封加密的登录，`version` 供比较后再写 |
| `supplier_groups` | `UNIQUE (supplier_id, upstream_ref)`；代号全局唯一、设定后不变；`key_error` 与 `key_retry_at` 同空同有；`pool_*` 号池读数 |
| `egress_proxies` | 密码信封同空同有；`fallback_id` 指向另一行 |
| `credentials` | `UNIQUE (supplier_id, upstream_key_ref)`；`egress_id` 空为直连；`erased` 不能还有密文；RPM 1 到 100,000 或空；`id` 由调用方生成（加密要用行 ID）；检测结果同空同有；`UNIQUE (supplier_id, region)`，`call_names` 是这条凭证（云平台区域或直连 key）调用端点时用的模型名；`settings` 是按类型描述核对的设置；`balance_*` 是钱包 key 的余额 |
| `intake_rules`、`inbox_items` | PK `supplier_id`；`UNIQUE (supplier_id, kind, subject)` |
| `supplier_models` | PK `(supplier_group_id, upstream_name)`；`model_id` 空即待映射 |
| `supplier_aliases`、`supplier_alias_revisions` | 首版不可变，`UNIQUE NULLS NOT DISTINCT (supplier_id, supplier_group_id, upstream_name)`；修订 PK `(alias_id, revision)`，只追加 |
| `endpoints` | `UNIQUE (supplier_group_id, model_id)`；成本已知必有成本计划（`known_has_schedule`） |
| `cost_schedules` | 只追加，`UNIQUE (endpoint_id, fingerprint)` |
| `attempt_costs` | PK `(request_id, attempt_seq)`，`attempt_seq` 1 到 33；`charge_record_id` 唯一；`test` 标记检测行；索引 `(endpoint_id, finished_at)` |
| `ended_requests`、`lost_requests`、`lost_request_resolutions` | 只插入；带请求所属租户 |
| `charge_records`、`charge_events` | `UNIQUE (supplier_id, charge_ref)`；事件 `UNIQUE (charge_record_id, revision)` |
| `sync_states`、`sync_events`、`sync_plans` | 状态 jsonb 加格式号、清单位置 `listing`、栅栏、租约；事件和计划只插入 |
| `unit_values` | 只插入；`value` 在 (0, 1000]、至多 15 位小数；币种与原生单位都匹配的最新一行生效 |
| `rate_buckets` | PK `(scope, name)` |
| `usage_records` | `UNIQUE (supplier_id, request_id)`；`new`/`rejected` |
| `attempt_journal` | 旧对账日志，不再读写 |

- **行级安全**：`suppliers` 按本行归属与范围比较，平台供应商只有平台范围可见；子表策略是"所属供应商对本事务可见"，插入也挡住挂到看不见的供应商下。同步、对账、限速和采集的表是平台范围。
- **复合外键**：同时存 `supplier_id` 与 `supplier_group_id` 的表外键指向 `supplier_groups (id, supplier_id)`；`model_id` 是目录 ID，没有外键。
- **授权**：应用角色只对 `supplier_logins`（移除时）、`usage_records`、`inbox_items`（情况结束）、`egress_proxies` 有 DELETE；UPDATE 按列授予，不含 `id`、`supplier_id`、归属、`kind`、`origin`、`upstream_account`、`upstream_key_ref`。没有后台任务角色，应用角色暂能改 `attempt_costs` 与 `charge_records` 的对账列。
- **密钥**：没有明文列；数据库只查信封四列齐全和长度，写入边界只收 `secret.Sealed`。触发器 `fence_envelope_key_version` 拒绝退役的主密钥版本（[platform 的「凭证加密」](../../../platform/secret/README.md#凭证加密)）。
