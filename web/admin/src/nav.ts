// The console board's entries the admin's pages borrow their icons from, until the admin has its own design: on the
// phone the first four are tabs and the rest are in the sheet of more, so a page's icon puts it in one or the other.
export const tabIcons = ["ov", "usage", "logs", "keys"] as const;
export const sheetIcons = ["route", "presets", "plugins", "byok", "budget", "observe", "bill", "mkeys"] as const;
type Icon = (typeof tabIcons)[number] | (typeof sheetIcons)[number];

export const sections = [
  "overview",
  "users",
  "supply",
  "catalog",
  "pricing",
  "routing",
  "payments",
  "operations",
  "alerts",
  "site",
  "platform",
  "calls",
  "audit",
  "security",
] as const;

type Section = (typeof sections)[number];

// The server checks the permission again on every request. Detail pages belong to their list entry.
export const nav = [
  { section: "overview", to: "/", label: "nav.overview", icon: "ov", permission: "sites.manage" },
  { section: "users", to: "/users", label: "nav.users", icon: "mkeys", permission: "users.view" },
  { section: "supply", to: "/suppliers", label: "nav.suppliers", icon: "byok", permission: "suppliers.view" },
  { section: "supply", to: "/mappings", label: "nav.mappings", icon: "route", permission: "suppliers.view" },
  { section: "catalog", to: "/models", label: "nav.models", icon: "plugins", permission: "catalog.view" },
  { section: "pricing", to: "/pricing", label: "nav.pricing", icon: "bill", permission: "pricing.view" },
  {
    section: "routing",
    to: "/routing-presets",
    label: "nav.routingPresets",
    icon: "presets",
    permission: "sites.manage",
  },
  {
    section: "routing",
    to: "/source-classes",
    label: "nav.sourceClasses",
    icon: "presets",
    permission: "sites.manage",
  },
  { section: "payments", to: "/orders", label: "nav.orders", icon: "bill", permission: "sites.manage" },
  { section: "payments", to: "/redeem-codes", label: "nav.redeemCodes", icon: "keys", permission: "sites.manage" },
  { section: "operations", to: "/approvals", label: "nav.approvals", icon: "budget", permission: "operations.approve" },
  { section: "alerts", to: "/alert-events", label: "nav.alertEvents", icon: "observe", permission: "sites.manage" },
  {
    section: "alerts",
    to: "/alert-configuration",
    label: "nav.alertConfig",
    icon: "budget",
    permission: "sites.manage",
  },
  { section: "site", to: "/settings", label: "nav.settings", icon: "mkeys", permission: "sites.manage" },
  { section: "site", to: "/legal-documents", label: "nav.legalDocs", icon: "plugins", permission: "sites.manage" },
  { section: "site", to: "/announcements", label: "nav.announcements", icon: "observe", permission: "sites.manage" },
  { section: "platform", to: "/retention", label: "nav.retention", icon: "observe", permission: "sites.manage" },
  { section: "platform", to: "/rate-limits", label: "nav.rateLimits", icon: "budget", permission: "sites.manage" },
  { section: "calls", to: "/calls", label: "nav.calls", icon: "logs", permission: "calls.search" },
  { section: "audit", to: "/audit", label: "nav.audit", icon: "observe", permission: "audit.view" },
] as const satisfies readonly { section: Section; to: string; label: string; icon: Icon; permission: string }[];
