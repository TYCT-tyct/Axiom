import type { Messages } from "@axiom/ui";
import type * as zh from "../zh/settings-site";

export const brand = {
  preview: "Light and dark preview",
  light: "Light mode",
  dark: "Dark mode",
  sample: "Accent text and link",
  contrast: "Text {{text}}:1 · Fill {{solid}}:1",
  adjusted: "Adjusted automatically to meet WCAG AA in both light and dark modes (text ≥4.5:1, fill ≥3:1).",
  unchanged: "The chosen colour already meets WCAG AA contrast in both light and dark modes.",
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

export const loginLimits = {} satisfies Messages<typeof zh.loginLimits>;
