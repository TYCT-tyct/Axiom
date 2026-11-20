import { expect, type Page, test } from "@playwright/test";
import { apps } from "./apps.ts";
import { mockConsole } from "./console-api.ts";
import { pauseAhead } from "./fx-helpers.ts";
import { knownEmail, mfaEmail } from "./mock-auth.ts";
import { signIn } from "./sign-in.ts";

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

// stopClock stops the page's clock once the solid is whole: from then on only runFor moves it, and with it the frames.
async function stopClock(page: Page) {
  await expect(stageCanvas(page)).toBeAttached();
  await expect.poll(async () => (await solid(page)).count, formed).toBeGreaterThan(400);
  await page.clock.install();
  await pauseAhead(page, 100);
  await page.clock.runFor(500);
}

// How far the solid's middle moves across 12 samples 35 ms apart: a shake (420 ms) moves it a few pixels each way.
async function sway(page: Page) {
  const middles: number[] = [];
  for (let i = 0; i < 12; i++) {
    await page.clock.runFor(35);
    middles.push((await solid(page)).cx);
  }
  return Math.max(...middles) - Math.min(...middles);
}

test("the sea's sharks form the step's solid, which keeps turning", async ({ page }) => {
  await mockConsole(page);
  await page.goto(signInPage);
  await stopClock(page);
  const before = await pictureHash(page);
  await page.clock.runFor(1000);
  expect(await pictureHash(page)).not.toBe(before);
});

test("the solid becomes the next step's: the padlock of a code melts into the key of a passkey", async ({ page }) => {
  const state = await mockConsole(page);
  state.site = { ...state.site, passkeys: true };
  // the passkey's request stays out, so its step stays
  await page.route("**/api/console/v1/auth/passkeys/mfa/begin", () => {});
  await page.goto(signInPage);
  await signIn(page, mfaEmail);
  await expect(page.getByRole("heading", { level: 1, name: "两步验证" })).toBeVisible();
  await stopClock(page);
  const lock = await solid(page);
  expect(lock.h).toBeGreaterThan(lock.w);
  await page.getByRole("button", { name: "使用通行密钥" }).click();
  await page.clock.runFor(1500);
  const key = await solid(page);
  expect(key.w).toBeGreaterThan(key.h * 1.5);
});

test("a refused password shakes the solid", async ({ page }) => {
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
  expect(shaken).toBeGreaterThan(still + 4);
});
