// The shell's own words beside the board's, which its region's locales in @axiom/boards hold. Their own module, as the
// settings page's: the English root locale file is at the line limit in scripts/limits.ts.
import type { Messages } from "@axiom/ui";

export const zh = {
  path: "当前位置",
  collapse: "收起",
  expand: "展开",
  balanceUnavailable: "暂不可用",
};

export const en: Messages<typeof zh> = {
  path: "Location",
  collapse: "Collapse",
  expand: "Expand",
  balanceUnavailable: "Unavailable",
};
