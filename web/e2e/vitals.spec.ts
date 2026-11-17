import { readFileSync } from "node:fs";
import { createRequire } from "node:module";
import { dirname, join } from "node:path";
import { type Browser, devices, expect, type Locator, type Page, test } from "@playwright/test";
import { bracketed, median, scaledLcp } from "../scripts/vitals.ts";
import { servedOffset } from "../vite.shared.ts";
import { mockAdmin } from "./admin-api.ts";
import { adminSuite } from "./admin-scenes.ts";
import { adminUrl } from "./apps.ts";
import { mockConsole } from "./console-api.ts";
import { consoleSuite } from "./console-scenes.ts";
import { signedInAs } from "./mock-auth.ts";
import { pageHeading } from "./page-heading.ts";
import type { Scene, Suite } from "./scene.ts";
import { siteSuite } from "./site-scenes.ts";
import { traced } from "./vitals-cpu.ts";
import { words } from "./words.ts";

// The performance budgets of docs/frontend.md「前端性能指标」 as checks: LCP and CLS of the main pages on a phone over a
// slow network and on a desktop, INP of the common operations at 4x slower CPU, and the smoothness of a long list
// scrolled. Each number is measured in the browser by the web-vitals library (the list's frames by requestAnimationFrame
// and the long-task observer), written to the report (each test's annotations, which Playwright's reporters list), and compared
// with the budget (below); a page over its ceiling fails the run. The numbers are timings of the host in this run, so
// each page load is scaled by the reference page measured beside it (below): the measurements are the median of three
// page loads, each between two reference loads (LCP), or the worst of the operation (INP), as the document states. The
// overview cannot be scaled that way (docs/frontend.md「实测」), so its gate is its main-thread CPU time, measured from a
// trace, against the base commit's in pairs of loads.

// target is the budget of the document; ceiling is what fails the run. Where the pages do not meet a target yet, the
// ceiling is the first measurement with room for a slower host (docs/frontend.md「实测」), so the gate stops a page from
// getting worse while the gap is open; where they meet it, the two are the same.
// A metric with no ceiling is reported and never fails: CLS moved between 0.05 and 0.8 on the same page between two
// runs, and the late frames of the scroll between 0.45 (a developer's host) and 0.68 (CI's); INP is a latency that follows the
// load of the shared host (it went over its ceiling on a busy train). A limit on them would only trip by chance.
// Baseline observations on the built pages: desktop LCP 0.50-0.83 s, phone 2.24-4.10 s; the phone regression
// ceiling leaves 1.5x room above that first CI maximum. Local INP ranged 96-920 ms (CI maximum 280 ms).
// With the scroll origin corrected, calls had 36-38% late frames and 47-49 long tasks locally (68% late on CI);
// users had 4.1-4.5% late frames and 10 long tasks. These are observations, not alternate budgets.
// The remaining gaps are not fully diagnosed: phone LCP includes throttled resource round trips, models/statement
// CLS follows content layout, dialog INP follows rendering, and the long-list tasks still need attribution.
type Limit = { target: number; ceiling?: number };
const budget = {
  lcpDesktop: { target: 1500, ceiling: 1500 },
  lcpPhone: { target: 2500, ceiling: 6000 },
  // The website's own phone target (docs/frontend.md「前端性能指标」); the ceiling is its first measurement, 2.05 s
  // scaled, with the same 1.5x room for a slower host as the console's.
  lcpSitePhone: { target: 2000, ceiling: 3100 },
  inp: { target: 100 },
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

// Every number here is a timing of this host, and the vitals job shares its host with the other jobs of its train and of
// every pull request building beside it. Each page load except the overview is therefore measured between two loads of
// the console overview, the lightest page with the same shell and a table of mocked data, each in a fresh context with
// the same throttle. The page's LCP is scaled to the mean of the two reference loads around it on the baseline run the
// ceilings came from, and the median of the three scaled loads drops the one a burst landed in (docs/frontend.md
// 「实测」). A burst stretches a whole load, not one side of it, and contention can only delay a load, never hurry one.
//
// The overview cannot be scaled that way: sign-in's LCP moved 1.24x while the overview's moved 3.5x on run 41983413805,
// because the overview runs the shell's JavaScript, API calls and a table and sign-in much less; CPU contention stretches
// them differently (docs/frontend.md「实测」). Its gate is therefore the CPU time of its own load, from a trace: the sum
// of the main-thread tasks' `tdur`, which excludes the time the thread was descheduled. Those loads run without the CPU
// throttle in both viewports: the throttle suspends the main thread by busy-waiting in a signal handler for (rate - 1)
// times the wall time since it last let the thread run, up to 1 ms of it (Chromium's thread_cpu_throttler.cc), so
// under it `tdur` is the page's work plus a spin sized by how the host schedules the thread. Unthrottled, the same work
// still costs 1.4-1.9x the CPU time on a busy CI host, which no fixed ceiling can tell from a slower page. So the gate
// is relative, as bench-compare is for Go: where BASE names the commit this tree is measured against
// (playwright.vitals.config.ts), that commit's overview is loaded in turns with this one, unthrottled, in the same job,
// and the median of the pairs' ratios is gated: the two loads of a pair, back to back, see the same host. On main the
// same tree already passed its train, so the time is only reported there and a busy host cannot turn main red. The
// absolute backstop stops a slow creep that each PR's ratio lets through: it is three times the clean build's time,
// above the 1.9x a busy host added on CI (docs/frontend.md「实测」).
const overviewMs = { phone: 3496, desktop: 948 } as Record<"phone" | "desktop", number>;
// Recalibrated 2026-11-17 on b7e5225f's production build, served by Go, Linux/8 cores, Chromium 156.0.8078.4:
// node web/scripts/vitals.ts <origin> <output-directory>, after the builds/checks ended; nine fresh contexts per
// viewport, the gate's mock, network profiles and toplevel tdur sum, cpu=1 in BOTH viewports. Desktop median
// 470.467 ms (409.926-782.806), phone 576.562 ms (539.418-681.867). Round each median UP to milliseconds, then
// multiply by three: 471*3 / 577*3. The separate detailed trace and 4x-CPU phone samples do not set this backstop.
// The old 340/400 ms calibration predates the board shell; even #826's parent now measures 512/674 ms on this host.
// This only resets the cumulative backstop; keep the paired regression gate below unchanged (docs/frontend.md「实测」).
const overviewCpu = {
  desktop: { target: 1413, ceiling: 1413 },
  phone: { target: 1731, ceiling: 1731 },
} satisfies Record<"phone" | "desktop", Limit>;
const overviewRatio: Limit = { target: 1.25, ceiling: 1.25 };
// Nine pairs: on 62 CI runs one pair's ratio scattered 0.62-1.65 around 1, independently of its
// neighbours, so the median of nine fails an unchanged tree about once in 8600 runs on the phone, where the ratio of
// two five-load medians failed about once in 60 (docs/frontend.md「实测」).
// Calibration evidence (phone / desktop, one million bootstrap samples of 310 pairs per profile):
// five independent medians had false-positive rates 1/61 / 1/290; five paired ratios 1/348 / 1/2353;
// nine paired ratios 1/8621 / 1/142857, detecting a 1.4x slowdown in 99.3% / 96.3% of samples.
// Adjacent demeaned pairs had lag-one correlations -0.21 / -0.25 (independent five-sample expectation -0.25).
// A 400 ms busy wait in the overview module produced ratios 1.326 / 1.458, and in the shared shell 1.533 / 1.733;
// an unchanged tree measured 0.968 / 0.988 despite one pair in each exceeding 1.3. These support the paired median,
// not retries or wider ceilings. A trimmed mean was slightly better but did not justify another statistic.
// Hardware instruction counts were steadier (desktop 841-862 million while tdur varied 293-563 ms), but collecting
// another process's counters needs CAP_PERFMON and CAP_SYS_PTRACE. A shared host running PR code must not grant those
// capabilities just for this gate. A synthetic 1500-row reference was no better than raw CPU time: its uninterrupted
// work does not pay the same cold-cache cost as the overview's many short tasks. The base page is the matched control.
const overviewPairs = 9;
// The base's overview, beside this tree's: its origin, where playwright.vitals.config.ts serves it, or none.
const baseOrigin = process.env.AXIOM_VITALS_BASE_PORT
  ? `http://localhost:${Number(process.env.AXIOM_VITALS_BASE_PORT) + servedOffset}`
  : undefined;

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

// load is one scene in a fresh context with the profile's throttle.
async function load(
  browser: Browser,
  profile: Profile,
  device: Parameters<Browser["newContext"]>[0],
  suite: Suite,
  scene: Scene,
): Promise<Vitals> {
  const page = await measuredPage(browser, profile, device);
  await suite.prepare(page, scene);
  await scene.open(page);
  const result = await vitals(page);
  await page.context().close();
  return result;
}

// busyLoad is the main-thread CPU time of one scene in a fresh context with the profile's throttle, traced from before
// the navigation to after the page settled. The context is closed even when the load fails, so a trace cannot outlive its
// test.
async function busyLoad(
  browser: Browser,
  profile: Profile,
  device: Parameters<Browser["newContext"]>[0],
  suite: Suite,
  scene: Scene,
): Promise<number> {
  const page = await measuredPage(browser, profile, device);
  try {
    await suite.prepare(page, scene);
    const session = await page.context().newCDPSession(page);
    const { busyMs } = await traced(session, async () => {
      await scene.open(page);
      await vitals(page);
    });
    return busyMs;
  } finally {
    await page.context().close();
  }
}

// A throttled page is slow by design: the 4x CPU and the slow network stretch a load and its waits well past the 30 s.
test.describe.configure({ timeout: 120_000 });

function record(name: string, value: number, { target, ceiling }: Limit, note = "") {
  const line = `${value.toFixed(3)}${note ? ` (${note})` : ""} (target ${target}${value > target ? ", over" : ""}, ceiling ${ceiling ?? "none"})`;
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

// The console overview is the reference for the pages with its shell: the lightest page with that shell and a mocked
// table, so a slow host or a busy neighbour stretches both, and scaling keeps the ceiling a page budget rather than a
// host budget. The overview's own gate is its main-thread CPU time against the base's (measureOverview), not its LCP.
const overview = sceneOf(consoleSuite, "overview");

// The pages a person lands on and works in, by their names in the suites' scene lists. The overview is not here: it is
// the reference for all of these and has its own test.
const pages: [Suite, string][] = [
  ...["sign-in", "keys", "calls", "balance", "statement"].map((name): [Suite, string] => [consoleSuite, name]),
  ...["users", "suppliers", "models", "pricing", "settings"].map((name): [Suite, string] => [adminSuite, name]),
  [siteSuite, "home"],
];

const profiles: {
  name: "phone" | "desktop";
  profile: Profile;
  device: Parameters<Browser["newContext"]>[0];
  lcp: Limit;
}[] = [
  { name: "phone", profile: phone, device: devices["Pixel 5"], lcp: budget.lcpPhone },
  { name: "desktop", profile: desktop, device: devices["Desktop Chrome"], lcp: budget.lcpDesktop },
];

// A page's reference: the page read beside it three times and its LCP on the run the ceiling came from.
type Reference = { suite: Suite; scene: Scene; lcp: number };

// measurePage loads the page three times, each between two loads of its reference, and gates the median of the scaled
// LCPs. The reference is read beside the page, so a host that is slow for one is slow for both, and the mean of the two
// brackets follows a load that rises through the sample instead of charging the page the slower side every time.
async function measurePage(
  browser: Browser,
  profile: Profile,
  device: Parameters<Browser["newContext"]>[0],
  suite: Suite,
  scene: Scene,
  reference: Reference,
  name: "phone" | "desktop",
  lcp: Limit,
) {
  const label = `${suite.app} ${scene.name} ${name}`;
  const pairs: { base: number; page: number; cls: number }[] = [];
  for (let i = 0; i < 3; i++) {
    const before = await load(browser, profile, device, reference.suite, reference.scene);
    const page = await load(browser, profile, device, suite, scene);
    const after = await load(browser, profile, device, reference.suite, reference.scene);
    pairs.push({
      ...bracketed({
        before: before.LCP ?? Number.POSITIVE_INFINITY,
        after: after.LCP ?? Number.POSITIVE_INFINITY,
        page: page.LCP ?? Number.POSITIVE_INFINITY,
      }),
      cls: page.CLS ?? 0,
    });
  }
  record(
    `LCP ${label}`,
    scaledLcp(pairs, reference.lcp),
    lcp,
    `raw ${median(pairs.map((pair) => pair.page)).toFixed(0)} ms, ${reference.scene.name} ${median(pairs.map((pair) => pair.base)).toFixed(0)} of ${reference.lcp} ms`,
  );
  record(`CLS ${label}`, Math.max(...pairs.map((pair) => pair.cls)), budget.cls);
}

// measureOverview loads the overview unthrottled in nine pairs, each this tree's and, where there is one, the base's, back
// to back in alternating order so the side loaded first is not always the same. It gates the median of the pairs'
// ratios, so a slow load on either side moves one pair and the median drops it, and this tree's median against the
// backstop; without a base it only reports. It then loads the overview three times with the profile's throttle and
// reports the LCP and CLS: the LCP is what the page budget speaks of, but it is only reported, because the host
// stretches it in ways the CPU time does not (docs/frontend.md「实测」).
async function measureOverview(
  browser: Browser,
  profile: Profile,
  device: Parameters<Browser["newContext"]>[0],
  name: "phone" | "desktop",
  lcp: Limit,
) {
  const label = `console overview ${name}`;
  const unthrottled = { ...profile, cpu: 1 };
  const head: number[] = [];
  const base: number[] = [];
  const sides = [
    async () => head.push(await busyLoad(browser, unthrottled, device, consoleSuite, overview)),
    ...(baseOrigin
      ? [
          async () =>
            base.push(await busyLoad(browser, unthrottled, { ...device, baseURL: baseOrigin }, consoleSuite, overview)),
        ]
      : []),
  ];
  for (let i = 0; i < overviewPairs; i++) {
    for (const side of i % 2 ? [...sides].reverse() : sides) {
      await side();
    }
  }
  const cpu = overviewCpu[name];
  const loads = (values: number[]) => values.map((ms) => ms.toFixed(0)).join("/");
  if (base.length) {
    const ratios = base.map((ms, i) => (head[i] ?? Number.POSITIVE_INFINITY) / ms);
    record(`main-thread time ${label}`, median(head), cpu, `unthrottled, backstop, loads ${loads(head)} ms`);
    record(
      `main-thread time ratio ${label}`,
      median(ratios),
      overviewRatio,
      `median of pairs ${ratios.map((ratio) => ratio.toFixed(2)).join("/")}; this tree ${median(head).toFixed(0)} ms, base ${median(base).toFixed(0)} ms (loads ${loads(base)} ms)`,
    );
  } else {
    record(
      `main-thread time ${label}`,
      median(head),
      { target: cpu.target },
      `unthrottled, reported only: no base to compare with, loads ${loads(head)} ms`,
    );
  }
  const throttled: Vitals[] = [];
  for (let i = 0; i < 3; i++) {
    throttled.push(await load(browser, profile, device, consoleSuite, overview));
  }
  record(
    `LCP ${label}`,
    median(throttled.map((load) => load.LCP ?? Number.POSITIVE_INFINITY)),
    { target: lcp.target },
    "reported only",
  );
  record(`CLS ${label}`, Math.max(...throttled.map((load) => load.CLS ?? 0)), budget.cls);
}

test.describe("loading", () => {
  for (const { name, profile, device, lcp } of profiles) {
    test(`console overview: main-thread time and CLS on ${name}`, async ({ browser }) => {
      // Nine pairs of unthrottled loads with the base and three throttled ones, against nine loads in a page's test.
      test.setTimeout(240_000);
      await measureOverview(browser, profile, device, name, lcp);
    });
    for (const [suite, sceneName] of pages) {
      test(`${suite.app} ${sceneName}: LCP and CLS on ${name}`, async ({ browser }) => {
        const scene = sceneOf(suite, sceneName);
        const reference = { suite: consoleSuite, scene: overview, lcp: overviewMs[name] };
        const limit = suite === siteSuite && name === "phone" ? budget.lcpSitePhone : lcp;
        await measurePage(browser, profile, device, suite, scene, reference, name, limit);
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
    const rows = page.getByRole("table", { name: "调用记录" }).getByRole("row");
    await page.getByRole("button", { name: "加载更多" }).click();
    await page.getByLabel("模型").fill("openai/gpt-5");
    await page.getByRole("button", { name: "筛选" }).click();
    await expect(page).toHaveURL(/model=openai%2Fgpt-5/);
    await rows.nth(1).getByRole("link").click();
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
  // tasks that held the main thread for 50 ms or more. The calls and the users are the two long lists the budget names.
  // rows scopes the count to the list, since the calls page also shows a summary table above it.
  async function loadRows(page: Page, rows: Locator, count: number) {
    const more = page.getByRole("button", { name: "加载更多" });
    for (let loaded = 50; loaded < count; loaded += 50) {
      await more.click();
      await expect(rows).toHaveCount(Math.min(loaded + 50, count) + 1);
    }
  }

  // Loading a page scrolls its button into view, so the list is near its end; a measurement from there reaches the
  // bottom in a few frames and then counts a still page as a smooth scroll. Start at the top, let the layout settle,
  // and prove the page moved every frame for the whole five seconds.
  async function scroll(page: Page) {
    await page.evaluate(() => window.scrollTo(0, 0));
    await page.evaluate(
      () =>
        new Promise<void>((done) => requestAnimationFrame(() => requestAnimationFrame(() => setTimeout(done, 100)))),
    );
    const outcome = await page.evaluate(
      () =>
        new Promise<{
          late: number;
          total: number;
          longTasks: number;
          moved: number;
          startY: number;
          endY: number;
        }>((done) => {
          let longTasks = 0;
          new PerformanceObserver((list) => {
            longTasks += list.getEntries().length;
          }).observe({ type: "longtask", buffered: false });
          const gaps: number[] = [];
          let moved = 0;
          let last = performance.now();
          const start = last;
          const startY = window.scrollY;
          const step = (now: number) => {
            gaps.push(now - last);
            last = now;
            const before = window.scrollY;
            window.scrollBy(0, 20);
            if (window.scrollY > before) {
              moved++;
            }
            if (now - start < 5000) {
              requestAnimationFrame(step);
            } else {
              done({
                late: gaps.filter((g) => g > 25).length,
                total: gaps.length,
                longTasks,
                moved,
                startY,
                endY: window.scrollY,
              });
            }
          };
          requestAnimationFrame(step);
        }),
    );
    expect(outcome.endY).toBeGreaterThan(outcome.startY);
    expect(outcome.moved).toBeGreaterThan(outcome.total * 0.9);
    return outcome;
  }

  test("console calls: 1000 rows scroll without dropped frames", async ({ browser }) => {
    // The list has no ceiling: its frames and long tasks are reported. Loading 1000 rows through the infinite scroll can
    // take more than the 120 s describe timeout on a loaded host, which would fail before it reports anything.
    test.slow();
    const page = await measuredPage(browser, phone, devices["Desktop Chrome"]);
    const state = await mockConsole(page, "worst");
    signedInAs(state);
    state.calls = Array.from({ length: 1000 }, (_, n) => ({
      ...(state.calls[n % state.calls.length] as (typeof state.calls)[number]),
      id: `00000000-0000-4000-8000-${String(n).padStart(12, "0")}`,
    }));
    await page.goto("/console/calls");
    await loadRows(page, page.getByRole("table", { name: "调用记录" }).getByRole("row"), 1000);
    expect(await page.evaluate(() => document.documentElement.scrollHeight)).toBeGreaterThan(10_000);
    const outcome = await scroll(page);
    record("late frames, console calls", outcome.late / outcome.total, budget.lateFrames);
    record("long tasks while scrolling, console calls", outcome.longTasks, budget.longTasks);
  });

  test("admin users: 1000 rows scroll without dropped frames", async ({ browser }) => {
    test.slow();
    const page = await measuredPage(browser, phone, devices["Desktop Chrome"]);
    const state = await mockAdmin(page, "worst");
    signedInAs(state, "mfa");
    state.users = Array.from({ length: 1000 }, (_, n) => {
      const user = state.users[n % state.users.length] as (typeof state.users)[number];
      const digits = String(n).padStart(26, "0");
      return { ...user, id: `user_${digits}`, account_id: `acct_${digits}`, email: `user${n}@example.com` };
    });
    await page.goto(adminUrl("users"));
    await pageHeading(page);
    await loadRows(page, page.getByRole("row"), 1000);
    expect(await page.evaluate(() => document.documentElement.scrollHeight)).toBeGreaterThan(10_000);
    const outcome = await scroll(page);
    record("late frames, admin users", outcome.late / outcome.total, budget.lateFrames);
    record("long tasks while scrolling, admin users", outcome.longTasks, budget.longTasks);
  });
});
