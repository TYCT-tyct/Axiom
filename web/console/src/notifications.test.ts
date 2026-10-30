import { ApiError, type Schemas } from "@axiom/api";
import { describe, expect, it, vi } from "vitest";
import { initialSettings, putPreferences, spendAnomalyChange } from "./pages/notifications";

const body = (spendAnomaly: boolean | undefined): Schemas["NotificationPreferencesRequest"] => ({
  low_balance: true,
  top_up: true,
  factor_changed: true,
  ...(spendAnomaly === undefined ? {} : { spend_anomaly: spendAnomaly }),
  threshold: "0",
  webhook_url: "",
  language: "en",
});

// A node that predates the preference omits it; the page must then hide the switch and leave the field out of the
// request. A stored value must survive an unrelated save, and an unchanged echo must not overwrite another tab.
describe("notification settings", () => {
  it("starts from the stored switch and leaves it undefined when the node omits it", () => {
    expect(initialSettings({ spend_anomaly: false }).spendAnomaly).toBe(false);
    expect(initialSettings({}).spendAnomaly).toBeUndefined();
  });

  it("asks to change the switch only when the node supports it and the user changed it", () => {
    expect(spendAnomalyChange(initialSettings({}), undefined)).toEqual({});
    expect(spendAnomalyChange(initialSettings({ spend_anomaly: false }), false)).toEqual({});
    expect(spendAnomalyChange(initialSettings({ spend_anomaly: false }), true)).toEqual({ spend_anomaly: false });
    expect(spendAnomalyChange(initialSettings({ spend_anomaly: true }), false)).toEqual({ spend_anomaly: true });
  });
});

// The read may come from a new node while the save lands on an old one. Neither a closed schema that rejects the field
// nor today's decoder that ignores it may turn that into a complete save: the other settings are stored, the reminder
// is reported as unsaved.
describe("saving across versions", () => {
  it("retries without the reminder when the handling node rejects the field", async () => {
    const send = vi
      .fn<(body: Schemas["NotificationPreferencesRequest"]) => Promise<Schemas["NotificationPreferences"]>>()
      .mockRejectedValueOnce(new ApiError(422, null))
      .mockResolvedValueOnce({ spend_anomaly: true });
    const result = await putPreferences(body(false), send);
    expect(result.spendSaved).toBe(false);
    expect(send).toHaveBeenCalledTimes(2);
    expect(send.mock.calls[0]?.[0]).toMatchObject({ spend_anomaly: false });
    expect(send.mock.calls[1]?.[0]).not.toHaveProperty("spend_anomaly");
  });

  it("reports the reminder unsaved when the handling node ignores the field", async () => {
    const send = vi
      .fn<(body: Schemas["NotificationPreferencesRequest"]) => Promise<Schemas["NotificationPreferences"]>>()
      .mockResolvedValue({});
    const result = await putPreferences(body(false), send);
    expect(result.spendSaved).toBe(false);
    expect(send).toHaveBeenCalledTimes(1);
  });

  it("reports a complete save when the handling node stores the field", async () => {
    const send = vi
      .fn<(body: Schemas["NotificationPreferencesRequest"]) => Promise<Schemas["NotificationPreferences"]>>()
      .mockResolvedValue({ spend_anomaly: false });
    const result = await putPreferences(body(false), send);
    expect(result.spendSaved).toBe(true);
    expect(send).toHaveBeenCalledTimes(1);
  });

  it("does not retry a refusal that omitting the field cannot fix", async () => {
    const send = vi
      .fn<(body: Schemas["NotificationPreferencesRequest"]) => Promise<Schemas["NotificationPreferences"]>>()
      .mockRejectedValue(new ApiError(401, null));
    await expect(putPreferences(body(false), send)).rejects.toBeInstanceOf(ApiError);
    expect(send).toHaveBeenCalledTimes(1);
  });
});
