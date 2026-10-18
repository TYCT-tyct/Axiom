import type { Messages } from "@axiom/ui";
import type zh from "../zh";
import mappings from "./mappings";
import models from "./models";
import pricing from "./pricing";
import settings from "./settings";
import suppliers from "./suppliers";
import users from "./users";

export default {
  brandName: "Axiom Admin",
  nav: {
    users: "Users",
    suppliers: "Suppliers",
    mappings: "Unmapped models",
    models: "Model catalog",
    pricing: "Pricing",
    settings: "Site settings",
  },
  signIn: { description: "The admin is for operators. After signing in you also complete two-step verification." },
  home: {
    noAccessTitle: "No page is available to you",
    noAccessBody: "Your account holds no admin permission. Ask the owner to grant one.",
  },
  filter: { apply: "Filter", reset: "Clear" },
  change: {
    reason: "Reason",
    reasonHint: "Why you are doing this. It goes to the audit log and can be looked up later.",
    reasonMissing: "Enter a reason.",
    confirmLabel: "Type “{{name}}” to confirm",
    confirmMismatch: "What you typed doesn’t match the thing you are changing.",
    done: "Done.",
    replayed: "This was already done earlier. Below is the result from then; nothing was done again.",
    close: "Close",
    unknown:
      "It isn’t known whether this change was made. The form is locked: resending sends the same request, which the server does at most once — the surest way to learn the result. If you close this, the first request may still take effect, and the data on the page does not prove it didn’t.",
    resend: "Resend as it was",
    stale:
      "Someone else changed this after you opened it. Nothing was saved, so their change isn’t overwritten. Reloading fills in the current version and discards what you entered here.",
    reload: "Reload",
    reused:
      "This request’s key was already used for different content, so the first attempt may have been made; nothing more is sent from here. The data you see after closing may not include it yet, so it can’t tell you whether to do it again.",
  },
  users,
  suppliers,
  mappings,
  models,
  pricing,
  settings,
} satisfies Messages<typeof zh>;
