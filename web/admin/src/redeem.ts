import type { AdminSchemas } from "@axiom/api";
import { toInstant } from "@axiom/app";
import { parseMoney } from "@axiom/ui";
import type { Problem } from "./change";

// The states a page can narrow the list by; the server derives expired from an enabled code past its expiry.
export const redeemStates = ["enabled", "used", "disabled", "expired"] as const;

// redeemState is the state in the address, or nothing: anything else does not narrow.
export function redeemState(value: unknown) {
  return redeemStates.find((state) => state === value);
}

export type RedeemBatch = Omit<AdminSchemas["RedeemCodeBatchRequest"], "reason">;

type Words = { amountInvalid: string; countInvalid: string; noteInvalid: string; expiresInvalid: string };

// readBatch makes a generation request from the dialog's fields. Whole minor units, the count's range and the note's
// length are the server's to check too; the page says so before the request goes.
export function readBatch(
  values: Record<string, string>,
  siteId: AdminSchemas["SiteId"],
  currency: string,
  words: Words,
): RedeemBatch | Problem {
  const amount = parseMoney(values.amount ?? "");
  if (amount === null || /^0+$/.test(amount)) {
    return { problem: words.amountInvalid };
  }
  const count = Number(values.count);
  if (!/^\d+$/.test(values.count ?? "") || !Number.isInteger(count) || count < 1 || count > 100) {
    return { problem: words.countInvalid };
  }
  const note = (values.note ?? "").trim();
  if ([...note].length > 200) {
    return { problem: words.noteInvalid };
  }
  const expires_at = toInstant(values.expires_at);
  if (values.expires_at && (!expires_at || new Date(expires_at).getTime() <= Date.now())) {
    return { problem: words.expiresInvalid };
  }
  return {
    site_id: siteId,
    amount: { amount, currency },
    count,
    note: note === "" ? undefined : note,
    expires_at,
  };
}
