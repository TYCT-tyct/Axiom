import { expect, it } from "vitest";
import { isProblem } from "./change";
import { readCredit } from "./credit-read";

const words = { amountInvalid: "amount", expiresInvalid: "expires", channelInvalid: "channel" };

it("reads a gift in the site's currency", () => {
  expect(readCredit({ source: "gift", amount: "12.5" }, "USD", words)).toEqual({
    source: "gift",
    amount: { amount: "12500000000", currency: "USD" },
    expires_at: undefined,
  });
});

it("reads an offline payment in the currency it was paid in, with its channel", () => {
  expect(
    readCredit({ source: "offline_payment", amount: "74", currency: "CNY", channel: " ICBC 6222 " }, "USD", words),
  ).toEqual({
    source: "offline_payment",
    payment: { amount: { amount: "74000000000", currency: "CNY" }, channel: "ICBC 6222" },
    expires_at: undefined,
  });
});

it.each([
  [{ source: "gift", amount: "0" }, "amount"],
  [{ source: "offline_payment", amount: "1.0000000001", currency: "USD", channel: "c" }, "amount"],
  [{ source: "offline_payment", amount: "1", currency: "USD", channel: "  " }, "channel"],
  [{ source: "gift", amount: "1", expires_at: "2000-01-01T00:00" }, "expires"],
])("refuses %o", (values, problem) => {
  const read = readCredit(values, "USD", words);
  expect(isProblem(read) ? read.problem : null).toBe(problem);
});
