import type { AdminSchemas } from "@axiom/api";
import { parseMoney } from "@axiom/ui";
import type { Problem } from "./change";

export type Grant = Omit<AdminSchemas["CreditGrantRequest"], "reason">;

// The currencies an exchange rate can be kept for (api/admin.yaml, billing.exchange_rates): an offline payment is in
// one of them, and the server converts it to the site's currency at the rate in force.
export const paymentCurrencies = ["USD", "CNY", "EUR", "GBP", "JPY", "HKD", "SGD", "AUD", "CAD", "CHF"] as const;

type Words = { amountInvalid: string; expiresInvalid: string; channelInvalid: string };

// readCredit makes a grant request from the dialog's fields: a gift of an amount in the site's currency, or an offline
// payment of an amount in the currency it was paid in, received at a channel. Whole minor units and the limit of one
// grant are the server's to check.
export function readCredit(values: Record<string, string>, currency: string, words: Words): Grant | Problem {
  const amount = parseMoney(values.amount ?? "");
  if (amount === null || /^0+$/.test(amount)) {
    return { problem: words.amountInvalid };
  }
  const expires = values.expires_at ? new Date(values.expires_at) : undefined;
  if (expires && !(expires.getTime() > Date.now())) {
    return { problem: words.expiresInvalid };
  }
  const expires_at = expires?.toISOString();
  if (values.source !== "offline_payment") {
    return { source: "gift", amount: { amount, currency }, expires_at };
  }
  const channel = (values.channel ?? "").trim();
  if (!channel || channel.length > 200) {
    return { problem: words.channelInvalid };
  }
  const paid = values.currency ?? currency;
  return { source: "offline_payment", payment: { amount: { amount, currency: paid }, channel }, expires_at };
}
