import type { Messages } from "@axiom/ui";
import type zh from "./zh";

export default {
  nav: {
    overview: "Overview",
    keys: "API keys",
    logs: "Logs",
  },
  overview: {
    description: "Your balance, this month’s spend and recent errors will show here.",
    version: "Program version",
  },
  keys: {
    description: "Create and manage the keys you call Axiom with.",
  },
  logs: {
    description: "The result, attempts and cost of every call.",
  },
  comingSoon: "This page is under construction.",
  newVersion: {
    message: "Axiom has been updated. Reload the page to use the new version.",
    reload: "Reload",
  },
} satisfies Messages<typeof zh>;
