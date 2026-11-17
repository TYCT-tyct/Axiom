// The overview board's own sentences: the generated region supplies its labels, and these carry the numbers the page
// puts beside them. They live here because the root locale files are at the code-line limit in scripts/limits.ts.
import type { Messages } from "@axiom/ui";

export const zh = {
  loadingKeys: "正在加载 Key…",
  keysFailed: "Key 加载失败",
  copied: "已复制",
  copyFailed: "复制失败",
  copyUnavailable: "接入地址不可用",
  copyFailedBody: "复制失败，请手动选择下面的地址：",
  copyUnavailableBody: "接入地址还没加载出来，请稍后重试。",
  dailySpend: "每日花费明细",
  errorDetails: "报错明细",
  averagePerCall: "平均每次 {{amount}}",
  balanceNote: "账户现在能花多少",
  keysNote: "{{enabled}} 个可用，{{disabled}} 个不可用",
  chartEmpty: "本月还没有调用",
  chartNoSpend: "本月尚无花费",
  chartFailed: "每日花费加载失败",
  modelsFailed: "按模型的使用情况加载失败",
  callsFailed: "最近的报错加载失败",
};

export const en: Messages<typeof zh> = {
  loadingKeys: "Loading keys…",
  keysFailed: "Keys failed to load",
  copied: "Copied",
  copyFailed: "Copy failed",
  copyUnavailable: "Address unavailable",
  copyFailedBody: "Copy failed. Select the address below to copy it:",
  copyUnavailableBody: "The API address has not loaded yet. Try again in a moment.",
  dailySpend: "Daily spending",
  errorDetails: "Error details",
  averagePerCall: "{{amount}} per request",
  balanceNote: "Available to spend",
  keysNote: "{{enabled}} usable, {{disabled}} unusable",
  chartEmpty: "No calls this month",
  chartNoSpend: "No spending this month",
  chartFailed: "Daily spend failed to load",
  modelsFailed: "Spending by model failed to load",
  callsFailed: "Recent errors failed to load",
};
