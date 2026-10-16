import { AxeBuilder } from "@axe-core/playwright";
import { expect, type Page, test } from "@playwright/test";
import { suites } from "./suites.ts";

// The acceptance gate of docs/frontend.md「验收门槛」 as checks, for the console and the admin: WCAG 2.2 AA in both
// colour schemes and both languages, and no page wider than the window from 360px to 2560px, with the worst realistic
// data on screen.

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
      });
    }
  }
}
