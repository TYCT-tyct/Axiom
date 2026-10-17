import { AxeBuilder } from "@axe-core/playwright";
import { expect, type Page, test } from "@playwright/test";
import { suites } from "./suites.ts";

// The acceptance gate of docs/frontend.md「验收门槛」 as checks, for the console and the admin: WCAG 2.2 AA in both
// colour schemes and both languages, and no page wider than the window from 360px to 2560px, with the worst realistic
// data on screen. A table may scroll sideways inside its own box, which the page's width does not show: the buttons
// and links of its rows must still be in view without scrolling it.

const schemes = ["light", "dark"] as const;
const languages = ["zh", "en"] as const;
const widths = [360, 768, 1280, 2560] as const;

async function setup(page: Page, scheme: (typeof schemes)[number], language: (typeof languages)[number]) {
  await page.emulateMedia({ colorScheme: scheme, reducedMotion: "reduce" });
  await page.addInitScript((value) => localStorage.setItem("axiom.language", value), language);
}

for (const suite of suites) {
  for (const scene of suite.scenes) {
    const name = `${suite.app} ${scene.name}`;
    for (const scheme of schemes) {
      for (const language of languages) {
        test(`${name} has no WCAG 2.2 AA violation (${scheme}, ${language})`, async ({ page }) => {
          await setup(page, scheme, language);
          await suite.prepare(page, scene);
          await page.setViewportSize({ width: 1280, height: 800 });
          await scene.open(page);
          const result = await new AxeBuilder({ page })
            .withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "wcag22aa"])
            .analyze();
          expect(
            result.violations.map((v) => `${v.id}: ${v.nodes.map((n) => n.target.join(" ")).join(" | ")}`),
          ).toEqual([]);
        });
      }
    }

    for (const width of widths) {
      test(`${name} does not overflow the window at ${width}px with the worst-case data`, async ({ page }) => {
        await setup(page, "dark", width === 360 ? "en" : "zh");
        await suite.prepare(page, scene, "worst");
        await page.setViewportSize({ width, height: 900 });
        await scene.open(page);
        const overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
        expect(overflow).toBeLessThanOrEqual(0);
        const hidden = await page.evaluate(() =>
          [...document.querySelectorAll("table :is(a, button)")].flatMap((control) => {
            const box = control.getBoundingClientRect();
            const frame = control.closest("table")?.parentElement?.getBoundingClientRect();
            const out = frame && box.width > 0 && (box.left < frame.left - 0.5 || box.right > frame.right + 0.5);
            return out ? [control.getAttribute("aria-label") ?? control.textContent] : [];
          }),
        );
        expect(hidden).toEqual([]);
      });
    }
  }
}
