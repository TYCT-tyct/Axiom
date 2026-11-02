import { type AdminSchemas, ApiError } from "@axiom/api";
import { expect, it } from "vitest";
import { savePaymentSettings } from "./payment-save";

type Update = AdminSchemas["PaymentSettingsUpdate"]["settings"];
const buffered: Update = { open: true, top_up_buffer_ppm: 20_000 };
const unbuffered: Update = { open: true };
const refusal = new ApiError(400, null, {
  body: {
    error: {
      code: "invalid_request",
      message: "the request breaks the contract",
      fields: [{ pointer: "/settings/top_up_buffer_ppm", message: "unknown field" }],
    },
  },
});
const lost = new ApiError(0, null);

it("resends the downgraded body after its successful answer was lost", async () => {
  const attempted = new Map();
  const bodies: Update[] = [];
  const first = async (body: Update) => {
    bodies.push(body);
    if (bodies.length === 1) {
      throw refusal;
    }
    // The downgraded save was committed but its answer never arrived.
    throw lost;
  };
  await expect(savePaymentSettings(attempted, "key", buffered, "save", first)).rejects.toBe(lost);
  expect(attempted.get("key")).toEqual({ settings: unbuffered, skipped: true });

  // The resend carries the body the original dialog read again; it must use the stored downgraded body, so the new node
  // replays the committed change instead of refusing the key as reused.
  const again = async (body: Update) => {
    bodies.push(body);
    expect(body).toEqual(unbuffered);
    return { data: { open: true }, replayed: true };
  };
  const saved = await savePaymentSettings(attempted, "key", buffered, "save", again);
  expect(saved).toEqual({ data: { open: true }, replayed: true, skipped: true });
  expect(bodies).toEqual([buffered, unbuffered, unbuffered]);
});
