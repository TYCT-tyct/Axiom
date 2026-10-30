import { describe, expect, it } from "vitest";
import { initialSettings, spendAnomalyChange } from "./pages/notifications";

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
