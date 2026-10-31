import type { AdminSchemas } from "@axiom/api";
import { bufferRefused, withoutBuffer } from "./payment-read";

type Update = AdminSchemas["PaymentSettingsUpdate"]["settings"];
type Saved = { data: AdminSchemas["PaymentSettings"]; replayed: boolean };

// PaymentAttempt is the body a key must resend. A downgraded body is kept under the same key, so a resend after a lost
// answer never goes back to the buffered form and is not refused as a reused key.
export type PaymentAttempt = { settings: Update; skipped: boolean };

export type PaymentPut = (settings: Update, reason: string, key: string) => Promise<Saved>;

// savePaymentSettings sends the body attempted for key, downgrading once for an older node that refuses the buffer. The
// downgraded body is stored before its response is awaited: a lost answer must leave the same body for the resend.
export async function savePaymentSettings(
  attempted: Map<string, PaymentAttempt>,
  key: string,
  settings: Update,
  reason: string,
  put: PaymentPut,
): Promise<Saved & { skipped: boolean }> {
  const known = attempted.get(key);
  const body = known?.settings ?? settings;
  try {
    const saved = await put(body, reason, key);
    return { ...saved, skipped: known?.skipped === true };
  } catch (error) {
    if (!("top_up_buffer_ppm" in body) || !bufferRefused(error)) {
      throw error;
    }
    const downgraded = withoutBuffer(body);
    attempted.set(key, { settings: downgraded, skipped: true });
    const saved = await put(downgraded, reason, key);
    return { ...saved, skipped: true };
  }
}
