import { expect, type Page, test } from "@playwright/test";
import { apps } from "./apps.ts";
import { mockConsole } from "./console-api.ts";
import {
  departOnly,
  expectMovingNow,
  loop,
  open,
  pause,
  pauseAhead,
  running,
  setHidden,
  siteSignIn as signIn,
} from "./fx-helpers.ts";
import { knownEmail, knownPassword } from "./mock-auth.ts";
import { pageHeading } from "./page-heading.ts";

// The console's sign-in pages (port design §9): the sea and the solid on it, loaded after the first content, and the
// title's decode, which plays once for a visitor the website's departure brought (its one-shot marker) and never for a
// direct visit.
// the console's engine chunk, not the frame's (login-frame-*.js), nor the website's own, which its homepage asks for
// once its first screen is drawn (site/src/home/sign-in.tsx)
const loginCode = /\/console\/assets\/login-[\w-]{8}\.js$/;
const signInPage = `${apps[0].url.replace(/\/$/, "")}/console/sign-in`;
const stageCanvas = (page: Page) => page.locator(".lg-sea > canvas:not(.sea)");
// The sharks are called in and taken in before the solid is whole: up to 3.4 s for the far ones to give up, and a second
// to be taken in.
const formed = { timeout: 15_000 };

// Whether the decode played: the title's characters hidden for it, and its canvas drawn on (a hidden title alone could
// be the veil, lifted by its timer), recorded from the document's first script.
async function watchDecode(page: Page) {
  await page.addInitScript(() => {
    const seen = { hidden: false, drawn: false };
    (window as unknown as { decode: typeof seen }).decode = seen;
    new MutationObserver((records) => {
      for (const record of records) {
        const target = record.target as HTMLElement;
        if (target.matches?.(".lg-title .ch") && target.style.color === "transparent") seen.hidden = true;
      }
    }).observe(document, { subtree: true, attributes: true, attributeFilter: ["style"] });
    for (const name of ["fillText", "fill", "fillRect"] as const) {
      const draw = CanvasRenderingContext2D.prototype[name] as (...args: unknown[]) => void;
      CanvasRenderingContext2D.prototype[name] = function (this: CanvasRenderingContext2D, ...args: unknown[]) {
        if (this.canvas.classList.contains("lg-dc")) seen.drawn = true;
        return draw.apply(this, args);
      } as never;
    }
  });
  return () => page.evaluate(() => (window as unknown as { decode: { hidden: boolean; drawn: boolean } }).decode);
}

test("arriving through the departure decodes the title once, its code asked for at once; a direct visit does not", async ({
  page,
}) => {
  await departOnly(page);
  const decoded = await watchDecode(page);
  const asked: boolean[] = [];
  await page.route(loginCode, async (route) => {
    asked.push(await page.evaluate(() => performance.getEntriesByName("first-contentful-paint").length > 0));
    await route.fallback();
  });
  await open(page);
  await signIn(page).click();
  await expect(page).toHaveURL(/\/console\/sign-in$/);
  await pageHeading(page);
  await running(page);
  await expect(stageCanvas(page)).toBeAttached();
  await expect(page.locator(".lg-title .ch[style*=transparent]")).toHaveCount(0);
  expect(await decoded()).toEqual({ hidden: true, drawn: true });
  expect(asked).toHaveLength(1);
  // the marker was taken: the same page again is a direct visit, whose effects wait for its first content
  expect(await page.evaluate(() => sessionStorage.getItem("axiom:arrival"))).toBeNull();
  await page.reload();
  await pageHeading(page);
  await running(page);
  await pause(page);
  expect(await decoded()).toEqual({ hidden: false, drawn: false });
  expect(asked).toEqual([asked[0], true]);
});

// An arrival asks for the engine at once, so it may run before the page's question is in the frame: the title still
// decodes once it comes.
test("an arrival whose engine runs before its question comes still decodes the title", async ({ page }) => {
  const decoded = await watchDecode(page);
  await mockConsole(page);
  let release!: () => void;
  const held = new Promise<void>((resolve) => (release = resolve));
  await page.route(/\/assets\/sign-in-[\w-]{8}\.js$/, async (route) => {
    await held;
    await route.fallback();
  });
  await page.goto(apps[0].url);
  await page.evaluate(() => sessionStorage.setItem("axiom:arrival", String(Date.now())));
  await page.goto(signInPage);
  // the engine runs (the solid's canvas is Morph's) while the page's code is still held
  await expect(stageCanvas(page)).toBeAttached();
  await expect(page.locator(".lg-title")).toHaveCount(0);
  release();
  await expect.poll(decoded).toEqual({ hidden: true, drawn: true });
});

// An arrival shows the frame whole: its parts wait unseen for the sea's code, then come in together, not one by one.
test("an arrival's sign-in page comes in whole once the sea's code has come", async ({ page }) => {
  await mockConsole(page);
  let release!: () => void;
  const held = new Promise<void>((resolve) => (release = resolve));
  await page.route(loginCode, async (route) => {
    await held;
    await route.fallback();
  });
  await page.goto(apps[0].url);
  await page.evaluate(() => sessionStorage.setItem("axiom:arrival", String(Date.now())));
  // the page's clock stands still: what shows the frame is the code coming, not the wait's limit
  await page.clock.install();
  await page.goto(signInPage);
  const parts = page.locator(".lg > .lg-sea, .lg > .lg-top, .lg > .lg-side");
  await expect(parts).toHaveCount(3);
  for (const part of await parts.all()) await expect(part).toHaveCSS("opacity", "0");
  release();
  await expect(stageCanvas(page)).toBeAttached();
  for (const part of await parts.all()) await expect(part).toHaveCSS("opacity", "1");
});

test("the sign-in page's sea and stage rest while the tab is hidden, and leave nothing behind once signed in", async ({
  page,
}) => {
  await mockConsole(page);
  await page.goto(signInPage);
  await pageHeading(page);
  await running(page);
  await setHidden(page, true);
  const frames = (await loop(page)).frames;
  await pause(page);
  expect((await loop(page)).frames).toBe(frames);
  expect((await loop(page)).pending).toBe(false);
  await setHidden(page, false);
  await expect.poll(async () => (await loop(page)).frames).toBeGreaterThan(frames);
  await page.getByLabel("邮箱").fill(knownEmail);
  await page.getByRole("button", { name: "继续", exact: true }).click();
  await page.getByLabel("密码", { exact: true }).fill(knownPassword);
  await page.getByRole("button", { name: "登录", exact: true }).click();
  await expect(page.getByRole("heading", { level: 1, name: "概览" })).toBeVisible();
  expect(await loop(page)).toEqual({ frames: 0, owners: 0, listening: false, pending: false });
});

// Every animation plays whatever the system's motion setting (docs/frontend.md「动画」): reduced motion asked for changes
// nothing on the sign-in pages, so the sea and the stage draw changing pictures and an arrival's title decodes.
for (const reducedMotion of ["no-preference", "reduce"] as const)
  test(`an arrival's sign-in page moves and decodes its title (${reducedMotion})`, async ({ page }) => {
    await page.emulateMedia({ reducedMotion });
    const decoded = await watchDecode(page);
    await mockConsole(page);
    await page.clock.install();
    await page.goto(apps[0].url);
    await page.evaluate(() => sessionStorage.setItem("axiom:arrival", String(Date.now())));
    await page.goto(signInPage);
    await expect(stageCanvas(page)).toBeAttached();
    await running(page);
    await expect.poll(decoded).toEqual({ hidden: true, drawn: true });
    await expect(page.locator(".lg-title .ch[style*=transparent]")).toHaveCount(0);
    // Morph calls the sea's sharks in before drawing the solid; wait until it has drawn opaque tiles before freezing.
    await expect
      .poll(
        () =>
          stageCanvas(page).evaluate((el) => {
            const c = el as HTMLCanvasElement;
            if (!c.width || !c.height) return false;
            const { data } = (c.getContext("2d") as CanvasRenderingContext2D).getImageData(0, 0, c.width, c.height);
            return data.some((v, i) => i % 4 === 3 && v >= 160);
          }),
        formed,
      )
      .toBe(true);
    await expectMovingNow(page, page.locator(".lg-sea > canvas.sea:not(.cover)"), stageCanvas(page));
  });

test("a sign-in page whose effects fail to load still signs in, with no unhandled error", async ({ page }) => {
  const unhandled: string[] = [];
  page.on("pageerror", (error) => unhandled.push(error.message));
  let refused = 0;
  await page.route(loginCode, (route) => {
    refused += 1;
    return route.fulfill({ status: 404 });
  });
  await mockConsole(page);
  await page.goto(signInPage);
  await page.getByLabel("邮箱").fill(knownEmail);
  await page.getByRole("button", { name: "继续", exact: true }).click();
  await page.getByLabel("密码", { exact: true }).fill(knownPassword);
  await page.getByRole("button", { name: "登录", exact: true }).click();
  await expect(page.getByRole("heading", { level: 1, name: "概览" })).toBeVisible();
  expect(refused).toBe(1);
  expect(unhandled).toEqual([]);
});

// How many of the solid's pixels are in the ring colour, which only its accent part is drawn in once lit: none while the
// address is empty, some once a whole one has been heard (the envelope's seal and letter). The solid turns with the
// time, so the lit part is what a test can count on; it is drawn at 0.72 to full opacity. colour names the ring's light
// or dark value; without it, the page's own.
async function ringPixels(page: Page, colour?: string) {
  return stageCanvas(page).evaluate((element, colour) => {
    const canvas = element as HTMLCanvasElement;
    const probe = document.createElement("i");
    probe.style.color = colour ?? "var(--ring)";
    (canvas.closest(".lg") as HTMLElement).append(probe);
    const ring = (getComputedStyle(probe).color.match(/\d+/g) ?? []).map(Number);
    probe.remove();
    const { data } = (canvas.getContext("2d") as CanvasRenderingContext2D).getImageData(
      0,
      0,
      canvas.width,
      canvas.height,
    );
    let count = 0;
    for (let i = 0; i < data.length; i += 4) {
      if ((data[i + 3] ?? 0) >= 180 && [0, 1, 2].every((c) => Math.abs((data[i + c] ?? 0) - (ring[c] ?? 0)) <= 2))
        count++;
    }
    return count;
  }, colour);
}

// The sign-in pages stand in one frame: going from signing in to registering changes the question only, and the sea and
// the solid on it are the ones already drawn, not made again.
test("going from signing in to registering keeps the sea and its solid", async ({ page }) => {
  await mockConsole(page);
  await page.goto(signInPage);
  await expect(stageCanvas(page)).toBeAttached();
  await page.locator(".lg canvas.sea").evaluate((sea) => {
    (window as unknown as { sea: Element }).sea = sea;
  });
  const stage = await stageCanvas(page).elementHandle();
  await page.locator(".lg-form").getByRole("link", { name: "注册", exact: true }).click();
  await expect(page).toHaveURL(/\/console\/register$/);
  await expect(page.locator(".lg-title")).toContainText("注册");
  expect(
    await page.locator(".lg canvas.sea").evaluate((sea) => sea === (window as unknown as { sea: Element }).sea),
  ).toBe(true);
  expect(await stage?.evaluate((canvas) => canvas.isConnected)).toBe(true);
  // one frame, one question
  await expect(page.locator(".lg")).toHaveCount(1);
  await expect(page.locator(".lg-form form")).toHaveCount(1);
});

test("what is typed reaches the solid: a whole address lights the envelope", async ({ page }) => {
  await mockConsole(page);
  await page.goto(signInPage);
  await expect(stageCanvas(page)).toBeAttached();
  expect(await ringPixels(page)).toBe(0);
  await page.getByLabel("邮箱").fill(knownEmail);
  await expect.poll(() => ringPixels(page), formed).toBeGreaterThan(0);
});

test("what was typed before the solid's code came is what the solid starts from", async ({ page }) => {
  await mockConsole(page);
  // the solid's code is held until the address is typed
  let release!: () => void;
  const held = new Promise<void>((resolve) => (release = resolve));
  await page.route(loginCode, async (route) => {
    await held;
    await route.fallback();
  });
  await page.goto(signInPage);
  await page.getByLabel("邮箱").fill(knownEmail);
  release();
  await expect(stageCanvas(page)).toBeAttached();
  await expect.poll(() => ringPixels(page), formed).toBeGreaterThan(0);
});

test("the solid takes the new scheme's colours when it changes", async ({ page }) => {
  await page.emulateMedia({ colorScheme: "light" });
  await mockConsole(page);
  await page.goto(signInPage);
  await expect(stageCanvas(page)).toBeAttached();
  await page.getByLabel("邮箱").fill(knownEmail);
  // the accent's light and dark values (tokens.css --accentT)
  const light = "#10738c";
  const dark = "#4ccce6";
  await expect.poll(() => ringPixels(page, light), formed).toBeGreaterThan(0);
  await page.emulateMedia({ colorScheme: "dark" });
  await expect.poll(() => ringPixels(page, dark)).toBeGreaterThan(0);
  expect(await ringPixels(page, light)).toBe(0);
});

test("the solid takes the site's brand colour when the site's settings come after it was drawn", async ({ page }) => {
  const state = await mockConsole(page);
  state.site = { ...state.site, brand_color: "#ff0000" };
  let release!: () => void;
  const held = new Promise<void>((resolve) => (release = resolve));
  await page.route("**/api/console/v1/site", async (route) => {
    await held;
    await route.fallback();
  });
  await page.goto(signInPage);
  await expect(stageCanvas(page)).toBeAttached();
  await page.getByLabel("邮箱").fill(knownEmail);
  await expect.poll(() => ringPixels(page, "#10738c"), formed).toBeGreaterThan(0);
  release();
  // drawn again in the console's accent as the brand makes it
  await expect.poll(() => ringPixels(page, "var(--accentT)")).toBeGreaterThan(0);
  expect(await ringPixels(page, "#10738c")).toBe(0);
});

test("an arrival's decode ends with the title it plays over: a new step's title is not drawn over", async ({
  page,
}) => {
  await mockConsole(page);
  await page.goto(apps[0].url);
  await page.evaluate(() => sessionStorage.setItem("axiom:arrival", String(Date.now())));
  // the engine's code is held once asked for, so the clock can be stopped before the decode's first frame
  let release!: () => void;
  const held = new Promise<void>((resolve) => (release = resolve));
  let asked!: () => void;
  const requested = new Promise<void>((resolve) => (asked = resolve));
  await page.route(loginCode, async (route) => {
    asked();
    await held;
    await route.fallback();
  });
  await page.clock.install();
  await page.goto(signInPage);
  await requested;
  // stopped before the title's veil (1.5 s) lifts, so the engine still finds it waiting for its decode
  await pauseAhead(page, 50);
  release();
  await expect(stageCanvas(page)).toBeAttached();
  const decodeCanvas = page.locator("canvas.lg-dc");
  const drawn = () =>
    decodeCanvas.evaluate((element) => {
      const canvas = element as HTMLCanvasElement;
      const { data } = (canvas.getContext("2d") as CanvasRenderingContext2D).getImageData(
        0,
        0,
        canvas.width,
        canvas.height,
      );
      return data.some((value, i) => i % 4 === 3 && value > 0);
    });
  await page.clock.runFor(100);
  await expect.poll(drawn).toBe(true);
  await page.getByLabel("邮箱").fill(knownEmail);
  await page.getByRole("button", { name: "继续", exact: true }).click();
  await expect(page.getByRole("heading", { level: 1, name: "输入密码" })).toBeVisible();
  expect(await drawn()).toBe(false);
});

test("an arrival whose step changes before the effects' code comes shows its title, and no decode plays", async ({
  page,
}) => {
  const decoded = await watchDecode(page);
  await mockConsole(page);
  await page.goto(apps[0].url);
  await page.evaluate(() => sessionStorage.setItem("axiom:arrival", String(Date.now())));
  let release!: () => void;
  const held = new Promise<void>((resolve) => (release = resolve));
  await page.route(loginCode, async (route) => {
    await held;
    await route.fallback();
  });
  await page.goto(signInPage);
  await expect(page.locator(".lg-title .ch[style*=transparent]").first()).toBeAttached();
  await page.getByLabel("邮箱").fill(knownEmail);
  await page.getByRole("button", { name: "继续", exact: true }).click();
  await expect(page.locator(".lg-title .ch[style*=transparent]")).toHaveCount(0);
  release();
  await expect(stageCanvas(page)).toBeAttached();
  await pause(page);
  expect((await decoded()).drawn).toBe(false);
});
