import { expect, type Page } from "@playwright/test";
import { apps } from "./apps.ts";
import { mockConsole } from "./console-api.ts";
import { pageHeading } from "./page-heading.ts";

// What the frame-loop tests share (fx.spec.ts, fx-sign-in.spec.ts): the loop's state as the page exposes it on
// window.axiomFx, a hidden tab, a pause, the website opened, and its header's way to the sign-in pages.

export type LoopState = { frames: number; owners: number; listening: boolean; pending: boolean };
export const loop = (page: Page) =>
  page.evaluate(() => (window as unknown as { axiomFx: { state: LoopState } }).axiomFx.state);

// setHidden makes the tab hidden or visible again, as switching tabs does.
export const setHidden = (page: Page, hidden: boolean) =>
  page.evaluate((value) => {
    Object.defineProperty(document, "hidden", { configurable: true, get: () => value });
    Object.defineProperty(document, "visibilityState", {
      configurable: true,
      get: () => (value ? "hidden" : "visible"),
    });
    document.dispatchEvent(new Event("visibilitychange"));
  }, hidden);

export const pause = (page: Page) => page.evaluate(() => new Promise((done) => setTimeout(done, 300)));

export async function open(page: Page) {
  await mockConsole(page);
  await page.goto(apps[0].url);
  await pageHeading(page);
}

export const running = (page: Page) => expect.poll(async () => (await loop(page)).frames).toBeGreaterThan(0);

export const siteSignIn = (page: Page) => page.getByRole("banner").getByRole("link", { name: "登录", exact: true });
