import { expect, test } from "@playwright/test";
import { open } from "./fx-helpers.ts";

// The glass behind the first screen's lede is a ::before with `backdrop-filter`. lightningcss 1.33.0 dropped that
// declaration from the build when a hand-written `-webkit-` copy followed it in the rule (#882), and Chrome and Firefox
// then showed no blur. Delete this with the board converter's workaround once the issue is closed.
test("the first screen's glass blurs what is behind it in the built page", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await open(page);
  const lens = page.locator(".ho .lede.lens").first();
  await expect(lens).toBeVisible();
  const filter = await lens.evaluate((el) => getComputedStyle(el, "::before").backdropFilter);
  expect(filter).toContain("blur");
});
