// The console board's entries the admin's pages borrow their icons from, until the admin has its own design: on the
// phone the first four are tabs and the rest are in the sheet of more, so a page's icon puts it in one or the other.
export const tabIcons = ["ov", "usage", "logs", "keys"] as const;
export const sheetIcons = ["route", "presets", "plugins", "byok", "budget", "observe", "bill", "mkeys"] as const;
type Icon = (typeof tabIcons)[number] | (typeof sheetIcons)[number];

// The pages of the shell, in the order the sidebar lists them. A page shows for a session that holds its permission
// (the server decides again on every request); home is the operations dashboard, and a visitor without its permission
// is sent to the first page they may open.
export const nav = [
  { to: "/", label: "nav.overview", icon: "ov", permission: "sites.manage" },
  { to: "/approvals", label: "nav.approvals", icon: "budget", permission: "operations.approve" },
  { to: "/users", label: "nav.users", icon: "mkeys", permission: "users.view" },
  { to: "/orders", label: "nav.orders", icon: "bill", permission: "sites.manage" },
  { to: "/redeem-codes", label: "nav.redeemCodes", icon: "keys", permission: "sites.manage" },
  { to: "/calls", label: "nav.calls", icon: "logs", permission: "calls.search" },
  { to: "/audit", label: "nav.audit", icon: "observe", permission: "audit.view" },
  { to: "/suppliers", label: "nav.suppliers", icon: "byok", permission: "suppliers.view" },
  { to: "/mappings", label: "nav.mappings", icon: "route", permission: "suppliers.view" },
  { to: "/models", label: "nav.models", icon: "plugins", permission: "catalog.view" },
  { to: "/source-classes", label: "nav.sourceClasses", icon: "presets", permission: "sites.manage" },
  { to: "/routing-presets", label: "nav.routingPresets", icon: "presets", permission: "sites.manage" },
  { to: "/pricing", label: "nav.pricing", icon: "bill", permission: "pricing.view" },
  { to: "/settings", label: "nav.settings", icon: "mkeys", permission: "sites.manage" },
] as const satisfies readonly { to: string; label: string; icon: Icon; permission: string }[];
