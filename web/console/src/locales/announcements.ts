// The site announcements' messages, in their own module because the English root locale file is at the code-line limit
// in scripts/limits.ts; a label and its translation are edited as a pair, so both languages stay together.
import type { Messages } from "@axiom/ui";

export const zh = {
  title: "站内公告",
  loading: "正在加载公告…",
  unavailable: "公告暂不可用。",
  error: "公告加载失败，请重试。",
  retry: "重试",
  empty: "暂无有效公告。",
};

export const en: Messages<typeof zh> = {
  title: "Site announcements",
  loading: "Loading announcements…",
  unavailable: "Announcements are currently unavailable.",
  error: "Could not load announcements. Try again.",
  retry: "Try again",
  empty: "No active announcements.",
};
