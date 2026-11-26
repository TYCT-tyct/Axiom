export const brand = {
  preview: "浅色与深色预览",
  light: "浅色模式",
  dark: "深色模式",
  sample: "强调文字与链接",
  contrast: "文字对比度 {{text}}:1 · 填充对比度 {{solid}}:1",
  adjusted: "为了在浅色和深色页面上都达到 WCAG AA（文字 ≥4.5:1、填充 ≥3:1），已自动微调强调色。",
  unchanged: "原始品牌色在浅色与深色模式下均已满足对比度要求。",
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

export const loginLimits = {};
