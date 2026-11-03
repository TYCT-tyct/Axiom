import type { AdminSchemas } from "@axiom/api";
import { describe, expect, it } from "vitest";
import { modelPricesChangeUnconfirmed } from "./model-prices";

type View = AdminSchemas["SiteSettingsView"];

// A view with only the model-price fields the check reads.
function view(settings?: boolean, defaults?: boolean): View {
  return {
    settings: settings === undefined ? {} : { model_prices_public: settings },
    defaults: defaults === undefined ? {} : { model_prices_public: defaults },
  };
}

describe("modelPricesChangeUnconfirmed", () => {
  // A page read from a new node sends false to an old node, which ignores the field and answers without it.
  it("reports an old node's answer to a new page that hid prices", () => {
    expect(modelPricesChangeUnconfirmed(view(undefined, true), { model_prices_public: false }, view())).toBe(true);
  });

  it("reports an old node's answer to a new page that made prices public again", () => {
    expect(modelPricesChangeUnconfirmed(view(false, true), { model_prices_public: true }, view())).toBe(true);
  });

  it("accepts a server that confirms the new value", () => {
    expect(modelPricesChangeUnconfirmed(view(undefined, true), { model_prices_public: false }, view(false, true))).toBe(
      false,
    );
    expect(modelPricesChangeUnconfirmed(view(false, true), { model_prices_public: true }, view(true, true))).toBe(
      false,
    );
  });

  it("does not block another setting when the switch did not change", () => {
    expect(modelPricesChangeUnconfirmed(view(false, true), { model_prices_public: false }, view())).toBe(false);
  });

  it("does not check a page served by a node that has no field", () => {
    expect(modelPricesChangeUnconfirmed(view(), {}, view())).toBe(false);
  });
});
