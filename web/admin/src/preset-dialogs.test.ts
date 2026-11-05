import type { AdminSchemas } from "@axiom/api";
import { expect, it } from "vitest";
import { selectable, systemPreset } from "./preset-dialogs";
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
