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
  },
  keys: {
    description: "Create and manage the keys you call Axiom with.",
  },
  logs: {
    description: "The result, attempts and cost of every call.",
  },
  comingSoon: "This page is under construction.",
} satisfies Messages<typeof zh>;
