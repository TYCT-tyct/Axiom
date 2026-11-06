import type { AdminSchemas } from "@axiom/api";
import { systemPreset } from "@axiom/app";
import { expect, it } from "vitest";
import { currencyOf, selectable } from "./preset-dialogs";
import type { PresetLayer } from "./queries";

const preset = (over: Partial<AdminSchemas["RoutingPreset"]>) =>
  ({
    id: "00000000-0000-4000-8000-000000000001",
    scope: "platform",
    disabled: false,
    ...over,
  }) as AdminSchemas["RoutingPreset"];
const platform: PresetLayer = { scope: "platform" };
const site: PresetLayer = { scope: "site", siteId: "site_00000000000000000000000001" as AdminSchemas["SiteId"] };

// A layer's default offers only what the server accepts for it (internal/routing defaults.go): the platform's serves
// every site, so it takes no priced preset; a site's takes its own and the platform's in its currency, never the
// system preset; no default takes a disabled one.
it.each([
  { layer: platform, over: {}, offered: true },
  { layer: platform, over: { id: systemPreset }, offered: true },
  { layer: platform, over: { currency: "USD" }, offered: false },
  { layer: platform, over: { disabled: true }, offered: false },
  { layer: platform, over: { scope: "site" as const }, offered: false },
  { layer: site, over: { scope: "site" as const }, offered: true },
  { layer: site, over: { scope: "site" as const, disabled: true }, offered: false },
  { layer: site, over: {}, offered: true },
  { layer: site, over: { currency: "USD" }, offered: true },
  { layer: site, over: { currency: "CNY" }, offered: false },
  { layer: site, over: { id: systemPreset }, offered: false },
])("$layer.scope default, preset $over: $offered", ({ layer, over, offered }) => {
  expect(selectable(layer, preset(over), "USD")).toBe(offered);
});

const cannotPrice = { problem: "routingPresets.form.cannotPrice" };
const invalid = { problem: "routingPresets.form.currencyInvalid" };

// The currency a write names (internal/routing ErrPresetCurrency): a changed platform preset keeps the one it has,
// priced or not, so a rename of one with a currency and no price never makes it serve every site; one without a
// currency cannot gain a price; a new one names a valid code only when priced; a site's names none.
it.each([
  {
    case: "a rename of a platform preset with a currency and no price",
    layer: platform,
    preset: preset({ currency: "USD" }),
    priced: false,
    sent: "USD",
  },
  {
    case: "a priced platform preset changed",
    layer: platform,
    preset: preset({ currency: "USD" }),
    priced: true,
    sent: "USD",
  },
  { case: "an unpriced platform preset changed", layer: platform, preset: preset({}), priced: false, sent: undefined },
  {
    case: "an unpriced platform preset given a price",
    layer: platform,
    preset: preset({}),
    priced: true,
    sent: cannotPrice,
  },
  { case: "a new priced platform preset", layer: platform, preset: undefined, priced: true, sent: "EUR" },
  { case: "a new unpriced platform preset", layer: platform, preset: undefined, priced: false, sent: undefined },
  {
    case: "a new platform preset with a bad code",
    layer: platform,
    preset: undefined,
    priced: true,
    code: "eu",
    sent: invalid,
  },
  { case: "a site preset", layer: site, preset: preset({ scope: "site" }), priced: true, sent: undefined },
])("$case names $sent", ({ layer, preset: before, priced, code = "EUR", sent }) => {
  expect(currencyOf(layer, before, code, priced)).toEqual(sent);
});
