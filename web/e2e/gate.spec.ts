import { AxeBuilder } from "@axe-core/playwright";
import { expect, type Page, test } from "@playwright/test";
import { applyBrand, brands } from "./brand.ts";
import { siteSuite } from "./site-scenes.ts";
import { suites } from "./suites.ts";

// The acceptance gate of docs/frontend.md「验收门槛」 as checks, for the console, the admin and the website: WCAG 2.2 AA in both
// colour schemes and both languages, and no page wider than the window from 360px to 2560px, with the worst realistic
// data on screen. A table may scroll sideways inside its own box, which the page's width does not show: the buttons
// and links of its rows must still be in view without scrolling it.

const schemes = ["light", "dark"] as const;
const languages = ["zh", "en"] as const;
const widths = [360, 768, 1280, 2560] as const;
const axeTags = ["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "wcag22aa"];

// axe's time grows with the DOM. A table repeats one row component, and the usual fixtures cycle every row state within
// 11 rows (a disabled user, a free or failed call), so 12 rows cover every state the table can show. Scanning the whole
// 50-row admin calls list cost 4.5 s idle and 15 s under a 2x CPU load, and timed out at 30 s in CI (#624). The overflow
// checks below still lay out every row with the worst data. The gate asserts violations only, so axe need not build or
// return its passes and inapplicable results either.
const axeRows = 12;
const axe = (page: Page) =>
  new AxeBuilder({ page })
    .options({ resultTypes: ["violations"] })
    .withTags(axeTags)
    .exclude(`tbody tr:nth-child(n + ${axeRows + 1})`);

// Nothing here is shared: every check gets its own page, its own mocked API state (suite.prepare) and the one server
// Playwright starts for the whole run. In the default mode a single worker took the whole file by itself, 8.9 of the
// 9.1 minutes a full run took.
test.describe.configure({ mode: "parallel" });

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
          const result = await axe(page).analyze();
          expect(
            result.violations.map((v) => `${v.id}: ${v.nodes.map((n) => n.target.join(" ")).join(" | ")}`),
          ).toEqual([]);
        });
      }
    }

    if (suite.brands.includes(scene.name)) {
      for (const [brand, color] of Object.entries(brands)) {
        for (const scheme of schemes) {
          test(`${name} has no contrast violation with the ${brand} brand colour (${scheme})`, async ({ page }) => {
            await setup(page, scheme, "en");
            await suite.prepare(page, scene);
            await page.setViewportSize({ width: 1280, height: 800 });
            await scene.open(page);
            await applyBrand(page, color);
            const result = await axe(page).withRules(["color-contrast"]).analyze();
            expect(result.violations.map((v) => v.nodes.map((n) => n.target.join(" ")).join(" | "))).toEqual([]);
          });
        }
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
        // A table too wide for its box scrolls inside it, so a control in a middle column may wait off screen until the
        // box is scrolled; a row's own buttons (a sticky actions cell) and every control of a box that cannot scroll
        // must be on screen (docs/frontend.md「表格与标识符」).
        const hidden = await page.evaluate(() =>
          [...document.querySelectorAll("table :is(a, button)")].flatMap((control) => {
            const box = control.getBoundingClientRect();
            const scroller = control.closest("table")?.parentElement;
            const frame = scroller?.getBoundingClientRect();
            const out = frame && box.width > 0 && (box.left < frame.left - 0.5 || box.right > frame.right + 0.5);
            const cell = control.closest("td, th");
            const sticky = cell !== null && getComputedStyle(cell).position === "sticky";
            const scrolls = scroller !== null && scroller !== undefined && scroller.scrollWidth > scroller.clientWidth;
            return out && (sticky || !scrolls) ? [control.getAttribute("aria-label") ?? control.textContent] : [];
          }),
        );
        expect(hidden).toEqual([]);
        // A header keeps its own line at every width: a narrow numeric column must not break 「调用」 in two.
        const wrapped = await page.evaluate(() =>
          [...document.querySelectorAll("th")]
            .filter((th) => getComputedStyle(th).whiteSpace !== "nowrap")
            .map((th) => th.textContent),
        );
        expect(wrapped).toEqual([]);
        // The website has no sidebar: its first nav is the header's.
        if (width === 1280 && suite !== siteSuite) {
          // The sidebar's column reaches the end of the document, not only the end of the window. The console's sidebar
          // sticks below the bar, as tall as the window, while the document scrolls past it; the admin's column holds a
          // window-tall box of links.
          const sidebar = await page.evaluate(() => {
            const nav = [...document.querySelectorAll('nav[aria-label="主导航"]')].find((n) =>
              n.checkVisibility({ visibilityProperty: true }),
            );
            // A modal dialog scrolls its own body over a locked page; the column behind it is only viewport-tall.
            if (!nav || document.querySelector("dialog[open]")) {
              return null;
            }
            const aside = nav.closest("aside");
            const column = aside?.parentElement ?? nav;
            return {
              bottom: column.getBoundingClientRect().bottom + window.scrollY,
              page: document.documentElement.scrollHeight,
              sticky: aside && {
                position: getComputedStyle(aside).position,
                gap: Math.abs(aside.getBoundingClientRect().bottom - window.innerHeight),
              },
            };
          });
          if (sidebar) {
            expect(sidebar.bottom).toBeGreaterThanOrEqual(sidebar.page - 1);
          }
          if (sidebar?.sticky) {
            expect(sidebar.sticky.position).toBe("sticky");
            expect(sidebar.sticky.gap).toBeLessThanOrEqual(1);
          }
        }
      });
    }
  }
}
