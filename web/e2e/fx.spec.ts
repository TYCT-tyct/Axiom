import { expect, type Page, test } from "@playwright/test";
import { apps } from "./apps.ts";
import { collectErrors } from "./collect-errors.ts";
import { mockConsole } from "./console-api.ts";
import { pageHeading } from "./page-heading.ts";

// The website's first-screen effects on the shared frame loop (port design §8.2, docs/frontend.md「动效运行时」): the sea
// and its entrance, and the language's decode and the theme's dive. The loop exposes its state on window.axiomFx (frames
// run since it last started, owners, whether it holds listeners); the cases read it rather than time anything. That
// the loop forgets everything when its last owner leaves (a component unmounted) is packages/fx's own test: the
// website has no client route to leave the first screen by.

const sea = /\/assets\/sea-[\w-]+\.js$/;
const prefs = /\/assets\/prefs-[\w-]+\.js$/;

type LoopState = { frames: number; owners: number; listening: boolean; pending: boolean };
const loop = (page: Page) =>
  page.evaluate(() => (window as unknown as { axiomFx: { state: LoopState } }).axiomFx.state);

// The page's policy violations, as the page reports them.
async function watchPolicy(page: Page) {
  await page.addInitScript(() => {
    const seen: string[] = [];
    (window as unknown as { policy: string[] }).policy = seen;
    document.addEventListener("securitypolicyviolation", (e) => seen.push(`${e.violatedDirective} ${e.blockedURI}`));
  });
  return () => page.evaluate(() => (window as unknown as { policy: string[] }).policy);
}

// setHidden makes the tab hidden or visible again, as switching tabs does.
const setHidden = (page: Page, hidden: boolean) =>
  page.evaluate((value) => {
    Object.defineProperty(document, "hidden", { configurable: true, get: () => value });
    Object.defineProperty(document, "visibilityState", {
      configurable: true,
      get: () => (value ? "hidden" : "visible"),
    });
    document.dispatchEvent(new Event("visibilitychange"));
  }, hidden);

const pause = (page: Page) => page.evaluate(() => new Promise((done) => setTimeout(done, 300)));

async function open(page: Page) {
  await mockConsole(page);
  await page.goto(apps[0].url);
  await pageHeading(page);
}

const running = (page: Page) => expect.poll(async () => (await loop(page)).frames).toBeGreaterThan(0);
const entrance = (page: Page) => page.locator(".tr").getAttribute("data-enter");

test("the effects' code is not asked for before the first screen is drawn, and the entrance runs on the loop", async ({
  page,
}) => {
  const asked: { painted: boolean; heading: boolean }[] = [];
  await page.route(sea, async (route) => {
    asked.push(
      await page.evaluate(() => ({
        painted: performance.getEntriesByName("first-contentful-paint").length > 0,
        heading: (document.querySelector("h1")?.getBoundingClientRect().height ?? 0) > 0,
      })),
    );
    await route.continue();
  });
  const prefsAsked: string[] = [];
  page.on("request", (request) => {
    if (prefs.test(request.url())) prefsAsked.push(request.url());
  });
  await open(page);
  await running(page);
  expect(asked).toEqual([{ painted: true, heading: true }]);
  // the tide rises once and is over, while the sea goes on
  await expect.poll(() => entrance(page)).toBe("done");
  const after = (await loop(page)).frames;
  await expect.poll(async () => (await loop(page)).frames).toBeGreaterThan(after);
  // the switches' effects load only when one is used
  expect(prefsAsked).toEqual([]);
});

test("the sea rests while the tab is hidden or the first screen is out of view, and goes on after", async ({
  page,
}) => {
  await open(page);
  await running(page);

  await setHidden(page, true);
  const hidden = (await loop(page)).frames;
  await pause(page);
  expect(await loop(page)).toMatchObject({ frames: hidden, pending: false });
  await setHidden(page, false);
  await expect.poll(async () => (await loop(page)).frames).toBeGreaterThan(hidden);

  // the screens below come with S2; a block as tall stands in for them
  await page.evaluate(() => {
    const scroller = document.querySelector(".hs") as HTMLElement;
    const below = document.createElement("div");
    below.style.height = "3000px";
    scroller.append(below);
    scroller.scrollTop = scroller.scrollHeight;
  });
  await expect.poll(async () => (await loop(page)).pending).toBe(false);
  const away = (await loop(page)).frames;
  await pause(page);
  expect((await loop(page)).frames).toBe(away);
  await page.evaluate(() => {
    (document.querySelector(".hs") as HTMLElement).scrollTop = 0;
  });
  await expect.poll(async () => (await loop(page)).frames).toBeGreaterThan(away);
});

test("under reduced motion nothing runs: the sea is one still frame and the entrance is at its end", async ({
  page,
}) => {
  await page.emulateMedia({ reducedMotion: "reduce" });
  const asked = page.waitForRequest(sea);
  await open(page);
  await asked;
  await expect.poll(() => entrance(page)).toBe("done");
  const canvas = page.locator("canvas.sea").first();
  const drawn = () => canvas.evaluate((c: HTMLCanvasElement) => c.toDataURL());
  await expect.poll(async () => (await drawn()).length).toBeGreaterThan(10_000);
  const first = await drawn();
  await page.evaluate(() => new Promise((done) => setTimeout(done, 500)));
  expect(await drawn()).toBe(first);
  expect(await loop(page)).toEqual({ frames: 0, owners: 0, listening: false, pending: false });
  expect(await page.evaluate(() => document.getAnimations().length)).toBe(0);
  // the other theme switches at once, and the still sea is drawn again in it
  await page.getByRole("button", { name: /切换到/ }).click();
  await expect.poll(drawn).not.toBe(first);
  expect(await loop(page)).toMatchObject({ frames: 0, owners: 0 });
});

test("a sea that fails to load leaves the page working, with no unhandled error", async ({ page }) => {
  const errors = collectErrors(page);
  await page.route(sea, (route) => route.fulfill({ status: 404, body: "" }));
  const failed = page.waitForResponse(sea);
  await open(page);
  await failed;
  await pause(page);
  // the browser reports the missing file itself; nothing else may go wrong
  expect(errors.filter((error) => !error.includes("404"))).toEqual([]);
  await page.getByRole("banner").getByRole("button", { name: "简体中文" }).click();
  await page.getByRole("option", { name: /English/ }).click();
  await expect(page.getByRole("heading", { level: 1 })).toHaveText("One key for every model");
  expect(errors.filter((error) => !error.includes("404"))).toEqual([]);
});

test("the language decodes in and the theme dives, each kept on reload, with no policy violation", async ({ page }) => {
  const violations = await watchPolicy(page);
  await page.emulateMedia({ colorScheme: "light" });
  await open(page);
  await running(page);
  const decode = page.locator("canvas.scramble[data-on]");

  await page.getByRole("banner").getByRole("button", { name: "简体中文" }).click();
  await page.getByRole("combobox").fill("eng");
  await page.getByRole("combobox").press("Enter");
  await expect(decode).toHaveCount(1);
  await expect(page.locator("html")).toHaveAttribute("lang", "en-US");
  await expect(page.getByRole("heading", { level: 1 })).toHaveText("One key for every model");
  await expect(decode).toHaveCount(0);
  await expect(page.getByRole("link", { name: "Sign up and get an API key" })).toBeVisible();

  await page.getByRole("button", { name: "Switch to dark" }).click();
  await expect(page.locator("html")).toHaveAttribute("data-theme", "dark");
  await expect(page.locator(".tr")).toHaveClass(/\bdark\b/);
  await expect(page.locator("[data-t]")).toHaveCount(0);

  await page.reload();
  await pageHeading(page);
  await expect(page.locator("html")).toHaveAttribute("data-theme", "dark");
  await expect(page.getByRole("heading", { level: 1 })).toHaveText("One key for every model");
  // back to the system's scheme: the page follows it again, and says so
  await page.getByRole("button", { name: "Switch to light" }).click();
  const said = page.getByRole("status").filter({ hasText: "Following system" });
  await expect.poll(() => said.evaluate((el) => getComputedStyle(el).opacity)).toBe("1");
  await expect(page.locator("html")).not.toHaveAttribute("data-theme");
  expect(await violations()).toEqual([]);
});
