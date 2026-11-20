import { expect, type Page, test } from "@playwright/test";
import { apps } from "./apps.ts";
import { mockConsole } from "./console-api.ts";
import { expectMoving, loop, open, pause, pauseAhead, running, setHidden, siteSignIn as signIn } from "./fx-helpers.ts";
import { knownEmail, knownPassword } from "./mock-auth.ts";
import { pageHeading } from "./page-heading.ts";

// The console's sign-in pages (port design §9): the sea and the stage on it, loaded after the first content, and the
// title's decode, which plays once for a visitor the website's departure brought (its one-shot marker) and never for a
// direct visit.
// the engine's own chunk, not the frame's (login-frame-*.js)
const loginCode = /\/assets\/login-[\w-]{8}\.js$/;
const signInPage = `${apps[0].url.replace(/\/$/, "")}/console/sign-in`;
const stageCanvas = (page: Page) => page.locator(".lg-sea > canvas:not(.sea)");

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

test("arriving through the departure decodes the title once, after the first content; a direct visit does not", async ({
  page,
}) => {
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
  expect(asked).toEqual([true]);
  // the marker was taken: the same page again is a direct visit
  expect(await page.evaluate(() => sessionStorage.getItem("axiom:arrival"))).toBeNull();
  await page.reload();
  await pageHeading(page);
  await running(page);
  await pause(page);
  expect(await decoded()).toEqual({ hidden: false, drawn: false });
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
    await page.goto(apps[0].url);
    await page.evaluate(() => sessionStorage.setItem("axiom:arrival", String(Date.now())));
    await page.goto(signInPage);
    await expect(stageCanvas(page)).toBeAttached();
    await running(page);
    await expect.poll(decoded).toEqual({ hidden: true, drawn: true });
    await expect(page.locator(".lg-title .ch[style*=transparent]")).toHaveCount(0);
    await expectMoving(page, page.locator(".lg-sea > canvas.sea:not(.cover)"), stageCanvas(page));
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

// How many of the stage's pixels are in the ring colour, which only its lit tiles are drawn in: none while the address is
// empty, some once a whole one has been heard. The frames change with the time (the glyphs flicker, the loose tiles
// drift), so the lit tiles are what a test can count on. colour names the ring's light or dark value; without it, the
// page's own.
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
      if (data[i + 3] === 255 && [0, 1, 2].every((c) => Math.abs((data[i + c] ?? 0) - (ring[c] ?? 0)) <= 2)) count++;
    }
    return count;
  }, colour);
}

test("what is typed reaches the stage: a whole address lights the envelope", async ({ page }) => {
  await mockConsole(page);
  await page.goto(signInPage);
  await expect(stageCanvas(page)).toBeAttached();
  expect(await ringPixels(page)).toBe(0);
  await page.getByLabel("邮箱").fill(knownEmail);
  await expect.poll(() => ringPixels(page)).toBeGreaterThan(0);
});

test("what was typed before the stage's code came is what the stage starts from", async ({ page }) => {
  await mockConsole(page);
  // the stage's code is held until the address is typed
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
  await expect.poll(() => ringPixels(page)).toBeGreaterThan(0);
});

test("the stage's tiles take the new scheme's colours when it changes", async ({ page }) => {
  await page.emulateMedia({ colorScheme: "light" });
  await mockConsole(page);
  await page.goto(signInPage);
  await expect(stageCanvas(page)).toBeAttached();
  await page.getByLabel("邮箱").fill(knownEmail);
  // the accent's light and dark values (tokens.css --accentT)
  const light = "#10738c";
  const dark = "#4ccce6";
  await expect.poll(() => ringPixels(page, light)).toBeGreaterThan(0);
  await page.emulateMedia({ colorScheme: "dark" });
  await expect.poll(() => ringPixels(page, dark)).toBeGreaterThan(0);
  expect(await ringPixels(page, light)).toBe(0);
});

test("the stage takes the site's brand colour when the site's settings come after it was drawn", async ({ page }) => {
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
  await expect.poll(() => ringPixels(page, "#10738c")).toBeGreaterThan(0);
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
