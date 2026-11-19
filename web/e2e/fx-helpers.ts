import { expect, type Page } from "@playwright/test";
import { apps } from "./apps.ts";
import { mockConsole } from "./console-api.ts";
import { pageHeading } from "./page-heading.ts";

// What the effect cases share (fx.spec.ts, fx-screens.spec.ts, fx-sign-in.spec.ts): the loop's counter, a hidden tab,
// the page opened as the first screen is drawn, its header's way to the sign-in pages, and the screens' places.

type LoopState = { frames: number; owners: number; listening: boolean; pending: boolean };
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
// The screens below the first (§8.2's rows for the screens): each one's code comes as its place nears the view, and its
// script runs on the same loop, so the counter speaks for them too once the first screen is out of view. The places
// (data-place) are there before the code, so the cases scroll to them. The usual mock data lists gpt-5 first.
export const screenChunk = /\/assets\/([a-z]+)-screen-[\w-]+\.js$/;
// The screens below the first that the page delivers, in order (Site.template.html's sections 1 to 6), written down here
// rather than read from the page, so a screen that goes missing fails the cases that walk them.
export const delivered = [1, 2, 3, 4, 5, 6];
export const place = (page: Page, sec: number) => page.locator(`.under [data-place="${sec}"]`);
export const sections = (page: Page) =>
  page.locator(".under [data-place]").evaluateAll((els) => els.map((el) => Number((el as HTMLElement).dataset.place)));
export const scrollTo = (page: Page, sec: number) =>
  place(page, sec).evaluate((el) => el.scrollIntoView({ block: "start" }));
// a screen has faded in once it is seen (the board's .hfm.in): a screen at its faint start would pass a visibility check
export const shown = (page: Page, sec: number) =>
  expect
    .poll(() =>
      place(page, sec)
        .locator(".hfm")
        .evaluate((el) => getComputedStyle(el).opacity),
    )
    .toBe("1");
