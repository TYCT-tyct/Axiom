import { readFileSync } from "node:fs";
import { mkdir, writeFile } from "node:fs/promises";
import { createRequire } from "node:module";
import { availableParallelism, cpus, loadavg } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

// The LCP of a page on a host that other jobs share, from the three page loads vitals.spec.ts takes, each between two
// loads of the reference page (docs/frontend.md「实测」).

export type Pair = { base: number; page: number };

export const median = (values: number[]) => [...values].sort((a, b) => a - b)[Math.floor(values.length / 2)] ?? 0;

// bracketed is one page's pair from the reference loads taken just before and just after it. The mean is the reference
// beside the page, so a host whose load rises through the sample raises both sides of the ratio; taking only the load
// before the page charged every pair the rise to the next load (#624).
export const bracketed = ({ before, after, page }: { before: number; after: number; page: number }): Pair => ({
  base: (before + after) / 2,
  page,
});

// A pair's page LCP, scaled to the reference page's LCP of the run the ceiling came from. The factor is capped at one:
// a host slower than that run is compensated, a faster one does not shrink the page's budget below the ceiling.
export const scaled = ({ base, page }: Pair, reference: number) => page * Math.min(1, reference / base);

// scaledLcp takes the median of the three scaled pairs, so a burst of another job's work that landed in one pair does not
// decide the gate; a page that got slower raises all three.
export const scaledLcp = (pairs: Pair[], reference: number) => median(pairs.map((pair) => scaled(pair, reference)));

// Run against a production console build on a quiet host:
// node web/scripts/vitals.ts <origin> <output-directory>
// The nine samples use the gate's toplevel trace. A separate tenth load records attribution: collecting a JS profile
// adds work, so its timings must not calibrate the backstop. CPU-throttled samples include Chromium's busy wait and
// are diagnostic only. The gate's phone viewport uses the phone network profile with cpu=1.
if (import.meta.main) {
  const [origin, output] = process.argv.slice(2);
  if (!origin || !output) throw new Error("usage: node web/scripts/vitals.ts <origin> <output-directory>");
  const { chromium, devices } = await import("@playwright/test");
  const { traced } = await import("../e2e/vitals-cpu.ts");
  const { runnerImport } = await import("vite");
  // The scene's transitive locale imports need the same TypeScript resolution as the test runner.
  const { module } = await runnerImport<typeof import("../e2e/console-scenes.ts")>(
    fileURLToPath(new URL("../e2e/console-scenes.ts", import.meta.url)),
    { configFile: false },
  );
  const scene = module.consoleSuite.scenes.find((scene) => scene.name === "overview");
  if (!scene) throw new Error("no overview scene");
  const library = readFileSync(
    join(dirname(createRequire(import.meta.url).resolve("web-vitals")), "web-vitals.iife.js"),
    "utf8",
  );
  const collector = `${library}; window.__vitals = {};
    for (const [name, on] of [["LCP", webVitals.onLCP], ["CLS", webVitals.onCLS], ["INP", webVitals.onINP]]) {
      on(metric => window.__vitals[name] = metric.value, { reportAllChanges: true, durationThreshold: 16 });
    }`;
  const profiles = [
    {
      name: "desktop",
      device: devices["Desktop Chrome"],
      rtt: 40,
      down: 10 * 1024 * 0.9 * 128,
      up: 10 * 1024 * 0.9 * 128,
      cpu: 1,
    },
    { name: "phone", device: devices["Pixel 5"], rtt: 150, down: 1.6 * 1024 * 0.9 * 128, up: 750 * 0.9 * 128, cpu: 1 },
    {
      name: "phone4x",
      device: devices["Pixel 5"],
      rtt: 150,
      down: 1.6 * 1024 * 0.9 * 128,
      up: 750 * 0.9 * 128,
      cpu: 4,
    },
  ];
  await mkdir(output, { recursive: true });
  const browser = await chromium.launch();
  const results = [];
  try {
    await writeFile(
      join(output, "environment.json"),
      JSON.stringify(
        {
          at: new Date().toISOString(),
          origin,
          chromium: browser.version(),
          node: process.version,
          cpu: cpus()[0]?.model,
          cores: availableParallelism(),
          load: loadavg(),
        },
        null,
        2,
      ),
    );
    for (const profile of profiles) {
      const samples: number[] = [];
      for (let i = 0; i < 10; i++) {
        const context = await browser.newContext({
          ...profile.device,
          baseURL: origin,
          locale: "zh-CN",
          timezoneId: "Asia/Shanghai",
        });
        try {
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
          await module.consoleSuite.prepare(page, scene);
          const tracing = await context.newCDPSession(page);
          const load = async () => {
            await scene.open(page);
            await page.waitForLoadState("networkidle");
            await page.evaluate(() => new Promise((done) => requestAnimationFrame(() => requestAnimationFrame(done))));
          };
          if (i < 9) {
            samples.push((await traced(tracing, load)).busyMs);
          } else {
            const events: unknown[] = [];
            tracing.on("Tracing.dataCollected", ({ value }) => events.push(...value));
            const complete = new Promise<void>((resolve) => tracing.once("Tracing.tracingComplete", () => resolve()));
            await tracing.send("Tracing.start", {
              categories: "toplevel,devtools.timeline,v8,blink.user_timing,disabled-by-default-v8.cpu_profiler",
              transferMode: "ReportEvents",
            });
            await load();
            await tracing.send("Tracing.end");
            await complete;
            await writeFile(join(output, `${profile.name}.trace.json`), JSON.stringify({ traceEvents: events }));
            const dom = await page.evaluate(() => ({
              nodes: document.querySelectorAll("*").length,
              overviewNodes: document.querySelector(".p-ov")?.querySelectorAll("*").length,
              svg: document.querySelector(".p-ov")?.querySelectorAll("svg *").length,
              animations: document.getAnimations().map((animation) => animation.playState),
              resources: performance.getEntriesByType("resource").map(({ name, duration }) => ({ name, duration })),
              vitals: (window as unknown as { __vitals: Record<string, number> }).__vitals,
            }));
            await writeFile(join(output, `${profile.name}.dom.json`), JSON.stringify(dom, null, 2));
          }
        } finally {
          await context.close();
        }
      }
      const result = {
        name: profile.name,
        samples,
        median: median(samples),
        min: Math.min(...samples),
        max: Math.max(...samples),
      };
      results.push(result);
      console.log(JSON.stringify(result));
      await writeFile(join(output, "samples.json"), JSON.stringify(results, null, 2));
    }
  } finally {
    await browser.close();
  }
}
