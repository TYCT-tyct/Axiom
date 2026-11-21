import { expect, type Page, test } from "@playwright/test";
import { apps } from "./apps.ts";
import { mockConsole } from "./console-api.ts";
import { expectMoving, open, pauseAhead, running, scrollTo, shown, siteSignIn } from "./fx-helpers.ts";
import { pageHeading } from "./page-heading.ts";

// Home <-> sign-in tiles transitions (port design §9, trans.js.txt STYLES.tiles):
// - return from sign-in pages via clicking .lg .brand, pressing Escape (except inside a non-empty input/textarea),
//   or browser Back, reversible midway
// - midway reversal of the website's forward departure via Escape
// - sea-floor Axiom wordmark tiles flying into the sign-in panel via frame.__axCast.wordTiles()
const signInPage = `${apps[0].url.replace(/\/$/, "")}/console/sign-in`;
const departChunk = /\/assets\/depart-[\w-]+\.js$/;
const siteOverlay = (page: Page) => page.locator(".tr > canvas[aria-hidden]:last-child");
const loginOverlay = (page: Page) => page.locator(".lg > canvas[aria-hidden]:last-child");

async function watchEveryPolicy(page: Page) {
  const seen: string[] = [];
  await page.exposeFunction("reportTransitionPolicy", (line: string) => seen.push(line));
  await page.addInitScript(() =>
    document.addEventListener("securitypolicyviolation", (e) =>
      (window as unknown as { reportTransitionPolicy: (line: string) => void }).reportTransitionPolicy(
        `${location.pathname} ${e.violatedDirective} ${e.blockedURI}`,
      ),
    ),
  );
  return seen;
}

const hasInk = (canvas: ReturnType<Page["locator"]>) =>
  canvas.evaluate((element) => {
    const c = element as HTMLCanvasElement;
    if (!c.width || !c.height) return false;
    const { data } = (c.getContext("2d") as CanvasRenderingContext2D).getImageData(0, 0, c.width, c.height);
    return data.some((value, i) => i % 4 === 3 && value > 20);
  });

const waitObserved = (canvas: ReturnType<Page["locator"]>) =>
  canvas.evaluate((el) => new Promise<void>((seen) => new IntersectionObserver(() => seen()).observe(el)));

test("clicking the Axiom mark on sign-in plays the return tiles transition back to home, CSP-clean", async ({
  page,
}) => {
  const violations = await watchEveryPolicy(page);
  await open(page);
  await running(page);
  await siteSignIn(page).click();
  await expect(page).toHaveURL(/\/console\/sign-in$/);
  await pageHeading(page);
  await page.clock.install();
  await pauseAhead(page, 100);
  await page.locator(".lg a.brand").click();
  const overlay = loginOverlay(page);
  await expect(overlay).toBeAttached();
  await waitObserved(overlay);
  await page.clock.runFor(200);
  expect(await hasInk(overlay)).toBe(true);
  await expect(page).toHaveURL(/\/console\/sign-in$/);
  await page.clock.runFor(350);
  await expect(page).toHaveURL(new RegExp(`^${apps[0].url.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}$`));
  await pageHeading(page);
  await expect(page.locator(".tr")).toHaveAttribute("data-enter", "done");
  expect(violations).toEqual([]);
});

// The home page come back to is settled, with no entrance to let its sea go: the sea moves all the same.
test("the home page come back to from sign-in is settled and its sea moves", async ({ page }) => {
  await open(page);
  await running(page);
  await siteSignIn(page).click();
  await expect(page).toHaveURL(/\/console\/sign-in$/);
  await pageHeading(page);
  await page.locator(".lg a.brand").click();
  await expect(page).toHaveURL(new RegExp(`^${apps[0].url.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}$`));
  await expect(page.locator(".tr")).toHaveAttribute("data-enter", "done");
  await expectMoving(page, page.locator(".tr canvas.sea:not(.cover)"));
  await expect(page.locator(".tr")).toHaveAttribute("data-enter", "done");
});

test("Escape returns to home unless typing in a non-empty input, and works under reduced motion", async ({ page }) => {
  await page.emulateMedia({ reducedMotion: "reduce" });
  await mockConsole(page);
  await page.goto(signInPage);
  await pageHeading(page);
  const email = page.getByLabel("邮箱");
  await email.fill("someone@example.com");
  await email.press("Escape");
  await expect(loginOverlay(page)).toHaveCount(0);
  await expect(page).toHaveURL(/\/console\/sign-in$/);
  await email.fill("");
  await email.press("Escape");
  await expect(loginOverlay(page)).toBeAttached();
  await expect(page).toHaveURL(new RegExp(`^${apps[0].url.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}$`));
});

test("browser Back from a website arrival plays the return transition and can be reversed midway via Forward", async ({
  page,
}) => {
  await open(page);
  await running(page);
  await siteSignIn(page).click();
  await expect(page).toHaveURL(/\/console\/sign-in$/);
  await pageHeading(page);
  await page.clock.install();
  await pauseAhead(page, 100);
  // First Back starts the return transition; Forward midway reverses it back onto sign-in.
  await page.evaluate(() => history.back());
  const overlay = loginOverlay(page);
  await expect(overlay).toBeAttached();
  await waitObserved(overlay);
  await page.clock.runFor(220);
  expect(await hasInk(overlay)).toBe(true);
  await page.evaluate(() => history.forward());
  await page.clock.runFor(350);
  await expect(overlay).toHaveCount(0);
  await expect(page).toHaveURL(/\/console\/sign-in$/);
  // Second Back lets the 500 ms return complete and lands back on the home page.
  await page.evaluate(() => history.back());
  await expect(overlay).toBeAttached();
  await waitObserved(overlay);
  await page.clock.runFor(550);
  await expect(page).toHaveURL(new RegExp(`^${apps[0].url.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}$`));
});

test("clicking the form panel midway reverses a brand-click return back onto sign-in", async ({ page }) => {
  await mockConsole(page);
  await page.goto(signInPage);
  await pageHeading(page);
  await page.clock.install();
  await pauseAhead(page, 100);
  await page.locator(".lg a.brand").click();
  const overlay = loginOverlay(page);
  await expect(overlay).toBeAttached();
  await waitObserved(overlay);
  await page.clock.runFor(220);
  expect(await hasInk(overlay)).toBe(true);
  await page.locator(".lg-side").click();
  await page.clock.runFor(400);
  await expect(overlay).toHaveCount(0);
  expect(await page.locator(".lg-form").evaluate((el) => (el as HTMLElement).style.opacity)).toBe("");
  await expect(page).toHaveURL(/\/console\/sign-in$/);
});

test("Escape midway reverses the website's forward departure back to home", async ({ page }) => {
  await open(page);
  await running(page);
  await page.clock.install();
  await pauseAhead(page, 100);
  await siteSignIn(page).click();
  const overlay = siteOverlay(page);
  await expect(overlay).toBeAttached();
  await waitObserved(overlay);
  await page.clock.runFor(200);
  await expect(siteSignIn(page)).toBeHidden();
  await page.keyboard.press("Escape");
  await page.clock.runFor(400);
  await expect(overlay).toHaveCount(0);
  await expect(siteSignIn(page)).toBeVisible();
  await page.clock.runFor(600);
  await expect(page).toHaveURL(new RegExp(`^${apps[0].url.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}$`));
});

test("return still goes home if the transition chunk fails to load", async ({ page }) => {
  const unhandled: string[] = [];
  page.on("pageerror", (error) => unhandled.push(error.message));
  await page.route(departChunk, (route) => route.fulfill({ status: 404, body: "" }));
  await mockConsole(page);
  await page.goto(signInPage);
  await pageHeading(page);
  await page.locator(".lg a.brand").click();
  await expect(page).toHaveURL(new RegExp(`^${apps[0].url.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}$`));
  expect(unhandled).toEqual([]);
});

test("leaving from the sea floor flies __axCast.wordTiles() into the sign-in panel and restores them on reversal", async ({
  page,
}) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await open(page);
  await running(page);
  await scrollTo(page, 6);
  await shown(page, 6);
  // If PR #891's Clawd engine is mounted on .tr, observe its real __axCast.wordTiles(); otherwise attach a faithful
  // spy implementing clawd-cast.js's wordTiles() contract so the flight and restore path is verified end to end.
  const installed = await page.evaluate(() => {
    const frame = document.querySelector(".tr") as HTMLElement & {
      __axCast?: {
        wordTiles?: () => {
          tiles: { x: number; y: number; w: number; h: number; m: number; li: number }[];
          tile: string;
          ink: string;
          gf: number;
          hide: () => void;
          show: () => void;
        } | null;
      };
    };
    const log = { calls: 0, hidden: 0, shown: 0, tileCount: 0 };
    (window as unknown as { __wordLog: typeof log }).__wordLog = log;
    if (frame.__axCast?.wordTiles) {
      const orig = frame.__axCast.wordTiles.bind(frame.__axCast);
      frame.__axCast.wordTiles = () => {
        log.calls++;
        const res = orig();
        if (!res) return null;
        log.tileCount = res.tiles.length;
        const hide = res.hide;
        const show = res.show;
        return {
          ...res,
          hide: () => {
            log.hidden++;
            hide();
          },
          show: () => {
            log.shown++;
            show();
          },
        };
      };
      return "real";
    }
    const art = frame.querySelector(".hfm6 canvas.art")?.getBoundingClientRect();
    const y0 = art ? Math.max(40, art.top + 20) : 640;
    const x0 = art ? Math.max(40, art.left + 80) : 120;
    const tiles = Array.from({ length: 40 }, (_, i) => ({
      x: x0 + (i % 10) * 14,
      y: y0 + Math.floor(i / 10) * 14,
      w: 10,
      h: 10,
      m: 0.9,
      li: i % 5,
    }));
    frame.__axCast = {
      wordTiles: () => {
        log.calls++;
        log.tileCount = tiles.length;
        return {
          tiles,
          tile: "#556677",
          ink: "#112233",
          gf: 8,
          hide: () => {
            log.hidden++;
          },
          show: () => {
            log.shown++;
          },
        };
      },
    };
    return "stub";
  });
  expect(["real", "stub"]).toContain(installed);
  await page.clock.install();
  await pauseAhead(page, 100);
  await page.locator(".hfm6 a[data-go]").first().click();
  const overlay = siteOverlay(page);
  await expect(overlay).toBeAttached();
  await waitObserved(overlay);
  await page.clock.runFor(200);
  expect(await hasInk(overlay)).toBe(true);
  const mid = await page.evaluate(
    () =>
      (window as unknown as { __wordLog: { calls: number; hidden: number; shown: number; tileCount: number } })
        .__wordLog,
  );
  expect(mid.calls).toBe(1);
  expect(mid.tileCount).toBeGreaterThan(0);
  expect(mid.hidden).toBe(1);
  expect(mid.shown).toBe(0);
  // Reversing midway restores the seabed letters via wm.show()
  await page.keyboard.press("Escape");
  await page.clock.runFor(400);
  await expect(overlay).toHaveCount(0);
  const after = await page.evaluate(() => (window as unknown as { __wordLog: { shown: number } }).__wordLog);
  expect(after.shown).toBe(1);
});
