import type { AdminSchemas } from "@axiom/api";

type Kind = AdminSchemas["SupplierInboxKind"];

// The four kinds the queue is filtered by (adPg_suppliers' inbox: 价格, 模型, Key, 余额), each what the API's kinds
// mean to the person working the queue: what it costs, what is served, what calls it, and what pays for it.
export const categories = ["price", "model", "key", "balance"] as const;
export type Category = (typeof categories)[number];

export const categoryOf = {
  low_balance: "balance",
  ratio_changed: "price",
  cost_mismatch: "price",
  group_added: "model",
  unlisted_model_called: "model",
  key_invalid: "key",
  unknown_model: "model",
} as const satisfies Record<Kind, Category>;

// How much an item weighs: a balance running out and a key refused stop traffic, a price change may cost money, the
// rest is news (adInbox's sev).
export const toneOf = {
  low_balance: "bad",
  key_invalid: "bad",
  ratio_changed: "warn",
  cost_mismatch: "warn",
  unknown_model: "warn",
  group_added: "neutral",
  unlisted_model_called: "neutral",
} as const satisfies Record<Kind, "bad" | "warn" | "neutral">;

// A difference in basis points as the page writes it: 820 is 8.2%.
export const basisPoints = (bp: number) => `${(Math.abs(bp) / 100).toFixed(1)}%`;

// cost_mismatch's difference, from the estimate and the charge (getSupplierReconciliation's difference_bp, which the
// item does not carry), in basis points; none when nothing was estimated.
export function mismatch(estimated: string | undefined, charged: string | undefined) {
  const est = Number(estimated);
  const got = Number(charged);
  if (!Number.isFinite(est) || !Number.isFinite(got) || est === 0) return undefined;
  return Math.round(((got - est) / est) * 10_000);
}
