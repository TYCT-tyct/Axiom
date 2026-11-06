// @vitest-environment jsdom
import type { AdminSchemas } from "@axiom/api";
import { afterEach, expect, it, vi } from "vitest";
import en from "./locales/en";
import { DisablePresetDialog } from "./preset-dialogs";
import { renderInDom } from "./test-dom";

const platformPreset = "00000000-0000-4000-9000-000000000001";
const sitePreset = "00000000-0000-4000-9000-000000000002";
const site = "site_01jbz3x0000000000000000000";

// The platform's default and the site's both point at the platform preset; the site preset is no layer's default.
vi.mock("@axiom/api", async (importOriginal) =>
  (await import("./test-dom")).withAdminApi(importOriginal, {
    GET: vi.fn(() => Promise.resolve({ data: { preset_id: platformPreset, version: 1 }, response: new Response() })),
  }),
);

vi.mock("@axiom/app", async (importOriginal) =>
  (await import("./test-dom")).signedIn(importOriginal, ["sites.manage"]),
);

const preset = (id: string, scope: "platform" | "site", keys?: number): AdminSchemas["RoutingPreset"] => ({
  id,
  scope,
  slug: "p",
  name: "P",
  revision: 1,
  version: 1,
  rules: { default: {} },
  disabled: false,
  keys,
  created_at: "2026-11-01T00:00:00Z",
  updated_at: "2026-11-01T00:00:00Z",
});

const { disableKeys, disableDefault } = en.routingPresets;

let unmount = async () => {};
afterEach(() => unmount());

// Before disabling, the dialog says how many keys still route by the preset and which default this page reads points
// at it, so the operator changes that first. The warning never turns the button off: the page's default may be stale
// and the node decides (docs/domains/routing.md); a node that counts no keys has no number said for it.
it.each([
  {
    name: "the platform's and the site's default, with keys",
    preset: preset(platformPreset, "platform", 2),
    said: ["Accounts have 2 keys bound to it.", disableDefault.platform, disableDefault.site],
    unsaid: [disableKeys.none],
  },
  {
    name: "no default, one key",
    preset: preset(sitePreset, "site", 1),
    said: ["Accounts have 1 key bound to it."],
    unsaid: [disableDefault.platform, disableDefault.site],
  },
  {
    name: "no default, no keys",
    preset: preset(sitePreset, "site", 0),
    said: [disableKeys.none],
    unsaid: [disableDefault.platform, disableDefault.site],
  },
  {
    name: "a node that counts no keys",
    preset: preset(sitePreset, "site"),
    said: [],
    unsaid: [disableKeys.none, "bound to it."],
  },
])("says what disabling meets: $name", async ({ preset, said, unsaid }) => {
  ({ unmount } = await renderInDom(<DisablePresetDialog preset={preset} siteId={site} onClose={() => {}} />));
  const dialog = document.querySelector("dialog");
  await vi.waitFor(() => {
    for (const text of said) {
      expect(dialog?.textContent).toContain(text);
    }
  });
  for (const text of unsaid) {
    expect(dialog?.textContent).not.toContain(text);
  }
  expect(dialog?.querySelector<HTMLButtonElement>("button[type=submit]")?.disabled).toBe(false);
});
