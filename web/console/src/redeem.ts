import type { Schemas } from "@axiom/api";

// What the redeem form sends: the code as typed; the server removes its dashes and spaces and ignores case.
export type RedeemRequest = Schemas["RedeemCodeRequest"];

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

export function isRedeemRequest(value: unknown): value is RedeemRequest {
  if (typeof value !== "object" || value === null) {
    return false;
  }
  return typeof (value as { code?: unknown }).code === "string";
}
