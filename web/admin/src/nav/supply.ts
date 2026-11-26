import type { NavEntry } from "../nav";

export const supply: readonly NavEntry[] = [
  {
    key: "suppliers",
    to: "/suppliers",
    icon: "byok",
    permission: "suppliers.view",
    pending: "inbox",
    also: [
      { to: "/suppliers?tab=models", label: "upstreamModels" },
      { to: "/suppliers?tab=inbox", label: "inbox" },
      { to: "/suppliers?tab=keys", label: "upstreamKeys" },
    ],
  },
  {
    key: "models",
    to: "/models",
    icon: "models",
    permission: "catalog.view",
    also: [{ to: "/models?tab=authors", label: "modelAuthors" }],
  },
  {
    key: "routing",
    to: "/routing",
    icon: "route",
    permission: "sites.manage",
    also: [
      { to: "/routing?tab=defaults", label: "routingDefaults" },
      { to: "/routing?tab=sources", label: "sourceClasses" },
    ],
  },
  {
    key: "pricing",
    to: "/pricing",
    icon: "classify",
    permission: "pricing.view",
    also: [
      { to: "/pricing?tab=rules", label: "pricingRules" },
      { to: "/pricing?tab=rates", label: "exchangeRates" },
      { to: "/pricing?tab=policy", label: "pricingPolicy" },
    ],
  },
  {
    key: "plugins",
    to: "/plugins",
    icon: "plugin",
    permission: "sites.manage",
    also: [
      { to: "/plugins/web", label: "webSearch" },
      { to: "/plugins/file", label: "fileParser" },
    ],
  },
];
