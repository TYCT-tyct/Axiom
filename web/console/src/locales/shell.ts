// The shell's own words beside the board's, which its region's locales in @axiom/boards hold. Their own module, as the
// settings page's: the English root locale file is at the line limit in scripts/limits.ts.
import type { Messages } from "@axiom/ui";

export const zh = {
  path: "当前位置",
  collapse: "收起",
  expand: "展开",
  balanceUnavailable: "暂不可用",
  callsPaused: "站点已暂停这个账户的调用，所有 key 暂时不能调用模型。请联系站点管理员。",
};

export const en: Messages<typeof zh> = {
  path: "Location",
  collapse: "Collapse",
  expand: "Expand",
  balanceUnavailable: "Unavailable",
  callsPaused:
    "The site has paused this account's calls, so every key's calls are refused; signing in, paying and managing keys still work. Contact the site's administrator.",
};
