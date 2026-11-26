import type shell from "../locales/zh/shell";
import type { Group, NavEntry } from "../nav";
import type { SettingsPage } from "../settings-registry";

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
