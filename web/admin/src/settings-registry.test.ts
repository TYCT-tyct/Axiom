// @vitest-environment jsdom
import { expect, it } from "vitest";
import { settingKeys, settingSearchItems } from "./settings-registry";
import { i18n } from "./test-dom";

it("lists every registered setting for the palette, in words, with the page and the row to flash", async () => {
  await i18n.loadNamespaces("admin");
  const items = settingSearchItems(i18n.t);
  expect(items.map((item) => item.key)).toEqual(settingKeys);
  expect(items.find((item) => item.key === "registration.burst")).toEqual({
    key: "registration.burst",
    label: "Sign-ups per email domain at once",
    detail: "1 to 10,000.",
    page: "registration",
    search: { setting: "registration.burst" },
  });
  // a label that is its own key was not found in the words the palette has before any settings page loads
  for (const item of items) expect(item.label).not.toMatch(/^setting\./);
});
