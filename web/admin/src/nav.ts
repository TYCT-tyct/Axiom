import type { IconName } from "@axiom/ui";

// The pages of the shell, in the order the sidebar lists them. A page shows for a session that holds its permission
// (the server decides again on every request); home sends a visitor to the first one they may open.
export const nav = [
  { to: "/users", label: "nav.users", icon: "card", permission: "users.view" },
  { to: "/suppliers", label: "nav.suppliers", icon: "cube", permission: "suppliers.view" },
  { to: "/mappings", label: "nav.mappings", icon: "list", permission: "suppliers.view" },
  { to: "/models", label: "nav.models", icon: "grid", permission: "catalog.view" },
  { to: "/pricing", label: "nav.pricing", icon: "sliders", permission: "pricing.view" },
  { to: "/settings", label: "nav.settings", icon: "shield", permission: "sites.manage" },
] as const satisfies readonly { to: string; label: string; icon: IconName; permission: string }[];
