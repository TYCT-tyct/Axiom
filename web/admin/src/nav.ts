import type { GlyphName } from "@axiom/ui";
import type shell from "./locales/zh/shell";
import type { SettingsPage } from "./settings-registry";

// The admin's navigation as the admin board draws it (adNavDef, adSetDef and adPhoneTabs in admin_core.js; the table in
// ADMIN_HOWTO「导航怎么分」): four named groups in the sidebar, and the system settings, whose pages replace the
// sidebar with their own five groups. Each design page points at the route that serves it today; the routes the design
// folds into a page's tabs belong to that page's entry (also), which they mark as current and under which ⌘K finds
// them, until the page's own tabs link them. An entry shows only if the session holds its permission, and a group with
// no entry left is hidden. The server checks the permission again on every request.

// What waits on a page, counted from the queries the shell already has (pending.ts).
export type Pending = "alerts" | "approvals" | "payments" | "inbox";

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
        pending: "inbox",
        also: [
          { to: "/suppliers?tab=models", label: "upstreamModels" },
          { to: "/suppliers?tab=inbox", label: "inbox" },
          { to: "/suppliers?tab=keys", label: "upstreamKeys" },
        ],
      },
      {
        key: "models",
        to: "/models",
        icon: "models",
        permission: "catalog.view",
        also: [{ to: "/models?tab=authors", label: "modelAuthors" }],
      },
      {
        key: "routing",
        to: "/routing",
        icon: "route",
        permission: "sites.manage",
        also: [
          { to: "/routing?tab=defaults", label: "routingDefaults" },
          { to: "/routing?tab=sources", label: "sourceClasses" },
        ],
      },
      {
        key: "pricing",
        to: "/pricing",
        icon: "classify",
        permission: "pricing.view",
        also: [
          { to: "/pricing?tab=rules", label: "pricingRules" },
          { to: "/pricing?tab=rates", label: "exchangeRates" },
          { to: "/pricing?tab=policy", label: "pricingPolicy" },
          { to: "/subscription-plans", label: "subscriptionPlans" },
        ],
      },
      {
        key: "plugins",
        to: "/plugins",
        icon: "plugin",
        permission: "sites.manage",
        also: [
          { to: "/plugins/web", label: "webSearch" },
          { to: "/plugins/file", label: "fileParser" },
        ],
      },
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

// The route of each settings page whose rows are registered (settings-registry.ts), and the name it has in the
// navigation: the palette sends a setting there, with its row to flash.
export const settingsRoutes = {
  site: { to: "/settings/site", nav: "site" },
  signin: { to: "/settings/sign-in", nav: "signIn" },
  registration: { to: "/settings/registration", nav: "registration" },
  session: { to: "/settings/sessions", nav: "sessions" },
  limits: { to: "/settings/limits", nav: "keysAndWorkspaces" },
  payments: { to: "/settings/payments", nav: "payments" },
  mail: { to: "/settings/mail", nav: "mail" },
  gateway: { to: "/settings/gateway", nav: "gateway" },
  alerts: { to: "/alert-configuration", nav: "alertRules" },
  agent: { to: "/settings/agent", nav: "agent" },
} as const satisfies Record<SettingsPage, { to: string; nav: keyof typeof shell.nav }>;

export const settingsEntry: NavEntry = {
  key: "settings",
  to: settingsRoutes.site.to,
  icon: "settings",
  permission: "sites.manage",
};

// The settings' five groups (adSetDef); 主题 is not built (#948).
export const settingsGroups: readonly Group<keyof typeof shell.settingsGroups>[] = [
  {
    key: "site",
    entries: [
      { key: "site", to: settingsRoutes.site.to, icon: "spaces", permission: "sites.manage" },
      { key: "legal", to: "/legal-documents", icon: "docs", permission: "sites.manage" },
    ],
  },
  {
    key: "access",
    entries: [
      { key: "signIn", to: settingsRoutes.signin.to, icon: "keys", permission: "sites.manage" },
      { key: "registration", to: settingsRoutes.registration.to, icon: "user", permission: "sites.manage" },
      { key: "sessions", to: settingsRoutes.session.to, icon: "security", permission: "sites.manage" },
    ],
  },
  {
    key: "limits",
    entries: [
      { key: "keysAndWorkspaces", to: settingsRoutes.limits.to, icon: "mkey", permission: "sites.manage" },
      { key: "rateLimits", to: "/rate-limits", icon: "budget", permission: "sites.manage" },
      { key: "payments", to: settingsRoutes.payments.to, icon: "wallet", permission: "sites.manage" },
    ],
  },
  {
    key: "services",
    entries: [
      { key: "mail", to: settingsRoutes.mail.to, icon: "mail", permission: "sites.manage" },
      { key: "gateway", to: settingsRoutes.gateway.to, icon: "status", permission: "sites.manage" },
    ],
  },
  {
    key: "running",
    entries: [
      { key: "alertRules", to: settingsRoutes.alerts.to, icon: "bell", permission: "sites.manage" },
      { key: "agent", to: settingsRoutes.agent.to, icon: "presets", permission: "operations.manage" },
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
