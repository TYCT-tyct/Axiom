import { type Browser, expect, type Page, test } from "@playwright/test";
import { collectErrors } from "./collect-errors.ts";
import { loop, open, pause, running, scrollTo, setHidden, siteSignIn } from "./fx-helpers.ts";

// The home page's animations must look and move the same on every machine (#747): a display scaled by Windows, a fast
// monitor, a slow processor, and several effects at once. The cases here are the ones that do not depend on the
// machine's speed: they read canvas sizes and the engine's own state, and drive time through the page's clock.

// A page on a display with the given scaling.
async function onDisplay(browser: Browser, deviceScaleFactor: number, size = { width: 1440, height: 900 }) {
  const context = await browser.newContext({ deviceScaleFactor, viewport: size, locale: "zh-CN" });
  return { context, page: await context.newPage() };
}

const canvases = (page: Page) =>
  page.evaluate(() =>
    [...document.querySelectorAll<HTMLCanvasElement>(".tr > canvas")]
      .filter((c) => c.clientWidth > 0)
      .map((c) => ({
        name: c.classList.contains("sea") ? "sea" : "wh",
        w: c.width,
        h: c.height,
        cw: c.clientWidth,
        ch: c.clientHeight,
      })),
  );

// the whale's canvas has something on it
const painted = (page: Page) =>
  page.locator(".tr > canvas.wh").evaluate((canvas: HTMLCanvasElement) => {
    const g = canvas.getContext("2d", { willReadFrequently: true }) as CanvasRenderingContext2D;
    return g.getImageData(0, 0, canvas.width, canvas.height).data.some((value, i) => i % 4 === 3 && value > 0);
  });

// The engines draw at the display's pixel ratio, up to 2 (the sea, the entrance and the departure cap it so): a canvas
// whose backing size is not its CSS size times that ratio is drawn blurred or its character grid is mis-sized. The
// whale's canvas takes the integer part, the sea's rounds: they never differ by more than one pixel.
for (const [dpr, size] of [
  [1, { width: 1440, height: 900 }],
  [1.25, { width: 1440, height: 900 }],
  [1.5, { width: 1437, height: 853 }],
  [1.75, { width: 1280, height: 720 }],
  [2, { width: 1440, height: 900 }],
  [3, { width: 1440, height: 900 }],
] as const) {
  test(`at display scaling ${dpr} the sea and the whale are drawn at ${Math.min(dpr, 2)} pixels per CSS pixel`, async ({
    browser,
  }) => {
    const { context, page } = await onDisplay(browser, dpr, size);
    const errors = collectErrors(page);
    await open(page);
    await running(page);
    await page.keyboard.type("deepseek");
    await expect.poll(() => painted(page), { timeout: 30_000 }).toBe(true);
    const scale = Math.min(dpr, 2);
    const drawn = await canvases(page);
    expect(drawn.map((c) => c.name).sort()).toEqual(["sea", "wh"]);
    for (const c of drawn) {
      expect(c.cw).toBe(size.width);
      expect(Math.abs(c.w - c.cw * scale)).toBeLessThanOrEqual(1);
      expect(Math.abs(c.h - c.ch * scale)).toBeLessThanOrEqual(1);
    }
    expect(errors).toEqual([]);
    await context.close();
  });
}

// The sea steps its world in fixed 1/60 s steps however the frames come (ocean-orig.js tick), so a 144 Hz monitor must
// not run it faster: the same five seconds, delivered at each rate, leave every shark where the 60 Hz run leaves it.
test("the sea's sharks move the same in five seconds at 30, 60, 120 and 144 frames a second", async ({ page }) => {
  await page.addInitScript(() => {
    let seed = 12345;
    Math.random = () => {
      seed = (seed + 0x6d2b79f5) | 0;
      let t = Math.imul(seed ^ (seed >>> 15), 1 | seed);
      t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
      return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
    };
  });
  await open(page);
  await running(page);
  const runs = await page.evaluate(() => {
    type Engine = {
      world: { time: number; sharks: { nodes: Float32Array }[] };
      tick(dt: number, draw?: boolean): void;
      destroy(): void;
    };
    const { Engine } = (
      window as unknown as { AxiomOceanOrig: { Engine: new (c: HTMLCanvasElement, r: object[], o: object) => Engine } }
    ).AxiomOceanOrig;
    const roster = [
      { kind: "hammerhead", size: 1 },
      { kind: "classic", size: 0.72 },
    ];
    const out: Record<number, { time: number; heads: number[][] }> = {};
    for (const hz of [30, 60, 120, 144]) {
      let state = 7;
      Math.random = () => {
        state = (state * 1664525 + 1013904223) >>> 0;
        return state / 4294967296;
      };
      const canvas = document.createElement("canvas");
      canvas.style.cssText = "position:fixed;left:0;top:0;width:900px;height:600px;opacity:0;pointer-events:none";
      document.body.append(canvas);
      const engine = new Engine(canvas, roster, { whale: null, hold: true, sizeK: 0.64, maxLen: 640, virtualScale: 1 });
      for (let frame = 0; frame < 5 * hz; frame++) engine.tick(1 / hz, false);
      out[hz] = { time: engine.world.time, heads: engine.world.sharks.map((s) => [s.nodes[0] ?? 0, s.nodes[1] ?? 0]) };
      engine.destroy();
      canvas.remove();
    }
    return out;
  });
  for (const hz of [30, 120, 144]) {
    // one world step more or less at the end of the five seconds, nothing the rate could multiply
    expect(Math.abs((runs[hz]?.time ?? 0) - (runs[60]?.time ?? 0))).toBeLessThanOrEqual(1 / 60 + 1e-6);
    expect(Math.abs((runs[hz]?.time ?? 0) - 5)).toBeLessThan(0.1);
    runs[hz]?.heads.forEach((head, i) => {
      const base = runs[60]?.heads[i] ?? [0, 0];
      expect(Math.hypot((head[0] ?? 0) - (base[0] ?? 0), (head[1] ?? 0) - (base[1] ?? 0))).toBeLessThan(25);
    });
  }
});

// The whale's own frames cap a step at 0.05 s, so it keeps its pace down to 20 frames a second. On a machine that cannot
// draw so often it goes slower, so the case is run on the page's clock, at 60 frames a second whatever the machine.
test("the whale is on the water within three seconds of the word, on a clock that ticks at 60 frames a second", async ({
  page,
}) => {
  await open(page);
  await running(page);
  await scrollTo(page, 6);
  await page.clock.install();
  const loaded = page.waitForRequest(/\/assets\/whale-[\w-]+\.js$/);
  await page.keyboard.type("deepseek");
  await loaded;
  let seen = 0;
  for (; seen < 3000 && !(await painted(page).catch(() => false)); seen += 100) {
    await page.clock.runFor(100);
    await page.waitForTimeout(20);
  }
  expect(seen).toBeLessThan(3000);
});

// Every animation plays whatever the system's motion setting, the whale and the sea in the same picture.
test("under reduced motion the sea changes between two looks and the whale comes", async ({ page }) => {
  await page.emulateMedia({ reducedMotion: "reduce" });
  await open(page);
  await running(page);
  const look = () =>
    page
      .locator("canvas.sea:not(.cover)")
      .evaluate((canvas: HTMLCanvasElement) => canvas.toDataURL().length + canvas.toDataURL().slice(-200));
  const before = await look();
  await expect.poll(look).not.toBe(before);
  await page.keyboard.type("deepseek");
  await expect.poll(() => painted(page), { timeout: 30_000 }).toBe(true);
});

// A tab hidden for ten seconds: the loop holds no frame, and the sea goes on from where it was, one short step first.
test("a tab hidden for ten seconds rests, and its sea moves again as it comes back", async ({ page }) => {
  await open(page);
  await running(page);
  await page.clock.install();
  await setHidden(page, true);
  await pause(page);
  const rested = await loop(page);
  expect(rested.pending).toBe(false);
  await page.clock.fastForward(10_000);
  expect(await loop(page)).toEqual(rested);
  await setHidden(page, false);
  await page.clock.runFor(500);
  expect((await loop(page)).frames).toBeGreaterThan(rested.frames);
  expect((await loop(page)).pending).toBe(true);
});

// The effects together: the whale on the sea, the page scrolled under it, and the departure pressed while it is out.
test("the whale, a scroll and the departure at once leave no error, and the sign-in page's sea runs", async ({
  page,
}) => {
  test.setTimeout(90_000);
  const errors = collectErrors(page);
  await open(page);
  await running(page);
  await page.keyboard.type("deepseek");
  await expect.poll(() => painted(page), { timeout: 30_000 }).toBe(true);
  const frames = (await loop(page)).frames;
  await scrollTo(page, 3);
  await scrollTo(page, 6);
  // back to the first screen, where the header carries the way to sign in (scrolled, it is the compact island)
  await page.locator(".hs").evaluate((el) => el.scrollTo({ top: 0, behavior: "instant" }));
  await expect.poll(async () => (await loop(page)).frames).toBeGreaterThan(frames);
  await siteSignIn(page).click();
  await expect(page).toHaveURL(/\/console\/sign-in$/);
  await expect(page.getByRole("heading", { level: 1 })).toBeVisible();
  await running(page);
  const arrived = (await loop(page)).frames;
  await expect.poll(async () => (await loop(page)).frames).toBeGreaterThan(arrived);
  // the sign-in page asks who is signed in and is told no (401), which the browser logs
  expect(errors.filter((e) => !e.includes("401"))).toEqual([]);
});
