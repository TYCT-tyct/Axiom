import type { paths } from "../packages/api/src/console.gen.ts";
import type { MockState, Schemas } from "./console-data.ts";
import { type Ctx as CoreCtx, fail, ok } from "./mock.ts";

type Ctx<K extends keyof paths> = CoreCtx<paths, K, "post", MockState>;

export function createOrder({ body, state }: Ctx<"/api/console/v1/orders">) {
  const quote = state.methods.find((item) => item.method === body.method) ?? state.methods[0];
  const order: Schemas["Order"] = {
    id: `ord_${String(state.orders.length + 100).padStart(26, "0")}` as Schemas["OrderId"],
    state: "pending",
    method: body.method,
    credits: body.credits,
    total: quote?.total ?? { amount: "7373", currency: "CNY" },
    fee: quote?.fee ?? { amount: "73", currency: "CNY" },
    created_at: new Date().toISOString(),
    expires_at: new Date(Date.now() + 15 * 60_000).toISOString(),
    checkout: { pay_url: "https://pay.example.com/checkout/new", qr_code: "https://pay.example.com/qr/new" },
    refund_allowed: true,
    refunds: [],
  };
  state.orders.unshift(order);
  return { status: 201 as const, json: order };
}

export function verifyOrder({ params, state }: Ctx<"/api/console/v1/orders/{order_id}/verify">) {
  const order = state.orders.find((item) => item.id === params.order_id);
  if (!order) return fail(404, "not_found");
  if (order.state === "pending") {
    order.state = "paid";
    order.paid_at = new Date().toISOString();
    order.paid_total = order.total;
    order.credited = order.credits;
    order.checkout = undefined;
  }
  return ok(order);
}

export function cancelOrder({ params, state }: Ctx<"/api/console/v1/orders/{order_id}/cancel">) {
  const order = state.orders.find((item) => item.id === params.order_id);
  if (!order) return fail(404, "not_found");
  if (order.state === "pending" || order.state === "cancelled") {
    order.state = "cancelled";
    order.checkout = undefined;
    return ok(order);
  }
  return fail(409, "conflict");
}

export function requestRefund({ body, params, state }: Ctx<"/api/console/v1/orders/{order_id}/refunds">) {
  const order = state.orders.find((item) => item.id === params.order_id);
  if (!order) return fail(404, "not_found");
  if (!order.refund_allowed) return fail(403, "forbidden");
  const refund: Schemas["OrderRefund"] = {
    id: `rfd_${String((order.refunds?.length ?? 0) + 100).padStart(26, "0")}` as Schemas["RefundId"],
    state: "pending_review",
    credits: body.credits,
    amount: { amount: "3650", currency: order.total.currency },
    created_at: new Date().toISOString(),
  };
  order.refunds = [...(order.refunds ?? []), refund];
  return { status: 201 as const, json: refund };
}
