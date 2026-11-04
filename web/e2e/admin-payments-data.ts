import { range, type Schemas, siteId, usd, uuid } from "./admin-fixtures.ts";

// The payment fixtures of the mocked admin API: one channel, its orders, and the refunds frozen on them.

const merchantId = uuid(8, 1) as Schemas["MerchantId"];
const payAmount = (amount: string): Schemas["PayAmount"] => ({ amount, currency: "USD" });

function aMerchant(): Schemas["Merchant"] {
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

// The paid credit batches behind the dashboard's business figures, inside the window. The list is by the batch's
// own time, as the server's paid-batch view is, not by an order's creation time.
function paidBatches(people: Schemas["UserSummary"][], fallbackUser: Schemas["UserSummary"]) {
  const fallback = people[0] ?? fallbackUser;
  const at = (minutes: number) => new Date(Date.now() - minutes * 60_000).toISOString();
  return [
    {
      id: uuid(15, 1),
      account_id: people[0]?.account_id ?? fallback.account_id,
      amount: usd("1500"),
      created_at: at(30),
      online: true,
    },
    {
      id: uuid(15, 2),
      account_id: people[1]?.account_id ?? fallback.account_id,
      amount: usd("2000"),
      created_at: at(60),
      online: false,
      offline_channel: "bank transfer",
    },
    {
      id: uuid(15, 3),
      account_id: people[2]?.account_id ?? fallback.account_id,
      amount: usd("700"),
      created_at: at(90),
      online: true,
    },
  ] satisfies Schemas["PaidBatch"][];
}

// The payment fixtures of a populated site: one channel, its orders, its paid batches, and the refunds frozen on
// them. The caller passes one fallback user for an empty people list; a populated fixture always has its owner first.
export function paymentData(people: Schemas["UserSummary"][], fallbackUser: Schemas["UserSummary"]) {
  const fallback = people[0] ?? fallbackUser;
  const orders = range(60).map((n) => anOrder(n, people[(n - 1) % Math.max(people.length, 1)] ?? fallback));
  const refunds = range(3).map((n) =>
    aRefund(n, orders[n - 1] ?? orders[0] ?? anOrder(1, fallback), people[n] ?? fallback),
  );
  for (const refund of refunds) {
    const order = orders.find((candidate) => candidate.id === refund.order_id);
    if (order) {
      order.refunds = [...(order.refunds ?? []), refund];
    }
  }
  return { merchants: [aMerchant()], orders, paidBatches: paidBatches(people, fallbackUser), refunds };
}
