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
// The sharks are called in and taken in before the solid is whole: up to 3.4 s for the far ones to give up, and a second
// to be taken in.
const formed = { timeout: 15_000 };

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

// stopClock waits until the solid is whole and settled in hold phase, then leaves the fake clock paused (installed
// before goto so performance.now() never jumps backwards behind Morph's cached timestamps).
async function stopClock(page: Page) {
  await expect(stageCanvas(page)).toBeAttached();
  await expect.poll(async () => (await solid(page)).count, formed).toBeGreaterThan(400);
  await pauseAhead(page, 100);
  await page.clock.fastForward(4500);
  await page.clock.fastForward(1000);
  await page.clock.runFor(1000);
}

// How much the solid's middle oscillates across 12 samples 35 ms apart, as the summed second differences: the solid's
// own turning drifts its middle smoothly, which they cancel, while a shake (420 ms, three cycles, a quarter cycle per
// sample) reverses direction at every other sample. Summed first differences counted the drift too: a train measured
// 60 px of quiet drift against 52.5 px with the shake (train 44822730292).
async function sway(page: Page) {
  const middles: number[] = [];
  for (let i = 0; i < 12; i++) {
    await page.clock.runFor(35);
    middles.push((await solid(page)).cx);
  }
  return middles
    .slice(2)
    .reduce((total, x, i) => total + Math.abs(x - 2 * (middles[i + 1] ?? x) + (middles[i] ?? x)), 0);
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

test("a refused password shakes the solid", async ({ page }) => {
  await page.clock.install();
  await mockConsole(page);
  await page.goto(signInPage);
  await page.getByLabel("邮箱").fill(knownEmail);
  await page.getByRole("button", { name: "继续", exact: true }).click();
  await page.getByLabel("密码", { exact: true }).fill("not the password");
  await stopClock(page);
  const still = await sway(page);
  await page.getByRole("button", { name: "登录", exact: true }).click();
  await expect(page.getByRole("alert")).toBeVisible();
  const shaken = await sway(page);
  expect(shaken).toBeGreaterThan(still + 12);
});

test("the solid disperses back into sharks after 20 s idle and re-forms", async ({ page }) => {
  await page.clock.install();
  await mockConsole(page);
  await page.goto(signInPage);
  await stopClock(page);
  // 20 s idle triggers release (760 ms disperse into rest), then 5.2 s rest, then sharks gather again
  await page.clock.fastForward(21_000);
  await page.clock.fastForward(900);
  await page.clock.runFor(100);
  expect((await solid(page)).count).toBe(0);
  await page.clock.fastForward(5500);
  await page.clock.fastForward(6200);
  await page.clock.runFor(1200);
  expect((await solid(page)).count).toBeGreaterThan(400);
});

test("on a phone viewport the sea is a narrow band with the small tile solid in its corner", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await mockConsole(page);
  await page.goto(signInPage);
  const sea = page.locator(".lg-sea");
  await expect(sea).toBeVisible();
  expect((await sea.boundingBox())?.height).toBe(208);
  await expect(stageCanvas(page)).toBeAttached();
  await expect.poll(async () => (await solid(page)).count, formed).toBeGreaterThan(100);
  const box = await solid(page);
  const width = await stageCanvas(page).evaluate((el) => (el as HTMLCanvasElement).width);
  expect(box.cx).toBeGreaterThan(width / 2);
});
