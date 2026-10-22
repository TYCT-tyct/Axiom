export default {
  description: "站点给用户的会话和 key 的设置。没填的项用代码里的默认值，所以这里只记录站点有意改动的。",
  title: "会话与 key",
  own: "站点设定",
  default: "默认值",
  raw: "（{{seconds}} 秒）",
  edit: "修改设置",
  intro: "留空的项恢复成默认值。更短的会话时间对所有会话从下一次请求起生效。修改会写进审计日志。",
  save: "保存设置",
  saved: "设置已保存。",
  field: {
    console_idle_timeout_seconds: "控制台：闲置多久后会话失效",
    console_max_age_seconds: "控制台：会话最长保持多久",
    admin_idle_timeout_seconds: "管理后台：闲置多久后会话失效",
    admin_max_age_seconds: "管理后台：会话最长保持多久",
    new_key_daily_limit: "新 key 的每日花费上限",
  },
  hint: "单位是秒，范围 {{min}} 到 {{max}}。留空用默认值（{{default}}）。",
  limitHint: "金额（{{currency}}），0 到 1,000,000，最多 9 位小数。留空用默认值。",
  invalidRange: "时间要填整数秒，并且在提示的范围内。",
  invalidLimit: "新 key 的每日上限要填 0 到 1,000,000 之间的{{currency}}金额，最多 9 位小数。",
  keyLimits: {
    title: {
      site: "本站账户的 key 限额",
      account: "这个账户的 key 限额",
    },
    field: {
      max_keys: "最多保有多少把调用 key",
      creations_per_hour: "每小时最多创建多少把调用 key",
    },
    own: "在这里设置",
    from: { site: "默认值", account: "沿用本站" },
    edit: "修改 key 限额",
    save: "保存 key 限额",
    saved: "key 限额已保存。",
    intro: {
      site: "本站所有账户都按这里的限额，除非账户自己另有设置。留空就回到代码里的默认值。调低不会删除已有的 key，超过的账户只是不能再建。修改会记进审计日志。",
      account:
        "这个账户自己的限额，盖过本站的。留空就沿用本站的。调低不会删除已有的 key，账户只是不能再建。修改会记进审计日志。",
    },
    hint: {
      site: "整数，范围 {{min}} 到 {{max}}。留空用代码里的默认值。",
      account: "整数，范围 {{min}} 到 {{max}}。留空沿用本站的。",
    },
    invalid: "限额要填整数，并且在 {{min}} 到 {{max}} 之间。",
  },
};
