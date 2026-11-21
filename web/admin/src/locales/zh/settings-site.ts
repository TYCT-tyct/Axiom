export const brand = {
  title: "站点品牌色",
  unavailableLabel: "品牌色",
  unavailable: "暂时看不到：这台服务器还没有站点品牌色设置。这里其他内容照常可用。",
  color: "品牌色",
  default: "默认配色（#00a2c7）",
  preview: "浅色与深色预览",
  light: "浅色模式",
  dark: "深色模式",
  sample: "强调文字与链接",
  contrast: "文字对比度 {{text}}:1 · 填充对比度 {{solid}}:1",
  adjusted: "为了在浅色和深色页面上都达到 WCAG AA（文字 ≥4.5:1、填充 ≥3:1），已自动微调强调色。",
  unchanged: "原始品牌色在浅色与深色模式下均已满足对比度要求。",
  edit: "修改品牌色",
  editTitle: "修改站点品牌色",
  intro:
    "填写 #rrggbb 十六进制颜色，控制台与官网会自动推导出浅色与深色两套强调色；留空恢复默认配色。修改会记进审计日志。",
  hint: "格式为 #rrggbb，例如 #00a2c7；留空恢复默认配色。",
  invalid: "请填写 #rrggbb 格式的颜色（例如 #00a2c7），或留空恢复默认配色。",
  save: "保存品牌色",
  saved: "站点品牌色已设为 {{color}}。",
  cleared: "已恢复默认配色。",
};

export const workspaceLimit = {
  title: {
    site: "本站账户的工作区上限",
    account: "这个账户的工作区上限",
  },
  field: "最多可创建的工作区数",
  unavailableLabel: "工作区上限",
  unavailable: "暂时看不到：这台服务器还没有工作区上限。这里其他内容照常可用。",
  own: "在这里设置",
  from: { site: "默认值", account: "沿用本站" },
  edit: "修改工作区上限",
  save: "保存工作区上限",
  saved: "工作区上限已保存。",
  intro: {
    site: "本站所有账户都按这里的上限，除非账户自己另有设置。留空回到代码默认值（50）。调低不会停用已有工作区，只是不能再新建。修改会记进审计日志。",
    account:
      "这个账户自己的上限，覆盖本站设置。留空沿用本站的上限。调低不会停用已有工作区，只是不能再新建。修改会记进审计日志。",
  },
  hint: {
    site: "整数，范围 {{min}} 到 {{max}}。留空用代码默认值。",
    account: "整数，范围 {{min}} 到 {{max}}。留空沿用本站的上限。",
  },
  invalid: "工作区上限要填 {{min}} 到 {{max}} 之间的整数。",
};

export const loginLimits = {
  title: "登录尝试限制",
  unavailableLabel: "登录限制",
  unavailable: "暂时看不到：这台服务器还没有登录尝试限制。这里其他内容照常可用。",
  field: {
    ip_attempt_burst: "单 IP 突发尝试次数",
    ip_attempts_per_minute: "单 IP 每分钟尝试次数",
    email_sign_in_failures: "同邮箱登录失败上限",
    user_code_failures: "验证码失败上限",
    challenge_code_attempts: "单次挑战验证码尝试上限",
  },
  edit: "修改登录限制",
  save: "保存登录限制",
  saved: "登录尝试限制已保存。",
  intro: "按 IP、邮箱和验证挑战限制登录尝试。留空的项回到代码里的默认值。修改会记进审计日志。",
  hint: "整数，范围 {{min}} 到 {{max}}。留空用默认值（{{default}}）。",
  invalid: "请填写各项提示范围内的整数，或留空使用默认值。",
};
