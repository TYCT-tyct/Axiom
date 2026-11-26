import type { NavEntry } from "../nav";

export const people: readonly NavEntry[] = [
  {
    key: "users",
    to: "/users",
    icon: "team",
    permission: "users.view",
    also: [
      { to: "/users/accounts", label: "accounts" },
      { to: "/users/roles", label: "roles" },
    ],
  },
  {
    key: "subs",
    to: "/subscriptions",
    icon: "subs",
    permission: "users.view",
    also: [{ to: "/subscriptions/plans", label: "subscriptionPlans" }],
  },
  {
    key: "orders",
    to: "/orders",
    icon: "wallet",
    permission: "sites.manage",
    pending: "payments",
    also: [
      { to: "/orders/cash", label: "cash" },
      { to: "/orders/batches", label: "batches" },
      { to: "/orders/refunds", label: "refunds" },
      { to: "/orders/discrepancies", label: "discrepancies" },
    ],
  },
  { key: "redeem", to: "/redeem-codes", icon: "ticket", permission: "sites.manage" },
];
