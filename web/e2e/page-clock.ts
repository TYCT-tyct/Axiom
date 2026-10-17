import { expect, type Page } from "@playwright/test";

// advanceUntil moves the page's clock (page.clock) until the check holds. What makes these tests flaky on a loaded
// runner is the page's own timers: the pauses between a query's retries, the batching of its updates. Run in simulated
// time, they cost no real time, so how many requests the server saw, and when the page shows their answer, do not
// depend on how fast the machine is. Call it after page.clock.install(), and give the check a short timeout so that a
// miss moves the clock on instead of waiting out a full one.
export async function advanceUntil(page: Page, check: () => void | Promise<void>) {
  await expect(async () => {
    await page.clock.runFor(5_000);
    await check();
  }).toPass();
}
