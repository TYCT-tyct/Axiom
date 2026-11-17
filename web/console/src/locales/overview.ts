// The overview board's own sentences: the generated region supplies its labels, and these carry the numbers the page
// puts beside them. They live here because the root locale files are at the code-line limit in scripts/limits.ts.
import type { Messages } from "@axiom/ui";

export const zh = {
  loadingKeys: "正在加载 Key…",
  keysFailed: "Key 加载失败",
  createFirstBody: "新建一个 Key 开始调用。",
  quotaTitle: "{{name}} 已用 {{percent}}% 额度",
  quotaBody: "{{remaining}} / {{limit}}。之后这个 Key 的请求会被拒绝。",
  noLimitTitle: "{{count}} 个 Key 都没有设置额度",
  noLimitBody: "设置上限后，这里会提醒快用完的 Key。",
  manageKeys: "管理 Key",
  averagePerCall: "平均每次 {{amount}}",
  balanceNote: "账户现在能花多少",
  keysNote: "{{enabled}} 个可用，{{disabled}} 个不可用",
  chartEmpty: "本月还没有调用",
  chartFailed: "每日花费加载失败",
  modelsFailed: "按模型的使用情况加载失败",
  callsFailed: "最近的报错加载失败",
};

export const en: Messages<typeof zh> = {
  loadingKeys: "Loading keys…",
  keysFailed: "Keys failed to load",
  createFirstBody: "Create a key to start calling.",
  quotaTitle: "{{name}} has used {{percent}}% of its quota",
  quotaBody: "{{remaining}} / {{limit}}. After that, requests with this key are refused.",
  noLimitTitle: "None of the {{count}} keys has a quota",
  noLimitBody: "Set a limit and the keys running out will be flagged here.",
  manageKeys: "Manage keys",
  averagePerCall: "{{amount}} per request",
  balanceNote: "Available to spend",
  keysNote: "{{enabled}} usable, {{disabled}} unusable",
  chartEmpty: "No calls this month",
  chartFailed: "Daily spend failed to load",
  modelsFailed: "Spending by model failed to load",
  callsFailed: "Recent errors failed to load",
};
