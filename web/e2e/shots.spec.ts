import { mkdirSync } from "node:fs";
import { join } from "node:path";
import { type Page, test } from "@playwright/test";
import { applyBrand, brands } from "./brand.ts";
import { suites } from "./suites.ts";

// The screenshots of the acceptance gate (docs/frontend.md「验收门槛」) for a person to look at: every screen of each app at 360, 768,
// 1280 and 2560px, in both colour schemes and both languages, then with extreme brand colours. They are not a test
// of anything and not kept in the repository; they are written only where AXIOM_SHOTS_DIR names a directory.
//   AXIOM_SHOTS_DIR=/tmp/shots just web   (or: pnpm --dir web e2e shots, optionally narrowed with -g "admin users")

const directory = process.env.AXIOM_SHOTS_DIR;
test.skip(!directory, "set AXIOM_SHOTS_DIR to take the screenshots");
test.describe.configure({ mode: "parallel" });

const widths = [360, 768, 1280, 2560] as const;

async function shoot(page: Page, name: string) {
  mkdirSync(directory as string, { recursive: true });
  await page.screenshot({ path: join(directory as string, `${name}.png`), fullPage: true });
}

for (const suite of suites) {
  for (const scene of suite.scenes) {
    const name = `${suite.app}-${scene.name}`;
    for (const width of widths) {
      for (const scheme of ["light", "dark"] as const) {
        for (const language of ["zh", "en"] as const) {
          test(`${name} ${width} ${scheme} ${language}`, async ({ page }) => {
            await page.emulateMedia({ colorScheme: scheme, reducedMotion: "reduce" });
            await page.addInitScript((value) => localStorage.setItem("axiom.language", value), language);
            await suite.prepare(page, scene, "usual");
            await page.setViewportSize({ width, height: 900 });
            await scene.open(page);
            await shoot(page, `${name}-${width}-${scheme}-${language}`);
          });
        }
      }
    }

    // The same screen with the worst realistic data: the longest names the contract allows, the biggest amounts, a debt.
    for (const width of [360, 1280] as const) {
      test(`worst ${name} ${width}`, async ({ page }) => {
        await page.emulateMedia({ colorScheme: "dark", reducedMotion: "reduce" });
        await suite.prepare(page, scene, "worst");
        await page.setViewportSize({ width, height: 900 });
        await scene.open(page);
        await shoot(page, `worst-${name}-${width}`);
      });
    }
  }

  for (const [brand, color] of Object.entries(brands)) {
    for (const scheme of ["light", "dark"] as const) {
      for (const scene of suite.scenes.filter((s) => suite.brands.includes(s.name))) {
        test(`brand ${brand} ${suite.app}-${scene.name} ${scheme}`, async ({ page }) => {
          await page.emulateMedia({ colorScheme: scheme, reducedMotion: "reduce" });
          await suite.prepare(page, scene);
          await page.setViewportSize({ width: 1280, height: 800 });
          await scene.open(page);
          await applyBrand(page, color);
          await shoot(page, `brand-${brand}-${suite.app}-${scene.name}-${scheme}`);
        });
      }
    }
  }
}
