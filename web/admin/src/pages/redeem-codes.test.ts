import type { AdminSchemas } from "@axiom/api";
import { expect, it } from "vitest";
import { redeemFigures } from "./redeem-codes";

const now = Date.parse("2026-11-26T00:00:00Z");
const code = (state: AdminSchemas["RedeemCode"]["state"], amount: string, expires?: string) =>
  ({
    id: `${state}-${amount}-${expires}`,
    state,
    amount: { amount, currency: "USD" },
    expires_at: expires,
  }) as AdminSchemas["RedeemCode"];

it("counts only what the codes loaded say, with the credit exact past 2^53", () => {
  const figures = redeemFigures(
    [
      code("used", "9007199254740993"),
      code("used", "1"),
      code("enabled", "5", "2026-12-10T00:00:00Z"),
      code("enabled", "5", "2027-06-01T00:00:00Z"),
      code("enabled", "5"),
      code("disabled", "5", "2026-12-01T00:00:00Z"),
    ],
    now,
  );
  expect(figures).toEqual({ enabled: 3, used: 2, redeemed: "9007199254740994", soon: 1 });
});
