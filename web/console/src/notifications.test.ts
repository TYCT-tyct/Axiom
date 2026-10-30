import { describe, expect, it } from "vitest";
import { initialSettings } from "./pages/notifications";

// The form must start from what the account stored: an older node that omits the field, or a reminder the account
// turned off, must not silently turn unusual-activity mail back on when the page is opened.
describe("notification settings", () => {
  it("starts from the stored unusual-activity switch and defaults it on when the node omits it", () => {
    expect(
      initialSettings({
        low_balance: true,
        top_up: true,
        factor_changed: true,
        spend_anomaly: false,
        threshold: "0",
        webhook_url: "",
        language: "en",
      }).spendAnomaly,
    ).toBe(false);
    expect(initialSettings({}).spendAnomaly).toBe(true);
  });
});
