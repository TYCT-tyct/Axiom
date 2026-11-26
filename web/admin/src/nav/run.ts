import type { NavEntry } from "../nav";

export const run: readonly NavEntry[] = [
  { key: "overview", to: "/", icon: "overview", permission: "sites.manage" },
  {
    key: "ops",
    to: "/monitor",
    icon: "status",
    permission: "sites.manage",
    pending: "alerts",
    also: [{ to: "/alert-events", label: "alerts" }],
  },
  { key: "approvals", to: "/approvals", icon: "privacy", permission: "operations.approve", pending: "approvals" },
];
