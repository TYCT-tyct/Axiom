import { expect, test } from "@playwright/test";
import { mockConsole, signedInAs } from "./console-api.ts";

// Every animation plays whatever the system's motion setting (docs/frontend.md「动画」): the console's own CSS motion,
// which the shared components carry, is the same under reduced motion asked for and not.
for (const reducedMotion of ["no-preference", "reduce"] as const)
  test(`a hovered control's icon moves, over the design's duration (${reducedMotion})`, async ({ page }) => {
    await page.emulateMedia({ reducedMotion });
    signedInAs(await mockConsole(page));
    await page.goto("/console/routing-presets");
    await page.getByRole("button", { name: "新建预设" }).click();
    // the preset editor's add button: the shared ActionButton with a Glyph
    const button = page.getByRole("dialog").locator("button:has(svg g)").first();
    // the icon's parts: every one that is moved by hover
    const parts = button.locator("svg [data-p], svg g > *");
    const read = () =>
      parts.evaluateAll((els) =>
        els.map((el) => ({
          transform: getComputedStyle(el).transform,
          duration: getComputedStyle(el).transitionDuration,
        })),
      );
    const rest = await read();
    expect(rest.length).toBeGreaterThan(0);
    await button.hover();
    await expect.poll(async () => (await read()).some((part, i) => part.transform !== rest[i]?.transform)).toBe(true);
    // 250ms (tokens.css --dur): a global override to 1ms would show here as 0.001s
    for (const { duration } of await read()) expect(duration).toBe("0.25s");
  });
