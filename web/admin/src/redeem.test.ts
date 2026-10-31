import { expect, it } from "vitest";
import { isProblem } from "./change";
import { readBatch, redeemState } from "./redeem";

const words = { amountInvalid: "amount", countInvalid: "count", noteInvalid: "note", expiresInvalid: "expires" };
const site = "site_00000000000000000000000000";

it("reads a batch in the site's currency, without what was left empty", () => {
  expect(readBatch({ amount: "2.5", count: "3", note: " spring " }, site, "USD", words)).toEqual({
    site_id: site,
    amount: { amount: "2500000000", currency: "USD" },
    count: 3,
    note: "spring",
    expires_at: undefined,
  });
});

it.each([
  [{ amount: "0", count: "1" }, "amount"],
  [{ amount: "1.0000000001", count: "1" }, "amount"],
  [{ amount: "1", count: "0" }, "count"],
  [{ amount: "1", count: "101" }, "count"],
  [{ amount: "1", count: "2.5" }, "count"],
  [{ amount: "1", count: "1", note: "x".repeat(201) }, "note"],
  [{ amount: "1", count: "1", expires_at: "2000-01-01T00:00" }, "expires"],
])("refuses %o", (values, problem) => {
  const read = readBatch(values, site, "USD", words);
  expect(isProblem(read) ? read.problem : null).toBe(problem);
});

it("keeps only a state the page knows", () => {
  expect(redeemState("expired")).toBe("expired");
  expect(redeemState("gone")).toBeUndefined();
});
