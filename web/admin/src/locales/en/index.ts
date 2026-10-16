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
  },
  users,
  suppliers,
  mappings,
  models,
  pricing,
  settings,
} satisfies Messages<typeof zh>;
