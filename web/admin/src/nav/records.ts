import type { NavEntry } from "../nav";

export const records: readonly NavEntry[] = [
  { key: "announcements", to: "/announcements", icon: "speak", permission: "sites.manage" },
  { key: "calls", to: "/calls", icon: "logs", permission: "calls.search" },
  { key: "audit", to: "/audit", icon: "docs", permission: "audit.view" },
];
