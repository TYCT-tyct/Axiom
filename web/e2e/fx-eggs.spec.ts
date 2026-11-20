import { expect, type Page, test } from "@playwright/test";
import { collectErrors } from "./collect-errors.ts";
import { open, running } from "./fx-helpers.ts";

// The sea's easter eggs (docs/frontend.md「海的彩蛋」, @axiom/fx/eggs and site/src/home/easter-eggs.tsx): the sea-floor terminal
// to the keyboard, its commands on the site's own data and the sea, and the director's budget of one auto moment a
// visit on a clock the case moves. The director's other rules and the terminal's lines are unit tests
// (packages/fx/src/director.test.ts, site/src/home/terminal.test.ts, site/src/home/easter-eggs.test.tsx).

type State = { ran: string[]; earned: string[]; queued: string[] };
const eggs = (page: Page) =>
  page.evaluate(() => (window as unknown as { axiomEggs?: { state: State } }).axiomEggs?.state ?? null);

const whale = /\/assets\/whale-[\w-]+\.js$/;
const policy = (page: Page) => {
  const seen: string[] = [];
  page.on("console", (message) => {
    if (/Content Security Policy/i.test(message.text())) seen.push(message.text());
  });
  return seen;
};

// the clock of the cases: a weekday morning in a known zone, neither night nor 24 October
test.use({ timezoneId: "Asia/Shanghai" });

test("/ opens the sea-floor terminal, which answers help, ls, whale, feed and exit", async ({ page }) => {
  const errors = collectErrors(page);
  const violations = policy(page);
  const asked: string[] = [];
  page.on("request", (request) => {
    if (whale.test(request.url())) asked.push(request.url());
  });
  await page.setViewportSize({ width: 1440, height: 900 });
  await open(page);
  await running(page);
  await expect.poll(() => eggs(page)).not.toBeNull();
  const terminal = page.getByRole("dialog", { name: "海底终端" });
  await expect(terminal).toBeHidden();

  await page.keyboard.press("/");
  await expect(terminal).toBeVisible();
  const input = terminal.getByRole("textbox", { name: "输入命令" });
  await expect(input).toBeFocused();
  const run = async (line: string) => {
    await input.fill(line);
    await input.press("Enter");
  };
  await run("help");
  await expect(terminal).toContainText("whale        叫鲸来");
  // the site's published models (the mock's), not the design's list
  await run("ls");
  await expect(terminal).toContainText(/openai\/gpt-5\s+2 条线路/);
  await expect(terminal).toContainText("deepseek/deepseek-v3");
  expect(asked).toEqual([]);
  await run("whale");
  await expect(terminal).toContainText("鲸来了。");
  await expect.poll(() => asked).toHaveLength(1);
  await run("feed");
  await expect(terminal).toContainText("鱼来了。");
  await expect(terminal.locator('[aria-live="polite"] > *')).toHaveCount(10);

  await run("exit");
  await expect(terminal).toBeHidden();
  expect(errors).toEqual([]);
  expect(violations).toEqual([]);
});

test("the hourly tide waits out the first ten seconds and comes once a visit", async ({ page }) => {
  await page.clock.install({ time: new Date("2026-11-20T09:59:00+08:00") });
  await open(page);
  await running(page);
  // the tide is due, but the visit is only seconds old
  await expect.poll(() => eggs(page)).toEqual({ ran: [], earned: [], queued: ["tide"] });
  await page.clock.fastForward(10_000);
  await expect.poll(() => eggs(page)).toEqual({ ran: ["tide"], earned: [], queued: [] });
  // an hour on, the tide is due again; the visit has had its auto moment
  await page.clock.fastForward("01:00:00");
  await page.clock.runFor(2000);
  expect(await page.evaluate(() => new Date().getMinutes())).toBe(59);
  expect(await eggs(page)).toEqual({ ran: ["tide"], earned: [], queued: [] });
});
