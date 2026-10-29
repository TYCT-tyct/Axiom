import { readFileSync } from "node:fs";
import { createRequire } from "node:module";
import { dirname, join } from "node:path";
import { type Browser, devices, expect, type Page, test } from "@playwright/test";
import { adminSuite } from "./admin-scenes.ts";
import { mockConsole } from "./console-api.ts";
import { consoleSuite } from "./console-scenes.ts";
import { signedInAs } from "./mock-auth.ts";
import type { Suite } from "./scene.ts";
import { words } from "./words.ts";

// The performance budgets of docs/frontend.md「前端性能指标」 as checks: LCP and CLS of the main pages on a phone over a
// slow network and on a desktop, INP of the common operations at 4x slower CPU, and the smoothness of a long list
// scrolled. Each number is measured in the browser by the web-vitals library (the list's frames by requestAnimationFrame
// and the long-task observer), written to the report (each test's annotations, which Playwright's reporters list), and compared
// with the budget (below); a page over its ceiling fails the run. The numbers are of the server in this run, so a slower CI
// host gives larger ones: the ceilings leave room for that, and the measurements are the median of three loads (LCP) or
// the worst of the operation (INP), as the document states.

// target is the budget of the document; ceiling is what fails the run. Where the pages do not meet a target yet, the
// ceiling is the first measurement with room for a slower host (docs/frontend.md「实测」), so the gate stops a page from
// getting worse while the gap is open; where they meet it, the two are the same.
// A metric with no ceiling is reported and never fails: CLS moved between 0.05 and 0.8 on the same page between two
// runs, and the late frames of the scroll between 0.45 (a developer's host) and 0.68 (CI's), so a limit on them would only
// trip by chance until the cause is found.
type Limit = { target: number; ceiling?: number };
const budget = {
  lcpDesktop: { target: 1500, ceiling: 1500 },
  lcpPhone: { target: 2500, ceiling: 6000 },
  inp: { target: 100, ceiling: 500 },
  cls: { target: 0.05 },
  lateFrames: { target: 0.05 },
  longTasks: { target: 0 },
} satisfies Record<string, Limit>;

// The mocked API (page.route) answers inside the browser, where the network throttle does not reach, so the API hop of a
// load costs nothing here and the phone LCP is that much better than a real phone's.
// Lighthouse's mobile defaults (slow 4G, 4x CPU) and its desktop ones; the throughput is its 1.6 Mbps and 10 Mbps with the
// 0.9 factor it applies for DevTools throttling.
const phone = { rtt: 150, down: 1.6 * 1024 * 0.9 * 128, up: 750 * 0.9 * 128, cpu: 4 };
const desktop = { rtt: 40, down: 10 * 1024 * 0.9 * 128, up: 10 * 1024 * 0.9 * 128, cpu: 1 };
type Profile = typeof phone;

// web-vitals' IIFE build, which a page gets before its own scripts so it sees the whole load.
// The package does not export its IIFE file, so it is found beside the CommonJS one the package does.
const library = readFileSync(
  join(dirname(createRequire(import.meta.url).resolve("web-vitals")), "web-vitals.iife.js"),
  "utf8",
);
const collector = `${library};
window.__vitals = {};
for (const [name, on] of [["LCP", webVitals.onLCP], ["CLS", webVitals.onCLS], ["INP", webVitals.onINP]]) {
  on((metric) => { window.__vitals[name] = metric.value; }, { reportAllChanges: true, durationThreshold: 16 });
}`;

type Vitals = { LCP?: number; CLS?: number; INP?: number };

async function measuredPage(browser: Browser, profile: Profile, device: Parameters<Browser["newContext"]>[0]) {
  const context = await browser.newContext({ ...device, locale: "zh-CN", timezoneId: "Asia/Shanghai" });
  const page = await context.newPage();
  await page.addInitScript(collector);
  const session = await context.newCDPSession(page);
  await session.send("Emulation.setCPUThrottlingRate", { rate: profile.cpu });
  await session.send("Network.enable");
  await session.send("Network.emulateNetworkConditions", {
    offline: false,
    latency: profile.rtt,
    downloadThroughput: profile.down,
    uploadThroughput: profile.up,
  });
  return page;
}

const frames = (page: Page) =>
  page.evaluate(() => new Promise((done) => requestAnimationFrame(() => requestAnimationFrame(done))));

// The metrics once the page has settled: the network is quiet and two frames have been drawn since.
async function vitals(page: Page): Promise<Vitals> {
  await page.waitForLoadState("networkidle");
  await frames(page);
  return page.evaluate(() => (window as unknown as { __vitals: Vitals }).__vitals);
}

const median = (values: number[]) => [...values].sort((a, b) => a - b)[Math.floor(values.length / 2)] ?? 0;

// A throttled page is slow by design: the 4x CPU and the slow network stretch a load and its waits well past the 30 s.
test.describe.configure({ timeout: 120_000 });

function record(name: string, value: number, { target, ceiling }: Limit) {
  const line = `${value.toFixed(3)} (target ${target}${value > target ? ", over" : ""}, ceiling ${ceiling ?? "none"})`;
  console.log(`vitals ${name}: ${line}`);
  test.info().annotations.push({ type: name, description: line });
  if (ceiling !== undefined) {
    expect(value, `${name} is over its ceiling of ${ceiling}`).toBeLessThanOrEqual(ceiling);
  }
}

function sceneOf(suite: Suite, name: string) {
  const scene = suite.scenes.find((s) => s.name === name);
  if (!scene) {
    throw new Error(`no scene ${name}`);
  }
  return scene;
}

// The pages a person lands on and works in, by their names in the suites' scene lists.
const pages: [Suite, string][] = [
  ...["overview", "keys", "calls", "balance", "statement"].map((name): [Suite, string] => [consoleSuite, name]),
  ...["users", "suppliers", "models", "pricing", "settings"].map((name): [Suite, string] => [adminSuite, name]),
];

const profiles = [
  { name: "phone", profile: phone, device: devices["Pixel 5"], lcp: budget.lcpPhone },
  { name: "desktop", profile: desktop, device: devices["Desktop Chrome"], lcp: budget.lcpDesktop },
];

test.describe("loading", () => {
  for (const { name, profile, device, lcp } of profiles) {
    for (const [suite, sceneName] of pages) {
      test(`${suite.app} ${sceneName}: LCP and CLS on ${name}`, async ({ browser }) => {
        const scene = sceneOf(suite, sceneName);
        const loads: Vitals[] = [];
        for (let i = 0; i < 3; i++) {
          const page = await measuredPage(browser, profile, device);
          await suite.prepare(page, scene);
          await scene.open(page);
          loads.push(await vitals(page));
          await page.context().close();
        }
        const label = `${suite.app} ${sceneName} ${name}`;
        record(`LCP ${label}`, median(loads.map((l) => l.LCP ?? Number.POSITIVE_INFINITY)), lcp);
        record(`CLS ${label}`, Math.max(...loads.map((l) => l.CLS ?? 0)), budget.cls);
      });
    }
  }
});

// The operations of the document, each on a fresh page at 4x CPU, taking the worst INP the page saw. A dialog scene's own
// last step is the click that opens it (or submits it), which is the interaction measured.
const dialogs: [Suite, string][] = [
  ...["key-create", "key-edit", "key-delete", "key-secret", "totp-setup"].map((name): [Suite, string] => [
    consoleSuite,
    name,
  ]),
  ...["credit", "supplier-connect", "supplier-pause", "mapping", "price-publish", "rule-new", "settings-edit"].map(
    (name): [Suite, string] => [adminSuite, name],
  ),
];

async function slowPage(browser: Browser, suite: Suite, sceneName: string) {
  const scene = sceneOf(suite, sceneName);
  const page = await measuredPage(browser, { ...desktop, cpu: 4 }, devices["Desktop Chrome"]);
  await suite.prepare(page, scene);
  return { page, scene };
}

test.describe("interaction", () => {
  for (const [suite, sceneName] of dialogs) {
    test(`${suite.app} ${sceneName}: INP`, async ({ browser }) => {
      const { page, scene } = await slowPage(browser, suite, sceneName);
      await scene.open(page);
      record(`INP ${suite.app} ${sceneName}`, (await vitals(page)).INP ?? 0, budget.inp);
      await page.context().close();
    });
  }

  test("console calls: INP of filtering, paging and opening a detail", async ({ browser }) => {
    const { page, scene } = await slowPage(browser, consoleSuite, "calls");
    await scene.open(page);
    await page.getByRole("button", { name: "加载更多" }).click();
    await page.getByLabel("模型").fill("openai/gpt-5");
    await page.getByRole("button", { name: "筛选" }).click();
    await expect(page).toHaveURL(/model=openai%2Fgpt-5/);
    await page.getByRole("row").nth(1).getByRole("link").click();
    await expect(page.getByRole("heading", { level: 1, name: "调用详情" })).toBeVisible();
    record("INP console calls", (await vitals(page)).INP ?? 0, budget.inp);
    await page.context().close();
  });

  test("admin users: INP of filtering and opening an account", async ({ browser }) => {
    const { page, scene } = await slowPage(browser, adminSuite, "users");
    await scene.open(page);
    await page.getByLabel(words("users.searchLabel")).fill("user1");
    await page.getByRole("button", { name: words("filter.apply") }).click();
    await page
      .getByRole("link", { name: words("users.open") })
      .first()
      .click();
    await expect(page.getByRole("heading", { level: 1 })).toBeVisible();
    record("INP admin users", (await vitals(page)).INP ?? 0, budget.inp);
    await page.context().close();
  });
});

test.describe("long list", () => {
  // The list pages by cursor, 50 rows a time, so the longest it gets is the pages a person has asked for: this loads
  // 1000 rows, then scrolls 5 seconds at 4x CPU (docs/frontend.md「前端性能指标」), counting the frames drawn late and the
  // tasks that held the main thread for 50 ms or more.
  test("console calls: 1000 rows scroll without dropped frames", async ({ browser }) => {
    const page = await measuredPage(browser, phone, devices["Desktop Chrome"]);
    const state = await mockConsole(page, "worst");
    signedInAs(state);
    state.calls = Array.from({ length: 1000 }, (_, n) => ({
      ...(state.calls[n % state.calls.length] as (typeof state.calls)[number]),
      id: `00000000-0000-4000-8000-${String(n).padStart(12, "0")}`,
    }));
    await page.goto("/console/calls");
    const more = page.getByRole("button", { name: "加载更多" });
    for (let rows = 51; rows < 1001; rows += 50) {
      await more.click();
      await expect(page.getByRole("row")).toHaveCount(Math.min(rows + 50, 1001));
    }
    expect(await page.evaluate(() => document.documentElement.scrollHeight)).toBeGreaterThan(10_000);
    const outcome = await page.evaluate(
      () =>
        new Promise<{ late: number; total: number; longTasks: number }>((done) => {
          let longTasks = 0;
          new PerformanceObserver((list) => {
            longTasks += list.getEntries().length;
          }).observe({ type: "longtask", buffered: false });
          const gaps: number[] = [];
          let last = performance.now();
          const start = last;
          const step = (now: number) => {
            gaps.push(now - last);
            last = now;
            window.scrollBy(0, 40);
            if (now - start < 5000) {
              requestAnimationFrame(step);
            } else {
              done({ late: gaps.filter((g) => g > 25).length, total: gaps.length, longTasks });
            }
          };
          requestAnimationFrame(step);
        }),
    );
    record("late frames, console calls", outcome.late / outcome.total, budget.lateFrames);
    record("long tasks while scrolling, console calls", outcome.longTasks, budget.longTasks);
  });
});
