import { mkdirSync } from "node:fs";
import { join } from "node:path";
import { type Page, test } from "@playwright/test";
import { adminUrl } from "./apps.ts";
import { applyBrand, brands } from "./brand.ts";
import { suites } from "./suites.ts";

// The screenshots of the acceptance gate (docs/frontend.md「验收门槛」) for a person to look at: every screen of each app at 360, 768,
// 1280 and 2560px, in both colour schemes and both languages, then with extreme brand colours. They are not a test
// of anything and not kept in the repository; they are written only where AXIOM_SHOTS_DIR names a directory.
//   AXIOM_SHOTS_DIR=/tmp/shots just web   (or: pnpm --dir web e2e shots, optionally narrowed with -g "admin users")

const directory = process.env.AXIOM_SHOTS_DIR;
test.skip(!directory, "set AXIOM_SHOTS_DIR to take the screenshots");
// The machine may run in UTC; a viewer is somewhere. Take the screenshots in one named zone so every time in them is a
// local time with its zone, which is what the product shows.
test.use({ timezoneId: "Asia/Shanghai" });
test.describe.configure({ mode: "parallel" });

// The apps are stamped with the version of the program built from this tree, and the page asks the API for it to notice
// a deployment (packages/app/src/version.tsx). The mocks answer AXIOM_VERSION, empty when the shots are taken by hand,
// so every screenshot showed the "Axiom has been updated" banner. Read the stamp from the entry script the server
// serves, which is the build under test, so the shots show the page as users see it.
if (directory && !process.env.AXIOM_VERSION) {
  const html = await (await fetch(adminUrl())).text();
  const entry = html.match(/<script type="module"[^>]*src="([^"]+)"/)?.[1];
  const stamp = entry && (await (await fetch(adminUrl(entry))).text()).match(/axiom v[^"'`]+/)?.[0];
  if (!stamp) {
    throw new Error("the admin build carries no version stamp: run just web-embed first");
  }
  process.env.AXIOM_VERSION = stamp;
}

const widths = [360, 768, 1280, 2560] as const;

async function shoot(page: Page, name: string, fullPage = true) {
  mkdirSync(directory as string, { recursive: true });
  await page.screenshot({ path: join(directory as string, `${name}.png`), fullPage });
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
        // A full-page capture of the worst data is tens of thousands of pixels tall and puts a modal far from its
        // top; the viewport is where the dialog can be judged.
        if (await page.getByRole("dialog").isVisible()) {
          await shoot(page, `worst-${name}-${width}-dialog`, false);
        }
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
