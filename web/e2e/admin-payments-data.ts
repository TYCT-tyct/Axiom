import { type Schemas, siteId, usd, uuid } from "./admin-fixtures.ts";

// The payment fixtures of the mocked admin API: one channel, its orders, and the refunds frozen on them.

const merchantId = uuid(8, 1) as Schemas["MerchantId"];
const payAmount = (amount: string): Schemas["PayAmount"] => ({ amount, currency: "USD" });

export function aMerchant(): Schemas["Merchant"] {
  return {
    id: merchantId,
    site_id: siteId,
    kind: "easypay",
    name: "Alipay",
    enabled: true,
    currency: "USD",
    ttl_seconds: 1800,
    refund_enabled: true,
    user_refund: true,
    sort: 1,
  };
}

export function anOrder(n: number, user: Schemas["UserSummary"], over: Partial<Schemas["PaymentOrder"]> = {}) {
  const pending = n % 5 === 0;
  const at = new Date(Date.UTC(2026, 9, 30, 12, 0, 0) - n * 60_000).toISOString();
  return {
    id: uuid(13, n),
    account_id: user.account_id,
    user_id: user.id,
    merchant_id: merchantId,
    merchant_name: "Alipay",
    method: "alipay",
    state: pending ? "pending" : "paid",
    credits: usd("10"),
    total: payAmount("1000"),
    fee: payAmount("30"),
    credited: pending ? undefined : usd("10"),
    paid_total: pending ? undefined : payAmount("1000"),
    paid_fee: pending ? undefined : payAmount("30"),
    trade_no: `trade-${n}`,
    created_at: at,
    paid_at: pending ? undefined : at,
    expires_at: at,
    refund_allowed: true,
    refunds: [],
    ...over,
  } satisfies Schemas["PaymentOrder"];
}

export function aRefund(n: number, order: Schemas["PaymentOrder"], user: Schemas["UserSummary"], reason?: string) {
  return {
    id: uuid(14, n),
    order_id: order.id,
    account_id: order.account_id,
    state: "frozen" as const,
    via: "channel" as const,
    credits: usd("5"),
    amount: payAmount("500"),
    reason: reason ?? "The credits were not used.",
    requested_by: user.id,
    created_at: order.created_at,
  } satisfies Schemas["PaymentRefund"];
}
