import type { Schemas } from "@axiom/api";
import { describe, expect, it } from "vitest";
import { mergeOrder } from "./order-merge";

const order = (over: Partial<Schemas["Order"]>): Schemas["Order"] =>
  ({
    id: "ord_01j00000000000000000000000",
    state: "pending",
    method: "easypay",
    credits: { amount: "10000000000", currency: "USD" },
    total: { amount: "1030", currency: "USD" },
    fee: { amount: "30", currency: "USD" },
    created_at: "2026-10-28T00:00:00Z",
    expires_at: "2026-10-28T00:15:00Z",
    ...over,
  }) as Schemas["Order"];

describe("mergeOrder", () => {
  it("does not invent a checkout when neither answer has one", () => {
    const created = order({ state: "pending" });
    const live = order({ state: "pending" });
    expect(mergeOrder(created, live)?.checkout).toBeUndefined();
  });

  it("keeps the created answer's secret and the read's state", () => {
    const created = order({ state: "pending", checkout: { client_secret: "secret" } });
    const live = order({ state: "review", checkout: { qr_code: "wxp://pay/token" } });
    const merged = mergeOrder(created, live);
    expect(merged?.state).toBe("review");
    expect(merged?.checkout).toEqual({ client_secret: "secret", qr_code: "wxp://pay/token" });
  });

  it("does not merge another order", () => {
    const created = order({ id: "ord_01j00000000000000000000001" });
    const live = order({ id: "ord_01j00000000000000000000002", state: "paid" });
    expect(mergeOrder(created, live)).toEqual(created);
  });
});
