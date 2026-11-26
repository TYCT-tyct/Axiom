import { expect, type Page, test } from "@playwright/test";
import { apps } from "./apps.ts";
import { mockConsole } from "./console-api.ts";
import { pauseAhead } from "./fx-helpers.ts";
import { knownEmail, knownPassword, mfaEmail } from "./mock-auth.ts";

// The solid on the console's sign-in pages' sea (login-ui.js's Morph, as the final boards' sign-in page runs it): the
// sea's sharks are called in and become a turning solid for the step, which melts into the next step's and shakes when
// an answer is refused. What is typed lighting it is fx-sign-in.spec.ts's.
const signInPage = `${apps[0].url.replace(/\/$/, "")}/console/sign-in`;
const stageCanvas = (page: Page) => page.locator(".lg-sea > canvas.lg-stg");
// The solid's opaque pixels: how many, and the box they fill, in the canvas's pixels.
const solid = (page: Page) =>
  stageCanvas(page).evaluate((element) => {
    const canvas = element as HTMLCanvasElement;
    const { data } = (canvas.getContext("2d") as CanvasRenderingContext2D).getImageData(
      0,
      0,
      canvas.width,
      canvas.height,
    );
    let [count, x0, y0, x1, y1] = [0, Number.POSITIVE_INFINITY, Number.POSITIVE_INFINITY, -1, -1];
    for (let i = 3; i < data.length; i += 4) {
      if ((data[i] ?? 0) < 160) continue;
      const p = (i - 3) / 4;
      const x = p % canvas.width;
      const y = Math.floor(p / canvas.width);
      count++;
      [x0, y0, x1, y1] = [Math.min(x0, x), Math.min(y0, y), Math.max(x1, x), Math.max(y1, y)];
    }
    return { count, w: x1 - x0, h: y1 - y0, cx: (x0 + x1) / 2 };
  });

const pictureHash = (page: Page) =>
  stageCanvas(page).evaluate((element) => {
    const canvas = element as HTMLCanvasElement;
    const { data } = (canvas.getContext("2d") as CanvasRenderingContext2D).getImageData(
      0,
      0,
      canvas.width,
      canvas.height,
    );
    let hash = 2166136261;
    for (const value of data) hash = Math.imul(hash ^ value, 16777619);
    return hash >>> 0;
  });

// stopClock stops the page's clock once the solid is on the sea and runs it until the solid is whole and settled in its
// hold phase, where it leaves the clock paused (installed before goto, so performance.now() never jumps backwards
// behind Morph's cached timestamps). The sharks are called in on the first frames; those not in reach give up 3.4 s
// after (6 s once one has been taken in) and are taken in, in 950 ms. The solid steps on its own clock, but only on a
// frame the sea draws, and a slow sea draws every other frame (ocean.ts pacedTick): a jump of the clock is one frame,
// which may be skipped, so a run of frames follows each jump. Nothing waits on the machine's own time.
async function stopClock(page: Page) {
  await expect(stageCanvas(page)).toBeAttached();
  await pauseAhead(page, 100);
  await page.clock.runFor(100);
  await page.clock.fastForward(6000);
  await page.clock.runFor(2000);
}

test("the sea's sharks form the step's solid, which keeps turning, and the old stage does not draw", async ({
  page,
}) => {
  await page.addInitScript(() => {
    (window as unknown as { stageGlyphs: number }).stageGlyphs = 0;
    const draw = CanvasRenderingContext2D.prototype.fillText;
    CanvasRenderingContext2D.prototype.fillText = function (this: CanvasRenderingContext2D, ...args) {
      if (this.canvas.classList.contains("lg-stg")) (window as unknown as { stageGlyphs: number }).stageGlyphs++;
      return draw.apply(this, args);
    };
  });
  await page.clock.install();
  await mockConsole(page);
  await page.goto(signInPage);
  await stopClock(page);
  expect((await solid(page)).count).toBeGreaterThan(400);
  await expect(stageCanvas(page)).toHaveCount(1);
  // Morph draws shaded tiles with fillRect only; the old Stage drew cycling glyphs with fillText.
  expect(await page.evaluate(() => (window as unknown as { stageGlyphs: number }).stageGlyphs)).toBe(0);
  const before = await pictureHash(page);
  await page.clock.runFor(1000);
  expect(await pictureHash(page)).not.toBe(before);
});

test("the solid melts across steps: envelope to key on sign-in, padlock to key on MFA", async ({ page }) => {
  await page.clock.install();
  const state = await mockConsole(page);
  state.site = { ...state.site, passkeys: true };
  // the passkey's request stays out, so its step stays
  await page.route("**/api/console/v1/auth/passkeys/mfa/begin", () => {});
  await page.goto(signInPage);
  await stopClock(page);
  const envelope = await solid(page);
  await page.getByLabel("邮箱").fill(mfaEmail);
  await page.getByRole("button", { name: "继续", exact: true }).click();
  await page.clock.fastForward(1200);
  await page.clock.runFor(100);
  const passwordKey = await solid(page);
  expect(passwordKey.w / passwordKey.h).toBeGreaterThan((envelope.w / envelope.h) * 1.2);
  await page.clock.resume();
  await page.getByLabel("密码", { exact: true }).fill(knownPassword);
  await page.getByRole("button", { name: "登录", exact: true }).click();
  await expect(page.getByRole("heading", { level: 1, name: "两步验证" })).toBeVisible();
  await stopClock(page);
  const lock = await solid(page);
  expect(lock.h).toBeGreaterThan(lock.w);
  await page.getByRole("button", { name: "使用通行密钥" }).click();
  await page.clock.fastForward(1200);
  await page.clock.runFor(100);
  const key = await solid(page);
  expect(key.w).toBeGreaterThan(key.h * 1.5);
});

// What the solid (login-ui.js's Morph, the design's engine as it is) is told and what it does, read at the engine's own
// doors as its code defines them: the refusals it hears, and the phases its frames pass through (off, gather, hold, back
// into the sharks, rest). The class is wrapped as the page defines it, so neither the engine's code nor the page's
// changes.
type Morph = { phase: string; hear(heard: { type?: string }): void; tick(...args: unknown[]): void };
type Seen = { refusals: number; phases: string[] };
function watchSolid() {
  const seen: Seen = { refusals: 0, phases: ["off"] };
  (window as unknown as { solidSeen: Seen }).solidSeen = seen;
  let engine: { prototype: Morph } | undefined;
  Object.defineProperty(window, "AxiomLoginMorph", {
    configurable: true,
    get: () => engine,
    set(value: { prototype: Morph }) {
      const { hear, tick } = value.prototype;
      value.prototype.hear = function (this: Morph, heard) {
        if (heard?.type === "bad") seen.refusals++;
        return hear.call(this, heard);
      };
      value.prototype.tick = function (this: Morph, ...args) {
        tick.apply(this, args);
        if (this.phase !== seen.phases.at(-1)) seen.phases.push(this.phase);
      };
      engine = value;
    },
  });
}
const solidSeen = (page: Page) => page.evaluate(() => (window as unknown as { solidSeen: Seen }).solidSeen);

// A refused answer shakes the solid, as the engine's code does on hearing it. The page's part is to tell the solid of
// the refusal, once: read from the canvas instead, the shake is a few pixels on a 7.5 px grid under a turning solid,
// which no threshold tells apart from the turning on a busy machine (#908).
test("a refused password is told to the solid once", async ({ page }) => {
  await page.addInitScript(watchSolid);
  await mockConsole(page);
  await page.goto(signInPage);
  // the solid is on the sea: a refusal before its code is in has nothing to shake, and is not kept for it
  await expect(stageCanvas(page)).toBeAttached();
  await page.getByLabel("邮箱").fill(knownEmail);
  await page.getByRole("button", { name: "继续", exact: true }).click();
  await page.getByLabel("密码", { exact: true }).fill("not the password");
  expect((await solidSeen(page)).refusals).toBe(0);
  await page.getByRole("button", { name: "登录", exact: true }).click();
  await expect(page.getByRole("alert")).toBeVisible();
  expect((await solidSeen(page)).refusals).toBe(1);
});

// Each jump of the clock is followed by a run of frames (stopClock says why), and the case reads the phases the solid
// went through rather than a picture at a moment a frame early or late (#911).
test("the solid disperses back into sharks after 20 s idle and re-forms", async ({ page }) => {
  await page.addInitScript(watchSolid);
  await page.clock.install();
  await mockConsole(page);
  await page.goto(signInPage);
  await stopClock(page);
  expect((await solidSeen(page)).phases).toEqual(["off", "gather", "hold"]);
  // 20 s left alone, it comes apart into the sharks (760 ms) and leaves them to swim
  await page.clock.fastForward(20_000);
  await page.clock.runFor(1000);
  expect((await solidSeen(page)).phases).toEqual(["off", "gather", "hold", "back", "rest"]);
  expect((await solid(page)).count).toBe(0);
  // after 5.2 s at rest they are called in again; those not in reach within 6 s give up, and are taken in (950 ms)
  await page.clock.fastForward(5200);
  await page.clock.runFor(100);
  await page.clock.fastForward(6000);
  await page.clock.runFor(1200);
  expect((await solidSeen(page)).phases).toEqual(["off", "gather", "hold", "back", "rest", "gather", "hold"]);
  expect((await solid(page)).count).toBeGreaterThan(400);
});

test("on a phone viewport the sea is a narrow band with the small tile solid in its corner", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.clock.install();
  await mockConsole(page);
  await page.goto(signInPage);
  const sea = page.locator(".lg-sea");
  await expect(sea).toBeVisible();
  expect((await sea.boundingBox())?.height).toBe(208);
  await stopClock(page);
  const box = await solid(page);
  expect(box.count).toBeGreaterThan(100);
  const width = await stageCanvas(page).evaluate((el) => (el as HTMLCanvasElement).width);
  expect(box.cx).toBeGreaterThan(width / 2);
});
