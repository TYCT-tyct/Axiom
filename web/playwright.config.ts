import { defineConfig, devices } from "@playwright/test";
import { freeFirstPort } from "./scripts/free-port.ts";

// Unless AXIOM_WEB_PORT names the ports, this run takes free ones, so another worktree's checks can run at the same time.
// The variable also carries the choice to the test workers, which load this file again, and to the preview servers.
process.env.AXIOM_WEB_PORT ??= String(await freeFirstPort());
const { apps } = await import("./e2e/apps.ts");

// End-to-end tests run against the production builds served by `vite preview` under the pages' content security
// policy (vite.shared.ts), so a page that breaks the policy fails here. `just web` builds first, stamped with a version.
export default defineConfig({
  testDir: "e2e",
  forbidOnly: Boolean(process.env.CI),
  reporter: process.env.CI ? "github" : "list",
  use: {
    baseURL: apps[1].url,
    locale: "zh-CN",
    trace: "retain-on-failure",
  },
  projects: [{ name: "chromium", use: { ...devices["Desktop Chrome"] } }],
  // Never reuse a server already listening: it may be another worktree's build, and this one's tests would pass or fail
  // on it. The ports are free when chosen; one taken since fails the run (the preview servers use strictPort).
  webServer: apps.map((app) => ({
    command: `pnpm --filter ${app.filter} preview`,
    url: app.url,
    reuseExistingServer: false,
  })),
});
