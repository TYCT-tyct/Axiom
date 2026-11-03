import type { IconName } from "@axiom/ui";

// The pages of the shell, in the order the sidebar lists them. A page shows for a session that holds its permission
// (the server decides again on every request); home is the operations dashboard, and a visitor without its permission
// is sent to the first page they may open.
export const nav = [
  { to: "/", label: "nav.overview", icon: "grid", permission: "sites.manage" },
  { to: "/users", label: "nav.users", icon: "card", permission: "users.view" },
  { to: "/orders", label: "nav.orders", icon: "card", permission: "sites.manage" },
  { to: "/redeem-codes", label: "nav.redeemCodes", icon: "card", permission: "sites.manage" },
  { to: "/calls", label: "nav.calls", icon: "list", permission: "calls.search" },
  { to: "/audit", label: "nav.audit", icon: "search", permission: "audit.view" },
  { to: "/suppliers", label: "nav.suppliers", icon: "cube", permission: "suppliers.view" },
  { to: "/mappings", label: "nav.mappings", icon: "list", permission: "suppliers.view" },
  { to: "/models", label: "nav.models", icon: "grid", permission: "catalog.view" },
  { to: "/source-classes", label: "nav.sourceClasses", icon: "sliders", permission: "sites.manage" },
  { to: "/pricing", label: "nav.pricing", icon: "sliders", permission: "pricing.view" },
  { to: "/settings", label: "nav.settings", icon: "shield", permission: "sites.manage" },
] as const satisfies readonly { to: string; label: string; icon: IconName; permission: string }[];
