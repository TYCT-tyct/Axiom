import type { Messages } from "@axiom/ui";
import type * as zh from "../zh/settings-site";

export const brand = {
  title: "Site brand colour",
  unavailableLabel: "Brand colour",
  unavailable: "Not available: this server does not give the site brand colour yet. Everything else here works.",
  color: "Brand colour",
  default: "Default palette (#00a2c7)",
  preview: "Light and dark preview",
  light: "Light mode",
  dark: "Dark mode",
  sample: "Accent text and link",
  contrast: "Text {{text}}:1 · Fill {{solid}}:1",
  adjusted: "Adjusted automatically to meet WCAG AA in both light and dark modes (text ≥4.5:1, fill ≥3:1).",
  unchanged: "The chosen colour already meets WCAG AA contrast in both light and dark modes.",
  edit: "Edit brand colour",
  editTitle: "Edit site brand colour",
  intro:
    "Enter a #rrggbb hex colour; the console and site derive light and dark accents from it. Leave empty to return to the default palette. The change goes to the audit log.",
  hint: "Written as #rrggbb, such as #00a2c7. Leave empty for the default palette.",
  invalid: "Enter a colour written as #rrggbb (such as #00a2c7), or leave empty for the default palette.",
  save: "Save brand colour",
  saved: "Site brand colour set to {{color}}.",
  cleared: "Site returned to the default palette.",
} satisfies Messages<typeof zh.brand>;

export const workspaceLimit = {
  title: {
    site: "Workspace limit of the site's accounts",
    account: "Workspace limit of this account",
  },
  field: "Workspaces an account may hold",
  unavailableLabel: "Workspace limit",
  unavailable: "Not available: this server does not give the workspace limit yet. Everything else here works.",
  own: "Set here",
  from: { site: "Default", account: "From the site" },
  edit: "Edit workspace limit",
  save: "Save workspace limit",
  saved: "The workspace limit is saved.",
  intro: {
    site: "What every account of the site is held to unless it has a limit of its own. Leaving it empty returns to the code default (50). Lowering it disables no workspace; accounts only cannot create more. The change goes to the audit log.",
    account:
      "This account's own limit, over the site's. Leaving it empty returns to the site's limit. Lowering it disables no workspace; the account only cannot create more. The change goes to the audit log.",
  },
  hint: {
    site: "A whole number from {{min}} to {{max}}. Empty uses the default in code.",
    account: "A whole number from {{min}} to {{max}}. Empty uses the site's.",
  },
  invalid: "Enter a whole number from {{min}} to {{max}}.",
} satisfies Messages<typeof zh.workspaceLimit>;

export const loginLimits = {
  title: "Sign-in attempt limits",
  unavailableLabel: "Login limits",
  unavailable: "Not available: this server does not give sign-in attempt limits yet. Everything else here works.",
  field: {
    ip_attempt_burst: "IP attempt burst",
    ip_attempts_per_minute: "IP attempts per minute",
    email_sign_in_failures: "Email sign-in failures",
    user_code_failures: "User verification code failures",
    challenge_code_attempts: "Attempts per verification challenge",
  },
  edit: "Edit login limits",
  save: "Save login limits",
  saved: "The sign-in attempt limits are saved.",
  intro:
    "Caps sign-in attempts per IP, email and verification challenge. Fields left empty use code defaults. The change goes to the audit log.",
  hint: "A whole number from {{min}} to {{max}}. Empty uses the default ({{default}}).",
  invalid: "Enter whole numbers within each field's bounds, or leave empty for the default.",
} satisfies Messages<typeof zh.loginLimits>;
