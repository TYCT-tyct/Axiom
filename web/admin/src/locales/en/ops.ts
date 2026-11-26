import type { Messages } from "@axiom/ui";
import type zh from "../zh/ops";

// The two cards the dashboard and the operations monitor share: the lines and the upstream balances.
export default {
  lines: {
    title: "Lines",
    window: { "1h": "Last hour", "24h": "Last 24 hours" },
    line: "Line",
    calls: "Requests",
    failures: "Failed",
    rate: "Success",
    first: "First token",
    busy: "Concurrency",
    share: "Traffic",
    none: "—",
    seconds: "{{value}} s",
    busyBar: "Concurrency held by {{name}}'s upstream accounts",
    shareBar: "{{name}}'s share of all requests",
    routing: "Routing presets",
    monitor: "Operations monitor",
    empty: "No request went through any line in this period",
  },
  balances: {
    title: "Upstream balances",
    lede: "Estimated from the last 7 days' average spend",
    none: "No channel is connected yet.",
    noneReadable: "No channel has a balance that can be read.",
    noSpend: "No spend in the last 7 days to estimate from",
    short_one: "Lasts only {{count}} day",
    short_other: "Lasts only {{count}} days",
    enough_one: "Lasts {{count}} day",
    enough_other: "Lasts {{count}} days",
    bar: "Days {{name}}'s balance will last",
    unread: "{{names}} have no balance to read, so they are not listed.",
    join: ", ",
    balance: "{{value}} {{unit}}",
    unit: { quota: "quota", usd: "USD" },
  },
} satisfies Messages<typeof zh>;
