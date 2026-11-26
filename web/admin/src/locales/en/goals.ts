import type { Messages } from "@axiom/ui";
import type zh from "../zh/goals";

// The dashboard's month goals, loaded with the cards under the figures.
export default {
  title: "This month's targets",
  lede: "{{month}}. The line marks where an even spread over the days would have each bar today.",
  none: "No target is set for this month. Set one at the top right.",
  loading: "Reading the targets…",
  failed: "The targets could not be read.",
  revenue: "Revenue",
  paid: "Paying accounts",
  margin: "Gross margin",
  retention: "Next-month retention",
  of: " / {{target}}",
  pace: "Spread over the days, this is where it should be today",
} satisfies Messages<typeof zh>;
