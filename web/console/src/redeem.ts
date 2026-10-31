import { ApiError, type Schemas } from "@axiom/api";
import { refused } from "@axiom/app";

// What the redeem form sends: the code as typed; the server removes its dashes and spaces and ignores case.
export type RedeemRequest = Schemas["RedeemCodeRequest"];

// Where a redemption stands with its one Idempotency-Key. A code is a credential, so it stays in this page's memory:
// sessionStorage keeps the key of an attempt whose answer did not arrive and nothing else. A reload then asks for the
// code again, and the server replays only the redemption of the same code under the same key, so retyping it cannot
// redeem it twice (internal/payments/redeem.go 的 `replay`).
export type Redemption =
  | { state: "editing"; key: string }
  // The answer did not arrive. request is this page's memory of what it sent: absent after a reload, and then the form
  // asks for the code again.
  | { state: "unknown"; key: string; request?: RedeemRequest }
  // The key is bound to a different code, so this request was not made; the form starts again with a new key.
  | { state: "reused" };

// Sendable is a redemption with the key a retry must keep.
export type Sendable = Extract<Redemption, { key: string }>;

// afterFailure says where a redemption sent from `from` stands after error. A redemption has no revision, so a stale
// answer is not one of its states.
export function afterFailure(from: Sendable, request: RedeemRequest, error: unknown, newKey: () => string): Redemption {
  if (error instanceof ApiError && error.code === "idempotency_key_reused") {
    return { state: "reused" };
  }
  // Once the result is unknown, no other answer proves the first try was not done, so the same key stays and the code
  // goes out again under it. A refusal before anything was done frees the form, which makes its next try another change.
  if (from.state === "unknown" || !refused(error)) {
    return { state: "unknown", key: from.key, request };
  }
  return { state: "editing", key: newKey() };
}

// readCode refuses only what the page can see is unusable: nothing typed, or more than the contract's 64 characters.
export function readCode(values: Record<string, string>): RedeemRequest | "missing" | "long" {
  const code = (values.code ?? "").trim();
  if (!code) {
    return "missing";
  }
  if ([...code].length > 64) {
    return "long";
  }
  return { code };
}

// redemptionStore names the key kept for one account: another account signed in on this browser must not inherit an
// attempt it never made.
const redemptionStore = (scope: string) => `${scope}/redeem`;

// readRedemption reads the key of an attempt whose answer did not arrive, or null when there is none. Only the key is
// kept, so the code is not filled in from storage: it is asked for again.
export function readRedemption(scope: string): Redemption | null {
  try {
    const raw = sessionStorage.getItem(redemptionStore(scope));
    if (!raw) {
      return null;
    }
    const value: unknown = JSON.parse(raw);
    const key = typeof value === "object" && value !== null ? (value as { key?: unknown }).key : undefined;
    return typeof key === "string" ? { state: "unknown", key } : null;
  } catch {
    return null;
  }
}

// writeRedemption keeps the key while the answer is unknown and forgets it once one is known. The request body is never
// written: a code that may still be valid must not outlive the page in readable form.
export function writeRedemption(scope: string, redemption: Redemption) {
  try {
    if (redemption.state !== "unknown") {
      sessionStorage.removeItem(redemptionStore(scope));
      return;
    }
    sessionStorage.setItem(redemptionStore(scope), JSON.stringify({ key: redemption.key }));
  } catch {
    // The page still holds this attempt; only a reload loses it.
  }
}
