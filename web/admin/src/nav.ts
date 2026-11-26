import type { GlyphName } from "@axiom/ui";
import type shell from "./locales/zh/shell";

// The admin's navigation as the admin board draws it (adNavDef, adSetDef and adPhoneTabs in admin_core.js; the table in
// ADMIN_HOWTO「导航怎么分」): four named groups in the sidebar, and the system settings, whose pages replace the
// sidebar with their own five groups. Each design page points at the route that serves it today; the routes the design
// folds into a page's tabs belong to that page's entry (also), which they mark as current and under which ⌘K finds
// them, until the page's own tabs link them. An entry shows only if the session holds its permission, and a group with
// no entry left is hidden. The server checks the permission again on every request.

// What waits on a page, counted from the queries the shell already has (pending.ts).
export type Pending = "alerts" | "approvals" | "payments" | "mappings";

// label names the route in ⌘K (shell.also); a route without one is a detail page, found from its list.
type Also = { to: string; label?: keyof typeof shell.also };
export type NavEntry = {
  key: keyof typeof shell.nav;
  to: string;
  icon: GlyphName;
  permission: string;
  pending?: Pending;
  also?: readonly Also[];
};
type Group<K> = { key: K; entries: readonly NavEntry[] };

export const groups: readonly Group<keyof typeof shell.groups>[] = [
  {
    key: "run",
    entries: [
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
    ],
  },
  {
    key: "people",
    entries: [
      { key: "users", to: "/users", icon: "team", permission: "users.view", also: [{ to: "/accounts" }] },
      // 订阅管理 (subs) has no page yet; F6 adds it, and /subscription-plans moves under it.
      {
        key: "orders",
        to: "/orders",
        icon: "wallet",
        permission: "sites.manage",
        pending: "payments",
        also: [{ to: "/payment-discrepancies", label: "discrepancies" }],
      },
      { key: "redeem", to: "/redeem-codes", icon: "ticket", permission: "sites.manage" },
    ],
  },
  {
    key: "supply",
    entries: [
      {
        key: "suppliers",
        to: "/suppliers",
        icon: "byok",
        permission: "suppliers.view",
        pending: "mappings",
        also: [{ to: "/mappings", label: "mappings" }],
      },
      { key: "models", to: "/models", icon: "models", permission: "catalog.view" },
      {
        key: "routing",
        to: "/routing-presets",
        icon: "route",
        permission: "sites.manage",
        also: [{ to: "/source-classes", label: "sourceClasses" }],
      },
      {
        key: "pricing",
        to: "/pricing",
        icon: "classify",
        permission: "pricing.view",
        also: [
          { to: "/exchange-rates", label: "exchangeRates" },
          { to: "/subscription-plans", label: "subscriptionPlans" },
        ],
      },
      // 插件 (plugins) has no page yet; F9 adds it.
    ],
  },
  {
    key: "records",
    entries: [
      { key: "announcements", to: "/announcements", icon: "speak", permission: "sites.manage" },
      { key: "calls", to: "/calls", icon: "logs", permission: "calls.search" },
      { key: "audit", to: "/audit", icon: "docs", permission: "audit.view" },
    ],
  },
];

export const settingsEntry: NavEntry = {
  key: "settings",
  to: "/settings",
  icon: "settings",
  permission: "sites.manage",
};

// The settings' five groups. The pages F10 splits out of today's one settings page (登录方式, 注册, 会话与登录保护,
// Key 与工作区, 充值与商户, 邮件) are on /settings until then, so their groups show only what has a route of its own;
// 主题 is not built (#948).
export const settingsGroups: readonly Group<keyof typeof shell.settingsGroups>[] = [
  {
    key: "site",
    entries: [
      { key: "site", to: "/settings", icon: "spaces", permission: "sites.manage" },
      { key: "legal", to: "/legal-documents", icon: "docs", permission: "sites.manage" },
    ],
  },
  { key: "limits", entries: [{ key: "rateLimits", to: "/rate-limits", icon: "budget", permission: "sites.manage" }] },
  { key: "services", entries: [{ key: "gateway", to: "/retention", icon: "status", permission: "sites.manage" }] },
  {
    key: "running",
    entries: [
      { key: "alertRules", to: "/alert-configuration", icon: "bell", permission: "sites.manage" },
      {
        key: "agent",
        to: "/operations-credentials",
        icon: "presets",
        permission: "operations.manage",
        also: [{ to: "/operations-limits", label: "operationsLimits" }],
      },
    ],
  },
];

// Every page the navigation reaches, for the first page a session may open (pages/home.tsx).
export const pages = [...groups, ...settingsGroups].flatMap((group) => group.entries);

// The phone's bottom entries: the first four of these the session may open (adPhoneTabs), then 更多.
export const phoneTabs = [
  "overview",
  "ops",
  "approvals",
  "users",
  "calls",
] as const satisfies readonly NavEntry["key"][];
