import type { ReactNode } from "react";
import { loadTranslatedPage } from "../locales/load-page";
import { movedPage, shellPage, text } from "./shell";

// A settings page, which the palette reaches with the row to flash named in the address (settings-drafts.tsx).
const settingsPage = (path: string, load: () => Promise<{ default: () => ReactNode }>) =>
  shellPage(path, load, (search) => ({ setting: text(search.setting) }));

// 系统设置: the system settings' pages, and the operator's own page.
export const systemRoutes = [
  shellPage("/me", () => loadTranslatedPage(import("../pages/me"))),
  settingsPage("/alert-configuration", () => loadTranslatedPage(import("../pages/alert-configuration"))),
  shellPage("/legal-documents", () => loadTranslatedPage(import("../pages/legal-documents"))),
  shellPage("/rate-limits", () => loadTranslatedPage(import("../pages/rate-limits"))),
  settingsPage("/settings/site", () => loadTranslatedPage(import("../pages/settings-site"))),
  settingsPage("/settings/sign-in", () => loadTranslatedPage(import("../pages/settings-sign-in"))),
  settingsPage("/settings/registration", () => loadTranslatedPage(import("../pages/settings-registration"))),
  settingsPage("/settings/sessions", () => loadTranslatedPage(import("../pages/settings-sessions"))),
  settingsPage("/settings/limits", () => loadTranslatedPage(import("../pages/settings-limits"))),
  settingsPage("/settings/payments", () => loadTranslatedPage(import("../pages/settings-payments"))),
  settingsPage("/settings/mail", () => loadTranslatedPage(import("../pages/settings-mail"))),
  settingsPage("/settings/gateway", () => loadTranslatedPage(import("../pages/settings-gateway"))),
  settingsPage("/settings/agent", () => loadTranslatedPage(import("../pages/settings-agent"))),
  // Where the pages of the earlier settings went (adAlias): a link or a bookmark still lands on the page.
  movedPage("/settings", () => ({ to: "/settings/site" })),
  movedPage("/retention", () => ({ to: "/settings/gateway" })),
  movedPage("/operations-credentials", () => ({ to: "/settings/agent" })),
  movedPage("/operations-limits", () => ({ to: "/settings/agent" })),
] as const;
