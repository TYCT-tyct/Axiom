import { availableParallelism } from "node:os";
import { defineConfig, devices } from "@playwright/test";
import { freeFirstPort } from "./scripts/free-port.ts";

// Unless AXIOM_WEB_PORT names the ports, this run takes free ones, so another worktree's checks can run at the same time.
// The variable also carries the choice to the test workers, which load this file again.
process.env.AXIOM_WEB_PORT ??= String(await freeFirstPort());
const { apps } = await import("./e2e/apps.ts");

// End-to-end tests run against the production builds served by the Go server that embeds them (internal/platform/webui),
// under its content security policy, so a page that breaks the policy fails here. `just web` builds and embeds first,
// stamping the console with a version.
const [website, , admin] = apps;
const address = (url: string) => `localhost:${new URL(url).port}`;

// A check is about one core of work (the browser, the page and the test process); CI gives this job eight cores.
// #297 measured the same suite on the 96-core host at a load average of 50 to 108: 48 workers made an
// axe scan take 5 to 6 s at the median and up to 32 s at worst, and 10 checks crossed Playwright's 30 s timeout;
// 8 workers took 1.3 to 1.4 s and 4.8 s, with every check passing and the whole run no slower (95 to 110 s against
// 85 to 127 s).
// Node already counts the container's CPU quota. Fill that share on CI; local runs leave half the machine for other work.
const capacity = availableParallelism();
const workers = Math.min(8, process.env.CI ? capacity : Math.max(1, Math.floor(capacity / 2)));

export default defineConfig({
  testDir: "e2e",
  workers,
  forbidOnly: Boolean(process.env.CI),
  reporter: process.env.CI ? "github" : "list",
  use: {
    baseURL: apps[1].url,
    locale: "zh-CN",
    trace: "retain-on-failure",
  },
  projects: [{ name: "chromium", use: { ...devices["Desktop Chrome"] } }],
  // Never reuse a server already listening: it may be another worktree's build, and this one's tests would pass or fail
  // on it. The ports are free when chosen; one taken since fails the run.
  webServer: {
    command: `go run ./internal/platform/webui/cmd/serveweb ${address(website.url)} ${address(admin.url)}`,
    cwd: "..",
    url: website.url,
    reuseExistingServer: false,
    timeout: 120_000, // the first run compiles the server
  },
});
