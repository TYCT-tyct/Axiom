import { expect, type Page, test } from "@playwright/test";
import { apps } from "./apps.ts";
import { collectErrors } from "./collect-errors.ts";
import { mockConsole } from "./console-api.ts";
import { knownEmail, knownPassword } from "./mock-auth.ts";
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

  // far below the first screen, past every screen, nothing is in view to play
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
  await expect(page.locator(".hs > main").getByRole("link", { name: "Sign up and get an API key" })).toBeVisible();

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

// The screens below the first (§8.2's Models, Steps and Trust rows): each one's code comes as its place nears the view,
// and its script runs on the same loop, so the counter speaks for them too once the first screen is out of view. The
// places (data-place) are there before the code, so the cases scroll to them. The usual mock data lists gpt-5 first.
const screenChunk = /\/assets\/(models|steps|trust)-screen-[\w-]+\.js$/;
const place = (page: Page, sec: number) => page.locator(`.under [data-place="${sec}"]`);
const scrollTo = (page: Page, sec: number) => place(page, sec).evaluate((el) => el.scrollIntoView({ block: "start" }));
// a screen has faded in once it is seen (the board's .hfm.in): a screen at its faint start would pass a visibility check
const shown = (page: Page, sec: number) =>
  expect
    .poll(() =>
      place(page, sec)
        .locator(".hfm")
        .evaluate((el) => getComputedStyle(el).opacity),
    )
    .toBe("1");

test("each screen below the first loads as it nears the view, and plays only while it is seen", async ({ page }) => {
  const asked: string[] = [];
  page.on("request", (request) => {
    const name = screenChunk.exec(request.url())?.[1];
    if (name) asked.push(name);
  });
  await page.setViewportSize({ width: 1440, height: 900 });
  await open(page);
  // the models screen lies within a view's height of the first, and the trust screen well below that does not; the
  // steps screen starts exactly a view's height below the first screen's end, at the edge of that margin
  await expect.poll(() => asked).toContain("models");
  expect(asked).not.toContain("trust");
  await expect(page.locator(".hfm2 .mr .mn").first()).toHaveText("gpt-5");

  await scrollTo(page, 3);
  // the models and steps chunks are both asked for at the top, in either order: which ones came is what counts
  await expect.poll(() => [...asked].sort()).toEqual(["models", "steps", "trust"]);
  await shown(page, 3);
  await running(page);
  await setHidden(page, true);
  const hidden = (await loop(page)).frames;
  await pause(page);
  expect(await loop(page)).toMatchObject({ frames: hidden, pending: false });
  await setHidden(page, false);
  await expect.poll(async () => (await loop(page)).frames).toBeGreaterThan(hidden);

  // the island names the section being read
  await expect(page.locator(".isl")).toContainText("三步开始");

  // past every screen nothing is in view to play
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
});

test("under reduced motion the screens below show their end at once and do not turn", async ({ page }) => {
  await page.emulateMedia({ reducedMotion: "reduce" });
  await page.setViewportSize({ width: 1440, height: 900 });
  await open(page);
  for (const sec of [2, 3, 4]) {
    await scrollTo(page, sec);
    await shown(page, sec);
  }
  // the three steps are done: the first request has come back
  await expect(page.locator(".hfm3 .c200.ok")).toHaveText("200");
  await expect(page.locator(".hfm2 .mr.on .mn")).toHaveText("gpt-5");
  // no script owns anything on the loop, so the list cannot move on to the next model
  await pause(page);
  expect(await loop(page)).toMatchObject({ frames: 0, owners: 0 });
});

test("a model and a step are chosen from the keyboard", async ({ page }) => {
  await page.emulateMedia({ reducedMotion: "reduce" });
  await page.setViewportSize({ width: 1440, height: 900 });
  await open(page);
  await scrollTo(page, 2);
  const second = page.locator(".hfm2 .mr").nth(1);
  await second.focus();
  await page.keyboard.press("Enter");
  await expect(second).toHaveClass(/\bon\b/);
  await expect(page.locator(".hfm2 .mr .mn").nth(1)).not.toHaveText("");
  await expect(page.locator(".hfm2 .mr[tabindex='0']")).toHaveCount(await page.locator(".hfm2 .mr").count());
  await expect(page.locator(".hfm3 .st[role='button']")).toHaveCount(3);
});

test("the models screen shows the call's failure, not the board's figures, when the models cannot be had", async ({
  page,
}) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await mockConsole(page);
  // after the usual mocks, so this answer is the one the page gets
  await page.route(/\/api\/console\/v1\/models(\?|$)/, (route) => route.fulfill({ status: 500, body: "{}" }));
  await page.goto(apps[0].url);
  await pageHeading(page);
  await scrollTo(page, 2);
  await expect(page.locator(".hfm2").getByRole("alert")).toBeVisible();
  await expect(page.locator(".hfm2 .mr")).toHaveCount(0);
  await expect(page.locator(".hfm2 .pick")).toHaveCount(0);
});

test("a screen whose code fails to arrive says so, and retrying reloads the page and loads it", async ({ page }) => {
  // the network is down for the chunk until the failure shows: the page asks for it twice, its module preload and the
  // import itself, so failing only the first request would fail the preload and let the import through
  let down = true;
  await page.route(/\/assets\/steps-screen-[\w-]+\.js$/, (route) =>
    down ? route.abort("internetdisconnected") : route.continue(),
  );
  await page.setViewportSize({ width: 1440, height: 900 });
  await open(page);
  await scrollTo(page, 3);
  const retry = place(page, 3).getByRole("button");
  await expect(retry).toBeVisible();
  down = false;
  await Promise.all([page.waitForEvent("load"), retry.click()]);
  await pageHeading(page);
  await scrollTo(page, 3);
  await shown(page, 3);
});

test("without the site's API address the steps say so rather than show an empty one saved", async ({ page }) => {
  await page.emulateMedia({ reducedMotion: "reduce" });
  await page.setViewportSize({ width: 1440, height: 900 });
  await mockConsole(page, "usual", {
    site: { name: "Axiom", currency: "USD", key_prefix: "sk-ax-v1-", registration: "invite" },
  });
  await page.goto(apps[0].url);
  await pageHeading(page);
  await scrollTo(page, 3);
  await shown(page, 3);
  await expect(page.locator(".hfm3")).toContainText("本站 API 地址暂不可用");
  await expect(page.locator(".hfm3")).not.toContainText("已保存");
});

// The departure to the console's sign-in pages (port design §9): the pressed button breaks into tiles that stream to
// where the sign-in panel will be, and half a second after the press the browser goes to the button's page, whatever
// became of the picture on the way.
const depart = /\/assets\/depart-[\w-]+\.js$/;
const signIn = (page: Page) => page.getByRole("banner").getByRole("link", { name: "登录", exact: true });
// the first screen's way on: the steps screen below has one of the same name
const signUp = (page: Page) => page.locator(".hs > main").getByRole("link", { name: "注册，获取API key" });
const hang = (page: Page, url: RegExp) => page.route(url, () => new Promise<void>(() => {}));

// Policy violations of every document the tab shows, the website's and the one it goes to, kept in the test.
async function watchEveryPolicy(page: Page) {
  const seen: string[] = [];
  await page.exposeFunction("reportPolicy", (line: string) => seen.push(line));
  await page.addInitScript(() =>
    document.addEventListener("securitypolicyviolation", (e) =>
      (window as unknown as { reportPolicy: (line: string) => void }).reportPolicy(
        `${location.pathname} ${e.violatedDirective} ${e.blockedURI}`,
      ),
    ),
  );
  return seen;
}

// The sign-in documents the tab asks for.
function watchDeparture(page: Page) {
  const asked: string[] = [];
  page.on("request", (request) => {
    if (request.isNavigationRequest() && /\/console\//.test(request.url())) asked.push(request.url());
  });
  return asked;
}

test("pressing sign in plays the departure, loaded on the press, and goes half a second later", async ({ page }) => {
  const violations = await watchEveryPolicy(page);
  const loaded: string[] = [];
  page.on("request", (request) => {
    if (depart.test(request.url())) loaded.push(request.url());
  });
  await open(page);
  await running(page);
  expect(loaded).toEqual([]);
  // time stands still from before the press: only runFor moves it, so the deadline is measured from the press alone
  await page.clock.install();
  await page.clock.pauseAt((await page.evaluate(() => Date.now())) + 100);
  const asked = watchDeparture(page);
  await signIn(page).click();
  await expect(page.locator(".tr > canvas[aria-hidden]:last-child")).toBeAttached();
  expect(loaded).toHaveLength(1);
  await page.clock.runFor(150);
  await expect(signIn(page)).toBeHidden();
  await page.clock.runFor(340);
  expect(asked).toEqual([]);
  await page.clock.runFor(20);
  await expect(page).toHaveURL(/\/console\/sign-in$/);
  expect(violations).toEqual([]);
});

test("under reduced motion the sign-up button goes at once, without the departure's code", async ({ page }) => {
  await page.emulateMedia({ reducedMotion: "reduce" });
  const loaded: string[] = [];
  page.on("request", (request) => {
    if (depart.test(request.url())) loaded.push(request.url());
  });
  await hang(page, depart);
  await open(page);
  await signUp(page).click();
  await expect(page).toHaveURL(/\/console\/register$/);
  expect(loaded).toEqual([]);
});

test("a press goes though the departure's code never comes", async ({ page }) => {
  const unhandled: string[] = [];
  page.on("pageerror", (error) => unhandled.push(error.message));
  await hang(page, depart);
  await open(page);
  await signUp(page).click();
  await expect(page).toHaveURL(/\/console\/register$/);
  expect(unhandled).toEqual([]);
});

test("after a departure's navigation is stopped, the next press goes at once", async ({ page }) => {
  await open(page);
  await running(page);
  const asked = watchDeparture(page);
  await hang(page, /\/console\/sign-in$/);
  await signIn(page).click();
  await expect.poll(() => asked.length).toBe(1);
  // the browser's stop button, and a press on another way in straight after it
  await (await page.context().newCDPSession(page)).send("Page.stopLoading");
  await signUp(page).click();
  await expect(page).toHaveURL(/\/console\/register$/);
  expect(asked).toHaveLength(2);
});

test("a departure whose navigation is stopped puts the page back", async ({ page }) => {
  await open(page);
  await running(page);
  const asked = watchDeparture(page);
  await hang(page, /\/console\/sign-in$/);
  await signIn(page).click();
  await expect.poll(() => asked.length).toBe(1);
  await (await page.context().newCDPSession(page)).send("Page.stopLoading");
  await expect(signIn(page)).toBeVisible();
  await expect(page.locator(".tr > canvas[aria-hidden]:last-child")).not.toBeAttached();
});

// The console's sign-in pages (port design §9): the sea and the stage on it, loaded after the first content, and the
// title's decode, which plays once for a visitor the website's departure brought (its one-shot marker) and never for a
// direct visit.
// the engine's own chunk, not the frame's (login-frame-*.js)
const loginCode = /\/assets\/login-[\w-]{8}\.js$/;
const signInPage = `${apps[0].url.replace(/\/$/, "")}/console/sign-in`;
const stageCanvas = (page: Page) => page.locator(".lg-sea > canvas:not(.sea)");

// Whether any of the title's characters was hidden for the decode, recorded from the document's first script.
async function watchDecode(page: Page) {
  await page.addInitScript(() => {
    const seen = { hidden: false };
    (window as unknown as { decode: typeof seen }).decode = seen;
    new MutationObserver((records) => {
      for (const record of records) {
        const target = record.target as HTMLElement;
        if (target.matches?.(".lg-title .ch") && target.style.color === "transparent") seen.hidden = true;
      }
    }).observe(document, { subtree: true, attributes: true, attributeFilter: ["style"] });
  });
  return () => page.evaluate(() => (window as unknown as { decode: { hidden: boolean } }).decode.hidden);
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
  expect(await decoded()).toBe(true);
  expect(asked).toEqual([true]);
  // the marker was taken: the same page again is a direct visit
  expect(await page.evaluate(() => sessionStorage.getItem("axiom:arrival"))).toBeNull();
  await page.reload();
  await pageHeading(page);
  await running(page);
  await pause(page);
  expect(await decoded()).toBe(false);
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

test("under reduced motion an arrival's sign-in page is still: the formed shape and the title as it is", async ({
  page,
}) => {
  await page.emulateMedia({ reducedMotion: "reduce" });
  const decoded = await watchDecode(page);
  await mockConsole(page);
  await page.goto(apps[0].url);
  await page.evaluate(() => sessionStorage.setItem("axiom:arrival", String(Date.now())));
  await page.goto(signInPage);
  await expect(stageCanvas(page)).toBeAttached();
  await pause(page);
  expect((await loop(page)).frames).toBe(0);
  expect(await decoded()).toBe(false);
});

test("a sign-in page whose effects fail to load still signs in, with no unhandled error", async ({ page }) => {
  const unhandled: string[] = [];
  page.on("pageerror", (error) => unhandled.push(error.message));
  await page.route(loginCode, (route) => route.fulfill({ status: 404 }));
  await mockConsole(page);
  await page.goto(signInPage);
  await page.getByLabel("邮箱").fill(knownEmail);
  await page.getByRole("button", { name: "继续", exact: true }).click();
  await page.getByLabel("密码", { exact: true }).fill(knownPassword);
  await page.getByRole("button", { name: "登录", exact: true }).click();
  await expect(page.getByRole("heading", { level: 1, name: "概览" })).toBeVisible();
  expect(unhandled).toEqual([]);
});
